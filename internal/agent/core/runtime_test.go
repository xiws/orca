package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xiws/orca/internal/agent/parse"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/handler"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/command"
	"github.com/xiws/orca/pkg/event"
)

type scriptedRequester struct {
	replies []llm.Result
	seen    [][]llm.ChatMessage
}

func (s *scriptedRequester) Request(prompts []llm.ChatMessage, _ chan<- string) llm.Result {
	s.seen = append(s.seen, append([]llm.ChatMessage(nil), prompts...))
	if len(s.replies) == 0 {
		return llm.Result{}
	}
	reply := s.replies[0]
	s.replies = s.replies[1:]
	return reply
}

func newTestRuntime(t *testing.T) (*Runtime, handler.Workspace) {
	t.Helper()
	workspace := handler.Workspace{Root: t.TempDir(), Publisher: event.NewEventBus()}
	handle := command.NewCommandHandle()
	if err := handler.Register(handle, workspace); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{commands: handle, bus: workspace.Publisher, workspace: workspace}
	t.Cleanup(runtime.Close)
	return runtime, workspace
}

func script(runtime *Runtime, replies ...llm.Result) *scriptedRequester {
	requester := &scriptedRequester{replies: replies}
	runtime.newRequester = func(llm.ModelInfo) llm.Requester { return requester }
	return requester
}

