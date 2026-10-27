package utils

import (
	"os"
)

// GetEnv 获取环境变量的值
func GetEnv(key string) string {
	return os.Getenv(key)
}
