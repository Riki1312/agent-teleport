package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/pathmap"
)

// maxSessionFiles bounds how many rollout files one session may span.
const maxSessionFiles = 4096

// historyBase is the reference a rollout's session_meta makes to a prefix of
// another rollout object: the records before EndOrdinalExclusive, which end at
// EndByteOffset. Codex writes it for forks, which reference another thread, and
// for reverts, which reference an earlier object of the same thread.
type historyBase struct {
	// ObjectID names the referenced rollout object. Despite its JSON name, it
	// is the thread ID only for a thread's original rollout; a revert snapshot
	// has an object ID of its own.
	ObjectID            string `json:"thread_id"`
	EndOrdinalExclusive int64  `json:"end_ordinal_exclusive"`
	EndByteOffset       int64  `json:"end_byte_offset"`
}

// rolloutFile is one physical rollout object: a thread's original rollout, or a
// snapshot that Codex writes when the thread is reverted.
type rolloutFile struct {
	// name is the native file name, derived from the local creation time, the
	// thread ID, and for a snapshot its object ID.
	name   string
	object string
	data   []byte
	meta   sessionMeta
	// location is the local file the object was read from, if any.
	location string
}

func (f rolloutFile) isSnapshot() bool {
	return f.object != f.meta.ID
}

// codexSession is every rollout object a session needs: all objects of its
// root thread and of the threads it spawned, which share its session ID, and
// the referenced prefixes of ancestors they fork from. Files are ordered so
// that every referenced object precedes the files that reference it.
type codexSession struct {
	root  string
	files []rolloutFile
	// turns is the number of turns the root thread replays.
	turns int
}

// head returns the current object of a thread: its newest snapshot, or its
// original rollout when it was never reverted.
func (s codexSession) head(thread string) rolloutFile {
	var head rolloutFile
	for _, file := range s.files {
		if file.meta.ID == thread {
			head = file
		}
	}
	return head
}

// threads returns the session's threads in file order, root first.
func (s codexSession) threads() []string {
	var threads []string
	seen := map[string]bool{}
	for _, file := range s.files {
		if !seen[file.meta.ID] {
			seen[file.meta.ID] = true
			threads = append(threads, file.meta.ID)
		}
	}
	return threads
}

func (s codexSession) payloads() []archive.Payload {
	payloads := make([]archive.Payload, 0, len(s.files))
	for _, file := range s.files {
		payloads = append(payloads, archive.Payload{Path: file.name, Data: file.data})
	}
	return payloads
}

// rolloutName matches native rollout file names; a second UUID names the
// object of a revert snapshot.
var rolloutName = regexp.MustCompile(`^rollout-(\d{4})-(\d{2})-(\d{2})T\d{2}-\d{2}-\d{2}-([0-9a-fA-F-]{36})(?:_([0-9a-fA-F-]{36}))?\.jsonl$`)

// newRolloutFile parses one rollout object and checks that its name matches the
// thread it records.
func newRolloutFile(name string, data []byte) (rolloutFile, error) {
	match := rolloutName.FindStringSubmatch(name)
	if match == nil {
		return rolloutFile{}, fmt.Errorf("unsupported rollout file name %q", name)
	}
	meta, err := parseRollout(data)
	if err != nil {
		return rolloutFile{}, fmt.Errorf("%s: %w", name, err)
	}
	if match[4] != meta.ID {
		return rolloutFile{}, fmt.Errorf("rollout file %s records thread %s", name, meta.ID)
	}
	object := match[4]
	if match[5] != "" {
		object = match[5]
		if meta.Base == nil {
			return rolloutFile{}, fmt.Errorf("snapshot %s does not reference the history it reverts", name)
		}
	}
	return rolloutFile{name: name, object: object, data: data, meta: meta}, nil
}

// activeRolloutPath returns where Codex keeps an active rollout with the given
// native name: below the local creation date encoded in the name.
func activeRolloutPath(home, name string) (string, error) {
	match := rolloutName.FindStringSubmatch(name)
	if match == nil {
		return "", fmt.Errorf("unsupported rollout file name %q", name)
	}
	return filepath.Join(home, "sessions", match[1], match[2], match[3], name), nil
}

