// Command sanitize-fixture scrubs a completed Codex rollout for use as a
// compatibility fixture. Its output must still be reviewed manually before it
// is committed.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	var inputPath, outputPath string
	flag.StringVar(&inputPath, "input", "", "source rollout JSONL")
	flag.StringVar(&outputPath, "output", "", "sanitized fixture JSONL")
	flag.Parse()
	if inputPath == "" || outputPath == "" {
		fail(errors.New("-input and -output are required"))
	}
	input, err := os.Open(inputPath)
	if err != nil {
		fail(err)
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		fail(err)
	}

	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	encoder := json.NewEncoder(output)
	lastType := ""
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			fail(err)
		}
		event = sanitize(event, "").(map[string]any)
		if err := encoder.Encode(event); err != nil {
			fail(err)
		}
		lastType = eventType(event)
	}
	if err := scanner.Err(); err != nil {
		fail(err)
	}
	if lastType != "event_msg/task_complete" {
		fail(errors.New("source does not end at a completed turn"))
	}
	if err := output.Close(); err != nil {
		fail(err)
	}
}

func sanitize(value any, key string) any {
	if replacement, ok := replacementFor(key, value); ok {
		return replacement
	}
	switch value := value.(type) {
	case map[string]any:
		for childKey, child := range value {
			value[childKey] = sanitize(child, childKey)
		}
	case []any:
		for index, child := range value {
			value[index] = sanitize(child, key)
		}
	}
	return value
}

func replacementFor(key string, value any) (any, bool) {
	switch key {
	case "text", "last_agent_message", "developer_instructions", "summary",
		"body", "instructions", "filesystem", "multi_agent_usage_hint":
		if _, ok := value.(string); ok {
			return "sanitized fixture text", true
		}
	case "input":
		if _, ok := value.(string); ok {
			return "{}", true
		}
	case "aggregated_output", "formatted_output", "stdout", "stderr":
		if _, ok := value.(string); ok {
			return "sanitized fixture output", true
		}
	case "encrypted_content":
		if _, ok := value.(string); ok {
			return "", true
		}
	case "name":
		if _, ok := value.(string); ok {
			return "fixture-item", true
		}
	case "shell":
		if _, ok := value.(string); ok {
			return "/bin/sh", true
		}
	case "cmd", "command":
		switch value.(type) {
		case string:
			return "true", true
		case []any:
			return []any{"true"}, true
		}
	case "cwd", "path", "root":
		if _, ok := value.(string); ok {
			return "/workspace/source", true
		}
	case "workspace_roots":
		if _, ok := value.([]any); ok {
			return []any{"/workspace/source"}, true
		}
	case "repository_url":
		return "https://example.invalid/sanitized/fixture.git", true
	case "branch":
		return "fixture", true
	case "commit_hash":
		return strings.Repeat("0", 40), true
	}
	return nil, false
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
