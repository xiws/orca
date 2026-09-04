package handler

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceResolve(t *testing.T) {
	root := t.TempDir()
	ws := Workspace{Root: root}

	cases := []struct {
		name string
		want string
	}{
		{name: "doc.md", want: filepath.Join(root, "doc.md")},
		{name: "nested/deep/file.go", want: filepath.Join(root, "nested/deep/file.go")},
		{name: "./same.md", want: filepath.Join(root, "same.md")},
		{name: "inside/../also.md", want: filepath.Join(root, "also.md")},
		{name: filepath.Join(root, "abs.md"), want: filepath.Join(root, "abs.md")},
		// The parent does not exist yet, which write is allowed to create.
		{name: "created/by/write.md", want: filepath.Join(root, "created/by/write.md")},
	}
	for _, tc := range cases {
		got, err := ws.Resolve(tc.name)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("Resolve(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWorkspaceRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	ws := Workspace{Root: root}

	outside := []string{
		"../sibling.txt",
		filepath.Join(filepath.Dir(root), "sibling.txt"),
		root + "/../sibling.txt",
		"/etc/passwd",
	}
	for _, name := range outside {
		if got, err := ws.Resolve(name); !errors.Is(err, ErrOutsideWorkspace) {
			t.Fatalf("Resolve(%q) = (%q, %v), want %v", name, got, err, ErrOutsideWorkspace)
		}
	}
}

func TestWorkspaceRejectsSymlinksThatEscape(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "secret.txt"), []byte("x"), fileMode); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(root, "escape")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ws := Workspace{Root: root}
	if _, err := ws.Resolve(filepath.Join("escape", "secret.txt")); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("Resolve() through an escaping symlink = %v, want %v", err, ErrOutsideWorkspace)
	}
	if _, err := ws.Resolve("escape/secret.txt"); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("Resolve() of a symlinked file = %v, want %v", err, ErrOutsideWorkspace)
	}
}

func TestWorkspaceSymlinkInsideRootIsAllowed(t *testing.T) {
	root := t.TempDir()
	ws := Workspace{Root: root}
	seed(t, ws, "real/target.txt", "x")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	got, err := ws.Resolve("link/target.txt")
	if err != nil {
		t.Fatalf("Resolve() inside the workspace error = %v", err)
	}
	if want := filepath.Join(root, "link/target.txt"); got != want {
		t.Fatalf("Resolve() = %q, want %q", got, want)
	}
}

func TestWorkspaceRequiresAFilename(t *testing.T) {
	ws := Workspace{Root: t.TempDir()}
	for _, name := range []string{"", "   ", "\t"} {
		if _, err := ws.Resolve(name); !errors.Is(err, ErrEmptyFilename) {
			t.Fatalf("Resolve(%q) error = %v, want %v", name, err, ErrEmptyFilename)
		}
	}
}

func TestWorkspaceFallsBackToTheWorkingDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Workspace{}.Resolve("README.md")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := filepath.Join(cwd, "README.md"); got != want {
		t.Fatalf("Resolve() = %q, want %q", got, want)
	}
}

func TestWorkspaceResolvesARelativeRoot(t *testing.T) {
	got, err := Workspace{Root: "."}.Resolve("a.md")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("Resolve() = %q, want an absolute path", got)
	}
}
