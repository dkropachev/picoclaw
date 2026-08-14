package prworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type implementationRepair struct {
	calls int
	last  RepairRequest
}

type failedImplementationRepair struct{}

func (failedImplementationRepair) Repair(context.Context, RepairRequest) (RepairResult, error) {
	return RepairResult{}, errors.New("private repair failure")
}

func (repair *implementationRepair) Repair(_ context.Context, request RepairRequest) (RepairResult, error) {
	repair.calls++
	repair.last = request
	return RepairResult{
		Summary: "fixed", WorkspaceID: "local", ChangedFiles: []string{"pkg/retry.go"},
		SemanticLines: 10, Modules: 1, CandidateSHA: "candidate", CandidateDiff: testCandidateDiff,
		PromptDigest: "sha256:repair",
	}, nil
}

func TestImplementationRepairReceivesSharedCurrentGuidanceAndSeparatePromptDigests(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	now := aggregate.Workspace.UpdatedAt
	message := Message{
		ID: "pms_11111111111111111111111111111111", Role: "user", Stage: "both",
		Content: "preserve retry ordering", CharterID: aggregate.Workspace.ActiveCharterID,
		HeadSHA: aggregate.ProviderSnapshot.HeadSHA, CreatedAt: now,
	}
	correction := Correction{
		ID: "pco_11111111111111111111111111111111", Kind: CorrectionImplementation,
		Applicability: CorrectionReviewAndImpl, TargetType: "workspace", TargetID: aggregate.Workspace.ID,
		OriginalClaim: "reorder callbacks", Correction: "preserve callback order",
		CharterID: aggregate.Workspace.ActiveCharterID, HeadSHA: aggregate.ProviderSnapshot.HeadSHA, CreatedAt: now,
	}
	lesson := RepositoryLesson{
		ID: "prl_11111111111111111111111111111111", RepositoryID: aggregate.Workspace.RepositoryID,
		Kind: CorrectionRepositoryPreference, Applicability: CorrectionReviewAndImpl,
		PRType: PRTypeFix, Text: "retry ordering is stable API", Active: true, CreatedAt: now,
	}
	seeded, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000060", Patch: AggregatePatch{
			AppendMessages: []Message{message}, AppendCorrections: []Correction{correction}, AppendLessons: []RepositoryLesson{lesson},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	repair := &implementationRepair{}
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: repair, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: seeded.Aggregate.Workspace.ID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
		RequestID: "request-00000061", FindingIDs: []string{seeded.Aggregate.Findings[0].ID},
		NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repair.last.Context.Messages) != 1 || len(repair.last.Context.Corrections) != 1 ||
		len(repair.last.Context.RepositoryLessons) != 1 || len(repair.last.AuthorizedFindingIDs) != 1 {
		t.Fatalf("repair context = %#v", repair.last)
	}
	if len(result.RepairAttempts) != 1 || result.RepairAttempts[0].PromptDigest != "sha256:repair" ||
		result.RepairAttempts[0].ScopePromptDigest == "" || result.RepairAttempts[0].ScopePromptDigest == result.RepairAttempts[0].PromptDigest {
		t.Fatalf("repair prompt evidence = %#v", result.RepairAttempts)
	}
}

type implementationValidation struct{}

func (implementationValidation) Validate(_ context.Context, _ ValidationRequest) (ValidationRun, error) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	return ValidationRun{State: ExecutionSucceeded, CandidateSHA: "candidate", Checks: []ValidationCheck{{ID: "test", Name: "tests", Status: "passed"}}, StartedAt: now, FinishedAt: &now}, nil
}

type infrastructureImplementationValidation struct{}

func (infrastructureImplementationValidation) Validate(_ context.Context, _ ValidationRequest) (ValidationRun, error) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	return ValidationRun{
		State: ExecutionFailed, CandidateSHA: "candidate",
		Checks:    []ValidationCheck{{ID: "test", Name: "tests", Status: "infrastructure_error"}},
		StartedAt: now, FinishedAt: &now,
	}, nil
}

type mismatchedImplementationValidation struct{}

func (mismatchedImplementationValidation) Validate(_ context.Context, _ ValidationRequest) (ValidationRun, error) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	return ValidationRun{
		ID: "pvr_ffffffffffffffffffffffffffffffff", State: ExecutionSucceeded,
		CandidateSHA: "another-candidate",
		Checks:       []ValidationCheck{{ID: "test", Name: "tests", Status: "passed"}},
		StartedAt:    now, FinishedAt: &now,
	}, nil
}

