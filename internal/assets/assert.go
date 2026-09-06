package assets

import _ "embed"

//go:embed system_prompt.md
var SystemPrompt string

//go:embed task_prompt.md
var SubtaskPrompt string
