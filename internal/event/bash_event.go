package event

import (
	"fmt"
	"orca/pkg/event"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

type BashEvent struct {
	command string
	content string
	id      int64
}

func (e BashEvent) GetId() int64 {
	return e.id
}

func (e BashEvent) GetName() string {
	return "BashEvent"
}

func NewBashEvent(command string, content string, id int64) BashEvent {
	return BashEvent{
		command: command,
		content: content,
		id:      id,
	}
}

type BashEventHandler struct {
}

func (b BashEventHandler) Handle(ent event.Event) {

	bashEvent, ok := ent.(BashEvent)
	if !ok {
		panic("BashEvent handler is not a BashEvent")
	}

	var content = fmt.Sprintf("# %s \n\n task id: %d \n\n %s", bashEvent.command, bashEvent.id, bashEvent.content)

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(styles.NoTTYStyleConfig),
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
