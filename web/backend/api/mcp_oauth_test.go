package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	picoauth "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
)

type mcpOAuthTestHarness struct {
	configPath string
	handler    *Handler
	mux        *http.ServeMux
}

func newMCPOAuthTestHarness(
	t *testing.T,
	configure func(*config.Config),
) *mcpOAuthTestHarness {
	t.Helper()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", home, err)
	}
	t.Setenv(config.EnvHome, home)

	cfg := config.DefaultConfig()
	if configure != nil {
		configure(cfg)
	}
	configPath := filepath.Join(root, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	t.Cleanup(handler.Shutdown)
	return &mcpOAuthTestHarness{
		configPath: configPath,
		handler:    handler,
		mux:        mux,
	}
}

func (h *mcpOAuthTestHarness) request(
	t *testing.T,
	method string,
	path string,
) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Host = "localhost:18800"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func installMCPOAuthLoginStub(
	t *testing.T,
	stub func(
		context.Context,
		string,
		config.MCPServerConfig,
		picomcp.OAuthLoginOptions,
	) (*picomcp.OAuthLoginResult, error),
) {
	t.Helper()
	original := mcpOAuthLogin
	mcpOAuthLogin = stub
	t.Cleanup(func() {
		mcpOAuthLogin = original
	})
}

func installMCPOAuthClock(t *testing.T, now time.Time) {
	t.Helper()
	original := mcpOAuthNow
	mcpOAuthNow = func() time.Time { return now }
	t.Cleanup(func() {
		mcpOAuthNow = original
	})
}

func decodeMCPOAuthJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; body=%s", err, rec.Body.String())
	}
	return response
}

func remoteMCPOAuthServerConfig(cfg *config.Config) {
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"remote": {
			Enabled: true,
			Type:    "http",
			URL:     "https://mcp.example.test/api",
		},
	}
}

