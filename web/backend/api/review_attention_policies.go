package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	reviewAttentionPoliciesPath                  = "/api/reviews/attention-policies"
	reviewAttentionPolicyRequestEnvelopeMaxBytes = 64 << 10
	reviewAttentionPolicyRequestMaxBytes         = gatetypes.MaxGatePolicyCatalogBytes + reviewAttentionPolicyRequestEnvelopeMaxBytes
	reviewAttentionPoliciesUnavailable           = "attention_policies_unavailable"
	reviewAttentionPolicyInvalidRequest          = "invalid_attention_policy_request"
	reviewAttentionPoliciesInvalid               = "invalid_attention_policies"
	reviewAttentionPolicySaveFailed              = "attention_policy_save_failed"
	reviewAttentionConfigRevisionMismatch        = "config_revision_mismatch"
	reviewAttentionExpectedRevisionRequired      = "expected_config_revision_required"
)

type reviewAttentionPolicyEffects struct {
	GatewayEffect string `json:"gateway_effect"`
}

type reviewAttentionPoliciesResponse struct {
	config.ReviewAttentionConfig
	CatalogRevision string                       `json:"catalog_revision"`
	ConfigRevision  string                       `json:"config_revision"`
	Effects         reviewAttentionPolicyEffects `json:"effects"`
}

type reviewAttentionPoliciesPutRequest struct {
	ExpectedConfigRevision *string `json:"expected_config_revision"`
	config.ReviewAttentionConfig
}

func (h *Handler) handleGetReviewAttentionPolicies(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r == nil || r.Method != http.MethodGet {
		h.handleReviewAttentionPoliciesMethodNotAllowed(w, r)
		return
	}
	if !validReviewAttentionPoliciesRequest(r) {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionPolicyInvalidRequest,
		)
		return
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeReviewAPIError(
			w,
			http.StatusInternalServerError,
			reviewAttentionPoliciesUnavailable,
		)
		return
	}
	source, err := validateReviewAttentionPolicyConfiguration(cfg)
	if err != nil {
		writeReviewAPIError(
			w,
			http.StatusInternalServerError,
			reviewAttentionPoliciesUnavailable,
		)
		return
	}
	writeReviewAttentionPoliciesJSON(w, cfg, source, revision)
}

func (h *Handler) handlePutReviewAttentionPolicies(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r == nil || r.Method != http.MethodPut {
		h.handleReviewAttentionPoliciesMethodNotAllowed(w, r)
		return
	}
	if !validReviewAttentionPoliciesRequest(r) {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionPolicyInvalidRequest,
		)
		return
	}
	if reviewMutationCrossSite(r) {
		writeReviewAPIError(
			w,
			http.StatusForbidden,
			reviewAttentionPolicyInvalidRequest,
		)
		return
	}

	var request reviewAttentionPoliciesPutRequest
	if !decodeReviewAttentionPolicyRequest(w, r, &request) {
		return
	}
	if request.ExpectedConfigRevision == nil ||
		strings.TrimSpace(*request.ExpectedConfigRevision) == "" {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionExpectedRevisionRequired,
		)
		return
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()

	cfg, currentRevision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeReviewAPIError(
			w,
			http.StatusInternalServerError,
			reviewAttentionPoliciesUnavailable,
		)
		return
	}
	if *request.ExpectedConfigRevision != currentRevision {
		writeReviewAPIError(
			w,
			http.StatusConflict,
			reviewAttentionConfigRevisionMismatch,
		)
		return
	}

	cfg.Reviews.Attention = request.ReviewAttentionConfig
	source, err := validateReviewAttentionPolicyConfiguration(cfg)
	if err != nil {
		writeReviewAPIError(
			w,
			http.StatusUnprocessableEntity,
			reviewAttentionPoliciesInvalid,
		)
		return
	}

	revision, err := h.saveConfigIfRevision(
		h.configPath,
		cfg,
		currentRevision,
	)
	if errors.Is(err, config.ErrConfigRevisionMismatch) {
		writeReviewAPIError(
			w,
			http.StatusConflict,
			reviewAttentionConfigRevisionMismatch,
		)
		return
	}
	if err != nil {
		writeReviewAPIError(
			w,
			http.StatusInternalServerError,
			reviewAttentionPolicySaveFailed,
		)
		return
	}
	writeReviewAttentionPoliciesJSON(w, cfg, source, revision)
}

