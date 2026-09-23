package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	workspace := t.TempDir()
	s, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, workspace
}

func mustApply(t *testing.T, s *Store, mutation domain.Mutation) {
	t.Helper()
	if err := s.Apply(context.Background(), mutation); err != nil {
		t.Fatal(err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s: mode %o, want %o", path, info.Mode().Perm(), want)
	}
}

func graph() domain.Mutation {
	ref := model.Ref{Provider: "test-provider", Model: "test-model"}
	return domain.Mutation{
		Sessions: []*domain.Session{{ID: 1, Title: "session", Messages: []domain.Message{{ID: 1, TaskID: 1, Role: domain.UserMessage, Content: "user"}}}},
		Tasks:    []*domain.Task{{ID: 1, Version: 1, SessionID: 1, Input: "task"}, {ID: 2, Version: 1, ParentTaskID: 1, Input: "child"}},
		Runs: []*domain.Run{
			{ID: 1, TaskID: 1, TaskVersion: 1, SessionID: 1, RootRunID: 1, State: domain.Running, WaitingID: 1, Model: ref},
			{ID: 2, TaskID: 2, TaskVersion: 1, ParentRunID: 1, RootRunID: 1, State: domain.Queued, Model: ref},
		},
		Invocations: []*domain.Invocation{{ID: 1, RunID: 1, Model: ref, Phase: "ready", Thread: domain.Thread{ContextVersion: 1, Messages: []model.Message{{Role: "user", Content: "prompt"}}, Cursor: &model.Cursor{Adapter: "test", Version: 1, Model: ref, ThreadID: 1, ContextVersion: 1, Sequence: 1, Data: json.RawMessage(`{"position":1}`)}}}},
		Inputs:      []*domain.InputRequest{{ID: 1, RunID: 1, InvocationID: 1, CallKey: "input-call", Kind: "approval", State: "pending"}},
		Tools:       []*domain.ToolExecution{{Key: "tool-call", RunID: 1, InvocationID: 1, Name: "read", State: "prepared"}},
		Delegations: []*domain.Delegation{{Key: "delegate-call", ParentRunID: 1, InvocationID: 1, Children: []domain.RunID{2}}},
		Artifacts:   []*domain.Artifact{{ID: 1, RunID: 1, InvocationID: 1, Kind: "answer", Content: "answer"}},
		Events:      []domain.Event{{RunID: 1, InvocationID: 1, Kind: "created"}, {RunID: 1, Kind: "ready"}},
	}
}

func TestSQLitePragmasPermissionsAndRoundTrip(t *testing.T) {
	s, workspace := testStore(t)
	var journal string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Fatalf("journal mode %s", journal)
	}
	for pragma, want := range map[string]int{"synchronous": 2, "foreign_keys": 1, "busy_timeout": 5000, "user_version": schemaVersion} {
		var value int
		if err := s.db.QueryRow("PRAGMA " + pragma).Scan(&value); err != nil {
			t.Fatal(err)
		}
		if value != want {
			t.Fatalf("%s = %d, want %d", pragma, value, want)
		}
	}
	mutation := graph()
	mustApply(t, s, mutation)
	dir := filepath.Join(workspace, ".orca")
	assertMode(t, dir, 0700)
	for _, name := range []string{"state.sqlite3", "state.sqlite3-wal", "state.sqlite3-shm", "run.lock"} {
		assertMode(t, filepath.Join(dir, name), 0600)
	}
	if mutation.Sessions[0].Version != 1 || mutation.Runs[0].Version != 1 || mutation.Invocations[0].Version != 1 || mutation.Inputs[0].Version != 1 || mutation.Tools[0].Version != 1 {
		t.Fatal("committed versions not updated")
	}
	if mutation.Events[0].Sequence == 0 || mutation.Events[1].Sequence <= mutation.Events[0].Sequence {
		t.Fatal("event sequences not assigned")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	ctx := context.Background()
	session, err := reopened.Session(ctx, 1)
	if err != nil || !reflect.DeepEqual(session, mutation.Sessions[0]) {
		t.Fatalf("session round trip: %#v %v", session, err)
	}
	invocation, err := reopened.Invocation(ctx, 1)
	if err != nil || !reflect.DeepEqual(invocation, mutation.Invocations[0]) {
		t.Fatalf("invocation round trip: %#v %v", invocation, err)
	}
	task, err := reopened.Task(ctx, 1, 0)
	if err != nil || !reflect.DeepEqual(task, mutation.Tasks[0]) {
		t.Fatalf("task round trip: %#v %v", task, err)
	}
	run, err := reopened.Run(ctx, 1)
	if err != nil || !reflect.DeepEqual(run, mutation.Runs[0]) {
		t.Fatalf("run round trip: %#v %v", run, err)
	}
	input, err := reopened.Input(ctx, 1)
	if err != nil || !reflect.DeepEqual(input, mutation.Inputs[0]) {
		t.Fatalf("input round trip: %#v %v", input, err)
	}
	input, err = reopened.InputByCall(ctx, "input-call")
	if err != nil || input.ID != 1 {
		t.Fatalf("input by call: %v %v", input, err)
	}
	tool, err := reopened.Tool(ctx, "tool-call")
	if err != nil || !reflect.DeepEqual(tool, mutation.Tools[0]) {
		t.Fatalf("tool round trip: %#v %v", tool, err)
	}
	delegation, err := reopened.Delegation(ctx, "delegate-call")
	if err != nil || !reflect.DeepEqual(delegation, mutation.Delegations[0]) {
		t.Fatalf("delegation round trip: %#v %v", delegation, err)
	}
	sessions, err := reopened.Sessions(ctx)
	if err != nil || len(sessions) != 1 || !reflect.DeepEqual(sessions[0], *session) {
		t.Fatalf("sessions: %v %v", sessions, err)
	}
	runs, err := reopened.Runs(ctx)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs: %v %v", runs, err)
	}
	invocations, err := reopened.Invocations(ctx, 1)
	if err != nil || len(invocations) != 1 || !reflect.DeepEqual(invocations[0], *invocation) {
		t.Fatalf("invocations: %v %v", invocations, err)
	}
	artifacts, err := reopened.Artifacts(ctx, 1)
	if err != nil || len(artifacts) != 1 || !reflect.DeepEqual(artifacts[0], *mutation.Artifacts[0]) {
		t.Fatalf("artifacts: %v %v", artifacts, err)
	}
	events, err := reopened.Events(ctx, 1, 0, 1)
	if err != nil || len(events) != 1 || events[0].Sequence != mutation.Events[0].Sequence {
		t.Fatalf("events first page: %v %v", events, err)
	}
	events, err = reopened.Events(ctx, 1, events[0].Sequence, 1)
	if err != nil || len(events) != 1 || events[0].Sequence != mutation.Events[1].Sequence {
		t.Fatalf("events second page: %v %v", events, err)
	}
	events, err = reopened.Events(ctx, 1, events[0].Sequence, 0)
	if err != nil || len(events) != 0 {
		t.Fatalf("events final page: %v %v", events, err)
	}
}

