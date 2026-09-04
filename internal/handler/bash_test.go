package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBashHandlerRunsInAWorkspace(t *testing.T) {
	ws, handle := newWorkspace(t)
	seed(t, ws, "nested/keep.txt", "x")

	result := handleOne(t, handle, NewBashOption(1, "ls", "nested", 0))
	if !result.OK {
		t.Fatalf("bash result = %+v", result)
	}
	if strings.TrimSpace(result.Content) != "keep.txt" {
		t.Fatalf("output = %q, want the listing of the workdir", result.Content)
	}
}

func TestBashHandlerMergesStdoutAndStderr(t *testing.T) {
	_, handle := newWorkspace(t)

	result := handleOne(t, handle, NewBashOption(1, "echo out; echo err >&2", "", 0))
	if !result.OK {
		t.Fatalf("bash result = %+v", result)
	}
	if !strings.Contains(result.Content, "out") || !strings.Contains(result.Content, "err") {
		t.Fatalf("output = %q, want both streams", result.Content)
	}
}

func TestBashHandlerReportsExitStatusAndKeepsOutput(t *testing.T) {
	_, handle := newWorkspace(t)

	result := handleOne(t, handle, NewBashOption(1, "echo before; exit 3", "", 0))
	if result.OK {
		t.Fatalf("bash result = %+v, want a failure", result)
	}
	if !strings.Contains(result.Err, "exit status 3") {
		t.Fatalf("error = %q, want the exit code", result.Err)
	}
	if !strings.Contains(result.Content, "before") {
		t.Fatalf("output = %q, want what the command printed before failing", result.Content)
	}

	missing := handleOne(t, handle, NewBashOption(2, "orca-no-such-binary", "", 0))
	if missing.OK || missing.Err == "" {
		t.Fatalf("missing binary = %+v", missing)
	}
}

func TestBashHandlerTimesOut(t *testing.T) {
	_, handle := newWorkspace(t)

	started := time.Now()
	result := handleOne(t, handle, NewBashOption(1, "echo half way; sleep 30", "", 1))
	elapsed := time.Since(started)

	if result.OK || !strings.Contains(result.Err, "timed out after 1 seconds") {
		t.Fatalf("timeout result = %+v", result)
	}
	if !strings.Contains(result.Content, "half way") {
		t.Fatalf("output = %q, want the output produced before the timeout", result.Content)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("took %v, want the command killed at the one second deadline", elapsed)
	}
}

func TestBashHandlerKillsTheWholeProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signalling a process group is a unix concept")
	}
	_, handle := newWorkspace(t)

	// The shell records the pid of the child it started, so the test can check
	// afterwards that the timeout took the child down as well.
	marker := filepath.Join(t.TempDir(), "child.pid")
	result := handleOne(t, handle, NewBashOption(1,
		fmt.Sprintf(`sleep 30 & echo $! > %s; wait`, marker), "", 1))
	if result.OK || !strings.Contains(result.Err, "timed out") {
		t.Fatalf("background job result = %+v", result)
	}

	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the shell did not record its child: %v", err)
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err != nil || pid <= 0 {
		t.Fatalf("child pid %q is not a number", strings.TrimSpace(string(raw)))
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		alive := handleOne(t, handle, NewBashOption(2, fmt.Sprintf("kill -0 %d", pid), "", 5))
		if !alive.OK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d survived the timeout", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestBashHandlerTruncatesNoisyOutput(t *testing.T) {
	_, handle := newWorkspace(t)

	result := handleOne(t, handle, NewBashOption(1, "seq 1 20000", "", 0))
	if !result.OK {
		t.Fatalf("bash result = %+v", result)
	}
	if !strings.Contains(result.Content, "more bytes of output omitted") {
		t.Fatalf("output should be cut, got %d bytes with tail %q", len(result.Content), tail(result.Content))
	}
	if len(result.Content) > MaxBashOutput+len("\n... 0 more bytes of output omitted")+64 {
		t.Fatalf("output length = %d, want it capped at %d bytes", len(result.Content), MaxBashOutput)
	}
	if !strings.HasPrefix(result.Content, "1\n2\n") {
		t.Fatalf("output should keep its head, got %q", tail(result.Content))
	}
}

func TestBashHandlerValidatesInput(t *testing.T) {
	_, handle := newWorkspace(t)

	empty := handleOne(t, handle, NewBashOption(1, "   ", "", 0))
	if empty.OK || !strings.Contains(empty.Err, ErrEmptyCommand.Error()) {
		t.Fatalf("empty command = %+v", empty)
	}

	outside := handleOne(t, handle, NewBashOption(2, "pwd", t.TempDir(), 0))
	if outside.OK || !strings.Contains(outside.Err, ErrOutsideWorkspace.Error()) {
		t.Fatalf("workdir outside the workspace = %+v", outside)
	}
}

func TestBashHandlerReturnsOutputVerbatim(t *testing.T) {
	_, handle := newWorkspace(t)

	result := handleOne(t, handle, NewBashOption(1, "printf ok", "", 0))
	if !result.OK || result.Content != "ok" {
		t.Fatalf("bash result = %+v, want the output without a added terminator", result)
	}
}

func TestCappedWriterCountsDroppedBytes(t *testing.T) {
	out := &cappedWriter{limit: 8}
	n, err := out.Write([]byte("0123456789"))
	if n != 10 || err != nil {
		t.Fatalf("Write() = (%d, %v), want the caller to see no short write", n, err)
	}
	if out.String() != "01234567" || out.dropped != 2 {
		t.Fatalf("capped writer = (%q, %d), want the head plus 2 dropped bytes", out.String(), out.dropped)
	}

	// Writing beyond the limit stays harmless.
	if _, err := out.Write([]byte("more")); err != nil {
		t.Fatalf("Write() after the limit = %v", err)
	}
	if out.String() != "01234567" || out.dropped != 6 {
		t.Fatalf("capped writer = (%q, %d), want 6 dropped bytes", out.String(), out.dropped)
	}
}
