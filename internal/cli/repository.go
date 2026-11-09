package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/provider"
	"github.com/Riki1312/agent-teleport/internal/repository"
	"github.com/Riki1312/agent-teleport/internal/secrets"
)

// syncState relates a saved session to the local copy of the same session.
type syncState string

const (
	inSync          syncState = "in-sync"
	localAhead      syncState = "local-ahead"
	repositoryAhead syncState = "repository-ahead"
	diverged        syncState = "diverged"
	notOpened       syncState = "not-opened"
	// unavailable means the session's agent is not installed or not usable.
	unavailable syncState = "unavailable"
	// unreadable means the saved session or the comparison failed.
	unreadable syncState = "error"
)

var syncStates = map[provider.ImportOutcome]syncState{
	provider.Unchanged: inSync,
	provider.KeptNewer: localAhead,
	provider.Updated:   repositoryAhead,
	provider.Imported:  notOpened,
}

func (a *App) save(ctx context.Context, args []string) error {
	set := newFlagSet("save")
	last := set.Bool("last", false, "")
	includeAll := set.Bool("all", false, "")
	name := set.String("name", "", "")
	allowSecrets := set.Bool("allow-secrets", false, "")
	positional, err := a.parse(set, args, saveUsage)
	if err != nil {
		return usageOrHelp(err, "")
	}
	selector, err := a.sessionSelector(positional, *last)
	if err != nil {
		return err
	}
	if *name != "" {
		if err := repository.ValidateName(*name); err != nil {
			return &UsageError{Message: err.Error()}
		}
	}
	repo, err := currentRepository(ctx)
	if err != nil {
		return err
	}
	stores, err := a.openStores(ctx)
	if err != nil {
		return err
	}
	defer stores.close()
	session, err := a.selectStoredSession(stores, selector, *last, *includeAll)
	if err != nil {
		return err
	}
	if !isWithin(session.Workspace, repo.Root()) {
		return fmt.Errorf("session %s belongs to %s, which is outside the repository at %s", session.ID, session.Workspace, repo.Root())
	}
	adapter, store := adapterFor(session.Provider), stores[session.Provider]
	saved, err := repositorySession(ctx, adapter, store, repo, session.ID, a.Now)
	if err != nil {
		return err
	}
	existing, err := repo.Sessions()
	if err != nil {
		return err
	}
	target, previous, err := saveTarget(existing, saved, *name, session.Title)
	if err != nil {
		return err
	}
	if previous != nil {
		state, err := compareSaved(adapter, store, repo, *previous)
		switch {
		case errors.Is(err, provider.ErrDiverged):
			return fmt.Errorf("the local copy of %s has diverged from the one saved as %s: run agent-teleport open %s --replace to take the saved copy, or fork the session in %s and save the fork under another --name", session.ID, target, target, adapter.Name)
		case err != nil:
			return err
		case state == inSync:
			_, err := fmt.Fprintf(a.Stdout, "%s session %s is already saved as %s\n", adapter.Name, session.ID, repository.Path(target))
			return err
		case state == repositoryAhead:
			return fmt.Errorf("the session saved as %s is newer than the local copy; run agent-teleport open %s first", target, target)
		}
	}
	findings := scanSecrets(saved, previous)
	if err := a.reportSecrets(target, findings, *allowSecrets); err != nil {
		return err
	}
	if err := repo.Store(target, saved); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	_, err = fmt.Fprintf(a.Stdout, "saved %s session %s as %s\ncommit it with: git add %s\n", adapter.Name, session.ID, repository.Path(target), repository.Directory)
	return err
}

// repositorySession exports a session and moves it to the repository's
// placeholder paths, recording the checked-out commit and branch.
func repositorySession(ctx context.Context, adapter provider.Adapter, store provider.Store, repo repository.Repository, id string, now func() time.Time) (archive.Archive, error) {
	exported, err := store.Export(id, now())
	if err != nil {
		return archive.Archive{}, err
	}
	moved, err := adapter.Relocate(exported, repo.Root(), repository.Workspace, repository.Home)
	if err != nil {
		return archive.Archive{}, fmt.Errorf("make session paths relative to the repository: %w", err)
	}
	header := moved.Manifest.Header()
	state := repo.State(ctx)
	header.Repository = &state
	return archive.New(header, moved.Payloads)
}

