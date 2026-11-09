// Package appserver is a minimal JSON-RPC client for the Codex App Server
// started with "codex app-server --stdio".
package appserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/Riki1312/agent-teleport/internal/buildinfo"
)

const (
	maxMessageSize = 64 << 20
	maxStderrSize  = 64 << 10
)

// Thread is the subset of an App Server thread that agent-teleport uses.
type Thread struct {
	ID        string `json:"id"`
	CWD       string `json:"cwd"`
	Name      string `json:"name"`
	Preview   string `json:"preview"`
	Path      string `json:"path"`
	UpdatedAt int64  `json:"updatedAt"`
	Status    struct {
		Type string `json:"type"`
	} `json:"status"`
	// Turns is filled only for loaded threads, such as by Resume.
	Turns []struct {
		ID string `json:"id"`
	} `json:"turns"`
}

// RPCError is an error response returned by the App Server.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("Codex App Server error %d: %s", e.Code, e.Message)
}

type response struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// Client is a running App Server process. It is not safe for concurrent use.
type Client struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	lines   *bufio.Scanner
	stderr  limitedBuffer
	nextID  int
	closed  bool
}

// Start launches the App Server of the Codex binary for codexHome and completes
// its initialization handshake. The process stops when ctx is canceled or Close is called.
func Start(ctx context.Context, binary, codexHome string) (*Client, error) {
	command := exec.CommandContext(ctx, binary, "app-server", "--stdio")
	command.Env = withCodexHome(os.Environ(), codexHome)

	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex App Server stdin: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex App Server stdout: %w", err)
	}
	client := &Client{command: command, stdin: stdin, nextID: 1}
	client.lines = bufio.NewScanner(stdout)
	client.lines.Buffer(make([]byte, 64<<10), maxMessageSize)
	command.Stderr = &client.stderr

	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start Codex App Server: %w", err)
	}
	if err := client.initialize(); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// Close stops the App Server process.
func (c *Client) Close() {
	if c == nil || c.closed {
		return
	}
	c.closed = true
	_ = c.stdin.Close()
	if c.command.Process != nil {
		_ = c.command.Process.Kill()
	}
	_ = c.command.Wait()
}

// ListQuery selects the threads List returns.
type ListQuery struct {
	// CWD limits the listing to one workspace when it is not empty.
	CWD string
	// StateOnly reads only the persisted state database; otherwise Codex may
	// scan and repair its state from rollout files.
	StateOnly bool
	// SourceKinds limits the listing to threads started by these sources, such
	// as "cli" or "exec". App Server lists only interactive sources by default.
	SourceKinds []string
}

// List returns every thread that matches query, newest first.
func (c *Client) List(query ListQuery) ([]Thread, error) {
	var result []Thread
	var cursor any
	for {
		params := map[string]any{
			"cursor":         cursor,
			"limit":          100,
			"sortKey":        "updated_at",
			"sortDirection":  "desc",
			"useStateDbOnly": query.StateOnly,
		}
		if query.CWD != "" {
			params["cwd"] = query.CWD
		}
		if len(query.SourceKinds) > 0 {
			params["sourceKinds"] = query.SourceKinds
		}
		raw, err := c.call("thread/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Data       []Thread `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("decode thread/list response: %w", err)
		}
		result = append(result, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return result, nil
		}
		cursor = *page.NextCursor
	}
}

// Read returns the metadata of one thread without its turns.
func (c *Client) Read(id string) (Thread, error) {
	return c.threadResponse("thread/read", map[string]any{
		"threadId":     id,
		"includeTurns": false,
	}, id)
}

// Resume loads a thread for continuation in cwd without starting a turn.
func (c *Client) Resume(id, cwd string) (Thread, error) {
	return c.threadResponse("thread/resume", map[string]any{
		"threadId": id,
		"cwd":      cwd,
	}, id)
}

func (c *Client) threadResponse(method string, params map[string]any, id string) (Thread, error) {
	raw, err := c.call(method, params)
	if err != nil {
		return Thread{}, err
	}
	var value struct {
		Thread Thread `json:"thread"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return Thread{}, fmt.Errorf("decode %s response: %w", method, err)
	}
	if value.Thread.ID == "" {
		return Thread{}, fmt.Errorf("%s returned an empty thread ID", method)
	}
	if id != "" && value.Thread.ID != id {
		return Thread{}, fmt.Errorf("%s returned ID %q, want %q", method, value.Thread.ID, id)
	}
	return value.Thread, nil
}

// Delete removes a thread, including its rollout file and indexed state.
func (c *Client) Delete(id string) error {
	_, err := c.call("thread/delete", map[string]any{"threadId": id})
	return err
}

// Archive moves a thread to the archived sessions store.
func (c *Client) Archive(id string) error {
	_, err := c.call("thread/archive", map[string]any{"threadId": id})
	return err
}

func (c *Client) initialize() error {
	_, err := c.call("initialize", map[string]any{
		"clientInfo": map[string]string{
			"name":    "agent-teleport",
			"title":   "agent-teleport",
			"version": buildinfo.Version(),
		},
	})
	if err != nil {
		return fmt.Errorf("initialize Codex App Server: %w", err)
	}
	return c.notify("initialized", map[string]any{})
}

func (c *Client) call(method string, params any) (json.RawMessage, error) {
	id := c.nextID
	c.nextID++
	if err := c.send(map[string]any{"method": method, "id": id, "params": params}); err != nil {
		return nil, err
	}
	for c.lines.Scan() {
		var reply response
		if err := json.Unmarshal(c.lines.Bytes(), &reply); err != nil {
			return nil, fmt.Errorf("decode App Server message: %w", err)
		}
		if len(reply.ID) == 0 || reply.Method != "" {
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
		return nil, fmt.Errorf("read App Server output: %w%s", err, c.stderrSuffix())
	}
	return nil, fmt.Errorf("App Server closed its output%s", c.stderrSuffix())
}

func (c *Client) notify(method string, params any) error {
	return c.send(map[string]any{"method": method, "params": params})
}

func (c *Client) send(message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode App Server request: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := c.stdin.Write(encoded); err != nil {
		return fmt.Errorf("write App Server request: %w", err)
	}
	return nil
}

func (c *Client) stderrSuffix() string {
	value := strings.TrimSpace(c.stderr.buffer.String())
	if value == "" {
		return ""
	}
	return ": " + value
}

func withCodexHome(environment []string, home string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, value := range environment {
		if !strings.HasPrefix(value, "CODEX_HOME=") {
			result = append(result, value)
		}
	}
	return append(result, "CODEX_HOME="+home)
}

// limitedBuffer keeps the first maxStderrSize bytes of App Server diagnostics
// and discards the rest, so a noisy server cannot grow memory without bound.
type limitedBuffer struct {
	buffer bytes.Buffer
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if remaining := maxStderrSize - b.buffer.Len(); remaining > 0 {
		b.buffer.Write(data[:min(len(data), remaining)])
	}
	return len(data), nil
}
