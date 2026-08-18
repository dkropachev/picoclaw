//go:build !mipsle && !netbsd && !(freebsd && arm)

package prworkspace

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/stretchr/testify/require"
)

func TestEventingStoreConfiguredFallbackGateRoundTripsAndResponds(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	raw, err := eventing.Open(ctx, filepath.Join(t.TempDir(), "fallback-gate.sqlite"), eventing.WithClock(func() time.Time { return now }))
	require.NoError(t, err)
	store := NewEventingStore(raw)

	input := testCreateInput()
	input.Provider.BaseSHA = "base-commit"
	input.Provider.HeadRepositoryID = input.Provider.RepositoryID
	input.Provider.HeadRepository = input.Provider.Repository
	created, err := store.Create(ctx, input)
	require.NoError(t, err)
	charter := charterInvariantRecord(input, "durable-fallback", 1, now)
	charterPhase := PhaseCharter
	seeded, err := store.Mutate(ctx, Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-seed-durable-fallback-charter",
		Patch:     AggregatePatch{Phase: &charterPhase, AppendCharters: []Charter{charter}},
	})
	require.NoError(t, err)

	service, err := NewService(ServiceConfig{Store: store, Now: func() time.Time { return now }})
	require.NoError(t, err)

	waiting, err := service.ConfirmCharter(ctx, ConfirmCharterRequest{
		WorkspaceID: input.Workspace.ID, CharterID: charter.ID,
		ExpectedVersion: seeded.Aggregate.Workspace.Version, RequestID: "request-start-durable-fallback-gate",
	})
	require.NoError(t, err)
	require.Len(t, waiting.Gates, 1)
	require.Equal(t, ExecutionWaitingUser, waiting.Gates[0].State)
	require.Nil(t, waiting.Gates[0].runtime)
	require.NotNil(t, waiting.Gates[0].Turns[0].GateForm)

	confirmed, err := service.RespondGate(ctx, RespondGateRequest{
		WorkspaceID: input.Workspace.ID, GateRunID: waiting.Gates[0].ID,
		ExpectedVersion: waiting.Workspace.Version, RequestID: "request-answer-durable-fallback-gate",
		FieldValues: map[string]any{"action": "approve"},
	})
	require.NoError(t, err)
	require.Equal(t, PhaseReview, confirmed.Workspace.Phase)
	require.Equal(t, charter.ID, confirmed.Workspace.ActiveCharterID)
	require.True(t, confirmed.Charters[0].Confirmed)
}

func TestEventingStorePersistsAutomaticDeferredGateV3Metadata(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	raw, err := eventing.Open(ctx, filepath.Join(t.TempDir(), "automatic-gate.sqlite"), eventing.WithClock(func() time.Time { return now }))
	require.NoError(t, err)
	store := NewEventingStore(raw)
	input := testCreateInput()
	input.Provider.BaseSHA = "base-commit"
	input.Provider.HeadRepositoryID = input.Provider.RepositoryID
	input.Provider.HeadRepository = input.Provider.Repository
	created, err := store.Create(ctx, input)
	require.NoError(t, err)
	service := &Service{deferredIssueMode: DeferredIssuesAutomatic, now: func() time.Time { return now }}
	gate, err := service.deferredPublicationGate(ctx, created.Aggregate, map[string]any{
		"group": map[string]any{"id": "pdg_11111111111111111111111111111111"},
	})
	require.NoError(t, err)
	mutated, err := store.Mutate(ctx, Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-persist-automatic-gate-v3", Patch: AggregatePatch{AppendGates: []GateRun{gate}},
	})
	require.NoError(t, err)
	require.Len(t, mutated.Aggregate.Gates, 1)
	persisted := mutated.Aggregate.Gates[0]
	require.True(t, gateCompletedWith(persisted, "publish"))
	require.NotNil(t, persisted.Turns[0].GateForm)
	require.Equal(t, "gates.deferred-publish", persisted.Turns[0].GateForm.GateRef)
	require.NotEmpty(t, persisted.Turns[0].ActionRevision)
}