// saveTarget chooses the name to save session under: the name it is already
// saved as, else the requested name, else one derived from its title. It
// returns the earlier save of the session, if any.
func saveTarget(existing []repository.Saved, session archive.Archive, requested, title string) (string, *archive.Archive, error) {
	taken := map[string]bool{}
	for _, saved := range existing {
		taken[saved.Name] = true
		if saved.Err != nil || saved.Archive.Manifest.SessionID != session.Manifest.SessionID {
			continue
		}
		if requested != "" && requested != saved.Name {
			return "", nil, fmt.Errorf("session %s is already saved as %s", session.Manifest.SessionID, saved.Name)
		}
		return saved.Name, &saved.Archive, nil
	}
	if requested != "" {
		if taken[requested] {
			return "", nil, fmt.Errorf("%s already holds another session; choose another --name", repository.Path(requested))
		}
		return requested, nil, nil
	}
	name := sessionName(title)
	if taken[name] {
		name += "-" + shortID(session.Manifest.SessionID)
	}
	if taken[name] {
		return "", nil, errors.New("choose a name for the session with --name")
	}
	return name, nil, nil
}

// sessionName derives a valid saved session name from a title.
func sessionName(title string) string {
	var name strings.Builder
	for _, character := range strings.ToLower(title) {
		switch {
		case character >= 'a' && character <= 'z' || character >= '0' && character <= '9':
			name.WriteRune(character)
		case name.Len() > 0 && !strings.HasSuffix(name.String(), "-"):
			name.WriteByte('-')
		}
		if name.Len() >= 48 {
			break
		}
	}
	if base := strings.Trim(name.String(), "-"); base != "" {
		return base
	}
	return "session"
}

// scanSecrets scans what saving adds to the repository: every file of saved,
// or, for a file that only grew since the previous save, its new lines.
func scanSecrets(saved archive.Archive, previous *archive.Archive) []secrets.Finding {
	var findings []secrets.Finding
	for _, payload := range saved.Payloads {
		from := 0
		if previous != nil {
			if old, ok := previous.Data(payload.Path); ok && bytes.HasPrefix(payload.Data, old) {
				from = bytes.LastIndexByte(old, '\n') + 1
			}
		}
		findings = append(findings, secrets.Scan(payload.Path, payload.Data, from)...)
	}
	return findings
}

// reportSecrets lists findings on stderr, and refuses them unless allowed.
func (a *App) reportSecrets(name string, findings []secrets.Finding, allowed bool) error {
	for _, finding := range findings {
		location := filepath.ToSlash(filepath.Join(repository.Path(name), finding.Path))
		if _, err := fmt.Fprintf(a.Stderr, "agent-teleport: possible %s in %s line %d\n", finding.Rule, location, finding.Line); err != nil {
			return err
		}
	}
	if len(findings) == 0 || allowed {
		return nil
	}
	return fmt.Errorf("refusing to save %d possible secrets; committing them publishes them to everyone with access to the repository. Rotate any real credential, then pass --allow-secrets to save anyway", len(findings))
}

// compareSaved relates a saved session to the local copy, through a dry-run
// import of the saved session into its place in this checkout.
func compareSaved(adapter provider.Adapter, store provider.Store, repo repository.Repository, saved archive.Archive) (syncState, error) {
	local, err := adapter.Relocate(saved, repository.Workspace, repo.Root(), repository.Home)
	if err != nil {
		return unreadable, err
	}
	result, err := store.Import(provider.ImportRequest{Archive: local, Workspace: local.Manifest.Workspace, DryRun: true})
	if errors.Is(err, provider.ErrDiverged) {
		return diverged, err
	}
	if err != nil {
		return unreadable, err
	}
	return syncStates[result.Outcome], nil
}

