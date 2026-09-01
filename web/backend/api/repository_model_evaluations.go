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
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/repoeval"
)

const repositoryModelEvaluationRequestMaxBytes = 256 << 10

var (
	errRepositoryModelEvaluationBusy             = errors.New("repository model evaluation is active")
	errRepositoryModelEvaluationUnavailableModel = errors.New("repository model evaluation model alias is unavailable")
	errRepositoryModelEvaluationMediaType        = errors.New(
		"repository model evaluation JSON content type is required",
	)
	errRepositoryModelEvaluationRequestTooLarge = errors.New("repository model evaluation request is too large")
)

type repositoryModelEvaluationPatchRequest struct {
	ExpectedVersion         int64           `json:"expected_version"`
	ProfileID               *string         `json:"profile_id,omitempty"`
	Repository              *string         `json:"repository,omitempty"`
	Ref                     *string         `json:"ref,omitempty"`
	CandidateModels         *[]string       `json:"candidate_models,omitempty"`
	SelectorModelAlias      *string         `json:"selector_model_alias,omitempty"`
	JudgeModelAlias         *string         `json:"judge_model_alias,omitempty"`
	Focus                   *repoeval.Focus `json:"focus,omitempty"`
	DefaultFilesPerLanguage *int            `json:"default_files_per_language,omitempty"`
	FilesPerLanguage        *map[string]int `json:"files_per_language,omitempty"`
}

// repositoryModelEvaluationCreateAPIRequest keeps the public profile-driven
// admission contract separate from the fully materialized durable request.
// Deprecated custom fields are decoded only so admission can reject them with a
// clear profile-driven error; existing durable legacy evaluations remain readable.
type repositoryModelEvaluationCreateAPIRequest struct {
	Repository              string          `json:"repository"`
	Ref                     string          `json:"ref,omitempty"`
	ProfileID               string          `json:"profile_id,omitempty"`
	CandidateModels         []string        `json:"candidate_models"`
	SelectorModelAlias      *string         `json:"selector_model_alias,omitempty"`
	JudgeModelAlias         *string         `json:"judge_model_alias,omitempty"`
	Focus                   *repoeval.Focus `json:"focus,omitempty"`
	DefaultFilesPerLanguage *int            `json:"default_files_per_language,omitempty"`
	FilesPerLanguage        *map[string]int `json:"files_per_language,omitempty"`
}

type repositoryModelEvaluationActionRequest struct {
	ExpectedVersion int64 `json:"expected_version"`
}

