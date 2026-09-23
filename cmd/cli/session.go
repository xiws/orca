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

func commandHelp(args []string) bool {
	return len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help"
}

func isRemove(command string) bool {
	return command == "rm" || command == "remove" || command == "delete"
}

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

func writeJSON(out io.Writer, value any) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	var safe bytes.Buffer
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
