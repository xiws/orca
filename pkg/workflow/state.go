package workflow

// State 表示一个状态节点。
type State struct {
	// Name 状态唯一标识。
	Name string

	// Terminal 是否为终态。
	Terminal bool

	// Handler 节点业务处理逻辑。
	// Engine.Run 进入该状态时调用；终态节点可为 nil。
	Handler Handler
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

// Handler 设置状态的业务处理逻辑。
// Engine.Run 进入该状态时调用 Handler.Handle，返回的 action 决定走哪条 transition。
func (b *StateBuilder) Handler(h Handler) *StateBuilder {
	b.state.Handler = h
	return b
}
