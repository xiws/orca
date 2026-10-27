// Package store 提供基于 SQLite 的状态持久化存储，包括工作空间锁、WAL 日志、
// CAS（比较并交换）更新、崩溃恢复以及外部数据导入功能。
package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xiws/orca/internal/domain"
)

// 以下序列化类型刻意不依赖已废弃的 session/core/llm 包。
// 不可使用原始 JSON 或开放式元数据映射替代：只有明确列出的历史字段才能进入 SQLite（含 WAL）。

// ArchivedModel 归档的模型标识。
type ArchivedModel struct {
	Provider string `json:"provider"`
	ModelID  string `json:"model_id"`
}

// ArchivedToolCall 归档的工具调用记录。
type ArchivedToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Reasoning string `json:"reasoning,omitempty"`
}

// ArchivedMessage 归档的对话消息。
type ArchivedMessage struct {
	ID         int64              `json:"id"`
	Role       string             `json:"role"`
	Content    string             `json:"content,omitempty"`
	ToolCalls  []ArchivedToolCall `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
	CreateTime int64              `json:"create_time"`
}

// ArchivedUsage 归档的 token 使用统计。
type ArchivedUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ArchivedRemoteMetadata 仅包含已知的续接标识符，未知元数据、认证信息和提供方连接详情会被丢弃。
type ArchivedRemoteMetadata struct {
	Cursor               string `json:"cursor,omitempty"`
	ConversationMetadata string `json:"conversation_metadata,omitempty"`
	GeminiMetadata       string `json:"gemini_metadata,omitempty"`
}

// ArchivedOtterState 归档的 Otter 会话续接状态。
type ArchivedOtterState struct {
	ChatSessionID      string                  `json:"chat_session_id,omitempty"`
	ParentMessageID    int64                   `json:"parent_message_id,omitempty"`
	ParentMessageIDStr string                  `json:"parent_message_id_str,omitempty"`
	RemoteMetadata     *ArchivedRemoteMetadata `json:"remote_metadata,omitempty"`
	Delivered          int                     `json:"delivered"`
}

// ArchivedTranscript 归档的完整对话转录记录。
type ArchivedTranscript struct {
	Model      ArchivedModel       `json:"model"`
	Messages   []ArchivedMessage   `json:"messages"`
	TotalUsage ArchivedUsage       `json:"total_usage"`
	OtterState *ArchivedOtterState `json:"otter_state,omitempty"`
}

// ArchivedInvocation 归档的单次调用记录，包含子调用。
type ArchivedInvocation struct {
	ID          int64                `json:"id"`
	TaskID      domain.TaskID        `json:"task_id"`
	ProjectPath string               `json:"project_path"`
	Model       ArchivedModel        `json:"model"`
	Messages    []ArchivedMessage    `json:"messages"`
	TotalUsage  ArchivedUsage        `json:"total_usage"`
	OtterState  *ArchivedOtterState  `json:"otter_state,omitempty"`
	Children    []ArchivedInvocation `json:"children,omitempty"`
}

// ImportArchive 是不可变的、清洗后的源数据快照，用于审计计数和内容。
// 此处所有 ID 均为源 ID，而非活跃数据库引用。续接标识不得用于初始化新的调用或游标。
type ImportArchive struct {
	SourcePath  string               `json:"source_path"`      // 源文件路径
	SourceID    domain.SessionID     `json:"source_id"`        // 源会话 ID
	SHA256      string               `json:"sha256"`           // 源文件 SHA256
	Version     int                  `json:"version"`          // 0 = 无版本遗留格式，2 = v2
	Session     *domain.Session      `json:"session"`          // 会话数据
	Tasks       []*domain.Task       `json:"tasks"`            // 任务列表
	Invocations []ArchivedInvocation `json:"invocations"`      // 调用记录
	Legacy      *ArchivedTranscript  `json:"legacy,omitempty"` // 遗留格式转录
}

// importSchema 延迟创建导入记录表，与导入操作在同一事务中。
// 复合主键为来源索引。
const importSchema = `CREATE TABLE IF NOT EXISTS imported_sessions (
 source_path TEXT NOT NULL,
 source_id INTEGER NOT NULL CHECK(source_id > 0),
 source_sha256 TEXT NOT NULL,
 session_id INTEGER NOT NULL UNIQUE REFERENCES sessions(id) DEFERRABLE INITIALLY DEFERRED,
 data TEXT NOT NULL,
 PRIMARY KEY(source_path, source_id, source_sha256)
)`

// Import 从外部 JSON 文件导入会话数据，只读不修改源文件。
// owner 授权重新分配所有权，但必须解析为此数据库的工作空间。
func (s *Store) Import(ctx context.Context, path, owner string) (domain.SessionID, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, sql.ErrConnDone
	}

	// 从实际的主数据库推导所有权，而非 CWD 或源路径。
	var databasePath string
	if err := s.db.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name = 'main'").Scan(&databasePath); err != nil {
		return 0, storageError(err)
	}
	workspace, err := canonicalImportPath(filepath.Dir(filepath.Dir(databasePath)))
	if err != nil {
		return 0, fmt.Errorf("resolve database workspace: %w", err)
	}
	// 校验 owner 路径。
	if owner != "" {
		if !filepath.IsAbs(owner) {
			return 0, fmt.Errorf("import owner must be an absolute workspace path")
		}
		resolved, err := canonicalImportPath(owner)
		if err != nil {
			return 0, fmt.Errorf("resolve import owner: %w", err)
		}
		if resolved != workspace {
			return 0, fmt.Errorf("import owner must match the open database workspace")
		}
	}

	// 规范化源路径并读取文件。
	source, err := canonicalImportPath(path)
	if err != nil {
		return 0, fmt.Errorf("resolve import source: %w", err)
	}
	info, err := os.Stat(source)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("import source must be a regular JSON file")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return 0, fmt.Errorf("read import source: %w", err)
	}
	archive, err := decodeImport(data)
	if err != nil {
		return 0, err
	}
	// 未指定 owner 时校验归属。
	if owner == "" && !importBelongsTo(archive, workspace) {
		return 0, fmt.Errorf("source workspace is missing, ambiguous, or different; specify an explicit owner matching the open database workspace")
	}
	archive.SourcePath = source
	archive.SourceID = archive.Session.ID
	digest := sha256.Sum256(data)
	archive.SHA256 = hex.EncodeToString(digest[:])
	// 在开启写事务之前序列化白名单字段，原始字节（可能含凭证）不传入 SQL 操作。
	archived, err := json.Marshal(archive)
	if err != nil {
		return 0, err
	}
	mutation := remapImport(archive, workspace)
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	// 在事务中执行导入：先检查是否已导入，再应用变更。
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, storageError(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, importSchema); err != nil {
		return 0, storageError(err)
	}
	var id domain.SessionID
	err = tx.QueryRowContext(ctx, `SELECT session_id FROM imported_sessions
 WHERE source_path = ? AND source_id = ? AND source_sha256 = ?`, source, archive.SourceID, archive.SHA256).Scan(&id)
	if err == nil {
		return id, nil // 已导入过，返回已有 ID。
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, storageError(err)
	}
	committed, err := applyMutation(ctx, tx, mutation)
	if err != nil {
		return 0, storageError(err)
	}
	id = mutation.Sessions[0].ID
	if _, err := tx.ExecContext(ctx, `INSERT INTO imported_sessions
 (source_path, source_id, source_sha256, session_id, data) VALUES(?,?,?,?,?)`, source, archive.SourceID, archive.SHA256, id, string(archived)); err != nil {
		return 0, storageError(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, storageError(err)
	}
	// 事务提交成功后执行回调。
	for _, update := range committed {
		update()
	}
	return id, nil
}

// Archive 查询已导入会话的清洗后源数据快照，包括完整的递归转录和每次调用的使用统计。
// 未导入的会话没有归档。
func (s *Store) Archive(ctx context.Context, id domain.SessionID) (*ImportArchive, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, sql.ErrConnDone
	}
	// 检查导入表是否存在（延迟创建）。
	var exists int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'imported_sessions'").Scan(&exists); err != nil {
		return nil, storageError(err)
	}
	if exists == 0 {
		return nil, domain.ErrNotFound
	}
	return readOne[ImportArchive](ctx, s.db, "SELECT data FROM imported_sessions WHERE session_id = ?", id)
}

// canonicalImportPath 将路径解析为绝对路径并跟随符号链接。
func canonicalImportPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

// importBelongsTo 校验导入数据的 project_path 是否都匹配目标工作空间。
func importBelongsTo(archive *ImportArchive, workspace string) bool {
	matches := func(path string) bool {
		if !filepath.IsAbs(path) {
			return false
		}
		resolved, err := canonicalImportPath(path)
		return err == nil && resolved == workspace
	}
	if !matches(archive.Session.ProjectPath) {
		return false
	}
	// 递归校验所有调用的 project_path。
	var check func([]ArchivedInvocation) bool
	check = func(invocations []ArchivedInvocation) bool {
		for _, inv := range invocations {
			if (inv.ProjectPath != "" && !matches(inv.ProjectPath)) || !check(inv.Children) {
				return false
			}
		}
		return true
	}
	return check(archive.Invocations)
}

// decodeImport 解码导入 JSON 数据，支持 v2 和无版本遗留格式。
func decodeImport(data []byte) (*ImportArchive, error) {
	if err := validateImportJSON(data); err != nil {
		return nil, err
	}
	var header struct {
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("invalid import JSON: %w", err)
	}
	archive := &ImportArchive{}
	if len(header.Version) != 0 {
		var version int
		if err := json.Unmarshal(header.Version, &version); err != nil || version != 2 {
			return nil, fmt.Errorf("unsupported import version (only unversioned legacy or version 2 are supported)")
		}
		// 仅解码源字段，不解码调用方提供的来源信息。
		var record struct {
			Session     *domain.Session      `json:"session"`
			Tasks       []*domain.Task       `json:"tasks"`
			Invocations []ArchivedInvocation `json:"invocations"`
			Legacy      *ArchivedTranscript  `json:"legacy"`
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("invalid v2 import: %w", err)
		}
		archive.Version = version
		archive.Session, archive.Tasks = record.Session, record.Tasks
		archive.Invocations, archive.Legacy = record.Invocations, record.Legacy
	} else {
		// 遗留格式解码。
		var old struct {
			ID          domain.SessionID `json:"id"`
			Title       string           `json:"title"`
			ProjectPath string           `json:"project_path"`
			Provider    struct {
				Provider string
				ModelID  string
			} `json:"provider"`
			Messages   []ArchivedMessage   `json:"messages"`
			TotalUsage ArchivedUsage       `json:"total_usage"`
			OtterState *ArchivedOtterState `json:"otter_state"`
			CreateTime int64               `json:"create_time"`
			UpdateTime int64               `json:"update_time"`
		}
		if err := json.Unmarshal(data, &old); err != nil {
			return nil, fmt.Errorf("invalid legacy import: %w", err)
		}
		archive.Session = &domain.Session{
			ID: old.ID, Title: old.Title, ProjectPath: old.ProjectPath,
			Messages: importTimeline(old.Messages), CreateTime: old.CreateTime, UpdateTime: old.UpdateTime,
		}
		archive.Legacy = &ArchivedTranscript{
			Model:    ArchivedModel{Provider: old.Provider.Provider, ModelID: old.Provider.ModelID},
			Messages: old.Messages, TotalUsage: old.TotalUsage, OtterState: old.OtterState,
		}
	}
	if err := validateImportRecord(archive); err != nil {
		return nil, err
	}
	return archive, nil
}

// validateImportJSON 在类型化解码前拒绝重复键、大小写别名和无效 UTF-8，
// 因为 encoding/json 会静默接受这些问题。
func validateImportJSON(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid import JSON: invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return fmt.Errorf("import JSON exceeds maximum nesting depth")
		}
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("invalid import JSON: %w", err)
		}
		if depth == 0 && token != json.Delim('{') {
			return fmt.Errorf("import JSON must be an object")
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim == '{' {
			keys := make(map[string]bool)
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return fmt.Errorf("invalid import JSON: %w", err)
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("invalid import JSON object key")
				}
				// 使用 SimpleFold 循环中最小的 rune，与 encoding/json 保持一致。
				name = strings.Map(func(r rune) rune {
					for {
						next := unicode.SimpleFold(r)
						if next <= r {
							return next
						}
						r = next
					}
				}, name)
				if keys[name] {
					return fmt.Errorf("duplicate import JSON key (including case aliases)")
				}
				keys[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		} else if delim == '[' {
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		} else {
			return fmt.Errorf("invalid import JSON delimiter")
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("invalid import JSON: %w", err)
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid import JSON: trailing data")
	}
	return nil
}

// validateImportRecord 校验导入记录的完整性和一致性。
func validateImportRecord(a *ImportArchive) error {
	if a.Session == nil || a.Session.ID <= 0 {
		return fmt.Errorf("import session must have a positive ID")
	}
	if a.Session.Version < 0 {
		return fmt.Errorf("invalid import session version")
	}
	// 校验任务 ID 唯一性和会话归属。
	tasks := make(map[domain.TaskID]*domain.Task)
	for _, task := range a.Tasks {
		if task == nil || task.ID <= 0 || tasks[task.ID] != nil {
			return fmt.Errorf("invalid or duplicate import task ID")
		}
		if task.SessionID != a.Session.ID {
			return fmt.Errorf("import task refers to another session")
		}
		if task.Version < 0 {
			return fmt.Errorf("invalid import task version")
		}
		tasks[task.ID] = task
	}
	// 在任何 ID 重映射之前校验父引用和循环依赖。
	visited := make(map[domain.TaskID]uint8)
	var visit func(domain.TaskID) error
	visit = func(id domain.TaskID) error {
		if id == 0 {
			return nil
		}
		task := tasks[id]
		if task == nil {
			return fmt.Errorf("import task refers to unknown parent task")
		}
		if visited[id] == 1 {
			return fmt.Errorf("import task parent cycle")
		}
		if visited[id] == 2 {
			return nil
		}
		visited[id] = 1
		if err := visit(task.ParentTaskID); err != nil {
			return err
		}
		visited[id] = 2
		return nil
	}
	for id := range tasks {
		if err := visit(id); err != nil {
			return err
		}
	}
	// 校验会话消息时间线。
	messages := make(map[int64]bool)
	for _, message := range a.Session.Messages {
		if message.ID <= 0 || messages[message.ID] {
			return fmt.Errorf("invalid or duplicate import timeline message ID")
		}
		messages[message.ID] = true
		if message.Role != domain.UserMessage && message.Role != domain.AssistantMessage {
			return fmt.Errorf("invalid import timeline message role")
		}
		if message.TaskID != 0 && tasks[message.TaskID] == nil {
			return fmt.Errorf("import message refers to unknown task")
		}
	}
	// 校验遗留转录。
	if a.Legacy != nil {
		if err := validateImportTranscript(a.Legacy.Messages, a.Legacy.TotalUsage, a.Legacy.OtterState); err != nil {
			return err
		}
	}
	// 递归校验调用记录。
	seen := make(map[int64]bool)
	var invocations func([]ArchivedInvocation) error
	invocations = func(list []ArchivedInvocation) error {
		for _, inv := range list {
			if inv.ID <= 0 || seen[inv.ID] {
				return fmt.Errorf("invalid or duplicate import invocation ID")
			}
			seen[inv.ID] = true
			if tasks[inv.TaskID] == nil {
				return fmt.Errorf("import invocation refers to unknown task")
			}
			if err := validateImportTranscript(inv.Messages, inv.TotalUsage, inv.OtterState); err != nil {
				return err
			}
			if err := invocations(inv.Children); err != nil {
				return err
			}
		}
		return nil
	}
	return invocations(a.Invocations)
}

// validateImportTranscript 校验单次转录的消息、使用量和续接状态。
func validateImportTranscript(messages []ArchivedMessage, usage ArchivedUsage, state *ArchivedOtterState) error {
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.TotalTokens < 0 {
		return fmt.Errorf("invalid import usage")
	}
	if state != nil && (state.Delivered < 0 || state.ParentMessageID < 0) {
		return fmt.Errorf("invalid archived continuation state")
	}
	seen := make(map[int64]bool)
	for _, message := range messages {
		if message.ID <= 0 || seen[message.ID] {
			return fmt.Errorf("invalid or duplicate archived message ID")
		}
		seen[message.ID] = true
		switch message.Role {
		case "system", "user", "assistant", "tool":
		default:
			return fmt.Errorf("invalid archived message role")
		}
		calls := make(map[string]bool)
		for _, call := range message.ToolCalls {
			if call.ID == "" || calls[call.ID] {
				return fmt.Errorf("invalid or duplicate archived tool call ID")
			}
			calls[call.ID] = true
		}
	}
	return nil
}

// importTimeline 从归档消息中提取会话时间线消息，
// 过滤掉仅有工具调用的助手中间消息，保留有实质内容的消息。
func importTimeline(messages []ArchivedMessage) []domain.Message {
	var timeline []domain.Message
	pending := -1
	hasUser := false
	appendMessage := func(message ArchivedMessage) {
		timeline = append(timeline, domain.Message{
			ID: message.ID, Role: domain.MessageRole(message.Role), Content: message.Content, CreateTime: message.CreateTime,
		})
	}
	flush := func() {
		if pending >= 0 {
			appendMessage(messages[pending])
		}
		pending = -1
	}
	for i, message := range messages {
		switch message.Role {
		case "user":
			flush()
			appendMessage(message)
			hasUser = true
		case "assistant":
			pending = -1
			// 仅保留有文本内容且无工具调用的助手消息。
			if hasUser && len(message.ToolCalls) == 0 && message.ToolCallID == "" && strings.TrimSpace(message.Content) != "" {
				pending = i
			}
		case "tool":
			pending = -1
		}
	}
	flush()
	return timeline
}

// remapImport 将导入数据的 ID 重映射为新的唯一 ID，避免与现有数据冲突。
// 不修改原始快照，并避免偶然复用源 ID。
func remapImport(archive *ImportArchive, workspace string) domain.Mutation {
	used := map[int64]bool{int64(archive.Session.ID): true}
	for _, task := range archive.Tasks {
		used[int64(task.ID)] = true
	}
	for _, message := range archive.Session.Messages {
		used[message.ID] = true
	}
	// 生成不冲突的新 ID。
	fresh := func() int64 {
		for {
			id := domain.NewID()
			if !used[id] {
				used[id] = true
				return id
			}
		}
	}
	session := *archive.Session
	session.ID, session.Version, session.ProjectPath = domain.SessionID(fresh()), 0, workspace
	// 建立旧任务 ID 到新 ID 的映射。
	taskIDs := make(map[domain.TaskID]domain.TaskID, len(archive.Tasks))
	for _, task := range archive.Tasks {
		taskIDs[task.ID] = domain.TaskID(fresh())
	}
	mutation := domain.Mutation{Sessions: []*domain.Session{&session}}
	for _, old := range archive.Tasks {
		task := *old
		task.ID, task.SessionID, task.ParentTaskID = taskIDs[old.ID], session.ID, taskIDs[old.ParentTaskID]
		if task.Version == 0 { // 无版本控制的 v2 文件表示首次规格化。
			task.Version = 1
		}
		mutation.Tasks = append(mutation.Tasks, &task)
	}
	// 重映射消息 ID 和任务引用。
	session.Messages = make([]domain.Message, len(archive.Session.Messages))
	for i, old := range archive.Session.Messages {
		message := old
		message.ID, message.TaskID = fresh(), taskIDs[old.TaskID]
		session.Messages[i] = message
	}
	return mutation
}
