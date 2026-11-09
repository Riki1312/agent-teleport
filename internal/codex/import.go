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
// request.Workspace, and makes it available through Codex. Ancestors of a
// forked session are placed first or reused when they already exist locally;
// the session itself is updated only when the archive extends it. Nothing
// changes before validation succeeds, and a dry run never starts App Server.
func (s *Store) Import(request ImportRequest) (ImportResult, error) {
	imported, err := s.prepareHistory(request.Archive, request.Workspace)
	if err != nil {
		return ImportResult{}, err
	}
	session := imported.session()
	ancestors, err := s.planAncestors(imported.files[:len(imported.files)-1])
	if err != nil {
		return ImportResult{}, err
	}
	stored, err := findSession(s.home, session.meta.ID)
	if err != nil {
		return ImportResult{}, err
	}
	destination, err := importDestination(stored, s.home, session.name)
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
		if err := s.layout.recoverReplacement(destination, session.meta.ID, resetThread(client, session.meta.ID), s.repairThread(client, destination)); err != nil {
			return ImportResult{}, err
		}
		if stored, err = findSession(s.home, session.meta.ID); err != nil {
			return ImportResult{}, err
		}
	}
	outcome, err := s.planImport(stored, destination, session, request.Replace)
	if err != nil {
		return ImportResult{}, err
	}
	result := ImportResult{SessionID: session.meta.ID, Workspace: session.meta.CWD, Outcome: outcome}
	if request.DryRun {
		return result, nil
	}
	for _, ancestor := range ancestors {
		if err := s.placeRollout(ancestor.path, ancestor.data); err != nil {
			return ImportResult{}, fmt.Errorf("place ancestor session: %w", err)
		}
	}
	switch outcome {
	case Imported:
		err = s.placeNew(client, destination, session)
	case Updated, Replaced:
		err = s.replaceExisting(client, destination, session)
	}
	if err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

// prepareHistory validates an archive for the installed Codex version and
// returns its history mapped to workspace.
func (s *Store) prepareHistory(source archive.Archive, workspace string) (history, error) {
	sourceLayout, imported, err := validateArchive(source)
	if err != nil {
		return history{}, err
	}
	if sourceLayout.minimum != s.layout.minimum {
		return history{}, fmt.Errorf("archive from Codex %s uses a rollout layout that Codex %s does not read", source.Manifest.ProviderVersion, s.version)
	}
	remapped, err := s.layout.remapHistory(imported, workspace)
	if err != nil {
		return history{}, fmt.Errorf("remap workspace: %w", err)
	}
	return remapped, nil
}

// ancestorPlacement is an ancestor rollout that an import must place.
type ancestorPlacement struct {
	path string
	data []byte
}

// planAncestors returns the ancestors missing locally. An ancestor that exists
// locally must already start with the prefix the history references, because
// replacing an ancestor would change the history of its other forks.
func (s *Store) planAncestors(ancestors []rolloutFile) ([]ancestorPlacement, error) {
	var placements []ancestorPlacement
	for _, ancestor := range ancestors {
		stored, err := findSession(s.home, ancestor.meta.ID)
		if err != nil {
			return nil, err
		}
		switch {
		case len(stored.pending) > 0:
			return nil, fmt.Errorf("ancestor session %s has an interrupted replacement; import that session first", ancestor.meta.ID)
		case len(stored.archived) > 0 || len(stored.snapshots) > 0 || len(stored.active) > 1:
			return nil, fmt.Errorf("ancestor session %s exists locally as an archived, reverted, or duplicated thread; restore it in Codex first", ancestor.meta.ID)
		case len(stored.active) == 1:
			local, err := readStable(stored.active[0])
			if err != nil {
				return nil, fmt.Errorf("read ancestor session %s: %w", ancestor.meta.ID, err)
			}
			if bytes.HasPrefix(local, ancestor.data) {
				continue
			}
			if localMeta, err := parseRollout(local); err == nil && localMeta.CWD != ancestor.meta.CWD {
				return nil, fmt.Errorf("ancestor session %s exists in %s; import the fork there with --cwd", ancestor.meta.ID, localMeta.CWD)
			}
			return nil, fmt.Errorf("ancestor session %s differs locally from the history in the archive", ancestor.meta.ID)
		default:
			path, err := activeRolloutPath(s.home, ancestor.name)
			if err != nil {
				return nil, err
			}
			placements = append(placements, ancestorPlacement{path: path, data: ancestor.data})
		}
	}
	return placements, nil
}

