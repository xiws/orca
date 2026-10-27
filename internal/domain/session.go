package domain

import "time"

// SessionID 会话的唯一标识符。
type SessionID int64

// MessageRole 消息角色类型。
type MessageRole string

const (
	// UserMessage 用户消息角色。
	UserMessage MessageRole = "user"
	// AssistantMessage 助手消息角色。
	AssistantMessage MessageRole = "assistant"
)

// Message 表示会话中的一条消息，包含内容、角色和时间戳。
type Message struct {
	ID         int64       `json:"id"`                // 消息唯一标识
	TaskID     TaskID      `json:"task_id,omitempty"` // 关联的任务 ID
	Role       MessageRole `json:"role"`              // 消息角色（user/assistant）
	Content    string      `json:"content"`           // 消息内容
	CreateTime int64       `json:"create_time"`       // 创建时间（Unix 秒）
}

// Session 表示一个对话会话，包含消息时间线和项目路径。
type Session struct {
	Version     int64     `json:"version"`      // 版本号，每次修改递增
	ID          SessionID `json:"id"`           // 会话唯一标识
	Title       string    `json:"title"`        // 会话标题
	ProjectPath string    `json:"project_path"` // 项目路径
	Messages    []Message `json:"messages"`     // 消息时间线
	CreateTime  int64     `json:"create_time"`  // 创建时间
	UpdateTime  int64     `json:"update_time"`  // 最后更新时间
}

// NewSession 创建一个新会话，自动生成唯一 ID 并设置初始时间戳。
func NewSession(projectPath string) *Session {
	now := time.Now().Unix()
	return &Session{
		ID:          SessionID(NewID()),
		ProjectPath: projectPath,
		Messages:    make([]Message, 0),
		CreateTime:  now,
		UpdateTime:  now,
	}
}

// AppendUser 向会话追加一条用户消息。
func (s *Session) AppendUser(taskID TaskID, content string) {
	s.append(taskID, UserMessage, content)
}

// AppendAssistant 向会话追加一条助手消息。
func (s *Session) AppendAssistant(taskID TaskID, content string) {
	s.append(taskID, AssistantMessage, content)
}

// append 是追加消息的内部通用方法，自动设置消息 ID 和更新时间。
func (s *Session) append(taskID TaskID, role MessageRole, content string) {
	now := time.Now().Unix()
	s.Messages = append(s.Messages, Message{
		ID:         NewID(),
		TaskID:     taskID,
		Role:       role,
		Content:    content,
		CreateTime: now,
	})
	s.UpdateTime = now
}
