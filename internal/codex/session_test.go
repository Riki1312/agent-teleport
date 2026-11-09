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

	multiAgentRoot     = "01a1c03e-3fe4-7502-a037-4d6415a7ea24"
	multiAgentOriginal = "multi-agent/rollout-2026-11-09T13-17-48-01a1c03e-3fe4-7502-a037-4d6415a7ea24.jsonl"
	multiAgentSnapshot = "multi-agent/rollout-2026-11-09T13-18-31-01a1c03e-3fe4-7502-a037-4d6415a7ea24_01a1c03e-eae4-79c1-89ce-3116151361ce.jsonl"
	multiAgentChild    = "multi-agent/rollout-2026-11-09T13-17-52-01a1c03e-5302-7d83-83a8-dd5225e920ad.jsonl"
)

func TestValidateSessionOfNativeFork(t *testing.T) {
	files := fixtureFiles(t, parentFixture, forkFixture)

	// when
	session, err := testLayout.validateSession(files[1].meta.ID, files)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if session.turns != 3 || len(session.threads()) != 2 {
		t.Fatalf("validateSession() turns = %d, threads = %v", session.turns, session.threads())
	}
}

func TestValidateSessionOfRevertedMultiAgentSession(t *testing.T) {
	files := fixtureFiles(t, multiAgentOriginal, multiAgentSnapshot, multiAgentChild)

	// when
	session, err := testLayout.validateSession(multiAgentRoot, files)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if session.turns != 3 {
		t.Fatalf("reverted session replays %d turns, want 3", session.turns)
	}
	if head := session.head(multiAgentRoot); !head.isSnapshot() {
		t.Fatal("the root thread's current object is not its revert snapshot")
	}
	if threads := session.threads(); len(threads) != 2 || threads[0] != multiAgentRoot {
		t.Fatalf("threads() = %v", threads)
	}
}

func TestValidateSessionRejectsInconsistentSessions(t *testing.T) {
	original, snapshot, child := fixtureFiles(t, multiAgentOriginal, multiAgentSnapshot, multiAgentChild)[0], fixtureFiles(t, multiAgentSnapshot)[0], fixtureFiles(t, multiAgentChild)[0]
	for name, files := range map[string][]rolloutFile{
		"missing original":     {snapshot, child},
		"snapshot before base": {snapshot, original, child},
		"missing root thread":  {child},
		"duplicated object":    {original, original},
	} {
		t.Run(name, func(t *testing.T) {
			// when
			_, err := testLayout.validateSession(multiAgentRoot, files)

			// then
			if err == nil {
				t.Fatal("validateSession accepted an inconsistent session")
			}
		})
	}
}

func TestRemapSessionMovesReferencesAndRoundTrips(t *testing.T) {
	for name, fixtures := range map[string][]string{
		"fork":                 {parentFixture, forkFixture},
		"reverted multi-agent": {multiAgentOriginal, multiAgentSnapshot, multiAgentChild},
	} {
		t.Run(name, func(t *testing.T) {
			files := fixtureFiles(t, fixtures...)
			root := files[len(files)-1].meta.SessionID
			if name == "fork" {
				root = files[1].meta.ID
			}
			original, err := testLayout.validateSession(root, files)
			if err != nil {
				t.Fatal(err)
			}

			// when
			remapped, err := testLayout.remapSession(original, "/target/a-longer-workspace")
			if err != nil {
				t.Fatal(err)
			}
			restored, err := testLayout.remapSession(remapped, "/workspace/source")

			// then
			if err != nil {
				t.Fatal(err)
			}
			for index, file := range remapped.files {
				if file.meta.CWD != "/target/a-longer-workspace" {
					t.Fatalf("%s keeps workspace %s", file.name, file.meta.CWD)
				}
				if !bytes.Equal(restored.files[index].data, original.files[index].data) {
					t.Fatalf("remapping %s there and back changed it", file.name)
				}
			}
			if remapped.turns != original.turns {
				t.Fatalf("remapped session replays %d turns, want %d", remapped.turns, original.turns)
			}
		})
	}
}

