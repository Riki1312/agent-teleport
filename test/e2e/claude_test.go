package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Riki1312/agent-teleport/internal/claude"
	"github.com/Riki1312/agent-teleport/internal/codex"
)

// TestClaudeTwoHomeTransfer records sessions with the native Claude Code CLI
// against a scripted local Messages API, so it needs no credentials and makes
// no model calls, then moves them between isolated homes and workspaces.
func TestClaudeTwoHomeTransfer(t *testing.T) {
	binary := buildAgentTeleport(t)
	claudePath := requireClaude(t, binary)
	api := startScriptedAPI(t)
	work := testWorkDir(t)
	source := claudeHome{binary: claudePath, home: mkdir(t, filepath.Join(work, "source-home")), api: api}
	target := claudeHome{binary: claudePath, home: mkdir(t, filepath.Join(work, "target-home")), api: api}
	sourceCWD := mkdir(t, filepath.Join(work, "source-workspace"))
	// A workspace longer than 200 characters makes Claude Code hash its
	// project folder name.
	targetCWD := mkdir(t, filepath.Join(work, "target-workspace", strings.Repeat("nested-directory-", 12), "project"))
	otherCWD := mkdir(t, filepath.Join(work, "other-workspace"))

	sessionID := source.prompt(t, sourceCWD, "", "spawn a child")
	source.prompt(t, sourceCWD, sessionID, "print a large output")
	archivePath := filepath.Join(work, "session.teleport")
	runClaude(t, binary, source, nil, true, "export", "--all", sessionID, "-o", archivePath)
	assertArchiveFiles(t, archivePath, sessionID+"/subagents/", sessionID+"/tool-results/")
	runClaude(t, binary, source, nil, true, "inspect", archivePath, "--json")

	assertOutput(t, runClaude(t, binary, target, nil, true,
		"import", archivePath, "--cwd", targetCWD), "imported")
	assertListedCWD(t, runClaude(t, binary, target, nil, true,
		"list", "--all", "--json"), sessionID, targetCWD)
	assertOutput(t, runClaude(t, binary, target, nil, true,
		"import", archivePath, "--cwd", targetCWD), "already up to date")
	assertOutput(t, runClaude(t, binary, target, nil, true, "__complete", "export", "--all", ""), sessionID+"\tspawn a child")

	// Claude Code continues the imported session from the target workspace,
	// even by folder name with --continue, and sees only target paths.
	resumed := target.request(t, targetCWD, "--continue", "after import")
	resumed.requireTexts(t, "spawning", "child result", "reply")
	resumed.requireMoved(t, targetCWD, sourceCWD, source.home)

	exported := runClaude(t, binary, target, nil, true, "export", "--all", sessionID, "-o", "-")
	sourceTranscript := readTranscript(t, source.home, sessionID)
	assertOutput(t, runClaude(t, binary, source, exported, true,
		"import", "-", "--cwd", sourceCWD), "updated")
	if updated := readTranscript(t, source.home, sessionID); !bytes.HasPrefix(updated, sourceTranscript) {
		t.Fatal("moving the session back did not restore the original transcript bytes")
	}
	assertOutput(t, runClaude(t, binary, source, exported, true,
		"import", "-", "--cwd", sourceCWD), "already up to date")
	assertOutput(t, runClaude(t, binary, source, nil, true,
		"import", archivePath, "--cwd", sourceCWD), "newer than the archive")

	source.prompt(t, sourceCWD, sessionID, "continue on the source")
	divergedPath := filepath.Join(work, "diverged.teleport")
	runClaude(t, binary, source, nil, true, "export", "--all", sessionID, "-o", divergedPath)

	// While Claude Code works on a turn, the session exports up to that turn's
	// prompt and is never changed, even with --replace.
	running := target.start(t, targetCWD, sessionID, "hold the session")
	busyPath := filepath.Join(work, "busy.teleport")
	runClaude(t, binary, target, nil, true, "export", "--all", sessionID, "-o", busyPath)
	if transcript, _ := decodeArchive(t, busyPath).Data(sessionID + ".jsonl"); bytes.Contains(transcript, []byte("hold the session")) {
		t.Fatal("the export of a busy session holds its turn in progress")
	}
	runClaude(t, binary, target, nil, false, "import", divergedPath, "--cwd", targetCWD, "--replace")
	running.wait(t)

	runClaude(t, binary, target, nil, false, "import", divergedPath, "--cwd", targetCWD)
	assertOutput(t, runClaude(t, binary, target, nil, true,
		"import", divergedPath, "--cwd", targetCWD, "--replace"), "replaced")
	runClaude(t, binary, target, nil, false, "import", divergedPath, "--cwd", otherCWD)
	assertOutput(t, runClaude(t, binary, target, nil, true,
		"import", divergedPath, "--cwd", otherCWD, "--replace"), "replaced")
	assertListedCWD(t, runClaude(t, binary, target, nil, true,
		"list", "--all", "--json"), sessionID, otherCWD)
	if folders, _ := filepath.Glob(filepath.Join(target.home, "projects", "*", sessionID+".jsonl")); len(folders) != 1 {
		t.Fatalf("session is stored in %d project folders after a move, want 1", len(folders))
	}

	moved := target.request(t, otherCWD, "--resume", sessionID, "after the move")
	moved.requireTexts(t, "spawning", "child result", "continue on the source")
	moved.requireMoved(t, otherCWD, sourceCWD, source.home)
}

