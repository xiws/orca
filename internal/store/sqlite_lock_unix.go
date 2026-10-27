//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"

	"github.com/xiws/orca/internal/domain"
	"golang.org/x/sys/unix"
)

// lockFile 对 f 加非阻塞排他 flock，语义等价于 flock(LOCK_EX|LOCK_NB)：
// 锁已被其他进程（或同进程的另一个打开实例）持有时立刻失败并返回
// domain.ErrConflict。调用方负责在返回错误时关闭 f。
func lockFile(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return fmt.Errorf("workspace is already locked: %w", domain.ErrConflict)
		}
		return err
	}
	return nil
}

// unlockFile 释放 lockFile 获取的 flock。锁本身由 Close 关闭描述符时兜底释放，
// 这里显式释放以便 Close 能拿到解锁错误。
func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

// privateFile 以安全方式打开或创建文件：使用 O_NOFOLLOW 防止符号链接，
// fstat 验证文件为普通文件且硬链接数为 1，避免对凭据文件等的意外操作。
func privateFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, fmt.Errorf("open state file %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		f.Close()
		return nil, err
	}
	// 验证文件为普通文件且无共享硬链接
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("state file must be a regular, unshared file: %s", path)
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
