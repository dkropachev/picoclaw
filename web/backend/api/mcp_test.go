package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	picoauth "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

type mcpAPITestHarness struct {
	configPath string
	handler    *Handler
	mux        *http.ServeMux
}

func newMCPAPITestHarness(
	t *testing.T,
	configure func(*config.Config),
) *mcpAPITestHarness {
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

	mux := http.NewServeMux()
	handler := NewHandler(configPath)
	handler.RegisterRoutes(mux)
	return &mcpAPITestHarness{configPath: configPath, handler: handler, mux: mux}
}

func (h *mcpAPITestHarness) request(
	t *testing.T,
	method string,
	path string,
	payload any,
) *httptest.ResponseRecorder {
	t.Helper()

	var body *strings.Reader
	if payload == nil {
		body = strings.NewReader("")
	} else {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		body = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func requireMCPStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, want, rec.Body.String())
	}
}

func loadMCPTestConfig(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	return cfg
}

func TestMCPGetRedactsSecretsAndReportsAuthStatus(t *testing.T) {
	const (
		envSecret        = "env-value-that-must-not-leak"
		headerSecret     = "header-value-that-must-not-leak"
		tokenSecret      = "access-token-that-must-not-leak"
		expiredSecret    = "expired-token-that-must-not-leak"
		credentialSecret = "mcp:credential-id-must-not-leak"
	)
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Enabled = true
		cfg.Tools.MCP.Discovery = config.ToolDiscoveryConfig{
			Enabled:          true,
			TTL:              7,
			MaxSearchResults: 4,
			UseBM25:          true,
		}
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"local": {
				Enabled: true,
				Type:    "stdio",
				Command: "npx",
				Args:    []string{"-y", "local-server"},
				Env: map[string]string{
					"API_TOKEN": envSecret,
					"PLAIN":     "also-not-returned",
				},
			},
			"remote": {
				Enabled: true,
				Type:    "http",
				URL:     "https://mcp.example.test/api",
				Headers: map[string]string{
					"X-API-Key": headerSecret,
					"X-Tenant":  "tenant-secret",
				},
				Auth: &config.MCPServerAuthConfig{
					Type:         "bearer",
					CredentialID: credentialSecret,
				},
			},
			"expired": {
				Enabled: true,
				Type:    "sse",
				URL:     "https://expired.example.test/sse",
				Auth: &config.MCPServerAuthConfig{
					Type: "oauth",
				},
			},
			"missing": {
				Enabled: true,
				Type:    "http",
				URL:     "https://missing.example.test/mcp",
				Auth: &config.MCPServerAuthConfig{
					Type: "bearer",
				},
			},
		}
	})
	if err := picoauth.SetCredential(credentialSecret, &picoauth.AuthCredential{
		AccessToken: tokenSecret,
		Provider:    "mcp",
		AuthMethod:  "bearer",
	}); err != nil {
		t.Fatalf("SetCredential(remote) error = %v", err)
	}
	if err := picoauth.SetCredential("mcp:expired", &picoauth.AuthCredential{
		AccessToken: expiredSecret,
		ExpiresAt:   time.Now().Add(-time.Hour),
		Provider:    "mcp",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential(expired) error = %v", err)
	}

	rec := harness.request(t, http.MethodGet, "/api/mcp", nil)
	requireMCPStatus(t, rec, http.StatusOK)

	for _, secret := range []string{
		envSecret,
		headerSecret,
		tokenSecret,
		expiredSecret,
		credentialSecret,
		"tenant-secret",
	} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("GET /api/mcp leaked %q: %s", secret, rec.Body.String())
		}
	}

	var response mcpConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !response.Enabled {
		t.Fatal("response.Enabled = false, want true")
	}
	if response.Discovery.TTL != 7 || response.Discovery.MaxSearchResults != 4 {
		t.Fatalf("response.Discovery = %#v, want persisted discovery settings", response.Discovery)
	}
	if len(response.Servers) != 4 {
		t.Fatalf("len(response.Servers) = %d, want 4", len(response.Servers))
	}

	servers := make(map[string]mcpServerSummary, len(response.Servers))
	for _, server := range response.Servers {
		servers[server.Name] = server
	}
	if got := servers["local"].EnvKeys; !reflect.DeepEqual(got, []string{"API_TOKEN", "PLAIN"}) {
		t.Fatalf("local.EnvKeys = %#v, want sorted keys", got)
	}
	if got := servers["remote"].HeaderKeys; !reflect.DeepEqual(got, []string{"X-API-Key", "X-Tenant"}) {
		t.Fatalf("remote.HeaderKeys = %#v, want sorted keys", got)
	}
	if got := servers["remote"].Auth; got.Type != "bearer" || !got.Configured || got.Expired {
		t.Fatalf("remote.Auth = %#v, want configured non-expired bearer", got)
	}
	if got := servers["expired"].Auth; got.Type != "oauth" || !got.Configured || !got.Expired {
		t.Fatalf("expired.Auth = %#v, want configured expired oauth", got)
	}
	if got := servers["missing"].Auth; got.Type != "bearer" || got.Configured || got.Expired {
		t.Fatalf("missing.Auth = %#v, want unconfigured bearer", got)
	}
	if got := servers["local"].Auth; got.Type != "none" || got.Configured || got.Expired {
		t.Fatalf("local.Auth = %#v, want no auth", got)
	}
}

