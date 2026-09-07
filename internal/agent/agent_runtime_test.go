package agent

import (
	"encoding/json"
	"errors"
	"orca/pkg/utils"
	"strings"
	"testing"

	"orca/internal/handler"
	"orca/internal/llm"
	"orca/pkg/command"
)

// newTestRuntime returns a runtime whose command registry is scoped to a
// temporary workspace, so file commands never touch the real project tree. The
// session lives inside the returned task.
func newTestRuntime(t *testing.T) (*Runtime, handler.Workspace) {
	t.Helper()
	root := t.TempDir()
	handle := command.NewCommandHandle()
	if err := handler.Register(handle, handler.Workspace{Root: root}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return &Runtime{commands: handle}, handler.Workspace{Root: root}
}

// TestExecuteCommandAppendsResults checks that every tool call is executed and
// its handler.CommandResult is appended to the session as a tool message, in the
// order the model produced them.
func TestExecuteCommandAppendsResults(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	task := NewTask("write then read a.md", "")

	tools := []llm.ToolCall{
		{ID: "call-1", Name: handler.CommandWrite, Arguments: `{"filename":"a.md","content":"hello world"}`},
		{ID: "call-2", Name: handler.CommandRead, Arguments: `{"filename":"a.md"}`},
	}
	// SessionInfo is a pointer, so the appends are visible on the caller's task
	// even though executeCommand receives the Task by value.
	if err := runtime.executeCommand(task, tools); err != nil {
		t.Fatalf("executeCommand() error = %v", err)
	}

	messages := task.SessionInfo.Messages
	if len(messages) != len(tools) {
		t.Fatalf("recorded %d messages, want %d: %+v", len(messages), len(tools), messages)
	}
	wantCommands := []string{handler.CommandWrite, handler.CommandRead}
	for i, msg := range messages {
		if msg.Role != llm.RoleTool {
			t.Errorf("message %d role = %q, want %q", i, msg.Role, llm.RoleTool)
		}
		var result handler.CommandResult
		if err := json.Unmarshal([]byte(msg.Content), &result); err != nil {
			t.Fatalf("message %d content %q is not a CommandResult: %v", i, msg.Content, err)
		}
		if result.Command != wantCommands[i] {
			t.Errorf("message %d command = %q, want %q", i, result.Command, wantCommands[i])
		}
		if !result.OK {
			t.Errorf("message %d = %+v, want success", i, result)
		}
		if result.Id == 0 {
			t.Errorf("message %d has a zero id, want a generated one", i)
		}
	}
	// The read runs after the write and therefore sees the written content.
	if !strings.Contains(messages[1].Content, "hello world") {
		t.Errorf("read result = %q, want it to carry the written content", messages[1].Content)
	}
}

// TestExecuteCommandReportsUnknownTool confirms an unparsable call fails fast and
// leaves nothing appended to the session.
func TestExecuteCommandReportsUnknownTool(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	task := NewTask("teleport", "")

	err := runtime.executeCommand(task, []llm.ToolCall{
		{ID: "call-1", Name: "teleport", Arguments: `{}`},
	})
	if !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("executeCommand() error = %v, want %v", err, ErrUnknownCommand)
	}
	if len(task.SessionInfo.Messages) != 0 {
		t.Errorf("recorded %d messages, want none after a failed call", len(task.SessionInfo.Messages))
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
	task := NewTask("no provider configured", "")

	err, msg := runtime.RunTask(task)
	if err == nil {
		t.Fatal("RunTask() error = nil, want the model request to fail")
	}
	if msg != "" {
		t.Errorf("RunTask() msg = %q, want empty on failure", msg)
	}
}

func TestRunTask(t *testing.T) {
	var prompt = "原样输出文件内容:go.mod"
	var task = NewTask(prompt, "")
	var project_path = utils.GetEnv("PROJECT_PATH")
	data := PromptContext{
		ProjectPath:   project_path,
		ContextLength: 8192,
	}

	systemPrompt := utils.GetSystemPrompt(data)
	task.SessionInfo.AppendMessage(llm.RoleSystem, systemPrompt)
	task.SessionInfo.AppendMessage(llm.RoleUser, prompt)
	task.SessionInfo.SetProvider("ollama", "ornith-1.5:9b")
	runtime := NewRuntime()
	err, _ := runtime.RunTask(task)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
}

func TestSubRunTask(t *testing.T) {
	var prompt = "./internal/handler/read.go 目录下的read命令，如果读取到的文件太长，则后续添加到prompt的时候会超出上下文，这时候直接不返回读取的信息改为返回:fail command: read filename 超长，文件多少行多少字"
	var task = NewTask(prompt, "")
	var projectPath = utils.GetEnv("PROJECT_PATH")
	data := PromptContext{
		ProjectPath:   projectPath,
		ContextLength: 8192,
	}

	systemPrompt := utils.GetSystemPrompt(data)
	task.SessionInfo.AppendMessage(llm.RoleSystem, systemPrompt)
	task.SessionInfo.AppendMessage(llm.RoleUser, prompt)
	task.SessionInfo.SetProvider("ollama", "ornith-1.5:9b")
	runtime := NewRuntime()
	err, msg := runtime.RunTask(task)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	t.Logf("%s", msg)
}
