package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	parentFixture = "rollout-2026-11-09T01-37-58-01a1bdbd-8748-7a72-96ac-80bbb811e9c9.jsonl"
	forkFixture   = "rollout-2026-11-09T13-05-32-01a1c033-0a38-7d43-a289-532721dea6d7.jsonl"
)

func TestValidateHistoryOfNativeFork(t *testing.T) {
	files := nativeForkFiles(t)

	// when
	validated, err := testLayout.validateHistory(files)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if validated.turns != 3 || validated.session().meta.ID != files[1].meta.ID {
		t.Fatalf("validateHistory() turns = %d, session %s", validated.turns, validated.session().meta.ID)
	}
}

func TestValidateHistoryRejectsMissingOrMismatchedAncestors(t *testing.T) {
	files := nativeForkFiles(t)
	shortened := files[0]
	shortened.data = completedPrefixOf(t, shortened.data, 2)
	for name, history := range map[string][]rolloutFile{
		"missing ancestor":    {files[1]},
		"shorter ancestor":    {shortened, files[1]},
		"ancestor after fork": {files[1], files[0]},
	} {
		t.Run(name, func(t *testing.T) {
			// when
			_, err := testLayout.validateHistory(history)

			// then
			if err == nil {
				t.Fatal("validateHistory accepted an inconsistent history")
			}
		})
	}
}

func TestRemapHistoryRewritesAncestorsAndOffsets(t *testing.T) {
	original, err := testLayout.validateHistory(nativeForkFiles(t))
	if err != nil {
		t.Fatal(err)
	}

	// when
	remapped, err := testLayout.remapHistory(original, "/target/a-longer-workspace")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := testLayout.remapHistory(remapped, "/workspace/source")

	// then
	if err != nil {
		t.Fatal(err)
	}
	parent, fork := remapped.files[0], remapped.files[1]
	if bytes.Contains(parent.data, []byte(`"cwd":"/workspace/source"`)) || fork.meta.CWD != "/target/a-longer-workspace" {
		t.Fatal("remapped history keeps the source workspace")
	}
	if fork.meta.Base.EndByteOffset != int64(len(parent.data)) || len(parent.data) == len(original.files[0].data) {
		t.Fatalf("fork offset %d does not follow the rewritten ancestor length %d", fork.meta.Base.EndByteOffset, len(parent.data))
	}
	for index, file := range restored.files {
		if !bytes.Equal(file.data, original.files[index].data) {
			t.Fatalf("remapping %s there and back changed it", file.name)
		}
	}
}

func TestReadHistoryTakesTheReferencedAncestorPrefix(t *testing.T) {
	home := t.TempDir()
	files := nativeForkFiles(t)
	continuedParent := append(append([]byte{}, files[0].data...), files[0].data[bytes.IndexByte(files[0].data, '\n')+1:]...)
	writeRollout(t, home, parentFixture, continuedParent)
	forkPath := writeRollout(t, home, forkFixture, files[1].data)

	// when
	read, err := testLayout.readHistory(home, forkPath)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if len(read.files) != 2 || !bytes.Equal(read.files[0].data, files[0].data) {
		t.Fatal("readHistory did not return exactly the referenced ancestor prefix")
	}
}

func TestReadHistoryRejectsRevertedThreads(t *testing.T) {
	home := t.TempDir()
	files := nativeForkFiles(t)
	writeRollout(t, home, parentFixture, files[0].data)
	writeRollout(t, home, strings.TrimSuffix(parentFixture, ".jsonl")+"_01999999-3333-7777-8888-444444444444.jsonl", files[0].data)
	forkPath := writeRollout(t, home, forkFixture, files[1].data)

	// when
	_, err := testLayout.readHistory(home, forkPath)

	// then
	if err == nil || !strings.Contains(err.Error(), "reverted") {
		t.Fatalf("readHistory error = %v", err)
	}
}

func TestActiveRolloutPathFollowsTheNativeName(t *testing.T) {
	// when
	path, err := activeRolloutPath("/codex", parentFixture)

	// then
	if err != nil || path != filepath.Join("/codex", "sessions", "2026", "11", "09", parentFixture) {
		t.Fatalf("activeRolloutPath() = %q, %v", path, err)
	}
	for _, name := range []string{"rollout.jsonl", "../" + parentFixture, strings.TrimSuffix(parentFixture, ".jsonl") + "_01999999-3333-7777-8888-444444444444.jsonl"} {
		if _, err := activeRolloutPath("/codex", name); err == nil {
			t.Fatalf("activeRolloutPath accepted %q", name)
		}
	}
}

func TestRewriteBaseOffsetRequiresTheDecodedValue(t *testing.T) {
	fork := nativeForkFiles(t)[1]

	// when
	_, err := rewriteBaseOffset(fork.data, fork.meta.Base.EndByteOffset+1, 1)

	// then
	if err == nil {
		t.Fatal("rewriteBaseOffset accepted an offset that does not match the reference")
	}
}

func nativeForkFiles(t *testing.T) []rolloutFile {
	t.Helper()
	var files []rolloutFile
	for _, name := range []string{parentFixture, forkFixture} {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "codex", name))
		if err != nil {
			t.Fatal(err)
		}
		file, err := newRolloutFile(name, data)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	return files
}

func writeRollout(t *testing.T, home, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(home, "sessions", "2026", "11", "09", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func completedPrefixOf(t *testing.T, rollout []byte, turns int) []byte {
	t.Helper()
	end := 0
	for line := range bytes.Lines(rollout) {
		end += len(line)
		if bytes.Contains(line, []byte(`"type":"task_complete"`)) {
			if turns--; turns == 0 {
				return rollout[:end]
			}
		}
	}
	t.Fatal("fixture has too few completed turns")
	return nil
}
