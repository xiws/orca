package modes

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
)

// AgentMode 自主执行模式：Clarifier → Executor 线性链。
type AgentMode struct {
	Runtime *core.Runtime
}

func (m *AgentMode) Name() string { return "agent" }

func (m *AgentMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	// 1. CLARIFYING：Clarifier 分析需求，产出 Specification。
	clarifier := &roles.Clarifier{Runtime: m.Runtime}
	result, err := clarifier.Clarify(inv, task.Input)
	if err != nil {
		return "", fmt.Errorf("agent mode clarify: %w", err)
	}
	task.Specification = result.Specification

	// 将 Specification 注入当前执行上下文，不改写任务原始需求。
	if task.Specification != nil {
		inv.AppendMessage(llm.RoleUser, roles.FormatSpecification(task.Specification))
	}

	// 2. EXECUTE：Executor 在调用方权限内执行任务。
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(inv)
}
