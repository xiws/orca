package domain

// TaskID 任务的唯一标识符。
type TaskID int64

// Task 表示一个执行任务，包含输入、标题、可选的规格说明，并关联到会话和父任务。
type Task struct {
	Version       int            `json:"version"`                  // 版本号，每次修改递增
	ParentTaskID  TaskID         `json:"parent_task_id,omitempty"` // 父任务 ID，用于任务层级
	ID            TaskID         `json:"id"`                       // 任务唯一标识
	SessionID     SessionID      `json:"session_id,omitempty"`     // 所属会话 ID
	Input         string         `json:"input"`                    // 用户原始输入
	Title         string         `json:"title"`                    // 任务标题
	Specification *Specification `json:"specification,omitempty"`  // 可选的需求规格
}

// NewTask 创建一个新任务，自动生成唯一 ID，初始版本为 1。
func NewTask(input, title string, sessionID SessionID) *Task {
	return &Task{
		ID:        TaskID(NewID()),
		Version:   1,
		SessionID: sessionID,
		Input:     input,
		Title:     title,
	}
}
