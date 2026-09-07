package repoaudit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func dedupDeepPendingFixture(t *testing.T, fileCount int) assignmentCoverageFixture {
	t.Helper()
	fixture := newAssignmentCoverageFixture(t, fileCount, fileCount)
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "deep-dedup-run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	checkpoint := assignmentCoverageCheckpoint(fixture, "deep-dedup-run", 0, fixture.files)
	for index, file := range fixture.files {
		checkpoint.Observation.Findings = append(
			checkpoint.Observation.Findings,
			repositoryReviewCampaignFinding(file, "deep finding "+string(rune('a'+index))),
		)
	}
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), checkpoint); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func dedupDeepState(t *testing.T, fixture assignmentCoverageFixture) RepositoryState {
	t.Helper()
	state, found, err := fixture.store.Get(fixture.repository)
	if err != nil || !found {
		t.Fatalf("dedup state found=%v err=%v", found, err)
	}
	return state
}

func dedupDeepCloneState(t *testing.T, state RepositoryState) RepositoryState {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var cloned RepositoryState
	if err := json.Unmarshal(data, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func dedupDeepSaveFailureStore(
	t *testing.T,
	state RepositoryState,
	now time.Time,
) Store {
	t.Helper()
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return now }
	store.loadForTest = func(string) (RepositoryState, error) {
		return dedupDeepCloneState(t, state), nil
	}
	store.openForTest = func(context.Context) (*sql.DB, error) {
		return nil, errors.New("injected repository review save failure")
	}
	return store
}

type dedupDeepCompletionResult struct {
	state   RepositoryState
	finding Finding
	created bool
	err     error
}

func dedupDeepComplete(
	store Store,
	repository string,
	completion DeduplicationCompletion,
) dedupDeepCompletionResult {
	state, finding, created, err := store.CompleteDeduplicationJob(repository, completion)
	return dedupDeepCompletionResult{
		state: state, finding: finding, created: created, err: err,
	}
}

type dedupDeepFailureResult struct {
	state    RepositoryState
	raw      RawReviewFinding
	terminal bool
	err      error
}

func dedupDeepFail(
	store Store,
	repository, jobID, leaseID string,
	cause error,
) dedupDeepFailureResult {
	state, raw, terminal, err := store.FailDeduplicationJob(repository, jobID, leaseID, cause)
	return dedupDeepFailureResult{state: state, raw: raw, terminal: terminal, err: err}
}

func dedupDeepSameBucketFixture(t *testing.T) assignmentCoverageFixture {
	t.Helper()
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "deep-same-bucket", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	checkpoint := assignmentCoverageCheckpoint(fixture, "deep-same-bucket", 0, fixture.files)
	first := repositoryReviewCampaignFinding(fixture.files[0], "same bucket first")
	second := first
	second.Title = "same bucket second"
	second.Message = "same bucket second message"
	second.Evidence = "same bucket second evidence"
	checkpoint.Observation.Findings = []FindingCandidate{first, second}
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(t.Context(), checkpoint); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestDeduplicationDeepEngineErrorCoverage(t *testing.T) {
	diagnosis := deduplicationModelTestDiagnosis("deep")
	candidate := deduplicationModelTestCandidate(1, 1, "candidate")
	snapshot := RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "review", DeduplicationModel: "dedup",
		SimilarityThreshold: 90, CandidateLimit: 4,
	}
	if _, err := DeduplicationAdmissionBucket("", FileRef{}, ""); err == nil {
		t.Fatal("empty bucket identity accepted")
	}
	if _, err := DeduplicationAdmissionBucket("campaign\x00", FileRef{Path: "x", BlobSHA: "a"}, "s"); err == nil {
		t.Fatal("NUL bucket identity accepted")
	}

	for name, run := range map[string]func() error{
		"invalid settings": func() error {
			bad := snapshot
			bad.CandidateLimit = 21
			_, err := EvaluateDeduplicationCandidates(nil, bad, diagnosis, []DeduplicationCandidateSnapshot{candidate}, 0, nil, nil)
			return err
		},
		"missing scorer": func() error {
			_, err := EvaluateDeduplicationCandidates(nil, snapshot, diagnosis, []DeduplicationCandidateSnapshot{candidate}, 0, nil, nil)
			return err
		},
		"invalid candidates": func() error {
			_, err := EvaluateDeduplicationCandidates(nil, snapshot, diagnosis, []DeduplicationCandidateSnapshot{{ID: "bad"}}, 0,
				func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
					return DeduplicationScoringResponse{}, nil
				}, nil)
			return err
		},
		"canceled scoring": func() error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := EvaluateDeduplicationCandidates(ctx, snapshot, diagnosis, []DeduplicationCandidateSnapshot{candidate}, 0,
				func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
					return DeduplicationScoringResponse{}, nil
				}, nil)
			return err
		},
		"scorer failure": func() error {
			_, err := EvaluateDeduplicationCandidates(t.Context(), snapshot, diagnosis, []DeduplicationCandidateSnapshot{candidate}, 0,
				func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
					return DeduplicationScoringResponse{}, errors.New("score failed")
				}, nil)
			return err
		},
		"missing judge": func() error {
			_, err := EvaluateDeduplicationCandidates(t.Context(), snapshot, diagnosis, []DeduplicationCandidateSnapshot{candidate}, 0,
				func(_ context.Context, _ RepositoryReviewDeduplicationSnapshot, _ string, request DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
					return DeduplicationScoringResponse{Scores: []DeduplicationCandidateScore{{CandidateID: request.Candidates[0].ID, Score: 100, Explanation: "same"}}}, nil
				}, nil)
			return err
		},
		"judge failure": func() error {
			_, err := EvaluateDeduplicationCandidates(t.Context(), snapshot, diagnosis, []DeduplicationCandidateSnapshot{candidate}, 0,
				func(_ context.Context, _ RepositoryReviewDeduplicationSnapshot, _ string, request DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
					return DeduplicationScoringResponse{Scores: []DeduplicationCandidateScore{{CandidateID: request.Candidates[0].ID, Score: 100, Explanation: "same"}}}, nil
				},
				func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationJudgeRequest) (DeduplicationJudgment, error) {
					return DeduplicationJudgment{}, errors.New("judge failed")
				})
			return err
		},
		"malformed judgment": func() error {
			_, err := EvaluateDeduplicationCandidates(t.Context(), snapshot, diagnosis, []DeduplicationCandidateSnapshot{candidate}, 0,
				func(_ context.Context, _ RepositoryReviewDeduplicationSnapshot, _ string, request DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
					return DeduplicationScoringResponse{Scores: []DeduplicationCandidateScore{{CandidateID: request.Candidates[0].ID, Score: 100, Explanation: "same"}}}, nil
				},
				func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationJudgeRequest) (DeduplicationJudgment, error) {
					return DeduplicationJudgment{Decision: "duplicate", CandidateID: "not-supplied"}, nil
				})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("expected error")
			}
		})
	}

	twentyOne := make([]DeduplicationShortlistedCandidate, 21)
	for index := range twentyOne {
		twentyOne[index].OpaqueID = string(rune('a' + index))
	}
	if _, err := PrepareDeduplicationJudgeRequest(diagnosis, twentyOne, 0); err == nil {
		t.Fatal("oversized judge shortlist accepted")
	}
	if _, err := PrepareDeduplicationJudgeRequest(diagnosis, []DeduplicationShortlistedCandidate{{}}, 0); err == nil {
		t.Fatal("empty opaque judge ID accepted")
	}
	if _, err := DeduplicationCandidateUniverseDigest([]DeduplicationCandidateSnapshot{{ID: "bad"}}); err == nil {
		t.Fatal("invalid universe accepted")
	}
	requests, ordered, emptyScoringErr := PrepareDeduplicationScoringRequests(
		diagnosis,
		nil,
		0,
	)
	if emptyScoringErr != nil || len(requests) != 0 || len(ordered) != 0 {
		t.Fatalf("empty scoring universe=%#v %#v %v", requests, ordered, emptyScoringErr)
	}
	_, _, invalidScoringErr := PrepareDeduplicationScoringRequests(
		diagnosis,
		[]DeduplicationCandidateSnapshot{{ID: "bad"}},
		0,
	)
	if invalidScoringErr == nil {
		t.Fatal("invalid scoring candidate accepted")
	}

	many := make([]DeduplicationCandidateSnapshot, 17)
	for index := range many {
		many[index] = deduplicationModelTestCandidate(index+1, uint64(index+1), "candidate")
	}
	firstChunk, _, err := PrepareDeduplicationScoringRequests(diagnosis, many[:16], 0)
	if err != nil {
		t.Fatal(err)
	}
	encodedFirst, _ := json.Marshal(firstChunk[0])
	oversizedSnapshot := snapshot
	oversizedSnapshot.CandidateLimit = 17
	if _, err := EvaluateDeduplicationCandidates(
		t.Context(),
		oversizedSnapshot,
		diagnosis,
		many,
		len(encodedFirst)+8,
		func(_ context.Context, _ RepositoryReviewDeduplicationSnapshot, _ string, request DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
			response := DeduplicationScoringResponse{}
			for _, supplied := range request.Candidates {
				response.Scores = append(
					response.Scores,
					DeduplicationCandidateScore{
						CandidateID: supplied.ID, Score: 100, Explanation: "same",
					},
				)
			}
			return response, nil
		},
		func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationJudgeRequest) (DeduplicationJudgment, error) {
			return DeduplicationJudgment{Decision: "new"}, nil
		},
	); err == nil {
		t.Fatal("oversized aggregate judge request accepted")
	}
}

