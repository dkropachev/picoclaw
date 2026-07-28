package providers

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestCreateProviderBootstrapsRunnableAccountRouterFallback(t *testing.T) {
	router := config.AccountRouterConfig{
		Name:    "router-1",
		Enabled: true,
		Entry:   "primary",
		Blocks: []config.AccountRouterBlock{
			{
				ID:      "disconnected",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "credential:anthropic:must-not-load",
			},
			{
				ID:       "primary",
				Type:     config.AccountRouterBlockTypeAccount,
				Account:  "credential:openai:missing",
				Fallback: "fallback",
			},
			{
				ID:      "fallback",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "credential:github-copilot:ready",
			},
		},
	}

	provider, modelID, credentialCalls := createProviderWithRouterCredentialStubs(t, router)
	if provider == nil {
		t.Fatal("CreateProvider() returned nil provider")
	}
	if modelID != "router-1" {
		t.Fatalf("model selector = %q, want router-1", modelID)
	}
	wantCalls := []string{"openai:missing", "github-copilot:ready"}
	if !reflect.DeepEqual(credentialCalls, wantCalls) {
		t.Fatalf("credential calls = %v, want %v", credentialCalls, wantCalls)
	}
}

func TestCreateProviderBootstrapsRunnableAccountFromBranchEntry(t *testing.T) {
	zero := 0.0
	one := 1.0
	router := config.AccountRouterConfig{
		Name:    "router-1",
		Enabled: true,
		Entry:   "branch",
		Blocks: []config.AccountRouterBlock{
			{
				ID:   "branch",
				Type: config.AccountRouterBlockTypeBranch,
				Condition: &config.AccountRouterCondition{
					Left:     config.AccountRouterExpression{Value: &zero},
					Operator: config.AccountRouterBranchOpEQ,
					Right:    config.AccountRouterExpression{Value: &one},
				},
				Then: "missing",
				Else: "ready",
			},
			{
				ID:      "missing",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "credential:openai:missing",
			},
			{
				ID:      "ready",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "credential:github-copilot:ready",
			},
		},
	}

	provider, modelID, credentialCalls := createProviderWithRouterCredentialStubs(t, router)
	if provider == nil {
		t.Fatal("CreateProvider() returned nil provider")
	}
	if modelID != "router-1" {
		t.Fatalf("model selector = %q, want router-1", modelID)
	}
	wantCalls := []string{"openai:missing", "github-copilot:ready"}
	if !reflect.DeepEqual(credentialCalls, wantCalls) {
		t.Fatalf("credential calls = %v, want %v", credentialCalls, wantCalls)
	}
}

func createProviderWithRouterCredentialStubs(
	t *testing.T,
	router config.AccountRouterConfig,
) (LLMProvider, string, []string) {
	t.Helper()

	origGetCredential := getCredential
	origNewCopilotProvider := newGitHubCopilotProviderWithToken
	t.Cleanup(func() {
		getCredential = origGetCredential
		newGitHubCopilotProviderWithToken = origNewCopilotProvider
	})

	var credentialCalls []string
	getCredential = func(credentialID string) (*auth.AuthCredential, error) {
		credentialCalls = append(credentialCalls, credentialID)
		switch credentialID {
		case "openai:missing":
			return nil, errors.New("credential is not configured")
		case "github-copilot:ready":
			return &auth.AuthCredential{
				AccessToken: "gho_ready-token",
				Provider:    "github-copilot",
				AuthMethod:  "token",
			}, nil
		default:
			t.Fatalf("unexpected credential lookup %q", credentialID)
			return nil, nil
		}
	}
	newGitHubCopilotProviderWithToken = func(token string, model string) (LLMProvider, error) {
		if token != "gho_ready-token" {
			t.Fatalf("constructor token = %q, want gho_ready-token", token)
		}
		if model != "auto" {
			t.Fatalf("constructor model = %q, want auto", model)
		}
		return &factoryStubProvider{}, nil
	}

	cfg := config.DefaultConfig()
	cfg.ModelList = nil
	cfg.Agents.Defaults.ModelName = router.Name
	cfg.AccountRouters = []config.AccountRouterConfig{router}
	cfg.MaterializeAccountRouterModels()

	provider, modelID, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	return provider, modelID, credentialCalls
}
