//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewDecisionRunAdmissionIsAtomicAndIdempotent(t *testing.T) {
	t.Parallel()

	store, clock, reviewCase := newReviewDecisionRunFixture(t, ":memory:")
	key := testReviewDecisionKey(reviewCase)
	admission := ReviewDecisionRunAdmission{
		Key:   key,
		RunID: "wr_00000000000000000000000000000101",
	}
	var calls atomic.Int32
	link, existed, err := store.AdmitReviewDecisionRun(
		context.Background(),
		admission,
		func(ctx context.Context) error {
			calls.Add(1)
			return ctx.Err()
		},
	)
	require.NoError(t, err)
	assert.False(t, existed)
	assert.Equal(t, admission.Key, link.Key)
	assert.Equal(t, admission.RunID, link.RunID)
	assert.Equal(t, clock.UTC(), link.CreatedAt)
	assert.Equal(t, int32(1), calls.Load())

	stored, err := store.GetReviewDecisionRun(context.Background(), key)
	require.NoError(t, err)
	assert.Equal(t, link, stored)

	*clock = clock.Add(time.Hour)
	retried, existed, err := store.AdmitReviewDecisionRun(
		context.Background(),
		admission,
		func(context.Context) error {
			calls.Add(1)
			return errors.New("must not run")
		},
	)
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, link, retried)
	assert.Equal(t, int32(1), calls.Load())
}

func TestReviewDecisionRunAdmissionRollsBackCallbackFailure(t *testing.T) {
	t.Parallel()

	store, _, reviewCase := newReviewDecisionRunFixture(t, ":memory:")
	key := testReviewDecisionKey(reviewCase)
	admission := ReviewDecisionRunAdmission{
		Key:   key,
		RunID: "wr_00000000000000000000000000000102",
	}
	callbackErr := errors.New("durable workflow create failed")
	_, existed, err := store.AdmitReviewDecisionRun(
		context.Background(),
		admission,
		func(context.Context) error { return callbackErr },
	)
	assert.ErrorIs(t, err, callbackErr)
	assert.False(t, existed)
	_, err = store.GetReviewDecisionRun(context.Background(), key)
	assert.ErrorIs(t, err, ErrNotFound)

	var calls atomic.Int32
	link, existed, err := store.AdmitReviewDecisionRun(
		context.Background(),
		admission,
		func(context.Context) error {
			calls.Add(1)
			return nil
		},
	)
	require.NoError(t, err)
	assert.False(t, existed)
	assert.Equal(t, admission.RunID, link.RunID)
	assert.Equal(t, int32(1), calls.Load())
}

func TestReviewDecisionRunAdmissionFencesCaseVersionButRetainsHistoricalRetry(t *testing.T) {
	t.Parallel()

	store, _, reviewCase := newReviewDecisionRunFixture(t, ":memory:")
	historical := ReviewDecisionRunAdmission{
		Key:   testReviewDecisionKey(reviewCase),
		RunID: "wr_00000000000000000000000000000103",
	}
	link, _, err := store.AdmitReviewDecisionRun(
		context.Background(),
		historical,
		func(context.Context) error { return nil },
	)
	require.NoError(t, err)

	detail, err := store.AppendReviewMessages(context.Background(), ReviewMessageAppend{
		CaseID:          reviewCase.ID,
		ExpectedVersion: reviewCase.Version,
		Messages: []ReviewMessageDraft{{
			Kind:    ReviewMessageChat,
			Role:    ReviewMessageUser,
			Content: "Please inspect the cancellation finding.",
		}},
	})
	require.NoError(t, err)
	require.Equal(t, reviewCase.Version+1, detail.Case.Version)

	var historicalCalls atomic.Int32
	retried, existed, err := store.AdmitReviewDecisionRun(
		context.Background(),
		historical,
		func(context.Context) error {
			historicalCalls.Add(1)
			return nil
		},
	)
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, link, retried)
	assert.Zero(t, historicalCalls.Load())

	stale := ReviewDecisionRunAdmission{
		Key: ReviewDecisionKey{
			CaseID:         reviewCase.ID,
			CaseVersion:    reviewCase.Version,
			DecisionPoint:  "after-finding-edit",
			PolicyRevision: historical.Key.PolicyRevision,
		},
		RunID: "wr_00000000000000000000000000000104",
	}
	var staleCalls atomic.Int32
	_, existed, err = store.AdmitReviewDecisionRun(
		context.Background(),
		stale,
		func(context.Context) error {
			staleCalls.Add(1)
			return nil
		},
	)
	assert.ErrorIs(t, err, ErrReviewConflict)
	assert.False(t, existed)
	assert.Zero(t, staleCalls.Load())

	current := stale
	current.Key.CaseVersion = detail.Case.Version
	current.RunID = "wr_00000000000000000000000000000105"
	_, existed, err = store.AdmitReviewDecisionRun(
		context.Background(),
		current,
		func(context.Context) error { return nil },
	)
	require.NoError(t, err)
	assert.False(t, existed)
}