func (a *App) open(ctx context.Context, args []string) error {
	set := newFlagSet("open")
	replace := set.Bool("replace", false, "")
	dryRun := set.Bool("dry-run", false, "")
	resume := set.Bool("resume", false, "")
	positional, err := a.parse(set, args, openUsage)
	if err != nil || len(positional) > 1 || (len(positional) == 0 && !a.Interactive) {
		return usageOrHelp(err, "open requires one saved session name")
	}
	if err := resumeAllowed(*dryRun, *resume); err != nil {
		return err
	}
	repo, err := currentRepository(ctx)
	if err != nil {
		return err
	}
	name := ""
	if len(positional) == 1 {
		name = positional[0]
	} else if name, err = a.chooseSaved(repo); err != nil {
		return err
	}
	saved, err := repo.Load(name)
	if err != nil {
		return err
	}
	adapter, err := archiveAdapter(saved)
	if err != nil {
		return err
	}
	local, err := adapter.Relocate(saved, repository.Workspace, repo.Root(), repository.Home)
	if err != nil {
		return err
	}
	store, err := adapter.Open(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	return a.importInto(adapter, store, provider.ImportRequest{Archive: local, Workspace: local.Manifest.Workspace, Replace: *replace, DryRun: *dryRun}, *resume)
}

// chooseSaved asks the person at the terminal to choose a saved session.
func (a *App) chooseSaved(repo repository.Repository) (string, error) {
	saved, err := repo.Sessions()
	if err != nil {
		return "", err
	}
	if len(saved) == 0 {
		return "", errors.New("no sessions are saved in this repository")
	}
	var rows strings.Builder
	writer := tabwriter.NewWriter(&rows, 0, 4, 2, ' ', 0)
	for index, session := range saved {
		description := "unreadable"
		if session.Err == nil {
			if adapter, err := archiveAdapter(session.Archive); err == nil {
				description = adapter.Command + "\t" + shortID(session.Archive.Manifest.SessionID)
			}
		}
		_, _ = fmt.Fprintf(writer, "%3d\t%s\t%s\n", index+1, session.Name, description)
	}
	_ = writer.Flush()
	choice, err := a.choose("Saved session", strings.Split(strings.TrimSuffix(rows.String(), "\n"), "\n"))
	if err != nil {
		return "", err
	}
	return saved[choice].Name, nil
}

// statusEntry is the JSON representation of one saved session in status
// output.
type statusEntry struct {
	Name      string    `json:"name"`
	Provider  string    `json:"provider,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	State     syncState `json:"state"`
	Commit    string    `json:"commit,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func (a *App) status(ctx context.Context, args []string) error {
	set := newFlagSet("status")
	asJSON := set.Bool("json", false, "")
	positional, err := a.parse(set, args, statusUsage)
	if err != nil || len(positional) > 0 {
		return usageOrHelp(err, "status accepts no arguments")
	}
	repo, err := currentRepository(ctx)
	if err != nil {
		return err
	}
	saved, err := repo.Sessions()
	if err != nil {
		return err
	}
	opened := stores{}
	defer opened.close()
	entries := make([]statusEntry, 0, len(saved))
	for _, session := range saved {
		entries = append(entries, a.statusOf(ctx, repo, session, opened))
	}
	if *asJSON {
		return writeJSON(a.Stdout, entries)
	}
	if len(entries) == 0 {
		_, err := fmt.Fprintln(a.Stderr, "no sessions are saved in this repository; save one with agent-teleport save")
		return err
	}
	writer := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tPROVIDER\tID\tSTATE"); err != nil {
		return err
	}
	for _, entry := range entries {
		command := ""
		if entry.Provider != "" {
			command = adapterFor(entry.Provider).Command
		}
		state := strings.ReplaceAll(string(entry.State), "-", " ")
		if entry.Error != "" {
			state += ": " + entry.Error
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", entry.Name, command, shortID(entry.SessionID), state); err != nil {
			return err
		}
	}
	return writer.Flush()
}

// statusOf compares one saved session with its local copy, opening the
// session's provider on first use.
func (a *App) statusOf(ctx context.Context, repo repository.Repository, saved repository.Saved, opened stores) statusEntry {
	entry := statusEntry{Name: saved.Name, State: unreadable}
	if saved.Err != nil {
		entry.Error = saved.Err.Error()
		return entry
	}
	manifest := saved.Archive.Manifest
	entry.SessionID = manifest.SessionID
	if manifest.Repository != nil {
		entry.Commit, entry.Branch = manifest.Repository.Commit, manifest.Repository.Branch
	}
	adapter, err := archiveAdapter(saved.Archive)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	entry.Provider = adapter.ID
	store, ok := opened[adapter.ID]
	if !ok {
		if store, err = adapter.Open(ctx); err != nil {
			entry.State, entry.Error = unavailable, err.Error()
			return entry
		}
		opened[adapter.ID] = store
	}
	state, err := compareSaved(adapter, store, repo, saved.Archive)
	entry.State = state
	if err != nil && state != diverged {
		entry.Error = err.Error()
	}
	return entry
}

// currentRepository returns the Git repository of the current directory.
func currentRepository(ctx context.Context) (repository.Repository, error) {
	workspace, err := currentWorkspace()
	if err != nil {
		return repository.Repository{}, err
	}
	return repository.Find(ctx, workspace)
}

// isWithin reports whether path is dir or inside it.
func isWithin(path, dir string) bool {
	relative, err := filepath.Rel(dir, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
