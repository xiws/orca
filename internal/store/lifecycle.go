package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/xiws/orca/internal/domain"
)

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

// HasUnresolved reports uncertain execution in a run tree, or all runs when
// rootID is zero. Active model/tool calls are not unresolved unless their run
// is reconciling or stopped. Only scalar columns are read, never transcripts.
func (s *Store) HasUnresolved(ctx context.Context, rootID domain.RunID) (bool, error) {
	var unresolved bool
	if err := s.db.QueryRowContext(ctx, hasUnresolvedSQL, rootID, rootID).Scan(&unresolved); err != nil {
		return false, storageError(err)
	}
	return unresolved, nil
}

func (s *Store) Tools(ctx context.Context, runID domain.RunID) ([]domain.ToolExecution, error) {
	return readMany[domain.ToolExecution](ctx, s.db, "SELECT data FROM tools WHERE run_id = ? ORDER BY key", runID)
}

func (s *Store) Inputs(ctx context.Context, runID domain.RunID) ([]domain.InputRequest, error) {
	return readMany[domain.InputRequest](ctx, s.db, "SELECT data FROM inputs WHERE run_id = ? ORDER BY id", runID)
}

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
	var imports int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'imported_sessions'").Scan(&imports); err != nil {
		return err
	}
	for _, id := range ids {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM sessions WHERE id = ?", id).Scan(&exists); err != nil {
			return storageError(err)
		}
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
		if imports > 0 {
			if _, err := tx.ExecContext(ctx, "DELETE FROM imported_sessions WHERE session_id = ?", id); err != nil {
				return err
			}
		}
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
