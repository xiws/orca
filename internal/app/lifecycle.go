package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/xiws/orca/internal/domain"
)

// UpdateTask 更新任务的目标和标题，标题最长 80 个字符。
func (s *Service) UpdateTask(ctx context.Context, id domain.TaskID, input, title string) (*domain.Task, error) {
	if strings.TrimSpace(input) == "" {
		return nil, fmt.Errorf("goal cannot be empty")
	}
	task, err := s.store.Task(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	task.Version++
	task.Input = input
	// 标题为空时使用输入内容，最长 80 个字符
	if strings.TrimSpace(title) == "" {
		title = input
	}
	runes := []rune(title)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	task.Title = string(runes)
	task.Specification = nil // 规格说明在目标变更后清空
	if err := s.store.Apply(ctx, domain.Mutation{Tasks: []*domain.Task{task}}); err != nil {
		return nil, err
	}
	return task, nil
}

// DeleteSession 删除指定会话及其所有关联数据。
func (s *Service) DeleteSession(ctx context.Context, id domain.SessionID) error {
	return s.store.DeleteSessions(ctx, []domain.SessionID{id})
}

// DeleteAllSessions 删除所有会话。
func (s *Service) DeleteAllSessions(ctx context.Context) error {
	sessions, err := s.store.Sessions(ctx)
	if err != nil {
		return err
	}
	ids := make([]domain.SessionID, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	return s.store.DeleteSessions(ctx, ids)
}

// ReconcileRun 手动协调一个运行树的外部结果，将未完成的调用和工具标记为已协调。
func (s *Service) ReconcileRun(ctx context.Context, id domain.RunID, note string) (*domain.Run, error) {
	// 必须提供协调说明
	if strings.TrimSpace(note) == "" {
		return nil, fmt.Errorf("record the verified external outcome before reconciliation")
	}
	run, err := s.store.Run(ctx, id)
	if err != nil {
		return nil, err
	}
	runs, err := s.store.Runs(ctx)
	if err != nil {
		return nil, err
	}
	// 确认运行树中存在未完成的外部操作
	unresolved, err := s.store.HasUnresolved(ctx, run.RootRunID)
	if err != nil {
		return nil, err
	}
	if !unresolved {
		return nil, fmt.Errorf("run tree has no unresolved outcome")
	}
	// 确认执行已停止
	s.mu.Lock()
	_, active := s.active[run.RootRunID]
	s.mu.Unlock()
	if active {
		return nil, fmt.Errorf("execution has not stopped")
	}
	mutation := domain.Mutation{}
	// 遍历同一根运行树中的所有运行
	for i := range runs {
		r := &runs[i]
		if r.RootRunID != run.RootRunID {
			continue
		}
		// 将已开始的调用标记为失败，避免重放
		invocations, err := s.store.Invocations(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		for j := range invocations {
			inv := &invocations[j]
			if inv.Phase == "model_started" {
				inv.Phase = "failed"
				inv.Error = "External outcome manually reconciled; original invocation will not be replayed"
				inv.Reservation = 0
				mutation.Invocations = append(mutation.Invocations, inv)
			}
		}
		// 将未完成的工具执行标记为已协调
		executions, err := s.store.Tools(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		for j := range executions {
			tool := &executions[j]
			if tool.State == "started" || tool.State == "unknown" {
				tool.State = "reconciled"
				mutation.Tools = append(mutation.Tools, tool)
			}
		}
		changed := false
		// 将未终止的运行标记为失败
		if !r.State.Terminal() {
			if r.State == domain.Cancelling {
				return nil, fmt.Errorf("cancellation is still in progress")
			}
			r.State = domain.Failed
			r.Error = "Manually reconciled without replay: " + note
			r.WaitingID = 0
			changed = true
		}
		// 释放根运行的预留 token 预算
		if r.ID == run.RootRunID && r.Budget.Reserved != 0 {
			r.Budget.Tokens += r.Budget.Reserved
			r.Budget.Reserved = 0
			changed = true
		}
		if changed {
			mutation.Runs = append(mutation.Runs, r)
		}
		// 追加协调记录作为制品和事件
		mutation.Artifacts = append(mutation.Artifacts, &domain.Artifact{ID: domain.NewID(), RunID: r.ID, Kind: "reconciliation", Content: note})
		mutation.Events = append(mutation.Events, domain.Event{RunID: r.ID, Kind: "reconciled", Content: note})
	}
	if err := s.store.Apply(ctx, mutation); err != nil {
		return nil, err
	}
	s.signal()
	return s.store.Run(ctx, run.RootRunID)
}
