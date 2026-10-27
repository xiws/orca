package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

// queryer 定义支持上下文查询的接口，*sql.DB 和 *sql.Tx 均满足此接口
type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// readOne 从查询结果中读取单行 JSON 数据并反序列化为指定类型
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

// readMany 从查询结果中读取多行 JSON 数据并反序列化为指定类型切片
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

// loadSessionMessages 从数据库加载 Session 的所有消息到内存
func loadSessionMessages(ctx context.Context, q queryer, session *domain.Session) error {
	messages, err := readMany[domain.Message](ctx, q, "SELECT data FROM session_messages WHERE session_id = ? ORDER BY sequence", session.ID)
	if err == nil {
		session.Messages = messages
	}
	return err
}

// loadInvocationMessages 从数据库加载 Invocation 线程的消息到内存
func loadInvocationMessages(ctx context.Context, q queryer, invocation *domain.Invocation) error {
	messages, err := readMany[model.Message](ctx, q, "SELECT data FROM invocation_messages WHERE invocation_id = ? AND context_version = ? ORDER BY sequence", invocation.ID, invocation.Thread.ContextVersion)
	if err == nil {
		invocation.Thread.Messages = messages
	}
	return err
}

// Session 按 ID 查询单个 Session 及其完整消息历史
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

// SessionMessages 按追加序号分页查询 Session 消息（序号从 1 开始，非消息 ID）
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

// Task 按 ID 查询 Task；version=0 时返回最新版本
func (s *Store) Task(ctx context.Context, id domain.TaskID, version int) (*domain.Task, error) {
	if version == 0 {
		return readOne[domain.Task](ctx, s.db, "SELECT data FROM tasks WHERE id = ? ORDER BY version DESC LIMIT 1", id)
	}
	return readOne[domain.Task](ctx, s.db, "SELECT data FROM tasks WHERE id = ? AND version = ?", id, version)
}

// Run 按 ID 查询 Run
func (s *Store) Run(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	return readOne[domain.Run](ctx, s.db, "SELECT data FROM runs WHERE id = ?", id)
}

// Invocation 按 ID 查询 Invocation 及其线程消息
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

// Input 按 ID 查询 InputRequest
func (s *Store) Input(ctx context.Context, id domain.InputRequestID) (*domain.InputRequest, error) {
	return readOne[domain.InputRequest](ctx, s.db, "SELECT data FROM inputs WHERE id = ?", id)
}

// InputByCall 按 call key 查询关联的 InputRequest
func (s *Store) InputByCall(ctx context.Context, key string) (*domain.InputRequest, error) {
	if key == "" {
		return nil, domain.ErrNotFound
	}
	return readOne[domain.InputRequest](ctx, s.db, "SELECT data FROM inputs WHERE call_key = ?", key)
}

// Tool 按 key 查询 ToolExecution
func (s *Store) Tool(ctx context.Context, key string) (*domain.ToolExecution, error) {
	return readOne[domain.ToolExecution](ctx, s.db, "SELECT data FROM tools WHERE key = ?", key)
}

// Delegation 按 key 查询 Delegation 记录
func (s *Store) Delegation(ctx context.Context, key string) (*domain.Delegation, error) {
	return readOne[domain.Delegation](ctx, s.db, "SELECT data FROM delegations WHERE key = ?", key)
}

// Runs 查询所有 Run 记录
func (s *Store) Runs(ctx context.Context) ([]domain.Run, error) {
	return readMany[domain.Run](ctx, s.db, "SELECT data FROM runs ORDER BY id")
}

// Sessions 查询所有 Session 及其完整消息历史
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

// Events 按 Run 分页查询事件流，after 为上次收到的最大序号
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

// Invocations 查询指定 Run 下的所有 Invocation 及其线程消息
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

// Artifacts 查询指定 Run 下的所有产出物
func (s *Store) Artifacts(ctx context.Context, runID domain.RunID) ([]domain.Artifact, error) {
	return readMany[domain.Artifact](ctx, s.db, "SELECT data FROM artifacts WHERE run_id = ? ORDER BY id", runID)
}
