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
)

const maxMessageSize = 64 << 20

type Thread struct {
	ID         string `json:"id"`
	CWD        string `json:"cwd"`
	Name       string `json:"name"`
	Preview    string `json:"preview"`
	Path       string `json:"path"`
	CLIVersion string `json:"cliVersion"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
	Status     struct {
		Type string `json:"type"`
	} `json:"status"`
}

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
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

type Client struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	lines   *bufio.Scanner
	stderr  bytes.Buffer
	nextID  int
	closed  bool
}

func Start(ctx context.Context, codexPath, codexHome string) (*Client, error) {
	command := exec.CommandContext(ctx, codexPath, "app-server", "--stdio")
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

func (c *Client) List(cwd string, stateOnly bool) ([]Thread, error) {
	var result []Thread
	var cursor any
	for {
		params := map[string]any{
			"cursor":         cursor,
			"limit":          100,
			"sortKey":        "updated_at",
			"sortDirection":  "desc",
			"useStateDbOnly": stateOnly,
		}
		if cwd != "" {
			params["cwd"] = cwd
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

func (c *Client) Read(id string) (Thread, error) {
	raw, err := c.call("thread/read", map[string]any{
		"threadId":     id,
		"includeTurns": false,
	})
	if err != nil {
		return Thread{}, err
	}
	var value struct {
		Thread Thread `json:"thread"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return Thread{}, fmt.Errorf("decode thread/read response: %w", err)
	}
	if value.Thread.ID != id {
		return Thread{}, fmt.Errorf("thread/read returned ID %q, want %q", value.Thread.ID, id)
	}
	return value.Thread, nil
}

func (c *Client) Delete(id string) error {
	_, err := c.call("thread/delete", map[string]any{"threadId": id})
	return err
}

func (c *Client) initialize() error {
	_, err := c.call("initialize", map[string]any{
		"clientInfo": map[string]string{
			"name":    "cxport",
			"title":   "agent-teleport",
			"version": "0.0.0",
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
	value := strings.TrimSpace(c.stderr.String())
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
