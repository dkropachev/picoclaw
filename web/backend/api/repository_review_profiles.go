package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

type repositoryReviewProfileConfigRequest struct {
	Name                  string                                 `json:"name"`
	ReviewFocus           string                                 `json:"review_focus"`
	ScopePolicy           repoaudit.RepositoryReviewScopePolicy  `json:"scope_policy"`
	ReviewerModel         string                                 `json:"reviewer_model"`
	Force                 bool                                   `json:"force"`
	AutoContinue          *bool                                  `json:"auto_continue,omitempty"`
	MaxFilesPerRun        int                                    `json:"max_files_per_run"`
	MaxContentBytes       int64                                  `json:"max_content_bytes"`
	MaxParallelChildren   int                                    `json:"max_parallel_children"`
	EstimatedOutputTokens int                                    `json:"estimated_output_tokens"`
	Budget                repoaudit.RepositoryReviewBudgetPolicy `json:"budget"`
	ExpectedVersion       int64                                  `json:"expected_version,omitempty"`
}

func (h *Handler) registerRepositoryReviewProfileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repository-reviews/profiles", h.handleListRepositoryReviewProfiles)
	mux.HandleFunc("POST /api/repository-reviews/profiles", h.handleCreateRepositoryReviewProfile)
	mux.HandleFunc(
		"GET /api/repository-reviews/profiles/{profile_id}",
		h.handleGetRepositoryReviewProfile,
	)
	mux.HandleFunc(
		"PATCH /api/repository-reviews/profiles/{profile_id}",
		h.handleUpdateRepositoryReviewProfile,
	)
	mux.HandleFunc(
		"DELETE /api/repository-reviews/profiles/{profile_id}",
		h.handleDeleteRepositoryReviewProfile,
	)
}

func (h *Handler) handleGetRepositoryReviewProfile(w http.ResponseWriter, r *http.Request) {
	store, err := h.repositoryReviewStore()
	if err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	profile, found, err := store.GetProfile(r.Context(), r.PathValue("profile_id"))
	if err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	if !found {
		writeRepositoryReviewProfileError(w, os.ErrNotExist)
		return
	}
	writeRepositoryReviewJSON(w, http.StatusOK, map[string]any{"profile": profile})
}

func (h *Handler) handleListRepositoryReviewProfiles(w http.ResponseWriter, r *http.Request) {
	store, err := h.repositoryReviewStore()
	if err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	profiles, err := store.ListProfiles(r.Context())
	if err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	writeRepositoryReviewJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
}

func (h *Handler) handleCreateRepositoryReviewProfile(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	var request repositoryReviewProfileConfigRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	store, err := h.repositoryReviewStore()
	if err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	if validationErr := h.validateRepositoryReviewProfileModel(request.ReviewerModel); validationErr != nil {
		writeRepositoryReviewProfileError(w, validationErr)
		return
	}
	if validationErr := h.validateRepositoryReviewProfilePricing(
		request.ReviewerModel,
		request.Budget,
	); validationErr != nil {
		writeRepositoryReviewProfileError(w, validationErr)
		return
	}
	created, err := store.CreateProfile(r.Context(), repositoryReviewProfileFromRequest(request))
	if err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	writeRepositoryReviewJSON(w, http.StatusCreated, map[string]any{"profile": created})
}

func (h *Handler) handleUpdateRepositoryReviewProfile(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	var request repositoryReviewProfileConfigRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	store, err := h.repositoryReviewStore()
	if err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	if validationErr := h.validateRepositoryReviewProfileModel(request.ReviewerModel); validationErr != nil {
		writeRepositoryReviewProfileError(w, validationErr)
		return
	}
	if validationErr := h.validateRepositoryReviewProfilePricing(
		request.ReviewerModel,
		request.Budget,
	); validationErr != nil {
		writeRepositoryReviewProfileError(w, validationErr)
		return
	}
	if inactiveErr := ensureRepositoryReviewProfileInactive(
		r.Context(),
		store,
		r.PathValue("profile_id"),
	); inactiveErr != nil {
		writeRepositoryReviewProfileError(w, inactiveErr)
		return
	}
	updated, err := store.UpdateProfile(
		r.Context(), r.PathValue("profile_id"), request.ExpectedVersion,
		func(candidate *repoaudit.RepositoryReviewProfile) error {
			applyRepositoryReviewProfileRequest(candidate, request)
			return nil
		},
	)
	if err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	writeRepositoryReviewJSON(w, http.StatusOK, map[string]any{"profile": updated})
}

