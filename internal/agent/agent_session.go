package agent

import (
	"github.com/xiws/orca/internal/handler"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/internal/tool"
	"github.com/xiws/orca/pkg/utils"
	"time"
)

type Session struct {
	Messages    []llm.ChatMessage `json:"messages"`              //messages timeline
	ProjectPath string            `json:"project_path"`          // project path
	Provider    llm.ModelInfo     `json:"provider"`              // model provider
	Id          int64             `json:"id"`                    // id
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

// AppendToolPrompt appends the description of the free-form JSON command
// protocol to the system context. It is what keeps the tool loop working on
// providers without native function calling: they never receive tool
// definitions, so the commands have to travel in the prompt instead. A
// provider the tools can be declared to natively gets nothing extra.
func (s *Session) AppendToolPrompt() {
	if s.Provider.SupportsTools {
		return
	}
	s.AppendMessage(llm.RoleSystem, handler.ToolPrompt())
}

// Append records a fully built message, filling in the identity fields a caller
// left zero so every entry of the timeline is identifiable on its own.
// It also updates the session's UpdateTime to reflect the latest activity.
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

// AppendAssistant records one model reply, tool calls included.
//
// The turn has to be stored before its tools run: the tool messages that follow
// answer it by id, and the protocol does not allow that answer without the
// assistant message it belongs to.
func (s *Session) AppendAssistant(content string, calls []llm.ToolCall) {
	s.Append(llm.ChatMessage{
		Role:      llm.RoleAssistant,
		Content:   content,
		ToolCalls: calls,
	})
}

// AppendToolResult answers a single tool call. callID must be the id of the call
// this content belongs to; a tool message without one breaks the conversation
// for every following request.
func (s *Session) AppendToolResult(callID, content string) {
	s.Append(llm.ChatMessage{
		Role:       llm.RoleTool,
		Content:    content,
		ToolCallID: callID,
	})
}

// LastAssistantText returns the content of the most recent assistant turn, which
// is the model's answer once the loop stops asking for tools.
func (s *Session) LastAssistantText() string {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == llm.RoleAssistant {
			return s.Messages[i].Content
		}
	}
	return ""
}

// AddUsage accumulates the token usage from a single LLM request into the
// session-level total, so the caller can track cumulative context consumption
// against the model's context window.
func (s *Session) AddUsage(usage llm.Usage) {
	s.TotalUsage.PromptTokens += usage.PromptTokens
	s.TotalUsage.CompletionTokens += usage.CompletionTokens
	s.TotalUsage.TotalTokens += usage.TotalTokens
}

// GetOtterState returns the saved otter platform state, or a zero value
// when no state has been recorded yet.
func (s *Session) GetOtterState() llm.OtterState {
	if s.OtterState != nil {
		return *s.OtterState
	}
	return llm.OtterState{}
}

// SetOtterState persists the otter platform state into the session.
func (s *Session) SetOtterState(state llm.OtterState) {
	s.OtterState = &state
}
