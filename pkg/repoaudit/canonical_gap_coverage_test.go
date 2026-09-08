package repoaudit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
)

func canonicalGapCampaignCoverage(t *testing.T) RepositoryReviewCampaignCoverage {
	t.Helper()
	file := repositoryAuditTestFile("gap.go", "a", 1)
	catalog, err := repositoryReviewAssignmentCatalogCountForTest("profile-gap", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	return RepositoryReviewCampaignCoverage{
		ID:                    NewRepositoryReviewCampaignID(),
		CommitSHA:             repositoryReviewCampaignTestCommit,
		InventoryHash:         repositoryReviewCampaignTestInventory,
		ProfileHash:           "profile-gap",
		ScopeDigest:           repositoryReviewCampaignTestScopeDigest(t, file),
		RequiredAssignments:   1,
		SelectedFiles:         1,
		AssignmentCatalog:     catalog,
		DeduplicationSnapshot: repositoryReviewDeduplicationSnapshotForTest(),
		Paths:                 map[string]RepositoryReviewCampaignPathCoverage{},
	}
}

func TestCanonicalCampaignGapCoverage(t *testing.T) {
	coverage := canonicalGapCampaignCoverage(t)
	file := repositoryAuditTestFile("gap.go", "a", 1)

	if got := CurrentCampaignFindings(RepositoryState{}, " "); len(got) != 0 {
		t.Fatalf("empty campaign findings = %#v", got)
	}
	state := RepositoryState{
		Findings: []Finding{{CampaignID: coverage.ID}, {CampaignID: NewRepositoryReviewCampaignID()}},
		RawFindings: []RawReviewFinding{
			{ID: "rrw_first", CampaignID: coverage.ID},
			{ID: "rrw_other", CampaignID: NewRepositoryReviewCampaignID()},
		},
	}
	if got := CurrentCampaignRawFindings(state, coverage.ID); len(got) != 1 || got[0].ID != "rrw_first" {
		t.Fatalf("campaign raw findings = %#v", got)
	}
	if got := CurrentCampaignRawFindings(state, ""); len(got) != 0 {
		t.Fatalf("empty campaign raw findings = %#v", got)
	}
	if !DeduplicatedFindingBelongsToCampaign(state, Finding{CampaignID: coverage.ID}, " "+coverage.ID+" ") {
		t.Fatal("canonical campaign membership rejected")
	}

	digest := coverage.ScopeDigest
	if changed, err := bindRepositoryReviewCampaignScope(
		nil, coverage.ID, coverage.CommitSHA, coverage.InventoryHash, coverage.ProfileHash,
		digest, 1, 1,
	); changed || !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("nil campaign binding = %v, %v", changed, err)
	}
	wrong := RepositoryState{CurrentCampaign: &RepositoryReviewCampaignCoverage{
		ID: NewRepositoryReviewCampaignID(), CommitSHA: coverage.CommitSHA,
	}}
	if _, err := bindRepositoryReviewCampaignScope(
		&wrong, coverage.ID, coverage.CommitSHA, coverage.InventoryHash, coverage.ProfileHash,
		digest, 1, 1,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong campaign binding error = %v", err)
	}
	partial := RepositoryState{CurrentCampaign: &RepositoryReviewCampaignCoverage{
		ID: coverage.ID, CommitSHA: coverage.CommitSHA, ScopeDigest: digest,
	}}
	if _, err := bindRepositoryReviewCampaignScope(
		&partial, coverage.ID, coverage.CommitSHA, coverage.InventoryHash, coverage.ProfileHash,
		digest, 1, 1,
	); err == nil {
		t.Fatal("partial campaign binding accepted")
	}
	unbound := RepositoryState{CurrentCampaign: &RepositoryReviewCampaignCoverage{
		ID: coverage.ID, CommitSHA: coverage.CommitSHA,
	}}
	if changed, err := bindRepositoryReviewCampaignScope(
		&unbound, coverage.ID, coverage.CommitSHA, coverage.InventoryHash, coverage.ProfileHash,
		digest, 1, 1,
	); err != nil || !changed || unbound.CurrentCampaign.Paths == nil {
		t.Fatalf("unbound campaign binding = %#v, %v", unbound.CurrentCampaign, err)
	}
	if changed, err := bindRepositoryReviewCampaignScope(
		&unbound, coverage.ID, coverage.CommitSHA, coverage.InventoryHash, coverage.ProfileHash,
		digest, 1, 1,
	); err != nil || changed {
		t.Fatalf("replayed campaign binding = %v, %v", changed, err)
	}
	if _, err := bindRepositoryReviewCampaignScope(
		&unbound, coverage.ID, coverage.CommitSHA, "changed", coverage.ProfileHash,
		digest, 1, 1,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("drifted campaign binding error = %v", err)
	}

	nilPaths := coverage
	nilPaths.Paths = nil
	if changed, err := mergeRepositoryReviewCampaignPath(
		&nilPaths, file.Path, RepositoryReviewCampaignPathCoverage{Completed: true},
	); err != nil || !changed || nilPaths.Paths == nil {
		t.Fatalf("nil-map path merge = %#v, %v", nilPaths.Paths, err)
	}
	unsupported := coverage
	unsupported.Paths = map[string]RepositoryReviewCampaignPathCoverage{}
	if changed, err := mergeRepositoryReviewCampaignPath(
		&unsupported, file.Path, RepositoryReviewCampaignPathCoverage{Unsupported: true},
	); err != nil || !changed {
		t.Fatalf("unsupported path merge = %v, %v", changed, err)
	}
	if changed, err := mergeRepositoryReviewCampaignPath(
		&unsupported, file.Path, RepositoryReviewCampaignPathCoverage{Unsupported: true},
	); err != nil || changed {
		t.Fatalf("unsupported path replay = %v, %v", changed, err)
	}
	unsupported.Paths[file.Path] = RepositoryReviewCampaignPathCoverage{AssignmentBits: "AQ"}
	if _, err := mergeRepositoryReviewCampaignPath(
		&unsupported, file.Path, RepositoryReviewCampaignPathCoverage{Unsupported: true},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("unsupported assignment rewrite error = %v", err)
	}
	if _, err := mergeRepositoryReviewCampaignPath(
		nil, file.Path, RepositoryReviewCampaignPathCoverage{Inspected: true},
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("nil path merge error = %v", err)
	}

	if validateRepositoryReviewCampaignCoverage(nil) != nil {
		t.Fatal("nil campaign coverage rejected")
	}
	badCatalog := coverage
	badCatalog.AssignmentCatalog = append([]RepositoryReviewAssignment(nil), coverage.AssignmentCatalog...)
	badCatalog.AssignmentCatalog[0].ProfileHash = "wrong"
	if validateRepositoryReviewCampaignCoverage(&badCatalog) == nil {
		t.Fatal("mismatched assignment catalog accepted")
	}
	badPath := coverage
	badPath.Paths = map[string]RepositoryReviewCampaignPathCoverage{file.Path: {}}
	if validateRepositoryReviewCampaignCoverage(&badPath) == nil {
		t.Fatal("empty path projection accepted")
	}
	tooMany := coverage
	tooMany.SelectedFiles = 0
	tooMany.Paths = map[string]RepositoryReviewCampaignPathCoverage{file.Path: {Unsupported: true}}
	if validateRepositoryReviewCampaignCoverage(&tooMany) == nil {
		t.Fatal("out-of-scope terminal path accepted")
	}
	if clone := cloneRepositoryReviewCampaignCoverage(RepositoryReviewCampaignCoverage{}); clone.Paths != nil {
		t.Fatalf("nil campaign path clone = %#v", clone.Paths)
	}
	cloned := cloneRepositoryReviewCampaignCoverage(coverage)
	cloned.Paths[file.Path] = RepositoryReviewCampaignPathCoverage{Unsupported: true}
	if _, changed := coverage.Paths[file.Path]; changed {
		t.Fatal("campaign path clone aliased source")
	}

	loadFailure := newRepositoryAuditTestStore(t)
	loadFailure.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, errors.New("load failed")
	}
	if _, err := loadFailure.BeginCampaign(context.Background(), BeginCampaignRequest{
		Repository: "owner/load-failure", CampaignID: NewRepositoryReviewCampaignID(),
		CommitSHA:             repositoryReviewCampaignTestCommit,
		DeduplicationSnapshot: repositoryReviewDeduplicationSnapshotForTest(),
	}); err == nil {
		t.Fatal("campaign load failure ignored")
	}
}

func TestCanonicalCampaignRecordBindingGapCoverage(t *testing.T) {
	campaignID := NewRepositoryReviewCampaignID()
	otherCampaignID := NewRepositoryReviewCampaignID()
	valid := RepositoryState{
		Runs:        []ReviewRun{{ID: "run", CampaignID: campaignID, FindingIDs: []string{"rrw_one"}}},
		Contexts:    []FindingContext{{ID: "context", RunID: "run", CampaignID: campaignID}},
		Findings:    []Finding{{ID: "rdf_one", CampaignID: campaignID, ContextIDs: []string{"context"}}},
		RawFindings: []RawReviewFinding{{ID: "rrw_one", CampaignID: campaignID}},
	}
	if err := validateRepositoryReviewCampaignRecordBindings(valid); err != nil {
		t.Fatalf("valid canonical bindings rejected: %v", err)
	}
	cases := []RepositoryState{
		{Runs: []ReviewRun{{}}},
		{Runs: []ReviewRun{{ID: "same", CampaignID: campaignID}, {ID: "same", CampaignID: campaignID}}},
		{Contexts: []FindingContext{{ID: "context"}}},
		{
			Runs:     []ReviewRun{{ID: "run", CampaignID: campaignID}},
			Contexts: []FindingContext{{ID: "context", RunID: "run", CampaignID: otherCampaignID}},
		},
		{Contexts: []FindingContext{
			{CampaignID: campaignID},
			{ID: "same", CampaignID: campaignID},
			{ID: "same", CampaignID: campaignID},
		}},
		{Findings: []Finding{{ID: "rdf", CampaignID: campaignID}}},
		{
			Contexts: []FindingContext{{ID: "context", CampaignID: campaignID}},
			Findings: []Finding{{ID: "rdf", CampaignID: otherCampaignID, ContextIDs: []string{"context"}}},
		},
		{
			Contexts: []FindingContext{{ID: "context", CampaignID: campaignID}},
			Findings: []Finding{
				{ID: "same", CampaignID: campaignID, ContextIDs: []string{"context"}},
				{ID: "same", CampaignID: campaignID, ContextIDs: []string{"context"}},
			},
		},
		{
			Contexts:    []FindingContext{{ID: "context", CampaignID: campaignID}},
			Findings:    []Finding{{ID: "same", CampaignID: campaignID, ContextIDs: []string{"context"}}},
			RawFindings: []RawReviewFinding{{ID: "", CampaignID: campaignID}, {ID: "same", CampaignID: campaignID}},
		},
		{RawFindings: []RawReviewFinding{{ID: "same", CampaignID: campaignID}, {ID: "same", CampaignID: campaignID}}},
		{
			Runs:        []ReviewRun{{ID: "run", CampaignID: campaignID, FindingIDs: []string{"missing"}}},
			RawFindings: []RawReviewFinding{{ID: "rrw", CampaignID: campaignID}},
		},
		{
			Runs:        []ReviewRun{{ID: "run", CampaignID: campaignID, FindingIDs: []string{"rrw"}}},
			RawFindings: []RawReviewFinding{{ID: "rrw", CampaignID: otherCampaignID}},
		},
	}
	for index, state := range cases {
		if err := validateRepositoryReviewCampaignRecordBindings(state); err == nil {
			t.Errorf("invalid binding case %d accepted", index)
		}
	}
}

