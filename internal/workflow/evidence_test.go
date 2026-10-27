package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xiws/orca/internal/agent"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
	"github.com/xiws/orca/internal/store"
	"github.com/xiws/orca/internal/tools"
)

func (h *harness) validation(run *domain.Run, results []string) (*domain.Invocation, []*domain.ToolExecution) {
	h.t.Helper()
	inv := &domain.Invocation{ID: domain.InvocationID(domain.NewID()), RunID: run.ID, NodeID: "validator", Role: "validator", Phase: "completed", Thread: domain.Thread{ContextVersion: 1}}
	var calls []model.Call
	for i := range results {
		calls = append(calls, model.Call{ID: fmt.Sprintf("check-%d", i), Name: "bash", Arguments: `{"content":"exit 0"}`})
	}
	inv.Thread.Messages = []model.Message{{Role: "assistant", ToolCalls: calls}}
	var records []*domain.ToolExecution
	for i, result := range results {
		inv.Thread.Messages = append(inv.Thread.Messages, model.Message{Role: "tool", ToolCallID: calls[i].ID, Content: result})
		records = append(records, &domain.ToolExecution{Key: fmt.Sprintf("%d:1:%s", inv.ID, calls[i].ID), RunID: run.ID, InvocationID: inv.ID, Name: "bash", Arguments: calls[i].Arguments, Workspace: run.Workspace, State: "completed", Result: result})
	}
	run.State = domain.Running
	run.Nodes = []domain.Node{{ID: "validator", Role: "validator", InvocationID: inv.ID, State: "completed", Result: "validation"}, {ID: "verifier", Role: "verifier", State: "completed", Result: `{"status":"passed","summary":"claimed","evidence":["check-0"]}`}}
	return inv, records
}

