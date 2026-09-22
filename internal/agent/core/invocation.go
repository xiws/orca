package core

import (
	"maps"
	"slices"
	"time"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/llm"
	"github.com/xiws/orca/pkg/utils"
)

type Invocation struct {
	ID          int64             `json:"id"`
	TaskID      domain.TaskID     `json:"task_id"`
	ProjectPath string            `json:"project_path"`
	Provider    llm.ModelInfo     `json:"-"`
	Messages    []llm.ChatMessage `json:"messages"`
	TotalUsage  llm.Usage         `json:"total_usage"`
	OtterState  *llm.OtterState   `json:"otter_state,omitempty"`
	Children    []*Invocation     `json:"children,omitempty"`
}

func NewInvocation(taskID domain.TaskID, projectPath string, provider llm.ModelInfo) *Invocation {
	provider.AllowedTools = slices.Clone(provider.AllowedTools)
	return &Invocation{
		ID:          utils.GetSnowFlakeId(),
		TaskID:      taskID,
		ProjectPath: projectPath,
		Provider:    provider,
		Messages:    make([]llm.ChatMessage, 0),
	}
}

func (i *Invocation) NewChild() *Invocation {
	child := NewInvocation(i.TaskID, i.ProjectPath, i.Provider)
	i.Children = append(i.Children, child)
	return child
}

func (i *Invocation) AppendMessage(role, content string) {
	i.Append(llm.ChatMessage{Role: role, Content: content})
}

func (i *Invocation) Append(message llm.ChatMessage) {
	if message.Id == 0 {
		message.Id = utils.GetSnowFlakeId()
	}
	if message.CreateTime == 0 {
		message.CreateTime = time.Now().Unix()
	}
	message.ToolCalls = slices.Clone(message.ToolCalls)
	i.Messages = append(i.Messages, message)
}

func (i *Invocation) AppendAssistant(content string, calls []llm.ToolCall) {
	i.Append(llm.ChatMessage{Role: llm.RoleAssistant, Content: content, ToolCalls: calls})
}

func (i *Invocation) AppendToolResult(callID, content string) {
	i.Append(llm.ChatMessage{Role: llm.RoleTool, Content: content, ToolCallID: callID})
}

func (i *Invocation) AddUsage(usage llm.Usage) {
	i.TotalUsage.PromptTokens += usage.PromptTokens
	i.TotalUsage.CompletionTokens += usage.CompletionTokens
	i.TotalUsage.TotalTokens += usage.TotalTokens
}

func (i *Invocation) GetOtterState() llm.OtterState {
	if i.OtterState == nil {
		return llm.OtterState{}
	}
	state := *i.OtterState
	state.RemoteMetadata = maps.Clone(state.RemoteMetadata)
	return state
}

func (i *Invocation) SetOtterState(state llm.OtterState) {
	state.RemoteMetadata = maps.Clone(state.RemoteMetadata)
	i.OtterState = &state
}
