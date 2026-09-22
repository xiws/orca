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

func (m *TestMode) Name() string { return "test" }

func (m *TestMode) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	// 1. Executor 在当前 Invocation 中写/改测试。
	executor := &roles.Executor{Runtime: m.Runtime}
	result, err := executor.Execute(inv)
	if err != nil {
		return "", fmt.Errorf("test mode execute: %w", err)
	}

	// 2. Verifier 使用独立上下文验证显式传入的执行结果。
	verifier := &roles.Verifier{Runtime: m.Runtime}
	verifyResult, err := verifier.Verify(inv, task, result)
	if err != nil {
		return result, fmt.Errorf("test mode verify: %w", err)
	}
	if verifyResult.Passed {
		return result, nil
	}

	// 3. 验证未通过：仅尝试一次修复，计划追加为消息，不修改 Task.Input。
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

	// 4. 必须重新验证修复后的执行结果，不能把执行成功当作验证通过。
	verifyResult, err = verifier.Verify(inv, task, result)
	if err != nil {
		return result, fmt.Errorf("test mode verify repair: %w", err)
	}
	if !verifyResult.Passed {
		return result, fmt.Errorf("test mode: verification failed after one repair: %s", verifyResult.Summary)
	}
	return result, nil
}

func appendRepairInstruction(inv *core.Invocation, fixPlan string) {
	inv.AppendMessage(llm.RoleUser, "## 修复方案\n"+fixPlan)
}
