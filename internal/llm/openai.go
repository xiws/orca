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

// openAIClient talks to an OpenAI-compatible /chat/completions endpoint using
// server-sent events, assembling the streamed reply into a Result.
type openAIClient struct {
	info   ModelInfo
	client *http.Client
}

// NewOpenAIRequester builds a Requester for the resolved model based on its api type.
// It falls back to the OpenAI-compatible client for unknown api values.
func NewOpenAIRequester(info ModelInfo) Requester {
	return &openAIClient{
		info:   info,
		client: &http.Client{Timeout: 5 * time.Minute},
	}
}

// chatMessage is a single message in the OpenAI chat protocol.
//
// ToolCalls is set on an assistant message that asks for tools, ToolCallID on
// the tool messages that answer one call each. The protocol requires those two
// halves to match: a tool message whose tool_call_id names no call of the
// assistant message right before it is rejected by a strict server and
// misread by a lenient one.
type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// chatToolCall is the wire form of one invocation inside an assistant message:
// the call id, the constant type OpenAI accepts today, and the function name
// together with its arguments as a JSON encoded string.
type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

// chatToolFunction is the "function" payload of a chatToolCall.
type chatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatRequest is the JSON body sent to /chat/completions.
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	// Tools is the OpenAI function-calling declaration of the commands the
	// model may invoke. It is omitted for models that report no tool support.
	Tools []Tool `json:"tools,omitempty"`
}

// streamDelta is the incremental payload carried by one streamed chunk.
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

// streamChunk is one SSE data payload decoded from the stream.
type streamChunk struct {
	Choices []struct {
		Delta        streamDelta `json:"delta"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// Request streams the model output. Content chunks are forwarded to msgs as
// they arrive; msgs may be nil when live output is not needed. Once the stream
// completes, the assembled Result is returned.
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

// endpoint builds the chat completions URL from the provider base URL.
func (c *openAIClient) endpoint() string {
	return strings.TrimRight(c.info.BaseURL, "/") + "/chat/completions"
}

// body marshals the request payload from the conversation carried by prompts,
// translating each ChatMessage into an OpenAI chat message while keeping the
// caller's ordering. When the resolved model supports tools, the OpenAI tool
// definitions of the built-in commands are attached, so the model can call them
// natively instead of having to follow a free-form JSON protocol.
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
		request.Tools = Tools()
	}
	payload, err := json.Marshal(request)
	if err != nil {
		// The inputs are plain strings, so marshalling cannot realistically fail.
		return []byte("{}")
	}
	return payload
}

// wireMessage translates one internal ChatMessage into its wire form. It
// reports false only for a message that carries nothing at all, which no
// correctly built conversation produces.
//
// An assistant turn whose content is empty but which carries tool calls is
// kept: it is the half of the exchange the tool messages answer, and dropping
// it — as a plain "skip empty content" rule used to — severs the protocol.
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

// sseData extracts the payload of an SSE "data:" line, reporting whether the
// line carries usable data.
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

// EnsureToolCallIDs gives every call an id. The protocol requires the tool
// message that answers a call to quote it, and a compatible server may omit
// ids from its stream; without this the answers could not be matched and the
// conversation would be rejected. A call that already carries an id keeps it,
// so applying this twice is harmless.
func EnsureToolCallIDs(calls []ToolCall) []ToolCall {
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("call_%d", i)
		}
	}
	return calls
}

// orderedToolCalls flattens the index-keyed tool calls into stream order.
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

// send forwards a chunk to msgs, ignoring a nil channel.
func send(msgs chan<- string, chunk string) {
	if msgs == nil {
		return
	}
	msgs <- chunk
}
