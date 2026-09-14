package workflow

// Hook 是状态流转的生命周期事件。
type Hook interface {
	Before(ctx Context) error
	After(ctx Context) error
}
