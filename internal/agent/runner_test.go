package agent

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
	"github.com/xiws/orca/internal/store"
	"github.com/xiws/orca/internal/tools"
)

type modelFunc func(context.Context, model.Request, model.Sink) (model.Response, error)

func (f modelFunc) Complete(ctx context.Context, req model.Request, sink model.Sink) (model.Response, error) {
	return f(ctx, req, sink)
}

type faultStore struct {
	*store.Store
	fail          func(domain.Mutation) bool
	apply         func(context.Context, domain.Mutation) error
	run           func(context.Context, domain.RunID) (*domain.Run, error)
	invocationErr error
	toolErr       error
}

func (s *faultStore) Apply(ctx context.Context, m domain.Mutation) error {
	if s.fail != nil && s.fail(m) {
		return errors.New("injected checkpoint failure")
	}
	if s.apply != nil {
		return s.apply(ctx, m)
	}
	return s.Store.Apply(ctx, m)
}

func (s *faultStore) Run(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	if s.run != nil {
		return s.run(ctx, id)
	}
	return s.Store.Run(ctx, id)
}

func (s *faultStore) Invocation(ctx context.Context, id domain.InvocationID) (*domain.Invocation, error) {
	if s.invocationErr != nil {
		return nil, s.invocationErr
	}
	return s.Store.Invocation(ctx, id)
}

func (s *faultStore) Tool(ctx context.Context, key string) (*domain.ToolExecution, error) {
	if s.toolErr != nil {
		return nil, s.toolErr
	}
	return s.Store.Tool(ctx, key)
}

func runnerFixture(t *testing.T, client model.Client) (*Runner, *faultStore, *domain.Invocation) {
	t.Helper()
	workspace := t.TempDir()
	db, err := store.Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	fs := &faultStore{Store: db}
	gateway, err := tools.New(workspace, fs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gateway.Close() })
	task := domain.NewTask("goal", "goal", 0)
	run := &domain.Run{ID: domain.RunID(domain.NewID()), TaskID: task.ID, TaskVersion: 1, Workspace: workspace, State: domain.Running, Limits: domain.DefaultLimits(), Policy: domain.Policy{Tools: []string{"read", "request_input"}}}
	run.RootRunID = run.ID
	inv := &domain.Invocation{ID: domain.InvocationID(domain.NewID()), RunID: run.ID, Role: "responder", RoleVersion: 1, Model: model.Ref{Provider: "fake", Model: "model"}, Policy: run.Policy, Phase: "model", Thread: domain.Thread{ContextVersion: 1, Messages: []model.Message{{Role: "user", Content: "goal"}}}}
	if err := db.Apply(context.Background(), domain.Mutation{Tasks: []*domain.Task{task}, Runs: []*domain.Run{run}, Invocations: []*domain.Invocation{inv}}); err != nil {
		t.Fatal(err)
	}
	return NewRunner(fs, client, gateway, 4), fs, inv
}

