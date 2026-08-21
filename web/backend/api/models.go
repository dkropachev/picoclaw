package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/audio/asr"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	cliprovider "github.com/sipeed/picoclaw/pkg/providers/cli"
	providercommon "github.com/sipeed/picoclaw/pkg/providers/common"
)

// registerModelRoutes binds account-owned model management endpoints to the ServeMux.
func (h *Handler) registerModelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/accounts/models", h.handleListModels)
	mux.HandleFunc("POST /api/accounts/models/fetch", h.handleFetchModels)
	mux.HandleFunc("GET /api/accounts/models/catalog", h.handleListCatalogs)
	mux.HandleFunc("DELETE /api/accounts/models/catalog/{id}", h.handleDeleteCatalog)
	mux.HandleFunc("POST /api/accounts/models", h.handleAddModel)
	mux.HandleFunc("POST /api/accounts/models/default", h.handleSetDefaultModel)
	mux.HandleFunc("POST /api/accounts/model-aliases", h.handleAddModelAlias)
	mux.HandleFunc("PUT /api/accounts/model-aliases/{index}", h.handleUpdateModelAlias)
	mux.HandleFunc("DELETE /api/accounts/model-aliases/{index}", h.handleDeleteModelAlias)
	mux.HandleFunc("PUT /api/accounts/models/{index}", h.handleUpdateModel)
	mux.HandleFunc("DELETE /api/accounts/models/{index}", h.handleDeleteModel)
	mux.HandleFunc("POST /api/accounts/models/{index}/test", h.handleTestModel)
	mux.HandleFunc("POST /api/accounts/models/test-inline", h.handleTestInlineModel)
}

// modelResponse is the JSON structure returned for each model in the list.
// All ModelConfig fields are included so the frontend can display and edit them.
type modelResponse struct {
	Index        int                         `json:"index"`
	ModelName    string                      `json:"model_name"`
	Provider     string                      `json:"provider,omitempty"`
	Model        string                      `json:"model"`
	APIBase      string                      `json:"api_base,omitempty"`
	APIKey       string                      `json:"api_key"`
	Proxy        string                      `json:"proxy,omitempty"`
	AuthMethod   string                      `json:"auth_method,omitempty"`
	CredentialID string                      `json:"credential_id,omitempty"`
	Router       *config.AccountRouterConfig `json:"router,omitempty"`
	ModelRouter  *config.ModelRouterConfig   `json:"model_router,omitempty"`
	// Advanced fields
	ConnectMode                 string                      `json:"connect_mode,omitempty"`
	Workspace                   string                      `json:"workspace,omitempty"`
	RPM                         int                         `json:"rpm,omitempty"`
	MaxTokensField              string                      `json:"max_tokens_field,omitempty"`
	RequestTimeout              int                         `json:"request_timeout,omitempty"`
	ThinkingLevel               string                      `json:"thinking_level,omitempty"`
	ReasoningEffort             string                      `json:"reasoning_effort,omitempty"`
	InputPricePerMTok           float64                     `json:"input_price_per_1m,omitempty"`
	OutputPricePerMTok          float64                     `json:"output_price_per_1m,omitempty"`
	Subscription                bool                        `json:"subscription,omitempty"`
	SubscriptionEquivalentModel string                      `json:"subscription_equivalent_model,omitempty"`
	ToolSchemaTransform         string                      `json:"tool_schema_transform,omitempty"`
	Streaming                   config.ModelStreamingConfig `json:"streaming,omitempty"`
	ExtraBody                   map[string]any              `json:"extra_body,omitempty"`
	CustomHeaders               map[string]string           `json:"custom_headers,omitempty"`
	// Meta
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
	Status    string `json:"status"`
	IsDefault bool   `json:"is_default"`
	IsVirtual bool   `json:"is_virtual"`
}

func normalizeStoredModelConfig(mc *config.ModelConfig) bool {
	if mc == nil {
		return false
	}

	changed := false
	model := strings.TrimSpace(mc.Model)
	if model != mc.Model {
		mc.Model = model
		changed = true
	}
	provider := strings.TrimSpace(mc.Provider)
	if provider != mc.Provider {
		mc.Provider = provider
		changed = true
	}
	authMethod := strings.ToLower(strings.TrimSpace(mc.AuthMethod))
	if authMethod != mc.AuthMethod {
		mc.AuthMethod = authMethod
		changed = true
	}
	credentialID := strings.TrimSpace(mc.CredentialID)
	if credentialID != mc.CredentialID {
		mc.CredentialID = credentialID
		changed = true
	}
	if effort, err := providercommon.NormalizeReasoningEffort(mc.ReasoningEffort); err == nil {
		if effort != mc.ReasoningEffort {
			mc.ReasoningEffort = effort
			changed = true
		}
	}

	if provider != "" {
		normalizedProvider := providers.NormalizeProvider(provider)
		if providers.IsSupportedModelProvider(normalizedProvider) &&
			normalizedProvider != provider {
			mc.Provider = normalizedProvider
			changed = true
		}
		if mc.Provider == "elevenlabs" {
			if _, strippedModel, found := strings.Cut(
				model,
				"/",
			); found &&
				providers.NormalizeProvider(strings.TrimSpace(provider)) == "elevenlabs" {
				strippedModel = strings.TrimSpace(strippedModel)
				if strippedModel != "" && strippedModel != mc.Model {
					mc.Model = strippedModel
					changed = true
				}
			}
		}
		return changed
	}

	effectiveProvider, modelID := providers.SplitModelProviderAndID(model, "openai")
	if effectiveProvider == "" {
		return changed
	}
	if mc.Provider != effectiveProvider {
		mc.Provider = effectiveProvider
		changed = true
	}
	if mc.Model != modelID {
		mc.Model = modelID
		changed = true
	}
	return changed
}

func normalizeIncomingModelConfig(mc *config.ModelConfig) {
	if mc == nil {
		return
	}

	mc.Model = strings.TrimSpace(mc.Model)
	mc.Provider = strings.TrimSpace(mc.Provider)
	mc.AuthMethod = strings.ToLower(strings.TrimSpace(mc.AuthMethod))
	mc.CredentialID = strings.TrimSpace(mc.CredentialID)
	if mc.ModelRouter != nil ||
		strings.EqualFold(strings.TrimSpace(mc.Provider), config.ModelRouterProvider) {
		mc.Provider = config.ModelRouterProvider
		if strings.TrimSpace(mc.Model) == "" {
			mc.Model = strings.TrimSpace(mc.ModelName)
		}
		mc.APIKeys = nil
		mc.APIBase = ""
		mc.Proxy = ""
		mc.AuthMethod = ""
		mc.CredentialID = ""
		mc.ConnectMode = ""
		mc.Workspace = ""
	}
	if mc.Router != nil ||
		providers.NormalizeProvider(mc.Provider) == config.AccountRouterProvider {
		mc.Provider = config.AccountRouterProvider
		mc.Model = ""
		mc.APIKeys = nil
		mc.APIBase = ""
		mc.Proxy = ""
		mc.AuthMethod = ""
		mc.CredentialID = ""
		mc.ConnectMode = ""
		mc.Workspace = ""
	}
	if effort, err := providercommon.NormalizeReasoningEffort(mc.ReasoningEffort); err == nil {
		mc.ReasoningEffort = effort
	}
	if mc.Provider == config.AccountRouterProvider || mc.Provider == config.ModelRouterProvider {
		return
	}
	if mc.Provider == "" {
		mc.Provider, mc.Model = providers.SplitModelProviderAndID(mc.Model, "openai")
	} else {
		mc.Provider = providers.NormalizeProvider(mc.Provider)
		if mc.Provider == "elevenlabs" {
			if _, strippedModel, found := strings.Cut(mc.Model, "/"); found {
				strippedModel = strings.TrimSpace(strippedModel)
				if strippedModel != "" {
					mc.Model = strippedModel
				}
			}
		}
	}
	if mc.Provider == "antigravity" && mc.AuthMethod == "" {
		mc.AuthMethod = "oauth"
	}
	if mc.CredentialID != "" {
		if credentialID, err := auth.NormalizeCredentialID(mc.Provider, mc.CredentialID); err == nil {
			mc.CredentialID = credentialID
		}
	}
}

func createAllowedForProvider(provider string) bool {
	normalized := providers.NormalizeProvider(provider)
	switch normalized {
	case "bedrock":
		// Bedrock currently authenticates through the AWS SDK credential chain
		// (env vars, shared profiles, IAM roles, etc.), and this Web layer does
		// not yet have a reliable preflight check for those credential sources.
		// Keep it creatable in the catalog and let provider construction/runtime
		// return the concrete AWS error when the environment is incomplete.
		return true
	case "claude-cli", "codex-cli":
		return cliProviderCreateAllowedFromCurrentStatus(normalized)
	default:
		return providers.IsCreatableModelProvider(normalized)
	}
}

// cliProviderCreateAllowedFromCurrentStatus intentionally reuses the existing
// local model status pipeline so provider catalog gating follows the same CLI
// executable probe used by launcher readiness.
func cliProviderCreateAllowedFromCurrentStatus(provider string) bool {
	status := modelConfigurationStatus(&config.ModelConfig{
		Provider: provider,
		Model:    provider,
	})
	return status.Available
}

func modelProviderOptionsForResponse() []providers.ModelProviderOption {
	options := providers.ModelProviderOptions()
	for i := range options {
		options[i].CreateAllowed = createAllowedForProvider(options[i].ID)
	}
	return options
}

