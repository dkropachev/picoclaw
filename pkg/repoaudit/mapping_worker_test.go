package repoaudit

import (
	"context"
	"errors"
	"testing"
)

func TestMappingWorkerAIOutcomesAndOpaqueCandidates(t *testing.T) {
	for _, test := range []struct {
		name        string
		decision    string
		confidence  float64
		wantCount   int
		wantState   RepositoryMatchState
		wantRelated bool
	}{
		{name: "same", decision: "same", confidence: .95, wantCount: 1, wantState: RepositoryMatchKnown},
		{name: "related", decision: "related", confidence: .93, wantCount: 2, wantState: RepositoryMatchNew, wantRelated: true},
		{name: "uncertain", decision: "uncertain", confidence: .72, wantCount: 2, wantState: RepositoryMatchProvisional},
		{name: "distinct", decision: "distinct", confidence: .98, wantCount: 2, wantState: RepositoryMatchNew},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newRepositoryAuditTestStore(t)
			first := recordMappingWorkerFinding(t, store, "run-one", "commit-one", "old/wait.go", "awaiter.signal")
			if _, err := store.ProcessPendingMappingJobs(t.Context(), first.Repository, RepositoryMappingProcessOptions{
				DefaultBranchVerified: func(context.Context, Finding) (bool, error) { return true, nil },
			}); err != nil {
				t.Fatal(err)
			}
			second := recordMappingWorkerFinding(
				t,
				store,
				"run-two",
				"commit-two",
				"new/predicate.go",
				"predicate.wake",
			)
			called := false
			_, err := store.ProcessPendingMappingJobs(t.Context(), second.Repository, RepositoryMappingProcessOptions{
				DefaultBranchVerified: func(context.Context, Finding) (bool, error) { return true, nil },
				Adjudicate: func(
					_ context.Context,
					_ RepositoryMappingModelSnapshot,
					request RepositoryMappingAIRequest,
				) (RepositoryMappingAdjudication, error) {
					called = true
					if len(request.Candidates) != 1 || request.Candidates[0].ID != "candidate_1" ||
						request.Candidates[0].Finding.ID != "" ||
						request.Candidates[0].Finding.ReviewFindingIDs != nil {
						t.Fatalf("non-opaque AI candidates = %#v", request.Candidates)
					}
					candidateID := ""
					if test.decision != "distinct" {
						candidateID = request.Candidates[0].ID
					}
					return RepositoryMappingAdjudication{
						Decision: test.decision, CandidateID: candidateID,
						Confidence: test.confidence, MatchingAnchors: []string{"waiters"},
						Explanation: "bounded causal comparison",
					}, nil
				},
			})
			if err != nil || !called {
				t.Fatalf("processor called=%v err=%v", called, err)
			}
			state, found, err := store.Get(second.Repository)
			if err != nil || !found || len(state.RepositoryFindings) != test.wantCount {
				t.Fatalf("state=%#v found=%v err=%v", state.RepositoryFindings, found, err)
			}
			mapped := state.RepositoryFindings[len(state.RepositoryFindings)-1]
			if test.wantCount == 1 {
				mapped = state.RepositoryFindings[0]
			}
			if mapped.MatchState != test.wantState {
				t.Fatalf("match state = %q, want %q", mapped.MatchState, test.wantState)
			}
			if test.wantRelated && (len(mapped.PossibleDuplicates) != 1 ||
				mapped.PossibleDuplicates[0].Relation != "related") {
				t.Fatalf("related candidates = %#v", mapped.PossibleDuplicates)
			}
		})
	}
}

