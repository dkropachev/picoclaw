package repoaudit

import (
	"context"
	"encoding/json"
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
		Repository: "owner/repo", CampaignID: generated,
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
		nil,
		repositoryAuditTestNow,
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
	plan, planErr := store.PlanWithProfileLimitAuthoritativeForCampaign(
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

	if _, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
		context.Background(), request.Repository, request.CommitSHA,
		strings.Repeat("e", 64), repositoryReviewCampaignTestProfile,
		request.CampaignID, 1, []FileRef{file}, false, 1, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed inventory error = %v", err)
	}
	unauthorizedID := NewRepositoryReviewCampaignID()
	if _, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
		context.Background(), request.Repository, request.CommitSHA,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		unauthorizedID, 1, []FileRef{file}, false, 1, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("unauthorized plan error = %v", err)
	}

	staleReplacement := BeginCampaignRequest{
		Repository: request.Repository, CampaignID: unauthorizedID,
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
		Repository: repository, CampaignID: secondID, ExpectedCampaignID: firstID,
		CommitSHA:             repositoryReviewCampaignTestCommit,
		ExpectedReviewVersion: first.ReviewVersion, Exact: true,
	})
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	if _, err := store.BeginCampaign(context.Background(), BeginCampaignRequest{
		Repository: repository, CampaignID: firstID, ExpectedCampaignID: secondID,
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

func TestRepositoryReviewCampaignReconcileBackfillAndExactPromotion(t *testing.T) {
	store, legacy := repositoryReviewCoverageStore(t, "owner/campaign-backfill")
	legacyFile := repositoryAuditTestFile("legacy.go", "8", 1)
	legacyPlan := Plan{
		Repository: legacy.Repository, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash:   repositoryReviewCampaignTestProfile, Authoritative: true,
		PendingFiles: []FileRef{legacyFile}, UnchangedFiles: []FileRef{},
		CreatedAt: repositoryAuditTestNow,
	}
	legacyPlan.ID = planDigest(legacyPlan)
	legacy.Runs = []ReviewRun{{
		ID: "legacy-run", PlanID: legacyPlan.ID, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		FindingIDs:    []string{"rfn_legacy"}, UnreviewedFiles: 1,
	}}
	legacy.Contexts = []FindingContext{{
		ID: "rctx_legacy", Repository: legacy.Repository,
		CommitSHA:     repositoryReviewCampaignTestCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash:   repositoryReviewCampaignTestProfile, RunID: "legacy-run",
		Files: []FileRef{legacyFile},
	}, {
		ID: "rctx_unrelated", Repository: legacy.Repository,
		CommitSHA:     repositoryReviewCampaignOtherCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash:   repositoryReviewCampaignTestProfile, RunID: "legacy-run",
		Files: []FileRef{legacyFile},
	}}
	legacy.Findings = []Finding{{
		ID: "rfn_legacy", Repository: legacy.Repository,
		CommitSHA: repositoryReviewCampaignTestCommit,
		File:      legacyFile, ContextIDs: []string{"rctx_legacy"},
	}}
	if err := store.save(&legacy); err != nil {
		t.Fatal(err)
	}
	campaignID, begun := beginRepositoryReviewCampaignForTest(
		t, store, legacy.Repository, false,
	)
	if _, err := store.BeginCampaign(context.Background(), BeginCampaignRequest{
		Repository: legacy.Repository, CampaignID: campaignID,
		CommitSHA:             repositoryReviewCampaignTestCommit,
		ExpectedReviewVersion: begun.ReviewVersion, Exact: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("BeginCampaign improperly promoted unknown coverage: %v", err)
	}
	coverage := RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash:       repositoryReviewCampaignTestInventory,
		ProfileHash:         repositoryReviewCampaignTestProfile,
		ScopeDigest:         repositoryReviewCampaignTestScopeDigest(t, legacyFile),
		RequiredAssignments: 1, SelectedFiles: 1, Exact: false,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{
			"legacy.go": {Inspected: true},
		},
	}
	request := ReconcileCampaignRequest{
		Repository: legacy.Repository, ExpectedReviewVersion: begun.ReviewVersion,
		Coverage: coverage, SelectedScope: []FileRef{legacyFile},
		Runs: []RepositoryReviewCampaignRunRecovery{{
			ID: "legacy-run", Plan: legacyPlan, InspectedFiles: 1,
		}},
		ContextIDs: []string{"rctx_legacy"}, FindingIDs: []string{"rfn_legacy"},
	}
	arbitrary := request
	arbitrary.Coverage = cloneRepositoryReviewCampaignCoverage(coverage)
	arbitrary.Coverage.Paths["outside.go"] = RepositoryReviewCampaignPathCoverage{Inspected: true}
	if _, err := store.ReconcileCampaign(context.Background(), arbitrary); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("outside-manifest recovered path error = %v", err)
	}
	reconciled, reconcileErr := store.ReconcileCampaign(context.Background(), request)
	if reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	if reconciled.CurrentCampaign == nil || reconciled.CurrentCampaign.Exact ||
		!reconciled.CurrentCampaign.Paths["legacy.go"].Inspected ||
		reconciled.Runs[0].CampaignID != campaignID ||
		reconciled.Runs[0].InspectedFiles != 1 ||
		reconciled.Runs[0].ProfileHash != coverage.ProfileHash ||
		reconciled.Runs[0].ScopeDigest != coverage.ScopeDigest ||
		reconciled.Contexts[0].CampaignID != campaignID ||
		reconciled.Findings[0].CampaignID != campaignID {
		t.Fatalf("reconciled campaign = %#v", reconciled)
	}
	originalPathCoverage := request.Coverage.Paths["legacy.go"]
	request.Coverage.Paths["legacy.go"] = RepositoryReviewCampaignPathCoverage{Unsupported: true}
	detached, _, loadErr := store.Get(legacy.Repository)
	if loadErr != nil || detached.CurrentCampaign.Paths["legacy.go"] != originalPathCoverage {
		t.Fatalf("caller-owned recovered coverage aliased durable state: %#v err=%v", detached, loadErr)
	}
	request.Coverage.Paths["legacy.go"] = originalPathCoverage
	replayed, replayErr := store.ReconcileCampaign(context.Background(), request)
	if replayErr != nil || replayed.Version != reconciled.Version ||
		replayed.ReviewVersion != reconciled.ReviewVersion {
		t.Fatalf("reconcile replay = %#v err=%v", replayed, replayErr)
	}

	promotion := request
	promotion.ExpectedReviewVersion = reconciled.ReviewVersion
	promotion.Coverage = cloneRepositoryReviewCampaignCoverage(coverage)
	promotion.Coverage.Exact = true
	promotion.Coverage.Paths["legacy.go"] = RepositoryReviewCampaignPathCoverage{
		Inspected: true, Completed: true,
	}
	stalePromotion := promotion
	stalePromotion.ExpectedReviewVersion = begun.ReviewVersion
	if _, err := store.ReconcileCampaign(
		context.Background(), stalePromotion,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale reconcile error = %v", err)
	}
	afterStale, _, staleLoadErr := store.Get(legacy.Repository)
	if staleLoadErr != nil || afterStale.CurrentCampaign.Exact ||
		afterStale.CurrentCampaign.Paths["legacy.go"].Completed {
		t.Fatalf("stale recovery escaped into durable state: %#v err=%v", afterStale, staleLoadErr)
	}
	promoted, promotionErr := store.ReconcileCampaign(context.Background(), promotion)
	if promotionErr != nil || promoted.CurrentCampaign == nil || !promoted.CurrentCampaign.Exact ||
		!promoted.CurrentCampaign.Paths["legacy.go"].Completed {
		t.Fatalf("exact promotion = %#v err=%v", promoted, promotionErr)
	}

	downgrade := request
	downgrade.ExpectedReviewVersion = promoted.ReviewVersion
	downgrade.Coverage = cloneRepositoryReviewCampaignCoverage(promotion.Coverage)
	downgrade.Coverage.Exact = false
	unchanged, downgradeErr := store.ReconcileCampaign(context.Background(), downgrade)
	if downgradeErr != nil || !unchanged.CurrentCampaign.Exact ||
		unchanged.Version != promoted.Version+1 || unchanged.ReviewVersion != promoted.ReviewVersion+1 {
		t.Fatalf("exactness downgrade = %#v err=%v", unchanged, downgradeErr)
	}
	unrelated := downgrade
	unrelated.ExpectedReviewVersion = unchanged.ReviewVersion
	unrelated.ContextIDs = append(unrelated.ContextIDs, "rctx_unrelated")
	if _, err := store.ReconcileCampaign(context.Background(), unrelated); !errors.Is(err, ErrConflict) {
		t.Fatalf("unrelated context recovery error = %v", err)
	}
	beginReplay, beginReplayErr := store.BeginCampaign(context.Background(), BeginCampaignRequest{
		Repository: legacy.Repository, CampaignID: campaignID,
		CommitSHA:             repositoryReviewCampaignTestCommit,
		ExpectedReviewVersion: begun.ReviewVersion, Exact: false,
	})
	if beginReplayErr != nil || !beginReplay.CurrentCampaign.Exact || beginReplay.Version != unchanged.Version {
		t.Fatalf("post-promotion BeginCampaign replay = %#v err=%v", beginReplay, beginReplayErr)
	}

	wrong := promotion
	wrong.ExpectedReviewVersion = promoted.ReviewVersion
	wrong.Coverage.ID = NewRepositoryReviewCampaignID()
	if _, err := store.ReconcileCampaign(context.Background(), wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong-authority reconcile error = %v", err)
	}

	left := FindingContext{CampaignID: campaignID, Repository: legacy.Repository, RunID: "run"}
	right := left
	right.CampaignID = NewRepositoryReviewCampaignID()
	if contextBindingDigest(left) == contextBindingDigest(right) {
		t.Fatal("finding context digest does not bind CampaignID")
	}
}

func TestRepositoryReviewCampaignReconcileRejectsMissingRunAtFullRetention(t *testing.T) {
	store, state := repositoryReviewCoverageStore(t, "owner/campaign-pruned-run")
	state.Runs = make([]ReviewRun, maxAutomationRunIDs)
	for index := range state.Runs {
		state.Runs[index] = ReviewRun{ID: fmt.Sprintf("retained-%04d", index)}
	}
	file := repositoryAuditTestFile("legacy.go", "9", 1)
	state.Contexts = []FindingContext{{
		ID: "rctx_pruned", Repository: state.Repository,
		CommitSHA:     repositoryReviewCampaignTestCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash:   repositoryReviewCampaignTestProfile, RunID: "pruned-run",
		Files: []FileRef{file},
	}}
	state.Findings = []Finding{{
		ID: "rfn_pruned", Repository: state.Repository,
		CommitSHA: repositoryReviewCampaignTestCommit, File: file,
		ContextIDs: []string{"rctx_pruned"},
	}}
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	campaignID, begun := beginRepositoryReviewCampaignForTest(t, store, state.Repository, false)
	coverage := RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash:       repositoryReviewCampaignTestInventory,
		ProfileHash:         repositoryReviewCampaignTestProfile,
		ScopeDigest:         repositoryReviewCampaignTestScopeDigest(t, file),
		RequiredAssignments: 1, SelectedFiles: 1, Paths: map[string]RepositoryReviewCampaignPathCoverage{
			file.Path: {Inspected: true},
		},
	}
	if _, err := store.ReconcileCampaign(context.Background(), ReconcileCampaignRequest{
		Repository: state.Repository, ExpectedReviewVersion: begun.ReviewVersion,
		Coverage: coverage, SelectedScope: []FileRef{file},
		ContextIDs: []string{"rctx_pruned"}, FindingIDs: []string{"rfn_pruned"},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("full-retention missing run recovery error = %v", err)
	}
}

func TestRepositoryReviewCampaignReconcileRejectsFindingAbsentFromContext(t *testing.T) {
	store, state := repositoryReviewCoverageStore(t, "owner/campaign-context-primary")
	primary := repositoryAuditTestFile("primary.go", "a", 1)
	other := repositoryAuditTestFile("other.go", "b", 1)
	plan := Plan{
		Repository: state.Repository, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash:   repositoryReviewCampaignTestProfile, Authoritative: true,
		PendingFiles: []FileRef{other, primary}, UnchangedFiles: []FileRef{},
		CreatedAt: repositoryAuditTestNow,
	}
	plan.ID = planDigest(plan)
	state.Runs = []ReviewRun{{
		ID: "legacy-run", PlanID: plan.ID, CommitSHA: plan.CommitSHA,
		InventoryHash: plan.InventoryHash, FindingIDs: []string{"rfn_bad_context"},
		UnreviewedFiles: 2,
	}}
	state.Contexts = []FindingContext{{
		ID: "rctx_other", Repository: state.Repository, CommitSHA: plan.CommitSHA,
		InventoryHash: plan.InventoryHash, ProfileHash: plan.ProfileHash,
		RunID: "legacy-run", Files: []FileRef{other},
	}}
	state.Findings = []Finding{{
		ID: "rfn_bad_context", Repository: state.Repository, CommitSHA: plan.CommitSHA,
		File: primary, ContextIDs: []string{"rctx_other"},
	}}
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	campaignID, begun := beginRepositoryReviewCampaignForTest(t, store, state.Repository, false)
	coverage := RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: plan.CommitSHA, InventoryHash: plan.InventoryHash,
		ProfileHash:         plan.ProfileHash,
		ScopeDigest:         repositoryReviewCampaignTestScopeDigest(t, other, primary),
		RequiredAssignments: 1, SelectedFiles: 2, Paths: map[string]RepositoryReviewCampaignPathCoverage{
			primary.Path: {Inspected: true}, other.Path: {Inspected: true},
		},
	}
	if _, err := store.ReconcileCampaign(context.Background(), ReconcileCampaignRequest{
		Repository: state.Repository, ExpectedReviewVersion: begun.ReviewVersion,
		Coverage: coverage, SelectedScope: []FileRef{other, primary},
		Runs:       []RepositoryReviewCampaignRunRecovery{{ID: "legacy-run", Plan: plan, InspectedFiles: 2}},
		ContextIDs: []string{"rctx_other"}, FindingIDs: []string{"rfn_bad_context"},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("misattributed finding context recovery error = %v", err)
	}
}

func TestRepositoryReviewCampaignReconcileRejectsTaggedZeroInspectionRewrite(t *testing.T) {
	store, state := repositoryReviewCoverageStore(t, "owner/campaign-tagged-zero")
	file := repositoryAuditTestFile("zero.go", "d", 1)
	plan := Plan{
		Repository: state.Repository, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash:   repositoryReviewCampaignTestProfile, Authoritative: true,
		PendingFiles: []FileRef{file}, UnchangedFiles: []FileRef{}, CreatedAt: repositoryAuditTestNow,
	}
	plan.ID = planDigest(plan)
	state.Runs = []ReviewRun{{
		ID: "legacy-zero", PlanID: plan.ID, CommitSHA: plan.CommitSHA,
		InventoryHash: plan.InventoryHash, UnreviewedFiles: 1,
	}}
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	campaignID, begun := beginRepositoryReviewCampaignForTest(t, store, state.Repository, false)
	coverage := RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: plan.CommitSHA, InventoryHash: plan.InventoryHash,
		ProfileHash:         plan.ProfileHash,
		ScopeDigest:         repositoryReviewCampaignTestScopeDigest(t, file),
		RequiredAssignments: 1, SelectedFiles: 1,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{},
	}
	base := ReconcileCampaignRequest{
		Repository: state.Repository, ExpectedReviewVersion: begun.ReviewVersion,
		Coverage: coverage, SelectedScope: []FileRef{file},
		Runs: []RepositoryReviewCampaignRunRecovery{{ID: "legacy-zero", Plan: plan}},
	}
	tagged, err := store.ReconcileCampaign(context.Background(), base)
	if err != nil || tagged.Runs[0].CampaignID != campaignID || tagged.Runs[0].InspectedFiles != 0 ||
		tagged.Runs[0].ProfileHash != plan.ProfileHash || tagged.Runs[0].ScopeDigest != coverage.ScopeDigest {
		t.Fatalf("tagged zero run=%#v err=%v", tagged.Runs, err)
	}
	rewrite := base
	rewrite.ExpectedReviewVersion = tagged.ReviewVersion
	rewrite.Runs = []RepositoryReviewCampaignRunRecovery{{
		ID: "legacy-zero", Plan: plan, InspectedFiles: 1,
	}}
	if _, err := store.ReconcileCampaign(context.Background(), rewrite); !errors.Is(err, ErrConflict) {
		t.Fatalf("tagged zero inspection rewrite error = %v", err)
	}
}

