package modes

import (
	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/llm"
)

// ReviewMode 只读评审模式：只关注代码质量、潜在 bug、安全性、性能。
// 不修改任何文件，工具只给 read + bash（bash 仅运行验证命令）。
type ReviewMode struct {
	Runtime *core.Runtime
}

func (m *ReviewMode) Name() string { return "review" }

func (m *ReviewMode) Run(task *core.Task) (string, error) {
	// 注入评审系统提示词
	if task.SessionInfo == nil {
		task.SessionInfo = core.NewSession()
	}
	task.SessionInfo.Messages = append([]llm.ChatMessage{
		{Role: llm.RoleSystem, Content: reviewSystemPrompt},
	}, task.SessionInfo.Messages...)

	task.SessionInfo.Provider.AllowedTools = []string{"read", "bash"}
	executor := &roles.Executor{Runtime: m.Runtime}
	return executor.Execute(task)
}

// reviewSystemPrompt 代码评审系统提示词：质量、bug、安全、性能四维度。
const reviewSystemPrompt = `你是一位资深代码评审专家。请从以下四个维度对代码进行评审：

1. **代码质量**：命名规范、可读性、结构清晰度、是否符合项目约定
2. **潜在 Bug**：逻辑错误、边界条件、空指针、资源泄漏、并发安全
3. **安全性**：注入风险、敏感信息暴露、权限校验、输入验证
4. **性能**：不必要的分配、低效算法、N+1 查询、锁竞争

评审规则：
- 使用 read 工具读取相关代码文件
- 使用 bash 工具运行编译、测试等验证命令佐证结论
- 不要修改任何文件
- 按 critical / warning / info 三级分类问题
- 每个问题给出具体文件、行号和修复建议
- 最后给出总体评价和总结`