func TestMappingWorkerReleasesFailedAIAndContinuesLaterJobs(t *testing.T) {
	for _, test := range []struct {
		name  string
		first func(RepositoryMappingAIRequest) (RepositoryMappingAdjudication, error)
	}{
		{
			name: "malformed output",
			first: func(RepositoryMappingAIRequest) (RepositoryMappingAdjudication, error) {
				return RepositoryMappingAdjudication{Decision: "merge", Confidence: .9}, nil
			},
		},
		{
			name: "timeout",
			first: func(RepositoryMappingAIRequest) (RepositoryMappingAdjudication, error) {
				return RepositoryMappingAdjudication{}, context.DeadlineExceeded
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newRepositoryAuditTestStore(t)
			first := recordMappingWorkerFinding(t, store, "base-run", "base-commit", "old/wait.go", "awaiter.signal")
			if _, err := store.ProcessPendingMappingJobs(t.Context(), first.Repository, RepositoryMappingProcessOptions{
				DefaultBranchVerified: func(context.Context, Finding) (bool, error) { return true, nil },
			}); err != nil {
				t.Fatal(err)
			}
			second := recordMappingWorkerFinding(
				t,
				store,
				"failed-run",
				"failed-commit",
				"new/predicate.go",
				"predicate.wake",
			)
			third := recordMappingWorkerFinding(
				t,
				store,
				"later-run",
				"later-commit",
				"moved/predicate.go",
				"predicate.resume",
			)
			calls := 0
			result, err := store.ProcessPendingMappingJobs(
				t.Context(),
				second.Repository,
				RepositoryMappingProcessOptions{
					DefaultBranchVerified: func(context.Context, Finding) (bool, error) { return true, nil },
					Adjudicate: func(
						_ context.Context,
						_ RepositoryMappingModelSnapshot,
						request RepositoryMappingAIRequest,
					) (RepositoryMappingAdjudication, error) {
						calls++
						if calls == 1 {
							return test.first(request)
						}
						return RepositoryMappingAdjudication{
							Decision: "distinct", Confidence: .96,
							Explanation: "bounded distinct causal path",
						}, nil
					},
				},
			)
			if err == nil || result.PendingAI != 1 || result.Completed != 1 || calls != 2 {
				t.Fatalf("mapping result=%#v calls=%d err=%v", result, calls, err)
			}
			state, found, getErr := store.Get(third.Repository)
			if getErr != nil || !found || len(state.RepositoryFindings) != 2 {
				t.Fatalf("repository findings=%#v found=%v err=%v", state.RepositoryFindings, found, getErr)
			}
			failedJob := lifecycleJobForFinding(t, state, second.Findings[len(second.Findings)-1].ID)
			if failedJob.State != RepositoryMappingPending || !failedJob.ReservedAt.IsZero() ||
				failedJob.Error == "" {
				t.Fatalf("failed mapping job=%#v", failedJob)
			}
		})
	}
}

func TestRunFindingStatusAttemptsAreBoundedAndExplicitlyRetryable(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	base := recordMappingWorkerFinding(
		t, store, "base-run", "base-commit", "old/wait.go", "awaiter.signal",
	)
	if _, err := store.ProcessPendingMappingJobs(
		t.Context(),
		base.Repository,
		RepositoryMappingProcessOptions{
			DefaultBranchVerified: func(context.Context, Finding) (bool, error) { return true, nil },
		},
	); err != nil {
		t.Fatal(err)
	}
	pending := recordMappingWorkerFinding(
		t, store, "retry-run", "retry-commit", "new/predicate.go", "predicate.wake",
	)
	findingID := pending.Findings[len(pending.Findings)-1].ID
	if _, _, err := store.RetryRunFindingStatus(
		pending.Repository,
		[]string{findingID},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("uncapped pending retry error=%v", err)
	}
	calls := 0
	failedOptions := RepositoryMappingProcessOptions{
		DefaultBranchVerified: func(context.Context, Finding) (bool, error) { return true, nil },
		Adjudicate: func(
			context.Context,
			RepositoryMappingModelSnapshot,
			RepositoryMappingAIRequest,
		) (RepositoryMappingAdjudication, error) {
			calls++
			return RepositoryMappingAdjudication{}, context.DeadlineExceeded
		},
	}
	for attempt := 0; attempt < RepositoryRunFindingStatusAttemptLimit; attempt++ {
		if _, err := store.ProcessPendingMappingJobs(
			t.Context(), pending.Repository, failedOptions,
		); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("attempt %d error=%v", attempt+1, err)
		}
	}
	if calls != RepositoryRunFindingStatusAttemptLimit {
		t.Fatalf("automatic calls=%d", calls)
	}
	if result, err := store.ProcessPendingMappingJobs(
		t.Context(), pending.Repository, failedOptions,
	); err != nil || result != (RepositoryMappingProcessResult{}) ||
		calls != RepositoryRunFindingStatusAttemptLimit {
		t.Fatalf("capped result=%#v calls=%d err=%v", result, calls, err)
	}
	failed, found, err := store.Get(pending.Repository)
	if err != nil || !found {
		t.Fatalf("failed state found=%v err=%v", found, err)
	}
	job := lifecycleJobForFinding(t, failed, findingID)
	if job.State != RepositoryMappingPending ||
		job.Attempts != RepositoryRunFindingStatusAttemptLimit || job.Error == "" {
		t.Fatalf("capped job=%#v", job)
	}
	if _, cappedJob, _, claimed, err := store.ClaimMappingJob(
		pending.Repository,
		job.ID,
		RepositoryMappingModelSnapshot{},
	); err != nil || claimed || cappedJob.Attempts != RepositoryRunFindingStatusAttemptLimit {
		t.Fatalf("atomic cap job=%#v claimed=%v err=%v", cappedJob, claimed, err)
	}
	if _, _, err := store.RetryRunFindingStatus(
		pending.Repository,
		[]string{findingID, findingID},
	); err == nil {
		t.Fatal("duplicate retry selection succeeded")
	}
	unchanged, _, _ := store.Get(pending.Repository)
	if unchangedJob := lifecycleJobForFinding(
		t,
		unchanged,
		findingID,
	); unchangedJob.Attempts != RepositoryRunFindingStatusAttemptLimit ||
		unchangedJob.Error == "" {
		t.Fatalf("invalid retry mutated job=%#v", unchangedJob)
	}

	retried, selected, err := store.RetryRunFindingStatus(
		pending.Repository,
		[]string{findingID},
	)
	if err != nil || len(selected) != 1 || selected[0].ID != findingID {
		t.Fatalf("retry selected=%#v err=%v", selected, err)
	}
	job = lifecycleJobForFinding(t, retried, findingID)
	if job.State != RepositoryMappingPending || job.Attempts != 0 || job.Error != "" ||
		!mappingAdjudicationEmpty(job.Adjudication) || job.CandidateUniverse != "" {
		t.Fatalf("reset job=%#v", job)
	}
	result, err := store.ProcessPendingMappingJobs(
		t.Context(),
		pending.Repository,
		RepositoryMappingProcessOptions{
			DefaultBranchVerified: func(context.Context, Finding) (bool, error) { return true, nil },
			Adjudicate: func(
				context.Context,
				RepositoryMappingModelSnapshot,
				RepositoryMappingAIRequest,
			) (RepositoryMappingAdjudication, error) {
				return RepositoryMappingAdjudication{
					Decision: "distinct", Confidence: .99,
					Explanation: "The causal paths are distinct.",
				}, nil
			},
		},
	)
	if err != nil || result.Completed != 1 || result.Created != 1 {
		t.Fatalf("retried result=%#v err=%v", result, err)
	}
	completed, _, _ := store.Get(pending.Repository)
	if finding := completed.Findings[findingIndexByID(completed.Findings, findingID)]; finding.RepositoryFindingID == "" {
		t.Fatalf("retried finding=%#v", finding)
	}
	if _, _, err := store.RetryRunFindingStatus(
		pending.Repository,
		[]string{findingID},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("associated retry error=%v", err)
	}
}

