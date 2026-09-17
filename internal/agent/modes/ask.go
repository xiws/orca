package modes

import (
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
)

// AskMode 只读问答模式：不澄清、不规划、不进状态机，单角色单循环。
// 工具只给 read，从根上保证"不改文件"。
type AskMode struct {
	Runtime *core.Runtime
}

func (m *AskMode) Name() string { return "ask" }

func (m *AskMode) Run(task *core.Task) (string, error) {
	executor := &roles.Executor{Runtime: m.Runtime, Tools: []string{"read"}}
	return executor.Execute(task)
}
