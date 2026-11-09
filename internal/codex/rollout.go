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
	for index, line := range lines[1:] {
		if len(line) == 0 {
			return sessionMeta{}, fmt.Errorf("rollout line %d is empty", index+2)
		}
		if !json.Valid(line) {
			return sessionMeta{}, fmt.Errorf("rollout line %d is not valid JSON", index+2)
		}
	}
	last, err := classifyEvent(lines[len(lines)-1])
	if err != nil {
		return sessionMeta{}, fmt.Errorf("decode final rollout event: %w", err)
	}
	if last != l.completeEvent {
		return sessionMeta{}, errors.New("rollout does not end at a completed turn")
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
// the recorded workspace to targetCWD. Paths inside the workspace keep their
// relative suffix. Only rewritten JSON strings change; all other bytes,
// including recorded tool results, stay as recorded.
func (l layout) remapWorkspace(data []byte, targetCWD string) ([]byte, sessionMeta, error) {
	meta, err := l.validateRollout(data)
	if err != nil {
		return nil, sessionMeta{}, err
	}
	if !filepath.IsAbs(targetCWD) {
		return nil, sessionMeta{}, errors.New("target workspace must be absolute")
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
		rewritten, changed, err := remapPathStrings(line, meta.CWD, targetCWD)
		if err != nil {
			return nil, sessionMeta{}, fmt.Errorf("remap rollout line %d: %w", index+1, err)
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

// rolloutPath returns where Codex stores the active rollout for meta.
func rolloutPath(home string, meta sessionMeta) string {
	timestamp := meta.Timestamp.UTC()
	nameTime := timestamp.Format("2006-01-02T15-04-05")
	name := fmt.Sprintf("rollout-%s-%s.jsonl", nameTime, meta.ID)
	return filepath.Join(
		home,
		"sessions",
		timestamp.Format("2006"),
		timestamp.Format("01"),
		timestamp.Format("02"),
		name,
	)
}

// existingRollouts returns every active or archived rollout file for sessionID.
// Codex lists only active threads, so collision checks must inspect both stores.
func existingRollouts(home, sessionID string) ([]string, error) {
	suffix := "-" + sessionID + ".jsonl"
	var paths []string
	for _, root := range []string{filepath.Join(home, "sessions"), filepath.Join(home, "archived_sessions")} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if path == root && errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			name := entry.Name()
			if !entry.IsDir() && strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, suffix) {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("search existing rollouts: %w", err)
		}
	}
	return paths, nil
}

// remapPathStrings rewrites each JSON string in line that is sourceCWD or a
// path below it. A string that mentions sourceCWD in any other form fails
// closed, because a partial remap would leave the session tied to the source.
func remapPathStrings(line []byte, sourceCWD, targetCWD string) ([]byte, int, error) {
	var output []byte
	copied := 0
	changed := 0
	for start := 0; start < len(line); start++ {
		if line[start] != '"' {
			continue
		}
		end := stringTokenEnd(line, start)
		if end < 0 {
			return nil, 0, errors.New("unterminated JSON string")
		}
		token := line[start : end+1]
		var value string
		if err := json.Unmarshal(token, &value); err != nil {
			return nil, 0, err
		}
		suffix, ok := strings.CutPrefix(value, sourceCWD)
		if !ok || (suffix != "" && suffix[0] != '/') {
			if mentionsPath(value, sourceCWD) {
				return nil, 0, errors.New("workspace path appears in an unsupported form")
			}
			start = end
			continue
		}
		encoded, err := encodeString(targetCWD + suffix)
		if err != nil {
			return nil, 0, err
		}
		output = append(output, line[copied:start]...)
		output = append(output, encoded...)
		copied = end + 1
		changed++
		start = end
	}
	if changed == 0 {
		return line, 0, nil
	}
	return append(output, line[copied:]...), changed, nil
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

// mentionsPath reports whether value contains path followed by a path boundary.
func mentionsPath(value, path string) bool {
	for offset := 0; ; {
		found := strings.Index(value[offset:], path)
		if found < 0 {
			return false
		}
		next := offset + found + len(path)
		if next == len(value) || !isPathNameByte(value[next]) {
			return true
		}
		offset = offset + found + 1
	}
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
