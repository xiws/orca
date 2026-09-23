//go:build unix

package tools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xiws/orca/internal/domain"
)

func TestBashDirectoryOutputAndExit(t *testing.T) {
	f := setup(t, true)
	if err := os.Mkdir(filepath.Join(f.dir, "sub dir"), 0755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(f.dir, "sub dir", "marker"), "opened-directory")
	c := call("bash", "cwd", map[string]any{"content": "printf '%s' \"$(<marker)\"", "workdir": "sub dir", "timeout": 5})
	m := requireOK(t, f.exec(t, c, "cwd"))
	if m["output"] != "opened-directory" || m["exit_code"] != float64(0) {
		t.Fatalf("cwd: %+v", m)
	}
	c = call("bash", "large-output", map[string]any{"content": "printf '%070000d' 0; printf 'stderr' >&2"})
	m = requireOK(t, f.exec(t, c, "large-output"))
	if len(m["output"].(string)) != maxBashOutput || m["truncated"] != true {
		t.Fatalf("output bound: %d, %v", len(m["output"].(string)), m["truncated"])
	}
	c = call("bash", "exit", map[string]any{"content": "printf 'kept output'; exit 7"})
	r := f.exec(t, c, "exit")
	requireError(t, r, "command_failed")
	m = body(t, r)
	if m["output"] != "kept output" || m["exit_code"] != float64(7) {
		t.Fatalf("failed command result: %s", r.Content)
	}
	if f.l.tools["exit"].ExitCode != 7 {
		t.Fatal("exit status not persisted")
	}
}

func TestBashWorkdirEscapeAndPreparedFailures(t *testing.T) {
	f := setup(t, true)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(f.dir, "escape")); err != nil {
		t.Fatal(err)
	}
	for i, dir := range []string{"../", outside, "escape"} {
		c := call("bash", fmt.Sprint(i), map[string]any{"content": "printf bad > marker", "workdir": dir})
		requireError(t, f.exec(t, c, fmt.Sprint(i)), "")
	}
	requireAbsent(t, filepath.Join(outside, "marker"))
	for fail := 1; fail <= 2; fail++ {
		f := setup(t, true)
		f.l.failAt = fail
		c := call("bash", "fault", map[string]any{"content": "printf bad > marker"})
		if _, err := f.g.Execute(context.Background(), f.run, f.inv, c, "fault"); !errors.Is(err, errLedger) {
			t.Fatalf("failure: %v", err)
		}
		requireAbsent(t, filepath.Join(f.dir, "marker"))
	}
}

func TestBashCompletedDoesNotReplay(t *testing.T) {
	f := setup(t, true)
	c := call("bash", "once", map[string]any{"content": "printf x >> count; printf result"})
	r := f.exec(t, c, "once")
	requireOK(t, r)
	if again := f.exec(t, c, "once"); again.Content != r.Content {
		t.Fatal("completed bash was not reused")
	}
	requireFile(t, filepath.Join(f.dir, "count"), "x")
}

func TestBashCancellationKillsProcessGroup(t *testing.T) {
	f := setup(t, true)
	fifo := filepath.Join(f.dir, "ready")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	pipe, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	ready := make(chan string, 1)
	go func() { line, _ := bufio.NewReader(pipe).ReadString('\n'); ready <- strings.TrimSpace(line) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := call("bash", "cancel-group", map[string]any{"content": "sleep 30 & child=$!; printf '%s\\n' \"$child\" > ready; wait", "timeout": 120})
	done := make(chan error, 1)
	go func() { _, err := f.g.Execute(ctx, f.run, f.inv, c, "cancel-group"); done <- err }()
	var pid int
	select {
	case line := <-ready:
		pid, err = strconv.Atoi(line)
		if err != nil {
			t.Fatalf("child pid %q: %v", line, err)
		}
	case err := <-done:
		t.Fatalf("command stopped before readiness: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("command failed to become ready")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, domain.ErrUnknown) || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process group did not terminate promptly")
	}
	// A killed child may temporarily remain as a zombie on Unix. It must not
	// remain an executing process after the tool returns.
	status, psErr := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if psErr == nil && strings.TrimSpace(string(status)) != "" && !strings.HasPrefix(strings.TrimSpace(string(status)), "Z") {
		t.Fatalf("child %d still alive: %q", pid, status)
	}
	if f.l.tools["cancel-group"].State != "unknown" {
		t.Fatalf("cancellation not recorded: %+v", f.l.tools["cancel-group"])
	}
	if _, err := f.g.Execute(context.Background(), f.run, f.inv, c, "cancel-group"); !errors.Is(err, domain.ErrUnknown) {
		t.Fatalf("cancelled bash replayed: %v", err)
	}
}

func TestBashTimeoutIsUnknownAndBounded(t *testing.T) {
	f := setup(t, true)
	start := time.Now()
	c := call("bash", "timeout", map[string]any{"content": "sleep 30", "timeout": 1})
	_, err := f.g.Execute(context.Background(), f.run, f.inv, c, "timeout")
	if !errors.Is(err, domain.ErrUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not terminate promptly")
	}
	if f.l.tools["timeout"].State != "unknown" {
		t.Fatal("timeout outcome not recorded")
	}
}

func TestReadRejectsFIFOBeforeBlocking(t *testing.T) {
	f := setup(t, true)
	if err := syscall.Mkfifo(filepath.Join(f.dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	r := f.exec(t, call("read", "fifo", map[string]any{"filename": "pipe"}), "fifo")
	requireError(t, r, "execution_failed")
}
