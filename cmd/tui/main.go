package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// programRef 是事件处理器通过它向 Bubble Tea 发送消息的共享指针。
// 在 NewProgram 之后、Run 之前设置。
var programRef *tea.Program

func main() {
	m := newModel()
	p := tea.NewProgram(m, tea.WithAltScreen())
	programRef = p
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "orca tui: %v\n", err)
		os.Exit(1)
	}
}
