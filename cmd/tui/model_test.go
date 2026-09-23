package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/domain"
)

type fakeService struct {
	service
	run            domain.Run
	events         []domain.Event
	inputs         []domain.InputRequest
	calls          []string
	requests       []app.SubmitRequest
	subscribeCount int
	closed         bool
	observeErr     error
	operationErr   error
	answer         string
	approved       bool
	answerID       domain.InputRequestID
}

func newFake() *fakeService {
	return &fakeService{run: domain.Run{ID: 1, RootRunID: 1, SessionID: 8, TaskID: 9, State: domain.Running}}
}
func (f *fakeService) Submit(_ context.Context, r app.SubmitRequest) (*domain.Run, error) {
	f.calls = append(f.calls, "submit")
	f.requests = append(f.requests, r)
	r0 := f.run
	return &r0, f.operationErr
}
func (f *fakeService) ContinueSession(_ context.Context, id domain.SessionID, r app.SubmitRequest) (*domain.Run, error) {
	f.calls = append(f.calls, "continue")
	if r.SessionID != id {
		panic("wrong session")
	}
	f.requests = append(f.requests, r)
	r0 := f.run
	return &r0, f.operationErr
}
func (f *fakeService) Run(context.Context, domain.RunID) (*domain.Run, error) {
	r := f.run
	return &r, nil
}
func (f *fakeService) Runs(context.Context) ([]domain.Run, error) {
	return []domain.Run{f.run, {ID: 2, RootRunID: 1}}, nil
}
func (f *fakeService) Session(context.Context, domain.SessionID) (*domain.Session, error) {
	f.calls = append(f.calls, "session")
	return &domain.Session{ID: 8, Title: "saved conversation"}, f.operationErr
}
func (f *fakeService) ResumeRun(context.Context, domain.RunID) (*domain.Run, error) {
	f.calls = append(f.calls, "resume")
	r := f.run
	return &r, f.operationErr
}
func (f *fakeService) RetryTask(context.Context, domain.TaskID) (*domain.Run, error) {
	f.calls = append(f.calls, "retry")
	r := f.run
	return &r, f.operationErr
}
func (f *fakeService) RespondToInput(_ context.Context, id domain.InputRequestID, text string, approved bool) (*domain.Run, error) {
	f.calls = append(f.calls, "respond")
	f.answerID, f.answer, f.approved = id, text, approved
	r := f.run
	return &r, f.operationErr
}
func (f *fakeService) Subscribe(domain.RunID) (<-chan domain.Event, func()) {
	f.subscribeCount++
	ch := make(chan domain.Event)
	if f.closed {
		close(ch)
	}
	return ch, func() {}
}
func (f *fakeService) Observe(_ context.Context, id domain.RunID, after int64) ([]domain.Event, error) {
	if f.observeErr != nil {
		return nil, f.observeErr
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
func (f *fakeService) PendingInputs(context.Context, domain.RunID) ([]domain.InputRequest, error) {
	return f.inputs, nil
}
func (f *fakeService) Cancel(ctx context.Context, _ domain.RunID) error {
	if ctx.Err() != nil {
		panic("cancelled persistence context")
	}
	f.calls = append(f.calls, "cancel")
	f.run.State = domain.Cancelled
	return nil
}
func (f *fakeService) Wait(context.Context, domain.RunID) (*domain.Run, error) {
	f.calls = append(f.calls, "wait")
	r := f.run
	return &r, nil
}

func update(m model, msg tea.Msg) (model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(model), cmd
}
func start(t *testing.T, f *fakeService) model {
	t.Helper()
	m := newModel(f)
	m, _ = update(m, tea.WindowSizeMsg{Width: 90, Height: 30})
	next, cmd := m.dispatch("first task")
	m = next.(model)
	m, _ = update(m, cmd())
	return m
}

func TestConstructionAndHelpHaveNoConfigurationIO(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WORKSPACE", root)
	t.Setenv("HOME", filepath.Join(root, "missing-home"))
	_ = newModel(newFake())
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = []string{"orca-tui", "--help"}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(root)
	if len(files) != 0 {
		t.Fatal("help or construction opened environment")
	}
}

func TestSessionContinuationAndRecoveryAreDifferentOperations(t *testing.T) {
	f := newFake()
	m := start(t, f)
	defer func() { m.stopObserving() }()
	f.run.State = domain.Succeeded
	m, _ = update(m, m.snapshot()())
	next, cmd := m.dispatch("second task")
	m = next.(model)
	m, _ = update(m, cmd())
	if !reflect.DeepEqual(f.calls, []string{"submit", "continue"}) || len(f.requests) != 2 || f.requests[1].SessionID != 8 || f.requests[1].Input != "second task" {
		t.Fatalf("%v %+v", f.calls, f.requests)
	}
	f.run.State = domain.Interrupted
	m, _ = update(m, m.snapshot()())
	next, cmd = m.dispatch("/resume 1")
	m = next.(model)
	m, _ = update(m, cmd())
	if f.calls[len(f.calls)-1] != "resume" || len(f.requests) != 2 {
		t.Fatal("resume submitted a new task")
	}
}

func TestTUICommandsValidateAndPreserveResponse(t *testing.T) {
	for _, text := range []string{"/resume", "/resume 0", "/resume +1", "/resume 9223372036854775808", "/resume 1 extra", "/respond 1", "/approve 1 extra", "/mode invalid", "/help extra", "/runs extra", "/typo"} {
		f := newFake()
		m := newModel(f)
		m.input.SetValue(text)
		next, cmd := m.dispatch(text)
		m = next.(model)
		if cmd != nil || len(f.calls) != 0 || m.input.Value() != text {
			t.Fatalf("invalid command dispatched or discarded draft: %q", text)
		}
	}
	for _, tc := range []struct {
		text     string
		call     string
		response string
		approved bool
	}{
		{"/session 8", "session", "", false}, {"/resume 1", "resume", "", false}, {"/retry 9", "retry", "", false},
		{"/approve 12", "respond", "", true}, {"/reject 12", "respond", "", false}, {"/respond 12 keep  spaces", "respond", "keep  spaces", false},
	} {
		f := newFake()
		m := newModel(f)
		next, cmd := m.dispatch(tc.text)
		m = next.(model)
		m, _ = update(m, cmd())
		m.stopObserving()
		if f.calls[0] != tc.call || f.answer != tc.response || f.approved != tc.approved {
			t.Fatalf("%q %+v", tc.text, f)
		}
	}
}

func TestChildWaitKeepsObservingAndHumanWaitShowsID(t *testing.T) {
	f := newFake()
	m := start(t, f)
	defer func() { m.stopObserving() }()
	f.run.State = domain.Waiting
	m, cmd := update(m, m.snapshot()())
	if cmd == nil || m.stopWatch == nil || !m.executing() || !strings.Contains(m.status, "child") {
		t.Fatal("child wait stopped observation")
	}
	f.inputs = []domain.InputRequest{{ID: 17, RunID: 2, Kind: "approval", Prompt: "workspace=/tmp tool=bash exact arguments", State: "pending"}}
	m, cmd = update(m, m.snapshot()())
	if cmd == nil || m.stopWatch == nil || m.executing() || !strings.Contains(m.status, "ID=17") {
		t.Fatalf("%s", m.status)
	}
	history := strings.Join(m.chatHistory, "\n")
	if !strings.Contains(history, "/approve 17") || !strings.Contains(history, "exact arguments") {
		t.Fatal(history)
	}
	m, _ = update(m, m.snapshot()())
	if strings.Count(strings.Join(m.chatHistory, "\n"), "/approve 17") != 1 {
		t.Fatal("duplicated input prompt")
	}
	next, cmd := m.dispatch("/approve 17")
	m = next.(model)
	m, _ = update(m, cmd())
	if f.answerID != 17 || !f.approved {
		t.Fatal("approval not routed")
	}
}

func TestObservationRecoveryReconnectionAndStaleGeneration(t *testing.T) {
	f := newFake()
	m := start(t, f)
	defer func() { m.stopObserving() }()
	f.observeErr = errors.New("temporary disconnect")
	m, cmd := update(m, m.snapshot()())
	if cmd == nil || m.stopWatch == nil || len(m.cursors) != 0 {
		t.Fatal("observer error stopped execution or advanced cursor")
	}
	f.observeErr = nil
	f.events = []domain.Event{{Sequence: 1, RunID: 1, InvocationID: 10, AssistantSequence: 3, Kind: "message", Content: "durable one"}, {Sequence: 2, RunID: 2, InvocationID: 20, AssistantSequence: 3, Kind: "message", Content: "durable child"}}
	m, _ = update(m, m.snapshot()())
	if m.cursors[1] != 1 || m.cursors[2] != 2 {
		t.Fatal(m.cursors)
	}
	m, _ = update(m, deltaMsg{generation: m.generation, closed: true})
	if m.deltas != nil {
		t.Fatal("closed subscription retained")
	}
	m, cmd = update(m, pollMsg{m.generation})
	if cmd == nil || f.subscribeCount != 2 {
		t.Fatal("did not resubscribe")
	}
	m, _ = update(m, m.snapshot()())
	if strings.Count(strings.Join(m.chatHistory, "\n"), "durable one") != 1 {
		t.Fatal("reconnect duplicated durable history")
	}
	before := m.status
	m, _ = update(m, snapshotMsg{generation: m.generation - 1, err: errors.New("stale")})
	if m.status != before {
		t.Fatal("stale result applied")
	}
}

func TestPartitionedDeltasReplaceWithCompleteDurableMessage(t *testing.T) {
	f := newFake()
	m := start(t, f)
	defer func() { m.stopObserving() }()
	m.runIDs[2] = true
	events := []domain.Event{
		{RunID: 1, InvocationID: 10, AssistantSequence: 3, Kind: "delta", Content: "par"},
		{RunID: 2, InvocationID: 10, AssistantSequence: 3, Kind: "delta", Content: "child"},
		{RunID: 1, InvocationID: 10, AssistantSequence: 5, Kind: "delta", Content: "later"},
	}
	for _, e := range events {
		m, _ = update(m, deltaMsg{generation: m.generation, event: e})
	}
	if len(m.previews) != 3 || !strings.Contains(m.viewport.View(), "par") {
		t.Fatal("actual delta text missing or streams conflated")
	}
	final := events[0]
	final.Kind = "message"
	final.Sequence = 1
	final.Content = "complete text with missing middle"
	f.events = []domain.Event{final}
	m, _ = update(m, m.snapshot()())
	if len(m.previews) != 2 || strings.Count(strings.Join(m.chatHistory, "\n"), final.Content) != 1 {
		t.Fatal("final was truncated, lost or duplicated")
	}
	m, _ = update(m, deltaMsg{generation: m.generation, event: events[0]})
	if len(m.previews) != 2 {
		t.Fatal("late delta resurrected confirmed stream")
	}
	final.Sequence = 2
	final.AssistantSequence = 5
	final.Content = "later complete"
	f.events = append(f.events, final)
	m, _ = update(m, m.snapshot()())
	if len(m.previews) != 1 {
		t.Fatal("confirmed wrong partition")
	}
	f.run.State = domain.Succeeded
	m, _ = update(m, m.snapshot()())
	if m.stopWatch != nil || len(m.previews) != 0 {
		t.Fatal("unconfirmed preview survived terminal state")
	}
}

func TestPreviewBoundsDoNotTruncateDurableFinal(t *testing.T) {
	f := newFake()
	m := start(t, f)
	defer func() { m.stopObserving() }()
	for i := 1; i <= 1000; i++ {
		m.addDelta(domain.Event{RunID: 1, InvocationID: domain.InvocationID(i), AssistantSequence: 3, Kind: "delta", Content: strings.Repeat("x", maxPreviewBytes*2)})
	}
	if len(m.previews) > maxPreviews || len(m.confirmed) > maxStreamInvocations || len(m.previewOrder) > maxPreviews {
		t.Fatal("unbounded streaming state")
	}
	for _, text := range m.previews {
		if len(text) > maxPreviewBytes {
			t.Fatal("unbounded preview")
		}
	}
	text := strings.Repeat("complete", maxPreviewBytes)
	f.events = []domain.Event{{Sequence: 1, RunID: 1, InvocationID: 1, AssistantSequence: 3, Kind: "message", Content: text}}
	m, _ = update(m, m.snapshot()())
	if strings.Count(strings.Join(m.chatHistory, "\n"), text) != 1 {
		t.Fatal("durable final truncated")
	}
}

func TestResizePreservesDraftHistoryAndTerminalSafety(t *testing.T) {
	m := newModel(newFake())
	m.append("answer\x1b[2J\r\a\u202e\u2066中文\tlong text")
	m.input.SetValue("unsent 中文 draft")
	history := append([]string(nil), m.chatHistory...)
	draft := m.input.Value()
	m.status = "error\x1b[2J\nforged"
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 2, Height: 2}, {Width: 1, Height: 1}, {Width: 17, Height: 8}, {Width: 80, Height: 24}} {
		m, _ = update(m, size)
		view := m.View()
		if !reflect.DeepEqual(m.chatHistory, history) || m.input.Value() != draft {
			t.Fatal("resize lost draft or history")
		}
		lines := strings.Split(view, "\n")
		if len(lines) > size.Height {
			t.Fatal("height overflow")
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size.Width {
				t.Fatalf("width overflow: %q", line)
			}
		}
		if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\u202e") || strings.Contains(view, "\u2066") || strings.Contains(view, "\a") {
			t.Fatalf("terminal injection: %q", view)
		}
	}
}

