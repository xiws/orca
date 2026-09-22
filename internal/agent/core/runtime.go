// Package core 驱动对话流程：向模型请求下一步操作，
// 执行模型要求的命令并将结果返回给它。
package core

import (
	"errors"
	"fmt"
	"strings"

	event2 "github.com/xiws/orca/internal/event"
	"github.com/xiws/orca/internal/tool"

	"github.com/xiws/orca/internal/agent/parse"
	"github.com/xiws/orca/internal/handler"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/command"
	"github.com/xiws/orca/pkg/event"
	"github.com/xiws/orca/pkg/utils"
)

// MaxTurns 限制单个任务可执行的模型轮次。如果模型不断请求工具，
// 否则会无限循环，永远无法返回答案。
const MaxTurns = 64

// ErrMaxTurns 在任务达到 MaxTurns 而模型仍在调用工具时返回。
var ErrMaxTurns = errors.New("agent: reached the turn limit while the model kept calling tools")

// requesterFactory 构建与模型对话的客户端。测试会安装自己的
// 实现来驱动循环，使用脚本化的模型响应。
type requesterFactory func(llm.ModelInfo) llm.Requester

// RuntimeOption 配置 Runtime 的构造行为。
type RuntimeOption func(*Runtime)

// WithSkipDefaultHandlers 跳过注册默认的 CLI 事件处理器（打印到 stdout），
// 由调用方通过 Subscribe 自行注册处理器（例如 TUI）。
func WithSkipDefaultHandlers() RuntimeOption {
	return func(r *Runtime) { r.skipDefaultHandlers = true }
}

// WithMsgChannel 设置流式内容输出的外部通道。
// TUI 使用此选项接收流式内容块，而非由内部 goroutine 打印到 stdout。
func WithMsgChannel(ch chan<- string) RuntimeOption {
	return func(r *Runtime) { r.msg = ch }
}

// Runtime 执行一个模型轮次产生的命令。
//
// 命令始终按模型产生的顺序执行，不会并发：
// 一个接一个的读取、写入或编辑不会看到过时的内容，
// 而 bash 命令可能依赖前一个写入刚创建的文件。
type Runtime struct {
	commands            command.Command
	bus                 event.EventPublisher
	workspace           handler.Workspace
	msg                 chan<- string
	newRequester        requesterFactory
	skipDefaultHandlers bool
}

// NewRuntime 将命令注册表连接到事件总线。总线可以为 nil，
// 此时结果仅返回给调用方。opts 可控制是否跳过默认事件处理器。
func NewRuntime(opts ...RuntimeOption) *Runtime {
	var runtime Runtime
	for _, opt := range opts {
		opt(&runtime)
	}

	// 始终创建事件总线，以便调用方可以通过 Subscribe 注册自己的处理器。
	bus := event.NewEventBus()
	if !runtime.skipDefaultHandlers {
		if err := registerEventHandlers(bus); err != nil {
			panic(err)
		}
	}
	runtime.bus = bus

	var work = handler.Workspace{
		Root:      utils.GetCurrentPath(),
		Publisher: runtime.bus,
	}

	handle := command.NewCommandHandle()
	if err := handler.Register(handle, work); err != nil {
		panic(err)
	}

	runtime.workspace = work
	runtime.commands = handle

	if !runtime.skipDefaultHandlers {
		ch := make(chan string)
		runtime.msg = ch
		go runtime.messageChannel(ch)
	}

	return &runtime
}

// Subscribe 向运行时的事件总线注册事件处理器。
// 事件总线始终创建，调用方可随时订阅自定义处理器。
func (r *Runtime) Subscribe(eventName event.Event, handler event.EventHandler) error {
	if r.bus == nil {
		return nil
	}
	return r.bus.(*event.EventBus).Subscribe(eventName, handler)
}

// Publish 向运行时的事件总线发布事件，供外部模块（如 workflow 编排层）
// 在不接触内部字段的情况下通知订阅者。总线未初始化时为 no-op。
func (r *Runtime) Publish(e event.Event) error {
	if r.bus == nil {
		return nil
	}
	return r.bus.Publish(e)
}

// Close 关闭事件总线并等待已接受的事件处理完成。
func (r *Runtime) Close() {
	if bus, ok := r.bus.(*event.EventBus); ok {
		_ = bus.Close()
	}
}

