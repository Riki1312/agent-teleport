package claude

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/fsutil"
	"github.com/Riki1312/agent-teleport/internal/pathmap"
	"github.com/Riki1312/agent-teleport/internal/provider"
)

// Import validates an archive for the installed Claude Code version, maps it
// to request.Workspace, and places the session where Claude Code looks for it:
// the project folder of that workspace. When the session exists, every file
// follows the fast-forward rule: files the archive extends are replaced
// atomically, and a longer local copy is kept. Nothing changes before the
// whole session has been planned.
func (s *Store) Import(request provider.ImportRequest) (provider.ImportResult, error) {
	incoming, err := s.prepareSession(request.Archive, request.Workspace)
	if err != nil {
		return provider.ImportResult{}, err
	}
	plan, err := s.planImport(incoming, request.Replace)
	if err != nil {
		return provider.ImportResult{}, err
	}
	result := provider.ImportResult{SessionID: incoming.id, Workspace: incoming.info.workspace, Outcome: plan.outcome}
	if request.DryRun || !plan.changes() {
		return result, nil
	}
	if err := plan.execute(); err != nil {
		return provider.ImportResult{}, err
	}
	if err := verifyImport(plan.folder, incoming); err != nil {
		return provider.ImportResult{}, fmt.Errorf("verify imported session: %w", err)
	}
	return result, nil
}

// prepareSession validates source and moves its workspace metadata to target.
// The workspace moves to target, and the source project folder, which the
// recorded system prompt names as the memory folder, moves to the target's.
func (s *Store) prepareSession(source archive.Archive, target string) (claudeSession, error) {
	sourceLayout, session, err := validateArchive(source)
	if err != nil {
		return claudeSession{}, err
	}
	if _, _, err := layoutFor(s.version); err != nil {
		return claudeSession{}, err
	}
	var moves []pathmap.Move
	for _, move := range []pathmap.Move{
		{From: source.Manifest.Workspace, To: target},
		{
			From: filepath.Join(source.Manifest.Home, "projects", projectFolderName(source.Manifest.Workspace)),
			To:   s.projectFolder(target),
		},
	} {
		if move.From != move.To {
			moves = append(moves, move)
		}
	}
	if len(moves) == 0 {
		return session, nil
	}
	mapping, err := pathmap.New(moves...)
	if err != nil {
		return claudeSession{}, err
	}
	files := make([]sessionFile, 0, len(session.files))
	for _, file := range session.files {
		if file.isTranscript() {
			if file.data, err = sourceLayout.remapTranscript(file.data, mapping); err != nil {
				return claudeSession{}, fmt.Errorf("%s: %w", file.path, err)
			}
		}
		files = append(files, file)
	}
	remapped, err := newSession(session.id, files)
	if err != nil {
		return claudeSession{}, err
	}
	if remapped.info.workspace != target {
		return claudeSession{}, fmt.Errorf("remapped transcript starts in %s, not %s", remapped.info.workspace, target)
	}
	return remapped, nil
}

// projectFolder returns the folder where Claude Code keeps the sessions of
// workspace.
func (s *Store) projectFolder(workspace string) string {
	return filepath.Join(s.home, "projects", projectFolderName(workspace))
}

// importPlan lists the file changes of one import.
type importPlan struct {
	// folder is the project folder the session is imported into.
	folder  string
	outcome provider.ImportOutcome
	// writes are the files to place in folder, the session transcript last.
	writes []sessionFile
	// removals are absolute paths of local files the archive replaces: files
	// of a diverged copy that the archive lacks, and copies of the session in
	// other project folders.
	removals []string
}

func (p importPlan) changes() bool {
	return len(p.writes) > 0 || len(p.removals) > 0
}

// fileComparison is how a local file relates to the archived one.
type fileComparison int

const (
	sameFile fileComparison = iota
	// incomingAhead means the archive adds the file or extends its transcript.
	incomingAhead
	// localAhead means the local copy has the file alone or extends it.
	localAhead
	diverged
	// metadataChanged means sub-agent metadata differs, which follows the
	// direction of the rest of the session.
	metadataChanged
)

