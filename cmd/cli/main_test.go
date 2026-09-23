package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/domain"
)

type appSubmit = app.SubmitRequest

type fakeService struct {
	service
	root      domain.Run
	all       []domain.Run
	events    []domain.Event
	inputs    []domain.InputRequest
	last      string
	onSubmit  func(appSubmit)
	wait      func(context.Context, domain.RunID) (*domain.Run, error)
	observe   func(context.Context, domain.RunID, int64) ([]domain.Event, error)
	subscribe func() (<-chan domain.Event, func())
	cancel    func(context.Context, domain.RunID) error
	deleted   []domain.SessionID
	revised   string
	note      string
	response  string
	approved  bool
	inputID   domain.InputRequestID
}

func newFake() *fakeService {
	r := domain.Run{ID: 1, RootRunID: 1, SessionID: 8, TaskID: 9, State: domain.Succeeded}
	return &fakeService{root: r, all: []domain.Run{r}}
}
func (f *fakeService) Submit(_ context.Context, r appSubmit) (*domain.Run, error) {
	f.last = "submit"
	if f.onSubmit != nil {
		f.onSubmit(r)
	}
	r0 := f.root
	return &r0, nil
}
func (f *fakeService) ContinueSession(_ context.Context, id domain.SessionID, r appSubmit) (*domain.Run, error) {
	f.last = fmt.Sprintf("continue:%d", id)
	if f.onSubmit != nil {
		f.onSubmit(r)
	}
	r0 := f.root
	return &r0, nil
}
func (f *fakeService) Run(context.Context, domain.RunID) (*domain.Run, error) {
	r := f.root
	return &r, nil
}
func (f *fakeService) Runs(context.Context) ([]domain.Run, error) { return f.all, nil }
func (f *fakeService) Wait(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	if f.wait != nil {
		return f.wait(ctx, id)
	}
	r := f.root
	return &r, nil
}
func (f *fakeService) Observe(ctx context.Context, id domain.RunID, after int64) ([]domain.Event, error) {
	if f.observe != nil {
		return f.observe(ctx, id, after)
	}
	var result []domain.Event
	for _, e := range f.events {
		if e.RunID == id && e.Sequence > after {
			result = append(result, e)
			if len(result) == 2 {
				break
			}
		}
	}
	return result, nil
}
func (f *fakeService) Subscribe(domain.RunID) (<-chan domain.Event, func()) {
	if f.subscribe != nil {
		return f.subscribe()
	}
	return make(chan domain.Event), func() {}
}
func (f *fakeService) PendingInputs(context.Context, domain.RunID) ([]domain.InputRequest, error) {
	return f.inputs, nil
}
func (f *fakeService) ResumeRun(_ context.Context, id domain.RunID) (*domain.Run, error) {
	f.last = fmt.Sprintf("resume:%d", id)
	return f.Run(context.Background(), id)
}
func (f *fakeService) RetryTask(_ context.Context, id domain.TaskID) (*domain.Run, error) {
	f.last = fmt.Sprintf("retry:%d", id)
	return f.Run(context.Background(), 1)
}
func (f *fakeService) RespondToInput(_ context.Context, id domain.InputRequestID, text string, approved bool) (*domain.Run, error) {
	f.inputID, f.response, f.approved = id, text, approved
	return f.Run(context.Background(), 1)
}
func (f *fakeService) Cancel(ctx context.Context, id domain.RunID) error {
	if f.cancel != nil {
		return f.cancel(ctx, id)
	}
	f.last = fmt.Sprintf("cancel:%d", id)
	return nil
}
func (f *fakeService) Sessions(context.Context) ([]domain.Session, error) {
	return []domain.Session{{ID: 8}}, nil
}
func (f *fakeService) Session(context.Context, domain.SessionID) (*domain.Session, error) {
	return &domain.Session{ID: 8}, nil
}
func (f *fakeService) DeleteSession(_ context.Context, id domain.SessionID) error {
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *fakeService) DeleteAllSessions(context.Context) error { f.last = "delete-all"; return nil }
func (f *fakeService) UpdateTask(_ context.Context, id domain.TaskID, input, title string) (*domain.Task, error) {
	f.last = fmt.Sprintf("revise:%d", id)
	f.revised = input
	return &domain.Task{ID: id, Input: input, Title: title}, nil
}
func (f *fakeService) ReconcileRun(_ context.Context, id domain.RunID, note string) (*domain.Run, error) {
	f.last = fmt.Sprintf("reconcile:%d", id)
	f.note = note
	r := f.root
	r.State = domain.Failed
	return &r, nil
}

