package e2e_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Riki1312/agent-teleport/internal/archive"
	"github.com/Riki1312/agent-teleport/internal/codex"
	"github.com/Riki1312/agent-teleport/internal/codex/appserver"
)

func TestCopyConflictingAncestor(t *testing.T) {
	binary := buildAgentTeleport(t)
	codexPath := requireCodex(t, binary)
	work := testWorkDir(t)
	home := mkdir(t, filepath.Join(work, "normal"))
	referenceHome := mkdir(t, filepath.Join(work, "reference"))
	cwd := mkdir(t, filepath.Join(work, "workspace"))

	const ancestor = "01a1bdbd-8748-7a72-96ac-80bbb811e9c9"
	const fork = "01a1c033-0a38-7d43-a289-532721dea6d7"
	const ancestorName = "rollout-2026-11-09T01-37-58-" + ancestor + ".jsonl"
	const forkName = "rollout-2026-11-09T13-05-32-" + fork + ".jsonl"
	readFixture := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(moduleRoot(t), "testdata", "codex", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	parentData, forkData := readFixture(ancestorName), readFixture(forkName)
	pack := func(label, id string, payloads ...archive.Payload) string {
		t.Helper()
		bundle, err := archive.New(archive.Header{
			Provider: codex.Provider, ProviderVersion: "0.167.0",
			SessionID: id, Workspace: "/workspace/source",
			Home: "/home/source/.codex", CreatedAt: time.Now(),
		}, payloads)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(work, label+archive.Extension)
		if err := bundle.WriteFile(path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	local := pack("local", ancestor, archive.Payload{Path: ancestorName, Data: divergedCopy(t, parentData)})
	run(t, binary, codexPath, home, nil, true, "import", local, "--cwd", cwd)
	before := historySnapshot(t, home)
	original := pack("original", fork,
		archive.Payload{Path: ancestorName, Data: parentData},
		archive.Payload{Path: forkName, Data: forkData},
	)
	run(t, binary, codexPath, home, nil, false, "import", original, "--cwd", cwd, "--dry-run")
	run(t, binary, codexPath, referenceHome, nil, true, "import", original, "--cwd", cwd)

	dryRun := run(t, binary, codexPath, home, nil, true, "import", original, "--cwd", cwd, "--copy-conflicts", "workstation", "--dry-run")
	if !equalHistory(before, historySnapshot(t, home)) {
		t.Fatal("dry run changed rollout files")
	}
	renamedAncestor := destinationThread(t, dryRun, ancestor)
	if renamedAncestor == ancestor {
		t.Fatal("conflicting ancestor kept its original ID")
	}
	actual := run(t, binary, codexPath, home, nil, true, "import", original, "--cwd", cwd, "--copy-conflicts", "workstation")
	assertOutput(t, actual, "imported Codex session "+fork)
	if destinationThread(t, actual, ancestor) != renamedAncestor {
		t.Fatal("the dry run and import planned different IDs")
	}
	after := historySnapshot(t, home)
	run(t, binary, codexPath, home, nil, true, "import", original, "--cwd", cwd, "--copy-conflicts", "workstation")
	if !equalHistory(after, historySnapshot(t, home)) {
		t.Fatal("repeat import changed rollout contents or added duplicate files")
	}
	// An interrupted append to a copied thread is recovered under the same ID.
	for name, data := range after {
		if !strings.HasSuffix(name, "-"+renamedAncestor+".jsonl") {
			continue
		}
		path := home + name
		marker := fmt.Sprintf("%d %d\n", len(data), len(data)+10)
		if err := os.WriteFile(path+".agent-teleport-append", []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data+"part"), 0o600); err != nil {
			t.Fatal(err)
		}
		run(t, binary, codexPath, home, nil, true, "import", original, "--cwd", cwd, "--copy-conflicts", "workstation")
		assertMissing(t, path+".agent-teleport-append")
		if !equalHistory(after, historySnapshot(t, home)) {
			t.Fatal("retry did not recover the copied thread's original history")
		}
	}
	for path, data := range before {
		if after[path] != data {
			t.Fatal("import changed the existing ancestor's rollout")
		}
	}

	// Another root from the same source can use the ancestor that was copied
	// for its fork. An update to that root also uses the same ID.
	ancestorArchive := pack("ancestor", ancestor, archive.Payload{Path: ancestorName, Data: parentData})
	parentResult := run(t, binary, codexPath, home, nil, true, "import", ancestorArchive, "--cwd", cwd, "--copy-conflicts", "workstation")
	if destinationThread(t, parentResult, ancestor) != renamedAncestor {
		t.Fatal("same-source import used a different ancestor ID")
	}

	readTurns := func(store, parent string) []string {
		t.Helper()
		client, err := appserver.Start(context.Background(), codexPath, store, "--disable", "goals")
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		if _, err := client.Resume(parent, ""); err != nil {
			t.Fatal(err)
		}
		thread, err := client.Resume(fork, "")
		if err != nil {
			t.Fatal(err)
		}
		var turns []string
		for _, turn := range thread.Turns {
			turns = append(turns, turn.ID)
		}
		return turns
	}
	want, got := readTurns(referenceHome, ancestor), readTurns(home, renamedAncestor)
	if len(want) != 3 || !slices.Equal(want, got) {
		t.Fatalf("native replay changed the fork's turn IDs: source %v, consolidated %v", want, got)
	}
}

func TestCopyRevertedHistoryWithSubAgents(t *testing.T) {
	binary := buildAgentTeleport(t)
	codexPath := requireCodex(t, binary)
	work := testWorkDir(t)
	source := mkdir(t, filepath.Join(work, "source"))
	target := mkdir(t, filepath.Join(work, "target"))
	cwd := mkdir(t, filepath.Join(work, "workspace"))

	multiAgent := revertedMultiAgentFixture(t)
	root := multiAgent.Manifest.SessionID
	write := func(label string, header archive.Header, files ...archive.Payload) string {
		t.Helper()
		data, err := archive.New(header, files)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(work, label+archive.Extension)
		if err := data.WriteFile(path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	full := write("multi-agent", multiAgent.Manifest.Header(), multiAgent.Payloads...)
	first := multiAgent.Payloads[0]
	first.Data = divergedCopy(t, first.Data)
	diverged := write("conflicting-local", multiAgent.Manifest.Header(), first)
	run(t, binary, codexPath, target, nil, true, "import", diverged, "--cwd", cwd)
	before := historySnapshot(t, target)
	run(t, binary, codexPath, source, nil, true, "import", full, "--cwd", cwd)

	// Import a fork first into a disposable home. Exporting the fork must
	// retain the start of the removed turn so native revert can replay it.
	forkID := forkSession(t, codexPath, source, root)
	forkArchive := filepath.Join(work, "fork"+archive.Extension)
	run(t, binary, codexPath, source, nil, true, "export", forkID, "-o", forkArchive)
	forkHome := mkdir(t, filepath.Join(work, "fork-target"))
	run(t, binary, codexPath, forkHome, nil, true, "import", diverged, "--cwd", cwd)
	run(t, binary, codexPath, forkHome, nil, true, "import", forkArchive, "--cwd", cwd, "--copy-conflicts", "workstation")
	assertResumed(t, codexPath, forkHome, forkID, cwd, 3)
	run(t, binary, codexPath, forkHome, nil, true, "import", forkArchive, "--cwd", cwd, "--copy-conflicts", "workstation")

	// Consolidate the full root, including its reverts and spawned thread.
	out := run(t, binary, codexPath, target, nil, true, "import", full, "--cwd", cwd, "--copy-conflicts", "workstation")
	copied := destinationThread(t, out, root)
	assertResumed(t, codexPath, target, copied, cwd, 3)
	run(t, binary, codexPath, target, nil, true, "import", full, "--cwd", cwd, "--copy-conflicts", "workstation")
	after := historySnapshot(t, target)
	for path, data := range before {
		if after[path] != data {
			t.Fatal("consolidation changed the original root's rollout")
		}
	}
	roundtrip := filepath.Join(work, "roundtrip"+archive.Extension)
	run(t, binary, codexPath, target, nil, true, "export", copied, "-o", roundtrip)
	if got := len(decodeArchive(t, roundtrip).Payloads); got != 3 {
		t.Fatalf("copied session exports %d rollouts, want original, snapshot, and sub-agent", got)
	}
}

func destinationThread(t *testing.T, result []byte, source string) string {
	t.Helper()
	for _, line := range strings.Split(string(result), "\n") {
		if id, ok := strings.CutPrefix(line, "incoming thread "+source+" uses "); ok {
			return id
		}
	}
	t.Fatalf("no copied ID for %s in %s", source, result)
	return ""
}