func TestCanonicalLifecyclePureGapCoverage(t *testing.T) {
	if err := validateRepositoryStateVersion(nil); err == nil {
		t.Fatal("nil repository state accepted")
	}
	initialized := RepositoryState{SchemaVersion: SchemaVersion}
	if err := validateRepositoryStateVersion(&initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.Files == nil || initialized.Unsupported == nil || initialized.RawFindings == nil ||
		initialized.DeduplicationJobs == nil || initialized.FileAttributions == nil ||
		initialized.RepositoryFindings == nil || initialized.ValidationJobs == nil {
		t.Fatalf("repository state maps were not initialized: %#v", initialized)
	}
	if ensureMappingJobsForFindings(nil, []string{"rdf"}, time.Now()) != 0 {
		t.Fatal("nil state created mapping jobs")
	}
	now := time.Now().UTC()
	state := RepositoryState{
		Findings: []Finding{
			{ID: "pending"},
			{ID: "mapped", RepositoryFindingID: "rrf_mapped"},
		},
		MappingJobs: []RepositoryMappingJob{{ReviewFindingID: "existing"}},
	}
	if got := ensureMappingJobsForFindings(
		&state, []string{"missing", "mapped", "pending", "pending"}, now,
	); got != 1 {
		t.Fatalf("created mapping jobs = %d", got)
	}

	hints := MatchHints{
		Component: "scheduler", Operation: "resume waiter", FailureMode: "stale owner",
		Trigger: "failed wake", ViolatedInvariant: "waiter uses current owner",
		ObservableOutcome: "waiter blocks", RelatedSymbols: []string{"Waiter.Resume"},
		SourceAnchors: []string{"owner"}, DistinguishingFacts: []string{"predicate false"},
	}
	effort := FixEffort{
		Quick:   FixEffortEstimate{LOCMin: 1, LOCMax: 10, Class: "tiny", Rationale: "Local."},
		Quality: FixEffortEstimate{LOCMin: 41, LOCMax: 80, Class: "medium", Rationale: "Invariant."},
	}
	target := RepositoryFinding{CanonicalSeverity: "low"}
	associateOccurrenceWithRepositoryFinding(nil, Finding{}, now)
	associateOccurrenceWithRepositoryFinding(
		&target,
		Finding{ID: "rdf", CommitSHA: "commit", Severity: "high", MatchHints: hints, FixEffort: effort, CreatedAt: now},
		now,
	)
	associateOccurrenceWithRepositoryFinding(&target, Finding{ID: "rdf"}, now)
	if target.MatchHints.Component == "" || target.FixEffort.Quick.Class == "" || target.CanonicalSeverity != "high" {
		t.Fatalf("associated occurrence = %#v", target)
	}

	merged := mergeRepositoryFindingRecords(&RepositoryFinding{
		ID: "target", CanonicalSeverity: "low", Lifecycle: RepositoryFindingOpen,
	}, RepositoryFinding{
		ID: "source", CanonicalSeverity: "high", Lifecycle: RepositoryFindingRegressed,
		MatchHints: hints, FixEffort: effort,
		ReviewFindingIDs: []string{"rdf"}, FoundCommits: []string{"commit"},
		PathSymbolHistory:  []RepositoryFindingPathSymbol{{ReviewFindingID: "rdf", ObservedAt: now}},
		ResolutionHistory:  []RepositoryFindingResolution{{Outcome: RepositoryValidationNotFixed, ValidatedAt: now}},
		PossibleDuplicates: []RepositoryFindingPossibleDuplicate{{CandidateID: "other", Relation: "related"}},
	}, now)
	if merged.Lifecycle != RepositoryFindingRegressed || !pathSymbolHistoryContains(merged.PathSymbolHistory, "rdf") {
		t.Fatalf("merged repository finding = %#v", merged)
	}
	if pathSymbolHistoryContains(nil, "missing") {
		t.Fatal("missing path history reported present")
	}
	if validationJobSequence([]RepositoryValidationJob{
		{RepositoryFindingID: "one"}, {RepositoryFindingID: "two"}, {RepositoryFindingID: "one"},
	}, "one") != 2 {
		t.Fatal("validation sequence mismatch")
	}
	resolutions := make([]RepositoryFindingResolution, maxRepositoryResolutionHistory)
	if got := appendBoundedResolution(
		resolutions, RepositoryFindingResolution{Summary: "new"},
	); len(got) != maxRepositoryResolutionHistory || got[len(got)-1].Summary != "new" {
		t.Fatalf("bounded resolutions = %#v", got)
	}
	if got := mergeRepositoryResolutionHistories(nil, nil); got != nil {
		t.Fatalf("empty resolution merge = %#v", got)
	}
	left := make([]RepositoryFindingResolution, maxRepositoryResolutionHistory)
	for index := range left {
		left[index].ValidatedAt = now.Add(time.Duration(index) * time.Minute)
	}
	if got := mergeRepositoryResolutionHistories(
		left, []RepositoryFindingResolution{{ValidatedAt: now.Add(2 * time.Hour)}},
	); len(got) != maxRepositoryResolutionHistory {
		t.Fatalf("trimmed resolution history count = %d", len(got))
	}
	if repositoryFindingAllowsIssueActions(RepositoryState{}, Finding{}) ||
		repositoryFindingAllowsIssueActions(
			RepositoryState{}, Finding{RepositoryFindingID: "missing"},
		) {
		t.Fatal("unmapped finding allowed issue actions")
	}
	conflicted := RepositoryState{RepositoryFindings: []RepositoryFinding{{
		ID: "rrf", Issue: RepositoryFindingIssueAssociation{Conflict: true},
	}}}
	if repositoryFindingHasIssueConflict(conflicted, Finding{}) ||
		!repositoryFindingHasIssueConflict(conflicted, Finding{RepositoryFindingID: "rrf"}) {
		t.Fatal("issue conflict projection mismatch")
	}
	if stringSlicesEqual([]string{"one"}, []string{"two"}) || stringSlicesEqual([]string{"one"}, nil) {
		t.Fatal("different slices compared equal")
	}
	if _, err := normalizeValidationCommits(make([]string, maxValidationCandidateCommits+1)); err == nil {
		t.Fatal("oversized validation candidates accepted")
	}
	commit := strings.Repeat("a", 40)
	if _, err := normalizeValidationCommits([]string{"bad"}); err == nil {
		t.Fatal("invalid validation commit accepted")
	}
	if _, err := normalizeValidationCommits([]string{commit, commit}); err == nil {
		t.Fatal("duplicate validation commit accepted")
	}
	if err := validateRepositoryLifecycleState(RepositoryState{
		ValidationJobs: make([]RepositoryValidationJob, maxRepositoryValidationJobs+1),
	}); err == nil {
		t.Fatal("oversized lifecycle state accepted")
	}
}

func TestCanonicalStorePureGapCoverage(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	catalog, err := repositoryReviewAssignmentCatalogCountForTest("profile-gap", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PlanAssignmentsForCampaign(
		context.Background(), "owner/repo", repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, "profile-gap", "bad", catalog, nil, false, 1, true,
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid campaign plan error = %v", err)
	}
	badCatalog := append([]RepositoryReviewAssignment(nil), catalog...)
	badCatalog[0].ID = "bad"
	if _, err := store.PlanAssignmentsForCampaign(
		context.Background(), "owner/repo", repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, "profile-gap", NewRepositoryReviewCampaignID(),
		badCatalog, nil, false, 1, true,
	); err == nil {
		t.Fatal("invalid assignment catalog accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.planWithProfileLimitAuthoritative(
		canceled, "owner/repo", repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, "profile-gap", NewRepositoryReviewCampaignID(),
		catalog, nil, false, 1, true,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled plan error = %v", err)
	}
	file := repositoryAuditTestFile("gap.go", "a", 1)
	if _, err := normalizeFiles([]FileRef{file, file}); err == nil {
		t.Fatal("duplicate inventory path accepted")
	}
	if validBlobSHA(strings.Repeat("g", 40)) {
		t.Fatal("non-hex blob accepted")
	}
	if err := prepareRepositoryStateForPersistence(nil); err == nil {
		t.Fatal("nil state prepared")
	}
	if (Store{}).clock().IsZero() {
		t.Fatal("default store clock returned zero")
	}
	if _, err := repositoryReviewCampaignScopeDigestForPlan(Plan{
		PendingFiles: []FileRef{{Path: "bad", BlobSHA: "bad"}},
	}); err == nil {
		t.Fatal("invalid plan scope hashed")
	}
	nonCanonical := file
	nonCanonical.Path = " gap.go "
	if _, err := canonicalRepositoryReviewCampaignFiles([]FileRef{nonCanonical}); err == nil {
		t.Fatal("non-canonical campaign file accepted")
	}

	for index, hints := range []MatchHints{
		{Component: ""},
		{Component: "x", RelatedSymbols: make([]string, maxMatchHintItems+1)},
		{Component: "x", RelatedSymbols: []string{"bad\nvalue"}},
		{Component: "x", RelatedSymbols: []string{"same", " SAME "}},
	} {
		if err := validateMatchHints(hints); err == nil {
			t.Errorf("invalid match hints %d accepted", index)
		}
	}
	if err := validateFixEffort(FixEffort{
		Quick: FixEffortEstimate{}, Quality: FixEffortEstimate{},
	}); err == nil {
		t.Fatal("invalid quick effort accepted")
	}
	if err := validateFixEffort(FixEffort{
		Quick:   FixEffortEstimate{LOCMin: 10, LOCMax: 10, Class: "tiny", Rationale: "local"},
		Quality: FixEffortEstimate{LOCMin: 1, LOCMax: 10, Class: "tiny", Rationale: "local"},
	}); err == nil {
		t.Fatal("smaller quality effort accepted")
	}
	for _, estimate := range []FixEffortEstimate{
		{LOCMin: 151, LOCMax: 500, Class: "large", Rationale: "many units"},
		{LOCMin: 501, LOCMax: 600, Class: "refactor", Rationale: "cross-subsystem architectural migration"},
	} {
		if err := validateFixEffortEstimate(estimate); err != nil {
			t.Fatalf("valid effort %#v rejected: %v", estimate, err)
		}
	}

	base := repositoryReviewCoverageState("owner/state-gap")
	base.CampaignHistory = map[string]string{}
	invalidCampaign := base
	invalidCampaign.CurrentCampaign = &RepositoryReviewCampaignCoverage{}
	if validateState(invalidCampaign) == nil {
		t.Fatal("invalid campaign state accepted")
	}
	invalidHistory := base
	invalidHistory.CampaignHistory = map[string]string{"bad": repositoryReviewCampaignTestCommit}
	if validateState(invalidHistory) == nil {
		t.Fatal("invalid campaign history accepted")
	}
	if _, _, err := selectedFindings(nil, nil); err == nil {
		t.Fatal("empty finding selection accepted")
	}
	all := []Finding{{ID: "one"}}
	if _, _, err := selectedFindings(all, []string{"one", "one"}); err == nil {
		t.Fatal("duplicate finding selection accepted")
	}
	if _, _, err := selectedFindings(all, []string{"missing"}); err == nil {
		t.Fatal("missing finding selection accepted")
	}
	labels := make([]string, 0, 25)
	labels = append(labels, "", "same", "same")
	for index := range 22 {
		labels = append(labels, fmt.Sprintf("label-%d", index))
	}
	if got := normalizeLabels(labels); len(got) != 20 {
		t.Fatalf("normalized labels count = %d", len(got))
	}

	if GitHubRepositoryIdentity("owner/repo") != "owner/repo" {
		t.Fatal("shorthand GitHub identity not canonical")
	}
	identifiers := map[string]struct{}{}
	(&repositoryReviewGuardLiteralNode{}).collectIdentifiers(identifiers)
	if len(identifiers) != 0 {
		t.Fatalf("literal identifiers = %#v", identifiers)
	}
	if !ValidRepositoryReviewAutomationID("rra_gap") || ValidRepositoryReviewAutomationID("bad") {
		t.Fatal("automation ID validation mismatch")
	}
}

func canonicalGapIssueGenerationRequest(repository string) IssueGenerationRequest {
	return IssueGenerationRequest{
		Repository: repository, FindingID: "rdf_gap", GenerationID: "generation-gap",
		ResolvedInstructions: "find root cause", InstructionsMode: IssueDraftInstructionsDefault,
		GeneratorModel: "writer", GeneratorAccount: "account",
	}
}

func TestCanonicalMutationFailureGapCoverage(t *testing.T) {
	loadFailure := newRepositoryAuditTestStore(t)
	loadFailure.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, errors.New("load failed")
	}
	repository := "owner/load-gap"
	generation := canonicalGapIssueGenerationRequest(repository)
	if _, _, _, err := loadFailure.ReserveIssueGeneration(generation); err == nil {
		t.Fatal("reserve generation load failure ignored")
	}
	if _, _, _, err := loadFailure.BeginIssueRegeneration(repository, "draft", generation); err == nil {
		t.Fatal("regeneration load failure ignored")
	}
	if _, _, err := loadFailure.CompleteIssueGeneration(
		repository, "draft", generation.GenerationID, "title", "body", nil, "",
	); err == nil {
		t.Fatal("generation completion load failure ignored")
	}
	if _, err := loadFailure.DeleteIssueDraft(repository, "draft", 1); err == nil {
		t.Fatal("draft deletion load failure ignored")
	}
	if _, _, err := loadFailure.LinkExistingIssue(ExistingIssueLink{
		Repository: repository, FindingID: "rdf_gap", ExpectedFindingVersion: 1,
		ExternalID: "1", ExternalURL: "https://github.com/owner/repo/issues/1",
		Title: "title", Confirmed: true,
	}); err == nil {
		t.Fatal("issue link load failure ignored")
	}
	if _, err := loadFailure.UnlinkExistingIssue(repository, "rdf_gap", 1, true); err == nil {
		t.Fatal("issue unlink load failure ignored")
	}
	if _, _, err := loadFailure.UpdateIssueDraft(repository, "draft", "title", "body", nil, 1); err == nil {
		t.Fatal("draft update load failure ignored")
	}
	if _, _, err := loadFailure.SetIssueDraftPublication(
		repository, "draft", 1, IssueDraftUnknown, "", "",
	); err == nil {
		t.Fatal("publication update load failure ignored")
	}
	if _, _, _, err := loadFailure.ClaimIssueDraftPublication(repository, "draft", 1); err == nil {
		t.Fatal("publication claim load failure ignored")
	}

	lockFailure := newRepositoryAuditTestStore(t)
	if err := os.MkdirAll(
		repositoryReviewTestLockPath(t, lockFailure.root, "store.lock"), 0o700,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := lockFailure.ReserveIssueGeneration(
		canonicalGapIssueGenerationRequest("owner/lock-gap"),
	); err == nil {
		t.Fatal("reserve generation lock failure ignored")
	}
	if _, _, _, err := lockFailure.BeginIssueRegeneration(
		"owner/lock-gap", "draft", canonicalGapIssueGenerationRequest("owner/lock-gap"),
	); err == nil {
		t.Fatal("regeneration lock failure ignored")
	}
	if _, _, err := lockFailure.CompleteIssueGeneration(
		"owner/lock-gap", "draft", "generation", "title", "body", nil, "",
	); err == nil {
		t.Fatal("generation completion lock failure ignored")
	}
	if _, err := lockFailure.DeleteIssueDraft("owner/lock-gap", "draft", 1); err == nil {
		t.Fatal("draft deletion lock failure ignored")
	}
	if _, _, err := lockFailure.LinkExistingIssue(ExistingIssueLink{
		Repository: "owner/lock-gap", FindingID: "rdf", ExpectedFindingVersion: 1,
		ExternalID: "1", ExternalURL: "https://github.com/owner/repo/issues/1",
		Title: "title", Confirmed: true,
	}); err == nil {
		t.Fatal("issue link lock failure ignored")
	}
	if _, err := lockFailure.UnlinkExistingIssue("owner/lock-gap", "rdf", 1, true); err == nil {
		t.Fatal("issue unlink lock failure ignored")
	}
	if _, _, err := lockFailure.UpdateIssueDraft(
		"owner/lock-gap", "draft", "title", "body", nil, 1,
	); err == nil {
		t.Fatal("draft update lock failure ignored")
	}
	if _, _, err := lockFailure.SetIssueDraftPublication(
		"owner/lock-gap", "draft", 1, IssueDraftUnknown, "", "",
	); err == nil {
		t.Fatal("publication update lock failure ignored")
	}
	if _, _, _, err := lockFailure.ClaimIssueDraftPublication("owner/lock-gap", "draft", 1); err == nil {
		t.Fatal("publication claim lock failure ignored")
	}
}

func TestCanonicalIssueStateGapCoverage(t *testing.T) {
	repository := "owner/issue-gap"
	now := time.Now().UTC()
	aggregate := RepositoryFinding{
		ID: "rrf_gap", MatchState: RepositoryMatchKnown, Lifecycle: RepositoryFindingOpen,
		Issue: RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone},
	}
	finding := Finding{
		ID: "rdf_gap", RepositoryFindingID: aggregate.ID,
		RepositoryMatchState: RepositoryMatchKnown, Status: FindingPosted, Version: 1,
	}
	closedStore := newRepositoryAuditTestStore(t)
	closedStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{
			Repository: repository, Findings: []Finding{finding},
			RepositoryFindings: []RepositoryFinding{aggregate},
		}, nil
	}
	if _, _, _, err := closedStore.ReserveIssueGeneration(
		canonicalGapIssueGenerationRequest(repository),
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("closed finding reservation error = %v", err)
	}

	validGenerated := IssueDraft{
		ID: "draft", Repository: repository, FindingIDs: []string{finding.ID},
		Origin: IssueDraftOriginAIGenerated, State: IssueDraftGenerating, Version: 1,
		GenerationID: "generation", ResolvedInstructions: "instructions",
		InstructionsMode: IssueDraftInstructionsDefault,
		GeneratorModel:   "writer", GeneratorAccount: "account",
		CreatedAt: now, UpdatedAt: now,
	}
	profileOnlyAttempt := validGenerated
	profileOnlyAttempt.AttemptGeneratorProfileID = "rrpf_gap"
	profileOnlyAttempt.AttemptGeneratorProfileVersion = 1
	if err := validateIssueAssociations(RepositoryState{
		Repository: repository, Findings: []Finding{finding}, IssueDrafts: []IssueDraft{profileOnlyAttempt},
	}); err == nil {
		t.Fatal("partial issue attempt profile accepted")
	}
	badLink := IssueDraft{
		ID: "linked", Repository: repository, FindingIDs: []string{finding.ID},
		Origin: IssueDraftOriginLinked, State: IssueDraftPosted, Version: 1,
		Title: "title", CreatedAt: now, UpdatedAt: now,
	}
	if err := validateIssueAssociations(RepositoryState{
		Repository: repository, Findings: []Finding{finding}, IssueDrafts: []IssueDraft{badLink},
	}); err == nil {
		t.Fatal("linked issue without provider identity accepted")
	}
	if !issuePublicationFindingStatusUnresolved(
		RepositoryState{MappingJobs: []RepositoryMappingJob{{
			ReviewFindingID: finding.ID, State: RepositoryMappingPending,
		}}},
		Finding{ID: finding.ID, Status: FindingOpen},
	) {
		t.Fatal("pending mapping projected as resolved")
	}
	if issuePublicationFindingStatusUnresolved(
		RepositoryState{}, Finding{ID: finding.ID, Status: FindingOpen},
	) {
		t.Fatal("open finding without mapping projected unresolved")
	}
	if !(IssuePublicationEligibility{}).AllowsPostedAcknowledgement() {
		t.Fatal("empty posted acknowledgement eligibility rejected")
	}
}

func TestCanonicalMappingWorkerPureGapCoverage(t *testing.T) {
	job := RepositoryMappingJob{
		ID: "job", Adjudication: RepositoryMappingAdjudication{Decision: "distinct", Confidence: 1},
		CandidateUniverse: "universe",
	}
	completion, method, err := (Store{}).repositoryMappingCompletionForJob(
		context.Background(), RepositoryState{}, job, Finding{}, RepositoryMappingProcessOptions{},
	)
	if err != nil || method != "ai" || completion.CreateMatchState != RepositoryMatchNew {
		t.Fatalf("saved adjudication completion = %#v, %q, %v", completion, method, err)
	}
	_, _, err = (Store{}).repositoryMappingCompletionForJob(
		context.Background(),
		RepositoryState{RepositoryFindings: []RepositoryFinding{{
			ID: "candidate", PathSymbolHistory: []RepositoryFindingPathSymbol{{Path: "same.go"}},
		}}},
		RepositoryMappingJob{ID: "job"}, Finding{File: FileRef{Path: "same.go"}},
		RepositoryMappingProcessOptions{},
	)
	if !errors.Is(err, errRepositoryMappingNeedsAI) {
		t.Fatalf("missing adjudicator error = %v", err)
	}

	confirmed := RepositoryFinding{
		ID: "rrf_confirmed", ValidationState: RepositoryValidationConfirmed,
		FixCommitSHA: strings.Repeat("a", 40),
	}
	possible := RepositoryMappingCompletion{
		DefaultBranchVerified: true,
		PossibleDuplicates: []RepositoryFindingPossibleDuplicate{{
			CandidateID: confirmed.ID, Relation: "uncertain",
		}},
	}
	result, err := repositoryMappingRegressionCompletion(
		context.Background(), RepositoryState{RepositoryFindings: []RepositoryFinding{confirmed}},
		Finding{}, possible,
		RepositoryMappingProcessOptions{RegressionVerified: func(
			context.Context, Finding, RepositoryFinding,
		) (bool, error) {
			return true, nil
		}},
	)
	if err != nil || !result.RegressionVerified || result.RegressionFindingID != confirmed.ID {
		t.Fatalf("possible duplicate regression = %#v, %v", result, err)
	}
	if _, err := repositoryMappingRegressionCompletion(
		context.Background(), RepositoryState{RepositoryFindings: []RepositoryFinding{confirmed}},
		Finding{}, RepositoryMappingCompletion{
			RepositoryFindingID: confirmed.ID, DefaultBranchVerified: true,
		}, RepositoryMappingProcessOptions{RegressionVerified: func(
			context.Context, Finding, RepositoryFinding,
		) (bool, error) {
			return false, errors.New("verification failed")
		}},
	); err == nil {
		t.Fatal("regression callback error ignored")
	}
	if result, err := repositoryMappingRegressionCompletion(
		context.Background(), RepositoryState{}, Finding{},
		RepositoryMappingCompletion{RepositoryFindingID: "missing", DefaultBranchVerified: true},
		RepositoryMappingProcessOptions{RegressionVerified: func(
			context.Context, Finding, RepositoryFinding,
		) (bool, error) {
			return true, nil
		}},
	); err != nil || result.RegressionVerified {
		t.Fatalf("missing regression candidate = %#v, %v", result, err)
	}

	request, mapping := repositoryOpaqueMappingRequest(
		Finding{RepositoryFindingID: "secret"},
		[]RepositoryFinding{{ID: "known"}},
		[]RepositoryMatchCandidate{{ID: "missing"}, {ID: "known"}},
	)
	if len(request.Candidates) != 1 || len(mapping) != 1 || request.Finding.RepositoryFindingID != "" {
		t.Fatalf("opaque mapping request = %#v, %#v", request, mapping)
	}
	for decision, expected := range map[string]RepositoryMatchState{
		"same": RepositoryMatchProvisional, "related": RepositoryMatchNew,
		"uncertain": RepositoryMatchProvisional, "distinct": RepositoryMatchNew,
	} {
		completion := repositoryCompletionFromAdjudication("job", RepositoryMappingAdjudication{
			Decision: decision, CandidateID: "candidate", Confidence: .5,
		}, nil, true)
		if completion.CreateMatchState != expected {
			t.Errorf("%s completion = %#v", decision, completion)
		}
	}

	loadFailure := newRepositoryAuditTestStore(t)
	loadFailure.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, errors.New("load failed")
	}
	if err := loadFailure.releaseMappingJob("owner/repo", "job", errors.New("cause")); err == nil {
		t.Fatal("mapping release load failure ignored")
	}
	running := newRepositoryAuditTestStore(t)
	running.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{
			Repository:  "owner/repo",
			MappingJobs: []RepositoryMappingJob{{ID: "job", State: RepositoryMappingRunning}},
		}, nil
	}
	_ = running.releaseMappingJob("owner/repo", "job", errRepositoryMappingNeedsAI)
}

