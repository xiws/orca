package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

func TestTaskVersionsRemainPinnedAndSessionDeletion(t *testing.T) {
	f := &scriptedModel{answer: func(context.Context, model.Request) (model.Response, error) { return answer("done"), nil }}
	s, db, _ := setup(t, f, 1)
	run := submit(t, s, app.SubmitRequest{Input: "original", Mode: "ask"})
	wait(t, s, run.ID)
	updated, err := s.UpdateTask(context.Background(), run.TaskID, "revised", "")
	if err != nil || updated.Version != 2 {
		t.Fatalf("revision %+v %v", updated, err)
	}
	old, err := db.Task(context.Background(), run.TaskID, run.TaskVersion)
	if err != nil || old.Input != "original" {
		t.Fatalf("old goal mutated %+v %v", old, err)
	}
	retried, err := s.RetryTask(context.Background(), run.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID == run.ID || retried.TaskVersion != 2 || retried.Prompt != "revised" {
		t.Fatalf("retry %+v", retried)
	}
	wait(t, s, retried.ID)
	if err := s.DeleteSession(context.Background(), run.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(context.Background(), run.SessionID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("session survived: %v", err)
	}
}

func TestUnknownRequiresManualReconciliationBeforeRetryOrDelete(t *testing.T) {
	f := &scriptedModel{answer: func(context.Context, model.Request) (model.Response, error) {
		return model.Response{}, model.ErrUnknown
	}}
	s, db, _ := setup(t, f, 1)
	run := submit(t, s, app.SubmitRequest{Input: "unknown", Mode: "ask"})
	wait(t, s, run.ID)
	if err := s.DeleteSession(context.Background(), run.SessionID); err == nil {
		t.Fatal("deleted unknown evidence")
	}
	if _, err := s.ReconcileRun(context.Background(), run.ID, ""); err == nil {
		t.Fatal("missing human evidence accepted")
	}
	var reconciled *domain.Run
	var err error
	deadline := time.Now().Add(time.Second)
	for {
		reconciled, err = s.ReconcileRun(context.Background(), run.ID, "Verified the remote turn ended; no workspace action occurred")
		if err == nil || !strings.Contains(err.Error(), "has not stopped") || time.Now().After(deadline) {
			break
		}
	}
	if err != nil || reconciled.State != domain.Failed || reconciled.Budget.Reserved != 0 {
		t.Fatalf("reconcile %+v %v", reconciled, err)
	}
	invs, err := db.Invocations(context.Background(), run.ID)
	if err != nil || len(invs) != 1 || invs[0].Phase != "failed" {
		t.Fatalf("old checkpoint %+v %v", invs, err)
	}
	if err := db.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	artifacts, err := db.Artifacts(context.Background(), run.ID)
	if err != nil || len(artifacts) != 1 || artifacts[0].Kind != "reconciliation" {
		t.Fatalf("audit %+v %v", artifacts, err)
	}
	if err := s.DeleteSession(context.Background(), run.SessionID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 {
		t.Fatal("reconciliation replayed a model turn")
	}
}

func TestRejectExpiredApprovalAndAtomicDeleteAll(t *testing.T) {
	f := &scriptedModel{answer: func(_ context.Context, req model.Request) (model.Response, error) {
		return toolCall("bash", `{"content":"true"}`), nil
	}}
	s, db, _ := setup(t, f, 1)
	run := submit(t, s, app.SubmitRequest{Input: "command", Mode: "terminal"})
	wait(t, s, run.ID)
	inputs, err := s.PendingInputs(context.Background(), run.ID)
	if err != nil || len(inputs) != 1 {
		t.Fatalf("inputs %+v %v", inputs, err)
	}
	inputs[0].ExpiresAt = time.Now().Add(-time.Minute).Unix()
	if err := db.Apply(context.Background(), domain.Mutation{Inputs: []*domain.InputRequest{&inputs[0]}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RespondToInput(context.Background(), inputs[0].ID, "", true); err == nil {
		t.Fatal("expired approval accepted")
	}
	if err := s.DeleteAllSessions(context.Background()); err == nil {
		t.Fatal("active run deleted")
	}
	if _, err := s.Session(context.Background(), run.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if result := wait(t, s, run.ID); result.State != domain.Cancelled {
		t.Fatalf("cancel wait: %+v", result)
	}
}

func TestUnknownBlocksQueuedWorkButAllowsCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	f := &scriptedModel{answer: func(ctx context.Context, req model.Request) (model.Response, error) {
		if req.Messages[len(req.Messages)-1].Content != "uncertain" {
			return answer("done"), nil
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return model.Response{}, model.ErrUnknown
	}}
	s, db, _ := setup(t, f, 1)
	first := submit(t, s, app.SubmitRequest{Input: "uncertain", Mode: "ask"})
	<-started
	sameSession := submit(t, s, app.SubmitRequest{SessionID: first.SessionID, Input: "cancel queued", Mode: "ask"})
	otherSession := submit(t, s, app.SubmitRequest{Input: "hold queued", Mode: "ask"})
	close(release)
	if result := wait(t, s, first.ID); result.State != domain.Reconciling {
		t.Fatalf("unknown state: %s", result.State)
	}
	if _, err := s.Submit(context.Background(), app.SubmitRequest{Input: "bypass", Mode: "ask"}); !errors.Is(err, domain.ErrUnknown) {
		t.Fatalf("new session bypassed unknown: %v", err)
	}
	if err := s.Cancel(context.Background(), sameSession.ID); err != nil {
		t.Fatal(err)
	}
	if result := wait(t, s, sameSession.ID); result.State != domain.Cancelled {
		t.Fatalf("queued cancellation: %s", result.State)
	}
	if _, err := s.RetryTask(context.Background(), sameSession.TaskID); !errors.Is(err, domain.ErrUnknown) {
		t.Fatalf("retry bypassed workspace unknown: %v", err)
	}
	queued, err := db.Run(context.Background(), otherSession.ID)
	if err != nil || queued.State != domain.Queued {
		t.Fatalf("queued work advanced: %+v %v", queued, err)
	}
	f.mu.Lock()
	calls := len(f.requests)
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("unknown allowed %d model calls", calls)
	}
	if _, err := s.ReconcileRun(context.Background(), first.ID, "Remote turn inspected; no workspace effects occurred"); err != nil {
		t.Fatal(err)
	}
	if result := wait(t, s, otherSession.ID); result.State != domain.Succeeded {
		t.Fatalf("queue not released: %s", result.State)
	}
	next := submit(t, s, app.SubmitRequest{Input: "accepted after reconciliation", Mode: "ask"})
	if result := wait(t, s, next.ID); result.State != domain.Succeeded {
		t.Fatalf("new work failed: %s", result.State)
	}
}
