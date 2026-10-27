package workflow

// Hook 是状态流转的生命周期事件。
type Hook interface {
	// Before 在状态流转前执行
	Before(ctx Context) error
	// After 在状态流转后执行
	After(ctx Context) error
}
