package mcp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	picoauth "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

func setMCPAuthTestHome(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvHome, filepath.Join(t.TempDir(), ".picoclaw"))
}

func TestCredentialID(t *testing.T) {
	tests := []struct {
		name       string
		serverName string
		auth       *config.MCPServerAuthConfig
		want       string
		wantErr    bool
	}{
		{name: "derived", serverName: "github", want: "mcp:github"},
		{
			name:       "explicit bare",
			serverName: "ignored",
			auth:       &config.MCPServerAuthConfig{CredentialID: "work"},
			want:       "mcp:work",
		},
		{
			name:       "explicit qualified",
			serverName: "ignored",
			auth:       &config.MCPServerAuthConfig{CredentialID: "mcp:work"},
			want:       "mcp:work",
		},
		{
			name:       "wrong provider",
			serverName: "ignored",
			auth:       &config.MCPServerAuthConfig{CredentialID: "openai:work"},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CredentialID(tt.serverName, tt.auth)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CredentialID() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("CredentialID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCredentialIDFallbackAvoidsLegacyNameCollisions(t *testing.T) {
	upper, err := CredentialID("GitHub", nil)
	if err != nil {
		t.Fatal(err)
	}
	lower, err := CredentialID("github", nil)
	if err != nil {
		t.Fatal(err)
	}
	spaced, err := CredentialID("my server", nil)
	if err != nil {
		t.Fatal(err)
	}
	if upper == lower || upper == spaced || lower == spaced {
		t.Fatalf("derived credential IDs collide: upper=%q lower=%q spaced=%q", upper, lower, spaced)
	}
}

func TestServerHeadersWithStoredAuth(t *testing.T) {
	setMCPAuthTestHome(t)

	credentialID, credentialIDErr := CredentialID("private-server", nil)
	if credentialIDErr != nil {
		t.Fatal(credentialIDErr)
	}
	if setErr := picoauth.SetCredential(credentialID, &picoauth.AuthCredential{
		AccessToken: "stored-token",
		Provider:    "mcp",
		AuthMethod:  "bearer",
	}); setErr != nil {
		t.Fatalf("SetCredential() error = %v", setErr)
	}

	originalHeaders := map[string]string{
		"X-Client":      "picoclaw",
		"authorization": "Bearer stale-config-token",
	}
	headers, err := serverHeadersWithStoredAuth("private-server", config.MCPServerConfig{
		URL:     "https://mcp.example.test/api",
		Headers: originalHeaders,
		Auth:    &config.MCPServerAuthConfig{Type: "bearer"},
	})
	if err != nil {
		t.Fatalf("serverHeadersWithStoredAuth() error = %v", err)
	}
	if got := headers["Authorization"]; got != "Bearer stored-token" {
		t.Fatalf("Authorization = %q, want stored token", got)
	}
	if _, ok := headers["authorization"]; ok {
		t.Fatal("case-variant configured Authorization header was not removed")
	}
	if got := headers["X-Client"]; got != "picoclaw" {
		t.Fatalf("X-Client = %q, want picoclaw", got)
	}
	if got := originalHeaders["authorization"]; got != "Bearer stale-config-token" {
		t.Fatalf("input headers mutated: authorization = %q", got)
	}
}

func TestServerHeadersWithStoredAuthErrors(t *testing.T) {
	setMCPAuthTestHome(t)

	_, err := serverHeadersWithStoredAuth("missing", config.MCPServerConfig{
		URL:  "https://mcp.example.test/api",
		Auth: &config.MCPServerAuthConfig{Type: "bearer"},
	})
	if err == nil || !strings.Contains(err.Error(), "needs a configured bearer credential") {
		t.Fatalf("missing credential error = %v", err)
	}

	credentialID, credentialIDErr := CredentialID("expired", nil)
	if credentialIDErr != nil {
		t.Fatal(credentialIDErr)
	}
	if setErr := picoauth.SetCredential(credentialID, &picoauth.AuthCredential{
		AccessToken: "expired-token",
		ExpiresAt:   time.Now().Add(-time.Minute),
		Provider:    "mcp",
		AuthMethod:  "oauth",
	}); setErr != nil {
		t.Fatalf("SetCredential() error = %v", setErr)
	}
	_, err = serverHeadersWithStoredAuth("expired", config.MCPServerConfig{
		URL:  "https://mcp.example.test/api",
		Auth: &config.MCPServerAuthConfig{Type: "oauth"},
	})
	if err == nil || !strings.Contains(err.Error(), "has expired") {
		t.Fatalf("expired credential error = %v", err)
	}

	_, err = serverHeadersWithStoredAuth("unsupported", config.MCPServerConfig{
		Auth: &config.MCPServerAuthConfig{Type: "basic"},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported MCP auth type") {
		t.Fatalf("unsupported auth error = %v", err)
	}
}

func TestServerHTTPAuthRequiresHTTPSExceptLoopback(t *testing.T) {
	setMCPAuthTestHome(t)

	credentialID, err := CredentialID("remote", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := picoauth.SetCredential(credentialID, &picoauth.AuthCredential{
		AccessToken: "stored-token",
		Provider:    "mcp",
		AuthMethod:  "bearer",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	for _, test := range []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "public HTTP", rawURL: "http://mcp.example.test/api", wantErr: true},
		{name: "HTTPS", rawURL: "https://mcp.example.test/api"},
		{name: "IPv4 loopback", rawURL: "http://127.0.0.1:9123/mcp"},
		{name: "IPv6 loopback", rawURL: "http://[::1]:9123/mcp"},
		{name: "localhost", rawURL: "http://localhost:9123/mcp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, authErr := serverHTTPAuth("remote", config.MCPServerConfig{
				URL:  test.rawURL,
				Auth: &config.MCPServerAuthConfig{Type: "bearer"},
			})
			if test.wantErr {
				if authErr == nil || !strings.Contains(authErr.Error(), "require HTTPS") {
					t.Fatalf("serverHTTPAuth() error = %v, want HTTPS requirement", authErr)
				}
				return
			}
			if authErr != nil {
				t.Fatalf("serverHTTPAuth() error = %v", authErr)
			}
		})
	}
}

func TestHeaderTransportDoesNotForwardManagedHeadersAcrossOrigins(t *testing.T) {
	var leakedAuthorization string
	var leakedCustomSecret string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakedAuthorization = r.Header.Get("Authorization")
		leakedCustomSecret = r.Header.Get("X-Mcp-Secret")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusFound)
	}))
	defer source.Close()

	sourceURL, err := url.Parse(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &headerTransport{
			base:         http.DefaultTransport,
			headers:      map[string]string{"Authorization": "Bearer secret", "X-MCP-Secret": "custom-secret"},
			originScheme: sourceURL.Scheme,
			originHost:   sourceURL.Host,
		},
	}

	response, err := client.Get(source.URL)
	if err != nil {
		t.Fatalf("redirect request failed: %v", err)
	}
	response.Body.Close()

	if leakedAuthorization != "" {
		t.Fatalf("Authorization leaked across origins: %q", leakedAuthorization)
	}
	if leakedCustomSecret != "" {
		t.Fatalf("custom MCP secret leaked across origins: %q", leakedCustomSecret)
	}
}

