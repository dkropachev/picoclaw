package repoaudit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotMappingJobsFreezesOnlyPendingRequestedJobs(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	_, first := recordLifecycleFinding(
		t, store, strings.Repeat("1", 40), strings.Repeat("a", 40),
		"snapshot-first", "main", "main", true, "first snapshot finding",
	)
	state, second := recordLifecycleFinding(
		t, store, strings.Repeat("2", 40), strings.Repeat("b", 40),
		"snapshot-second", "main", "main", true, "second snapshot finding",
	)
	firstJob := lifecycleJobForFinding(t, state, first.ID)
	_, claimed, _, ok, err := store.ClaimMappingJob(
		state.Repository, firstJob.ID, RepositoryMappingModelSnapshot{},
	)
	if err != nil || !ok {
		t.Fatalf("claim first mapping job: claimed=%v err=%v", ok, err)
	}
	state, _, err = store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: claimed.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot := RepositoryMappingModelSnapshot{
		ProfileID: "rrpf_snapshot", ProfileVersion: 3,
		Prompt: "matcher-v1", Model: "reviewer", Account: "account-a",
	}
	version := state.Version
	state, err = store.SnapshotMappingJobs(
		state.Repository,
		[]string{" ", first.ID, "missing", second.ID, second.ID},
		snapshot,
	)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != version+1 {
		t.Fatalf("state version=%d, want %d", state.Version, version+1)
	}
	firstJob = lifecycleJobForFinding(t, state, first.ID)
	secondJob := lifecycleJobForFinding(t, state, second.ID)
	if firstJob.ModelSnapshot != (RepositoryMappingModelSnapshot{}) ||
		secondJob.ModelSnapshot != snapshot || secondJob.UpdatedAt.IsZero() {
		t.Fatalf("snapshotted jobs first=%#v second=%#v", firstJob, secondJob)
	}

	idempotent, err := store.SnapshotMappingJobs(state.Repository, []string{second.ID}, snapshot)
	if err != nil || idempotent.Version != state.Version {
		t.Fatalf("idempotent snapshot version=%d err=%v", idempotent.Version, err)
	}
	conflicting := snapshot
	conflicting.Model = "different-reviewer"
	if _, err := store.SnapshotMappingJobs(
		state.Repository, []string{second.ID}, conflicting,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting snapshot error=%v", err)
	}
}

