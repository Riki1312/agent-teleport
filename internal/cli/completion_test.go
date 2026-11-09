package cli

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
)

func TestEveryDocumentedOptionIsAccepted(t *testing.T) {
	for command, usage := range commandUsages {
		for _, documented := range options(usage) {
			for _, name := range documented.names {
				arguments := []string{command, name}
				if documented.argument != "" {
					arguments = append(arguments, "value")
				}
				app := &App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}

				// when
				err := app.Run(context.Background(), append(arguments, "--help"))

				// then
				if err != nil {
					t.Fatalf("%s: %v", strings.Join(arguments, " "), err)
				}
			}
		}
	}
}

func TestCandidates(t *testing.T) {
	app := &App{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	for words, want := range map[string][]string{
		"ex":                {"completion", "doctor", "export", "import", "inspect", "list", "open", "save", "status"},
		"export --":         {"--last", "--all", "--output"},
		"export -o ":        {filesCandidate},
		"import --cwd ":     {filesCandidate},
		"save --name ":      nil,
		"import ":           {filesCandidate},
		"completion ":       {"bash", "zsh"},
		"unknown ":          nil,
		"status --":         {"--json"},
		"save --allow-secr": {"--last", "--all", "--name", "--allow-secrets"},
	} {
		// when
		candidates := app.candidates(context.Background(), strings.Split(words, " "))

		// then
		if !slices.Equal(candidates, want) {
			t.Fatalf("candidates(%q) = %q, want %q", words, candidates, want)
		}
	}
}

func TestCompletionPrintsAScriptPerShell(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		var output bytes.Buffer
		app := &App{Stdout: &output, Stderr: &bytes.Buffer{}}

		// when
		err := app.Run(context.Background(), []string{"completion", shell})

		// then
		if err != nil || !strings.Contains(output.String(), "agent-teleport __complete") {
			t.Fatalf("completion %s = %v:\n%s", shell, err, output.String())
		}
	}
	if err := (&App{Stdout: &bytes.Buffer{}}).Run(context.Background(), []string{"completion", "fish"}); err == nil {
		t.Fatal("completion accepted an unsupported shell")
	}
}
