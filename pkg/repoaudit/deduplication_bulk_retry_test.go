package repoaudit

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRetryDeduplicationsReturnsOrderedPartialResultsAtomically(t *testing.T) {
	fixture := dedupDeepPendingFixture(t, 4)
	state := dedupDeepState(t, fixture)
	now := state.UpdatedAt.Add(time.Minute)
	for _, index := range []int{0, 1, 3} {
		markDeduplicationFailed(
			&state.RawFindings[index],
			&state.DeduplicationJobs[deduplicationJobIndexByRawID(
				state.DeduplicationJobs,
				state.RawFindings[index].ID,
			)],
			"attempt_limit",
			now,
		)
	}
	historicalID := state.RawFindings[3].ID
	state.RawFindings[3].AssignmentID = historicalReplayAssignmentID
	state.RawFindings[3].DiagnosisDigest = RawReviewFindingDiagnosisDigest(state.RawFindings[3])
	state.Version++
	state.UpdatedAt = now
	reconcileFindingsProcessingCounters(&state)
	state.FindingsProcessing.UpdatedAt = now
	if err := fixture.store.save(&state); err != nil {
		t.Fatal(err)
	}

	firstID := state.RawFindings[1].ID
	secondID := state.RawFindings[0].ID
	nonfailedID := state.RawFindings[2].ID
	firstBefore := state.RawFindings[1]
	secondBefore := state.RawFindings[0]
	versionBefore := state.Version
	nextOrdinal := state.NextDeduplicationOrdinal

	updated, result, err := fixture.store.RetryDeduplications(
		fixture.repository,
		[]string{"missing-source", "  " + firstID + " ", nonfailedID, historicalID, secondID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.RetriedIDs, []string{firstID, secondID}) {
		t.Fatalf("retried IDs=%v", result.RetriedIDs)
	}
	wantFailures := []DeduplicationRetryFailure{
		{
			SourceID: "missing-source", Code: "not_found",
			Message: "Finding processing source was not found.",
		},
		{
			SourceID: nonfailedID, Code: "not_retryable",
			Message: "Finding processing source is not retryable.",
		},
		{
			SourceID: historicalID, Code: "historical_replay_required",
			Message: "Historical sources must be retried through historical consolidation.",
		},
	}
	if !reflect.DeepEqual(result.Failures, wantFailures) {
		t.Fatalf("failures=%#v", result.Failures)
	}
	if updated.Version != versionBefore+1 || updated.NextDeduplicationOrdinal != nextOrdinal+2 ||
		updated.FindingsProcessing.Pending != 3 || updated.FindingsProcessing.Failed != 1 {
		t.Fatalf(
			"version=%d next=%d counters=%#v",
			updated.Version,
			updated.NextDeduplicationOrdinal,
			updated.FindingsProcessing,
		)
	}
	firstRaw, firstJob := bulkRetrySource(t, updated, firstID)
	secondRaw, secondJob := bulkRetrySource(t, updated, secondID)
	if firstJob.InsertionOrdinal != nextOrdinal || secondJob.InsertionOrdinal != nextOrdinal+1 ||
		firstRaw.InsertionOrdinal != firstBefore.InsertionOrdinal ||
		secondRaw.InsertionOrdinal != secondBefore.InsertionOrdinal {
		t.Fatalf(
			"ordinals first=%d/%d second=%d/%d",
			firstRaw.InsertionOrdinal,
			firstJob.InsertionOrdinal,
			secondRaw.InsertionOrdinal,
			secondJob.InsertionOrdinal,
		)
	}
	if firstRaw.State != RawFindingDeduplicationPending || firstRaw.Failure != nil ||
		firstJob.State != DeduplicationJobPending || firstJob.Attempts != 0 ||
		secondRaw.State != RawFindingDeduplicationPending || secondRaw.Failure != nil ||
		secondJob.State != DeduplicationJobPending || secondJob.Attempts != 0 {
		t.Fatalf("retried first=%#v/%#v second=%#v/%#v", firstRaw, firstJob, secondRaw, secondJob)
	}
	assertBulkRetryDiagnosisUnchanged(t, firstBefore, firstRaw)
	assertBulkRetryDiagnosisUnchanged(t, secondBefore, secondRaw)

	persisted := dedupDeepState(t, fixture)
	if persisted.Version != updated.Version ||
		persisted.NextDeduplicationOrdinal != updated.NextDeduplicationOrdinal {
		t.Fatalf("persisted state did not match result: %#v", persisted)
	}
}

func TestRetryDeduplicationsAllIneligibleDoesNotMutateState(t *testing.T) {
	fixture := dedupDeepPendingFixture(t, 2)
	state := dedupDeepState(t, fixture)
	now := state.UpdatedAt.Add(time.Minute)
	historicalID := state.RawFindings[1].ID
	markDeduplicationFailed(
		&state.RawFindings[1],
		&state.DeduplicationJobs[deduplicationJobIndexByRawID(
			state.DeduplicationJobs,
			historicalID,
		)],
		"attempt_limit",
		now,
	)
	state.RawFindings[1].AssignmentID = historicalReplayAssignmentID
	state.RawFindings[1].DiagnosisDigest = RawReviewFindingDiagnosisDigest(state.RawFindings[1])
	state.Version++
	state.UpdatedAt = now
	reconcileFindingsProcessingCounters(&state)
	state.FindingsProcessing.UpdatedAt = now
	if err := fixture.store.save(&state); err != nil {
		t.Fatal(err)
	}
	before := dedupDeepState(t, fixture)

	returned, result, err := fixture.store.RetryDeduplications(
		fixture.repository,
		[]string{state.RawFindings[0].ID, "missing", historicalID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.RetriedIDs == nil || len(result.RetriedIDs) != 0 || result.Failures == nil ||
		len(result.Failures) != 3 {
		t.Fatalf("result=%#v", result)
	}
	if !reflect.DeepEqual(returned, before) {
		t.Fatal("all-ineligible retry changed returned state")
	}
	persisted := dedupDeepState(t, fixture)
	if !reflect.DeepEqual(persisted, before) {
		t.Fatal("all-ineligible retry changed persisted state")
	}
}

func TestRetryDeduplicationsRejectsMalformedSelectionBeforeMutation(t *testing.T) {
	fixture := dedupDeepPendingFixture(t, 1)
	before := dedupDeepState(t, fixture)
	validID := before.RawFindings[0].ID
	overLimit := make([]string, 201)
	for index := range overLimit {
		overLimit[index] = "source-" + strings.Repeat("x", index%10) + string(rune('A'+index%26))
	}
	invalidUTF8 := string([]byte{0xff})
	for name, sourceIDs := range map[string][]string{
		"empty":         nil,
		"over limit":    overLimit,
		"blank":         {" "},
		"NUL":           {"bad\x00source"},
		"invalid UTF-8": {invalidUTF8},
		"too long":      {strings.Repeat("x", 257)},
		"duplicate":     {validID, " " + validID + " "},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := fixture.store.RetryDeduplications(fixture.repository, sourceIDs); err == nil {
				t.Fatal("malformed retry selection was accepted")
			}
			persisted := dedupDeepState(t, fixture)
			if !reflect.DeepEqual(persisted, before) {
				t.Fatal("malformed retry selection changed state")
			}
		})
	}
}

func TestRetryDeduplicationsRejectsInvalidStoreInputs(t *testing.T) {
	_, _, err := NewStore(t.TempDir()).RetryDeduplications(" ", []string{"source"})
	if err == nil || !strings.Contains(err.Error(), "repository is required") {
		t.Fatalf("empty repository error=%v", err)
	}

	store := NewStore(t.TempDir())
	if err = os.MkdirAll(store.root+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.RetryDeduplications("owner/repo", []string{"source"}); err == nil {
		t.Fatal("bulk retry accepted an unsafe store lock")
	}

	_, err = retryDeduplicationInState(nil, "source", time.Now())
	if err == nil || !strings.Contains(err.Error(), "state is required") {
		t.Fatalf("nil state error=%v", err)
	}
}

func TestRetryDeduplicationsPropagatesLoadAndSaveFailures(t *testing.T) {
	fixture := dedupDeepPendingFixture(t, 1)
	state := dedupDeepState(t, fixture)
	now := state.UpdatedAt.Add(time.Minute)
	markDeduplicationFailed(
		&state.RawFindings[0],
		&state.DeduplicationJobs[0],
		"attempt_limit",
		now,
	)
	state.Version++
	state.UpdatedAt = now
	reconcileFindingsProcessingCounters(&state)
	state.FindingsProcessing.UpdatedAt = now

	loadFailure := NewStore(t.TempDir())
	loadFailure.loadForTest = func(string) (RepositoryState, error) {
		return RepositoryState{}, errDeduplicationBulkRetryLoad
	}
	if _, _, err := loadFailure.RetryDeduplications(
		fixture.repository,
		[]string{state.RawFindings[0].ID},
	); err != errDeduplicationBulkRetryLoad {
		t.Fatalf("load error=%v", err)
	}

	saveFailure := dedupDeepSaveFailureStore(t, state, now.Add(time.Minute))
	if returned, result, err := saveFailure.RetryDeduplications(
		fixture.repository,
		[]string{state.RawFindings[0].ID},
	); err == nil || returned.Repository != "" || result.RetriedIDs != nil || result.Failures != nil {
		t.Fatalf("save failure returned state=%#v result=%#v err=%v", returned, result, err)
	}
}

var errDeduplicationBulkRetryLoad = &deduplicationBulkRetryTestError{}

type deduplicationBulkRetryTestError struct{}

func (*deduplicationBulkRetryTestError) Error() string { return "bulk retry load failed" }

func bulkRetrySource(
	t *testing.T,
	state RepositoryState,
	sourceID string,
) (RawReviewFinding, DeduplicationJob) {
	t.Helper()
	rawIndex := rawFindingIndexByID(state.RawFindings, sourceID)
	jobIndex := deduplicationJobIndexByRawID(state.DeduplicationJobs, sourceID)
	if rawIndex < 0 || jobIndex < 0 {
		t.Fatalf("source %q was not retained", sourceID)
	}
	return state.RawFindings[rawIndex], state.DeduplicationJobs[jobIndex]
}

func assertBulkRetryDiagnosisUnchanged(
	t *testing.T,
	before RawReviewFinding,
	after RawReviewFinding,
) {
	t.Helper()
	before.State = after.State
	before.Disposition = after.Disposition
	before.DeduplicatedFindingID = after.DeduplicatedFindingID
	before.History = after.History
	before.Failure = after.Failure
	before.Version = after.Version
	before.UpdatedAt = after.UpdatedAt
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("retry rewrote diagnosis or provenance\nbefore=%#v\nafter=%#v", before, after)
	}
}
