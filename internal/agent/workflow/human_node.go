package workflow

import (
	"fmt"
	"time"
)

// HumanNode 表示工作流中的人工审批节点。
// 用于高风险操作，需要人工确认后才能继续执行。
type HumanNode struct {
	// ID 节点唯一标识。
	ID string `json:"id"`

	// Message 显示给用户的审批消息。
	Message string `json:"message"`

	// Timeout 等待审批的超时时间。
	// 零值表示不超时（无限等待）。
	Timeout time.Duration `json:"timeout"`

	// Response 审批结果通道。
	// true = 批准，false = 拒绝。
	Response chan bool `json:"-"`
}

// NewHumanNode 创建人工审批节点。
func NewHumanNode(id, message string, timeout time.Duration) *HumanNode {
	return &HumanNode{
		ID:       id,
		Message:  message,
		Timeout:  timeout,
		Response: make(chan bool, 1),
	}
}

// Wait 阻塞等待用户审批。
//
// 返回：
// - true: 用户批准
// - false: 用户拒绝
// - error: 超时或其他错误
//
// CLI 模式下通过 stdin 交互，TUI 模式下通过 UI 弹窗。
func (h *HumanNode) Wait() (bool, error) {
	if h.Timeout > 0 {
		select {
		case approved := <-h.Response:
			return approved, nil
		case <-time.After(h.Timeout):
			return false, fmt.Errorf("human approval timeout after %v", h.Timeout)
		}
	}

	// 无超时，无限等待
	approved := <-h.Response
	return approved, nil
}

// Approve 发送批准信号。
func (h *HumanNode) Approve() {
	select {
	case h.Response <- true:
	default:
		// 通道已满或已关闭，忽略
	}
}

// Reject 发送拒绝信号。
func (h *HumanNode) Reject() {
	select {
	case h.Response <- false:
	default:
		// 通道已满或已关闭，忽略
	}
}

// HumanApprovalEvent 是人工审批请求事件。
// 用于通过 Event Bus 通知 TUI/CLI 显示审批界面。
type HumanApprovalEvent struct {
	NodeID  string
	Message string
	Node    *HumanNode
}

// GetName 返回事件名称。
func (e HumanApprovalEvent) GetName() string {
	return "human_approval_request"
}

// GetId 返回事件 ID。
func (e HumanApprovalEvent) GetId() int64 {
	return time.Now().UnixNano()
}
