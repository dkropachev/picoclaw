package tools

import (
	"path/filepath"
	"testing"
	"time"

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

func TestResolveToolAdaptationAppliesProfileOverride(t *testing.T) {
	cfg := config.DefaultToolAdaptationConfig()
	cfg.VisibleToolSurface = config.ToolSurfaceCodex
	cfg.CacheSensitiveAPIs = config.ToolCacheSensitivityAlways
	cfg.ProfileOverrides = []config.ToolAdaptationProfileOverride{{
		Provider:           "copilot",
		Model:              " GPT-5.4 ",
		VisibleToolSurface: config.ToolSurfaceSimple,
		CacheSensitiveAPIs: config.ToolCacheSensitivityNever,
	}}

	decision := ResolveToolAdaptation(cfg, "github-copilot", "gpt-5.4")
	if decision.VisibleToolSurface != config.ToolSurfaceSimple {
		t.Fatalf(
			"VisibleToolSurface = %q, want profile override %q",
			decision.VisibleToolSurface,
			config.ToolSurfaceSimple,
		)
	}
	if decision.CacheSensitive {
		t.Fatal("CacheSensitive = true, want profile override false")
	}

	unmatched := ResolveToolAdaptation(cfg, "openai", "gpt-5.4")
	if unmatched.VisibleToolSurface != config.ToolSurfaceCodex || !unmatched.CacheSensitive {
		t.Fatalf("unmatched decision = %#v, want global policy", unmatched)
	}
}

func TestResolveToolAdaptationProfileOverrideInheritsEmptyFields(t *testing.T) {
	cfg := config.DefaultToolAdaptationConfig()
	cfg.VisibleToolSurface = config.ToolSurfaceSimple
	cfg.CacheSensitiveAPIs = config.ToolCacheSensitivityNever
	cfg.ProfileOverrides = []config.ToolAdaptationProfileOverride{{
		Provider: "openai",
		Model:    "gpt-5",
	}}

	decision := ResolveToolAdaptation(cfg, "openai", "gpt-5")
	if decision.VisibleToolSurface != config.ToolSurfaceSimple || decision.CacheSensitive {
		t.Fatalf("decision = %#v, want inherited simple/cache-insensitive policy", decision)
	}
}

func TestResolveToolAdaptationCanonicalAliasDuplicateUsesLastOverride(t *testing.T) {
	cfg := config.DefaultToolAdaptationConfig()
	cfg.VisibleToolSurface = config.ToolSurfaceSimple
	cfg.CacheSensitiveAPIs = config.ToolCacheSensitivityAlways
	cfg.ProfileOverrides = []config.ToolAdaptationProfileOverride{
		{
			Provider:           "copilot",
			Model:              "gpt-5",
			VisibleToolSurface: config.ToolSurfaceCodex,
		},
		{
			Provider:           "github-copilot",
			Model:              "gpt-5",
			CacheSensitiveAPIs: config.ToolCacheSensitivityNever,
		},
	}

	decision := ResolveToolAdaptation(cfg, "github-copilot", "gpt-5")
	if decision.VisibleToolSurface != config.ToolSurfaceSimple || decision.CacheSensitive {
		t.Fatalf("decision = %#v, want last alias override with inherited simple surface", decision)
	}
}

func TestNormalizeToolAdaptationProfileCanonicalizesProviderAliases(t *testing.T) {
	got := normalizeToolAdaptationProfile(ToolAdaptationProfile{
		Provider: " CoPiLoT ",
		Model:    " GPT-5 ",
	})
	want := (ToolAdaptationProfile{
		Provider: "github-copilot",
		Model:    "gpt-5",
	})
	if got != want {
		t.Fatalf("normalizeToolAdaptationProfile() = %#v, want %#v", got, want)
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

func TestToolAdaptationStateMergesPersistedProviderAliases(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	resetToolAdaptationStateForTest(t, statePath)

	older := time.Date(2026, time.July, 27, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	persisted := &toolAdaptationStateStore{
		observations: map[string]ToolAdaptationObservation{
			"copilot/gpt-5.4": {
				Profile:            ToolAdaptationProfile{Provider: "copilot", Model: "gpt-5.4"},
				VisibleToolSurface: config.ToolSurfaceSimple,
				PromptTokens:       800,
				CachedTokens:       100,
				ObservedAt:         older,
			},
			"github-copilot/gpt-5.4": {
				Profile:            ToolAdaptationProfile{Provider: "github-copilot", Model: "gpt-5.4"},
				VisibleToolSurface: config.ToolSurfaceCodex,
				PromptTokens:       1200,
				CachedTokens:       600,
				ObservedAt:         newer,
			},
		},
		outcomes: map[string]ToolAdaptationToolOutcome{
			"copilot/gpt-5.4/codex/exec_command": {
				Profile:            ToolAdaptationProfile{Provider: "copilot", Model: "gpt-5.4"},
				VisibleToolSurface: config.ToolSurfaceCodex,
				ToolName:           "exec_command",
				Successes:          2,
				Failures:           1,
				LastError:          "old error",
				LastDurationMS:     100,
				UpdatedAt:          older,
			},
			"github-copilot/gpt-5.4/codex/exec_command": {
				Profile:            ToolAdaptationProfile{Provider: "github-copilot", Model: "gpt-5.4"},
				VisibleToolSurface: config.ToolSurfaceCodex,
				ToolName:           "exec_command",
				Successes:          3,
				Failures:           4,
				LastError:          "new error",
				LastDurationMS:     250,
				UpdatedAt:          newer,
			},
		},
		pathOverride: statePath,
	}
	if err := persisted.saveLocked(); err != nil {
		t.Fatalf("saveLocked() error = %v", err)
	}

	observation, ok := LatestToolAdaptationObservation(
		ToolAdaptationProfile{Provider: "github-copilot", Model: "gpt-5.4"},
	)
	if !ok {
		t.Fatal("LatestToolAdaptationObservation() ok = false, want true")
	}
	if observation.Profile.Provider != "github-copilot" ||
		observation.VisibleToolSurface != config.ToolSurfaceCodex ||
		observation.CachedTokens != 600 ||
		!observation.ObservedAt.Equal(newer) {
		t.Fatalf("loaded observation = %#v, want newest canonical observation", observation)
	}

	outcomes := LatestToolAdaptationToolOutcomes(
		ToolAdaptationProfile{Provider: "copilot", Model: "gpt-5.4"},
	)
	if len(outcomes) != 1 {
		t.Fatalf("len(loaded outcomes) = %d, want 1 merged outcome", len(outcomes))
	}
	outcome := outcomes[0]
	if outcome.Profile.Provider != "github-copilot" ||
		outcome.Successes != 5 ||
		outcome.Failures != 5 ||
		outcome.LastError != "new error" ||
		outcome.LastDurationMS != 250 ||
		!outcome.UpdatedAt.Equal(newer) {
		t.Fatalf("loaded outcome = %#v, want merged counters and newest metadata", outcome)
	}
}

func TestToolAdaptationStateHelpers(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	if got := toolAdaptationStatePath(); got != filepath.Join(home, "tool-adaptation.db") {
		t.Fatalf("toolAdaptationStatePath() = %q, want path under configured home", got)
	}
	if got := ToolSchemaHash(nil); got != "" {
		t.Fatalf("ToolSchemaHash(nil) = %q, want empty hash", got)
	}

	leftFirst := []providers.ToolDefinition{
		{Function: providers.ToolFunctionDefinition{Name: "write_file", Parameters: map[string]any{"type": "object"}}},
		{Function: providers.ToolFunctionDefinition{Name: "read_file", Parameters: map[string]any{"type": "object"}}},
	}
	rightFirst := []providers.ToolDefinition{
		{Function: providers.ToolFunctionDefinition{Name: "read_file", Parameters: map[string]any{"type": "object"}}},
		{Function: providers.ToolFunctionDefinition{Name: "write_file", Parameters: map[string]any{"type": "object"}}},
	}
	if leftHash, rightHash := ToolSchemaHash(leftFirst), ToolSchemaHash(rightFirst); leftHash == "" ||
		leftHash != rightHash {
		t.Fatalf("ToolSchemaHash order stability = %q vs %q, want same non-empty hash", leftHash, rightHash)
	}
}

func TestToolAdaptationStateRejectsInvalidInputs(t *testing.T) {
	resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))

	if _, ok := ObserveToolAdaptationCache(
		ToolAdaptationProfile{},
		config.ToolSurfacePicoClaw,
		nil,
		&providers.UsageInfo{PromptTokens: 1000},
	); ok {
		t.Fatal("ObserveToolAdaptationCache(empty profile) ok = true, want false")
	}
	profile := ToolAdaptationProfile{Provider: "reject-provider", Model: "reject-model"}
	if _, ok := ObserveToolAdaptationCache(profile, config.ToolSurfacePicoClaw, nil, nil); ok {
		t.Fatal("ObserveToolAdaptationCache(nil usage) ok = true, want false")
	}
	if _, ok := ObserveToolAdaptationCache(
		profile,
		config.ToolSurfacePicoClaw,
		nil,
		&providers.UsageInfo{PromptTokens: 10},
	); ok {
		t.Fatal("ObserveToolAdaptationCache(low prompt tokens) ok = true, want false")
	}
	if _, ok := LatestToolAdaptationObservation(ToolAdaptationProfile{}); ok {
		t.Fatal("LatestToolAdaptationObservation(empty profile) ok = true, want false")
	}
	if _, ok := ObserveToolAdaptationToolOutcome(profile, config.ToolSurfaceCodex, " ", true, "", 0); ok {
		t.Fatal("ObserveToolAdaptationToolOutcome(empty tool) ok = true, want false")
	}
	if outcomes := LatestToolAdaptationToolOutcomes(ToolAdaptationProfile{}); outcomes != nil {
		t.Fatalf("LatestToolAdaptationToolOutcomes(empty profile) = %#v, want nil", outcomes)
	}
}

func TestToolAdaptationToolOutcomesSortBySurfaceThenTool(t *testing.T) {
	resetToolAdaptationStateForTest(t, filepath.Join(t.TempDir(), "state.json"))
	profile := ToolAdaptationProfile{Provider: "sort-provider", Model: "sort-model"}

	for _, item := range []struct {
		surface string
		tool    string
	}{
		{surface: config.ToolSurfaceSimple, tool: "write_file"},
		{surface: config.ToolSurfaceCodex, tool: "exec_command"},
		{surface: config.ToolSurfaceCodex, tool: "apply_patch"},
	} {
		if _, ok := ObserveToolAdaptationToolOutcome(profile, item.surface, item.tool, true, "", 0); !ok {
			t.Fatalf("ObserveToolAdaptationToolOutcome(%s/%s) ok = false, want true", item.surface, item.tool)
		}
	}

	outcomes := LatestToolAdaptationToolOutcomes(profile)
	got := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		got = append(got, outcome.VisibleToolSurface+"/"+outcome.ToolName)
	}
	want := []string{
		config.ToolSurfaceCodex + "/apply_patch",
		config.ToolSurfaceCodex + "/exec_command",
		config.ToolSurfaceSimple + "/write_file",
	}
	if len(got) != len(want) {
		t.Fatalf("sorted outcomes = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted outcomes = %#v, want %#v", got, want)
		}
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
