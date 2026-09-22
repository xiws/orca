package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/utils"
)

func isolatedPaths(t *testing.T) (project, home string) {
	t.Helper()
	root := t.TempDir()
	project, home = filepath.Join(root, "project"), filepath.Join(root, "home")
	for _, path := range []string{project, home} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("WORKSPACE", project)
	t.Setenv("HOME", home)
	if utils.GetCurrentPath() != project {
		t.Fatal("WORKSPACE does not isolate GetCurrentPath")
	}
	return project, home
}

func testRecord(project string) *Record {
	return &Record{
		Session: &domain.Session{
			ID: 101, Title: "saved session", ProjectPath: project, CreateTime: 100, UpdateTime: 200,
			Messages: []domain.Message{
				{ID: 201, TaskID: 301, Role: domain.UserMessage, Content: "inspect project", CreateTime: 101},
				{ID: 202, TaskID: 301, Role: domain.AssistantMessage, Content: "inspection complete", CreateTime: 102},
			},
		},
		Tasks: []*domain.Task{{
			ID: 301, SessionID: 101, Input: "inspect project", Title: "inspection",
			Specification: &domain.Specification{
				Goal: "understand project", Constraints: []string{"offline"},
				Requirements:       []domain.Requirement{{ID: "R1", Description: "keep history", Priority: "high"}},
				AcceptanceCriteria: []string{"history intact"}, Assumptions: []string{"local files"},
			},
		}},
	}
}

func testInvocation(project string) *core.Invocation {
	return &core.Invocation{
		ID: 401, TaskID: 301, ProjectPath: project,
		Provider: llm.ModelInfo{
			Provider: "test-provider", ModelID: "test-model", APIKey: "secret-not-for-disk",
			BaseURL: "https://private.invalid", API: "openai-completions", AllowedTools: []string{"private-tool"},
		},
		Messages: []llm.ChatMessage{
			{Id: 501, Role: llm.RoleSystem, Content: "system prompt", CreateTime: 101},
			{Id: 502, Role: llm.RoleUser, Content: "inspect", CreateTime: 102},
			{Id: 503, Role: llm.RoleAssistant, Content: "checking", CreateTime: 103, ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: "read", Arguments: `{"path":"a.go"}`, Reasoning: "inspect source"},
				{ID: "call-2", Name: "list", Arguments: `{"path":"."}`, Reasoning: "inspect layout"},
			}},
			{Id: 504, Role: llm.RoleTool, Content: "source", ToolCallID: "call-1", CreateTime: 104},
			{Id: 505, Role: llm.RoleTool, Content: "layout", ToolCallID: "call-2", CreateTime: 105},
			{Id: 506, Role: llm.RoleAssistant, Content: "complete", CreateTime: 106},
		},
		TotalUsage: llm.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
		OtterState: &llm.OtterState{
			ChatSessionID: "remote-1", ParentMessageID: 8, ParentMessageIDStr: "parent-8", Delivered: 6,
			RemoteMetadata: map[string]string{"cursor": "cursor-1"},
		},
		Children: []*core.Invocation{{
			ID: 402, TaskID: 301, ProjectPath: project,
			Provider:   llm.ModelInfo{Provider: "child-provider", ModelID: "child-model", APIKey: "child-secret"},
			Messages:   []llm.ChatMessage{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "child-call", Name: "read", Arguments: "{}"}}}},
			TotalUsage: llm.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
			OtterState: &llm.OtterState{RemoteMetadata: map[string]string{"cursor": "child-cursor"}},
			Children: []*core.Invocation{{
				ID: 403, TaskID: 301, ProjectPath: project, TotalUsage: llm.Usage{TotalTokens: 2},
			}},
		}},
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func assertNoCredentials(t *testing.T, data []byte) {
	t.Helper()
	for _, forbidden := range []string{"apikey", "api_key", "allowedtools", "allowed_tools", "baseurl", "base_url", "secret-not-for-disk", "child-secret", "legacy-secret", "private-tool", "private.invalid"} {
		if bytes.Contains(bytes.ToLower(data), []byte(forbidden)) {
			t.Errorf("new file contains forbidden provider data %q", forbidden)
		}
	}
}