func TestReviewDecisionRunAdmissionRejectsIdentityConflicts(t *testing.T) {
	t.Parallel()

	store, _, reviewCase := newReviewDecisionRunFixture(t, ":memory:")
	first := ReviewDecisionRunAdmission{
		Key:   testReviewDecisionKey(reviewCase),
		RunID: "wr_00000000000000000000000000000106",
	}
	_, _, err := store.AdmitReviewDecisionRun(
		context.Background(),
		first,
		func(context.Context) error { return nil },
	)
	require.NoError(t, err)

	tests := []struct {
		name      string
		admission ReviewDecisionRunAdmission
	}{
		{
			name: "same decision uses another run",
			admission: ReviewDecisionRunAdmission{
				Key:   first.Key,
				RunID: "wr_00000000000000000000000000000107",
			},
		},
		{
			name: "same run is reused by another decision",
			admission: ReviewDecisionRunAdmission{
				Key: ReviewDecisionKey{
					CaseID:         first.Key.CaseID,
					CaseVersion:    first.Key.CaseVersion,
					DecisionPoint:  "before-push",
					PolicyRevision: first.Key.PolicyRevision,
				},
				RunID: first.RunID,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32
			_, existed, err := store.AdmitReviewDecisionRun(
				context.Background(),
				test.admission,
				func(context.Context) error {
					calls.Add(1)
					return nil
				},
			)
			assert.ErrorIs(t, err, ErrReviewConflict)
			assert.False(t, existed)
			assert.Zero(t, calls.Load())
		})
	}
}

func TestReviewDecisionRunAdmissionSerializesConcurrentRetries(t *testing.T) {
	t.Parallel()

	store, _, reviewCase := newReviewDecisionRunFixture(t, ":memory:")
	admission := ReviewDecisionRunAdmission{
		Key:   testReviewDecisionKey(reviewCase),
		RunID: "wr_00000000000000000000000000000108",
	}
	const workers = 24
	start := make(chan struct{})
	results := make(chan reviewDecisionRunTestResult, workers)
	var (
		callbacks atomic.Int32
		group     sync.WaitGroup
	)
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			link, existed, err := store.AdmitReviewDecisionRun(
				context.Background(),
				admission,
				func(context.Context) error {
					callbacks.Add(1)
					return nil
				},
			)
			results <- reviewDecisionRunTestResult{link: link, existed: existed, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	created := 0
	for result := range results {
		require.NoError(t, result.err)
		assert.Equal(t, admission.Key, result.link.Key)
		assert.Equal(t, admission.RunID, result.link.RunID)
		if !result.existed {
			created++
		}
	}
	assert.Equal(t, 1, created)
	assert.Equal(t, int32(1), callbacks.Load())
}

func TestReviewDecisionRunAdmissionSerializesAcrossStoreInstances(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "review-decisions-concurrent.db")
	first, _, reviewCase := newReviewDecisionRunFixture(t, path)
	second := openReviewDecisionPeer(t, path)
	admission := ReviewDecisionRunAdmission{
		Key:   testReviewDecisionKey(reviewCase),
		RunID: "wr_00000000000000000000000000000113",
	}
	callbackEntered := make(chan struct{}, 1)
	releaseCallback := make(chan struct{})
	start := make(chan struct{})
	results := make(chan reviewDecisionRunTestResult, 2)
	var (
		callbacks atomic.Int32
		group     sync.WaitGroup
	)
	for _, store := range []*Store{first, second} {
		group.Add(1)
		go func(candidate *Store) {
			defer group.Done()
			<-start
			link, existed, err := candidate.AdmitReviewDecisionRun(
				context.Background(),
				admission,
				func(context.Context) error {
					callbacks.Add(1)
					callbackEntered <- struct{}{}
					<-releaseCallback
					return nil
				},
			)
			results <- reviewDecisionRunTestResult{link: link, existed: existed, err: err}
		}(store)
	}
	close(start)
	awaitReviewDecisionSignal(t, callbackEntered, "one admission callback to start")
	close(releaseCallback)
	group.Wait()
	close(results)

	created := 0
	var want ReviewDecisionRunLink
	for result := range results {
		require.NoError(t, result.err)
		if want == (ReviewDecisionRunLink{}) {
			want = result.link
		} else {
			assert.Equal(t, want, result.link)
		}
		if !result.existed {
			created++
		}
	}
	assert.Equal(t, 1, created)
	assert.Equal(t, int32(1), callbacks.Load())
	stored, err := second.GetReviewDecisionRun(context.Background(), admission.Key)
	require.NoError(t, err)
	assert.Equal(t, want, stored)
}

