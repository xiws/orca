package modes

import (
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/assets"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
)

// ReviewMode 代码评审模式：关注代码质量、潜在 bug、安全性、性能。
// 保留 read + bash 验证工具语义；bash 并非只读工具，权限不得超过调用方。
type ReviewMode struct {
	Runtime *core.Runtime
}

// Name 返回模式名称。
func (m *ReviewMode) Name() string { return "review" }

// Run 评审模式执行：注入评审系统提示词，限制工具为 read + bash，然后执行。
func (m *ReviewMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	// 在消息历史最前面插入评审系统提示词
	inv.Messages = append([]llm.ChatMessage{
		{Role: llm.RoleSystem, Content: assets.ReviewPrompt},
	}, inv.Messages...)

	// 限制工具为 read 和 bash（bash 用于运行验证命令）
	roles.RestrictTools(inv, "read", "bash")
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(inv)
}