func TestMCPBrowserOAuthFlowPersistsCredential(t *testing.T) {
	harness := newMCPOAuthTestHarness(t, remoteMCPOAuthServerConfig)

	type invocation struct {
		serverName  string
		server      config.MCPServerConfig
		redirectURL string
	}
	invoked := make(chan invocation, 1)
	authorizationResult := make(chan *mcpauth.AuthorizationResult, 1)
	authorizationURL := "https://identity.example.test/authorize?client_id=client-123&state=flow-state"
	expiresAt := time.Now().Add(time.Hour).Round(time.Second)

	installMCPOAuthLoginStub(t, func(
		ctx context.Context,
		serverName string,
		server config.MCPServerConfig,
		options picomcp.OAuthLoginOptions,
	) (*picomcp.OAuthLoginResult, error) {
		invoked <- invocation{
			serverName:  serverName,
			server:      server,
			redirectURL: options.RedirectURL,
		}
		result, err := options.AuthorizationCodeFetcher(ctx, &mcpauth.AuthorizationArgs{
			URL: authorizationURL,
		})
		if err != nil {
			return nil, err
		}
		authorizationResult <- result
		return &picomcp.OAuthLoginResult{
			Token: &oauth2.Token{
				AccessToken:  "oauth-access-token",
				RefreshToken: "oauth-refresh-token",
				TokenType:    "Bearer",
				Expiry:       expiresAt,
			},
			RefreshMetadata: picomcp.OAuthRefreshMetadata{
				TokenURL:     "https://identity.example.test/token",
				ClientID:     "client-123",
				ClientSecret: "client-secret",
				AuthStyle:    "header",
			},
			ToolCount: 2,
			Tools:     []string{"search", "read"},
		}, nil
	})

	start := harness.request(t, http.MethodPost, "/api/mcp/servers/remote/oauth")
	requireMCPStatus(t, start, http.StatusOK)
	startResponse := decodeMCPOAuthJSON(t, start)
	flowID, _ := startResponse["flow_id"].(string)
	if flowID == "" {
		t.Fatalf("flow_id is empty: %#v", startResponse)
	}
	if got := startResponse["auth_url"]; got != authorizationURL {
		t.Fatalf("auth_url = %#v, want %q", got, authorizationURL)
	}

	select {
	case got := <-invoked:
		if got.serverName != "remote" {
			t.Fatalf("serverName = %q, want remote", got.serverName)
		}
		if got.server.URL != "https://mcp.example.test/api" {
			t.Fatalf("server URL = %q, want configured MCP URL", got.server.URL)
		}
		if got.redirectURL != "http://localhost:18800/mcp/oauth/callback" {
			t.Fatalf("redirect URL = %q, want dashboard callback", got.redirectURL)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP OAuth login stub was not invoked")
	}

	status := harness.request(t, http.MethodGet, "/api/mcp/oauth/flows/"+flowID)
	requireMCPStatus(t, status, http.StatusOK)
	statusResponse := decodeMCPOAuthJSON(t, status)
	if statusResponse["status"] != oauthFlowPending {
		t.Fatalf("initial flow status = %#v, want %q", statusResponse["status"], oauthFlowPending)
	}

	callback := harness.request(
		t,
		http.MethodGet,
		"/mcp/oauth/callback?state=flow-state&code=callback-code",
	)
	requireMCPStatus(t, callback, http.StatusOK)
	if body := callback.Body.String(); !strings.Contains(body, "picoclaw-mcp-oauth-result") ||
		!strings.Contains(body, flowID) ||
		!strings.Contains(body, "/agent/mcp?mcp_oauth_flow_id=") {
		t.Fatalf("callback did not render the MCP OAuth postMessage/fallback page: %s", body)
	}

	select {
	case result := <-authorizationResult:
		if result == nil || result.Code != "callback-code" || result.State != "flow-state" {
			t.Fatalf("authorization result = %#v, want callback code and state", result)
		}
	case <-time.After(time.Second):
		t.Fatal("callback code/state was not handed to the authorization fetcher")
	}

	cfg := loadMCPTestConfig(t, harness.configPath)
	server := cfg.Tools.MCP.Servers["remote"]
	if server.Auth == nil {
		t.Fatal("server.Auth is nil after successful OAuth login")
	}
	if server.Auth.Type != "oauth" {
		t.Fatalf("server.Auth.Type = %q, want oauth", server.Auth.Type)
	}
	if server.Auth.CredentialID == "" {
		t.Fatal("server.Auth.CredentialID is empty after successful OAuth login")
	}
	if server.Auth.Revision <= 0 {
		t.Fatalf("server.Auth.Revision = %d, want positive revision", server.Auth.Revision)
	}

	credential, err := picoauth.GetCredential(server.Auth.CredentialID)
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if credential == nil {
		t.Fatal("OAuth credential was not persisted")
	}
	if credential.AccessToken != "oauth-access-token" ||
		credential.RefreshToken != "oauth-refresh-token" ||
		credential.TokenType != "Bearer" ||
		!credential.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("persisted token fields = %#v, want complete OAuth token", credential)
	}
	if credential.Provider != "mcp" || credential.AuthMethod != "oauth" {
		t.Fatalf("persisted credential identity = %#v, want MCP OAuth", credential)
	}
	if credential.OAuthTokenURL != "https://identity.example.test/token" ||
		credential.OAuthClientID != "client-123" ||
		credential.OAuthClientSecret != "client-secret" ||
		credential.OAuthAuthStyle != "header" {
		t.Fatalf("persisted refresh metadata = %#v, want captured OAuth client state", credential)
	}

	status = harness.request(t, http.MethodGet, "/api/mcp/oauth/flows/"+flowID)
	requireMCPStatus(t, status, http.StatusOK)
	statusResponse = decodeMCPOAuthJSON(t, status)
	if statusResponse["status"] != oauthFlowSuccess {
		t.Fatalf("completed flow status = %#v, want %q", statusResponse["status"], oauthFlowSuccess)
	}
	if statusResponse["tool_count"] != float64(2) {
		t.Fatalf("completed flow tool_count = %#v, want 2", statusResponse["tool_count"])
	}

	replay := harness.request(
		t,
		http.MethodGet,
		"/mcp/oauth/callback?state=flow-state&code=replayed-code",
	)
	requireMCPStatus(t, replay, http.StatusBadRequest)
	if body := replay.Body.String(); !strings.Contains(body, "picoclaw-mcp-oauth-result") ||
		!strings.Contains(body, "flow_not_found") {
		t.Fatalf("replayed callback should be rejected without reusing the flow: %s", body)
	}
}

