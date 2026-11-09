package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testSessionID = "01999999-1111-7777-8888-222222222222"

var testLayout = layouts[len(layouts)-1]

func TestValidateAndRemapWorkspace(t *testing.T) {
	source := fixtureRollout("/source/workspace")
	meta, err := testLayout.validateRollout(source)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != testSessionID || meta.CWD != "/source/workspace" {
		t.Fatalf("unexpected metadata: %#v", meta)
	}

	remapped, remappedMeta, err := testLayout.remapWorkspace(source, "/target/workspace")
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
	if _, err := testLayout.validateRollout(remapped); err != nil {
		t.Fatalf("remapped rollout is invalid: %v", err)
	}
}

func TestRemapWorkspaceRoundTripsNativeFixture(t *testing.T) {
	original, err := os.ReadFile(filepath.Join("..", "..", "testdata", "codex", parentFixture))
	if err != nil {
		t.Fatal(err)
	}
	source, err := testLayout.validateRollout(original)
	if err != nil {
		t.Fatal(err)
	}

	// when
	remapped, _, err := testLayout.remapWorkspace(original, "/target/workspace")
	if err != nil {
		t.Fatal(err)
	}
	restored, _, err := testLayout.remapWorkspace(remapped, source.CWD)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatal("remapping to another workspace and back changed the rollout")
	}
	originalLines := bytes.Split(original, []byte{'\n'})
	for index, line := range bytes.Split(remapped, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		kind, err := classifyEvent(line)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := testLayout.workspaceEvents[kind]; ok {
			if bytes.Contains(line, []byte(source.CWD)) {
				t.Fatalf("workspace metadata on line %d keeps the source workspace", index+1)
			}
		} else if !bytes.Equal(line, originalLines[index]) {
			t.Fatalf("line %d is not workspace metadata but changed", index+1)
		}
	}
}