func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("%s permissions = %o, want %o", path, info.Mode().Perm(), mode)
	}
}

func TestV2RoundTrip(t *testing.T) {
	project, _ := isolatedPaths(t)
	r := testRecord(project)
	r.Capture(testInvocation(project))
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(101)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, loaded) {
		t.Fatalf("roundtrip changed record:\nwant %#v\ngot %#v", r, loaded)
	}
	path := sessionPath(getProjectSessionDir(), 101)
	data := mustRead(t, path)
	assertNoCredentials(t, data)
	var file map[string]json.RawMessage
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if string(file["version"]) != "2" || file["session"] == nil || file["tasks"] == nil || file["invocations"] == nil {
		t.Fatalf("invalid v2 envelope: %s", data)
	}
	var sessionFields map[string]json.RawMessage
	if err := json.Unmarshal(file["session"], &sessionFields); err != nil {
		t.Fatal(err)
	}
	if len(sessionFields) != 6 {
		t.Fatalf("domain session contains unexpected state: %s", file["session"])
	}
	for _, key := range []string{"id", "title", "project_path", "messages", "create_time", "update_time"} {
		if _, ok := sessionFields[key]; !ok {
			t.Errorf("domain session missing %s", key)
		}
	}
	assertMode(t, path, 0600)
	assertMode(t, getProjectSessionDir(), 0700)
	assertMode(t, filepath.Join(project, ".orca"), 0700)
	metas := List()
	if len(metas) != 1 || metas[0].MessageCount != 2 || metas[0].TotalTokens != 25 {
		t.Fatalf("wrong metadata: %+v", metas)
	}
	if loaded.LastModel() != (ModelRef{Provider: "test-provider", ModelID: "test-model"}) {
		t.Fatalf("wrong last model: %+v", loaded.LastModel())
	}
}

func TestCaptureIsAnIndependentSnapshot(t *testing.T) {
	project, _ := isolatedPaths(t)
	r := testRecord(project)
	inv := testInvocation(project)
	r.Capture(inv)
	snapshot := &r.Invocations[0]
	inv.Messages[0].Content = "changed"
	inv.Messages[2].ToolCalls[0].Arguments = "changed arguments"
	inv.OtterState.Delivered = 99
	inv.OtterState.RemoteMetadata["cursor"] = "changed cursor"
	inv.Children[0].Messages[0].ToolCalls[0].Name = "changed tool"
	inv.Children[0].OtterState.RemoteMetadata["cursor"] = "changed child cursor"
	inv.Children[0].Children[0].TotalUsage.TotalTokens = 9
	inv.TotalUsage.TotalTokens = 20
	inv.Provider.AllowedTools[0] = "changed permission"
	if snapshot.Messages[0].Content != "system prompt" || snapshot.Messages[2].ToolCalls[0].Arguments != `{"path":"a.go"}` ||
		snapshot.OtterState.Delivered != 6 || snapshot.OtterState.RemoteMetadata["cursor"] != "cursor-1" ||
		snapshot.Children[0].Messages[0].ToolCalls[0].Name != "read" ||
		snapshot.Children[0].OtterState.RemoteMetadata["cursor"] != "child-cursor" ||
		snapshot.Children[0].Children[0].TotalUsage.TotalTokens != 2 || snapshot.TotalUsage.TotalTokens != 18 {
		t.Fatal("snapshot shares mutable state with invocation")
	}
	snapshot.Messages[2].ToolCalls[1].Name = "snapshot-only"
	if inv.Messages[2].ToolCalls[1].Name != "list" {
		t.Fatal("mutating snapshot changes source tool calls")
	}
	inv.Children = append(inv.Children, &core.Invocation{ID: 404, TaskID: 301, ProjectPath: project, TotalUsage: llm.Usage{TotalTokens: 1}})
	r.Capture(inv)
	r.Capture(inv)
	if len(r.Invocations) != 1 || len(r.Invocations[0].Children) != 2 || invocationTokens(r.Invocations) != 35 {
		t.Fatalf("repeated capture duplicated execution or usage: %+v", r.Invocations)
	}
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	if metas := List(); len(metas) != 1 || metas[0].TotalTokens != 35 {
		t.Fatalf("capture usage counted twice: %+v", metas)
	}
}

