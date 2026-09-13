//go:build !windows

package handler

import (
	"os/exec"
	"syscall"
)

// groupAttr 在 shell 自己的进程组中启动，以便后续可以将整个组作为单元发信号。
func groupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killGroup 将 shell 及其产生的所有进程一起杀死。
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// Shell 已经消失，只能尝试杀死它自身。
		return cmd.Process.Kill()
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
