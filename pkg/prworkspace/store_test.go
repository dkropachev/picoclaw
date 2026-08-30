package prworkspace

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryStoreCASIdempotencyAndIdentity(t *testing.T) {
	store := NewMemoryStore()
	created := testCreateInput()
	first, err := store.Create(context.Background(), created)
	if err != nil {
		t.Fatal(err)
	}
	retry := created
	retry.Workspace.CreatedAt = retry.Workspace.CreatedAt.Add(time.Minute)
	retry.Provider.ObservedAt = retry.Provider.ObservedAt.Add(time.Minute)
	replay, err := store.Create(context.Background(), retry)
	if err != nil || !replay.Replayed {
		t.Fatalf("create replay = %#v, %v", replay, err)
	}
	phase := PhaseCharter
	mutation := Mutation{
		WorkspaceID: first.Aggregate.Workspace.ID, ExpectedVersion: 1,
		RequestID: "request-00000002", Patch: AggregatePatch{Phase: &phase},
	}
	updated, err := store.Mutate(context.Background(), mutation)
	if err != nil || updated.Aggregate.Workspace.Version != 2 {
		t.Fatalf("mutate = %#v, %v", updated, err)
	}
	replayed, err := store.Mutate(context.Background(), mutation)
	if err != nil || !replayed.Replayed || replayed.Aggregate.Workspace.Version != 2 {
		t.Fatalf("mutation replay = %#v, %v", replayed, err)
	}
	mutation.RequestID = "request-00000003"
	if _, err := store.Mutate(context.Background(), mutation); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale mutation error = %v", err)
	}
}

func TestMemoryStoreRejectsProviderIdentityChange(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), testCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	provider := created.Aggregate.ProviderSnapshot
	provider.RepositoryID = "other"
	_, err = store.Mutate(context.Background(), Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: 1,
		RequestID: "request-00000004", Patch: AggregatePatch{Provider: &provider},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("identity change error = %v", err)
	}
}

func TestMemoryStoreFinalizesFirstDraftPullRequestIdentityUnderPublicationLease(t *testing.T) {
	ctx := context.Background()
	input := testCreateInput()
	input.RequestID = "request-create-feature-draft"
	input.Provider.Intent = IntentImplementFeature
	input.Provider.SourceKind = SourceBrief
	input.Provider.SourceID = "brief-memory-draft"
	input.Provider.SourceNumber = 0
	input.Provider.PullRequestID = ""
	input.Provider.PullNumber = 0
	input.Workspace.Intent = input.Provider.Intent
	input.Workspace.SourceKind = input.Provider.SourceKind
	input.Workspace.SourceID = input.Provider.SourceID
	input.Workspace.SourceNumber = 0
	input.Workspace.PullRequestID = ""
	input.Workspace.PullNumber = 0

	memory := NewMemoryStore()
	created, err := memory.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Store: memory})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	publication := Publication{
		ID: "ppb_11111111111111111111111111111111", Kind: PublicationBranchPush,
		State: ExecutionQueued, ExpectedHeadSHA: input.Provider.HeadSHA,
		PayloadDigest: "sha256:draft-publication", CreatedAt: now, UpdatedAt: now,
	}
	queued, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-queue-feature-draft",
		Patch:     AggregatePatch{AppendPublications: []Publication{publication}},
	})
	if err != nil {
		t.Fatal(err)
	}
	publication.State = ExecutionRunning
	publication.Attempts = 1
	claimed, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: queued.Aggregate.Workspace.Version,
		RequestID:                "request-claim-feature-draft",
		Patch:                    AggregatePatch{ReplacePublications: []Publication{publication}},
		branchPublicationLeaseID: publication.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := claimed.Aggregate.ProviderSnapshot
	provider.PullRequestID = "pull-17"
	provider.PullNumber = 17
	provider.HeadRef = "picoclaw/code-cli"
	provider.HeadSHA = "candidate-commit"
	publication.State = ExecutionSucceeded
	publication.ExternalID = provider.PullRequestID
	publication.ExternalURL = "https://github.com/octo/repo/pull/17"
	publication.PublishedAt = &now
	phase, state := PhaseComplete, ExecutionSucceeded
	finalized, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: claimed.Aggregate.Workspace.Version,
		RequestID: "request-finalize-feature-draft",
		Patch: AggregatePatch{
			Phase: &phase, ExecutionState: &state, Provider: &provider,
			ReplacePublications: []Publication{publication},
		},
		branchPublicationLeaseID: publication.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Aggregate.Workspace.PullRequestID != provider.PullRequestID ||
		finalized.Aggregate.Workspace.PullNumber != provider.PullNumber ||
		finalized.Aggregate.ProviderSnapshot.PullRequestID != provider.PullRequestID ||
		finalized.Aggregate.Publications[0].State != ExecutionSucceeded {
		t.Fatalf("finalized draft identity = %#v", finalized.Aggregate)
	}
}

