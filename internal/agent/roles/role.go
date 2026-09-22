package roles

import (
	"slices"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/llm"
)

// RestrictTools 将当前 Invocation 的工具权限收紧为继承权限与角色/模式权限的交集。
// 沿用 Provider 的约定：nil 表示未限制，非 nil 的空列表表示禁用全部工具。
// 始终创建非 nil 的新列表，避免空交集变成全部权限或修改调用方的底层切片。
func RestrictTools(inv *core.Invocation, allowedTools ...string) {
	inherited := inv.Provider.AllowedTools
	restricted := make([]string, 0, len(allowedTools))
	for _, tool := range allowedTools {
		if inherited == nil || slices.Contains(inherited, tool) {
			restricted = append(restricted, tool)
		}
	}
	inv.Provider.AllowedTools = restricted
}

// newRoleInvocation 为角色创建同一任务下的独立执行上下文，轨迹保留在父 Invocation 树中。
func newRoleInvocation(parentInv *core.Invocation, systemPrompt, userInput string, allowedTools ...string) *core.Invocation {
	inv := parentInv.NewChild()
	RestrictTools(inv, allowedTools...)
	inv.AppendMessage(llm.RoleSystem, systemPrompt)
	inv.AppendMessage(llm.RoleUser, userInput)
	return inv
}

// runRole 使用调用方的模型和工作区执行角色，不创建 Task 或用户 Session。
func runRole(runtime *core.Runtime, parentInv *core.Invocation, systemPrompt, userInput string, allowedTools ...string) (string, error) {
	return runtime.Run(newRoleInvocation(parentInv, systemPrompt, userInput, allowedTools...))
}
