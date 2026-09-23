package model

import (
	"context"
	"encoding/json"
	"errors"
)

type Ref struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type Call struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Message struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCalls  []Call `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	Estimated        bool  `json:"estimated,omitempty"`
}

type Cursor struct {
	Adapter        string          `json:"adapter"`
	Version        int             `json:"version"`
	Model          Ref             `json:"model"`
	ThreadID       int64           `json:"thread_id"`
	ContextVersion int             `json:"context_version"`
	Sequence       int             `json:"sequence"`
	Data           json.RawMessage `json:"data"`
}

func (c *Cursor) Matches(adapter string, req Request) bool {
	return c == nil || (c.Adapter == adapter && c.Version == 1 && c.Model == req.Model && c.ThreadID == req.ThreadID && c.ContextVersion == req.ContextVersion && c.Sequence >= 0 && c.Sequence <= len(req.Messages))
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type Request struct {
	Model           Ref
	ThreadID        int64
	ContextVersion  int
	Messages        []Message
	Tools           []Tool
	Cursor          *Cursor
	MaxOutputTokens int
}

type Response struct {
	Message  Message
	Usage    Usage
	Cursor   *Cursor
	Complete bool
}

type Sink func(string)

type Client interface {
	Complete(context.Context, Request, Sink) (Response, error)
}

var ErrUnknown = errors.New("model request outcome unknown; reconcile before retry")
var ErrCursorMismatch = errors.New("provider cursor does not match thread context")

func Estimate(messages []Message) int64 {
	var bytes int
	for _, m := range messages {
		bytes += len(m.Content) + 16
		for _, c := range m.ToolCalls {
			bytes += len(c.Name) + len(c.Arguments) + 16
		}
	}
	return int64(bytes+3) / 4
}
