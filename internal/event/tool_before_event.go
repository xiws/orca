package event

import (
	"fmt"
	"orca/pkg/event"
)

type ToolBeforeEvent struct {
	command string
	goal    string
	id      int64
}

func (e ToolBeforeEvent) GetId() int64 {
	return e.id
}

func (e ToolBeforeEvent) GetName() string {
	return "ToolBeforeEvent"
}

func NewToolBeforeEvent(command string, goal string, id int64) ToolBeforeEvent {
	return ToolBeforeEvent{
		command: command,
		goal:    goal,
		id:      id,
	}
}

type ToolEventBeforeHandler struct {
}

func (b ToolEventBeforeHandler) Handle(ent event.Event) {
	toolEvent, ok := ent.(ToolBeforeEvent)
	if !ok {
		panic("ToolEvent handler is not a ToolEvent")
	}
	fmt.Print("\033[1m")
	fmt.Print("\033[38;5;15m")  // 前景色（文字）白色
	fmt.Print("\033[42;5;250m") // 背景色浅灰
	var content string
	if toolEvent.goal != "" {
		content = fmt.Sprintf(" [%s] %s \033[0m \n\n ", toolEvent.goal, toolEvent.command)
	} else {
		content = fmt.Sprintf(" %s \033[0m \n\n ", toolEvent.command)
	}
	fmt.Println(content)
}
