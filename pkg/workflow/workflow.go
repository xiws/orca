package workflow

// Workflow 定义一个有向状态图。
type Workflow struct {
	name        string
	states      map[string]*State
	transitions []*Transition
}

// NewWorkflow 创建一个命名工作流实例
func NewWorkflow(name string) *Workflow {
	return &Workflow{
		name:   name,
		states: make(map[string]*State),
	}
}

// State 注册一个状态节点，返回 StateBuilder 用于链式配置。
// 如果状态名称已存在，返回的 builder 会覆盖原有状态。
func (w *Workflow) State(name string) *StateBuilder {
	// 检查状态是否已存在，若存在则复用
	if _, exists := w.states[name]; exists {
		return &StateBuilder{state: w.states[name], workflow: w}
	}
	// 创建新状态并注册
	state := &State{Name: name}
	w.states[name] = state
	return &StateBuilder{state: state, workflow: w}
}

// From 创建一个流转定义，返回 TransitionBuilder 用于链式配置。
// 需要继续调用 To 和 Action 完成流转定义
func (w *Workflow) From(state string) *TransitionBuilder {
	return &TransitionBuilder{
		transition: &Transition{From: state},
		workflow:   w,
	}
}

// States 返回所有已注册的状态映射
func (w *Workflow) States() map[string]*State {
	return w.states
}

// Transitions 返回所有已注册的流转列表
func (w *Workflow) Transitions() []*Transition {
	return w.transitions
}