func TestValidationRequiresEntireLatestDurableShellSet(t *testing.T) {
	ok := `{"ok":true,"exit_code":0}`
	cases := []struct {
		name    string
		results []string
		alter   func(*domain.Run, *domain.Invocation, *[]*domain.ToolExecution)
		want    bool
	}{
		{name: "all passed", results: []string{ok, ok}, want: true},
		{name: "no shell"},
		{name: "success cannot hide failure", results: []string{ok, `{"ok":false,"exit_code":1}`}},
		{name: "missing explicit exit", results: []string{`{"ok":true}`}},
		{name: "missing ok", results: []string{`{"exit_code":0}`}},
		{name: "null exit", results: []string{`{"ok":true,"exit_code":null}`}},
		{name: "null ok", results: []string{`{"ok":null,"exit_code":0}`}},
		{name: "nonzero exit", results: []string{`{"ok":true,"exit_code":2}`}},
		{name: "malformed result", results: []string{"not JSON"}},
		{name: "missing ledger", results: []string{ok}, alter: func(_ *domain.Run, _ *domain.Invocation, records *[]*domain.ToolExecution) { *records = nil }},
		{name: "unknown ledger", results: []string{ok}, alter: func(_ *domain.Run, _ *domain.Invocation, records *[]*domain.ToolExecution) {
			(*records)[0].State = "unknown"
		}},
		{name: "prepared ledger", results: []string{ok}, alter: func(_ *domain.Run, _ *domain.Invocation, records *[]*domain.ToolExecution) {
			(*records)[0].State = "prepared"
		}},
		{name: "nonzero ledger exit", results: []string{ok}, alter: func(_ *domain.Run, _ *domain.Invocation, records *[]*domain.ToolExecution) {
			(*records)[0].ExitCode = 2
		}},
		{name: "different workspace", results: []string{ok}, alter: func(_ *domain.Run, _ *domain.Invocation, records *[]*domain.ToolExecution) {
			(*records)[0].Workspace += "/elsewhere"
		}},
		{name: "wrong ledger tool", results: []string{ok}, alter: func(_ *domain.Run, _ *domain.Invocation, records *[]*domain.ToolExecution) {
			(*records)[0].Name = "read"
		}},
		{name: "transcript forged", results: []string{ok}, alter: func(_ *domain.Run, _ *domain.Invocation, records *[]*domain.ToolExecution) {
			(*records)[0].Result = `{"ok":false,"exit_code":1}`
		}},
		{name: "missing tool response", results: []string{ok}, alter: func(_ *domain.Run, inv *domain.Invocation, _ *[]*domain.ToolExecution) {
			inv.Thread.Messages = inv.Thread.Messages[:1]
		}},
		{name: "duplicate tool response", results: []string{ok}, alter: func(_ *domain.Run, inv *domain.Invocation, _ *[]*domain.ToolExecution) {
			inv.Thread.Messages = append(inv.Thread.Messages, inv.Thread.Messages[1])
		}},
		{name: "unfinished invocation", results: []string{ok}, alter: func(_ *domain.Run, inv *domain.Invocation, _ *[]*domain.ToolExecution) { inv.Phase = "tools" }},
		{name: "new validator overrides old", results: []string{ok}, alter: func(run *domain.Run, _ *domain.Invocation, _ *[]*domain.ToolExecution) {
			run.Nodes = append(run.Nodes, domain.Node{ID: "new-validator", Role: "validator", State: "pending"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, 1)
			r := h.run("test", "goal")
			inv, records := h.validation(r, tc.results)
			if tc.alter != nil {
				tc.alter(r, inv, &records)
			}
			h.apply(domain.Mutation{Runs: []*domain.Run{r}, Invocations: []*domain.Invocation{inv}, Tools: records})
			valid, err := h.w.hasValidation(context.Background(), r)
			if err != nil || valid != tc.want {
				t.Fatalf("valid=%v want=%v err=%v", valid, tc.want, err)
			}
		})
	}
}

func TestRebasedValidationUsesExactExecutionKeys(t *testing.T) {
	h := newHarness(t, nil, 1)
	run := h.run("test", "goal")
	result := `{"ok":true,"exit_code":0}`
	inv, records := h.validation(run, []string{result})
	h.apply(domain.Mutation{Runs: []*domain.Run{run}, Invocations: []*domain.Invocation{inv}, Tools: records})
	previousKey := records[0].Key
	for cycle := 0; cycle < 2; cycle++ {
		offset := inv.Thread.SequenceOffset + len(inv.Thread.Messages)
		inv.Phase = "model"
		call := model.Call{ID: "check-0", Name: "bash", Arguments: `{"content":"exit 0"}`}
		messages := []model.Message{
			{Role: "user", Content: "new validation context"},
			{Role: "assistant", ToolCalls: []model.Call{call}},
			{Role: "tool", ToolCallID: call.ID, Content: result},
			{Role: "assistant", ToolCalls: []model.Call{call}},
			{Role: "tool", ToolCallID: call.ID, Content: result},
		}
		if err := agent.Rebase(inv, messages); err != nil {
			t.Fatal(err)
		}
		inv.Phase, inv.AssistantSequence = "completed", offset+4
		h.apply(domain.Mutation{Invocations: []*domain.Invocation{inv}})
		var err error
		inv, err = h.db.Invocation(context.Background(), inv.ID)
		if err != nil {
			t.Fatal(err)
		}
		calls := invocationCalls(inv)
		if len(calls) != 2 || calls[0].key != fmt.Sprintf("%d:%d:%s", inv.ID, offset+2, call.ID) || calls[1].key != fmt.Sprintf("%d:%d:%s", inv.ID, offset+4, call.ID) {
			t.Fatalf("incorrect rebased call keys: %+v", calls)
		}
		if valid, err := h.w.hasValidation(context.Background(), run); err != nil || valid {
			t.Fatalf("old ledger reused after rebase: %v %v", valid, err)
		}
		var fresh []*domain.ToolExecution
		for _, recorded := range calls {
			if recorded.results != 1 || recorded.result != result {
				t.Fatalf("repeated call result misbound: %+v", recorded)
			}
			fresh = append(fresh, &domain.ToolExecution{Key: recorded.key, RunID: run.ID, InvocationID: inv.ID, Name: call.Name, Arguments: call.Arguments, Workspace: run.Workspace, State: "completed", Result: result})
		}
		h.apply(domain.Mutation{Tools: fresh})
		evidence, valid, err := h.w.validationEvidence(context.Background(), run)
		if err != nil || !valid || len(evidence) != 2 {
			t.Fatalf("fresh execution evidence missing: %+v %v %v", evidence, valid, err)
		}
		if validEvidence(agent.Verification{Status: "passed", Evidence: []string{previousKey}}, evidence) || validEvidence(agent.Verification{Status: "passed", Evidence: []string{call.ID}}, evidence) {
			t.Fatal("stale or ambiguous evidence accepted")
		}
		if !validEvidence(agent.Verification{Status: "passed", Evidence: []string{calls[0].key, calls[1].key}}, evidence) {
			t.Fatal("exact rebased execution keys rejected")
		}
		previousKey = calls[1].key
	}
}

type runnerBoundaryStore struct {
	*store.Store
	stage  string
	armed  bool
	faults int
}

func (s *runnerBoundaryStore) Run(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	if s.stage == "settle read" && s.armed {
		s.armed = false
		s.faults++
		return nil, errors.New("settle root unavailable")
	}
	return s.Store.Run(ctx, id)
}

func (s *runnerBoundaryStore) Apply(ctx context.Context, m domain.Mutation) error {
	if s.stage == "settle CAS" && len(m.Invocations) > 0 && m.Invocations[0].Phase == "completed" {
		root, err := s.Store.Run(ctx, m.Invocations[0].RunID)
		if err != nil {
			return err
		}
		if err := s.Store.Apply(ctx, domain.Mutation{Runs: []*domain.Run{root}}); err != nil {
			return err
		}
		s.faults++
	}
	if s.stage == "pre-tool" && len(m.Tools) > 0 && m.Tools[0].State == "prepared" && s.faults == 0 {
		s.faults++
		return errors.New("tool prepare unavailable")
	}
	return s.Store.Apply(ctx, m)
}

func TestRunnerBoundaryFailuresDoNotTerminalizeWorkflow(t *testing.T) {
	for _, stage := range []string{"settle read", "settle CAS", "pre-tool"} {
		t.Run(stage, func(t *testing.T) {
			h := newHarness(t, nil, 1)
			fs := &runnerBoundaryStore{Store: h.db, stage: stage}
			gateway, err := tools.New(h.workspace, fs)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { gateway.Close() })
			sends := 0
			client := modelFunc(func(context.Context, model.Request) (model.Response, error) {
				sends++
				fs.armed = true
				if stage == "pre-tool" && sends == 1 {
					return call("read", `{"filename":"missing.txt"}`), nil
				}
				return answer("done"), nil
			})
			h.w.agent = agent.NewRunner(fs, client, gateway, 1)
			run := h.drive(h.run("ask", "goal").ID)
			want := domain.Reconciling
			if stage == "pre-tool" {
				want = domain.Interrupted
			}
			if run.State != want || sends != 1 || fs.faults == 0 || !strings.Contains(run.Error, domain.ErrCheckpoint.Error()) || run.Nodes[0].State == "failed" {
				t.Fatalf("checkpoint failure terminalized workflow: %+v sends=%d faults=%d", run, sends, fs.faults)
			}
			if stage == "settle CAS" && fs.faults != 8 {
				t.Fatalf("CAS retry count=%d", fs.faults)
			}
			inv, err := h.db.Invocation(context.Background(), run.Nodes[0].InvocationID)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "pre-tool" {
				if inv.Phase != "tools" || strings.Contains(run.Error, domain.ErrUnknown.Error()) {
					t.Fatalf("safe pre-tool failure poisoned as unknown: %+v", run)
				}
				run.State = domain.Running
				h.apply(domain.Mutation{Runs: []*domain.Run{run}})
				if done := h.drive(run.ID); done.State != domain.Succeeded || sends != 2 {
					t.Fatalf("safe tool retry failed: %+v sends=%d", done, sends)
				}
			} else {
				if inv.Phase != "model_started" || inv.Reservation == 0 {
					t.Fatalf("uncertain model intent lost: %+v", inv)
				}
				if resumed := h.drive(run.ID); resumed.State != domain.Reconciling || sends != 1 {
					t.Fatalf("uncertain model replayed: %+v sends=%d", resumed, sends)
				}
			}
		})
	}
}

