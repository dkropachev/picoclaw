package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
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
	if requests, ordered, err := PrepareDeduplicationScoringRequests(diagnosis, nil, 0); err != nil || len(requests) != 0 || len(ordered) != 0 {
		t.Fatalf("empty scoring universe=%#v %#v %v", requests, ordered, err)
	}
	if _, _, err := PrepareDeduplicationScoringRequests(diagnosis, []DeduplicationCandidateSnapshot{{ID: "bad"}}, 0); err == nil {
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
	if _, err := EvaluateDeduplicationCandidates(t.Context(), oversizedSnapshot, diagnosis, many, len(encodedFirst)+8,
		func(_ context.Context, _ RepositoryReviewDeduplicationSnapshot, _ string, request DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
			response := DeduplicationScoringResponse{}
			for _, supplied := range request.Candidates {
				response.Scores = append(response.Scores, DeduplicationCandidateScore{CandidateID: supplied.ID, Score: 100, Explanation: "same"})
			}
			return response, nil
		},
		func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationJudgeRequest) (DeduplicationJudgment, error) {
			return DeduplicationJudgment{Decision: "new"}, nil
		}); err == nil {
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
	lockPath := filepath.Join(unsafeWorkspace, storeDirectory) + ".deduplication-slot-00.lock"
	if err := os.Symlink(filepath.Join(unsafeWorkspace, "missing"), lockPath); err != nil {
		t.Fatal(err)
	}
	if _, err := unsafeStore.AcquireDeduplicationSlot(t.Context()); err == nil {
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
	request := DeduplicationScoringRequest{Finding: diagnosis, Candidates: []DeduplicationScoringCandidate{{ID: "a", Diagnosis: diagnosis}, {ID: "a", Diagnosis: diagnosis}}}
	if err := ValidateDeduplicationScoringResponse(DeduplicationScoringResponse{Scores: []DeduplicationCandidateScore{{CandidateID: "a", Score: 90, Explanation: "one"}, {CandidateID: "a", Score: 90, Explanation: "two"}}}, request); err == nil {
		t.Fatal("duplicate request IDs accepted")
	}
	request.Candidates[0].ID = ""
	if err := ValidateDeduplicationScoringResponse(DeduplicationScoringResponse{Scores: make([]DeduplicationCandidateScore, 2)}, request); err == nil {
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
	if _, err := ShortlistDeduplicationCandidates(missingOpaque, make([]DeduplicationCandidateScore, 2), 90, 2); err == nil {
		t.Fatal("missing opaque candidate accepted")
	}
	duplicateOpaque := append([]DeduplicationCandidateSnapshot(nil), candidates...)
	duplicateOpaque[0].OpaqueID = duplicateOpaque[1].OpaqueID
	if _, err := ShortlistDeduplicationCandidates(duplicateOpaque, make([]DeduplicationCandidateScore, 2), 90, 2); err == nil {
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
	if _, err := DecodeDeduplicationJudgment([]byte(strings.Repeat("x", DeduplicationMaximumInputBytes+1))); err == nil {
		t.Fatal("oversized model response accepted")
	}
	if _, err := normalizeDeduplicationCandidateSnapshots([]DeduplicationCandidateSnapshot{deduplicationModelTestCandidate(1, 1, "x"), deduplicationModelTestCandidate(1, 2, "x")}); err == nil {
		t.Fatal("duplicate canonical candidate accepted")
	}
	if _, err := ShortlistDeduplicationCandidates([]DeduplicationCandidateSnapshot{{ID: "bad"}}, nil, 90, 1); err == nil {
		t.Fatal("invalid canonical shortlist candidate accepted")
	}
	ordinalCandidates := []DeduplicationCandidateSnapshot{
		{ID: "a", Version: 1, CreationOrdinal: 2, OpaqueID: "one", Diagnosis: diagnosis},
		{ID: "z", Version: 1, CreationOrdinal: 1, OpaqueID: "two", Diagnosis: diagnosis},
	}
	ordinalScores := []DeduplicationCandidateScore{{CandidateID: "one", Score: 90, Explanation: "same"}, {CandidateID: "two", Score: 90, Explanation: "same"}}
	if got, err := ShortlistDeduplicationCandidates(ordinalCandidates, ordinalScores, 90, 2); err != nil || got[0].ID != "z" {
		t.Fatalf("shortlist ordinal ordering=%#v err=%v", got, err)
	}
}

func TestDeduplicationDeepStoreInjectionCoverage(t *testing.T) {
	repository := "owner/injected"
	loadErr := errors.New("load failed")
	loadFailure := NewStore(t.TempDir())
	loadFailure.loadForTest = func(string) (RepositoryState, error) { return RepositoryState{}, loadErr }
	completion := DeduplicationCompletion{JobID: "job", LeaseID: "lease", CandidateUniverseDigest: "digest", Decision: DeduplicationJudgment{Decision: "new"}}
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
	if _, err := loadFailure.ProcessPendingDeduplicationJobs(t.Context(), repository, DeduplicationProcessOptions{}); !errors.Is(err, loadErr) {
		t.Fatalf("process load error=%v", err)
	}

	malformed := RepositoryState{Repository: repository, DeduplicationJobs: []DeduplicationJob{{ID: "job", RawFindingID: "missing"}}}
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
}

func TestDeduplicationDeepClaimAndCompletionCoverage(t *testing.T) {
	if _, _, _, err := (Store{}).ClaimDeduplicationJob("", "", -time.Second); err == nil {
		t.Fatal("invalid claim accepted")
	}
	fixture := dedupDeepSameBucketFixture(t)
	state := dedupDeepState(t, fixture)
	if _, _, _, err := fixture.store.ClaimDeduplicationJob(fixture.repository, "missing", time.Minute); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing claim error=%v", err)
	}
	firstJob, secondJob := state.DeduplicationJobs[0], state.DeduplicationJobs[1]
	if _, claim, claimed, err := fixture.store.ClaimDeduplicationJob(fixture.repository, secondJob.ID, time.Minute); err != nil || claimed || claim.Job.State != DeduplicationJobPending {
		t.Fatalf("FIFO claim=%#v claimed=%v err=%v", claim, claimed, err)
	}
	_, firstClaim, claimed, err := fixture.store.ClaimDeduplicationJob(fixture.repository, firstJob.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("first claim=%#v claimed=%v err=%v", firstClaim, claimed, err)
	}
	if _, running, claimed, err := fixture.store.ClaimDeduplicationJob(fixture.repository, firstJob.ID, time.Minute); err != nil || claimed || running.Job.State != DeduplicationJobRunning {
		t.Fatalf("running claim=%#v claimed=%v err=%v", running, claimed, err)
	}
	fixture.store.now = func() time.Time { return firstClaim.Job.LeaseExpiresAt.Add(time.Second) }
	_, reclaimed, claimed, err := fixture.store.ClaimDeduplicationJob(fixture.repository, firstJob.ID, time.Minute)
	if err != nil || !claimed || reclaimed.Job.Attempts != 2 {
		t.Fatalf("expired reclaim=%#v claimed=%v err=%v", reclaimed, claimed, err)
	}

	if _, _, _, err := fixture.store.CompleteDeduplicationJob("", DeduplicationCompletion{}); err == nil {
		t.Fatal("invalid completion accepted")
	}
	if _, _, _, err := fixture.store.CompleteDeduplicationJob(fixture.repository, DeduplicationCompletion{
		JobID: "missing", LeaseID: "lease", CandidateUniverseDigest: "digest",
		Decision: DeduplicationJudgment{Decision: "new"},
	}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing completion error=%v", err)
	}
	fixture.store.now = func() time.Time { return reclaimed.Job.LeaseExpiresAt.Add(time.Second) }
	if _, _, _, err := fixture.store.CompleteDeduplicationJob(fixture.repository, DeduplicationCompletion{
		JobID: reclaimed.Job.ID, LeaseID: reclaimed.Job.LeaseID,
		CandidateUniverseDigest: reclaimed.UniverseDigest,
		Decision:                DeduplicationJudgment{Decision: "new"},
	}); !errors.Is(err, ErrDeduplicationLeaseExpired) {
		t.Fatalf("expired completion error=%v", err)
	}

	// Restore a clock inside the reclaimed lease and complete it as new.
	fixture.store.now = func() time.Time { return reclaimed.Job.UpdatedAt.Add(time.Second) }
	completion := DeduplicationCompletion{
		JobID: reclaimed.Job.ID, LeaseID: reclaimed.Job.LeaseID,
		CandidateUniverseDigest: reclaimed.UniverseDigest,
		Decision:                DeduplicationJudgment{Decision: "new"},
	}
	completedState, target, created, err := fixture.store.CompleteDeduplicationJob(fixture.repository, completion)
	if err != nil || !created {
		t.Fatalf("new completion target=%#v created=%v err=%v", target, created, err)
	}
	if _, replayed, replayCreated, err := fixture.store.CompleteDeduplicationJob(fixture.repository, completion); err != nil || !replayCreated || replayed.ID != target.ID {
		t.Fatalf("idempotent completion=%#v created=%v err=%v", replayed, replayCreated, err)
	}
	wrong := completion
	wrong.Decision = DeduplicationJudgment{Decision: "duplicate", CandidateID: "other"}
	if _, _, _, err := fixture.store.CompleteDeduplicationJob(fixture.repository, wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
	if len(completedState.DeduplicatedFindings) != 1 {
		t.Fatalf("completed state=%#v", completedState.DeduplicatedFindings)
	}

	// The second same-bucket raw now snapshots the created candidate.
	_, duplicateClaim, claimed, err := fixture.store.ClaimDeduplicationJob(fixture.repository, secondJob.ID, time.Minute)
	if err != nil || !claimed || len(duplicateClaim.Candidates) != 1 {
		t.Fatalf("duplicate claim=%#v claimed=%v err=%v", duplicateClaim, claimed, err)
	}
	baseDuplicate := DeduplicationCompletion{
		JobID: duplicateClaim.Job.ID, LeaseID: duplicateClaim.Job.LeaseID,
		CandidateUniverseDigest: duplicateClaim.UniverseDigest,
		Decision:                DeduplicationJudgment{Decision: "duplicate", CandidateID: target.ID},
	}
	if _, _, _, err := fixture.store.CompleteDeduplicationJob(fixture.repository, baseDuplicate); err == nil {
		t.Fatal("duplicate outside shortlist accepted")
	}
	baseDuplicate.ShortlistedScores = []DeduplicationCandidateScore{{
		CandidateID: target.ID, Score: 100, Explanation: "same defect",
	}}
	if _, duplicate, created, err := fixture.store.CompleteDeduplicationJob(fixture.repository, baseDuplicate); err != nil || created || len(duplicate.RawSourceIDs) != 2 {
		t.Fatalf("duplicate completion=%#v created=%v err=%v", duplicate, created, err)
	}
}

func TestDeduplicationDeepDurableScoreCoverage(t *testing.T) {
	candidates := []DeduplicationCandidateSnapshot{
		{ID: "b", Version: 1, CreationOrdinal: 2},
		{ID: "a", Version: 1, CreationOrdinal: 1},
		{ID: "c", Version: 1, CreationOrdinal: 1},
	}
	job := &DeduplicationJob{ModelSnapshot: RepositoryReviewDeduplicationSnapshot{SimilarityThreshold: 90, CandidateLimit: 3}}
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
	if _, _, _, err := (Store{}).FailDeduplicationJob("", "", "", errors.New("x")); err == nil {
		t.Fatal("invalid failure release accepted")
	}
	fixture := dedupDeepPendingFixture(t, 1)
	state := dedupDeepState(t, fixture)
	if _, _, _, err := fixture.store.FailDeduplicationJob(fixture.repository, "missing", "lease", errors.New("x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing failure job error=%v", err)
	}
	job := state.DeduplicationJobs[0]
	_, claim, claimed, err := fixture.store.ClaimDeduplicationJob(fixture.repository, job.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.store.FailDeduplicationJob(fixture.repository, job.ID, "wrong", errors.New("x")); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong lease error=%v", err)
	}
	if _, raw, terminal, err := fixture.store.FailDeduplicationJob(fixture.repository, job.ID, claim.Job.LeaseID, ErrDeduplicationUniverseChanged); err != nil || terminal || raw.State != RawFindingDeduplicationPending || raw.History[len(raw.History)-1].Failure.Code != "candidate_universe_changed" {
		t.Fatalf("universe failure raw=%#v terminal=%v err=%v", raw, terminal, err)
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
			_, localClaim, claimed, err := local.store.ClaimDeduplicationJob(local.repository, localState.DeduplicationJobs[0].ID, time.Minute)
			if err != nil || !claimed {
				t.Fatal(err)
			}
			if _, _, terminal, err := local.store.FailDeduplicationJob(local.repository, localClaim.Job.ID, localClaim.Job.LeaseID, cause); err != nil || terminal {
				t.Fatalf("failure terminal=%v err=%v", terminal, err)
			}
		})
	}

	terminalFixture := dedupDeepPendingFixture(t, 1)
	terminalState := dedupDeepState(t, terminalFixture)
	_, terminalClaim, claimed, err := terminalFixture.store.ClaimDeduplicationJob(terminalFixture.repository, terminalState.DeduplicationJobs[0].ID, time.Minute)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	terminalState = dedupDeepState(t, terminalFixture)
	terminalState.DeduplicationJobs[0].Attempts = DeduplicationAttemptLimit
	terminalState.Version++
	if err := terminalFixture.store.save(&terminalState); err != nil {
		t.Fatal(err)
	}
	failedState, failedRaw, terminal, err := terminalFixture.store.FailDeduplicationJob(
		terminalFixture.repository, terminalClaim.Job.ID, terminalClaim.Job.LeaseID, errors.New("final"),
	)
	if err != nil || !terminal || failedRaw.State != RawFindingDeduplicationFailed {
		t.Fatalf("terminal raw=%#v terminal=%v err=%v", failedRaw, terminal, err)
	}
	if _, _, err := terminalFixture.store.RetryDeduplication("", ""); err == nil {
		t.Fatal("invalid retry accepted")
	}
	if _, _, err := terminalFixture.store.RetryDeduplication(terminalFixture.repository, "missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing retry error=%v", err)
	}
	if _, _, err := terminalFixture.store.RetryDeduplication(terminalFixture.repository, failedRaw.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := terminalFixture.store.RetryDeduplication(terminalFixture.repository, failedRaw.ID); !errors.Is(err, ErrConflict) {
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
	if _, _, _, err := broken.FailDeduplicationJob(terminalFixture.repository, terminalClaim.Job.ID, terminalClaim.Job.LeaseID, errors.New("x")); err == nil {
		t.Fatal("failure release without raw accepted")
	}
}

func TestDeduplicationDeepProcessorFailureCoverage(t *testing.T) {
	missing := NewStore(t.TempDir())
	if _, err := missing.ProcessPendingDeduplicationJobs(t.Context(), "owner/missing", DeduplicationProcessOptions{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing processor error=%v", err)
	}
	invalidLease := dedupDeepPendingFixture(t, 1)
	if _, err := invalidLease.store.ProcessPendingDeduplicationJobs(t.Context(), invalidLease.repository, DeduplicationProcessOptions{LeaseDuration: DeduplicationMaximumLeaseDuration + time.Second}); err == nil {
		t.Fatal("invalid processor lease accepted")
	}

	fixture := dedupDeepSameBucketFixture(t)
	state := dedupDeepState(t, fixture)
	_, seedClaim, claimed, err := fixture.store.ClaimDeduplicationJob(fixture.repository, state.DeduplicationJobs[0].ID, time.Minute)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	if _, _, _, err := fixture.store.CompleteDeduplicationJob(fixture.repository, DeduplicationCompletion{
		JobID: seedClaim.Job.ID, LeaseID: seedClaim.Job.LeaseID,
		CandidateUniverseDigest: seedClaim.UniverseDigest,
		Decision:                DeduplicationJudgment{Decision: "new"},
	}); err != nil {
		t.Fatal(err)
	}
	releases := make([]func(), 0, DeduplicationConcurrency)
	for range DeduplicationConcurrency {
		release, err := fixture.store.AcquireDeduplicationSlot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	state = dedupDeepState(t, fixture)
	secondJobID := state.DeduplicationJobs[1].ID
	blockedOutcome := fixture.store.processOneDeduplicationJob(canceled, fixture.repository, secondJobID, DeduplicationProcessOptions{})
	for _, release := range releases {
		release()
	}
	if blockedOutcome.err == nil || blockedOutcome.created || blockedOutcome.duplicate {
		t.Fatalf("canceled processor outcome=%#v", blockedOutcome)
	}
	processed := DeduplicationProcessResult{}

	// A model callback changes the candidate universe after claim. Completion
	// fails and the verified lease is safely returned to pending.
	processed, err = fixture.store.ProcessPendingDeduplicationJobs(t.Context(), fixture.repository, DeduplicationProcessOptions{
		Score: func(_ context.Context, _ RepositoryReviewDeduplicationSnapshot, _ string, request DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
			current := dedupDeepState(t, fixture)
			current.DeduplicatedFindings[0].Version++
			current.Version++
			if saveErr := fixture.store.save(&current); saveErr != nil {
				t.Fatal(saveErr)
			}
			return DeduplicationScoringResponse{Scores: []DeduplicationCandidateScore{{CandidateID: request.Candidates[0].ID, Score: 100, Explanation: "same"}}}, nil
		},
		Judge: func(_ context.Context, _ RepositoryReviewDeduplicationSnapshot, _ string, request DeduplicationJudgeRequest) (DeduplicationJudgment, error) {
			return DeduplicationJudgment{Decision: "duplicate", CandidateID: request.Candidates[0].OpaqueID}, nil
		},
	})
	if err == nil || processed.Completed != 0 {
		t.Fatalf("stale processor=%#v err=%v", processed, err)
	}

	// Third attempt fails terminally through the scorer-error path.
	providerOptions := DeduplicationProcessOptions{Score: func(context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationScoringRequest) (DeduplicationScoringResponse, error) {
		return DeduplicationScoringResponse{}, errors.New("provider failed")
	}}
	for attempt := 0; attempt < DeduplicationAttemptLimit && processed.Failed == 0; attempt++ {
		processed, err = fixture.store.ProcessPendingDeduplicationJobs(t.Context(), fixture.repository, providerOptions)
	}
	if err == nil || processed.Failed != 1 {
		t.Fatalf("terminal processor=%#v err=%v", processed, err)
	}
	if empty, err := fixture.store.ProcessPendingDeduplicationJobs(t.Context(), fixture.repository, DeduplicationProcessOptions{}); err != nil || empty != (DeduplicationProcessResult{}) {
		t.Fatalf("empty processor=%#v err=%v", empty, err)
	}

	deferredFixture := dedupDeepSameBucketFixture(t)
	deferredState := dedupDeepState(t, deferredFixture)
	if _, _, claimed, err := deferredFixture.store.ClaimDeduplicationJob(deferredFixture.repository, deferredState.DeduplicationJobs[0].ID, time.Minute); err != nil || !claimed {
		t.Fatal(err)
	}
	if deferred, err := deferredFixture.store.ProcessPendingDeduplicationJobs(t.Context(), deferredFixture.repository, DeduplicationProcessOptions{}); err != nil || deferred.Deferred != 1 {
		t.Fatalf("deferred processor=%#v err=%v", deferred, err)
	}
}

func TestDeduplicationDeepStateValidationCoverage(t *testing.T) {
	fixture := dedupDeepPendingFixture(t, 1)
	if _, err := fixture.store.ProcessPendingDeduplicationJobs(t.Context(), fixture.repository, DeduplicationProcessOptions{}); err != nil {
		t.Fatal(err)
	}
	base := dedupDeepState(t, fixture)
	if err := validateDeduplicationState(base); err != nil {
		t.Fatalf("valid baseline: %v", err)
	}
	pendingProjectionIndex := -1
	for index := range base.Findings {
		if base.Findings[index].DeduplicationPending {
			pendingProjectionIndex = index
			break
		}
	}
	if pendingProjectionIndex < 0 {
		base.Findings = append(base.Findings, Finding{
			ID: "pending-projection", CampaignID: base.RawFindings[0].CampaignID,
			Repository: base.Repository, DeduplicationPending: true,
			RawFindingIDs: []string{base.RawFindings[0].ID},
		})
		pendingProjectionIndex = len(base.Findings) - 1
	}
	mutations := map[string]func(*RepositoryState){
		"raw diagnosis": func(state *RepositoryState) {
			state.RawFindings[0].Severity = ""
			state.RawFindings[0].DiagnosisDigest = RawReviewFindingDiagnosisDigest(state.RawFindings[0])
		},
		"raw history": func(state *RepositoryState) {
			state.RawFindings[0].History = append(state.RawFindings[0].History, RawFindingHistoryEntry{
				State: "bad", Disposition: RawFindingDispositionUndecided, At: repositoryAuditTestNow,
			})
		},
		"pending empty": func(state *RepositoryState) {
			state.Findings[pendingProjectionIndex].RawFindingIDs = nil
		},
		"pending missing": func(state *RepositoryState) {
			state.Findings[pendingProjectionIndex].RawFindingIDs = []string{"missing"}
		},
		"pending duplicate": func(state *RepositoryState) {
			id := state.RawFindings[0].ID
			state.Findings[pendingProjectionIndex].RawFindingIDs = []string{id, id}
		},
		"dedup missing source": func(state *RepositoryState) {
			state.DeduplicatedFindings[0].RawSourceIDs = append(state.DeduplicatedFindings[0].RawSourceIDs, "missing")
		},
		"dedup duplicate source": func(state *RepositoryState) {
			id := state.RawFindings[0].ID
			state.DeduplicatedFindings[0].RawSourceIDs = []string{id, id}
		},
		"dedup rewrite": func(state *RepositoryState) {
			state.DeduplicatedFindings[0].Title = "rewritten"
		},
		"dedup history": func(state *RepositoryState) {
			state.DeduplicatedFindings[0].History = append(state.DeduplicatedFindings[0].History, DeduplicatedFindingHistoryEntry{At: repositoryAuditTestNow})
		},
		"job state": func(state *RepositoryState) {
			state.DeduplicationJobs[0].State = "bad"
		},
		"candidate version": func(state *RepositoryState) {
			state.DeduplicationJobs[0].CandidateVersions = []DeduplicationCandidateVersion{{CandidateID: "candidate", Version: 0}}
		},
		"candidate duplicate": func(state *RepositoryState) {
			state.DeduplicationJobs[0].CandidateVersions = []DeduplicationCandidateVersion{{CandidateID: "candidate", Version: 1}, {CandidateID: "candidate", Version: 1}}
		},
		"shortlist oversized": func(state *RepositoryState) {
			state.DeduplicationJobs[0].ShortlistedScores = make([]DeduplicationCandidateScore, DeduplicationMaximumShortlist+1)
			for index := range state.DeduplicationJobs[0].ShortlistedScores {
				state.DeduplicationJobs[0].ShortlistedScores[index] = DeduplicationCandidateScore{CandidateID: "candidate-" + string(rune('a'+index)), Score: 90, Explanation: "same"}
			}
		},
		"shortlist invalid": func(state *RepositoryState) {
			state.DeduplicationJobs[0].ShortlistedScores = []DeduplicationCandidateScore{{CandidateID: "candidate", Score: 101, Explanation: "same"}}
		},
		"shortlist duplicate": func(state *RepositoryState) {
			state.DeduplicationJobs[0].ShortlistedScores = []DeduplicationCandidateScore{{CandidateID: "candidate", Score: 90, Explanation: "same"}, {CandidateID: "candidate", Score: 91, Explanation: "same"}}
		},
		"job history": func(state *RepositoryState) {
			state.DeduplicationJobs[0].History = append(state.DeduplicationJobs[0].History, DeduplicationJobHistoryEntry{State: "bad", At: repositoryAuditTestNow})
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
	ordinal.DeduplicatedFindings[0].CreationOrdinal = ordinal.RawFindings[0].InsertionOrdinal + 1
	ordinal.NextDeduplicationOrdinal = ordinal.DeduplicatedFindings[0].CreationOrdinal + 1
	if err := validateDeduplicationState(ordinal); err != nil {
		t.Fatalf("higher deduplicated ordinal rejected: %v", err)
	}
}

func TestDeduplicationDeepProjectionAndCounterCoverage(t *testing.T) {
	if synchronizeDeduplicatedFindingProjections(nil) {
		t.Fatal("nil projection state changed")
	}
	state := RepositoryState{
		UpdatedAt: repositoryAuditTestNow,
		DeduplicatedFindings: []DeduplicatedReviewFinding{{
			ID: "dedup", Version: 1, Status: FindingOpen, UpdatedAt: repositoryAuditTestNow,
		}},
		Findings: []Finding{{ID: "other"}},
	}
	if synchronizeDeduplicatedFindingProjections(&state) {
		t.Fatal("missing projection changed deduplicated finding")
	}
	state.Findings = append(state.Findings, Finding{
		ID: "dedup", Status: FindingDismissed, RepositoryFindingID: "repository-target",
		RepositoryMatchState: RepositoryMatchKnown,
	})
	if !synchronizeDeduplicatedFindingProjections(&state) || len(state.DeduplicatedFindings[0].History) != 1 || state.DeduplicatedFindings[0].History[0].At != state.UpdatedAt {
		t.Fatalf("projection synchronization=%#v", state.DeduplicatedFindings[0])
	}
	counters := RepositoryState{UpdatedAt: repositoryAuditTestNow, DeduplicatedFindings: []DeduplicatedReviewFinding{{CreationOrdinal: 9}}}
	if !reconcileFindingsProcessingCounters(&counters) || counters.NextDeduplicationOrdinal != 10 {
		t.Fatalf("ordinal counters=%#v", counters)
	}
}

func TestDeduplicationDeepLegacyRecordCoverage(t *testing.T) {
	repository := "owner/legacy-deep"
	plan := Plan{
		Repository: repository, CampaignID: "campaign", CommitSHA: strings.Repeat("a", 40),
		TargetBranch: "main", AdvertisedDefaultBranch: "main", TargetIsDefault: true,
	}
	file := repositoryAuditTestFile("pkg/legacy.go", "a", 10)
	candidate := repositoryReviewCampaignFinding(file, "legacy deep")
	contextRecord := FindingContext{ID: "context", Repository: repository}
	observation := Observation{Model: "review", Reviewer: "reviewer"}
	if _, err := persistLegacyRecordFinding(&RepositoryState{}, plan, "run", 0, 0, contextRecord, observation, FileRef{}, candidate, repositoryAuditTestNow); err == nil {
		t.Fatal("legacy record with invalid primary accepted")
	}
	state := RepositoryState{Repository: repository, CurrentCampaign: &RepositoryReviewCampaignCoverage{
		ID: plan.CampaignID,
		DeduplicationSnapshot: &RepositoryReviewDeduplicationSnapshot{
			ReviewerModel: "review", DeduplicationModel: "dedup", AccountRef: "account",
			SimilarityThreshold: 88, CandidateLimit: 7,
		},
	}}
	id, err := persistLegacyRecordFinding(&state, plan, "run", 0, 0, contextRecord, observation, file, candidate, repositoryAuditTestNow)
	if err != nil || id == "" || state.DeduplicationJobs[0].ModelSnapshot.CandidateLimit != 0 || state.DeduplicationJobs[0].ModelSnapshot.SimilarityThreshold != 88 {
		t.Fatalf("legacy record id=%q state=%#v err=%v", id, state, err)
	}
	if _, err := persistLegacyRecordFinding(&state, plan, "run", 0, 0, contextRecord, observation, file, candidate, repositoryAuditTestNow); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate legacy record error=%v", err)
	}
}

func TestDeduplicationDeepReconcileCoverage(t *testing.T) {
	fixture := dedupDeepPendingFixture(t, 2)
	state := dedupDeepState(t, fixture)
	for _, job := range state.DeduplicationJobs {
		if _, _, claimed, err := fixture.store.ClaimDeduplicationJob(fixture.repository, job.ID, time.Minute); err != nil || !claimed {
			t.Fatalf("claim %s=%v %v", job.ID, claimed, err)
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
	if state.DeduplicationJobs[0].State != DeduplicationJobPending || state.DeduplicationJobs[1].State != DeduplicationJobFailed {
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
	if got := appendRawFindingHistory(rawHistory, RawFindingHistoryEntry{At: now}); len(got) != DeduplicationHistoryLimit {
		t.Fatalf("raw history len=%d", len(got))
	}
	if got := appendDeduplicationJobHistory(jobHistory, DeduplicationJobHistoryEntry{At: now}); len(got) != DeduplicationHistoryLimit {
		t.Fatalf("job history len=%d", len(got))
	}
	if got := appendDeduplicatedFindingHistory(dedupHistory, DeduplicatedFindingHistoryEntry{At: now}); len(got) != DeduplicationHistoryLimit {
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
	if !reflect.DeepEqual(cloneRepositoryReviewDeduplicationSnapshot(nil), (*RepositoryReviewDeduplicationSnapshot)(nil)) {
		t.Fatal("nil snapshot clone changed")
	}
}
