package workflow

import (
	"fmt"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/agent/roles"
	wf "github.com/xiws/orca/pkg/workflow"
)

// WorkflowRunner 将 WorkflowPlan 编译为 pkg/workflow.Workflow 并执行。
// 它是 Planner 输出和 pkg/workflow 引擎之间的桥梁。
type WorkflowRunner struct {
	engine  *wf.Engine
	runtime *core.Runtime
}

// NewWorkflowRunner 创建 WorkflowRunner。
func NewWorkflowRunner(runtime *core.Runtime) *WorkflowRunner {
	return &WorkflowRunner{runtime: runtime}
}

// Run 将 WorkflowPlan 编译为 workflow 并执行。
//
// 执行流程：
// 1. 将 WorkflowPlan 转换为 workflow.Workflow 定义
// 2. 创建 workflow.Engine
// 3. 从初始节点开始执行
// 4. 根据节点类型调用相应的 Agent（Executor/Verifier/Repair）
func (wr *WorkflowRunner) Run(plan *roles.WorkflowPlan, task *core.Task) error {
	if plan == nil || len(plan.Nodes) == 0 {
		return fmt.Errorf("workflow plan is empty")
	}

	// 构建 workflow 定义
	definition := wr.buildWorkflow(plan)
	wr.engine = wf.NewEngine(definition)

	// 找到初始节点（第一个 execute 节点）
	initialNode := wr.findInitialNode(plan)
	if initialNode == "" {
		return fmt.Errorf("no initial node found in workflow plan")
	}

	// 执行 workflow
	currentState := initialNode
	ctx := wf.Context{
		"task":    task,
		"runtime": wr.runtime,
		"plan":    plan,
	}

	for {
		// 获取当前节点
		node := wr.findNode(plan, currentState)
		if node == nil {
			return fmt.Errorf("node %q not found in plan", currentState)
		}

		// 重试保护：防止 verify→repair→execute 无限循环
		if node.Type == "execute" && task.RetryCount > task.MaxRetries {
			return fmt.Errorf("max retries (%d) exceeded for task", task.MaxRetries)
		}

		// 1) 执行节点业务逻辑
		nodeResult, err := wr.ExecuteNode(node, task)
		if err != nil {
			return fmt.Errorf("node %q execution failed: %w", currentState, err)
		}

		// 2) 将执行结果存入 context
		ctx["node_result"] = nodeResult
		ctx["node_id"] = currentState

		// 3) 将结果映射为 edge action
		action := wr.resolveAction(node, nodeResult)

		// 4) 通过 engine 执行状态流转（Guard/Condition/Hook 校验）
		result, err := wr.engine.Execute(wf.ExecuteRequest{
			InstanceID:   fmt.Sprintf("%d", task.Id),
			CurrentState: currentState,
			Action:       action,
			Context:      ctx,
		})
		if err != nil {
			return fmt.Errorf("transition failed at %q with action %q: %w", currentState, action, err)
		}

		// 5) 检查是否到达终态
		if wr.isTerminal(plan, result.To) {
			return nil
		}

		// 流转到下一状态
		currentState = result.To
	}
}

// buildWorkflow 将 WorkflowPlan 转换为 workflow.Workflow。
func (wr *WorkflowRunner) buildWorkflow(plan *roles.WorkflowPlan) *wf.Workflow {
	w := wf.NewWorkflow("agent-workflow")

	// 注册所有节点为状态
	for _, node := range plan.Nodes {
		state := w.State(node.ID)
		if wr.isTerminal(plan, node.ID) {
			state.Terminal()
		}
	}

	// 注册所有边为流转
	for _, edge := range plan.Edges {
		w.From(edge.From).To(edge.To).Action(edge.Action)
	}

	return w
}

// findInitialNode 找到初始执行节点。
func (wr *WorkflowRunner) findInitialNode(plan *roles.WorkflowPlan) string {
	// 优先找第一个 execute 节点
	for _, node := range plan.Nodes {
		if node.Type == "execute" {
			return node.ID
		}
	}
	// 否则返回第一个节点
	if len(plan.Nodes) > 0 {
		return plan.Nodes[0].ID
	}
	return ""
}

// findNode 在 plan 中查找指定 ID 的节点。
func (wr *WorkflowRunner) findNode(plan *roles.WorkflowPlan, id string) *roles.PlanNode {
	for i := range plan.Nodes {
		if plan.Nodes[i].ID == id {
			return &plan.Nodes[i]
		}
	}
	return nil
}

// resolveAction 将 ExecuteNode 的返回结果映射为 PlanEdge 的 Action。
//
// 映射规则：
//   - execute 节点：成功返回 → "complete"，error → "fail"
//   - verify  节点：返回 "PASS" → "complete"，返回 "FAIL:..." → "fail"
//   - repair  节点：始终 → "complete"（修复后回到 execute）
//   - human   节点：返回 "APPROVED" → "complete"，返回 "REJECTED" → "fail"
func (wr *WorkflowRunner) resolveAction(node *roles.PlanNode, result string) string {
	switch node.Type {
	case "execute":
		return "complete"
	case "verify":
		if result == "PASS" {
			return "complete"
		}
		return "fail"
	case "repair":
		return "complete"
	case "human":
		if result == "APPROVED" {
			return "complete"
		}
		return "fail"
	default:
		return "complete"
	}
}

// isTerminal 检查节点是否为终态。
// 终态节点是没有出边的节点。
func (wr *WorkflowRunner) isTerminal(plan *roles.WorkflowPlan, nodeID string) bool {
	for _, edge := range plan.Edges {
		if edge.From == nodeID {
			return false
		}
	}
	return true
}

// ExecuteNode 执行单个节点。
// 根据节点类型调用相应的 Agent。
func (wr *WorkflowRunner) ExecuteNode(node *roles.PlanNode, task *core.Task) (string, error) {
	switch node.Type {
	case "execute":
		executor := &roles.Executor{Runtime: wr.runtime, Tools: node.Tools}
		result, err := executor.Execute(task)
		if err != nil {
			return "", err
		}
		task.TaskResult = result
		return result, nil
	case "verify":
		verifier := &roles.Verifier{Runtime: wr.runtime}
		result, err := verifier.Verify(task)
		if err != nil {
			return "", err
		}
		if result.Passed {
			return "PASS", nil
		}
		return "FAIL: " + result.Summary, nil
	case "repair":
		repairAgent := &roles.Repair{Runtime: wr.runtime}
		// 从 task 当前状态构造 VerifyResult（失败上下文）
		verifyResult := &roles.VerifyResult{
			Passed:  false,
			Summary: task.TaskResult,
		}
		repairResult, err := repairAgent.Repair(task, verifyResult)
		if err != nil {
			return "", err
		}
		task.RetryCount++
		// 将修复指令追加到 task input 供下次 Execute 使用
		task.Input += "\n\n## Repair Instructions\n" + repairResult.FixPlan
		task.TaskResult = ""
		return repairResult.FixPlan, nil
	case "human":
		hn := NewHumanNode(node.ID, node.Description, 0)
		// 通过 event bus 发布审批请求
		if wr.runtime != nil {
			_ = wr.runtime.Publish(HumanApprovalEvent{
				NodeID:  node.ID,
				Message: node.Description,
				Node:    hn,
			})
		}
		approved, err := hn.Wait()
		if err != nil {
			return "", err
		}
		if !approved {
			return "REJECTED", nil
		}
		return "APPROVED", nil
	default:
		return "", fmt.Errorf("unknown node type: %s", node.Type)
	}
}
