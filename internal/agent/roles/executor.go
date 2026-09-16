package roles

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/llm"
)

// Executor 是执行 Agent。
// 它负责实际完成任务，调用工具（read/write/edit/bash）完成工作。
// Executor 本质上是现有 Runtime.execute() 循环的封装。
type Executor struct {
	Runtime *core.Runtime

	// Tools 限制 Executor 可使用的工具列表。
	// nil 表示使用全部可用工具。
	Tools []string
}

// Execute 执行一个任务。
//
// 它构建合适的 Task + Session 后调用 Runtime.RunTask。
// 如果 task 已有 Specification，会将其作为上下文注入。
func (e *Executor) Execute(task *core.Task) (string, error) {
	// 如果任务没有 Session，创建一个新的
	if task.SessionInfo == nil {
		task.SessionInfo = core.NewSession()
	}

	// 构建执行上下文
	context := e.buildContext(task)
	if context != "" {
		task.SessionInfo.AppendMessage(llm.RoleSystem, context)
	}

	// 调用 Runtime 执行
	err, result := e.Runtime.RunTask(task)
	if err != nil {
		return "", fmt.Errorf("executor: %w", err)
	}

	return result, nil
}

// buildContext 根据任务信息构建执行上下文。
func (e *Executor) buildContext(task *core.Task) string {
	var ctx string

	// 如果有 Specification，注入为上下文
	if task.Specification != nil {
		ctx += formatSpecification(task.Specification)
	}

	// 如果有原始输入但没有 Specification，使用原始输入
	if task.Input != "" && task.Specification == nil {
		ctx += "任务目标:\n" + task.Input + "\n"
	}

	return ctx
}

// formatSpecification 将 Specification 格式化为可读文本。
func formatSpecification(spec *core.Specification) string {
	if spec == nil {
		return ""
	}

	var result string
	result += "## 任务目标\n" + spec.Goal + "\n\n"

	if len(spec.Requirements) > 0 {
		result += "## 需求\n"
		for _, req := range spec.Requirements {
			result += fmt.Sprintf("- [%s] %s: %s\n", req.Priority, req.ID, req.Description)
		}
		result += "\n"
	}

	if len(spec.Constraints) > 0 {
		result += "## 约束\n"
		for _, c := range spec.Constraints {
			result += "- " + c + "\n"
		}
		result += "\n"
	}

	if len(spec.AcceptanceCriteria) > 0 {
		result += "## 验收标准\n"
		for _, c := range spec.AcceptanceCriteria {
			result += "- " + c + "\n"
		}
		result += "\n"
	}

	if len(spec.Assumptions) > 0 {
		result += "## 假设\n"
		for _, a := range spec.Assumptions {
			result += "- " + a + "\n"
		}
		result += "\n"
	}

	return result
}
