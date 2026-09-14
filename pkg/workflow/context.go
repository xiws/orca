// Package workflow 提供可编程有向状态流转引擎。
package workflow

// Context 是执行上下文，用于在 Guard/Condition/Hook 间传递数据。
type Context map[string]any
