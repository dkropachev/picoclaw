package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	minCacheSniffPromptTokens   = 256
	toolAdaptationStateFilename = "tool_adaptation_state.json"
)

// ToolAdaptationProfile identifies the model/API pair whose tool behavior is
// being learned. Provider/model aliases are normalized so UI and runtime calls
// land on the same profile where possible.
type ToolAdaptationProfile struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ToolAdaptationObservation records the latest cache evidence for a pinned
// visible tool surface and tool schema fingerprint.
type ToolAdaptationObservation struct {
	Profile            ToolAdaptationProfile `json:"profile"`
	VisibleToolSurface string                `json:"visible_tool_surface"`
	ToolSchemaHash     string                `json:"tool_schema_hash"`
	PromptTokens       int                   `json:"prompt_tokens"`
	CachedTokens       int                   `json:"cached_tokens"`
	CacheHitRatio      float64               `json:"cache_hit_ratio"`
	CacheSensitive     bool                  `json:"cache_sensitive"`
	Sniffed            bool                  `json:"sniffed"`
	ObservedAt         time.Time             `json:"observed_at"`
}

type ToolAdaptationToolOutcome struct {
	Profile            ToolAdaptationProfile `json:"profile"`
	VisibleToolSurface string                `json:"visible_tool_surface"`
	ToolName           string                `json:"tool_name"`
	Successes          int                   `json:"successes"`
	Failures           int                   `json:"failures"`
	LastError          string                `json:"last_error,omitempty"`
	LastDurationMS     int64                 `json:"last_duration_ms"`
	UpdatedAt          time.Time             `json:"updated_at"`
}

type toolAdaptationStateFile struct {
	Version      int                                  `json:"version"`
	Observations map[string]ToolAdaptationObservation `json:"observations"`
	Outcomes     map[string]ToolAdaptationToolOutcome `json:"outcomes"`
}

type toolAdaptationStateStore struct {
	mu           sync.RWMutex
	observations map[string]ToolAdaptationObservation
	outcomes     map[string]ToolAdaptationToolOutcome
	loaded       bool
	pathOverride string
}

var globalToolAdaptationState = &toolAdaptationStateStore{
	observations: map[string]ToolAdaptationObservation{},
	outcomes:     map[string]ToolAdaptationToolOutcome{},
}

func ObserveToolAdaptationCache(
	profile ToolAdaptationProfile,
	visibleToolSurface string,
	toolDefs []providers.ToolDefinition,
	usage *providers.UsageInfo,
) (ToolAdaptationObservation, bool) {
	return globalToolAdaptationState.observe(profile, visibleToolSurface, toolDefs, usage)
}

func LatestToolAdaptationObservation(profile ToolAdaptationProfile) (ToolAdaptationObservation, bool) {
	return globalToolAdaptationState.latest(profile)
}

func ObserveToolAdaptationToolOutcome(
	profile ToolAdaptationProfile,
	visibleToolSurface string,
	toolName string,
	success bool,
	errorSummary string,
	duration time.Duration,
) (ToolAdaptationToolOutcome, bool) {
	return globalToolAdaptationState.observeToolOutcome(
		profile,
		visibleToolSurface,
		toolName,
		success,
		errorSummary,
		duration,
	)
}

func LatestToolAdaptationToolOutcomes(profile ToolAdaptationProfile) []ToolAdaptationToolOutcome {
	return globalToolAdaptationState.latestToolOutcomes(profile)
}

func ToolAdaptationStatePath() string {
	return filepath.Join(config.GetHome(), toolAdaptationStateFilename)
}

