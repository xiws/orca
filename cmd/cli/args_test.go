package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseSubmission(t *testing.T) {
	a, err := parseArgs([]string{"-p", "fake", "-m", "local", "-session", "123", "-mode", "ask", "-f", "one", "--file", "two", "-sp", "system", "goal", "text"}, &bytes.Buffer{})
	want := &CliArgs{Provider: "fake", Model: "local", SessionId: 123, Mode: "ask", Files: []string{"one", "two"}, SystemPrompt: "system", Prompt: "goal text"}
	if err != nil || !reflect.DeepEqual(a, want) {
		t.Fatalf("%+v %v", a, err)
	}
	a, err = parseArgs([]string{"--", "-literal"}, &bytes.Buffer{})
	if err != nil || a.Prompt != "-literal" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestStrictArguments(t *testing.T) {
	invalid := [][]string{
		{}, {"-session", "0", "goal"}, {"-session", "-1", "goal"}, {"-session", "+1", "goal"}, {"-session", "9223372036854775808", "goal"},
		{"-p", "only", "goal"}, {"-m", "only", "goal"}, {"-mode", "unknown", "goal"}, {"-unknown", "goal"}, {"-f"}, {" "},
		{"session", "rm"}, {"session", "rm", "--all", "1"}, {"session", "show", "1", "extra"}, {"session", "list", "extra"},
		{"session", "import", "old.json"}, {"session", "import", "old.json", "--owner", " "},
		{"run", "show", "1", "--after", "-1"}, {"run", "show", "1", "--after", "bad"}, {"run", "show", "0"}, {"run", "help", "extra"},
		{"run", "resume", "1", "extra"}, {"run", "respond", "1", " "}, {"run", "approve", "bad"}, {"run", "reject", "+1"}, {"run", "typo", "1"},
		{"run", "reconcile", "1"}, {"run", "reconcile", "1", "--note", " "}, {"run", "reconcile", "1", "--note", "ok", "extra"},
		{"task", "revise", "1"}, {"task", "revise", "0", "input"}, {"task", "revise", "1", " "}, {"task", "list"},
	}
	for _, argv := range invalid {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			if a, err := parseArgs(argv, &bytes.Buffer{}); err == nil {
				t.Fatalf("accepted %+v", a)
			}
		})
	}
	for _, argv := range [][]string{{"session", "rm", "--all"}, {"run", "show", "1", "--after", "0"}, {"run", "reconcile", "1", "--note", "verified externally"}, {"task", "revise", "1", "new input"}} {
		if _, err := parseArgs(argv, &bytes.Buffer{}); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
	}
}

func setArgs(t *testing.T, args ...string) {
	t.Helper()
	old := os.Args
	os.Args = append([]string{"orca"}, args...)
	t.Cleanup(func() { os.Args = old })
}

func TestHelpAndInputErrorsHaveNoConfigurationIO(t *testing.T) {
	for _, argv := range [][]string{{"--help"}, {"-session", "1", "--help"}, {"session"}, {"session", "help"}, {"run", "--help"}, {"task", "-h"}} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("WORKSPACE", root)
			t.Setenv("HOME", filepath.Join(root, "absent-home"))
			setArgs(t, argv...)
			if err := run(); err != nil {
				t.Fatal(err)
			}
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 0 {
				t.Fatalf("help performed I/O: %v %v", files, err)
			}
		})
	}
	for _, flag := range []string{"-f", "-sp"} {
		t.Run(flag, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("WORKSPACE", root)
			t.Setenv("HOME", filepath.Join(root, "absent-home"))
			setArgs(t, flag, filepath.Join(root, "missing"), "goal")
			if err := run(); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%v", err)
			}
			files, _ := os.ReadDir(root)
			if len(files) != 0 {
				t.Fatal("opened environment before reading files")
			}
		})
	}
}

func TestPrepareReadsAllFilesBeforeSubmit(t *testing.T) {
	root := t.TempDir()
	attachment := filepath.Join(root, "source.go")
	system := filepath.Join(root, "system.txt")
	if err := os.WriteFile(attachment, []byte("package sample\n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(system, []byte("custom system\n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &CliArgs{Prompt: "explain", Files: []string{attachment}, SystemPrompt: system, SessionId: 8, Provider: "fake", Model: "local"}
	req, err := prepareRequest(a)
	if err != nil {
		t.Fatal(err)
	}
	if req.Input != "explain" || req.SystemPrompt != "custom system\n\n" || req.SessionID != 8 || req.Model.Provider != "fake" || !strings.Contains(req.Prompt, "package sample\n</file>\n\nexplain") {
		t.Fatalf("%+v", req)
	}
	os.Remove(attachment)
	os.Remove(system)
	f := newFake()
	f.onSubmit = func(got appSubmit) {
		if !reflect.DeepEqual(got, req) {
			t.Fatalf("%+v", got)
		}
	}
	if err := execute(t.Context(), f, nil, a, req, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if f.last != "continue:8" {
		t.Fatal(f.last)
	}
	a.Files = []string{attachment}
	if p, err := userPrompt(a); p != "" || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%q %v", p, err)
	}
}
