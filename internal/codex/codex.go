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
	"github.com/Riki1312/agent-teleport/internal/provider"
)

// Provider identifies Codex sessions in archive manifests.
const Provider = "openai-codex"

// Adapter registers Codex with the command-line interface.
var Adapter = provider.Adapter{
	ID:      Provider,
	Name:    "Codex",
	Command: "codex",
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
func (s *Store) Sessions(workspace string) ([]provider.Session, error) {
	client, err := s.appServer()
	if err != nil {
		return nil, err
	}
	threads, err := client.List(appserver.ListQuery{CWD: workspace, SourceKinds: userSources})
	if err != nil {
		return nil, fmt.Errorf("list Codex sessions: %w", err)
	}
	// A scanning listing reports a reverted thread twice: once from Codex's
	// database and once from its original rollout file.
	sessions := make([]provider.Session, 0, len(threads))
	seen := map[string]bool{}
	for _, thread := range threads {
		if !seen[thread.ID] {
			seen[thread.ID] = true
			sessions = append(sessions, newSession(thread))
		}
	}
	return sessions, nil
}

func newSession(thread appserver.Thread) provider.Session {
	title := thread.Name
	if title == "" {
		title = thread.Preview
	}
	return provider.Session{
		Provider:  Provider,
		ID:        thread.ID,
		Title:     strings.Join(strings.Fields(title), " "),
		Workspace: thread.CWD,
		UpdatedAt: time.Unix(thread.UpdatedAt, 0).UTC(),
	}
}

// Session returns a local session by its full ID. Codex lists a fork only
// once it has turns of its own, so a session can exist without being listed.
func (s *Store) Session(id string) (provider.Session, error) {
	thread, err := s.thread(id)
	if err != nil {
		return provider.Session{}, err
	}
	return newSession(thread), nil
}

func (s *Store) thread(id string) (appserver.Thread, error) {
	client, err := s.appServer()
	if err != nil {
		return appserver.Thread{}, err
	}
	thread, err := client.Read(id)
	if err != nil {
		return appserver.Thread{}, fmt.Errorf("read Codex session %s: %w", id, err)
	}
	return thread, nil
}

// Export snapshots a completed session into an archive stamped with createdAt.
// A forked session carries the prefix of every ancestor rollout it extends. It
// fails while the session has an active or incomplete turn.
func (s *Store) Export(id string, createdAt time.Time) (archive.Archive, error) {
	thread, err := s.thread(id)
	if err != nil {
		return archive.Archive{}, err
	}
	if !isQuiescent(thread.Status.Type) {
		return archive.Archive{}, fmt.Errorf("session %s is not quiescent (status %q)", id, thread.Status.Type)
	}
	if thread.Path == "" {
		return archive.Archive{}, fmt.Errorf("Codex did not report a rollout path for session %s", id)
	}
	exported, err := s.layout.readSession(s.home, thread.Path)
	if err != nil {
		return archive.Archive{}, fmt.Errorf("snapshot session %s: %w", id, err)
	}
	meta := exported.head(exported.root).meta
	if meta.ID != id {
		return archive.Archive{}, fmt.Errorf("rollout session ID %s does not match selected session %s", meta.ID, id)
	}
	return archive.New(archive.Header{
		Provider:        Provider,
		ProviderVersion: s.version,
		SessionID:       meta.ID,
		Workspace:       meta.CWD,
		Home:            s.home,
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

// Diagnose reports the local Codex installation and whether App Server starts
// and lists threads.
func Diagnose(ctx context.Context) provider.Diagnosis {
	diagnosis := provider.Diagnosis{MinimumVersion: layouts[0].minimum, NewestTestedVersion: layouts[len(layouts)-1].newestTested}
	diagnosis.Home, diagnosis.Err = codexHome()
	if diagnosis.Err != nil {
		return diagnosis
	}
	binary := codexBinary()
	diagnosis.Version, diagnosis.Err = detectVersion(ctx, binary)
	if diagnosis.Err != nil {
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
	if err != nil {
		diagnosis.Err = fmt.Errorf("App Server: %w", err)
	}
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

// validateArchive checks a Codex archive and returns its layout and session.
func validateArchive(source archive.Archive) (layout, codexSession, error) {
	if source.Manifest.Provider != Provider {
		return layout{}, codexSession{}, fmt.Errorf("archive provider %q is not %s", source.Manifest.Provider, Provider)
	}
	sourceLayout, _, err := layoutFor(source.Manifest.ProviderVersion)
	if err != nil {
		return layout{}, codexSession{}, err
	}
	stored, err := sourceLayout.sessionFromArchive(source)
	if err != nil {
		return layout{}, codexSession{}, fmt.Errorf("validate session history: %w", err)
	}
	meta := stored.head(stored.root).meta
	if source.Manifest.SessionID != meta.ID {
		return layout{}, codexSession{}, fmt.Errorf("manifest session ID %s does not match rollout %s", source.Manifest.SessionID, meta.ID)
	}
	if source.Manifest.Workspace != meta.CWD {
		return layout{}, codexSession{}, fmt.Errorf("manifest workspace %s does not match rollout %s", source.Manifest.Workspace, meta.CWD)
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
	if errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("Codex is %w: %s was not found", provider.ErrNotInstalled, binary)
	}
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", binary, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[0] != "codex-cli" {
		return "", fmt.Errorf("unexpected Codex version output %q", strings.TrimSpace(string(output)))
	}
	return fields[1], nil
}
