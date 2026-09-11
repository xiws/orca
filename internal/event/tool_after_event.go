package event

import (
	"fmt"
	"orca/internal/tool"
	"orca/pkg/event"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

// ToolAfterEvent is published after a tool command executes. It carries
// structured metadata so the handler can render readable results.
type ToolAfterEvent struct {
	id       int64
	tool     string // "read" | "write" | "edit" | "bash"
	file     string // target file path (empty for bash)
	ok       bool   // whether the command succeeded
	summary  string // one-line summary
	content  string // detailed output content
	exitCode int    // bash exit code; 0 for non-bash or success
}

func (e ToolAfterEvent) GetId() int64    { return e.id }
func (e ToolAfterEvent) GetName() string { return "ToolAfterEvent" }

// NewToolAfterEvent builds an after-event with structured fields.
//
//   - tool: command name ("read", "write", "edit", "bash")
//   - file: target file path (empty for bash)
//   - content: detailed output (file content for read, merged output for bash)
//   - ok: whether the command succeeded
//   - summary: one-line summary
//   - exitCode: bash exit status (0 otherwise)
//   - id: invocation id
func NewToolAfterEvent(tool, file, content string, ok bool, summary string, exitCode int, id int64) ToolAfterEvent {
	return ToolAfterEvent{
		tool:     tool,
		file:     file,
		content:  content,
		ok:       ok,
		summary:  summary,
		exitCode: exitCode,
		id:       id,
	}
}

// ToolAfterEventHandler renders the after-event result using glamour.
type ToolAfterEventHandler struct{}

func (b ToolAfterEventHandler) Handle(ent event.Event) {
	if tool.Get(tool.KeyDebug) == "false" {
		return
	}

	e, ok := ent.(ToolAfterEvent)
	if !ok {
		panic("ToolAfterEventHandler received a non-ToolAfterEvent")
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(styles.ASCIIStyleConfig),
		glamour.WithWordWrap(-1),
	)
	if err != nil {
		// Fallback: print raw
		b.printRaw(e)
		return
	}

	md := b.renderMarkdown(e)
	rendered, err := renderer.Render(md)
	if err != nil {
		b.printRaw(e)
		return
	}
	fmt.Print(rendered)
}

// renderMarkdown builds the markdown representation of a tool after-event.
func (b ToolAfterEventHandler) renderMarkdown(e ToolAfterEvent) string {
	if !e.ok {
		return fmt.Sprintf("❌ **%s** `%s` failed: %s\n", e.tool, e.file, e.summary)
	}

	switch e.tool {
	case "read":
		lang := detectLang(e.file)
		return fmt.Sprintf("```%s\n%s\n```\n", lang, e.content)

	case "write":
		return fmt.Sprintf("✅ %s\n", e.summary)

	case "edit":
		return fmt.Sprintf("✅ %s\n", e.summary)

	case "bash":
		var out strings.Builder
		exitWord := "ok"
		if e.exitCode != 0 {
			exitWord = fmt.Sprintf("exit code: %d", e.exitCode)
		}
		out.WriteString(fmt.Sprintf("```\n$ %s\n```\n", e.summary))
		if e.content != "" {
			out.WriteString(fmt.Sprintf("```\n%s\n```\n", e.content))
		}
		if e.exitCode != 0 {
			out.WriteString(fmt.Sprintf("\n_%s_\n", exitWord))
		}
		return out.String()

	default:
		return fmt.Sprintf("```txt\n%s\n```\n", e.content)
	}
}

// printRaw prints the event content as plain text, used as a fallback when
// glamour rendering fails.
func (b ToolAfterEventHandler) printRaw(e ToolAfterEvent) {
	if !e.ok {
		fmt.Printf("❌ %s (%s): %s\n", e.tool, e.file, e.summary)
		return
	}
	fmt.Printf("✔ %s (%s): %s\n", e.tool, e.file, e.summary)
	if e.content != "" {
		fmt.Println(e.content)
	}
}

// detectLang maps a file extension to a markdown code-block language tag.
// Returns an empty string for unknown extensions, which renders as a plain
// code block without syntax highlighting.
func detectLang(filePath string) string {
	switch {
	case strings.HasSuffix(filePath, ".go"):
		return "go"
	case strings.HasSuffix(filePath, ".md"):
		return "markdown"
	case strings.HasSuffix(filePath, ".json"):
		return "json"
	case strings.HasSuffix(filePath, ".yaml"),
		strings.HasSuffix(filePath, ".yml"):
		return "yaml"
	case strings.HasSuffix(filePath, ".sh"):
		return "bash"
	case strings.HasSuffix(filePath, ".py"):
		return "python"
	case strings.HasSuffix(filePath, ".js"):
		return "javascript"
	case strings.HasSuffix(filePath, ".ts"):
		return "typescript"
	case strings.HasSuffix(filePath, ".html"):
		return "html"
	case strings.HasSuffix(filePath, ".css"):
		return "css"
	case strings.HasSuffix(filePath, ".toml"):
		return "toml"
	case strings.HasSuffix(filePath, ".mod"):
		return "go"
	case strings.HasSuffix(filePath, ".sum"):
		return "go"
	case strings.HasSuffix(filePath, ".rs"):
		return "rust"
	case strings.HasSuffix(filePath, ".rb"):
		return "ruby"
	case strings.HasSuffix(filePath, ".java"):
		return "java"
	case strings.HasSuffix(filePath, ".sql"):
		return "sql"
	case strings.HasSuffix(filePath, ".xml"):
		return "xml"
	case strings.HasSuffix(filePath, ".proto"):
		return "protobuf"
	default:
		return ""
	}
}
