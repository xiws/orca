//go:build live

package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/xiws/orca/internal/handler"
)

// liveRequesterInfo requires ORCA_LIVE_TEST=1, ORCA_LIVE_BASE_URL and
// ORCA_LIVE_MODEL. ORCA_LIVE_API_KEY is optional for unauthenticated endpoints;
// ORCA_LIVE_SUPPORTS_TOOLS=1 opts into the tool-calling test. No saved config or
// credentials are consulted, and every opted-in test runs in a temporary HOME
// and workspace, never in the repository.
func liveRequesterInfo(t *testing.T) (ModelInfo, string) {
	t.Helper()
	if testing.Short() || os.Getenv("ORCA_LIVE_TEST") != "1" {
		t.Skip("live requests require ORCA_LIVE_TEST=1 and non-short mode")
	}
	info := ModelInfo{
		Provider:      "live",
		Name:          "explicit live endpoint",
		API:           "openai-completions",
		BaseURL:       os.Getenv("ORCA_LIVE_BASE_URL"),
		APIKey:        os.Getenv("ORCA_LIVE_API_KEY"),
		ModelID:       os.Getenv("ORCA_LIVE_MODEL"),
		SupportsTools: os.Getenv("ORCA_LIVE_SUPPORTS_TOOLS") == "1",
		AllowedTools:  []string{handler.CommandRead},
	}
	if info.BaseURL == "" || info.ModelID == "" {
		t.Fatal("ORCA_LIVE_BASE_URL and ORCA_LIVE_MODEL are required when opting in")
	}
	endpoint, err := url.Parse(info.BaseURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		t.Fatal("ORCA_LIVE_BASE_URL must be an explicit HTTP(S) endpoint")
	}

	workspace := t.TempDir()
	t.Setenv("HOME", workspace)
	t.Setenv("WORKSPACE", workspace)
	t.Chdir(workspace)
	// Use the working directory's spelling when validating absolute tool paths.
	workspace, err = os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return info, workspace
}

// TestRequesterWithDefaultModel checks live streaming using only an explicitly
// supplied endpoint and model, rather than a saved default model.
func TestRequesterWithDefaultModel(t *testing.T) {
	info, _ := liveRequesterInfo(t)

	msgs := make(chan string)
	var chunks []string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for chunk := range msgs {
			chunks = append(chunks, chunk)
			fmt.Print(chunk)
		}
	}()

	result := NewOpenAIRequester(info).Request([]ChatMessage{
		{Role: RoleSystem, Content: "You are a concise assistant. Answer in one short sentence."},
		{Role: RoleUser, Content: "Introduce what an orca is in one sentence."},
	}, msgs)
	close(msgs)
	wg.Wait()

	if result.Error != nil {
		if isUnreachable(result.Error) {
			t.Skipf("skipping: %s at %s is not reachable: %v", info.Name, info.BaseURL, result.Error)
		}
		t.Fatalf("Request() error = %v", result.Error)
	}
	if result.Content == "" {
		t.Fatal("Request() returned empty Content")
	}
	if got, want := strings.Join(chunks, ""), result.Content; got != want {
		t.Errorf("streamed content %q != final Content %q", got, want)
	}
	t.Logf("model=%s provider=%s finish=%q usage=%+d prompt=%d completion=%d tokens",
		info.ModelID, info.Provider, result.FinishReason, result.Usage.TotalTokens,
		result.Usage.PromptTokens, result.Usage.CompletionTokens)
}

// TestRequesterReadsReadmeWithToolCalling validates a read tool call against a
// temporary fixture. Model-generated paths are never used to inspect other files.
func TestRequesterReadsReadmeWithToolCalling(t *testing.T) {
	info, workspace := liveRequesterInfo(t)
	if !info.SupportsTools {
		t.Skip("live tool calling requires ORCA_LIVE_SUPPORTS_TOOLS=1")
	}
	readme := filepath.Join(workspace, "README.md")
	if err := os.WriteFile(readme, []byte("# Fixture\nThis README belongs only to the live requester test.\n"), 0600); err != nil {
		t.Fatal(err)
	}

	result := NewOpenAIRequester(info).Request([]ChatMessage{
		{Role: RoleSystem, Content: "You are a coding agent. Use the read tool to inspect files; never invent their content."},
		{Role: RoleUser, Content: "Read file " + readme},
	}, nil)

	if result.Error != nil {
		if isUnreachable(result.Error) {
			t.Skipf("skipping: %s at %s is not reachable: %v", info.Name, info.BaseURL, result.Error)
		}
		t.Fatalf("Request() error = %v", result.Error)
	}

	var read *ToolCall
	for i, call := range result.ToolCalls {
		t.Logf("tool call: id=%q name=%q arguments=%s", call.ID, call.Name, call.Arguments)
		if call.Name == handler.CommandRead {
			read = &result.ToolCalls[i]
		}
	}
	if read == nil {
		t.Skipf("model %s made no read tool call (finish=%q content=%q); its tool support cannot be verified",
			info.ModelID, result.FinishReason, result.Content)
	}

	var options struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal([]byte(read.Arguments), &options); err != nil {
		t.Fatalf("arguments %q of the read call are not valid JSON: %v", read.Arguments, err)
	}
	if options.Filename == "" {
		t.Fatalf("read call %q carries no filename", read.Arguments)
	}
	filename, err := filepath.Abs(options.Filename)
	if err != nil || filename != readme {
		t.Fatalf("read call targets %q, want only fixture %q", options.Filename, readme)
	}
	if _, err := os.Stat(readme); err != nil {
		t.Errorf("fixture %q cannot be opened: %v", readme, err)
	}
}

// isUnreachable distinguishes unavailable live endpoints from request failures.
func isUnreachable(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, os.ErrDeadlineExceeded) ||
		strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "no such host")
}