// readSession reads the session whose root thread has a rollout at path, with
// the threads it spawned and the ancestors they fork from. The root thread's
// current object is determined from its local files, because Codex can report
// the original rollout of a reverted thread.
func (l layout) readSession(home, path string) (codexSession, error) {
	reported, err := readRolloutFile(path)
	if err != nil {
		return codexSession{}, err
	}
	root, err := threadHead(home, reported.meta.ID)
	if err != nil {
		return codexSession{}, err
	}
	if root.meta.SessionID != root.meta.ID {
		return codexSession{}, fmt.Errorf("thread %s was spawned by session %s; export that session instead", root.meta.ID, root.meta.SessionID)
	}
	heads := []rolloutFile{root}
	spawned, err := spawnedThreadHeads(home, root.meta.ID)
	if err != nil {
		return codexSession{}, err
	}
	heads = append(heads, spawned...)

	collected := map[string]rolloutFile{}
	var order []string
	var collect func(file rolloutFile, depth int) error
	collect = func(file rolloutFile, depth int) error {
		if depth > maxSessionFiles {
			return errors.New("session history references too many rollouts")
		}
		if existing, ok := collected[file.object]; ok && len(existing.data) >= len(file.data) {
			return nil
		}
		if base := file.meta.Base; base != nil {
			ancestor, err := readObject(home, *base)
			if err != nil {
				return err
			}
			inSession := ancestor.meta.SessionID == root.meta.ID || ancestor.meta.ID == root.meta.ID
			if !inSession {
				ancestor.data = ancestor.data[:base.EndByteOffset]
			}
			if err := collect(ancestor, depth+1); err != nil {
				return err
			}
		}
		if _, ok := collected[file.object]; !ok {
			order = append(order, file.object)
		}
		collected[file.object] = file
		return nil
	}
	for _, head := range heads {
		if err := collect(head, 0); err != nil {
			return codexSession{}, err
		}
	}
	files := make([]rolloutFile, 0, len(order))
	for _, object := range order {
		files = append(files, collected[object])
	}
	return l.validateSession(root.meta.ID, files)
}

// threadHead returns the current object of a local thread.
func threadHead(home, thread string) (rolloutFile, error) {
	stored, err := findSession(home, thread)
	if err != nil {
		return rolloutFile{}, err
	}
	var objects []rolloutFile
	for _, path := range append(append(append([]string{}, stored.active...), stored.archived...), stored.snapshots...) {
		object, err := readRolloutFile(path)
		if err != nil {
			return rolloutFile{}, err
		}
		objects = append(objects, object)
	}
	if len(objects) == 0 {
		return rolloutFile{}, fmt.Errorf("thread %s has no local rollout", thread)
	}
	return currentObject(objects)
}

// readRolloutFile reads and parses the rollout object at path.
func readRolloutFile(path string) (rolloutFile, error) {
	data, err := readStable(path)
	if err != nil {
		return rolloutFile{}, err
	}
	return newRolloutFile(filepath.Base(path), data)
}

// readObject reads the local rollout object that base references.
func readObject(home string, base historyBase) (rolloutFile, error) {
	paths, err := objectPaths(home, base.ObjectID)
	if err != nil {
		return rolloutFile{}, err
	}
	if len(paths) != 1 {
		return rolloutFile{}, fmt.Errorf("referenced rollout %s has %d local files, want 1", base.ObjectID, len(paths))
	}
	file, err := readRolloutFile(paths[0])
	if err != nil {
		return rolloutFile{}, err
	}
	if base.EndByteOffset > int64(len(file.data)) {
		return rolloutFile{}, fmt.Errorf("rollout %s is shorter than the history that references it", base.ObjectID)
	}
	return file, nil
}