func TestRepositoryReviewCampaignReconcileIndexesLargeTagSet(t *testing.T) {
	store, state := repositoryReviewCoverageStore(t, "owner/campaign-large-recovery")
	file := repositoryAuditTestFile("large.go", "e", 1)
	campaignID := NewRepositoryReviewCampaignID()
	scopeDigest := repositoryReviewCampaignTestScopeDigest(t, file)
	state.CurrentCampaign = &RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash:   repositoryReviewCampaignTestProfile, ScopeDigest: scopeDigest,
		RequiredAssignments: 1, SelectedFiles: 1,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{file.Path: {Inspected: true}},
	}
	state.CampaignHistory = map[string]string{campaignID: repositoryReviewCampaignTestCommit}
	state.Runs = []ReviewRun{{
		ID: "tagged-run", CampaignID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash:   repositoryReviewCampaignTestProfile, ScopeDigest: scopeDigest,
	}}
	const records = 5000
	contextIDs := make([]string, 0, records)
	findingIDs := make([]string, 0, records)
	for index := range records {
		contextID := fmt.Sprintf("rctx_large_%04d", index)
		findingID := fmt.Sprintf("rfn_large_%04d", index)
		contextIDs = append(contextIDs, contextID)
		findingIDs = append(findingIDs, findingID)
		state.Contexts = append(state.Contexts, FindingContext{
			ID: contextID, Repository: state.Repository,
			CommitSHA:     repositoryReviewCampaignTestCommit,
			InventoryHash: repositoryReviewCampaignTestInventory,
			ProfileHash:   repositoryReviewCampaignTestProfile,
			RunID:         "tagged-run", Files: []FileRef{file},
		})
		state.Findings = append(state.Findings, Finding{
			ID: findingID, Repository: state.Repository,
			CommitSHA: repositoryReviewCampaignTestCommit, File: file,
			ContextIDs: []string{contextID},
		})
	}
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	reconciled, err := store.ReconcileCampaign(context.Background(), ReconcileCampaignRequest{
		Repository: state.Repository, ExpectedReviewVersion: state.ReviewVersion,
		Coverage: *state.CurrentCampaign, SelectedScope: []FileRef{file},
		ContextIDs: contextIDs, FindingIDs: findingIDs,
	})
	if err != nil || len(reconciled.Contexts) != records || len(reconciled.Findings) != records ||
		reconciled.Contexts[records-1].CampaignID != campaignID ||
		reconciled.Findings[records-1].CampaignID != campaignID {
		t.Fatalf("large indexed recovery contexts=%d findings=%d err=%v",
			len(reconciled.Contexts), len(reconciled.Findings), err)
	}
}

