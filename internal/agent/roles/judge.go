package roles

import (
	"fmt"
	"strings"

	"github.com/xiws/orca/internal/agent/core"
)

// Judge 是汇总判断 Agent。
// 它汇总初始回答和多方点评，输出更全面的最终答案。
// Deliberate 模式依赖此角色。
type Judge struct {
	Runtime *core.Runtime
}

// Synthesize 汇总首答和各方点评，输出最终答案。
//
// input: 原始问题
// answer: Responder 的初始回答
// opinions: 各 Critic 的点评列表
// Judge 仅使用调用方已允许的 read 工具。
func (j *Judge) Synthesize(parentInv *core.Invocation, input, answer string, opinions []string) (string, error) {
	// 将所有输入格式化为 Judge 可理解的文本
	formattedInput := formatJudgeInput(input, answer, opinions)
	// 在子 Invocation 中执行 LLM 对话，限制工具为 read
	result, err := runRole(j.Runtime, parentInv, judgeSystemPrompt, formattedInput, "read")
	if err != nil {
		return "", fmt.Errorf("judge: %w", err)
	}

	return result, nil
}

// formatJudgeInput 将原始问题、初始回答和各方点评格式化为 Judge 的输入。
// 结构：原始问题 → 初始回答 → 各点评 → 汇总指令。
func formatJudgeInput(input, answer string, opinions []string) string {
	var b strings.Builder

	b.WriteString("## 原始问题\n")
	b.WriteString(input)
	b.WriteString("\n\n## 初始回答\n")
	b.WriteString(answer)
	b.WriteString("\n")

	for i, opinion := range opinions {
		fmt.Fprintf(&b, "\n## 点评 %d\n%s\n", i+1, opinion)
	}

	b.WriteString("\n请综合以上所有观点，给出一个更全面、更平衡的最终答案。\n")
	return b.String()
}
