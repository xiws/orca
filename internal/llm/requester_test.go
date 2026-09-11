package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// defaultProvider / defaultModel mirror .orca/setting.json and select the model
// used by the live integration test below.
const (
	defaultProvider = "ollama"
	defaultModel    = "ornith-1.5:9b"
)

// TestRequesterParsesOpenAIStream drives Request against a canned OpenAI-style
// SSE stream, so the streaming, tool-call assembly and usage parsing are
// verified deterministically without reaching a real model.
func TestRequesterParsesOpenAIStream(t *testing.T) {
	const sse = "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"read\",\"arguments\":\"{\\\"fil\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"ename\\\":\\\"a.txt\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":7,\"total_tokens\":12}}\n\n" +
		"data: [DONE]\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/chat/completions" {
			t.Errorf("request path = %q, want %q", got, "/chat/completions")
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer server.Close()

	requester := NewOpenAIRequester(ModelInfo{
		Provider: "fake",
		API:      "openai-completions",
		BaseURL:  server.URL,
		APIKey:   "test-key",
		ModelID:  "fake-model",
	})

	msgs := make(chan string)
	var streamed []string
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for chunk := range msgs {
			streamed = append(streamed, chunk)
		}
	}()

	result := requester.Request([]ChatMessage{{Role: RoleUser, Content: "hi"}}, msgs)
	close(msgs)
	wg.Wait()

	if result.Error != nil {
		t.Fatalf("Request() error = %v", result.Error)
	}
	if result.Content != "Hello" {
		t.Errorf("Content = %q, want %q", result.Content, "Hello")
	}
	if strings.Join(streamed, "") != result.Content {
		t.Errorf("streamed %q != Content %q", strings.Join(streamed, ""), result.Content)
	}
	if result.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want %q", result.FinishReason, "tool_calls")
	}
	if result.Usage.TotalTokens != 12 {
		t.Errorf("Usage.TotalTokens = %d, want 12", result.Usage.TotalTokens)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(result.ToolCalls))
	}
	call := result.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "read" || call.Arguments != `{"filename":"a.txt"}` {
		t.Errorf("ToolCall = %+v, want {call_1 read {\"filename\":\"a.txt\"}}", call)
	}
}

// TestRequesterNilChannelDoesNotBlock confirms msgs may be nil, in which case
// the content is only collected into the returned Result. It runs against the
// same fake server so it needs no live model.
func TestRequesterNilChannelDoesNotBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	requester := NewOpenAIRequester(ModelInfo{API: "openai-completions", BaseURL: server.URL, ModelID: "fake-model"})
	result := requester.Request([]ChatMessage{{Role: RoleUser, Content: "hi"}}, nil)
	if result.Error != nil {
		t.Fatalf("Request() error = %v", result.Error)
	}
	if result.Content != "ok" {
		t.Errorf("Content = %q, want %q", result.Content, "ok")
	}
}

// sentRequest mirrors the JSON body openAIClient.body marshals, so a fake
// server can inspect what would be handed to the model.
type sentRequest struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
	Tools    []Tool        `json:"tools"`
}

