package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func configureAgentSelectionFixtures(cfg *config.Config) {
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "account-a",
		Provider:  "openai",
		Model:     "transport-placeholder",
		APIKeys:   config.SimpleSecureStrings("test-secret"),
		Enabled:   true,
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "coding", Model: "gpt-5.4"},
		{Name: "backup", Model: "gpt-5.4-mini"},
	}
	cfg.AccountRouters = config.AccountRouterList{{
		Name:    "account-pool",
		Enabled: true,
		Entry:   "primary",
		Blocks: []config.AccountRouterBlock{{
			ID:      "primary",
			Type:    config.AccountRouterBlockTypeAccount,
			Account: "account-a",
		}},
	}}
	cfg.ModelRouters = config.ModelRouterList{{
		Name:    "smart",
		Enabled: true,
		Entry:   "model",
		Blocks: []config.ModelRouterBlock{{
			ID:    "model",
			Type:  config.ModelRouterBlockTypeModel,
			Model: "coding",
		}},
	}}
}

func TestAgentsAPIAccountRefAndAliasPolicyRoundTrip(t *testing.T) {
	harness := newAgentAPITestHarness(t, configureAgentSelectionFixtures)
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	fallbacks := []string{"backup"}
	created := decodeAgentCollection(t, harness.request(
		t,
		http.MethodPost,
		"/api/agents",
		agentMutationRequest{
			ExpectedConfigRevision: &initial.ConfigRevision,
			Agent: &agentResource{
				ID:         "reviewer",
				AccountRef: " account-pool ",
				Model: &agentModelPolicy{
					Primary:   " smart ",
					Fallbacks: &fallbacks,
				},
			},
		},
	))
	if len(created.Agents) != 2 {
		t.Fatalf("created agents = %#v", created.Agents)
	}
	reviewer := created.Agents[1]
	if reviewer.AccountRef != "account-pool" || reviewer.Model == nil ||
		reviewer.Model.Primary != "smart" || reviewer.Model.Fallbacks == nil ||
		len(*reviewer.Model.Fallbacks) != 1 ||
		(*reviewer.Model.Fallbacks)[0] != "backup" {
		t.Fatalf("created reviewer = %#v", reviewer)
	}

	updated := decodeAgentCollection(t, harness.request(
		t,
		http.MethodPut,
		"/api/agents/reviewer",
		agentMutationRequest{
			ExpectedConfigRevision: &created.ConfigRevision,
			Agent: &agentResource{
				ID:         "reviewer",
				AccountRef: "account-a",
				Model:      &agentModelPolicy{Primary: "coding"},
			},
		},
	))
	reviewer = updated.Agents[1]
	if reviewer.AccountRef != "account-a" || reviewer.Model == nil ||
		reviewer.Model.Primary != "coding" {
		t.Fatalf("updated reviewer = %#v", reviewer)
	}

	saved, err := config.LoadConfigForUpdate(harness.configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	if len(saved.Agents.List) != 2 ||
		saved.Agents.List[1].AccountRef != "account-a" ||
		saved.Agents.List[1].Model == nil ||
		saved.Agents.List[1].Model.Primary != "coding" {
		t.Fatalf("persisted reviewer = %#v", saved.Agents.List)
	}
}

func TestAgentsAPIEmptySelectionInheritsConfiguredDefaults(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		configureAgentSelectionFixtures(cfg)
		cfg.Agents.Defaults.AccountRef = "account-a"
		cfg.Agents.Defaults.ModelName = "coding"
	})
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	created := decodeAgentCollection(t, harness.request(
		t,
		http.MethodPost,
		"/api/agents",
		agentMutationRequest{
			ExpectedConfigRevision: &initial.ConfigRevision,
			Agent: &agentResource{
				ID:    "worker",
				Model: &agentModelPolicy{},
			},
		},
	))
	if created.Agents[1].AccountRef != "" ||
		created.Agents[1].Model == nil ||
		created.Agents[1].Model.Primary != "" {
		t.Fatalf("inherited selection projection = %#v", created.Agents[1])
	}
	saved, err := config.LoadConfigForUpdate(harness.configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate() error = %v", err)
	}
	if saved.Agents.List[1].AccountRef != "" ||
		saved.Agents.List[1].Model == nil ||
		saved.Agents.List[1].Model.Primary != "" ||
		saved.Agents.Defaults.AccountRef != "account-a" ||
		saved.Agents.Defaults.ModelName != "coding" {
		t.Fatalf("persisted inherited selection = %#v", saved.Agents)
	}
}

func TestAgentsAPIRejectsRawModelsAndInvalidAccounts(t *testing.T) {
	harness := newAgentAPITestHarness(t, configureAgentSelectionFixtures)
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)

	tests := []struct {
		name       string
		accountRef string
		primary    string
		fallbacks  []string
		message    string
	}{
		{
			name:       "unknown account",
			accountRef: "missing-account",
			primary:    "coding",
			message:    `account "missing-account" is not configured or enabled`,
		},
		{
			name:       "raw upstream model",
			accountRef: "account-a",
			primary:    "gpt-5.4",
			message:    `model alias "gpt-5.4" is not configured`,
		},
		{
			name:       "raw model list account",
			accountRef: "account-a",
			primary:    "account-a",
			message:    `model alias "account-a" is not configured`,
		},
		{
			name:       "case mismatch",
			accountRef: "account-a",
			primary:    "Coding",
			message:    `model alias "Coding" is not configured`,
		},
		{
			name:       "model router fallback",
			accountRef: "account-a",
			primary:    "coding",
			fallbacks:  []string{"smart"},
			message:    `fallback model alias "smart" is not configured`,
		},
		{
			name:       "unknown fallback",
			accountRef: "account-a",
			primary:    "coding",
			fallbacks:  []string{"missing"},
			message:    `fallback model alias "missing" is not configured`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fallbacks := append([]string(nil), test.fallbacks...)
			policy := &agentModelPolicy{Primary: test.primary}
			if test.fallbacks != nil {
				policy.Fallbacks = &fallbacks
			}
			recorder := harness.request(
				t,
				http.MethodPost,
				"/api/agents",
				agentMutationRequest{
					ExpectedConfigRevision: &initial.ConfigRevision,
					Agent: &agentResource{
						ID:         "worker",
						AccountRef: test.accountRef,
						Model:      policy,
					},
				},
			)
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf(
					"status = %d, body=%s",
					recorder.Code,
					recorder.Body.String(),
				)
			}
			var response agentErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if response.Error != "invalid_agent" ||
				!strings.Contains(response.Message, test.message) {
				t.Fatalf("response = %#v, want message containing %q", response, test.message)
			}
		})
	}
}

func TestAgentsAPIRejectsPersistedRawModelReference(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		configureAgentSelectionFixtures(cfg)
		cfg.Agents.List = []config.AgentConfig{{
			ID:         "main",
			Default:    true,
			AccountRef: "account-a",
			Model: &config.AgentModelConfig{
				Primary: "gpt-5.4",
			},
		}}
	})
	recorder := harness.request(t, http.MethodGet, "/api/agents", nil)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response agentErrorResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if response.Error != "agents_unavailable" {
		t.Fatalf("response = %#v", response)
	}
}
