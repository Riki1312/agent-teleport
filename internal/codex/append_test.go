package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendRecordsAppendsAndRemovesItsMarker(t *testing.T) {
	home, path := appendFixture(t, "first\n")

	// when
	err := appendRecords(home, testSessionID, path, []byte("second\n"))

	// then
	if err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, path, []byte("first\nsecond\n"))
	assertMissing(t, path+appendSuffix)
}

func TestRecoverAppendUndoesAPartialAppend(t *testing.T) {
	home, path := appendFixture(t, "first\nsec")
	writeMarker(t, path, "6 13\n")

	// when
	err := recoverAppend(home, testSessionID, path)

	// then
	if err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, path, []byte("first\n"))
	assertMissing(t, path+appendSuffix)
}

func TestRecoverAppendKeepsACompletedAppend(t *testing.T) {
	home, path := appendFixture(t, "first\nsecond\n")
	writeMarker(t, path, "6 13\n")

	// when
	err := recoverAppend(home, testSessionID, path)

	// then
	if err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, path, []byte("first\nsecond\n"))
	assertMissing(t, path+appendSuffix)
}

func TestRecoverAppendRefusesARolloutThatChangedAfterwards(t *testing.T) {
	home, path := appendFixture(t, "first\nsecond\nthird\n")
	writeMarker(t, path, "6 13\n")

	// when
	err := recoverAppend(home, testSessionID, path)

	// then
	if err == nil || !strings.Contains(err.Error(), "changed after an interrupted append") {
		t.Fatalf("recoverAppend error = %v", err)
	}
}

func TestAppendRecordsRefusesAThreadOpenInCodex(t *testing.T) {
	home, path := appendFixture(t, "first\n")
	unlock, err := lockThread(home, testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	// when
	err = appendRecords(home, testSessionID, path, []byte("second\n"))

	// then
	if !errors.Is(err, errSessionOpen) {
		t.Fatalf("appendRecords error = %v", err)
	}
	assertFileEquals(t, path, []byte("first\n"))
}

func appendFixture(t *testing.T, content string) (string, string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, "rollout.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, path
}

func writeMarker(t *testing.T, path, sizes string) {
	t.Helper()
	if err := os.WriteFile(path+appendSuffix, []byte(sizes), 0o600); err != nil {
		t.Fatal(err)
	}
}
