// Package assets 通过 go:embed 嵌入系统提示词模板文件。
package assets

import _ "embed"

// SystemPrompt 嵌入系统级提示词模板。
//
//go:embed system_prompt.md
var SystemPrompt string

// SubtaskPrompt 嵌入子任务提示词模板。
//
//go:embed task_prompt.md
var SubtaskPrompt string

// ReviewPrompt 嵌入代码审查提示词模板。
//
//go:embed review_prompt.md
var ReviewPrompt string

// DeliberatePrompt 嵌入深度思考模式提示词模板。
//
//go:embed deliberate_prompt.md
var DeliberatePrompt string
