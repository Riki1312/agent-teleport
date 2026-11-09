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

	"github.com/Riki1312/agent-teleport/internal/appserver"
	"github.com/Riki1312/agent-teleport/internal/bundle"
	"github.com/Riki1312/agent-teleport/internal/codex"
)

func TestTwoHomeTransfer(t *testing.T) {
	codexPath, compatibility := requireCodex(t)
	binary := buildCXPort(t)
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
	fixture := loadFixture(t, compatibility, sourceHome, sourceCWD)

	assertListedCWD(t, run(t, binary, codexPath, sourceHome, nil, true,
		"list", "--all", "--json"), fixture.Meta.ID, sourceCWD)

	archivePath := filepath.Join(work, "session.cxport")
	run(t, binary, codexPath, sourceHome, nil, true,
		"export", "--all", fixture.Meta.ID, "-o", archivePath)
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := bundle.Read(archiveFile)
	archiveFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(archive.Rollout, fixture.Data) {
		t.Fatal("export did not preserve fixture rollout bytes")
	}
	run(t, binary, codexPath, sourceHome, nil, true, "inspect", archivePath, "--json")

	run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", targetAlias)
	assertListedCWD(t, run(t, binary, codexPath, targetHome, nil, true,
		"list", "--all", "--json"), fixture.Meta.ID, targetCWD)
	run(t, binary, codexPath, targetHome, nil, false,
		"import", archivePath, "--cwd", targetCWD)
	run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", targetCWD, "--if-exists", "skip")
	run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", replacementCWD, "--if-exists", "replace")
	assertListedCWD(t, run(t, binary, codexPath, targetHome, nil, true,
		"list", "--all", "--json"), fixture.Meta.ID, replacementCWD)

	destination := compatibility.RolloutPath(targetHome, fixture.Meta)
	if err := os.Link(destination, destination+".cxport-rollback"); err != nil {
		t.Fatalf("simulate interrupted replacement: %v", err)
	}
	run(t, binary, codexPath, targetHome, nil, true,
		"import", archivePath, "--cwd", replacementCWD, "--if-exists", "replace")
	assertMissing(t, destination+".cxport-rollback")
	assertMissing(t, destination+".cxport-stage")

	exported := run(t, binary, codexPath, sourceHome, nil, true,
		"export", "--all", fixture.Meta.ID, "-o", "-")
	run(t, binary, codexPath, pipeHome, exported, true,
		"import", "-", "--cwd", pipeCWD)
	assertListedCWD(t, run(t, binary, codexPath, pipeHome, nil, true,
		"list", "--all", "--json"), fixture.Meta.ID, pipeCWD)

	archiveSession(t, codexPath, pipeHome, fixture.Meta.ID)
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
	resumed, err := client.Resume(fixture.Meta.ID, replacementCWD)
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
	Data []byte
	Meta codex.SessionMeta
}

func loadFixture(t *testing.T, compatibility codex.Compatibility, home, cwd string) rolloutFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(
		moduleRoot(t),
		"testdata", "codex", compatibility.Version, "rollout.jsonl",
	))
	if err != nil {
		t.Fatal(err)
	}
	data, meta, err := compatibility.RemapWorkspace(data, cwd)
	if err != nil {
		t.Fatal(err)
	}
	path := compatibility.RolloutPath(home, meta)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return rolloutFixture{Data: data, Meta: meta}
}

func requireCodex(t *testing.T) (string, codex.Compatibility) {
	t.Helper()
	path, err := exec.LookPath("codex")
	if err != nil {
		skipOrFail(t, "Codex CLI is not installed")
	}
	version, err := codex.Version(path)
	if err != nil {
		skipOrFail(t, err.Error())
	}
	compatibility, err := codex.LookupCompatibility(version)
	if err != nil {
		skipOrFail(t, err.Error())
	}
	return path, compatibility
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
		if !strings.HasPrefix(value, "CODEX_HOME=") && !strings.HasPrefix(value, "CXPORT_CODEX=") {
			environment = append(environment, value)
		}
	}
	return append(environment, "CODEX_HOME="+home, "CXPORT_CODEX="+codexPath)
}

func assertListedCWD(t *testing.T, output []byte, sessionID, want string) {
	t.Helper()
	var threads []struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	}
	if err := json.Unmarshal(output, &threads); err != nil {
		t.Fatalf("decode list output: %v", err)
	}
	for _, thread := range threads {
		if thread.ID == sessionID {
			if thread.CWD != want {
				t.Fatalf("listed cwd = %q, want %q", thread.CWD, want)
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
