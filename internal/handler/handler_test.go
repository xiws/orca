package handler

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orca/pkg/command"
)

// newWorkspace returns a workspace rooted in a temporary directory, with all
// four command handlers registered.
func newWorkspace(t *testing.T) (Workspace, *command.CommandHandle) {
	t.Helper()
	root := t.TempDir()
	handle := command.NewCommandHandle()
	if err := Register(handle, Workspace{Root: root}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return Workspace{Root: root}, handle
}

// seed writes content to name inside ws and returns the full path.
func seed(t *testing.T, ws Workspace, name, content string) string {
	t.Helper()
	path := filepath.Join(ws.Root, name)
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), fileMode); err != nil {
		t.Fatal(err)
	}
	return path
}

// readRawFile returns the content of name inside ws.
func readRawFile(t *testing.T, ws Workspace, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(ws.Root, name))
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", name, err)
	}
	return string(data)
}

// samePath compares two paths after resolving symlinks, ignoring a path that
// cannot be resolved.
func samePath(t *testing.T, left, right string) bool {
	t.Helper()
	resolve := func(path string) string {
		if full, err := filepath.EvalSymlinks(path); err == nil {
			return full
		}
		return path
	}
	return resolve(left) == resolve(right)
}

// handleOne runs a single option through the registry and assets that the
// framework accepted it.
func handleOne(t *testing.T, handle *command.CommandHandle, cmd command.CommandOption) CommandResult {
	t.Helper()
	err, value := handle.Execute(cmd)
	if err != nil {
		t.Fatalf("Execute(%s) framework error = %v", cmd.GetName(), err)
	}
	result, ok := value.(CommandResult)
	if !ok {
		t.Fatalf("Execute(%s) result = %T, want CommandResult", cmd.GetName(), value)
	}
	return result
}

func TestRegisterDispatchesAllCommands(t *testing.T) {
	ws, handle := newWorkspace(t)

	created := handleOne(t, handle, NewWriteOption(1, "notes/a.md", "# title\n\nbody\n"))
	if !created.OK {
		t.Fatalf("write result = %+v", created)
	}
	if created.Command != CommandWrite || created.Id != 1 {
		t.Fatalf("write result = %+v, want command write and id 1", created)
	}
	if got := readRawFile(t, ws, "notes/a.md"); got != "# title\n\nbody\n" {
		t.Fatalf("file content = %q", got)
	}

	read := handleOne(t, handle, NewReadOption(1, "notes/a.md", 1, 1))
	if !read.OK || read.Content != "1\t# title\n" {
		t.Fatalf("read result = %+v", read)
	}

	edited := handleOne(t, handle, NewEditOption(1, "notes/a.md", []EditFragment{
		{Diff: "@@\n \n- body\n+ content\n"},
	}))
	if !edited.OK {
		t.Fatalf("edit result = %+v", edited)
	}
	if !strings.Contains(edited.Content, "3 -> 3 lines") {
		t.Fatalf("edit summary = %q, want the line counts before and after", edited.Content)
	}

	bashed := handleOne(t, handle, NewBashOption(1, "pwd", "", 0))
	if !bashed.OK {
		t.Fatalf("bash result = %+v", bashed)
	}
	// A shell may report the directory as it was handed over or with symlinks
	// resolved, which temporary directories on macOS are, so compare the
	// physical forms.
	if got := strings.TrimSpace(bashed.Content); !samePath(t, got, ws.Root) {
		t.Fatalf("bash ran in %q, want %q", got, ws.Root)
	}

	for _, result := range []CommandResult{created, read, edited, bashed} {
		if result.Id == 0 || result.Command == "" {
			t.Fatalf("result %+v misses its identity", result)
		}
	}
}

func TestHandlersRejectForeignOptions(t *testing.T) {
	ws := Workspace{Root: t.TempDir()}

	cases := []struct {
		name    string
		handler command.CommandHandler
		foreign command.CommandOption
	}{
		{CommandRead, ReadHandler{Workspace: ws}, NewBashOption(1, "pwd", "", 0)},
		{CommandWrite, WriteHandler{Workspace: ws}, NewReadOption(1, "a.md", 0, 0)},
		{CommandEdit, EditHandler{Workspace: ws}, NewWriteOption(1, "a.md", "x")},
		{CommandBash, BashHandler{Workspace: ws}, NewEditOption(1, "a.md", nil)},
	}
	for _, tc := range cases {
		err, value := tc.handler.Handle(tc.foreign)
		if !errors.Is(err, ErrUnsupportedOption) {
			t.Fatalf("%s Handle() error = %v, want %v", tc.name, err, ErrUnsupportedOption)
		}
		if value != nil {
			t.Fatalf("%s Handle() value = %v, want nil on a framework error", tc.name, value)
		}
	}
}

func TestResultForGeneratesMissingId(t *testing.T) {
	result := ResultFor(&ReadOption{}, "payload", nil)
	if result.Id == 0 {
		t.Fatal("ResultFor() left the id zero, want a generated one")
	}
	if result.Command != CommandRead {
		t.Fatalf("ResultFor() command = %q, want %q", result.Command, CommandRead)
	}
}

func TestNewResultCarriesFailure(t *testing.T) {
	failed := NewResult(1, CommandBash, "partial output", os.ErrPermission)
	if failed.OK || failed.Err == "" || failed.Content != "partial output" {
		t.Fatalf("NewResult() = %+v, want a failure keeping its content", failed)
	}
}
