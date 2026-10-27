package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiws/orca/internal/agent"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
	"github.com/xiws/orca/internal/store"
	"github.com/xiws/orca/internal/tools"
)

type modelFunc func(context.Context, model.Request) (model.Response, error)
type agentFunc func(context.Context, domain.InvocationID, string) agent.Outcome

func (f agentFunc) Advance(ctx context.Context, id domain.InvocationID, resume string) agent.Outcome {
	return f(ctx, id, resume)
}

func (f modelFunc) Complete(ctx context.Context, req model.Request, _ model.Sink) (model.Response, error) {
	return f(ctx, req)
}
func answer(text string) model.Response {
	return model.Response{Complete: true, Message: model.Message{Role: "assistant", Content: text}, Usage: model.Usage{TotalTokens: 10}}
}
func call(name, args string) model.Response {
	r := answer("")
	r.Message.ToolCalls = []model.Call{{ID: "call-1", Name: name, Arguments: args}}
	return r
}

type harness struct {
	t         *testing.T
	db        *store.Store
	w         *Runner
	workspace string
}

func newHarness(t *testing.T, f modelFunc, agentLimit int, workflowLimit ...int) *harness {
	t.Helper()
	workspace := t.TempDir()
	db, err := store.Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := tools.New(workspace, db)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		f = func(context.Context, model.Request) (model.Response, error) {
			return model.Response{}, errors.New("unexpected model call")
		}
	}
	w := NewRunner(db, agent.NewRunner(db, f, gateway, agentLimit), workflowLimit...)
	t.Cleanup(func() { gateway.Close(); db.Close() })
	return &harness{t, db, w, workspace}
}
func (h *harness) apply(m domain.Mutation) {
	h.t.Helper()
	if err := h.db.Apply(context.Background(), m); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) run(mode, prompt string) *domain.Run {
	h.t.Helper()
	template, err := Mode(mode)
	if err != nil {
		h.t.Fatal(err)
	}
	session := domain.NewSession(h.workspace)
	task := domain.NewTask(prompt, prompt, session.ID)
	session.AppendUser(task.ID, prompt)
	r := &domain.Run{ID: domain.RunID(domain.NewID()), TaskID: task.ID, TaskVersion: task.Version, SessionID: session.ID, Workspace: h.workspace, Mode: mode, TemplateVersion: 1, Model: model.Ref{Provider: "fake", Model: "fake"}, Policy: template.Policy, Limits: domain.DefaultLimits(), State: domain.Queued, Nodes: template.Nodes(), Prompt: prompt, CreatedAt: time.Now().UnixNano()}
	r.RootRunID = r.ID
	h.apply(domain.Mutation{Sessions: []*domain.Session{session}, Tasks: []*domain.Task{task}, Runs: []*domain.Run{r}})
	return r
}
func (h *harness) load(id domain.RunID) *domain.Run {
	h.t.Helper()
	r, err := h.db.Run(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}
func (h *harness) drive(id domain.RunID) *domain.Run {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := h.w.Drive(ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}
func (h *harness) respond(input domain.InputRequest, approved bool) {
	h.t.Helper()
	input.State, input.Response, input.Approved = "answered", "confirmed", approved
	run := h.load(input.RunID)
	run.State, run.WaitingID = domain.Running, 0
	h.apply(domain.Mutation{Inputs: []*domain.InputRequest{&input}, Runs: []*domain.Run{run}})
}
func (h *harness) driveApproved(id domain.RunID) *domain.Run {
	h.t.Helper()
	for i := 0; i < 20; i++ {
		run := h.drive(id)
		if run.State != domain.Waiting {
			return run
		}
		inputs, err := h.w.PendingInputs(context.Background(), id)
		if err != nil || len(inputs) == 0 {
			h.t.Fatalf("waiting without input: %+v %v", run, err)
		}
		for _, input := range inputs {
			h.respond(input, true)
		}
	}
	h.t.Fatal("workflow did not terminate")
	return nil
}
func requestText(req model.Request) string {
	var text strings.Builder
	for _, m := range req.Messages {
		text.WriteString(m.Content)
		text.WriteByte('\n')
	}
	return text.String()
}

func TestFastPathsAndReadOnlyContracts(t *testing.T) {
	for _, tc := range []struct{ mode, result string }{
		{"ask", "answer"}, {"code", "done"},
		{"review", `{"summary":"reviewed","findings":[]}`},
		{"plan", `{"goal":"build","steps":[{"id":"a","description":"execute later","dependencies":[]}]}`},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			var calls atomic.Int32
			h := newHarness(t, func(_ context.Context, req model.Request) (model.Response, error) {
				calls.Add(1)
				if tc.mode != "code" {
					for _, tool := range req.Tools {
						if tool.Name != "read" && tool.Name != "request_input" {
							return model.Response{}, fmt.Errorf("unsafe tool: %s", tool.Name)
						}
					}
				}
				return answer(tc.result), nil
			}, 1)
			run := h.drive(h.run(tc.mode, "goal").ID)
			if run.State != domain.Succeeded || calls.Load() != 1 || len(run.Nodes) != 1 || run.Repairs != 0 {
				t.Fatalf("not a fast path: %+v / %d calls", run, calls.Load())
			}
			invs, _ := h.db.Invocations(context.Background(), run.ID)
			if len(invs) != 1 || len(invocationCalls(&invs[0])) != 0 {
				t.Fatal("unexpected execution")
			}
		})
	}
}

