package mcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	picoauth "github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

const mcpCredentialProvider = "mcp"

// CredentialID returns the auth-store key used by an MCP server.
func CredentialID(serverName string, authConfig *config.MCPServerAuthConfig) (string, error) {
	rawID := ""
	if authConfig != nil {
		rawID = strings.TrimSpace(authConfig.CredentialID)
	}
	if rawID == "" {
		serverName = strings.TrimSpace(serverName)
		if serverName == strings.ToLower(serverName) {
			if credentialID, err := picoauth.NormalizeCredentialID(mcpCredentialProvider, serverName); err == nil {
				return credentialID, nil
			}
		}
		digest := sha256.Sum256([]byte(serverName))
		rawID = fmt.Sprintf("server-%x", digest[:12])
	}
	return picoauth.NormalizeCredentialID(mcpCredentialProvider, rawID)
}

func serverHeadersWithStoredAuth(
	serverName string,
	cfg config.MCPServerConfig,
) (map[string]string, error) {
	headers, tokenSource, err := serverHTTPAuth(serverName, cfg)
	if err != nil {
		return nil, err
	}
	if tokenSource != nil {
		token, err := tokenSource.Token()
		if err != nil {
			return nil, err
		}
		headers["Authorization"] = "Bearer " + token.AccessToken
	}
	return headers, nil
}

func serverHTTPAuth(
	serverName string,
	cfg config.MCPServerConfig,
) (map[string]string, oauth2.TokenSource, error) {
	headers := cloneStringMap(cfg.Headers)

	if cfg.Auth == nil {
		return headers, nil, nil
	}

	authType := strings.ToLower(strings.TrimSpace(cfg.Auth.Type))
	switch authType {
	case "", "none":
		return headers, nil, nil
	case "bearer", "oauth":
	default:
		return nil, nil, fmt.Errorf("unsupported MCP auth type %q", cfg.Auth.Type)
	}
	if !isHTTPSOrLoopbackHTTP(cfg.URL) {
		return nil, nil, fmt.Errorf(
			"MCP %s credentials require HTTPS, except for loopback development servers",
			authType,
		)
	}

	credentialID, err := CredentialID(serverName, cfg.Auth)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid MCP credential reference: %w", err)
	}
	credential, err := picoauth.GetCredential(credentialID)
	if err != nil {
		return nil, nil, fmt.Errorf("load MCP credential %q: %w", credentialID, err)
	}
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return nil, nil, fmt.Errorf("MCP server %q needs a configured %s credential", serverName, authType)
	}

	for key := range headers {
		if strings.EqualFold(key, "Authorization") {
			delete(headers, key)
		}
	}

	tokenSource, err := storedMCPTokenSourceForCredential(credentialID, authType, credential)
	if err != nil {
		return nil, nil, fmt.Errorf("MCP %s credential for server %q: %w", authType, serverName, err)
	}
	return headers, tokenSource, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values)+1)
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func oauthTokenSourceForCredential(
	credentialID string,
	credential *picoauth.AuthCredential,
) (oauth2.TokenSource, error) {
	return storedMCPTokenSourceForCredential(credentialID, "oauth", credential)
}

func storedMCPTokenSourceForCredential(
	credentialID string,
	authType string,
	credential *picoauth.AuthCredential,
) (oauth2.TokenSource, error) {
	if err := validateStoredMCPCredential(credentialID, authType, credential); err != nil {
		return nil, err
	}
	if authType == "oauth" && hasOAuthRefreshMetadata(credential) &&
		!isHTTPSOrLoopbackHTTP(credential.OAuthTokenURL) {
		return nil, fmt.Errorf("has an insecure OAuth token endpoint")
	}
	if authType == "oauth" && credential.IsExpired() && !hasOAuthRefreshMetadata(credential) {
		return nil, fmt.Errorf("has expired and cannot be refreshed; log in again")
	}
	if authType == "bearer" && credential.IsExpired() {
		return nil, fmt.Errorf("has expired; replace it in the dashboard")
	}
	return &storedMCPTokenSource{credentialID: credentialID, authType: authType}, nil
}

type storedMCPTokenSource struct {
	credentialID string
	authType     string
}

func (s *storedMCPTokenSource) Token() (*oauth2.Token, error) {
	if s.authType == "oauth" {
		return (&persistingOAuthTokenSource{credentialID: s.credentialID}).Token()
	}
	credential, err := picoauth.GetCredential(s.credentialID)
	if err != nil {
		return nil, fmt.Errorf("load MCP credential %q: %w", s.credentialID, err)
	}
	if err := validateStoredMCPCredential(s.credentialID, s.authType, credential); err != nil {
		return nil, err
	}
	if credential.IsExpired() {
		return nil, fmt.Errorf("MCP %s credential %q has expired", s.authType, s.credentialID)
	}
	return oauthTokenFromCredential(credential), nil
}

