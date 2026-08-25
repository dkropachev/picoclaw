package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
)

const (
	agentRequestMaxBytes = 1 << 20
)

type agentModelPolicy struct {
	Primary   string    `json:"primary"`
	Fallbacks *[]string `json:"fallbacks"`
}

type agentSubagentsPolicy struct {
	AllowAgents []string `json:"allow_agents"`
}

type agentResource struct {
	ID                string                `json:"id"`
	Name              string                `json:"name"`
	Workspace         string                `json:"workspace"`
	AccountRef        string                `json:"account_ref"`
	Model             *agentModelPolicy     `json:"model"`
	Skills            []string              `json:"skills"`
	Subagents         *agentSubagentsPolicy `json:"subagents"`
	IsDefault         bool                  `json:"is_default"`
	DefaultConfigured bool                  `json:"default_configured"`
	Implicit          bool                  `json:"implicit"`
}

type agentEffects struct {
	LauncherEffect string `json:"launcher_effect"`
	CatalogEffect  string `json:"catalog_effect"`
	GatewayEffect  string `json:"gateway_effect"`
}

type agentsCollectionResponse struct {
	Agents         []agentResource         `json:"agents"`
	Total          int                     `json:"total"`
	NextCursor     string                  `json:"next_cursor,omitempty"`
	CanonicalQuery string                  `json:"canonical_query,omitempty"`
	QuerySchema    *collectionquery.Schema `json:"query_schema,omitempty"`
	DefaultAgentID string                  `json:"default_agent_id"`
	ConfigRevision string                  `json:"config_revision"`
	Effects        agentEffects            `json:"effects"`
}

type agentItemResponse struct {
	Agent          agentResource `json:"agent"`
	DefaultAgentID string        `json:"default_agent_id"`
	ConfigRevision string        `json:"config_revision"`
	Effects        agentEffects  `json:"effects"`
}

type agentMutationRequest struct {
	ExpectedConfigRevision *string        `json:"expected_config_revision"`
	Agent                  *agentResource `json:"agent"`
}

type agentRevisionRequest struct {
	ExpectedConfigRevision *string `json:"expected_config_revision"`
}

type agentDeleteBlocker struct {
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}

type agentErrorResponse struct {
	Error    string               `json:"error"`
	Message  string               `json:"message,omitempty"`
	Blockers []agentDeleteBlocker `json:"blockers,omitempty"`
}

func (h *Handler) registerAgentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/agents", h.handleListAgents)
	mux.HandleFunc(
		"POST /api/agents",
		h.requireAgentMutationOrigin(h.handleCreateAgent),
	)
	mux.HandleFunc(
		"POST /api/agents/bulk-delete",
		h.requireAgentMutationOrigin(h.handleBulkDeleteAgents),
	)
	mux.HandleFunc("GET /api/agents/{id}", h.handleGetAgent)
	mux.HandleFunc(
		"GET /api/agents/{id}/capabilities",
		h.handleGetAgentCapabilities,
	)
	mux.HandleFunc(
		"PATCH /api/agents/{id}/capabilities",
		h.requireAgentMutationOrigin(h.handlePatchAgentCapabilities),
	)
	mux.HandleFunc("/api/agents/{id}/activity", h.handleGetAgentActivity)
	mux.HandleFunc(
		"PUT /api/agents/{id}",
		h.requireAgentMutationOrigin(h.handleUpdateAgent),
	)
	mux.HandleFunc(
		"DELETE /api/agents/{id}",
		h.requireAgentMutationOrigin(h.handleDeleteAgent),
	)
	mux.HandleFunc(
		"POST /api/agents/{id}/default",
		h.requireAgentMutationOrigin(h.handleSetDefaultAgent),
	)
}

func (h *Handler) requireAgentMutationOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
		case "", "none", "same-origin":
		default:
			writeAgentError(w, http.StatusForbidden, "cross_origin_agent_mutation", nil)
			return
		}
		if rawOrigin := strings.TrimSpace(r.Header.Get("Origin")); rawOrigin != "" {
			origin, err := url.Parse(rawOrigin)
			if err != nil || origin.Scheme == "" || origin.Host == "" ||
				!strings.EqualFold(origin.Host, mcpRequestHost(h, r)) ||
				!strings.EqualFold(origin.Scheme, mcpRequestScheme(h, r)) {
				writeAgentError(w, http.StatusForbidden, "cross_origin_agent_mutation", nil)
				return
			}
		}
		next(w, r)
	}
}

