package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEndpointTrimsTrailingSlash pins the URL the client would POST to: a base
// URL carrying a trailing slash must be trimmed, and every provider is expected
// to be reached under the fixed /chat/completions route.
func TestEndpointTrimsTrailingSlash(t *testing.T) {
	tests := []struct {
		name string
		base string
		want string
	}{
		{
			name: "trailing slash is trimmed",
			base: "https://example.com/v1/",
			want: "https://example.com/v1/chat/completions",
		},
		{
			name: "no trailing slash is left untouched",
			base: "https://example.com/v1",
			want: "https://example.com/v1/chat/completions",
		},
		{
			name: "empty base yields the route only",
			base: "",
			want: "/chat/completions",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &openAIClient{info: ModelInfo{BaseURL: tt.base}}
			if got := c.endpoint(); got != tt.want {
				t.Errorf("endpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestEndpointReachesCompletionsRoute checks the live side: the request is sent
// to the expected path with an upstream built by the constructor.
func TestEndpointReachesCompletionsRoute(t *testing.T) {
	type captured struct {
		method string
		path   string
	}
	var got captured
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = captured{method: r.Method, path: r.URL.Path}
		_, _ = w.Write([]byte("data: {" + `
data: [DONE]` + "}"))
	}))
	defer server.Close()

	c := &openAIClient{info: ModelInfo{BaseURL: string(server.URL), ModelID: "fake-model"}}
	_ = c.Request([]ChatMessage{{Role: RoleUser, Content: "hi"}}, nil)
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.path != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", got.path)
	}
}

// TestEndpointDoesNotAttachToolsWhenUnsupported re-checks the request side with
// the upstream verifying it received no tool declarations.
func TestEndpointDoesNotAttachToolsWhenUnsupported(t *testing.T) {
	var sawTools bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent sentRequest
		if err := json.NewDecoder(r.Body).Decode(&sent); err == nil && len(sent.Tools) != 0 {
			sawTools = true
		}
		_, _ = w.Write([]byte("data: {}" + `
data: [DONE]`))
	}))
	defer server.Close()

	c := &openAIClient{info: ModelInfo{
		API:           "openai-completions",
		BaseURL:       server.URL,
		ModelID:       "fake-model",
		SupportsTools: false,
	}}
	_ = c.Request([]ChatMessage{{Role: RoleUser, Content: "hi"}}, nil)
	if sawTools {
		t.Error("request should not declare tools for a model without tool support")
	}
}

// TestSSEDataExtractsPayloads verifies the SSE helper: a usable data line is
// returned trimmed, while blank, non-data and empty-data lines are reported as
// carrying no data.
func TestSSEDataExtractsPayloads(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "plain data line", in: "data: hello", want: "hello", ok: true},
		{name: "leading whitespace is trimmed", in: "   data:    hi   ", want: "hi", ok: true},
		{name: "prefixed data line", in: "data: hello world", want: "hello world", ok: true},
		{name: "empty data is dropped", in: "data:  ", want: "", ok: false},
		{name: "blank line is dropped", in: "   ", want: "", ok: false},
		{name: "event line is ignored", in: "event: message", want: "", ok: false},
		{name: "comment is ignored", in: ": just a comment", want: "", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := sseData(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Errorf("sseData(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestWireMessageDropsEmptyMessages confirms the conversion rules: a message
// carrying no useful content is filtered out, while an assistant turn that only
// carries tool calls is kept even when its content is empty.
func TestWireMessageDropsEmptyMessages(t *testing.T) {
	if msg, ok := wireMessage(ChatMessage{Role: RoleUser, Content: "hi"}); !ok || msg.Content != "hi" {
		t.Errorf("wireMessage(user) = (%+v, %v), want (content:hi, true)", msg, ok)
	}
	if _, ok := wireMessage(ChatMessage{Role: RoleUser, Content: ""}); ok {
		t.Error("wireMessage(empty) should be dropped")
	}

	// An empty-content assistant turn that carries tool calls is kept.
	var assistants = ChatMessage{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: ToolRead}}}
	if msg, ok := wireMessage(assistants); !ok || len(msg.ToolCalls) != 1 || msg.ToolCallID != "" {
		t.Errorf("wireMessage(tool assistant) = (%+v, %v), want a kept message with one tool call", msg, ok)
	}
}

// TestWireMessageTranslatesToolCalls checks each tool call is rewritten into the
// OpenAI shape, using the constant tool type and preserving name and arguments.
func TestWireMessageTranslatesToolCalls(t *testing.T) {
	message, ok := wireMessage(ChatMessage{
		Role:       RoleAssistant,
		Content:    "please",
		ToolCallID: "call-2",
		ToolCalls: []ToolCall{
			{ID: "call-1", Name: ToolRead, Arguments: `{"filename":"a.md"}`},
		},
	})
	if !ok {
		t.Fatalf("wireMessage() returned ok=false")
	}
	if message.Role != RoleAssistant || message.Content != "please" || message.ToolCallID != "call-2" {
		t.Errorf("scalar fields = %+v, want them preserved", message)
	}
	if len(message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(message.ToolCalls))
	}
	tc := message.ToolCalls[0]
	if tc.ID != "call-1" || tc.Type != ToolType || tc.Function.Name != ToolRead || tc.Function.Arguments != `{"filename":"a.md"}` {
		t.Errorf("tool call = %+v, want the read call in OpenAI shape", tc)
	}
}
