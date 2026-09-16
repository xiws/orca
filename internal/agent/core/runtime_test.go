package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiws/orca/internal/agent/parse"
	"github.com/xiws/orca/internal/handler"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/command"
	"github.com/xiws/orca/pkg/event"
	"github.com/xiws/orca/pkg/utils"
)

// scriptedRequester 按顺序回放固定的模型回复列表，并记录每次
// 对话，以便在没有真实模型的情况下驱动循环。
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

// newTestRuntime 返回命令注册表限定在临时工作区的运行时，
// 因此文件命令不会触及真实项目树。
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

// script 安装一个按顺序回放回复的 requester。
func script(runtime *Runtime, replies ...llm.Result) *scriptedRequester {
	requester := &scriptedRequester{replies: replies}
	runtime.newRequester = func(llm.ModelInfo) llm.Requester { return requester }
	return requester
}

// seedFile 将 content 写入工作区根目录。
func seedFile(t *testing.T, ws handler.Workspace, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ws.Root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newConversation 返回包含真实运行起始的系统和用户轮次的任务。
func newConversation(target, userTurn string) *Task {
	task := NewTask(target, "")
	task.SessionInfo.AppendMessage(llm.RoleSystem, "You are a coding agent.")
	task.SessionInfo.AppendMessage(llm.RoleUser, userTurn)
	return task
}

// rolesOf 列出每条消息的角色，这是协议实际关注的内容。
func rolesOf(messages []llm.ChatMessage) []string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	return roles
}

// TestExecuteKeepsToolProtocolOrder 驱动一轮工具调用并检查
// 对话是否按 OpenAI 协议要求的方式读取：携带工具调用的助手轮次，
// 然后通过 id 回复的工具消息，然后是模型的回答——
// 并且第二次请求能看到所有这些。
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

	// 时间线：system、user、assistant(call)、tool(answer)、assistant(done)。
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

	// 第二次请求必须看到助手轮次及其回复；否则
	// 模型将不知道它已经做了什么。
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

// TestExecuteAnswersFailedToolCall 检查无法运行的工具仍然会被回复。
// 读取不存在的路径是业务失败，模型必须能读取并恢复，而不是致命错误。
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

// TestExecuteCommandAnswersUnknownTool 覆盖没有注册处理器的调用：
// 它无法运行，但必须被回复，以确保助手轮次不会缺少工具消息。
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
	if payload.OK || !strings.Contains(payload.Err, parse.ErrUnknownCommand.Error()) {
		t.Errorf("payload = %+v, want an unknown command failure", payload)
	}
}

// TestExecuteStopsAtTurnLimit 确保不断请求工具的模型不会无限循环。
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

// TestExecuteFallsBackToTextCommands 驱动没有原生函数调用的 Provider：
// 命令嵌入在回复文本中，循环仍然必须运行它们。
func TestExecuteFallsBackToTextCommands(t *testing.T) {
	runtime, ws := newTestRuntime(t)
	seedFile(t, ws, "a.md", "hello\n")

	requester := script(runtime,
		llm.Result{FinishReason: "stop", Content: "Let me read it.\n" +
			`{"command":"read","filename":"a.md"}`},
		llm.Result{FinishReason: "stop", Content: "done"},
	)

	task := newConversation("read a.md", "read a.md")
	// 不支持原生工具支持的 Provider 正是开启回退的条件；
	// 零 ModelInfo 恰好表达了这一点。
	task.SessionInfo.Provider = llm.ModelInfo{Provider: "otter", API: "otter", ModelID: "deepseek"}
	err, result := runtime.RunTask(task)
	if err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	if result != "done" {
		t.Fatalf("RunTask() result = %q, want %q", result, "done")
	}
	if len(requester.seen) != 2 {
		t.Fatalf("model was called %d times, want 2", len(requester.seen))
	}

	timeline := task.SessionInfo.Messages
	want := []string{llm.RoleSystem, llm.RoleUser, llm.RoleAssistant, llm.RoleTool, llm.RoleAssistant}
	if got := rolesOf(timeline); !equalStrings(got, want) {
		t.Fatalf("session roles = %v, want %v", got, want)
	}

	assistant := timeline[2]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Name != handler.CommandRead {
		t.Fatalf("assistant turn = %+v, want the text command lifted into a tool call", assistant)
	}
	answer := timeline[3]
	if answer.Role != llm.RoleTool || answer.ToolCallID != assistant.ToolCalls[0].ID {
		t.Fatalf("tool message = %+v, want one answering the parsed call", answer)
	}
	var payload handler.CommandResult
	if err := json.Unmarshal([]byte(answer.Content), &payload); err != nil {
		t.Fatalf("tool content %q is not a CommandResult: %v", answer.Content, err)
	}
	if !payload.OK || !strings.Contains(payload.Content, "hello") {
		t.Errorf("tool payload = %+v, want the read content", payload)
	}
}

// TestNewRuntimeRegistersToolCommands 断言构造函数连接了
// 知道每个内置命令的注册表，将文件访问限定在项目根目录。
func TestNewRuntimeRegistersToolCommands(t *testing.T) {
	runtime := NewRuntime()
	if runtime.commands == nil {
		t.Fatal("NewRuntime() built a runtime with no command registry")
	}
	for _, name := range handler.Commands() {
		// 空负载让每个处理器不触及文件系统：命令被分发并
		// 以业务结果失败而非 ErrCommandNotFound，这正好证明它已注册。
		opt, err := parse.OptionFromCall(1, name, `{}`)
		if err != nil {
			t.Fatalf("OptionFromCall(%q) error = %v", name, err)
		}
		if execErr, _ := runtime.commands.Execute(opt); errors.Is(execErr, command.ErrCommandNotFound) {
			t.Errorf("command %q is not registered", name)
		}
	}
}

// TestRunTaskSurfacesModelFailure 测试 RunTask 对模型轮次的委托。
// 未解析的 Provider 产生空的 ModelInfo，因此请求针对无效端点，
// 在 HTTP 客户端处失败而无网络往返；RunTask 必须暴露该错误且不返回消息。
func TestRunTaskSurfacesModelFailure(t *testing.T) {
	runtime, _ := newTestRuntime(t)
	task := newConversation("no provider configured", "no provider configured")
	// 显式的零 Provider 避免使用 .orca/setting.json 中配置的模型；
	// 否则这个测试会真的调用 ollama。
	task.SessionInfo.Provider = llm.ModelInfo{}

	err, msg := runtime.RunTask(task)
	if err == nil {
		t.Fatal("RunTask() error = nil, want the model request to fail")
	}
	if msg != "" {
		t.Errorf("RunTask() msg = %q, want empty on failure", msg)
	}
}

// TestRunTaskLiveAgent 和 TestSubRunTaskLiveAgent 与 .orca/setting.json
// 中配置的模型对话。它们在 short 模式下跳过，因为需要可达的端点。
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

// equalStrings 比较两个角色列表。
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
