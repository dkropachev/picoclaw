package agent

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func cloneModelConfigForResolution(
	_ string,
	modelCfg *config.ModelConfig,
	workspace string,
) *config.ModelConfig {
	if modelCfg == nil {
		return nil
	}
	clone := *modelCfg
	if clone.Workspace == "" {
		clone.Workspace = workspace
	}
	return &clone
}

func candidateFromModelConfig(
	_ string,
	modelCfg *config.ModelConfig,
) (providers.FallbackCandidate, bool) {
	if modelCfg == nil {
		return providers.FallbackCandidate{}, false
	}
	protocol, modelID := providers.ExtractProtocol(modelCfg)
	if strings.TrimSpace(protocol) == "" || strings.TrimSpace(modelID) == "" {
		return providers.FallbackCandidate{}, false
	}
	return providers.FallbackCandidate{
		Provider:    protocol,
		Model:       modelID,
		DisplayName: strings.TrimSpace(modelCfg.ModelName),
		RPM:         modelCfg.RPM,
	}, true
}

func resolvedCandidateModel(
	candidates []providers.FallbackCandidate,
	fallback string,
) string {
	if len(candidates) > 0 && strings.TrimSpace(candidates[0].Model) != "" {
		return candidates[0].Model
	}
	return strings.TrimSpace(fallback)
}

func resolvedCandidateProvider(
	candidates []providers.FallbackCandidate,
	fallback string,
) string {
	if len(candidates) > 0 && strings.TrimSpace(candidates[0].Provider) != "" {
		return candidates[0].Provider
	}
	return strings.TrimSpace(fallback)
}

func resolvedCandidateModelName(
	candidates []providers.FallbackCandidate,
	fallback string,
) string {
	if len(candidates) > 0 {
		if alias := modelAliasFromCandidateIdentityKey(
			candidates[0].IdentityKey,
		); alias != "" {
			return alias
		}
		if displayName := strings.TrimSpace(candidates[0].DisplayName); displayName != "" {
			return displayName
		}
	}
	return strings.TrimSpace(fallback)
}

// resolveActiveModelConfig reconstructs the concrete provider configuration
// only from the account+alias identity attached during strict alias
// resolution. Raw provider/model candidates are intentionally not resolved.
func resolveActiveModelConfig(
	cfg *config.Config,
	workspace string,
	candidates []providers.FallbackCandidate,
	_ string,
	_ string,
) *config.ModelConfig {
	if cfg == nil || len(candidates) == 0 {
		return nil
	}
	candidate := candidates[0]
	accountRef := accountRefFromCandidateIdentityKey(candidate.IdentityKey)
	modelAlias := modelAliasFromCandidateIdentityKey(candidate.IdentityKey)
	if accountRef == "" || modelAlias == "" {
		return nil
	}
	modelCfg, err := concreteAccountModelConfig(
		cfg,
		accountRef,
		modelAlias,
		workspace,
	)
	if err != nil {
		return nil
	}
	modelCfg.Model = strings.TrimSpace(candidate.Model)
	return modelCfg
}
