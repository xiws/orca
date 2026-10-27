package modes

import (
	"fmt"
	"strings"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/domain"
)

// PlanMode 计划模式：分析需求 → 制定计划，但暂不执行。
// 流程在 PLANNING 截止，不进入 RUNNING。
type PlanMode struct {
	Runtime *core.Runtime
}

// Name 返回模式名称。
func (m *PlanMode) Name() string { return "plan" }

// Run 计划模式执行流程：
// 1. Clarifier 分析需求，产出 Specification
// 2. Planner 将 Specification 转换为 WorkflowPlan
// 流程在 PLANNING 截止，不进入 RUNNING。
func (m *PlanMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	// 第一步：Clarifier 输出 Specification（question 时与用户往返）
	clarifier := &roles.Clarifier{Runtime: m.Runtime}
	result, err := clarifier.Clarify(inv, task.Input)
	if err != nil {
		return "", fmt.Errorf("plan mode clarify: %w", err)
	}
	task.Specification = result.Specification

	// 第二步：Planner 将 Specification 转换为 WorkflowPlan
	planner := &roles.Planner{Runtime: m.Runtime}
	plan, err := planner.Plan(inv, task.Specification)
	if err != nil {
		return "", fmt.Errorf("plan mode plan: %w", err)
	}

	return formatPlan(plan), nil
}

// formatPlan 将 WorkflowPlan 格式化为可读文本。
func formatPlan(plan *roles.WorkflowPlan) string {
	if plan == nil {
		return "（无执行计划）"
	}

	var b strings.Builder
	b.WriteString("## 执行计划\n\n")

	for i, node := range plan.Nodes {
		fmt.Fprintf(&b, "### 节点 %d: %s (%s)\n", i+1, node.ID, node.Type)
		if node.Description != "" {
			b.WriteString(node.Description + "\n")
		}
		if len(node.Tools) > 0 {
			fmt.Fprintf(&b, "工具: %s\n", strings.Join(node.Tools, ", "))
		}
		b.WriteString("\n")
	}

	if len(plan.Edges) > 0 {
		b.WriteString("### 流转\n")
		for _, edge := range plan.Edges {
			fmt.Fprintf(&b, "- %s --[%s]--> %s\n", edge.From, edge.Action, edge.To)
		}
	}

	return b.String()
}
