package handler

import (
	"os"
	"strings"
	"testing"
	"time"
)

const sampleFile = "line one\nline two\nline three\n"

func TestEditHandlerReplacesUniqueMatch(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", sampleFile)

	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{Diff: "@@\n line one\n- line two\n+ line 2\n line three\n"},
	}))
	if !result.OK {
		t.Fatalf("edit result = %+v", result)
	}
	if got := readRawFile(t, ws, "test.md"); got != "line one\nline 2\nline three\n" {
		t.Fatalf("file content = %q", got)
	}
	if !strings.Contains(result.Content, "applied 1 hunk") {
		t.Fatalf("summary = %q, want the number of hunks applied", result.Content)
	}
}

func TestEditHandlerKeepsUntouchedLines(t *testing.T) {
	ws, handle := newWorkspace(t)
	content := "package main\n\nfunc main() {\n\tprintln(1)\n}\n"
	seed(t, ws, "main.go", content)

	result := handleOne(t, handle, NewEditOption(1, "main.go", []EditFragment{
		{Diff: "@@\n func main() {\n-\tprintln(1)\n+\tprintln(2)\n }\n"},
	}))
	if !result.OK {
		t.Fatalf("edit result = %+v", result)
	}
	want := "package main\n\nfunc main() {\n\tprintln(2)\n}\n"
	if got := readRawFile(t, ws, "main.go"); got != want {
		t.Fatalf("file content = %q, want only the matched span changed", got)
	}
}

func TestEditHandlerSpansMultipleLines(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", sampleFile)

	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{Diff: "@@\n line one\n- line one\n- line two\n+ first\n+ second\n+ third\n"},
	}))
	if !result.OK {
		t.Fatalf("edit result = %+v", result)
	}
	if got := readRawFile(t, ws, "test.md"); got != "first\nsecond\nthird\nline three\n" {
		t.Fatalf("file content = %q", got)
	}
	if !strings.Contains(result.Content, "3 -> 4 lines") {
		t.Fatalf("summary = %q, want the line counts before and after", result.Content)
	}
}

func TestEditHandlerWithAmbiguousContext(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", "dup\ndup\n")

	// hunkpatch applies a no-context diff to the first matching occurrence.
	first := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{Diff: "@@\n- dup\n+ single\n"},
	}))
	if !first.OK {
		t.Fatalf("no-context diff should apply to the first occurrence: %+v", first)
	}
	if got := readRawFile(t, ws, "test.md"); got != "single\ndup\n" {
		t.Fatalf("file content = %q, want the first occurrence replaced", got)
	}

	// A diff with context behind the change targets the second occurrence uniquely.
	second := handleOne(t, handle, NewEditOption(2, "test.md", []EditFragment{
		{Diff: "@@\n dup\n- dup\n+ single2\n"},
	}))
	if !second.OK {
		t.Fatalf("targeted edit = %+v", second)
	}
	if got := readRawFile(t, ws, "test.md"); got != "single\nsingle2\n" {
		t.Fatalf("file content = %q, want the second occurrence replaced", got)
	}
}

func TestEditHandlerReportsAMissingMatch(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", sampleFile)

	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{Diff: "@@\n line one\n- line forty\n+ x\n"},
	}))
	if result.OK || !strings.Contains(result.Err, "did not match") {
		t.Fatalf("edit with a missing match = %+v", result)
	}
	if got := readRawFile(t, ws, "test.md"); got != sampleFile {
		t.Fatalf("file changed after a rejected edit: %q", got)
	}
}

func TestEditHandlerAppliesFragmentsInOrder(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", "a\nb\nc\n")

	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{Diff: "@@\n- a\n+ 1\n b\n"},
		{Diff: "@@\n 1\n- b\n+ 2\n c\n"},
	}))
	if !result.OK {
		t.Fatalf("edit result = %+v", result)
	}
	if got := readRawFile(t, ws, "test.md"); got != "1\n2\nc\n" {
		t.Fatalf("file content = %q, want the second fragment to see the output of the first", got)
	}
}

func TestEditHandlerFailsAtomically(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", sampleFile)

	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{Diff: "@@\n- line one\n+ LINE ONE\n line two\n"},
		{Diff: "@@\n- not in the file\n+ x\n line two\n"},
	}))
	if result.OK || !strings.Contains(result.Err, "fragment 2") {
		t.Fatalf("edit result = %+v, want the failing fragment named", result)
	}
	if got := readRawFile(t, ws, "test.md"); got != sampleFile {
		t.Fatalf("file changed by a failed edit: %q", got)
	}
}

func TestEditHandlerValidatesInput(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", sampleFile)

	empty := handleOne(t, handle, NewEditOption(1, "test.md", nil))
	if empty.OK || !strings.Contains(empty.Err, ErrEmptyContents.Error()) {
		t.Fatalf("edit without fragments = %+v", empty)
	}

	noDiff := handleOne(t, handle, NewEditOption(2, "test.md", []EditFragment{{}}))
	if noDiff.OK || !strings.Contains(noDiff.Err, ErrFragmentLocator.Error()) {
		t.Fatalf("fragment without a diff = %+v", noDiff)
	}

	missing := handleOne(t, handle, NewEditOption(3, "nope.md", []EditFragment{{Diff: "@@\n- a\n+ b\n"}}))
	if missing.OK || !strings.Contains(missing.Err, "nope.md") {
		t.Fatalf("edit of a missing file = %+v", missing)
	}
}

func TestEditHandlerIsANoOpWhenNothingChanges(t *testing.T) {
	ws, handle := newWorkspace(t)
	path := seed(t, ws, "test.md", sampleFile)
	before := statModified(t, path)

	// A diff whose old and new are identical — hunkpatch treats this as a hunk
	// that left the text unchanged, which we report as an error since the model
	// should not have sent it.
	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{Diff: "@@\n line one\n- line two\n+ line two\n line three\n"},
	}))
	if result.OK {
		t.Fatalf("identical old/new should be rejected, got ok: %+v", result)
	}
	if after := statModified(t, path); !after.Equal(before) {
		t.Fatalf("file was rewritten after a rejected edit: %v then %v", before, after)
	}
}

func TestEditHandlerConvertsCRLFToLF(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "win.md", "one\r\ntwo\r\nthree\r\n")

	// hunkpatch internally strips \r from lines, so CRLF content becomes LF.
	// Context before the change is required for CRLF sources.
	diffEdit := handleOne(t, handle, NewEditOption(1, "win.md", []EditFragment{
		{Diff: "@@\n one\n- one\n- two\n+ ONE\n+ TWO\n"},
	}))
	if !diffEdit.OK {
		t.Fatalf("diff edit = %+v", diffEdit)
	}
	if got := readRawFile(t, ws, "win.md"); got != "ONE\nTWO\nthree\n" {
		t.Fatalf("file content = %q, want LF-only output", got)
	}
}

func TestEditHandlerHunkpatchAddsTrailingNewline(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", "one\ntwo")

	// hunkpatch internally normalizes lines and adds a trailing newline
	// even when the source file didn't have one.
	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{Diff: "@@\n- one\n+ ONE\n two\n"},
	}))
	if !result.OK {
		t.Fatalf("edit result = %+v", result)
	}
	if got := readRawFile(t, ws, "test.md"); got != "ONE\ntwo\n" {
		t.Fatalf("file content = %q, want a trailing newline added by hunkpatch", got)
	}
}

// statModified returns the modification time of a file, which rewriting it
// would move forward.
func statModified(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}
