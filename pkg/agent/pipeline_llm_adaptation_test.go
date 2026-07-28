package agent

import (
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
