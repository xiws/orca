//go:build unix

// Unix 平台的 bash 工具实现，支持进程组管理和非阻塞 I/O。
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

// killGroup 终止整个进程组，确保子进程和孙进程都被清理
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

// bash 在 Unix 平台上执行 shell 命令，使用独立进程组和超时控制
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
	// 创建带超时的上下文，默认超时 60 秒
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(intArg(args, "timeout", 60))*time.Second)
	defer cancel()
	// 创建 bash 子进程，不加载配置文件
	cmd := exec.CommandContext(runCtx, "/bin/bash", "--noprofile", "--norc", "-c", stringArg(args, "content"))
	// 继承父进程环境变量，但过滤掉可能影响安全性的变量
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "BASH_ENV=") || strings.HasPrefix(value, "ENV=") || strings.HasPrefix(value, "CDPATH=") {
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	// 设置独立进程组，便于整体终止
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = time.Second // 限制 Wait 等待子进程退出的时间
	out := &cappedOutput{}
	cmd.Stdout, cmd.Stderr = out, out
	err = startInDirectory(cmd, dir) // 在指定目录中启动命令
	if err == nil {
		err = cmd.Wait()
	}
	// 确保后台子进程不会在本工具返回后继续存活
	if cmd.Process != nil {
		_ = killGroup(cmd)
	}
	// 解析退出码
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
