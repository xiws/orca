//go:build darwin

// macOS 平台使用线程级 fchdir 设置子进程工作目录，
// 因为 Darwin 的 /dev/fd 不能作为目录路径使用。
package tools

import (
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

// startInDirectory 在新 goroutine 中使用线程级 fchdir 设置工作目录后启动命令。
// goroutine 退出时 Go 会回收该 OS 线程，避免其他 goroutine 继承被修改的 cwd。
func startInDirectory(cmd *exec.Cmd, dir *os.File) error {
	done := make(chan error, 1)
	go func() {
		// 锁定 OS 线程以使用线程级 fchdir，不解锁：
		// goroutine 退出时 Go 回收线程，避免其他 goroutine 继承被修改的 cwd
		runtime.LockOSThread()
		if err := unix.PthreadFchdir(int(dir.Fd())); err != nil {
			done <- err
			return
		}
		done <- cmd.Start()
	}()
	return <-done
}
