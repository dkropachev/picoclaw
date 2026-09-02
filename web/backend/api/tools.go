package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/providers"
	picotools "github.com/sipeed/picoclaw/pkg/tools"
)

const toolStateRequestMaxBytes = 1 << 20

type toolCatalogEntry struct {
	Name        string
	Description string
	Category    string
	ConfigKey   string
}

type toolSupportItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	ConfigKey   string `json:"config_key"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	ReasonCode  string `json:"reason_code,omitempty"`
}

type toolSupportResponse struct {
	Tools          []toolSupportItem      `json:"tools"`
	Total          int                    `json:"total"`
	NextCursor     string                 `json:"next_cursor,omitempty"`
	CanonicalQuery string                 `json:"canonical_query,omitempty"`
	QuerySchema    collectionquery.Schema `json:"query_schema"`
}

type toolStateRequest struct {
	Enabled *bool `json:"enabled"`
}

type webSearchProviderOption struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Configured   bool   `json:"configured"`
	Current      bool   `json:"current"`
	RequiresAuth bool   `json:"requires_auth"`
}

type webSearchProviderConfig struct {
	Enabled    bool     `json:"enabled"`
	MaxResults int      `json:"max_results"`
	BaseURL    string   `json:"base_url,omitempty"`
	APIKey     string   `json:"api_key,omitempty"`
	APIKeys    []string `json:"api_keys,omitempty"`
	ModelAlias string   `json:"model_alias,omitempty"`
	APIKeySet  bool     `json:"api_key_set,omitempty"`
}

type webSearchConfigResponse struct {
	Provider       string                             `json:"provider"`
	CurrentService string                             `json:"current_service"`
	PreferNative   bool                               `json:"prefer_native"`
	Proxy          string                             `json:"proxy,omitempty"`
	Providers      []webSearchProviderOption          `json:"providers"`
	ModelAliases   []string                           `json:"model_aliases"`
	Settings       map[string]webSearchProviderConfig `json:"settings"`
}

type webSearchConfigRequest struct {
	Provider     string                             `json:"provider"`
	PreferNative bool                               `json:"prefer_native"`
	Proxy        string                             `json:"proxy"`
	Settings     map[string]webSearchProviderConfig `json:"settings"`
}

type threadPolicyRequest struct {
	Enabled      bool                                `json:"enabled"`
	Mode         string                              `json:"mode"`
	Instructions string                              `json:"instructions"`
	Rules        []config.ThreadPolicyRule           `json:"rules"`
	Agents       map[string]config.ThreadAgentPolicy `json:"agents,omitempty"`
}

type toolAdaptationConfigRequest struct {
	Enabled                bool                                    `json:"enabled"`
	VisibleToolSurface     string                                  `json:"visible_tool_surface"`
	LearnFromToolCalls     bool                                    `json:"learn_from_tool_calls"`
	RunModelProbes         bool                                    `json:"run_model_probes"`
	AllowRuntimeDowngrade  string                                  `json:"allow_runtime_downgrade"`
	AllowRuntimePromotion  string                                  `json:"allow_runtime_promotion"`
	ApplyVisibleChanges    string                                  `json:"apply_visible_changes"`
	CacheSensitiveAPIs     string                                  `json:"cache_sensitive_apis"`
	CacheBreakingDowngrade bool                                    `json:"cache_breaking_downgrade"`
	ProfileOverrides       *[]config.ToolAdaptationProfileOverride `json:"profile_overrides,omitempty"`
	Resolved               *toolAdaptationResolvedState            `json:"resolved,omitempty"`
	Observation            *picotools.ToolAdaptationObservation    `json:"observation,omitempty"`
	Outcomes               []picotools.ToolAdaptationToolOutcome   `json:"outcomes,omitempty"`
	Profiles               []toolAdaptationProfileState            `json:"profiles,omitempty"`
}

type toolAdaptationResolvedState struct {
	Provider            string `json:"provider"`
	Model               string `json:"model"`
	StoreID             string `json:"store_id"`
	VisibleToolSurface  string `json:"visible_tool_surface"`
	PinnedToolSurface   string `json:"pinned_tool_surface"`
	SurfaceEvidence     string `json:"surface_evidence"`
	RuntimeDowngrade    bool   `json:"runtime_downgrade"`
	RuntimePromotion    bool   `json:"runtime_promotion"`
	ApplyVisibleChanges string `json:"apply_visible_changes"`
	CacheSensitive      bool   `json:"cache_sensitive"`
	CacheEvidence       string `json:"cache_evidence"`
}

type toolAdaptationProfileState struct {
	ID              string                                `json:"id"`
	Label           string                                `json:"label"`
	Source          string                                `json:"source"`
	IsDefault       bool                                  `json:"is_default"`
	IsOverride      bool                                  `json:"is_override"`
	ProbeAvailable  bool                                  `json:"probe_available"`
	ProbeAccountRef string                                `json:"probe_account_ref,omitempty"`
	ProbeModelAlias string                                `json:"probe_model_alias,omitempty"`
	Resolved        toolAdaptationResolvedState           `json:"resolved"`
	Observation     *picotools.ToolAdaptationObservation  `json:"observation,omitempty"`
	Outcomes        []picotools.ToolAdaptationToolOutcome `json:"outcomes,omitempty"`
}

type toolAdaptationProbeRequest struct {
	AccountRef string `json:"account_ref"`
	ModelAlias string `json:"model_alias"`
}

var toolCatalog = []toolCatalogEntry{
	{
		Name:        "read_file",
		Description: "Read file content from the workspace or explicitly allowed paths.",
		Category:    "filesystem",
		ConfigKey:   "read_file",
	},
	{
		Name:        "write_file",
		Description: "Create or overwrite files within the writable workspace scope.",
		Category:    "filesystem",
		ConfigKey:   "write_file",
	},
	{
		Name:        "list_dir",
		Description: "Inspect directories and enumerate files available to the agent.",
		Category:    "filesystem",
		ConfigKey:   "list_dir",
	},
	{
		Name:        "edit_file",
		Description: "Apply targeted edits to existing files without rewriting everything.",
		Category:    "filesystem",
		ConfigKey:   "edit_file",
	},
	{
		Name:        "append_file",
		Description: "Append content to the end of an existing file.",
		Category:    "filesystem",
		ConfigKey:   "append_file",
	},
	{
		Name:        "exec",
		Description: "Run shell commands inside the configured workspace sandbox.",
		Category:    "filesystem",
		ConfigKey:   "exec",
	},
	{
		Name:        "cron",
		Description: "Schedule one-time or recurring reminders, jobs, and shell commands.",
		Category:    "automation",
		ConfigKey:   "cron",
	},
	{
		Name:        "web_search",
		Description: "Search the web using the configured providers.",
		Category:    "web",
		ConfigKey:   "web",
	},
	{
		Name:        "web_fetch",
		Description: "Fetch and summarize the contents of a webpage.",
		Category:    "web",
		ConfigKey:   "web_fetch",
	},
	{
		Name:        "message",
		Description: "Send a follow-up message back to the active user or chat.",
		Category:    "communication",
		ConfigKey:   "message",
	},
	{
		Name:        "send_file",
		Description: "Send an outbound file or media attachment to the active chat.",
		Category:    "communication",
		ConfigKey:   "send_file",
	},
	{
		Name:        "find_skills",
		Description: "Search external skill registries for installable skills.",
		Category:    "skills",
		ConfigKey:   "find_skills",
	},
	{
		Name:        "install_skill",
		Description: "Install a skill into the current workspace from a registry.",
		Category:    "skills",
		ConfigKey:   "install_skill",
	},
	{
		Name:        "spawn",
		Description: "Launch a background subagent for long-running or delegated work.",
		Category:    "agents",
		ConfigKey:   "spawn",
	},
	{
		Name:        "spawn_status",
		Description: "Query the status of spawned subagents.",
		Category:    "agents",
		ConfigKey:   "spawn_status",
	},
	{
		Name:        "threads",
		Description: "Search, create, switch, and configure PicoClaw UI threads.",
		Category:    "agents",
		ConfigKey:   "threads",
	},
	{
		Name:        "workflow",
		Description: "List, validate, reload, run, cancel, retry, graph, and inspect reusable workspace workflows.",
		Category:    "automation",
		ConfigKey:   "workflow",
	},
	{
		Name:        "git_workspace",
		Description: "Allocate, lock, reuse, clean, and drop local git repository workspaces.",
		Category:    "filesystem",
		ConfigKey:   "git_workspace",
	},
	{
		Name:        "i2c",
		Description: "Interact with I2C hardware devices exposed on the host.",
		Category:    "hardware",
		ConfigKey:   "i2c",
	},
	{
		Name:        "spi",
		Description: "Interact with SPI hardware devices exposed on the host.",
		Category:    "hardware",
		ConfigKey:   "spi",
	},
	{
		Name:        "serial",
		Description: "Interact with serial ports exposed on the host.",
		Category:    "hardware",
		ConfigKey:   "serial",
	},
	{
		Name:        "tool_search_tool_regex",
		Description: "Discover hidden MCP tools by regex search when tool discovery is enabled.",
		Category:    "discovery",
		ConfigKey:   "mcp.discovery.use_regex",
	},
	{
		Name:        "tool_search_tool_bm25",
		Description: "Discover hidden MCP tools by semantic ranking when tool discovery is enabled.",
		Category:    "discovery",
		ConfigKey:   "mcp.discovery.use_bm25",
	},
}

func (h *Handler) registerToolRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tools", h.handleListTools)
	mux.HandleFunc(
		"PUT /api/tools/{name}/state",
		h.requireCollectionMutationOrigin(h.handleUpdateToolState),
	)
	mux.HandleFunc("GET /api/tools/web-search-config", h.handleGetWebSearchConfig)
	mux.HandleFunc(
		"PUT /api/tools/web-search-config",
		h.requireCollectionMutationOrigin(h.handleUpdateWebSearchConfig),
	)
	mux.HandleFunc("GET /api/tools/thread-policy", h.handleGetThreadPolicy)
	mux.HandleFunc(
		"PUT /api/tools/thread-policy",
		h.requireCollectionMutationOrigin(h.handleUpdateThreadPolicy),
	)
	mux.HandleFunc("GET /api/tools/adaptation", h.handleGetToolAdaptation)
	mux.HandleFunc(
		"PUT /api/tools/adaptation",
		h.requireCollectionMutationOrigin(h.handleUpdateToolAdaptation),
	)
	mux.HandleFunc(
		"POST /api/tools/adaptation/probe",
		h.requireCollectionMutationOrigin(h.handleRunToolAdaptationProbe),
	)
	mux.HandleFunc("GET /api/tools/{id}", h.handleGetTool)
}

func (h *Handler) handleListTools(w http.ResponseWriter, r *http.Request) {
	listRequest, ok := parseCollectionListRequest(w, r, toolCollectionSchema)
	if !ok {
		return
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "config_load_failed",
			"Failed to load configuration", -1, nil,
		)
		return
	}
	items := buildToolSupport(cfg)
	page, err := pageToolSupportItems(items, listRequest)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}

	writeCollectionJSON(w, http.StatusOK, toolSupportResponse{
		Tools:          page.Items,
		Total:          page.Total,
		NextCursor:     page.NextCursor,
		CanonicalQuery: listRequest.Query.Canonical(),
		QuerySchema:    toolCollectionSchemaWithSuggestions(items),
	})
}

func (h *Handler) handleUpdateToolState(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	toolName, status, code := resolveToolStateTarget(r.PathValue("name"))
	if code != "" {
		message := "Tool not found"
		if code == "invalid_tool_id" {
			message = "Tool ID is invalid"
		}
		writeCollectionError(w, status, code, message, -1, nil)
		return
	}
	if err := validateToolStateContentType(r); err != nil {
		writeCollectionError(
			w,
			http.StatusUnsupportedMediaType,
			"json_content_type_required",
			"Content-Type must be application/json",
			-1,
			nil,
		)
		return
	}
	var req toolStateRequest
	if err := decodeToolStateRequest(w, r, &req); err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeCollectionError(
				w,
				http.StatusRequestEntityTooLarge,
				"collection_request_too_large",
				"Tool state request exceeds 1 MiB",
				-1,
				nil,
			)
			return
		}
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_tool_state",
			"Invalid tool state request",
			-1,
			nil,
		)
		return
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	if applyErr := applyToolState(cfg, toolName, *req.Enabled); applyErr != nil {
		writeCollectionError(
			w,
			http.StatusUnprocessableEntity,
			"tool_state_unavailable",
			"Tool state cannot be updated",
			-1,
			nil,
		)
		return
	}

	_, err = h.saveToolStateConfig(h.configPath, cfg, revision)
	if errors.Is(err, config.ErrConfigRevisionMismatch) {
		writeCollectionError(
			w,
			http.StatusConflict,
			"config_revision_mismatch",
			"Configuration changed; reload and try again",
			-1,
			nil,
		)
		return
	}
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_save_failed",
			"Failed to save configuration",
			-1,
			nil,
		)
		return
	}

	writeCollectionJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func validateToolStateContentType(r *http.Request) error {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return errors.New("exactly one Content-Type header is required")
	}
	mediaType, _, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	return nil
}

func decodeToolStateRequest(
	w http.ResponseWriter,
	r *http.Request,
	request *toolStateRequest,
) error {
	if r.Body == nil {
		return io.EOF
	}
	decoder := json.NewDecoder(http.MaxBytesReader(
		w,
		r.Body,
		toolStateRequestMaxBytes,
	))
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	opening, isDelimiter := first.(json.Delim)
	if !isDelimiter || opening != '{' {
		return errors.New("tool state request must be an object")
	}
	seenEnabled := false
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return tokenErr
		}
		name, isString := nameToken.(string)
		if !isString {
			return errors.New("tool state field name must be a string")
		}
		if name != "enabled" {
			return fmt.Errorf("unknown tool state field %q", name)
		}
		if seenEnabled {
			return errors.New("duplicate enabled field")
		}
		seenEnabled = true
		var enabled *bool
		if decodeErr := decoder.Decode(&enabled); decodeErr != nil {
			return decodeErr
		}
		if enabled == nil {
			return errors.New("enabled must be a boolean")
		}
		request.Enabled = enabled
	}
	closingToken, err := decoder.Token()
	if err != nil {
		return err
	}
	closing, ok := closingToken.(json.Delim)
	if !ok || closing != '}' {
		return errors.New("tool state request is not a complete object")
	}
	if !seenEnabled {
		return errors.New("enabled is required")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("tool state request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func (h *Handler) handleGetToolAdaptation(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(buildToolAdaptationResponse(cfg)); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func (h *Handler) handleUpdateToolAdaptation(w http.ResponseWriter, r *http.Request) {
	var req toolAdaptationConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if req.ProfileOverrides != nil {
		if err := validateToolAdaptationProfileOverrides(*req.ProfileOverrides); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	profileOverrides := cfg.Tools.Adaptation.ProfileOverrides
	if req.ProfileOverrides != nil {
		profileOverrides = canonicalToolAdaptationProfileOverrides(*req.ProfileOverrides)
	}
	cfg.Tools.Adaptation = config.ToolAdaptationConfig{
		Enabled:                req.Enabled,
		VisibleToolSurface:     req.VisibleToolSurface,
		LearnFromToolCalls:     req.LearnFromToolCalls,
		RunModelProbes:         req.RunModelProbes,
		AllowRuntimeDowngrade:  req.AllowRuntimeDowngrade,
		AllowRuntimePromotion:  req.AllowRuntimePromotion,
		ApplyVisibleChanges:    req.ApplyVisibleChanges,
		CacheSensitiveAPIs:     req.CacheSensitiveAPIs,
		CacheBreakingDowngrade: req.CacheBreakingDowngrade,
		ProfileOverrides:       profileOverrides,
	}.Normalized()

	if _, err := config.SaveConfigIfRevision(h.configPath, cfg, revision); err != nil {
		if errors.Is(err, config.ErrConfigRevisionMismatch) {
			http.Error(w, "Configuration changed; reload and try again", http.StatusConflict)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(buildToolAdaptationResponse(cfg)); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func (h *Handler) handleRunToolAdaptationProbe(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	adaptation := cfg.Tools.Adaptation.Normalized()
	if !adaptation.Enabled {
		http.Error(w, "tool adaptation is disabled", http.StatusConflict)
		return
	}
	if !adaptation.RunModelProbes {
		http.Error(w, "tool adaptation probes are disabled", http.StatusConflict)
		return
	}

	var req toolAdaptationProbeRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&req); decodeErr != nil && decodeErr != io.EOF {
		http.Error(w, fmt.Sprintf("invalid probe request: %v", decodeErr), http.StatusBadRequest)
		return
	}
	accountRef := strings.TrimSpace(req.AccountRef)
	modelAlias := strings.TrimSpace(req.ModelAlias)
	if (accountRef == "") != (modelAlias == "") {
		http.Error(w, "probe account_ref and model_alias must be provided together", http.StatusBadRequest)
		return
	}
	if accountRef == "" {
		accountRef, modelAlias = defaultToolAdaptationProbeSelection(cfg)
	}
	if modelAlias == "" {
		http.Error(w, config.ErrNoModelConfigured.Error(), http.StatusBadRequest)
		return
	}
	if accountRef == "" {
		http.Error(w, "no account configured", http.StatusBadRequest)
		return
	}
	modelCfg, err := resolveToolAdaptationAccountAlias(cfg, accountRef, modelAlias)
	if err != nil {
		http.Error(
			w,
			fmt.Sprintf(
				"failed to resolve probe account %q with model alias %q: %v",
				accountRef,
				modelAlias,
				err,
			),
			http.StatusBadRequest,
		)
		return
	}
	if strings.TrimSpace(modelCfg.Workspace) == "" {
		modelCfg.Workspace = cfg.WorkspacePath()
	}
	executionPolicy := isolation.NewExecutionPolicy(cfg.Isolation)
	llmProvider, modelID, err := providers.CreateProviderFromConfigWithExecutionPolicy(
		modelCfg,
		executionPolicy,
	)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create probe provider: %v", err), http.StatusBadRequest)
		return
	}
	if stateful, ok := llmProvider.(providers.StatefulProvider); ok {
		defer stateful.Close()
	}
	effectiveProvider, model := providers.ExtractProtocol(modelCfg)
	providerName := providers.NormalizeProvider(effectiveProvider)
	if strings.TrimSpace(modelID) != "" {
		model = modelID
	}

	decision := picotools.ResolveToolAdaptation(adaptation, providerName, model)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result := picotools.RunToolAdaptationProbe(
		ctx,
		llmProvider,
		picotools.ToolAdaptationProfile{Provider: providerName, Model: model},
		decision.PinnedToolSurface,
		model,
	)

	w.Header().Set("Content-Type", "application/json")
	if !result.Success {
		w.WriteHeader(http.StatusBadGateway)
	}
	if err := json.NewEncoder(w).Encode(result); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func defaultToolAdaptationProbeSelection(cfg *config.Config) (string, string) {
	if cfg == nil {
		return "", ""
	}
	accountRef := strings.TrimSpace(cfg.Agents.Defaults.AccountRef)
	if router := findAccountRouterForAdaptation(cfg, accountRef); router != nil {
		accounts := accountRouterAccountsForAdaptation(router)
		if len(accounts) == 0 {
			return "", ""
		}
		accountRef = strings.TrimSpace(accounts[0])
	}
	modelAlias := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	if router := findModelRouterForAdaptation(cfg, modelAlias); router != nil {
		aliases := modelRouterModelRefsForAdaptation(router)
		if len(aliases) == 0 {
			return "", ""
		}
		modelAlias = strings.TrimSpace(aliases[0])
	}
	return accountRef, modelAlias
}

func buildToolAdaptationResponse(cfg *config.Config) toolAdaptationConfigRequest {
	adaptation := config.DefaultToolAdaptationConfig()
	if cfg != nil {
		adaptation = cfg.Tools.Adaptation.Normalized()
	}
	profileOverrides := canonicalToolAdaptationProfileOverrides(adaptation.ProfileOverrides)
	resp := toolAdaptationConfigRequest{
		Enabled:                adaptation.Enabled,
		VisibleToolSurface:     adaptation.VisibleToolSurface,
		LearnFromToolCalls:     adaptation.LearnFromToolCalls,
		RunModelProbes:         adaptation.RunModelProbes,
		AllowRuntimeDowngrade:  adaptation.AllowRuntimeDowngrade,
		AllowRuntimePromotion:  adaptation.AllowRuntimePromotion,
		ApplyVisibleChanges:    adaptation.ApplyVisibleChanges,
		CacheSensitiveAPIs:     adaptation.CacheSensitiveAPIs,
		CacheBreakingDowngrade: adaptation.CacheBreakingDowngrade,
		ProfileOverrides:       &profileOverrides,
	}
	if cfg != nil {
		provider, model := resolveToolAdaptationProfileForConfig(cfg)
		profile := picotools.ToolAdaptationProfile{Provider: provider, Model: model}
		resp.Resolved = buildToolAdaptationResolvedState(adaptation, provider, model)
		resp.Observation = resp.Resolved.cacheObservation()
		resp.Outcomes = picotools.LatestToolAdaptationToolOutcomes(profile)
		resp.Profiles = buildToolAdaptationProfiles(cfg, adaptation, profile)
	}
	return resp
}

func buildToolAdaptationResolvedState(
	adaptation config.ToolAdaptationConfig,
	provider string,
	model string,
) *toolAdaptationResolvedState {
	provider = providers.NormalizeProvider(provider)
	model = strings.TrimSpace(model)
	decision := picotools.ResolveToolAdaptation(adaptation, provider, model)
	return &toolAdaptationResolvedState{
		Provider:            provider,
		Model:               model,
		StoreID:             picotools.ToolAdaptationStoreID.String(),
		VisibleToolSurface:  decision.VisibleToolSurface,
		PinnedToolSurface:   decision.PinnedToolSurface,
		SurfaceEvidence:     decision.SurfaceEvidence,
		RuntimeDowngrade:    decision.RuntimeDowngrade,
		RuntimePromotion:    decision.RuntimePromotion,
		ApplyVisibleChanges: decision.ApplyVisibleChanges,
		CacheSensitive:      decision.CacheSensitive,
		CacheEvidence:       decision.CacheEvidence,
	}
}

func (s *toolAdaptationResolvedState) cacheObservation() *picotools.ToolAdaptationObservation {
	if s == nil {
		return nil
	}
	observation, ok := picotools.LatestToolAdaptationObservation(picotools.ToolAdaptationProfile{
		Provider: s.Provider,
		Model:    s.Model,
	})
	if !ok {
		return nil
	}
	return &observation
}

func resolveToolAdaptationProfileForConfig(
	cfg *config.Config,
) (provider string, model string) {
	if cfg == nil {
		return "", ""
	}
	modelAlias := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	if modelAlias == "" {
		return "", ""
	}
	if router := findModelRouterForAdaptation(cfg, modelAlias); router != nil {
		refs := modelRouterModelRefsForAdaptation(router)
		if len(refs) == 0 {
			return "", ""
		}
		modelAlias = refs[0]
	}

	accountRef := strings.TrimSpace(cfg.Agents.Defaults.AccountRef)
	if router := findAccountRouterForAdaptation(cfg, accountRef); router != nil {
		accounts := accountRouterAccountsForAdaptation(router)
		if len(accounts) == 0 {
			return "", ""
		}
		accountRef = accounts[0]
	}
	modelCfg, err := resolveToolAdaptationAccountAlias(cfg, accountRef, modelAlias)
	if err != nil {
		return "", ""
	}
	provider, model = providers.ExtractProtocol(modelCfg)
	return providers.NormalizeProvider(provider), strings.TrimSpace(model)
}

func buildToolAdaptationProfiles(
	cfg *config.Config,
	adaptation config.ToolAdaptationConfig,
	defaultProfile picotools.ToolAdaptationProfile,
) []toolAdaptationProfileState {
	if cfg == nil {
		return nil
	}
	builder := toolAdaptationProfileBuilder{
		cfg:          cfg,
		adaptation:   adaptation,
		activeKeys:   map[string]struct{}{},
		overrideKeys: toolAdaptationOverrideKeys(adaptation.ProfileOverrides),
		seenProfiles: map[string]struct{}{},
	}
	builder.addActiveProfile(defaultProfile.Provider, defaultProfile.Model)
	for _, alias := range cfg.ModelAliases {
		for _, accountRef := range concreteAccountRefsForToolAdaptation(cfg) {
			modelCfg, err := resolveToolAdaptationAccountAlias(
				cfg,
				accountRef,
				alias.Name,
			)
			if err != nil {
				continue
			}
			provider, model := providers.ExtractProtocol(modelCfg)
			builder.addProfile(
				provider,
				model,
				"model alias",
				strings.TrimSpace(alias.Name),
			)
		}
	}
	for _, override := range adaptation.ProfileOverrides {
		builder.addProfile(
			override.Provider,
			override.Model,
			"manual override",
			strings.TrimSpace(override.Provider)+" / "+strings.TrimSpace(override.Model),
		)
	}
	return builder.profiles
}

func resolveToolAdaptationAccountAlias(
	cfg *config.Config,
	accountRef string,
	modelAlias string,
) (*config.ModelConfig, error) {
	if credentialID, ok := config.AccountRouterCredentialAccountID(accountRef); ok {
		provider, ok := config.AccountRouterCredentialAccountProvider(accountRef)
		if !ok {
			return nil, fmt.Errorf("credential account %q has an unsupported provider", accountRef)
		}
		provider = probeCredentialRuntimeProvider(provider)
		model, err := cfg.ResolveModelAlias(modelAlias, accountRef)
		if err != nil {
			return nil, err
		}
		model, err = providers.ResolveModelForProvider(provider, model)
		if err != nil {
			return nil, err
		}
		return &config.ModelConfig{
			ModelName:    strings.TrimSpace(accountRef),
			Provider:     providers.NormalizeProvider(provider),
			Model:        model,
			AuthMethod:   probeCredentialRuntimeAuthMethod(provider),
			CredentialID: credentialID,
			Enabled:      true,
		}, nil
	}
	return resolveConcreteAccountAliasConfig(cfg, accountRef, modelAlias)
}

func concreteAccountRefsForToolAdaptation(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	seen := make(map[string]bool)
	refs := make([]string, 0, len(cfg.ModelList))
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	for _, account := range cfg.ModelList {
		if account == nil || !account.Enabled ||
			account.IsAccountRouter() || account.IsModelRouter() {
			continue
		}
		add(account.ModelName)
	}
	for i := range cfg.AccountRouters {
		for _, ref := range accountRouterAccountsForAdaptation(&cfg.AccountRouters[i]) {
			add(ref)
		}
	}
	return refs
}

type toolAdaptationProfileBuilder struct {
	cfg          *config.Config
	adaptation   config.ToolAdaptationConfig
	activeKeys   map[string]struct{}
	overrideKeys map[string]struct{}
	seenProfiles map[string]struct{}
	profiles     []toolAdaptationProfileState
}

func (b *toolAdaptationProfileBuilder) addActiveProfile(provider string, model string) {
	b.addProfileWithActive(provider, model, "active configuration", "active", true)
}

func (b *toolAdaptationProfileBuilder) addProfile(provider string, model string, source string, label string) {
	b.addProfileWithActive(provider, model, source, label, false)
}

func (b *toolAdaptationProfileBuilder) addProfileWithActive(
	provider string,
	model string,
	source string,
	label string,
	active bool,
) {
	provider = providers.NormalizeProvider(provider)
	model = strings.TrimSpace(model)
	if provider == "" || model == "" {
		return
	}
	profile := picotools.ToolAdaptationProfile{Provider: provider, Model: model}
	key := toolAdaptationProfileKeyForAPI(provider, model)
	if active {
		b.activeKeys[key] = struct{}{}
	}
	if _, ok := b.seenProfiles[key]; ok {
		if active {
			for i := range b.profiles {
				if b.profiles[i].ID == key {
					b.profiles[i].IsDefault = true
					break
				}
			}
		}
		return
	}
	b.seenProfiles[key] = struct{}{}
	resolved := buildToolAdaptationResolvedState(b.adaptation, provider, model)
	observation := resolved.cacheObservation()
	outcomes := picotools.LatestToolAdaptationToolOutcomes(profile)
	displayLabel := strings.TrimSpace(label)
	if displayLabel == "" {
		displayLabel = strings.TrimSpace(strings.Join([]string{provider, model}, " / "))
	}
	probeAccountRef, probeModelAlias, probeConfig, probeErr := probeSelectionForProfile(
		b.cfg,
		provider,
		model,
	)
	probeAvailable := probeErr == nil && probeModelConfigReady(probeConfig)
	b.profiles = append(b.profiles, toolAdaptationProfileState{
		ID:              key,
		Label:           displayLabel,
		Source:          strings.TrimSpace(source),
		IsDefault:       active || b.isActiveKey(key),
		IsOverride:      b.isOverrideKey(key),
		ProbeAvailable:  probeAvailable,
		ProbeAccountRef: probeAccountRef,
		ProbeModelAlias: probeModelAlias,
		Resolved:        *resolved,
		Observation:     observation,
		Outcomes:        outcomes,
	})
}

func probeSelectionForProfile(
	cfg *config.Config,
	providerName string,
	model string,
) (string, string, *config.ModelConfig, error) {
	if cfg == nil {
		return "", "", nil, fmt.Errorf("config is nil")
	}
	providerName = providers.NormalizeProvider(providerName)
	model = strings.TrimSpace(model)
	var firstAccount string
	var firstAlias string
	var firstConfig *config.ModelConfig
	for _, alias := range cfg.ModelAliases {
		for _, accountRef := range concreteAccountRefsForToolAdaptation(cfg) {
			modelCfg, err := resolveToolAdaptationAccountAlias(
				cfg,
				accountRef,
				alias.Name,
			)
			if err != nil || !probeModelConfigMatches(modelCfg, providerName, model) {
				continue
			}
			if firstConfig == nil {
				firstAccount = strings.TrimSpace(accountRef)
				firstAlias = strings.TrimSpace(alias.Name)
				firstConfig = modelCfg
			}
			if probeModelConfigReady(modelCfg) {
				return strings.TrimSpace(accountRef), strings.TrimSpace(alias.Name), modelCfg, nil
			}
		}
	}
	if firstConfig != nil {
		return firstAccount, firstAlias, firstConfig, nil
	}
	return "", "", nil, fmt.Errorf(
		"no configured account and model alias resolves to profile %s/%s",
		providerName,
		model,
	)
}

func (b *toolAdaptationProfileBuilder) isActiveKey(key string) bool {
	_, ok := b.activeKeys[key]
	return ok
}

func (b *toolAdaptationProfileBuilder) isOverrideKey(key string) bool {
	_, ok := b.overrideKeys[key]
	return ok
}

func toolAdaptationOverrideKeys(
	overrides []config.ToolAdaptationProfileOverride,
) map[string]struct{} {
	keys := make(map[string]struct{}, len(overrides))
	for _, override := range overrides {
		provider := strings.TrimSpace(override.Provider)
		model := strings.TrimSpace(override.Model)
		if provider == "" || model == "" {
			continue
		}
		keys[toolAdaptationProfileKeyForAPI(provider, model)] = struct{}{}
	}
	return keys
}

func toolAdaptationProfileKeyForAPI(provider string, model string) string {
	return providers.ModelKey(provider, model)
}

func canonicalToolAdaptationProfileOverrides(
	overrides []config.ToolAdaptationProfileOverride,
) []config.ToolAdaptationProfileOverride {
	canonical := make([]config.ToolAdaptationProfileOverride, 0, len(overrides))
	indexByKey := make(map[string]int, len(overrides))
	for _, override := range overrides {
		override.Provider = providers.NormalizeProvider(override.Provider)
		override.Model = strings.TrimSpace(override.Model)
		key := providers.ModelKey(override.Provider, override.Model)
		if index, exists := indexByKey[key]; exists {
			canonical[index] = override
			continue
		}
		indexByKey[key] = len(canonical)
		canonical = append(canonical, override)
	}
	return canonical
}

func validateToolAdaptationProfileOverrides(
	overrides []config.ToolAdaptationProfileOverride,
) error {
	for i, override := range overrides {
		if strings.TrimSpace(override.Provider) == "" || strings.TrimSpace(override.Model) == "" {
			return fmt.Errorf("profile_overrides[%d] requires provider and model", i)
		}
		switch strings.ToLower(strings.TrimSpace(override.VisibleToolSurface)) {
		case "", config.ToolSurfaceAuto, config.ToolSurfaceCodex,
			config.ToolSurfacePicoClaw, config.ToolSurfaceSimple:
		default:
			return fmt.Errorf(
				"profile_overrides[%d] has invalid visible_tool_surface %q",
				i,
				override.VisibleToolSurface,
			)
		}
		switch strings.ToLower(strings.TrimSpace(override.CacheSensitiveAPIs)) {
		case "", config.ToolCacheSensitivityAuto, config.ToolCacheSensitivityNever,
			config.ToolCacheSensitivityAlways:
		default:
			return fmt.Errorf(
				"profile_overrides[%d] has invalid cache_sensitive_apis %q",
				i,
				override.CacheSensitiveAPIs,
			)
		}
	}
	return nil
}

func findAccountRouterForAdaptation(cfg *config.Config, name string) *config.AccountRouterConfig {
	name = strings.TrimSpace(name)
	for i := range cfg.AccountRouters {
		if strings.TrimSpace(cfg.AccountRouters[i].Name) == name {
			return &cfg.AccountRouters[i]
		}
	}
	return nil
}

func findModelRouterForAdaptation(cfg *config.Config, name string) *config.ModelRouterConfig {
	name = strings.TrimSpace(name)
	for i := range cfg.ModelRouters {
		if strings.TrimSpace(cfg.ModelRouters[i].Name) == name {
			return &cfg.ModelRouters[i]
		}
	}
	return nil
}

func modelConfigsByNameForAdaptation(cfg *config.Config, name string) []*config.ModelConfig {
	name = strings.TrimSpace(name)
	var matches []*config.ModelConfig
	for _, mc := range cfg.ModelList {
		if mc == nil || !mc.Enabled {
			continue
		}
		if strings.TrimSpace(mc.ModelName) == name {
			matches = append(matches, mc)
		}
	}
	return matches
}

func modelRouterModelRefsForAdaptation(router *config.ModelRouterConfig) []string {
	if router == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var refs []string
	for _, block := range router.Blocks {
		if strings.TrimSpace(block.Type) != config.ModelRouterBlockTypeModel {
			continue
		}
		ref := strings.TrimSpace(block.Model)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	return refs
}

func accountRouterAccountsForAdaptation(router *config.AccountRouterConfig) []string {
	if router == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var accounts []string
	for _, block := range router.Blocks {
		for _, account := range accountRouterBlockAccountsForAdaptation(block) {
			account = strings.TrimSpace(account)
			if account == "" {
				continue
			}
			if _, ok := seen[account]; ok {
				continue
			}
			seen[account] = struct{}{}
			accounts = append(accounts, account)
		}
	}
	return accounts
}

func accountRouterBlockAccountsForAdaptation(block config.AccountRouterBlock) []string {
	switch strings.TrimSpace(block.Type) {
	case config.AccountRouterBlockTypeAccount:
		return []string{block.Account}
	case config.AccountRouterBlockTypeLoadBalance:
		return block.Accounts
	case config.AccountRouterBlockTypeBranch:
		var accounts []string
		if block.Condition != nil {
			accounts = append(accounts, accountRouterExpressionAccountsForAdaptation(block.Condition.Left)...)
			accounts = append(accounts, accountRouterExpressionAccountsForAdaptation(block.Condition.Right)...)
		}
		return accounts
	default:
		return nil
	}
}

func accountRouterExpressionAccountsForAdaptation(expr config.AccountRouterExpression) []string {
	var accounts []string
	if account := strings.TrimSpace(expr.Account); account != "" {
		accounts = append(accounts, account)
	}
	if expr.Left != nil {
		accounts = append(accounts, accountRouterExpressionAccountsForAdaptation(*expr.Left)...)
	}
	if expr.Right != nil {
		accounts = append(accounts, accountRouterExpressionAccountsForAdaptation(*expr.Right)...)
	}
	return accounts
}

func probeModelConfigForProfile(
	cfg *config.Config,
	providerName string,
	model string,
) (*config.ModelConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	providerName = providers.NormalizeProvider(providerName)
	model = strings.TrimSpace(model)
	if providerName == "" || model == "" {
		return nil, fmt.Errorf("provider and model are required")
	}

	candidates := probeModelConfigCandidates(cfg, providerName, model)
	var firstExact *config.ModelConfig
	for _, candidate := range candidates {
		if probeModelConfigMatches(candidate, providerName, model) {
			clone := *candidate
			if firstExact == nil {
				firstExact = &clone
			}
			if probeModelConfigReady(&clone) {
				return &clone, nil
			}
		}
	}
	if firstExact != nil {
		// Keep the first exact match so the provider factory can return its
		// specific missing-credential or invalid-configuration error.
		return firstExact, nil
	}
	return nil, fmt.Errorf(
		"no configured upstream model matches profile %s/%s",
		providerName,
		model,
	)
}

func probeModelConfigCandidates(
	cfg *config.Config,
	providerName string,
	model string,
) []*config.ModelConfig {
	var candidates []*config.ModelConfig
	for _, modelCfg := range cfg.ModelList {
		if modelCfg == nil || !modelCfg.Enabled ||
			modelCfg.IsAccountRouter() || modelCfg.IsModelRouter() {
			continue
		}
		clone := *modelCfg
		candidateProvider, _ := providers.ExtractProtocol(modelCfg)
		if providers.NormalizeProvider(candidateProvider) ==
			providers.NormalizeProvider(providerName) {
			clone.Provider = candidateProvider
			clone.Model = strings.TrimSpace(model)
		}
		candidates = append(candidates, &clone)
	}
	for i := range cfg.AccountRouters {
		router := &cfg.AccountRouters[i]
		if !router.Enabled {
			continue
		}
		for _, account := range accountRouterAccountsForAdaptation(router) {
			candidates = append(
				candidates,
				probeModelConfigsForAccount(
					cfg,
					account,
					providerName,
					model,
					map[string]struct{}{},
				)...,
			)
		}
	}
	return candidates
}

func probeModelConfigsForAccount(
	cfg *config.Config,
	account string,
	providerName string,
	model string,
	visited map[string]struct{},
) []*config.ModelConfig {
	account = strings.TrimSpace(account)
	if account == "" {
		return nil
	}
	visitKey := strings.ToLower(account)
	if _, exists := visited[visitKey]; exists {
		return nil
	}
	visited[visitKey] = struct{}{}

	if credentialID, ok := config.AccountRouterCredentialAccountID(account); ok {
		provider, providerOK := config.AccountRouterCredentialAccountProvider(account)
		if !providerOK {
			return nil
		}
		provider = probeCredentialRuntimeProvider(provider)
		if providers.NormalizeProvider(provider) != providers.NormalizeProvider(providerName) {
			return nil
		}
		return []*config.ModelConfig{{
			ModelName:    account,
			Provider:     provider,
			Model:        strings.TrimSpace(model),
			AuthMethod:   probeCredentialRuntimeAuthMethod(provider),
			CredentialID: credentialID,
			Enabled:      true,
		}}
	}

	var candidates []*config.ModelConfig
	for _, modelCfg := range modelConfigsByNameForAdaptation(cfg, account) {
		if modelCfg == nil || !modelCfg.Enabled {
			continue
		}
		switch {
		case modelCfg.IsAccountRouter():
			router := modelCfg.Router
			if router == nil {
				router = findAccountRouterForAdaptation(cfg, modelCfg.ModelName)
			}
			if router == nil || !router.Enabled {
				continue
			}
			for _, nestedAccount := range accountRouterAccountsForAdaptation(router) {
				candidates = append(
					candidates,
					probeModelConfigsForAccount(
						cfg,
						nestedAccount,
						providerName,
						model,
						visited,
					)...,
				)
			}
		case modelCfg.IsModelRouter():
			continue
		default:
			clone := *modelCfg
			candidateProvider, _ := providers.ExtractProtocol(modelCfg)
			if providers.NormalizeProvider(candidateProvider) == providers.NormalizeProvider(providerName) {
				clone.Provider = candidateProvider
				clone.Model = strings.TrimSpace(model)
			}
			candidates = append(candidates, &clone)
		}
	}
	return candidates
}

func probeCredentialRuntimeProvider(provider string) string {
	switch providers.NormalizeProvider(provider) {
	case "google-antigravity":
		return "antigravity"
	case "copilot":
		return "github-copilot"
	default:
		return providers.NormalizeProvider(provider)
	}
}

func probeCredentialRuntimeAuthMethod(provider string) string {
	switch providers.NormalizeProvider(provider) {
	case "openai", "antigravity":
		return "oauth"
	default:
		return "token"
	}
}

func probeModelConfigMatches(
	modelCfg *config.ModelConfig,
	providerName string,
	model string,
) bool {
	if modelCfg == nil {
		return false
	}
	provider, modelID := providers.ExtractProtocol(modelCfg)
	return providers.NormalizeProvider(provider) == providers.NormalizeProvider(providerName) &&
		strings.EqualFold(strings.TrimSpace(modelID), strings.TrimSpace(model))
}

// probeModelConfigReady mirrors the provider factory's non-network
// prerequisites. It deliberately does not contact the provider; the probe
// request itself is responsible for reporting reachability and model support.
func probeModelConfigReady(modelCfg *config.ModelConfig) bool {
	if modelCfg == nil || !modelCfg.Enabled {
		return false
	}
	protocol, model := providers.ExtractProtocol(modelCfg)
	protocol = providers.NormalizeProvider(protocol)
	if protocol == "" || strings.TrimSpace(model) == "" ||
		protocol == config.AccountRouterProvider ||
		protocol == config.ModelRouterProvider ||
		!providers.IsSupportedModelProvider(protocol) {
		return false
	}

	authMethod := strings.ToLower(strings.TrimSpace(modelCfg.AuthMethod))
	apiKey := strings.TrimSpace(modelCfg.APIKey())
	apiBase := strings.TrimSpace(modelCfg.APIBase)
	switch protocol {
	case "openai":
		if authMethod == "oauth" || authMethod == "token" {
			return hasModelConfiguration(modelCfg)
		}
		return apiKey != "" || apiBase != ""
	case "azure":
		// Azure accepts either an explicit key or ambient Entra credentials,
		// but always requires the resource endpoint.
		return apiBase != ""
	case "anthropic":
		if authMethod == "oauth" || authMethod == "token" {
			return hasModelConfiguration(modelCfg)
		}
		return apiKey != ""
	case "anthropic-messages", "alibaba-coding-anthropic":
		return apiKey != ""
	case "gemini", "minimax":
		if authMethod == "token" && apiKey == "" {
			return hasModelConfiguration(modelCfg)
		}
		return apiKey != "" || apiBase != ""
	case "antigravity":
		return hasModelConfiguration(modelCfg)
	case "bedrock", "claude-cli", "codex-cli":
		return true
	case "github-copilot":
		if authMethod == "oauth" || authMethod == "token" {
			return hasModelConfiguration(modelCfg)
		}
		// The local bridge is validated by the actual probe.
		connectMode := strings.ToLower(strings.TrimSpace(modelCfg.ConnectMode))
		return connectMode == "" || connectMode == "grpc"
	default:
		if authMethod == "token" && apiKey == "" {
			return hasModelConfiguration(modelCfg)
		}
		return hasModelConfiguration(modelCfg) || apiBase != ""
	}
}

func buildToolSupport(cfg *config.Config) []toolSupportItem {
	items := make([]toolSupportItem, 0, len(toolCatalog))
	for _, entry := range toolCatalog {
		status := "disabled"
		reasonCode := ""

		switch entry.Name {
		case "find_skills", "install_skill":
			if cfg.Tools.IsToolEnabled(entry.ConfigKey) {
				if cfg.Tools.IsToolEnabled("skills") {
					status = "enabled"
				} else {
					status = "blocked"
					reasonCode = "requires_skills"
				}
			}
		case "spawn", "spawn_status":
			if cfg.Tools.IsToolEnabled(entry.ConfigKey) {
				if cfg.Tools.IsToolEnabled("subagent") {
					status = "enabled"
				} else {
					status = "blocked"
					reasonCode = "requires_subagent"
				}
			}
		case "tool_search_tool_regex":
			status, reasonCode = resolveDiscoveryToolSupport(cfg, cfg.Tools.MCP.Discovery.UseRegex)
		case "tool_search_tool_bm25":
			status, reasonCode = resolveDiscoveryToolSupport(cfg, cfg.Tools.MCP.Discovery.UseBM25)
		case "web_search":
			status, reasonCode = resolveWebSearchToolSupport(cfg)
		case "workflow":
			if cfg.Tools.Workflow.Enabled {
				if cfg.Workflows.Enabled {
					status = "enabled"
				} else {
					status = "blocked"
					reasonCode = "requires_workflows"
				}
			}
		case "i2c", "spi":
			status, reasonCode = resolveHardwareToolSupport(cfg.Tools.IsToolEnabled(entry.ConfigKey))
		case "serial":
			status, reasonCode = resolveSerialToolSupport(cfg.Tools.IsToolEnabled(entry.ConfigKey))
		default:
			if cfg.Tools.IsToolEnabled(entry.ConfigKey) {
				status = "enabled"
			}
		}

		items = append(items, toolSupportItem{
			ID:          toolCollectionResourceID(entry.Name),
			Name:        entry.Name,
			Description: entry.Description,
			Category:    entry.Category,
			ConfigKey:   entry.ConfigKey,
			Status:      status,
			Reason:      reasonCode,
			ReasonCode:  reasonCode,
		})
	}
	return items
}

func resolveHardwareToolSupport(enabled bool) (string, string) {
	if !enabled {
		return "disabled", ""
	}
	if runtime.GOOS != "linux" {
		return "blocked", "requires_linux"
	}
	return "enabled", ""
}

func resolveSerialToolSupport(enabled bool) (string, string) {
	if !enabled {
		return "disabled", ""
	}
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		return "enabled", ""
	default:
		return "blocked", "requires_serial_platform"
	}
}

func resolveDiscoveryToolSupport(cfg *config.Config, methodEnabled bool) (string, string) {
	if !cfg.Tools.IsToolEnabled("mcp") {
		return "disabled", ""
	}
	if !cfg.Tools.MCP.Discovery.Enabled {
		return "blocked", "requires_mcp_discovery"
	}
	if !methodEnabled {
		return "disabled", ""
	}
	return "enabled", ""
}

func resolveWebSearchToolSupport(cfg *config.Config) (string, string) {
	if !cfg.Tools.IsToolEnabled("web") {
		return "disabled", ""
	}
	return "enabled", ""
}

func applyToolState(cfg *config.Config, toolName string, enabled bool) error {
	switch toolName {
	case "read_file":
		cfg.Tools.ReadFile.Enabled = enabled
	case "write_file":
		cfg.Tools.WriteFile.Enabled = enabled
	case "list_dir":
		cfg.Tools.ListDir.Enabled = enabled
	case "edit_file":
		cfg.Tools.EditFile.Enabled = enabled
	case "append_file":
		cfg.Tools.AppendFile.Enabled = enabled
	case "exec":
		cfg.Tools.Exec.Enabled = enabled
	case "cron":
		cfg.Tools.Cron.Enabled = enabled
	case "web_search":
		cfg.Tools.Web.Enabled = enabled
	case "web_fetch":
		cfg.Tools.WebFetch.Enabled = enabled
	case "git_workspace":
		cfg.Tools.GitWorkspace.Enabled = enabled
	case "message":
		cfg.Tools.Message.Enabled = enabled
	case "send_file":
		cfg.Tools.SendFile.Enabled = enabled
	case "find_skills":
		cfg.Tools.FindSkills.Enabled = enabled
		if enabled {
			cfg.Tools.Skills.Enabled = true
		}
	case "install_skill":
		cfg.Tools.InstallSkill.Enabled = enabled
		if enabled {
			cfg.Tools.Skills.Enabled = true
		}
	case "spawn":
		cfg.Tools.Spawn.Enabled = enabled
		if enabled {
			cfg.Tools.Subagent.Enabled = true
		}
	case "spawn_status":
		cfg.Tools.SpawnStatus.Enabled = enabled
		if enabled {
			cfg.Tools.Spawn.Enabled = true
			cfg.Tools.Subagent.Enabled = true
		}
	case "threads":
		cfg.Tools.Threads.Enabled = enabled
	case "workflow":
		cfg.Tools.Workflow.Enabled = enabled
	case "i2c":
		cfg.Tools.I2C.Enabled = enabled
	case "spi":
		cfg.Tools.SPI.Enabled = enabled
	case "serial":
		cfg.Tools.Serial.Enabled = enabled
	case "tool_search_tool_regex":
		cfg.Tools.MCP.Discovery.UseRegex = enabled
		if enabled {
			cfg.Tools.MCP.Enabled = true
			cfg.Tools.MCP.Discovery.Enabled = true
		}
	case "tool_search_tool_bm25":
		cfg.Tools.MCP.Discovery.UseBM25 = enabled
		if enabled {
			cfg.Tools.MCP.Enabled = true
			cfg.Tools.MCP.Discovery.Enabled = true
		}
	default:
		return fmt.Errorf("tool %q cannot be updated", toolName)
	}
	return nil
}

func (h *Handler) handleGetWebSearchConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(buildWebSearchConfigResponse(cfg)); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func (h *Handler) handleUpdateWebSearchConfig(w http.ResponseWriter, r *http.Request) {
	var req webSearchConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	provider := normalizeWebSearchProvider(req.Provider)
	if provider == "" {
		http.Error(w, "invalid web search provider", http.StatusBadRequest)
		return
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	cfg.Tools.Web.Provider = provider
	cfg.Tools.Web.PreferNative = req.PreferNative
	cfg.Tools.Web.Proxy = strings.TrimSpace(req.Proxy)

	if settings, ok := req.Settings["sogou"]; ok {
		cfg.Tools.Web.Sogou.Enabled = settings.Enabled
		cfg.Tools.Web.Sogou.MaxResults = settings.MaxResults
	}
	if settings, ok := req.Settings["duckduckgo"]; ok {
		cfg.Tools.Web.DuckDuckGo.Enabled = settings.Enabled
		cfg.Tools.Web.DuckDuckGo.MaxResults = settings.MaxResults
	}
	if settings, ok := req.Settings["gemini"]; ok {
		cfg.Tools.Web.Gemini.Enabled = settings.Enabled
		cfg.Tools.Web.Gemini.MaxResults = settings.MaxResults
		cfg.Tools.Web.Gemini.ModelAlias = strings.TrimSpace(settings.ModelAlias)
		if key := strings.TrimSpace(settings.APIKey); key != "" {
			cfg.Tools.Web.Gemini.APIKey = *config.NewSecureString(key)
		}
	}
	if settings, ok := req.Settings["brave"]; ok {
		cfg.Tools.Web.Brave.Enabled = settings.Enabled
		cfg.Tools.Web.Brave.MaxResults = settings.MaxResults
		if keys, ok := normalizeWebSearchAPIKeys(settings.APIKeys, settings.APIKey); ok {
			cfg.Tools.Web.Brave.SetAPIKeys(keys)
		}
	}
	if settings, ok := req.Settings["tavily"]; ok {
		cfg.Tools.Web.Tavily.Enabled = settings.Enabled
		cfg.Tools.Web.Tavily.MaxResults = settings.MaxResults
		cfg.Tools.Web.Tavily.BaseURL = strings.TrimSpace(settings.BaseURL)
		if keys, ok := normalizeWebSearchAPIKeys(settings.APIKeys, settings.APIKey); ok {
			cfg.Tools.Web.Tavily.SetAPIKeys(keys)
		}
	}
	if settings, ok := req.Settings["kagi"]; ok {
		cfg.Tools.Web.Kagi.Enabled = settings.Enabled
		cfg.Tools.Web.Kagi.MaxResults = settings.MaxResults
		cfg.Tools.Web.Kagi.BaseURL = strings.TrimSpace(settings.BaseURL)
		if keys, ok := normalizeWebSearchAPIKeys(settings.APIKeys, settings.APIKey); ok {
			cfg.Tools.Web.Kagi.SetAPIKeys(keys)
		}
	}
	if settings, ok := req.Settings["perplexity"]; ok {
		cfg.Tools.Web.Perplexity.Enabled = settings.Enabled
		cfg.Tools.Web.Perplexity.MaxResults = settings.MaxResults
		cfg.Tools.Web.Perplexity.ModelAlias = strings.TrimSpace(settings.ModelAlias)
		if keys, ok := normalizeWebSearchAPIKeys(settings.APIKeys, settings.APIKey); ok {
			cfg.Tools.Web.Perplexity.APIKeys = config.SimpleSecureStrings(keys...)
		}
	}
	if settings, ok := req.Settings["searxng"]; ok {
		cfg.Tools.Web.SearXNG.Enabled = settings.Enabled
		cfg.Tools.Web.SearXNG.MaxResults = settings.MaxResults
		cfg.Tools.Web.SearXNG.BaseURL = strings.TrimSpace(settings.BaseURL)
	}
	if settings, ok := req.Settings["glm_search"]; ok {
		cfg.Tools.Web.GLMSearch.Enabled = settings.Enabled
		cfg.Tools.Web.GLMSearch.MaxResults = settings.MaxResults
		cfg.Tools.Web.GLMSearch.BaseURL = strings.TrimSpace(settings.BaseURL)
		if key := strings.TrimSpace(settings.APIKey); key != "" {
			cfg.Tools.Web.GLMSearch.APIKey = *config.NewSecureString(key)
		}
	}
	if settings, ok := req.Settings["baidu_search"]; ok {
		cfg.Tools.Web.BaiduSearch.Enabled = settings.Enabled
		cfg.Tools.Web.BaiduSearch.MaxResults = settings.MaxResults
		cfg.Tools.Web.BaiduSearch.BaseURL = strings.TrimSpace(settings.BaseURL)
		if key := strings.TrimSpace(settings.APIKey); key != "" {
			cfg.Tools.Web.BaiduSearch.APIKey = *config.NewSecureString(key)
		}
	}

	if err := cfg.ValidateModelSelections(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := config.SaveConfigIfRevision(h.configPath, cfg, revision); err != nil {
		if errors.Is(err, config.ErrConfigRevisionMismatch) {
			http.Error(w, "Configuration changed; reload and try again", http.StatusConflict)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(buildWebSearchConfigResponse(cfg)); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func (h *Handler) handleGetThreadPolicy(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	policy := normalizedThreadPolicy(cfg.Tools.Threads.Policy)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(policy); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func (h *Handler) handleUpdateThreadPolicy(w http.ResponseWriter, r *http.Request) {
	var req threadPolicyRequest
	if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", decodeErr), http.StatusBadRequest)
		return
	}

	mode, err := normalizeThreadPolicyMode(req.Mode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rules := config.NormalizeThreadPolicyRules(req.Rules)
	for _, rule := range rules {
		if strings.TrimSpace(rule.Description) == "" {
			http.Error(w, "thread policy rule description is required", http.StatusBadRequest)
			return
		}
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	cfg.Tools.Threads.Policy = config.ThreadPolicyConfig{
		Enabled:      req.Enabled,
		Mode:         mode,
		Instructions: strings.TrimSpace(req.Instructions),
		Rules:        rules,
		Agents:       normalizeThreadAgentPolicies(req.Agents),
	}

	if _, err := config.SaveConfigIfRevision(h.configPath, cfg, revision); err != nil {
		if errors.Is(err, config.ErrConfigRevisionMismatch) {
			http.Error(w, "Configuration changed; reload and try again", http.StatusConflict)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	policy := normalizedThreadPolicy(cfg.Tools.Threads.Policy)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(policy); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}

func normalizedThreadPolicy(policy config.ThreadPolicyConfig) config.ThreadPolicyConfig {
	if policy.Mode == "" {
		policy.Mode = config.ThreadPolicyModeTool
	}
	policy.Mode = policy.EffectiveMode()
	policy.Instructions = strings.TrimSpace(policy.Instructions)
	policy.Rules = config.NormalizeThreadPolicyRules(policy.Rules)
	if policy.Rules == nil {
		policy.Rules = []config.ThreadPolicyRule{}
	}
	policy.Agents = normalizeThreadAgentPolicies(policy.Agents)
	return policy
}

func normalizeThreadPolicyMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case config.ThreadPolicyModeAuto:
		return config.ThreadPolicyModeAuto, nil
	case "":
		return config.ThreadPolicyModeTool, nil
	case config.ThreadPolicyModeTool:
		return config.ThreadPolicyModeTool, nil
	case config.ThreadPolicyModeSuggest:
		return config.ThreadPolicyModeSuggest, nil
	case config.ThreadPolicyModeOff:
		return config.ThreadPolicyModeOff, nil
	default:
		return "", fmt.Errorf("invalid thread policy mode")
	}
}

func normalizeThreadAgentPolicies(src map[string]config.ThreadAgentPolicy) map[string]config.ThreadAgentPolicy {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]config.ThreadAgentPolicy, len(src))
	for agentID, policy := range src {
		agentID = strings.TrimSpace(agentID)
		if agentID == "" {
			continue
		}
		if strings.TrimSpace(policy.Mode) != "" {
			policy.Mode = config.NormalizeThreadPolicyMode(policy.Mode)
		}
		if strings.TrimSpace(policy.AttachStrategy) != "" {
			policy.AttachStrategy = config.NormalizeThreadAttachStrategy(policy.AttachStrategy)
		}
		out[agentID] = policy
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeWebSearchProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "auto":
		return "auto"
	case "sogou",
		"brave",
		"tavily",
		"kagi",
		"duckduckgo",
		"gemini",
		"perplexity",
		"searxng",
		"glm_search",
		"baidu_search":
		return strings.ToLower(strings.TrimSpace(provider))
	default:
		return ""
	}
}

func normalizeWebSearchAPIKeys(apiKeys []string, apiKey string) ([]string, bool) {
	if apiKeys != nil {
		keys := make([]string, 0, len(apiKeys))
		seen := make(map[string]struct{}, len(apiKeys))
		for _, key := range apiKeys {
			trimmed := strings.TrimSpace(key)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			keys = append(keys, trimmed)
		}
		return keys, true
	}

	if trimmed := strings.TrimSpace(apiKey); trimmed != "" {
		return []string{trimmed}, true
	}

	return nil, false
}

func buildWebSearchConfigResponse(cfg *config.Config) webSearchConfigResponse {
	opts := picotools.WebSearchToolOptionsFromConfig(cfg)
	current := resolveCurrentWebSearchProvider(cfg)
	settings := map[string]webSearchProviderConfig{
		"sogou": {
			Enabled:    cfg.Tools.Web.Sogou.Enabled,
			MaxResults: cfg.Tools.Web.Sogou.MaxResults,
		},
		"duckduckgo": {
			Enabled:    cfg.Tools.Web.DuckDuckGo.Enabled,
			MaxResults: cfg.Tools.Web.DuckDuckGo.MaxResults,
		},
		"gemini": {
			Enabled:    cfg.Tools.Web.Gemini.Enabled,
			MaxResults: cfg.Tools.Web.Gemini.MaxResults,
			ModelAlias: cfg.Tools.Web.Gemini.ModelAlias,
			APIKeySet:  cfg.Tools.Web.Gemini.APIKey.String() != "",
		},
		"brave": {
			Enabled:    cfg.Tools.Web.Brave.Enabled,
			MaxResults: cfg.Tools.Web.Brave.MaxResults,
			APIKeySet:  len(cfg.Tools.Web.Brave.APIKeys.Values()) > 0,
		},
		"tavily": {
			Enabled:    cfg.Tools.Web.Tavily.Enabled,
			MaxResults: cfg.Tools.Web.Tavily.MaxResults,
			BaseURL:    cfg.Tools.Web.Tavily.BaseURL,
			APIKeySet:  len(cfg.Tools.Web.Tavily.APIKeys.Values()) > 0,
		},
		"kagi": {
			Enabled:    cfg.Tools.Web.Kagi.Enabled,
			MaxResults: cfg.Tools.Web.Kagi.MaxResults,
			BaseURL:    cfg.Tools.Web.Kagi.BaseURL,
			APIKeySet:  len(cfg.Tools.Web.Kagi.APIKeys.Values()) > 0,
		},
		"perplexity": {
			Enabled:    cfg.Tools.Web.Perplexity.Enabled,
			MaxResults: cfg.Tools.Web.Perplexity.MaxResults,
			ModelAlias: cfg.Tools.Web.Perplexity.ModelAlias,
			APIKeySet:  len(cfg.Tools.Web.Perplexity.APIKeys.Values()) > 0,
		},
		"searxng": {
			Enabled:    cfg.Tools.Web.SearXNG.Enabled,
			MaxResults: cfg.Tools.Web.SearXNG.MaxResults,
			BaseURL:    cfg.Tools.Web.SearXNG.BaseURL,
		},
		"glm_search": {
			Enabled:    cfg.Tools.Web.GLMSearch.Enabled,
			MaxResults: cfg.Tools.Web.GLMSearch.MaxResults,
			BaseURL:    cfg.Tools.Web.GLMSearch.BaseURL,
			APIKeySet:  cfg.Tools.Web.GLMSearch.APIKey.String() != "",
		},
		"baidu_search": {
			Enabled:    cfg.Tools.Web.BaiduSearch.Enabled,
			MaxResults: cfg.Tools.Web.BaiduSearch.MaxResults,
			BaseURL:    cfg.Tools.Web.BaiduSearch.BaseURL,
			APIKeySet:  cfg.Tools.Web.BaiduSearch.APIKey.String() != "",
		},
	}

	providers := []webSearchProviderOption{
		{
			ID:         "auto",
			Label:      "Auto",
			Configured: current != "",
			Current: cfg.Tools.Web.Provider == "" ||
				cfg.Tools.Web.Provider == "auto",
		},
		{
			ID:         "sogou",
			Label:      "Sogou",
			Configured: picotools.WebSearchProviderReady(opts, "sogou"),
			Current:    current == "sogou",
		},
		{
			ID:         "duckduckgo",
			Label:      "DuckDuckGo",
			Configured: picotools.WebSearchProviderReady(opts, "duckduckgo"),
			Current:    current == "duckduckgo",
		},
		{
			ID:           "gemini",
			Label:        "Gemini (Google Search)",
			Configured:   picotools.WebSearchProviderReady(opts, "gemini"),
			Current:      current == "gemini",
			RequiresAuth: true,
		},
		{
			ID:           "brave",
			Label:        "Brave Search",
			Configured:   picotools.WebSearchProviderReady(opts, "brave"),
			Current:      current == "brave",
			RequiresAuth: true,
		},
		{
			ID:           "tavily",
			Label:        "Tavily",
			Configured:   picotools.WebSearchProviderReady(opts, "tavily"),
			Current:      current == "tavily",
			RequiresAuth: true,
		},
		{
			ID:           "kagi",
			Label:        "Kagi Search",
			Configured:   picotools.WebSearchProviderReady(opts, "kagi"),
			Current:      current == "kagi",
			RequiresAuth: true,
		},
		{
			ID:           "perplexity",
			Label:        "Perplexity",
			Configured:   picotools.WebSearchProviderReady(opts, "perplexity"),
			Current:      current == "perplexity",
			RequiresAuth: true,
		},
		{
			ID:         "searxng",
			Label:      "SearXNG",
			Configured: picotools.WebSearchProviderReady(opts, "searxng"),
			Current:    current == "searxng",
		},
		{
			ID:           "glm_search",
			Label:        "GLM Search",
			Configured:   picotools.WebSearchProviderReady(opts, "glm_search"),
			Current:      current == "glm_search",
			RequiresAuth: true,
		},
		{
			ID:           "baidu_search",
			Label:        "Baidu Search",
			Configured:   picotools.WebSearchProviderReady(opts, "baidu_search"),
			Current:      current == "baidu_search",
			RequiresAuth: true,
		},
	}

	provider := cfg.Tools.Web.Provider
	if provider == "" {
		provider = "auto"
	}

	return webSearchConfigResponse{
		Provider:       provider,
		CurrentService: current,
		PreferNative:   cfg.Tools.Web.PreferNative,
		Proxy:          cfg.Tools.Web.Proxy,
		Providers:      providers,
		ModelAliases:   configuredModelAliasNames(cfg),
		Settings:       settings,
	}
}

func configuredModelAliasNames(cfg *config.Config) []string {
	if cfg == nil {
		return []string{}
	}
	aliases := make([]string, 0, len(cfg.ModelAliases))
	for i := range cfg.ModelAliases {
		if name := strings.TrimSpace(cfg.ModelAliases[i].Name); name != "" {
			aliases = append(aliases, name)
		}
	}
	return aliases
}

func resolveCurrentWebSearchProvider(cfg *config.Config) string {
	if cfg == nil || !cfg.Tools.IsToolEnabled("web") {
		return ""
	}
	selected, err := picotools.ResolveWebSearchProviderName(picotools.WebSearchToolOptionsFromConfig(cfg), "")
	if err != nil {
		return ""
	}
	return selected
}
