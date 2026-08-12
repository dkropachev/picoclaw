package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

const (
	reviewGatewayRequestTimeout   = 5 * time.Second
	reviewGatewayAIRequestTimeout = 120 * time.Second
	reviewGatewayProviderTimeout  = 30 * time.Second

	reviewProxyQueryMaxBytes   = 8 << 10
	reviewProxyRequestMaxBytes = 1 << 20
	// A captured review is bounded at 4 MiB and its durable transcript at
	// 4 MiB of UTF-8. Allow headroom for JSON escaping and public metadata
	// while retaining a hard launcher memory bound.
	reviewProxyResponseMaxBytes   = 32 << 20
	reviewProxyRepositoryMaxBytes = 512
	reviewProxyCursorMaxBytes     = 1 << 10
	reviewProxyMaximumLimit       = 100
)

type reviewQueryValidator func(string) error

var reviewListQueryContract = map[string]reviewQueryValidator{
	"status": enumReviewQueryValue(
		"status",
		"open",
		"all_dropped",
		"submitting",
		"submission_unknown",
		"submitted",
		"stale",
	),
	"repository": reviewRepositoryQueryValue,
	"limit":      reviewLimitQueryValue,
	"cursor": boundedReviewQueryValue(
		"cursor",
		reviewProxyCursorMaxBytes,
	),
}

var reviewGatewayPIDData = func() *ppid.PidFileData {
	// Review reads must not attach to a process, probe health, or clean stale
	// metadata. The explicit local-host validation below decides whether the
	// peeked connection authority is safe to receive the process bearer.
	return ppid.PeekPidFile(globalConfigDir())
}

func (h *Handler) registerReviewRoutes(mux *http.ServeMux) {
	mux.HandleFunc(
		"GET /api/reviews/attention-agents",
		h.handleGetReviewAttentionAgents,
	)
	// Keep an exact method fallback ahead of the case subtree. Without it,
	// unsupported methods would be interpreted as malformed review-case IDs.
	mux.HandleFunc(
		"/api/reviews/attention-agents",
		h.handleReviewAttentionAgentsMethodNotAllowed,
	)
	mux.HandleFunc(
		"GET /api/reviews/attention-policies",
		h.handleGetReviewAttentionPolicies,
	)
	mux.HandleFunc(
		"PUT /api/reviews/attention-policies",
		h.handlePutReviewAttentionPolicies,
	)
	// Keep an exact method fallback ahead of the case subtree. Without it,
	// unsupported methods would be interpreted as malformed review-case IDs.
	mux.HandleFunc(
		"/api/reviews/attention-policies",
		h.handleReviewAttentionPoliciesMethodNotAllowed,
	)
	mux.HandleFunc("/api/reviews", h.handleReviewList)
	mux.HandleFunc("/api/reviews/", h.handleReviewSubtree)
}

func (h *Handler) handleReviewList(w http.ResponseWriter, r *http.Request) {
	if !requireReviewMethod(w, r, http.MethodGet) {
		return
	}
	if !canonicalReviewRequestPath(r) ||
		r.URL.Path != "/api/reviews" {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid review request")
		return
	}
	query, err := validateReviewProxyQuery(r.URL.RawQuery)
	if err != nil {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid review query")
		return
	}
	h.proxyReviewGateway(
		w,
		r,
		http.MethodGet,
		"/runtime/eventing/reviews",
		query,
		nil,
		reviewGatewayRequestTimeout,
	)
}

