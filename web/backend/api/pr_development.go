package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/prdevelopment"
)

const (
	prDevelopmentAPIPath     = "/api/pr-development"
	prDevelopmentRuntimePath = "/runtime/eventing/pr-development"
	prDevelopmentChatBytes   = 32 << 10
	prDevelopmentRepairBytes = prdevelopment.MaximumRepairInstructionBytes
	prDevelopmentChatDepth   = 16
)

var prDevelopmentListQueryContract = map[string]reviewQueryValidator{
	"repository":  prDevelopmentRepositoryQueryValue,
	"pull_number": prDevelopmentPullNumberQueryValue,
	"limit":       reviewLimitQueryValue,
	"cursor": boundedReviewQueryValue(
		"cursor",
		reviewProxyCursorMaxBytes,
	),
}

// GuardPRDevelopmentCanonicalPaths must wrap the ServeMux that owns the
// launcher API routes. net/http's ServeMux redirects paths containing dot or
// duplicate-slash segments before an exact route handler can reject them, so
// this guard keeps the PR development surface fail-closed instead.
func GuardPRDevelopmentCanonicalPaths(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noncanonicalPRDevelopmentPath(r) {
			writeReviewAPIError(
				w,
				http.StatusBadRequest,
				"invalid development request",
			)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func noncanonicalPRDevelopmentPath(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	if !pathTraversesPRDevelopment(r.URL.Path) {
		return false
	}
	cleaned := path.Clean(r.URL.Path)
	return r.URL.Path != cleaned || r.URL.EscapedPath() != r.URL.Path
}

func pathTraversesPRDevelopment(requestPath string) bool {
	segments := make([]string, 0, strings.Count(requestPath, "/"))
	for _, segment := range strings.Split(requestPath, "/") {
		switch segment {
		case "", ".":
			continue
		case "..":
			if len(segments) > 0 {
				segments = segments[:len(segments)-1]
			}
		default:
			segments = append(segments, segment)
		}
		current := "/" + strings.Join(segments, "/")
		if current == prDevelopmentAPIPath ||
			strings.HasPrefix(current, prDevelopmentAPIPath+"/") {
			return true
		}
	}
	return false
}

func prDevelopmentRepositoryQueryValue(value string) error {
	if err := boundedReviewQueryValue(
		"repository",
		prdevelopment.MaximumRepositoryBytes,
	)(value); err != nil {
		return err
	}
	owner, repository, ok := strings.Cut(value, "/")
	if !ok ||
		owner == "" ||
		repository == "" ||
		strings.Contains(repository, "/") ||
		!validReviewRepositoryPart(owner) ||
		!validReviewRepositoryPart(repository) {
		return errors.New("repository is invalid")
	}
	return nil
}

func (h *Handler) registerPRDevelopmentRoutes(mux *http.ServeMux) {
	mux.HandleFunc(prDevelopmentAPIPath, h.handlePRDevelopmentList)
	mux.HandleFunc(prDevelopmentAPIPath+"/", h.handlePRDevelopmentDetail)
}

func (h *Handler) handlePRDevelopmentList(w http.ResponseWriter, r *http.Request) {
	if !requireReviewMethod(w, r, http.MethodGet) {
		return
	}
	if invalidPRDevelopmentReadBody(r) {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid development request")
		return
	}
	if !canonicalReviewRequestPath(r) || r.URL.Path != prDevelopmentAPIPath {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid development request")
		return
	}
	query, err := validatePRDevelopmentQuery(r.URL.RawQuery)
	if err != nil {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid development query")
		return
	}
	h.proxyReviewGateway(
		w,
		r,
		http.MethodGet,
		prDevelopmentRuntimePath,
		query,
		nil,
		reviewGatewayRequestTimeout,
	)
}

func (h *Handler) handlePRDevelopmentDetail(w http.ResponseWriter, r *http.Request) {
	if !canonicalReviewRequestPath(r) {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid development request")
		return
	}
	segments := strings.Split(r.URL.Path[len(prDevelopmentAPIPath)+1:], "/")
	if len(segments) == 0 {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid development request")
		return
	}
	caseID := segments[0]
	if !validOperatorPrefixedID(caseID, "pdc_") {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid development request")
		return
	}
	switch {
	case len(segments) == 1:
		if !requireReviewMethod(w, r, http.MethodGet) {
			return
		}
		if invalidPRDevelopmentReadBody(r) ||
			r.URL.RawQuery != "" || r.URL.ForceQuery {
			writeReviewAPIError(w, http.StatusBadRequest, "invalid development request")
			return
		}
		h.proxyReviewGateway(
			w,
			r,
			http.MethodGet,
			prDevelopmentRuntimePath+"/"+caseID,
			"",
			nil,
			reviewGatewayRequestTimeout,
		)
	case len(segments) == 2 && segments[1] == "chat":
		h.handleReviewMutationValidated(
			w,
			r,
			http.MethodPost,
			prDevelopmentRuntimePath+"/"+caseID+"/chat",
			reviewGatewayAIRequestTimeout,
			validatePRDevelopmentChatMutation,
		)
	case len(segments) == 2 && segments[1] == "attention":
		if !requireReviewMethod(w, r, http.MethodGet) {
			return
		}
		if invalidPRDevelopmentReadBody(r) ||
			r.URL.RawQuery != "" || r.URL.ForceQuery {
			writeReviewAPIError(w, http.StatusBadRequest, "invalid development request")
			return
		}
		h.proxyReviewGateway(
			w,
			r,
			http.MethodGet,
			prDevelopmentRuntimePath+"/"+caseID+"/attention",
			"",
			nil,
			reviewGatewayRequestTimeout,
		)
	case len(segments) == 3 && segments[1] == "attention" &&
		segments[2] == "respond":
		h.handleReviewMutationValidated(
			w,
			r,
			http.MethodPost,
			prDevelopmentRuntimePath+"/"+caseID+"/attention/respond",
			reviewGatewayAIRequestTimeout,
			validatePRDevelopmentAttentionResponse,
		)
	case len(segments) == 2 && segments[1] == "repair":
		h.handleReviewMutationValidated(
			w,
			r,
			http.MethodPost,
			prDevelopmentRuntimePath+"/"+caseID+"/repair",
			reviewGatewayRequestTimeout,
			validatePRDevelopmentRepairMutation,
		)
	default:
		writeReviewAPIError(w, http.StatusNotFound, "not found")
	}
}

func validatePRDevelopmentAttentionResponse(r *http.Request, raw []byte) error {
	if r == nil || r.ContentLength < 0 || len(r.TransferEncoding) != 0 ||
		len(raw) == 0 || !utf8.Valid(raw) || !validJSONUnicodeScalars(raw) ||
		rejectDuplicateJSONKeys(raw, prDevelopmentChatDepth, nil) != nil ||
		validateExactPRDevelopmentAttentionResponseFields(raw) != nil {
		return errors.New("invalid development attention response")
	}
	var body struct {
		ExpectedCaseVersion *int64  `json:"expected_case_version"`
		ResponseToken       *string `json:"response_token"`
		Response            *string `json:"response"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("development attention response contains trailing JSON")
		}
		return err
	}
	if body.ExpectedCaseVersion == nil || *body.ExpectedCaseVersion < 0 ||
		*body.ExpectedCaseVersion > int64(prdevelopment.MaximumConversationVersion) ||
		body.ResponseToken == nil || !validPRDevelopmentAttentionResponseToken(
		*body.ResponseToken,
	) || body.Response == nil {
		return errors.New("development attention response fields are invalid")
	}
	response := strings.TrimSpace(*body.Response)
	if response == "" || response != *body.Response || !utf8.ValidString(response) ||
		strings.ContainsRune(response, '\x00') || len(response) > prDevelopmentChatBytes {
		return errors.New("development attention response is invalid")
	}
	return nil
}

func validateExactPRDevelopmentAttentionResponseFields(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 3 {
		return errors.New("development attention response fields are invalid")
	}
	for _, name := range []string{
		"expected_case_version",
		"response_token",
		"response",
	} {
		if _, ok := fields[name]; !ok {
			return errors.New("development attention response fields are invalid")
		}
	}
	return nil
}

func validPRDevelopmentAttentionResponseToken(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+64 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validatePRDevelopmentRepairMutation(r *http.Request, raw []byte) error {
	if r == nil || r.ContentLength < 0 || len(r.TransferEncoding) != 0 ||
		len(raw) == 0 || !utf8.Valid(raw) || !validJSONUnicodeScalars(raw) ||
		rejectDuplicateJSONKeys(raw, prDevelopmentChatDepth, nil) != nil ||
		validateExactPRDevelopmentRepairFields(raw) != nil {
		return errors.New("invalid development repair request")
	}
	var body struct {
		ExpectedConversationVersion *int64  `json:"expected_conversation_version"`
		ExpectedRepairRevision      *int64  `json:"expected_repair_revision"`
		RequestID                   *string `json:"request_id"`
		Instruction                 *string `json:"instruction"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("development repair contains trailing JSON")
		}
		return err
	}
	if body.ExpectedConversationVersion == nil ||
		*body.ExpectedConversationVersion < 0 ||
		*body.ExpectedConversationVersion > int64(prdevelopment.MaximumConversationVersion) ||
		body.ExpectedRepairRevision == nil ||
		*body.ExpectedRepairRevision < 0 ||
		*body.ExpectedRepairRevision > int64(prdevelopment.MaximumRepairRevision) ||
		body.RequestID == nil || !validOperatorPrefixedID(*body.RequestID, "prq_") ||
		body.Instruction == nil {
		return errors.New("development repair fields are invalid")
	}
	instruction := strings.TrimSpace(*body.Instruction)
	if instruction == "" || !utf8.ValidString(instruction) ||
		strings.ContainsRune(instruction, '\x00') ||
		len(instruction) > prDevelopmentRepairBytes {
		return errors.New("development repair instruction is invalid")
	}
	return nil
}

func validateExactPRDevelopmentRepairFields(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 4 {
		return errors.New("development repair fields are invalid")
	}
	for _, name := range []string{
		"expected_conversation_version",
		"expected_repair_revision",
		"request_id",
		"instruction",
	} {
		if _, ok := fields[name]; !ok {
			return errors.New("development repair fields are invalid")
		}
	}
	return nil
}

func validatePRDevelopmentChatMutation(r *http.Request, raw []byte) error {
	if r == nil || r.ContentLength < 0 || len(r.TransferEncoding) != 0 ||
		len(raw) == 0 || !utf8.Valid(raw) || !validJSONUnicodeScalars(raw) ||
		rejectDuplicateJSONKeys(raw, prDevelopmentChatDepth, nil) != nil ||
		validateExactPRDevelopmentChatFields(raw) != nil {
		return errors.New("invalid development chat request")
	}
	var body struct {
		ExpectedVersion *int64  `json:"expected_version"`
		Content         *string `json:"content"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("development chat contains trailing JSON")
		}
		return err
	}
	if body.ExpectedVersion == nil || *body.ExpectedVersion < 0 ||
		*body.ExpectedVersion > int64(prdevelopment.MaximumConversationVersion) ||
		body.Content == nil {
		return errors.New("development chat fields are invalid")
	}
	content := strings.TrimSpace(*body.Content)
	if content == "" || !utf8.ValidString(content) ||
		strings.ContainsRune(content, '\x00') || len(content) > prDevelopmentChatBytes {
		return errors.New("development chat content is invalid")
	}
	return nil
}

func validateExactPRDevelopmentChatFields(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 2 {
		return errors.New("development chat fields are invalid")
	}
	if _, ok := fields["expected_version"]; !ok {
		return errors.New("development chat fields are invalid")
	}
	if _, ok := fields["content"]; !ok {
		return errors.New("development chat fields are invalid")
	}
	return nil
}

func invalidPRDevelopmentReadBody(r *http.Request) bool {
	if r == nil || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return true
	}
	encodings := reviewHeaderValues(r.Header, "Content-Encoding")
	return len(encodings) > 1 ||
		len(encodings) == 1 &&
			!strings.EqualFold(strings.TrimSpace(encodings[0]), "identity")
}

func validatePRDevelopmentQuery(rawQuery string) (string, error) {
	if len(rawQuery) > reviewProxyQueryMaxBytes {
		return "", errors.New("query is too large")
	}
	if rawQuery == "" {
		return "", nil
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", err
	}
	for name, candidates := range values {
		validator, ok := prDevelopmentListQueryContract[name]
		if !ok || len(candidates) != 1 {
			return "", fmt.Errorf(
				"unsupported or repeated development query parameter %q",
				name,
			)
		}
		if err := validator(candidates[0]); err != nil {
			return "", err
		}
	}
	return values.Encode(), nil
}

func prDevelopmentPullNumberQueryValue(value string) error {
	pullNumber, err := strconv.ParseInt(value, 10, 64)
	if err != nil ||
		pullNumber <= 0 ||
		pullNumber > prdevelopment.MaximumPullNumber ||
		strconv.FormatInt(pullNumber, 10) != value {
		return errors.New("pull number is invalid")
	}
	return nil
}
