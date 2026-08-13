//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestEventAutomationWiresTrustedReviewAttentionPolicySource(
	t *testing.T,
) {
	tests := []struct {
		name             string
		workflowsEnabled bool
		policies         reviews.AttentionPolicySource
		wantLauncher     bool
	}{
		{
			name:             "enabled with trusted source",
			workflowsEnabled: true,
			policies:         gatewayAttentionPolicySource{},
			wantLauncher:     true,
		},
		{
			name:             "enabled with configured empty source",
			workflowsEnabled: true,
			wantLauncher:     true,
		},
		{
			name:             "source remains inert without workflows",
			workflowsEnabled: false,
			policies:         gatewayAttentionPolicySource{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			cfg := eventAutomationTestConfig(
				workspace,
				filepath.Join(workspace, "eventing", "events.db"),
				true,
				test.workflowsEnabled,
			)
			var executor *workflows.Executor
			if test.workflowsEnabled {
				executor = &workflows.Executor{WorkspaceDir: workspace}
			}
			service, err := newEventAutomationServiceWithReviews(
				context.Background(),
				cfg,
				executor,
				nil,
				nil,
				eventReviewRuntime{attentionPolicies: test.policies},
			)
			if err != nil {
				t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
			}
			if service == nil {
				t.Fatal("newEventAutomationServiceWithReviews() service is nil")
			}
			t.Cleanup(func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if closeErr := service.Close(closeCtx); closeErr != nil {
					t.Errorf("Close() error = %v", closeErr)
				}
			})
			if got := service.reviewAttention != nil; got != test.wantLauncher {
				t.Fatalf("review attention launcher configured = %t, want %t", got, test.wantLauncher)
			}
			if test.wantLauncher && executor.Store == nil {
				t.Fatal("review attention launcher was built before assigning the durable run store")
			}
		})
	}
}

func TestEventAutomationRejectsInvalidConfiguredAttentionBeforeStorage(
	t *testing.T,
) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	databasePath := filepath.Join(workspace, "eventing", "events.db")
	cfg := eventAutomationTestConfig(workspace, databasePath, true, true)
	cfg.Reviews.Attention.Repositories = map[string]map[string]workflows.RepositoryGatePolicy{
		"not-a-repository": {},
	}

	service, err := newEventAutomationServiceWithReviews(
		context.Background(),
		cfg,
		&workflows.Executor{WorkspaceDir: workspace},
		nil,
		nil,
		eventReviewRuntime{},
	)
	if service != nil {
		t.Fatalf("newEventAutomationServiceWithReviews() service = %#v, want nil", service)
	}
	if err == nil || !strings.Contains(err.Error(), "review attention") {
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v, want safe policy validation", err)
	}
	if _, statErr := os.Stat(databasePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Stat(%q) error = %v, want os.ErrNotExist", databasePath, statErr)
	}
}

func TestConfiguredReviewAttentionKeepsOversizedLegacyMigrationActive(t *testing.T) {
	attention := config.ReviewAttentionConfig{
		Global: map[string][]workflows.GateSpec{
			"review.ready": make([]workflows.GateSpec, 9),
		},
		Repositories: make(
			map[string]map[string]workflows.RepositoryGatePolicy,
			config.MaxReviewAttentionRepositories,
		),
	}
	for index := range attention.Global["review.ready"] {
		attention.Global["review.ready"][index] = workflows.GateSpec{
			ID:   fmt.Sprintf("global_%d", index),
			Kind: workflows.GateZero,
		}
	}
	for index := range config.MaxReviewAttentionRepositories {
		attention.Repositories[fmt.Sprintf("owner/repository-%d", index)] = map[string]workflows.RepositoryGatePolicy{
			"review.ready": {
				Mode: workflows.GatePolicyOverlay,
				Gates: []workflows.GateSpec{
					{ID: fmt.Sprintf("repository_%d", index), Kind: workflows.GateZero},
				},
			},
		}
	}
	_, migrationErr := attention.NamedRuleSets()
	if !errors.Is(migrationErr, config.ErrReviewAttentionLegacyMigrationExceedsBounds) {
		t.Fatalf("NamedRuleSets() error = %v, want migration bounds", migrationErr)
	}

	source, err := configuredReviewAttentionPolicySourceForConfig(attention)
	if err != nil {
		t.Fatalf("configuredReviewAttentionPolicySourceForConfig() error = %v", err)
	}
	var snapshot reviews.AttentionPolicySnapshot
	err = source.WithReviewAttentionPolicy(
		context.Background(),
		reviews.AttentionPolicySelector{
			Repository: "owner/repository-1023", DecisionPoint: "review.ready",
		},
		func(_ context.Context, selected reviews.AttentionPolicySnapshot) error {
			snapshot = selected
			return nil
		},
	)
	if err != nil {
		t.Fatalf("WithReviewAttentionPolicy() error = %v", err)
	}
	if len(snapshot.Global) != 9 || snapshot.Repository == nil ||
		len(snapshot.Repository.Gates) != 1 ||
		snapshot.Repository.Gates[0].ID != "repository_1023" {
		t.Fatalf("selected legacy snapshot = %#v", snapshot)
	}
}

