package promptir

import (
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func TestFromMessagesUsesSystemPartsScopes(t *testing.T) {
	msgs := []protocoltypes.Message{{
		Role:    "system",
		Content: "stable\nruntime\nsummary",
		SystemParts: []protocoltypes.ContentBlock{
			{Type: "text", Text: "stable", PromptSource: "runtime.kernel"},
			{Type: "text", Text: "runtime", PromptSlot: "runtime", PromptSource: "runtime.context"},
			{Type: "text", Text: "summary", PromptSlot: "summary", PromptSource: "context.summary"},
		},
	}}

	prompt := FromMessages(msgs)
	if len(prompt.Items) != 3 {
		t.Fatalf("items len = %d, want 3", len(prompt.Items))
	}
	if prompt.Items[0].Scope != ScopeStableInstruction {
		t.Fatalf("item[0].Scope = %q, want stable_instruction", prompt.Items[0].Scope)
	}
	if prompt.Items[1].Scope != ScopeRuntime {
		t.Fatalf("item[1].Scope = %q, want runtime", prompt.Items[1].Scope)
	}
	if prompt.Items[2].Scope != ScopeSummary {
		t.Fatalf("item[2].Scope = %q, want summary", prompt.Items[2].Scope)
	}
}

func TestMessagesRoundTripOrderedPartsAndMedia(t *testing.T) {
	parts := []Part{
		{Type: string(PartTypeText), Text: "before"},
		{Type: string(PartTypeImage), URI: "data:image/png;base64,abc", MIMEType: "image/png"},
		{Type: string(PartTypeText), Text: "after"},
	}
	msgs := []protocoltypes.Message{{
		Role:  "user",
		Parts: parts,
	}}

	roundTrip := ToMessages(FromMessages(msgs))
	if len(roundTrip) != 1 {
		t.Fatalf("roundTrip len = %d, want 1", len(roundTrip))
	}
	if !reflect.DeepEqual(roundTrip[0].Parts, parts) {
		t.Fatalf("parts = %#v, want %#v", roundTrip[0].Parts, parts)
	}
	if len(roundTrip[0].Media) != 1 || roundTrip[0].Media[0] != parts[1].URI {
		t.Fatalf("media = %#v, want image URI", roundTrip[0].Media)
	}
}

func TestToolCallAndResultIDsSurviveRoundTrip(t *testing.T) {
	msgs := []protocoltypes.Message{
		{
			Role: "assistant",
			ToolCalls: []protocoltypes.ToolCall{{
				ID:        "call_1",
				Name:      "lookup",
				Arguments: map[string]any{"n": 2},
			}},
		},
		{Role: "tool", ToolCallID: "call_1", Content: "ok"},
	}

	prompt := FromMessages(msgs)
	if len(prompt.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(prompt.Items))
	}
	if prompt.Items[0].ToolCallID != "call_1" || prompt.Items[1].ToolCallID != "call_1" {
		t.Fatalf("tool IDs not preserved: %#v", prompt.Items)
	}
	if got := ToolArgumentsMap(prompt.Items[0])["n"]; got != 2 {
		t.Fatalf("ToolArgumentsMap n = %#v, want int 2", got)
	}

	roundTrip := ToMessages(prompt)
	if roundTrip[0].ToolCalls[0].ID != "call_1" || roundTrip[1].ToolCallID != "call_1" {
		t.Fatalf("round trip IDs not preserved: %#v", roundTrip)
	}
}
