package codex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Riki1312/agent-teleport/internal/codex/appserver"
)

// replacePhase names a persisted step of a replacement. Tests interrupt the
// transaction after each phase to prove that recovery restores the old session.
type replacePhase string

const (
	phaseBackedUp    replacePhase = "backed-up"
	phaseStateReset  replacePhase = "state-reset"
	phaseNewPlaced   replacePhase = "new-placed"
	phaseNewVerified replacePhase = "new-verified"
)

// interruptionError simulates a process exit at a replacement checkpoint.
type interruptionError struct {
	phase replacePhase
}

func (e *interruptionError) Error() string {
	return "simulated interruption after " + string(e.phase)
}

// resetThreadFunc deletes the session's thread state and rollout through Codex.
type resetThreadFunc func() error

// repairThreadFunc lets Codex rebuild and verify thread state for meta from the
// rollout file at its destination.
type repairThreadFunc func(sessionMeta) error

// checkpointFunc runs after each persisted replacement phase.
type checkpointFunc func(replacePhase) error

// recoverReplacement rolls back any replacement of destination interrupted
// before its commit point. Without a rollback link it only removes an abandoned
// stage file.
func (l layout) recoverReplacement(destination, sessionID string, reset resetThreadFunc, repair repairThreadFunc) error {
	rollback := rollbackPath(destination)
	if _, err := os.Stat(rollback); errors.Is(err, os.ErrNotExist) {
		if err := removeIfExists(stagePath(destination)); err != nil {
			return fmt.Errorf("remove abandoned replacement stage: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect replacement rollback: %w", err)
	}
	return l.restoreRollback(destination, sessionID, reset, repair)
}

// replaceRollout replaces the session at destination with replacement through a
// recoverable transaction:
//
//  1. Recover any earlier interrupted replacement.
//  2. Stage the replacement and hard-link the old rollout as a rollback copy.
//  3. Delete the old thread through Codex, then move the staged rollout into place.
//  4. Let Codex repair the new thread, then remove the rollback link to commit.
//
// Any failure before the commit point restores and repairs the old session.
func (l layout) replaceRollout(
	destination string,
	replacement []byte,
	meta sessionMeta,
	reset resetThreadFunc,
	repair repairThreadFunc,
	checkpoint checkpointFunc,
) error {
	if err := l.recoverReplacement(destination, meta.ID, reset, repair); err != nil {
		return fmt.Errorf("recover interrupted replacement: %w", err)
	}
	_, oldMeta, err := l.stableRollout(destination)
	if err != nil {
		return fmt.Errorf("validate existing rollout: %w", err)
	}
	if oldMeta.ID != meta.ID {
		return fmt.Errorf("existing rollout has session ID %s, want %s", oldMeta.ID, meta.ID)
	}
	replacementMeta, err := l.validateRollout(replacement)
	if err != nil {
		return fmt.Errorf("validate replacement rollout: %w", err)
	}
	if replacementMeta.ID != meta.ID || replacementMeta.CWD != meta.CWD || !replacementMeta.Timestamp.Equal(meta.Timestamp) {
		return errors.New("replacement rollout metadata does not match the requested replacement")
	}

	stage := stagePath(destination)
	rollback := rollbackPath(destination)
	if err := writeExclusive(stage, replacement); err != nil {
		return fmt.Errorf("stage replacement rollout: %w", err)
	}
	stageCommitted := false
	defer func() {
		if !stageCommitted {
			_ = os.Remove(stage)
		}
	}()
	if err := os.Link(destination, rollback); err != nil {
		return fmt.Errorf("create replacement rollback link: %w", err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return l.rollbackAfterError(destination, meta.ID, reset, repair, fmt.Errorf("sync replacement rollback: %w", err))
	}
	if err := runCheckpoint(checkpoint, phaseBackedUp); err != nil {
		return err
	}

	if err := reset(); err != nil {
		return l.rollbackAfterError(destination, meta.ID, reset, repair, fmt.Errorf("delete existing Codex thread: %w", err))
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			err = errors.New("Codex deleted thread state but left its rollout in place")
		} else {
			err = fmt.Errorf("inspect deleted rollout: %w", err)
		}
		return l.rollbackAfterError(destination, meta.ID, reset, repair, err)
	}
	if err := runCheckpoint(checkpoint, phaseStateReset); err != nil {
		return err
	}

	if err := os.Rename(stage, destination); err != nil {
		return l.rollbackAfterError(destination, meta.ID, reset, repair, fmt.Errorf("place replacement rollout: %w", err))
	}
	stageCommitted = true
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return l.rollbackAfterError(destination, meta.ID, reset, repair, fmt.Errorf("sync replacement rollout: %w", err))
	}
	if err := runCheckpoint(checkpoint, phaseNewPlaced); err != nil {
		return err
	}

	if err := repair(meta); err != nil {
		return l.rollbackAfterError(destination, meta.ID, reset, repair, fmt.Errorf("repair replacement thread: %w", err))
	}
	if err := runCheckpoint(checkpoint, phaseNewVerified); err != nil {
		return err
	}
	if err := os.Remove(rollback); err != nil {
		return fmt.Errorf("commit replacement by removing rollback link: %w", err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return fmt.Errorf("sync committed replacement: %w", err)
	}
	return nil
}

func (l layout) rollbackAfterError(
	destination, sessionID string,
	reset resetThreadFunc,
	repair repairThreadFunc,
	cause error,
) error {
	if err := l.restoreRollback(destination, sessionID, reset, repair); err != nil {
		return fmt.Errorf("%w; rollback failed: %v", cause, err)
	}
	return cause
}

func (l layout) restoreRollback(destination, sessionID string, reset resetThreadFunc, repair repairThreadFunc) error {
	rollback := rollbackPath(destination)
	_, oldMeta, err := l.stableRollout(rollback)
	if err != nil {
		return fmt.Errorf("validate rollback rollout: %w", err)
	}
	if oldMeta.ID != sessionID {
		return fmt.Errorf("rollback session ID %s does not match %s", oldMeta.ID, sessionID)
	}
	if err := reset(); err != nil && !isMissingRollout(err) {
		return fmt.Errorf("clear interrupted replacement state: %w", err)
	}
	if err := removeIfExists(destination); err != nil {
		return fmt.Errorf("remove interrupted replacement rollout: %w", err)
	}
	if err := os.Link(rollback, destination); err != nil {
		return fmt.Errorf("restore rollback rollout: %w", err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return fmt.Errorf("sync restored rollout: %w", err)
	}
	if err := repair(oldMeta); err != nil {
		return fmt.Errorf("repair restored thread: %w", err)
	}
	if err := os.Remove(rollback); err != nil {
		return fmt.Errorf("remove completed rollback link: %w", err)
	}
	if err := removeIfExists(stagePath(destination)); err != nil {
		return fmt.Errorf("remove abandoned replacement stage: %w", err)
	}
	return syncDirectory(filepath.Dir(destination))
}

func runCheckpoint(checkpoint checkpointFunc, phase replacePhase) error {
	if checkpoint == nil {
		return nil
	}
	return checkpoint(phase)
}

// writeExclusive creates path with owner-only permissions and syncs its content.
func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	if closeErr := directory.Close(); syncErr == nil {
		syncErr = closeErr
	}
	return syncErr
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// stageSuffix and rollbackSuffix name the recovery files kept next to a
// rollout while it is written or replaced.
const (
	stageSuffix    = ".agent-teleport-stage"
	rollbackSuffix = ".agent-teleport-rollback"
)

func stagePath(destination string) string {
	return destination + stageSuffix
}

func rollbackPath(destination string) string {
	return destination + rollbackSuffix
}

// isMissingRollout reports whether Codex rejected a delete because the thread no
// longer has a rollout, which recovery treats as already reset.
func isMissingRollout(err error) bool {
	var value *appserver.RPCError
	return errors.As(err, &value) && value.Code == -32600 &&
		strings.Contains(value.Message, "no rollout found for thread id")
}