func TestVerifierEvidenceIdentityNotFreeText(t *testing.T) {
	records := []validationRecord{{Key: "100:2:a", CallID: "a", Name: "bash", Successful: true}, {Key: "100:4:shared", CallID: "shared", Name: "bash", Successful: true}, {Key: "100:6:shared", CallID: "shared", Name: "bash", Successful: true}}
	for _, tc := range []struct {
		citation string
		want     bool
	}{
		{"100:2:a", true}, {"100:2:a actual exit=0", true}, {"a actual check", true}, {"100:4:shared", true},
		{"shared", false}, {"100:2:a-fabricated", false}, {"invented", false}, {"", false}, {"tests passed", false},
	} {
		t.Run(tc.citation, func(t *testing.T) {
			if got := validEvidence(agent.Verification{Status: "passed", Evidence: []string{tc.citation}}, records); got != tc.want {
				t.Fatalf("citation %q valid=%v", tc.citation, got)
			}
		})
	}
	if validEvidence(agent.Verification{Status: "failed", Evidence: []string{"imaginary"}}, records) {
		t.Fatal("failed verdict accepted fabricated evidence")
	}
	if validEvidence(agent.Verification{Status: "passed", Evidence: []string{"a", "fake"}}, records) {
		t.Fatal("valid ID hid fabricated second ID")
	}
	if !validEvidence(agent.Verification{Status: "inconclusive"}, nil) {
		t.Fatal("inconclusive should not require nonexistent evidence")
	}
}

