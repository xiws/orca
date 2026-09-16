package roles

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/llm"
)

// Repair 是修复 Agent。
// 它根据 Verifier 的失败原因生成修复方案，然后交给 Executor 执行。
type Repair struct {
	Runtime *core.Runtime
}

// RepairResult 是 Repair 的输出。
type RepairResult struct {
	// FixPlan 修复计划描述。
	FixPlan string `json:"fix_plan"`

	// FilesToModify 需要修改的文件列表。
	FilesToModify []string `json:"files_to_modify,omitempty"`
}

// Repair 根据失败原因生成修复方案。
//
// 输入：原始 Specification + VerifyResult（失败原因）。
// 输出：修复指令，交给 Executor 执行。
func (r *Repair) Repair(task *core.Task, verifyResult *VerifyResult) (*RepairResult, error) {
	repairTask := core.NewTask("generate repair plan", "repair")
	repairTask.SessionInfo.Messages = []llm.ChatMessage{
		{Role: llm.RoleSystem, Content: repairSystemPrompt},
		{Role: llm.RoleUser, Content: r.buildRepairInput(task, verifyResult)},
	}

	err, result := r.Runtime.RunTask(repairTask)
	if err != nil {
		return nil, fmt.Errorf("repair: %w", err)
	}

	return parseRepairResult(result)
}

// buildRepairInput 构建 Repair 的输入。
func (r *Repair) buildRepairInput(task *core.Task, verifyResult *VerifyResult) string {
	var input string

	// 原始需求
	if task.Input != "" {
		input += "## 原始需求\n" + task.Input + "\n\n"
	}

	// 验收标准
	if task.Specification != nil && len(task.Specification.AcceptanceCriteria) > 0 {
		input += "## 验收标准\n"
		for _, c := range task.Specification.AcceptanceCriteria {
			input += "- " + c + "\n"
		}
		input += "\n"
	}

	// 验证失败原因
	input += "## 验证失败原因\n"
	if verifyResult.Summary != "" {
		input += verifyResult.Summary + "\n\n"
	}

	if len(verifyResult.Issues) > 0 {
		input += "### 发现的问题\n"
		for _, issue := range verifyResult.Issues {
			input += fmt.Sprintf("- [%s] %s\n", issue.Severity, issue.Description)
		}
		input += "\n"
	}

	input += "请生成修复方案，说明需要修改哪些文件、如何修改。\n"
	input += "修复方案将被交给 Executor 执行。\n"

	return input
}

// parseRepairResult 从 LLM 输出中解析 RepairResult。
// 当前简化实现：直接将输出作为 FixPlan。
func parseRepairResult(output string) (*RepairResult, error) {
	// TODO: 实现更精确的 JSON 解析
	return &RepairResult{
		FixPlan: output,
	}, nil
}