func executeArgs(t *testing.T, f *fakeService, args ...string) string {
	t.Helper()
	out := &bytes.Buffer{}
	a, err := parseArgs(args, out)
	if err != nil {
		t.Fatal(err)
	}
	req := app.SubmitRequest{Input: a.Prompt, SessionID: domain.SessionID(a.SessionId)}
	if err := execute(t.Context(), f, nil, a, req, out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestServiceRouting(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"goal"}, "submit"}, {[]string{"-session", "8", "follow up"}, "continue:8"}, {[]string{"run", "resume", "1"}, "resume:1"},
		{[]string{"run", "retry", "9"}, "retry:9"}, {[]string{"run", "cancel", "1"}, "cancel:1"},
	} {
		f := newFake()
		executeArgs(t, f, tc.args...)
		if f.last != tc.want {
			t.Fatalf("%v: %s", tc.args, f.last)
		}
	}
	for _, cmd := range []string{"approve", "reject", "respond"} {
		f := newFake()
		args := []string{"run", cmd, "12"}
		if cmd == "respond" {
			args = append(args, "keep  spacing")
		}
		executeArgs(t, f, args...)
		if f.inputID != 12 || f.approved != (cmd == "approve") || (cmd == "respond" && f.response != "keep  spacing") {
			t.Fatalf("%s %+v", cmd, f)
		}
	}
}

func TestManagementCommandsDoNotExecute(t *testing.T) {
	f := newFake()
	executeArgs(t, f, "session", "rm", "1", "2", "1")
	if !reflect.DeepEqual(f.deleted, []domain.SessionID{1, 2}) {
		t.Fatal(f.deleted)
	}
	executeArgs(t, f, "session", "rm", "--all")
	if f.last != "delete-all" {
		t.Fatal(f.last)
	}
	executeArgs(t, f, "task", "revise", "9", "revised input")
	if f.last != "revise:9" || f.revised != "revised input" {
		t.Fatalf("%+v", f)
	}
	output := executeArgs(t, f, "run", "reconcile", "1", "--note", "checked external system; not replayed")
	if f.last != "reconcile:1" || f.note == "" || !strings.Contains(output, "No operation was replayed") {
		t.Fatal(output)
	}
	out := &bytes.Buffer{}
	a := &CliArgs{SubCommand: "session", SubArgs: []string{"import", "source.json", "--owner", "workspace"}}
	err := execute(t.Context(), f, func(_ context.Context, path, owner string) (domain.SessionID, error) {
		if path != "source.json" || owner != "workspace" {
			t.Fatal(path, owner)
		}
		return 8, nil
	}, a, app.SubmitRequest{}, out)
	if err != nil || !strings.Contains(out.String(), "original JSON may still contain secrets") || !strings.Contains(out.String(), "NOT restored") {
		t.Fatalf("%s %v", out, err)
	}
}

func TestDurablePaginationAndPerRunCursors(t *testing.T) {
	f := newFake()
	f.all = append(f.all, domain.Run{ID: 2, RootRunID: 1}, domain.Run{ID: 3, RootRunID: 99})
	for i := int64(1); i <= 8; i++ {
		f.events = append(f.events, domain.Event{Sequence: i, RunID: domain.RunID(1 + i%2), Kind: "message", Content: fmt.Sprint(i)})
	}
	cursors := map[domain.RunID]int64{}
	events, ids, err := readEvents(t.Context(), f, 1, 2, cursors)
	if err != nil || len(events) != 6 || len(ids) != 2 || cursors[1] != 8 || cursors[2] != 7 {
		t.Fatalf("%+v %+v %v", events, cursors, err)
	}
	for i, e := range events {
		if e.Sequence != int64(i+3) {
			t.Fatal(events)
		}
	}
	again, _, err := readEvents(t.Context(), f, 1, 2, cursors)
	if err != nil || len(again) != 0 {
		t.Fatalf("%v %v", again, err)
	}
	// A failed batch must not advance any run's externally-visible cursor.
	f.observe = func(_ context.Context, id domain.RunID, after int64) ([]domain.Event, error) {
		if id == 2 {
			return nil, errors.New("offline")
		}
		if after == 0 {
			return []domain.Event{{RunID: id, Sequence: 1}}, nil
		}
		return nil, nil
	}
	cursors = map[domain.RunID]int64{}
	_, _, err = readEvents(t.Context(), f, 1, 0, cursors)
	if err == nil || len(cursors) != 0 {
		t.Fatalf("%v %v", cursors, err)
	}
}

