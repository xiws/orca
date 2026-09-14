package workflow

// State 表示一个状态节点。
type State struct {
	// Name 状态唯一标识。
	Name string

	// Terminal 是否为终态。
	Terminal bool
}

// StateBuilder 提供链式 API 注册状态。
type StateBuilder struct {
	state    *State
	workflow *Workflow
}

// Terminal 标记状态为终态。
func (b *StateBuilder) Terminal() *StateBuilder {
	b.state.Terminal = true
	return b
}
