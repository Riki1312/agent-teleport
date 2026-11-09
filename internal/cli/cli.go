package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/Riki1312/agent-teleport/internal/appserver"
	"github.com/Riki1312/agent-teleport/internal/bundle"
	"github.com/Riki1312/agent-teleport/internal/codex"
)

type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
}

const maxInputArchive = 513 << 20

type UsageError struct {
	Message string
}

func (e *UsageError) Error() string { return e.Message }

func New() *App {
	return &App{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Now:    time.Now,
	}
}

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
		return a.inspect(args[1:])
	case "import":
		return a.importBundle(ctx, args[1:])
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

func (a *App) list(ctx context.Context, args []string) error {
	var includeAll, asJSON bool
	for _, arg := range args {
		switch arg {
		case "--all":
			includeAll = true
		case "--json":
			asJSON = true
		case "-h", "--help":
			_, writeErr := fmt.Fprintln(a.Stdout, "Usage: cxport list [--all] [--json]")
			return writeErr
		default:
			return &UsageError{Message: fmt.Sprintf("unknown list option %q", arg)}
		}
	}
	cwd, client, err := openCodex(ctx, includeAll)
	if err != nil {
		return err
	}
	defer client.Close()
	threads, err := client.List(cwd, false)
	if err != nil {
		return fmt.Errorf("list Codex sessions: %w", err)
	}
	if asJSON {
		return writeJSON(a.Stdout, threads)
	}
	writer := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "UPDATED\tID\tWORKSPACE\tTITLE"); err != nil {
		return err
	}
	for _, thread := range threads {
		updated := time.Unix(thread.UpdatedAt, 0).UTC().Format(time.RFC3339)
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", updated, thread.ID, thread.CWD, title(thread)); err != nil {
			return err
		}
	}
	return writer.Flush()
}

type exportOptions struct {
	session string
	last    bool
	all     bool
	output  string
	help    bool
}

func (a *App) export(ctx context.Context, args []string) error {
	options, err := parseExport(args)
	if err != nil {
		return err
	}
	if options.help {
		_, err := fmt.Fprintln(a.Stdout, "Usage: cxport export [SESSION] [--all] [--last] [-o PATH|-]")
		return err
	}
	if options.session == "" && !options.last {
		return &UsageError{Message: "export requires SESSION or --last (interactive picker is not implemented yet)"}
	}
	if options.session != "" && options.last {
		return &UsageError{Message: "SESSION and --last cannot be used together"}
	}

	home, err := codex.Home()
	if err != nil {
		return err
	}
	compatibility, err := requireSupportedCodex(codexBinary())
	if err != nil {
		return err
	}
	client, err := appserver.Start(ctx, codexBinary(), home)
	if err != nil {
		return err
	}
	defer client.Close()
	cwd := ""
	if !options.all {
		cwd, err = currentWorkspace()
		if err != nil {
			return err
		}
	}
	threads, err := client.List(cwd, false)
	if err != nil {
		return fmt.Errorf("discover Codex sessions: %w", err)
	}
	thread, err := selectThread(threads, options.session, options.last)
	if err != nil {
		return err
	}
	if thread.Status.Type != "idle" && thread.Status.Type != "notLoaded" {
		return fmt.Errorf("session %s is not quiescent (status %q)", thread.ID, thread.Status.Type)
	}
	if thread.Path == "" {
		return fmt.Errorf("Codex did not report a rollout path for session %s", thread.ID)
	}
	rollout, meta, err := compatibility.StableRollout(thread.Path)
	if err != nil {
		return fmt.Errorf("snapshot session %s: %w", thread.ID, err)
	}
	if meta.ID != thread.ID {
		return fmt.Errorf("rollout session ID %s does not match selected thread %s", meta.ID, thread.ID)
	}
	manifest := bundle.NewManifest(codex.Provider, compatibility.Version, meta.ID, meta.CWD, a.Now())
	output := options.output
	if output == "" {
		output = defaultArchiveName(thread)
	}
	encoded, err := encodeArchive(manifest, rollout)
	if err != nil {
		return err
	}
	if output == "-" {
		if _, err := a.Stdout.Write(encoded); err != nil {
			return fmt.Errorf("write archive to stdout: %w", err)
		}
		_, err = fmt.Fprintf(a.Stderr, "exported %s\n", thread.ID)
		return err
	}
	if err := writeNewFile(output, encoded); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.Stdout, "exported %s to %s\n", thread.ID, output)
	return err
}