func TestMCPServerCRUDPreservesAndRemovesExplicitSecretKeys(t *testing.T) {
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Enabled = false
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{}
	})

	rec := harness.request(t, http.MethodPost, "/api/mcp/servers", map[string]any{
		"name":     "local",
		"type":     "stdio",
		"command":  "npx",
		"args":     []string{"-y", "old-server"},
		"enabled":  false,
		"env_file": ".env.old",
		"env": map[string]string{
			"KEEP": "keep-secret",
			"DROP": "drop-secret",
		},
		"env_keys": []string{"KEEP", "DROP"},
	})
	requireMCPStatus(t, rec, http.StatusOK)

	cfg := loadMCPTestConfig(t, harness.configPath)
	if !cfg.Tools.MCP.Enabled {
		t.Fatal("adding the first MCP server should enable MCP globally")
	}
	if cfg.Tools.MCP.Servers["local"].Enabled {
		t.Fatal("server Enabled = true, want explicitly requested false")
	}

	rec = harness.request(t, http.MethodPut, "/api/mcp/servers/local", map[string]any{
		"name":     "renamed-local",
		"type":     "stdio",
		"command":  "node",
		"args":     []string{"new-server.js"},
		"enabled":  true,
		"deferred": false,
		"env_file": ".env.new",
		"env": map[string]string{
			"KEEP": "",
			"NEW":  "new-secret",
		},
		"env_keys": []string{"KEEP", "NEW"},
	})
	requireMCPStatus(t, rec, http.StatusOK)

	cfg = loadMCPTestConfig(t, harness.configPath)
	if _, exists := cfg.Tools.MCP.Servers["local"]; exists {
		t.Fatal("old server name still exists after rename")
	}
	local, exists := cfg.Tools.MCP.Servers["renamed-local"]
	if !exists {
		t.Fatal("renamed-local server was not saved")
	}
	if got, want := local.Env, map[string]string{
		"KEEP": "keep-secret",
		"NEW":  "new-secret",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("renamed-local.Env = %#v, want %#v", got, want)
	}
	if local.Deferred == nil || *local.Deferred {
		t.Fatalf("renamed-local.Deferred = %#v, want explicit false", local.Deferred)
	}
	if local.Command != "node" || !reflect.DeepEqual(local.Args, []string{"new-server.js"}) ||
		local.EnvFile != ".env.new" || !local.Enabled {
		t.Fatalf("renamed-local = %#v, want updated stdio settings", local)
	}

	rec = harness.request(t, http.MethodPost, "/api/mcp/servers", map[string]any{
		"name": "remote",
		"type": "http",
		"url":  "https://mcp.example.test/api",
		"headers": map[string]string{
			"X-Keep": "keep-header-secret",
			"X-Drop": "drop-header-secret",
		},
		"header_keys": []string{"X-Keep", "X-Drop"},
	})
	requireMCPStatus(t, rec, http.StatusOK)

	rec = harness.request(t, http.MethodPut, "/api/mcp/servers/remote", map[string]any{
		"name": "renamed-remote",
		"type": "sse",
		"url":  "https://mcp.example.test/events",
		"headers": map[string]string{
			"x-keep": "",
			"X-New":  "new-header-secret",
		},
		"header_keys": []string{"x-keep", "X-New"},
	})
	requireMCPStatus(t, rec, http.StatusOK)

	cfg = loadMCPTestConfig(t, harness.configPath)
	if _, oldRemoteExists := cfg.Tools.MCP.Servers["remote"]; oldRemoteExists {
		t.Fatal("old remote server name still exists after rename")
	}
	remote, exists := cfg.Tools.MCP.Servers["renamed-remote"]
	if !exists {
		t.Fatal("renamed-remote server was not saved")
	}
	if got, want := remote.Headers, map[string]string{
		"x-keep": "keep-header-secret",
		"X-New":  "new-header-secret",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("renamed-remote.Headers = %#v, want %#v", got, want)
	}
	if remote.Type != "sse" || remote.URL != "https://mcp.example.test/events" {
		t.Fatalf("renamed-remote = %#v, want updated SSE settings", remote)
	}

	rec = harness.request(t, http.MethodDelete, "/api/mcp/servers/renamed-local", nil)
	requireMCPStatus(t, rec, http.StatusOK)
	cfg = loadMCPTestConfig(t, harness.configPath)
	if !cfg.Tools.MCP.Enabled {
		t.Fatal("deleting a non-final server should leave MCP enabled")
	}

	rec = harness.request(t, http.MethodDelete, "/api/mcp/servers/renamed-remote", nil)
	requireMCPStatus(t, rec, http.StatusOK)
	cfg = loadMCPTestConfig(t, harness.configPath)
	if cfg.Tools.MCP.Enabled {
		t.Fatal("deleting the final server should disable MCP globally")
	}
	if len(cfg.Tools.MCP.Servers) != 0 {
		t.Fatalf("len(Servers) = %d, want 0", len(cfg.Tools.MCP.Servers))
	}
}

