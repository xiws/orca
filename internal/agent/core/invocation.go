// Package core 提供 Agent 执行的核心基础设施，包括调用上下文管理、运行时和提示词组装。
package core

import (
	"maps"
	"slices"
	"time"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/utils"
)

// Invocation 表示一次 LLM 调用的上下文，包含消息历史、token 用量和子调用链。
// 它是 Agent 执行过程中的核心数据结构，支持树形嵌套（父子调用）。
type Invocation struct {
	// ID 调用的唯一标识（雪花算法生成）。
	ID int64 `json:"id"`
	// TaskID 所属任务的标识。
	TaskID domain.TaskID `json:"task_id"`
	// ProjectPath 项目根目录路径。
	ProjectPath string `json:"project_path"`
	// Provider 使用的 LLM 模型信息（不序列化）。
	Provider llm.ModelInfo `json:"-"`
	// Messages 对话消息历史。
	Messages []llm.ChatMessage `json:"messages"`
	// TotalUsage 本次调用累计的 token 用量。
	TotalUsage llm.Usage `json:"total_usage"`
	// OtterState LLM Provider 的状态（如会话 ID），用于恢复断点续传。
	OtterState *llm.OtterState `json:"otter_state,omitempty"`
	// Children 子调用列表，用于支持任务分解等场景。
	Children []*Invocation `json:"children,omitempty"`
}

// NewInvocation 创建一个新的调用上下文。
// 会克隆 AllowedTools 切片以避免外部修改影响调用。
func NewInvocation(taskID domain.TaskID, projectPath string, provider llm.ModelInfo) *Invocation {
	// 克隆工具白名单，防止后续修改影响原始 Provider 配置
	provider.AllowedTools = slices.Clone(provider.AllowedTools)
	return &Invocation{
		ID:          utils.GetSnowFlakeId(),
		TaskID:      taskID,
		ProjectPath: projectPath,
		Provider:    provider,
		Messages:    make([]llm.ChatMessage, 0),
	}
}

// NewChild 创建一个子调用，继承父调用的任务、项目和模型配置。
// 子调用会被追加到当前调用的 Children 列表中。
func (i *Invocation) NewChild() *Invocation {
	child := NewInvocation(i.TaskID, i.ProjectPath, i.Provider)
	i.Children = append(i.Children, child)
	return child
}

// AppendMessage 以角色和内容的方式快速追加一条消息。
func (i *Invocation) AppendMessage(role, content string) {
	i.Append(llm.ChatMessage{Role: role, Content: content})
}

// Append 追加一条消息到调用历史。
// 自动为缺少 ID 或时间戳的消息补全。
func (i *Invocation) Append(message llm.ChatMessage) {
	if message.Id == 0 {
		message.Id = utils.GetSnowFlakeId()
	}
	if message.CreateTime == 0 {
		message.CreateTime = time.Now().Unix()
	}
	// 克隆 ToolCalls 避免外部修改
	message.ToolCalls = slices.Clone(message.ToolCalls)
	i.Messages = append(i.Messages, message)
}

// AppendAssistant 追加一条 assistant 角色的消息，可携带工具调用。
func (i *Invocation) AppendAssistant(content string, calls []llm.ToolCall) {
	i.Append(llm.ChatMessage{Role: llm.RoleAssistant, Content: content, ToolCalls: calls})
}

// AppendToolResult 追加一条工具执行结果消息。
func (i *Invocation) AppendToolResult(callID, content string) {
	i.Append(llm.ChatMessage{Role: llm.RoleTool, Content: content, ToolCallID: callID})
}

// AddUsage 累加 token 用量统计。
func (i *Invocation) AddUsage(usage llm.Usage) {
	i.TotalUsage.PromptTokens += usage.PromptTokens
	i.TotalUsage.CompletionTokens += usage.CompletionTokens
	i.TotalUsage.TotalTokens += usage.TotalTokens
}

// GetOtterState 返回当前 Provider 状态的副本（深拷贝 RemoteMetadata）。
func (i *Invocation) GetOtterState() llm.OtterState {
	if i.OtterState == nil {
		return llm.OtterState{}
	}
	state := *i.OtterState
	// 克隆元数据，防止外部修改影响内部状态
	state.RemoteMetadata = maps.Clone(state.RemoteMetadata)
	return state
}

// SetOtterState 设置 Provider 状态（深拷贝 RemoteMetadata）。
func (i *Invocation) SetOtterState(state llm.OtterState) {
	state.RemoteMetadata = maps.Clone(state.RemoteMetadata)
	i.OtterState = &state
}
