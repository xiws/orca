package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

const (
	maxReadBytes = 1 << 20
	maxReadLines = 2000
)

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func openRegular(root *os.Root, name string) (*os.File, error) {
	// Nonblocking open avoids hanging on a FIFO before the regular-file check.
	f, err := root.OpenFile(name, os.O_RDONLY|nonblock, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("only regular files are supported")
	}
	return f, nil
}

func (g *Gateway) read(ctx context.Context, call model.Call, args map[string]any) (Result, error) {
	name, err := g.relative(stringArg(args, "filename"))
	if err != nil {
		return Result{}, err
	}
	f, err := openRegular(g.root, name)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	start, end := intArg(args, "start", 1), intArg(args, "end", 0)
	h := sha256.New()
	r := bufio.NewReaderSize(io.TeeReader(contextReader{ctx, f}, h), 32<<10)
	var content strings.Builder
	line, total, last := 1, 0, 0
	truncated := false
	for {
		fragment, err := r.ReadSlice('\n')
		if len(fragment) > 0 {
			total = line
			if line >= start && (end == 0 || line <= end) {
				if line-start >= maxReadLines {
					truncated = true
				} else {
					n := min(len(fragment), maxReadBytes-content.Len())
					if n > 0 {
						content.Write(fragment[:n])
						last = line
					}
					if n < len(fragment) {
						truncated = true
					}
				}
			}
			if fragment[len(fragment)-1] == '\n' {
				line++
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return Result{}, err
		}
	}
	after, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return Result{}, fmt.Errorf("%w: file changed while reading; read it again", domain.ErrConflict)
	}
	return toolResult(call, map[string]any{"filename": name, "content": content.String(), "start": start, "end": last, "total_lines": total, "sha256": fmt.Sprintf("%x", h.Sum(nil)), "truncated": truncated}), nil
}

// baseline uses a pinned parent directory. Leaf symlinks are rejected for
// mutation, rather than silently replacing a link instead of its target.
func baseline(ctx context.Context, parent *os.Root, base, expected string, capture bool) ([]byte, os.FileMode, error) {
	info, err := parent.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		if expected == "absent" {
			return nil, 0644, nil
		}
		return nil, 0, fmt.Errorf("%w: file no longer exists", domain.ErrConflict)
	}
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("mutation target must be a regular file, not a symlink or special file")
	}
	if expected == "absent" {
		return nil, 0, fmt.Errorf("%w: file already exists", domain.ErrConflict)
	}
	f, err := openRegular(parent, base)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	h := sha256.New()
	var data []byte
	if capture {
		data, err = io.ReadAll(io.LimitReader(contextReader{ctx, f}, maxReadBytes+1))
		if len(data) > maxReadBytes {
			return nil, 0, fmt.Errorf("edit supports files up to 1 MiB; use write with a baseline hash")
		}
		_, _ = h.Write(data)
	} else {
		_, err = io.Copy(h, contextReader{ctx, f})
	}
	if err != nil {
		return nil, 0, err
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != expected {
		return nil, 0, fmt.Errorf("%w: SHA-256 baseline does not match; read the current file before retrying", domain.ErrConflict)
	}
	return data, info.Mode().Perm(), nil
}

func (g *Gateway) write(ctx context.Context, call model.Call, args map[string]any) (Result, error) {
	name, err := g.relative(stringArg(args, "filename"))
	if err != nil {
		return Result{}, err
	}
	if name == "." {
		return Result{}, fmt.Errorf("filename must not be the workspace directory")
	}
	if call.Name == "write" && stringArg(args, "expected_hash") == "absent" {
		if err := g.root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return Result{}, err
		}
	}
	// No resolved host pathname is used to read, write, or publish a file.
	parent, err := g.root.OpenRoot(filepath.Dir(name))
	if err != nil {
		return Result{}, err
	}
	defer parent.Close()
	base, expected := filepath.Base(name), stringArg(args, "expected_hash")
	old, mode, err := baseline(ctx, parent, base, expected, call.Name == "edit")
	if err != nil {
		return Result{}, err
	}
	content := []byte(stringArg(args, "content"))
	if call.Name == "edit" {
		needle := []byte(stringArg(args, "old_string"))
		if bytes.Count(old, needle) != 1 {
			return Result{}, fmt.Errorf("old_string must match exactly once; the file was not changed")
		}
		content = bytes.Replace(old, needle, []byte(stringArg(args, "new_string")), 1)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	temp := ".orca-tool-" + rand.Text()
	f, err := parent.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Result{}, err
	}
	defer parent.Remove(temp)
	_, writeErr := f.Write(content)
	if writeErr == nil {
		writeErr = f.Chmod(mode)
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return Result{}, err
	}
	// Recheck after staging the data. Gateway.mu spans both checks and the
	// publication. This is not a compare-and-swap against unrelated editors:
	// an external writer can still race the final check and rename.
	if _, _, err := baseline(ctx, parent, base, expected, false); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if expected == "absent" {
		// An atomic no-clobber publication: unlike Rename, Link cannot replace
		// a file an external editor created after the absence check.
		err = parent.Link(temp, base)
	} else {
		err = parent.Rename(temp, base)
	}
	if err != nil {
		return Result{}, err
	}
	return toolResult(call, map[string]any{"filename": name, "bytes": len(content), "sha256": fmt.Sprintf("%x", sha256.Sum256(content))}), nil
}
