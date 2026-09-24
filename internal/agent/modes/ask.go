package modes

import (
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/domain"
)

// AskMode 只读问答模式：不澄清、不规划、不进状态机，单角色单循环。
// 工具限制为调用方已允许的 read。
type AskMode struct {
	Runtime *core.Runtime
}

// Name 返回模式名称。
func (m *AskMode) Name() string { return "ask" }

// Run 只读模式执行：限制工具为 read，直接交给 Executor 执行。
func (m *AskMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	// 限制工具权限，只允许 read
	roles.RestrictTools(inv, "read")
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(inv)
}
