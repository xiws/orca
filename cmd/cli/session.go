package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiws/orca/internal/session"
)

// HandleSessionCommand processes the "session" subcommand.
func HandleSessionCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("session command requires a subcommand (list, rm)")
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
		return fmt.Errorf("unknown session subcommand: %s", args[0])
	}
}

// listSessions displays all saved sessions.
func listSessions() error {
	metas := session.List()
	if len(metas) == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	// Print header
	fmt.Printf("%-20s  %-30s  %8s  %8s  %s\n",
		"ID", "PROJECT", "MESSAGES", "TOKENS", "UPDATED")
	fmt.Println(strings.Repeat("-", 100))

	// Print each session
	for _, m := range metas {
		project := truncatePath(m.ProjectPath, 30)
		updated := formatRelativeTime(m.UpdateTime)
		fmt.Printf("%-20d  %-30s  %8d  %8d  %s\n",
			m.Id, project, m.MessageCount, m.TotalTokens, updated)
	}

	return nil
}

// deleteSessions handles the "rm" subcommand with optional --all flag.
func deleteSessions(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("rm requires a session ID or --all flag")
	}

	// Check for --all flag
	if args[0] == "--all" || args[0] == "-a" {
		return deleteAllSessions()
	}

	// Delete specific sessions
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue // Skip unknown flags
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

// deleteAllSessions removes all sessions after confirmation.
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

// truncatePath shortens a path to fit within maxLen characters.
func truncatePath(path string, maxLen int) string {
	if len(path) <= maxLen {
		return path
	}

	// Try to show the last part of the path
	base := filepath.Base(path)
	if len(base) <= maxLen-3 {
		return "..." + path[len(path)-maxLen+3:]
	}

	// Just truncate from the beginning
	return "..." + path[len(path)-maxLen+3:]
}

// formatRelativeTime formats a Unix timestamp as a human-readable relative time.
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

// printSessionHelp displays help for the session subcommand.
func printSessionHelp() {
	help := `Usage: orca session <command> [arguments]

Commands:
  list, ls              List all saved sessions
  rm, remove <id...>    Delete specified session(s)
  rm --all              Delete all sessions
  help                  Show this help message

Examples:
  orca session list
  orca session rm 1234567890123456789
  orca session rm --all
`
	fmt.Fprint(os.Stdout, help)
}
