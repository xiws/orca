// Package agent drives a conversation: it asks a model for the next step,
// runs the commands the model asks for and hands the results back to it.
package agent

import (
	"errors"
	"fmt"
	"strings"

	event2 "orca/internal/event"
	"orca/internal/tool"

	"orca/internal/handler"
	"orca/internal/llm"
	"orca/pkg/command"
	"orca/pkg/event"
	"orca/pkg/utils"
)

// MaxTurns caps how many model turns one task may take. A model that keeps
// asking for tools would otherwise loop forever and never hand back an answer.
const MaxTurns = 64

// ErrMaxTurns is returned when a task reaches MaxTurns while the model is still
// calling tools.
var ErrMaxTurns = errors.New("agent: reached the turn limit while the model kept calling tools")

// requesterFactory builds the client used to talk to the model. A test installs
// its own to drive the loop against a scripted model.
type requesterFactory func(llm.ModelInfo) llm.Requester

// Runtime executes the commands produced by one model turn.
//
// Commands always run in the order the model produced them and never
// concurrently: a read, write or edit that follows another one must not see
// stale content, and a bash command may depend on the file a previous write
// just created.
type Runtime struct {
	commands     command.Command
	bus          event.EventPublisher
	workspace    handler.Workspace
	msg          chan<- string
	newRequester requesterFactory
}

// NewRuntime wires a command registry to an event bus. The bus may be nil, in
// which case results are only returned to the caller.
func NewRuntime() *Runtime {

	bus, err := registerEvent()
	if err != nil {
		panic(err)
	}

	var work = handler.Workspace{
		Root:      utils.GetCurrentPath(),
		Publisher: bus,
	}

	handle := command.NewCommandHandle()
	if err := handler.Register(handle, work); err != nil {
		panic(err)
	}

	ch := make(chan string)

	var runtime = &Runtime{commands: handle, bus: bus, workspace: work, msg: ch}
	go runtime.messageChannel(ch)
	return runtime
}

// RegisterEvent starts the event bus the runtime publishes command results on,
// if it does not exist yet, and subscribes h to eventName. Call it repeatedly to
// attach more subscribers to the same bus; the topics are the names returned by
// event.Event.GetName.
func registerEvent() (event.EventPublisher, error) {
	bus := event.NewEventBus()
	if err := bus.Subscribe(event2.BashEvent{}, event2.BashEventHandler{}); err != nil {
		return nil, err
	}

	if err := bus.Subscribe(event2.ToolAfterEvent{}, event2.ToolAfterEventHandler{}); err != nil {
		return nil, err
	}

	if err := bus.Subscribe(event2.ToolBeforeEvent{}, event2.ToolEventBeforeHandler{}); err != nil {
		return nil, err
	}

	if err := bus.Subscribe(event2.TaskComplateEvent{}, event2.TaskComplateEventHandler{}); err != nil {
		return nil, err
	}

	return bus, nil
}

// RunTask 运行任务
func (r *Runtime) RunTask(task *Task) (error, string) {
	var err, result = r.execute(task)
	if err != nil {
		return err, result
	}
	r.bus.Publish(event2.NewTaskComplateEvent(result, task.Id))
	return err, result
}

// ExecuteCommand execute command
func (r *Runtime) ExecuteCommand(opt command.CommandOption) (error, any) {
	return r.commands.Execute(opt)
}

// execute drives the model loop of a single task until it stops asking for
// tools.
//
// Every round stores the assistant turn before running its tools, so the
// conversation always reads back as the protocol requires: the assistant
// message carrying tool calls, then one tool message per call quoting it.
func (r *Runtime) execute(task *Task) (error, string) {
	var requester = r.buildRequester(task.SessionInfo.GetProvider())

	for turn := 0; turn < MaxTurns; turn++ {
		var res = requester.Request(task.SessionInfo.GetMessages(), nil)
		if res.Error != nil {
			return res.Error, ""
		}

		task.SessionInfo.AddUsage(res.Usage)
		r.reportUsage(task, res.Usage)

		calls := llm.EnsureToolCallIDs(res.ToolCalls)
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

// buildRequester returns the client for info, honouring the factory a test
// installed on a Runtime literal.
func (r *Runtime) buildRequester(info llm.ModelInfo) llm.Requester {
	if r.newRequester != nil {
		return r.newRequester(info)
	}
	return llm.NewOpenAIRequester(info)
}

// reportUsage prints the token consumption of one round when debug is on.
func (r *Runtime) reportUsage(task *Task, usage llm.Usage) {
	if tool.Get(tool.KeyDebug) != "true" {
		return
	}
	fmt.Printf("\033[38;5;243m[Token] 本次: prompt=%d, completion=%d, total=%d | 累计: %d/%d\033[0m\n",
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens,
		task.SessionInfo.TotalUsage.TotalTokens, task.SessionInfo.Provider.ContextWindow)
}

// executeCommand runs the tool calls of one assistant turn, in order, and
// answers every one of them with exactly one tool message.
//
// A call that fails is answered too. The failure travels as a failed
// CommandResult rather than as a Go error, because the protocol demands one tool
// message per call and a model corrects a wrong path or a bad argument far
// better when it can read what went wrong than when the loop dies.
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

// runTool executes one tool call and renders the content of the tool message
// that answers it.
func (r *Runtime) runTool(task *Task, call llm.ToolCall) (string, error) {
	id := utils.GetSnowFlakeId()
	opt, err := OptionFromCall(id, call.Name, call.Arguments)
	if err != nil {
		return "", err
	}

	// 将 reasoning 从 ToolCall 传入 option，供 handler 发布 ToolBeforeEvent 时使用
	setReasoning(opt, call.Reasoning)

	execErr, res := r.commands.Execute(opt)
	if execErr != nil {
		return "", execErr
	}

	result, ok := res.(handler.CommandResult)
	if !ok {
		return "", fmt.Errorf("agent: command %q returned an unexpected result", call.Name)
	}

	// create_task carries sub-tasks rather than a payload: they are run here and
	// the aggregate answer is what the tool message reports back.
	if result.OK && result.Command == handler.CommandCreateTask {
		return r.runSubTasks(task, result), nil
	}
	return result.JSON(), nil
}

// failureResult renders the tool message for a call the runtime never got to
// run, such as an unknown tool or arguments it cannot decode.
func failureResult(call llm.ToolCall, err error) string {
	return handler.NewResult(0, call.Name, "", err).JSON()
}

// runSubTasks executes the sub-tasks of a create_task call and folds their
// answers into the single tool message that answers that call.
//
// Each sub-task is a task of its own with its own conversation, so it is given
// the same system prompt the parent runs under. Without it the child would know
// neither the project it works in nor the conventions it is meant to follow,
// and would start by exploring the tree at random.
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

func (r *Runtime) messageChannel(ch <-chan string) {
	for msg := range ch {
		fmt.Printf("%s", msg)
	}
}
