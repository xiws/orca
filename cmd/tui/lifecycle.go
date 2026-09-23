package main

import (
	"context"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xiws/orca/internal/domain"
)

// Commands can outlive Bubble Tea after a terminal I/O error. Closing this gate
// prevents commands not yet scheduled from submitting work, and joins commands
// that already started before the environment is closed.
type operationGroup struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	closed  bool
	lastRun domain.RunID
}

func (g *operationGroup) track(cmd tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			return nil
		}
		g.wg.Add(1)
		g.mu.Unlock()
		defer g.wg.Done()
		result := cmd()
		var run *domain.Run
		switch msg := result.(type) {
		case operationMsg:
			run = msg.run
		case cancelMsg:
			run = msg.run
		}
		if run != nil {
			g.mu.Lock()
			g.lastRun = run.ID
			g.mu.Unlock()
		}
		return result
	}
}

func (g *operationGroup) finish() domain.RunID {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastRun
}

func stopActiveRun(ctx context.Context, s service, id domain.RunID) error {
	if id == 0 {
		return nil
	}
	r, err := s.Run(ctx, id)
	if err != nil {
		return err
	}
	if r == nil || r.State.Terminal() || r.State == domain.Interrupted || r.State == domain.Reconciling {
		return nil
	}
	if r.State == domain.Waiting {
		inputs, err := s.PendingInputs(ctx, id)
		if err != nil {
			return err
		}
		if len(inputs) > 0 {
			runs, err := s.Runs(ctx)
			if err != nil {
				return err
			}
			active := false
			root := r.RootRunID
			if root == 0 {
				root = r.ID
			}
			for _, child := range runs {
				if child.RootRunID == root && (child.State == domain.Running || child.State == domain.Queued || child.State == domain.Cancelling) {
					active = true
					break
				}
			}
			if !active {
				return nil
			} // Preserve saved human waits on a normal exit.
		}
	}
	if err := s.Cancel(ctx, id); err != nil {
		return err
	}
	_, err = s.Wait(ctx, id)
	return err
}
