package bundle

import (
	"bytes"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	rollout := []byte("sanitized rollout bytes\n")
	manifest := NewManifest(
		"openai-codex",
		"0.154.0",
		"01999999-1111-7777-8888-222222222222",
		"/source/workspace",
		time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC),
	)
	var encoded bytes.Buffer
	if err := Write(&encoded, manifest, rollout); err != nil {
		t.Fatal(err)
	}
	decoded, err := Read(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Manifest != manifest {
		t.Fatalf("manifest changed: %#v", decoded.Manifest)
	}
	if !bytes.Equal(decoded.Rollout, rollout) {
		t.Fatalf("rollout = %q, want %q", decoded.Rollout, rollout)
	}
}

func TestReadRejectsNonArchive(t *testing.T) {
	if _, err := Read(bytes.NewBufferString("not an archive")); err == nil {
		t.Fatal("Read accepted invalid input")
	}
}
