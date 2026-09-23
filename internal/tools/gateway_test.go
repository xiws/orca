package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

var errLedger = errors.New("injected ledger failure")

type memoryLedger struct {
	mu         sync.Mutex
	tools      map[string]*domain.ToolExecution
	inputs     map[string]*domain.InputRequest
	history    []string
	applies    int
	failAt     int
	toolErr    error
	inputErr   error
	afterApply func(string)
}

func newLedger() *memoryLedger {
	return &memoryLedger{tools: map[string]*domain.ToolExecution{}, inputs: map[string]*domain.InputRequest{}}
}
func (l *memoryLedger) Apply(ctx context.Context, m domain.Mutation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	l.applies++
	if l.applies == l.failAt {
		l.mu.Unlock()
		return errLedger
	}
	// Tool execution must not create tasks/runs/inputs or invoke orchestration.
	if len(m.Tools) != 1 || len(m.Tasks)+len(m.Runs)+len(m.Invocations)+len(m.Inputs)+len(m.Delegations)+len(m.Sessions)+len(m.Artifacts)+len(m.Events) != 0 {
		l.mu.Unlock()
		return fmt.Errorf("unexpected mutation: %+v", m)
	}
	r := m.Tools[0]
	old := l.tools[r.Key]
	if (old == nil && r.Version != 0) || (old != nil && old.Version != r.Version) {
		l.mu.Unlock()
		return domain.ErrConflict
	}
	copy := *r
	copy.Version++
	l.tools[r.Key] = &copy
	l.history = append(l.history, r.State)
	callback := l.afterApply
	l.mu.Unlock()
	if callback != nil {
		callback(r.State)
	}
	return nil
}
func (l *memoryLedger) Tool(ctx context.Context, key string) (*domain.ToolExecution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.toolErr != nil {
		return nil, l.toolErr
	}
	if r := l.tools[key]; r != nil {
		copy := *r
		return &copy, nil
	}
	return nil, domain.ErrNotFound
}
func (l *memoryLedger) InputByCall(ctx context.Context, key string) (*domain.InputRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inputErr != nil {
		return nil, l.inputErr
	}
	if r := l.inputs[key]; r != nil {
		copy := *r
		return &copy, nil
	}
	return nil, domain.ErrNotFound
}
func (l *memoryLedger) saveInput(r *domain.InputRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	copy := *r
	copy.Version++
	l.inputs[r.CallKey] = &copy
}

type fixture struct {
	g   *Gateway
	l   *memoryLedger
	run *domain.Run
	inv *domain.Invocation
	dir string
}

