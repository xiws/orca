//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"

	"github.com/xiws/orca/internal/domain"
	"golang.org/x/sys/windows"
)

// lockRange 是 LockFileEx 需要的一个非空字节区间；锁文件不存放业务数据，
// 占用什么区间无所谓，只要所有进程用同一个区间即可保证互斥。
const lockRange = 1

// lockFile 对 f 的句柄加非阻塞排他锁，对应 Unix 侧的 flock(LOCK_EX|LOCK_NB)：
// Windows 没有 flock(2)，用 LockFileEx + LOCKFILE_FAIL_IMMEDIATELY 得到
// "立刻失败"的语义；锁被占用时返回 domain.ErrConflict。
// 调用方负责在返回错误时关闭 f。
func lockFile(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockRange, 0, new(windows.Overlapped))
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return fmt.Errorf("workspace is already locked: %w", domain.ErrConflict)
		}
		return err
	}
	return nil
}

// unlockFile 释放 lockFile 获取的字节区间锁。
func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockRange, 0, new(windows.Overlapped))
}

// privateFile 以安全方式打开或创建文件：用 FILE_FLAG_OPEN_REPARSE_POINT 阻止
// 跟随符号链接/junction，再用 GetFileInformationByHandle 验证文件是普通文件、
// 不是重解析点且硬链接数为 1，对应 Unix 侧的 O_NOFOLLOW + fstat 校验。
// Windows 用 ACL 而非 POSIX 权限位保护状态文件，因此这里不做 chmod。
func privateFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0)
	if err != nil {
		return nil, fmt.Errorf("open state file %s: %w", path, err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	// 目录、符号链接/junction 以及有额外硬链接的文件一律拒绝
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 || info.NumberOfLinks != 1 {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("state file must be a regular, unshared file: %s", path)
	}
	return os.NewFile(uintptr(h), path), nil
}
