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
	replay, err := store.Create(context.Background(), created)
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

func testCreateInput() CreateInput {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	provider := ProviderSnapshot{
		Provider: "github", ProviderOrigin: "https://github.com", RepositoryID: "1",
		Repository: "octo/repo", PullRequestID: "2", PullNumber: 3, HeadSHA: "abcdef",
		ObservedAt: now,
	}
	return CreateInput{
		RequestID: "request-00000001",
		Workspace: Workspace{
			ID: "prw_11111111111111111111111111111111", Provider: "github",
			ProviderOrigin: provider.ProviderOrigin, RepositoryID: provider.RepositoryID,
			PullRequestID: provider.PullRequestID, Repository: provider.Repository,
			PullNumber: provider.PullNumber, ProviderHeadSHA: provider.HeadSHA,
			Phase: PhaseIntake, ExecutionState: ExecutionSucceeded, Version: 1, CreatedAt: now,
		},
		Provider: provider,
	}
}
