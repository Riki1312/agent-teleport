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
	"path/filepath"
	"strings"
)

const (
	fixtureWorkspace = "/workspace/source"
	fixtureHome      = "/home/fixture"
	fixtureText      = "sanitized fixture text"
	fixtureOutput    = "sanitized fixture output"
)

func main() {
	outputDirectory := flag.String("output", "", "directory for the sanitized rollouts, which keep their file names")
	flag.Parse()
	if *outputDirectory == "" || flag.NArg() == 0 {
		fail(errors.New("usage: sanitize-fixture -output DIR ROLLOUT..."))
	}
	if err := run(flag.Args(), *outputDirectory); err != nil {
		fail(err)
	}
}

// run sanitizes rollouts that may reference each other through history_base.
// Sanitizing changes record lengths, so every reference is moved to the same
// record boundary of the sanitized target; references must point at rollouts
// listed earlier.
func run(inputs []string, outputDirectory string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var sanitizer *sanitizer
	boundaries := map[string][2][]int64{}
	outputs := map[string][]byte{}
	for _, input := range inputs {
		data, err := os.ReadFile(input)
		if err != nil {
			return err
		}
		var output bytes.Buffer
		encoder := json.NewEncoder(&output)
		encoder.SetEscapeHTML(false)
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64<<10), 64<<20)
		for first := true; scanner.Scan(); first = false {
			var event map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				return fmt.Errorf("%s: %w", input, err)
			}
			if first {
				payload, _ := event["payload"].(map[string]any)
				if sanitizer == nil {
					workspace, _ := payload["cwd"].(string)
					if workspace == "" {
						return errors.New("first event has no workspace")
					}
					sanitizer = newSanitizer(workspace, home)
				}
				if err := moveReference(payload, boundaries); err != nil {
					return fmt.Errorf("%s: %w", input, err)
				}
			}
			if err := encoder.Encode(sanitizer.value(event, "")); err != nil {
				return err
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if err := sanitizer.requireClean(output.Bytes()); err != nil {
			return fmt.Errorf("%s: %w", input, err)
		}
		boundaries[objectID(input)] = [2][]int64{recordEnds(data), recordEnds(output.Bytes())}
		outputs[filepath.Base(input)] = output.Bytes()
	}
	for name, data := range outputs {
		if err := os.WriteFile(filepath.Join(outputDirectory, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// moveReference moves a history_base byte offset to the same record boundary
// of the sanitized rollout it references.
func moveReference(payload map[string]any, boundaries map[string][2][]int64) error {
	base, ok := payload["history_base"].(map[string]any)
	if !ok {
		return nil
	}
	object, _ := base["thread_id"].(string)
	offset, _ := base["end_byte_offset"].(float64)
	ends, ok := boundaries[object]
	if !ok {
		return fmt.Errorf("history_base references %s, which must be listed earlier", object)
	}
	for index, end := range ends[0] {
		if end == int64(offset) {
			base["end_byte_offset"] = ends[1][index]
			return nil
		}
	}
	return errors.New("history_base does not end at a record boundary")
}

// objectID returns the rollout object a native file name records: the
// snapshot UUID when present, otherwise the thread ID.
func objectID(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if index := strings.LastIndex(name, "_"); index >= 0 {
		return name[index+1:]
	}
	return name[len(name)-36:]
}

func recordEnds(data []byte) []int64 {
	var ends []int64
	for index, character := range data {
		if character == '\n' {
			ends = append(ends, int64(index+1))
		}
	}
	return ends
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

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sanitize-fixture:", err)
	os.Exit(1)
}
