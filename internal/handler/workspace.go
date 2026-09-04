package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Workspace scopes file commands to a project directory. Commands may only
// touch files inside it; a path that resolves elsewhere, directly or through a
// symlink, is rejected before any file is opened.
type Workspace struct {
	// Root is the directory relative paths are resolved against and the only
	// tree file commands may modify. An empty Root falls back to the process
	// working directory.
	Root string
}

// Resolve turns a file name coming from a command into an absolute path.
//
// Absolute paths are used as they are, relative ones are joined with Root. The
// result is cleaned and, when Root is set, verified to stay inside it after
// symlinks of the nearest existing ancestor have been resolved, so neither
// ".." segments nor a symlinked file can point outside the workspace.
func (t Workspace) Resolve(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", ErrEmptyFilename
	}

	root, err := t.root()
	if err != nil {
		return "", err
	}

	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)

	canonical, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	// The root itself may live behind a symlink, as /tmp does on macOS, so both
	// sides have to be canonical before they are compared.
	canonicalRoot, err := canonicalPath(root)
	if err != nil {
		return "", err
	}
	if err := within(canonicalRoot, canonical); err != nil {
		return "", err
	}
	return path, nil
}

// root returns the absolute workspace root, falling back to the current working
// directory when none is configured.
func (t Workspace) root() (string, error) {
	if t.Root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Clean(cwd), nil
	}
	root, err := filepath.Abs(t.Root)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", t.Root, err)
	}
	return filepath.Clean(root), nil
}

// canonicalPath resolves symlinks for the deepest existing ancestor of path and
// reattaches the remainder, which lets a not yet created file be checked too.
func canonicalPath(path string) (string, error) {
	existing, missing := splitAtExisting(path)
	resolved := existing
	if existing != "" {
		full, err := filepath.EvalSymlinks(existing)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", existing, err)
		}
		resolved = full
	}
	return filepath.Join(resolved, missing), nil
}

// splitAtExisting walks up from path until it finds an entry that exists,
// returning that entry and the trailing segments below it.
func splitAtExisting(path string) (existing, missing string) {
	rest := path
	for rest != "" && rest != string(filepath.Separator) {
		if _, err := os.Lstat(rest); err == nil {
			return rest, strings.TrimPrefix(strings.TrimPrefix(path, rest), string(filepath.Separator))
		}
		parent := filepath.Dir(rest)
		if parent == rest {
			break
		}
		rest = parent
	}
	return "", path
}

// within reports whether path stays inside root once both are absolute and
// cleaned. Symlink escapes are handled by comparing canonical forms upstream.
func within(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s", ErrOutsideWorkspace, path)
	}
	return nil
}