func canonicalGapFindingCandidate(path string) FindingCandidate {
	line := 7
	return FindingCandidate{
		Severity: "high", Title: "Waiter stalls", File: path, Line: &line,
		Symbol: "Waiter.Resume", Message: "Waiter retains stale owner.",
		Evidence: "False predicate reads stale owner.", Impact: "Request stalls.",
		Validation: Validation{Status: "confirmed", Summary: "Traced control flow."},
		MatchHints: MatchHints{
			Component: "scheduler", Operation: "resume waiter", FailureMode: "stale owner",
			Trigger: "failed wake", ViolatedInvariant: "waiter uses current owner",
			ObservableOutcome: "request stalls", RelatedSymbols: []string{"Waiter.Resume"},
			SourceAnchors: []string{"owner"}, DistinguishingFacts: []string{"predicate false"},
		},
		FixEffort: FixEffort{
			Quick:   FixEffortEstimate{LOCMin: 1, LOCMax: 10, Class: "tiny", Rationale: "Local."},
			Quality: FixEffortEstimate{LOCMin: 20, LOCMax: 40, Class: "small", Rationale: "Related paths."},
		},
	}
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestCanonicalCheckpointHelperGapCoverage(t *testing.T) {
	file := repositoryAuditTestFile("gap.go", "b", 1)
	catalog, err := repositoryReviewAssignmentCatalogCountForTest("profile-gap", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		CampaignID: NewRepositoryReviewCampaignID(), Repository: "owner/repo",
		CommitSHA: repositoryReviewCampaignTestCommit, InventoryHash: "inventory",
		ProfileHash: "profile-gap", AssignmentCatalog: catalog,
		TargetIsDefault: true,
	}
	observation := Observation{
		Model: "provider/reviewer", ModelAlias: "reviewer", Account: "account",
		Reviewer: catalog[0].FocusID, ScopeFiles: []FileRef{file},
		RawDigest: "sha256:" + strings.Repeat("d", 64),
	}
	request := CheckpointRepositoryReviewAssignmentRequest{
		AutomationID: "rra_gap", RunID: "run", Plan: plan, AssignmentID: catalog[0].ID,
		AgentID: "main", ChildIndex: 1, Observation: observation,
		CompletedAt: repositoryAuditTestNow,
	}
	assignment := catalog[0]
	if _, err := reconcileRepositoryReviewCheckpointAttribution(
		nil, request, assignment, []FileRef{file},
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("nil checkpoint attribution error = %v", err)
	}
	base := repositoryReviewCheckpointAttribution(request, assignment, []FileRef{file}, request.CompletedAt)
	retained, err := NewRepositoryReviewFileAttribution(base)
	if err != nil {
		t.Fatal(err)
	}
	mismatchedID := base
	mismatchedID.ID = "rrfa_wrong"
	if _, err := NewRepositoryReviewFileAttribution(mismatchedID); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("mismatched attribution ID error = %v", err)
	}
	other := retained
	other.ID = repositoryReviewFileAttributionID(RepositoryReviewFileAttribution{
		AutomationID: retained.AutomationID, RunID: retained.RunID, ChildIndex: 2,
	})
	if _, err := reconcileRepositoryReviewCheckpointAttribution(
		&RepositoryState{FileAttributions: []RepositoryReviewFileAttribution{other}},
		request, assignment, []FileRef{file},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign retained attribution error = %v", err)
	}
	if _, err := reconcileRepositoryReviewCheckpointAttribution(
		&RepositoryState{FileAttributions: []RepositoryReviewFileAttribution{retained, retained}},
		request, assignment, []FileRef{file},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate retained attribution error = %v", err)
	}
	changedTime := request
	changedTime.CompletedAt = request.CompletedAt.Add(time.Minute)
	if _, err := reconcileRepositoryReviewCheckpointAttribution(
		&RepositoryState{FileAttributions: []RepositoryReviewFileAttribution{retained}},
		changedTime, assignment, []FileRef{file},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed completion time error = %v", err)
	}
	zeroTime := request
	zeroTime.CompletedAt = time.Time{}
	if _, err := reconcileRepositoryReviewCheckpointAttribution(
		&RepositoryState{}, zeroTime, assignment, []FileRef{file},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing attribution time error = %v", err)
	}

	ids := repositoryReviewCheckpointRawFindingIDs([]RawReviewFinding{
		{
			ID: "rrw_b", CampaignID: plan.CampaignID, RunID: request.RunID,
			AssignmentID: request.AssignmentID, InsertionOrdinal: 2,
		},
		{
			ID: "rrw_c", CampaignID: plan.CampaignID, RunID: request.RunID,
			AssignmentID: request.AssignmentID, InsertionOrdinal: 1,
		},
		{
			ID: "rrw_a", CampaignID: plan.CampaignID, RunID: request.RunID,
			AssignmentID: request.AssignmentID, InsertionOrdinal: 1,
		},
		{ID: "rrw_other", CampaignID: NewRepositoryReviewCampaignID()},
	}, plan.CampaignID, request.RunID, request.AssignmentID)
	if strings.Join(ids, ",") != "rrw_a,rrw_c,rrw_b" {
		t.Fatalf("sorted checkpoint finding IDs = %#v", ids)
	}

	state := RepositoryState{CurrentCampaign: &RepositoryReviewCampaignCoverage{
		DeduplicationSnapshot: repositoryReviewDeduplicationSnapshotForTest(),
	}}
	candidate := canonicalGapFindingCandidate(file.Path)
	badPlan := plan
	badPlan.CampaignID = ""
	if _, err := persistRepositoryReviewCheckpointObservation(
		&state, badPlan, request.RunID, request.AssignmentID,
		Observation{
			Model: observation.Model, ModelAlias: observation.ModelAlias, Account: observation.Account,
			Reviewer: observation.Reviewer, ScopeFiles: []FileRef{file}, RawDigest: observation.RawDigest,
			Findings: []FindingCandidate{candidate},
		}, []FileRef{file}, request.CompletedAt,
	); err == nil {
		t.Fatal("invalid admission campaign accepted")
	}
	if err := persistRawRepositoryReviewCheckpointFinding(
		&RepositoryState{}, "rrw", "bucket", plan, request.RunID, request.AssignmentID,
		"context", Observation{}, file, candidate, request.CompletedAt,
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid raw provenance error = %v", err)
	}
	validState := RepositoryState{CurrentCampaign: &RepositoryReviewCampaignCoverage{
		DeduplicationSnapshot: repositoryReviewDeduplicationSnapshotForTest(),
	}}
	rawID := "rrw_gap"
	bucket, err := DeduplicationAdmissionBucket(plan.CampaignID, file, candidate.Symbol)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistRawRepositoryReviewCheckpointFinding(
		&validState, rawID, bucket, plan, request.RunID, request.AssignmentID,
		"context", observation, file, candidate, request.CompletedAt,
	); err != nil {
		t.Fatal(err)
	}
	withoutJob := validState
	withoutJob.DeduplicationJobs = nil
	if err := persistRawRepositoryReviewCheckpointFinding(
		&withoutJob, rawID, bucket, plan, request.RunID, request.AssignmentID,
		"context", observation, file, candidate, request.CompletedAt,
	); err == nil {
		t.Fatal("raw finding without deduplication job accepted")
	}
	changed := candidate
	changed.Title = "Different title"
	if err := persistRawRepositoryReviewCheckpointFinding(
		&validState, rawID, bucket, plan, request.RunID, request.AssignmentID,
		"context", observation, file, changed, request.CompletedAt,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed raw finding error = %v", err)
	}
	if err := persistRawRepositoryReviewCheckpointFinding(
		&validState, rawID, bucket, plan, request.RunID, request.AssignmentID,
		"context", observation, file, candidate, request.CompletedAt,
	); err != nil {
		t.Fatalf("raw finding replay error = %v", err)
	}
	aliasless := observation
	aliasless.ModelAlias = ""
	if err := persistRawRepositoryReviewCheckpointFinding(
		&validState, "rrw_aliasless", bucket, plan, request.RunID, request.AssignmentID,
		"context-two", aliasless, file, candidate, request.CompletedAt,
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("aliasless checkpoint provenance error = %v", err)
	}

	archive := RepositoryState{
		CurrentCampaign: &RepositoryReviewCampaignCoverage{
			ID: plan.CampaignID, ScopeDigest: "scope", SelectedFiles: 2,
			Paths: map[string]RepositoryReviewCampaignPathCoverage{
				file.Path: {Completed: true}, "unsupported.go": {Unsupported: true},
			},
		},
		ActiveReviewRun: &RepositoryReviewActiveRun{
			ID: "run", CampaignID: plan.CampaignID, PlanID: "plan",
			Reservations: map[string]RepositoryReviewAssignmentReservation{
				assignment.ID: {AssignmentID: assignment.ID, Files: []FileRef{file}},
			},
		},
		Contexts: []FindingContext{{
			RunID: "run", CampaignID: plan.CampaignID, Model: "provider/reviewer",
		}},
	}
	archiveInterruptedRepositoryReviewRun(&archive, request.CompletedAt)
	if len(archive.Runs) != 1 || archive.Runs[0].RemainingFiles != 0 ||
		len(archive.Runs[0].Models) != 1 {
		t.Fatalf("interrupted canonical archive = %#v", archive.Runs)
	}
}

