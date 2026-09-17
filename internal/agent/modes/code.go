package modes

import (
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
)

// CodeMode 日常开发模式：直接干活，全工具单循环。
// 可以修改代码、创建文件、运行基础工具。
type CodeMode struct {
	Runtime *core.Runtime
}

func (m *CodeMode) Name() string { return "code" }

func (m *CodeMode) Run(task *core.Task) (string, error) {
	// 直连执行，全工具单循环（Tools=nil 表示不限制）
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(task)
}
