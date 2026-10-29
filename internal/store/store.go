// Package store 在内存中保存一次运行的全部状态，并把事件流追加写入
// workspace/.orca/history.jsonl。
//
// 设计取舍：状态不跨进程、不跨崩溃。进程退出后状态即消失，历史留在
// JSONL 文件里供人直接阅读（cat / jq）。因此这里没有事务、没有数据库、
// 没有崩溃恢复，也没有结果未知（unknown）状态。
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/xiws/orca/internal/domain"
)

// Store 是全部运行状态的内存载体，同时负责追加历史日志。
type Store struct {
	mu        sync.Mutex
	log       *os.File
	seq       int64
	closed    bool
	sessions  map[domain.SessionID]*domain.Session
	tasks     map[taskKey]*domain.Task
	runs      map[domain.RunID]*domain.Run
	calls     map[domain.InvocationID]*domain.Invocation
	inputs    map[domain.InputRequestID]*domain.InputRequest
	inputKey  map[string]domain.InputRequestID
	tools     map[string]*domain.ToolExecution
	delegs    map[string]*domain.Delegation
	artifacts []domain.Artifact
	events    map[domain.RunID][]domain.Event
}

// taskKey 用 (id, version) 唯一标识一个不可变的 Task 版本。
type taskKey struct {
	ID      domain.TaskID
	Version int
}

// Open 打开工作空间的状态目录，准备历史日志。历史文件不存在时会被创建。
func Open(workspace string) (*Store, error) {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, ".orca")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "history.jsonl")
	log, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &Store{
		log:      log,
		sessions: map[domain.SessionID]*domain.Session{},
		tasks:    map[taskKey]*domain.Task{},
		runs:     map[domain.RunID]*domain.Run{},
		calls:    map[domain.InvocationID]*domain.Invocation{},
		inputs:   map[domain.InputRequestID]*domain.InputRequest{},
		inputKey: map[string]domain.InputRequestID{},
		tools:    map[string]*domain.ToolExecution{},
		delegs:   map[string]*domain.Delegation{},
		events:   map[domain.RunID][]domain.Event{},
	}, nil
}

// Close 关闭历史日志。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.log.Close()
}

// historyEntry 是写入 history.jsonl 的一行。
type historyEntry struct {
	Time     int64  `json:"time"`
	Sequence int64  `json:"seq"`
	Run      int64  `json:"run"`
	Invoke   int64  `json:"invocation,omitempty"`
	Kind     string `json:"kind"`
	Content  string `json:"content"`
}

// Apply 原子地应用一组领域变更。任何一项校验失败都不会写入任何状态。
//
// 版本号沿用原来的乐观锁语义：Mutation 里带的 Version 必须等于当前版本，
// 否则返回 ErrConflict；提交成功后把新版本号回写到调用方持有的对象上。
func (s *Store) Apply(ctx context.Context, m domain.Mutation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validate(m); err != nil {
		return err
	}
	return s.commit(m)
}

