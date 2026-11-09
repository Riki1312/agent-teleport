package cli

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/repository"
	"github.com/Riki1312/agent-teleport/internal/secrets"
)

const savedSessionID = "6f5fb979-80ed-48da-8688-9ee51a3bd400"

func savedSession(t *testing.T, id string, payloads ...archive.Payload) archive.Archive {
	t.Helper()
	created, err := archive.New(archive.Header{
		Provider: "anthropic-claude", ProviderVersion: "2.1.319", SessionID: id,
		Workspace: repository.Workspace, Home: repository.Home, CreatedAt: time.Now(),
	}, payloads)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestSessionNameIsDerivedFromTheTitle(t *testing.T) {
	for title, want := range map[string]string{
		"Fix the login bug!":     "fix-the-login-bug",
		"Ünïcode only: ✓":        "n-code-only",
		"":                       "session",
		strings.Repeat("ab", 40): strings.Repeat("ab", 24),
	} {
		// when, then
		if name := sessionName(title); name != want || repository.ValidateName(name) != nil {
			t.Fatalf("sessionName(%q) = %q, want %q", title, name, want)
		}
	}
}

func TestSaveTargetReusesTheNameOfAnEarlierSave(t *testing.T) {
	payload := archive.Payload{Path: "s.jsonl", Data: []byte("{}\n")}
	session := savedSession(t, savedSessionID, payload)
	other := savedSession(t, "11111111-1111-1111-1111-111111111111", payload)
	existing := []repository.Saved{{Name: "fix-login", Archive: other}, {Name: "earlier", Archive: session}}

	// when
	name, previous, err := saveTarget(existing, session, "", "Fix login")

	// then
	if err != nil || name != "earlier" || previous == nil {
		t.Fatalf("saveTarget() = %q, %v, %v", name, previous, err)
	}
	if _, _, err := saveTarget(existing, session, "renamed", "Fix login"); err == nil {
		t.Fatal("saveTarget() renamed an earlier save")
	}
}

func TestSaveTargetAvoidsNamesOfOtherSessions(t *testing.T) {
	payload := archive.Payload{Path: "s.jsonl", Data: []byte("{}\n")}
	existing := []repository.Saved{{Name: "fix-login", Archive: savedSession(t, "11111111-1111-1111-1111-111111111111", payload)}}
	session := savedSession(t, savedSessionID, payload)

	// when
	name, previous, err := saveTarget(existing, session, "", "Fix login")

	// then
	if err != nil || name != "fix-login-6f5fb979" || previous != nil {
		t.Fatalf("saveTarget() = %q, %v, %v", name, previous, err)
	}
	if _, _, err := saveTarget(existing, session, "fix-login", ""); err == nil {
		t.Fatal("saveTarget() reused the name of another session")
	}
}

func TestScanSecretsScansOnlyWhatASaveAdds(t *testing.T) {
	key := "-----BEGIN RSA " + "PRIVATE KEY-----"
	previous := savedSession(t, savedSessionID, archive.Payload{Path: "s.jsonl", Data: []byte(`{"m":"` + key + `"}` + "\n")})
	grown := savedSession(t, savedSessionID, archive.Payload{Path: "s.jsonl", Data: []byte(`{"m":"` + key + `"}` + "\n" + `{"m":"clean"}` + "\n" + `{"m":"` + key + `"}` + "\n")})

	// when
	findings := scanSecrets(grown, &previous)

	// then
	if !slices.Equal(findings, []secrets.Finding{{Path: "s.jsonl", Line: 3, Rule: "private key"}}) {
		t.Fatalf("scanSecrets() = %+v", findings)
	}
}
