package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Riki1312/agent-teleport/internal/claude"
	"github.com/Riki1312/agent-teleport/internal/provider"
)

func testSessions() []provider.Session {
	return []provider.Session{
		{Provider: claude.Provider, ID: "aaaa1111-0000-0000-0000-000000000000", Title: "Fix the login bug", UpdatedAt: time.Now()},
		{Provider: claude.Provider, ID: "bbbb2222-0000-0000-0000-000000000000", Title: "Login page redesign", UpdatedAt: time.Now()},
		{Provider: claude.Provider, ID: "cccc3333-0000-0000-0000-000000000000", Title: "bbbb", UpdatedAt: time.Now()},
	}
}

func TestSelectSessionPrefersTheMostPreciseMatch(t *testing.T) {
	for selector, want := range map[string]string{
		"aaaa":              "aaaa1111-0000-0000-0000-000000000000",
		"bbbb":              "bbbb2222-0000-0000-0000-000000000000",
		"Fix the login bug": "aaaa1111-0000-0000-0000-000000000000",
		"REDESIGN":          "bbbb2222-0000-0000-0000-000000000000",
	} {
		// when
		session, err := selectSession(testSessions(), selector, false)

		// then
		if err != nil || session.ID != want {
			t.Fatalf("selectSession(%q) = %s, %v; want %s", selector, session.ID, err, want)
		}
	}
}

func TestSelectSessionListsTheSessionsOfAnAmbiguousSelector(t *testing.T) {
	// when
	_, err := selectSession(testSessions(), "login", false)

	// then
	if err == nil || !strings.Contains(err.Error(), "matches 2 sessions") || !strings.Contains(err.Error(), "aaaa1111  Fix the login bug") {
		t.Fatalf("selectSession() error = %v", err)
	}
	if _, err := selectSession(testSessions(), "nothing", false); !errors.Is(err, errNoMatch) {
		t.Fatalf("selectSession() error = %v, want errNoMatch", err)
	}
}

func TestChooseSessionAsksForANumber(t *testing.T) {
	for input, want := range map[string]string{"2\n": "bbbb2222-0000-0000-0000-000000000000", " 1 ": "aaaa1111-0000-0000-0000-000000000000"} {
		var prompt bytes.Buffer
		app := &App{Stdin: strings.NewReader(input), Stderr: &prompt, Now: time.Now, Interactive: true}

		// when
		session, err := app.chooseSession(testSessions(), false)

		// then
		if err != nil || session.ID != want {
			t.Fatalf("chooseSession(%q) = %s, %v", input, session.ID, err)
		}
		if !strings.Contains(prompt.String(), "  2  just now  claude  bbbb2222  Login page redesign") || !strings.HasSuffix(prompt.String(), "Session [1-3]: ") {
			t.Fatalf("prompt =\n%s", prompt.String())
		}
	}
}

func TestChooseSessionRejectsOtherAnswers(t *testing.T) {
	for _, input := range []string{"", "0\n", "4\n", "two\n"} {
		app := &App{Stdin: strings.NewReader(input), Stderr: &bytes.Buffer{}, Now: time.Now, Interactive: true}

		// when
		_, err := app.chooseSession(testSessions(), false)

		// then
		if err == nil {
			t.Fatalf("chooseSession(%q) succeeded", input)
		}
	}
}

func TestSessionSelectorNeedsASessionOutsideTerminalsAndAgents(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")

	// when
	_, err := (&App{}).sessionSelector(nil, false)

	// then
	if !errors.Is(err, errNoSelection) {
		t.Fatalf("sessionSelector() error = %v", err)
	}
	if _, err := (&App{Interactive: true}).sessionSelector(nil, false); err != nil {
		t.Fatalf("sessionSelector() at a terminal error = %v", err)
	}
}

func TestResumeIsRefusedInsideAnAgent(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "6f5fb979-80ed-48da-8688-9ee51a3bd400")

	// when
	err := resumeAllowed(false, true)

	// then
	var usage *UsageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "/resume") {
		t.Fatalf("resumeAllowed() error = %v", err)
	}
	if err := resumeAllowed(false, false); err != nil {
		t.Fatalf("resumeAllowed() without --resume error = %v", err)
	}
}
