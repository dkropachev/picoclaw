package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	picotools "github.com/sipeed/picoclaw/pkg/tools"
)

func TestApplyToolAdaptationSurfaceSimpleTransformsSchemas(t *testing.T) {
	defs := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: "probe",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"value": map[string]any{"type": "string"},
				},
			},
		},
	}}

	got := applyToolAdaptationSurface(config.ToolSurfaceSimple, defs)
	if _, ok := got[0].Function.Parameters["additionalProperties"]; ok {
		t.Fatalf("simple surface kept additionalProperties: %#v", got[0].Function.Parameters)
	}
	if _, ok := defs[0].Function.Parameters["additionalProperties"]; !ok {
		t.Fatalf("simple surface mutated original defs: %#v", defs[0].Function.Parameters)
	}
}

func TestApplyToolAdaptationSurfaceHidesCodexToolsForNonCodexSurface(t *testing.T) {
	defs := toolDefsForAdaptationTest("exec", "exec_command", "write_stdin", "read_file", "apply_patch")

	got := applyToolAdaptationSurface(config.ToolSurfacePicoClaw, defs)
	gotNames := toolDefNamesForAdaptationTest(got)
	want := []string{"exec", "read_file"}
	if fmt.Sprint(gotNames) != fmt.Sprint(want) {
		t.Fatalf("tool names = %v, want %v", gotNames, want)
	}
}

func TestApplyToolAdaptationSurfaceCodexHidesNativeReplacedTools(t *testing.T) {
	defs := toolDefsForAdaptationTest("exec", "exec_command", "write_stdin", "read_file", "apply_patch", "edit_file")

	got := applyToolAdaptationSurface(config.ToolSurfaceCodex, defs)
	gotNames := toolDefNamesForAdaptationTest(got)
	want := []string{"exec_command", "write_stdin", "read_file", "apply_patch"}
	if fmt.Sprint(gotNames) != fmt.Sprint(want) {
		t.Fatalf("tool names = %v, want %v", gotNames, want)
	}
}

func TestMergeHookToolDefinitionChangesRemovesAdaptationEquivalents(t *testing.T) {
	base := toolDefsForAdaptationTest(
		"exec",
		"exec_command",
		"write_stdin",
		"write_file",
		"edit_file",
		"append_file",
		"apply_patch",
		"read_file",
	)
	before := filterToolDefinitionsForAdaptationSurface(config.ToolSurfacePicoClaw, base)
	after := make([]providers.ToolDefinition, 0, len(before))
	for _, def := range before {
		if def.Function.Name != "exec" {
			after = append(after, def)
		}
	}

	got := mergeHookToolDefinitionChanges(base, before, after)
	gotNames := toolDefNamesForAdaptationTest(got)
	want := []string{"write_file", "edit_file", "append_file", "apply_patch", "read_file"}
	if fmt.Sprint(gotNames) != fmt.Sprint(want) {
		t.Fatalf("tool names = %v, want %v", gotNames, want)
	}
}

func TestEffectiveToolAdaptationSurfaceUsesImmediateLearnedChange(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Adaptation.ApplyVisibleChanges = config.ToolVisibleChangeImmediate
	cfg.Tools.Adaptation.CacheSensitiveAPIs = config.ToolCacheSensitivityNever
	profile := picotools.ToolAdaptationProfile{
		Provider: "local",
		Model:    fmt.Sprintf("model-%d", time.Now().UnixNano()),
	}
	_, ok := picotools.ObserveToolAdaptationToolOutcome(
		profile,
		config.ToolSurfaceSimple,
		"read_file",
		true,
		"",
		0,
	)
	if !ok {
		t.Fatal("ObserveToolAdaptationToolOutcome() ok = false, want true")
	}

	ts := &turnState{
		agent: &AgentInstance{
			ToolAdaptation: picotools.ToolAdaptationDecision{
				Enabled:             true,
				PinnedToolSurface:   config.ToolSurfacePicoClaw,
				VisibleToolSurface:  config.ToolSurfacePicoClaw,
				RuntimePromotion:    true,
				RuntimeDowngrade:    true,
				ApplyVisibleChanges: config.ToolVisibleChangeImmediate,
			},
		},
	}
	exec := &turnExecution{
		activeModelConfig: &config.ModelConfig{Provider: profile.Provider},
		activeModel:       profile.Model,
	}

	got := effectiveToolAdaptationSurfaceForTurn(cfg, ts, exec)
	if got != config.ToolSurfaceSimple {
		t.Fatalf("effective surface = %q, want %q", got, config.ToolSurfaceSimple)
	}
}

