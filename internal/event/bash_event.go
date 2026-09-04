package event

import (
	"fmt"
	"orca/pkg/event"

	"github.com/charmbracelet/glamour"
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

	var content = fmt.Sprintf("# %s \n\n task id: %d \n\n %s", bashEvent.command, bashEvent.id)

	cmd, err := glamour.Render(content, "light")
	if err != nil {
		panic(err)
	}

	out, err := glamour.Render(bashEvent.content, "light")
	if err != nil {
		panic(err)
	}

	fmt.Println(cmd)
	fmt.Print(out)
}
