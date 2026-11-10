package pathmap

import (
	"errors"
	"strings"
	"testing"
)

func mustNew(t *testing.T, moves ...Move) Mapping {
	t.Helper()
	mapping, err := New(moves...)
	if err != nil {
		t.Fatal(err)
	}
	return mapping
}

func TestPathMovesPathsAtBoundariesOnly(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/home/me/project"})

	// when
	moved := mapping.Path("cd /work/project/src && ls /work/project-other /work/project")

	// then
	if want := "cd /home/me/project/src && ls /work/project-other /home/me/project"; moved != want {
		t.Fatalf("Path() = %q, want %q", moved, want)
	}
}

func TestPathPrefersTheLongestNestedSource(t *testing.T) {
	mapping := mustNew(t,
		Move{From: "/Users/a", To: "/Users/b"},
		Move{From: "/Users/a/.claude/projects/-Users-a", To: "/Users/b/.claude/projects/-Users-b"},
	)

	// when
	moved := mapping.Path("memory at /Users/a/.claude/projects/-Users-a/memory, cwd /Users/a")

	// then
	if want := "memory at /Users/b/.claude/projects/-Users-b/memory, cwd /Users/b"; moved != want {
		t.Fatalf("Path() = %q, want %q", moved, want)
	}
}

func TestRewriteKeepsEveryOtherByte(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/target"})
	// The unchanged string keeps an escape that Go would not write.
	record := []byte(`{"cwd" : "/work/project", "n":1.50, "nested":"{\"dir\":\"/work/project/a\"}", "text":"` + unicodeEscape("00e9") + ` /work/projects"}`)

	// when
	rewritten, err := mapping.Rewrite(record)

	// then
	if err != nil {
		t.Fatal(err)
	}
	want := `{"cwd" : "/target", "n":1.50, "nested":"{\"dir\":\"/target/a\"}", "text":"` + unicodeEscape("00e9") + ` /work/projects"}`
	if string(rewritten) != want {
		t.Fatalf("Rewrite() = %s, want %s", rewritten, want)
	}
}

func TestRewriteRejectsRecordsThatAlreadyMentionTheTarget(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/target"})

	for _, record := range []string{`{"cwd":"/work/project","note":"/target"}`, `{"note":"/target/x"}`} {
		// when
		_, err := mapping.Rewrite([]byte(record))

		// then
		if !errors.Is(err, ErrIrreversible) {
			t.Fatalf("Rewrite(%s) error = %v, want ErrIrreversible", record, err)
		}
	}
}

func TestRewriteRejectsStringsThatWouldNotEncodeBack(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/target"})

	// when
	_, err := mapping.Rewrite([]byte(`{"cwd":"` + unicodeEscape("002f") + `work/project"}`))

	// then
	if !errors.Is(err, ErrIrreversible) {
		t.Fatalf("Rewrite() error = %v, want ErrIrreversible", err)
	}
}

func TestRewriteWithoutMentionsReturnsTheRecord(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/target"})
	record := []byte(`{"cwd":"/elsewhere"}`)

	// when
	rewritten, err := mapping.Rewrite(record)

	// then
	if err != nil || string(rewritten) != string(record) {
		t.Fatalf("Rewrite() = %s, %v", rewritten, err)
	}
}

func TestRewriteRejectsAnUnterminatedString(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/target"})

	// when
	_, err := mapping.Rewrite([]byte(`{"cwd":"/work/project`))

	// then
	if err == nil {
		t.Fatal("Rewrite() accepted an unterminated string")
	}
}

func TestRewriteLinesKeepsLineEndingsAndNamesTheFailingLine(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/target"})

	// when
	rewritten, err := mapping.RewriteLines([]byte("{\"a\":\"/work/project\"}\n\n{\"b\":1}"))

	// then
	if err != nil || string(rewritten) != "{\"a\":\"/target\"}\n\n{\"b\":1}" {
		t.Fatalf("RewriteLines() = %q, %v", rewritten, err)
	}
	if _, err := mapping.RewriteLines([]byte("{}\n{\"a\":\"/target\"}\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("RewriteLines() error = %v", err)
	}
}

func TestNewRejectsRelativeAndRootPaths(t *testing.T) {
	for _, move := range []Move{
		{From: "relative", To: "/target"},
		{From: "/source", To: "relative"},
		{From: "/", To: "/target"},
	} {
		// when
		_, err := New(move)

		// then
		if err == nil {
			t.Fatalf("New(%+v) succeeded", move)
		}
	}
}

func TestRewriteDecodesRecordsWithEscapedSlashes(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/target"})

	// when
	_, err := mapping.Rewrite([]byte(`{"cwd":"\/work\/project\/a"}`))

	// then
	// The mention is found despite the escapes; re-encoding would drop them,
	// so the rewrite fails closed instead of skipping the record.
	if !errors.Is(err, ErrIrreversible) {
		t.Fatalf("Rewrite() error = %v, want ErrIrreversible", err)
	}
}

func TestEscapesPrintableASCII(t *testing.T) {
	for record, want := range map[string]bool{
		`{"a":"` + unicodeEscape("001b") + `[31m"}`: false,
		`{"a":"` + unicodeEscape("00e9") + `"}`:     false,
		`{"a":"` + unicodeEscape("0041") + `"}`:     true,
		`{"a":"` + unicodeEscape("002F") + `"}`:     true,
		`{"a":"x` + string(rune(92)) + `/y"}`:       true,
		`{"a":"` + unicodeEscape("00") + `"}`:       false,
	} {
		// when, then
		if got := escapesPrintableASCII([]byte(record)); got != want {
			t.Fatalf("escapesPrintableASCII(%s) = %t", record, got)
		}
	}
}

// unicodeEscape returns the JSON escape of a UTF-16 code unit in hex. Tests
// assemble escapes so that no tool along the way decodes them.
func unicodeEscape(hex string) string {
	return string(rune(92)) + "u" + hex
}