func TestFailUnsafeProviderClosesRunningBranchPublicationThroughLeaseFence(t *testing.T) {
	ctx := context.Background()
	input := testCreateInput()
	input.RequestID = "request-create-unsafe-feature"
	input.Provider.Intent = IntentImplementFeature
	input.Provider.SourceKind = SourceBrief
	input.Provider.SourceID = "brief-unsafe-feature"
	input.Provider.SourceNumber = 0
	input.Provider.PullRequestID = ""
	input.Provider.PullNumber = 0
	input.Workspace.Intent = input.Provider.Intent
	input.Workspace.SourceKind = input.Provider.SourceKind
	input.Workspace.SourceID = input.Provider.SourceID
	input.Workspace.SourceNumber = 0
	input.Workspace.PullRequestID = ""
	input.Workspace.PullNumber = 0

	memory := NewMemoryStore()
	created, err := memory.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Store: memory})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	publication := Publication{
		ID: "ppb_22222222222222222222222222222222", Kind: PublicationBranchPush,
		State: ExecutionQueued, ExpectedHeadSHA: input.Provider.HeadSHA,
		PayloadDigest: "sha256:unsafe-publication", CreatedAt: now, UpdatedAt: now,
	}
	queued, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-queue-unsafe-feature",
		Patch:     AggregatePatch{AppendPublications: []Publication{publication}},
	})
	if err != nil {
		t.Fatal(err)
	}
	publication.State = ExecutionRunning
	publication.Attempts = 1
	claimed, err := service.store.Mutate(ctx, Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: queued.Aggregate.Workspace.Version,
		RequestID:                "request-claim-unsafe-feature",
		Patch:                    AggregatePatch{ReplacePublications: []Publication{publication}},
		branchPublicationLeaseID: publication.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	failed, err := service.FailUnsafeProvider(ctx, claimed.Aggregate, "request-fail-unsafe-feature")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Workspace.Version != claimed.Aggregate.Workspace.Version+2 ||
		failed.Workspace.ExecutionState != ExecutionFailed ||
		len(failed.Publications) != 1 || failed.Publications[0].State != ExecutionFailed ||
		failed.Publications[0].PublicErrorCode != "unsafe_provider" ||
		!unsafeProviderFailureRecorded(failed) {
		t.Fatalf("unsafe running publication result = %#v", failed)
	}
}

func TestMemoryStoreBindsAndFreezesFindingSourceProvenance(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), testCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	source := testAIExecutionSource("aix_11111111111111111111111111111111")
	finding := Finding{
		ID: "pfn_11111111111111111111111111111111", Fingerprint: "sha256:finding",
		SourceAvailable: true, source: source, Version: 1,
	}
	mutation := Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: 1,
		RequestID: "request-source-provenance-01",
		Patch:     AggregatePatch{UpsertFindings: []Finding{finding}},
	}
	stored, err := store.Mutate(context.Background(), mutation)
	if err != nil || stored.Aggregate.Findings[0].source == nil {
		t.Fatalf("source mutation = %#v, %v", stored, err)
	}

	changed := finding
	changedSource := *source
	changedSource.SessionRevision = "sha256:changed-source-revision"
	changed.source = &changedSource
	mutation.Patch = AggregatePatch{UpsertFindings: []Finding{changed}}
	if _, err := store.Mutate(context.Background(), mutation); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("changed replay error = %v", err)
	}
	mutation.RequestID = "request-source-provenance-02"
	mutation.ExpectedVersion = stored.Aggregate.Workspace.Version
	if _, err := store.Mutate(context.Background(), mutation); !errors.Is(err, ErrConflict) {
		t.Fatalf("retarget source error = %v", err)
	}

	removed := finding
	removed.SourceAvailable, removed.source = false, nil
	mutation.RequestID = "request-source-provenance-03"
	mutation.Patch = AggregatePatch{UpsertFindings: []Finding{removed}}
	if _, err := store.Mutate(context.Background(), mutation); !errors.Is(err, ErrConflict) {
		t.Fatalf("remove source error = %v", err)
	}

	mutation.RequestID = "request-source-provenance-04"
	mutation.Patch = AggregatePatch{UpsertFindings: []Finding{finding}}
	if _, err := store.Mutate(context.Background(), mutation); err != nil {
		t.Fatalf("same source update error = %v", err)
	}
}

func testCreateInput() CreateInput {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	provider := ProviderSnapshot{
		Intent: IntentPickupPR, SourceKind: SourcePullRequest, SourceID: "2", SourceNumber: 3,
		Provider: "github", ProviderOrigin: "https://github.com", RepositoryID: "1",
		Repository: "octo/repo", PullRequestID: "2", PullNumber: 3, HeadSHA: "abcdef",
		ObservedAt: now,
	}
	return CreateInput{
		RequestID: "request-00000001",
		Workspace: Workspace{
			ID: "devw_11111111111111111111111111111111", Intent: provider.Intent,
			SourceKind: provider.SourceKind, SourceID: provider.SourceID, SourceNumber: provider.SourceNumber,
			Provider:       "github",
			ProviderOrigin: provider.ProviderOrigin, RepositoryID: provider.RepositoryID,
			PullRequestID: provider.PullRequestID, Repository: provider.Repository,
			PullNumber: provider.PullNumber, ProviderHeadSHA: provider.HeadSHA,
			Phase: PhaseIntake, ExecutionState: ExecutionSucceeded, Version: 1, CreatedAt: now,
		},
		Provider: provider,
	}
}
