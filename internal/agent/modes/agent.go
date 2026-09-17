package modes

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
)

// AgentMode 自主执行模式：完整闭环，Clarifier → Planner → Executor 线性链。
// 简化版实现，不走 workflow 状态机。
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

	// 2. EXECUTE：Executor 全工具执行任务
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(task)
}
