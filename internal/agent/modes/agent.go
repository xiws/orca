package modes

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/llm"
)

// AgentMode 自主执行模式：完整闭环，Clarifier → Executor 线性链。
type AgentMode struct {
	Runtime *core.Runtime
}

func (m *AgentMode) Name() string { return "agent" }

func (m *AgentMode) Run(task *core.Task) (string, error) {
	// 1. CLARIFYING：Clarifier 分析需求，产出 Specification
	clarifier := &roles.Clarifier{Runtime: m.Runtime}
	result, err := clarifier.Clarify(task.Input)
	if err != nil {
		return "", fmt.Errorf("agent mode clarify: %w", err)
	}
	task.Specification = result.Specification

	// 将 Specification 注入为上下文，供 Executor 的 LLM 使用
	if task.Specification != nil {
		task.SessionInfo.AppendMessage(llm.RoleUser, roles.FormatSpecification(task.Specification))
	}

	// 2. EXECUTE：Executor 全工具执行任务
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(task)
}