func TestImplementationRejectsValidationEvidenceForAnotherAttemptOrCandidate(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	repair := &implementationRepair{}
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: repair, Validation: mismatchedImplementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-validation-mismatch", FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	stage := result.StageRuns[len(result.StageRuns)-1]
	if repair.calls != 1 || stage.State != ExecutionFailed || stage.PublicError != "validation_evidence_mismatch" {
		t.Fatalf("mismatched validation result = repairs %d, stage %#v", repair.calls, stage)
	}
	if len(result.ValidationRuns) != 1 || result.ValidationRuns[0].CandidateSHA != "candidate" ||
		result.ValidationRuns[0].ID == "pvr_ffffffffffffffffffffffffffffffff" {
		t.Fatalf("persisted validation identity = %#v", result.ValidationRuns)
	}
}

type statusImplementationValidation struct {
	status string
	calls  int
	seen   []ValidationRequest
}

func (validation *statusImplementationValidation) Validate(_ context.Context, request ValidationRequest) (ValidationRun, error) {
	validation.calls++
	validation.seen = append(validation.seen, request)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	return ValidationRun{
		ID: request.ID, State: ExecutionFailed, CandidateSHA: request.CandidateSHA,
		Checks: []ValidationCheck{{
			ID: "local-ci", Name: "Local CI", Status: validation.status,
			Summary: "validation evidence " + strings.Repeat("detail ", 6000),
		}},
		StartedAt: now, FinishedAt: &now,
	}, nil
}

func TestImplementationStopsOnValidationInfrastructureFailureWithoutRepairingCodeAgain(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	repair := &implementationRepair{}
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: repair, Validation: infrastructureImplementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000062", FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	stage := result.StageRuns[len(result.StageRuns)-1]
	if repair.calls != 1 || len(result.RepairAttempts) != 1 || len(result.ValidationRuns) != 1 {
		t.Fatalf("infrastructure failure retried code: repairs=%d attempts=%d validations=%d", repair.calls, len(result.RepairAttempts), len(result.ValidationRuns))
	}
	if stage.State != ExecutionFailed || stage.PublicError != "validation_infrastructure_failed" {
		t.Fatalf("implementation stage = %#v", stage)
	}
	for _, finding := range result.Findings {
		if finding.Title == "Validation remains non-green" {
			t.Fatalf("infrastructure failure became a code finding: %#v", finding)
		}
	}
}

func TestImplementationInfrastructureRetryGetsNewStableValidationAttempt(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	repair := &implementationRepair{}
	validation := &statusImplementationValidation{status: "infrastructure_error"}
	firstRequestID := "request-infrastructure-first"
	first, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: repair, Validation: validation,
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: firstRequestID, FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondRequestID := "request-infrastructure-second"
	second, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: repair, Validation: validation,
	}, RunImplementationRequest{
		WorkspaceID: first.Workspace.ID, ExpectedVersion: first.Workspace.Version,
		RequestID: secondRequestID, FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(validation.seen) != 2 || validation.seen[0].ID == validation.seen[1].ID ||
		validation.seen[0].ID != stableID("pvr_", aggregate.Workspace.ID, firstRequestID, "1") ||
		validation.seen[1].ID != stableID("pvr_", aggregate.Workspace.ID, secondRequestID, "1") {
		t.Fatalf("infrastructure retry validation IDs = %#v", validation.seen)
	}
	if len(second.ValidationRuns) != 2 || second.ValidationRuns[0].ID == second.ValidationRuns[1].ID {
		t.Fatalf("durable validation attempts = %#v", second.ValidationRuns)
	}
}

