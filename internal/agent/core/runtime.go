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

const MaxTurns = 64

var ErrMaxTurns = errors.New("agent: reached the turn limit while the model kept calling tools")

type requesterFactory func(llm.ModelInfo) llm.Requester

type RuntimeOption func(*Runtime)

func WithSkipDefaultHandlers() RuntimeOption {
	return func(r *Runtime) { r.skipDefaultHandlers = true }
}

func WithMsgChannel(ch chan<- string) RuntimeOption {
	return func(r *Runtime) { r.msg = ch }
}

func WithWorkspace(root string) RuntimeOption {
	return func(r *Runtime) { r.workspace.Root = root }
}

type Runtime struct {
	commands            command.Command
	bus                 event.EventPublisher
	workspace           handler.Workspace
	msg                 chan<- string
	newRequester        requesterFactory
	skipDefaultHandlers bool
	ownedMsg            chan string
	output              sync.WaitGroup
	closeOnce           sync.Once
}

func NewRuntime(opts ...RuntimeOption) *Runtime {
	runtime := &Runtime{}
	for _, opt := range opts {
		opt(runtime)
	}

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

	handle := command.NewCommandHandle()
	if err := handler.Register(handle, runtime.workspace); err != nil {
		panic(err)
	}
	runtime.commands = handle

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

func (r *Runtime) Subscribe(eventName event.Event, handler event.EventHandler) error {
	if r.bus == nil {
		return nil
	}
	return r.bus.(*event.EventBus).Subscribe(eventName, handler)
}

func (r *Runtime) Publish(e event.Event) error {
	if r.bus == nil {
		return nil
	}
	return r.bus.Publish(e)
}

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

func registerEventHandlers(bus *event.EventBus) error {
	if err := bus.Subscribe(event2.ToolAfterEvent{}, event2.ToolAfterEventHandler{}); err != nil {
		return err
	}
	return bus.Subscribe(event2.ToolBeforeEvent{}, event2.ToolEventBeforeHandler{})
}

func (r *Runtime) Run(inv *Invocation) (string, error) {
	requester := r.buildRequester(inv.Provider)
	restoreOtterState(requester, inv)

	for turn := 0; turn < MaxTurns; turn++ {
		res := requester.Request(inv.Messages, r.msg)
		if res.Error != nil {
			return "", res.Error
		}
		inv.AddUsage(res.Usage)
		r.reportUsage(inv, res.Usage)
		captureOtterState(requester, inv)

		calls := llm.EnsureToolCallIDs(res.ToolCalls)
		if len(calls) == 0 && !inv.Provider.SupportsTools {
			calls = llm.EnsureToolCallIDs(parse.ParseTextCalls(res.Content))
		}
		inv.AppendAssistant(res.Content, calls)
		if len(calls) == 0 {
			return res.Content, nil
		}
		if err := r.executeCommand(inv, calls); err != nil {
			return "", err
		}
	}
	return "", ErrMaxTurns
}

func (r *Runtime) ExecuteCommand(opt command.CommandOption) (error, any) {
	return r.commands.Execute(opt)
}

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

func captureOtterState(requester llm.Requester, inv *Invocation) {
	if capture, ok := requester.(interface{ State() llm.OtterState }); ok {
		inv.SetOtterState(capture.State())
	}
}

func (r *Runtime) buildRequester(info llm.ModelInfo) llm.Requester {
	if r.newRequester != nil {
		return r.newRequester(info)
	}
	return llm.NewRequester(info)
}

func (r *Runtime) reportUsage(inv *Invocation, usage llm.Usage) {
	if tool.Get(tool.KeyDebug) != "true" {
		return
	}
	fmt.Printf("\033[38;5;243m[Token] 本次: prompt=%d, completion=%d, total=%d | 本次执行累计: %d\033[0m\n",
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, inv.TotalUsage.TotalTokens)
}

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

func (r *Runtime) runTool(inv *Invocation, call llm.ToolCall) (string, error) {
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
	if result.OK && result.Command == handler.CommandCreateTask {
		return r.runSubTasks(inv, result), nil
	}
	return result.JSON(), nil
}

func failureResult(call llm.ToolCall, err error) string {
	return handler.NewResult(0, call.Name, "", err).JSON()
}

func (r *Runtime) runSubTasks(parent *Invocation, result handler.CommandResult) string {
	var out strings.Builder
	fmt.Fprintf(&out, "created %d sub-task(s)\n", len(result.TaskTarget))
	for index, target := range result.TaskTarget {
		child := parent.NewChild()
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

func isToolAllowed(name string, allowed []string) bool {
	for _, candidate := range allowed {
		if candidate == name {
			return true
		}
	}
	return false
}
