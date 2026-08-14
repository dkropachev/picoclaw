package gateway

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
)

func TestPublicLocalCISummaryBoundsInfrastructureStacks(t *testing.T) {
	value := "dependency download failed\n" + strings.Repeat("detail\n", 900) +
		"\nruntime stack:\n" + strings.Repeat("private stack\n", 900)
	got := publicLocalCISummary(value, localci.StatusInfrastructureError)
	if len(got) > (4<<10)+64 || strings.Contains(got, "runtime stack") || strings.Contains(got, "private stack") {
		t.Fatalf("publicLocalCISummary() leaked an unbounded stack (%d bytes)", len(got))
	}
}

func TestPublicLocalCISummaryPreservesConciseCodeFailure(t *testing.T) {
	value := "--- FAIL: TestGreeting\nwant hello, got goodbye"
	if got := publicLocalCISummary(value, localci.StatusFailed); got != value {
		t.Fatalf("publicLocalCISummary() = %q", got)
	}
}

func TestPublicLocalCISummaryScrubsStackAtByteZeroCRLFAndControls(t *testing.T) {
	tests := []string{
		"runtime stack:\r\nprivate frame\x00\r\n",
		"fatal error: newosproc\r\nprivate frame\x1b\r\n",
		"goroutine 1 [running]:\r\nprivate frame\x7f\r\n",
	}
	for _, value := range tests {
		if got := publicLocalCISummary(value, localci.StatusInfrastructureError); got != "" {
			t.Fatalf("publicLocalCISummary(%q) = %q, want empty scrubbed stack", value, got)
		}
	}
	got := publicLocalCISummary("compile\x00 failed\r\nline two\x1b", localci.StatusFailed)
	if got != "compile failed\nline two" {
		t.Fatalf("publicLocalCISummary() control scrub = %q", got)
	}
}
