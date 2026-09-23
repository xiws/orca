// Package providers adapts model requests without retaining conversation state.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/xiws/orca/internal/model"
)

// Connection is resolved for one invocation. It must never be persisted.
// BaseURL can also contain credentials, so the entire connection is JSON-hidden.
type Connection struct {
	API           string `json:"-"`
	BaseURL       string `json:"-"`
	APIKey        string `json:"-"`
	SupportsTools bool   `json:"-"`
	ContextWindow int    `json:"-"`
}

func (Connection) String() string   { return "Connection{runtime-only}" }
func (Connection) GoString() string { return "Connection{runtime-only}" }

type SecretResolver interface {
	Resolve(context.Context, model.Ref) (Connection, error)
}

type Client struct {
	resolver   SecretResolver
	http       *http.Client
	buildOtter otterBackendBuilder
}

const requestTimeout = 5 * time.Minute
const loginTimeout = 30 * time.Second
const defaultMaxOutput = 4096
const maxResponseBytes = 16 << 20

var sharedHTTP = &http.Client{
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 100, MaxIdleConnsPerHost: 10, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: time.Minute, ForceAttemptHTTP2: true},
	Timeout:   requestTimeout,
	// Never forward credentials or replay model submissions across redirects.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func New(resolver SecretResolver) *Client {
	return &Client{resolver: resolver, http: sharedHTTP, buildOtter: buildOtterBackend}
}

var _ model.Client = (*Client)(nil)

func (c *Client) Complete(ctx context.Context, req model.Request, sink model.Sink) (model.Response, error) {
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	if c == nil || c.resolver == nil {
		return model.Response{}, errors.New("providers: secret resolver required")
	}
	if req.Model.Provider == "" || req.Model.Model == "" {
		return model.Response{}, errors.New("providers: model reference required")
	}
	if req.MaxOutputTokens < 0 {
		return model.Response{}, errors.New("providers: negative output limit")
	}
	if req.MaxOutputTokens == 0 {
		req.MaxOutputTokens = defaultMaxOutput
	}
	if req.MaxOutputTokens > maxResponseBytes/4 {
		return model.Response{}, errors.New("providers: output limit too large")
	}
	if err := model.ValidateTools(req.Tools); err != nil {
		return model.Response{}, err
	}
	conn, err := c.resolver.Resolve(ctx, req.Model)
	if err != nil {
		return model.Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	if conn.ContextWindow < 0 {
		return model.Response{}, errors.New("providers: negative context window")
	}
	// Account for all history, including tool call/result pairs, plus declarations
	// and output reserve. Never silently drop a message to fit the window.
	toolJSON, _ := json.Marshal(req.Tools)
	estimate := model.Estimate(req.Messages) + int64((len(toolJSON)+3)/4)
	if !conn.SupportsTools || conn.API == "otter" {
		prompt, err := model.ToolPrompt(req.Tools)
		if err != nil {
			return model.Response{}, err
		}
		estimate = model.Estimate(req.Messages) + model.Estimate([]model.Message{{Role: "system", Content: prompt}})
	}
	if conn.ContextWindow > 0 && estimate+int64(req.MaxOutputTokens) > int64(conn.ContextWindow) {
		return model.Response{}, fmt.Errorf("providers: context limit exceeded (estimated input %d + output %d > %d)", estimate, req.MaxOutputTokens, conn.ContextWindow)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	switch conn.API {
	case "openai", "openai-completions", "openai-chat-completions":
		if req.Cursor != nil {
			return model.Response{}, model.ErrCursorMismatch
		}
		return c.completeOpenAI(ctx, conn, req, sink)
	case "otter":
		return c.completeOtter(ctx, req, sink)
	default:
		return model.Response{}, errors.New("providers: unsupported model API")
	}
}

// Remote errors may contain tokens, request URLs, or echoed Authorization
// headers. Retain only safe local diagnoses and context cancellation identity.
func remoteError(ctx context.Context, stage string, unknown bool) error {
	err := errors.New(stage)
	if ctx.Err() != nil {
		err = fmt.Errorf("%s: %w", stage, ctx.Err())
	}
	if unknown {
		return fmt.Errorf("%w: %w", model.ErrUnknown, err)
	}
	return err
}

func finishMessage(req model.Request, message model.Message, native bool) (model.Message, error) {
	var calls []model.Call
	var err error
	if native {
		calls, err = model.NormalizeCalls(message.ToolCalls, req.Tools)
	} else {
		if len(message.ToolCalls) != 0 {
			return model.Message{}, model.ErrToolProtocol
		}
		calls, err = model.ParseTextCalls(message.Content, req.Tools)
	}
	if err != nil {
		return model.Message{}, err
	}
	message.Role = "assistant"
	message.ToolCalls = calls
	return message, nil
}