func TestRepositoryReviewCampaignRecordCoverageTagsAndPromotion(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	repository := "owner/campaign-record"
	campaignID, _ := beginRepositoryReviewCampaignForTest(t, store, repository, true)
	files := []FileRef{
		repositoryAuditTestFile("pkg/first.go", "1", 80),
		repositoryAuditTestFile("pkg/second.go", "2", 90),
	}
	plan := planRepositoryReviewCampaignForTest(t, store, repository, campaignID, files, false, 2)
	request := RecordRequest{
		Plan: plan, RunID: "campaign-partial", CompletedAt: repositoryAuditTestNow,
		InspectedFiles: []FileRef{files[0]}, CompletedFiles: []FileRef{},
		Observations: []Observation{{
			Model: "review-a", ScopeFiles: files,
			Findings: []FindingCandidate{repositoryReviewCampaignFinding(files[0], "Partial finding")},
		}},
	}
	request.ReviewEvidence = []RepositoryReviewEvidence{
		repositoryReviewCampaignSuccessfulEvidence(files, []FileRef{files[0]}, request.Observations[0], true),
		{AssignmentID: "assignment-failed", ScopeFiles: files, Required: true},
	}
	partial, err := store.Record(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Run.CampaignID != campaignID || partial.Run.InspectedFiles != 1 ||
		partial.Run.ReviewedFiles != 0 || len(partial.State.Files) != 0 ||
		len(partial.State.Findings) != 1 || len(partial.State.Contexts) != 1 ||
		partial.State.Findings[0].CampaignID != campaignID ||
		partial.State.Contexts[0].CampaignID != campaignID {
		t.Fatalf("partial campaign result = %#v", partial)
	}
	metrics := CurrentCampaignMetrics(partial.State, campaignID, nil, repositoryAuditTestNow)
	if !metrics.CoverageAvailable || !metrics.CoverageExact || metrics.SelectedFiles != 2 ||
		metrics.InspectedFiles != 1 || metrics.CompletedFiles != 0 || metrics.RemainingFiles != 2 ||
		metrics.FindingOccurrences != 1 || metrics.FindingAggregates != 0 ||
		metrics.PendingFindingMappings != 1 {
		t.Fatalf("partial campaign metrics = %#v", metrics)
	}

	promotionPlan := planRepositoryReviewCampaignForTest(t, store, repository, campaignID, files, false, 2)
	promotionRequest := RecordRequest{
		Plan: promotionPlan, RunID: "campaign-promotion", CompletedAt: repositoryAuditTestNow,
		InspectedFiles: []FileRef{files[0]}, CompletedFiles: []FileRef{files[0]},
		Observations: []Observation{{Model: "review-a", ScopeFiles: []FileRef{files[0]}}},
	}
	promotionRequest.ReviewEvidence = []RepositoryReviewEvidence{
		repositoryReviewCampaignSuccessfulEvidence(
			[]FileRef{files[0]}, []FileRef{files[0]}, promotionRequest.Observations[0], true,
		),
	}
	secondPromotionEvidence := repositoryReviewCampaignSuccessfulEvidence(
		[]FileRef{files[0]}, []FileRef{files[0]}, promotionRequest.Observations[0], true,
	)
	secondPromotionEvidence.AssignmentID = "assignment-success-second"
	promotionRequest.ReviewEvidence = append(promotionRequest.ReviewEvidence, secondPromotionEvidence)
	promotionRequest.ReviewEvidence = append(promotionRequest.ReviewEvidence,
		RepositoryReviewEvidence{
			AssignmentID: "assignment-second-file-failed", ScopeFiles: []FileRef{files[1]}, Required: true,
		},
		RepositoryReviewEvidence{
			AssignmentID: "assignment-second-file-failed-second", ScopeFiles: []FileRef{files[1]}, Required: true,
		},
	)
	promoted, err := store.Record(context.Background(), promotionRequest)
	if err != nil {
		t.Fatal(err)
	}
	metrics = CurrentCampaignMetrics(promoted.State, campaignID, nil, repositoryAuditTestNow)
	if metrics.InspectedFiles != 1 || metrics.CompletedFiles != 1 || metrics.RemainingFiles != 1 ||
		promoted.Run.InspectedFiles != 1 || promoted.Run.ReviewedFiles != 1 {
		t.Fatalf("promoted result=%#v metrics=%#v", promoted.Run, metrics)
	}
	replayed, err := store.Record(context.Background(), promotionRequest)
	if err != nil || replayed.State.Version != promoted.State.Version ||
		len(replayed.State.Runs) != len(promoted.State.Runs) {
		t.Fatalf("campaign replay = %#v err=%v", replayed, err)
	}

	zeroFindingPlan := planRepositoryReviewCampaignForTest(t, store, repository, campaignID, files, false, 2)
	zeroObservation := Observation{Model: "review-a", ScopeFiles: []FileRef{files[1]}}
	zeroFinding, err := store.Record(context.Background(), RecordRequest{
		Plan: zeroFindingPlan, RunID: "campaign-zero-finding", CompletedAt: repositoryAuditTestNow,
		InspectedFiles: []FileRef{files[1]}, CompletedFiles: []FileRef{},
		Observations: []Observation{zeroObservation},
		ReviewEvidence: []RepositoryReviewEvidence{
			repositoryReviewCampaignSuccessfulEvidence(
				[]FileRef{files[1]}, []FileRef{files[1]}, zeroObservation, false,
			),
			{AssignmentID: "assignment-required-failed", ScopeFiles: []FileRef{files[1]}, Required: true},
			{AssignmentID: "assignment-required-failed-second", ScopeFiles: []FileRef{files[1]}, Required: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	metrics = CurrentCampaignMetrics(zeroFinding.State, campaignID, nil, repositoryAuditTestNow)
	if metrics.InspectedFiles != 2 || metrics.CompletedFiles != 1 ||
		len(zeroFinding.State.Contexts) != 1 || len(zeroFinding.State.Findings) != 1 {
		t.Fatalf("zero-finding result=%#v metrics=%#v", zeroFinding.State, metrics)
	}
}

func TestRepositoryReviewCampaignRecordBindsAuthorizedUnboundScope(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	repository := "owner/campaign-record-binding"
	campaignID, begun := beginRepositoryReviewCampaignForTest(t, store, repository, true)
	file := repositoryAuditTestFile("service.go", "7", 15)
	plan := Plan{
		CampaignID: campaignID, Repository: repository,
		CommitSHA:           repositoryReviewCampaignTestCommit,
		InventoryHash:       repositoryReviewCampaignTestInventory,
		ProfileHash:         repositoryReviewCampaignTestProfile,
		RequiredAssignments: 1, Authoritative: true, TargetIsDefault: true,
		StateVersion: begun.ReviewVersion, PendingFiles: []FileRef{file},
		UnchangedFiles: []FileRef{}, CreatedAt: repositoryAuditTestNow,
	}
	plan.ID = planDigest(plan)
	directObservation := Observation{Model: "review-a", ScopeFiles: []FileRef{file}}
	result, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "direct-binding", CompletedAt: repositoryAuditTestNow,
		InspectedFiles: []FileRef{file}, CompletedFiles: []FileRef{},
		Observations: []Observation{directObservation},
		ReviewEvidence: []RepositoryReviewEvidence{
			repositoryReviewCampaignSuccessfulEvidence(
				[]FileRef{file}, []FileRef{file}, directObservation, false,
			),
			{AssignmentID: "assignment-required-failed", ScopeFiles: []FileRef{file}, Required: true},
		},
	})
	if err != nil || result.State.CurrentCampaign == nil ||
		result.State.CurrentCampaign.InventoryHash != plan.InventoryHash ||
		result.State.CurrentCampaign.ProfileHash != plan.ProfileHash ||
		result.State.CurrentCampaign.SelectedFiles != 1 ||
		!result.State.CurrentCampaign.Paths[file.Path].Inspected {
		t.Fatalf("directly bound campaign result=%#v err=%v", result, err)
	}
}

func TestRepositoryReviewCampaignScopeDigestRejectsSameCountUniverseChanges(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	repository := "owner/campaign-scope-digest"
	campaignID, _ := beginRepositoryReviewCampaignForTest(t, store, repository, true)
	first := repositoryAuditTestFile("a.go", "1", 10)
	second := repositoryAuditTestFile("b.go", "2", 20)
	if _, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
		context.Background(), repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 1, []FileRef{first, second}, false, 2, true,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
		context.Background(), repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 2, []FileRef{first, second}, false, 2, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("required assignment drift error = %v", err)
	}
	replacedPath := repositoryAuditTestFile("c.go", "3", 20)
	if _, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
		context.Background(), repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 1, []FileRef{first, replacedPath}, false, 2, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-count changed path universe error = %v", err)
	}
	changedBlob := second
	changedBlob.BlobSHA = strings.Repeat("4", 40)
	if _, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
		context.Background(), repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 1, []FileRef{first, changedBlob}, false, 2, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-path changed blob universe error = %v", err)
	}
	changedCategory := second
	changedCategory.Category = "test"
	if _, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
		context.Background(), repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 1, []FileRef{first, changedCategory}, false, 2, true,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-path changed category universe error = %v", err)
	}
}

