package domain

import (
	"time"

	"github.com/xiws/orca/pkg/utils"
)

type SessionID int64

type MessageRole string

const (
	UserMessage      MessageRole = "user"
	AssistantMessage MessageRole = "assistant"
)

type Message struct {
	ID         int64       `json:"id"`
	TaskID     TaskID      `json:"task_id,omitempty"`
	Role       MessageRole `json:"role"`
	Content    string      `json:"content"`
	CreateTime int64       `json:"create_time"`
}

type Session struct {
	ID          SessionID `json:"id"`
	Title       string    `json:"title"`
	ProjectPath string    `json:"project_path"`
	Messages    []Message `json:"messages"`
	CreateTime  int64     `json:"create_time"`
	UpdateTime  int64     `json:"update_time"`
}

func NewSession(projectPath string) *Session {
	now := time.Now().Unix()
	return &Session{
		ID:          SessionID(utils.GetSnowFlakeId()),
		ProjectPath: projectPath,
		Messages:    make([]Message, 0),
		CreateTime:  now,
		UpdateTime:  now,
	}
}

func (s *Session) AppendUser(taskID TaskID, content string) {
	s.append(taskID, UserMessage, content)
}

func (s *Session) AppendAssistant(taskID TaskID, content string) {
	s.append(taskID, AssistantMessage, content)
}

func (s *Session) append(taskID TaskID, role MessageRole, content string) {
	now := time.Now().Unix()
	s.Messages = append(s.Messages, Message{
		ID:         utils.GetSnowFlakeId(),
		TaskID:     taskID,
		Role:       role,
		Content:    content,
		CreateTime: now,
	})
	s.UpdateTime = now
}
