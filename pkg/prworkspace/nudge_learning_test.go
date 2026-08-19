package prworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type failedReviewNudgeAI struct{}

func (failedReviewNudgeAI) RunIsolated(_ context.Context, request IsolatedAIRequest) (map[string]any, error) {
	switch request.Operation {
	case "nudge.plan":
		return nil, context.Canceled
	case "review.nudge":
		return nil, errors.New("isolated model unavailable")
	case "review.initial":
		return map[string]any{
			"summary": "initial complete", "findings": []any{},
			"coverage": map[string]any{
				"reviewed_areas": []any{}, "unreviewed_areas": []any{},
				"tests_considered": []any{}, "residual_risks": []any{},
			},
		}, nil
	default:
		return nil, errors.New("unexpected operation")
	}
}

func TestRunReviewPersistsFailedNudgeAttempt(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), testCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	charter := Charter{
		ID: stableID("pcr_", created.Aggregate.Workspace.ID, "confirmed"), Type: PRTypeFix,
		Goal: "fix retries", BaseSHA: created.Aggregate.ProviderSnapshot.BaseSHA,
		HeadSHA: created.Aggregate.ProviderSnapshot.HeadSHA, Confirmed: true, CreatedAt: now,
	}
	phase, active := PhaseReview, charter.ID
	ready, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-00000002",
		Patch: AggregatePatch{
			Phase: &phase, ActiveCharterID: &active, AppendCharters: []Charter{charter},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(
		ServiceConfig{
			Store:          store,
			ReviewEvidence: serviceReviewEvidence{},
			AI:             failedReviewNudgeAI{},
			Gates:          passingGates{},
			Now:            func() time.Time { return now },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := service.RunReview(context.Background(), RunReviewRequest{
		WorkspaceID: ready.Aggregate.Workspace.ID, ExpectedVersion: ready.Aggregate.Workspace.Version,
		RequestID: "request-00000003", NudgePolicy: DefaultNudgePolicy(),
	})
	if runErr == nil {
		t.Fatal("failed nudge returned no error")
	}
	if len(result.NudgeRounds) != 1 || result.NudgeRounds[0].State != ExecutionFailed ||
		result.NudgeRounds[0].PublicError != "nudge_ai_failed" {
		t.Fatalf("failed attempt was not durable: %#v", result.NudgeRounds)
	}
	if result.NudgeRounds[0].Reward != nil {
		t.Fatalf("failed attempt gained reward: %#v", result.NudgeRounds[0])
	}
	stored, err := store.Get(context.Background(), result.Workspace.ID)
	if err != nil || len(stored.NudgeRounds) != 1 {
		t.Fatalf("stored attempts = %#v, %v", stored.NudgeRounds, err)
	}
}

func TestFindingOutcomesAndCorrectionsRecomputeDelayedNudgeReward(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), testCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	findingID := stableID("pfn_", created.Aggregate.Workspace.ID, "nudge-finding")
	roundID := stableID("pnr_", created.Aggregate.Workspace.ID, "nudge-round")
	finding := Finding{
		ID: findingID, Fingerprint: "sha256:nudge", Origin: FindingOriginNudge,
		OriginRunID: stableID("psr_", created.Aggregate.Workspace.ID, "review"), NudgeRoundID: roundID,
		Severity: "high", Title: "retry race", Message: "retry races cancellation",
		Scope: ScopeAssessment{
			Distance: ScopeExact, Size: ChangeSizeXS, TypeCompatible: true, Confidence: 1,
		},
		Disposition: FindingOpen, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	round := NudgeRoundRecord{
		ID: roundID, Stage: NudgeReviewSearch, StageRunID: finding.OriginRunID,
		Round: 1, Strategy: NudgeAdversarial, Challenge: "challenge",
		VariantDigest: "sha256:variant", PromptDigest: "sha256:prompt",
		State: ExecutionSucceeded, NovelFindings: 1, FindingIDs: []string{findingID}, CreatedAt: now,
	}
	seeded, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-00000011",
		Patch:     AggregatePatch{UpsertFindings: []Finding{finding}, AppendNudgeRounds: []NudgeRoundRecord{round}},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	dismissed, err := service.DecideFinding(context.Background(), FindingDecisionRequest{
		WorkspaceID: seeded.Aggregate.Workspace.ID, FindingID: findingID,
		ExpectedVersion: seeded.Aggregate.Workspace.Version, RequestID: "request-00000012",
		Disposition: FindingDismissed, Scope: finding.Scope, Reason: "not reproducible",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dismissed.NudgeRounds[0].Reward == nil || *dismissed.NudgeRounds[0].Reward != 0 ||
		dismissed.NudgeRounds[0].ResolvedFindings != 1 {
		t.Fatalf("dismissal reward = %#v", dismissed.NudgeRounds[0])
	}
	accepted, err := service.DecideFinding(context.Background(), FindingDecisionRequest{
		WorkspaceID: dismissed.Workspace.ID, FindingID: findingID,
		ExpectedVersion: dismissed.Workspace.Version, RequestID: "request-00000013",
		Disposition: FindingInScope, Scope: finding.Scope, Reason: "reproduced under race detector",
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.NudgeRounds[0].Reward == nil || *accepted.NudgeRounds[0].Reward != .25 ||
		accepted.NudgeRounds[0].ResolvedFindings != 1 {
		t.Fatalf("re-adjudication double-counted or stale: %#v", accepted.NudgeRounds[0])
	}
	corrected, err := service.AddCorrection(context.Background(), AddCorrectionRequest{
		WorkspaceID: accepted.Workspace.ID, ExpectedVersion: accepted.Workspace.Version,
		RequestID: "request-00000014",
		Correction: Correction{
			Kind: CorrectionFactual, Applicability: CorrectionReviewAndImpl,
			TargetType: "finding", TargetID: findingID, OriginalClaim: "always races",
			Correction: "races only after cancellation", Evidence: "trace",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if corrected.NudgeRounds[0].Reward == nil || *corrected.NudgeRounds[0].Reward != 0 ||
		corrected.Findings[0].RewardSource != "user_correction:factual" {
		t.Fatalf("correction reward = %#v finding=%#v", corrected.NudgeRounds[0], corrected.Findings[0])
	}
}

func TestRunReviewSelectsFromDurableWorkspaceLearning(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), testCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	charter := Charter{
		ID: stableID("pcr_", created.Aggregate.Workspace.ID, "learned"), Type: PRTypeFix,
		Goal: "fix retries", HeadSHA: created.Aggregate.ProviderSnapshot.HeadSHA,
		Confirmed: true, CreatedAt: now,
	}
	rounds := make([]NudgeRoundRecord, 0, len(nudgeStrategies))
	for index, strategy := range nudgeStrategies {
		reward := .1
		if strategy == NudgeValidation {
			reward = 1
		}
		rounds = append(rounds, NudgeRoundRecord{
			ID:    stableID("pnr_", created.Aggregate.Workspace.ID, "history", string(strategy)),
			Stage: NudgeReviewSearch, Round: index + 1, Strategy: strategy,
			State: ExecutionSucceeded, Reward: float64Pointer(reward), CreatedAt: now,
		})
	}
	phase, active := PhaseReview, charter.ID
	ready, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-00000021",
		Patch: AggregatePatch{
			Phase: &phase, ActiveCharterID: &active, AppendCharters: []Charter{charter}, AppendNudgeRounds: rounds,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(
		ServiceConfig{
			Store:          store,
			ReviewEvidence: serviceReviewEvidence{},
			AI:             serviceAI{},
			Gates:          passingGates{},
			Now:            func() time.Time { return now },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunReview(context.Background(), RunReviewRequest{
		WorkspaceID: ready.Aggregate.Workspace.ID, ExpectedVersion: ready.Aggregate.Workspace.Version,
		RequestID: "request-00000022", NudgePolicy: NudgePolicy{MinimumAdditionalRounds: 1, MaximumAdditionalRounds: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	latest := result.NudgeRounds[len(result.NudgeRounds)-1]
	if latest.Strategy != NudgeValidation {
		t.Fatalf("selected strategy = %q, want durable winner %q", latest.Strategy, NudgeValidation)
	}
	if !strings.Contains(latest.Challenge, "variant 2") {
		t.Fatalf("historical wording variant was reused: %q", latest.Challenge)
	}
}

func TestLinkingDeferredIssueConfirmsNudgeReward(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), testCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	findingID := stableID("pfn_", created.Aggregate.Workspace.ID, "deferred-nudge")
	roundID := stableID("pnr_", created.Aggregate.Workspace.ID, "deferred-nudge")
	groupID := stableID("pdg_", created.Aggregate.Workspace.ID, "deferred-group")
	finding := Finding{
		ID: findingID, Fingerprint: "sha256:deferred", Origin: FindingOriginNudge,
		NudgeRoundID: roundID, Severity: "medium", Title: "follow-up", Message: "adjacent cleanup",
		Scope: ScopeAssessment{
			Distance: ScopeRelatedFollowup, Size: ChangeSizeS, TypeCompatible: true, Confidence: .8,
		},
		Disposition: FindingDeferred, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	seeded, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-00000031",
		Patch: AggregatePatch{
			UpsertFindings: []Finding{finding},
			AppendNudgeRounds: []NudgeRoundRecord{{
				ID: roundID, Stage: NudgeImplementationDone, State: ExecutionSucceeded,
				Round: 1, Strategy: NudgeCoverageGaps, FindingIDs: []string{findingID}, CreatedAt: now,
			}},
			UpsertDeferred: []DeferredGroup{{
				ID: groupID, Title: "follow-up", Body: "track it", FindingIDs: []string{findingID},
				Version: 1, CreatedAt: now, UpdatedAt: now,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	linked, err := service.LinkDeferred(context.Background(), LinkDeferredRequest{
		WorkspaceID: seeded.Aggregate.Workspace.ID, GroupID: groupID,
		ExpectedVersion: seeded.Aggregate.Workspace.Version, RequestID: "request-00000032",
		ExistingIssueURL: "https://github.com/octo/repo/issues/42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if linked.NudgeRounds[0].Reward == nil || *linked.NudgeRounds[0].Reward != .75 ||
		linked.Findings[0].RewardSource != "user_linked_deferred_issue" {
		t.Fatalf("deferred reward = %#v finding=%#v", linked.NudgeRounds[0], linked.Findings[0])
	}
}