func setup(t *testing.T, auto bool) fixture {
	t.Helper()
	dir := t.TempDir()
	l := newLedger()
	g, err := New(dir, l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := g.Close(); err != nil {
			t.Error(err)
		}
	})
	names := []string{"read", "write", "edit", "bash", "create_task", "request_input"}
	p := domain.Policy{Tools: names}
	if auto {
		p.AutoApprove = []string{"write", "edit", "bash"}
	}
	return fixture{g, l, &domain.Run{ID: 1, Workspace: dir, Policy: p}, &domain.Invocation{ID: 2, RunID: 1, Role: "worker", Policy: p}, dir}
}
func call(name, id string, args any) model.Call {
	b, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return model.Call{Name: name, ID: id, Arguments: string(b)}
}
func (f fixture) exec(t *testing.T, c model.Call, key string) Result {
	t.Helper()
	r, err := f.g.Execute(context.Background(), f.run, f.inv, c, key)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return r
}
func body(t *testing.T, r Result) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.Content), &m); err != nil {
		t.Fatalf("not JSON: %q: %v", r.Content, err)
	}
	return m
}
func requireOK(t *testing.T, r Result) map[string]any {
	t.Helper()
	m := body(t, r)
	if m["ok"] != true {
		t.Fatalf("tool failed: %s", r.Content)
	}
	return m
}
func requireError(t *testing.T, r Result, code string) {
	t.Helper()
	m := body(t, r)
	if m["ok"] != false {
		t.Fatalf("expected tool error: %s", r.Content)
	}
	if code != "" && m["error"].(map[string]any)["code"] != code {
		t.Fatalf("want %s: %s", code, r.Content)
	}
}
func sum(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
func writeArgs(name, content, hash string) map[string]any {
	return map[string]any{"filename": name, "content": content, "expected_hash": hash}
}
func mustWrite(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
func requireFile(t *testing.T, name, want string) {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil || string(b) != want {
		t.Fatalf("file %s = %q, %v; want %q", name, b, err, want)
	}
}
func requireAbsent(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected absent %s: %v", name, err)
	}
}

func TestDefinitionsAndStrictValidation(t *testing.T) {
	f := setup(t, true)
	if got := f.g.Definitions(domain.Policy{}); len(got) != 0 {
		t.Fatalf("nil policy exposes tools: %+v", got)
	}
	defs := f.g.Definitions(f.run.Policy)
	if len(defs) != 6 {
		t.Fatalf("definitions: %+v", defs)
	}
	for _, def := range defs {
		var published schema
		if err := json.Unmarshal(def.Parameters, &published); err != nil {
			t.Fatal(err)
		}
		if published.AdditionalProperties == nil || *published.AdditionalProperties {
			t.Errorf("loose schema: %s", def.Name)
		}
		b, _ := json.Marshal(lookup(def.Name).parameters)
		if string(b) != string(def.Parameters) {
			t.Fatalf("registry drift: %s", def.Name)
		}
	}
	cases := []struct{ name, args string }{
		{"read", `{}`}, {"read", `null`}, {"read", `[]`},
		{"read", `{"filename":"x","extra":1}`},
		{"read", `{"filename":"x","filename":"y"}`},
		{"read", `{"filename":"x"} {}`},
		{"read", `{"filename":null}`}, {"read", `{"filename":1}`},
		{"read", `{"filename":"x","start":0}`},
		{"read", `{"filename":"x","start":1.5}`},
		{"read", `{"filename":"x","start":2,"end":1}`},
		{"read", `{"filename":"x","start":99999999999999999999999999}`},
		{"write", `{"filename":"x","content":"y"}`},
		{"write", `{"filename":"x","content":"y","expected_hash":"bad"}`},
		{"write", `{"filename":"x","content":null,"expected_hash":"absent"}`},
		{"edit", `{"filename":"x","contents":[{"diff":"x"}]}`},
		{"edit", `{"filename":"x","old_string":"a","new_string":"b","expected_hash":"absent"}`},
		{"edit", `{"filename":"x","old_string":"","new_string":"b","expected_hash":"` + sum("a") + `"}`},
		{"bash", `{"content":" "}`}, {"bash", `{"content":"true","timeout":121}`},
		{"bash", `{"content":"true","timeout":0}`}, {"bash", `{"content":"true","timeout":"1"}`},
		{"bash", `{"command":"true"}`},
		{"create_task", `{"task_target":[]}`},
		{"create_task", `{"task_target":[{"title":"a","description":"b","role":"admin"}]}`},
		{"create_task", `{"task_target":[{"title":"a"}]}`},
		{"request_input", `{"prompt":"a","response":"inject"}`},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%s-%d", tc.name, i), func(t *testing.T) {
			r := f.exec(t, model.Call{Name: tc.name, ID: "bad", Arguments: tc.args}, fmt.Sprint(i))
			requireError(t, r, "invalid_arguments")
		})
	}
	requireError(t, f.exec(t, call("unknown", "unknown", map[string]any{}), "unknown"), "unknown_tool")
	if f.l.applies != 0 {
		t.Fatalf("invalid arguments performed mutations: %d", f.l.applies)
	}
}

