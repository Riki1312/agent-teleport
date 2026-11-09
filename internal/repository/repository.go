// Package repository keeps sessions in a Git repository, next to the code
// they discuss.
//
// A saved session is an unpacked archive under .agent-teleport/sessions/<name>/
// whose paths are relative to the repository: the repository root is recorded
// as Workspace and the provider's storage directory as Home, two placeholder
// directories that open maps to the local checkout and agent.
package repository

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/fsutil"
)

// Workspace and Home are the placeholder directories that saved sessions
// record for the repository root and for the provider's storage directory.
const (
	Workspace = "/agent-teleport/repository"
	Home      = "/agent-teleport/home"
)

// Directory is the repository-relative directory that holds saved sessions.
const Directory = ".agent-teleport"

// attributes keeps saved sessions byte for byte in every checkout, and marks
// them as generated so that code review collapses their diffs.
const attributes = "# Sessions saved by agent-teleport: stored byte for byte, collapsed in code review.\nsessions/** -text linguist-generated\n"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Repository is a Git working tree.
type Repository struct {
	root string
}

// Saved is one saved session. Err reports why a saved session cannot be read,
// in which case Archive is empty.
type Saved struct {
	Name    string
	Archive archive.Archive
	Err     error
}

// Find returns the repository whose working tree contains dir.
func Find(ctx context.Context, dir string) (Repository, error) {
	output, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, fmt.Errorf("find the Git repository of %s: %w", dir, err)
	}
	root, err := filepath.EvalSymlinks(output)
	if err != nil {
		return Repository{}, err
	}
	return Repository{root: root}, nil
}

// Root returns the canonical root of the working tree.
func (r Repository) Root() string {
	return r.root
}

// State returns the checked-out commit and branch.
func (r Repository) State(ctx context.Context) archive.Repository {
	commit, _ := git(ctx, r.root, "rev-parse", "--verify", "--quiet", "HEAD")
	branch, _ := git(ctx, r.root, "symbolic-ref", "--quiet", "--short", "HEAD")
	return archive.Repository{Commit: commit, Branch: branch}
}

// ValidateName requires a saved session name to be short, lowercase, and safe
// as a directory name.
func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid session name %q: use up to 64 lowercase letters, digits, dots, dashes, and underscores, starting with a letter or digit", name)
	}
	return nil
}

// Sessions returns every saved session, by name.
func (r Repository) Sessions() ([]Saved, error) {
	entries, err := os.ReadDir(r.sessionsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var saved []Saved
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		session, err := r.Load(entry.Name())
		saved = append(saved, Saved{Name: entry.Name(), Archive: session, Err: err})
	}
	slices.SortFunc(saved, func(a, b Saved) int { return strings.Compare(a.Name, b.Name) })
	return saved, nil
}

// Load reads the saved session name, completing an interrupted save first.
func (r Repository) Load(name string) (archive.Archive, error) {
	if err := ValidateName(name); err != nil {
		return archive.Archive{}, err
	}
	dir := filepath.Join(r.sessionsDir(), name)
	if err := archive.RecoverDir(dir); err != nil {
		return archive.Archive{}, err
	}
	saved, err := archive.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return archive.Archive{}, fmt.Errorf("no session is saved as %q in this repository", name)
	}
	if err != nil {
		return archive.Archive{}, fmt.Errorf("read saved session %s: %w", name, err)
	}
	if err := requireRelative(saved); err != nil {
		return archive.Archive{}, fmt.Errorf("saved session %s: %w", name, err)
	}
	return saved, nil
}

// requireRelative requires session to record the placeholder directories.
func requireRelative(session archive.Archive) error {
	if session.Manifest.Home != Home || !isWithin(session.Manifest.Workspace, Workspace) {
		return errors.New("the session does not record repository-relative paths")
	}
	return nil
}

// Store saves session as name, replacing an earlier save, and creates the
// attributes file of the sessions directory on first use.
func (r Repository) Store(name string, session archive.Archive) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := requireRelative(session); err != nil {
		return err
	}
	if err := os.MkdirAll(r.sessionsDir(), 0o755); err != nil {
		return err
	}
	attributesPath := filepath.Join(r.root, Directory, ".gitattributes")
	if _, err := os.Stat(attributesPath); errors.Is(err, fs.ErrNotExist) {
		if err := fsutil.WriteAtomic(attributesPath, []byte(attributes)); err != nil {
			return err
		}
	}
	return session.WriteDir(filepath.Join(r.sessionsDir(), name))
}

// Path returns the repository-relative path of the saved session name.
func Path(name string) string {
	return filepath.ToSlash(filepath.Join(Directory, "sessions", name))
}

func (r Repository) sessionsDir() string {
	return filepath.Join(r.root, Directory, "sessions")
}

// isWithin reports whether path is dir or inside it.
func isWithin(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func git(ctx context.Context, dir string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = dir
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return "", fmt.Errorf("%w: %s", err, message)
		}
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
