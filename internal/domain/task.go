package domain

type TaskID int64

type Task struct {
	Version       int            `json:"version"`
	ParentTaskID  TaskID         `json:"parent_task_id,omitempty"`
	ID            TaskID         `json:"id"`
	SessionID     SessionID      `json:"session_id,omitempty"`
	Input         string         `json:"input"`
	Title         string         `json:"title"`
	Specification *Specification `json:"specification,omitempty"`
}

func NewTask(input, title string, sessionID SessionID) *Task {
	return &Task{
		ID:        TaskID(NewID()),
		Version:   1,
		SessionID: sessionID,
		Input:     input,
		Title:     title,
	}
}
