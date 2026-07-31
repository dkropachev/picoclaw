package reviews

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	// RuntimeRoutePrefix is served inside the protected event-operator
	// generation. The launcher exposes the same operations under /api/reviews.
	RuntimeRoutePrefix = "/runtime/eventing/reviews"

	maxReviewRequestBody = 1 << 20
	maxReviewQueryBytes  = 8 << 10
	maxReviewJSONDepth   = 128
)

// Handler exposes one immutable Service generation over strict JSON routes.
type Handler struct {
	Service *Service
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	setReviewHeaders(w)
	if handler == nil || handler.Service == nil {
		writeReviewError(w, http.StatusServiceUnavailable, "review service unavailable", nil)
		return
	}
	if request == nil ||
		request.URL == nil ||
		request.URL.Fragment != "" ||
		request.URL.EscapedPath() != request.URL.Path {
		writeReviewError(w, http.StatusBadRequest, "invalid review request", nil)
		return
	}
	switch request.URL.Path {
	case RuntimeRoutePrefix:
		if request.Method != http.MethodGet {
			writeReviewMethod(w, http.MethodGet)
			return
		}
		handler.list(w, request)
		return
	}
	if !strings.HasPrefix(request.URL.Path, RuntimeRoutePrefix+"/") {
		writeReviewError(w, http.StatusNotFound, "not found", nil)
		return
	}
	segments := strings.Split(
		strings.TrimPrefix(request.URL.Path, RuntimeRoutePrefix+"/"),
		"/",
	)
	if len(segments) == 0 ||
		!validReviewID(segments[0], "prc_") {
		writeReviewError(w, http.StatusBadRequest, "invalid review request", nil)
		return
	}
	caseID := segments[0]
	if len(segments) == 1 {
		if request.Method != http.MethodGet {
			writeReviewMethod(w, http.MethodGet)
			return
		}
		handler.get(w, request, caseID)
		return
	}
	if request.URL.RawQuery != "" {
		writeReviewError(w, http.StatusBadRequest, "invalid review request", nil)
		return
	}
	switch {
	case len(segments) == 2 && segments[1] == "chat":
		if request.Method != http.MethodPost {
			writeReviewMethod(w, http.MethodPost)
			return
		}
		handler.chat(w, request, caseID)
	case len(segments) == 2 && segments[1] == "submit":
		if request.Method != http.MethodPost {
			writeReviewMethod(w, http.MethodPost)
			return
		}
		handler.submit(w, request, caseID)
	case len(segments) == 2 && segments[1] == "reconcile":
		if request.Method != http.MethodPost {
			writeReviewMethod(w, http.MethodPost)
			return
		}
		handler.reconcile(w, request, caseID)
	case len(segments) == 3 &&
		segments[1] == "findings" &&
		validReviewID(segments[2], "prf_"):
		if request.Method != http.MethodPatch {
			writeReviewMethod(w, http.MethodPatch)
			return
		}
		handler.updateFinding(w, request, caseID, segments[2])
	case len(segments) == 4 &&
		segments[1] == "findings" &&
		validReviewID(segments[2], "prf_"):
		if request.Method != http.MethodPost {
			writeReviewMethod(w, http.MethodPost)
			return
		}
		switch segments[3] {
		case "drop":
			handler.dropFinding(w, request, caseID, segments[2])
		case "restore":
			handler.restoreFinding(w, request, caseID, segments[2])
		case "rephrase":
			handler.rephrase(w, request, caseID, segments[2])
		default:
			writeReviewError(w, http.StatusNotFound, "not found", nil)
		}
	default:
		writeReviewError(w, http.StatusNotFound, "not found", nil)
	}
}

func (handler *Handler) list(w http.ResponseWriter, request *http.Request) {
	values, err := strictReviewQuery(request.URL.RawQuery)
	if err != nil {
		writeReviewOperationError(w, handler.Service, "", err)
		return
	}
	limit := 0
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || strconv.Itoa(limit) != raw {
			writeReviewOperationError(w, handler.Service, "", ErrInvalidRequest)
			return
		}
	}
	page, err := handler.Service.List(request.Context(), ListRequest{
		Status:     eventing.ReviewCaseStatus(values.Get("status")),
		Repository: values.Get("repository"),
		Limit:      limit,
		Cursor:     values.Get("cursor"),
	})
	if err != nil {
		writeReviewOperationError(w, handler.Service, "", err)
		return
	}
	writeReviewJSON(w, http.StatusOK, page)
}

func (handler *Handler) get(
	w http.ResponseWriter,
	request *http.Request,
	caseID string,
) {
	if request.URL.RawQuery != "" {
		writeReviewError(w, http.StatusBadRequest, "invalid review request", nil)
		return
	}
	detail, err := handler.Service.Get(request.Context(), caseID)
	if err != nil {
		writeReviewOperationError(w, handler.Service, caseID, err)
		return
	}
	writeReviewJSON(w, http.StatusOK, detail)
}