func TestCanonicalLifecycleFailureGapCoverage(t *testing.T) {
	loadFailure := newRepositoryAuditTestStore(t)
	loadFailure.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, errors.New("load failed")
	}
	if _, _, _, err := loadFailure.reconcileRepositoryJobs("owner/repo"); err == nil {
		t.Fatal("job reconciliation load failure ignored")
	}
	if _, _, err := loadFailure.RetryRunFindingStatus("owner/repo", []string{"rdf"}); err == nil {
		t.Fatal("mapping retry load failure ignored")
	}
	if _, _, _, _, err := loadFailure.ClaimMappingJob(
		"owner/repo", "job", RepositoryMappingModelSnapshot{},
	); err == nil {
		t.Fatal("mapping claim load failure ignored")
	}
	if _, _, err := loadFailure.SaveMappingAdjudication(
		"owner/repo", "job", RepositoryMappingAdjudication{Decision: "distinct", Confidence: 1},
	); err == nil {
		t.Fatal("adjudication save load failure ignored")
	}
	if _, _, err := loadFailure.CompleteMappingJob("owner/repo", RepositoryMappingCompletion{
		JobID: "job", CreateMatchState: RepositoryMatchNew,
	}); err == nil {
		t.Fatal("mapping completion load failure ignored")
	}
	if _, _, err := loadFailure.ResolvePossibleDuplicate("owner/repo", RepositoryDuplicateResolution{
		ProvisionalID: "provisional", CandidateID: "candidate", Decision: "distinct",
		ExpectedProvisionalVersion: 1,
	}); err == nil {
		t.Fatal("duplicate resolution load failure ignored")
	}
	if _, _, err := loadFailure.ReserveValidationJobs(
		"owner/repo", []string{"rrf"}, RepositoryMappingModelSnapshot{},
	); err == nil {
		t.Fatal("validation reservation load failure ignored")
	}
	if _, _, _, _, err := loadFailure.ClaimValidationJob("owner/repo", "job"); err == nil {
		t.Fatal("validation claim load failure ignored")
	}
	if _, _, err := loadFailure.SetValidationJobCandidates(
		"owner/repo", "job", []string{strings.Repeat("a", 40)},
	); err == nil {
		t.Fatal("validation candidates load failure ignored")
	}
	if _, _, _, err := loadFailure.CompleteValidationJob(
		"owner/repo", RepositoryValidationCompletion{JobID: "job", Outcome: RepositoryValidationNotFixed},
	); err == nil {
		t.Fatal("validation completion load failure ignored")
	}
	if _, _, err := loadFailure.UpdateRepositoryFindingIssueSnapshot(
		"owner/repo", RepositoryIssueSnapshotUpdate{
			RepositoryFindingID: "rrf", State: RepositoryFindingIssueNone,
		},
	); err == nil {
		t.Fatal("issue snapshot load failure ignored")
	}
	if _, _, err := loadFailure.SetRepositoryFindingLifecycle(
		"owner/repo", "rrf", RepositoryFindingOpen, 1,
	); err == nil {
		t.Fatal("manual lifecycle load failure ignored")
	}

	lockFailure := newRepositoryAuditTestStore(t)
	if err := os.MkdirAll(
		repositoryReviewTestLockPath(t, lockFailure.root, "store.lock"), 0o700,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lockFailure.RetryRunFindingStatus("owner/repo", []string{"rdf"}); err == nil {
		t.Fatal("mapping retry lock failure ignored")
	}
}

