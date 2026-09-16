package memory

import "sync"

// SessionMemory 是短期记忆，存储单次任务的上下文。
//
// 生命周期：单次任务，任务结束后清空。
// 内容：当前消息、工具结果、临时变量。
//
// 注意：SessionMemory 包装现有 agent.Session，不重复实现消息存储。
// 它提供额外的工具结果缓存和临时变量存储。
type SessionMemory struct {
	mu sync.RWMutex

	// ToolResults 缓存工具调用结果。
	// key: tool call ID, value: tool result
	ToolResults map[string]string

	// Variables 临时变量，供 Agent 在执行过程中使用。
	Variables map[string]any
}

// NewSessionMemory 创建会话记忆。
func NewSessionMemory() *SessionMemory {
	return &SessionMemory{
		ToolResults: make(map[string]string),
		Variables:   make(map[string]any),
	}
}

// SetToolResult 缓存工具调用结果。
func (m *SessionMemory) SetToolResult(callID, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ToolResults[callID] = result
}

// GetToolResult 获取缓存的工具结果。
func (m *SessionMemory) GetToolResult(callID string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result, ok := m.ToolResults[callID]
	return result, ok
}

// SetVariable 设置临时变量。
func (m *SessionMemory) SetVariable(key string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Variables[key] = value
}

// GetVariable 获取临时变量。
func (m *SessionMemory) GetVariable(key string) (any, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, ok := m.Variables[key]
	return value, ok
}

// Clear 清空所有会话记忆。
func (m *SessionMemory) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ToolResults = make(map[string]string)
	m.Variables = make(map[string]any)
}