func TestSnapshotMappingJobsRejectsInvalidInputsAndMissingLedger(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	valid := RepositoryMappingModelSnapshot{Model: "reviewer"}
	for _, test := range []struct {
		name     string
		repo     string
		ids      []string
		snapshot RepositoryMappingModelSnapshot
	}{
		{name: "empty snapshot", repo: "owner/repo", ids: []string{"rf_1"}},
		{name: "invalid profile version", repo: "owner/repo", ids: []string{"rf_1"}, snapshot: RepositoryMappingModelSnapshot{ProfileID: "rrpf_bad"}},
		{name: "empty IDs", repo: "owner/repo", ids: []string{"", "  "}, snapshot: valid},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.SnapshotMappingJobs(test.repo, test.ids, test.snapshot); err == nil {
				t.Fatal("SnapshotMappingJobs unexpectedly succeeded")
			}
		})
	}
	empty, err := store.SnapshotMappingJobs("owner/missing", []string{"rf_1"}, valid)
	if err != nil || len(empty.MappingJobs) != 0 {
		t.Fatalf("missing ledger snapshot=%#v err=%v", empty, err)
	}
	blockedRoot := filepath.Join(t.TempDir(), "store-file")
	if err := os.WriteFile(blockedRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(blockedRoot).SnapshotMappingJobs(
		"owner/blocked", []string{"rf_1"}, valid,
	); err == nil {
		t.Fatal("snapshot with blocked store root unexpectedly succeeded")
	}
	saveFailureStore := newRepositoryAuditTestStore(t)
	saveFailureState, saveFailureFinding := recordLifecycleFinding(
		t, saveFailureStore, strings.Repeat("4", 40), strings.Repeat("d", 40),
		"snapshot-save-failure", "main", "main", true, "snapshot save failure",
	)
	if err := os.RemoveAll(saveFailureStore.root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(saveFailureStore.root, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := saveFailureStore.SnapshotMappingJobs(
		saveFailureState.Repository, []string{saveFailureFinding.ID}, valid,
	); err == nil {
		t.Fatal("snapshot persistence failure unexpectedly succeeded")
	}
}

func TestRepositoryIdentityRejectsUnsafeSpellingsAndCleansLocalPaths(t *testing.T) {
	local := filepath.Join(t.TempDir(), "nested", "..", "repository")
	identities := RepositoryLedgerIdentities(local)
	if len(identities) != 1 || identities[0] != filepath.Clean(local) {
		t.Fatalf("local identities=%#v", identities)
	}
	if CanonicalRepositoryIdentity("  ") != "" || RepositoryLedgerIdentities("  ") != nil {
		t.Fatal("empty repository identity was accepted")
	}
	for _, input := range []string{
		local,
		"@github.com:owner/repo.git",
		"git@example.com:owner/repo.git",
		"https://example.com/owner/repo.git",
		"https://github.com/owner.git",
		"owner/repo/extra",
		"owner/repo!",
		strings.Repeat("a", 101) + "/repo",
	} {
		if got := GitHubRepositoryIdentity(input); got != "" {
			t.Fatalf("GitHubRepositoryIdentity(%q)=%q", input, got)
		}
	}
}

func TestRepositoryMatchingSameComponentFallbackWithOpaqueInvalidAggregate(t *testing.T) {
	finding := Finding{
		ID: "rf_component", File: repositoryAuditTestFile("left.go", "a", 10), Symbol: "left.signal",
		Title: "Waiter stalls", MatchHints: MatchHints{Component: "scheduler"},
	}
	aggregate := RepositoryFinding{
		CanonicalTitle: "Queue stalls", MatchHints: MatchHints{Component: "scheduler"},
		PathSymbolHistory: []RepositoryFindingPathSymbol{{Path: "right.go", Symbol: "right.signal"}},
	}
	result := MatchRepositoryFinding(finding, []RepositoryFinding{aggregate}, nil, nil)
	if result.Method != "distinct" && result.Method != "ai" {
		t.Fatalf("same-component fallback result=%#v", result)
	}
}

func TestRepositoryControlAndGuardRemainingReachableBranches(t *testing.T) {
	store := newAutomationTestStore(t)
	created := createAutomationForTest(t, store, "rra_coverage_branches", "Coverage branches")
	invalidBranch := created
	invalidBranch.ResolvedTargetBranch = "bad..branch"
	if err := validateAutomation(invalidBranch); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("invalid resolved branch error=%v", err)
	}
	contradictory := created
	contradictory.ResolvedTargetBranch = "main"
	contradictory.AdvertisedDefaultBranch = "main"
	contradictory.TargetIsDefault = false
	if err := validateAutomation(contradictory); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("contradictory branch error=%v", err)
	}
	if got := canonicalAutomationRepository(" Relative-Repository.git/ "); got != "relative-repository" {
		t.Fatalf("fallback canonical repository=%q", got)
	}
	unknown := (&RepositoryReviewGuardUnknownError{Fields: []string{"spend.total.usd", "account.limits"}}).Error()
	if !strings.Contains(unknown, "spend.total.usd") || !strings.Contains(unknown, "account.limits") {
		t.Fatalf("unknown guard error=%q", unknown)
	}
	_, _ = EvaluateRepositoryReviewGuardExpression("42", RepositoryReviewGuardEnvironment{})
}

func TestRepositoryIssuePublicationConflictFence(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("7", 40), strings.Repeat("7", 40),
		"publication-conflict", "main", "main", true, "publication conflict",
	)
	state, aggregate := completeRepositoryAuditTestMapping(t, store, state, occurrence.ID)
	state, issue, err := store.LinkExistingIssue(ExistingIssueLink{
		Repository: state.Repository, FindingID: occurrence.ID,
		ExpectedFindingVersion: state.Findings[findingIndexByID(state.Findings, occurrence.ID)].Version,
		ExternalID:             "17", ExternalURL: "https://github.com/owner/repo/issues/17",
		Title: "Existing", Confirmed: true,
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
	if _, _, _, err := store.ClaimIssueDraftPublication(
		state.Repository, issue.ID, issue.Version,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("issue conflict publication claim error=%v", err)
	}
}
