package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"orca/internal/event"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"orca/pkg/command"
)

const (
	// DefaultBashTimeout is how many seconds a bash command may run when it does
	// not ask for a different limit.
	DefaultBashTimeout = 60
	// MaxBashOutput caps how many output bytes a bash command keeps, so a noisy
	// command cannot flood the next prompt.
	MaxBashOutput = 32 << 10
	// killGrace is how long bashHandler waits for the output pipes to drain after
	// the process has been killed.
	killGrace = 5 * time.Second
)

// BashHandler serves the bash command.
type BashHandler struct {
	Workspace
}

// Handle runs a command line in a shell, returning its merged output. A non
// zero exit status or a timeout fails the result but still reports whatever the
// command managed to print.
func (t BashHandler) Handle(cmd command.CommandOption) (error, any) {
	opt, ok := cmd.(*BashOption)
	if !ok {
		return fmt.Errorf("%w: %T is not a %s option", ErrUnsupportedOption, cmd, CommandBash), nil
	}

	// Publish before-event with the command itself as meta.
	publish(t.Publisher, event.NewToolBeforeEvent("bash", "", opt.Content, opt.Reasoning, opt.Id))

	output, exitCode, err := t.run(opt)

	// After-event: summary carries the original command, exitCode for bash-specific rendering.
	publish(t.Publisher, event.NewToolAfterEvent("bash", "", output, err == nil, opt.Content, exitCode, opt.Id))

	return nil, ResultFor(opt, output, err)
}

func (t BashHandler) run(opt *BashOption) (string, int, error) {
	if strings.TrimSpace(opt.Content) == "" {
		return "", 0, ErrEmptyCommand
	}

	if err := validateCommand(opt.Content); err != nil {
		return "", 0, err
	}

	dir, err := t.workdir(opt.Workdir)
	if err != nil {
		return "", 0, err
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = DefaultBashTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	shell, shellArgs := shellCommand(opt.Content)

	cmd := exec.CommandContext(ctx, shell, shellArgs...)
	cmd.Dir = dir
	cmd.SysProcAttr = groupAttr()
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = killGrace

	out := &cappedWriter{limit: MaxBashOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	runErr := cmd.Run()

	report := out.String()
	if out.dropped > 0 {
		report = fmt.Sprintf("%s\n... %d more bytes of output omitted", report, out.dropped)
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return report, -1, fmt.Errorf("timed out after %d seconds", timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return report, exitErr.ExitCode(), fmt.Errorf("exit status %d", exitErr.ExitCode())
	}
	if runErr != nil {
		return report, -1, runErr
	}
	return report, 0, nil
}

// workdir resolves the directory the command runs in, defaulting to the root of
// the workspace. It stays subject to the containment rules of the workspace.
func (t BashHandler) workdir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return t.root()
	}
	return t.Resolve(dir)
}

// shellCommand picks the platform shell used to interpret a command line.
func shellCommand(line string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", line}
	}
	shell := "/bin/bash"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}
	return shell, []string{"-c", line}
}

// cappedWriter collects output up to limit bytes and counts everything beyond
// it, so callers can tell the model that output was dropped.
type cappedWriter struct {
	buf     bytes.Buffer
	limit   int
	dropped int
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	remaining := c.limit - c.buf.Len()
	if len(p) > remaining {
		if remaining > 0 {
			c.buf.Write(p[:remaining])
		}
		c.dropped += len(p) - max(remaining, 0)
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

func (c *cappedWriter) String() string { return c.buf.String() }