func TestMCPRemoteOriginEditDisconnectsCredentialAndSecretHeaders(t *testing.T) {
	for _, authType := range []string{"bearer", "oauth"} {
		t.Run(authType, func(t *testing.T) {
			const credentialID = "mcp:remote"
			harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
				cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
					"remote": {
						Enabled: true,
						Type:    "http",
						URL:     "https://mcp.example.test/original",
						Headers: map[string]string{
							"X-Secret": "persisted-header-secret",
						},
						Auth: &config.MCPServerAuthConfig{
							Type:         authType,
							CredentialID: credentialID,
							Revision:     9,
						},
					},
				}
			})
			if err := picoauth.SetCredential(credentialID, &picoauth.AuthCredential{
				AccessToken: "persisted-access-token",
				Provider:    "mcp",
				AuthMethod:  authType,
			}); err != nil {
				t.Fatalf("SetCredential() error = %v", err)
			}

			rec := harness.request(t, http.MethodPut, "/api/mcp/servers/remote", map[string]any{
				"name": "remote",
				"type": "http",
				"url":  "https://mcp.example.test/new-path",
				"headers": map[string]string{
					"X-Secret": "",
				},
				"header_keys": []string{"X-Secret"},
			})
			requireMCPStatus(t, rec, http.StatusOK)

			cfg := loadMCPTestConfig(t, harness.configPath)
			server := cfg.Tools.MCP.Servers["remote"]
			if server.Auth == nil ||
				server.Auth.Type != authType ||
				server.Auth.CredentialID != credentialID ||
				server.Auth.Revision != 9 {
				t.Fatalf("same-origin path edit changed auth linkage: %#v", server.Auth)
			}
			if got := server.Headers["X-Secret"]; got != "persisted-header-secret" {
				t.Fatalf("same-origin path edit header = %q, want preserved secret", got)
			}
			if credential, err := picoauth.GetCredential(credentialID); err != nil ||
				credential == nil ||
				credential.AccessToken != "persisted-access-token" {
				t.Fatalf("same-origin path edit credential = %#v, err=%v", credential, err)
			}

			rec = harness.request(t, http.MethodPut, "/api/mcp/servers/remote", map[string]any{
				"name": "remote",
				"type": "http",
				"url":  "https://other.example.test/mcp",
				"headers": map[string]string{
					"X-Secret": "",
				},
				"header_keys": []string{"X-Secret"},
			})
			requireMCPStatus(t, rec, http.StatusOK)

			cfg = loadMCPTestConfig(t, harness.configPath)
			server = cfg.Tools.MCP.Servers["remote"]
			if server.Auth != nil {
				t.Fatalf("cross-origin edit preserved auth linkage: %#v", server.Auth)
			}
			if len(server.Headers) != 0 {
				t.Fatalf("cross-origin edit preserved blank secret headers: %#v", server.Headers)
			}
			credential, err := picoauth.GetCredential(credentialID)
			if err != nil {
				t.Fatalf("GetCredential() error = %v", err)
			}
			if credential != nil {
				t.Fatalf("cross-origin edit retained unshared credential: %#v", credential)
			}
		})
	}
}

func TestMCPServerValidationRejectsUnsafeOrIncompleteInput(t *testing.T) {
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{}
	})

	tests := []struct {
		name     string
		path     string
		payload  any
		contains string
	}{
		{
			name: "invalid server name",
			path: "/api/mcp/servers",
			payload: map[string]any{
				"name":    "bad name",
				"type":    "stdio",
				"command": "npx",
			},
			contains: "server name may contain only",
		},
		{
			name: "missing stdio command",
			path: "/api/mcp/servers",
			payload: map[string]any{
				"name": "local",
				"type": "stdio",
			},
			contains: "command is required",
		},
		{
			name: "invalid remote URL",
			path: "/api/mcp/servers",
			payload: map[string]any{
				"name": "remote",
				"type": "http",
				"url":  "ftp://mcp.example.test",
			},
			contains: "valid HTTP(S) URL",
		},
		{
			name: "credentials embedded in URL",
			path: "/api/mcp/servers",
			payload: map[string]any{
				"name": "remote",
				"type": "http",
				"url":  "https://user:secret@mcp.example.test/api",
			},
			contains: "credentials must not be embedded",
		},
		{
			name: "reserved transport header",
			path: "/api/mcp/servers",
			payload: map[string]any{
				"name": "remote",
				"type": "http",
				"url":  "https://mcp.example.test/api",
				"headers": map[string]string{
					"mCp-SeSsIoN-iD": "session",
				},
			},
			contains: "managed by the MCP transport",
		},
		{
			name: "header newline",
			path: "/api/mcp/servers",
			payload: map[string]any{
				"name": "remote",
				"type": "http",
				"url":  "https://mcp.example.test/api",
				"headers": map[string]string{
					"X-Unsafe": "one\r\ntwo",
				},
			},
			contains: "contains an invalid value",
		},
		{
			name: "duplicate case-insensitive header key",
			path: "/api/mcp/servers",
			payload: map[string]any{
				"name": "remote",
				"type": "http",
				"url":  "https://mcp.example.test/api",
				"headers": map[string]string{
					"X-Key": "value",
				},
				"header_keys": []string{"X-Key", "x-key"},
			},
			contains: "duplicate key",
		},
		{
			name: "blank new environment value",
			path: "/api/mcp/servers",
			payload: map[string]any{
				"name":    "local",
				"type":    "stdio",
				"command": "npx",
				"env": map[string]string{
					"NEW_SECRET": "",
				},
				"env_keys": []string{"NEW_SECRET"},
			},
			contains: "value is required for new key",
		},
		{
			name: "unknown JSON field",
			path: "/api/mcp/servers",
			payload: map[string]any{
				"name":       "local",
				"type":       "stdio",
				"command":    "npx",
				"unexpected": true,
			},
			contains: "unknown field",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := harness.request(t, http.MethodPost, tt.path, tt.payload)
			requireMCPStatus(t, rec, http.StatusBadRequest)
			if !strings.Contains(rec.Body.String(), tt.contains) {
				t.Fatalf("body = %q, want substring %q", rec.Body.String(), tt.contains)
			}
		})
	}

	rec := harness.request(t, http.MethodPatch, "/api/mcp/settings", map[string]any{
		"enabled": true,
		"discovery": map[string]any{
			"enabled":            true,
			"ttl":                0,
			"max_search_results": 5,
			"use_bm25":           true,
			"use_regex":          false,
		},
	})
	requireMCPStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "ttl must be at least 1") {
		t.Fatalf("body = %q, want discovery TTL validation", rec.Body.String())
	}

	cfg := loadMCPTestConfig(t, harness.configPath)
	if len(cfg.Tools.MCP.Servers) != 0 {
		t.Fatalf("invalid requests persisted servers: %#v", cfg.Tools.MCP.Servers)
	}
}

