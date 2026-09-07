package repoaudit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const repositoryReviewCampaignTestProfile = "profile-campaign-v1"

var (
	repositoryReviewCampaignTestCommit    = strings.Repeat("a", 40)
	repositoryReviewCampaignOtherCommit   = strings.Repeat("b", 40)
	repositoryReviewCampaignTestInventory = strings.Repeat("c", 64)
)

func repositoryReviewCampaignTestScopeDigest(t *testing.T, files ...FileRef) string {
	t.Helper()
	digest, err := repositoryReviewCampaignScopeDigestForFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestRepositoryReviewCampaignIDAndBeginAuthorization(t *testing.T) {
	generated := NewRepositoryReviewCampaignID()
	if !ValidRepositoryReviewCampaignID(generated) {
		t.Fatalf("generated campaign ID %q is invalid", generated)
	}
	for _, invalid := range []string{
		"", "rrc_", "RRC_upper", "rrc_-leading", "rrc_has space", "other_campaign",
		"rrc_" + strings.Repeat("a", 125),
	} {
		if ValidRepositoryReviewCampaignID(invalid) {
			t.Errorf("invalid campaign ID %q was accepted", invalid)
		}
	}

	store := newRepositoryAuditTestStore(t)
	request := BeginCampaignRequest{
		DeduplicationSnapshot: repositoryReviewDeduplicationSnapshotForTest(),
		Repository:            "owner/repo", CampaignID: generated,
		CommitSHA: repositoryReviewCampaignTestCommit, ExpectedReviewVersion: 0, Exact: true,
	}
	started, beginErr := store.BeginCampaign(context.Background(), request)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	if started.Version != 1 || started.ReviewVersion != 1 || started.CurrentCampaign == nil ||
		started.CurrentCampaign.ID != generated || started.CurrentCampaign.CommitSHA != request.CommitSHA ||
		!started.CurrentCampaign.Exact || started.CurrentCampaign.Paths == nil ||
		repositoryReviewCampaignScopeBound(started.CurrentCampaign) {
		t.Fatalf("begun campaign state = %#v", started)
	}
	if metrics := CurrentCampaignMetrics(
		started,
		generated,
	); metrics.CoverageAvailable ||
		metrics.CoverageExact {
		t.Fatalf("unbound campaign was projected as known coverage: %#v", metrics)
	}

	replayed, replayErr := store.BeginCampaign(context.Background(), request)
	if replayErr != nil || replayed.Version != started.Version || replayed.ReviewVersion != started.ReviewVersion {
		t.Fatalf("idempotent begin = %#v err=%v", replayed, replayErr)
	}
	changed := request
	changed.CommitSHA = repositoryReviewCampaignOtherCommit
	if _, err := store.BeginCampaign(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-ID changed commit error = %v", err)
	}
	changed = request
	changed.Exact = false
	unchangedExact, downgradeErr := store.BeginCampaign(context.Background(), changed)
	if downgradeErr != nil || !unchangedExact.CurrentCampaign.Exact || unchangedExact.Version != started.Version {
		t.Fatalf("same-ID exactness downgrade = %#v err=%v", unchangedExact, downgradeErr)
	}

	file := repositoryAuditTestFile("pkg/service.go", "d", 80)
	plan, planErr := store.planAssignmentsForCampaignCountForTest(
		context.Background(), request.Repository, request.CommitSHA,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		request.CampaignID, 1, []FileRef{file}, false, 1, true,
	)
	if planErr != nil {
		t.Fatal(planErr)
	}
	bound, found, loadErr := store.Get(request.Repository)
	if loadErr != nil || !found || bound.CurrentCampaign == nil ||
		bound.CurrentCampaign.InventoryHash != repositoryReviewCampaignTestInventory ||
		bound.CurrentCampaign.ProfileHash != repositoryReviewCampaignTestProfile ||
		bound.CurrentCampaign.SelectedFiles != 1 || plan.StateVersion != bound.ReviewVersion {
		t.Fatalf("bound campaign state=%#v plan=%#v found=%v err=%v", bound, plan, found, loadErr)
	}

	if _, err := store.planAssignmentsForCampaignCountForTest(
		context.Background(), request.Repository, request.CommitSHA,
		strings.Repeat("e", 64), repositoryReviewCampaignTestProfile,
		request.CampaignID, 1, []FileRef{file}, false, 1, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed inventory error = %v", err)
	}
	unauthorizedID := NewRepositoryReviewCampaignID()
	if _, err := store.planAssignmentsForCampaignCountForTest(
		context.Background(), request.Repository, request.CommitSHA,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		unauthorizedID, 1, []FileRef{file}, false, 1, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("unauthorized plan error = %v", err)
	}

	staleReplacement := BeginCampaignRequest{
		DeduplicationSnapshot: repositoryReviewDeduplicationSnapshotForTest(),
		Repository:            request.Repository, CampaignID: unauthorizedID,
		CommitSHA: request.CommitSHA, ExpectedReviewVersion: started.ReviewVersion, Exact: true,
	}
	if _, err := store.BeginCampaign(context.Background(), staleReplacement); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale replacement error = %v", err)
	}
	staleReplacement.ExpectedReviewVersion = bound.ReviewVersion
	if _, err := store.BeginCampaign(context.Background(), staleReplacement); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement without prior campaign identity error = %v", err)
	}
	staleReplacement.ExpectedCampaignID = generated
	replaced, replaceErr := store.BeginCampaign(context.Background(), staleReplacement)
	if replaceErr != nil || replaced.CurrentCampaign == nil || replaced.CurrentCampaign.ID != unauthorizedID ||
		replaced.ReviewVersion != bound.ReviewVersion+1 {
		t.Fatalf("authorized replacement = %#v err=%v", replaced, replaceErr)
	}
}