type repositoryModelEvaluationSummary struct {
	ID              string            `json:"id"`
	Version         int64             `json:"version"`
	Status          repoeval.Status   `json:"status"`
	Repository      string            `json:"repository"`
	Ref             string            `json:"ref"`
	CandidateModels []string          `json:"candidate_models"`
	Progress        repoeval.Progress `json:"progress"`
	Usage           repoeval.Usage    `json:"usage"`
	Warnings        []string          `json:"warnings"`
	Failure         string            `json:"failure,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	FinishedAt      *time.Time        `json:"finished_at,omitempty"`
}

type repositoryModelEvaluationDetail struct {
	Evaluation repoeval.Evaluation `json:"evaluation"`
}

type repositoryModelEvaluationRepositoryOption struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Label      string `json:"label"`
}

type repositoryModelEvaluationProfileOption struct {
	ID                      string         `json:"id"`
	Version                 int64          `json:"version"`
	Name                    string         `json:"name"`
	ReviewerModel           string         `json:"reviewer_model"`
	AccountRef              string         `json:"account_ref,omitempty"`
	ReviewFocus             string         `json:"review_focus"`
	Focus                   repoeval.Focus `json:"focus"`
	MaxFilesPerBatch        int            `json:"max_files_per_batch"`
	MaxContentBytesPerBatch int64          `json:"max_content_bytes_per_batch"`
	MaxParallelChildren     int            `json:"max_parallel_children"`
	AvailableModels         []string       `json:"available_models"`
}

var repositoryModelEvaluationCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "id", Type: collectionquery.TypeString, Sortable: true},
		{Name: "status", Type: collectionquery.TypeEnum, Sortable: true, SuggestedValues: []string{
			"draft", "preflighting", "ready", "running", "judging", "analyzing",
			"completed", "canceling", "canceled", "failed",
		}},
		{Name: "repository", Type: collectionquery.TypeString, Sortable: true},
		{Name: "ref", Type: collectionquery.TypeString, Sortable: true},
		{Name: "models", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "progress", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "version", Type: collectionquery.TypeNumber, Sortable: true},
		{Name: "created", Type: collectionquery.TypeTimestamp, Sortable: true},
		{Name: "updated", Type: collectionquery.TypeTimestamp, Sortable: true},
	},
	[]collectionquery.SortField{{Field: "updated", Direction: collectionquery.Descending}},
)

func (h *Handler) registerRepositoryModelEvaluationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/model-evaluations", h.handleListRepositoryModelEvaluations)
	mux.HandleFunc("POST /api/model-evaluations", h.handleCreateRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/bulk-delete", h.handleBulkDeleteRepositoryModelEvaluations)
	mux.HandleFunc("POST /api/model-evaluations/run", h.handleRunRepositoryModelEvaluation)
	mux.HandleFunc("GET /api/model-evaluations/options", h.handleRepositoryModelEvaluationOptions)
	mux.HandleFunc("GET /api/model-evaluations/{id}", h.handleGetRepositoryModelEvaluation)
	mux.HandleFunc("PATCH /api/model-evaluations/{id}", h.handlePatchRepositoryModelEvaluation)
	mux.HandleFunc("DELETE /api/model-evaluations/{id}", h.handleDeleteRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/{id}/preflight", h.handlePreflightRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/{id}/run", h.handleRunExistingRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/{id}/start", h.handleStartRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/{id}/cancel", h.handleCancelRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/{id}/resume", h.handleResumeRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/{id}/restart", h.handleRestartRepositoryModelEvaluation)
	mux.HandleFunc("GET /api/model-evaluations/{id}/corpus", h.handleRepositoryModelEvaluationCorpus)
}

type repositoryModelEvaluationBulkDeleteRequest struct {
	Items []repoeval.BulkDeleteItem `json:"items"`
}

func (h *Handler) handleBulkDeleteRepositoryModelEvaluations(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryModelEvaluationMutation(r); err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	var request repositoryModelEvaluationBulkDeleteRequest
	if err := decodeRepositoryModelEvaluationRequest(r, &request); err != nil ||
		len(request.Items) == 0 || len(request.Items) > 200 {
		writeRepositoryModelEvaluationError(w, errors.Join(repoeval.ErrInvalidEvaluation, err))
		return
	}
	store, _, err := h.repositoryModelEvaluationStore()
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	result, err := store.BulkDelete(r.Context(), request.Items)
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeRepositoryReviewJSON(w, http.StatusOK, result)
}

func (h *Handler) repositoryModelEvaluationStore() (repoeval.Store, *config.Config, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return repoeval.Store{}, nil, err
	}
	return repoeval.NewSQLiteStore(cfg.WorkspacePath()), cfg, nil
}

func (h *Handler) handleListRepositoryModelEvaluations(w http.ResponseWriter, r *http.Request) {
	listRequest, ok := parseCollectionListRequest(w, r, repositoryModelEvaluationCollectionSchema)
	if !ok {
		return
	}
	store, _, err := h.repositoryModelEvaluationStore()
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	evaluations, err := store.List(r.Context())
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	page, err := collectionquery.Paginate(
		evaluations, listRequest.Query, listRequest.Cursor, listRequest.Limit, listRequest.Now,
		collectionquery.PageOptions[repoeval.Evaluation]{
			ID:         func(evaluation repoeval.Evaluation) (string, error) { return evaluation.ID, nil },
			ValidateID: validRepositoryModelEvaluationCollectionID,
			Clone:      repoeval.Clone,
			Resolve: func(evaluation repoeval.Evaluation, field collectionquery.Field, _ time.Time) (collectionquery.FieldValue, bool) {
				switch field {
				case "id":
					return collectionquery.StringValue(evaluation.ID), true
				case "status":
					return collectionquery.EnumValue(string(evaluation.Status)), true
				case "repository":
					return collectionquery.StringValue(
						sanitizeRepositoryModelEvaluationIdentity(evaluation.Repository),
					), true
				case "ref":
					return collectionquery.StringValue(evaluation.Ref), true
				case "models":
					return collectionquery.NumberValue(float64(len(evaluation.CandidateModels))), true
				case "progress":
					return collectionquery.NumberValue(evaluation.Progress.Percent), true
				case "version":
					return collectionquery.NumberValue(float64(evaluation.Version)), true
				case "created":
					return collectionquery.TimestampValue(evaluation.CreatedAt), true
				case "updated":
					return collectionquery.TimestampValue(evaluation.UpdatedAt), true
				default:
					return collectionquery.FieldValue{}, false
				}
			},
		},
	)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	summaries := make([]repositoryModelEvaluationSummary, len(page.Items))
	for index, evaluation := range page.Items {
		summaries[index] = projectRepositoryModelEvaluationSummary(evaluation)
	}
	repositories := make([]string, 0, len(evaluations))
	refs := make([]string, 0, len(evaluations))
	for _, evaluation := range evaluations {
		repositories = append(repositories, sanitizeRepositoryModelEvaluationIdentity(evaluation.Repository))
		refs = append(refs, evaluation.Ref)
	}
	writeRepositoryReviewJSON(w, http.StatusOK, map[string]any{
		"evaluations": summaries, "total": page.Total, "next_cursor": page.NextCursor,
		"canonical_query": listRequest.Query.Canonical(),
		"query_schema": collectionSchemaWithSuggestions(
			repositoryModelEvaluationCollectionSchema,
			map[collectionquery.Field][]string{
				"repository": repositories, "ref": refs,
			},
		),
	})
}

func validRepositoryModelEvaluationCollectionID(id string) bool {
	digest, ok := strings.CutPrefix(id, "rme_")
	if !ok || len(digest) != 32 {
		return false
	}
	for _, character := range digest {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func (h *Handler) handleCreateRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryModelEvaluationMutation(r); err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	payload, err := decodeRepositoryModelEvaluationCreateAPIRequest(r)
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	store, cfg, err := h.repositoryModelEvaluationStore()
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	request, err := h.materializeRepositoryModelEvaluationCreateRequest(r.Context(), cfg, payload)
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	created, err := store.Create(r.Context(), request)
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	writeRepositoryReviewJSON(
		w,
		http.StatusCreated,
		repositoryModelEvaluationDetail{Evaluation: projectRepositoryModelEvaluation(created)},
	)
}

func (h *Handler) handleRunRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryModelEvaluationMutation(r); err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	payload, err := decodeRepositoryModelEvaluationCreateAPIRequest(r)
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	_, cfg, err := h.repositoryModelEvaluationStore()
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	request, err := h.materializeRepositoryModelEvaluationCreateRequest(r.Context(), cfg, payload)
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	controller, err := h.ensureRepositoryModelEvaluationController()
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	evaluation, err := controller.Run(r.Context(), request)
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	writeRepositoryReviewJSON(
		w,
		http.StatusAccepted,
		repositoryModelEvaluationDetail{Evaluation: projectRepositoryModelEvaluation(evaluation)},
	)
}

func decodeRepositoryModelEvaluationCreateAPIRequest(
	r *http.Request,
) (repositoryModelEvaluationCreateAPIRequest, error) {
	var request repositoryModelEvaluationCreateAPIRequest
	if err := decodeRepositoryModelEvaluationRequest(r, &request); err != nil {
		return repositoryModelEvaluationCreateAPIRequest{}, err
	}
	normalizedRepository, err := normalizeRepositoryModelEvaluationRepository(r.Context(), request.Repository)
	if err != nil {
		return repositoryModelEvaluationCreateAPIRequest{}, err
	}
	request.Repository = normalizedRepository
	return request, nil
}

func (h *Handler) materializeRepositoryModelEvaluationCreateRequest(
	ctx context.Context,
	cfg *config.Config,
	payload repositoryModelEvaluationCreateAPIRequest,
) (repoeval.CreateRequest, error) {
	profileID := strings.TrimSpace(payload.ProfileID)
	if profileID == "" {
		return repoeval.CreateRequest{}, fmt.Errorf(
			"%w: profile_id is required",
			repoeval.ErrInvalidEvaluation,
		)
	}
	if payload.Focus != nil || payload.DefaultFilesPerLanguage != nil || payload.FilesPerLanguage != nil ||
		payload.SelectorModelAlias != nil || payload.JudgeModelAlias != nil {
		return repoeval.CreateRequest{}, fmt.Errorf(
			"%w: profile-driven probes do not accept custom scope, quota, selector, or judge options",
			repoeval.ErrInvalidEvaluation,
		)
	}
	store, err := h.repositoryReviewStore()
	if err != nil {
		return repoeval.CreateRequest{}, err
	}
	profile, found, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return repoeval.CreateRequest{}, err
	}
	if !found {
		return repoeval.CreateRequest{}, os.ErrNotExist
	}
	if profile.MaxFilesPerRun < 1 || profile.MaxFilesPerRun > 128 {
		return repoeval.CreateRequest{}, fmt.Errorf(
			"%w: profile files per batch must be between 1 and 128 for a model probe",
			repoeval.ErrInvalidEvaluation,
		)
	}
	focus := repositoryModelEvaluationFocusFromReviewProfile(profile)
	effectiveAccount := repositoryReviewEffectiveAccountRef(cfg, profile.AccountRef)
	candidates := normalizeRepositoryModelEvaluationAliases(payload.CandidateModels)
	if len(candidates) < 2 || !slices.Contains(candidates, strings.TrimSpace(profile.ReviewerModel)) {
		return repoeval.CreateRequest{}, fmt.Errorf(
			"%w: candidate models must include the selected profile reviewer and at least one comparison model",
			repoeval.ErrInvalidEvaluation,
		)
	}
	available := repositoryModelEvaluationAvailableAliasesForAccount(cfg, effectiveAccount)
	for _, alias := range candidates {
		if !available[alias] {
			return repoeval.CreateRequest{}, fmt.Errorf(
				"%w: %q is unavailable for profile account %q",
				errRepositoryModelEvaluationUnavailableModel,
				alias,
				effectiveAccount,
			)
		}
	}
	selector := strings.TrimSpace(profile.ReviewerModel)
	judge := repositoryModelEvaluationAutomaticJudge(cfg, effectiveAccount, candidates, selector)
	snapshot := &repoeval.ProfileSnapshot{
		ID:                      profile.ID,
		Version:                 profile.Version,
		Name:                    profile.Name,
		ReviewerModel:           selector,
		AccountRef:              effectiveAccount,
		ReviewFocus:             profile.ReviewFocus,
		Focus:                   focus,
		MaxFilesPerBatch:        profile.MaxFilesPerRun,
		MaxContentBytesPerBatch: profile.MaxContentBytes,
		MaxParallelChildren:     profile.MaxParallelChildren,
	}
	return repoeval.CreateRequest{
		Repository:              payload.Repository,
		Ref:                     payload.Ref,
		CandidateModels:         candidates,
		SelectorModelAlias:      selector,
		JudgeModelAlias:         judge,
		Focus:                   focus,
		DefaultFilesPerLanguage: repoeval.DefaultFilesPerLanguage,
		FilesPerLanguage:        map[string]int{},
		Profile:                 snapshot,
		WorkSizingPlan: repositoryModelEvaluationWorkSizingPlan(
			profile.MaxFilesPerRun,
			profile.MaxContentBytes,
		),
	}, nil
}

func normalizeRepositoryModelEvaluationAliases(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		alias := strings.TrimSpace(raw)
		if alias == "" {
			continue
		}
		if _, duplicate := seen[alias]; duplicate {
			continue
		}
		seen[alias] = struct{}{}
		out = append(out, alias)
	}
	return out
}

func repositoryModelEvaluationFocusFromReviewProfile(
	profile repoaudit.RepositoryReviewProfile,
) repoeval.Focus {
	codeTypes := make([]repoeval.CodeType, 0, len(profile.ScopePolicy.CodeTypes))
	for _, codeType := range profile.ScopePolicy.CodeTypes {
		codeTypes = append(codeTypes, repoeval.CodeType(codeType))
	}
	return repoeval.Focus{
		CodeTypes:      codeTypes,
		IncludeFolders: append([]string(nil), profile.ScopePolicy.IncludeFolders...),
		ExcludeFolders: append([]string(nil), profile.ScopePolicy.ExcludeFolders...),
		FreeText:       profile.ScopePolicy.FreeText,
	}
}

func repositoryModelEvaluationAvailableAliasesForAccount(
	cfg *config.Config,
	accountRef string,
) map[string]bool {
	available := make(map[string]bool)
	for _, option := range repositoryReviewModelOptions(cfg) {
		alias := strings.TrimSpace(option.Alias)
		if alias == "" || !option.Available || option.BlockedReason != "" ||
			!repositoryModelEvaluationAliasTransportSafe(alias) ||
			!repositoryReviewAliasAvailableForAccount(cfg, alias, accountRef) {
			continue
		}
		available[alias] = true
	}
	return available
}

func repositoryModelEvaluationAutomaticJudge(
	cfg *config.Config,
	accountRef string,
	candidates []string,
	fallback string,
) string {
	selected := make(map[string]struct{}, len(candidates))
	for _, alias := range candidates {
		selected[alias] = struct{}{}
	}
	available := repositoryModelEvaluationAvailableAliasesForAccount(cfg, accountRef)
	options := repositoryReviewModelOptions(cfg)
	for _, preferDefault := range []bool{true, false} {
		for _, option := range options {
			alias := strings.TrimSpace(option.Alias)
			if option.Default != preferDefault || !available[alias] {
				continue
			}
			if _, candidate := selected[alias]; !candidate {
				return alias
			}
		}
	}
	if available[fallback] {
		return fallback
	}
	return ""
}

func repositoryModelEvaluationWorkSizingPlan(
	maximumFiles int,
	maximumContentBytes int64,
) []repoeval.WorkSizingPoint {
	fileValues := repositoryModelEvaluationSizingLadder(int64(maximumFiles))
	contentValues := repositoryModelEvaluationSizingLadder(maximumContentBytes)
	points := make([]repoeval.WorkSizingPoint, 0, len(fileValues)+len(contentValues)-1)
	for _, value := range fileValues {
		if value == int64(maximumFiles) {
			continue
		}
		points = append(points, repoeval.WorkSizingPoint{
			ID:                   fmt.Sprintf("wsp_files_%d_bytes_%d", value, maximumContentBytes),
			Axis:                 repoeval.WorkSizingAxisFilesPerBatch,
			FilesPerBatch:        int(value),
			ContentBytesPerBatch: maximumContentBytes,
		})
	}
	for _, value := range contentValues {
		if value == maximumContentBytes {
			continue
		}
		points = append(points, repoeval.WorkSizingPoint{
			ID:                   fmt.Sprintf("wsp_files_%d_bytes_%d", maximumFiles, value),
			Axis:                 repoeval.WorkSizingAxisContentBytesPerBatch,
			FilesPerBatch:        maximumFiles,
			ContentBytesPerBatch: value,
		})
	}
	points = append(points, repoeval.WorkSizingPoint{
		ID:                   fmt.Sprintf("wsp_files_%d_bytes_%d", maximumFiles, maximumContentBytes),
		Axis:                 repoeval.WorkSizingAxisConfigured,
		FilesPerBatch:        maximumFiles,
		ContentBytesPerBatch: maximumContentBytes,
	})
	return points
}

func repositoryModelEvaluationSizingLadder(maximum int64) []int64 {
	if maximum <= 0 {
		return nil
	}
	values := []int64{(maximum + 3) / 4, (maximum + 1) / 2, maximum}
	out := make([]int64, 0, len(values))
	for _, value := range values {
		value = max(int64(1), value)
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func (h *Handler) handleGetRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.RawQuery != "" {
		writeRepositoryModelEvaluationError(w, errors.New("invalid repository model evaluation request"))
		return
	}
	evaluation, found, err := h.getRepositoryModelEvaluation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	if !found {
		writeRepositoryModelEvaluationError(w, os.ErrNotExist)
		return
	}
	writeRepositoryReviewJSON(
		w,
		http.StatusOK,
		repositoryModelEvaluationDetail{Evaluation: projectRepositoryModelEvaluation(evaluation)},
	)
}

func (h *Handler) getRepositoryModelEvaluation(ctx context.Context, id string) (repoeval.Evaluation, bool, error) {
	store, _, err := h.repositoryModelEvaluationStore()
	if err != nil {
		return repoeval.Evaluation{}, false, err
	}
	return store.Get(ctx, id)
}

func (h *Handler) handlePatchRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryModelEvaluationMutation(r); err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	var request repositoryModelEvaluationPatchRequest
	if err := decodeRepositoryModelEvaluationRequest(r, &request); err != nil || request.ExpectedVersion < 1 {
		writeRepositoryModelEvaluationError(w, errors.Join(repoeval.ErrInvalidEvaluation, err))
		return
	}
	if request.Repository != nil {
		normalizedRepository, normalizeErr := normalizeRepositoryModelEvaluationRepository(
			r.Context(),
			*request.Repository,
		)
		if normalizeErr != nil {
			writeRepositoryModelEvaluationError(w, normalizeErr)
			return
		}
		request.Repository = &normalizedRepository
	}
	store, cfg, err := h.repositoryModelEvaluationStore()
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	current, found, err := store.Get(r.Context(), r.PathValue("id"))
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	if current.Status != repoeval.StatusDraft {
		writeRepositoryModelEvaluationError(w, repoeval.ErrInvalidTransition)
		return
	}
	if request.ProfileID == nil {
		writeRepositoryModelEvaluationError(
			w,
			fmt.Errorf(
				"%w: profile_id is required when editing a model probe",
				repoeval.ErrInvalidEvaluation,
			),
		)
		return
	}
	proposed := repoeval.Clone(current)
	applyRepositoryModelEvaluationPatch(&proposed, request)
	if request.Focus != nil || request.SelectorModelAlias != nil || request.JudgeModelAlias != nil ||
		request.DefaultFilesPerLanguage != nil || request.FilesPerLanguage != nil {
		writeRepositoryModelEvaluationError(
			w,
			fmt.Errorf("%w: profile-driven probes do not accept custom options", repoeval.ErrInvalidEvaluation),
		)
		return
	}
	payload := repositoryModelEvaluationCreateAPIRequest{
		Repository:      proposed.Repository,
		Ref:             proposed.Ref,
		ProfileID:       *request.ProfileID,
		CandidateModels: append([]string(nil), proposed.CandidateModels...),
	}
	materialized, materializeErr := h.materializeRepositoryModelEvaluationCreateRequest(
		r.Context(), cfg, payload,
	)
	if materializeErr != nil {
		writeRepositoryModelEvaluationError(w, materializeErr)
		return
	}
	applyRepositoryModelEvaluationMaterialized(&proposed, materialized)
	updated, err := store.Update(
		r.Context(),
		current.ID,
		request.ExpectedVersion,
		func(candidate *repoeval.Evaluation) error {
			applyRepositoryModelEvaluationPatch(candidate, request)
			applyRepositoryModelEvaluationMaterialized(candidate, materialized)
			return nil
		},
	)
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	writeRepositoryReviewJSON(
		w,
		http.StatusOK,
		repositoryModelEvaluationDetail{Evaluation: projectRepositoryModelEvaluation(updated)},
	)
}

func applyRepositoryModelEvaluationMaterialized(
	evaluation *repoeval.Evaluation,
	request repoeval.CreateRequest,
) {
	if evaluation == nil {
		return
	}
	evaluation.Repository = request.Repository
	evaluation.Ref = request.Ref
	evaluation.CandidateModels = append([]string(nil), request.CandidateModels...)
	evaluation.SelectorModelAlias = request.SelectorModelAlias
	evaluation.JudgeModelAlias = request.JudgeModelAlias
	evaluation.Focus = request.Focus
	evaluation.Profile = request.Profile
	evaluation.DefaultFilesPerLanguage = request.DefaultFilesPerLanguage
	evaluation.FilesPerLanguage = make(map[string]int, len(request.FilesPerLanguage))
	for language, limit := range request.FilesPerLanguage {
		evaluation.FilesPerLanguage[language] = limit
	}
	evaluation.WorkSizingPlan = append([]repoeval.WorkSizingPoint(nil), request.WorkSizingPlan...)
}

func applyRepositoryModelEvaluationPatch(
	evaluation *repoeval.Evaluation,
	request repositoryModelEvaluationPatchRequest,
) {
	if request.Repository != nil {
		evaluation.Repository = *request.Repository
	}
	if request.Ref != nil {
		evaluation.Ref = *request.Ref
	}
	if request.CandidateModels != nil {
		evaluation.CandidateModels = append([]string(nil), (*request.CandidateModels)...)
	}
	if request.SelectorModelAlias != nil {
		evaluation.SelectorModelAlias = *request.SelectorModelAlias
	}
	if request.JudgeModelAlias != nil {
		evaluation.JudgeModelAlias = *request.JudgeModelAlias
	}
	if request.Focus != nil {
		evaluation.Focus = *request.Focus
	}
	if request.DefaultFilesPerLanguage != nil {
		evaluation.DefaultFilesPerLanguage = *request.DefaultFilesPerLanguage
	}
	if request.FilesPerLanguage != nil {
		evaluation.FilesPerLanguage = make(map[string]int, len(*request.FilesPerLanguage))
		for language, limit := range *request.FilesPerLanguage {
			evaluation.FilesPerLanguage[language] = limit
		}
	}
}

func normalizeRepositoryModelEvaluationRepository(ctx context.Context, repository string) (string, error) {
	normalized, err := normalizeRepositoryReviewAutomationRepository(repository)
	if err != nil {
		return "", errors.Join(repoeval.ErrInvalidEvaluation, err)
	}
	if filepath.IsAbs(normalized) {
		canonical, resolveErr := filepath.EvalSymlinks(normalized)
		if resolveErr != nil {
			return "", fmt.Errorf(
				"%w: local repository path is unavailable",
				repoeval.ErrInvalidEvaluation,
			)
		}
		info, statErr := os.Stat(canonical)
		if statErr != nil || !info.IsDir() {
			return "", fmt.Errorf(
				"%w: local repository path is unavailable",
				repoeval.ErrInvalidEvaluation,
			)
		}
		if !repositoryModelEvaluationGitRoot(ctx, canonical) {
			return "", fmt.Errorf(
				"%w: local repository path must be the root of a Git repository",
				repoeval.ErrInvalidEvaluation,
			)
		}
		normalized = filepath.Clean(canonical)
	}
	return normalized, nil
}

func repositoryModelEvaluationGitRoot(ctx context.Context, repository string) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	validationCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	run := func(arguments ...string) (string, error) {
		command := exec.CommandContext(validationCtx, "git", append([]string{"-C", repository}, arguments...)...)
		environment := make([]string, 0, len(os.Environ())+1)
		for _, variable := range os.Environ() {
			if !strings.HasPrefix(variable, "GIT_") {
				environment = append(environment, variable)
			}
		}
		command.Env = append(environment, "GIT_OPTIONAL_LOCKS=0")
		output, err := command.Output()
		return strings.TrimSpace(string(output)), err
	}

	bare, err := run("rev-parse", "--is-bare-repository")
	if err != nil || bare != "true" && bare != "false" {
		return false
	}
	rootArgument := "--show-toplevel"
	if bare == "true" {
		rootArgument = "--absolute-git-dir"
	}
	root, err := run("rev-parse", rootArgument)
	if err != nil || root == "" {
		return false
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	return err == nil && filepath.Clean(canonicalRoot) == filepath.Clean(repository)
}

func (h *Handler) handleDeleteRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryModelEvaluationMutation(r); err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	var request repositoryModelEvaluationActionRequest
	if err := decodeRepositoryModelEvaluationRequest(r, &request); err != nil || request.ExpectedVersion < 1 {
		writeRepositoryModelEvaluationError(w, errors.Join(repoeval.ErrInvalidEvaluation, err))
		return
	}
	store, _, err := h.repositoryModelEvaluationStore()
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	evaluation, found, err := store.Get(r.Context(), r.PathValue("id"))
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	if evaluation.Status != repoeval.StatusDraft {
		writeRepositoryModelEvaluationError(w, repoeval.ErrInvalidTransition)
		return
	}
	if err := store.Delete(r.Context(), evaluation.ID, request.ExpectedVersion); err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handlePreflightRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	h.handleRepositoryModelEvaluationAction(w, r, "preflight")
}

func (h *Handler) handleRunExistingRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	h.handleRepositoryModelEvaluationAction(w, r, "run")
}

func (h *Handler) handleStartRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	h.handleRepositoryModelEvaluationAction(w, r, "start")
}

func (h *Handler) handleCancelRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	h.handleRepositoryModelEvaluationAction(w, r, "cancel")
}

func (h *Handler) handleResumeRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	h.handleRepositoryModelEvaluationAction(w, r, "resume")
}

func (h *Handler) handleRestartRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	h.handleRepositoryModelEvaluationAction(w, r, "restart")
}

func (h *Handler) handleRepositoryModelEvaluationAction(w http.ResponseWriter, r *http.Request, action string) {
	if err := validateRepositoryModelEvaluationMutation(r); err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	var request repositoryModelEvaluationActionRequest
	if err := decodeRepositoryModelEvaluationRequest(r, &request); err != nil || request.ExpectedVersion < 1 {
		writeRepositoryModelEvaluationError(w, errors.Join(repoeval.ErrInvalidEvaluation, err))
		return
	}
	controller, err := h.ensureRepositoryModelEvaluationController()
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	var evaluation repoeval.Evaluation
	switch action {
	case "preflight":
		evaluation, err = controller.Preflight(r.Context(), r.PathValue("id"), request.ExpectedVersion)
	case "run":
		evaluation, err = controller.RunExisting(r.Context(), r.PathValue("id"), request.ExpectedVersion)
	case "start":
		evaluation, err = controller.StartEvaluation(r.Context(), r.PathValue("id"), request.ExpectedVersion)
	case "cancel":
		evaluation, err = controller.Cancel(r.Context(), r.PathValue("id"), request.ExpectedVersion)
	case "resume":
		evaluation, err = controller.Resume(r.Context(), r.PathValue("id"), request.ExpectedVersion)
	case "restart":
		evaluation, err = controller.Restart(r.Context(), r.PathValue("id"), request.ExpectedVersion)
	default:
		err = repoeval.ErrInvalidTransition
	}
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	writeRepositoryReviewJSON(
		w,
		http.StatusAccepted,
		repositoryModelEvaluationDetail{Evaluation: projectRepositoryModelEvaluation(evaluation)},
	)
}

func (h *Handler) handleRepositoryModelEvaluationCorpus(w http.ResponseWriter, r *http.Request) {
	offset, limit, err := repositoryModelEvaluationPage(r)
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	evaluation, found, err := h.getRepositoryModelEvaluation(r.Context(), r.PathValue("id"))
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	if evaluation.Corpus == nil {
		writeRepositoryReviewJSON(
			w,
			http.StatusOK,
			map[string]any{"files": []repoeval.CorpusFile{}, "offset": 0, "total": 0},
		)
		return
	}
	total := len(evaluation.Corpus.Files)
	offset = min(offset, total)
	end := min(total, offset+limit)
	response := map[string]any{
		"commit_sha": evaluation.Corpus.CommitSHA, "inventory_hash": evaluation.Corpus.InventoryHash,
		"selection_rationale": evaluation.Corpus.SelectionRationale,
		"generated_at":        evaluation.Corpus.GeneratedAt,
		"language_counts":     evaluation.Corpus.LanguageCounts,
		"files":               append([]repoeval.CorpusFile(nil), evaluation.Corpus.Files[offset:end]...),
		"offset":              offset, "total": total,
	}
	if end < total {
		response["next_offset"] = end
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func repositoryModelEvaluationPage(r *http.Request) (int, int, error) {
	if r == nil || r.URL == nil {
		return 0, 0, errors.New("invalid repository model evaluation request")
	}
	query := r.URL.Query()
	for key, values := range query {
		if (key != "offset" && key != "limit") || len(values) != 1 {
			return 0, 0, errors.New("invalid repository model evaluation request")
		}
	}
	parse := func(raw string, fallback, maximum int) (int, error) {
		if raw == "" {
			return fallback, nil
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || maximum > 0 && (value < 1 || value > maximum) {
			return 0, errors.New("invalid repository model evaluation request")
		}
		return value, nil
	}
	offset, err := parse(query.Get("offset"), 0, 0)
	if err != nil {
		return 0, 0, err
	}
	limit, err := parse(query.Get("limit"), 50, 200)
	return offset, limit, err
}

func (h *Handler) handleRepositoryModelEvaluationOptions(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.RawQuery != "" {
		writeRepositoryModelEvaluationError(w, errors.New("invalid repository model evaluation request"))
		return
	}
	_, cfg, err := h.repositoryModelEvaluationStore()
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	models := repositoryReviewModelOptions(cfg)
	safeModels := make([]repositoryReviewModelOption, 0, len(models))
	for _, model := range models {
		if model.Available && model.BlockedReason == "" && repositoryModelEvaluationAliasTransportSafe(model.Alias) {
			safeModels = append(safeModels, model)
		}
	}
	profiles := make([]repositoryModelEvaluationProfileOption, 0)
	reviewStore := repoaudit.NewSQLiteStore(cfg.WorkspacePath())
	storedProfiles, listErr := reviewStore.ListProfiles(r.Context())
	if listErr != nil {
		writeRepositoryModelEvaluationError(w, listErr)
		return
	}
	for _, profile := range storedProfiles {
		if profile.MaxFilesPerRun < 1 || profile.MaxFilesPerRun > 128 {
			continue
		}
		accountRef := repositoryReviewEffectiveAccountRef(cfg, profile.AccountRef)
		availableSet := repositoryModelEvaluationAvailableAliasesForAccount(cfg, accountRef)
		if !availableSet[strings.TrimSpace(profile.ReviewerModel)] {
			continue
		}
		availableAliases := make([]string, 0, len(availableSet))
		for _, model := range safeModels {
			if availableSet[model.Alias] {
				availableAliases = append(availableAliases, model.Alias)
			}
		}
		if len(availableAliases) < 2 {
			continue
		}
		profiles = append(profiles, repositoryModelEvaluationProfileOption{
			ID:                      profile.ID,
			Version:                 profile.Version,
			Name:                    profile.Name,
			ReviewerModel:           profile.ReviewerModel,
			AccountRef:              accountRef,
			ReviewFocus:             profile.ReviewFocus,
			Focus:                   repositoryModelEvaluationFocusFromReviewProfile(profile),
			MaxFilesPerBatch:        profile.MaxFilesPerRun,
			MaxContentBytesPerBatch: profile.MaxContentBytes,
			MaxParallelChildren:     profile.MaxParallelChildren,
			AvailableModels:         availableAliases,
		})
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Name == profiles[j].Name {
			return profiles[i].ID < profiles[j].ID
		}
		return profiles[i].Name < profiles[j].Name
	})
	repositories := make([]repositoryModelEvaluationRepositoryOption, 0)
	if manager, managerErr := h.gitWorkspaceManager(); managerErr == nil {
		if stats, statsErr := manager.Stats(r.Context()); statsErr == nil {
			upstreamByRepository := make(map[string]string)
			for _, workspace := range stats.Workspaces {
				upstream := sanitizeRepositoryModelEvaluationIdentity(workspace.UpstreamURL)
				if upstream != "" {
					upstreamByRepository[workspace.RepoID] = upstream
				}
			}
			seen := make(map[string]struct{})
			for _, repository := range stats.Repositories {
				identity := sanitizeRepositoryModelEvaluationIdentity(repository.RemoteURL)
				if identity == "" {
					identity = upstreamByRepository[repository.ID]
				}
				if identity == "" {
					continue
				}
				identity, err = normalizeRepositoryModelEvaluationRepository(r.Context(), identity)
				if err != nil {
					continue
				}
				if _, duplicate := seen[identity]; duplicate {
					continue
				}
				seen[identity] = struct{}{}
				label := strings.TrimSuffix(filepath.Base(strings.TrimSuffix(identity, "/")), ".git")
				repositories = append(
					repositories,
					repositoryModelEvaluationRepositoryOption{ID: repository.ID, Repository: identity, Label: label},
				)
			}
		}
	}
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].Label < repositories[j].Label })
	writeRepositoryReviewJSON(w, http.StatusOK, map[string]any{
		"models":        safeModels,
		"profiles":      profiles,
		"profile_count": len(storedProfiles),
		"repositories":  repositories,
		"code_types": []repoeval.CodeType{
			repoeval.CodeTypeHotpath,
			repoeval.CodeTypeCode,
			repoeval.CodeTypeTest,
			repoeval.CodeTypeBenchTest,
		},
		"default_files_per_language": repoeval.DefaultFilesPerLanguage,
		"max_files_per_language":     repoeval.MaxFilesPerLanguage,
		"max_candidate_models":       8,
	})
}

func validateRepositoryModelEvaluationAliases(cfg *config.Config, candidates []string, selector, judge string) error {
	available := make(map[string]bool)
	for _, option := range repositoryReviewModelOptions(cfg) {
		available[option.Alias] = option.Available && option.BlockedReason == ""
	}
	aliases := append(append([]string(nil), candidates...), selector, judge)
	seenCandidates := make(map[string]struct{}, len(candidates))
	for index, rawAlias := range aliases {
		alias := strings.TrimSpace(rawAlias)
		if !repositoryModelEvaluationAliasTransportSafe(alias) {
			return fmt.Errorf("%w: model alias contains an unsupported list delimiter", repoeval.ErrInvalidEvaluation)
		}
		if alias == "" || !available[alias] {
			return fmt.Errorf("%w: %q", errRepositoryModelEvaluationUnavailableModel, alias)
		}
		if index < len(candidates) {
			if _, duplicate := seenCandidates[alias]; duplicate {
				return fmt.Errorf("%w: duplicate candidate alias", repoeval.ErrInvalidEvaluation)
			}
			seenCandidates[alias] = struct{}{}
		}
	}
	return nil
}

func validateRepositoryModelEvaluationExecutionAliases(
	cfg *config.Config,
	candidates []string,
	selector string,
	judge string,
	profile *repoeval.ProfileSnapshot,
) error {
	if profile == nil {
		return validateRepositoryModelEvaluationAliases(cfg, candidates, selector, judge)
	}
	available := repositoryModelEvaluationAvailableAliasesForAccount(cfg, profile.AccountRef)
	aliases := append(append([]string(nil), candidates...), selector, judge)
	seenCandidates := make(map[string]struct{}, len(candidates))
	for index, rawAlias := range aliases {
		alias := strings.TrimSpace(rawAlias)
		if !repositoryModelEvaluationAliasTransportSafe(alias) || !available[alias] {
			return fmt.Errorf(
				"%w: %q is unavailable for frozen profile account %q",
				errRepositoryModelEvaluationUnavailableModel,
				alias,
				profile.AccountRef,
			)
		}
		if index < len(candidates) {
			if _, duplicate := seenCandidates[alias]; duplicate {
				return fmt.Errorf("%w: duplicate candidate alias", repoeval.ErrInvalidEvaluation)
			}
			seenCandidates[alias] = struct{}{}
		}
	}
	return nil
}

func repositoryModelEvaluationAliasTransportSafe(alias string) bool {
	alias = strings.TrimSpace(alias)
	return alias != "" && !strings.ContainsAny(alias, ",;\r\n")
}

func decodeRepositoryModelEvaluationRequest(r *http.Request, target any) error {
	if r == nil || r.URL == nil || r.URL.RawQuery != "" || r.Body == nil ||
		r.ContentLength > repositoryModelEvaluationRequestMaxBytes {
		if r != nil && r.ContentLength > repositoryModelEvaluationRequestMaxBytes {
			return errRepositoryModelEvaluationRequestTooLarge
		}
		return errors.New("invalid repository model evaluation request")
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return errRepositoryModelEvaluationMediaType
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errRepositoryModelEvaluationMediaType
	}
	for name, value := range parameters {
		if !strings.EqualFold(name, "charset") || !strings.EqualFold(value, "utf-8") {
			return errRepositoryModelEvaluationMediaType
		}
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, repositoryModelEvaluationRequestMaxBytes+1))
	if err != nil {
		return errors.New("invalid repository model evaluation request")
	}
	if len(raw) > repositoryModelEvaluationRequestMaxBytes {
		return errRepositoryModelEvaluationRequestTooLarge
	}
	if !utf8.Valid(raw) || rejectDuplicateJSONKeys(raw, 32, repositoryModelEvaluationExactJSONKeySubtree) != nil {
		return errors.New("invalid repository model evaluation request")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid repository model evaluation request")
	}
	return nil
}

func repositoryModelEvaluationExactJSONKeySubtree(_ []string, foldedKey string) bool {
	return foldedKey == foldAgentJSONKey("files_per_language")
}

func validateRepositoryModelEvaluationMutation(r *http.Request) error {
	if r == nil || r.URL == nil || r.URL.RawQuery != "" || prWorkspaceMutationCrossSite(r) {
		return errors.New("invalid repository model evaluation request")
	}
	if err := validateEventReplayHeaders(r.Header); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "content type") {
			return errRepositoryModelEvaluationMediaType
		}
		return errors.New("invalid repository model evaluation request")
	}
	return nil
}

func projectRepositoryModelEvaluationSummary(evaluation repoeval.Evaluation) repositoryModelEvaluationSummary {
	progress := evaluation.Progress
	progress.Message = sanitizeRepositoryModelEvaluationRuntimeText(progress.Message, evaluation.Repository)
	return repositoryModelEvaluationSummary{
		ID: evaluation.ID, Version: evaluation.Version, Status: evaluation.Status,
		Repository: sanitizeRepositoryModelEvaluationIdentity(evaluation.Repository), Ref: evaluation.Ref,
		CandidateModels: append([]string(nil), evaluation.CandidateModels...), Progress: progress,
		Usage: evaluation.Usage, Warnings: append([]string(nil), evaluation.Warnings...),
		Failure:   sanitizeRepositoryModelEvaluationRuntimeText(evaluation.Failure, evaluation.Repository),
		CreatedAt: evaluation.CreatedAt, UpdatedAt: evaluation.UpdatedAt, FinishedAt: evaluation.FinishedAt,
	}
}

func projectRepositoryModelEvaluation(evaluation repoeval.Evaluation) repoeval.Evaluation {
	projected := repoeval.Clone(evaluation)
	if len(projected.Checkpoint.Batches) > 0 {
		_, unsupported := repositoryModelEvaluationJudgedClaimCounts(projected.Checkpoint.Batches)
		claims := make(map[string][]repoeval.ModelClaim)
		omittedClaims := make(map[string]int)
		availableClaims := make(map[string]bool)
		for _, checkpoint := range projected.Checkpoint.Batches {
			for alias, ledger := range checkpoint.ClaimLedger {
				availableClaims[alias] = true
				claims[alias] = append(claims[alias], ledger...)
			}
			for alias, omitted := range checkpoint.ClaimLedgerOmitted {
				omittedClaims[alias] += omitted
			}
		}
		for index := range projected.Comparisons {
			alias := projected.Comparisons[index].ModelAlias
			if availableClaims[alias] {
				projected.Comparisons[index].Claims = append(
					[]repoeval.ModelClaim(nil),
					claims[alias]...,
				)
				projected.Comparisons[index].ClaimsOmitted = omittedClaims[alias]
				projected.Comparisons[index].ClaimLedgerAvailable = true
				for claimIndex := range projected.Comparisons[index].Claims {
					claim := &projected.Comparisons[index].Claims[claimIndex]
					claim.Title = sanitizeRepositoryModelEvaluationRuntimeText(claim.Title, evaluation.Repository)
					claim.Evidence = sanitizeRepositoryModelEvaluationRuntimeText(claim.Evidence, evaluation.Repository)
					claim.Impact = sanitizeRepositoryModelEvaluationRuntimeText(claim.Impact, evaluation.Repository)
					claim.JudgeRationale = sanitizeRepositoryModelEvaluationRuntimeText(
						claim.JudgeRationale,
						evaluation.Repository,
					)
				}
			}
			if projected.Comparisons[index].UnsupportedClaims != nil {
				continue
			}
			count, found := unsupported[projected.Comparisons[index].ModelAlias]
			if !found {
				continue
			}
			projected.Comparisons[index].UnsupportedClaims = &count
		}
	}
	projected.Failure = sanitizeRepositoryModelEvaluationRuntimeText(projected.Failure, evaluation.Repository)
	projected.Progress.Message = sanitizeRepositoryModelEvaluationRuntimeText(
		projected.Progress.Message,
		evaluation.Repository,
	)
	projected.Repository = sanitizeRepositoryModelEvaluationIdentity(projected.Repository)
	if projected.Corpus != nil {
		projected.Corpus.Files = nil
	}
	projected.Checkpoint = repoeval.Checkpoint{}
	projected.WorkSizingUsage = nil
	projected.WorkSizingConcreteModels = nil
	if len(projected.RunIDs) > 50 {
		projected.RunIDs = append([]string(nil), projected.RunIDs[len(projected.RunIDs)-50:]...)
	}
	return projected
}

func sanitizeRepositoryModelEvaluationIdentity(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" || filepath.IsAbs(identity) {
		return ""
	}
	if parsed, err := url.Parse(identity); err == nil {
		if strings.EqualFold(parsed.Scheme, "file") || parsed.User != nil || parsed.RawQuery != "" ||
			parsed.Fragment != "" {
			return ""
		}
	}
	return identity
}

func writeRepositoryModelEvaluationError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "repository_model_evaluation_unavailable"
	switch {
	case errors.Is(err, errRepositoryModelEvaluationMediaType):
		status, code = http.StatusUnsupportedMediaType, "json_content_type_required"
	case errors.Is(err, errRepositoryModelEvaluationRequestTooLarge):
		status, code = http.StatusRequestEntityTooLarge, "repository_model_evaluation_request_too_large"
	case errors.Is(err, os.ErrNotExist):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, repoeval.ErrConflict), errors.Is(err, repoeval.ErrInvalidTransition),
		errors.Is(err, repoeval.ErrControllerLocked), errors.Is(err, errRepositoryModelEvaluationBusy):
		status, code = http.StatusConflict, "stale_repository_model_evaluation"
	case errors.Is(err, repoeval.ErrInvalidEvaluation), errors.Is(err, errRepositoryModelEvaluationUnavailableModel),
		errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		func() bool { var syntax *json.SyntaxError; return errors.As(err, &syntax) }(),
		func() bool { var mismatch *json.UnmarshalTypeError; return errors.As(err, &mismatch) }(),
		strings.Contains(strings.ToLower(err.Error()), "invalid"),
		strings.Contains(strings.ToLower(err.Error()), "unknown field"):
		status, code = http.StatusBadRequest, "invalid_repository_model_evaluation"
	}
	message := strings.TrimSpace(err.Error())
	if status >= 500 {
		message = strings.ReplaceAll(code, "_", " ")
	}
	writeRepositoryReviewJSON(w, status, map[string]string{"code": code, "message": message})
}
