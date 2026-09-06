package event

import (
	"fmt"
	"orca/pkg/event"
	"strings"

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

	if strings.HasSuffix(toolEvent.command, ".md") {
		b.markdown(toolEvent.content)
		return
	}

	if strings.HasSuffix(toolEvent.command, ".go") {
		b.markdown(fmt.Sprintf("```go\n%s\n```", toolEvent.content))
		return
	}

	if strings.HasSuffix(toolEvent.command, ".json") {
		b.markdown(fmt.Sprintf("```json\n%s\n```", toolEvent.content))
		return
	}
	b.markdown(fmt.Sprintf("```txt\n%s\n```", toolEvent.content))
}

func (b ToolAfterEventHandler) markdown(ctx string) {
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(styles.ASCIIStyleConfig),
		glamour.WithWordWrap(-1),
	)

	if err != nil {
		panic(err)
	}

	cmd, err := renderer.Render(ctx)
	if err != nil {
		panic(err)
	}

	fmt.Println(cmd)
}
