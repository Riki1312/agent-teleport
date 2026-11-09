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
)

// scriptedResponses is a local OpenAI Responses API that answers Codex with
// scripted replies chosen by the prompt, so Codex records real sessions
// without credentials or model calls.
type scriptedResponses struct {
	server *httptest.Server
	mu     sync.Mutex
	count  int
	// commands maps a prompt to the shell command Codex is asked to run for
	// it; every other prompt gets a text reply.
	commands map[string]string
	// outputs are the tool outputs Codex reported, for failure messages.
	outputs []string
	// texts are the input texts of the latest request.
	texts []string
}

// latestTexts returns the input texts of the latest request Codex sent.
func (r *scriptedResponses) latestTexts() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.texts, "\n")
}

func (r *scriptedResponses) toolOutputs() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.outputs, "\n")
}

func startScriptedResponses(t *testing.T, commands map[string]string) *scriptedResponses {
	t.Helper()
	api := &scriptedResponses{commands: commands}
	api.server = httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(api.server.Close)
	return api
}

// configure writes a Codex configuration to home that uses the scripted API.
// Plugins are disabled unless the test needs them, because Codex otherwise
// syncs its remote plugin catalog in the background after a turn.
func (r *scriptedResponses) configure(t *testing.T, home string, plugins bool) {
	t.Helper()
	config := fmt.Sprintf("model = \"scripted\"\nmodel_provider = \"scripted\"\n\n[model_providers.scripted]\nname = \"scripted\"\nbase_url = %q\nwire_api = \"responses\"\n\n[features]\nplugins = %t\n", r.server.URL+"/v1", plugins)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (r *scriptedResponses) serve(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[],"models":[]}`))
		return
	}
	var body struct {
		Input []struct {
			Type    string          `json:"type"`
			Output  json.RawMessage `json:"output"`
			Role    string          `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	// The reply depends on the last prompt, unless a tool has answered since.
	prompt, answered := "", false
	for _, item := range body.Input {
		switch {
		case item.Type == "message" && item.Role == "user" && len(item.Content) > 0:
			prompt, answered = strings.TrimSpace(item.Content[len(item.Content)-1].Text), false
		case item.Type == "function_call_output":
			answered = true
		}
	}
	r.mu.Lock()
	r.texts = nil
	for _, item := range body.Input {
		if item.Type == "function_call_output" && len(item.Output) > 0 {
			r.outputs = append(r.outputs, string(item.Output))
		}
		for _, content := range item.Content {
			r.texts = append(r.texts, content.Text)
		}
	}
	r.count++
	index := r.count
	r.mu.Unlock()
	item := map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "reply to " + prompt}}}
	if command, ok := r.commands[prompt]; ok && !answered {
		arguments, _ := json.Marshal(map[string]string{"cmd": command})
		item = map[string]any{"type": "function_call", "name": "exec_command", "call_id": fmt.Sprintf("call_%d", index), "arguments": string(arguments)}
	}
	item["id"] = fmt.Sprintf("item_%d", index)
	writer.Header().Set("content-type", "text/event-stream")
	event := func(data map[string]any) {
		encoded, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", data["type"], encoded)
	}
	response := fmt.Sprintf("resp_%d", index)
	event(map[string]any{"type": "response.created", "response": map[string]any{"id": response}})
	event(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
	event(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	event(map[string]any{"type": "response.completed", "response": map[string]any{"id": response, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}}})
}

// codexExec runs one non-interactive Codex turn in cwd, inside Codex's
// workspace sandbox, resuming sessionID unless it is empty, and returns the
// session's ID.
func codexExec(t *testing.T, codexPath, home, cwd, sessionID, prompt string) string {
	t.Helper()
	arguments := []string{"exec", "--json", "--skip-git-repo-check", "--sandbox", "workspace-write"}
	if sessionID != "" {
		arguments = append(arguments, "resume", sessionID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, codexPath, append(arguments, prompt)...)
	command.Dir = cwd
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(home, "user"), "CODEX_HOME=" + home}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("codex exec %q: %v: %s", prompt, err, stderr.Bytes())
	}
	for line := range bytes.Lines(output) {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "thread.started" {
			return event.ThreadID
		}
	}
	t.Fatalf("codex exec %q reported no thread: %s", prompt, output)
	return ""
}
