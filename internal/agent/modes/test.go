package modes

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
)

// TestMode 测试模式：执行并验证，未通过时最多生成和执行一次修复方案，再次验证。
type TestMode struct {
	Runtime *core.Runtime
}

// Name 返回模式名称。
func (m *TestMode) Name() string { return "test" }

// Run 测试模式执行流程：
// 1. Executor 执行任务（写/改测试）
// 2. Verifier 独立验证执行结果
// 3. 验证未通过时，Repair 生成修复方案，Executor 执行修复
// 4. 重新验证修复后的结果（最多一次修复机会）
func (m *TestMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	// 第一步：Executor 在当前 Invocation 中写/改测试
	executor := &roles.Executor{Runtime: m.Runtime}
	result, err := executor.Execute(inv)
	if err != nil {
		return "", fmt.Errorf("test mode execute: %w", err)
	}

	// 第二步：Verifier 使用独立上下文验证执行结果
	verifier := &roles.Verifier{Runtime: m.Runtime}
	verifyResult, err := verifier.Verify(inv, task, result)
	if err != nil {
		return result, fmt.Errorf("test mode verify: %w", err)
	}
	if verifyResult.Passed {
		return result, nil
	}

	// 第三步：验证未通过，Repair 生成修复方案，Executor 执行修复
	repair := &roles.Repair{Runtime: m.Runtime}
	repairResult, err := repair.Repair(inv, task, verifyResult)
	if err != nil {
		return result, fmt.Errorf("test mode repair: %w", err)
	}
	appendRepairInstruction(inv, repairResult.FixPlan)
	result, err = executor.Execute(inv)
	if err != nil {
		return "", fmt.Errorf("test mode execute repair: %w", err)
	}

	// 第四步：必须重新验证修复后的执行结果，不能把执行成功当作验证通过
	verifyResult, err = verifier.Verify(inv, task, result)
	if err != nil {
		return result, fmt.Errorf("test mode verify repair: %w", err)
	}
	if !verifyResult.Passed {
		return result, fmt.Errorf("test mode: verification failed after one repair: %s", verifyResult.Summary)
	}
	return result, nil
}

// appendRepairInstruction 将修复方案作为用户消息追加到调用上下文。
func appendRepairInstruction(inv *core.Invocation, fixPlan string) {
	inv.AppendMessage(llm.RoleUser, "## 修复方案\n"+fixPlan)
}
