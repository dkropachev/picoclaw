package mcp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"github.com/sipeed/picoclaw/pkg/config"
)

// OAuthLoginOptions configures an interactive authorization-code login.
type OAuthLoginOptions struct {
	RedirectURL              string
	AuthorizationCodeFetcher mcpauth.AuthorizationCodeFetcher
	HTTPClient               *http.Client
}

// OAuthLoginResult is the credential and live probe result produced by a login.
type OAuthLoginResult struct {
	Token           *oauth2.Token
	RefreshMetadata OAuthRefreshMetadata
	ToolCount       int
	Tools           []string
}

// OAuthRefreshMetadata is the non-token OAuth client state required to use a
// refresh token after the launcher or gateway restarts.
type OAuthRefreshMetadata struct {
	TokenURL     string
	ClientID     string
	ClientSecret string
	AuthStyle    string
}

// LoginWithOAuth runs MCP protected-resource discovery, dynamic client
// registration, PKCE authorization, token exchange, and a live MCP probe.
// Persisting the returned token remains the caller's responsibility.
func LoginWithOAuth(
	ctx context.Context,
	serverName string,
	serverConfig config.MCPServerConfig,
	options OAuthLoginOptions,
) (*OAuthLoginResult, error) {
	transportType := config.EffectiveMCPTransportType(serverConfig)
	if transportType != "http" && transportType != "sse" {
		return nil, fmt.Errorf("OAuth login requires a remote HTTP or SSE MCP server")
	}
	if strings.TrimSpace(serverConfig.URL) == "" {
		return nil, fmt.Errorf("OAuth login requires an MCP server URL")
	}
	if !isHTTPSOrLoopbackHTTP(serverConfig.URL) {
		return nil, fmt.Errorf("MCP OAuth requires HTTPS, except for loopback development servers")
	}
	if strings.TrimSpace(options.RedirectURL) == "" {
		return nil, fmt.Errorf("OAuth login requires a redirect URL")
	}
	if !isHTTPSOrLoopbackHTTP(options.RedirectURL) {
		return nil, fmt.Errorf("MCP OAuth callback requires HTTPS, except on loopback")
	}
	if options.AuthorizationCodeFetcher == nil {
		return nil, fmt.Errorf("OAuth login requires an authorization code fetcher")
	}

	oauthClient := &http.Client{}
	if options.HTTPClient != nil {
		*oauthClient = *options.HTTPClient
	}
	capture := &oauthCaptureTransport{base: oauthClient.Transport}
	oauthClient.Transport = capture

	handler, err := mcpauth.NewAuthorizationCodeHandler(&mcpauth.AuthorizationCodeHandlerConfig{
		RedirectURL:              options.RedirectURL,
		AuthorizationCodeFetcher: options.AuthorizationCodeFetcher,
		Client:                   oauthClient,
		DynamicClientRegistrationConfig: &mcpauth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				RedirectURIs:            []string{options.RedirectURL},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
				ClientName:              "PicoClaw MCP Dashboard",
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create MCP OAuth handler: %w", err)
	}

	connection, err := ConnectServerWithOAuth(ctx, serverName, serverConfig, handler, oauthClient)
	if err != nil {
		return nil, err
	}
	defer connection.Session.Close()

	tokenSource, err := handler.TokenSource(ctx)
	if err != nil {
		return nil, fmt.Errorf("read MCP OAuth token source: %w", err)
	}
	if tokenSource == nil {
		return nil, fmt.Errorf("MCP server did not request OAuth authorization")
	}
	token, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("read MCP OAuth token: %w", err)
	}
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return nil, fmt.Errorf("MCP OAuth completed without an access token")
	}

	toolNames := make([]string, 0, len(connection.Tools))
	for _, tool := range connection.Tools {
		if tool != nil {
			toolNames = append(toolNames, tool.Name)
		}
	}
	return &OAuthLoginResult{
		Token:           token,
		RefreshMetadata: capture.snapshot(),
		ToolCount:       len(connection.Tools),
		Tools:           toolNames,
	}, nil
}

type oauthCaptureTransport struct {
	base http.RoundTripper

	mu           sync.Mutex
	tokenURL     string
	clientID     string
	clientSecret string
	authStyle    string
}

func (t *oauthCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.captureRequest(req)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func (t *oauthCaptureTransport) captureRequest(req *http.Request) {
	_, _, usedBasicAuth := req.BasicAuth()
	if username, password, ok := req.BasicAuth(); ok {
		t.mu.Lock()
		t.clientID = username
		t.clientSecret = password
		t.authStyle = "header"
		t.mu.Unlock()
	}

	mediaType, _, _ := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if mediaType != "application/x-www-form-urlencoded" || req.Body == nil {
		return
	}
	const captureLimit = 1 << 20
	originalBody := req.Body
	prefix, err := io.ReadAll(io.LimitReader(originalBody, captureLimit+1))
	req.Body = &replayedReadCloser{
		Reader: io.MultiReader(bytes.NewReader(prefix), originalBody),
		Closer: originalBody,
	}
	if err != nil || len(prefix) > captureLimit {
		return
	}
	form, err := url.ParseQuery(string(prefix))
	if err != nil {
		return
	}
	t.mu.Lock()
	if clientID := strings.TrimSpace(form.Get("client_id")); clientID != "" {
		t.clientID = clientID
	}
	if clientSecret := form.Get("client_secret"); clientSecret != "" {
		t.clientSecret = clientSecret
	}
	if form.Has("client_id") && !usedBasicAuth {
		t.authStyle = "params"
	}
	switch strings.TrimSpace(form.Get("grant_type")) {
	case "authorization_code", "refresh_token":
		t.tokenURL = req.URL.String()
	}
	t.mu.Unlock()
}

func (t *oauthCaptureTransport) snapshot() OAuthRefreshMetadata {
	t.mu.Lock()
	defer t.mu.Unlock()
	return OAuthRefreshMetadata{
		TokenURL:     t.tokenURL,
		ClientID:     t.clientID,
		ClientSecret: t.clientSecret,
		AuthStyle:    t.authStyle,
	}
}

type replayedReadCloser struct {
	io.Reader
	io.Closer
}

func isHTTPSOrLoopbackHTTP(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return false
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return true
	}
	if !strings.EqualFold(parsed.Scheme, "http") {
		return false
	}
	hostname := strings.TrimSpace(parsed.Hostname())
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

// IsHTTPSOrLoopbackHTTP reports whether a URL is safe for transmitting MCP
// credentials: HTTPS everywhere, with plain HTTP allowed only on loopback.
func IsHTTPSOrLoopbackHTTP(rawURL string) bool {
	return isHTTPSOrLoopbackHTTP(rawURL)
}
