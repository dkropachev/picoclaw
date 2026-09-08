package repoaudit

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMappingAdjudicationAssociationDefaultBranchFenceAndRestart(t *testing.T) {
	store := NewStore(t.TempDir())
	state, first := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), "run-first",
		"main", "main", true, "first defect",
	)
	snapshot := RepositoryMappingModelSnapshot{
		ProfileID: "rrpf_mapping", ProfileVersion: 2, Model: "reviewer", Account: "account",
	}
	_, claimedJob, _, claimed, err := store.ClaimMappingJob(
		state.Repository, state.MappingJobs[0].ID, snapshot,
	)
	if err != nil || !claimed || claimedJob.State != RepositoryMappingRunning {
		t.Fatalf("claim = %#v claimed=%v err=%v", claimedJob, claimed, err)
	}
	adjudication := RepositoryMappingAdjudication{
		Decision: "distinct", CandidateID: "opaque-candidate", Confidence: .98,
		Explanation: "Causal anchors conflict.",
	}
	if _, _, saveErr := store.SaveMappingAdjudication(
		state.Repository,
		claimedJob.ID,
		adjudication,
	); saveErr != nil {
		t.Fatal(saveErr)
	}
	if _, _, replayErr := store.SaveMappingAdjudication(
		state.Repository,
		claimedJob.ID,
		adjudication,
	); replayErr != nil {
		t.Fatalf("adjudication replay: %v", replayErr)
	}
	completed, repositoryFinding, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: claimedJob.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil || repositoryFinding.MatchState != RepositoryMatchNew ||
		completed.Findings[0].RepositoryFindingID != repositoryFinding.ID {
		t.Fatalf("completion = %#v / %#v err=%v", completed.Findings, repositoryFinding, err)
	}
	if replay, same, replayErr := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: claimedJob.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	}); replayErr != nil || same.ID != repositoryFinding.ID || len(replay.RepositoryFindings) != 1 {
		t.Fatalf("completion replay = %#v / %#v err=%v", replay, same, replayErr)
	}

	secondState, second := recordLifecycleFinding(
		t, store, strings.Repeat("c", 40), strings.Repeat("d", 40), "run-branch",
		"feature", "main", false, "branch-only defect",
	)
	secondJob := lifecycleJobForFinding(t, secondState, second.ID)
	_, secondJob, _, claimed, err = store.ClaimMappingJob(
		secondState.Repository,
		secondJob.ID,
		RepositoryMappingModelSnapshot{},
	)
	if err != nil || !claimed {
		t.Fatalf("second claim=%v err=%v", claimed, err)
	}
	if _, _, completionErr := store.CompleteMappingJob(secondState.Repository, RepositoryMappingCompletion{
		JobID: secondJob.ID, CreateMatchState: RepositoryMatchNew,
	}); completionErr == nil || !strings.Contains(completionErr.Error(), "non-default") {
		t.Fatalf("non-default create error = %v", completionErr)
	}
	joined, known, err := store.CompleteMappingJob(secondState.Repository, RepositoryMappingCompletion{
		JobID: secondJob.ID, RepositoryFindingID: repositoryFinding.ID,
	})
	if err != nil || len(known.ReviewFindingIDs) != 2 ||
		joined.Findings[findingIndexByID(joined.Findings, second.ID)].RepositoryMatchState != RepositoryMatchKnown {
		t.Fatalf("non-default association = %#v err=%v", known, err)
	}
	for name, mutate := range map[string]func(*RepositoryState){
		"identity": func(candidate *RepositoryState) {
			oldID := candidate.RepositoryFindings[0].ID
			newID := stableID("rrf_", candidate.Repository, "forged-first-occurrence")
			candidate.RepositoryFindings[0].ID = newID
			for index := range candidate.Findings {
				if candidate.Findings[index].RepositoryFindingID == oldID {
					candidate.Findings[index].RepositoryFindingID = newID
				}
			}
			for index := range candidate.MappingJobs {
				if candidate.MappingJobs[index].RepositoryFindingID == oldID {
					candidate.MappingJobs[index].RepositoryFindingID = newID
				}
			}
		},
		"commit set": func(candidate *RepositoryState) {
			candidate.RepositoryFindings[0].FoundCommits = append(
				candidate.RepositoryFindings[0].FoundCommits, strings.Repeat("9", 40),
			)
		},
		"path history": func(candidate *RepositoryState) {
			candidate.RepositoryFindings[0].PathSymbolHistory[0].Path = "forged.go"
		},
	} {
		t.Run("rejects repository finding "+name+" drift", func(t *testing.T) {
			candidate := dedupDeepCloneState(t, joined)
			mutate(&candidate)
			if err := validateState(candidate); err == nil {
				t.Fatalf("repository finding %s drift was accepted", name)
			}
		})
	}

	thirdState, third := recordLifecycleFinding(
		t, store, strings.Repeat("e", 40), strings.Repeat("f", 40), "run-restart",
		"main", "main", true, "restart defect",
	)
	thirdJob := lifecycleJobForFinding(t, thirdState, third.ID)
	claimLifecycleMappingJob(t, store, thirdState.Repository, thirdJob, snapshot)
	if _, reconcileErr := store.ReconcileJobs(context.Background()); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	after, _, _ := store.Get(thirdState.Repository)
	thirdJob = lifecycleJobForFinding(t, after, third.ID)
	if thirdJob.State != RepositoryMappingPending || thirdJob.ModelSnapshot != snapshot ||
		thirdJob.ReservedAt != (time.Time{}) {
		t.Fatalf("reconciled mapping job = %#v", thirdJob)
	}
	_ = first
}