func (h *Handler) handleListAgents(w http.ResponseWriter, r *http.Request) {
	listRequest, ok := parseCollectionListRequest(w, r, agentCollectionSchema)
	if !ok {
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()

	cfg, revision, ok := h.loadCurrentAgentConfig(w)
	if !ok {
		return
	}
	releaseConfigMutation()
	response := h.buildAgentsCollectionResponse(cfg, revision)
	allResources := append([]agentResource(nil), response.Agents...)
	page, err := pageAgentResources(response.Agents, listRequest)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	response.Agents = page.Items
	response.Total = page.Total
	response.NextCursor = page.NextCursor
	response.CanonicalQuery = listRequest.Query.Canonical()
	names := make([]string, 0, len(allResources))
	workspaces := make([]string, 0, len(allResources))
	accounts := make([]string, 0, len(allResources))
	models := make([]string, 0, len(allResources))
	for _, resource := range allResources {
		names = append(names, resource.Name)
		workspaces = append(workspaces, resource.Workspace)
		accounts = append(accounts, resource.AccountRef)
		if resource.Model != nil {
			models = append(models, resource.Model.Primary)
		}
	}
	schema := collectionSchemaWithSuggestions(
		agentCollectionSchema,
		map[collectionquery.Field][]string{
			"name": names, "workspace": workspaces, "account": accounts, "model": models,
		},
	)
	response.QuerySchema = &schema
	writeAgentJSON(w, http.StatusOK, response)
}

func (h *Handler) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	id := r.PathValue("id")
	if !routing.IsCanonicalAgentID(id) {
		writeAgentError(w, http.StatusBadRequest, "invalid_agent_id", nil)
		return
	}

	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()

	cfg, revision, ok := h.loadCurrentAgentConfig(w)
	if !ok {
		return
	}
	resource, found := findAgentResource(cfg, id)
	if !found {
		writeAgentError(w, http.StatusNotFound, "agent_not_found", nil)
		return
	}
	releaseConfigMutation()
	writeAgentJSON(w, http.StatusOK, agentItemResponse{
		Agent:          resource,
		DefaultAgentID: effectiveDefaultAgentID(cfg),
		ConfigRevision: revision,
		Effects:        agentEffectsForConfig(cfg),
	})
}

func (h *Handler) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var request agentMutationRequest
	if !decodeAgentRequest(w, r, &request) {
		return
	}
	if request.ExpectedConfigRevision == nil ||
		strings.TrimSpace(*request.ExpectedConfigRevision) == "" {
		writeAgentError(w, http.StatusBadRequest, "expected_config_revision_required", nil)
		return
	}
	if request.Agent == nil {
		writeAgentError(w, http.StatusBadRequest, "agent_required", nil)
		return
	}
	next, err := agentConfigFromResource(*request.Agent)
	if err != nil {
		writeAgentValidationError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_agent",
			err,
		)
		return
	}

	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()

	cfg, currentRevision, ok := h.loadAgentConfigForUpdate(w)
	if !ok {
		return
	}
	if *request.ExpectedConfigRevision != currentRevision {
		writeAgentError(w, http.StatusConflict, "config_revision_mismatch", nil)
		return
	}
	if _, exists := findConfiguredAgent(cfg, next.ID); exists ||
		len(cfg.Agents.List) == 0 && next.ID == routing.DefaultAgentID {
		writeAgentError(w, http.StatusConflict, "agent_exists", nil)
		return
	}
	if len(cfg.Agents.List) == 0 {
		cfg.Agents.List = []config.AgentConfig{{
			ID:      routing.DefaultAgentID,
			Default: true,
		}}
	}
	cfg.Agents.List = append(cfg.Agents.List, next)
	if err = validateAgentConfiguration(cfg); err != nil {
		writeAgentValidationError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_agent",
			err,
		)
		return
	}
	revision, ok := h.saveAgentConfig(w, cfg, currentRevision)
	if !ok {
		return
	}
	releaseConfigMutation()
	writeAgentJSON(
		w,
		http.StatusCreated,
		h.buildAgentsCollectionResponse(cfg, revision),
	)
}

func (h *Handler) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !routing.IsCanonicalAgentID(id) {
		writeAgentError(w, http.StatusBadRequest, "invalid_agent_id", nil)
		return
	}
	var request agentMutationRequest
	if !decodeAgentRequest(w, r, &request) {
		return
	}
	if request.ExpectedConfigRevision == nil ||
		strings.TrimSpace(*request.ExpectedConfigRevision) == "" {
		writeAgentError(w, http.StatusBadRequest, "expected_config_revision_required", nil)
		return
	}
	if request.Agent == nil {
		writeAgentError(w, http.StatusBadRequest, "agent_required", nil)
		return
	}
	if request.Agent.ID != id {
		writeAgentError(w, http.StatusConflict, "agent_id_immutable", nil)
		return
	}
	next, err := agentConfigFromResource(*request.Agent)
	if err != nil {
		writeAgentValidationError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_agent",
			err,
		)
		return
	}

	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()

	cfg, currentRevision, ok := h.loadAgentConfigForUpdate(w)
	if !ok {
		return
	}
	if *request.ExpectedConfigRevision != currentRevision {
		writeAgentError(w, http.StatusConflict, "config_revision_mismatch", nil)
		return
	}
	index, exists := findConfiguredAgent(cfg, id)
	if !exists {
		if len(cfg.Agents.List) != 0 || id != routing.DefaultAgentID {
			writeAgentError(w, http.StatusNotFound, "agent_not_found", nil)
			return
		}
		next.Default = true
		cfg.Agents.List = []config.AgentConfig{next}
	} else {
		existing := cfg.Agents.List[index]
		next.Default = existing.Default
		next.Subagents = preserveHiddenSubagentModel(next.Subagents, existing.Subagents)
		cfg.Agents.List[index] = next
	}
	if err = validateAgentConfiguration(cfg); err != nil {
		writeAgentValidationError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_agent",
			err,
		)
		return
	}
	revision, ok := h.saveAgentConfig(w, cfg, currentRevision)
	if !ok {
		return
	}
	releaseConfigMutation()
	writeAgentJSON(
		w,
		http.StatusOK,
		h.buildAgentsCollectionResponse(cfg, revision),
	)
}

