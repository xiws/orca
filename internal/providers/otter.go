package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xiws/orca/internal/model"
	otter "github.com/xiws/otter/pkg/provider"
)

// 本文件实现通过 otter 平台 provider 访问 ChatGPT、Gemini 和 DeepSeek。

// otterBackend 是 otter provider 发送流式请求的接口。
type otterBackend interface {
	SendStream(context.Context, *otter.SendRequest) (<-chan otter.StreamEvent, error)
}

// otterBackendBuilder 根据平台名称构建 otter 后端。
type otterBackendBuilder func(context.Context, string) (otterBackend, error)

// otterRemoteSession 是需要在服务端创建会话的平台接口（如 DeepSeek）。
type otterRemoteSession interface {
	CreateRemoteSession(context.Context) (string, error)
}

// otterState 捕获 otter 平台侧的对话状态。
// 没有 requester 或 delivered 计数器保存在 Client 上；
// 游标将远程对话绑定到已确认的历史前缀，包括与该游标一起返回的助手消息。
type otterState struct {
	ChatSessionID      string            `json:"chat_session_id,omitempty"`
	ParentMessageID    int               `json:"parent_message_id,omitempty"`
	ParentMessageIDStr string            `json:"parent_message_id_str,omitempty"`
	RemoteMetadata     map[string]string `json:"remote_metadata,omitempty"`
	Delivered          int               `json:"delivered"`
	Prefix             string            `json:"prefix"`
}

