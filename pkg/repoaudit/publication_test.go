package repoaudit

import (
	"errors"
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

func TestEvaluateIssuePublicationReportsEveryBlocker(t *testing.T) {
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
			name: "preview is not canonical",
			mutate: func(_ *RepositoryState, draft *IssueDraft) {
				draft.Canonical = false
			},
			code: IssuePublicationPreviewNotCanonical,
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
				state.Findings[0].Status = FindingDismissed
			},
			code: IssuePublicationFindingStatusUnresolved,
		},
		{
			name: "deduplication is unresolved",
			mutate: func(state *RepositoryState, _ *IssueDraft) {
				state.Findings[0].DeduplicationPending = true
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
			name: "historical merge is active",
			mutate: func(state *RepositoryState, _ *IssueDraft) {
				state.HistoricalDeduplication = HistoricalDeduplicationReplay{
					Required: true, Status: HistoricalDeduplicationMerging,
					MergeLease: HistoricalDeduplicationMergeLease{ID: "rhl_active"},
				}
			},
			code: IssuePublicationHistoricalMergeActive,
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
}

func TestEvaluateIssuePublicationAllowsInitialAndReconciliationStates(t *testing.T) {
	t.Parallel()
	for _, origin := range []IssueDraftOrigin{IssueDraftOriginAIGenerated, IssueDraftOriginLegacy} {
		for _, stateValue := range []IssueDraftState{
			IssueDraftEditing,
			IssueDraftPublishing,
			IssueDraftUnknown,
		} {
			name := string(origin) + "/" + string(stateValue)
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				state, draft := issuePublicationEligibleFixture()
				draft.Origin, draft.State = origin, stateValue
				eligibility := EvaluateIssuePublication(state, draft)
				if !eligibility.CanPublish || len(eligibility.PublishBlockers) != 0 {
					t.Fatalf("eligibility = %#v", eligibility)
				}
			})
		}
	}
	state, draft := issuePublicationEligibleFixture()
	state.Findings[0].RepositoryFindingID = ""
	state.Findings[0].RepositoryMatchState = ""
	state.RepositoryFindings = nil
	eligibility := EvaluateIssuePublication(state, draft)
	if !eligibility.CanPublish ||
		eligibility.HasBlocker(IssuePublicationFindingNotPublishable) {
		t.Fatalf("pre-queue legacy eligibility = %#v", eligibility)
	}
}

func TestEvaluateIssuePublicationAggregatesGroupedPreviewBlockers(t *testing.T) {
	t.Parallel()
	state, draft := issuePublicationGroupedFixture()
	eligibility := EvaluateIssuePublication(state, draft)
	want := []IssuePublicationBlocker{
		{
			Code: IssuePublicationFindingStatusUnresolved, Count: 3,
			Message: "One or more linked findings do not yet have a publishable status.",
		},
		{
			Code: IssuePublicationDuplicateReviewRequired, Count: 1,
			Message: "A duplicate decision is required before publication.",
		},
	}
	if eligibility.CanPublish || len(eligibility.PublishBlockers) != len(want) {
		t.Fatalf("eligibility = %#v", eligibility)
	}
	for index := range want {
		if eligibility.PublishBlockers[index] != want[index] {
			t.Fatalf("blocker %d = %#v, want %#v", index, eligibility.PublishBlockers[index], want[index])
		}
	}
}