func (handler *Handler) updateFinding(
	w http.ResponseWriter,
	request *http.Request,
	caseID, findingID string,
) {
	var body struct {
		ExpectedVersion int64                       `json:"expected_version"`
		Finding         eventing.ReviewFindingDraft `json:"finding"`
	}
	if err := decodeReviewBody(w, request, &body); err != nil {
		writeReviewBodyError(w, err)
		return
	}
	detail, err := handler.Service.UpdateFinding(request.Context(), UpdateFindingRequest{
		CaseID:          caseID,
		FindingID:       findingID,
		ExpectedVersion: body.ExpectedVersion,
		Finding:         body.Finding,
	})
	if err != nil {
		writeReviewOperationError(w, handler.Service, caseID, err)
		return
	}
	writeReviewJSON(w, http.StatusOK, detail)
}

func (handler *Handler) dropFinding(
	w http.ResponseWriter,
	request *http.Request,
	caseID, findingID string,
) {
	body, ok := decodeTransitionBody(w, request)
	if !ok {
		return
	}
	detail, err := handler.Service.DropFinding(
		request.Context(),
		TransitionFindingRequest{
			CaseID:          caseID,
			FindingID:       findingID,
			ExpectedVersion: body.ExpectedVersion,
			Reason:          body.Reason,
		},
	)
	if err != nil {
		writeReviewOperationError(w, handler.Service, caseID, err)
		return
	}
	writeReviewJSON(w, http.StatusOK, detail)
}

func (handler *Handler) restoreFinding(
	w http.ResponseWriter,
	request *http.Request,
	caseID, findingID string,
) {
	body, ok := decodeTransitionBody(w, request)
	if !ok {
		return
	}
	detail, err := handler.Service.RestoreFinding(
		request.Context(),
		TransitionFindingRequest{
			CaseID:          caseID,
			FindingID:       findingID,
			ExpectedVersion: body.ExpectedVersion,
			Reason:          body.Reason,
		},
	)
	if err != nil {
		writeReviewOperationError(w, handler.Service, caseID, err)
		return
	}
	writeReviewJSON(w, http.StatusOK, detail)
}

func (handler *Handler) chat(
	w http.ResponseWriter,
	request *http.Request,
	caseID string,
) {
	var body struct {
		ExpectedVersion int64  `json:"expected_version"`
		FindingID       string `json:"finding_id,omitempty"`
		Content         string `json:"content"`
	}
	if err := decodeReviewBody(w, request, &body); err != nil {
		writeReviewBodyError(w, err)
		return
	}
	if body.FindingID != "" && !validReviewID(body.FindingID, "prf_") {
		writeReviewError(w, http.StatusBadRequest, "invalid review request", nil)
		return
	}
	detail, err := handler.Service.Chat(request.Context(), ChatRequest{
		CaseID:          caseID,
		ExpectedVersion: body.ExpectedVersion,
		FindingID:       body.FindingID,
		Content:         body.Content,
	})
	if err != nil {
		writeReviewOperationError(w, handler.Service, caseID, err)
		return
	}
	writeReviewJSON(w, http.StatusOK, detail)
}

func (handler *Handler) rephrase(
	w http.ResponseWriter,
	request *http.Request,
	caseID, findingID string,
) {
	var body struct {
		ExpectedVersion int64  `json:"expected_version"`
		Instruction     string `json:"instruction"`
	}
	if err := decodeReviewBody(w, request, &body); err != nil {
		writeReviewBodyError(w, err)
		return
	}
	result, err := handler.Service.Rephrase(request.Context(), RephraseRequest{
		CaseID:          caseID,
		FindingID:       findingID,
		ExpectedVersion: body.ExpectedVersion,
		Instruction:     body.Instruction,
	})
	if err != nil {
		writeReviewOperationError(w, handler.Service, caseID, err)
		return
	}
	writeReviewJSON(w, http.StatusOK, result)
}

func (handler *Handler) submit(
	w http.ResponseWriter,
	request *http.Request,
	caseID string,
) {
	var body struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if err := decodeReviewBody(w, request, &body); err != nil {
		writeReviewBodyError(w, err)
		return
	}
	detail, err := handler.Service.Submit(request.Context(), SubmitCaseRequest{
		CaseID:          caseID,
		ExpectedVersion: body.ExpectedVersion,
	})
	if err != nil {
		writeReviewOperationError(w, handler.Service, caseID, err)
		return
	}
	writeReviewJSON(w, http.StatusAccepted, detail)
}

func (handler *Handler) reconcile(
	w http.ResponseWriter,
	request *http.Request,
	caseID string,
) {
	var body struct {
		ExpectedVersion int64                                   `json:"expected_version"`
		Resolution      eventing.ReviewReconciliationResolution `json:"resolution"`
	}
	if err := decodeReviewBody(w, request, &body); err != nil {
		writeReviewBodyError(w, err)
		return
	}
	detail, err := handler.Service.Reconcile(
		request.Context(),
		ReconcileCaseRequest{
			CaseID:          caseID,
			ExpectedVersion: body.ExpectedVersion,
			Resolution:      body.Resolution,
		},
	)
	if err != nil {
		writeReviewOperationError(w, handler.Service, caseID, err)
		return
	}
	writeReviewJSON(w, http.StatusOK, detail)
}

