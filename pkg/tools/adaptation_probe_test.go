package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type probeProvider struct {
	response *providers.LLMResponse
	err      error
	messages *[]providers.Message
}

func (p probeProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	if p.messages != nil {
		*p.messages = append([]providers.Message(nil), messages...)
	}
	return p.response, p.err
}

func (p probeProvider) GetDefaultModel() string {
	return "probe-model"
}

func TestRunToolAdaptationProbeSuccess(t *testing.T) {
	resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))
	profile := ToolAdaptationProfile{Provider: "probe", Model: "model"}
	result := RunToolAdaptationProbe(
		context.Background(),
		probeProvider{response: &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{{
				Name:      "adaptation_probe_echo",
				Arguments: map[string]any{"value": "probe-ok"},
			}},
		}},
		profile,
		config.ToolSurfaceSimple,
		"model",
	)

	if !result.Success {
		t.Fatalf("Success = false, error=%q", result.Error)
	}
	outcomes := LatestToolAdaptationToolOutcomes(profile)
	if len(outcomes) != 1 || outcomes[0].Successes != 1 {
		t.Fatalf("outcomes = %#v, want one success", outcomes)
	}
}

func TestRunToolAdaptationProbeFailure(t *testing.T) {
	resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))
	profile := ToolAdaptationProfile{Provider: "probe", Model: "model"}
	result := RunToolAdaptationProbe(
		context.Background(),
		probeProvider{response: &providers.LLMResponse{
			ToolCalls: []providers.ToolCall{{
				Name:      "wrong_tool",
				Arguments: map[string]any{"value": "probe-ok"},
			}},
		}},
		profile,
		config.ToolSurfaceSimple,
		"model",
	)

	if result.Success {
		t.Fatal("Success = true, want false")
	}
	outcomes := LatestToolAdaptationToolOutcomes(profile)
	if len(outcomes) != 1 || outcomes[0].Failures != 1 {
		t.Fatalf("outcomes = %#v, want one failure", outcomes)
	}
}

func TestRunToolAdaptationProbeCodexPromptIncludesExpectedArguments(t *testing.T) {
	resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))
	var messages []providers.Message
	result := RunToolAdaptationProbe(
		context.Background(),
		probeProvider{
			response: &providers.LLMResponse{
				ToolCalls: []providers.ToolCall{{
					Name: "update_plan",
					Arguments: map[string]any{
						"plan": []any{map[string]any{
							"step":   "probe",
							"status": "completed",
						}},
					},
				}},
			},
			messages: &messages,
		},
		ToolAdaptationProfile{Provider: "probe", Model: "model"},
		config.ToolSurfaceCodex,
		"model",
	)
	if !result.Success {
		t.Fatalf("Success = false, error=%q", result.Error)
	}
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	if !strings.Contains(messages[0].Content, `{"plan":[{"step":"probe","status":"completed"}]}`) {
		t.Fatalf("prompt = %q, want exact update_plan arguments", messages[0].Content)
	}
}
