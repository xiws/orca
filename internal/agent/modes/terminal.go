package modes

import (
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
)

// TerminalMode 环境操作模式：工具只给 bash，专注构建、运行、装依赖等操作。
type TerminalMode struct {
	Runtime *core.Runtime
}

func (m *TerminalMode) Name() string { return "terminal" }

func (m *TerminalMode) Run(task *core.Task) (string, error) {
	task.SessionInfo.Provider.AllowedTools = []string{"bash"}
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(task)
}
