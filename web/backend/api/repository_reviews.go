package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

// Draft text can legally require six-byte JSON escapes per source byte; leave
// bounded envelope/label headroom so every Store-valid draft remains editable.
const repositoryReviewRequestMaxBytes = 8 << 20

type repositoryReviewIssueUpdateRequest struct {
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Labels          []string `json:"labels,omitempty"`
	ExpectedVersion int64    `json:"expected_version"`
}

func (h *Handler) registerRepositoryReviewRoutes(mux *http.ServeMux) {
	h.registerRepositoryReviewAutomationRoutes(mux)
}

func repositoryReviewPageInteger(raw string, fallback, maximum int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || maximum > 0 && (value < 1 || value > maximum) {
		return 0, errors.New("invalid repository review request")
	}
	return value, nil
}

func (h *Handler) repositoryReviewStore() (repoaudit.Store, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return repoaudit.Store{}, err
	}
	return repoaudit.NewSQLiteStore(cfg.WorkspacePath()), nil
}

func decodeRepositoryReviewRequest(r *http.Request, target any) error {
	if r == nil || r.Body == nil || r.ContentLength > repositoryReviewRequestMaxBytes {
		return errors.New("invalid repository review request")
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, repositoryReviewRequestMaxBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid repository review request")
	}
	return nil
}

func validateRepositoryReviewMutation(r *http.Request) error {
	if r == nil || r.URL == nil || r.URL.RawQuery != "" || prWorkspaceMutationCrossSite(r) ||
		validateEventReplayHeaders(r.Header) != nil {
		return errors.New("invalid repository review request")
	}
	return nil
}

func writeRepositoryReviewError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "repository_review_unavailable"
	switch {
	case errors.Is(err, os.ErrNotExist):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, repoaudit.ErrRepositoryReviewPurgeInProgress):
		status, code = http.StatusConflict, "repository_review_purge_in_progress"
	case errors.Is(err, repoaudit.ErrConflict):
		status, code = http.StatusConflict, "stale_repository_review"
	case errors.Is(err, repoaudit.ErrInvalidPlan),
		errors.Is(err, io.ErrUnexpectedEOF),
		isRepositoryReviewJSONError(err),
		strings.Contains(strings.ToLower(err.Error()), "invalid"),
		strings.Contains(strings.ToLower(err.Error()), "required"),
		strings.Contains(strings.ToLower(err.Error()), "duplicate"),
		strings.Contains(strings.ToLower(err.Error()), "unknown field"),
		strings.Contains(strings.ToLower(err.Error()), "cannot unmarshal"),
		strings.Contains(strings.ToLower(err.Error()), "unexpected end"),
		errors.Is(err, io.EOF):
		status, code = http.StatusBadRequest, "invalid_request"
	}
	writeRepositoryReviewJSON(w, status, map[string]string{
		"code": code, "message": strings.ReplaceAll(code, "_", " "),
	})
}

func writeRepositoryReviewJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