// historyDigest 计算消息历史的 SHA-256 摘要，用于游标校验。
func historyDigest(messages []model.Message) string {
	data, _ := json.Marshal(messages)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// restoreOtter 从请求的游标中恢复 otter 平台状态。
// 校验游标类型、序列号、历史摘要等一致性，不匹配则返回 ErrCursorMismatch。
func restoreOtter(req model.Request) (otterState, error) {
	var state otterState
	cur := req.Cursor
	if cur == nil {
		return state, nil
	}
	if !cur.Matches("otter", req) || cur.Sequence <= 0 || model.StrictJSON(cur.Data) != nil {
		return state, model.ErrCursorMismatch
	}
	dec := json.NewDecoder(bytes.NewReader(cur.Data))
	dec.DisallowUnknownFields()
	if dec.Decode(&state) != nil || state.Delivered != cur.Sequence || state.Prefix != historyDigest(req.Messages[:cur.Sequence]) || state.ParentMessageID < 0 {
		return otterState{}, model.ErrCursorMismatch
	}
	if state.ChatSessionID == "" || (state.ParentMessageID == 0 && state.ParentMessageIDStr == "") {
		return otterState{}, model.ErrCursorMismatch
	}
	for key, value := range state.RemoteMetadata {
		if req.Model.Model != "gemini" || key != "gemini_metadata" || !validGeminiMetadata(value) {
			return otterState{}, model.ErrCursorMismatch
		}
	}
	if req.Model.Model == "gemini" && state.RemoteMetadata["gemini_metadata"] == "" {
		return otterState{}, model.ErrCursorMismatch
	}
	return state, nil
}

// validGeminiMetadata 校验 Gemini 平台的续传元数据是否为合法的 JSON 数组。
func validGeminiMetadata(raw string) bool {
	var data []json.RawMessage
	return model.StrictJSON([]byte(raw)) == nil && json.Unmarshal([]byte(raw), &data) == nil && len(data) > 0
}

// pendingOtter 将请求中尚未发送的消息组装为平台可接受的提示文本。
// delivered 标记已确认发送的历史条数，之后的消息视为待发送。
func pendingOtter(req model.Request, delivered int) (string, error) {
	var prompt strings.Builder
	for _, msg := range req.Messages[delivered:] {
		if msg.Content == "" && len(msg.ToolCalls) == 0 {
			continue
		}
		if prompt.Len() > 0 {
			prompt.WriteString("\n\n")
		}
		switch msg.Role {
		case "system":
			prompt.WriteString("System context:\n")
		case "user":
			prompt.WriteString("User:\n")
		case "assistant":
			prompt.WriteString("Previous assistant response:\n")
		case "tool":
			prompt.WriteString("Tool result for " + msg.ToolCallID + ":\n")
		default:
			return "", errors.New("otter: unsupported message role")
		}
		prompt.WriteString(msg.Content)
		if len(msg.ToolCalls) > 0 {
			raw, _ := json.Marshal(msg.ToolCalls)
			prompt.WriteString("\n<tool_calls>" + string(raw) + "</tool_calls>")
		}
	}
	if prompt.Len() == 0 {
		return "", errors.New("otter: no undelivered messages")
	}
	tools, err := model.ToolPrompt(req.Tools)
	if err != nil {
		return "", err
	}
	if tools != "" {
		return tools + "\n\n" + prompt.String(), nil
	}
	return prompt.String(), nil
}

// completeOtter 通过 otter 平台 provider 完成请求，支持游标续传。
// 流式接收平台事件，完成后构建游标以供下次续传使用。
func (c *Client) completeOtter(ctx context.Context, req model.Request, sink model.Sink) (model.Response, error) {
	state, err := restoreOtter(req)
	if err != nil {
		return model.Response{}, err
	}
	prompt, err := pendingOtter(req, state.Delivered)
	if err != nil {
		return model.Response{}, err
	}
	loginCtx, cancelLogin := context.WithTimeout(ctx, loginTimeout)
	// 构建后端（可能需要登录）
	backend, err := c.buildOtter(loginCtx, req.Model.Model)
	cancelLogin()
	if err != nil {
		// 构建/登录阶段未提交模型轮次，不将已知设置错误转为 ErrUnknown，
		// 也不暴露 provider 凭据错误文本。
		if ctx.Err() != nil {
			return model.Response{}, ctx.Err()
		}
		return model.Response{}, errors.New("otter: backend construction or authentication failed")
	}
	if backend == nil {
		return model.Response{}, errors.New("otter: backend missing")
	}
	if ctx.Err() != nil {
		return model.Response{}, ctx.Err()
	}
	// 为需要服务端创建会话的平台（如 DeepSeek）初始化远程会话
	if remote, ok := backend.(otterRemoteSession); ok && state.ChatSessionID == "" {
		createCtx, cancel := context.WithTimeout(ctx, loginTimeout)
		state.ChatSessionID, err = remote.CreateRemoteSession(createCtx)
		canceled := createCtx.Err()
		cancel()
		if err != nil || canceled != nil || state.ChatSessionID == "" {
			return model.Response{}, remoteError(ctx, "otter: remote session creation failed", false)
		}
	}
	if ctx.Err() != nil {
		return model.Response{}, ctx.Err()
	}
	// 发送流式请求并消费事件
	events, err := backend.SendStream(ctx, &otter.SendRequest{Prompt: prompt, ChatSessionID: state.ChatSessionID, ParentMessageID: state.ParentMessageID, ParentMessageIDStr: state.ParentMessageIDStr, RemoteMetadata: cloneMetadata(state.RemoteMetadata), Timeout: requestTimeout})
	if err != nil || events == nil {
		return model.Response{}, remoteError(ctx, "otter: submission failed", true)
	}
	var text strings.Builder
	usage := model.Usage{}
	done := false
	// 事件消费主循环：从 events 通道读取流事件，处理文本、元数据和完成信号
	for {
		select {
		case <-ctx.Done():
			return model.Response{}, remoteError(ctx, "otter: stream canceled", true)
		case event, ok := <-events:
			if !ok {
				// 流关闭：校验完成状态，构建游标用于下次续传
				if !done || ctx.Err() != nil {
					return model.Response{}, remoteError(ctx, "otter: incomplete stream", true)
				}
				message, err := finishMessage(req, model.Message{Content: text.String()}, false)
				if err != nil {
					return model.Response{}, fmt.Errorf("%w: %w", model.ErrUnknown, err)
				}
				if state.ChatSessionID == "" || state.ParentMessageID == 0 && state.ParentMessageIDStr == "" {
					return model.Response{}, remoteError(ctx, "otter: missing continuation identifiers", true)
				}
				if req.Model.Model == "gemini" && !validGeminiMetadata(state.RemoteMetadata["gemini_metadata"]) {
					return model.Response{}, remoteError(ctx, "otter: missing continuation metadata", true)
				}
				// 将助手回复追加到历史，计算摘要并序列化状态到游标
				history := make([]model.Message, 0, len(req.Messages)+1)
				history = append(history, req.Messages...)
				history = append(history, message)
				state.Delivered = len(history)
				state.Prefix = historyDigest(history)
				data, _ := json.Marshal(state)
				if ctx.Err() != nil {
					return model.Response{}, remoteError(ctx, "otter: completion canceled", true)
				}
				cursor := &model.Cursor{Adapter: "otter", Version: 1, Model: req.Model, ThreadID: req.ThreadID, ContextVersion: req.ContextVersion, Sequence: state.Delivered, Data: data}
				return model.Response{Message: message, Cursor: cursor, Usage: usage, Complete: true}, nil
			}
			if done {
				return model.Response{}, remoteError(ctx, "otter: event after done", true)
			}
			if event.Err != nil {
				return model.Response{}, remoteError(ctx, "otter: stream failed", true)
			}
			switch event.Type {
			case "text":
				if text.Len()+len(event.Content) > maxResponseBytes || text.Len()+len(event.Content) > req.MaxOutputTokens*16 {
					return model.Response{}, remoteError(ctx, "otter: output limit exceeded", true)
				}
				text.WriteString(event.Content)
				if sink != nil && event.Content != "" {
					sink(event.Content)
				}
			case "meta":
			case "done":
				done = true
			default:
				return model.Response{}, remoteError(ctx, "otter: invalid stream event", true)
			}
			if event.RemoteConversationID != "" {
				state.ChatSessionID = event.RemoteConversationID
			}
			if event.ResponseMessageID != 0 {
				state.ParentMessageID = event.ResponseMessageID
			}
			if event.ResponseMessageIDStr != "" {
				state.ParentMessageIDStr = event.ResponseMessageIDStr
			}
			if event.TokenUsage != 0 {
				usage.TotalTokens = int64(event.TokenUsage)
			}
			// 仅允许已记录的续传元数据写入持久化游标
			if req.Model.Model == "gemini" {
				if value := event.RemoteMetadata["gemini_metadata"]; value != "" {
					if !validGeminiMetadata(value) {
						return model.Response{}, remoteError(ctx, "otter: invalid continuation metadata", true)
					}
					state.RemoteMetadata = map[string]string{"gemini_metadata": value}
				}
			}
		}
	}
}

// cloneMetadata 深拷贝元数据 map，防止并发修改。
func cloneMetadata(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
