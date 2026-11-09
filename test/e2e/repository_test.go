package e2e_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Riki1312/agent-teleport/internal/claude"
	"github.com/Riki1312/agent-teleport/internal/codex"
)

// TestClaudeRepositorySession saves a Claude Code session in a Git
// repository, opens it in a clone with another agent home, and keeps both
// copies in step through the repository.
func TestClaudeRepositorySession(t *testing.T) {
	binary := buildAgentTeleport(t)
	claudePath := requireClaude(t, binary)
	api := startScriptedAPI(t)
	work := testWorkDir(t)
	source := claudeHome{binary: claudePath, home: mkdir(t, filepath.Join(work, "source-home")), api: api}
	target := claudeHome{binary: claudePath, home: mkdir(t, filepath.Join(work, "target-home")), api: api}
	origin := initRepository(t, filepath.Join(work, "origin"))
	sessionID := source.prompt(t, origin, "", "spawn a child")
	source.prompt(t, origin, sessionID, "print a large output")

	atOrigin := func(wantSuccess bool, args ...string) []byte {
		return runIn(t, binary, source.agentTeleportEnvironment(), origin, nil, wantSuccess, args...)
	}
	assertOutput(t, atOrigin(true, "save", "--last", "--name", "spawn"), "saved Claude Code session")
	saved := filepath.Join(origin, ".agent-teleport", "sessions", "spawn")
	assertMentionsNone(t, saved, origin, source.home)
	assertStates(t, atOrigin(true, "status", "--json"), map[string]string{"spawn": "in-sync"})
	assertOutput(t, atOrigin(true, "save", "--last"), "already saved as")
	git(t, origin, "add", ".agent-teleport")
	git(t, origin, "commit", "--quiet", "-m", "Save the session")

	clone := filepath.Join(work, "clones", "a-clone-with-a-longer-path")
	git(t, work, "clone", "--quiet", origin, clone)
	clone, _ = filepath.EvalSymlinks(clone)
	atClone := func(wantSuccess bool, args ...string) []byte {
		return runIn(t, binary, target.agentTeleportEnvironment(), clone, nil, wantSuccess, args...)
	}
	assertStates(t, atClone(true, "status", "--json"), map[string]string{"spawn": "not-opened"})
	assertOutput(t, atClone(true, "__complete", "open", ""), "spawn")
	assertOutput(t, atClone(true, "open", "spawn"), "imported Claude Code session")
	assertStates(t, atClone(true, "status", "--json"), map[string]string{"spawn": "in-sync"})
	opened := target.request(t, clone, "--resume", sessionID, "continue in the clone")
	opened.requireTexts(t, "spawning", "child result", "reply")
	opened.requireMoved(t, clone, origin, source.home)

	// Claude Code saves its own session; the save holds the session up to the
	// turn that runs it.
	api.runOn("save from inside", claude.BinaryEnvironment+"="+claudePath+" "+codex.BinaryEnvironment+"="+missingCommand+" "+binary+" save")
	target.prompt(t, clone, sessionID, "save from inside")
	insideSave, err := os.ReadFile(filepath.Join(clone, ".agent-teleport", "sessions", "spawn", sessionID+".jsonl"))
	if err != nil || !bytes.Contains(insideSave, []byte("continue in the clone")) || bytes.Contains(insideSave, []byte("save from inside")) {
		t.Fatalf("the save from inside the session holds the wrong turns: %v", err)
	}

	assertStates(t, atClone(true, "status", "--json"), map[string]string{"spawn": "local-ahead"})
	assertOutput(t, atClone(true, "save", "--last"), "saved Claude Code session")
	git(t, clone, "commit", "--quiet", "-am", "Continue the session")
	git(t, origin, "pull", "--quiet", clone, "HEAD")
	assertStates(t, atOrigin(true, "status", "--json"), map[string]string{"spawn": "repository-ahead"})
	atOrigin(false, "save", "--last")
	assertOutput(t, atOrigin(true, "open", "spawn"), "updated Claude Code session")
	assertStates(t, atOrigin(true, "status", "--json"), map[string]string{"spawn": "in-sync"})

	// Saving refuses a likely credential unless it is accepted, and scans only
	// what the save adds.
	source.prompt(t, origin, sessionID, "here is -----BEGIN OPENSSH "+"PRIVATE KEY----- for you")
	atOrigin(false, "save", "--last")
	assertOutput(t, atOrigin(true, "save", "--last", "--allow-secrets"), "saved Claude Code session")
	source.prompt(t, origin, sessionID, "a clean prompt")
	assertOutput(t, atOrigin(true, "save", "--last"), "saved Claude Code session")
	git(t, origin, "commit", "--quiet", "-am", "Continue on the origin")

	target.prompt(t, clone, sessionID, "diverge in the clone")
	git(t, clone, "pull", "--quiet", "--no-rebase", origin, "HEAD")
	assertStates(t, atClone(true, "status", "--json"), map[string]string{"spawn": "diverged"})
	atClone(false, "save", "--last")
	// Run inside Claude Code, open explains how to switch to the session in the
	// chat, and refuses to start a second agent with --resume.
	insideClaude := append(target.agentTeleportEnvironment(), "CLAUDE_CODE_SESSION_ID="+sessionID)
	runIn(t, binary, insideClaude, clone, nil, false, "open", "spawn", "--replace", "--resume")
	assertOutput(t, runIn(t, binary, insideClaude, clone, nil, true, "open", "spawn", "--replace"), "type /resume "+sessionID+" in Claude Code")
	assertStates(t, atClone(true, "status", "--json"), map[string]string{"spawn": "in-sync"})
}

