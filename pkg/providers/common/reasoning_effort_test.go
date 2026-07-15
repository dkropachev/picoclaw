package common

import "testing"

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "", want: "", ok: true},
		{input: "provider-default", want: "", ok: true},
		{input: "off", want: "none", ok: true},
		{input: "NONE", want: "none", ok: true},
		{input: "minimal", want: "minimal", ok: true},
		{input: "low", want: "low", ok: true},
		{input: "medium", want: "medium", ok: true},
		{input: "high", want: "high", ok: true},
		{input: "xhigh", want: "xhigh", ok: true},
		{input: "max", ok: false},
		{input: "adaptive", ok: false},
	}

	for _, tt := range tests {
		got, err := NormalizeReasoningEffort(tt.input)
		if tt.ok && err != nil {
			t.Fatalf("NormalizeReasoningEffort(%q) error = %v", tt.input, err)
		}
		if !tt.ok && err == nil {
			t.Fatalf("NormalizeReasoningEffort(%q) expected error", tt.input)
		}
		if got != tt.want {
			t.Fatalf("NormalizeReasoningEffort(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
