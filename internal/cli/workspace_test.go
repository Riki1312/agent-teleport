package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalWorkspace(t *testing.T) {
	root := t.TempDir()
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "project")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, want string }{
		{workspace, filepath.Join(physicalRoot, "project")},
		{alias, filepath.Join(physicalRoot, "project")},
		{filepath.Join(alias, "future", "child"), filepath.Join(physicalRoot, "project", "future", "child")},
	} {
		got, err := canonicalWorkspace(test.path)
		if err != nil || got != test.want {
			t.Fatalf("canonicalWorkspace(%q) = %q, %v; want %q", test.path, got, err, test.want)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, "future")); !os.IsNotExist(err) {
		t.Fatalf("canonicalization created a missing directory: %v", err)
	}
}

func TestCanonicalWorkspaceRejectsBrokenSymlink(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(root, "broken")
	if err := os.Symlink(filepath.Join(root, "missing"), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalWorkspace(filepath.Join(alias, "project")); err == nil {
		t.Fatal("broken workspace symlink was accepted")
	}
}

func TestCanonicalWorkspaceRejectsNonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(file, "project")} {
		if _, err := canonicalWorkspace(path); err == nil {
			t.Fatalf("non-directory workspace path %q was accepted", path)
		}
	}
}

func TestCanonicalWorkspaceRejectsSymlinkLoop(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	if err := os.Symlink(second, first); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(first, second); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalWorkspace(filepath.Join(first, "project")); err == nil {
		t.Fatal("workspace containing a symlink loop was accepted")
	}
}