func TestExitDuringSubmissionCancelsThenWaits(t *testing.T) {
	f := newFake()
	m := newModel(f)
	next, submit := m.dispatch("task")
	m = next.(model)
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil || !m.quitting {
		t.Fatal("quit before submission returned")
	}
	m, cmd = update(m, submit())
	if cmd == nil || !m.cancelPending {
		t.Fatal("did not cancel newly submitted run")
	}
	m, quit := update(m, cmd())
	if quit == nil {
		t.Fatal("did not quit after wait")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatal("not quit")
	}
	if !reflect.DeepEqual(f.calls, []string{"submit", "cancel", "wait"}) {
		t.Fatal(f.calls)
	}
}

func TestCompletedConfirmationAndReattachDoNotDuplicateAnswers(t *testing.T) {
	f := newFake()
	m := start(t, f)
	defer func() { m.stopObserving() }()
	f.events = []domain.Event{
		{Sequence: 1, RunID: 1, InvocationID: 10, AssistantSequence: 3, Kind: "message", Content: "unique final answer"},
		{Sequence: 2, RunID: 1, Kind: "completed", Content: "unique final answer"},
	}
	f.run.State = domain.Succeeded
	m, _ = update(m, m.snapshot()())
	next, cmd := m.dispatch("/watch 1")
	m = next.(model)
	m, _ = update(m, cmd())
	m, _ = update(m, m.snapshot()())
	if strings.Count(strings.Join(m.chatHistory, "\n"), "unique final answer") != 1 {
		t.Fatal("completion or reattachment duplicated the answer")
	}
}