func TestReviewDecisionRunAdmissionHoldsCaseVersionFenceAcrossStoreInstances(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "review-decisions-version-fence.db")
	first, _, reviewCase := newReviewDecisionRunFixture(t, path)
	second := openReviewDecisionPeer(t, path)
	admission := ReviewDecisionRunAdmission{
		Key:   testReviewDecisionKey(reviewCase),
		RunID: "wr_00000000000000000000000000000114",
	}
	callbackEntered := make(chan struct{}, 1)
	releaseCallback := make(chan struct{})
	admissionResult := make(chan reviewDecisionRunTestResult, 1)
	go func() {
		link, existed, err := first.AdmitReviewDecisionRun(
			context.Background(),
			admission,
			func(context.Context) error {
				callbackEntered <- struct{}{}
				<-releaseCallback
				return nil
			},
		)
		admissionResult <- reviewDecisionRunTestResult{link: link, existed: existed, err: err}
	}()
	awaitReviewDecisionSignal(t, callbackEntered, "admission callback to hold the writer fence")

	mutationResult := make(chan reviewDecisionMutationTestResult, 1)
	mutationStarted := make(chan struct{})
	go func() {
		close(mutationStarted)
		detail, err := second.AppendReviewMessages(context.Background(), ReviewMessageAppend{
			CaseID:          reviewCase.ID,
			ExpectedVersion: reviewCase.Version,
			Messages: []ReviewMessageDraft{{
				Kind:    ReviewMessageChat,
				Role:    ReviewMessageUser,
				Content: "Mutation must wait for attention admission.",
			}},
		})
		mutationResult <- reviewDecisionMutationTestResult{detail: detail, err: err}
	}()
	awaitReviewDecisionSignal(t, mutationStarted, "case mutation to start")
	assertReviewDecisionStillBlocked(t, mutationResult, "case mutation committed before admission released its fence")

	close(releaseCallback)
	admitted := awaitReviewDecisionAdmissionResult(t, admissionResult)
	require.NoError(t, admitted.err)
	assert.False(t, admitted.existed)
	mutated := awaitReviewDecisionMutationResult(t, mutationResult)
	require.NoError(t, mutated.err)
	assert.Equal(t, reviewCase.Version+1, mutated.detail.Case.Version)
}

