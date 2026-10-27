package workflow

// Condition 用于业务条件控制。
// Evaluate 根据上下文评估条件是否满足，返回 true 表示满足
type Condition interface {
	Evaluate(ctx Context) bool
}
