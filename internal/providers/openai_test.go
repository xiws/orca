package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiws/orca/internal/model"
)

type resolverFunc func(context.Context, model.Ref) (Connection, error)

func (f resolverFunc) Resolve(ctx context.Context, ref model.Ref) (Connection, error) {
	return f(ctx, ref)
}
func requestForTest() model.Request {
	return model.Request{Model: model.Ref{Provider: "local", Model: "test"}, ThreadID: 41, ContextVersion: 2, MaxOutputTokens: 128, Messages: []model.Message{{Role: "user", Content: "hello"}}, Tools: []model.Tool{{Name: "custom", Description: "from gateway", Parameters: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`)}}}
}
func httpClientForTest(server *httptest.Server, native bool) *Client {
	return New(resolverFunc(func(context.Context, model.Ref) (Connection, error) {
		return Connection{API: "openai-completions", BaseURL: server.URL + "/v1", APIKey: "secret-key", SupportsTools: native, ContextWindow: 10000}, nil
	}))
}
func frame(w io.Writer, raw string) { _, _ = fmt.Fprintf(w, "data: %s\n\n", raw) }
func finishStream(w io.Writer) {
	frame(w, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	frame(w, "[DONE]")
}

func TestOpenAIRequestStreamingToolsAndStableOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer secret-key" {
			t.Error("wrong endpoint or authentication")
		}
		var body wireRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.MaxTokens != 128 || !body.Stream || len(body.Tools) != 1 || body.Tools[0].Function.Name != "custom" || len(body.Messages) != 3 || len(body.Messages[1].ToolCalls) != 1 || body.Messages[2].ToolCallID != "old-id" {
			t.Errorf("wrong request: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		frame(w, `{"choices":[{"index":0,"delta":{"content":"hi ","tool_calls":[{"index":1,"function":{"name":"custom","arguments":"{\"x\":"}}]}}]}`)
		frame(w, `{"choices":[{"index":0,"delta":{"content":"there","tool_calls":[{"index":0,"id":"provided","type":"function","function":{"name":"cus","arguments":"{\"x\":1}"}}]}}]}`)
		frame(w, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}},{"index":0,"function":{"name":"tom"}}]},"finish_reason":"tool_calls"}]}`)
		frame(w, `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
		frame(w, "[DONE]")
	}))
	defer server.Close()
	req := requestForTest()
	req.Messages = append(req.Messages, model.Message{Role: "assistant", ToolCalls: []model.Call{{ID: "old-id", Name: "custom", Arguments: `{"x":0}`}}}, model.Message{Role: "tool", ToolCallID: "old-id", Content: "old result"})
	client := httpClientForTest(server, true)
	var text strings.Builder
	result, err := client.Complete(context.Background(), req, func(s string) { text.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Cursor != nil || text.String() != "hi there" || result.Message.Content != "hi there" || result.Usage.TotalTokens != 15 {
		t.Fatalf("bad result: %+v", result)
	}
	calls := result.Message.ToolCalls
	if len(calls) != 2 || calls[0].ID != "provided" || calls[0].Arguments != `{"x":1}` || calls[1].Arguments != `{"x":2}` || calls[1].ID == "" {
		t.Fatalf("bad ordered calls: %+v", calls)
	}
	again, err := client.Complete(context.Background(), req, nil)
	if err != nil || !reflect.DeepEqual(again.Message.ToolCalls, calls) {
		t.Fatal("unstable calls", err)
	}
}

func TestOpenAIFallbackOnlyGatewayAndExplicitCalls(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		calls      int
		bad        bool
	}{
		{"plain", "ordinary answer", 0, false},
		{"prose", `Example: {"command":"custom","data":{"x":1}}`, 0, false},
		{"explicit", `<tool_calls>[{"name":"custom","arguments":{"x":1}}]</tool_calls>`, 1, false},
		{"legacy", `{"command":"custom","data":{"x":1}}`, 1, false},
		{"broken", `<tool_calls>[`, 0, true},
		{"undeclared", `{"command":"bash","data":{}}`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body wireRequest
				_ = json.NewDecoder(r.Body).Decode(&body)
				if len(body.Tools) != 0 || len(body.Messages) < 2 || !strings.Contains(body.Messages[0].Content, "from gateway") || strings.Contains(body.Messages[0].Content, `"bash"`) {
					t.Error("fallback declaration drift")
				}
				raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": tc.text}}}})
				frame(w, string(raw))
				finishStream(w)
			}))
			defer server.Close()
			result, err := httpClientForTest(server, false).Complete(context.Background(), requestForTest(), nil)
			if tc.bad {
				if !errors.Is(err, model.ErrUnknown) || !errors.Is(err, model.ErrToolProtocol) || result.Complete {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				return
			}
			if err != nil || !result.Complete || len(result.Message.ToolCalls) != tc.calls {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestOpenAIIncompleteStreamsNeverComplete(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty", ""},
		{"EOF", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"},
		{"finish without DONE", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"},
		{"DONE without finish", "data: [DONE]\n\n"},
		{"malformed JSON", "data: {broken}\n\ndata: [DONE]\n\n"},
		{"length", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"},
		{"missing choice", "data: {}\n\n"},
		{"missing index", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"name\":\"custom\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"},
		{"bad args", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"custom\",\"arguments\":\"{\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			defer server.Close()
			result, err := httpClientForTest(server, true).Complete(context.Background(), requestForTest(), nil)
			if !errors.Is(err, model.ErrUnknown) || result.Complete || result.Cursor != nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestOpenAICancellationAndUnknownSubmission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frame(w, `{"choices":[{"delta":{"content":"started"}}]}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := httpClientForTest(server, true).Complete(ctx, requestForTest(), func(string) { cancel() })
	if !errors.Is(err, model.ErrUnknown) || !errors.Is(err, context.Canceled) || result.Complete {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, err = httpClientForTest(server, true).Complete(ctx, requestForTest(), nil)
	if !errors.Is(err, context.Canceled) || errors.Is(err, model.ErrUnknown) {
		t.Fatalf("presubmission cancellation: %v", err)
	}
}

func TestOpenAIHTTPStatusRedactionAndNoRedirect(t *testing.T) {
	for _, status := range []int{401, 429, 500, 408, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "http://invalid.invalid/leak")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "secret-key Bearer reflected-secret")
			}))
			defer server.Close()
			result, err := httpClientForTest(server, true).Complete(context.Background(), requestForTest(), nil)
			if err == nil || strings.Contains(err.Error(), "secret") || result.Complete {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if errors.Is(err, model.ErrUnknown) != (status == 500 || status == 408) {
				t.Fatal("wrong classification", err)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestOpenAITransportErrorAndSharedTransport(t *testing.T) {
	resolver := resolverFunc(func(context.Context, model.Ref) (Connection, error) {
		return Connection{API: "openai", BaseURL: "http://localhost"}, nil
	})
	c := New(resolver)
	if c.http != New(resolver).http || c.http.Transport != sharedHTTP.Transport {
		t.Fatal("client/transport not shared")
	}
	c.http = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("Authorization: secret") })}
	_, err := c.Complete(context.Background(), requestForTest(), nil)
	if !errors.Is(err, model.ErrUnknown) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}

func TestContextLimitBeforeSubmissionAndSecretsNotJSON(t *testing.T) {
	var sends atomic.Int32
	c := New(resolverFunc(func(context.Context, model.Ref) (Connection, error) {
		return Connection{API: "openai", BaseURL: "http://localhost", ContextWindow: 100, SupportsTools: true}, nil
	}))
	c.http = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) { sends.Add(1); return nil, errors.New("called") })}
	req := requestForTest()
	req.MaxOutputTokens = 1
	req.Messages = append(req.Messages, model.Message{Role: "assistant", ToolCalls: []model.Call{{ID: "id", Name: "custom", Arguments: strings.Repeat("a", 2000)}}}, model.Message{Role: "tool", ToolCallID: "id", Content: "result"})
	before, _ := json.Marshal(req.Messages)
	_, err := c.Complete(context.Background(), req, nil)
	after, _ := json.Marshal(req.Messages)
	if err == nil || errors.Is(err, model.ErrUnknown) || sends.Load() != 0 || string(before) != string(after) {
		t.Fatalf("context check err=%v sends=%d", err, sends.Load())
	}
	raw, _ := json.Marshal(Connection{APIKey: "secret", BaseURL: "http://secret", API: "openai"})
	if strings.Contains(string(raw), "secret") {
		t.Fatal("credentials marshaled")
	}
}

func TestOpenAIRequestDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err := httpClientForTest(server, true).Complete(ctx, requestForTest(), nil)
	if !errors.Is(err, model.ErrUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
