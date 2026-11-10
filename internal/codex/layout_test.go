package codex

import (
	"strings"
	"testing"
)

func TestLayoutForVersions(t *testing.T) {
	for _, test := range []struct {
		version   string
		supported bool
		tested    bool
	}{
		{version: "0.164.9", supported: false},
		{version: "0.165.0-alpha.1", supported: false},
		{version: "0.165.0", supported: true, tested: true},
		{version: "0.167.0", supported: true, tested: true},
		{version: "0.168.0-alpha.1", supported: true, tested: false},
		{version: "1.0.0", supported: true, tested: false},
	} {
		t.Run(test.version, func(t *testing.T) {
			// when
			_, tested, err := layoutFor(test.version)

			// then
			if (err == nil) != test.supported || tested != test.tested {
				t.Fatalf("layoutFor() tested = %t, error = %v", tested, err)
			}
		})
	}
}

func TestLayoutForRejectsUnparsableVersions(t *testing.T) {
	for _, version := range []string{"", "0.167", "v0.167.0", "0.x.0"} {
		// when
		_, _, err := layoutFor(version)

		// then
		if err == nil || !strings.Contains(err.Error(), "unrecognized") {
			t.Fatalf("layoutFor(%q) error = %v", version, err)
		}
	}
}

func TestClassifyEventReadsTheTopLevelPayloadType(t *testing.T) {
	for line, want := range map[string]eventKind{
		`{"type":"event_msg","payload":{"type":"task_started"}}`: {eventType: "event_msg", payloadType: "task_started"},
		`{"payload":{"info":{"type":"nested"},"text":"a } \"type\": x","list":[{"type":"no"}],"n":1.5, "type" : "task_complete"},"type":"event_msg"}`: {eventType: "event_msg", payloadType: "task_complete"},
		`{"type":"session_meta","payload":{"id":"x"}}`: {eventType: "session_meta"},
		`{"type":"compacted","payload":"text"}`:        {eventType: "compacted"},
	} {
		// when
		kind, err := classifyEvent([]byte(line))

		// then
		if err != nil || kind != want {
			t.Fatalf("classifyEvent(%s) = %+v, %v; want %+v", line, kind, err, want)
		}
	}
	if _, err := classifyEvent([]byte(`{"type":"event_msg","payload":{"type":`)); err == nil {
		t.Fatal("classifyEvent accepted a truncated record")
	}
}
