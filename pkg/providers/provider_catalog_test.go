package providers

import "testing"

func TestNormalizeProviderMatchesProviderCatalog(t *testing.T) {
	t.Parallel()

	for provider, option := range modelProviderOptionsByName {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeProvider(provider); got != provider {
				t.Fatalf("NormalizeProvider(%q) = %q, want %q", provider, got, provider)
			}
			for _, alias := range option.Aliases {
				if got := NormalizeProvider(alias); got != provider {
					t.Errorf(
						"NormalizeProvider(%q) = %q, want %q",
						alias,
						got,
						provider,
					)
				}
			}
		})
	}
}

func TestResolveModelForProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		model    string
		want     string
		wantErr  string
	}{
		{
			name:     "bare model",
			provider: "openai",
			model:    "gpt-5.4",
			want:     "gpt-5.4",
		},
		{
			name:     "matching provider prefix",
			provider: "openai",
			model:    "openai/gpt-5.4",
			want:     "gpt-5.4",
		},
		{
			name:     "provider-specific namespace",
			provider: "nvidia",
			model:    "nvidia/openai/gpt-oss-120b",
			want:     "openai/gpt-oss-120b",
		},
		{
			name:     "mismatched provider prefix",
			provider: "github-copilot",
			model:    "openai/gpt-5.4",
			wantErr:  `model provider "openai" does not match account provider "github-copilot"`,
		},
		{
			name:     "missing model",
			provider: "openai",
			wantErr:  "no model configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveModelForProvider(tt.provider, tt.model)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("ResolveModelForProvider() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveModelForProvider() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ResolveModelForProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}