func TestMCPOAuthRecreatedRenamedServerForksCredentialOwnership(t *testing.T) {
	const originalCredentialID = "mcp:foo"
	harness := newMCPOAuthTestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"bar": {
				Enabled: true,
				Type:    "http",
				URL:     "https://original.example.test/mcp",
				Auth: &config.MCPServerAuthConfig{
					Type:         "bearer",
					CredentialID: originalCredentialID,
					Revision:     1,
				},
			},
			"foo": {
				Enabled: true,
				Type:    "http",
				URL:     "https://recreated.example.test/mcp",
			},
		}
	})
	if err := picoauth.SetCredential(originalCredentialID, &picoauth.AuthCredential{
		AccessToken: "renamed-server-token",
		Provider:    "mcp",
		AuthMethod:  "bearer",
	}); err != nil {
		t.Fatalf("SetCredential(original) error = %v", err)
	}

	flow := &mcpOAuthFlow{
		ServerName: "foo",
		ServerURL:  "https://recreated.example.test/mcp",
		Transport:  "http",
	}
	result := &picomcp.OAuthLoginResult{
		Token: &oauth2.Token{
			AccessToken:  "recreated-oauth-token",
			RefreshToken: "recreated-refresh-token",
			TokenType:    "Bearer",
			Expiry:       time.Now().Add(time.Hour),
		},
		RefreshMetadata: picomcp.OAuthRefreshMetadata{
			TokenURL: "https://identity.example.test/token",
			ClientID: "recreated-client",
		},
	}
	if err := harness.handler.persistMCPOAuthResult(flow, result); err != nil {
		t.Fatalf("persistMCPOAuthResult() error = %v", err)
	}

	cfg := loadMCPTestConfig(t, harness.configPath)
	renamedAuth := cfg.Tools.MCP.Servers["bar"].Auth
	recreatedAuth := cfg.Tools.MCP.Servers["foo"].Auth
	if renamedAuth == nil || recreatedAuth == nil {
		t.Fatalf("auth references missing: renamed=%#v recreated=%#v", renamedAuth, recreatedAuth)
	}
	if renamedAuth.CredentialID != originalCredentialID {
		t.Fatalf("renamed credential ID = %q, want %q", renamedAuth.CredentialID, originalCredentialID)
	}
	if recreatedAuth.CredentialID == originalCredentialID {
		t.Fatalf("recreated OAuth server reused renamed credential %q", originalCredentialID)
	}
	renamedCredential, err := picoauth.GetCredential(originalCredentialID)
	if err != nil {
		t.Fatalf("GetCredential(renamed) error = %v", err)
	}
	recreatedCredential, err := picoauth.GetCredential(recreatedAuth.CredentialID)
	if err != nil {
		t.Fatalf("GetCredential(recreated) error = %v", err)
	}
	if renamedCredential == nil || renamedCredential.AccessToken != "renamed-server-token" {
		t.Fatalf("renamed credential = %#v, want original token", renamedCredential)
	}
	if recreatedCredential == nil || recreatedCredential.AccessToken != "recreated-oauth-token" {
		t.Fatalf("recreated credential = %#v, want OAuth token", recreatedCredential)
	}
}