func TestMultipleTasksInOneSession(t *testing.T) {
	project, _ := isolatedPaths(t)
	r := testRecord(project)
	first := testInvocation(project)
	r.Capture(first)
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	r, err := Load(101)
	if err != nil {
		t.Fatal(err)
	}
	r.Tasks = append(r.Tasks, &domain.Task{ID: 302, SessionID: 101, Input: "next task", Title: "follow-up"})
	r.Session.Messages = append(r.Session.Messages,
		domain.Message{ID: 203, TaskID: 302, Role: domain.UserMessage, Content: "next task", CreateTime: 201},
		domain.Message{ID: 204, TaskID: 302, Role: domain.AssistantMessage, Content: "done", CreateTime: 202})
	r.Capture(&core.Invocation{
		ID: 405, TaskID: 302, ProjectPath: project,
		Provider: llm.ModelInfo{Provider: "next-provider", ModelID: "next-model"}, TotalUsage: llm.Usage{TotalTokens: 8},
	})
	r.Capture(first)
	if r.LastModel().Provider != "next-provider" {
		t.Fatal("recapturing an older execution changed the latest root")
	}
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(101)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r, loaded) || len(loaded.Tasks) != 2 || loaded.Tasks[1].SessionID != loaded.Session.ID {
		t.Fatal("multiple-task roundtrip lost identity or history")
	}
	if metas := List(); len(metas) != 1 || metas[0].TotalTokens != 33 || metas[0].MessageCount != 4 {
		t.Fatalf("wrong multiple-task metadata: %+v", metas)
	}
}

func legacyData(t *testing.T, project string) ([]byte, []llm.ChatMessage) {
	t.Helper()
	messages := []llm.ChatMessage{
		{Id: 1, Role: llm.RoleSystem, Content: "system"},
		{Id: 2, Role: llm.RoleAssistant, Content: "orphan assistant"},
		{Id: 3, Role: llm.RoleUser, Content: "first question", CreateTime: 11},
		{Id: 4, Role: llm.RoleAssistant, Content: "intermediate assistant"},
		{Id: 5, Role: llm.RoleAssistant, Content: "using tool", ToolCalls: []llm.ToolCall{{ID: "old-call", Name: "read", Arguments: "{}", Reasoning: "check"}}},
		{Id: 6, Role: llm.RoleTool, Content: "tool output", ToolCallID: "old-call"},
		{Id: 7, Role: llm.RoleAssistant, Content: "first answer", CreateTime: 12},
		{Id: 8, Role: llm.RoleUser, Content: "unfinished question", CreateTime: 13},
		{Id: 9, Role: llm.RoleAssistant, Content: "not a final answer"},
		{Id: 10, Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "pending-call", Name: "read", Arguments: "{}"}}},
		{Id: 11, Role: llm.RoleTool, ToolCallID: "pending-call", Content: "pending result"},
		{Id: 12, Role: llm.RoleUser, Content: "last question", CreateTime: 14},
		{Id: 13, Role: llm.RoleAssistant, Content: "more intermediate text"},
		{Id: 14, Role: llm.RoleAssistant, Content: "last answer", CreateTime: 15},
	}
	data, err := json.MarshalIndent(map[string]any{
		"id": 101, "title": "", "project_path": project,
		"provider": llm.ModelInfo{Provider: "legacy-provider", ModelID: "legacy-model", APIKey: "legacy-secret", AllowedTools: []string{"private-tool"}},
		"messages": messages, "total_usage": llm.Usage{PromptTokens: 70, CompletionTokens: 30, TotalTokens: 100},
		"create_time": 10, "update_time": 20,
		"otter_state":       llm.OtterState{ChatSessionID: "legacy-remote", ParentMessageID: 9, Delivered: len(messages), RemoteMetadata: map[string]string{"cursor": "legacy-cursor"}},
		"unknown_old_field": "preserved in raw backup",
	}, "", "    ")
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]byte(" \n"), data...), '\n'), messages
}