func TestOnlyLatestValidatorEvidenceMayPass(t *testing.T) {
	h := newHarness(t, nil, 1)
	r := h.run("test", "goal")
	old, oldTools := h.validation(r, []string{`{"ok":true,"exit_code":0}`})
	newInv, newTools := h.validation(r, []string{`{"ok":false,"exit_code":1}`})
	r.Nodes = append([]domain.Node{{ID: "old-validator", Role: "validator", State: "completed", InvocationID: old.ID}}, r.Nodes...)
	r.Nodes[len(r.Nodes)-1].Result = fmt.Sprintf(`{"status":"passed","summary":"claims old success","evidence":[%q]}`, oldTools[0].Key)
	h.apply(domain.Mutation{Runs: []*domain.Run{r}, Invocations: []*domain.Invocation{old, newInv}, Tools: append(oldTools, newTools...)})
	if err := h.w.finish(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.State != domain.Failed || !strings.Contains(r.Result, `"status":"inconclusive"`) || r.Repairs != 0 {
		t.Fatalf("stale validation passed: %+v", r)
	}
}

func (h *harness) child(root *domain.Run, mode string) *domain.Run {
	h.t.Helper()
	task := domain.NewTask("child", "child", root.SessionID)
	task.ParentTaskID = root.TaskID
	template, _ := Mode(mode)
	run := &domain.Run{ID: domain.RunID(domain.NewID()), TaskID: task.ID, TaskVersion: task.Version, SessionID: root.SessionID, RootRunID: root.ID, ParentRunID: root.ID, Depth: 1, Workspace: root.Workspace, Mode: mode, TemplateVersion: 1, Policy: template.Policy, Limits: root.Limits, State: domain.Queued, Nodes: template.Nodes(), Prompt: "child", CreatedAt: root.CreatedAt + 1}
	h.apply(domain.Mutation{Tasks: []*domain.Task{task}, Runs: []*domain.Run{run}})
	return run
}

func TestRepairsConsumeRootBudgetAcrossChildren(t *testing.T) {
	h := newHarness(t, nil, 1)
	root := h.run("test", "root")
	root.State, root.Limits.MaxRepairs = domain.Running, 1
	h.apply(domain.Mutation{Runs: []*domain.Run{root}})
	first, second := h.child(root, "test"), h.child(root, "test")
	for i, child := range []*domain.Run{first, second} {
		inv, records := h.validation(child, []string{`{"ok":false,"exit_code":7}`})
		child.Nodes[len(child.Nodes)-1].Result = fmt.Sprintf(`{"status":"failed","summary":"failed check","evidence":[%q]}`, records[0].Key)
		h.apply(domain.Mutation{Runs: []*domain.Run{child}, Invocations: []*domain.Invocation{inv}, Tools: records})
		if err := h.w.finish(context.Background(), child); err != nil {
			t.Fatal(err)
		}
		if i == 0 && (child.Repairs != 1 || len(child.Nodes) != 5) {
			t.Fatalf("repair not scheduled: %+v", child)
		}
		if i == 1 && (child.Repairs != 0 || child.State != domain.Failed) {
			t.Fatalf("child exceeded root budget: %+v", child)
		}
	}
	root = h.load(root.ID)
	if root.Repairs != 1 {
		t.Fatalf("root repairs=%d", root.Repairs)
	}
	if repaired, err := h.w.repair(context.Background(), root); err != nil || repaired {
		t.Fatalf("root exceeded shared budget: %v %v", repaired, err)
	}
	code := h.child(root, "code")
	if repaired, err := h.w.repair(context.Background(), code); err != nil || repaired || len(code.Nodes) != 1 {
		t.Fatal("code child expanded")
	}
}

func TestRootRepairNodeIdentityIsLocalButCounterIsGlobal(t *testing.T) {
	h := newHarness(t, nil, 1)
	root := h.run("test", "root")
	root.State, root.Repairs, root.Limits.MaxRepairs = domain.Running, 1, 3 // one child already repaired
	h.apply(domain.Mutation{Runs: []*domain.Run{root}})
	for i := 1; i <= 2; i++ {
		ok, err := h.w.repair(context.Background(), root)
		if err != nil || !ok || root.Nodes[len(root.Nodes)-3].ID != fmt.Sprintf("repair-%d-repair", i) {
			t.Fatalf("unstable local attempt: %+v %v", root.Nodes, err)
		}
	}
	if root.Repairs != 3 {
		t.Fatalf("global counter=%d", root.Repairs)
	}
}

func TestRecoveredChildStatePropagatesBeforeAnyExecution(t *testing.T) {
	for _, phase := range []string{"model_started", "tool_started", "model"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t, nil, 1)
			root := h.run("code", "root")
			root.State = domain.Running
			h.apply(domain.Mutation{Runs: []*domain.Run{root}})
			root.State = domain.Waiting
			h.apply(domain.Mutation{Runs: []*domain.Run{root}})
			child := h.child(root, "code")
			child.State = domain.Running
			inv := &domain.Invocation{ID: domain.InvocationID(domain.NewID()), RunID: child.ID, NodeID: child.Nodes[0].ID, Phase: phase, Thread: domain.Thread{ContextVersion: 1}}
			var records []*domain.ToolExecution
			if phase == "tool_started" {
				inv.Phase = "tools"
				inv.Thread.Messages = []model.Message{{Role: "assistant", ToolCalls: []model.Call{{ID: "shell", Name: "bash"}}}}
				records = []*domain.ToolExecution{{Key: fmt.Sprintf("%d:1:shell", inv.ID), RunID: child.ID, InvocationID: inv.ID, Name: "bash", State: "started"}}
			}
			child.Nodes[0].InvocationID = inv.ID
			h.apply(domain.Mutation{Runs: []*domain.Run{child}, Invocations: []*domain.Invocation{inv}, Tools: records})
			if err := h.db.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			result := h.drive(root.ID)
			want := domain.Reconciling
			if phase == "model" {
				want = domain.Interrupted
			}
			if result.State != want {
				t.Fatalf("root hides recovered child: %+v", result)
			}
		})
	}
}

