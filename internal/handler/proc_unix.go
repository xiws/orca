//go:build !windows

package handler

import (
	"os/exec"
	"syscall"
)

// groupAttr starts the shell in its own process group, so the group can be
// signalled as a unit later on.
func groupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the shell together with everything it spawned.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// The shell is already gone, only itself can still be reaped.
		return cmd.Process.Kill()
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
