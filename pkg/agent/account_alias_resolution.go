package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const accountAliasIdentitySeparator = "\x00"

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func accountAliasIdentityKey(accountRef, modelAlias string) string {
	accountRef = strings.TrimSpace(accountRef)
	modelAlias = strings.TrimSpace(modelAlias)
	if accountRef == "" || modelAlias == "" {
		return ""
	}
	return "account_ref:" + accountRef +
		accountAliasIdentitySeparator +
		"model_alias:" + modelAlias
}

func accountRefFromCandidateIdentityKey(identityKey string) string {
	const prefix = "account_ref:"
	identityKey = strings.TrimSpace(identityKey)
	if !strings.HasPrefix(identityKey, prefix) {
		return ""
	}
	accountPart, _, found := strings.Cut(
		strings.TrimPrefix(identityKey, prefix),
		accountAliasIdentitySeparator,
	)
	if !found {
		return ""
	}
	return strings.TrimSpace(accountPart)
}

func concreteAccountRefForCandidates(
	candidates []providers.FallbackCandidate,
) string {
	for _, candidate := range candidates {
		if accountRef := accountRefFromCandidateIdentityKey(candidate.IdentityKey); accountRef != "" {
			return accountRef
		}
	}
	return ""
}

func associateRouterSelectionCandidate(
	selection *accountrouter.Selection,
	candidate providers.FallbackCandidate,
	accountRef string,
) {
	if selection == nil || strings.TrimSpace(selection.RouterName) == "" {
		return
	}
	if selection.CandidateAccounts == nil {
		selection.CandidateAccounts = make(map[string]string)
	}
	if selection.ProviderAccounts == nil {
		selection.ProviderAccounts = make(map[string]string)
	}
	selection.CandidateAccounts[candidate.StableKey()] = accountRef
	selection.ProviderAccounts[providers.ModelKey(candidate.Provider, candidate.Model)] = accountRef
	selection.Candidates = []providers.FallbackCandidate{candidate}
}

func aliasFromAccountCandidateIdentityKey(identityKey string) string {
	const marker = accountAliasIdentitySeparator + "model_alias:"
	_, alias, found := strings.Cut(identityKey, marker)
	if !found {
		return ""
	}
	return strings.TrimSpace(alias)
}

func concreteAccountModelConfig(
	cfg *config.Config,
	accountRef string,
	modelAlias string,
	workspace string,
) (*config.ModelConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	accountRef = strings.TrimSpace(accountRef)
	if accountRef == "" {
		return nil, fmt.Errorf("no account configured")
	}
	modelID, err := cfg.ResolveModelAlias(modelAlias, accountRef)
	if err != nil {
		return nil, err
	}

	if credentialCfg, credential := accountRouterCredentialAccountConfig(
		accountRef,
		modelID,
	); credential {
		if credentialCfg == nil {
			return nil, fmt.Errorf("credential account %q is not supported", accountRef)
		}
		resolvedModel, resolveErr := providers.ResolveModelForProvider(
			credentialCfg.Provider,
			modelID,
		)
		if resolveErr != nil {
			return nil, resolveErr
		}
		credentialCfg.Model = resolvedModel
		credentialCfg.Workspace = workspace
		return credentialCfg, nil
	}

	accountCfg, err := cfg.GetEnabledModelConfig(accountRef)
	if err != nil {
		return nil, fmt.Errorf("account %q is not configured: %w", accountRef, err)
	}
	if accountCfg.IsAccountRouter() || accountCfg.IsModelRouter() {
		return nil, fmt.Errorf("account %q is not a concrete account", accountRef)
	}
	accountProvider, _ := providers.ExtractProtocol(accountCfg)
	if strings.TrimSpace(accountProvider) == "" {
		return nil, fmt.Errorf("account %q has no provider configured", accountRef)
	}
	resolvedModel, err := providers.ResolveModelForProvider(accountProvider, modelID)
	if err != nil {
		return nil, fmt.Errorf("model alias %q for account %q: %w", modelAlias, accountRef, err)
	}
	clone := cloneModelConfigForResolution("", accountCfg, workspace)
	clone.Provider = accountProvider
	clone.Model = resolvedModel
	return clone, nil
}

func candidateForAccountAlias(
	cfg *config.Config,
	accountRef string,
	modelAlias string,
	workspace string,
	providersOut map[string]providers.LLMProvider,
	policy isolation.ExecutionPolicy,
) (providers.FallbackCandidate, error) {
	modelCfg, err := concreteAccountModelConfig(
		cfg,
		accountRef,
		modelAlias,
		workspace,
	)
	if err != nil {
		return providers.FallbackCandidate{}, err
	}
	candidate, ok := candidateFromModelConfig("", modelCfg)
	if !ok {
		return providers.FallbackCandidate{}, config.ErrNoModelConfigured
	}
	candidate.DisplayName = strings.TrimSpace(modelAlias)
	candidate.IdentityKey = accountAliasIdentityKey(accountRef, modelAlias)
	agentCandidateProvidersMu.RLock()
	retainedProvider := providersOut[candidate.IdentityKey]
	agentCandidateProvidersMu.RUnlock()
	if retainedProvider != nil {
		return candidate, nil
	}

	provider, _, err := providers.CreateProviderFromConfigWithExecutionPolicy(
		modelCfg,
		policy,
	)
	if err != nil {
		return providers.FallbackCandidate{}, err
	}
	if !registerCandidateProvider(providersOut, candidate, provider) {
		closeProviderIfStateful(provider)
	}
	return candidate, nil
}

