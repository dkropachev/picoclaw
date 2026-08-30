package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"

	providercommon "github.com/sipeed/picoclaw/pkg/providers/common"
)

const (
	localRepairCacheKeyDomain = "picoclaw-local-repair-cache-v1\x00"
	localRepairProfileDomain  = "picoclaw-local-repair-profile-v1\x00"
)

type localRepairProviderProfile struct {
	MaxTokens       int
	Temperature     float64
	CacheKey        string
	ReasoningEffort string
}

func normalizeLocalRepairReasoningEffort(raw string) (string, error) {
	effort, err := providercommon.NormalizeReasoningEffort(raw)
	if err != nil {
		return "", errors.New("local repair reasoning effort is invalid")
	}
	return effort, nil
}

func newLocalRepairProviderProfile(
	maxTokens int,
	temperature float64,
	reasoningEffort string,
	workspaceID string,
	promptDigest string,
) (localRepairProviderProfile, error) {
	if maxTokens < 1 || maxTokens > 1<<20 || math.IsNaN(temperature) ||
		math.IsInf(temperature, 0) || temperature < 0 || temperature > 2 {
		return localRepairProviderProfile{}, errors.New("local repair provider profile is invalid")
	}
	effort, err := normalizeLocalRepairReasoningEffort(reasoningEffort)
	if err != nil {
		return localRepairProviderProfile{}, err
	}
	cacheKey, err := localRepairPromptCacheKey(workspaceID, promptDigest)
	if err != nil {
		return localRepairProviderProfile{}, err
	}
	return localRepairProviderProfile{
		MaxTokens:       maxTokens,
		Temperature:     temperature,
		CacheKey:        cacheKey,
		ReasoningEffort: effort,
	}, nil
}

func localRepairPromptCacheKey(workspaceID, promptDigest string) (string, error) {
	if workspaceID == "" || workspaceID != strings.TrimSpace(workspaceID) ||
		!validLocalRepairIdentity(workspaceID, 1024) ||
		promptDigest == "" || promptDigest != strings.TrimSpace(promptDigest) ||
		!validLocalRepairIdentity(promptDigest, 1024) {
		return "", errors.New("local repair cache identity is invalid")
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(localRepairCacheKeyDomain))
	_, _ = digest.Write([]byte(workspaceID))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(promptDigest))
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (profile localRepairProviderProfile) options() map[string]any {
	options := map[string]any{
		"max_tokens":       profile.MaxTokens,
		"temperature":      profile.Temperature,
		"prompt_cache_key": profile.CacheKey,
	}
	if profile.ReasoningEffort != "" {
		options["reasoning_effort"] = profile.ReasoningEffort
	}
	return options
}

func (profile localRepairProviderProfile) digest() (string, error) {
	if err := profile.validateOptions(profile.options()); err != nil {
		return "", err
	}
	// A fixed struct gives the profile a deterministic field order without
	// exposing workspace, prompt, account, provider, or model identity.
	canonical, err := json.Marshal(struct {
		MaxTokens       int     `json:"max_tokens"`
		Temperature     float64 `json:"temperature"`
		CacheKey        string  `json:"prompt_cache_key"`
		ReasoningEffort string  `json:"reasoning_effort,omitempty"`
	}{
		MaxTokens:       profile.MaxTokens,
		Temperature:     profile.Temperature,
		CacheKey:        profile.CacheKey,
		ReasoningEffort: profile.ReasoningEffort,
	})
	if err != nil {
		return "", errors.New("local repair provider profile is invalid")
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(localRepairProfileDomain))
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func (profile localRepairProviderProfile) validateOptions(options map[string]any) error {
	wantCount := 3
	if profile.ReasoningEffort != "" {
		wantCount++
	}
	if len(options) != wantCount {
		return errors.New("local repair provider profile is invalid")
	}
	maxTokens, maxTokensOK := options["max_tokens"].(int)
	temperature, temperatureOK := options["temperature"].(float64)
	cacheKey, cacheKeyOK := options["prompt_cache_key"].(string)
	if !maxTokensOK || !temperatureOK || !cacheKeyOK ||
		maxTokens != profile.MaxTokens || temperature != profile.Temperature ||
		cacheKey != profile.CacheKey || !validLocalRepairOpaqueDigest(cacheKey) {
		return errors.New("local repair provider profile is invalid")
	}
	rawEffort, effortPresent := options["reasoning_effort"]
	if profile.ReasoningEffort == "" {
		if effortPresent {
			return errors.New("local repair provider profile is invalid")
		}
		return nil
	}
	effort, effortOK := rawEffort.(string)
	if !effortOK || effort != profile.ReasoningEffort {
		return errors.New("local repair provider profile is invalid")
	}
	normalized, err := providercommon.NormalizeReasoningEffort(effort)
	if err != nil || normalized != effort {
		return errors.New("local repair provider profile is invalid")
	}
	return nil
}

func validLocalRepairOpaqueDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}
