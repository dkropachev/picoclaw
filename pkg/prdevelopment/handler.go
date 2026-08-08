package prdevelopment

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
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	// RuntimeRoutePrefix is reachable only through the protected event-operator
	// generation. The launcher mirrors it under /api/pr-development.
	RuntimeRoutePrefix = "/runtime/eventing/pr-development"
	maxQueryBytes      = 8 << 10
	maxChatRequestBody = 1 << 20
	maxChatJSONDepth   = 16
)

var errInvalidChatMediaType = errors.New("invalid development chat media type")

// Handler exposes one immutable Service generation over exact read routes and
// one bounded, case-owned advisory chat mutation.
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
		request.URL.ForceQuery ||
		request.URL.Fragment != "" ||
		request.URL.EscapedPath() != request.URL.Path {
		writeError(w, http.StatusBadRequest, "invalid development workbench request")
		return
	}
	if request.URL.Path == RuntimeRoutePrefix {
		if request.Method != http.MethodGet {
			writeMethod(w, http.MethodGet)
			return
		}
		if invalidReadRequest(request) {
			writeError(w, http.StatusBadRequest, "invalid development workbench request")
			return
		}
		handler.list(w, request)
		return
	}
	if !strings.HasPrefix(request.URL.Path, RuntimeRoutePrefix+"/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	segments := strings.Split(
		strings.TrimPrefix(request.URL.Path, RuntimeRoutePrefix+"/"),
		"/",
	)
	if len(segments) == 0 || !validCaseID(segments[0]) {
		writeError(w, http.StatusBadRequest, "invalid development workbench request")
		return
	}
	if request.URL.RawQuery != "" || request.URL.ForceQuery {
		writeError(w, http.StatusBadRequest, "invalid development workbench request")
		return
	}
	caseID := segments[0]
	switch {
	case len(segments) == 1:
		if request.Method != http.MethodGet {
			writeMethod(w, http.MethodGet)
			return
		}
		if invalidReadRequest(request) {
			writeError(w, http.StatusBadRequest, "invalid development workbench request")
			return
		}
		detail, err := handler.Service.Get(request.Context(), caseID)
		if err != nil {
			writeOperationError(w, err, nil)
			return
		}
		writeJSON(w, http.StatusOK, detail)
	case len(segments) == 2 && segments[1] == "chat":
		if request.Method != http.MethodPost {
			writeMethod(w, http.MethodPost)
			return
		}
		handler.chat(w, request, caseID)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func invalidReadRequest(request *http.Request) bool {
	return request == nil ||
		request.ContentLength != 0 ||
		len(request.TransferEncoding) != 0 ||
		!identityReadEncoding(request.Header)
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
		writeOperationError(w, err, nil)
		return
	}
	limit, err := canonicalPositiveInt(values.Get("limit"), false)
	if err != nil || limit > MaximumCaseListLimit {
		writeOperationError(w, ErrInvalidRequest, nil)
		return
	}
	pullNumber, err := canonicalPositiveInt64(values.Get("pull_number"), false)
	if err != nil {
		writeOperationError(w, ErrInvalidRequest, nil)
		return
	}
	page, err := handler.Service.List(request.Context(), ListRequest{
		Repository: values.Get("repository"),
		PullNumber: pullNumber,
		Limit:      limit,
		Cursor:     values.Get("cursor"),
	})
	if err != nil {
		writeOperationError(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (handler *Handler) chat(
	w http.ResponseWriter,
	request *http.Request,
	caseID string,
) {
	if hasChatBrowserProvenance(request.Header) {
		writeError(w, http.StatusForbidden, "cross-site development chat request rejected")
		return
	}
	var body struct {
		ExpectedVersion *int64  `json:"expected_version"`
		Content         *string `json:"content"`
	}
	if err := decodeChatBody(w, request, &body); err != nil {
		var maximum *http.MaxBytesError
		switch {
		case errors.As(err, &maximum):
			writeError(w, http.StatusRequestEntityTooLarge, "development chat request is too large")
		case errors.Is(err, errInvalidChatMediaType):
			writeError(w, http.StatusUnsupportedMediaType, "development chat requires JSON with identity encoding")
		default:
			writeError(w, http.StatusBadRequest, "invalid development chat request")
		}
		return
	}
	if body.ExpectedVersion == nil || body.Content == nil {
		writeError(w, http.StatusBadRequest, "invalid development chat request")
		return
	}
	detail, err := handler.Service.Chat(request.Context(), ChatRequest{
		CaseID:          caseID,
		ExpectedVersion: *body.ExpectedVersion,
		Content:         *body.Content,
	})
	if err != nil {
		var latest *Detail
		if !errors.Is(err, ErrInvalidRequest) &&
			!errors.Is(err, eventing.ErrInvalidPRDevelopment) &&
			!errors.Is(err, eventing.ErrNotFound) {
			latestCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if current, getErr := handler.Service.Get(latestCtx, caseID); getErr == nil {
				latest = &current
			}
		}
		writeOperationError(w, err, latest)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func hasChatBrowserProvenance(header http.Header) bool {
	for name := range header {
		if strings.EqualFold(name, "Origin") ||
			strings.EqualFold(name, "Referer") ||
			strings.EqualFold(name, "Sec-Fetch-Site") {
			return true
		}
	}
	return false
}

func decodeChatBody(
	w http.ResponseWriter,
	request *http.Request,
	target any,
) error {
	if request == nil || request.Body == nil ||
		!chatJSONContentType(request.Header) ||
		!identityReadEncoding(request.Header) {
		return errInvalidChatMediaType
	}
	if request.ContentLength <= 0 || len(request.TransferEncoding) != 0 {
		return ErrInvalidRequest
	}
	if request.ContentLength > maxChatRequestBody {
		return &http.MaxBytesError{Limit: maxChatRequestBody}
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, request.Body, maxChatRequestBody))
	if err != nil {
		return err
	}
	if len(raw) == 0 || !utf8.Valid(raw) ||
		validateProviderJSONStringEncoding(raw) != nil ||
		rejectDuplicateChatJSONKeys(raw) != nil ||
		validateExactChatJSONFields(raw) != nil {
		return ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrInvalidRequest
		}
		return err
	}
	return nil
}

func validateExactChatJSONFields(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 2 {
		return ErrInvalidRequest
	}
	if _, ok := fields["expected_version"]; !ok {
		return ErrInvalidRequest
	}
	if _, ok := fields["content"]; !ok {
		return ErrInvalidRequest
	}
	return nil
}

func chatJSONContentType(header http.Header) bool {
	value, ok := exactlyOneHeader(header, "Content-Type")
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

func exactlyOneHeader(header http.Header, target string) (string, bool) {
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

func rejectDuplicateChatJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var consume func(int) error
	consume = func(depth int) error {
		if depth > maxChatJSONDepth {
			return ErrInvalidRequest
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				nameToken, tokenErr := decoder.Token()
				name, ok := nameToken.(string)
				if tokenErr != nil || !ok {
					return ErrInvalidRequest
				}
				if _, duplicate := seen[name]; duplicate {
					return ErrInvalidRequest
				}
				seen[name] = struct{}{}
				if err = consume(depth + 1); err != nil {
					return err
				}
			}
			closing, closeErr := decoder.Token()
			if closeErr != nil || closing != json.Delim('}') {
				return ErrInvalidRequest
			}
		case '[':
			for decoder.More() {
				if err = consume(depth + 1); err != nil {
					return err
				}
			}
			closing, closeErr := decoder.Token()
			if closeErr != nil || closing != json.Delim(']') {
				return ErrInvalidRequest
			}
		default:
			return ErrInvalidRequest
		}
		return nil
	}
	if err := consume(0); err != nil {
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

func writeOperationError(w http.ResponseWriter, err error, latest *Detail) {
	status := http.StatusInternalServerError
	message := "development workbench operation failed"
	switch {
	case errors.Is(err, eventing.ErrNotFound):
		status, message = http.StatusNotFound, "development case not found"
	case errors.Is(err, eventing.ErrPRDevelopmentConversationConflict):
		status, message = http.StatusConflict, "development conversation changed; reload before retrying"
	case errors.Is(err, eventing.ErrPRDevelopmentConversationCapacity):
		status, message = http.StatusConflict, "development conversation has reached its limit"
	case errors.Is(err, ErrInvalidRequest),
		errors.Is(err, eventing.ErrInvalidPRDevelopment):
		status, message = http.StatusBadRequest, "invalid development workbench request"
	case errors.Is(err, ErrUnavailable):
		status, message = http.StatusServiceUnavailable, "development workbench unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		status, message = http.StatusGatewayTimeout, "development workbench timed out"
	}
	writeErrorWithDetail(w, status, message, latest)
}

func writeMethod(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeErrorWithDetail(w, status, message, nil)
}

func writeErrorWithDetail(
	w http.ResponseWriter,
	status int,
	message string,
	detail *Detail,
) {
	writeJSON(w, status, struct {
		Error  string  `json:"error"`
		Detail *Detail `json:"detail,omitempty"`
	}{Error: message, Detail: detail})
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
