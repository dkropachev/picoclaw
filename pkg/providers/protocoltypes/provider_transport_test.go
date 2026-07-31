package protocoltypes

import "testing"

func TestUsesOpenAICompatibleHTTPTransport(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{provider: "openai", want: true},
		{provider: "litellm", want: true},
		{provider: "openrouter", want: true},
		{provider: "anthropic", want: false},
		{provider: "anthropic-messages", want: false},
		{provider: "custom-openai-compatible", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			if got := UsesOpenAICompatibleHTTPTransport(tt.provider); got != tt.want {
				t.Fatalf(
					"UsesOpenAICompatibleHTTPTransport(%q) = %v, want %v",
					tt.provider,
					got,
					tt.want,
				)
			}
		})
	}
}
