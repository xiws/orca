package agent

import "orca/pkg/utils"

type Task struct {
	Id          int64    `json:"id"`
	TaskResult  string   `json:"task_result"`
	Children    []Task   `json:"children"`
	SessionInfo *Session `json:"session_info"`
	TaskTarget  string   `json:"task_target"`
}

func NewTask(taskTarget string) *Task {
	var session = NewSession()
	return &Task{
		Children:    []Task{},
		SessionInfo: session,
		TaskResult:  "",
		Id:          utils.GetSnowFlakeId(),
		TaskTarget:  taskTarget,
	}
}
