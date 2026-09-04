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
		{OldString: "line two", NewString: "line 2"},
	}))
	if !result.OK {
		t.Fatalf("edit result = %+v", result)
	}
	if got := readRawFile(t, ws, "test.md"); got != "line one\nline 2\nline three\n" {
		t.Fatalf("file content = %q", got)
	}
	if !strings.Contains(result.Content, "replaced 1 occurrence(s)") {
		t.Fatalf("summary = %q, want the number of replacements", result.Content)
	}
}

func TestEditHandlerKeepsUntouchedLines(t *testing.T) {
	ws, handle := newWorkspace(t)
	content := "package main\n\nfunc main() {\n\tprintln(1)\n}\n"
	seed(t, ws, "main.go", content)

	result := handleOne(t, handle, NewEditOption(1, "main.go", []EditFragment{
		{OldString: "println(1)", NewString: "println(2)"},
	}))
	if !result.OK {
		t.Fatalf("edit result = %+v", result)
	}
	if got := readRawFile(t, ws, "main.go"); got != strings.Replace(content, "println(1)", "println(2)", 1) {
		t.Fatalf("file content = %q, want only the matched span changed", got)
	}
}

func TestEditHandlerSpansMultipleLines(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", sampleFile)

	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{OldString: "line one\nline two", NewString: "first\nsecond\nthird"},
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

func TestEditHandlerRequiresAUniqueMatch(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", "dup\ndup\n")

	ambiguous := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{OldString: "dup", NewString: "single"},
	}))
	if ambiguous.OK || !strings.Contains(ambiguous.Err, "matches 2 times") {
		t.Fatalf("ambiguous edit = %+v", ambiguous)
	}
	if got := readRawFile(t, ws, "test.md"); got != "dup\ndup\n" {
		t.Fatalf("file changed after a rejected edit: %q", got)
	}

	all := handleOne(t, handle, NewEditOption(2, "test.md", []EditFragment{
		{OldString: "dup", NewString: "single", ReplaceAll: true},
	}))
	if !all.OK {
		t.Fatalf("replace_all edit = %+v", all)
	}
	if got := readRawFile(t, ws, "test.md"); got != "single\nsingle\n" {
		t.Fatalf("file content = %q, want both occurrences replaced", got)
	}
}

func TestEditHandlerReportsAMissingMatch(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", sampleFile)

	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{OldString: "line forty", NewString: "x"},
	}))
	if result.OK || !strings.Contains(result.Err, "not found") {
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
		{OldString: "a", NewString: "1"},
		{OldString: "b", NewString: "2"},
		{OldString: "1\n2", NewString: "first half"},
	}))
	if !result.OK {
		t.Fatalf("edit result = %+v", result)
	}
	if got := readRawFile(t, ws, "test.md"); got != "first half\nc\n" {
		t.Fatalf("file content = %q, want the third fragment to see the output of the first two", got)
	}
}

func TestEditHandlerFailsAtomically(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", sampleFile)

	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{OldString: "line one", NewString: "LINE ONE"},
		{OldString: "not in the file", NewString: "x"},
	}))
	if result.OK || !strings.Contains(result.Err, "fragment 2") {
		t.Fatalf("edit result = %+v, want the failing fragment named", result)
	}
	if got := readRawFile(t, ws, "test.md"); got != sampleFile {
		t.Fatalf("file changed by a failed edit: %q", got)
	}
}

func TestEditHandlerLineRangeFragment(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", sampleFile)

	replaced := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{Start: 1, End: 2, Content: "modify content"},
	}))
	if !replaced.OK {
		t.Fatalf("edit result = %+v", replaced)
	}
	if got := readRawFile(t, ws, "test.md"); got != "modify content\nline three\n" {
		t.Fatalf("file content = %q", got)
	}

	// An empty replacement removes the range.
	removed := handleOne(t, handle, NewEditOption(2, "test.md", []EditFragment{
		{Start: 1, End: 1},
	}))
	if !removed.OK {
		t.Fatalf("removal edit = %+v", removed)
	}
	if got := readRawFile(t, ws, "test.md"); got != "line three\n" {
		t.Fatalf("file content = %q, want the first line gone", got)
	}

	beyond := handleOne(t, handle, NewEditOption(3, "test.md", []EditFragment{
		{Start: 5, End: 9, Content: "x"},
	}))
	if beyond.OK || !strings.Contains(beyond.Err, "beyond the end of the file") {
		t.Fatalf("out of range edit = %+v", beyond)
	}

	// The doc example range is refused as well on a file that short.
	far := handleOne(t, handle, NewEditOption(4, "test.md", []EditFragment{
		{Start: 100, End: 101, Content: " modify content"},
	}))
	if far.OK || !strings.Contains(far.Err, "start line 100") {
		t.Fatalf("edit at line 100 of a 3 line file = %+v", far)
	}
}