func TestOAuthTokenSourceRefreshesAndPersistsCredential(t *testing.T) {
	setMCPAuthTestHome(t)

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := r.Form.Get("refresh_token"); got != "old-refresh" {
			t.Errorf("refresh_token = %q, want old-refresh", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "new-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	credentialID := "mcp:refreshable"
	if err := picoauth.SetCredential(credentialID, &picoauth.AuthCredential{
		AccessToken:       "old-access",
		RefreshToken:      "old-refresh",
		TokenType:         "Bearer",
		ExpiresAt:         time.Now().Add(-time.Minute),
		Provider:          "mcp",
		AuthMethod:        "oauth",
		OAuthTokenURL:     tokenServer.URL,
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		OAuthAuthStyle:    "params",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}
	credential, err := picoauth.GetCredential(credentialID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := oauthTokenSourceForCredential(credentialID, credential)
	if err != nil {
		t.Fatalf("oauthTokenSourceForCredential() error = %v", err)
	}
	token, err := source.Token()
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if token.AccessToken != "new-access" {
		t.Fatalf("AccessToken = %q, want new-access", token.AccessToken)
	}

	persisted, err := picoauth.GetCredential(credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.AccessToken != "new-access" || persisted.RefreshToken != "new-refresh" {
		t.Fatalf("refreshed credential was not persisted: %#v", persisted)
	}
	if !persisted.ExpiresAt.After(time.Now().Add(50 * time.Minute)) {
		t.Fatalf("refreshed expiry was not persisted: %v", persisted.ExpiresAt)
	}
}

func TestOAuthTokenSourceDoesNotOverwriteReplacementCredential(t *testing.T) {
	setMCPAuthTestHome(t)

	var refreshRequests atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"stale-refresh","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()

	credentialID := "mcp:replacement"
	original := &picoauth.AuthCredential{
		AccessToken:   "expired-oauth",
		RefreshToken:  "old-refresh",
		ExpiresAt:     time.Now().Add(-time.Minute),
		Provider:      "mcp",
		AuthMethod:    "oauth",
		OAuthTokenURL: tokenServer.URL,
		OAuthClientID: "old-client",
	}
	if err := picoauth.SetCredential(credentialID, original); err != nil {
		t.Fatal(err)
	}
	source, sourceErr := oauthTokenSourceForCredential(credentialID, original)
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	if setErr := picoauth.SetCredential(credentialID, &picoauth.AuthCredential{
		AccessToken: "new-bearer",
		Provider:    "mcp",
		AuthMethod:  "bearer",
	}); setErr != nil {
		t.Fatal(setErr)
	}

	if _, tokenErr := source.Token(); tokenErr == nil ||
		!strings.Contains(tokenErr.Error(), "changed") {
		t.Fatalf("Token() error = %v, want replacement detection", tokenErr)
	}
	if refreshRequests.Load() != 0 {
		t.Fatalf("stale OAuth source made %d refresh requests", refreshRequests.Load())
	}
	persisted, err := picoauth.GetCredential(credentialID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || persisted.AccessToken != "new-bearer" || persisted.AuthMethod != "bearer" {
		t.Fatalf("replacement credential was overwritten: %#v", persisted)
	}
}

func TestSharedOAuthCredentialRefreshIsSerialized(t *testing.T) {
	setMCPAuthTestHome(t)

	var refreshRequests atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshRequests.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"access_token":"shared-new-access","refresh_token":"rotated-refresh",` +
				`"token_type":"Bearer","expires_in":3600}`,
		))
	}))
	defer tokenServer.Close()

	credentialID := "mcp:shared-oauth"
	credential := &picoauth.AuthCredential{
		AccessToken:   "shared-expired",
		RefreshToken:  "shared-refresh",
		ExpiresAt:     time.Now().Add(-time.Minute),
		Provider:      "mcp",
		AuthMethod:    "oauth",
		OAuthTokenURL: tokenServer.URL,
		OAuthClientID: "shared-client",
	}
	if err := picoauth.SetCredential(credentialID, credential); err != nil {
		t.Fatal(err)
	}
	first, err := oauthTokenSourceForCredential(credentialID, credential)
	if err != nil {
		t.Fatal(err)
	}
	second, err := oauthTokenSourceForCredential(credentialID, credential)
	if err != nil {
		t.Fatal(err)
	}

	sources := []oauth2.TokenSource{first, second}
	tokens := make([]*oauth2.Token, len(sources))
	errs := make([]error, len(sources))
	var wait sync.WaitGroup
	for index, source := range sources {
		wait.Add(1)
		go func() {
			defer wait.Done()
			tokens[index], errs[index] = source.Token()
		}()
	}
	wait.Wait()
	for index, token := range tokens {
		if errs[index] != nil {
			t.Fatalf("source %d Token() error = %v", index, errs[index])
		}
		if token == nil || token.AccessToken != "shared-new-access" {
			t.Fatalf("source %d token = %#v", index, token)
		}
	}
	if refreshRequests.Load() != 1 {
		t.Fatalf("refresh request count = %d, want 1", refreshRequests.Load())
	}
}

func TestOAuthRefreshTransportPinsValidatedAddress(t *testing.T) {
	var lookups atomic.Int32
	var dialed string
	transport := &oauthRefreshPinnedTransport{
		configuredHost: "identity.example.test",
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			if lookups.Add(1) == 1 {
				return []net.IPAddr{{IP: net.ParseIP("8.8.4.4")}}, nil
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
	if _, err := transport.resolveAndPin(context.Background()); err != nil {
		t.Fatal(err)
	}
	connection, err := transport.dialContext(
		context.Background(),
		"tcp",
		"identity.example.test:443",
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if lookups.Load() != 1 || dialed != "8.8.4.4:443" {
		t.Fatalf("lookups=%d dialed=%q, want pinned public address", lookups.Load(), dialed)
	}
}

func TestOAuthRefreshTransportPrivateDNSPolicy(t *testing.T) {
	local := &oauthRefreshPinnedTransport{
		configuredHost: "identity.internal.",
		allowPrivate:   explicitLocalOAuthHost("identity.internal."),
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("10.0.0.4")}}, nil
		},
	}
	if _, err := local.resolveAndPin(context.Background()); err != nil {
		t.Fatalf("intentional private DNS endpoint was rejected: %v", err)
	}

	public := &oauthRefreshPinnedTransport{
		configuredHost: "identity.example.com",
		allowPrivate:   explicitLocalOAuthHost("identity.example.com"),
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("10.0.0.4")}}, nil
		},
	}
	if _, err := public.resolveAndPin(context.Background()); err == nil {
		t.Fatal("public OAuth hostname resolving private was allowed")
	}
}
