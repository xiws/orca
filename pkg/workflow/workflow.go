package workflow

// Workflow 定义一个有向状态图。
type Workflow struct {
	name        string
	states      map[string]*State
	transitions []*Transition
}

// NewWorkflow 创建一个命名流程。
func NewWorkflow(name string) *Workflow {
	return &Workflow{
		name:   name,
		states: make(map[string]*State),
	}
}

// State 注册一个状态节点，返回 StateBuilder 用于链式配置。
// 如果状态名称已存在，返回的 builder 会覆盖原有状态。
func (w *Workflow) State(name string) *StateBuilder {
	if _, exists := w.states[name]; exists {
		return &StateBuilder{state: w.states[name], workflow: w}
	}
	state := &State{Name: name}
	w.states[name] = state
	return &StateBuilder{state: state, workflow: w}
}

// From 创建一个流转定义，返回 TransitionBuilder 用于链式配置。
func (w *Workflow) From(state string) *TransitionBuilder {
	return &TransitionBuilder{
		transition: &Transition{From: state},
		workflow:   w,
	}
}

// States 返回所有已注册的状态。
func (w *Workflow) States() map[string]*State {
	return w.states
}

// Transitions 返回所有已注册的流转。
func (w *Workflow) Transitions() []*Transition {
	return w.transitions
}
