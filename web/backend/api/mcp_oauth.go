package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	picoauth "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
)

const (
	mcpOAuthFlowTTL        = 10 * time.Minute
	mcpOAuthStartTimeout   = 30 * time.Second
	mcpOAuthCallbackWait   = 30 * time.Second
	mcpOAuthTerminalFlowGC = 30 * time.Minute
)

var (
	mcpOAuthLogin = picomcp.LoginWithOAuth
	mcpOAuthNow   = time.Now
)

type mcpOAuthCallbackResult struct {
	Code  string
	State string
	Err   error
}

type mcpOAuthFlow struct {
	ID           string
	ServerName   string
	ServerURL    string
	Transport    string
	StartingAuth *config.MCPServerAuthConfig
	Status       string
	AuthURL      string
	OAuthState   string
	Error        string
	ToolCount    int
	Tools        []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ExpiresAt    time.Time
	callback     chan mcpOAuthCallbackResult
	ready        chan struct{}
	done         chan struct{}
	cancel       context.CancelFunc
	readyOnce    sync.Once
	doneOnce     sync.Once
}

type mcpOAuthFlowResponse struct {
	FlowID     string   `json:"flow_id"`
	ServerName string   `json:"server_name"`
	Status     string   `json:"status"`
	AuthURL    string   `json:"auth_url,omitempty"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
	Error      string   `json:"error,omitempty"`
	ToolCount  int      `json:"tool_count,omitempty"`
	Tools      []string `json:"tools,omitempty"`
}

func (h *Handler) registerMCPOAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc(
		"POST /api/mcp/servers/{name}/oauth",
		h.requireMCPMutationOrigin(h.handleStartMCPOAuth),
	)
	mux.HandleFunc("GET /api/mcp/oauth/flows/{id}", h.handleGetMCPOAuthFlow)
	mux.HandleFunc("GET /mcp/oauth/callback", h.handleMCPOAuthCallback)
}

func (h *Handler) handleStartMCPOAuth(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load config: %v", err), http.StatusInternalServerError)
		return
	}

	name := findMCPServerName(cfg.Tools.MCP.Servers, r.PathValue("name"))
	if name == "" {
		http.Error(w, fmt.Sprintf("MCP server %q not found", r.PathValue("name")), http.StatusNotFound)
		return
	}
	server := cfg.Tools.MCP.Servers[name]
	transport := config.EffectiveMCPTransportType(server)
	if transport != "http" && transport != "sse" {
		http.Error(w, "OAuth login is only supported for remote MCP servers", http.StatusBadRequest)
		return
	}

	now := mcpOAuthNow()
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(mcpOAuthFlowTTL))
	flow := &mcpOAuthFlow{
		ID:           "mcp_" + newOAuthFlowID(),
		ServerName:   name,
		ServerURL:    strings.TrimSpace(server.URL),
		Transport:    transport,
		StartingAuth: cloneMCPAuthConfig(server.Auth),
		Status:       oauthFlowPending,
		CreatedAt:    now,
		UpdatedAt:    now,
		ExpiresAt:    now.Add(mcpOAuthFlowTTL),
		callback:     make(chan mcpOAuthCallbackResult, 1),
		ready:        make(chan struct{}),
		done:         make(chan struct{}),
		cancel:       cancel,
	}
	h.storeMCPOAuthFlow(flow)

	redirectURL := h.buildMCPOAuthRedirectURI(r)
	go h.runMCPOAuthFlow(ctx, flow, server, redirectURL)

	timer := time.NewTimer(mcpOAuthStartTimeout)
	defer timer.Stop()
	select {
	case <-flow.ready:
	case <-flow.done:
	case <-r.Context().Done():
		cancel()
		h.finishMCPOAuthFlow(flow.ID, oauthFlowError, r.Context().Err().Error(), nil)
		return
	case <-timer.C:
		cancel()
		h.finishMCPOAuthFlow(
			flow.ID,
			oauthFlowError,
			"timed out while preparing the MCP authorization page",
			nil,
		)
		http.Error(w, "Timed out while preparing MCP OAuth login", http.StatusGatewayTimeout)
		return
	}

	response, ok := h.getMCPOAuthFlow(flow.ID)
	if !ok {
		http.Error(w, "MCP OAuth flow was not found", http.StatusInternalServerError)
		return
	}
	if response.Status != oauthFlowPending || strings.TrimSpace(response.AuthURL) == "" {
		message := strings.TrimSpace(response.Error)
		if message == "" {
			message = "MCP server did not provide an OAuth authorization URL"
		}
		http.Error(w, message, http.StatusBadGateway)
		return
	}

	writeJSON(w, map[string]any{
		"status":      "ok",
		"flow_id":     response.FlowID,
		"server_name": response.ServerName,
		"auth_url":    response.AuthURL,
		"expires_at":  response.ExpiresAt,
	})
}

func (h *Handler) handleGetMCPOAuthFlow(w http.ResponseWriter, r *http.Request) {
	flowID := strings.TrimSpace(r.PathValue("id"))
	if flowID == "" {
		http.Error(w, "missing flow id", http.StatusBadRequest)
		return
	}
	flow, ok := h.getMCPOAuthFlow(flowID)
	if !ok {
		http.Error(w, "flow not found", http.StatusNotFound)
		return
	}
	writeJSON(w, flow)
}

func (h *Handler) handleMCPOAuthCallback(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		renderMCPOAuthCallbackPage(w, "", oauthFlowError, "Missing state", "missing_state")
		return
	}

	flow, ok := h.consumeMCPOAuthFlowByState(state)
	if !ok {
		renderMCPOAuthCallbackPage(w, "", oauthFlowError, "OAuth flow not found", "flow_not_found")
		return
	}
	if flow.Status != oauthFlowPending {
		renderMCPOAuthCallbackPage(
			w,
			flow.FlowID,
			flow.Status,
			"OAuth flow already completed",
			flow.Error,
		)
		return
	}

	callback := mcpOAuthCallbackResult{State: state}
	if providerError := strings.TrimSpace(r.URL.Query().Get("error")); providerError != "" {
		if description := strings.TrimSpace(r.URL.Query().Get("error_description")); description != "" {
			providerError += ": " + description
		}
		callback.Err = errors.New(providerError)
	} else {
		callback.Code = strings.TrimSpace(r.URL.Query().Get("code"))
		if callback.Code == "" {
			callback.Err = errors.New("missing authorization code")
		}
	}

	storedFlow := h.mcpOAuthFlowPointer(flow.FlowID)
	if storedFlow == nil {
		renderMCPOAuthCallbackPage(
			w,
			flow.FlowID,
			oauthFlowError,
			"OAuth flow not found",
			"flow_not_found",
		)
		return
	}
	select {
	case storedFlow.callback <- callback:
	default:
		renderMCPOAuthCallbackPage(
			w,
			flow.FlowID,
			oauthFlowError,
			"OAuth callback already received",
			"callback_already_received",
		)
		return
	}

	timer := time.NewTimer(mcpOAuthCallbackWait)
	defer timer.Stop()
	select {
	case <-storedFlow.done:
	case <-timer.C:
	case <-r.Context().Done():
		return
	}

	completed, found := h.getMCPOAuthFlow(flow.FlowID)
	if !found {
		renderMCPOAuthCallbackPage(
			w,
			flow.FlowID,
			oauthFlowError,
			"OAuth flow not found",
			"flow_not_found",
		)
		return
	}
	switch completed.Status {
	case oauthFlowSuccess:
		renderMCPOAuthCallbackPage(
			w,
			completed.FlowID,
			completed.Status,
			"Authentication successful",
			"",
		)
	case oauthFlowPending:
		renderMCPOAuthCallbackPage(
			w,
			completed.FlowID,
			completed.Status,
			"Finishing authentication",
			"",
		)
	default:
		message := completed.Error
		if message == "" {
			message = "authorization failed"
		}
		renderMCPOAuthCallbackPage(
			w,
			completed.FlowID,
			completed.Status,
			"Authentication failed",
			message,
		)
	}
}

func (h *Handler) runMCPOAuthFlow(
	ctx context.Context,
	flow *mcpOAuthFlow,
	server config.MCPServerConfig,
	redirectURL string,
) {
	result, err := mcpOAuthLogin(
		ctx,
		flow.ServerName,
		server,
		picomcp.OAuthLoginOptions{
			RedirectURL: redirectURL,
			AuthorizationCodeFetcher: func(
				fetchContext context.Context,
				args *mcpauth.AuthorizationArgs,
			) (*mcpauth.AuthorizationResult, error) {
				if args == nil || strings.TrimSpace(args.URL) == "" {
					return nil, fmt.Errorf("MCP server returned an empty OAuth authorization URL")
				}
				parsed, parseErr := url.Parse(args.URL)
				if parseErr != nil {
					return nil, fmt.Errorf("invalid MCP OAuth authorization URL: %w", parseErr)
				}
				if !picomcp.IsHTTPSOrLoopbackHTTP(parsed.String()) {
					return nil, fmt.Errorf(
						"MCP OAuth authorization URL must use HTTPS, except on loopback",
					)
				}
				state := strings.TrimSpace(parsed.Query().Get("state"))
				if state == "" {
					return nil, fmt.Errorf("MCP OAuth authorization URL is missing state")
				}
				if !h.publishMCPOAuthAuthorization(flow.ID, args.URL, state) {
					return nil, fmt.Errorf("MCP OAuth flow is no longer active")
				}

				select {
				case callback := <-flow.callback:
					if callback.Err != nil {
						return nil, callback.Err
					}
					return &mcpauth.AuthorizationResult{
						Code:  callback.Code,
						State: callback.State,
					}, nil
				case <-fetchContext.Done():
					return nil, fetchContext.Err()
				}
			},
			HTTPClient: newMCPOAuthHTTPClient(server.URL),
		},
	)
	if err != nil {
		status := oauthFlowError
		if errors.Is(err, context.DeadlineExceeded) ||
			(errors.Is(ctx.Err(), context.DeadlineExceeded) && !flow.ExpiresAt.IsZero() &&
				!mcpOAuthNow().Before(flow.ExpiresAt)) {
			status = oauthFlowExpired
		}
		h.finishMCPOAuthFlow(flow.ID, status, err.Error(), nil)
		return
	}
	if result == nil || result.Token == nil || strings.TrimSpace(result.Token.AccessToken) == "" {
		h.finishMCPOAuthFlow(
			flow.ID,
			oauthFlowError,
			"MCP OAuth completed without an access token",
			nil,
		)
		return
	}
	if !h.mcpOAuthFlowPending(flow.ID) {
		return
	}
	if err := h.persistMCPOAuthResult(flow, result); err != nil {
		h.finishMCPOAuthFlow(flow.ID, oauthFlowError, err.Error(), nil)
		return
	}
	h.finishMCPOAuthFlow(flow.ID, oauthFlowSuccess, "", result)
}

func (h *Handler) persistMCPOAuthResult(
	flow *mcpOAuthFlow,
	result *picomcp.OAuthLoginResult,
) error {
	h.mcpMu.Lock()
	defer h.mcpMu.Unlock()
	if flow.ID != "" {
		h.mcpOAuthMu.Lock()
		defer h.mcpOAuthMu.Unlock()
		current := h.mcpOAuthFlows[flow.ID]
		if h.mcpOAuthLatestByServer[flow.ServerName] != flow.ID ||
			current == nil ||
			current.Status != oauthFlowPending {
			return fmt.Errorf("MCP OAuth login was superseded; use the newer login window")
		}
	}

	cfg, err := config.LoadConfigForUpdate(h.configPath)
	if err != nil {
		return fmt.Errorf("load config after OAuth login: %w", err)
	}
	name := findMCPServerName(cfg.Tools.MCP.Servers, flow.ServerName)
	if name == "" {
		return fmt.Errorf("MCP server %q was removed or renamed during login", flow.ServerName)
	}
	server := cfg.Tools.MCP.Servers[name]
	if config.EffectiveMCPTransportType(server) != flow.Transport ||
		strings.TrimSpace(server.URL) != flow.ServerURL {
		return fmt.Errorf("MCP server %q changed during login; start login again", name)
	}
	if !sameMCPAuthConfig(server.Auth, flow.StartingAuth) {
		return fmt.Errorf("MCP server %q authentication changed during login; start login again", name)
	}

	previousCredentialID := ""
	if server.Auth != nil {
		previousCredentialID, _ = picomcp.CredentialID(name, server.Auth)
	}
	credentialID, err := credentialIDForMCPMutation(name, server.Auth, cfg.Tools.MCP.Servers)
	if err != nil {
		return fmt.Errorf("prepare MCP credential: %w", err)
	}
	oldCredential, err := picoauth.GetCredential(credentialID)
	if err != nil {
		return fmt.Errorf("load existing MCP credential: %w", err)
	}

	credential := &picoauth.AuthCredential{
		AccessToken:       result.Token.AccessToken,
		RefreshToken:      result.Token.RefreshToken,
		TokenType:         result.Token.TokenType,
		OAuthTokenURL:     result.RefreshMetadata.TokenURL,
		OAuthClientID:     result.RefreshMetadata.ClientID,
		OAuthClientSecret: result.RefreshMetadata.ClientSecret,
		OAuthAuthStyle:    result.RefreshMetadata.AuthStyle,
		ExpiresAt:         result.Token.Expiry,
		Provider:          "mcp",
		AuthMethod:        "oauth",
	}
	if err := picoauth.SetCredential(credentialID, credential); err != nil {
		return fmt.Errorf("save MCP OAuth credential: %w", err)
	}

	server.Auth = &config.MCPServerAuthConfig{
		Type:         "oauth",
		CredentialID: credentialID,
		Revision:     nextMCPAuthRevision(server.Auth),
	}
	cfg.Tools.MCP.Servers[name] = server
	if err := config.SaveConfig(h.configPath, cfg); err != nil {
		if oldCredential == nil {
			_ = picoauth.DeleteCredential(credentialID)
		} else {
			_ = picoauth.SetCredential(credentialID, oldCredential)
		}
		return fmt.Errorf("save MCP OAuth configuration: %w", err)
	}

	if previousCredentialID != "" && previousCredentialID != credentialID &&
		!mcpCredentialReferenced(cfg.Tools.MCP.Servers, previousCredentialID) {
		_ = picoauth.DeleteCredential(previousCredentialID)
	}
	if flow.ID != "" {
		current := h.mcpOAuthFlows[flow.ID]
		current.Status = oauthFlowSuccess
		current.Error = ""
		current.ToolCount = result.ToolCount
		current.Tools = append([]string(nil), result.Tools...)
		current.UpdatedAt = mcpOAuthNow()
		if current.OAuthState != "" {
			delete(h.mcpOAuthState, current.OAuthState)
		}
		current.readyOnce.Do(func() { close(current.ready) })
		current.doneOnce.Do(func() { close(current.done) })
	}
	return nil
}

func sameMCPAuthConfig(left, right *config.MCPServerAuthConfig) bool {
	if left == nil || right == nil {
		return left == right
	}
	return strings.EqualFold(strings.TrimSpace(left.Type), strings.TrimSpace(right.Type)) &&
		strings.TrimSpace(left.CredentialID) == strings.TrimSpace(right.CredentialID) &&
		left.Revision == right.Revision
}

func (h *Handler) storeMCPOAuthFlow(flow *mcpOAuthFlow) {
	now := mcpOAuthNow()
	h.mcpOAuthMu.Lock()
	defer h.mcpOAuthMu.Unlock()
	h.gcMCPOAuthFlowsLocked(now)
	if previousID := h.mcpOAuthLatestByServer[flow.ServerName]; previousID != "" {
		if previous := h.mcpOAuthFlows[previousID]; previous != nil &&
			previous.Status == oauthFlowPending {
			previous.Status = oauthFlowError
			previous.Error = "superseded by a newer login"
			previous.UpdatedAt = now
			if previous.OAuthState != "" {
				delete(h.mcpOAuthState, previous.OAuthState)
			}
			if previous.cancel != nil {
				previous.cancel()
			}
			previous.readyOnce.Do(func() { close(previous.ready) })
			previous.doneOnce.Do(func() { close(previous.done) })
		}
	}
	h.mcpOAuthFlows[flow.ID] = flow
	h.mcpOAuthLatestByServer[flow.ServerName] = flow.ID
}

func (h *Handler) publishMCPOAuthAuthorization(flowID, authURL, state string) bool {
	now := mcpOAuthNow()
	h.mcpOAuthMu.Lock()
	defer h.mcpOAuthMu.Unlock()

	h.gcMCPOAuthFlowsLocked(now)
	flow, ok := h.mcpOAuthFlows[flowID]
	if !ok || flow.Status != oauthFlowPending || !now.Before(flow.ExpiresAt) {
		return false
	}
	if _, exists := h.mcpOAuthState[state]; exists {
		flow.Status = oauthFlowError
		flow.Error = "OAuth state collision"
		flow.UpdatedAt = now
		flow.readyOnce.Do(func() { close(flow.ready) })
		return false
	}
	flow.AuthURL = authURL
	flow.OAuthState = state
	flow.UpdatedAt = now
	h.mcpOAuthState[state] = flow.ID
	flow.readyOnce.Do(func() { close(flow.ready) })
	return true
}

func (h *Handler) getMCPOAuthFlow(flowID string) (mcpOAuthFlowResponse, bool) {
	now := mcpOAuthNow()
	h.mcpOAuthMu.Lock()
	defer h.mcpOAuthMu.Unlock()

	h.gcMCPOAuthFlowsLocked(now)
	flow, ok := h.mcpOAuthFlows[flowID]
	if !ok {
		return mcpOAuthFlowResponse{}, false
	}
	return mcpOAuthFlowToResponse(flow), true
}

func (h *Handler) consumeMCPOAuthFlowByState(state string) (mcpOAuthFlowResponse, bool) {
	now := mcpOAuthNow()
	h.mcpOAuthMu.Lock()
	defer h.mcpOAuthMu.Unlock()

	h.gcMCPOAuthFlowsLocked(now)
	flowID, ok := h.mcpOAuthState[state]
	if !ok {
		return mcpOAuthFlowResponse{}, false
	}
	delete(h.mcpOAuthState, state)
	flow, ok := h.mcpOAuthFlows[flowID]
	if !ok {
		return mcpOAuthFlowResponse{}, false
	}
	return mcpOAuthFlowToResponse(flow), true
}

func (h *Handler) mcpOAuthFlowPointer(flowID string) *mcpOAuthFlow {
	h.mcpOAuthMu.Lock()
	defer h.mcpOAuthMu.Unlock()
	return h.mcpOAuthFlows[flowID]
}

func (h *Handler) mcpOAuthFlowPending(flowID string) bool {
	now := mcpOAuthNow()
	h.mcpOAuthMu.Lock()
	defer h.mcpOAuthMu.Unlock()
	h.gcMCPOAuthFlowsLocked(now)
	flow, ok := h.mcpOAuthFlows[flowID]
	return ok && flow.Status == oauthFlowPending
}

func (h *Handler) finishMCPOAuthFlow(
	flowID, status, errorMessage string,
	result *picomcp.OAuthLoginResult,
) {
	now := mcpOAuthNow()
	h.mcpOAuthMu.Lock()
	defer h.mcpOAuthMu.Unlock()

	flow, ok := h.mcpOAuthFlows[flowID]
	if !ok {
		return
	}
	if flow.Status == oauthFlowPending {
		flow.Status = status
		flow.Error = strings.TrimSpace(errorMessage)
		flow.UpdatedAt = now
		if result != nil {
			flow.ToolCount = result.ToolCount
			flow.Tools = append([]string(nil), result.Tools...)
		}
		if flow.OAuthState != "" {
			delete(h.mcpOAuthState, flow.OAuthState)
		}
	}
	flow.readyOnce.Do(func() { close(flow.ready) })
	flow.doneOnce.Do(func() { close(flow.done) })
}

func (h *Handler) gcMCPOAuthFlowsLocked(now time.Time) {
	for id, flow := range h.mcpOAuthFlows {
		if flow.Status == oauthFlowPending && !flow.ExpiresAt.IsZero() &&
			!now.Before(flow.ExpiresAt) {
			flow.Status = oauthFlowExpired
			flow.Error = "flow expired"
			flow.UpdatedAt = now
			if flow.OAuthState != "" {
				delete(h.mcpOAuthState, flow.OAuthState)
			}
			if flow.cancel != nil {
				flow.cancel()
			}
			flow.readyOnce.Do(func() { close(flow.ready) })
		}
		if flow.Status != oauthFlowPending &&
			now.Sub(flow.UpdatedAt) > mcpOAuthTerminalFlowGC {
			if flow.OAuthState != "" {
				delete(h.mcpOAuthState, flow.OAuthState)
			}
			delete(h.mcpOAuthFlows, id)
			if h.mcpOAuthLatestByServer[flow.ServerName] == id {
				delete(h.mcpOAuthLatestByServer, flow.ServerName)
			}
		}
	}
}

func (h *Handler) cancelMCPOAuthFlows() {
	h.mcpOAuthMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(h.mcpOAuthFlows))
	for _, flow := range h.mcpOAuthFlows {
		if flow.Status == oauthFlowPending && flow.cancel != nil {
			cancels = append(cancels, flow.cancel)
		}
	}
	h.mcpOAuthMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func mcpOAuthFlowToResponse(flow *mcpOAuthFlow) mcpOAuthFlowResponse {
	response := mcpOAuthFlowResponse{
		FlowID:     flow.ID,
		ServerName: flow.ServerName,
		Status:     flow.Status,
		AuthURL:    flow.AuthURL,
		Error:      flow.Error,
		ToolCount:  flow.ToolCount,
		Tools:      append([]string(nil), flow.Tools...),
	}
	if !flow.ExpiresAt.IsZero() {
		response.ExpiresAt = flow.ExpiresAt.Format(time.RFC3339)
	}
	return response
}

func (h *Handler) buildMCPOAuthRedirectURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := mcpRequestHost(h, r)
	if h.trustMCPOAuthForwardedHeaders(r) {
		candidate := forwardedProtoFirst(r)
		if candidate == "http" || candidate == "https" {
			scheme = candidate
		}
	}
	return (&url.URL{
		Scheme: scheme,
		Host:   host,
		Path:   "/mcp/oauth/callback",
	}).String()
}

func (h *Handler) trustMCPOAuthForwardedHeaders(r *http.Request) bool {
	peerHost := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(peerHost); err == nil {
		peerHost = host
	}
	peerIP := net.ParseIP(strings.Trim(peerHost, "[]"))
	if peerIP == nil {
		return false
	}
	if peerIP.IsLoopback() {
		return true
	}
	for _, rawCIDR := range h.serverTrustedProxyCIDRs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(rawCIDR))
		if err == nil && network.Contains(peerIP) {
			return true
		}
	}
	return false
}

type mcpOAuthNetworkGuardTransport struct {
	base                   http.RoundTripper
	configuredHost         string
	allowConfiguredPrivate bool

	policyMu sync.Mutex
	pinned   map[string][]net.IP
	lookupIP func(context.Context, string) ([]net.IPAddr, error)
	dial     func(context.Context, string, string) (net.Conn, error)
}

func (t *mcpOAuthNetworkGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.validateTarget(req.Context(), req.URL); err != nil {
		return nil, err
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func (t *mcpOAuthNetworkGuardTransport) validateTarget(ctx context.Context, target *url.URL) error {
	if target == nil || strings.TrimSpace(target.Hostname()) == "" {
		return fmt.Errorf("MCP OAuth request has an invalid target")
	}
	if !picomcp.IsHTTPSOrLoopbackHTTP(target.String()) {
		return fmt.Errorf("MCP OAuth endpoint %q must use HTTPS", target.Hostname())
	}
	_, err := t.resolveAndPin(ctx, target.Hostname())
	return err
}

func (t *mcpOAuthNetworkGuardTransport) resolveAndPin(
	ctx context.Context,
	rawHostname string,
) ([]net.IP, error) {
	hostname := strings.ToLower(strings.TrimSpace(rawHostname))
	if hostname == "" {
		return nil, fmt.Errorf("MCP OAuth request has an invalid target")
	}
	t.policyMu.Lock()
	if pinned := cloneMCPIPs(t.pinned[hostname]); len(pinned) > 0 {
		t.policyMu.Unlock()
		return pinned, nil
	}
	t.policyMu.Unlock()

	var configuredIPs []net.IP
	if t.configuredHost != "" && !strings.EqualFold(hostname, t.configuredHost) {
		var err error
		configuredIPs, err = t.resolveAndPin(ctx, t.configuredHost)
		if err != nil {
			return nil, fmt.Errorf("resolve configured MCP endpoint: %w", err)
		}
	}

	lookup := t.lookupIP
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	addresses, err := lookup(ctx, hostname)
	if err != nil {
		return nil, fmt.Errorf("resolve MCP OAuth endpoint %q: %w", hostname, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("MCP OAuth endpoint %q did not resolve", hostname)
	}
	approved := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ip := address.IP
		switch {
		case ip.IsUnspecified(), ip.IsMulticast(), ip.IsLinkLocalMulticast(), ip.IsLinkLocalUnicast():
			return nil, fmt.Errorf("MCP OAuth endpoint %q resolves to a blocked network address", hostname)
		case picomcp.IsPrivateOrSpecialIP(ip) &&
			!(strings.EqualFold(hostname, t.configuredHost) && t.allowConfiguredPrivate) &&
			!(picomcp.IsExplicitLocalHostname(hostname) && mcpIPInList(ip, configuredIPs)):
			return nil, fmt.Errorf("MCP OAuth endpoint %q resolves to a private network", hostname)
		}
		approved = append(approved, append(net.IP(nil), ip...))
	}
	t.policyMu.Lock()
	if t.pinned == nil {
		t.pinned = make(map[string][]net.IP)
	}
	if existing := t.pinned[hostname]; len(existing) > 0 {
		approved = cloneMCPIPs(existing)
	} else {
		t.pinned[hostname] = cloneMCPIPs(approved)
	}
	t.policyMu.Unlock()
	return approved, nil
}

func (t *mcpOAuthNetworkGuardTransport) dialContext(
	ctx context.Context,
	network, address string,
) (net.Conn, error) {
	hostname, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse MCP OAuth dial target %q: %w", address, err)
	}
	approved, err := t.resolveAndPin(ctx, hostname)
	if err != nil {
		return nil, err
	}
	dial := t.dial
	if dial == nil {
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		dial = dialer.DialContext
	}
	var lastErr error
	for _, ip := range approved {
		connection, dialErr := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("MCP OAuth endpoint %q has no approved addresses", hostname)
	}
	return nil, lastErr
}

func cloneMCPIPs(values []net.IP) []net.IP {
	cloned := make([]net.IP, 0, len(values))
	for _, value := range values {
		cloned = append(cloned, append(net.IP(nil), value...))
	}
	return cloned
}

func mcpIPInList(candidate net.IP, values []net.IP) bool {
	for _, value := range values {
		if candidate.Equal(value) {
			return true
		}
	}
	return false
}

func mcpExplicitLocalHostname(hostname string) bool {
	return picomcp.IsExplicitLocalHostname(hostname)
}

func newMCPOAuthHTTPClient(serverURL string) *http.Client {
	guard := &mcpOAuthNetworkGuardTransport{}
	var transport *http.Transport
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	} else {
		transport = &http.Transport{}
	}
	if parsed, err := url.Parse(strings.TrimSpace(serverURL)); err == nil {
		guard.configuredHost = strings.ToLower(strings.TrimSpace(parsed.Hostname()))
		guard.allowConfiguredPrivate = mcpExplicitLocalHostname(guard.configuredHost)
	}
	// Pin the addresses approved by the guard into the actual dial. Proxies are
	// deliberately disabled because a proxy would resolve the target again and
	// bypass the local-network boundary.
	transport.Proxy = nil
	transport.DialContext = guard.dialContext
	transport.ResponseHeaderTimeout = 30 * time.Second
	guard.base = transport
	return &http.Client{
		Transport: guard,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many MCP OAuth redirects")
			}
			if !picomcp.IsHTTPSOrLoopbackHTTP(request.URL.String()) {
				return fmt.Errorf("MCP OAuth redirect target must use HTTPS")
			}
			if len(via) > 0 &&
				!sameMCPRemoteOrigin(via[len(via)-1].URL.String(), request.URL.String()) {
				return fmt.Errorf("MCP OAuth redirect target changed origins")
			}
			return nil
		},
	}
}

func renderMCPOAuthCallbackPage(
	w http.ResponseWriter,
	flowID, status, title, errorMessage string,
) {
	payload := map[string]string{
		"type":   "picoclaw-mcp-oauth-result",
		"flowId": flowID,
		"status": status,
	}
	if errorMessage != "" {
		payload["error"] = errorMessage
	}
	payloadJSON, _ := json.Marshal(payload)

	message := title
	if errorMessage != "" {
		message += ": " + errorMessage
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if status == oauthFlowError || status == oauthFlowExpired {
		w.WriteHeader(http.StatusBadRequest)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_, _ = fmt.Fprintf(
		w,
		"<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>PicoClaw MCP OAuth</title></head><body><script>(function(){var payload=%s;var hasOpener=false;try{if(window.opener&&!window.opener.closed){window.opener.postMessage(payload,window.location.origin);hasOpener=true}}catch(e){}var target='/agent/mcp?mcp_oauth_flow_id='+encodeURIComponent(payload.flowId||'')+'&mcp_oauth_status='+encodeURIComponent(payload.status||'');setTimeout(function(){if(hasOpener){window.close();return}window.location.replace(target)},800)})();</script><div style=\"font-family:Inter,system-ui,sans-serif;padding:24px\"><h2>%s</h2><p>%s</p><p>You can close this window.</p></div></body></html>",
		string(payloadJSON),
		html.EscapeString(title),
		html.EscapeString(message),
	)
}