func TestCanonicalLifecycleValidationGapCoverage(t *testing.T) {
	if err := validateMappingModelSnapshot(RepositoryMappingModelSnapshot{
		ProfileID: "rrpf_gap", ProfileVersion: -1,
	}); err == nil {
		t.Fatal("negative profile snapshot version accepted")
	}
	for index, adjudication := range []RepositoryMappingAdjudication{
		{Decision: "bad"},
		{Decision: "distinct", Confidence: 2},
		{Decision: "distinct", Confidence: .5, MatchingAnchors: []string{"bad\nanchor"}},
		{Decision: "distinct", Confidence: .5, MatchingAnchors: []string{"same", " SAME "}},
	} {
		if err := validateMappingAdjudication(adjudication); err == nil {
			t.Errorf("invalid adjudication %d accepted", index)
		}
	}
	if _, err := normalizePossibleDuplicates([]RepositoryFindingPossibleDuplicate{
		{CandidateID: "same", Relation: "related", Confidence: .5},
		{CandidateID: "same", Relation: "related", Confidence: .5},
	}); err == nil {
		t.Fatal("duplicate possible candidate accepted")
	}
	for name, pair := range map[string]struct {
		job        RepositoryMappingJob
		completion RepositoryMappingCompletion
	}{
		"eligible same": {
			job: RepositoryMappingJob{Adjudication: RepositoryMappingAdjudication{
				Decision: "same", CandidateID: "candidate", Confidence: .99,
			}},
			completion: RepositoryMappingCompletion{RepositoryFindingID: "wrong"},
		},
		"low same": {
			job: RepositoryMappingJob{Adjudication: RepositoryMappingAdjudication{
				Decision: "same", CandidateID: "candidate", Confidence: .5,
			}},
			completion: RepositoryMappingCompletion{CreateMatchState: RepositoryMatchNew},
		},
		"uncertain": {
			job: RepositoryMappingJob{Adjudication: RepositoryMappingAdjudication{
				Decision: "uncertain", CandidateID: "candidate", Confidence: .5,
			}},
			completion: RepositoryMappingCompletion{CreateMatchState: RepositoryMatchNew},
		},
		"related": {
			job: RepositoryMappingJob{Adjudication: RepositoryMappingAdjudication{
				Decision: "related", CandidateID: "candidate", Confidence: .5,
			}},
			completion: RepositoryMappingCompletion{RepositoryFindingID: "candidate"},
		},
		"distinct": {
			job: RepositoryMappingJob{Adjudication: RepositoryMappingAdjudication{
				Decision: "distinct", Confidence: 1,
			}},
			completion: RepositoryMappingCompletion{RepositoryFindingID: "candidate"},
		},
	} {
		if err := mappingCompletionMatchesAdjudication(pair.job, pair.completion); err == nil {
			t.Errorf("invalid %s completion accepted", name)
		}
	}

	now := time.Now().UTC()
	state := RepositoryState{
		UpdatedAt: now,
		RepositoryFindings: []RepositoryFinding{
			{
				ID: "one", Lifecycle: RepositoryFindingOpen,
				Issue: RepositoryFindingIssueAssociation{State: RepositoryFindingIssueDraft},
			},
			{ID: "two", Lifecycle: RepositoryFindingOpen},
		},
		Findings: []Finding{
			{ID: "missing-draft", RepositoryFindingID: "one", IssueDraftID: "missing"},
			{ID: "first", RepositoryFindingID: "two", IssueDraftID: "draft-a"},
			{ID: "second", RepositoryFindingID: "two", IssueDraftID: "draft-b"},
		},
		IssueDrafts: []IssueDraft{
			{
				ID: "draft-a", State: IssueDraftPosted, ExternalID: "1",
				ExternalURL: "https://example.test/1", UpdatedAt: now,
			},
			{
				ID: "draft-b", State: IssueDraftPosted, ExternalID: "2",
				ExternalURL: "https://example.test/2", UpdatedAt: now,
			},
		},
	}
	synchronizeRepositoryFindingIssues(&state)
	if state.RepositoryFindings[0].Issue.State != RepositoryFindingIssueNone ||
		!state.RepositoryFindings[1].Issue.Conflict {
		t.Fatalf("synchronized issue projections = %#v", state.RepositoryFindings)
	}
}