func TestReadSessionCollectsSnapshotsAndSpawnedThreads(t *testing.T) {
	home := t.TempDir()
	files := fixtureFiles(t, multiAgentOriginal, multiAgentSnapshot, multiAgentChild, parentFixture)
	var snapshotPath string
	for _, file := range files {
		path := writeRollout(t, home, file.name, file.data)
		if file.isSnapshot() {
			snapshotPath = path
		}
	}

	// when
	session, err := testLayout.readSession(home, snapshotPath)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if len(session.files) != 3 || session.root != multiAgentRoot {
		t.Fatalf("readSession() root %s with %d files, want the original, the snapshot, and the child", session.root, len(session.files))
	}
}

func TestReadSessionTakesTheReferencedAncestorPrefix(t *testing.T) {
	home := t.TempDir()
	files := fixtureFiles(t, parentFixture, forkFixture)
	continuedParent := append(append([]byte{}, files[0].data...), files[0].data[bytes.IndexByte(files[0].data, '\n')+1:]...)
	writeRollout(t, home, parentFixture, continuedParent)
	forkPath := writeRollout(t, home, forkFixture, files[1].data)

	// when
	session, err := testLayout.readSession(home, forkPath)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if len(session.files) != 2 || !bytes.Equal(session.files[0].data, files[0].data) {
		t.Fatal("readSession did not return exactly the referenced ancestor prefix")
	}
}

func TestReadSessionRejectsSpawnedThreads(t *testing.T) {
	home := t.TempDir()
	child := fixtureFiles(t, multiAgentChild)[0]
	path := writeRollout(t, home, child.name, child.data)

	// when
	_, err := testLayout.readSession(home, path)

	// then
	if err == nil || !strings.Contains(err.Error(), "export that session instead") {
		t.Fatalf("readSession error = %v", err)
	}
}

func TestRevertedTurnNamesTheRemovedTurn(t *testing.T) {
	files := fixtureFiles(t, multiAgentOriginal, multiAgentSnapshot)

	// when
	turn, err := revertedTurn(files[0], *files[1].meta.Base)

	// then
	if err != nil || !strings.HasPrefix(turn, "01a1c03e-8b0") {
		t.Fatalf("revertedTurn() = %q, %v", turn, err)
	}
}

func TestActiveRolloutPathFollowsTheNativeName(t *testing.T) {
	// when
	path, err := activeRolloutPath("/codex", parentFixture)

	// then
	if err != nil || path != filepath.Join("/codex", "sessions", "2026", "11", "09", parentFixture) {
		t.Fatalf("activeRolloutPath() = %q, %v", path, err)
	}
	for _, name := range []string{"rollout.jsonl", "../" + parentFixture} {
		if _, err := activeRolloutPath("/codex", name); err == nil {
			t.Fatalf("activeRolloutPath accepted %q", name)
		}
	}
}

func TestRewriteBaseRequiresTheDecodedValue(t *testing.T) {
	fork := fixtureFiles(t, forkFixture)[0]
	stale := *fork.meta.Base
	stale.EndByteOffset++

	// when
	_, err := rewriteBase(fork.data, stale, stale.ObjectID, 1)

	// then
	if err == nil {
		t.Fatal("rewriteBase accepted a reference that does not match the decoded value")
	}
}

func fixtureFiles(t *testing.T, names ...string) []rolloutFile {
	t.Helper()
	var files []rolloutFile
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "codex", name))
		if err != nil {
			t.Fatal(err)
		}
		file, err := newRolloutFile(filepath.Base(name), data)
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

func TestFirstLineSessionIDReadsRecordsLongerThanItsBuffer(t *testing.T) {
	instructions := strings.Repeat("x", 200<<10)
	first := `{"type":"session_meta","payload":{"id":"child","session_id":"` + testSessionID + `","base_instructions":"` + instructions + `"}}` + "\n"
	path := writeRollout(t, t.TempDir(), "rollout-large.jsonl", []byte(first))

	// when
	sessionID, err := firstLineSessionID(path)

	// then
	if err != nil || sessionID != testSessionID {
		t.Fatalf("firstLineSessionID() = %q, %v", sessionID, err)
	}
}
