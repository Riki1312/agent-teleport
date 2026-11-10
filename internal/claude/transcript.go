package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// record holds the fields of a transcript record that the adapter reads.
type record struct {
	Type        string       `json:"type"`
	SessionID   string       `json:"sessionId"`
	AgentID     string       `json:"agentId"`
	CWD         string       `json:"cwd"`
	Timestamp   string       `json:"timestamp"`
	IsMeta      bool         `json:"isMeta"`
	IsSidechain bool         `json:"isSidechain"`
	AITitle     string       `json:"aiTitle"`
	CustomTitle string       `json:"customTitle"`
	Message     borrowedJSON `json:"message"`
}

// borrowedJSON keeps a JSON value as a slice of the decoded record instead of
// a copy, because transcripts can hold hundreds of megabytes of messages. It
// is valid while the record's bytes are.
type borrowedJSON []byte

// UnmarshalJSON keeps data without copying it.
func (b *borrowedJSON) UnmarshalJSON(data []byte) error {
	*b = data
	return nil
}

// transcriptInfo summarizes a transcript.
type transcriptInfo struct {
	// workspaces are the distinct working directories the records name, in
	// the order they first appear. A session that moved into a worktree names
	// both the checkout it started in and the worktree.
	workspaces []string
	// title is the session's custom title, else its generated title, else its
	// first prompt.
	title     string
	updatedAt time.Time
}

// parseTranscript checks that data is a sequence of complete JSON records of
// sessionID and, for a sub-agent transcript, of agentID. It requires at least
// one message and a recorded working directory.
func parseTranscript(data []byte, sessionID, agentID string) (transcriptInfo, error) {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return transcriptInfo{}, errors.New("transcript does not end with a complete record")
	}
	var info transcriptInfo
	var customTitle, generatedTitle, firstPrompt string
	messages := 0
	for index, line := range bytes.Split(data[:len(data)-1], []byte{'\n'}) {
		var entry record
		if err := json.Unmarshal(line, &entry); err != nil {
			return transcriptInfo{}, fmt.Errorf("transcript line %d: %w", index+1, err)
		}
		if entry.SessionID != "" && entry.SessionID != sessionID {
			return transcriptInfo{}, fmt.Errorf("transcript line %d belongs to session %s, not %s", index+1, entry.SessionID, sessionID)
		}
		if agentID != "" && entry.AgentID != "" && entry.AgentID != agentID {
			return transcriptInfo{}, fmt.Errorf("transcript line %d belongs to agent %s, not %s", index+1, entry.AgentID, agentID)
		}
		if entry.CWD != "" && !slices.Contains(info.workspaces, entry.CWD) {
			info.workspaces = append(info.workspaces, entry.CWD)
		}
		if timestamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil && timestamp.After(info.updatedAt) {
			info.updatedAt = timestamp
		}
		switch {
		case entry.CustomTitle != "":
			customTitle = entry.CustomTitle
		case entry.AITitle != "":
			generatedTitle = entry.AITitle
		}
		if (entry.Type == "user" || entry.Type == "assistant") && len(entry.Message) > 0 {
			messages++
			if firstPrompt == "" && entry.Type == "user" && !entry.IsMeta && !entry.IsSidechain {
				firstPrompt = promptText(entry.Message)
			}
		}
	}
	if messages == 0 {
		return transcriptInfo{}, errors.New("transcript has no messages")
	}
	if len(info.workspaces) == 0 {
		return transcriptInfo{}, errors.New("transcript records no working directory")
	}
	for _, title := range []string{customTitle, generatedTitle, firstPrompt} {
		if title = strings.Join(strings.Fields(title), " "); title != "" {
			info.title = title
			break
		}
	}
	return info, nil
}

// lastPromptOffset returns the byte offset where the turn of the last prompt
// a person sent in the main conversation starts: at the queue records that
// enqueued the prompt, or at the prompt itself.
func lastPromptOffset(data []byte) int {
	last, offset, queued := 0, 0, -1
	for line := range bytes.Lines(data) {
		var entry record
		_ = json.Unmarshal(line, &entry)
		switch {
		case entry.Type == "queue-operation":
			if queued < 0 {
				queued = offset
			}
		case entry.Type == "user" && !entry.IsMeta && !entry.IsSidechain && promptText(entry.Message) != "":
			last = offset
			if queued >= 0 {
				last = queued
			}
			queued = -1
		default:
			queued = -1
		}
		offset += len(line)
	}
	return last
}

// workspaceIn returns the recorded working directory whose project folder is
// folderName, which Claude Code names after the directory it stores the
// session for.
func (i transcriptInfo) workspaceIn(folderName string) (string, bool) {
	for _, workspace := range i.workspaces {
		if projectFolderName(workspace) == folderName {
			return workspace, true
		}
	}
	return "", false
}

// completeRecords returns the prefix of data that ends with its last complete
// record, so a transcript that Claude Code is appending to can be listed.
func completeRecords(data []byte) []byte {
	return data[:bytes.LastIndexByte(data, '\n')+1]
}

// promptText returns the text a person typed in a user message, or "" for
// messages that carry only tool results.
func promptText(message []byte) string {
	var decoded struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message, &decoded) != nil {
		return ""
	}
	var text string
	if json.Unmarshal(decoded.Content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(decoded.Content, &blocks) != nil {
		return ""
	}
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			return block.Text
		}
	}
	return ""
}

// maxProjectFolderName is the longest project folder name Claude Code uses
// before it shortens the name and appends a hash.
const maxProjectFolderName = 200

// projectFolderName returns the name of the folder under projects/ where
// Claude Code stores the sessions started in workspace. Every UTF-16 code unit
// that is not an ASCII letter or digit becomes a dash, and long names are cut
// and suffixed with a base-36 hash of the workspace path.
func projectFolderName(workspace string) string {
	units := utf16.Encode([]rune(workspace))
	var name strings.Builder
	for _, unit := range units {
		if unit < 0x80 && isASCIIAlphanumeric(byte(unit)) {
			name.WriteByte(byte(unit))
		} else {
			name.WriteByte('-')
		}
	}
	if name.Len() <= maxProjectFolderName {
		return name.String()
	}
	return name.String()[:maxProjectFolderName] + "-" + strconv.FormatInt(absInt64(int64(stringHash(units))), 36)
}

// stringHash is the 32-bit string hash Claude Code applies to folder names.
func stringHash(units []uint16) int32 {
	var hash int32
	for _, unit := range units {
		hash = 31*hash + int32(unit)
	}
	return hash
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func isASCIIAlphanumeric(character byte) bool {
	return (character >= '0' && character <= '9') ||
		(character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z')
}

// validSessionID reports whether id has the shape of a Claude Code session ID,
// a UUID, which keeps it safe to use in file names.
func validSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for index, character := range id {
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdef", character) {
				return false
			}
		}
	}
	return true
}
