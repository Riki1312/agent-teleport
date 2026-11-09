package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/codex"
	"github.com/Riki1312/agent-teleport/internal/codex/appserver"
)

func TestTwoHomeTransfer(t *testing.T) {
	binary := buildAgentTeleport(t)
	codexPath := requireCodex(t, binary)
	work := testWorkDir(t)
	sourceHome := mkdir(t, filepath.Join(work, "source-home"))
	sourceCWD := mkdir(t, filepath.Join(work, "source-workspace"))
	targetHome := mkdir(t, filepath.Join(work, "target-home"))
	targetCWD := mkdir(t, filepath.Join(work, "target-workspace"))
	targetAlias := filepath.Join(work, "target-alias")
	if err := os.Symlink(targetCWD, targetAlias); err != nil {
		t.Fatal(err)
	}
	replacementCWD := mkdir(t, filepath.Join(work, "replacement-workspace"))
	pipeHome := mkdir(t, filepath.Join(work, "pipe-home"))
	pipeCWD := mkdir(t, filepath.Join(work, "pipe-workspace"))
	fixture := seedFixture(t, binary, codexPath, work, sourceHome, sourceCWD)

	assertListedCWD(t, run(t, binary, codexPath, sourceHome, nil, true,
		"list", "--all", "--json"), fixture.ID, sourceCWD)

	archivePath := filepath.Join(work, "session.teleport")
	run(t, binary, codexPath, sourceHome, nil, true,
		"export", "--all", fixture.ID, "-o", archivePath)
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	exportedArchive, err := archive.Decode(archiveFile)
	_ = archiveFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if rollout, _ := exportedArchive.Data(fixture.Name); !bytes.Equal(rollout, fixture.Data) {
		t.Fatal("export did not preserve the stored rollout bytes")
	}
	run(t, binary, codexPath, sourceHome, nil, true, "inspect", archivePath, "--json")

	shortPath := filepath.Join(work, "short"+archive.Extension)
	writeArchive(t, shortPath, fixture, sourceCWD, completedPrefix(t, fixture.Data, 2))
	divergedPath := filepath.Join(work, "diverged"+archive.Extension)
	writeArchive(t, divergedPath, fixture, sourceCWD, divergedCopy(t, fixture.Data))

	assertOutput(t, run(t, binary, codexPath, targetHome, nil, true,
		"import", shortPath, "--cwd", targetAlias), "imported")
	assertListedCWD(t, run(t, binary, codexPath, targetHome, nil, true,
		"list", "--all", "--json"), fixture.ID, targetCWD)
	assertOutput(t, run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", targetCWD), "updated")
	assertOutput(t, run(t, binary, codexPath, targetHome, nil, true,
		"import", shortPath, "--cwd", targetCWD), "newer than the archive")
	assertOutput(t, run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", targetCWD), "already up to date")
	run(t, binary, codexPath, targetHome, nil, false,
		"import", divergedPath, "--cwd", targetCWD)
	run(t, binary, codexPath, targetHome, nil, false,
		"import", archivePath, "--cwd", replacementCWD)
	assertOutput(t, run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", replacementCWD, "--replace"), "replaced")
	assertListedCWD(t, run(t, binary, codexPath, targetHome, nil, true,
		"list", "--all", "--json"), fixture.ID, replacementCWD)

	destination := filepath.Join(targetHome, strings.TrimPrefix(fixture.Path, sourceHome))
	if err := os.Link(destination, destination+".agent-teleport-rollback"); err != nil {
		t.Fatalf("simulate interrupted replacement: %v", err)
	}
	assertOutput(t, run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", replacementCWD), "already up to date")
	assertMissing(t, destination+".agent-teleport-rollback")
	assertMissing(t, destination+".agent-teleport-stage")

	exported := run(t, binary, codexPath, sourceHome, nil, true,
		"export", "--all", fixture.ID, "-o", "-")
	run(t, binary, codexPath, pipeHome, exported, true,
		"import", "-", "--cwd", pipeCWD)
	assertListedCWD(t, run(t, binary, codexPath, pipeHome, nil, true,
		"list", "--all", "--json"), fixture.ID, pipeCWD)

	archiveSession(t, codexPath, pipeHome, fixture.ID)
	archived := filepath.Join(pipeHome, "archived_sessions", filepath.Base(destination))
	run(t, binary, codexPath, pipeHome, nil, false,
		"import", archivePath, "--cwd", pipeCWD)
	run(t, binary, codexPath, pipeHome, nil, false,
		"import", archivePath, "--cwd", pipeCWD, "--replace")
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("import changed an archived session: %v", err)
	}

	forkID := forkSession(t, codexPath, sourceHome, fixture.ID)
	forkArchivePath := filepath.Join(work, "fork"+archive.Extension)
	run(t, binary, codexPath, sourceHome, nil, true,
		"export", "--all", forkID, "-o", forkArchivePath)
	if files := len(decodeArchive(t, forkArchivePath).Manifest.Files); files != 2 {
		t.Fatalf("fork archive has %d files, want the ancestor and the fork", files)
	}
	forkHome := mkdir(t, filepath.Join(work, "fork-home"))
	forkCWD := mkdir(t, filepath.Join(work, "fork-workspace-with-a-longer-name"))
	assertOutput(t, run(t, binary, codexPath, forkHome, nil, true,
		"import", forkArchivePath, "--cwd", forkCWD), "imported")
	assertOutput(t, run(t, binary, codexPath, forkHome, nil, true,
		"import", forkArchivePath, "--cwd", forkCWD), "already up to date")
	assertResumed(t, codexPath, forkHome, forkID, forkCWD, fixture)
	run(t, binary, codexPath, forkHome, nil, false,
		"import", forkArchivePath, "--cwd", pipeCWD, "--replace")
	assertOutput(t, run(t, binary, codexPath, targetHome, nil, true,
		"import", forkArchivePath, "--cwd", replacementCWD), "imported")
	assertResumed(t, codexPath, targetHome, forkID, replacementCWD, fixture)

	client, err := appserver.Start(context.Background(), codexPath, targetHome)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	resumed, err := client.Resume(fixture.ID, replacementCWD)
	if err != nil {
		t.Fatalf("resume imported fixture: %v", err)
	}
	if resumed.CWD != replacementCWD {
		t.Fatalf("resumed cwd = %q, want %q", resumed.CWD, replacementCWD)
	}
	if want := bytes.Count(fixture.Data, []byte(`"type":"task_started"`)); len(resumed.Turns) != want {
		t.Fatalf("resumed session has %d turns, want %d", len(resumed.Turns), want)
	}
}

func archiveSession(t *testing.T, codexPath, home, sessionID string) {
	t.Helper()
	client, err := appserver.Start(context.Background(), codexPath, home)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Archive(sessionID); err != nil {
		t.Fatalf("archive session: %v", err)
	}
}

func forkSession(t *testing.T, codexPath, home, sessionID string) string {
	t.Helper()
	client, err := appserver.Start(context.Background(), codexPath, home)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	fork, err := client.Fork(sessionID)
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	return fork.ID
}

func decodeArchive(t *testing.T, path string) archive.Archive {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	decoded, err := archive.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

// assertResumed resumes sessionID in an isolated App Server and requires every
// turn of the fixture history in workspace.
func assertResumed(t *testing.T, codexPath, home, sessionID, workspace string, fixture rolloutFixture) {
	t.Helper()
	client, err := appserver.Start(context.Background(), codexPath, home)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	resumed, err := client.Resume(sessionID, "")
	if err != nil {
		t.Fatalf("resume %s: %v", sessionID, err)
	}
	want := bytes.Count(fixture.Data, []byte(`"type":"task_started"`))
	if resumed.CWD != workspace || len(resumed.Turns) != want {
		t.Fatalf("resumed %s has %d turns in %q, want %d in %q", sessionID, len(resumed.Turns), resumed.CWD, want, workspace)
	}
}

type rolloutFixture struct {
	ID      string
	Name    string
	Version string
	Path    string
	Data    []byte
}

// seedFixture imports the sanitized fixture into home through the CLI, mapped
// to cwd, and returns the rollout Codex stored.
func seedFixture(t *testing.T, binary, codexPath, work, home, cwd string) rolloutFixture {
	t.Helper()
	name := "rollout-2026-11-09T01-37-58-01a1bdbd-8748-7a72-96ac-80bbb811e9c9.jsonl"
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "testdata", "codex", name))
	if err != nil {
		t.Fatal(err)
	}
	firstLine, _, _ := bytes.Cut(data, []byte{'\n'})
	var first struct {
		Payload struct {
			ID         string `json:"id"`
			CWD        string `json:"cwd"`
			CLIVersion string `json:"cli_version"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(firstLine, &first); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(work, "fixture"+archive.Extension)
	fixture := rolloutFixture{ID: first.Payload.ID, Name: name, Version: first.Payload.CLIVersion}
	writeArchive(t, archivePath, fixture, first.Payload.CWD, data)
	run(t, binary, codexPath, home, nil, true, "import", archivePath, "--cwd", cwd)
	paths, err := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*-"+first.Payload.ID+".jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("locate seeded rollout: %q, %v", paths, err)
	}
	stored, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	fixture.Path, fixture.Data = paths[0], stored
	return fixture
}

// writeArchive packs rollout as the fixture session's only payload.
func writeArchive(t *testing.T, path string, fixture rolloutFixture, workspace string, rollout []byte) {
	t.Helper()
	created, err := archive.New(archive.Header{
		Provider:        codex.Provider,
		ProviderVersion: fixture.Version,
		SessionID:       fixture.ID,
		Workspace:       workspace,
		CreatedAt:       time.Now(),
	}, []archive.Payload{{Path: fixture.Name, Data: rollout}})
	if err != nil {
		t.Fatal(err)
	}
	if err := created.WriteFile(path); err != nil {
		t.Fatal(err)
	}
}

// completedPrefix returns the rollout up to and including its turns-th
// completed turn.
func completedPrefix(t *testing.T, rollout []byte, turns int) []byte {
	t.Helper()
	end := 0
	for line := range bytes.Lines(rollout) {
		end += len(line)
		if bytes.Contains(line, []byte(`"type":"task_complete"`)) {
			if turns--; turns == 0 {
				return rollout[:end]
			}
		}
	}
	t.Fatal("fixture has too few completed turns")
	return nil
}

// divergedCopy returns the rollout with its first completed turn rewritten,
// simulating a copy that continued differently on another machine.
func divergedCopy(t *testing.T, rollout []byte) []byte {
	t.Helper()
	original := []byte("sanitized fixture text")
	if !bytes.Contains(rollout, original) {
		t.Fatal("fixture has no sanitized text to change")
	}
	return bytes.Replace(rollout, original, []byte("sanitized fixture TEXT"), 1)
}

func assertOutput(t *testing.T, output []byte, want string) {
	t.Helper()
	if !bytes.Contains(output, []byte(want)) {
		t.Fatalf("output %q does not contain %q", output, want)
	}
}

// requireCodex returns the installed Codex binary when the built CLI reports it
// as supported.
func requireCodex(t *testing.T, binary string) string {
	t.Helper()
	path, err := exec.LookPath("codex")
	if err != nil {
		skipOrFail(t, "Codex CLI is not installed")
	}
	var report struct {
		CodexVersion string `json:"codex_version"`
		Supported    bool   `json:"supported"`
		Error        string `json:"error"`
	}
	output := run(t, binary, path, t.TempDir(), nil, true, "doctor", "--json")
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	if !report.Supported {
		skipOrFail(t, "Codex "+report.CodexVersion+" is unsupported "+report.Error)
	}
	return path
}

func skipOrFail(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("AGENT_TELEPORT_E2E_REQUIRED") == "1" {
		t.Fatal(reason)
	}
	t.Skip(reason)
}

func buildAgentTeleport(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "agent-teleport")
	command := exec.Command("go", "build", "-o", binary, "./cmd/agent-teleport")
	command.Dir = moduleRoot(t)
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "go-build"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build agent-teleport: %v: %s", err, output)
	}
	return binary
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate E2E test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func testWorkDir(t *testing.T) string {
	t.Helper()
	path := t.TempDir()
	if value := os.Getenv("AGENT_TELEPORT_E2E_WORKDIR"); value != "" {
		path = mkdir(t, value)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func run(
	t *testing.T,
	binary, codexPath, home string,
	stdin []byte,
	wantSuccess bool,
	args ...string,
) []byte {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Env = cleanEnvironment(home, codexPath)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if wantSuccess && err != nil {
		t.Fatalf("agent-teleport %s failed: %v: %s", strings.Join(args, " "), err, stderr.Bytes())
	}
	if !wantSuccess && err == nil {
		t.Fatalf("agent-teleport %s unexpectedly succeeded", strings.Join(args, " "))
	}
	return stdout.Bytes()
}

func cleanEnvironment(home, codexPath string) []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "CODEX_HOME=") && !strings.HasPrefix(value, codex.BinaryEnvironment+"=") {
			environment = append(environment, value)
		}
	}
	return append(environment, "CODEX_HOME="+home, codex.BinaryEnvironment+"="+codexPath)
}

func assertListedCWD(t *testing.T, output []byte, sessionID, want string) {
	t.Helper()
	var threads []struct {
		ID        string `json:"id"`
		Workspace string `json:"workspace"`
	}
	if err := json.Unmarshal(output, &threads); err != nil {
		t.Fatalf("decode list output: %v", err)
	}
	for _, thread := range threads {
		if thread.ID == sessionID {
			if thread.Workspace != want {
				t.Fatalf("listed workspace = %q, want %q", thread.Workspace, want)
			}
			return
		}
	}
	t.Fatalf("fixture session not listed: %s", output)
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be absent, got %v", path, err)
	}
}
