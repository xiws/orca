package event

import (
	"fmt"
	"orca/pkg/event"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

type ToolAfterEvent struct {
	command string
	content string
	id      int64
}

func (e ToolAfterEvent) GetId() int64 {
	return e.id
}

func (e ToolAfterEvent) GetName() string {
	return "ToolAfterEvent"
}

func NewToolAfterEvent(command string, content string, id int64) ToolAfterEvent {
	return ToolAfterEvent{
		command: command,
		content: content,
		id:      id,
	}
}

type ToolAfterEventHandler struct {
}

func (b ToolAfterEventHandler) Handle(ent event.Event) {
	toolEvent, ok := ent.(ToolAfterEvent)
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

	cmd, err := renderer.Render(toolEvent.content)
	if err != nil {
		panic(err)
	}

	fmt.Println(cmd)
}