func TestLegacyMigrationAndOriginalBackup(t *testing.T) {
	project, _ := isolatedPaths(t)
	original, messages := legacyData(t, project)
	path := sessionPath(getProjectSessionDir(), 101)
	mustWrite(t, path, original)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(101)
	if err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0644) // 读取旧凭据文件不改变权限。
	if len(r.Tasks) != 0 || len(r.Invocations) != 0 {
		t.Fatal("legacy import invented tasks or executions")
	}
	wantTimeline := []domain.Message{
		{ID: 3, Role: domain.UserMessage, Content: "first question", CreateTime: 11},
		{ID: 7, Role: domain.AssistantMessage, Content: "first answer", CreateTime: 12},
		{ID: 8, Role: domain.UserMessage, Content: "unfinished question", CreateTime: 13},
		{ID: 12, Role: domain.UserMessage, Content: "last question", CreateTime: 14},
		{ID: 14, Role: domain.AssistantMessage, Content: "last answer", CreateTime: 15},
	}
	if !reflect.DeepEqual(r.Session.Messages, wantTimeline) || !reflect.DeepEqual(r.Legacy.Messages, messages) {
		t.Fatalf("migration lost archive or polluted timeline: %+v", r.Session.Messages)
	}
	if r.LastModel() != (ModelRef{Provider: "legacy-provider", ModelID: "legacy-model"}) || r.Legacy.TotalUsage != (llm.Usage{PromptTokens: 70, CompletionTokens: 30, TotalTokens: 100}) || r.Legacy.OtterState.RemoteMetadata["cursor"] != "legacy-cursor" {
		t.Fatal("legacy model, usage or remote state lost")
	}
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(getProjectSessionDir(), "101.legacy.json")
	if !bytes.Equal(mustRead(t, backup), original) {
		t.Fatal("legacy backup is not byte-identical")
	}
	assertMode(t, backup, 0600)
	assertMode(t, path, 0600)
	assertNoCredentials(t, mustRead(t, path))
	loaded, err := Load(101)
	if err != nil || !reflect.DeepEqual(loaded, r) {
		t.Fatalf("migrated roundtrip failed: %v", err)
	}
	if metas := List(); len(metas) != 1 || metas[0].Title != "first question" || metas[0].MessageCount != 5 || metas[0].TotalTokens != 100 {
		t.Fatalf("wrong legacy metadata: %+v", metas)
	}
	r.Tasks = append(r.Tasks, &domain.Task{ID: 301, SessionID: 101})
	fresh := core.NewInvocation(301, project, llm.ModelInfo{Provider: "resolved", ModelID: "resolved-model"})
	if fresh.OtterState != nil || len(fresh.Messages) != 0 {
		t.Fatal("new invocation reused legacy cursor or transcript")
	}
	fresh.TotalUsage = llm.Usage{TotalTokens: 5}
	r.Capture(fresh)
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, backup), original) {
		t.Fatal("second save changed original backup")
	}
	if r.LastModel().Provider != "resolved" || List()[0].TotalTokens != 105 {
		t.Fatal("new execution did not replace model reference or usage is incorrect")
	}
	if err := Delete(101); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, backup), original) || len(List()) != 0 {
		t.Fatal("Delete removed backup or List included it")
	}
	if err := DeleteAll(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, backup), original) {
		t.Fatal("DeleteAll removed legacy backup")
	}
}