func TestRemapWorkspacePreservesUnrelatedBytes(t *testing.T) {
	source := fixtureRollout("/source/workspace")
	line := `{"type":"turn_context","payload":{"z":"<kept & exact>","cwd":"/source/workspace","roots":["/source/workspace/.git","/source/workspace-sibling"],"n":12345678901234567890}}`
	source = bytes.Replace(source, []byte(`{"payload":{"cwd":"/source/workspace"},"type":"turn_context"}`), []byte(line), 1)

	// when
	remapped, _, err := testLayout.remapWorkspace(source, "/target/workspace")

	// then
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"turn_context","payload":{"z":"<kept & exact>","cwd":"/target/workspace","roots":["/target/workspace/.git","/source/workspace-sibling"],"n":12345678901234567890}}`
	if !bytes.Contains(remapped, []byte(want+"\n")) {
		t.Fatalf("remapped line changed unrelated bytes:\n%s", remapped)
	}
}

func TestRemapWorkspaceRewritesEmbeddedPaths(t *testing.T) {
	source := bytes.Replace(
		fixtureRollout("/source/workspace"),
		[]byte(`{"payload":{"cwd":"/source/workspace"},"type":"turn_context"}`),
		[]byte(`{"payload":{"cwd":"/source/workspace","uri":"file:///source/workspace","xml":"<root>/source/workspace/.git</root>","other":"/other/source/workspace"},"type":"turn_context"}`),
		1,
	)

	// when
	remapped, _, err := testLayout.remapWorkspace(source, "/target/workspace")

	// then
	if err != nil {
		t.Fatal(err)
	}
	want := `{"payload":{"cwd":"/target/workspace","uri":"file:///target/workspace","xml":"<root>/target/workspace/.git</root>","other":"/other/source/workspace"},"type":"turn_context"}`
	if !bytes.Contains(remapped, []byte(want)) {
		t.Fatalf("remapped rollout does not contain %s", want)
	}
}

func TestRemapWorkspaceRejectsIrreversibleRewrite(t *testing.T) {
	source := bytes.Replace(
		fixtureRollout("/source/workspace"),
		[]byte(`{"payload":{"cwd":"/source/workspace"},"type":"turn_context"}`),
		[]byte(`{"payload":{"cwd":"/source/workspace","previous":"/target/workspace"},"type":"turn_context"}`),
		1,
	)

	// when
	_, _, err := testLayout.remapWorkspace(source, "/target/workspace")

	// then
	if err == nil || !strings.Contains(err.Error(), "reversible") {
		t.Fatalf("remapWorkspace error = %v", err)
	}
}

func TestValidateRolloutRejectsOpenTurn(t *testing.T) {
	source := append(fixtureRollout("/source/workspace"), []byte(`{"type":"event_msg","payload":{"type":"task_started"}}`+"\n")...)

	// when
	_, err := testLayout.validateRollout(source)

	// then
	if err == nil || !strings.Contains(err.Error(), "completed turn") {
		t.Fatalf("validateRollout error = %v", err)
	}
}

func TestValidateRolloutAcceptsSettingsAfterCompletedTurn(t *testing.T) {
	source := append(fixtureRollout("/source/workspace"), []byte(`{"type":"event_msg","payload":{"type":"thread_settings_applied"}}`+"\n")...)

	// when, then
	if _, err := testLayout.validateRollout(source); err != nil {
		t.Fatalf("validateRollout error = %v", err)
	}
}

func TestValidateRolloutRejectsIncompleteTurn(t *testing.T) {
	source := fixtureRollout("/source/workspace")
	lastLine := bytes.LastIndex(source[:len(source)-1], []byte{'\n'})
	incomplete := append([]byte(nil), source[:lastLine+1]...)
	if _, err := testLayout.validateRollout(incomplete); err == nil || !strings.Contains(err.Error(), "completed turn") {
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
	if _, err := testLayout.validateRollout(source); err == nil || !strings.Contains(err.Error(), "UUIDv7") {
		t.Fatalf("ValidateRollout error = %v", err)
	}
}

func TestValidateRolloutHistoryBase(t *testing.T) {
	for _, test := range []struct {
		name      string
		base      any
		wantError bool
	}{
		{name: "external file prefix", base: map[string]any{
			"thread_id":             "01999999-3333-7777-8888-444444444444",
			"end_ordinal_exclusive": 5,
			"end_byte_offset":       1024,
		}, wantError: true},
		{name: "null base", base: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			first, rest, _ := bytes.Cut(fixtureRollout("/source/workspace"), []byte{'\n'})
			var event map[string]any
			if err := json.Unmarshal(first, &event); err != nil {
				t.Fatal(err)
			}
			event["payload"].(map[string]any)["history_base"] = test.base
			header, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			source := append(append(header, '\n'), rest...)
			_, err = testLayout.validateRollout(source)
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "extends another rollout") {
					t.Fatalf("ValidateRollout must reject an incomplete single-file archive, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("explicit null history base must remain supported: %v", err)
			}
		})
	}
}

func TestParseRolloutThreadAndSessionIDs(t *testing.T) {
	const otherSession = "01999999-3333-7777-8888-444444444444"
	for _, test := range []struct {
		name          string
		threadID      string
		sessionID     string
		wantSessionID string
	}{
		{name: "thread ID only", threadID: testSessionID, wantSessionID: testSessionID},
		{name: "legacy session ID only", sessionID: testSessionID, wantSessionID: testSessionID},
		{name: "matching IDs", threadID: testSessionID, sessionID: testSessionID, wantSessionID: testSessionID},
		{name: "spawned thread", threadID: testSessionID, sessionID: otherSession, wantSessionID: otherSession},
	} {
		t.Run(test.name, func(t *testing.T) {
			first, rest, _ := bytes.Cut(fixtureRollout("/source/workspace"), []byte{'\n'})
			var event map[string]any
			if err := json.Unmarshal(first, &event); err != nil {
				t.Fatal(err)
			}
			payload := event["payload"].(map[string]any)
			delete(payload, "id")
			if test.threadID != "" {
				payload["id"] = test.threadID
			}
			if test.sessionID != "" {
				payload["session_id"] = test.sessionID
			}
			header, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}

			// when
			meta, err := parseRollout(append(append(header, '\n'), rest...))

			// then
			if err != nil || meta.ID != testSessionID || meta.SessionID != test.wantSessionID {
				t.Fatalf("parseRollout() = %#v, %v", meta, err)
			}
		})
	}
}

func TestFindSessionSearchesActiveArchivedAndPendingFiles(t *testing.T) {
	home := t.TempDir()
	name := "rollout-2026-09-13T09-58-44-" + testSessionID + ".jsonl"
	active := filepath.Join(home, "sessions", "2026", "09", "13", name)
	archived := filepath.Join(home, "archived_sessions", name)
	unrelated := filepath.Join(home, "sessions", "2026", "09", "13", "rollout-2026-09-13T09-58-44-01999999-3333-7777-8888-444444444444.jsonl")
	for _, path := range []string{active, archived, unrelated, active + rollbackSuffix, active + stageSuffix} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// when
	found, err := findSession(home, testSessionID)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(found.active, []string{active}) || !slices.Equal(found.archived, []string{archived}) || !slices.Equal(found.pending, []string{active}) {
		t.Fatalf("findSession() = %+v", found)
	}
}

func TestFindSessionWithoutSessionStores(t *testing.T) {
	// when
	found, err := findSession(t.TempDir(), testSessionID)

	// then
	if err != nil || len(found.active)+len(found.archived)+len(found.pending) != 0 {
		t.Fatalf("findSession() = %+v, %v", found, err)
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
