package codex

import (
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

func (c Compatibility) isWorkspaceMetadata(event map[string]any) bool {
	kind := classifyEvent(event)
	_, ok := c.workspaceEvents[kind]
	return ok
}

func (c Compatibility) isCompleteEvent(event map[string]any) bool {
	return classifyEvent(event) == c.completeEvent
}

func classifyEvent(event map[string]any) eventKind {
	eventType, _ := event["type"].(string)
	payload, _ := event["payload"].(map[string]any)
	payloadType, _ := payload["type"].(string)
	return eventKind{eventType: eventType, payloadType: payloadType}
}
