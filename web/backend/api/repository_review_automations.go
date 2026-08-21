package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

type repositoryReviewAutomationConfigRequest struct {
	Name                  string                                          `json:"name"`
	Repository            string                                          `json:"repository"`
	Ref                   string                                          `json:"ref,omitempty"`
	Target                string                                          `json:"target"`
	ReviewFocus           string                                          `json:"review_focus"`
	ScopePolicy           repoaudit.RepositoryReviewScopePolicy           `json:"scope_policy"`
	ReviewerModels        []string                                        `json:"reviewer_models"`
	CompareModels         bool                                            `json:"compare_models"`
	ModelPrices           map[string]repoaudit.RepositoryReviewModelPrice `json:"model_prices,omitempty"`
	Force                 bool                                            `json:"force"`
	AutoContinue          *bool                                           `json:"auto_continue,omitempty"`
	MaxFilesPerRun        int                                             `json:"max_files_per_run"`
	MaxContentBytes       int64                                           `json:"max_content_bytes"`
	MaxParallelChildren   int                                             `json:"max_parallel_children"`
	EstimatedOutputTokens int                                             `json:"estimated_output_tokens"`
	Budget                repoaudit.RepositoryReviewBudgetPolicy          `json:"budget"`
	ExpectedVersion       int64                                           `json:"expected_version,omitempty"`
}

type repositoryReviewAutomationActionRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
	ResetBudget     bool  `json:"reset_budget,omitempty"`
}

type repositoryReviewModelOption struct {
	Alias            string  `json:"alias"`
	ResolvedModel    string  `json:"resolved_model"`
	Provider         string  `json:"provider,omitempty"`
	Available        bool    `json:"available"`
	BlockedReason    string  `json:"blocked_reason,omitempty"`
	PriceKnown       bool    `json:"price_known"`
	InputPricePer1M  float64 `json:"input_price_per_1m,omitempty"`
	OutputPricePer1M float64 `json:"output_price_per_1m,omitempty"`
	Subscription     bool    `json:"subscription,omitempty"`
	EquivalentModel  string  `json:"equivalent_model,omitempty"`
	Default          bool    `json:"default,omitempty"`
}

type repositoryReviewAccountOption struct {
	ID       string                               `json:"id"`
	Provider string                               `json:"provider,omitempty"`
	Label    string                               `json:"label"`
	Status   string                               `json:"status"`
	Entries  []repositoryReviewAccountLimitOption `json:"entries"`
}

type repositoryReviewAccountLimitOption struct {
	Name             string   `json:"name"`
	Status           string   `json:"status"`
	Window           string   `json:"window,omitempty"`
	UsedPercent      *int     `json:"used_percent,omitempty"`
	RemainingPercent *float64 `json:"remaining_percent,omitempty"`
	RefreshesAt      string   `json:"refreshes_at,omitempty"`
}

func (h *Handler) registerRepositoryReviewAutomationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repository-reviews/automations", h.handleListRepositoryReviewAutomations)
	mux.HandleFunc("POST /api/repository-reviews/automations", h.handleCreateRepositoryReviewAutomation)
	mux.HandleFunc(
		"PATCH /api/repository-reviews/automations/{automation_id}",
		h.handleUpdateRepositoryReviewAutomation,
	)
	mux.HandleFunc(
		"DELETE /api/repository-reviews/automations/{automation_id}",
		h.handleDeleteRepositoryReviewAutomation,
	)
	mux.HandleFunc(
		"POST /api/repository-reviews/automations/{automation_id}/start",
		h.handleStartRepositoryReviewAutomation,
	)
	mux.HandleFunc(
		"POST /api/repository-reviews/automations/{automation_id}/pause",
		h.handlePauseRepositoryReviewAutomation,
	)
	mux.HandleFunc(
		"POST /api/repository-reviews/automations/{automation_id}/resume",
		h.handleResumeRepositoryReviewAutomation,
	)
	mux.HandleFunc(
		"POST /api/repository-reviews/automations/{automation_id}/restart",
		h.handleRestartRepositoryReviewAutomation,
	)
	mux.HandleFunc("GET /api/repository-reviews/automation-options", h.handleRepositoryReviewAutomationOptions)
}