func TestImplementationValidationDefinitionStatusesBlockWithoutCodeFinding(t *testing.T) {
	tests := []struct {
		status      string
		publicError string
	}{
		{status: "incomplete", publicError: "validation_plan_incomplete"},
		{status: "plan_changed", publicError: "validation_plan_changed"},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			service, aggregate := readyImplementationService(t)
			repair := &implementationRepair{}
			validation := &statusImplementationValidation{status: test.status}
			result, err := service.RunImplementation(context.Background(), ImplementationConfig{
				Repair: repair, Validation: validation,
			}, RunImplementationRequest{
				WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
				RequestID:  "request-definition-status-" + test.status,
				FindingIDs: []string{aggregate.Findings[0].ID},
			})
			if err != nil {
				t.Fatal(err)
			}
			stage := result.StageRuns[len(result.StageRuns)-1]
			if repair.calls != 1 || validation.calls != 1 || stage.State != ExecutionBlocked ||
				stage.PublicError != test.publicError {
				t.Fatalf("definition blocker result = stage %#v, repairs %d, validations %d", stage, repair.calls, validation.calls)
			}
			for _, finding := range result.Findings {
				if finding.Title == "Validation remains non-green" {
					t.Fatalf("definition blocker became a code finding: %#v", finding)
				}
			}
			if len(validation.seen) != 1 || validation.seen[0].ID == "" || result.ValidationRuns[0].ID != validation.seen[0].ID {
				t.Fatalf("validation attempt identity was not assigned before execution: %#v / %#v", validation.seen, result.ValidationRuns)
			}
		})
	}
}

type failThenPassImplementationValidation struct {
	calls int
	seen  []ValidationRequest
}

func (validation *failThenPassImplementationValidation) Validate(_ context.Context, request ValidationRequest) (ValidationRun, error) {
	validation.calls++
	validation.seen = append(validation.seen, request)
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	status, state, summary := "failed", ExecutionFailed, "compile failed: missing RetryPolicy"
	if validation.calls > 1 {
		status, state, summary = "passed", ExecutionSucceeded, "ok"
	}
	return ValidationRun{
		ID: request.ID, State: state, CandidateSHA: request.CandidateSHA,
		Checks:    []ValidationCheck{{ID: "go-test", Name: "go test", Status: status, Summary: summary}},
		StartedAt: now, FinishedAt: &now,
	}, nil
}

func TestImplementationFeedsBoundedValidationEvidenceIntoNextRepair(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	repair := &implementationRepair{}
	validation := &failThenPassImplementationValidation{}
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: repair, Validation: validation,
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-validation-evidence", FindingIDs: []string{aggregate.Findings[0].ID},
		NudgePolicy: ConfiguredNudgePolicy(0, 0), MaxCycles: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repair.calls != 2 || validation.calls != 2 || len(result.ValidationRuns) != 2 {
		t.Fatalf("repair/validation cycles = %d/%d, runs=%d", repair.calls, validation.calls, len(result.ValidationRuns))
	}
	if repair.last.Context.Validation == nil {
		t.Fatal("second repair did not receive validation evidence")
	}
	encoded, err := json.Marshal(repair.last.Context.Validation)
	if err != nil || len(encoded) > maxRepairValidationSummaryBytes+4096 || !strings.Contains(string(encoded), "missing RetryPolicy") {
		t.Fatalf("bounded validation evidence = %d bytes, %v, %s", len(encoded), err, encoded)
	}
	if validation.seen[0].ID == "" || validation.seen[0].ID == validation.seen[1].ID {
		t.Fatalf("validation attempt IDs = %#v", validation.seen)
	}
}

