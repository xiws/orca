package workflow

// Transition 表示状态之间的一条流转路径。
type Transition struct {
	// From 起始状态。
	From string

	// To 目标状态。
	To string

	// Action 用户执行动作。
	Action string

	// Conditions 条件列表。
	Conditions []Condition

	// Guards 权限控制列表。
	Guards []Guard

	// Hooks 生命周期事件列表。
	Hooks []Hook
}

// TransitionBuilder 提供链式 API 注册流转。
type TransitionBuilder struct {
	transition *Transition
	workflow   *Workflow
}

// To 设置目标状态。
func (b *TransitionBuilder) To(state string) *TransitionBuilder {
	b.transition.To = state
	return b
}

// Action 设置执行动作并将流转注册到 Workflow。
func (b *TransitionBuilder) Action(action string) *TransitionBuilder {
	b.transition.Action = action
	b.workflow.transitions = append(b.workflow.transitions, b.transition)
	return b
}

// Guard 添加权限控制。
func (b *TransitionBuilder) Guard(guard Guard) *TransitionBuilder {
	b.transition.Guards = append(b.transition.Guards, guard)
	return b
}

// Condition 添加业务条件。
func (b *TransitionBuilder) Condition(cond Condition) *TransitionBuilder {
	b.transition.Conditions = append(b.transition.Conditions, cond)
	return b
}

// Hook 添加生命周期事件。
func (b *TransitionBuilder) Hook(hook Hook) *TransitionBuilder {
	b.transition.Hooks = append(b.transition.Hooks, hook)
	return b
}
