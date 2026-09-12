package handler

import (
	"fmt"
	"github.com/xiws/orca/internal/event"
	"github.com/xiws/orca/pkg/command"
	"strings"
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
	Workspace
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

	meta := h.buildMeta(opt)
	publish(h.Publisher, event.NewToolBeforeEvent("create_task", "", meta, opt.Reasoning, opt.Id))

	var targets []TaskBaseInfo
	for _, target := range opt.TaskTarget {
		if target.Title == "" || target.Description == "" {
			continue
		}
		targets = append(targets, target)
	}

	if len(targets) == 0 {
		return nil, ResultFor(cmd, "", fmt.Errorf("task_target is required"))
	}

	result := ResultFor(cmd, "", nil)
	result.TaskTarget = targets
	return nil, result
}

// buildMeta returns the contextual description for a create_task operation.
func (h *CreateTaskHandler) buildMeta(opt *CreateTaskOption) string {
	if len(opt.TaskTarget) == 0 {
		return "create_task (0 sub-tasks)"
	}
	titles := make([]string, 0, len(opt.TaskTarget))
	for _, t := range opt.TaskTarget {
		titles = append(titles, t.Title)
	}
	return fmt.Sprintf("Create sub-tasks: %s", strings.Join(titles, ", "))
}
