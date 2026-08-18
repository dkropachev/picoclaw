package prworkspace

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

const (
	testCandidateHunk = "@@ -1,5 +1,5 @@"
	testCandidateDiff = "diff --git a/pkg/retry.go b/pkg/retry.go\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/pkg/retry.go\n" +
		"+++ b/pkg/retry.go\n" +
		testCandidateHunk + "\n" +
		"-old one\n-old two\n-old three\n-old four\n-old five\n" +
		"+new one\n+new two\n+new three\n+new four\n+new five\n"
)

type serviceProvider struct{ snapshot ProviderSnapshot }

func (provider serviceProvider) ResolvePullRequest(context.Context, ResolveRequest) (ProviderSnapshot, error) {
	return provider.snapshot, nil
}

type serviceReviewEvidence struct{}

func (serviceReviewEvidence) LoadReviewEvidence(_ context.Context, snapshot ProviderSnapshot) (ReviewEvidence, error) {
	return ReviewEvidence{
		ProviderRevision: snapshot.ProviderRevision, BaseSHA: snapshot.BaseSHA,
		HeadSHA: snapshot.HeadSHA, UnifiedDiff: "diff --git a/retry.go b/retry.go\n",
	}, nil
}

type serviceAI struct{}

func (serviceAI) RunIsolated(_ context.Context, request IsolatedAIRequest) (map[string]any, error) {
	switch request.Operation {
	case "charter.draft":
		return map[string]any{
			"type": "fix", "goal": "Fix retry handling",
			"acceptance_criteria": []any{"Retries once"}, "included_areas": []any{"pkg/retry"},
			"excluded_areas": []any{"unrelated cleanup"}, "non_goals": []any{"new feature"},
		}, nil
	case "nudge.plan":
		return nil, context.Canceled
	case "completion.initial", "completion.nudge":
		return map[string]any{
			"summary": "Complete", "complete": true,
			"missing_in_scope": []any{}, "out_of_scope": []any{},
			"coverage": map[string]any{"reviewed_areas": []any{}, "unreviewed_areas": []any{}, "tests_considered": []any{}, "residual_risks": []any{}},
		}, nil
	case "scope.audit":
		return map[string]any{
			"changes": []any{map[string]any{
				"path": "pkg/retry.go", "hunk": testCandidateHunk, "module": "pkg/retry", "semantic_lines": 10,
				"presence": "candidate_present", "scope_distance": "S0_exact", "change_size": "XS",
				"type_compatible": true, "confidence": 1.0, "charter_clauses": []any{"goal"}, "explanation": "exact charter work",
			}},
			"files": 1, "semantic_lines": 10, "modules": 1,
			"worst_scope_distance": "S0_exact", "worst_change_size": "XS", "type_compatible": true, "confidence": 1.0,
			"charter_clauses": []any{"goal"}, "explanation": "The candidate is exactly within the confirmed charter.",
		}, nil
	default:
		return map[string]any{
			"summary": "No findings", "findings": []any{},
			"coverage": map[string]any{"reviewed_areas": []any{}, "unreviewed_areas": []any{}, "tests_considered": []any{}, "residual_risks": []any{}},
		}, nil
	}
}

type passingGates struct{}

func (passingGates) Start(_ context.Context, request GateRequest) (GateRun, error) {
	return testSucceededGate(request), nil
}
func (passingGates) Respond(_ context.Context, gate GateRun, fieldValues map[string]any) (GateRun, error) {
	return answerTestGate(gate, fieldValues), nil
}