func TestDeduplicationDeepSlotAndSnapshotCoverage(t *testing.T) {
	store := NewStore(t.TempDir())
	release, err := store.AcquireDeduplicationSlot(nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	unsafeWorkspace := t.TempDir()
	unsafeStore := NewStore(unsafeWorkspace)
	lockPath := repositoryReviewTestLockPath(
		t,
		unsafeStore.root,
		"deduplication-slot-00.lock",
	)
	symlinkErr := os.Symlink(filepath.Join(unsafeWorkspace, "missing"), lockPath)
	if symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	unsafeRelease, unsafeAcquireErr := unsafeStore.AcquireDeduplicationSlot(t.Context())
	if unsafeAcquireErr == nil {
		unsafeRelease()
		t.Fatal("unsafe slot lock accepted")
	}

	snapshot, err := RepositoryReviewDeduplicationSnapshotFromAutomation(RepositoryReviewAutomation{
		ReviewerModels: []string{"review"}, AccountRef: "fallback-account",
		DeduplicationModel: "dedup", DeduplicationSimilarityThreshold: 90,
		DeduplicationCandidateLimit: 4,
	})
	if err != nil || snapshot.AccountRef != "fallback-account" || snapshot.DeduplicationModel != "dedup" {
		t.Fatalf("fallback snapshot=%#v err=%v", snapshot, err)
	}
	if _, err := RepositoryReviewDeduplicationSnapshotFromAutomation(RepositoryReviewAutomation{}); err == nil {
		t.Fatal("invalid automation snapshot accepted")
	}
	if reconcileFindingsProcessingCounters(nil) {
		t.Fatal("nil processing counters changed")
	}
}

func TestDeduplicationDeepScoringAndDecodeCoverage(t *testing.T) {
	diagnosis := deduplicationModelTestDiagnosis("deep")
	candidates := []DeduplicationCandidateSnapshot{
		{ID: "z", Version: 1, CreationOrdinal: 2, OpaqueID: "opaque-z", Diagnosis: diagnosis},
		{ID: "a", Version: 1, CreationOrdinal: 1, OpaqueID: "opaque-a", Diagnosis: diagnosis},
	}
	request := DeduplicationScoringRequest{
		Finding: diagnosis,
		Candidates: []DeduplicationScoringCandidate{
			{ID: "a", Diagnosis: diagnosis},
			{ID: "a", Diagnosis: diagnosis},
		},
	}
	if err := ValidateDeduplicationScoringResponse(
		DeduplicationScoringResponse{
			Scores: []DeduplicationCandidateScore{
				{CandidateID: "a", Score: 90, Explanation: "one"},
				{CandidateID: "a", Score: 90, Explanation: "two"},
			},
		},
		request,
	); err == nil {
		t.Fatal("duplicate request IDs accepted")
	}
	request.Candidates[0].ID = ""
	if err := ValidateDeduplicationScoringResponse(
		DeduplicationScoringResponse{Scores: make([]DeduplicationCandidateScore, 2)},
		request,
	); err == nil {
		t.Fatal("empty request ID accepted")
	}

	for name, scores := range map[string][]DeduplicationCandidateScore{
		"missing opaque":  {{CandidateID: "opaque-a", Score: 90, Explanation: "ok"}},
		"unknown score":   {{CandidateID: "opaque-a", Score: 90, Explanation: "ok"}, {CandidateID: "unknown", Score: 90, Explanation: "ok"}},
		"duplicate score": {{CandidateID: "opaque-a", Score: 90, Explanation: "ok"}, {CandidateID: "opaque-a", Score: 90, Explanation: "ok"}},
		"bad score":       {{CandidateID: "opaque-a", Score: -1, Explanation: "ok"}, {CandidateID: "opaque-z", Score: 90, Explanation: "ok"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ShortlistDeduplicationCandidates(candidates, scores, 90, 2); err == nil {
				t.Fatal("invalid scores accepted")
			}
		})
	}
	missingOpaque := append([]DeduplicationCandidateSnapshot(nil), candidates...)
	missingOpaque[0].OpaqueID = ""
	if _, err := ShortlistDeduplicationCandidates(
		missingOpaque,
		make([]DeduplicationCandidateScore, 2),
		90,
		2,
	); err == nil {
		t.Fatal("missing opaque candidate accepted")
	}
	duplicateOpaque := append([]DeduplicationCandidateSnapshot(nil), candidates...)
	duplicateOpaque[0].OpaqueID = duplicateOpaque[1].OpaqueID
	if _, err := ShortlistDeduplicationCandidates(
		duplicateOpaque,
		make([]DeduplicationCandidateScore, 2),
		90,
		2,
	); err == nil {
		t.Fatal("duplicate opaque candidate accepted")
	}
	for _, limits := range [][2]int{{-1, 1}, {101, 1}, {90, -1}, {90, 21}} {
		if _, err := ShortlistDeduplicationCandidates(candidates, nil, limits[0], limits[1]); err == nil {
			t.Fatalf("invalid threshold/limit accepted: %v", limits)
		}
	}

	if _, err := DecodeDeduplicationScoringResponse(nil); err == nil {
		t.Fatal("empty scoring JSON accepted")
	}
	if _, err := DecodeDeduplicationJudgment([]byte("{")); err == nil {
		t.Fatal("malformed judgment accepted")
	}
	if _, err := DecodeDeduplicationScoringResponse([]byte(`{"scores":[]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeDeduplicationJudgment([]byte(`{"decision":"new"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeDeduplicationJudgment([]byte(`{"decision":"new"} !`)); err == nil {
		t.Fatal("malformed trailing JSON accepted")
	}
	if _, err := DecodeDeduplicationJudgment(
		[]byte(strings.Repeat("x", DeduplicationMaximumInputBytes+1)),
	); err == nil {
		t.Fatal("oversized model response accepted")
	}
	if _, err := normalizeDeduplicationCandidateSnapshots([]DeduplicationCandidateSnapshot{
		deduplicationModelTestCandidate(1, 1, "x"),
		deduplicationModelTestCandidate(1, 2, "x"),
	}); err == nil {
		t.Fatal("duplicate canonical candidate accepted")
	}
	if _, err := ShortlistDeduplicationCandidates(
		[]DeduplicationCandidateSnapshot{{ID: "bad"}},
		nil,
		90,
		1,
	); err == nil {
		t.Fatal("invalid canonical shortlist candidate accepted")
	}
	ordinalCandidates := []DeduplicationCandidateSnapshot{
		{ID: "a", Version: 1, CreationOrdinal: 2, OpaqueID: "one", Diagnosis: diagnosis},
		{ID: "z", Version: 1, CreationOrdinal: 1, OpaqueID: "two", Diagnosis: diagnosis},
	}
	ordinalScores := []DeduplicationCandidateScore{
		{CandidateID: "one", Score: 90, Explanation: "same"},
		{CandidateID: "two", Score: 90, Explanation: "same"},
	}
	if got, err := ShortlistDeduplicationCandidates(ordinalCandidates, ordinalScores, 90, 2); err != nil ||
		got[0].ID != "z" {
		t.Fatalf("shortlist ordinal ordering=%#v err=%v", got, err)
	}
}

func TestDeduplicationDeepStoreInjectionCoverage(t *testing.T) {
	repository := "owner/injected"
	loadErr := errors.New("load failed")
	loadFailure := NewStore(t.TempDir())
	loadFailure.loadForTest = func(string) (RepositoryState, error) { return RepositoryState{}, loadErr }
	completion := DeduplicationCompletion{
		JobID:                   "job",
		LeaseID:                 "lease",
		CandidateUniverseDigest: "digest",
		Decision:                DeduplicationJudgment{Decision: "new"},
	}
	if _, _, _, err := loadFailure.ClaimDeduplicationJob(repository, "job", time.Minute); !errors.Is(err, loadErr) {
		t.Fatalf("claim load error=%v", err)
	}
	if _, _, _, err := loadFailure.CompleteDeduplicationJob(repository, completion); !errors.Is(err, loadErr) {
		t.Fatalf("complete load error=%v", err)
	}
	if _, _, _, err := loadFailure.FailDeduplicationJob(repository, "job", "lease", loadErr); !errors.Is(err, loadErr) {
		t.Fatalf("fail load error=%v", err)
	}
	if _, _, err := loadFailure.RetryDeduplication(repository, "raw"); !errors.Is(err, loadErr) {
		t.Fatalf("retry load error=%v", err)
	}
	if _, err := loadFailure.reconcileRepositoryDeduplicationJobs(repository); !errors.Is(err, loadErr) {
		t.Fatalf("reconcile load error=%v", err)
	}
	if _, err := loadFailure.ProcessPendingDeduplicationJobs(
		t.Context(),
		repository,
		DeduplicationProcessOptions{},
	); !errors.Is(
		err,
		loadErr,
	) {
		t.Fatalf("process load error=%v", err)
	}

	malformed := RepositoryState{
		Repository:        repository,
		DeduplicationJobs: []DeduplicationJob{{ID: "job", RawFindingID: "missing"}},
	}
	broken := NewStore(t.TempDir())
	broken.loadForTest = func(string) (RepositoryState, error) { return malformed, nil }
	if _, _, _, err := broken.ClaimDeduplicationJob(repository, "job", time.Minute); err == nil {
		t.Fatal("claim without raw accepted")
	}
	if _, _, _, err := broken.CompleteDeduplicationJob(repository, completion); err == nil {
		t.Fatal("completion without raw accepted")
	}

	workspace := t.TempDir()
	unsafeStore := NewStore(workspace)
	if err := os.Symlink(t.TempDir(), filepath.Join(workspace, storeDirectory)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := unsafeStore.ClaimDeduplicationJob(repository, "job", time.Minute); err == nil {
		t.Fatal("claim acquired unsafe lock")
	}
	if _, _, _, err := unsafeStore.CompleteDeduplicationJob(repository, completion); err == nil {
		t.Fatal("completion acquired unsafe lock")
	}
	if _, _, _, err := unsafeStore.FailDeduplicationJob(repository, "job", "lease", errors.New("x")); err == nil {
		t.Fatal("failure release acquired unsafe lock")
	}
	if _, _, err := unsafeStore.RetryDeduplication(repository, "raw"); err == nil {
		t.Fatal("retry acquired unsafe lock")
	}
	if _, err := unsafeStore.reconcileRepositoryDeduplicationJobs(repository); err == nil {
		t.Fatal("reconcile acquired unsafe lock")
	}

	lockWorkspace := t.TempDir()
	lockFailure := NewStore(lockWorkspace)
	lockPath := repositoryReviewTestLockPath(t, lockFailure.root, "store.lock")
	if err := os.Symlink(filepath.Join(lockWorkspace, "missing"), lockPath); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := lockFailure.ClaimDeduplicationJob(repository, "job", time.Minute); err == nil {
		t.Fatal("claim ignored unsafe lock file")
	}
	if _, _, _, err := lockFailure.CompleteDeduplicationJob(repository, completion); err == nil {
		t.Fatal("completion ignored unsafe lock file")
	}
	if _, _, _, err := lockFailure.FailDeduplicationJob(repository, "job", "lease", errors.New("x")); err == nil {
		t.Fatal("failure release ignored unsafe lock file")
	}
	if _, _, err := lockFailure.RetryDeduplication(repository, "raw"); err == nil {
		t.Fatal("retry ignored unsafe lock file")
	}
	if _, err := lockFailure.reconcileRepositoryDeduplicationJobs(repository); err == nil {
		t.Fatal("reconcile ignored unsafe lock file")
	}
}

func TestDeduplicationDeepRemainingWorkerCoverage(t *testing.T) {
	fixture := dedupDeepPendingFixture(t, 1)
	pending := dedupDeepState(t, fixture)
	job := pending.DeduplicationJobs[0]

	conflictState := dedupDeepCloneState(t, pending)
	conflictState.RawFindings[0].Disposition = RawFindingDispositionNew
	conflictStore := NewStore(t.TempDir())
	conflictStore.loadForTest = func(string) (RepositoryState, error) { return conflictState, nil }
	if _, _, _, err := conflictStore.ClaimDeduplicationJob(fixture.repository, job.ID, time.Minute); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("invalid raw claim error=%v", err)
	}

	claimSaveFailure := dedupDeepSaveFailureStore(t, pending, repositoryAuditTestNow)
	if _, _, _, err := claimSaveFailure.ClaimDeduplicationJob(fixture.repository, job.ID, time.Minute); err == nil {
		t.Fatal("claim save failure ignored")
	}
	attemptLimit := dedupDeepCloneState(t, pending)
	attemptLimit.DeduplicationJobs[0].Attempts = DeduplicationAttemptLimit
	attemptSaveFailure := dedupDeepSaveFailureStore(t, attemptLimit, repositoryAuditTestNow)
	if _, _, _, err := attemptSaveFailure.ClaimDeduplicationJob(fixture.repository, job.ID, time.Minute); err == nil {
		t.Fatal("attempt-limit save failure ignored")
	}

	_, claim, claimed, err := fixture.store.ClaimDeduplicationJob(fixture.repository, job.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	running := dedupDeepState(t, fixture)
	completion := DeduplicationCompletion{
		JobID: claim.Job.ID, LeaseID: claim.Job.LeaseID,
		CandidateUniverseDigest: claim.UniverseDigest,
		Decision:                DeduplicationJudgment{Decision: "new"},
	}
	wrongLease := completion
	wrongLease.LeaseID = "wrong"
	wrongLeaseResult := dedupDeepComplete(fixture.store, fixture.repository, wrongLease)
	if !errors.Is(
		wrongLeaseResult.err,
		ErrConflict,
	) {
		t.Fatalf("wrong completion lease error=%v", wrongLeaseResult.err)
	}
	collision := dedupDeepCloneState(t, running)
	collision.Findings = append(collision.Findings, Finding{ID: stableID("rdf_", collision.RawFindings[0].ID)})
	collisionStore := NewStore(t.TempDir())
	collisionStore.now = func() time.Time { return claim.Job.UpdatedAt.Add(time.Second) }
	collisionStore.loadForTest = func(string) (RepositoryState, error) { return collision, nil }
	collisionResult := dedupDeepComplete(collisionStore, fixture.repository, completion)
	if !errors.Is(
		collisionResult.err,
		ErrConflict,
	) {
		t.Fatalf("deduplicated collision error=%v", collisionResult.err)
	}
	completeSaveFailure := dedupDeepSaveFailureStore(t, running, claim.Job.UpdatedAt.Add(time.Second))
	completionSaveResult := dedupDeepComplete(
		completeSaveFailure,
		fixture.repository,
		completion,
	)
	if completionSaveResult.err == nil || completionSaveResult.state.Repository != "" ||
		completionSaveResult.finding.ID != "" || completionSaveResult.created {
		t.Fatal("completion save failure ignored")
	}
	failSaveFailure := dedupDeepSaveFailureStore(t, running, claim.Job.UpdatedAt.Add(time.Second))
	failState, failRaw, failTerminal, failErr := failSaveFailure.FailDeduplicationJob(
		fixture.repository,
		job.ID,
		claim.Job.LeaseID,
		errors.New("x"),
	)
	if failState.Repository != "" || failRaw.ID != "" || failTerminal || failErr == nil {
		t.Fatalf(
			"failure release save result=%#v raw=%#v terminal=%v err=%v",
			failState, failRaw, failTerminal, failErr,
		)
	}

	completedState, completedFinding, _, err := fixture.store.CompleteDeduplicationJob(fixture.repository, completion)
	if err != nil {
		t.Fatal(err)
	}
	terminalState, terminalClaim, terminalClaimed, terminalErr := fixture.store.ClaimDeduplicationJob(
		fixture.repository,
		job.ID,
		time.Minute,
	)
	if terminalErr != nil || terminalState.Repository == "" || terminalClaimed ||
		terminalClaim.Job.State != DeduplicationJobCompleted {
		t.Fatalf("terminal claim=%#v claimed=%v err=%v", terminalClaim, terminalClaimed, terminalErr)
	}
	missingCompletedTarget := dedupDeepCloneState(t, completedState)
	missingCompletedTarget.Findings = nil
	missingTargetStore := NewStore(t.TempDir())
	missingTargetStore.loadForTest = func(string) (RepositoryState, error) { return missingCompletedTarget, nil }
	missingTargetResult := dedupDeepComplete(missingTargetStore, fixture.repository, completion)
	if !errors.Is(
		missingTargetResult.err,
		ErrConflict,
	) {
		t.Fatalf("missing completed target error=%v", missingTargetResult.err)
	}
	_ = completedFinding

	failedFixture := dedupDeepPendingFixture(t, 1)
	failedPending := dedupDeepState(t, failedFixture)
	_, failedClaim, claimed, err := failedFixture.store.ClaimDeduplicationJob(
		failedFixture.repository,
		failedPending.DeduplicationJobs[0].ID,
		time.Minute,
	)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	failedRunning := dedupDeepState(t, failedFixture)
	failedRunning.DeduplicationJobs[0].Attempts = DeduplicationAttemptLimit
	failedRunning.Version++
	failedSaveErr := failedFixture.store.save(&failedRunning)
	if failedSaveErr != nil {
		t.Fatal(failedSaveErr)
	}
	failedState, _, terminal, err := failedFixture.store.FailDeduplicationJob(
		failedFixture.repository, failedClaim.Job.ID, failedClaim.Job.LeaseID, errors.New("terminal"),
	)
	if err != nil || !terminal {
		t.Fatal(err)
	}
	failedState.NextDeduplicationOrdinal = 0
	retryZeroStore := NewStore(t.TempDir())
	retryZeroStore.loadForTest = func(string) (RepositoryState, error) {
		return dedupDeepCloneState(t, failedState), nil
	}
	if retried, _, retryErr := retryZeroStore.RetryDeduplication(
		failedFixture.repository,
		failedState.RawFindings[0].ID,
	); retryErr != nil ||
		retried.NextDeduplicationOrdinal != 2 {
		t.Fatalf("zero-tail retry state=%#v err=%v", retried, retryErr)
	}
	retrySaveFailure := dedupDeepSaveFailureStore(t, failedState, repositoryAuditTestNow)
	if _, _, retryErr := retrySaveFailure.RetryDeduplication(
		failedFixture.repository,
		failedState.RawFindings[0].ID,
	); retryErr == nil {
		t.Fatal("retry save failure ignored")
	}

	runningReconcile := dedupDeepCloneState(t, running)
	reconcileSaveFailure := dedupDeepSaveFailureStore(t, runningReconcile, claim.Job.UpdatedAt.Add(time.Second))
	if _, err := reconcileSaveFailure.reconcileRepositoryDeduplicationJobs(fixture.repository); err == nil {
		t.Fatal("reconcile save failure ignored")
	}
	reconcileOuter := fixture.store
	reconcileOuter.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, errors.New("reconcile load failed")
	}
	if _, err := reconcileOuter.ReconcileDeduplicationJobs(t.Context()); err == nil {
		t.Fatal("outer reconciliation ignored repository error")
	}

	if ordered, err := normalizeDurableDeduplicationScores(
		[]DeduplicationCandidateScore{
			{CandidateID: "b", Score: 90, Explanation: "same"},
			{CandidateID: "a", Score: 90, Explanation: "same"},
		},
		&DeduplicationJob{ModelSnapshot: RepositoryReviewDeduplicationSnapshot{
			SimilarityThreshold: 90,
			CandidateLimit:      2,
		}},
		[]DeduplicationCandidateSnapshot{
			{ID: "a", Version: 1, CreationOrdinal: 1},
			{ID: "b", Version: 1, CreationOrdinal: 2},
		},
	); err != nil || ordered[0].CandidateID != "a" {
		t.Fatalf("durable ordinal ordering=%#v err=%v", ordered, err)
	}
}

func TestDeduplicationDeepProcessorOrderingCoverage(t *testing.T) {
	state := RepositoryState{Repository: "owner/process-order", Version: 1, DeduplicationJobs: []DeduplicationJob{
		{ID: "z", AdmissionBucket: "a", InsertionOrdinal: 1, State: DeduplicationJobPending},
		{ID: "a", AdmissionBucket: "a", InsertionOrdinal: 1, State: DeduplicationJobPending},
		{ID: "b", AdmissionBucket: "b", InsertionOrdinal: 1, State: DeduplicationJobPending},
	}}
	store := NewStore(t.TempDir())
	store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ProcessPendingDeduplicationJobs(
		canceled,
		state.Repository,
		DeduplicationProcessOptions{},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("ordered canceled processor error=%v", err)
	}
	empty := state
	empty.DeduplicationJobs = nil
	store.loadForTest = func(string) (RepositoryState, error) { return empty, nil }
	if result, err := store.ProcessPendingDeduplicationJobs(
		nil,
		state.Repository,
		DeduplicationProcessOptions{},
	); err != nil ||
		result != (DeduplicationProcessResult{}) {
		t.Fatalf("nil-context empty processor=%#v err=%v", result, err)
	}
}

func TestDeduplicationDeepClaimAndCompletionCoverage(t *testing.T) {
	if _, _, _, err := (Store{}).ClaimDeduplicationJob("", "", -time.Second); err == nil {
		t.Fatal("invalid claim accepted")
	}
	fixture := dedupDeepSameBucketFixture(t)
	state := dedupDeepState(t, fixture)
	if _, _, _, err := fixture.store.ClaimDeduplicationJob(fixture.repository, "missing", time.Minute); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("missing claim error=%v", err)
	}
	firstJob, secondJob := state.DeduplicationJobs[0], state.DeduplicationJobs[1]
	secondState, secondClaim, secondClaimed, secondErr := fixture.store.ClaimDeduplicationJob(
		fixture.repository,
		secondJob.ID,
		time.Minute,
	)
	if secondErr != nil || secondState.Repository == "" || secondClaimed ||
		secondClaim.Job.State != DeduplicationJobPending {
		t.Fatalf("FIFO claim=%#v claimed=%v err=%v", secondClaim, secondClaimed, secondErr)
	}
	firstState, firstClaim, firstClaimed, firstErr := fixture.store.ClaimDeduplicationJob(
		fixture.repository,
		firstJob.ID,
		time.Minute,
	)
	if firstErr != nil || firstState.Repository == "" || !firstClaimed {
		t.Fatalf("first claim=%#v claimed=%v err=%v", firstClaim, firstClaimed, firstErr)
	}
	runningState, running, runningClaimed, runningErr := fixture.store.ClaimDeduplicationJob(
		fixture.repository,
		firstJob.ID,
		time.Minute,
	)
	if runningErr != nil || runningState.Repository == "" || runningClaimed ||
		running.Job.State != DeduplicationJobRunning {
		t.Fatalf("running claim=%#v claimed=%v err=%v", running, runningClaimed, runningErr)
	}
	fixture.store.now = func() time.Time { return firstClaim.Job.LeaseExpiresAt.Add(time.Second) }
	reclaimedState, reclaimed, reclaimedClaimed, reclaimedErr := fixture.store.ClaimDeduplicationJob(
		fixture.repository,
		firstJob.ID,
		time.Minute,
	)
	if reclaimedErr != nil || reclaimedState.Repository == "" || !reclaimedClaimed || reclaimed.Job.Attempts != 2 {
		t.Fatalf("expired reclaim=%#v claimed=%v err=%v", reclaimed, reclaimedClaimed, reclaimedErr)
	}

	invalidResult := dedupDeepComplete(fixture.store, "", DeduplicationCompletion{})
	if invalidResult.err == nil || invalidResult.state.Repository != "" ||
		invalidResult.finding.ID != "" || invalidResult.created {
		t.Fatal("invalid completion accepted")
	}
	missingResult := dedupDeepComplete(fixture.store, fixture.repository, DeduplicationCompletion{
		JobID: "missing", LeaseID: "lease", CandidateUniverseDigest: "digest",
		Decision: DeduplicationJudgment{Decision: "new"},
	})
	if !errors.Is(missingResult.err, os.ErrNotExist) || missingResult.state.Repository != "" ||
		missingResult.finding.ID != "" || missingResult.created {
		t.Fatalf("missing completion error=%v", missingResult.err)
	}
	fixture.store.now = func() time.Time { return reclaimed.Job.LeaseExpiresAt.Add(time.Second) }
	expiredResult := dedupDeepComplete(fixture.store, fixture.repository, DeduplicationCompletion{
		JobID: reclaimed.Job.ID, LeaseID: reclaimed.Job.LeaseID,
		CandidateUniverseDigest: reclaimed.UniverseDigest,
		Decision:                DeduplicationJudgment{Decision: "new"},
	})
	if !errors.Is(expiredResult.err, ErrDeduplicationLeaseExpired) ||
		expiredResult.state.Repository != "" || expiredResult.finding.ID != "" || expiredResult.created {
		t.Fatalf("expired completion error=%v", expiredResult.err)
	}

	// Restore a clock inside the reclaimed lease and complete it as new.
	fixture.store.now = func() time.Time { return reclaimed.Job.UpdatedAt.Add(time.Second) }
	completion := DeduplicationCompletion{
		JobID: reclaimed.Job.ID, LeaseID: reclaimed.Job.LeaseID,
		CandidateUniverseDigest: reclaimed.UniverseDigest,
		Decision:                DeduplicationJudgment{Decision: "new"},
	}
	completedResult := dedupDeepComplete(fixture.store, fixture.repository, completion)
	completedState, target := completedResult.state, completedResult.finding
	if completedResult.err != nil || !completedResult.created {
		t.Fatalf("new completion target=%#v created=%v err=%v", target, completedResult.created, completedResult.err)
	}
	replayResult := dedupDeepComplete(fixture.store, fixture.repository, completion)
	if replayResult.err != nil || !replayResult.created || replayResult.finding.ID != target.ID {
		t.Fatalf(
			"idempotent completion=%#v created=%v err=%v",
			replayResult.finding, replayResult.created, replayResult.err,
		)
	}
	wrong := completion
	wrong.Decision = DeduplicationJudgment{Decision: "duplicate", CandidateID: "other"}
	changedResult := dedupDeepComplete(fixture.store, fixture.repository, wrong)
	if !errors.Is(changedResult.err, ErrConflict) || changedResult.state.Repository != "" ||
		changedResult.finding.ID != "" || changedResult.created {
		t.Fatalf("changed replay error=%v", changedResult.err)
	}
	if len(completedState.Findings) != 1 {
		t.Fatalf("completed state=%#v", completedState.Findings)
	}

	// The second same-bucket raw now snapshots the created candidate.
	duplicateState, duplicateClaim, duplicateClaimed, duplicateErr := fixture.store.ClaimDeduplicationJob(
		fixture.repository,
		secondJob.ID,
		time.Minute,
	)
	if duplicateErr != nil ||
		duplicateState.Repository == "" ||
		!duplicateClaimed ||
		len(duplicateClaim.Candidates) != 1 {
		t.Fatalf("duplicate claim=%#v claimed=%v err=%v", duplicateClaim, duplicateClaimed, duplicateErr)
	}
	baseDuplicate := DeduplicationCompletion{
		JobID: duplicateClaim.Job.ID, LeaseID: duplicateClaim.Job.LeaseID,
		CandidateUniverseDigest: duplicateClaim.UniverseDigest,
		Decision:                DeduplicationJudgment{Decision: "duplicate", CandidateID: target.ID},
	}
	badScore := baseDuplicate
	badScore.ShortlistedScores = []DeduplicationCandidateScore{{
		CandidateID: target.ID, Score: 89, Explanation: "below threshold",
	}}
	if badScoreResult := dedupDeepComplete(fixture.store, fixture.repository, badScore); badScoreResult.err == nil {
		t.Fatal("invalid completion shortlist accepted")
	}
	if missingScoreResult := dedupDeepComplete(
		fixture.store, fixture.repository, baseDuplicate,
	); missingScoreResult.err == nil {
		t.Fatal("duplicate outside shortlist accepted")
	}
	baseDuplicate.ShortlistedScores = []DeduplicationCandidateScore{{
		CandidateID: target.ID, Score: 100, Explanation: "same defect",
	}}
	duplicateRunning := dedupDeepState(t, fixture)
	duplicateRunning.Findings[0].RawSourceIDs = append(
		duplicateRunning.Findings[0].RawSourceIDs,
		duplicateClaim.RawFinding.ID,
	)
	duplicateConflictStore := NewStore(t.TempDir())
	duplicateConflictStore.now = func() time.Time { return duplicateClaim.Job.UpdatedAt.Add(time.Second) }
	duplicateConflictStore.loadForTest = func(string) (RepositoryState, error) {
		return dedupDeepCloneState(t, duplicateRunning), nil
	}
	duplicateConflictResult := dedupDeepComplete(duplicateConflictStore, fixture.repository, baseDuplicate)
	if !errors.Is(duplicateConflictResult.err, ErrConflict) {
		t.Fatalf("repeated raw source error=%v", duplicateConflictResult.err)
	}
	duplicateResult := dedupDeepComplete(fixture.store, fixture.repository, baseDuplicate)
	if duplicateResult.err != nil || duplicateResult.created || len(duplicateResult.finding.RawSourceIDs) != 2 {
		t.Fatalf(
			"duplicate completion=%#v created=%v err=%v",
			duplicateResult.finding, duplicateResult.created, duplicateResult.err,
		)
	}
}

func TestDeduplicationDeepDurableScoreCoverage(t *testing.T) {
	candidates := []DeduplicationCandidateSnapshot{
		{ID: "b", Version: 1, CreationOrdinal: 2},
		{ID: "a", Version: 1, CreationOrdinal: 1},
		{ID: "c", Version: 1, CreationOrdinal: 1},
	}
	job := &DeduplicationJob{
		ModelSnapshot: RepositoryReviewDeduplicationSnapshot{SimilarityThreshold: 90, CandidateLimit: 3},
	}
	valid := []DeduplicationCandidateScore{
		{CandidateID: "b", Score: 99, Explanation: "same"},
		{CandidateID: "c", Score: 95, Explanation: "same"},
		{CandidateID: "a", Score: 95, Explanation: "same"},
	}
	ordered, err := normalizeDurableDeduplicationScores(valid, job, candidates)
	if err != nil || ordered[0].CandidateID != "b" || ordered[1].CandidateID != "a" {
		t.Fatalf("durable ordering=%#v err=%v", ordered, err)
	}
	for name, input := range map[string]struct {
		scores []DeduplicationCandidateScore
		job    *DeduplicationJob
	}{
		"nil job":         {scores: nil, job: nil},
		"too many":        {scores: append(valid, valid[0]), job: job},
		"unknown":         {scores: []DeduplicationCandidateScore{{CandidateID: "x", Score: 90, Explanation: "same"}}, job: job},
		"below threshold": {scores: []DeduplicationCandidateScore{{CandidateID: "a", Score: 89, Explanation: "same"}}, job: job},
		"above range":     {scores: []DeduplicationCandidateScore{{CandidateID: "a", Score: 101, Explanation: "same"}}, job: job},
		"bad explanation": {scores: []DeduplicationCandidateScore{{CandidateID: "a", Score: 90}}, job: job},
		"duplicate":       {scores: []DeduplicationCandidateScore{{CandidateID: "a", Score: 90, Explanation: "same"}, {CandidateID: "a", Score: 91, Explanation: "same"}}, job: job},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeDurableDeduplicationScores(input.scores, input.job, candidates); err == nil {
				t.Fatal("invalid durable scores accepted")
			}
		})
	}
}

func TestDeduplicationDeepFailRetryCoverage(t *testing.T) {
	if result := dedupDeepFail(Store{}, "", "", "", errors.New("x")); result.err == nil {
		t.Fatal("invalid failure release accepted")
	}
	fixture := dedupDeepPendingFixture(t, 1)
	state := dedupDeepState(t, fixture)
	missingResult := dedupDeepFail(fixture.store, fixture.repository, "missing", "lease", errors.New("x"))
	if !errors.Is(
		missingResult.err,
		os.ErrNotExist,
	) {
		t.Fatalf("missing failure job error=%v", missingResult.err)
	}
	job := state.DeduplicationJobs[0]
	claimState, claim, claimSucceeded, claimErr := fixture.store.ClaimDeduplicationJob(
		fixture.repository,
		job.ID,
		time.Minute,
	)
	if claimErr != nil || claimState.Repository == "" || !claimSucceeded {
		t.Fatal(claimErr)
	}
	wrongLeaseResult := dedupDeepFail(fixture.store, fixture.repository, job.ID, "wrong", errors.New("x"))
	if !errors.Is(
		wrongLeaseResult.err,
		ErrConflict,
	) {
		t.Fatalf("wrong lease error=%v", wrongLeaseResult.err)
	}
	universeResult := dedupDeepFail(
		fixture.store, fixture.repository, job.ID, claim.Job.LeaseID, ErrDeduplicationUniverseChanged,
	)
	if universeResult.err != nil || universeResult.terminal ||
		universeResult.raw.State != RawFindingDeduplicationPending ||
		universeResult.raw.History[len(universeResult.raw.History)-1].Failure.Code != "candidate_universe_changed" {
		t.Fatalf(
			"universe failure raw=%#v terminal=%v err=%v",
			universeResult.raw, universeResult.terminal, universeResult.err,
		)
	}

	for name, cause := range map[string]error{
		"canceled": context.Canceled,
		"deadline": context.DeadlineExceeded,
		"lease":    ErrDeduplicationLeaseExpired,
		"provider": errors.New("provider"),
	} {
		t.Run(name, func(t *testing.T) {
			local := dedupDeepPendingFixture(t, 1)
			localState := dedupDeepState(t, local)
			localClaimState, localClaim, localClaimed, localClaimErr := local.store.ClaimDeduplicationJob(
				local.repository,
				localState.DeduplicationJobs[0].ID,
				time.Minute,
			)
			if localClaimErr != nil || !localClaimed || localClaimState.Repository == "" {
				t.Fatal(localClaimErr)
			}
			failureResult := dedupDeepFail(
				local.store, local.repository, localClaim.Job.ID, localClaim.Job.LeaseID, cause,
			)
			if failureResult.err != nil || failureResult.terminal {
				t.Fatalf("failure terminal=%v err=%v", failureResult.terminal, failureResult.err)
			}
		})
	}

	terminalFixture := dedupDeepPendingFixture(t, 1)
	terminalState := dedupDeepState(t, terminalFixture)
	terminalClaimState, terminalClaim, terminalClaimed, terminalClaimErr := terminalFixture.store.ClaimDeduplicationJob(
		terminalFixture.repository,
		terminalState.DeduplicationJobs[0].ID,
		time.Minute,
	)
	if terminalClaimErr != nil || terminalClaimState.Repository == "" || !terminalClaimed {
		t.Fatal(terminalClaimErr)
	}
	terminalState = dedupDeepState(t, terminalFixture)
	terminalState.DeduplicationJobs[0].Attempts = DeduplicationAttemptLimit
	terminalState.Version++
	terminalSaveErr := terminalFixture.store.save(&terminalState)
	if terminalSaveErr != nil {
		t.Fatal(terminalSaveErr)
	}
	failureResult := dedupDeepFail(
		terminalFixture.store,
		terminalFixture.repository, terminalClaim.Job.ID, terminalClaim.Job.LeaseID, errors.New("final"),
	)
	failedState, failedRaw := failureResult.state, failureResult.raw
	if failureResult.err != nil || !failureResult.terminal || failedRaw.State != RawFindingDeduplicationFailed {
		t.Fatalf("terminal raw=%#v terminal=%v err=%v", failedRaw, failureResult.terminal, failureResult.err)
	}
	if _, _, err := terminalFixture.store.RetryDeduplication("", ""); err == nil {
		t.Fatal("invalid retry accepted")
	}
	if _, _, err := terminalFixture.store.RetryDeduplication(terminalFixture.repository, "missing"); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("missing retry error=%v", err)
	}
	if _, _, err := terminalFixture.store.RetryDeduplication(terminalFixture.repository, failedRaw.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := terminalFixture.store.RetryDeduplication(terminalFixture.repository, failedRaw.ID); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("nonfailed retry error=%v", err)
	}
	missingJob := failedState
	missingJob.DeduplicationJobs = nil
	broken := NewStore(t.TempDir())
	broken.loadForTest = func(string) (RepositoryState, error) { return missingJob, nil }
	if _, _, err := broken.RetryDeduplication(terminalFixture.repository, failedRaw.ID); err == nil {
		t.Fatal("retry without job accepted")
	}
	missingRaw := failedState
	missingRaw.RawFindings = nil
	broken.loadForTest = func(string) (RepositoryState, error) { return missingRaw, nil }
	if result := dedupDeepFail(
		broken,
		terminalFixture.repository,
		terminalClaim.Job.ID,
		terminalClaim.Job.LeaseID,
		errors.New("x"),
	); result.err == nil {
		t.Fatal("failure release without raw accepted")
	}
}

