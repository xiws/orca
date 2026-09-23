//go:build darwin

package tools

import (
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

func startInDirectory(cmd *exec.Cmd, dir *os.File) error {
	done := make(chan error, 1)
	go func() {
		// Darwin's /dev/fd entries cannot be used as directory paths. Its
		// thread-local fchdir lets fork inherit the opened directory without
		// changing the process-wide cwd or resolving another host pathname.
		runtime.LockOSThread()
		// Deliberately do not unlock: when this goroutine exits Go retires the
		// OS thread, so no unrelated goroutine can inherit its private cwd.
		if err := unix.PthreadFchdir(int(dir.Fd())); err != nil {
			done <- err
			return
		}
		done <- cmd.Start()
	}()
	return <-done
}
