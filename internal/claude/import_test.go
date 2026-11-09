package claude

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/provider"
)

const (
	sourceWorkspace = "/source/project"
	sourceHome      = "/source/.claude"
	agentPath       = testSessionID + "/subagents/agent-a1.jsonl"
	metadataPath    = testSessionID + "/subagents/agent-a1.meta.json"
)

func message(text, workspace string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q},"cwd":%q,"sessionId":%q}`, text, workspace, testSessionID)
}

func agentMessage(workspace string) string {
	return fmt.Sprintf(`{"type":"user","isSidechain":true,"agentId":"a1","message":{"role":"user","content":"child"},"cwd":%q,"sessionId":%q}`, workspace, testSessionID)
}

// testArchive packs a session started in sourceWorkspace with the given
// prompts and a finished sub-agent.
func testArchive(t *testing.T, prompts ...string) archive.Archive {
	t.Helper()
	records := make([]string, 0, len(prompts))
	for _, prompt := range prompts {
		records = append(records, message(prompt, sourceWorkspace))
	}
	created, err := archive.New(archive.Header{
		Provider:        Provider,
		ProviderVersion: "2.1.319",
		SessionID:       testSessionID,
		Workspace:       sourceWorkspace,
		Home:            sourceHome,
		CreatedAt:       time.Now(),
	}, []archive.Payload{
		{Path: testSessionID + ".jsonl", Data: lines(records...)},
		{Path: agentPath, Data: lines(agentMessage(sourceWorkspace))},
		{Path: metadataPath, Data: []byte(`{"finished":true}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{home: t.TempDir(), version: "2.1.319", layout: layouts[0]}
}

func importArchive(t *testing.T, store *Store, source archive.Archive, workspace string, replace bool) (provider.ImportOutcome, error) {
	t.Helper()
	result, err := store.Import(provider.ImportRequest{Archive: source, Workspace: workspace, Replace: replace})
	return result.Outcome, err
}

func mustImport(t *testing.T, store *Store, source archive.Archive, workspace string, replace bool, want provider.ImportOutcome) {
	t.Helper()
	outcome, err := importArchive(t, store, source, workspace, replace)
	if err != nil || outcome != want {
		t.Fatalf("Import() = %q, %v; want %q", outcome, err, want)
	}
}

func storedFile(t *testing.T, store *Store, workspace, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(store.projectFolder(workspace), filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestImportPlacesTheRemappedSession(t *testing.T) {
	store := testStore(t)

	// when
	mustImport(t, store, testArchive(t, "one"), "/target/project", false, provider.Imported)

	// then
	if got := storedFile(t, store, "/target/project", testSessionID+".jsonl"); got != string(lines(message("one", "/target/project"))) {
		t.Fatalf("stored transcript = %s", got)
	}
	if got := storedFile(t, store, "/target/project", agentPath); got != string(lines(agentMessage("/target/project"))) {
		t.Fatalf("stored agent transcript = %s", got)
	}
	session, err := store.Session(testSessionID)
	if err != nil || session.Workspace != "/target/project" || session.Title != "one" {
		t.Fatalf("Session() = %+v, %v", session, err)
	}
}

func TestImportAppliesTheFastForwardRule(t *testing.T) {
	store := testStore(t)
	short, long := testArchive(t, "one"), testArchive(t, "one", "two")
	mustImport(t, store, short, "/target", false, provider.Imported)

	// when, then
	mustImport(t, store, short, "/target", false, provider.Unchanged)
	mustImport(t, store, long, "/target", false, provider.Updated)
	mustImport(t, store, long, "/target", false, provider.Unchanged)
	mustImport(t, store, short, "/target", false, provider.KeptNewer)
	if got := storedFile(t, store, "/target", testSessionID+".jsonl"); !strings.Contains(got, `"two"`) {
		t.Fatal("keeping the newer copy changed the transcript")
	}
}

func TestImportReplacesADivergedCopyOnlyWhenAsked(t *testing.T) {
	store := testStore(t)
	mustImport(t, store, testArchive(t, "one", "local"), "/target", false, provider.Imported)
	extra := filepath.Join(store.projectFolder("/target"), testSessionID, "tool-results", "extra.txt")
	if err := os.MkdirAll(filepath.Dir(extra), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extra, []byte("local only"), 0o600); err != nil {
		t.Fatal(err)
	}
	diverged := testArchive(t, "one", "remote")

	// when
	_, err := importArchive(t, store, diverged, "/target", false)

	// then
	if err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("Import() error = %v", err)
	}
	mustImport(t, store, diverged, "/target", true, provider.Replaced)
	if got := storedFile(t, store, "/target", testSessionID+".jsonl"); !strings.Contains(got, `"remote"`) {
		t.Fatal("replacement did not write the archived transcript")
	}
	if _, err := os.Stat(extra); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement kept a file the archive lacks: %v", err)
	}
}

func TestImportMovesASessionFromAnotherWorkspaceOnlyWhenAsked(t *testing.T) {
	store := testStore(t)
	source := testArchive(t, "one")
	mustImport(t, store, source, "/first", false, provider.Imported)

	// when
	_, err := importArchive(t, store, source, "/second", false)

	// then
	if err == nil || !strings.Contains(err.Error(), "/first") {
		t.Fatalf("Import() error = %v", err)
	}
	mustImport(t, store, source, "/second", true, provider.Replaced)
	if _, err := os.Stat(store.projectFolder("/first")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("moving the session left its old project folder: %v", err)
	}
	if session, err := store.Session(testSessionID); err != nil || session.Workspace != "/second" {
		t.Fatalf("Session() = %+v, %v", session, err)
	}
}

