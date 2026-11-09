package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/Riki1312/agent-teleport/internal/archive"
)

// maxHistoryDepth bounds how many ancestors a session history may have.
const maxHistoryDepth = 64

// historyBase is the reference a forked rollout's session_meta makes to a prefix
// of another thread's rollout: the records before EndOrdinalExclusive, which
// end at EndByteOffset.
type historyBase struct {
	ThreadID            string `json:"thread_id"`
	EndOrdinalExclusive int64  `json:"end_ordinal_exclusive"`
	EndByteOffset       int64  `json:"end_byte_offset"`
}

// rolloutFile is one physical rollout file of a session history.
type rolloutFile struct {
	// name is the native file name, which Codex derives from the creation time
	// and thread ID.
	name string
	data []byte
	meta sessionMeta
}

// history is a session's own rollout together with the ancestor prefixes it
// extends. Files are ordered root first and the session's rollout is last;
// every ancestor holds exactly the prefix its descendant references.
type history struct {
	files []rolloutFile
	// turns is the number of turns the whole history replays.
	turns int
}

func (h history) session() rolloutFile {
	return h.files[len(h.files)-1]
}

func (h history) payloads() []archive.Payload {
	payloads := make([]archive.Payload, 0, len(h.files))
	for _, file := range h.files {
		payloads = append(payloads, archive.Payload{Path: file.name, Data: file.data})
	}
	return payloads
}

// rolloutName matches native rollout file names. A second UUID marks a
// snapshot that Codex writes when a thread is reverted.
var rolloutName = regexp.MustCompile(`^rollout-(\d{4})-(\d{2})-(\d{2})T\d{2}-\d{2}-\d{2}-([0-9a-fA-F-]{36})(_[0-9a-fA-F-]{36})?\.jsonl$`)

// activeRolloutPath returns where Codex keeps an active rollout with the given
// native name: below the local creation date encoded in the name.
func activeRolloutPath(home, name string) (string, error) {
	match := rolloutName.FindStringSubmatch(name)
	if match == nil || match[5] != "" {
		return "", fmt.Errorf("unsupported rollout file name %q", name)
	}
	return filepath.Join(home, "sessions", match[1], match[2], match[3], name), nil
}

// newRolloutFile parses one rollout file and checks that its name matches the
// thread it records.
func newRolloutFile(name string, data []byte) (rolloutFile, error) {
	match := rolloutName.FindStringSubmatch(name)
	if match == nil {
		return rolloutFile{}, fmt.Errorf("unsupported rollout file name %q", name)
	}
	if match[5] != "" {
		return rolloutFile{}, errors.New("reverted Codex threads are not supported: their current history is a snapshot that Codex locates through its own database")
	}
	meta, err := parseRollout(data)
	if err != nil {
		return rolloutFile{}, fmt.Errorf("%s: %w", name, err)
	}
	if match[4] != meta.ID {
		return rolloutFile{}, fmt.Errorf("rollout file %s records thread %s", name, meta.ID)
	}
	return rolloutFile{name: name, data: data, meta: meta}, nil
}

// readHistory reads the session rollout at path and the ancestor prefixes it
// references from the local Codex stores.
func (l layout) readHistory(home, path string) (history, error) {
	data, err := readStable(path)
	if err != nil {
		return history{}, err
	}
	file, err := newRolloutFile(filepath.Base(path), data)
	if err != nil {
		return history{}, err
	}
	stored, err := findSession(home, file.meta.ID)
	if err != nil {
		return history{}, err
	}
	if len(stored.snapshots) > 0 {
		return history{}, fmt.Errorf("session %s has been reverted; reverted Codex threads are not supported", file.meta.ID)
	}
	files := []rolloutFile{file}
	for base := file.meta.Base; base != nil; base = files[0].meta.Base {
		if len(files) > maxHistoryDepth {
			return history{}, errors.New("session history has too many ancestors")
		}
		ancestor, err := readAncestor(home, *base)
		if err != nil {
			return history{}, err
		}
		files = append([]rolloutFile{ancestor}, files...)
	}
	return l.validateHistory(files)
}

// readAncestor reads the prefix of the local rollout that base references.
func readAncestor(home string, base historyBase) (rolloutFile, error) {
	stored, err := findSession(home, base.ThreadID)
	if err != nil {
		return rolloutFile{}, err
	}
	if len(stored.snapshots) > 0 {
		return rolloutFile{}, fmt.Errorf("ancestor session %s has been reverted; reverted Codex threads are not supported", base.ThreadID)
	}
	paths := append(append([]string{}, stored.active...), stored.archived...)
	if len(paths) != 1 {
		return rolloutFile{}, fmt.Errorf("ancestor session %s has %d rollouts, want 1", base.ThreadID, len(paths))
	}
	data, err := readStable(paths[0])
	if err != nil {
		return rolloutFile{}, err
	}
	if base.EndByteOffset > int64(len(data)) {
		return rolloutFile{}, fmt.Errorf("ancestor session %s is shorter than the history that references it", base.ThreadID)
	}
	return newRolloutFile(filepath.Base(paths[0]), data[:base.EndByteOffset])
}

