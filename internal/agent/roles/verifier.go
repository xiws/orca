package roles

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
)

// Verifier 是验证 Agent。
// 它判断任务是否真正完成，而不是仅凭 Executor 的"我完成了"。
type Verifier struct {
	Runtime *core.Runtime
}

// VerifyResult 是 Verifier 的输出。
type VerifyResult struct {
	// Passed 表示验证是否通过。
	Passed bool `json:"passed"`

	// Issues 发现的问题列表（Passed=false 时非空）。
	Issues []Issue `json:"issues,omitempty"`

	// Summary 验证结果的总结。
	Summary string `json:"summary"`
}

// Issue 描述验证中发现的一个问题。
type Issue struct {
	// Description 问题描述。
	Description string `json:"description"`

	// Severity 严重程度：critical / warning / info。
	Severity string `json:"severity"`
}

// Verify 判断任务是否真正完成。
//
// Verifier 是只读 Agent：只给 read + bash 工具（运行测试）。
// 输入：Specification（验收标准）+ Executor 的执行记录。
func (v *Verifier) Verify(task *core.Task) (*VerifyResult, error) {
	userInput := v.buildVerifyInput(task)
	output, err := runRole(v.Runtime, verifierSystemPrompt, userInput)
	if err != nil {
		return nil, fmt.Errorf("verifier: %w", err)
	}

	return parseVerifyResult(output)
}

// buildVerifyInput 构建 Verifier 的输入。
func (v *Verifier) buildVerifyInput(task *core.Task) string {
	var input string

	// 验收标准
	if task.Specification != nil && len(task.Specification.AcceptanceCriteria) > 0 {
		input += "## 验收标准\n"
		for _, c := range task.Specification.AcceptanceCriteria {
			input += "- " + c + "\n"
		}
		input += "\n"
	}

	// 任务目标
	if task.Input != "" {
		input += "## 原始需求\n" + task.Input + "\n\n"
	}

	// Executor 的执行结果
	if task.TaskResult != "" {
		input += "## Executor 的执行结果\n" + task.TaskResult + "\n\n"
	}

	input += "请验证上述任务是否真正完成。检查：\n"
	input += "1. 代码是否编译通过？\n"
	input += "2. 测试是否通过？\n"
	input += "3. 需求是否满足？\n"
	input += "4. 是否有遗漏的问题？\n"

	return input
}

// parseVerifyResult 从 LLM 输出中解析 VerifyResult。
// 当前简化实现：检查输出中是否包含 "PASS" 或 "FAIL" 关键字。
func parseVerifyResult(output string) (*VerifyResult, error) {
	// TODO: 实现更精确的 JSON 解析
	result := &VerifyResult{
		Summary: output,
	}

	// 简单关键字检测
	if containsKeyword(output, "PASS", "通过", "成功", "完成") {
		result.Passed = true
	} else if containsKeyword(output, "FAIL", "失败", "错误", "问题") {
		result.Passed = false
		result.Issues = []Issue{
			{Description: output, Severity: "critical"},
		}
	} else {
		// 默认视为通过
		result.Passed = true
	}

	return result, nil
}

// containsKeyword 检查文本中是否包含任一关键字。
func containsKeyword(text string, keywords ...string) bool {
	for _, kw := range keywords {
		for i := 0; i <= len(text)-len(kw); i++ {
			if text[i:i+len(kw)] == kw {
				return true
			}
		}
	}
	return false
}
