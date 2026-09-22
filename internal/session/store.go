// Package session 持久化用户会话及独立的任务、执行快照。
package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/utils"
)

const (
	sessionsDir = "sessions"
	sessionExt  = ".json"
	fileVersion = 2
)

// ModelRef 仅标识模型；调用方必须重新解析连接设置和凭据。
type ModelRef struct {
	Provider string `json:"provider"`
	ModelID  string `json:"model_id"`
}

// InvocationRecord 的 usage 只属于本次执行，不包含 Children 的 usage。
type InvocationRecord struct {
	ID          int64              `json:"id"`
	TaskID      domain.TaskID      `json:"task_id"`
	ProjectPath string             `json:"project_path"`
	Model       ModelRef           `json:"model"`
	Messages    []llm.ChatMessage  `json:"messages"`
	TotalUsage  llm.Usage          `json:"total_usage"`
	OtterState  *llm.OtterState    `json:"otter_state,omitempty"`
	Children    []InvocationRecord `json:"children,omitempty"`
}

// LegacyTranscript 是完整的旧模型历史归档，不是可复用的执行上下文。
// OtterState 仅供归档，不能用于初始化新的 Invocation。
type LegacyTranscript struct {
	Model      ModelRef          `json:"model"`
	Messages   []llm.ChatMessage `json:"messages"`
	TotalUsage llm.Usage         `json:"total_usage"`
	OtterState *llm.OtterState   `json:"otter_state,omitempty"`
}

type Record struct {
	Session     *domain.Session    `json:"session"`
	Tasks       []*domain.Task     `json:"tasks"`
	Invocations []InvocationRecord `json:"invocations"`
	Legacy      *LegacyTranscript  `json:"legacy,omitempty"`
}

// Capture 深复制执行树；同一根执行再次捕获时替换快照，而不是累加 usage。
func (r *Record) Capture(inv *core.Invocation) {
	if r == nil || inv == nil {
		return
	}
	snapshot := captureInvocation(inv)
	for i := range r.Invocations {
		if r.Invocations[i].ID == inv.ID {
			r.Invocations[i] = snapshot
			return
		}
	}
	r.Invocations = append(r.Invocations, snapshot)
}

func captureInvocation(inv *core.Invocation) InvocationRecord {
	snapshot := InvocationRecord{
		ID: inv.ID, TaskID: inv.TaskID, ProjectPath: inv.ProjectPath,
		Model:      ModelRef{Provider: inv.Provider.Provider, ModelID: inv.Provider.ModelID},
		Messages:   slices.Clone(inv.Messages),
		TotalUsage: inv.TotalUsage,
	}
	for i := range snapshot.Messages {
		snapshot.Messages[i].ToolCalls = slices.Clone(inv.Messages[i].ToolCalls)
	}
	if inv.OtterState != nil {
		state := *inv.OtterState
		state.RemoteMetadata = maps.Clone(state.RemoteMetadata)
		snapshot.OtterState = &state
	}
	if inv.Children != nil {
		snapshot.Children = make([]InvocationRecord, 0, len(inv.Children))
		for _, child := range inv.Children {
			if child != nil {
				snapshot.Children = append(snapshot.Children, captureInvocation(child))
			}
		}
	}
	return snapshot
}

// LastModel 返回最新加入的根执行的模型引用；旧归档只在没有新执行时使用。
func (r *Record) LastModel() ModelRef {
	if r == nil {
		return ModelRef{}
	}
	if len(r.Invocations) > 0 {
		return r.Invocations[len(r.Invocations)-1].Model
	}
	if r.Legacy != nil {
		return r.Legacy.Model
	}
	return ModelRef{}
}

// SessionMeta 保存会话的摘要信息，用于列表展示。
type SessionMeta struct {
	Id           int64  `json:"id"`
	Title        string `json:"title"`
	ProjectPath  string `json:"project_path"`
	MessageCount int    `json:"message_count"`
	TotalTokens  int    `json:"total_tokens"`
	CreateTime   int64  `json:"create_time"`
	UpdateTime   int64  `json:"update_time"`
}

