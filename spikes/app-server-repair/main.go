// Command app-server-repair probes whether Codex App Server can rebuild its state
// database from a newly placed rollout. It is disposable spike code, not cxport's
// import implementation.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxMessageSize = 64 << 20

type sessionMeta struct {
	id        string
	timestamp time.Time
	rollout   []byte
	remapped  int
}

type threadSummary struct {
	ID  string `json:"id"`
	CWD string `json:"cwd"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("app-server error %d: %s", e.Code, e.Message)
}

type response struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type client struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	lines   *bufio.Scanner
	stderr  bytes.Buffer
	nextID  int
	closed  bool
}

func main() {
	var codexPath string
	var rolloutPath string
	var targetHome string
	var targetCWD string
	var remapScope string

	flag.StringVar(&codexPath, "codex", "codex", "path to the Codex CLI")
	flag.StringVar(&rolloutPath, "rollout", "", "source rollout JSONL")
	flag.StringVar(&targetHome, "target-home", "", "empty target CODEX_HOME")
	flag.StringVar(&targetCWD, "target-cwd", "", "workspace recorded in the imported rollout")
	flag.StringVar(&remapScope, "remap", "workspace-metadata", "CWD fields to remap: workspace-metadata or session-meta")
	flag.Parse()

	if rolloutPath == "" || targetHome == "" || targetCWD == "" {
		fail(errors.New("-rollout, -target-home, and -target-cwd are required"))
	}

	meta, err := loadSession(rolloutPath, targetCWD, remapScope)
	if err != nil {
		fail(err)
	}
	if err := requireEmptyDirectory(targetHome); err != nil {
		fail(err)
	}
	if err := os.MkdirAll(targetCWD, 0o700); err != nil {
		fail(fmt.Errorf("create target workspace: %w", err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := startClient(ctx, codexPath, targetHome)
	if err != nil {
		fail(err)
	}
	defer c.close()

	if err := c.initialize(); err != nil {
		fail(err)
	}

	before, err := c.list("", true)
	if err != nil {
		fail(fmt.Errorf("state-only list before placement: %w", err))
	}

	destination, err := placeRollout(targetHome, rolloutPath, meta)
	if err != nil {
		fail(err)
	}

	afterPlacement, err := c.list("", true)
	if err != nil {
		fail(fmt.Errorf("state-only list after placement: %w", err))
	}
	scanned, err := c.list("", false)
	if err != nil {
		fail(fmt.Errorf("scan-and-repair list: %w", err))
	}
	afterRepair, err := c.list("", true)
	if err != nil {
		fail(fmt.Errorf("state-only list after repair: %w", err))
	}
	if !contains(scanned, meta.id) {
		fail(fmt.Errorf("scan-and-repair did not discover session %s", meta.id))
	}
	if !contains(afterRepair, meta.id) {
		fail(fmt.Errorf("session %s was not persisted to the state database", meta.id))
	}

	readThread, readErr := c.read(meta.id)
	resumeThread, resumeErr := c.resume(meta.id, targetCWD)
	afterResume, listAfterResumeErr := c.list("", true)
	c.close()

	restarted, restartErr := startClient(ctx, codexPath, targetHome)
	var afterRestart []threadSummary
	var restartReadThread threadSummary
	var restartReadErr error
	if restartErr == nil {
		defer restarted.close()
		restartErr = restarted.initialize()
	}
	if restartErr == nil {
		afterRestart, restartErr = restarted.list("", true)
	}
	if restartErr == nil {
		restartReadThread, restartReadErr = restarted.read(meta.id)
	}

	fmt.Printf("session: %s\n", meta.id)
	fmt.Printf("destination: %s\n", destination)
	fmt.Printf("cwd_fields_remapped: %d\n", meta.remapped)
	fmt.Printf("state_before_placement: %t\n", contains(before, meta.id))
	fmt.Printf("state_after_placement: %t\n", contains(afterPlacement, meta.id))
	fmt.Printf("scan_discovered: %t\n", contains(scanned, meta.id))
	fmt.Printf("state_after_repair: %t\n", contains(afterRepair, meta.id))
	fmt.Printf("repaired_cwd: %s\n", cwdOf(afterRepair, meta.id))
	fmt.Printf("thread_read: %t%s\n", readErr == nil, errorSuffix(readErr))
	fmt.Printf("read_cwd: %s\n", readThread.CWD)
	fmt.Printf("thread_resume: %t%s\n", resumeErr == nil, errorSuffix(resumeErr))
	fmt.Printf("resume_cwd: %s\n", resumeThread.CWD)
	fmt.Printf("state_after_resume: %t%s\n", contains(afterResume, meta.id), errorSuffix(listAfterResumeErr))
	fmt.Printf("persisted_cwd_after_resume: %s\n", cwdOf(afterResume, meta.id))
	fmt.Printf("state_after_restart: %t%s\n", contains(afterRestart, meta.id), errorSuffix(restartErr))
	fmt.Printf("thread_read_after_restart: %t%s\n", restartReadErr == nil && restartReadThread.ID == meta.id, errorSuffix(restartReadErr))
	fmt.Printf("cwd_after_restart: %s\n", restartReadThread.CWD)
}

func loadSession(path, targetCWD, remapScope string) (sessionMeta, error) {
	if remapScope != "workspace-metadata" && remapScope != "session-meta" {
		return sessionMeta{}, fmt.Errorf("unknown -remap value %q", remapScope)
	}
	source, err := os.ReadFile(path)
	if err != nil {
		return sessionMeta{}, fmt.Errorf("read rollout: %w", err)
	}
	lines := bytes.Split(source, []byte{'\n'})
	if len(lines) < 2 {
		return sessionMeta{}, errors.New("rollout has no complete first line")
	}
	var first map[string]any
	if err := json.Unmarshal(lines[0], &first); err != nil {
		return sessionMeta{}, fmt.Errorf("decode session metadata: %w", err)
	}
	if first["type"] != "session_meta" {
		return sessionMeta{}, fmt.Errorf("first rollout event has type %q, want session_meta", first["type"])
	}
	payload, ok := first["payload"].(map[string]any)
	if !ok {
		return sessionMeta{}, errors.New("session metadata payload is not an object")
	}
	id, _ := payload["session_id"].(string)
	if id == "" {
		id, _ = payload["id"].(string)
	}
	if id == "" {
		return sessionMeta{}, errors.New("session metadata has no session ID")
	}
	sourceCWD, _ := payload["cwd"].(string)
	if sourceCWD == "" {
		return sessionMeta{}, errors.New("session metadata has no cwd")
	}
	timestampValue, _ := payload["timestamp"].(string)
	timestamp, err := time.Parse(time.RFC3339Nano, timestampValue)
	if err != nil {
		return sessionMeta{}, fmt.Errorf("parse session timestamp %q: %w", timestampValue, err)
	}

	remapped := 0
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			return sessionMeta{}, fmt.Errorf("decode rollout line %d: %w", index+1, err)
		}
		eventType, _ := event["type"].(string)
		lineRemapped := 0
		if remapScope == "workspace-metadata" && isWorkspaceMetadata(event) {
			lineRemapped = remapCWDValues(event, sourceCWD, targetCWD)
		} else if eventType == "session_meta" {
			lineRemapped = remapCWDValues(event, sourceCWD, targetCWD)
		}
		if lineRemapped == 0 {
			continue
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			return sessionMeta{}, fmt.Errorf("encode remapped rollout line %d: %w", index+1, err)
		}
		lines[index] = encoded
		remapped += lineRemapped
	}
	return sessionMeta{id: id, timestamp: timestamp, rollout: bytes.Join(lines, []byte{'\n'}), remapped: remapped}, nil
}

func isWorkspaceMetadata(event map[string]any) bool {
	eventType, _ := event["type"].(string)
	if eventType == "session_meta" || eventType == "world_state" || eventType == "turn_context" {
		return true
	}
	if eventType != "event_msg" {
		return false
	}
	payload, _ := event["payload"].(map[string]any)
	payloadType, _ := payload["type"].(string)
	return payloadType == "thread_settings_applied"
}

func remapCWDValues(value any, sourceCWD, targetCWD string) int {
	remapped := 0
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "cwd" {
				if cwd, ok := child.(string); ok {
					switch cwd {
					case sourceCWD:
						value[key] = targetCWD
						remapped++
					case "file://" + sourceCWD:
						value[key] = "file://" + targetCWD
						remapped++
					}
				}
			}
			remapped += remapCWDValues(child, sourceCWD, targetCWD)
		}
	case []any:
		for _, child := range value {
			remapped += remapCWDValues(child, sourceCWD, targetCWD)
		}
	}
	return remapped
}

func requireEmptyDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create target home: %w", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read target home: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("target home %s is not empty", path)
	}
	return nil
}

func placeRollout(home, sourcePath string, meta sessionMeta) (string, error) {
	directory := filepath.Join(
		home,
		"sessions",
		fmt.Sprintf("%04d", meta.timestamp.Year()),
		fmt.Sprintf("%02d", int(meta.timestamp.Month())),
		fmt.Sprintf("%02d", meta.timestamp.Day()),
	)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create session directory: %w", err)
	}

	destination := filepath.Join(directory, filepath.Base(sourcePath))
	temporary, err := os.CreateTemp(directory, ".cxport-rollout-*")
	if err != nil {
		return "", fmt.Errorf("create staged rollout: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", fmt.Errorf("protect staged rollout: %w", err)
	}
	if _, err := temporary.Write(meta.rollout); err != nil {
		temporary.Close()
		return "", fmt.Errorf("write staged rollout: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", fmt.Errorf("sync staged rollout: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close staged rollout: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", fmt.Errorf("place rollout: %w", err)
	}
	return destination, nil
}

func startClient(ctx context.Context, codexPath, home string) (*client, error) {
	command := exec.CommandContext(ctx, codexPath, "app-server", "--stdio")
	command.Env = withoutCodexHome(os.Environ(), home)

	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open app-server stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open app-server stdout: %w", err)
	}
	c := &client{command: command, stdin: stdin, nextID: 1}
	c.lines = bufio.NewScanner(stdout)
	c.lines.Buffer(make([]byte, 64<<10), maxMessageSize)
	command.Stderr = &c.stderr

	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start app-server: %w", err)
	}
	return c, nil
}

func withoutCodexHome(environment []string, home string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, value := range environment {
		if !strings.HasPrefix(value, "CODEX_HOME=") {
			result = append(result, value)
		}
	}
	return append(result, "CODEX_HOME="+home)
}

func (c *client) initialize() error {
	_, err := c.call("initialize", map[string]any{
		"clientInfo": map[string]string{
			"name":    "cxport_spike",
			"title":   "cxport App Server repair spike",
			"version": "0.0.0",
		},
	})
	if err != nil {
		return fmt.Errorf("initialize app-server: %w", err)
	}
	return c.notify("initialized", map[string]any{})
}

func (c *client) list(cwd string, stateOnly bool) ([]threadSummary, error) {
	params := map[string]any{
		"limit":          100,
		"useStateDbOnly": stateOnly,
	}
	if cwd != "" {
		params["cwd"] = cwd
	}
	result, err := c.call("thread/list", params)
	if err != nil {
		return nil, err
	}
	var page struct {
		Data []threadSummary `json:"data"`
	}
	if err := json.Unmarshal(result, &page); err != nil {
		return nil, fmt.Errorf("decode thread/list response: %w", err)
	}
	return page.Data, nil
}

func (c *client) read(id string) (threadSummary, error) {
	result, err := c.call("thread/read", map[string]any{
		"threadId":     id,
		"includeTurns": false,
	})
	if err != nil {
		return threadSummary{}, err
	}
	return responseHasThread(result, id)
}

func (c *client) resume(id, cwd string) (threadSummary, error) {
	result, err := c.call("thread/resume", map[string]any{
		"threadId": id,
		"cwd":      cwd,
	})
	if err != nil {
		return threadSummary{}, err
	}
	return responseHasThread(result, id)
}

func responseHasThread(result json.RawMessage, id string) (threadSummary, error) {
	var value struct {
		Thread threadSummary `json:"thread"`
	}
	if err := json.Unmarshal(result, &value); err != nil {
		return threadSummary{}, fmt.Errorf("decode thread response: %w", err)
	}
	if value.Thread.ID != id {
		return threadSummary{}, fmt.Errorf("thread response returned ID %q, want %q", value.Thread.ID, id)
	}
	return value.Thread, nil
}

func (c *client) call(method string, params any) (json.RawMessage, error) {
	id := c.nextID
	c.nextID++
	if err := c.send(map[string]any{"method": method, "id": id, "params": params}); err != nil {
		return nil, err
	}

	for c.lines.Scan() {
		var reply response
		if err := json.Unmarshal(c.lines.Bytes(), &reply); err != nil {
			return nil, fmt.Errorf("decode app-server message: %w", err)
		}
		if len(reply.ID) == 0 {
			continue
		}
		var responseID int
		if err := json.Unmarshal(reply.ID, &responseID); err != nil || responseID != id {
			continue
		}
		if reply.Error != nil {
			return nil, reply.Error
		}
		return reply.Result, nil
	}
	if err := c.lines.Err(); err != nil {
		return nil, fmt.Errorf("read app-server output: %w%s", err, c.stderrSuffix())
	}
	return nil, fmt.Errorf("app-server closed its output%s", c.stderrSuffix())
}

func (c *client) notify(method string, params any) error {
	return c.send(map[string]any{"method": method, "params": params})
}

func (c *client) send(message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode app-server request: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := c.stdin.Write(encoded); err != nil {
		return fmt.Errorf("write app-server request: %w", err)
	}
	return nil
}

func (c *client) close() {
	if c.closed {
		return
	}
	c.closed = true
	c.stdin.Close()
	if c.command.Process != nil {
		c.command.Process.Kill()
	}
	c.command.Wait()
}

func (c *client) stderrSuffix() string {
	value := strings.TrimSpace(c.stderr.String())
	if value == "" {
		return ""
	}
	return ": " + value
}

func contains(threads []threadSummary, wanted string) bool {
	for _, thread := range threads {
		if thread.ID == wanted {
			return true
		}
	}
	return false
}

func cwdOf(threads []threadSummary, wanted string) string {
	for _, thread := range threads {
		if thread.ID == wanted {
			return thread.CWD
		}
	}
	return ""
}

func errorSuffix(err error) string {
	if err == nil {
		return ""
	}
	var appError *rpcError
	if errors.As(err, &appError) {
		return " (" + strconv.Itoa(appError.Code) + ": " + appError.Message + ")"
	}
	return " (" + err.Error() + ")"
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "app-server-repair:", err)
	os.Exit(1)
}
