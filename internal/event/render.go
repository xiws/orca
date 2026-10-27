package event

import (
	"fmt"
	"strings"
)

// buildTitle 返回工具的 emoji 前缀标题行。
func buildTitle(tool, file, meta string) string {
	const (
		emojiRead       = "📖"
		emojiWrite      = "📝"
		emojiEdit       = "✏️"
		emojiBash       = "💻"
		emojiCreateTask = "🎯"
	)

	switch tool {
	case "read":
		return fmt.Sprintf("%s Read `%s` (%s)", emojiRead, file, meta)
	case "write":
		return fmt.Sprintf("%s Write `%s` (%s)", emojiWrite, file, meta)
	case "edit":
		return fmt.Sprintf("%s Edit `%s` (%s)", emojiEdit, file, meta)
	case "bash":
		return fmt.Sprintf("%s $ `%s`", emojiBash, meta)
	case "create_task":
		return fmt.Sprintf("%s %s", emojiCreateTask, meta)
	default:
		if file != "" {
			return fmt.Sprintf("`%s` — `%s` (%s)", tool, file, meta)
		}
		return fmt.Sprintf("`%s` (%s)", tool, meta)
	}
}

// detectLang 将文件扩展名映射到 markdown 代码块的语言标签。
// 未知扩展名返回空字符串，渲染为无语法高亮的普通代码块。
func detectLang(filePath string) string {
	switch {
	case strings.HasSuffix(filePath, ".go"):
		return "go"
	case strings.HasSuffix(filePath, ".md"):
		return "markdown"
	case strings.HasSuffix(filePath, ".json"):
		return "json"
	case strings.HasSuffix(filePath, ".yaml"),
		strings.HasSuffix(filePath, ".yml"):
		return "yaml"
	case strings.HasSuffix(filePath, ".sh"):
		return "bash"
	case strings.HasSuffix(filePath, ".py"):
		return "python"
	case strings.HasSuffix(filePath, ".js"):
		return "javascript"
	case strings.HasSuffix(filePath, ".ts"):
		return "typescript"
	case strings.HasSuffix(filePath, ".html"):
		return "html"
	case strings.HasSuffix(filePath, ".css"):
		return "css"
	case strings.HasSuffix(filePath, ".toml"):
		return "toml"
	case strings.HasSuffix(filePath, ".mod"):
		return "go"
	case strings.HasSuffix(filePath, ".sum"):
		return "go"
	case strings.HasSuffix(filePath, ".rs"):
		return "rust"
	case strings.HasSuffix(filePath, ".rb"):
		return "ruby"
	case strings.HasSuffix(filePath, ".java"):
		return "java"
	case strings.HasSuffix(filePath, ".sql"):
		return "sql"
	case strings.HasSuffix(filePath, ".xml"):
		return "xml"
	case strings.HasSuffix(filePath, ".proto"):
		return "protobuf"
	default:
		return ""
	}
}

// renderBeforeMarkdown 构建工具 before-event 的 markdown 表示。
func renderBeforeMarkdown(tool, file, meta, reasoning string) string {
	title := buildTitle(tool, file, meta)

	if reasoning == "" {
		return fmt.Sprintf("**%s**\n", title)
	}
	return fmt.Sprintf("**%s**\n> %s\n", title, reasoning)
}

// renderAfterMarkdown 构建工具 after-event 的 markdown 表示。
func renderAfterMarkdown(tool, file, content, summary string, ok bool, exitCode int) string {
	if !ok {
		return fmt.Sprintf("❌ **%s** `%s` failed: %s\n", tool, file, summary)
	}

	switch tool {
	case "read":
		lang := detectLang(file)
		return fmt.Sprintf("```%s\n%s\n```\n", lang, content)

	case "write":
		return fmt.Sprintf("✅ %s\n", summary)

	case "edit":
		return fmt.Sprintf("✅ %s\n", summary)

	case "bash":
		var out strings.Builder
		exitWord := "ok"
		if exitCode != 0 {
			exitWord = fmt.Sprintf("exit code: %d", exitCode)
		}
		out.WriteString(fmt.Sprintf("```\n$ %s\n```\n", summary))
		if content != "" {
			out.WriteString(fmt.Sprintf("```\n%s\n```\n", content))
		}
		if exitCode != 0 {
			out.WriteString(fmt.Sprintf("\n_%s_\n", exitWord))
		}
		return out.String()

	default:
		return fmt.Sprintf("```txt\n%s\n```\n", content)
	}
}

// renderAfterRaw 以纯文本形式打印事件内容，作为 glamour 渲染失败时的回退。
func renderAfterRaw(tool, file, summary, content string, ok bool) {
	if !ok {
		fmt.Printf("❌ %s (%s): %s\n", tool, file, summary)
		return
	}
	fmt.Printf("✔ %s (%s): %s\n", tool, file, summary)
	if content != "" {
		fmt.Println(content)
	}
}
