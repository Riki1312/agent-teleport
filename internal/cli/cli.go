// Package cli implements the command-line interface. It parses arguments,
// selects sessions, and formats output; provider adapters own every storage
// rule.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/buildinfo"
	"github.com/Riki1312/agent-teleport/internal/codex"
	"github.com/Riki1312/agent-teleport/internal/provider"
)

// adapters lists every supported provider in display order.
var adapters = []provider.Adapter{codex.Adapter}

// App runs commands against injectable standard streams and clock.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
}

// UsageError reports invalid command-line usage; the process exits with status 2.
type UsageError struct {
	Message string
}

func (e *UsageError) Error() string { return e.Message }

// New returns an App bound to the process streams and clock.
func New() *App {
	return &App{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Now: time.Now}
}

// Run executes the command named by args[0].
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &UsageError{Message: "missing command\n\n" + usage}
	}
	switch args[0] {
	case "help", "-h", "--help":
		_, err := fmt.Fprint(a.Stdout, usage)
		return err
	case "version", "--version":
		_, err := fmt.Fprintf(a.Stdout, "agent-teleport %s\n", buildinfo.Version())
		return err
	case "list":
		return a.list(ctx, args[1:])
	case "export":
		return a.export(ctx, args[1:])
	case "inspect":
		return a.inspect(ctx, args[1:])
	case "import":
		return a.importArchive(ctx, args[1:])
	case "doctor":
		return a.doctor(ctx, args[1:])
	default:
		return &UsageError{Message: fmt.Sprintf("unknown command %q\n\n%s", args[0], usage)}
	}
}

// errHelp reports that a command printed its help and should succeed.
var errHelp = errors.New("help requested")

// errNoMatch reports that no listed session matches a selector.
var errNoMatch = errors.New("no session matches")

// isSessionID reports whether selector has the shape of a full session ID,
// which export can look up even when a provider does not list the session.
func isSessionID(selector string) bool {
	return len(selector) == 36 && strings.Count(selector, "-") == 4
}

