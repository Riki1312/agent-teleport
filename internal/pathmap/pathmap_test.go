package pathmap

import (
	"errors"
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
	record := []byte(`{"cwd" : "/work/project", "n":1.50, "nested":"{\"dir\":\"/work/project/a\"}", "text":"\u00e9 /work/projects"}`)

	// when
	rewritten, changed, err := mapping.Rewrite(record)

	// then
	if err != nil {
		t.Fatal(err)
	}
	want := `{"cwd" : "/target", "n":1.50, "nested":"{\"dir\":\"/target/a\"}", "text":"\u00e9 /work/projects"}`
	if string(rewritten) != want || changed != 2 {
		t.Fatalf("Rewrite() = %s (%d changed), want %s", rewritten, changed, want)
	}
}

func TestRewriteLimitedToMembersLeavesOtherValues(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/target"})
	record := []byte(`{"message":{"content":"read /work/project/a"},"cwd":"/work/project","sessionId":"s"}`)

	// when
	rewritten, changed, err := mapping.Rewrite(record, "cwd")

	// then
	if err != nil {
		t.Fatal(err)
	}
	want := `{"message":{"content":"read /work/project/a"},"cwd":"/target","sessionId":"s"}`
	if string(rewritten) != want || changed != 1 {
		t.Fatalf("Rewrite() = %s (%d changed), want %s", rewritten, changed, want)
	}
}

func TestRewriteRejectsRecordsThatAlreadyMentionTheTarget(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/target"})

	// when
	_, _, err := mapping.Rewrite([]byte(`{"cwd":"/work/project","note":"/target"}`))

	// then
	if !errors.Is(err, ErrIrreversible) {
		t.Fatalf("Rewrite() error = %v, want ErrIrreversible", err)
	}
}

func TestRewriteWithoutMentionsReturnsTheRecord(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/target"})
	record := []byte(`{"cwd":"/elsewhere"}`)

	// when
	rewritten, changed, err := mapping.Rewrite(record, "cwd")

	// then
	if err != nil || changed != 0 || string(rewritten) != string(record) {
		t.Fatalf("Rewrite() = %s, %d, %v", rewritten, changed, err)
	}
}

func TestRewriteOfMembersRequiresAnObject(t *testing.T) {
	mapping := mustNew(t, Move{From: "/work/project", To: "/target"})

	for _, record := range []string{`["/work/project"]`, `{"cwd":"/work/project"} {}`, `{"cwd":`} {
		// when
		_, _, err := mapping.Rewrite([]byte(record), "cwd")

		// then
		if err == nil {
			t.Fatalf("Rewrite(%s) succeeded", record)
		}
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
