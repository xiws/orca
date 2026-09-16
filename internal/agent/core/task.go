package core

import (
	"time"

	"github.com/xiws/orca/pkg/utils"
)

// TaskStatus 表示任务在生命周期中的当前阶段。
type TaskStatus string

const (
	TaskCreated    TaskStatus = "CREATED"
	TaskClarifying TaskStatus = "CLARIFYING"
	TaskPlanning   TaskStatus = "PLANNING"
	TaskRunning    TaskStatus = "RUNNING"
	TaskVerifying  TaskStatus = "VERIFYING"
	TaskCompleted  TaskStatus = "COMPLETED"
	TaskFailed     TaskStatus = "FAILED"
)

// TaskEvent 记录一次状态变更。
type TaskEvent struct {
	From      TaskStatus `json:"from"`
	To        TaskStatus `json:"to"`
	Timestamp int64      `json:"timestamp"`
	Reason    string     `json:"reason"`
}

// Task 描述一个有生命周期的用户任务。
type Task struct {
	Id            int64          `json:"id"`
	Input         string         `json:"input"`                   // 原始用户输入
	Title         string         `json:"title"`                   // 任务标题
	Specification *Specification `json:"specification,omitempty"` // Clarifier 输出
	WorkflowID    string         `json:"workflow_id,omitempty"`   // Planner 生成的 workflow
	Status        TaskStatus     `json:"status"`                  // 生命周期状态
	SessionInfo   *Session       `json:"session_info"`
	TaskResult    string         `json:"task_result"`
	RetryCount    int            `json:"retry_count"` // 修复重试计数
	MaxRetries    int            `json:"max_retries"` // 最大重试次数
	History       []TaskEvent    `json:"history"`     // 状态变更历史

	// 向后兼容字段，后续逐步迁移。
	TaskTarget string `json:"task_target"`
	TaskTitle  string `json:"task_title"`
}

// NewTask 创建一个新任务，初始状态为 CREATED。
func NewTask(input, title string) *Task {
	var session = NewSession()
	return &Task{
		SessionInfo: session,
		TaskResult:  "",
		Id:          utils.GetSnowFlakeId(),
		Input:       input,
		Title:       title,
		Status:      TaskCreated,
		MaxRetries:  3,
		// 向后兼容
		TaskTarget: input,
		TaskTitle:  title,
	}
}

// RecordTransition 记录一次状态变更到历史。
func (t *Task) RecordTransition(from, to TaskStatus, reason string) {
	t.History = append(t.History, TaskEvent{
		From:      from,
		To:        to,
		Timestamp: time.Now().Unix(),
		Reason:    reason,
	})
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

type PromptContext struct {
	ProjectPath   string
	ContextLength int
}
