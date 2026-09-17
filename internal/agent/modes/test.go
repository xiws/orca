package modes

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
)

// TestMode 测试模式：写测试、跑测试、分析失败。
// 简化版实现：Executor 全工具写测试 + Verifier 跑测试验证。
type TestMode struct {
	Runtime *core.Runtime
}

func (m *TestMode) Name() string { return "test" }

func (m *TestMode) Run(task *core.Task) (string, error) {
	// 1. Executor 全工具写/改测试
	executor := &roles.Executor{Runtime: m.Runtime}
	result, err := executor.Execute(task)
	if err != nil {
		return "", fmt.Errorf("test mode execute: %w", err)
	}

	// 2. Verifier 只读跑测试验证
	verifier := &roles.Verifier{Runtime: m.Runtime}
	verifyResult, err := verifier.Verify(task)
	if err != nil {
		// 验证失败不阻塞，返回 Executor 结果
		return result, nil
	}

	if verifyResult.Passed {
		return result, nil
	}

	// 3. 验证未通过：尝试 Repair 生成修复方案
	repair := &roles.Repair{Runtime: m.Runtime}
	repairResult, err := repair.Repair(task, verifyResult)
	if err != nil {
		// 修复方案生成失败，返回当前结果
		return result, nil
	}

	// 将修复方案追加到任务输入，再执行一次
	task.Input += "\n\n## 修复方案\n" + repairResult.FixPlan
	task.TaskResult = ""
	return executor.Execute(task)
}