// TestCodexRepositorySession saves the sanitized Codex fixture in a Git
// repository and opens it in a clone with another Codex home.
func TestCodexRepositorySession(t *testing.T) {
	binary := buildAgentTeleport(t)
	codexPath := requireCodex(t, binary)
	work := testWorkDir(t)
	sourceHome := mkdir(t, filepath.Join(work, "source-home"))
	targetHome := mkdir(t, filepath.Join(work, "target-home"))
	origin := initRepository(t, filepath.Join(work, "origin"))
	fixture := seedFixture(t, binary, codexPath, work, sourceHome, origin)

	atOrigin := func(wantSuccess bool, args ...string) []byte {
		return runIn(t, binary, cleanEnvironment(sourceHome, codexPath), origin, nil, wantSuccess, args...)
	}
	assertOutput(t, atOrigin(true, "save", fixture.ID, "--name", "fixture"), "saved Codex session")
	assertMentionsNone(t, filepath.Join(origin, ".agent-teleport", "sessions", "fixture"), origin, sourceHome)
	assertStates(t, atOrigin(true, "status", "--json"), map[string]string{"fixture": "in-sync"})
	git(t, origin, "add", ".agent-teleport")
	git(t, origin, "commit", "--quiet", "-m", "Save the session")

	clone := filepath.Join(work, "clones", "a-clone-with-a-longer-path")
	git(t, work, "clone", "--quiet", origin, clone)
	clone, _ = filepath.EvalSymlinks(clone)
	atClone := func(wantSuccess bool, args ...string) []byte {
		return runIn(t, binary, cleanEnvironment(targetHome, codexPath), clone, nil, wantSuccess, args...)
	}
	assertStates(t, atClone(true, "status", "--json"), map[string]string{"fixture": "not-opened"})
	assertOutput(t, atClone(true, "open", "fixture"), "imported Codex session")
	assertStates(t, atClone(true, "status", "--json"), map[string]string{"fixture": "in-sync"})
	assertResumed(t, codexPath, targetHome, fixture.ID, clone, fixtureTurns(fixture.Data))
}

// initRepository creates a Git repository with one commit and returns its
// canonical root.
func initRepository(t *testing.T, path string) string {
	t.Helper()
	mkdir(t, path)
	git(t, path, "init", "--quiet", "--initial-branch", "main")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, path, "add", "README.md")
	git(t, path, "commit", "--quiet", "-m", "Start")
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// git runs Git in dir without the user's configuration, so signing and hooks
// never apply.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=agent-teleport", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=agent-teleport", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

// assertMentionsNone requires no file under dir to mention any of paths.
func assertMentionsNone(t *testing.T, dir string, paths ...string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, machinePath := range paths {
			if bytes.Contains(data, []byte(machinePath)) {
				t.Errorf("%s mentions %s", strings.TrimPrefix(path, dir), machinePath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// assertStates requires status --json output to report want, by name.
func assertStates(t *testing.T, output []byte, want map[string]string) {
	t.Helper()
	var entries []struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(output, &entries); err != nil {
		t.Fatalf("decode status output: %v", err)
	}
	got := map[string]string{}
	for _, entry := range entries {
		got[entry.Name] = entry.State
		if entry.Error != "" {
			got[entry.Name] += ": " + entry.Error
		}
	}
	for name, state := range want {
		if got[name] != state {
			t.Fatalf("status of %s = %q, want %q", name, got[name], state)
		}
	}
}

// TestCodexSaveFromInsideASession records a session with the native Codex CLI
// against a scripted Responses API, has Codex run agent-teleport save inside
// its workspace sandbox, and opens the saved session in a clone.
func TestCodexSaveFromInsideASession(t *testing.T) {
	binary := buildAgentTeleport(t)
	codexPath := requireCodex(t, binary)
	work := testWorkDir(t)
	origin := initRepository(t, filepath.Join(work, "origin"))
	api := startScriptedResponses(t, map[string]string{
		"save this session": claude.BinaryEnvironment + "=" + missingCommand + " " + binary + " save --name native",
	})
	sourceHome := mkdir(t, filepath.Join(work, "source-home"))
	api.configure(t, sourceHome)

	sessionID := codexExec(t, codexPath, sourceHome, origin, "", "start the session")
	codexExec(t, codexPath, sourceHome, origin, sessionID, "save this session")

	// The save holds the session up to the turn that ran it.
	saved := filepath.Join(origin, ".agent-teleport", "sessions", "native")
	rollouts, err := filepath.Glob(filepath.Join(saved, "rollout-*.jsonl"))
	if err != nil || len(rollouts) != 1 {
		t.Fatalf("saved rollouts = %q, %v; tool output: %s", rollouts, err, api.toolOutputs())
	}
	if data, _ := os.ReadFile(rollouts[0]); fixtureTurns(data) != 1 {
		t.Fatalf("saved session has %d turns, want the 1 completed before the save", fixtureTurns(data))
	}
	assertMentionsNone(t, saved, origin, sourceHome)
	assertStates(t, runIn(t, binary, cleanEnvironment(sourceHome, codexPath), origin, nil, true, "status", "--json"), map[string]string{"native": "local-ahead"})

	git(t, origin, "add", ".agent-teleport")
	git(t, origin, "commit", "--quiet", "-m", "Save the session")
	clone := filepath.Join(work, "clone")
	git(t, work, "clone", "--quiet", origin, clone)
	clone, _ = filepath.EvalSymlinks(clone)
	// Codex resumes a session only with its model provider configured.
	targetHome := mkdir(t, filepath.Join(work, "target-home"))
	api.configure(t, targetHome)
	assertOutput(t, runIn(t, binary, cleanEnvironment(targetHome, codexPath), clone, nil, true, "open", "native"), "imported Codex session")
	assertResumed(t, codexPath, targetHome, sessionID, clone, 1)
}
