package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace/lifecycleflow"
	"github.com/sipeed/picoclaw/pkg/workflows"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	prLifecycleWorkflowConfigurationsPath = "/api/pr-lifecycle/workflow-configurations"
	prLifecycleRepositoryAssignmentsPath  = "/api/pr-lifecycle/repository-assignments"
	prLifecycleRequestMaxBytes            = config.MaxPRLifecycleConfigBytes + (64 << 10)
)

type prLifecycleGateCatalogEntry struct {
	WorkflowRef       string                `json:"workflow-ref"`
	GateRef           string                `json:"gate-ref"`
	WorkflowRevision  string                `json:"workflow-revision,omitempty"`
	SourceAISupported bool                  `json:"source-ai-supported"`
	Prompt            string                `json:"prompt"`
	Fields            []gatetypes.GateField `json:"fields,omitempty"`
	DefaultAction     *gatetypes.GateAction `json:"default-action,omitempty"`
	EffectiveAction   *gatetypes.GateAction `json:"effective-action,omitempty"`
	ActionSource      string                `json:"action-source,omitempty"`
}

type prLifecycleWorkflowConfigurationsResponse struct {
	WorkflowConfigurations         map[string]config.PRLifecycleWorkflowConfiguration `json:"workflow-configurations"`
	DefaultWorkflowConfigurationID string                                             `json:"default-workflow-configuration"`
	Nudge                          config.PRLifecycleNudgeConfig                      `json:"nudge"`
	Scope                          config.PRLifecycleScopeConfig                      `json:"scope"`
	GateCatalog                    map[string]prLifecycleGateCatalogEntry             `json:"gate-catalog"`
	Flow                           lifecycleflow.Graph                                `json:"flow"`
	FlowRevision                   string                                             `json:"flow-revision"`
	CatalogRevision                string                                             `json:"catalog-revision"`
	ConfigRevision                 string                                             `json:"config-revision"`
	Effects                        prLifecycleEffectsResponse                         `json:"effects"`
}

type prLifecycleWorkflowConfigurationsPutRequest struct {
	ExpectedConfigRevision         string                                             `json:"expected-config-revision"`
	RequestID                      string                                             `json:"request-id"`
	WorkflowConfigurations         map[string]config.PRLifecycleWorkflowConfiguration `json:"workflow-configurations"`
	DefaultWorkflowConfigurationID string                                             `json:"default-workflow-configuration"`
	Nudge                          config.PRLifecycleNudgeConfig                      `json:"nudge"`
	Scope                          config.PRLifecycleScopeConfig                      `json:"scope"`
}

type prLifecycleEffectsResponse struct {
	GatewayEffect        string `json:"gateway-effect"`
	DeferredPolicyEffect string `json:"deferred-policy-effect"`
}

type prLifecycleWorkflowConfigurationSummary struct {
	Name           string                                `json:"name"`
	DeferredIssues config.PRLifecycleDeferredIssueConfig `json:"deferred-issues"`
}

type prLifecycleRepositoryAssignmentsResponse struct {
	RepositoryAssignments          map[string]string                                  `json:"repository-assignments"`
	WorkflowConfigurations         map[string]prLifecycleWorkflowConfigurationSummary `json:"workflow-configurations"`
	DefaultWorkflowConfigurationID string                                             `json:"default-workflow-configuration"`
	ConfigRevision                 string                                             `json:"config-revision"`
	Effects                        prLifecycleEffectsResponse                         `json:"effects"`
}

type prLifecycleRepositoryAssignmentsPutRequest struct {
	ExpectedConfigRevision string            `json:"expected-config-revision"`
	RequestID              string            `json:"request-id"`
	RepositoryAssignments  map[string]string `json:"repository-assignments"`
}