// TestRequestSendsToolsOnlyWhenModelSupportsThem pins down the request side of
// tool calling: the built-in commands are declared as OpenAI tools only for a
// model that reports tool support, and the conversation travels in order with
// streaming requested.
func TestRequestSendsToolsOnlyWhenModelSupportsThem(t *testing.T) {
	tests := []struct {
		name          string
		supportsTools bool
		wantTools     int
	}{
		{name: "tool capable model declares every command", supportsTools: true, wantTools: len(Tools())},
		{name: "model without tool support declares none", supportsTools: false, wantTools: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sent sentRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()

			requester := NewOpenAIRequester(ModelInfo{
				API:           "openai-completions",
				BaseURL:       server.URL,
				ModelID:       "fake-model",
				SupportsTools: tt.supportsTools,
			})
			result := requester.Request([]ChatMessage{
				{Role: RoleSystem, Content: "You are a concise assistant."},
				{Role: RoleUser, Content: "Read README.md"},
			}, nil)
			if result.Error != nil {
				t.Fatalf("Request() error = %v", result.Error)
			}

			if sent.Model != "fake-model" {
				t.Errorf("request model = %q, want %q", sent.Model, "fake-model")
			}
			if !sent.Stream {
				t.Error("request should ask for a streamed response")
			}
			wantRoles := []string{RoleSystem, RoleUser}
			if len(sent.Messages) != len(wantRoles) {
				t.Fatalf("sent %d messages, want %d", len(sent.Messages), len(wantRoles))
			}
			for i, role := range wantRoles {
				if sent.Messages[i].Role != role {
					t.Errorf("message %d role = %q, want %q", i, sent.Messages[i].Role, role)
				}
			}

			if len(sent.Tools) != tt.wantTools {
				t.Fatalf("request declared %d tools, want %d", len(sent.Tools), tt.wantTools)
			}
			if tt.wantTools == 0 {
				return
			}
			wantNames := []string{ToolRead, ToolWrite, ToolEdit, ToolBash}
			for i, name := range wantNames {
				if sent.Tools[i].Function.Name != name {
					t.Errorf("tool %d = %q, want %q", i, sent.Tools[i].Function.Name, name)
				}
				if sent.Tools[i].Type != ToolType {
					t.Errorf("tool %q type = %q, want %q", name, sent.Tools[i].Type, ToolType)
				}
			}
		})
	}
}

// TestRequestBodyKeepsToolProtocolOrder checks the wire form of a full tool
// calling round trip: the assistant turn that asks for a tool, the tool message
// that answers it by id, and the next assistant turn.
//
// Losing either half is what makes a model lose track of what it has already
// done: without the assistant message the tool output has no author, and
// without the tool_call_id a strict server rejects the whole conversation.
func TestRequestBodyKeepsToolProtocolOrder(t *testing.T) {
	client := &openAIClient{info: ModelInfo{ModelID: "fake-model", SupportsTools: true}}
	body := client.body([]ChatMessage{
		{Role: RoleSystem, Content: "You are a coding agent."},
		{Role: RoleUser, Content: "Summarise a.md"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "call-1", Name: ToolRead, Arguments: `{"filename":"a.md"}`},
			{ID: "call-2", Name: ToolBash, Arguments: `{"content":"pwd"}`},
		}},
		{Role: RoleTool, ToolCallID: "call-1", Content: `{"ok":true}`},
		{Role: RoleTool, ToolCallID: "call-2", Content: `{"ok":true}`},
		{Role: RoleAssistant, Content: "done"},
		// An empty entry no longer carries anything the model can use and must
		// not reach the wire at all.
		{Role: RoleUser, Content: ""},
	})

	var sent sentRequest
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("Unmarshal() error = %v, body: %s", err, body)
	}

	wantRoles := []string{RoleSystem, RoleUser, RoleAssistant, RoleTool, RoleTool, RoleAssistant}
	if len(sent.Messages) != len(wantRoles) {
		t.Fatalf("sent %d messages, want %d: %s", len(sent.Messages), len(wantRoles), body)
	}
	for i, want := range wantRoles {
		if got := sent.Messages[i].Role; got != want {
			t.Errorf("message %d role = %q, want %q", i, got, want)
		}
	}

	assistant := sent.Messages[2]
	if len(assistant.ToolCalls) != 2 {
		t.Fatalf("assistant tool calls = %d, want 2", len(assistant.ToolCalls))
	}
	if got := assistant.ToolCalls[0]; got.ID != "call-1" || got.Type != ToolType || got.Function.Name != ToolRead || got.Function.Arguments != `{"filename":"a.md"}` {
		t.Errorf("assistant tool call = %+v, want the read call in OpenAI shape", got)
	}

	for i, wantID := range []string{"call-1", "call-2"} {
		if got := sent.Messages[3+i].ToolCallID; got != wantID {
			t.Errorf("tool message %d answers %q, want %q", i, got, wantID)
		}
	}
}

