package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
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
	eventGatewayRequestTimeout  = 5 * time.Second
	eventProxyQueryMaxBytes     = 8 << 10
	eventProxyJSONMaxBytes      = 4 << 20
	eventProxyPayloadMaxBytes   = 32 << 20
	eventReplayRequestMaxBytes  = 1 << 10
	eventProxyCursorMaxBytes    = 1 << 10
	eventProxyWorkflowMaxBytes  = 1 << 10
	eventProxySourceMaxBytes    = 128
	eventProxyConnectorMaxBytes = 256
	eventProxyTypeMaxBytes      = 256
	eventProxyMaximumLimit      = 100

	eventReplayUnknownOutcomeMessage = "replay outcome unknown; inspect events before retrying"
)

var (
	eventGatewayDo = func(req *http.Request, timeout time.Duration) (*http.Response, error) {
		client := newEventGatewayHTTPClient(timeout)
		return client.Do(req)
	}
	eventGatewayPIDData = func(h *Handler, cfg *config.Config) *ppid.PidFileData {
		return h.liveEventGatewayPIDData(cfg)
	}
)

func newEventGatewayHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Event operations are process-local. Never disclose the PID bearer token
	// to an HTTP proxy inherited from the launcher's environment.
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type eventQueryValidator func(string) error

var eventListQueryContract = map[string]eventQueryValidator{
	"source":         boundedEventQueryValue("source", eventProxySourceMaxBytes),
	"connector":      boundedEventQueryValue("connector", eventProxyConnectorMaxBytes),
	"type":           boundedEventQueryValue("type", eventProxyTypeMaxBytes),
	"routing_status": enumEventQueryValue("routing_status", "pending", "claimed", "succeeded", "dead"),
	"limit":          eventLimitQueryValue,
	"cursor":         boundedEventQueryValue("cursor", eventProxyCursorMaxBytes),
}

var eventDispatchQueryContract = map[string]eventQueryValidator{
	"event_id":     eventIDQueryValue,
	"workflow_ref": boundedEventQueryValue("workflow_ref", eventProxyWorkflowMaxBytes),
	"status":       enumEventQueryValue("status", "pending", "claimed", "running", "succeeded", "failed", "dead"),
	"limit":        eventLimitQueryValue,
	"cursor":       boundedEventQueryValue("cursor", eventProxyCursorMaxBytes),
}

func (h *Handler) registerEventRoutes(mux *http.ServeMux) {
	h.registerEventSourceCollectionRoutes(mux)
	mux.HandleFunc("/api/events", h.handleEventList)
	mux.HandleFunc("/api/events/dispatches", h.handleEventDispatchList)
	mux.HandleFunc("/api/events/", h.handleEventSubtree)
}

func (h *Handler) handleEventSubtree(w http.ResponseWriter, r *http.Request) {
	const prefix = "/api/events/"
	if r == nil || r.URL == nil || !strings.HasPrefix(r.URL.Path, prefix) {
		writeEventAPIError(w, http.StatusNotFound, "not found")
		return
	}
	segments := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if len(segments) == 2 && segments[0] == "dispatches" && segments[1] != "" {
		r.SetPathValue("id", segments[1])
		h.handleEventDispatchGet(w, r)
		return
	}
	if len(segments) == 1 && segments[0] != "" {
		r.SetPathValue("id", segments[0])
		h.handleEventGet(w, r)
		return
	}
	if len(segments) == 2 && segments[0] != "" {
		r.SetPathValue("id", segments[0])
		switch segments[1] {
		case "payload":
			h.handleEventPayload(w, r)
			return
		case "replay":
			h.handleEventReplay(w, r)
			return
		}
	}
	writeEventAPIError(w, http.StatusNotFound, "not found")
}

func (h *Handler) handleEventList(w http.ResponseWriter, r *http.Request) {
	if !requireEventMethod(w, r, http.MethodGet) {
		return
	}
	if !canonicalEventRequestPath(r) {
		writeEventAPIError(w, http.StatusBadRequest, "invalid event request")
		return
	}
	query, err := validateEventProxyQuery(r.URL.RawQuery, eventListQueryContract)
	if err != nil {
		writeEventAPIError(w, http.StatusBadRequest, "invalid event query")
		return
	}
	h.proxyEventGateway(w, r, "/runtime/eventing/events", query, nil, false, false)
}

func (h *Handler) handleEventDispatchList(w http.ResponseWriter, r *http.Request) {
	if !requireEventMethod(w, r, http.MethodGet) {
		return
	}
	if !canonicalEventRequestPath(r) {
		writeEventAPIError(w, http.StatusBadRequest, "invalid dispatch request")
		return
	}
	query, err := validateEventProxyQuery(r.URL.RawQuery, eventDispatchQueryContract)
	if err != nil {
		writeEventAPIError(w, http.StatusBadRequest, "invalid dispatch query")
		return
	}
	h.proxyEventGateway(w, r, "/runtime/eventing/dispatches", query, nil, false, false)
}

