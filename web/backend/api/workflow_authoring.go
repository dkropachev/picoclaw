package api

import (
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sipeed/picoclaw/pkg/netbind"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	workflowAuthoringGatewayTimeout = 5 * time.Second
	workflowAuthoringAPIPath        = "/api/workflows/authoring/capabilities"
)

var (
	workflowAuthoringGatewayDo = func(
		req *http.Request,
		timeout time.Duration,
	) (*http.Response, error) {
		client := newEventGatewayHTTPClient(timeout)
		return client.Do(req)
	}
	workflowAuthoringGatewayPIDData = func(
		h *Handler,
	) *ppid.PidFileData {
		return h.readOnlyWorkflowAuthoringGatewayPIDData()
	}
)

func (h *Handler) registerWorkflowAuthoringRoutes(mux *http.ServeMux) {
	mux.HandleFunc(
		"GET "+workflowAuthoringAPIPath,
		h.handleGetWorkflowAuthoringCapabilities,
	)
}

func (h *Handler) handleGetWorkflowAuthoringCapabilities(
	w http.ResponseWriter,
	r *http.Request,
) {
	setWorkflowAuthoringAPIHeaders(w)
	if r == nil || r.URL == nil ||
		r.Method != http.MethodGet ||
		r.URL.Path != workflowAuthoringAPIPath ||
		r.URL.RawQuery != "" {
		writeWorkflowAuthoringAPIUnavailable(w)
		return
	}

	pidData := workflowAuthoringGatewayPIDData(h)
	if !validEventGatewayPIDData(pidData) {
		writeWorkflowAuthoringAPIUnavailable(w)
		return
	}
	target, ok := h.workflowAuthoringGatewayURL(pidData)
	if !ok {
		writeWorkflowAuthoringAPIUnavailable(w)
		return
	}

	upstreamRequest, err := http.NewRequestWithContext(
		r.Context(),
		http.MethodGet,
		target.String(),
		nil,
	)
	if err != nil {
		writeWorkflowAuthoringAPIUnavailable(w)
		return
	}
	upstreamRequest.Header.Set("Accept", "application/json")
	upstreamRequest.Header.Set("Authorization", "Bearer "+pidData.Token)

	response, err := workflowAuthoringGatewayDo(
		upstreamRequest,
		workflowAuthoringGatewayTimeout,
	)
	if err != nil || response == nil || response.Body == nil {
		writeWorkflowAuthoringAPIUnavailable(w)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK ||
		response.ContentLength < -1 ||
		response.ContentLength > workflows.MaxWorkflowAuthoringResponseBytes {
		writeWorkflowAuthoringAPIUnavailable(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(
		response.Body,
		workflows.MaxWorkflowAuthoringResponseBytes+1,
	))
	if err != nil ||
		int64(len(body)) > workflows.MaxWorkflowAuthoringResponseBytes ||
		!workflowAuthoringJSONResponse(
			workflowAuthoringResponseContentType(response.Header),
			body,
		) {
		writeWorkflowAuthoringAPIUnavailable(w)
		return
	}
	catalog, err := workflows.DecodeWorkflowAuthoringCapabilities(body)
	if err != nil {
		writeWorkflowAuthoringAPIUnavailable(w)
		return
	}
	body, ok = workflows.MarshalWorkflowAuthoringCapabilities(catalog)
	if !ok {
		writeWorkflowAuthoringAPIUnavailable(w)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *Handler) workflowAuthoringGatewayURL(
	pidData *ppid.PidFileData,
) (*url.URL, bool) {
	if pidData == nil {
		return nil, false
	}
	port := pidData.Port
	if port < 1 || port > 65535 {
		return nil, false
	}
	bindHost := strings.TrimSpace(pidData.Host)
	if bindHost == "" ||
		bindHost != pidData.Host ||
		len(bindHost) > 255 ||
		!validWorkflowAuthoringBindHost(bindHost) {
		return nil, false
	}
	plan, err := netbind.BuildPlan(bindHost, netbind.DefaultLoopback)
	if err != nil {
		return nil, false
	}
	host := strings.TrimSpace(plan.ProbeHost)
	if strings.TrimSpace(host) == "" {
		return nil, false
	}
	return &url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   workflows.RuntimeAuthoringCapabilitiesPath,
	}, true
}

func validWorkflowAuthoringBindHost(value string) bool {
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			continue
		}
		switch character {
		case '.', '-', '_', ':', '[', ']', ',', '*', '%':
		default:
			return false
		}
	}
	return true
}

func (h *Handler) readOnlyWorkflowAuthoringGatewayPIDData() *ppid.PidFileData {
	pidData := cloneEventGatewayPIDData(ppid.PeekPidFile(globalConfigDir()))
	if h.validReadOnlyWorkflowAuthoringGatewayPIDData(pidData) {
		return pidData
	}

	gateway.mu.Lock()
	cached := cloneEventGatewayPIDData(gateway.pidData)
	gateway.mu.Unlock()
	if !h.validReadOnlyWorkflowAuthoringGatewayPIDData(cached) {
		return nil
	}
	return cached
}

func (h *Handler) validReadOnlyWorkflowAuthoringGatewayPIDData(
	pidData *ppid.PidFileData,
) bool {
	if !validEventGatewayPIDData(pidData) {
		return false
	}
	if _, validTarget := h.workflowAuthoringGatewayURL(pidData); !validTarget {
		return false
	}
	ok, _, _ := h.validateGatewayPidData(pidData, nil)
	return ok
}

func workflowAuthoringResponseContentType(header http.Header) string {
	values := make([]string, 0, 1)
	for name, candidates := range header {
		if strings.EqualFold(name, "Content-Type") {
			values = append(values, candidates...)
		}
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return ""
	}
	return values[0]
}

func workflowAuthoringJSONResponse(contentType string, body []byte) bool {
	mediaType, parameters, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	for name, value := range parameters {
		if !strings.EqualFold(name, "charset") ||
			!strings.EqualFold(strings.TrimSpace(value), "utf-8") {
			return false
		}
	}
	return len(body) > 0 && json.Valid(body)
}

func setWorkflowAuthoringAPIHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeWorkflowAuthoringAPIUnavailable(w http.ResponseWriter) {
	setWorkflowAuthoringAPIHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "workflow_authoring_capabilities_unavailable",
	})
}
