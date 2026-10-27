package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"unicode"
	"unicode/utf16"

	"github.com/xiws/orca/internal/domain"
)

// commandHelp 判断子命令参数是否请求帮助（无参数、help、-h 或 --help）。
func commandHelp(args []string) bool {
	return len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help"
}

// isRemove 判断命令是否为删除类操作（rm/remove/delete）。
func isRemove(command string) bool {
	return command == "rm" || command == "remove" || command == "delete"
}

// handleSession 分发会话子命令：list/show/import/rm。
func handleSession(ctx context.Context, s service, importer importSession, args []string, out io.Writer) error {
	if commandHelp(args) {
		_, err := fmt.Fprint(out, helpText)
		return err
	}
	switch args[0] {
	case "list", "ls":
		sessions, err := s.Sessions(ctx)
		if err != nil {
			return err
		}
		return writeJSON(out, sessions)
	case "show":
		id, err := ParseSessionId(args[1])
		if err != nil {
			return err
		}
		session, err := s.Session(ctx, domain.SessionID(id))
		if err != nil {
			return err
		}
		return writeJSON(out, session)
	case "import":
		if importer == nil {
			return fmt.Errorf("session importer unavailable")
		}
		id, err := importer(ctx, args[1], args[3])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Imported session=%d owner=%q\nWarning: the original JSON may still contain secrets; protect or remove it manually. Old execution cursors are NOT restored.\n", id, args[3])
		return err
	case "rm", "remove", "delete":
		return deleteSessions(ctx, s, args[1:], out)
	default:
		return fmt.Errorf("unknown session subcommand %q", args[0])
	}
}

// deleteSessions 根据参数删除指定会话或全部会话，跳过重复 ID。
func deleteSessions(ctx context.Context, s service, args []string, out io.Writer) error {
	if err := validateCommand(&CliArgs{SubCommand: "session", SubArgs: append([]string{"rm"}, args...)}); err != nil {
		return err
	}
	if args[0] == "--all" || args[0] == "-a" {
		if err := s.DeleteAllSessions(ctx); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "Deleted all sessions in this workspace.")
		return err
	}
	seen := map[domain.SessionID]bool{}
	for _, arg := range args {
		id, _ := ParseSessionId(arg)
		if seen[domain.SessionID(id)] {
			continue
		}
		if err := s.DeleteSession(ctx, domain.SessionID(id)); err != nil {
			return fmt.Errorf("delete session %d: %w", id, err)
		}
		seen[domain.SessionID(id)] = true
		if _, err := fmt.Fprintf(out, "Deleted session=%d\n", id); err != nil {
			return err
		}
	}
	return nil
}

// writeJSON 将值格式化为缩进 JSON 输出，并对控制字符和格式字符做 Unicode 转义，
// 避免终端显示异常。
func writeJSON(out io.Writer, value any) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	var safe bytes.Buffer
	// 转义控制字符和 Unicode 格式字符，防止终端渲染异常
	for _, r := range encoded.String() {
		if (unicode.IsControl(r) && r != '\n') || unicode.Is(unicode.Cf, r) {
			if r > 0xffff {
				hi, lo := utf16.EncodeRune(r)
				fmt.Fprintf(&safe, "\\u%04x\\u%04x", hi, lo)
			} else {
				fmt.Fprintf(&safe, "\\u%04x", r)
			}
		} else {
			safe.WriteRune(r)
		}
	}
	_, err := out.Write(safe.Bytes())
	return err
}