func TestOperationGateJoinsLateSubmissionAndBlocksUnstartedWork(t *testing.T) {
	g := &operationGroup{}
	started, release := make(chan struct{}), make(chan struct{})
	cmd := g.track(func() tea.Msg { close(started); <-release; return operationMsg{run: &domain.Run{ID: 42}} })
	go cmd()
	<-started
	finished := make(chan domain.RunID, 1)
	go func() { finished <- g.finish() }()
	select {
	case <-finished:
		t.Fatal("closed before submission finished")
	default:
	}
	close(release)
	if id := <-finished; id != 42 {
		t.Fatalf("lost late run identity %d", id)
	}
	called := false
	late := g.track(func() tea.Msg { called = true; return nil })
	late()
	if called {
		t.Fatal("submitted work after environment shutdown began")
	}
	f := newFake()
	if err := stopActiveRun(t.Context(), f, 1); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, []string{"cancel", "wait"}) {
		t.Fatal(f.calls)
	}
}

func TestErrorsAreNotSuccess(t *testing.T) {
	f := newFake()
	f.operationErr = errors.New("provider rejected request")
	m := newModel(f)
	next, cmd := m.dispatch("task")
	m = next.(model)
	m, _ = update(m, cmd())
	if m.busy || m.status != "error" || !strings.Contains(strings.Join(m.chatHistory, "\n"), "provider rejected request") {
		t.Fatal("operation failure hidden")
	}
	f.operationErr = nil
	m = start(t, f)
	defer func() { m.stopObserving() }()
	f.run.State = domain.Failed
	f.run.Error = "provider disconnected"
	m, _ = update(m, m.snapshot()())
	if m.state != domain.Failed || !strings.Contains(strings.Join(m.chatHistory, "\n"), "provider disconnected") {
		t.Fatal("failed run shown as success")
	}
}