func TestEffectiveToolAdaptationSurfaceUsesRoutedProfileOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Adaptation.ProfileOverrides = []config.ToolAdaptationProfileOverride{{
		Provider:           "anthropic",
		Model:              "claude-sonnet",
		VisibleToolSurface: config.ToolSurfaceSimple,
	}}

	ts := &turnState{
		agent: &AgentInstance{
			ToolAdaptation: picotools.ToolAdaptationDecision{
				Enabled:             true,
				PinnedToolSurface:   config.ToolSurfaceCodex,
				VisibleToolSurface:  config.ToolSurfaceCodex,
				ApplyVisibleChanges: config.ToolVisibleChangeNextSession,
			},
		},
	}
	exec := &turnExecution{
		activeCandidates: []providers.FallbackCandidate{{
			Provider: "claude",
			Model:    "claude-sonnet",
		}},
		activeModelConfig: &config.ModelConfig{
			Provider: config.AccountRouterProvider,
			Model:    "claude-sonnet",
		},
	}

	got := effectiveToolAdaptationSurfaceForTurn(cfg, ts, exec)
	if got != config.ToolSurfaceSimple {
		t.Fatalf("effective surface = %q, want routed profile override %q", got, config.ToolSurfaceSimple)
	}
	profile := toolAdaptationProfileForTurn(cfg, exec)
	if profile.Provider != "anthropic" {
		t.Fatalf("profile provider = %q, want canonical provider %q", profile.Provider, "anthropic")
	}
}

func TestEffectiveToolAdaptationSurfaceDoesNotTreatCacheOnlyOverrideAsSurfaceChange(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Adaptation.ProfileOverrides = []config.ToolAdaptationProfileOverride{{
		Provider:           "anthropic",
		Model:              "claude-sonnet",
		CacheSensitiveAPIs: config.ToolCacheSensitivityNever,
	}}

	ts := &turnState{
		agent: &AgentInstance{
			ToolAdaptation: picotools.ToolAdaptationDecision{
				Enabled:             true,
				PinnedToolSurface:   config.ToolSurfaceCodex,
				VisibleToolSurface:  config.ToolSurfaceCodex,
				ApplyVisibleChanges: config.ToolVisibleChangeNextSession,
			},
		},
	}
	exec := &turnExecution{
		activeCandidates: []providers.FallbackCandidate{{
			Provider: "anthropic",
			Model:    "claude-sonnet",
		}},
	}

	got := effectiveToolAdaptationSurfaceForTurn(cfg, ts, exec)
	if got != config.ToolSurfaceCodex {
		t.Fatalf("effective surface = %q, want startup pin %q", got, config.ToolSurfaceCodex)
	}
}

func TestEffectiveToolAdaptationSurfaceAutoOverrideObeysRuntimePolicy(t *testing.T) {
	t.Setenv("PICOCLAW_HOME", t.TempDir())
	model := fmt.Sprintf("claude-auto-%d", time.Now().UnixNano())
	cfg := config.DefaultConfig()
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Adaptation.VisibleToolSurface = config.ToolSurfaceCodex
	cfg.Tools.Adaptation.CacheSensitiveAPIs = config.ToolCacheSensitivityNever
	cfg.Tools.Adaptation.ProfileOverrides = []config.ToolAdaptationProfileOverride{{
		Provider:           "anthropic",
		Model:              model,
		VisibleToolSurface: config.ToolSurfaceAuto,
	}}

	ts := &turnState{
		agent: &AgentInstance{
			ToolAdaptation: picotools.ToolAdaptationDecision{
				Enabled:             true,
				PinnedToolSurface:   config.ToolSurfaceCodex,
				VisibleToolSurface:  config.ToolSurfaceCodex,
				RuntimePromotion:    true,
				RuntimeDowngrade:    true,
				ApplyVisibleChanges: config.ToolVisibleChangeNextSession,
			},
		},
	}
	exec := &turnExecution{
		activeCandidates: []providers.FallbackCandidate{{
			Provider: "anthropic",
			Model:    model,
		}},
	}

	got := effectiveToolAdaptationSurfaceForTurn(cfg, ts, exec)
	if got != config.ToolSurfaceCodex {
		t.Fatalf("next-session surface = %q, want startup pin %q", got, config.ToolSurfaceCodex)
	}

	cfg.Tools.Adaptation.ApplyVisibleChanges = config.ToolVisibleChangeImmediate
	got = effectiveToolAdaptationSurfaceForTurn(cfg, ts, exec)
	if got != config.ToolSurfaceSimple {
		t.Fatalf("immediate surface = %q, want auto heuristic %q", got, config.ToolSurfaceSimple)
	}
}

