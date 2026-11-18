package codex

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestCopyIdentityFieldsPreserveRecordedContent(t *testing.T) {
	const source = "01a1bdbd-8748-7a72-96ac-80bbb811e9c9"
	destination := copiedThreadID("workstation", "/workspace", source)
	ids := map[string]string{source: destination}
	const format = `{"payload":{"thread_id":%q,"item":{"sender_thread_id":%q,"agents_states":{%q:"running"},"text":%q},"item.sender_thread_id":%q}}`

	record := []byte(fmt.Sprintf(format, source, source, source, source, source))
	got, err := rewriteIdentityFields(record, nil, 0, record, "event_msg", ids)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(format, destination, destination, destination, source, source)
	if string(got) != want || len(got) != len(record) {
		t.Fatalf("unexpected rewritten record %s", got)
	}
	if bytes.Contains(record, []byte(destination)) {
		t.Fatal("rewriting changed the source buffer")
	}

	unknown := []byte(fmt.Sprintf(`{"payload":{"unfamiliar_thread_id":%q}}`, source))
	if _, err := rewriteIdentityFields(unknown, nil, 0, unknown, "event_msg", ids); err == nil || !strings.Contains(err.Error(), "unsupported Codex identity field") {
		t.Fatalf("unsupported identity field returned %v", err)
	}
}

func TestPlanRejectsArchiveMissingRevertBoundary(t *testing.T) {
	files := fixtureFiles(t, multiAgentOriginal, multiAgentSnapshot, multiAgentChild)
	end := files[1].meta.Base.EndByteOffset
	files[0].data = files[0].data[:end]
	files[0].kinds = files[0].kinds[:bytes.Count(files[0].data, []byte{'\n'})]
	session, err := testLayout.validateSession(multiAgentRoot, files)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{layout: testLayout, home: t.TempDir()}
	if _, err := store.planSession(session, false); err == nil || !strings.Contains(err.Error(), "first removed turn boundary") {
		t.Fatalf("missing revert boundary returned %v", err)
	}
}