func TestValidationQueueCommitFenceRegressionAndIssueTTL(t *testing.T) {
	store := NewStore(t.TempDir())
	clock := time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	state, finding := recordLifecycleFinding(
		t, store, strings.Repeat("1", 40), strings.Repeat("2", 40), "resolution-run",
		"main", "main", true, "resolvable defect",
	)
	job := lifecycleJobForFinding(t, state, finding.ID)
	job = claimLifecycleMappingJob(t, store, state.Repository, job, RepositoryMappingModelSnapshot{})
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, jobs, err := store.ReserveValidationJobs(
		state.Repository, []string{aggregate.ID}, RepositoryMappingModelSnapshot{Model: "reviewer"},
	)
	if err != nil || len(jobs) != 1 || jobs[0].State != RepositoryValidationPending {
		t.Fatalf("validation reserve=%#v err=%v", jobs, err)
	}
	state, running, _, claimed, err := store.ClaimValidationJob(state.Repository, jobs[0].ID)
	if err != nil || !claimed || running.State != RepositoryValidationRunning {
		t.Fatalf("validation claim=%#v claimed=%v err=%v", running, claimed, err)
	}
	fixCommit := strings.Repeat("3", 40)
	otherCommit := strings.Repeat("4", 40)
	state, running, err = store.SetValidationJobCandidates(state.Repository, running.ID, []string{fixCommit})
	if err != nil {
		t.Fatal(err)
	}
	outsideState, outsideFinding, outsideJob, outsideErr := store.CompleteValidationJob(
		state.Repository,
		RepositoryValidationCompletion{
			JobID: running.ID, Outcome: RepositoryValidationConfirmed,
			SelectedCommitSHA: otherCommit, FixCommitTime: clock,
		})
	if outsideErr == nil || !strings.Contains(outsideErr.Error(), "outside") ||
		outsideState.Repository != "" || outsideFinding.ID != "" || outsideJob.ID != "" {
		t.Fatalf(
			"unsupplied commit result = %#v / %#v / %#v, error = %v",
			outsideState,
			outsideFinding,
			outsideJob,
			outsideErr,
		)
	}
	invalidTagState, invalidTagFinding, invalidTagJob, invalidTagErr := store.CompleteValidationJob(
		state.Repository,
		RepositoryValidationCompletion{
			JobID: running.ID, Outcome: RepositoryValidationConfirmed,
			SelectedCommitSHA: fixCommit, FixCommitTime: clock, FirstContainingTag: "release-one",
		})
	if invalidTagErr == nil || !strings.Contains(invalidTagErr.Error(), "semantic") ||
		invalidTagState.Repository != "" || invalidTagFinding.ID != "" || invalidTagJob.ID != "" {
		t.Fatalf(
			"non-semantic tag result = %#v / %#v / %#v, error = %v",
			invalidTagState,
			invalidTagFinding,
			invalidTagJob,
			invalidTagErr,
		)
	}
	clock = clock.Add(time.Minute)
	state, resolved, completedJob, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: running.ID, Outcome: RepositoryValidationConfirmed,
		SelectedCommitSHA: fixCommit, FixCommitTime: clock.Add(-time.Hour),
		FirstContainingTag: "v1.2.3", Summary: "The supplied diff restores the invariant.",
	})
	if err != nil || completedJob.State != RepositoryValidationConfirmed ||
		resolved.Lifecycle != RepositoryFindingResolved || resolved.FixCommitSHA != fixCommit ||
		len(resolved.ResolutionHistory) != 1 {
		t.Fatalf("resolved=%#v job=%#v err=%v", resolved, completedJob, err)
	}

	clock = clock.Add(time.Minute)
	state, refreshed, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
		RepositoryFindingID: aggregate.ID, ExpectedVersion: resolved.Version,
		ExternalID: "17", URL: "https://github.com/owner/repo/issues/17",
		Origin: IssueDraftOriginLinked, State: RepositoryFindingIssueClosed, Title: "Tracked defect",
	})
	if err != nil || refreshed.Lifecycle != RepositoryFindingResolved ||
		!RepositoryFindingIssueSnapshotFresh(refreshed, clock) ||
		RepositoryFindingIssueSnapshotFresh(refreshed, clock.Add(RepositoryIssueSnapshotTTL)) {
		t.Fatalf("closed snapshot=%#v err=%v", refreshed, err)
	}
	clock = clock.Add(RepositoryIssueSnapshotTTL)
	_, reopened, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
		RepositoryFindingID: aggregate.ID, ExpectedVersion: refreshed.Version,
		ExternalID: "17", URL: "https://github.com/owner/repo/issues/17",
		Origin: IssueDraftOriginLinked, State: RepositoryFindingIssueOpen, Title: "Tracked defect",
	})
	if err != nil || reopened.Lifecycle != RepositoryFindingOpen {
		t.Fatalf("reopened snapshot=%#v err=%v", reopened, err)
	}

	clock = clock.Add(time.Minute)
	newState, newOccurrence := recordLifecycleFinding(
		t, store, strings.Repeat("5", 40), strings.Repeat("6", 40), "regression-run",
		"main", "main", true, "resolvable defect returns",
	)
	newJob := lifecycleJobForFinding(t, newState, newOccurrence.ID)
	newJob = claimLifecycleMappingJob(t, store, newState.Repository, newJob, RepositoryMappingModelSnapshot{})
	_, regressed, err := store.CompleteMappingJob(newState.Repository, RepositoryMappingCompletion{
		JobID: newJob.ID, RepositoryFindingID: aggregate.ID,
	})
	// Reopening intentionally returned lifecycle to open, so restore a confirmed
	// resolution projection to exercise the post-resolution occurrence rule.
	if err != nil || regressed.Lifecycle == RepositoryFindingRegressed {
		// The actual regression transition is covered below on an independently
		// resolved aggregate; this assertion documents reopening precedence.
		t.Fatalf("reopened association=%#v err=%v", regressed, err)
	}
}

