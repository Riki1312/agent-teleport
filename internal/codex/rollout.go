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
	ID        string
	CWD       string
	Timestamp time.Time
}

// stableRollout reads a rollout and validates it, failing when the file changed
// during the read.
func (l layout) stableRollout(path string) ([]byte, sessionMeta, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, sessionMeta{}, fmt.Errorf("stat rollout: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, sessionMeta{}, errors.New("rollout is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, sessionMeta{}, fmt.Errorf("read rollout: %w", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, sessionMeta{}, fmt.Errorf("stat rollout after read: %w", err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, sessionMeta{}, errors.New("rollout changed while it was being read")
	}
	meta, err := l.validateRollout(data)
	if err != nil {
		return nil, sessionMeta{}, err
	}
	return data, meta, nil
}

// validateRollout checks that data is one complete, self-contained Codex
// session ending at a completed turn, and returns its recorded identity.
func (l layout) validateRollout(data []byte) (sessionMeta, error) {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return sessionMeta{}, errors.New("rollout does not end with a complete JSONL record")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) == 0 || len(lines[0]) == 0 {
		return sessionMeta{}, errors.New("rollout has no session metadata")
	}
	var first struct {
		Type    string `json:"type"`
		Payload struct {
			ID          string          `json:"id"`
			SessionID   string          `json:"session_id"`
			CWD         string          `json:"cwd"`
			Timestamp   string          `json:"timestamp"`
			HistoryBase json.RawMessage `json:"history_base"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(lines[0], &first); err != nil {
		return sessionMeta{}, fmt.Errorf("decode session metadata: %w", err)
	}
	if first.Type != "session_meta" {
		return sessionMeta{}, fmt.Errorf("first rollout event has type %q, want session_meta", first.Type)
	}
	if len(first.Payload.HistoryBase) != 0 && !bytes.Equal(bytes.TrimSpace(first.Payload.HistoryBase), []byte("null")) {
		return sessionMeta{}, errors.New("rollout references an external history_base; single-file archives cannot preserve its dependencies")
	}
	if first.Payload.ID != "" && first.Payload.SessionID != "" && first.Payload.ID != first.Payload.SessionID {
		return sessionMeta{}, errors.New("rollout has different thread and session IDs; this layout is not supported")
	}
	id := first.Payload.SessionID
	if id == "" {
		id = first.Payload.ID
	}
	if id == "" || first.Payload.CWD == "" || first.Payload.Timestamp == "" {
		return sessionMeta{}, errors.New("session metadata is missing id, cwd, or timestamp")
	}
	if !validSessionID(id) {
		return sessionMeta{}, fmt.Errorf("session ID %q is not a UUIDv7", id)
	}
	timestamp, err := time.Parse(time.RFC3339Nano, first.Payload.Timestamp)
	if err != nil {
		return sessionMeta{}, fmt.Errorf("parse session timestamp: %w", err)
	}
	turnOpen, completedTurns := false, 0
	for index, line := range lines[1:] {
		if len(line) == 0 {
			return sessionMeta{}, fmt.Errorf("rollout line %d is empty", index+2)
		}
		if !json.Valid(line) {
			return sessionMeta{}, fmt.Errorf("rollout line %d is not valid JSON", index+2)
		}
		kind, err := classifyEvent(line)
		if err != nil {
			return sessionMeta{}, fmt.Errorf("decode rollout line %d: %w", index+2, err)
		}
		switch {
		case kind == l.turnStarted:
			turnOpen = true
		case l.isTurnEnd(kind):
			turnOpen = false
			completedTurns++
		}
	}
	if turnOpen || completedTurns == 0 {
		return sessionMeta{}, errors.New("rollout does not end after a completed turn")
	}
	return sessionMeta{ID: id, CWD: first.Payload.CWD, Timestamp: timestamp}, nil
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

// remapWorkspace moves every workspace path in recognized metadata events from
// the recorded workspace to targetCWD, keeping the relative suffix of paths
// inside the workspace. Only rewritten JSON strings change; all other bytes,
// including recorded tool results, stay as recorded. The rewrite must be
// reversible, so a rollout that already mentions targetCWD in its metadata
// fails.
func (l layout) remapWorkspace(data []byte, targetCWD string) ([]byte, sessionMeta, error) {
	meta, err := l.validateRollout(data)
	if err != nil {
		return nil, sessionMeta{}, err
	}
	if !filepath.IsAbs(targetCWD) {
		return nil, sessionMeta{}, errors.New("target workspace must be absolute")
	}
	if filepath.Dir(meta.CWD) == meta.CWD {
		return nil, sessionMeta{}, errors.New("a filesystem root workspace cannot be remapped")
	}
	lines := bytes.Split(data, []byte{'\n'})
	remapped := 0
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		kind, err := classifyEvent(line)
		if err != nil {
			return nil, sessionMeta{}, fmt.Errorf("decode rollout line %d: %w", index+1, err)
		}
		if !l.isWorkspaceEvent(kind) {
			continue
		}
		rewritten, changed, err := remapStrings(line, meta.CWD, targetCWD)
		if err != nil {
			return nil, sessionMeta{}, fmt.Errorf("remap rollout line %d: %w", index+1, err)
		}
		restored, _, err := remapStrings(rewritten, targetCWD, meta.CWD)
		if err != nil || !bytes.Equal(restored, line) {
			return nil, sessionMeta{}, fmt.Errorf("rollout line %d already mentions %s; the workspace remap would not be reversible", index+1, targetCWD)
		}
		lines[index] = rewritten
		remapped += changed
	}
	if remapped == 0 {
		return nil, sessionMeta{}, errors.New("rollout contains no recognized workspace metadata")
	}
	meta.CWD = targetCWD
	return bytes.Join(lines, []byte{'\n'}), meta, nil
}

// rolloutPath returns where Codex would store a new active rollout for meta.
// Codex names rollouts after their local creation time.
func rolloutPath(home string, meta sessionMeta) string {
	timestamp := meta.Timestamp.Local()
	name := fmt.Sprintf("rollout-%s-%s.jsonl", timestamp.Format("2006-01-02T15-04-05"), meta.ID)
	return filepath.Join(home, "sessions", timestamp.Format("2006"), timestamp.Format("01"), timestamp.Format("02"), name)
}

// storedSession lists the files Codex and agent-teleport keep for one session.
type storedSession struct {
	// active and archived are rollout files in the active and archived stores.
	active   []string
	archived []string
	// pending are the rollout paths with a rollback link or stage file left by
	// an interrupted write.
	pending []string
}

// findSession searches the active and archived Codex stores for the files of
// sessionID. Codex lists only active threads, and a rollout may have any
// timestamp in its name, so the stores themselves are the source of truth.
func findSession(home, sessionID string) (storedSession, error) {
	var found storedSession
	suffix := "-" + sessionID + ".jsonl"
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
