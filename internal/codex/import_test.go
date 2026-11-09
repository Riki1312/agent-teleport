package codex

import (
	"errors"
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