func TestValidationRestartAndPostResolutionOccurrenceRegresses(t *testing.T) {
	store := NewStore(t.TempDir())
	clock := time.Date(2026, 8, 26, 15, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	state, finding := recordLifecycleFinding(
		t, store, strings.Repeat("7", 40), strings.Repeat("8", 40), "before-fix",
		"main", "main", true, "regression target",
	)
	job := lifecycleJobForFinding(t, state, finding.ID)
	job = claimLifecycleMappingJob(t, store, state.Repository, job, RepositoryMappingModelSnapshot{})
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, validationJobs, err := store.ReserveValidationJobs(
		state.Repository,
		[]string{aggregate.ID},
		RepositoryMappingModelSnapshot{},
	)
	if err != nil || len(validationJobs) != 1 {
		t.Fatalf("validation reservation=%#v err=%v", validationJobs, err)
	}
	state, running, validationFinding, validationClaimed, validationClaimErr := store.ClaimValidationJob(
		state.Repository,
		validationJobs[0].ID,
	)
	if validationClaimErr != nil || !validationClaimed || validationFinding.ID != aggregate.ID {
		t.Fatalf(
			"validation claim=%v finding=%#v err=%v",
			validationClaimed,
			validationFinding,
			validationClaimErr,
		)
	}
	if _, reconcileErr := store.ReconcileJobs(context.Background()); reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	state, _, _ = store.Get(state.Repository)
	running = lifecycleValidationJobByID(t, state, running.ID)
	if running.State != RepositoryValidationPending || running.ReservedAt != (time.Time{}) {
		t.Fatalf("reconciled validation job=%#v", running)
	}
	_, running, _, claimed, err := store.ClaimValidationJob(state.Repository, running.ID)
	if err != nil || !claimed {
		t.Fatalf("reclaim=%v err=%v", claimed, err)
	}
	fix := strings.Repeat("9", 40)
	_, running, _ = store.SetValidationJobCandidates(state.Repository, running.ID, []string{fix})
	clock = clock.Add(time.Minute)
	_, resolved, _, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: running.ID, Outcome: RepositoryValidationConfirmed,
		SelectedCommitSHA: fix, FixCommitTime: clock.Add(-time.Hour), Summary: "confirmed",
	})
	if err != nil || resolved.Lifecycle != RepositoryFindingResolved {
		t.Fatalf("resolution=%#v err=%v", resolved, err)
	}
	clock = clock.Add(time.Minute)
	state, later := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("c", 40), "after-fix",
		"main", "main", true, "regression target returns",
	)
	laterJob := lifecycleJobForFinding(t, state, later.ID)
	laterJob = claimLifecycleMappingJob(t, store, state.Repository, laterJob, RepositoryMappingModelSnapshot{})
	_, regressed, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: laterJob.ID, RepositoryFindingID: aggregate.ID,
		DefaultBranchVerified: true, RegressionVerified: true, RegressionFixCommit: fix,
		RegressionFindingID: aggregate.ID,
	})
	if err != nil || regressed.Lifecycle != RepositoryFindingRegressed ||
		regressed.ValidationState != RepositoryValidationNotRequested ||
		len(regressed.ResolutionHistory) != 1 {
		t.Fatalf("regression=%#v err=%v", regressed, err)
	}
}

