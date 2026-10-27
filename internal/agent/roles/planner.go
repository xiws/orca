package roles

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/domain"
)

// Planner 是规划 Agent。
// 它将 Specification 转换为 WorkflowPlan，定义执行步骤和节点。
type Planner struct {
	Runtime *core.Runtime
}

// WorkflowPlan 描述一个工作流执行计划。
type WorkflowPlan struct {
	// Nodes 计划中的节点列表。
	Nodes []PlanNode `json:"nodes"`

	// Edges 节点之间的连接。
	Edges []PlanEdge `json:"edges"`
}

// PlanNode 描述工作流中的一个节点。
type PlanNode struct {
	// ID 节点唯一标识。
	ID string `json:"id"`

	// Type 节点类型：execute / verify / repair / human。
	Type string `json:"type"`

	// Description 节点描述。
	Description string `json:"description"`

	// Tools 该节点可用的工具列表。
	Tools []string `json:"tools,omitempty"`
}

// PlanEdge 描述节点之间的连接。
type PlanEdge struct {
	// From 起始节点 ID。
	From string `json:"from"`

	// To 目标节点 ID。
	To string `json:"to"`

	// Action 触发动作。
	Action string `json:"action"`
}

// Plan 将 Specification 转换为 WorkflowPlan。
//
// Planner 只使用调用方已允许的 read 工具读取项目上下文。
// 通过子 Invocation 执行独立的 LLM 对话。
func (p *Planner) Plan(parentInv *core.Invocation, spec *domain.Specification) (*WorkflowPlan, error) {
	// 将 Specification 格式化为 Planner 可理解的输入
	userInput := formatSpecificationForPlanner(spec)
	// 在子 Invocation 中执行 LLM 对话，限制工具为 read
	output, err := runRole(p.Runtime, parentInv, plannerSystemPrompt, userInput, "read")
	if err != nil {
		return nil, fmt.Errorf("planner: %w", err)
	}

	return parseWorkflowPlan(output)
}

// formatSpecificationForPlanner 将 Specification 格式化为 Planner 的输入。
func formatSpecificationForPlanner(spec *domain.Specification) string {
	if spec == nil {
		return "请为任务创建执行计划。"
	}

	var input string
	input += "## 任务目标\n" + spec.Goal + "\n\n"

	if len(spec.Requirements) > 0 {
		input += "## 需求\n"
		for _, req := range spec.Requirements {
			input += fmt.Sprintf("- [%s] %s: %s\n", req.Priority, req.ID, req.Description)
		}
		input += "\n"
	}

	if len(spec.Constraints) > 0 {
		input += "## 约束\n"
		for _, c := range spec.Constraints {
			input += "- " + c + "\n"
		}
		input += "\n"
	}

	if len(spec.AcceptanceCriteria) > 0 {
		input += "## 验收标准\n"
		for _, c := range spec.AcceptanceCriteria {
			input += "- " + c + "\n"
		}
		input += "\n"
	}

	input += "请根据以上信息创建执行计划。\n"
	return input
}

// parseWorkflowPlan 从 LLM 输出中解析 WorkflowPlan。
// 当前简化实现：生成默认的 workflow 模板。
func parseWorkflowPlan(output string) (*WorkflowPlan, error) {
	// TODO: 实现 JSON 解析逻辑
	// 当前返回默认 workflow 模板
	return DefaultWorkflowPlan(), nil
}

// DefaultWorkflowPlan 返回默认的工作流计划。
// 适用于简单任务，无需 Planner 拆分。
//
// 流程：execute → verify → done（成功）
//
//	   ↓ fail
//	execute（重试）
func DefaultWorkflowPlan() *WorkflowPlan {
	return &WorkflowPlan{
		Nodes: []PlanNode{
			{
				ID:          "execute",
				Type:        "execute",
				Description: "执行任务",
				Tools:       []string{"read", "write", "edit", "bash"},
			},
			{
				ID:          "verify",
				Type:        "verify",
				Description: "验证结果",
				Tools:       []string{"read", "bash"},
			},
			{
				ID:          "done",
				Type:        "execute",
				Description: "任务完成",
			},
		},
		Edges: []PlanEdge{
			{From: "execute", To: "verify", Action: "complete"},
			{From: "verify", To: "done", Action: "complete"}, // 验证通过 → 完成
			{From: "verify", To: "execute", Action: "fail"},  // 验证失败 → 重试
		},
	}
}
