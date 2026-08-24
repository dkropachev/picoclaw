package api

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

const prWorkspaceProxyResponseMaxBytes = 32 << 20

var prWorkspaceGatewayPIDData = func() *ppid.PidFileData {
	// A launcher proxy must never attach to a process or repair PID metadata.
	// The local numeric-host validation below decides whether the peeked bearer
	// may safely be forwarded.
	return ppid.PeekPidFile(globalConfigDir())
}

func (h *Handler) proxyPRWorkspaceGateway(
	w http.ResponseWriter,
	r *http.Request,
	method string,
	upstreamPath string,
	rawQuery string,
	body []byte,
	timeout time.Duration,
) {
	setPRWorkspaceResponseHeaders(w)

	var cfg *config.Config
	if loaded, err := config.LoadConfig(h.configPath); err == nil {
		cfg = loaded
	}
	pidData := prWorkspaceGatewayPIDData()
	if !validAgentActivityPIDData(pidData) {
		writePRWorkspaceAPIError(w, http.StatusServiceUnavailable, "gateway_unavailable")
		return
	}
	target, err := h.eventGatewayURL(pidData, cfg, upstreamPath, rawQuery)
	if err != nil {
		writePRWorkspaceAPIError(w, http.StatusServiceUnavailable, "gateway_unavailable")
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
		writePRWorkspaceAPIError(w, http.StatusBadGateway, "invalid_gateway_response")
		return
	}
	upstreamRequest.Header.Set("Accept", "application/json")
	upstreamRequest.Header.Set("Authorization", "Bearer "+pidData.Token)
	if body != nil {
		upstreamRequest.Header.Set("Content-Type", "application/json")
	}

	response, err := eventGatewayDo(upstreamRequest, timeout)
	if err != nil {
		writePRWorkspaceAPIError(w, http.StatusServiceUnavailable, "gateway_unavailable")
		return
	}
	if response == nil || response.Body == nil {
		writePRWorkspaceAPIError(w, http.StatusBadGateway, "invalid_gateway_response")
		return
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized ||
		response.StatusCode == http.StatusForbidden {
		writePRWorkspaceAPIError(w, http.StatusServiceUnavailable, "gateway_unavailable")
		return
	}
	if response.StatusCode < 200 || response.StatusCode > 599 ||
		response.StatusCode >= 300 && response.StatusCode < 400 ||
		response.ContentLength > prWorkspaceProxyResponseMaxBytes {
		writePRWorkspaceAPIError(w, http.StatusBadGateway, "invalid_gateway_response")
		return
	}
	responseBody, err := io.ReadAll(io.LimitReader(
		response.Body,
		prWorkspaceProxyResponseMaxBytes+1,
	))
	if err != nil || len(responseBody) > prWorkspaceProxyResponseMaxBytes ||
		!eventGatewayJSONResponse(response.Header.Get("Content-Type"), responseBody) {
		writePRWorkspaceAPIError(w, http.StatusBadGateway, "invalid_gateway_response")
		return
	}

	if location, ok := externalPRWorkspaceLocation(
		exactlyOnePRWorkspaceResponseHeader(response.Header, "Location"),
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

func prWorkspaceMutationCrossSite(r *http.Request) bool {
	if r == nil {
		return true
	}
	fetchSites := prWorkspaceHeaderValues(r.Header, "Sec-Fetch-Site")
	origins := prWorkspaceHeaderValues(r.Header, "Origin")
	referers := prWorkspaceHeaderValues(r.Header, "Referer")
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
	return len(origins) == 0 && len(referers) == 0
}

func prWorkspaceHeaderValues(header http.Header, target string) []string {
	var values []string
	for name, candidates := range header {
		if strings.EqualFold(name, target) {
			values = append(values, candidates...)
		}
	}
	return values
}

func exactlyOnePRWorkspaceResponseHeader(header http.Header, target string) string {
	values := prWorkspaceHeaderValues(header, target)
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func externalPRWorkspaceLocation(raw string) (string, bool) {
	if raw == prWorkspaceRuntimePath {
		return prWorkspaceAPIPath, true
	}
	if !strings.HasPrefix(raw, prWorkspaceRuntimePath+"/") ||
		strings.ContainsAny(raw, "%?#") {
		return "", false
	}
	id := strings.TrimPrefix(raw, prWorkspaceRuntimePath+"/")
	if len(id) != len("devw_")+32 || !strings.HasPrefix(id, "devw_") ||
		strings.Contains(id, "/") {
		return "", false
	}
	for _, character := range strings.TrimPrefix(id, "devw_") {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return "", false
		}
	}
	return prWorkspaceAPIPath + "/" + id, true
}

func setPRWorkspaceResponseHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
