package api

import (
	"bytes"
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

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestOAuthLoginRejectsUnsupportedMethod(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		strings.NewReader(`{"provider":"anthropic","method":"browser"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestOAuthBrowserFlowCreatedAndQueried(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	oauthGeneratePKCE = func() (auth.PKCECodes, error) {
		return auth.PKCECodes{CodeVerifier: "verifier-1", CodeChallenge: "challenge-1"}, nil
	}
	oauthGenerateState = func() (string, error) { return "state-1", nil }
	oauthBuildAuthorizeURL = func(cfg auth.OAuthProviderConfig, pkce auth.PKCECodes, state, redirectURI string) string {
		return "https://example.com/authorize?state=" + state
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		strings.NewReader(`{"provider":"openai","method":"browser"}`),
	)
	req.Host = "localhost:18800"
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var loginResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	flowID, _ := loginResp["flow_id"].(string)
	if flowID == "" {
		t.Fatalf("flow_id is empty: %v", loginResp)
	}
	if loginResp["auth_url"] != "https://example.com/authorize?state=state-1" {
		t.Fatalf("unexpected auth_url: %v", loginResp["auth_url"])
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/oauth/flows/"+flowID, nil)
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("flow status code = %d, want %d, body=%s", rec2.Code, http.StatusOK, rec2.Body.String())
	}
	var flowResp oauthFlowResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &flowResp); err != nil {
		t.Fatalf("unmarshal flow response: %v", err)
	}
	if flowResp.Status != oauthFlowPending {
		t.Fatalf("flow status = %q, want %q", flowResp.Status, oauthFlowPending)
	}
	if flowResp.Method != oauthMethodBrowser {
		t.Fatalf("flow method = %q, want %q", flowResp.Method, oauthMethodBrowser)
	}
}

func TestOAuthBrowserLoginReplacesExpiredNamedCredentialWithoutTouchingSibling(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	if err := auth.SetCredential("openai:work", &auth.AuthCredential{
		AccessToken:  "expired-access-token",
		RefreshToken: "expired-refresh-token",
		ExpiresAt:    time.Now().Add(-time.Hour),
		Provider:     "openai",
		AuthMethod:   "oauth",
		Email:        "work@example.com",
	}); err != nil {
		t.Fatalf("SetCredential expired error: %v", err)
	}
	sibling := &auth.AuthCredential{
		AccessToken:  "personal-access-token",
		RefreshToken: "personal-refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour).UTC().Round(0),
		Provider:     "openai",
		AuthMethod:   "oauth",
		Email:        "personal@example.com",
	}
	if err := auth.SetCredential("openai:personal", sibling); err != nil {
		t.Fatalf("SetCredential sibling error: %v", err)
	}

	oauthGeneratePKCE = func() (auth.PKCECodes, error) {
		return auth.PKCECodes{CodeVerifier: "verifier-1", CodeChallenge: "challenge-1"}, nil
	}
	oauthGenerateState = func() (string, error) { return "state-1", nil }
	oauthBuildAuthorizeURL = func(auth.OAuthProviderConfig, auth.PKCECodes, string, string) string {
		return "https://example.com/authorize?state=state-1"
	}
	fresh := &auth.AuthCredential{
		AccessToken:  "fresh-access-token",
		RefreshToken: "fresh-refresh-token",
		ExpiresAt:    time.Now().Add(2 * time.Hour).UTC().Round(0),
		Provider:     "openai",
		AuthMethod:   "oauth",
		Email:        "renewed@example.com",
	}
	oauthExchangeCodeForTokens = func(
		auth.OAuthProviderConfig,
		string,
		string,
		string,
	) (*auth.AuthCredential, error) {
		return fresh, nil
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		strings.NewReader(
			`{"provider":"openai","credential_id":"openai:work","method":"browser"}`,
		),
	)
	loginReq.Host = "localhost:18800"
	loginReq.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d, body=%s", loginRec.Code, http.StatusOK, loginRec.Body.String())
	}

	var loginResp oauthFlowResponse
	if err := json.Unmarshal(loginRec.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	if loginResp.CredentialID != "openai:work" {
		t.Fatalf("login credential_id = %q, want openai:work", loginResp.CredentialID)
	}
	if loginResp.FlowID == "" {
		t.Fatal("login flow_id is empty")
	}

	callbackRec := httptest.NewRecorder()
	callbackReq := httptest.NewRequest(
		http.MethodGet,
		"/oauth/callback?state=state-1&code=renewal-code",
		nil,
	)
	mux.ServeHTTP(callbackRec, callbackReq)
	if callbackRec.Code != http.StatusOK {
		t.Fatalf("callback status = %d, want %d, body=%s", callbackRec.Code, http.StatusOK, callbackRec.Body.String())
	}

	flowRec := httptest.NewRecorder()
	flowReq := httptest.NewRequest(http.MethodGet, "/api/oauth/flows/"+loginResp.FlowID, nil)
	mux.ServeHTTP(flowRec, flowReq)
	if flowRec.Code != http.StatusOK {
		t.Fatalf("flow status = %d, want %d, body=%s", flowRec.Code, http.StatusOK, flowRec.Body.String())
	}
	var flowResp oauthFlowResponse
	if err := json.Unmarshal(flowRec.Body.Bytes(), &flowResp); err != nil {
		t.Fatalf("unmarshal flow response: %v", err)
	}
	if flowResp.Status != oauthFlowSuccess || flowResp.CredentialID != "openai:work" {
		t.Fatalf("completed flow = %#v, want success for openai:work", flowResp)
	}

	store, err := auth.LoadStore()
	if err != nil {
		t.Fatalf("LoadStore error: %v", err)
	}
	if len(store.Credentials) != 2 {
		t.Fatalf("credential count = %d, want 2: %#v", len(store.Credentials), store.Credentials)
	}
	renewed := store.Credentials["openai:work"]
	if renewed == nil || *renewed != *fresh || renewed.IsExpired() {
		t.Fatalf("renewed credential = %#v, want fresh non-expired tokens", renewed)
	}
	unchanged := store.Credentials["openai:personal"]
	if unchanged == nil || *unchanged != *sibling {
		t.Fatalf("sibling credential = %#v, want unchanged %#v", unchanged, sibling)
	}
}

func TestOAuthTokenRenewalRecoversExactAccountRouterCredential(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	workspace := filepath.Join(filepath.Dir(configPath), "router-workspace")
	cfg.Agents.Defaults.Workspace = workspace
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig error: %v", saveErr)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("MkdirAll workspace error: %v", err)
	}

	const target = "credential:openai:work"
	const sibling = "credential:openai:worker"
	statePath := filepath.Join(workspace, "account_router_state.json")
	router := accountrouter.New("router-main", &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "target",
		Blocks: []config.AccountRouterBlock{
			{
				ID:       "target",
				Type:     config.AccountRouterBlockTypeAccount,
				Account:  target,
				Fallback: "sibling",
			},
			{
				ID:      "sibling",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: sibling,
			},
		},
	}, map[string]accountrouter.Account{
		target: {
			Candidates: []providers.FallbackCandidate{{
				Provider:    "openai",
				Model:       "gpt-4o",
				IdentityKey: "account:" + target,
			}},
		},
		sibling: {
			Candidates: []providers.FallbackCandidate{{
				Provider:    "openai",
				Model:       "gpt-4o",
				IdentityKey: "account:" + sibling,
			}},
		},
	}, statePath)
	if router == nil {
		t.Fatal("accountrouter.New() returned nil")
	}
	failedSelection := router.Select("", accountrouter.SelectReasonInitial)
	failedCandidate := failedSelection.Candidates[0]
	authFailure := errors.New("expired token")
	router.RecordFallbackResult(failedSelection, &providers.FallbackResult{
		Attempts: []providers.FallbackAttempt{{
			Provider:    failedCandidate.Provider,
			Model:       failedCandidate.Model,
			IdentityKey: failedCandidate.StableKey(),
			Reason:      providers.FailoverAuth,
			Error:       authFailure,
		}},
	}, authFailure)
	beforeRenewal := router.Select("", accountrouter.SelectReasonInitial)
	if got := beforeRenewal.CandidateAccounts[beforeRenewal.Candidates[0].StableKey()]; got != sibling {
		t.Fatalf("account before renewal = %q, want fallback %q", got, sibling)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		strings.NewReader(`{"provider":"openai","credential_id":"openai:work","method":"token","token":"fresh-token"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("renewal status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	afterRenewal := router.Select("", accountrouter.SelectReasonInitial)
	if got := afterRenewal.CandidateAccounts[afterRenewal.Candidates[0].StableKey()]; got != target {
		t.Fatalf("account after renewal = %q, want recovered %q", got, target)
	}
}

func TestOAuthCredentialPersistenceTreatsRouterInvalidationAsBestEffort(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	defaultWorkspace := filepath.Join(filepath.Dir(configPath), "default-workspace")
	workerWorkspace := filepath.Join(filepath.Dir(configPath), "worker-workspace")
	cfg.Agents.Defaults.Workspace = defaultWorkspace
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "worker", Workspace: workerWorkspace},
	}
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig error: %v", saveErr)
	}

	invalidatedPaths := make(map[string]bool)
	oauthInvalidateCredentialAuth = func(statePath, credentialID string) error {
		invalidatedPaths[filepath.Clean(statePath)] = true
		if credentialID != "openai:work" {
			t.Fatalf("invalidation credential ID = %q, want openai:work", credentialID)
		}
		return errors.New("injected invalidation failure")
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		strings.NewReader(
			`{"provider":"openai","credential_id":"openai:work","method":"token","token":"durable-token"}`,
		),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	wantPaths := []string{
		filepath.Join(defaultWorkspace, "account_router_state.json"),
		filepath.Join(workerWorkspace, "account_router_state.json"),
	}
	if len(invalidatedPaths) != len(wantPaths) {
		t.Fatalf("invalidation paths = %v, want %v", invalidatedPaths, wantPaths)
	}
	for _, wantPath := range wantPaths {
		if !invalidatedPaths[filepath.Clean(wantPath)] {
			t.Fatalf("invalidation paths = %v, missing %q", invalidatedPaths, wantPath)
		}
	}
	credential, err := auth.GetCredential("openai:work")
	if err != nil {
		t.Fatalf("GetCredential error: %v", err)
	}
	if credential == nil || credential.AccessToken != "durable-token" {
		t.Fatalf("persisted credential = %#v, want durable token", credential)
	}
}

func TestOAuthBrowserFlowInfersCredentialIDFromEmail(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	oauthGeneratePKCE = func() (auth.PKCECodes, error) {
		return auth.PKCECodes{CodeVerifier: "verifier-1", CodeChallenge: "challenge-1"}, nil
	}
	oauthGenerateState = func() (string, error) { return "state-1", nil }
	oauthBuildAuthorizeURL = func(cfg auth.OAuthProviderConfig, pkce auth.PKCECodes, state, redirectURI string) string {
		return "https://example.com/authorize?state=" + state
	}
	oauthExchangeCodeForTokens = func(
		cfg auth.OAuthProviderConfig,
		code, codeVerifier, redirectURI string,
	) (*auth.AuthCredential, error) {
		return &auth.AuthCredential{
			AccessToken: "oauth-access-token",
			AccountID:   "acc-123",
			Provider:    "openai",
			AuthMethod:  "oauth",
			Email:       "Dmitry.Kropachev.Do@example.com",
		}, nil
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		strings.NewReader(`{"provider":"openai","method":"browser"}`),
	)
	req.Host = "localhost:18800"
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/oauth/callback?state=state-1&code=abc", nil)
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("callback status = %d, want %d, body=%s", rec2.Code, http.StatusOK, rec2.Body.String())
	}

	cred, err := auth.GetCredential("openai:dmitry.kropachev.do")
	if err != nil {
		t.Fatalf("GetCredential inferred error: %v", err)
	}
	if cred == nil || cred.AccessToken != "oauth-access-token" {
		t.Fatalf("inferred credential = %#v, want saved OAuth token", cred)
	}
	defaultCred, err := auth.GetCredential("openai")
	if err != nil {
		t.Fatalf("GetCredential default error: %v", err)
	}
	if defaultCred != nil {
		t.Fatalf("default credential should not be created, got %#v", defaultCred)
	}

	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/api/oauth/flows/"+recFlowID(t, rec.Body.Bytes()), nil)
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("flow status code = %d, want %d, body=%s", rec3.Code, http.StatusOK, rec3.Body.String())
	}
	var flowResp oauthFlowResponse
	if unmarshalErr := json.Unmarshal(rec3.Body.Bytes(), &flowResp); unmarshalErr != nil {
		t.Fatalf("unmarshal flow response: %v", unmarshalErr)
	}
	if flowResp.CredentialID != "openai:dmitry.kropachev.do" {
		t.Fatalf("flow credential_id = %q, want openai:dmitry.kropachev.do", flowResp.CredentialID)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	for _, model := range cfg.ModelList {
		if model.CredentialID == "openai:dmitry.kropachev.do" {
			t.Fatalf("oauth login should not create a model entry, got %#v", model)
		}
	}
}

func TestOAuthFlowExpiresWhenQueried(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	now := time.Date(2026, 3, 6, 12, 0, 0, 0, time.UTC)
	oauthNow = func() time.Time { return now }

	h := NewHandler(configPath)
	h.storeOAuthFlow(&oauthFlow{
		ID:        "expired-flow",
		Provider:  oauthProviderOpenAI,
		Method:    oauthMethodBrowser,
		Status:    oauthFlowPending,
		CreatedAt: now.Add(-20 * time.Minute),
		UpdatedAt: now.Add(-20 * time.Minute),
		ExpiresAt: now.Add(-1 * time.Minute),
	})

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/flows/expired-flow", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var flowResp oauthFlowResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &flowResp); err != nil {
		t.Fatalf("unmarshal flow response: %v", err)
	}
	if flowResp.Status != oauthFlowExpired {
		t.Fatalf("flow status = %q, want %q", flowResp.Status, oauthFlowExpired)
	}
}

