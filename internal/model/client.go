// Package model 定义 LLM 客户端交互所需的核心数据类型，包括请求、响应、消息、工具调用等。
package model

import (
	"context"
	"encoding/json"
	"errors"
)

// Ref 标识一个特定的模型提供方和模型名称。
type Ref struct {
	Provider string `json:"provider"` // 提供方名称
	Model    string `json:"model"`    // 模型名称
}

// Call 表示一次工具调用，包含调用 ID、工具名称和参数。
type Call struct {
	ID        string `json:"id"`        // 调用唯一标识
	Name      string `json:"name"`      // 工具名称
	Arguments string `json:"arguments"` // 参数 JSON 字符串
}

// Message 表示对话中的一条消息，支持系统/用户/助手/工具角色。
type Message struct {
	Role       string `json:"role"`                   // 角色（system/user/assistant/tool）
	Content    string `json:"content"`                // 消息内容
	ToolCalls  []Call `json:"tool_calls,omitempty"`   // 助手发起的工具调用列表
	ToolCallID string `json:"tool_call_id,omitempty"` // 工具响应关联的调用 ID
}

// Usage 记录一次模型调用的 token 使用统计。
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`       // 提示 token 数
	CompletionTokens int64 `json:"completion_tokens"`   // 补全 token 数
	TotalTokens      int64 `json:"total_tokens"`        // 总 token 数
	Estimated        bool  `json:"estimated,omitempty"` // 是否为估算值
}

// Cursor 记录模型提供方的续接状态，用于恢复对话上下文。
type Cursor struct {
	Adapter        string          `json:"adapter"`         // 适配器名称
	Version        int             `json:"version"`         // 游标版本
	Model          Ref             `json:"model"`           // 对应模型
	ThreadID       int64           `json:"thread_id"`       // 线程 ID
	ContextVersion int             `json:"context_version"` // 上下文版本
	Sequence       int             `json:"sequence"`        // 消息序列号
	Data           json.RawMessage `json:"data"`            // 适配器特定的续接数据
}

// Matches 检查游标是否与给定的适配器和请求匹配。
func (c *Cursor) Matches(adapter string, req Request) bool {
	return c == nil || (c.Adapter == adapter && c.Version == 1 && c.Model == req.Model && c.ThreadID == req.ThreadID && c.ContextVersion == req.ContextVersion && c.Sequence >= 0 && c.Sequence <= len(req.Messages))
}

// Tool 描述一个可供模型调用的工具，包含名称、描述和 JSON Schema 参数定义。
type Tool struct {
	Name        string          `json:"name"`        // 工具名称
	Description string          `json:"description"` // 工具描述
	Parameters  json.RawMessage `json:"parameters"`  // JSON Schema 参数定义
}

// Request 封装一次完整的模型请求。
type Request struct {
	Model           Ref       // 目标模型
	ThreadID        int64     // 线程 ID
	ContextVersion  int       // 上下文版本
	Messages        []Message // 对话消息列表
	Tools           []Tool    // 可用工具列表
	Cursor          *Cursor   // 续接游标
	MaxOutputTokens int       // 最大输出 token 数
}

// Response 封装模型的一次响应。
type Response struct {
	Message  Message // 助手响应消息
	Usage    Usage   // token 使用统计
	Cursor   *Cursor // 更新后的续接游标
	Complete bool    // 是否已完成（无更多流式数据）
}

// Sink 是流式响应的回调函数类型。
type Sink func(string)

// Client 定义 LLM 客户端接口。
type Client interface {
	// Complete 发送请求并获取（可能为流式的）模型响应。
	Complete(context.Context, Request, Sink) (Response, error)
}

// ErrUnknown 模型请求结果未知，对账前不可重试。
var ErrUnknown = errors.New("model request outcome unknown; reconcile before retry")

// ErrCursorMismatch 提供方游标与线程上下文不匹配。
var ErrCursorMismatch = errors.New("provider cursor does not match thread context")

// Estimate 根据消息内容粗略估算 token 数量（按 UTF-8 字节数 / 4 近似）。
func Estimate(messages []Message) int64 {
	var bytes int
	for _, m := range messages {
		bytes += len(m.Content) + 16 // 每条消息加 16 字节开销
		for _, c := range m.ToolCalls {
			bytes += len(c.Name) + len(c.Arguments) + 16
		}
	}
	// 每 4 字节约等于 1 个 token。
	return int64(bytes+3) / 4
}
