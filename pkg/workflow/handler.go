package workflow

// Handler 是状态节点的业务处理逻辑。
// 状态进入时由 Engine.Run 调用，返回的 action 决定走哪条 transition。
// Handle 执行节点业务逻辑，返回要执行的动作名称和可能的错误
type Handler interface {
	Handle(ctx Context) (action string, err error)
}