func TestPolicyIntersectionAndCurrentAuthorization(t *testing.T) {
	f := setup(t, true)
	c := call("write", "write-1", writeArgs("x", "new", "absent"))
	f.inv.Policy.Tools = nil
	requireError(t, f.exec(t, c, "one"), "not_allowed")
	f.inv.Policy = domain.Policy{Tools: []string{"write"}}
	r := f.exec(t, c, "one")
	if r.Waiting == nil {
		t.Fatal("one-sided auto approval was accepted")
	}
	f.l.saveInput(r.Waiting)
	r.Waiting.State, r.Waiting.Approved = "approved", true
	f.l.saveInput(r.Waiting)
	f.run.Policy.Tools = nil
	requireError(t, f.exec(t, c, "one"), "not_allowed")
	requireAbsent(t, filepath.Join(f.dir, "x"))
	if f.l.applies != 0 {
		t.Fatal("denied policy executed")
	}
}

func TestApprovalBindingLifecycle(t *testing.T) {
	f := setup(t, false)
	c := call("write", "call-write", writeArgs("nested/x", "approved content", "absent"))
	r := f.exec(t, c, "approval-key")
	if r.Waiting == nil || r.Waiting.Kind != "approval" || r.Waiting.CallKey != "approval-key" || r.Waiting.Version != 0 {
		t.Fatalf("waiting: %+v", r)
	}
	if !strings.Contains(r.Waiting.Prompt, f.dir) || !strings.Contains(r.Waiting.Prompt, "approved content") || !strings.Contains(r.Waiting.Prompt, c.ID) {
		t.Fatalf("inexact approval prompt: %s", r.Waiting.Prompt)
	}
	if len(f.l.inputs) != 0 || f.l.applies != 0 {
		t.Fatal("gateway persisted waiting request")
	}
	requireAbsent(t, filepath.Join(f.dir, "nested"))
	f.l.saveInput(r.Waiting) // stand in for the runner's atomic waiting mutation
	pending := f.exec(t, c, "approval-key")
	if pending.Waiting == nil || pending.Waiting.ID != r.Waiting.ID {
		t.Fatal("pending input was not reused")
	}
	changed := call("write", c.ID, writeArgs("nested/x", "unapproved content", "absent"))
	requireError(t, f.exec(t, changed, "approval-key"), "approval_binding_conflict")
	changed = c
	changed.ID = "another-call"
	requireError(t, f.exec(t, changed, "approval-key"), "approval_binding_conflict")
	other := setup(t, false)
	other.g.ledger = f.l
	requireError(t, other.exec(t, c, "approval-key"), "approval_binding_conflict")
	r.Waiting.State, r.Waiting.Approved = "answered", true
	f.l.saveInput(r.Waiting)
	requireError(t, f.exec(t, call("write", c.ID, writeArgs("nested/x", "changed", "absent")), "approval-key"), "approval_binding_conflict")
	result := f.exec(t, c, "approval-key")
	requireOK(t, result)
	requireFile(t, filepath.Join(f.dir, "nested/x"), "approved content")
	if !reflect.DeepEqual(f.l.history, []string{"prepared", "started", "completed"}) {
		t.Fatalf("history: %v", f.l.history)
	}
	// JSON object field order/spacing does not change the approved operation.
	c.Arguments = `{ "expected_hash":"absent", "filename":"nested/x", "content":"approved content" }`
	if again := f.exec(t, c, "approval-key"); again.Content != result.Content {
		t.Fatal("completed result changed")
	}
	if f.l.applies != 3 {
		t.Fatal("completed operation was replayed")
	}
}

