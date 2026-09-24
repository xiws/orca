package core

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/xiws/orca/internal/agent/parse"
	event2 "github.com/xiws/orca/internal/event"
	"github.com/xiws/orca/internal/handler"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/internal/tool"
	"github.com/xiws/orca/pkg/command"
	"github.com/xiws/orca/pkg/event"
	"github.com/xiws/orca/pkg/utils"
)

// MaxTurns 单次执行的最大轮次，防止模型无限循环调用工具。
const MaxTurns = 64

// ErrMaxTurns 达到最大轮次限制时返回的错误。
var ErrMaxTurns = errors.New("agent: reached the turn limit while the model kept calling tools")

// requesterFactory 是创建 LLM 请求器的工厂函数类型。
type requesterFactory func(llm.ModelInfo) llm.Requester

// RuntimeOption 是 Runtime 的功能选项。
type RuntimeOption func(*Runtime)

// WithSkipDefaultHandlers 跳过注册默认的事件处理器（用于测试等场景）。
func WithSkipDefaultHandlers() RuntimeOption {
	return func(r *Runtime) { r.skipDefaultHandlers = true }
}

// WithMsgChannel 设置消息输出通道，用于接收模型流式输出。
func WithMsgChannel(ch chan<- string) RuntimeOption {
	return func(r *Runtime) { r.msg = ch }
}

// WithWorkspace 设置工作区根目录。
func WithWorkspace(root string) RuntimeOption {
	return func(r *Runtime) { r.workspace.Root = root }
}

// Runtime 是 Agent 的运行时环境，管理事件总线、命令执行和 LLM 交互。
// 它是 Agent 模式、角色的底座，负责编排工具调用循环。
type Runtime struct {
	// commands 命令处理器，负责执行工具调用。
	commands command.Command
	// bus 事件总线，用于发布/订阅工具执行事件。
	bus event.EventPublisher
	// workspace 工作区配置（根目录和事件发布器）。
	workspace handler.Workspace
	// msg 流式消息输出通道。
	msg chan<- string
	// newRequester 自定义的 LLM 请求器工厂（可选）。
	newRequester requesterFactory
	// skipDefaultHandlers 是否跳过默认事件处理器注册。
	skipDefaultHandlers bool
	// ownedMsg Runtime 自己创建的消息通道（未外部提供时使用）。
	ownedMsg chan string
	// output 用于等待消息输出 goroutine 完成。
	output sync.WaitGroup
	// closeOnce 确保 Close 只执行一次。
	closeOnce sync.Once
}

// NewRuntime 创建 Agent 运行时，按选项初始化事件总线、命令处理器和消息通道。
// 如果未提供消息通道，会自动创建一个并将输出打印到标准输出。
func NewRuntime(opts ...RuntimeOption) *Runtime {
	runtime := &Runtime{}
	for _, opt := range opts {
		opt(runtime)
	}

	// 初始化事件总线并注册默认处理器
	bus := event.NewEventBus()
	if !runtime.skipDefaultHandlers {
		if err := registerEventHandlers(bus); err != nil {
			panic(err)
		}
	}
	runtime.bus = bus
	if runtime.workspace.Root == "" {
		runtime.workspace.Root = utils.GetCurrentPath()
	}
	runtime.workspace.Publisher = bus

	// 注册命令处理器
	handle := command.NewCommandHandle()
	if err := handler.Register(handle, runtime.workspace); err != nil {
		panic(err)
	}
	runtime.commands = handle

	// 未提供消息通道时，创建默认通道并启动后台输出 goroutine
	if !runtime.skipDefaultHandlers && runtime.msg == nil {
		runtime.ownedMsg = make(chan string)
		runtime.msg = runtime.ownedMsg
		runtime.output.Add(1)
		go func() {
			defer runtime.output.Done()
			for msg := range runtime.ownedMsg {
				fmt.Print(msg)
			}
		}()
	}
	return runtime
}

// Subscribe 订阅指定事件。
func (r *Runtime) Subscribe(eventName event.Event, handler event.EventHandler) error {
	if r.bus == nil {
		return nil
	}
	return r.bus.(*event.EventBus).Subscribe(eventName, handler)
}

// Publish 发布事件到事件总线。
func (r *Runtime) Publish(e event.Event) error {
	if r.bus == nil {
		return nil
	}
	return r.bus.Publish(e)
}

// Close 释放运行时资源，关闭事件总线和消息通道。
func (r *Runtime) Close() {
	r.closeOnce.Do(func() {
		if bus, ok := r.bus.(*event.EventBus); ok {
			_ = bus.Close()
		}
		if r.ownedMsg != nil {
			close(r.ownedMsg)
			r.output.Wait()
		}
	})
}

// registerEventHandlers 注册默认的工具执行事件处理器（执行前后事件）。
func registerEventHandlers(bus *event.EventBus) error {
	if err := bus.Subscribe(event2.ToolAfterEvent{}, event2.ToolAfterEventHandler{}); err != nil {
		return err
	}
	return bus.Subscribe(event2.ToolBeforeEvent{}, event2.ToolEventBeforeHandler{})
}