// registerEventHandlers 将默认的 CLI 事件处理器订阅到给定的事件总线。
func registerEventHandlers(bus *event.EventBus) error {
	if err := bus.Subscribe(event2.ToolAfterEvent{}, event2.ToolAfterEventHandler{}); err != nil {
		return err
	}

	if err := bus.Subscribe(event2.ToolBeforeEvent{}, event2.ToolEventBeforeHandler{}); err != nil {
		return err
	}

	if err := bus.Subscribe(event2.TaskCompleteEvent{}, event2.TaskCompleteEventHandler{}); err != nil {
		return err
	}

	return nil
}

// RunTask 运行任务
func (r *Runtime) RunTask(task *Task) (error, string) {
	var err, result = r.execute(task)
	if err != nil {
		return err, result
	}
	r.bus.Publish(event2.NewTaskCompleteEvent(result, task.Id))
	return err, result
}

// ExecuteCommand execute command
func (r *Runtime) ExecuteCommand(opt command.CommandOption) (error, any) {
	return r.commands.Execute(opt)
}

// execute 驱动单个任务的模型循环，直到模型不再请求工具。
//
// 每轮在运行工具之前先存储助手消息，因此对话始终按协议要求读取：
// 携带工具调用的助手消息，然后每个调用对应一条工具消息。
func (r *Runtime) execute(task *Task) (error, string) {
	var requester = r.buildRequester(task.SessionInfo.GetProvider())

	// 如果 requester 支持且会话携带之前保存的状态，
	// 则恢复 otter 平台状态。这让恢复的流程可以继续
	// 同一个远程对话，而不是创建新对话。
	restoreOtterState(requester, task.SessionInfo)

	for turn := 0; turn < MaxTurns; turn++ {
		var res = requester.Request(task.SessionInfo.GetMessages(), r.msg)
		if res.Error != nil {
			return res.Error, ""
		}

		task.SessionInfo.AddUsage(res.Usage)
		r.reportUsage(task, res.Usage)

		// 每次成功请求后捕获 otter 平台状态，以便
		// 崩溃或恢复时能从对话中断处继续。
		captureOtterState(requester, task.SessionInfo)

		calls := llm.EnsureToolCallIDs(res.ToolCalls)
		if len(calls) == 0 && !task.SessionInfo.Provider.SupportsTools {
			// Provider 无法原生调用工具：模型将命令嵌入
			// 回复文本中，在此处提取出来。
			calls = llm.EnsureToolCallIDs(parse.ParseTextCalls(res.Content))
		}
		task.SessionInfo.AppendAssistant(res.Content, calls)
		if len(calls) == 0 {
			return nil, res.Content
		}

		if err := r.executeCommand(task, calls); err != nil {
			return err, ""
		}
	}

	return ErrMaxTurns, ""
}

// restoreOtterState 从会话保存的 otter 状态重新加载 requester，
// 如果 requester 支持且会话携带了状态。
func restoreOtterState(requester llm.Requester, session *Session) {
	type stateRestorer interface {
		RestoreState(llm.OtterState)
	}
	restore, ok := requester.(stateRestorer)
	if !ok {
		return
	}
	state := session.GetOtterState()
	if state.ChatSessionID != "" || state.Delivered > 0 {
		restore.RestoreState(state)
	}
}

// captureOtterState 将 requester 的 otter 平台状态快照保存到会话中，
// 如果 requester 支持。
func captureOtterState(requester llm.Requester, session *Session) {
	type stateCapture interface {
		State() llm.OtterState
	}
	if capture, ok := requester.(stateCapture); ok {
		session.SetOtterState(capture.State())
	}
}

// buildRequester 返回 info 的客户端，尊重测试在 Runtime 字面量上
// 安装的工厂函数。
func (r *Runtime) buildRequester(info llm.ModelInfo) llm.Requester {
	if r.newRequester != nil {
		return r.newRequester(info)
	}
	return llm.NewRequester(info)
}

// reportUsage 在 debug 开启时打印单轮的 token 消耗。
func (r *Runtime) reportUsage(task *Task, usage llm.Usage) {
	if tool.Get(tool.KeyDebug) != "true" {
		return
	}
	fmt.Printf("\033[38;5;243m[Token] 本次: prompt=%d, completion=%d, total=%d | 累计: %d/%d\033[0m\n",
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
		task.SessionInfo.TotalUsage.TotalTokens, task.SessionInfo.Provider.ContextWindow)
}

