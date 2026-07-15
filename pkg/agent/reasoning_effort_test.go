package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestApplyReasoningEffortOption(t *testing.T) {
	opts := map[string]any{"reasoning_effort": "low"}
	applyReasoningEffortOption(opts, &config.ModelConfig{ReasoningEffort: "HIGH"})

	if got := opts["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", got)
	}
}

func TestApplyReasoningEffortOptionClearsStaleValue(t *testing.T) {
	opts := map[string]any{"reasoning_effort": "high"}
	applyReasoningEffortOption(opts, &config.ModelConfig{})

	if _, ok := opts["reasoning_effort"]; ok {
		t.Fatalf("reasoning_effort should be cleared, got %#v", opts["reasoning_effort"])
	}
}

func TestHasReasoningEffortConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.ModelConfig
		want bool
	}{
		{name: "nil"},
		{name: "empty", cfg: &config.ModelConfig{}},
		{name: "valid", cfg: &config.ModelConfig{ReasoningEffort: "HIGH"}, want: true},
		{name: "max unsupported", cfg: &config.ModelConfig{ReasoningEffort: "max"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasReasoningEffortConfig(tt.cfg); got != tt.want {
				t.Fatalf("hasReasoningEffortConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}