func validateStoredMCPCredential(
	credentialID string,
	authType string,
	credential *picoauth.AuthCredential,
) error {
	if credential == nil {
		return fmt.Errorf("MCP credential %q was removed", credentialID)
	}
	if credential.Provider != mcpCredentialProvider {
		return fmt.Errorf(
			"MCP credential %q changed provider; now belongs to provider %q",
			credentialID,
			credential.Provider,
		)
	}
	method := strings.ToLower(strings.TrimSpace(credential.AuthMethod))
	if method != authType {
		return fmt.Errorf(
			"MCP credential %q changed auth; uses %q auth, want %q",
			credentialID,
			method,
			authType,
		)
	}
	if strings.TrimSpace(credential.AccessToken) == "" {
		return fmt.Errorf("MCP credential %q has no access token", credentialID)
	}
	return nil
}

func hasOAuthRefreshMetadata(credential *picoauth.AuthCredential) bool {
	return credential != nil &&
		strings.TrimSpace(credential.RefreshToken) != "" &&
		strings.TrimSpace(credential.OAuthTokenURL) != "" &&
		strings.TrimSpace(credential.OAuthClientID) != ""
}

func oauthTokenFromCredential(credential *picoauth.AuthCredential) *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  credential.AccessToken,
		RefreshToken: credential.RefreshToken,
		TokenType:    credential.TokenType,
		Expiry:       credential.ExpiresAt,
	}
}

type persistingOAuthTokenSource struct {
	credentialID string
}

func (s *persistingOAuthTokenSource) Token() (*oauth2.Token, error) {
	authoritative, err := picoauth.RefreshCredential(
		s.credentialID,
		func(credential *picoauth.AuthCredential) bool {
			if credential == nil {
				return false
			}
			token := &oauth2.Token{
				AccessToken: credential.AccessToken,
				Expiry:      credential.ExpiresAt,
			}
			return !token.Valid()
		},
		func(credential *picoauth.AuthCredential) (*picoauth.AuthCredential, error) {
			if err := validateStoredMCPCredential(s.credentialID, "oauth", credential); err != nil {
				return nil, err
			}
			token := oauthTokenFromCredential(credential)
			if !hasOAuthRefreshMetadata(credential) {
				return nil, fmt.Errorf("OAuth access token has expired; log in again")
			}
			if !isHTTPSOrLoopbackHTTP(credential.OAuthTokenURL) {
				return nil, fmt.Errorf("has an insecure OAuth token endpoint")
			}

			authStyle := oauth2.AuthStyleAutoDetect
			switch strings.ToLower(strings.TrimSpace(credential.OAuthAuthStyle)) {
			case "header":
				authStyle = oauth2.AuthStyleInHeader
			case "params":
				authStyle = oauth2.AuthStyleInParams
			}
			oauthConfig := &oauth2.Config{
				ClientID:     credential.OAuthClientID,
				ClientSecret: credential.OAuthClientSecret,
				Endpoint: oauth2.Endpoint{
					TokenURL:  credential.OAuthTokenURL,
					AuthStyle: authStyle,
				},
			}
			refreshClient, err := newOAuthRefreshClient(credential.OAuthTokenURL)
			if err != nil {
				return nil, err
			}
			refreshContext := context.WithValue(context.Background(), oauth2.HTTPClient, refreshClient)
			refreshed, err := oauthConfig.TokenSource(refreshContext, token).Token()
			if err != nil {
				return nil, err
			}
			if refreshed == nil || strings.TrimSpace(refreshed.AccessToken) == "" {
				return nil, fmt.Errorf("OAuth refresh returned an empty access token")
			}

			updated := *credential
			updated.AccessToken = refreshed.AccessToken
			updated.TokenType = refreshed.TokenType
			updated.ExpiresAt = refreshed.Expiry
			if refreshed.RefreshToken != "" {
				updated.RefreshToken = refreshed.RefreshToken
			}
			return &updated, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("refresh MCP OAuth credential: %w", err)
	}
	if authoritative == nil {
		return nil, fmt.Errorf("MCP credential %q was removed", s.credentialID)
	}
	if err := validateStoredMCPCredential(s.credentialID, "oauth", authoritative); err != nil {
		return nil, err
	}
	if hasOAuthRefreshMetadata(authoritative) &&
		!isHTTPSOrLoopbackHTTP(authoritative.OAuthTokenURL) {
		return nil, fmt.Errorf("MCP credential %q has an insecure OAuth token endpoint", s.credentialID)
	}
	resolved := oauthTokenFromCredential(authoritative)
	if !resolved.Valid() {
		return nil, fmt.Errorf("MCP OAuth credential did not return a token")
	}
	return resolved, nil
}

func newOAuthRefreshClient(tokenURL string) (*http.Client, error) {
	origin, err := url.Parse(tokenURL)
	if err != nil || origin.Scheme == "" || origin.Host == "" {
		return nil, fmt.Errorf("has an invalid OAuth token endpoint")
	}
	guard := &oauthRefreshPinnedTransport{
		configuredHost: strings.ToLower(strings.TrimSpace(origin.Hostname())),
		allowPrivate:   explicitLocalOAuthHost(origin.Hostname()),
	}
	var transport *http.Transport
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = defaultTransport.Clone()
	} else {
		transport = &http.Transport{}
	}
	transport.Proxy = nil
	transport.DialContext = guard.dialContext
	transport.ResponseHeaderTimeout = 30 * time.Second
	guard.base = transport
	return &http.Client{
		Transport: guard,
		Timeout:   30 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many OAuth token endpoint redirects")
			}
			if !isHTTPSOrLoopbackHTTP(request.URL.String()) {
				return fmt.Errorf("OAuth token endpoint redirected to insecure HTTP")
			}
			if !strings.EqualFold(request.URL.Scheme, origin.Scheme) ||
				!strings.EqualFold(request.URL.Host, origin.Host) {
				return fmt.Errorf("OAuth token endpoint redirected across origins")
			}
			return nil
		},
	}, nil
}

