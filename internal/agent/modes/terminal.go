package modes

import (
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/domain"
)

// TerminalMode 环境操作模式：工具限制为调用方已允许的 bash，专注构建、运行、装依赖等操作。
type TerminalMode struct {
	Runtime *core.Runtime
}

func (m *TerminalMode) Name() string { return "terminal" }

func (m *TerminalMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	roles.RestrictTools(inv, "bash")
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(inv)
}
