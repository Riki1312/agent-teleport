// Package secrets finds credentials in session files before they are
// committed to a repository.
//
// The scanner matches only unambiguous credential formats, such as private key
// blocks and provider tokens with distinctive prefixes, so a finding is worth
// stopping for. It guesses nothing from entropy. JSON records are scanned by
// their decoded strings, so escapes such as \n never hide a token boundary.
package secrets

import (
	"bytes"
	"encoding/json"
	"regexp"
)

// Finding locates one likely credential without revealing it.
type Finding struct {
	// Path is the payload path of the file, and Line the 1-based line.
	Path string
	Line int
	// Rule names the credential format that matched.
	Rule string
}

// rule is one credential format.
type rule struct {
	name    string
	pattern *regexp.Regexp
}

var rules = []rule{
	{name: "private key", pattern: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{name: "AWS access key ID", pattern: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{name: "GitHub token", pattern: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{60,}\b`)},
	{name: "Anthropic API key", pattern: regexp.MustCompile(`\bsk-ant-[a-z]+[0-9]{2}-[A-Za-z0-9_-]{80,}`)},
	{name: "OpenAI API key", pattern: regexp.MustCompile(`\bsk-(?:proj|svcacct|admin)-[A-Za-z0-9_-]{40,}|\bsk-[A-Za-z0-9]{20}T3BlbkFJ[A-Za-z0-9]{20}\b`)},
	{name: "Slack token", pattern: regexp.MustCompile(`\bxox[abposr]-[0-9]{6,}-[0-9A-Za-z-]{10,}`)},
	{name: "Google API key", pattern: regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{name: "Stripe secret key", pattern: regexp.MustCompile(`\b[rs]k_live_[0-9A-Za-z]{24,}\b`)},
}

// Scan reports the likely credentials in data from byte offset from on, which
// must be at the start of a line, numbering lines from the start of data. A
// file that was scanned before is scanned only from where it grew.
func Scan(path string, data []byte, from int) []Finding {
	var findings []Finding
	line := bytes.Count(data[:from], []byte{'\n'})
	for content := range bytes.Lines(data[from:]) {
		line++
		strings := texts(content)
		for _, candidate := range rules {
			for _, text := range strings {
				if candidate.pattern.Match(text) {
					findings = append(findings, Finding{Path: path, Line: line, Rule: candidate.name})
					break
				}
			}
		}
	}
	return findings
}

// texts returns the decoded strings of a JSON line, or the line itself when it
// is not JSON.
func texts(line []byte) [][]byte {
	var value any
	if json.Unmarshal(line, &value) != nil {
		return [][]byte{line}
	}
	var found [][]byte
	var collect func(any)
	collect = func(value any) {
		switch typed := value.(type) {
		case string:
			found = append(found, []byte(typed))
		case []any:
			for _, item := range typed {
				collect(item)
			}
		case map[string]any:
			for _, item := range typed {
				collect(item)
			}
		}
	}
	collect(value)
	return found
}