func TestIssueDraftStateProjectsOntoRepositoryFindingWithoutChangingPublicationFences(t *testing.T) {
	store := NewStore(t.TempDir())
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("d", 40), strings.Repeat("4", 40), "issue-projection",
		"main", "main", true, "issue projection defect",
	)
	job := lifecycleJobForFinding(t, state, occurrence.ID)
	job = claimLifecycleMappingJob(t, store, state.Repository, job, RepositoryMappingModelSnapshot{})
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, draft, reserved, err := store.ReserveIssueGeneration(IssueGenerationRequest{
		Repository: state.Repository, FindingID: occurrence.ID, GenerationID: "rrig_projection_one",
		ResolvedInstructions: "Present the diagnosis.", InstructionsMode: IssueDraftInstructionsDefault,
		GeneratorModel: "writer", GeneratorAccount: "account",
	})
	if err != nil || !reserved {
		t.Fatalf("reserve=%v draft=%#v err=%v", reserved, draft, err)
	}
	projected := state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
	if projected.Issue.State != RepositoryFindingIssueDraft {
		t.Fatalf("generating projection=%#v", projected.Issue)
	}
	state, draft, err = store.CompleteIssueGeneration(
		state.Repository, draft.ID, draft.GenerationID, "Generated issue", "Grounded body", []string{"bug"}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.DeleteIssueDraft(state.Repository, draft.ID, draft.Version)
	if err != nil {
		t.Fatal(err)
	}
	projected = state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
	if projected.Issue.State != RepositoryFindingIssueNone {
		t.Fatalf("deleted projection=%#v", projected.Issue)
	}
	state, draft, reserved, err = store.ReserveIssueGeneration(IssueGenerationRequest{
		Repository: state.Repository, FindingID: occurrence.ID, GenerationID: "rrig_projection_two",
		ResolvedInstructions: "Present the diagnosis.", InstructionsMode: IssueDraftInstructionsDefault,
		GeneratorModel: "writer", GeneratorAccount: "account",
	})
	if err != nil || !reserved {
		t.Fatalf("second reserve=%v err=%v", reserved, err)
	}
	state, draft, err = store.CompleteIssueGeneration(
		state.Repository, draft.ID, draft.GenerationID, "Generated issue", "Grounded body", []string{"bug"}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	state, draft, claimed, err := store.ClaimIssueDraftPublication(state.Repository, draft.ID, draft.Version)
	if err != nil || !claimed {
		t.Fatalf("publication claim=%v draft=%#v err=%v", claimed, draft, err)
	}
	projected = state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
	if projected.Issue.State != RepositoryFindingIssueUnknown || projected.Issue.URL != "" {
		t.Fatalf("publishing projection=%#v", projected.Issue)
	}
	state, _, err = store.SetIssueDraftPublication(
		state.Repository, draft.ID, draft.Version, IssueDraftPosted,
		"31", "https://github.com/owner/repo/issues/31",
	)
	if err != nil {
		t.Fatal(err)
	}
	projected = state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
	if projected.Issue.State != RepositoryFindingIssueOpen ||
		projected.Issue.URL != "https://github.com/owner/repo/issues/31" {
		t.Fatalf("posted projection=%#v", projected.Issue)
	}
}

func TestClosedExistingIssueLinkMovesAggregateToResolutionPending(t *testing.T) {
	store := NewStore(t.TempDir())
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("e", 40), strings.Repeat("5", 40), "closed-issue-link",
		"main", "main", true, "closed issue defect",
	)
	job := lifecycleJobForFinding(t, state, occurrence.ID)
	_, job, _, claimed, err := store.ClaimMappingJob(
		state.Repository, job.ID, RepositoryMappingModelSnapshot{},
	)
	if err != nil || !claimed {
		t.Fatalf("mapping claim=%v err=%v", claimed, err)
	}
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	occurrence = state.Findings[findingIndexByID(state.Findings, occurrence.ID)]
	state, _, err = store.LinkExistingIssue(ExistingIssueLink{
		Repository: state.Repository, FindingID: occurrence.ID,
		ExpectedFindingVersion: occurrence.Version,
		ExternalID:             "42", ExternalURL: "https://github.com/owner/repo/issues/42",
		State: "closed", Title: "Already closed", Origin: IssueDraftOriginLinked,
		Confirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	projected := state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
	if projected.Issue.State != RepositoryFindingIssueClosed ||
		projected.Lifecycle != RepositoryFindingResolutionPending {
		t.Fatalf("closed issue projection=%#v lifecycle=%q", projected.Issue, projected.Lifecycle)
	}
}

func TestMappingCreationRequeuesWhenCandidateUniverseChanges(t *testing.T) {
	store := NewStore(t.TempDir())
	_, first := recordLifecycleFinding(
		t, store, strings.Repeat("1", 40), strings.Repeat("a", 40), "universe-first",
		"main", "main", true, "first universe defect",
	)
	state, second := recordLifecycleFinding(
		t, store, strings.Repeat("2", 40), strings.Repeat("b", 40), "universe-second",
		"main", "main", true, "second universe defect", MatchHints{
			Component: "scheduler", Operation: "discard canceled waiter",
			FailureMode: "canceled waiter remains queued", Trigger: "queue rotation after cancellation",
			ViolatedInvariant: "canceled waiters leave the queue", ObservableOutcome: "queue capacity is exhausted",
			RelatedSymbols: []string{"Scheduler.Run"}, SourceAnchors: []string{"canceled"},
			DistinguishingFacts: []string{"requires cancellation"},
		},
	)
	firstJob := lifecycleJobForFinding(t, state, first.ID)
	secondJob := lifecycleJobForFinding(t, state, second.ID)
	_, firstJob, _, firstClaimed, err := store.ClaimMappingJob(
		state.Repository, firstJob.ID, RepositoryMappingModelSnapshot{},
	)
	if err != nil || !firstClaimed {
		t.Fatalf("first claim=%v err=%v", firstClaimed, err)
	}
	_, secondJob, _, secondClaimed, err := store.ClaimMappingJob(
		state.Repository, secondJob.ID, RepositoryMappingModelSnapshot{},
	)
	if err != nil || !secondClaimed {
		t.Fatalf("second claim=%v err=%v", secondClaimed, err)
	}
	emptyUniverse := repositoryMatchingUniverseFingerprint(nil)
	state, _, err = store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: firstJob.ID, CreateMatchState: RepositoryMatchNew,
		DefaultBranchVerified: true, ExpectedUniverse: emptyUniverse,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err = store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: secondJob.ID, CreateMatchState: RepositoryMatchNew,
		DefaultBranchVerified: true, ExpectedUniverse: emptyUniverse,
	})
	if !errors.Is(err, errRepositoryMappingUniverseChanged) || len(state.RepositoryFindings) != 1 {
		t.Fatalf("universe completion findings=%d err=%v", len(state.RepositoryFindings), err)
	}
	job := lifecycleJobForFinding(t, state, second.ID)
	occurrence := state.Findings[findingIndexByID(state.Findings, second.ID)]
	if job.State != RepositoryMappingPending || occurrence.RepositoryFindingID != "" {
		t.Fatalf("requeued job=%#v occurrence=%#v", job, occurrence)
	}
}

