package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsolatedImportLifecycle exercises the public CLI while native Codex
// continues history against the local scripted API. The shim only translates
// interactive resume to exec so the test needs no terminal or credentials.
func TestIsolatedImportLifecycle(t *testing.T) {
	binary := buildAgentTeleport(t)
	codexPath := requireCodex(t, binary)
	api := startScriptedResponses(t, nil)
	work := testWorkDir(t)
	base := mkdir(t, filepath.Join(work, "normal-home"))
	origin := mkdir(t, filepath.Join(work, "original-workspace"))
	target := initRepository(t, filepath.Join(work, "target-workspace"))
	api.configure(t, base, false)
	root := codexExec(t, codexPath, base, origin, "", "original conversation")
	fork := forkSession(t, codexPath, base, root)
	packed := filepath.Join(work, "session.teleport")
	run(t, binary, codexPath, base, nil, true, "export", fork, "-o", packed)
	before := historySnapshot(t, base)

	// A normal import cannot replace the ancestor in another workspace.
	run(t, binary, codexPath, base, nil, false, "import", packed, "--cwd", target)
	run(t, binary, codexPath, base, nil, true, "import", packed, "--cwd", target, "--isolated", "--dry-run")
	assertMissing(t, filepath.Join(base, "agent-teleport"))

	// Configuration may explicitly target the primary DB. Isolated launch
	// must override it even though config.toml remains shared by reference.
	skill := mkdir(t, filepath.Join(base, "skills", "isolated-probe"))
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: isolated-probe\ndescription: Isolated runtime skill marker.\n---\nUse the local scripted API.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(base, "config.toml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config = append([]byte(fmt.Sprintf("sqlite_home = %q\n", base)), config...)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	auth := []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"agent-teleport-test-key"}`)
	if err := os.WriteFile(filepath.Join(base, "auth.json"), auth, 0o600); err != nil {
		t.Fatal(err)
	}
	output := run(t, binary, codexPath, base, nil, true, "import", packed, "--cwd", target, "--isolated")
	reference := importReference(t, output)
	assertOutput(t, output, "agent-teleport resume "+reference)
	assertOutput(t, run(t, binary, codexPath, base, nil, true, "import", packed, "--cwd", target, "--isolated"), "already up to date")

	var listed []struct{ ID, Reference, Workspace string }
	if err := json.Unmarshal(run(t, binary, codexPath, base, nil, true, "list", "--all", "--json"), &listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, session := range listed {
		found = found || (session.ID == fork && session.Reference == reference && session.Workspace == target)
	}
	if !found {
		t.Fatalf("isolated import not listed: %+v", listed)
	}

	if after := historySnapshot(t, base); !equalHistory(before, after) {
		t.Fatal("initial isolated import changed primary history")
	}
	// An updated archive must extend the same managed store and keep its handle.
	codexExec(t, codexPath, base, origin, fork, "source update")
	updated := filepath.Join(work, "updated.teleport")
	run(t, binary, codexPath, base, nil, true, "export", fork, "-o", updated)
	update := run(t, binary, codexPath, base, nil, true, "import", updated, "--cwd", target, "--isolated")
	assertOutput(t, update, "updated Codex session")
	if importReference(t, update) != reference {
		t.Fatal("updated archive created another isolated import")
	}
	before = historySnapshot(t, base)
	// Native interrupted appends must still recover on a real managed import.
	// A preliminary dry-run plan would incorrectly block this recovery.
	paths, err := filepath.Glob(filepath.Join(base, "agent-teleport", "imports", "*", "codex", "sessions", "*", "*", "*", "rollout-*-"+fork+".jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("find managed rollout: %v, %v", paths, err)
	}
	original, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0]+".agent-teleport-append", []byte(fmt.Sprintf("%d %d\n", len(original), len(original)+20)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], append(append([]byte{}, original...), []byte("part")...), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, binary, codexPath, base, nil, true, "import", updated, "--cwd", target, "--isolated")
	recovered, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(recovered, original) {
		t.Fatalf("interrupted managed append was not restored: %v", err)
	}
	assertMissing(t, paths[0]+".agent-teleport-append")
	capture := filepath.Join(work, "resumed-home")
	shim := codexResumeShim(t, work, codexPath, capture)
	environment := append(cleanEnvironment(base, shim), "CODEX_SESSION_ID=", "CODEX_THREAD_ID=", "CLAUDE_CODE_SESSION_ID=")
	runIn(t, binary, environment, "", nil, true, "resume", reference)
	actualHome, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	home := strings.TrimSpace(string(actualHome))
	if home == base || !strings.HasPrefix(home, filepath.Join(base, "agent-teleport", "imports")) {
		t.Fatalf("resumed wrong home: %s", home)
	}
	if !strings.Contains(api.latestTexts(), "isolated-probe") {
		t.Fatal("resumed Codex did not load the shared skill")
	}
	if !strings.Contains(api.latestTexts(), "source update") {
		t.Fatal("resumed Codex did not receive the archive update")
	}
	if !strings.Contains(api.latestTexts(), "original conversation") {
		t.Fatal("resumed Codex did not receive the imported history")
	}
	// The selected store also works when agent-teleport runs inside Codex.
	inside := cleanEnvironment(home, codexPath)
	assertListedCWD(t, runIn(t, binary, inside, target, nil, true, "list", "--all", "--json"), fork, target)
	runIn(t, binary, inside, target, nil, true, "doctor", "--json")
	continued := filepath.Join(work, "continued.teleport")
	runIn(t, binary, inside, target, nil, true, "export", fork, "-o", continued)
	if got := decodeArchive(t, continued); got.Manifest.SessionID != fork || got.Manifest.Workspace != target {
		t.Fatal("continued export changed identity or workspace")
	}
	runIn(t, binary, environment, target, nil, true, "save", reference, "--name", "isolated")
	assertStates(t, runIn(t, binary, environment, target, nil, true, "status", "--isolated", "--json"), map[string]string{"isolated": "in-sync"})
	assertOutput(t, runIn(t, binary, environment, target, nil, true, "open", "isolated", "--isolated"), "already up to date")
	assertOutput(t, run(t, binary, codexPath, base, nil, true, "import", packed, "--cwd", target, "--isolated"), "newer than the archive")

	// File-backed credentials are referenced, not copied into the receipt.
	if destination, err := os.Readlink(filepath.Join(home, "auth.json")); err != nil || destination != filepath.Join(base, "auth.json") {
		t.Fatalf("file login is not shared: %s, %v", destination, err)
	}
	runAgent(t, append(cleanEnvironment(home, codexPath), "CODEX_SESSION_ID=", "CODEX_THREAD_ID="), codexPath, "login", "status")
	if after := historySnapshot(t, base); !equalHistory(before, after) {
		t.Fatal("isolated lifecycle changed primary session histories")
	}
	unchanged, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(config, unchanged) {
		t.Fatal("isolated lifecycle changed primary configuration")
	}

	// Exported updates remain ordinary native archives and import into a
	// clean home without a dependency on the managed store or local handle.
	other := mkdir(t, filepath.Join(work, "another-home"))
	api.configure(t, other, false)
	run(t, binary, codexPath, other, nil, true, "import", continued, "--cwd", target)
}

func importReference(t *testing.T, output []byte) string {
	t.Helper()
	for _, word := range strings.Fields(string(output)) {
		if strings.HasPrefix(word, "iso-") {
			return strings.TrimRight(word, ")")
		}
	}
	t.Fatalf("import returned no isolated reference: %s", output)
	return ""
}

func codexResumeShim(t *testing.T, work, binary, capture string) string {
	t.Helper()
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	script := "#!/bin/bash\nargs=()\nwhile [ $# -gt 0 ]; do\n if [ \"$1\" = resume ]; then\n  shift\n  printf '%s\\n' \"$CODEX_HOME\" > " + quote(capture) + "\n  exec " + quote(binary) + " \"${args[@]}\" exec --json --skip-git-repo-check --sandbox workspace-write resume \"$1\" 'continued isolated conversation'\n fi\n args+=(\"$1\")\n shift\ndone\nexec " + quote(binary) + " \"${args[@]}\"\n"
	path := filepath.Join(work, "codex-resume-shim")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func historySnapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, name := range []string{"sessions", "archived_sessions"} {
		err := filepath.WalkDir(filepath.Join(home, name), func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".jsonl") {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				result[strings.TrimPrefix(path, home)] = string(data)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func equalHistory(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for path, data := range a {
		if b[path] != data {
			return false
		}
	}
	return true
}
