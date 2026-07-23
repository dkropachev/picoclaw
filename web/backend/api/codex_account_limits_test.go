package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/auth"
)

func TestHandleCodexAccountLimitsUsesUsageAPI(t *testing.T) {
	withPicoclawAuthHome(t)
	setOpenAIAuthCredential(
		t,
		"openai",
		"default-token",
		"",
		"acc-default",
		"default@example.com",
	)
	setOpenAIAuthCredential(
		t,
		"openai:work",
		"work-token",
		"",
		"acc-work",
		"work@example.com",
	)
	if err := auth.SetCredential("anthropic", &auth.AuthCredential{
		AccessToken: "anthropic-token",
		Provider:    "anthropic",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential(anthropic) error: %v", err)
	}

	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.URL.Path != "/api/codex/usage" {
			t.Errorf("path = %q, want /api/codex/usage", r.URL.Path)
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer default-token" && authHeader != "Bearer work-token" {
			t.Errorf("Authorization = %q", authHeader)
		}
		accountID := r.Header.Get("Chatgpt-Account-Id")
		if accountID != "acc-default" && accountID != "acc-work" {
			t.Errorf("Chatgpt-Account-Id = %q", accountID)
		}
		if r.Header.Get("Originator") != "codex_cli_rs" {
			t.Errorf("Originator = %q, want codex_cli_rs", r.Header.Get("Originator"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "pro",
			"rate_limit": map[string]any{
				"allowed":       true,
				"limit_reached": false,
				"primary_window": map[string]any{
					"used_percent":         42,
					"limit_window_seconds": 18000,
					"reset_at":             1735689600,
				},
				"secondary_window": map[string]any{
					"used_percent":         5,
					"limit_window_seconds": 604800,
					"reset_at":             1736294400,
				},
			},
			"additional_rate_limits": []map[string]any{
				{
					"limit_name":      "GPT-5.3-Codex-Spark",
					"metered_feature": "codex_spark",
					"rate_limit": map[string]any{
						"allowed":       true,
						"limit_reached": false,
						"primary_window": map[string]any{
							"used_percent":         0,
							"limit_window_seconds": 18000,
							"reset_at":             1735689600,
						},
					},
				},
			},
		})
	}))
	defer server.Close()
	withCodexAccountLimitsBaseURL(t, server.URL)

	h := NewHandler("")
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/codex-account-limits", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("usage API requests = %d, want 2", got)
	}

	var resp codexAccountLimitsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2: %#v", len(resp.Accounts), resp.Accounts)
	}
	work := findCodexLimitAccount(resp.Accounts, "openai:work")
	if work == nil {
		t.Fatal("work account missing")
	}
	if work.Email != "work@example.com" || work.AccountID != "acc-work" || work.Plan != "pro" {
		t.Fatalf("work account metadata = %#v", work)
	}
	if len(work.Entries) != 3 {
		t.Fatalf("work entries = %d, want 3: %#v", len(work.Entries), work.Entries)
	}
	if work.Entries[0].Name != "codex" || work.Entries[0].Window != "5h" ||
		work.Entries[0].UsedPercent == nil || *work.Entries[0].UsedPercent != 42 {
		t.Fatalf("first usage entry = %#v", work.Entries[0])
	}
	if work.Entries[1].Window != "weekly" {
		t.Fatalf("second usage entry = %#v", work.Entries[1])
	}
	if work.Entries[2].Name != "GPT-5.3-Codex-Spark" ||
		work.Entries[2].UsedPercent == nil || *work.Entries[2].UsedPercent != 0 {
		t.Fatalf("additional usage entry = %#v", work.Entries[2])
	}
	missing := findCodexLimitAccount(resp.Accounts, "missing")
	if missing != nil {
		t.Fatalf("Codex config-only account should not be returned: %#v", missing)
	}
	anthropic := findCodexLimitAccount(resp.Accounts, "anthropic")
	if anthropic != nil {
		t.Fatalf("non-OpenAI credential should not be returned: %#v", anthropic)
	}
}