func TestExistingPublicDirectoryAndCredentialsUntouched(t *testing.T) {
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".orca")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(dir, "models.json")
	secret := []byte(`{"api_key":"secret-not-for-state","cookie":"credential-cookie","authorization":"private-bearer","password":"dont-copy"}`)
	if err := os.WriteFile(credential, secret, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(credential, 0644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mustApply(t, s, graph())
	assertMode(t, dir, 0755)
	assertMode(t, credential, 0644)
	content, err := os.ReadFile(credential)
	if err != nil || !bytes.Equal(content, secret) {
		t.Fatalf("credentials changed: %v", err)
	}
	for _, name := range []string{"state.sqlite3", "state.sqlite3-wal", "state.sqlite3-shm", "run.lock"} {
		path := filepath.Join(dir, name)
		assertMode(t, path, 0600)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"api_key", "cookie", "authorization", "password", "secret-not-for-state", "credential-cookie", "private-bearer", "dont-copy"} {
			if bytes.Contains(raw, []byte(forbidden)) {
				t.Fatalf("credential field/value %q appears in %s", forbidden, name)
			}
		}
	}
}

func TestCASAndAtomicRollback(t *testing.T) {
	s, _ := testStore(t)
	m := graph()
	mustApply(t, s, m)
	ctx := context.Background()
	m.Sessions[0].Title = "not committed"
	m.Sessions[0].Messages = append(m.Sessions[0].Messages, domain.Message{ID: 2, Content: "not committed"})
	m.Runs[0].Result = "not committed"
	m.Invocations[0].Thread.Messages = append(m.Invocations[0].Thread.Messages, model.Message{Role: "assistant", Content: "not committed"})
	m.Inputs[0].State = "answered"
	m.Tools[0].State = "started"
	newTask := &domain.Task{ID: 1, Version: 2, SessionID: 1, Input: "new version"}
	m.Tasks = append(m.Tasks, newTask)
	m.Artifacts = append(m.Artifacts, &domain.Artifact{ID: 2, RunID: 1, Kind: "not committed"})
	// The duplicate event fails last, after every other entity has been written.
	if err := s.Apply(ctx, m); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want uniqueness conflict, got %v", err)
	}
	if m.Sessions[0].Version != 1 || m.Runs[0].Version != 1 || m.Invocations[0].Version != 1 || m.Inputs[0].Version != 1 || m.Tools[0].Version != 1 || newTask.Version != 2 {
		t.Fatal("failed commit changed in-memory revisions")
	}
	session, _ := s.Session(ctx, 1)
	invocation, _ := s.Invocation(ctx, 1)
	run, _ := s.Run(ctx, 1)
	tool, _ := s.Tool(ctx, "tool-call")
	input, _ := s.Input(ctx, 1)
	artifacts, _ := s.Artifacts(ctx, 1)
	if session.Title != "session" || len(session.Messages) != 1 || len(invocation.Thread.Messages) != 1 || run.Result != "" || tool.State != "prepared" || input.State != "pending" || len(artifacts) != 1 {
		t.Fatal("partial mutation committed")
	}
	if _, err := s.Task(ctx, 1, 2); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("task insertion survived rollback: %v", err)
	}
	// All revisions now move together on success.
	m.Events = nil
	mustApply(t, s, m)
	if m.Sessions[0].Version != 2 || m.Runs[0].Version != 2 || m.Invocations[0].Version != 2 || m.Inputs[0].Version != 2 || m.Tools[0].Version != 2 {
		t.Fatal("revisions did not move together")
	}
	stale := *m.Runs[0]
	stale.Version = 1
	session.Title = "stale write"
	session.Version = 2
	if err := s.Apply(ctx, domain.Mutation{Sessions: []*domain.Session{session}, Runs: []*domain.Run{&stale}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want CAS conflict, got %v", err)
	}
	if session.Version != 2 {
		t.Fatal("rollback advanced session version")
	}
	stored, _ := s.Session(ctx, 1)
	if stored.Title == "stale write" {
		t.Fatal("CAS conflict partially committed")
	}
	// A positive expected revision never inserts a missing record.
	missing := &domain.Session{ID: 999, Version: 3}
	if err := s.Apply(ctx, domain.Mutation{Sessions: []*domain.Session{missing}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("missing CAS: %v", err)
	}
	duplicate := &domain.Session{ID: 1}
	if err := s.Apply(ctx, domain.Mutation{Sessions: []*domain.Session{duplicate}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate insertion: %v", err)
	}
}

func TestDeferredForeignKeysRollbackAndPreserveErrors(t *testing.T) {
	cases := map[string]func() domain.Mutation{
		"task session": func() domain.Mutation {
			return domain.Mutation{Tasks: []*domain.Task{{ID: 1, Version: 1, SessionID: 99}}}
		},
		"task parent": func() domain.Mutation {
			return domain.Mutation{Tasks: []*domain.Task{{ID: 1, Version: 1, ParentTaskID: 99}}}
		},
		"message task": func() domain.Mutation {
			return domain.Mutation{Sessions: []*domain.Session{{ID: 2, Messages: []domain.Message{{TaskID: 99}}}}}
		},
		"task version":          func() domain.Mutation { m := graph(); m.Runs[0].TaskVersion = 99; return m },
		"run root":              func() domain.Mutation { m := graph(); m.Runs[0].RootRunID = 99; return m },
		"invocation run":        func() domain.Mutation { m := graph(); m.Invocations[0].RunID = 99; return m },
		"input invocation":      func() domain.Mutation { m := graph(); m.Inputs[0].InvocationID = 99; return m },
		"input wrong run":       func() domain.Mutation { m := graph(); m.Inputs[0].RunID = 2; return m },
		"waiting input":         func() domain.Mutation { m := graph(); m.Runs[0].WaitingID = 99; return m },
		"tool invocation":       func() domain.Mutation { m := graph(); m.Tools[0].InvocationID = 99; return m },
		"delegation invocation": func() domain.Mutation { m := graph(); m.Delegations[0].InvocationID = 99; return m },
		"delegation child":      func() domain.Mutation { m := graph(); m.Delegations[0].Children = []domain.RunID{99}; return m },
		"artifact invocation":   func() domain.Mutation { m := graph(); m.Artifacts[0].InvocationID = 99; return m },
		"event invocation":      func() domain.Mutation { m := graph(); m.Events[1].InvocationID = 99; return m },
	}
	for name, makeMutation := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := testStore(t)
			session := &domain.Session{ID: 100, Title: "original"}
			mustApply(t, s, domain.Mutation{Sessions: []*domain.Session{session}})
			session.Title = "not committed"
			m := makeMutation()
			m.Sessions = append(m.Sessions, session)
			err := s.Apply(context.Background(), m)
			if err == nil || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("foreign key error must be preserved: %v", err)
			}
			if session.Version != 1 {
				t.Fatal("failed COMMIT advanced revision")
			}
			stored, _ := s.Session(context.Background(), 100)
			if stored.Title != "original" {
				t.Fatal("failed COMMIT persisted metadata")
			}
			for _, run := range m.Runs {
				if run.Version != 0 {
					t.Fatal("failed COMMIT advanced inserted run revision")
				}
			}
		})
	}
}

