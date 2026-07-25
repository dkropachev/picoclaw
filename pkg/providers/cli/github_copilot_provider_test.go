package cliprovider

import (
	"context"
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
	t.Cleanup(func() { newCopilotClient = origNewClient })

	fakeClient := &fakeCopilotClient{}
	var gotOptions *copilot.ClientOptions
	newCopilotClient = func(opts *copilot.ClientOptions) copilotClient {
		gotOptions = opts
		return fakeClient
	}

	provider, err := NewGitHubCopilotProviderWithToken("gho_test-token", "gpt-4.1")
	if err != nil {
		t.Fatalf("NewGitHubCopilotProviderWithToken() error = %v", err)
	}
	defer provider.Close()

	if gotOptions == nil {
		t.Fatal("client options not captured")
	}
	if gotOptions.CLIUrl != "" {
		t.Fatalf("CLIUrl = %q, want empty for token-backed client", gotOptions.CLIUrl)
	}
	if gotOptions.GitHubToken != "gho_test-token" {
		t.Fatalf("GitHubToken = %q, want token", gotOptions.GitHubToken)
	}
	if gotOptions.UseLoggedInUser == nil || *gotOptions.UseLoggedInUser {
		t.Fatalf("UseLoggedInUser = %v, want explicit false", gotOptions.UseLoggedInUser)
	}
	if fakeClient.config == nil {
		t.Fatal("session config not captured")
	}
	if fakeClient.config.ClientName != githubCopilotClientName {
		t.Fatalf("ClientName = %q, want %q", fakeClient.config.ClientName, githubCopilotClientName)
	}
	if fakeClient.config.Model != "gpt-4.1" {
		t.Fatalf("Model = %q, want gpt-4.1", fakeClient.config.Model)
	}

	resp, err := provider.Chat(context.Background(), []Message{{Role: "user", Content: "hello"}}, nil, "gpt-4.1", nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Content != "copilot response" {
		t.Fatalf("Content = %q, want copilot response", resp.Content)
	}
	if !strings.Contains(fakeClient.session.prompt, `"role":"user"`) ||
		!strings.Contains(fakeClient.session.prompt, `"content":"hello"`) {
		t.Fatalf("prompt = %q, want serialized input messages", fakeClient.session.prompt)
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
