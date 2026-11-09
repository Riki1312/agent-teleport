// Package claude moves Claude Code sessions in and out of agent-teleport
// archives.
//
// Claude Code keeps each session as an append-only JSONL transcript under
// projects/<folder>/, where the folder is named after the session's workspace,
// with sub-agent transcripts and large tool outputs in a session folder beside
// it. Claude Code has no index: it resumes a session by finding its transcript.
// Its storage is a private, versioned implementation detail, so the adapter
// accepts only transcript layouts verified for the installed version.
package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/provider"
)

// Provider identifies Claude Code sessions in archive manifests.
const Provider = "anthropic-claude"

// BinaryEnvironment names the variable that overrides the Claude Code binary
// path.
const BinaryEnvironment = "AGENT_TELEPORT_CLAUDE"

// Adapter registers Claude Code with the command-line interface.
var Adapter = provider.Adapter{
	ID:      Provider,
	Name:    "Claude Code",
	Command: "claude",
	Open: func(ctx context.Context) (provider.Store, error) {
		store, err := Open(ctx)
		if err != nil {
			return nil, err
		}
		return store, nil
	},
	Diagnose:        Diagnose,
	ValidateArchive: ValidateArchive,
}

// Store is the local Claude Code installation.
type Store struct {
	ctx     context.Context
	binary  string
	home    string
	version string
	layout  layout
}

// Open detects the local Claude Code installation and requires a supported
// version.
func Open(ctx context.Context) (*Store, error) {
	home, err := claudeHome()
	if err != nil {
		return nil, err
	}
	binary := claudeBinary()
	version, err := detectVersion(ctx, binary)
	if err != nil {
		return nil, err
	}
	layout, _, err := layoutFor(version)
	if err != nil {
		return nil, err
	}
	return &Store{ctx: ctx, binary: binary, home: home, version: version, layout: layout}, nil
}

// Close releases nothing; Claude Code needs no running process.
func (s *Store) Close() {}

