package tools

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type probeProvider struct {
	response *providers.LLMResponse
	err      error
	messages *[]providers.Message
	tools    *[]providers.ToolDefinition
}

func (p probeProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	if p.messages != nil {
		*p.messages = append([]providers.Message(nil), messages...)
	}
	if p.tools != nil {
		*p.tools = append([]providers.ToolDefinition(nil), tools...)
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

func TestRunToolAdaptationProbeBoundaryFailures(t *testing.T) {
	tests := []struct {
		name            string
		provider        providers.LLMProvider
		wantError       string
		wantObservation bool
	}{
		{
			name:      "nil provider",
			provider:  nil,
			wantError: "provider is nil",
		},
		{
			name: "provider failure",
			provider: probeProvider{
				err: errors.New("probe transport unavailable"),
			},
			wantError:       "probe transport unavailable",
			wantObservation: true,
		},
		{
			name:            "nil provider response",
			provider:        probeProvider{},
			wantError:       "model did not call a tool",
			wantObservation: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))
			profile := ToolAdaptationProfile{Provider: "probe", Model: "boundary-model"}
			result := RunToolAdaptationProbe(
				context.Background(),
				test.provider,
				profile,
				config.ToolSurfaceSimple,
				"boundary-model",
			)
			if result.Success || !strings.Contains(result.Error, test.wantError) {
				t.Fatalf("result = %#v, want failure containing %q", result, test.wantError)
			}
			if result.ToolName != "adaptation_probe_echo" ||
				result.VisibleToolSurface != config.ToolSurfaceSimple || result.RanAt.IsZero() {
				t.Fatalf("failure identity = %#v", result)
			}

			outcomes := LatestToolAdaptationToolOutcomes(profile)
			if !test.wantObservation {
				if len(outcomes) != 0 {
					t.Fatalf("pre-dispatch failure recorded outcomes: %#v", outcomes)
				}
				return
			}
			if len(outcomes) != 1 || outcomes[0].Failures != 1 ||
				outcomes[0].Successes != 0 || !strings.Contains(outcomes[0].LastError, test.wantError) {
				t.Fatalf("failure outcomes = %#v, want one observed failure", outcomes)
			}
		})
	}
}

func TestRunToolAdaptationProbeCodexPromptIncludesExpectedArguments(t *testing.T) {
	resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))
	var messages []providers.Message
	var definitions []providers.ToolDefinition
	result := RunToolAdaptationProbe(
		context.Background(),
		probeProvider{
			response: &providers.LLMResponse{
				ToolCalls: []providers.ToolCall{{
					Name:      "exec_command",
					Arguments: map[string]any{"cmd": "printf probe-ok"},
				}},
			},
			messages: &messages,
			tools:    &definitions,
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
	if !strings.Contains(messages[0].Content, `{"cmd":"printf probe-ok"}`) {
		t.Fatalf("prompt = %q, want exact exec_command arguments", messages[0].Content)
	}
	if result.ToolName != "exec_command" || len(definitions) != 1 ||
		definitions[0].Function.Name != "exec_command" {
		t.Fatalf("probe definition/result = %#v / %#v", definitions, result)
	}
	if !strings.Contains(definitions[0].Function.Description, "without executing") {
		t.Fatalf("probe description = %q, want explicit non-execution", definitions[0].Function.Description)
	}
	wantSchema := NewCodexExecCommandTool(nil).Parameters()
	if !reflect.DeepEqual(definitions[0].Function.Parameters, wantSchema) {
		t.Fatalf(
			"probe schema = %#v, want production schema %#v",
			definitions[0].Function.Parameters,
			wantSchema,
		)
	}
}

func TestRunToolAdaptationProbeCodexRejectsNonExactOrMultipleCalls(t *testing.T) {
	tests := []struct {
		name      string
		toolCalls []providers.ToolCall
		wantError string
	}{
		{
			name: "removed argument",
			toolCalls: []providers.ToolCall{{
				Name: "exec_command",
				Arguments: map[string]any{
					"cmd":           "printf probe-ok",
					"yield_time_ms": 1,
				},
			}},
			wantError: "invalid arguments",
		},
		{
			name: "schema-valid optional extra",
			toolCalls: []providers.ToolCall{{
				Name: "exec_command",
				Arguments: map[string]any{
					"cmd":        "printf probe-ok",
					"background": false,
				},
			}},
			wantError: "unexpected arguments",
		},
		{
			name: "multiple calls",
			toolCalls: []providers.ToolCall{
				{Name: "exec_command", Arguments: map[string]any{"cmd": "printf probe-ok"}},
				{Name: "exec_command", Arguments: map[string]any{"cmd": "printf probe-ok"}},
			},
			wantError: "want exactly one",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))
			profile := ToolAdaptationProfile{Provider: "probe", Model: "model"}
			result := RunToolAdaptationProbe(
				context.Background(),
				probeProvider{response: &providers.LLMResponse{ToolCalls: test.toolCalls}},
				profile,
				config.ToolSurfaceCodex,
				"model",
			)
			if result.Success || !strings.Contains(result.Error, test.wantError) {
				t.Fatalf("result = %#v, want failure containing %q", result, test.wantError)
			}
			outcomes := LatestToolAdaptationToolOutcomes(profile)
			if len(outcomes) != 1 || outcomes[0].Failures != 1 || outcomes[0].Successes != 0 {
				t.Fatalf("outcomes = %#v, want one failure", outcomes)
			}
		})
	}
}
