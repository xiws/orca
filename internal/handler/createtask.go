package handler

import (
	"fmt"
	"orca/pkg/command"
)

// CreateTaskOption is the "createTask" command parameter: it carries the
// high-level goal the user wants the AI to decompose into sub-tasks, run
// each one independently, and then aggregate the results.
type CreateTaskOption struct {
	Id         int64
	Reasoning  string
	TaskTarget []TaskBaseInfo `json:"task_target"`
}

type CreateTaskHandler struct {
}

// NewCreateTaskHandler create a task
func NewCreateTaskHandler() *CreateTaskHandler {
	return &CreateTaskHandler{}
}

// GetId returns the invocation id that correlates the result with this call.
func (t CreateTaskOption) GetId() int64 { return t.Id }

// GetName returns the routing name of the command.
func (CreateTaskOption) GetName() string { return CommandCreateTask }

// NewCreateTaskOption builds a createTask command.
func NewCreateTaskOption(id int64, taskTarget []TaskBaseInfo) *CreateTaskOption {
	return &CreateTaskOption{Id: id, TaskTarget: taskTarget}
}

func (h *CreateTaskHandler) Handle(cmd command.CommandOption) (error, any) {
	opt, ok := cmd.(*CreateTaskOption)
	if !ok {
		return ErrUnsupportedOption, nil
	}

	var targets []TaskBaseInfo
	var isHasTarget bool = false
	for _, target := range opt.TaskTarget {
		if target.Title == "" || target.Description == "" {
			continue
		}
		isHasTarget = true
		targets = append(targets, target)
	}

	if !isHasTarget {
		return nil, ResultFor(cmd, "", fmt.Errorf("task_target is required"))
	}

	var result = ResultFor(cmd, "", nil)
	result.TaskTarget = targets
	return nil, result
}
