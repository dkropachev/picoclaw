//nolint:govet // Independent streaming assertions intentionally reuse narrow error names.
package providers

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/isolation"
)

type coverageStreamingProvider struct {
	toolCaptureProvider
	closed bool
	chunks int
}

func (provider *coverageStreamingProvider) ChatStream(
	_ context.Context,
	_ []Message,
	tools []ToolDefinition,
	_ string,
	_ map[string]any,
	onChunk func(string),
) (*LLMResponse, error) {
	provider.lastTools = tools
	provider.chunks++
	if onChunk != nil {
		onChunk("streamed")
	}
	return &LLMResponse{Content: "streamed"}, nil
}

func (provider *coverageStreamingProvider) SupportsThinking() bool     { return true }
func (provider *coverageStreamingProvider) SupportsNativeSearch() bool { return true }
func (provider *coverageStreamingProvider) Close()                     { provider.closed = true }

type coverageEventProvider struct{ coverageStreamingProvider }

func (provider *coverageEventProvider) ChatStreamEvents(
	_ context.Context,
	_ []Message,
	tools []ToolDefinition,
	_ string,
	_ map[string]any,
	onChunk func(StreamChunk),
) (*LLMResponse, error) {
	provider.lastTools = tools
	if onChunk != nil {
		onChunk(StreamChunk{Content: "event"})
	}
	return &LLMResponse{Content: "event"}, nil
}

func coverageToolDefinitions() []ToolDefinition {
	return []ToolDefinition{{
		Type: "function",
		Function: ToolFunctionDefinition{
			Name: "tool",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{"type": "string", "format": "date-time"},
				},
			},
		},
	}}
}

func TestCoverageToolSchemaStreamingCapabilitiesAndClose(t *testing.T) {
	if wrapped, err := wrapProviderWithToolSchemaTransform(nil, "simple"); err != nil || wrapped != nil {
		t.Fatalf("nil transformed provider = %#v, %v", wrapped, err)
	}
	if wrapped, err := wrapProviderWithToolSchemaTransform(
		&toolCaptureProvider{},
		"invalid",
	); err == nil ||
		wrapped != nil {
		t.Fatalf("invalid transform = %#v, %v", wrapped, err)
	}
	delegate := &coverageStreamingProvider{}
	wrapped, err := wrapProviderWithToolSchemaTransform(delegate, "simple")
	if err != nil {
		t.Fatal(err)
	}
	streaming, ok := wrapped.(*toolSchemaStreamingProvider)
	if !ok {
		t.Fatalf("streaming wrapper = %T", wrapped)
	}
	var accumulated string
	if response, err := streaming.ChatStream(
		t.Context(), nil, coverageToolDefinitions(), "model", nil,
		func(value string) { accumulated = value },
	); err != nil || response.Content != "streamed" || accumulated != "streamed" {
		t.Fatalf("stream response = %#v, %v, chunk=%q", response, err, accumulated)
	}
	var event StreamChunk
	if response, err := streaming.ChatStreamEvents(
		t.Context(), nil, coverageToolDefinitions(), "model", nil,
		func(value StreamChunk) { event = value },
	); err != nil || response.Content != "streamed" || event.Content != "streamed" {
		t.Fatalf("fallback event stream = %#v, %v, event=%#v", response, err, event)
	}
	if !streaming.SupportsThinking() || !streaming.SupportsNativeSearch() {
		t.Fatal("capabilities were not delegated")
	}
	streaming.Close()
	if !delegate.closed {
		t.Fatal("stateful delegate was not closed")
	}

	eventDelegate := &coverageEventProvider{}
	wrapped, err = wrapProviderWithToolSchemaTransform(eventDelegate, "simple")
	if err != nil {
		t.Fatal(err)
	}
	streaming = wrapped.(*toolSchemaStreamingProvider)
	event = StreamChunk{}
	if response, err := streaming.ChatStreamEvents(
		t.Context(), nil, coverageToolDefinitions(), "model", nil,
		func(value StreamChunk) { event = value },
	); err != nil || response.Content != "event" || event.Content != "event" {
		t.Fatalf("native event stream = %#v, %v, event=%#v", response, err, event)
	}

	plain := &toolSchemaTransformProvider{delegate: &toolCaptureProvider{}, transform: "simple"}
	if plain.SupportsThinking() || plain.SupportsNativeSearch() {
		t.Fatal("plain delegate advertised optional capabilities")
	}
	plain.Close()
}

