// Package llm 提供对大语言模型的访问，将流式模型输出转换为结构化的 Go 结果。
package llm

// ToolCall 是模型产生的单个函数/工具调用。
// json 标签与 OpenAI 在助手消息中使用的线路形式匹配，
// 因此记录的轮次可以在下一次请求中原样回放给模型。
// Reasoning 仅在本地使用：是模型自己的思考，不会发回。
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Reasoning string `json:"reasoning,omitempty"` // AI 调用此工具前的推理过程
}

// Usage 报告请求的 token 消耗。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Result 是请求的流式输出完成后组装的结构化信息。
// 非 nil 的 Error 表示请求或流失败。
type Result struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
	Error        error
}

// Requester 抽象模型后端。
//
// Request 将 prompts 携带的对话发送到模型，并在到达时将增量内容流式传输到 msgs。
// 当调用方不需要实时输出时 msgs 可以为 nil；此时内容仅收集到返回的 Result 中。
// 流完成后返回完全组装的 Result。
type Requester interface {
	Request(prompts []ChatMessage, msgs chan<- string) Result
}
