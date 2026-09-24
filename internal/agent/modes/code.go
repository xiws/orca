package modes

import (
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/domain"
)

// CodeMode 日常开发模式：直接执行，在调用方的工具权限内完成开发。
type CodeMode struct {
	Runtime *core.Runtime
}

// Name 返回模式名称。
func (m *CodeMode) Name() string { return "code" }

// Run 直接执行模式：不限制额外工具，使用调用方的完整工具权限。
func (m *CodeMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(inv)
}