func TestCanonicalIssueMutationBranchGapCoverage(t *testing.T) {
	repository := "owner/issue-branches"
	aggregate := RepositoryFinding{
		ID: "rrf", MatchState: RepositoryMatchKnown, Lifecycle: RepositoryFindingOpen,
		Issue: RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone},
	}
	unmappedStore := newRepositoryAuditTestStore(t)
	unmappedStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{
			Repository: repository, Findings: []Finding{{ID: "rdf", Status: FindingOpen}},
		}, nil
	}
	request := canonicalGapIssueGenerationRequest(repository)
	request.FindingID = "rdf"
	if _, _, _, err := unmappedStore.ReserveIssueGeneration(request); !errors.Is(err, ErrConflict) {
		t.Fatalf("unmapped issue reservation error = %v", err)
	}

	now := time.Now().UTC()
	wrongOrigin := newRepositoryAuditTestStore(t)
	wrongOrigin.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{
			Repository: repository,
			Findings: []Finding{{
				ID: "rdf", RepositoryFindingID: aggregate.ID,
				RepositoryMatchState: RepositoryMatchKnown, Status: FindingOpen, Version: 1,
			}},
			RepositoryFindings: []RepositoryFinding{aggregate},
			IssueDrafts: []IssueDraft{{
				ID: "draft", Origin: IssueDraftOriginLinked, FindingIDs: []string{"rdf"},
				State: IssueDraftPosted, Version: 1, CreatedAt: now, UpdatedAt: now,
			}},
		}, nil
	}
	if _, _, _, err := wrongOrigin.BeginIssueRegeneration(repository, "draft", request); !errors.Is(err, ErrConflict) {
		t.Fatalf("linked regeneration error = %v", err)
	}
	missingFinding := wrongOrigin
	missingFinding.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{
			Repository: repository,
			IssueDrafts: []IssueDraft{{
				ID: "draft", Origin: IssueDraftOriginAIGenerated, FindingIDs: []string{"rdf"},
			}},
		}, nil
	}
	regenerationState, regenerationDraft,
		regenerationReserved, regenerationErr := missingFinding.BeginIssueRegeneration(
		repository, "draft", request,
	)
	if !errors.Is(regenerationErr, ErrConflict) {
		t.Fatalf(
			"missing regeneration finding state=%#v draft=%#v reserved=%v error=%v",
			regenerationState, regenerationDraft, regenerationReserved, regenerationErr,
		)
	}
	posted := Finding{
		ID: "rdf", RepositoryFindingID: aggregate.ID,
		RepositoryMatchState: RepositoryMatchKnown, Status: FindingPosted, Version: 1,
	}
	postedStore := newRepositoryAuditTestStore(t)
	postedStore.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{
			Repository: repository, Findings: []Finding{posted},
			RepositoryFindings: []RepositoryFinding{aggregate},
		}, nil
	}
	if _, _, err := postedStore.LinkExistingIssue(ExistingIssueLink{
		Repository: repository, FindingID: posted.ID, ExpectedFindingVersion: 1,
		ExternalID: "1", ExternalURL: "https://github.com/owner/repo/issues/1",
		Title: "title", Confirmed: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("posted finding link error = %v", err)
	}
}

func TestCanonicalStorePublicationBranchGapCoverage(t *testing.T) {
	repository := "owner/repo"
	now := time.Now().UTC()
	stateForDraft := func(draftState IssueDraftState) RepositoryState {
		draft := IssueDraft{
			ID: "draft", Repository: repository, FindingIDs: []string{"rdf"},
			Origin: IssueDraftOriginAIGenerated, State: draftState, Version: 2,
			Title: "title", Body: "body", Labels: []string{"bug"},
			GenerationID: "generation", ResolvedInstructions: "instructions",
			InstructionsMode: IssueDraftInstructionsDefault,
			GeneratorModel:   "writer", GeneratorAccount: "account",
			CreatedAt: now, UpdatedAt: now,
		}
		return RepositoryState{
			SchemaVersion: SchemaVersion, ID: RepositoryID(repository), Repository: repository,
			Version: 1, ReviewVersion: 1, UpdatedAt: now,
			Files: map[string]ReviewedFile{}, Unsupported: map[string]UnsupportedFile{},
			ReviewAttempts: map[string]int{}, ReviewAttemptIdentities: map[string]string{},
			CampaignHistory: map[string]string{},
			Findings: []Finding{{
				ID: "rdf", RepositoryFindingID: "rrf", RepositoryMatchState: RepositoryMatchKnown,
				IssueDraftID: draft.ID, Status: FindingOpen, Version: 1,
			}},
			RepositoryFindings: []RepositoryFinding{{
				ID: "rrf", MatchState: RepositoryMatchKnown, Lifecycle: RepositoryFindingOpen,
				Issue: RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone},
			}},
			IssueDrafts: []IssueDraft{draft},
		}
	}
	storeWithState := func(state RepositoryState) Store {
		store := newRepositoryAuditTestStore(t)
		store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
		return store
	}
	if _, _, err := storeWithState(stateForDraft(IssueDraftEditing)).SetIssueDraftPublication(
		repository, "draft", 2, IssueDraftGenerating, "", "",
	); err == nil {
		t.Fatal("invalid publication state accepted")
	}
	if _, _, err := storeWithState(stateForDraft(IssueDraftEditing)).SetIssueDraftPublication(
		repository, "missing", 2, IssueDraftUnknown, "", "",
	); err == nil {
		t.Fatal("missing publication draft accepted")
	}
	posted := stateForDraft(IssueDraftPosted)
	if _, _, err := storeWithState(posted).SetIssueDraftPublication(
		repository, "draft", 2, IssueDraftPosted, "1", "https://github.com/owner/repo/issues/1",
	); err != nil {
		t.Fatalf("posted publication replay error = %v", err)
	}
	if _, _, err := storeWithState(stateForDraft(IssueDraftUnknown)).SetIssueDraftPublication(
		repository, "draft", 1, IssueDraftUnknown, "", "",
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale publication error = %v", err)
	}
	if _, _, err := storeWithState(stateForDraft(IssueDraftEditing)).SetIssueDraftPublication(
		repository, "draft", 2, IssueDraftUnknown, "", "",
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("editing publication transition error = %v", err)
	}
	if _, _, err := storeWithState(stateForDraft(IssueDraftUnknown)).SetIssueDraftPublication(
		repository, "draft", 2, IssueDraftEditing, "", "",
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown-to-editing publication error = %v", err)
	}
	if _, _, err := storeWithState(stateForDraft(IssueDraftPublishing)).SetIssueDraftPublication(
		repository, "draft", 2, IssueDraftPosted, "", "bad-url",
	); err == nil {
		t.Fatal("posted publication without identity accepted")
	}
	withUnselected := stateForDraft(IssueDraftPublishing)
	withUnselected.Findings = append(withUnselected.Findings, Finding{ID: "unselected"})
	publicationState, publicationDraft, publicationErr := storeWithState(withUnselected).SetIssueDraftPublication(
		repository, "draft", 2, IssueDraftPosted, "1", "https://github.com/owner/repo/issues/1",
	)
	_ = publicationState
	_ = publicationDraft
	_ = publicationErr

	if _, _, _, err := storeWithState(stateForDraft(IssueDraftEditing)).ClaimIssueDraftPublication(
		repository, "missing", 2,
	); err == nil {
		t.Fatal("missing publication claim accepted")
	}
	posted = stateForDraft(IssueDraftPosted)
	posted.IssueDrafts[0].ExternalID = "1"
	posted.IssueDrafts[0].ExternalURL = "https://github.com/owner/repo/issues/1"
	posted.Findings[0].Status = FindingPosted
	if _, _, claimed, err := storeWithState(posted).ClaimIssueDraftPublication(
		repository, "draft", 2,
	); err != nil || claimed {
		t.Fatalf("posted publication acknowledgement = %v, %v", claimed, err)
	}
	unpublishable := stateForDraft(IssueDraftEditing)
	unpublishable.Repository = "local/repo/extra"
	if _, _, _, err := storeWithState(unpublishable).ClaimIssueDraftPublication(
		unpublishable.Repository, "draft", 2,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("unpublishable claim error = %v", err)
	}
	if _, _, claimed, err := storeWithState(stateForDraft(IssueDraftPublishing)).ClaimIssueDraftPublication(
		repository, "draft", 2,
	); err != nil || claimed {
		t.Fatalf("publishing claim replay = %v, %v", claimed, err)
	}
	if _, _, _, err := storeWithState(stateForDraft(IssueDraftEditing)).ClaimIssueDraftPublication(
		repository, "draft", 1,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale editing claim error = %v", err)
	}
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestCanonicalStorePlanBranchGapCoverage(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	catalog, err := repositoryReviewAssignmentCatalogCountForTest("profile-gap", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	campaignID := NewRepositoryReviewCampaignID()
	validArgs := func(ctx context.Context, files []FileRef, maximum int) (Plan, error) {
		return store.planWithProfileLimitAuthoritative(
			ctx, "owner/repo", repositoryReviewCampaignTestCommit,
			repositoryReviewCampaignTestInventory, "profile-gap", campaignID,
			catalog, files, false, maximum, true,
		)
	}
	if _, err := store.planWithProfileLimitAuthoritative(
		context.Background(), "", repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, "profile-gap", campaignID,
		catalog, nil, false, 1, true,
	); err == nil {
		t.Fatal("empty plan repository accepted")
	}
	if _, err := validArgs(context.Background(), []FileRef{{Path: "bad", BlobSHA: "bad"}}, 1); err == nil {
		t.Fatal("invalid plan file accepted")
	}
	if _, err := validArgs(context.Background(), nil, 0); err == nil {
		t.Fatal("zero pending limit accepted")
	}
	tooManyFiles := make([]FileRef, maxReviewFiles+1)
	for index := range tooManyFiles {
		tooManyFiles[index] = repositoryAuditTestFile(
			fmt.Sprintf("oversized/%06d.go", index), "a", int64(index),
		)
	}
	if _, err := validArgs(context.Background(), tooManyFiles, 1); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("oversized plan error = %v", err)
	}

	loadFailure := newRepositoryAuditTestStore(t)
	loadFailure.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, errors.New("load failed")
	}
	if _, err := loadFailure.planWithProfileLimitAuthoritative(
		context.Background(), "owner/repo", repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, "profile-gap", campaignID,
		catalog, nil, false, 1, true,
	); err == nil {
		t.Fatal("plan load failure ignored")
	}
	lockFailure := newRepositoryAuditTestStore(t)
	if err := os.MkdirAll(
		repositoryReviewTestLockPath(t, lockFailure.root, "store.lock"), 0o700,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := lockFailure.planWithProfileLimitAuthoritative(
		context.Background(), "owner/repo", repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, "profile-gap", campaignID,
		catalog, nil, false, 1, true,
	); err == nil {
		t.Fatal("plan lock failure ignored")
	}

	branchFixture := newAssignmentCoverageFixture(t, 5, 5)
	branchState, _, err := branchFixture.store.Get(branchFixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	branchState.ActiveForceCampaignID = "rfc_existing"
	branchState.ActiveForceProfileHash = branchFixture.plan.ProfileHash
	branchState.ActiveForceCommitSHA = branchFixture.plan.CommitSHA
	branchState.Unsupported[branchFixture.files[0].Path] = UnsupportedFile{
		FileRef: branchFixture.files[0], CommitSHA: branchFixture.plan.CommitSHA,
		ProfileHash: branchFixture.plan.ProfileHash, ForceCampaignID: "rfc_existing",
		Reason: "generated", UpdatedAt: repositoryAuditTestNow,
	}
	completed, err := creditAllRequiredRepositoryReviewAssignmentsForTest(
		RepositoryReviewCampaignPathCoverage{}, branchFixture.catalog,
	)
	if err != nil {
		t.Fatal(err)
	}
	branchState.CurrentCampaign.Paths[branchFixture.files[1].Path] = completed
	branchState.ReviewAttempts[branchFixture.files[2].Path] = 2
	branchState.ReviewAttemptIdentities[branchFixture.files[2].Path] = reviewAttemptIdentity(
		branchFixture.files[2], branchFixture.plan.ProfileHash,
	)
	branchStore := branchFixture.store
	branchStore.loadForTest = func(string) (RepositoryState, error) { return branchState, nil }
	branchPlan, err := branchStore.planWithProfileLimitAuthoritative(
		context.Background(), branchFixture.repository, branchFixture.plan.CommitSHA,
		branchFixture.plan.InventoryHash, branchFixture.plan.ProfileHash, branchFixture.campaignID,
		branchFixture.catalog, branchFixture.files, true, len(branchFixture.files), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if branchPlan.ForceCampaignID != "rfc_existing" || len(branchPlan.UnsupportedFiles) != 1 ||
		len(branchPlan.UnchangedFiles) != 1 || len(branchPlan.PendingFiles) != 3 ||
		branchPlan.PendingFiles[len(branchPlan.PendingFiles)-1].Path != branchFixture.files[2].Path {
		t.Fatalf("canonical branch plan = %#v", branchPlan)
	}

	if _, err := BindPlanBranch(Plan{}, "bad..branch", "main", false); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid target branch error = %v", err)
	}
	if _, err := BindPlanBranch(Plan{}, "main", "other", true); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("contradictory branch error = %v", err)
	}
	if _, err := loadFailure.SnapshotMappingJobs(
		"owner/repo", []string{"rdf"}, RepositoryMappingModelSnapshot{Model: "reviewer"},
	); err == nil {
		t.Fatal("mapping snapshot load failure ignored")
	}

	fixture := newAssignmentCoverageFixture(t, 1, 1)
	base := fixture.plan
	badGroup := base
	badGroup.UnchangedFiles = append([]FileRef(nil), badGroup.PendingFiles...)
	if _, err := validateRepositoryReviewCampaignPlan(badGroup); err == nil {
		t.Fatal("duplicate plan group accepted")
	}
	badReason := base
	badReason.PendingFiles = nil
	badReason.AssignmentPlans = nil
	badReason.UnsupportedFiles = []UnsupportedFile{{
		FileRef: fixture.files[0], Reason: " bad ",
	}}
	if _, err := validateRepositoryReviewCampaignPlan(badReason); err == nil {
		t.Fatal("non-canonical unsupported reason accepted")
	}
	badUnsupported := base
	badUnsupported.UnsupportedFiles = []UnsupportedFile{{
		FileRef: fixture.files[0], Reason: "binary",
	}}
	if _, err := validateRepositoryReviewCampaignPlan(badUnsupported); err == nil {
		t.Fatal("overlapping unsupported file accepted")
	}

	invalidAttempt := repositoryReviewCoverageState("owner/invalid-attempt")
	invalidAttempt.ReviewAttempts["bad"] = 1
	invalidAttempt.ReviewAttemptIdentities[strings.Repeat("x", 4097)] = "identity"
	if validateState(invalidAttempt) == nil {
		t.Fatal("invalid attempt identity accepted")
	}
	invalidFinding := repositoryReviewCoverageState("owner/invalid-finding")
	invalidFinding.Findings = []Finding{{
		ID: "rdf", CampaignID: NewRepositoryReviewCampaignID(), CommitSHA: repositoryReviewCampaignTestCommit,
	}}
	if validateState(invalidFinding) == nil {
		t.Fatal("finding without campaign history accepted")
	}
	invalidBindings := repositoryReviewCoverageState("owner/invalid-bindings")
	invalidBindings.Runs = []ReviewRun{{}}
	if validateState(invalidBindings) == nil {
		t.Fatal("invalid record binding accepted")
	}
}

