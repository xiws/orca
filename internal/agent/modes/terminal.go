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

// Name 返回模式名称。
func (m *TerminalMode) Name() string { return "terminal" }

// Run 终端操作模式执行：限制工具为 bash，专注构建、运行、装依赖等操作。
func (m *TerminalMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	// 限制工具权限，只允许 bash
	roles.RestrictTools(inv, "bash")
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(inv)
}
