package main

import (
	"crypto/sha256"
	"fmt"
	"unicode/utf8"

	"github.com/xiws/orca/internal/domain"
)

// 流式预览的资源上限：防止恶意或大量流式数据耗尽内存。
const maxPreviews = 32           // 最大同时预览数
const maxPreviewBytes = 8192     // 单个预览最大字节数
const maxStreamInvocations = 256 // 最大追踪的 invocation 数量

// invocationKey 唯一标识一次 invocation（运行 + invocation ID）。
type invocationKey struct {
	run        domain.RunID
	invocation domain.InvocationID
}

// streamKey 在 invocation 基础上加上 assistant 序列号，标识一条流式片段。
type streamKey struct {
	invocationKey
	sequence int
}

// addDelta 处理流式增量事件：预览文本是有损且有界的，不能作为最终答案来源。
// 高水位标记拒绝已持久化确认后的迟到 delta。当 invocation 身份预算用尽时，
// 停止预览新 invocation，而不是驱逐围栏意外复活旧文本。持久化事件不受此处限制。
func (m *model) addDelta(e domain.Event) {
	if e.AssistantSequence <= 0 || e.InvocationID == 0 {
		return
	}
	if m.previews == nil {
		m.previews = map[streamKey]string{}
	}
	if m.confirmed == nil {
		m.confirmed = map[invocationKey]int{}
	}
	inv := invocationKey{e.RunID, e.InvocationID}
	high, exists := m.confirmed[inv]
	if e.AssistantSequence <= high || (!exists && len(m.confirmed) >= maxStreamInvocations) {
		return
	}
	if !exists {
		m.confirmed[inv] = 0
	}
	key := streamKey{inv, e.AssistantSequence}
	text, exists := m.previews[key]
	if !exists {
		if len(m.previews) >= maxPreviews {
			return
		}
		m.previewOrder = append(m.previewOrder, key)
	}
	// 在清理之前做长度限制，避免恶意 chunk 产生第二次无界分配；
	// 被切分的 UTF-8 片段会被替换而非执行。
	remaining := maxPreviewBytes - len(text)
	if remaining <= 0 {
		return
	}
	chunk := e.Content
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
	}
	chunk = safeText(chunk)
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
		for !utf8.ValidString(chunk) {
			chunk = chunk[:len(chunk)-1]
		}
	}
	m.previews[key] = text + chunk
	m.refresh()
}

// confirm 处理持久化消息事件：更新已确认序列号的高水位，并移除已被确认的预览。
func (m *model) confirm(e domain.Event) {
	if e.Kind != "message" || e.AssistantSequence <= 0 {
		return
	}
	inv := invocationKey{e.RunID, e.InvocationID}
	if m.confirmed == nil {
		m.confirmed = map[invocationKey]int{}
	}
	if _, exists := m.confirmed[inv]; exists || len(m.confirmed) < maxStreamInvocations {
		m.confirmed[inv] = max(m.confirmed[inv], e.AssistantSequence)
	}
	order := m.previewOrder[:0]
	for _, key := range m.previewOrder {
		if key.invocationKey == inv && key.sequence <= e.AssistantSequence {
			delete(m.previews, key)
		} else {
			order = append(order, key)
		}
	}
	m.previewOrder = order
}

// appendEvent 将一个持久化事件追加到聊天历史：先 confirm 已确认序列，
// 并检测 completed 事件是否与上一条 message 内容重复。
func (m *model) appendEvent(e domain.Event) {
	m.confirm(e)
	if m.lastMessages == nil {
		m.lastMessages = map[domain.RunID][32]byte{}
	}
	content := e.Content
	hash := sha256.Sum256([]byte(content))
	if e.Kind == "message" {
		m.lastMessages[e.RunID] = hash
	}
	if e.Kind == "completed" {
		if previous, ok := m.lastMessages[e.RunID]; ok && previous == hash {
			content = "(confirms the preceding durable message)"
		}
		delete(m.lastMessages, e.RunID)
	}
	m.append(fmt.Sprintf("[event=%d run=%d invocation=%d assistant=%d kind=%s] %s", e.Sequence, e.RunID, e.InvocationID, e.AssistantSequence, e.Kind, content))
}
