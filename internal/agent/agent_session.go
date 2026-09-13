package agent

import (
	"github.com/xiws/orca/internal/handler"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/internal/tool"
	"github.com/xiws/orca/pkg/utils"
	"time"
)

type Session struct {
	Messages    []llm.ChatMessage `json:"messages"`              // 消息时间线
	ProjectPath string            `json:"project_path"`          // 项目路径
	Provider    llm.ModelInfo     `json:"provider"`              // 模型 Provider
	Id          int64             `json:"id"`                    // 会话 ID
	TotalUsage  llm.Usage         `json:"total_usage"`           // 累计 token 消耗
	CreateTime  int64             `json:"create_time"`           // 首次创建时间
	UpdateTime  int64             `json:"update_time"`           // 最后更新时间
	OtterState  *llm.OtterState   `json:"otter_state,omitempty"` // otter 平台状态
}

func NewSession() *Session {
	var prompts = make([]llm.ChatMessage, 0)
	providerName := tool.Get(tool.KeyDefaultProvider)
	modelId := tool.Get(tool.KeyDefaultModel)
	var defaultProvider = llm.GetProvider(providerName, modelId)
	now := time.Now().Unix()
	return &Session{
		Messages:    prompts,
		Id:          utils.GetSnowFlakeId(),
		ProjectPath: utils.GetCurrentPath(),
		Provider:    defaultProvider,
		CreateTime:  now,
		UpdateTime:  now,
	}
}

func (s *Session) GetMessages() []llm.ChatMessage {
	return s.Messages
}

func (s *Session) GetProjectPath() string {
	return s.ProjectPath
}

func (s *Session) GetProvider() llm.ModelInfo {
	return s.Provider
}

func (s *Session) SetProvider(providerName, modelId string) {
	s.Provider = llm.GetProvider(providerName, modelId)
}

func (s *Session) AppendMessage(role, msg string) {
	s.Append(llm.ChatMessage{Role: role, Content: msg})
}

// AppendToolPrompt 将自由格式 JSON 命令协议的描述追加到系统上下文。
// 它让工具循环在没有原生函数调用的 Provider 上工作：这些 Provider
// 不会接收工具定义，因此命令必须通过提示词传递。
// 支持原生工具声明的 Provider 不会获得额外内容。
func (s *Session) AppendToolPrompt() {
	if s.Provider.SupportsTools {
		return
	}
	s.AppendMessage(llm.RoleSystem, handler.ToolPrompt())
}

// Append 记录一条完整构建的消息，填充调用方留下的零值身份字段，
// 确保时间线中的每条记录都可独立识别。
// 它还会更新会话的 UpdateTime 以反映最新活动。
func (s *Session) Append(message llm.ChatMessage) {
	if message.Id == 0 {
		message.Id = utils.GetSnowFlakeId()
	}
	if message.CreateTime == 0 {
		message.CreateTime = time.Now().Unix()
	}
	s.Messages = append(s.Messages, message)
	s.UpdateTime = time.Now().Unix()
}

// AppendAssistant 记录一条模型回复，包含工具调用。
//
// 该轮次必须在其工具运行之前存储：后续的工具消息通过 id 回复它，
// 协议不允许在没有对应助手消息的情况下回复。
func (s *Session) AppendAssistant(content string, calls []llm.ToolCall) {
	s.Append(llm.ChatMessage{
		Role:      llm.RoleAssistant,
		Content:   content,
		ToolCalls: calls,
	})
}

// AppendToolResult 回复单个工具调用。callID 必须是此内容所属调用的 id；
// 缺少它的工具消息会破坏后续所有请求的对话。
func (s *Session) AppendToolResult(callID, content string) {
	s.Append(llm.ChatMessage{
		Role:       llm.RoleTool,
		Content:    content,
		ToolCallID: callID,
	})
}

// LastAssistantText 返回最近一次助手轮次的内容，
// 即循环停止请求工具后模型的回答。
func (s *Session) LastAssistantText() string {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == llm.RoleAssistant {
			return s.Messages[i].Content
		}
	}
	return ""
}

// AddUsage 将单次 LLM 请求的 token 消耗累加到会话级总计，
// 以便调用方跟踪相对于模型上下文窗口的累计消耗。
func (s *Session) AddUsage(usage llm.Usage) {
	s.TotalUsage.PromptTokens += usage.PromptTokens
	s.TotalUsage.CompletionTokens += usage.CompletionTokens
	s.TotalUsage.TotalTokens += usage.TotalTokens
}

// GetOtterState 返回已保存的 otter 平台状态，
// 如果尚未记录状态则返回零值。
func (s *Session) GetOtterState() llm.OtterState {
	if s.OtterState != nil {
		return *s.OtterState
	}
	return llm.OtterState{}
}

// SetOtterState 将 otter 平台状态持久化到会话中。
func (s *Session) SetOtterState(state llm.OtterState) {
	s.OtterState = &state
}