func TestOAuthCallbackUnknownState(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?state=unknown&code=abc", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "OAuth flow not found") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/accounts?oauth_flow_id=") {
		t.Fatalf("callback fallback should target /accounts, body: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "/credentials?oauth_flow_id=") {
		t.Fatalf("callback fallback should not target /credentials, body: %s", rec.Body.String())
	}
}

func TestOAuthLogoutClearsCredentialAndConfig(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName:  "gpt-5.4",
		Model:      "openai/gpt-5.4",
		AuthMethod: "oauth",
	})
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}
	if err = auth.SetCredential(oauthProviderOpenAI, &auth.AuthCredential{
		AccessToken: "token-before-logout",
		Provider:    oauthProviderOpenAI,
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential error: %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/logout", bytes.NewBufferString(`{"provider":"openai"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cred, err := auth.GetCredential(oauthProviderOpenAI)
	if err != nil {
		t.Fatalf("GetCredential error: %v", err)
	}
	if cred != nil {
		t.Fatalf("expected credential deleted, got %#v", cred)
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	for _, m := range updated.ModelList {
		if strings.HasPrefix(m.Model, "openai/") && m.AuthMethod != "" {
			t.Fatalf("openai model auth_method = %q, want empty", m.AuthMethod)
		}
	}
}

func TestOAuthLogoutClearsAuthMethodForExplicitProviderField(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName:  "gpt-5.4",
		Provider:   "openai",
		Model:      "gpt-5.4",
		AuthMethod: "oauth",
	})
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}
	if err = auth.SetCredential(oauthProviderOpenAI, &auth.AuthCredential{
		AccessToken: "token-before-logout",
		Provider:    oauthProviderOpenAI,
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential error: %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/logout", bytes.NewBufferString(`{"provider":"openai"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if got := updated.ModelList[len(updated.ModelList)-1].AuthMethod; got != "" {
		t.Fatalf("auth_method = %q, want empty", got)
	}
}

