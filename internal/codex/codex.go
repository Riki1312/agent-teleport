// Package codex moves OpenAI Codex sessions in and out of agent-teleport
// archives.
//
// Codex storage is a private, versioned implementation detail. The adapter
// accepts only rollout layouts verified for the installed Codex version, and it
// lets Codex App Server rebuild its own thread state instead of writing Codex
// databases directly.
package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/codex/appserver"
)

// Provider identifies Codex sessions in archive manifests.
const Provider = "openai-codex"

// BinaryEnvironment names the variable that overrides the Codex binary path.
const BinaryEnvironment = "AGENT_TELEPORT_CODEX"

// userSources are the thread sources a person starts and can resume.
// Sub-agent threads belong to their parent session and are not listed.
var userSources = []string{"cli", "vscode", "exec", "appServer"}

// allSources are every thread source App Server reports, used to verify that
// an imported session is discovered whatever started it.
var allSources = []string{"cli", "vscode", "exec", "appServer", "subAgent", "subAgentReview", "subAgentCompact", "subAgentThreadSpawn", "subAgentOther", "unknown"}

// Store is the local Codex installation. It starts App Server on first use and
// keeps it running until Close.
type Store struct {
	ctx     context.Context
	binary  string
	home    string
	version string
	layout  layout
	client  *appserver.Client
}

// Session summarizes a local Codex session.
type Session struct {
	ID string
	// Title is the session's name, or Codex's preview of its first message.
	Title     string
	Workspace string
	UpdatedAt time.Time

	rolloutPath string
	status      string
}

// Diagnosis describes the local Codex installation without changing it.
type Diagnosis struct {
	Home    string
	Version string
	// Supported reports whether the version has a known rollout layout, and
	// Tested whether it is within the range verified by the two-home test.
	Supported bool
	Tested    bool
	// MinimumVersion and NewestTestedVersion bound the verified range.
	MinimumVersion      string
	NewestTestedVersion string
	// DetectionError reports why the Codex home or version could not be found.
	DetectionError error
	// AppServerError reports why App Server could not start or list threads.
	AppServerError error
}

