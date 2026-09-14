package workflow

// Guard 用于权限控制。
type Guard interface {
	Check(ctx Context) error
}
