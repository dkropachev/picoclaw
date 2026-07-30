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

	agentloop "github.com/sipeed/picoclaw/pkg/agent"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
	"github.com/sipeed/picoclaw/pkg/routing"
)

const (
	agentActivityGatewayTimeout   = 3 * time.Second
	agentActivityResponseMaxBytes = 128 << 10
	agentActivityAPIPathPrefix    = "/api/agents/"
)

var (
	agentActivityGatewayClient = newAgentActivityGatewayHTTPClient(
		agentActivityGatewayTimeout,
	)
	agentActivityGatewayDo = func(
		request *http.Request,
		_ time.Duration,
	) (*http.Response, error) {
		return agentActivityGatewayClient.Do(request)
	}
	agentActivityGatewayPIDData = func() *ppid.PidFileData {
		return ppid.PeekPidFile(globalConfigDir())
	}
	agentActivityInterfaceAddrs = net.InterfaceAddrs
)

func newAgentActivityGatewayHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// handleGetAgentActivity proxies one bounded activity page from the protected
// gateway runtime. Route registration lives with the other agent routes.
func (h *Handler) handleGetAgentActivity(w http.ResponseWriter, r *http.Request) {
	setAgentActivityAPIHeaders(w)
	if r == nil || r.URL == nil {
		writeAgentActivityAPIError(w, http.StatusNotFound, "agent_not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAgentActivityAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	agentID, ok := launcherAgentActivityID(r)
	if !ok {
		writeAgentActivityAPIError(w, http.StatusNotFound, "agent_not_found")
		return
	}
	cursor, limit, err := agentloop.ParseAgentActivityQuery(r.URL.RawQuery)
	if err != nil {
		writeAgentActivityAPIError(
			w,
			http.StatusBadRequest,
			"invalid_agent_activity_query",
		)
		return
	}

	pidData := agentActivityGatewayPIDData()
	target, ok := agentActivityGatewayURL(pidData, agentID, cursor, limit)
	if !ok {
		writeAgentActivityAPIError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	upstreamRequest, err := http.NewRequestWithContext(
		r.Context(),
		http.MethodGet,
		target.String(),
		nil,
	)
	if err != nil {
		writeAgentActivityAPIError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	// Build a fresh request and set only the two gateway-facing headers. In
	// particular, browser Authorization, Cookie, forwarding, and tracing
	// headers never cross this boundary.
	upstreamRequest.Header.Set("Accept", "application/json")
	upstreamRequest.Header.Set("Authorization", "Bearer "+pidData.Token)
	// An explicitly present empty User-Agent suppresses net/http's automatic
	// Go-http-client header without adding a wire-visible application header.
	upstreamRequest.Header["User-Agent"] = nil

	response, err := agentActivityGatewayDo(
		upstreamRequest,
		agentActivityGatewayTimeout,
	)
	if err != nil || response == nil || response.Body == nil {
		writeAgentActivityAPIError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusBadRequest:
		writeAgentActivityAPIError(
			w,
			http.StatusBadRequest,
			"invalid_agent_activity_query",
		)
		return
	case http.StatusNotFound:
		writeAgentActivityAPIError(w, http.StatusNotFound, "agent_not_found")
		return
	case http.StatusOK:
	default:
		writeAgentActivityAPIError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	if response.ContentLength < -1 ||
		response.ContentLength > agentActivityResponseMaxBytes ||
		!agentActivityJSONContentType(response.Header) {
		writeAgentActivityAPIError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(
		response.Body,
		agentActivityResponseMaxBytes+1,
	))
	if err != nil || len(raw) == 0 ||
		int64(len(raw)) > agentActivityResponseMaxBytes {
		writeAgentActivityAPIError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	page, err := agentloop.DecodeAgentActivityPage(raw)
	if err != nil || page.AgentID != agentID || len(page.Events) > limit {
		writeAgentActivityAPIError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	encoded, err := agentloop.MarshalAgentActivityPage(page)
	if err != nil || len(encoded) > agentActivityResponseMaxBytes {
		writeAgentActivityAPIError(
			w,
			http.StatusServiceUnavailable,
			"agent_activity_unavailable",
		)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func launcherAgentActivityID(r *http.Request) (string, bool) {
	if r == nil || r.URL == nil || r.URL.RawPath != "" {
		return "", false
	}
	id := r.PathValue("id")
	if id == "" {
		path := r.URL.Path
		if !strings.HasPrefix(path, agentActivityAPIPathPrefix) {
			return "", false
		}
		relative := strings.TrimPrefix(path, agentActivityAPIPathPrefix)
		segments := strings.Split(relative, "/")
		if len(segments) != 2 || segments[1] != "activity" {
			return "", false
		}
		id = segments[0]
	}
	if !routing.IsCanonicalAgentID(id) ||
		r.URL.Path != agentActivityAPIPathPrefix+id+"/activity" {
		return "", false
	}
	return id, true
}

func agentActivityGatewayURL(
	pidData *ppid.PidFileData,
	agentID string,
	cursor string,
	limit int,
) (*url.URL, bool) {
	if !validAgentActivityPIDData(pidData) ||
		!routing.IsCanonicalAgentID(agentID) ||
		limit < 1 ||
		limit > agentloop.MaxAgentActivityLimit {
		return nil, false
	}
	host, ok := agentActivityGatewayHost(pidData.Host)
	if !ok {
		return nil, false
	}
	return &url.URL{
		Scheme: "http",
		Host: net.JoinHostPort(
			host,
			strconv.Itoa(pidData.Port),
		),
		Path: agentloop.RuntimeAgentActivityRoutePrefix +
			agentID +
			"/activity",
		RawQuery: agentloop.AgentActivityUpstreamQuery(cursor, limit),
	}, true
}

func validAgentActivityPIDData(pidData *ppid.PidFileData) bool {
	if pidData == nil ||
		pidData.PID <= 0 ||
		pidData.Port < 1 ||
		pidData.Port > 65535 ||
		pidData.Host == "" ||
		pidData.Host != strings.TrimSpace(pidData.Host) ||
		len(pidData.Host) > 255 ||
		pidData.Token == "" ||
		pidData.Token != strings.TrimSpace(pidData.Token) ||
		len(pidData.Token) > 4096 ||
		!validEventGatewayBearer(pidData.Token) {
		return false
	}
	_, ok := agentActivityGatewayHost(pidData.Host)
	return ok
}

func agentActivityGatewayHost(host string) (string, bool) {
	return host, localAgentActivityHost(host)
}

func localAgentActivityHost(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addresses, err := agentActivityInterfaceAddrs()
	if err != nil {
		return false
	}
	for _, address := range addresses {
		var localIP net.IP
		switch value := address.(type) {
		case *net.IPNet:
			localIP = value.IP
		case *net.IPAddr:
			localIP = value.IP
		default:
			rawHost, _, splitErr := net.ParseCIDR(address.String())
			if splitErr == nil {
				localIP = rawHost
			}
		}
		if localIP != nil && localIP.Equal(ip) {
			return true
		}
	}
	return false
}

func agentActivityJSONContentType(header http.Header) bool {
	values := make([]string, 0, 1)
	for name, candidates := range header {
		if strings.EqualFold(name, "Content-Type") {
			values = append(values, candidates...)
		}
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	for name, value := range parameters {
		if !strings.EqualFold(name, "charset") ||
			!strings.EqualFold(strings.TrimSpace(value), "utf-8") {
			return false
		}
	}
	return true
}

func setAgentActivityAPIHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeAgentActivityAPIError(
	w http.ResponseWriter,
	status int,
	message string,
) {
	setAgentActivityAPIHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", "1")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
