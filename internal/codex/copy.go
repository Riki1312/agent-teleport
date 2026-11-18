package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Riki1312/agent-teleport/internal/codex/appserver"
	"github.com/Riki1312/agent-teleport/internal/provider"
)

// threadPlanError identifies the two planning failures for which copy mode
// can safely choose a different thread or recover an earlier write.
type threadPlanError struct {
	thread      string
	interrupted bool
	cause       error
}

func (e threadPlanError) Error() string { return e.cause.Error() }
func (e threadPlanError) Unwrap() error { return e.cause }

// copiedThreadID preserves the source UUIDv7 timestamp and puts the copy in a
// deterministic namespace. The mapping is independent of archive contents,
// so a later export or a sibling fork finds the same destination thread.
func copiedThreadID(source, workspace, id string) string {
	digest := sha256.Sum256([]byte("agent-teleport/codex/conflict-copy/v1\x00" + source + "\x00" + workspace + "\x00" + id))
	digest[2] = digest[2]&0x3f | 0x80
	parts := hex.EncodeToString(digest[:10])
	return id[:14] + "7" + parts[:3] + "-" + parts[4:8] + "-" + parts[8:]
}

// planConflictCopy retries only recognized history conflicts, rewriting the
// incoming graph after each new mapping. A second conflict at the mapped ID
// identifies another history under this source label and is left untouched.
func (s *Store) planConflictCopy(client *appserver.Client, original codexSession, source string) (codexSession, sessionPlan, []provider.ThreadCopy, error) {
	threads := original.threads()
	workspace := original.head(original.root).meta.CWD
	ids := make(map[string]string, len(threads))
	copied := make(map[string]bool, len(threads))
	for _, thread := range threads {
		id := copiedThreadID(source, workspace, thread)
		stored, err := findSession(s.home, id)
		if err != nil {
			return codexSession{}, sessionPlan{}, nil, err
		}
		if len(stored.active)+len(stored.archived)+len(stored.pending)+len(stored.appending)+len(stored.snapshots) > 0 {
			ids[thread] = id
			copied[id] = true
		}
	}

	session, err := s.layout.copyIdentities(original, ids)
	if err != nil {
		return codexSession{}, sessionPlan{}, nil, err
	}
	recovered := map[string]bool{}
	for {
		plan, err := s.planWithCopies(session, false, copied)
		if err == nil {
			var changes []provider.ThreadCopy
			for _, thread := range threads {
				if id := ids[thread]; id != "" {
					changes = append(changes, provider.ThreadCopy{SourceID: thread, DestinationID: id})
				}
			}
			return session, plan, changes, nil
		}
		var failure threadPlanError
		if !errors.As(err, &failure) {
			return codexSession{}, sessionPlan{}, nil, err
		}
		if failure.interrupted {
			if client == nil || recovered[failure.thread] {
				return codexSession{}, sessionPlan{}, nil, err
			}
			if err := s.recoverThread(client, session.root, failure.thread); err != nil {
				return codexSession{}, sessionPlan{}, nil, err
			}
			recovered[failure.thread] = true
			continue
		}
		if copied[failure.thread] {
			return codexSession{}, sessionPlan{}, nil, fmt.Errorf("history at copied thread %s conflicts with this archive; use a different source label for a different history: %w", failure.thread, err)
		}
		id := copiedThreadID(source, workspace, failure.thread)
		session, err = s.layout.copyIdentities(session, map[string]string{failure.thread: id})
		if err != nil {
			return codexSession{}, sessionPlan{}, nil, err
		}
		ids[failure.thread] = id
		copied[id] = true
	}
}

// copyIdentities rewrites provider-owned IDs only. Every replacement is
// exactly 36 bytes, so fork ordinals and byte offsets remain valid.
func (l layout) copyIdentities(session codexSession, ids map[string]string) (codexSession, error) {
	if len(ids) == 0 {
		return session, nil
	}
	files := make([]rolloutFile, 0, len(session.files))
	for _, file := range session.files {
		var data []byte
		offset, index := 0, 0
		for line := range bytes.Lines(file.data) {
			kind := file.kinds[index]
			// Response items are model-side records. IDs in their command
			// arguments, tool outputs, or text belong to that history.
			if kind.eventType != "response_item" && containsThreadID(line, ids) {
				var err error
				data, err = rewriteIdentityFields(file.data, data, offset, line, kind.eventType, ids)
				if err != nil {
					return codexSession{}, fmt.Errorf("%s record %d: %w", file.name, index+1, err)
				}
			}
			offset += len(line)
			index++
		}
		if data != nil {
			var err error
			file, err = file.rewritten(data)
			if err != nil {
				return codexSession{}, err
			}
		}
		match := rolloutName.FindStringSubmatchIndex(file.name)
		originalID := file.name[match[8]:match[9]]
		if id := ids[originalID]; id != "" {
			if file.meta.ID != id {
				return codexSession{}, fmt.Errorf("%s has an unsupported encoding of its thread ID", file.name)
			}
			file.name = file.name[:match[8]] + id + file.name[match[9]:]
			if file.object == originalID {
				file.object = id
			}
		}
		files = append(files, file)
	}
	if id := ids[session.root]; id != "" {
		session.root = id
	}
	return l.validateSession(session.root, files)
}

