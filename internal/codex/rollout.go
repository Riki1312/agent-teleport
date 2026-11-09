package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sessionMeta is the identity recorded in a rollout's first event.
type sessionMeta struct {
	// ID is the thread, and SessionID the session whose root thread spawned it;
	// they are equal for a session's root thread.
	ID        string
	SessionID string
	CWD       string
	Timestamp time.Time
	// Base is the ancestor prefix a forked rollout extends, or nil for a
	// rollout that holds its whole history.
	Base *historyBase
}

// turnState tracks turn boundaries across the records of a history.
type turnState struct {
	open      bool
	started   int
	completed int
}

// readStable reads a rollout and fails when the file changed during the read.
func readStable(path string) ([]byte, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat rollout: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("rollout is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rollout: %w", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat rollout after read: %w", err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("rollout changed while it was being read")
	}
	return data, nil
}

// parseRollout checks that data is a sequence of complete JSON records starting
// with session metadata, and returns the recorded identity. It does not check
// turn boundaries, which depend on the whole history.
func parseRollout(data []byte) (sessionMeta, error) {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return sessionMeta{}, errors.New("rollout does not end with a complete JSONL record")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines[0]) == 0 {
		return sessionMeta{}, errors.New("rollout has no session metadata")
	}
	var first struct {
		Type    string `json:"type"`
		Payload struct {
			ID          string       `json:"id"`
			SessionID   string       `json:"session_id"`
			CWD         string       `json:"cwd"`
			Timestamp   string       `json:"timestamp"`
			HistoryBase *historyBase `json:"history_base"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(lines[0], &first); err != nil {
		return sessionMeta{}, fmt.Errorf("decode session metadata: %w", err)
	}
	if first.Type != "session_meta" {
		return sessionMeta{}, fmt.Errorf("first rollout event has type %q, want session_meta", first.Type)
	}
	id := first.Payload.ID
	if id == "" {
		id = first.Payload.SessionID
	}
	sessionID := first.Payload.SessionID
	if sessionID == "" {
		sessionID = id
	}
	if id == "" || first.Payload.CWD == "" || first.Payload.Timestamp == "" {
		return sessionMeta{}, errors.New("session metadata is missing id, cwd, or timestamp")
	}
	if !validSessionID(id) || !validSessionID(sessionID) {
		return sessionMeta{}, fmt.Errorf("session ID %q is not a UUIDv7", id)
	}
	if base := first.Payload.HistoryBase; base != nil && (!validSessionID(base.ObjectID) || base.EndByteOffset <= 0 || base.EndOrdinalExclusive <= 0) {
		return sessionMeta{}, errors.New("session metadata has an invalid history_base reference")
	}
	timestamp, err := time.Parse(time.RFC3339Nano, first.Payload.Timestamp)
	if err != nil {
		return sessionMeta{}, fmt.Errorf("parse session timestamp: %w", err)
	}
	for index, line := range lines[1:] {
		if len(line) == 0 {
			return sessionMeta{}, fmt.Errorf("rollout line %d is empty", index+2)
		}
		if !json.Valid(line) {
			return sessionMeta{}, fmt.Errorf("rollout line %d is not valid JSON", index+2)
		}
	}
	return sessionMeta{ID: id, SessionID: sessionID, CWD: first.Payload.CWD, Timestamp: timestamp, Base: first.Payload.HistoryBase}, nil
}

// advanceTurns applies the turn boundaries in the records after a rollout's
// session metadata to state.
func (l layout) advanceTurns(state *turnState, data []byte) error {
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	for index, line := range lines[1:] {
		kind, err := classifyEvent(line)
		if err != nil {
			return fmt.Errorf("decode rollout line %d: %w", index+2, err)
		}
		switch {
		case kind == l.turnStarted:
			state.open = true
			state.started++
		case l.isTurnEnd(kind):
			state.open = false
			state.completed++
		}
	}
	return nil
}

// validateRollout checks that data is one self-contained Codex session ending
// after a completed turn, and returns its recorded identity.
func (l layout) validateRollout(data []byte) (sessionMeta, error) {
	meta, err := parseRollout(data)
	if err != nil {
		return sessionMeta{}, err
	}
	if meta.Base != nil {
		return sessionMeta{}, errors.New("rollout extends another rollout")
	}
	var state turnState
	if err := l.advanceTurns(&state, data); err != nil {
		return sessionMeta{}, err
	}
	if state.open || state.completed == 0 {
		return sessionMeta{}, errors.New("rollout does not end after a completed turn")
	}
	return meta, nil
}

// validSessionID accepts only UUIDv7 strings, which also keeps a session ID safe
// to use in rollout file names.
func validSessionID(id string) bool {
	if len(id) != 36 || id[14] != '7' {
		return false
	}
	for index := 0; index < len(id); index++ {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if id[index] != '-' {
				return false
			}
			continue
		}
		character := id[index]
		isHex := (character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') ||
			(character >= 'A' && character <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// remapWorkspace moves a self-contained rollout from its recorded workspace to
// targetCWD; see remapLines.
func (l layout) remapWorkspace(data []byte, targetCWD string) ([]byte, sessionMeta, error) {
	meta, err := l.validateRollout(data)
	if err != nil {
		return nil, sessionMeta{}, err
	}
	remapped, changed, err := l.remapLines(data, meta.CWD, targetCWD)
	if err != nil {
		return nil, sessionMeta{}, err
	}
	if changed == 0 {
		return nil, sessionMeta{}, errors.New("rollout contains no recognized workspace metadata")
	}
	meta.CWD = targetCWD
	return remapped, meta, nil
}

// remapLines moves every mention of sourceCWD in recognized workspace metadata
// events to targetCWD, keeping the relative suffix of paths inside the
// workspace, and returns the number of strings changed. Only rewritten JSON
// strings change; all other bytes, including recorded tool results, stay as
// recorded. The rewrite must be reversible, so a rollout whose metadata
// already mentions targetCWD fails.
func (l layout) remapLines(data []byte, sourceCWD, targetCWD string) ([]byte, int, error) {
	if !filepath.IsAbs(targetCWD) {
		return nil, 0, errors.New("target workspace must be absolute")
	}
	if filepath.Dir(sourceCWD) == sourceCWD {
		return nil, 0, errors.New("a filesystem root workspace cannot be remapped")
	}
	lines := bytes.Split(data, []byte{'\n'})
	remapped := 0
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		kind, err := classifyEvent(line)
		if err != nil {
			return nil, 0, fmt.Errorf("decode rollout line %d: %w", index+1, err)
		}
		if !l.isWorkspaceEvent(kind) {
			continue
		}
		rewritten, changed, err := remapStrings(line, sourceCWD, targetCWD)
		if err != nil {
			return nil, 0, fmt.Errorf("remap rollout line %d: %w", index+1, err)
		}
		restored, _, err := remapStrings(rewritten, targetCWD, sourceCWD)
		if err != nil || !bytes.Equal(restored, line) {
			return nil, 0, fmt.Errorf("rollout line %d already mentions %s; the workspace remap would not be reversible", index+1, targetCWD)
		}
		lines[index] = rewritten
		remapped += changed
	}
	return bytes.Join(lines, []byte{'\n'}), remapped, nil
}

// storedSession lists the files Codex and agent-teleport keep for one session.
type storedSession struct {
	// active and archived are rollout files in the active and archived stores.
	active   []string
	archived []string
	// pending are the rollout paths with a rollback link or stage file left by
	// an interrupted write.
	pending []string
	// snapshots are rollouts Codex writes when a thread is reverted.
	snapshots []string
	// appending are rollouts with the marker of an interrupted append.
	appending []string
}

// findSession searches the active and archived Codex stores for the files of
// sessionID. Codex lists only active threads, and a rollout may have any
// timestamp in its name, so the stores themselves are the source of truth.
func findSession(home, sessionID string) (storedSession, error) {
	var found storedSession
	suffix := "-" + sessionID + ".jsonl"
	snapshotPrefix := "-" + sessionID + "_"
	for _, store := range []struct {
		root  string
		paths *[]string
	}{
		{root: filepath.Join(home, "sessions"), paths: &found.active},
		{root: filepath.Join(home, "archived_sessions"), paths: &found.archived},
	} {
		err := filepath.WalkDir(store.root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if path == store.root && errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			name := entry.Name()
			if entry.IsDir() || !strings.HasPrefix(name, "rollout-") {
				return nil
			}
			switch {
			case strings.HasSuffix(name, suffix):
				*store.paths = append(*store.paths, path)
			case strings.HasSuffix(name, suffix+rollbackSuffix):
				found.pending = appendUnique(found.pending, strings.TrimSuffix(path, rollbackSuffix))
			case strings.HasSuffix(name, suffix+stageSuffix):
				found.pending = appendUnique(found.pending, strings.TrimSuffix(path, stageSuffix))
			case strings.HasSuffix(name, appendSuffix):
				found.appending = appendUnique(found.appending, strings.TrimSuffix(path, appendSuffix))
			case strings.Contains(name, snapshotPrefix) && strings.HasSuffix(name, ".jsonl"):
				found.snapshots = append(found.snapshots, path)
			}
			return nil
		})
		if err != nil {
			return storedSession{}, fmt.Errorf("search stored sessions: %w", err)
		}
	}
	return found, nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// remapStrings rewrites every mention of sourceCWD inside the JSON strings of
// line, returning the line and the number of strings changed. A mention must
// start and end at a path boundary, so sibling paths such as sourceCWD-other
// are left alone.
func remapStrings(line []byte, sourceCWD, targetCWD string) ([]byte, int, error) {
	var output []byte
	copied := 0
	changed := 0
	for index := 0; index < len(line); index++ {
		if line[index] != '"' {
			continue
		}
		start := index
		end := stringTokenEnd(line, start)
		if end < 0 {
			return nil, 0, errors.New("unterminated JSON string")
		}
		index = end
		var value string
		if err := json.Unmarshal(line[start:end+1], &value); err != nil {
			return nil, 0, err
		}
		replaced := replacePath(value, sourceCWD, targetCWD)
		if replaced == value {
			continue
		}
		encoded, err := encodeString(replaced)
		if err != nil {
			return nil, 0, err
		}
		output = append(output, line[copied:start]...)
		output = append(output, encoded...)
		copied = end + 1
		changed++
	}
	if changed == 0 {
		return line, 0, nil
	}
	return append(output, line[copied:]...), changed, nil
}

// replacePath replaces each mention of path in value that is not part of a
// longer path name.
func replacePath(value, path, replacement string) string {
	var output strings.Builder
	copied := 0
	for offset := 0; offset < len(value); {
		found := strings.Index(value[offset:], path)
		if found < 0 {
			break
		}
		start := offset + found
		end := start + len(path)
		startsAtBoundary := start == 0 || !isPathNameByte(value[start-1])
		endsAtBoundary := end == len(value) || !isPathNameByte(value[end])
		if !startsAtBoundary || !endsAtBoundary {
			offset = start + 1
			continue
		}
		output.WriteString(value[copied:start])
		output.WriteString(replacement)
		copied = end
		offset = end
	}
	if copied == 0 {
		return value
	}
	output.WriteString(value[copied:])
	return output.String()
}

// stringTokenEnd returns the index of the quote closing the JSON string that
// starts at start, or -1 when the string is unterminated.
func stringTokenEnd(line []byte, start int) int {
	for index := start + 1; index < len(line); index++ {
		switch line[index] {
		case '\\':
			index++
		case '"':
			return index
		}
	}
	return -1
}

func isPathNameByte(character byte) bool {
	return character == '.' || character == '_' || character == '-' ||
		(character >= '0' && character <= '9') ||
		(character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z')
}

func encodeString(value string) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}
