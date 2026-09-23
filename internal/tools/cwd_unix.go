//go:build unix && !darwin

package tools

import (
	"os"
	"os/exec"
)

func startInDirectory(cmd *exec.Cmd, dir *os.File) error {
	command := cmd.Args[len(cmd.Args)-1]
	// FD 3 is the opened directory, not a pathname resolved before execution.
	// On systems without directory-capable /dev/fd, fail closed before running
	// the requested command; never fall back to a race-prone resolved path.
	cmd.Args = []string{cmd.Path, "--noprofile", "--norc", "-c", `cd -P -- /dev/fd/3 || exit 125
exec 3<&-
exec /bin/bash --noprofile --norc -c "$1"`, "orca-tools", command}
	cmd.ExtraFiles = []*os.File{dir}
	return cmd.Start()
}
