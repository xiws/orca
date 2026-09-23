//go:build unix

package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/xiws/orca/internal/model"
)

const nonblock = syscall.O_NONBLOCK

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (g *Gateway) bash(ctx context.Context, call model.Call, args map[string]any) (Result, int, error) {
	name, err := g.relative(stringArg(args, "workdir"))
	if err != nil {
		return Result{}, -1, err
	}
	dir, err := g.root.OpenFile(name, os.O_RDONLY|nonblock, 0)
	if err != nil {
		return Result{}, -1, err
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		return Result{}, -1, err
	}
	if !info.IsDir() {
		return Result{}, -1, fmt.Errorf("workdir must be a directory")
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(intArg(args, "timeout", 60))*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/bin/bash", "--noprofile", "--norc", "-c", stringArg(args, "content"))
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "BASH_ENV=") || strings.HasPrefix(value, "ENV=") || strings.HasPrefix(value, "CDPATH=") {
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = time.Second
	out := &cappedOutput{}
	cmd.Stdout, cmd.Stderr = out, out
	err = startInDirectory(cmd, dir)
	if err == nil {
		err = cmd.Wait()
	}
	// Do not let background descendants outlive this tool, including when the
	// shell itself exits before its children close inherited output pipes.
	if cmd.Process != nil {
		_ = killGroup(cmd)
	}
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	data := map[string]any{"output": out.String(), "exit_code": exitCode, "truncated": out.Truncated(), "ok": err == nil}
	if err != nil {
		data["error"] = map[string]string{"code": "command_failed", "message": err.Error()}
	}
	r := toolResult(call, data)
	if runCtx.Err() != nil {
		return r, exitCode, runCtx.Err()
	}
	return r, exitCode, nil
}
