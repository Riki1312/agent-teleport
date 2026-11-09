package codex

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/codex/appserver"
)

// CollisionPolicy decides what Import does when the session already exists.
type CollisionPolicy string

const (
	// CollisionError fails without changing the existing session.
	CollisionError CollisionPolicy = "error"
	// CollisionSkip keeps the existing session and succeeds.
	CollisionSkip CollisionPolicy = "skip"
	// CollisionReplace replaces the existing session through a recoverable
	// transaction.
	CollisionReplace CollisionPolicy = "replace"
)

// ImportRequest describes one archive import.
type ImportRequest struct {
	Archive   archive.Archive
	Workspace string
	IfExists  CollisionPolicy
	DryRun    bool
}

// ImportOutcome is what Import did, or would do in a dry run.
type ImportOutcome string

const (
	// Imported means the session was new and is now available.
	Imported ImportOutcome = "imported"
	// Replaced means an existing session was replaced.
	Replaced ImportOutcome = "replaced"
	// Skipped means an existing session was kept unchanged.
	Skipped ImportOutcome = "skipped"
)

// ImportResult reports the session an import produced.
type ImportResult struct {
	SessionID string
	Workspace string
	Outcome   ImportOutcome
}

// Import validates an archive for the installed Codex version, maps it to
// request.Workspace, and makes it available through Codex. Nothing changes
// before validation succeeds, and a dry run never starts App Server.
func (s *Store) Import(request ImportRequest) (ImportResult, error) {
	sourceLayout, rollout, err := validateArchive(request.Archive)
	if err != nil {
		return ImportResult{}, err
	}
	if request.Archive.Manifest.ProviderVersion != s.version {
		return ImportResult{}, fmt.Errorf("archive Codex version %s is incompatible with installed version %s", request.Archive.Manifest.ProviderVersion, s.version)
	}
	meta, err := sourceLayout.validateRollout(rollout)
	if err != nil {
		return ImportResult{}, err
	}
	if request.Workspace != meta.CWD {
		rollout, meta, err = s.layout.remapWorkspace(rollout, request.Workspace)
		if err != nil {
			return ImportResult{}, fmt.Errorf("remap workspace: %w", err)
		}
	}
	destination := rolloutPath(s.home, meta)
	result := ImportResult{SessionID: meta.ID, Workspace: meta.CWD, Outcome: Imported}
	if request.DryRun {
		return s.checkImport(destination, meta, request.IfExists, result)
	}

	client, err := s.appServer()
	if err != nil {
		return ImportResult{}, err
	}
	reset := func() error { return client.Delete(meta.ID) }
	repair := func(repaired sessionMeta) error { return verifyRepair(client, repaired.ID, repaired.CWD) }
	if err := s.layout.recoverReplacement(destination, meta.ID, reset, repair); err != nil {
		return ImportResult{}, err
	}
	existing, err := existingRollouts(s.home, meta.ID)
	if err != nil {
		return ImportResult{}, err
	}
	if len(existing) > 0 {
		return s.importExisting(client, request.IfExists, existing, destination, rollout, meta, result)
	}
	if err := s.placeRollout(destination, rollout); err != nil {
		return ImportResult{}, err
	}
	if err := verifyRepair(client, meta.ID, meta.CWD); err != nil {
		if cleanupErr := client.Delete(meta.ID); cleanupErr != nil {
			return ImportResult{}, fmt.Errorf("repair imported session: %w; cleanup also failed: %v", err, cleanupErr)
		}
		return ImportResult{}, fmt.Errorf("repair imported session: %w", err)
	}
	return result, nil
}

// checkImport reports what Import would do without changing local state.
func (s *Store) checkImport(destination string, meta sessionMeta, policy CollisionPolicy, result ImportResult) (ImportResult, error) {
	pending, err := replacementPending(destination)
	if err != nil {
		return ImportResult{}, fmt.Errorf("inspect replacement recovery state: %w", err)
	}
	if pending {
		return ImportResult{}, errors.New("an interrupted replacement requires recovery; rerun without --dry-run")
	}
	existing, err := existingRollouts(s.home, meta.ID)
	if err != nil {
		return ImportResult{}, err
	}
	if len(existing) == 0 {
		return result, nil
	}
	switch policy {
	case CollisionSkip:
		result.Outcome = Skipped
		return result, nil
	case CollisionReplace:
		if err := requireReplaceable(existing, destination, meta.ID); err != nil {
			return ImportResult{}, err
		}
		result.Outcome = Replaced
		return result, nil
	default:
		return ImportResult{}, fmt.Errorf("session %s already exists", meta.ID)
	}
}