func TestReviewDecisionRunAdmissionCancellationWhileWaitingAcrossStoreInstances(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "review-decisions-canceled-wait.db")
	first, _, reviewCase := newReviewDecisionRunFixture(t, path)
	second := openReviewDecisionPeer(t, path, WithBusyTimeout(250*time.Millisecond))
	firstAdmission := ReviewDecisionRunAdmission{
		Key:   testReviewDecisionKey(reviewCase),
		RunID: "wr_00000000000000000000000000000115",
	}
	callbackEntered := make(chan struct{}, 1)
	releaseCallback := make(chan struct{})
	firstResult := make(chan reviewDecisionRunTestResult, 1)
	go func() {
		link, existed, err := first.AdmitReviewDecisionRun(
			context.Background(),
			firstAdmission,
			func(context.Context) error {
				callbackEntered <- struct{}{}
				<-releaseCallback
				return nil
			},
		)
		firstResult <- reviewDecisionRunTestResult{link: link, existed: existed, err: err}
	}()
	awaitReviewDecisionSignal(t, callbackEntered, "first admission callback to hold the writer lock")

	secondAdmission := ReviewDecisionRunAdmission{
		Key: ReviewDecisionKey{
			CaseID:         reviewCase.ID,
			CaseVersion:    reviewCase.Version,
			DecisionPoint:  "before-push",
			PolicyRevision: firstAdmission.Key.PolicyRevision,
		},
		RunID: "wr_00000000000000000000000000000116",
	}
	waitingContext, cancelWaiting := context.WithCancel(context.Background())
	secondStarted := make(chan struct{})
	secondResult := make(chan reviewDecisionRunTestResult, 1)
	var secondCallbacks atomic.Int32
	go func() {
		close(secondStarted)
		link, existed, err := second.AdmitReviewDecisionRun(
			waitingContext,
			secondAdmission,
			func(context.Context) error {
				secondCallbacks.Add(1)
				return nil
			},
		)
		secondResult <- reviewDecisionRunTestResult{link: link, existed: existed, err: err}
	}()
	awaitReviewDecisionSignal(t, secondStarted, "second admission to begin waiting")
	assertReviewDecisionStillBlocked(t, secondResult, "second admission did not wait on the writer lock")
	cancelWaiting()
	canceled := awaitReviewDecisionAdmissionResult(t, secondResult)
	assert.ErrorIs(t, canceled.err, context.Canceled)
	assert.False(t, canceled.existed)
	assert.Zero(t, secondCallbacks.Load())

	close(releaseCallback)
	firstCompleted := awaitReviewDecisionAdmissionResult(t, firstResult)
	require.NoError(t, firstCompleted.err)
	_, err := second.GetReviewDecisionRun(context.Background(), secondAdmission.Key)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestReviewDecisionRunLinkSurvivesReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "review-decisions.db")
	store, _, reviewCase := newReviewDecisionRunFixture(t, path)
	admission := ReviewDecisionRunAdmission{
		Key:   testReviewDecisionKey(reviewCase),
		RunID: "wr_00000000000000000000000000000109",
	}
	want, _, err := store.AdmitReviewDecisionRun(
		context.Background(),
		admission,
		func(context.Context) error { return nil },
	)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	reopened, err := Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	got, err := reopened.GetReviewDecisionRun(context.Background(), admission.Key)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	var calls atomic.Int32
	retried, existed, err := reopened.AdmitReviewDecisionRun(
		context.Background(),
		admission,
		func(context.Context) error {
			calls.Add(1)
			return nil
		},
	)
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, want, retried)
	assert.Zero(t, calls.Load())
}