func (a *App) inspect(args []string) error {
	var path string
	var asJSON bool
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		case "-h", "--help":
			_, err := fmt.Fprintln(a.Stdout, "Usage: cxport inspect <ARCHIVE|-> [--json]")
			return err
		default:
			if strings.HasPrefix(arg, "-") && arg != "-" {
				return &UsageError{Message: fmt.Sprintf("unknown inspect option %q", arg)}
			}
			if path != "" {
				return &UsageError{Message: "inspect accepts exactly one archive"}
			}
			path = arg
		}
	}
	if path == "" {
		return &UsageError{Message: "inspect requires an archive path or -"}
	}
	archive, err := a.readBundle(path)
	if err != nil {
		return err
	}
	if archive.Manifest.Provider != codex.Provider {
		return fmt.Errorf("unsupported provider %q", archive.Manifest.Provider)
	}
	if compatibility, compatibilityErr := codex.LookupCompatibility(archive.Manifest.ProviderVersion); compatibilityErr == nil {
		meta, err := compatibility.ValidateRollout(archive.Rollout)
		if err != nil {
			return fmt.Errorf("validate rollout: %w", err)
		}
		if err := manifestMatches(archive.Manifest, meta); err != nil {
			return err
		}
	}
	if asJSON {
		return writeJSON(a.Stdout, archive.Manifest)
	}
	_, err = fmt.Fprintf(a.Stdout, "session: %s\nprovider: %s %s\ncreated: %s\nworkspace: %s\n",
		archive.Manifest.SessionID,
		archive.Manifest.Provider,
		archive.Manifest.ProviderVersion,
		archive.Manifest.CreatedAt.Format(time.RFC3339),
		archive.Manifest.OriginalCWD,
	)
	return err
}

type importOptions struct {
	archive     string
	cwd         string
	preserveCWD bool
	ifExists    collisionPolicy
	dryRun      bool
	resume      bool
	help        bool
}

type collisionPolicy string

const (
	collisionError   collisionPolicy = "error"
	collisionSkip    collisionPolicy = "skip"
	collisionReplace collisionPolicy = "replace"
)