// Sessions lists local sessions, most recently updated first. An empty
// workspace lists sessions from every workspace. Transcripts that do not parse
// are skipped; exporting them by ID reports why.
func (s *Store) Sessions(workspace string) ([]provider.Session, error) {
	folder := "*"
	if workspace != "" {
		folder = projectFolderName(workspace)
	}
	paths, err := filepath.Glob(filepath.Join(s.home, "projects", folder, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sessions := make([]provider.Session, 0, len(paths))
	for _, path := range paths {
		id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if !validSessionID(id) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("list Claude Code sessions: %w", err)
		}
		info, err := parseTranscript(completeRecords(data), id, "")
		if err != nil || (workspace != "" && info.workspace != workspace) {
			continue
		}
		sessions = append(sessions, listedSession(id, info))
	}
	slices.SortStableFunc(sessions, func(a, b provider.Session) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return sessions, nil
}

func listedSession(id string, info transcriptInfo) provider.Session {
	return provider.Session{
		Provider:  Provider,
		ID:        id,
		Title:     info.title,
		Workspace: info.workspace,
		UpdatedAt: info.updatedAt,
	}
}

// Session returns a local session by its full ID.
func (s *Store) Session(id string) (provider.Session, error) {
	folder, err := s.sessionFolder(id)
	if err != nil {
		return provider.Session{}, err
	}
	session, err := readSession(folder, id)
	if err != nil {
		return provider.Session{}, fmt.Errorf("read Claude Code session %s: %w", id, err)
	}
	return listedSession(id, session.info), nil
}

// sessionFolder returns the project folder that holds the transcript of id.
func (s *Store) sessionFolder(id string) (string, error) {
	folders, err := transcriptFolders(s.home, id)
	if err != nil {
		return "", err
	}
	switch len(folders) {
	case 0:
		return "", fmt.Errorf("Claude Code session %s not found", id)
	case 1:
		return folders[0], nil
	default:
		return "", fmt.Errorf("Claude Code session %s is stored in several project folders: %s", id, strings.Join(folders, ", "))
	}
}

// Export snapshots a session into an archive stamped with createdAt: its
// transcript, its sub-agent transcripts, and its saved tool outputs. It fails
// while a Claude Code process is working on the session.
func (s *Store) Export(id string, createdAt time.Time) (archive.Archive, error) {
	folder, err := s.sessionFolder(id)
	if err != nil {
		return archive.Archive{}, err
	}
	live, open, err := sessionProcess(s.home, id)
	if err != nil {
		return archive.Archive{}, err
	}
	if open && live.Status != "idle" {
		return archive.Archive{}, fmt.Errorf("session %s is %s in Claude Code (process %d); wait for its turn to finish", id, live.Status, live.PID)
	}
	session, err := readSession(folder, id)
	if err != nil {
		return archive.Archive{}, fmt.Errorf("snapshot session %s: %w", id, err)
	}
	if projectFolderName(session.info.workspace) != filepath.Base(folder) {
		return archive.Archive{}, fmt.Errorf("session %s started in %s but is stored in project folder %s", id, session.info.workspace, filepath.Base(folder))
	}
	return archive.New(archive.Header{
		Provider:        Provider,
		ProviderVersion: s.version,
		SessionID:       id,
		Workspace:       session.info.workspace,
		Home:            s.home,
		CreatedAt:       createdAt,
	}, session.payloads())
}

// ResumeCommand returns the Claude Code command that continues sessionID in
// workspace.
func (s *Store) ResumeCommand(sessionID, workspace string) *exec.Cmd {
	command := exec.CommandContext(s.ctx, s.binary, "--resume", sessionID)
	command.Dir = workspace
	return command
}

// ValidateArchive checks that a Claude Code archive is internally consistent
// for the Claude Code version that wrote it. It needs no local installation.
func ValidateArchive(source archive.Archive) error {
	_, _, err := validateArchive(source)
	return err
}

// validateArchive checks a Claude Code archive and returns its layout and
// session.
func validateArchive(source archive.Archive) (layout, claudeSession, error) {
	if source.Manifest.Provider != Provider {
		return layout{}, claudeSession{}, fmt.Errorf("archive provider %q is not %s", source.Manifest.Provider, Provider)
	}
	sourceLayout, _, err := layoutFor(source.Manifest.ProviderVersion)
	if err != nil {
		return layout{}, claudeSession{}, err
	}
	if !validSessionID(source.Manifest.SessionID) {
		return layout{}, claudeSession{}, fmt.Errorf("invalid Claude Code session ID %q", source.Manifest.SessionID)
	}
	session, err := sessionFromArchive(source)
	if err != nil {
		return layout{}, claudeSession{}, fmt.Errorf("validate session: %w", err)
	}
	if source.Manifest.Workspace != session.info.workspace {
		return layout{}, claudeSession{}, fmt.Errorf("manifest workspace %s does not match transcript %s", source.Manifest.Workspace, session.info.workspace)
	}
	return sourceLayout, session, nil
}

// Diagnose reports the local Claude Code installation.
func Diagnose(ctx context.Context) provider.Diagnosis {
	diagnosis := provider.Diagnosis{MinimumVersion: layouts[0].minimum, NewestTestedVersion: layouts[len(layouts)-1].newestTested}
	diagnosis.Home, diagnosis.Err = claudeHome()
	if diagnosis.Err != nil {
		return diagnosis
	}
	diagnosis.Version, diagnosis.Err = detectVersion(ctx, claudeBinary())
	if diagnosis.Err != nil {
		return diagnosis
	}
	_, tested, layoutErr := layoutFor(diagnosis.Version)
	diagnosis.Supported = layoutErr == nil
	diagnosis.Tested = tested
	return diagnosis
}

func claudeHome() (string, error) {
	if value := os.Getenv("CLAUDE_CONFIG_DIR"); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate user home: %w", err)
	}
	return filepath.Join(home, ".claude"), nil
}

func claudeBinary() string {
	if value := os.Getenv(BinaryEnvironment); value != "" {
		return value
	}
	return "claude"
}

func detectVersion(ctx context.Context, binary string) (string, error) {
	output, err := exec.CommandContext(ctx, binary, "--version").Output()
	if errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("Claude Code is %w: %s was not found", provider.ErrNotInstalled, binary)
	}
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", binary, err)
	}
	version, product, found := strings.Cut(strings.TrimSpace(string(output)), " ")
	if !found || product != "(Claude Code)" {
		return "", fmt.Errorf("unexpected Claude Code version output %q", strings.TrimSpace(string(output)))
	}
	return version, nil
}
