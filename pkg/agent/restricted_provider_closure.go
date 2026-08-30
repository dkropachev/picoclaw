package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
)

// RestrictedProviderClosureStatus is the stable outcome of a restricted
// provider closure audit.
type RestrictedProviderClosureStatus string

const (
	RestrictedProviderClosureSafe   RestrictedProviderClosureStatus = "safe"
	RestrictedProviderClosureUnsafe RestrictedProviderClosureStatus = "unsafe_provider"
)

// RestrictedProviderClosureAudit reports whether every provider reachable by
// one agent is permitted for a controller-owned operation. Provider is set to
// the normalized restricted provider only for an unsafe result.
type RestrictedProviderClosureAudit struct {
	Status   RestrictedProviderClosureStatus
	Provider string
}

// AuditRestrictedProviderClosureWithRuntimeLease audits the exact agent and
// configuration generation carried by ctx. It resolves configuration only;
// it never selects a route, constructs a provider, or accesses a workspace.
func (al *AgentLoop) AuditRestrictedProviderClosureWithRuntimeLease(
	ctx context.Context,
	agentID string,
) (RestrictedProviderClosureAudit, error) {
	if al == nil {
		return RestrictedProviderClosureAudit{}, errors.New(
			"restricted provider closure runtime is unavailable",
		)
	}
	generation, err := al.runtimeGenerationFromLease(ctx)
	if err != nil {
		return RestrictedProviderClosureAudit{}, errors.New(
			"restricted provider closure runtime lease is unavailable",
		)
	}
	if agentID != strings.TrimSpace(agentID) || !routing.IsCanonicalAgentID(agentID) {
		return RestrictedProviderClosureAudit{}, errors.New(
			"restricted provider closure agent ID must be exact and canonical",
		)
	}
	agent, ok := generation.registry.GetAgent(agentID)
	if !ok || agent == nil || agent.ID != agentID {
		return RestrictedProviderClosureAudit{}, errors.New(
			"restricted provider closure agent is unavailable",
		)
	}
	if agent.ConfigurationError != nil {
		return RestrictedProviderClosureAudit{}, errors.New(
			"restricted provider closure agent configuration is invalid",
		)
	}
	return auditRestrictedProviderClosure(generation.cfg, agent)
}

func auditRestrictedProviderClosure(
	cfg *config.Config,
	agent *AgentInstance,
) (RestrictedProviderClosureAudit, error) {
	if cfg == nil || agent == nil {
		return RestrictedProviderClosureAudit{}, errors.New(
			"restricted provider closure configuration is unavailable",
		)
	}

	// Inspect the already materialized runtime graph first. The configuration
	// pass below proves routes (notably model-router targets) that are resolved
	// lazily, while this pass also fails closed if a runtime binding diverges.
	if audit, unsafe := auditRestrictedCandidates(agent.Candidates); unsafe {
		return audit, nil
	}
	if audit, unsafe := auditRestrictedAccountRouter(agent.AccountRouter); unsafe {
		return audit, nil
	}
	if audit, unsafe := auditRestrictedCandidates(agent.LightCandidates); unsafe {
		return audit, nil
	}
	if audit, unsafe := auditRestrictedAccountRouter(agent.LightAccountRouter); unsafe {
		return audit, nil
	}

	accountRefs, err := restrictedProviderClosureAccounts(cfg, agent.AccountRef)
	if err != nil {
		return RestrictedProviderClosureAudit{}, err
	}
	var closureErr error
	if agent.ModelRouter == nil {
		if audit, unsafe, auditErr := auditRestrictedAliasChain(
			cfg,
			accountRefs,
			agent.Model,
			agent.Fallbacks,
			agent.Workspace,
		); unsafe {
			return audit, nil
		} else if auditErr != nil {
			closureErr = auditErr
		}
	} else {
		seenTargets := make(map[string]bool)
		foundTarget := false
		for _, block := range agent.ModelRouter.Config.Blocks {
			if strings.TrimSpace(block.Type) != config.ModelRouterBlockTypeModel {
				continue
			}
			target := strings.TrimSpace(block.Model)
			if target == "" || seenTargets[target] {
				continue
			}
			seenTargets[target] = true
			foundTarget = true
			if audit, unsafe, auditErr := auditRestrictedAliasChain(
				cfg,
				accountRefs,
				target,
				agent.Fallbacks,
				agent.Workspace,
			); unsafe {
				return audit, nil
			} else if auditErr != nil && closureErr == nil {
				closureErr = auditErr
			}
		}
		if !foundTarget {
			closureErr = errors.New(
				"restricted provider closure model router has no targets",
			)
		}
	}

	if agent.Router != nil {
		if audit, unsafe, auditErr := auditRestrictedAliasChain(
			cfg,
			accountRefs,
			agent.Router.LightModel(),
			nil,
			agent.Workspace,
		); unsafe {
			return audit, nil
		} else if auditErr != nil && closureErr == nil {
			closureErr = auditErr
		}
	}
	if closureErr != nil {
		return RestrictedProviderClosureAudit{}, closureErr
	}

	return RestrictedProviderClosureAudit{Status: RestrictedProviderClosureSafe}, nil
}

