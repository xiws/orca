// Package agent drives a conversation: it asks a model for the next step,
// runs the commands the model asks for and hands the results back to it.
package agent

import (
	"fmt"
	event2 "orca/internal/event"
	"orca/internal/tool"

	"orca/internal/handler"
	"orca/internal/llm"
	"orca/pkg/command"
	"orca/pkg/event"
	"orca/pkg/utils"
)

// Runtime executes the commands produced by one model turn.
//
// Commands always run in the order the model produced them and never
// concurrently: a read, write or edit that follows another one must not see
// stale content, and a bash command may depend on the file a previous write
// just created.
type Runtime struct {
	commands  command.Command
	bus       event.EventPublisher
	workspace handler.Workspace
	msg       chan<- string
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
	if task.Children != nil && len(task.Children) > 0 {
		for _, child := range task.Children {
			err, msg := r.RunTask(child)
			if err != nil {
				return err, msg
			}
			task.TaskTarget = msg
		}
	}

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

func (r *Runtime) execute(task *Task) (error, string) {
	var requester = llm.NewOpenAIRequester(task.SessionInfo.GetProvider())
	var res = requester.Request(task.SessionInfo.Messages, nil)
	if res.Error != nil {
		return res.Error, ""
	}

	// 累计 token 消耗并打印使用情况
	task.SessionInfo.AddUsage(res.Usage)
	contextWindow := task.SessionInfo.Provider.ContextWindow
	if tool.Get(tool.KeyDebug) == "true" {
		fmt.Printf("\033[38;5;243m[Token] 本次: prompt=%d, completion=%d, total=%d | 累计: %d/%d\033[0m\n",
			res.Usage.PromptTokens, res.Usage.CompletionTokens, res.Usage.TotalTokens,
			task.SessionInfo.TotalUsage.TotalTokens, contextWindow)
	}

	if res.FinishReason == "tool_calls" {
		if err := r.executeCommand(task, res.ToolCalls); err != nil {
			return err, ""
		}

		return r.execute(task)
	}

	return nil, res.Content
}

// executeCommand runs the tool calls the model produced, in order, and appends
// each command result back into the session as a tool message so the next model
// turn can see what its calls produced. The result is serialized to JSON because
// handler.CommandResult is meant to travel to the model as data, a failure
// included, rather than as a Go error.
func (r *Runtime) executeCommand(task *Task, tools []llm.ToolCall) error {
	for _, call := range tools {
		id := utils.GetSnowFlakeId()
		opt, err := OptionFromCall(id, call.Name, call.Arguments)
		if err != nil {
			return err
		}

		// 将 reasoning 从 ToolCall 传入 option，供 handler 发布 ToolBeforeEvent 时使用
		setReasoning(opt, call.Reasoning)

		execErr, res := r.commands.Execute(opt)
		if execErr != nil {
			return execErr
		}

		result, ok := res.(handler.CommandResult)
		if !ok {
			return fmt.Errorf("agent: command %q returned an unexpected result", call.Name)
		}

		var subTasks = make([]*Task, len(result.TaskTarget))
		if result.Command == handler.CommandCreateTask {
			for index, target := range result.TaskTarget {
				var childTask = NewTask(target.Description, target.Title)
				subTasks[index] = childTask
				taskContext := NewTaskContext(target.Description, target.Title)
				taskPrompt := utils.GetSubtaskPrompt(taskContext)
				childTask.SessionInfo.AppendMessage(llm.RoleUser, taskPrompt)
				err, msg := r.RunTask(childTask)
				if err != nil {
					task.SessionInfo.AppendMessage(llm.RoleAssistant, msg)
				}
			}
			continue
		}

		task.SessionInfo.AppendMessage(llm.RoleTool, result.String())
	}
	return nil
}

func (r *Runtime) messageChannel(ch <-chan string) {
	for msg := range ch {
		fmt.Printf("%s", msg)
	}
}
