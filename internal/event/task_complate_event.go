package event

import (
	"fmt"
	"orca/pkg/event"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

type TaskComplateEvent struct {
	Result string
	id     int64
}

func (e TaskComplateEvent) GetId() int64 {
	return e.id
}

func (e TaskComplateEvent) GetName() string {
	return "TaskComplateEvent"
}

func NewTaskComplateEvent(result string, id int64) TaskComplateEvent {
	return TaskComplateEvent{
		Result: result,
		id:     id,
	}
}

type TaskComplateEventHandler struct {
}

func (b TaskComplateEventHandler) Handle(ent event.Event) {
	taskEvent, ok := ent.(TaskComplateEvent)
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
