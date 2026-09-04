//go:build windows

package handler

import (
	"os/exec"
	"syscall"
)

// createNewProcessGroup detaches the shell from the console group of the orca
// process, mirroring what Setpgid does on unix.
const createNewProcessGroup = 0x00000200

// groupAttr starts the shell in its own process group.
func groupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// killGroup kills the shell. Windows has no group signal, so commands launched
// by the shell may outlive it by the grace period of the wait.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