func TestReviewDecisionRunAdmissionPreservesContextCancellation(t *testing.T) {
	t.Parallel()

	store, _, reviewCase := newReviewDecisionRunFixture(t, ":memory:")
	admission := ReviewDecisionRunAdmission{
		Key:   testReviewDecisionKey(reviewCase),
		RunID: "wr_00000000000000000000000000000110",
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	_, _, err := store.AdmitReviewDecisionRun(
		ctx,
		admission,
		func(context.Context) error {
			calls.Add(1)
			return nil
		},
	)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, calls.Load())
	_, err = store.GetReviewDecisionRun(context.Background(), admission.Key)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestReviewDecisionRunAdmissionReportsPostCreateCommitUncertainty(t *testing.T) {
	t.Parallel()

	store, _, reviewCase := newReviewDecisionRunFixture(t, ":memory:")
	admission := ReviewDecisionRunAdmission{
		Key:   testReviewDecisionKey(reviewCase),
		RunID: "wr_00000000000000000000000000000112",
	}
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	_, existed, err := store.AdmitReviewDecisionRun(
		ctx,
		admission,
		func(callbackContext context.Context) error {
			assert.NoError(t, callbackContext.Err())
			calls.Add(1)
			cancel()
			return nil
		},
	)
	assert.ErrorIs(t, err, ErrReviewDecisionAdmissionUncertain)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, existed)
	assert.Equal(t, int32(1), calls.Load())

	// A canceled COMMIT is ambiguous to the admission caller. This driver
	// rolls it back, but callers must still reconcile the deterministic RunID
	// instead of relying on that implementation detail.
	_, err = store.GetReviewDecisionRun(context.Background(), admission.Key)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestReviewDecisionRunValidationRejectsMalformedIdentities(t *testing.T) {
	t.Parallel()

	store, _, reviewCase := newReviewDecisionRunFixture(t, ":memory:")
	valid := ReviewDecisionRunAdmission{
		Key:   testReviewDecisionKey(reviewCase),
		RunID: "wr_00000000000000000000000000000111",
	}
	tests := []struct {
		name   string
		mutate func(*ReviewDecisionRunAdmission)
	}{
		{name: "case ID", mutate: func(input *ReviewDecisionRunAdmission) { input.Key.CaseID = "prc_bad" }},
		{name: "case ID whitespace", mutate: func(input *ReviewDecisionRunAdmission) {
			input.Key.CaseID = " " + input.Key.CaseID
		}},
		{name: "case version", mutate: func(input *ReviewDecisionRunAdmission) { input.Key.CaseVersion = 0 }},
		{
			name:   "decision point empty",
			mutate: func(input *ReviewDecisionRunAdmission) { input.Key.DecisionPoint = " " },
		},
		{
			name:   "decision point uppercase",
			mutate: func(input *ReviewDecisionRunAdmission) { input.Key.DecisionPoint = "Submitted-review" },
		},
		{
			name:   "decision point invalid punctuation",
			mutate: func(input *ReviewDecisionRunAdmission) { input.Key.DecisionPoint = "submitted/review" },
		},
		{
			name:   "decision point leading whitespace",
			mutate: func(input *ReviewDecisionRunAdmission) { input.Key.DecisionPoint = " submitted-review" },
		},
		{
			name:   "decision point trailing whitespace",
			mutate: func(input *ReviewDecisionRunAdmission) { input.Key.DecisionPoint = "submitted-review " },
		},
		{name: "decision point too long", mutate: func(input *ReviewDecisionRunAdmission) {
			input.Key.DecisionPoint = strings.Repeat("d", maxReviewDecisionPointBytes+1)
		}},
		{
			name:   "policy revision empty",
			mutate: func(input *ReviewDecisionRunAdmission) { input.Key.PolicyRevision = " " },
		},
		{
			name:   "policy revision label",
			mutate: func(input *ReviewDecisionRunAdmission) { input.Key.PolicyRevision = "policy-v1" },
		},
		{name: "policy revision uppercase digest", mutate: func(input *ReviewDecisionRunAdmission) {
			input.Key.PolicyRevision = "sha256:" + strings.Repeat("A", 64)
		}},
		{name: "policy revision wrong digest length", mutate: func(input *ReviewDecisionRunAdmission) {
			input.Key.PolicyRevision = "sha256:" + strings.Repeat("a", 63)
		}},
		{
			name:   "policy revision trailing whitespace",
			mutate: func(input *ReviewDecisionRunAdmission) { input.Key.PolicyRevision += " " },
		},
		{name: "run ID", mutate: func(input *ReviewDecisionRunAdmission) { input.RunID = "wr_bad" }},
		{name: "run ID whitespace", mutate: func(input *ReviewDecisionRunAdmission) { input.RunID += " " }},
		{
			name:   "run ID uppercase",
			mutate: func(input *ReviewDecisionRunAdmission) { input.RunID = "wr_000000000000000000000000000001AA" },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := valid
			test.mutate(&input)
			var calls atomic.Int32
			_, _, err := store.AdmitReviewDecisionRun(
				context.Background(),
				input,
				func(context.Context) error {
					calls.Add(1)
					return nil
				},
			)
			assert.ErrorIs(t, err, ErrInvalidReview)
			assert.Zero(t, calls.Load())
		})
	}

	_, _, err := store.AdmitReviewDecisionRun(context.Background(), valid, nil)
	assert.ErrorIs(t, err, ErrInvalidReview)
}