func TestCodexAccountLimitsRefreshesExpiredToken(t *testing.T) {
	withPicoclawAuthHome(t)
	setOpenAIAuthCredential(
		t,
		"openai",
		"expired-token",
		"refresh-token",
		"acc-old",
		"default@example.com",
	)

	var usageRequests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/codex/usage":
			count := atomic.AddInt32(&usageRequests, 1)
			if count == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"token_expired","message":"SECRET"}}`))
				return
			}
			if r.Header.Get("Authorization") != "Bearer refreshed-token" {
				t.Errorf("Authorization after refresh = %q", r.Header.Get("Authorization"))
			}
			if r.Header.Get("Chatgpt-Account-Id") != "acc-new" {
				t.Errorf("Chatgpt-Account-Id after refresh = %q", r.Header.Get("Chatgpt-Account-Id"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"plan_type": "pro",
				"rate_limit": map[string]any{
					"allowed":       true,
					"limit_reached": false,
					"primary_window": map[string]any{
						"used_percent":         7,
						"limit_window_seconds": 60,
						"reset_at":             1735689600,
					},
				},
			})
		case "/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse refresh form: %v", err)
			}
			if r.Form.Get("refresh_token") != "refresh-token" {
				t.Errorf("refresh_token = %q", r.Form.Get("refresh_token"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "refreshed-token",
				"refresh_token": "new-refresh-token",
				"account_id":    "acc-new",
				"id_token":      makeCodexIDToken("default@example.com", "acc-new", "pro"),
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	withCodexAccountLimitsBaseURL(t, server.URL)
	withCodexTokenURL(t, server.URL+"/oauth/token")

	resp, err := loadCodexAccountLimits(t.Context())
	if err != nil {
		t.Fatalf("loadCodexAccountLimits() error = %v", err)
	}
	if got := atomic.LoadInt32(&usageRequests); got != 2 {
		t.Fatalf("usage requests = %d, want 2", got)
	}
	if len(resp.Accounts) != 1 {
		t.Fatalf("accounts = %#v", resp.Accounts)
	}
	if resp.Accounts[0].LimitsStatus != "available" || resp.Accounts[0].LimitsError != "" {
		t.Fatalf("account limits = %#v", resp.Accounts[0])
	}
	if resp.Accounts[0].AccountID != "acc-new" {
		t.Fatalf("account id = %q, want acc-new", resp.Accounts[0].AccountID)
	}
}

func TestCodexAccountLimitsSanitizesAPIError(t *testing.T) {
	withPicoclawAuthHome(t)
	setOpenAIAuthCredential(
		t,
		"openai",
		"expired-token",
		"",
		"acc-default",
		"default@example.com",
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"token_expired","message":"SECRET_TOKEN_VALUE"}}`))
	}))
	defer server.Close()
	withCodexAccountLimitsBaseURL(t, server.URL)

	resp, err := loadCodexAccountLimits(t.Context())
	if err != nil {
		t.Fatalf("loadCodexAccountLimits() error = %v", err)
	}
	if len(resp.Accounts) != 1 {
		t.Fatalf("accounts = %#v", resp.Accounts)
	}
	account := resp.Accounts[0]
	if account.LimitsStatus != "error" || account.LimitsError != "token_expired" {
		t.Fatalf("account error = %#v", account)
	}
	encoded, _ := json.Marshal(resp)
	if strings.Contains(string(encoded), "SECRET_TOKEN_VALUE") {
		t.Fatalf("response leaked upstream body: %s", encoded)
	}
}

func TestCodexAccountLimitsUsageURL(t *testing.T) {
	tests := map[string]string{
		"":                                  "https://chatgpt.com/backend-api/wham/usage",
		"https://chatgpt.com":               "https://chatgpt.com/backend-api/wham/usage",
		"https://chatgpt.com/backend-api":   "https://chatgpt.com/backend-api/wham/usage",
		"https://example.test":              "https://example.test/api/codex/usage",
		"https://example.test/backend-api":  "https://example.test/backend-api/wham/usage",
		"https://example.test/backend-api/": "https://example.test/backend-api/wham/usage",
		"  https://chat.openai.com  ":       "https://chat.openai.com/backend-api/wham/usage",
	}
	for input, want := range tests {
		if got := codexAccountLimitsUsageURL(input); got != want {
			t.Fatalf("codexAccountLimitsUsageURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func withPicoclawAuthHome(t *testing.T) {
	t.Helper()
	t.Setenv("PICOCLAW_HOME", t.TempDir())
}

func withCodexAccountLimitsBaseURL(t *testing.T, baseURL string) {
	t.Helper()
	orig := codexAccountLimitsBaseURL
	codexAccountLimitsBaseURL = func() string { return baseURL }
	t.Cleanup(func() { codexAccountLimitsBaseURL = orig })
}

func withCodexTokenURL(t *testing.T, tokenURL string) {
	t.Helper()
	orig := codexAccountLimitsTokenURL
	codexAccountLimitsTokenURL = tokenURL
	t.Cleanup(func() { codexAccountLimitsTokenURL = orig })
}

func setOpenAIAuthCredential(
	t *testing.T,
	credentialID string,
	accessToken string,
	refreshToken string,
	accountID string,
	email string,
) {
	t.Helper()
	if err := auth.SetCredential(credentialID, &auth.AuthCredential{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		AccountID:    accountID,
		Provider:     "openai",
		AuthMethod:   "oauth",
		Email:        email,
	}); err != nil {
		t.Fatalf("SetCredential(%s) error: %v", credentialID, err)
	}
}

func makeCodexIDToken(email string, accountID string, plan string) string {
	claims := map[string]any{
		"email": email,
		"https://api.openai.com/profile": map[string]any{
			"email": email,
		},
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  plan,
		},
	}
	payload, _ := json.Marshal(claims)
	return fmt.Sprintf(
		"header.%s.signature",
		base64.RawURLEncoding.EncodeToString(payload),
	)
}

func findCodexLimitAccount(accounts []codexAccountLimitAccount, id string) *codexAccountLimitAccount {
	for i := range accounts {
		if accounts[i].ID == id {
			return &accounts[i]
		}
	}
	return nil
}

func TestCodexTokenRefreshRequestUsesFormEncoding(t *testing.T) {
	var gotRefreshToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parse refresh body: %v", err)
		}
		gotRefreshToken = values.Get("refresh_token")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fresh-token",
		})
	}))
	defer server.Close()
	withCodexTokenURL(t, server.URL)

	refreshed, err := refreshCodexAccountLimitsToken(t.Context(), codexAuthTokens{
		AccessToken:  "old-token",
		RefreshToken: "refresh-me",
		AccountID:    "acc-123",
	})
	if err != nil {
		t.Fatalf("refreshCodexAccountLimitsToken() error = %v", err)
	}
	if gotRefreshToken != "refresh-me" {
		t.Fatalf("refresh token = %q, want refresh-me", gotRefreshToken)
	}
	if refreshed.AccessToken != "fresh-token" ||
		refreshed.RefreshToken != "refresh-me" ||
		refreshed.AccountID != "acc-123" {
		t.Fatalf("refreshed tokens = %#v", refreshed)
	}
}
