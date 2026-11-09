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
	binary := buildCXPort(t)
	codexPath, version := requireCodex(t, binary)
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
	fixture := seedFixture(t, binary, codexPath, version, work, sourceHome, sourceCWD)

	assertListedCWD(t, run(t, binary, codexPath, sourceHome, nil, true,
		"list", "--all", "--json"), fixture.ID, sourceCWD)

	archivePath := filepath.Join(work, "session.cxport")
	run(t, binary, codexPath, sourceHome, nil, true,
		"export", "--all", fixture.ID, "-o", archivePath)
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	exportedArchive, err := archive.Decode(archiveFile)
	archiveFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if rollout, _ := exportedArchive.Payload("rollout.jsonl"); !bytes.Equal(rollout, fixture.Data) {
		t.Fatal("export did not preserve the stored rollout bytes")
	}
	run(t, binary, codexPath, sourceHome, nil, true, "inspect", archivePath, "--json")

	run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", targetAlias)
	assertListedCWD(t, run(t, binary, codexPath, targetHome, nil, true,
		"list", "--all", "--json"), fixture.ID, targetCWD)
	run(t, binary, codexPath, targetHome, nil, false,
		"import", archivePath, "--cwd", targetCWD)
	run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", targetCWD, "--if-exists", "skip")
	run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", replacementCWD, "--if-exists", "replace")
	assertListedCWD(t, run(t, binary, codexPath, targetHome, nil, true,
		"list", "--all", "--json"), fixture.ID, replacementCWD)

	destination := filepath.Join(targetHome, strings.TrimPrefix(fixture.Path, sourceHome))
	if err := os.Link(destination, destination+".cxport-rollback"); err != nil {
		t.Fatalf("simulate interrupted replacement: %v", err)
	}
	run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", replacementCWD, "--if-exists", "replace")
	assertMissing(t, destination+".cxport-rollback")
	assertMissing(t, destination+".cxport-stage")

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
		"import", archivePath, "--cwd", pipeCWD, "--if-exists", "replace")
	run(t, binary, codexPath, pipeHome, nil, true,
		"import", archivePath, "--cwd", pipeCWD, "--if-exists", "skip")
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("import changed an archived session: %v", err)
	}

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

type rolloutFixture struct {
	ID   string
	Path string
	Data []byte
}

// seedFixture imports the sanitized fixture for version into home through the
// CLI, mapped to cwd, and returns the rollout Codex stored.
func seedFixture(t *testing.T, binary, codexPath, version, work, home, cwd string) rolloutFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "testdata", "codex", version, "rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	firstLine, _, _ := bytes.Cut(data, []byte{'\n'})
	var first struct {
		Payload struct {
			ID  string `json:"id"`
			CWD string `json:"cwd"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(firstLine, &first); err != nil {
		t.Fatal(err)
	}
	fixtureArchive, err := archive.New(archive.Header{
		Provider:        codex.Provider,
		ProviderVersion: version,
		SessionID:       first.Payload.ID,
		Workspace:       first.Payload.CWD,
		CreatedAt:       time.Now(),
	}, []archive.Payload{{Path: "rollout.jsonl", Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(work, "fixture"+archive.Extension)
	if err := fixtureArchive.WriteFile(archivePath); err != nil {
		t.Fatal(err)
	}
	run(t, binary, codexPath, home, nil, true, "import", archivePath, "--cwd", cwd)
	paths, err := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*-"+first.Payload.ID+".jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("locate seeded rollout: %q, %v", paths, err)
	}
	stored, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	return rolloutFixture{ID: first.Payload.ID, Path: paths[0], Data: stored}
}

// requireCodex returns the installed Codex binary and version when the built
// CLI reports it as supported.
func requireCodex(t *testing.T, binary string) (string, string) {
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
	return path, report.CodexVersion
}

func skipOrFail(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("CXPORT_E2E_REQUIRED") == "1" {
		t.Fatal(reason)
	}
	t.Skip(reason)
}

func buildCXPort(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "cxport")
	command := exec.Command("go", "build", "-o", binary, "./cmd/cxport")
	command.Dir = moduleRoot(t)
	command.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "go-build"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build cxport: %v: %s", err, output)
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
	if value := os.Getenv("CXPORT_E2E_WORKDIR"); value != "" {
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
		t.Fatalf("cxport %s failed: %v: %s", strings.Join(args, " "), err, stderr.Bytes())
	}
	if !wantSuccess && err == nil {
		t.Fatalf("cxport %s unexpectedly succeeded", strings.Join(args, " "))
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