func TestCanonicalRepositoryIssueAssociationMergeBoundaries(t *testing.T) {
	now := repositoryAuditTestNow
	open := RepositoryFindingIssueAssociation{
		ExternalID: "1", URL: "https://github.com/owner/repo/issues/1",
		Origin: IssueDraftOriginLinked, State: RepositoryFindingIssueOpen,
		Title: "open", SnapshotAt: now,
	}
	closed := open
	closed.State = RepositoryFindingIssueClosed
	closed.Title = "closed"
	closed.SnapshotAt = now.Add(time.Minute)
	if merged := mergeRepositoryIssueAssociations(
		RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone}, open,
	); merged.URL != open.URL {
		t.Fatalf("empty-left merge=%#v", merged)
	}
	if merged := mergeRepositoryIssueAssociations(open, RepositoryFindingIssueAssociation{}); merged.URL != open.URL {
		t.Fatalf("empty-right merge=%#v", merged)
	}
	if merged := mergeRepositoryIssueAssociations(open, closed); merged.State != RepositoryFindingIssueClosed {
		t.Fatalf("newer snapshot merge=%#v", merged)
	}
	if merged := mergeRepositoryIssueAssociations(closed, open); merged.State != RepositoryFindingIssueClosed {
		t.Fatalf("older snapshot merge=%#v", merged)
	}
	withoutURL := open
	withoutURL.URL = ""
	if merged := mergeRepositoryIssueAssociations(withoutURL, open); merged.URL != open.URL {
		t.Fatalf("missing-left URL merge=%#v", merged)
	}
	if merged := mergeRepositoryIssueAssociations(open, withoutURL); merged.URL != open.URL {
		t.Fatalf("missing-right URL merge=%#v", merged)
	}
	other := open
	other.ExternalID = "2"
	other.URL = "https://github.com/owner/repo/issues/2"
	other.ConflictURLs = []string{"https://github.com/owner/repo/issues/3"}
	conflict := mergeRepositoryIssueAssociations(open, other)
	if !conflict.Conflict || len(conflict.ConflictURLs) != 3 {
		t.Fatalf("conflicting association merge=%#v", conflict)
	}

	target := RepositoryFinding{Issue: RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone}}
	state := RepositoryState{IssueDrafts: []IssueDraft{{
		ID: "draft", Origin: IssueDraftOriginDiscovered, State: IssueDraftPosted,
		ExternalID: "2", ExternalURL: other.URL, ExternalState: "closed",
		Title: "discovered", UpdatedAt: now,
	}}}
	mergeOccurrenceIssueAssociation(nil, &target, Finding{IssueDraftID: "draft"})
	mergeOccurrenceIssueAssociation(&state, nil, Finding{IssueDraftID: "draft"})
	mergeOccurrenceIssueAssociation(&state, &target, Finding{})
	mergeOccurrenceIssueAssociation(&state, &target, Finding{IssueDraftID: "missing"})
	mergeOccurrenceIssueAssociation(&state, &target, Finding{IssueDraftID: "draft"})
	if target.Issue.URL != other.URL || target.Issue.State != RepositoryFindingIssueClosed {
		t.Fatalf("occurrence issue projection=%#v", target.Issue)
	}
	for _, test := range []struct {
		state    IssueDraftState
		external string
		want     RepositoryFindingIssueState
	}{
		{IssueDraftPosted, "open", RepositoryFindingIssueOpen},
		{IssueDraftPosted, "closed", RepositoryFindingIssueClosed},
		{IssueDraftPublishing, "", RepositoryFindingIssueUnknown},
		{IssueDraftUnknown, "", RepositoryFindingIssueUnknown},
		{IssueDraftEditing, "", RepositoryFindingIssueDraft},
	} {
		projected := repositoryIssueAssociationFromDraft(IssueDraft{
			State: test.state, ExternalState: test.external,
		})
		if projected.State != test.want {
			t.Fatalf("draft state %q projected as %q, want %q", test.state, projected.State, test.want)
		}
	}
}

