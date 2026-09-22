package modes

import (
	"fmt"
	"strings"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/assets"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
)

// DeliberateMode 多角色辩论模式：Responder 首答 → 多个 Critic 点评 → Judge 汇总。
// fan-out/fan-in 定制编排，不进状态机。
type DeliberateMode struct {
	Runtime *core.Runtime
}

func (m *DeliberateMode) Name() string { return "deliberate" }

func (m *DeliberateMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	// 1. Responder：生成初始回答。
	answer, err := m.respond(inv)
	if err != nil {
		return "", fmt.Errorf("deliberate respond: %w", err)
	}

	// 2. Critics：多视角点评（含 Devil's Advocate）。
	opinions := m.critics(inv, task.Input, answer)

	// 3. Judge：汇总所有观点，输出最终答案。
	judge := &roles.Judge{Runtime: m.Runtime}
	return judge.Synthesize(inv, task.Input, answer, opinions)
}

// respond 使用 Executor 生成初始回答，仅使用调用方已允许的 read 工具。
func (m *DeliberateMode) respond(inv *core.Invocation) (string, error) {
	roles.RestrictTools(inv, "read")
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(inv)
}

// critics 从多个视角对初始回答进行点评。
// 返回各 Critic 的点评文本列表。
func (m *DeliberateMode) critics(parentInv *core.Invocation, input, answer string) []string {
	// 从嵌入的 prompt 文件中解析各视角。
	sections := strings.Split(assets.DeliberatePrompt, "\n---\n")
	prompts := []struct {
		name   string
		prompt string
	}{
		{name: "架构视角", prompt: strings.TrimSpace(sections[0])},
		{name: "边界与正确性视角", prompt: strings.TrimSpace(sections[1])},
		{name: "Devil's Advocate（魔鬼代言人）", prompt: strings.TrimSpace(sections[2])},
	}

	var opinions []string
	for _, p := range prompts {
		opinion, err := m.runCritic(parentInv, input, answer, p.prompt)
		if err != nil {
			opinions = append(opinions, fmt.Sprintf("[%s] 点评失败: %v", p.name, err))
			continue
		}
		opinions = append(opinions, fmt.Sprintf("[%s]\n%s", p.name, opinion))
	}
	return opinions
}

// runCritic 在同一任务的子 Invocation 中独立点评，继承模型和工作区。
func (m *DeliberateMode) runCritic(parentInv *core.Invocation, input, answer, criticPrompt string) (string, error) {
	return m.Runtime.Run(newCriticInvocation(parentInv, input, answer, criticPrompt))
}

func newCriticInvocation(parentInv *core.Invocation, input, answer, criticPrompt string) *core.Invocation {
	inv := parentInv.NewChild()
	roles.RestrictTools(inv, "read")
	inv.AppendMessage(llm.RoleSystem, criticPrompt)
	inv.AppendMessage(llm.RoleUser, fmt.Sprintf("原始问题：%s\n\n待点评的回答：\n%s", input, answer))
	return inv
}
