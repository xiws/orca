// Package providers 适配模型请求，不保留对话状态。
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

// Connection 是单次调用解析出的连接信息，绝不应被持久化。
// BaseURL 可能包含凭据，因此整个连接结构体隐藏 JSON 序列化。
type Connection struct {
	API           string `json:"-"`
	BaseURL       string `json:"-"`
	APIKey        string `json:"-"`
	SupportsTools bool   `json:"-"`
	ContextWindow int    `json:"-"`
}

// String 返回 Connection 的安全字符串表示，不暴露凭据。
func (Connection) String() string { return "Connection{runtime-only}" }

// GoString 返回 Connection 的安全详细字符串表示，不暴露凭据。
func (Connection) GoString() string { return "Connection{runtime-only}" }

// SecretResolver 根据模型引用解析出连接信息（含凭据）。
type SecretResolver interface {
	Resolve(context.Context, model.Ref) (Connection, error)
}

// Client 是通过后端与 LLM provider 通信的客户端。
type Client struct {
	resolver   SecretResolver
	http       *http.Client
	buildOtter otterBackendBuilder
}

// 请求、登录超时及响应大小限制的常量。
const requestTimeout = 5 * time.Minute
const loginTimeout = 30 * time.Second
const defaultMaxOutput = 4096
const maxResponseBytes = 16 << 20

// sharedHTTP 是全局共享的 HTTP 客户端，配置了连接池和凭据保护。
var sharedHTTP = &http.Client{
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 100, MaxIdleConnsPerHost: 10, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: time.Minute, ForceAttemptHTTP2: true},
	Timeout:   requestTimeout,
	// 不跟随重定向，防止凭据泄露或重放模型提交。
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// New 使用给定的密钥解析器创建一个新的 Client。
func New(resolver SecretResolver) *Client {
	return &Client{resolver: resolver, http: sharedHTTP, buildOtter: buildOtterBackend}
}

// 编译时检查：确保 Client 实现了 model.Client 接口。
var _ model.Client = (*Client)(nil)

// Complete 将请求发送到模型后端并流式返回结果。
// 根据连接的 API 类型选择 OpenAI 兼容或 otter 平台后端。
func (c *Client) Complete(ctx context.Context, req model.Request, sink model.Sink) (model.Response, error) {
	// 提前检查上下文是否已取消
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
	// 未指定输出 token 上限时使用默认值
	if req.MaxOutputTokens == 0 {
		req.MaxOutputTokens = defaultMaxOutput
	}
	if req.MaxOutputTokens > maxResponseBytes/4 {
		return model.Response{}, errors.New("providers: output limit too large")
	}
	if err := model.ValidateTools(req.Tools); err != nil {
		return model.Response{}, err
	}
	// 解析模型引用对应的连接信息（含凭据）
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
	// 估算输入 token 数量：包括历史消息、工具声明和输出预留。
	// 绝不静默丢弃消息来适应上下文窗口。
	toolJSON, _ := json.Marshal(req.Tools)
	estimate := model.Estimate(req.Messages) + int64((len(toolJSON)+3)/4)
	// 不支持原生工具调用的模型，将工具定义注入系统提示
	if !conn.SupportsTools || conn.API == "otter" {
		prompt, err := model.ToolPrompt(req.Tools)
		if err != nil {
			return model.Response{}, err
		}
		estimate = model.Estimate(req.Messages) + model.Estimate([]model.Message{{Role: "system", Content: prompt}})
	}
	// 检查估算的输入+输出是否超出上下文窗口
	if conn.ContextWindow > 0 && estimate+int64(req.MaxOutputTokens) > int64(conn.ContextWindow) {
		return model.Response{}, fmt.Errorf("providers: context limit exceeded (estimated input %d + output %d > %d)", estimate, req.MaxOutputTokens, conn.ContextWindow)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	// 根据 API 类型分发到对应的后端
	switch conn.API {
	case "openai", "openai-completions", "openai-chat-completions":
		// OpenAI 不支持游标续传
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

// remoteError 构造安全的错误信息：远端错误可能包含 token、请求 URL
// 或回显的 Authorization 头，因此仅保留安全的本地诊断和上下文取消信息。
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

// finishMessage 完成助手消息：根据是否支持原生工具调用，
// 选择解析原生工具调用或从文本中提取工具调用。
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
