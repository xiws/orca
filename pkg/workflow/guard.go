package workflow

// Guard 用于权限控制。
// Check 根据上下文检查是否允许流转，返回 error 表示拒绝
type Guard interface {
	Check(ctx Context) error
}