func modelRouterFromModelConfig(mc *config.ModelConfig) (*config.ModelRouterConfig, error) {
	if mc == nil || mc.ModelRouter == nil {
		return nil, fmt.Errorf("model_router config is required")
	}
	router := *mc.ModelRouter
	router.Blocks = append([]config.ModelRouterBlock(nil), mc.ModelRouter.Blocks...)
	for i := range router.Blocks {
		router.Blocks[i].Rules = append(
			[]config.ModelRouterRule(nil),
			mc.ModelRouter.Blocks[i].Rules...)
	}
	router.Name = strings.TrimSpace(mc.ModelName)
	if router.Name == "" {
		return nil, fmt.Errorf("model_name is required")
	}
	if !router.Enabled {
		router.Enabled = mc.Enabled
	}
	if err := router.Validate(); err != nil {
		return nil, err
	}
	return &router, nil
}

func accountRouterFromModelConfig(mc *config.ModelConfig) (*config.AccountRouterConfig, error) {
	if mc == nil || mc.Router == nil {
		return nil, fmt.Errorf("router config is required")
	}
	router := *mc.Router
	router.Blocks = append([]config.AccountRouterBlock(nil), mc.Router.Blocks...)
	for i := range router.Blocks {
		router.Blocks[i].Accounts = append([]string(nil), mc.Router.Blocks[i].Accounts...)
	}
	router.Name = strings.TrimSpace(mc.ModelName)
	if router.Name == "" {
		return nil, fmt.Errorf("model_name is required")
	}
	if !router.Enabled {
		router.Enabled = mc.Enabled
	}
	if err := router.Validate(); err != nil {
		return nil, err
	}
	return &router, nil
}

func findAccountRouterIndex(cfg *config.Config, name string) int {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return -1
	}
	for i := range cfg.AccountRouters {
		if strings.TrimSpace(cfg.AccountRouters[i].Name) == name {
			return i
		}
	}
	return -1
}

func findModelRouterIndex(cfg *config.Config, name string) int {
	name = strings.TrimSpace(name)
	if cfg == nil || name == "" {
		return -1
	}
	for i := range cfg.ModelRouters {
		if strings.TrimSpace(cfg.ModelRouters[i].Name) == name {
			return i
		}
	}
	return -1
}

func validateIncomingModelConfig(mc *config.ModelConfig, existing *config.ModelConfig) error {
	if mc == nil {
		return fmt.Errorf("model config is required")
	}
	if mc.IsModelRouter() {
		_, err := modelRouterFromModelConfig(mc)
		return err
	}
	if mc.IsAccountRouter() {
		_, err := accountRouterFromModelConfig(mc)
		return err
	}
	if err := mc.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(mc.Provider) == "" {
		return fmt.Errorf("provider is required")
	}
	if !providers.IsSupportedModelProvider(mc.Provider) {
		return fmt.Errorf("provider %q is not supported", mc.Provider)
	}
	if strings.TrimSpace(mc.CredentialID) != "" {
		if _, err := auth.NormalizeCredentialID(mc.Provider, mc.CredentialID); err != nil {
			return err
		}
	}
	if mc.Provider == "elevenlabs" &&
		strings.TrimSpace(mc.Model) != asr.ElevenLabsSupportedModelID() {
		return fmt.Errorf(
			"provider %q only supports model %q",
			mc.Provider,
			asr.ElevenLabsSupportedModelID(),
		)
	}
	if !createAllowedForProvider(mc.Provider) {
		if existing == nil {
			return fmt.Errorf("provider %q is not available for new models", mc.Provider)
		}
		existingProvider, _ := providers.ExtractProtocol(existing)
		if providers.NormalizeProvider(existingProvider) != mc.Provider {
			return fmt.Errorf("provider %q is not available for selection", mc.Provider)
		}
	}
	return nil
}

func normalizeStoredModelProviders(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}

	changed := false
	for _, model := range cfg.ModelList {
		if normalizeStoredModelConfig(model) {
			changed = true
		}
	}
	return changed
}

// handleListModels returns all model_list entries with masked API keys.
//
//	GET /api/accounts/models
func (h *Handler) handleListModels(w http.ResponseWriter, r *http.Request) {
	cfg, revision, err := config.LoadConfigSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	// Normalize legacy provider/model storage in memory so GET can round-trip
	// through the current API shape without mutating the on-disk config.
	normalizeStoredModelProviders(cfg)

	defaultModel := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	defaultAccountRef := strings.TrimSpace(cfg.Agents.Defaults.AccountRef)
	modelStatuses := make([]modelConfigurationSummary, len(cfg.ModelList))

	var wg sync.WaitGroup
	wg.Add(len(cfg.ModelList))
	for i, m := range cfg.ModelList {
		go func(i int, m *config.ModelConfig) {
			defer wg.Done()
			modelStatuses[i] = modelConfigurationStatus(m)
		}(i, m)
	}
	wg.Wait()

	models := make([]modelResponse, 0, len(cfg.ModelList))
	for i, m := range cfg.ModelList {
		provider, modelID := providers.ExtractProtocol(m)
		models = append(models, modelResponse{
			Index:                       i,
			ModelName:                   m.ModelName,
			Provider:                    provider,
			Model:                       modelID,
			APIBase:                     m.APIBase,
			APIKey:                      maskAPIKey(m.APIKey()),
			Proxy:                       m.Proxy,
			AuthMethod:                  m.AuthMethod,
			CredentialID:                m.CredentialID,
			Router:                      m.Router,
			ModelRouter:                 m.ModelRouter,
			ConnectMode:                 m.ConnectMode,
			Workspace:                   m.Workspace,
			RPM:                         m.RPM,
			MaxTokensField:              m.MaxTokensField,
			RequestTimeout:              m.RequestTimeout,
			ThinkingLevel:               m.ThinkingLevel,
			ReasoningEffort:             m.ReasoningEffort,
			InputPricePerMTok:           m.InputPricePerMTok,
			OutputPricePerMTok:          m.OutputPricePerMTok,
			Subscription:                m.Subscription,
			SubscriptionEquivalentModel: m.SubscriptionEquivalentModel,
			ToolSchemaTransform:         m.ToolSchemaTransform,
			Streaming:                   m.Streaming,
			ExtraBody:                   m.ExtraBody,
			CustomHeaders:               m.CustomHeaders,
			Enabled:                     m.Enabled,
			Available:                   modelStatuses[i].Available,
			Status:                      modelStatuses[i].Status,
			IsDefault:                   m.ModelName == defaultAccountRef,
			IsVirtual:                   m.IsVirtual(),
		})
	}
	models = appendCredentialAccountModelResponses(models, defaultAccountRef)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"models":              models,
		"model_aliases":       cfg.ModelAliases,
		"model_alias_catalog": config.DeveloperModelAliasCatalog(),
		"total":               len(models),
		"default_account_ref": defaultAccountRef,
		"default_model":       defaultModel,
		"revision":            revision,
		"provider_options":    modelProviderOptionsForResponse(),
	})
}

func appendCredentialAccountModelResponses(
	models []modelResponse,
	defaultAccountRef string,
) []modelResponse {
	store, err := oauthLoadStore()
	if err != nil || store == nil {
		return models
	}

	representedCredentials := make(map[string]bool, len(models))
	for _, model := range models {
		if !model.Enabled {
			continue
		}
		if credentialID := representedModelCredentialID(model); credentialID != "" {
			representedCredentials[credentialID] = true
		}
	}

	credentialIDs := make([]string, 0, len(store.Credentials))
	for credentialID, cred := range store.Credentials {
		if cred == nil {
			continue
		}
		credentialID = strings.ToLower(strings.TrimSpace(credentialID))
		if representedCredentials[credentialID] {
			continue
		}
		provider := credentialIDProvider(credentialID)
		if provider == "" ||
			!credentialProviderMatches(cred, provider) {
			continue
		}
		credentialIDs = append(credentialIDs, credentialID)
	}
	sort.Strings(credentialIDs)

	nextIndex := len(models)
	for _, credentialID := range credentialIDs {
		cred := store.Credentials[credentialID]
		provider := credentialIDProvider(credentialID)
		modelName := config.AccountRouterCredentialAccountPrefix + strings.ToLower(
			strings.TrimSpace(credentialID),
		)
		authMethod := strings.ToLower(strings.TrimSpace(cred.AuthMethod))
		if authMethod == "" {
			authMethod = defaultCredentialAccountAuthMethod(provider)
		}
		available := strings.TrimSpace(cred.AccessToken) != "" && !cred.IsExpired()
		status := "available"
		if !available {
			status = "unconfigured"
		}
		models = append(models, modelResponse{
			Index:        nextIndex,
			ModelName:    modelName,
			Provider:     provider,
			Model:        "",
			APIKey:       "",
			AuthMethod:   authMethod,
			CredentialID: credentialID,
			Enabled:      true,
			Available:    available,
			Status:       status,
			IsDefault:    modelName == defaultAccountRef,
			IsVirtual:    true,
		})
		nextIndex++
	}
	return models
}

func credentialIDProvider(credentialID string) string {
	provider, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(credentialID)), ":")
	return providers.NormalizeProvider(provider)
}

func representedModelCredentialID(model modelResponse) string {
	provider := providers.NormalizeProvider(model.Provider)
	if provider == "" {
		return ""
	}
	authMethod := strings.ToLower(strings.TrimSpace(model.AuthMethod))
	if authMethod == "" && provider == "antigravity" {
		authMethod = "oauth"
	}
	if !isCredentialAuthMethod(authMethod) {
		return ""
	}
	credentialID, err := auth.NormalizeCredentialID(
		authProviderForCredentialModel(provider),
		model.CredentialID,
	)
	if err != nil {
		return ""
	}
	return credentialID
}

func authProviderForCredentialModel(provider string) string {
	switch providers.NormalizeProvider(provider) {
	case "antigravity":
		return oauthProviderGoogleAntigravity
	default:
		return providers.NormalizeProvider(provider)
	}
}

func defaultCredentialAccountAuthMethod(provider string) string {
	switch providers.NormalizeProvider(provider) {
	case "openai", "antigravity":
		return "oauth"
	default:
		return "token"
	}
}