func (h *Handler) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !routing.IsCanonicalAgentID(id) {
		writeAgentError(w, http.StatusBadRequest, "invalid_agent_id", nil)
		return
	}
	var request agentRevisionRequest
	if !decodeAgentRequest(w, r, &request) {
		return
	}
	if request.ExpectedConfigRevision == nil ||
		strings.TrimSpace(*request.ExpectedConfigRevision) == "" {
		writeAgentError(w, http.StatusBadRequest, "expected_config_revision_required", nil)
		return
	}

	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()

	cfg, currentRevision, ok := h.loadAgentConfigForUpdate(w)
	if !ok {
		return
	}
	if *request.ExpectedConfigRevision != currentRevision {
		writeAgentError(w, http.StatusConflict, "config_revision_mismatch", nil)
		return
	}
	index, exists := findConfiguredAgent(cfg, id)
	if !exists {
		if len(cfg.Agents.List) == 0 && id == routing.DefaultAgentID {
			writeAgentError(w, http.StatusConflict, "implicit_agent_required", nil)
			return
		}
		writeAgentError(w, http.StatusNotFound, "agent_not_found", nil)
		return
	}
	dependencies := agentDeleteDependenciesForConfig(r.Context(), cfg)
	if blockers := agentDeleteBlockers(cfg, id, dependencies); len(blockers) > 0 {
		writeAgentError(w, http.StatusConflict, "agent_referenced", blockers)
		return
	}
	wasConfiguredDefault := cfg.Agents.List[index].Default
	cfg.Agents.List = append(cfg.Agents.List[:index], cfg.Agents.List[index+1:]...)
	if wasConfiguredDefault && len(cfg.Agents.List) > 0 {
		for agentIndex := range cfg.Agents.List {
			cfg.Agents.List[agentIndex].Default = agentIndex == 0
		}
	}
	if err := validateAgentConfiguration(cfg); err != nil {
		writeAgentValidationError(
			w,
			http.StatusUnprocessableEntity,
			"invalid_agent",
			err,
		)
		return
	}
	revision, ok := h.saveAgentConfig(w, cfg, currentRevision)
	if !ok {
		return
	}
	releaseConfigMutation()
	writeAgentJSON(
		w,
		http.StatusOK,
		h.buildAgentsCollectionResponse(cfg, revision),
	)
}

func (h *Handler) handleSetDefaultAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !routing.IsCanonicalAgentID(id) {
		writeAgentError(w, http.StatusBadRequest, "invalid_agent_id", nil)
		return
	}
	var request agentRevisionRequest
	if !decodeAgentRequest(w, r, &request) {
		return
	}
	if request.ExpectedConfigRevision == nil ||
		strings.TrimSpace(*request.ExpectedConfigRevision) == "" {
		writeAgentError(w, http.StatusBadRequest, "expected_config_revision_required", nil)
		return
	}

	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()

	cfg, currentRevision, ok := h.loadAgentConfigForUpdate(w)
	if !ok {
		return
	}
	if *request.ExpectedConfigRevision != currentRevision {
		writeAgentError(w, http.StatusConflict, "config_revision_mismatch", nil)
		return
	}
	index, exists := findConfiguredAgent(cfg, id)
	if !exists {
		if len(cfg.Agents.List) == 0 && id == routing.DefaultAgentID {
			releaseConfigMutation()
			writeAgentJSON(
				w,
				http.StatusOK,
				h.buildAgentsCollectionResponse(cfg, currentRevision),
			)
			return
		}
		writeAgentError(w, http.StatusNotFound, "agent_not_found", nil)
		return
	}
	if cfg.Agents.List[index].Default {
		releaseConfigMutation()
		writeAgentJSON(
			w,
			http.StatusOK,
			h.buildAgentsCollectionResponse(cfg, currentRevision),
		)
		return
	}
	for agentIndex := range cfg.Agents.List {
		cfg.Agents.List[agentIndex].Default = agentIndex == index
	}
	revision, ok := h.saveAgentConfig(w, cfg, currentRevision)
	if !ok {
		return
	}
	releaseConfigMutation()
	writeAgentJSON(
		w,
		http.StatusOK,
		h.buildAgentsCollectionResponse(cfg, revision),
	)
}