func TestCheckpointFailuresNeverResubmitUnknownModel(t *testing.T) {
	for _, phase := range []string{"model_started", "completed"} {
		t.Run(phase, func(t *testing.T) {
			var sends atomic.Int32
			client := modelFunc(func(context.Context, model.Request, model.Sink) (model.Response, error) {
				sends.Add(1)
				return model.Response{Complete: true, Message: model.Message{Content: "done"}}, nil
			})
			runner, fs, inv := runnerFixture(t, client)
			fs.fail = func(m domain.Mutation) bool { return len(m.Invocations) > 0 && m.Invocations[0].Phase == phase }
			out := runner.Advance(context.Background(), inv.ID, "")
			if !errors.Is(out.Err, domain.ErrCheckpoint) {
				t.Fatalf("not checkpoint failure: %+v", out)
			}
			saved, err := fs.Invocation(context.Background(), inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			fs.fail = nil
			if phase == "model_started" {
				if sends.Load() != 0 || saved.Phase != "model" {
					t.Fatalf("submission before durable intent: %+v", saved)
				}
				if out := runner.Advance(context.Background(), inv.ID, ""); out.Kind != "completed" {
					t.Fatalf("safe retry %+v", out)
				}
			} else {
				if sends.Load() != 1 || saved.Phase != "model_started" || len(saved.Thread.Messages) != 1 {
					t.Fatalf("partial response committed %+v", saved)
				}
				out := runner.Advance(context.Background(), inv.ID, "")
				if !errors.Is(out.Err, domain.ErrUnknown) || sends.Load() != 1 {
					t.Fatalf("unknown replayed %+v", out)
				}
			}
		})
	}
}

func TestCompletedInvocationReusesResultAndSharedBudget(t *testing.T) {
	var sends atomic.Int32
	runner, fs, inv := runnerFixture(t, modelFunc(func(context.Context, model.Request, model.Sink) (model.Response, error) {
		sends.Add(1)
		return model.Response{Complete: true, Message: model.Message{Content: "result"}}, nil
	}))
	for range 2 {
		if out := runner.Advance(context.Background(), inv.ID, ""); out.Kind != "completed" || out.Result != "result" {
			t.Fatal(out)
		}
	}
	root, err := fs.Run(context.Background(), inv.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 1 || root.Budget.Turns != 1 || root.Budget.Tokens <= 0 || root.Budget.Reserved != 0 {
		t.Fatalf("budget %+v sends %d", root.Budget, sends.Load())
	}
}

func TestBudgetAndCancellationBeforeModelSubmission(t *testing.T) {
	runner, fs, inv := runnerFixture(t, modelFunc(func(context.Context, model.Request, model.Sink) (model.Response, error) {
		t.Error("model called without budget")
		return model.Response{}, nil
	}))
	root, _ := fs.Run(context.Background(), inv.RunID)
	root.Limits.MaxTokens = 1
	if err := fs.Apply(context.Background(), domain.Mutation{Runs: []*domain.Run{root}}); err != nil {
		t.Fatal(err)
	}
	if out := runner.Advance(context.Background(), inv.ID, ""); !errors.Is(out.Err, domain.ErrBudget) {
		t.Fatalf("budget ignored %+v", out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out := runner.Advance(ctx, inv.ID, ""); !errors.Is(out.Err, context.Canceled) {
		t.Fatalf("cancel ignored %+v", out)
	}
}

func TestProviderLimitDoesNotConsumeGlobalSlotsWhileWaiting(t *testing.T) {
	runner := NewRunner(nil, nil, nil, 4)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for range 2 {
		if err := runner.acquire(ctx, "busy"); err != nil {
			t.Fatal(err)
		}
	}
	blocked := make(chan error, 1)
	go func() { blocked <- runner.acquire(ctx, "busy") }()
	for _, provider := range []string{"other-a", "other-b"} {
		if err := runner.acquire(ctx, provider); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-blocked:
		t.Fatalf("provider cap ignored %v", err)
	default:
	}
	runner.release("busy")
	if err := <-blocked; err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"busy", "busy", "other-a", "other-b"} {
		runner.release(provider)
	}
	if runner.inFlight != 0 {
		t.Fatal(fmt.Sprint(runner.inFlight))
	}
}

func TestRunnerReadFailuresPreserveCheckpoint(t *testing.T) {
	for _, stage := range []string{"invocation", "initial run", "loop run", "reserve", "settle"} {
		t.Run(stage, func(t *testing.T) {
			sends := 0
			runner, fs, inv := runnerFixture(t, modelFunc(func(context.Context, model.Request, model.Sink) (model.Response, error) {
				sends++
				return model.Response{Complete: true, Message: model.Message{Content: "done"}}, nil
			}))
			fault := errors.New("injected read failure")
			read := 0
			if stage == "invocation" {
				fs.invocationErr = fault
			} else {
				failAt := map[string]int{"initial run": 1, "loop run": 2, "reserve": 3, "settle": 4}[stage]
				fs.run = func(ctx context.Context, id domain.RunID) (*domain.Run, error) {
					read++
					if read == failAt {
						return nil, fault
					}
					return fs.Store.Run(ctx, id)
				}
			}
			out := runner.Advance(context.Background(), inv.ID, "")
			if out.Kind != "failed" || !errors.Is(out.Err, domain.ErrCheckpoint) || !errors.Is(out.Err, fault) {
				t.Fatalf("lost checkpoint identity: %+v", out)
			}
			fs.run, fs.invocationErr = nil, nil
			saved, err := fs.Invocation(context.Background(), inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			root, err := fs.Run(context.Background(), inv.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "settle" {
				if saved.Phase != "model_started" || len(saved.Thread.Messages) != 1 || sends != 1 || root.Budget.Reserved != saved.Reservation || saved.Reservation == 0 {
					t.Fatalf("lost durable intent: %+v budget=%+v sends=%d", saved, root.Budget, sends)
				}
				if out = runner.Advance(context.Background(), inv.ID, ""); !errors.Is(out.Err, domain.ErrUnknown) || sends != 1 {
					t.Fatalf("replayed uncertain submission: %+v sends=%d", out, sends)
				}
			} else {
				if saved.Phase != "model" || sends != 0 || root.Budget.Reserved != 0 {
					t.Fatalf("submitted before checkpoint: %+v sends=%d", saved, sends)
				}
				if out = runner.Advance(context.Background(), inv.ID, ""); out.Kind != "completed" || sends != 1 {
					t.Fatalf("safe retry failed: %+v sends=%d", out, sends)
				}
			}
		})
	}
}

func TestRunnerCASExhaustionPreservesCheckpoint(t *testing.T) {
	for _, phase := range []string{"model_started", "completed"} {
		t.Run(phase, func(t *testing.T) {
			sends, conflicts := 0, 0
			runner, fs, inv := runnerFixture(t, modelFunc(func(context.Context, model.Request, model.Sink) (model.Response, error) {
				sends++
				return model.Response{Complete: true, Message: model.Message{Content: "done"}}, nil
			}))
			fs.apply = func(ctx context.Context, m domain.Mutation) error {
				if len(m.Invocations) > 0 && m.Invocations[0].Phase == phase {
					root, err := fs.Store.Run(ctx, inv.RunID)
					if err != nil {
						return err
					}
					if err := fs.Store.Apply(ctx, domain.Mutation{Runs: []*domain.Run{root}}); err != nil {
						return err
					}
					conflicts++
				}
				return fs.Store.Apply(ctx, m)
			}
			out := runner.Advance(context.Background(), inv.ID, "")
			if !errors.Is(out.Err, domain.ErrCheckpoint) || !errors.Is(out.Err, domain.ErrConflict) || conflicts != 8 {
				t.Fatalf("exhaustion lost checkpoint: %+v conflicts=%d", out, conflicts)
			}
			fs.apply = nil
			saved, err := fs.Invocation(context.Background(), inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			events, err := fs.Events(context.Background(), inv.RunID, 0, 100)
			if err != nil || len(events) != 0 {
				t.Fatalf("published uncommitted response: %+v %v", events, err)
			}
			if phase == "completed" {
				root, err := fs.Run(context.Background(), inv.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if sends != 1 || saved.Phase != "model_started" || saved.Reservation == 0 || root.Budget.Reserved != saved.Reservation || root.Budget.Tokens != 0 {
					t.Fatalf("partial settlement: %+v budget=%+v", saved, root.Budget)
				}
				if out = runner.Advance(context.Background(), inv.ID, ""); !errors.Is(out.Err, domain.ErrUnknown) || sends != 1 {
					t.Fatalf("uncertain model replayed: %+v", out)
				}
			} else {
				if sends != 0 || saved.Phase != "model" {
					t.Fatalf("sent without reservation: %+v sends=%d", saved, sends)
				}
				if out = runner.Advance(context.Background(), inv.ID, ""); out.Kind != "completed" || sends != 1 {
					t.Fatalf("safe retry failed: %+v", out)
				}
			}
		})
	}
}

func TestUsageEstimatesChargeToolDefinitions(t *testing.T) {
	for _, reported := range []bool{false, true} {
		t.Run(fmt.Sprint(reported), func(t *testing.T) {
			var fs *faultStore
			var expected int64
			sends := 0
			runner, ledger, inv := runnerFixture(t, modelFunc(func(ctx context.Context, req model.Request, _ model.Sink) (model.Response, error) {
				sends++
				prompt, err := model.ToolPrompt(req.Tools)
				if err != nil || prompt == "" {
					t.Fatalf("missing definitions: %q %v", prompt, err)
				}
				schema := model.Estimate([]model.Message{{Role: "system", Content: prompt}})
				saved, err := fs.Invocation(ctx, domain.InvocationID(req.ThreadID))
				if err != nil || saved.Reservation != model.Estimate(req.Messages)+schema+4096 {
					t.Fatalf("reservation did not charge definitions: %+v %v", saved, err)
				}
				message := model.Message{Role: "assistant", Content: "done"}
				if sends == 1 {
					message.ToolCalls = []model.Call{{ID: "read", Name: "read", Arguments: `{"filename":"missing.txt"}`}}
				}
				usage := model.Usage{}
				if reported {
					usage.TotalTokens = 7
					expected += 7
				} else {
					messages := append(append([]model.Message(nil), req.Messages...), message)
					expected += model.Estimate(messages) + schema
				}
				return model.Response{Complete: true, Message: message, Usage: usage}, nil
			}))
			fs = ledger
			if out := runner.Advance(context.Background(), inv.ID, ""); out.Kind != "completed" {
				t.Fatal(out)
			}
			saved, err := fs.Invocation(context.Background(), inv.ID)
			if err != nil {
				t.Fatal(err)
			}
			root, err := fs.Run(context.Background(), inv.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if sends != 2 || saved.Usage.TotalTokens != expected || saved.Usage.Estimated == reported || root.Budget.Tokens != expected || root.Budget.Reserved != 0 {
				t.Fatalf("incorrect usage: %+v budget=%+v expected=%d sends=%d", saved.Usage, root.Budget, expected, sends)
			}
		})
	}
}

func TestRebaseKeepsExecutionKeysAndEventSequencesDistinct(t *testing.T) {
	ctx := context.Background()
	sends := 0
	runner, fs, inv := runnerFixture(t, modelFunc(func(_ context.Context, req model.Request, sink model.Sink) (model.Response, error) {
		sends++
		sink("first")
		sink("second")
		return model.Response{Complete: true, Cursor: &model.Cursor{ContextVersion: req.ContextVersion, Sequence: 2}, Message: model.Message{Content: "firstsecond", ToolCalls: []model.Call{
			{ID: "read", Name: "read", Arguments: `{"filename":"missing.txt"}`},
			{ID: "input", Name: "request_input", Arguments: `{"prompt":"confirm target"}`},
			{ID: "delegate", Name: "create_task", Arguments: `{"task_target":[{"title":"child","description":"child"}]}`},
		}}}, nil
	}))
	root, err := fs.Run(ctx, inv.RunID)
	if err != nil {
		t.Fatal(err)
	}
	root.Policy.Tools = append(root.Policy.Tools, "create_task")
	inv.Role, inv.Policy = "executor", root.Policy
	if err := fs.Apply(ctx, domain.Mutation{Runs: []*domain.Run{root}, Invocations: []*domain.Invocation{inv}}); err != nil {
		t.Fatal(err)
	}
	fs.fail = func(m domain.Mutation) bool {
		return len(m.Invocations) > 0 && m.Invocations[0].Phase == "model_started" && len(m.Invocations[0].Thread.Messages) > 1
	}
	var deltas []domain.Event
	runner.Sink = func(event domain.Event) { deltas = append(deltas, event) }
	seenKeys := map[string]bool{}
	seenInputs := map[domain.InputRequestID]bool{}
	var sequences []int
	for cycle := 0; cycle < 3; cycle++ {
		sequence := inv.Thread.SequenceOffset + len(inv.Thread.Messages) + 1
		sequences = append(sequences, sequence)
		out := runner.Advance(ctx, inv.ID, "")
		if out.Kind != "input" || out.Input == nil || seenInputs[out.Input.ID] {
			t.Fatalf("reused old input fence: %+v", out)
		}
		seenInputs[out.Input.ID] = true
		key := func(call string) string { return fmt.Sprintf("%d:%d:%s", inv.ID, sequence, call) }
		if out.Input.CallKey != key("input") {
			t.Fatalf("input key=%s want=%s", out.Input.CallKey, key("input"))
		}
		record, err := fs.Tool(ctx, key("read"))
		if err != nil || record.State != "completed" || seenKeys[record.Key] {
			t.Fatalf("read reused an old execution: %+v %v", record, err)
		}
		seenKeys[record.Key] = true
		saved, err := fs.Invocation(ctx, inv.ID)
		if err != nil || saved.AssistantSequence != sequence {
			t.Fatalf("assistant sequence not persisted: %+v %v", saved, err)
		}
		for _, event := range deltas[cycle*2:] {
			if event.Kind != "delta" || event.RunID != root.ID || event.InvocationID != inv.ID || event.AssistantSequence != saved.AssistantSequence {
				t.Fatalf("delta fence disagrees with checkpoint: %+v", event)
			}
		}
		out.Input.State, out.Input.Response = "answered", "target confirmed"
		if err := fs.Apply(ctx, domain.Mutation{Inputs: []*domain.InputRequest{out.Input}}); err != nil {
			t.Fatal(err)
		}
		out = runner.Advance(ctx, inv.ID, "")
		if out.Kind != "delegated" || out.Key != key("delegate") || seenKeys[out.Key] {
			t.Fatalf("delegation reused old key: %+v", out)
		}
		seenKeys[out.Key] = true
		if _, err := fs.Delegation(ctx, out.Key); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("old delegation matched: %v", err)
		}
		if err := fs.Apply(ctx, domain.Mutation{Delegations: []*domain.Delegation{{Key: out.Key, ParentRunID: root.ID, InvocationID: inv.ID}}}); err != nil {
			t.Fatal(err)
		}
		if out = runner.Advance(ctx, inv.ID, "child finished"); !errors.Is(out.Err, domain.ErrCheckpoint) {
			t.Fatalf("did not stop at next model boundary: %+v", out)
		}
		inv, err = fs.Invocation(ctx, inv.ID)
		if err != nil || inv.Phase != "model" || len(inv.Pending) != 0 || sends != cycle+1 {
			t.Fatalf("invalid rebase checkpoint: %+v %v sends=%d", inv, err, sends)
		}
		if cycle < 2 {
			offset := inv.Thread.SequenceOffset + len(inv.Thread.Messages)
			if err := Rebase(inv, []model.Message{{Role: "user", Content: "fresh context"}}); err != nil {
				t.Fatal(err)
			}
			if inv.Thread.SequenceOffset != offset || inv.Thread.ContextVersion != cycle+2 || inv.Thread.Cursor != nil {
				t.Fatalf("rebase lost sequence history: %+v", inv.Thread)
			}
			if err := fs.Apply(ctx, domain.Mutation{Invocations: []*domain.Invocation{inv}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	events, err := fs.Events(ctx, root.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	messages, previous := 0, 0
	for _, event := range events {
		if event.Kind != "message" {
			continue
		}
		if messages >= len(sequences) || event.AssistantSequence != sequences[messages] || event.AssistantSequence <= previous {
			t.Fatalf("durable event fence reused or lost: %+v sequences=%v", event, sequences)
		}
		previous = event.AssistantSequence
		messages++
	}
	if messages != 3 || len(deltas) != 6 {
		t.Fatalf("missing events: messages=%d deltas=%d", messages, len(deltas))
	}
}

func TestGatewayFailuresKeepTheirActualUncertainty(t *testing.T) {
	for _, stage := range []string{"lookup", "prepared", "started", "completed", "cancelled", "deadline", "unknown cancellation"} {
		t.Run(stage, func(t *testing.T) {
			sends := 0
			runner, fs, inv := runnerFixture(t, modelFunc(func(_ context.Context, req model.Request, _ model.Sink) (model.Response, error) {
				sends++
				message := model.Message{Content: "done"}
				if req.Messages[len(req.Messages)-1].Role != "tool" {
					message.ToolCalls = []model.Call{{ID: "read", Name: "read", Arguments: `{"filename":"missing.txt"}`}}
				}
				return model.Response{Complete: true, Message: message}, nil
			}))
			fault := errors.New("tool ledger unavailable")
			switch stage {
			case "lookup":
				fs.toolErr = fault
			case "cancelled":
				fault = context.Canceled
				fs.toolErr = fault
			case "deadline":
				fault = context.DeadlineExceeded
				fs.toolErr = fault
			case "unknown cancellation":
				fault = errors.Join(domain.ErrUnknown, context.Canceled)
				fs.toolErr = fault
			default:
				fs.apply = func(ctx context.Context, m domain.Mutation) error {
					if len(m.Tools) > 0 && m.Tools[0].State == stage {
						return fault
					}
					return fs.Store.Apply(ctx, m)
				}
			}
			out := runner.Advance(context.Background(), inv.ID, "")
			unknown := stage == "completed" || stage == "unknown cancellation"
			checkpoint := stage == "lookup" || stage == "prepared" || stage == "started"
			if !errors.Is(out.Err, fault) || errors.Is(out.Err, domain.ErrUnknown) != unknown || errors.Is(out.Err, domain.ErrCheckpoint) != checkpoint {
				t.Fatalf("incorrect gateway classification: %+v", out)
			}
			fs.apply, fs.toolErr = nil, nil
			saved, err := fs.Invocation(context.Background(), inv.ID)
			if err != nil || saved.Phase != "tools" || saved.NextCall != 0 || sends != 1 {
				t.Fatalf("tool checkpoint changed: %+v %v sends=%d", saved, err, sends)
			}
			if checkpoint {
				if out = runner.Advance(context.Background(), inv.ID, ""); out.Kind != "completed" || sends != 2 {
					t.Fatalf("safe pre-tool failure poisoned retry: %+v sends=%d", out, sends)
				}
			}
			if stage == "completed" {
				if out = runner.Advance(context.Background(), inv.ID, ""); !errors.Is(out.Err, domain.ErrUnknown) || sends != 1 {
					t.Fatalf("started tool replayed: %+v sends=%d", out, sends)
				}
			}
		})
	}
}
