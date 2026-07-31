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
	if modelID != "coding" {
		t.Fatalf("model alias = %q, want coding", modelID)
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
	if modelID != "coding" {
		t.Fatalf("model alias = %q, want coding", modelID)
	}
	wantCalls := []string{"openai:missing", "github-copilot:ready"}
	if !reflect.DeepEqual(credentialCalls, wantCalls) {
		t.Fatalf("credential calls = %v, want %v", credentialCalls, wantCalls)
	}
}

func TestCreateProviderBootstrapsModelRouterFromTerminalAlias(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "openai-work"
	cfg.Agents.Defaults.ModelName = "task-router"
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "openai-work",
		Provider:  "openai",
		APIBase:   "https://api.example.test/v1",
		Enabled:   true,
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "gpt-5.4",
	}}
	cfg.ModelRouters = []config.ModelRouterConfig{{
		Name:    "task-router",
		Enabled: true,
		Entry:   "route",
		Blocks: []config.ModelRouterBlock{
			{
				ID:       "route",
				Type:     config.ModelRouterBlockTypeRules,
				Fallback: "coding",
				Rules: []config.ModelRouterRule{{
					Match:  config.ModelRouterRuleHasCode,
					Target: "coding",
				}},
			},
			{
				ID:    "coding",
				Type:  config.ModelRouterBlockTypeModel,
				Model: "coding",
			},
		},
	}}

	provider, selector, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if _, ok := provider.(*HTTPProvider); !ok {
		t.Fatalf("provider = %T, want *HTTPProvider", provider)
	}
	if selector != "task-router" {
		t.Fatalf("selector = %q, want task-router", selector)
	}
}

func TestCreateProviderIgnoresDisabledDuplicateConcreteAccountRows(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "work"
	cfg.Agents.Defaults.ModelName = "coding"
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "gpt-5.4",
	}}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "work",
			Provider:  "anthropic",
			APIKeys:   config.SimpleSecureStrings("disabled-key"),
			Enabled:   false,
		},
		{
			ModelName: "work",
			Provider:  "openai",
			APIBase:   "https://enabled.example.test/v1",
			APIKeys:   config.SimpleSecureStrings("enabled-key"),
			Enabled:   true,
		},
	}

	for range 4 {
		provider, selector, err := CreateProvider(cfg)
		if err != nil {
			t.Fatalf("CreateProvider() error = %v", err)
		}
		if _, ok := provider.(*HTTPProvider); !ok {
			t.Fatalf("provider = %T, want enabled OpenAI-compatible account", provider)
		}
		if selector != "coding" {
			t.Fatalf("selector = %q, want coding", selector)
		}
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
		if model != "gpt-4.1" {
			t.Fatalf("constructor model = %q, want gpt-4.1", model)
		}
		return &factoryStubProvider{}, nil
	}

	cfg := config.DefaultConfig()
	cfg.ModelList = nil
	cfg.Agents.Defaults.AccountRef = router.Name
	cfg.Agents.Defaults.ModelName = "coding"
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "gpt-4.1",
	}}
	cfg.AccountRouters = []config.AccountRouterConfig{router}
	cfg.MaterializeAccountRouterModels()

	provider, modelID, err := CreateProvider(cfg)
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	return provider, modelID, credentialCalls
}
