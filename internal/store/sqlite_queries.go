package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readOne[T any](ctx context.Context, q queryer, query string, args ...any) (*T, error) {
	var data []byte
	if err := q.QueryRowContext(ctx, query, args...).Scan(&data); err != nil {
		return nil, storageError(err)
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func readMany[T any](ctx context.Context, q queryer, query string, args ...any) ([]T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	values := make([]T, 0)
	for rows.Next() {
		var data []byte
		var value T
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadSessionMessages(ctx context.Context, q queryer, session *domain.Session) error {
	messages, err := readMany[domain.Message](ctx, q, "SELECT data FROM session_messages WHERE session_id = ? ORDER BY sequence", session.ID)
	if err == nil {
		session.Messages = messages
	}
	return err
}

func loadInvocationMessages(ctx context.Context, q queryer, invocation *domain.Invocation) error {
	messages, err := readMany[model.Message](ctx, q, "SELECT data FROM invocation_messages WHERE invocation_id = ? AND context_version = ? ORDER BY sequence", invocation.ID, invocation.Thread.ContextVersion)
	if err == nil {
		invocation.Thread.Messages = messages
	}
	return err
}

func (s *Store) Session(ctx context.Context, id domain.SessionID) (*domain.Session, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback()
	session, err := readOne[domain.Session](ctx, tx, "SELECT data FROM sessions WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if err := loadSessionMessages(ctx, tx, session); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return session, nil
}

// SessionMessages pages by the one-based append sequence (not message ID).
// Session and Sessions still return the complete history for existing callers.
func (s *Store) SessionMessages(ctx context.Context, id domain.SessionID, after int64, limit int) ([]domain.Message, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM sessions WHERE id = ?", id).Scan(&exists); err != nil {
		return nil, storageError(err)
	}
	if limit <= 0 {
		limit = 100
	}
	messages, err := readMany[domain.Message](ctx, tx, "SELECT data FROM session_messages WHERE session_id = ? AND sequence > ? ORDER BY sequence LIMIT ?", id, after, limit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return messages, nil
}

func (s *Store) Task(ctx context.Context, id domain.TaskID, version int) (*domain.Task, error) {
	if version == 0 {
		return readOne[domain.Task](ctx, s.db, "SELECT data FROM tasks WHERE id = ? ORDER BY version DESC LIMIT 1", id)
	}
	return readOne[domain.Task](ctx, s.db, "SELECT data FROM tasks WHERE id = ? AND version = ?", id, version)
}

func (s *Store) Run(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	return readOne[domain.Run](ctx, s.db, "SELECT data FROM runs WHERE id = ?", id)
}

func (s *Store) Invocation(ctx context.Context, id domain.InvocationID) (*domain.Invocation, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback()
	invocation, err := readOne[domain.Invocation](ctx, tx, "SELECT data FROM invocations WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if err := loadInvocationMessages(ctx, tx, invocation); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return invocation, nil
}

func (s *Store) Input(ctx context.Context, id domain.InputRequestID) (*domain.InputRequest, error) {
	return readOne[domain.InputRequest](ctx, s.db, "SELECT data FROM inputs WHERE id = ?", id)
}

func (s *Store) InputByCall(ctx context.Context, key string) (*domain.InputRequest, error) {
	if key == "" {
		return nil, domain.ErrNotFound
	}
	return readOne[domain.InputRequest](ctx, s.db, "SELECT data FROM inputs WHERE call_key = ?", key)
}

func (s *Store) Tool(ctx context.Context, key string) (*domain.ToolExecution, error) {
	return readOne[domain.ToolExecution](ctx, s.db, "SELECT data FROM tools WHERE key = ?", key)
}

func (s *Store) Delegation(ctx context.Context, key string) (*domain.Delegation, error) {
	return readOne[domain.Delegation](ctx, s.db, "SELECT data FROM delegations WHERE key = ?", key)
}

func (s *Store) Runs(ctx context.Context) ([]domain.Run, error) {
	return readMany[domain.Run](ctx, s.db, "SELECT data FROM runs ORDER BY id")
}

func (s *Store) Sessions(ctx context.Context) ([]domain.Session, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback()
	sessions, err := readMany[domain.Session](ctx, tx, "SELECT data FROM sessions ORDER BY id")
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		if err := loadSessionMessages(ctx, tx, &sessions[i]); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return sessions, nil
}

func (s *Store) Events(ctx context.Context, runID domain.RunID, after int64, limit int) ([]domain.Event, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, "SELECT sequence, run_id, COALESCE(invocation_id, 0), assistant_sequence, kind, content FROM events WHERE run_id = ? AND sequence > ? ORDER BY sequence LIMIT ?", runID, after, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	events := make([]domain.Event, 0)
	for rows.Next() {
		var event domain.Event
		if err := rows.Scan(&event.Sequence, &event.RunID, &event.InvocationID, &event.AssistantSequence, &event.Kind, &event.Content); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) Invocations(ctx context.Context, runID domain.RunID) ([]domain.Invocation, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback()
	invocations, err := readMany[domain.Invocation](ctx, tx, "SELECT data FROM invocations WHERE run_id = ? ORDER BY id", runID)
	if err != nil {
		return nil, err
	}
	for i := range invocations {
		if err := loadInvocationMessages(ctx, tx, &invocations[i]); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, storageError(err)
	}
	return invocations, nil
}

func (s *Store) Artifacts(ctx context.Context, runID domain.RunID) ([]domain.Artifact, error) {
	return readMany[domain.Artifact](ctx, s.db, "SELECT data FROM artifacts WHERE run_id = ? ORDER BY id", runID)
}