// historyFromArchive rebuilds the history stored in a Codex archive, whose
// payloads are native rollout files ordered root first.
func (l layout) historyFromArchive(source archive.Archive) (history, error) {
	files := make([]rolloutFile, 0, len(source.Payloads))
	for _, payload := range source.Payloads {
		file, err := newRolloutFile(payload.Path, payload.Data)
		if err != nil {
			return history{}, err
		}
		files = append(files, file)
	}
	return l.validateHistory(files)
}

// validateHistory checks that files form one linear history ending at a
// completed turn, and counts its turns.
func (l layout) validateHistory(files []rolloutFile) (history, error) {
	if len(files) == 0 || len(files) > maxHistoryDepth+1 {
		return history{}, fmt.Errorf("session history must have between 1 and %d rollouts", maxHistoryDepth+1)
	}
	seen := make(map[string]struct{}, len(files))
	var state turnState
	for index, file := range files {
		if _, duplicate := seen[file.meta.ID]; duplicate {
			return history{}, fmt.Errorf("session history contains thread %s twice", file.meta.ID)
		}
		seen[file.meta.ID] = struct{}{}
		if err := checkBase(files, index); err != nil {
			return history{}, err
		}
		if err := l.advanceTurns(&state, file.data); err != nil {
			return history{}, fmt.Errorf("%s: %w", file.name, err)
		}
	}
	if state.open || state.completed == 0 {
		return history{}, errors.New("rollout does not end after a completed turn")
	}
	return history{files: files, turns: state.started}, nil
}

// checkBase requires the file at index to reference exactly the whole previous
// file, which must end with the record before the referenced ordinal.
func checkBase(files []rolloutFile, index int) error {
	base := files[index].meta.Base
	if index == 0 {
		if base != nil {
			return fmt.Errorf("session history is missing ancestor %s", base.ThreadID)
		}
		return nil
	}
	ancestor := files[index-1]
	if base == nil || base.ThreadID != ancestor.meta.ID || base.EndByteOffset != int64(len(ancestor.data)) {
		return fmt.Errorf("%s does not extend the ancestor stored before it", files[index].name)
	}
	ordinal, err := lastOrdinal(ancestor.data)
	if err != nil {
		return fmt.Errorf("%s: %w", ancestor.name, err)
	}
	if ordinal != base.EndOrdinalExclusive-1 {
		return fmt.Errorf("%s references ordinal %d, but its ancestor ends at ordinal %d", files[index].name, base.EndOrdinalExclusive, ordinal)
	}
	return nil
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

// remapHistory moves every file of h from the session's workspace to target.
// Rewriting an ancestor changes its length, so each descendant's byte offset is
// updated to the rewritten ancestor's length.
func (l layout) remapHistory(h history, target string) (history, error) {
	source := h.session().meta.CWD
	if source == target {
		return h, nil
	}
	files := make([]rolloutFile, 0, len(h.files))
	for index, file := range h.files {
		data := file.data
		if index > 0 {
			var err error
			data, err = rewriteBaseOffset(data, file.meta.Base.EndByteOffset, int64(len(files[index-1].data)))
			if err != nil {
				return history{}, fmt.Errorf("%s: %w", file.name, err)
			}
		}
		data, changed, err := l.remapLines(data, source, target)
		if err != nil {
			return history{}, fmt.Errorf("%s: %w", file.name, err)
		}
		if index == len(h.files)-1 && changed == 0 {
			return history{}, errors.New("rollout contains no recognized workspace metadata")
		}
		remapped, err := newRolloutFile(file.name, data)
		if err != nil {
			return history{}, err
		}
		files = append(files, remapped)
	}
	return l.validateHistory(files)
}

// endByteOffset matches the byte offset in a history_base reference.
var endByteOffset = regexp.MustCompile(`"end_byte_offset"\s*:\s*(\d+)`)

// rewriteBaseOffset replaces the history_base byte offset in the first line of
// data, leaving every other byte unchanged.
func rewriteBaseOffset(data []byte, from, to int64) ([]byte, error) {
	firstLine, rest, _ := bytes.Cut(data, []byte{'\n'})
	matches := endByteOffset.FindAllSubmatchIndex(firstLine, -1)
	if len(matches) != 1 {
		return nil, errors.New("session metadata must contain exactly one history_base byte offset")
	}
	digits := firstLine[matches[0][2]:matches[0][3]]
	if string(digits) != strconv.FormatInt(from, 10) {
		return nil, errors.New("history_base byte offset does not match the decoded reference")
	}
	var output bytes.Buffer
	output.Write(firstLine[:matches[0][2]])
	output.WriteString(strconv.FormatInt(to, 10))
	output.Write(firstLine[matches[0][3]:])
	output.WriteByte('\n')
	output.Write(rest)
	return output.Bytes(), nil
}