func TestEffectiveToolAdaptationSurfaceAliasDuplicateUsesLastWholeOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Adaptation.VisibleToolSurface = config.ToolSurfaceSimple
	cfg.Tools.Adaptation.ProfileOverrides = []config.ToolAdaptationProfileOverride{
		{
			Provider:           "copilot",
			Model:              "gpt-5",
			VisibleToolSurface: config.ToolSurfaceSimple,
		},
		{
			Provider:           "github-copilot",
			Model:              "gpt-5",
			CacheSensitiveAPIs: config.ToolCacheSensitivityNever,
		},
	}

	ts := &turnState{
		agent: &AgentInstance{
			ToolAdaptation: picotools.ToolAdaptationDecision{
				Enabled:             true,
				PinnedToolSurface:   config.ToolSurfaceCodex,
				VisibleToolSurface:  config.ToolSurfaceCodex,
				ApplyVisibleChanges: config.ToolVisibleChangeNextSession,
			},
		},
	}
	exec := &turnExecution{
		activeCandidates: []providers.FallbackCandidate{{
			Provider: "github-copilot",
			Model:    "gpt-5",
		}},
	}

	got := effectiveToolAdaptationSurfaceForTurn(cfg, ts, exec)
	if got != config.ToolSurfaceCodex {
		t.Fatalf("effective surface = %q, want startup pin after cache-only last override", got)
	}
}

type adaptationFallbackCaptureProvider struct {
	toolNames [][]string
	models    []string
}

func (p *adaptationFallbackCaptureProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	defs []providers.ToolDefinition,
	model string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.models = append(p.models, model)
	p.toolNames = append(p.toolNames, toolDefNamesForAdaptationTest(defs))
	if len(p.models) == 1 {
		return nil, fmt.Errorf("status: 429 - rate limit exceeded")
	}
	return &providers.LLMResponse{
		Content:      "fallback answer",
		FinishReason: "stop",
	}, nil
}

func (p *adaptationFallbackCaptureProvider) GetDefaultModel() string {
	return "adaptation-primary"
}

type adaptationNamedTool string

func (t adaptationNamedTool) Name() string {
	return string(t)
}

func (t adaptationNamedTool) Description() string {
	return "adaptation test tool"
}

func (t adaptationNamedTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (t adaptationNamedTool) Execute(
	_ context.Context,
	_ map[string]any,
) *picotools.ToolResult {
	return &picotools.ToolResult{ForLLM: "ok"}
}

func TestPipelineCallLLMUsesFallbackProfileToolSurface(t *testing.T) {
	provider := &adaptationFallbackCaptureProvider{}
	al, agent, cleanup := newTurnCoordTestLoop(t, provider)
	defer cleanup()

	fallbackModel := fmt.Sprintf("fallback-codex-%d", time.Now().UnixNano())
	al.cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	al.cfg.Tools.Adaptation.VisibleToolSurface = config.ToolSurfaceSimple
	al.cfg.Tools.Adaptation.ProfileOverrides = []config.ToolAdaptationProfileOverride{{
		Provider:           "openai",
		Model:              fallbackModel,
		VisibleToolSurface: config.ToolSurfaceCodex,
	}}
	agent.ToolAdaptation = picotools.ResolveToolAdaptation(
		al.cfg.Tools.Adaptation,
		"anthropic",
		"primary-simple",
	)
	agent.Tools = picotools.NewToolRegistry()
	agent.Tools.Register(adaptationNamedTool("exec"))
	agent.Tools.Register(adaptationNamedTool("exec_command"))
	agent.Candidates = []providers.FallbackCandidate{
		{Provider: "anthropic", Model: "primary-simple"},
		{Provider: "openai", Model: fallbackModel},
	}
	al.fallback = providers.NewFallbackChain(providers.NewCooldownTracker(), nil)

	pipeline := NewPipeline(al)
	ts := newTurnState(agent, makeTestProcessOpts("adaptation-fallback"), turnEventScope{
		turnID:  "turn-adaptation-fallback",
		context: newTurnContext(nil, nil, nil),
	})
	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatalf("SetupTurn() error = %v", err)
	}
	if _, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1); err != nil {
		t.Fatalf("CallLLM() error = %v", err)
	}

	if len(provider.toolNames) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(provider.toolNames))
	}
	if got, want := fmt.Sprint(provider.toolNames[0]), fmt.Sprint([]string{"exec"}); got != want {
		t.Fatalf("primary tools = %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(provider.toolNames[1]), fmt.Sprint([]string{"exec_command"}); got != want {
		t.Fatalf("fallback tools = %s, want %s", got, want)
	}
	if exec.visibleToolSurface != config.ToolSurfaceCodex {
		t.Fatalf("successful surface = %q, want %q", exec.visibleToolSurface, config.ToolSurfaceCodex)
	}
	if got, want := fmt.Sprint(toolDefNamesForAdaptationTest(exec.providerToolDefs)),
		fmt.Sprint([]string{"exec_command"}); got != want {
		t.Fatalf("successful exec tools = %s, want %s", got, want)
	}
}