func recordMappingWorkerFinding(
	t *testing.T,
	store Store,
	runID, commit, pathValue, symbol string,
) RepositoryState {
	t.Helper()
	file := repositoryAuditTestFile(pathValue, string(rune('a'+len(runID)%5)), 20)
	plan, err := store.Plan(t.Context(), "owner/repo", commit, "inventory-"+runID, []FileRef{file}, true)
	if err != nil {
		t.Fatal(err)
	}
	candidate := FindingCandidate{
		Severity: "high", Title: "Predicate waiter remains attached after owner move",
		Symbol: symbol, File: pathValue,
		Message:    "A false predicate leaves the waiter on the moved-from owner.",
		Evidence:   "The wake path requeues through the stale owner field.",
		Impact:     "The coroutine remains blocked indefinitely.",
		Validation: Validation{Status: "confirmed", Summary: "Traced the false-predicate wake path."},
		MatchHints: MatchHints{
			Component: "core scheduling", Operation: "requeue predicate waiter after owner move",
			FailureMode:         "waiter remains attached to the moved from owner",
			Trigger:             "move followed by an unsuccessful predicate wakeup",
			ViolatedInvariant:   "every waiter requeues on its current owner",
			ObservableOutcome:   "coroutine remains blocked indefinitely",
			RelatedSymbols:      []string{"condition.awaiter", "predicate.signal"},
			SourceAnchors:       []string{"owner", "waiters", "add_waiter"},
			DistinguishingFacts: []string{"requires an owner move", "predicate remains false"},
		},
		FixEffort: FixEffort{
			Quick: FixEffortEstimate{LOCMin: 5, LOCMax: 20, Class: "small", Rationale: "Localized containment."},
			Quality: FixEffortEstimate{
				LOCMin:    30,
				LOCMax:    100,
				Class:     "medium",
				Rationale: "Ownership spans related units.",
			},
		},
	}
	result, err := store.Record(t.Context(), RecordRequest{
		Plan:  plan,
		RunID: runID,
		Observations: []Observation{
			{Model: "reviewer", ScopeFiles: []FileRef{file}, Findings: []FindingCandidate{candidate}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.State
}
