package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

func benchmarkStore(b *testing.B) *Store {
	b.Helper()
	s, err := Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := s.Close(); err != nil {
			b.Error(err)
		}
	})
	// Exercise the actual durable store, never an in-memory or relaxed-sync DB.
	var journal string
	var synchronous int
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		b.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		b.Fatal(err)
	}
	if journal != "wal" || synchronous != 2 {
		b.Fatalf("want WAL/FULL, got %s/%d", journal, synchronous)
	}
	return s
}

func benchmarkApply(b *testing.B, s *Store, m domain.Mutation) {
	b.Helper()
	if err := s.Apply(context.Background(), m); err != nil {
		b.Fatal(err)
	}
}

func benchmarkInvocationHistory(b *testing.B, s *Store, count, messages int) *domain.Run {
	b.Helper()
	run := &domain.Run{ID: 1, TaskID: 1, TaskVersion: 1, RootRunID: 1, State: domain.Interrupted}
	m := domain.Mutation{
		Tasks: []*domain.Task{{ID: 1, Version: 1}},
		Runs:  []*domain.Run{run},
	}
	transcript := make([]model.Message, messages)
	for i := range transcript {
		transcript[i] = model.Message{Role: "assistant", Content: strings.Repeat("x", 512)}
	}
	for i := 1; i <= count; i++ {
		m.Invocations = append(m.Invocations, &domain.Invocation{
			ID: domain.InvocationID(i), RunID: 1, Phase: "completed",
			Thread: domain.Thread{ContextVersion: 1, Messages: transcript},
		})
	}
	benchmarkApply(b, s, m)
	return run
}

func BenchmarkHasUnresolved(b *testing.B) {
	for _, history := range []struct {
		name        string
		invocations int
		messages    int
	}{
		{name: "short", invocations: 100, messages: 4},
		{name: "long_transcript", invocations: 1, messages: 10000},
		{name: "long_invocation_history", invocations: 10000, messages: 4},
	} {
		b.Run(history.name, func(b *testing.B) {
			s := benchmarkStore(b)
			run := benchmarkInvocationHistory(b, s, history.invocations, history.messages)
			for _, state := range []struct {
				name  string
				run   domain.RunState
				phase string
				want  bool
			}{
				{name: "running", run: domain.Running, phase: "model_started"},
				{name: "stopped_clear", run: domain.Interrupted, phase: "completed"},
				{name: "stopped_uncertain", run: domain.Interrupted, phase: "model_started", want: true},
			} {
				b.Run(state.name, func(b *testing.B) {
					run.State = state.run
					benchmarkApply(b, s, domain.Mutation{Runs: []*domain.Run{run}})
					invocation, err := s.Invocation(context.Background(), domain.InvocationID(history.invocations))
					if err != nil {
						b.Fatal(err)
					}
					invocation.Phase = state.phase
					benchmarkApply(b, s, domain.Mutation{Invocations: []*domain.Invocation{invocation}})
					for _, scope := range []struct {
						name string
						root domain.RunID
					}{{name: "global"}, {name: "tree", root: 1}} {
						b.Run(scope.name, func(b *testing.B) {
							ctx := context.Background()
							b.ReportAllocs()
							for b.Loop() {
								got, err := s.HasUnresolved(ctx, scope.root)
								if err != nil || got != state.want {
									b.Fatalf("HasUnresolved = %v, %v; want %v", got, err, state.want)
								}
							}
						})
					}
				})
			}
		})
	}
}

