// Package modes 定义交互模式的编排层。
// 模式只做三件事：限工具、选角色、定流转；具体执行一律委托底座。
package modes

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/domain"
)

// Mode 是一个交互模式的编排器。
type Mode interface {
	// Name 返回模式名，供 CLI 参数与 TUI 切换使用。
	Name() string

	// Run 编排任务，在独立的 Invocation 中执行并返回最终答复。
	Run(task *domain.Task, inv *core.Invocation) (string, error)
}

// For 按名称构造模式（默认 code）。
func For(name string, rt *core.Runtime) Mode {
	switch name {
	case "ask":
		return &AskMode{Runtime: rt}
	case "code":
		return &CodeMode{Runtime: rt}
	case "plan":
		return &PlanMode{Runtime: rt}
	case "agent":
		return &AgentMode{Runtime: rt}
	case "review":
		return &ReviewMode{Runtime: rt}
	case "test":
		return &TestMode{Runtime: rt}
	case "terminal":
		return &TerminalMode{Runtime: rt}
	case "deliberate":
		return &DeliberateMode{Runtime: rt}
	default:
		panic(fmt.Sprintf("modes: unknown mode %q", name))
	}
}
