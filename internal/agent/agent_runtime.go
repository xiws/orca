// Package agent drives a conversation: it asks a model for the next step,
// runs the commands the model asks for and hands the results back to it.
package agent

import (
	"fmt"
	event2 "orca/internal/event"

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
	commands  *command.CommandHandle
	bus       event.EventPublisher
	workspace handler.Workspace
	msg       chan<- string
}

// NewRuntime wires a command registry to an event bus. The bus may be nil, in
// which case results are only returned to the caller.
func NewRuntime() *Runtime {
	handle := command.NewCommandHandle()
	var work = handler.Workspace{
		Root: utils.GetCurrentPath(),
	}

	if err := handler.Register(handle, work); err != nil {
		panic(err)
	}
	bus, err := registerEvent()
	if err != nil {
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

	if err := bus.Subscribe(event2.ToolEvent{}, event2.ToolEventHandler{}); err != nil {
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

	return r.execute(task)
}

func (r *Runtime) execute(task *Task) (error, string) {
	var requester = llm.NewOpenAIRequester(task.SessionInfo.GetProvider())
	var res = requester.Request(task.SessionInfo.Messages, nil)
	if res.Error != nil {
		return res.Error, ""
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
			for _, target := range result.TaskTarget {
				var childTask = NewTask(target.Description, target.Title)
				subTasks = append(subTasks, childTask)
				err, msg := r.RunTask(childTask)
				taskPrompt := utils.GetSubtaskPrompt(NewTaskContext(target.Description, target.Title, msg))
				if err != nil {
					task.SessionInfo.AppendMessage(llm.RoleAssistant, taskPrompt)
				}
			}
			continue
		}

		task.SessionInfo.AppendMessage(llm.RoleTool, result.String())
		r.bus.Publish(event2.NewToolEvent(call.Name+"\t"+call.Arguments, result.Content, result.Id))
	}
	return nil
}

func (r *Runtime) messageChannel(ch <-chan string) {
	for msg := range ch {
		fmt.Printf("%s", msg)
	}
}
