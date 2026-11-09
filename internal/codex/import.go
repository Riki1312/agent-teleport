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
// request.Workspace, and makes the session available through Codex: its root
// thread, the threads it spawned, and the ancestors they fork from. Every
// thread follows the fast-forward rule: new threads are placed, threads the
// archive extends receive the new records, and reverts are replayed through
// Codex. Nothing changes before the whole session has been planned, and a dry
// run never starts App Server.
func (s *Store) Import(request ImportRequest) (ImportResult, error) {
	session, err := s.prepareSession(request.Archive, request.Workspace)
	if err != nil {
		return ImportResult{}, err
	}
	var client *appserver.Client
	if !request.DryRun {
		if client, err = s.appServer(); err != nil {
			return ImportResult{}, err
		}
		if err := s.recoverSession(client, session); err != nil {
			return ImportResult{}, err
		}
	}
	plan, err := s.planSession(session, request.Replace)
	if err != nil {
		return ImportResult{}, err
	}
	root := session.head(session.root)
	result := ImportResult{SessionID: session.root, Workspace: root.meta.CWD, Outcome: plan.outcome}
	if request.DryRun || !plan.changes() {
		return result, nil
	}
	if err := s.executePlan(client, session, plan); err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

// prepareSession validates an archive for the installed Codex version and
// returns its session mapped to workspace.
func (s *Store) prepareSession(source archive.Archive, workspace string) (codexSession, error) {
	sourceLayout, imported, err := validateArchive(source)
	if err != nil {
		return codexSession{}, err
	}
	if sourceLayout.minimum != s.layout.minimum {
		return codexSession{}, fmt.Errorf("archive from Codex %s uses a rollout layout that Codex %s does not read", source.Manifest.ProviderVersion, s.version)
	}
	remapped, err := s.layout.remapSession(imported, workspace)
	if err != nil {
		return codexSession{}, fmt.Errorf("remap workspace: %w", err)
	}
	return remapped, nil
}

// recoverSession completes or rolls back writes to the session's threads that
// an earlier import left unfinished.
func (s *Store) recoverSession(client *appserver.Client, session codexSession) error {
	for _, thread := range session.threads() {
		stored, err := findSession(s.home, thread)
		if err != nil {
			return err
		}
		for _, path := range stored.appending {
			if err := recoverAppend(s.home, thread, path); err != nil {
				return err
			}
		}
		for _, destination := range stored.pending {
			if thread != session.root {
				return fmt.Errorf("thread %s has an interrupted replacement that agent-teleport cannot recover", thread)
			}
			if err := s.layout.recoverReplacement(destination, thread, resetThread(client, thread), s.repairThread(client, destination)); err != nil {
				return err
			}
		}
	}
	return nil
}

// fileStep is what an import does with one rollout object of the archive.
type fileStep int

const (
	// stepKeep leaves an equal or newer local object unchanged.
	stepKeep fileStep = iota
	// stepPlace writes a new original rollout.
	stepPlace
	// stepAppend appends the archive's newer records to a local object.
	stepAppend
	// stepReplay replays a revert through Codex, then appends the records the
	// snapshot holds after its header.
	stepReplay
	// stepReplace replaces a diverged root thread through the recoverable
	// replacement transaction.
	stepReplace
)

// filePlan is the planned step for one rollout object of the archive.
type filePlan struct {
	step fileStep
	// path is the local file a keep, append, or replace step refers to, or
	// the destination of a place step.
	path string
	// records are the bytes an append step adds.
	records []byte
}

// localObject is the local counterpart of an archived rollout object, used to
// point references at it.
type localObject struct {
	object string
	data   []byte
	// from is the archived object the local one corresponds to.
	from []byte
}

func (o localObject) moved(offset int64) (int64, error) {
	return newBoundaryMap(o.from, o.data).moved(offset)
}

// sessionPlan is the planned import of a whole session.
type sessionPlan struct {
	files   []filePlan
	objects map[string]localObject
	outcome ImportOutcome
}

func (p sessionPlan) changes() bool {
	for _, file := range p.files {
		if file.step != stepKeep {
			return true
		}
	}
	return false
}

// planSession decides, without changing local state, how to import every
// thread of the session.
func (s *Store) planSession(session codexSession, replace bool) (sessionPlan, error) {
	plan := sessionPlan{files: make([]filePlan, len(session.files)), objects: map[string]localObject{}}
	outcomes := map[string]ImportOutcome{}
	for _, thread := range session.threads() {
		outcome, err := s.planThread(session, thread, replace, &plan)
		if err != nil {
			return sessionPlan{}, err
		}
		outcomes[thread] = outcome
	}
	plan.outcome = outcomes[session.root]
	if plan.outcome == Unchanged || plan.outcome == KeptNewer {
		for thread, outcome := range outcomes {
			if thread != session.root && (outcome == Imported || outcome == Updated) {
				plan.outcome = Updated
			}
		}
	}
	return plan, nil
}

// planThread plans the objects of one thread. Its archived objects form a
// chain: the original rollout, then each revert snapshot. A local copy is
// compared segment by segment, where a segment is the original rollout or the
// records a snapshot holds after its revert header.
func (s *Store) planThread(session codexSession, thread string, replace bool, plan *sessionPlan) (ImportOutcome, error) {
	var chain []int
	for index, file := range session.files {
		if file.meta.ID == thread {
			chain = append(chain, index)
		}
	}
	first := session.files[chain[0]]
	member := isSessionThread(first, session.root)
	stored, err := findSession(s.home, thread)
	if err != nil {
		return "", err
	}
	switch {
	case len(stored.archived) > 0:
		return "", fmt.Errorf("thread %s is archived at %s; restore or delete it in Codex before importing it", thread, strings.Join(stored.archived, ", "))
	case len(stored.pending) > 0 || len(stored.appending) > 0:
		return "", fmt.Errorf("thread %s has an interrupted write; rerun without --dry-run to recover it", thread)
	case len(stored.active) > 1:
		return "", fmt.Errorf("thread %s has several rollouts: %s", thread, strings.Join(stored.active, ", "))
	}
	if len(stored.active) == 0 {
		if len(stored.snapshots) > 0 {
			return "", fmt.Errorf("thread %s has snapshots but no original rollout", thread)
		}
		path, err := activeRolloutPath(s.home, first.name)
		if err != nil {
			return "", err
		}
		plan.files[chain[0]] = filePlan{step: stepPlace, path: path}
		for _, index := range chain[1:] {
			if session.files[index].meta.SessionID == session.root && thread != session.root {
				return "", fmt.Errorf("reverted sub-agent thread %s is not supported", thread)
			}
			plan.files[index] = filePlan{step: stepReplay}
		}
		return Imported, nil
	}

	local, err := s.localChain(stored)
	if err != nil {
		return "", err
	}
	replacingRoot := replace && thread == session.root
	if member && local[0].meta.CWD != first.meta.CWD && !replacingRoot {
		return "", fmt.Errorf("thread %s already exists in %s; import there with --cwd, or pass --replace", thread, local[0].meta.CWD)
	}
	if thread == session.root || member {
		if err := requireClosed(s.home, thread); err != nil {
			return "", err
		}
	}
	incoming := make([]rolloutFile, len(chain))
	for position, index := range chain {
		incoming[position] = session.files[index]
	}
	if incoming[0], err = s.pointAtLocalObjects(incoming[0], plan.objects); err != nil {
		return "", err
	}
	incomingSegments, err := s.segments(incoming)
	if err != nil {
		return "", err
	}
	localSegments, err := s.segments(local)
	if err != nil {
		return "", err
	}
	common := min(len(incomingSegments), len(localSegments))
	for position := 0; position < common-1; position++ {
		if !bytes.Equal(incomingSegments[position], localSegments[position]) {
			return "", fmt.Errorf("thread %s has been reverted differently on this machine; delete it in Codex and import again", thread)
		}
		plan.keep(chain[position], incoming[position], local[position], nil)
	}
	last := common - 1
	relation := compareHistories(localSegments[last], incomingSegments[last])
	keepLast := func() { plan.keep(chain[last], incoming[last], local[last], nil) }
	switch {
	case !member && (relation == historiesEqual || relation == localAhead) && len(localSegments) >= len(incomingSegments):
		keepLast()
		return Unchanged, nil
	case !member:
		return "", fmt.Errorf("ancestor thread %s differs locally from the history in the archive", thread)
	case len(incomingSegments) == len(localSegments) && relation == historiesEqual:
		keepLast()
		return Unchanged, nil
	case len(incomingSegments) <= len(localSegments) && (relation == historiesEqual || relation == localAhead):
		keepLast()
		return KeptNewer, nil
	case len(incomingSegments) >= len(localSegments) && relation == incomingAhead:
		records := incomingSegments[last][len(localSegments[last]):]
		plan.keep(chain[last], incoming[last], local[last], records)
	case len(incomingSegments) > len(localSegments) && relation == historiesEqual:
		keepLast()
	case relation == historiesDiverged && replace && thread == session.root && len(chain) == 1 && len(local) == 1:
		plan.files[chain[0]] = filePlan{step: stepReplace, path: stored.active[0]}
		return Replaced, nil
	case relation == historiesDiverged && thread == session.root:
		return "", fmt.Errorf("session %s has diverged from the archive; pass --replace to overwrite the local copy", thread)
	default:
		return "", fmt.Errorf("thread %s has diverged from the archive; delete it in Codex and import again", thread)
	}
	for _, index := range chain[common:] {
		plan.files[index] = filePlan{step: stepReplay}
	}
	return Updated, nil
}

// keep plans to leave a local object as it is, or to append records to it,
// and records it as the counterpart of the archived object.
func (p *sessionPlan) keep(index int, incoming, local rolloutFile, records []byte) {
	step := stepKeep
	if len(records) > 0 {
		step = stepAppend
	}
	p.files[index] = filePlan{step: step, path: local.location, records: records}
	p.objects[incoming.object] = localObject{object: local.object, data: append(append([]byte{}, local.data...), records...), from: incoming.data}
}

// localChain reads the local objects of a thread in chain order: the original
// rollout first, then each snapshot that references the previous object.
func (s *Store) localChain(stored storedSession) ([]rolloutFile, error) {
	original, err := readRolloutFile(stored.active[0])
	if err != nil {
		return nil, err
	}
	original.location = stored.active[0]
	snapshots := map[string]rolloutFile{}
	for _, path := range stored.snapshots {
		snapshot, err := readRolloutFile(path)
		if err != nil {
			return nil, err
		}
		snapshot.location = path
		snapshots[snapshot.meta.Base.ObjectID] = snapshot
	}
	chain := []rolloutFile{original}
	for len(snapshots) > 0 {
		next, ok := snapshots[chain[len(chain)-1].object]
		if !ok {
			return nil, fmt.Errorf("thread %s has snapshots that do not form one revert chain", original.meta.ID)
		}
		delete(snapshots, chain[len(chain)-1].object)
		chain = append(chain, next)
	}
	return chain, nil
}

// segments returns the comparable parts of a thread's objects: the whole
// original rollout, then the records each snapshot holds after its header.
func (s *Store) segments(chain []rolloutFile) ([][]byte, error) {
	segments := [][]byte{chain[0].data}
	for _, snapshot := range chain[1:] {
		continuation, err := s.layout.snapshotContinuation(snapshot)
		if err != nil {
			return nil, err
		}
		segments = append(segments, continuation)
	}
	return segments, nil
}

// pointAtLocalObjects rewrites the reference of an archived original rollout
// to the local counterpart of the object it references, if that differs.
func (s *Store) pointAtLocalObjects(file rolloutFile, objects map[string]localObject) (rolloutFile, error) {
	base := file.meta.Base
	if base == nil {
		return file, nil
	}
	target, ok := objects[base.ObjectID]
	if !ok {
		return file, nil
	}
	offset, err := target.moved(base.EndByteOffset)
	if err != nil {
		return rolloutFile{}, fmt.Errorf("%s: %w", file.name, err)
	}
	if target.object == base.ObjectID && offset == base.EndByteOffset {
		return file, nil
	}
	data, err := rewriteBase(file.data, *base, target.object, offset)
	if err != nil {
		return rolloutFile{}, fmt.Errorf("%s: %w", file.name, err)
	}
	return newRolloutFile(file.name, data)
}

// executePlan performs a session plan in archive order, so every referenced
// object exists locally before the objects that reference it, then verifies
// the session.
func (s *Store) executePlan(client *appserver.Client, session codexSession, plan sessionPlan) error {
	for index, file := range session.files {
		step := plan.files[index]
		switch step.step {
		case stepKeep:
		case stepAppend:
			if err := appendRecords(s.home, file.meta.ID, step.path, step.records); err != nil {
				return err
			}
		case stepPlace:
			placed, err := s.pointAtLocalObjects(file, plan.objects)
			if err != nil {
				return err
			}
			if err := s.placeRollout(step.path, placed.data); err != nil {
				return fmt.Errorf("place thread %s: %w", file.meta.ID, err)
			}
			plan.objects[file.object] = localObject{object: placed.object, data: placed.data, from: file.data}
		case stepReplay:
			replayed, err := s.replayRevert(session, file, plan.objects)
			if err != nil {
				return fmt.Errorf("replay revert of thread %s: %w", file.meta.ID, err)
			}
			plan.objects[file.object] = replayed
		case stepReplace:
			if err := s.replaceExisting(client, step.path, file); err != nil {
				return err
			}
			return nil
		}
	}
	if err := s.verifyImportedSession(client, session, plan); err != nil {
		if plan.files[0].step == stepPlace && len(session.files) == 1 {
			if cleanupErr := client.Delete(session.root); cleanupErr != nil {
				return fmt.Errorf("verify imported session: %w; cleanup also failed: %v", err, cleanupErr)
			}
		}
		return fmt.Errorf("verify imported session: %w", err)
	}
	return nil
}

// replayRevert asks Codex to revert a thread the way the archived snapshot
// did, then appends the records the snapshot holds after its header. The
// replayed snapshot is Codex's own, so Codex locates it as the thread's
// current history.
func (s *Store) replayRevert(session codexSession, snapshot rolloutFile, objects map[string]localObject) (localObject, error) {
	thread := snapshot.meta.ID
	base := *snapshot.meta.Base
	target, ok := objects[base.ObjectID]
	if !ok {
		return localObject{}, fmt.Errorf("reverted rollout %s is not available locally", base.ObjectID)
	}
	offset, err := target.moved(base.EndByteOffset)
	if err != nil {
		return localObject{}, err
	}
	turn, err := revertedTurn(rolloutFile{data: target.data}, historyBase{ObjectID: target.object, EndOrdinalExclusive: base.EndOrdinalExclusive, EndByteOffset: offset})
	if err != nil {
		return localObject{}, err
	}
	if err := requireClosed(s.home, thread); err != nil {
		return localObject{}, err
	}
	before, err := findSession(s.home, thread)
	if err != nil {
		return localObject{}, err
	}
	loader, err := appserver.Start(s.ctx, s.binary, s.home, safeLoadArguments...)
	if err != nil {
		return localObject{}, fmt.Errorf("start Codex App Server to replay a revert: %w", err)
	}
	err = s.loadThreads(loader, session, thread)
	if err == nil {
		err = loader.Revert(thread, turn)
	}
	loader.Close()
	if err != nil {
		return localObject{}, err
	}
	after, err := findSession(s.home, thread)
	if err != nil {
		return localObject{}, err
	}
	created := newPaths(before.snapshots, after.snapshots)
	if len(created) != 1 {
		return localObject{}, fmt.Errorf("Codex wrote %d snapshots for the revert, want 1", len(created))
	}
	native, err := readRolloutFile(created[0])
	if err != nil {
		return localObject{}, err
	}
	if err := s.requireRevertHeader(native, snapshot); err != nil {
		return localObject{}, err
	}
	continuation, err := s.layout.snapshotContinuation(snapshot)
	if err != nil {
		return localObject{}, err
	}
	if len(continuation) > 0 {
		if err := appendRecords(s.home, thread, created[0], continuation); err != nil {
			return localObject{}, err
		}
	}
	return localObject{object: native.object, data: append(native.data, continuation...), from: snapshot.data}, nil
}

// requireRevertHeader checks that Codex's replayed snapshot consists of
// exactly a revert header with the same records as the archived snapshot's.
func (s *Store) requireRevertHeader(native, archived rolloutFile) error {
	nativeKinds, err := recordKinds(native.data)
	if err != nil {
		return err
	}
	archivedKinds, err := recordKinds(archived.data)
	if err != nil {
		return err
	}
	if len(nativeKinds) != s.layout.revertHeaderRecords || len(archivedKinds) < len(nativeKinds) {
		return fmt.Errorf("Codex wrote a revert header of %d records, want %d", len(nativeKinds), s.layout.revertHeaderRecords)
	}
	for index, kind := range nativeKinds {
		if archivedKinds[index] != kind {
			return errors.New("Codex wrote a different revert header than the archived snapshot")
		}
	}
	return nil
}

func recordKinds(data []byte) ([]eventKind, error) {
	var kinds []eventKind
	for line := range bytes.Lines(data) {
		kind, err := classifyEvent(line)
		if err != nil {
			return nil, err
		}
		kinds = append(kinds, kind)
	}
	return kinds, nil
}

func newPaths(before, after []string) []string {
	known := map[string]bool{}
	for _, path := range before {
		known[path] = true
	}
	var created []string
	for _, path := range after {
		if !known[path] {
			created = append(created, path)
		}
	}
	return created
}

// loadThreads resumes the session's threads in archive order up to and
// including last, so every fork's ancestors are loaded before it. Spawned
// threads load through their parent and are skipped.
func (s *Store) loadThreads(loader *appserver.Client, session codexSession, last string) error {
	for _, thread := range session.threads() {
		head := session.head(thread)
		if head.meta.SessionID == session.root && thread != session.root {
			continue
		}
		if _, err := loader.Resume(thread, ""); err != nil {
			return fmt.Errorf("load thread %s: %w", thread, err)
		}
		if thread == last {
			return nil
		}
	}
	return nil
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

// verifySession lets Codex scan and repair a self-contained thread, then
// requires the scan, the persisted state, and a thread read to report it in
// its workspace.
func (s *Store) verifySession(client *appserver.Client, _ string, meta sessionMeta) error {
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

// verifyImportedSession checks the result of an import. A session of one
// self-contained thread must appear with the target workspace in the scan, the
// persisted state, and a thread read. Otherwise Codex lists a fork only once it
// has turns of its own, builds a fork's turns only after loading its
// ancestors, and loads spawned threads through their parent, so the session is
// loaded in order in a separate App Server that cannot start turns, hooks, or
// automations: the root thread must replay every turn, and every spawned
// thread must be readable in its workspace.
func (s *Store) verifyImportedSession(client *appserver.Client, session codexSession, plan sessionPlan) error {
	root := session.head(session.root)
	if len(session.files) == 1 && root.meta.Base == nil {
		return s.verifySession(client, plan.files[0].path, root.meta)
	}
	if _, err := client.List(appserver.ListQuery{SourceKinds: allSources}); err != nil {
		return err
	}
	loader, err := appserver.Start(s.ctx, s.binary, s.home, safeLoadArguments...)
	if err != nil {
		return fmt.Errorf("start Codex App Server to load the session: %w", err)
	}
	defer loader.Close()
	if err := s.loadThreads(loader, session, ""); err != nil {
		return err
	}
	loaded, err := loader.Resume(session.root, "")
	if err != nil {
		return err
	}
	if len(loaded.Turns) != session.turns || loaded.CWD != root.meta.CWD {
		return fmt.Errorf("loaded session has %d turns in %q, want %d turns in %q", len(loaded.Turns), loaded.CWD, session.turns, root.meta.CWD)
	}
	for _, thread := range session.threads() {
		head := session.head(thread)
		if thread == session.root || head.meta.SessionID != session.root {
			continue
		}
		read, err := client.Read(thread)
		if err != nil {
			return fmt.Errorf("read spawned thread %s: %w", thread, err)
		}
		if read.CWD != head.meta.CWD {
			return fmt.Errorf("spawned thread %s has workspace %q, want %q", thread, read.CWD, head.meta.CWD)
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
