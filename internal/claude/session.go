package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/fsutil"
)

// fileKind classifies the files of a stored session.
type fileKind int

const (
	// transcriptKind is the session transcript, <session>.jsonl.
	transcriptKind fileKind = iota
	// agentTranscriptKind is a sub-agent transcript,
	// <session>/subagents/agent-<id>.jsonl.
	agentTranscriptKind
	// agentMetadataKind describes a sub-agent,
	// <session>/subagents/agent-<id>.meta.json.
	agentMetadataKind
	// toolResultKind is a tool output too large to keep in the transcript,
	// <session>/tool-results/<name>.
	toolResultKind
)

var (
	agentTranscriptName = regexp.MustCompile(`^agent-([A-Za-z0-9_-]+)\.jsonl$`)
	agentMetadataName   = regexp.MustCompile(`^agent-([A-Za-z0-9_-]+)\.meta\.json$`)
	toolResultName      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// sessionFile is one file of a stored session.
type sessionFile struct {
	// path is slash-separated and relative to the session's project folder.
	path string
	kind fileKind
	// agent is the sub-agent ID of an agent transcript or its metadata.
	agent string
	data  []byte
}

// isTranscript reports whether a file is an append-only transcript.
func (f sessionFile) isTranscript() bool {
	return f.kind == transcriptKind || f.kind == agentTranscriptKind
}

// claudeSession is a validated session: its transcript first, then the files
// of its session folder in path order.
type claudeSession struct {
	id    string
	info  transcriptInfo
	files []sessionFile
}

func (s claudeSession) payloads() []archive.Payload {
	payloads := make([]archive.Payload, 0, len(s.files))
	for _, file := range s.files {
		payloads = append(payloads, archive.Payload{Path: file.path, Data: file.data})
	}
	return payloads
}

// classifyPath returns the kind of a session file path and, for sub-agent
// files, the agent ID. Unknown files fail closed.
func classifyPath(sessionID, filePath string) (fileKind, string, error) {
	if filePath == sessionID+".jsonl" {
		return transcriptKind, "", nil
	}
	directory, name := path.Split(filePath)
	switch directory {
	case sessionID + "/subagents/":
		if match := agentTranscriptName.FindStringSubmatch(name); match != nil {
			return agentTranscriptKind, match[1], nil
		}
		if match := agentMetadataName.FindStringSubmatch(name); match != nil {
			return agentMetadataKind, match[1], nil
		}
	case sessionID + "/tool-results/":
		if toolResultName.MatchString(name) {
			return toolResultKind, "", nil
		}
	}
	return 0, "", fmt.Errorf("unrecognized session file %s", filePath)
}

// newSession validates the files of sessionID: one transcript, sub-agent
// transcripts of the same session, and well-formed sub-agent metadata.
func newSession(sessionID string, files []sessionFile) (claudeSession, error) {
	session := claudeSession{id: sessionID}
	for index, file := range files {
		kind, agent, err := classifyPath(sessionID, file.path)
		if err != nil {
			return claudeSession{}, err
		}
		file.kind, file.agent = kind, agent
		switch kind {
		case transcriptKind:
			if index != 0 {
				return claudeSession{}, errors.New("the session transcript must be its first file")
			}
			if session.info, err = parseTranscript(file.data, sessionID, ""); err != nil {
				return claudeSession{}, fmt.Errorf("%s: %w", file.path, err)
			}
		case agentTranscriptKind:
			if _, err := parseTranscript(file.data, sessionID, agent); err != nil {
				return claudeSession{}, fmt.Errorf("%s: %w", file.path, err)
			}
		case agentMetadataKind:
			var metadata map[string]any
			if err := json.Unmarshal(file.data, &metadata); err != nil {
				return claudeSession{}, fmt.Errorf("%s: %w", file.path, err)
			}
		}
		session.files = append(session.files, file)
	}
	if len(session.files) == 0 || session.files[0].kind != transcriptKind {
		return claudeSession{}, fmt.Errorf("session %s has no transcript", sessionID)
	}
	return session, nil
}

// readLocalFiles reads the files Claude Code keeps for sessionID in a project
// folder, the transcript first. It returns no files when the session is
// absent, ignores stage files of an interrupted import, and fails on files of
// an unknown layout.
func readLocalFiles(folder, sessionID string) ([]sessionFile, error) {
	var files []sessionFile
	transcript, err := fsutil.ReadStable(filepath.Join(folder, sessionID+".jsonl"))
	switch {
	case err == nil:
		files = append(files, sessionFile{path: sessionID + ".jsonl", data: transcript})
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	root := filepath.Join(folder, sessionID)
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			if current == root && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() || strings.HasSuffix(entry.Name(), fsutil.StageSuffix) {
			return nil
		}
		relative, err := filepath.Rel(folder, current)
		if err != nil {
			return err
		}
		filePath := filepath.ToSlash(relative)
		if _, _, err := classifyPath(sessionID, filePath); err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("session file %s is not a regular file", filePath)
		}
		data, err := fsutil.ReadStable(current)
		if err != nil {
			return err
		}
		files = append(files, sessionFile{path: filePath, data: data})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read session folder: %w", err)
	}
	return files, nil
}

// readSession reads and validates a stored session.
func readSession(folder, sessionID string) (claudeSession, error) {
	files, err := readLocalFiles(folder, sessionID)
	if err != nil {
		return claudeSession{}, err
	}
	return newSession(sessionID, files)
}

// sessionFromArchive validates the payloads of an archive as one session.
func sessionFromArchive(source archive.Archive) (claudeSession, error) {
	files := make([]sessionFile, 0, len(source.Payloads))
	for _, payload := range source.Payloads {
		files = append(files, sessionFile{path: payload.Path, data: payload.Data})
	}
	return newSession(source.Manifest.SessionID, files)
}

// transcriptFolders returns the project folders that hold a transcript of
// sessionID. Claude Code resumes a session by ID from any of them.
func transcriptFolders(home, sessionID string) ([]string, error) {
	if !validSessionID(sessionID) {
		return nil, fmt.Errorf("invalid Claude Code session ID %q", sessionID)
	}
	matches, err := filepath.Glob(filepath.Join(home, "projects", "*", sessionID+".jsonl"))
	if err != nil {
		return nil, err
	}
	folders := make([]string, 0, len(matches))
	for _, match := range matches {
		folders = append(folders, filepath.Dir(match))
	}
	return folders, nil
}

// readFolderWorkspace returns the workspace of the session stored in folder,
// or the folder name when the transcript cannot be read, for messages about
// copies in other workspaces.
func readFolderWorkspace(folder, sessionID string) string {
	data, err := os.ReadFile(filepath.Join(folder, sessionID+".jsonl"))
	if err != nil {
		return filepath.Base(folder)
	}
	info, err := parseTranscript(completeRecords(data), sessionID, "")
	if err != nil {
		return filepath.Base(folder)
	}
	return info.workspace
}
