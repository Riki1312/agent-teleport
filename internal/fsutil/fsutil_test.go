package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicReplacesContentAndDiscardsAStaleStage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.jsonl")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+StageSuffix, []byte("interrupted"), 0o600); err != nil {
		t.Fatal(err)
	}

	// when
	err := WriteAtomic(path, []byte("new"))

	// then
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "new" {
		t.Fatalf("file = %q, want new", data)
	}
	if _, err := os.Stat(path + StageSuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage file remains: %v", err)
	}
}

func TestReadStableRejectsDirectories(t *testing.T) {
	// when
	_, err := ReadStable(t.TempDir())

	// then
	if err == nil {
		t.Fatal("ReadStable() read a directory")
	}
}