func TestRunningResumeFreezesPriorHistoryOnly(t *testing.T) {
	var observed string
	h := newHarness(t, func(_ context.Context, req model.Request) (model.Response, error) {
		observed = requestText(req)
		return answer("done"), nil
	}, 1)
	run := h.run("ask", "current")
	session, _ := h.db.Session(context.Background(), run.SessionID)
	priorTask := domain.NewTask("earlier", "earlier", session.ID)
	futureTask := domain.NewTask("future", "future", session.ID)
	template, _ := Mode("ask")
	prior := &domain.Run{ID: domain.RunID(domain.NewID()), TaskID: priorTask.ID, TaskVersion: 1, SessionID: session.ID, State: domain.Succeeded, CreatedAt: run.CreatedAt - 1}
	prior.RootRunID = prior.ID
	future := &domain.Run{ID: domain.RunID(domain.NewID()), TaskID: futureTask.ID, TaskVersion: 1, SessionID: session.ID, State: domain.Queued, Nodes: template.Nodes(), CreatedAt: run.CreatedAt + 1}
	future.RootRunID = future.ID
	session.AppendUser(priorTask.ID, "earlier")
	session.AppendAssistant(priorTask.ID, "confirmed earlier answer")
	session.AppendUser(futureTask.ID, "future secret")
	run.State = domain.Interrupted
	h.apply(domain.Mutation{Sessions: []*domain.Session{session}, Tasks: []*domain.Task{priorTask, futureTask}, Runs: []*domain.Run{prior, run, future}})
	run.State = domain.Running // app ResumeRun performs this before Drive
	h.apply(domain.Mutation{Runs: []*domain.Run{run}})
	result := h.drive(run.ID)
	if result.State != domain.Succeeded || !result.InputFrozen || !strings.Contains(observed, "confirmed earlier answer") || strings.Contains(observed, "future secret") || strings.Count(observed, "current") != 1 {
		t.Fatalf("bad freeze: %s / %+v", observed, result)
	}
}

