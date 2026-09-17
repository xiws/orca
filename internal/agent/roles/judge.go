package roles

import (
	"fmt"
	"strings"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/llm"
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
func (j *Judge) Synthesize(input, answer string, opinions []string) (string, error) {
	task := core.NewTask("synthesize final answer", "judge")
	task.SessionInfo.Messages = []llm.ChatMessage{
		{Role: llm.RoleSystem, Content: judgeSystemPrompt},
		{Role: llm.RoleUser, Content: formatJudgeInput(input, answer, opinions)},
	}

	err, result := j.Runtime.RunTask(task)
	if err != nil {
		return "", fmt.Errorf("judge: %w", err)
	}

	return result, nil
}

// formatJudgeInput 将原始问题、初始回答和各方点评格式化为 Judge 的输入。
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