func (h *Handler) handleEventDispatchGet(w http.ResponseWriter, r *http.Request) {
	if !requireEventMethod(w, r, http.MethodGet) {
		return
	}
	const prefix = "/api/events/dispatches/"
	id := r.PathValue("id")
	if !canonicalEventRequestPath(r) ||
		r.URL.Path != prefix+id ||
		!validOperatorDispatchID(id) ||
		r.URL.RawQuery != "" {
		writeEventAPIError(w, http.StatusBadRequest, "invalid dispatch request")
		return
	}
	h.proxyEventGateway(
		w,
		r,
		"/runtime/eventing/dispatches/"+id,
		"",
		nil,
		false,
		false,
	)
}

func (h *Handler) handleEventGet(w http.ResponseWriter, r *http.Request) {
	if !requireEventMethod(w, r, http.MethodGet) {
		return
	}
	id := r.PathValue("id")
	if !canonicalEventRequestPath(r) ||
		!validOperatorEventID(id) ||
		r.URL.RawQuery != "" {
		writeEventAPIError(w, http.StatusBadRequest, "invalid event request")
		return
	}
	h.proxyEventGateway(
		w,
		r,
		"/runtime/eventing/events/"+id,
		"",
		nil,
		false,
		false,
	)
}

func (h *Handler) handleEventPayload(w http.ResponseWriter, r *http.Request) {
	if !requireEventMethod(w, r, http.MethodGet) {
		return
	}
	id := r.PathValue("id")
	if !canonicalEventRequestPath(r) ||
		!validOperatorEventID(id) ||
		r.URL.RawQuery != "" {
		writeEventAPIError(w, http.StatusBadRequest, "invalid event payload request")
		return
	}
	h.proxyEventGateway(
		w,
		r,
		"/runtime/eventing/events/"+id+"/payload",
		"",
		nil,
		true,
		false,
	)
}

func (h *Handler) handleEventReplay(w http.ResponseWriter, r *http.Request) {
	if !requireEventMethod(w, r, http.MethodPost) {
		return
	}
	if eventReplayCrossSite(r) {
		writeEventAPIError(w, http.StatusForbidden, "cross-site replay request rejected")
		return
	}
	id := r.PathValue("id")
	if !canonicalEventRequestPath(r) ||
		!validOperatorEventID(id) ||
		r.URL.RawQuery != "" {
		writeEventAPIError(w, http.StatusBadRequest, "invalid event replay request")
		return
	}
	if err := validateEventReplayHeaders(r.Header); err != nil {
		writeEventAPIError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	if err := decodeEmptyEventReplayBody(w, r); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeEventAPIError(w, http.StatusRequestEntityTooLarge, "event replay request body is too large")
			return
		}
		writeEventAPIError(w, http.StatusBadRequest, "event replay request must be one empty JSON object")
		return
	}
	h.proxyEventGateway(
		w,
		r,
		"/runtime/eventing/events/"+id+"/replay",
		"",
		[]byte("{}"),
		false,
		true,
	)
}

func eventReplayCrossSite(r *http.Request) bool {
	fetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if fetchSite == "same-origin" {
		return launcherSetupCrossSite(r)
	}
	if fetchSite != "" {
		return true
	}
	if strings.TrimSpace(r.Header.Get("Origin")) == "" &&
		strings.TrimSpace(r.Header.Get("Referer")) == "" {
		return true
	}
	return launcherSetupCrossSite(r)
}

func requireEventMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	setEventResponseHeaders(w)
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeEventAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
	return false
}

func canonicalEventRequestPath(r *http.Request) bool {
	return r != nil &&
		r.URL != nil &&
		r.URL.Fragment == "" &&
		r.URL.EscapedPath() == r.URL.Path
}

func validateEventReplayHeaders(header http.Header) error {
	raw, ok := exactlyOneEventHeader(header, "Content-Type")
	if !ok {
		return errors.New("invalid content type")
	}
	mediaType, parameters, err := mime.ParseMediaType(raw)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return errors.New("invalid content type")
	}
	for name, value := range parameters {
		if !strings.EqualFold(name, "charset") ||
			!strings.EqualFold(value, "utf-8") {
			return errors.New("invalid content type")
		}
	}
	var encodings []string
	for name, values := range header {
		if strings.EqualFold(name, "Content-Encoding") {
			encodings = append(encodings, values...)
		}
	}
	if len(encodings) > 1 ||
		len(encodings) == 1 && !strings.EqualFold(strings.TrimSpace(encodings[0]), "identity") {
		return errors.New("invalid content encoding")
	}
	return nil
}

