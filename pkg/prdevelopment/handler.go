package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	// RuntimeRoutePrefix is reachable only through the protected event-operator
	// generation. The launcher mirrors it under /api/pr-development.
	RuntimeRoutePrefix = "/runtime/eventing/pr-development"
	maxQueryBytes      = 8 << 10
)

// Handler exposes an immutable Service generation over two exact GET routes.
type Handler struct {
	Service *Service
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	setResponseHeaders(w)
	if handler == nil || handler.Service == nil {
		writeError(w, http.StatusServiceUnavailable, "development workbench unavailable")
		return
	}
	if request == nil ||
		request.URL == nil ||
		request.URL.Fragment != "" ||
		request.URL.EscapedPath() != request.URL.Path ||
		request.ContentLength != 0 ||
		len(request.TransferEncoding) != 0 ||
		!identityReadEncoding(request.Header) {
		writeError(w, http.StatusBadRequest, "invalid development workbench request")
		return
	}
	if request.URL.Path == RuntimeRoutePrefix {
		if request.Method != http.MethodGet {
			writeMethod(w)
			return
		}
		handler.list(w, request)
		return
	}
	if !strings.HasPrefix(request.URL.Path, RuntimeRoutePrefix+"/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	caseID := strings.TrimPrefix(request.URL.Path, RuntimeRoutePrefix+"/")
	if strings.Contains(caseID, "/") || !validCaseID(caseID) {
		writeError(w, http.StatusBadRequest, "invalid development workbench request")
		return
	}
	if request.Method != http.MethodGet {
		writeMethod(w)
		return
	}
	if request.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid development workbench request")
		return
	}
	detail, err := handler.Service.Get(request.Context(), caseID)
	if err != nil {
		writeOperationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func identityReadEncoding(header http.Header) bool {
	var values []string
	for name, candidates := range header {
		if strings.EqualFold(name, "Content-Encoding") {
			values = append(values, candidates...)
		}
	}
	return len(values) == 0 ||
		len(values) == 1 &&
			strings.EqualFold(strings.TrimSpace(values[0]), "identity")
}

func (handler *Handler) list(w http.ResponseWriter, request *http.Request) {
	values, err := strictQuery(request.URL.RawQuery)
	if err != nil {
		writeOperationError(w, err)
		return
	}
	limit, err := canonicalPositiveInt(values.Get("limit"), false)
	if err != nil || limit > MaximumCaseListLimit {
		writeOperationError(w, ErrInvalidRequest)
		return
	}
	pullNumber, err := canonicalPositiveInt64(values.Get("pull_number"), false)
	if err != nil {
		writeOperationError(w, ErrInvalidRequest)
		return
	}
	page, err := handler.Service.List(request.Context(), ListRequest{
		Repository: values.Get("repository"),
		PullNumber: pullNumber,
		Limit:      limit,
		Cursor:     values.Get("cursor"),
	})
	if err != nil {
		writeOperationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func strictQuery(rawQuery string) (url.Values, error) {
	if len(rawQuery) > maxQueryBytes {
		return nil, ErrInvalidRequest
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	allowed := map[string]struct{}{
		"repository":  {},
		"pull_number": {},
		"limit":       {},
		"cursor":      {},
	}
	for name, candidates := range values {
		if _, ok := allowed[name]; !ok ||
			len(candidates) != 1 ||
			candidates[0] == "" {
			return nil, ErrInvalidRequest
		}
	}
	return values, nil
}

func canonicalPositiveInt(value string, required bool) (int, error) {
	if value == "" && !required {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 || strconv.Itoa(parsed) != value {
		return 0, ErrInvalidRequest
	}
	return parsed, nil
}

func canonicalPositiveInt64(value string, required bool) (int64, error) {
	if value == "" && !required {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, ErrInvalidRequest
	}
	return parsed, nil
}

func writeOperationError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "development workbench operation failed"
	switch {
	case errors.Is(err, eventing.ErrNotFound):
		status, message = http.StatusNotFound, "development case not found"
	case errors.Is(err, ErrInvalidRequest),
		errors.Is(err, eventing.ErrInvalidPRDevelopment):
		status, message = http.StatusBadRequest, "invalid development workbench request"
	case errors.Is(err, ErrUnavailable):
		status, message = http.StatusServiceUnavailable, "development workbench unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		status, message = http.StatusGatewayTimeout, "development workbench timed out"
	}
	writeError(w, status, message)
}

func writeMethod(w http.ResponseWriter) {
	w.Header().Set("Allow", http.MethodGet)
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

func setResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
