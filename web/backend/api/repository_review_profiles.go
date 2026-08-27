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
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

var repositoryReviewProfileCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "id", Type: collectionquery.TypeString, Sortable: true},
		{Name: "name", Type: collectionquery.TypeString, Sortable: true},
		{Name: "account", Type: collectionquery.TypeString, Sortable: true},
		{Name: "reviewer", Type: collectionquery.TypeString, Sortable: true},
		{Name: "issue_writer", Type: collectionquery.TypeString, Sortable: true},
		{Name: "force", Type: collectionquery.TypeBoolean, Sortable: true},
		{Name: "auto_continue", Type: collectionquery.TypeBoolean, Sortable: true},
		{Name: "files", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "parallel", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "version", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "updated", Type: collectionquery.TypeTimestamp, Sortable: true},
	},
	[]collectionquery.SortField{{Field: "name", Direction: collectionquery.Ascending}},
)

type repositoryReviewProfileConfigRequest struct {
	Name                string                                 `json:"name"`
	ReviewFocus         string                                 `json:"review_focus"`
	ScopePolicy         repoaudit.RepositoryReviewScopePolicy  `json:"scope_policy"`
	ReviewerModel       string                                 `json:"reviewer_model"`
	IssueWriterModel    string                                 `json:"issue_writer_model,omitempty"`
	IssuePrompt         *string                                `json:"issue_prompt,omitempty"`
	AccountRef          string                                 `json:"account_ref,omitempty"`
	Force               bool                                   `json:"force"`
	AutoContinue        *bool                                  `json:"auto_continue,omitempty"`
	MaxFilesPerRun      int                                    `json:"max_files_per_run"`
	MaxContentBytes     int64                                  `json:"max_content_bytes"`
	MaxParallelChildren int                                    `json:"max_parallel_children"`
	Budget              repoaudit.RepositoryReviewBudgetPolicy `json:"budget"`
	ExpectedVersion     int64                                  `json:"expected_version,omitempty"`
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
	query, _ := collectionquery.Parse("", repositoryReviewProfileCollectionSchema)
	projected := profiles
	total := len(profiles)
	nextCursor := ""
	if r.URL != nil && r.URL.RawQuery != "" {
		listRequest, ok := parseCollectionListRequest(w, r, repositoryReviewProfileCollectionSchema)
		if !ok {
			return
		}
		query = listRequest.Query
		page, pageErr := collectionquery.Paginate(
			profiles,
			query,
			listRequest.Cursor,
			listRequest.Limit,
			listRequest.Now,
			collectionquery.PageOptions[repoaudit.RepositoryReviewProfile]{
				ID: func(profile repoaudit.RepositoryReviewProfile) (string, error) {
					return profile.ID, nil
				},
				Clone: func(profile repoaudit.RepositoryReviewProfile) repoaudit.RepositoryReviewProfile {
					return profile
				},
				Resolve: func(
					profile repoaudit.RepositoryReviewProfile,
					field collectionquery.Field,
					_ time.Time,
				) (collectionquery.FieldValue, bool) {
					return repositoryReviewProfileCollectionField(profile, field)
				},
			},
		)
		if pageErr != nil {
			writeCollectionPageError(w, pageErr)
			return
		}
		projected = page.Items
		total = page.Total
		nextCursor = page.NextCursor
	}
	names := make([]string, 0, len(profiles))
	accounts := make([]string, 0, len(profiles))
	reviewers := make([]string, 0, len(profiles))
	writers := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		names = append(names, profile.Name)
		accounts = append(accounts, profile.AccountRef)
		reviewers = append(reviewers, profile.ReviewerModel)
		writer := profile.IssueWriterModel
		if writer == "" {
			writer = profile.ReviewerModel
		}
		writers = append(writers, writer)
	}
	writeRepositoryReviewJSON(w, http.StatusOK, map[string]any{
		"profiles":        projected,
		"total":           total,
		"next_cursor":     nextCursor,
		"canonical_query": query.Canonical(),
		"query_schema": collectionSchemaWithSuggestions(
			repositoryReviewProfileCollectionSchema,
			map[collectionquery.Field][]string{
				"name": names, "account": accounts, "reviewer": reviewers, "issue_writer": writers,
			},
		),
	})
}