// requireClaude returns the installed Claude Code binary when the built CLI
// reports it as supported.
func requireClaude(t *testing.T, binary string) string {
	t.Helper()
	path, err := exec.LookPath("claude")
	if err != nil {
		skipOrFail(t, "Claude Code CLI is not installed")
	}
	home := claudeHome{binary: path, home: t.TempDir()}
	report := doctorReport(t, runClaude(t, binary, home, nil, true, "doctor", "--json"), claude.Provider)
	if !report.Supported {
		skipOrFail(t, "Claude Code "+report.Version+" is unsupported "+report.Error)
	}
	return path
}

// claudeHome is an isolated Claude Code configuration directory.
type claudeHome struct {
	binary string
	home   string
	api    *scriptedAPI
}

// environment runs a process with home as its Claude Code configuration and
// user home, talking to the scripted API with a placeholder key.
func (h claudeHome) environment() []string {
	environment := []string{
		"HOME=" + h.home,
		"CLAUDE_CONFIG_DIR=" + h.home,
		"PATH=" + os.Getenv("PATH"),
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_TELEMETRY=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"ANTHROPIC_API_KEY=agent-teleport-test-key",
	}
	if h.api != nil {
		environment = append(environment, "ANTHROPIC_BASE_URL="+h.api.server.URL)
	}
	for _, name := range []string{"TMPDIR", "LANG", "USER"} {
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

// command returns a non-interactive Claude Code command that continues
// sessionID, or starts a session when sessionID is empty.
func (h claudeHome) command(ctx context.Context, cwd string, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, h.binary, append([]string{"--output-format", "json", "--allowedTools", "Bash", "Agent", "-p"}, arguments...)...)
	command.Dir = cwd
	command.Env = h.environment()
	return command
}

// prompt sends one prompt and returns the session ID Claude Code reports.
func (h claudeHome) prompt(t *testing.T, cwd, sessionID, prompt string) string {
	t.Helper()
	arguments := []string{prompt}
	if sessionID != "" {
		arguments = []string{"--resume", sessionID, prompt}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var stderr bytes.Buffer
	command := h.command(ctx, cwd, arguments...)
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("claude %q: %v: %s", prompt, err, stderr.Bytes())
	}
	var result struct {
		SessionID string `json:"session_id"`
		IsError   bool   `json:"is_error"`
	}
	if err := json.Unmarshal(output, &result); err != nil || result.IsError {
		t.Fatalf("claude %q failed: %v: %s", prompt, err, output)
	}
	return result.SessionID
}

// request sends one prompt with arguments and returns the conversation Claude
// Code sent to the API for it.
func (h claudeHome) request(t *testing.T, cwd string, arguments ...string) apiRequest {
	t.Helper()
	before := h.api.count()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if output, err := h.command(ctx, cwd, arguments...).CombinedOutput(); err != nil {
		t.Fatalf("claude %q: %v: %s", arguments, err, output)
	}
	return h.api.firstSince(t, before)
}

// runningClaude is a Claude Code process working on a session.
type runningClaude struct {
	command *exec.Cmd
	cancel  context.CancelFunc
	output  *bytes.Buffer
}

// start resumes sessionID with a prompt that keeps Claude Code busy in a tool
// call, and returns once Claude Code has registered the session as open.
func (h claudeHome) start(t *testing.T, cwd, sessionID, prompt string) runningClaude {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	output := &bytes.Buffer{}
	command := h.command(ctx, cwd, "--resume", sessionID, prompt)
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	running := runningClaude{command: command, cancel: cancel, output: output}
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if h.api.holding() && registered(h.home, sessionID) {
			return running
		}
	}
	entries, _ := filepath.Glob(filepath.Join(h.home, "sessions", "*"))
	running.wait(t)
	t.Fatalf("Claude Code did not register the open session: held %t, registry %q", h.api.holding(), entries)
	return runningClaude{}
}

func (r runningClaude) wait(t *testing.T) {
	t.Helper()
	defer r.cancel()
	if err := r.command.Wait(); err != nil {
		t.Fatalf("claude: %v: %s", err, r.output.Bytes())
	}
}