type blockingImplementationRepair struct {
	implementationRepair
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (repair *blockingImplementationRepair) Repair(ctx context.Context, request RepairRequest) (RepairResult, error) {
	repair.once.Do(func() { close(repair.started) })
	select {
	case <-repair.release:
	case <-ctx.Done():
		return RepairResult{}, ctx.Err()
	}
	return repair.implementationRepair.Repair(ctx, request)
}

func TestImplementationClaimsWorkspaceBeforeSideEffects(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	secondService, err := NewService(ServiceConfig{
		Store: service.store, AI: serviceAI{}, Gates: passingGates{},
		Now: func() time.Time { return aggregate.Workspace.UpdatedAt },
	})
	if err != nil {
		t.Fatal(err)
	}
	repair := &blockingImplementationRepair{started: make(chan struct{}), release: make(chan struct{})}
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.RunImplementation(context.Background(), ImplementationConfig{
			Repair: repair, Validation: implementationValidation{},
		}, RunImplementationRequest{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID: "request-concurrent-first", FindingIDs: []string{aggregate.Findings[0].ID},
			NudgePolicy: ConfiguredNudgePolicy(0, 0),
		})
		firstDone <- err
	}()
	<-repair.started
	_, secondErr := secondService.RunImplementation(context.Background(), ImplementationConfig{
		Repair: repair, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-concurrent-second", FindingIDs: []string{aggregate.Findings[0].ID},
		NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if !errors.Is(secondErr, ErrConflict) {
		t.Fatalf("concurrent RunImplementation() error = %v, want ErrConflict", secondErr)
	}
	close(repair.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if repair.calls != 1 {
		t.Fatalf("side-effecting repair calls = %d, want 1", repair.calls)
	}
}

type failedCompletionNudgeAI struct{}

type aggregateOrderedFinalizer struct {
	implementationRepair
	store        Store
	acknowledged bool
}

func (repair *aggregateOrderedFinalizer) FinalizeRepair(
	_ context.Context,
	workspaceID string,
	result RepairResult,
) (RepairResult, error) {
	if workspaceID == "" {
		return result, errors.New("missing workspace identity")
	}
	result.PublicationFence = &ImplementationPublicationFence{
		BaseCommit: "base", Tip: result.CandidateSHA, Tree: result.CandidateSHA,
	}
	return result, nil
}

func (repair *aggregateOrderedFinalizer) AcknowledgeFinalizedRepair(
	ctx context.Context,
	workspaceID string,
	result RepairResult,
) error {
	aggregate, err := repair.store.Get(ctx, workspaceID)
	if err != nil {
		return err
	}
	for _, attempt := range aggregate.RepairAttempts {
		if attempt.CandidateSHA == result.CandidateSHA && attempt.PublicationFence != nil &&
			attempt.PublicationFence.Tip == result.PublicationFence.Tip {
			repair.acknowledged = true
			return nil
		}
	}
	return errors.New("finalization was acknowledged before aggregate persistence")
}

func TestImplementationAcknowledgesFinalizationOnlyAfterAggregateMutation(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	repair := &aggregateOrderedFinalizer{store: service.store}
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: repair, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-finalization-ack-order", FindingIDs: []string{aggregate.Findings[0].ID},
		NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !repair.acknowledged || len(result.RepairAttempts) != 1 || result.RepairAttempts[0].PublicationFence == nil {
		t.Fatalf("finalization acknowledgement/result = %v / %#v", repair.acknowledged, result.RepairAttempts)
	}
}

func (failedCompletionNudgeAI) RunIsolated(_ context.Context, request IsolatedAIRequest) (map[string]any, error) {
	switch request.Operation {
	case "scope.audit":
		return map[string]any{
			"changes": []any{map[string]any{
				"path": "pkg/retry.go", "hunk": testCandidateHunk, "module": "pkg/retry", "semantic_lines": 10,
				"presence": "candidate_present", "scope_distance": "S0_exact", "change_size": "XS",
				"type_compatible": true, "confidence": 1.0, "charter_clauses": []any{"goal"}, "explanation": "exact charter work",
			}},
			"files": 1, "semantic_lines": 10, "modules": 1,
			"worst_scope_distance": "S0_exact", "worst_change_size": "XS", "type_compatible": true, "confidence": 1.0,
			"charter_clauses": []any{"goal"}, "explanation": "exact charter work",
		}, nil
	case "nudge.plan":
		return nil, context.Canceled
	case "completion.nudge":
		return nil, errors.New("completion nudge unavailable")
	case "completion.initial":
		return map[string]any{
			"summary": "complete", "complete": true,
			"missing_in_scope": []any{}, "out_of_scope": []any{},
			"coverage": map[string]any{
				"reviewed_areas": []any{}, "unreviewed_areas": []any{},
				"tests_considered": []any{}, "residual_risks": []any{},
			},
		}, nil
	default:
		return nil, errors.New("unexpected operation")
	}
}

func TestImplementationRunsRepairValidationAndCompletionNudges(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	repair := &implementationRepair{}
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: repair, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000020", FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if repair.calls != 1 || result.Workspace.Phase != PhasePublication ||
		len(result.RepairAttempts) != 1 || len(result.ValidationRuns) != 1 || len(result.NudgeRounds) != 2 {
		t.Fatalf("result phase=%q repair=%d validation=%d nudges=%d", result.Workspace.Phase, len(result.RepairAttempts), len(result.ValidationRuns), len(result.NudgeRounds))
	}
	if result.Findings[0].Disposition != FindingFixed {
		t.Fatalf("finding disposition = %q", result.Findings[0].Disposition)
	}
}

func TestImplementationFailureActivityDoesNotClaimItFinished(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: failedImplementationRepair{}, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000024", FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	stage := result.StageRuns[len(result.StageRuns)-1]
	activity := result.Activity[len(result.Activity)-1]
	if stage.State != ExecutionFailed || stage.PublicError != "repair_failed" {
		t.Fatalf("implementation stage = %#v", stage)
	}
	if activity.Summary != "Implementation stopped before a publishable candidate was ready" {
		t.Fatalf("implementation activity = %#v", activity)
	}
}

func TestGreenValidationRewardsOriginatingNudge(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	finding := aggregate.Findings[0]
	roundID := stableID("pnr_", aggregate.Workspace.ID, "review-nudge")
	finding.NudgeRoundID = roundID
	finding.Origin = FindingOriginNudge
	round := NudgeRoundRecord{
		ID: roundID, StageRunID: stableID("psr_", aggregate.Workspace.ID, "review"),
		Stage: NudgeReviewSearch, Round: 1, Strategy: NudgeAdversarial,
		Challenge: "find retry races", VariantDigest: "sha256:variant", PromptDigest: "sha256:prompt",
		State: ExecutionSucceeded, NovelFindings: 1, FindingIDs: []string{finding.ID}, CreatedAt: finding.CreatedAt,
	}
	seeded, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000019",
		Patch:     AggregatePatch{UpsertFindings: []Finding{finding}, AppendNudgeRounds: []NudgeRoundRecord{round}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: &implementationRepair{}, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: seeded.Aggregate.Workspace.ID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
		RequestID: "request-00000020", FindingIDs: []string{finding.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range result.NudgeRounds {
		if candidate.ID != roundID {
			continue
		}
		if candidate.Reward == nil || *candidate.Reward != 1 || candidate.ResolvedFindings != 1 {
			t.Fatalf("validation reward = %#v", candidate)
		}
		return
	}
	t.Fatalf("originating nudge round missing: %#v", result.NudgeRounds)
}

func TestImplementationPersistsFailedCompletionNudge(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	service.ai.Runner = failedCompletionNudgeAI{}
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: &implementationRepair{}, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000023", FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err == nil {
		t.Fatal("failed completion nudge returned no error")
	}
	if len(result.NudgeRounds) != 1 || result.NudgeRounds[0].State != ExecutionFailed ||
		result.NudgeRounds[0].PublicError != "nudge_ai_failed" {
		t.Fatalf("failed completion attempt = %#v", result.NudgeRounds)
	}
	if len(result.RepairAttempts) != 1 || len(result.ValidationRuns) != 1 {
		t.Fatalf("repair evidence lost: repairs=%d validation=%d", len(result.RepairAttempts), len(result.ValidationRuns))
	}
}

func readyImplementationService(t *testing.T) (*Service, Aggregate) {
	t.Helper()
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	input := testCreateInput()
	input.Provider.Owned = true
	input.Provider.HeadWritable = true
	_, err := store.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	charter := Charter{ID: "pcr_11111111111111111111111111111111", Type: PRTypeFix, Goal: "fix", BaseSHA: input.Provider.BaseSHA, HeadSHA: input.Provider.HeadSHA, Confirmed: true, CreatedAt: now}
	reviewStage := StageRun{
		ID: "psr_11111111111111111111111111111111", Stage: "review", State: ExecutionSucceeded,
		CharterID: charter.ID, HeadSHA: charter.HeadSHA, Attempt: 1, StartedAt: now, FinishedAt: &now,
	}
	finding := Finding{
		ID: "pfn_11111111111111111111111111111111", Fingerprint: "sha256:finding",
		Origin: FindingOriginReview, OriginRunID: reviewStage.ID, Severity: "high", Title: "retry bug", Message: "fix retry",
		Scope:       ScopeAssessment{Distance: ScopeExact, Size: ChangeSizeXS, TypeCompatible: true, Confidence: 1},
		Disposition: FindingInScope, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	active := charter.ID
	phase := PhaseTriage
	mutated, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: 1, RequestID: "request-00000010",
		Patch: AggregatePatch{
			Phase: &phase, ActiveCharterID: &active, AppendCharters: []Charter{charter},
			AppendStageRuns: []StageRun{reviewStage}, UpsertFindings: []Finding{finding},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Store: store, AI: serviceAI{}, Gates: passingGates{}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return service, mutated.Aggregate
}
