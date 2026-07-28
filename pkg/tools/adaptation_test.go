package tools

import (
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestResolveToolAdaptationAutoCodexCacheSensitive(t *testing.T) {
	decision := ResolveToolAdaptation(config.DefaultToolAdaptationConfig(), "openai", "gpt-5")

	if decision.VisibleToolSurface != config.ToolSurfaceCodex {
		t.Fatalf("VisibleToolSurface = %q, want %q", decision.VisibleToolSurface, config.ToolSurfaceCodex)
	}
	if !decision.CacheSensitive {
		t.Fatal("CacheSensitive = false, want true")
	}
	if decision.RuntimeDowngrade {
		t.Fatal("RuntimeDowngrade = true, want false for cache-sensitive auto")
	}
	if decision.RuntimePromotion {
		t.Fatal("RuntimePromotion = true, want false for cache-sensitive auto")
	}
}

func TestResolveToolAdaptationAllowsRuntimeWhenCacheInsensitive(t *testing.T) {
	cfg := config.DefaultToolAdaptationConfig()
	cfg.CacheSensitiveAPIs = config.ToolCacheSensitivityNever

	decision := ResolveToolAdaptation(cfg, "ollama", "qwen3-coder")

	if decision.VisibleToolSurface != config.ToolSurfacePicoClaw {
		t.Fatalf("VisibleToolSurface = %q, want %q", decision.VisibleToolSurface, config.ToolSurfacePicoClaw)
	}
	if decision.CacheSensitive {
		t.Fatal("CacheSensitive = true, want false")
	}
	if !decision.RuntimeDowngrade {
		t.Fatal("RuntimeDowngrade = false, want true for cache-insensitive auto")
	}
	if !decision.RuntimePromotion {
		t.Fatal("RuntimePromotion = false, want true for cache-insensitive auto")
	}
}

func TestResolveToolAdaptationPinnedSurface(t *testing.T) {
	cfg := config.DefaultToolAdaptationConfig()
	cfg.VisibleToolSurface = config.ToolSurfaceSimple

	decision := ResolveToolAdaptation(cfg, "openai", "gpt-5")

	if decision.VisibleToolSurface != config.ToolSurfaceSimple {
		t.Fatalf("VisibleToolSurface = %q, want %q", decision.VisibleToolSurface, config.ToolSurfaceSimple)
	}
	if decision.PinnedToolSurface != config.ToolSurfaceSimple {
		t.Fatalf("PinnedToolSurface = %q, want %q", decision.PinnedToolSurface, config.ToolSurfaceSimple)
	}
}

func TestResolveToolAdaptationUsesPositiveSniffedCacheEvidence(t *testing.T) {
	resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))
	profile := ToolAdaptationProfile{Provider: "local-test", Model: "cache-sensitive"}
	_, ok := ObserveToolAdaptationCache(
		profile,
		config.ToolSurfacePicoClaw,
		[]providers.ToolDefinition{{
			Function: providers.ToolFunctionDefinition{
				Name:       "read_file",
				Parameters: map[string]any{"type": "object"},
			},
		}},
		&providers.UsageInfo{PromptTokens: 1000, CachedTokens: 400},
	)
	if !ok {
		t.Fatal("ObserveToolAdaptationCache() ok = false, want true")
	}

	decision := ResolveToolAdaptation(config.DefaultToolAdaptationConfig(), profile.Provider, profile.Model)
	if !decision.CacheSensitive {
		t.Fatal("CacheSensitive = false, want true from sniffed cached tokens")
	}
	if decision.CacheEvidence != "sniffed" {
		t.Fatalf("CacheEvidence = %q, want sniffed", decision.CacheEvidence)
	}
	if decision.RuntimeDowngrade || decision.RuntimePromotion {
		t.Fatalf(
			"runtime auto = downgrade:%v promotion:%v, want both false",
			decision.RuntimeDowngrade,
			decision.RuntimePromotion,
		)
	}
}

func TestResolveToolAdaptationDoesNotTreatCacheMissAsSafeForHeuristicSensitiveAPI(t *testing.T) {
	resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))
	profile := ToolAdaptationProfile{Provider: "openai", Model: "gpt-5-cache-miss"}
	_, ok := ObserveToolAdaptationCache(
		profile,
		config.ToolSurfaceCodex,
		[]providers.ToolDefinition{{
			Function: providers.ToolFunctionDefinition{
				Name:       "exec_command",
				Parameters: map[string]any{"type": "object"},
			},
		}},
		&providers.UsageInfo{PromptTokens: 1000, CachedTokens: 0},
	)
	if !ok {
		t.Fatal("ObserveToolAdaptationCache() ok = false, want true")
	}

	decision := ResolveToolAdaptation(config.DefaultToolAdaptationConfig(), profile.Provider, profile.Model)
	if !decision.CacheSensitive {
		t.Fatal("CacheSensitive = false, want heuristic-sensitive after cache miss")
	}
	if decision.CacheEvidence != "heuristic" {
		t.Fatalf("CacheEvidence = %q, want heuristic", decision.CacheEvidence)
	}
}

