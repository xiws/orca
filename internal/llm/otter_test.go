package llm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xiws/otter/pkg/provider"
)

// fakeOtterBackend 记录接收到的请求并为每次调用重放脚本化的流，
// 使适配器可以在没有 otter 平台的情况下被驱动。
type fakeOtterBackend struct {
	requests []*provider.SendRequest
	replies  [][]provider.StreamEvent
}

func (f *fakeOtterBackend) Name() string { return "fake" }

func (f *fakeOtterBackend) SendStream(_ context.Context, req *provider.SendRequest) (<-chan provider.StreamEvent, error) {
	f.requests = append(f.requests, req)
	var script []provider.StreamEvent
	if len(f.replies) > 0 {
		script = f.replies[0]
		f.replies = f.replies[1:]
	}
	events := make(chan provider.StreamEvent, len(script))
	for _, event := range script {
		events <- event
	}
	close(events)
	return events, nil
}

// fakeRemoteBackend 添加了 DeepSeek 风格平台需要的服务端会话创建；
// 普通的 fakeOtterBackend 不满足该能力断言，
// 这正是 ChatGPT/Gemini 形式的测试所需的。
type fakeRemoteBackend struct {
	fakeOtterBackend
	remoteID  string
	createNum int
}

func (f *fakeRemoteBackend) CreateRemoteSession(context.Context) (string, error) {
	f.createNum++
	return f.remoteID, nil
}

// newFakeOtterRequester 将 requester 接入 fake，绕过真实构建器运行的配置和登录逻辑。
func newFakeOtterRequester(fake otterBackend) *otterRequester {
	return newOtterRequester(ModelInfo{Provider: "otter", API: APIOtter, ModelID: "deepseek"}, func(string) (otterBackend, error) {
		return fake, nil
	})
}

// drain 收集 requester 流式传输到 msgs 的内容并报告为一个字符串。
func drain(msgs chan string) string {
	close(msgs)
	var out strings.Builder
	for chunk := range msgs {
		out.WriteString(chunk)
	}
	return out.String()
}

// TestOtterRequesterFirstRequestSendsSystemAndUser 驱动首次调用：
// 平台会话在服务端创建，提示拼接系统上下文和用户轮次，
// 回复流式回传到 msgs。
func TestOtterRequesterFirstRequestSendsSystemAndUser(t *testing.T) {
	fake := &fakeRemoteBackend{remoteID: "chat-1"}
	fake.replies = [][]provider.StreamEvent{
		{
			{Type: "text", Content: "Hel"},
			{Type: "text", Content: "lo"},
			{Type: "meta", ResponseMessageID: 42, TokenUsage: 7},
			{Type: "done"},
		},
	}
	requester := newFakeOtterRequester(fake)

	messages := []ChatMessage{
		{Role: RoleSystem, Content: "You are Orca."},
		{Role: RoleSystem, Content: "You can act by replying with JSON commands."},
		{Role: RoleUser, Content: "read main.go"},
	}
	msgs := make(chan string, 16)
	result := requester.Request(messages, msgs)
	if result.Error != nil {
		t.Fatalf("Request() error = %v", result.Error)
	}
	if result.Content != "Hello" {
		t.Errorf("Content = %q, want %q", result.Content, "Hello")
	}
	if result.Usage.TotalTokens != 7 {
		t.Errorf("Usage.TotalTokens = %d, want the token usage of the reply", result.Usage.TotalTokens)
	}
	if streamed := drain(msgs); streamed != "Hello" {
		t.Errorf("streamed = %q, want %q", streamed, "Hello")
	}

	if len(fake.requests) != 1 {
		t.Fatalf("platform received %d requests, want 1", len(fake.requests))
	}
	request := fake.requests[0]
	want := "You are Orca.\n\nYou can act by replying with JSON commands.\n\nread main.go"
	if request.Prompt != want {
		t.Errorf("Prompt = %q, want %q", request.Prompt, want)
	}
	if request.ChatSessionID != "chat-1" {
		t.Errorf("ChatSessionID = %q, want the server-side session id", request.ChatSessionID)
	}
}

