package core

import (
	"github.com/xiws/orca/pkg/utils"
)

// Task 描述一个有生命周期的用户任务。
type Task struct {
	Id            int64          `json:"id"`
	Input         string         `json:"input"`                   // 原始用户输入
	Title         string         `json:"title"`                   // 任务标题
	Specification *Specification `json:"specification,omitempty"` // Clarifier 输出
	WorkflowID    string         `json:"workflow_id,omitempty"`   // Planner 生成的 workflow
	SessionInfo   *Session       `json:"session_info"`
	TaskResult    string         `json:"task_result"`
	RetryCount    int            `json:"retry_count"` // 修复重试计数
	MaxRetries    int            `json:"max_retries"` // 最大重试次数
}

// NewTask 创建一个新任务。
func NewTask(input, title string) *Task {
	var session = NewSession()
	return &Task{
		SessionInfo: session,
		TaskResult:  "",
		Id:          utils.GetSnowFlakeId(),
		Input:       input,
		Title:       title,
		MaxRetries:  3,
	}
}

type TaskContext struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description"`
	Result      string `json:"result"`
}

func NewTaskContext(desc, title string) TaskContext {
	return TaskContext{
		Title:       title,
		Description: desc,
		Result:      "",
	}
}

type SystemPromptContext struct {
	ProjectPath   string
	ContextLength int
}