// validate 检查整组变更是否合法，不修改任何状态。
func (s *Store) validate(m domain.Mutation) error {
	for _, session := range m.Sessions {
		if session == nil {
			return errors.New("nil session")
		}
		if old, ok := s.sessions[session.ID]; ok && old.Version != session.Version {
			return domain.ErrConflict
		}
	}
	for _, task := range m.Tasks {
		if task == nil {
			return errors.New("nil task")
		}
		key := taskKey{task.ID, task.Version}
		if old, ok := s.tasks[key]; ok {
			same, err := equalJSON(old, task)
			if err != nil {
				return err
			}
			if !same {
				return fmt.Errorf("immutable task changed: %w", domain.ErrConflict)
			}
		}
	}
	for _, run := range m.Runs {
		if run == nil {
			return errors.New("nil run")
		}
		old, ok := s.runs[run.ID]
		if !ok {
			continue
		}
		if old.Version != run.Version {
			return domain.ErrConflict
		}
		if !domain.CanTransition(old.State, run.State) {
			return fmt.Errorf("illegal run transition %s -> %s", old.State, run.State)
		}
		if old.TaskID != run.TaskID || old.TaskVersion != run.TaskVersion {
			return fmt.Errorf("run task version is immutable: %w", domain.ErrConflict)
		}
	}
	for _, call := range m.Invocations {
		if call == nil {
			return errors.New("nil invocation")
		}
		old, ok := s.calls[call.ID]
		if !ok {
			continue
		}
		if old.Version != call.Version {
			return domain.ErrConflict
		}
		if old.RunID != call.RunID || call.Thread.ContextVersion < old.Thread.ContextVersion {
			return fmt.Errorf("invocation owner or context version cannot regress: %w", domain.ErrConflict)
		}
	}
	for _, input := range m.Inputs {
		if input == nil {
			return errors.New("nil input")
		}
		if old, ok := s.inputs[input.ID]; ok && old.Version != input.Version {
			return domain.ErrConflict
		}
	}
	for _, tool := range m.Tools {
		if tool == nil {
			return errors.New("nil tool")
		}
		if old, ok := s.tools[tool.Key]; ok && old.Version != tool.Version {
			return domain.ErrConflict
		}
	}
	for _, delegation := range m.Delegations {
		if delegation == nil {
			return errors.New("nil delegation")
		}
		if old, ok := s.delegs[delegation.Key]; ok {
			same, err := equalJSON(old, delegation)
			if err != nil {
				return err
			}
			if !same {
				return fmt.Errorf("immutable delegation changed: %w", domain.ErrConflict)
			}
		}
	}
	for _, artifact := range m.Artifacts {
		if artifact == nil {
			return errors.New("nil artifact")
		}
	}
	return nil
}

// commit 写入已校验的变更，并把新的版本号与事件序号回写给调用方。
func (s *Store) commit(m domain.Mutation) error {
	for _, session := range m.Sessions {
		next := clone(session)
		next.Version = session.Version + 1
		s.sessions[session.ID] = next
		session.Version = next.Version
	}
	for _, task := range m.Tasks {
		key := taskKey{task.ID, task.Version}
		if _, ok := s.tasks[key]; !ok {
			s.tasks[key] = clone(task)
		}
	}
	for _, run := range m.Runs {
		next := clone(run)
		next.Version = run.Version + 1
		s.runs[run.ID] = next
		run.Version = next.Version
	}
	for _, call := range m.Invocations {
		next := clone(call)
		next.Version = call.Version + 1
		s.calls[call.ID] = next
		call.Version = next.Version
	}
	for _, input := range m.Inputs {
		next := clone(input)
		next.Version = input.Version + 1
		s.inputs[input.ID] = next
		if input.CallKey != "" {
			s.inputKey[input.CallKey] = input.ID
		}
		input.Version = next.Version
	}
	for _, tool := range m.Tools {
		next := clone(tool)
		next.Version = tool.Version + 1
		s.tools[tool.Key] = next
		tool.Version = next.Version
	}
	for _, delegation := range m.Delegations {
		if _, ok := s.delegs[delegation.Key]; !ok {
			s.delegs[delegation.Key] = clone(delegation)
		}
	}
	for _, artifact := range m.Artifacts {
		s.artifacts = append(s.artifacts, *clone(artifact))
	}
	for i := range m.Events {
		event := m.Events[i]
		s.seq++
		event.Sequence = s.seq
		s.events[event.RunID] = append(s.events[event.RunID], event)
		s.writeHistory(event)
		m.Events[i].Sequence = event.Sequence
	}
	return nil
}

