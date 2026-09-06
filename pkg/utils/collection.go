package utils

func Select[T any, R any](list []T, fun func(item T) R) []R {
	var result []R
	for _, item := range list {
		result = append(result, fun(item))
	}
	return result
}
