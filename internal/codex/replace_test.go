package codex

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceRolloutCommitsVerifiedReplacement(t *testing.T) {
	destination, _, newRollout, newMeta := replacementFixture(t)
	state := &fakeReplacementState{destination: destination, cwd: "/old/workspace"}

	if err := testCompatibility.ReplaceRollout(destination, newRollout, newMeta, state.reset, state.repair); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, destination, newRollout)
	if state.cwd != "/new/workspace" {
		t.Fatalf("repaired cwd = %q", state.cwd)
	}
	assertMissing(t, rollbackPath(destination))
	assertMissing(t, stagePath(destination))
}

func TestReplaceRolloutRestoresOldSessionAfterRepairFailure(t *testing.T) {
	destination, oldRollout, newRollout, newMeta := replacementFixture(t)
	state := &fakeReplacementState{
		destination: destination,
		cwd:         "/old/workspace",
		failCWD:     "/new/workspace",
	}

	err := testCompatibility.ReplaceRollout(destination, newRollout, newMeta, state.reset, state.repair)
	if err == nil || !errors.Is(err, errInjectedRepair) {
		t.Fatalf("ReplaceRollout error = %v", err)
	}
	assertFileEquals(t, destination, oldRollout)
	if state.cwd != "/old/workspace" {
		t.Fatalf("rollback repaired cwd = %q", state.cwd)
	}
	assertMissing(t, rollbackPath(destination))
	assertMissing(t, stagePath(destination))
}

func TestRecoverReplacementAfterEveryCheckpoint(t *testing.T) {
	phases := []replacePhase{
		phaseBackedUp,
		phaseStateReset,
		phaseNewPlaced,
		phaseNewVerified,
	}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			destination, oldRollout, newRollout, newMeta := replacementFixture(t)
			state := &fakeReplacementState{destination: destination, cwd: "/old/workspace"}
			checkpoint := func(current replacePhase) error {
				if current == phase {
					return &interruptionError{phase: phase}
				}
				return nil
			}

			err := testCompatibility.replaceRollout(destination, newRollout, newMeta, state.reset, state.repair, checkpoint)
			var interrupted *interruptionError
			if !errors.As(err, &interrupted) {
				t.Fatalf("replaceRollout error = %v", err)
			}
			pending, err := ReplacementPending(destination)
			if err != nil {
				t.Fatal(err)
			}
			if !pending {
				t.Fatal("interrupted replacement has no recovery marker")
			}
			if err := testCompatibility.RecoverReplacement(destination, newMeta.ID, state.reset, state.repair); err != nil {
				t.Fatal(err)
			}
			assertFileEquals(t, destination, oldRollout)
			if state.cwd != "/old/workspace" {
				t.Fatalf("recovered cwd = %q", state.cwd)
			}
			assertMissing(t, rollbackPath(destination))
			assertMissing(t, stagePath(destination))
		})
	}
}

func TestRecoverReplacementRemovesAbandonedStage(t *testing.T) {
	destination, _, _, newMeta := replacementFixture(t)
	if err := os.WriteFile(stagePath(destination), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := &fakeReplacementState{destination: destination, cwd: "/old/workspace"}
	if err := testCompatibility.RecoverReplacement(destination, newMeta.ID, state.reset, state.repair); err != nil {
		t.Fatal(err)
	}
	assertMissing(t, stagePath(destination))
}

var errInjectedRepair = errors.New("injected repair failure")

type fakeReplacementState struct {
	destination string
	cwd         string
	failCWD     string
}

func (s *fakeReplacementState) reset() error {
	err := os.Remove(s.destination)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.cwd = ""
	return nil
}

func (s *fakeReplacementState) repair(meta SessionMeta) error {
	if meta.CWD == s.failCWD {
		return errInjectedRepair
	}
	data, err := os.ReadFile(s.destination)
	if err != nil {
		return err
	}
	actual, err := testCompatibility.ValidateRollout(data)
	if err != nil {
		return err
	}
	if actual.ID != meta.ID || actual.CWD != meta.CWD {
		return errors.New("repaired rollout metadata does not match")
	}
	s.cwd = actual.CWD
	return nil
}

func replacementFixture(t *testing.T) (string, []byte, []byte, SessionMeta) {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "rollout.jsonl")
	oldRollout := fixtureRollout("/old/workspace")
	newRollout, newMeta, err := testCompatibility.RemapWorkspace(oldRollout, "/new/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, oldRollout, 0o600); err != nil {
		t.Fatal(err)
	}
	return destination, oldRollout, newRollout, newMeta
}

func assertFileEquals(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("file %s does not match expected rollout", path)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be absent, got %v", path, err)
	}
}