func (h *Handler) handleListRepositoryReviewAutomations(w http.ResponseWriter, r *http.Request) {
	store, err := h.repositoryReviewStore()
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	automations, err := store.ListAutomations(r.Context())
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	projected := make([]repoaudit.RepositoryReviewAutomation, len(automations))
	for index := range automations {
		projected[index] = projectRepositoryReviewAutomation(automations[index])
	}
	writeRepositoryReviewJSON(w, http.StatusOK, map[string]any{"automations": projected})
}

func (h *Handler) handleCreateRepositoryReviewAutomation(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	var request repositoryReviewAutomationConfigRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	store, err := h.repositoryReviewStore()
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	automation := repositoryReviewAutomationFromRequest(request)
	if automation.Name == "" {
		automation.Name = automation.Repository
	}
	if len(automation.ReviewerModels) == 0 {
		if cfg, cfgErr := config.LoadConfig(h.configPath); cfgErr == nil {
			if model := strings.TrimSpace(cfg.Agents.Defaults.GetModelName()); model != "" {
				automation.ReviewerModels = []string{model}
			}
		}
	}
	created, err := store.CreateAutomation(r.Context(), automation)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	writeRepositoryReviewJSON(
		w,
		http.StatusCreated,
		map[string]any{"automation": projectRepositoryReviewAutomation(created)},
	)
}

func (h *Handler) handleUpdateRepositoryReviewAutomation(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	var request repositoryReviewAutomationConfigRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	store, err := h.repositoryReviewStore()
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	updated, err := store.UpdateAutomation(
		r.Context(), r.PathValue("automation_id"), request.ExpectedVersion,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			if candidate.Status == repoaudit.RepositoryReviewAutomationRunning ||
				candidate.Status == repoaudit.RepositoryReviewAutomationStopping {
				return errRepositoryReviewAutomationBusy
			}
			previous := *candidate
			previous.BudgetPolicy.AccountIDs = append([]string(nil), candidate.BudgetPolicy.AccountIDs...)
			previous.BudgetPolicy.MinRemainingPercentByWindow = maps.Clone(
				candidate.BudgetPolicy.MinRemainingPercentByWindow,
			)
			applyRepositoryReviewAutomationRequest(candidate, request)
			if repositoryReviewExecutionConfigurationChanged(previous, *candidate) {
				candidate.Status = repoaudit.RepositoryReviewAutomationIdle
				candidate.ScopePlan = repoaudit.RepositoryReviewScopePlan{}
				candidate.PauseReason = ""
				candidate.PauseDetail = ""
				candidate.RequestedPauseReason = ""
				candidate.RequestedPauseDetail = ""
				candidate.Progress = repoaudit.RepositoryReviewProgress{}
				candidate.Usage = repoaudit.RepositoryReviewTokenUsage{}
				candidate.EstimatedCostUSD = 0
				candidate.ModelStats = make(map[string]repoaudit.RepositoryReviewModelStats)
				candidate.ModelCoverageSketches = make(map[string]string)
				candidate.StartedAt = time.Time{}
				candidate.CompletedAt = time.Time{}
				candidate.NextCheckAt = time.Time{}
				candidate.AccountLimitSnapshots = nil
			} else {
				if repositoryReviewQuotaConfigurationChanged(previous.BudgetPolicy, candidate.BudgetPolicy) {
					candidate.AccountLimitSnapshots = nil
					candidate.NextCheckAt = time.Time{}
				}
			}
			return nil
		},
	)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	writeRepositoryReviewJSON(
		w,
		http.StatusOK,
		map[string]any{"automation": projectRepositoryReviewAutomation(updated)},
	)
}

