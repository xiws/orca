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

func (m *ReviewMode) Name() string { return "review" }

func (m *ReviewMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	// 仅在当前 Invocation 注入评审系统提示词。
	inv.Messages = append([]llm.ChatMessage{
		{Role: llm.RoleSystem, Content: assets.ReviewPrompt},
	}, inv.Messages...)

	roles.RestrictTools(inv, "read", "bash")
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(inv)
}
