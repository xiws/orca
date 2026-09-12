package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiws/orca/internal/handler"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/command"
	"github.com/xiws/orca/pkg/event"
	"github.com/xiws/orca/pkg/utils"
)

// scriptedRequester replays a fixed list of model replies and records every
// conversation it is handed, so the loop can be driven without a real model.
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

// newTestRuntime returns a runtime whose command registry is scoped to a
// temporary workspace, so file commands never touch the real project tree.
func newTestRuntime(t *testing.T) (*Runtime, handler.Workspace) {
	t.Helper()
	root := t.TempDir()
	bus := event.NewEventBus()
	t.Cleanup(func() { _ = bus.Close() })

	workspace := handler.Workspace{Root: root, Publisher: bus}
	handle := command.NewCommandHandle()
	if err := handler.Register(handle, workspace); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &Runtime{commands: handle, bus: bus, workspace: workspace}, workspace
}

// script installs a requester that replays replies in order.
func script(runtime *Runtime, replies ...llm.Result) *scriptedRequester {
	requester := &scriptedRequester{replies: replies}
	runtime.newRequester = func(llm.ModelInfo) llm.Requester { return requester }
	return requester
}

// seedFile writes content into the workspace root.
func seedFile(t *testing.T, ws handler.Workspace, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws.Root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newConversation returns a task with the system and user turns a real run
// starts from.
func newConversation(target, userTurn string) *Task {
	task := NewTask(target, "")
	task.SessionInfo.AppendMessage(llm.RoleSystem, "You are a coding agent.")
	task.SessionInfo.AppendMessage(llm.RoleUser, userTurn)
	return task
}

// rolesOf lists the role of every message, which is what the protocol is
// actually about.
func rolesOf(messages []llm.ChatMessage) []string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	return roles
}

// TestExecuteKeepsToolProtocolOrder drives one tool round and checks the
// conversation reads back the way the OpenAI protocol demands: the assistant
// turn carrying the tool call, then the tool message answering it by id, then
// the model's answer — and that the second request is given all of it.
func TestExecuteKeepsToolProtocolOrder(t *testing.T) {
	runtime, ws := newTestRuntime(t)
	seedFile(t, ws, "a.md", "hello\n")

	requester := script(runtime,
		llm.Result{FinishReason: "tool_calls", ToolCalls: []llm.ToolCall{
			{ID: "call-1", Name: handler.CommandRead, Arguments: `{"filename":"a.md"}`},
		}},
		llm.Result{FinishReason: "stop", Content: "done"},
	)

	task := newConversation("read a.md", "read a.md")
	err, result := runtime.RunTask(task)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if result != "done" {
		t.Fatalf("RunTask() result = %q, want %q", result, "done")
	}

	// The timeline: system, user, assistant(call), tool(answer), assistant(done).
	messages := task.SessionInfo.Messages
	want := []string{llm.RoleSystem, llm.RoleUser, llm.RoleAssistant, llm.RoleTool, llm.RoleAssistant}
	if got := rolesOf(messages); !equalStrings(got, want) {
		t.Fatalf("session roles = %v, want %v", got, want)
	}

	assistant := messages[2]
	if !assistant.IsToolCallTurn() {
		t.Fatalf("message 2 = %+v, want an assistant turn carrying its tool calls", assistant)
	}
	if got := assistant.ToolCalls[0].ID; got != "call-1" {
		t.Errorf("assistant tool call id = %q, want %q", got, "call-1")
	}

	answer := messages[3]
	if answer.Role != llm.RoleTool || answer.ToolCallID != "call-1" {
		t.Fatalf("tool message = %+v, want role tool answering call-1", answer)
	}
	var payload handler.CommandResult
	if err := json.Unmarshal([]byte(answer.Content), &payload); err != nil {
		t.Fatalf("tool content %q is not a CommandResult: %v", answer.Content, err)
	}
	if !payload.OK || !strings.Contains(payload.Content, "hello") {
		t.Errorf("tool payload = %+v, want the read content", payload)
	}

	// The second request has to see the assistant turn and its answer; without
	// them the model would not know what it had already done.
	if len(requester.seen) != 2 {
		t.Fatalf("model was called %d times, want 2", len(requester.seen))
	}
	second := requester.seen[1]
	wantSecond := []string{llm.RoleSystem, llm.RoleUser, llm.RoleAssistant, llm.RoleTool}
	if got := rolesOf(second); !equalStrings(got, wantSecond) {
		t.Fatalf("second request roles = %v, want %v", got, wantSecond)
	}
	if !second[2].IsToolCallTurn() || second[3].ToolCallID != "call-1" {
		t.Errorf("second request lost the call/answer pair: %+v", second[2:])
	}
}

