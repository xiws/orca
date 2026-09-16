package workflow

import "fmt"

// Engine 是状态流转执行引擎。
type Engine struct {
	workflow *Workflow
}

// NewEngine 创建一个执行引擎。
func NewEngine(wf *Workflow) *Engine {
	return &Engine{workflow: wf}
}

// ExecuteRequest 描述一次状态流转请求。
type ExecuteRequest struct {
	// InstanceID 当前实例 ID。
	InstanceID string

	// CurrentState 当前状态。
	CurrentState string

	// Action 执行动作。
	Action string

	// Context 执行上下文。
	Context Context
}

// ExecuteResult 描述一次状态流转的结果。
type ExecuteResult struct {
	// From 原状态。
	From string

	// To 新状态。
	To string

	// Action 执行动作。
	Action string

	// TransitionID 流转标识。
	TransitionID string
}

// Execute 执行一次状态流转。
//
// 执行流程：
//  1. 验证当前状态存在
//  2. 查找匹配的 Transition（From + Action）
//  3. 执行 Guard 检查
//  4. 执行 Condition 评估
//  5. 执行 Before Hook
//  6. 状态流转
//  7. 执行 After Hook
//  8. 返回结果
func (e *Engine) Execute(req ExecuteRequest) (ExecuteResult, error) {
	// 1. 验证当前状态
	state, exists := e.workflow.states[req.CurrentState]
	if !exists {
		return ExecuteResult{}, fmt.Errorf("%w: %s", ErrStateNotFound, req.CurrentState)
	}

	// 2. 查找匹配的 Transition
	transition := e.findTransition(req.CurrentState, req.Action)
	if transition == nil {
		return ExecuteResult{}, fmt.Errorf("%w: %s + %s", ErrInvalidTransition, req.CurrentState, req.Action)
	}

	ctx := req.Context
	if ctx == nil {
		ctx = make(Context)
	}

	// 3. 执行 Guard
	for _, guard := range transition.Guards {
		if err := guard.Check(ctx); err != nil {
			return ExecuteResult{}, fmt.Errorf("%w: %v", ErrForbidden, err)
		}
	}

	// 4. 执行 Condition
	for _, cond := range transition.Conditions {
		if !cond.Evaluate(ctx) {
			return ExecuteResult{}, ErrConditionNotMet
		}
	}

	// 5. 执行 Before Hook
	for _, hook := range transition.Hooks {
		if err := hook.Before(ctx); err != nil {
			return ExecuteResult{}, fmt.Errorf("before hook failed: %w", err)
		}
	}

	// 6. 状态流转
	result := ExecuteResult{
		From:   req.CurrentState,
		To:     transition.To,
		Action: req.Action,
	}

	// 生成 TransitionID
	if req.InstanceID != "" {
		result.TransitionID = fmt.Sprintf("%s:%s->%s", req.InstanceID, result.From, result.To)
	}

	// 7. 执行 After Hook
	for _, hook := range transition.Hooks {
		if err := hook.After(ctx); err != nil {
			return result, fmt.Errorf("after hook failed: %w", err)
		}
	}

	_ = state // 状态已验证存在

	return result, nil
}

// findTransition 查找匹配的 Transition。
func (e *Engine) findTransition(from, action string) *Transition {
	for _, t := range e.workflow.transitions {
		if t.From == from && t.Action == action {
			return t
		}
	}
	return nil
}

// RunRequest 描述一次自动执行请求。
type RunRequest struct {
	// InitialState 初始状态。
	InitialState string

	// Context 执行上下文，在 Handler 和 Guard/Condition/Hook 间共享。
	Context Context

	// InstanceID 当前实例 ID（可选）。
	InstanceID string
}

// RunResult 描述一次自动执行的结果。
type RunResult struct {
	// FinalState 最终状态。
	FinalState string

	// History 完整流转历史。
	History []ExecuteResult
}

// Run 自动驱动状态流转，从 InitialState 开始循环执行：
//
//	进入状态 → 调用 Handler.Handle → 用返回的 action 查找 Transition
//	→ Guard/Condition/Hook 校验 → 流转到下一状态 → 重复直到终态
//
// 终态节点的 Handler 不会被调用。
func (e *Engine) Run(req RunRequest) (RunResult, error) {
	currentState := req.InitialState
	ctx := req.Context
	if ctx == nil {
		ctx = make(Context)
	}

	var history []ExecuteResult

	for {
		state, exists := e.workflow.states[currentState]
		if !exists {
			return RunResult{FinalState: currentState, History: history},
				fmt.Errorf("%w: %s", ErrStateNotFound, currentState)
		}

		// 终态退出
		if state.Terminal {
			return RunResult{FinalState: currentState, History: history}, nil
		}

		// 非终态无 Handler → 配置错误
		if state.Handler == nil {
			return RunResult{FinalState: currentState, History: history},
				fmt.Errorf("state %q has no handler and is not terminal", currentState)
		}

		// 执行节点业务逻辑
		action, err := state.Handler.Handle(ctx)
		if err != nil {
			return RunResult{FinalState: currentState, History: history},
				fmt.Errorf("handler failed at %q: %w", currentState, err)
		}

		// 用 action 查找 transition 并执行流转（复用 Execute 逻辑）
		result, err := e.Execute(ExecuteRequest{
			InstanceID:   req.InstanceID,
			CurrentState: currentState,
			Action:       action,
			Context:      ctx,
		})
		if err != nil {
			return RunResult{FinalState: currentState, History: history}, err
		}

		history = append(history, result)
		currentState = result.To
	}
}
