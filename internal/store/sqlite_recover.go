package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/xiws/orca/internal/domain"
)

// Recover 在启动时恢复崩溃遗留的不一致状态。
// 不会重试外部工作；终态 Run 上的未决外部工作会在事务提交后
// 以 ErrUnknown 上报，而不会静默地恢复或忽略。
func (s *Store) Recover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return sql.ErrConnDone
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	// 查找处于 started 或 unknown 状态的工具执行记录
	tools, err := readMany[domain.ToolExecution](ctx, tx, "SELECT data FROM tools WHERE state IN ('started','unknown') ORDER BY key")
	if err != nil {
		return err
	}
	// 标记受影响的 Run，将 started 工具转为 unknown
	uncertain := make(map[domain.RunID]bool)
	mutation := domain.Mutation{}
	for i := range tools {
		tool := &tools[i]
		uncertain[tool.RunID] = true
		if tool.State == "started" {
			tool.State = "unknown"
			mutation.Tools = append(mutation.Tools, tool)
		}
	}
	// 查找处于 model_started 阶段的 Invocation，标记其 Run 为不确定
	invocations, err := readMany[domain.Invocation](ctx, tx, "SELECT data FROM invocations WHERE phase = 'model_started'")
	if err != nil {
		return err
	}
	for _, invocation := range invocations {
		uncertain[invocation.RunID] = true
	}
	// 加载所有 Run，确定每个根 Run 的恢复目标状态
	runs, err := readMany[domain.Run](ctx, tx, "SELECT data FROM runs ORDER BY id")
	if err != nil {
		return err
	}
	uncertainRoots := make(map[domain.RunID]bool)   // 含有不确定外部工作的根 Run
	interruptedRoots := make(map[domain.RunID]bool) // 含有中断状态的根 Run
	for _, run := range runs {
		if uncertain[run.ID] || run.State == domain.Reconciling {
			uncertainRoots[run.RootRunID] = true
		}
		if run.State == domain.Running || run.State == domain.Interrupted {
			interruptedRoots[run.RootRunID] = true
		}
	}
	var terminalUnknown []domain.RunID // 终态 Run 上存在不确定外部工作的记录
	for i := range runs {
		run := &runs[i]
		if run.State.Terminal() {
			// 终态 Run 不再转换状态，仅记录不一致情况
			if uncertain[run.ID] {
				terminalUnknown = append(terminalUnknown, run.ID)
			}
			continue
		}
		// 根据不确定性和当前状态确定恢复目标状态
		target := run.State
		switch {
		case uncertain[run.ID] || (run.ID == run.RootRunID && uncertainRoots[run.ID]):
			target = domain.Reconciling
		case run.ID == run.RootRunID && run.State == domain.Waiting && interruptedRoots[run.ID]:
			target = domain.Interrupted
		case run.State == domain.Running:
			target = domain.Interrupted
		case run.State == domain.Cancelling:
			target = domain.Cancelled
		}
		if target == run.State {
			continue
		}
		// Queued -> reconciling 不是合法领域转换，需先转为 interrupted 再转 reconciling
		if run.State == domain.Queued && target == domain.Reconciling {
			run.State = domain.Interrupted
			if _, err := applyMutation(ctx, tx, domain.Mutation{Runs: []*domain.Run{run}}); err != nil {
				return storageError(err)
			}
			persisted, err := readOne[domain.Run](ctx, tx, "SELECT data FROM runs WHERE id = ?", run.ID)
			if err != nil {
				return err
			}
			run = persisted
		}
		run.State = target
		mutation.Runs = append(mutation.Runs, run)
	}
	if _, err := applyMutation(ctx, tx, mutation); err != nil {
		return storageError(err)
	}
	if err := tx.Commit(); err != nil {
		return storageError(err)
	}
	if len(terminalUnknown) > 0 {
		return fmt.Errorf("%w: terminal runs %v", domain.ErrUnknown, terminalUnknown)
	}
	return nil
}
