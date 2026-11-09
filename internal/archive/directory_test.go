package archive

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDirRoundTripsAndReplacesAnEarlierArchive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	first := testArchive(t, Payload{Path: "a.jsonl", Data: []byte("one\n")}, Payload{Path: "sub/b.txt", Data: []byte("b")})
	second := testArchive(t, Payload{Path: "a.jsonl", Data: []byte("one\ntwo\n")})
	if err := first.WriteDir(dir); err != nil {
		t.Fatal(err)
	}

	// when
	err := second.WriteDir(dir)

	// then
	if err != nil {
		t.Fatal(err)
	}
	read, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !read.equal(second) {
		t.Fatalf("ReadDir() = %+v", read.Payloads)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "b.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the earlier archive's file remains: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(dir))
	if len(entries) != 1 {
		t.Fatalf("WriteDir left %d entries beside the archive", len(entries))
	}
}

func TestReadDirRejectsChangedAndUnlistedFiles(t *testing.T) {
	for name, change := range map[string]func(dir string) error{
		"changed payload": func(dir string) error { return os.WriteFile(filepath.Join(dir, "a.jsonl"), []byte("two\n"), 0o600) },
		"unlisted file":   func(dir string) error { return os.WriteFile(filepath.Join(dir, "extra.txt"), nil, 0o600) },
		"missing payload": func(dir string) error { return os.Remove(filepath.Join(dir, "a.jsonl")) },
		"linked payload": func(dir string) error {
			path := filepath.Join(dir, "a.jsonl")
			if err := os.Rename(path, path+".real"); err != nil {
				return err
			}
			return os.Symlink(path+".real", path)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "session")
			if err := testArchive(t, Payload{Path: "a.jsonl", Data: []byte("one\n")}).WriteDir(dir); err != nil {
				t.Fatal(err)
			}
			if err := change(dir); err != nil {
				t.Fatal(err)
			}

			// when
			_, err := ReadDir(dir)

			// then
			if err == nil {
				t.Fatal("ReadDir() accepted a modified archive directory")
			}
		})
	}
}

func TestRecoverDirRestoresAnArchiveMovedAsideByAnInterruptedWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")
	if err := testArchive(t, Payload{Path: "a.jsonl", Data: []byte("one\n")}).WriteDir(dir); err != nil {
		t.Fatal(err)
	}
	stage, previous := siblingDirs(dir)
	if err := os.Rename(dir, previous); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}

	// when
	err := RecoverDir(dir)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDir(dir); err != nil {
		t.Fatalf("the archive was not restored: %v", err)
	}
	for _, leftover := range []string{stage, previous} {
		if _, err := os.Stat(leftover); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s remains: %v", filepath.Base(leftover), err)
		}
	}
}

func TestManifestRecordsRepositoryStateOnlyWhenSet(t *testing.T) {
	packed := testArchive(t, Payload{Path: "a.jsonl", Data: []byte("one\n")})
	header := packed.Manifest.Header()
	header.Repository = &Repository{Commit: "abc", Branch: "main"}
	saved, err := New(header, packed.Payloads)
	if err != nil {
		t.Fatal(err)
	}

	// when
	packedJSON, _ := encodeManifest(packed.Manifest)
	savedJSON, _ := encodeManifest(saved.Manifest)

	// then
	if strings.Contains(string(packedJSON), "repository") || !strings.Contains(string(savedJSON), `"commit": "abc"`) {
		t.Fatalf("manifests:\n%s\n%s", packedJSON, savedJSON)
	}
}
