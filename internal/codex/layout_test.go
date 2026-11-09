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
