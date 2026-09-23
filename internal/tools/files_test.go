package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestReadBoundsAndFullHash(t *testing.T) {
	f := setup(t, true)
	content := strings.Repeat("line\n", 2500)
	mustWrite(t, filepath.Join(f.dir, "lines"), content)
	r := f.exec(t, call("read", "read-lines", map[string]any{"filename": "lines"}), "read-lines")
	m := requireOK(t, r)
	if m["sha256"] != sum(content) || m["end"] != float64(2000) || m["truncated"] != true || len(strings.Split(strings.TrimSuffix(m["content"].(string), "\n"), "\n")) != 2000 {
		t.Fatalf("line bounds/hash: %s", r.Content)
	}
	r = f.exec(t, call("read", "read-range", map[string]any{"filename": filepath.Join(f.dir, "lines"), "start": 2200, "end": 2201}), "read-range")
	m = requireOK(t, r)
	if m["content"] != "line\nline\n" || m["start"] != float64(2200) || m["end"] != float64(2201) || m["truncated"] != false || m["sha256"] != sum(content) {
		t.Fatalf("range: %s", r.Content)
	}
	large := strings.Repeat("x", maxReadBytes+10)
	mustWrite(t, filepath.Join(f.dir, "large"), large)
	r = f.exec(t, call("read", "read-bytes", map[string]any{"filename": "large"}), "read-bytes")
	m = requireOK(t, r)
	if len(m["content"].(string)) != maxReadBytes || m["truncated"] != true || m["sha256"] != sum(large) {
		t.Fatal("byte bound or full-file hash is incorrect")
	}
	mustWrite(t, filepath.Join(f.dir, "empty"), "")
	m = requireOK(t, f.exec(t, call("read", "empty", map[string]any{"filename": "empty"}), "empty"))
	if m["sha256"] != sum("") || m["content"] != "" || m["total_lines"] != float64(0) {
		t.Fatalf("empty file: %+v", m)
	}
}

func TestWriteEditBaselineAndAtomicPublication(t *testing.T) {
	f := setup(t, true)
	name := filepath.Join(f.dir, "x")
	requireOK(t, f.exec(t, call("write", "create", writeArgs("x", "old text", "absent")), "create"))
	requireError(t, f.exec(t, call("write", "exists", writeArgs("x", "bad", "absent")), "exists"), "conflict")
	requireFile(t, name, "old text")
	mustWrite(t, name, "user text")
	requireError(t, f.exec(t, call("write", "stale-write", writeArgs("x", "bad", sum("old text"))), "stale-write"), "conflict")
	edits := map[string]any{"filename": "x", "old_string": "text", "new_string": "replacement", "expected_hash": sum("old text")}
	requireError(t, f.exec(t, call("edit", "stale-edit", edits), "stale-edit"), "conflict")
	requireFile(t, name, "user text")
	edits["expected_hash"] = sum("user text")
	if err := os.Chmod(name, 0600); err != nil {
		t.Fatal(err)
	}
	m := requireOK(t, f.exec(t, call("edit", "edit", edits), "edit"))
	if m["sha256"] != sum("user replacement") {
		t.Fatalf("edit hash: %+v", m)
	}
	requireFile(t, name, "user replacement")
	info, err := os.Stat(name)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode not preserved: %v %v", info, err)
	}
	requireOK(t, f.exec(t, call("write", "overwrite", writeArgs("x", "final", sum("user replacement"))), "overwrite"))
	requireFile(t, name, "final")
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "x" {
		t.Fatalf("temporary files leaked: %v", entries)
	}
	for i, old := range []string{"missing", "a"} {
		mustWrite(t, name, "a a")
		r := f.exec(t, call("edit", fmt.Sprint(i), map[string]any{"filename": "x", "old_string": old, "new_string": "b", "expected_hash": sum("a a")}), "bad-edit-"+fmt.Sprint(i))
		requireError(t, r, "execution_failed")
		requireFile(t, name, "a a")
	}
}