func TestMCPOAuthNewLoginSupersedesOlderResult(t *testing.T) {
	harness := newMCPOAuthTestHarness(t, remoteMCPOAuthServerConfig)
	now := time.Now()
	newFlow := func(id string) *mcpOAuthFlow {
		return &mcpOAuthFlow{
			ID:         id,
			ServerName: "remote",
			ServerURL:  "https://mcp.example.test/api",
			Transport:  "http",
			Status:     oauthFlowPending,
			CreatedAt:  now,
			UpdatedAt:  now,
			ExpiresAt:  now.Add(time.Minute),
			callback:   make(chan mcpOAuthCallbackResult, 1),
			ready:      make(chan struct{}),
			done:       make(chan struct{}),
		}
	}
	older := newFlow("mcp_older")
	newer := newFlow("mcp_newer")
	harness.handler.storeMCPOAuthFlow(older)
	harness.handler.storeMCPOAuthFlow(newer)

	result := &picomcp.OAuthLoginResult{
		Token: &oauth2.Token{
			AccessToken:  "newest-access-token",
			RefreshToken: "newest-refresh-token",
			TokenType:    "Bearer",
			Expiry:       now.Add(time.Hour),
		},
		RefreshMetadata: picomcp.OAuthRefreshMetadata{
			TokenURL: "https://identity.example.test/token",
			ClientID: "newest-client",
		},
	}
	if err := harness.handler.persistMCPOAuthResult(older, result); err == nil ||
		!strings.Contains(err.Error(), "superseded") {
		t.Fatalf("older persist error = %v, want superseded", err)
	}
	cfg := loadMCPTestConfig(t, harness.configPath)
	if cfg.Tools.MCP.Servers["remote"].Auth != nil {
		t.Fatalf("superseded flow changed server auth: %#v", cfg.Tools.MCP.Servers["remote"].Auth)
	}

	if err := harness.handler.persistMCPOAuthResult(newer, result); err != nil {
		t.Fatalf("newer persist error = %v", err)
	}
	cfg = loadMCPTestConfig(t, harness.configPath)
	authConfig := cfg.Tools.MCP.Servers["remote"].Auth
	if authConfig == nil || authConfig.Type != "oauth" {
		t.Fatalf("newer flow did not persist OAuth auth: %#v", authConfig)
	}
	credential, err := picoauth.GetCredential(authConfig.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if credential == nil || credential.AccessToken != "newest-access-token" {
		t.Fatalf("newer credential = %#v", credential)
	}
	status, ok := harness.handler.getMCPOAuthFlow(newer.ID)
	if !ok || status.Status != oauthFlowSuccess {
		t.Fatalf("newer flow status = %#v, want success", status)
	}
}

func TestMCPBrowserOAuthProviderErrorIsReported(t *testing.T) {
	harness := newMCPOAuthTestHarness(t, remoteMCPOAuthServerConfig)

	const authorizationURL = "https://identity.example.test/authorize?state=provider-error-state"
	fetcherError := make(chan error, 1)
	installMCPOAuthLoginStub(t, func(
		ctx context.Context,
		_ string,
		_ config.MCPServerConfig,
		options picomcp.OAuthLoginOptions,
	) (*picomcp.OAuthLoginResult, error) {
		_, err := options.AuthorizationCodeFetcher(ctx, &mcpauth.AuthorizationArgs{
			URL: authorizationURL,
		})
		fetcherError <- err
		if err == nil {
			return nil, errors.New("authorization fetcher unexpectedly succeeded")
		}
		return nil, err
	})

	start := harness.request(t, http.MethodPost, "/api/mcp/servers/remote/oauth")
	requireMCPStatus(t, start, http.StatusOK)
	startResponse := decodeMCPOAuthJSON(t, start)
	flowID, _ := startResponse["flow_id"].(string)
	if flowID == "" {
		t.Fatalf("flow_id is empty: %#v", startResponse)
	}

	callbackPath := "/mcp/oauth/callback?" + url.Values{
		"state":             {"provider-error-state"},
		"error":             {"access_denied"},
		"error_description": {"The user denied access"},
	}.Encode()
	callback := harness.request(t, http.MethodGet, callbackPath)
	requireMCPStatus(t, callback, http.StatusBadRequest)
	if body := callback.Body.String(); !strings.Contains(body, "picoclaw-mcp-oauth-result") ||
		!strings.Contains(body, flowID) ||
		!strings.Contains(body, "access_denied") {
		t.Fatalf("provider error callback body = %s", body)
	}

	select {
	case err := <-fetcherError:
		if err == nil || !strings.Contains(err.Error(), "access_denied") {
			t.Fatalf("authorization fetcher error = %v, want provider error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("provider error was not handed to the authorization fetcher")
	}

	status := harness.request(t, http.MethodGet, "/api/mcp/oauth/flows/"+flowID)
	requireMCPStatus(t, status, http.StatusOK)
	statusResponse := decodeMCPOAuthJSON(t, status)
	if statusResponse["status"] != oauthFlowError {
		t.Fatalf("flow status = %#v, want %q", statusResponse["status"], oauthFlowError)
	}
	if got, _ := statusResponse["error"].(string); !strings.Contains(got, "access_denied") {
		t.Fatalf("flow error = %q, want provider error", got)
	}

	cfg := loadMCPTestConfig(t, harness.configPath)
	if cfg.Tools.MCP.Servers["remote"].Auth != nil {
		t.Fatalf("provider error should not configure MCP auth: %#v", cfg.Tools.MCP.Servers["remote"].Auth)
	}
}

func TestMCPBrowserOAuthRejectsMissingAndLocalServers(t *testing.T) {
	harness := newMCPOAuthTestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"local": {
				Enabled: true,
				Type:    "stdio",
				Command: "local-mcp",
			},
		}
	})

	var calls atomic.Int32
	installMCPOAuthLoginStub(t, func(
		context.Context,
		string,
		config.MCPServerConfig,
		picomcp.OAuthLoginOptions,
	) (*picomcp.OAuthLoginResult, error) {
		calls.Add(1)
		return nil, errors.New("unexpected OAuth login")
	})

	missing := harness.request(t, http.MethodPost, "/api/mcp/servers/missing/oauth")
	requireMCPStatus(t, missing, http.StatusNotFound)

	local := harness.request(t, http.MethodPost, "/api/mcp/servers/local/oauth")
	requireMCPStatus(t, local, http.StatusBadRequest)
	if !strings.Contains(strings.ToLower(local.Body.String()), "remote") {
		t.Fatalf("stdio rejection should explain remote-only OAuth: %s", local.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("OAuth login stub calls = %d, want 0", calls.Load())
	}
}

func TestMCPBrowserOAuthUnknownFlowAndCallbackState(t *testing.T) {
	harness := newMCPOAuthTestHarness(t, remoteMCPOAuthServerConfig)

	status := harness.request(t, http.MethodGet, "/api/mcp/oauth/flows/unknown-flow")
	requireMCPStatus(t, status, http.StatusNotFound)

	callback := harness.request(
		t,
		http.MethodGet,
		"/mcp/oauth/callback?state=unknown-state&code=unused-code",
	)
	requireMCPStatus(t, callback, http.StatusBadRequest)
	if body := callback.Body.String(); !strings.Contains(body, "picoclaw-mcp-oauth-result") ||
		!strings.Contains(strings.ToLower(body), "not found") {
		t.Fatalf("unknown callback state should render a safe postMessage error page: %s", body)
	}
}

func TestMCPBrowserOAuthFlowExpiresAndInvalidatesState(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	installMCPOAuthClock(t, now)
	harness := newMCPOAuthTestHarness(t, remoteMCPOAuthServerConfig)

	var cancelCalls atomic.Int32
	flow := &mcpOAuthFlow{
		ID:         "mcp_expired",
		ServerName: "remote",
		Status:     oauthFlowPending,
		OAuthState: "expired-state",
		CreatedAt:  now.Add(-11 * time.Minute),
		UpdatedAt:  now.Add(-11 * time.Minute),
		ExpiresAt:  now.Add(-time.Minute),
		callback:   make(chan mcpOAuthCallbackResult, 1),
		ready:      make(chan struct{}),
		done:       make(chan struct{}),
		cancel:     func() { cancelCalls.Add(1) },
	}
	harness.handler.mcpOAuthMu.Lock()
	harness.handler.mcpOAuthFlows[flow.ID] = flow
	harness.handler.mcpOAuthState[flow.OAuthState] = flow.ID
	harness.handler.mcpOAuthMu.Unlock()

	status := harness.request(t, http.MethodGet, "/api/mcp/oauth/flows/"+flow.ID)
	requireMCPStatus(t, status, http.StatusOK)
	statusResponse := decodeMCPOAuthJSON(t, status)
	if statusResponse["status"] != oauthFlowExpired {
		t.Fatalf("flow status = %#v, want %q", statusResponse["status"], oauthFlowExpired)
	}
	if statusResponse["error"] != "flow expired" {
		t.Fatalf("flow error = %#v, want flow expired", statusResponse["error"])
	}
	if cancelCalls.Load() != 1 {
		t.Fatalf("flow cancel calls = %d, want 1", cancelCalls.Load())
	}

	callback := harness.request(
		t,
		http.MethodGet,
		"/mcp/oauth/callback?state=expired-state&code=late-code",
	)
	requireMCPStatus(t, callback, http.StatusBadRequest)
	if !strings.Contains(callback.Body.String(), "flow_not_found") {
		t.Fatalf("expired state should no longer be consumable: %s", callback.Body.String())
	}
}

func TestMCPBrowserOAuthStartFailureBecomesTerminalFlow(t *testing.T) {
	harness := newMCPOAuthTestHarness(t, remoteMCPOAuthServerConfig)

	installMCPOAuthLoginStub(t, func(
		context.Context,
		string,
		config.MCPServerConfig,
		picomcp.OAuthLoginOptions,
	) (*picomcp.OAuthLoginResult, error) {
		return nil, errors.New("dynamic client registration failed")
	})

	start := harness.request(t, http.MethodPost, "/api/mcp/servers/remote/oauth")
	requireMCPStatus(t, start, http.StatusBadGateway)
	if !strings.Contains(start.Body.String(), "dynamic client registration failed") {
		t.Fatalf("start error body = %s", start.Body.String())
	}

	harness.handler.mcpOAuthMu.Lock()
	var flowID string
	for id := range harness.handler.mcpOAuthFlows {
		flowID = id
		break
	}
	flowCount := len(harness.handler.mcpOAuthFlows)
	harness.handler.mcpOAuthMu.Unlock()
	if flowCount != 1 || flowID == "" {
		t.Fatalf("stored flows = %d with id %q, want one diagnosable terminal flow", flowCount, flowID)
	}

	status := harness.request(t, http.MethodGet, "/api/mcp/oauth/flows/"+flowID)
	requireMCPStatus(t, status, http.StatusOK)
	statusResponse := decodeMCPOAuthJSON(t, status)
	if statusResponse["status"] != oauthFlowError {
		t.Fatalf("flow status = %#v, want %q", statusResponse["status"], oauthFlowError)
	}
	if got, _ := statusResponse["error"].(string); !strings.Contains(got, "dynamic client registration failed") {
		t.Fatalf("flow error = %q, want start failure", got)
	}

	cfg := loadMCPTestConfig(t, harness.configPath)
	if cfg.Tools.MCP.Servers["remote"].Auth != nil {
		t.Fatalf("start failure should not configure auth: %#v", cfg.Tools.MCP.Servers["remote"].Auth)
	}
	credential, err := picoauth.GetCredential("mcp:remote")
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if credential != nil {
		t.Fatalf("start failure persisted credential = %#v, want nil", credential)
	}
}

func TestMCPBrowserOAuthPersistenceRollsBackCredentialWhenConfigSaveFails(t *testing.T) {
	const credentialID = "mcp:remote"
	harness := newMCPOAuthTestHarness(t, func(cfg *config.Config) {
		remoteMCPOAuthServerConfig(cfg)
		server := cfg.Tools.MCP.Servers["remote"]
		server.Auth = &config.MCPServerAuthConfig{
			Type:         "bearer",
			CredentialID: credentialID,
			Revision:     17,
		}
		cfg.Tools.MCP.Servers["remote"] = server
	})
	originalCredential := &picoauth.AuthCredential{
		AccessToken: "original-bearer-token",
		Provider:    "mcp",
		AuthMethod:  "bearer",
	}
	if err := picoauth.SetCredential(credentialID, originalCredential); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	configDir := filepath.Dir(harness.configPath)
	if err := os.Chmod(harness.configPath, 0o400); err != nil {
		t.Fatalf("Chmod(config) error = %v", err)
	}
	if err := os.Chmod(configDir, 0o500); err != nil {
		t.Fatalf("Chmod(config dir) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(configDir, 0o700)
		_ = os.Chmod(harness.configPath, 0o600)
	})

	err := harness.handler.persistMCPOAuthResult(
		&mcpOAuthFlow{
			ServerName: "remote",
			ServerURL:  "https://mcp.example.test/api",
			Transport:  "http",
			StartingAuth: &config.MCPServerAuthConfig{
				Type:         "bearer",
				CredentialID: credentialID,
				Revision:     17,
			},
		},
		&picomcp.OAuthLoginResult{
			Token: &oauth2.Token{
				AccessToken:  "new-oauth-access-token",
				RefreshToken: "new-oauth-refresh-token",
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "save MCP OAuth configuration") {
		t.Fatalf("persistMCPOAuthResult() error = %v, want config save failure", err)
	}

	credential, getErr := picoauth.GetCredential(credentialID)
	if getErr != nil {
		t.Fatalf("GetCredential() error = %v", getErr)
	}
	if credential == nil ||
		credential.AccessToken != originalCredential.AccessToken ||
		credential.Provider != originalCredential.Provider ||
		credential.AuthMethod != originalCredential.AuthMethod {
		t.Fatalf("credential after rollback = %#v, want %#v", credential, originalCredential)
	}

	cfg := loadMCPTestConfig(t, harness.configPath)
	authConfig := cfg.Tools.MCP.Servers["remote"].Auth
	if authConfig == nil ||
		authConfig.Type != "bearer" ||
		authConfig.CredentialID != credentialID ||
		authConfig.Revision != 17 {
		t.Fatalf("config auth after failed save = %#v, want original bearer linkage", authConfig)
	}
}

func TestMCPBrowserOAuthPersistenceRollsBackCredentialOnConfigRevisionConflict(t *testing.T) {
	const credentialID = "mcp:remote"
	harness := newMCPOAuthTestHarness(t, func(cfg *config.Config) {
		remoteMCPOAuthServerConfig(cfg)
		server := cfg.Tools.MCP.Servers["remote"]
		server.Auth = &config.MCPServerAuthConfig{
			Type:         "bearer",
			CredentialID: credentialID,
			Revision:     17,
		}
		cfg.Tools.MCP.Servers["remote"] = server
	})
	originalCredential := &picoauth.AuthCredential{
		AccessToken: "original-bearer-token",
		Provider:    "mcp",
		AuthMethod:  "bearer",
	}
	if err := picoauth.SetCredential(credentialID, originalCredential); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	originalSave := mcpSaveConfigIfRevision
	var injected atomic.Bool
	mcpSaveConfigIfRevision = func(
		path string,
		cfg *config.Config,
		expectedRevision string,
	) (string, error) {
		if injected.CompareAndSwap(false, true) {
			concurrent, revision, err := config.LoadConfigForUpdateSnapshot(path)
			if err != nil {
				return "", fmt.Errorf("load concurrent config: %w", err)
			}
			concurrent.Workflows.Enabled = true
			concurrent.Workflows.MaxConcurrentRuns = 29
			if _, err = originalSave(path, concurrent, revision); err != nil {
				return "", fmt.Errorf("save concurrent config: %w", err)
			}
		}
		return originalSave(path, cfg, expectedRevision)
	}
	t.Cleanup(func() {
		mcpSaveConfigIfRevision = originalSave
	})

	err := harness.handler.persistMCPOAuthResult(
		&mcpOAuthFlow{
			ServerName: "remote",
			ServerURL:  "https://mcp.example.test/api",
			Transport:  "http",
			StartingAuth: &config.MCPServerAuthConfig{
				Type:         "bearer",
				CredentialID: credentialID,
				Revision:     17,
			},
		},
		&picomcp.OAuthLoginResult{
			Token: &oauth2.Token{
				AccessToken:  "new-oauth-access-token",
				RefreshToken: "new-oauth-refresh-token",
			},
		},
	)
	if !errors.Is(err, config.ErrConfigRevisionMismatch) {
		t.Fatalf(
			"persistMCPOAuthResult() error = %v, want config revision mismatch",
			err,
		)
	}

	credential, getErr := picoauth.GetCredential(credentialID)
	if getErr != nil {
		t.Fatalf("GetCredential() error = %v", getErr)
	}
	if credential == nil ||
		credential.AccessToken != originalCredential.AccessToken ||
		credential.Provider != originalCredential.Provider ||
		credential.AuthMethod != originalCredential.AuthMethod {
		t.Fatalf("credential after CAS rollback = %#v, want %#v", credential, originalCredential)
	}

	cfg := loadMCPTestConfig(t, harness.configPath)
	if !cfg.Workflows.Enabled || cfg.Workflows.MaxConcurrentRuns != 29 {
		t.Fatalf("concurrent workflow settings were lost: %#v", cfg.Workflows)
	}
	authConfig := cfg.Tools.MCP.Servers["remote"].Auth
	if authConfig == nil ||
		authConfig.Type != "bearer" ||
		authConfig.CredentialID != credentialID ||
		authConfig.Revision != 17 {
		t.Fatalf("config auth after CAS conflict = %#v, want original bearer linkage", authConfig)
	}
}

func TestMCPOAuthNetworkGuardBlocksPublicServerPivotToLocalNetworks(t *testing.T) {
	guard := &mcpOAuthNetworkGuardTransport{configuredHost: "8.8.8.8"}

	for _, rawURL := range []string{
		"https://0.0.0.1/token",
		"https://127.0.0.1/token",
		"https://10.0.0.2/register",
		"https://100.64.0.1/token",
		"https://169.254.169.254/latest/meta-data",
	} {
		target, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("url.Parse(%q) error = %v", rawURL, err)
		}
		if err := guard.validateTarget(context.Background(), target); err == nil {
			t.Fatalf("validateTarget(%q) unexpectedly allowed a local-network pivot", rawURL)
		}
	}

	explicit, _ := url.Parse("https://8.8.8.8/mcp")
	if err := guard.validateTarget(context.Background(), explicit); err != nil {
		t.Fatalf("configured MCP target was rejected: %v", err)
	}
}

func TestMCPOAuthNetworkGuardPinsValidatedAddressesIntoDial(t *testing.T) {
	var lookups atomic.Int32
	var dialed string
	guard := &mcpOAuthNetworkGuardTransport{
		configuredHost: "mcp.example.test",
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			if lookups.Add(1) == 1 {
				return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
			}
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		dial: func(_ context.Context, _, address string) (net.Conn, error) {
			dialed = address
			left, right := net.Pipe()
			_ = right.Close()
			return left, nil
		},
	}
	target, _ := url.Parse("https://mcp.example.test/mcp")
	if err := guard.validateTarget(context.Background(), target); err != nil {
		t.Fatalf("validateTarget() error = %v", err)
	}
	connection, err := guard.dialContext(
		context.Background(),
		"tcp",
		"mcp.example.test:443",
	)
	if err != nil {
		t.Fatalf("dialContext() error = %v", err)
	}
	_ = connection.Close()
	if lookups.Load() != 1 {
		t.Fatalf("DNS lookup count = %d, want one pinned lookup", lookups.Load())
	}
	if dialed != "8.8.8.8:443" {
		t.Fatalf("dialed address = %q, want validated public IP", dialed)
	}
}

func TestMCPOAuthNetworkGuardAllowsConfiguredPrivateDNSButNotPrivatePivot(t *testing.T) {
	guard := &mcpOAuthNetworkGuardTransport{
		configuredHost:         "mcp.internal",
		allowConfiguredPrivate: true,
		lookupIP: func(_ context.Context, hostname string) ([]net.IPAddr, error) {
			switch hostname {
			case "mcp.internal", "identity-same.internal", "identity.example.com":
				return []net.IPAddr{{IP: net.ParseIP("10.0.0.2")}}, nil
			case "identity-other.internal":
				return []net.IPAddr{{IP: net.ParseIP("10.0.0.3")}}, nil
			default:
				return nil, fmt.Errorf("unexpected lookup for %q", hostname)
			}
		},
	}
	configured, _ := url.Parse("https://mcp.internal/mcp")
	if err := guard.validateTarget(context.Background(), configured); err != nil {
		t.Fatalf("configured private DNS target was rejected: %v", err)
	}
	sameAddress, _ := url.Parse("https://identity-same.internal/token")
	if err := guard.validateTarget(context.Background(), sameAddress); err != nil {
		t.Fatalf("same-address private OAuth target was rejected: %v", err)
	}
	publicLooking, _ := url.Parse("https://identity.example.com/token")
	if err := guard.validateTarget(context.Background(), publicLooking); err == nil {
		t.Fatal("public-looking OAuth hostname resolving to the same private address was allowed")
	}
	privatePivot, _ := url.Parse("https://identity-other.internal/token")
	if err := guard.validateTarget(context.Background(), privatePivot); err == nil {
		t.Fatal("different private OAuth target was allowed")
	}
}

func TestMCPOAuthHTTPClientRejectsHTTPSDowngradeRedirect(t *testing.T) {
	client := newMCPOAuthHTTPClient("https://203.0.113.10/mcp")
	request := httptest.NewRequest(http.MethodPost, "http://example.test/token", nil)
	if err := client.CheckRedirect(request, nil); err == nil {
		t.Fatal("CheckRedirect() allowed an HTTPS-to-HTTP OAuth downgrade")
	}
}

func TestMCPOAuthHTTPClientRejectsCrossOriginHTTPSRedirect(t *testing.T) {
	client := newMCPOAuthHTTPClient("https://mcp.example.test/mcp")
	previous := httptest.NewRequest(http.MethodPost, "https://mcp.example.test/token", nil)
	redirect := httptest.NewRequest(http.MethodPost, "https://attacker.example/token", nil)
	if err := client.CheckRedirect(redirect, []*http.Request{previous}); err == nil {
		t.Fatal("CheckRedirect() allowed a cross-origin OAuth redirect")
	}
}

func TestBuildMCPOAuthRedirectURITrustsOnlyConfiguredProxy(t *testing.T) {
	handler := NewHandler(filepath.Join(t.TempDir(), "config.json"))
	request := httptest.NewRequest(http.MethodPost, "http://internal/api/mcp", nil)
	request.Host = "internal:18800"
	request.RemoteAddr = "198.51.100.20:12345"
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "dashboard.example.test")

	if got := handler.buildMCPOAuthRedirectURI(request); got != "http://internal:18800/mcp/oauth/callback" {
		t.Fatalf("untrusted proxy redirect URI = %q", got)
	}

	handler.serverTrustedProxyCIDRs = []string{"198.51.100.0/24"}
	if got := handler.buildMCPOAuthRedirectURI(request); got != "https://dashboard.example.test/mcp/oauth/callback" {
		t.Fatalf("trusted proxy redirect URI = %q", got)
	}
}