func TestApplySuccessfulFallbackCandidateUpdatesAdaptationProfile(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Provider = "openai"
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "fallback-alias",
		Provider:  "anthropic",
		Model:     "claude-sonnet",
	}}
	fallback := providers.FallbackCandidate{
		Provider:    "anthropic",
		Model:       "claude-sonnet",
		DisplayName: "fallback-alias",
		IdentityKey: "model_name:fallback-alias",
	}
	agent := &AgentInstance{
		Workspace: t.TempDir(),
		Candidates: []providers.FallbackCandidate{
			{Provider: "openai", Model: "gpt-5", DisplayName: "primary"},
			fallback,
		},
		CandidateProviders: map[string]providers.LLMProvider{
			providers.ModelKey(fallback.Provider, fallback.Model): &simpleConvProvider{},
		},
	}
	exec := &turnExecution{
		activeCandidates: []providers.FallbackCandidate{
			agent.Candidates[0],
			fallback,
		},
		activeModel:       "gpt-5",
		activeModelConfig: &config.ModelConfig{Provider: "openai", Model: "gpt-5"},
		activeProvider:    &simpleConvProvider{},
		llmModel:          "gpt-5",
		llmModelName:      "primary",
	}
	pipeline := &Pipeline{Cfg: cfg}

	pipeline.applySuccessfulFallbackCandidate(
		&turnState{agent: agent},
		exec,
		&providers.FallbackResult{
			Provider:    fallback.Provider,
			Model:       fallback.Model,
			IdentityKey: fallback.StableKey(),
		},
	)

	profile := toolAdaptationProfileForTurn(cfg, exec)
	if profile.Provider != "anthropic" || profile.Model != "claude-sonnet" {
		t.Fatalf("profile = %#v, want anthropic/claude-sonnet", profile)
	}
	if exec.llmModelName != "fallback-alias" {
		t.Fatalf("llmModelName = %q, want fallback-alias", exec.llmModelName)
	}
	if len(exec.activeCandidates) != 1 || exec.activeCandidates[0].StableKey() != fallback.StableKey() {
		t.Fatalf("activeCandidates = %#v, want selected fallback only", exec.activeCandidates)
	}
}

func TestApplySuccessfulFallbackCandidateUsesFallbackResultWhenCandidateMissing(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Provider = "openai"
	agent := &AgentInstance{
		Workspace: t.TempDir(),
		Provider:  &simpleConvProvider{},
	}
	exec := &turnExecution{
		activeCandidates: []providers.FallbackCandidate{{
			Provider:    "openai",
			Model:       "gpt-5",
			DisplayName: "primary",
		}},
		activeModel:       "gpt-5",
		activeModelConfig: &config.ModelConfig{Provider: "openai", Model: "gpt-5"},
		activeProvider:    &simpleConvProvider{},
		llmModel:          "gpt-5",
		llmModelName:      "primary",
	}
	pipeline := &Pipeline{Cfg: cfg}

	pipeline.applySuccessfulFallbackCandidate(
		&turnState{agent: agent},
		exec,
		&providers.FallbackResult{
			Provider:    "anthropic",
			Model:       "claude-sonnet",
			IdentityKey: "provider:anthropic:model:claude-sonnet",
		},
	)

	if exec.activeModel != "claude-sonnet" || exec.llmModel != "claude-sonnet" {
		t.Fatalf("active model = %q/%q, want fallback model", exec.activeModel, exec.llmModel)
	}
	if len(exec.activeCandidates) != 1 {
		t.Fatalf("len(activeCandidates) = %d, want 1", len(exec.activeCandidates))
	}
	selected := exec.activeCandidates[0]
	if selected.Provider != "anthropic" || selected.Model != "claude-sonnet" {
		t.Fatalf("selected candidate = %#v, want fallback result candidate", selected)
	}
	if exec.activeProvider == nil {
		t.Fatal("activeProvider = nil, want existing provider fallback")
	}
}