func TestMCPMutationsRejectCrossOriginAndNonJSONRequests(t *testing.T) {
	harness := newMCPAPITestHarness(t, nil)
	payload := `{"name":"unsafe","enabled":true,"type":"stdio","command":"sh"}`

	crossOrigin := httptest.NewRequest(
		http.MethodPost,
		"http://launcher.local/api/mcp/servers",
		strings.NewReader(payload),
	)
	crossOrigin.Header.Set("Content-Type", "text/plain")
	crossOrigin.Header.Set("Origin", "http://localhost:9999")
	crossOrigin.Header.Set("Sec-Fetch-Site", "same-site")
	rec := httptest.NewRecorder()
	harness.mux.ServeHTTP(rec, crossOrigin)
	requireMCPStatus(t, rec, http.StatusForbidden)

	nonJSON := httptest.NewRequest(
		http.MethodPost,
		"http://launcher.local/api/mcp/servers",
		strings.NewReader(payload),
	)
	nonJSON.Header.Set("Content-Type", "text/plain")
	nonJSON.Header.Set("Origin", "http://launcher.local")
	nonJSON.Header.Set("Sec-Fetch-Site", "same-origin")
	rec = httptest.NewRecorder()
	harness.mux.ServeHTTP(rec, nonJSON)
	requireMCPStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "application/json") {
		t.Fatalf("response = %q, want JSON content-type requirement", rec.Body.String())
	}

	if servers := loadMCPTestConfig(t, harness.configPath).Tools.MCP.Servers; len(servers) != 0 {
		t.Fatalf("rejected cross-origin request mutated servers: %#v", servers)
	}
}

func TestMCPMutationOriginUsesTrustedExternalProxyHost(t *testing.T) {
	harness := newMCPAPITestHarness(t, nil)
	harness.handler.serverTrustedProxyCIDRs = []string{"198.51.100.0/24"}
	payload := `{"name":"proxied","enabled":true,"type":"stdio","command":"server"}`
	request := httptest.NewRequest(
		http.MethodPost,
		"http://launcher-internal:18800/api/mcp/servers",
		strings.NewReader(payload),
	)
	request.Host = "launcher-internal:18800"
	request.RemoteAddr = "198.51.100.20:12345"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://dashboard.example.test")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("X-Forwarded-Host", "dashboard.example.test")

	rec := httptest.NewRecorder()
	harness.mux.ServeHTTP(rec, request)
	requireMCPStatus(t, rec, http.StatusOK)
	if _, ok := loadMCPTestConfig(t, harness.configPath).Tools.MCP.Servers["proxied"]; !ok {
		t.Fatal("trusted proxied mutation was not persisted")
	}
}

func TestMCPBearerCredentialLifecycleIsIsolatedAndRevisioned(t *testing.T) {
	const (
		firstToken  = "first-mcp-token"
		secondToken = "replacement-mcp-token"
	)
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"remote": {
				Enabled: true,
				Type:    "http",
				URL:     "https://mcp.example.test/api",
			},
		}
	})
	if err := picoauth.SetCredential("openai:test-account", &picoauth.AuthCredential{
		AccessToken: "unrelated-provider-token",
		Provider:    "openai",
		AuthMethod:  "api_key",
	}); err != nil {
		t.Fatalf("SetCredential(unrelated) error = %v", err)
	}

	rec := harness.request(t, http.MethodPut, "/api/mcp/servers/remote/credential", map[string]any{
		"auth_type": "bearer",
		"token":     "  " + firstToken + "  ",
	})
	requireMCPStatus(t, rec, http.StatusOK)

	cfg := loadMCPTestConfig(t, harness.configPath)
	server := cfg.Tools.MCP.Servers["remote"]
	if server.Auth == nil || server.Auth.Type != "bearer" || server.Auth.Revision <= 0 {
		t.Fatalf("server.Auth = %#v, want revisioned bearer reference", server.Auth)
	}
	credentialID := server.Auth.CredentialID
	if credentialID == "" {
		t.Fatal("server.Auth.CredentialID is empty")
	}
	firstRevision := server.Auth.Revision
	credential, err := picoauth.GetCredential(credentialID)
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if credential == nil || credential.AccessToken != firstToken {
		t.Fatalf("credential = %#v, want first trimmed token", credential)
	}
	configRaw, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	if strings.Contains(string(configRaw), firstToken) {
		t.Fatalf("config.json contains bearer token: %s", configRaw)
	}

	rec = harness.request(t, http.MethodGet, "/api/mcp", nil)
	requireMCPStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), firstToken) {
		t.Fatalf("GET /api/mcp leaked bearer token: %s", rec.Body.String())
	}

	rec = harness.request(t, http.MethodPut, "/api/mcp/servers/remote/credential", map[string]any{
		"auth_type": "bearer",
		"token":     secondToken,
	})
	requireMCPStatus(t, rec, http.StatusOK)

	cfg = loadMCPTestConfig(t, harness.configPath)
	server = cfg.Tools.MCP.Servers["remote"]
	if server.Auth == nil {
		t.Fatal("server.Auth = nil after replacement")
	}
	if server.Auth.CredentialID != credentialID {
		t.Fatalf(
			"CredentialID = %q after replacement, want stable %q",
			server.Auth.CredentialID,
			credentialID,
		)
	}
	if server.Auth.Revision <= firstRevision {
		t.Fatalf(
			"Revision = %d after replacement, want > %d",
			server.Auth.Revision,
			firstRevision,
		)
	}
	credential, err = picoauth.GetCredential(credentialID)
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if credential == nil || credential.AccessToken != secondToken {
		t.Fatalf("credential = %#v, want replacement token", credential)
	}

	rec = harness.request(t, http.MethodDelete, "/api/mcp/servers/remote/credential", nil)
	requireMCPStatus(t, rec, http.StatusOK)
	cfg = loadMCPTestConfig(t, harness.configPath)
	if cfg.Tools.MCP.Servers["remote"].Auth != nil {
		t.Fatalf("server.Auth = %#v after disconnect, want nil", cfg.Tools.MCP.Servers["remote"].Auth)
	}
	credential, err = picoauth.GetCredential(credentialID)
	if err != nil {
		t.Fatalf("GetCredential(MCP) error = %v", err)
	}
	if credential != nil {
		t.Fatalf("MCP credential still exists after disconnect: %#v", credential)
	}
	unrelated, err := picoauth.GetCredential("openai:test-account")
	if err != nil {
		t.Fatalf("GetCredential(unrelated) error = %v", err)
	}
	if unrelated == nil || unrelated.AccessToken != "unrelated-provider-token" {
		t.Fatalf("unrelated credential was modified: %#v", unrelated)
	}
}

