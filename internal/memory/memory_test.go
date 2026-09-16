package memory

import (
	"testing"
)

func TestNewMemoryService(t *testing.T) {
	svc := NewMemoryService()
	if svc == nil {
		t.Fatal("expected non-nil MemoryService")
	}
	if svc.Session() == nil {
		t.Error("expected non-nil SessionMemory")
	}
	if svc.Workflow() == nil {
		t.Error("expected non-nil WorkflowMemory")
	}
	if svc.Project() == nil {
		t.Error("expected non-nil ProjectMemory")
	}
}

func TestMemoryService_Reset(t *testing.T) {
	svc := NewMemoryService()
	svc.Session().SetVariable("key", "value")
	svc.Workflow().SetInstance("inst1", "node1")

	svc.Reset()

	// Session 和 Workflow 应该被重置
	if _, ok := svc.Session().GetVariable("key"); ok {
		t.Error("expected session to be reset")
	}
	if svc.Workflow().InstanceID != "" {
		t.Error("expected workflow to be reset")
	}
}

func TestSessionMemory_ToolResult(t *testing.T) {
	mem := NewSessionMemory()

	mem.SetToolResult("call1", "result1")
	mem.SetToolResult("call2", "result2")

	result, ok := mem.GetToolResult("call1")
	if !ok || result != "result1" {
		t.Errorf("expected 'result1', got %s", result)
	}

	_, ok = mem.GetToolResult("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent call")
	}
}

func TestSessionMemory_Variable(t *testing.T) {
	mem := NewSessionMemory()

	mem.SetVariable("count", 42)
	mem.SetVariable("name", "test")

	val, ok := mem.GetVariable("count")
	if !ok || val.(int) != 42 {
		t.Errorf("expected 42, got %v", val)
	}

	val, ok = mem.GetVariable("name")
	if !ok || val.(string) != "test" {
		t.Errorf("expected 'test', got %v", val)
	}

	_, ok = mem.GetVariable("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent variable")
	}
}

func TestSessionMemory_Clear(t *testing.T) {
	mem := NewSessionMemory()
	mem.SetToolResult("call1", "result")
	mem.SetVariable("key", "value")

	mem.Clear()

	if _, ok := mem.GetToolResult("call1"); ok {
		t.Error("expected tool results to be cleared")
	}
	if _, ok := mem.GetVariable("key"); ok {
		t.Error("expected variables to be cleared")
	}
}

func TestWorkflowMemory_Instance(t *testing.T) {
	mem := NewWorkflowMemory()
	mem.SetInstance("inst-123", "start")

	if mem.InstanceID != "inst-123" {
		t.Errorf("expected instance ID 'inst-123', got %s", mem.InstanceID)
	}
	if mem.CurrentNode != "start" {
		t.Errorf("expected current node 'start', got %s", mem.CurrentNode)
	}
	path := mem.GetPath()
	if len(path) != 1 || path[0] != "start" {
		t.Errorf("expected path ['start'], got %v", path)
	}
}

func TestWorkflowMemory_RecordVisit(t *testing.T) {
	mem := NewWorkflowMemory()
	mem.SetInstance("inst1", "node1")

	mem.RecordVisit("node2")
	mem.RecordVisit("node3")

	if mem.CurrentNode != "node3" {
		t.Errorf("expected current node 'node3', got %s", mem.CurrentNode)
	}

	path := mem.GetPath()
	if len(path) != 3 {
		t.Errorf("expected path length 3, got %d", len(path))
	}
}

func TestWorkflowMemory_Retry(t *testing.T) {
	mem := NewWorkflowMemory()

	count := mem.IncrementRetry()
	if count != 1 {
		t.Errorf("expected retry count 1, got %d", count)
	}

	count = mem.IncrementRetry()
	if count != 2 {
		t.Errorf("expected retry count 2, got %d", count)
	}

	mem.ResetRetry()
	if mem.RetryCount != 0 {
		t.Errorf("expected retry count 0 after reset, got %d", mem.RetryCount)
	}
}

func TestWorkflowMemory_Decision(t *testing.T) {
	mem := NewWorkflowMemory()

	mem.RecordDecision("node1", "go to node2")
	decision, ok := mem.GetDecision("node1")
	if !ok || decision != "go to node2" {
		t.Errorf("expected 'go to node2', got %s", decision)
	}

	_, ok = mem.GetDecision("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent decision")
	}
}

func TestWorkflowMemory_NodeResult(t *testing.T) {
	mem := NewWorkflowMemory()

	mem.SetNodeResult("execute", "completed successfully")
	result, ok := mem.GetNodeResult("execute")
	if !ok || result.(string) != "completed successfully" {
		t.Errorf("expected 'completed successfully', got %v", result)
	}

	_, ok = mem.GetNodeResult("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent node")
	}
}

func TestWorkflowMemory_Reset(t *testing.T) {
	mem := NewWorkflowMemory()
	mem.SetInstance("inst1", "node1")
	mem.RecordVisit("node2")
	mem.IncrementRetry()

	mem.Reset()

	if mem.InstanceID != "" {
		t.Error("expected instance ID to be reset")
	}
	if len(mem.History) != 0 {
		t.Error("expected history to be reset")
	}
	if mem.RetryCount != 0 {
		t.Error("expected retry count to be reset")
	}
}

func TestProjectMemory_Basic(t *testing.T) {
	mem := NewProjectMemory()

	mem.SetProjectPath("/path/to/project")
	if mem.GetProjectPath() != "/path/to/project" {
		t.Errorf("expected '/path/to/project', got %s", mem.GetProjectPath())
	}

	mem.SetArchitecture("Go + React")
	if mem.GetArchitecture() != "Go + React" {
		t.Errorf("expected 'Go + React', got %s", mem.GetArchitecture())
	}
}

func TestProjectMemory_Conventions(t *testing.T) {
	mem := NewProjectMemory()

	mem.AddConvention("使用 gofmt 格式化代码")
	mem.AddConvention("错误处理不使用 panic")

	conventions := mem.GetConventions()
	if len(conventions) != 2 {
		t.Errorf("expected 2 conventions, got %d", len(conventions))
	}
}

func TestProjectMemory_Constraints(t *testing.T) {
	mem := NewProjectMemory()

	mem.AddConstraint("不修改 pkg/event")
	mem.AddConstraint("保持向后兼容")

	constraints := mem.GetConstraints()
	if len(constraints) != 2 {
		t.Errorf("expected 2 constraints, got %d", len(constraints))
	}
}

func TestProjectMemory_Knowledge(t *testing.T) {
	mem := NewProjectMemory()

	mem.SetKnowledge("db_type", "PostgreSQL")
	mem.SetKnowledge("cache", "Redis")

	val, ok := mem.GetKnowledge("db_type")
	if !ok || val != "PostgreSQL" {
		t.Errorf("expected 'PostgreSQL', got %s", val)
	}

	_, ok = mem.GetKnowledge("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent knowledge")
	}
}

func TestProjectMemory_Clear(t *testing.T) {
	mem := NewProjectMemory()
	mem.SetProjectPath("/path")
	mem.SetArchitecture("arch")
	mem.AddConvention("conv")
	mem.SetKnowledge("key", "value")

	mem.Clear()

	if mem.GetProjectPath() != "" {
		t.Error("expected project path to be cleared")
	}
	if mem.GetArchitecture() != "" {
		t.Error("expected architecture to be cleared")
	}
	if len(mem.GetConventions()) != 0 {
		t.Error("expected conventions to be cleared")
	}
	if _, ok := mem.GetKnowledge("key"); ok {
		t.Error("expected knowledge to be cleared")
	}
}
