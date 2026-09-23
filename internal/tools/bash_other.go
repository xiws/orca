//go:build !unix

package tools

import (
	"context"
	"fmt"

	"github.com/xiws/orca/internal/model"
)

const nonblock = 0

func (g *Gateway) bash(context.Context, model.Call, map[string]any) (Result, int, error) {
	return Result{}, -1, fmt.Errorf("bash with directory handles and process-group cancellation requires Unix")
}
