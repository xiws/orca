//go:build unix && !darwin

package tools

import (
	"os"
	"os/exec"
)

// startInDirectory 通过文件描述符 3 传递已打开的目录，在目标目录中启动命令。
// 使用 /dev/fd/3 而非路径名，避免 TOCTOU 竞态条件。
func startInDirectory(cmd *exec.Cmd, dir *os.File) error {
	command := cmd.Args[len(cmd.Args)-1]
	// FD 3 是已打开的目录句柄，而非执行前解析的路径名。
	// 在不支持目录能力型 /dev/fd 的系统上，直接失败而不回退到
	// 存在竞态风险的解析路径。
	cmd.Args = []string{cmd.Path, "--noprofile", "--norc", "-c", `cd -P -- /dev/fd/3 || exit 125
exec 3<&-
exec /bin/bash --noprofile --norc -c "$1"`, "orca-tools", command}
	cmd.ExtraFiles = []*os.File{dir}
	return cmd.Start()
}