// handleAddModel appends a new model configuration entry.
//
//	POST /api/accounts/models
func (h *Handler) handleAddModel(w http.ResponseWriter, r *http.Request) {
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	type custom struct {
		config.ModelConfig
		APIKey       string `json:"api_key"`
		SetAsDefault bool   `json:"set_as_default"`
	}

	var mc custom
	if err = json.Unmarshal(body, &mc); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if mc.Router == nil &&
		mc.ModelRouter == nil &&
		strings.TrimSpace(mc.Provider) == "" {
		http.Error(w, "Validation error: provider is required", http.StatusBadRequest)
		return
	}

	normalizeIncomingModelConfig(&mc.ModelConfig)

	if err = validateIncomingModelConfig(&mc.ModelConfig, nil); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}

	if mc.APIKey != "" {
		mc.ModelConfig.SetAPIKey(mc.APIKey)
	}

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	if mc.ModelConfig.IsAccountRouter() {
		router, err := accountRouterFromModelConfig(&mc.ModelConfig)
		if err != nil {
			http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
			return
		}
		if findAccountRouterIndex(cfg, router.Name) >= 0 {
			http.Error(
				w,
				fmt.Sprintf("Account router %q already exists", router.Name),
				http.StatusBadRequest,
			)
			return
		}
		cfg.AccountRouters = append(cfg.AccountRouters, *router)
		h.saveAddedRouterModel(
			w,
			cfg,
			revision,
			&mc.ModelConfig,
			mc.SetAsDefault,
		)
		return
	}
	if mc.ModelConfig.IsModelRouter() {
		router, err := modelRouterFromModelConfig(&mc.ModelConfig)
		if err != nil {
			http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
			return
		}
		if findModelRouterIndex(cfg, router.Name) >= 0 {
			http.Error(
				w,
				fmt.Sprintf("Model router %q already exists", router.Name),
				http.StatusBadRequest,
			)
			return
		}
		cfg.ModelRouters = append(cfg.ModelRouters, *router)
		h.saveAddedRouterModel(
			w,
			cfg,
			revision,
			&mc.ModelConfig,
			mc.SetAsDefault,
		)
		return
	}

	cfg.ModelList = append(cfg.ModelList, &mc.ModelConfig)
	normalizeStoredModelProviders(cfg)
	if err := applyDefaultForModelMutation(
		cfg,
		&mc.ModelConfig,
		mc.SetAsDefault,
	); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	if err := validateAPIModelConfiguration(cfg); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}

	if !saveModelConfigMutation(w, h.configPath, cfg, revision) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"index":  len(cfg.ModelList) - 1,
	})
}

func (h *Handler) saveAddedRouterModel(
	w http.ResponseWriter,
	cfg *config.Config,
	revision string,
	model *config.ModelConfig,
	setAsDefault bool,
) {
	cfg.MaterializeAccountRouterModels()
	cfg.MaterializeModelRouterModels()
	if err := applyDefaultForModelMutation(cfg, model, setAsDefault); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	if err := validateAPIModelConfiguration(cfg); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	if !saveModelConfigMutation(w, h.configPath, cfg, revision) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"index":  len(cfg.ModelList) - 1,
	})
}

