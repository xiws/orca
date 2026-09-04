package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadHandlerNumbersLines(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", "one\ntwo\nthree\n")

	result := handleOne(t, handle, NewReadOption(1, "test.md", 0, 0))
	if !result.OK {
		t.Fatalf("read result = %+v", result)
	}
	if got := result.Content; got != "1\tone\n2\ttwo\n3\tthree\n" {
		t.Fatalf("read content = %q, want every line numbered", got)
	}
}

func TestReadHandlerRanges(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "test.md", "one\ntwo\nthree\nfour\n")

	cases := []struct {
		name    string
		start   int
		end     int
		want    string
		wantErr string
	}{
		{name: "middle", start: 2, end: 3, want: "2\ttwo\n3\tthree\n"},
		{name: "open start", start: 0, end: 1, want: "1\tone\n"},
		{name: "open end", start: 3, end: 0, want: "3\tthree\n4\tfour\n"},
		{name: "end clamped", start: 1, end: 99, want: "1\tone\n2\ttwo\n3\tthree\n4\tfour\n"},
		{name: "beyond file", start: 9, end: 0, wantErr: "beyond the end of the file"},
		{name: "reversed", start: 4, end: 2, wantErr: "after end line"},
	}
	for _, tc := range cases {
		result := handleOne(t, handle, NewReadOption(1, "test.md", tc.start, tc.end))
		if tc.wantErr != "" {
			if result.OK || !strings.Contains(result.Err, tc.wantErr) {
				t.Fatalf("%s: read result = %+v, want error containing %q", tc.name, result, tc.wantErr)
			}
			continue
		}
		if !result.OK || result.Content != tc.want {
			t.Fatalf("%s: read result = %+v, want content %q", tc.name, result, tc.want)
		}
	}
}

func TestReadHandlerTruncatesLongFiles(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "big.md", numberedLines(3*MaxReadLines))

	first := handleOne(t, handle, NewReadOption(1, "big.md", 0, 0))
	if !first.OK {
		t.Fatalf("read result = %+v", first)
	}
	// One numbered line per source line, plus the note telling how to continue.
	page := splitLines(first.Content)
	if len(page) != MaxReadLines+1 {
		t.Fatalf("read returned %d lines, want %d numbered lines plus a note", len(page), MaxReadLines)
	}
	if want := fmt.Sprintf("%d\tline %d", MaxReadLines, MaxReadLines); page[MaxReadLines-1] != want {
		t.Fatalf("last numbered line = %q, want %q", page[MaxReadLines-1], want)
	}
	note := page[len(page)-1]
	if !strings.Contains(note, fmt.Sprintf(`"start": %d`, MaxReadLines+1)) {
		t.Fatalf("note = %q, want it to point at the next page", note)
	}

	second := handleOne(t, handle, NewReadOption(2, "big.md", MaxReadLines+1, 2*MaxReadLines))
	if !second.OK {
		t.Fatalf("second read result = %+v", second)
	}
	if want := fmt.Sprintf("%d\tline %d", MaxReadLines+1, MaxReadLines+1); !strings.HasPrefix(second.Content, want) {
		t.Fatalf("second page starts with %q, want %q", tail(second.Content), want)
	}
}

func TestReadHandlerEmptyAndMissingFiles(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "empty.md", "")

	empty := handleOne(t, handle, NewReadOption(1, "empty.md", 0, 0))
	if !empty.OK || empty.Content != "(empty file)" {
		t.Fatalf("read of an empty file = %+v", empty)
	}

	missing := handleOne(t, handle, NewReadOption(2, "nope.md", 0, 0))
	if missing.OK || !strings.Contains(missing.Err, "nope.md") {
		t.Fatalf("read of a missing file = %+v, want the path reported", missing)
	}

	noName := handleOne(t, handle, NewReadOption(3, "   ", 0, 0))
	if noName.OK || !strings.Contains(noName.Err, ErrEmptyFilename.Error()) {
		t.Fatalf("read without a filename = %+v", noName)
	}
}

func TestReadHandlerStripsCarriageReturns(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "crlf.md", "one\r\ntwo\r\n")

	result := handleOne(t, handle, NewReadOption(1, "crlf.md", 0, 0))
	if !result.OK {
		t.Fatalf("read result = %+v", result)
	}
	if got := result.Content; strings.Contains(got, "\r") || got != "1\tone\n2\ttwo\n" {
		t.Fatalf("read content = %q, want line terminators out of the payload", got)
	}
}

func TestReadHandlerSymlinkOutsideWorkspace(t *testing.T) {
	ws, handle := newWorkspace(t)
	outside := seed(t, Workspace{Root: t.TempDir()}, "secret.txt", "top secret\n")

	link := filepath.Join(ws.Root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result := handleOne(t, handle, NewReadOption(1, "link.txt", 0, 0))
	if result.OK || !strings.Contains(result.Err, ErrOutsideWorkspace.Error()) {
		t.Fatalf("read through an escaping symlink = %+v", result)
	}
}

// numberedLines builds a file with count lines, each labelled with its number.
func numberedLines(count int) string {
	var out strings.Builder
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&out, "line %d\n", i)
	}
	return out.String()
}

// tail returns the last line of a multi line payload.
func tail(content string) string {
	parts := splitLines(content)
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