func TestDeliberateIndependentCriticsAndStableJudge(t *testing.T) {
	var h *harness
	var critics atomic.Int32
	both := make(chan struct{})
	h = newHarness(t, func(ctx context.Context, req model.Request) (model.Response, error) {
		inv, _ := h.db.Invocation(ctx, domain.InvocationID(req.ThreadID))
		text := requestText(req)
		if strings.Contains(text, "late session message") {
			return model.Response{}, errors.New("frozen input changed")
		}
		switch inv.Role {
		case "responder":
			r := h.load(inv.RunID)
			s, _ := h.db.Session(ctx, r.SessionID)
			s.AppendUser(0, "late session message")
			if err := h.db.Apply(ctx, domain.Mutation{Sessions: []*domain.Session{s}}); err != nil {
				return model.Response{}, err
			}
			return answer("PROPOSAL"), nil
		case "critic-correctness", "critic-security":
			if !strings.Contains(text, "PROPOSAL") || strings.Contains(text, "Evidence from critic-") {
				return model.Response{}, errors.New("critic input not independent")
			}
			if critics.Add(1) == 2 {
				close(both)
			}
			select {
			case <-both:
			case <-ctx.Done():
				return model.Response{}, ctx.Err()
			}
			return answer("CRITIQUE " + inv.Role), nil
		case "judge":
			a, b := strings.Index(text, "CRITIQUE critic-correctness"), strings.Index(text, "CRITIQUE critic-security")
			if a < 0 || b <= a {
				return model.Response{}, errors.New("unstable judge ordering")
			}
			return answer("JUDGED"), nil
		}
		return model.Response{}, errors.New("unexpected role")
	}, 4)
	r := h.drive(h.run("deliberate", "original").ID)
	if r.State != domain.Succeeded || r.Result != "JUDGED" || r.Budget.Turns != 4 {
		t.Fatalf("deliberate: %+v", r)
	}
}

func TestAllParallelCriticInputsRemainQueryable(t *testing.T) {
	var h *harness
	h = newHarness(t, func(ctx context.Context, req model.Request) (model.Response, error) {
		inv, _ := h.db.Invocation(ctx, domain.InvocationID(req.ThreadID))
		if strings.HasPrefix(inv.Role, "critic-") && req.Messages[len(req.Messages)-1].Role != "tool" {
			return call("request_input", `{"prompt":"clarify critique"}`), nil
		}
		return answer(inv.Role), nil
	}, 4)
	r := h.run("deliberate", "goal")
	if paused := h.drive(r.ID); paused.State != domain.Waiting {
		t.Fatalf("not waiting: %+v", paused)
	}
	inputs, err := h.w.PendingInputs(context.Background(), r.ID)
	if err != nil || len(inputs) != 2 {
		t.Fatalf("lost inputs: %+v %v", inputs, err)
	}
	h.respond(inputs[0], true)
	if paused := h.drive(r.ID); paused.State != domain.Waiting {
		t.Fatalf("ignored second input: %+v", paused)
	}
	inputs, _ = h.w.PendingInputs(context.Background(), r.ID)
	if len(inputs) != 1 {
		t.Fatalf("pending: %+v", inputs)
	}
	h.respond(inputs[0], true)
	if done := h.drive(r.ID); done.State != domain.Succeeded {
		t.Fatalf("resume: %+v", done)
	}
}