func (h *Handler) registerPRLifecycleWorkflowConfigurationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/pr-lifecycle/workflow-configurations", h.handleGetPRLifecycleWorkflowConfigurations)
	mux.HandleFunc("PUT /api/pr-lifecycle/workflow-configurations", h.handlePutPRLifecycleWorkflowConfigurations)
	mux.HandleFunc(prLifecycleWorkflowConfigurationsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "GET, PUT")
		writePRWorkspaceAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	})
	mux.HandleFunc("GET /api/pr-lifecycle/repository-assignments", h.handleGetPRLifecycleRepositoryAssignments)
	mux.HandleFunc("PUT /api/pr-lifecycle/repository-assignments", h.handlePutPRLifecycleRepositoryAssignments)
	mux.HandleFunc(prLifecycleRepositoryAssignmentsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "GET, PUT")
		writePRWorkspaceAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	})
}

func (h *Handler) handleGetPRLifecycleWorkflowConfigurations(w http.ResponseWriter, r *http.Request) {
	if !validPRLifecycleWorkflowConfigurationRequest(r) {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	cfg, revision, err := config.LoadCurrentConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writePRWorkspaceAPIError(w, http.StatusInternalServerError, "configuration_unavailable")
		return
	}
	gatewayEffect, deferredEffect := h.prLifecycleEffects()
	writePRLifecycleWorkflowConfigurations(
		w,
		cfg.PRLifecycle.Effective(),
		revision,
		gatewayEffect,
		deferredEffect,
	)
}

func (h *Handler) handlePutPRLifecycleWorkflowConfigurations(w http.ResponseWriter, r *http.Request) {
	if !validPRLifecycleWorkflowConfigurationRequest(r) || prWorkspaceMutationCrossSite(r) {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Body == nil || strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])) != "application/json" {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_content_type")
		return
	}
	var request prLifecycleWorkflowConfigurationsPutRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, prLifecycleRequestMaxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || request.ExpectedConfigRevision == "" ||
		!validPRLifecycleRequestID(request.RequestID) {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	cfg, revision, err := config.LoadCurrentConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writePRWorkspaceAPIError(w, http.StatusInternalServerError, "configuration_unavailable")
		return
	}
	if revision != request.ExpectedConfigRevision {
		writePRWorkspaceAPIError(w, http.StatusConflict, "config_revision_mismatch")
		return
	}
	current := cfg.PRLifecycle.Effective()
	candidate := config.PRLifecycleConfig{
		WorkflowConfigurations:         request.WorkflowConfigurations,
		DefaultWorkflowConfigurationID: request.DefaultWorkflowConfigurationID,
		RepositoryAssignments:          current.RepositoryAssignments,
		Nudge:                          request.Nudge,
		Scope:                          request.Scope,
	}
	if err := validatePRLifecycleWorkflowConfigurations(r.Context(), candidate, cfg); err != nil {
		writePRWorkspaceAPIError(w, http.StatusUnprocessableEntity, "invalid_workflow_configurations")
		return
	}
	if prLifecycleRuntimeRevision(candidate) == prLifecycleRuntimeRevision(current) {
		candidate = current
	}
	newRevision, err := h.savePRLifecycleConfig(cfg, candidate, revision)
	if errors.Is(err, config.ErrConfigRevisionMismatch) {
		writePRWorkspaceAPIError(w, http.StatusConflict, "config_revision_mismatch")
		return
	}
	if err != nil {
		writePRWorkspaceAPIError(w, http.StatusInternalServerError, "configuration_save_failed")
		return
	}
	gatewayEffect, deferredEffect := h.prLifecycleEffects()
	writePRLifecycleWorkflowConfigurations(w, candidate, newRevision, gatewayEffect, deferredEffect)
}

func (h *Handler) handleGetPRLifecycleRepositoryAssignments(w http.ResponseWriter, r *http.Request) {
	if !validPRLifecycleScopedRequest(r, prLifecycleRepositoryAssignmentsPath) {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	cfg, revision, err := config.LoadCurrentConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writePRWorkspaceAPIError(w, http.StatusInternalServerError, "configuration_unavailable")
		return
	}
	gatewayEffect, deferredEffect := h.prLifecycleEffects()
	writePRLifecycleRepositoryAssignments(
		w, cfg.PRLifecycle.Effective(), revision, gatewayEffect, deferredEffect,
	)
}

