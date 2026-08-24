package prworkspace

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDevelopmentIntakeVariantsAreMutuallyExclusive(t *testing.T) {
	valid := []CreateWorkspaceRequest{
		{Intent: IntentImplementFeature, SourceKind: SourceIssue, IssueURL: "https://github.com/octo/repo/issues/7"},
		{
			Intent:             IntentImplementFeature,
			SourceKind:         SourceBrief,
			RepositoryIdentity: "https://github.com|42",
			Brief:              "Add mobile notifications",
		},
		{Intent: IntentPickupPR, SourceKind: SourcePullRequest, PullRequestURL: "https://github.com/octo/repo/pull/9"},
	}
	for _, request := range valid {
		require.True(t, validCreateWorkspaceRequest(request), request)
	}
	invalid := []CreateWorkspaceRequest{
		{
			Intent:         IntentImplementFeature,
			SourceKind:     SourceIssue,
			IssueURL:       "https://github.com/octo/repo/issues/7",
			PullRequestURL: "https://github.com/octo/repo/pull/9",
		},
		{
			Intent:             IntentImplementFeature,
			SourceKind:         SourceBrief,
			RepositoryIdentity: "https://github.com|42",
			Brief:              "feature",
			IssueURL:           "https://github.com/octo/repo/issues/7",
		},
		{
			Intent:         IntentPickupPR,
			SourceKind:     SourcePullRequest,
			PullRequestURL: "https://github.com/octo/repo/pull/9",
			Brief:          "feature",
		},
		{Intent: IntentPickupPR, SourceKind: SourceIssue, IssueURL: "https://github.com/octo/repo/issues/7"},
	}
	for _, request := range invalid {
		require.False(t, validCreateWorkspaceRequest(request), request)
	}
}

type developmentIntakeResolver struct{}

func (developmentIntakeResolver) ResolvePullRequest(context.Context, ResolveRequest) (ProviderSnapshot, error) {
	return developmentProvider(SourcePullRequest, "pull-9", 9), nil
}

func (developmentIntakeResolver) ResolveIssue(context.Context, IssueResolveRequest) (ProviderSnapshot, error) {
	return developmentProvider(SourceIssue, "issue-7", 7), nil
}

func (developmentIntakeResolver) ResolveRepository(
	context.Context,
	RepositoryResolveRequest,
) (ProviderSnapshot, error) {
	return developmentProvider(SourceBrief, "brief-hash", 0), nil
}

func developmentProvider(source SourceKind, sourceID string, sourceNumber int64) ProviderSnapshot {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	provider := ProviderSnapshot{
		SourceID: sourceID, SourceNumber: sourceNumber, Provider: "github",
		ProviderOrigin: "https://github.com", RepositoryID: "42", Repository: "octo/repo",
		Title: "Work", AuthorID: "1", AuthorLogin: "octo", AuthenticatedUserID: "1",
		BaseRef: "main", BaseSHA: "aaaaaaaa", HeadRepositoryID: "42",
		HeadRepository: "octo/repo", HeadRef: "main", HeadSHA: "aaaaaaaa",
		State: "open", Owned: true, HeadWritable: true, CanCreatePullRequest: true, ObservedAt: now,
	}
	if source == SourcePullRequest {
		provider.PullRequestID, provider.PullNumber = sourceID, sourceNumber
	}
	return provider
}