type oauthRefreshPinnedTransport struct {
	base           http.RoundTripper
	configuredHost string
	allowPrivate   bool

	mu       sync.Mutex
	pinned   []net.IP
	lookupIP func(context.Context, string) ([]net.IPAddr, error)
	dial     func(context.Context, string, string) (net.Conn, error)
}

func (t *oauthRefreshPinnedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil ||
		!strings.EqualFold(request.URL.Hostname(), t.configuredHost) {
		return nil, fmt.Errorf("OAuth token request target changed origins")
	}
	if !isHTTPSOrLoopbackHTTP(request.URL.String()) {
		return nil, fmt.Errorf("OAuth token endpoint must use HTTPS")
	}
	if _, err := t.resolveAndPin(request.Context()); err != nil {
		return nil, err
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(request)
}

func (t *oauthRefreshPinnedTransport) resolveAndPin(ctx context.Context) ([]net.IP, error) {
	t.mu.Lock()
	if len(t.pinned) > 0 {
		pinned := cloneOAuthIPs(t.pinned)
		t.mu.Unlock()
		return pinned, nil
	}
	t.mu.Unlock()

	lookup := t.lookupIP
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	addresses, err := lookup(ctx, t.configuredHost)
	if err != nil {
		return nil, fmt.Errorf("resolve OAuth token endpoint %q: %w", t.configuredHost, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("OAuth token endpoint %q did not resolve", t.configuredHost)
	}
	approved := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ip := address.IP
		switch {
		case ip.IsUnspecified(), ip.IsMulticast(), ip.IsLinkLocalMulticast(), ip.IsLinkLocalUnicast():
			return nil, fmt.Errorf("OAuth token endpoint %q resolves to a blocked address", t.configuredHost)
		case IsPrivateOrSpecialIP(ip) && !t.allowPrivate:
			return nil, fmt.Errorf("OAuth token endpoint %q resolves to a private network", t.configuredHost)
		}
		approved = append(approved, append(net.IP(nil), ip...))
	}
	t.mu.Lock()
	if len(t.pinned) == 0 {
		t.pinned = cloneOAuthIPs(approved)
	} else {
		approved = cloneOAuthIPs(t.pinned)
	}
	t.mu.Unlock()
	return approved, nil
}

func (t *oauthRefreshPinnedTransport) dialContext(
	ctx context.Context,
	network, address string,
) (net.Conn, error) {
	hostname, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("parse OAuth token dial target %q: %w", address, err)
	}
	if !strings.EqualFold(strings.Trim(hostname, "[]"), t.configuredHost) {
		return nil, fmt.Errorf("OAuth token dial target changed origins")
	}
	approved, err := t.resolveAndPin(ctx)
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
		lastErr = fmt.Errorf("OAuth token endpoint has no approved addresses")
	}
	return nil, lastErr
}

func explicitLocalOAuthHost(hostname string) bool {
	return IsExplicitLocalHostname(hostname)
}

func cloneOAuthIPs(values []net.IP) []net.IP {
	cloned := make([]net.IP, 0, len(values))
	for _, value := range values {
		cloned = append(cloned, append(net.IP(nil), value...))
	}
	return cloned
}