func TestRepositoryReviewCampaignRecordRejectsUngroundedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(FileRef, *RecordRequest)
	}{
		{
			name:   "missing explicit inspection",
			mutate: func(_ FileRef, request *RecordRequest) { request.InspectedFiles = nil },
		},
		{
			name: "completion is not inspected",
			mutate: func(file FileRef, request *RecordRequest) {
				request.InspectedFiles = []FileRef{}
				request.CompletedFiles = []FileRef{file}
			},
		},
		{
			name: "finding is not inspected",
			mutate: func(_ FileRef, request *RecordRequest) {
				request.InspectedFiles = []FileRef{}
			},
		},
		{
			name: "file reference is not exact",
			mutate: func(file FileRef, request *RecordRequest) {
				file.Category = "test"
				request.InspectedFiles = []FileRef{file}
				request.CompletedFiles = []FileRef{}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newRepositoryAuditTestStore(t)
			repository := "owner/invalid-" + strings.ReplaceAll(test.name, " ", "-")
			campaignID, _ := beginRepositoryReviewCampaignForTest(t, store, repository, true)
			file := repositoryAuditTestFile("service.go", "3", 10)
			plan := planRepositoryReviewCampaignForTest(
				t, store, repository, campaignID, []FileRef{file}, false,
			)
			request := RecordRequest{
				Plan: plan, RunID: "invalid-campaign-record", CompletedAt: repositoryAuditTestNow,
				InspectedFiles: []FileRef{file}, CompletedFiles: []FileRef{},
				Observations: []Observation{{
					Model: "review-a", ScopeFiles: []FileRef{file},
					Findings: []FindingCandidate{repositoryReviewCampaignFinding(file, "Finding")},
				}},
			}
			request.ReviewEvidence = []RepositoryReviewEvidence{
				repositoryReviewCampaignSuccessfulEvidence(
					[]FileRef{file}, []FileRef{file}, request.Observations[0], false,
				),
				{AssignmentID: "assignment-required-failed", ScopeFiles: []FileRef{file}, Required: true},
			}
			test.mutate(file, &request)
			if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("Record error = %v, want ErrInvalidPlan", err)
			}
			state, _, err := store.Get(repository)
			if err != nil || len(state.Runs) != 0 || len(state.Findings) != 0 ||
				len(state.CurrentCampaign.Paths) != 0 {
				t.Fatalf("rejected record mutated state=%#v err=%v", state, err)
			}
		})
	}
}

