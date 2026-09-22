package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseArgsTurnOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want CliArgs
	}{
		{
			name: "short flags",
			argv: []string{"-p", "saved", "-m", "saved-model", "-session", "123", "-mode", "ask", "-f", "first.go", "-f", "second.go", "-s", "system.txt", "continue", "the task"},
			want: CliArgs{Provider: "saved", Model: "saved-model", SessionId: 123, Mode: "ask", Files: []string{"first.go", "second.go"}, SystemPrompt: "system.txt", Prompt: "continue the task"},
		},
		{
			name: "long flags",
			argv: []string{"--provider", "saved", "--model", "saved-model", "--session", "456", "--mode", "code", "--file", "first.go", "--file", "second.go", "--sp", "system.txt", "new", "task"},
			want: CliArgs{Provider: "saved", Model: "saved-model", SessionId: 456, Mode: "code", Files: []string{"first.go", "second.go"}, SystemPrompt: "system.txt", Prompt: "new task"},
		},
		{
			name: "new session defaults",
			argv: []string{"new", "task"},
			want: CliArgs{Prompt: "new task"},
		},
		{
			name: "flag terminator",
			argv: []string{"--", "-not-a-flag", "prompt"},
			want: CliArgs{Prompt: "-not-a-flag prompt"},
		},
		{
			name: "session command",
			argv: []string{"session", "rm", "123", "456"},
			want: CliArgs{SubCommand: "session", SubArgs: []string{"rm", "123", "456"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLI(t)
			got := parseCLI(t, tc.argv...)
			if !reflect.DeepEqual(*got, tc.want) {
				t.Fatalf("ParseArgs() = %+v, want %+v", *got, tc.want)
			}
		})
	}
}

func TestParseArgsErrorsAndHelp(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
		help bool
	}{
		{name: "missing prompt", want: "missing prompt"},
		{name: "session still needs prompt", argv: []string{"-session", "123"}, want: "missing prompt"},
		{name: "invalid session ID", argv: []string{"-session", "not-an-id", "goal"}, want: "invalid value"},
		{name: "overflow session ID", argv: []string{"-session", "9223372036854775808", "goal"}, want: "invalid value"},
		{name: "unknown flag", argv: []string{"-unknown", "goal"}, want: "flag provided but not defined"},
		{name: "missing flag value", argv: []string{"-model"}, want: "flag needs an argument"},
		{name: "short help", argv: []string{"-h"}, help: true},
		{name: "long help", argv: []string{"--help"}, help: true},
		{name: "help command", argv: []string{"help"}, help: true},
		{name: "help after flags", argv: []string{"-session", "123", "--help"}, help: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLI(t)
			setCLIArgs(t, tc.argv...)
			args, err := ParseArgs()
			if args != nil || err == nil {
				t.Fatalf("ParseArgs() = %+v, %v; want nil args and error", args, err)
			}
			if tc.help {
				if !errors.Is(err, ErrHelpRequested) {
					t.Fatalf("help error = %v", err)
				}
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRunStopsBeforeExecutionOnInputErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{name: "parse failure", want: "missing prompt"},
		{name: "attachment failure", argv: []string{"-file", "missing.txt", "goal"}, want: "read attached file missing.txt"},
		{name: "system prompt failure", argv: []string{"-sp", "missing.txt", "goal"}, want: "read system prompt missing.txt"},
		{name: "session failure", argv: []string{"-session", "404", "goal"}, want: "load session 404"},
		{name: "model failure", argv: []string{"-p", "absent", "-m", "absent", "goal"}, want: "unknown model absent/absent"},
		{name: "help", argv: []string{"--help"}},
		{name: "session list", argv: []string{"session", "list"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLI(t)
			setCLIArgs(t, tc.argv...)
			err := run()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run() = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestUserPromptReadFailureDiscardsPartialAttachments(t *testing.T) {
	workspace, _ := isolateCLI(t)
	first, missing := filepath.Join(workspace, "first.txt"), filepath.Join(workspace, "missing.txt")
	writeCLIFile(t, first, "first attachment")
	prompt, err := userPrompt(&CliArgs{Files: []string{first, missing}, Prompt: "goal"})
	if prompt != "" || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("userPrompt() = %q, %v; want empty prompt and missing-file error", prompt, err)
	}
	if !strings.Contains(err.Error(), "read attached file "+missing) {
		t.Fatalf("error lost the failed attachment path: %v", err)
	}
}
