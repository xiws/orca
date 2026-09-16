package workflow

import (
	"testing"
	"time"
)

func TestNewHumanNode(t *testing.T) {
	node := NewHumanNode("approval1", "确认删除？", 30*time.Second)

	if node.ID != "approval1" {
		t.Errorf("expected ID 'approval1', got %s", node.ID)
	}
	if node.Message != "确认删除？" {
		t.Errorf("expected message '确认删除？', got %s", node.Message)
	}
	if node.Timeout != 30*time.Second {
		t.Errorf("expected timeout 30s, got %v", node.Timeout)
	}
	if node.Response == nil {
		t.Error("expected non-nil Response channel")
	}
}

func TestHumanNode_Approve(t *testing.T) {
	node := NewHumanNode("test", "test message", 0)

	// 异步批准
	go func() {
		time.Sleep(10 * time.Millisecond)
		node.Approve()
	}()

	approved, err := node.Wait()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approved {
		t.Error("expected approved = true")
	}
}

func TestHumanNode_Reject(t *testing.T) {
	node := NewHumanNode("test", "test message", 0)

	// 异步拒绝
	go func() {
		time.Sleep(10 * time.Millisecond)
		node.Reject()
	}()

	approved, err := node.Wait()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if approved {
		t.Error("expected approved = false")
	}
}

func TestHumanNode_Timeout(t *testing.T) {
	node := NewHumanNode("test", "test message", 50*time.Millisecond)

	// 不发送任何响应，应该超时
	approved, err := node.Wait()
	if err == nil {
		t.Error("expected timeout error")
	}
	if approved {
		t.Error("expected approved = false on timeout")
	}
}

func TestHumanNode_NoTimeout(t *testing.T) {
	node := NewHumanNode("test", "test message", 0)

	// 异步批准
	go func() {
		time.Sleep(10 * time.Millisecond)
		node.Approve()
	}()

	// 应该正常等待并返回
	approved, err := node.Wait()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approved {
		t.Error("expected approved = true")
	}
}

func TestHumanApprovalEvent(t *testing.T) {
	node := NewHumanNode("approval1", "确认操作", 0)
	event := HumanApprovalEvent{
		NodeID:  node.ID,
		Message: node.Message,
		Node:    node,
	}

	if event.GetName() != "human_approval_request" {
		t.Errorf("expected event name 'human_approval_request', got %s", event.GetName())
	}
	if event.GetId() == 0 {
		t.Error("expected non-zero event ID")
	}
}

func TestHumanNode_MultipleApproveCalls(t *testing.T) {
	node := NewHumanNode("test", "test", 0)

	// 多次调用 Approve 不应该 panic
	node.Approve()
	node.Approve()
	node.Approve()

	// 只能读取一次
	approved, _ := node.Wait()
	if !approved {
		t.Error("expected first read to return true")
	}
}
