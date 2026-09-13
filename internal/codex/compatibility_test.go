package codex

import (
	"reflect"
	"strings"
	"testing"
)

func TestCompatibilityTable(t *testing.T) {
	if got, want := SupportedVersions(), []string{"0.154.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SupportedVersions() = %v, want %v", got, want)
	}

	compatibility, err := LookupCompatibility("0.154.0")
	if err != nil {
		t.Fatal(err)
	}
	if compatibility.LayoutFingerprint != "rollout-v1:task-complete:workspace-metadata-v1" {
		t.Fatalf("unexpected layout fingerprint %q", compatibility.LayoutFingerprint)
	}
	if compatibility.RolloutFilenameLayout != "date-partitioned-rollout-v1" {
		t.Fatalf("unexpected rollout filename layout %q", compatibility.RolloutFilenameLayout)
	}
	if !compatibility.AppServer.ScanRepair || !compatibility.AppServer.Read ||
		!compatibility.AppServer.Resume || !compatibility.AppServer.Delete {
		t.Fatalf("incomplete App Server capabilities: %#v", compatibility.AppServer)
	}
}

func TestLookupCompatibilityFailsClosed(t *testing.T) {
	_, err := LookupCompatibility("9.9.9")
	if err == nil || !strings.Contains(err.Error(), "supported versions: 0.154.0") {
		t.Fatalf("LookupCompatibility() error = %v", err)
	}
}
