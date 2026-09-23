package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/xiws/orca/internal/domain"
)

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
	if strings.TrimSpace(title) == "" {
		title = input
	}
	runes := []rune(title)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	task.Title = string(runes)
	task.Specification = nil
	if err := s.store.Apply(ctx, domain.Mutation{Tasks: []*domain.Task{task}}); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Service) DeleteSession(ctx context.Context, id domain.SessionID) error {
	return s.store.DeleteSessions(ctx, []domain.SessionID{id})
}

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

func (s *Service) ReconcileRun(ctx context.Context, id domain.RunID, note string) (*domain.Run, error) {
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
	unresolved, err := s.store.HasUnresolved(ctx, run.RootRunID)
	if err != nil {
		return nil, err
	}
	if !unresolved {
		return nil, fmt.Errorf("run tree has no unresolved outcome")
	}
	s.mu.Lock()
	_, active := s.active[run.RootRunID]
	s.mu.Unlock()
	if active {
		return nil, fmt.Errorf("execution has not stopped")
	}
	mutation := domain.Mutation{}
	for i := range runs {
		r := &runs[i]
		if r.RootRunID != run.RootRunID {
			continue
		}
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
		if !r.State.Terminal() {
			if r.State == domain.Cancelling {
				return nil, fmt.Errorf("cancellation is still in progress")
			}
			r.State = domain.Failed
			r.Error = "Manually reconciled without replay: " + note
			r.WaitingID = 0
			changed = true
		}
		if r.ID == run.RootRunID && r.Budget.Reserved != 0 {
			r.Budget.Tokens += r.Budget.Reserved
			r.Budget.Reserved = 0
			changed = true
		}
		if changed {
			mutation.Runs = append(mutation.Runs, r)
		}
		mutation.Artifacts = append(mutation.Artifacts, &domain.Artifact{ID: domain.NewID(), RunID: r.ID, Kind: "reconciliation", Content: note})
		mutation.Events = append(mutation.Events, domain.Event{RunID: r.ID, Kind: "reconciled", Content: note})
	}
	if err := s.store.Apply(ctx, mutation); err != nil {
		return nil, err
	}
	s.signal()
	return s.store.Run(ctx, run.RootRunID)
}