func TestAgentInputDelegationAndDurableValidation(t *testing.T) {
	var h *harness
	h = newHarness(t, func(ctx context.Context, req model.Request) (model.Response, error) {
		inv, _ := h.db.Invocation(ctx, domain.InvocationID(req.ThreadID))
		run := h.load(inv.RunID)
		if run.ParentRunID != 0 {
			return answer("CHILD " + run.Prompt), nil
		}
		switch inv.Role {
		case "executor":
			calls := invocationCalls(inv)
			if len(calls) == 0 {
				return call("request_input", `{"prompt":"which target?"}`), nil
			}
			if len(calls) == 1 {
				return call("create_task", `{"task_target":[{"title":"a","description":"a"},{"title":"b","description":"b"}]}`), nil
			}
			if !strings.Contains(req.Messages[len(req.Messages)-1].Content, "CHILD") {
				return model.Response{}, errors.New("missing child evidence")
			}
			return answer("executed"), nil
		case "validator":
			if len(invocationCalls(inv)) == 0 {
				return call("bash", `{"content":"exit 0"}`), nil
			}
			return answer("validated"), nil
		case "verifier":
			records, valid, err := h.w.validationEvidence(ctx, run)
			if err != nil || !valid || len(records) != 1 {
				return model.Response{}, fmt.Errorf("missing validation: %v", err)
			}
			if !strings.Contains(requestText(req), records[0].Key) {
				return model.Response{}, errors.New("evidence IDs not supplied")
			}
			return answer(fmt.Sprintf(`{"status":"passed","summary":"verified","evidence":[%q]}`, records[0].Key+" actual acceptance check")), nil
		}
		return model.Response{}, errors.New("unexpected role")
	}, 4)
	r := h.driveApproved(h.run("agent", "goal").ID)
	if r.State != domain.Succeeded || r.Budget.Children != 2 || r.Budget.Turns != 8 || r.Budget.Reserved != 0 {
		t.Fatalf("agent: %+v", r)
	}
	runs, _ := h.db.Runs(context.Background())
	for _, child := range runs {
		if child.ParentRunID != 0 && (child.Mode != "code" || len(child.Nodes) != 1 || child.Repairs != 0) {
			t.Fatalf("child expanded: %+v", child)
		}
	}
}

func TestNestedDelegationOneSlotAndConcurrentRootBudget(t *testing.T) {
	for _, limit := range []int{1, 4} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			var h *harness
			h = newHarness(t, func(ctx context.Context, req model.Request) (model.Response, error) {
				inv, _ := h.db.Invocation(ctx, domain.InvocationID(req.ThreadID))
				r := h.load(inv.RunID)
				if req.Messages[len(req.Messages)-1].Role == "tool" {
					return answer("joined children"), nil
				}
				switch r.Depth {
				case 0:
					return call("create_task", `{"task_target":[{"title":"a","description":"branch a"},{"title":"b","description":"branch b"}]}`), nil
				case 1:
					return call("create_task", `{"task_target":[{"title":"leaf","description":"leaf"}]}`), nil
				default:
					return answer("LEAF"), nil
				}
			}, limit, limit)
			r := h.drive(h.run("code", "root").ID)
			if r.State != domain.Succeeded || r.Budget.Children != 4 || r.Budget.Turns != 8 || r.Budget.Tokens != 80 || r.Budget.Reserved != 0 {
				t.Fatalf("lost root budget or deadlock: %+v", r)
			}
		})
	}
}

func TestDefaultFourIndependentSubgoals(t *testing.T) {
	var h *harness
	var active, peak atomic.Int32
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	h = newHarness(t, func(ctx context.Context, req model.Request) (model.Response, error) {
		inv, _ := h.db.Invocation(ctx, domain.InvocationID(req.ThreadID))
		r := h.load(inv.RunID)
		if r.Depth == 0 {
			if req.Messages[len(req.Messages)-1].Role == "tool" {
				return answer("joined"), nil
			}
			return call("create_task", `{"task_target":[{"title":"1","description":"1"},{"title":"2","description":"2"},{"title":"3","description":"3"},{"title":"4","description":"4"},{"title":"5","description":"5"},{"title":"6","description":"6"}]}`), nil
		}
		return answer("child"), nil
	}, 8)
	// Measure workflow slots independently of the agent's per-provider limit.
	underlying := h.w.agent
	h.w.agent = agentFunc(func(ctx context.Context, id domain.InvocationID, resume string) agent.Outcome {
		inv, err := h.db.Invocation(ctx, id)
		if err != nil {
			return agent.Outcome{Kind: "failed", Err: err}
		}
		if h.load(inv.RunID).Depth > 0 {
			n := active.Add(1)
			defer active.Add(-1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return agent.Outcome{Kind: "failed", Err: ctx.Err()}
			}
		}
		return underlying.Advance(ctx, id, resume)
	})
	r := h.run("code", "root")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		r   *domain.Run
		err error
	}
	done := make(chan result, 1)
	go func() { r, err := h.w.Drive(ctx, r.ID); done <- result{r, err} }()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("subgoals did not overlap")
		}
	}
	if peak.Load() != 4 {
		t.Fatalf("default bound: %d", peak.Load())
	}
	close(release)
	out := <-done
	if out.err != nil || out.r.State != domain.Succeeded || peak.Load() != 4 || out.r.Budget.Children != 6 || out.r.Budget.Turns != 8 {
		t.Fatalf("parallel tree: %+v %v peak=%d", out.r, out.err, peak.Load())
	}
}

