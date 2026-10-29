package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/domain"
)

type appSubmit = app.SubmitRequest

func TestParseSubmission(t *testing.T) {
	a, err := parseArgs([]string{"-p", "fake", "-m", "local", "-mode", "ask", "-f", "one", "--file", "two", "-sp", "system", "goal", "text"}, &bytes.Buffer{})
	want := &CliArgs{Provider: "fake", Model: "local", Mode: "ask", Files: []string{"one", "two"}, SystemPrompt: "system", Prompt: "goal text"}
	if err != nil || !reflect.DeepEqual(a, want) {
		t.Fatalf("%+v %v", a, err)
	}
	a, err = parseArgs([]string{"--", "-literal"}, &bytes.Buffer{})
	if err != nil || a.Prompt != "-literal" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestStrictArguments(t *testing.T) {
	invalid := [][]string{
		{},
		{"-p", "only", "goal"}, {"-m", "only", "goal"}, {"-mode", "unknown", "goal"}, {"-unknown", "goal"}, {"-f"}, {" "},
	}
	for _, argv := range invalid {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			if a, err := parseArgs(argv, &bytes.Buffer{}); err == nil {
				t.Fatalf("accepted %+v", a)
			}
		})
	}
	// 子命令已经不存在，任何以子命令形式给出的参数都只是提示词
	a, err := parseArgs([]string{"run", "list"}, &bytes.Buffer{})
	if err != nil || a.Prompt != "run list" {
		t.Fatalf("%+v %v", a, err)
	}
}

func setArgs(t *testing.T, args ...string) {
	t.Helper()
	old := os.Args
	os.Args = append([]string{"orca"}, args...)
	t.Cleanup(func() { os.Args = old })
}

func TestHelpAndInputErrorsHaveNoConfigurationIO(t *testing.T) {
	for _, argv := range [][]string{{"--help"}, {"-mode", "ask", "--help"}} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("WORKSPACE", root)
			t.Setenv("HOME", filepath.Join(root, "absent-home"))
			setArgs(t, argv...)
			if err := run(); err != nil {
				t.Fatal(err)
			}
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 0 {
				t.Fatalf("help performed I/O: %v %v", files, err)
			}
		})
	}
	for _, flag := range []string{"-f", "-sp"} {
		t.Run(flag, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("WORKSPACE", root)
			t.Setenv("HOME", filepath.Join(root, "absent-home"))
			setArgs(t, flag, filepath.Join(root, "missing"), "goal")
			if err := run(); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%v", err)
			}
			files, _ := os.ReadDir(root)
			if len(files) != 0 {
				t.Fatal("opened environment before reading files")
			}
		})
	}
}