func (h *Handler) loadCurrentAgentConfig(
	w http.ResponseWriter,
) (*config.Config, string, bool) {
	cfg, revision, err := config.LoadCurrentConfigSnapshot(h.configPath)
	return validateLoadedAgentConfig(w, cfg, revision, err)
}

func (h *Handler) loadAgentConfigForUpdate(
	w http.ResponseWriter,
) (*config.Config, string, bool) {
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	return validateLoadedAgentConfig(w, cfg, revision, err)
}

func validateLoadedAgentConfig(
	w http.ResponseWriter,
	cfg *config.Config,
	revision string,
	err error,
) (*config.Config, string, bool) {
	if err != nil {
		writeAgentError(w, http.StatusInternalServerError, "agents_unavailable", nil)
		return nil, "", false
	}
	if err = validateAgentConfiguration(cfg); err != nil {
		writeAgentValidationError(
			w,
			http.StatusConflict,
			"invalid_agent_configuration",
			err,
		)
		return nil, "", false
	}
	return cfg, revision, true
}

func (h *Handler) saveAgentConfig(
	w http.ResponseWriter,
	cfg *config.Config,
	expectedRevision string,
) (string, bool) {
	revision, err := h.saveConfigIfRevision(h.configPath, cfg, expectedRevision)
	if errors.Is(err, config.ErrConfigRevisionMismatch) {
		writeAgentError(w, http.StatusConflict, "config_revision_mismatch", nil)
		return "", false
	}
	if err != nil {
		writeAgentError(w, http.StatusInternalServerError, "agent_save_failed", nil)
		return "", false
	}
	canonicalizeSavedAgents(cfg)
	return revision, true
}

func canonicalizeSavedAgents(cfg *config.Config) {
	if cfg == nil {
		return
	}
	encoded, err := json.Marshal(cfg.Agents)
	if err != nil {
		return
	}
	canonical := config.DefaultConfig().Agents
	if err = json.Unmarshal(encoded, &canonical); err != nil {
		return
	}
	cfg.Agents = canonical
}

func (h *Handler) buildAgentsCollectionResponse(
	cfg *config.Config,
	revision string,
) agentsCollectionResponse {
	resources := make([]agentResource, 0, len(cfg.Agents.List))
	if len(cfg.Agents.List) == 0 {
		resources = append(resources, agentResource{
			ID:                routing.DefaultAgentID,
			IsDefault:         true,
			DefaultConfigured: false,
			Implicit:          true,
		})
	} else {
		defaultID := effectiveDefaultAgentID(cfg)
		for index := range cfg.Agents.List {
			resources = append(
				resources,
				agentResourceFromConfig(cfg.Agents.List[index], defaultID),
			)
		}
	}
	return agentsCollectionResponse{
		Agents:         resources,
		DefaultAgentID: effectiveDefaultAgentID(cfg),
		ConfigRevision: revision,
		Effects:        agentEffectsForConfig(cfg),
	}
}

func agentEffectsForConfig(cfg *config.Config) agentEffects {
	currentSignature := computeGatewayRuntimeSignature(cfg)
	gateway.mu.Lock()
	bootSignature := gateway.bootConfigSignature
	runtimeStatus := gateway.runtimeStatus
	if runtimeStatus == "running" && !isCmdProcessAliveLocked(gateway.cmd) {
		runtimeStatus = "stopped"
	}
	gateway.mu.Unlock()
	gatewayEffect := "applied"
	if gatewayRestartRequiredBySignature(
		bootSignature,
		currentSignature,
		runtimeStatus,
	) {
		gatewayEffect = "restart_required"
	}
	return agentEffects{
		LauncherEffect: "applied",
		CatalogEffect:  "applied",
		GatewayEffect:  gatewayEffect,
	}
}

func effectiveDefaultAgentID(cfg *config.Config) string {
	if cfg == nil || len(cfg.Agents.List) == 0 {
		return routing.DefaultAgentID
	}
	for index := range cfg.Agents.List {
		if cfg.Agents.List[index].Default {
			return cfg.Agents.List[index].ID
		}
	}
	return cfg.Agents.List[0].ID
}

func findConfiguredAgent(cfg *config.Config, id string) (int, bool) {
	if cfg == nil {
		return -1, false
	}
	for index := range cfg.Agents.List {
		if cfg.Agents.List[index].ID == id {
			return index, true
		}
	}
	return -1, false
}

