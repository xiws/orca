package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/xiws/orca/internal/agent/core"
	"github.com/xiws/orca/internal/app"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/internal/session"
	"github.com/xiws/orca/internal/tool"
)

var cliDefaultProvider = llm.ModelInfo{
	Provider: "default", Name: "Default test provider", API: "openai-completions",
	BaseURL: "https://default.invalid/v1", APIKey: "default-test-key", ModelID: "default-model",
	ContextWindow: 4096, SupportsTools: true,
}

var cliSavedProvider = llm.ModelInfo{
	Provider: "saved", Name: "Saved test provider", API: "otter",
	BaseURL: "https://saved.invalid", APIKey: "old-test-key", ModelID: "saved-model",
	ContextWindow: 8192, SupportsTools: true, Reasoning: true,
}

// Package initialization needs WORKSPACE settings before per-test isolation can run.
func isolateCLI(t *testing.T) (workspace, home string) {
	t.Helper()
	root := t.TempDir()
	workspace, home = filepath.Join(root, "project"), filepath.Join(root, "home")
	cwd := filepath.Join(root, "cwd")
	for _, path := range []string{workspace, home, cwd} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(cwd)
	t.Setenv("WORKSPACE", workspace)
	t.Setenv("HOME", home)
	writeCLIFile(t, filepath.Join(workspace, ".orca", "setting.json"), `{"defaultProvider":"default","defaultModel":"default-model"}`)
	writeCLIFile(t, filepath.Join(workspace, ".orca", "system_prompt.md"), "current default system")
	writeCLIFile(t, filepath.Join(workspace, ".orca", "otter_prompt.md"), "current otter system")
	writeCLIModels(t, workspace, cliDefaultProvider, cliSavedProvider)

	oldProvider, oldModel := tool.Get(tool.KeyDefaultProvider), tool.Get(tool.KeyDefaultModel)
	t.Cleanup(func() {
		tool.Set(tool.KeyDefaultProvider, oldProvider)
		tool.Set(tool.KeyDefaultModel, oldModel)
	})
	settings := tool.LoadSettings()
	tool.Set(tool.KeyDefaultProvider, settings.DefaultProvider)
	tool.Set(tool.KeyDefaultModel, settings.DefaultModel)
	return workspace, home
}

func writeCLIFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeCLIModels(t *testing.T, workspace string, models ...llm.ModelInfo) {
	t.Helper()
	providers := make(map[string]any)
	for _, model := range models {
		providers[model.Provider] = map[string]any{
			"name": model.Name, "api": model.API, "baseUrl": model.BaseURL, "apiKey": model.APIKey,
			"models": []map[string]any{{
				"id": model.ModelID, "contextWindow": model.ContextWindow,
				"supportsTools": model.SupportsTools, "reasoning": model.Reasoning,
			}},
		}
	}
	data, err := json.Marshal(map[string]any{"providers": providers})
	if err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, filepath.Join(workspace, ".orca", "models.json"), string(data))
}