func ToolSchemaHash(toolDefs []providers.ToolDefinition) string {
	if len(toolDefs) == 0 {
		return ""
	}

	items := make([]map[string]any, 0, len(toolDefs))
	for _, def := range toolDefs {
		items = append(items, map[string]any{
			"name":       def.Function.Name,
			"parameters": def.Function.Parameters,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		left, _ := items[i]["name"].(string)
		right, _ := items[j]["name"].(string)
		return left < right
	})

	payload, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (s *toolAdaptationStateStore) observe(
	profile ToolAdaptationProfile,
	visibleToolSurface string,
	toolDefs []providers.ToolDefinition,
	usage *providers.UsageInfo,
) (ToolAdaptationObservation, bool) {
	profile = normalizeToolAdaptationProfile(profile)
	if profile.Provider == "" && profile.Model == "" {
		return ToolAdaptationObservation{}, false
	}
	if usage == nil || usage.PromptTokens < minCacheSniffPromptTokens {
		return ToolAdaptationObservation{}, false
	}

	ratio := 0.0
	if usage.PromptTokens > 0 {
		ratio = float64(usage.CachedTokens) / float64(usage.PromptTokens)
	}
	observation := ToolAdaptationObservation{
		Profile:            profile,
		VisibleToolSurface: strings.TrimSpace(visibleToolSurface),
		ToolSchemaHash:     ToolSchemaHash(toolDefs),
		PromptTokens:       usage.PromptTokens,
		CachedTokens:       usage.CachedTokens,
		CacheHitRatio:      ratio,
		CacheSensitive:     usage.CachedTokens > 0,
		Sniffed:            true,
		ObservedAt:         time.Now().UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	s.observations[profile.key()] = observation
	if err := s.saveLocked(); err != nil {
		logger.WarnCF("tools", "Failed to persist tool adaptation state", map[string]any{
			"path":  s.statePathLocked(),
			"error": err.Error(),
		})
	}
	return observation, true
}

func (s *toolAdaptationStateStore) latest(profile ToolAdaptationProfile) (ToolAdaptationObservation, bool) {
	profile = normalizeToolAdaptationProfile(profile)
	if profile.Provider == "" && profile.Model == "" {
		return ToolAdaptationObservation{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	observation, ok := s.observations[profile.key()]
	return observation, ok
}

func (s *toolAdaptationStateStore) observeToolOutcome(
	profile ToolAdaptationProfile,
	visibleToolSurface string,
	toolName string,
	success bool,
	errorSummary string,
	duration time.Duration,
) (ToolAdaptationToolOutcome, bool) {
	profile = normalizeToolAdaptationProfile(profile)
	toolName = strings.TrimSpace(toolName)
	if (profile.Provider == "" && profile.Model == "") || toolName == "" {
		return ToolAdaptationToolOutcome{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()

	key := toolOutcomeKey(profile, visibleToolSurface, toolName)
	outcome := s.outcomes[key]
	outcome.Profile = profile
	outcome.VisibleToolSurface = strings.TrimSpace(visibleToolSurface)
	outcome.ToolName = toolName
	outcome.LastDurationMS = duration.Milliseconds()
	outcome.UpdatedAt = time.Now().UTC()
	if success {
		outcome.Successes++
		outcome.LastError = ""
	} else {
		outcome.Failures++
		outcome.LastError = strings.TrimSpace(errorSummary)
	}
	s.outcomes[key] = outcome

	if err := s.saveLocked(); err != nil {
		logger.WarnCF("tools", "Failed to persist tool adaptation state", map[string]any{
			"path":  s.statePathLocked(),
			"error": err.Error(),
		})
	}
	return outcome, true
}

func (s *toolAdaptationStateStore) latestToolOutcomes(profile ToolAdaptationProfile) []ToolAdaptationToolOutcome {
	profile = normalizeToolAdaptationProfile(profile)
	if profile.Provider == "" && profile.Model == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()

	outcomes := make([]ToolAdaptationToolOutcome, 0)
	for _, outcome := range s.outcomes {
		if outcome.Profile.key() == profile.key() {
			outcomes = append(outcomes, outcome)
		}
	}
	sort.Slice(outcomes, func(i, j int) bool {
		if outcomes[i].VisibleToolSurface != outcomes[j].VisibleToolSurface {
			return outcomes[i].VisibleToolSurface < outcomes[j].VisibleToolSurface
		}
		return outcomes[i].ToolName < outcomes[j].ToolName
	})
	return outcomes
}

func (s *toolAdaptationStateStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true

	path := s.statePathLocked()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.WarnCF("tools", "Failed to read tool adaptation state", map[string]any{
				"path":  path,
				"error": err.Error(),
			})
		}
		return
	}

	var state toolAdaptationStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		logger.WarnCF("tools", "Failed to decode tool adaptation state", map[string]any{
			"path":  path,
			"error": err.Error(),
		})
		return
	}
	if state.Observations == nil {
		state.Observations = map[string]ToolAdaptationObservation{}
	}
	if state.Outcomes == nil {
		state.Outcomes = map[string]ToolAdaptationToolOutcome{}
	}
	observationKeys := make([]string, 0, len(state.Observations))
	for key := range state.Observations {
		observationKeys = append(observationKeys, key)
	}
	sort.Strings(observationKeys)
	for _, sourceKey := range observationKeys {
		observation := state.Observations[sourceKey]
		observation.Profile = normalizeToolAdaptationProfile(observation.Profile)
		if observation.Profile.Provider != "" || observation.Profile.Model != "" {
			key := observation.Profile.key()
			current, exists := s.observations[key]
			// Persisted aliases can collapse onto one canonical profile. Sorted
			// source keys make equal timestamps deterministic: the first key wins.
			if !exists || observation.ObservedAt.After(current.ObservedAt) {
				s.observations[key] = observation
			}
		}
	}
	outcomeKeys := make([]string, 0, len(state.Outcomes))
	for key := range state.Outcomes {
		outcomeKeys = append(outcomeKeys, key)
	}
	sort.Strings(outcomeKeys)
	for _, sourceKey := range outcomeKeys {
		outcome := state.Outcomes[sourceKey]
		outcome.Profile = normalizeToolAdaptationProfile(outcome.Profile)
		outcome.VisibleToolSurface = strings.TrimSpace(outcome.VisibleToolSurface)
		outcome.ToolName = strings.TrimSpace(outcome.ToolName)
		if (outcome.Profile.Provider != "" || outcome.Profile.Model != "") && outcome.ToolName != "" {
			key := toolOutcomeKey(outcome.Profile, outcome.VisibleToolSurface, outcome.ToolName)
			current, exists := s.outcomes[key]
			if !exists {
				s.outcomes[key] = outcome
				continue
			}

			successes := current.Successes + outcome.Successes
			failures := current.Failures + outcome.Failures
			// Keep metadata from the newest entry. As above, sorted source keys
			// make an UpdatedAt tie deterministic by retaining the first key.
			if outcome.UpdatedAt.After(current.UpdatedAt) {
				current = outcome
			}
			current.Successes = successes
			current.Failures = failures
			s.outcomes[key] = current
		}
	}
}

func (s *toolAdaptationStateStore) saveLocked() error {
	state := toolAdaptationStateFile{
		Version:      1,
		Observations: make(map[string]ToolAdaptationObservation, len(s.observations)),
		Outcomes:     make(map[string]ToolAdaptationToolOutcome, len(s.outcomes)),
	}
	for key, observation := range s.observations {
		state.Observations[key] = observation
	}
	for key, outcome := range s.outcomes {
		state.Outcomes[key] = outcome
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(s.statePathLocked(), data, 0o600)
}

func (s *toolAdaptationStateStore) statePathLocked() string {
	if strings.TrimSpace(s.pathOverride) != "" {
		return s.pathOverride
	}
	return ToolAdaptationStatePath()
}

func normalizeToolAdaptationProfile(profile ToolAdaptationProfile) ToolAdaptationProfile {
	return ToolAdaptationProfile{
		Provider: providers.NormalizeProvider(profile.Provider),
		Model:    strings.ToLower(strings.TrimSpace(profile.Model)),
	}
}

func (p ToolAdaptationProfile) key() string {
	p = normalizeToolAdaptationProfile(p)
	return fmt.Sprintf("%s/%s", p.Provider, p.Model)
}

func toolOutcomeKey(profile ToolAdaptationProfile, visibleToolSurface string, toolName string) string {
	return fmt.Sprintf(
		"%s/%s/%s",
		profile.key(),
		strings.TrimSpace(visibleToolSurface),
		strings.TrimSpace(toolName),
	)
}