// handleUpdateModel replaces a model configuration entry at the given index.
// If the request body omits api_key (or sends an empty string), the existing
// stored key is preserved so callers can update only api_base / proxy without
// exposing or clearing the secret.
//
//	PUT /api/accounts/models/{index}
func (h *Handler) handleUpdateModel(w http.ResponseWriter, r *http.Request) {
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var rawFields map[string]json.RawMessage
	if err = json.Unmarshal(body, &rawFields); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	type custom struct {
		config.ModelConfig
		APIKey       string `json:"api_key"`
		SetAsDefault bool   `json:"set_as_default"`
	}

	var mc custom
	if err = json.Unmarshal(body, &mc); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	if idx < 0 || idx >= len(cfg.ModelList) {
		http.Error(
			w,
			fmt.Sprintf("Index %d out of range (0-%d)", idx, len(cfg.ModelList)-1),
			http.StatusNotFound,
		)
		return
	}
	if !requireModelListRevision(w, r, revision) {
		return
	}
	existing := cfg.ModelList[idx]
	if strings.TrimSpace(mc.ModelName) != strings.TrimSpace(existing.ModelName) {
		http.Error(
			w,
			"account and router names are immutable; create a replacement before deleting the old entry",
			http.StatusBadRequest,
		)
		return
	}
	incomingIsRouter := mc.ModelConfig.IsAccountRouter()
	existingIsRouter := existing.IsAccountRouter()
	incomingIsModelRouter := mc.ModelConfig.IsModelRouter()
	existingIsModelRouter := existing.IsModelRouter()
	if incomingIsRouter || existingIsRouter || incomingIsModelRouter || existingIsModelRouter {
		if incomingIsRouter != existingIsRouter || incomingIsModelRouter != existingIsModelRouter {
			msg := "Cannot change a model, account router, or model router into another entry type"
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if incomingIsModelRouter {
			router, routerErr := modelRouterFromModelConfig(&mc.ModelConfig)
			if routerErr != nil {
				http.Error(w, fmt.Sprintf("Validation error: %v", routerErr), http.StatusBadRequest)
				return
			}
			routerIndex := findModelRouterIndex(cfg, existing.ModelName)
			if routerIndex < 0 {
				http.Error(
					w,
					fmt.Sprintf("Model router %q not found", existing.ModelName),
					http.StatusNotFound,
				)
				return
			}
			if duplicateIndex := findModelRouterIndex(cfg, router.Name); duplicateIndex >= 0 &&
				duplicateIndex != routerIndex {
				http.Error(
					w,
					fmt.Sprintf("Model router %q already exists", router.Name),
					http.StatusBadRequest,
				)
				return
			}
			cfg.ModelRouters[routerIndex] = *router
			cfg.MaterializeAccountRouterModels()
			cfg.MaterializeModelRouterModels()
			if defaultErr := applyDefaultForModelMutation(
				cfg,
				&mc.ModelConfig,
				mc.SetAsDefault,
			); defaultErr != nil {
				http.Error(
					w,
					fmt.Sprintf("Validation error: %v", defaultErr),
					http.StatusBadRequest,
				)
				return
			}
			if validateErr := validateAPIModelConfiguration(cfg); validateErr != nil {
				http.Error(
					w,
					fmt.Sprintf("Validation error: %v", validateErr),
					http.StatusBadRequest,
				)
				return
			}
			if !saveModelConfigMutation(w, h.configPath, cfg, revision) {
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
		router, routerErr := accountRouterFromModelConfig(&mc.ModelConfig)
		if routerErr != nil {
			http.Error(w, fmt.Sprintf("Validation error: %v", routerErr), http.StatusBadRequest)
			return
		}
		routerIndex := findAccountRouterIndex(cfg, existing.ModelName)
		if routerIndex < 0 {
			http.Error(
				w,
				fmt.Sprintf("Account router %q not found", existing.ModelName),
				http.StatusNotFound,
			)
			return
		}
		if duplicateIndex := findAccountRouterIndex(cfg, router.Name); duplicateIndex >= 0 &&
			duplicateIndex != routerIndex {
			http.Error(
				w,
				fmt.Sprintf("Account router %q already exists", router.Name),
				http.StatusBadRequest,
			)
			return
		}
		cfg.AccountRouters[routerIndex] = *router
		cfg.MaterializeAccountRouterModels()
		cfg.MaterializeModelRouterModels()
		if defaultErr := applyDefaultForModelMutation(
			cfg,
			&mc.ModelConfig,
			mc.SetAsDefault,
		); defaultErr != nil {
			http.Error(
				w,
				fmt.Sprintf("Validation error: %v", defaultErr),
				http.StatusBadRequest,
			)
			return
		}
		if validateErr := validateAPIModelConfiguration(cfg); validateErr != nil {
			http.Error(w, fmt.Sprintf("Validation error: %v", validateErr), http.StatusBadRequest)
			return
		}
		if !saveModelConfigMutation(w, h.configPath, cfg, revision) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		return
	}

	// Preserve the existing API key when the caller omits it (empty string).
	// This lets the UI update api_base / proxy without clearing the stored secret.
	if mc.APIKey == "" {
		if existingKey := cfg.ModelList[idx].APIKey(); existingKey != "" {
			mc.ModelConfig.SetAPIKey(existingKey)
		}
	} else {
		mc.ModelConfig.SetAPIKey(mc.APIKey)
	}
	// Preserve existing ExtraBody when omitted (nil), but clear it when
	// the frontend sends an empty object {} to indicate the field should
	// be removed.
	if mc.ExtraBody == nil {
		mc.ExtraBody = cfg.ModelList[idx].ExtraBody
	} else if len(mc.ExtraBody) == 0 {
		mc.ExtraBody = nil
	}
	// Preserve existing CustomHeaders when omitted (nil), but clear it when
	// the frontend sends an empty object {} to indicate the field should
	// be removed.
	if mc.CustomHeaders == nil {
		mc.CustomHeaders = cfg.ModelList[idx].CustomHeaders
	} else if len(mc.CustomHeaders) == 0 {
		mc.CustomHeaders = nil
	}
	if _, ok := rawFields["tool_schema_transform"]; !ok {
		mc.ToolSchemaTransform = cfg.ModelList[idx].ToolSchemaTransform
	}
	if _, ok := rawFields["streaming"]; !ok {
		mc.Streaming = cfg.ModelList[idx].Streaming
	}
	if _, ok := rawFields["credential_id"]; !ok {
		mc.CredentialID = cfg.ModelList[idx].CredentialID
	}
	if _, ok := rawFields["enabled"]; !ok {
		mc.Enabled = cfg.ModelList[idx].Enabled
	}
	// Preserve the existing Provider when the caller omits it. This keeps the
	// update API backward-compatible for clients that haven't started sending
	// the new field yet, while still allowing explicit clearing via "".
	if _, ok := rawFields["provider"]; !ok {
		mc.Provider = cfg.ModelList[idx].Provider
		// Older clients still round-trip the legacy model field only. When the
		// stored config encodes provider/model in Model and has no explicit
		// Provider field yet, continue preserving that hidden provider prefix.
		// This keeps provider-omitted updates backward-compatible even when an
		// older client edits the visible model ID.
		if strings.TrimSpace(cfg.ModelList[idx].Provider) == "" {
			existingRawModel := strings.TrimSpace(cfg.ModelList[idx].Model)
			incomingModel := strings.TrimSpace(mc.Model)
			existingProtocol, existingModelID := providers.ExtractProtocol(cfg.ModelList[idx])
			if existingRawModel != "" && existingRawModel != existingModelID &&
				incomingModel != "" {
				if incomingModel == existingModelID {
					mc.Model = existingRawModel
				} else if strings.Contains(incomingModel, "/") && !strings.Contains(existingModelID, "/") {
					// Older clients never saw the hidden provider prefix for simple
					// legacy entries such as "openai/gpt-4o". If they now send an
					// explicit provider/model string, treat it as the caller's full
					// intent instead of re-applying the old hidden prefix.
					mc.Model = incomingModel
				} else if !strings.HasPrefix(incomingModel, existingProtocol+"/") {
					mc.Model = existingProtocol + "/" + incomingModel
				}
			}
		}
	}

	normalizeIncomingModelConfig(&mc.ModelConfig)
	if err = validateIncomingModelConfig(&mc.ModelConfig, cfg.ModelList[idx]); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	cfg.ModelList[idx] = &mc.ModelConfig
	normalizeStoredModelProviders(cfg)
	if err := applyDefaultForModelMutation(
		cfg,
		&mc.ModelConfig,
		mc.SetAsDefault,
	); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	if err := validateAPIModelConfiguration(cfg); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}

	logger.Debugf("update model config: %#v", mc.ModelConfig)

	if !saveModelConfigMutation(w, h.configPath, cfg, revision) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func applyDefaultForModelMutation(
	cfg *config.Config,
	model *config.ModelConfig,
	setAsDefault bool,
) error {
	if !setAsDefault {
		return nil
	}
	if cfg == nil {
		return fmt.Errorf("model configuration is required")
	}
	if model == nil {
		return fmt.Errorf("model is required")
	}

	name := strings.TrimSpace(model.ModelName)
	if name == "" {
		return fmt.Errorf("model_name is required")
	}
	if model.IsModelRouter() {
		if strings.TrimSpace(cfg.Agents.Defaults.AccountRef) == "" {
			return fmt.Errorf("no account configured")
		}
		cfg.Agents.Defaults.ModelName = name
		return nil
	}
	if strings.TrimSpace(cfg.Agents.Defaults.ModelName) == "" {
		return config.ErrNoModelConfigured
	}
	cfg.Agents.Defaults.AccountRef = name
	return nil
}

func requireModelListRevision(
	w http.ResponseWriter,
	r *http.Request,
	currentRevision string,
) bool {
	expectedRevision := strings.TrimSpace(r.URL.Query().Get("revision"))
	if expectedRevision == "" {
		http.Error(w, "config revision is required; reload and try again", http.StatusPreconditionRequired)
		return false
	}
	if expectedRevision != currentRevision {
		http.Error(w, "Configuration changed; reload and try again", http.StatusConflict)
		return false
	}
	return true
}

func (h *Handler) handleDeleteVirtualModel(
	w http.ResponseWriter,
	cfg *config.Config,
	revision string,
	deletedModelName string,
	accountRouter bool,
) {
	referenceKind := "model router"
	displayKind := "Model router"
	references := modelAliasReferences(cfg, deletedModelName)
	routerIndex := findModelRouterIndex(cfg, deletedModelName)
	if accountRouter {
		referenceKind = "account router"
		displayKind = "Account router"
		references = modelAccountReferences(cfg, deletedModelName)
		routerIndex = findAccountRouterIndex(cfg, deletedModelName)
	}

	if len(references) > 0 {
		http.Error(
			w,
			fmt.Sprintf(
				"%s %q is still referenced by %s",
				referenceKind,
				deletedModelName,
				strings.Join(references, ", "),
			),
			http.StatusConflict,
		)
		return
	}
	if routerIndex < 0 {
		http.Error(
			w,
			fmt.Sprintf("%s %q not found", displayKind, deletedModelName),
			http.StatusNotFound,
		)
		return
	}

	if accountRouter {
		cfg.AccountRouters = append(
			cfg.AccountRouters[:routerIndex],
			cfg.AccountRouters[routerIndex+1:]...)
	} else {
		cfg.ModelRouters = append(
			cfg.ModelRouters[:routerIndex],
			cfg.ModelRouters[routerIndex+1:]...)
	}
	cfg.MaterializeAccountRouterModels()
	cfg.MaterializeModelRouterModels()
	if err := validateAPIModelConfiguration(cfg); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}
	if !saveModelConfigMutation(w, h.configPath, cfg, revision) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleDeleteModel removes a model configuration entry at the given index.
//
//	DELETE /api/accounts/models/{index}
func (h *Handler) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	if idx < 0 || idx >= len(cfg.ModelList) {
		http.Error(
			w,
			fmt.Sprintf("Index %d out of range (0-%d)", idx, len(cfg.ModelList)-1),
			http.StatusNotFound,
		)
		return
	}
	if !requireModelListRevision(w, r, revision) {
		return
	}

	deletedModelName := cfg.ModelList[idx].ModelName

	if cfg.ModelList[idx].IsAccountRouter() {
		h.handleDeleteVirtualModel(w, cfg, revision, deletedModelName, true)
		return
	}
	if cfg.ModelList[idx].IsModelRouter() {
		h.handleDeleteVirtualModel(w, cfg, revision, deletedModelName, false)
		return
	}

	accountWillRemain := false
	for candidateIndex, candidate := range cfg.ModelList {
		if candidateIndex == idx || candidate == nil || candidate.IsVirtual() ||
			candidate.IsAccountRouter() || candidate.IsModelRouter() {
			continue
		}
		if strings.TrimSpace(candidate.ModelName) == strings.TrimSpace(deletedModelName) {
			accountWillRemain = true
			break
		}
	}
	if !accountWillRemain {
		if references := modelAccountReferences(cfg, deletedModelName); len(references) > 0 {
			http.Error(
				w,
				fmt.Sprintf(
					"account %q is still referenced by %s",
					deletedModelName,
					strings.Join(references, ", "),
				),
				http.StatusConflict,
			)
			return
		}
	}
	cfg.ModelList = append(cfg.ModelList[:idx], cfg.ModelList[idx+1:]...)

	if err := validateAPIModelConfiguration(cfg); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}

	if !saveModelConfigMutation(w, h.configPath, cfg, revision) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleSetDefaultModel atomically sets the default account and model alias.
//
//	POST /api/accounts/models/default
func (h *Handler) handleSetDefaultModel(w http.ResponseWriter, r *http.Request) {
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req struct {
		AccountRef string `json:"account_ref"`
		ModelName  string `json:"model_name"`
	}
	if err = json.Unmarshal(body, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	req.AccountRef = strings.TrimSpace(req.AccountRef)
	req.ModelName = strings.TrimSpace(req.ModelName)
	if req.AccountRef == "" {
		http.Error(w, "account_ref is required", http.StatusBadRequest)
		return
	}
	if req.ModelName == "" {
		http.Error(w, config.ErrNoModelConfigured.Error(), http.StatusBadRequest)
		return
	}

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	if err := validateSelectableAccountRef(cfg, req.AccountRef); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !modelAliasOrRouterConfigured(cfg, req.ModelName) {
		http.Error(
			w,
			fmt.Sprintf("model alias %q is not configured", req.ModelName),
			http.StatusNotFound,
		)
		return
	}
	if err := validateSelectionGraph(cfg, req.AccountRef, req.ModelName, true); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cfg.Agents.Defaults.AccountRef = req.AccountRef
	cfg.Agents.Defaults.ModelName = req.ModelName
	if err := validateAPIModelConfiguration(cfg); err != nil {
		http.Error(w, fmt.Sprintf("Validation error: %v", err), http.StatusBadRequest)
		return
	}

	if !saveModelConfigMutation(w, h.configPath, cfg, revision) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":              "ok",
		"default_account_ref": req.AccountRef,
		"default_model":       req.ModelName,
	})
}

func credentialAccountAvailable(accountRef string) bool {
	credentialID, ok := config.AccountRouterCredentialAccountID(accountRef)
	if !ok {
		return false
	}
	provider, ok := config.AccountRouterCredentialAccountProvider(accountRef)
	if !ok {
		return false
	}
	credentialID, err := auth.NormalizeCredentialID(
		authProviderForCredentialModel(provider),
		credentialID,
	)
	if err != nil {
		return false
	}
	store, err := oauthLoadStore()
	if err != nil || store == nil {
		return false
	}
	cred := store.Credentials[credentialID]
	return cred != nil &&
		credentialProviderMatches(cred, provider) &&
		strings.TrimSpace(cred.AccessToken) != "" &&
		!cred.IsExpired()
}

func credentialProviderMatches(credential *auth.AuthCredential, provider string) bool {
	if credential == nil {
		return false
	}
	expected, err := auth.NormalizeCredentialID(authProviderForCredentialModel(provider), "")
	if err != nil {
		return false
	}
	actual, err := auth.NormalizeCredentialID(credential.Provider, "")
	return err == nil && actual == expected
}

// maskAPIKey returns a masked version of an API key for safe display.
// Keys longer than 12 chars show prefix + last 4 chars: "sk-****abcd".
// Keys 9-12 chars show prefix + last 2 chars: "sk-****cd".
// Shorter keys are fully masked as "****".
// Empty keys return empty string.
// Ensure at least 40% of the key will not be displayed.
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}

	if len(key) <= 8 {
		return "****"
	}

	// Show first 3 chars and last 2 chars
	if len(key) <= 12 {
		return key[:3] + "****" + key[len(key)-2:]
	}

	// Show first 3 chars and last 4 chars
	return key[:3] + "****" + key[len(key)-4:]
}