func TestCoverageProviderFacadeConstructors(t *testing.T) {
	if NewAntigravityProvider() == nil || NewAntigravityProviderForCredential("credential") == nil {
		t.Fatal("Antigravity provider facade returned nil")
	}
	if NewClaudeProvider("token") == nil || NewClaudeProviderWithBaseURL("token", "http://localhost") == nil ||
		NewClaudeProviderWithTokenSource("token", func() (string, error) { return "next", nil }) == nil ||
		NewClaudeProviderWithTokenSourceAndBaseURL(
			"token", func() (string, error) { return "next", nil }, "http://localhost",
		) == nil {
		t.Fatal("Claude provider facade returned nil")
	}
	if NewCodexProvider("token", "account") == nil || NewCodexProviderWithTokenSource(
		"token", "account", func() (string, string, error) { return "next", "account", nil },
	) == nil {
		t.Fatal("Codex provider facade returned nil")
	}
	if createClaudeTokenSourceForCredential("credential") == nil ||
		createCodexTokenSourceForCredential("credential") == nil {
		t.Fatal("credential token-source facade returned nil")
	}
	if NewClaudeCliProvider("workspace") == nil ||
		NewClaudeCliProviderWithExecutionPolicy("workspace", isolation.ExecutionPolicy{}) == nil ||
		NewCodexCliProvider("workspace") == nil ||
		NewCodexCliProviderWithExecutionPolicy("workspace", isolation.ExecutionPolicy{}) == nil {
		t.Fatal("CLI provider facade returned nil")
	}
	if source := CreateCodexCliTokenSource(); source == nil {
		t.Fatal("Codex CLI token source facade returned nil")
	}
	call := ToolCall{Function: &FunctionCall{Name: "tool", Arguments: `{"value":1}`}}
	if normalized := NormalizeToolCall(call); normalized.Function.Name != "tool" {
		t.Fatalf("normalized tool call = %#v", normalized)
	}
	if provider, err := NewGitHubCopilotProvider("bad://endpoint", "stdio", "model"); err == nil && provider == nil {
		t.Fatal("GitHub Copilot URI facade returned neither provider nor error")
	}
	if provider, err := NewGitHubCopilotProviderWithToken("invalid", "model"); err == nil && provider == nil {
		t.Fatal("GitHub Copilot token facade returned neither provider nor error")
	}
}

func TestCoverageProviderCatalogQueries(t *testing.T) {
	options := ModelProviderOptions()
	if len(options) == 0 {
		t.Fatal("provider catalog is empty")
	}
	for index := 1; index < len(options); index++ {
		if options[index-1].ID > options[index].ID {
			t.Fatal("provider catalog is not sorted")
		}
	}
	if !IsSupportedModelProvider("copilot") || IsSupportedModelProvider("unknown") ||
		!IsModelProviderFetchable("openai") || IsModelProviderFetchable("anthropic") ||
		!IsCreatableModelProvider("openai") || IsCreatableModelProvider("router") {
		t.Fatal("provider capability query mismatch")
	}
	models := CommonModelsForProvider("openai")
	if len(models) == 0 || CommonModelsForProvider("unknown") != nil {
		t.Fatal("common model query mismatch")
	}
	models[0] = "mutated"
	if CommonModelsForProvider("openai")[0] == "mutated" {
		t.Fatal("common model query leaked catalog slice")
	}
	for provider, want := range map[string]bool{
		"openai": true, "anthropic": true, "github-copilot": true,
		"elevenlabs": false, "bedrock": false, "unknown": false,
	} {
		if got := SupportsAccountStoreCredentials(provider); got != want {
			t.Fatalf("stored credential support %q = %v, want %v", provider, got, want)
		}
	}
	if provider, model := SplitModelProviderAndID(
		"openai/gpt-5",
		"anthropic",
	); provider != "openai" ||
		model != "gpt-5" {
		t.Fatalf("known model split = %q/%q", provider, model)
	}
	if provider, model := SplitModelProviderAndID(
		"namespace/model",
		"anthropic",
	); provider != "anthropic" ||
		model != "namespace/model" {
		t.Fatalf("unknown model split = %q/%q", provider, model)
	}
	if provider, model := SplitModelProviderAndID(" ", "openai"); provider != "" || model != "" {
		t.Fatalf("empty model split = %q/%q", provider, model)
	}
	if model, err := ResolveModelForProvider("openai", "openai/gpt-5"); err != nil || model != "gpt-5" {
		t.Fatalf("resolved provider model = %q, %v", model, err)
	}
}