type transitionBody struct {
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason,omitempty"`
}

func decodeTransitionBody(
	w http.ResponseWriter,
	request *http.Request,
) (transitionBody, bool) {
	var body transitionBody
	if err := decodeReviewBody(w, request, &body); err != nil {
		writeReviewBodyError(w, err)
		return transitionBody{}, false
	}
	return body, true
}

func decodeReviewBody(
	w http.ResponseWriter,
	request *http.Request,
	target any,
) error {
	if request.Body == nil ||
		!reviewJSONContentType(request.Header) ||
		!reviewIdentityEncoding(request.Header) {
		return ErrInvalidRequest
	}
	if request.ContentLength > maxReviewRequestBody {
		return &http.MaxBytesError{Limit: maxReviewRequestBody}
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, request.Body, maxReviewRequestBody))
	if err != nil {
		return err
	}
	if err := rejectDuplicateReviewJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrInvalidRequest
		}
		return err
	}
	return nil
}

func rejectDuplicateReviewJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var decodeValue func(int) error
	decodeValue = func(depth int) error {
		if depth > maxReviewJSONDepth {
			return ErrInvalidRequest
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return ErrInvalidRequest
				}
				if _, duplicate := seen[name]; duplicate {
					return ErrInvalidRequest
				}
				seen[name] = struct{}{}
				if err := decodeValue(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return ErrInvalidRequest
			}
		case '[':
			for decoder.More() {
				if err := decodeValue(depth + 1); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return ErrInvalidRequest
			}
		default:
			return ErrInvalidRequest
		}
		return nil
	}
	if err := decodeValue(0); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrInvalidRequest
		}
		return err
	}
	return nil
}

func strictReviewQuery(rawQuery string) (url.Values, error) {
	if len(rawQuery) > maxReviewQueryBytes {
		return nil, ErrInvalidRequest
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	allowed := map[string]struct{}{
		"status":     {},
		"repository": {},
		"limit":      {},
		"cursor":     {},
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

func reviewJSONContentType(header http.Header) bool {
	value, ok := exactlyOneReviewHeader(header, "Content-Type")
	if !ok {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(value)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	for name, parameter := range parameters {
		if !strings.EqualFold(name, "charset") ||
			!strings.EqualFold(parameter, "utf-8") {
			return false
		}
	}
	return true
}

func reviewIdentityEncoding(header http.Header) bool {
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

func exactlyOneReviewHeader(header http.Header, target string) (string, bool) {
	var values []string
	for name, candidates := range header {
		if strings.EqualFold(name, target) {
			values = append(values, candidates...)
		}
	}
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func writeReviewBodyError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeReviewError(
			w,
			http.StatusRequestEntityTooLarge,
			"review request body is too large",
			nil,
		)
		return
	}
	writeReviewError(w, http.StatusBadRequest, "invalid review request body", nil)
}

func writeReviewOperationError(
	w http.ResponseWriter,
	service *Service,
	caseID string,
	err error,
) {
	status := http.StatusInternalServerError
	message := "review operation failed"
	switch {
	case errors.Is(err, eventing.ErrNotFound):
		status, message = http.StatusNotFound, "review not found"
	case errors.Is(err, eventing.ErrReviewConflict):
		status, message = http.StatusConflict, "review changed; reload before retrying"
	case errors.Is(err, eventing.ErrInvalidTransition):
		status, message = http.StatusConflict, "review state does not allow this operation"
	case errors.Is(err, ErrInvalidRequest),
		errors.Is(err, eventing.ErrInvalidReview):
		status, message = http.StatusBadRequest, "invalid review request"
	case errors.Is(err, ErrUnavailable):
		status, message = http.StatusServiceUnavailable, "review service unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		status, message = http.StatusGatewayTimeout, "review operation timed out"
	}
	var latest *Detail
	if caseID != "" &&
		status != http.StatusNotFound &&
		service != nil {
		latestCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if detail, getErr := service.Get(latestCtx, caseID); getErr == nil {
			latest = &detail
		}
	}
	writeReviewError(w, status, message, latest)
}

func writeReviewError(
	w http.ResponseWriter,
	status int,
	message string,
	detail *Detail,
) {
	response := struct {
		Error  string  `json:"error"`
		Detail *Detail `json:"detail,omitempty"`
	}{
		Error:  message,
		Detail: detail,
	}
	writeReviewJSON(w, status, response)
}

func writeReviewMethod(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	writeReviewError(w, http.StatusMethodNotAllowed, "method not allowed", nil)
}

func writeReviewJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

func setReviewHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
