package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// File modes used when a target does not exist yet.
const (
	dirMode  = 0o755
	fileMode = 0o644
)

// writeAll writes content to the resolved form of path and reports where the
// bytes ended up.
//
// The bytes first land in a sibling temporary file which is then renamed over
// the target, so a failing or interrupted write can never leave a half written
// file behind. Parent directories are created as needed and an existing file
// keeps its mode.
func writeAll(ws Workspace, path, content string) (string, error) {
	resolved, err := ws.Resolve(path)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", fmt.Errorf("create directory %s: %w", dir, err)
	}

	temp, err := os.CreateTemp(dir, ".orca-*")
	if err != nil {
		return "", fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	defer os.Remove(temp.Name())

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

// readAll returns the content of path as a string.
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

// splitLines breaks content into lines without their terminators. A trailing
// newline does not produce an extra empty line, and a CR kept from a CRLF file
// is dropped so line numbers stay readable.
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