func TestPathsAndSymlinkBoundaries(t *testing.T) {
	f := setup(t, true)
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "secret"), "untouched")
	for i, filename := range []string{"../secret", "a/../secret", filepath.Join(outside, "secret"), "x\x00y"} {
		for _, tool := range []string{"read", "write"} {
			args := map[string]any{"filename": filename}
			if tool == "write" {
				args = writeArgs(filename, "bad", "absent")
			}
			requireError(t, f.exec(t, call(tool, fmt.Sprint(i), args), fmt.Sprintf("%s-%d", tool, i)), "invalid_path")
		}
	}
	if f.l.applies != 0 {
		t.Fatal("lexical traversal created records")
	}
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privileges on Windows")
	}
	if err := os.Symlink(outside, filepath.Join(f.dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(f.dir, "leaf")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"escape/secret", "leaf"} {
		requireError(t, f.exec(t, call("read", "read-"+name, map[string]any{"filename": name}), "read-"+name), "execution_failed")
		requireError(t, f.exec(t, call("write", "write-"+name, writeArgs(name, "bad", sum("untouched"))), "write-"+name), "execution_failed")
	}
	requireError(t, f.exec(t, call("write", "escape-new", writeArgs("escape/new", "bad", "absent")), "escape-new"), "execution_failed")
	requireAbsent(t, filepath.Join(outside, "new"))
	requireFile(t, filepath.Join(outside, "secret"), "untouched")
	mustWrite(t, filepath.Join(f.dir, "inside"), "safe")
	if err := os.Symlink("inside", filepath.Join(f.dir, "inside-link")); err != nil {
		t.Fatal(err)
	}
	requireOK(t, f.exec(t, call("read", "inside-link", map[string]any{"filename": "inside-link"}), "inside-link"))
	requireError(t, f.exec(t, call("write", "link-write", writeArgs("inside-link", "bad", sum("safe"))), "link-write"), "execution_failed")
	requireFile(t, filepath.Join(f.dir, "inside"), "safe")
}

func TestOpenedRootSurvivesPathReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open directory rename differs on Windows")
	}
	f := setup(t, true)
	workspace, moved, outside := filepath.Join(f.dir, "workspace"), filepath.Join(f.dir, "moved"), filepath.Join(f.dir, "outside")
	for _, dir := range []string{workspace, outside} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(workspace, "x"), "original root")
	mustWrite(t, filepath.Join(outside, "x"), "outside")
	g, err := New(workspace, f.l)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if err := os.Rename(workspace, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, workspace); err != nil {
		t.Fatal(err)
	}
	f.run.Workspace = workspace
	r, err := g.Execute(context.Background(), f.run, f.inv, call("read", "pinned", map[string]any{"filename": "x"}), "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if requireOK(t, r)["content"] != "original root" {
		t.Fatalf("used replaced host pathname: %s", r.Content)
	}
	r, err = g.Execute(context.Background(), f.run, f.inv, call("write", "pinned-write", writeArgs("x", "changed inside", sum("original root"))), "pinned-write")
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, r)
	requireFile(t, filepath.Join(outside, "x"), "outside")
	requireFile(t, filepath.Join(moved, "x"), "changed inside")
}

func TestConcurrentCallsSerializeBaselines(t *testing.T) {
	f := setup(t, true)
	mustWrite(t, filepath.Join(f.dir, "x"), "baseline")
	const count = 16
	results := make(chan Result, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := call("write", fmt.Sprint(i), writeArgs("x", fmt.Sprint(i), sum("baseline")))
			r, err := f.g.Execute(context.Background(), f.run, f.inv, c, fmt.Sprint(i))
			results <- r
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ok := 0
	for r := range results {
		if body(t, r)["ok"] == true {
			ok++
		} else {
			requireError(t, r, "conflict")
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent writes accepted the same baseline", ok)
	}
	if len(f.l.history) != 3*count {
		t.Fatal("missing ledger transitions")
	}
	for i := 0; i < len(f.l.history); i += 3 {
		if strings.Join(f.l.history[i:i+3], ",") != "prepared,started,completed" {
			t.Fatalf("interleaved tools: %v", f.l.history)
		}
	}
}

func TestConcurrentSameKeyExecutesOnce(t *testing.T) {
	f := setup(t, true)
	c := call("write", "same", writeArgs("x", "only once", "absent"))
	var wg sync.WaitGroup
	results := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := f.g.Execute(context.Background(), f.run, f.inv, c, "same")
			if err != nil {
				results <- err.Error()
				return
			}
			results <- r.Content
		}()
	}
	wg.Wait()
	close(results)
	var expected string
	for result := range results {
		if !json.Valid([]byte(result)) {
			t.Fatal(result)
		}
		if expected == "" {
			expected = result
		}
		if result != expected {
			t.Fatal("same key returned different results")
		}
	}
	if f.l.applies != 3 {
		t.Fatalf("same-key replay: %v", f.l.history)
	}
}
