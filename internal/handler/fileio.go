package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 目标不存在时使用的文件模式。
const (
	dirMode  = 0o755
	fileMode = 0o644
)

// writeAll 将内容写入 path 的解析形式，并报告字节最终位置。
//
// 字节先落入同级临时文件，然后重命名覆盖目标，
// 因此失败或中断的写入不会留下半写文件。
// 根据需要创建父目录，已有文件保留其模式。
func writeAll(ws Workspace, path, content string) (string, error) {
	resolved, err := ws.Resolve(path)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", fmt.Errorf("create directory %s: %w", dir, err)
	}

	// 使用临时文件 + 原子重命名，避免写入中断导致数据损坏。
	temp, err := os.CreateTemp(dir, ".orca-*")
	if err != nil {
		return "", fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	defer os.Remove(temp.Name())

	// 保留已有文件的权限模式，新文件使用默认模式。
	mode := os.FileMode(fileMode)
	if info, err := os.Stat(resolved); err == nil {
		mode = info.Mode().Perm()
	}

	if _, err := temp.WriteString(content); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("write %s: %w", resolved, err)
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return "", fmt.Errorf("chmod %s: %w", resolved, err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", temp.Name(), err)
	}
	if err := os.Rename(temp.Name(), resolved); err != nil {
		return "", fmt.Errorf("replace %s: %w", resolved, err)
	}
	return resolved, nil
}

// readAll 将 path 的内容作为字符串返回。
func readAll(ws Workspace, path string) (string, error) {
	resolved, err := ws.Resolve(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// splitLines 将内容拆分为不带终止行符的行。尾部换行不产生额外空行，
// CRLF 文件中保留的 CR 会被丢弃，使行号保持可读。
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines
}
