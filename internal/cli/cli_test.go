package cli

import "testing"

func TestParseExportInterspersedOptions(t *testing.T) {
	options, err := parseExport([]string{"session-id", "-o", "session.cxport", "--all"})
	if err != nil {
		t.Fatal(err)
	}
	if options.session != "session-id" || options.output != "session.cxport" || !options.all {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestParseImportRejectsUnsafeCombinations(t *testing.T) {
	tests := [][]string{
		{"session.cxport", "--cwd", "/target", "--preserve-cwd"},
		{"session.cxport", "--dry-run", "--resume"},
		{"session.cxport", "--if-exists", "replace"},
	}
	for _, arguments := range tests {
		if _, err := parseImport(arguments); err == nil {
			t.Fatalf("parseImport(%q) succeeded", arguments)
		}
	}
}
