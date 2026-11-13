package claude

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Riki1312/agent-teleport/internal/pathmap"
)

const testSessionID = "6f5fb979-80ed-48da-8688-9ee51a3bd400"

func TestProjectFolderNameReplacesEveryOtherCharacter(t *testing.T) {
	// when
	name := projectFolderName("/Users/me/My Project_é😀")

	// then
	if want := "-Users-me-My-Project----"; name != want {
		t.Fatalf("projectFolderName() = %q, want %q", name, want)
	}
}

func TestProjectFolderNameHashesLongWorkspaces(t *testing.T) {
	workspace := "/work/" + strings.Repeat("nested-directory-", 12) + "project/é😀"

	// when
	name := projectFolderName(workspace)

	// then
	want := projectFolderName(workspace[:len(workspace)-len("é😀")])[:maxProjectFolderName] + "-tyszp5"
	if name != want {
		t.Fatalf("projectFolderName() = %q, want %q", name, want)
	}
}

func TestParseTranscriptSummarizesTheSession(t *testing.T) {
	transcript := lines(
		`{"type":"queue-operation","sessionId":"`+testSessionID+`"}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"meta"},"cwd":"/work/project","sessionId":"`+testSessionID+`","timestamp":"2026-11-09T10:00:00.000Z"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"fix   the\nbuild"}]},"cwd":"/work/project","sessionId":"`+testSessionID+`","timestamp":"2026-11-09T10:00:01.000Z"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[]},"cwd":"/work/project/sub","sessionId":"`+testSessionID+`","timestamp":"2026-11-09T10:05:00.000Z"}`,
		`{"type":"file-history-snapshot","messageId":"m"}`,
	)

	// when
	info, err := parseTranscript(transcript, testSessionID, "")

	// then
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(info.workspaces, []string{"/work/project", "/work/project/sub"}) || info.title != "fix the build" || info.updatedAt.Minute() != 5 {
		t.Fatalf("parseTranscript() = %+v", info)
	}
}

func TestParseTranscriptPrefersCustomThenGeneratedTitles(t *testing.T) {
	message := `{"type":"user","message":{"role":"user","content":"prompt"},"cwd":"/w","sessionId":"` + testSessionID + `"}`
	for _, test := range []struct {
		records []string
		want    string
	}{
		{records: []string{message, `{"type":"ai-title","aiTitle":"Generated"}`}, want: "Generated"},
		{records: []string{`{"type":"custom-title","customTitle":"Mine"}`, message, `{"type":"ai-title","aiTitle":"Generated"}`}, want: "Mine"},
	} {
		// when
		info, err := parseTranscript(lines(test.records...), testSessionID, "")

		// then
		if err != nil || info.title != test.want {
			t.Fatalf("parseTranscript() title = %q, %v; want %q", info.title, err, test.want)
		}
	}
}

func TestParseTranscriptRejectsInconsistentTranscripts(t *testing.T) {
	message := `{"type":"user","message":{"role":"user","content":"p"},"cwd":"/w","sessionId":"` + testSessionID + `","agentId":"a1"}`
	for name, test := range map[string]struct {
		data  []byte
		agent string
	}{
		"empty":                   {data: nil},
		"incomplete final record": {data: []byte(message)},
		"invalid JSON":            {data: lines(message, `{`)},
		"other session":           {data: lines(strings.Replace(message, testSessionID, "11111111-1111-1111-1111-111111111111", 1))},
		"other agent":             {data: lines(message), agent: "a2"},
		"no messages":             {data: lines(`{"type":"queue-operation","sessionId":"` + testSessionID + `","cwd":"/w"}`)},
		"no working directory":    {data: lines(strings.Replace(message, `"cwd":"/w",`, "", 1))},
	} {
		t.Run(name, func(t *testing.T) {
			// when
			_, err := parseTranscript(test.data, testSessionID, test.agent)

			// then
			if err == nil {
				t.Fatal("parseTranscript() succeeded")
			}
		})
	}
}

func TestCompleteRecordsDropsARecordBeingWritten(t *testing.T) {
	// when
	complete := completeRecords([]byte("{\"a\":1}\n{\"b\""))

	// then
	if string(complete) != "{\"a\":1}\n" {
		t.Fatalf("completeRecords() = %q", complete)
	}
}

func TestRelocateRecordsMovesEveryPath(t *testing.T) {
	mapping, err := pathmap.New(
		pathmap.Move{From: "/work/project", To: "/target"},
		pathmap.Move{From: "/home/a/.claude/projects/-work-project", To: "/home/b/.claude/projects/-target"},
	)
	if err != nil {
		t.Fatal(err)
	}
	transcript := lines(
		`{"type":"user","message":{"role":"user","content":"read /work/project/a and /work/project-other"},"cwd":"/work/project"}`,
		`{"type":"attachment","attachment":{"type":"prompt_snapshot","systemPrompt":"memory at /home/a/.claude/projects/-work-project/memory/"},"cwd":"/work/project"}`,
	)

	// when
	relocated, err := mapping.RewriteLines(transcript)

	// then
	if err != nil {
		t.Fatal(err)
	}
	want := lines(
		`{"type":"user","message":{"role":"user","content":"read /target/a and /work/project-other"},"cwd":"/target"}`,
		`{"type":"attachment","attachment":{"type":"prompt_snapshot","systemPrompt":"memory at /home/b/.claude/projects/-target/memory/"},"cwd":"/target"}`,
	)
	if string(relocated) != string(want) {
		t.Fatalf("RewriteLines() =\n%s\nwant\n%s", relocated, want)
	}
}

