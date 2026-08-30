package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAcquireDeduplicationSlotEnforcesWorkspaceLimit(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	releases := make([]func(), 0, DeduplicationConcurrency)
	for range DeduplicationConcurrency {
		release, err := store.AcquireDeduplicationSlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	blocked, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.AcquireDeduplicationSlot(blocked); !errors.Is(err, context.Canceled) {
		t.Fatalf("fifth deduplication slot error = %v, want canceled", err)
	}
	releases[0]()
	release, err := store.AcquireDeduplicationSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	for _, held := range releases[1:] {
		held()
	}
}

func TestNormalizeDeduplicationSymbol(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":                          "",
		"  (*Server)  :: ServeHTTP": "server.servehttp",
		"pkg///Type###Method":       "pkg.type.method",
		"Client -> Send":            "client.send",
		"Cache..Entry::Load":        "cache.entry.load",
		"(*queue).re-try":           "queue.re.try",
	}
	for input, want := range tests {
		if got := NormalizeDeduplicationSymbol(input); got != want {
			t.Errorf("NormalizeDeduplicationSymbol(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDeduplicationAdmissionBucketIgnoresLineAndSeparatesIdentity(t *testing.T) {
	t.Parallel()
	base := FileRef{Path: "pkg/cache.go", BlobSHA: strings.Repeat("a", 40)}
	bucket, err := DeduplicationAdmissionBucket("rrc_campaign", base, "(*Cache).Load")
	if err != nil {
		t.Fatal(err)
	}
	equivalent, err := DeduplicationAdmissionBucket("rrc_campaign", base, "cache::load")
	if err != nil || equivalent != bucket {
		t.Fatalf("equivalent symbol bucket = %q, %v; want %q", equivalent, err, bucket)
	}
	missing, err := DeduplicationAdmissionBucket("rrc_campaign", base, "")
	if err != nil || missing == "" {
		t.Fatalf("missing legacy symbol bucket = %q, %v", missing, err)
	}
	variants := []struct {
		campaign string
		file     FileRef
		symbol   string
	}{
		{"rrc_other", base, "cache.load"},
		{"rrc_campaign", FileRef{Path: "pkg/Cache.go", BlobSHA: base.BlobSHA}, "cache.load"},
		{"rrc_campaign", FileRef{Path: base.Path, BlobSHA: strings.Repeat("b", 40)}, "cache.load"},
		{"rrc_campaign", base, "cache.store"},
	}
	for _, variant := range variants {
		got, gotErr := DeduplicationAdmissionBucket(variant.campaign, variant.file, variant.symbol)
		if gotErr != nil {
			t.Fatal(gotErr)
		}
		if got == bucket {
			t.Fatalf("distinct admission identity produced bucket %q", got)
		}
	}
}

func TestPrepareDeduplicationScoringRequestsIsCompleteBoundedAndDeterministic(t *testing.T) {
	t.Parallel()
	candidates := make([]DeduplicationCandidateSnapshot, 33)
	for index := range candidates {
		candidates[index] = deduplicationModelTestCandidate(33-index, uint64(33-index), "candidate")
	}
	requests, ordered, err := PrepareDeduplicationScoringRequests(
		deduplicationModelTestDiagnosis("raw"), candidates, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 || len(requests[0].Candidates) != 16 ||
		len(requests[1].Candidates) != 16 || len(requests[2].Candidates) != 1 {
		t.Fatalf("unexpected scoring chunks: %#v", requests)
	}
	seen := make(map[string]bool, len(candidates))
	for _, request := range requests {
		encoded, marshalErr := json.Marshal(request)
		if marshalErr != nil || len(encoded) > DeduplicationMaximumInputBytes {
			t.Fatalf("request size = %d, error = %v", len(encoded), marshalErr)
		}
		for _, candidate := range request.Candidates {
			if seen[candidate.ID] {
				t.Fatalf("candidate %q scored twice", candidate.ID)
			}
			seen[candidate.ID] = true
		}
	}
	if len(seen) != 33 || ordered[0].ID != "dedup-001" || ordered[32].ID != "dedup-033" {
		t.Fatalf("incomplete or non-deterministic scoring: seen=%d ordered=%#v", len(seen), ordered)
	}
}

func TestPrepareDeduplicationScoringRequestsHonorsByteCeiling(t *testing.T) {
	t.Parallel()
	candidates := []DeduplicationCandidateSnapshot{
		deduplicationModelTestCandidate(1, 1, strings.Repeat("x", 300)),
		deduplicationModelTestCandidate(2, 2, strings.Repeat("y", 300)),
	}
	base, _, err := PrepareDeduplicationScoringRequests(
		deduplicationModelTestDiagnosis("raw"), candidates[:1], DeduplicationMaximumInputBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	oneBytes, _ := json.Marshal(base[0])
	requests, _, err := PrepareDeduplicationScoringRequests(
		deduplicationModelTestDiagnosis("raw"), candidates, len(oneBytes)+4,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("byte ceiling produced %d calls, want 2", len(requests))
	}
	if _, _, err := PrepareDeduplicationScoringRequests(
		deduplicationModelTestDiagnosis(strings.Repeat("z", 600)), candidates[:1], 128,
	); err == nil {
		t.Fatal("oversized individual scoring input was accepted")
	}
}

func TestDeduplicationScoringResponseValidationRejectsIncompleteAndDuplicateIDs(t *testing.T) {
	t.Parallel()
	request := DeduplicationScoringRequest{
		Finding: deduplicationModelTestDiagnosis("raw"),
		Candidates: []DeduplicationScoringCandidate{
			{ID: "candidate-000001", Diagnosis: deduplicationModelTestDiagnosis("one")},
			{ID: "candidate-000002", Diagnosis: deduplicationModelTestDiagnosis("two")},
		},
	}
	valid := DeduplicationScoringResponse{Scores: []DeduplicationCandidateScore{
		{CandidateID: "candidate-000001", Score: 90, Explanation: "Same mechanism."},
		{CandidateID: "candidate-000002", Score: 89, Explanation: "Ambiguous mechanism."},
	}}
	if err := ValidateDeduplicationScoringResponse(valid, request); err != nil {
		t.Fatal(err)
	}
	tests := []DeduplicationScoringResponse{
		{Scores: valid.Scores[:1]},
		{Scores: []DeduplicationCandidateScore{valid.Scores[0], valid.Scores[0]}},
		{Scores: []DeduplicationCandidateScore{valid.Scores[0], {CandidateID: "other", Score: 90, Explanation: "Other."}}},
		{Scores: []DeduplicationCandidateScore{valid.Scores[0], {CandidateID: "candidate-000002", Score: 101, Explanation: "Invalid."}}},
		{Scores: []DeduplicationCandidateScore{valid.Scores[0], {CandidateID: "candidate-000002", Score: 90}}},
	}
	for _, response := range tests {
		if err := ValidateDeduplicationScoringResponse(response, request); err == nil {
			t.Fatalf("invalid scoring response accepted: %#v", response)
		}
	}
}

func TestShortlistDeduplicationCandidatesThresholdOrderingAndLimits(t *testing.T) {
	t.Parallel()
	candidates := []DeduplicationCandidateSnapshot{
		deduplicationModelTestCandidate(3, 2, "third"),
		deduplicationModelTestCandidate(1, 1, "first"),
		deduplicationModelTestCandidate(2, 1, "second"),
	}
	_, ordered, err := PrepareDeduplicationScoringRequests(
		deduplicationModelTestDiagnosis("raw"), candidates, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	scores := []DeduplicationCandidateScore{
		{CandidateID: ordered[0].OpaqueID, Score: 90, Explanation: "At threshold."},
		{CandidateID: ordered[1].OpaqueID, Score: 90, Explanation: "Tie at threshold."},
		{CandidateID: ordered[2].OpaqueID, Score: 89, Explanation: "Below threshold."},
	}
	shortlist, err := ShortlistDeduplicationCandidates(ordered, scores, 90, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(shortlist) != 2 || shortlist[0].ID != "dedup-001" || shortlist[1].ID != "dedup-002" {
		t.Fatalf("89/90 filter or stable ordering failed: %#v", shortlist)
	}
	for _, limit := range []int{0, 1, 4, 20} {
		got, gotErr := ShortlistDeduplicationCandidates(ordered, scores, 0, limit)
		if gotErr != nil {
			t.Fatalf("limit %d: %v", limit, gotErr)
		}
		want := min(limit, len(candidates))
		if len(got) != want {
			t.Fatalf("limit %d produced %d candidates, want %d", limit, len(got), want)
		}
	}
}

func TestValidateDeduplicationJudgment(t *testing.T) {
	t.Parallel()
	candidates := []DeduplicationShortlistedCandidate{{OpaqueID: "candidate-000001"}}
	valid := []DeduplicationJudgment{
		{Decision: "new"},
		{Decision: "duplicate", CandidateID: "candidate-000001"},
	}
	for _, judgment := range valid {
		if err := ValidateDeduplicationJudgment(judgment, candidates); err != nil {
			t.Fatalf("valid judgment %#v: %v", judgment, err)
		}
	}
	invalid := []DeduplicationJudgment{
		{},
		{Decision: "same", CandidateID: "candidate-000001"},
		{Decision: "new", CandidateID: "candidate-000001"},
		{Decision: "duplicate"},
		{Decision: "duplicate", CandidateID: "candidate-999999"},
	}
	for _, judgment := range invalid {
		if err := ValidateDeduplicationJudgment(judgment, candidates); err == nil {
			t.Fatalf("invalid judgment accepted: %#v", judgment)
		}
	}
}

func TestPrepareDeduplicationJudgeRequestBoundsInput(t *testing.T) {
	t.Parallel()
	finding := deduplicationModelTestDiagnosis("raw")
	candidate := DeduplicationShortlistedCandidate{
		OpaqueID: "candidate-000001", Diagnosis: deduplicationModelTestDiagnosis("candidate"),
		Score: 95, Explanation: "Same mechanism.",
	}
	request, err := PrepareDeduplicationJudgeRequest(finding, []DeduplicationShortlistedCandidate{candidate}, 0)
	if err != nil || len(request.Candidates) != 1 {
		t.Fatalf("valid judge request = %#v, %v", request, err)
	}
	encoded, err := json.Marshal(request)
	if err != nil || strings.Contains(string(encoded), "score") ||
		strings.Contains(string(encoded), "explanation") {
		t.Fatalf("judge request leaked scorer metadata: %s err=%v", encoded, err)
	}
	if _, err := PrepareDeduplicationJudgeRequest(finding, nil, 0); err == nil {
		t.Fatal("empty judge request was accepted")
	}
	if _, err := PrepareDeduplicationJudgeRequest(
		finding, []DeduplicationShortlistedCandidate{candidate, candidate}, 0,
	); err == nil {
		t.Fatal("duplicate judge candidate ID was accepted")
	}
	if _, err := PrepareDeduplicationJudgeRequest(
		deduplicationModelTestDiagnosis(strings.Repeat("x", 1000)),
		[]DeduplicationShortlistedCandidate{candidate}, 128,
	); err == nil {
		t.Fatal("oversized judge request was accepted")
	}
}

func TestEvaluateDeduplicationCandidatesScoresAllChunksAndSkipsEmptyJudge(t *testing.T) {
	t.Parallel()
	candidates := make([]DeduplicationCandidateSnapshot, 17)
	for index := range candidates {
		candidates[index] = deduplicationModelTestCandidate(index+1, uint64(index+1), "candidate")
	}
	scored := 0
	judged := 0
	result, err := EvaluateDeduplicationCandidates(
		context.Background(),
		RepositoryReviewDeduplicationSnapshot{SimilarityThreshold: 90, CandidateLimit: 4},
		deduplicationModelTestDiagnosis("raw"), candidates, 0,
		func(
			_ context.Context, _ RepositoryReviewDeduplicationSnapshot, instructions string,
			request DeduplicationScoringRequest,
		) (DeduplicationScoringResponse, error) {
			if instructions != DeduplicationScoringInstructions {
				t.Fatal("scorer did not receive the isolated scoring contract")
			}
			scored += len(request.Candidates)
			response := DeduplicationScoringResponse{}
			for _, candidate := range request.Candidates {
				response.Scores = append(response.Scores, DeduplicationCandidateScore{
					CandidateID: candidate.ID, Score: 89, Explanation: "Related but ambiguous.",
				})
			}
			return response, nil
		},
		func(
			context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationJudgeRequest,
		) (DeduplicationJudgment, error) {
			judged++
			return DeduplicationJudgment{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if scored != 17 || judged != 0 || result.Judgment.Decision != "new" || len(result.Scores) != 17 {
		t.Fatalf("model pipeline result=%#v scored=%d judged=%d", result, scored, judged)
	}
}

func TestEvaluateDeduplicationCandidatesLimitZeroCallsNoModel(t *testing.T) {
	t.Parallel()
	called := false
	result, err := EvaluateDeduplicationCandidates(
		context.Background(),
		RepositoryReviewDeduplicationSnapshot{SimilarityThreshold: 90, CandidateLimit: 0},
		deduplicationModelTestDiagnosis("raw"),
		[]DeduplicationCandidateSnapshot{deduplicationModelTestCandidate(1, 1, "candidate")},
		0,
		func(
			context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationScoringRequest,
		) (DeduplicationScoringResponse, error) {
			called = true
			return DeduplicationScoringResponse{}, nil
		}, nil,
	)
	if err != nil || called || result.Judgment.Decision != "new" {
		t.Fatalf("disabled model dedup result=%#v called=%v err=%v", result, called, err)
	}
}

func TestEvaluateDeduplicationCandidatesRejectsMalformedChunkBeforeJudge(t *testing.T) {
	t.Parallel()
	judged := false
	_, err := EvaluateDeduplicationCandidates(
		context.Background(),
		RepositoryReviewDeduplicationSnapshot{SimilarityThreshold: 90, CandidateLimit: 4},
		deduplicationModelTestDiagnosis("raw"),
		[]DeduplicationCandidateSnapshot{deduplicationModelTestCandidate(1, 1, "candidate")},
		0,
		func(
			context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationScoringRequest,
		) (DeduplicationScoringResponse, error) {
			return DeduplicationScoringResponse{}, nil
		},
		func(
			context.Context, RepositoryReviewDeduplicationSnapshot, string, DeduplicationJudgeRequest,
		) (DeduplicationJudgment, error) {
			judged = true
			return DeduplicationJudgment{Decision: "new"}, nil
		},
	)
	if err == nil || judged {
		t.Fatalf("malformed score error=%v judged=%v", err, judged)
	}
}

func TestStrictDeduplicationResponseDecoding(t *testing.T) {
	t.Parallel()
	if _, err := DecodeDeduplicationScoringResponse([]byte(
		`{"scores":[{"candidate_id":"candidate-1","score":90.5,"explanation":"same"}]}`,
	)); err == nil {
		t.Fatal("fractional integer score was accepted")
	}
	if _, err := DecodeDeduplicationJudgment([]byte(
		`{"decision":"new","unknown":true}`,
	)); err == nil {
		t.Fatal("unknown judgment field was accepted")
	}
	if _, err := DecodeDeduplicationJudgment([]byte(
		`{"decision":"new"} {"decision":"new"}`,
	)); err == nil {
		t.Fatal("trailing judgment was accepted")
	}
}

func TestDeduplicationCandidateUniverseDigestUsesStableOrderAndVersion(t *testing.T) {
	t.Parallel()
	left := deduplicationModelTestCandidate(1, 2, "one")
	right := deduplicationModelTestCandidate(2, 1, "two")
	first, err := DeduplicationCandidateUniverseDigest([]DeduplicationCandidateSnapshot{left, right})
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := DeduplicationCandidateUniverseDigest([]DeduplicationCandidateSnapshot{right, left})
	if err != nil || reordered != first {
		t.Fatalf("digest order instability: %q, %q, %v", first, reordered, err)
	}
	right.Version++
	changed, err := DeduplicationCandidateUniverseDigest([]DeduplicationCandidateSnapshot{left, right})
	if err != nil || changed == first {
		t.Fatalf("candidate version absent from digest: %q, %q, %v", first, changed, err)
	}
}

func deduplicationModelTestCandidate(id int, ordinal uint64, title string) DeduplicationCandidateSnapshot {
	return DeduplicationCandidateSnapshot{
		ID: "dedup-" + leftPadDeduplicationTestID(id), Version: 1,
		CreationOrdinal: ordinal, Diagnosis: deduplicationModelTestDiagnosis(title),
	}
}

func deduplicationModelTestDiagnosis(title string) DeduplicationDiagnosis {
	return DeduplicationDiagnosis{
		Severity: "high", Title: title, Message: title + " message",
		Evidence: title + " evidence", Impact: title + " impact",
		Validation: Validation{Status: "confirmed", Summary: title + " validated"},
	}
}

func leftPadDeduplicationTestID(value int) string {
	if value < 10 {
		return "00" + string(rune('0'+value))
	}
	if value < 100 {
		return "0" + string(rune('0'+value/10)) + string(rune('0'+value%10))
	}
	return "999"
}