func (a *App) importBundle(ctx context.Context, args []string) error {
	options, err := parseImport(args)
	if err != nil {
		return err
	}
	if options.help {
		_, err := fmt.Fprintln(a.Stdout, "Usage: cxport import <ARCHIVE|-> [--cwd PATH | --preserve-cwd] [--if-exists error|skip|replace] [--dry-run] [--resume]")
		return err
	}
	archive, err := a.readBundle(options.archive)
	if err != nil {
		return err
	}
	if archive.Manifest.Provider != codex.Provider {
		return fmt.Errorf("unsupported provider %q", archive.Manifest.Provider)
	}
	sourceCompatibility, err := codex.LookupCompatibility(archive.Manifest.ProviderVersion)
	if err != nil {
		return err
	}
	sourceMeta, err := sourceCompatibility.ValidateRollout(archive.Rollout)
	if err != nil {
		return fmt.Errorf("validate rollout: %w", err)
	}
	if err := manifestMatches(archive.Manifest, sourceMeta); err != nil {
		return err
	}
	targetCompatibility, err := requireSupportedCodex(codexBinary())
	if err != nil {
		return err
	}
	if sourceCompatibility.Version != targetCompatibility.Version {
		return fmt.Errorf("archive Codex version %s is incompatible with installed version %s", sourceCompatibility.Version, targetCompatibility.Version)
	}
	if !targetCompatibility.AppServer.ScanRepair || !targetCompatibility.AppServer.Read {
		return fmt.Errorf("Codex %s lacks verified App Server import capabilities", targetCompatibility.Version)
	}
	if options.ifExists == collisionReplace && !targetCompatibility.AppServer.Delete {
		return fmt.Errorf("Codex %s lacks a verified App Server delete capability required by replace", targetCompatibility.Version)
	}
	targetCWD := options.cwd
	if options.preserveCWD {
		targetCWD = archive.Manifest.OriginalCWD
	} else if targetCWD == "" {
		targetCWD, err = currentWorkspace()
	} else {
		targetCWD, err = canonicalWorkspace(targetCWD)
	}
	if err != nil {
		return fmt.Errorf("resolve target workspace: %w", err)
	}
	rollout := archive.Rollout
	meta := sourceMeta
	if targetCWD != sourceMeta.CWD {
		rollout, meta, err = targetCompatibility.RemapWorkspace(archive.Rollout, targetCWD)
		if err != nil {
			return fmt.Errorf("remap workspace: %w", err)
		}
	}

	home, err := codex.Home()
	if err != nil {
		return err
	}
	destination := targetCompatibility.RolloutPath(home, meta)
	if options.dryRun {
		return a.dryRunImport(home, destination, meta, targetCWD, options.ifExists)
	}
	client, err := appserver.Start(ctx, codexBinary(), home)
	if err != nil {
		return err
	}
	defer client.Close()
	reset := func() error { return client.Delete(meta.ID) }
	repair := func(repairMeta codex.SessionMeta) error {
		return verifyRepair(client, repairMeta.ID, repairMeta.CWD)
	}
	if err := targetCompatibility.RecoverReplacement(destination, meta.ID, reset, repair); err != nil {
		return err
	}
	existing, err := codex.ExistingRollouts(home, meta.ID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		if options.ifExists == collisionSkip {
			_, err := fmt.Fprintf(a.Stdout, "session %s already exists; skipped\n", meta.ID)
			return err
		}
		if options.ifExists == collisionError {
			return fmt.Errorf("session %s already exists", meta.ID)
		}
		if err := requireReplaceable(existing, destination, meta.ID); err != nil {
			return err
		}
		threads, err := client.List("", false)
		if err != nil {
			return fmt.Errorf("check existing session: %w", err)
		}
		thread := findThread(threads, meta.ID)
		if thread == nil {
			return fmt.Errorf("Codex does not list existing session %s", meta.ID)
		}
		if thread.Status.Type != "idle" && thread.Status.Type != "notLoaded" {
			return fmt.Errorf("existing session %s is not quiescent (status %q)", meta.ID, thread.Status.Type)
		}
		if err := targetCompatibility.ReplaceRollout(destination, rollout, meta, reset, repair); err != nil {
			return fmt.Errorf("replace session %s: %w", meta.ID, err)
		}
		if _, err := fmt.Fprintf(a.Stdout, "replaced %s in %s\nresume with: codex resume %s\n", meta.ID, targetCWD, meta.ID); err != nil {
			return err
		}
		return a.resumeIfRequested(ctx, options.resume, targetCWD, meta.ID)
	}
	if err := placeRollout(destination, rollout); err != nil {
		return err
	}
	if err := verifyRepair(client, meta.ID, targetCWD); err != nil {
		cleanupErr := client.Delete(meta.ID)
		if cleanupErr != nil {
			return fmt.Errorf("repair imported session: %w; cleanup also failed: %v", err, cleanupErr)
		}
		return fmt.Errorf("repair imported session: %w", err)
	}
	if _, err := fmt.Fprintf(a.Stdout, "imported %s into %s\nresume with: codex resume %s\n", meta.ID, targetCWD, meta.ID); err != nil {
		return err
	}
	return a.resumeIfRequested(ctx, options.resume, targetCWD, meta.ID)
}

func (a *App) dryRunImport(home, destination string, meta codex.SessionMeta, targetCWD string, policy collisionPolicy) error {
	pending, err := codex.ReplacementPending(destination)
	if err != nil {
		return fmt.Errorf("inspect replacement recovery state: %w", err)
	}
	if pending {
		return errors.New("an interrupted replacement requires recovery; rerun without --dry-run")
	}
	existing, err := codex.ExistingRollouts(home, meta.ID)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		_, err := fmt.Fprintf(a.Stdout, "import valid: %s -> %s\n", meta.ID, targetCWD)
		return err
	}
	switch policy {
	case collisionSkip:
		_, err = fmt.Fprintf(a.Stdout, "session %s already exists; would skip\n", meta.ID)
		return err
	case collisionReplace:
		if err := requireReplaceable(existing, destination, meta.ID); err != nil {
			return err
		}
		_, err = fmt.Fprintf(a.Stdout, "replacement valid: %s -> %s\n", meta.ID, targetCWD)
		return err
	default:
		return fmt.Errorf("session %s already exists", meta.ID)
	}
}

