package tools

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

type ToolAdaptationDecision struct {
	Enabled             bool
	VisibleToolSurface  string
	PinnedToolSurface   string
	SurfaceEvidence     string
	LearnFromToolCalls  bool
	RuntimeDowngrade    bool
	RuntimePromotion    bool
	ApplyVisibleChanges string
	CacheSensitive      bool
	CacheSensitiveAPIs  string
	CacheEvidence       string
	CacheObservation    *ToolAdaptationObservation
}

func (d ToolAdaptationDecision) UsesCodexCompatibleTools() bool {
	return d.Enabled && d.VisibleToolSurface == config.ToolSurfaceCodex
}

func (d ToolAdaptationDecision) MayUseCodexCompatibleTools() bool {
	if d.UsesCodexCompatibleTools() {
		return true
	}
	if !d.Enabled || !d.RuntimePromotion {
		return false
	}
	switch d.ApplyVisibleChanges {
	case config.ToolVisibleChangeImmediate, config.ToolVisibleChangeContextBoundary:
	default:
		return false
	}
	return d.SurfaceEvidence != "config"
}

func ResolveToolAdaptation(
	cfg config.ToolAdaptationConfig,
	providerName string,
	modelName string,
) ToolAdaptationDecision {
	normalized := cfg.Normalized()
	if !normalized.Enabled {
		return ToolAdaptationDecision{
			Enabled:             false,
			VisibleToolSurface:  config.ToolSurfacePicoClaw,
			PinnedToolSurface:   config.ToolSurfacePicoClaw,
			SurfaceEvidence:     "disabled",
			LearnFromToolCalls:  false,
			ApplyVisibleChanges: config.ToolVisibleChangeNever,
			CacheSensitiveAPIs:  normalized.CacheSensitiveAPIs,
			CacheEvidence:       "disabled",
		}
	}

	profile := ToolAdaptationProfile{Provider: providerName, Model: modelName}
	observation, hasObservation := LatestToolAdaptationObservation(profile)
	cacheSensitive, cacheEvidence := resolveCacheSensitivity(
		normalized.CacheSensitiveAPIs,
		providerName,
		modelName,
		observation,
		hasObservation,
	)
	outcomes := LatestToolAdaptationToolOutcomes(profile)
	visibleSurface, surfaceEvidence := resolveAutoToolSurface(
		normalized.VisibleToolSurface,
		providerName,
		modelName,
		outcomes,
	)
	decision := ToolAdaptationDecision{
		Enabled:             true,
		VisibleToolSurface:  visibleSurface,
		PinnedToolSurface:   visibleSurface,
		SurfaceEvidence:     surfaceEvidence,
		LearnFromToolCalls:  normalized.LearnFromToolCalls,
		RuntimeDowngrade:    resolveRuntimeAdaptation(normalized.AllowRuntimeDowngrade, cacheSensitive),
		RuntimePromotion:    resolveRuntimeAdaptation(normalized.AllowRuntimePromotion, cacheSensitive),
		ApplyVisibleChanges: normalized.ApplyVisibleChanges,
		CacheSensitive:      cacheSensitive,
		CacheSensitiveAPIs:  normalized.CacheSensitiveAPIs,
		CacheEvidence:       cacheEvidence,
	}
	if hasObservation {
		decision.CacheObservation = &observation
	}
	if normalized.CacheBreakingDowngrade {
		decision.RuntimeDowngrade = true
	}
	return decision
}

func resolveAutoToolSurface(
	surface string,
	providerName string,
	modelName string,
	outcomes []ToolAdaptationToolOutcome,
) (string, string) {
	surface = config.NormalizeToolSurface(surface)
	if surface != config.ToolSurfaceAuto {
		return surface, "config"
	}

	if learnedSurface, ok := learnedToolSurface(outcomes); ok {
		return learnedSurface, "learned"
	}

	provider := strings.ToLower(strings.TrimSpace(providerName))
	model := strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case strings.Contains(provider, "openai"), strings.Contains(provider, "codex"),
		strings.Contains(model, "gpt-"), strings.Contains(model, "codex"):
		return config.ToolSurfaceCodex, "heuristic"
	case strings.Contains(provider, "anthropic"), strings.Contains(model, "claude"):
		return config.ToolSurfaceSimple, "heuristic"
	default:
		return config.ToolSurfacePicoClaw, "heuristic"
	}
}

func learnedToolSurface(outcomes []ToolAdaptationToolOutcome) (string, bool) {
	type score struct {
		successes int
		failures  int
	}
	bySurface := map[string]score{}
	for _, outcome := range outcomes {
		surface := config.NormalizeToolSurface(outcome.VisibleToolSurface)
		if surface == config.ToolSurfaceAuto {
			continue
		}
		current := bySurface[surface]
		current.successes += outcome.Successes
		current.failures += outcome.Failures
		bySurface[surface] = current
	}

	bestSurface := ""
	bestScore := -1.0
	bestTotal := -1
	for surface, score := range bySurface {
		total := score.successes + score.failures
		if score.successes <= 0 || total <= 0 {
			continue
		}
		rate := float64(score.successes) / float64(total)
		if rate > bestScore || (rate == bestScore && total > bestTotal) {
			bestSurface = surface
			bestScore = rate
			bestTotal = total
		}
	}
	if bestSurface == "" {
		return "", false
	}
	return bestSurface, true
}

func resolveRuntimeAdaptation(value string, cacheSensitive bool) bool {
	switch config.NormalizeToolRuntimeAdaptation(value) {
	case config.ToolRuntimeAdaptationAllow:
		return true
	case config.ToolRuntimeAdaptationNever:
		return false
	default:
		return !cacheSensitive
	}
}

func resolveCacheSensitivity(
	value string,
	providerName string,
	modelName string,
	observation ToolAdaptationObservation,
	hasObservation bool,
) (bool, string) {
	switch config.NormalizeToolCacheSensitivity(value) {
	case config.ToolCacheSensitivityAlways:
		return true, "config"
	case config.ToolCacheSensitivityNever:
		return false, "config"
	}

	if hasObservation && observation.Sniffed && observation.CacheSensitive {
		return true, "sniffed"
	}

	provider := strings.ToLower(strings.TrimSpace(providerName))
	model := strings.ToLower(strings.TrimSpace(modelName))
	return strings.Contains(provider, "anthropic") ||
		strings.Contains(provider, "gemini") ||
		strings.Contains(provider, "openai") ||
		strings.Contains(model, "claude") ||
		strings.Contains(model, "gemini") ||
		strings.Contains(model, "gpt-"), "heuristic"
}