// executeCommand 按顺序运行一个助手轮次的工具调用，
// 并为每个调用回复恰好一条工具消息。
//
// 失败的调用也会被回复。失败以 CommandResult 的形式传递，
// 而不是 Go error，因为协议要求每个调用对应一条工具消息，
// 而且模型在能读取错误原因时比循环崩溃时更好地纠正错误路径。
func (r *Runtime) executeCommand(task *Task, calls []llm.ToolCall) error {
	for _, call := range calls {
		content, err := r.runTool(task, call)
		if err != nil {
			content = failureResult(call, err)
		}
		task.SessionInfo.AppendToolResult(call.ID, content)
	}
	return nil
}

// runTool 执行一个工具调用并渲染回复它的工具消息内容。
func (r *Runtime) runTool(task *Task, call llm.ToolCall) (string, error) {
	// 模式层工具过滤：如果 Session 声明了 AllowedTools，拒绝不在列表中的工具。
	if allowed := task.SessionInfo.Provider.AllowedTools; allowed != nil {
		if !isToolAllowed(call.Name, allowed) {
			return "", fmt.Errorf("tool %q is not allowed in current mode", call.Name)
		}
	}

	id := utils.GetSnowFlakeId()
	opt, err := parse.OptionFromCall(id, call.Name, call.Arguments)
	if err != nil {
		return "", err
	}

	// 将 reasoning 从 ToolCall 传入 option，供 handler 发布 ToolBeforeEvent 时使用
	parse.SetReasoning(opt, call.Reasoning)

	execErr, res := r.commands.Execute(opt)
	if execErr != nil {
		return "", execErr
	}

	result, ok := res.(handler.CommandResult)
	if !ok {
		return "", fmt.Errorf("agent: command %q returned an unexpected result", call.Name)
	}

	// create_task 携带子任务而非负载：它们在此处运行，
	// 聚合结果作为工具消息回复。
	if result.OK && result.Command == handler.CommandCreateTask {
		return r.runSubTasks(task, result), nil
	}
	return result.JSON(), nil
}

// failureResult 渲染运行时未能执行的工具消息，
// 例如未知工具或无法解码的参数。
func failureResult(call llm.ToolCall, err error) string {
	return handler.NewResult(0, call.Name, "", err).JSON()
}

// runSubTasks 执行 create_task 调用的子任务，并将它们的结果
// 折叠到回复该调用的单条工具消息中。
//
// 每个子任务都是独立的任务，拥有自己的对话，因此它会获得
// 与父任务相同的系统提示。没有它，子任务将不知道
// 它工作的项目和应遵循的约定，会开始随机探索目录树。
func (r *Runtime) runSubTasks(parent *Task, result handler.CommandResult) string {
	var out strings.Builder
	fmt.Fprintf(&out, "created %d sub-task(s)\n", len(result.TaskTarget))

	for index, target := range result.TaskTarget {
		child := NewTask(target.Description, target.Title)
		child.SessionInfo.Provider = parent.SessionInfo.Provider
		child.SessionInfo.ProjectPath = parent.SessionInfo.ProjectPath
		child.SessionInfo.AppendMessage(llm.RoleSystem, utils.GetSystemPrompt(PromptContext{
			ProjectPath:   child.SessionInfo.ProjectPath,
			ContextLength: child.SessionInfo.Provider.ContextWindow,
		}))
		child.SessionInfo.AppendToolPrompt()
		child.SessionInfo.AppendMessage(llm.RoleUser, utils.GetSubtaskPrompt(NewTaskContext(target.Description, target.Title)))

		fmt.Fprintf(&out, "\n--- sub-task %d: %s ---\n", index+1, target.Title)
		err, msg := r.RunTask(child)
		if err != nil {
			fmt.Fprintf(&out, "failed: %v\n", err)
			continue
		}
		out.WriteString(msg)
		out.WriteString("\n")
	}
	return out.String()
}

// isToolAllowed 检查 name 是否在 allowed 列表中。
func isToolAllowed(name string, allowed []string) bool {
	for _, a := range allowed {
		if a == name {
			return true
		}
	}
	return false
}

func (r *Runtime) messageChannel(ch <-chan string) {
	for msg := range ch {
		fmt.Printf("%s", msg)
	}
}