func (h *Handler) validateRepositoryReviewProfileModel(reviewerModel string) error {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return err
	}
	reviewerModel = strings.TrimSpace(reviewerModel)
	for _, option := range repositoryReviewModelOptions(cfg) {
		if option.Alias != reviewerModel {
			continue
		}
		if !option.Available {
			detail := strings.TrimSpace(option.BlockedReason)
			return fmt.Errorf(
				"%w: reviewer_model %q is unavailable: %s",
				repoaudit.ErrInvalidProfile,
				reviewerModel,
				detail,
			)
		}
		return nil
	}
	return fmt.Errorf("%w: reviewer_model %q is not a configured alias", repoaudit.ErrInvalidProfile, reviewerModel)
}

func (h *Handler) validateRepositoryReviewProfilePricing(
	reviewerModel string,
	budget repoaudit.RepositoryReviewBudgetPolicy,
) error {
	if budget.MaxEstimatedCostUSD <= 0 {
		return nil
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return err
	}
	reviewerModel = strings.TrimSpace(reviewerModel)
	for _, option := range repositoryReviewModelOptions(cfg) {
		if option.Alias == reviewerModel && option.PriceKnown {
			return nil
		}
	}
	return fmt.Errorf(
		"%w: cost budget requires centrally configured pricing for reviewer_model %q",
		repoaudit.ErrInvalidProfile,
		reviewerModel,
	)
}

func (h *Handler) handleDeleteRepositoryReviewProfile(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryReviewMutation(r); err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	var request repositoryReviewAutomationActionRequest
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	store, err := h.repositoryReviewStore()
	if err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	if err := store.DeleteProfile(r.Context(), r.PathValue("profile_id"), request.ExpectedVersion); err != nil {
		writeRepositoryReviewProfileError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func repositoryReviewProfileFromRequest(
	request repositoryReviewProfileConfigRequest,
) repoaudit.RepositoryReviewProfile {
	profile := repoaudit.RepositoryReviewProfile{}
	applyRepositoryReviewProfileRequest(&profile, request)
	if request.AutoContinue == nil {
		profile.AutoContinue = true
	}
	return profile
}

func applyRepositoryReviewProfileRequest(
	profile *repoaudit.RepositoryReviewProfile,
	request repositoryReviewProfileConfigRequest,
) {
	if profile == nil {
		return
	}
	profile.Name = request.Name
	profile.ReviewFocus = request.ReviewFocus
	profile.ScopePolicy = request.ScopePolicy
	profile.ReviewerModel = request.ReviewerModel
	profile.Force = request.Force
	if request.AutoContinue != nil {
		profile.AutoContinue = *request.AutoContinue
	}
	profile.MaxFilesPerRun = request.MaxFilesPerRun
	profile.MaxContentBytes = request.MaxContentBytes
	profile.MaxParallelChildren = request.MaxParallelChildren
	profile.EstimatedOutputTokens = request.EstimatedOutputTokens
	profile.BudgetPolicy = request.Budget
}

func ensureRepositoryReviewProfileInactive(
	ctx context.Context,
	store repoaudit.Store,
	profileID string,
) error {
	automations, err := store.ListAutomations(ctx)
	if err != nil {
		return err
	}
	for _, automation := range automations {
		if automation.ProfileID != strings.TrimSpace(profileID) {
			continue
		}
		if automation.Status == repoaudit.RepositoryReviewAutomationRunning ||
			automation.Status == repoaudit.RepositoryReviewAutomationStopping {
			return errRepositoryReviewProfileActive
		}
	}
	return nil
}

func writeRepositoryReviewProfileError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "repository_review_profile_unavailable"
	switch {
	case errors.Is(err, os.ErrNotExist) || strings.Contains(strings.ToLower(err.Error()), "not found"):
		status, code = http.StatusNotFound, "repository_review_profile_not_found"
	case errors.Is(err, repoaudit.ErrProfileAssigned):
		status, code = http.StatusConflict, "repository_review_profile_assigned"
	case errors.Is(err, errRepositoryReviewProfileActive), errors.Is(err, repoaudit.ErrProfileActive):
		status, code = http.StatusConflict, "repository_review_profile_active"
	case errors.Is(err, repoaudit.ErrConflict):
		status, code = http.StatusConflict, "stale_repository_review_profile"
	case errors.Is(err, repoaudit.ErrInvalidProfile),
		errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF),
		isRepositoryReviewJSONError(err),
		strings.Contains(strings.ToLower(err.Error()), "invalid"),
		strings.Contains(strings.ToLower(err.Error()), "required"),
		strings.Contains(strings.ToLower(err.Error()), "unknown field"),
		strings.Contains(strings.ToLower(err.Error()), "unexpected end"):
		status, code = http.StatusBadRequest, "invalid_repository_review_profile"
	}
	message := strings.TrimSpace(err.Error())
	if status >= 500 {
		message = strings.ReplaceAll(code, "_", " ")
	}
	writeRepositoryReviewJSON(w, status, map[string]string{"code": code, "message": message})
}

func isRepositoryReviewJSONError(err error) bool {
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	return errors.As(err, &syntaxError) || errors.As(err, &typeError)
}
