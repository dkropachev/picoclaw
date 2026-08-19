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

func TestLocalRepairFullPromptDigestBindsInstructionAndSharedContext(t *testing.T) {
	t.Parallel()
	base := localRepairFullPromptDigest("fix retry", `{"message":"preserve order"}`)
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(base) {
		t.Fatalf("full prompt digest = %q", base)
	}
	if base == localRepairFullPromptDigest("fix retry fully", `{"message":"preserve order"}`) ||
		base == localRepairFullPromptDigest("fix retry", `{"message":"change order"}`) {
		t.Fatal("full repair prompt digest did not bind all prompt inputs")
	}
}
