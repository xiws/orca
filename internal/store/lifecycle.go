package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/xiws/orca/internal/domain"
)

// hasUnresolvedSQL 查询是否存在不确定的执行状态。
const hasUnresolvedSQL = `SELECT EXISTS (
 SELECT 1 FROM runs AS r
 WHERE (? = 0 OR r.root_run_id = ?)
 AND (
  r.state = 'reconciling'
  OR (
   r.state IN ('interrupted', 'succeeded', 'failed', 'cancelled')
   AND (
    EXISTS (SELECT 1 FROM invocations AS i WHERE i.run_id = r.id AND i.phase = 'model_started')
    OR EXISTS (SELECT 1 FROM tools AS t WHERE t.run_id = r.id AND t.state IN ('started', 'unknown'))
   )
  )
 )
)`

// HasUnresolved 检查运行树中是否存在不确定的执行状态。
// rootID 为 0 时检查所有运行。活跃的模型/工具调用在其运行处于 reconciling
// 或停止状态之前不算未解决。仅读取标量列，不读取转录数据。
func (s *Store) HasUnresolved(ctx context.Context, rootID domain.RunID) (bool, error) {
	var unresolved bool
	if err := s.db.QueryRowContext(ctx, hasUnresolvedSQL, rootID, rootID).Scan(&unresolved); err != nil {
		return false, storageError(err)
	}
	return unresolved, nil
}

// Tools 返回指定运行的所有工具执行记录。
func (s *Store) Tools(ctx context.Context, runID domain.RunID) ([]domain.ToolExecution, error) {
	return readMany[domain.ToolExecution](ctx, s.db, "SELECT data FROM tools WHERE run_id = ? ORDER BY key", runID)
}

// Inputs 返回指定运行的所有输入请求记录。
func (s *Store) Inputs(ctx context.Context, runID domain.RunID) ([]domain.InputRequest, error) {
	return readMany[domain.InputRequest](ctx, s.db, "SELECT data FROM inputs WHERE run_id = ? ORDER BY id", runID)
}

// DeleteSessions 按顺序删除一个或多个会话及其所有关联数据。
// 如果会话存在活跃或未解决的执行则拒绝删除。
func (s *Store) DeleteSessions(ctx context.Context, ids []domain.SessionID) error {
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
	// 检查导入表是否存在。
	var imports int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'imported_sessions'").Scan(&imports); err != nil {
		return err
	}
	for _, id := range ids {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM sessions WHERE id = ?", id).Scan(&exists); err != nil {
			return storageError(err)
		}
		// 检查是否有活跃或未解决的执行，有则拒绝删除。
		var blocked int
		if err := tx.QueryRowContext(ctx, `SELECT
   (SELECT count(*) FROM runs WHERE session_id = ? AND state NOT IN ('succeeded','failed','cancelled')) +
   (SELECT count(*) FROM tools WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?) AND state IN ('started','unknown')) +
   (SELECT count(*) FROM invocations WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?) AND phase = 'model_started')`, id, id, id).Scan(&blocked); err != nil {
			return err
		}
		if blocked != 0 {
			return fmt.Errorf("session %d has active or unresolved execution", id)
		}
		// 清理导入记录。
		if imports > 0 {
			if _, err := tx.ExecContext(ctx, "DELETE FROM imported_sessions WHERE session_id = ?", id); err != nil {
				return err
			}
		}
		// 按外键依赖顺序删除所有关联数据。
		statements := []string{
			"DELETE FROM invocation_messages WHERE invocation_id IN (SELECT id FROM invocations WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?))",
			"DELETE FROM delegation_children WHERE delegation_key IN (SELECT key FROM delegations WHERE parent_run_id IN (SELECT id FROM runs WHERE session_id = ?))",
			"DELETE FROM delegations WHERE parent_run_id IN (SELECT id FROM runs WHERE session_id = ?)",
			"DELETE FROM events WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?)",
			"DELETE FROM artifacts WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?)",
			"DELETE FROM tools WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?)",
			"DELETE FROM inputs WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?)",
			"DELETE FROM invocations WHERE run_id IN (SELECT id FROM runs WHERE session_id = ?)",
			"DELETE FROM runs WHERE session_id = ?",
			"DELETE FROM session_messages WHERE session_id = ?",
			"DELETE FROM task_ids WHERE id IN (SELECT id FROM tasks WHERE session_id = ?)",
			"DELETE FROM tasks WHERE session_id = ?",
			"DELETE FROM sessions WHERE id = ?",
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement, id); err != nil {
				return storageError(err)
			}
		}
	}
	return storageError(tx.Commit())
}