// TestRequesterAssemblesParallelToolCalls feeds a stream where two tool calls
// arrive interleaved and out of index order, with their arguments split across
// chunks, and checks they are reassembled into complete calls ordered by index.
func TestRequesterAssemblesParallelToolCalls(t *testing.T) {
	const sse = `
data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"bash","arguments":"{\"content\":\"go "}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"filename\":\"README.md"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"test\"}"}}]},"finish_reason":"tool_calls"}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]}}]}

data: [DONE]

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse)
	}))
	defer server.Close()

	requester := NewOpenAIRequester(ModelInfo{API: "openai-completions", BaseURL: server.URL, ModelID: "fake-model", SupportsTools: true})
	result := requester.Request([]ChatMessage{{Role: RoleUser, Content: "read the readme and run the tests"}}, nil)
	if result.Error != nil {
		t.Fatalf("Request() error = %v", result.Error)
	}
	if result.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want %q", result.FinishReason, "tool_calls")
	}

	want := []ToolCall{
		{ID: "call_1", Name: ToolRead, Arguments: `{"filename":"README.md"}`},
		{ID: "call_2", Name: ToolBash, Arguments: `{"content":"go test"}`},
	}
	if len(result.ToolCalls) != len(want) {
		t.Fatalf("len(ToolCalls) = %d, want %d", len(result.ToolCalls), len(want))
	}
	for i, call := range result.ToolCalls {
		if call != want[i] {
			t.Errorf("ToolCalls[%d] = %+v, want %+v", i, call, want[i])
		}
	}
}

// TestRequesterWithDefaultModel issues a real streaming request to the model
// configured in .orca/setting.json (ollama / ornith-1.5:9b). It is skipped in
// short mode or when the local server is not running.
func TestRequesterWithDefaultModel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live model request in short mode")
	}

	// Resolve .orca/models.json from the repository root, not the package dir.
	t.Chdir("../..")

	info := GetProvider(defaultProvider, defaultModel)
	if info.BaseURL == "" {
		t.Fatalf("GetProvider(%q, %q) returned empty ModelInfo; check .orca/models.json", defaultProvider, defaultModel)
	}

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

// TestRequesterReadsReadmeWithToolCalling is the round trip of tool calling
// against the model configured in .orca/models.json: it asks the model to read
// the repository README.md, expects a read tool call back, and decodes its
// arguments the way handler.OptionFromCall will. It is skipped in short mode,
// when the endpoint is unreachable or when the model does not support tools.
func TestRequesterReadsReadmeWithToolCalling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live tool calling request in short mode")
	}

	// Resolve .orca/models.json and README.md from the repository root.
	t.Chdir("../..")

	info := GetProvider(defaultProvider, defaultModel)
	if info.BaseURL == "" {
		t.Fatalf("GetProvider(%q, %q) returned empty ModelInfo; check .orca/models.json", defaultProvider, defaultModel)
	}
	if !info.SupportsTools {
		t.Skipf("model %s/%s declares no tool support in .orca/models.json", defaultProvider, defaultModel)
	}

	result := NewOpenAIRequester(info).Request([]ChatMessage{
		{Role: RoleSystem, Content: "You are a coding agent. Use the read tool to inspect files; never invent their content."},
		{Role: RoleUser, Content: "Read file /Users/zhongxiwang/workspace/golang/orca/README.md"},
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
		if call.Name == ToolRead {
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
	if base := filepath.Base(options.Filename); base != "README.md" {
		t.Errorf("read call targets %q, want %q", options.Filename, "README.md")
	}
	// The runtime can only serve a path that really exists relative to the
	// workspace, which is what the tool description promises the model.
	if _, err := os.Stat(options.Filename); err != nil {
		t.Errorf("read call filename %q cannot be opened: %v", options.Filename, err)
	}
}

// isUnreachable reports whether err indicates the model endpoint could not be
// contacted, which distinguishes "ollama not running" from a real failure.
func isUnreachable(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, os.ErrDeadlineExceeded) ||
		strings.Contains(err.Error(), "connection refused") ||
		strings.Contains(err.Error(), "no such host")
}