func TestStoreMigratesV3ToReviewDecisionRunSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v3-review-decisions.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1)
	require.NoError(t, err)
	_, err = db.Exec(schemaV2)
	require.NoError(t, err)
	_, err = db.Exec(schemaV3)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 3)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	defer store.Close()
	assert.True(t, schemaObjectExists(t, store.db, "table", "pr_review_decision_runs"))
	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
}

func TestStoreReviewDecisionMigrationValidationFailureRollsBackVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "migration-v4-review-decisions-rollback.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1)
	require.NoError(t, err)
	_, err = db.Exec(schemaV2)
	require.NoError(t, err)
	_, err = db.Exec(schemaV3)
	require.NoError(t, err)
	malformed := strings.Replace(
		schemaV4ReviewDecisionRunsTable,
		"CHECK (case_version >= 1)",
		"CHECK (case_version >= 2)",
		1,
	)
	_, err = db.Exec(malformed)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 3)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v4")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 3, version)
}

func TestStoreRejectsCurrentReviewDecisionSchemaWithoutUniqueRunID(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "review-decisions-missing-run-identity.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1)
	require.NoError(t, err)
	_, err = db.Exec(schemaV2)
	require.NoError(t, err)
	_, err = db.Exec(schemaV3)
	require.NoError(t, err)
	malformed := strings.Replace(
		schemaV4ReviewDecisionRunsTable,
		"run_id TEXT NOT NULL UNIQUE,",
		"run_id TEXT NOT NULL,",
		1,
	)
	_, err = db.Exec(malformed)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, schemaVersion)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v4")
	var validationErr *schemaValidationError
	require.ErrorAs(t, err, &validationErr)
	assert.Equal(t, "pr_review_decision_runs", validationErr.object)
}

type reviewDecisionRunTestResult struct {
	link    ReviewDecisionRunLink
	existed bool
	err     error
}

type reviewDecisionMutationTestResult struct {
	detail ReviewCaseDetail
	err    error
}

func newReviewDecisionRunFixture(
	t *testing.T,
	path string,
) (*Store, *time.Time, ReviewCase) {
	t.Helper()
	store, clock, input := newReviewStoreFixtureAt(t, path)
	reviewCase, created, err := store.CaptureReview(context.Background(), input)
	require.NoError(t, err)
	require.True(t, created)
	return store, clock, reviewCase
}

func openReviewDecisionPeer(t *testing.T, path string, options ...Option) *Store {
	t.Helper()
	store, err := Open(context.Background(), path, options...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func awaitReviewDecisionSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "timed out waiting for "+description)
	}
}

func assertReviewDecisionStillBlocked[T any](t *testing.T, result <-chan T, message string) {
	t.Helper()
	select {
	case <-result:
		require.FailNow(t, message)
	case <-time.After(100 * time.Millisecond):
	}
}

func awaitReviewDecisionAdmissionResult(
	t *testing.T,
	result <-chan reviewDecisionRunTestResult,
) reviewDecisionRunTestResult {
	t.Helper()
	select {
	case received := <-result:
		return received
	case <-time.After(5 * time.Second):
		require.FailNow(t, "timed out waiting for review decision admission")
		return reviewDecisionRunTestResult{}
	}
}

func awaitReviewDecisionMutationResult(
	t *testing.T,
	result <-chan reviewDecisionMutationTestResult,
) reviewDecisionMutationTestResult {
	t.Helper()
	select {
	case received := <-result:
		return received
	case <-time.After(5 * time.Second):
		require.FailNow(t, "timed out waiting for review case mutation")
		return reviewDecisionMutationTestResult{}
	}
}

func testReviewDecisionKey(reviewCase ReviewCase) ReviewDecisionKey {
	return ReviewDecisionKey{
		CaseID:         reviewCase.ID,
		CaseVersion:    reviewCase.Version,
		DecisionPoint:  "submitted-review",
		PolicyRevision: "sha256:" + strings.Repeat("a", 64),
	}
}