// handleFetchModels fetches available models from an account reference or an
// upstream provider.
//
//	POST /api/accounts/models/fetch
func (h *Handler) handleFetchModels(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req struct {
		AccountRef   string `json:"account_ref,omitempty"`
		Provider     string `json:"provider"`
		APIKey       string `json:"api_key"`
		APIBase      string `json:"api_base"`
		AuthMethod   string `json:"auth_method,omitempty"`
		CredentialID string `json:"credential_id,omitempty"`
		ModelIndex   *int   `json:"model_index,omitempty"`
	}
	if err = json.Unmarshal(body, &req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	accountRef := strings.TrimSpace(req.AccountRef)
	if accountRef != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		models, issues, fetchErr := h.fetchModelsForAccountRef(ctx, accountRef)
		if fetchErr != nil {
			http.Error(
				w,
				fmt.Sprintf("Failed to fetch models: %v", fetchErr),
				http.StatusBadGateway,
			)
			return
		}
		writeFetchModelsResponse(w, models, issues)
		return
	}

	if req.Provider == "" {
		http.Error(w, "provider is required", http.StatusBadRequest)
		return
	}

	if !providers.IsModelProviderFetchable(req.Provider) {
		http.Error(
			w,
			fmt.Sprintf("provider %q does not support model listing", req.Provider),
			http.StatusBadRequest,
		)
		return
	}

	apiKey := strings.TrimSpace(req.APIKey)
	apiBase := strings.TrimSpace(req.APIBase)
	authMethod := strings.ToLower(strings.TrimSpace(req.AuthMethod))
	credentialID := strings.TrimSpace(req.CredentialID)

	if req.ModelIndex != nil {
		stored := h.lookupStoredModelFetchAuth(*req.ModelIndex, req.Provider, apiBase)
		if apiKey == "" && stored.APIKey != "" {
			apiKey = stored.APIKey
		}
		if authMethod == "" && stored.AuthMethod != "" {
			authMethod = stored.AuthMethod
		}
		if credentialID == "" && stored.CredentialID != "" {
			credentialID = stored.CredentialID
		}
	}

	if apiBase == "" {
		apiBase = providers.DefaultAPIBaseForProtocol(req.Provider)
	}
	if apiBase == "" {
		http.Error(
			w,
			fmt.Sprintf("No default API base for provider %q", req.Provider),
			http.StatusBadRequest,
		)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	models, err := fetchUpstreamModels(ctx, upstreamFetchOptions{
		Provider:     req.Provider,
		APIBase:      apiBase,
		APIKey:       apiKey,
		AuthMethod:   authMethod,
		CredentialID: credentialID,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to fetch models: %v", err), http.StatusBadGateway)
		return
	}

	// Auto-save fetched models to catalog
	catalogModels := make([]CatalogModel, len(models))
	for i, m := range models {
		catalogModels[i] = CatalogModel{ID: m.ID, OwnedBy: m.OwnedBy}
	}
	if saveErr := SaveCatalog(req.Provider, apiBase, apiKey, catalogModels); saveErr != nil {
		// Log but don't fail the request — saving catalog is non-critical
		logger.Warnf("Failed to save model catalog: %v", saveErr)
	}

	writeFetchModelsResponse(w, models, nil)
}

type accountModelFetchIssue struct {
	AccountRef string `json:"account_ref"`
	Error      string `json:"error"`
}

type fetchModelsResponse struct {
	Models []upstreamModel          `json:"models"`
	Total  int                      `json:"total"`
	Issues []accountModelFetchIssue `json:"issues,omitempty"`
}

func writeFetchModelsResponse(
	w http.ResponseWriter,
	models []upstreamModel,
	issues []accountModelFetchIssue,
) {
	if models == nil {
		models = []upstreamModel{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(fetchModelsResponse{
		Models: models,
		Total:  len(models),
		Issues: issues,
	})
}

func (h *Handler) fetchModelsForAccountRef(
	ctx context.Context,
	accountRef string,
) ([]upstreamModel, []accountModelFetchIssue, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}

	accountRef = strings.TrimSpace(accountRef)
	if accountRef == "" {
		return nil, nil, fmt.Errorf("account_ref is required")
	}
	if routerIndex := findAccountRouterIndex(cfg, accountRef); routerIndex >= 0 {
		return fetchModelsForAccountRouter(ctx, cfg, &cfg.AccountRouters[routerIndex])
	}

	modelConfig, err := resolveAccountModelConfig(cfg, accountRef)
	if err != nil {
		return nil, nil, err
	}
	models, fetchErr := fetchModelsForAccountConfig(ctx, modelConfig)
	if fetchErr != nil && len(models) == 0 {
		return nil, nil, fetchErr
	}
	var issues []accountModelFetchIssue
	if fetchErr != nil {
		issues = append(issues, accountModelFetchIssue{
			AccountRef: accountRef,
			Error:      fetchErr.Error(),
		})
	}
	return models, issues, nil
}

func fetchModelsForAccountRouter(
	ctx context.Context,
	cfg *config.Config,
	router *config.AccountRouterConfig,
) ([]upstreamModel, []accountModelFetchIssue, error) {
	accountRefs := reachableAccountRouterRefs(router)
	if len(accountRefs) == 0 {
		return nil, nil, fmt.Errorf("account router has no reachable accounts")
	}

	type fetchResult struct {
		models []upstreamModel
		err    error
	}
	results := make([]fetchResult, len(accountRefs))
	var wg sync.WaitGroup
	wg.Add(len(accountRefs))
	for i, accountRef := range accountRefs {
		go func() {
			defer wg.Done()
			modelConfig, err := resolveAccountModelConfig(cfg, accountRef)
			if err != nil {
				results[i].err = err
				return
			}
			results[i].models, results[i].err = fetchModelsForAccountConfig(ctx, modelConfig)
		}()
	}
	wg.Wait()

	modelLists := make([][]upstreamModel, 0, len(results))
	issues := make([]accountModelFetchIssue, 0)
	for i, result := range results {
		if len(result.models) > 0 {
			modelLists = append(modelLists, result.models)
		}
		if result.err != nil {
			issues = append(issues, accountModelFetchIssue{
				AccountRef: accountRefs[i],
				Error:      result.err.Error(),
			})
		}
	}
	return intersectUpstreamModelLists(modelLists), issues, nil
}

func reachableAccountRouterRefs(router *config.AccountRouterConfig) []string {
	if router == nil {
		return nil
	}
	blocksByID := make(map[string]config.AccountRouterBlock, len(router.Blocks))
	for _, block := range router.Blocks {
		id := strings.TrimSpace(block.ID)
		if id != "" {
			blocksByID[id] = block
		}
	}

	seenBlocks := make(map[string]bool, len(blocksByID))
	seenAccounts := make(map[string]bool)
	accountRefs := make([]string, 0)
	addAccount := func(accountRef string) {
		accountRef = strings.TrimSpace(accountRef)
		if accountRef == "" || seenAccounts[accountRef] {
			return
		}
		seenAccounts[accountRef] = true
		accountRefs = append(accountRefs, accountRef)
	}

	var walk func(string)
	walk = func(blockID string) {
		blockID = strings.TrimSpace(blockID)
		if blockID == "" || seenBlocks[blockID] {
			return
		}
		block, ok := blocksByID[blockID]
		if !ok {
			return
		}
		seenBlocks[blockID] = true

		switch strings.TrimSpace(block.Type) {
		case config.AccountRouterBlockTypeAccount:
			addAccount(block.Account)
		case config.AccountRouterBlockTypeLoadBalance:
			for _, accountRef := range block.Accounts {
				addAccount(accountRef)
			}
		}

		if strings.TrimSpace(block.Type) == config.AccountRouterBlockTypeBranch {
			walk(block.Then)
			walk(block.Else)
		}
		walk(block.Fallback)
	}
	walk(router.Entry)
	return accountRefs
}

func resolveAccountModelConfig(
	cfg *config.Config,
	accountRef string,
) (*config.ModelConfig, error) {
	accountRef = strings.TrimSpace(accountRef)
	if cfg == nil || accountRef == "" {
		return nil, fmt.Errorf("account_ref is required")
	}

	if credentialID, ok := config.AccountRouterCredentialAccountID(accountRef); ok {
		provider, providerOK := config.AccountRouterCredentialAccountProvider(accountRef)
		if !providerOK {
			return nil, fmt.Errorf("credential account %q has an unsupported provider", accountRef)
		}
		normalizedCredentialID, err := auth.NormalizeCredentialID(
			authProviderForCredentialModel(provider),
			credentialID,
		)
		if err != nil {
			return nil, fmt.Errorf("normalizing credential account %q: %w", accountRef, err)
		}
		credential, err := oauthGetCredential(normalizedCredentialID)
		if err != nil {
			return nil, fmt.Errorf("loading credential account %q: %w", accountRef, err)
		}
		if credential == nil ||
			strings.TrimSpace(credential.AccessToken) == "" &&
				strings.TrimSpace(credential.RefreshToken) == "" {
			return nil, fmt.Errorf("credential account %q is unavailable", accountRef)
		}
		if !credentialProviderMatches(credential, provider) {
			return nil, fmt.Errorf(
				"credential account %q belongs to provider %q",
				accountRef,
				credential.Provider,
			)
		}
		provider = providers.NormalizeProvider(provider)
		return &config.ModelConfig{
			ModelName:    accountRef,
			Provider:     provider,
			Model:        "",
			AuthMethod:   defaultCredentialAccountAuthMethod(provider),
			CredentialID: normalizedCredentialID,
			Enabled:      true,
		}, nil
	}

	var matches []*config.ModelConfig
	disabled := false
	for _, modelConfig := range cfg.ModelList {
		if modelConfig == nil || strings.TrimSpace(modelConfig.ModelName) != accountRef {
			continue
		}
		if modelConfig.IsAccountRouter() {
			return nil, fmt.Errorf("account router %q cannot be nested as an account", accountRef)
		}
		if modelConfig.IsModelRouter() {
			return nil, fmt.Errorf("model router %q cannot be used as an account", accountRef)
		}
		if !modelConfig.Enabled {
			disabled = true
			continue
		}
		matches = append(matches, modelConfig)
	}
	switch len(matches) {
	case 0:
		if disabled {
			return nil, fmt.Errorf("account %q is disabled", accountRef)
		}
		return nil, fmt.Errorf("account %q not found", accountRef)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("account %q is ambiguous", accountRef)
	}
}

func fetchModelsForAccountConfig(
	ctx context.Context,
	modelConfig *config.ModelConfig,
) ([]upstreamModel, error) {
	if modelConfig == nil {
		return nil, fmt.Errorf("account config is required")
	}
	provider, _ := providers.ExtractProtocol(modelConfig)
	provider = providers.NormalizeProvider(provider)
	fallbackModels := staticAccountModels(modelConfig, provider)
	if provider == "antigravity" {
		models, listed, err := fetchAntigravityCredentialModels(ctx, modelConfig)
		models = normalizeUpstreamModels(models)
		if err == nil && len(models) > 0 {
			return models, nil
		}
		if err == nil {
			err = fmt.Errorf("provider %q returned no available models", provider)
		}
		if listed {
			return nil, err
		}
		if len(fallbackModels) > 0 {
			return fallbackModels, err
		}
		return nil, err
	}
	if !providers.IsModelProviderFetchable(provider) {
		if len(fallbackModels) == 0 {
			return nil, fmt.Errorf(
				"provider %q does not support model listing and has no configured fallback",
				provider,
			)
		}
		return fallbackModels, nil
	}

	apiBase := providers.ResolveAPIBase(modelConfig)
	if apiBase == "" {
		if len(fallbackModels) > 0 {
			return fallbackModels, fmt.Errorf("no default API base for provider %q", provider)
		}
		return nil, fmt.Errorf("no default API base for provider %q", provider)
	}
	models, err := fetchUpstreamModels(ctx, upstreamFetchOptions{
		Provider:     provider,
		APIBase:      apiBase,
		APIKey:       modelConfig.APIKey(),
		AuthMethod:   strings.ToLower(strings.TrimSpace(modelConfig.AuthMethod)),
		CredentialID: strings.TrimSpace(modelConfig.CredentialID),
	})
	models = normalizeUpstreamModels(models)
	if err == nil && len(models) > 0 {
		return models, nil
	}
	if err == nil {
		err = fmt.Errorf("provider %q returned no models", provider)
	}
	if len(fallbackModels) > 0 {
		return fallbackModels, err
	}
	return nil, err
}

func staticAccountModels(
	modelConfig *config.ModelConfig,
	provider string,
) []upstreamModel {
	models := make([]upstreamModel, 0)
	if modelConfig != nil {
		_, modelID := providers.ExtractProtocol(modelConfig)
		if modelID = strings.TrimSpace(modelID); modelID != "" {
			models = append(models, upstreamModel{ID: modelID, OwnedBy: provider})
		}
	}
	for _, modelID := range providers.CommonModelsForProvider(provider) {
		models = append(models, upstreamModel{ID: modelID, OwnedBy: provider})
	}
	return normalizeUpstreamModels(models)
}

func normalizeUpstreamModels(models []upstreamModel) []upstreamModel {
	normalized := make([]upstreamModel, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" {
			continue
		}
		key := strings.ToLower(model.ID)
		if seen[key] {
			continue
		}
		seen[key] = true
		model.OwnedBy = strings.TrimSpace(model.OwnedBy)
		normalized = append(normalized, model)
	}
	return normalized
}

func intersectUpstreamModelLists(modelLists [][]upstreamModel) []upstreamModel {
	if len(modelLists) == 0 {
		return []upstreamModel{}
	}
	first := normalizeUpstreamModels(modelLists[0])
	if len(first) == 0 {
		return []upstreamModel{}
	}
	sets := make([]map[string]bool, 0, len(modelLists)-1)
	for _, models := range modelLists[1:] {
		set := make(map[string]bool)
		for _, model := range normalizeUpstreamModels(models) {
			set[strings.ToLower(model.ID)] = true
		}
		sets = append(sets, set)
	}
	intersection := make([]upstreamModel, 0, len(first))
	for _, model := range first {
		key := strings.ToLower(model.ID)
		present := true
		for _, set := range sets {
			if !set[key] {
				present = false
				break
			}
		}
		if present {
			intersection = append(intersection, model)
		}
	}
	return intersection
}

type storedModelFetchAuth struct {
	APIKey       string
	AuthMethod   string
	CredentialID string
}

func (h *Handler) lookupStoredModelFetchAuth(
	index int,
	reqProvider, reqAPIBase string,
) storedModelFetchAuth {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil || index < 0 || index >= len(cfg.ModelList) {
		return storedModelFetchAuth{}
	}
	stored := cfg.ModelList[index]
	storedProvider, _ := providers.ExtractProtocol(stored)
	if providers.NormalizeProvider(reqProvider) != providers.NormalizeProvider(storedProvider) {
		return storedModelFetchAuth{}
	}
	effectiveReqBase := strings.TrimSpace(reqAPIBase)
	if effectiveReqBase == "" {
		effectiveReqBase = providers.DefaultAPIBaseForProtocol(reqProvider)
	}
	effectiveStoredBase := strings.TrimSpace(stored.APIBase)
	if effectiveStoredBase == "" {
		effectiveStoredBase = providers.DefaultAPIBaseForProtocol(storedProvider)
	}
	if normalizeAPIBaseForCompare(
		effectiveReqBase,
	) != normalizeAPIBaseForCompare(
		effectiveStoredBase,
	) {
		return storedModelFetchAuth{}
	}
	return storedModelFetchAuth{
		APIKey:       stored.APIKey(),
		AuthMethod:   strings.ToLower(strings.TrimSpace(stored.AuthMethod)),
		CredentialID: strings.TrimSpace(stored.CredentialID),
	}
}

type upstreamModel struct {
	ID      string `json:"id"`
	OwnedBy string `json:"owned_by,omitempty"`
}

type upstreamFetchOptions struct {
	Provider     string
	APIBase      string
	APIKey       string
	AuthMethod   string
	CredentialID string
}

const openAICodexModelsEndpointDefault = "https://chatgpt.com/backend-api/codex/models"

var openAICodexModelsEndpoint = openAICodexModelsEndpointDefault

const openAICodexModelsClientVersionDefault = "0.144.0"

var listGitHubCopilotModelsWithToken = cliprovider.ListGitHubCopilotModelsWithToken

var (
	fetchAntigravityModels       = providers.FetchAntigravityModelsContext
	resolveAntigravityProject    = providers.FetchAntigravityProjectID
	refreshAntigravityCredential = auth.RefreshAccessToken
	refreshOpenAICodexCredential = auth.RefreshAccessToken
	refreshModelCredential       = auth.RefreshCredential
)

func isCredentialAuthMethod(authMethod string) bool {
	switch strings.ToLower(strings.TrimSpace(authMethod)) {
	case "oauth", "token":
		return true
	default:
		return false
	}
}

func fetchAntigravityCredentialModels(
	ctx context.Context,
	modelConfig *config.ModelConfig,
) ([]upstreamModel, bool, error) {
	credentialID, err := auth.NormalizeCredentialID(
		authProviderForCredentialModel("antigravity"),
		strings.TrimSpace(modelConfig.CredentialID),
	)
	if err != nil {
		return nil, false, fmt.Errorf("normalizing Antigravity credential: %w", err)
	}
	credential, err := oauthGetCredential(credentialID)
	if err != nil {
		return nil, false, fmt.Errorf("loading Antigravity credential %s: %w", credentialID, err)
	}
	if credential == nil {
		return nil, false, fmt.Errorf("no Antigravity credential for %s", credentialID)
	}
	if !credentialProviderMatches(credential, "antigravity") {
		return nil, false, fmt.Errorf(
			"credential %s belongs to provider %q, not antigravity",
			credentialID,
			credential.Provider,
		)
	}
	if credential.NeedsRefresh() && strings.TrimSpace(credential.RefreshToken) != "" {
		var refreshErr error
		credential, refreshErr = refreshModelCredential(
			credentialID,
			func(current *auth.AuthCredential) bool {
				return current != nil &&
					current.NeedsRefresh() &&
					strings.TrimSpace(current.RefreshToken) != ""
			},
			func(current *auth.AuthCredential) (*auth.AuthCredential, error) {
				if !credentialProviderMatches(current, "antigravity") {
					return nil, fmt.Errorf(
						"credential belongs to provider %q, not antigravity",
						current.Provider,
					)
				}
				refreshed, credentialRefreshErr := refreshAntigravityCredential(
					current,
					auth.GoogleAntigravityOAuthConfig(),
				)
				if credentialRefreshErr != nil {
					return nil, credentialRefreshErr
				}
				if refreshed == nil {
					return nil, fmt.Errorf("no credential returned")
				}
				refreshed.Email = current.Email
				refreshed.ProjectID = ""
				if !credentialProviderMatches(refreshed, "antigravity") {
					return nil, fmt.Errorf(
						"refreshed credential belongs to provider %q, not antigravity",
						refreshed.Provider,
					)
				}
				return refreshed, nil
			},
		)
		if refreshErr != nil {
			return nil, false, fmt.Errorf(
				"refreshing Antigravity credential %s: %w",
				credentialID,
				refreshErr,
			)
		}
		if credential == nil {
			return nil, false, fmt.Errorf(
				"Antigravity credential %s was removed while refreshing",
				credentialID,
			)
		}
		if !credentialProviderMatches(credential, "antigravity") {
			return nil, false, fmt.Errorf(
				"credential %s belongs to provider %q, not antigravity",
				credentialID,
				credential.Provider,
			)
		}
	}
	if credential.IsExpired() {
		return nil, false, fmt.Errorf(
			"Antigravity credential %s is expired",
			credentialID,
		)
	}
	for attempts := 0; strings.TrimSpace(credential.ProjectID) == "" && attempts < 2; attempts++ {
		credential, err = refreshModelCredential(
			credentialID,
			func(current *auth.AuthCredential) bool {
				return current != nil && strings.TrimSpace(current.ProjectID) == ""
			},
			func(current *auth.AuthCredential) (*auth.AuthCredential, error) {
				if !credentialProviderMatches(current, "antigravity") {
					return nil, fmt.Errorf(
						"credential belongs to provider %q, not antigravity",
						current.Provider,
					)
				}
				projectID, projectErr := resolveAntigravityProject(current.AccessToken)
				if projectErr != nil {
					return nil, projectErr
				}
				updated := *current
				updated.ProjectID = projectID
				return &updated, nil
			},
		)
		if err != nil {
			return nil, false, fmt.Errorf(
				"resolving Antigravity project for credential %s: %w",
				credentialID,
				err,
			)
		}
		if credential == nil {
			return nil, false, fmt.Errorf(
				"Antigravity credential %s was removed while resolving project",
				credentialID,
			)
		}
		if !credentialProviderMatches(credential, "antigravity") {
			return nil, false, fmt.Errorf(
				"credential %s belongs to provider %q, not antigravity",
				credentialID,
				credential.Provider,
			)
		}
	}
	accessToken := strings.TrimSpace(credential.AccessToken)
	if accessToken == "" {
		return nil, false, fmt.Errorf("Antigravity credential %s has no access token", credentialID)
	}
	projectID := strings.TrimSpace(credential.ProjectID)
	if projectID == "" {
		return nil, false, fmt.Errorf("Antigravity credential %s has no project ID", credentialID)
	}

	models, err := fetchAntigravityModels(ctx, accessToken, projectID)
	if err != nil {
		return nil, false, fmt.Errorf("listing Antigravity models: %w", err)
	}
	out := make([]upstreamModel, 0, len(models))
	for _, model := range models {
		if model.IsExhausted {
			continue
		}
		out = append(out, upstreamModel{
			ID:      model.ID,
			OwnedBy: "antigravity",
		})
	}
	return out, true, nil
}

func fetchUpstreamModels(ctx context.Context, opts upstreamFetchOptions) ([]upstreamModel, error) {
	apiBase := strings.TrimRight(strings.TrimSpace(opts.APIBase), "/")
	apiKey := strings.TrimSpace(opts.APIKey)
	provider := providers.NormalizeProvider(opts.Provider)
	authMethod := strings.ToLower(strings.TrimSpace(opts.AuthMethod))

	var fetchURL string
	switch provider {
	case "ollama":
		// Strip /v1 suffix if present to get the Ollama root
		root := apiBase
		if strings.HasSuffix(root, "/v1") {
			root = root[:len(root)-3]
		}
		root = strings.TrimRight(root, "/")
		fetchURL = root + "/api/tags"
		return fetchOllamaModels(ctx, fetchURL)
	case "nearai":
		fetchURL = apiBase + "/model/list"
		return fetchNearAIModels(ctx, fetchURL, apiKey)
	case "openai":
		if isCredentialAuthMethod(authMethod) {
			return fetchOpenAICodexModels(ctx, opts.CredentialID)
		}
		fetchURL = apiBase + "/models"
		return fetchOpenAICompatibleModels(ctx, fetchURL, apiKey)
	case "github-copilot":
		return fetchGitHubCopilotModels(ctx, authMethod, opts.CredentialID)
	default:
		// OpenAI-compatible: /v1/models
		fetchURL = apiBase + "/models"
		return fetchOpenAICompatibleModels(ctx, fetchURL, apiKey)
	}
}

func fetchStaticGitHubCopilotModels() []upstreamModel {
	modelIDs := providers.CommonModelsForProvider("github-copilot")
	models := make([]upstreamModel, 0, len(modelIDs))
	for _, id := range modelIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		models = append(models, upstreamModel{ID: id, OwnedBy: "github-copilot"})
	}
	return models
}

func fetchGitHubCopilotModels(
	ctx context.Context,
	authMethod string,
	credentialID string,
) ([]upstreamModel, error) {
	if !isCredentialAuthMethod(authMethod) && strings.TrimSpace(credentialID) == "" {
		return fetchStaticGitHubCopilotModels(), nil
	}
	cred, err := resolveGitHubCopilotCredential(credentialID)
	if err != nil {
		return nil, err
	}
	models, err := listGitHubCopilotModelsWithToken(ctx, cred.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("listing GitHub Copilot models: %w", err)
	}
	out := make([]upstreamModel, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, upstreamModel{ID: id, OwnedBy: "github-copilot"})
	}
	return out, nil
}

