package modes

import (
	"fmt"
	"strings"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/assets"
	"github.com/xiws/orca/internal/llm"
)

// DeliberateMode 多角色辩论模式：Responder 首答 → 多个 Critic 点评 → Judge 汇总。
// fan-out/fan-in 定制编排，不进状态机。
type DeliberateMode struct {
	Runtime *core.Runtime
}

func (m *DeliberateMode) Name() string { return "deliberate" }

func (m *DeliberateMode) Run(task *core.Task) (string, error) {
	// 1. Responder：生成初始回答
	answer, err := m.respond(task)
	if err != nil {
		return "", fmt.Errorf("deliberate respond: %w", err)
	}

	// 2. Critics：多视角点评（含 Devil's Advocate）
	opinions := m.critics(task, answer)

	// 3. Judge：汇总所有观点，输出最终答案
	judge := &roles.Judge{Runtime: m.Runtime}
	return judge.Synthesize(task.Input, answer, opinions)
}

// respond 使用 Executor 生成初始回答（只读，工具只给 read）。
func (m *DeliberateMode) respond(task *core.Task) (string, error) {
	task.SessionInfo.Provider.AllowedTools = []string{"read"}
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(task)
}

// critics 从多个视角对初始回答进行点评。
// 返回各 Critic 的点评文本列表。
func (m *DeliberateMode) critics(task *core.Task, answer string) []string {
	// 从嵌入的 prompt 文件中解析各视角
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
		opinion, err := m.runCritic(task, answer, p.prompt)
		if err != nil {
			opinions = append(opinions, fmt.Sprintf("[%s] 点评失败: %v", p.name, err))
			continue
		}
		opinions = append(opinions, fmt.Sprintf("[%s]\n%s", p.name, opinion))
	}
	return opinions
}

// runCritic 以指定视角提示词运行一次独立的 LLM 对话，返回点评文本。
func (m *DeliberateMode) runCritic(task *core.Task, answer, criticPrompt string) (string, error) {
	criticTask := core.NewTask(task.Input, "critic")
	criticTask.SessionInfo = core.NewSession()
	criticTask.SessionInfo.Provider = task.SessionInfo.Provider
	criticTask.SessionInfo.Messages = []llm.ChatMessage{
		{Role: llm.RoleSystem, Content: criticPrompt},
		{Role: llm.RoleUser, Content: fmt.Sprintf("原始问题：%s\n\n待点评的回答：\n%s", task.Input, answer)},
	}

	err, result := m.Runtime.RunTask(criticTask)
	if err != nil {
		return "", err
	}
	return result, nil
}
