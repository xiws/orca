package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/xiws/orca/internal/domain"
)

// Recover never retries external work. Terminal runs retain their terminal state;
// unresolved external work on such runs is reported with ErrUnknown AFTER the
// recovery transaction commits, rather than silently reviving or ignoring them.
func (s *Store) Recover(ctx context.Context) error {
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
	tools, err := readMany[domain.ToolExecution](ctx, tx, "SELECT data FROM tools WHERE state IN ('started','unknown') ORDER BY key")
	if err != nil {
		return err
	}
	uncertain := make(map[domain.RunID]bool)
	mutation := domain.Mutation{}
	for i := range tools {
		tool := &tools[i]
		uncertain[tool.RunID] = true
		if tool.State == "started" {
			tool.State = "unknown"
			mutation.Tools = append(mutation.Tools, tool)
		}
	}
	invocations, err := readMany[domain.Invocation](ctx, tx, "SELECT data FROM invocations WHERE phase = 'model_started'")
	if err != nil {
		return err
	}
	for _, invocation := range invocations {
		uncertain[invocation.RunID] = true
	}
	runs, err := readMany[domain.Run](ctx, tx, "SELECT data FROM runs ORDER BY id")
	if err != nil {
		return err
	}
	uncertainRoots := make(map[domain.RunID]bool)
	interruptedRoots := make(map[domain.RunID]bool)
	for _, run := range runs {
		if uncertain[run.ID] || run.State == domain.Reconciling {
			uncertainRoots[run.RootRunID] = true
		}
		if run.State == domain.Running || run.State == domain.Interrupted {
			interruptedRoots[run.RootRunID] = true
		}
	}
	var terminalUnknown []domain.RunID
	for i := range runs {
		run := &runs[i]
		if run.State.Terminal() {
			if uncertain[run.ID] {
				terminalUnknown = append(terminalUnknown, run.ID)
			}
			continue
		}
		target := run.State
		switch {
		case uncertain[run.ID] || (run.ID == run.RootRunID && uncertainRoots[run.ID]):
			target = domain.Reconciling
		case run.ID == run.RootRunID && run.State == domain.Waiting && interruptedRoots[run.ID]:
			target = domain.Interrupted
		case run.State == domain.Running:
			target = domain.Interrupted
		case run.State == domain.Cancelling:
			target = domain.Cancelled
		}
		if target == run.State {
			continue
		}
		// Queued -> reconciling is intentionally not a domain transition. Take the
		// two legal transitions inside this same transaction if inconsistent external
		// work is found on a queued run.
		if run.State == domain.Queued && target == domain.Reconciling {
			run.State = domain.Interrupted
			if _, err := applyMutation(ctx, tx, domain.Mutation{Runs: []*domain.Run{run}}); err != nil {
				return storageError(err)
			}
			persisted, err := readOne[domain.Run](ctx, tx, "SELECT data FROM runs WHERE id = ?", run.ID)
			if err != nil {
				return err
			}
			run = persisted
		}
		run.State = target
		mutation.Runs = append(mutation.Runs, run)
	}
	if _, err := applyMutation(ctx, tx, mutation); err != nil {
		return storageError(err)
	}
	if err := tx.Commit(); err != nil {
		return storageError(err)
	}
	if len(terminalUnknown) > 0 {
		return fmt.Errorf("%w: terminal runs %v", domain.ErrUnknown, terminalUnknown)
	}
	return nil
}
