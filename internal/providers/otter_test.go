package providers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xiws/orca/internal/model"
	otterconfig "github.com/xiws/otter/pkg/config"
	otter "github.com/xiws/otter/pkg/provider"
)

type fakeOtter struct {
	send func(context.Context, *otter.SendRequest) (<-chan otter.StreamEvent, error)
}

func (f *fakeOtter) SendStream(ctx context.Context, r *otter.SendRequest) (<-chan otter.StreamEvent, error) {
	return f.send(ctx, r)
}

type fakeRemote struct {
	*fakeOtter
	create func(context.Context) (string, error)
}

func (f *fakeRemote) CreateRemoteSession(ctx context.Context) (string, error) { return f.create(ctx) }
func eventStream(events ...otter.StreamEvent) <-chan otter.StreamEvent {
	ch := make(chan otter.StreamEvent, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return ch
}
func otterClientForTest(build otterBackendBuilder) *Client {
	c := New(resolverFunc(func(context.Context, model.Ref) (Connection, error) {
		return Connection{API: "otter", ContextWindow: 10000}, nil
	}))
	c.buildOtter = build
	return c
}
func otterRequest() model.Request {
	r := requestForTest()
	r.Model = model.Ref{Provider: "web", Model: "deepseek"}
	return r
}
func successfulOtter(text string) <-chan otter.StreamEvent {
	return eventStream(otter.StreamEvent{Type: "text", Content: text}, otter.StreamEvent{Type: "done", RemoteConversationID: "remote", ResponseMessageID: 8, TokenUsage: 14, RemoteMetadata: map[string]string{"auth_token": "must-not-persist"}})
}

func TestOtterCursorOwnsAllStateAndOnlyDeliversTail(t *testing.T) {
	var requests []*otter.SendRequest
	builds, creates := 0, 0
	c := otterClientForTest(func(context.Context, string) (otterBackend, error) {
		builds++
		return &fakeRemote{fakeOtter: &fakeOtter{send: func(ctx context.Context, r *otter.SendRequest) (<-chan otter.StreamEvent, error) {
			copy := *r
			requests = append(requests, &copy)
			return successfulOtter("answer"), nil
		}}, create: func(context.Context) (string, error) { creates++; return "new-remote", nil }}, nil
	})
	req := otterRequest()
	req.Messages = []model.Message{{Role: "system", Content: "system"}, {Role: "user", Content: "old question"}, {Role: "assistant", Content: "historical assistant answer"}, {Role: "user", Content: "new question"}}
	first, err := c.Complete(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Complete || first.Cursor == nil || first.Cursor.Sequence != len(req.Messages)+1 || first.Usage.TotalTokens != 14 {
		t.Fatalf("%+v", first)
	}
	if !strings.Contains(requests[0].Prompt, "historical assistant answer") || requests[0].ChatSessionID != "new-remote" {
		t.Fatal("initial history missing")
	}
	raw, _ := json.Marshal(first.Cursor)
	if strings.Contains(string(raw), "must-not-persist") || strings.Contains(string(raw), "auth_token") {
		t.Fatal("credential metadata persisted")
	}
	req.Messages = append(req.Messages, first.Message, model.Message{Role: "tool", ToolCallID: "call", Content: "only new tail"})
	req.Cursor = first.Cursor
	before, _ := json.Marshal(req.Cursor)
	second, err := c.Complete(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(req.Cursor)
	if string(before) != string(after) || second.Cursor.Sequence != len(req.Messages)+1 {
		t.Fatal("input cursor mutated")
	}
	if builds != 2 || creates != 1 || requests[1].ChatSessionID != "remote" || requests[1].ParentMessageID != 8 || !strings.Contains(requests[1].Prompt, "only new tail") || strings.Contains(requests[1].Prompt, "historical assistant answer") || strings.Contains(requests[1].Prompt, "Previous assistant response:\nanswer") {
		t.Fatalf("builds=%d creates=%d request=%+v", builds, creates, requests[1])
	}
	// A new invocation on the same Client has no delivered count or remote ID.
	fresh := otterRequest()
	fresh.ThreadID = 77
	if _, err := c.Complete(context.Background(), fresh, nil); err != nil {
		t.Fatal(err)
	}
	if creates != 2 || requests[2].ParentMessageID != 0 || !strings.Contains(requests[2].Prompt, "hello") {
		t.Fatal("state leaked across invocations")
	}
}

func TestOtterCursorMismatchRejectedBeforeBackend(t *testing.T) {
	var builds atomic.Int32
	c := otterClientForTest(func(context.Context, string) (otterBackend, error) {
		builds.Add(1)
		return &fakeOtter{send: func(context.Context, *otter.SendRequest) (<-chan otter.StreamEvent, error) {
			return successfulOtter("answer"), nil
		}}, nil
	})
	req := otterRequest()
	first, err := c.Complete(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Messages = append(req.Messages, first.Message, model.Message{Role: "user", Content: "next"})
	req.Cursor = first.Cursor
	mutations := []struct {
		name   string
		mutate func(*model.Request)
	}{
		{"adapter", func(r *model.Request) { r.Cursor.Adapter = "openai" }},
		{"version", func(r *model.Request) { r.Cursor.Version = 2 }},
		{"model", func(r *model.Request) { r.Cursor.Model.Model = "other" }},
		{"provider", func(r *model.Request) { r.Cursor.Model.Provider = "other" }},
		{"thread", func(r *model.Request) { r.ThreadID++ }},
		{"context version", func(r *model.Request) { r.ContextVersion++ }},
		{"negative sequence", func(r *model.Request) { r.Cursor.Sequence = -1 }},
		{"exact sequence", func(r *model.Request) { r.Cursor.Sequence-- }},
		{"sequence ahead", func(r *model.Request) { r.Cursor.Sequence = len(r.Messages) + 1 }},
		{"history changed", func(r *model.Request) { r.Messages[0].Content = "rewritten" }},
		{"missing response", func(r *model.Request) { r.Messages = r.Messages[:1] }},
		{"bad state", func(r *model.Request) { r.Cursor.Data = json.RawMessage(`{"delivered":1}`) }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			copy := req
			copy.Messages = append([]model.Message(nil), req.Messages...)
			cursor := *req.Cursor
			copy.Cursor = &cursor
			tc.mutate(&copy)
			result, err := c.Complete(context.Background(), copy, nil)
			if !errors.Is(err, model.ErrCursorMismatch) || result.Complete || result.Cursor != nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	if builds.Load() != 1 {
		t.Fatal("mismatch reached backend")
	}
}

func TestOtterFailuresNeverCommit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		events  []otter.StreamEvent
		sendErr error
	}{
		{name: "EOF"},
		{name: "partial", events: []otter.StreamEvent{{Type: "text", Content: "partial"}}},
		{name: "error", events: []otter.StreamEvent{{Type: "error", Err: errors.New("secret"), Content: "secret"}}},
		{name: "done error", events: []otter.StreamEvent{{Type: "done", Err: errors.New("secret")}}},
		{name: "after done", events: []otter.StreamEvent{{Type: "done", RemoteConversationID: "remote", ResponseMessageID: 2}, {Type: "text", Content: "late"}}},
		{name: "no continuation", events: []otter.StreamEvent{{Type: "done"}}},
		{name: "bad call", events: []otter.StreamEvent{{Type: "text", Content: "<tool_calls>["}, {Type: "done", RemoteConversationID: "remote", ResponseMessageID: 2}}},
		{name: "unknown event", events: []otter.StreamEvent{{Type: "surprise"}}},
		{name: "submission", sendErr: errors.New("secret")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := otterClientForTest(func(context.Context, string) (otterBackend, error) {
				return &fakeOtter{send: func(context.Context, *otter.SendRequest) (<-chan otter.StreamEvent, error) {
					return eventStream(tc.events...), tc.sendErr
				}}, nil
			})
			result, err := c.Complete(context.Background(), otterRequest(), nil)
			if !errors.Is(err, model.ErrUnknown) || strings.Contains(err.Error(), "secret") || result.Complete || result.Cursor != nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestOtterCancellationPropagatesAndNoCommitAfterDone(t *testing.T) {
	for _, at := range []string{"build", "create", "send", "done"} {
		t.Run(at, func(t *testing.T) {
			type key struct{}
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "marker"))
			defer cancel()
			check := func(ctx context.Context) {
				if ctx.Value(key{}) != "marker" {
					t.Error("context value lost")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Error("deadline missing")
				}
			}
			c := otterClientForTest(func(ctx context.Context, _ string) (otterBackend, error) {
				check(ctx)
				if at == "build" {
					cancel()
					return nil, ctx.Err()
				}
				return &fakeRemote{fakeOtter: &fakeOtter{send: func(ctx context.Context, _ *otter.SendRequest) (<-chan otter.StreamEvent, error) {
					check(ctx)
					if at == "send" {
						cancel()
						return nil, ctx.Err()
					}
					ch := make(chan otter.StreamEvent, 2)
					ch <- otter.StreamEvent{Type: "done", RemoteConversationID: "r", ResponseMessageID: 1}
					cancel()
					close(ch)
					return ch, nil
				}}, create: func(ctx context.Context) (string, error) {
					check(ctx)
					if at == "create" {
						cancel()
						return "", ctx.Err()
					}
					return "r", nil
				}}, nil
			})
			result, err := c.Complete(ctx, otterRequest(), nil)
			if err == nil || result.Complete || result.Cursor != nil {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if errors.Is(err, model.ErrUnknown) != (at == "send" || at == "done") {
				t.Fatalf("classification: %v", err)
			}
		})
	}
	c := otterClientForTest(func(context.Context, string) (otterBackend, error) {
		return &fakeOtter{send: func(context.Context, *otter.SendRequest) (<-chan otter.StreamEvent, error) {
			return make(chan otter.StreamEvent), nil
		}}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Complete(ctx, otterRequest(), nil)
	if !errors.Is(err, model.ErrUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestOtterConstructionFailureIsKnown(t *testing.T) {
	c := otterClientForTest(func(context.Context, string) (otterBackend, error) { return nil, errors.New("token=secret") })
	_, err := c.Complete(context.Background(), otterRequest(), nil)
	if err == nil || errors.Is(err, model.ErrUnknown) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}

func TestOtterConcurrentInvocations(t *testing.T) {
	var builds atomic.Int32
	c := otterClientForTest(func(context.Context, string) (otterBackend, error) {
		builds.Add(1)
		return &fakeOtter{send: func(context.Context, *otter.SendRequest) (<-chan otter.StreamEvent, error) {
			return successfulOtter("answer"), nil
		}}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := otterRequest()
			req.ThreadID = int64(i + 1)
			res, err := c.Complete(context.Background(), req, nil)
			if err != nil || res.Cursor.ThreadID != req.ThreadID {
				t.Errorf("invocation %d err=%v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if builds.Load() != 10 {
		t.Fatal("backend shared")
	}
}

type fakeDeepSeek struct {
	token string
	login func(context.Context) error
}

func (f *fakeDeepSeek) SetAuthToken(s string)           { f.token = s }
func (f *fakeDeepSeek) SetAccount(string)               {}
func (f *fakeDeepSeek) SetPassword(string)              {}
func (f *fakeDeepSeek) Login(ctx context.Context) error { return f.login(ctx) }
func (f *fakeDeepSeek) Token() string                   { return "refreshed-secret" }
func TestOtterLoginContextAndCredentialsReadOnly(t *testing.T) {
	t.Setenv("OTTER_DEEPSEEK_AUTH_TOKEN", "")
	t.Setenv("OTTER_DEEPSEEK_PASSWORD", "")
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	original := []byte(`{"deepseek":{"account":"account","password":"old-secret"}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := otterconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "present")
	fake := &fakeDeepSeek{login: func(ctx context.Context) error {
		if ctx.Value(key{}) != "present" {
			t.Error("login lost context")
		}
		return nil
	}}
	if err := initDeepSeek(ctx, fake, cfg); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, original) || cfg.DeepSeek.AuthToken != "" || fake.token != "refreshed-secret" {
		t.Fatal("credentials persisted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := initDeepSeek(canceled, fake, cfg); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestOtterRefusesCredentialDumpSwitches(t *testing.T) {
	t.Setenv("OTTER_SENTINEL_DUMP", filepath.Join(t.TempDir(), "must-not-exist"))
	if _, err := buildOtterBackend(context.Background(), "chatgpt"); err == nil {
		t.Fatal("credential dumps enabled")
	}
	if _, err := os.Stat(os.Getenv("OTTER_SENTINEL_DUMP")); !os.IsNotExist(err) {
		t.Fatal("dump created")
	}
}