func openAICodexModelsFetchURL() string {
	fetchURL := strings.TrimSpace(openAICodexModelsEndpoint)
	if fetchURL == "" {
		fetchURL = openAICodexModelsEndpointDefault
	}
	separator := "?"
	if strings.Contains(fetchURL, "?") {
		separator = "&"
	}
	return fetchURL + separator + "client_version=" + url.QueryEscape(
		openAICodexModelsClientVersion(),
	)
}

func openAICodexModelsClientVersion() string {
	return openAICodexModelsClientVersionDefault
}

func upstreamStatusError(provider string, statusCode int, body io.Reader) error {
	preview, _ := io.ReadAll(io.LimitReader(body, 512))
	detail := strings.TrimSpace(string(preview))
	if detail == "" {
		return fmt.Errorf("%s returned status %d", provider, statusCode)
	}
	return fmt.Errorf("%s returned status %d: %s", provider, statusCode, detail)
}

func resolveOpenAICodexCredential(credentialID string) (*auth.AuthCredential, error) {
	normalizedCredentialID, err := auth.NormalizeCredentialID("openai", credentialID)
	if err != nil {
		return nil, err
	}
	cred, err := auth.GetCredential(normalizedCredentialID)
	if err != nil {
		return nil, fmt.Errorf("loading auth credentials: %w", err)
	}
	if cred == nil {
		return nil, fmt.Errorf(
			"no credentials for %s. Run: picoclaw auth login --provider openai --credential-id %s",
			normalizedCredentialID,
			normalizedCredentialID,
		)
	}
	if !credentialProviderMatches(cred, "openai") {
		return nil, fmt.Errorf(
			"credential %s belongs to provider %q, not openai",
			normalizedCredentialID,
			cred.Provider,
		)
	}
	if cred.AuthMethod == "oauth" && cred.NeedsRefresh() && cred.RefreshToken != "" {
		cred, err = refreshModelCredential(
			normalizedCredentialID,
			func(current *auth.AuthCredential) bool {
				return current != nil &&
					current.AuthMethod == "oauth" &&
					current.NeedsRefresh() &&
					current.RefreshToken != ""
			},
			func(current *auth.AuthCredential) (*auth.AuthCredential, error) {
				if !credentialProviderMatches(current, "openai") {
					return nil, fmt.Errorf(
						"credential belongs to provider %q, not openai",
						current.Provider,
					)
				}
				refreshed, refreshErr := refreshOpenAICodexCredential(
					current,
					auth.OpenAIOAuthConfig(),
				)
				if refreshErr != nil {
					return nil, refreshErr
				}
				if refreshed == nil {
					return nil, fmt.Errorf("no credential returned")
				}
				if refreshed.AccountID == "" {
					refreshed.AccountID = current.AccountID
				}
				return refreshed, nil
			},
		)
		if err != nil {
			return nil, fmt.Errorf("refreshing token: %w", err)
		}
		if cred == nil {
			return nil, fmt.Errorf(
				"credential %s was removed while refreshing",
				normalizedCredentialID,
			)
		}
		if !credentialProviderMatches(cred, "openai") {
			return nil, fmt.Errorf(
				"credential %s belongs to provider %q, not openai",
				normalizedCredentialID,
				cred.Provider,
			)
		}
	}
	if strings.TrimSpace(cred.AccessToken) == "" {
		return nil, fmt.Errorf(
			"OpenAI Codex credential %s has no access token",
			normalizedCredentialID,
		)
	}
	return cred, nil
}