func repositoryReviewProfileCollectionField(
	profile repoaudit.RepositoryReviewProfile,
	field collectionquery.Field,
) (collectionquery.FieldValue, bool) {
	switch field {
	case "id":
		return collectionquery.StringValue(profile.ID), true
	case "name":
		return collectionquery.StringValue(profile.Name), true
	case "account":
		return collectionquery.StringValue(profile.AccountRef), true
	case "reviewer":
		return collectionquery.StringValue(profile.ReviewerModel), true
	case "issue_writer":
		writer := profile.IssueWriterModel
		if writer == "" {
			writer = profile.ReviewerModel
		}
		return collectionquery.StringValue(writer), true
	case "force":
		return collectionquery.BooleanValue(profile.Force), true
	case "auto_continue":
		return collectionquery.BooleanValue(profile.AutoContinue), true
	case "files":
		return collectionquery.NumberValue(float64(profile.MaxFilesPerRun)), true
	case "parallel":
		return collectionquery.NumberValue(float64(profile.MaxParallelChildren)), true
	case "version":
		return collectionquery.NumberValue(float64(profile.Version)), true
	case "updated":
		return collectionquery.TimestampValue(profile.UpdatedAt), true
	default:
		return collectionquery.FieldValue{}, false
	}
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
	if validationErr := h.validateRepositoryReviewProfileSelectionWithIssueWriter(
		request.AccountRef, request.ReviewerModel, request.IssueWriterModel, request.Budget,
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
	if validationErr := h.validateRepositoryReviewProfileSelectionWithIssueWriter(
		request.AccountRef, request.ReviewerModel, request.IssueWriterModel, request.Budget,
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

func (h *Handler) validateRepositoryReviewProfileSelection(
	accountRef string,
	reviewerModel string,
	budget repoaudit.RepositoryReviewBudgetPolicy,
) error {
	return h.validateRepositoryReviewProfileSelectionWithIssueWriter(
		accountRef, reviewerModel, "", budget,
	)
}

func (h *Handler) validateRepositoryReviewProfileSelectionWithIssueWriter(
	accountRef string,
	reviewerModel string,
	issueWriterModel string,
	budget repoaudit.RepositoryReviewBudgetPolicy,
) error {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return err
	}
	accountRef = repositoryReviewEffectiveAccountRef(cfg, accountRef)
	if err := validateSelectableAccountRef(cfg, accountRef); err != nil {
		return fmt.Errorf("%w: account_ref: %v", repoaudit.ErrInvalidProfile, err)
	}
	reviewerModel = strings.TrimSpace(reviewerModel)
	if err := validateRepositoryReviewPassiveModelAlias(
		cfg, accountRef, reviewerModel, "reviewer_model",
	); err != nil {
		return err
	}
	issueWriterModel = strings.TrimSpace(issueWriterModel)
	if issueWriterModel != "" {
		if err := validateRepositoryReviewIssueWriterAlias(
			cfg, accountRef, issueWriterModel,
		); err != nil {
			return err
		}
	}
	if repoaudit.RepositoryReviewGuardUsesSpend(budget.GuardExpression) {
		if price, known := repositoryReviewAliasPriceForAccount(
			cfg, reviewerModel, accountRef, make(map[string]bool),
		); !known || price.InputPricePerMTok <= 0 && price.OutputPricePerMTok <= 0 {
			return fmt.Errorf(
				"%w: spend.total.* requires centrally configured pricing for reviewer_model %q on account %q",
				repoaudit.ErrInvalidProfile, reviewerModel, accountRef,
			)
		}
	}
	return nil
}

func validateRepositoryReviewIssueWriterAlias(
	cfg *config.Config,
	accountRef string,
	modelAlias string,
) error {
	if _, aliasErr := cfg.GetModelAlias(modelAlias); aliasErr != nil {
		return fmt.Errorf(
			"%w: issue_writer_model %q is not configured",
			repoaudit.ErrInvalidProfile, modelAlias,
		)
	}
	if repositoryReviewAliasUsesAgenticCLIOnAccount(cfg, modelAlias, accountRef) {
		return fmt.Errorf(
			"%w: issue_writer_model %q uses an agentic CLI provider unavailable to immutable review on account %q",
			repoaudit.ErrInvalidProfile, modelAlias, accountRef,
		)
	}
	if !repositoryReviewAliasAvailableForAccount(cfg, modelAlias, accountRef) {
		return fmt.Errorf(
			"%w: issue_writer_model %q is unavailable on account %q",
			repoaudit.ErrInvalidProfile, modelAlias, accountRef,
		)
	}
	return nil
}

func validateRepositoryReviewPassiveModelAlias(
	cfg *config.Config,
	accountRef string,
	modelAlias string,
	field string,
) error {
	aliasConfig, aliasErr := cfg.GetModelAlias(modelAlias)
	if aliasErr != nil {
		return fmt.Errorf(
			"%w: %s %q is not configured",
			repoaudit.ErrInvalidProfile, field, modelAlias,
		)
	}
	if repositoryReviewAliasUsesAgenticCLI(cfg, *aliasConfig) ||
		repositoryReviewAliasUsesAgenticCLIOnAccount(cfg, modelAlias, accountRef) {
		return fmt.Errorf(
			"%w: %s %q uses an agentic CLI provider unavailable to immutable review",
			repoaudit.ErrInvalidProfile, field, modelAlias,
		)
	}
	if !repositoryReviewAliasAvailableForAccount(cfg, modelAlias, accountRef) {
		return fmt.Errorf(
			"%w: %s %q is unavailable on account %q",
			repoaudit.ErrInvalidProfile, field, modelAlias, accountRef,
		)
	}
	return nil
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
	profile.IssueWriterModel = request.IssueWriterModel
	if request.IssuePrompt != nil {
		profile.IssuePrompt = *request.IssuePrompt
	}
	profile.AccountRef = request.AccountRef
	profile.Force = request.Force
	if request.AutoContinue != nil {
		profile.AutoContinue = *request.AutoContinue
	}
	profile.MaxFilesPerRun = request.MaxFilesPerRun
	profile.MaxContentBytes = request.MaxContentBytes
	profile.MaxParallelChildren = request.MaxParallelChildren
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
