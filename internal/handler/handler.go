// Package handler implements the tool commands the agent can invoke: read,
// write, edit and bash. Every handler satisfies command.CommandHandler and
// reports business outcomes through CommandResult, keeping the error channel of
// the registry free for framework level failures.
package handler

import (
	"errors"
	"fmt"
	"orca/pkg/utils"

	"orca/pkg/command"
)

// Names of the built-in tool commands. They are the routing names used by the
// command registry and the value of the "command" field on the wire protocol.
const (
	CommandRead       = "read"
	CommandWrite      = "write"
	CommandEdit       = "edit"
	CommandBash       = "bash"
	CommandCreateTask = "create_task"
)

var (
	// ErrUnsupportedOption is returned when a handler is dispatched an option
	// of a type it cannot process.
	ErrUnsupportedOption = errors.New("unsupported command option")
	// ErrEmptyFilename is returned when a file command carries no filename.
	ErrEmptyFilename = errors.New("filename is required")
	// ErrEmptyCommand is returned when a bash command carries no command line.
	ErrEmptyCommand = errors.New("command is required")
	// ErrEmptyContents is returned when an edit command has no fragments.
	ErrEmptyContents = errors.New("edit contents is empty")
	// ErrOutsideWorkspace is returned when a path resolves outside the allowed
	// root directory.
	ErrOutsideWorkspace = errors.New("path is outside the workspace")
	// ErrFragmentLocator is returned when an edit fragment carries no diff.
	ErrFragmentLocator = errors.New("fragment needs a diff")
)

// CommandResult is the uniform result of a command execution. It is the value
// returned in the second slot of CommandHandler.Handle and is serialized back to
// the model, so a failure is data rather than a Go error.
type CommandResult struct {
	// Id correlates the result with the invocation that produced it, matching
	// the id the call carried.
	Id      int64  `json:"id"`
	Command string `json:"command"`
	OK      bool   `json:"ok"`
	// Content carries the command payload: file contents for read, merged
	// stdout and stderr for bash, a change summary for write and edit.
	Content string `json:"content"`
	// Err holds a short reason and is empty when OK is true.
	Err        string         `json:"err,omitempty"`
	TaskTarget []TaskBaseInfo `json:"task_target,omitempty"`
}

type TaskBaseInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

func (u CommandResult) String() string {
	return fmt.Sprintf("task id:%d \ncommand:%s\nresult:%s", u.Id, u.Command, u.Content)
}

// NewResult builds a CommandResult for the given identity. A non-nil err marks
// the result as failed and puts its message into Err.
func NewResult(id int64, name, content string, err error) CommandResult {
	result := CommandResult{Id: id, Command: name, Content: content, OK: err == nil}
	if err != nil {
		result.Err = err.Error()
	}
	return result
}

// ResultFor builds a CommandResult for a command option, so handlers only need
// to produce a payload and an error. Options built in code without an id still
// get one, keeping every result correlatable with a call.
func ResultFor(cmd command.CommandOption, content string, err error) CommandResult {
	id := cmd.GetId()
	if id == 0 {
		id = utils.GetSnowFlakeId()
	}
	return NewResult(id, cmd.GetName(), content, err)
}

// Register adds handlers for every built-in tool command to handle, scoping
// file access to ws.
func Register(handle *command.CommandHandle, ws Workspace) error {
	entries := []struct {
		option  command.CommandOption
		handler command.CommandHandler
	}{
		{&ReadOption{}, ReadHandler{Workspace: ws}},
		{&WriteOption{}, WriteHandler{Workspace: ws}},
		{&EditOption{}, EditHandler{Workspace: ws}},
		{&BashOption{}, BashHandler{Workspace: ws}},
		{&CreateTaskOption{}, &CreateTaskHandler{Workspace: ws}},
	}

	for _, entry := range entries {
		if err := handle.Register(entry.option, entry.handler); err != nil {
			return err
		}
	}
	return nil
}
