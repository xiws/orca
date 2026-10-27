package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xiws/orca/internal/model"
)

func writeConfig(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, ".orca")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func configRoots(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	return home, workspace
}

func TestWorkspaceOverlayAndRuntimeSecrets(t *testing.T) {
	home, workspace := configRoots(t)
	writeConfig(t, home, "setting.json", `{"defaultProvider":"shared","defaultModel":"home-model","debug":"true"}`)
	writeConfig(t, home, "models.json", `{"providers":{"shared":{"name":"home","api":"openai-completions","baseUrl":"http://home/v1","apiKey":"${ORCA_TEST_KEY}","models":[{"id":"home-model","contextWindow":8000,"supportsTools":true},{"id":"other","contextWindow":2000,"supportsTools":true}]},"home-only":{"api":"otter","models":[{"id":"deepseek"}]}}}`)
	writeConfig(t, workspace, "settings.json", `{"defaultModel":"local-model"}`)
	writeConfig(t, workspace, "models.json", `{"providers":{"shared":{"baseUrl":"http://workspace/v1","models":[{"id":"home-model","supportsTools":false},{"id":"local-model","contextWindow":12000,"supportsTools":true}]}}}`)
	writeConfig(t, home, "system_prompt.md", "home prompt")
	writeConfig(t, workspace, "system_prompt.md", "workspace {{.ProjectPath}} window={{.ContextLength}}")
	t.Setenv("ORCA_TEST_KEY", "first-secret")
	cfg, err := Load(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel() != (model.Ref{Provider: "shared", Model: "local-model"}) {
		t.Fatal(cfg.DefaultModel())
	}
	conn, err := cfg.Resolve(context.Background(), model.Ref{Provider: "shared", Model: "home-model"})
	if err != nil {
		t.Fatal(err)
	}
	if conn.API != "openai-completions" || conn.BaseURL != "http://workspace/v1" || conn.ContextWindow != 8000 || conn.SupportsTools || conn.APIKey != "first-secret" {
		t.Fatal("incorrect overlay")
	}
	if _, err := cfg.Resolve(context.Background(), model.Ref{Provider: "home-only", Model: "deepseek"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Resolve(context.Background(), model.Ref{Provider: "shared", Model: "other"}); err != nil {
		t.Fatal(err)
	}
	if prompt := cfg.SystemPrompt(cfg.DefaultModel()); !strings.Contains(prompt, workspace) || !strings.Contains(prompt, "12000") {
		t.Fatal(prompt)
	}
	t.Setenv("ORCA_TEST_KEY", "rotated-secret")
	conn, err = cfg.Resolve(context.Background(), cfg.DefaultModel())
	if err != nil || conn.APIKey != "rotated-secret" {
		t.Fatal("secret was cached", err)
	}
	cfgRaw, _ := json.Marshal(cfg)
	connRaw, _ := json.Marshal(conn)
	if strings.Contains(string(cfgRaw)+string(connRaw), "secret") || string(cfgRaw) != "{}" || string(connRaw) != "{}" {
		t.Fatal("credential serialized")
	}
	data, err := os.ReadFile(filepath.Join(home, ".orca", "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "${ORCA_TEST_KEY}") || strings.Contains(string(data), "secret") {
		t.Fatal("secret written back")
	}
}

func TestConfigExplicitWorkspaceNotProcessDirectory(t *testing.T) {
	home, workspace := configRoots(t)
	writeConfig(t, home, "setting.json", `{"defaultProvider":"p","defaultModel":"m"}`)
	writeConfig(t, home, "models.json", `{"providers":{"p":{"api":"otter","models":[{"id":"m"}]}}}`)
	cfg, err := Load(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel().Provider != "p" {
		t.Fatal("did not inherit home")
	}
	if _, err := Load(""); err == nil {
		t.Fatal("implicit cwd accepted")
	}
	if _, err := Load(filepath.Join(workspace, "missing")); err == nil {
		t.Fatal("missing workspace accepted")
	}
}

func TestConfigMissingFilesAndNoInitializationWrites(t *testing.T) {
	_, workspace := configRoots(t)
	cfg, err := Load(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel() != (model.Ref{}) || cfg.SystemPrompt(model.Ref{}) == "" {
		t.Fatal("bad empty configuration")
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatal("load created files", err)
	}
	if _, err := cfg.Resolve(context.Background(), model.Ref{Provider: "none", Model: "none"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestConfigurationErrorsAreClearAndDoNotExposeSecrets(t *testing.T) {
	for _, tc := range []struct{ name, file, content string }{
		{"broken json", "models.json", `{"providers":{"p":{"apiKey":"secret-token",BROKEN`},
		{"duplicate key", "models.json", `{"providers":{},"providers":{}}`},
		{"wrong type", "setting.json", `{"defaultProvider":123}`},
		{"null settings", "settings.json", `null`},
		{"negative window", "models.json", `{"providers":{"p":{"api":"openai","models":[{"id":"m","contextWindow":-1}]}}}`},
		{"duplicate model", "models.json", `{"providers":{"p":{"api":"openai","models":[{"id":"m"},{"id":"m"}]}}}`},
		{"missing api", "models.json", `{"providers":{"p":{"models":[{"id":"m"}]}}}`},
		{"template parse", "system_prompt.md", `{{bad syntax`},
		{"template execution", "system_prompt.md", `{{.DoesNotExist}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, workspace := configRoots(t)
			writeConfig(t, workspace, tc.file, tc.content)
			_, err := Load(workspace)
			if err == nil || !strings.Contains(err.Error(), "config:") || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("unsafe/absent error: %v", err)
			}
		})
	}
}

func TestSecretReferencesResolvedOnlyOnDemand(t *testing.T) {
	_, workspace := configRoots(t)
	t.Setenv("ORCA_MISSING_KEY", "")
	writeConfig(t, workspace, "models.json", `{"providers":{"p":{"api":"openai","apiKey":"${ORCA_MISSING_KEY}","models":[{"id":"m"}]}}}`)
	cfg, err := Load(workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := model.Ref{Provider: "p", Model: "m"}
	if _, err := cfg.Resolve(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "ORCA_MISSING_KEY") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cfg.Resolve(ctx, ref); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	t.Setenv("ORCA_MISSING_KEY", "value-${literal}")
	conn, err := cfg.Resolve(context.Background(), ref)
	if err != nil || conn.APIKey != "value-${literal}" {
		t.Fatal("expanded secret reparsed", err)
	}
	if _, err := cfg.Resolve(context.Background(), model.Ref{Provider: "p", Model: "missing"}); err == nil {
		t.Fatal("unknown model accepted")
	}
}

func TestSystemPromptWorkspacePriorityAndOtterOverride(t *testing.T) {
	home, workspace := configRoots(t)
	writeConfig(t, home, "models.json", `{"providers":{"web":{"api":"otter","models":[{"id":"deepseek","contextWindow":4096}]}}}`)
	writeConfig(t, home, "otter_prompt.md", "home otter")
	writeConfig(t, workspace, "system_prompt.md", "workspace generic")
	cfg, err := Load(workspace)
	if err != nil {
		t.Fatal(err)
	}
	ref := model.Ref{Provider: "web", Model: "deepseek"}
	if cfg.SystemPrompt(ref) != "workspace generic" {
		t.Fatal(cfg.SystemPrompt(ref))
	}
	writeConfig(t, workspace, "otter_prompt.md", "workspace otter {{.ContextLength}}")
	cfg, err = Load(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SystemPrompt(ref) != "workspace otter 4096" {
		t.Fatal(cfg.SystemPrompt(ref))
	}
}