func TestUnknownChildAndCompletedSiblingNeverBecomeSuccessBatch(t *testing.T) {
	h := newHarness(t, nil, 1)
	root := h.run("code", "root")
	root.State = domain.Running
	h.apply(domain.Mutation{Runs: []*domain.Run{root}})
	root.State = domain.Waiting
	root.Nodes[0].State = "children"
	// Seed children, and a genuine parent delegation so the root cannot run yet.
	parentInv := &domain.Invocation{ID: domain.InvocationID(domain.NewID()), RunID: root.ID, NodeID: root.Nodes[0].ID, Phase: "delegated", AssistantSequence: 1, Pending: []model.Call{{ID: "delegate", Name: "create_task"}}, Thread: domain.Thread{ContextVersion: 1}}
	root.Nodes[0].InvocationID = parentInv.ID
	h.apply(domain.Mutation{Runs: []*domain.Run{root}, Invocations: []*domain.Invocation{parentInv}})
	a, b := h.child(root, "code"), h.child(root, "code")
	key, _ := pendingKey(parentInv)
	h.apply(domain.Mutation{Delegations: []*domain.Delegation{{Key: key, ParentRunID: root.ID, InvocationID: parentInv.ID, Children: []domain.RunID{a.ID, b.ID}}}})
	h.w.agent = agentFunc(func(ctx context.Context, id domain.InvocationID, _ string) agent.Outcome {
		inv, err := h.db.Invocation(ctx, id)
		if err != nil {
			return agent.Outcome{Kind: "failed", Err: err}
		}
		if inv.RunID == a.ID {
			return agent.Outcome{Kind: "failed", Err: domain.ErrUnknown}
		}
		return agent.Outcome{Kind: "completed", Result: "done"}
	})
	result := h.drive(root.ID)
	if result.State != domain.Reconciling || h.load(b.ID).Nodes[0].State == "completed" {
		t.Fatalf("unsafe batch committed: %+v", result)
	}
}