func TestWaitingInputAndFailure(t *testing.T) {
	f := newFake()
	f.root.State = domain.Waiting
	f.inputs = []domain.InputRequest{{ID: 15, RunID: 1, Kind: "approval", Prompt: "exact arguments"}}
	out := &bytes.Buffer{}
	if err := followRun(t.Context(), f, &f.root, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "orca run approve 15") || !strings.Contains(out.String(), "not a failure") {
		t.Fatal(out)
	}
	f = newFake()
	f.root.State = domain.Failed
	f.root.Error = "provider failed"
	if err := followRun(t.Context(), f, &f.root, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "provider failed") {
		t.Fatal(err)
	}
}

func TestChildWaitAndSubscriptionReconnect(t *testing.T) {
	f := newFake()
	var waits atomic.Int32
	var subscriptions atomic.Int32
	reconnected := make(chan struct{})
	f.subscribe = func() (<-chan domain.Event, func()) {
		ch := make(chan domain.Event)
		if subscriptions.Add(1) == 1 {
			close(ch)
		} else {
			close(reconnected)
		}
		return ch, func() {}
	}
	f.wait = func(ctx context.Context, _ domain.RunID) (*domain.Run, error) {
		r := f.root
		if waits.Add(1) == 1 {
			r.State = domain.Waiting
			return &r, nil
		}
		select {
		case <-reconnected:
			return &r, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := followRun(ctx, f, &f.root, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if subscriptions.Load() < 2 || waits.Load() < 2 {
		t.Fatal("stopped during child wait")
	}
}

func TestCancellationWaitsAndJoinsObserver(t *testing.T) {
	f := newFake()
	cancelled := make(chan struct{})
	var once sync.Once
	var waits atomic.Int32
	f.cancel = func(ctx context.Context, _ domain.RunID) error {
		if ctx.Err() != nil {
			t.Error("cancel used cancelled persistence context")
		}
		once.Do(func() { close(cancelled) })
		return nil
	}
	f.wait = func(ctx context.Context, _ domain.RunID) (*domain.Run, error) {
		waits.Add(1)
		select {
		case <-cancelled:
			r := f.root
			r.State = domain.Cancelled
			return &r, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := followRun(ctx, f, &f.root, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("did not cancel")
	}
	if waits.Load() < 2 {
		t.Fatal("did not wait again after Cancel")
	}
}

func TestCompletionConfirmsRatherThanRepeatsDurableAnswer(t *testing.T) {
	out := &bytes.Buffer{}
	d := newStreamDisplay(out)
	if err := d.events([]domain.Event{{Sequence: 1, RunID: 1, InvocationID: 2, AssistantSequence: 3, Kind: "message", Content: "unique answer"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.events([]domain.Event{{Sequence: 2, RunID: 1, Kind: "completed", Content: "unique answer"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "unique answer") != 1 || !strings.Contains(out.String(), "confirms") {
		t.Fatal(out)
	}
}

func TestStreamConfirmationAndSafeDurableOutput(t *testing.T) {
	out := &bytes.Buffer{}
	d := newStreamDisplay(out)
	e := domain.Event{RunID: 1, InvocationID: 2, AssistantSequence: 3, Kind: "delta", Content: "partial"}
	if err := d.delta(e); err != nil || out.Len() != 0 {
		t.Fatal("pipe must contain only durable events")
	}
	d.width = 180
	if err := d.delta(e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "partial") {
		t.Fatal("missing actual delta")
	}
	final := e
	final.Kind = "message"
	final.Sequence = 5
	final.Content = "complete\x1b[2J\u202eanswer"
	if err := d.events([]domain.Event{final}); err != nil {
		t.Fatal(err)
	}
	if len(d.previews) != 0 {
		t.Fatal("preview not replaced")
	}
	n := out.Len()
	d.delta(e)
	if out.Len() != n {
		t.Fatal("late delta resurrected final")
	}
	plain := &bytes.Buffer{}
	if err := printEvents(plain, []domain.Event{final}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "\x1b") || strings.Contains(plain.String(), "\u202e") {
		t.Fatal("terminal injection")
	}
}
