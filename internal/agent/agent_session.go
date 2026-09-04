package agent

import (
	"orca/internal/llm"
	"orca/pkg/utils"
	"time"
)

type Session struct {
	Messages    []llm.ChatMessage `json:"messages"`     //messages timeline
	ProjectPath string            `json:"project_path"` // project path
	Provider    llm.ModelInfo     `json:"provider"`     // model provider
	Id          int64             `json:"id"`           // id
}

func NewSession() *Session {
	var prompts = make([]llm.ChatMessage, 0)
	providerName := Get(KeyDefaultProvider)
	modelId := Get(KeyDefaultModel)
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