func findAgentResource(cfg *config.Config, id string) (agentResource, bool) {
	if cfg != nil && len(cfg.Agents.List) == 0 && id == routing.DefaultAgentID {
		return agentResource{
			ID:                routing.DefaultAgentID,
			IsDefault:         true,
			DefaultConfigured: false,
			Implicit:          true,
		}, true
	}
	index, found := findConfiguredAgent(cfg, id)
	if !found {
		return agentResource{}, false
	}
	return agentResourceFromConfig(
		cfg.Agents.List[index],
		effectiveDefaultAgentID(cfg),
	), true
}

func agentResourceFromConfig(
	agent config.AgentConfig,
	defaultID string,
) agentResource {
	resource := agentResource{
		ID:                agent.ID,
		Name:              agent.Name,
		Workspace:         agent.Workspace,
		AccountRef:        agent.AccountRef,
		Model:             modelPolicyFromConfig(agent.Model),
		Skills:            cloneStrings(agent.Skills),
		IsDefault:         agent.ID == defaultID,
		DefaultConfigured: agent.Default,
	}
	if agent.Subagents != nil && agent.Subagents.AllowAgents != nil {
		resource.Subagents = &agentSubagentsPolicy{
			AllowAgents: cloneStrings(agent.Subagents.AllowAgents),
		}
	}
	return resource
}

func modelPolicyFromConfig(model *config.AgentModelConfig) *agentModelPolicy {
	if model == nil {
		return nil
	}
	policy := &agentModelPolicy{Primary: model.Primary}
	if model.Fallbacks != nil {
		fallbacks := cloneStrings(model.Fallbacks)
		policy.Fallbacks = &fallbacks
	}
	return policy
}

func agentConfigFromResource(resource agentResource) (config.AgentConfig, error) {
	if !routing.IsCanonicalAgentID(resource.ID) {
		return config.AgentConfig{}, errors.New("invalid agent id")
	}
	name, err := normalizeAgentScalar(resource.Name, 256)
	if err != nil {
		return config.AgentConfig{}, err
	}
	workspace, err := normalizeAgentScalar(resource.Workspace, 16<<10)
	if err != nil {
		return config.AgentConfig{}, err
	}
	accountRef, err := normalizeAgentScalar(resource.AccountRef, 1024)
	if err != nil {
		return config.AgentConfig{}, err
	}
	model, err := modelPolicyToConfig(resource.Model)
	if err != nil {
		return config.AgentConfig{}, err
	}
	skills, err := normalizeAgentList(resource.Skills, false)
	if err != nil {
		return config.AgentConfig{}, err
	}
	var subagents *config.SubagentsConfig
	if resource.Subagents != nil {
		allowAgents, listErr := normalizeAgentList(
			resource.Subagents.AllowAgents,
			true,
		)
		if listErr != nil {
			return config.AgentConfig{}, listErr
		}
		subagents = &config.SubagentsConfig{AllowAgents: allowAgents}
	}
	return config.AgentConfig{
		ID:         resource.ID,
		Name:       name,
		Workspace:  workspace,
		AccountRef: accountRef,
		Model:      model,
		Skills:     skills,
		Subagents:  subagents,
	}, nil
}

func modelPolicyToConfig(
	policy *agentModelPolicy,
) (*config.AgentModelConfig, error) {
	if policy == nil {
		return nil, nil
	}
	primary, err := normalizeAgentScalar(policy.Primary, 1024)
	if err != nil {
		return nil, err
	}
	var fallbacks []string
	if policy.Fallbacks != nil {
		fallbacks, err = normalizeAgentList(*policy.Fallbacks, false)
		if err != nil {
			return nil, err
		}
	}
	return &config.AgentModelConfig{
		Primary:   primary,
		Fallbacks: fallbacks,
	}, nil
}

func normalizeAgentScalar(value string, maxBytes int) (string, error) {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || len(value) > maxBytes {
		return "", errors.New("invalid agent scalar")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New("invalid agent scalar")
		}
	}
	return value, nil
}

func normalizeAgentList(values []string, allowWildcard bool) ([]string, error) {
	if values == nil {
		return nil, nil
	}
	if len(values) > 1024 {
		return nil, errors.New("agent list is too long")
	}
	normalized := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		normalizedValue, err := normalizeAgentScalar(value, 1024)
		if err != nil || normalizedValue == "" {
			return nil, errors.New("agent list contains a blank or invalid value")
		}
		if !allowWildcard && normalizedValue == "*" {
			return nil, errors.New("wildcard is not allowed")
		}
		if _, duplicate := seen[normalizedValue]; duplicate {
			return nil, errors.New("agent list contains duplicate values")
		}
		seen[normalizedValue] = struct{}{}
		normalized[index] = normalizedValue
	}
	if allowWildcard {
		if _, wildcard := seen["*"]; wildcard && len(normalized) != 1 {
			return nil, errors.New("wildcard must be the only allowed agent")
		}
	}
	return normalized, nil
}

