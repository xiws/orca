package utils

import (
	"orca/internal/assets"
	"strings"
	"text/template"
)

func GetSystemPrompt(data any) string {
	t := template.New("base")

	template.Must(t.Parse(assets.SystemPrompt))
	var buf strings.Builder

	err := t.Execute(&buf, data)
	if err != nil {
		panic(err)
	}

	return buf.String()
}

func GetSubtaskPrompt(data any) string {
	t := template.New("subtask")
	template.Must(t.Parse(assets.SubtaskPrompt))
	var buf strings.Builder

	err := t.Execute(&buf, data)
	if err != nil {
		panic(err)
	}

	return buf.String()
}