// writeHistory 把事件追加到 history.jsonl。写失败不影响运行，历史只是旁路。
func (s *Store) writeHistory(event domain.Event) {
	entry := historyEntry{
		Time:     time.Now().Unix(),
		Sequence: event.Sequence,
		Run:      int64(event.RunID),
		Invoke:   int64(event.InvocationID),
		Kind:     event.Kind,
		Content:  event.Content,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_, _ = s.log.Write(append(line, '\n'))
}

// Session 按 ID 查询会话。
func (s *Store) Session(ctx context.Context, id domain.SessionID) (*domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return get(s.sessions, id)
}

// Sessions 返回全部会话，按 ID 升序。
func (s *Store) Sessions(ctx context.Context) ([]domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		out = append(out, *clone(session))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Task 按 ID 查询任务；version 为 0 时返回最新版本。
func (s *Store) Task(ctx context.Context, id domain.TaskID, version int) (*domain.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest *domain.Task
	for key, task := range s.tasks {
		if key.ID != id || (version != 0 && key.Version != version) {
			continue
		}
		if latest == nil || key.Version > latest.Version {
			latest = task
		}
	}
	if latest == nil {
		return nil, domain.ErrNotFound
	}
	return clone(latest), nil
}

// Run 按 ID 查询运行。
func (s *Store) Run(ctx context.Context, id domain.RunID) (*domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return get(s.runs, id)
}

// Runs 返回全部运行，按 ID 升序。
func (s *Store) Runs(ctx context.Context) ([]domain.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Run, 0, len(s.runs))
	for _, run := range s.runs {
		out = append(out, *clone(run))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Invocation 按 ID 查询调用。
func (s *Store) Invocation(ctx context.Context, id domain.InvocationID) (*domain.Invocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return get(s.calls, id)
}

// Invocations 返回指定运行下的全部调用，按 ID 升序。
func (s *Store) Invocations(ctx context.Context, runID domain.RunID) ([]domain.Invocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Invocation, 0)
	for _, call := range s.calls {
		if call.RunID == runID {
			out = append(out, *clone(call))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Input 按 ID 查询输入请求。
func (s *Store) Input(ctx context.Context, id domain.InputRequestID) (*domain.InputRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return get(s.inputs, id)
}

// InputByCall 按工具调用键查询关联的输入请求。
func (s *Store) InputByCall(ctx context.Context, key string) (*domain.InputRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == "" {
		return nil, domain.ErrNotFound
	}
	id, ok := s.inputKey[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return get(s.inputs, id)
}

// Tool 按调用键查询工具执行记录。
func (s *Store) Tool(ctx context.Context, key string) (*domain.ToolExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return get(s.tools, key)
}

// Delegation 按调用键查询委派记录。
func (s *Store) Delegation(ctx context.Context, key string) (*domain.Delegation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return get(s.delegs, key)
}

// Tools 返回指定运行下的工具执行记录。
func (s *Store) Tools(ctx context.Context, runID domain.RunID) ([]domain.ToolExecution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.ToolExecution, 0)
	for _, tool := range s.tools {
		if tool.RunID == runID {
			out = append(out, *clone(tool))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Inputs 返回指定运行下的输入请求。
func (s *Store) Inputs(ctx context.Context, runID domain.RunID) ([]domain.InputRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.InputRequest, 0)
	for _, input := range s.inputs {
		if input.RunID == runID {
			out = append(out, *clone(input))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Events 返回指定运行在 after 之后的事件，最多 limit 条。
func (s *Store) Events(ctx context.Context, runID domain.RunID, after int64, limit int) ([]domain.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]domain.Event, 0, limit)
	for _, event := range s.events[runID] {
		if event.Sequence <= after {
			continue
		}
		out = append(out, event)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Artifacts 返回指定运行下的产物。
func (s *Store) Artifacts(ctx context.Context, runID domain.RunID) ([]domain.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Artifact, 0)
	for _, artifact := range s.artifacts {
		if artifact.RunID == runID {
			out = append(out, artifact)
		}
	}
	return out, nil
}

// get 在持锁状态下读取并深拷贝一条记录。
func get[K comparable, T any](m map[K]*T, key K) (*T, error) {
	value, ok := m[key]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return clone(value), nil
}

// clone 通过 JSON 深拷贝一个领域对象，避免调用方与内部状态共享指针。
func clone[T any](value *T) *T {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return &out
}

// equalJSON 比较两个领域对象的 JSON 表示是否一致。
func equalJSON[T any](a, b *T) (bool, error) {
	left, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(left, right), nil
}
