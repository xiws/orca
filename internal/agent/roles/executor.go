package roles

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
)

// Executor 是执行 Agent。
// 它负责实际完成任务，调用工具（read/write/edit/bash）完成工作。
// Executor 本质上是现有 Runtime.execute() 循环的封装。
type Executor struct {
	Runtime *core.Runtime
}

// Execute 执行一个任务。
func (e *Executor) Execute(task *core.Task) (string, error) {
	if task.SessionInfo == nil {
		task.SessionInfo = core.NewSession()
	}

	err, result := e.Runtime.RunTask(task)
	if err != nil {
		return "", fmt.Errorf("executor: %w", err)
	}

	return result, nil
}

// FormatSpecification 将 Specification 格式化为可读文本。
func FormatSpecification(spec *core.Specification) string {
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