// Open detects the local Codex installation and requires a supported version.
func Open(ctx context.Context) (*Store, error) {
	home, err := codexHome()
	if err != nil {
		return nil, err
	}
	binary := codexBinary()
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

// Close stops App Server if the store started it.
func (s *Store) Close() {
	s.client.Close()
	s.client = nil
}

// Sessions lists local sessions, most recently updated first. An empty
// workspace lists sessions from every workspace.
func (s *Store) Sessions(workspace string) ([]Session, error) {
	client, err := s.appServer()
	if err != nil {
		return nil, err
	}
	threads, err := client.List(appserver.ListQuery{CWD: workspace, SourceKinds: userSources})
	if err != nil {
		return nil, fmt.Errorf("list Codex sessions: %w", err)
	}
	sessions := make([]Session, 0, len(threads))
	for _, thread := range threads {
		sessions = append(sessions, newSession(thread))
	}
	return sessions, nil
}

func newSession(thread appserver.Thread) Session {
	title := thread.Name
	if title == "" {
		title = thread.Preview
	}
	return Session{
		ID:          thread.ID,
		Title:       strings.Join(strings.Fields(title), " "),
		Workspace:   thread.CWD,
		UpdatedAt:   time.Unix(thread.UpdatedAt, 0).UTC(),
		rolloutPath: thread.Path,
		status:      thread.Status.Type,
	}
}

// Session returns a local session by its full ID. Codex lists a fork only
// once it has turns of its own, so a session can exist without being listed.
func (s *Store) Session(id string) (Session, error) {
	client, err := s.appServer()
	if err != nil {
		return Session{}, err
	}
	thread, err := client.Read(id)
	if err != nil {
		return Session{}, fmt.Errorf("read Codex session %s: %w", id, err)
	}
	return newSession(thread), nil
}

// Export snapshots a completed session into an archive stamped with createdAt.
// A forked session carries the prefix of every ancestor rollout it extends. It
// fails while the session has an active or incomplete turn.
func (s *Store) Export(session Session, createdAt time.Time) (archive.Archive, error) {
	if !isQuiescent(session.status) {
		return archive.Archive{}, fmt.Errorf("session %s is not quiescent (status %q)", session.ID, session.status)
	}
	if session.rolloutPath == "" {
		return archive.Archive{}, fmt.Errorf("Codex did not report a rollout path for session %s", session.ID)
	}
	exported, err := s.layout.readHistory(s.home, session.rolloutPath)
	if err != nil {
		return archive.Archive{}, fmt.Errorf("snapshot session %s: %w", session.ID, err)
	}
	meta := exported.session().meta
	if meta.ID != session.ID {
		return archive.Archive{}, fmt.Errorf("rollout session ID %s does not match selected session %s", meta.ID, session.ID)
	}
	return archive.New(archive.Header{
		Provider:        Provider,
		ProviderVersion: s.version,
		SessionID:       meta.ID,
		Workspace:       meta.CWD,
		CreatedAt:       createdAt,
	}, exported.payloads())
}

// ResumeCommand returns the Codex command that continues sessionID in
// workspace.
func (s *Store) ResumeCommand(sessionID, workspace string) *exec.Cmd {
	command := exec.CommandContext(s.ctx, s.binary, "resume", sessionID)
	command.Dir = workspace
	return command
}

// ValidateArchive checks that a Codex archive is internally consistent for the
// Codex version that wrote it. It needs no local Codex installation.
func ValidateArchive(source archive.Archive) error {
	_, _, err := validateArchive(source)
	return err
}

// Diagnose reports the local Codex installation and whether App Server starts.
func Diagnose(ctx context.Context) Diagnosis {
	diagnosis := Diagnosis{MinimumVersion: layouts[0].minimum, NewestTestedVersion: layouts[len(layouts)-1].newestTested}
	diagnosis.Home, diagnosis.DetectionError = codexHome()
	if diagnosis.DetectionError != nil {
		return diagnosis
	}
	binary := codexBinary()
	diagnosis.Version, diagnosis.DetectionError = detectVersion(ctx, binary)
	if diagnosis.DetectionError != nil {
		return diagnosis
	}
	_, tested, layoutErr := layoutFor(diagnosis.Version)
	diagnosis.Supported = layoutErr == nil
	diagnosis.Tested = tested
	client, err := appserver.Start(ctx, binary, diagnosis.Home)
	if err == nil {
		_, err = client.List(appserver.ListQuery{StateOnly: true})
		client.Close()
	}
	diagnosis.AppServerError = err
	return diagnosis
}

func (s *Store) appServer() (*appserver.Client, error) {
	if s.client != nil {
		return s.client, nil
	}
	client, err := appserver.Start(s.ctx, s.binary, s.home)
	if err != nil {
		return nil, err
	}
	s.client = client
	return client, nil
}

// validateArchive checks a Codex archive and returns its layout and history.
func validateArchive(source archive.Archive) (layout, history, error) {
	if source.Manifest.Provider != Provider {
		return layout{}, history{}, fmt.Errorf("archive provider %q is not %s", source.Manifest.Provider, Provider)
	}
	sourceLayout, _, err := layoutFor(source.Manifest.ProviderVersion)
	if err != nil {
		return layout{}, history{}, err
	}
	stored, err := sourceLayout.historyFromArchive(source)
	if err != nil {
		return layout{}, history{}, fmt.Errorf("validate session history: %w", err)
	}
	meta := stored.session().meta
	if source.Manifest.SessionID != meta.ID {
		return layout{}, history{}, fmt.Errorf("manifest session ID %s does not match rollout %s", source.Manifest.SessionID, meta.ID)
	}
	if source.Manifest.Workspace != meta.CWD {
		return layout{}, history{}, fmt.Errorf("manifest workspace %s does not match rollout %s", source.Manifest.Workspace, meta.CWD)
	}
	return sourceLayout, stored, nil
}

// isQuiescent reports whether an App Server status allows reading or replacing
// a rollout. Another process's live session reports notLoaded, so callers also
// require a completed final turn.
func isQuiescent(status string) bool {
	return status == "idle" || status == "notLoaded"
}

func codexHome() (string, error) {
	if value := os.Getenv("CODEX_HOME"); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate user home: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
}

func codexBinary() string {
	if value := os.Getenv(BinaryEnvironment); value != "" {
		return value
	}
	return "codex"
}

func detectVersion(ctx context.Context, binary string) (string, error) {
	output, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", binary, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[0] != "codex-cli" {
		return "", fmt.Errorf("unexpected Codex version output %q", strings.TrimSpace(string(output)))
	}
	return fields[1], nil
}
