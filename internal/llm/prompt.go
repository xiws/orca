package llm

type ChatMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	CreateTime int64  `json:"create_time"`
	Id         int64  `json:"id"`
}

// Roles used to build a PromptContext.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleTool      = "tool"
	RoleAssistant = "assistant"
)
