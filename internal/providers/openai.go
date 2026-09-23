package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/xiws/orca/internal/model"
)

type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type wireCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}
type wireMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type wireTool struct {
	Type     string     `json:"type"`
	Function model.Tool `json:"function"`
}
type wireRequest struct {
	Model         string        `json:"model"`
	Messages      []wireMessage `json:"messages"`
	Tools         []wireTool    `json:"tools,omitempty"`
	Stream        bool          `json:"stream"`
	MaxTokens     int           `json:"max_tokens"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

func openAIBody(conn Connection, req model.Request) ([]byte, error) {
	body := wireRequest{Model: req.Model.Model, Stream: true, MaxTokens: req.MaxOutputTokens}
	body.StreamOptions.IncludeUsage = true
	if !conn.SupportsTools {
		prompt, err := model.ToolPrompt(req.Tools)
		if err != nil {
			return nil, err
		}
		if prompt != "" {
			body.Messages = append(body.Messages, wireMessage{Role: "system", Content: prompt})
		}
	} else {
		for _, tool := range req.Tools {
			body.Tools = append(body.Tools, wireTool{Type: "function", Function: tool})
		}
	}
	for _, msg := range req.Messages {
		m := wireMessage{Role: msg.Role, Content: msg.Content, ToolCallID: msg.ToolCallID}
		if conn.SupportsTools {
			for _, call := range msg.ToolCalls {
				m.ToolCalls = append(m.ToolCalls, wireCall{ID: call.ID, Type: "function", Function: wireFunction{Name: call.Name, Arguments: call.Arguments}})
			}
		} else {
			if len(msg.ToolCalls) > 0 {
				raw, _ := json.Marshal(msg.ToolCalls)
				m.Content += "\n<tool_calls>" + string(raw) + "</tool_calls>"
			}
			if m.Role == "tool" {
				m.Role = "user"
				m.Content = "Tool result for " + m.ToolCallID + ":\n" + m.Content
			}
			m.ToolCallID = ""
		}
		body.Messages = append(body.Messages, m)
	}
	return json.Marshal(body)
}

func (c *Client) completeOpenAI(ctx context.Context, conn Connection, req model.Request, sink model.Sink) (model.Response, error) {
	endpoint, err := url.Parse(conn.BaseURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return model.Response{}, errors.New("openai: invalid base URL (credentials and query parameters are not allowed)")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/chat/completions"
	body, err := openAIBody(conn, req)
	if err != nil {
		return model.Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return model.Response{}, errors.New("openai: invalid request")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if conn.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+conn.APIKey)
	}
	if ctx.Err() != nil {
		return model.Response{}, ctx.Err()
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return model.Response{}, remoteError(ctx, "openai: transport failed", true)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// A rejection is known; timeouts and server failures may follow a submission.
		unknown := resp.StatusCode == 408 || resp.StatusCode >= 500 || resp.StatusCode >= 200 && resp.StatusCode < 300
		return model.Response{}, remoteError(ctx, fmt.Sprintf("openai: HTTP status %d", resp.StatusCode), unknown)
	}
	result, err := readOpenAI(ctx, resp, req.MaxOutputTokens, sink)
	if err != nil {
		return model.Response{}, remoteError(ctx, "openai: incomplete or malformed stream", true)
	}
	result.Message, err = finishMessage(req, result.Message, conn.SupportsTools)
	if err != nil {
		return model.Response{}, fmt.Errorf("%w: %w", model.ErrUnknown, err)
	}
	if err := ctx.Err(); err != nil {
		return model.Response{}, remoteError(ctx, "openai: completion canceled", true)
	}
	result.Complete = true
	return result, nil
}

type streamChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    *int         `json:"index"`
				ID       string       `json:"id"`
				Type     string       `json:"type"`
				Function wireFunction `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *model.Usage    `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func readOpenAI(ctx context.Context, resp *http.Response, maxOutput int, sink model.Sink) (model.Response, error) {
	var result model.Response
	var content strings.Builder
	calls := map[int]*model.Call{}
	finished, done := false, false
	total, output := 0, 0
	consume := func(data string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if data == "[DONE]" {
			if !finished {
				return errors.New("missing finish reason")
			}
			done = true
			return nil
		}
		if model.StrictJSON([]byte(data)) != nil {
			return errors.New("invalid SSE JSON")
		}
		var chunk streamChunk
		if json.Unmarshal([]byte(data), &chunk) != nil || len(chunk.Error) != 0 && string(chunk.Error) != "null" {
			return errors.New("invalid chunk")
		}
		if len(chunk.Choices) == 0 && chunk.Usage == nil {
			return errors.New("missing choices")
		}
		if chunk.Usage != nil {
			result.Usage = *chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 || finished {
				return errors.New("unexpected choice")
			}
			delta := choice.Delta
			if delta.Role != "" && delta.Role != "assistant" {
				return errors.New("unexpected role")
			}
			output += len(delta.Content)
			for _, tc := range delta.ToolCalls {
				if tc.Index == nil || *tc.Index < 0 || *tc.Index > 1023 || tc.Type != "" && tc.Type != "function" {
					return errors.New("invalid tool index/type")
				}
				target := calls[*tc.Index]
				if target == nil {
					target = &model.Call{}
					calls[*tc.Index] = target
				}
				if tc.ID != "" && tc.ID != target.ID {
					target.ID += tc.ID
				}
				target.Name += tc.Function.Name
				target.Arguments += tc.Function.Arguments
				output += len(tc.ID) + len(tc.Function.Name) + len(tc.Function.Arguments)
			}
			if output > maxResponseBytes || output > maxOutput*16 {
				return errors.New("output limit exceeded")
			}
			content.WriteString(delta.Content)
			if sink != nil && delta.Content != "" {
				sink(delta.Content)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if choice.FinishReason != "" {
				if choice.FinishReason != "stop" && choice.FinishReason != "tool_calls" {
					return errors.New("unfinished completion")
				}
				if choice.FinishReason == "tool_calls" && len(calls) == 0 {
					return errors.New("missing calls")
				}
				finished = true
			}
		}
		return nil
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var data []string
	for scanner.Scan() {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		line := scanner.Text()
		total += len(line)
		if total > maxResponseBytes {
			return result, errors.New("stream too large")
		}
		if line == "" {
			if len(data) != 0 {
				if err := consume(strings.Join(data, "\n")); err != nil {
					return result, err
				}
				data = nil
				if done {
					break
				}
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if scanner.Err() != nil {
		return result, scanner.Err()
	}
	// A terminal marker may be the last unterminated SSE line, but bare EOF is
	// never evidence of completion, even after finish_reason was received.
	if !done && len(data) == 1 && data[0] == "[DONE]" {
		if err := consume(data[0]); err != nil {
			return result, err
		}
	}
	if !done || !finished || ctx.Err() != nil {
		return result, errors.New("missing stream terminator")
	}
	result.Message = model.Message{Role: "assistant", Content: content.String()}
	indices := make([]int, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		result.Message.ToolCalls = append(result.Message.ToolCalls, *calls[index])
	}
	return result, nil
}