// importDestination returns the rollout path an import writes: the path of an
// interrupted write, the existing active rollout, or the native file name in
// the active store.
func importDestination(stored storedSession, home, name string) (string, error) {
	switch {
	case len(stored.pending) > 1:
		return "", fmt.Errorf("session has several interrupted writes: %s", strings.Join(stored.pending, ", "))
	case len(stored.pending) == 1:
		return stored.pending[0], nil
	case len(stored.active) == 1:
		return stored.active[0], nil
	default:
		return activeRolloutPath(home, name)
	}
}

// planImport decides how to import the session rollout without changing local
// state.
func (s *Store) planImport(stored storedSession, destination string, session rolloutFile, replace bool) (ImportOutcome, error) {
	id := session.meta.ID
	if len(stored.archived) > 0 {
		return "", fmt.Errorf("session %s is archived at %s; restore or delete it in Codex before importing it", id, strings.Join(stored.archived, ", "))
	}
	if len(stored.snapshots) > 0 {
		return "", fmt.Errorf("session %s has been reverted locally; reverted Codex threads are not supported", id)
	}
	if len(stored.active) == 0 {
		return Imported, nil
	}
	if len(stored.active) > 1 || filepath.Clean(stored.active[0]) != filepath.Clean(destination) {
		return "", fmt.Errorf("session %s has several rollouts: %s", id, strings.Join(stored.active, ", "))
	}
	local, err := readStable(destination)
	if err != nil {
		return "", fmt.Errorf("read existing session %s: %w", id, err)
	}
	localMeta, err := parseRollout(local)
	if err != nil {
		return "", fmt.Errorf("read existing session %s: %w", id, err)
	}
	var localTurns turnState
	if err := s.layout.advanceTurns(&localTurns, local); err != nil || localTurns.open {
		return "", fmt.Errorf("existing session %s has a turn in progress", id)
	}
	if localMeta.CWD != session.meta.CWD && !replace {
		return "", fmt.Errorf("session %s already exists in %s; import there with --cwd, or pass --replace", id, localMeta.CWD)
	}
	switch compareHistories(local, session.data) {
	case historiesEqual:
		return Unchanged, nil
	case incomingAhead:
		return Updated, nil
	case localAhead:
		return KeptNewer, nil
	}
	if !replace {
		return "", fmt.Errorf("session %s has diverged from the archive; pass --replace to overwrite the local copy", id)
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
// failed verification deletes the new session again; placed ancestors remain,
// since they are complete sessions of their own.
func (s *Store) placeNew(client *appserver.Client, destination string, session rolloutFile) error {
	if err := s.placeRollout(destination, session.data); err != nil {
		return err
	}
	if err := s.verifySession(client, destination, session.meta); err != nil {
		if cleanupErr := client.Delete(session.meta.ID); cleanupErr != nil {
			return fmt.Errorf("repair imported session: %w; cleanup also failed: %v", err, cleanupErr)
		}
		return fmt.Errorf("repair imported session: %w", err)
	}
	return nil
}

// replaceExisting replaces a quiescent local session through the recoverable
// replacement transaction.
func (s *Store) replaceExisting(client *appserver.Client, destination string, session rolloutFile) error {
	id := session.meta.ID
	threads, err := client.List(appserver.ListQuery{SourceKinds: allSources})
	if err != nil {
		return fmt.Errorf("check existing session: %w", err)
	}
	if thread := findThread(threads, id); thread != nil && !isQuiescent(thread.Status.Type) {
		return fmt.Errorf("existing session %s is not quiescent (status %q)", id, thread.Status.Type)
	}
	if err := s.layout.replaceRollout(destination, session.data, session.meta, resetThread(client, id), s.repairThread(client, destination), nil); err != nil {
		return fmt.Errorf("replace session %s: %w", id, err)
	}
	return nil
}

func resetThread(client *appserver.Client, sessionID string) resetThreadFunc {
	return func() error { return client.Delete(sessionID) }
}

func (s *Store) repairThread(client *appserver.Client, destination string) repairThreadFunc {
	return func(meta sessionMeta) error { return s.verifySession(client, destination, meta) }
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

// verifySession lets Codex scan and repair the session whose rollout is at
// destination, then checks the result. A self-contained session must appear in
// the scan, the persisted state, and a thread read with the target workspace.
// A fork is verified by loading its history; see verifyFork.
func (s *Store) verifySession(client *appserver.Client, destination string, meta sessionMeta) error {
	if meta.Base != nil {
		return s.verifyFork(client, destination, meta)
	}
	threads, err := client.List(appserver.ListQuery{SourceKinds: allSources})
	if err != nil {
		return err
	}
	thread := findThread(threads, meta.ID)
	if thread == nil {
		return errors.New("Codex App Server did not discover the imported session")
	}
	stateThreads, err := client.List(appserver.ListQuery{StateOnly: true, SourceKinds: allSources})
	if err != nil {
		return err
	}
	stateThread := findThread(stateThreads, meta.ID)
	if stateThread == nil {
		return errors.New("Codex App Server did not persist the imported session")
	}
	readThread, err := client.Read(meta.ID)
	if err != nil {
		return err
	}
	if thread.CWD != meta.CWD || stateThread.CWD != meta.CWD || readThread.CWD != meta.CWD {
		return fmt.Errorf("repaired workspace does not match target %q (scan %q, state %q, read %q)", meta.CWD, thread.CWD, stateThread.CWD, readThread.CWD)
	}
	return nil
}

// verifyFork verifies a forked session. Codex lists a fork only once it has
// turns of its own, and builds a fork's turns only after loading its
// ancestors, so the history is loaded root first in a separate App Server
// that cannot start turns, hooks, or automations. The session must then
// replay every turn of its history.
func (s *Store) verifyFork(client *appserver.Client, destination string, meta sessionMeta) error {
	if _, err := client.List(appserver.ListQuery{SourceKinds: allSources}); err != nil {
		return err
	}
	local, err := s.layout.readHistory(s.home, destination)
	if err != nil {
		return err
	}
	loader, err := appserver.Start(s.ctx, s.binary, s.home, safeLoadArguments...)
	if err != nil {
		return fmt.Errorf("start Codex App Server to load the session history: %w", err)
	}
	defer loader.Close()
	for index, file := range local.files {
		thread, err := loader.Resume(file.meta.ID, "")
		if err != nil {
			return fmt.Errorf("load session %s: %w", file.meta.ID, err)
		}
		if index == len(local.files)-1 && (len(thread.Turns) != local.turns || thread.CWD != meta.CWD) {
			return fmt.Errorf("loaded session has %d turns in %q, want %d turns in %q", len(thread.Turns), thread.CWD, local.turns, meta.CWD)
		}
	}
	return nil
}

// safeLoadArguments start an App Server that loads histories without running
// anything: no automatic goals, hooks, plugins, shell snapshots, or local
// automations.
var safeLoadArguments = []string{
	"--disable", "goals",
	"--disable", "hooks",
	"--disable", "plugins",
	"--disable", "shell_snapshot",
	"--disable", "in_app_local_automation",
}

func findThread(threads []appserver.Thread, id string) *appserver.Thread {
	for index := range threads {
		if threads[index].ID == id {
			return &threads[index]
		}
	}
	return nil
}