func TestDeduplicationDeepProcessorFailureCoverage(t *testing.T) {
	missing := NewStore(t.TempDir())
	if _, err := missing.ProcessPendingDeduplicationJobs(
		t.Context(),
		"owner/missing",
		DeduplicationProcessOptions{},
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("missing processor error=%v", err)
	}
	invalidLease := dedupDeepPendingFixture(t, 1)
	if _, err := invalidLease.store.ProcessPendingDeduplicationJobs(
		t.Context(),
		invalidLease.repository,
		DeduplicationProcessOptions{
			LeaseDuration: DeduplicationMaximumLeaseDuration + time.Second,
		},
	); err == nil {
		t.Fatal("invalid processor lease accepted")
	}

	fixture := dedupDeepSameBucketFixture(t)
	state := dedupDeepState(t, fixture)
	_, seedClaim, claimed, err := fixture.store.ClaimDeduplicationJob(
		fixture.repository,
		state.DeduplicationJobs[0].ID,
		time.Minute,
	)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	seedResult := dedupDeepComplete(fixture.store, fixture.repository, DeduplicationCompletion{
		JobID: seedClaim.Job.ID, LeaseID: seedClaim.Job.LeaseID,
		CandidateUniverseDigest: seedClaim.UniverseDigest,
		Decision:                DeduplicationJudgment{Decision: "new"},
	})
	if seedResult.err != nil || seedResult.state.Repository == "" || seedResult.finding.ID == "" ||
		!seedResult.created {
		t.Fatal(seedResult.err)
	}
	releases := make([]func(), 0, DeduplicationConcurrency)
	for range DeduplicationConcurrency {
		release, slotErr := fixture.store.AcquireDeduplicationSlot(t.Context())
		if slotErr != nil {
			t.Fatal(slotErr)
		}
		releases = append(releases, release)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	state = dedupDeepState(t, fixture)
	secondJobID := state.DeduplicationJobs[1].ID
	blockedOutcome := fixture.store.processOneDeduplicationJob(
		canceled,
		fixture.repository,
		secondJobID,
		DeduplicationProcessOptions{},
	)
	for _, release := range releases {
		release()
	}
	if blockedOutcome.err == nil || blockedOutcome.created || blockedOutcome.duplicate {
		t.Fatalf("canceled processor outcome=%#v", blockedOutcome)
	}
	processed := DeduplicationProcessResult{}

	// A model callback changes the candidate universe after claim. Completion
	// fails and the verified lease is safely returned to pending.
	processed, err = fixture.store.ProcessPendingDeduplicationJobs(
		t.Context(),
		fixture.repository,
		DeduplicationProcessOptions{
			Score: func(_ context.Context, _ RepositoryReviewDeduplicationSnapshot, _ string, request DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
				current := dedupDeepState(t, fixture)
				current.Findings[0].Version++
				current.Version++
				if saveErr := fixture.store.save(&current); saveErr != nil {
					t.Fatal(saveErr)
				}
				return DeduplicationScoringResponse{
					Scores: []DeduplicationCandidateScore{
						{CandidateID: request.Candidates[0].ID, Score: 100, Explanation: "same"},
					},
				}, nil
			},
			Judge: func(_ context.Context, _ RepositoryReviewDeduplicationSnapshot, _ string, request DeduplicationJudgeRequest) (DeduplicationJudgment, error) {
				return DeduplicationJudgment{Decision: "duplicate", CandidateID: request.Candidates[0].OpaqueID}, nil
			},
		},
	)
	if err == nil || processed.Completed != 0 {
		t.Fatalf("stale processor=%#v err=%v", processed, err)
	}

	// Third attempt fails terminally through the scorer-error path.
	providerOptions := DeduplicationProcessOptions{
		Score: func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
			return DeduplicationScoringResponse{}, errors.New("provider failed")
		},
	}
	for attempt := 0; attempt < DeduplicationAttemptLimit && processed.Failed == 0; attempt++ {
		processed, err = fixture.store.ProcessPendingDeduplicationJobs(t.Context(), fixture.repository, providerOptions)
	}
	if err == nil || processed.Failed != 1 {
		t.Fatalf("terminal processor=%#v err=%v", processed, err)
	}
	if empty, err := fixture.store.ProcessPendingDeduplicationJobs(
		t.Context(), fixture.repository, DeduplicationProcessOptions{},
	); err != nil ||
		empty != (DeduplicationProcessResult{}) {
		t.Fatalf("empty processor=%#v err=%v", empty, err)
	}

	deferredFixture := dedupDeepSameBucketFixture(t)
	deferredState := dedupDeepState(t, deferredFixture)
	deferredClaimState, _, deferredClaimed, deferredClaimErr := deferredFixture.store.ClaimDeduplicationJob(
		deferredFixture.repository,
		deferredState.DeduplicationJobs[0].ID,
		time.Minute,
	)
	if deferredClaimErr != nil || deferredClaimState.Repository == "" || !deferredClaimed {
		t.Fatal(deferredClaimErr)
	}
	if deferred, err := deferredFixture.store.ProcessPendingDeduplicationJobs(
		t.Context(), deferredFixture.repository, DeduplicationProcessOptions{},
	); err != nil ||
		deferred.Deferred != 1 {
		t.Fatalf("deferred processor=%#v err=%v", deferred, err)
	}
}

