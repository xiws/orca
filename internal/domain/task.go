package domain

import "github.com/xiws/orca/pkg/utils"

type TaskID int64

type Task struct {
	ID            TaskID         `json:"id"`
	SessionID     SessionID      `json:"session_id,omitempty"`
	Input         string         `json:"input"`
	Title         string         `json:"title"`
	Specification *Specification `json:"specification,omitempty"`
}

func NewTask(input, title string, sessionID SessionID) *Task {
	return &Task{
		ID:        TaskID(utils.GetSnowFlakeId()),
		SessionID: sessionID,
		Input:     input,
		Title:     title,
	}
}
