package event

import (
	"fmt"
	"orca/pkg/event"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

// ToolBeforeEvent is published before a tool command executes. It carries
// structured metadata so the handler can render a readable preview without
// parsing a flat command string.
type ToolBeforeEvent struct {
	id        int64
	tool      string // "read" | "write" | "edit" | "bash" | "create_task"
	file      string // file path (file ops only)
	meta      string // extra info: line range, fragment count, bash command
	reasoning string // model's reasoning for this tool call
}

func (e ToolBeforeEvent) GetId() int64    { return e.id }
func (e ToolBeforeEvent) GetName() string { return "ToolBeforeEvent" }

// NewToolBeforeEvent builds a before-event with structured fields.
//
//   - tool: command name ("read", "write", "edit", "bash", "create_task")
//   - file: target file path (empty for bash / create_task)
//   - meta: contextual info (line range, byte count, fragment count, bash command, etc.)
//   - reasoning: model reasoning text
//   - id: invocation id for correlation
func NewToolBeforeEvent(tool, file, meta, reasoning string, id int64) ToolBeforeEvent {
	return ToolBeforeEvent{
		tool:      tool,
		file:      file,
		meta:      meta,
		reasoning: reasoning,
		id:        id,
	}
}

// ToolEventBeforeHandler renders the before-event as a structured markdown
// preview using glamour, with tool-specific emoji and formatting.
type ToolEventBeforeHandler struct{}

func (b ToolEventBeforeHandler) Handle(ent event.Event) {
	e, ok := ent.(ToolBeforeEvent)
	if !ok {
		panic("ToolEventBeforeHandler received a non-ToolBeforeEvent")
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(styles.ASCIIStyleConfig),
		glamour.WithWordWrap(-1),
	)
	if err != nil {
		// Fallback: print raw
		fmt.Printf("%s %s %s\n", e.tool, e.file, e.meta)
		return
	}

	md := b.renderMarkdown(e)
	rendered, err := renderer.Render(md)
	if err != nil {
		fmt.Printf("%s %s %s\n", e.tool, e.file, e.meta)
		return
	}
	fmt.Print(rendered)
}

// renderMarkdown builds the markdown representation of a tool before-event.
func (b ToolEventBeforeHandler) renderMarkdown(e ToolBeforeEvent) string {
	title := buildTitle(e.tool, e.file, e.meta)

	if e.reasoning == "" {
		return fmt.Sprintf("**%s**\n", title)
	}
	return fmt.Sprintf("**%s**\n> %s\n", title, e.reasoning)
}

// buildTitle returns the emoji-prefixed title line for a tool.
func buildTitle(tool, file, meta string) string {
	const (
		emojiRead       = "📖"
		emojiWrite      = "📝"
		emojiEdit       = "✏️"
		emojiBash       = "💻"
		emojiCreateTask = "🎯"
	)

	switch tool {
	case "read":
		return fmt.Sprintf("%s Read `%s` (%s)", emojiRead, file, meta)
	case "write":
		return fmt.Sprintf("%s Write `%s` (%s)", emojiWrite, file, meta)
	case "edit":
		return fmt.Sprintf("%s Edit `%s` (%s)", emojiEdit, file, meta)
	case "bash":
		return fmt.Sprintf("%s $ `%s`", emojiBash, meta)
	case "create_task":
		return fmt.Sprintf("%s %s", emojiCreateTask, meta)
	default:
		if file != "" {
			return fmt.Sprintf("`%s` — `%s` (%s)", tool, file, meta)
		}
		return fmt.Sprintf("`%s` (%s)", tool, meta)
	}
}
