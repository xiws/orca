package event

import (
	"fmt"
	"github.com/xiws/orca/internal/tool"
	"github.com/xiws/orca/pkg/event"

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

// GetId 返回事件的唯一标识。
func (e ToolAfterEvent) GetId() int64 { return e.id }

// GetName 返回事件名称。
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

// Handle 处理 after-event，使用 glamour 渲染 markdown 结果到终端。
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
		renderAfterRaw(e.tool, e.file, e.summary, e.content, e.ok)
		return
	}

	md := renderAfterMarkdown(e.tool, e.file, e.content, e.summary, e.ok, e.exitCode)
	rendered, err := renderer.Render(md)
	if err != nil {
		renderAfterRaw(e.tool, e.file, e.summary, e.content, e.ok)
		return
	}
	fmt.Print(rendered)
}