func TestEditHandlerVerifiesExpectedLineRange(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", sampleFile)

	wrong := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{OldString: "line two", NewString: "x", Start: 7, End: 7},
	}))
	if wrong.OK || !strings.Contains(wrong.Err, "matches lines 2-2, not 7-7") {
		t.Fatalf("edit with a stale line hint = %+v", wrong)
	}

	right := handleOne(t, handle, NewEditOption(2, "test.md", []EditFragment{
		{OldString: "line two", NewString: "x", Start: 2, End: 2},
	}))
	if !right.OK {
		t.Fatalf("edit with a matching line hint = %+v", right)
	}
}

func TestEditHandlerValidatesInput(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", sampleFile)

	empty := handleOne(t, handle, NewEditOption(1, "test.md", nil))
	if empty.OK || !strings.Contains(empty.Err, ErrEmptyContents.Error()) {
		t.Fatalf("edit without fragments = %+v", empty)
	}

	noLocator := handleOne(t, handle, NewEditOption(2, "test.md", []EditFragment{{NewString: "x"}}))
	if noLocator.OK || !strings.Contains(noLocator.Err, ErrFragmentLocator.Error()) {
		t.Fatalf("fragment without a locator = %+v", noLocator)
	}

	missing := handleOne(t, handle, NewEditOption(3, "nope.md", []EditFragment{{OldString: "a", NewString: "b"}}))
	if missing.OK || !strings.Contains(missing.Err, "nope.md") {
		t.Fatalf("edit of a missing file = %+v", missing)
	}
}

func TestEditHandlerIsANoOpWhenNothingChanges(t *testing.T) {
	ws, handle := newWorkspace(t)
	path := seed(t, ws, "test.md", sampleFile)
	before := statModified(t, path)

	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{OldString: "line two", NewString: "line two"},
	}))
	if !result.OK {
		t.Fatalf("edit result = %+v", result)
	}
	if !strings.Contains(result.Content, "unchanged") {
		t.Fatalf("content = %q, want an unchanged report", result.Content)
	}
	if after := statModified(t, path); !after.Equal(before) {
		t.Fatalf("file was rewritten although nothing changed: %v then %v", before, after)
	}
}

func TestEditHandlerKeepsCRLFStyle(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "win.md", "one\r\ntwo\r\nthree\r\n")

	stringEdit := handleOne(t, handle, NewEditOption(1, "win.md", []EditFragment{
		{OldString: "one\r\ntwo", NewString: "ONE\r\nTWO"},
	}))
	if !stringEdit.OK {
		t.Fatalf("string edit = %+v", stringEdit)
	}
	if got := readRawFile(t, ws, "win.md"); got != "ONE\r\nTWO\r\nthree\r\n" {
		t.Fatalf("file content = %q", got)
	}

	lineEdit := handleOne(t, handle, NewEditOption(2, "win.md", []EditFragment{
		{Start: 3, End: 3, Content: "THREE"},
	}))
	if !lineEdit.OK {
		t.Fatalf("line edit = %+v", lineEdit)
	}
	if got := readRawFile(t, ws, "win.md"); got != "ONE\r\nTWO\r\nTHREE\r\n" {
		t.Fatalf("file content = %q, want the CRLF style kept", got)
	}
}

func TestEditHandlerLastLineWithoutTerminator(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", "one\ntwo")

	result := handleOne(t, handle, NewEditOption(1, "test.md", []EditFragment{
		{Start: 1, End: 1, Content: "ONE"},
	}))
	if !result.OK {
		t.Fatalf("edit result = %+v", result)
	}
	if got := readRawFile(t, ws, "test.md"); got != "ONE\ntwo" {
		t.Fatalf("file content = %q, want the missing trailing terminator kept missing", got)
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