func TestServiceIntakeCharterConfirmAndZeroFindingNudges(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	provider := ProviderSnapshot{
		Provider: "github", ProviderOrigin: "https://github.com", RepositoryID: "1",
		Repository: "octo/repo", PullRequestID: "2", PullNumber: 3,
		HeadRepositoryID: "1", HeadRepository: "octo/repo",
		BaseSHA: "base", HeadSHA: "head", ObservedAt: now, Owned: true, HeadWritable: true,
	}
	service, err := NewService(ServiceConfig{
		Store: NewMemoryStore(), Provider: serviceProvider{snapshot: provider}, AI: serviceAI{},
		ReviewEvidence: serviceReviewEvidence{}, Gates: passingGates{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := service.Create(context.Background(), CreateWorkspaceRequest{
		RequestID: "request-00000001", Resolve: ResolveRequest{PullRequestURL: "https://github.com/octo/repo/pull/3"},
	})
	if err != nil || aggregate.Workspace.Phase != PhaseCharter {
		t.Fatalf("create = %#v, %v", aggregate, err)
	}
	aggregate, err = service.DraftCharter(context.Background(), DraftCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-00000002",
	})
	if err != nil || len(aggregate.Charters) != 1 || aggregate.Charters[0].Confirmed {
		t.Fatalf("draft = %#v, %v", aggregate.Charters, err)
	}
	aggregate, err = service.ConfirmCharter(context.Background(), ConfirmCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, CharterID: aggregate.Charters[0].ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-00000003",
	})
	if err != nil || aggregate.Workspace.Phase != PhaseReview || !aggregate.Charters[0].Confirmed {
		t.Fatalf("confirm = %#v, %v", aggregate, err)
	}
	aggregate, err = service.RunReview(context.Background(), RunReviewRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000004", NudgePolicy: DefaultNudgePolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Workspace.Phase != PhaseTriage || len(aggregate.NudgeRounds) != 2 {
		t.Fatalf("review phase=%q nudges=%d", aggregate.Workspace.Phase, len(aggregate.NudgeRounds))
	}
	for _, round := range aggregate.NudgeRounds {
		if round.MinimumRounds != DefaultNudgePolicy().MinimumAdditionalRounds ||
			round.HardCap != DefaultNudgePolicy().MaximumAdditionalRounds {
			t.Fatalf("persisted nudge policy = min %d cap %d", round.MinimumRounds, round.HardCap)
		}
	}
}

func TestServiceDeferredModeUsesRepositoryGateConfiguration(t *testing.T) {
	lifecycle := config.DefaultPRLifecycleConfig()
	lifecycle.GateConfigs["off"] = config.PRLifecycleGateConfig{
		Name: "No deferred issues", Bindings: []config.PRLifecycleGateBinding{},
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{
			Mode: config.PRLifecycleDeferredIssuesOff,
		},
	}
	lifecycle.GateConfigs["automatic"] = config.PRLifecycleGateConfig{
		Name: "Automatic deferred issues", Bindings: []config.PRLifecycleGateBinding{},
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{
			Mode: config.PRLifecycleDeferredIssuesAutomatic,
		},
	}
	lifecycle.RepositoryAssignments["https://github.com|repo-off"] = "off"
	lifecycle.RepositoryAssignments["https://github.com|repo-automatic"] = "automatic"
	if err := lifecycle.Validate(); err != nil {
		t.Fatal(err)
	}
	resolver := func(providerOrigin, repositoryID string) DeferredIssueMode {
		_, selected, _, err := lifecycle.ConfigForRepository(providerOrigin, repositoryID)
		if err != nil {
			return DeferredIssuesOff
		}
		return DeferredIssueMode(selected.DeferredIssues.Mode)
	}
	service, err := NewService(ServiceConfig{
		Store: NewMemoryStore(), DeferredIssueMode: DeferredIssuesAsk,
		DeferredIssueModeForRepository: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		origin     string
		repository string
		want       DeferredIssueMode
	}{
		{name: "default", origin: "https://github.com", repository: "repo-default", want: DeferredIssuesAsk},
		{name: "assigned off", origin: "HTTPS://GITHUB.COM/", repository: "repo-off", want: DeferredIssuesOff},
		{name: "assigned automatic", origin: "https://github.com/", repository: "repo-automatic", want: DeferredIssuesAutomatic},
	} {
		t.Run(test.name, func(t *testing.T) {
			aggregate := Aggregate{Workspace: Workspace{
				ProviderOrigin: test.origin, RepositoryID: test.repository,
			}}
			if got := service.deferredMode(aggregate); got != test.want {
				t.Fatalf("deferredMode() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestServiceDeferredModeInvalidRepositoryResolutionFailsClosed(t *testing.T) {
	service, err := NewService(ServiceConfig{
		Store: NewMemoryStore(), DeferredIssueMode: DeferredIssuesAsk,
		DeferredIssueModeForRepository: func(string, string) DeferredIssueMode {
			return DeferredIssueMode("invalid")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate := Aggregate{Workspace: Workspace{
		ProviderOrigin: "https://github.com", RepositoryID: "repo",
	}}
	if got := service.deferredMode(aggregate); got != DeferredIssuesOff {
		t.Fatalf("invalid repository policy resolved to %q, want fail-closed off", got)
	}
}

func TestMissingAuthorizationConfigCreatesHumanGate(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, _ := NewService(ServiceConfig{Store: NewMemoryStore(), Now: func() time.Time { return now }})
	aggregate := Aggregate{Workspace: Workspace{ID: "prw_11111111111111111111111111111111"}}
	gate, err := service.startGate(context.Background(), aggregate, "pr.charter.confirm", map[string]any{"x": "y"})
	if err != nil {
		t.Fatal(err)
	}
	if gate.State != ExecutionWaitingUser || len(gate.Turns) != 1 || gate.Turns[0].Kind != "human" {
		t.Fatalf("fallback gate = %#v", gate)
	}
}