func (h *Handler) handleDeleteRepositoryReviewAutomation(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	var request repositoryReviewAutomationActionRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	store, err := h.repositoryReviewStore()
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	automation, found, err := store.GetAutomation(r.Context(), r.PathValue("automation_id"))
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	if automation.Status == repoaudit.RepositoryReviewAutomationRunning ||
		automation.Status == repoaudit.RepositoryReviewAutomationStopping {
		writeRepositoryReviewAutomationError(w, errRepositoryReviewAutomationBusy)
		return
	}
	if err := store.DeleteAutomation(r.Context(), automation.ID, request.ExpectedVersion); err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleStartRepositoryReviewAutomation(w http.ResponseWriter, r *http.Request) {
	h.handleRepositoryReviewAutomationStartAction(w, r, false, false)
}

func (h *Handler) handleResumeRepositoryReviewAutomation(w http.ResponseWriter, r *http.Request) {
	h.handleRepositoryReviewAutomationStartAction(w, r, true, false)
}

func (h *Handler) handleRestartRepositoryReviewAutomation(w http.ResponseWriter, r *http.Request) {
	h.handleRepositoryReviewAutomationStartAction(w, r, true, true)
}

func (h *Handler) handleRepositoryReviewAutomationStartAction(
	w http.ResponseWriter,
	r *http.Request,
	allowReset bool,
	restart bool,
) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	var request repositoryReviewAutomationActionRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	reset := allowReset && request.ResetBudget
	action := "start"
	if restart {
		action = "restart"
	} else if allowReset {
		action = "resume"
	}
	controller := h.repositoryReviewControllerInstance()
	if controller == nil {
		writeRepositoryReviewAutomationError(w, errors.New("repository review controller unavailable"))
		return
	}
	automation, err := controller.startAutomation(
		r.Context(), r.PathValue("automation_id"), request.ExpectedVersion, reset, action,
	)
	if errors.Is(err, errRepositoryReviewGuardBlocked) && automation.ID != "" {
		writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
			"automation": projectRepositoryReviewAutomation(automation),
			"outcome":    "paused",
		})
		return
	}
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
		"automation": projectRepositoryReviewAutomation(automation),
		"outcome":    "started",
	})
}

func (h *Handler) handlePauseRepositoryReviewAutomation(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	var request repositoryReviewAutomationActionRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	controller := h.repositoryReviewControllerInstance()
	if controller == nil {
		writeRepositoryReviewAutomationError(w, errors.New("repository review controller unavailable"))
		return
	}
	automation, err := controller.pauseAutomation(
		r.Context(), r.PathValue("automation_id"), request.ExpectedVersion,
	)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
		"automation": projectRepositoryReviewAutomation(automation),
	})
}

func projectRepositoryReviewAutomation(
	automation repoaudit.RepositoryReviewAutomation,
) repoaudit.RepositoryReviewAutomation {
	automation.ModelCoverageSketches = nil
	return automation
}

func repositoryReviewAutomationFromRequest(
	request repositoryReviewAutomationConfigRequest,
) repoaudit.RepositoryReviewAutomation {
	automation := repoaudit.RepositoryReviewAutomation{
		Name: request.Name, Repository: request.Repository, Ref: request.Ref,
		Target: request.Target, ReviewFocus: request.ReviewFocus,
		ScopePolicy:    request.ScopePolicy,
		ReviewerModels: request.ReviewerModels, CompareModels: request.CompareModels,
		ModelPrices: request.ModelPrices, Force: request.Force,
		MaxFilesPerRun: request.MaxFilesPerRun, MaxContentBytes: request.MaxContentBytes,
		MaxParallelChildren:   request.MaxParallelChildren,
		EstimatedOutputTokens: request.EstimatedOutputTokens,
		BudgetPolicy:          request.Budget,
		Status:                repoaudit.RepositoryReviewAutomationIdle,
	}
	if request.AutoContinue == nil {
		automation.AutoContinue = true
	} else {
		automation.AutoContinue = *request.AutoContinue
	}
	return automation
}

func applyRepositoryReviewAutomationRequest(
	automation *repoaudit.RepositoryReviewAutomation,
	request repositoryReviewAutomationConfigRequest,
) {
	if automation == nil {
		return
	}
	automation.Name = request.Name
	automation.Repository = request.Repository
	automation.Ref = request.Ref
	automation.Target = request.Target
	automation.ReviewFocus = request.ReviewFocus
	automation.ScopePolicy = request.ScopePolicy
	automation.ReviewerModels = append([]string(nil), request.ReviewerModels...)
	automation.CompareModels = request.CompareModels
	automation.ModelPrices = request.ModelPrices
	automation.Force = request.Force
	if request.AutoContinue != nil {
		automation.AutoContinue = *request.AutoContinue
	}
	automation.MaxFilesPerRun = request.MaxFilesPerRun
	automation.MaxContentBytes = request.MaxContentBytes
	automation.MaxParallelChildren = request.MaxParallelChildren
	automation.EstimatedOutputTokens = request.EstimatedOutputTokens
	automation.BudgetPolicy = request.Budget
}

