package codex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Riki1312/agent-teleport/internal/appserver"
)

type replacePhase string

const (
	phaseBackedUp    replacePhase = "backed-up"
	phaseStateReset  replacePhase = "state-reset"
	phaseNewPlaced   replacePhase = "new-placed"
	phaseNewVerified replacePhase = "new-verified"
)

type interruptionError struct {
	phase replacePhase
}

func (e *interruptionError) Error() string {
	return "simulated interruption after " + string(e.phase)
}

type resetThreadFunc func() error
type repairThreadFunc func(SessionMeta) error
type checkpointFunc func(replacePhase) error

func ReplacementPending(destination string) (bool, error) {
	for _, path := range []string{rollbackPath(destination), stagePath(destination)} {
		if _, err := os.Stat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func RecoverReplacement(destination, sessionID string, reset resetThreadFunc, repair repairThreadFunc) error {
	rollback := rollbackPath(destination)
	if _, err := os.Stat(rollback); errors.Is(err, os.ErrNotExist) {
		if err := removeIfExists(stagePath(destination)); err != nil {
			return fmt.Errorf("remove abandoned replacement stage: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect replacement rollback: %w", err)
	}
	return restoreRollback(destination, sessionID, reset, repair)
}

func ReplaceRollout(
	destination string,
	replacement []byte,
	meta SessionMeta,
	reset resetThreadFunc,
	repair repairThreadFunc,
) error {
	return replaceRollout(destination, replacement, meta, reset, repair, nil)
}

func replaceRollout(
	destination string,
	replacement []byte,
	meta SessionMeta,
	reset resetThreadFunc,
	repair repairThreadFunc,
	checkpoint checkpointFunc,
) error {
	if err := RecoverReplacement(destination, meta.ID, reset, repair); err != nil {
		return fmt.Errorf("recover interrupted replacement: %w", err)
	}
	_, oldMeta, err := StableRollout(destination)
	if err != nil {
		return fmt.Errorf("validate existing rollout: %w", err)
	}
	if oldMeta.ID != meta.ID {
		return fmt.Errorf("existing rollout has session ID %s, want %s", oldMeta.ID, meta.ID)
	}
	replacementMeta, err := ValidateRollout(replacement)
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
		return rollbackAfterError(destination, meta.ID, reset, repair, fmt.Errorf("sync replacement rollback: %w", err))
	}
	if err := runCheckpoint(checkpoint, phaseBackedUp); err != nil {
		return err
	}

	if err := reset(); err != nil {
		return rollbackAfterError(destination, meta.ID, reset, repair, fmt.Errorf("delete existing Codex thread: %w", err))
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			err = errors.New("Codex deleted thread state but left its rollout in place")
		} else {
			err = fmt.Errorf("inspect deleted rollout: %w", err)
		}
		return rollbackAfterError(destination, meta.ID, reset, repair, err)
	}
	if err := runCheckpoint(checkpoint, phaseStateReset); err != nil {
		return err
	}

	if err := os.Rename(stage, destination); err != nil {
		return rollbackAfterError(destination, meta.ID, reset, repair, fmt.Errorf("place replacement rollout: %w", err))
	}
	stageCommitted = true
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return rollbackAfterError(destination, meta.ID, reset, repair, fmt.Errorf("sync replacement rollout: %w", err))
	}
	if err := runCheckpoint(checkpoint, phaseNewPlaced); err != nil {
		return err
	}

	if err := repair(meta); err != nil {
		return rollbackAfterError(destination, meta.ID, reset, repair, fmt.Errorf("repair replacement thread: %w", err))
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

func rollbackAfterError(
	destination, sessionID string,
	reset resetThreadFunc,
	repair repairThreadFunc,
	cause error,
) error {
	if err := restoreRollback(destination, sessionID, reset, repair); err != nil {
		return fmt.Errorf("%w; rollback failed: %v", cause, err)
	}
	return cause
}

func restoreRollback(destination, sessionID string, reset resetThreadFunc, repair repairThreadFunc) error {
	rollback := rollbackPath(destination)
	_, oldMeta, err := StableRollout(rollback)
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

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func stagePath(destination string) string {
	return destination + ".cxport-stage"
}

func rollbackPath(destination string) string {
	return destination + ".cxport-rollback"
}

func isMissingRollout(err error) bool {
	var value *appserver.RPCError
	return errors.As(err, &value) && value.Code == -32600 &&
		strings.Contains(value.Message, "no rollout found for thread id")
}