func TestMCPMutationUsesSharedConfigLockAndPreservesConcurrentSettings(t *testing.T) {
	harness := newMCPAPITestHarness(t, nil)

	harness.handler.configMutationMu.Lock()
	configLocked := true
	defer func() {
		if configLocked {
			harness.handler.configMutationMu.Unlock()
		}
	}()

	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- harness.request(t, http.MethodPatch, "/api/mcp/settings", map[string]any{
			"enabled": true,
			"discovery": map[string]any{
				"enabled":            true,
				"ttl":                60,
				"max_search_results": 7,
				"use_bm25":           true,
				"use_regex":          false,
			},
		})
	}()

	select {
	case rec := <-response:
		t.Fatalf(
			"MCP mutation completed while shared config lock was held: status=%d body=%s",
			rec.Code,
			rec.Body.String(),
		)
	case <-time.After(50 * time.Millisecond):
	}

	concurrent, revision, err := config.LoadConfigForUpdateSnapshot(harness.configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdateSnapshot() error = %v", err)
	}
	concurrent.Workflows.Enabled = true
	concurrent.Workflows.MaxConcurrentRuns = 23
	if _, err = config.SaveConfigIfRevision(
		harness.configPath,
		concurrent,
		revision,
	); err != nil {
		t.Fatalf("SaveConfigIfRevision(concurrent workflow settings) error = %v", err)
	}

	harness.handler.configMutationMu.Unlock()
	configLocked = false

	var rec *httptest.ResponseRecorder
	select {
	case rec = <-response:
	case <-time.After(time.Second):
		t.Fatal("MCP mutation did not resume after shared config lock was released")
	}
	requireMCPStatus(t, rec, http.StatusOK)

	saved := loadMCPTestConfig(t, harness.configPath)
	if !saved.Workflows.Enabled || saved.Workflows.MaxConcurrentRuns != 23 {
		t.Fatalf(
			"concurrent workflow settings were lost: %#v",
			saved.Workflows,
		)
	}
	if !saved.Tools.MCP.Enabled ||
		!saved.Tools.MCP.Discovery.Enabled ||
		saved.Tools.MCP.Discovery.MaxSearchResults != 7 {
		t.Fatalf("MCP settings were not persisted: %#v", saved.Tools.MCP)
	}
}

func TestMCPBearerCredentialRollsBackOnConfigRevisionConflict(t *testing.T) {
	const credentialID = "mcp:remote"
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"remote": {
				Enabled: true,
				Type:    "http",
				URL:     "https://mcp.example.test/api",
				Auth: &config.MCPServerAuthConfig{
					Type:         "bearer",
					CredentialID: credentialID,
					Revision:     11,
				},
			},
		}
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
	injected := false
	mcpSaveConfigIfRevision = func(
		path string,
		cfg *config.Config,
		expectedRevision string,
	) (string, error) {
		if !injected {
			injected = true
			concurrent, revision, err := config.LoadConfigForUpdateSnapshot(path)
			if err != nil {
				return "", fmt.Errorf("load concurrent config: %w", err)
			}
			concurrent.Workflows.Enabled = true
			concurrent.Workflows.MaxConcurrentRuns = 31
			if _, err = originalSave(path, concurrent, revision); err != nil {
				return "", fmt.Errorf("save concurrent config: %w", err)
			}
		}
		return originalSave(path, cfg, expectedRevision)
	}
	t.Cleanup(func() {
		mcpSaveConfigIfRevision = originalSave
	})

	rec := harness.request(
		t,
		http.MethodPut,
		"/api/mcp/servers/remote/credential",
		map[string]any{
			"auth_type": "bearer",
			"token":     "replacement-bearer-token",
		},
	)
	requireMCPStatus(t, rec, http.StatusConflict)

	credential, err := picoauth.GetCredential(credentialID)
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if credential == nil ||
		credential.AccessToken != originalCredential.AccessToken ||
		credential.Provider != originalCredential.Provider ||
		credential.AuthMethod != originalCredential.AuthMethod {
		t.Fatalf("credential after CAS rollback = %#v, want %#v", credential, originalCredential)
	}

	cfg := loadMCPTestConfig(t, harness.configPath)
	if !cfg.Workflows.Enabled || cfg.Workflows.MaxConcurrentRuns != 31 {
		t.Fatalf("concurrent workflow settings were lost: %#v", cfg.Workflows)
	}
	authConfig := cfg.Tools.MCP.Servers["remote"].Auth
	if authConfig == nil ||
		authConfig.Type != "bearer" ||
		authConfig.CredentialID != credentialID ||
		authConfig.Revision != 11 {
		t.Fatalf("config auth after CAS conflict = %#v, want original bearer linkage", authConfig)
	}
}

