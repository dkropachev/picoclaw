package repoaudit

import (
	"strings"
	"testing"
)

func TestIsCanonicalGitHubRepository(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		repository string
		want       bool
	}{
		{name: "simple", repository: "owner/repo", want: true},
		{name: "repository punctuation", repository: "owner/repo_name.go-2", want: true},
		{name: "digits and hyphens", repository: "owner-2/repo-3", want: true},
		{name: "maximum components", repository: strings.Repeat("a", 100) + "/" + strings.Repeat("b", 100), want: true},
		{name: "empty", repository: "", want: false},
		{name: "missing repository", repository: "owner", want: false},
		{name: "missing owner", repository: "/repo", want: false},
		{name: "missing name", repository: "owner/", want: false},
		{name: "extra path", repository: "owner/repo/extra", want: false},
		{name: "URL", repository: "https://github.com/owner/repo", want: false},
		{name: "uppercase owner", repository: "Owner/repo", want: false},
		{name: "uppercase repository", repository: "owner/Repo", want: false},
		{name: "owner underscore", repository: "owner_name/repo", want: false},
		{name: "owner dot", repository: "owner.name/repo", want: false},
		{name: "dot owner", repository: "./repo", want: false},
		{name: "dot dot repository", repository: "owner/..", want: false},
		{name: "whitespace", repository: " owner/repo", want: false},
		{name: "unicode", repository: "ownér/repo", want: false},
		{name: "owner too long", repository: strings.Repeat("a", 101) + "/repo", want: false},
		{name: "repository too long", repository: "owner/" + strings.Repeat("b", 101), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := IsCanonicalGitHubRepository(test.repository); got != test.want {
				t.Fatalf("IsCanonicalGitHubRepository(%q) = %v, want %v", test.repository, got, test.want)
			}
		})
	}
}

func TestEvaluateIssuePublicationReportsEveryCanonicalBlocker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*RepositoryState, *IssueDraft)
		code   IssuePublicationBlockerCode
	}{
		{
			name: "repository is not GitHub",
			mutate: func(state *RepositoryState, draft *IssueDraft) {
				state.Repository, draft.Repository = "/workspace/repo", "/workspace/repo"
			},
			code: IssuePublicationRepositoryNotGitHub,
		},
		{
			name: "origin represents an existing issue",
			mutate: func(_ *RepositoryState, draft *IssueDraft) {
				draft.Origin = IssueDraftOriginLinked
			},
			code: IssuePublicationOriginNotPublishable,
		},
		{
			name: "preview state cannot publish",
			mutate: func(_ *RepositoryState, draft *IssueDraft) {
				draft.State = IssueDraftFailed
			},
			code: IssuePublicationStateNotPublishable,
		},
		{
			name: "finding is missing",
			mutate: func(_ *RepositoryState, draft *IssueDraft) {
				draft.FindingIDs = []string{"missing"}
			},
			code: IssuePublicationFindingMissing,
		},
		{
			name: "finding status is unresolved",
			mutate: func(state *RepositoryState, _ *IssueDraft) {
				state.Findings[0].Status = FindingPosted
			},
			code: IssuePublicationFindingStatusUnresolved,
		},
		{
			name: "duplicate decision is required",
			mutate: func(state *RepositoryState, _ *IssueDraft) {
				state.Findings[0].RepositoryMatchState = RepositoryMatchProvisional
				state.RepositoryFindings[0].MatchState = RepositoryMatchProvisional
			},
			code: IssuePublicationDuplicateReviewRequired,
		},
		{
			name: "finding points at another issue",
			mutate: func(state *RepositoryState, _ *IssueDraft) {
				state.Findings[0].IssueDraftID = "rid_other"
			},
			code: IssuePublicationIssueAssociationConflict,
		},
		{
			name: "repository finding has issue conflict",
			mutate: func(state *RepositoryState, _ *IssueDraft) {
				state.RepositoryFindings[0].Issue.Conflict = true
			},
			code: IssuePublicationIssueAssociationConflict,
		},
		{
			name: "finding otherwise cannot publish",
			mutate: func(state *RepositoryState, _ *IssueDraft) {
				state.Findings[0].IssueDraftID = ""
				state.RepositoryFindings[0].Lifecycle = RepositoryFindingDismissed
			},
			code: IssuePublicationFindingNotPublishable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state, draft := issuePublicationEligibleFixture()
			test.mutate(&state, &draft)
			eligibility := EvaluateIssuePublication(state, draft)
			if eligibility.CanPublish || len(eligibility.PublishBlockers) != 1 {
				t.Fatalf("eligibility = %#v", eligibility)
			}
			blocker := eligibility.PublishBlockers[0]
			if blocker.Code != test.code || blocker.Count != 1 || strings.TrimSpace(blocker.Message) == "" ||
				!eligibility.HasBlocker(test.code) {
				t.Fatalf("blocker = %#v, want %q", blocker, test.code)
			}
		})
	}
	state, draft := issuePublicationEligibleFixture()
	if EvaluateIssuePublication(state, draft).HasBlocker(IssuePublicationFindingMissing) {
		t.Fatal("eligible publication reported a missing-finding blocker")
	}
}

func TestEvaluateIssuePublicationAllowsInitialAndReconciliationStates(t *testing.T) {
	t.Parallel()
	for _, stateValue := range []IssueDraftState{
		IssueDraftEditing,
		IssueDraftPublishing,
		IssueDraftUnknown,
	} {
		name := string(stateValue)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			state, draft := issuePublicationEligibleFixture()
			draft.State = stateValue
			eligibility := EvaluateIssuePublication(state, draft)
			if !eligibility.CanPublish || len(eligibility.PublishBlockers) != 0 {
				t.Fatalf("eligibility = %#v", eligibility)
			}
		})
	}
}

func issuePublicationEligibleFixture() (RepositoryState, IssueDraft) {
	now := repositoryAuditTestNow
	draft := IssueDraft{
		ID: "rid_publication", Repository: "owner/repo", FindingIDs: []string{"finding"},
		Origin: IssueDraftOriginAIGenerated, Title: "Issue preview",
		Body: "Grounded diagnosis.", Labels: []string{"bug"}, State: IssueDraftEditing,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	repositoryFindingID := stableID("rrf_", "publication")
	finding := Finding{
		ID: "finding", Repository: draft.Repository, Status: FindingOpen,
		IssueDraftID: draft.ID, RepositoryFindingID: repositoryFindingID,
		RepositoryMatchState: RepositoryMatchNew,
		Version:              1, CreatedAt: now, UpdatedAt: now,
	}
	state := repositoryReviewCoverageState(draft.Repository)
	state.Findings = []Finding{finding}
	state.IssueDrafts = []IssueDraft{draft}
	state.RepositoryFindings = []RepositoryFinding{{
		ID: repositoryFindingID, Repository: draft.Repository,
		CanonicalTitle: "Issue preview", CanonicalSeverity: "high",
		ReviewFindingIDs: []string{finding.ID}, MatchState: RepositoryMatchNew,
		Lifecycle:       RepositoryFindingOpen,
		Issue:           RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone},
		ValidationState: RepositoryValidationNotRequested,
		Version:         1, CreatedAt: now, UpdatedAt: now,
	}}
	return state, draft
}
