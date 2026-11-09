package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// eventKind identifies a rollout event by its type and, for wrapped events, the
// type of its payload.
type eventKind struct {
	eventType   string
	payloadType string
}

// layout describes the rollout format written by a range of Codex versions.
type layout struct {
	// minimum is the oldest Codex version supported with this layout.
	minimum string
	// newestTested is the newest Codex version verified by the two-home test.
	// Newer versions are accepted with a warning while their rollouts still
	// pass structural validation.
	newestTested string
	// turnStarted and turnEnded mark turn boundaries; a rollout is complete
	// when every started turn has ended.
	turnStarted eventKind
	turnEnded   map[eventKind]struct{}
	// workspaceEvents carry the workspace metadata Codex reads when it rebuilds
	// thread state from a rollout.
	workspaceEvents map[eventKind]struct{}
	// revertHeaderRecords is the number of records Codex writes at the start
	// of a revert snapshot, before any later turn.
	revertHeaderRecords int
}

// layouts lists the supported rollout layouts, oldest first. Each layout is
// verified by the sanitized fixture in testdata/codex and the two-home test
// against every Codex version in CI.
var layouts = []layout{
	{
		minimum:      "0.165.0",
		newestTested: "0.167.0",
		turnStarted:  eventKind{eventType: "event_msg", payloadType: "task_started"},
		turnEnded: map[eventKind]struct{}{
			{eventType: "event_msg", payloadType: "task_complete"}: {},
			{eventType: "event_msg", payloadType: "turn_aborted"}:  {},
		},
		workspaceEvents: map[eventKind]struct{}{
			{eventType: "session_meta"}:                                      {},
			{eventType: "world_state"}:                                       {},
			{eventType: "turn_context"}:                                      {},
			{eventType: "event_msg", payloadType: "thread_settings_applied"}: {},
		},
		revertHeaderRecords: 2,
	},
}

// layoutFor returns the layout of a Codex version and whether that version is
// within the tested range. Versions older than every layout fail closed.
func layoutFor(version string) (layout, bool, error) {
	parsed, err := parseVersion(version)
	if err != nil {
		return layout{}, false, err
	}
	for index := len(layouts) - 1; index >= 0; index-- {
		candidate := layouts[index]
		if parsed.compare(mustParseVersion(candidate.minimum)) >= 0 {
			tested := parsed.compare(mustParseVersion(candidate.newestTested)) <= 0
			return candidate, tested, nil
		}
	}
	return layout{}, false, fmt.Errorf("Codex %s is unsupported; agent-teleport supports Codex %s or newer", version, layouts[0].minimum)
}

func (l layout) isWorkspaceEvent(kind eventKind) bool {
	_, ok := l.workspaceEvents[kind]
	return ok
}

func (l layout) isTurnEnd(kind eventKind) bool {
	_, ok := l.turnEnded[kind]
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

// version is a Codex release version such as 0.167.0 or 0.168.0-alpha.1.
type version struct {
	numbers    [3]int
	prerelease bool
}

func parseVersion(value string) (version, error) {
	release, prerelease, _ := strings.Cut(value, "-")
	parts := strings.Split(release, ".")
	if len(parts) != 3 {
		return version{}, fmt.Errorf("unrecognized Codex version %q", value)
	}
	parsed := version{prerelease: prerelease != ""}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return version{}, fmt.Errorf("unrecognized Codex version %q", value)
		}
		parsed.numbers[index] = number
	}
	return parsed, nil
}

func mustParseVersion(value string) version {
	parsed, err := parseVersion(value)
	if err != nil {
		panic(err)
	}
	return parsed
}

// compare orders versions; a prerelease sorts before its release.
func (v version) compare(other version) int {
	for index := range v.numbers {
		if v.numbers[index] != other.numbers[index] {
			if v.numbers[index] < other.numbers[index] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.prerelease == other.prerelease:
		return 0
	case v.prerelease:
		return -1
	default:
		return 1
	}
}