func (h *Handler) handleReviewSubtree(w http.ResponseWriter, r *http.Request) {
	const prefix = "/api/reviews/"
	if r == nil ||
		r.URL == nil ||
		!strings.HasPrefix(r.URL.Path, prefix) ||
		!canonicalReviewRequestPath(r) {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid review request")
		return
	}
	segments := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if len(segments) == 0 || !validOperatorReviewCaseID(segments[0]) {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid review request")
		return
	}
	caseID := segments[0]
	switch {
	case len(segments) == 1:
		h.handleReviewGet(w, r, caseID)
	case len(segments) == 2 && segments[1] == "attention":
		h.handleReviewAttentionGet(w, r, caseID)
	case len(segments) == 2 && segments[1] == "provider":
		h.handleReviewProviderGet(w, r, caseID)
	case len(segments) == 3 && segments[1] == "provider" && segments[2] == "thread":
		h.handleReviewMutationValidated(
			w,
			r,
			http.MethodPost,
			"/runtime/eventing/reviews/"+caseID+"/provider/thread",
			reviewGatewayProviderTimeout,
			validateReviewProviderThreadBody,
		)
	case len(segments) == 3 &&
		segments[1] == "attention" &&
		segments[2] == "respond":
		h.handleReviewMutation(
			w,
			r,
			http.MethodPost,
			"/runtime/eventing/reviews/"+caseID+"/attention/respond",
			reviewGatewayAIRequestTimeout,
		)
	case len(segments) == 2 && segments[1] == "chat":
		h.handleReviewMutation(
			w,
			r,
			http.MethodPost,
			"/runtime/eventing/reviews/"+caseID+"/chat",
			reviewGatewayAIRequestTimeout,
		)
	case len(segments) == 2 && segments[1] == "submit":
		h.handleReviewMutation(
			w,
			r,
			http.MethodPost,
			"/runtime/eventing/reviews/"+caseID+"/submit",
			reviewGatewayRequestTimeout,
		)
	case len(segments) == 2 && segments[1] == "reconcile":
		h.handleReviewMutation(
			w,
			r,
			http.MethodPost,
			"/runtime/eventing/reviews/"+caseID+"/reconcile",
			reviewGatewayRequestTimeout,
		)
	case len(segments) == 3 &&
		segments[1] == "findings" &&
		validOperatorReviewFindingID(segments[2]):
		h.handleReviewMutation(
			w,
			r,
			http.MethodPatch,
			"/runtime/eventing/reviews/"+caseID+"/findings/"+segments[2],
			reviewGatewayRequestTimeout,
		)
	case len(segments) == 4 &&
		segments[1] == "findings" &&
		validOperatorReviewFindingID(segments[2]):
		timeout := reviewGatewayRequestTimeout
		if segments[3] == "rephrase" {
			timeout = reviewGatewayAIRequestTimeout
		}
		switch segments[3] {
		case "drop", "restore", "rephrase":
			h.handleReviewMutation(
				w,
				r,
				http.MethodPost,
				"/runtime/eventing/reviews/"+caseID+
					"/findings/"+segments[2]+"/"+segments[3],
				timeout,
			)
		default:
			writeReviewAPIError(w, http.StatusNotFound, "not found")
		}
	default:
		writeReviewAPIError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) handleReviewProviderGet(
	w http.ResponseWriter,
	r *http.Request,
	caseID string,
) {
	if !requireReviewMethod(w, r, http.MethodGet) {
		return
	}
	query := r.URL.RawQuery
	timeout := reviewGatewayProviderTimeout
	if query != "" {
		if query != "view=status" {
			writeReviewAPIError(w, http.StatusBadRequest, "invalid review request")
			return
		}
		timeout = reviewGatewayRequestTimeout
	}
	h.proxyReviewGateway(
		w,
		r,
		http.MethodGet,
		"/runtime/eventing/reviews/"+caseID+"/provider",
		query,
		nil,
		timeout,
	)
}

func validateReviewProviderThreadBody(_ *http.Request, raw []byte) error {
	if rejectDuplicateJSONKeys(raw, 8, nil) != nil {
		return errors.New("duplicate provider thread request field")
	}
	var body struct {
		Token  string `json:"token"`
		Action string `json:"action"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("provider thread request has trailing JSON")
	}
	if !validReviewProviderToken(body.Token) ||
		(body.Action != "resolve" && body.Action != "unresolve") {
		return errors.New("invalid provider thread request")
	}
	return nil
}

func validReviewProviderToken(value string) bool {
	if !strings.HasPrefix(value, "rtt_") {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "rtt_"))
	return err == nil && len(decoded) == sha256.Size
}

func (h *Handler) handleReviewAttentionGet(
	w http.ResponseWriter,
	r *http.Request,
	caseID string,
) {
	if !requireReviewMethod(w, r, http.MethodGet) {
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid review request")
		return
	}
	h.proxyReviewGateway(
		w,
		r,
		http.MethodGet,
		"/runtime/eventing/reviews/"+caseID+"/attention",
		"",
		nil,
		reviewGatewayRequestTimeout,
	)
}

func (h *Handler) handleReviewGet(
	w http.ResponseWriter,
	r *http.Request,
	caseID string,
) {
	if !requireReviewMethod(w, r, http.MethodGet) {
		return
	}
	if r.URL.RawQuery != "" {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid review request")
		return
	}
	h.proxyReviewGateway(
		w,
		r,
		http.MethodGet,
		"/runtime/eventing/reviews/"+caseID,
		"",
		nil,
		reviewGatewayRequestTimeout,
	)
}

func (h *Handler) handleReviewMutation(
	w http.ResponseWriter,
	r *http.Request,
	method string,
	upstreamPath string,
	timeout time.Duration,
) {
	h.handleReviewMutationValidated(w, r, method, upstreamPath, timeout, nil)
}

func (h *Handler) handleReviewMutationValidated(
	w http.ResponseWriter,
	r *http.Request,
	method string,
	upstreamPath string,
	timeout time.Duration,
	validate func(*http.Request, []byte) error,
) {
	if !requireReviewMethod(w, r, method) {
		return
	}
	if reviewMutationCrossSite(r) {
		writeReviewAPIError(
			w,
			http.StatusForbidden,
			"cross-site review request rejected",
		)
		return
	}
	if r.URL.RawQuery != "" {
		writeReviewAPIError(w, http.StatusBadRequest, "invalid review request")
		return
	}
	if err := validateEventReplayHeaders(r.Header); err != nil {
		writeReviewAPIError(
			w,
			http.StatusUnsupportedMediaType,
			"Content-Type must be application/json with identity encoding",
		)
		return
	}
	body, err := readReviewMutationBody(w, r)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeReviewAPIError(
				w,
				http.StatusRequestEntityTooLarge,
				"review request body is too large",
			)
			return
		}
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			"review request body must contain one JSON value",
		)
		return
	}
	if validate != nil && validate(r, body) != nil {
		writeReviewAPIError(
			w,
			http.StatusBadRequest,
			"invalid review request",
		)
		return
	}
	h.proxyReviewGateway(
		w,
		r,
		method,
		upstreamPath,
		"",
		body,
		timeout,
	)
}

func reviewMutationCrossSite(r *http.Request) bool {
	if r == nil {
		return true
	}
	fetchSites := reviewHeaderValues(r.Header, "Sec-Fetch-Site")
	origins := reviewHeaderValues(r.Header, "Origin")
	referers := reviewHeaderValues(r.Header, "Referer")
	if len(fetchSites) > 1 || len(origins) > 1 || len(referers) > 1 {
		return true
	}
	for _, raw := range append(origins, referers...) {
		if !sameLauncherRequestOrigin(r, strings.TrimSpace(raw)) {
			return true
		}
	}
	if len(fetchSites) == 1 {
		return !strings.EqualFold(strings.TrimSpace(fetchSites[0]), "same-origin")
	}
	// Non-browser callers do not have Fetch Metadata. They still need one
	// unambiguous, matching Origin or Referer instead of silently bypassing
	// the browser mutation boundary.
	return len(origins) == 0 && len(referers) == 0
}

func reviewHeaderValues(header http.Header, target string) []string {
	var values []string
	for name, candidates := range header {
		if strings.EqualFold(name, target) {
			values = append(values, candidates...)
		}
	}
	return values
}

func readReviewMutationBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, errors.New("missing JSON body")
	}
	if r.ContentLength > reviewProxyRequestMaxBytes {
		return nil, &http.MaxBytesError{Limit: reviewProxyRequestMaxBytes}
	}
	body, err := io.ReadAll(http.MaxBytesReader(
		w,
		r.Body,
		reviewProxyRequestMaxBytes,
	))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || !json.Valid(body) {
		return nil, errors.New("invalid JSON body")
	}
	return body, nil
}

func validateReviewProxyQuery(rawQuery string) (string, error) {
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
		validator, ok := reviewListQueryContract[name]
		if !ok || len(candidates) != 1 {
			return "", fmt.Errorf(
				"unsupported or repeated review query parameter %q",
				name,
			)
		}
		if err := validator(candidates[0]); err != nil {
			return "", err
		}
	}
	return values.Encode(), nil
}

func boundedReviewQueryValue(
	name string,
	maximum int,
) reviewQueryValidator {
	return func(value string) error {
		if value == "" ||
			!utf8.ValidString(value) ||
			value != strings.TrimSpace(value) ||
			len(value) > maximum {
			return fmt.Errorf("%s is invalid", name)
		}
		return nil
	}
}

func enumReviewQueryValue(
	name string,
	allowed ...string,
) reviewQueryValidator {
	values := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		values[value] = struct{}{}
	}
	return func(value string) error {
		if _, ok := values[value]; !ok {
			return fmt.Errorf("%s is invalid", name)
		}
		return nil
	}
}

func reviewRepositoryQueryValue(value string) error {
	if err := boundedReviewQueryValue(
		"repository",
		reviewProxyRepositoryMaxBytes,
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

func validReviewRepositoryPart(value string) bool {
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '_' ||
			character == '-' ||
			character == '.' {
			continue
		}
		return false
	}
	return true
}

func reviewLimitQueryValue(value string) error {
	limit, err := strconv.Atoi(value)
	if err != nil ||
		strconv.Itoa(limit) != value ||
		limit < 1 ||
		limit > reviewProxyMaximumLimit {
		return errors.New("limit is invalid")
	}
	return nil
}

func validOperatorReviewCaseID(value string) bool {
	return validOperatorPrefixedID(value, "prc_")
}

func validOperatorReviewFindingID(value string) bool {
	return validOperatorPrefixedID(value, "prf_")
}

func canonicalReviewRequestPath(r *http.Request) bool {
	return r != nil &&
		r.URL != nil &&
		!r.URL.ForceQuery &&
		r.URL.Fragment == "" &&
		r.URL.EscapedPath() == r.URL.Path
}

func requireReviewMethod(
	w http.ResponseWriter,
	r *http.Request,
	method string,
) bool {
	setReviewResponseHeaders(w)
	if r != nil && r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeReviewAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	return false
}

func (h *Handler) proxyReviewGateway(
	w http.ResponseWriter,
	r *http.Request,
	method string,
	upstreamPath string,
	rawQuery string,
	body []byte,
	timeout time.Duration,
) {
	setReviewResponseHeaders(w)

	var cfg *config.Config
	if loaded, err := config.LoadConfig(h.configPath); err == nil {
		cfg = loaded
	}
	pidData := reviewGatewayPIDData()
	// Review routes forward a process bearer. Require the PID record to name
	// an explicit numeric local address before constructing the upstream URL;
	// accepting a hostname, wildcard, or remote address could exfiltrate that
	// bearer if the record were stale or corrupted.
	if !validAgentActivityPIDData(pidData) {
		writeReviewAPIError(
			w,
			http.StatusServiceUnavailable,
			"review gateway unavailable",
		)
		return
	}
	target, err := h.eventGatewayURL(pidData, cfg, upstreamPath, rawQuery)
	if err != nil {
		writeReviewAPIError(
			w,
			http.StatusServiceUnavailable,
			"review gateway unavailable",
		)
		return
	}

	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	upstreamRequest, err := http.NewRequestWithContext(
		r.Context(),
		method,
		target.String(),
		requestBody,
	)
	if err != nil {
		writeReviewAPIError(
			w,
			http.StatusBadGateway,
			"invalid review gateway response",
		)
		return
	}
	upstreamRequest.Header.Set("Accept", "application/json")
	upstreamRequest.Header.Set("Authorization", "Bearer "+pidData.Token)
	if body != nil {
		upstreamRequest.Header.Set("Content-Type", "application/json")
	}

	response, err := eventGatewayDo(upstreamRequest, timeout)
	if err != nil {
		writeReviewAPIError(
			w,
			http.StatusServiceUnavailable,
			"review gateway unavailable",
		)
		return
	}
	if response == nil || response.Body == nil {
		writeReviewAPIError(
			w,
			http.StatusBadGateway,
			"invalid review gateway response",
		)
		return
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized ||
		response.StatusCode == http.StatusForbidden {
		writeReviewAPIError(
			w,
			http.StatusServiceUnavailable,
			"review gateway unavailable",
		)
		return
	}
	if response.StatusCode < 200 ||
		response.StatusCode > 599 ||
		response.StatusCode >= 300 && response.StatusCode < 400 {
		writeReviewAPIError(
			w,
			http.StatusBadGateway,
			"invalid review gateway response",
		)
		return
	}
	if response.ContentLength > reviewProxyResponseMaxBytes {
		writeReviewAPIError(
			w,
			http.StatusBadGateway,
			"invalid review gateway response",
		)
		return
	}
	responseBody, err := io.ReadAll(io.LimitReader(
		response.Body,
		reviewProxyResponseMaxBytes+1,
	))
	if err != nil ||
		len(responseBody) > reviewProxyResponseMaxBytes ||
		!eventGatewayJSONResponse(
			response.Header.Get("Content-Type"),
			responseBody,
		) {
		writeReviewAPIError(
			w,
			http.StatusBadGateway,
			"invalid review gateway response",
		)
		return
	}

	if location, ok := externalReviewLocation(
		exactlyOneReviewResponseHeader(response.Header, "Location"),
	); ok {
		w.Header().Set("Location", location)
	}
	w.Header().Set("Content-Type", "application/json")
	if response.StatusCode == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func exactlyOneReviewResponseHeader(
	header http.Header,
	target string,
) string {
	var values []string
	for name, candidates := range header {
		if strings.EqualFold(name, target) {
			values = append(values, candidates...)
		}
	}
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func externalReviewLocation(raw string) (string, bool) {
	const (
		internalPrefix = "/runtime/eventing/reviews"
		externalPrefix = "/api/reviews"
	)
	if raw == internalPrefix {
		return externalPrefix, true
	}
	if !strings.HasPrefix(raw, internalPrefix+"/") ||
		strings.ContainsAny(raw, "%?#") {
		return "", false
	}
	segments := strings.Split(strings.TrimPrefix(raw, internalPrefix+"/"), "/")
	if len(segments) == 1 && validOperatorReviewCaseID(segments[0]) {
		return externalPrefix + "/" + segments[0], true
	}
	if len(segments) == 3 &&
		validOperatorReviewCaseID(segments[0]) &&
		segments[1] == "findings" &&
		validOperatorReviewFindingID(segments[2]) {
		return externalPrefix + "/" + strings.Join(segments, "/"), true
	}
	return "", false
}

func setReviewResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeReviewAPIError(
	w http.ResponseWriter,
	status int,
	message string,
) {
	setReviewResponseHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