func TestRepositoryReviewCampaignHistoryRejectsReuseIncludingZeroWork(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	repository := "owner/campaign-history"
	firstID, first := beginRepositoryReviewCampaignForTest(t, store, repository, true)
	secondID := NewRepositoryReviewCampaignID()
	second, beginErr := store.BeginCampaign(context.Background(), BeginCampaignRequest{
		DeduplicationSnapshot: repositoryReviewDeduplicationSnapshotForTest(),
		Repository:            repository, CampaignID: secondID, ExpectedCampaignID: firstID,
		CommitSHA:             repositoryReviewCampaignTestCommit,
		ExpectedReviewVersion: first.ReviewVersion, Exact: true,
	})
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	if _, err := store.BeginCampaign(context.Background(), BeginCampaignRequest{
		DeduplicationSnapshot: repositoryReviewDeduplicationSnapshotForTest(),
		Repository:            repository, CampaignID: firstID, ExpectedCampaignID: secondID,
		CommitSHA:             repositoryReviewCampaignTestCommit,
		ExpectedReviewVersion: second.ReviewVersion, Exact: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("zero-work campaign ID reuse error = %v", err)
	}
	loaded, _, loadErr := store.Get(repository)
	if loadErr != nil || len(loaded.CampaignHistory) != 2 ||
		loaded.CampaignHistory[firstID] != repositoryReviewCampaignTestCommit ||
		loaded.CampaignHistory[secondID] != repositoryReviewCampaignTestCommit {
		t.Fatalf("campaign history = %#v err=%v", loaded.CampaignHistory, loadErr)
	}
}

func TestRepositoryReviewCampaignScopeDigestRejectsSameCountUniverseChanges(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	repository := "owner/campaign-scope-digest"
	campaignID, _ := beginRepositoryReviewCampaignForTest(t, store, repository, true)
	first := repositoryAuditTestFile("a.go", "1", 10)
	second := repositoryAuditTestFile("b.go", "2", 20)
	if _, err := store.planAssignmentsForCampaignCountForTest(
		context.Background(), repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 1, []FileRef{first, second}, false, 2, true,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.planAssignmentsForCampaignCountForTest(
		context.Background(), repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 2, []FileRef{first, second}, false, 2, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("required assignment drift error = %v", err)
	}
	replacedPath := repositoryAuditTestFile("c.go", "3", 20)
	if _, err := store.planAssignmentsForCampaignCountForTest(
		context.Background(), repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 1, []FileRef{first, replacedPath}, false, 2, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-count changed path universe error = %v", err)
	}
	changedBlob := second
	changedBlob.BlobSHA = strings.Repeat("4", 40)
	if _, err := store.planAssignmentsForCampaignCountForTest(
		context.Background(), repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 1, []FileRef{first, changedBlob}, false, 2, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-path changed blob universe error = %v", err)
	}
	changedCategory := second
	changedCategory.Category = "test"
	if _, err := store.planAssignmentsForCampaignCountForTest(
		context.Background(), repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 1, []FileRef{first, changedCategory}, false, 2, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-path changed category universe error = %v", err)
	}
}

func TestRepositoryReviewCampaignMetricsIgnoreBoundedRunHistoryAndDeduplicateMappings(t *testing.T) {
	campaignID := NewRepositoryReviewCampaignID()
	otherCampaignID := NewRepositoryReviewCampaignID()
	state := RepositoryState{
		CurrentCampaign: &RepositoryReviewCampaignCoverage{
			ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
			InventoryHash: repositoryReviewCampaignTestInventory,
			ProfileHash:   repositoryReviewCampaignTestProfile,
			ScopeDigest: repositoryReviewCampaignTestScopeDigest(t,
				repositoryAuditTestFile("a.go", "1", 1),
				repositoryAuditTestFile("b.go", "2", 1),
				repositoryAuditTestFile("c.go", "3", 1)),
			RequiredAssignments: 1, SelectedFiles: 3, Exact: true,
			Paths: map[string]RepositoryReviewCampaignPathCoverage{
				"a.go": {Inspected: true, Completed: true},
				"b.go": {Inspected: true},
				"c.go": {Unsupported: true},
			},
		},
		Runs: make([]ReviewRun, 1000),
		Findings: []Finding{
			{ID: "rdf_one", CampaignID: campaignID, RepositoryFindingID: "rrf_shared"},
			{ID: "rdf_two", CampaignID: campaignID, RepositoryFindingID: "rrf_shared"},
			{ID: "rdf_three", CampaignID: campaignID, RepositoryFindingID: "rrf_other"},
			{ID: "rdf_pending", CampaignID: campaignID},
			{ID: "rdf_unrelated", CampaignID: otherCampaignID, RepositoryFindingID: "rrf_unrelated"},
		},
	}
	for index := range state.Runs {
		state.Runs[index] = ReviewRun{ID: fmt.Sprintf("newest-%04d", index)}
	}
	metrics := CurrentCampaignMetrics(state, campaignID)
	if metrics.InspectedFiles != 2 || metrics.CompletedFiles != 1 ||
		metrics.UnsupportedFiles != 1 || metrics.RemainingFiles != 1 ||
		metrics.FindingOccurrences != 4 || metrics.FindingAggregates != 2 ||
		metrics.PendingFindingMappings != 1 {
		t.Fatalf("campaign metrics = %#v", metrics)
	}
	state.Findings[2].RepositoryFindingID = "rrf_shared"
	merged := CurrentCampaignMetrics(state, campaignID)
	if merged.FindingOccurrences != 4 || merged.FindingAggregates != 1 ||
		merged.PendingFindingMappings != 1 {
		t.Fatalf("merged aggregate metrics = %#v", merged)
	}
}

func TestRepositoryReviewCampaignMetricsSurviveMoreThanThousandRuns(t *testing.T) {
	campaignID := NewRepositoryReviewCampaignID()
	state := RepositoryState{
		CurrentCampaign: &RepositoryReviewCampaignCoverage{
			ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
			InventoryHash: repositoryReviewCampaignTestInventory,
			ProfileHash:   repositoryReviewCampaignTestProfile,
			ScopeDigest: repositoryReviewCampaignTestScopeDigest(
				t, repositoryAuditTestFile("service.go", "4", 1),
			),
			RequiredAssignments: 1, SelectedFiles: 1, Exact: true,
			Paths: map[string]RepositoryReviewCampaignPathCoverage{
				"service.go": {Inspected: true},
			},
		},
		// The oldest of 1,001 campaign runs has already fallen out of the
		// bounded run ledger, while its immutable tagged finding remains.
		Runs: make([]ReviewRun, 1000),
	}
	for index := range 1001 {
		state.Findings = append(state.Findings, Finding{
			ID: fmt.Sprintf("rdf_campaign_%04d", index), CampaignID: campaignID,
		})
		if index > 0 {
			state.Runs[index-1] = ReviewRun{
				ID: fmt.Sprintf("run_campaign_%04d", index), CampaignID: campaignID,
			}
		}
	}
	metrics := CurrentCampaignMetrics(state, campaignID)
	if metrics.FindingOccurrences != 1001 || metrics.PendingFindingMappings != 1001 ||
		metrics.InspectedFiles != 1 || metrics.RemainingFiles != 1 {
		t.Fatalf("truncated campaign metrics = %#v", metrics)
	}
}

func TestRepositoryReviewCampaignHistoryValidationBounds(t *testing.T) {
	if err := validateRepositoryReviewCampaignHistory(map[string]string{
		"bad": repositoryReviewCampaignTestCommit,
	}); err == nil {
		t.Fatal("malformed campaign history ID was accepted")
	}
	if err := validateRepositoryReviewCampaignHistory(map[string]string{
		NewRepositoryReviewCampaignID(): "main",
	}); err == nil {
		t.Fatal("malformed campaign history commit was accepted")
	}
	overflow := make(map[string]string, maxReviewFiles+1)
	for index := 0; index <= maxReviewFiles; index++ {
		overflow[fmt.Sprintf("rrc_history_%06d", index)] = repositoryReviewCampaignTestCommit
	}
	if err := validateRepositoryReviewCampaignHistory(overflow); err == nil {
		t.Fatal("campaign history cardinality overflow was accepted")
	}
}

func TestRepositoryReviewCampaignValidationRejectsDuplicateRecordIDs(t *testing.T) {
	campaignID := NewRepositoryReviewCampaignID()
	base := repositoryReviewCoverageState("owner/campaign-duplicate-ids")
	base.CampaignHistory = map[string]string{campaignID: repositoryReviewCampaignTestCommit}
	tests := []struct {
		name   string
		mutate func(*RepositoryState)
	}{
		{
			name: "tagged and untagged run collision",
			mutate: func(state *RepositoryState) {
				state.Runs = []ReviewRun{
					{
						ID: "same", CampaignID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
						ProfileHash: repositoryReviewCampaignTestProfile,
						ScopeDigest: repositoryReviewCampaignTestScopeDigest(
							t, repositoryAuditTestFile("a.go", "1", 1),
						),
					},
					{ID: "same"},
				}
			},
		},
		{
			name: "context collision",
			mutate: func(state *RepositoryState) {
				state.Contexts = []FindingContext{{ID: "same"}, {
					ID: "same", CampaignID: campaignID,
					CommitSHA:   repositoryReviewCampaignTestCommit,
					ProfileHash: repositoryReviewCampaignTestProfile,
				}}
			},
		},
		{
			name: "finding collision",
			mutate: func(state *RepositoryState) {
				state.Findings = []Finding{{ID: "same"}, {
					ID: "same", CampaignID: campaignID,
					CommitSHA: repositoryReviewCampaignTestCommit,
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := base
			test.mutate(&state)
			if err := validateState(state); err == nil {
				t.Fatal("duplicate record identity was accepted")
			}
		})
	}
}

func TestRepositoryReviewCampaignValidationRejectsOrphanTaggedFindings(t *testing.T) {
	campaignID := NewRepositoryReviewCampaignID()
	for name, state := range map[string]RepositoryState{
		"no contexts": {
			Findings: []Finding{{ID: "finding", CampaignID: campaignID}},
		},
		"missing context": {
			Findings: []Finding{{
				ID: "finding", CampaignID: campaignID, ContextIDs: []string{"missing"},
			}},
		},
		"run missing finding": {
			Runs: []ReviewRun{{
				ID: "run", CampaignID: campaignID, FindingIDs: []string{"missing"},
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRepositoryReviewCampaignRecordBindings(state); err == nil {
				t.Fatal("orphan campaign record binding was accepted")
			}
		})
	}
	valid := RepositoryState{
		Contexts: []FindingContext{{ID: "context", CampaignID: campaignID}},
		RawFindings: []RawReviewFinding{{
			ID: "raw-finding", CampaignID: campaignID, ContextID: "context",
		}},
		Runs: []ReviewRun{{
			ID: "run", CampaignID: campaignID, FindingIDs: []string{"raw-finding"},
		}},
	}
	if err := validateRepositoryReviewCampaignRecordBindings(valid); err != nil {
		t.Fatalf("valid campaign record topology error = %v", err)
	}
}

func TestRepositoryReviewAutomationPersistsOptionalCampaignID(t *testing.T) {
	store := newAutomationTestStore(t)
	input := validAutomationForTest("rra_campaign", "Campaign")
	input.CampaignID = NewRepositoryReviewCampaignID()
	created, err := store.CreateAutomation(context.Background(), input)
	if err != nil || created.CampaignID != input.CampaignID {
		t.Fatalf("created automation=%#v err=%v", created, err)
	}
	loaded, found, err := store.GetAutomation(context.Background(), created.ID)
	if err != nil || !found || loaded.CampaignID != input.CampaignID {
		t.Fatalf("loaded automation=%#v found=%v err=%v", loaded, found, err)
	}
	invalid := validAutomationForTest("rra_bad_campaign", "Bad campaign")
	invalid.CampaignID = "not-a-campaign"
	if _, err := store.CreateAutomation(context.Background(), invalid); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("invalid automation campaign error = %v", err)
	}
}

func beginRepositoryReviewCampaignForTest(
	t *testing.T,
	store Store,
	repository string,
	exact bool,
) (string, RepositoryState) {
	t.Helper()
	current, _, err := store.Get(repository)
	if err != nil {
		t.Fatal(err)
	}
	campaignID := NewRepositoryReviewCampaignID()
	state, err := store.BeginCampaign(context.Background(), BeginCampaignRequest{
		DeduplicationSnapshot: repositoryReviewDeduplicationSnapshotForTest(),
		Repository:            repository, CampaignID: campaignID,
		CommitSHA:             repositoryReviewCampaignTestCommit,
		ExpectedReviewVersion: current.ReviewVersion, Exact: exact,
	})
	if err != nil {
		t.Fatal(err)
	}
	return campaignID, state
}

func repositoryReviewCampaignFinding(file FileRef, title string) FindingCandidate {
	return FindingCandidate{
		Severity: "high", Title: title, Symbol: "Save", File: file.Path,
		Message:  "The write loses a concurrent update.",
		Evidence: "The immutable trace confirms the failure.", Impact: "State is lost.",
		Validation: Validation{Status: "confirmed", Summary: "Confirmed against the assigned file."},
		MatchHints: MatchHints{
			Component: "persistence", Operation: "save state",
			FailureMode: "a concurrent update is overwritten", Trigger: "overlapping writes",
			ViolatedInvariant: "accepted updates remain durable", ObservableOutcome: "state is lost",
			RelatedSymbols: []string{"Save"}, SourceAnchors: []string{"version"},
			DistinguishingFacts: []string{"requires overlapping writes"},
		},
		FixEffort: FixEffort{
			Quick: FixEffortEstimate{
				LOCMin: 5, LOCMax: 20, Class: "small", Rationale: "Containment is local.",
			},
			Quality: FixEffortEstimate{
				LOCMin: 30, LOCMax: 100, Class: "medium", Rationale: "Several units participate.",
			},
		},
	}
}

func cloneRepositoryReviewCampaignCoverageForTest(
	coverage *RepositoryReviewCampaignCoverage,
) *RepositoryReviewCampaignCoverage {
	clone := *coverage
	clone.Paths = make(map[string]RepositoryReviewCampaignPathCoverage, len(coverage.Paths))
	for pathValue, pathCoverage := range coverage.Paths {
		clone.Paths[pathValue] = pathCoverage
	}
	return &clone
}
