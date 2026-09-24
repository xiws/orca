//go:build !unix

// 非 Unix 平台的 bash 工具存根，不支持进程组管理和目录句柄。
package tools

import (
	"context"
	"fmt"

	"github.com/xiws/orca/internal/model"
)

const nonblock = 0

// bash 在非 Unix 平台上返回错误
func (g *Gateway) bash(context.Context, model.Call, map[string]any) (Result, int, error) {
	return Result{}, -1, fmt.Errorf("bash with directory handles and process-group cancellation requires Unix")
}