func TestDeduplicationCanonicalStateValidationCoverage(t *testing.T) {
	fixture := dedupDeepPendingFixture(t, 1)
	if _, err := fixture.store.ProcessPendingDeduplicationJobs(
		t.Context(), fixture.repository, DeduplicationProcessOptions{},
	); err != nil {
		t.Fatal(err)
	}
	base := dedupDeepState(t, fixture)
	if err := validateDeduplicationState(base); err != nil {
		t.Fatalf("valid baseline: %v", err)
	}
	mutations := map[string]func(*RepositoryState){
		"raw diagnosis": func(state *RepositoryState) {
			state.RawFindings[0].Severity = ""
			state.RawFindings[0].DiagnosisDigest = RawReviewFindingDiagnosisDigest(state.RawFindings[0])
		},
		"raw snapshot digest": func(state *RepositoryState) {
			state.RawFindings[0].DeduplicationSnapshotDigest = "sha256:" + strings.Repeat("0", 64)
			state.RawFindings[0].DiagnosisDigest = RawReviewFindingDiagnosisDigest(state.RawFindings[0])
		},
		"raw history": func(state *RepositoryState) {
			state.RawFindings[0].History = append(state.RawFindings[0].History, RawFindingHistoryEntry{
				State: "bad", Disposition: RawFindingDispositionUndecided, At: repositoryAuditTestNow,
			})
		},
		"dedup missing source": func(state *RepositoryState) {
			state.Findings[0].RawSourceIDs = append(state.Findings[0].RawSourceIDs, "missing")
		},
		"dedup duplicate source": func(state *RepositoryState) {
			id := state.RawFindings[0].ID
			state.Findings[0].RawSourceIDs = []string{id, id}
		},
		"dedup rewrite": func(state *RepositoryState) {
			state.Findings[0].Title = "rewritten"
		},
		"dedup contributor": func(state *RepositoryState) {
			state.Findings[0].Models = append(state.Findings[0].Models, "invented")
		},
		"dedup history": func(state *RepositoryState) {
			state.Findings[0].History = append(
				state.Findings[0].History,
				DeduplicatedFindingHistoryEntry{At: repositoryAuditTestNow},
			)
		},
		"job state": func(state *RepositoryState) {
			state.DeduplicationJobs[0].State = "bad"
		},
		"job snapshot": func(state *RepositoryState) {
			state.DeduplicationJobs[0].ModelSnapshot.SimilarityThreshold--
		},
		"candidate version": func(state *RepositoryState) {
			state.DeduplicationJobs[0].CandidateVersions = []DeduplicationCandidateVersion{
				{CandidateID: "candidate", Version: 0},
			}
		},
		"candidate duplicate": func(state *RepositoryState) {
			state.DeduplicationJobs[0].CandidateVersions = []DeduplicationCandidateVersion{
				{CandidateID: "candidate", Version: 1},
				{CandidateID: "candidate", Version: 1},
			}
		},
		"shortlist oversized": func(state *RepositoryState) {
			state.DeduplicationJobs[0].ShortlistedScores = make(
				[]DeduplicationCandidateScore, DeduplicationMaximumShortlist+1,
			)
			for index := range state.DeduplicationJobs[0].ShortlistedScores {
				state.DeduplicationJobs[0].ShortlistedScores[index] = DeduplicationCandidateScore{
					CandidateID: fmt.Sprintf("candidate-%d", index), Score: 90, Explanation: "same",
				}
			}
		},
		"shortlist invalid": func(state *RepositoryState) {
			state.DeduplicationJobs[0].ShortlistedScores = []DeduplicationCandidateScore{
				{CandidateID: "candidate", Score: 101, Explanation: "same"},
			}
		},
		"shortlist duplicate": func(state *RepositoryState) {
			state.DeduplicationJobs[0].ShortlistedScores = []DeduplicationCandidateScore{
				{CandidateID: "candidate", Score: 90, Explanation: "same"},
				{CandidateID: "candidate", Score: 91, Explanation: "same"},
			}
		},
		"job history": func(state *RepositoryState) {
			state.DeduplicationJobs[0].History = append(
				state.DeduplicationJobs[0].History,
				DeduplicationJobHistoryEntry{State: "bad", At: repositoryAuditTestNow},
			)
		},
		"job count": func(state *RepositoryState) {
			state.DeduplicationJobs = nil
		},
		"raw target": func(state *RepositoryState) {
			state.RawFindings[0].DeduplicatedFindingID = "missing"
		},
		"counters": func(state *RepositoryState) {
			state.FindingsProcessing.Completed++
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			state := dedupDeepCloneState(t, base)
			mutate(&state)
			if err := validateDeduplicationState(state); err == nil {
				t.Fatal("corrupt deduplication state accepted")
			}
		})
	}
	ordinal := dedupDeepCloneState(t, base)
	ordinal.DeduplicationJobs[0].InsertionOrdinal++
	ordinal.Findings[0].CreationOrdinal = ordinal.DeduplicationJobs[0].InsertionOrdinal
	ordinal.NextDeduplicationOrdinal = ordinal.Findings[0].CreationOrdinal + 1
	if err := validateDeduplicationState(ordinal); err != nil {
		t.Fatalf("retried creation ordinal rejected: %v", err)
	}
}

