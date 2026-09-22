package assets

import _ "embed"

//go:embed system_prompt.md
var SystemPrompt string

//go:embed task_prompt.md
var SubtaskPrompt string

//go:embed review_prompt.md
var ReviewPrompt string

//go:embed deliberate_prompt.md
var DeliberatePrompt string