// requireReplaceable allows replacement only for one active rollout at the
// expected destination; archived or moved copies must be handled in Codex.
func requireReplaceable(existing []string, destination, sessionID string) error {
	if len(existing) == 1 && filepath.Clean(existing[0]) == filepath.Clean(destination) {
		return nil
	}
	return fmt.Errorf("session %s exists at %s; restore or delete it in Codex before replacing it", sessionID, strings.Join(existing, ", "))
}

func (a *App) resumeIfRequested(ctx context.Context, resume bool, cwd, sessionID string) error {
	if !resume {
		return nil
	}
	command := exec.CommandContext(ctx, codexBinary(), "resume", sessionID)
	command.Dir = cwd
	command.Stdin = a.Stdin
	command.Stdout = a.Stdout
	command.Stderr = a.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("resume imported session: %w", err)
	}
	return nil
}

func (a *App) doctor(ctx context.Context, args []string) error {
	var asJSON bool
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		case "-h", "--help":
			_, err := fmt.Fprintln(a.Stdout, "Usage: cxport doctor [--json]")
			return err
		default:
			return &UsageError{Message: fmt.Sprintf("unknown doctor option %q", arg)}
		}
	}
	home, err := codex.Home()
	if err != nil {
		return err
	}
	version, versionErr := codex.Version(codexBinary())
	report := struct {
		CodexHome             string                      `json:"codex_home"`
		CodexVersion          string                      `json:"codex_version,omitempty"`
		Supported             bool                        `json:"supported"`
		SupportedVersions     []string                    `json:"supported_versions"`
		LayoutFingerprint     string                      `json:"layout_fingerprint,omitempty"`
		RolloutFilenameLayout string                      `json:"rollout_filename_layout,omitempty"`
		Capabilities          codex.AppServerCapabilities `json:"app_server_capabilities"`
		AppServer             bool                        `json:"app_server"`
		AppServerError        string                      `json:"app_server_error,omitempty"`
		DetectionError        string                      `json:"detection_error,omitempty"`
	}{CodexHome: home, CodexVersion: version, SupportedVersions: codex.SupportedVersions()}
	if versionErr == nil {
		compatibility, compatibilityErr := codex.LookupCompatibility(version)
		if compatibilityErr == nil {
			report.Supported = true
			report.LayoutFingerprint = compatibility.LayoutFingerprint
			report.RolloutFilenameLayout = compatibility.RolloutFilenameLayout
			report.Capabilities = compatibility.AppServer
		}
	}
	if versionErr != nil {
		report.DetectionError = versionErr.Error()
	} else {
		client, startErr := appserver.Start(ctx, codexBinary(), home)
		if startErr == nil {
			_, startErr = client.List("", true)
			client.Close()
		}
		report.AppServer = startErr == nil
		if startErr != nil {
			report.AppServerError = startErr.Error()
		}
	}
	if asJSON {
		return writeJSON(a.Stdout, report)
	}
	_, err = fmt.Fprintf(
		a.Stdout,
		"Codex home: %s\nCodex version: %s\nSupported: %t\nLayout: %s\nApp Server: %t\n",
		home, version, report.Supported, report.LayoutFingerprint, report.AppServer,
	)
	if err != nil {
		return err
	}
	if report.DetectionError != "" {
		_, err = fmt.Fprintf(a.Stdout, "Detection error: %s\n", report.DetectionError)
	} else if report.AppServerError != "" {
		_, err = fmt.Fprintf(a.Stdout, "App Server error: %s\n", report.AppServerError)
	}
	return err
}

func parseExport(args []string) (exportOptions, error) {
	var options exportOptions
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--last":
			options.last = true
		case arg == "--all":
			options.all = true
		case arg == "-h" || arg == "--help":
			options.help = true
		case arg == "-o" || arg == "--output":
			index++
			if index >= len(args) {
				return options, &UsageError{Message: arg + " requires a path"}
			}
			options.output = args[index]
		case strings.HasPrefix(arg, "-o="):
			options.output = strings.TrimPrefix(arg, "-o=")
		case strings.HasPrefix(arg, "--output="):
			options.output = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-"):
			return options, &UsageError{Message: fmt.Sprintf("unknown export option %q", arg)}
		case options.session == "":
			options.session = arg
		default:
			return options, &UsageError{Message: "export accepts at most one session selector"}
		}
	}
	return options, nil
}

