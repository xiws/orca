package agent

import (
	"orca/internal/llm"
	"orca/internal/tool"
	"orca/pkg/utils"
	"time"
)

type Session struct {
	Messages    []llm.ChatMessage `json:"messages"`     //messages timeline
	ProjectPath string            `json:"project_path"` // project path
	Provider    llm.ModelInfo     `json:"provider"`     // model provider
	Id          int64             `json:"id"`           // id
	TotalUsage  llm.Usage         `json:"total_usage"`  // 累计 token 消耗
}

func NewSession() *Session {
	var prompts = make([]llm.ChatMessage, 0)
	providerName := tool.Get(tool.KeyDefaultProvider)
	modelId := tool.Get(tool.KeyDefaultModel)
	var defaultProvider = llm.GetProvider(providerName, modelId)
	return &Session{
		Messages:    prompts,
		Id:          utils.GetSnowFlakeId(),
		ProjectPath: utils.GetCurrentPath(),
		Provider:    defaultProvider,
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
	chat := llm.ChatMessage{
		Id:         utils.GetSnowFlakeId(),
		Role:       role,
		Content:    msg,
		CreateTime: time.Now().Unix(),
	}
	s.Messages = append(s.Messages, chat)
}

// AddUsage accumulates the token usage from a single LLM request into the
// session-level total, so the caller can track cumulative context consumption
// against the model's context window.
func (s *Session) AddUsage(usage llm.Usage) {
	s.TotalUsage.PromptTokens += usage.PromptTokens
	s.TotalUsage.CompletionTokens += usage.CompletionTokens
	s.TotalUsage.TotalTokens += usage.TotalTokens
}