func TestOAuthTokenLoginPersistsNamedCredentialWithoutModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		bytes.NewBufferString(`{"provider":"openai","credential_id":"work","method":"token","token":"named-token"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cred, err := auth.GetCredential("openai:work")
	if err != nil {
		t.Fatalf("GetCredential named error: %v", err)
	}
	if cred == nil || cred.AccessToken != "named-token" {
		t.Fatalf("named credential = %#v, want token", cred)
	}
	defaultCred, err := auth.GetCredential("openai")
	if err != nil {
		t.Fatalf("GetCredential default error: %v", err)
	}
	if defaultCred != nil {
		t.Fatalf("default credential should not be overwritten, got %#v", defaultCred)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	for _, m := range cfg.ModelList {
		if m.CredentialID == "openai:work" || m.ModelName == "gpt-5.4-work" {
			t.Fatalf("oauth login should not create a model entry, got %#v", m)
		}
	}
}

func TestOAuthProvidersIncludesGitHubCopilotTokenLogin(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/providers", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Providers []oauthProviderStatus `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal providers: %v", err)
	}
	for _, p := range resp.Providers {
		if p.Provider != oauthProviderGitHubCopilot {
			continue
		}
		if p.DisplayName != "GitHub Copilot" {
			t.Fatalf("display_name = %q, want GitHub Copilot", p.DisplayName)
		}
		if len(p.Methods) != 1 || p.Methods[0] != oauthMethodToken {
			t.Fatalf("methods = %#v, want token only", p.Methods)
		}
		return
	}
	t.Fatal("github-copilot provider missing")
}

