package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type eventKind struct {
	eventType   string
	payloadType string
}

type AppServerCapabilities struct {
	ScanRepair bool `json:"scan_repair"`
	Read       bool `json:"read"`
	Resume     bool `json:"resume"`
	Delete     bool `json:"delete"`
}

type Compatibility struct {
	Version               string
	LayoutFingerprint     string
	RolloutFilenameLayout string
	AppServer             AppServerCapabilities
	completeEvent         eventKind
	workspaceEvents       map[eventKind]struct{}
}

var compatibilities = map[string]Compatibility{
	"0.154.0": {
		Version:               "0.154.0",
		LayoutFingerprint:     "rollout-v1:task-complete:workspace-metadata-v1",
		RolloutFilenameLayout: "date-partitioned-rollout-v1",
		AppServer: AppServerCapabilities{
			ScanRepair: true,
			Read:       true,
			Resume:     true,
			Delete:     true,
		},
		completeEvent: eventKind{eventType: "event_msg", payloadType: "task_complete"},
		workspaceEvents: map[eventKind]struct{}{
			{eventType: "session_meta"}:                                      {},
			{eventType: "world_state"}:                                       {},
			{eventType: "turn_context"}:                                      {},
			{eventType: "event_msg", payloadType: "thread_settings_applied"}: {},
		},
	},
}

func LookupCompatibility(version string) (Compatibility, error) {
	compatibility, ok := compatibilities[version]
	if !ok {
		return Compatibility{}, fmt.Errorf(
			"Codex %s is unsupported; supported versions: %s",
			version,
			strings.Join(SupportedVersions(), ", "),
		)
	}
	return compatibility, nil
}

func SupportedVersions() []string {
	versions := make([]string, 0, len(compatibilities))
	for version := range compatibilities {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	return versions
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