func recordLifecycleFinding(
	t *testing.T,
	store Store,
	commit string,
	blob string,
	runID string,
	targetBranch string,
	defaultBranch string,
	targetIsDefault bool,
	title string,
	matchHintsOverride ...MatchHints,
) (RepositoryState, Finding) {
	t.Helper()
	file := FileRef{Path: "service.go", BlobSHA: blob, SizeBytes: 20, Category: "code", Mode: "100644"}
	current, _, err := store.Get("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	campaignID := NewRepositoryReviewCampaignID()
	expectedCampaignID := ""
	if current.CurrentCampaign != nil {
		expectedCampaignID = current.CurrentCampaign.ID
	}
	profileHash := stableID("sha256:", runID)
	snapshot := RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "reviewer", DeduplicationModel: "reviewer",
		SimilarityThreshold: DeduplicationDefaultThreshold, CandidateLimit: 0,
	}
	if _, err = store.BeginCampaign(context.Background(), BeginCampaignRequest{
		Repository: "owner/repo", CampaignID: campaignID, ExpectedCampaignID: expectedCampaignID,
		CommitSHA: commit, ExpectedReviewVersion: current.ReviewVersion, Exact: true,
		DeduplicationSnapshot: &snapshot,
	}); err != nil {
		t.Fatal(err)
	}
	assignment, err := NewRepositoryReviewAssignment(
		RepositoryReviewFocusCorrectnessState, "reviewer", "test-v1", profileHash, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanAssignmentsForCampaign(
		context.Background(), "owner/repo", commit, "inventory-"+runID, profileHash,
		campaignID, []RepositoryReviewAssignment{assignment}, []FileRef{file}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = BindPlanBranch(plan, targetBranch, defaultBranch, targetIsDefault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.BeginRepositoryReviewRun(context.Background(), BeginRepositoryReviewRunRequest{
		Plan: plan, RunID: runID, ReviewableFiles: []FileRef{file},
	}); err != nil {
		t.Fatal(err)
	}
	matchHints := MatchHints{
		Component: "scheduler", Operation: "requeue waiter", FailureMode: "stale owner",
		Trigger: "failed wake", ViolatedInvariant: "waiters use current owner",
		ObservableOutcome: "waiter remains blocked",
		RelatedSymbols:    []string{"Scheduler.Run"}, SourceAnchors: []string{"waiters"},
		DistinguishingFacts: []string{"requires failed wake"},
	}
	if len(matchHintsOverride) > 0 {
		matchHints = matchHintsOverride[0]
	}
	observation := Observation{
		Model: "provider/reviewer", ModelAlias: "reviewer", Account: "account",
		Reviewer: RepositoryReviewFocusCorrectnessState, ScopeFiles: []FileRef{file},
		RawDigest: "sha256:" + strings.Repeat("a", 64),
		Findings: []FindingCandidate{{
			Severity: "high",
			Title:    title,
			Symbol:   "Scheduler.Run",
			File:     file.Path,
			Message:  "The waiter remains attached to the old queue.",
			Evidence: "The failed wake path uses the stale queue owner.",
			Impact:   "The waiter remains blocked.",
			Validation: Validation{
				Status:  "confirmed",
				Summary: "Traced the stale owner.",
				Checks:  []string{"followed wake path"},
			},
			MatchHints: matchHints,
			FixEffort: FixEffort{
				Quick: FixEffortEstimate{
					LOCMin:    5,
					LOCMax:    20,
					Class:     "small",
					Rationale: "Localized containment.",
				},
				Quality: FixEffortEstimate{
					LOCMin:    30,
					LOCMax:    100,
					Class:     "medium",
					Rationale: "Ownership spans related units.",
				},
			},
		}},
	}
	checkpoint, err := store.CheckpointRepositoryReviewAssignment(
		context.Background(), CheckpointRepositoryReviewAssignmentRequest{
			Plan: plan, RunID: runID, AssignmentID: assignment.ID,
			AutomationID: "rra_lifecycle", AgentID: "main", ChildIndex: 1,
			Digest: "sha256:" + strings.Repeat("b", 64), AcknowledgedFiles: []FileRef{file},
			Observation: observation,
		},
	)
	if err != nil || len(checkpoint.AcceptedFindingIDs) != 1 {
		t.Fatal(err)
	}
	if _, err = store.FinalizeRepositoryReviewRun(
		context.Background(),
		FinalizeRepositoryReviewRunRequest{Plan: plan, RunID: runID},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ProcessPendingDeduplicationJobs(
		context.Background(), "owner/repo", DeduplicationProcessOptions{},
	); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinalizeRepositoryReviewRun(
		context.Background(), FinalizeRepositoryReviewRunRequest{Plan: plan, RunID: runID},
	); err != nil {
		t.Fatal(err)
	}
	result, _, err := store.Get("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	rawIndex := rawFindingIndexByID(result.RawFindings, checkpoint.AcceptedFindingIDs[0])
	if rawIndex < 0 {
		t.Fatal("recorded raw finding missing")
	}
	index := findingIndexByID(result.Findings, result.RawFindings[rawIndex].DeduplicatedFindingID)
	if index < 0 {
		t.Fatal("recorded finding missing")
	}
	return result, result.Findings[index]
}

func lifecycleJobForFinding(t *testing.T, state RepositoryState, findingID string) RepositoryMappingJob {
	t.Helper()
	for _, job := range state.MappingJobs {
		if job.ReviewFindingID == findingID {
			return job
		}
	}
	t.Fatalf("mapping job for %s missing: %#v", findingID, state.MappingJobs)
	return RepositoryMappingJob{}
}

func claimLifecycleMappingJob(
	t *testing.T,
	store Store,
	repository string,
	pendingJob RepositoryMappingJob,
	snapshot RepositoryMappingModelSnapshot,
) RepositoryMappingJob {
	t.Helper()
	state, claimedJob, finding, claimed, err := store.ClaimMappingJob(repository, pendingJob.ID, snapshot)
	if err != nil || !claimed || state.Repository != repository || claimedJob.ID != pendingJob.ID ||
		finding.ID != pendingJob.ReviewFindingID {
		t.Fatalf(
			"mapping claim=%v state=%#v job=%#v finding=%#v err=%v",
			claimed,
			state,
			claimedJob,
			finding,
			err,
		)
	}
	return claimedJob
}

func lifecycleValidationJobByID(t *testing.T, state RepositoryState, id string) RepositoryValidationJob {
	t.Helper()
	for _, job := range state.ValidationJobs {
		if job.ID == id {
			return job
		}
	}
	t.Fatalf("validation job %s missing", id)
	return RepositoryValidationJob{}
}

func TestLifecycleValidationBatchBoundaries(t *testing.T) {
	store := NewStore(filepath.Clean(t.TempDir()))
	if _, _, err := store.ReserveValidationJobs("owner/repo", nil, RepositoryMappingModelSnapshot{}); err == nil {
		t.Fatal("empty validation batch accepted")
	}
	if _, _, err := store.ReserveValidationJobs(
		"owner/repo", make([]string, maxValidationBatch+1), RepositoryMappingModelSnapshot{},
	); err == nil {
		t.Fatal("oversized validation batch accepted")
	}
	invalidState, invalidJob, invalidFinding, claimed, claimErr := store.ClaimMappingJob(
		"owner/repo", "missing", RepositoryMappingModelSnapshot{ProfileVersion: 1},
	)
	if claimErr == nil || claimed || invalidState.Repository != "" || invalidJob.ID != "" || invalidFinding.ID != "" {
		t.Fatalf(
			"invalid model snapshot result=%#v / %#v / %#v / %v, error=%v",
			invalidState,
			invalidJob,
			invalidFinding,
			claimed,
			claimErr,
		)
	}
	if _, _, err := store.SaveMappingAdjudication(
		"owner/repo", "missing", RepositoryMappingAdjudication{Decision: "same", Confidence: math.NaN()},
	); err == nil {
		t.Fatal("invalid adjudication accepted")
	}
}

func TestValidationSlotsAreWorkspaceWideAndBoundedToFour(t *testing.T) {
	store := NewStore(t.TempDir())
	releases := make([]func(), 0, RepositoryValidationConcurrency)
	for index := 0; index < RepositoryValidationConcurrency; index++ {
		release, err := store.AcquireValidationSlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	blocked, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.AcquireValidationSlot(blocked); !errors.Is(err, context.Canceled) {
		t.Fatalf("fifth validation slot error=%v", err)
	}
	for _, release := range releases {
		release()
	}
	release, err := store.AcquireValidationSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}
