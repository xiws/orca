package roles

import (
	"testing"

	"github.com/xiws/orca/internal/domain"
)

func TestDefaultWorkflowPlan(t *testing.T) {
	plan := DefaultWorkflowPlan()
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	if len(plan.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(plan.Nodes))
	}
	if len(plan.Edges) != 3 {
		t.Errorf("expected 3 edges, got %d", len(plan.Edges))
	}

	// 检查节点
	if plan.Nodes[0].ID != "execute" {
		t.Errorf("expected first node 'execute', got %s", plan.Nodes[0].ID)
	}
	if plan.Nodes[0].Type != "execute" {
		t.Errorf("expected type 'execute', got %s", plan.Nodes[0].Type)
	}
	if plan.Nodes[1].ID != "verify" {
		t.Errorf("expected second node 'verify', got %s", plan.Nodes[1].ID)
	}
	if plan.Nodes[2].ID != "done" {
		t.Errorf("expected third node 'done', got %s", plan.Nodes[2].ID)
	}
}

func TestWorkflowPlan_Structure(t *testing.T) {
	plan := &WorkflowPlan{
		Nodes: []PlanNode{
			{ID: "step1", Type: "execute", Description: "第一步", Tools: []string{"read"}},
			{ID: "step2", Type: "verify", Description: "验证"},
		},
		Edges: []PlanEdge{
			{From: "step1", To: "step2", Action: "complete"},
		},
	}

	if len(plan.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(plan.Nodes))
	}
	if plan.Nodes[0].Tools[0] != "read" {
		t.Errorf("expected tool 'read', got %s", plan.Nodes[0].Tools[0])
	}
}

func TestFormatSpecificationForPlanner_Nil(t *testing.T) {
	result := formatSpecificationForPlanner(nil)
	if result != "请为任务创建执行计划。" {
		t.Errorf("unexpected result for nil spec: %s", result)
	}
}

func TestFormatSpecificationForPlanner_Full(t *testing.T) {
	spec := &domain.Specification{
		Goal: "实现功能",
		Requirements: []domain.Requirement{
			{ID: "R1", Description: "需求1", Priority: "high"},
		},
		Constraints:        []string{"约束1"},
		AcceptanceCriteria: []string{"标准1"},
	}

	result := formatSpecificationForPlanner(spec)
	if result == "" {
		t.Error("expected non-empty result")
	}
	if !containsStr(result, "实现功能") {
		t.Error("expected goal in result")
	}
	if !containsStr(result, "需求1") {
		t.Error("expected requirement in result")
	}
	if !containsStr(result, "约束1") {
		t.Error("expected constraint in result")
	}
	if !containsStr(result, "标准1") {
		t.Error("expected acceptance criteria in result")
	}
}

func TestParseWorkflowPlan(t *testing.T) {
	// 当前简化实现返回默认 plan
	plan, err := parseWorkflowPlan("some output")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}
	// 应该返回默认 workflow
	if len(plan.Nodes) == 0 {
		t.Error("expected at least one node in default plan")
	}
}

func TestPlanNode_Types(t *testing.T) {
	types := []string{"execute", "verify", "repair", "human"}
	for _, nodeType := range types {
		node := PlanNode{ID: "test", Type: nodeType}
		if node.Type != nodeType {
			t.Errorf("expected type %s, got %s", nodeType, node.Type)
		}
	}
}

func TestPlanEdge_Structure(t *testing.T) {
	edge := PlanEdge{
		From:   "execute",
		To:     "verify",
		Action: "complete",
	}
	if edge.From != "execute" {
		t.Errorf("expected from 'execute', got %s", edge.From)
	}
	if edge.To != "verify" {
		t.Errorf("expected to 'verify', got %s", edge.To)
	}
	if edge.Action != "complete" {
		t.Errorf("expected action 'complete', got %s", edge.Action)
	}
}
