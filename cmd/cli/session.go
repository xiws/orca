package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiws/orca/internal/session"
)

// HandleSessionCommand 处理 "session" 子命令。
func HandleSessionCommand(args []string) error {
	if len(args) == 0 {
		printSessionHelp()
		return nil
	}

	switch args[0] {
	case "list", "ls":
		return listSessions()
	case "rm", "remove", "delete":
		return deleteSessions(args[1:])
	case "help", "-h", "--help":
		printSessionHelp()
		return nil
	default:
		return fmt.Errorf("unknown session subcommand: %s (run 'orca session help' for usage)", args[0])
	}
}

// listSessions 显示所有已保存的会话。
func listSessions() error {
	metas := session.List()
	if len(metas) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	// 打印表头
	fmt.Printf("%-20s  %-30s  %-30s  %8s  %8s  %s\n",
		"ID", "TITLE", "PROJECT", "MESSAGES", "TOKENS", "UPDATED")
	fmt.Println(strings.Repeat("-", 130))

	// 打印每个会话
	for _, m := range metas {
		title := truncateStr(m.Title, 30)
		project := truncatePath(m.ProjectPath, 30)
		updated := formatRelativeTime(m.UpdateTime)
		fmt.Printf("%-20d  %-30s  %-30s  %8d  %8d  %s\n",
			m.Id, title, project, m.MessageCount, m.TotalTokens, updated)
	}

	return nil
}

// deleteSessions 处理带可选 --all 标志的 "rm" 子命令。
func deleteSessions(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("rm requires a session ID or --all flag")
	}

	// 检查 --all 标志
	if args[0] == "--all" || args[0] == "-a" {
		return deleteAllSessions()
	}

	// 删除指定会话
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue // 跳过未知标志
		}

		id, err := ParseSessionId(arg)
		if err != nil {
			return fmt.Errorf("invalid session ID %q: %w", arg, err)
		}

		if err := session.Delete(id); err != nil {
			return fmt.Errorf("delete session %d: %w", id, err)
		}
		fmt.Printf("Deleted session %d\n", id)
	}

	return nil
}

// deleteAllSessions 确认后删除所有会话。
func deleteAllSessions() error {
	metas := session.List()
	if len(metas) == 0 {
		fmt.Println("No sessions to delete.")
		return nil
	}

	if err := session.DeleteAll(); err != nil {
		return err
	}

	fmt.Printf("Deleted %d session(s)\n", len(metas))
	return nil
}

// truncateStr 将字符串截断到 maxLen 个字符（按 rune 计算）。
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// truncatePath 缩短路径以适应 maxLen 个字符。
func truncatePath(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}

	// 尝试显示路径的最后一部分
	base := filepath.Base(path)
	if len(base) <= maxLen-3 {
		return "..." + path[len(path)-maxLen+3:]
	}

	// 从开头截断
	return "..." + path[len(path)-maxLen+3:]
}

// formatRelativeTime 将 Unix 时间戳格式化为可读的相对时间。
func formatRelativeTime(timestamp int64) string {
	if timestamp == 0 {
		return "unknown"
	}

	t := time.Unix(timestamp, 0)
	diff := time.Since(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Format("2006-01-02")
	}
}

// printSessionHelp 显示会话子命令的帮助信息。
func printSessionHelp() {
	help := `Usage: orca session <command> [arguments]

管理已保存的会话（列表包含项目目录与用户主目录下的全部会话）。

Commands:
  list, ls              列出所有已保存的会话
  rm, remove <id...>    删除指定会话
  rm --all, -a          删除所有会话
  help                  显示本帮助

Examples:
  orca session list
  orca session rm 1234567890123456789
  orca session rm --all

Tip: 使用 orca -session <id> "<prompt>" 可恢复指定会话继续对话。
`
	fmt.Fprint(os.Stdout, help)
}