func (h *Handler) handlePutPRLifecycleRepositoryAssignments(w http.ResponseWriter, r *http.Request) {
	if !validPRLifecycleScopedRequest(r, prLifecycleRepositoryAssignmentsPath) || prWorkspaceMutationCrossSite(r) {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Body == nil || strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])) != "application/json" {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_content_type")
		return
	}
	var request prLifecycleRepositoryAssignmentsPutRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, prLifecycleRequestMaxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || request.ExpectedConfigRevision == "" ||
		!validPRLifecycleRequestID(request.RequestID) {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	cfg, revision, err := config.LoadCurrentConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writePRWorkspaceAPIError(w, http.StatusInternalServerError, "configuration_unavailable")
		return
	}
	if revision != request.ExpectedConfigRevision {
		writePRWorkspaceAPIError(w, http.StatusConflict, "config_revision_mismatch")
		return
	}
	candidate := cfg.PRLifecycle.Effective()
	current := candidate
	candidate.RepositoryAssignments = request.RepositoryAssignments
	if err := validatePRLifecycleWorkflowConfigurations(r.Context(), candidate, cfg); err != nil {
		writePRWorkspaceAPIError(w, http.StatusUnprocessableEntity, "invalid_repository_assignments")
		return
	}
	if prLifecycleRuntimeRevision(candidate) == prLifecycleRuntimeRevision(current) {
		candidate = current
	}
	newRevision, err := h.savePRLifecycleConfig(cfg, candidate, revision)
	if errors.Is(err, config.ErrConfigRevisionMismatch) {
		writePRWorkspaceAPIError(w, http.StatusConflict, "config_revision_mismatch")
		return
	}
	if err != nil {
		writePRWorkspaceAPIError(w, http.StatusInternalServerError, "configuration_save_failed")
		return
	}
	gatewayEffect, deferredEffect := h.prLifecycleEffects()
	writePRLifecycleRepositoryAssignments(w, candidate, newRevision, gatewayEffect, deferredEffect)
}