type sessionFile struct {
	Version int `json:"version"`
	Record
}

// 仅读取旧格式需要的字段，凭据不进入 Record。
type legacyFile struct {
	Id          int64  `json:"id"`
	Title       string `json:"title"`
	ProjectPath string `json:"project_path"`
	Provider    struct {
		Provider string
		ModelID  string
	} `json:"provider"`
	TotalUsage llm.Usage         `json:"total_usage"`
	Messages   []llm.ChatMessage `json:"messages"`
	CreateTime int64             `json:"create_time"`
	UpdateTime int64             `json:"update_time"`
	OtterState *llm.OtterState   `json:"otter_state,omitempty"`
}

func getProjectSessionDir() string {
	return filepath.Join(utils.GetCurrentPath(), ".orca", sessionsDir)
}

func getHomeSessionDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".orca", sessionsDir)
}

func sessionPath(dir string, id int64) string {
	return filepath.Join(dir, strconv.FormatInt(id, 10)+sessionExt)
}

// Save 始终写入 Session 所属项目，而非当前工作目录。
// 覆盖旧格式前先独占创建原始字节备份，任何失败都不替换原文件。
func Save(r *Record) error {
	if err := validateRecord(r); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sessionFile{Version: fileVersion, Record: *r}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	dir := filepath.Join(r.Session.ProjectPath, ".orca", sessionsDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	path := sessionPath(dir, int64(r.Session.ID))
	original, err := os.ReadFile(path)
	if err == nil {
		previous, legacy, err := decodeRecord(original)
		if err != nil {
			return fmt.Errorf("refuse to overwrite %s: %w", path, err)
		}
		if previous.Session.ID != r.Session.ID {
			return fmt.Errorf("session ID does not match existing file %s", path)
		}
		if legacy {
			backup := strings.TrimSuffix(path, sessionExt) + ".legacy.json"
			if err := preserveLegacy(backup, original); err != nil {
				return fmt.Errorf("backup legacy session: %w", err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read existing session: %w", err)
	}

	tmp, err := writeTemp(dir, data)
	if err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	defer os.Remove(tmp)
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace session file: %w", err)
	}
	return nil
}

// writeTemp 写入、同步并关闭同目录的 0600 临时文件。
func writeTemp(dir string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func preserveLegacy(path string, data []byte) error {
	checkExisting := func() error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("legacy backup is not a regular file: %s", path)
		}
		existing, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("legacy backup conflict: %s", path)
		}
		return nil
	}
	if _, err := os.Lstat(path); err == nil {
		return checkExisting()
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := writeTemp(filepath.Dir(path), data)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	// Link 原子地发布完整备份，并且绝不覆盖已经存在的备份。
	if err := os.Link(tmp, path); err != nil {
		if os.IsExist(err) {
			return checkExisting()
		}
		return err
	}
	return nil
}

func validateRecord(r *Record) error {
	if r == nil || r.Session == nil || r.Session.ID <= 0 {
		return fmt.Errorf("session must have a positive ID")
	}
	if strings.TrimSpace(r.Session.ProjectPath) == "" || !filepath.IsAbs(r.Session.ProjectPath) {
		return fmt.Errorf("session project_path must be an absolute path")
	}
	tasks := make(map[domain.TaskID]bool, len(r.Tasks))
	for _, task := range r.Tasks {
		if task == nil || task.ID <= 0 {
			return fmt.Errorf("task must have a positive ID")
		}
		if task.SessionID != r.Session.ID {
			return fmt.Errorf("task %d belongs to another session", task.ID)
		}
		if tasks[task.ID] {
			return fmt.Errorf("duplicate task ID %d", task.ID)
		}
		tasks[task.ID] = true
	}
	for _, message := range r.Session.Messages {
		if message.Role != domain.UserMessage && message.Role != domain.AssistantMessage {
			return fmt.Errorf("invalid session message role %q", message.Role)
		}
		if message.TaskID != 0 && !tasks[message.TaskID] {
			return fmt.Errorf("message refers to unknown task %d", message.TaskID)
		}
	}
	seen := make(map[int64]bool)
	var validateInvocations func([]InvocationRecord) error
	validateInvocations = func(invocations []InvocationRecord) error {
		for _, inv := range invocations {
			if inv.ID <= 0 || seen[inv.ID] {
				return fmt.Errorf("invalid or duplicate invocation ID %d", inv.ID)
			}
			seen[inv.ID] = true
			if !tasks[inv.TaskID] {
				return fmt.Errorf("invocation %d refers to unknown task %d", inv.ID, inv.TaskID)
			}
			for _, message := range inv.Messages {
				switch message.Role {
				case llm.RoleSystem, llm.RoleUser, llm.RoleAssistant, llm.RoleTool:
				default:
					return fmt.Errorf("invalid invocation message role %q", message.Role)
				}
			}
			if err := validateInvocations(inv.Children); err != nil {
				return err
			}
		}
		return nil
	}
	return validateInvocations(r.Invocations)
}

// Load 项目优先；项目文件存在但损坏时明确报错，不退回同 ID 的 home 文件。
func Load(id int64) (*Record, error) {
	for _, dir := range []string{getProjectSessionDir(), getHomeSessionDir()} {
		if dir == "" {
			continue
		}
		r, err := loadCurrentRecord(dir, id)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return r, nil
	}
	return nil, fmt.Errorf("session %d not found", id)
}

func loadCurrentRecord(dir string, id int64) (*Record, error) {
	r, err := loadFromFile(sessionPath(dir, id))
	if err != nil {
		return nil, err
	}
	if int64(r.Session.ID) != id {
		return nil, fmt.Errorf("session ID does not match filename for %d", id)
	}
	if dir != getHomeSessionDir() || !filepath.IsAbs(r.Session.ProjectPath) {
		return r, nil
	}
	projectDir := filepath.Join(r.Session.ProjectPath, ".orca", sessionsDir)
	if projectDir == dir {
		return r, nil
	}
	current, err := loadFromFile(sessionPath(projectDir, id))
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if current.Session.ID != r.Session.ID || filepath.Clean(current.Session.ProjectPath) != filepath.Clean(r.Session.ProjectPath) {
		return nil, fmt.Errorf("session identity conflicts with home record for %d", id)
	}
	return current, nil
}

func loadFromFile(path string) (*Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// 保留 PathError，Load 需要区分不存在与不可读。
		return nil, err
	}
	r, _, err := decodeRecord(data)
	if err != nil {
		return nil, fmt.Errorf("parse session file %s: %w", path, err)
	}
	return r, nil
}

func decodeRecord(data []byte) (*Record, bool, error) {
	var header struct {
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, false, fmt.Errorf("invalid session JSON: %w", err)
	}
	if len(header.Version) != 0 {
		var version int
		if err := json.Unmarshal(header.Version, &version); err != nil || version != fileVersion {
			return nil, false, fmt.Errorf("unsupported session version: %s", header.Version)
		}
		var file sessionFile
		if err := json.Unmarshal(data, &file); err != nil {
			return nil, false, err
		}
		if err := validateRecord(&file.Record); err != nil {
			return nil, false, err
		}
		return &file.Record, false, nil
	}
	var old legacyFile
	if err := json.Unmarshal(data, &old); err != nil {
		return nil, false, err
	}
	if old.Id <= 0 {
		return nil, false, fmt.Errorf("invalid legacy session ID")
	}
	r := &Record{
		Session: &domain.Session{
			ID: domain.SessionID(old.Id), Title: old.Title, ProjectPath: old.ProjectPath,
			Messages: legacyTimeline(old.Messages), CreateTime: old.CreateTime, UpdateTime: old.UpdateTime,
		},
		Legacy: &LegacyTranscript{
			Model:    ModelRef{Provider: old.Provider.Provider, ModelID: old.Provider.ModelID},
			Messages: old.Messages, TotalUsage: old.TotalUsage, OtterState: old.OtterState,
		},
	}
	return r, true, nil
}

// 每个用户轮次只提取最后的非工具助手回复。工具链、中间助手消息完整留在归档。
func legacyTimeline(messages []llm.ChatMessage) []domain.Message {
	var timeline []domain.Message
	pending := -1
	hasUser := false
	appendMessage := func(message llm.ChatMessage) {
		timeline = append(timeline, domain.Message{
			ID: message.Id, Role: domain.MessageRole(message.Role), Content: message.Content,
			CreateTime: message.CreateTime,
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
		case llm.RoleUser:
			flush()
			appendMessage(message)
			hasUser = true
		case llm.RoleAssistant:
			pending = -1
			if hasUser && len(message.ToolCalls) == 0 && message.ToolCallID == "" && strings.TrimSpace(message.Content) != "" {
				pending = i
			}
		case llm.RoleTool:
			pending = -1
		}
	}
	flush()
	return timeline
}

// List 合并项目与 home，按更新时间倒序；备份不属于业务会话。
func List() []SessionMeta {
	var metas []SessionMeta
	seen := make(map[int64]bool)
	for _, dir := range []string{getProjectSessionDir(), getHomeSessionDir()} {
		entries, _ := os.ReadDir(dir)
		for _, entry := range entries {
			id, ok := sessionEntryID(entry)
			if !ok || seen[id] {
				continue
			}
			// 即使项目文件损坏，也不展示被它遮蔽的 home 历史。
			seen[id] = true
			r, err := loadCurrentRecord(dir, id)
			if err != nil {
				continue
			}
			s := r.Session
			title := s.Title
			if title == "" {
				title = firstUserMessage(s.Messages)
			}
			tokens := invocationTokens(r.Invocations)
			if r.Legacy != nil {
				tokens += r.Legacy.TotalUsage.TotalTokens
			}
			metas = append(metas, SessionMeta{
				Id: id, Title: title, ProjectPath: s.ProjectPath,
				MessageCount: len(s.Messages), TotalTokens: tokens,
				CreateTime: s.CreateTime, UpdateTime: s.UpdateTime,
			})
		}
	}
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].UpdateTime == metas[j].UpdateTime {
			return metas[i].Id > metas[j].Id
		}
		return metas[i].UpdateTime > metas[j].UpdateTime
	})
	return metas
}