func TestRepositoryReviewCampaignEvidenceRejectsIncompleteOrForgedAssignments(t *testing.T) {
	newRequest := func(t *testing.T, required int) (Store, RecordRequest, []FileRef) {
		t.Helper()
		store := newRepositoryAuditTestStore(t)
		repository := "owner/evidence-" + strings.ReplaceAll(t.Name(), "/", "-")
		campaignID, _ := beginRepositoryReviewCampaignForTest(t, store, repository, true)
		files := []FileRef{
			repositoryAuditTestFile("a.go", "1", 10),
			repositoryAuditTestFile("b.go", "2", 20),
		}
		plan := planRepositoryReviewCampaignForTest(
			t, store, repository, campaignID, files, false, required,
		)
		return store, RecordRequest{
			Plan: plan, RunID: "evidence-run", CompletedAt: repositoryAuditTestNow,
			InspectedFiles: []FileRef{}, CompletedFiles: []FileRef{},
			ReviewEvidence: []RepositoryReviewEvidence{},
		}, files
	}

	t.Run("top-level coverage without child evidence", func(t *testing.T) {
		store, request, files := newRequest(t, 1)
		request.InspectedFiles = files
		request.CompletedFiles = files
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("forged top-level coverage error = %v", err)
		}
	})

	t.Run("omitted failed required child", func(t *testing.T) {
		store, request, files := newRequest(t, 2)
		observation := Observation{Model: "review-a", ScopeFiles: files}
		request.Observations = []Observation{observation}
		request.InspectedFiles = files
		request.ReviewEvidence = []RepositoryReviewEvidence{
			repositoryReviewCampaignSuccessfulEvidence(files, files, observation, true),
		}
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("omitted required assignment error = %v", err)
		}
	})

	t.Run("duplicate assignment ID", func(t *testing.T) {
		store, request, files := newRequest(t, 2)
		request.ReviewEvidence = []RepositoryReviewEvidence{
			{AssignmentID: "duplicate", ScopeFiles: files, Required: true},
			{AssignmentID: "duplicate", ScopeFiles: files, Required: true},
		}
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("duplicate assignment error = %v", err)
		}
	})

	t.Run("noncanonical scope", func(t *testing.T) {
		store, request, files := newRequest(t, 1)
		request.ReviewEvidence = []RepositoryReviewEvidence{{
			AssignmentID: "reversed", ScopeFiles: []FileRef{files[1], files[0]}, Required: true,
		}}
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("noncanonical scope error = %v", err)
		}
	})

	t.Run("acknowledgement outside child scope", func(t *testing.T) {
		store, request, files := newRequest(t, 1)
		observation := Observation{Model: "review-a", ScopeFiles: []FileRef{files[0]}}
		request.ReviewEvidence = []RepositoryReviewEvidence{
			repositoryReviewCampaignSuccessfulEvidence(
				[]FileRef{files[0]}, []FileRef{files[1]}, observation, true,
			),
			{AssignmentID: "second", ScopeFiles: []FileRef{files[1]}, Required: true},
		}
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("forged acknowledgement error = %v", err)
		}
	})

	t.Run("cross-child finding acknowledgement laundering", func(t *testing.T) {
		store, request, files := newRequest(t, 1)
		firstObservation := Observation{
			Model: "review-a", ScopeFiles: files,
			Findings: []FindingCandidate{repositoryReviewCampaignFinding(files[0], "Laundered")},
		}
		secondObservation := Observation{Model: "review-b", ScopeFiles: files}
		first := repositoryReviewCampaignSuccessfulEvidence(
			files, []FileRef{files[1]}, firstObservation, true,
		)
		second := repositoryReviewCampaignSuccessfulEvidence(
			files, []FileRef{files[0]}, secondObservation, false,
		)
		second.AssignmentID = "assignment-optional"
		request.InspectedFiles = files
		request.ReviewEvidence = []RepositoryReviewEvidence{first, second}
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("cross-child finding laundering error = %v", err)
		}
	})
}