func parseCLI(t *testing.T, args ...string) *CliArgs {
	t.Helper()
	setCLIArgs(t, args...)
	parsed, err := ParseArgs()
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func setCLIArgs(t *testing.T, args ...string) {
	t.Helper()
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = append([]string{"orca"}, args...)
}

type cliExecutorFunc func(*domain.Task, *core.Invocation) (string, error)

func (f cliExecutorFunc) Run(task *domain.Task, inv *core.Invocation) (string, error) {
	return f(task, inv)
}

func assertCLIContext(t *testing.T, inv *core.Invocation, roles, contents []string) {
	t.Helper()
	var gotRoles, gotContents []string
	for _, message := range inv.Messages {
		gotRoles = append(gotRoles, message.Role)
		gotContents = append(gotContents, message.Content)
		if len(message.ToolCalls) != 0 || message.ToolCallID != "" {
			t.Errorf("old tool trace in new invocation: %+v", message)
		}
	}
	if !reflect.DeepEqual(gotRoles, roles) || !reflect.DeepEqual(gotContents, contents) {
		t.Fatalf("context = %v / %q, want %v / %q", gotRoles, gotContents, roles, contents)
	}
}

func assertFreshCLIInvocation(t *testing.T, turn *app.Turn) {
	t.Helper()
	inv := turn.Invocation
	if inv.ID <= 0 || turn.Task.ID <= 0 || inv.TaskID != turn.Task.ID || int64(turn.Task.ID) == int64(turn.Task.SessionID) || int64(turn.Task.ID) == inv.ID || int64(turn.Task.SessionID) == inv.ID {
		t.Fatalf("invalid invocation identity: %+v, task: %+v", inv, turn.Task)
	}
	if inv.OtterState != nil || inv.TotalUsage != (llm.Usage{}) || len(inv.Children) != 0 || inv.Provider.AllowedTools != nil {
		t.Fatalf("previous execution state leaked: %+v", inv)
	}
	if turn.Task.Specification != nil {
		t.Fatalf("previous task specification leaked: %+v", turn.Task)
	}
}

func TestPrepareTurnNewSession(t *testing.T) {
	workspace, _ := isolateCLI(t)
	record, turn, err := prepareTurn(parseCLI(t, "new", "goal"))
	if err != nil {
		t.Fatal(err)
	}
	if record.Session.ID <= 0 || turn.Task.ID <= 0 || int64(record.Session.ID) == int64(turn.Task.ID) || turn.Task.SessionID != record.Session.ID {
		t.Fatalf("session/task identities are conflated: %+v / %+v", record.Session, turn.Task)
	}
	if record.Session.ProjectPath != workspace || turn.Invocation.ProjectPath != workspace {
		t.Fatalf("project paths = %q / %q, want %q", record.Session.ProjectPath, turn.Invocation.ProjectPath, workspace)
	}
	if turn.Task.Input != "new goal" || turn.Task.Title != "new goal" || record.Session.Title != "new goal" {
		t.Fatalf("task/session metadata = %+v / %+v", turn.Task, record.Session)
	}
	if len(record.Tasks) != 1 || record.Tasks[0] != turn.Task || len(record.Invocations) != 0 || record.Legacy != nil {
		t.Fatalf("new record = %+v", record)
	}
	if !reflect.DeepEqual(turn.Invocation.Provider, cliDefaultProvider) {
		t.Fatalf("provider = %+v, want %+v", turn.Invocation.Provider, cliDefaultProvider)
	}
	assertFreshCLIInvocation(t, turn)
	assertCLIContext(t, turn.Invocation, []string{llm.RoleSystem, llm.RoleUser}, []string{"current default system", "new goal"})
	if len(record.Session.Messages) != 1 || record.Session.Messages[0].Role != domain.UserMessage || record.Session.Messages[0].TaskID != turn.Task.ID || record.Session.Messages[0].Content != "new goal" {
		t.Fatalf("session timeline = %+v", record.Session.Messages)
	}
	if len(session.List()) != 0 {
		t.Fatal("preparing a turn must not persist it before execution")
	}
}

func TestCLIConsecutiveTurnsUseFreshTaskAndCurrentCredentials(t *testing.T) {
	workspace, _ := isolateCLI(t)
	record, first, err := prepareTurn(parseCLI(t, "-p", "saved", "-m", "saved-model", "first goal"))
	if err != nil {
		t.Fatal(err)
	}
	first.Task.Specification = &domain.Specification{Goal: "old specification"}
	_, err = app.Execute(record, first, cliExecutorFunc(func(task *domain.Task, inv *core.Invocation) (string, error) {
		if task != first.Task || inv != first.Invocation {
			t.Fatal("executor did not receive the prepared task and invocation")
		}
		inv.AppendAssistant("private reasoning", []llm.ToolCall{{ID: "old-call", Name: "read", Arguments: `{}`}})
		inv.AppendToolResult("old-call", "private tool output")
		inv.AppendAssistant("private intermediate answer", nil)
		inv.AddUsage(llm.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18})
		inv.SetOtterState(llm.OtterState{
			ChatSessionID: "old-remote", ParentMessageID: 42, ParentMessageIDStr: "old-parent", Delivered: 6,
			RemoteMetadata: map[string]string{"cursor": "old-cursor"},
		})
		inv.Provider.AllowedTools = []string{"read"}
		child := inv.NewChild()
		child.AppendAssistant("private verifier answer", nil)
		child.AddUsage(llm.Usage{TotalTokens: 5})
		return "published answer", nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	current := cliSavedProvider
	current.Name, current.BaseURL, current.APIKey = "Current provider", "https://current.invalid", "rotated-test-key"
	current.ContextWindow, current.Reasoning = 16384, false
	writeCLIModels(t, workspace, cliDefaultProvider, current)
	writeCLIFile(t, filepath.Join(workspace, ".orca", "otter_prompt.md"), "fresh otter system")
	resumed, second, err := prepareTurn(parseCLI(t, "-session", strconv.FormatInt(int64(record.Session.ID), 10), "second goal"))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Session.ID != record.Session.ID || second.Task.SessionID != record.Session.ID || second.Task.ID == first.Task.ID || second.Invocation.ID == first.Invocation.ID {
		t.Fatalf("continuation reused execution identities: %+v / %+v", first, second)
	}
	if len(resumed.Tasks) != 2 || !reflect.DeepEqual(resumed.Tasks[0], first.Task) || resumed.Tasks[1] != second.Task || second.Task.Input != "second goal" || resumed.Session.Title != "first goal" {
		t.Fatalf("continuation lost task history: %+v", resumed)
	}
	if !reflect.DeepEqual(second.Invocation.Provider, current) {
		t.Fatalf("provider was not re-resolved: %+v, want %+v", second.Invocation.Provider, current)
	}
	assertFreshCLIInvocation(t, second)
	assertCLIContext(t, second.Invocation,
		[]string{llm.RoleSystem, llm.RoleUser, llm.RoleAssistant, llm.RoleUser},
		[]string{"fresh otter system", "first goal", "published answer", "second goal"})
	if len(resumed.Invocations) != 1 || !reflect.DeepEqual(resumed.Invocations[0], record.Invocations[0]) {
		t.Fatal("old invocation trace should remain archived, not resumed or erased")
	}
	if len(resumed.Session.Messages) != 3 || !reflect.DeepEqual(resumed.Session.Messages[:2], record.Session.Messages) || resumed.Session.Messages[2].TaskID != second.Task.ID {
		t.Fatalf("user history = %+v", resumed.Session.Messages)
	}
	second.Invocation.Messages[1].Content = "model-only change"
	if resumed.Session.Messages[0].Content != "first goal" {
		t.Fatal("invocation context aliases the session timeline")
	}
	_, err = app.Execute(resumed, second, cliExecutorFunc(func(_ *domain.Task, inv *core.Invocation) (string, error) {
		inv.AddUsage(llm.Usage{TotalTokens: 3})
		return "second published answer", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := session.Load(int64(record.Session.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tasks) != 2 || len(loaded.Invocations) != 2 || len(loaded.Session.Messages) != 4 {
		t.Fatalf("saved continuation = %+v", loaded)
	}
	if loaded.Invocations[0].TotalUsage.TotalTokens != 18 || loaded.Invocations[1].TotalUsage.TotalTokens != 3 || loaded.Invocations[1].OtterState != nil || loaded.Session.Messages[3].Content != "second published answer" {
		t.Fatal("saved continuation mixed execution state or lost the final answer")
	}
}

func TestPrepareTurnPreservesHomeSessionProjectPath(t *testing.T) {
	workspace, home := isolateCLI(t)
	owner := t.TempDir()
	record := &session.Record{Session: domain.NewSession(owner)}
	if err := session.Save(record); err != nil {
		t.Fatal(err)
	}
	name := strconv.FormatInt(int64(record.Session.ID), 10) + ".json"
	ownerPath := filepath.Join(owner, ".orca", "sessions", name)
	data, err := os.ReadFile(ownerPath)
	if err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, filepath.Join(home, ".orca", "sessions", name), string(data))
	resumed, turn, err := prepareTurn(parseCLI(t, "-session", strconv.FormatInt(int64(record.Session.ID), 10), "continue elsewhere"))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Session.ProjectPath != owner || turn.Invocation.ProjectPath != owner || resumed.Session.ID != record.Session.ID {
		t.Fatalf("current workspace replaced session ownership: %+v / %+v", resumed.Session, turn.Invocation)
	}
	// Models come from the current CLI configuration, not the session's project.
	if !reflect.DeepEqual(turn.Invocation.Provider, cliDefaultProvider) {
		t.Fatalf("current configuration was not used: %+v", turn.Invocation.Provider)
	}
	if _, err := app.Execute(resumed, turn, cliExecutorFunc(func(*domain.Task, *core.Invocation) (string, error) {
		return "done", nil
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".orca", "sessions", name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("continuation wrote into current workspace: %v", err)
	}
	t.Setenv("WORKSPACE", owner)
	loaded, err := session.Load(int64(record.Session.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tasks) != 1 || len(loaded.Session.Messages) != 2 || loaded.Session.Messages[1].Content != "done" {
		t.Fatalf("continuation was not saved to the owning project: %+v", loaded)
	}
}

func TestPrepareTurnModelSelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resume   bool
		provider string
		model    string
		want     llm.ModelInfo
	}{
		{name: "new default", want: cliDefaultProvider},
		{name: "new explicit", provider: "saved", model: "saved-model", want: cliSavedProvider},
		{name: "resume saved", resume: true, want: cliSavedProvider},
		{name: "resume explicit override", resume: true, provider: "default", model: "default-model", want: cliDefaultProvider},
		{name: "model alone keeps default", model: "saved-model", want: cliDefaultProvider},
		{name: "provider alone keeps default", provider: "saved", want: cliDefaultProvider},
		{name: "model alone keeps saved", resume: true, model: "default-model", want: cliSavedProvider},
		{name: "provider alone keeps saved", resume: true, provider: "default", want: cliSavedProvider},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLI(t)
			args := &CliArgs{Prompt: "goal", Provider: tc.provider, Model: tc.model}
			if tc.resume {
				record, turn, err := prepareTurn(&CliArgs{Prompt: "old goal", Provider: "saved", Model: "saved-model"})
				if err != nil {
					t.Fatal(err)
				}
				record.Capture(turn.Invocation)
				if err := session.Save(record); err != nil {
					t.Fatal(err)
				}
				args.SessionId = int64(record.Session.ID)
			}
			_, turn, err := prepareTurn(args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(turn.Invocation.Provider, tc.want) {
				t.Fatalf("provider = %+v, want %+v", turn.Invocation.Provider, tc.want)
			}
		})
	}
}

func TestPrepareTurnLegacyArchiveDoesNotRestoreExecutionState(t *testing.T) {
	workspace, _ := isolateCLI(t)
	legacy := map[string]any{
		"id": 123, "title": "legacy title", "project_path": workspace,
		"provider": map[string]any{
			"Provider": "saved", "ModelID": "saved-model", "APIKey": "archived-secret",
			"BaseURL": "https://archived.invalid", "AllowedTools": []string{"read"},
		},
		"messages": []llm.ChatMessage{
			{Id: 1, Role: llm.RoleSystem, Content: "old system"},
			{Id: 2, Role: llm.RoleUser, Content: "old question"},
			{Id: 3, Role: llm.RoleAssistant, Content: "old reasoning", ToolCalls: []llm.ToolCall{{ID: "call", Name: "read"}}},
			{Id: 4, Role: llm.RoleTool, Content: "old tool result", ToolCallID: "call"},
			{Id: 5, Role: llm.RoleAssistant, Content: "old answer"},
		},
		"total_usage": llm.Usage{PromptTokens: 40, CompletionTokens: 10, TotalTokens: 50},
		"otter_state": llm.OtterState{ChatSessionID: "legacy-remote", Delivered: 5, RemoteMetadata: map[string]string{"cursor": "legacy-cursor"}},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, ".orca", "sessions", "123.json")
	writeCLIFile(t, path, string(data))
	record, turn, err := prepareTurn(parseCLI(t, "-session", "123", "new goal"))
	if err != nil {
		t.Fatal(err)
	}
	if record.Session.ID != 123 || record.Session.Title != "legacy title" || turn.Task.SessionID != 123 || int64(turn.Task.ID) == 123 || len(record.Tasks) != 1 {
		t.Fatalf("legacy continuation identities = %+v / %+v", record, turn.Task)
	}
	assertFreshCLIInvocation(t, turn)
	assertCLIContext(t, turn.Invocation,
		[]string{llm.RoleSystem, llm.RoleUser, llm.RoleAssistant, llm.RoleUser},
		[]string{"current otter system", "old question", "old answer", "new goal"})
	if !reflect.DeepEqual(turn.Invocation.Provider, cliSavedProvider) {
		t.Fatalf("legacy credentials were not replaced: %+v", turn.Invocation.Provider)
	}
	if record.Legacy == nil || record.Legacy.OtterState == nil || record.Legacy.OtterState.Delivered != 5 || record.Legacy.TotalUsage.TotalTokens != 50 || len(record.Legacy.Messages) != 5 {
		t.Fatalf("legacy archive was lost: %+v", record.Legacy)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != string(data) {
		t.Fatalf("prepare rewrote the legacy file: %v", err)
	}
}

func TestUserPrompt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		files  []string
		prompt string
		want   string
	}{
		{name: "plain", prompt: "  keep prompt\n", want: "  keep prompt\n"},
		{name: "empty", want: ""},
		{name: "attachments in order", files: []string{"alpha.txt", `quoted "name".txt`}, prompt: "explain", want: "<file path=\"alpha.txt\">\nalpha\n</file>\n\n<file path=\"quoted \\\"name\\\".txt\">\nbeta \n</file>\n\nexplain"},
		{name: "empty attachment", files: []string{"empty.txt"}, want: "<file path=\"empty.txt\">\n\n</file>\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLI(t)
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			writeCLIFile(t, filepath.Join(cwd, "alpha.txt"), "alpha\n\n")
			writeCLIFile(t, filepath.Join(cwd, `quoted "name".txt`), "beta \n")
			writeCLIFile(t, filepath.Join(cwd, "empty.txt"), "")
			got, err := userPrompt(&CliArgs{Files: tc.files, Prompt: tc.prompt})
			if err != nil || got != tc.want {
				t.Fatalf("userPrompt() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestPrepareTurnAttachmentsAndCustomSystemPrompt(t *testing.T) {
	for _, supportsTools := range []bool{true, false} {
		t.Run(fmt.Sprintf("native_tools_%t", supportsTools), func(t *testing.T) {
			workspace, _ := isolateCLI(t)
			provider := cliDefaultProvider
			provider.SupportsTools = supportsTools
			writeCLIModels(t, workspace, provider)
			attachment, system := filepath.Join(workspace, "source.go"), filepath.Join(workspace, "custom.txt")
			writeCLIFile(t, attachment, "package example\n\n")
			writeCLIFile(t, system, "custom system\n\n")
			record, turn, err := prepareTurn(parseCLI(t, "-f", attachment, "-sp", system, "explain source"))
			if err != nil {
				t.Fatal(err)
			}
			wantPrompt := fmt.Sprintf("<file path=%q>\npackage example\n</file>\n\nexplain source", attachment)
			messages := turn.Invocation.Messages
			wantLen := 2
			if !supportsTools {
				wantLen++
			}
			if len(messages) != wantLen || messages[0].Role != llm.RoleSystem || messages[0].Content != "custom system\n\n" || messages[len(messages)-1].Role != llm.RoleUser || messages[len(messages)-1].Content != wantPrompt {
				t.Fatalf("prepared context = %+v", messages)
			}
			if !supportsTools && (messages[1].Role != llm.RoleSystem || messages[1].Content == "") {
				t.Fatal("custom system prompt removed required tool protocol instructions")
			}
			if turn.Task.Input != "explain source" || turn.Task.Title != "explain source" || record.Session.Title != "explain source" {
				t.Fatalf("attachment became task input/title: %+v", turn.Task)
			}
			if len(record.Session.Messages) != 1 || record.Session.Messages[0].Content != wantPrompt || record.Session.Messages[0].Role != domain.UserMessage {
				t.Fatalf("session should contain attachment but no system instructions: %+v", record.Session.Messages)
			}
		})
	}
}

func TestPrepareTurnErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(string) *CliArgs
		want string
		path bool
	}{
		{name: "missing attachment", args: func(root string) *CliArgs { return &CliArgs{Files: []string{filepath.Join(root, "missing.txt")}} }, want: "read attached file", path: true},
		{name: "attachment directory", args: func(root string) *CliArgs { return &CliArgs{Files: []string{root}} }, want: "read attached file", path: true},
		{name: "missing system prompt", args: func(root string) *CliArgs { return &CliArgs{SystemPrompt: filepath.Join(root, "missing.txt")} }, want: "read system prompt", path: true},
		{name: "system prompt directory", args: func(root string) *CliArgs { return &CliArgs{SystemPrompt: root} }, want: "read system prompt", path: true},
		{name: "missing session", args: func(string) *CliArgs { return &CliArgs{SessionId: 404} }, want: "load session 404"},
		{name: "corrupt session", args: func(string) *CliArgs { return &CliArgs{SessionId: 405} }, want: "load session 405: parse session file"},
		{name: "unknown provider", args: func(string) *CliArgs { return &CliArgs{Provider: "absent", Model: "default-model"} }, want: "unknown model absent/default-model"},
		{name: "unknown model", args: func(string) *CliArgs { return &CliArgs{Provider: "default", Model: "absent"} }, want: "unknown model default/absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace, _ := isolateCLI(t)
			writeCLIFile(t, filepath.Join(workspace, ".orca", "sessions", "405.json"), "{invalid")
			args := tc.args(workspace)
			args.Prompt = "goal"
			record, turn, err := prepareTurn(args)
			if err == nil || !strings.Contains(err.Error(), tc.want) || record != nil || turn != nil {
				t.Fatalf("prepareTurn() = %v, %v, %v; want nil, nil, %q", record, turn, err, tc.want)
			}
			if tc.path {
				var pathErr *os.PathError
				if !errors.As(err, &pathErr) {
					t.Fatalf("read error lost its cause: %v", err)
				}
				wantPath := args.SystemPrompt
				if len(args.Files) != 0 {
					wantPath = args.Files[0]
				}
				if pathErr.Path != wantPath || !strings.Contains(err.Error(), wantPath) {
					t.Fatalf("read error lost file path %q: %v", wantPath, err)
				}
			}
		})
	}
}

func TestPrepareTurnRemovedSavedModelDoesNotFallBack(t *testing.T) {
	workspace, _ := isolateCLI(t)
	record, turn, err := prepareTurn(&CliArgs{Prompt: "old goal", Provider: "saved", Model: "saved-model"})
	if err != nil {
		t.Fatal(err)
	}
	record.Capture(turn.Invocation)
	if err := session.Save(record); err != nil {
		t.Fatal(err)
	}
	writeCLIModels(t, workspace, cliDefaultProvider)
	resumed, next, err := prepareTurn(&CliArgs{SessionId: int64(record.Session.ID), Prompt: "new goal"})
	if err == nil || !strings.Contains(err.Error(), "unknown model saved/saved-model") || resumed != nil || next != nil {
		t.Fatalf("removed model silently fell back: %v / %v / %v", resumed, next, err)
	}
	loaded, err := session.Load(int64(record.Session.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, record) {
		t.Fatal("failed preparation modified the saved session")
	}
}
