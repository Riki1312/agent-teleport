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
	"errors"
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
const BinaryEnvironment = "CXPORT_CODEX"

// rolloutPayload is the archive path of a session's rollout file.
const rolloutPayload = "rollout.jsonl"

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
	ID        string
	Title     string
	Workspace string
	UpdatedAt time.Time

	rolloutPath string
	status      string
}

// Diagnosis describes the local Codex installation without changing it.
type Diagnosis struct {
	Home              string
	Version           string
	Supported         bool
	SupportedVersions []string
	AppServerError    error
	DetectionError    error
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
	layout, err := layoutFor(version)
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

// Version returns the installed Codex version.
func (s *Store) Version() string {
	return s.version
}

// Sessions lists local sessions, most recently updated first. An empty
// workspace lists sessions from every workspace.
func (s *Store) Sessions(workspace string) ([]Session, error) {
	client, err := s.appServer()
	if err != nil {
		return nil, err
	}
	threads, err := client.List(workspace, false)
	if err != nil {
		return nil, fmt.Errorf("list Codex sessions: %w", err)
	}
	sessions := make([]Session, 0, len(threads))
	for _, thread := range threads {
		title := thread.Name
		if title == "" {
			title = thread.Preview
		}
		sessions = append(sessions, Session{
			ID:          thread.ID,
			Title:       strings.Join(strings.Fields(title), " "),
			Workspace:   thread.CWD,
			UpdatedAt:   time.Unix(thread.UpdatedAt, 0).UTC(),
			rolloutPath: thread.Path,
			status:      thread.Status.Type,
		})
	}
	return sessions, nil
}

// Export snapshots a completed session into an archive created at now. It
// fails while the session has an active or incomplete turn.
func (s *Store) Export(session Session, now time.Time) (archive.Archive, error) {
	if !isQuiescent(session.status) {
		return archive.Archive{}, fmt.Errorf("session %s is not quiescent (status %q)", session.ID, session.status)
	}
	if session.rolloutPath == "" {
		return archive.Archive{}, fmt.Errorf("Codex did not report a rollout path for session %s", session.ID)
	}
	rollout, meta, err := s.layout.stableRollout(session.rolloutPath)
	if err != nil {
		return archive.Archive{}, fmt.Errorf("snapshot session %s: %w", session.ID, err)
	}
	if meta.ID != session.ID {
		return archive.Archive{}, fmt.Errorf("rollout session ID %s does not match selected session %s", meta.ID, session.ID)
	}
	return archive.New(archive.Header{
		Provider:        Provider,
		ProviderVersion: s.version,
		SessionID:       meta.ID,
		Workspace:       meta.CWD,
		CreatedAt:       now,
	}, []archive.Payload{{Path: rolloutPayload, Data: rollout}})
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
	diagnosis := Diagnosis{SupportedVersions: supportedVersions()}
	diagnosis.Home, diagnosis.DetectionError = codexHome()
	if diagnosis.DetectionError != nil {
		return diagnosis
	}
	binary := codexBinary()
	diagnosis.Version, diagnosis.DetectionError = detectVersion(ctx, binary)
	if diagnosis.DetectionError != nil {
		return diagnosis
	}
	_, layoutErr := layoutFor(diagnosis.Version)
	diagnosis.Supported = layoutErr == nil
	client, err := appserver.Start(ctx, binary, diagnosis.Home)
	if err == nil {
		_, err = client.List("", true)
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

// validateArchive checks a Codex archive and returns its layout and rollout.
func validateArchive(source archive.Archive) (layout, []byte, error) {
	if source.Manifest.Provider != Provider {
		return layout{}, nil, fmt.Errorf("archive provider %q is not %s", source.Manifest.Provider, Provider)
	}
	if len(source.Manifest.Files) != 1 {
		return layout{}, nil, errors.New("Codex archive must contain exactly one rollout")
	}
	rollout, ok := source.Payload(rolloutPayload)
	if !ok {
		return layout{}, nil, fmt.Errorf("Codex archive is missing %s", rolloutPayload)
	}
	sourceLayout, err := layoutFor(source.Manifest.ProviderVersion)
	if err != nil {
		return layout{}, nil, err
	}
	meta, err := sourceLayout.validateRollout(rollout)
	if err != nil {
		return layout{}, nil, fmt.Errorf("validate rollout: %w", err)
	}
	if source.Manifest.SessionID != meta.ID {
		return layout{}, nil, fmt.Errorf("manifest session ID %s does not match rollout %s", source.Manifest.SessionID, meta.ID)
	}
	if source.Manifest.Workspace != meta.CWD {
		return layout{}, nil, fmt.Errorf("manifest workspace %s does not match rollout %s", source.Manifest.Workspace, meta.CWD)
	}
	return sourceLayout, rollout, nil
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
