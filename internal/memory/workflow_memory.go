package memory

import "sync"

// WorkflowMemory 是流程记忆，存储 workflow 实例的状态。
//
// 生命周期：workflow 执行期间。
// 内容：当前节点、历史路径、重试计数、决策记录。
type WorkflowMemory struct {
	mu sync.RWMutex

	// InstanceID workflow 实例 ID。
	InstanceID string

	// CurrentNode 当前执行的节点 ID。
	CurrentNode string

	// History 已访问节点的历史路径。
	History []string

	// RetryCount 当前节点的重试次数。
	RetryCount int

	// Decisions 决策记录，key 为节点 ID，value 为决策结果。
	Decisions map[string]string

	// NodeResults 节点执行结果，key 为节点 ID。
	NodeResults map[string]any
}

// NewWorkflowMemory 创建工作流记忆。
func NewWorkflowMemory() *WorkflowMemory {
	return &WorkflowMemory{
		History:     make([]string, 0),
		Decisions:   make(map[string]string),
		NodeResults: make(map[string]any),
	}
}

// SetInstance 设置 workflow 实例信息。
func (m *WorkflowMemory) SetInstance(instanceID, initialNode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.InstanceID = instanceID
	m.CurrentNode = initialNode
	m.History = []string{initialNode}
	m.RetryCount = 0
}

// RecordVisit 记录访问节点。
func (m *WorkflowMemory) RecordVisit(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CurrentNode = nodeID
	m.History = append(m.History, nodeID)
}

// IncrementRetry 增加重试计数。
func (m *WorkflowMemory) IncrementRetry() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RetryCount++
	return m.RetryCount
}

// ResetRetry 重置重试计数（进入新节点时调用）。
func (m *WorkflowMemory) ResetRetry() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RetryCount = 0
}

// RecordDecision 记录节点决策。
func (m *WorkflowMemory) RecordDecision(nodeID, decision string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Decisions[nodeID] = decision
}

// GetDecision 获取节点决策。
func (m *WorkflowMemory) GetDecision(nodeID string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	decision, ok := m.Decisions[nodeID]
	return decision, ok
}

// SetNodeResult 设置节点执行结果。
func (m *WorkflowMemory) SetNodeResult(nodeID string, result any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.NodeResults[nodeID] = result
}

// GetNodeResult 获取节点执行结果。
func (m *WorkflowMemory) GetNodeResult(nodeID string) (any, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result, ok := m.NodeResults[nodeID]
	return result, ok
}

// GetPath 返回访问路径。
func (m *WorkflowMemory) GetPath() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	path := make([]string, len(m.History))
	copy(path, m.History)
	return path
}

// Reset 重置工作流记忆。
func (m *WorkflowMemory) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.InstanceID = ""
	m.CurrentNode = ""
	m.History = make([]string, 0)
	m.RetryCount = 0
	m.Decisions = make(map[string]string)
	m.NodeResults = make(map[string]any)
}