func TestClaimIssueDraftPublicationMatchesEligibility(t *testing.T) {
	t.Run("eligible claim and reconciliation", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state, draft := issuePublicationEligibleFixture()
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		if eligibility := EvaluateIssuePublication(state, draft); !eligibility.CanPublish {
			t.Fatalf("eligible fixture = %#v", eligibility)
		}
		state, publishing, claimed, err := store.ClaimIssueDraftPublication(
			state.Repository, draft.ID, draft.Version,
		)
		if err != nil || !claimed || publishing.State != IssueDraftPublishing {
			t.Fatalf("claim state=%#v draft=%#v claimed=%v err=%v", state, publishing, claimed, err)
		}
		if eligibility := EvaluateIssuePublication(state, publishing); !eligibility.CanPublish {
			t.Fatalf("publishing eligibility = %#v", eligibility)
		}
		state, repeated, claimed, err := store.ClaimIssueDraftPublication(
			state.Repository, publishing.ID, publishing.Version,
		)
		if err != nil || claimed || repeated.State != IssueDraftPublishing {
			t.Fatalf("repeat state=%#v draft=%#v claimed=%v err=%v", state, repeated, claimed, err)
		}
		state, unknown, err := store.SetIssueDraftPublication(
			state.Repository, publishing.ID, publishing.Version, IssueDraftUnknown, "", "",
		)
		if err != nil {
			t.Fatal(err)
		}
		if eligibility := EvaluateIssuePublication(state, unknown); !eligibility.CanPublish {
			t.Fatalf("unknown eligibility = %#v", eligibility)
		}
		_, repeated, claimed, err = store.ClaimIssueDraftPublication(
			state.Repository, unknown.ID, unknown.Version,
		)
		if err != nil || claimed || repeated.State != IssueDraftUnknown {
			t.Fatalf("unknown repeat draft=%#v claimed=%v err=%v", repeated, claimed, err)
		}
	})

	t.Run("grouped blockers reject atomically", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state, draft := issuePublicationGroupedFixture()
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		eligibility := EvaluateIssuePublication(state, draft)
		if eligibility.CanPublish || len(eligibility.PublishBlockers) != 2 ||
			eligibility.PublishBlockers[0].Code != IssuePublicationFindingStatusUnresolved ||
			eligibility.PublishBlockers[0].Count != 3 ||
			eligibility.PublishBlockers[1].Code != IssuePublicationDuplicateReviewRequired ||
			eligibility.PublishBlockers[1].Count != 1 {
			t.Fatalf("grouped eligibility = %#v", eligibility)
		}
		if _, _, claimed, err := store.ClaimIssueDraftPublication(
			state.Repository, draft.ID, draft.Version,
		); !errors.Is(err, ErrConflict) || claimed {
			t.Fatalf("blocked claim claimed=%v err=%v", claimed, err)
		}
		persisted, found, err := store.Get(state.Repository)
		if err != nil || !found {
			t.Fatalf("reload found=%v err=%v", found, err)
		}
		persistedDraft := persisted.IssueDrafts[issueDraftIndexByID(persisted.IssueDrafts, draft.ID)]
		if persistedDraft.State != IssueDraftEditing || persistedDraft.Version != draft.Version {
			t.Fatalf("blocked claim mutated draft = %#v", persistedDraft)
		}
	})

	t.Run("noncanonical conflict rejects", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state, target := issuePublicationEligibleFixture()
		canonical := target
		canonical.ID = "rid_publication_canonical"
		canonical.CreatedAt = canonical.CreatedAt.Add(1)
		canonical.UpdatedAt = canonical.UpdatedAt.Add(1)
		target.Canonical = false
		canonical.Canonical = true
		state.Findings[0].IssueDraftID = canonical.ID
		state.IssueDrafts = []IssueDraft{target, canonical}
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		eligibility := EvaluateIssuePublication(state, target)
		if eligibility.CanPublish ||
			!eligibility.HasBlocker(IssuePublicationPreviewNotCanonical) ||
			!eligibility.HasBlocker(IssuePublicationIssueAssociationConflict) {
			t.Fatalf("noncanonical eligibility = %#v", eligibility)
		}
		if _, _, claimed, err := store.ClaimIssueDraftPublication(
			state.Repository, target.ID, target.Version,
		); !errors.Is(err, ErrConflict) || claimed {
			t.Fatalf("noncanonical claim claimed=%v err=%v", claimed, err)
		}
	})

	t.Run("nonpublishable state rejects", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state, draft := issuePublicationEligibleFixture()
		draft.State = IssueDraftFailed
		state.IssueDrafts[0] = draft
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		eligibility := EvaluateIssuePublication(state, draft)
		if eligibility.CanPublish ||
			!eligibility.HasBlocker(IssuePublicationStateNotPublishable) {
			t.Fatalf("failed eligibility = %#v", eligibility)
		}
		if _, _, claimed, err := store.ClaimIssueDraftPublication(
			state.Repository, draft.ID, draft.Version,
		); !errors.Is(err, ErrConflict) || claimed {
			t.Fatalf("failed claim claimed=%v err=%v", claimed, err)
		}
	})

	t.Run("issue association conflict rejects", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state, occurrence := recordLifecycleFinding(
			t, store, strings.Repeat("7", 40), strings.Repeat("8", 40),
			"publication-eligibility-conflict", "main", "main", true,
			"publication conflict",
		)
		state, aggregate := completeRepositoryAuditTestMapping(
			t, store, state, occurrence.ID,
		)
		state, draft, err := store.LinkExistingIssue(ExistingIssueLink{
			Repository: state.Repository, FindingID: occurrence.ID,
			ExpectedFindingVersion: state.Findings[findingIndexByID(state.Findings, occurrence.ID)].Version,
			ExternalID:             "17", ExternalURL: "https://github.com/owner/repo/issues/17",
			Title: "Existing issue", Confirmed: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		index := repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)
		state.RepositoryFindings[index].Issue.Conflict = true
		state.RepositoryFindings[index].Issue.ConflictURLs = []string{
			"https://github.com/owner/repo/issues/17",
			"https://github.com/owner/repo/issues/18",
		}
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		eligibility := EvaluateIssuePublication(state, draft)
		if eligibility.CanPublish ||
			!eligibility.HasBlocker(IssuePublicationIssueAssociationConflict) {
			t.Fatalf("conflict eligibility = %#v", eligibility)
		}
		if _, _, claimed, err := store.ClaimIssueDraftPublication(
			state.Repository, draft.ID, draft.Version,
		); !errors.Is(err, ErrConflict) || claimed {
			t.Fatalf("conflict claim claimed=%v err=%v", claimed, err)
		}
	})

	t.Run("non GitHub repository rejects", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state, draft := issuePublicationEligibleFixture()
		state.Repository, state.ID = "/workspace/repo", RepositoryID("/workspace/repo")
		draft.Repository = state.Repository
		state.IssueDrafts[0].Repository = state.Repository
		state.Findings[0].Repository = state.Repository
		state.RepositoryFindings[0].Repository = state.Repository
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		eligibility := EvaluateIssuePublication(state, draft)
		if !eligibility.HasBlocker(IssuePublicationRepositoryNotGitHub) {
			t.Fatalf("eligibility = %#v", eligibility)
		}
		if _, _, claimed, err := store.ClaimIssueDraftPublication(
			state.Repository, draft.ID, draft.Version,
		); !errors.Is(err, ErrConflict) || claimed {
			t.Fatalf("non-GitHub claim claimed=%v err=%v", claimed, err)
		}
	})

	t.Run("historical merge preserves its typed fence", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state, draft := issuePublicationEligibleFixture()
		state.HistoricalDeduplication = issuePublicationHistoricalMergeFixture(state.Version)
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		eligibility := EvaluateIssuePublication(state, draft)
		if !eligibility.HasBlocker(IssuePublicationHistoricalMergeActive) {
			t.Fatalf("eligibility = %#v", eligibility)
		}
		if _, _, claimed, err := store.ClaimIssueDraftPublication(
			state.Repository, draft.ID, draft.Version,
		); !errors.Is(err, ErrHistoricalDeduplicationInProgress) || claimed {
			t.Fatalf("historical claim claimed=%v err=%v", claimed, err)
		}
	})

	t.Run("posted claim remains idempotent", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state, draft := issuePublicationEligibleFixture()
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		state, publishing, claimed, err := store.ClaimIssueDraftPublication(
			state.Repository, draft.ID, draft.Version,
		)
		if err != nil || !claimed {
			t.Fatalf("claim claimed=%v err=%v", claimed, err)
		}
		state, posted, err := store.SetIssueDraftPublication(
			state.Repository, publishing.ID, publishing.Version, IssueDraftPosted,
			"17", "https://github.com/owner/repo/issues/17",
		)
		if err != nil {
			t.Fatal(err)
		}
		eligibility := EvaluateIssuePublication(state, posted)
		if eligibility.CanPublish || !eligibility.HasBlocker(IssuePublicationStateNotPublishable) ||
			!eligibility.HasBlocker(IssuePublicationFindingStatusUnresolved) {
			t.Fatalf("posted eligibility = %#v", eligibility)
		}
		_, replayed, claimed, err := store.ClaimIssueDraftPublication(
			state.Repository, posted.ID, posted.Version,
		)
		if err != nil || claimed || replayed.State != IssueDraftPosted {
			t.Fatalf("posted replay draft=%#v claimed=%v err=%v", replayed, claimed, err)
		}
		state.HistoricalDeduplication = issuePublicationHistoricalMergeFixture(state.Version)
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		if _, _, claimed, err := store.ClaimIssueDraftPublication(
			state.Repository, posted.ID, posted.Version,
		); !errors.Is(err, ErrHistoricalDeduplicationInProgress) || claimed {
			t.Fatalf("posted historical claim claimed=%v err=%v", claimed, err)
		}
	})
}

