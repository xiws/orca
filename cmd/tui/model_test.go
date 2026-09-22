package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/internal/session"
)

type testExecutor func(*domain.Task, *core.Invocation) (string, error)

func (f testExecutor) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	return f(task, inv)
}

func newTestModel(t *testing.T) (model, chan string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("WORKSPACE", root)
	chunks := make(chan string, 100)
	m := model{
		record:   &session.Record{Session: domain.NewSession(root)},
		provider: llm.ModelInfo{Provider: "fake", ModelID: "test", SupportsTools: true},
		msgCh:    chunks, events: make(chan tea.Msg, 100), done: make(chan struct{}),
		status: "Ready",
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(model), chunks
}

func executeTurn(t *testing.T, m model, input string) (model, agentResultMsg) {
	t.Helper()
	m.state = stateThinking
	m.chatHistory = appendChat(m.chatHistory, "> "+input)
	finished := make(chan struct{})
	run := m.runAgent(input)
	go func() {
		run()
		close(finished)
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case msg := <-m.events:
			updated, _ := m.Update(msg)
			m = updated.(model)
			if result, ok := msg.(agentResultMsg); ok {
				select {
				case <-finished:
				case <-deadline.C:
					t.Fatal("execution command did not finish")
				}
				if len(m.events) != 0 || len(m.msgCh) != 0 {
					t.Fatal("stream chunks remained after final result")
				}
				return m, result
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for execution events")
		}
	}
}

func TestConsecutiveTurnsPersistDistinctTasks(t *testing.T) {
	m, chunks := newTestModel(t)
	sessionID := m.record.Session.ID
	m.executor = testExecutor(func(task *domain.Task, inv *core.Invocation) (string, error) {
		if inv.TaskID != task.ID || task.SessionID != sessionID {
			return "", errors.New("incorrect identities")
		}
		if inv.OtterState != nil || inv.TotalUsage.TotalTokens != 0 {
			return "", errors.New("execution state reused")
		}
		result := "answer to " + task.Input
		for _, ch := range result {
			chunks <- string(ch)
		}
		inv.AppendMessage(llm.RoleAssistant, result)
		inv.TotalUsage.TotalTokens = 10
		inv.OtterState = &llm.OtterState{ChatSessionID: "private"}
		return result, nil
	})
	for _, input := range []string{"first", "second"} {
		updated, result := executeTurn(t, m, input)
		m = updated
		if result.err != nil || m.state != stateIdle {
			t.Fatalf("turn failed: %+v, state %v", result, m.state)
		}
	}
	if strings.Count(strings.Join(m.chatHistory, "\n"), "answer to first") != 1 {
		t.Fatal("streamed final answer was duplicated")
	}
	stored, err := session.Load(int64(m.record.Session.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Tasks) != 2 || stored.Tasks[0].ID == stored.Tasks[1].ID || len(stored.Invocations) != 2 {
		t.Fatalf("tasks and invocations not distinct: %+v", stored)
	}
	if len(stored.Session.Messages) != 4 {
		t.Fatalf("timeline = %+v", stored.Session.Messages)
	}
	if got := stored.Invocations[1].Messages; len(got) != 5 || got[1].Content != "first" || got[2].Content != "answer to first" {
		t.Fatalf("second turn history = %+v", got)
	}
}

func TestFinalResultFollowsBufferedChunks(t *testing.T) {
	m, chunks := newTestModel(t)
	want := strings.Repeat("x", 500)
	m.executor = testExecutor(func(_ *domain.Task, inv *core.Invocation) (string, error) {
		for range 500 {
			chunks <- "x"
		}
		inv.AppendMessage(llm.RoleAssistant, want)
		return want, nil
	})
	m, result := executeTurn(t, m, "stream")
	if result.err != nil || len(m.chatHistory) != 2 || m.chatHistory[1] != want || m.status != "Ready" {
		t.Fatalf("incorrect ordered stream: result=%+v history=%v status=%s", result, m.chatHistory, m.status)
	}
}

func TestExecutionFailureDoesNotPublishSuccess(t *testing.T) {
	m, chunks := newTestModel(t)
	failure := errors.New("model unavailable")
	m.executor = testExecutor(func(_ *domain.Task, inv *core.Invocation) (string, error) {
		chunks <- "partial"
		inv.AppendMessage(llm.RoleAssistant, "partial")
		return "", failure
	})
	m, result := executeTurn(t, m, "fail")
	if !errors.Is(result.err, failure) || m.state != stateIdle || !strings.Contains(strings.Join(m.chatHistory, "\n"), failure.Error()) {
		t.Fatalf("failure not displayed: %+v", result)
	}
	stored, err := session.Load(int64(m.record.Session.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Session.Messages) != 1 || len(stored.Invocations[0].Messages) != 3 {
		t.Fatal("failed execution lost its trace or published success")
	}
}

func TestClosedStreamStillCompletes(t *testing.T) {
	m, chunks := newTestModel(t)
	close(chunks)
	m.executor = testExecutor(func(_ *domain.Task, inv *core.Invocation) (string, error) {
		inv.AppendMessage(llm.RoleAssistant, "unstreamed answer")
		return "unstreamed answer", nil
	})
	m, result := executeTurn(t, m, "closed stream")
	if result.err != nil || !strings.Contains(strings.Join(m.chatHistory, "\n"), result.result) {
		t.Fatalf("unstreamed final answer missing: %+v", result)
	}
}

func TestResizePreservesConversationAndDraft(t *testing.T) {
	m, _ := newTestModel(t)
	m.chatHistory = []string{"> first", "answer"}
	m.viewport.SetContent(strings.Join(m.chatHistory, "\n"))
	m.input.SetValue("unsent draft")
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 40}, {Width: 2, Height: 2}} {
		updated, _ := m.Update(size)
		m = updated.(model)
		if m.input.Value() != "unsent draft" || !reflect.DeepEqual(m.chatHistory, []string{"> first", "answer"}) || m.viewport.Height < 1 {
			t.Fatal("resize discarded conversation or input")
		}
	}
}

func TestIdleToolStatusDoesNotReopenTurn(t *testing.T) {
	m, _ := newTestModel(t)
	updated, _ := m.Update(toolStatusMsg("late tool result"))
	m = updated.(model)
	if m.state != stateIdle || len(m.chatHistory) != 0 {
		t.Fatal("late tool event reopened a completed turn")
	}
}

func TestWaitForEventStopsOnExit(t *testing.T) {
	m, _ := newTestModel(t)
	close(m.done)
	if msg := m.waitForEvent()(); msg != nil {
		t.Fatalf("exit returned unexpected message: %v", msg)
	}
}
