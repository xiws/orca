package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

func (s *Store) Apply(ctx context.Context, m domain.Mutation) error {
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
	committed, err := applyMutation(ctx, tx, m)
	if err != nil {
		return storageError(err)
	}
	if err := tx.Commit(); err != nil {
		return storageError(err)
	}
	for _, update := range committed {
		update()
	}
	return nil
}

// Callbacks update caller-owned revisions only after a successful COMMIT,
// including deferred foreign-key validation.
func applyMutation(ctx context.Context, tx *sql.Tx, m domain.Mutation) ([]func(), error) {
	var committed []func()
	for _, session := range m.Sessions {
		if session == nil {
			return nil, fmt.Errorf("nil session")
		}
		next := *session
		next.Version++
		type metadata domain.Session
		data := struct {
			*metadata
			Messages []domain.Message `json:"messages,omitempty"`
		}{metadata: (*metadata)(&next)}
		if err := putCAS(ctx, tx, "sessions", "id", session.ID, session.Version, data, nil, nil); err != nil {
			return nil, err
		}
		if err := appendSessionMessages(ctx, tx, session); err != nil {
			return nil, err
		}
		committed = append(committed, func() { session.Version = next.Version })
	}
	for _, task := range m.Tasks {
		if task == nil {
			return nil, fmt.Errorf("nil task")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO task_ids(id) VALUES(?) ON CONFLICT(id) DO NOTHING", task.ID); err != nil {
			return nil, err
		}
		_, err := putImmutable(ctx, tx, "tasks", "id = ? AND version = ?", []any{task.ID, task.Version},
			[]string{"id", "version", "session_id", "parent_task_id"}, []any{task.ID, task.Version, optionalID(task.SessionID), optionalID(task.ParentTaskID)}, task)
		if err != nil {
			return nil, err
		}
	}
	// Release active-task uniqueness before inserting a replacement run, regardless
	// of the caller's slice order. The entire handoff is still one transaction.
	runs := make([]*domain.Run, 0, len(m.Runs))
	for _, run := range m.Runs {
		if run == nil {
			return nil, fmt.Errorf("nil run")
		}
		if run.State.Terminal() {
			runs = append(runs, run)
		}
	}
	for _, run := range m.Runs {
		if !run.State.Terminal() {
			runs = append(runs, run)
		}
	}
	for _, run := range runs {
		old, err := readOne[domain.Run](ctx, tx, "SELECT data FROM runs WHERE id = ?", run.ID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
		if old != nil {
			if old.Version != run.Version {
				return nil, domain.ErrConflict
			}
			if !domain.CanTransition(old.State, run.State) {
				return nil, fmt.Errorf("illegal run transition %s -> %s", old.State, run.State)
			}
			if old.TaskID != run.TaskID || old.TaskVersion != run.TaskVersion {
				return nil, fmt.Errorf("run task version is immutable: %w", domain.ErrConflict)
			}
		}
		next := *run
		next.Version++
		err = putCAS(ctx, tx, "runs", "id", run.ID, run.Version, &next,
			[]string{"task_id", "task_version", "session_id", "root_run_id", "parent_run_id", "waiting_id", "state"},
			[]any{run.TaskID, run.TaskVersion, optionalID(run.SessionID), optionalID(run.RootRunID), optionalID(run.ParentRunID), optionalID(run.WaitingID), run.State})
		if err != nil {
			return nil, err
		}
		committed = append(committed, func() { run.Version = next.Version })
	}
	for _, invocation := range m.Invocations {
		if invocation == nil {
			return nil, fmt.Errorf("nil invocation")
		}
		old, err := readOne[domain.Invocation](ctx, tx, "SELECT data FROM invocations WHERE id = ?", invocation.ID)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
		if old != nil {
			if old.Version != invocation.Version {
				return nil, domain.ErrConflict
			}
			if old.RunID != invocation.RunID || invocation.Thread.ContextVersion < old.Thread.ContextVersion {
				return nil, fmt.Errorf("invocation owner or archived context cannot change: %w", domain.ErrConflict)
			}
		}
		next := *invocation
		next.Version++
		type metadata domain.Invocation
		type threadMetadata struct {
			ContextVersion int           `json:"context_version"`
			SequenceOffset int           `json:"sequence_offset"`
			Cursor         *model.Cursor `json:"cursor,omitempty"`
		}
		data := struct {
			*metadata
			Thread threadMetadata `json:"thread"`
		}{metadata: (*metadata)(&next), Thread: threadMetadata{invocation.Thread.ContextVersion, invocation.Thread.SequenceOffset, invocation.Thread.Cursor}}
		if err := putCAS(ctx, tx, "invocations", "id", invocation.ID, invocation.Version, data,
			[]string{"run_id", "context_version", "phase"}, []any{invocation.RunID, invocation.Thread.ContextVersion, invocation.Phase}); err != nil {
			return nil, err
		}
		if err := appendInvocationMessages(ctx, tx, invocation); err != nil {
			return nil, err
		}
		committed = append(committed, func() { invocation.Version = next.Version })
	}
	for _, input := range m.Inputs {
		if input == nil {
			return nil, fmt.Errorf("nil input")
		}
		next := *input
		next.Version++
		if err := putCAS(ctx, tx, "inputs", "id", input.ID, input.Version, &next,
			[]string{"run_id", "invocation_id", "call_key"}, []any{input.RunID, input.InvocationID, input.CallKey}); err != nil {
			return nil, err
		}
		committed = append(committed, func() { input.Version = next.Version })
	}
	for _, tool := range m.Tools {
		if tool == nil {
			return nil, fmt.Errorf("nil tool")
		}
		next := *tool
		next.Version++
		if err := putCAS(ctx, tx, "tools", "key", tool.Key, tool.Version, &next,
			[]string{"run_id", "invocation_id", "state"}, []any{tool.RunID, tool.InvocationID, tool.State}); err != nil {
			return nil, err
		}
		committed = append(committed, func() { tool.Version = next.Version })
	}
	for _, delegation := range m.Delegations {
		if delegation == nil {
			return nil, fmt.Errorf("nil delegation")
		}
		inserted, err := putImmutable(ctx, tx, "delegations", "key = ?", []any{delegation.Key},
			[]string{"key", "parent_run_id", "invocation_id"}, []any{delegation.Key, delegation.ParentRunID, delegation.InvocationID}, delegation)
		if err != nil {
			return nil, err
		}
		if inserted {
			for i, child := range delegation.Children {
				if _, err := tx.ExecContext(ctx, "INSERT INTO delegation_children(delegation_key, sequence, child_run_id) VALUES(?,?,?)", delegation.Key, i+1, child); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, artifact := range m.Artifacts {
		if artifact == nil {
			return nil, fmt.Errorf("nil artifact")
		}
		if _, err := putImmutable(ctx, tx, "artifacts", "id = ?", []any{artifact.ID},
			[]string{"id", "run_id", "invocation_id"}, []any{artifact.ID, artifact.RunID, optionalID(artifact.InvocationID)}, artifact); err != nil {
			return nil, err
		}
	}
	for i := range m.Events {
		event := m.Events[i]
		result, err := tx.ExecContext(ctx, "INSERT INTO events(sequence, run_id, invocation_id, assistant_sequence, kind, content) VALUES(?,?,?,?,?,?)",
			optionalID(event.Sequence), event.RunID, optionalID(event.InvocationID), event.AssistantSequence, event.Kind, event.Content)
		if err != nil {
			return nil, err
		}
		sequence, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		committed = append(committed, func() { m.Events[i].Sequence = sequence })
	}
	return committed, nil
}

func putCAS(ctx context.Context, tx *sql.Tx, table, keyColumn string, key any, version int64, value any, columns []string, values []any) error {
	if version < 0 || version == math.MaxInt64 {
		return domain.ErrConflict
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	columns = append(append([]string{}, columns...), "version", "data")
	values = append(append([]any{}, values...), version+1, string(data))
	if version == 0 {
		columns = append(columns, keyColumn)
		values = append(values, key)
		_, err := tx.ExecContext(ctx, insertSQL(table, columns), values...)
		return err
	}
	sets := make([]string, len(columns))
	for i, column := range columns {
		sets[i] = column + " = ?"
	}
	values = append(values, key, version)
	result, err := tx.ExecContext(ctx, "UPDATE "+table+" SET "+strings.Join(sets, ",")+" WHERE "+keyColumn+" = ? AND version = ?", values...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return domain.ErrConflict
	}
	return nil
}

func insertSQL(table string, columns []string) string {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")
	return "INSERT INTO " + table + " (" + strings.Join(columns, ",") + ") VALUES (" + placeholders + ")"
}

func putImmutable(ctx context.Context, tx *sql.Tx, table, where string, args []any, columns []string, values []any, value any) (bool, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT data FROM "+table+" WHERE "+where, args...).Scan(&old)
	if err == nil {
		if !bytes.Equal(old, data) {
			return false, fmt.Errorf("immutable %s changed: %w", table, domain.ErrConflict)
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	columns = append(append([]string{}, columns...), "data")
	values = append(append([]any{}, values...), string(data))
	_, err = tx.ExecContext(ctx, insertSQL(table, columns), values...)
	return err == nil, err
}

// comparePrefix checks the complete committed prefix but never updates or
// deletes it. Only the suffix is inserted, independently of metadata updates.
func comparePrefix[T any](ctx context.Context, tx *sql.Tx, query string, args []any, messages []T) (int, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var sequence int
		var old []byte
		if err := rows.Scan(&sequence, &old); err != nil {
			return 0, err
		}
		if count >= len(messages) || sequence != count+1 {
			return 0, fmt.Errorf("transcript prefix cannot be removed: %w", domain.ErrConflict)
		}
		data, err := json.Marshal(messages[count])
		if err != nil {
			return 0, err
		}
		if !bytes.Equal(data, old) {
			return 0, fmt.Errorf("transcript prefix cannot be edited: %w", domain.ErrConflict)
		}
		count++
	}
	return count, rows.Err()
}

func appendSessionMessages(ctx context.Context, tx *sql.Tx, session *domain.Session) error {
	start, err := comparePrefix(ctx, tx, "SELECT sequence, data FROM session_messages WHERE session_id = ? ORDER BY sequence", []any{session.ID}, session.Messages)
	if err != nil {
		return err
	}
	for i := start; i < len(session.Messages); i++ {
		message := session.Messages[i]
		data, err := json.Marshal(message)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO session_messages(session_id, sequence, task_id, data) VALUES(?,?,?,?)", session.ID, i+1, optionalID(message.TaskID), string(data)); err != nil {
			return err
		}
	}
	return nil
}

func appendInvocationMessages(ctx context.Context, tx *sql.Tx, invocation *domain.Invocation) error {
	start, err := comparePrefix(ctx, tx, "SELECT sequence, data FROM invocation_messages WHERE invocation_id = ? AND context_version = ? ORDER BY sequence", []any{invocation.ID, invocation.Thread.ContextVersion}, invocation.Thread.Messages)
	if err != nil {
		return err
	}
	for i := start; i < len(invocation.Thread.Messages); i++ {
		data, err := json.Marshal(invocation.Thread.Messages[i])
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO invocation_messages(invocation_id, context_version, sequence, data) VALUES(?,?,?,?)", invocation.ID, invocation.Thread.ContextVersion, i+1, string(data)); err != nil {
			return err
		}
	}
	return nil
}
