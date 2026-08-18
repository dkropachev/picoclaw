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
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace/lifecycleflow"
	"github.com/sipeed/picoclaw/pkg/workflows"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	prLifecycleGateConfigsPath     = "/api/pr-lifecycle/gate-configs"
	prLifecycleGateRequestMaxBytes = config.MaxPRLifecycleConfigBytes + (64 << 10)
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

type prLifecycleGateConfigsResponse struct {
	GateConfigs           map[string]config.PRLifecycleGateConfig `json:"gate-configs"`
	DefaultGateConfigID   string                                  `json:"default-gate-config"`
	RepositoryAssignments map[string]string                       `json:"repository-assignments"`
	Nudge                 config.PRLifecycleNudgeConfig           `json:"nudge"`
	Scope                 config.PRLifecycleScopeConfig           `json:"scope"`
	GateCatalog           map[string]prLifecycleGateCatalogEntry  `json:"gate-catalog"`
	Flow                  lifecycleflow.Graph                     `json:"flow"`
	FlowRevision          string                                  `json:"flow-revision"`
	CatalogRevision       string                                  `json:"catalog-revision"`
	ConfigRevision        string                                  `json:"config-revision"`
	Effects               struct {
		GatewayEffect        string `json:"gateway-effect"`
		DeferredPolicyEffect string `json:"deferred-policy-effect"`
	} `json:"effects"`
}

type prLifecycleGateConfigsPutRequest struct {
	ExpectedConfigRevision string                                  `json:"expected-config-revision"`
	RequestID              string                                  `json:"request-id"`
	GateConfigs            map[string]config.PRLifecycleGateConfig `json:"gate-configs"`
	DefaultGateConfigID    string                                  `json:"default-gate-config"`
	RepositoryAssignments  map[string]string                       `json:"repository-assignments"`
	Nudge                  config.PRLifecycleNudgeConfig           `json:"nudge"`
	Scope                  config.PRLifecycleScopeConfig           `json:"scope"`
}

func (h *Handler) registerPRLifecycleGateConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+prLifecycleGateConfigsPath, h.handleGetPRLifecycleGateConfigs)
	mux.HandleFunc("PUT "+prLifecycleGateConfigsPath, h.handlePutPRLifecycleGateConfigs)
	mux.HandleFunc(prLifecycleGateConfigsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "GET, PUT")
		writePRWorkspaceAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	})
}