// TestExecuteAnswersFailedToolCall checks that a tool which cannot run is still
// answered. A read of a path that does not exist is a business failure the
// model must be able to read and recover from, not a fatal error.
func TestExecuteAnswersFailedToolCall(t *testing.T) {
	runtime, _ := newTestRuntime(t)

	requester := script(runtime,
		llm.Result{FinishReason: "tool_calls", ToolCalls: []llm.ToolCall{
			{ID: "call-7", Name: handler.CommandRead, Arguments: `{"filename":"missing.md"}`},
		}},
		llm.Result{FinishReason: "stop", Content: "recovered"},
	)

	task := newConversation("read missing.md", "read missing.md")
	err, result := runtime.RunTask(task)
	if err != nil {
		t.Fatalf("a path the model guessed wrong must not abort the task: %v", err)
	}
	if result != "recovered" {
		t.Fatalf("RunTask() result = %q, want %q", result, "recovered")
	}
	if len(requester.seen) != 2 {
		t.Fatalf("model was called %d times, want the loop to continue", len(requester.seen))
	}

	answer := task.SessionInfo.Messages[3]
	if answer.Role != llm.RoleTool || answer.ToolCallID != "call-7" {
		t.Fatalf("failed call was not answered in order: %+v", answer)
	}
	var payload handler.CommandResult
	if err := json.Unmarshal([]byte(answer.Content), &payload); err != nil {
		t.Fatalf("tool content %q is not a CommandResult: %v", answer.Content, err)
	}
	if payload.OK {
		t.Errorf("tool payload = %+v, want ok:false", payload)
	}
	if !strings.Contains(payload.Err, "missing.md") {
		t.Errorf("tool payload err = %q, want the path it could not open", payload.Err)
	}
}

// TestExecuteCommandAnswersUnknownTool covers a call no handler is registered
// for: it cannot run, but it must still be answered so the assistant turn is
// never left without its tool message.
func TestExecuteCommandAnswersUnknownTool(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	task := newConversation("teleport", "teleport")

	err := runtime.executeCommand(task, []llm.ToolCall{
		{ID: "call-9", Name: "teleport", Arguments: `{}`},
	})
	if err != nil {
		t.Fatalf("executeCommand() error = %v, want the failure recorded as data", err)
	}

	messages := task.SessionInfo.Messages
	if len(messages) != 3 {
		t.Fatalf("recorded %d messages, want the two opening turns plus one answer", len(messages))
	}
	answer := messages[2]
	if answer.Role != llm.RoleTool || answer.ToolCallID != "call-9" {
		t.Fatalf("answer = %+v, want a tool message for call-9", answer)
	}
	var payload handler.CommandResult
	if err := json.Unmarshal([]byte(answer.Content), &payload); err != nil {
		t.Fatalf("tool content %q is not a CommandResult: %v", answer.Content, err)
	}
	if payload.OK || !strings.Contains(payload.Err, ErrUnknownCommand.Error()) {
		t.Errorf("payload = %+v, want an unknown command failure", payload)
	}
}

