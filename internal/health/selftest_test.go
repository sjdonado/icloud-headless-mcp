package health

import (
	"strings"
	"testing"
)

func TestSelfTest(t *testing.T) {
	report, ok := SelfTest()
	if !ok {
		t.Fatalf("self-test failed: %s", report)
	}
	for _, want := range []string{"ok envelope-quantity", "ok envelope-category", "ok unknown-metric", "ok early-tombstone", "ok reimport-zero-new", "ok day-rollup"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q: %s", want, report)
		}
	}
}
