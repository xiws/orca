package event

import (
	"fmt"
	"github.com/xiws/orca/internal/tool"
	"github.com/xiws/orca/pkg/event"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

// ToolAfterEvent 在工具命令执行后发布。它携带结构化元数据，
// 以便处理器渲染可读的结果。
type ToolAfterEvent struct {
	id       int64
	tool     string // "read" | "write" | "edit" | "bash"
	file     string // 目标文件路径（bash 为空）
	ok       bool   // 命令是否成功
	summary  string // 单行摘要
	content  string // 详细输出内容
	exitCode int    // bash 退出码；非 bash 或成功时为 0
}

func (e ToolAfterEvent) GetId() int64    { return e.id }
func (e ToolAfterEvent) GetName() string { return "ToolAfterEvent" }

// ToolName 返回命令名。
func (e ToolAfterEvent) ToolName() string { return e.tool }

// File 返回目标文件路径。
func (e ToolAfterEvent) File() string { return e.file }

// OK 返回命令是否成功。
func (e ToolAfterEvent) OK() bool { return e.ok }

// Content 返回详细输出内容。
func (e ToolAfterEvent) Content() string { return e.content }

// Summary 返回单行摘要。
func (e ToolAfterEvent) Summary() string { return e.summary }

// ExitCode 返回 bash 退出码。
func (e ToolAfterEvent) ExitCode() int { return e.exitCode }

// NewToolAfterEvent 构建带结构化字段的 after-event。
//
//   - tool: 命令名（"read"、"write"、"edit"、"bash"）
//   - file: 目标文件路径（bash 为空）
//   - content: 详细输出（read 的文件内容，bash 的合并输出）
//   - ok: 命令是否成功
//   - summary: 单行摘要
//   - exitCode: bash 退出状态（其他情况为 0）
//   - id: 调用 id
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

// ToolAfterEventHandler 使用 glamour 渲染 after-event 结果。
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
		// 回退：打印原始内容
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

// renderMarkdown 构建工具 after-event 的 markdown 表示。
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

// printRaw 以纯文本形式打印事件内容，作为 glamour 渲染失败时的回退。
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

// detectLang 将文件扩展名映射到 markdown 代码块的语言标签。
// 未知扩展名返回空字符串，渲染为无语法高亮的普通代码块。
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