// registered reports whether Claude Code's session registry lists sessionID.
func registered(home, sessionID string) bool {
	entries, _ := filepath.Glob(filepath.Join(home, "sessions", "*.json"))
	for _, entry := range entries {
		data, err := os.ReadFile(entry)
		if err == nil && bytes.Contains(data, []byte(sessionID)) {
			return true
		}
	}
	return false
}

// runClaude runs agent-teleport against an isolated Claude Code home, with
// Codex reported as not installed, and returns its stdout.
func runClaude(t *testing.T, binary string, home claudeHome, stdin []byte, wantSuccess bool, args ...string) []byte {
	t.Helper()
	return runIn(t, binary, home.agentTeleportEnvironment(), "", stdin, wantSuccess, args...)
}

// agentTeleportEnvironment runs agent-teleport against this home.
func (h claudeHome) agentTeleportEnvironment() []string {
	return isolatedEnvironment(
		"CLAUDE_CONFIG_DIR="+h.home,
		claude.BinaryEnvironment+"="+h.binary,
		codex.BinaryEnvironment+"="+missingCommand,
	)
}

func assertArchiveFiles(t *testing.T, path string, prefixes ...string) {
	t.Helper()
	manifest := decodeArchive(t, path).Manifest
	for _, prefix := range prefixes {
		found := false
		for _, file := range manifest.Files {
			found = found || strings.HasPrefix(file.Path, prefix)
		}
		if !found {
			t.Fatalf("archive has no file under %s", prefix)
		}
	}
}

func transcriptPath(t *testing.T, home, sessionID string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(home, "projects", "*", sessionID+".jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("find transcript of %s: %v %q", sessionID, err, matches)
	}
	return matches[0]
}

func readTranscript(t *testing.T, home, sessionID string) []byte {
	t.Helper()
	data, err := os.ReadFile(transcriptPath(t, home, sessionID))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// scriptedAPI is a local Messages API that answers Claude Code with scripted
// replies chosen by the prompt, and records every conversation it receives.
type scriptedAPI struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []apiRequest
	held     bool
	// commands maps a prompt to a shell command Claude Code is asked to run.
	commands map[string]string
}

// runOn makes Claude Code run command through its Bash tool for prompt.
func (a *scriptedAPI) runOn(prompt, command string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.commands == nil {
		a.commands = map[string]string{}
	}
	a.commands[prompt] = command
}