func TestRepositoryReviewCampaignEvidenceBounds(t *testing.T) {
	if _, _, _, err := deriveRepositoryReviewCampaignEvidence(
		make([]RepositoryReviewEvidence, maxReviewObservations+1),
		map[string]FileRef{},
		1,
		map[string]struct{}{},
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("oversized evidence count error = %v", err)
	}
	longFile := repositoryAuditTestFile(strings.Repeat("a", 4096), "b", 1)
	evidence := make([]RepositoryReviewEvidence, 4200)
	for index := range evidence {
		evidence[index] = RepositoryReviewEvidence{
			AssignmentID: fmt.Sprintf("assignment-%04d", index),
			ScopeFiles:   []FileRef{longFile}, Required: true,
		}
	}
	if _, _, _, err := deriveRepositoryReviewCampaignEvidence(
		evidence,
		map[string]FileRef{longFile.Path: longFile},
		1,
		map[string]struct{}{},
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("oversized evidence metadata error = %v", err)
	}

	store := newRepositoryAuditTestStore(t)
	campaignID, _ := beginRepositoryReviewCampaignForTest(t, store, "owner/assignment-bound", true)
	if _, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
		context.Background(),
		"owner/assignment-bound",
		repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory,
		repositoryReviewCampaignTestProfile,
		campaignID,
		maxRepositoryReviewRequiredAssignments+1,
		[]FileRef{repositoryAuditTestFile("source.go", "c", 1)},
		false,
		1,
		true,
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("oversized required assignment count error = %v", err)
	}
}

func TestRepositoryReviewCampaignSeedsInheritedNoopAndUnsupportedCoverage(t *testing.T) {
	store, state := repositoryReviewCoverageStore(t, "owner/campaign-inherited")
	completed := repositoryAuditTestFile("pkg/completed.go", "4", 20)
	unsupported := repositoryAuditTestFile("pkg/generated.bin", "5", 30)
	state.Files[completed.Path] = ReviewedFile{
		FileRef: completed, CommitSHA: repositoryReviewCampaignTestCommit,
		ProfileHash: repositoryReviewCampaignTestProfile, RunID: "legacy-complete",
		ReviewedAt: repositoryAuditTestNow,
	}
	state.Unsupported[unsupported.Path] = UnsupportedFile{
		FileRef: unsupported, CommitSHA: repositoryReviewCampaignTestCommit,
		ProfileHash: repositoryReviewCampaignTestProfile, Reason: "binary evidence",
		UpdatedAt: repositoryAuditTestNow,
	}
	unsupportedInput := unsupported
	unsupportedInput.Category = "generated"
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	campaignID, _ := beginRepositoryReviewCampaignForTest(t, store, state.Repository, true)
	plan := planRepositoryReviewCampaignForTest(
		t, store, state.Repository, campaignID, []FileRef{completed, unsupportedInput}, false,
	)
	if len(plan.PendingFiles) != 0 || len(plan.UnchangedFiles) != 1 || len(plan.UnsupportedFiles) != 1 {
		t.Fatalf("inherited plan = %#v", plan)
	}
	if plan.UnsupportedFiles[0].FileRef != unsupportedInput {
		t.Fatalf("persisted unsupported metadata was not rebound: %#v", plan.UnsupportedFiles[0])
	}
	boundState, _, loadErr := store.Get(state.Repository)
	if loadErr != nil || boundState.CurrentCampaign.ScopeDigest !=
		repositoryReviewCampaignTestScopeDigest(t, completed, unsupportedInput) {
		t.Fatalf("rebound unsupported scope digest=%q err=%v",
			boundState.CurrentCampaign.ScopeDigest, loadErr)
	}
	finalized, finalizeErr := store.FinalizeNoopPlan(plan, 7)
	if finalizeErr != nil {
		t.Fatal(finalizeErr)
	}
	metrics := CurrentCampaignMetrics(finalized, campaignID, nil, repositoryAuditTestNow)
	if metrics.InspectedFiles != 0 || metrics.CompletedFiles != 1 ||
		metrics.UnsupportedFiles != 1 || metrics.RemainingFiles != 0 ||
		finalized.LastExcludedFiles != 7 {
		t.Fatalf("inherited metrics=%#v state=%#v", metrics, finalized)
	}
	if finalized.CurrentCampaign.Paths[completed.Path].Inspected {
		t.Fatal("inherited checkpoint was incorrectly counted as model inspection")
	}

	forceStore, forceState := repositoryReviewCoverageStore(t, "owner/campaign-force")
	forceState.Files[completed.Path] = state.Files[completed.Path]
	if err := forceStore.save(&forceState); err != nil {
		t.Fatal(err)
	}
	forceCampaignID, _ := beginRepositoryReviewCampaignForTest(
		t, forceStore, forceState.Repository, true,
	)
	forced := planRepositoryReviewCampaignForTest(
		t, forceStore, forceState.Repository, forceCampaignID, []FileRef{completed}, true,
	)
	loaded, _, _ := forceStore.Get(forceState.Repository)
	forceMetrics := CurrentCampaignMetrics(loaded, forceCampaignID, nil, repositoryAuditTestNow)
	if len(forced.PendingFiles) != 1 || forceMetrics.CompletedFiles != 0 ||
		forceMetrics.RemainingFiles != 1 {
		t.Fatalf("forced plan=%#v metrics=%#v", forced, forceMetrics)
	}

	unsupportedStore := newRepositoryAuditTestStore(t)
	unsupportedRepository := "owner/campaign-run-unsupported"
	unsupportedCampaignID, _ := beginRepositoryReviewCampaignForTest(
		t, unsupportedStore, unsupportedRepository, true,
	)
	file := repositoryAuditTestFile("pkg/unavailable.go", "6", 40)
	unsupportedPlan := planRepositoryReviewCampaignForTest(
		t, unsupportedStore, unsupportedRepository, unsupportedCampaignID, []FileRef{file}, false,
	)
	result, err := unsupportedStore.Record(context.Background(), RecordRequest{
		Plan: unsupportedPlan, RunID: "campaign-unsupported", CompletedAt: repositoryAuditTestNow,
		InspectedFiles: []FileRef{}, CompletedFiles: []FileRef{},
		ReviewEvidence:   []RepositoryReviewEvidence{},
		UnsupportedFiles: []UnsupportedFile{{FileRef: file, Reason: "content unavailable"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unsupportedMetrics := CurrentCampaignMetrics(
		result.State, unsupportedCampaignID, nil, repositoryAuditTestNow,
	)
	if unsupportedMetrics.UnsupportedFiles != 1 || unsupportedMetrics.RemainingFiles != 0 ||
		result.Run.UnsupportedCount != 1 {
		t.Fatalf("unsupported result=%#v metrics=%#v", result.Run, unsupportedMetrics)
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
			{ID: "rfn_one", CampaignID: campaignID, RepositoryFindingID: "rrf_shared"},
			{ID: "rfn_two", CampaignID: campaignID, RepositoryFindingID: "rrf_shared"},
			{ID: "rfn_three", CampaignID: campaignID, RepositoryFindingID: "rrf_other"},
			{ID: "rfn_pending", CampaignID: campaignID},
			{ID: "rfn_unrelated", CampaignID: otherCampaignID, RepositoryFindingID: "rrf_unrelated"},
		},
	}
	for index := range state.Runs {
		state.Runs[index] = ReviewRun{ID: fmt.Sprintf("newest-%04d", index)}
	}
	metrics := CurrentCampaignMetrics(state, campaignID, []string{"missing-old-run"}, repositoryAuditTestNow)
	if metrics.InspectedFiles != 2 || metrics.CompletedFiles != 1 ||
		metrics.UnsupportedFiles != 1 || metrics.RemainingFiles != 1 ||
		metrics.FindingOccurrences != 4 || metrics.FindingAggregates != 2 ||
		metrics.PendingFindingMappings != 1 {
		t.Fatalf("campaign metrics = %#v", metrics)
	}
	state.Findings[2].RepositoryFindingID = "rrf_shared"
	merged := CurrentCampaignMetrics(state, campaignID, nil, repositoryAuditTestNow)
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
			ID: fmt.Sprintf("rfn_campaign_%04d", index), CampaignID: campaignID,
		})
		if index > 0 {
			state.Runs[index-1] = ReviewRun{
				ID: fmt.Sprintf("run_campaign_%04d", index), CampaignID: campaignID,
			}
		}
	}
	metrics := CurrentCampaignMetrics(
		state, campaignID, []string{"run_campaign_0000"}, repositoryAuditTestNow,
	)
	if metrics.FindingOccurrences != 1001 || metrics.PendingFindingMappings != 1001 ||
		metrics.InspectedFiles != 1 || metrics.RemainingFiles != 1 {
		t.Fatalf("truncated campaign metrics = %#v", metrics)
	}
}

