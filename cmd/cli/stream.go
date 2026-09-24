package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/xiws/orca/internal/domain"
)

// streamInvocation 唯一标识一次 invocation（运行 + invocation ID）。
type streamInvocation struct {
	run        domain.RunID
	invocation domain.InvocationID
}

// streamKey 在 invocation 基础上加上 assistant 序列号，标识一条流式片段。
type streamKey struct {
	streamInvocation
	sequence int
}

// streamDisplay 管理 CLI 下的流式预览输出。只有交互终端才显示可替换的预览行；
// 管道/文件只包含持久化输出，有损的 delta 不会污染保存的转录或重复最终答案。
// 此处所有的转义字节均由本结构产生。
type streamDisplay struct {
	out          io.Writer
	width        int
	visible      bool
	previews     map[streamKey]string
	confirmed    map[streamInvocation]int
	lastMessages map[domain.RunID][32]byte
}

// newStreamDisplay 构造 streamDisplay；若输出为终端则读取宽度以启用预览行。
func newStreamDisplay(out io.Writer) *streamDisplay {
	d := &streamDisplay{out: out, previews: map[streamKey]string{}, confirmed: map[streamInvocation]int{}}
	if f, ok := out.(*os.File); ok && term.IsTerminal(f.Fd()) {
		if width, _, err := term.GetSize(f.Fd()); err == nil {
			d.width = max(1, width-1)
		}
	}
	return d
}

// clear 清除当前可见的预览行（使用 ANSI 转义序列）。
func (d *streamDisplay) clear() error {
	if !d.visible {
		return nil
	}
	d.visible = false
	_, err := fmt.Fprint(d.out, "\r\x1b[2K")
	return err
}

// delta 处理流式增量事件：仅在终端模式下显示临时预览，受大小和数量限制。
func (d *streamDisplay) delta(e domain.Event) error {
	if d.width == 0 || e.AssistantSequence <= 0 || e.InvocationID == 0 {
		return nil
	}
	inv := streamInvocation{e.RunID, e.InvocationID}
	high, exists := d.confirmed[inv]
	// 跳过已确认的序列，且在 invocation 数量过多时不再注册新 invocation
	if e.AssistantSequence <= high || (!exists && len(d.confirmed) >= 256) {
		return nil
	}
	if !exists {
		d.confirmed[inv] = 0
	}
	key := streamKey{inv, e.AssistantSequence}
	text, exists := d.previews[key]
	if !exists && len(d.previews) >= 32 {
		return nil
	}
	remaining := 8192 - len(text)
	if remaining <= 0 {
		return nil
	}
	chunk := e.Content
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
	}
	text += chunk
	// 若拼接导致 UTF-8 不完整，则逐字节回退直至合法
	for !utf8.ValidString(text) && len(text) > 0 {
		text = text[:len(text)-1]
	}
	d.previews[key] = text
	if err := d.clear(); err != nil {
		return err
	}
	line := fmt.Sprintf("[stream run=%d invocation=%d assistant=%d; provisional] %+q", e.RunID, e.InvocationID, e.AssistantSequence, text)
	d.visible = true
	_, err := fmt.Fprint(d.out, ansi.Truncate(line, d.width, ""))
	return err
}

// events 处理一批持久化事件：清除预览，更新已确认序列号，输出消息事件。
func (d *streamDisplay) events(events []domain.Event) error {
	if len(events) == 0 {
		return nil
	}
	if err := d.clear(); err != nil {
		return err
	}
	for _, e := range events {
		if e.Kind != "message" || e.AssistantSequence <= 0 {
			continue
		}
		inv := streamInvocation{e.RunID, e.InvocationID}
		if _, exists := d.confirmed[inv]; exists || len(d.confirmed) < 256 {
			d.confirmed[inv] = max(d.confirmed[inv], e.AssistantSequence)
		}
		for key := range d.previews {
			if key.streamInvocation == inv && key.sequence <= e.AssistantSequence {
				delete(d.previews, key)
			}
		}
	}
	if d.lastMessages == nil {
		d.lastMessages = map[domain.RunID][32]byte{}
	}
	return printDurableEvents(d.out, events, d.lastMessages)
}

// printDurableEvents 输出持久化事件，若 completed 事件的内容与上一条 message 相同，
// 则替换为确认提示以避免重复。
func printDurableEvents(out io.Writer, events []domain.Event, last map[domain.RunID][32]byte) error {
	for _, e := range events {
		content := e.Content
		hash := sha256.Sum256([]byte(content))
		if e.Kind == "message" {
			last[e.RunID] = hash
		}
		if e.Kind == "completed" {
			if previous, ok := last[e.RunID]; ok && previous == hash {
				content = "(confirms the preceding durable message)"
			}
			delete(last, e.RunID)
		}
		if _, err := fmt.Fprintf(out, "[event=%d run=%d invocation=%d assistant=%d kind=%q] %q\n", e.Sequence, e.RunID, e.InvocationID, e.AssistantSequence, e.Kind, content); err != nil {
			return err
		}
	}
	return nil
}
