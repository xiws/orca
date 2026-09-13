package utils

import (
	"os"
	"strings"
	"text/template"

	"github.com/xiws/orca/internal/assets"
)

var (
	SystemPrompt      = "system_prompt.md"
	OtterSystemPrompt = "otter_prompt.md"
)

func GetSystemPrompt(data any) string {
	t := template.New("base")

	for _, path := range GetConfigPaths(SystemPrompt) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return string(data)
	}

	template.Must(t.Parse(assets.SystemPrompt))
	var buf strings.Builder

	err := t.Execute(&buf, data)
	if err != nil {
		panic(err)
	}

	return buf.String()
}

func GetOtterSystemPrompt(data any) string {
	t := template.New("base")

	for _, path := range GetConfigPaths(OtterSystemPrompt) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return string(data)
	}

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
