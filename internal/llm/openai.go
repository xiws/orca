package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/xiws/orca/internal/tool"
	"io"
	"net/http"
	"net/http/httputil"
	"sort"
	"strings"
	"time"
)

// openAIClient 通过 server-sent events 与 OpenAI 兼容的 /chat/completions 端点通信，
// 将流式回复组装为 Result。
type openAIClient struct {
	info   ModelInfo
	client *http.Client
}

// NewOpenAIRequester 构建一个与 OpenAI 兼容的 /chat/completions 端点通信的 Requester。
// NewRequester 是入口，根据模型的 api 类型选择客户端。
func NewOpenAIRequester(info ModelInfo) Requester {
	return &openAIClient{
		info:   info,
		client: &http.Client{Timeout: 5 * time.Minute},
	}
}

// chatMessage 是 OpenAI 聊天协议中的单条消息。
//
// ToolCalls 在请求工具的助手消息上设置，ToolCallID 在回复每个调用的工具消息上设置。
// 协议要求这两半匹配：一条工具消息的 tool_call_id 如果没有对应前面助手消息的调用，
// 严格的服务端会拒绝，宽松的服务端会误读。
type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// chatToolCall 是助手消息中单个调用的线路形式：
// 调用 id、OpenAI 目前接受的固定 type 值，以及函数名和 JSON 编码的参数字符串。
type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

// chatToolFunction 是 chatToolCall 的 "function" 载荷。
type chatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatRequest 是发送到 /chat/completions 的 JSON 请求体。
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	// Tools 是模型可调用的命令的 OpenAI 函数调用声明。
	// 对于不报告工具支持的模型，此字段省略。
	Tools []Tool `json:"tools,omitempty"`
}

// streamDelta 是单个流式分块携带的增量载荷。
type streamDelta struct {
	Reasoning string `json:"reasoning"`
	Content   string `json:"content"`
	ToolCalls []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// streamChunk 是从流中解码的一个 SSE 数据载荷。
type streamChunk struct {
	Choices []struct {
		Delta        streamDelta `json:"delta"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// Request 流式输出模型内容。内容分块在到达时转发到 msgs；
// 当不需要实时输出时 msgs 可以为 nil。流完成后返回组装好的 Result。
func (c *openAIClient) Request(prompts []ChatMessage, msgs chan<- string) Result {
	var result Result

	req, err := http.NewRequest(http.MethodPost, c.endpoint(), bytes.NewReader(c.body(prompts)))
	if err != nil {
		result.Error = err
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.info.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.info.APIKey)
	}

	if tool.Get(tool.KeyDebug) == "true" {
		requestDump, err := httputil.DumpRequestOut(req, true)
		if err == nil {
			fmt.Printf("========== HTTP REQUEST ==========\n%s\n", requestDump)
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		result.Error = err
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		result.Error = fmt.Errorf("llm: %s returned status %d: %s", c.info.API, resp.StatusCode, strings.TrimSpace(string(detail)))
		return result
	}

	var content strings.Builder
	toolCalls := make(map[int]*ToolCall)
	var currentReasoning strings.Builder

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		data, ok := sseData(scanner.Text())
		if !ok {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage != nil {
			result.Usage = *chunk.Usage
		}

		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				result.FinishReason = choice.FinishReason
			}

			// 输出推理内容
			if choice.Delta.Reasoning != "" {
				currentReasoning.WriteString(choice.Delta.Reasoning)
				//fmt.Print("\033[38;5;243m" + choice.Delta.Reasoning + "\033[0m")
			}

			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				send(msgs, choice.Delta.Content)
			}

			for _, tc := range choice.Delta.ToolCalls {
				target, exists := toolCalls[tc.Index]
				if !exists {
					target = &ToolCall{}
					// 将当前累积的推理内容关联到工具调用
					if currentReasoning.Len() > 0 {
						target.Reasoning = currentReasoning.String()
						currentReasoning.Reset()
					}
					toolCalls[tc.Index] = target
				}
				if tc.ID != "" {
					target.ID = tc.ID
				}
				if tc.Function.Name != "" {
					target.Name = tc.Function.Name
				}
				target.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		result.Error = err
	}

	result.Content = content.String()
	result.ToolCalls = EnsureToolCallIDs(orderedToolCalls(toolCalls))
	return result
}

// endpoint 从 provider 的 base URL 构建 chat completions 的 URL。
func (c *openAIClient) endpoint() string {
	return strings.TrimRight(c.info.BaseURL, "/") + "/chat/completions"
}

// body 从 prompts 携带的对话中编组请求载荷，
// 将每个 ChatMessage 转换为 OpenAI 聊天消息，同时保持调用方的排序。
// 当解析出的模型支持工具时，会附加内置命令的 OpenAI 工具定义，
// 使模型可以原生调用它们，而不必遵循自由格式的 JSON 协议。
func (c *openAIClient) body(prompts []ChatMessage) []byte {
	messages := make([]chatMessage, 0, len(prompts))
	for _, prompt := range prompts {
		message, ok := wireMessage(prompt)
		if !ok {
			continue
		}
		messages = append(messages, message)
	}
	request := chatRequest{
		Model:    c.info.ModelID,
		Messages: messages,
		Stream:   true,
	}
	if c.info.SupportsTools {
		request.Tools = filterTools(c.info.AllowedTools)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		// 输入都是纯字符串，编组实际上不可能失败。
		return []byte("{}")
	}
	return payload
}

// wireMessage 将一个内部 ChatMessage 转换为线路形式。
// 仅当消息完全不携带任何数据时返回 false，正常构建的对话不会产生这种情况。
//
// 内容为空但携带工具调用的助手轮次会被保留：它是工具消息回复的那一半交换，
// 丢弃它——如同以前简单的"跳过空内容"规则——会破坏协议。
func wireMessage(prompt ChatMessage) (chatMessage, bool) {
	message := chatMessage{
		Role:       prompt.Role,
		Content:    prompt.Content,
		ToolCallID: prompt.ToolCallID,
	}
	if len(prompt.ToolCalls) > 0 {
		message.ToolCalls = make([]chatToolCall, 0, len(prompt.ToolCalls))
		for _, call := range prompt.ToolCalls {
			message.ToolCalls = append(message.ToolCalls, chatToolCall{
				ID:       call.ID,
				Type:     ToolType,
				Function: chatToolFunction{Name: call.Name, Arguments: call.Arguments},
			})
		}
	}
	if message.Content == "" && message.ToolCallID == "" && len(message.ToolCalls) == 0 {
		return chatMessage{}, false
	}
	return message, true
}

// sseData 提取 SSE "data:" 行的载荷，报告该行是否携带可用数据。
func sseData(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" {
		return "", false
	}
	return data, true
}

// EnsureToolCallIDs 为每个调用分配一个 id。协议要求回复调用的工具消息引用其 id，
// 而兼容的服务端可能在其流中省略 id；没有这个处理，回复将无法匹配，
// 对话会被拒绝。已有 id 的调用会保留它，因此重复应用是无害的。
func EnsureToolCallIDs(calls []ToolCall) []ToolCall {
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("call_%d", i)
		}
	}
	return calls
}

// orderedToolCalls 将按索引键存储的工具调用展平为流顺序。
func orderedToolCalls(toolCalls map[int]*ToolCall) []ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	ordered := make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		ordered = append(ordered, *toolCalls[index])
	}
	return ordered
}

// send 将分块转发到 msgs，忽略 nil 通道。
func send(msgs chan<- string, chunk string) {
	if msgs == nil {
		return
	}
	msgs <- chunk
}