func (h *Handler) handleReviewAttentionPoliciesMethodNotAllowed(
	w http.ResponseWriter,
	_ *http.Request,
) {
	w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
	writeReviewAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func validReviewAttentionPoliciesRequest(r *http.Request) bool {
	return r != nil &&
		canonicalReviewRequestPath(r) &&
		r.URL.Path == reviewAttentionPoliciesPath &&
		r.URL.RawQuery == ""
}

func decodeReviewAttentionPolicyRequest(
	w http.ResponseWriter,
	r *http.Request,
	destination any,
) bool {
	if r == nil || r.Body == nil {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionPolicyInvalidRequest,
		)
		return false
	}
	if err := validateEventReplayHeaders(r.Header); err != nil {
		writeReviewAPIError(
			w,
			http.StatusUnsupportedMediaType,
			reviewAttentionPolicyInvalidRequest,
		)
		return false
	}
	if r.ContentLength > reviewAttentionPolicyRequestMaxBytes {
		writeReviewAPIError(
			w,
			http.StatusRequestEntityTooLarge,
			reviewAttentionPolicyInvalidRequest,
		)
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(
		w,
		r.Body,
		reviewAttentionPolicyRequestMaxBytes,
	))
	if err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeReviewAPIError(
				w,
				http.StatusRequestEntityTooLarge,
				reviewAttentionPolicyInvalidRequest,
			)
			return false
		}
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionPolicyInvalidRequest,
		)
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(raw) ||
		!validJSONUnicodeScalars(raw) ||
		rejectDuplicateReviewAttentionJSONKeys(raw) != nil {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionPolicyInvalidRequest,
		)
		return false
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(raw, &envelope); err != nil ||
		explicitNullReviewAttentionCollection(envelope, "global") ||
		explicitNullReviewAttentionCollection(envelope, "repositories") {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionPolicyInvalidRequest,
		)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(destination); err != nil {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionPolicyInvalidRequest,
		)
		return false
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			reviewAttentionPolicyInvalidRequest,
		)
		return false
	}
	return true
}

func explicitNullReviewAttentionCollection(
	envelope map[string]json.RawMessage,
	name string,
) bool {
	wanted := foldAgentJSONKey(name)
	for key, raw := range envelope {
		if foldAgentJSONKey(key) == wanted &&
			bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return true
		}
	}
	return false
}

func rejectDuplicateReviewAttentionJSONKeys(raw []byte) error {
	// The policy envelope adds several levels around gate questions. Keep the
	// transport bound above the workflow value bound while still rejecting
	// adversarially deep JSON before typed decoding.
	const maxReviewAttentionRequestJSONDepth = 128
	return rejectDuplicateJSONKeys(
		raw,
		maxReviewAttentionRequestJSONDepth,
		reviewAttentionQuestionsExactKeySubtree,
	)
}

func reviewAttentionQuestionsExactKeySubtree(
	path []string,
	foldedKey string,
) bool {
	if !strings.EqualFold(foldedKey, "questions") {
		return false
	}
	// Global gates live at global.<decision-point>[]. Repository gates live at
	// repositories.<repository>.<decision-point>.gates[]. A decision point or
	// repository named "questions" must not switch its typed children into
	// arbitrary-map semantics.
	return (len(path) == 3 && strings.EqualFold(path[0], "global") && path[2] == "[]") ||
		(len(path) == 5 && strings.EqualFold(path[0], "repositories") &&
			strings.EqualFold(path[3], "gates") && path[4] == "[]")
}

func newReviewAttentionPolicySource(
	cfg *config.Config,
) (*reviews.ConfigAttentionPolicySource, error) {
	if cfg == nil {
		return nil, errors.New("review attention configuration is required")
	}
	if err := cfg.Reviews.Validate(); err != nil {
		return nil, err
	}
	return reviews.NewConfigAttentionPolicySource(
		cfg.Reviews.Attention.Global,
		cfg.Reviews.Attention.Repositories,
	)
}

func validateReviewAttentionPolicyConfiguration(
	cfg *config.Config,
) (*reviews.ConfigAttentionPolicySource, error) {
	source, err := newReviewAttentionPolicySource(cfg)
	if err != nil {
		return nil, err
	}

	configured := make(map[string]struct{}, len(cfg.Agents.List)+1)
	if len(cfg.Agents.List) == 0 {
		configured[routing.DefaultAgentID] = struct{}{}
	} else {
		for index := range cfg.Agents.List {
			configured[cfg.Agents.List[index].ID] = struct{}{}
		}
	}
	for _, agentID := range source.AgentIDs() {
		if _, ok := configured[agentID]; !ok {
			return nil, errors.New("review attention policy references an unavailable agent")
		}
	}
	return source, nil
}

func writeReviewAttentionPoliciesJSON(
	w http.ResponseWriter,
	cfg *config.Config,
	source *reviews.ConfigAttentionPolicySource,
	configRevision string,
) {
	attention := cfg.Reviews.Attention
	if attention.Global == nil {
		attention.Global = make(map[string][]gatetypes.GateSpec)
	}
	if attention.Repositories == nil {
		attention.Repositories = make(
			map[string]map[string]gatetypes.RepositoryGatePolicy,
		)
	}
	writeReviewJSON(w, http.StatusOK, reviewAttentionPoliciesResponse{
		ReviewAttentionConfig: attention,
		CatalogRevision:       source.CatalogRevision(),
		ConfigRevision:        configRevision,
		Effects: reviewAttentionPolicyEffects{
			GatewayEffect: agentEffectsForConfig(cfg).GatewayEffect,
		},
	})
}

func writeReviewJSON(w http.ResponseWriter, status int, value any) {
	setReviewResponseHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
