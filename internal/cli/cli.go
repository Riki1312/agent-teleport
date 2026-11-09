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
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/buildinfo"
	"github.com/Riki1312/agent-teleport/internal/codex"
)

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
	store, err := codex.Open(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	sessions, err := a.sessions(store, *includeAll)
	if err != nil {
		return err
	}
	if *asJSON {
		entries := make([]sessionEntry, 0, len(sessions))
		for _, session := range sessions {
			entries = append(entries, sessionEntry{
				Provider:  codex.Provider,
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
		columns := []string{relativeTime(now, session.UpdatedAt), providerName, shortID(session.ID)}
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

	store, err := codex.Open(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	sessions, err := a.sessions(store, *includeAll)
	if err != nil {
		return err
	}
	session, err := selectSession(sessions, selector, *last)
	if err != nil {
		return err
	}
	exported, err := store.Export(session, a.Now())
	if err != nil {
		return err
	}
	if *output == "-" {
		encoded, err := exported.Encode()
		if err != nil {
			return err
		}
		if _, err := a.Stdout.Write(encoded); err != nil {
			return fmt.Errorf("write archive to stdout: %w", err)
		}
		_, err = fmt.Fprintf(a.Stderr, "exported Codex session %s\n", session.ID)
		return err
	}
	path := *output
	if path == "" {
		path = defaultArchiveName(session)
	}
	if err := exported.WriteFile(path); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.Stdout, "exported Codex session %s to %s\n", session.ID, path)
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
	if err := codex.ValidateArchive(source); err != nil {
		return err
	}
	report := inspectReport{Manifest: source.Manifest}
	if importErr := a.checkImport(ctx, source); importErr != nil {
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
func (a *App) checkImport(ctx context.Context, source archive.Archive) error {
	workspace, err := currentWorkspace()
	if err != nil {
		return err
	}
	store, err := codex.Open(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	_, err = store.Import(codex.ImportRequest{Archive: source, Workspace: workspace, DryRun: true})
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
	store, err := codex.Open(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.Import(codex.ImportRequest{Archive: source, Workspace: workspace, Replace: *replace, DryRun: *dryRun})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(a.Stdout, describeImport(result, *dryRun)); err != nil {
		return err
	}
	if *dryRun {
		return nil
	}
	if _, err := fmt.Fprintf(a.Stdout, "resume with: codex resume %s\n", result.SessionID); err != nil {
		return err
	}
	if !*resume {
		return nil
	}
	resumeCommand := store.ResumeCommand(result.SessionID, result.Workspace)
	resumeCommand.Stdin, resumeCommand.Stdout, resumeCommand.Stderr = a.Stdin, a.Stdout, a.Stderr
	if err := resumeCommand.Run(); err != nil {
		return fmt.Errorf("resume imported session: %w", err)
	}
	return nil
}

// describeImport returns the one-line summary of an import outcome.
func describeImport(result codex.ImportResult, dryRun bool) string {
	formats := importSummaries
	if dryRun {
		formats = dryRunSummaries
	}
	return fmt.Sprintf(formats[result.Outcome], result.SessionID, result.Workspace)
}

var importSummaries = map[codex.ImportOutcome]string{
	codex.Imported:  "imported Codex session %s into %s",
	codex.Updated:   "updated Codex session %s in %s",
	codex.Replaced:  "replaced Codex session %s in %s",
	codex.Unchanged: "Codex session %s is already up to date in %s",
	codex.KeptNewer: "kept Codex session %s in %s: the local copy is newer than the archive",
}

var dryRunSummaries = map[codex.ImportOutcome]string{
	codex.Imported:  "would import Codex session %s into %s",
	codex.Updated:   "would update Codex session %s in %s",
	codex.Replaced:  "would replace Codex session %s in %s",
	codex.Unchanged: "Codex session %s is already up to date in %s",
	codex.KeptNewer: "would keep Codex session %s in %s: the local copy is newer than the archive",
}

// doctorReport is the JSON representation of doctor output.
type doctorReport struct {
	CodexHome           string `json:"codex_home"`
	CodexVersion        string `json:"codex_version,omitempty"`
	Supported           bool   `json:"supported"`
	Tested              bool   `json:"tested"`
	MinimumVersion      string `json:"minimum_version"`
	NewestTestedVersion string `json:"newest_tested_version"`
	AppServer           bool   `json:"app_server"`
	Error               string `json:"error,omitempty"`
}

func (a *App) doctor(ctx context.Context, args []string) error {
	set := newFlagSet("doctor")
	asJSON := set.Bool("json", false, "")
	positional, err := a.parse(set, args, doctorUsage)
	if err != nil || len(positional) > 0 {
		return usageOrHelp(err, "doctor accepts no arguments")
	}
	diagnosis := codex.Diagnose(ctx)
	report := doctorReport{
		CodexHome:           diagnosis.Home,
		CodexVersion:        diagnosis.Version,
		Supported:           diagnosis.Supported,
		Tested:              diagnosis.Tested,
		MinimumVersion:      diagnosis.MinimumVersion,
		NewestTestedVersion: diagnosis.NewestTestedVersion,
		AppServer:           diagnosis.DetectionError == nil && diagnosis.AppServerError == nil,
	}
	if diagnosis.DetectionError != nil {
		report.Error = diagnosis.DetectionError.Error()
	} else if diagnosis.AppServerError != nil {
		report.Error = diagnosis.AppServerError.Error()
	}
	if *asJSON {
		return writeJSON(a.Stdout, report)
	}
	version := report.CodexVersion
	if version == "" {
		version = "not detected"
	}
	_, err = fmt.Fprintf(a.Stdout, "Codex\n  version: %s\n  home:    %s\n  status:  %s\n", version, report.CodexHome, doctorStatus(report))
	return err
}

func doctorStatus(report doctorReport) string {
	switch {
	case report.Error != "":
		return "error: " + report.Error
	case !report.Supported:
		return fmt.Sprintf("unsupported; agent-teleport supports Codex %s or newer", report.MinimumVersion)
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

func (a *App) sessions(store *codex.Store, includeAll bool) ([]codex.Session, error) {
	workspace := ""
	if !includeAll {
		var err error
		if workspace, err = currentWorkspace(); err != nil {
			return nil, err
		}
	}
	return store.Sessions(workspace)
}

func selectSession(sessions []codex.Session, selector string, last bool) (codex.Session, error) {
	if last {
		if len(sessions) == 0 {
			return codex.Session{}, errors.New("no sessions found")
		}
		return sessions[0], nil
	}
	var matches []codex.Session
	for _, session := range sessions {
		if strings.HasPrefix(session.ID, selector) || session.Title == selector {
			matches = append(matches, session)
		}
	}
	if len(matches) == 0 {
		return codex.Session{}, fmt.Errorf("no session matches %q", selector)
	}
	if len(matches) > 1 {
		return codex.Session{}, fmt.Errorf("session selector %q is ambiguous", selector)
	}
	return matches[0], nil
}

// providerName is the short provider name shown in tables.
const providerName = "codex"

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

func defaultArchiveName(session codex.Session) string {
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