func TestTerminalApprovalAndNoReplay(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprint(approved), func(t *testing.T) {
			h := newHarness(t, func(_ context.Context, req model.Request) (model.Response, error) {
				if req.Messages[len(req.Messages)-1].Role == "tool" {
					return answer("reported outcome"), nil
				}
				return call("bash", `{"content":"printf x >> marker"}`), nil
			}, 1)
			r := h.run("terminal", "operation")
			if paused := h.drive(r.ID); paused.State != domain.Waiting {
				t.Fatalf("no approval: %+v", paused)
			}
			path := filepath.Join(h.workspace, "marker")
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("executed before approval: %v", err)
			}
			inputs, _ := h.w.PendingInputs(context.Background(), r.ID)
			if len(inputs) != 1 || inputs[0].Kind != "approval" {
				t.Fatalf("inputs: %+v", inputs)
			}
			h.respond(inputs[0], approved)
			if done := h.drive(r.ID); done.State != domain.Succeeded {
				t.Fatalf("terminal: %+v", done)
			}
			h.drive(r.ID)
			data, err := os.ReadFile(path)
			if approved && (err != nil || string(data) != "x") {
				t.Fatalf("command replayed/lost: %q %v", data, err)
			}
			if !approved && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("denied command ran: %q %v", data, err)
			}
		})
	}
}

func TestTestModeActualFailureFiniteRepair(t *testing.T) {
	var h *harness
	var repairs atomic.Int32
	h = newHarness(t, func(ctx context.Context, req model.Request) (model.Response, error) {
		inv, _ := h.db.Invocation(ctx, domain.InvocationID(req.ThreadID))
		switch inv.Role {
		case "executor":
			return answer("implemented"), nil
		case "repair":
			repairs.Add(1)
			return answer("attempted repair"), nil
		case "validator":
			if len(invocationCalls(inv)) == 0 {
				return call("bash", `{"content":"exit 7"}`), nil
			}
			return answer("check failed"), nil
		case "verifier":
			records, valid, err := h.w.validationEvidence(ctx, h.load(inv.RunID))
			if err != nil || valid || len(records) != 1 || !strings.Contains(records[0].Result, `"exit_code":7`) {
				return model.Response{}, fmt.Errorf("actual failed check missing: %+v %v", records, err)
			}
			return answer(fmt.Sprintf(`{"status":"failed","summary":"check failed","evidence":[%q]}`, records[0].Key)), nil
		}
		return model.Response{}, errors.New("unexpected role")
	}, 2)
	r := h.run("test", "goal")
	r.Limits.MaxRepairs = 1
	h.apply(domain.Mutation{Runs: []*domain.Run{r}})
	done := h.driveApproved(r.ID)
	if done.State != domain.Failed || done.Repairs != 1 || repairs.Load() != 1 || len(done.Nodes) != 6 || !strings.HasPrefix(done.Error, "failed:") {
		t.Fatalf("unbounded or false success: %+v repairs=%d", done, repairs.Load())
	}
}

// Workflows use the real CAS store while failures are injected only at their
// boundary. Agent model/tool checkpoints continue to be durable.
type faultStore struct {
	ExecutionStore
	apply func(context.Context, domain.Mutation) error
}

func (s *faultStore) Apply(ctx context.Context, m domain.Mutation) error { return s.apply(ctx, m) }

