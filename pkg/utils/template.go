package utils

import (
	"os"
	"strings"
	"text/template"

	"github.com/xiws/orca/internal/assets"
)

var (
	// SystemPrompt 系统提示词配置文件名
	SystemPrompt = "system_prompt.md"
	// OtterSystemPrompt Otter 系统提示词配置文件名
	OtterSystemPrompt = "otter_prompt.md"
)

// GetSystemPrompt 获取系统提示词，优先从配置文件加载，否则使用内置模板
func GetSystemPrompt(data any) string {
	t := template.New("base")

	// 尝试从配置文件加载
	for _, path := range GetConfigPaths(SystemPrompt) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return string(data)
	}

	// 使用内置模板
	template.Must(t.Parse(assets.SystemPrompt))
	var buf strings.Builder

	err := t.Execute(&buf, data)
	if err != nil {
		panic(err)
	}

	return buf.String()
}

// GetOtterSystemPrompt 获取 Otter 系统提示词，优先从配置文件加载，否则使用内置模板
func GetOtterSystemPrompt(data any) string {
	t := template.New("base")

	// 尝试从配置文件加载
	for _, path := range GetConfigPaths(OtterSystemPrompt) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return string(data)
	}

	// 使用内置模板
	template.Must(t.Parse(assets.SystemPrompt))
	var buf strings.Builder

	err := t.Execute(&buf, data)
	if err != nil {
		panic(err)
	}

	return buf.String()
}

// GetSubtaskPrompt 使用内置模板生成子任务提示词
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
