package roles

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/domain"
)

// Executor 是执行 Agent。
// 它负责实际完成任务，在调用方的工具权限内完成工作。
// Executor 本质上是 Runtime.Run 的封装。
type Executor struct {
	Runtime *core.Runtime
}

// Execute 执行调用方提供的 Invocation，不创建 Task 或 Session。
func (e *Executor) Execute(inv *core.Invocation) (string, error) {
	return e.Runtime.Run(inv)
}

// FormatSpecification 将 Specification 格式化为可读文本。
func FormatSpecification(spec *domain.Specification) string {
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