func TestRelocateRecordsRejectsIrreversibleRewrites(t *testing.T) {
	mapping, err := pathmap.New(pathmap.Move{From: "/work/project", To: "/target"})
	if err != nil {
		t.Fatal(err)
	}
	transcript := lines(`{"type":"user","message":{"role":"user","content":"/work/project and /target"},"cwd":"/work/project"}`)

	// when
	_, err = mapping.RewriteLines(transcript)

	// then
	if !errors.Is(err, pathmap.ErrIrreversible) {
		t.Fatalf("RewriteLines() error = %v", err)
	}
}

func TestLayoutForVersions(t *testing.T) {
	for _, test := range []struct {
		version   string
		supported bool
		tested    bool
	}{
		{version: "2.1.316", supported: false},
		{version: "2.1.317", supported: true, tested: true},
		{version: "2.1.322", supported: true, tested: true},
		{version: "2.1.323", supported: true, tested: true},
		{version: "2.1.324", supported: true, tested: false},
	} {
		// when
		_, tested, err := layoutFor(test.version)

		// then
		if (err == nil) != test.supported || tested != test.tested {
			t.Fatalf("layoutFor(%s) tested = %t, error = %v", test.version, tested, err)
		}
	}
}

func lines(records ...string) []byte {
	return []byte(strings.Join(records, "\n") + "\n")
}

func TestCompletedTranscriptEndIncludesTheQueueRecordsOfThePrompt(t *testing.T) {
	first := string(lines(`{"type":"user","message":{"role":"user","content":"one"}}`, `{"type":"assistant","message":{"role":"assistant","content":[],"stop_reason":"end_turn"}}`))
	turn := string(lines(`{"type":"queue-operation","operation":"enqueue","content":"two"}`, `{"type":"queue-operation","operation":"dequeue"}`, `{"type":"user","message":{"role":"user","content":"two"}}`, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result"}]}}`))

	// when
	offset := completedTranscriptEnd([]byte(first + turn))

	// then
	if offset != len(first) {
		t.Fatalf("completedTranscriptEnd() = %d, want %d", offset, len(first))
	}
}

func TestCompletedTranscriptEndKeepsCompletedHistoryBeforeBufferedPrompt(t *testing.T) {
	completed := lines(`{"type":"user","message":{"content":"completed prompt"}}`, `{"type":"assistant","message":{"stop_reason":"end_turn"}}`, `{"type":"last-prompt"}`)
	queue := `{"type":"queue-operation","operation":"enqueue","content":"next prompt"}`
	prompt := `{"type":"user","message":{"content":"next prompt"}}`
	tool := `{"type":"assistant","message":{"stop_reason":"tool_use"}}`
	metadata := `{"type":"last-prompt"}`
	for _, test := range []struct {
		name    string
		records []string
	}{
		{name: "buffered prompt"},
		{name: "queued prompt", records: []string{queue}},
		{name: "queue followed by metadata", records: []string{queue, metadata}},
		{name: "metadata between queue and prompt", records: []string{queue, metadata, prompt, tool}},
		{name: "recorded prompt and tool", records: []string{queue, prompt, tool}},
		{name: "queued follow-up during tool", records: []string{prompt, tool, queue}},
		{name: "earlier turn finishes with queued follow-up", records: []string{prompt, tool, queue, `{"type":"assistant","message":{"stop_reason":"end_turn"}}`}},
		{name: "follow-up starts after earlier turn", records: []string{prompt, tool, queue, `{"type":"assistant","message":{"stop_reason":"end_turn"}}`, prompt, tool}},
		{name: "subagent completion", records: []string{prompt, `{"type":"assistant","isSidechain":true,"message":{"stop_reason":"end_turn"}}`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := append([]byte{}, completed...)
			if len(test.records) > 0 {
				data = append(data, lines(test.records...)...)
			}
			if got := completedTranscriptEnd(data); got != len(completed) {
				t.Fatalf("offset = %d, want completed prefix %d", got, len(completed))
			}
		})
	}
}

func TestCompletedTranscriptEndKeepsFinishedQueuedFollowUp(t *testing.T) {
	data := lines(`{"type":"user","message":{"content":"first"}}`, `{"type":"assistant","message":{"stop_reason":"tool_use"}}`, `{"type":"queue-operation","operation":"enqueue","content":"follow up"}`, `{"type":"assistant","message":{"stop_reason":"end_turn"}}`, `{"type":"user","message":{"content":"follow up"}}`, `{"type":"assistant","message":{"stop_reason":"end_turn"}}`)
	if got := completedTranscriptEnd(data); got != len(data) {
		t.Fatalf("end = %d, want full completed transcript %d", got, len(data))
	}
}