func validateAgentConfiguration(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("agent configuration is required")
	}
	if err := validateAgentDefaultsSelection(cfg); err != nil {
		return err
	}
	ids := make(map[string]struct{}, len(cfg.Agents.List)+1)
	if len(cfg.Agents.List) == 0 {
		ids[routing.DefaultAgentID] = struct{}{}
	}
	defaults := 0
	for index := range cfg.Agents.List {
		agent := &cfg.Agents.List[index]
		if !routing.IsCanonicalAgentID(agent.ID) {
			return errors.New("invalid configured agent id")
		}
		if _, duplicate := ids[agent.ID]; duplicate {
			return errors.New("duplicate configured agent id")
		}
		ids[agent.ID] = struct{}{}
		if agent.Default {
			defaults++
		}
		if err := validateConfiguredAgentValues(cfg, agent); err != nil {
			return err
		}
	}
	if defaults > 1 {
		return errors.New("multiple configured default agents")
	}
	for index := range cfg.Agents.List {
		agent := &cfg.Agents.List[index]
		if agent.Subagents == nil {
			continue
		}
		for _, target := range agent.Subagents.AllowAgents {
			if target == "*" {
				continue
			}
			if !routing.IsCanonicalAgentID(target) {
				return errors.New("invalid subagent target")
			}
			if target == agent.ID {
				return errors.New("agent cannot delegate to itself")
			}
			if _, found := ids[target]; !found {
				return errors.New("subagent target does not exist")
			}
		}
	}
	if cfg.Agents.Dispatch != nil {
		for _, rule := range cfg.Agents.Dispatch.Rules {
			if !routing.IsCanonicalAgentID(rule.Agent) {
				return errors.New("invalid dispatch agent")
			}
			if _, found := ids[rule.Agent]; !found {
				return errors.New("dispatch agent does not exist")
			}
		}
	}
	return validateConfiguredModelSelectionGraphs(cfg)
}

func validateAgentDefaultsSelection(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("agent configuration is required")
	}
	defaults := &cfg.Agents.Defaults
	if _, err := normalizeAgentScalar(defaults.AccountRef, 1024); err != nil {
		return err
	}
	if _, err := normalizeAgentScalar(defaults.ModelName, 1024); err != nil {
		return err
	}
	if _, err := normalizeAgentList(defaults.ModelFallbacks, false); err != nil {
		return err
	}
	if accountRef := strings.TrimSpace(defaults.AccountRef); accountRef != "" {
		if err := validateSelectableAccountRef(cfg, accountRef); err != nil {
			return fmt.Errorf("agents.defaults.account_ref: %w", err)
		}
	}
	if err := validateAgentModelPolicy(
		cfg,
		strings.TrimSpace(defaults.ModelName),
		defaults.ModelFallbacks,
	); err != nil {
		return fmt.Errorf("agents.defaults: %w", err)
	}
	return nil
}

func validateConfiguredAgentValues(
	cfg *config.Config,
	agent *config.AgentConfig,
) error {
	if _, err := normalizeAgentScalar(agent.Name, 256); err != nil {
		return err
	}
	if _, err := normalizeAgentScalar(agent.Workspace, 16<<10); err != nil {
		return err
	}
	if _, err := normalizeAgentScalar(agent.AccountRef, 1024); err != nil {
		return err
	}
	if accountRef := strings.TrimSpace(agent.AccountRef); accountRef != "" {
		if err := validateSelectableAccountRef(cfg, accountRef); err != nil {
			return fmt.Errorf("agent %q account_ref: %w", agent.ID, err)
		}
	}
	if agent.Model != nil {
		if _, err := normalizeAgentScalar(agent.Model.Primary, 1024); err != nil {
			return err
		}
		if _, err := normalizeAgentList(agent.Model.Fallbacks, false); err != nil {
			return err
		}
		if err := validateAgentModelPolicy(
			cfg,
			strings.TrimSpace(agent.Model.Primary),
			agent.Model.Fallbacks,
		); err != nil {
			return fmt.Errorf("agent %q model: %w", agent.ID, err)
		}
	}
	if _, err := normalizeAgentList(agent.Skills, false); err != nil {
		return err
	}
	if agent.Subagents != nil {
		if _, err := normalizeAgentList(agent.Subagents.AllowAgents, true); err != nil {
			return err
		}
		if agent.Subagents.Model != nil {
			if _, err := normalizeAgentScalar(agent.Subagents.Model.Primary, 1024); err != nil {
				return err
			}
			if _, err := normalizeAgentList(
				agent.Subagents.Model.Fallbacks,
				false,
			); err != nil {
				return err
			}
			if err := validateAgentModelPolicy(
				cfg,
				strings.TrimSpace(agent.Subagents.Model.Primary),
				agent.Subagents.Model.Fallbacks,
			); err != nil {
				return fmt.Errorf("agent %q subagents model: %w", agent.ID, err)
			}
		}
	}
	return nil
}