func resolveGitHubCopilotCredential(credentialID string) (*auth.AuthCredential, error) {
	normalizedCredentialID, err := auth.NormalizeCredentialID("github-copilot", credentialID)
	if err != nil {
		return nil, err
	}
	cred, err := auth.GetCredential(normalizedCredentialID)
	if err != nil {
		return nil, fmt.Errorf(
			"loading GitHub Copilot credential %s: %w",
			normalizedCredentialID,
			err,
		)
	}
	if cred == nil {
		return nil, fmt.Errorf(
			"no credentials for %s. Run: picoclaw auth login --provider github-copilot --credential-id %s",
			normalizedCredentialID,
			normalizedCredentialID,
		)
	}
	if strings.TrimSpace(cred.AccessToken) == "" {
		return nil, fmt.Errorf(
			"GitHub Copilot credential %s has no access token",
			normalizedCredentialID,
		)
	}
	if provider := providers.NormalizeProvider(cred.Provider); provider != "" &&
		provider != "github-copilot" {
		return nil, fmt.Errorf(
			"credential %s belongs to provider %q, not github-copilot",
			normalizedCredentialID,
			cred.Provider,
		)
	}
	return cred, nil
}

func fetchOpenAICodexModels(ctx context.Context, credentialID string) ([]upstreamModel, error) {
	cred, err := resolveOpenAICodexCredential(credentialID)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openAICodexModelsFetchURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cred.AccessToken))
	if accountID := strings.TrimSpace(cred.AccountID); accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
	req.Header.Set("Oai-Product-Sku", "codex")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, upstreamStatusError("codex upstream", resp.StatusCode, resp.Body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return parseOpenAICodexModelsBody(body)
}

