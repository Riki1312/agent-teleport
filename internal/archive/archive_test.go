package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := testArchive(t, Payload{Path: "rollout.jsonl", Data: randomBytes(t)}, Payload{Path: "history/part-1.jsonl", Data: randomBytes(t)})

	// when
	encoded, err := original.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(encoded))

	// then
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Manifest.SessionID != original.Manifest.SessionID || len(decoded.Manifest.Files) != 2 {
		t.Fatalf("manifest changed: %#v", decoded.Manifest)
	}
	for _, payload := range original.Payloads {
		data, ok := decoded.Payload(payload.Path)
		if !ok || !bytes.Equal(data, payload.Data) {
			t.Fatalf("payload %s changed", payload.Path)
		}
	}
}

func TestNewRejectsUnsafePayloadPaths(t *testing.T) {
	for _, path := range []string{"", ".", "/absolute", "../escape", "a/../b", "a//b", `a\b`, "manifest.json"} {
		t.Run(path, func(t *testing.T) {
			// when
			_, err := New(testHeader(), []Payload{{Path: path, Data: []byte("data")}})

			// then
			if err == nil || !strings.Contains(err.Error(), "invalid payload path") {
				t.Fatalf("New() error = %v", err)
			}
		})
	}
}

func TestNewRejectsDuplicatePayloads(t *testing.T) {
	// when
	_, err := New(testHeader(), []Payload{{Path: "a"}, {Path: "a"}})

	// then
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestDecodeRejectsCorruptedPayload(t *testing.T) {
	original := testArchive(t, Payload{Path: "rollout.jsonl", Data: []byte("original")})
	original.Payloads[0].Data = []byte("modified")

	// when
	_, err := Decode(bytes.NewReader(rawArchive(t, original)))

	// then
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("Decode() error = %v", err)
	}
}

func TestDecodeRejectsUnlistedEntries(t *testing.T) {
	original := testArchive(t, Payload{Path: "rollout.jsonl", Data: []byte("data")})
	original.Payloads = append(original.Payloads, Payload{Path: "extra", Data: []byte("unlisted")})

	// when
	_, err := Decode(bytes.NewReader(rawArchive(t, original)))

	// then
	if err == nil || !strings.Contains(err.Error(), "unlisted") {
		t.Fatalf("Decode() error = %v", err)
	}
}

func TestDecodeRejectsNonArchive(t *testing.T) {
	// when, then
	if _, err := Decode(strings.NewReader("not an archive")); err == nil {
		t.Fatal("Decode accepted invalid input")
	}
}

func TestWriteFileKeepsExistingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session"+Extension)
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	// when
	err := testArchive(t, Payload{Path: "rollout.jsonl", Data: []byte("data")}).WriteFile(path)

	// then
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "existing" {
		t.Fatal("WriteFile changed an existing file")
	}
}

func TestWriteFileUsesOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session"+Extension)

	// when
	err := testArchive(t, Payload{Path: "rollout.jsonl", Data: []byte("data")}).WriteFile(path)

	// then
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %v, want 0600", info.Mode().Perm())
	}
}

func testHeader() Header {
	return Header{
		Provider:        "openai-codex",
		ProviderVersion: "0.154.0",
		SessionID:       "01999999-1111-7777-8888-222222222222",
		Workspace:       "/source/workspace",
		CreatedAt:       time.Now(),
	}
}

func testArchive(t *testing.T, payloads ...Payload) Archive {
	t.Helper()
	archive, err := New(testHeader(), payloads)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

// rawArchive packs an archive without validation so tests can build invalid input.
func rawArchive(t *testing.T, archive Archive) []byte {
	t.Helper()
	manifest, err := json.Marshal(archive.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range append([]Payload{{Path: "manifest.json", Data: manifest}}, archive.Payloads...) {
		if err := tarWriter.WriteHeader(&tar.Header{Name: entry.Path, Mode: 0o600, Size: int64(len(entry.Data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func randomBytes(t *testing.T) []byte {
	t.Helper()
	data := make([]byte, 256)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	return data
}