func issuePublicationHistoricalMergeFixture(version int64) HistoricalDeduplicationReplay {
	return HistoricalDeduplicationReplay{
		Required: true, Status: HistoricalDeduplicationMerging,
		ProfileSnapshot: HistoricalDeduplicationProfileSnapshot{
			ReviewerModel: "reviewer", DeduplicationModel: "deduplicator",
			SimilarityThreshold: DeduplicationDefaultThreshold,
			CandidateLimit:      DeduplicationDefaultCandidateLimit,
		},
		SnapshotVersion: version, Attempts: 1,
		MergeLease: HistoricalDeduplicationMergeLease{
			ID: "rhl_publication", AcquiredAt: repositoryAuditTestNow,
			Groups: []HistoricalDeduplicationMergeGroup{{Members: []HistoricalDeduplicationFindingVersion{
				{ID: "rrf_a", Version: 1}, {ID: "rrf_b", Version: 1},
			}}},
		},
		UpdatedAt: repositoryAuditTestNow,
	}
}

func issuePublicationEligibleFixture() (RepositoryState, IssueDraft) {
	now := repositoryAuditTestNow
	draft := IssueDraft{
		ID: "rid_publication", Repository: "owner/repo", FindingIDs: []string{"finding"},
		Origin: IssueDraftOriginLegacy, Canonical: true, Title: "Issue preview",
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

func issuePublicationGroupedFixture() (RepositoryState, IssueDraft) {
	now := repositoryAuditTestNow
	draft := IssueDraft{
		ID: "rid_grouped_publication", Repository: "owner/repo",
		FindingIDs: []string{"pending", "processing", "failed", "duplicate"},
		Origin:     IssueDraftOriginLegacy, Canonical: true, Title: "Grouped preview",
		Body: "Grounded grouped diagnosis.", Labels: []string{"bug"}, State: IssueDraftEditing,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	makeFinding := func(id string) Finding {
		return Finding{
			ID: id, Repository: draft.Repository, Status: FindingOpen, IssueDraftID: draft.ID,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
	}
	findings := []Finding{
		makeFinding("pending"),
		makeFinding("processing"),
		makeFinding("failed"),
		makeFinding("duplicate"),
		makeFinding("candidate"),
	}
	provisionalID := stableID("rrf_", "grouped-provisional")
	candidateID := stableID("rrf_", "grouped-candidate")
	findings[3].RepositoryFindingID = provisionalID
	findings[3].RepositoryMatchState = RepositoryMatchProvisional
	findings[4].IssueDraftID = ""
	findings[4].RepositoryFindingID = candidateID
	findings[4].RepositoryMatchState = RepositoryMatchNew
	state := repositoryReviewCoverageState(draft.Repository)
	state.Findings = findings
	state.IssueDrafts = []IssueDraft{draft}
	state.MappingJobs = []RepositoryMappingJob{
		{
			ID: mappingJobID("pending"), ReviewFindingID: "pending", State: RepositoryMappingPending,
			CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: mappingJobID("processing"), ReviewFindingID: "processing", State: RepositoryMappingRunning,
			ReservedAt: now, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: mappingJobID("failed"), ReviewFindingID: "failed", State: RepositoryMappingPending,
			Attempts: RepositoryRunFindingStatusAttemptLimit, CreatedAt: now, UpdatedAt: now,
		},
	}
	state.RepositoryFindings = []RepositoryFinding{
		{
			ID: provisionalID, Repository: draft.Repository,
			CanonicalTitle: "Possible duplicate", CanonicalSeverity: "high",
			ReviewFindingIDs: []string{"duplicate"}, MatchState: RepositoryMatchProvisional,
			Lifecycle: RepositoryFindingOpen,
			Issue:     RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone},
			PossibleDuplicates: []RepositoryFindingPossibleDuplicate{{
				CandidateID: candidateID, Relation: "uncertain", Confidence: .82,
				Explanation: "The causal evidence is incomplete.", CreatedAt: now,
			}},
			ValidationState: RepositoryValidationNotRequested,
			Version:         1, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: candidateID, Repository: draft.Repository,
			CanonicalTitle: "Existing finding", CanonicalSeverity: "high",
			ReviewFindingIDs: []string{"candidate"}, MatchState: RepositoryMatchNew,
			Lifecycle:       RepositoryFindingOpen,
			Issue:           RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone},
			ValidationState: RepositoryValidationNotRequested,
			Version:         1, CreatedAt: now.Add(-1), UpdatedAt: now,
		},
	}
	return state, draft
}