func TestApprovalDeniedExpiredAndDefaults(t *testing.T) {
	for _, name := range []string{"write", "edit", "bash"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t, false)
			mustWrite(t, filepath.Join(f.dir, "x"), "old")
			args := map[string]any{"filename": "x", "content": "new", "expected_hash": sum("old")}
			if name == "edit" {
				args = map[string]any{"filename": "x", "old_string": "old", "new_string": "new", "expected_hash": sum("old")}
			}
			if name == "bash" {
				args = map[string]any{"content": "printf changed > x"}
			}
			c := call(name, name, args)
			r := f.exec(t, c, name)
			if r.Waiting == nil || r.Waiting.Kind != "approval" {
				t.Fatal("missing default approval")
			}
			requireFile(t, filepath.Join(f.dir, "x"), "old")
			if f.l.applies != 0 {
				t.Fatal("approval started external work")
			}
			r.Waiting.State = "rejected"
			f.l.saveInput(r.Waiting)
			requireError(t, f.exec(t, c, name), "approval_rejected")
			r.Waiting.State, r.Waiting.Approved, r.Waiting.ExpiresAt = "approved", true, time.Now().Add(-time.Second).Unix()
			f.l.saveInput(r.Waiting)
			requireError(t, f.exec(t, c, name), "approval_expired")
			requireFile(t, filepath.Join(f.dir, "x"), "old")
		})
	}
}

func TestControlIntentsDoNotExecuteOrPersist(t *testing.T) {
	f := setup(t, false)
	c := call("create_task", "goal-call", map[string]any{"task_target": []Goal{{"Inspect", "Inspect the implementation"}, {"Test", "Run isolated tests"}}})
	r := f.exec(t, c, "goals")
	if len(r.Goals) != 2 || r.Goals[0].Title != "Inspect" || r.Waiting != nil {
		t.Fatalf("goals: %+v", r)
	}
	requireOK(t, r)
	c = call("request_input", "input-call", map[string]any{"prompt": "Which implementation?"})
	r = f.exec(t, c, "input")
	if r.Waiting == nil || r.Waiting.Kind != "input" {
		t.Fatalf("input: %+v", r)
	}
	f.l.saveInput(r.Waiting)
	if pending := f.exec(t, c, "input"); pending.Waiting == nil || pending.Waiting.ID != r.Waiting.ID {
		t.Fatal("pending input not reused")
	}
	requireError(t, f.exec(t, call("request_input", c.ID, map[string]any{"prompt": "Different?"}), "input"), "approval_binding_conflict")
	r.Waiting.State, r.Waiting.Response = "answered", "Use the smaller one."
	f.l.saveInput(r.Waiting)
	answer := f.exec(t, c, "input")
	if answer.Content != r.Waiting.Response || answer.Waiting != nil {
		t.Fatalf("answer: %+v", answer)
	}
	if f.l.applies != 0 || len(f.l.tools) != 0 {
		t.Fatal("control intent wrote an execution ledger")
	}
}

func TestCompletedPreparedAndUnknown(t *testing.T) {
	f := setup(t, true)
	c := call("write", "call-a", writeArgs("x", "first", "absent"))
	r := f.exec(t, c, "complete")
	requireOK(t, r)
	mustWrite(t, filepath.Join(f.dir, "x"), "user revision")
	if again := f.exec(t, c, "complete"); again.Content != r.Content {
		t.Fatal("completed result not reused")
	}
	requireFile(t, filepath.Join(f.dir, "x"), "user revision")
	requireError(t, f.exec(t, call("write", c.ID, writeArgs("x", "different", "absent")), "complete"), "call_key_conflict")
	other := setup(t, true)
	other.g.ledger = f.l
	requireError(t, other.exec(t, c, "complete"), "call_key_conflict")
	for _, state := range []string{"started", "unknown", "unexpected"} {
		c := call("write", state, writeArgs(state, "bad", "absent"))
		_, canonical, err := lookup(c.Name).parse(c.Arguments)
		if err != nil {
			t.Fatal(err)
		}
		f.l.tools[state] = &domain.ToolExecution{Key: state, RunID: f.run.ID, InvocationID: f.inv.ID, Name: c.Name, Arguments: canonical, Workspace: f.dir, State: state, Version: 1}
		if _, err := f.g.Execute(context.Background(), f.run, f.inv, c, state); !errors.Is(err, domain.ErrUnknown) {
			t.Fatalf("%s: %v", state, err)
		}
		requireAbsent(t, filepath.Join(f.dir, state))
	}
	c = call("write", "prepared", writeArgs("prepared", "safe", "absent"))
	_, canonical, _ := lookup(c.Name).parse(c.Arguments)
	f.l.tools["prepared"] = &domain.ToolExecution{Key: "prepared", RunID: f.run.ID, InvocationID: f.inv.ID, Name: c.Name, Arguments: canonical, Workspace: f.dir, State: "prepared", Version: 1}
	requireOK(t, f.exec(t, c, "prepared"))
	requireFile(t, filepath.Join(f.dir, "prepared"), "safe")
}