func validatePRLifecycleWorkflowConfigurations(
	ctx context.Context,
	candidate config.PRLifecycleConfig,
	cfg *config.Config,
) error {
	if cfg == nil {
		return errors.New("configuration is unavailable")
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := validatePRLifecycleGateCatalogBindings(candidate); err != nil {
		return err
	}
	if err := candidate.ValidateAgentReferences(cfg.Agents); err != nil {
		return err
	}
	return validatePRLifecycleGateActionWorkflows(ctx, candidate, cfg)
}

func (h *Handler) savePRLifecycleConfig(
	cfg *config.Config,
	candidate config.PRLifecycleConfig,
	revision string,
) (string, error) {
	runtimeRevision := prLifecycleRuntimeRevision(candidate)
	if runtimeRevision == prLifecycleRuntimeRevision(cfg.PRLifecycle.Effective()) {
		return revision, nil
	}
	cfg.PRLifecycle = candidate
	deferredRevision := prLifecycleDeferredPolicyRevision(candidate)
	// Serialize the saved generation and its pending effect with gateway start
	// completion. A concurrent gateway start can then clear only the exact
	// configuration it loaded, never a newer save.
	h.prLifecycleEffectMu.Lock()
	newRevision, err := config.SaveConfigIfRevision(h.configPath, cfg, revision)
	if err == nil {
		h.prLifecyclePendingCatalog = runtimeRevision
		h.prLifecyclePendingDeferred = deferredRevision
	}
	h.prLifecycleEffectMu.Unlock()
	return newRevision, err
}

func validatePRLifecycleGateActionWorkflows(
	ctx context.Context,
	lifecycle config.PRLifecycleConfig,
	cfg *config.Config,
) error {
	if cfg == nil {
		return errors.New("configuration is unavailable")
	}
	validated := make(map[string]struct{})
	active := make(map[string]struct{})
	knownAgents := make(map[string]struct{}, len(cfg.Agents.List)+1)
	if len(cfg.Agents.List) == 0 {
		knownAgents["main"] = struct{}{}
	} else {
		for _, agent := range cfg.Agents.List {
			knownAgents[agent.ID] = struct{}{}
		}
	}
	var validateRef func(string, int) error
	validateRef = func(ref string, depth int) error {
		if depth > workflows.MaxGateActionWorkflowDepth {
			return fmt.Errorf("gate action workflow nesting exceeds %d", workflows.MaxGateActionWorkflowDepth)
		}
		if _, ok := validated[ref]; ok {
			return nil
		}
		if _, cycle := active[ref]; cycle {
			return fmt.Errorf("gate action workflow cycle at %q", ref)
		}
		active[ref] = struct{}{}
		defer delete(active, ref)
		workflow, err := workflows.LoadLocal(
			ctx,
			cfg.WorkspacePath(),
			ref,
			workflowLocalOptionsFromConfig(cfg)...,
		)
		if err != nil {
			return fmt.Errorf("load gate action workflow %q: %w", ref, err)
		}
		if err := workflows.Validate(workflow); err != nil {
			return fmt.Errorf("validate gate action workflow %q: %w", ref, err)
		}
		if err := workflows.ValidatePrivateGateActionWorkflow(workflow); err != nil {
			return fmt.Errorf("gate action workflow %q is not private-safe: %w", ref, err)
		}
		for gateID, gate := range workflow.Gates {
			if gate.DefaultAction == nil {
				return fmt.Errorf("gate action workflow %q gate %q has no default-action", ref, gateID)
			}
			switch gate.DefaultAction.Type {
			case gatetypes.GateActionDeterministic:
				if _, err := workflows.ValidateGateFieldValues(gate.Fields, gate.DefaultAction.Fields); err != nil {
					return fmt.Errorf("gate action workflow %q gate %q deterministic default: %w", ref, gateID, err)
				}
			case gatetypes.GateActionAI:
				if _, exists := knownAgents[gate.DefaultAction.AgentID]; !exists {
					return fmt.Errorf("gate action workflow %q gate %q selects unknown agent %q", ref, gateID, gate.DefaultAction.AgentID)
				}
			case gatetypes.GateActionWorkflow:
				if err := validateRef(gate.DefaultAction.WorkflowRef, depth+1); err != nil {
					return err
				}
			}
		}
		validated[ref] = struct{}{}
		return nil
	}
	for _, workflowConfiguration := range lifecycle.WorkflowConfigurations {
		for _, binding := range workflowConfiguration.Bindings {
			if binding.Action != nil && binding.Action.Type == gatetypes.GateActionWorkflow {
				if err := validateRef(binding.Action.WorkflowRef, 1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validatePRLifecycleGateCatalogBindings(lifecycle config.PRLifecycleConfig) error {
	catalog, err := prworkspace.PRLifecycleGateCatalog()
	if err != nil {
		return err
	}
	known := make(map[string]prworkspace.PRLifecycleGateCatalogEntry, len(catalog))
	for _, entry := range catalog {
		known[entry.WorkflowRef+"\x00"+entry.GateRef] = entry
	}
	for configurationID, workflowConfiguration := range lifecycle.WorkflowConfigurations {
		for _, binding := range workflowConfiguration.Bindings {
			entry, exists := known[binding.WorkflowRef+"\x00"+binding.GateRef]
			if !exists {
				return fmt.Errorf(
					"workflow configuration %q references unpublished workflow gate %q %q",
					configurationID, binding.WorkflowRef, binding.GateRef,
				)
			}
			if binding.Action != nil && binding.Action.Type == gatetypes.GateActionDeterministic {
				if _, err := workflows.ValidateGateFieldValues(entry.Gate.Fields, binding.Action.Fields); err != nil {
					return fmt.Errorf("workflow configuration %q deterministic fields: %w", configurationID, err)
				}
			}
		}
	}
	return nil
}

func writePRLifecycleWorkflowConfigurations(
	w http.ResponseWriter,
	lifecycle config.PRLifecycleConfig,
	configRevision string,
	gatewayEffect string,
	deferredPolicyEffect string,
) {
	flow, flowRevision := lifecycleflow.Default()
	catalogRevision := prLifecycleWorkflowConfigurationsCatalogRevision(lifecycle)
	response := prLifecycleWorkflowConfigurationsResponse{
		WorkflowConfigurations: lifecycle.WorkflowConfigurations, DefaultWorkflowConfigurationID: lifecycle.DefaultWorkflowConfigurationID,
		Nudge: lifecycle.Nudge, Scope: lifecycle.Scope,
		GateCatalog: make(map[string]prLifecycleGateCatalogEntry), Flow: flow,
		FlowRevision: flowRevision, CatalogRevision: catalogRevision, ConfigRevision: configRevision,
	}
	response.GateCatalog = prLifecycleGateCatalog(lifecycle)
	response.Effects.GatewayEffect = gatewayEffect
	response.Effects.DeferredPolicyEffect = deferredPolicyEffect
	setPRWorkspaceResponseHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

func writePRLifecycleRepositoryAssignments(
	w http.ResponseWriter,
	lifecycle config.PRLifecycleConfig,
	configRevision string,
	gatewayEffect string,
	deferredPolicyEffect string,
) {
	summaries := make(map[string]prLifecycleWorkflowConfigurationSummary, len(lifecycle.WorkflowConfigurations))
	for id, workflowConfiguration := range lifecycle.WorkflowConfigurations {
		summaries[id] = prLifecycleWorkflowConfigurationSummary{
			Name: workflowConfiguration.Name, DeferredIssues: workflowConfiguration.DeferredIssues,
		}
	}
	response := prLifecycleRepositoryAssignmentsResponse{
		RepositoryAssignments:          lifecycle.RepositoryAssignments,
		WorkflowConfigurations:         summaries,
		DefaultWorkflowConfigurationID: lifecycle.DefaultWorkflowConfigurationID,
		ConfigRevision:                 configRevision,
		Effects: prLifecycleEffectsResponse{
			GatewayEffect:        gatewayEffect,
			DeferredPolicyEffect: deferredPolicyEffect,
		},
	}
	setPRWorkspaceResponseHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

func prLifecycleGateCatalog(
	lifecycle config.PRLifecycleConfig,
) map[string]prLifecycleGateCatalogEntry {
	result := make(map[string]prLifecycleGateCatalogEntry)
	catalog, err := prworkspace.PRLifecycleGateCatalog()
	if err != nil {
		return result
	}
	defaultConfiguration := lifecycle.WorkflowConfigurations[lifecycle.DefaultWorkflowConfigurationID]
	for _, source := range catalog {
		key := source.DecisionPoint
		if _, duplicate := result[key]; duplicate {
			key += "#" + strings.TrimPrefix(source.GateRef, "gates.")
		}
		entry := prLifecycleGateCatalogEntry{
			WorkflowRef: source.WorkflowRef, GateRef: source.GateRef,
			WorkflowRevision:  source.WorkflowRevision,
			SourceAISupported: source.SourceAISupported,
			Prompt:            source.Gate.Prompt, Fields: clonePRLifecycleGateCatalogFields(source.Gate.Fields),
			DefaultAction:   clonePRLifecycleGateCatalogAction(source.Gate.DefaultAction),
			EffectiveAction: clonePRLifecycleGateCatalogAction(source.Gate.DefaultAction),
			ActionSource:    "workflow-default",
		}
		for _, binding := range defaultConfiguration.Bindings {
			if binding.WorkflowRef == source.WorkflowRef && binding.GateRef == source.GateRef &&
				binding.Action != nil {
				entry.EffectiveAction = clonePRLifecycleGateCatalogAction(binding.Action)
				entry.ActionSource = "config-override"
				break
			}
		}
		result[key] = entry
	}
	return result
}

func clonePRLifecycleGateCatalogFields(fields []gatetypes.GateField) []gatetypes.GateField {
	cloned := append([]gatetypes.GateField(nil), fields...)
	for index := range cloned {
		cloned[index].Options = append([]gatetypes.GateFieldOption(nil), cloned[index].Options...)
	}
	return cloned
}

func clonePRLifecycleGateCatalogAction(action *gatetypes.GateAction) *gatetypes.GateAction {
	if action == nil {
		return nil
	}
	cloned := *action
	if action.Fields != nil {
		cloned.Fields = make(map[string]any, len(action.Fields))
		for key, value := range action.Fields {
			cloned.Fields[key] = value
		}
	}
	return &cloned
}

func prLifecycleWorkflowConfigurationsCatalogRevision(lifecycle config.PRLifecycleConfig) string {
	lifecycle = canonicalPRLifecycleRevisionInput(lifecycle)
	_, flowRevision := lifecycleflow.Default()
	encoded, _ := json.Marshal(struct {
		WorkflowConfigurations         map[string]config.PRLifecycleWorkflowConfiguration `json:"workflow-configurations"`
		DefaultWorkflowConfigurationID string                                             `json:"default-workflow-configuration"`
		Nudge                          config.PRLifecycleNudgeConfig                      `json:"nudge"`
		Scope                          config.PRLifecycleScopeConfig                      `json:"scope"`
		FlowRevision                   string                                             `json:"flow-revision"`
		GateCatalog                    map[string]prLifecycleGateCatalogEntry             `json:"gate-catalog"`
	}{
		WorkflowConfigurations:         lifecycle.WorkflowConfigurations,
		DefaultWorkflowConfigurationID: lifecycle.DefaultWorkflowConfigurationID,
		Nudge:                          lifecycle.Nudge, Scope: lifecycle.Scope,
		FlowRevision: flowRevision, GateCatalog: prLifecycleGateCatalog(lifecycle),
	})
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func prLifecycleRuntimeRevision(lifecycle config.PRLifecycleConfig) string {
	lifecycle = canonicalPRLifecycleRevisionInput(lifecycle)
	_, flowRevision := lifecycleflow.Default()
	encoded, _ := json.Marshal(struct {
		Lifecycle    config.PRLifecycleConfig               `json:"lifecycle"`
		FlowRevision string                                 `json:"flow-revision"`
		GateCatalog  map[string]prLifecycleGateCatalogEntry `json:"gate-catalog"`
	}{Lifecycle: lifecycle, FlowRevision: flowRevision, GateCatalog: prLifecycleGateCatalog(lifecycle)})
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (h *Handler) prLifecycleEffects() (string, string) {
	h.prLifecycleEffectMu.Lock()
	defer h.prLifecycleEffectMu.Unlock()
	gatewayEffect := "applied"
	deferredEffect := "applied"
	if h.prLifecyclePendingCatalog != "" {
		gatewayEffect = "restart-required"
		if h.prLifecycleAppliedDeferred == "" ||
			h.prLifecycleAppliedDeferred != h.prLifecyclePendingDeferred {
			deferredEffect = "restart-required"
		}
	}
	return gatewayEffect, deferredEffect
}

// markPRLifecycleGatewayApplied records the exact lifecycle configuration
// loaded by a newly started gateway. A different configuration may have been
// saved while the process was starting, in which case its restart-required
// effect must remain.
func (h *Handler) markPRLifecycleGatewayApplied(lifecycle config.PRLifecycleConfig) {
	effective := lifecycle.Effective()
	runtimeRevision := prLifecycleRuntimeRevision(effective)
	deferredRevision := prLifecycleDeferredPolicyRevision(effective)
	h.prLifecycleEffectMu.Lock()
	defer h.prLifecycleEffectMu.Unlock()
	h.prLifecycleAppliedDeferred = deferredRevision
	if h.prLifecyclePendingCatalog == runtimeRevision {
		h.prLifecyclePendingCatalog = ""
		h.prLifecyclePendingDeferred = ""
	}
}

func prLifecycleDeferredPolicyRevision(lifecycle config.PRLifecycleConfig) string {
	defaultMode := lifecycle.WorkflowConfigurations[lifecycle.DefaultWorkflowConfigurationID].DeferredIssues.Mode
	assignmentModes := make(map[string]config.PRLifecycleDeferredIssueMode, len(lifecycle.RepositoryAssignments))
	for identity, configurationID := range lifecycle.RepositoryAssignments {
		parts := strings.Split(identity, "|")
		if len(parts) != 2 {
			continue
		}
		canonical, err := config.CanonicalPRLifecycleRepositoryIdentity(parts[0], parts[1])
		if err != nil {
			continue
		}
		mode := lifecycle.WorkflowConfigurations[configurationID].DeferredIssues.Mode
		if mode != defaultMode {
			assignmentModes[canonical] = mode
		}
	}
	encoded, _ := json.Marshal(struct {
		DefaultMode     config.PRLifecycleDeferredIssueMode            `json:"default-mode"`
		AssignmentModes map[string]config.PRLifecycleDeferredIssueMode `json:"repository-modes"`
	}{
		DefaultMode:     defaultMode,
		AssignmentModes: assignmentModes,
	})
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func canonicalPRLifecycleRevisionInput(lifecycle config.PRLifecycleConfig) config.PRLifecycleConfig {
	canonical := lifecycle
	canonical.WorkflowConfigurations = make(
		map[string]config.PRLifecycleWorkflowConfiguration,
		len(lifecycle.WorkflowConfigurations),
	)
	for id, workflowConfiguration := range lifecycle.WorkflowConfigurations {
		cloned := workflowConfiguration
		cloned.Bindings = append([]config.PRLifecycleGateBinding(nil), workflowConfiguration.Bindings...)
		sort.Slice(cloned.Bindings, func(left, right int) bool {
			if cloned.Bindings[left].WorkflowRef != cloned.Bindings[right].WorkflowRef {
				return cloned.Bindings[left].WorkflowRef < cloned.Bindings[right].WorkflowRef
			}
			return cloned.Bindings[left].GateRef < cloned.Bindings[right].GateRef
		})
		canonical.WorkflowConfigurations[id] = cloned
	}
	canonical.RepositoryAssignments = make(map[string]string, len(lifecycle.RepositoryAssignments))
	for identity, configurationID := range lifecycle.RepositoryAssignments {
		parts := strings.Split(identity, "|")
		if len(parts) != 2 {
			continue
		}
		canonicalIdentity, err := config.CanonicalPRLifecycleRepositoryIdentity(parts[0], parts[1])
		if err == nil {
			canonical.RepositoryAssignments[canonicalIdentity] = configurationID
		}
	}
	return canonical
}

func validPRLifecycleWorkflowConfigurationRequest(r *http.Request) bool {
	return validPRLifecycleScopedRequest(r, prLifecycleWorkflowConfigurationsPath)
}

func validPRLifecycleScopedRequest(r *http.Request, expectedPath string) bool {
	return r != nil && r.URL != nil && r.URL.Path == expectedPath &&
		r.URL.RawQuery == "" && r.URL.Fragment == "" && !r.URL.ForceQuery &&
		r.URL.EscapedPath() == r.URL.Path
}

func validPRLifecycleRequestID(value string) bool {
	if len(value) < 16 || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || strings.ContainsRune("._:-", char) {
			continue
		}
		return false
	}
	return true
}