func TestPrepareReadsAllFilesBeforeSubmit(t *testing.T) {
	root := t.TempDir()
	attachment := filepath.Join(root, "source.go")
	system := filepath.Join(root, "system.txt")
	if err := os.WriteFile(attachment, []byte("package sample\n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(system, []byte("custom system\n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &CliArgs{Prompt: "explain", Files: []string{attachment}, SystemPrompt: system, Provider: "fake", Model: "local"}
	req, err := prepareRequest(a)
	if err != nil {
		t.Fatal(err)
	}
	if req.Input != "explain" || req.SystemPrompt != "custom system\n\n" || req.Model.Provider != "fake" || !strings.Contains(req.Prompt, "package sample\n</file>\n\nexplain") {
		t.Fatalf("%+v", req)
	}
	os.Remove(attachment)
	a.Files = []string{attachment}
	if p, err := userPrompt(a); p != "" || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%q %v", p, err)
	}
}

type fakeService struct {
	mu      sync.Mutex
	all     []domain.Run
	events  []domain.Event
	waitFn  func(context.Context, domain.RunID) (*domain.Run, error)
	inputs  []domain.InputRequest
	respond []string
	cancelN int
}

func (f *fakeService) Submit(context.Context, appSubmit) (*domain.Run, error) {
	return nil, errors.New("not used")
}

func (f *fakeService) ContinueSession(context.Context, domain.SessionID, appSubmit) (*domain.Run, error) {
	return nil, errors.New("not used")
}

func (f *fakeService) Runs(context.Context) ([]domain.Run, error) { return f.all, nil }

func (f *fakeService) Wait(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	return f.waitFn(ctx, id)
}

func (f *fakeService) Observe(_ context.Context, id domain.RunID, after int64) ([]domain.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Event
	for _, e := range f.events {
		if e.RunID == id && e.Sequence > after {
			out = append(out, e)
			if len(out) == 2 {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeService) Subscribe(domain.RunID) (<-chan domain.Event, func()) {
	ch := make(chan domain.Event)
	return ch, func() { close(ch) }
}

func (f *fakeService) PendingInputs(context.Context, domain.RunID) ([]domain.InputRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inputs, nil
}

func (f *fakeService) RespondToInput(_ context.Context, id domain.InputRequestID, text string, approved bool) (*domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = nil
	f.respond = append(f.respond, text)
	return &domain.Run{ID: domain.RunID(id), State: domain.Running}, nil
}

func (f *fakeService) Cancel(context.Context, domain.RunID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelN++
	return nil
}

// TestReadEventsMergesRootAndChildren 验证父/子 run 的事件按序列归并，且游标不重复读取。
func TestReadEventsMergesRootAndChildren(t *testing.T) {
	f := &fakeService{
		all: []domain.Run{{ID: 1, RootRunID: 1}, {ID: 2, RootRunID: 1}, {ID: 3, RootRunID: 3}},
		events: []domain.Event{
			{Sequence: 1, RunID: 1, Kind: "state", Content: "queued"},
			{Sequence: 2, RunID: 2, Kind: "state", Content: "queued"},
			{Sequence: 3, RunID: 1, Kind: "message", Content: "done"},
		},
	}
	cursors := map[domain.RunID]int64{}
	events, ids, err := readEvents(context.Background(), f, 1, cursors)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Sequence != 1 || events[2].Sequence != 3 {
		t.Fatalf("%+v", events)
	}
	if !ids[2] || ids[3] {
		t.Fatalf("ids %+v", ids)
	}
	again, _, err := readEvents(context.Background(), f, 1, cursors)
	if err != nil || len(again) != 0 {
		t.Fatalf("%+v %v", again, err)
	}
}

// TestFollowRunAnswersWaitingInput 验证等待中的输入会在同一个进程内被读入并回复。
func TestFollowRunAnswersWaitingInput(t *testing.T) {
	var calls int
	waiting := domain.Run{ID: 1, RootRunID: 1, State: domain.Waiting}
	done := domain.Run{ID: 1, RootRunID: 1, State: domain.Succeeded}
	f := &fakeService{all: []domain.Run{done}}
	f.inputs = []domain.InputRequest{{ID: 7, RunID: 1, Kind: "input", Prompt: `{"kind":"input"}`, State: "pending"}}
	f.waitFn = func(context.Context, domain.RunID) (*domain.Run, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		calls++
		if calls == 1 {
			r := waiting
			return &r, nil
		}
		r := done
		return &r, nil
	}
	out := &bytes.Buffer{}
	err := followRun(context.Background(), f, &domain.Run{ID: 1, RootRunID: 1}, out, bufio.NewReader(strings.NewReader("42\n")))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.respond) != 1 || f.respond[0] != "42" {
		t.Fatalf("%+v", f.respond)
	}
	if !strings.Contains(out.String(), "state=succeeded") {
		t.Fatalf("%q", out.String())
	}
}

// TestFollowRunRejectsApproval 验证审批回答 n 时传 approved=false。
func TestFollowRunRejectsApproval(t *testing.T) {
	var calls int
	waiting := domain.Run{ID: 1, RootRunID: 1, State: domain.Waiting}
	failed := domain.Run{ID: 1, RootRunID: 1, State: domain.Failed, Error: "declined"}
	f := &fakeService{all: []domain.Run{failed}}
	f.inputs = []domain.InputRequest{{ID: 9, RunID: 1, Kind: "approval", Prompt: `{"kind":"approval"}`, State: "pending"}}
	f.waitFn = func(context.Context, domain.RunID) (*domain.Run, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		calls++
		if calls == 1 {
			r := waiting
			return &r, nil
		}
		r := failed
		return &r, nil
	}
	err := followRun(context.Background(), f, &domain.Run{ID: 1, RootRunID: 1}, &bytes.Buffer{}, bufio.NewReader(strings.NewReader("n\n")))
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("%v", err)
	}
	if len(f.respond) != 1 || f.respond[0] != "" {
		t.Fatalf("%+v", f.respond)
	}
}

// TestFollowRunCancelsOnSignal 验证上下文取消会取消运行并等待其收敛。
func TestFollowRunCancelsOnSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	var once sync.Once
	f := &fakeService{all: []domain.Run{{ID: 1, RootRunID: 1}}}
	f.waitFn = func(context.Context, domain.RunID) (*domain.Run, error) {
		<-release
		return &domain.Run{ID: 1, RootRunID: 1, State: domain.Cancelled}, nil
	}
	go func() {
		once.Do(cancel)
		close(release)
	}()
	if err := followRun(ctx, f, &domain.Run{ID: 1, RootRunID: 1}, &bytes.Buffer{}, bufio.NewReader(strings.NewReader(""))); err != nil {
		t.Fatal(err)
	}
	if f.cancelN == 0 {
		t.Fatal("cancel was not called")
	}
}

// TestPromptRejectsEmptyAnswer 验证空回答被拒绝。
func TestPromptRejectsEmptyAnswer(t *testing.T) {
	_, _, err := prompt(&bytes.Buffer{}, bufio.NewReader(strings.NewReader("\n")), domain.InputRequest{Kind: "input"})
	if err == nil {
		t.Fatal("accepted empty answer")
	}
	_, approved, err := prompt(&bytes.Buffer{}, bufio.NewReader(strings.NewReader("yes\n")), domain.InputRequest{Kind: "approval"})
	if err != nil || !approved {
		t.Fatalf("%v %v", approved, err)
	}
}