func TestMCPRecreatedRenamedServerForksDefaultCredentialOwnership(t *testing.T) {
	const (
		originalToken  = "renamed-server-token"
		recreatedToken = "recreated-server-token"
	)
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"foo": {
				Enabled: true,
				Type:    "http",
				URL:     "https://original.example.test/mcp",
			},
		}
	})

	rec := harness.request(t, http.MethodPut, "/api/mcp/servers/foo/credential", map[string]any{
		"auth_type": "bearer",
		"token":     originalToken,
	})
	requireMCPStatus(t, rec, http.StatusOK)

	rec = harness.request(t, http.MethodPut, "/api/mcp/servers/foo", map[string]any{
		"name":    "bar",
		"enabled": true,
		"type":    "http",
		"url":     "https://original.example.test/mcp",
	})
	requireMCPStatus(t, rec, http.StatusOK)

	rec = harness.request(t, http.MethodPost, "/api/mcp/servers", map[string]any{
		"name":    "foo",
		"enabled": true,
		"type":    "http",
		"url":     "https://recreated.example.test/mcp",
	})
	requireMCPStatus(t, rec, http.StatusOK)
	rec = harness.request(t, http.MethodPut, "/api/mcp/servers/foo/credential", map[string]any{
		"auth_type": "bearer",
		"token":     recreatedToken,
	})
	requireMCPStatus(t, rec, http.StatusOK)

	cfg := loadMCPTestConfig(t, harness.configPath)
	renamedAuth := cfg.Tools.MCP.Servers["bar"].Auth
	recreatedAuth := cfg.Tools.MCP.Servers["foo"].Auth
	if renamedAuth == nil || recreatedAuth == nil {
		t.Fatalf("auth references missing: renamed=%#v recreated=%#v", renamedAuth, recreatedAuth)
	}
	if renamedAuth.CredentialID == recreatedAuth.CredentialID {
		t.Fatalf("renamed and recreated servers share credential %q", renamedAuth.CredentialID)
	}
	renamedCredential, err := picoauth.GetCredential(renamedAuth.CredentialID)
	if err != nil {
		t.Fatalf("GetCredential(renamed) error = %v", err)
	}
	recreatedCredential, err := picoauth.GetCredential(recreatedAuth.CredentialID)
	if err != nil {
		t.Fatalf("GetCredential(recreated) error = %v", err)
	}
	if renamedCredential == nil || renamedCredential.AccessToken != originalToken {
		t.Fatalf("renamed credential = %#v, want original token", renamedCredential)
	}
	if recreatedCredential == nil || recreatedCredential.AccessToken != recreatedToken {
		t.Fatalf("recreated credential = %#v, want recreated token", recreatedCredential)
	}
}

func TestMCPBearerCredentialRequiresHTTPSExceptLoopback(t *testing.T) {
	t.Run("public HTTP is rejected", func(t *testing.T) {
		harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
			cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
				"remote": {
					Enabled: true,
					Type:    "http",
					URL:     "http://mcp.example.test/api",
				},
			}
		})

		rec := harness.request(t, http.MethodPut, "/api/mcp/servers/remote/credential", map[string]any{
			"auth_type": "bearer",
			"token":     "must-not-be-stored",
		})
		requireMCPStatus(t, rec, http.StatusBadRequest)
		if !strings.Contains(rec.Body.String(), "require HTTPS") {
			t.Fatalf("response = %q, want actionable HTTPS error", rec.Body.String())
		}
		cfg := loadMCPTestConfig(t, harness.configPath)
		if cfg.Tools.MCP.Servers["remote"].Auth != nil {
			t.Fatalf("rejected bearer token left auth config: %#v", cfg.Tools.MCP.Servers["remote"].Auth)
		}
	})

	t.Run("loopback HTTP is allowed", func(t *testing.T) {
		harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
			cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
				"local": {
					Enabled: true,
					Type:    "http",
					URL:     "http://127.0.0.1:9123/mcp",
				},
			}
		})

		rec := harness.request(t, http.MethodPut, "/api/mcp/servers/local/credential", map[string]any{
			"auth_type": "bearer",
			"token":     "loopback-token",
		})
		requireMCPStatus(t, rec, http.StatusOK)
	})
}