func repositoryReviewExecutionConfigurationChanged(
	previous, next repoaudit.RepositoryReviewAutomation,
) bool {
	return previous.Repository != next.Repository || previous.Ref != next.Ref ||
		previous.Target != next.Target || previous.ReviewFocus != next.ReviewFocus ||
		!repositoryReviewScopePoliciesEqual(previous.ScopePolicy, next.ScopePolicy) ||
		previous.CompareModels != next.CompareModels || previous.Force != next.Force ||
		previous.MaxContentBytes != next.MaxContentBytes ||
		!slicesEqual(previous.ReviewerModels, next.ReviewerModels)
}

func repositoryReviewScopePoliciesEqual(
	left, right repoaudit.RepositoryReviewScopePolicy,
) bool {
	left, leftErr := repoaudit.NormalizeRepositoryReviewScopePolicy(left)
	right, rightErr := repoaudit.NormalizeRepositoryReviewScopePolicy(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return slicesEqualRepositoryReviewCodeTypes(left.CodeTypes, right.CodeTypes) &&
		slicesEqual(left.IncludeFolders, right.IncludeFolders) &&
		slicesEqual(left.ExcludeFolders, right.ExcludeFolders) &&
		left.FreeText == right.FreeText
}

func slicesEqualRepositoryReviewCodeTypes(
	left, right []repoaudit.RepositoryReviewCodeType,
) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func repositoryReviewQuotaConfigurationChanged(
	previous, next repoaudit.RepositoryReviewBudgetPolicy,
) bool {
	return !slicesEqual(previous.AccountIDs, next.AccountIDs) ||
		previous.MinRemainingPercent != next.MinRemainingPercent ||
		!maps.Equal(previous.MinRemainingPercentByWindow, next.MinRemainingPercentByWindow) ||
		previous.PauseOnUnknown != next.PauseOnUnknown ||
		previous.CheckIntervalSeconds != next.CheckIntervalSeconds
}

func (h *Handler) handleRepositoryReviewAutomationOptions(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	models := repositoryReviewModelOptions(cfg)
	limitsCtx, cancelLimits := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancelLimits()
	limits, limitsErr := loadCodexAccountLimits(limitsCtx)
	accounts := repositoryReviewAccountOptions(limits)
	response := map[string]any{"models": models, "accounts": accounts}
	if limitsError := repositoryReviewLimitsError(limits, limitsErr); limitsError != "" {
		response["limits_error"] = limitsError
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func repositoryReviewLimitsError(limits codexAccountLimitsResponse, limitsErr error) string {
	if limitsErr != nil {
		return limitsErr.Error()
	}
	return strings.TrimSpace(limits.Error)
}

func repositoryReviewModelOptions(cfg *config.Config) []repositoryReviewModelOption {
	if cfg == nil {
		return []repositoryReviewModelOption{}
	}
	defaultModel := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	options := make([]repositoryReviewModelOption, 0, len(cfg.ModelAliases))
	for _, alias := range cfg.ModelAliases {
		resolved := strings.TrimSpace(alias.Model)
		provider, concrete := protocoltypes.SplitKnownProviderModel(resolved)
		if provider != "" {
			resolved = concrete
		}
		option := repositoryReviewModelOption{
			Alias: alias.Name, ResolvedModel: resolved, Provider: provider,
			Available: repositoryReviewAliasAvailableForRuntime(cfg, alias),
			Default:   alias.Name == defaultModel,
		}
		if repositoryReviewAliasUsesAgenticCLI(cfg, alias) {
			option.Available = false
			option.BlockedReason = "Agentic CLI models are not allowed for immutable repository review."
		} else if !option.Available {
			option.BlockedReason = "This alias is unavailable for every active review account."
		}
		if account, ok := repositoryReviewConservativePricedAccount(cfg, alias); ok {
			option.PriceKnown = account.InputPricePerMTok > 0 || account.OutputPricePerMTok > 0
			option.InputPricePer1M = account.InputPricePerMTok
			option.OutputPricePer1M = account.OutputPricePerMTok
			option.Subscription = account.Subscription
			option.EquivalentModel = account.SubscriptionEquivalentModel
			if option.Provider == "" {
				option.Provider = protocoltypes.NormalizeProvider(account.Provider)
			}
		}
		options = append(options, option)
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Default != options[j].Default {
			return options[i].Default
		}
		return options[i].Alias < options[j].Alias
	})
	return options
}

func repositoryReviewAliasAvailableForRuntime(
	cfg *config.Config,
	alias config.ModelAliasConfig,
) bool {
	if cfg == nil || strings.TrimSpace(alias.Name) == "" {
		return false
	}
	for _, accountRef := range repositoryReviewRuntimeAccountRefs(cfg) {
		resolved, err := cfg.ResolveModelAlias(alias.Name, accountRef)
		if err == nil && strings.TrimSpace(resolved) != "" {
			return true
		}
	}
	return false
}

func repositoryReviewAliasUsesAgenticCLI(
	cfg *config.Config,
	alias config.ModelAliasConfig,
) bool {
	models := make([]string, 0, len(alias.AccountOverrides)+1)
	models = append(models, alias.Model)
	relevantAccounts := make(map[string]struct{})
	for _, accountRef := range repositoryReviewRuntimeAccountRefs(cfg) {
		relevantAccounts[accountRef] = struct{}{}
		if override, exists := alias.AccountOverrides[accountRef]; exists {
			models = append(models, override)
		}
	}
	for _, model := range models {
		provider, _ := protocoltypes.SplitKnownProviderModel(strings.TrimSpace(model))
		if provider == "codex-cli" || provider == "claude-cli" {
			return true
		}
	}
	if cfg != nil {
		for accountRef := range relevantAccounts {
			account, err := cfg.GetEnabledModelConfig(accountRef)
			if err != nil || account == nil {
				continue
			}
			provider := protocoltypes.NormalizeProvider(account.Provider)
			if (provider == "codex-cli" || provider == "claude-cli") &&
				func() bool { _, resolveErr := cfg.ResolveModelAlias(alias.Name, accountRef); return resolveErr == nil }() {
				return true
			}
		}
	}
	return false
}

func repositoryReviewRuntimeAccountRefs(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	defaultRef := strings.TrimSpace(cfg.Agents.Defaults.AccountRef)
	refs := []string{defaultRef}
	var router *config.AccountRouterConfig
	for index := range cfg.AccountRouters {
		if cfg.AccountRouters[index].Enabled &&
			strings.TrimSpace(cfg.AccountRouters[index].Name) == defaultRef {
			router = &cfg.AccountRouters[index]
			break
		}
	}
	if router == nil {
		if account, err := cfg.GetEnabledModelConfig(defaultRef); err == nil &&
			account != nil && account.IsAccountRouter() {
			router = account.Router
		}
	}
	if router != nil {
		refs = refs[:0]
		for _, block := range router.Blocks {
			switch strings.TrimSpace(block.Type) {
			case config.AccountRouterBlockTypeAccount:
				refs = append(refs, strings.TrimSpace(block.Account))
			case config.AccountRouterBlockTypeLoadBalance:
				for _, accountRef := range block.Accounts {
					refs = append(refs, strings.TrimSpace(accountRef))
				}
			}
		}
	}
	seen := make(map[string]struct{}, len(refs))
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

func repositoryReviewConservativePricedAccount(
	cfg *config.Config,
	alias config.ModelAliasConfig,
) (*config.ModelConfig, bool) {
	return repositoryReviewAliasPrice(cfg, alias.Name, make(map[string]bool))
}

func repositoryReviewAliasPrice(
	cfg *config.Config,
	aliasName string,
	visiting map[string]bool,
) (*config.ModelConfig, bool) {
	if cfg == nil {
		return nil, false
	}
	aliasName = strings.TrimSpace(aliasName)
	if aliasName == "" || visiting[aliasName] {
		return nil, false
	}
	visiting[aliasName] = true
	defer delete(visiting, aliasName)
	aggregate := &config.ModelConfig{}
	found := false
	for _, accountRef := range repositoryReviewRuntimeAccountRefs(cfg) {
		account, err := cfg.GetEnabledModelConfig(accountRef)
		if err != nil || account == nil || account.IsAccountRouter() || account.IsModelRouter() {
			continue
		}
		resolved, err := cfg.ResolveModelAliasConfig(aliasName, accountRef)
		if err != nil {
			continue
		}
		inputPrice := resolved.InputPricePerMTok
		outputPrice := resolved.OutputPricePerMTok
		equivalent := strings.TrimSpace(resolved.SubscriptionEquivalentModel)
		if inputPrice <= 0 && outputPrice <= 0 && resolved.Subscription && equivalent != "" {
			if inherited, ok := repositoryReviewAliasPrice(cfg, equivalent, visiting); ok {
				inputPrice = inherited.InputPricePerMTok
				outputPrice = inherited.OutputPricePerMTok
			}
		}
		if inputPrice <= 0 && outputPrice <= 0 {
			continue
		}
		found = true
		aggregate.InputPricePerMTok = max(aggregate.InputPricePerMTok, inputPrice)
		aggregate.OutputPricePerMTok = max(aggregate.OutputPricePerMTok, outputPrice)
		aggregate.Subscription = aggregate.Subscription || resolved.Subscription
		if aggregate.SubscriptionEquivalentModel == "" && equivalent != "" {
			aggregate.SubscriptionEquivalentModel = equivalent
		}
		if aggregate.Provider == "" {
			aggregate.Provider = resolved.Provider
		}
	}
	return aggregate, found
}

func repositoryReviewAccountOptions(
	limits codexAccountLimitsResponse,
) []repositoryReviewAccountOption {
	accounts := make([]repositoryReviewAccountOption, 0, len(limits.Accounts))
	for _, account := range limits.Accounts {
		label := strings.TrimSpace(account.Email)
		if label == "" {
			label = strings.TrimSpace(account.AccountID)
		}
		if label == "" {
			label = account.ID
		}
		status := firstRepositoryReviewLimitDetail(
			account.LimitsStatus, account.CredentialStatus, account.LimitsError,
		)
		option := repositoryReviewAccountOption{
			ID: account.ID, Provider: account.Provider, Label: label, Status: status,
			Entries: make([]repositoryReviewAccountLimitOption, 0, len(account.Entries)),
		}
		for _, entry := range account.Entries {
			limit := repositoryReviewAccountLimitOption{
				Name: entry.Name, Status: entry.Status, Window: entry.Window,
				UsedPercent: entry.UsedPercent, RefreshesAt: entry.RefreshesAt,
			}
			if entry.UsedPercent != nil {
				remaining := math.Max(0, math.Min(100, 100-float64(*entry.UsedPercent)))
				limit.RemainingPercent = &remaining
			}
			option.Entries = append(option.Entries, limit)
		}
		accounts = append(accounts, option)
	}
	return accounts
}

func writeRepositoryReviewAutomationError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "repository_review_automation_unavailable"
	switch {
	case errors.Is(err, os.ErrNotExist) || strings.Contains(strings.ToLower(err.Error()), "not found"):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, repoaudit.ErrConflict), errors.Is(err, errRepositoryReviewAutomationBusy),
		errors.Is(err, errRepositoryReviewInvalidTransition),
		errors.Is(err, repoaudit.ErrAutomationControllerLocked):
		status, code = http.StatusConflict, "stale_repository_review_automation"
	case errors.Is(err, repoaudit.ErrInvalidAutomation),
		errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF),
		func() bool { var target *json.SyntaxError; return errors.As(err, &target) }(),
		func() bool { var target *json.UnmarshalTypeError; return errors.As(err, &target) }(),
		strings.Contains(strings.ToLower(err.Error()), "invalid"),
		strings.Contains(strings.ToLower(err.Error()), "required"),
		strings.Contains(strings.ToLower(err.Error()), "unknown field"),
		strings.Contains(strings.ToLower(err.Error()), "cannot unmarshal"),
		strings.Contains(strings.ToLower(err.Error()), "unexpected end"):
		status, code = http.StatusBadRequest, "invalid_repository_review_automation"
	}
	message := strings.TrimSpace(err.Error())
	if status >= 500 {
		message = strings.ReplaceAll(code, "_", " ")
	}
	writeRepositoryReviewJSON(w, status, map[string]string{"code": code, "message": message})
}