// planImport compares the archived session with the local copy and decides
// every file change.
func (s *Store) planImport(incoming claudeSession, replace bool) (importPlan, error) {
	plan := importPlan{folder: s.projectFolder(incoming.info.workspace)}
	folders, err := transcriptFolders(s.home, incoming.id)
	if err != nil {
		return importPlan{}, err
	}
	for _, folder := range folders {
		if folder == plan.folder {
			continue
		}
		if !replace {
			return importPlan{}, fmt.Errorf("Claude Code session %s exists for workspace %s; import it with --cwd there, or pass --replace to move it", incoming.id, readFolderWorkspace(folder, incoming.id))
		}
		other, err := readLocalFiles(folder, incoming.id)
		if err != nil {
			return importPlan{}, err
		}
		for _, file := range other {
			plan.removals = append(plan.removals, filepath.Join(folder, filepath.FromSlash(file.path)))
		}
	}
	localFiles, err := readLocalFiles(plan.folder, incoming.id)
	if err != nil {
		return importPlan{}, err
	}
	local := make(map[string][]byte, len(localFiles))
	for _, file := range localFiles {
		local[file.path] = file.data
	}

	comparisons := make([]fileComparison, len(incoming.files))
	seen := map[fileComparison]bool{}
	for index, file := range incoming.files {
		comparisons[index] = compareFile(file, local)
		seen[comparisons[index]] = true
		delete(local, file.path)
	}
	if len(local) > 0 {
		seen[localAhead] = true
	}
	isDiverged := seen[diverged] || (seen[incomingAhead] && seen[localAhead]) ||
		(seen[metadataChanged] && !seen[incomingAhead] && !seen[localAhead])
	switch {
	case isDiverged && !replace:
		return importPlan{}, fmt.Errorf("the local copy of session %s has diverged from the archive; pass --replace to overwrite it", incoming.id)
	case isDiverged || len(plan.removals) > 0:
		plan.outcome = provider.Replaced
		for path := range local {
			plan.removals = append(plan.removals, filepath.Join(plan.folder, filepath.FromSlash(path)))
		}
		slices.Sort(plan.removals)
	case seen[incomingAhead] && len(localFiles) == 0:
		plan.outcome = provider.Imported
	case seen[incomingAhead]:
		plan.outcome = provider.Updated
	case seen[localAhead]:
		plan.outcome = provider.KeptNewer
		return plan, nil
	default:
		plan.outcome = provider.Unchanged
		return plan, nil
	}
	// Only a replacement can meet a file the local copy extends.
	for index, file := range incoming.files {
		if comparisons[index] != sameFile {
			plan.writes = append(plan.writes, file)
		}
	}
	// The transcript is written last, so Claude Code never finds a session
	// whose sub-agent files are missing.
	slices.SortStableFunc(plan.writes, func(a, b sessionFile) int {
		return boolOrder(a.kind == transcriptKind) - boolOrder(b.kind == transcriptKind)
	})
	if err := requireClosed(s.home, incoming.id); err != nil {
		return importPlan{}, err
	}
	return plan, nil
}

func boolOrder(value bool) int {
	if value {
		return 1
	}
	return 0
}

// compareFile compares an archived file with its local copy, if any.
func compareFile(file sessionFile, local map[string][]byte) fileComparison {
	existing, ok := local[file.path]
	switch {
	case !ok:
		return incomingAhead
	case bytes.Equal(existing, file.data):
		return sameFile
	case file.kind == agentMetadataKind:
		return metadataChanged
	case !file.isTranscript():
		return diverged
	case bytes.HasPrefix(file.data, existing):
		return incomingAhead
	case bytes.HasPrefix(existing, file.data):
		return localAhead
	default:
		return diverged
	}
}

// execute places the planned files, then removes the replaced ones, so an
// interruption leaves the archived session in place.
func (p importPlan) execute() error {
	for _, file := range p.writes {
		destination := filepath.Join(p.folder, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := fsutil.WriteAtomic(destination, file.data); err != nil {
			return fmt.Errorf("write %s: %w", file.path, err)
		}
	}
	for _, path := range p.removals {
		if err := fsutil.RemoveIfExists(path); err != nil {
			return fmt.Errorf("remove replaced session file: %w", err)
		}
		removeEmptyParents(path)
	}
	return nil
}

// removeEmptyParents removes the folders a removal left empty, up to and
// including the project folder.
func removeEmptyParents(path string) {
	for directory := filepath.Dir(path); filepath.Base(directory) != "projects"; directory = filepath.Dir(directory) {
		if os.Remove(directory) != nil {
			return
		}
	}
}

// verifyImport reads the imported session back and requires the archived
// files, as remapped, in its project folder.
func verifyImport(folder string, incoming claudeSession) error {
	stored, err := readSession(folder, incoming.id)
	if err != nil {
		return err
	}
	files := make(map[string][]byte, len(stored.files))
	for _, file := range stored.files {
		files[file.path] = file.data
	}
	for _, file := range incoming.files {
		data, ok := files[file.path]
		if !ok {
			return fmt.Errorf("%s is missing", file.path)
		}
		// A transcript may have grown since it was written, if Claude Code
		// resumed it in the meantime.
		matches := bytes.Equal(data, file.data) || (file.isTranscript() && bytes.HasPrefix(data, file.data))
		if !matches {
			return errors.New(file.path + " does not match the archive")
		}
	}
	return nil
}
