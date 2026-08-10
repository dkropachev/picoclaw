package agent

import (
	"regexp"
	"testing"
)

func TestControllerLocalRepairPromptDigestIsStableAndOpaque(t *testing.T) {
	t.Parallel()
	first := ControllerLocalRepairPromptDigest()
	second := ControllerLocalRepairPromptDigest()
	if first != second || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first) {
		t.Fatalf("ControllerLocalRepairPromptDigest() = %q, second = %q", first, second)
	}
	if first == localRepairSystemPrompt {
		t.Fatal("prompt digest exposed the prompt text")
	}
}