func TestOAuthProviderCredentialStatusLifecycle(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name       string
		credential *auth.AuthCredential
		wantStatus string
		wantLogin  bool
	}{
		{name: "missing", wantStatus: "not_logged_in"},
		{
			name: "connected",
			credential: &auth.AuthCredential{
				Provider: "openai", AuthMethod: "oauth", AccessToken: "connected",
				AccountID: "account", Email: "user@example.test", ProjectID: "project",
				ExpiresAt: now.Add(time.Hour),
			},
			wantStatus: "connected", wantLogin: true,
		},
		{
			name: "needs refresh",
			credential: &auth.AuthCredential{
				Provider: "openai", AuthMethod: "oauth", AccessToken: "refresh-soon",
				ExpiresAt: now.Add(time.Minute),
			},
			wantStatus: "needs_refresh", wantLogin: true,
		},
		{
			name: "expired",
			credential: &auth.AuthCredential{
				Provider: "openai", AuthMethod: "oauth", AccessToken: "expired",
				ExpiresAt: now.Add(-time.Minute),
			},
			wantStatus: "expired", wantLogin: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := newOAuthProviderCredentialStatus("openai", "openai:work", test.credential)
			if status.Provider != "openai" || status.CredentialID != "openai:work" ||
				status.DisplayName == "" || len(status.Methods) == 0 || status.Status != test.wantStatus ||
				status.LoggedIn != test.wantLogin {
				t.Fatalf("credential status = %#v", status)
			}
			if test.credential != nil {
				if status.AuthMethod != test.credential.AuthMethod || status.AccountID != test.credential.AccountID ||
					status.Email != test.credential.Email || status.ProjectID != test.credential.ProjectID {
					t.Fatalf("credential metadata = %#v", status)
				}
				if test.credential.ExpiresAt.IsZero() != (status.ExpiresAt == "") {
					t.Fatalf("credential expiry = %#v", status)
				}
			}
		})
	}

	preserved := oauthProviderStatus{DisplayName: "Preserved", Methods: []string{"token"}}
	preserved.applyCredential("deepseek", "deepseek:work", &auth.AuthCredential{
		Provider: "deepseek", AuthMethod: "token", AccessToken: "token",
	})
	if preserved.DisplayName != "Preserved" || !reflect.DeepEqual(preserved.Methods, []string{"token"}) ||
		preserved.Status != "connected" || preserved.ExpiresAt != "" {
		t.Fatalf("prepopulated provider status = %#v", preserved)
	}
}

