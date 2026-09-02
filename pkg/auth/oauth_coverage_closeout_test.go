package auth

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type oauthErrorReader struct{ err error }

func (reader oauthErrorReader) Read([]byte) (int, error) { return 0, reader.err }

func TestCoverageOAuthConfigurationCallbackAndListener(t *testing.T) {
	google := GoogleAntigravityOAuthConfig()
	if google.ClientID == "" || google.ClientSecret == "" || google.TokenURL == "" {
		t.Fatalf("Google OAuth config = %#v", google)
	}
	if got := decodeBase64("not-base64"); got != "not-base64" {
		t.Fatalf("invalid base64 fallback = %q", got)
	}
	if state, err := GenerateState(); err != nil || len(state) != 64 {
		t.Fatalf("generated state = %q, %v", state, err)
	}
	if got := oauthCallbackRedirectURI(1234); got != "http://localhost:1234/auth/callback" {
		t.Fatalf("callback URI = %q", got)
	}

	for _, test := range []struct {
		name       string
		query      string
		wantStatus int
		wantCode   string
		wantError  bool
	}{
		{name: "state mismatch", query: "?state=wrong&code=code", wantStatus: 400, wantError: true},
		{name: "provider error", query: "?state=state&error=denied", wantStatus: 400, wantError: true},
		{name: "success", query: "?state=state&code=ready", wantStatus: 200, wantCode: "ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			results := make(chan callbackResult, 1)
			request := httptest.NewRequest(http.MethodGet, "/auth/callback"+test.query, nil)
			recorder := httptest.NewRecorder()
			oauthCallbackHandler("state", results).ServeHTTP(recorder, request)
			result := <-results
			if recorder.Code != test.wantStatus || result.code != test.wantCode ||
				(result.err != nil) != test.wantError {
				t.Fatalf("callback = status:%d result:%#v", recorder.Code, result)
			}
		})
	}
	listener, port, err := listenOAuthCallback(0)
	if err != nil || port <= 0 {
		t.Fatalf("callback listener = %#v, %d, %v", listener, port, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageOAuthDeviceCodeProtocol(t *testing.T) {
	var tokenPolls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"device_auth_id":"device","user_code":"USER","interval":0}`)
		case "/api/accounts/deviceauth/token":
			tokenPolls.Add(1)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"authorization_code":"code","code_verifier":"verifier"}`)
		case "/oauth/token":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"access_token":"access","expires_in":60}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	cfg := OAuthProviderConfig{Issuer: server.URL, ClientID: "client"}
	info, err := RequestDeviceCode(cfg)
	if err != nil || info.Interval != 5 || info.DeviceAuthID != "device" ||
		info.VerifyURL != server.URL+"/codex/device" {
		t.Fatalf("device code = %#v, %v", info, err)
	}
	credential, err := PollDeviceCodeOnce(cfg, "device", "USER")
	if err != nil || credential == nil || credential.AccessToken != "access" || tokenPolls.Load() != 1 {
		t.Fatalf("device poll = %#v, %v, polls=%d", credential, err, tokenPolls.Load())
	}

	for _, body := range []string{
		`{`,
		`{"device_auth_id":"device","user_code":"USER","interval":{}}`,
	} {
		if _, err := parseDeviceCodeResponse([]byte(body)); err == nil {
			t.Fatalf("invalid device response %q accepted", body)
		}
	}
	for raw, want := range map[string]int{"": 0, "null": 0, `" "`: 0, `"7"`: 7, "9": 9} {
		if got, err := parseFlexibleInt([]byte(raw)); err != nil || got != want {
			t.Fatalf("flexible int %q = %d, %v", raw, got, err)
		}
	}
}

