package main

import (
	"crypto/sha256"
	"fmt"
	"unicode/utf8"

	"github.com/xiws/orca/internal/domain"
)

const maxPreviews = 32
const maxPreviewBytes = 8192
const maxStreamInvocations = 256

type invocationKey struct {
	run        domain.RunID
	invocation domain.InvocationID
}
type streamKey struct {
	invocationKey
	sequence int
}

// Preview text is lossy and bounded, never a source for a final answer. A
// high-water mark rejects late deltas after durable confirmation. Once the
// identity budget is full we stop previewing new invocations, not evict fences
// and accidentally resurrect old text. Durable events are never capped here.
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
	// Limit before sanitation as well, so a malicious chunk cannot create a
	// second unbounded allocation. Split UTF-8 chunks are replaced, not executed.
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
