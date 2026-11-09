package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// liveSession is a running Claude Code process, as recorded in the session
// registry Claude Code keeps under sessions/<pid>.json.
type liveSession struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	// Status is idle while the process waits for input; any other status means
	// it may be writing to the session.
	Status string `json:"status"`
}

// errSessionOpen reports that a running Claude Code process has the session
// open, so changing its files could lose or interleave records.
var errSessionOpen = errors.New("session is open in Claude Code")

// sessionProcess returns the running Claude Code process that has sessionID
// open. Registry entries of processes that have exited are ignored.
func sessionProcess(home, sessionID string) (liveSession, bool, error) {
	entries, err := os.ReadDir(filepath.Join(home, "sessions"))
	if errors.Is(err, fs.ErrNotExist) {
		return liveSession{}, false, nil
	}
	if err != nil {
		return liveSession{}, false, fmt.Errorf("read Claude Code session registry: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(home, "sessions", entry.Name()))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return liveSession{}, false, fmt.Errorf("read Claude Code session registry: %w", err)
		}
		var live liveSession
		if json.Unmarshal(data, &live) != nil || live.PID <= 0 || live.SessionID != sessionID {
			continue
		}
		if processAlive(live.PID) {
			return live, true, nil
		}
	}
	return liveSession{}, false, nil
}

// requireClosed fails when a running Claude Code process has sessionID open.
func requireClosed(home, sessionID string) error {
	live, open, err := sessionProcess(home, sessionID)
	if err != nil {
		return err
	}
	if open {
		return fmt.Errorf("%w (process %d); exit it first", errSessionOpen, live.PID)
	}
	return nil
}