func validateAgentModelPolicy(
	cfg *config.Config,
	primary string,
	fallbacks []string,
) error {
	if primary != "" && !modelAliasOrRouterConfigured(cfg, primary) {
		return fmt.Errorf("model alias %q is not configured", primary)
	}
	for _, fallback := range fallbacks {
		fallback = strings.TrimSpace(fallback)
		if _, err := cfg.GetModelAlias(fallback); err != nil {
			if errors.Is(err, config.ErrNoModelConfigured) {
				return config.ErrNoModelConfigured
			}
			return fmt.Errorf("fallback model alias %q is not configured", fallback)
		}
	}
	return nil
}

func preserveHiddenSubagentModel(
	next *config.SubagentsConfig,
	existing *config.SubagentsConfig,
) *config.SubagentsConfig {
	if existing == nil || existing.Model == nil {
		return next
	}
	if next == nil {
		next = &config.SubagentsConfig{}
	}
	next.Model = cloneAgentModel(existing.Model)
	return next
}

func cloneAgentModel(model *config.AgentModelConfig) *config.AgentModelConfig {
	if model == nil {
		return nil
	}
	return &config.AgentModelConfig{
		Primary:   model.Primary,
		Fallbacks: cloneStrings(model.Fallbacks),
	}
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func agentDeleteBlockers(
	cfg *config.Config,
	targetID string,
	dependencies agentDeleteDependencies,
) []agentDeleteBlocker {
	blockers := agentConfigurationDeleteBlockers(cfg, targetID, dependencies)
	if cfg == nil {
		return blockers
	}
	for index := range cfg.Agents.List {
		agent := &cfg.Agents.List[index]
		if agent.ID == targetID || agent.Subagents == nil {
			continue
		}
		for _, allowed := range agent.Subagents.AllowAgents {
			if allowed == targetID {
				blockers = append(blockers, agentDeleteBlocker{
					Kind:    "subagent_allowlist",
					AgentID: agent.ID,
				})
				break
			}
		}
	}
	sortAgentDeleteBlockers(blockers)
	return blockers
}

type agentDeleteDependencies struct {
	workflowReferences          []prLifecycleGateActionWorkflowAgentReference
	workflowReferencesAvailable bool
}

func agentDeleteDependenciesForConfig(
	ctx context.Context,
	cfg *config.Config,
) agentDeleteDependencies {
	if cfg == nil {
		return agentDeleteDependencies{workflowReferencesAvailable: true}
	}
	references, err := prLifecycleGateActionWorkflowAgentReferences(
		ctx,
		cfg.PRLifecycle,
		cfg,
	)
	return agentDeleteDependencies{
		workflowReferences:          references,
		workflowReferencesAvailable: err == nil,
	}
}

// agentConfigurationDeleteBlockers returns references that remain regardless
// of which other agents are selected for the same bulk deletion. Agent-to-agent
// allowlists are handled separately so a selected referrer can be co-deleted.
func agentConfigurationDeleteBlockers(
	cfg *config.Config,
	targetID string,
	dependencies agentDeleteDependencies,
) []agentDeleteBlocker {
	if cfg == nil {
		return nil
	}
	blockers := make([]agentDeleteBlocker, 0)
	if cfg.Agents.Dispatch != nil {
		for _, rule := range cfg.Agents.Dispatch.Rules {
			if rule.Agent == targetID {
				blockers = append(blockers, agentDeleteBlocker{
					Kind: "dispatch_rule",
					Name: rule.Name,
				})
			}
		}
	}
	for configurationID, workflowConfiguration := range cfg.PRLifecycle.WorkflowConfigurations {
		for _, binding := range workflowConfiguration.Bindings {
			action := binding.Action
			if action == nil || string(action.Type) != "ai" || action.Session == "source" ||
				strings.TrimSpace(action.AgentID) != targetID {
				continue
			}
			blockers = append(blockers, agentDeleteBlocker{
				Kind: "pr_lifecycle_action",
				Name: strings.Join(
					[]string{configurationID, binding.WorkflowRef, binding.GateRef},
					":",
				),
			})
		}
	}
	if !dependencies.workflowReferencesAvailable {
		blockers = append(blockers, agentDeleteBlocker{
			Kind: "pr_lifecycle_workflow_unavailable",
		})
	} else {
		for _, reference := range dependencies.workflowReferences {
			if strings.TrimSpace(reference.AgentID) != targetID {
				continue
			}
			blockers = append(blockers, agentDeleteBlocker{
				Kind: "pr_lifecycle_action",
				Name: reference.WorkflowRef + ":" + reference.GateRef,
			})
		}
	}
	return blockers
}

func sortAgentDeleteBlockers(blockers []agentDeleteBlocker) {
	sort.SliceStable(blockers, func(i, j int) bool {
		if blockers[i].Kind == blockers[j].Kind {
			if blockers[i].AgentID == blockers[j].AgentID {
				return blockers[i].Name < blockers[j].Name
			}
			return blockers[i].AgentID < blockers[j].AgentID
		}
		return blockers[i].Kind < blockers[j].Kind
	})
}

func decodeAgentRequest(w http.ResponseWriter, r *http.Request, destination any) bool {
	return decodeAgentRequestWithMaxBytes(
		w,
		r,
		destination,
		agentRequestMaxBytes,
	)
}

func decodeAgentRequestWithMaxBytes(
	w http.ResponseWriter,
	r *http.Request,
	destination any,
	maxBytes int64,
) bool {
	if r.Body == nil {
		writeAgentError(w, http.StatusBadRequest, "invalid_agent_request", nil)
		return false
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeAgentError(w, http.StatusUnsupportedMediaType, "json_content_type_required", nil)
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeAgentError(w, http.StatusUnsupportedMediaType, "json_content_type_required", nil)
		return false
	}
	for name, value := range parameters {
		if !strings.EqualFold(name, "charset") ||
			!strings.EqualFold(value, "utf-8") {
			writeAgentError(w, http.StatusUnsupportedMediaType, "json_content_type_required", nil)
			return false
		}
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeAgentError(w, http.StatusRequestEntityTooLarge, "agent_request_too_large", nil)
			return false
		}
		writeAgentError(w, http.StatusBadRequest, "invalid_agent_request", nil)
		return false
	}
	if !utf8.Valid(raw) || rejectDuplicateAgentJSONKeys(raw) != nil {
		writeAgentError(w, http.StatusBadRequest, "invalid_agent_request", nil)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(destination); err != nil {
		writeAgentError(w, http.StatusBadRequest, "invalid_agent_request", nil)
		return false
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeAgentError(w, http.StatusBadRequest, "invalid_agent_request", nil)
		return false
	}
	return true
}

func rejectDuplicateAgentJSONKeys(raw []byte) error {
	return rejectDuplicateJSONKeys(raw, 32, nil)
}

type exactJSONKeySubtree func(path []string, foldedKey string) bool

func rejectDuplicateJSONKeys(
	raw []byte,
	maximumDepth int,
	exactSubtree exactJSONKeySubtree,
) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(
		decoder,
		0,
		maximumDepth,
		false,
		nil,
		exactSubtree,
	); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func consumeUniqueJSONValue(
	decoder *json.Decoder,
	depth int,
	maximumDepth int,
	exactKeys bool,
	path []string,
	exactSubtree exactJSONKeySubtree,
) error {
	if depth > maximumDepth {
		return errors.New("JSON nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, tokenErr := decoder.Token()
			if tokenErr != nil {
				return tokenErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key must be a string")
			}
			comparisonKey := key
			foldedKey := foldAgentJSONKey(key)
			if !exactKeys {
				comparisonKey = foldedKey
			}
			if _, duplicate := seen[comparisonKey]; duplicate {
				return fmt.Errorf("duplicate object key %q at %v", key, path)
			}
			seen[comparisonKey] = struct{}{}
			childExactKeys := exactKeys
			if exactSubtree != nil && exactSubtree(path, foldedKey) {
				childExactKeys = true
			}
			if err = consumeUniqueJSONValue(
				decoder,
				depth+1,
				maximumDepth,
				childExactKeys,
				append(path, key),
				exactSubtree,
			); err != nil {
				return err
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim('}') {
			return errors.New("unterminated object")
		}
	case '[':
		for decoder.More() {
			if err = consumeUniqueJSONValue(
				decoder,
				depth+1,
				maximumDepth,
				exactKeys,
				append(path, "[]"),
				exactSubtree,
			); err != nil {
				return err
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim(']') {
			return errors.New("unterminated array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

// foldAgentJSONKey mirrors encoding/json's case-insensitive object-name
// matching so differently-cased members cannot overwrite the same struct field.
func foldAgentJSONKey(key string) string {
	var folded strings.Builder
	folded.Grow(len(key))
	for _, char := range key {
		for {
			next := unicode.SimpleFold(char)
			if next <= char {
				break
			}
			char = next
		}
		folded.WriteRune(char)
	}
	return folded.String()
}

func writeAgentError(
	w http.ResponseWriter,
	status int,
	code string,
	blockers []agentDeleteBlocker,
) {
	writeAgentJSON(w, status, agentErrorResponse{
		Error:    code,
		Blockers: blockers,
	})
}

func writeAgentValidationError(
	w http.ResponseWriter,
	status int,
	code string,
	err error,
) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	writeAgentJSON(w, status, agentErrorResponse{
		Error:   code,
		Message: message,
	})
}

func writeAgentJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
