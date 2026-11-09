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

func TestRelocateMovesEveryWorkspacePath(t *testing.T) {
	source := fixtureRollout("/source/workspace")

	// when
	relocated, meta, err := relocateRollout(source, "/target/workspace")

	// then
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != testSessionID || meta.CWD != "/target/workspace" {
		t.Fatalf("relocated metadata = %#v", meta)
	}
	if bytes.Contains(relocated, []byte("/source/workspace")) {
		t.Fatal("relocated rollout still mentions the source workspace")
	}
	if !bytes.Contains(relocated, []byte(`"cwd":"file:///target/workspace"`)) {
		t.Fatal("recorded tool result path was not moved")
	}
}

func TestRelocateRoundTripsNativeFixture(t *testing.T) {
	fixture := fixtureFiles(t, parentFixture)[0]
	session, err := testLayout.validateSession(fixture.meta.ID, []rolloutFile{fixture})
	if err != nil {
		t.Fatal(err)
	}

	// when
	relocated, err := relocate(testLayout, session, "/workspace/source", "/target/workspace-with-a-longer-name")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := relocate(testLayout, relocated, "/target/workspace-with-a-longer-name", "/workspace/source")

	// then
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored.files[0].data, fixture.data) {
		t.Fatal("moving to another workspace and back changed the rollout")
	}
	moved := relocated.files[0].data
	if bytes.Contains(moved, []byte(`/workspace/source"`)) || bytes.Contains(moved, []byte("/workspace/source/")) {
		t.Fatal("relocated fixture still mentions the source workspace")
	}
}

func TestRelocatePreservesUnrelatedBytes(t *testing.T) {
	source := fixtureRollout("/source/workspace")
	line := `{"type":"turn_context","payload":{"z":"<kept & exact>","cwd":"/source/workspace","roots":["/source/workspace/.git","/source/workspace-sibling"],"n":12345678901234567890}}`
	source = bytes.Replace(source, []byte(`{"payload":{"cwd":"/source/workspace"},"type":"turn_context"}`), []byte(line), 1)

	// when
	relocated, _, err := relocateRollout(source, "/target/workspace")

	// then
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"turn_context","payload":{"z":"<kept & exact>","cwd":"/target/workspace","roots":["/target/workspace/.git","/source/workspace-sibling"],"n":12345678901234567890}}`
	if !bytes.Contains(relocated, []byte(want+"\n")) {
		t.Fatalf("relocated line changed unrelated bytes:\n%s", relocated)
	}
}

func TestRelocateRewritesEmbeddedPaths(t *testing.T) {
	source := bytes.Replace(
		fixtureRollout("/source/workspace"),
		[]byte(`{"payload":{"cwd":"/source/workspace"},"type":"turn_context"}`),
		[]byte(`{"payload":{"cwd":"/source/workspace","uri":"file:///source/workspace","xml":"<root>/source/workspace/.git</root>","other":"/other/source/workspace"},"type":"turn_context"}`),
		1,
	)

	// when
	relocated, _, err := relocateRollout(source, "/target/workspace")

	// then
	if err != nil {
		t.Fatal(err)
	}
	want := `{"payload":{"cwd":"/target/workspace","uri":"file:///target/workspace","xml":"<root>/target/workspace/.git</root>","other":"/other/source/workspace"},"type":"turn_context"}`
	if !bytes.Contains(relocated, []byte(want)) {
		t.Fatalf("relocated rollout does not contain %s", want)
	}
}

func TestRelocateRejectsIrreversibleRewrite(t *testing.T) {
	source := bytes.Replace(
		fixtureRollout("/source/workspace"),
		[]byte(`{"payload":{"cwd":"/source/workspace"},"type":"turn_context"}`),
		[]byte(`{"payload":{"cwd":"/source/workspace","previous":"/target/workspace"},"type":"turn_context"}`),
		1,
	)

	// when
	_, _, err := relocateRollout(source, "/target/workspace")

	// then
	if err == nil || !strings.Contains(err.Error(), "reversible") {
		t.Fatalf("relocate error = %v", err)
	}
}

func TestValidateSessionRejectsOpenTurn(t *testing.T) {
	source := append(fixtureRollout("/source/workspace"), []byte(`{"type":"event_msg","payload":{"type":"task_started"}}`+"\n")...)

	// when
	_, err := singleSession(source)

	// then
	if err == nil || !strings.Contains(err.Error(), "turn") {
		t.Fatalf("validateSession error = %v", err)
	}
}

func TestValidateSessionAcceptsSettingsAfterCompletedTurn(t *testing.T) {
	source := append(fixtureRollout("/source/workspace"), []byte(`{"type":"event_msg","payload":{"type":"thread_settings_applied"}}`+"\n")...)

	// when, then
	if _, err := singleSession(source); err != nil {
		t.Fatalf("validateSession error = %v", err)
	}
}

func TestValidateSessionRejectsIncompleteTurn(t *testing.T) {
	source := fixtureRollout("/source/workspace")
	lastLine := bytes.LastIndex(source[:len(source)-1], []byte{'\n'})
	incomplete := append([]byte(nil), source[:lastLine+1]...)

	// when
	_, err := singleSession(incomplete)

	// then
	if err == nil || !strings.Contains(err.Error(), "turn") {
		t.Fatalf("validateSession error = %v", err)
	}
}

func TestParseRolloutRejectsUnsafeSessionID(t *testing.T) {
	source := bytes.Replace(
		fixtureRollout("/source/workspace"),
		[]byte(testSessionID),
		[]byte("../../../../tmp/not-a-session-id"),
		1,
	)

	// when
	_, err := parseRollout(source)

	// then
	if err == nil || !strings.Contains(err.Error(), "UUIDv7") {
		t.Fatalf("parseRollout error = %v", err)
	}
}

func TestValidateSessionHistoryBase(t *testing.T) {
	for _, test := range []struct {
		name      string
		base      any
		wantError bool
	}{
		{name: "reference to a missing object", base: map[string]any{
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

			// when
			_, err = singleSession(append(append(header, '\n'), rest...))

			// then
			if (err != nil) != test.wantError {
				t.Fatalf("validateSession error = %v, want error %t", err, test.wantError)
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
	for _, path := range []string{active, archived, unrelated, active + rollbackSuffix, active + stageSuffix, active + appendSuffix, unrelated + appendSuffix} {
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
	if !slices.Equal(found.active, []string{active}) || !slices.Equal(found.archived, []string{archived}) ||
		!slices.Equal(found.pending, []string{active}) || !slices.Equal(found.appending, []string{active}) {
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

// testRolloutName is the file name of the rollout fixtureRollout builds.
const testRolloutName = "rollout-2026-09-13T09-58-44-" + testSessionID + ".jsonl"

// singleSession validates data as a session of one rollout.
func singleSession(data []byte) (codexSession, error) {
	file, err := newRolloutFile(testRolloutName, data)
	if err != nil {
		return codexSession{}, err
	}
	return testLayout.validateSession(file.meta.ID, []rolloutFile{file})
}

// relocateRollout moves a session of one rollout from its workspace to target.
func relocateRollout(data []byte, target string) ([]byte, sessionMeta, error) {
	session, err := singleSession(data)
	if err != nil {
		return nil, sessionMeta{}, err
	}
	relocated, err := relocate(testLayout, session, session.head(session.root).meta.CWD, target)
	if err != nil {
		return nil, sessionMeta{}, err
	}
	root := relocated.head(relocated.root)
	return root.data, root.meta, nil
}