func TestApplySuccessfulFallbackCandidateIgnoresEmptyResultModel(t *testing.T) {
	exec := &turnExecution{
		activeCandidates: []providers.FallbackCandidate{{
			Provider: "openai",
			Model:    "gpt-5",
		}},
		activeModel: "gpt-5",
		llmModel:    "gpt-5",
	}
	pipeline := &Pipeline{Cfg: config.DefaultConfig()}

	pipeline.applySuccessfulFallbackCandidate(
		&turnState{agent: &AgentInstance{Workspace: t.TempDir()}},
		exec,
		&providers.FallbackResult{IdentityKey: "missing"},
	)

	if exec.activeModel != "gpt-5" || exec.llmModel != "gpt-5" {
		t.Fatalf("active model changed to %q/%q, want unchanged", exec.activeModel, exec.llmModel)
	}
}

func TestProviderForFallbackCandidatePrefersCandidateProvider(t *testing.T) {
	candidate := providers.FallbackCandidate{
		Provider: "anthropic",
		Model:    "claude-sonnet",
	}
	candidateProvider := &simpleConvProvider{}
	activeProvider := &simpleConvProvider{}
	agent := &AgentInstance{
		CandidateProviders: map[string]providers.LLMProvider{
			providers.ModelKey(candidate.Provider, candidate.Model): candidateProvider,
		},
	}

	got, err := providerForFallbackCandidate(agent, activeProvider, candidate)
	if err != nil {
		t.Fatalf("providerForFallbackCandidate() err = %v, want nil", err)
	}
	if got != candidateProvider {
		t.Fatalf("providerForFallbackCandidate() = %#v, want candidate provider", got)
	}
}

func TestProviderForFallbackCandidateFallsBackToActiveProvider(t *testing.T) {
	activeProvider := &simpleConvProvider{}

	got, err := providerForFallbackCandidate(
		&AgentInstance{},
		activeProvider,
		providers.FallbackCandidate{Provider: "anthropic", Model: "claude-sonnet"},
	)
	if err != nil {
		t.Fatalf("providerForFallbackCandidate() err = %v, want nil", err)
	}
	if got != activeProvider {
		t.Fatalf("providerForFallbackCandidate() = %#v, want active provider", got)
	}
}

func TestProviderForFallbackCandidateRequiresProvider(t *testing.T) {
	_, err := providerForFallbackCandidate(
		nil,
		nil,
		providers.FallbackCandidate{Provider: "anthropic", Model: "claude-sonnet"},
	)
	if err == nil {
		t.Fatal("providerForFallbackCandidate() err = nil, want error")
	}
}

func TestToolAdaptationProfilePrefersRouterCandidate(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Provider = config.AccountRouterProvider
	exec := &turnExecution{
		activeCandidates: []providers.FallbackCandidate{{
			Provider: "github-copilot",
			Model:    "gpt-5.4",
		}},
		activeModelConfig: &config.ModelConfig{
			Provider: config.AccountRouterProvider,
			Model:    "gpt-5.4",
		},
		llmModel: "gpt-5.4",
	}

	profile := toolAdaptationProfileForTurn(cfg, exec)
	if profile.Provider != "github-copilot" || profile.Model != "gpt-5.4" {
		t.Fatalf("profile = %#v, want concrete router candidate", profile)
	}
}

func toolDefsForAdaptationTest(names ...string) []providers.ToolDefinition {
	defs := make([]providers.ToolDefinition, 0, len(names))
	for _, name := range names {
		defs = append(defs, providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name:       name,
				Parameters: map[string]any{"type": "object"},
			},
		})
	}
	return defs
}

func toolDefNamesForAdaptationTest(defs []providers.ToolDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Function.Name)
	}
	return names
}
