package codex

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/codex/appserver"
)

// ImportRequest describes one archive import.
type ImportRequest struct {
	Archive archive.Archive
	// Workspace is the canonical absolute directory the session is mapped to.
	Workspace string
	// Replace allows overwriting a local copy that has diverged from the
	// archive, or that belongs to another workspace.
	Replace bool
	DryRun  bool
}

// ImportOutcome is what Import did, or would do in a dry run.
type ImportOutcome string

const (
	// Imported means the session was new and is now available.
	Imported ImportOutcome = "imported"
	// Updated means the archive extended the local copy, which now matches it.
	Updated ImportOutcome = "updated"
	// Unchanged means the local copy already matched the archive.
	Unchanged ImportOutcome = "unchanged"
	// KeptNewer means the local copy extends the archive and was kept.
	KeptNewer ImportOutcome = "kept-newer"
	// Replaced means a diverged local copy was replaced with the archive.
	Replaced ImportOutcome = "replaced"
)

// ImportResult reports the session an import produced.
type ImportResult struct {
	SessionID string
	Workspace string
	Outcome   ImportOutcome
}

// Import validates an archive for the installed Codex version, maps it to
// request.Workspace, and makes it available through Codex. An existing copy of
// the session is updated only when the archive extends it. Nothing changes
// before validation succeeds, and a dry run never starts App Server.
func (s *Store) Import(request ImportRequest) (ImportResult, error) {
	rollout, meta, err := s.prepareRollout(request.Archive, request.Workspace)
	if err != nil {
		return ImportResult{}, err
	}
	stored, err := findSession(s.home, meta.ID)
	if err != nil {
		return ImportResult{}, err
	}
	destination, err := importDestination(stored, s.home, meta)
	if err != nil {
		return ImportResult{}, err
	}
	var client *appserver.Client
	if request.DryRun {
		if len(stored.pending) > 0 {
			return ImportResult{}, errors.New("an interrupted replacement requires recovery; rerun without --dry-run")
		}
	} else {
		if client, err = s.appServer(); err != nil {
			return ImportResult{}, err
		}
		if err := s.layout.recoverReplacement(destination, meta.ID, resetThread(client, meta.ID), repairThread(client)); err != nil {
			return ImportResult{}, err
		}
		if stored, err = findSession(s.home, meta.ID); err != nil {
			return ImportResult{}, err
		}
	}
	outcome, err := s.planImport(stored, destination, rollout, meta, request.Replace)
	if err != nil {
		return ImportResult{}, err
	}
	result := ImportResult{SessionID: meta.ID, Workspace: meta.CWD, Outcome: outcome}
	if request.DryRun {
		return result, nil
	}
	switch outcome {
	case Imported:
		err = s.placeNew(client, destination, rollout, meta)
	case Updated, Replaced:
		err = s.replaceExisting(client, destination, rollout, meta)
	}
	if err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

// prepareRollout validates an archive for the installed Codex version and
// returns its rollout mapped to workspace.
func (s *Store) prepareRollout(source archive.Archive, workspace string) ([]byte, sessionMeta, error) {
	sourceLayout, rollout, err := validateArchive(source)
	if err != nil {
		return nil, sessionMeta{}, err
	}
	if sourceLayout.minimum != s.layout.minimum {
		return nil, sessionMeta{}, fmt.Errorf("archive from Codex %s uses a rollout layout that Codex %s does not read", source.Manifest.ProviderVersion, s.version)
	}
	meta, err := sourceLayout.validateRollout(rollout)
	if err != nil {
		return nil, sessionMeta{}, err
	}
	if workspace == meta.CWD {
		return rollout, meta, nil
	}
	rollout, meta, err = s.layout.remapWorkspace(rollout, workspace)
	if err != nil {
		return nil, sessionMeta{}, fmt.Errorf("remap workspace: %w", err)
	}
	return rollout, meta, nil
}

// importDestination returns the rollout path an import writes: the path of an
// interrupted write, the existing active rollout, or a new path named the way
// Codex names it.
func importDestination(stored storedSession, home string, meta sessionMeta) (string, error) {
	switch {
	case len(stored.pending) > 1:
		return "", fmt.Errorf("session %s has several interrupted writes: %s", meta.ID, strings.Join(stored.pending, ", "))
	case len(stored.pending) == 1:
		return stored.pending[0], nil
	case len(stored.active) == 1:
		return stored.active[0], nil
	default:
		return rolloutPath(home, meta), nil
	}
}

// planImport decides how to import rollout without changing local state.
func (s *Store) planImport(stored storedSession, destination string, rollout []byte, meta sessionMeta, replace bool) (ImportOutcome, error) {
	if len(stored.archived) > 0 {
		return "", fmt.Errorf("session %s is archived at %s; restore or delete it in Codex before importing it", meta.ID, strings.Join(stored.archived, ", "))
	}
	if len(stored.active) == 0 {
		return Imported, nil
	}
	if len(stored.active) > 1 || filepath.Clean(stored.active[0]) != filepath.Clean(destination) {
		return "", fmt.Errorf("session %s has several rollouts: %s", meta.ID, strings.Join(stored.active, ", "))
	}
	local, localMeta, err := s.layout.stableRollout(destination)
	if err != nil {
		return "", fmt.Errorf("read existing session %s: %w", meta.ID, err)
	}
	if localMeta.CWD != meta.CWD && !replace {
		return "", fmt.Errorf("session %s already exists in %s; import there with --cwd, or pass --replace", meta.ID, localMeta.CWD)
	}
	switch compareHistories(local, rollout) {
	case historiesEqual:
		return Unchanged, nil
	case incomingAhead:
		return Updated, nil
	case localAhead:
		return KeptNewer, nil
	}
	if !replace {
		return "", fmt.Errorf("session %s has diverged from the archive; pass --replace to overwrite the local copy", meta.ID)
	}
	return Replaced, nil
}

// historyComparison relates two copies of an append-only history.
type historyComparison int

const (
	historiesEqual historyComparison = iota
	incomingAhead
	localAhead
	historiesDiverged
)

// compareHistories relates two copies of the same session. Codex appends
// rollout records only at the end, so a copy that starts with the other is the
// same history with more turns.
func compareHistories(local, incoming []byte) historyComparison {
	switch {
	case bytes.Equal(local, incoming):
		return historiesEqual
	case bytes.HasPrefix(incoming, local):
		return incomingAhead
	case bytes.HasPrefix(local, incoming):
		return localAhead
	default:
		return historiesDiverged
	}
}

// placeNew places the rollout of a new session and verifies its repair. A
// failed verification deletes the new session again.
func (s *Store) placeNew(client *appserver.Client, destination string, rollout []byte, meta sessionMeta) error {
	if err := s.placeRollout(destination, rollout); err != nil {
		return err
	}
	if err := verifyRepair(client, meta.ID, meta.CWD); err != nil {
		if cleanupErr := client.Delete(meta.ID); cleanupErr != nil {
			return fmt.Errorf("repair imported session: %w; cleanup also failed: %v", err, cleanupErr)
		}
		return fmt.Errorf("repair imported session: %w", err)
	}
	return nil
}

// replaceExisting replaces a quiescent local session through the recoverable
// replacement transaction.
func (s *Store) replaceExisting(client *appserver.Client, destination string, rollout []byte, meta sessionMeta) error {
	threads, err := client.List(appserver.ListQuery{SourceKinds: allSources})
	if err != nil {
		return fmt.Errorf("check existing session: %w", err)
	}
	thread := findThread(threads, meta.ID)
	if thread == nil {
		return fmt.Errorf("Codex does not list existing session %s", meta.ID)
	}
	if !isQuiescent(thread.Status.Type) {
		return fmt.Errorf("existing session %s is not quiescent (status %q)", meta.ID, thread.Status.Type)
	}
	if err := s.layout.replaceRollout(destination, rollout, meta, resetThread(client, meta.ID), repairThread(client), nil); err != nil {
		return fmt.Errorf("replace session %s: %w", meta.ID, err)
	}
	return nil
}

func resetThread(client *appserver.Client, sessionID string) resetThreadFunc {
	return func() error { return client.Delete(sessionID) }
}

func repairThread(client *appserver.Client) repairThreadFunc {
	return func(meta sessionMeta) error { return verifyRepair(client, meta.ID, meta.CWD) }
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
	linkErr := os.Link(stage, destination)
	removeErr := os.Remove(stage)
	if errors.Is(linkErr, os.ErrExist) {
		return fmt.Errorf("rollout destination already exists: %s", destination)
	}
	if linkErr != nil {
		return fmt.Errorf("place rollout: %w", linkErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove rollout stage: %w", removeErr)
	}
	return syncDirectory(directory)
}

// verifyRepair lets Codex scan and repair the session, then requires that the
// scan, the persisted state, and a thread read all report the target workspace.
func verifyRepair(client *appserver.Client, id, cwd string) error {
	threads, err := client.List(appserver.ListQuery{SourceKinds: allSources})
	if err != nil {
		return err
	}
	thread := findThread(threads, id)
	if thread == nil {
		return errors.New("Codex App Server did not discover the imported session")
	}
	stateThreads, err := client.List(appserver.ListQuery{StateOnly: true, SourceKinds: allSources})
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