func (s *Store) importExisting(
	client *appserver.Client,
	policy CollisionPolicy,
	existing []string,
	destination string,
	rollout []byte,
	meta sessionMeta,
	result ImportResult,
) (ImportResult, error) {
	switch policy {
	case CollisionSkip:
		result.Outcome = Skipped
		return result, nil
	case CollisionReplace:
	default:
		return ImportResult{}, fmt.Errorf("session %s already exists", meta.ID)
	}
	if err := requireReplaceable(existing, destination, meta.ID); err != nil {
		return ImportResult{}, err
	}
	threads, err := client.List("", false)
	if err != nil {
		return ImportResult{}, fmt.Errorf("check existing session: %w", err)
	}
	thread := findThread(threads, meta.ID)
	if thread == nil {
		return ImportResult{}, fmt.Errorf("Codex does not list existing session %s", meta.ID)
	}
	if !isQuiescent(thread.Status.Type) {
		return ImportResult{}, fmt.Errorf("existing session %s is not quiescent (status %q)", meta.ID, thread.Status.Type)
	}
	reset := func() error { return client.Delete(meta.ID) }
	repair := func(repaired sessionMeta) error { return verifyRepair(client, repaired.ID, repaired.CWD) }
	if err := s.layout.replaceRollout(destination, rollout, meta, reset, repair, nil); err != nil {
		return ImportResult{}, fmt.Errorf("replace session %s: %w", meta.ID, err)
	}
	result.Outcome = Replaced
	return result, nil
}

// requireReplaceable allows replacement only for one active rollout at the
// expected destination; archived or moved copies must be handled in Codex.
func requireReplaceable(existing []string, destination, sessionID string) error {
	if len(existing) == 1 && filepath.Clean(existing[0]) == filepath.Clean(destination) {
		return nil
	}
	return fmt.Errorf("session %s exists at %s; restore or delete it in Codex before replacing it", sessionID, strings.Join(existing, ", "))
}

// placeRollout writes a new rollout through the stage file and links it into
// place, so Codex never observes a partial rollout and an existing file is
// never replaced. Recovery removes a stage file left by an interruption.
func (s *Store) placeRollout(destination string, rollout []byte) error {
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	stage := stagePath(destination)
	if err := writeExclusive(stage, rollout); err != nil {
		return fmt.Errorf("stage rollout: %w", err)
	}
	defer os.Remove(stage)
	if err := os.Link(stage, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("rollout destination already exists: %s", destination)
		}
		return fmt.Errorf("place rollout: %w", err)
	}
	return syncDirectory(directory)
}

// verifyRepair lets Codex scan and repair the session, then requires that the
// scan, the persisted state, and a thread read all report the target workspace.
func verifyRepair(client *appserver.Client, id, cwd string) error {
	threads, err := client.List("", false)
	if err != nil {
		return err
	}
	thread := findThread(threads, id)
	if thread == nil {
		return errors.New("Codex App Server did not discover the imported session")
	}
	stateThreads, err := client.List("", true)
	if err != nil {
		return err
	}
	stateThread := findThread(stateThreads, id)
	if stateThread == nil {
		return errors.New("Codex App Server did not persist the imported session")
	}
	readThread, err := client.Read(id)
	if err != nil {
		return err
	}
	if thread.CWD != cwd || stateThread.CWD != cwd || readThread.CWD != cwd {
		return fmt.Errorf("repaired workspace does not match target %q (scan %q, state %q, read %q)", cwd, thread.CWD, stateThread.CWD, readThread.CWD)
	}
	return nil
}

func findThread(threads []appserver.Thread, id string) *appserver.Thread {
	for index := range threads {
		if threads[index].ID == id {
			return &threads[index]
		}
	}
	return nil
}
