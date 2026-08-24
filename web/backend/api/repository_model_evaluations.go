package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoeval"
)

const repositoryModelEvaluationRequestMaxBytes = 256 << 10

var (
	errRepositoryModelEvaluationBusy             = errors.New("repository model evaluation is active")
	errRepositoryModelEvaluationUnavailableModel = errors.New("repository model evaluation model alias is unavailable")
)

type repositoryModelEvaluationPatchRequest struct {
	ExpectedVersion         int64           `json:"expected_version"`
	Repository              *string         `json:"repository,omitempty"`
	Ref                     *string         `json:"ref,omitempty"`
	CandidateModels         *[]string       `json:"candidate_models,omitempty"`
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

func (h *Handler) registerRepositoryModelEvaluationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/model-evaluations", h.handleListRepositoryModelEvaluations)
	mux.HandleFunc("POST /api/model-evaluations", h.handleCreateRepositoryModelEvaluation)
	mux.HandleFunc("GET /api/model-evaluations/options", h.handleRepositoryModelEvaluationOptions)
	mux.HandleFunc("GET /api/model-evaluations/{id}", h.handleGetRepositoryModelEvaluation)
	mux.HandleFunc("PATCH /api/model-evaluations/{id}", h.handlePatchRepositoryModelEvaluation)
	mux.HandleFunc("DELETE /api/model-evaluations/{id}", h.handleDeleteRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/{id}/preflight", h.handlePreflightRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/{id}/start", h.handleStartRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/{id}/cancel", h.handleCancelRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/{id}/resume", h.handleResumeRepositoryModelEvaluation)
	mux.HandleFunc("POST /api/model-evaluations/{id}/restart", h.handleRestartRepositoryModelEvaluation)
	mux.HandleFunc("GET /api/model-evaluations/{id}/corpus", h.handleRepositoryModelEvaluationCorpus)
}

func (h *Handler) repositoryModelEvaluationStore() (repoeval.Store, *config.Config, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return repoeval.Store{}, nil, err
	}
	return repoeval.NewStore(cfg.WorkspacePath()), cfg, nil
}

func (h *Handler) handleListRepositoryModelEvaluations(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.RawQuery != "" {
		writeRepositoryModelEvaluationError(w, errors.New("invalid repository model evaluation request"))
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
	summaries := make([]repositoryModelEvaluationSummary, len(evaluations))
	for index, evaluation := range evaluations {
		summaries[index] = projectRepositoryModelEvaluationSummary(evaluation)
	}
	writeRepositoryReviewJSON(w, http.StatusOK, map[string]any{"evaluations": summaries})
}

func (h *Handler) handleCreateRepositoryModelEvaluation(w http.ResponseWriter, r *http.Request) {
	if err := validateRepositoryModelEvaluationMutation(r); err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	var request repoeval.CreateRequest
	if err := decodeRepositoryModelEvaluationRequest(r, &request); err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	normalizedRepository, err := normalizeRepositoryModelEvaluationRepository(r.Context(), request.Repository)
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	request.Repository = normalizedRepository
	store, cfg, err := h.repositoryModelEvaluationStore()
	if err != nil {
		writeRepositoryModelEvaluationError(w, err)
		return
	}
	if aliasErr := validateRepositoryModelEvaluationAliases(
		cfg,
		request.CandidateModels,
		request.SelectorModelAlias,
		request.JudgeModelAlias,
	); aliasErr != nil {
		writeRepositoryModelEvaluationError(w, aliasErr)
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
	proposed := repoeval.Clone(current)
	applyRepositoryModelEvaluationPatch(&proposed, request)
	if aliasErr := validateRepositoryModelEvaluationAliases(
		cfg,
		proposed.CandidateModels,
		proposed.SelectorModelAlias,
		proposed.JudgeModelAlias,
	); aliasErr != nil {
		writeRepositoryModelEvaluationError(w, aliasErr)
		return
	}
	updated, err := store.Update(
		r.Context(),
		current.ID,
		request.ExpectedVersion,
		func(candidate *repoeval.Evaluation) error {
			applyRepositoryModelEvaluationPatch(candidate, request)
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
	if evaluation.Status.InFlight() {
		writeRepositoryModelEvaluationError(w, errRepositoryModelEvaluationBusy)
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
		"models":       safeModels,
		"repositories": repositories,
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

func repositoryModelEvaluationAliasTransportSafe(alias string) bool {
	alias = strings.TrimSpace(alias)
	return alias != "" && !strings.ContainsAny(alias, ",;\r\n")
}

func decodeRepositoryModelEvaluationRequest(r *http.Request, target any) error {
	if r == nil || r.URL == nil || r.URL.RawQuery != "" || r.Body == nil ||
		r.ContentLength > repositoryModelEvaluationRequestMaxBytes {
		return errors.New("invalid repository model evaluation request")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, repositoryModelEvaluationRequestMaxBytes+1))
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

func validateRepositoryModelEvaluationMutation(r *http.Request) error {
	if r == nil || r.URL == nil || r.URL.RawQuery != "" || prWorkspaceMutationCrossSite(r) ||
		validateEventReplayHeaders(r.Header) != nil {
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
