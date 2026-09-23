package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xiws/orca/internal/domain"
)

func TestHasUnresolvedStateMatrix(t *testing.T) {
	states := []domain.RunState{
		domain.Queued, domain.Running, domain.Waiting, domain.Cancelling,
		domain.Interrupted, domain.Succeeded, domain.Failed, domain.Cancelled, domain.Reconciling,
	}
	operations := []struct {
		name      string
		phase     string
		toolState string
		uncertain bool
	}{
		{name: "no operations"},
		{name: "completed", phase: "completed", toolState: "completed"},
		{name: "model started", phase: "model_started", uncertain: true},
		{name: "tool started", phase: "completed", toolState: "started", uncertain: true},
		{name: "tool unknown", phase: "completed", toolState: "unknown", uncertain: true},
		{name: "tool prepared", phase: "ready", toolState: "prepared"},
		{name: "tool failed", phase: "failed", toolState: "failed"},
		{name: "tool cancelled", phase: "cancelled", toolState: "cancelled"},
	}
	for _, state := range states {
		for _, op := range operations {
			t.Run(string(state)+"/"+op.name, func(t *testing.T) {
				s, _ := testStore(t)
				m := domain.Mutation{
					Tasks: []*domain.Task{{ID: 1, Version: 1}},
					Runs:  []*domain.Run{{ID: 1, TaskID: 1, TaskVersion: 1, RootRunID: 1, State: state}},
				}
				if op.phase != "" {
					// A later completed invocation must not hide an earlier uncertain one.
					m.Invocations = []*domain.Invocation{
						{ID: 1, RunID: 1, Phase: op.phase},
						{ID: 2, RunID: 1, Phase: "completed"},
					}
				}
				if op.toolState != "" {
					m.Tools = []*domain.ToolExecution{
						{Key: "a", RunID: 1, InvocationID: 1, State: op.toolState},
						{Key: "z", RunID: 1, InvocationID: 2, State: "completed"},
					}
				}
				mustApply(t, s, m)
				stopped := state == domain.Interrupted || state == domain.Succeeded || state == domain.Failed || state == domain.Cancelled
				want := state == domain.Reconciling || (stopped && op.uncertain)
				assertHasUnresolved(t, s, 0, want)
				assertHasUnresolved(t, s, 1, want)
				assertHasUnresolved(t, s, 99, false)
			})
		}
	}
}

func TestHasUnresolvedTreeFilters(t *testing.T) {
	for _, cause := range []string{"reconciling", "model_started", "started", "unknown"} {
		t.Run(cause, func(t *testing.T) {
			s, _ := testStore(t)
			assertHasUnresolved(t, s, 0, false)
			assertHasUnresolved(t, s, 1, false)
			m := domain.Mutation{
				Tasks: []*domain.Task{{ID: 1, Version: 1}, {ID: 2, Version: 1}, {ID: 3, Version: 1}, {ID: 4, Version: 1}},
				Runs: []*domain.Run{
					{ID: 1, TaskID: 1, TaskVersion: 1, RootRunID: 1, State: domain.Interrupted},
					{ID: 2, TaskID: 2, TaskVersion: 1, RootRunID: 1, ParentRunID: 1, State: domain.Interrupted},
					{ID: 3, TaskID: 3, TaskVersion: 1, RootRunID: 1, ParentRunID: 2, State: domain.Interrupted},
					{ID: 4, TaskID: 4, TaskVersion: 1, RootRunID: 4, State: domain.Succeeded},
				},
				Invocations: []*domain.Invocation{{ID: 1, RunID: 3, Phase: "completed"}},
			}
			switch cause {
			case "reconciling":
				m.Runs[2].State = domain.Reconciling
			case "model_started":
				m.Invocations[0].Phase = cause
			default:
				m.Tools = []*domain.ToolExecution{{Key: "uncertain", RunID: 3, InvocationID: 1, State: cause}}
			}
			mustApply(t, s, m)
			assertHasUnresolved(t, s, 0, true)
			assertHasUnresolved(t, s, 1, true)
			// The filter is root_run_id, not id or parent_run_id.
			assertHasUnresolved(t, s, 2, false)
			assertHasUnresolved(t, s, 3, false)
			assertHasUnresolved(t, s, 4, false)
			assertHasUnresolved(t, s, 99, false)
		})
	}
}