func TestOAuthProvidersIncludesOnlyAccountStoreCapableModelProviders(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/providers", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Providers []oauthProviderStatus `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal providers: %v", err)
	}
	providersByID := make(map[string]oauthProviderStatus, len(resp.Providers))
	for _, p := range resp.Providers {
		providersByID[p.Provider] = p
	}

	for _, provider := range []string{"deepseek", "gemini"} {
		status, ok := providersByID[provider]
		if !ok {
			t.Fatalf("%s provider missing from account provider list", provider)
		}
		if len(status.Methods) != 1 || status.Methods[0] != oauthMethodToken {
			t.Fatalf("%s methods = %#v, want token", provider, status.Methods)
		}
	}

	for _, provider := range []string{"bedrock", "claude-cli", "codex-cli", "elevenlabs"} {
		if _, ok := providersByID[provider]; ok {
			t.Fatalf("%s must not be advertised by the account provider API", provider)
		}
	}

	for _, provider := range []string{
		oauthProviderOpenAI,
		oauthProviderAnthropic,
		oauthProviderGoogleAntigravity,
		oauthProviderGitHubCopilot,
	} {
		if _, ok := providersByID[provider]; !ok {
			t.Fatalf("special account provider %s missing from account provider list", provider)
		}
	}
}

func TestOAuthTokenLoginRejectsProvidersWithoutAccountStoreRuntime(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	for _, provider := range []string{"bedrock", "claude-cli", "codex-cli", "elevenlabs"} {
		t.Run(provider, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/oauth/login",
				bytes.NewBufferString(fmt.Sprintf(
					`{"provider":%q,"method":"token","token":"must-not-be-saved"}`,
					provider,
				)),
			)
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "unsupported provider") {
				t.Fatalf("body = %q, want unsupported provider error", rec.Body.String())
			}
			cred, err := auth.GetCredential(provider)
			if err != nil {
				t.Fatalf("GetCredential error: %v", err)
			}
			if cred != nil {
				t.Fatalf("credential was persisted for unsupported account provider: %#v", cred)
			}
		})
	}
}

