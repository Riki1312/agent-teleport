package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const Provider = "openai-codex"

type SessionMeta struct {
	ID        string
	CWD       string
	Timestamp time.Time
}

func Home() (string, error) {
	if value := os.Getenv("CODEX_HOME"); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate user home: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

func Version(codexPath string) (string, error) {
	output, err := exec.Command(codexPath, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", codexPath, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[0] != "codex-cli" {
		return "", fmt.Errorf("unexpected Codex version output %q", strings.TrimSpace(string(output)))
	}
	return fields[1], nil
}

func (c Compatibility) StableRollout(path string) ([]byte, SessionMeta, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, SessionMeta{}, fmt.Errorf("stat rollout: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, SessionMeta{}, errors.New("rollout is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, SessionMeta{}, fmt.Errorf("read rollout: %w", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, SessionMeta{}, fmt.Errorf("stat rollout after read: %w", err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, SessionMeta{}, errors.New("rollout changed while it was being read")
	}
	meta, err := c.ValidateRollout(data)
	if err != nil {
		return nil, SessionMeta{}, err
	}
	return data, meta, nil
}

func (c Compatibility) ValidateRollout(data []byte) (SessionMeta, error) {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return SessionMeta{}, errors.New("rollout does not end with a complete JSONL record")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) == 0 || len(lines[0]) == 0 {
		return SessionMeta{}, errors.New("rollout has no session metadata")
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
		return SessionMeta{}, fmt.Errorf("decode session metadata: %w", err)
	}
	if first.Type != "session_meta" {
		return SessionMeta{}, fmt.Errorf("first rollout event has type %q, want session_meta", first.Type)
	}
	if len(first.Payload.HistoryBase) != 0 && !bytes.Equal(bytes.TrimSpace(first.Payload.HistoryBase), []byte("null")) {
		return SessionMeta{}, errors.New("rollout references an external history_base; single-file archives cannot preserve its dependencies")
	}
	if first.Payload.ID != "" && first.Payload.SessionID != "" && first.Payload.ID != first.Payload.SessionID {
		return SessionMeta{}, errors.New("rollout has different thread and session IDs; this layout is not supported")
	}
	id := first.Payload.SessionID
	if id == "" {
		id = first.Payload.ID
	}
	if id == "" || first.Payload.CWD == "" || first.Payload.Timestamp == "" {
		return SessionMeta{}, errors.New("session metadata is missing id, cwd, or timestamp")
	}
	if !validSessionID(id) {
		return SessionMeta{}, fmt.Errorf("session ID %q is not a UUIDv7", id)
	}
	timestamp, err := time.Parse(time.RFC3339Nano, first.Payload.Timestamp)
	if err != nil {
		return SessionMeta{}, fmt.Errorf("parse session timestamp: %w", err)
	}
	for index, line := range lines[1:] {
		if len(line) == 0 {
			return SessionMeta{}, fmt.Errorf("rollout line %d is empty", index+2)
		}
		if !json.Valid(line) {
			return SessionMeta{}, fmt.Errorf("rollout line %d is not valid JSON", index+2)
		}
	}
	last, err := classifyEvent(lines[len(lines)-1])
	if err != nil {
		return SessionMeta{}, fmt.Errorf("decode final rollout event: %w", err)
	}
	if last != c.completeEvent {
		return SessionMeta{}, errors.New("rollout does not end at a completed turn")
	}
	return SessionMeta{ID: id, CWD: first.Payload.CWD, Timestamp: timestamp}, nil
}

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
		if !((character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') ||
			(character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

// RemapWorkspace moves every workspace path in recognized metadata events from
// the recorded workspace to targetCWD. Paths inside the workspace keep their
// relative suffix. Only rewritten JSON strings change; all other bytes,
// including recorded tool results, stay as recorded.
func (c Compatibility) RemapWorkspace(data []byte, targetCWD string) ([]byte, SessionMeta, error) {
	meta, err := c.ValidateRollout(data)
	if err != nil {
		return nil, SessionMeta{}, err
	}
	if !filepath.IsAbs(targetCWD) {
		return nil, SessionMeta{}, errors.New("target workspace must be absolute")
	}
	lines := bytes.Split(data, []byte{'\n'})
	remapped := 0
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		kind, err := classifyEvent(line)
		if err != nil {
			return nil, SessionMeta{}, fmt.Errorf("decode rollout line %d: %w", index+1, err)
		}
		if _, ok := c.workspaceEvents[kind]; !ok {
			continue
		}
		rewritten, changed, err := remapPathStrings(line, meta.CWD, targetCWD)
		if err != nil {
			return nil, SessionMeta{}, fmt.Errorf("remap rollout line %d: %w", index+1, err)
		}
		lines[index] = rewritten
		remapped += changed
	}
	if remapped == 0 {
		return nil, SessionMeta{}, errors.New("rollout contains no recognized workspace metadata")
	}
	meta.CWD = targetCWD
	return bytes.Join(lines, []byte{'\n'}), meta, nil
}

func (c Compatibility) RolloutPath(home string, meta SessionMeta) string {
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

// ExistingRollouts returns every active or archived rollout file for sessionID.
// Codex lists only active threads, so collision checks must inspect both stores.
func ExistingRollouts(home, sessionID string) ([]string, error) {
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