// apiRequest is the part of a Messages API request the test inspects.
type apiRequest struct {
	System []struct {
		Text string `json:"text"`
	} `json:"system"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Name string `json:"name"`
	} `json:"tools"`
	Stream bool `json:"stream"`
}

func startScriptedAPI(t *testing.T) *scriptedAPI {
	t.Helper()
	api := &scriptedAPI{}
	api.server = httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(api.server.Close)
	return api
}

func (a *scriptedAPI) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

func (a *scriptedAPI) holding() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.held
}

// firstSince returns the first conversation request received after the
// first `before` requests.
func (a *scriptedAPI) firstSince(t *testing.T, before int) apiRequest {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.requests) <= before {
		t.Fatal("Claude Code sent no request")
	}
	return a.requests[before]
}

func (a *scriptedAPI) serve(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !strings.HasPrefix(request.URL.Path, "/v1/messages") {
		http.NotFound(writer, request)
		return
	}
	if strings.HasSuffix(request.URL.Path, "/count_tokens") {
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(`{"input_tokens":10}`))
		return
	}
	var body apiRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	a.requests = append(a.requests, body)
	index := len(a.requests)
	blocks := body.reply(index)
	if command, ok := a.commands[body.lastPrompt()]; ok && body.hasTool("Bash") {
		blocks = []map[string]any{{"type": "tool_use", "id": fmt.Sprintf("toolu_%04d", index), "name": "Bash", "input": map[string]any{"command": command, "description": "run a command"}}}
	}
	if body.lastPrompt() == "hold the session" && blocks[len(blocks)-1]["type"] == "tool_use" {
		a.held = true
	}
	a.mu.Unlock()
	writeReply(writer, body, index, blocks)
}

// reply chooses the scripted content blocks for a conversation.
func (r apiRequest) reply(index int) []map[string]any {
	prompt := r.lastPrompt()
	toolUse := func(name string, input map[string]any) []map[string]any {
		return []map[string]any{
			{"type": "text", "text": "using " + name},
			{"type": "tool_use", "id": fmt.Sprintf("toolu_%04d", index), "name": name, "input": input},
		}
	}
	switch {
	case prompt == "child task":
		return []map[string]any{{"type": "text", "text": "child result"}}
	case prompt == "spawn a child" && r.hasTool("Agent"):
		blocks := toolUse("Agent", map[string]any{"description": "probe child", "prompt": "child task", "subagent_type": "general-purpose", "run_in_background": false})
		blocks[0]["text"] = "spawning"
		return blocks
	case prompt == "print a large output" && r.hasTool("Bash"):
		return toolUse("Bash", map[string]any{"command": "seq 1 60000", "description": "print numbers"})
	case prompt == "hold the session" && r.hasTool("Bash"):
		return toolUse("Bash", map[string]any{"command": "sleep 5", "description": "wait"})
	case prompt == "continue on the source":
		return []map[string]any{{"type": "text", "text": "continue on the source"}}
	default:
		return []map[string]any{{"type": "text", "text": "reply"}}
	}
}

// lastPrompt returns the text of the last user message when that message is a
// prompt rather than a tool result.
func (r apiRequest) lastPrompt() string {
	for index := len(r.Messages) - 1; index >= 0; index-- {
		message := r.Messages[index]
		if message.Role != "user" {
			continue
		}
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			if text = strings.TrimSpace(text); !strings.HasPrefix(text, "<") {
				return text
			}
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			return ""
		}
		prompt := ""
		for _, block := range blocks {
			switch {
			case block.Type == "tool_result":
				return ""
			case block.Type == "text" && !strings.HasPrefix(strings.TrimSpace(block.Text), "<"):
				prompt = strings.TrimSpace(block.Text)
			}
		}
		// A message of system reminders only follows the prompt it annotates.
		if prompt != "" {
			return prompt
		}
	}
	return ""
}

func (r apiRequest) hasTool(name string) bool {
	for _, tool := range r.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

// texts returns every text block of the conversation in order.
func (r apiRequest) texts() []string {
	var texts []string
	for _, message := range r.Messages {
		var text string
		if json.Unmarshal(message.Content, &text) == nil {
			texts = append(texts, text)
			continue
		}
		var blocks []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Content json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(message.Content, &blocks)
		for _, block := range blocks {
			texts = append(texts, block.Text, string(block.Content))
		}
	}
	return texts
}

// requireTexts requires every text in want, in order, within the
// conversation.
func (r apiRequest) requireTexts(t *testing.T, want ...string) {
	t.Helper()
	texts := r.texts()
	position := 0
	for _, text := range texts {
		if position < len(want) && strings.Contains(text, want[position]) {
			position++
		}
	}
	if position < len(want) {
		t.Fatalf("resumed conversation lacks %q in order", want[position])
	}
}

// requireMoved requires the conversation Claude Code sends, its system prompt
// and every message, to name workspace and none of the source paths, and
// requires no notice that the workspace moved.
func (r apiRequest) requireMoved(t *testing.T, workspace string, sourcePaths ...string) {
	t.Helper()
	conversation := r.texts()
	for _, block := range r.System {
		conversation = append(conversation, block.Text)
	}
	for _, message := range r.Messages {
		conversation = append(conversation, string(message.Content))
	}
	joined := strings.Join(conversation, "\n")
	if !strings.Contains(joined, workspace) {
		t.Fatal("resumed conversation does not name the target workspace")
	}
	for _, path := range sourcePaths {
		if strings.Contains(joined, path) {
			t.Fatalf("resumed conversation still names %s", path)
		}
	}
	if strings.Contains(joined, "(was ") {
		t.Fatal("Claude Code reported that the workspace moved")
	}
}

// writeReply streams blocks as a Messages API response.
func writeReply(writer http.ResponseWriter, request apiRequest, index int, blocks []map[string]any) {
	stopReason := "end_turn"
	for _, block := range blocks {
		if block["type"] == "tool_use" {
			stopReason = "tool_use"
		}
	}
	message := map[string]any{
		"id": fmt.Sprintf("msg_%04d", index), "type": "message", "role": "assistant", "model": "claude-test",
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 1},
	}
	if !request.Stream {
		message["content"], message["stop_reason"] = blocks, stopReason
		writer.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(writer).Encode(message)
		return
	}
	writer.Header().Set("content-type", "text/event-stream")
	event := func(name string, data any) {
		encoded, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", name, encoded)
	}
	event("message_start", map[string]any{"type": "message_start", "message": message})
	for position, block := range blocks {
		if block["type"] == "text" {
			event("content_block_start", map[string]any{"type": "content_block_start", "index": position, "content_block": map[string]any{"type": "text", "text": ""}})
			event("content_block_delta", map[string]any{"type": "content_block_delta", "index": position, "delta": map[string]any{"type": "text_delta", "text": block["text"]}})
		} else {
			input, _ := json.Marshal(block["input"])
			event("content_block_start", map[string]any{"type": "content_block_start", "index": position, "content_block": map[string]any{"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{}}})
			event("content_block_delta", map[string]any{"type": "content_block_delta", "index": position, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
		}
		event("content_block_stop", map[string]any{"type": "content_block_stop", "index": position})
	}
	event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 2}})
	event("message_stop", map[string]any{"type": "message_stop"})
}
