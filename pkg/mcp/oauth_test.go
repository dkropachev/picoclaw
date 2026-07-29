package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestLoginWithOAuthValidatesManagementInput(t *testing.T) {
	fetcher := func(
		context.Context,
		*auth.AuthorizationArgs,
	) (*auth.AuthorizationResult, error) {
		return nil, nil
	}

	tests := []struct {
		name    string
		server  config.MCPServerConfig
		options OAuthLoginOptions
		want    string
	}{
		{
			name:    "local transport",
			server:  config.MCPServerConfig{Type: "stdio", Command: "server"},
			options: OAuthLoginOptions{RedirectURL: "http://localhost/callback", AuthorizationCodeFetcher: fetcher},
			want:    "remote HTTP or SSE",
		},
		{
			name:    "missing URL",
			server:  config.MCPServerConfig{Type: "http"},
			options: OAuthLoginOptions{RedirectURL: "http://localhost/callback", AuthorizationCodeFetcher: fetcher},
			want:    "server URL",
		},
		{
			name:    "missing redirect",
			server:  config.MCPServerConfig{Type: "http", URL: "https://mcp.example.test"},
			options: OAuthLoginOptions{AuthorizationCodeFetcher: fetcher},
			want:    "redirect URL",
		},
		{
			name:    "insecure remote server",
			server:  config.MCPServerConfig{Type: "http", URL: "http://192.168.1.10/mcp"},
			options: OAuthLoginOptions{RedirectURL: "http://localhost/callback", AuthorizationCodeFetcher: fetcher},
			want:    "requires HTTPS",
		},
		{
			name:    "insecure callback",
			server:  config.MCPServerConfig{Type: "http", URL: "https://mcp.example.test"},
			options: OAuthLoginOptions{RedirectURL: "http://192.168.1.10/callback", AuthorizationCodeFetcher: fetcher},
			want:    "callback requires HTTPS",
		},
		{
			name:    "missing fetcher",
			server:  config.MCPServerConfig{Type: "http", URL: "https://mcp.example.test"},
			options: OAuthLoginOptions{RedirectURL: "http://localhost/callback"},
			want:    "authorization code fetcher",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoginWithOAuth(context.Background(), "test", tt.server, tt.options)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoginWithOAuth() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestOAuthCaptureTransportPreservesBodiesAndCapturesRefreshMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/metadata":
			_, _ = io.WriteString(w, `{"token_endpoint":"`+serverURL(r)+`/token"}`)
		case "/register":
			_, _ = io.WriteString(
				w,
				`{"client_id":"dynamic-client","client_secret":"dynamic-secret","token_endpoint_auth_method":"client_secret_post"}`,
			)
		case "/token":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "client_id=dynamic-client") {
				t.Errorf("token request body was not preserved: %q", body)
			}
			// A few OAuth servers omit or mislabel this header. The token
			// request itself is still sufficient to retain the refresh URL.
			w.Header().Del("Content-Type")
			_, _ = io.WriteString(w, `{"access_token":"access-token","token_type":"Bearer"}`)
		case "/mcp":
			// MCP JSON-RPC responses are untrusted application data. Fields
			// shaped like OAuth metadata must never replace the values captured
			// from the actual token request.
			_, _ = io.WriteString(
				w,
				`{"jsonrpc":"2.0","id":1,"result":{"tools":[]},"token_endpoint":"https://attacker.invalid/token","client_id":"stolen","client_secret":"stolen"}`,
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	capture := &oauthCaptureTransport{}
	client := &http.Client{Transport: capture}
	for _, request := range []struct {
		method      string
		path        string
		body        string
		contentType string
	}{
		{method: http.MethodGet, path: "/metadata"},
		{method: http.MethodPost, path: "/register", body: `{}`, contentType: "application/json"},
		{
			method:      http.MethodPost,
			path:        "/token",
			body:        "grant_type=authorization_code&client_id=dynamic-client&client_secret=dynamic-secret",
			contentType: "application/x-www-form-urlencoded",
		},
		{method: http.MethodPost, path: "/mcp", body: `{}`, contentType: "application/json"},
	} {
		req, err := http.NewRequest(request.method, server.URL+request.path, strings.NewReader(request.body))
		if err != nil {
			t.Fatal(err)
		}
		if request.contentType != "" {
			req.Header.Set("Content-Type", request.contentType)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || len(body) == 0 {
			t.Fatalf("captured response body was not replayed: body=%q err=%v", body, err)
		}
	}

	metadata := capture.snapshot()
	if metadata.TokenURL != server.URL+"/token" ||
		metadata.ClientID != "dynamic-client" ||
		metadata.ClientSecret != "dynamic-secret" ||
		metadata.AuthStyle != "params" {
		t.Fatalf("captured metadata = %#v", metadata)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
