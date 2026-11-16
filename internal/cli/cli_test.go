package cli

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Riki1312/agent-teleport/internal/provider"
)

func TestParseAcceptsFlagsBetweenArguments(t *testing.T) {
	app := &App{Stdout: &bytes.Buffer{}}
	set := newFlagSet("export")
	output := set.String("o", "", "")
	includeAll := set.Bool("all", false, "")

	// when
	positional, err := app.parse(set, []string{"session-id", "-o", "-", "--all", "second"}, exportUsage)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(positional, []string{"session-id", "second"}) || *output != "-" || !*includeAll {
		t.Fatalf("parse() = %q, output %q, all %t", positional, *output, *includeAll)
	}
}

func TestImportRejectsInvalidOptionsBeforeReadingArchive(t *testing.T) {
	for _, arguments := range [][]string{
		{"import", "session.teleport", "--dry-run", "--resume"},
		{"import", "-", "--resume"},
		{"import", "session.teleport", "--if-exists", "replace"},
		{"import"},
		{"import", "one.teleport", "two.teleport"},
		{"import", "session.teleport", "--unknown"},
	} {
		app := &App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

		// when
		err := app.Run(context.Background(), arguments)

		// then
		var usageError *UsageError
		if !errors.As(err, &usageError) {
			t.Fatalf("Run(%q) error = %v, want a usage error", arguments, err)
		}
	}
}

func TestCommandHelpSucceeds(t *testing.T) {
	for _, command := range []string{"list", "export", "inspect", "import", "doctor"} {
		output := &bytes.Buffer{}
		app := &App{Stdout: output}

		// when
		err := app.Run(context.Background(), []string{command, "--help"})

		// then
		if err != nil || !bytes.Contains(output.Bytes(), []byte("Usage:")) {
			t.Fatalf("%s --help = %q, %v", command, output, err)
		}
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		elapsed time.Duration
		want    string
	}{
		{elapsed: 30 * time.Second, want: "just now"},
		{elapsed: time.Minute, want: "1 minute ago"},
		{elapsed: 5 * time.Hour, want: "5 hours ago"},
		{elapsed: 3 * 24 * time.Hour, want: "3 days ago"},
	} {
		// when, then
		if got := relativeTime(now, now.Add(-test.elapsed)); got != test.want {
			t.Fatalf("relativeTime(%v) = %q, want %q", test.elapsed, got, test.want)
		}
	}
}

func TestVersionSucceeds(t *testing.T) {
	output := &bytes.Buffer{}
	app := &App{Stdout: output}

	// when
	err := app.Run(context.Background(), []string{"--version"})

	// then
	if err != nil || !bytes.HasPrefix(output.Bytes(), []byte("agent-teleport ")) {
		t.Fatalf("--version = %q, %v", output, err)
	}
}

// Embedding the unused store operations makes unexpected calls fail rather
// than giving the fixture an implementation unrelated to this boundary.
type instructionStore struct {
	provider.Store
	result   provider.ImportResult
	prepared int
}

func (s *instructionStore) Import(provider.ImportRequest) (provider.ImportResult, error) {
	return s.result, nil
}

var errResumePreparation = errors.New("runtime setup unavailable")

func (s *instructionStore) PrepareResume(string, string) (*exec.Cmd, error) {
	s.prepared++
	return nil, errResumePreparation
}

func TestImportInstructionsDoNotPrepareResume(t *testing.T) {
	adapter := provider.Adapter{Name: "Codex", Current: func() (string, bool) { return "", false }}
	for _, test := range []struct{ name, reference string }{{"normal", ""}, {"isolated", "iso-0123456789abcdef"}} {
		t.Run(test.name, func(t *testing.T) {
			reference := test.reference
			var output bytes.Buffer
			store := &instructionStore{result: provider.ImportResult{SessionID: savedSessionID, Reference: reference, Workspace: "/workspace", Outcome: provider.Imported}}
			app := &App{Stdout: &output}
			if err := app.importInto(adapter, store, provider.ImportRequest{}, false); err != nil {
				t.Fatal(err)
			}
			selector := reference
			if selector == "" {
				selector = savedSessionID
			}
			if !strings.Contains(output.String(), "resume with: agent-teleport resume "+selector) || store.prepared != 0 {
				t.Fatalf("instructions prepared runtime or selected another copy: %q, calls=%d", output.String(), store.prepared)
			}
			if err := app.runResume(store, selector, "/workspace"); !errors.Is(err, errResumePreparation) || store.prepared != 1 {
				t.Fatalf("launch skipped runtime preparation: %v, calls=%d", err, store.prepared)
			}
		})
	}
}
