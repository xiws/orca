package llm

// ChatMessage is one entry of a conversation, in the shape of the OpenAI chat
// protocol. Which fields carry data depends on Role:
//
//	RoleSystem, RoleUser  Content
//	RoleAssistant         Content and, when the model asked for tools, ToolCalls
//	RoleTool              Content and ToolCallID
//
// The protocol constrains the order of these messages and the runtime has to
// honour it: an assistant message carrying tool calls must be followed by one
// RoleTool message per call, each quoting the id of the call it answers, and
// nothing may be inserted between them.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	CreateTime int64      `json:"create_time"`
	Id         int64      `json:"id"`
}

// Roles used to build a PromptContext.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleTool      = "tool"
	RoleAssistant = "assistant"
)

// IsToolCallTurn reports whether the message is an assistant turn that asks for
// tools, which is what the following tool messages must answer.
func (m ChatMessage) IsToolCallTurn() bool {
	return m.Role == RoleAssistant && len(m.ToolCalls) > 0
}