func invocationTokens(invocations []InvocationRecord) int {
	var tokens int
	for _, inv := range invocations {
		tokens += inv.TotalUsage.TotalTokens + invocationTokens(inv.Children)
	}
	return tokens
}

func sessionEntryID(entry os.DirEntry) (int64, bool) {
	if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionExt) {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimSuffix(entry.Name(), sessionExt), 10, 64)
	return id, err == nil && id > 0 && entry.Name() == strconv.FormatInt(id, 10)+sessionExt
}

// Delete 只删除业务文件，绝不静默清理可能含旧凭据的原始备份。
func Delete(id int64) error {
	found := false
	for _, dir := range []string{getProjectSessionDir(), getHomeSessionDir()} {
		if dir == "" {
			continue
		}
		if err := os.Remove(sessionPath(dir, id)); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("delete session: %w", err)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("session %d not found", id)
	}
	return nil
}

func DeleteAll() error {
	var errs []string
	for _, dir := range []string{getProjectSessionDir(), getHomeSessionDir()} {
		if dir == "" {
			continue
		}
		if err := clearDir(dir); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", dir, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors deleting sessions: %s", strings.Join(errs, "; "))
	}
	return nil
}

func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, ok := sessionEntryID(entry); !ok {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func firstUserMessage(messages []domain.Message) string {
	for _, message := range messages {
		if message.Role == domain.UserMessage && strings.TrimSpace(message.Content) != "" {
			line := strings.SplitN(strings.TrimSpace(message.Content), "\n", 2)[0]
			runes := []rune(strings.TrimSpace(line))
			if len(runes) > 30 {
				return string(runes[:27]) + "..."
			}
			return string(runes)
		}
	}
	return ""
}
