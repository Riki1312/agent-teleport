package cli

import (
	"os"
	"path/filepath"
	"testing"
)

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
		{"session.cxport", "--if-exists", "overwrite"},
	}
	for _, arguments := range tests {
		if _, err := parseImport(arguments); err == nil {
			t.Fatalf("parseImport(%q) succeeded", arguments)
		}
	}
}

func TestParseImportAcceptsReplace(t *testing.T) {
	options, err := parseImport([]string{"session.cxport", "--if-exists", "replace"})
	if err != nil {
		t.Fatal(err)
	}
	if options.ifExists != collisionReplace {
		t.Fatalf("ifExists = %q", options.ifExists)
	}
}

func TestWriteNewFileKeepsExistingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.cxport")
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	// when
	err := writeNewFile(path, []byte("replacement"))

	// then
	if err == nil {
		t.Fatal("writeNewFile replaced an existing file")
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "existing" {
		t.Fatalf("existing output changed: %q, %v", data, readErr)
	}
}

func TestWriteNewFileUsesOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.cxport")

	// when
	err := writeNewFile(path, []byte("archive"))

	// then
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %v, want 0600", info.Mode().Perm())
	}
}
