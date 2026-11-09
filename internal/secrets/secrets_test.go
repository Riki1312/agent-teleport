package secrets

import (
	"slices"
	"strings"
	"testing"
)

// Credential-shaped values are assembled at run time, so the repository holds
// no literal that looks like a real credential.
func token(prefix string, length int) string {
	return prefix + strings.Repeat("A1b2", length/4+1)[:length]
}

func TestScanFindsEachCredentialFormat(t *testing.T) {
	for rule, value := range map[string]string{
		"private key":       "-----BEGIN OPENSSH " + "PRIVATE KEY-----",
		"AWS access key ID": "AKIA" + strings.Repeat("Z", 16),
		"GitHub token":      token("gh"+"p_", 36),
		"Anthropic API key": token("sk-"+"ant-api03-", 90),
		"OpenAI API key":    token("sk-"+"proj-", 48),
		"Slack token":       "xox" + "b-123456789-" + token("", 24),
		"Google API key":    token("AI"+"za", 35),
		"Stripe secret key": token("sk"+"_live_", 24),
	} {
		t.Run(rule, func(t *testing.T) {
			// when
			findings := Scan("session.jsonl", []byte("first line\nexport KEY="+value+"\n"), 0)

			// then
			if !slices.Equal(findings, []Finding{{Path: "session.jsonl", Line: 2, Rule: rule}}) {
				t.Fatalf("Scan() = %+v", findings)
			}
		})
	}
}

func TestScanDecodesJSONStrings(t *testing.T) {
	record := `{"output":"ok\n` + token("gh"+"p_", 36) + `","other":["` + token("gh"+"p_", 40) + `"]}` + "\n"

	// when
	findings := Scan("session.jsonl", []byte(record), 0)

	// then
	if !slices.Equal(findings, []Finding{{Path: "session.jsonl", Line: 1, Rule: "GitHub token"}}) {
		t.Fatalf("Scan() = %+v", findings)
	}
}

func TestScanIgnoresTokensEmbeddedInLongerWords(t *testing.T) {
	// A base64 blob, such as encrypted reasoning, may contain any prefix.
	blob := `{"encrypted":"` + strings.Repeat("Qx9", 10) + token("AI"+"za", 35) + strings.Repeat("Qx9", 10) + `"}` + "\n"

	// when, then
	if findings := Scan("session.jsonl", []byte(blob), 0); len(findings) != 0 {
		t.Fatalf("Scan() = %+v", findings)
	}
}

func TestScanStartsAtTheGivenOffset(t *testing.T) {
	old := "KEY=" + token("gh"+"p_", 36) + "\n"
	data := []byte(old + "clean\nKEY=" + token("sk"+"_live_", 30) + "\n")

	// when
	findings := Scan("output.txt", data, len(old))

	// then
	if !slices.Equal(findings, []Finding{{Path: "output.txt", Line: 3, Rule: "Stripe secret key"}}) {
		t.Fatalf("Scan() = %+v", findings)
	}
}