func parseImport(args []string) (importOptions, error) {
	options := importOptions{ifExists: collisionError}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--preserve-cwd":
			options.preserveCWD = true
		case arg == "--dry-run":
			options.dryRun = true
		case arg == "--resume":
			options.resume = true
		case arg == "-h" || arg == "--help":
			options.help = true
		case arg == "--cwd" || arg == "--if-exists":
			index++
			if index >= len(args) {
				return options, &UsageError{Message: arg + " requires a value"}
			}
			if arg == "--cwd" {
				options.cwd = args[index]
			} else {
				options.ifExists = collisionPolicy(args[index])
			}
		case strings.HasPrefix(arg, "--cwd="):
			options.cwd = strings.TrimPrefix(arg, "--cwd=")
		case strings.HasPrefix(arg, "--if-exists="):
			options.ifExists = collisionPolicy(strings.TrimPrefix(arg, "--if-exists="))
		case strings.HasPrefix(arg, "-") && arg != "-":
			return options, &UsageError{Message: fmt.Sprintf("unknown import option %q", arg)}
		case options.archive == "":
			options.archive = arg
		default:
			return options, &UsageError{Message: "import accepts exactly one archive"}
		}
	}
	if options.help {
		return options, nil
	}
	if options.archive == "" {
		return options, &UsageError{Message: "import requires an archive path or -"}
	}
	if options.cwd != "" && options.preserveCWD {
		return options, &UsageError{Message: "--cwd and --preserve-cwd cannot be used together"}
	}
	if options.ifExists != collisionError && options.ifExists != collisionSkip && options.ifExists != collisionReplace {
		return options, &UsageError{Message: "--if-exists must be error, skip, or replace"}
	}
	if options.dryRun && options.resume {
		return options, &UsageError{Message: "--dry-run and --resume cannot be used together"}
	}
	return options, nil
}

func openCodex(ctx context.Context, includeAll bool) (string, *appserver.Client, error) {
	home, err := codex.Home()
	if err != nil {
		return "", nil, err
	}
	if _, err := requireSupportedCodex(codexBinary()); err != nil {
		return "", nil, err
	}
	cwd := ""
	if !includeAll {
		cwd, err = currentWorkspace()
		if err != nil {
			return "", nil, err
		}
	}
	client, err := appserver.Start(ctx, codexBinary(), home)
	if err != nil {
		return "", nil, err
	}
	return cwd, client, nil
}

func currentWorkspace() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current workspace: %w", err)
	}
	return canonicalWorkspace(cwd)
}

func codexBinary() string {
	if value := os.Getenv("CXPORT_CODEX"); value != "" {
		return value
	}
	return "codex"
}

func requireSupportedCodex(path string) (codex.Compatibility, error) {
	version, err := codex.Version(path)
	if err != nil {
		return codex.Compatibility{}, err
	}
	return codex.LookupCompatibility(version)
}

func selectThread(threads []appserver.Thread, selector string, last bool) (appserver.Thread, error) {
	if last {
		if len(threads) == 0 {
			return appserver.Thread{}, errors.New("no Codex sessions found")
		}
		return threads[0], nil
	}
	var matches []appserver.Thread
	for _, thread := range threads {
		if thread.ID == selector || strings.HasPrefix(thread.ID, selector) || thread.Name == selector {
			matches = append(matches, thread)
		}
	}
	if len(matches) == 0 {
		return appserver.Thread{}, fmt.Errorf("no session matches %q", selector)
	}
	if len(matches) > 1 {
		return appserver.Thread{}, fmt.Errorf("session selector %q is ambiguous", selector)
	}
	return matches[0], nil
}

func findThread(threads []appserver.Thread, id string) *appserver.Thread {
	for index := range threads {
		if threads[index].ID == id {
			return &threads[index]
		}
	}
	return nil
}

func title(thread appserver.Thread) string {
	value := thread.Name
	if value == "" {
		value = thread.Preview
	}
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 72 {
		value = string(runes[:69]) + "..."
	}
	return value
}

func defaultArchiveName(thread appserver.Thread) string {
	value := title(thread)
	if value == "" {
		value = "codex-session"
	}
	var output strings.Builder
	lastWasDash := false
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			output.WriteRune(character)
			lastWasDash = false
		} else if output.Len() > 0 && !lastWasDash {
			output.WriteByte('-')
			lastWasDash = true
		}
		if output.Len() >= 48 {
			break
		}
	}
	base := strings.Trim(output.String(), "-")
	if base == "" {
		base = "codex-session"
	}
	shortID := thread.ID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	return fmt.Sprintf("%s-%s.cxport", base, shortID)
}

