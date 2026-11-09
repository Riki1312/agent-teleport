package codex

import (
	"bytes"
	"encoding/json"

	"github.com/Riki1312/agent-teleport/internal/semver"
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
		revertHeaderRecords: 2,
	},
}

// layoutFor returns the layout of a Codex version and whether that version is
// within the tested range. Versions older than every layout fail closed.
func layoutFor(version string) (layout, bool, error) {
	return semver.Select("Codex", version, layouts, func(candidate layout) semver.Range {
		return semver.Range{Minimum: candidate.minimum, NewestTested: candidate.newestTested}
	})
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