func TestCanonicalStoreReadGapCoverage(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	if _, found, err := store.GetByID("bad"); err != nil || found {
		t.Fatalf("invalid state ID lookup = %v, %v", found, err)
	}
	if _, found, err := store.GetByID(RepositoryID("owner/missing")); err != nil || found {
		t.Fatalf("missing state ID lookup = %v, %v", found, err)
	}
	if _, found, err := store.ResolveRepositoryState("owner/missing"); err != nil || found {
		t.Fatalf("missing canonical state lookup = %v, %v", found, err)
	}
	state := repositoryReviewCoverageState("owner/list-gap")
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.listSummaries(0); err == nil {
		t.Fatal("zero-limit summary catalog accepted existing row")
	}
	lockFailure := newRepositoryAuditTestStore(t)
	if err := os.MkdirAll(
		repositoryReviewTestLockPath(t, lockFailure.root, "store.lock"), 0o700,
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lockFailure.Get("owner/repo"); err == nil {
		t.Fatal("state read lock failure ignored")
	}
}

func TestCanonicalControlGapCoverage(t *testing.T) {
	base := validAutomationForTest("rra_control_gap", "Control gap")
	base.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	base.Version = 1
	base.CreatedAt = automationTestNow
	base.UpdatedAt = automationTestNow
	withoutProfile := base
	withoutProfile.ProfileVersion = 1
	if err := validateAutomation(withoutProfile); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("profile version without profile error = %v", err)
	}
	badProfile := base
	badProfile.ProfileID = "bad"
	badProfile.ProfileVersion = 1
	if err := validateAutomation(badProfile); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("invalid assigned profile error = %v", err)
	}
	store := newAutomationTestStore(t)
	if err := store.ensureRepositoryAutomationUniqueUnlocked("rra_gap", " "); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("empty canonical automation repository error = %v", err)
	}
	missingProfile := base
	missingProfile.ProfileID = "rrpf_missing"
	missingProfile.ProfileVersion = 1
	if err := store.validateAutomationProfileSnapshotUnlocked(missingProfile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing profile snapshot error = %v", err)
	}
	if canonicalAutomationRepository(" ") != "" {
		t.Fatal("empty automation repository became canonical")
	}
	local := filepath.Join(t.TempDir(), "nested", "..", "repository")
	if got := canonicalAutomationRepository(local); got != filepath.Clean(local) {
		t.Fatalf("canonical local repository = %q", got)
	}

	saveFailure := newAutomationTestStore(t)
	poisonRepositoryReviewStoreOnClock(t, &saveFailure)
	if _, err := saveFailure.CreateAutomation(
		context.Background(), validAutomationForTest("rra_create_save_gap", "Create save gap"),
	); err == nil {
		t.Fatal("automation create save failure ignored")
	}
}

func TestCanonicalCheckpointRemainingHelperGapCoverage(t *testing.T) {
	file := repositoryAuditTestFile("remaining.go", "c", 1)
	catalog, err := repositoryReviewAssignmentCatalogCountForTest("profile-gap", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		CampaignID: NewRepositoryReviewCampaignID(), Repository: "owner/repo",
		CommitSHA: repositoryReviewCampaignTestCommit, InventoryHash: "inventory",
		ProfileHash: "profile-gap", AssignmentCatalog: catalog, TargetIsDefault: true,
	}
	observation := Observation{
		Model: "provider/reviewer", ModelAlias: "reviewer", Account: "account",
		Reviewer: catalog[0].FocusID, ScopeFiles: []FileRef{file},
		RawDigest: "sha256:" + strings.Repeat("d", 64),
	}
	request := CheckpointRepositoryReviewAssignmentRequest{
		AutomationID: "rra_gap", RunID: "run", Plan: plan, AssignmentID: catalog[0].ID,
		AgentID: "main", ChildIndex: 1, Observation: observation,
		CompletedAt: repositoryAuditTestNow,
	}
	retained, err := NewRepositoryReviewFileAttribution(
		repositoryReviewCheckpointAttribution(request, catalog[0], []FileRef{file}, request.CompletedAt),
	)
	if err != nil {
		t.Fatal(err)
	}
	drifted := retained
	drifted.Model = "other-model"
	if _, err := reconcileRepositoryReviewCheckpointAttribution(
		&RepositoryState{FileAttributions: []RepositoryReviewFileAttribution{drifted}},
		request, catalog[0], []FileRef{file},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("drifted retained attribution error = %v", err)
	}
	state := RepositoryState{}
	if changed, err := reconcileRepositoryReviewCheckpointAttribution(
		&state, request, catalog[0], []FileRef{file},
	); err != nil || !changed || len(state.FileAttributions) != 1 {
		t.Fatalf("repaired retained attribution = %v, %#v, %v", changed, state.FileAttributions, err)
	}

	checkpointState := RepositoryState{CurrentCampaign: &RepositoryReviewCampaignCoverage{
		DeduplicationSnapshot: repositoryReviewDeduplicationSnapshotForTest(),
	}}
	candidate := canonicalGapFindingCandidate(file.Path)
	badObservation := observation
	badObservation.Account = ""
	badObservation.Findings = []FindingCandidate{candidate}
	if _, err := persistRepositoryReviewCheckpointObservation(
		&checkpointState, plan, request.RunID, request.AssignmentID,
		badObservation, []FileRef{file}, request.CompletedAt,
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid persisted observation provenance error = %v", err)
	}
	if _, err := (Store{}).FinalizeRepositoryReviewRun(nil, FinalizeRepositoryReviewRunRequest{}); err == nil {
		t.Fatal("empty finalization accepted after nil context normalization")
	}

	coverage := canonicalGapCampaignCoverage(t)
	coverage.Paths[file.Path] = RepositoryReviewCampaignPathCoverage{Completed: true}
	cloned := cloneRepositoryReviewCampaignCoverage(coverage)
	if !cloned.Paths[file.Path].Completed {
		t.Fatal("campaign path clone omitted entry")
	}
	partial := coverage
	partial.ProfileHash = ""
	partial.ScopeDigest = ""
	partial.RequiredAssignments = 0
	partial.SelectedFiles = 0
	partial.AssignmentCatalog = nil
	partial.Paths = map[string]RepositoryReviewCampaignPathCoverage{file.Path: {Completed: true}}
	if validateRepositoryReviewCampaignCoverage(&partial) == nil {
		t.Fatal("unbound campaign with paths accepted")
	}
}