// TestOtterRequesterFollowUpSendsToolResultsOnly 检查增量：
// 第二次调用时系统上下文和用户轮次已在平台上，
// 因此只发送工具结果，引用上一次回复 id。
func TestOtterRequesterFollowUpSendsToolResultsOnly(t *testing.T) {
	fake := &fakeRemoteBackend{remoteID: "chat-1"}
	fake.replies = [][]provider.StreamEvent{
		{{Type: "text", Content: "one"}, {Type: "meta", ResponseMessageID: 7}, {Type: "done"}},
		{{Type: "text", Content: "two"}, {Type: "done"}},
	}
	requester := newFakeOtterRequester(fake)

	opening := []ChatMessage{
		{Role: RoleSystem, Content: "be careful"},
		{Role: RoleUser, Content: "read a.md"},
	}
	if result := requester.Request(opening, nil); result.Error != nil {
		t.Fatalf("first Request() error = %v", result.Error)
	}

	followUp := append(append([]ChatMessage(nil), opening...),
		ChatMessage{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_0", Name: "read", Arguments: `{"filename":"a.md"}`}}},
		ChatMessage{Role: RoleTool, ToolCallID: "call_0", Content: `{"ok":true,"content":"hi"}`},
	)
	result := requester.Request(followUp, nil)
	if result.Error != nil {
		t.Fatalf("second Request() error = %v", result.Error)
	}
	if result.Content != "two" {
		t.Errorf("Content = %q, want %q", result.Content, "two")
	}

	if len(fake.requests) != 2 {
		t.Fatalf("platform received %d requests, want 2", len(fake.requests))
	}
	follow := fake.requests[1]
	want := "Tool result for read:\n{\"ok\":true,\"content\":\"hi\"}"
	if follow.Prompt != want {
		t.Errorf("follow-up Prompt = %q, want %q", follow.Prompt, want)
	}
	if follow.ParentMessageID != 7 {
		t.Errorf("ParentMessageID = %d, want the previous reply id", follow.ParentMessageID)
	}
	if fake.createNum != 1 {
		t.Errorf("CreateRemoteSession called %d times, want exactly one session", fake.createNum)
	}
}

// TestOtterRequesterStringFollowUpKeepsConversation 检查 ChatGPT/Gemini 形式：
// 无服务端会话创建，done 事件报告的对话 id 和回复 id 传入下一次请求。
func TestOtterRequesterStringFollowUpKeepsConversation(t *testing.T) {
	fake := &fakeOtterBackend{replies: [][]provider.StreamEvent{
		{
			{Type: "text", Content: "one"},
			{Type: "done", RemoteConversationID: "conv-9", ResponseMessageIDStr: "msg-1",
				RemoteMetadata: map[string]string{"conversation_metadata": "opaque"}},
		},
		{{Type: "text", Content: "two"}, {Type: "done"}},
	}}
	requester := newFakeOtterRequester(fake)

	if result := requester.Request([]ChatMessage{{Role: RoleUser, Content: "hi"}}, nil); result.Error != nil {
		t.Fatalf("first Request() error = %v", result.Error)
	}
	if result := requester.Request([]ChatMessage{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleTool, Content: `{"ok":true}`},
	}, nil); result.Error != nil {
		t.Fatalf("second Request() error = %v", result.Error)
	}

	follow := fake.requests[1]
	if follow.ChatSessionID != "conv-9" {
		t.Errorf("ChatSessionID = %q, want the conversation id of the done event", follow.ChatSessionID)
	}
	if follow.ParentMessageIDStr != "msg-1" {
		t.Errorf("ParentMessageIDStr = %q, want the reply id of the done event", follow.ParentMessageIDStr)
	}
	if follow.RemoteMetadata["conversation_metadata"] != "opaque" {
		t.Errorf("RemoteMetadata = %v, want the metadata of the done event", follow.RemoteMetadata)
	}
}

// TestOtterRequesterErrorEventFailsRequestAndKeepsTail 确保流中报告的错误
// 会使请求失败而不推进已发送标记，因此重试的调用方重新发送相同的尾部。
func TestOtterRequesterErrorEventFailsRequestAndKeepsTail(t *testing.T) {
	fake := &fakeOtterBackend{replies: [][]provider.StreamEvent{
		{{Type: "error", Err: errors.New("auth expired")}},
		{{Type: "text", Content: "ok"}, {Type: "done"}},
	}}
	requester := newFakeOtterRequester(fake)

	messages := []ChatMessage{{Role: RoleUser, Content: "hello"}}
	if result := requester.Request(messages, nil); result.Error == nil {
		t.Fatal("Request() error = nil, want the platform failure")
	}
	if result := requester.Request(messages, nil); result.Error != nil {
		t.Fatalf("retry Request() error = %v, want the same tail resent", result.Error)
	}

	if len(fake.requests) != 2 {
		t.Fatalf("platform received %d requests, want 2", len(fake.requests))
	}
	if fake.requests[0].Prompt != fake.requests[1].Prompt {
		t.Errorf("retry prompt = %q, want the same tail %q", fake.requests[1].Prompt, fake.requests[0].Prompt)
	}
}

// TestOtterRequesterRejectsEmptyTail 覆盖没有新消息可发送的请求，
// 否则会向平台发送空消息。
func TestOtterRequesterRejectsEmptyTail(t *testing.T) {
	requester := newFakeOtterRequester(&fakeOtterBackend{})
	result := requester.Request(nil, nil)
	if result.Error == nil {
		t.Fatal("Request() error = nil, want a no-new-message failure")
	}
	if !strings.Contains(result.Error.Error(), "no new message") {
		t.Errorf("error = %v, want it to say there is nothing to send", result.Error)
	}
}

// TestBuildOtterBackendRejectsUnknownPlatform 检查 otter 平台不认识的模型 id 被按名拒绝。
func TestBuildOtterBackendRejectsUnknownPlatform(t *testing.T) {
	_, err := buildOtterBackend("no-such-platform")
	if err == nil {
		t.Fatal("buildOtterBackend() error = nil, want a rejection")
	}
	if !strings.Contains(err.Error(), "unsupported platform") {
		t.Errorf("error = %v, want it to name the unsupported platform", err)
	}
}