func TestEventingStoreRoundTripsUnifiedAggregatePrivateStateAndReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 8, 13, 18, 0, 0, 0, time.UTC)
	databasePath := filepath.Join(t.TempDir(), "eventing.sqlite")
	raw, err := eventing.Open(ctx, databasePath, eventing.WithClock(func() time.Time { return now }))
	require.NoError(t, err)
	store := NewEventingStore(raw)

	workspaceID := stableID("prw_", "https://github.example.test", "repo-1", "pull-7")
	provider := ProviderSnapshot{
		Provider: "github", ProviderOrigin: "https://github.example.test",
		RepositoryID: "repo-1", Repository: "octo/project", PullRequestID: "pull-7", PullNumber: 7,
		Title: "Fix the race", AuthorID: "user-1", AuthorLogin: "octo", AuthenticatedUserID: "user-1",
		BaseRef: "main", BaseSHA: "base-commit", HeadRepositoryID: "repo-1",
		HeadRepository: "octo/project", HeadRef: "fix/race", HeadSHA: "head-commit", State: "open",
		Owned: true, HeadWritable: true, CanReview: true, CanCreateIssue: true, ObservedAt: now,
	}
	created, err := store.Create(ctx, CreateInput{
		RequestID: "request-create-0001",
		Workspace: Workspace{
			ID: workspaceID, Provider: "github", ProviderOrigin: provider.ProviderOrigin,
			RepositoryID: provider.RepositoryID, PullRequestID: provider.PullRequestID,
			Repository: provider.Repository, PullNumber: provider.PullNumber,
			Phase: PhaseCharter, ExecutionState: ExecutionWaitingUser,
			ProviderHeadSHA: provider.HeadSHA, Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		Provider: provider,
	})
	require.NoError(t, err)
	require.False(t, created.Replayed)
	replayedCreate, err := store.Create(ctx, CreateInput{
		RequestID: "request-create-0001", Workspace: created.Aggregate.Workspace, Provider: provider,
	})
	require.NoError(t, err)
	require.True(t, replayedCreate.Replayed)

	charterID := stableID("pcr_", workspaceID, "charter")
	stageID := stableID("psr_", workspaceID, "implementation")
	roundID := stableID("pnr_", workspaceID, "nudge")
	findingID := stableID("pfn_", workspaceID, "finding")
	correctionID := stableID("pco_", workspaceID, "correction")
	lessonID := stableID("prl_", provider.RepositoryID, correctionID)
	groupID := stableID("pdg_", workspaceID, "deferred")
	repairID := stableID("pra_", workspaceID, "repair")
	validationID := stableID("pvr_", workspaceID, "validation")
	gateID := stableID("pgr_", workspaceID, "gate")
	publicationID := stableID("ppb_", workspaceID, "publication")
	messageID := stableID("pms_", workspaceID, "message")
	reward := .75
	finished := now.Add(time.Minute)
	policy := json.RawMessage(`{"version":"3","workflow-ref":"workflows/pr-lifecycle.yml","workflow-revision":"sha256:test-workflow","gate-ref":"gates.implementation-complete","config-id":"strict","config-revision":"sha256:test-config","action-revision":"sha256:test-action"}`)
	subject := json.RawMessage(`{"repair_id":"` + repairID + `"}`)
	publicationPayload, publicationDigest, err := encodePublicationPayload(issuePublicationPayload{
		ProviderOrigin: provider.ProviderOrigin, RepositoryID: provider.RepositoryID, Repository: provider.Repository,
		Title: "Adjacent cleanup", Body: "Create a follow-up", Labels: []string{"follow-up"}, FindingIDs: []string{findingID},
	})
	require.NoError(t, err)
	gate := GateRun{
		ID: gateID, DecisionPoint: "pr.implementation.complete", TargetID: repairID,
		State:          ExecutionWaitingUser,
		PolicyRevision: "policy-v3", SubjectRevision: "subject-v4",
		Evidence: GateEvidence{CharterType: PRTypeFix, CharterGoal: "remove the race", CandidateSHA: "tip-commit", ChangedFiles: []string{"pkg/worker/run.go"}},
		Turns: []GateTurn{{
			StageID: "authorize", Kind: "human", Status: "waiting",
			GateForm: &GateForm{GateRef: "gates.implementation-complete", Prompt: "Complete?"},
		}}, CreatedAt: now,
		runtime: &gateRuntime{
			ConfigID: "strict", WorkflowRunID: "workflow-private-1",
			PinnedPolicy: policy, PinnedSubject: subject,
		},
	}
	sourceExecution := &AIExecutionSource{
		ExecutionID: "aix_11111111111111111111111111111111", WorkspaceID: workspaceID,
		Binding: "sha256:source-binding", AgentID: "main",
		SessionRevision: "sha256:source-revision", Tools: "none",
	}
	sourceExecution.Session = aiExecutionSourceSessionKey(sourceExecution)
	patch := AggregatePatch{
		Phase: pointer(PhaseImplementation), ExecutionState: pointer(ExecutionWaitingGate),
		ActiveCharterID: &charterID,
		AppendCharters: []Charter{{
			ID: charterID, Revision: 1, Type: PRTypeFix, Goal: "remove the race",
			AcceptanceCriteria: []string{"race detector is green"}, IncludedAreas: []string{"pkg/worker"},
			ExcludedAreas: []string{"web"}, NonGoals: []string{"refactor queue"},
			BaseSHA: provider.BaseSHA, HeadSHA: provider.HeadSHA, Confirmed: true,
			CreatedAt: now, ConfirmedAt: &now,
		}},
		AppendStageRuns: []StageRun{{
			ID: stageID, Stage: "implementation", State: ExecutionWaitingGate, CharterID: charterID,
			HeadSHA: provider.HeadSHA, Attempt: 1, PromptDigest: "sha256:stage", StartedAt: now,
			Evidence: &StageEvidence{
				Stage: "implementation_completion", RunID: stageID, Summary: "checked completion",
				Coverage:   Coverage{ReviewedAreas: []string{"pkg/worker"}, TestsConsidered: []string{"go test"}},
				FindingIDs: []string{findingID}, PromptDigest: "sha256:completion", CreatedAt: now,
				Validation: map[string]any{"run": ValidationRun{
					ID: validationID, StageRunID: stageID, State: ExecutionSucceeded, CandidateSHA: "tree-private",
					Checks:    []ValidationCheck{{ID: "go-test", Name: "go test", Status: "passed", DurationMS: 17}},
					StartedAt: now, FinishedAt: &finished,
				}},
			},
		}},
		AppendNudgeRounds: []NudgeRoundRecord{{
			ID: roundID, StageRunID: stageID, Stage: NudgeImplementationDone,
			Round: 1, MinimumRounds: 1, HardCap: 2, Strategy: NudgeAdversarial,
			Challenge: "find omitted completion work", VariantDigest: "sha256:variant",
			PromptDigest: "sha256:nudge", State: ExecutionSucceeded,
			NovelFindings: 1, FindingIDs: []string{findingID}, ResolvedFindings: 1,
			Reward: &reward, RewardProvenance: "user_disposition", CreatedAt: now,
		}},
		UpsertFindings: []Finding{{
			ID: findingID, Fingerprint: "sha256:finding", Origin: FindingOriginNudge,
			OriginRunID: stageID, NudgeRoundID: roundID, Severity: "medium", Title: "Adjacent cleanup",
			Message: "track this separately", Impact: "maintainability", Recommendation: "follow up",
			Validation: "add a regression test",
			Scope: ScopeAssessment{Distance: ScopeRelatedFollowup, Size: ChangeSizeS, Presence: WorkFollowUp, Files: 2, SemanticLines: 30, Modules: 1, Estimated: true, TypeCompatible: true, Confidence: .9, CharterClauses: []string{"non-goal"}, Explanation: "related but not required", ChangeEvidence: []ScopeChange{{
				Path: "pkg/worker/followup.go", Hunk: "@@ -1 +1 @@", Module: "pkg", SemanticLines: 12,
				Presence: WorkFollowUp, Distance: ScopeRelatedFollowup, Size: ChangeSizeS,
				TypeCompatible: true, Confidence: .9, CharterClauses: []string{"non-goal"}, Explanation: "follow-up only",
			}}},
			Disposition: FindingDeferred, NudgeReward: &reward, RewardSource: "user_disposition",
			SourceAvailable: true, source: sourceExecution,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}},
		AppendMessages: []Message{{
			ID: messageID, Role: "user", Stage: "implementation", Content: "keep the patch narrow",
			CharterID: charterID, HeadSHA: provider.HeadSHA, CreatedAt: now,
		}},
		AppendCorrections: []Correction{{
			ID: correctionID, Kind: CorrectionScope, Applicability: CorrectionReviewAndImpl,
			TargetType: "finding", TargetID: findingID, OriginalClaim: "in scope",
			Correction: "defer it", Evidence: "charter exclusion", CharterID: charterID,
			HeadSHA: provider.HeadSHA, Promoted: true, CreatedAt: now,
		}},
		AppendLessons: []RepositoryLesson{{
			ID: lessonID, RepositoryID: provider.RepositoryID, SourcePR: workspaceID,
			CorrectionID: correctionID, Kind: CorrectionScope, Applicability: CorrectionReviewAndImpl,
			PRType: PRTypeFix, Text: "avoid adjacent cleanup in fixes", Active: true, CreatedAt: now,
		}},
		UpsertDeferred: []DeferredGroup{{
			ID: groupID, Title: "Adjacent cleanup", Body: "Create a follow-up", FindingIDs: []string{findingID},
			Scope: ScopeAssessment{Distance: ScopeRelatedFollowup, Size: ChangeSizeS, Presence: WorkFollowUp, Files: 2, SemanticLines: 30, Modules: 1, Estimated: true, TypeCompatible: true, Confidence: .9, ChangeEvidence: []ScopeChange{{
				Path: "pkg/worker/followup.go", Presence: WorkFollowUp, Distance: ScopeRelatedFollowup,
				Size: ChangeSizeS, TypeCompatible: true, Confidence: .9,
			}}},
			Labels: []string{"follow-up"}, PublicationID: publicationID, Version: 1, CreatedAt: now, UpdatedAt: now,
		}},
		AppendRepairs: []RepairAttempt{{
			ID: repairID, StageRunID: stageID, Number: 1, State: ExecutionSucceeded,
			Instruction: "fix only the selected race", WorkspaceID: "git-workspace-private",
			ResultSummary: "fixed the race", ChangedFiles: []string{"pkg/worker/run.go"},
			FindingIDs: []string{findingID}, CandidateSHA: "tip-commit",
			Scope: ScopeAssessment{Distance: ScopeExact, Size: ChangeSizeXS, Presence: WorkCandidatePresent, Files: 1, SemanticLines: 8, Modules: 1, TypeCompatible: true, Confidence: 1, ChangeEvidence: []ScopeChange{{
				Path: "pkg/worker/run.go", Hunk: "@@ -10 +10 @@", Module: "pkg", SemanticLines: 8,
				Presence: WorkCandidatePresent, Distance: ScopeExact, Size: ChangeSizeXS, TypeCompatible: true, Confidence: 1,
			}}},
			PromptDigest: "sha256:repair", ScopePromptDigest: "sha256:scope", StartedAt: now, FinishedAt: &finished,
			PublicationFence: &ImplementationPublicationFence{
				GitWorkspaceID: "git-workspace-private", LineID: "line-private", LineVersion: 4,
				MutationEpoch: 4, ParkIntentID: "park-private", BaseCommit: "base-commit",
				Tip: "tip-commit", Tree: "tree-private",
			},
		}},
		AppendValidations: []ValidationRun{{
			ID: validationID, StageRunID: stageID, State: ExecutionSucceeded, CandidateSHA: "tip-commit",
			Checks:    []ValidationCheck{{ID: "go-test", Name: "go test", Status: "passed", DurationMS: 17}},
			StartedAt: now, FinishedAt: &finished,
		}},
		AppendGates: []GateRun{gate},
		AppendPublications: []Publication{{
			ID: publicationID, Kind: PublicationGitHubIssue, State: ExecutionWaitingGate,
			TargetID: groupID, FindingIDs: []string{findingID}, PayloadDigest: publicationDigest,
			CreatedAt: now, UpdatedAt: now, payload: publicationPayload,
		}},
		Activity: []Activity{{Kind: "workspace.integration_test", Actor: "system", Summary: "round trip", CreatedAt: now}},
	}
	mutation := Mutation{WorkspaceID: workspaceID, ExpectedVersion: 1, RequestID: "request-mutate-0001", Patch: patch}
	mutated, err := store.Mutate(ctx, mutation)
	require.NoError(t, err)
	require.Equal(t, int64(2), mutated.Aggregate.Workspace.Version)
	require.Len(t, mutated.Aggregate.Gates, 1)
	require.NotNil(t, mutated.Aggregate.Gates[0].runtime)
	require.Equal(t, policy, mutated.Aggregate.Gates[0].runtime.PinnedPolicy)
	require.Equal(t, subject, mutated.Aggregate.Gates[0].runtime.PinnedSubject)
	require.Equal(t, "workflow-private-1", mutated.Aggregate.Gates[0].runtime.WorkflowRunID)
	require.Equal(t, gate.Evidence, mutated.Aggregate.Gates[0].Evidence)
	require.NotNil(t, mutated.Aggregate.StageRuns[0].Evidence)
	require.Equal(t, []string{"pkg/worker"}, mutated.Aggregate.StageRuns[0].Evidence.Coverage.ReviewedAreas)
	require.Equal(t, charterID, mutated.Aggregate.Messages[0].CharterID)
	require.Equal(t, provider.HeadSHA, mutated.Aggregate.Messages[0].HeadSHA)
	require.Equal(t, "sha256:scope", mutated.Aggregate.RepairAttempts[0].ScopePromptDigest)
	require.NotNil(t, mutated.Aggregate.RepairAttempts[0].PublicationFence)
	require.Equal(t, "tree-private", mutated.Aggregate.RepairAttempts[0].PublicationFence.Tree)
	require.Equal(t, []string{findingID}, mutated.Aggregate.Publications[0].FindingIDs)
	require.Equal(t, publicationPayload, mutated.Aggregate.Publications[0].payload)
	require.Equal(t, []string{findingID}, mutated.Aggregate.DeferredGroups[0].FindingIDs)
	require.Equal(t, WorkFollowUp, mutated.Aggregate.Findings[0].Scope.Presence)
	require.True(t, mutated.Aggregate.Findings[0].SourceAvailable)
	require.NotNil(t, mutated.Aggregate.Findings[0].source)
	require.Equal(t, sourceExecution.Session, mutated.Aggregate.Findings[0].source.Session)
	projected, err := json.Marshal(mutated.Aggregate)
	require.NoError(t, err)
	require.NotContains(t, string(projected), sourceExecution.Session)
	require.Equal(t, "pkg/worker/followup.go", mutated.Aggregate.Findings[0].Scope.ChangeEvidence[0].Path)
	require.Equal(t, WorkCandidatePresent, mutated.Aggregate.RepairAttempts[0].Scope.Presence)
	require.Equal(t, "@@ -10 +10 @@", mutated.Aggregate.RepairAttempts[0].Scope.ChangeEvidence[0].Hunk)

	createRelatedWorkspace := func(
		pullRequestID string,
		pullNumber int64,
		providerOrigin string,
		repositoryID string,
		repository string,
	) Aggregate {
		t.Helper()
		relatedProvider := provider
		relatedProvider.ProviderOrigin = providerOrigin
		relatedProvider.RepositoryID = repositoryID
		relatedProvider.Repository = repository
		relatedProvider.PullRequestID = pullRequestID
		relatedProvider.PullNumber = pullNumber
		relatedProvider.HeadRepositoryID = repositoryID
		relatedProvider.HeadRepository = repository
		relatedProvider.HeadRef = "lesson-consumer"
		relatedProvider.HeadSHA = stableID("head_", providerOrigin, repositoryID, pullRequestID)
		relatedID := stableID("prw_", providerOrigin, repositoryID, pullRequestID)
		createdRelated, createErr := store.Create(ctx, CreateInput{
			RequestID: "request-create-" + pullRequestID,
			Workspace: Workspace{
				ID: relatedID, Provider: relatedProvider.Provider, ProviderOrigin: providerOrigin,
				RepositoryID: repositoryID, PullRequestID: pullRequestID, Repository: repository,
				PullNumber: pullNumber, Phase: PhaseCharter, ExecutionState: ExecutionWaitingUser,
				ProviderHeadSHA: relatedProvider.HeadSHA, Version: 1, CreatedAt: now, UpdatedAt: now,
			},
			Provider: relatedProvider,
		})
		require.NoError(t, createErr)
		return createdRelated.Aggregate
	}

	sameRepository := createRelatedWorkspace("pull-8", 8, provider.ProviderOrigin, provider.RepositoryID, provider.Repository)
	require.Len(t, sameRepository.RepositoryLessons, 1, "active lessons must follow repository identity across pull requests")
	require.Equal(t, lessonID, sameRepository.RepositoryLessons[0].ID)
	differentOrigin := createRelatedWorkspace("pull-9", 9, "https://other.example.test", provider.RepositoryID, provider.Repository)
	require.Empty(t, differentOrigin.RepositoryLessons, "repository names and IDs must not cross provider origins")
	differentRepository := createRelatedWorkspace("pull-10", 10, provider.ProviderOrigin, "repo-2", "octo/other-project")
	require.Empty(t, differentRepository.RepositoryLessons, "lessons must not cross provider repository IDs")

	stage := mutated.Aggregate.StageRuns[0]
	stage.State, stage.Summary, stage.FinishedAt = ExecutionSucceeded, "complete", &finished
	later, err := store.Mutate(ctx, Mutation{
		WorkspaceID: workspaceID, ExpectedVersion: 2, RequestID: "request-mutate-0002",
		Patch: AggregatePatch{ReplaceStageRuns: []StageRun{stage}},
	})
	require.NoError(t, err, "stage replacement must retain its original workspace-version input")
	require.Equal(t, int64(3), later.Aggregate.Workspace.Version)
	publication := later.Aggregate.Publications[0]
	publication.State = ExecutionQueued
	queued, err := store.Mutate(ctx, Mutation{
		WorkspaceID: workspaceID, ExpectedVersion: 3, RequestID: "request-mutate-0003",
		Patch: AggregatePatch{ReplacePublications: []Publication{publication}},
	})
	require.NoError(t, err)
	require.Equal(t, ExecutionQueued, queued.Aggregate.Publications[0].State)
	publication = queued.Aggregate.Publications[0]
	publication.State, publication.Attempts = ExecutionRunning, publication.Attempts+1
	claimed, err := store.Mutate(ctx, Mutation{
		WorkspaceID: workspaceID, ExpectedVersion: 4, RequestID: "request-mutate-0004",
		Patch: AggregatePatch{ReplacePublications: []Publication{publication}},
	})
	require.NoError(t, err)
	require.Equal(t, ExecutionRunning, claimed.Aggregate.Publications[0].State)
	publication = claimed.Aggregate.Publications[0]
	publication.State, publication.ExternalID, publication.ExternalURL = ExecutionSucceeded, "issue-9", "https://github.example.test/octo/project/issues/9"
	publication.PublishedAt = &finished
	finalized, err := store.Mutate(ctx, Mutation{
		WorkspaceID: workspaceID, ExpectedVersion: 5, RequestID: "request-mutate-0005",
		Patch: AggregatePatch{ReplacePublications: []Publication{publication}},
	})
	require.NoError(t, err)
	require.Equal(t, ExecutionSucceeded, finalized.Aggregate.Publications[0].State)

	historicalReplay, err := store.Mutate(ctx, mutation)
	require.NoError(t, err)
	require.True(t, historicalReplay.Replayed)
	require.Equal(t, int64(2), historicalReplay.Aggregate.Workspace.Version)
	require.Equal(t, ExecutionWaitingGate, historicalReplay.Aggregate.StageRuns[0].State)
	require.True(t, historicalReplay.Aggregate.Findings[0].SourceAvailable)
	require.NotNil(t, historicalReplay.Aggregate.Findings[0].source)
	require.Equal(t, sourceExecution.Session, historicalReplay.Aggregate.Findings[0].source.Session)
	finding := finalized.Aggregate.Findings[0]
	finding.Disposition, finding.Version = FindingDismissed, finding.Version+1
	removed, err := store.Mutate(ctx, Mutation{
		WorkspaceID: workspaceID, ExpectedVersion: 6, RequestID: "request-mutate-0006",
		Patch: AggregatePatch{UpsertFindings: []Finding{finding}},
	})
	require.NoError(t, err)
	require.Empty(t, removed.Aggregate.DeferredGroups[0].FindingIDs)
	require.Equal(t, FindingDismissed, removed.Aggregate.Findings[0].Disposition)

	require.NoError(t, raw.Close())
	reopened, err := eventing.Open(ctx, databasePath, eventing.WithClock(func() time.Time { return now }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	durable, err := NewEventingStore(reopened).Get(ctx, workspaceID)
	require.NoError(t, err)
	require.Equal(t, int64(7), durable.Workspace.Version)
	require.Equal(t, ExecutionSucceeded, durable.StageRuns[0].State)
	require.Equal(t, ExecutionSucceeded, durable.Publications[0].State)
	require.Equal(t, publicationPayload, durable.Publications[0].payload)
	require.NotNil(t, durable.Gates[0].runtime)
	require.Equal(t, "workflow-private-1", durable.Gates[0].runtime.WorkflowRunID)
	require.Equal(t, gate.Evidence, durable.Gates[0].Evidence)
	require.NotNil(t, durable.RepairAttempts[0].PublicationFence)
	require.Equal(t, "park-private", durable.RepairAttempts[0].PublicationFence.ParkIntentID)
	require.Equal(t, WorkCandidatePresent, durable.RepairAttempts[0].Scope.Presence)
	require.Equal(t, "pkg/worker/run.go", durable.RepairAttempts[0].Scope.ChangeEvidence[0].Path)
	require.Empty(t, durable.DeferredGroups[0].FindingIDs)
}

func pointer[T any](value T) *T { return &value }