func TestCoverageOAuthDeviceAndExchangeFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		body string
		code int
		run  func(OAuthProviderConfig) error
	}{
		{
			name: "device status", path: "/api/accounts/deviceauth/usercode", body: "denied", code: 403,
			run: func(cfg OAuthProviderConfig) error { _, err := RequestDeviceCode(cfg); return err },
		},
		{
			name: "device malformed", path: "/api/accounts/deviceauth/usercode", body: "{", code: 200,
			run: func(cfg OAuthProviderConfig) error { _, err := RequestDeviceCode(cfg); return err },
		},
		{
			name: "poll pending", path: "/api/accounts/deviceauth/token", body: "pending", code: 409,
			run: func(cfg OAuthProviderConfig) error {
				credential, err := PollDeviceCodeOnce(cfg, "device", "user")
				if credential != nil {
					return errors.New("pending poll returned credential")
				}
				return err
			},
		},
		{
			name: "poll malformed", path: "/api/accounts/deviceauth/token", body: "{", code: 200,
			run: func(cfg OAuthProviderConfig) error {
				_, err := PollDeviceCodeOnce(cfg, "device", "user")
				return err
			},
		},
		{
			name: "exchange status", path: "/oauth/token", body: "denied", code: 403,
			run: func(cfg OAuthProviderConfig) error {
				_, err := ExchangeCodeForTokens(cfg, "code", "verifier", "redirect")
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != test.path {
					http.NotFound(writer, request)
					return
				}
				writer.WriteHeader(test.code)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			if err := test.run(OAuthProviderConfig{Issuer: server.URL, ClientID: "client"}); err == nil {
				t.Fatal("OAuth failure path succeeded")
			}
		})
	}
	invalid := OAuthProviderConfig{Issuer: "://invalid", ClientID: "client"}
	if _, err := RequestDeviceCode(invalid); err == nil {
		t.Fatal("invalid device issuer succeeded")
	}
	if _, err := PollDeviceCodeOnce(invalid, "device", "user"); err == nil {
		t.Fatal("invalid poll issuer succeeded")
	}
	if _, err := ExchangeCodeForTokens(invalid, "code", "verifier", "redirect"); err == nil {
		t.Fatal("invalid token issuer succeeded")
	}
}

func TestCoverageOAuthJWTAndTokenInputEdges(t *testing.T) {
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{
		"https://api.openai.com/profile":{"email":" nested@example.com "},
		"https://api.openai.com/auth":{"chatgpt_account_id":"nested-account"}
	}`))
	token := "header." + claims + ".signature"
	if got := extractEmail(token); got != "nested@example.com" {
		t.Fatalf("nested email = %q", got)
	}
	if got := extractAccountID(token); got != "nested-account" {
		t.Fatalf("nested account = %q", got)
	}
	for _, token := range []string{"not-jwt", "header.%%%.signature", "header.e2JhZH0.signature"} {
		if _, err := parseJWTClaims(token); err == nil {
			t.Fatalf("invalid JWT %q accepted", token)
		}
	}
	for _, provider := range []string{"anthropic", "openai", "github-copilot", "custom"} {
		if providerDisplayName(provider) == "" {
			t.Fatalf("provider display %q empty", provider)
		}
	}
	canary := errors.New("reader canary")
	if _, err := LoginPasteToken("openai", oauthErrorReader{canary}); !errors.Is(err, canary) {
		t.Fatalf("paste reader error = %v", err)
	}
	if _, err := LoginPasteToken("openai", strings.NewReader("\n")); err == nil {
		t.Fatal("empty pasted token accepted")
	}
	if _, err := LoginSetupToken(oauthErrorReader{canary}); !errors.Is(err, canary) {
		t.Fatalf("setup reader error = %v", err)
	}
	for _, input := range []string{"", "wrong", "sk-ant-oat01-short"} {
		if _, err := LoginSetupToken(strings.NewReader(input + "\n")); err == nil {
			t.Fatalf("invalid setup token %q accepted", input)
		}
	}
	valid := "sk-ant-oat01-" + strings.Repeat("a", 80)
	credential, err := LoginSetupToken(strings.NewReader(valid + "\n"))
	if err != nil || credential.Provider != "anthropic" || credential.AuthMethod != "oauth" {
		t.Fatalf("setup token = %#v, %v", credential, err)
	}
}
