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

type streamInvocation struct {
	run        domain.RunID
	invocation domain.InvocationID
}
type streamKey struct {
	streamInvocation
	sequence int
}

// Only an interactive terminal gets a replaceable preview line. Pipes/files
// contain exclusively durable output, so lossy deltas never pollute a saved
// transcript or duplicate the final answer. All escape bytes here are ours.
type streamDisplay struct {
	out          io.Writer
	width        int
	visible      bool
	previews     map[streamKey]string
	confirmed    map[streamInvocation]int
	lastMessages map[domain.RunID][32]byte
}

func newStreamDisplay(out io.Writer) *streamDisplay {
	d := &streamDisplay{out: out, previews: map[streamKey]string{}, confirmed: map[streamInvocation]int{}}
	if f, ok := out.(*os.File); ok && term.IsTerminal(f.Fd()) {
		if width, _, err := term.GetSize(f.Fd()); err == nil {
			d.width = max(1, width-1)
		}
	}
	return d
}

func (d *streamDisplay) clear() error {
	if !d.visible {
		return nil
	}
	d.visible = false
	_, err := fmt.Fprint(d.out, "\r\x1b[2K")
	return err
}

func (d *streamDisplay) delta(e domain.Event) error {
	if d.width == 0 || e.AssistantSequence <= 0 || e.InvocationID == 0 {
		return nil
	}
	inv := streamInvocation{e.RunID, e.InvocationID}
	high, exists := d.confirmed[inv]
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
