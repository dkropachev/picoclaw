package repoaudit

import (
	"context"
	"encoding/json"
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

func TestRepositoryIdentityRunFallbackSkipsUnmatchedLedgers(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("service.go", "e", 10)
	for _, seed := range []struct{ repository, run string }{
		{repository: "owner/unmatched", run: "run-other"},
		{repository: "owner/selected", run: "run-selected"},
	} {
		plan, err := store.Plan(t.Context(), seed.repository, "commit", "inventory", []FileRef{file}, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Record(t.Context(), RecordRequest{
			Plan: plan, RunID: seed.run,
			Observations: []Observation{{Model: "reviewer", ScopeFiles: []FileRef{file}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	resolved, found, err := store.ResolveRepositoryState("owner/missing", []string{"run-selected"})
	if err != nil || !found || resolved.Repository != "owner/selected" {
		t.Fatalf("resolved=%#v found=%v err=%v", resolved, found, err)
	}
}

func TestRepositoryStoreRemainingValidationBranches(t *testing.T) {
	if _, err := BindPlanBranch(
		Plan{}, "main", strings.Repeat("d", maxRepositoryReviewBranchBytes+1), true,
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("oversized default branch error=%v", err)
	}
	store := newRepositoryAuditTestStore(t)
	file := repositoryAuditTestFile("branch.go", "f", 20)
	plan, err := store.Plan(t.Context(), "owner/branch", "commit", "inventory", []FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindPlanBranch(plan, "main", "main", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(t.Context(), RecordRequest{
		Plan: bound, RunID: "branch-mismatch", TargetBranch: "feature",
		AdvertisedDefaultBranch: "main", TargetIsDefault: false,
	}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("record branch mismatch error=%v", err)
	}
	pendingState, pendingFinding := recordLifecycleFinding(
		t, store, strings.Repeat("8", 40), strings.Repeat("c", 40),
		"pending-issue-action", "main", "main", true, "pending issue action",
	)
	if _, _, err := store.PrepareIssue(IssueDraftRequest{
		Repository: pendingState.Repository, FindingIDs: []string{pendingFinding.ID},
		ExpectedVersion: pendingState.Version,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("pending issue action error=%v", err)
	}

	base := validGeneratedFindingCandidateForCoverage()
	incomplete := base
	incomplete.Message = ""
	if err := ValidateGeneratedFindingCandidate(incomplete); err == nil {
		t.Fatal("incomplete generated finding was accepted")
	}
	newlineHint := base
	newlineHint.MatchHints.SourceAnchors = []string{"owner\nfield"}
	if err := ValidateGeneratedFindingCandidate(newlineHint); err == nil {
		t.Fatal("newline match hint was accepted")
	}
	for _, estimate := range []FixEffortEstimate{
		{LOCMin: 1, LOCMax: 10, Class: "tiny", Rationale: "Local."},
		{LOCMin: 11, LOCMax: 40, Class: "small", Rationale: "Local."},
		{LOCMin: 41, LOCMax: 150, Class: "medium", Rationale: "Several units."},
		{LOCMin: 151, LOCMax: 500, Class: "large", Rationale: "Many units."},
	} {
		if err := validateFixEffortEstimate(estimate); err != nil {
			t.Fatalf("valid effort %#v: %v", estimate, err)
		}
	}
}

func TestRepositoryStoreSemanticCausalBranches(t *testing.T) {
	file := repositoryAuditTestFile("semantic.go", "a", 40)
	line := 10
	baseCandidate := validGeneratedFindingCandidateForCoverage()
	baseCandidate.File = file.Path
	baseCandidate.Line = &line
	baseCandidate.Title = "Waiter remains blocked after owner move"
	baseCandidate.Message = "The waiter remains attached to the old owner."
	baseCandidate.Evidence = "The false predicate path requeues through the old owner."
	existing := Finding{
		File: file, Line: &line, Symbol: baseCandidate.Symbol,
		Title: baseCandidate.Title, Message: baseCandidate.Message, Evidence: baseCandidate.Evidence,
		MatchHints: baseCandidate.MatchHints,
	}
	conflict := baseCandidate
	conflict.MatchHints.Trigger = "move after 17 retries"
	existing.MatchHints.Trigger = "move after 2 retries"
	if got := semanticFindingIndex([]Finding{existing}, file, conflict); got != -1 {
		t.Fatalf("causal conflict merged at %d", got)
	}
	existing.MatchHints = baseCandidate.MatchHints
	tweak := baseCandidate
	tweak.MatchHints.Operation = "discard waiter from unrelated maintenance queue"
	tweak.MatchHints.FailureMode = "waiter is discarded from unrelated owner state"
	tweak.MatchHints.Trigger = "waiter sees independent timer expiration"
	tweak.MatchHints.ViolatedInvariant = "waiter state remains isolated from maintenance"
	tweak.MatchHints.ObservableOutcome = "coroutine request fails independently"
	tweak.MatchHints.SourceAnchors = []string{"different_anchor"}
	if got := semanticFindingIndex([]Finding{existing}, file, tweak); got != -1 {
		t.Fatalf("low-causal-similarity finding merged at %d", got)
	}
	if got := semanticFindingIndex([]Finding{existing}, file, baseCandidate); got != 0 {
		t.Fatalf("same causal finding index=%d", got)
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

func TestRepositoryStoreEntryAndStateValidationErrors(t *testing.T) {
	t.Skip("legacy per-ledger JSON entry validation was replaced by SQLite constraints")
	store := newRepositoryAuditTestStore(t)
	state := RepositoryState{
		SchemaVersion: SchemaVersion, ID: RepositoryID("owner/invalid"), Repository: "owner/invalid",
		Files: map[string]ReviewedFile{}, Unsupported: map[string]UnsupportedFile{},
		ReviewAttempts: map[string]int{}, ReviewAttemptIdentities: map[string]string{},
		RepositoryFindings: []RepositoryFinding{{ID: "bad"}},
	}
	data, marshalErr := json.Marshal(state)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	path := store.path(state.Repository)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, getErr := store.Get(state.Repository); getErr == nil {
		t.Fatal("invalid lifecycle state loaded")
	}
	entries, readErr := os.ReadDir(filepath.Dir(path))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if entry.Name() == filepath.Base(path) {
			if _, err := repositoryReviewStateFromEntry(filepath.Dir(path), entry); err == nil {
				t.Fatal("invalid lifecycle state entry loaded")
			}
		}
	}

	blockedSummaryRoot := filepath.Join(t.TempDir(), "summary-root-file")
	if err := os.WriteFile(blockedSummaryRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(blockedSummaryRoot).ListSummaries(); err == nil {
		t.Fatal("blocked summary root unexpectedly listed")
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Record(canceled, RecordRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled record error=%v", err)
	}
}

func validGeneratedFindingCandidateForCoverage() FindingCandidate {
	return FindingCandidate{
		Severity: "high", Title: "Waiter remains blocked", Symbol: "awaiter.signal",
		File: "waiter.go", Message: "The waiter retains its old owner.",
		Evidence: "The false predicate path uses the old owner.", Impact: "Coroutine blocks.",
		Validation: Validation{Status: "confirmed", Summary: "Traced the path."},
		MatchHints: MatchHints{
			Component: "scheduler", Operation: "requeue waiter after owner move",
			FailureMode: "waiter retains old owner", Trigger: "false predicate after move",
			ViolatedInvariant: "waiter uses current owner", ObservableOutcome: "coroutine blocks",
			RelatedSymbols: []string{"awaiter.signal"}, SourceAnchors: []string{"owner"},
			DistinguishingFacts: []string{"requires owner move"},
		},
		FixEffort: FixEffort{
			Quick:   FixEffortEstimate{LOCMin: 5, LOCMax: 20, Class: "small", Rationale: "Local containment."},
			Quality: FixEffortEstimate{LOCMin: 30, LOCMax: 100, Class: "medium", Rationale: "Several units."},
		},
	}
}