func TestLegacyBackupConflict(t *testing.T) {
	for _, existing := range []string{"same", "different", "directory", "symlink"} {
		t.Run(existing, func(t *testing.T) {
			project, _ := isolatedPaths(t)
			original, _ := legacyData(t, project)
			path := sessionPath(getProjectSessionDir(), 101)
			backup := filepath.Join(getProjectSessionDir(), "101.legacy.json")
			mustWrite(t, path, original)
			switch existing {
			case "same":
				mustWrite(t, backup, original)
				if err := os.Chmod(backup, 0644); err != nil {
					t.Fatal(err)
				}
			case "different":
				mustWrite(t, backup, []byte("older irreplaceable history"))
			case "directory":
				if err := os.Mkdir(backup, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(path, backup); err != nil {
					t.Fatal(err)
				}
			}
			r, err := Load(101)
			if err != nil {
				t.Fatal(err)
			}
			err = Save(r)
			if existing == "same" {
				if err != nil {
					t.Fatal(err)
				}
				assertMode(t, backup, 0644)
				if !bytes.Equal(mustRead(t, backup), original) {
					t.Fatal("existing backup changed")
				}
			} else {
				if err == nil || !bytes.Equal(mustRead(t, path), original) {
					t.Fatal("backup conflict did not protect the original file")
				}
				if existing == "different" && string(mustRead(t, backup)) != "older irreplaceable history" {
					t.Fatal("conflicting backup overwritten")
				}
			}
			entries, err := os.ReadDir(getProjectSessionDir())
			if err != nil || len(entries) != 2 {
				t.Fatalf("temporary file leaked: %v, %v", entries, err)
			}
		})
	}
}

func TestCorruptAndUnknownVersionsAreNeverOverwritten(t *testing.T) {
	for _, content := range []string{
		`{"version":3,"session":{}}`, `{"version":1}`, `{"version":null}`, `{"version":"2"}`,
		`{"version":2`, `{"version":2,"session":null}`, `{"version":2,"session":[]}`,
		`not json`, `{}`, `null`, `[]`, `{"id":101,"messages":"broken"}`,
	} {
		t.Run(content, func(t *testing.T) {
			project, _ := isolatedPaths(t)
			path := sessionPath(getProjectSessionDir(), 101)
			mustWrite(t, path, []byte(content))
			if _, err := Load(101); err == nil {
				t.Fatal("invalid file loaded successfully")
			}
			if err := Save(testRecord(project)); err == nil {
				t.Fatal("invalid existing file was silently overwritten")
			}
			if string(mustRead(t, path)) != content {
				t.Fatal("failed Save changed existing history")
			}
			if len(List()) != 0 {
				t.Fatal("List included invalid record")
			}
		})
	}
}

func TestSaveValidationPreservesExistingFile(t *testing.T) {
	cases := map[string]func(*Record) *Record{
		"nil record":              func(*Record) *Record { return nil },
		"nil session":             func(r *Record) *Record { r.Session = nil; return r },
		"zero ID":                 func(r *Record) *Record { r.Session.ID = 0; return r },
		"empty project":           func(r *Record) *Record { r.Session.ProjectPath = ""; return r },
		"relative project":        func(r *Record) *Record { r.Session.ProjectPath = "relative"; return r },
		"nil task":                func(r *Record) *Record { r.Tasks = append(r.Tasks, nil); return r },
		"zero task ID":            func(r *Record) *Record { r.Tasks[0].ID = 0; return r },
		"wrong session":           func(r *Record) *Record { r.Tasks[0].SessionID = 999; return r },
		"duplicate task":          func(r *Record) *Record { r.Tasks = append(r.Tasks, r.Tasks[0]); return r },
		"invalid role":            func(r *Record) *Record { r.Session.Messages[0].Role = "tool"; return r },
		"unknown message task":    func(r *Record) *Record { r.Session.Messages[0].TaskID = 999; return r },
		"unknown invocation task": func(r *Record) *Record { r.Invocations[0].TaskID = 999; return r },
		"duplicate invocation":    func(r *Record) *Record { r.Invocations = append(r.Invocations, r.Invocations[0]); return r },
		"duplicate child":         func(r *Record) *Record { r.Invocations[0].Children[0].ID = r.Invocations[0].ID; return r },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			project, _ := isolatedPaths(t)
			r := testRecord(project)
			r.Capture(testInvocation(project))
			if err := Save(r); err != nil {
				t.Fatal(err)
			}
			path := sessionPath(getProjectSessionDir(), 101)
			original := mustRead(t, path)
			if err := Save(mutate(r)); err == nil {
				t.Fatal("invalid record accepted")
			}
			if !bytes.Equal(mustRead(t, path), original) {
				t.Fatal("validation failure changed existing file")
			}
		})
	}
}