// Run 执行一次完整的 LLM 调用循环：请求模型 → 解析工具调用 → 执行工具 → 重复，
// 直到模型不再请求工具或达到最大轮次限制。返回模型的最终文本回复。
func (r *Runtime) Run(inv *Invocation) (string, error) {
	requester := r.buildRequester(inv.Provider)
	// 恢复 Provider 状态（支持断点续传）
	restoreOtterState(requester, inv)

	for turn := 0; turn < MaxTurns; turn++ {
		res := requester.Request(inv.Messages, r.msg)
		if res.Error != nil {
			return "", res.Error
		}
		inv.AddUsage(res.Usage)
		r.reportUsage(inv, res.Usage)
		// 捕获 Provider 状态以便后续恢复
		captureOtterState(requester, inv)

		// 解析工具调用：优先使用原生工具调用，否则从文本中提取
		calls := llm.EnsureToolCallIDs(res.ToolCalls)
		if len(calls) == 0 && !inv.Provider.SupportsTools {
			calls = llm.EnsureToolCallIDs(parse.ParseTextCalls(res.Content))
		}
		inv.AppendAssistant(res.Content, calls)
		// 没有工具调用则返回最终结果
		if len(calls) == 0 {
			return res.Content, nil
		}
		if err := r.executeCommand(inv, calls); err != nil {
			return "", err
		}
	}
	return "", ErrMaxTurns
}

// ExecuteCommand 直接执行一条命令选项，返回执行结果。
func (r *Runtime) ExecuteCommand(opt command.CommandOption) (error, any) {
	return r.commands.Execute(opt)
}

// restoreOtterState 尝试从 Invocation 恢复 Provider 的会话状态。
func restoreOtterState(requester llm.Requester, inv *Invocation) {
	restore, ok := requester.(interface{ RestoreState(llm.OtterState) })
	if !ok {
		return
	}
	state := inv.GetOtterState()
	if state.ChatSessionID != "" || state.Delivered > 0 {
		restore.RestoreState(state)
	}
}

// captureOtterState 从 Provider 捕获会话状态并保存到 Invocation。
func captureOtterState(requester llm.Requester, inv *Invocation) {
	if capture, ok := requester.(interface{ State() llm.OtterState }); ok {
		inv.SetOtterState(capture.State())
	}
}

// buildRequester 根据模型信息创建 LLM 请求器。
func (r *Runtime) buildRequester(info llm.ModelInfo) llm.Requester {
	if r.newRequester != nil {
		return r.newRequester(info)
	}
	return llm.NewRequester(info)
}

// reportUsage 在 debug 模式下输出 token 用量信息。
func (r *Runtime) reportUsage(inv *Invocation, usage llm.Usage) {
	if tool.Get(tool.KeyDebug) != "true" {
		return
	}
	fmt.Printf("\033[38;5;243m[Token] 本次: prompt=%d, completion=%d, total=%d | 本次执行累计: %d\033[0m\n",
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, inv.TotalUsage.TotalTokens)
}

// executeCommand 依次执行所有工具调用，并将结果追加到消息历史。
// 单个工具执行失败不会中断整个流程，错误信息会作为工具结果返回给模型。
func (r *Runtime) executeCommand(inv *Invocation, calls []llm.ToolCall) error {
	for _, call := range calls {
		content, err := r.runTool(inv, call)
		if err != nil {
			content = failureResult(call, err)
		}
		inv.AppendToolResult(call.ID, content)
	}
	return nil
}

// runTool 执行单个工具调用，检查工具权限并解析参数。
func (r *Runtime) runTool(inv *Invocation, call llm.ToolCall) (string, error) {
	// 检查工具是否在当前模式的白名单中
	if allowed := inv.Provider.AllowedTools; allowed != nil && !isToolAllowed(call.Name, allowed) {
		return "", fmt.Errorf("tool %q is not allowed in current mode", call.Name)
	}

	opt, err := parse.OptionFromCall(utils.GetSnowFlakeId(), call.Name, call.Arguments)
	if err != nil {
		return "", err
	}
	parse.SetReasoning(opt, call.Reasoning)
	execErr, res := r.commands.Execute(opt)
	if execErr != nil {
		return "", execErr
	}
	result, ok := res.(handler.CommandResult)
	if !ok {
		return "", fmt.Errorf("agent: command %q returned an unexpected result", call.Name)
	}
	// 创建子任务的特殊处理：递归执行子任务
	if result.OK && result.Command == handler.CommandCreateTask {
		return r.runSubTasks(inv, result), nil
	}
	return result.JSON(), nil
}

// failureResult 将工具执行错误格式化为 JSON 结果。
func failureResult(call llm.ToolCall, err error) string {
	return handler.NewResult(0, call.Name, "", err).JSON()
}

// runSubTasks 递归执行子任务，每个子任务在独立的子 Invocation 中运行。
func (r *Runtime) runSubTasks(parent *Invocation, result handler.CommandResult) string {
	var out strings.Builder
	fmt.Fprintf(&out, "created %d sub-task(s)\n", len(result.TaskTarget))
	for index, target := range result.TaskTarget {
		child := parent.NewChild()
		// 为子任务初始化系统提示词和对话上下文
		child.AppendMessage(llm.RoleSystem, utils.GetSystemPrompt(SystemPromptContext{
			ProjectPath: child.ProjectPath, ContextLength: child.Provider.ContextWindow,
		}))
		if !child.Provider.SupportsTools {
			child.AppendMessage(llm.RoleSystem, handler.ToolPrompt())
		}
		child.AppendMessage(llm.RoleUser, utils.GetSubtaskPrompt(NewTaskContext(target.Description, target.Title)))
		fmt.Fprintf(&out, "\n--- sub-task %d: %s ---\n", index+1, target.Title)
		msg, err := r.Run(child)
		if err != nil {
			fmt.Fprintf(&out, "failed: %v\n", err)
			continue
		}
		out.WriteString(msg)
		out.WriteString("\n")
	}
	return out.String()
}

// isToolAllowed 检查工具名是否在白名单中。
func isToolAllowed(name string, allowed []string) bool {
	for _, candidate := range allowed {
		if candidate == name {
			return true
		}
	}
	return false
}
