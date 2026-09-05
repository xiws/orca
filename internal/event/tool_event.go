package event

import (
	"fmt"
	"orca/pkg/event"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

type ToolEvent struct {
	command string
	content string
	id      int64
}

func (e ToolEvent) GetId() int64 {
	return e.id
}

func (e ToolEvent) GetName() string {
	return "ToolEvent"
}

func NewToolEvent(command string, content string, id int64) ToolEvent {
	return ToolEvent{
		command: command,
		content: content,
		id:      id,
	}
}

type ToolEventHandler struct {
}

func (b ToolEventHandler) Handle(ent event.Event) {
	toolEvent, ok := ent.(ToolEvent)
	if !ok {
		panic("ToolEvent handler is not a ToolEvent")
	}

	var content = fmt.Sprintf("# %s \n\n task id: %d \n----------- \n\n %s\n\n-----------", toolEvent.command, toolEvent.id, toolEvent.content)

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(styles.ASCIIStyleConfig),
		glamour.WithWordWrap(-1),
	)

	if err != nil {
		panic(err)
	}

	cmd, err := renderer.Render(content)
	if err != nil {
		panic(err)
	}

	fmt.Println(cmd)
}