func auditRestrictedCandidates(
	candidates []providers.FallbackCandidate,
) (RestrictedProviderClosureAudit, bool) {
	for _, candidate := range candidates {
		if provider, unsafe := restrictedProvider(candidate.Provider); unsafe {
			return unsafeRestrictedProviderAudit(provider), true
		}
	}
	return RestrictedProviderClosureAudit{}, false
}

func auditRestrictedAccountRouter(
	router *accountrouter.Router,
) (RestrictedProviderClosureAudit, bool) {
	if router == nil {
		return RestrictedProviderClosureAudit{}, false
	}
	accountNames := make([]string, 0, len(router.Accounts))
	for accountName := range router.Accounts {
		accountNames = append(accountNames, accountName)
	}
	sort.Strings(accountNames)
	for _, accountName := range accountNames {
		if audit, unsafe := auditRestrictedCandidates(
			router.Accounts[accountName].Candidates,
		); unsafe {
			return audit, true
		}
	}
	return RestrictedProviderClosureAudit{}, false
}

func restrictedProviderClosureAccounts(
	cfg *config.Config,
	accountRef string,
) ([]string, error) {
	accountRef = strings.TrimSpace(accountRef)
	if accountRef == "" {
		return nil, errors.New("restricted provider closure account is unavailable")
	}
	if router := lookupAccountRouterConfig(cfg, accountRef); router != nil {
		accounts := accountRouterAccountNames(router)
		if len(accounts) == 0 {
			return nil, errors.New(
				"restricted provider closure account router has no terminals",
			)
		}
		return accounts, nil
	}
	return []string{accountRef}, nil
}

func auditRestrictedAliasChain(
	cfg *config.Config,
	accountRefs []string,
	primary string,
	fallbacks []string,
	workspace string,
) (RestrictedProviderClosureAudit, bool, error) {
	if strings.TrimSpace(primary) == "" {
		return RestrictedProviderClosureAudit{}, false, errors.New(
			"restricted provider closure has no primary model alias",
		)
	}
	aliases := make([]string, 0, 1+len(fallbacks))
	aliases = append(aliases, primary)
	aliases = append(aliases, fallbacks...)
	seen := make(map[string]bool, len(aliases))
	var closureErr error
	for aliasIndex, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || seen[alias] {
			continue
		}
		seen[alias] = true
		for _, accountRef := range accountRefs {
			modelCfg, err := concreteAccountModelConfig(
				cfg,
				accountRef,
				alias,
				workspace,
			)
			if err != nil {
				if aliasIndex > 0 && errors.Is(err, config.ErrModelAliasDisabled) {
					continue
				}
				if closureErr == nil {
					closureErr = fmt.Errorf(
						"restricted provider closure cannot resolve model alias: %w",
						err,
					)
				}
				continue
			}
			provider, _ := providers.ExtractProtocol(modelCfg)
			if normalized, unsafe := restrictedProvider(provider); unsafe {
				return unsafeRestrictedProviderAudit(normalized), true, nil
			}
		}
	}
	if closureErr != nil {
		return RestrictedProviderClosureAudit{}, false, closureErr
	}
	return RestrictedProviderClosureAudit{}, false, nil
}

func unsafeRestrictedProviderAudit(provider string) RestrictedProviderClosureAudit {
	return RestrictedProviderClosureAudit{
		Status:   RestrictedProviderClosureUnsafe,
		Provider: provider,
	}
}

func restrictedProvider(provider string) (string, bool) {
	normalized := providers.NormalizeProvider(provider)
	switch normalized {
	case "codex-cli", "claude-cli":
		return normalized, true
	default:
		return "", false
	}
}
