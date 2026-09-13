package llm

// ChatMessage 是对话中的一条记录，采用 OpenAI 聊天协议的形式。
// 哪些字段携带数据取决于 Role：
//
//	RoleSystem, RoleUser  Content
//	RoleAssistant         Content，以及当模型请求工具时的 ToolCalls
//	RoleTool              Content 和 ToolCallID
//
// 协议约束了这些消息的顺序，运行时必须遵守：
// 携带工具调用的助手消息后面必须紧跟每个调用对应的一条 RoleTool 消息，
// 每条引用其回复的调用 id，中间不得插入其他内容。
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	CreateTime int64      `json:"create_time"`
	Id         int64      `json:"id"`
}

// 用于构建 PromptContext 的角色常量。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleTool      = "tool"
	RoleAssistant = "assistant"
)

// IsToolCallTurn 报告消息是否是请求工具的助手轮次，
// 即后续工具消息需要回复的对象。
func (m ChatMessage) IsToolCallTurn() bool {
	return m.Role == RoleAssistant && len(m.ToolCalls) > 0
}