func TestProjectHomePriorityAndSaveProjectBinding(t *testing.T) {
	project, home := isolatedPaths(t)
	homeDir := filepath.Join(home, ".orca", sessionsDir)
	original, _ := legacyData(t, project)
	homePath := sessionPath(homeDir, 101)
	mustWrite(t, homePath, original)
	mustWrite(t, filepath.Join(homeDir, "101.legacy.json"), []byte("separate backup"))
	r, err := Load(101)
	if err != nil || r.Legacy == nil {
		t.Fatalf("home fallback failed: %v", err)
	}
	if len(List()) != 1 || List()[0].TotalTokens != 100 {
		t.Fatal("home history not listed")
	}
	otherProject := t.TempDir()
	t.Setenv("WORKSPACE", otherProject)
	r.Session.Title = "saved to original project"
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	projectPath := sessionPath(filepath.Join(project, ".orca", sessionsDir), 101)
	if _, err := os.Stat(projectPath); err != nil {
		t.Fatal("Save did not bind to Session.ProjectPath:", err)
	}
	if _, err := os.Stat(sessionPath(getProjectSessionDir(), 101)); !os.IsNotExist(err) {
		t.Fatal("Save wrote to current WORKSPACE")
	}
	if !bytes.Equal(mustRead(t, homePath), original) {
		t.Fatal("home original was changed")
	}
	if _, err := os.Stat(filepath.Join(project, ".orca", sessionsDir, "101.legacy.json")); !os.IsNotExist(err) {
		t.Fatal("new project file unnecessarily duplicated home backup")
	}
	t.Setenv("WORKSPACE", project)
	loaded, err := Load(101)
	if err != nil || loaded.Session.Title != "saved to original project" {
		t.Fatalf("project did not win over home: %v", err)
	}
	if metas := List(); len(metas) != 1 || metas[0].Title != loaded.Session.Title {
		t.Fatalf("List priority differs from Load: %+v", metas)
	}
	mustWrite(t, projectPath, []byte("corrupt project file"))
	if _, err := Load(101); err == nil || len(List()) != 0 {
		t.Fatal("corrupt project silently fell back to conflicting home history")
	}
	if err := Delete(101); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{projectPath, homePath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("Delete left business file %s", path)
		}
	}
	if string(mustRead(t, filepath.Join(homeDir, "101.legacy.json"))) != "separate backup" {
		t.Fatal("Delete changed home backup")
	}
}

func TestHomeContinuationFindsLatestOwningProjectRecord(t *testing.T) {
	project, home := isolatedPaths(t)
	original, _ := legacyData(t, project)
	homePath := sessionPath(filepath.Join(home, ".orca", sessionsDir), 101)
	mustWrite(t, homePath, original)
	t.Setenv("WORKSPACE", t.TempDir())
	for i := 0; i < 2; i++ {
		r, err := Load(101)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Tasks) != i || len(r.Invocations) != i {
			t.Fatal("home continuation loaded stale history")
		}
		task := domain.NewTask("new goal", "new title", r.Session.ID)
		r.Tasks = append(r.Tasks, task)
		r.Session.AppendUser(task.ID, task.Input)
		r.Session.AppendAssistant(task.ID, "answer")
		r.Session.Title = "current title"
		inv := core.NewInvocation(task.ID, project, llm.ModelInfo{})
		inv.AppendMessage(llm.RoleUser, task.Input)
		inv.AppendMessage(llm.RoleAssistant, "answer")
		r.Capture(inv)
		if err := Save(r); err != nil {
			t.Fatal(err)
		}
		metas := List()
		if len(metas) != 1 || metas[0].Title != "current title" || metas[0].MessageCount != len(r.Session.Messages) {
			t.Fatalf("home list did not resolve current history: %+v", metas)
		}
	}
	r, err := Load(101)
	if err != nil || len(r.Tasks) != 2 || len(r.Invocations) != 2 {
		t.Fatalf("successive continuations lost history: %+v, %v", r, err)
	}
	if !bytes.Equal(mustRead(t, homePath), original) {
		t.Fatal("home original was rewritten")
	}
	projectPath := sessionPath(filepath.Join(project, ".orca", sessionsDir), 101)
	mustWrite(t, projectPath, []byte("corrupt current record"))
	if _, err := Load(101); err == nil || len(List()) != 0 {
		t.Fatal("corrupt owning project fell back to stale home history")
	}
}