func TestServiceCreatesEachDevelopmentIdentityWithoutMixingSources(t *testing.T) {
	service, err := NewService(ServiceConfig{
		Store: NewMemoryStore(), Provider: developmentIntakeResolver{},
		Now: func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	tests := []CreateWorkspaceRequest{
		{
			RequestID:  "dev_request_issue_0001",
			Intent:     IntentImplementFeature,
			SourceKind: SourceIssue,
			IssueURL:   "https://github.com/octo/repo/issues/7",
		},
		{
			RequestID:          "dev_request_brief_0001",
			Intent:             IntentImplementFeature,
			SourceKind:         SourceBrief,
			RepositoryIdentity: "https://github.com|42",
			Brief:              "Add mobile notifications",
		},
		{
			RequestID:      "dev_request_pull_00001",
			Intent:         IntentPickupPR,
			SourceKind:     SourcePullRequest,
			PullRequestURL: "https://github.com/octo/repo/pull/9",
		},
	}
	for _, request := range tests {
		aggregate, createErr := service.Create(t.Context(), request)
		require.NoError(t, createErr, "%#v", request)
		require.Equal(t, request.Intent, aggregate.Workspace.Intent)
		require.Equal(t, request.SourceKind, aggregate.Workspace.SourceKind)
		require.Regexp(t, `^devw_[0-9a-f]{32}$`, aggregate.Workspace.ID)
	}
}

func TestIdenticalBriefsAreIndependentButRequestRetriesReplay(t *testing.T) {
	service, err := NewService(ServiceConfig{Store: NewMemoryStore(), Provider: developmentIntakeResolver{}})
	require.NoError(t, err)
	request := CreateWorkspaceRequest{
		RequestID: "dev_brief_independent_01", Intent: IntentImplementFeature, SourceKind: SourceBrief,
		RepositoryIdentity: "https://github.com|42", Brief: "Add mobile notifications",
	}
	first, err := service.Create(t.Context(), request)
	require.NoError(t, err)
	replayed, err := service.Create(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, first.Workspace.ID, replayed.Workspace.ID)

	request.RequestID = "dev_brief_independent_02"
	second, err := service.Create(t.Context(), request)
	require.NoError(t, err)
	require.NotEqual(t, first.Workspace.ID, second.Workspace.ID)
}

type fixedPickupResolver struct{ snapshot ProviderSnapshot }

func (resolver fixedPickupResolver) ResolvePullRequest(context.Context, ResolveRequest) (ProviderSnapshot, error) {
	return resolver.snapshot, nil
}

func TestPickupRejectsClosedOrUnwritablePullRequest(t *testing.T) {
	for name, mutate := range map[string]func(*ProviderSnapshot){
		"closed":     func(value *ProviderSnapshot) { value.State = "closed" },
		"unwritable": func(value *ProviderSnapshot) { value.HeadWritable = false },
	} {
		t.Run(name, func(t *testing.T) {
			provider := developmentProvider(SourcePullRequest, "pull-9", 9)
			mutate(&provider)
			service, err := NewService(ServiceConfig{
				Store: NewMemoryStore(), Provider: fixedPickupResolver{snapshot: provider},
			})
			require.NoError(t, err)
			_, err = service.Create(t.Context(), CreateWorkspaceRequest{
				RequestID: "dev_pickup_rejected_01", Intent: IntentPickupPR,
				SourceKind: SourcePullRequest, PullRequestURL: "https://github.com/octo/repo/pull/9",
			})
			require.ErrorContains(t, err, "open and writable")
		})
	}
}

func TestAmbiguousCharterCannotBeAutomaticallyConfirmed(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	service, err := NewService(ServiceConfig{
		Store: NewMemoryStore(), Provider: developmentIntakeResolver{}, Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	aggregate, err := service.Create(t.Context(), CreateWorkspaceRequest{
		RequestID: "dev_ambiguous_charter_01", Intent: IntentImplementFeature,
		SourceKind: SourceIssue, IssueURL: "https://github.com/octo/repo/issues/7",
	})
	require.NoError(t, err)
	charter := Charter{
		ID: stableID("pcr_", aggregate.Workspace.ID, "ambiguous"), Revision: 1,
		Type: PRTypeFeature, Goal: "Add notifications", AcceptanceCriteria: []string{"Deliver an inbox"},
		ClarificationNeeded: true, ClarificationQuestion: "Which mobile platforms are required?",
		BaseSHA: aggregate.ProviderSnapshot.BaseSHA, HeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		CreatedAt: now,
	}
	stored, err := service.store.Mutate(t.Context(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "dev_ambiguous_charter_02", Patch: AggregatePatch{AppendCharters: []Charter{charter}},
	})
	require.NoError(t, err)
	_, err = service.ConfirmCharterAutomatically(t.Context(), ConfirmCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, CharterID: charter.ID,
		ExpectedVersion: stored.Aggregate.Workspace.Version, RequestID: "dev_ambiguous_charter_03",
	})
	require.ErrorIs(t, err, ErrConflict)
	manual, err := service.ConfirmCharter(t.Context(), ConfirmCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, CharterID: charter.ID,
		ExpectedVersion: stored.Aggregate.Workspace.Version, RequestID: "dev_ambiguous_charter_04",
	})
	require.NoError(t, err)
	require.False(t, manual.Charters[len(manual.Charters)-1].ClarificationNeeded)
	require.Empty(t, manual.Charters[len(manual.Charters)-1].ClarificationQuestion)
}