// encodeArchive builds and verifies the complete archive before any output is
// written, so a failed export never emits a partial archive.
func encodeArchive(manifest bundle.Manifest, rollout []byte) ([]byte, error) {
	var encoded bytes.Buffer
	if err := bundle.Write(&encoded, manifest, rollout); err != nil {
		return nil, err
	}
	if _, err := bundle.Read(bytes.NewReader(encoded.Bytes())); err != nil {
		return nil, fmt.Errorf("verify completed archive: %w", err)
	}
	return encoded.Bytes(), nil
}

// writeNewFile creates path with owner-only permissions without replacing an
// existing file. It avoids hard links, which removable FAT and exFAT drives
// do not support, and removes the file again when writing fails.
func writeNewFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("output already exists: %s", path)
	}
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write archive: %w", err)
	}
	return nil
}

func (a *App) readBundle(path string) (bundle.Bundle, error) {
	if path == "-" {
		temporary, err := os.CreateTemp("", "cxport-import-*.cxport")
		if err != nil {
			return bundle.Bundle{}, fmt.Errorf("create private import spool: %w", err)
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		if err := temporary.Chmod(0o600); err != nil {
			temporary.Close()
			return bundle.Bundle{}, fmt.Errorf("protect import spool: %w", err)
		}
		written, err := io.Copy(temporary, io.LimitReader(a.Stdin, maxInputArchive+1))
		if err != nil {
			temporary.Close()
			return bundle.Bundle{}, fmt.Errorf("spool archive from stdin: %w", err)
		}
		if written > maxInputArchive {
			temporary.Close()
			return bundle.Bundle{}, errors.New("archive from stdin exceeds its size limit")
		}
		if _, err := temporary.Seek(0, io.SeekStart); err != nil {
			temporary.Close()
			return bundle.Bundle{}, fmt.Errorf("rewind import spool: %w", err)
		}
		archive, err := bundle.Read(temporary)
		closeErr := temporary.Close()
		if err != nil {
			return bundle.Bundle{}, fmt.Errorf("read archive from stdin: %w", err)
		}
		if closeErr != nil {
			return bundle.Bundle{}, fmt.Errorf("close import spool: %w", closeErr)
		}
		return archive, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return bundle.Bundle{}, fmt.Errorf("open archive: %w", err)
	}
	defer file.Close()
	archive, err := bundle.Read(file)
	if err != nil {
		return bundle.Bundle{}, fmt.Errorf("read archive: %w", err)
	}
	return archive, nil
}

func manifestMatches(manifest bundle.Manifest, meta codex.SessionMeta) error {
	if manifest.SessionID != meta.ID {
		return fmt.Errorf("manifest session ID %s does not match rollout %s", manifest.SessionID, meta.ID)
	}
	if manifest.OriginalCWD != meta.CWD {
		return fmt.Errorf("manifest workspace %s does not match rollout %s", manifest.OriginalCWD, meta.CWD)
	}
	return nil
}

func placeRollout(destination string, rollout []byte) error {
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".cxport-rollout-*")
	if err != nil {
		return fmt.Errorf("create staged rollout: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(rollout); err != nil {
		temporary.Close()
		return fmt.Errorf("write staged rollout: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync staged rollout: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close staged rollout: %w", err)
	}
	if err := os.Link(temporaryPath, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("rollout destination already exists: %s", destination)
		}
		return fmt.Errorf("commit rollout: %w", err)
	}
	return nil
}

func verifyRepair(client *appserver.Client, id, cwd string) error {
	threads, err := client.List("", false)
	if err != nil {
		return err
	}
	thread := findThread(threads, id)
	if thread == nil {
		return errors.New("App Server did not discover the imported session")
	}
	stateThreads, err := client.List("", true)
	if err != nil {
		return err
	}
	stateThread := findThread(stateThreads, id)
	if stateThread == nil {
		return errors.New("App Server did not persist the imported session")
	}
	readThread, err := client.Read(id)
	if err != nil {
		return err
	}
	if thread.CWD != cwd || stateThread.CWD != cwd || readThread.CWD != cwd {
		return fmt.Errorf("repaired workspace does not match target %q (scan %q, state %q, read %q)", cwd, thread.CWD, stateThread.CWD, readThread.CWD)
	}
	return nil
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