func TestListAndDeleteAllOnlyRecognizeSessionFiles(t *testing.T) {
	project, home := isolatedPaths(t)
	r := testRecord(project)
	r.Session.Title = ""
	r.Session.Messages[0].Content = "  这是一个很长的用户会话标题用来验证按字符截取不会破坏中文字符并且忽略第二行\nsecond line"
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	newer := testRecord(home)
	newer.Session.ID = 102
	newer.Session.UpdateTime = 999
	newer.Tasks[0].SessionID = 102
	if err := Save(newer); err != nil {
		t.Fatal(err)
	}
	data := mustRead(t, sessionPath(getProjectSessionDir(), 101))
	for _, name := range []string{"101.legacy.json", "999.legacy.json", "config.json", "000101.json", ".session-leftover.tmp"} {
		mustWrite(t, filepath.Join(getProjectSessionDir(), name), data)
	}
	if err := os.Mkdir(filepath.Join(getProjectSessionDir(), "103.json"), 0700); err != nil {
		t.Fatal(err)
	}
	metas := List()
	if len(metas) != 2 || metas[0].Id != 102 || metas[1].Id != 101 || len([]rune(metas[1].Title)) != 30 || !strings.HasSuffix(metas[1].Title, "...") {
		t.Fatalf("unexpected list: %+v", metas)
	}
	if err := DeleteAll(); err != nil {
		t.Fatal(err)
	}
	if len(List()) != 0 {
		t.Fatal("DeleteAll left sessions")
	}
	for _, name := range []string{"101.legacy.json", "999.legacy.json", "config.json", "000101.json", ".session-leftover.tmp", "103.json"} {
		if _, err := os.Stat(filepath.Join(getProjectSessionDir(), name)); err != nil {
			t.Fatalf("DeleteAll removed non-session %s: %v", name, err)
		}
	}
}

func TestInvalidInvocationRoles(t *testing.T) {
	for _, child := range []bool{false, true} {
		project, _ := isolatedPaths(t)
		r := testRecord(project)
		r.Capture(testInvocation(project))
		if child {
			r.Invocations[0].Children[0].Messages[0].Role = "invalid"
		} else {
			r.Invocations[0].Messages[0].Role = "invalid"
		}
		if err := Save(r); err == nil {
			t.Fatal("invalid invocation role was saved")
		}
	}
}

func TestAtomicReplacement(t *testing.T) {
	project, _ := isolatedPaths(t)
	r := testRecord(project)
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	path := sessionPath(getProjectSessionDir(), 101)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			r.Session.Title = strings.Repeat("replacement", 1024+i)
			if err := Save(r); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	// 不在 writer 完成前退出，确保 TempDir 清理不会与写入并发。
	var readErr error
	for {
		select {
		case err := <-done:
			if err != nil || readErr != nil {
				t.Fatalf("atomic replacement failed: writer=%v reader=%v", err, readErr)
			}
			entries, err := os.ReadDir(getProjectSessionDir())
			if err != nil || len(entries) != 1 {
				t.Fatalf("replacement left temporary files: %v, %v", entries, err)
			}
			return
		default:
			data, err := os.ReadFile(path)
			if err == nil {
				_, _, err = decodeRecord(data)
			}
			if err != nil {
				readErr = err
			}
		}
	}
}

func TestSaveFilesystemFailureAndCredentialPermissions(t *testing.T) {
	project, home := isolatedPaths(t)
	credentials := filepath.Join(home, ".orca", "models.json")
	mustWrite(t, credentials, []byte(`{"APIKey":"existing-key"}`))
	if err := os.Chmod(credentials, 0644); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(project, ".orca")
	mustWrite(t, blocker, []byte("existing file"))
	if err := Save(testRecord(project)); err == nil {
		t.Fatal("Save unexpectedly succeeded with blocked directory")
	}
	if string(mustRead(t, blocker)) != "existing file" {
		t.Fatal("failed Save changed blocking file")
	}
	assertMode(t, credentials, 0644)
	if string(mustRead(t, credentials)) != `{"APIKey":"existing-key"}` {
		t.Fatal("Save changed credentials")
	}
}
