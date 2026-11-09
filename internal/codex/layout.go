package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// eventKind identifies a rollout event by its type and, for wrapped events, the
// type of its payload.
type eventKind struct {
	eventType   string
	payloadType string
}

// layout describes the rollout format written by one Codex version.
type layout struct {
	// completeEvent is the event Codex writes when a turn completes.
	completeEvent eventKind
	// workspaceEvents carry the workspace metadata Codex reads when it rebuilds
	// thread state from a rollout.
	workspaceEvents map[eventKind]struct{}
}

// layouts maps every supported Codex version to the rollout layout verified for
// it by a sanitized fixture and the two-home transfer test.
var layouts = map[string]layout{
	"0.154.0": {
		completeEvent: eventKind{eventType: "event_msg", payloadType: "task_complete"},
		workspaceEvents: map[eventKind]struct{}{
			{eventType: "session_meta"}:                                      {},
			{eventType: "world_state"}:                                       {},
			{eventType: "turn_context"}:                                      {},
			{eventType: "event_msg", payloadType: "thread_settings_applied"}: {},
		},
	},
}

func layoutFor(version string) (layout, error) {
	layout, ok := layouts[version]
	if !ok {
		return layout, fmt.Errorf("Codex %s is unsupported; supported versions: %s", version, strings.Join(supportedVersions(), ", "))
	}
	return layout, nil
}

func supportedVersions() []string {
	versions := make([]string, 0, len(layouts))
	for version := range layouts {
		versions = append(versions, version)
	}
	slices.Sort(versions)
	return versions
}

func (l layout) isWorkspaceEvent(kind eventKind) bool {
	_, ok := l.workspaceEvents[kind]
	return ok
}

// classifyEvent decodes only the event and payload types of one rollout line.
func classifyEvent(line []byte) (eventKind, error) {
	var event struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return eventKind{}, err
	}
	kind := eventKind{eventType: event.Type}
	if bytes.HasPrefix(bytes.TrimSpace(event.Payload), []byte{'{'}) {
		var payload struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return eventKind{}, err
		}
		kind.payloadType = payload.Type
	}
	return kind, nil
}
