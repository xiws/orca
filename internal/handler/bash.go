package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/xiws/orca/internal/event"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/xiws/orca/pkg/command"
)

const (
	// DefaultBashTimeout 是 bash 命令在未指定其他限制时的运行秒数。
	DefaultBashTimeout = 60
	// MaxBashOutput 限制 bash 命令保留的输出字节数，防止嘈杂的命令淹没下一个提示。
	MaxBashOutput = 32 << 10
	// killGrace 是 bashHandler 在进程被杀死后等待输出管道排空的时间。
	killGrace = 5 * time.Second
)

// BashHandler 服务 bash 命令。
type BashHandler struct {
	Workspace
}

// Handle 在 shell 中运行命令行，返回其合并输出。
// 非零退出状态或超时会失败结果，但仍会报告命令已打印的内容。
func (t BashHandler) Handle(cmd command.CommandOption) (error, any) {
	opt, ok := cmd.(*BashOption)
	if !ok {
		return fmt.Errorf("%w: %T is not a %s option", ErrUnsupportedOption, cmd, CommandBash), nil
	}

	// 发布 before-event，命令本身作为 meta。
	publish(t.Publisher, event.NewToolBeforeEvent("bash", "", opt.Content, opt.Reasoning, opt.Id))

	output, exitCode, err := t.run(opt)

	// After-event：摘要携带原始命令，exitCode 用于 bash 特定渲染。
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

// workdir 解析命令运行的目录，默认为工作区根目录。
// 它仍受工作区的包含规则约束。
func (t BashHandler) workdir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return t.root()
	}
	return t.Resolve(dir)
}

// shellCommand 选择用于解释命令行的平台 shell。
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

// cappedWriter 收集最多 limit 字节的输出，并统计超出的部分，
// 以便调用方可以告诉模型输出被截断了。
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
