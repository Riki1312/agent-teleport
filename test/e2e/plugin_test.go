package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// skillName is how both agents name the plugin's skill.
const skillName = "agent-teleport:teleport"

// TestPluginInstallsInClaudeCode installs the repository's plugin
// marketplace into an isolated Claude Code home and requires Claude Code to
// offer the skill.
func TestPluginInstallsInClaudeCode(t *testing.T) {
	claudePath := requireClaude(t, buildAgentTeleport(t))
	home := claudeHome{binary: claudePath, home: t.TempDir(), api: startScriptedAPI(t)}
	runAgent(t, home.environment(), claudePath, "plugin", "marketplace", "add", moduleRoot(t))
	runAgent(t, home.environment(), claudePath, "plugin", "install", "agent-teleport@agent-teleport")

	// when
	request := home.request(t, mkdir(t, filepath.Join(t.TempDir(), "workspace")), "hello")

	// then
	offered := false
	for _, text := range append(request.texts(), systemTexts(request)...) {
		offered = offered || strings.Contains(text, skillName)
	}
	if !offered {
		t.Fatalf("Claude Code does not offer %s", skillName)
	}
}

// TestPluginInstallsInCodex installs the same marketplace into an isolated
// Codex home and requires Codex to offer the skill.
func TestPluginInstallsInCodex(t *testing.T) {
	codexPath := requireCodex(t, buildAgentTeleport(t))
	api := startScriptedResponses(t, nil)
	home := t.TempDir()
	api.configure(t, home, true)
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(home, "user"), "CODEX_HOME=" + home}
	runAgent(t, environment, codexPath, "plugin", "marketplace", "add", moduleRoot(t))
	runAgent(t, environment, codexPath, "plugin", "add", "agent-teleport@agent-teleport")

	// when
	codexExec(t, codexPath, home, mkdir(t, filepath.Join(t.TempDir(), "workspace")), "", "hello")

	// then
	if !strings.Contains(api.latestTexts(), skillName) {
		t.Fatalf("Codex does not offer %s", skillName)
	}
}

func systemTexts(request apiRequest) []string {
	texts := make([]string, 0, len(request.System))
	for _, block := range request.System {
		texts = append(texts, block.Text)
	}
	return texts
}

// runAgent runs an agent CLI command that needs no model.
func runAgent(t *testing.T, environment []string, binary string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = environment
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v: %s", filepath.Base(binary), strings.Join(args, " "), err, output)
	}
}
