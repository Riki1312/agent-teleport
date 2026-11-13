package codex

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Riki1312/agent-teleport/internal/provider"
)

func TestCompareHistories(t *testing.T) {
	short := []byte("{\"turn\":1}\n")
	long := []byte("{\"turn\":1}\n{\"turn\":2}\n")
	other := []byte("{\"turn\":1}\n{\"turn\":3}\n")
	for _, test := range []struct {
		name            string
		local, incoming []byte
		want            historyComparison
	}{
		{name: "equal", local: long, incoming: long, want: historiesEqual},
		{name: "incoming ahead", local: short, incoming: long, want: incomingAhead},
		{name: "local ahead", local: long, incoming: short, want: localAhead},
		{name: "diverged", local: long, incoming: other, want: historiesDiverged},
	} {
		t.Run(test.name, func(t *testing.T) {
			// when, then
			if got := compareHistories(test.local, test.incoming); got != test.want {
				t.Fatalf("compareHistories() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestPlanSessionFollowsTheFastForwardRule(t *testing.T) {
	parent, fork := fixtureFiles(t, parentFixture)[0], fixtureFiles(t, forkFixture)[0]
	for _, test := range []struct {
		name  string
		local []byte
		want  provider.ImportOutcome
	}{
		{name: "new", local: nil, want: provider.Imported},
		{name: "equal", local: fork.data, want: provider.Unchanged},
		{name: "incoming ahead", local: fork.data[:len(fork.data)-len(lastRecord(fork.data))], want: provider.Updated},
		{name: "local ahead", local: append(append([]byte{}, fork.data...), settingsRecord("local")...), want: provider.KeptNewer},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := planStore(t, parent, fork, test.local)
			session, err := testLayout.validateSession(fork.meta.ID, []rolloutFile{parent, fork})
			if err != nil {
				t.Fatal(err)
			}

			// when
			plan, err := store.planSession(session, false)

			// then
			if err != nil || plan.outcome != test.want {
				t.Fatalf("planSession() = %q, %v; want %q", plan.outcome, err, test.want)
			}
		})
	}
}

func TestPlanSessionReplacesOnlySessionsOfOneThread(t *testing.T) {
	parent, fork := fixtureFiles(t, parentFixture)[0], fixtureFiles(t, forkFixture)[0]
	store := planStore(t, parent, fork, append(append([]byte{}, fork.data...), settingsRecord("local")...))
	incoming, err := newRolloutFile(fork.name, append(append([]byte{}, fork.data...), settingsRecord("incoming")...))
	if err != nil {
		t.Fatal(err)
	}
	session, err := testLayout.validateSession(fork.meta.ID, []rolloutFile{parent, incoming})
	if err != nil {
		t.Fatal(err)
	}

	// when
	_, err = store.planSession(session, true)

	// then
	if !errors.Is(err, provider.ErrDiverged) {
		t.Fatalf("planSession() error = %v, want ErrDiverged", err)
	}
}

// planStore returns a store whose home holds parent and, unless local is nil,
// a local copy of fork with the given content.
func planStore(t *testing.T, parent, fork rolloutFile, local []byte) *Store {
	t.Helper()
	home := t.TempDir()
	writeRollout(t, home, parent.name, parent.data)
	if local != nil {
		writeRollout(t, home, fork.name, local)
	}
	return &Store{home: home, version: testLayout.newestTested, layout: testLayout}
}

func lastRecord(data []byte) []byte {
	trimmed := data[:len(data)-1]
	for index := len(trimmed) - 1; index >= 0; index-- {
		if trimmed[index] == '\n' {
			return data[index+1:]
		}
	}
	return data
}

func settingsRecord(marker string) []byte {
	return []byte(`{"timestamp":"2026-11-09T13:06:00.000Z","type":"event_msg","payload":{"type":"thread_settings_applied","marker":"` + marker + `"}}` + "\n")
}

// Native resume/revert may persist the loader's settings after the snapshot's
// settings. Replaying must preserve the archived settings and ordinals, while
// refusing to discard conversation events or unknown header layouts.
func TestRevertSnapshotPreservesArchivedHistory(t *testing.T) {
	archived := fixtureFiles(t, multiAgentSnapshot)[0]
	lines := bytes.SplitAfter(archived.data, []byte{'\n'})
	meta := bytes.Replace(lines[0], []byte(`"type":"session_meta"`), []byte(`"type":"session_meta","native_marker":true`), 1)
	want := append(append([]byte{}, meta...), bytes.Join(lines[1:], nil)...)
	for _, test := range []struct {
		name    string
		records [][]byte
		valid   bool
	}{
		{name: "native settings", records: [][]byte{settingsRecord("native")}, valid: true},
		{name: "extra loader settings", records: [][]byte{settingsRecord("native"), settingsRecord("loader")}, valid: true},
		{name: "missing settings"},
		{name: "conversation event", records: [][]byte{settingsRecord("native"), []byte(`{"type":"response_item","payload":{"type":"message"}}` + "\n")}},
		{name: "unknown header size", records: [][]byte{settingsRecord("one"), settingsRecord("two"), settingsRecord("three")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := append([]byte{}, meta...)
			for _, record := range test.records {
				data = append(data, record...)
			}
			native, err := newRolloutFile(archived.name, data)
			if err != nil {
				t.Fatal(err)
			}
			store := &Store{layout: testLayout}
			restored, err := store.revertSnapshot(native, archived)
			if (err == nil) != test.valid {
				t.Fatalf("revertSnapshot error = %v, valid = %t", err, test.valid)
			}
			if test.valid && !bytes.Equal(restored, want) {
				t.Fatal("replayed snapshot did not preserve archived settings and continuation exactly")
			}
			if !bytes.Equal(native.data, data) {
				t.Fatal("header validation changed native data")
			}
		})
	}
}

func TestRevertSnapshotRejectsChangedHistoryBoundary(t *testing.T) {
	archived := fixtureFiles(t, multiAgentSnapshot)[0]
	for _, change := range []string{"thread", "workspace", "object", "offset", "ordinal"} {
		t.Run(change, func(t *testing.T) {
			native := archived
			native.data = bytes.Join(bytes.SplitAfter(archived.data, []byte{'\n'})[:2], nil)
			native.kinds = archived.kinds[:2]
			base := *native.meta.Base
			native.meta.Base = &base
			switch change {
			case "thread":
				native.meta.ID = testSessionID
			case "workspace":
				native.meta.CWD = "/different"
			case "object":
				native.meta.Base.ObjectID = testSessionID
			case "offset":
				native.meta.Base.EndByteOffset++
			case "ordinal":
				native.data = bytes.Replace(native.data, []byte(`"ordinal":`), []byte(`"ordinal":9`), 1)
			}
			store := &Store{layout: testLayout}
			if _, err := store.revertSnapshot(native, archived); err == nil {
				t.Fatal("accepted a different history boundary")
			}
		})
	}
}

func TestRestoreRevertCommitsHistoryAndProtectsConcurrentWriters(t *testing.T) {
	for _, state := range []string{"unchanged", "modified", "open"} {
		t.Run(state, func(t *testing.T) {
			archived := fixtureFiles(t, multiAgentSnapshot)[0]
			data := bytes.Join(bytes.SplitAfter(archived.data, []byte{'\n'})[:2], nil)
			data = append(data, settingsRecord("loader")...)
			home := t.TempDir()
			path := filepath.Join(home, archived.name)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			native, err := readRolloutFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if state == "modified" {
				data = append(data, settingsRecord("concurrent")...)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if state == "open" {
				unlock, err := lockThread(home, archived.meta.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
			}
			store := &Store{home: home, layout: testLayout}
			restored, err := store.restoreRevert(native, archived)
			switch state {
			case "unchanged":
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(restored, archived.data) {
					t.Fatal("restored history differs")
				}
				assertFileEquals(t, path, archived.data)
			case "modified":
				if err == nil || !strings.Contains(err.Error(), "changed") {
					t.Fatalf("error = %v", err)
				}
				assertFileEquals(t, path, data)
			case "open":
				if !errors.Is(err, errSessionOpen) {
					t.Fatalf("error = %v", err)
				}
				assertFileEquals(t, path, data)
			}
			assertMissing(t, stagePath(path))
		})
	}
}
