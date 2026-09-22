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

func (m *AskMode) Name() string { return "ask" }

func (m *AskMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	roles.RestrictTools(inv, "read")
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(inv)
}
