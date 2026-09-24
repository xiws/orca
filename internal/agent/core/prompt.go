package core

// TaskContext 描述任务的上下文信息，用于构建提示词。
type TaskContext struct {
	// Title 任务标题（可选）。
	Title string `json:"title,omitempty"`
	// Description 任务描述。
	Description string `json:"description"`
	// Result 任务执行结果。
	Result string `json:"result"`
}

// NewTaskContext 创建一个任务上下文。
func NewTaskContext(description, title string) TaskContext {
	return TaskContext{Title: title, Description: description}
}

// SystemPromptContext 提供构建系统提示词所需的上下文信息。
type SystemPromptContext struct {
	// ProjectPath 项目根目录路径。
	ProjectPath string
	// ContextLength 模型的上下文窗口大小。
	ContextLength int
}
