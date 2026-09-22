package core

type TaskContext struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description"`
	Result      string `json:"result"`
}

func NewTaskContext(description, title string) TaskContext {
	return TaskContext{Title: title, Description: description}
}

type SystemPromptContext struct {
	ProjectPath   string
	ContextLength int
}