func containsThreadID(line []byte, ids map[string]string) bool {
	for id := range ids {
		if bytes.Contains(line, []byte(id)) {
			return true
		}
	}
	return false
}

// identityFields lists the schema positions that refer to session or rollout
// IDs in native records. Other strings, including structured tool arguments
// and results, are carried through as authored.
var identityFields = map[string]map[string]bool{
	"session_meta": {
		"$.payload.id": true, "$.payload.session_id": true, "$.payload.forked_from_id": true,
		"$.payload.parent_thread_id": true, "$.payload.history_base.thread_id": true,
		"$.payload.source.subagent.thread_spawn.parent_thread_id": true,
	},
	"token_usage_record": {
		"$.payload.thread_id": true, "$.payload.session_id": true,
	},
	"compacted": {
		"$.payload.latest_token_usage_record.thread_id": true, "$.payload.latest_token_usage_record.session_id": true,
		"$.payload.resume_metadata.dynamic_tools.thread_id":   true,
		"$.payload.resume_metadata.dynamic_tools[].thread_id": true,
	},
	"event_msg": {
		"$.payload.thread_id": true, "$.payload.session_id": true,
		"$.payload.parent_thread_id": true, "$.payload.child_thread_id": true,
		"$.payload.sender_thread_id": true, "$.payload.receiver_thread_id": true,
		"$.payload.receiver_thread_ids[]": true,
		"$.payload.item.agent_thread_id":  true, "$.payload.item.sender_thread_id": true,
		"$.payload.item.receiver_thread_id": true, "$.payload.item.receiver_thread_ids[]": true,
		"$.payload.item.agentThreadId": true, "$.payload.item.senderThreadId": true,
		"$.payload.item.receiverThreadIds[]": true,
	},
}

// rewriteIdentityFields visits already validated JSON without re-encoding
// records or their text. Offsets index original, including when the record
// is a slice of a large rollout. Allocate a copy only for a matched field.
func rewriteIdentityFields(original, rewritten []byte, offset int, line []byte, event string, ids map[string]string) ([]byte, error) {
	replace := func(start, end int, id string) error {
		if end-start != len(id) {
			return errors.New("unsupported escaped Codex thread ID")
		}
		if rewritten == nil {
			rewritten = bytes.Clone(original)
		}
		copy(rewritten[offset+start:offset+end], id)
		return nil
	}
	var visit func(int, string) error
	visit = func(start int, path string) error {
		start = skipSpace(line, start)
		switch line[start] {
		case '{', '[':
			object := line[start] == '{'
			for pos := skipSpace(line, start+1); line[pos] != '}' && line[pos] != ']'; pos = skipSpace(line, pos+1) {
				child := path + "[]"
				if object {
					end := stringEnd(line, pos)
					key, err := identityString(line[pos : end+1])
					if err != nil {
						return err
					}
					if id := ids[key]; id != "" && event == "event_msg" && (path == "$.payload.item.agents_states" || path == "$.payload.item.agentsStates") {
						if err := replace(pos+1, end, id); err != nil {
							return err
						}
					}
					child = path + "." + key
					if strings.ContainsAny(key, ".[]") {
						// Arbitrary map keys cannot define provider field paths.
						child = ""
					}
					pos = skipSpace(line, skipSpace(line, end+1)+1)
				}
				end := valueEnd(line, pos)
				if child != "" && containsThreadID(line[pos:end], ids) {
					if err := visit(pos, child); err != nil {
						return err
					}
				}
				pos = skipSpace(line, end)
				if line[pos] != ',' {
					break
				}
			}
		case '"':
			end := stringEnd(line, start)
			value, err := identityString(line[start : end+1])
			if err != nil {
				return err
			}
			id := ids[value]
			if id == "" {
				return nil
			}
			if identityFields[event][path] {
				return replace(start+1, end, id)
			}
			field := strings.ReplaceAll(strings.ToLower(path[strings.LastIndexByte(path, '.')+1:]), "_", "")
			if strings.Contains(field, "threadid") || strings.Contains(field, "sessionid") {
				return fmt.Errorf("unsupported Codex identity field %s in %s", path, event)
			}
		}
		return nil
	}
	err := visit(0, "$")
	return rewritten, err
}

// identityString decodes a JSON string while leaving plain native IDs and
// keys on the fast path.
func identityString(data []byte) (string, error) {
	if !bytes.ContainsRune(data, '\\') {
		return string(data[1 : len(data)-1]), nil
	}
	var value string
	err := json.Unmarshal(data, &value)
	return value, err
}
