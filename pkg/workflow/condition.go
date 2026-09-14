package workflow

// Condition 用于业务条件控制。
type Condition interface {
	Evaluate(ctx Context) bool
}