func TestDeduplicationDeepReconcileCoverage(t *testing.T) {
	fixture := dedupDeepPendingFixture(t, 2)
	state := dedupDeepState(t, fixture)
	for _, job := range state.DeduplicationJobs {
		claimState, _, claimSucceeded, claimErr := fixture.store.ClaimDeduplicationJob(
			fixture.repository,
			job.ID,
			time.Minute,
		)
		if claimErr != nil || claimState.Repository == "" || !claimSucceeded {
			t.Fatalf("claim %s=%v %v", job.ID, claimSucceeded, claimErr)
		}
	}
	state = dedupDeepState(t, fixture)
	state.DeduplicationJobs[0].Attempts = 1
	state.DeduplicationJobs[1].Attempts = DeduplicationAttemptLimit
	state.Version++
	if err := fixture.store.save(&state); err != nil {
		t.Fatal(err)
	}
	runningState := state
	reset, err := fixture.store.ReconcileDeduplicationJobs(nil)
	if err != nil || reset != 2 {
		t.Fatalf("reconcile reset=%d err=%v", reset, err)
	}
	state = dedupDeepState(t, fixture)
	if state.DeduplicationJobs[0].State != DeduplicationJobPending ||
		state.DeduplicationJobs[1].State != DeduplicationJobFailed {
		t.Fatalf("reconciled jobs=%#v", state.DeduplicationJobs)
	}
	if reset, err = fixture.store.ReconcileDeduplicationJobs(t.Context()); err != nil || reset != 0 {
		t.Fatalf("idempotent reconcile=%d %v", reset, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.store.ReconcileDeduplicationJobs(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reconciliation error=%v", err)
	}

	missingRaw := runningState
	missingRaw.RawFindings = nil
	broken := NewStore(t.TempDir())
	broken.loadForTest = func(string) (RepositoryState, error) { return missingRaw, nil }
	if _, err := broken.reconcileRepositoryDeduplicationJobs(fixture.repository); err == nil {
		t.Fatal("running reconciliation without raw accepted")
	}
	loadFailure := NewStore(t.TempDir())
	loadFailure.loadForTest = func(string) (RepositoryState, error) { return RepositoryState{}, errors.New("load failed") }
	if _, err := loadFailure.reconcileRepositoryDeduplicationJobs(fixture.repository); err == nil {
		t.Fatal("reconcile load failure ignored")
	}

	workspace := t.TempDir()
	unsafeStore := NewStore(workspace)
	if err := os.Symlink(t.TempDir(), filepath.Join(workspace, storeDirectory)); err != nil {
		t.Fatal(err)
	}
	if _, err := unsafeStore.ReconcileDeduplicationJobs(t.Context()); err == nil {
		t.Fatal("unsafe reconciliation root accepted")
	}
}

func TestDeduplicationDeepHistoryAndPredicateCoverage(t *testing.T) {
	now := repositoryAuditTestNow
	rawHistory := make([]RawFindingHistoryEntry, DeduplicationHistoryLimit)
	jobHistory := make([]DeduplicationJobHistoryEntry, DeduplicationHistoryLimit)
	dedupHistory := make([]DeduplicatedFindingHistoryEntry, DeduplicationHistoryLimit)
	if got := appendRawFindingHistory(rawHistory, RawFindingHistoryEntry{At: now}); len(
		got,
	) != DeduplicationHistoryLimit {
		t.Fatalf("raw history len=%d", len(got))
	}
	if got := appendDeduplicationJobHistory(jobHistory, DeduplicationJobHistoryEntry{At: now}); len(
		got,
	) != DeduplicationHistoryLimit {
		t.Fatalf("job history len=%d", len(got))
	}
	if got := appendDeduplicatedFindingHistory(dedupHistory, DeduplicatedFindingHistoryEntry{At: now}); len(
		got,
	) != DeduplicationHistoryLimit {
		t.Fatalf("dedup history len=%d", len(got))
	}
	for _, code := range []string{"candidate_universe_changed", "processing_interrupted", "lease_expired", "attempt_limit", "other"} {
		failure := safeDeduplicationFailure(code, false, now)
		if failure.Code != code || failure.Message == "" || failure.Retryable {
			t.Fatalf("safe failure=%#v", failure)
		}
	}
	if validRawFindingState(RawReviewFinding{State: "bad"}) ||
		validRawFindingHistoryState("bad", RawFindingDispositionUndecided) ||
		validDeduplicationJobState(DeduplicationJob{State: "bad"}) ||
		validDeduplicationJobHistoryState("bad") {
		t.Fatal("invalid enum accepted")
	}
	if deduplicationJobIndexByRawID(nil, "missing") != -1 || deduplicationJobIndexByID(nil, "missing") != -1 {
		t.Fatal("missing job index found")
	}
	if !deduplicationCandidateVersionsMatch(nil, nil) || deduplicationCandidateVersionsMatch(
		[]DeduplicationCandidateVersion{{CandidateID: "a", Version: 1}}, nil,
	) || deduplicationCandidateVersionsMatch(
		[]DeduplicationCandidateVersion{{CandidateID: "wrong", Version: 1}},
		[]DeduplicationCandidateSnapshot{{ID: "a", Version: 1}},
	) {
		t.Fatal("candidate-version predicate mismatch")
	}
	if !reflect.DeepEqual(
		cloneRepositoryReviewDeduplicationSnapshot(nil),
		(*RepositoryReviewDeduplicationSnapshot)(nil),
	) {
		t.Fatal("nil snapshot clone changed")
	}
}
