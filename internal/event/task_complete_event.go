package event

import (
	"fmt"
	"github.com/xiws/orca/pkg/event"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

type TaskCompleteEvent struct {
	Result string
	id     int64
}

func (e TaskCompleteEvent) GetId() int64 {
	return e.id
}

func (e TaskCompleteEvent) GetName() string {
	return "TaskCompleteEvent"
}

func NewTaskCompleteEvent(result string, id int64) TaskCompleteEvent {
	return TaskCompleteEvent{
		Result: result,
		id:     id,
	}
}

type TaskCompleteEventHandler struct {
}

func (b TaskCompleteEventHandler) Handle(ent event.Event) {
	taskEvent, ok := ent.(TaskCompleteEvent)
	if !ok {
		panic("ToolEvent handler is not a ToolEvent")
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(styles.ASCIIStyleConfig),
		glamour.WithWordWrap(-1),
	)

	if err != nil {
		panic(err)
	}

	cmd, err := renderer.Render(taskEvent.Result)
	if err != nil {
		panic(err)
	}

	fmt.Println(cmd)
}
