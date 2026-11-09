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
		return &UsageError{Message: usage}
	}
	switch args[0] {
	case "help", "-h", "--help":
		_, err := fmt.Fprint(a.Stdout, usage)
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

const usage = `Usage: cxport <command> [options]

Commands:
  list       List Codex sessions
  export     Export one session to a .cxport archive
  inspect    Validate and describe an archive
  import     Import an archive into the local Codex store
  doctor     Check local Codex compatibility

Run "cxport <command> --help" for command options.
`

const (
	listUsage    = "Usage: cxport list [--all] [--json]\n"
	exportUsage  = "Usage: cxport export [SESSION] [--all] [--last] [-o PATH|-]\n"
	inspectUsage = "Usage: cxport inspect <ARCHIVE|-> [--json]\n"
	importUsage  = "Usage: cxport import <ARCHIVE|-> [--cwd PATH | --preserve-cwd] [--if-exists error|skip|replace] [--dry-run] [--resume]\n"
	doctorUsage  = "Usage: cxport doctor [--json]\n"
)

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
	writer := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "UPDATED\tID\tWORKSPACE\tTITLE"); err != nil {
		return err
	}
	for _, session := range sessions {
		updated := session.UpdatedAt.Format(time.RFC3339)
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", updated, session.ID, session.Workspace, shorten(session.Title)); err != nil {
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
		return &UsageError{Message: "export requires SESSION or --last (interactive picker is not implemented yet)"}
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
		_, err = fmt.Fprintf(a.Stderr, "exported %s\n", session.ID)
		return err
	}
	path := *output
	if path == "" {
		path = defaultArchiveName(session)
	}
	if err := exported.WriteFile(path); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.Stdout, "exported %s to %s\n", session.ID, path)
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
	_, err = store.Import(codex.ImportRequest{Archive: source, Workspace: workspace, IfExists: codex.CollisionError, DryRun: true})
	return err
}

func (a *App) importArchive(ctx context.Context, args []string) error {
	set := newFlagSet("import")
	cwd := set.String("cwd", "", "")
	preserveCWD := set.Bool("preserve-cwd", false, "")
	ifExists := set.String("if-exists", string(codex.CollisionError), "")
	dryRun := set.Bool("dry-run", false, "")
	resume := set.Bool("resume", false, "")
	positional, err := a.parse(set, args, importUsage)
	if err != nil || len(positional) != 1 {
		return usageOrHelp(err, "import requires exactly one archive path or -")
	}
	policy := codex.CollisionPolicy(*ifExists)
	switch {
	case *cwd != "" && *preserveCWD:
		return &UsageError{Message: "--cwd and --preserve-cwd cannot be used together"}
	case policy != codex.CollisionError && policy != codex.CollisionSkip && policy != codex.CollisionReplace:
		return &UsageError{Message: "--if-exists must be error, skip, or replace"}
	case *dryRun && *resume:
		return &UsageError{Message: "--dry-run and --resume cannot be used together"}
	}

	source, err := a.readArchive(positional[0])
	if err != nil {
		return err
	}
	workspace := source.Manifest.Workspace
	if !*preserveCWD {
		workspace, err = targetWorkspace(*cwd)
		if err != nil {
			return fmt.Errorf("resolve target workspace: %w", err)
		}
	}
	store, err := codex.Open(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.Import(codex.ImportRequest{Archive: source, Workspace: workspace, IfExists: policy, DryRun: *dryRun})
	if err != nil {
		return err
	}
	if *dryRun {
		_, err = fmt.Fprintf(a.Stdout, "would %s %s into %s\n", dryRunVerb(result.Outcome), result.SessionID, result.Workspace)
		return err
	}
	if result.Outcome == codex.Skipped {
		_, err = fmt.Fprintf(a.Stdout, "session %s already exists; skipped\n", result.SessionID)
		return err
	}
	if _, err := fmt.Fprintf(a.Stdout, "%s %s into %s\nresume with: codex resume %s\n", result.Outcome, result.SessionID, result.Workspace, result.SessionID); err != nil {
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

func dryRunVerb(outcome codex.ImportOutcome) string {
	switch outcome {
	case codex.Replaced:
		return "replace"
	case codex.Skipped:
		return "skip"
	default:
		return "import"
	}
}

// doctorReport is the JSON representation of doctor output.
type doctorReport struct {
	CodexHome         string   `json:"codex_home"`
	CodexVersion      string   `json:"codex_version,omitempty"`
	Supported         bool     `json:"supported"`
	SupportedVersions []string `json:"supported_versions"`
	AppServer         bool     `json:"app_server"`
	Error             string   `json:"error,omitempty"`
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
		CodexHome:         diagnosis.Home,
		CodexVersion:      diagnosis.Version,
		Supported:         diagnosis.Supported,
		SupportedVersions: diagnosis.SupportedVersions,
		AppServer:         diagnosis.DetectionError == nil && diagnosis.AppServerError == nil,
	}
	if diagnosis.DetectionError != nil {
		report.Error = diagnosis.DetectionError.Error()
	} else if diagnosis.AppServerError != nil {
		report.Error = diagnosis.AppServerError.Error()
	}
	if *asJSON {
		return writeJSON(a.Stdout, report)
	}
	_, err = fmt.Fprintf(a.Stdout, "Codex home: %s\nCodex version: %s\nSupported: %t (supported versions: %s)\nApp Server: %t\n",
		report.CodexHome, report.CodexVersion, report.Supported, strings.Join(report.SupportedVersions, ", "), report.AppServer)
	if err == nil && report.Error != "" {
		_, err = fmt.Fprintf(a.Stdout, "Error: %s\n", report.Error)
	}
	return err
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
	shortID := session.ID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	return fmt.Sprintf("%s-%s%s", base, shortID, archive.Extension)
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
	defer file.Close()
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
