package agent

import "orca/pkg/utils"

type Task struct {
	Id          int64    `json:"id"`
	TaskResult  string   `json:"task_result"`
	SessionInfo *Session `json:"session_info"`
	TaskTarget  string   `json:"task_target"`
	TaskTitle   string   `json:"task_title"`
}

func NewTask(taskTarget, title string) *Task {
	var session = NewSession()
	return &Task{
		SessionInfo: session,
		TaskResult:  "",
		Id:          utils.GetSnowFlakeId(),
		TaskTarget:  taskTarget,
		TaskTitle:   title,
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

type PromptContext struct {
	ProjectPath   string
	ContextLength int
}
