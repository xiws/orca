package modes

import (
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/assets"
	"github.com/xiws/orca/internal/llm"
)

// ReviewMode 只读评审模式：只关注代码质量、潜在 bug、安全性、性能。
// 不修改任何文件，工具只给 read + bash（bash 仅运行验证命令）。
type ReviewMode struct {
	Runtime *core.Runtime
}

func (m *ReviewMode) Name() string { return "review" }

func (m *ReviewMode) Run(task *core.Task) (string, error) {
	// 注入评审系统提示词
	if task.SessionInfo == nil {
		task.SessionInfo = core.NewSession()
	}
	task.SessionInfo.Messages = append([]llm.ChatMessage{
		{Role: llm.RoleSystem, Content: assets.ReviewPrompt},
	}, task.SessionInfo.Messages...)

	task.SessionInfo.Provider.AllowedTools = []string{"read", "bash"}
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(task)
}
