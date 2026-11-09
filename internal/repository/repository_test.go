package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Riki1312/agent-teleport/internal/archive"
)

func testRepository(t *testing.T) Repository {
	t.Helper()
	root := t.TempDir()
	command := exec.Command("git", "init", "--quiet", "--initial-branch", "main", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	nested := filepath.Join(root, "nested", "dir")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	repo, err := Find(context.Background(), nested)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func savedArchive(t *testing.T, workspace string) archive.Archive {
	t.Helper()
	created, err := archive.New(archive.Header{
		Provider: "anthropic-claude", ProviderVersion: "2.1.319", SessionID: "6f5fb979-80ed-48da-8688-9ee51a3bd400",
		Workspace: workspace, Home: Home, CreatedAt: time.Now(),
	}, []archive.Payload{{Path: "session.jsonl", Data: []byte("{}\n")}})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestFindReturnsTheCanonicalRootOfANestedDirectory(t *testing.T) {
	repo := testRepository(t)

	// when, then
	if _, err := os.Stat(filepath.Join(repo.Root(), ".git")); err != nil {
		t.Fatalf("Root() = %s: %v", repo.Root(), err)
	}
	if resolved, _ := filepath.EvalSymlinks(repo.Root()); resolved != repo.Root() {
		t.Fatalf("Root() = %s is not canonical", repo.Root())
	}
}

func TestStateOfARepositoryWithoutCommits(t *testing.T) {
	// when
	state := testRepository(t).State(context.Background())

	// then
	if state.Commit != "" || state.Branch != "main" {
		t.Fatalf("State() = %+v", state)
	}
}

func TestStoreAndLoadSavedSessions(t *testing.T) {
	repo := testRepository(t)
	for _, name := range []string{"second", "first"} {
		if err := repo.Store(name, savedArchive(t, Workspace+"/nested")); err != nil {
			t.Fatal(err)
		}
	}

	// when
	saved, err := repo.Sessions()

	// then
	if err != nil || len(saved) != 2 || saved[0].Name != "first" || saved[1].Name != "second" || saved[0].Err != nil {
		t.Fatalf("Sessions() = %+v, %v", saved, err)
	}
	attributes, err := os.ReadFile(filepath.Join(repo.Root(), Directory, ".gitattributes"))
	if err != nil || !strings.Contains(string(attributes), "sessions/** -text linguist-generated") {
		t.Fatalf("attributes = %q, %v", attributes, err)
	}
}

func TestStoreAndLoadRequireRepositoryRelativePaths(t *testing.T) {
	repo := testRepository(t)
	absolute := savedArchive(t, "/Users/someone/project")
	if err := repo.Store("absolute", absolute); err == nil {
		t.Fatal("Store() saved machine paths")
	}
	if err := absolute.WriteDir(filepath.Join(repo.Root(), Directory, "sessions", "absolute")); err != nil {
		t.Fatal(err)
	}

	// when
	_, err := repo.Load("absolute")

	// then
	if err == nil || !strings.Contains(err.Error(), "repository-relative") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestValidateName(t *testing.T) {
	for _, name := range []string{"fix-login", "v1.2_notes", "a"} {
		if err := ValidateName(name); err != nil {
			t.Fatalf("ValidateName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", ".hidden", "Upper", "a/b", "..", strings.Repeat("a", 65)} {
		if err := ValidateName(name); err == nil {
			t.Fatalf("ValidateName(%q) succeeded", name)
		}
	}
}
