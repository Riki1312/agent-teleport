package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/Riki1312/agent-teleport/internal/fsutil"
	"github.com/Riki1312/agent-teleport/internal/pathmap"
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

// readRecords reads the complete records of a rollout. Codex only appends to
// rollouts, so the complete records are a consistent snapshot even while a
// Codex process is writing the file.
func readRecords(path string) ([]byte, error) {
	data, err := fsutil.ReadRegular(path)
	if err != nil {
		return nil, fmt.Errorf("read rollout: %w", err)
	}
	return data[:bytes.LastIndexByte(data, '\n')+1], nil
}

// firstPrompt returns the first message a person sent in a rollout, skipping
// the environment and instruction blocks that Codex sends as user messages.
func firstPrompt(data []byte) string {
	for line := range bytes.Lines(data) {
		var record struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil || record.Type != "response_item" || record.Payload.Type != "message" || record.Payload.Role != "user" {
			continue
		}
		for _, content := range record.Payload.Content {
			text := strings.TrimSpace(content.Text)
			if content.Type == "input_text" && text != "" && !strings.HasPrefix(text, "<") && !strings.HasPrefix(text, "# AGENTS.md instructions") {
				return text
			}
		}
	}
	return ""
}

// withoutOpenTurn returns the prefix of a rollout before its turn in
// progress, or the whole rollout when every started turn has ended.
func (l layout) withoutOpenTurn(data []byte) ([]byte, error) {
	open := false
	start, offset := 0, 0
	for line := range bytes.Lines(data) {
		kind, err := classifyEvent(line)
		if err != nil {
			return nil, err
		}
		switch {
		case kind == l.turnStarted && !open:
			open, start = true, offset
		case l.isTurnEnd(kind):
			open = false
		}
		offset += len(line)
	}
	if open {
		return data[:start], nil
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

// relocateLines moves every path that mapping covers in every record of
// data, keeping the relative suffix of paths inside a moved directory, and
// returns the number of strings changed. Only rewritten JSON strings change,
// and the rewrite must be reversible, so a rollout that already mentions a
// target directory fails.
func relocateLines(data []byte, mapping pathmap.Mapping) ([]byte, int, error) {
	lines := bytes.Split(data, []byte{'\n'})
	relocated := 0
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		rewritten, changed, err := mapping.Rewrite(line)
		if errors.Is(err, pathmap.ErrIrreversible) {
			return nil, 0, fmt.Errorf("rollout line %d already mentions the target directory; the workspace move would not be reversible", index+1)
		}
		if err != nil {
			return nil, 0, fmt.Errorf("move paths on rollout line %d: %w", index+1, err)
		}
		lines[index] = rewritten
		relocated += changed
	}
	return bytes.Join(lines, []byte{'\n'}), relocated, nil
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
			case strings.HasSuffix(name, appendSuffix) && isSessionRollout(strings.TrimSuffix(name, appendSuffix), suffix, snapshotPrefix):
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

// isSessionRollout reports whether a rollout file name is the original rollout
// or a snapshot of the thread whose names end with suffix or contain
// snapshotPrefix.
func isSessionRollout(name, suffix, snapshotPrefix string) bool {
	return strings.HasSuffix(name, suffix) || (strings.Contains(name, snapshotPrefix) && strings.HasSuffix(name, ".jsonl"))
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
