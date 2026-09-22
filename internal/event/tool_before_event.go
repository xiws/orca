package event

import (
	"fmt"
	"github.com/xiws/orca/pkg/event"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

// ToolBeforeEvent 在工具命令执行前发布。它携带结构化元数据，
// 以便处理器无需解析扁平命令字符串即可渲染可读的预览。
type ToolBeforeEvent struct {
	id        int64
	tool      string // "read" | "write" | "edit" | "bash" | "create_task"
	file      string // 文件路径（仅文件操作）
	meta      string // 额外信息：行范围、片段数、bash 命令
	reasoning string // 模型对此工具调用的推理
}

func (e ToolBeforeEvent) GetId() int64    { return e.id }
func (e ToolBeforeEvent) GetName() string { return "ToolBeforeEvent" }

// ToolName 返回命令名（"read"、"write"、"edit"、"bash"、"create_task"）。
func (e ToolBeforeEvent) ToolName() string { return e.tool }

// File 返回目标文件路径。
func (e ToolBeforeEvent) File() string { return e.file }

// Meta 返回额外上下文信息（行范围、bash 命令等）。
func (e ToolBeforeEvent) Meta() string { return e.meta }

// Reasoning 返回模型对此工具调用的推理。
func (e ToolBeforeEvent) Reasoning() string { return e.reasoning }

// NewToolBeforeEvent 构建带结构化字段的 before-event。
//
//   - tool: 命令名（"read"、"write"、"edit"、"bash"、"create_task"）
//   - file: 目标文件路径（bash / create_task 为空）
//   - meta: 上下文信息（行范围、字节数、片段数、bash 命令等）
//   - reasoning: 模型推理文本
//   - id: 关联用的调用 id
func NewToolBeforeEvent(tool, file, meta, reasoning string, id int64) ToolBeforeEvent {
	return ToolBeforeEvent{
		tool:      tool,
		file:      file,
		meta:      meta,
		reasoning: reasoning,
		id:        id,
	}
}

// ToolEventBeforeHandler 将 before-event 渲染为结构化 markdown 预览，
// 使用 glamour，带有工具特定的 emoji 和格式。
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
		// 回退：打印原始内容
		fmt.Printf("%s %s %s\n", e.tool, e.file, e.meta)
		return
	}

	md := renderBeforeMarkdown(e.tool, e.file, e.meta, e.reasoning)
	rendered, err := renderer.Render(md)
	if err != nil {
		fmt.Printf("%s %s %s\n", e.tool, e.file, e.meta)
		return
	}
	fmt.Print(rendered)
}
