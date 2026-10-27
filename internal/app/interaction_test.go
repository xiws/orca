package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/internal/session"
)

type executorFunc func(*domain.Task, *core.Invocation) (string, error)

func (f executorFunc) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	return f(task, inv)
}

func newRecord(t *testing.T) *session.Record {
	t.Helper()
	root := t.TempDir()
	t.Setenv("WORKSPACE", root)
	t.Setenv("HOME", t.TempDir())
	return &session.Record{Session: domain.NewSession(root)}
}

func TestConsecutiveTurnsShareOnlyUserHistory(t *testing.T) {
	record := newRecord(t)
	provider := llm.ModelInfo{Provider: "fake", ModelID: "first", SupportsTools: true, APIKey: "not-persisted"}
	first := Prepare(record, Request{Input: "first goal", Prompt: "attachment\nfirst goal", SystemPrompt: "first system", Provider: provider})
	if first.Task.Input != "first goal" || first.Task.SessionID != record.Session.ID || first.Invocation.TaskID != first.Task.ID {
		t.Fatalf("first turn = %+v", first)
	}
	execute := executorFunc(func(task *domain.Task, inv *core.Invocation) (string, error) {
		inv.AppendAssistant("internal reasoning", []llm.ToolCall{{ID: "read-call", Name: "read", Arguments: `{}`}})
		inv.AppendToolResult("read-call", "private tool output")
		inv.AppendAssistant("executor answer", nil)
		inv.Provider.AllowedTools = []string{"read"}
		inv.AddUsage(llm.Usage{TotalTokens: 7})
		inv.SetOtterState(llm.OtterState{ChatSessionID: "remote-first", Delivered: 5})
		child := inv.NewChild()
		child.AppendAssistant("private verifier answer", nil)
		child.AddUsage(llm.Usage{TotalTokens: 3})
		return "published final answer", nil
	})
	if _, err := Execute(record, first, execute); err != nil {
		t.Fatal(err)
	}
	if len(record.Session.Messages) != 2 || record.Session.Messages[1].Content != "published final answer" {
		t.Fatalf("user timeline includes internal results: %+v", record.Session.Messages)
	}
	loaded, err := session.Load(int64(record.Session.ID))
	if err != nil {
		t.Fatal(err)
	}
	provider.ModelID = "second"
	second := Prepare(loaded, Request{Input: "second goal", SystemPrompt: "second system", Provider: provider})
	if second.Task.ID == first.Task.ID || int64(second.Task.ID) == int64(record.Session.ID) || second.Task.SessionID != first.Task.SessionID || second.Invocation.ID == first.Invocation.ID {
		t.Fatal("task/session/invocation identities are conflated")
	}
	if second.Invocation.OtterState != nil || second.Invocation.TotalUsage.TotalTokens != 0 || second.Invocation.Provider.AllowedTools != nil {
		t.Fatal("previous execution state leaked into a new task")
	}
	var contents []string
	for _, message := range second.Invocation.Messages {
		contents = append(contents, message.Content)
	}
	want := []string{"second system", "attachment\nfirst goal", "published final answer", "second goal"}
	if !reflect.DeepEqual(contents, want) {
		t.Fatalf("context = %v, want %v", contents, want)
	}
	if second.Invocation.Provider.ModelID != "second" || second.Invocation.Provider.APIKey != "not-persisted" {
		t.Fatal("new task did not use explicitly supplied model settings")
	}
	second.Invocation.Messages[1].Content = "changed context"
	if loaded.Session.Messages[0].Content != "attachment\nfirst goal" {
		t.Fatal("model context mutated the session timeline")
	}
}

func TestExecutionFailureRetainsTraceWithoutPublishingSuccess(t *testing.T) {
	record := newRecord(t)
	turn := Prepare(record, Request{Input: "goal", SystemPrompt: "system", Provider: llm.ModelInfo{SupportsTools: true}})
	want := errors.New("verification failed")
	result, err := Execute(record, turn, executorFunc(func(task *domain.Task, inv *core.Invocation) (string, error) {
		inv.AppendAssistant("partial result", nil)
		return "partial result", want
	}))
	if !errors.Is(err, want) || result != "partial result" {
		t.Fatalf("Execute() = %q, %v", result, err)
	}
	loaded, err := session.Load(int64(record.Session.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Session.Messages) != 1 || len(loaded.Invocations) != 1 {
		t.Fatalf("failed turn was lost or published: %+v", loaded)
	}
	messages := loaded.Invocations[0].Messages
	if messages[len(messages)-1].Content != "partial result" {
		t.Fatal("failure lost model trace")
	}
}

func TestPrepareUsesFreshSystemAndProtocolInstructions(t *testing.T) {
	record := newRecord(t)
	turn := Prepare(record, Request{Input: "goal", SystemPrompt: "custom system", Provider: llm.ModelInfo{SupportsTools: false}})
	if len(turn.Invocation.Messages) != 3 || turn.Invocation.Messages[0].Content != "custom system" || turn.Invocation.Messages[1].Role != llm.RoleSystem {
		t.Fatalf("protocol instructions = %+v", turn.Invocation.Messages)
	}
	if len(record.Session.Messages) != 1 || record.Session.Messages[0].Role != domain.UserMessage {
		t.Fatal("model instructions contaminated user history")
	}
}

func TestExecuteRejectsMismatchedIdentities(t *testing.T) {
	record := newRecord(t)
	turn := Prepare(record, Request{Input: "goal", SystemPrompt: "system"})
	turn.Invocation.TaskID++
	_, err := Execute(record, turn, executorFunc(func(*domain.Task, *core.Invocation) (string, error) {
		t.Fatal("executor must not run for mismatched identities")
		return "", nil
	}))
	if err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Fatalf("error = %v", err)
	}
}