func TestToolAdaptationObservationPersistsAcrossStoreReload(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	resetToolAdaptationStateForTest(t, statePath)
	profile := ToolAdaptationProfile{Provider: "persist-provider", Model: "persist-model"}
	written, ok := ObserveToolAdaptationCache(
		profile,
		config.ToolSurfacePicoClaw,
		[]providers.ToolDefinition{{
			Function: providers.ToolFunctionDefinition{
				Name:       "read_file",
				Parameters: map[string]any{"type": "object"},
			},
		}},
		&providers.UsageInfo{PromptTokens: 1200, CachedTokens: 300},
	)
	if !ok {
		t.Fatal("ObserveToolAdaptationCache() ok = false, want true")
	}

	globalToolAdaptationState = &toolAdaptationStateStore{
		observations: map[string]ToolAdaptationObservation{},
		pathOverride: statePath,
	}
	loaded, ok := LatestToolAdaptationObservation(profile)
	if !ok {
		t.Fatal("LatestToolAdaptationObservation() ok = false after reload, want true")
	}
	if loaded.CachedTokens != written.CachedTokens || loaded.ToolSchemaHash != written.ToolSchemaHash {
		t.Fatalf("loaded observation = %#v, want persisted %#v", loaded, written)
	}
}

func TestToolAdaptationToolOutcomesAccumulateAndPersist(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	resetToolAdaptationStateForTest(t, statePath)
	profile := ToolAdaptationProfile{Provider: "outcome-provider", Model: "outcome-model"}

	first, ok := ObserveToolAdaptationToolOutcome(
		profile,
		config.ToolSurfaceCodex,
		"exec_command",
		true,
		"",
		10,
	)
	if !ok {
		t.Fatal("ObserveToolAdaptationToolOutcome(success) ok = false, want true")
	}
	second, ok := ObserveToolAdaptationToolOutcome(
		profile,
		config.ToolSurfaceCodex,
		"exec_command",
		false,
		"bad args",
		20,
	)
	if !ok {
		t.Fatal("ObserveToolAdaptationToolOutcome(failure) ok = false, want true")
	}
	if first.Successes != 1 || second.Successes != 1 || second.Failures != 1 {
		t.Fatalf("outcomes = first:%#v second:%#v, want accumulated success/failure", first, second)
	}

	globalToolAdaptationState = &toolAdaptationStateStore{
		observations: map[string]ToolAdaptationObservation{},
		outcomes:     map[string]ToolAdaptationToolOutcome{},
		pathOverride: statePath,
	}
	loaded := LatestToolAdaptationToolOutcomes(profile)
	if len(loaded) != 1 {
		t.Fatalf("len(loaded outcomes) = %d, want 1", len(loaded))
	}
	if loaded[0].Successes != 1 || loaded[0].Failures != 1 || loaded[0].LastError != "bad args" {
		t.Fatalf("loaded outcome = %#v, want persisted counts and last error", loaded[0])
	}
}

func TestResolveToolAdaptationAutoUsesLearnedSuccessfulSurface(t *testing.T) {
	resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))
	profile := ToolAdaptationProfile{Provider: "unknown-provider", Model: "unknown-model"}
	_, ok := ObserveToolAdaptationToolOutcome(
		profile,
		config.ToolSurfaceSimple,
		"read_file",
		true,
		"",
		10,
	)
	if !ok {
		t.Fatal("ObserveToolAdaptationToolOutcome(simple) ok = false, want true")
	}
	_, ok = ObserveToolAdaptationToolOutcome(
		profile,
		config.ToolSurfacePicoClaw,
		"read_file",
		false,
		"bad args",
		10,
	)
	if !ok {
		t.Fatal("ObserveToolAdaptationToolOutcome(picoclaw) ok = false, want true")
	}

	decision := ResolveToolAdaptation(config.DefaultToolAdaptationConfig(), profile.Provider, profile.Model)
	if decision.VisibleToolSurface != config.ToolSurfaceSimple {
		t.Fatalf("VisibleToolSurface = %q, want learned simple", decision.VisibleToolSurface)
	}
	if decision.SurfaceEvidence != "learned" {
		t.Fatalf("SurfaceEvidence = %q, want learned", decision.SurfaceEvidence)
	}
}

func TestResolveToolAdaptationAutoIgnoresLearnedSurfacesWithoutSuccess(t *testing.T) {
	resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))
	profile := ToolAdaptationProfile{Provider: "unknown-provider", Model: "failure-only-model"}
	_, ok := ObserveToolAdaptationToolOutcome(
		profile,
		config.ToolSurfaceSimple,
		"read_file",
		false,
		"bad args",
		10,
	)
	if !ok {
		t.Fatal("ObserveToolAdaptationToolOutcome() ok = false, want true")
	}

	decision := ResolveToolAdaptation(config.DefaultToolAdaptationConfig(), profile.Provider, profile.Model)
	if decision.VisibleToolSurface != config.ToolSurfacePicoClaw {
		t.Fatalf("VisibleToolSurface = %q, want heuristic picoclaw", decision.VisibleToolSurface)
	}
	if decision.SurfaceEvidence != "heuristic" {
		t.Fatalf("SurfaceEvidence = %q, want heuristic", decision.SurfaceEvidence)
	}
}

func resetToolAdaptationStateForTest(t *testing.T, path string) {
	t.Helper()
	previous := globalToolAdaptationState
	globalToolAdaptationState = &toolAdaptationStateStore{
		observations: map[string]ToolAdaptationObservation{},
		outcomes:     map[string]ToolAdaptationToolOutcome{},
		pathOverride: path,
	}
	t.Cleanup(func() {
		globalToolAdaptationState = previous
	})
}