func BenchmarkSessionMessagesBoundedPagination(b *testing.B) {
	const pageSize = 50
	for _, history := range []int{100, 10000} {
		b.Run(fmt.Sprintf("history_%d", history), func(b *testing.B) {
			s := benchmarkStore(b)
			session := &domain.Session{ID: 1, Messages: make([]domain.Message, history)}
			for i := range session.Messages {
				session.Messages[i] = domain.Message{ID: int64(i + 1), Role: domain.UserMessage, Content: strings.Repeat("x", 512)}
			}
			benchmarkApply(b, s, domain.Mutation{Sessions: []*domain.Session{session}})
			for _, page := range []struct {
				name  string
				after int64
			}{{name: "first"}, {name: "last", after: int64(history - pageSize)}} {
				b.Run(page.name, func(b *testing.B) {
					ctx := context.Background()
					b.ReportAllocs()
					for b.Loop() {
						messages, err := s.SessionMessages(ctx, 1, page.after, pageSize)
						if err != nil || len(messages) != pageSize {
							b.Fatalf("page length = %d, error = %v", len(messages), err)
						}
						if messages[0].ID != page.after+1 || messages[pageSize-1].ID != page.after+pageSize {
							b.Fatal("incorrect page boundaries")
						}
					}
				})
			}
		})
	}
}

// Each measured checkpoint appends one message to both transcripts via Apply.
// Use a fixed count (for example -benchtime=20x) to compare growing histories;
// final_messages reports the resulting length of each transcript. The explicit
// WAL checkpoint variant also measures copying committed WAL frames back to DB.
func BenchmarkFullWALAppendCheckpoint(b *testing.B) {
	for _, history := range []int{100, 10000} {
		for _, fullCheckpoint := range []bool{false, true} {
			name := "apply"
			if fullCheckpoint {
				name = "apply_and_wal_checkpoint"
			}
			b.Run(fmt.Sprintf("history_%d/%s", history, name), func(b *testing.B) {
				s := benchmarkStore(b)
				content := strings.Repeat("x", 512)
				session := &domain.Session{ID: 1, Messages: make([]domain.Message, history)}
				invocation := &domain.Invocation{
					ID: 1, RunID: 1, Phase: "ready",
					Thread: domain.Thread{ContextVersion: 1, Messages: make([]model.Message, history)},
				}
				for i := range history {
					session.Messages[i] = domain.Message{ID: int64(i + 1), Role: domain.AssistantMessage, Content: content}
					invocation.Thread.Messages[i] = model.Message{Role: "assistant", Content: content}
				}
				benchmarkApply(b, s, domain.Mutation{
					Sessions:    []*domain.Session{session},
					Tasks:       []*domain.Task{{ID: 1, Version: 1, SessionID: 1}},
					Runs:        []*domain.Run{{ID: 1, TaskID: 1, TaskVersion: 1, SessionID: 1, RootRunID: 1, State: domain.Running}},
					Invocations: []*domain.Invocation{invocation},
				})
				// Start with the fixture fully checkpointed; setup cost is excluded.
				benchmarkWALCheckpoint(b, s)
				mutation := domain.Mutation{Sessions: []*domain.Session{session}, Invocations: []*domain.Invocation{invocation}}
				ctx := context.Background()
				b.ReportAllocs()
				for b.Loop() {
					session.Messages = append(session.Messages, domain.Message{ID: int64(len(session.Messages) + 1), Role: domain.AssistantMessage, Content: content})
					invocation.Thread.Messages = append(invocation.Thread.Messages, model.Message{Role: "assistant", Content: content})
					if err := s.Apply(ctx, mutation); err != nil {
						b.Fatal(err)
					}
					if fullCheckpoint {
						benchmarkWALCheckpoint(b, s)
					}
				}
				b.ReportMetric(float64(len(session.Messages)), "final_messages")
			})
		}
	}
}

func benchmarkWALCheckpoint(b *testing.B, s *Store) {
	b.Helper()
	var busy, frames, checkpointed int
	if err := s.db.QueryRow("PRAGMA wal_checkpoint(FULL)").Scan(&busy, &frames, &checkpointed); err != nil {
		b.Fatal(err)
	}
	if busy != 0 || frames != checkpointed {
		b.Fatalf("incomplete WAL checkpoint: busy=%d frames=%d checkpointed=%d", busy, frames, checkpointed)
	}
}