func TestCheckpointFailureReplaysOnlyDurableOutcome(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, func(context.Context, model.Request) (model.Response, error) {
		calls.Add(1)
		return answer("durable"), nil
	}, 1)
	r := h.run("ask", "goal")
	failed := false
	h.w.store = &faultStore{h.db, func(ctx context.Context, m domain.Mutation) error {
		if !failed && len(m.Artifacts) > 0 {
			failed = true
			return errors.New("disk unavailable")
		}
		return h.db.Apply(ctx, m)
	}}
	paused := h.drive(r.ID)
	if !failed || paused.State != domain.Interrupted || calls.Load() != 1 {
		t.Fatalf("checkpoint not interrupted: %+v", paused)
	}
	paused.State = domain.Running
	h.apply(domain.Mutation{Runs: []*domain.Run{paused}})
	if done := h.drive(r.ID); done.State != domain.Succeeded || calls.Load() != 1 {
		t.Fatalf("model replayed: %+v calls=%d", done, calls.Load())
	}
}

func TestCheckpointErrorsWrapAndCancelledCASNeverWins(t *testing.T) {
	h := newHarness(t, func(context.Context, model.Request) (model.Response, error) { return answer("late success"), nil }, 1)
	r := h.run("ask", "goal")
	fault := errors.New("checkpoint failure")
	h.w.store = &faultStore{h.db, func(context.Context, domain.Mutation) error { return fault }}
	_, err := h.w.Drive(context.Background(), r.ID)
	if !errors.Is(err, domain.ErrCheckpoint) || !errors.Is(err, fault) {
		t.Fatalf("unwrapped commit: %v", err)
	}
	cancelled := false
	h.w.store = &faultStore{h.db, func(ctx context.Context, m domain.Mutation) error {
		if !cancelled && len(m.Artifacts) > 0 {
			cancelled = true
			latest := h.load(r.ID)
			latest.State = domain.Cancelling
			h.apply(domain.Mutation{Runs: []*domain.Run{latest}})
			return domain.ErrConflict
		}
		return h.db.Apply(ctx, m)
	}}
	done := h.drive(r.ID)
	if !cancelled || done.State != domain.Cancelled || done.Nodes[0].State == "completed" {
		t.Fatalf("cancellation overwritten: %+v", done)
	}
	artifacts, _ := h.db.Artifacts(context.Background(), r.ID)
	if len(artifacts) != 0 {
		t.Fatalf("late success committed: %+v", artifacts)
	}
}

func TestWaitingCheckpointFailurePreservesInput(t *testing.T) {
	h := newHarness(t, func(_ context.Context, req model.Request) (model.Response, error) {
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			return answer("answered"), nil
		}
		return call("request_input", `{"prompt":"target?"}`), nil
	}, 1)
	r := h.run("ask", "goal")
	failed := false
	h.w.store = &faultStore{h.db, func(ctx context.Context, m domain.Mutation) error {
		for _, event := range m.Events {
			if event.Kind == "waiting" && !failed {
				failed = true
				return errors.New("lost workflow waiting checkpoint")
			}
		}
		return h.db.Apply(ctx, m)
	}}
	if paused := h.drive(r.ID); paused.State != domain.Interrupted {
		t.Fatalf("not interrupted: %+v", paused)
	}
	inputs, err := h.w.PendingInputs(context.Background(), r.ID)
	if err != nil || len(inputs) != 1 {
		t.Fatalf("orphaned input: %+v %v", inputs, err)
	}
	paused := h.load(r.ID)
	paused.State = domain.Running
	h.apply(domain.Mutation{Runs: []*domain.Run{paused}})
	if paused := h.drive(r.ID); paused.State != domain.Waiting || paused.WaitingID != inputs[0].ID {
		t.Fatalf("waiting not restored: %+v", paused)
	}
	h.respond(inputs[0], true)
	if done := h.drive(r.ID); done.State != domain.Succeeded {
		t.Fatalf("resume: %+v", done)
	}
}

