// Package event 定义事件处理相关的类型和处理器。
package event

import (
	"fmt"
	"github.com/xiws/orca/pkg/event"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

// TaskCompleteEvent 表示任务完成事件，包含执行结果。
type TaskCompleteEvent struct {
	Result string // 任务执行结果的 Markdown 内容
	id     int64  // 事件唯一标识
}

// GetId 返回事件的唯一标识。
func (e TaskCompleteEvent) GetId() int64 {
	return e.id
}

// GetName 返回事件名称。
func (e TaskCompleteEvent) GetName() string {
	return "TaskCompleteEvent"
}

// NewTaskCompleteEvent 创建一个任务完成事件。
func NewTaskCompleteEvent(result string, id int64) TaskCompleteEvent {
	return TaskCompleteEvent{
		Result: result,
		id:     id,
	}
}

// TaskCompleteEventHandler 是任务完成事件的处理者。
type TaskCompleteEventHandler struct {
}

// Handle 处理任务完成事件，使用 glamour 将结果渲染为终端 Markdown 格式并输出。
func (b TaskCompleteEventHandler) Handle(ent event.Event) {
	taskEvent, ok := ent.(TaskCompleteEvent)
	if !ok {
		panic("ToolEvent handler is not a ToolEvent")
	}

	// 创建终端 Markdown 渲染器。
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(styles.ASCIIStyleConfig),
		glamour.WithWordWrap(-1),
	)

	if err != nil {
		panic(err)
	}

	// 渲染结果内容并输出到终端。
	cmd, err := renderer.Render(taskEvent.Result)
	if err != nil {
		panic(err)
	}

	fmt.Println(cmd)
}