// TestExecuteStopsAtTurnLimit makes sure a model that keeps asking for tools
// cannot spin forever.
func TestExecuteStopsAtTurnLimit(t *testing.T) {
	runtime, ws := newTestRuntime(t)
	seedFile(t, ws, "a.md", "hello\n")

	replies := make([]llm.Result, 0, MaxTurns+1)
	for i := 0; i < MaxTurns+1; i++ {
		replies = append(replies, llm.Result{FinishReason: "tool_calls", ToolCalls: []llm.ToolCall{
			{ID: "call-1", Name: handler.CommandRead, Arguments: `{"filename":"a.md"}`},
		}})
	}
	script(runtime, replies...)

	task := newConversation("loop forever", "loop forever")
	err, _ := runtime.RunTask(task)
	if !errors.Is(err, ErrMaxTurns) {
		t.Fatalf("RunTask() error = %v, want %v", err, ErrMaxTurns)
	}
}

// TestNewRuntimeRegistersToolCommands asserts the constructor wires a registry
// that knows every built-in command, scoping file access to the project root.
func TestNewRuntimeRegistersToolCommands(t *testing.T) {
	runtime := NewRuntime()
	if runtime.commands == nil {
		t.Fatal("NewRuntime() built a runtime with no command registry")
	}
	for _, name := range handler.Commands() {
		// An empty payload keeps each handler from touching the file system: the
		// command is dispatched and fails as a business result rather than with
		// ErrCommandNotFound, which is exactly what proves it is registered.
		opt, err := OptionFromCall(1, name, `{}`)
		if err != nil {
			t.Fatalf("OptionFromCall(%q) error = %v", name, err)
		}
		if execErr, _ := runtime.commands.Execute(opt); errors.Is(execErr, command.ErrCommandNotFound) {
			t.Errorf("command %q is not registered", name)
		}
	}
}

// TestRunTaskSurfacesModelFailure exercises RunTask's delegation to the model
// turn. An unresolved provider yields an empty ModelInfo, so the request is
// built against an invalid endpoint and fails at the HTTP client without any
// network round trip; RunTask must surface that error and return no message.
func TestRunTaskSurfacesModelFailure(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	task := newConversation("no provider configured", "no provider configured")
	// An explicit zero provider keeps the request off the model configured in
	// .orca/setting.json; without it this test would really call ollama.
	task.SessionInfo.Provider = llm.ModelInfo{}

	err, msg := runtime.RunTask(task)
	if err == nil {
		t.Fatal("RunTask() error = nil, want the model request to fail")
	}
	if msg != "" {
		t.Errorf("RunTask() msg = %q, want empty on failure", msg)
	}
}

// TestRunTaskLiveAgent and TestSubRunTaskLiveAgent talk to the model configured
// in .orca/setting.json. They are skipped in short mode because they need a
// reachable endpoint.
func TestRunTaskLiveAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live model request in short mode")
	}
	var prompt = "编辑README.md文件，在后面追加目前tool_calling.go中做了些什么"
	var task = NewTask(prompt, "")
	data := PromptContext{
		ProjectPath:   utils.GetEnv("PROJECT_PATH"),
		ContextLength: 8192,
	}

	task.SessionInfo.AppendMessage(llm.RoleSystem, utils.GetSystemPrompt(data))
	task.SessionInfo.AppendMessage(llm.RoleUser, prompt)
	task.SessionInfo.SetProvider("ollama", "ornith-1.5:9b")
	runtime := NewRuntime()
	err, _ := runtime.RunTask(task)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
}

func TestSubRunTaskLiveAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live model request in short mode")
	}
	var prompt = "./internal/handler/read.go 目录下的read命令，如果读取到的文件太长，则后续添加到prompt的时候会超出上下文，这时候直接不返回读取的信息改为返回:fail command: read filename 超长，文件多少行多少字"
	var task = NewTask(prompt, "")
	data := PromptContext{
		ProjectPath:   utils.GetEnv("PROJECT_PATH"),
		ContextLength: 8192,
	}

	task.SessionInfo.AppendMessage(llm.RoleSystem, utils.GetSystemPrompt(data))
	task.SessionInfo.AppendMessage(llm.RoleUser, prompt)
	task.SessionInfo.SetProvider("ollama", "ornith-1.5:9b")
	runtime := NewRuntime()
	err, msg := runtime.RunTask(task)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	t.Logf("%s", msg)
}

// equalStrings compares two role lists.
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