func TestMCPInsecureLegacyCredentialCanBeDisconnected(t *testing.T) {
	const credentialID = "mcp:legacy"
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"legacy": {
				Enabled: true,
				Type:    "http",
				URL:     "http://mcp.example.test/api",
				Auth: &config.MCPServerAuthConfig{
					Type:         "bearer",
					CredentialID: credentialID,
				},
			},
		}
	})
	if err := picoauth.SetCredential(credentialID, &picoauth.AuthCredential{
		AccessToken: "legacy-cleartext-token",
		Provider:    "mcp",
		AuthMethod:  "bearer",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	rec := harness.request(t, http.MethodPut, "/api/mcp/servers/legacy", map[string]any{
		"name":      "legacy",
		"enabled":   true,
		"type":      "http",
		"url":       "http://mcp.example.test/api",
		"auth_mode": "none",
	})
	requireMCPStatus(t, rec, http.StatusOK)

	cfg := loadMCPTestConfig(t, harness.configPath)
	if cfg.Tools.MCP.Servers["legacy"].Auth != nil {
		t.Fatalf("legacy auth was not disconnected: %#v", cfg.Tools.MCP.Servers["legacy"].Auth)
	}
	credential, err := picoauth.GetCredential(credentialID)
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if credential != nil {
		t.Fatalf("legacy credential was not cleaned up: %#v", credential)
	}
}

func TestMCPCustomHeadersRequireHTTPSExceptLoopback(t *testing.T) {
	harness := newMCPAPITestHarness(t, nil)
	rec := harness.request(t, http.MethodPost, "/api/mcp/servers", map[string]any{
		"name":      "cleartext",
		"enabled":   true,
		"type":      "http",
		"url":       "http://mcp.example.test/api",
		"auth_mode": "custom",
		"headers": map[string]string{
			"X-API-Key": "must-not-be-sent",
		},
		"header_keys": []string{"X-API-Key"},
	})
	requireMCPStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "custom headers require HTTPS") {
		t.Fatalf("response = %q, want custom-header HTTPS requirement", rec.Body.String())
	}
	if len(loadMCPTestConfig(t, harness.configPath).Tools.MCP.Servers) != 0 {
		t.Fatal("rejected cleartext custom-header server was persisted")
	}
}

func TestMCPSharedCredentialIsDeletedOnlyAfterLastReference(t *testing.T) {
	const sharedCredentialID = "mcp:shared"
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		sharedAuth := &config.MCPServerAuthConfig{
			Type:         "bearer",
			CredentialID: sharedCredentialID,
		}
		cfg.Tools.MCP.Enabled = true
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"one": {
				Enabled: true,
				Type:    "http",
				URL:     "https://one.example.test/mcp",
				Auth:    sharedAuth,
			},
			"two": {
				Enabled: true,
				Type:    "http",
				URL:     "https://two.example.test/mcp",
				Auth: &config.MCPServerAuthConfig{
					Type:         "bearer",
					CredentialID: sharedCredentialID,
				},
			},
			"three": {
				Enabled: true,
				Type:    "http",
				URL:     "https://three.example.test/mcp",
				Auth: &config.MCPServerAuthConfig{
					Type:         "bearer",
					CredentialID: sharedCredentialID,
				},
			},
		}
	})
	if err := picoauth.SetCredential(sharedCredentialID, &picoauth.AuthCredential{
		AccessToken: "shared-secret",
		Provider:    "mcp",
		AuthMethod:  "bearer",
	}); err != nil {
		t.Fatalf("SetCredential(shared) error = %v", err)
	}

	rec := harness.request(t, http.MethodDelete, "/api/mcp/servers/one", nil)
	requireMCPStatus(t, rec, http.StatusOK)
	if credential, err := picoauth.GetCredential(sharedCredentialID); err != nil || credential == nil {
		t.Fatalf("shared credential removed while server references it: credential=%#v err=%v", credential, err)
	}

	rec = harness.request(t, http.MethodDelete, "/api/mcp/servers/two/credential", nil)
	requireMCPStatus(t, rec, http.StatusOK)
	if credential, err := picoauth.GetCredential(sharedCredentialID); err != nil || credential == nil {
		t.Fatalf("shared credential removed while last server references it: credential=%#v err=%v", credential, err)
	}

	rec = harness.request(t, http.MethodDelete, "/api/mcp/servers/three/credential", nil)
	requireMCPStatus(t, rec, http.StatusOK)
	credential, err := picoauth.GetCredential(sharedCredentialID)
	if err != nil {
		t.Fatalf("GetCredential(shared) error = %v", err)
	}
	if credential != nil {
		t.Fatalf("shared credential remains after last reference was removed: %#v", credential)
	}
}

