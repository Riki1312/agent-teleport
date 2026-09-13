package codex

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSessionID = "01999999-1111-7777-8888-222222222222"

func TestValidateAndRemapWorkspace(t *testing.T) {
	source := fixtureRollout("/source/workspace")
	meta, err := ValidateRollout(source)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != testSessionID || meta.CWD != "/source/workspace" {
		t.Fatalf("unexpected metadata: %#v", meta)
	}

	remapped, remappedMeta, err := RemapWorkspace(source, "/target/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if remappedMeta.CWD != "/target/workspace" {
		t.Fatalf("remapped cwd = %q", remappedMeta.CWD)
	}
	if bytes.Contains(remapped, []byte(`"cwd":"/source/workspace"`)) {
		t.Fatal("recognized workspace metadata retained the source cwd")
	}
	if !bytes.Contains(remapped, []byte(`"cwd":"file:///source/workspace"`)) {
		t.Fatal("recorded tool result path was unexpectedly rewritten")
	}
	if _, err := ValidateRollout(remapped); err != nil {
		t.Fatalf("remapped rollout is invalid: %v", err)
	}
}

func TestValidateRolloutRejectsIncompleteTurn(t *testing.T) {
	source := fixtureRollout("/source/workspace")
	lastLine := bytes.LastIndex(source[:len(source)-1], []byte{'\n'})
	incomplete := append([]byte(nil), source[:lastLine+1]...)
	if _, err := ValidateRollout(incomplete); err == nil || !strings.Contains(err.Error(), "completed turn") {
		t.Fatalf("ValidateRollout error = %v", err)
	}
}

func TestValidateRolloutRejectsUnsafeSessionID(t *testing.T) {
	source := bytes.Replace(
		fixtureRollout("/source/workspace"),
		[]byte(testSessionID),
		[]byte("../../../../tmp/not-a-session-id"),
		1,
	)
	if _, err := ValidateRollout(source); err == nil || !strings.Contains(err.Error(), "UUIDv7") {
		t.Fatalf("ValidateRollout error = %v", err)
	}
}

func TestRolloutPath(t *testing.T) {
	meta := SessionMeta{
		ID:        testSessionID,
		Timestamp: time.Date(2026, 9, 13, 9, 58, 44, 0, time.UTC),
	}
	want := filepath.Join("/codex", "sessions", "2026", "09", "13", "rollout-2026-09-13T09-58-44-"+testSessionID+".jsonl")
	if got := RolloutPath("/codex", meta); got != want {
		t.Fatalf("RolloutPath() = %q, want %q", got, want)
	}
}

func fixtureRollout(cwd string) []byte {
	events := []any{
		map[string]any{
			"timestamp": "2026-09-13T09:58:44Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":        testSessionID,
				"timestamp": "2026-09-13T09:58:44Z",
				"cwd":       cwd,
			},
		},
		map[string]any{
			"type": "world_state",
			"payload": map[string]any{"state": map[string]any{
				"environments": map[string]any{"environments": map[string]any{
					"local": map[string]any{"cwd": cwd},
				}},
			}},
		},
		map[string]any{"type": "turn_context", "payload": map[string]any{"cwd": cwd}},
		map[string]any{
			"type": "event_msg",
			"payload": map[string]any{
				"type":            "thread_settings_applied",
				"thread_settings": map[string]any{"cwd": cwd},
			},
		},
		map[string]any{
			"type": "event_msg",
			"payload": map[string]any{
				"type": "item_completed",
				"item": map[string]any{"cwd": "file://" + cwd},
			},
		},
		map[string]any{"type": "event_msg", "payload": map[string]any{"type": "task_complete"}},
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			panic(err)
		}
	}
	return output.Bytes()
}