func TestEventAutomationPreservesInjectedAttentionPolicySource(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "eventing", "events.db"),
		true,
		true,
	)
	// The invalid configured value proves the lower-level constructor does not
	// replace or even consult a generation-fenced source supplied by its caller.
	cfg.Reviews.Attention.Repositories = map[string]map[string]workflows.RepositoryGatePolicy{
		"not-a-repository": {},
	}
	service, err := newEventAutomationServiceWithReviews(
		context.Background(),
		cfg,
		&workflows.Executor{WorkspaceDir: workspace},
		nil,
		nil,
		eventReviewRuntime{attentionPolicies: gatewayAttentionPolicySource{}},
	)
	if err != nil {
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
	}
	if service == nil || service.reviewAttention == nil {
		t.Fatal("newEventAutomationServiceWithReviews() did not preserve the injected source")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = service.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestValidateConfiguredReviewAttentionAgents(t *testing.T) {
	tests := []struct {
		name          string
		kind          workflows.GateKind
		agentID       string
		withRuntime   bool
		clearSessions bool
		wantErrorPart string
	}{
		{
			name: "empty source needs no runtime",
		},
		{
			name:          "AI source needs runtime",
			kind:          workflows.GateAIIsolatedContext,
			agentID:       "reviewer",
			wantErrorPart: "require the agent runtime",
		},
		{
			name:          "unknown agent is rejected",
			kind:          workflows.GateAIIsolatedContext,
			agentID:       "missing",
			withRuntime:   true,
			wantErrorPart: `agent "missing" is unavailable`,
		},
		{
			name:          "isolated agent needs no durable session",
			kind:          workflows.GateAIIsolatedContext,
			agentID:       "reviewer",
			withRuntime:   true,
			clearSessions: true,
		},
		{
			name:          "working agent needs durable session",
			kind:          workflows.GateAIWorkingContext,
			agentID:       "reviewer",
			withRuntime:   true,
			clearSessions: true,
			wantErrorPart: "has no session store",
		},
		{
			name:        "working agent with session is accepted",
			kind:        workflows.GateAIWorkingContext,
			agentID:     "reviewer",
			withRuntime: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			global := map[string][]workflows.GateSpec{}
			if test.kind != "" {
				global["before_submit"] = []workflows.GateSpec{{
					ID:       "attention",
					Kind:     test.kind,
					AgentID:  test.agentID,
					Criteria: "Ask only when operator judgment is required.",
					Title:    "Operator attention",
				}}
			}
			source, err := reviews.NewConfigAttentionPolicySource(global, nil)
			if err != nil {
				t.Fatalf("NewConfigAttentionPolicySource() error = %v", err)
			}
			var agentLoop *agent.AgentLoop
			if test.withRuntime {
				agentLoop = newAttentionPolicyTestAgentLoop(t)
				if test.clearSessions {
					runtimeAgent, ok := agentLoop.GetRegistry().GetAgent("reviewer")
					if !ok {
						t.Fatal("reviewer runtime is missing")
					}
					runtimeAgent.Sessions = nil
				}
			}
			err = validateConfiguredReviewAttentionAgents(source, agentLoop)
			if test.wantErrorPart == "" {
				if err != nil {
					t.Fatalf("validateConfiguredReviewAttentionAgents() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErrorPart) {
				t.Fatalf(
					"validateConfiguredReviewAttentionAgents() error = %v, want %q",
					err,
					test.wantErrorPart,
				)
			}
		})
	}
}

func newAttentionPolicyTestAgentLoop(t *testing.T) *agent.AgentLoop {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP.Enabled = false
	cfg.Agents.List = []config.AgentConfig{{ID: "reviewer", Default: true}}
	messageBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(
		cfg,
		messageBus,
		&startupBlockedProvider{reason: "not used"},
	)
	t.Cleanup(func() {
		agentLoop.Stop()
		messageBus.Close()
		agentLoop.Close()
	})
	return agentLoop
}

type gatewayAttentionPolicySource struct{}

func (gatewayAttentionPolicySource) WithReviewAttentionPolicy(
	ctx context.Context,
	_ reviews.AttentionPolicySelector,
	consume reviews.AttentionPolicyUse,
) error {
	return consume(ctx, reviews.AttentionPolicySnapshot{})
}