func TestTaskVersionsAndActiveRunUniqueness(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	task := &domain.Task{ID: 1, Version: 1, Input: "first"}
	run := &domain.Run{ID: 1, TaskID: 1, TaskVersion: 1, State: domain.Running}
	mustApply(t, s, domain.Mutation{Tasks: []*domain.Task{task}, Runs: []*domain.Run{run}})
	mustApply(t, s, domain.Mutation{Tasks: []*domain.Task{task}})
	changed := *task
	changed.Input = "changed"
	if err := s.Apply(ctx, domain.Mutation{Tasks: []*domain.Task{&changed}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("mutable task accepted: %v", err)
	}
	changed.Version = 2
	mustApply(t, s, domain.Mutation{Tasks: []*domain.Task{&changed}})
	latest, err := s.Task(ctx, 1, 0)
	if err != nil || latest.Version != 2 || latest.Input != "changed" {
		t.Fatalf("latest task: %v %v", latest, err)
	}
	previous, _ := s.Task(ctx, 1, 1)
	if previous.Input != "first" {
		t.Fatal("previous task overwritten")
	}
	next := &domain.Run{ID: 2, TaskID: 1, TaskVersion: 2, State: domain.Queued}
	if err := s.Apply(ctx, domain.Mutation{Runs: []*domain.Run{next}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second active task run accepted: %v", err)
	}
	if next.Version != 0 {
		t.Fatal("conflict advanced version")
	}
	run.State = domain.Succeeded
	mustApply(t, s, domain.Mutation{Runs: []*domain.Run{next, run}})
	if next.Version != 1 {
		t.Fatal("terminal run did not release task")
	}
}

func TestIllegalTransitionsAndNoTerminalRevival(t *testing.T) {
	for _, states := range [][2]domain.RunState{{domain.Queued, domain.Succeeded}, {domain.Running, domain.Queued}, {domain.Succeeded, domain.Running}, {domain.Failed, domain.Reconciling}, {domain.Cancelled, domain.Running}} {
		t.Run(string(states[0])+"-"+string(states[1]), func(t *testing.T) {
			s, _ := testStore(t)
			run := &domain.Run{ID: 1, TaskID: 1, TaskVersion: 1, State: states[0]}
			mustApply(t, s, domain.Mutation{Tasks: []*domain.Task{{ID: 1, Version: 1}}, Runs: []*domain.Run{run}})
			run.State = states[1]
			if err := s.Apply(context.Background(), domain.Mutation{Runs: []*domain.Run{run}}); err == nil {
				t.Fatal("illegal transition accepted")
			}
			saved, _ := s.Run(context.Background(), 1)
			if saved.State != states[0] || saved.Version != 1 || run.Version != 1 {
				t.Fatal("illegal transition changed state")
			}
		})
	}
}

func TestAppendOnlyTranscriptsAndContextArchives(t *testing.T) {
	s, _ := testStore(t)
	m := graph()
	mustApply(t, s, m)
	// Reject any rewriting/deletion of old rows, even a same-content rewrite.
	for _, table := range []string{"session_messages", "invocation_messages"} {
		for _, operation := range []string{"UPDATE", "DELETE"} {
			statement := fmt.Sprintf("CREATE TRIGGER guard_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'transcript rewritten'); END", table, operation, operation, table)
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	session := m.Sessions[0]
	invocation := m.Invocations[0]
	session.Messages = append(session.Messages, domain.Message{ID: 2, Role: domain.AssistantMessage, Content: "second"})
	invocation.Thread.Messages = append(invocation.Thread.Messages, model.Message{Role: "assistant", Content: "second"})
	for range 3 {
		mustApply(t, s, domain.Mutation{Sessions: []*domain.Session{session}, Invocations: []*domain.Invocation{invocation}})
	}
	ctx := context.Background()
	for _, table := range []string{"sessions", "invocations"} {
		var metadata string
		if err := s.db.QueryRow("SELECT data FROM " + table + " WHERE id = 1").Scan(&metadata); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(metadata, `"messages"`) || strings.Contains(metadata, `"second"`) || strings.Contains(metadata, `"prompt"`) {
			t.Fatalf("transcript leaked into %s metadata: %s", table, metadata)
		}
	}
	for _, table := range []string{"session_messages", "invocation_messages"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("%s row count = %d", table, count)
		}
	}
	messages, err := s.SessionMessages(ctx, 1, 1, 1)
	if err != nil || len(messages) != 1 || messages[0].Content != "second" {
		t.Fatalf("session page: %v %v", messages, err)
	}
	for _, change := range []string{"edit", "delete"} {
		t.Run(change, func(t *testing.T) {
			badSession, _ := s.Session(ctx, 1)
			badInvocation, _ := s.Invocation(ctx, 1)
			if change == "edit" {
				badSession.Messages[0].Content = "tampered"
				badInvocation.Thread.Messages[0].Content = "tampered"
			} else {
				badSession.Messages = badSession.Messages[:1]
				badInvocation.Thread.Messages = badInvocation.Thread.Messages[:1]
			}
			for _, mutation := range []domain.Mutation{{Sessions: []*domain.Session{badSession}}, {Invocations: []*domain.Invocation{badInvocation}}} {
				if err := s.Apply(ctx, mutation); !errors.Is(err, domain.ErrConflict) {
					t.Fatalf("prefix modification accepted: %v", err)
				}
			}
		})
	}
	invocation.Thread.ContextVersion = 2
	invocation.Thread.Messages = []model.Message{{Role: "user", Content: "new context"}}
	invocation.Thread.Cursor = nil
	mustApply(t, s, domain.Mutation{Invocations: []*domain.Invocation{invocation}})
	saved, err := s.Invocation(ctx, 1)
	if err != nil || len(saved.Thread.Messages) != 1 || saved.Thread.Messages[0].Content != "new context" {
		t.Fatalf("context read: %v %v", saved, err)
	}
	archived, err := readMany[model.Message](ctx, s.db, "SELECT data FROM invocation_messages WHERE invocation_id = 1 AND context_version = 1 ORDER BY sequence")
	if err != nil || len(archived) != 2 || archived[0].Content != "prompt" {
		t.Fatalf("archive lost: %v %v", archived, err)
	}
	invocation.Thread.ContextVersion = 1
	invocation.Thread.Messages = archived
	if err := s.Apply(ctx, domain.Mutation{Invocations: []*domain.Invocation{invocation}}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("archived context revived: %v", err)
	}
}

func TestRecoveryMatrix(t *testing.T) {
	states := []domain.RunState{domain.Queued, domain.Running, domain.Waiting, domain.Interrupted, domain.Reconciling, domain.Cancelling, domain.Succeeded, domain.Failed, domain.Cancelled}
	for _, state := range states {
		for _, uncertainty := range []string{"none", "model_started", "started", "unknown"} {
			t.Run(string(state)+"/"+uncertainty, func(t *testing.T) {
				s, _ := testStore(t)
				ctx := context.Background()
				run := &domain.Run{ID: 1, TaskID: 1, TaskVersion: 1, State: state}
				invocation := &domain.Invocation{ID: 1, RunID: 1, Phase: "ready", Thread: domain.Thread{Messages: []model.Message{{Role: "user", Content: "preserve"}}}}
				tool := &domain.ToolExecution{Key: "call", RunID: 1, InvocationID: 1, State: "completed"}
				if uncertainty == "model_started" {
					invocation.Phase = uncertainty
				}
				if uncertainty == "started" || uncertainty == "unknown" {
					tool.State = uncertainty
				}
				mustApply(t, s, domain.Mutation{Tasks: []*domain.Task{{ID: 1, Version: 1}}, Runs: []*domain.Run{run}, Invocations: []*domain.Invocation{invocation}, Tools: []*domain.ToolExecution{tool}})
				err := s.Recover(ctx)
				terminalUnknown := state.Terminal() && uncertainty != "none"
				if terminalUnknown {
					if !errors.Is(err, domain.ErrUnknown) {
						t.Fatalf("terminal unresolved operation silently ignored: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				saved, err := s.Run(ctx, 1)
				if err != nil {
					t.Fatal(err)
				}
				want := state
				if !state.Terminal() {
					if uncertainty != "none" {
						want = domain.Reconciling
					} else if state == domain.Running {
						want = domain.Interrupted
					} else if state == domain.Cancelling {
						want = domain.Cancelled
					}
				}
				if saved.State != want {
					t.Fatalf("recovered %s, want %s", saved.State, want)
				}
				savedTool, _ := s.Tool(ctx, "call")
				if uncertainty == "started" && savedTool.State != "unknown" {
					t.Fatal("started tool not marked unknown")
				}
				savedInvocation, _ := s.Invocation(ctx, 1)
				if savedInvocation.Version != invocation.Version || !reflect.DeepEqual(savedInvocation.Thread.Messages, invocation.Thread.Messages) {
					t.Fatal("recovery changed model transcript")
				}
				if err := s.Recover(ctx); err != nil && !terminalUnknown {
					t.Fatal(err)
				}
				twice, _ := s.Run(ctx, 1)
				toolTwice, _ := s.Tool(ctx, "call")
				if twice.Version != saved.Version || twice.State != saved.State || toolTwice.Version != savedTool.Version {
					t.Fatal("recovery is not idempotent")
				}
				if state.Terminal() && saved.Version != run.Version {
					t.Fatal("terminal run was modified")
				}
			})
		}
	}
}

func TestMissingRecords(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	lookups := []func() error{
		func() error { _, err := s.Session(ctx, 99); return err },
		func() error { _, err := s.SessionMessages(ctx, 99, 0, 10); return err },
		func() error { _, err := s.Task(ctx, 99, 0); return err },
		func() error { _, err := s.Task(ctx, 99, 1); return err },
		func() error { _, err := s.Run(ctx, 99); return err },
		func() error { _, err := s.Invocation(ctx, 99); return err },
		func() error { _, err := s.Input(ctx, 99); return err },
		func() error { _, err := s.InputByCall(ctx, "missing"); return err },
		func() error { _, err := s.InputByCall(ctx, ""); return err },
		func() error { _, err := s.Tool(ctx, "missing"); return err },
		func() error { _, err := s.Delegation(ctx, "missing"); return err },
	}
	for i, lookup := range lookups {
		if err := lookup(); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("lookup %d: %v", i, err)
		}
	}
}

func TestConcurrentCASAndCancellation(t *testing.T) {
	s, _ := testStore(t)
	run := &domain.Run{ID: 1, TaskID: 1, TaskVersion: 1, State: domain.Running}
	mustApply(t, s, domain.Mutation{Tasks: []*domain.Task{{ID: 1, Version: 1}}, Runs: []*domain.Run{run}})
	results := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		copy := *run
		copy.Budget.Turns++
		wg.Go(func() { results <- s.Apply(context.Background(), domain.Mutation{Runs: []*domain.Run{&copy}}) })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, domain.ErrConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("%d CAS winners", successes)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session := &domain.Session{ID: 9}
	if err := s.Apply(ctx, domain.Mutation{Sessions: []*domain.Session{session}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %v", err)
	}
	if session.Version != 0 {
		t.Fatal("cancelled write advanced revision")
	}
	if _, err := s.Session(context.Background(), 9); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cancelled insertion persisted: %v", err)
	}
}

func TestStoreLockSubprocess(t *testing.T) {
	if workspace := os.Getenv("ORCA_TEST_LOCK_WORKSPACE"); workspace != "" {
		s, err := Open(workspace)
		if s != nil {
			s.Close()
		}
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("child acquired lock: %v", err)
		}
		return
	}
	s, workspace := testStore(t)
	other, err := Open(workspace)
	if other != nil {
		other.Close()
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("same-process lock not exclusive: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestStoreLockSubprocess$", "-test.count=1")
	cmd.Env = append(os.Environ(), "ORCA_TEST_LOCK_WORKSPACE="+workspace)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock failed: %v\n%s", err, output)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	other, err = Open(workspace)
	if err != nil {
		t.Fatalf("Close did not release lock: %v", err)
	}
	other.Close()
}

func TestRejectSymlinksAndHardlinksWithoutTouchingCredentials(t *testing.T) {
	for _, name := range []string{"state.sqlite3", "run.lock", "state.sqlite3-wal", "state.sqlite3-shm", "state.sqlite3-journal"} {
		for _, linkType := range []string{"symlink", "hardlink"} {
			t.Run(name+"/"+linkType, func(t *testing.T) {
				workspace := t.TempDir()
				dir := filepath.Join(workspace, ".orca")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				credential := filepath.Join(workspace, "credential.json")
				original := []byte(`{"secret":"do-not-touch"}`)
				if err := os.WriteFile(credential, original, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(credential, 0644); err != nil {
					t.Fatal(err)
				}
				link := os.Symlink
				if linkType == "hardlink" {
					link = os.Link
				}
				if err := link(credential, filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
				s, err := Open(workspace)
				if s != nil {
					s.Close()
				}
				if err == nil {
					t.Fatal("linked state file accepted")
				}
				assertMode(t, credential, 0644)
				got, err := os.ReadFile(credential)
				if err != nil || !bytes.Equal(got, original) {
					t.Fatalf("credentials changed: %v", err)
				}
			})
		}
	}
}

func TestRecoveryAfterProcessExit(t *testing.T) {
	if workspace := os.Getenv("ORCA_TEST_CRASH_WORKSPACE"); workspace != "" {
		s, err := Open(workspace)
		if err != nil {
			t.Fatal(err)
		}
		mutation := graph()
		mutation.Tools[0].State = "started"
		mutation.Invocations[0].Phase = "model_started"
		mustApply(t, s, mutation)
		// Deliberately bypass Close and SQLite's checkpoint-on-close. The OS
		// releases flock, but recovery must read the committed WAL itself.
		os.Exit(0)
	}
	workspace := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestRecoveryAfterProcessExit$", "-test.count=1")
	cmd.Env = append(os.Environ(), "ORCA_TEST_CRASH_WORKSPACE="+workspace)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("writer process failed: %v\n%s", err, output)
	}
	info, err := os.Stat(filepath.Join(workspace, ".orca", "state.sqlite3-wal"))
	if err != nil || info.Size() == 0 {
		t.Fatalf("writer did not leave a WAL: %v", err)
	}
	s, err := Open(workspace)
	if err != nil {
		t.Fatalf("unclean process exit did not release flock: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := s.Run(ctx, 1)
	if err != nil || run.State != domain.Reconciling {
		t.Fatalf("WAL run recovery: %v %v", run, err)
	}
	tool, err := s.Tool(ctx, "tool-call")
	if err != nil || tool.State != "unknown" {
		t.Fatalf("WAL ledger recovery: %v %v", tool, err)
	}
	invocation, err := s.Invocation(ctx, 1)
	if err != nil || len(invocation.Thread.Messages) != 1 || invocation.Thread.Messages[0].Content != "prompt" {
		t.Fatalf("WAL transcript recovery: %v %v", invocation, err)
	}
}

func TestSchemaVersionRejectionAndLockCleanup(t *testing.T) {
	for _, version := range []int{-1, 0, schemaVersion + 1, 42} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s, workspace := testStore(t)
			if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			rejected, err := Open(workspace)
			if rejected != nil {
				rejected.Close()
			}
			if err == nil {
				t.Fatalf("schema %d accepted", version)
			}
			db, err := sql.Open("sqlite", filepath.Join(workspace, ".orca", "state.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
				db.Close()
				t.Fatal(err)
			}
			db.Close()
			reopened, err := Open(workspace)
			if err != nil {
				t.Fatalf("rejected Open leaked the lock: %v", err)
			}
			reopened.Close()
		})
	}
}

func TestSchemaOneMigrationPreservesEventsAndMessageIdentity(t *testing.T) {
	s, workspace := testStore(t)
	mustApply(t, s, graph())
	if _, err := s.db.Exec("ALTER TABLE events DROP COLUMN assistant_sequence; PRAGMA user_version = 1;"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	ctx := context.Background()
	events, err := reopened.Events(ctx, 1, 0, 100)
	if err != nil || len(events) != 2 || events[0].Kind != "created" || events[0].AssistantSequence != 0 {
		t.Fatalf("legacy events changed: %+v %v", events, err)
	}
	inv, err := reopened.Invocation(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	inv.Thread.SequenceOffset = 100
	inv.AssistantSequence = 102
	inv.Thread.Messages = append(inv.Thread.Messages, model.Message{Role: "assistant", Content: "confirmed"})
	mustApply(t, reopened, domain.Mutation{Invocations: []*domain.Invocation{inv}, Events: []domain.Event{{RunID: 1, InvocationID: 1, AssistantSequence: 102, Kind: "message", Content: "confirmed"}}})
	events, err = reopened.Events(ctx, 1, events[1].Sequence, 100)
	if err != nil || len(events) != 1 || events[0].AssistantSequence != 102 || events[0].Content != "confirmed" {
		t.Fatalf("message identity lost: %+v %v", events, err)
	}
	inv, err = reopened.Invocation(ctx, 1)
	if err != nil || inv.Thread.SequenceOffset != 100 || inv.AssistantSequence != 102 {
		t.Fatalf("thread identity lost: %+v %v", inv, err)
	}
}
