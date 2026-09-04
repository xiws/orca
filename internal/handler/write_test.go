package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteHandlerCreatesFileAndParents(t *testing.T) {
	ws, handle := newWorkspace(t)

	result := handleOne(t, handle, NewWriteOption(1, "deep/nested/a.md", "hello orca"))
	if !result.OK {
		t.Fatalf("write result = %+v", result)
	}
	want := fmt.Sprintf("wrote %d bytes to %s", len("hello orca"), filepath.Join(ws.Root, "deep/nested/a.md"))
	if result.Content != want {
		t.Fatalf("write content = %q, want %q", result.Content, want)
	}
	if got := readRawFile(t, ws, "deep/nested/a.md"); got != "hello orca" {
		t.Fatalf("file content = %q", got)
	}
}

func TestWriteHandlerRebuildsExistingFile(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", "line one\nline two\nline three\n")

	result := handleOne(t, handle, NewWriteOption(1, "test.md", "only line\n"))
	if !result.OK {
		t.Fatalf("write result = %+v", result)
	}
	if got := readRawFile(t, ws, "test.md"); got != "only line\n" {
		t.Fatalf("file content = %q, want the previous content gone", got)
	}
}

func TestWriteHandlerKeepsExistingMode(t *testing.T) {
	ws, handle := newWorkspace(t)
	path := seed(t, ws, "script.sh", "echo old\n")
	if err := os.Chmod(path, 0o750); err != nil {
		t.Fatal(err)
	}

	if result := handleOne(t, handle, NewWriteOption(1, "script.sh", "echo new\n")); !result.OK {
		t.Fatalf("write result = %+v", result)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("mode after rewrite = %o, want 750", got)
	}
}

func TestWriteHandlerLeavesNoTemporaryFiles(t *testing.T) {
	ws, handle := newWorkspace(t)

	if result := handleOne(t, handle, NewWriteOption(1, "clean.md", "x")); !result.OK {
		t.Fatalf("write result = %+v", result)
	}
	entries, err := os.ReadDir(filepath.Join(ws.Root))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "clean.md" {
			t.Fatalf("workspace holds %q after writing, want only the target", entry.Name())
		}
	}
}

func TestWriteHandlerRejectsPathsOutsideWorkspace(t *testing.T) {
	_, handle := newWorkspace(t)
	outside := filepath.Join(t.TempDir(), "escape.md")

	result := handleOne(t, handle, NewWriteOption(1, outside, "x"))
	if result.OK || !strings.Contains(result.Err, ErrOutsideWorkspace.Error()) {
		t.Fatalf("write outside the workspace = %+v", result)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("file %q was created although the command was rejected", outside)
	}

	relative := handleOne(t, handle, NewWriteOption(2, "../escape.md", "x"))
	if relative.OK || !strings.Contains(relative.Err, ErrOutsideWorkspace.Error()) {
		t.Fatalf("write through .. = %+v", relative)
	}
}
