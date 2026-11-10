package codex

import (
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
// Rollouts can hold hundreds of megabytes, so the payload is neither copied
// nor decoded beyond its type.
func classifyEvent(line []byte) (eventKind, error) {
	var event struct {
		Type    string      `json:"type"`
		Payload payloadType `json:"payload"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return eventKind{}, err
	}
	return eventKind{eventType: event.Type, payloadType: string(event.Payload)}, nil
}

// payloadType decodes the type member of an event payload, or nothing for a
// payload that is not an object.
type payloadType string

// UnmarshalJSON reads the payload's top-level type member. The outer decode
// has already validated the JSON, so the members are only skipped over.
func (p *payloadType) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '{' {
		return nil
	}
	for index := 1; index < len(data); {
		index = skipSpace(data, index)
		if data[index] != '"' {
			return nil
		}
		keyEnd := stringEnd(data, index)
		key := data[index+1 : keyEnd]
		index = skipSpace(data, keyEnd+1) + 1
		index = skipSpace(data, index)
		valueEnd := valueEnd(data, index)
		if string(key) == "type" && data[index] == '"' {
			var value string
			if err := json.Unmarshal(data[index:valueEnd], &value); err != nil {
				return err
			}
			*p = payloadType(value)
			return nil
		}
		index = skipSpace(data, valueEnd)
		if index >= len(data) || data[index] != ',' {
			return nil
		}
		index++
	}
	return nil
}

func skipSpace(data []byte, index int) int {
	for index < len(data) && (data[index] == ' ' || data[index] == '\t' || data[index] == '\n' || data[index] == '\r') {
		index++
	}
	return index
}

// stringEnd returns the index of the quote closing the string at start.
func stringEnd(data []byte, start int) int {
	for index := start + 1; index < len(data); index++ {
		switch data[index] {
		case '\\':
			index++
		case '"':
			return index
		}
	}
	return len(data) - 1
}

// valueEnd returns the index just after the valid JSON value at start.
func valueEnd(data []byte, start int) int {
	switch data[start] {
	case '"':
		return stringEnd(data, start) + 1
	case '{', '[':
		depth := 0
		for index := start; index < len(data); index++ {
			switch data[index] {
			case '"':
				index = stringEnd(data, index)
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return index + 1
				}
			}
		}
		return len(data)
	default:
		index := start
		for index < len(data) && data[index] != ',' && data[index] != '}' && data[index] != ']' {
			index++
		}
		return index
	}
}