func (h *Handler) handleGetPRLifecycleGateConfigs(w http.ResponseWriter, r *http.Request) {
	if !validPRLifecycleGateConfigRequest(r) {
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
	writePRLifecycleGateConfigs(
		w,
		cfg.PRLifecycle.Effective(),
		revision,
		gatewayEffect,
		deferredEffect,
	)
}

func (h *Handler) handlePutPRLifecycleGateConfigs(w http.ResponseWriter, r *http.Request) {
	if !validPRLifecycleGateConfigRequest(r) || prWorkspaceMutationCrossSite(r) {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Body == nil || strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])) != "application/json" {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_content_type")
		return
	}
	var request prLifecycleGateConfigsPutRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, prLifecycleGateRequestMaxBytes))
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
	candidate := config.PRLifecycleConfig{
		GateConfigs: request.GateConfigs, DefaultGateConfigID: request.DefaultGateConfigID,
		RepositoryAssignments: request.RepositoryAssignments, Nudge: request.Nudge,
		Scope: request.Scope,
	}
	if err := candidate.Validate(); err != nil || validatePRLifecycleGateCatalogBindings(candidate) != nil {
		writePRWorkspaceAPIError(w, http.StatusUnprocessableEntity, "invalid_gate_configs")
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
	if err := candidate.ValidateAgentReferences(cfg.Agents); err != nil {
		writePRWorkspaceAPIError(w, http.StatusUnprocessableEntity, "invalid_gate_configs")
		return
	}
	if err := validatePRLifecycleGateActionWorkflows(r.Context(), candidate, cfg); err != nil {
		writePRWorkspaceAPIError(w, http.StatusUnprocessableEntity, "invalid_gate_configs")
		return
	}
	cfg.PRLifecycle = candidate
	effectiveCandidate := candidate.Effective()
	catalogRevision := prLifecycleGateConfigsCatalogRevision(effectiveCandidate)
	deferredRevision := prLifecycleDeferredPolicyRevision(effectiveCandidate)
	// Serialize the saved generation and its pending effect with gateway start
	// completion. A concurrent gateway start can then clear only the exact
	// configuration it loaded, never a newer save.
	h.prLifecycleEffectMu.Lock()
	newRevision, err := config.SaveConfigIfRevision(h.configPath, cfg, revision)
	if err == nil {
		h.prLifecyclePendingCatalog = catalogRevision
		h.prLifecyclePendingDeferred = deferredRevision
	}
	h.prLifecycleEffectMu.Unlock()
	if errors.Is(err, config.ErrConfigRevisionMismatch) {
		writePRWorkspaceAPIError(w, http.StatusConflict, "config_revision_mismatch")
		return
	}
	if err != nil {
		writePRWorkspaceAPIError(w, http.StatusInternalServerError, "configuration_save_failed")
		return
	}
	gatewayEffect, deferredEffect := h.prLifecycleEffects()
	writePRLifecycleGateConfigs(
		w,
		effectiveCandidate,
		newRevision,
		gatewayEffect,
		deferredEffect,
	)
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
	for _, gateConfig := range lifecycle.GateConfigs {
		for _, binding := range gateConfig.Bindings {
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
	for configID, gateConfig := range lifecycle.GateConfigs {
		for _, binding := range gateConfig.Bindings {
			entry, exists := known[binding.WorkflowRef+"\x00"+binding.GateRef]
			if !exists {
				return fmt.Errorf(
					"gate configuration %q references unpublished workflow gate %q %q",
					configID, binding.WorkflowRef, binding.GateRef,
				)
			}
			if binding.Action != nil && binding.Action.Type == gatetypes.GateActionDeterministic {
				if _, err := workflows.ValidateGateFieldValues(entry.Gate.Fields, binding.Action.Fields); err != nil {
					return fmt.Errorf("gate configuration %q deterministic fields: %w", configID, err)
				}
			}
		}
	}
	return nil
}

func writePRLifecycleGateConfigs(
	w http.ResponseWriter,
	lifecycle config.PRLifecycleConfig,
	configRevision string,
	gatewayEffect string,
	deferredPolicyEffect string,
) {
	flow, flowRevision := lifecycleflow.Default()
	catalogRevision := prLifecycleGateConfigsCatalogRevision(lifecycle)
	response := prLifecycleGateConfigsResponse{
		GateConfigs: lifecycle.GateConfigs, DefaultGateConfigID: lifecycle.DefaultGateConfigID,
		RepositoryAssignments: lifecycle.RepositoryAssignments, Nudge: lifecycle.Nudge,
		Scope:       lifecycle.Scope,
		GateCatalog: make(map[string]prLifecycleGateCatalogEntry), Flow: flow,
		FlowRevision: flowRevision, CatalogRevision: catalogRevision, ConfigRevision: configRevision,
	}
	response.GateCatalog = prLifecycleGateConfigCatalog(lifecycle)
	response.Effects.GatewayEffect = gatewayEffect
	response.Effects.DeferredPolicyEffect = deferredPolicyEffect
	setPRWorkspaceResponseHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

func prLifecycleGateConfigCatalog(
	lifecycle config.PRLifecycleConfig,
) map[string]prLifecycleGateCatalogEntry {
	result := make(map[string]prLifecycleGateCatalogEntry)
	catalog, err := prworkspace.PRLifecycleGateCatalog()
	if err != nil {
		return result
	}
	defaultConfig := lifecycle.GateConfigs[lifecycle.DefaultGateConfigID]
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
		for _, binding := range defaultConfig.Bindings {
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

func prLifecycleGateConfigsCatalogRevision(lifecycle config.PRLifecycleConfig) string {
	_, flowRevision := lifecycleflow.Default()
	encoded, _ := json.Marshal(struct {
		Lifecycle    config.PRLifecycleConfig               `json:"lifecycle"`
		FlowRevision string                                 `json:"flow-revision"`
		GateCatalog  map[string]prLifecycleGateCatalogEntry `json:"gate-catalog"`
	}{Lifecycle: lifecycle, FlowRevision: flowRevision, GateCatalog: prLifecycleGateConfigCatalog(lifecycle)})
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
	catalogRevision := prLifecycleGateConfigsCatalogRevision(effective)
	deferredRevision := prLifecycleDeferredPolicyRevision(effective)
	h.prLifecycleEffectMu.Lock()
	defer h.prLifecycleEffectMu.Unlock()
	h.prLifecycleAppliedDeferred = deferredRevision
	if h.prLifecyclePendingCatalog == catalogRevision {
		h.prLifecyclePendingCatalog = ""
		h.prLifecyclePendingDeferred = ""
	}
}

func prLifecycleDeferredPolicyRevision(lifecycle config.PRLifecycleConfig) string {
	used := map[string]struct{}{lifecycle.DefaultGateConfigID: {}}
	assignments := make(map[string]string, len(lifecycle.RepositoryAssignments))
	for identity, configID := range lifecycle.RepositoryAssignments {
		parts := strings.Split(identity, "|")
		canonical := strings.ToLower(strings.TrimRight(parts[0], "/") + "|" + parts[1])
		assignments[canonical] = configID
		used[configID] = struct{}{}
	}
	modes := make(map[string]config.PRLifecycleDeferredIssueMode, len(used))
	for configID := range used {
		modes[configID] = lifecycle.GateConfigs[configID].DeferredIssues.Mode
	}
	encoded, _ := json.Marshal(struct {
		Default     string                                         `json:"default-gate-config"`
		Assignments map[string]string                              `json:"repository-assignments"`
		Modes       map[string]config.PRLifecycleDeferredIssueMode `json:"deferred-issue-modes"`
	}{Default: lifecycle.DefaultGateConfigID, Assignments: assignments, Modes: modes})
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validPRLifecycleGateConfigRequest(r *http.Request) bool {
	return r != nil && r.URL != nil && r.URL.Path == prLifecycleGateConfigsPath &&
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