func TestCanonicalMappingProcessGapCoverage(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	state := recordMappingWorkerFinding(
		t, store, "mapping-cancel-gap", strings.Repeat("1", 40),
		"wait.go", "wait.signal",
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, processErr := store.ProcessPendingMappingJobs(
		ctx, state.Repository, RepositoryMappingProcessOptions{},
	)
	if !errors.Is(processErr, context.Canceled) {
		t.Fatalf("canceled mapping process error = %v", processErr)
	}

	base := recordMappingWorkerFinding(
		t, store, "mapping-base-gap", strings.Repeat("2", 40),
		"old/wait.go", "awaiter.signal",
	)
	if _, err := store.ProcessPendingMappingJobs(
		context.Background(), base.Repository,
		RepositoryMappingProcessOptions{DefaultBranchVerified: func(context.Context, Finding) (bool, error) {
			return true, nil
		}},
	); err != nil {
		t.Fatal(err)
	}
	later := recordMappingWorkerFinding(
		t, store, "mapping-later-gap", strings.Repeat("3", 40),
		"moved/wait.go", "predicate.resume",
	)
	result, err := store.ProcessPendingMappingJobs(
		context.Background(), later.Repository,
		RepositoryMappingProcessOptions{DefaultBranchVerified: func(context.Context, Finding) (bool, error) {
			return true, nil
		}},
	)
	if err != nil || result.PendingAI == 0 {
		t.Fatalf("pending AI mapping result = %#v, %v", result, err)
	}

	loaded, _, err := store.Get(later.Repository)
	if err != nil {
		t.Fatal(err)
	}
	for index := range loaded.MappingJobs {
		if loaded.MappingJobs[index].State == RepositoryMappingPending {
			loaded.MappingJobs[index].ModelSnapshot = RepositoryMappingModelSnapshot{Model: "frozen"}
		}
	}
	frozen := newRepositoryAuditTestStore(t)
	frozen.loadForTest = func(string) (RepositoryState, error) { return loaded, nil }
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	_, _ = frozen.ProcessPendingMappingJobs(ctx, loaded.Repository, RepositoryMappingProcessOptions{})
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestCanonicalPurgeAndSQLiteGapCoverage(t *testing.T) {
	store := newAutomationTestStore(t)
	automation := createAutomationForTest(t, store, "rra_snapshot_inventory_gap", "Snapshot inventory gap")
	loads := 0
	store.loadForTest = func(repository string) (RepositoryState, error) {
		loads++
		if loads == 1 {
			return RepositoryState{Repository: repository, Version: 1}, nil
		}
		return RepositoryState{}, errors.New("inventory failed")
	}
	snapshot, err := store.RepositoryReviewAutomationSnapshot(context.Background(), automation.ID)
	if err != nil || snapshot.PurgeInventoryError == nil {
		t.Fatalf("snapshot inventory failure = %#v, %v", snapshot, err)
	}

	noncanonical := repositoryReviewPurgeIntent{
		SchemaVersion: repositoryReviewPurgeIntentSchemaVersion,
		Mode:          repositoryReviewPurgeReset, Phase: repositoryReviewPurgePrepared,
		AutomationID: "rra_noncanonical_gap", ConfiguredRepository: "owner/repo",
		Repository: "owner/other", ExpectedAutomationVersion: 1,
		CreatedAt: automationTestNow,
	}
	if _, err := NewStore(t.TempDir()).applyPurgeIntent(noncanonical); err == nil {
		t.Fatal("noncanonical purge intent applied")
	}
	missing := noncanonical
	missing.AutomationID = "rra_missing_gap"
	missing.Repository = missing.ConfiguredRepository
	if _, err := NewStore(t.TempDir()).applyPurgeIntent(missing); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing prepared automation error = %v", err)
	}
	openFailure := Store{openForTest: func(context.Context) (*sql.DB, error) {
		return nil, errors.New("database failed")
	}}
	if _, err := openFailure.repositoryReviewPurgeRetirementMatches(noncanonical); err == nil {
		t.Fatal("retirement lookup database failure ignored")
	}
	if err := openFailure.removeRepositoryReviewPurgeRetirement("rra_gap"); err == nil {
		t.Fatal("retirement removal database failure ignored")
	}
	if _, _, _, err := openFailure.validatePreparedPurgeIntent(missing); err == nil {
		t.Fatal("prepared automation database failure ignored")
	}
	if err := openFailure.applyPurgeAutomationPhase(missing); err == nil {
		t.Fatal("purge automation database failure ignored")
	}
	if err := openFailure.verifyPurgeAutomationApplied(missing); err == nil {
		t.Fatal("purge verification database failure ignored")
	}
	if err := NewStore(t.TempDir()).removeRepositoryReviewLedgers([]repositoryReviewPurgeLedgerTarget{
		{Repository: "owner/one", Version: 1}, {Repository: "owner/two", Version: 1},
	}); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("multi-ledger removal error = %v", err)
	}
	missingRoot := NewStore(filepath.Join(t.TempDir(), "missing-workspace"))
	if _, found, err := missingRoot.loadPrimaryPurgeFenceMatching(func(repositoryReviewPurgeIntent) bool {
		return true
	}); err != nil || found {
		t.Fatalf("missing purge fence root = %v, %v", found, err)
	}
	badRoot := NewStore(t.TempDir())
	if err := os.WriteFile(badRoot.root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := badRoot.savePurgeIntent(missing); err == nil {
		t.Fatal("purge intent unsafe root accepted")
	}

	databaseStore := newRepositoryAuditTestStore(t)
	database, err := databaseStore.openDatabase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `CREATE TABLE unexpected_gap (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := validateRepositoryReviewSchemaObjectSet(context.Background(), conn); err == nil {
		t.Fatal("unexpected repository-review schema object accepted")
	}
	if err := validateRepositoryReviewDatabaseSchema(context.Background(), conn); err == nil {
		t.Fatal("database schema with unexpected object accepted")
	}

	legacyRoot := t.TempDir()
	for _, name := range []string{"profile_gap.json", "automation_gap.json"} {
		if err := os.WriteFile(filepath.Join(legacyRoot, name), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := legacyRepositoryReviewSources(legacyRoot)
	if err != nil || len(sources) != 2 || sources[0].Order != 2 || sources[1].Order != 0 {
		t.Fatalf("configuration import sources = %#v, %v", sources, err)
	}
	invalidInput := sqlitestoreLegacyInputForGap("automation_bad.json", []byte(`{`))
	result, err := importLegacyRepositoryReviewSource(context.Background(), conn, invalidInput)
	if err != nil || result.Skipped != 1 {
		t.Fatalf("invalid configuration import = %#v, %v", result, err)
	}
	state := repositoryReviewCoverageState("owner/version-zero-gap")
	state.Version = 0
	if err := saveRepositoryStateDatabase(context.Background(), database, &state); err != nil || state.Version != 1 {
		t.Fatalf("zero-version state save = %d, %v", state.Version, err)
	}
	huge := validAutomationForTest("rra_huge_gap", "Huge gap")
	huge.ReviewFocus = strings.Repeat("x", int(maxAutomationFileBytes))
	if _, err := insertRepositoryReviewAutomationConn(
		context.Background(), conn, huge, true, 0,
	); err == nil {
		t.Fatal("oversized automation row accepted")
	}
}

func sqlitestoreLegacyInputForGap(relative string, data []byte) sqlitestore.LegacyInput {
	return sqlitestore.LegacyInput{Relative: relative, Data: data}
}

func TestCanonicalPureFunctionTailGapCoverage(t *testing.T) {
	if got := GitHubRepositoryIdentity("git@github.com:owner/repo.git"); got != "owner/repo" {
		t.Fatalf("SCP repository identity = %q", got)
	}

	state := RepositoryState{
		UpdatedAt: time.Now().UTC(),
		RawFindings: []RawReviewFinding{{
			ID: "rrw_counter", InsertionOrdinal: 7,
			State: RawFindingDeduplicationPending,
		}},
	}
	if !reconcileFindingsProcessingCounters(&state) || state.NextDeduplicationOrdinal != 8 {
		t.Fatalf("reconciled counters = %#v, next=%d", state.FindingsProcessing, state.NextDeduplicationOrdinal)
	}
	if validRepositoryReviewBranchProvenance(" main ", "main", true) {
		t.Fatal("non-canonical branch provenance accepted")
	}

	finding := Finding{CommitSHA: "commit", Fingerprint: "fingerprint"}
	aggregate := RepositoryFinding{ID: "rrf_exact", ReviewFindingIDs: []string{"rdf_exact"}}
	matched := MatchRepositoryFinding(
		finding,
		[]RepositoryFinding{aggregate},
		map[string]Finding{"rdf_exact": {CommitSHA: "commit", Fingerprint: "fingerprint"}},
		nil,
	)
	if matched.RepositoryFindingID != aggregate.ID || matched.Method != "exact_same_commit_fingerprint" {
		t.Fatalf("exact match = %#v", matched)
	}

	candidates := make([]RepositoryFinding, repositoryMatchCandidateLimit+2)
	for index := range candidates {
		candidates[index] = RepositoryFinding{
			ID:         fmt.Sprintf("rrf_rank_%02d", index),
			MatchHints: MatchHints{Component: "scheduler"},
		}
	}
	ranked := MatchRepositoryFinding(
		Finding{MatchHints: MatchHints{Component: "scheduler"}}, candidates, nil, nil,
	)
	if len(ranked.Candidates) != repositoryMatchCandidateLimit {
		t.Fatalf("bounded match candidates = %d", len(ranked.Candidates))
	}
}
