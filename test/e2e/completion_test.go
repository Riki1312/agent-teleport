package e2e_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBashCompletion loads the bash script and completes a command and the
// files of an archive argument.
func TestBashCompletion(t *testing.T) {
	binary := buildAgentTeleport(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "session.teleport"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	complete := func(words ...string) string {
		script := `eval "$(agent-teleport completion bash)"
COMP_WORDS=(agent-teleport ` + strings.Join(words, " ") + `)
COMP_CWORD=` + string(rune('0'+len(words))) + `
_agent_teleport
printf '%s\n' "${COMPREPLY[@]}"`
		return runShell(t, "bash", binary, dir, script)
	}

	// when, then
	if got := complete("exp"); got != "export" {
		t.Fatalf("completing exp = %q", got)
	}
	if got := complete("import", "sess"); got != "session.teleport" {
		t.Fatalf("completing an archive = %q", got)
	}
	if got := complete("save", "--allow"); got != "--allow-secrets" {
		t.Fatalf("completing an option = %q", got)
	}
}

// TestZshCompletion requires the zsh script to parse and to register its
// completion function.
func TestZshCompletion(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("zsh is not installed")
	}
	binary := buildAgentTeleport(t)
	script := `autoload -U compinit && compinit -u -D
source <(agent-teleport completion zsh)
print -r -- ${_comps[agent-teleport]}`

	// when
	registered := runShell(t, "zsh", binary, t.TempDir(), script)

	// then
	if registered != "_agent_teleport" {
		t.Fatalf("zsh completion registers %q", registered)
	}
}

// runShell runs script in shell with agent-teleport on the path, in dir.
func runShell(t *testing.T, shell, binary, dir, script string) string {
	t.Helper()
	command := exec.Command(shell, "-c", script)
	command.Dir = dir
	command.Env = append(os.Environ(), "PATH="+filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+t.TempDir())
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v: %s", shell, err, output)
	}
	return strings.TrimSpace(string(output))
}
