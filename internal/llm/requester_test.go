package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xiws/orca/internal/handler"
)

// TestRequesterParsesOpenAIStream 驱动 Request 对接预制的 OpenAI 风格 SSE 流，
// 使流式传输、工具调用组装和 usage 解析可以在不访问真实模型的情况下确定性地验证。
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

// TestRequesterNilChannelDoesNotBlock 确认 msgs 可以为 nil，
// 此时内容仅收集到返回的 Result 中。它对接相同的 fake 服务器，因此不需要实时模型。
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

// sentRequest 镜像 openAIClient.body 编组的 JSON 请求体，
// 使 fake 服务器可以检查将交给模型的内容。
type sentRequest struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
	Tools    []Tool        `json:"tools"`
}

// TestRequestSendsToolsOnlyWhenModelSupportsThem 固定工具调用的请求端：
// 内置命令仅在模型报告工具支持时声明为 OpenAI 工具，
// 对话按顺序传输并请求流式响应。
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
			wantNames := []string{handler.CommandRead, handler.CommandWrite, handler.CommandEdit, handler.CommandBash}
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

// TestRequestBodyKeepsToolProtocolOrder 检查完整工具调用往返的线路形式：
// 请求工具的助手轮次、按 id 回复的工具消息，以及下一个助手轮次。
//
// 丢失任何一半都会使模型失去已做过的跟踪：没有助手消息，工具输出没有作者；
// 没有 tool_call_id，严格的服务端会拒绝整个对话。
func TestRequestBodyKeepsToolProtocolOrder(t *testing.T) {
	client := &openAIClient{info: ModelInfo{ModelID: "fake-model", SupportsTools: true}}
	body := client.body([]ChatMessage{
		{Role: RoleSystem, Content: "You are a coding agent."},
		{Role: RoleUser, Content: "Summarise a.md"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "call-1", Name: handler.CommandRead, Arguments: `{"filename":"a.md"}`},
			{ID: "call-2", Name: handler.CommandBash, Arguments: `{"content":"pwd"}`},
		}},
		{Role: RoleTool, ToolCallID: "call-1", Content: `{"ok":true}`},
		{Role: RoleTool, ToolCallID: "call-2", Content: `{"ok":true}`},
		{Role: RoleAssistant, Content: "done"},
		// 空条目不再携带模型可用的内容，不应到达线路。
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
	if got := assistant.ToolCalls[0]; got.ID != "call-1" || got.Type != ToolType || got.Function.Name != handler.CommandRead || got.Function.Arguments != `{"filename":"a.md"}` {
		t.Errorf("assistant tool call = %+v, want the read call in OpenAI shape", got)
	}

	for i, wantID := range []string{"call-1", "call-2"} {
		if got := sent.Messages[3+i].ToolCallID; got != wantID {
			t.Errorf("tool message %d answers %q, want %q", i, got, wantID)
		}
	}
}

// TestRequesterAssemblesParallelToolCalls 输入一个两个工具调用交错且乱序到达的流，
// 参数跨分块拆分，检查它们被重新组装为按索引排序的完整调用。
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
		{ID: "call_1", Name: handler.CommandRead, Arguments: `{"filename":"README.md"}`},
		{ID: "call_2", Name: handler.CommandBash, Arguments: `{"content":"go test"}`},
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