func TestRepositoryReviewCampaignMigrationValidationAndSerializationBounds(t *testing.T) {
	campaignID := NewRepositoryReviewCampaignID()
	state := repositoryReviewCoverageState("owner/campaign-migration")
	state.CurrentCampaign = &RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash:   repositoryReviewCampaignTestProfile,
		ScopeDigest: repositoryReviewCampaignTestScopeDigest(
			t, repositoryAuditTestFile("a.go", "5", 1),
		),
		RequiredAssignments: 1, SelectedFiles: 1, Exact: true,
	}
	migrated, migrationErr := migrateRepositoryState(&state)
	if migrationErr != nil || !migrated || state.CurrentCampaign.Paths == nil || state.CurrentCampaign.Exact {
		t.Fatalf("nil coverage migration=%v state=%#v err=%v", migrated, state.CurrentCampaign, migrationErr)
	}
	if err := validateState(state); err != nil {
		t.Fatalf("migrated lower-bound state is invalid: %v", err)
	}
	if cloned := cloneRepositoryReviewCampaignCoverage(RepositoryReviewCampaignCoverage{}); cloned.Paths != nil {
		t.Fatalf("clone converted unknown nil coverage to an exact empty map: %#v", cloned)
	}

	valid := &RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash:   repositoryReviewCampaignTestProfile,
		ScopeDigest: repositoryReviewCampaignTestScopeDigest(
			t, repositoryAuditTestFile("a.go", "5", 1),
		),
		RequiredAssignments: 1, SelectedFiles: 1, Exact: true,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{"a.go": {Inspected: true}},
	}
	invalid := []*RepositoryReviewCampaignCoverage{
		{
			ID:        "invalid",
			CommitSHA: repositoryReviewCampaignTestCommit,
			Paths:     map[string]RepositoryReviewCampaignPathCoverage{},
		},
		{
			ID:            campaignID,
			CommitSHA:     repositoryReviewCampaignTestCommit,
			InventoryHash: "inventory",
			Paths:         map[string]RepositoryReviewCampaignPathCoverage{},
		},
		{ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit, Paths: nil},
		func() *RepositoryReviewCampaignCoverage {
			value := cloneRepositoryReviewCampaignCoverageForTest(valid)
			value.Paths = map[string]RepositoryReviewCampaignPathCoverage{"../escape.go": {Inspected: true}}
			return value
		}(),
		func() *RepositoryReviewCampaignCoverage {
			value := cloneRepositoryReviewCampaignCoverageForTest(valid)
			value.Paths["a.go"] = RepositoryReviewCampaignPathCoverage{}
			return value
		}(),
		func() *RepositoryReviewCampaignCoverage {
			value := cloneRepositoryReviewCampaignCoverageForTest(valid)
			value.Paths["a.go"] = RepositoryReviewCampaignPathCoverage{Inspected: true, Unsupported: true}
			return value
		}(),
		func() *RepositoryReviewCampaignCoverage {
			value := cloneRepositoryReviewCampaignCoverageForTest(valid)
			value.SelectedFiles = 0
			return value
		}(),
	}
	for index, coverage := range invalid {
		if err := validateRepositoryReviewCampaignCoverage(coverage); err == nil {
			t.Errorf("invalid campaign coverage %d was accepted: %#v", index, coverage)
		}
	}

	tooMany := cloneRepositoryReviewCampaignCoverageForTest(valid)
	tooMany.SelectedFiles = maxReviewFiles
	tooMany.Paths = make(map[string]RepositoryReviewCampaignPathCoverage, maxReviewFiles+1)
	for index := 0; index <= maxReviewFiles; index++ {
		tooMany.Paths[fmt.Sprintf("p/%06d", index)] = RepositoryReviewCampaignPathCoverage{Inspected: true}
	}
	if err := validateRepositoryReviewCampaignCoverage(tooMany); err == nil {
		t.Fatal("campaign path-count overflow was accepted")
	}

	tooLarge := cloneRepositoryReviewCampaignCoverageForTest(valid)
	tooLarge.SelectedFiles = 4097
	tooLarge.Paths = make(map[string]RepositoryReviewCampaignPathCoverage, tooLarge.SelectedFiles)
	for index := 0; index < tooLarge.SelectedFiles; index++ {
		prefix := fmt.Sprintf("%04x/", index)
		pathValue := prefix + strings.Repeat("a", 4096-len(prefix))
		tooLarge.Paths[pathValue] = RepositoryReviewCampaignPathCoverage{Inspected: true}
	}
	if err := validateRepositoryReviewCampaignCoverage(tooLarge); err == nil {
		t.Fatal("campaign path metadata overflow was accepted")
	}

	for _, mutate := range []func(*RepositoryState){
		func(value *RepositoryState) { value.Findings = []Finding{{ID: "rfn_bad", CampaignID: "bad"}} },
		func(value *RepositoryState) { value.Contexts = []FindingContext{{CampaignID: "bad"}} },
		func(value *RepositoryState) { value.Runs = []ReviewRun{{CampaignID: "bad"}} },
		func(value *RepositoryState) { value.Runs = []ReviewRun{{InspectedFiles: -1}} },
	} {
		candidate := repositoryReviewCoverageState("owner/campaign-tags")
		mutate(&candidate)
		if err := validateState(candidate); err == nil {
			t.Fatal("invalid campaign tag state was accepted")
		}
	}

	retainedCampaignID := NewRepositoryReviewCampaignID()
	retained := repositoryReviewCoverageState("owner/retained-campaign")
	retained.CurrentCampaign = cloneRepositoryReviewCampaignCoverageForTest(valid)
	retained.CampaignHistory = map[string]string{
		valid.ID: valid.CommitSHA, retainedCampaignID: repositoryReviewCampaignTestCommit,
	}
	retained.Runs = []ReviewRun{{
		ID: "old-run", CampaignID: retainedCampaignID,
		CommitSHA:   repositoryReviewCampaignTestCommit,
		ProfileHash: repositoryReviewCampaignTestProfile, ScopeDigest: valid.ScopeDigest,
		FindingIDs: []string{"rfn_old"},
	}}
	retained.Contexts = []FindingContext{{
		ID: "rctx_old", RunID: "old-run", CampaignID: retainedCampaignID,
		CommitSHA:   repositoryReviewCampaignTestCommit,
		ProfileHash: repositoryReviewCampaignTestProfile,
	}}
	retained.Findings = []Finding{{
		ID: "rfn_old", CampaignID: retainedCampaignID,
		CommitSHA: repositoryReviewCampaignTestCommit, ContextIDs: []string{"rctx_old"},
	}}
	if err := validateState(retained); err != nil {
		t.Fatalf("retained prior campaign tags were rejected: %v", err)
	}
	retained.Contexts[0].CampaignID = campaignID
	if err := validateState(retained); err == nil {
		t.Fatal("mismatched retained campaign topology was accepted")
	}

	legacyJSON, marshalErr := json.Marshal(struct {
		Plan    Plan            `json:"plan"`
		Run     ReviewRun       `json:"run"`
		Request RecordRequest   `json:"request"`
		State   RepositoryState `json:"state"`
	}{Plan: Plan{}, Run: ReviewRun{}, Request: RecordRequest{}, State: RepositoryState{}})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(legacyJSON), "campaign_id") ||
		strings.Contains(string(legacyJSON), "current_campaign") ||
		strings.Contains(string(legacyJSON), "inspected_files") {
		t.Fatalf("legacy zero-value serialization changed: %s", legacyJSON)
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

	legacy := repositoryReviewCoverageState("owner/history-legacy")
	if _, err := migrateRepositoryState(&legacy); err != nil {
		t.Fatal(err)
	}
	legacy.CampaignHistory = nil
	firstMigration, err := migrateRepositoryState(&legacy)
	if err != nil || firstMigration || legacy.CampaignHistory != nil {
		t.Fatalf("legacy history migration=%v history=%#v err=%v", firstMigration, legacy.CampaignHistory, err)
	}
	secondMigration, err := migrateRepositoryState(&legacy)
	if err != nil || secondMigration || legacy.CampaignHistory != nil {
		t.Fatalf("second legacy history migration=%v history=%#v err=%v", secondMigration, legacy.CampaignHistory, err)
	}
}

