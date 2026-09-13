//go:build windows

package handler

import (
	"os/exec"
	"syscall"
)

// createNewProcessGroup 将 shell 从 orca 进程的控制台组中分离，
// 镜像 unix 上 Setpgid 的行为。
const createNewProcessGroup = 0x00000200

// groupAttr 在 shell 自己的进程组中启动。
func groupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// killGroup 杀死 shell。Windows 没有组信号，因此 shell 启动的命令
// 可能在等待的宽限期内继续存活。
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
