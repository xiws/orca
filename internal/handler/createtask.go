package handler

import (
	"fmt"
	"github.com/xiws/orca/internal/event"
	"github.com/xiws/orca/pkg/command"
	"strings"
)

// CreateTaskOption 是 "createTask" 命令参数：携带用户希望 AI 分解为
// 子任务、独立运行每个子任务、然后聚合结果的高级目标。
type CreateTaskOption struct {
	Id         int64
	Reasoning  string
	TaskTarget []TaskBaseInfo `json:"task_target"`
}

// CreateTaskHandler 服务 create_task 命令，将高级目标分解为可独立执行的子任务。
type CreateTaskHandler struct {
	Workspace
}

// GetId 返回将结果与此调用关联的调用 id。
func (t CreateTaskOption) GetId() int64 { return t.Id }

// GetName 返回命令的路由名称。
func (CreateTaskOption) GetName() string { return CommandCreateTask }

// NewCreateTaskOption 构建 createTask 命令。
func NewCreateTaskOption(id int64, taskTarget []TaskBaseInfo) *CreateTaskOption {
	return &CreateTaskOption{Id: id, TaskTarget: taskTarget}
}

// Handle 解析 create_task 命令，过滤无效子任务后返回任务列表。
func (h *CreateTaskHandler) Handle(cmd command.CommandOption) (error, any) {
	opt, ok := cmd.(*CreateTaskOption)
	if !ok {
		return ErrUnsupportedOption, nil
	}

	meta := h.buildMeta(opt)
	publish(h.Publisher, event.NewToolBeforeEvent("create_task", "", meta, opt.Reasoning, opt.Id))

	// 过滤掉标题或描述为空的无效子任务。
	var targets []TaskBaseInfo
	for _, target := range opt.TaskTarget {
		if target.Title == "" || target.Description == "" {
			continue
		}
		targets = append(targets, target)
	}

	// 必须至少有一个有效子任务。
	if len(targets) == 0 {
		return nil, ResultFor(cmd, "", fmt.Errorf("task_target is required"))
	}

	result := ResultFor(cmd, "", nil)
	result.TaskTarget = targets
	return nil, result
}

// buildMeta 返回 create_task 操作的上下文描述。
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