func exactlyOneEventHeader(header http.Header, target string) (string, bool) {
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

func decodeEmptyEventReplayBody(w http.ResponseWriter, r *http.Request) error {
	if r.ContentLength > eventReplayRequestMaxBytes {
		return &http.MaxBytesError{Limit: eventReplayRequestMaxBytes}
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, eventReplayRequestMaxBytes))
	var body map[string]json.RawMessage
	if err := decoder.Decode(&body); err != nil {
		return err
	}
	if body == nil || len(body) != 0 {
		return errors.New("body is not an empty object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateEventProxyQuery(
	rawQuery string,
	contract map[string]eventQueryValidator,
) (string, error) {
	if len(rawQuery) > eventProxyQueryMaxBytes {
		return "", errors.New("query is too large")
	}
	if rawQuery == "" {
		return "", nil
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", err
	}
	for key, items := range values {
		validator, ok := contract[key]
		if !ok || len(items) != 1 {
			return "", fmt.Errorf("unsupported or repeated query parameter %q", key)
		}
		if err := validator(items[0]); err != nil {
			return "", err
		}
	}
	return values.Encode(), nil
}

func boundedEventQueryValue(
	name string,
	maximum int,
) eventQueryValidator {
	return func(value string) error {
		if !utf8.ValidString(value) {
			return fmt.Errorf("%s is not valid UTF-8", name)
		}
		if value == "" || value != strings.TrimSpace(value) || len(value) > maximum {
			return fmt.Errorf("%s is invalid", name)
		}
		return nil
	}
}

func enumEventQueryValue(name string, allowed ...string) eventQueryValidator {
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

func eventLimitQueryValue(value string) error {
	limit, err := strconv.Atoi(value)
	if err != nil ||
		strconv.Itoa(limit) != value ||
		limit < 1 ||
		limit > eventProxyMaximumLimit {
		return errors.New("limit is invalid")
	}
	return nil
}

func eventIDQueryValue(value string) error {
	if !validOperatorEventID(value) {
		return errors.New("event_id is invalid")
	}
	return nil
}

func validOperatorEventID(value string) bool {
	return validOperatorPrefixedID(value, "ev_")
}

func validOperatorDispatchID(value string) bool {
	return validOperatorPrefixedID(value, "dsp_")
}

func validOperatorPrefixedID(value, prefix string) bool {
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, char := range value[len(prefix):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func (h *Handler) proxyEventGateway(
	w http.ResponseWriter,
	r *http.Request,
	upstreamPath string,
	rawQuery string,
	body []byte,
	payload bool,
	rewriteReplayLocation bool,
) {
	setEventResponseHeaders(w)

	var cfg *config.Config
	if loaded, err := config.LoadConfig(h.configPath); err == nil {
		cfg = loaded
	}
	pidData := eventGatewayPIDData(h, cfg)
	if !validEventGatewayPIDData(pidData) {
		writeEventAPIError(w, http.StatusServiceUnavailable, "event gateway unavailable")
		return
	}
	target, err := h.eventGatewayURL(pidData, cfg, upstreamPath, rawQuery)
	if err != nil {
		writeEventAPIError(w, http.StatusServiceUnavailable, "event gateway unavailable")
		return
	}

	method := http.MethodGet
	var requestBody io.Reader
	if body != nil {
		method = http.MethodPost
		requestBody = bytes.NewReader(body)
	}
	upstreamRequest, err := http.NewRequestWithContext(r.Context(), method, target.String(), requestBody)
	if err != nil {
		writeEventAPIError(w, http.StatusBadGateway, "invalid event gateway response")
		return
	}
	upstreamRequest.Header.Set("Accept", "application/json")
	upstreamRequest.Header.Set("Authorization", "Bearer "+pidData.Token)
	if body != nil {
		upstreamRequest.Header.Set("Content-Type", "application/json")
	}

	response, err := eventGatewayDo(upstreamRequest, eventGatewayRequestTimeout)
	if err != nil {
		if rewriteReplayLocation {
			writeEventReplayUnknownOutcome(w)
			return
		}
		writeEventAPIError(w, http.StatusServiceUnavailable, "event gateway unavailable")
		return
	}
	if response == nil || response.Body == nil {
		if rewriteReplayLocation {
			writeEventReplayUnknownOutcome(w)
			return
		}
		writeEventAPIError(w, http.StatusBadGateway, "invalid event gateway response")
		return
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		writeEventAPIError(w, http.StatusServiceUnavailable, "event gateway unavailable")
		return
	}
	if response.StatusCode < 200 ||
		response.StatusCode > 599 ||
		response.StatusCode >= 300 && response.StatusCode < 400 {
		if rewriteReplayLocation {
			writeEventReplayUnknownOutcome(w)
			return
		}
		writeEventAPIError(w, http.StatusBadGateway, "invalid event gateway response")
		return
	}

	maximum := int64(eventProxyJSONMaxBytes)
	if payload {
		maximum = eventProxyPayloadMaxBytes
	}
	if response.ContentLength > maximum {
		if rewriteReplayLocation {
			writeEventReplayUnknownOutcome(w)
			return
		}
		writeEventAPIError(w, http.StatusBadGateway, "invalid event gateway response")
		return
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil || int64(len(responseBody)) > maximum {
		if rewriteReplayLocation {
			writeEventReplayUnknownOutcome(w)
			return
		}
		writeEventAPIError(w, http.StatusBadGateway, "invalid event gateway response")
		return
	}
	if !eventGatewayJSONResponse(response.Header.Get("Content-Type"), responseBody) {
		if rewriteReplayLocation {
			writeEventReplayUnknownOutcome(w)
			return
		}
		writeEventAPIError(w, http.StatusBadGateway, "invalid event gateway response")
		return
	}

	location := ""
	if rewriteReplayLocation && response.StatusCode == http.StatusCreated {
		location, err = externalEventReplayLocation(response.Header.Get("Location"))
		if err != nil {
			writeEventReplayUnknownOutcome(w)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if location != "" {
		w.Header().Set("Location", location)
	}
	if response.StatusCode == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func validEventGatewayPIDData(pidData *ppid.PidFileData) bool {
	if pidData == nil || pidData.PID <= 0 || strings.TrimSpace(pidData.Token) == "" {
		return false
	}
	return pidData.Token == strings.TrimSpace(pidData.Token) &&
		len(pidData.Token) <= 4096 &&
		validEventGatewayBearer(pidData.Token)
}

func validEventGatewayBearer(token string) bool {
	for _, character := range token {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func (h *Handler) liveEventGatewayPIDData(cfg *config.Config) *ppid.PidFileData {
	if pidData := h.sanitizeGatewayPidData(
		ppid.ReadPidFileWithCheck(globalConfigDir()),
		cfg,
	); pidData != nil {
		return cloneEventGatewayPIDData(pidData)
	}

	gateway.mu.Lock()
	cached := cloneEventGatewayPIDData(gateway.pidData)
	gateway.mu.Unlock()
	if cached == nil {
		return nil
	}
	return cloneEventGatewayPIDData(h.sanitizeGatewayPidData(cached, cfg))
}

func cloneEventGatewayPIDData(pidData *ppid.PidFileData) *ppid.PidFileData {
	if pidData == nil {
		return nil
	}
	return &ppid.PidFileData{
		PID:     pidData.PID,
		Token:   pidData.Token,
		Version: pidData.Version,
		Port:    pidData.Port,
		Host:    pidData.Host,
	}
}

func (h *Handler) eventGatewayURL(
	pidData *ppid.PidFileData,
	cfg *config.Config,
	upstreamPath string,
	rawQuery string,
) (*url.URL, error) {
	if pidData == nil ||
		(!strings.HasPrefix(upstreamPath, "/runtime/eventing/") &&
			!strings.HasPrefix(upstreamPath, "/runtime/repository-reviews/")) {
		return nil, errors.New("invalid event gateway target")
	}
	port := pidData.Port
	if port == 0 {
		port = 18790
		if cfg != nil && cfg.Gateway.Port != 0 {
			port = cfg.Gateway.Port
		}
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("invalid event gateway port")
	}
	bindHost := strings.TrimSpace(pidData.Host)
	if bindHost == "" {
		bindHost = h.effectiveGatewayBindHost(cfg)
	}
	host := gatewayProbeHost(bindHost)
	if strings.TrimSpace(host) == "" {
		return nil, errors.New("invalid event gateway host")
	}
	return &url.URL{
		Scheme:   "http",
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		Path:     upstreamPath,
		RawQuery: rawQuery,
	}, nil
}

func eventGatewayJSONResponse(contentType string, body []byte) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	return len(body) > 0 && json.Valid(body)
}

func externalEventReplayLocation(raw string) (string, error) {
	const prefix = "/runtime/eventing/events/"
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, prefix) {
		return "", errors.New("invalid replay location")
	}
	id := strings.TrimPrefix(raw, prefix)
	if !validOperatorEventID(id) || raw != prefix+id {
		return "", errors.New("invalid replay location")
	}
	return "/api/events/" + id, nil
}

func setEventResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeEventAPIError(w http.ResponseWriter, status int, message string) {
	setEventResponseHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func writeEventReplayUnknownOutcome(w http.ResponseWriter) {
	writeEventAPIError(
		w,
		http.StatusInternalServerError,
		eventReplayUnknownOutcomeMessage,
	)
}