func parseOpenAICodexModelsBody(body []byte) ([]upstreamModel, error) {
	var envelope struct {
		Models []struct {
			Slug           string `json:"slug"`
			DisplayName    string `json:"display_name"`
			Visibility     string `json:"visibility"`
			SupportedInAPI *bool  `json:"supported_in_api"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Models != nil {
		models := make([]upstreamModel, 0, len(envelope.Models))
		for _, m := range envelope.Models {
			id := strings.TrimSpace(m.Slug)
			if id == "" {
				id = strings.TrimSpace(m.DisplayName)
			}
			if id == "" {
				continue
			}
			visibility := strings.ToLower(strings.TrimSpace(m.Visibility))
			if visibility != "" && visibility != "list" {
				continue
			}
			models = append(models, upstreamModel{ID: id, OwnedBy: "openai-codex"})
		}
		return models, nil
	}
	return parseOpenAICompatibleModelsBody(body)
}

func fetchNearAIModels(ctx context.Context, fetchURL, apiKey string) ([]upstreamModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, upstreamStatusError("nearai", resp.StatusCode, resp.Body)
	}

	var parsed struct {
		Models []struct {
			ModelID  string `json:"modelId"`
			OwnedBy  string `json:"ownedBy"`
			Metadata struct {
				OwnedBy string `json:"ownedBy"`
			} `json:"metadata"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	models := make([]upstreamModel, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		id := strings.TrimSpace(m.ModelID)
		if id == "" {
			continue
		}
		ownedBy := strings.TrimSpace(m.OwnedBy)
		if ownedBy == "" {
			ownedBy = strings.TrimSpace(m.Metadata.OwnedBy)
		}
		models = append(models, upstreamModel{ID: id, OwnedBy: ownedBy})
	}
	return models, nil
}

func fetchOpenAICompatibleModels(
	ctx context.Context,
	fetchURL, apiKey string,
) ([]upstreamModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, upstreamStatusError("upstream", resp.StatusCode, resp.Body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return parseOpenAICompatibleModelsBody(body)
}

func parseOpenAICompatibleModelsBody(body []byte) ([]upstreamModel, error) {
	type modelItem struct {
		ID      string `json:"id"`
		OwnedBy string `json:"owned_by"`
	}

	// {"data": [...]} envelope. Distinguish "envelope shape with empty list"
	// from "object without a data key" via Data being non-nil after unmarshal:
	// json.Unmarshal sets Data to []modelItem{} for `{"data":[]}` but leaves
	// it as nil when "data" is absent or null.
	var envelope struct {
		Data []modelItem `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Data != nil {
		models := make([]upstreamModel, 0, len(envelope.Data))
		for _, m := range envelope.Data {
			if m.ID != "" {
				models = append(models, upstreamModel{ID: m.ID, OwnedBy: m.OwnedBy})
			}
		}
		return models, nil
	}

	// Bare-array shape, including `[]`.
	var arr []modelItem
	if err := json.Unmarshal(body, &arr); err == nil {
		models := make([]upstreamModel, 0, len(arr))
		for _, m := range arr {
			if m.ID != "" {
				models = append(models, upstreamModel{ID: m.ID, OwnedBy: m.OwnedBy})
			}
		}
		return models, nil
	}

	preview := body
	if len(preview) > 256 {
		preview = preview[:256]
	}
	return nil, fmt.Errorf(
		"decode response: unrecognized shape: %s",
		strings.TrimSpace(string(preview)),
	)
}

func fetchOllamaModels(ctx context.Context, fetchURL string) ([]upstreamModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, upstreamStatusError("ollama", resp.StatusCode, resp.Body)
	}

	var parsed struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}

	models := make([]upstreamModel, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		id := m.Name
		if id == "" {
			id = m.Model
		}
		if id != "" {
			models = append(models, upstreamModel{ID: id})
		}
	}
	return models, nil
}

// normalizeAPIBaseForCompare normalizes an API base URL for equality comparison
// by trimming trailing slashes and lowering the scheme/host.
func normalizeAPIBaseForCompare(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.TrimRight(raw, "/")
	u, err := url.Parse(raw)
	if err != nil {
		return strings.ToLower(raw)
	}
	if u.Host == "" {
		u, err = url.Parse("//" + raw)
		if err != nil {
			return strings.ToLower(raw)
		}
	}
	return strings.ToLower(
		u.Scheme,
	) + "://" + strings.ToLower(
		u.Host,
	) + strings.TrimRight(
		u.Path,
		"/",
	)
}

// handleTestModel tests connectivity to a model endpoint.
//
//	POST /api/accounts/models/{index}/test
func (h *Handler) handleTestModel(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.Error(w, "Invalid index", http.StatusBadRequest)
		return
	}

	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	if idx < 0 || idx >= len(cfg.ModelList) {
		http.Error(
			w,
			fmt.Sprintf("Index %d out of range (0-%d)", idx, len(cfg.ModelList)-1),
			http.StatusNotFound,
		)
		return
	}

	m := cfg.ModelList[idx]
	start := time.Now()
	summary := modelConfigurationStatus(m)
	latency := time.Since(start).Milliseconds()

	result := map[string]any{
		"success":    summary.Available,
		"latency_ms": latency,
		"status":     summary.Status,
	}

	if !summary.Available {
		if summary.Status == modelStatusUnconfigured {
			result["error"] = "API key not configured"
		} else {
			result["error"] = "Endpoint unreachable"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleTestInlineModel tests connectivity using inline (unsaved) parameters.
// Unlike handleTestModel which only checks saved config, this endpoint performs
// a real network probe (e.g. GET /models) to verify the endpoint is reachable.
//
//	POST /api/accounts/models/test-inline
func (h *Handler) handleTestInlineModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	var req struct {
		Provider   string `json:"provider"`
		Model      string `json:"model"`
		APIBase    string `json:"api_base"`
		APIKey     string `json:"api_key"`
		AuthMethod string `json:"auth_method"`
		ModelIndex *int   `json:"model_index"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	m := &config.ModelConfig{
		Provider:   strings.TrimSpace(req.Provider),
		Model:      strings.TrimSpace(req.Model),
		APIBase:    strings.TrimSpace(req.APIBase),
		AuthMethod: strings.TrimSpace(req.AuthMethod),
	}
	if req.APIKey != "" {
		m.SetAPIKey(req.APIKey)
	}

	// When api_key is empty and model_index is provided, fall back to stored credentials.
	// This lets the edit form test unsaved field changes while using the saved key.
	// Only reuse the stored key when the provider and effective API base match
	// the saved model, to prevent attaching a credential to a different endpoint.
	if req.APIKey == "" && req.ModelIndex != nil {
		cfg, err := config.LoadConfig(h.configPath)
		if err == nil && *req.ModelIndex >= 0 && *req.ModelIndex < len(cfg.ModelList) {
			stored := cfg.ModelList[*req.ModelIndex]
			storedProvider, _ := providers.ExtractProtocol(stored)
			reqProvider := providers.NormalizeProvider(m.Provider)
			providerMatch := reqProvider == "" ||
				reqProvider == providers.NormalizeProvider(storedProvider)

			effectiveReqBase := strings.TrimSpace(m.APIBase)
			if effectiveReqBase == "" {
				effectiveReqBase = providers.DefaultAPIBaseForProtocol(reqProvider)
			}
			effectiveStoredBase := strings.TrimSpace(stored.APIBase)
			if effectiveStoredBase == "" {
				effectiveStoredBase = providers.DefaultAPIBaseForProtocol(storedProvider)
			}
			baseMatch := normalizeAPIBaseForCompare(
				effectiveReqBase,
			) == normalizeAPIBaseForCompare(
				effectiveStoredBase,
			)

			if providerMatch && baseMatch {
				if stored.APIKey() != "" {
					m.SetAPIKey(stored.APIKey())
				}
				if m.APIBase == "" && stored.APIBase != "" {
					m.APIBase = stored.APIBase
				}
			}
		}
	}

	// Check if configuration exists
	if !hasModelConfiguration(m) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success":    false,
			"latency_ms": 0,
			"status":     modelStatusUnconfigured,
			"error":      "API key not configured",
		})
		return
	}

	// Perform a real network probe
	start := time.Now()
	available := probeModelConnectivity(m)
	latency := time.Since(start).Milliseconds()

	result := map[string]any{
		"success":    available,
		"latency_ms": latency,
	}
	if available {
		result["status"] = modelStatusAvailable
	} else {
		result["status"] = modelStatusUnreachable
		result["error"] = "Endpoint unreachable"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// probeModelConnectivity performs a real network probe to verify model endpoint reachability.
func probeModelConnectivity(m *config.ModelConfig) bool {
	apiBase := modelProbeAPIBase(m)
	protocol, modelID := splitModel(m)

	switch protocol {
	case "ollama":
		return probeOllamaModel(apiBase, modelID)
	case "vllm", "lmstudio", "gpt4free":
		return probeOpenAICompatibleModel(apiBase, modelID, m.APIKey())
	case "github-copilot":
		if isCredentialAuthMethod(m.AuthMethod) {
			return hasModelConfiguration(m)
		}
		return probeTCPService(apiBase)
	case "claude-cli":
		return probeCommandAvailable("claude")
	case "codex-cli":
		return probeCommandAvailable("codex")
	default:
		// For remote providers (OpenAI, Anthropic, Gemini, DeepSeek, etc.),
		// make a real GET /models request to verify connectivity and credentials.
		if apiBase != "" {
			return probeOpenAICompatibleModel(apiBase, modelID, m.APIKey())
		}
		return false
	}
}