func TestApplyFailureStopsExecution(t *testing.T) {
	for failAt := 1; failAt <= 3; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			f := setup(t, true)
			f.l.failAt = failAt
			c := call("write", "fault", writeArgs("x", "written", "absent"))
			_, err := f.g.Execute(context.Background(), f.run, f.inv, c, "fault")
			if !errors.Is(err, errLedger) {
				t.Fatalf("lost ledger error: %v", err)
			}
			if f.l.applies != failAt {
				t.Fatalf("continued after failure: %d", f.l.applies)
			}
			if failAt < 3 {
				requireAbsent(t, filepath.Join(f.dir, "x"))
			} else {
				requireFile(t, filepath.Join(f.dir, "x"), "written")
				if !errors.Is(err, domain.ErrUnknown) {
					t.Fatalf("uncommitted effect not unknown: %v", err)
				}
				if _, err := f.g.Execute(context.Background(), f.run, f.inv, c, "fault"); !errors.Is(err, domain.ErrUnknown) {
					t.Fatalf("replayed started: %v", err)
				}
			}
		})
	}
}

func TestLookupFailuresAndCancellationStopActions(t *testing.T) {
	for _, which := range []string{"tool", "input", "cancel"} {
		t.Run(which, func(t *testing.T) {
			f := setup(t, true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch which {
			case "tool":
				f.l.toolErr = errLedger
			case "input":
				f.l.inputErr = errLedger
			case "cancel":
				cancel()
			}
			_, err := f.g.Execute(ctx, f.run, f.inv, call("write", which, writeArgs("x", "bad", "absent")), which)
			if err == nil {
				t.Fatal("missing error")
			}
			if f.l.applies != 0 {
				t.Fatal("performed work after failure")
			}
			requireAbsent(t, filepath.Join(f.dir, "x"))
		})
	}
}

func TestCancellationAfterExternalActionIsUnknown(t *testing.T) {
	f := setup(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel during completion commit: the file action happened but the
	// transaction cannot commit. A subsequent attempt must not replay it.
	f.l.afterApply = func(state string) {
		if state == "started" {
			// Cancellation just after started is also conservatively unknown,
			// even if the action never got a chance to begin.
			cancel()
		}
	}
	c := call("write", "cancel", writeArgs("x", "bad", "absent"))
	_, err := f.g.Execute(ctx, f.run, f.inv, c, "cancel")
	if !errors.Is(err, domain.ErrUnknown) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	requireAbsent(t, filepath.Join(f.dir, "x"))
	if _, err := f.g.Execute(context.Background(), f.run, f.inv, c, "cancel"); !errors.Is(err, domain.ErrUnknown) {
		t.Fatalf("cancelled operation replayed: %v", err)
	}
}

func TestCallIDsAndLifecycle(t *testing.T) {
	f := setup(t, true)
	mustWrite(t, filepath.Join(f.dir, "x"), "hello")
	for _, id := range []string{"first-call", "second-call"} {
		r := f.exec(t, call("read", id, map[string]any{"filename": "x"}), id)
		if requireOK(t, r)["call_id"] != id {
			t.Fatalf("call id lost: %s", r.Content)
		}
	}
	if err := f.g.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.g.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.g.Execute(context.Background(), f.run, f.inv, call("read", "closed", map[string]any{"filename": "x"}), "closed"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
	if _, err := New(f.dir, nil); err == nil {
		t.Fatal("nil ledger accepted")
	}
	if _, err := New(filepath.Join(f.dir, "missing"), f.l); err == nil {
		t.Fatal("missing workspace accepted")
	}
}