func TestImportRefusesSessionsOpenInClaudeCode(t *testing.T) {
	store := testStore(t)
	mustImport(t, store, testArchive(t, "one"), "/target", false, provider.Imported)
	register(t, store.home, os.Getpid())

	// when
	_, err := importArchive(t, store, testArchive(t, "one", "two"), "/target", false)

	// then
	if !errors.Is(err, errSessionOpen) {
		t.Fatalf("Import() error = %v, want errSessionOpen", err)
	}
	mustImport(t, store, testArchive(t, "one"), "/target", false, provider.Unchanged)
}

func TestImportIgnoresRegistryEntriesOfExitedProcesses(t *testing.T) {
	store := testStore(t)
	mustImport(t, store, testArchive(t, "one"), "/target", false, provider.Imported)
	register(t, store.home, exitedProcess(t))

	// when, then
	mustImport(t, store, testArchive(t, "one", "two"), "/target", false, provider.Updated)
}

func TestImportRejectsUnknownSessionFiles(t *testing.T) {
	store := testStore(t)
	mustImport(t, store, testArchive(t, "one"), "/target", false, provider.Imported)
	unknown := filepath.Join(store.projectFolder("/target"), testSessionID, "unknown.json")
	if err := os.WriteFile(unknown, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	// when
	_, err := importArchive(t, store, testArchive(t, "one"), "/target", false)

	// then
	if err == nil || !strings.Contains(err.Error(), "unrecognized session file") {
		t.Fatalf("Import() error = %v", err)
	}
}

func TestValidateArchiveRejectsInconsistentArchives(t *testing.T) {
	valid := testArchive(t, "one")
	for name, change := range map[string]func(*archive.Archive){
		"other provider":    func(a *archive.Archive) { a.Manifest.Provider = "openai-codex" },
		"old version":       func(a *archive.Archive) { a.Manifest.ProviderVersion = "2.1.300" },
		"other workspace":   func(a *archive.Archive) { a.Manifest.Workspace = "/elsewhere" },
		"unknown file":      func(a *archive.Archive) { a.Payloads[2].Path = testSessionID + "/memory/notes.md" },
		"transcript second": func(a *archive.Archive) { a.Payloads[0], a.Payloads[1] = a.Payloads[1], a.Payloads[0] },
	} {
		t.Run(name, func(t *testing.T) {
			changed := valid
			changed.Payloads = append([]archive.Payload(nil), valid.Payloads...)
			change(&changed)

			// when
			err := ValidateArchive(changed)

			// then
			if err == nil {
				t.Fatal("ValidateArchive() succeeded")
			}
		})
	}
}

// register records pid in Claude Code's session registry as having the test
// session open.
func register(t *testing.T, home string, pid int) {
	t.Helper()
	registry := filepath.Join(home, "sessions")
	if err := os.MkdirAll(registry, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"status":"idle"}`, pid, testSessionID)
	if err := os.WriteFile(filepath.Join(registry, fmt.Sprintf("%d.json", pid)), []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
}

// exitedProcess returns the PID of a process that has exited.
func exitedProcess(t *testing.T) int {
	t.Helper()
	process, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exit 0"}, &os.ProcAttr{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	return process.Pid
}

func TestRelocateMovesSavedToolOutputsAndTheProjectFolder(t *testing.T) {
	outputPath := testSessionID + "/tool-results/output.txt"
	sourceFolder := filepath.Join(sourceHome, "projects", projectFolderName(sourceWorkspace))
	source, err := archive.New(archive.Header{
		Provider: Provider, ProviderVersion: "2.1.319", SessionID: testSessionID,
		Workspace: sourceWorkspace, Home: sourceHome, CreatedAt: time.Now(),
	}, []archive.Payload{
		{Path: testSessionID + ".jsonl", Data: lines(message("saved to "+sourceFolder+"/"+outputPath, sourceWorkspace))},
		{Path: outputPath, Data: []byte(sourceWorkspace + "/main.go\n")},
	})
	if err != nil {
		t.Fatal(err)
	}

	// when
	moved, err := Relocate(source, "/source", "/target", "/target-home")

	// then
	if err != nil {
		t.Fatal(err)
	}
	targetFolder := filepath.Join("/target-home", "projects", projectFolderName("/target/project"))
	transcript, _ := moved.Data(testSessionID + ".jsonl")
	output, _ := moved.Data(outputPath)
	if moved.Manifest.Workspace != "/target/project" || moved.Manifest.Home != "/target-home" ||
		!strings.Contains(string(transcript), targetFolder+"/"+outputPath) || string(output) != "/target/project/main.go\n" {
		t.Fatalf("Relocate() = %+v\n%s\n%s", moved.Manifest, transcript, output)
	}
	restored, err := Relocate(moved, "/target", "/source", sourceHome)
	if err != nil {
		t.Fatal(err)
	}
	for index, payload := range restored.Payloads {
		if string(payload.Data) != string(source.Payloads[index].Data) {
			t.Fatalf("moving back changed %s", payload.Path)
		}
	}
}
