package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prworkspace/lifecycleflow"
)

const (
	prLifecycleGateProfilesPath    = "/api/pr-lifecycle/gate-profiles"
	prLifecycleGateRequestMaxBytes = config.MaxPRLifecycleConfigBytes + (64 << 10)
)

type prLifecycleGateProfilesResponse struct {
	GateProfiles          map[string]config.PRLifecycleGateProfile `json:"gate_profiles"`
	DefaultGateProfileID  string                                   `json:"default_gate_profile_id"`
	RepositoryAssignments map[string]string                        `json:"repository_assignments"`
	Nudge                 config.PRLifecycleNudgeConfig            `json:"nudge"`
	Scope                 config.PRLifecycleScopeConfig            `json:"scope"`
	DeferredIssues        config.PRLifecycleDeferredIssueConfig    `json:"deferred_issues"`
	Flow                  lifecycleflow.Graph                      `json:"flow"`
	FlowRevision          string                                   `json:"flow_revision"`
	CatalogRevision       string                                   `json:"catalog_revision"`
	ConfigRevision        string                                   `json:"config_revision"`
	Effects               struct {
		GatewayEffect string `json:"gateway_effect"`
	} `json:"effects"`
}

type prLifecycleGateProfilesPutRequest struct {
	ExpectedConfigRevision string                                   `json:"expected_config_revision"`
	RequestID              string                                   `json:"request_id"`
	GateProfiles           map[string]config.PRLifecycleGateProfile `json:"gate_profiles"`
	DefaultGateProfileID   string                                   `json:"default_gate_profile_id"`
	RepositoryAssignments  map[string]string                        `json:"repository_assignments"`
	Nudge                  config.PRLifecycleNudgeConfig            `json:"nudge"`
	Scope                  config.PRLifecycleScopeConfig            `json:"scope"`
	DeferredIssues         config.PRLifecycleDeferredIssueConfig    `json:"deferred_issues"`
}

func (h *Handler) registerPRLifecycleGateProfileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+prLifecycleGateProfilesPath, h.handleGetPRLifecycleGateProfiles)
	mux.HandleFunc("PUT "+prLifecycleGateProfilesPath, h.handlePutPRLifecycleGateProfiles)
	mux.HandleFunc(prLifecycleGateProfilesPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "GET, PUT")
		writePRWorkspaceAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	})
}

func (h *Handler) handleGetPRLifecycleGateProfiles(w http.ResponseWriter, r *http.Request) {
	if !validPRLifecycleSettingsRequest(r) {
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
	writePRLifecycleGateProfiles(
		w,
		cfg.PRLifecycle.Effective(),
		revision,
		h.prLifecycleGatewayEffect(),
	)
}

func (h *Handler) handlePutPRLifecycleGateProfiles(w http.ResponseWriter, r *http.Request) {
	if !validPRLifecycleSettingsRequest(r) || prWorkspaceMutationCrossSite(r) {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if r.Body == nil || strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])) != "application/json" {
		writePRWorkspaceAPIError(w, http.StatusBadRequest, "invalid_content_type")
		return
	}
	var request prLifecycleGateProfilesPutRequest
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
		GateProfiles: request.GateProfiles, DefaultGateProfileID: request.DefaultGateProfileID,
		RepositoryAssignments: request.RepositoryAssignments, Nudge: request.Nudge,
		Scope: request.Scope, DeferredIssues: request.DeferredIssues,
	}
	if err := candidate.Validate(); err != nil || !prLifecycleGateProfilesUseKnownDecisionPoints(candidate) {
		writePRWorkspaceAPIError(w, http.StatusUnprocessableEntity, "invalid_gate_profiles")
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
		writePRWorkspaceAPIError(w, http.StatusUnprocessableEntity, "invalid_gate_profiles")
		return
	}
	cfg.PRLifecycle = candidate
	effectiveCandidate := candidate.Effective()
	catalogRevision := prLifecycleGateProfilesCatalogRevision(effectiveCandidate)
	// Serialize the saved generation and its pending effect with gateway start
	// completion. A concurrent gateway start can then clear only the exact
	// catalog it loaded, never a newer save.
	h.prLifecycleEffectMu.Lock()
	newRevision, err := config.SaveConfigIfRevision(h.configPath, cfg, revision)
	if err == nil {
		h.prLifecyclePendingCatalog = catalogRevision
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
	writePRLifecycleGateProfiles(
		w,
		effectiveCandidate,
		newRevision,
		h.prLifecycleGatewayEffect(),
	)
}

func prLifecycleGateProfilesUseKnownDecisionPoints(lifecycle config.PRLifecycleConfig) bool {
	for _, profile := range lifecycle.GateProfiles {
		for decisionPoint := range profile.Workflows {
			if !lifecycleflow.IsKnownDecisionPoint(decisionPoint) {
				return false
			}
		}
	}
	return true
}

func writePRLifecycleGateProfiles(
	w http.ResponseWriter,
	lifecycle config.PRLifecycleConfig,
	configRevision string,
	gatewayEffect string,
) {
	flow, flowRevision := lifecycleflow.Default()
	catalogRevision := prLifecycleGateProfilesCatalogRevision(lifecycle)
	response := prLifecycleGateProfilesResponse{
		GateProfiles: lifecycle.GateProfiles, DefaultGateProfileID: lifecycle.DefaultGateProfileID,
		RepositoryAssignments: lifecycle.RepositoryAssignments, Nudge: lifecycle.Nudge,
		Scope: lifecycle.Scope, DeferredIssues: lifecycle.DeferredIssues, Flow: flow,
		FlowRevision:    flowRevision,
		CatalogRevision: catalogRevision,
		ConfigRevision:  configRevision,
	}
	response.Effects.GatewayEffect = gatewayEffect
	setPRWorkspaceResponseHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

func prLifecycleGateProfilesCatalogRevision(lifecycle config.PRLifecycleConfig) string {
	_, flowRevision := lifecycleflow.Default()
	encoded, _ := json.Marshal(struct {
		Lifecycle    config.PRLifecycleConfig `json:"lifecycle"`
		FlowRevision string                   `json:"flow_revision"`
	}{Lifecycle: lifecycle, FlowRevision: flowRevision})
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (h *Handler) prLifecycleGatewayEffect() string {
	h.prLifecycleEffectMu.Lock()
	defer h.prLifecycleEffectMu.Unlock()
	if h.prLifecyclePendingCatalog != "" {
		return "restart_required"
	}
	return "applied"
}

// markPRLifecycleGatewayApplied records the exact lifecycle catalog loaded by
// a newly started gateway. A different catalog may have been saved while the
// process was starting, in which case its restart-required effect must remain.
func (h *Handler) markPRLifecycleGatewayApplied(lifecycle config.PRLifecycleConfig) {
	catalogRevision := prLifecycleGateProfilesCatalogRevision(lifecycle.Effective())
	h.prLifecycleEffectMu.Lock()
	defer h.prLifecycleEffectMu.Unlock()
	if h.prLifecyclePendingCatalog == catalogRevision {
		h.prLifecyclePendingCatalog = ""
	}
}

func validPRLifecycleSettingsRequest(r *http.Request) bool {
	return r != nil && r.URL != nil && r.URL.Path == prLifecycleGateProfilesPath &&
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
