// Package llm provides access to large language models, converting streamed
// model output into structured Go results.
package llm

// PromptContext carries the full prompt for a single model request.
// It is composed of the system prompt, the user role prompt and the tool prompt.
type PromptContext struct {
	// SystemPrompt is the high level instruction that steers the model behaviour.
	SystemPrompt string
	// UserPrompt is the actual user request for this turn.
	UserPrompt string
	// ToolPrompt describes the available tools and their expected usage.
	ToolPrompt string
}

// ToolCall is a single function/tool invocation produced by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
	Reasoning string // AI 调用此工具前的推理过程
}

// Usage reports token consumption for a request.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Result is the structured information assembled once the streamed output of a
// request completes. A non-nil Error means the request or stream failed.
type Result struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
	Error        error
}

// Requester abstracts a model backend.
//
// Request sends the conversation carried by prompts to the model and streams
// incremental content to msgs as it arrives. msgs may be nil when the caller
// does not need the live output; in that case the content is only collected into
// the returned Result. When the stream finishes, the fully assembled Result is
// returned.
type Requester interface {
	Request(prompts []ChatMessage, msgs chan<- string) Result
}