// sessionEntry is the stable JSON representation of a listed session.
type sessionEntry struct {
	Provider  string    `json:"provider"`
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Workspace string    `json:"workspace"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (a *App) list(ctx context.Context, args []string) error {
	set := newFlagSet("list")
	includeAll := set.Bool("all", false, "")
	asJSON := set.Bool("json", false, "")
	positional, err := a.parse(set, args, listUsage)
	if err != nil || len(positional) > 0 {
		return usageOrHelp(err, "list accepts no arguments")
	}
	stores, err := a.openStores(ctx)
	if err != nil {
		return err
	}
	defer stores.close()
	sessions, err := a.sessions(stores, *includeAll)
	if err != nil {
		return err
	}
	if *asJSON {
		entries := make([]sessionEntry, 0, len(sessions))
		for _, session := range sessions {
			entries = append(entries, sessionEntry{
				Provider:  session.Provider,
				ID:        session.ID,
				Title:     session.Title,
				Workspace: session.Workspace,
				UpdatedAt: session.UpdatedAt,
			})
		}
		return writeJSON(a.Stdout, entries)
	}
	if len(sessions) == 0 {
		_, err := fmt.Fprintln(a.Stderr, "no sessions found; use --all to include other directories")
		return err
	}
	writer := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	header := "UPDATED\tPROVIDER\tID\tTITLE"
	if *includeAll {
		header = "UPDATED\tPROVIDER\tID\tWORKSPACE\tTITLE"
	}
	if _, err := fmt.Fprintln(writer, header); err != nil {
		return err
	}
	now := a.Now()
	for _, session := range sessions {
		columns := []string{relativeTime(now, session.UpdatedAt), adapterFor(session.Provider).Command, shortID(session.ID)}
		if *includeAll {
			columns = append(columns, session.Workspace)
		}
		columns = append(columns, shorten(session.Title))
		if _, err := fmt.Fprintln(writer, strings.Join(columns, "\t")); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (a *App) export(ctx context.Context, args []string) error {
	set := newFlagSet("export")
	last := set.Bool("last", false, "")
	includeAll := set.Bool("all", false, "")
	output := set.String("o", "", "")
	set.StringVar(output, "output", "", "")
	positional, err := a.parse(set, args, exportUsage)
	if err != nil {
		return usageOrHelp(err, "")
	}
	if len(positional) > 1 {
		return &UsageError{Message: "export accepts at most one session selector"}
	}
	selector := strings.Join(positional, "")
	if selector == "" && !*last {
		return &UsageError{Message: "export needs a SESSION or --last\n\n" + exportUsage}
	}
	if selector != "" && *last {
		return &UsageError{Message: "SESSION and --last cannot be used together"}
	}

	stores, err := a.openStores(ctx)
	if err != nil {
		return err
	}
	defer stores.close()
	sessions, err := a.sessions(stores, *includeAll)
	if err != nil {
		return err
	}
	session, err := selectSession(sessions, selector, *last)
	if errors.Is(err, errNoMatch) && isSessionID(selector) {
		session, err = stores.session(selector)
	}
	if err != nil {
		return err
	}
	exported, err := stores[session.Provider].Export(session.ID, a.Now())
	if err != nil {
		return err
	}
	name := adapterFor(session.Provider).Name
	if *output == "-" {
		encoded, err := exported.Encode()
		if err != nil {
			return err
		}
		if _, err := a.Stdout.Write(encoded); err != nil {
			return fmt.Errorf("write archive to stdout: %w", err)
		}
		_, err = fmt.Fprintf(a.Stderr, "exported %s session %s\n", name, session.ID)
		return err
	}
	path := *output
	if path == "" {
		path = defaultArchiveName(session)
	}
	if err := exported.WriteFile(path); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.Stdout, "exported %s session %s to %s\n", name, session.ID, path)
	return err
}

// inspectReport is the JSON representation of inspect output.
type inspectReport struct {
	Manifest    archive.Manifest `json:"manifest"`
	Importable  bool             `json:"importable"`
	ImportError string           `json:"import_error,omitempty"`
}

func (a *App) inspect(ctx context.Context, args []string) error {
	set := newFlagSet("inspect")
	asJSON := set.Bool("json", false, "")
	positional, err := a.parse(set, args, inspectUsage)
	if err != nil || len(positional) != 1 {
		return usageOrHelp(err, "inspect requires exactly one archive path or -")
	}
	source, err := a.readArchive(positional[0])
	if err != nil {
		return err
	}
	adapter, err := archiveAdapter(source)
	if err != nil {
		return err
	}
	if err := adapter.ValidateArchive(source); err != nil {
		return err
	}
	report := inspectReport{Manifest: source.Manifest}
	if importErr := a.checkImport(ctx, adapter, source); importErr != nil {
		report.ImportError = importErr.Error()
	} else {
		report.Importable = true
	}
	if *asJSON {
		return writeJSON(a.Stdout, report)
	}
	importable := "yes"
	if !report.Importable {
		importable = "no: " + report.ImportError
	}
	_, err = fmt.Fprintf(a.Stdout, "session: %s\nprovider: %s %s\ncreated: %s\nworkspace: %s\nimportable here: %s\n",
		source.Manifest.SessionID,
		source.Manifest.Provider,
		source.Manifest.ProviderVersion,
		source.Manifest.CreatedAt.Format(time.RFC3339),
		source.Manifest.Workspace,
		importable,
	)
	return err
}

// checkImport dry-runs an import of source into the current directory.
func (a *App) checkImport(ctx context.Context, adapter provider.Adapter, source archive.Archive) error {
	workspace, err := currentWorkspace()
	if err != nil {
		return err
	}
	store, err := adapter.Open(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	_, err = store.Import(provider.ImportRequest{Archive: source, Workspace: workspace, DryRun: true})
	return err
}

func (a *App) importArchive(ctx context.Context, args []string) error {
	set := newFlagSet("import")
	cwd := set.String("cwd", "", "")
	replace := set.Bool("replace", false, "")
	dryRun := set.Bool("dry-run", false, "")
	resume := set.Bool("resume", false, "")
	positional, err := a.parse(set, args, importUsage)
	if err != nil || len(positional) != 1 {
		return usageOrHelp(err, "import requires exactly one archive path or -")
	}
	if *dryRun && *resume {
		return &UsageError{Message: "--dry-run and --resume cannot be used together"}
	}
	source, err := a.readArchive(positional[0])
	if err != nil {
		return err
	}
	workspace, err := targetWorkspace(*cwd)
	if err != nil {
		return fmt.Errorf("resolve target workspace: %w", err)
	}
	adapter, err := archiveAdapter(source)
	if err != nil {
		return err
	}
	store, err := adapter.Open(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.Import(provider.ImportRequest{Archive: source, Workspace: workspace, Replace: *replace, DryRun: *dryRun})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(a.Stdout, describeImport(adapter.Name, result, *dryRun)); err != nil {
		return err
	}
	if *dryRun {
		return nil
	}
	resumeCommand := store.ResumeCommand(result.SessionID, result.Workspace)
	if _, err := fmt.Fprintf(a.Stdout, "resume with: %s\n", displayCommand(resumeCommand)); err != nil {
		return err
	}
	if !*resume {
		return nil
	}
	resumeCommand.Stdin, resumeCommand.Stdout, resumeCommand.Stderr = a.Stdin, a.Stdout, a.Stderr
	if err := resumeCommand.Run(); err != nil {
		return fmt.Errorf("resume imported session: %w", err)
	}
	return nil
}

// describeImport returns the one-line summary of an import outcome.
func describeImport(providerName string, result provider.ImportResult, dryRun bool) string {
	formats := importSummaries
	if dryRun {
		formats = dryRunSummaries
	}
	return fmt.Sprintf(formats[result.Outcome], providerName, result.SessionID, result.Workspace)
}

var importSummaries = map[provider.ImportOutcome]string{
	provider.Imported:  "imported %s session %s into %s",
	provider.Updated:   "updated %s session %s in %s",
	provider.Replaced:  "replaced %s session %s in %s",
	provider.Unchanged: "%s session %s is already up to date in %s",
	provider.KeptNewer: "kept %s session %s in %s: the local copy is newer than the archive",
}

var dryRunSummaries = map[provider.ImportOutcome]string{
	provider.Imported:  "would import %s session %s into %s",
	provider.Updated:   "would update %s session %s in %s",
	provider.Replaced:  "would replace %s session %s in %s",
	provider.Unchanged: "%s session %s is already up to date in %s",
	provider.KeptNewer: "would keep %s session %s in %s: the local copy is newer than the archive",
}

// displayCommand formats a command the way a person would type it.
func displayCommand(command *exec.Cmd) string {
	arguments := slices.Clone(command.Args)
	arguments[0] = filepath.Base(arguments[0])
	return strings.Join(arguments, " ")
}

// doctorReport is the JSON representation of doctor output.
type doctorReport struct {
	Providers []providerReport `json:"providers"`
}

// providerReport describes one provider installation in doctor output.
type providerReport struct {
	Provider            string `json:"provider"`
	Name                string `json:"name"`
	Home                string `json:"home"`
	Version             string `json:"version,omitempty"`
	Supported           bool   `json:"supported"`
	Tested              bool   `json:"tested"`
	MinimumVersion      string `json:"minimum_version"`
	NewestTestedVersion string `json:"newest_tested_version"`
	Error               string `json:"error,omitempty"`
}

func (a *App) doctor(ctx context.Context, args []string) error {
	set := newFlagSet("doctor")
	asJSON := set.Bool("json", false, "")
	positional, err := a.parse(set, args, doctorUsage)
	if err != nil || len(positional) > 0 {
		return usageOrHelp(err, "doctor accepts no arguments")
	}
	report := doctorReport{Providers: make([]providerReport, 0, len(adapters))}
	for _, adapter := range adapters {
		diagnosis := adapter.Diagnose(ctx)
		entry := providerReport{
			Provider:            adapter.ID,
			Name:                adapter.Name,
			Home:                diagnosis.Home,
			Version:             diagnosis.Version,
			Supported:           diagnosis.Supported,
			Tested:              diagnosis.Tested,
			MinimumVersion:      diagnosis.MinimumVersion,
			NewestTestedVersion: diagnosis.NewestTestedVersion,
		}
		if diagnosis.Err != nil {
			entry.Error = diagnosis.Err.Error()
		}
		report.Providers = append(report.Providers, entry)
	}
	if *asJSON {
		return writeJSON(a.Stdout, report)
	}
	for index, entry := range report.Providers {
		if index > 0 {
			if _, err := fmt.Fprintln(a.Stdout); err != nil {
				return err
			}
		}
		version := entry.Version
		if version == "" {
			version = "not detected"
		}
		if _, err := fmt.Fprintf(a.Stdout, "%s\n  version: %s\n  home:    %s\n  status:  %s\n", entry.Name, version, entry.Home, doctorStatus(entry)); err != nil {
			return err
		}
	}
	return nil
}

func doctorStatus(report providerReport) string {
	switch {
	case report.Error != "":
		return "error: " + report.Error
	case !report.Supported:
		return fmt.Sprintf("unsupported; agent-teleport supports %s %s or newer", report.Name, report.MinimumVersion)
	case !report.Tested:
		return fmt.Sprintf("ready, but untested: the newest tested version is %s; unknown history layouts are still rejected", report.NewestTestedVersion)
	default:
		return "ready"
	}
}

func newFlagSet(name string) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	return set
}

// parse parses flags anywhere among the arguments and returns the positional
// arguments in order. It prints commandUsage and returns errHelp for -h.
func (a *App) parse(set *flag.FlagSet, args []string, commandUsage string) ([]string, error) {
	var positional []string
	for {
		if err := set.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				if _, writeErr := fmt.Fprint(a.Stdout, commandUsage); writeErr != nil {
					return nil, writeErr
				}
				return nil, errHelp
			}
			return nil, &UsageError{Message: fmt.Sprintf("%v\n%s", err, commandUsage)}
		}
		args = set.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// usageOrHelp turns a parse result into the command's return value: help
// succeeds, parse errors pass through, and otherwise message is a usage error.
func usageOrHelp(err error, message string) error {
	if errors.Is(err, errHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	return &UsageError{Message: message}
}

// stores holds the opened store of every installed provider, by provider ID.
type stores map[string]provider.Store

// openStores opens every installed provider. A provider that is installed but
// unusable is reported on stderr and skipped, so one broken installation does
// not hide the sessions of another.
func (a *App) openStores(ctx context.Context) (stores, error) {
	opened := stores{}
	var failures []error
	for _, adapter := range adapters {
		store, err := adapter.Open(ctx)
		if errors.Is(err, provider.ErrNotInstalled) {
			continue
		}
		if err != nil {
			failures = append(failures, err)
			if _, writeErr := fmt.Fprintf(a.Stderr, "agent-teleport: skipping %s: %v\n", adapter.Name, err); writeErr != nil {
				return nil, writeErr
			}
			continue
		}
		opened[adapter.ID] = store
	}
	if len(opened) == 0 {
		if len(failures) > 0 {
			return nil, errors.Join(failures...)
		}
		return nil, errors.New("no supported coding agent is installed; run agent-teleport doctor")
	}
	return opened, nil
}

func (s stores) close() {
	for _, store := range s {
		store.Close()
	}
}

// sessions lists the sessions of every store, most recently updated first.
func (a *App) sessions(opened stores, includeAll bool) ([]provider.Session, error) {
	workspace := ""
	if !includeAll {
		var err error
		if workspace, err = currentWorkspace(); err != nil {
			return nil, err
		}
	}
	var sessions []provider.Session
	for _, adapter := range adapters {
		store, ok := opened[adapter.ID]
		if !ok {
			continue
		}
		listed, err := store.Sessions(workspace)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, listed...)
	}
	slices.SortStableFunc(sessions, func(a, b provider.Session) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return sessions, nil
}

// session looks up a full session ID in every store.
func (s stores) session(id string) (provider.Session, error) {
	var failures []error
	for _, adapter := range adapters {
		store, ok := s[adapter.ID]
		if !ok {
			continue
		}
		session, err := store.Session(id)
		if err == nil {
			return session, nil
		}
		failures = append(failures, err)
	}
	return provider.Session{}, fmt.Errorf("%w %q: %w", errNoMatch, id, errors.Join(failures...))
}

func selectSession(sessions []provider.Session, selector string, last bool) (provider.Session, error) {
	if last {
		if len(sessions) == 0 {
			return provider.Session{}, errors.New("no sessions found")
		}
		return sessions[0], nil
	}
	var matches []provider.Session
	for _, session := range sessions {
		if strings.HasPrefix(session.ID, selector) || session.Title == selector {
			matches = append(matches, session)
		}
	}
	if len(matches) == 0 {
		return provider.Session{}, fmt.Errorf("%w %q", errNoMatch, selector)
	}
	if len(matches) > 1 {
		return provider.Session{}, fmt.Errorf("session selector %q is ambiguous", selector)
	}
	return matches[0], nil
}

// adapterFor returns the adapter of a provider ID reported by a store.
func adapterFor(id string) provider.Adapter {
	for _, adapter := range adapters {
		if adapter.ID == id {
			return adapter
		}
	}
	panic("unknown provider " + id)
}

// archiveAdapter returns the adapter of the provider that wrote source.
func archiveAdapter(source archive.Archive) (provider.Adapter, error) {
	for _, adapter := range adapters {
		if adapter.ID == source.Manifest.Provider {
			return adapter, nil
		}
	}
	return provider.Adapter{}, fmt.Errorf("archive provider %q is not supported", source.Manifest.Provider)
}

// shortID returns the leading part of a session ID, which export accepts as a
// selector while it is unique.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// relativeTime describes t relative to now for human-readable tables.
func relativeTime(now, t time.Time) string {
	elapsed := now.Sub(t)
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return plural(int(elapsed/time.Minute), "minute") + " ago"
	case elapsed < 24*time.Hour:
		return plural(int(elapsed/time.Hour), "hour") + " ago"
	case elapsed < 30*24*time.Hour:
		return plural(int(elapsed/(24*time.Hour)), "day") + " ago"
	default:
		return t.Local().Format(time.DateOnly)
	}
}

func plural(count int, unit string) string {
	if count == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", count, unit)
}

// shorten limits a title to one table cell.
func shorten(title string) string {
	runes := []rune(title)
	if len(runes) > 72 {
		return string(runes[:69]) + "..."
	}
	return title
}

func defaultArchiveName(session provider.Session) string {
	var name strings.Builder
	lastWasDash := false
	for _, character := range strings.ToLower(session.Title) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			name.WriteRune(character)
			lastWasDash = false
		} else if name.Len() > 0 && !lastWasDash {
			name.WriteByte('-')
			lastWasDash = true
		}
		if name.Len() >= 48 {
			break
		}
	}
	base := strings.Trim(name.String(), "-")
	if base == "" {
		base = "session"
	}
	return fmt.Sprintf("%s-%s%s", base, shortID(session.ID), archive.Extension)
}

// readArchive decodes an archive from path, or from stdin when path is "-".
// Decoding validates the whole archive in memory before any import starts.
func (a *App) readArchive(path string) (archive.Archive, error) {
	if path == "-" {
		decoded, err := archive.Decode(a.Stdin)
		if err != nil {
			return archive.Archive{}, fmt.Errorf("read archive from stdin: %w", err)
		}
		return decoded, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return archive.Archive{}, fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoded, err := archive.Decode(file)
	if err != nil {
		return archive.Archive{}, fmt.Errorf("read archive: %w", err)
	}
	return decoded, nil
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
