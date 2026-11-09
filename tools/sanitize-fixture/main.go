// Command sanitize-fixture scrubs a completed Codex rollout for use as a
// compatibility fixture. It replaces conversation text, tool input and output,
// instructions, and account identifiers, maps the workspace and home
// directories to fixed paths, and refuses to write output that still contains
// them. Its output must still be reviewed manually before it is committed.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"
	"strings"
)

const (
	fixtureWorkspace = "/workspace/source"
	fixtureHome      = "/home/fixture"
	fixtureText      = "sanitized fixture text"
	fixtureOutput    = "sanitized fixture output"
)

func main() {
	var inputPath, outputPath string
	flag.StringVar(&inputPath, "input", "", "source rollout JSONL")
	flag.StringVar(&outputPath, "output", "", "sanitized fixture JSONL")
	flag.Parse()
	if inputPath == "" || outputPath == "" {
		fail(errors.New("-input and -output are required"))
	}
	if err := run(inputPath, outputPath); err != nil {
		fail(err)
	}
}

func run(inputPath, outputPath string) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var sanitizer *sanitizer
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	lastType := ""
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return err
		}
		if sanitizer == nil {
			workspace, _ := event["payload"].(map[string]any)["cwd"].(string)
			if workspace == "" {
				return errors.New("first event has no workspace")
			}
			sanitizer = newSanitizer(workspace, home)
		}
		event = sanitizer.value(event, "").(map[string]any)
		if err := encoder.Encode(event); err != nil {
			return err
		}
		lastType = eventType(event)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if lastType != "event_msg/task_complete" && lastType != "event_msg/thread_settings_applied" {
		return errors.New("source does not end after a completed turn")
	}
	if err := sanitizer.requireClean(output.Bytes()); err != nil {
		return err
	}
	return os.WriteFile(outputPath, output.Bytes(), 0o600)
}

// sanitizer replaces private values in decoded rollout events.
type sanitizer struct {
	paths     *strings.Replacer
	forbidden []string
}

func newSanitizer(workspace, home string) *sanitizer {
	forbidden := []string{workspace, home}
	if current, err := user.Current(); err == nil && len(current.Username) > 2 {
		forbidden = append(forbidden, current.Username)
	}
	return &sanitizer{
		// The workspace comes first because it usually lies below home.
		paths:     strings.NewReplacer(workspace, fixtureWorkspace, home, fixtureHome),
		forbidden: forbidden,
	}
}

func (s *sanitizer) value(value any, key string) any {
	if replacement, ok := replacementFor(key, value); ok {
		return replacement
	}
	switch value := value.(type) {
	case map[string]any:
		for childKey, child := range value {
			value[childKey] = s.value(child, childKey)
		}
	case []any:
		for index, child := range value {
			value[index] = s.value(child, key)
		}
	case string:
		return s.paths.Replace(value)
	}
	return value
}

// requireClean fails when output still contains a private path or name.
func (s *sanitizer) requireClean(output []byte) error {
	for _, value := range s.forbidden {
		if bytes.Contains(output, []byte(value)) {
			return fmt.Errorf("sanitized output still contains a private value of length %d", len(value))
		}
	}
	return nil
}

func replacementFor(key string, value any) (any, bool) {
	// World state describes the apps connected to the source account as a map
	// from tool name to description.
	if tools, ok := value.(map[string]any); ok && key == "tools" && onlyStrings(tools) {
		clear(tools)
		return tools, true
	}
	if _, ok := value.(string); !ok {
		if key == "cmd" || key == "command" {
			if _, ok := value.([]any); ok {
				return []any{"true"}, true
			}
		}
		return nil, false
	}
	switch key {
	case "text", "last_agent_message", "developer_instructions", "summary", "body",
		"instructions", "filesystem", "multi_agent_usage_hint", "description":
		return fixtureText, true
	case "input":
		return "{}", true
	case "output", "aggregated_output", "formatted_output", "stdout", "stderr":
		return fixtureOutput, true
	case "encrypted_content":
		return "", true
	case "name":
		return "fixture-item", true
	case "shell":
		return "/bin/sh", true
	case "cmd", "command":
		return "true", true
	case "timezone":
		return "UTC", true
	case "creator_user_id":
		return "user-fixture", true
	case "creator_account_id":
		return "account-fixture", true
	case "repository_url":
		return "https://example.invalid/sanitized/fixture.git", true
	case "branch":
		return "fixture", true
	case "commit_hash":
		return strings.Repeat("0", 40), true
	}
	return nil, false
}

func onlyStrings(values map[string]any) bool {
	for _, value := range values {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}

func eventType(event map[string]any) string {
	top, _ := event["type"].(string)
	payload, _ := event["payload"].(map[string]any)
	inner, _ := payload["type"].(string)
	return top + "/" + inner
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sanitize-fixture:", err)
	os.Exit(1)
}
