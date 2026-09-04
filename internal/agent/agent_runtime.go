// Package agent drives a conversation: it asks a model for the next step,
// runs the commands the model asks for and hands the results back to it.
package agent

import (
	"encoding/json"
	"fmt"

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
	commands *command.CommandHandle
	bus      event.EventPublisher
}

// NewRuntime wires a command registry to an event bus. The bus may be nil, in
// which case results are only returned to the caller.
func NewRuntime() *Runtime {
	handle := command.NewCommandHandle()
	if err := handler.Register(handle, handler.Workspace{
		Root: utils.GetCurrentPath(),
	}); err != nil {
		panic(err)
	}

	var runtime = &Runtime{commands: handle}
	return runtime
}

// RegisterEvent starts the event bus the runtime publishes command results on,
// if it does not exist yet, and subscribes h to eventName. Call it repeatedly to
// attach more subscribers to the same bus; the topics are the names returned by
// event.Event.GetName.
func registerEvent(eventName string, h event.EventHandler) error {
	bus := event.NewEventBus()
	return bus.Subscribe(eventName, h)
}

func (r *Runtime) RunTask(task Task) (error, string) {
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

func (r *Runtime) execute(task Task) (error, string) {
	var requester = llm.NewRequester(task.SessionInfo.GetProvider())
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
func (r *Runtime) executeCommand(task Task, tools []llm.ToolCall) error {
	for _, call := range tools {
		id := utils.GetSnowFlakeId()
		opt, err := handler.OptionFromCall(id, call.Name, call.Arguments)
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

		content, err := json.Marshal(result)
		if err != nil {
			return err
		}
		task.SessionInfo.AppendMessage(llm.RoleTool, string(content))
	}
	return nil
}