func TestChildFailureAndUnknownReachRoot(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			var h *harness
			h = newHarness(t, func(ctx context.Context, req model.Request) (model.Response, error) {
				inv, _ := h.db.Invocation(ctx, domain.InvocationID(req.ThreadID))
				r := h.load(inv.RunID)
				if r.ParentRunID != 0 {
					if unknown {
						return model.Response{}, model.ErrUnknown
					}
					return model.Response{}, errors.New("child failed")
				}
				if req.Messages[len(req.Messages)-1].Role == "tool" {
					return answer("claimed success anyway"), nil
				}
				return call("create_task", `{"task_target":[{"title":"child","description":"child"}]}`), nil
			}, 1)
			r := h.drive(h.run("code", "root").ID)
			want := domain.Failed
			if unknown {
				want = domain.Reconciling
			}
			if r.State != want {
				t.Fatalf("child problem hidden: %+v", r)
			}
			s, _ := h.db.Session(context.Background(), r.SessionID)
			if len(s.Messages) != 1 {
				t.Fatalf("false session success: %+v", s.Messages)
			}
		})
	}
}

func TestChildWaitingInputsAndCancellation(t *testing.T) {
	var h *harness
	h = newHarness(t, func(ctx context.Context, req model.Request) (model.Response, error) {
		inv, _ := h.db.Invocation(ctx, domain.InvocationID(req.ThreadID))
		r := h.load(inv.RunID)
		if r.ParentRunID == 0 {
			return call("create_task", `{"task_target":[{"title":"a","description":"a"},{"title":"b","description":"b"}]}`), nil
		}
		return call("request_input", `{"prompt":"child input?"}`), nil
	}, 4)
	r := h.drive(h.run("code", "root").ID)
	inputs, err := h.w.PendingInputs(context.Background(), r.ID)
	if r.State != domain.Waiting || err != nil || len(inputs) != 2 {
		t.Fatalf("child waits lost: %+v %+v %v", r, inputs, err)
	}
	child := h.load(inputs[0].RunID)
	child.State = domain.Cancelling
	h.apply(domain.Mutation{Runs: []*domain.Run{child}})
	r = h.drive(r.ID)
	if r.State != domain.Cancelled {
		t.Fatalf("cancelled child left root waiting: %+v", r)
	}
	runs, _ := h.db.Runs(context.Background())
	for _, run := range runs {
		if run.State != domain.Cancelled {
			t.Fatalf("tree not cancelled: %+v", run)
		}
	}
}

func TestConcurrentDriveRejected(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	h := newHarness(t, func(ctx context.Context, _ model.Request) (model.Response, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
		return answer("done"), nil
	}, 1)
	r := h.run("ask", "goal")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.drive(r.ID) }()
	<-started
	if _, err := h.w.Drive(context.Background(), r.ID); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate writer allowed: %v", err)
	}
	close(release)
	wg.Wait()
}

func TestVerificationJSONRemainsTruthfulAfterDowngrade(t *testing.T) {
	h := newHarness(t, func(_ context.Context, req model.Request) (model.Response, error) {
		if strings.Contains(req.Messages[0].Content, "Independently evaluate") {
			return answer(`{"status":"passed","summary":"claimed","evidence":["invented"]}`), nil
		}
		return answer("no actual checks"), nil
	}, 1)
	r := h.drive(h.run("test", "goal").ID)
	var v agent.Verification
	if err := json.Unmarshal([]byte(r.Result), &v); err != nil {
		t.Fatal(err)
	}
	if r.State != domain.Failed || v.Status != "inconclusive" || r.Repairs != 0 || len(v.Evidence) != 0 {
		t.Fatalf("false passed result: %+v", r)
	}
}