func seedFile(t *testing.T, ws handler.Workspace, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws.Root, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func newConversation(root, prompt string) *Invocation {
	inv := NewInvocation(domain.TaskID(42), root, llm.ModelInfo{SupportsTools: true})
	inv.AppendMessage(llm.RoleSystem, "You are a coding agent.")
	inv.AppendMessage(llm.RoleUser, prompt)
	return inv
}

func rolesOf(messages []llm.ChatMessage) []string {
	roles := make([]string, len(messages))
	for i, message := range messages {
		roles[i] = message.Role
	}
	return roles
}

func TestExecuteKeepsToolProtocolOrder(t *testing.T) {
	runtime, ws := newTestRuntime(t)
	seedFile(t, ws, "a.md", "hello\n")
	requester := script(runtime,
		llm.Result{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "read", Arguments: `{"filename":"a.md"}`}}},
		llm.Result{Content: "done"},
	)
	inv := newConversation(ws.Root, "read a.md")
	result, err := runtime.Run(inv)
	if err != nil || result != "done" {
		t.Fatalf("Run() = %q, %v", result, err)
	}
	want := []string{llm.RoleSystem, llm.RoleUser, llm.RoleAssistant, llm.RoleTool, llm.RoleAssistant}
	if !reflect.DeepEqual(rolesOf(inv.Messages), want) {
		t.Fatalf("roles = %v", rolesOf(inv.Messages))
	}
	if inv.Messages[2].ToolCalls[0].ID != "call-1" || inv.Messages[3].ToolCallID != "call-1" {
		t.Fatal("tool call/result identity was lost")
	}
	var payload handler.CommandResult
	if err := json.Unmarshal([]byte(inv.Messages[3].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || !strings.Contains(payload.Content, "hello") {
		t.Fatalf("payload = %+v", payload)
	}
	if len(requester.seen) != 2 || !reflect.DeepEqual(rolesOf(requester.seen[1]), want[:4]) {
		t.Fatalf("requests = %+v", requester.seen)
	}
}

func TestExecuteAnswersFailedToolCall(t *testing.T) {
	runtime, ws := newTestRuntime(t)
	script(runtime,
		llm.Result{ToolCalls: []llm.ToolCall{{ID: "missing", Name: "read", Arguments: `{"filename":"missing.md"}`}}},
		llm.Result{Content: "recovered"},
	)
	inv := newConversation(ws.Root, "read missing.md")
	result, err := runtime.Run(inv)
	if err != nil || result != "recovered" {
		t.Fatalf("Run() = %q, %v", result, err)
	}
	var payload handler.CommandResult
	if err := json.Unmarshal([]byte(inv.Messages[3].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OK || !strings.Contains(payload.Err, "missing.md") || inv.Messages[3].ToolCallID != "missing" {
		t.Fatalf("failed tool result = %+v", payload)
	}
}

func TestExecuteCommandAnswersUnknownTool(t *testing.T) {
	runtime, ws := newTestRuntime(t)
	inv := newConversation(ws.Root, "teleport")
	if err := runtime.executeCommand(inv, []llm.ToolCall{{ID: "unknown", Name: "teleport", Arguments: `{}`}}); err != nil {
		t.Fatal(err)
	}
	if len(inv.Messages) != 3 || inv.Messages[2].ToolCallID != "unknown" || !strings.Contains(inv.Messages[2].Content, parse.ErrUnknownCommand.Error()) {
		t.Fatalf("messages = %+v", inv.Messages)
	}
}

func TestExecuteStopsAtTurnLimit(t *testing.T) {
	runtime, ws := newTestRuntime(t)
	seedFile(t, ws, "a.md", "hello")
	var replies []llm.Result
	for range MaxTurns + 1 {
		replies = append(replies, llm.Result{ToolCalls: []llm.ToolCall{{ID: "loop", Name: "read", Arguments: `{"filename":"a.md"}`}}})
	}
	script(runtime, replies...)
	_, err := runtime.Run(newConversation(ws.Root, "keep reading"))
	if !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("error = %v, want %v", err, ErrMaxTurns)
	}
}

func TestExecuteFallsBackToTextCommands(t *testing.T) {
	runtime, ws := newTestRuntime(t)
	seedFile(t, ws, "a.md", "hello")
	script(runtime, llm.Result{Content: `{"command":"read","filename":"a.md"}`}, llm.Result{Content: "done"})
	inv := newConversation(ws.Root, "read a.md")
	inv.Provider.SupportsTools = false
	result, err := runtime.Run(inv)
	if err != nil || result != "done" {
		t.Fatalf("Run() = %q, %v", result, err)
	}
	if len(inv.Messages) != 5 || len(inv.Messages[2].ToolCalls) != 1 || inv.Messages[3].ToolCallID != inv.Messages[2].ToolCalls[0].ID {
		t.Fatalf("messages = %+v", inv.Messages)
	}
}

func TestRuntimeHonorsInvocationTools(t *testing.T) {
	runtime, ws := newTestRuntime(t)
	inv := newConversation(ws.Root, "write a file")
	inv.Provider.AllowedTools = []string{}
	script(runtime,
		llm.Result{ToolCalls: []llm.ToolCall{{ID: "denied", Name: "write", Arguments: `{"filename":"a.txt","content":"wrong"}`}}},
		llm.Result{Content: "denied"},
	)
	if _, err := runtime.Run(inv); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inv.Messages[3].Content, "not allowed") {
		t.Fatalf("result = %s", inv.Messages[3].Content)
	}
	if _, err := os.Stat(filepath.Join(ws.Root, "a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied write touched filesystem: %v", err)
	}
}

func TestNewRuntimeRegistersToolCommands(t *testing.T) {
	runtime := NewRuntime(WithWorkspace(t.TempDir()), WithSkipDefaultHandlers())
	t.Cleanup(runtime.Close)
	for _, name := range handler.Commands() {
		opt, err := parse.OptionFromCall(1, name, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if execErr, _ := runtime.commands.Execute(opt); errors.Is(execErr, command.ErrCommandNotFound) {
			t.Errorf("command %q is not registered", name)
		}
	}
}

func TestRunSurfacesModelFailure(t *testing.T) {
	runtime, ws := newTestRuntime(t)
	want := errors.New("model unavailable")
	script(runtime, llm.Result{Error: want})
	inv := newConversation(ws.Root, "request")
	result, err := runtime.Run(inv)
	if !errors.Is(err, want) || result != "" || len(inv.Messages) != 2 {
		t.Fatalf("Run() = %q, %v; messages = %v", result, err, inv.Messages)
	}
}

func TestSubInvocationsKeepStateSeparate(t *testing.T) {
	runtime, ws := newTestRuntime(t)
	parent := newConversation(ws.Root, "split work")
	parent.Provider.Provider = "custom"
	parent.Provider.AllowedTools = []string{"create_task", "read"}
	parentReplies := &scriptedRequester{replies: []llm.Result{
		{ToolCalls: []llm.ToolCall{{ID: "delegate", Name: "create_task", Arguments: `{"task_target":[{"title":"child","description":"inspect"}]}`}}, Usage: llm.Usage{TotalTokens: 2}},
		{Content: "parent done"},
	}}
	childReplies := &scriptedRequester{replies: []llm.Result{{Content: "child done", Usage: llm.Usage{TotalTokens: 3}}}}
	calls := 0
	runtime.newRequester = func(info llm.ModelInfo) llm.Requester {
		calls++
		if info.Provider != "custom" || !reflect.DeepEqual(info.AllowedTools, parent.Provider.AllowedTools) {
			t.Fatalf("provider not inherited: %+v", info)
		}
		if calls == 1 {
			return parentReplies
		}
		return childReplies
	}
	result, err := runtime.Run(parent)
	if err != nil || result != "parent done" || len(parent.Children) != 1 {
		t.Fatalf("Run() = %q, %v; children = %d", result, err, len(parent.Children))
	}
	child := parent.Children[0]
	if child.ID == parent.ID || child.TaskID != parent.TaskID || child.ProjectPath != ws.Root || child.TotalUsage.TotalTokens != 3 || parent.TotalUsage.TotalTokens != 2 {
		t.Fatalf("parent/child state not isolated: %+v / %+v", parent, child)
	}
}