func TestRepositoryReviewCampaignRecoveryAggregateSizeBound(t *testing.T) {
	request := ReconcileCampaignRequest{
		Repository: strings.Repeat("r", maxRepositoryReviewCampaignRecoveryBytes+1),
	}
	if _, err := repositoryReviewCampaignRecoveryDigest(request); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("oversized recovery digest error = %v", err)
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
		Findings: []Finding{{
			ID: "finding", CampaignID: campaignID, ContextIDs: []string{"context"},
		}},
		Runs: []ReviewRun{{
			ID: "run", CampaignID: campaignID, FindingIDs: []string{"finding"},
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
		Repository: repository, CampaignID: campaignID,
		CommitSHA:             repositoryReviewCampaignTestCommit,
		ExpectedReviewVersion: current.ReviewVersion, Exact: exact,
	})
	if err != nil {
		t.Fatal(err)
	}
	return campaignID, state
}

func planRepositoryReviewCampaignForTest(
	t *testing.T,
	store Store,
	repository string,
	campaignID string,
	files []FileRef,
	force bool,
	requiredAssignments ...int,
) Plan {
	t.Helper()
	required := 1
	if len(requiredAssignments) > 0 {
		required = requiredAssignments[0]
	}
	maximumPending := max(1, len(files))
	plan, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
		context.Background(), repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, required, files, force, maximumPending, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func repositoryReviewCampaignFinding(file FileRef, title string) FindingCandidate {
	return FindingCandidate{
		Severity: "high", Title: title, Symbol: "Save", File: file.Path,
		Evidence: "The immutable trace confirms the failure.", Impact: "State is lost.",
		Validation: Validation{Status: "confirmed", Summary: "Confirmed against the assigned file."},
	}
}

func repositoryReviewCampaignSuccessfulEvidence(
	scope []FileRef,
	acknowledged []FileRef,
	observation Observation,
	required bool,
) RepositoryReviewEvidence {
	return RepositoryReviewEvidence{
		AssignmentID: "assignment-success",
		ScopeFiles:   append([]FileRef(nil), scope...), Required: required, Successful: true,
		AcknowledgedFiles: append([]FileRef(nil), acknowledged...), Observation: &observation,
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