func TestWorkflowCommitFaultsAlwaysInterrupt(t *testing.T) {
	for _, stage := range []string{"freeze", "invocation", "delegation", "repair", "finish"} {
		t.Run(stage, func(t *testing.T) {
			var h *harness
			h = newHarness(t, func(ctx context.Context, req model.Request) (model.Response, error) {
				inv, err := h.db.Invocation(ctx, domain.InvocationID(req.ThreadID))
				if err != nil {
					return model.Response{}, err
				}
				if stage == "delegation" {
					return call("create_task", `{"task_target":[{"title":"child","description":"child"}]}`), nil
				}
				if inv.Role == "verifier" {
					return answer(`{"status":"failed","summary":"acceptance missing","evidence":[]}`), nil
				}
				return answer("done"), nil
			}, 1)
			mode := "code"
			if stage == "repair" {
				mode = "test"
			}
			r := h.run(mode, "goal")
			injected := false
			h.w.store = &faultStore{h.db, func(ctx context.Context, m domain.Mutation) error {
				match := false
				switch stage {
				case "freeze":
					match = len(m.Runs) > 0 && m.Runs[0].InputFrozen && m.Runs[0].State == domain.Queued
				case "invocation":
					match = len(m.Invocations) > 0
				case "delegation":
					match = len(m.Delegations) > 0
				case "repair":
					match = len(m.Runs) > 0 && m.Runs[0].Repairs > 0
				case "finish":
					match = len(m.Sessions) > 0
				}
				if match && !injected {
					injected = true
					return fmt.Errorf("%s commit unavailable", stage)
				}
				return h.db.Apply(ctx, m)
			}}
			paused := h.drive(r.ID)
			if !injected || paused.State != domain.Interrupted || !strings.Contains(paused.Error, domain.ErrCheckpoint.Error()) {
				t.Fatalf("lost checkpoint classification: %+v", paused)
			}
			if paused.Repairs != 0 || paused.Budget.Children != 0 {
				t.Fatalf("partial budget commit: %+v", paused)
			}
			s, _ := h.db.Session(context.Background(), paused.SessionID)
			if len(s.Messages) != 1 {
				t.Fatal("published uncommitted final answer")
			}
		})
	}
}

func TestFinalSessionCASCannotOverwriteCancellation(t *testing.T) {
	h := newHarness(t, func(context.Context, model.Request) (model.Response, error) { return answer("late final"), nil }, 1)
	r := h.run("ask", "goal")
	cancelled := false
	h.w.store = &faultStore{h.db, func(ctx context.Context, m domain.Mutation) error {
		if len(m.Sessions) > 0 && !cancelled {
			cancelled = true
			latest := h.load(r.ID)
			latest.State = domain.Cancelling
			h.apply(domain.Mutation{Runs: []*domain.Run{latest}})
		}
		return h.db.Apply(ctx, m) // Real transactional CAS rejects the entire batch.
	}}
	done := h.drive(r.ID)
	if !cancelled || done.State != domain.Cancelled {
		t.Fatalf("cancelled final overwritten: %+v", done)
	}
	s, _ := h.db.Session(context.Background(), r.SessionID)
	if len(s.Messages) != 1 {
		t.Fatalf("session half committed: %+v", s.Messages)
	}
}

func TestCancelInFlightChildKeepsUnknownTree(t *testing.T) {
	var h *harness
	started := make(chan domain.RunID, 1)
	h = newHarness(t, func(ctx context.Context, req model.Request) (model.Response, error) {
		inv, err := h.db.Invocation(ctx, domain.InvocationID(req.ThreadID))
		if err != nil {
			return model.Response{}, err
		}
		run := h.load(inv.RunID)
		if run.ParentRunID == 0 {
			return call("create_task", `{"task_target":[{"title":"child","description":"child"}]}`), nil
		}
		started <- run.ID
		<-ctx.Done()
		return model.Response{}, model.ErrUnknown
	}, 1)
	r := h.run("code", "root")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := h.w.Drive(ctx, r.ID); done <- err }()
	var childID domain.RunID
	select {
	case childID = <-started:
	case <-ctx.Done():
		t.Fatal("child never started")
	}
	child := h.load(childID)
	child.State = domain.Cancelling
	h.apply(domain.Mutation{Runs: []*domain.Run{child}})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if h.load(r.ID).State != domain.Reconciling || h.load(childID).State != domain.Reconciling {
		t.Fatal("cancel hid unknown child request")
	}
}
