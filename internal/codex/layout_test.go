package codex

import (
	"strings"
	"testing"
)

func TestLayoutForUnsupportedVersionFailsClosed(t *testing.T) {
	// when
	_, err := layoutFor("9.9.9")

	// then
	if err == nil || !strings.Contains(err.Error(), "supported versions: 0.154.0") {
		t.Fatalf("layoutFor() error = %v", err)
	}
}
