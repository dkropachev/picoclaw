package cliprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

type fakeCopilotClient struct {
	startCalled bool
	stopped     bool
	session     *fakeCopilotSession
	config      *copilot.SessionConfig
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (f *fakeCopilotClient) Start(context.Context) error {
	f.startCalled = true
	return nil
}

func (f *fakeCopilotClient) Stop() {
	f.stopped = true
}

func (f *fakeCopilotClient) CreateSession(
	_ context.Context,
	cfg *copilot.SessionConfig,
) (copilotSession, error) {
	f.config = cfg
	if f.session == nil {
		f.session = &fakeCopilotSession{content: "copilot response"}
	}
	return f.session, nil
}

type fakeCopilotSession struct {
	content string
	prompt  string
}

func (f *fakeCopilotSession) SendAndWait(
	_ context.Context,
	opts copilot.MessageOptions,
) (*copilot.SessionEvent, error) {
	f.prompt = opts.Prompt
	return &copilot.SessionEvent{
		Data: copilot.Data{
			Content: copilot.String(f.content),
		},
	}, nil
}

func TestGitHubCopilotLocalProviderUsesExternalCLIURL(t *testing.T) {
	origNewClient := newCopilotClient
	t.Cleanup(func() { newCopilotClient = origNewClient })

	fakeClient := &fakeCopilotClient{}
	var gotOptions *copilot.ClientOptions
	newCopilotClient = func(opts *copilot.ClientOptions) copilotClient {
		gotOptions = opts
		return fakeClient
	}

	provider, err := NewGitHubCopilotProvider("localhost:4321", "grpc", "")
	if err != nil {
		t.Fatalf("NewGitHubCopilotProvider() error = %v", err)
	}
	defer provider.Close()

	if gotOptions == nil {
		t.Fatal("client options not captured")
	}
	if gotOptions.CLIUrl != "localhost:4321" {
		t.Fatalf("CLIUrl = %q, want localhost:4321", gotOptions.CLIUrl)
	}
	if gotOptions.GitHubToken != "" {
		t.Fatalf("GitHubToken should be empty for local bridge, got %q", gotOptions.GitHubToken)
	}
	if gotOptions.UseLoggedInUser != nil {
		t.Fatal("UseLoggedInUser should be nil for local bridge")
	}
	if !fakeClient.startCalled {
		t.Fatal("Start was not called")
	}
	if fakeClient.config == nil {
		t.Fatal("session config not captured")
	}
	if fakeClient.config.Model != githubCopilotDefaultModel {
		t.Fatalf("Model = %q, want %q", fakeClient.config.Model, githubCopilotDefaultModel)
	}
	if fakeClient.config.OnPermissionRequest == nil {
		t.Fatal("OnPermissionRequest should be configured")
	}
}

func TestGitHubCopilotTokenProviderDisablesAmbientLogin(t *testing.T) {
	origNewClient := newCopilotClient
	origHTTPClient := githubCopilotHTTPClient
	origTokenEndpoint := githubCopilotTokenEndpoint
	origAPIBase := githubCopilotAPIBaseEndpoint
	t.Cleanup(func() {
		newCopilotClient = origNewClient
		githubCopilotHTTPClient = origHTTPClient
		githubCopilotTokenEndpoint = origTokenEndpoint
		githubCopilotAPIBaseEndpoint = origAPIBase
	})

	newCopilotClient = func(opts *copilot.ClientOptions) copilotClient {
		t.Fatalf("token-backed GitHub Copilot provider must not construct SDK client: %#v", opts)
		return nil
	}

	var gotExchangeAuth string
	var gotChatAuth string
	var gotIntegrationID string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			gotExchangeAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token":"copilot-api-token","endpoints":{"api":%q}}`, srv.URL)
		case "/chat/completions":
			gotChatAuth = r.Header.Get("Authorization")
			gotIntegrationID = r.Header.Get("Copilot-Integration-Id")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"content":"copilot response"},"finish_reason":"stop"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	githubCopilotHTTPClient = srv.Client()
	githubCopilotTokenEndpoint = srv.URL + "/copilot_internal/v2/token"
	githubCopilotAPIBaseEndpoint = srv.URL

	provider, err := NewGitHubCopilotProviderWithToken("gho_test-token", "gpt-4.1")
	if err != nil {
		t.Fatalf("NewGitHubCopilotProviderWithToken() error = %v", err)
	}
	defer provider.Close()

	resp, err := provider.Chat(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "gpt-4.1", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Content != "copilot response" {
		t.Fatalf("Content = %q, want copilot response", resp.Content)
	}
	if gotExchangeAuth != "token gho_test-token" {
		t.Fatalf("exchange Authorization = %q, want GitHub token auth", gotExchangeAuth)
	}
	if gotChatAuth != "Bearer copilot-api-token" {
		t.Fatalf("chat Authorization = %q, want exchanged Copilot bearer", gotChatAuth)
	}
	if gotIntegrationID != "vscode-chat" {
		t.Fatalf("Copilot-Integration-Id = %q, want vscode-chat", gotIntegrationID)
	}
}

func TestGitHubCopilotTokenProviderRejectsUnsupportedToken(t *testing.T) {
	origNewClient := newCopilotClient
	t.Cleanup(func() { newCopilotClient = origNewClient })

	newCopilotClient = func(opts *copilot.ClientOptions) copilotClient {
		t.Fatalf("client should not be constructed for invalid token: %#v", opts)
		return nil
	}

	_, err := NewGitHubCopilotProviderWithToken("ghp_classic", "gpt-4.1")
	if err == nil {
		t.Fatal("NewGitHubCopilotProviderWithToken() error = nil, want unsupported token error")
	}
	if !strings.Contains(err.Error(), "ghp_") {
		t.Fatalf("error = %q, want ghp_ detail", err.Error())
	}
}

func TestGitHubCopilotTokenProviderDoesNotFallbackChatOnExchange403(t *testing.T) {
	origHTTPClient := githubCopilotHTTPClient
	origTokenEndpoint := githubCopilotTokenEndpoint
	origAPIBase := githubCopilotAPIBaseEndpoint
	t.Cleanup(func() {
		githubCopilotHTTPClient = origHTTPClient
		githubCopilotTokenEndpoint = origTokenEndpoint
		githubCopilotAPIBaseEndpoint = origAPIBase
	})

	var chatRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			http.Error(w, "exchange forbidden", http.StatusForbidden)
		case "/chat/completions":
			chatRequests++
			http.Error(w, "must not be called", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	githubCopilotHTTPClient = srv.Client()
	githubCopilotTokenEndpoint = srv.URL + "/copilot_internal/v2/token"
	githubCopilotAPIBaseEndpoint = srv.URL

	provider, err := NewGitHubCopilotProviderWithToken("gho_test-token", "gpt-4.1")
	if err != nil {
		t.Fatalf("NewGitHubCopilotProviderWithToken() error = %v", err)
	}
	_, err = provider.Chat(
		context.Background(),
		[]Message{{Role: "user", Content: "hello"}},
		nil,
		"gpt-4.1",
		nil,
	)
	if err == nil {
		t.Fatal("Chat() error = nil, want exchange error")
	}
	if chatRequests != 0 {
		t.Fatalf("chat requests = %d, want 0", chatRequests)
	}
	if !strings.Contains(err.Error(), "GitHub returned status 403") {
		t.Fatalf("error = %q, want exchange status 403", err)
	}
}

func TestListGitHubCopilotModelsWithTokenUsesExplicitToken(t *testing.T) {
	origNewClient := newCopilotClient
	origHTTPClient := githubCopilotHTTPClient
	origTokenEndpoint := githubCopilotTokenEndpoint
	origAPIBase := githubCopilotAPIBaseEndpoint
	t.Cleanup(func() {
		newCopilotClient = origNewClient
		githubCopilotHTTPClient = origHTTPClient
		githubCopilotTokenEndpoint = origTokenEndpoint
		githubCopilotAPIBaseEndpoint = origAPIBase
	})

	newCopilotClient = func(opts *copilot.ClientOptions) copilotClient {
		t.Fatalf("model listing must use direct HTTPS, not SDK client: %#v", opts)
		return nil
	}

	var gotExchangeAuth string
	var gotModelsAuth string
	var gotIntegrationID string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			gotExchangeAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"token":"copilot-api-token","endpoints":{"api":%q}}`, srv.URL)
		case "/models":
			gotModelsAuth = r.Header.Get("Authorization")
			gotIntegrationID = r.Header.Get("Copilot-Integration-Id")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[`+
				`{"id":"gpt-5","name":"GPT-5"},`+
				`{"id":"claude-sonnet-4.5","name":"Claude Sonnet 4.5"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	githubCopilotHTTPClient = srv.Client()
	githubCopilotTokenEndpoint = srv.URL + "/copilot_internal/v2/token"
	githubCopilotAPIBaseEndpoint = srv.URL

	models, err := ListGitHubCopilotModelsWithToken(context.Background(), "gho_test-token")
	if err != nil {
		t.Fatalf("ListGitHubCopilotModelsWithToken() error = %v", err)
	}
	if gotExchangeAuth != "token gho_test-token" {
		t.Fatalf("exchange Authorization = %q, want GitHub token auth", gotExchangeAuth)
	}
	if gotModelsAuth != "Bearer copilot-api-token" {
		t.Fatalf("models Authorization = %q, want exchanged Copilot bearer", gotModelsAuth)
	}
	if gotIntegrationID != "vscode-chat" {
		t.Fatalf("Copilot-Integration-Id = %q, want vscode-chat", gotIntegrationID)
	}
	if len(models) != 2 || models[1].ID != "claude-sonnet-4.5" {
		t.Fatalf("models = %+v, want direct API model list", models)
	}
}

func TestListGitHubCopilotModelsWithTokenFallsBackToRawTokenOnExchange404(t *testing.T) {
	origHTTPClient := githubCopilotHTTPClient
	origTokenEndpoint := githubCopilotTokenEndpoint
	origAPIBase := githubCopilotAPIBaseEndpoint
	t.Cleanup(func() {
		githubCopilotHTTPClient = origHTTPClient
		githubCopilotTokenEndpoint = origTokenEndpoint
		githubCopilotAPIBaseEndpoint = origAPIBase
	})

	var gotModelsAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			http.NotFound(w, r)
		case "/models":
			gotModelsAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"models":[{"id":"gpt-5.5","name":"GPT-5.5"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	githubCopilotHTTPClient = srv.Client()
	githubCopilotTokenEndpoint = srv.URL + "/copilot_internal/v2/token"
	githubCopilotAPIBaseEndpoint = srv.URL

	models, err := ListGitHubCopilotModelsWithToken(context.Background(), "gho_test-token")
	if err != nil {
		t.Fatalf("listGitHubCopilotModels() error = %v", err)
	}
	if gotModelsAuth != "Bearer gho_test-token" {
		t.Fatalf("models Authorization = %q, want raw GitHub bearer fallback", gotModelsAuth)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.5" {
		t.Fatalf("models = %+v, want fallback model", models)
	}
}

func TestListGitHubCopilotModelsWithTokenFallsBackToRawTokenOnExchange403(t *testing.T) {
	origHTTPClient := githubCopilotHTTPClient
	origTokenEndpoint := githubCopilotTokenEndpoint
	origAPIBase := githubCopilotAPIBaseEndpoint
	t.Cleanup(func() {
		githubCopilotHTTPClient = origHTTPClient
		githubCopilotTokenEndpoint = origTokenEndpoint
		githubCopilotAPIBaseEndpoint = origAPIBase
	})

	var modelsRequests int
	var gotModelsAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			http.Error(w, "exchange forbidden", http.StatusForbidden)
		case "/models":
			modelsRequests++
			gotModelsAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"models":[{"id":"gpt-5.5","name":"GPT-5.5"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	githubCopilotHTTPClient = srv.Client()
	githubCopilotTokenEndpoint = srv.URL + "/copilot_internal/v2/token"
	githubCopilotAPIBaseEndpoint = srv.URL

	models, err := ListGitHubCopilotModelsWithToken(context.Background(), "gho_test-token")
	if err != nil {
		t.Fatalf("ListGitHubCopilotModelsWithToken() error = %v", err)
	}
	if modelsRequests != 1 {
		t.Fatalf("models requests = %d, want 1", modelsRequests)
	}
	if gotModelsAuth != "Bearer gho_test-token" {
		t.Fatalf("models Authorization = %q, want raw GitHub bearer fallback", gotModelsAuth)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.5" {
		t.Fatalf("models = %+v, want fallback model", models)
	}
}

func TestListGitHubCopilotModelsWithTokenReturnsBothErrorsWhen403FallbackFails(t *testing.T) {
	origHTTPClient := githubCopilotHTTPClient
	origTokenEndpoint := githubCopilotTokenEndpoint
	origAPIBase := githubCopilotAPIBaseEndpoint
	t.Cleanup(func() {
		githubCopilotHTTPClient = origHTTPClient
		githubCopilotTokenEndpoint = origTokenEndpoint
		githubCopilotAPIBaseEndpoint = origAPIBase
	})

	var modelsRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			http.Error(w, "exchange forbidden for gho_test-token", http.StatusForbidden)
		case "/models":
			modelsRequests++
			http.Error(w, "models forbidden for gho_test-token", http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	githubCopilotHTTPClient = srv.Client()
	githubCopilotTokenEndpoint = srv.URL + "/copilot_internal/v2/token"
	githubCopilotAPIBaseEndpoint = srv.URL

	_, err := ListGitHubCopilotModelsWithToken(context.Background(), "gho_test-token")
	if err == nil {
		t.Fatal("ListGitHubCopilotModelsWithToken() error = nil, want fallback error")
	}
	if modelsRequests != 1 {
		t.Fatalf("models requests = %d, want 1", modelsRequests)
	}
	for _, want := range []string{
		"exchanging GitHub token for Copilot API token: GitHub returned status 403",
		"raw-token model fallback failed: GitHub Copilot models returned status 403",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "gho_test-token") {
		t.Fatalf("error leaked GitHub token: %q", err)
	}
}

func TestListGitHubCopilotModelsWithTokenPreserves403FallbackCancellation(t *testing.T) {
	origHTTPClient := githubCopilotHTTPClient
	origTokenEndpoint := githubCopilotTokenEndpoint
	origAPIBase := githubCopilotAPIBaseEndpoint
	t.Cleanup(func() {
		githubCopilotHTTPClient = origHTTPClient
		githubCopilotTokenEndpoint = origTokenEndpoint
		githubCopilotAPIBaseEndpoint = origAPIBase
	})

	var modelsRequests int
	githubCopilotHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/copilot_internal/v2/token":
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Body:       io.NopCloser(strings.NewReader("exchange forbidden")),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			case "/models":
				modelsRequests++
				return nil, context.Canceled
			default:
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       http.NoBody,
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}
		}),
	}
	githubCopilotTokenEndpoint = "https://api.github.test/copilot_internal/v2/token"
	githubCopilotAPIBaseEndpoint = "https://api.githubcopilot.test"

	_, err := ListGitHubCopilotModelsWithToken(context.Background(), "gho_test-token")
	if err == nil {
		t.Fatal("ListGitHubCopilotModelsWithToken() error = nil, want cancellation")
	}
	if modelsRequests != 1 {
		t.Fatalf("models requests = %d, want 1", modelsRequests)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want errors.Is(context.Canceled)", err)
	}
	if !strings.Contains(err.Error(), "GitHub returned status 403") {
		t.Fatalf("error = %q, want original exchange status 403", err)
	}
}

func TestListGitHubCopilotModelsWithTokenDoesNotFallbackForOtherExchangeErrors(t *testing.T) {
	for _, statusCode := range []int{http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("status_%d", statusCode), func(t *testing.T) {
			origHTTPClient := githubCopilotHTTPClient
			origTokenEndpoint := githubCopilotTokenEndpoint
			origAPIBase := githubCopilotAPIBaseEndpoint
			t.Cleanup(func() {
				githubCopilotHTTPClient = origHTTPClient
				githubCopilotTokenEndpoint = origTokenEndpoint
				githubCopilotAPIBaseEndpoint = origAPIBase
			})

			var modelsRequests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/copilot_internal/v2/token":
					http.Error(w, "exchange rejected", statusCode)
				case "/models":
					modelsRequests++
					http.Error(w, "must not be called", http.StatusInternalServerError)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			githubCopilotHTTPClient = srv.Client()
			githubCopilotTokenEndpoint = srv.URL + "/copilot_internal/v2/token"
			githubCopilotAPIBaseEndpoint = srv.URL

			_, err := ListGitHubCopilotModelsWithToken(context.Background(), "gho_test-token")
			if err == nil {
				t.Fatalf("ListGitHubCopilotModelsWithToken() error = nil, want status %d", statusCode)
			}
			if modelsRequests != 0 {
				t.Fatalf("models requests = %d, want 0", modelsRequests)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("GitHub returned status %d", statusCode)) {
				t.Fatalf("error = %q, want status %d", err, statusCode)
			}
			if strings.Contains(err.Error(), "gho_test-token") {
				t.Fatalf("error leaked GitHub token: %q", err)
			}
		})
	}
}
