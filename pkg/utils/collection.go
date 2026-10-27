// Package utils 提供通用工具函数集合。
package utils

// Select 对切片进行映射转换，将每个元素通过函数转换为新的类型
func Select[T any, R any](list []T, fun func(item T) R) []R {
	var result []R
	for _, item := range list {
		result = append(result, fun(item))
	}
	return result
}