func candidatesForAccountAliases(
	cfg *config.Config,
	accountRef string,
	primaryAlias string,
	fallbackAliases []string,
	workspace string,
	providersOut map[string]providers.LLMProvider,
	executionPolicies ...isolation.ExecutionPolicy,
) ([]providers.FallbackCandidate, error) {
	var policy isolation.ExecutionPolicy
	if len(executionPolicies) > 0 {
		policy = executionPolicies[0]
	} else {
		isolationCfg := config.DefaultConfig().Isolation
		if cfg != nil {
			isolationCfg = cfg.Isolation
		}
		policy = isolation.NewExecutionPolicy(isolationCfg)
	}
	if err := validateModelAliasReferences(cfg, primaryAlias, fallbackAliases); err != nil {
		return nil, err
	}
	aliases := make([]string, 0, 1+len(fallbackAliases))
	aliases = append(aliases, primaryAlias)
	aliases = append(aliases, fallbackAliases...)
	seen := make(map[string]bool, len(aliases))
	out := make([]providers.FallbackCandidate, 0, len(aliases))
	for aliasIndex, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || seen[alias] {
			continue
		}
		seen[alias] = true
		candidate, err := candidateForAccountAlias(
			cfg,
			accountRef,
			alias,
			workspace,
			providersOut,
			policy,
		)
		if err != nil {
			if aliasIndex > 0 && errors.Is(err, config.ErrModelAliasDisabled) {
				continue
			}
			return nil, fmt.Errorf(
				"model alias %q is not runnable for account %q: %w",
				alias,
				accountRef,
				err,
			)
		}
		out = append(out, candidate)
	}
	if len(out) == 0 {
		return nil, config.ErrNoModelConfigured
	}
	return out, nil
}

func validateModelAliasReferences(
	cfg *config.Config,
	primaryAlias string,
	fallbackAliases []string,
) error {
	primaryAlias = strings.TrimSpace(primaryAlias)
	if primaryAlias == "" {
		return config.ErrNoModelConfigured
	}
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if _, err := cfg.GetModelAlias(primaryAlias); err != nil {
		return err
	}
	for _, alias := range fallbackAliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if _, err := cfg.GetModelAlias(alias); err != nil {
			return fmt.Errorf("model fallback alias %q: %w", alias, err)
		}
	}
	return nil
}

func buildAccountRouterWithAliases(
	cfg *config.Config,
	accountRef string,
	primaryAlias string,
	fallbackAliases []string,
	workspace string,
	providersOut map[string]providers.LLMProvider,
	executionPolicies ...isolation.ExecutionPolicy,
) *accountrouter.Router {
	routerCfg := lookupAccountRouterConfig(cfg, accountRef)
	if routerCfg == nil {
		return nil
	}
	if err := validateModelAliasReferences(cfg, primaryAlias, fallbackAliases); err != nil {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentAccountRouterModelAliasesAreInvalid,
			logger.NewSafeFields(
				agentDiagnosticRouteField(accountRef),
				agentDiagnosticModelField(primaryAlias),
				agentDiagnosticErrorField(logger.ErrorClassValidation, err),
			),
		)
		return nil
	}
	accountNames := accountRouterAccountNames(routerCfg)
	accounts := make(map[string]accountrouter.Account, len(accountNames))
	for _, concreteAccount := range accountNames {
		candidates, err := candidatesForAccountAliases(
			cfg,
			concreteAccount,
			primaryAlias,
			fallbackAliases,
			workspace,
			providersOut,
			executionPolicies...,
		)
		if err != nil {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentAccountRouterAccountHasNoRunnableModelAlias,
				logger.NewSafeFields(
					agentDiagnosticRouteField(accountRef),
					agentDiagnosticAccountField(concreteAccount),
					agentDiagnosticModelField(primaryAlias),
					agentDiagnosticErrorField(logger.ErrorClassValidation, err),
				),
			)
			continue
		}
		rpm := 0
		if accountCfg, err := cfg.GetEnabledModelConfig(concreteAccount); err == nil &&
			accountCfg != nil {
			rpm = accountCfg.RPM
		}
		accounts[concreteAccount] = accountrouter.Account{
			Name:       concreteAccount,
			Candidates: candidates,
			RPM:        rpm,
		}
	}
	return accountrouter.NewForWorkspace(accountRef, routerCfg, accounts, workspace)
}

func lookupAccountRouterConfig(
	cfg *config.Config,
	accountRef string,
) *config.AccountRouterConfig {
	if cfg == nil {
		return nil
	}
	accountRef = strings.TrimSpace(accountRef)
	for i := range cfg.AccountRouters {
		if cfg.AccountRouters[i].Enabled &&
			strings.TrimSpace(cfg.AccountRouters[i].Name) == accountRef {
			return &cfg.AccountRouters[i]
		}
	}
	if modelCfg, err := cfg.GetEnabledModelConfig(accountRef); err == nil &&
		modelCfg != nil &&
		modelCfg.IsAccountRouter() {
		return modelCfg.Router
	}
	return nil
}
