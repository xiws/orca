package roles

import (
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/llm"
)

// runRole 执行一个角色的 LLM 对话。
//
// 所有角色（Clarifier/Planner/Verifier/Repair）遵循相同模式：
// 1. 创建 Task
// 2. 设置 system prompt + user input
// 3. 调用 Runtime.RunTask
// 4. 返回 LLM 输出
//
// 各角色只需构造自己的输入和解析输出。
func runRole(runtime *core.Runtime, systemPrompt, userInput string) (string, error) {
	task := core.NewTask(userInput, "role")
	task.SessionInfo.Messages = []llm.ChatMessage{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: userInput},
	}

	err, result := runtime.RunTask(task)
	if err != nil {
		return "", err
	}
	return result, nil
}