// objectPaths returns the local files of a rollout object in the active and
// archived stores.
func objectPaths(home, object string) ([]string, error) {
	var paths []string
	err := walkRollouts(home, func(path string, match []string) error {
		if match[5] == object || (match[5] == "" && match[4] == object) {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

// spawnedThreadHeads returns the current rollout of every thread whose session
// ID names root, other than root itself. A thread's current rollout is the
// object that no other object of the same thread references.
func spawnedThreadHeads(home, root string) ([]rolloutFile, error) {
	byThread := map[string][]rolloutFile{}
	var threads []string
	err := walkRollouts(home, func(path string, match []string) error {
		if match[4] == root {
			return nil
		}
		sessionID, err := firstLineSessionID(path)
		if err != nil || sessionID != root {
			return err
		}
		file, err := readRolloutFile(path)
		if err != nil {
			return err
		}
		if _, ok := byThread[file.meta.ID]; !ok {
			threads = append(threads, file.meta.ID)
		}
		byThread[file.meta.ID] = append(byThread[file.meta.ID], file)
		return nil
	})
	if err != nil {
		return nil, err
	}
	heads := make([]rolloutFile, 0, len(threads))
	for _, thread := range threads {
		head, err := currentObject(byThread[thread])
		if err != nil {
			return nil, err
		}
		heads = append(heads, head)
	}
	return heads, nil
}

// currentObject returns the object of one thread that no other object of the
// thread references.
func currentObject(objects []rolloutFile) (rolloutFile, error) {
	referenced := map[string]bool{}
	for _, object := range objects {
		if object.meta.Base != nil {
			referenced[object.meta.Base.ObjectID] = true
		}
	}
	var heads []rolloutFile
	for _, object := range objects {
		if !referenced[object.object] {
			heads = append(heads, object)
		}
	}
	if len(heads) != 1 {
		return rolloutFile{}, fmt.Errorf("thread %s has %d current rollouts, want 1", objects[0].meta.ID, len(heads))
	}
	return heads[0], nil
}

// firstLineSessionID reads the session ID from a rollout's first record without
// reading the whole file.
func firstLineSessionID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReaderSize(file, 64<<10)
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		// ReadSlice reuses its buffer, so each chunk is copied before the next
		// read.
		line = append(line, chunk...)
		if len(line) > 64<<20 {
			return "", fmt.Errorf("%s: session metadata is too large", path)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			// Codex is still writing the first record of a new rollout.
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		break
	}
	var first struct {
		Payload struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &first); err != nil {
		return "", fmt.Errorf("decode the first record of %s: %w", path, err)
	}
	if first.Payload.SessionID != "" {
		return first.Payload.SessionID, nil
	}
	return first.Payload.ID, nil
}

// walkRollouts calls visit for every rollout file in the active and archived
// stores with its file name match.
func walkRollouts(home string, visit func(path string, match []string) error) error {
	for _, root := range []string{filepath.Join(home, "sessions"), filepath.Join(home, "archived_sessions")} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if path == root && errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if match := rolloutName.FindStringSubmatch(entry.Name()); match != nil {
				return visit(path, match)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("search stored sessions: %w", err)
		}
	}
	return nil
}

// sessionFromArchive rebuilds the session stored in a Codex archive.
func (l layout) sessionFromArchive(source archive.Archive) (codexSession, error) {
	files := make([]rolloutFile, 0, len(source.Payloads))
	for _, payload := range source.Payloads {
		file, err := newRolloutFile(payload.Path, payload.Data)
		if err != nil {
			return codexSession{}, err
		}
		files = append(files, file)
	}
	return l.validateSession(source.Manifest.SessionID, files)
}

// validateSession checks that files form one consistent session rooted at
// root: unique objects, references that resolve to earlier objects at record
// boundaries, revert snapshots that extend their own thread, and no thread
// with a turn in progress. The root thread must end after a completed turn.
func (l layout) validateSession(root string, files []rolloutFile) (codexSession, error) {
	if len(files) == 0 || len(files) > maxSessionFiles {
		return codexSession{}, fmt.Errorf("session must have between 1 and %d rollouts", maxSessionFiles)
	}
	objects := map[string]rolloutFile{}
	threadObjects := map[string]int{}
	for _, file := range files {
		if _, duplicate := objects[file.object]; duplicate {
			return codexSession{}, fmt.Errorf("session contains rollout %s twice", file.object)
		}
		if base := file.meta.Base; base != nil {
			target, ok := objects[base.ObjectID]
			if !ok {
				return codexSession{}, fmt.Errorf("%s references rollout %s, which is missing or stored after it", file.name, base.ObjectID)
			}
			if err := checkReference(target, *base); err != nil {
				return codexSession{}, fmt.Errorf("%s: %w", file.name, err)
			}
			if file.isSnapshot() != (target.meta.ID == file.meta.ID) {
				return codexSession{}, fmt.Errorf("%s mixes a revert and a fork in one reference", file.name)
			}
		} else if file.isSnapshot() {
			return codexSession{}, fmt.Errorf("snapshot %s does not reference the history it reverts", file.name)
		}
		objects[file.object] = file
		threadObjects[file.meta.ID]++
	}
	session := codexSession{root: root, files: files}
	if _, ok := threadObjects[root]; !ok {
		return codexSession{}, fmt.Errorf("session is missing its root thread %s", root)
	}
	for _, thread := range session.threads() {
		head := session.head(thread)
		state, err := l.replayTurns(objects, head)
		if err != nil {
			return codexSession{}, err
		}
		if state.open {
			return codexSession{}, fmt.Errorf("thread %s has a turn in progress", thread)
		}
		if thread == root {
			if state.completed == 0 || head.meta.SessionID != root {
				return codexSession{}, errors.New("rollout does not end after a completed turn")
			}
			session.turns = state.started
		}
	}
	return session, nil
}

// checkReference requires base to end at a record boundary of target, after
// the record before the referenced ordinal.
func checkReference(target rolloutFile, base historyBase) error {
	if base.EndByteOffset > int64(len(target.data)) || target.data[base.EndByteOffset-1] != '\n' {
		return fmt.Errorf("reference to %s does not end at a record boundary", base.ObjectID)
	}
	ordinal, err := lastOrdinal(target.data[:base.EndByteOffset])
	if err != nil {
		return fmt.Errorf("%s: %w", target.name, err)
	}
	if ordinal != base.EndOrdinalExclusive-1 {
		return fmt.Errorf("reference to %s ends at ordinal %d, want %d", base.ObjectID, ordinal, base.EndOrdinalExclusive-1)
	}
	return nil
}

// replayTurns applies the turn boundaries of a thread's logical history, from
// its oldest referenced prefix to head, to a new turn state.
func (l layout) replayTurns(objects map[string]rolloutFile, head rolloutFile) (turnState, error) {
	chain := []historySegment{{file: head, end: int64(len(head.data))}}
	for current := head; current.meta.Base != nil; {
		target := objects[current.meta.Base.ObjectID]
		chain = append([]historySegment{{file: target, end: current.meta.Base.EndByteOffset}}, chain...)
		current = target
		if len(chain) > maxSessionFiles {
			return turnState{}, errors.New("session history references form a cycle")
		}
	}
	var state turnState
	for _, segment := range chain {
		if err := l.advanceTurns(&state, segment.file.data[:segment.end]); err != nil {
			return turnState{}, fmt.Errorf("%s: %w", segment.file.name, err)
		}
	}
	return state, nil
}

// historySegment is the part of a rollout object that a logical history uses.
type historySegment struct {
	file rolloutFile
	end  int64
}

// lastOrdinal returns the ordinal of the final record in a rollout.
func lastOrdinal(data []byte) (int64, error) {
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	var record struct {
		Ordinal *int64 `json:"ordinal"`
	}
	if err := json.Unmarshal(lines[len(lines)-1], &record); err != nil || record.Ordinal == nil {
		return 0, errors.New("final rollout record has no ordinal")
	}
	return *record.Ordinal, nil
}

// relocateSession moves every path that mapping covers in every file of the
// session. Rewriting a file changes its length, so every reference into it is
// moved to the same record boundary in the rewritten file.
func (l layout) relocateSession(session codexSession, mapping pathmap.Mapping) (codexSession, error) {
	if mapping.IsEmpty() {
		return session, nil
	}
	boundaries := map[string]boundaryMap{}
	files := make([]rolloutFile, 0, len(session.files))
	for _, file := range session.files {
		data := file.data
		if base := file.meta.Base; base != nil {
			offset, err := boundaries[base.ObjectID].moved(base.EndByteOffset)
			if err != nil {
				return codexSession{}, fmt.Errorf("%s: %w", file.name, err)
			}
			if data, err = rewriteBase(data, *base, base.ObjectID, offset); err != nil {
				return codexSession{}, fmt.Errorf("%s: %w", file.name, err)
			}
		}
		remapped, _, err := relocateLines(data, mapping)
		if err != nil {
			return codexSession{}, fmt.Errorf("%s: %w", file.name, err)
		}
		parsed, err := newRolloutFile(file.name, remapped)
		if err != nil {
			return codexSession{}, err
		}
		boundaries[file.object] = newBoundaryMap(file.data, remapped)
		files = append(files, parsed)
	}
	return l.validateSession(session.root, files)
}

// boundaryMap moves record boundaries of a rollout to the same record in a
// rewritten copy with the same number of records.
type boundaryMap struct {
	old, new []int64
}

func newBoundaryMap(original, rewritten []byte) boundaryMap {
	return boundaryMap{old: recordEnds(original), new: recordEnds(rewritten)}
}

func recordEnds(data []byte) []int64 {
	var ends []int64
	for index, character := range data {
		if character == '\n' {
			ends = append(ends, int64(index+1))
		}
	}
	return ends
}

func (m boundaryMap) moved(offset int64) (int64, error) {
	for index, end := range m.old {
		if end == offset && index < len(m.new) {
			return m.new[index], nil
		}
	}
	return 0, errors.New("referenced offset is not a record boundary of the rewritten rollout")
}

// baseObject and baseOffset match the object and byte offset of a
// history_base reference.
var (
	baseObject = regexp.MustCompile(`"history_base"\s*:\s*\{[^{}]*?"thread_id"\s*:\s*"([0-9a-fA-F-]{36})"`)
	baseOffset = regexp.MustCompile(`"end_byte_offset"\s*:\s*(\d+)`)
)

// rewriteBase points the history_base reference in the first record of data at
// object and offset, leaving every other byte unchanged.
func rewriteBase(data []byte, current historyBase, object string, offset int64) ([]byte, error) {
	firstLine, rest, _ := bytes.Cut(data, []byte{'\n'})
	objects := baseObject.FindAllSubmatchIndex(firstLine, -1)
	offsets := baseOffset.FindAllSubmatchIndex(firstLine, -1)
	if len(objects) != 1 || len(offsets) != 1 {
		return nil, errors.New("session metadata must contain exactly one history_base reference")
	}
	if string(firstLine[objects[0][2]:objects[0][3]]) != current.ObjectID ||
		string(firstLine[offsets[0][2]:offsets[0][3]]) != strconv.FormatInt(current.EndByteOffset, 10) {
		return nil, errors.New("history_base reference does not match its decoded value")
	}
	replacements := []struct {
		start, end int
		value      string
	}{
		{objects[0][2], objects[0][3], object},
		{offsets[0][2], offsets[0][3], strconv.FormatInt(offset, 10)},
	}
	if replacements[0].start > replacements[1].start {
		replacements[0], replacements[1] = replacements[1], replacements[0]
	}
	var output bytes.Buffer
	copied := 0
	for _, replacement := range replacements {
		output.Write(firstLine[copied:replacement.start])
		output.WriteString(replacement.value)
		copied = replacement.end
	}
	output.Write(firstLine[copied:])
	output.WriteByte('\n')
	output.Write(rest)
	return output.Bytes(), nil
}

// revertedTurn returns the ID of the turn that a revert snapshot removed: the
// turn whose start record has the referenced ordinal in the referenced object.
func revertedTurn(target rolloutFile, base historyBase) (string, error) {
	if line, _, _ := bytes.Cut(target.data[base.EndByteOffset:], []byte{'\n'}); len(line) > 0 {
		var record struct {
			Ordinal int64 `json:"ordinal"`
			Payload struct {
				Type   string `json:"type"`
				TurnID string `json:"turn_id"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return "", err
		}
		if record.Ordinal == base.EndOrdinalExclusive && record.Payload.Type == "task_started" && record.Payload.TurnID != "" {
			return record.Payload.TurnID, nil
		}
	}
	return "", fmt.Errorf("revert of %s does not start at a turn", base.ObjectID)
}

// snapshotContinuation returns the records a snapshot holds after the header
// that Codex writes when it reverts a thread.
func (l layout) snapshotContinuation(snapshot rolloutFile) ([]byte, error) {
	data := snapshot.data
	for index := 0; index < l.revertHeaderRecords; index++ {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			return nil, fmt.Errorf("snapshot %s is shorter than a revert header", snapshot.name)
		}
		data = data[end+1:]
	}
	return data, nil
}

// isSessionThread reports whether a file belongs to the session rooted at root
// rather than to an ancestor it forks from.
func isSessionThread(file rolloutFile, root string) bool {
	return file.meta.ID == root || file.meta.SessionID == root
}
