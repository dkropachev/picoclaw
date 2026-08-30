package prworkspace

import (
	"context"
	"errors"
	"testing"
	"time"
)

type deferredAI struct{}

func (deferredAI) RunIsolated(ctx context.Context, request IsolatedAIRequest) (IsolatedAIResult, error) {
	if request.Operation == "deferred.group" {
		return successfulIsolatedAIResult(map[string]any{"groups": []any{map[string]any{
			"title": "Follow-up retry design", "body": "Track broader retry design separately.",
			"finding_ids": []any{"pfn_11111111111111111111111111111111"}, "labels": []any{"follow-up"},
		}}}), nil
	}
	return (serviceAI{}).RunIsolated(ctx, request)
}

func TestDeferredGroupingCoversEveryFinding(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	input := testCreateInput()
	input.Provider.CanCreateIssue = true
	_, _ = store.Create(context.Background(), input)
	charter := Charter{
		ID: "pcr_11111111111111111111111111111111", Type: PRTypeFix, Goal: "fix retry",
		BaseSHA: input.Provider.BaseSHA, HeadSHA: input.Provider.HeadSHA, Confirmed: true, CreatedAt: now,
	}
	stage := StageRun{
		ID: "psr_11111111111111111111111111111111", Stage: "review", State: ExecutionSucceeded,
		CharterID: charter.ID, HeadSHA: charter.HeadSHA, Attempt: 1, StartedAt: now, FinishedAt: &now,
	}
	finding := Finding{
		ID:          "pfn_11111111111111111111111111111111",
		Fingerprint: "sha256:f",
		Origin:      FindingOriginReview,
		OriginRunID: stage.ID,
		Severity:    "low",
		Title:       "follow-up",
		Message:     "later",
		Scope: ScopeAssessment{
			Distance:       ScopeRelatedFollowup,
			Size:           ChangeSizeS,
			TypeCompatible: true,
			Confidence:     1,
		},
		Disposition: FindingDeferred,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	active := charter.ID
	seed, _ := store.Mutate(context.Background(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: 1, RequestID: "request-00000030",
		Patch: AggregatePatch{
			ActiveCharterID: &active, AppendCharters: []Charter{charter},
			AppendStageRuns: []StageRun{stage}, UpsertFindings: []Finding{finding},
		},
	})
	service, _ := NewService(
		ServiceConfig{Store: store, AI: deferredAI{}, Gates: passingGates{}, Now: func() time.Time { return now }},
	)
	result, err := service.RegroupDeferred(
		context.Background(),
		RegroupDeferredRequest{
			WorkspaceID:     input.Workspace.ID,
			ExpectedVersion: seed.Aggregate.Workspace.Version,
			RequestID:       "request-00000031",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DeferredGroups) != 1 || result.DeferredGroups[0].FindingIDs[0] != finding.ID {
		t.Fatalf("groups = %#v", result.DeferredGroups)
	}
}

func TestDeferredDraftMutationsRejectStaleAndExternallyBoundGroups(t *testing.T) {
	t.Run("update linked group", func(t *testing.T) {
		service, before, groups := seededDeferredMutationService(t)
		linked := groups[0]
		linked.ExistingIssueURL = "https://github.com/octo/repo/issues/42"
		before = replaceDeferredGroupForTest(t, service.store, before, linked, "request-seed-linked-update")

		result, err := service.UpdateDeferred(t.Context(), UpdateDeferredRequest{
			WorkspaceID: before.Workspace.ID, GroupID: linked.ID,
			ExpectedVersion: before.Workspace.Version, RequestID: "request-update-linked-group",
			Title: "changed", Body: "changed", Labels: []string{"follow-up"},
		})
		assertDeferredMutationConflictUnchanged(t, result, before, err)
	})

	t.Run("split linked group", func(t *testing.T) {
		service, before, groups := seededDeferredMutationService(t)
		linked := groups[0]
		linked.ExistingIssueURL = "https://github.com/octo/repo/issues/42"
		before = replaceDeferredGroupForTest(t, service.store, before, linked, "request-seed-linked-split")

		result, err := service.SplitDeferred(t.Context(), SplitDeferredRequest{
			WorkspaceID: before.Workspace.ID, GroupID: linked.ID,
			ExpectedVersion: before.Workspace.Version, RequestID: "request-split-linked-group",
			FindingIDs: []string{linked.FindingIDs[0]},
		})
		assertDeferredMutationConflictUnchanged(t, result, before, err)
	})

	t.Run("merge linked group", func(t *testing.T) {
		service, before, groups := seededDeferredMutationService(t)
		linked := groups[0]
		linked.ExistingIssueURL = "https://github.com/octo/repo/issues/42"
		before = replaceDeferredGroupForTest(t, service.store, before, linked, "request-seed-linked-merge")

		result, err := service.MergeDeferred(t.Context(), MergeDeferredRequest{
			WorkspaceID: before.Workspace.ID, GroupIDs: []string{linked.ID, groups[1].ID},
			ExpectedVersion: before.Workspace.Version, RequestID: "request-merge-linked-group",
			Title: "merged", Body: "merged",
		})
		assertDeferredMutationConflictUnchanged(t, result, before, err)
	})

	t.Run("merge stale workspace version", func(t *testing.T) {
		service, before, groups := seededDeferredMutationService(t)
		result, err := service.MergeDeferred(t.Context(), MergeDeferredRequest{
			WorkspaceID: before.Workspace.ID, GroupIDs: []string{groups[0].ID, groups[1].ID},
			ExpectedVersion: before.Workspace.Version - 1, RequestID: "request-merge-stale-groups",
			Title: "merged", Body: "merged",
		})
		assertDeferredMutationConflictUnchanged(t, result, before, err)
	})
}

func TestDeferredDraftMutationsRejectDuplicateFindingIDs(t *testing.T) {
	t.Run("split request duplicates", func(t *testing.T) {
		service, before, groups := seededDeferredMutationService(t)
		findingID := groups[0].FindingIDs[0]
		_, err := service.SplitDeferred(t.Context(), SplitDeferredRequest{
			WorkspaceID: before.Workspace.ID, GroupID: groups[0].ID,
			ExpectedVersion: before.Workspace.Version, RequestID: "request-split-duplicate-findings",
			FindingIDs: []string{findingID, findingID},
		})
		assertDeferredInvalidPersistedUnchanged(t, service.store, before, err)
	})

	t.Run("split corrupt source duplicates", func(t *testing.T) {
		service, before, groups := seededDeferredMutationService(t)
		corrupt := groups[0]
		corrupt.FindingIDs = append(corrupt.FindingIDs, corrupt.FindingIDs[0])
		before = replaceDeferredGroupForTest(t, service.store, before, corrupt, "request-seed-duplicate-split-source")
		_, err := service.SplitDeferred(t.Context(), SplitDeferredRequest{
			WorkspaceID: before.Workspace.ID, GroupID: corrupt.ID,
			ExpectedVersion: before.Workspace.Version, RequestID: "request-split-duplicate-source",
			FindingIDs: []string{corrupt.FindingIDs[0]},
		})
		assertDeferredInvalidPersistedUnchanged(t, service.store, before, err)
	})

	t.Run("merge overlapping source findings", func(t *testing.T) {
		service, before, groups := seededDeferredMutationService(t)
		corrupt := groups[1]
		corrupt.FindingIDs = append(corrupt.FindingIDs, groups[0].FindingIDs[0])
		before = replaceDeferredGroupForTest(t, service.store, before, corrupt, "request-seed-overlapping-merge-source")
		_, err := service.MergeDeferred(t.Context(), MergeDeferredRequest{
			WorkspaceID: before.Workspace.ID, GroupIDs: []string{groups[0].ID, corrupt.ID},
			ExpectedVersion: before.Workspace.Version, RequestID: "request-merge-overlapping-source",
			Title: "merged", Body: "merged",
		})
		assertDeferredInvalidPersistedUnchanged(t, service.store, before, err)
	})
}

func TestDeferredDraftUpdateSplitAndMerge(t *testing.T) {
	t.Run("update", func(t *testing.T) {
		service, before, groups := seededDeferredMutationService(t)
		result, err := service.UpdateDeferred(t.Context(), UpdateDeferredRequest{
			WorkspaceID: before.Workspace.ID, GroupID: groups[0].ID,
			ExpectedVersion: before.Workspace.Version, RequestID: "request-update-deferred-group",
			Title: "updated title", Body: "updated body", Labels: []string{"bug", "follow-up"},
		})
		if err != nil {
			t.Fatal(err)
		}
		updated, ok := findDeferredGroup(result.DeferredGroups, groups[0].ID)
		if !ok || updated.Title != "updated title" || updated.Body != "updated body" ||
			len(updated.Labels) != 2 || updated.Version != groups[0].Version+1 {
			t.Fatalf("updated group = %#v", updated)
		}
	})

	t.Run("split", func(t *testing.T) {
		service, before, groups := seededDeferredMutationService(t)
		result, err := service.SplitDeferred(t.Context(), SplitDeferredRequest{
			WorkspaceID: before.Workspace.ID, GroupID: groups[0].ID,
			ExpectedVersion: before.Workspace.Version, RequestID: "request-split-deferred-group",
			FindingIDs: []string{groups[0].FindingIDs[0]},
		})
		if err != nil {
			t.Fatal(err)
		}
		original, ok := findDeferredGroup(result.DeferredGroups, groups[0].ID)
		if !ok || len(original.FindingIDs) != 1 || original.FindingIDs[0] != groups[0].FindingIDs[1] ||
			len(result.DeferredGroups) != len(before.DeferredGroups)+1 {
			t.Fatalf("split groups = %#v", result.DeferredGroups)
		}
	})

	t.Run("merge", func(t *testing.T) {
		service, before, groups := seededDeferredMutationService(t)
		result, err := service.MergeDeferred(t.Context(), MergeDeferredRequest{
			WorkspaceID: before.Workspace.ID, GroupIDs: []string{groups[0].ID, groups[1].ID},
			ExpectedVersion: before.Workspace.Version, RequestID: "request-merge-deferred-groups",
			Title: "merged title", Body: "merged body",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.DeferredGroups) != len(before.DeferredGroups)+1 {
			t.Fatalf("merged groups = %#v", result.DeferredGroups)
		}
		for _, source := range groups {
			group, ok := findDeferredGroup(result.DeferredGroups, source.ID)
			if !ok || len(group.FindingIDs) != 0 {
				t.Fatalf("merged source group = %#v", group)
			}
		}
	})
}

func seededDeferredMutationService(t *testing.T) (*Service, Aggregate, []DeferredGroup) {
	t.Helper()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	input := testCreateInput()
	created, err := store.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	findingIDs := []string{
		"pfn_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"pfn_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"pfn_cccccccccccccccccccccccccccccccc",
	}
	findings := make([]Finding, len(findingIDs))
	for index, id := range findingIDs {
		findings[index] = Finding{
			ID:          id,
			Fingerprint: "sha256:deferred-" + id,
			Origin:      FindingOriginReview,
			Severity:    "low",
			Title:       "follow-up",
			Message:     "track separately",
			Scope: ScopeAssessment{
				Distance:       ScopeRelatedFollowup,
				Size:           ChangeSizeS,
				TypeCompatible: true,
				Confidence:     1,
			},
			Disposition: FindingDeferred,
			Version:     1,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
	}
	groups := []DeferredGroup{
		{
			ID: "pdg_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "first", Body: "first body",
			FindingIDs: findingIDs[:2], Labels: []string{"follow-up"}, Scope: findings[0].Scope,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: "pdg_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Title: "second", Body: "second body",
			FindingIDs: findingIDs[2:], Labels: []string{"bug"}, Scope: findings[2].Scope,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		},
	}
	seeded, err := store.Mutate(t.Context(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-seed-deferred-mutations",
		Patch:     AggregatePatch{UpsertFindings: findings, UpsertDeferred: groups},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return service, seeded.Aggregate, groups
}

func replaceDeferredGroupForTest(
	t *testing.T,
	store Store,
	before Aggregate,
	group DeferredGroup,
	requestID string,
) Aggregate {
	t.Helper()
	result, err := store.Mutate(t.Context(), Mutation{
		WorkspaceID: before.Workspace.ID, ExpectedVersion: before.Workspace.Version,
		RequestID: requestID, Patch: AggregatePatch{UpsertDeferred: []DeferredGroup{group}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Aggregate
}

func assertDeferredMutationConflictUnchanged(t *testing.T, result, before Aggregate, err error) {
	t.Helper()
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("mutation error = %v, want conflict", err)
	}
	if result.Workspace.Version != before.Workspace.Version {
		t.Fatalf("result version = %d, want %d", result.Workspace.Version, before.Workspace.Version)
	}
	if len(result.DeferredGroups) != len(before.DeferredGroups) {
		t.Fatalf("result groups = %#v, want unchanged %#v", result.DeferredGroups, before.DeferredGroups)
	}
}

func assertDeferredInvalidPersistedUnchanged(t *testing.T, store Store, before Aggregate, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutation error = %v, want invalid", err)
	}
	after, getErr := store.Get(t.Context(), before.Workspace.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if after.Workspace.Version != before.Workspace.Version || len(after.DeferredGroups) != len(before.DeferredGroups) {
		t.Fatalf("persisted aggregate = %#v, want unchanged %#v", after, before)
	}
}
