// Package provider defines the provider-neutral values and operations that the
// command-line interface uses for every supported coding agent. Each adapter
// keeps its storage rules private and exposes them through an Adapter.
package provider

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/Riki1312/agent-teleport/internal/archive"
)

// ErrNotInstalled reports that a provider's command is not installed, so its
// sessions are skipped where every provider is consulted.
var ErrNotInstalled = errors.New("not installed")

// ErrDiverged reports that the local copy of a session and the incoming one
// have diverged, or that the local copy belongs to another workspace, so the
// fast-forward rule cannot update it.
var ErrDiverged = errors.New("diverged from the archive")

// Adapter describes one supported provider.
type Adapter struct {
	// ID identifies the provider in archive manifests, such as openai-codex.
	ID string
	// Name is the provider's product name, such as Codex.
	Name string
	// Command is the provider's command name, shown in session tables.
	Command string
	// Open detects the local installation and requires a supported version.
	Open func(ctx context.Context) (Store, error)
	// Diagnose reports the local installation without changing it.
	Diagnose func(ctx context.Context) Diagnosis
	// ValidateArchive checks that an archive of this provider is internally
	// consistent. It needs no local installation.
	ValidateArchive func(source archive.Archive) error
	// Relocate moves an archived session from the directory from, which
	// contains its workspace, to the directory to, and records home as the
	// provider's storage directory. Every path inside from moves, in every
	// record, and the rewrite is reversible. It needs no local installation.
	Relocate func(source archive.Archive, from, to, home string) (archive.Archive, error)
}

// Store is an opened local provider installation.
type Store interface {
	// Sessions lists local sessions, most recently updated first. An empty
	// workspace lists sessions from every workspace.
	Sessions(workspace string) ([]Session, error)
	// Session returns a local session by its full ID, including sessions the
	// provider does not list.
	Session(id string) (Session, error)
	// Export snapshots a session into an archive stamped with createdAt.
	Export(id string, createdAt time.Time) (archive.Archive, error)
	// Import applies the fast-forward update rule to the archived session.
	Import(request ImportRequest) (ImportResult, error)
	// ResumeCommand returns the provider command that continues sessionID in
	// workspace.
	ResumeCommand(sessionID, workspace string) *exec.Cmd
	// Close releases any process the store started.
	Close()
}

// Session summarizes a local session.
type Session struct {
	// Provider is the ID of the provider that owns the session.
	Provider string
	ID       string
	// Title is the session's name, or the provider's preview of its first
	// message.
	Title     string
	Workspace string
	UpdatedAt time.Time
}

// ImportRequest describes one archive import.
type ImportRequest struct {
	Archive archive.Archive
	// Workspace is the canonical absolute directory the session is mapped to.
	Workspace string
	// Replace allows overwriting a local copy that has diverged from the
	// archive, or that belongs to another workspace.
	Replace bool
	DryRun  bool
}

// ImportOutcome is what an import did, or would do in a dry run.
type ImportOutcome string

const (
	// Imported means the session was new and is now available.
	Imported ImportOutcome = "imported"
	// Updated means the archive extended the local copy, which now matches it.
	Updated ImportOutcome = "updated"
	// Unchanged means the local copy already matched the archive.
	Unchanged ImportOutcome = "unchanged"
	// KeptNewer means the local copy extends the archive and was kept.
	KeptNewer ImportOutcome = "kept-newer"
	// Replaced means a diverged local copy was replaced with the archive.
	Replaced ImportOutcome = "replaced"
)

// ImportResult reports the session an import produced.
type ImportResult struct {
	SessionID string
	Workspace string
	Outcome   ImportOutcome
}

// Diagnosis describes a local provider installation.
type Diagnosis struct {
	// Home is the directory where the provider stores sessions.
	Home    string
	Version string
	// Supported reports whether the version has a known storage layout, and
	// Tested whether it is within the range verified by the two-home test.
	Supported bool
	Tested    bool
	// MinimumVersion and NewestTestedVersion bound the verified range.
	MinimumVersion      string
	NewestTestedVersion string
	// Err reports why the installation could not be detected or used.
	Err error
}