func TestHasUnresolvedCorrelatesOperationsToStoppedRun(t *testing.T) {
	s, _ := testStore(t)
	mustApply(t, s, domain.Mutation{
		Tasks: []*domain.Task{{ID: 1, Version: 1}, {ID: 2, Version: 1}},
		Runs: []*domain.Run{
			{ID: 1, TaskID: 1, TaskVersion: 1, RootRunID: 1, State: domain.Interrupted},
			{ID: 2, TaskID: 2, TaskVersion: 1, RootRunID: 2, State: domain.Running},
		},
		Invocations: []*domain.Invocation{{ID: 1, RunID: 2, Phase: "model_started"}},
		Tools:       []*domain.ToolExecution{{Key: "active", RunID: 2, InvocationID: 1, State: "started"}},
	})
	assertHasUnresolved(t, s, 0, false)
	assertHasUnresolved(t, s, 1, false)
	assertHasUnresolved(t, s, 2, false)
}

func TestHasUnresolvedNullRootIsGlobalOnly(t *testing.T) {
	s, _ := testStore(t)
	mustApply(t, s, domain.Mutation{
		Tasks: []*domain.Task{{ID: 1, Version: 1}},
		Runs:  []*domain.Run{{ID: 1, TaskID: 1, TaskVersion: 1, State: domain.Reconciling}},
	})
	assertHasUnresolved(t, s, 0, true)
	assertHasUnresolved(t, s, 1, false)
}

func TestHasUnresolvedDoesNotDecodeJSONOrTranscripts(t *testing.T) {
	for _, cause := range []string{"none", "reconciling", "model_started", "unknown"} {
		t.Run(cause, func(t *testing.T) {
			s, _ := testStore(t)
			m := graph()
			m.Runs[0].State = domain.Interrupted
			switch cause {
			case "reconciling":
				m.Runs[0].State = domain.Reconciling
			case "model_started":
				m.Invocations[0].Phase = cause
			case "unknown":
				m.Tools[0].State = cause
			}
			mustApply(t, s, m)
			for _, table := range []string{"runs", "invocations", "tools", "session_messages", "invocation_messages"} {
				if _, err := s.db.Exec("UPDATE " + table + " SET data = 'not JSON'"); err != nil {
					t.Fatal(err)
				}
			}
			assertHasUnresolved(t, s, 0, cause != "none")
			assertHasUnresolved(t, s, 1, cause != "none")
		})
	}
}

func TestHasUnresolvedIndexedCorrelations(t *testing.T) {
	s, _ := testStore(t)
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+hasUnresolvedSQL, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(plan, "\n")
	for _, alias := range []string{"i", "t"} {
		found := false
		for _, detail := range plan {
			if strings.Contains(detail, "SEARCH "+alias+" USING ") && strings.Contains(detail, "run_id=?") {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing indexed run_id correlation for %s:\n%s", alias, text)
		}
	}
}

func TestHasUnresolvedErrors(t *testing.T) {
	s, _ := testStore(t)
	mustApply(t, s, domain.Mutation{
		Tasks: []*domain.Task{{ID: 1, Version: 1}},
		Runs:  []*domain.Run{{ID: 1, TaskID: 1, TaskVersion: 1, RootRunID: 1, State: domain.Reconciling}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, root := range []domain.RunID{0, 1} {
		got, err := s.HasUnresolved(ctx, root)
		if got || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled lookup: %v, %v", got, err)
		}
	}

	// Exhaust the single-connection pool so the deadline expires while waiting,
	// rather than depending on the duration of a query or a sleep.
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got, err := s.HasUnresolved(ctx, 1)
	if got || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline lookup: %v, %v", got, err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	assertHasUnresolved(t, s, 1, true)

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, root := range []domain.RunID{0, 1} {
		got, err := s.HasUnresolved(context.Background(), root)
		if got || err == nil || errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("closed lookup must preserve database error: %v, %v", got, err)
		}
	}
}

func assertHasUnresolved(t *testing.T, s *Store, root domain.RunID, want bool) {
	t.Helper()
	got, err := s.HasUnresolved(context.Background(), root)
	if err != nil || got != want {
		t.Fatalf("HasUnresolved(%d) = %v, %v; want %v, nil", root, got, err, want)
	}
}
