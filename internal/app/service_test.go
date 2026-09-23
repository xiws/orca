package app_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiws/orca/internal/agent"
	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
	"github.com/xiws/orca/internal/store"
	"github.com/xiws/orca/internal/tools"
	"github.com/xiws/orca/internal/workflow"
)

type scriptedModel struct {
	mu       sync.Mutex
	requests []model.Request
	answer   func(context.Context, model.Request) (model.Response, error)
}

func (f *scriptedModel) Complete(ctx context.Context, req model.Request, sink model.Sink) (model.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	res, err := f.answer(ctx, req)
	if sink != nil && res.Message.Content != "" {
		sink(res.Message.Content)
	}
	return res, err
}
func answer(text string) model.Response {
	return model.Response{Complete: true, Message: model.Message{Role: "assistant", Content: text}, Usage: model.Usage{TotalTokens: 10}}
}
func toolCall(name, args string) model.Response {
	res := answer("")
	res.Message.ToolCalls = []model.Call{{ID: "call-1", Name: name, Arguments: args}}
	return res
}

func setup(t *testing.T, f *scriptedModel, concurrency int) (*app.Service, *store.Store, string) {
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
	runner := agent.NewRunner(db, f, gateway, concurrency)
	service := app.NewService(db, workflow.NewRunner(db, runner), app.Options{Workspace: workspace, DefaultModel: model.Ref{Provider: "fake", Model: "fake"}, Policy: domain.Policy{Tools: []string{"read", "write", "edit", "bash", "create_task", "request_input"}}, Limits: domain.DefaultLimits()})
	runner.Sink = service.Publish
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
		gateway.Close()
		db.Close()
	})
	return service, db, workspace
}
func wait(t *testing.T, s *app.Service, id domain.RunID) *domain.Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := s.Wait(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func submit(t *testing.T, s *app.Service, req app.SubmitRequest) *domain.Run {
	t.Helper()
	r, err := s.Submit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFastPathAndSessionQueueFreeze(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	f := &scriptedModel{}
	f.answer = func(ctx context.Context, req model.Request) (model.Response, error) {
		last := req.Messages[len(req.Messages)-1].Content
		if last == "first" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return model.Response{}, ctx.Err()
			}
			return answer("FIRST"), nil
		}
		var all strings.Builder
		for _, m := range req.Messages {
			all.WriteString(m.Content)
		}
		if !strings.Contains(all.String(), "FIRST") {
			return model.Response{}, fmt.Errorf("confirmed prior answer lost: %s", all.String())
		}
		if strings.Contains(all.String(), "third") {
			return model.Response{}, fmt.Errorf("later queued goal leaked")
		}
		return answer("SECOND"), nil
	}
	s, db, _ := setup(t, f, 1)
	first := submit(t, s, app.SubmitRequest{Input: "first", Mode: "ask"})
	<-started
	second := submit(t, s, app.SubmitRequest{SessionID: first.SessionID, Input: "second", Mode: "ask"})
	close(release)
	if r := wait(t, s, first.ID); r.State != domain.Succeeded {
		t.Fatalf("first: %+v", r)
	}
	if r := wait(t, s, second.ID); r.State != domain.Succeeded {
		t.Fatalf("second: %+v", r)
	}
	if first.TaskID == second.TaskID || first.SessionID != second.SessionID {
		t.Fatal("wrong identities")
	}
	session, err := db.Session(context.Background(), first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Messages) != 4 {
		t.Fatalf("timeline: %+v", session.Messages)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 2 {
		t.Fatalf("fast path called model %d times", len(f.requests))
	}
}

func TestApprovalBindsPendingToolAndDoesNotReplay(t *testing.T) {
	f := &scriptedModel{answer: func(_ context.Context, req model.Request) (model.Response, error) {
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			return answer("written"), nil
		}
		return toolCall("write", `{"filename":"a.txt","content":"new","expected_hash":"absent"}`), nil
	}}
	s, db, workspace := setup(t, f, 1)
	run := submit(t, s, app.SubmitRequest{Input: "write a file"})
	paused := wait(t, s, run.ID)
	if paused.State != domain.Waiting {
		t.Fatalf("did not wait: %+v", paused)
	}
	if _, err := os.Stat(filepath.Join(workspace, "a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write before approval: %v", err)
	}
	pending, err := s.PendingInputs(context.Background(), run.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v %v", pending, err)
	}
	if _, err := s.RespondToInput(context.Background(), pending[0].ID, "", true); err != nil {
		t.Fatal(err)
	}
	if result := wait(t, s, run.ID); result.State != domain.Succeeded {
		t.Fatalf("result: %+v", result)
	}
	if _, err := s.RespondToInput(context.Background(), pending[0].ID, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RespondToInput(context.Background(), pending[0].ID, "changed", true); err == nil {
		t.Fatal("duplicate altered reply accepted")
	}
	data, err := os.ReadFile(filepath.Join(workspace, "a.txt"))
	if err != nil || string(data) != "new" {
		t.Fatalf("file=%s %v", data, err)
	}
	invs, err := db.Invocations(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 1 || len(invs[0].Thread.Messages) != 5 {
		t.Fatalf("invocations: %+v", invs)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 2 {
		t.Fatalf("unexpected replay %d", len(f.requests))
	}
}

func TestDelegationCompletesWithOneModelSlot(t *testing.T) {
	f := &scriptedModel{answer: func(_ context.Context, req model.Request) (model.Response, error) {
		last := req.Messages[len(req.Messages)-1]
		if last.Role == "tool" {
			if !strings.Contains(last.Content, "CHILD") {
				return model.Response{}, errors.New("child result missing")
			}
			return answer("PARENT"), nil
		}
		if last.Content == "child goal" {
			return answer("CHILD"), nil
		}
		return toolCall("create_task", `{"task_target":[{"title":"child","description":"child goal"}]}`), nil
	}}
	s, db, _ := setup(t, f, 1)
	run := submit(t, s, app.SubmitRequest{Input: "parent goal", Mode: "code"})
	result := wait(t, s, run.ID)
	if result.State != domain.Succeeded || result.Result != "PARENT" {
		t.Fatalf("delegation failed: %+v", result)
	}
	runs, err := db.Runs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || result.Budget.Turns != 3 || result.Budget.Children != 1 {
		t.Fatalf("root budget or children wrong: %+v / %+v", result.Budget, runs)
	}
}

func TestUnknownModelOutcomeCannotResumeOrRetry(t *testing.T) {
	f := &scriptedModel{answer: func(context.Context, model.Request) (model.Response, error) {
		return model.Response{}, model.ErrUnknown
	}}
	s, _, _ := setup(t, f, 1)
	run := submit(t, s, app.SubmitRequest{Input: "goal", Mode: "ask"})
	result := wait(t, s, run.ID)
	if result.State != domain.Reconciling {
		t.Fatalf("unknown converted to %s", result.State)
	}
	if _, err := s.ResumeRun(context.Background(), run.ID); err == nil {
		t.Fatal("unknown resumed")
	}
	if _, err := s.RetryTask(context.Background(), run.TaskID); err == nil {
		t.Fatal("unknown bypassed by retry")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 {
		t.Fatal("unknown repeated")
	}
}

func TestCancelInFlightModelPreservesUnknown(t *testing.T) {
	started := make(chan struct{})
	f := &scriptedModel{answer: func(ctx context.Context, _ model.Request) (model.Response, error) {
		close(started)
		<-ctx.Done()
		return model.Response{}, model.ErrUnknown
	}}
	s, _, _ := setup(t, f, 1)
	run := submit(t, s, app.SubmitRequest{Input: "cancel me", Mode: "ask"})
	<-started
	if err := s.Cancel(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	result := wait(t, s, run.ID)
	if result.State != domain.Reconciling {
		t.Fatalf("cancel hid unknown request: %+v", result)
	}
}

func TestHumanInputContinuesSameInvocation(t *testing.T) {
	f := &scriptedModel{answer: func(_ context.Context, req model.Request) (model.Response, error) {
		if req.Messages[len(req.Messages)-1].Role == "tool" {
			return answer("clarified"), nil
		}
		return toolCall("request_input", `{"prompt":"Which target?"}`), nil
	}}
	s, db, _ := setup(t, f, 1)
	run := submit(t, s, app.SubmitRequest{Input: "ambiguous", Mode: "ask"})
	if paused := wait(t, s, run.ID); paused.State != domain.Waiting {
		t.Fatalf("not waiting: %+v", paused)
	}
	pending, err := s.PendingInputs(context.Background(), run.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending %+v %v", pending, err)
	}
	if _, err := s.RespondToInput(context.Background(), pending[0].ID, "local", false); err != nil {
		t.Fatal(err)
	}
	if result := wait(t, s, run.ID); result.State != domain.Succeeded {
		t.Fatalf("failed continuation: %+v", result)
	}
	invs, err := db.Invocations(context.Background(), run.ID)
	if err != nil || len(invs) != 1 {
		t.Fatalf("new invocation created for input: %+v %v", invs, err)
	}
}

func TestReadOnlyModesDoNotExposeShell(t *testing.T) {
	for _, mode := range []string{"ask", "review", "plan", "deliberate"} {
		t.Run(mode, func(t *testing.T) {
			f := &scriptedModel{answer: func(_ context.Context, req model.Request) (model.Response, error) {
				for _, tool := range req.Tools {
					if tool.Name == "bash" || tool.Name == "write" {
						return model.Response{}, errors.New("unsafe tool exposed")
					}
				}
				return answer("read-only"), nil
			}}
			s, _, _ := setup(t, f, 2)
			run := submit(t, s, app.SubmitRequest{Input: "inspect", Mode: mode})
			result := wait(t, s, run.ID)
			if strings.Contains(result.Error, "unsafe") {
				t.Fatal(result.Error)
			}
		})
	}
}