func TestOAuthTokenLoginPersistsCatalogProviderCredentialWithoutModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		bytes.NewBufferString(`{"provider":"deepseek","credential_id":"work","method":"token","token":"ds-token"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cred, err := auth.GetCredential("deepseek:work")
	if err != nil {
		t.Fatalf("GetCredential error: %v", err)
	}
	if cred == nil {
		t.Fatal("deepseek credential missing")
	}
	if cred.AccessToken != "ds-token" {
		t.Fatalf("AccessToken = %q, want saved token", cred.AccessToken)
	}
	if cred.Provider != "deepseek" {
		t.Fatalf("Provider = %q, want deepseek", cred.Provider)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	for _, m := range cfg.ModelList {
		if m.CredentialID == "deepseek:work" {
			t.Fatalf("token login should not create a model entry, got %#v", m)
		}
	}
}

func TestOAuthGitHubCopilotTokenLoginRejectsClassicPAT(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		bytes.NewBufferString(
			`{"provider":"copilot","credential_id":"work","method":"token","token":"ghp_unsupported"}`,
		),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ghp_") {
		t.Fatalf("error should mention ghp_ token family, body=%s", rec.Body.String())
	}
	cred, err := auth.GetCredential("github-copilot:work")
	if err != nil {
		t.Fatalf("GetCredential error: %v", err)
	}
	if cred != nil {
		t.Fatalf("credential should not be saved, got %#v", cred)
	}
}

func TestOAuthGitHubCopilotTokenLoginPersistsNamedCredentialWithoutModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/login",
		bytes.NewBufferString(
			`{"provider":"copilot","credential_id":"work","method":"token","token":"gho_named-token"}`,
		),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cred, err := auth.GetCredential("github-copilot:work")
	if err != nil {
		t.Fatalf("GetCredential named error: %v", err)
	}
	if cred == nil {
		t.Fatal("named credential missing")
	}
	if cred.AccessToken != "gho_named-token" {
		t.Fatalf("AccessToken = %q, want saved token", cred.AccessToken)
	}
	if cred.Provider != "github-copilot" {
		t.Fatalf("Provider = %q, want github-copilot", cred.Provider)
	}
	if cred.AuthMethod != "token" {
		t.Fatalf("AuthMethod = %q, want token", cred.AuthMethod)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	for _, m := range cfg.ModelList {
		if m.CredentialID == "github-copilot:work" || m.ModelName == "copilot-work" {
			t.Fatalf("oauth login should not create a model entry, got %#v", m)
		}
	}
}

func TestOAuthLogoutNamedCredentialOnlyClearsMatchingModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName:  "gpt-default",
			Provider:   "openai",
			Model:      "gpt-5.4",
			AuthMethod: "oauth",
		},
		{
			ModelName:    "gpt-work",
			Provider:     "openai",
			Model:        "gpt-5.4",
			AuthMethod:   "oauth",
			CredentialID: "openai:work",
		},
	}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}
	if err = auth.SetCredential("openai", &auth.AuthCredential{
		AccessToken: "default-token",
		Provider:    "openai",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential default error: %v", err)
	}
	if err = auth.SetCredential("openai:work", &auth.AuthCredential{
		AccessToken: "work-token",
		Provider:    "openai",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential named error: %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/oauth/logout",
		bytes.NewBufferString(`{"provider":"openai","credential_id":"work"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	defaultCred, err := auth.GetCredential("openai")
	if err != nil {
		t.Fatalf("GetCredential default error: %v", err)
	}
	if defaultCred == nil {
		t.Fatal("default credential was deleted")
	}
	namedCred, err := auth.GetCredential("openai:work")
	if err != nil {
		t.Fatalf("GetCredential named error: %v", err)
	}
	if namedCred != nil {
		t.Fatalf("named credential should be deleted, got %#v", namedCred)
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if updated.ModelList[0].AuthMethod != "oauth" {
		t.Fatalf("default auth_method = %q, want oauth", updated.ModelList[0].AuthMethod)
	}
	if updated.ModelList[1].AuthMethod != "" {
		t.Fatalf("named auth_method = %q, want empty", updated.ModelList[1].AuthMethod)
	}
}

func recFlowID(t *testing.T, body []byte) string {
	t.Helper()
	var loginResp oauthFlowResponse
	if err := json.Unmarshal(body, &loginResp); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	if loginResp.FlowID == "" {
		t.Fatalf("login response missing flow_id: %s", body)
	}
	return loginResp.FlowID
}

func setupOAuthTestEnv(t *testing.T) (string, func()) {
	t.Helper()

	tmp := t.TempDir()
	oldHome := os.Getenv("HOME")
	oldPicoHome := os.Getenv("PICOCLAW_HOME")

	if err := os.Setenv("HOME", tmp); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	if err := os.Setenv("PICOCLAW_HOME", filepath.Join(tmp, ".picoclaw")); err != nil {
		t.Fatalf("set PICOCLAW_HOME: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "custom-default",
		Provider:  "openai",
		Model:     "gpt-4o",
		APIKeys:   config.SimpleSecureStrings("sk-default"),
		Enabled:   true,
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "custom-default",
		Model: "openai/gpt-4o",
	}}

	configPath := filepath.Join(tmp, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}

	cleanup := func() {
		_ = os.Setenv("HOME", oldHome)
		if oldPicoHome == "" {
			_ = os.Unsetenv("PICOCLAW_HOME")
		} else {
			_ = os.Setenv("PICOCLAW_HOME", oldPicoHome)
		}
	}
	return configPath, cleanup
}

func resetOAuthHooks(t *testing.T) {
	t.Helper()

	origNow := oauthNow
	origGeneratePKCE := oauthGeneratePKCE
	origGenerateState := oauthGenerateState
	origBuildAuthorizeURL := oauthBuildAuthorizeURL
	origRequestDeviceCode := oauthRequestDeviceCode
	origPollDeviceCodeOnce := oauthPollDeviceCodeOnce
	origExchangeCodeForTokens := oauthExchangeCodeForTokens
	origGetCredential := oauthGetCredential
	origSetCredential := oauthSetCredential
	origDeleteCredential := oauthDeleteCredential
	origLoadStore := oauthLoadStore
	origFetchProject := oauthFetchAntigravityProject
	origFetchGoogleEmail := oauthFetchGoogleUserEmailFunc
	origInvalidateCredentialAuth := oauthInvalidateCredentialAuth

	t.Cleanup(func() {
		oauthNow = origNow
		oauthGeneratePKCE = origGeneratePKCE
		oauthGenerateState = origGenerateState
		oauthBuildAuthorizeURL = origBuildAuthorizeURL
		oauthRequestDeviceCode = origRequestDeviceCode
		oauthPollDeviceCodeOnce = origPollDeviceCodeOnce
		oauthExchangeCodeForTokens = origExchangeCodeForTokens
		oauthGetCredential = origGetCredential
		oauthSetCredential = origSetCredential
		oauthDeleteCredential = origDeleteCredential
		oauthLoadStore = origLoadStore
		oauthFetchAntigravityProject = origFetchProject
		oauthFetchGoogleUserEmailFunc = origFetchGoogleEmail
		oauthInvalidateCredentialAuth = origInvalidateCredentialAuth
	})
}