func TestMCPReplacingSharedCredentialForksCredentialOwnership(t *testing.T) {
	const sharedCredentialID = "mcp:shared"
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"one": {
				Enabled: true,
				Type:    "http",
				URL:     "https://one.example.test/mcp",
				Auth: &config.MCPServerAuthConfig{
					Type:         "bearer",
					CredentialID: sharedCredentialID,
				},
			},
			"two": {
				Enabled: true,
				Type:    "http",
				URL:     "https://two.example.test/mcp",
				Auth: &config.MCPServerAuthConfig{
					Type:         "bearer",
					CredentialID: sharedCredentialID,
				},
			},
		}
	})
	if err := picoauth.SetCredential(sharedCredentialID, &picoauth.AuthCredential{
		AccessToken: "original-shared-secret",
		Provider:    "mcp",
		AuthMethod:  "bearer",
	}); err != nil {
		t.Fatalf("SetCredential(shared) error = %v", err)
	}

	rec := harness.request(t, http.MethodPut, "/api/mcp/servers/one/credential", map[string]any{
		"auth_type": "bearer",
		"token":     "one-only-secret",
	})
	requireMCPStatus(t, rec, http.StatusOK)

	cfg := loadMCPTestConfig(t, harness.configPath)
	oneAuth := cfg.Tools.MCP.Servers["one"].Auth
	twoAuth := cfg.Tools.MCP.Servers["two"].Auth
	if oneAuth == nil || twoAuth == nil {
		t.Fatalf("auth references missing after replacement: one=%#v two=%#v", oneAuth, twoAuth)
	}
	if oneAuth.CredentialID == sharedCredentialID || oneAuth.CredentialID == twoAuth.CredentialID {
		t.Fatalf("replacement did not fork shared credential: one=%#v two=%#v", oneAuth, twoAuth)
	}
	oneCredential, err := picoauth.GetCredential(oneAuth.CredentialID)
	if err != nil {
		t.Fatalf("GetCredential(one) error = %v", err)
	}
	if oneCredential == nil || oneCredential.AccessToken != "one-only-secret" {
		t.Fatalf("one credential = %#v, want replacement", oneCredential)
	}
	sharedCredential, err := picoauth.GetCredential(sharedCredentialID)
	if err != nil {
		t.Fatalf("GetCredential(shared) error = %v", err)
	}
	if sharedCredential == nil || sharedCredential.AccessToken != "original-shared-secret" {
		t.Fatalf("shared credential was overwritten: %#v", sharedCredential)
	}
}

func TestMCPProbeReportsToolsAndAuthRequired(t *testing.T) {
	harness := newMCPAPITestHarness(t, func(cfg *config.Config) {
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"saved": {
				Enabled: false,
				Type:    "http",
				URL:     "https://saved.example.test/mcp",
				Auth: &config.MCPServerAuthConfig{
					Type:         "bearer",
					CredentialID: "mcp:saved",
				},
				Headers: map[string]string{
					"X-Secret": "persisted-header-secret",
				},
			},
		}
	})

	originalProbe := mcpProbeServer
	t.Cleanup(func() {
		mcpProbeServer = originalProbe
	})

	var capturedName string
	var capturedServer config.MCPServerConfig
	var capturedWorkspace string
	mcpProbeServer = func(
		ctx context.Context,
		name string,
		server config.MCPServerConfig,
		workspace string,
	) (mcpProbeResponse, error) {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			return mcpProbeResponse{}, errors.New("probe context has no deadline")
		}
		capturedName = name
		capturedServer = server
		capturedWorkspace = workspace
		return mcpProbeResponse{
			OK:        true,
			ToolCount: 2,
			Tools: []mcpProbeTool{
				{Name: "search", Description: "Search records"},
				{Name: "fetch"},
			},
		}, nil
	}

	probePayload := map[string]any{
		"name": "saved",
		"server": map[string]any{
			"name": "saved",
			"type": "http",
			"url":  "https://saved.example.test/mcp",
			"headers": map[string]string{
				"X-Secret": "",
			},
			"header_keys": []string{"X-Secret"},
		},
	}
	rec := harness.request(t, http.MethodPost, "/api/mcp/servers/test", probePayload)
	requireMCPStatus(t, rec, http.StatusOK)

	var success mcpProbeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &success); err != nil {
		t.Fatalf("json.Unmarshal(success) error = %v", err)
	}
	if !success.OK || success.ToolCount != 2 || len(success.Tools) != 2 ||
		success.Tools[0].Name != "search" {
		t.Fatalf("success response = %#v, want two discovered tools", success)
	}
	if capturedName != "saved" || !capturedServer.Enabled {
		t.Fatalf("probe arguments name=%q server=%#v, want enabled saved server", capturedName, capturedServer)
	}
	if capturedServer.Headers["X-Secret"] != "persisted-header-secret" {
		t.Fatalf("probe did not preserve configured header: %#v", capturedServer.Headers)
	}
	if capturedServer.Auth == nil || capturedServer.Auth.Type != "bearer" {
		t.Fatalf("same-origin probe did not preserve auth reference: %#v", capturedServer.Auth)
	}
	wantWorkspace := loadMCPTestConfig(t, harness.configPath).WorkspacePath()
	if capturedWorkspace != wantWorkspace {
		t.Fatalf("workspace = %q, want %q", capturedWorkspace, wantWorkspace)
	}

	probePayload["server"].(map[string]any)["url"] = "https://different.example.test/mcp"
	rec = harness.request(t, http.MethodPost, "/api/mcp/servers/test", probePayload)
	requireMCPStatus(t, rec, http.StatusOK)
	if capturedServer.Auth != nil {
		t.Fatalf("cross-origin probe retained auth reference: %#v", capturedServer.Auth)
	}
	if len(capturedServer.Headers) != 0 {
		t.Fatalf("cross-origin probe retained preserved secret headers: %#v", capturedServer.Headers)
	}

	mcpProbeServer = func(
		context.Context,
		string,
		config.MCPServerConfig,
		string,
	) (mcpProbeResponse, error) {
		return mcpProbeResponse{}, fmt.Errorf("remote returned HTTP 401 unauthorized")
	}
	rec = harness.request(t, http.MethodPost, "/api/mcp/servers/test", probePayload)
	requireMCPStatus(t, rec, http.StatusOK)

	var failure mcpProbeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
		t.Fatalf("json.Unmarshal(failure) error = %v", err)
	}
	if failure.OK || !failure.AuthRequired || failure.ToolCount != 0 ||
		!strings.Contains(failure.Error, "401") || len(failure.Tools) != 0 {
		t.Fatalf("failure response = %#v, want auth-required probe failure", failure)
	}
}
