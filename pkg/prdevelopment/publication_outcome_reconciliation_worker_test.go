package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestPublicationOutcomeReconciliationWorkerDesiredTipPublishes(t *testing.T) {
	t.Parallel()

	fixture := newPublicationOutcomeWorkerFixture()
	worker := fixture.worker(t, fixture.observerFor(fixture.desiredObservation()))

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	stored := fixture.store.publication(fixture.publication.ID)
	assert.Equal(t, eventing.PRDevelopmentPublicationPublished, stored.Status)
	assert.Equal(t, eventing.PRDevelopmentPublicationPushReconciled, stored.PushDisposition)
	assert.Equal(t, publicationOutcomeReconciledResult(fixture.publication), stored.PushResult)
	assert.Equal(t, []string{"expire", "list", "case", "thread", "reconcile"}, fixture.store.operations())

	processed, err = worker.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.False(t, processed)
	assert.Equal(t, 1, fixture.observerCalls())
}

func TestPublicationOutcomeReconciliationWorkerDesiredTipPublishesWhenExpectedTipIsIdentical(
	t *testing.T,
) {
	t.Parallel()

	fixture := newPublicationOutcomeWorkerFixture()
	publication := fixture.publication
	publication.PushRequest.ExpectedRemoteTip = publication.PushRequest.ExpectedTip
	publication.ExpectedRemoteTip = publication.PushRequest.ExpectedTip
	fixture.publication = publication
	fixture.store.publications[publication.ID] = publication
	worker := fixture.worker(t, fixture.observerFor(fixture.desiredObservation()))

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	assert.Equal(t, eventing.PRDevelopmentPublicationPublished,
		fixture.store.publication(publication.ID).Status)
}

func TestPublicationOutcomeReconciliationWorkerInconclusiveStatesStayUnknown(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		observation func(*publicationOutcomeWorkerFixture) TimedPublicationRemoteObservation
		observerErr error
		wantError   bool
	}{
		{
			name: "expected pre-push tip",
			observation: func(fixture *publicationOutcomeWorkerFixture) TimedPublicationRemoteObservation {
				observed := fixture.desiredObservation()
				observed.Observation.HeadSHA = fixture.publication.PushRequest.ExpectedRemoteTip
				return observed
			},
		},
		{
			name: "foreign tip",
			observation: func(fixture *publicationOutcomeWorkerFixture) TimedPublicationRemoteObservation {
				observed := fixture.desiredObservation()
				observed.Observation.HeadSHA = strings.Repeat("f", 40)
				return observed
			},
		},
		{
			name: "changed identity",
			observation: func(fixture *publicationOutcomeWorkerFixture) TimedPublicationRemoteObservation {
				observed := fixture.desiredObservation()
				observed.Observation.HeadRepository = "foreign/project"
				return observed
			},
		},
		{
			name: "missing head",
			observation: func(fixture *publicationOutcomeWorkerFixture) TimedPublicationRemoteObservation {
				observed := fixture.desiredObservation()
				observed.Observation.HeadSHA = ""
				return observed
			},
		},
		{
			name: "provider unavailable", observerErr: errors.New("provider unavailable"),
			wantError: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			fixture := newPublicationOutcomeWorkerFixture()
			observation := fixture.desiredObservation()
			if testCase.observation != nil {
				observation = testCase.observation(fixture)
			}
			observer := fixture.observerFor(observation)
			observer.err = testCase.observerErr
			worker := fixture.worker(t, observer)

			processed, err := worker.ProcessOne(context.Background())
			assert.True(t, processed)
			if testCase.wantError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			stored := fixture.store.publication(fixture.publication.ID)
			assert.Equal(t, eventing.PRDevelopmentPublicationOutcomeUnknown, stored.Status)
			assert.Zero(t, fixture.store.reconcileCalls)
			retry, exists := worker.retries[fixture.publication.ID]
			require.True(t, exists)
			assert.Equal(t, 1, retry.attempt)
			assert.Equal(t, fixture.now.Add(time.Second), retry.dueAt)
		})
	}
}

func TestPublicationOutcomeReconciliationWorkerExpiresBeforeObserving(t *testing.T) {
	t.Parallel()

	fixture := newPublicationOutcomeWorkerFixture()
	fixture.store.publications = make(map[string]eventing.PRDevelopmentPublication)
	fixture.store.expiryBatches = [][]eventing.PRDevelopmentPublication{{fixture.publication}}
	worker := fixture.worker(t, fixture.observerFor(fixture.desiredObservation()))

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	assert.Equal(t, []string{"expire"}, fixture.store.operations())
	assert.Zero(t, fixture.observerCalls())

	processed, err = worker.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	assert.Equal(t, eventing.PRDevelopmentPublicationPublished,
		fixture.store.publication(fixture.publication.ID).Status)
	assert.Equal(t, 1, fixture.observerCalls())
}

func TestPublicationOutcomeReconciliationWorkerRotatesPastBackoffAndRestartRetriesImmediately(
	t *testing.T,
) {
	t.Parallel()

	fixture := newPublicationOutcomeWorkerFixture()
	second := fixture.publication
	second.ID = "pdpub_00000000000000000000000000000002"
	second.CreatedAt = second.CreatedAt.Add(time.Second)
	second.AvailableAt = second.AvailableAt.Add(time.Second)
	fixture.store.publications[second.ID] = second
	foreign := fixture.desiredObservation()
	foreign.Observation.HeadSHA = strings.Repeat("f", 40)
	observer := fixture.observerFor(foreign, fixture.desiredObservation())
	worker := fixture.worker(t, observer)

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	assert.Equal(t, eventing.PRDevelopmentPublicationOutcomeUnknown,
		fixture.store.publication(fixture.publication.ID).Status)

	processed, err = worker.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	assert.Equal(t, eventing.PRDevelopmentPublicationPublished,
		fixture.store.publication(second.ID).Status,
		"the backed-off first row must not starve the later row")

	// A newly constructed worker has no retry memory and may safely perform an
	// immediate read of the still-unknown first row.
	restartedObserver := fixture.observerFor(fixture.desiredObservation())
	restarted := fixture.worker(t, restartedObserver)
	processed, err = restarted.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	assert.Equal(t, eventing.PRDevelopmentPublicationPublished,
		fixture.store.publication(fixture.publication.ID).Status)
	assert.Equal(t, 1, restartedObserver.calls())
}

func TestPublicationOutcomeReconciliationWorkerConvergesConcurrentObservations(t *testing.T) {
	t.Parallel()

	fixture := newPublicationOutcomeWorkerFixture()
	observer := fixture.observerFor(fixture.desiredObservation(), fixture.desiredObservation())
	observer.waitForCalls = 2
	first := fixture.worker(t, observer)
	second := fixture.worker(t, observer)

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, worker := range []*PublicationOutcomeReconciliationWorker{first, second} {
		go func(worker *PublicationOutcomeReconciliationWorker) {
			<-start
			processed, err := worker.ProcessOne(context.Background())
			if !processed && err == nil {
				err = errors.New("concurrent worker did not process the unknown outcome")
			}
			errs <- err
		}(worker)
	}
	close(start)
	for range 2 {
		require.NoError(t, <-errs)
	}
	stored := fixture.store.publication(fixture.publication.ID)
	assert.Equal(t, eventing.PRDevelopmentPublicationPublished, stored.Status)
	assert.Equal(t, 2, observer.calls())
	assert.Equal(t, 2, fixture.store.reconcileCalls)
}

func TestPublicationOutcomeReconciliationWorkerAcceptsLostCommitResponse(t *testing.T) {
	t.Parallel()

	fixture := newPublicationOutcomeWorkerFixture()
	fixture.store.commitThenError = context.DeadlineExceeded
	worker := fixture.worker(t, fixture.observerFor(fixture.desiredObservation()))

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	assert.True(t, processed)
	assert.Equal(t, eventing.PRDevelopmentPublicationPublished,
		fixture.store.publication(fixture.publication.ID).Status)
	assert.Empty(t, worker.retries)
}

func TestPublicationOutcomeReconciliationWorkerRejectsSubjectMismatchBeforeProvider(t *testing.T) {
	t.Parallel()

	fixture := newPublicationOutcomeWorkerFixture()
	fixture.store.thread.ID = "pdt_000000000000000000000000000000ff"
	observer := fixture.observerFor(fixture.desiredObservation())
	worker := fixture.worker(t, observer)

	processed, err := worker.ProcessOne(context.Background())
	assert.True(t, processed)
	assert.ErrorIs(t, err, eventing.ErrInvalidPRDevelopmentPublication)
	assert.Zero(t, observer.calls())
	assert.Zero(t, fixture.store.reconcileCalls)
}

func TestPublicationOutcomeReconciliationWorkerCancellationCreatesNoRetry(t *testing.T) {
	t.Parallel()

	fixture := newPublicationOutcomeWorkerFixture()
	observer := fixture.observerFor(fixture.desiredObservation())
	observer.block = true
	worker := fixture.worker(t, observer)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := worker.ProcessOne(ctx)
		done <- err
	}()
	require.Eventually(t, func() bool { return observer.calls() == 1 }, time.Second, time.Millisecond)
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
	assert.Empty(t, worker.retries)
	assert.Zero(t, fixture.store.reconcileCalls)
}

func TestPublicationOutcomeReconciliationWorkerBackoffAndRetryStateAreBounded(t *testing.T) {
	t.Parallel()

	fixture := newPublicationOutcomeWorkerFixture()
	worker := fixture.worker(t, fixture.observerFor(fixture.desiredObservation()))
	iterationStarted := fixture.now
	fixture.now = fixture.now.Add(30 * time.Second)
	worker.deferPublication("pdpub_failure_finished_later", iterationStarted)
	assert.Equal(
		t,
		fixture.now.Add(time.Second),
		worker.retries["pdpub_failure_finished_later"].dueAt,
		"backoff must start when the failed read finishes, not when the iteration began",
	)
	delete(worker.retries, "pdpub_failure_finished_later")
	for attempt := 1; attempt <= 10; attempt++ {
		worker.deferPublication(fixture.publication.ID, fixture.now)
		retry := worker.retries[fixture.publication.ID]
		assert.Equal(t, attempt, retry.attempt)
		assert.Equal(t, fixture.now.Add(PublicationRetryDelay(attempt)), retry.dueAt)
	}

	worker.retries = make(map[string]publicationOutcomeRetry)
	for index := 0; index <= publicationOutcomeReconciliationRetryCapacity; index++ {
		worker.deferPublication(fmt.Sprintf("pdpub_%032x", index), fixture.now)
	}
	assert.Len(t, worker.retries, publicationOutcomeReconciliationRetryCapacity)
	_, oldestRetained := worker.retries["pdpub_00000000000000000000000000000000"]
	assert.False(t, oldestRetained)
}

func TestPublicationOutcomeReconciliationWorkerConstructionAndCapabilitySurface(t *testing.T) {
	t.Parallel()

	_, err := NewPublicationOutcomeReconciliationWorker(
		PublicationOutcomeReconciliationWorkerConfig{},
	)
	assert.ErrorIs(t, err, ErrUnavailable)

	fixture := newPublicationOutcomeWorkerFixture()
	observer := fixture.observerFor(fixture.desiredObservation())
	config := PublicationOutcomeReconciliationWorkerConfig{
		Store: fixture.store, Observer: observer,
		BatchLimit: maximumPublicationOutcomeReconciliationBatchLimit + 100,
		Now:        func() time.Time { return fixture.now },
	}
	worker, err := NewPublicationOutcomeReconciliationWorker(config)
	require.NoError(t, err)
	assert.Equal(t, maximumPublicationOutcomeReconciliationBatchLimit, worker.batchLimit)
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encoded))

	storeType := reflect.TypeOf((*publicationOutcomeReconciliationStore)(nil)).Elem()
	methods := make([]string, 0, storeType.NumMethod())
	for index := 0; index < storeType.NumMethod(); index++ {
		methods = append(methods, storeType.Method(index).Name)
	}
	assert.Equal(t, []string{
		"ExpirePRDevelopmentPublicationPushes",
		"GetPRDevelopmentCase",
		"GetPRDevelopmentPublication",
		"GetPRDevelopmentThreadForCase",
		"ListPRDevelopmentPublicationUnknownOutcomes",
		"ReconcilePRDevelopmentPublicationOutcome",
	}, methods)
	workerType := reflect.TypeOf(worker)
	require.Equal(t, 1, workerType.NumMethod())
	assert.Equal(t, "ProcessOne", workerType.Method(0).Name)
}

type publicationOutcomeWorkerFixture struct {
	now         time.Time
	publication eventing.PRDevelopmentPublication
	store       *publicationOutcomeStoreFake
	observers   []*publicationOutcomeObserverFake
}

func newPublicationOutcomeWorkerFixture() *publicationOutcomeWorkerFixture {
	now := time.Date(2026, time.August, 11, 16, 0, 0, 0, time.UTC)
	createdAt := now.Add(-10 * time.Minute)
	providerObservedAt := now.Add(-5 * time.Minute)
	effectStartedAt := now.Add(-4 * time.Minute)
	completedAt := now.Add(-3 * time.Minute)
	provider := eventing.PRDevelopmentPublicationProviderObservation{
		Repository: "acme/project", PullNumber: 42,
		HeadRepository: "review-user/project", HeadRef: "repair/review-42",
		HeadSHA: strings.Repeat("a", 40), HeadCloneURL: "https://github.com/review-user/project.git",
		CurrentReviewState: eventing.PRDevelopmentReviewChangesRequested,
		ReviewDigest:       "sha256:" + strings.Repeat("b", 64),
	}
	request := eventing.PRDevelopmentPublicationPushRequest{
		Repository: "https://github.com/review-user/project.git",
		SourceRef:  provider.HeadRef, ExpectedSourceCommit: strings.Repeat("a", 40),
		WorkspaceID: "gw-publication-outcome", LineID: "pdln_00000000000000000000000000000001",
		ExpectedVersion: 2, ExpectedMutationEpoch: 2,
		ExpectedParkIntentID: "pdlnpark_00000000000000000000000000000001",
		ExpectedBase:         strings.Repeat("1", 40),
		ExpectedTip:          strings.Repeat("d", 40),
		ExpectedTree:         strings.Repeat("e", 40),
		ExpectedRemoteTip:    strings.Repeat("a", 40),
	}
	publication := eventing.PRDevelopmentPublication{
		ID:                      "pdpub_00000000000000000000000000000001",
		CaseID:                  "pdc_00000000000000000000000000000001",
		ThreadID:                "pdt_00000000000000000000000000000001",
		ProviderObservation:     provider,
		ProviderObservationHash: strings.Repeat("c", 64),
		ProviderObservedAt:      &providerObservedAt,
		PushRequest:             request,
		PushRequestHash:         strings.Repeat("a", 64),
		ExpectedRemoteTip:       request.ExpectedRemoteTip,
		Status:                  eventing.PRDevelopmentPublicationOutcomeUnknown,
		LastErrorCode:           eventing.PRDevelopmentPublicationErrorOutcomeUnknown,
		LastErrorDetail:         "outcome unknown",
		AvailableAt:             createdAt,
		CreatedAt:               createdAt,
		UpdatedAt:               completedAt,
		EffectStartedAt:         &effectStartedAt,
		CompletedAt:             &completedAt,
		Attempts:                1,
	}
	storedCase := eventing.PRDevelopmentCase{
		ID: publication.CaseID,
		PRDevelopmentCaptureInput: eventing.PRDevelopmentCaptureInput{
			Repository: provider.Repository, PullNumber: provider.PullNumber,
			HeadRepository: provider.HeadRepository, HeadRef: provider.HeadRef,
		},
	}
	thread := eventing.PRDevelopmentThread{
		ID: publication.ThreadID, Kind: eventing.PRDevelopmentThreadProvider,
		Identity: eventing.PRDevelopmentThreadIdentity{
			Provider: "github", ProviderOrigin: "https://github.com",
			PullAuthorID: "101", RepositoryID: "202", PullRequestID: "303",
			PullNumber: provider.PullNumber,
		},
		CaseCount: 1,
		Cases: []eventing.PRDevelopmentThreadCaseLink{{
			CaseID: publication.CaseID, Ordinal: 0,
		}},
	}
	store := &publicationOutcomeStoreFake{
		publications: map[string]eventing.PRDevelopmentPublication{
			publication.ID: publication,
		},
		caseValue: storedCase,
		thread:    thread,
	}
	return &publicationOutcomeWorkerFixture{
		now: now, publication: publication, store: store,
	}
}

func (fixture *publicationOutcomeWorkerFixture) desiredObservation() TimedPublicationRemoteObservation {
	provider := fixture.publication.ProviderObservation
	return TimedPublicationRemoteObservation{
		Observation: eventing.PRDevelopmentPublicationRemoteObservation{
			Repository: provider.Repository, PullNumber: provider.PullNumber,
			HeadRepository: provider.HeadRepository, HeadRef: provider.HeadRef,
			HeadSHA: fixture.publication.PushRequest.ExpectedTip,
		},
		ObservedAt: fixture.now,
	}
}

func (fixture *publicationOutcomeWorkerFixture) observerFor(
	observations ...TimedPublicationRemoteObservation,
) *publicationOutcomeObserverFake {
	observer := &publicationOutcomeObserverFake{observations: observations}
	fixture.observers = append(fixture.observers, observer)
	return observer
}

func (fixture *publicationOutcomeWorkerFixture) observerCalls() int {
	total := 0
	for _, observer := range fixture.observers {
		total += observer.calls()
	}
	return total
}

func (fixture *publicationOutcomeWorkerFixture) worker(
	t *testing.T,
	observer PublicationRemoteHeadObserver,
) *PublicationOutcomeReconciliationWorker {
	t.Helper()
	worker, err := NewPublicationOutcomeReconciliationWorker(
		PublicationOutcomeReconciliationWorkerConfig{
			Store: fixture.store, Observer: observer, BatchLimit: 1,
			Now: func() time.Time { return fixture.now },
		},
	)
	require.NoError(t, err)
	return worker
}

type publicationOutcomeStoreFake struct {
	mu              sync.Mutex
	publications    map[string]eventing.PRDevelopmentPublication
	caseValue       eventing.PRDevelopmentCase
	thread          eventing.PRDevelopmentThread
	expiryBatches   [][]eventing.PRDevelopmentPublication
	commitThenError error
	reconcileCalls  int
	ops             []string
}

func (store *publicationOutcomeStoreFake) ExpirePRDevelopmentPublicationPushes(
	_ context.Context,
	limit int,
) ([]eventing.PRDevelopmentPublication, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.ops = append(store.ops, "expire")
	if len(store.expiryBatches) == 0 {
		return nil, nil
	}
	expired := append([]eventing.PRDevelopmentPublication(nil), store.expiryBatches[0]...)
	store.expiryBatches = store.expiryBatches[1:]
	if len(expired) > limit {
		return expired, nil
	}
	for _, publication := range expired {
		store.publications[publication.ID] = publication
	}
	return expired, nil
}

func (store *publicationOutcomeStoreFake) ListPRDevelopmentPublicationUnknownOutcomes(
	_ context.Context,
	filter eventing.PRDevelopmentPublicationUnknownOutcomeFilter,
) (eventing.PRDevelopmentPublicationUnknownOutcomePage, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.ops = append(store.ops, "list")
	limit := filter.Limit
	if limit < 1 {
		limit = 1
	}
	rows := make([]eventing.PRDevelopmentPublication, 0, len(store.publications))
	for _, publication := range store.publications {
		if publication.Status != eventing.PRDevelopmentPublicationOutcomeUnknown {
			continue
		}
		if filter.After != nil && !publicationAfterOutcomeCursor(publication, *filter.After) {
			continue
		}
		rows = append(rows, publication)
	}
	sort.Slice(rows, func(left, right int) bool {
		if !rows[left].AvailableAt.Equal(rows[right].AvailableAt) {
			return rows[left].AvailableAt.Before(rows[right].AvailableAt)
		}
		if !rows[left].CreatedAt.Equal(rows[right].CreatedAt) {
			return rows[left].CreatedAt.Before(rows[right].CreatedAt)
		}
		return rows[left].ID < rows[right].ID
	})
	page := eventing.PRDevelopmentPublicationUnknownOutcomePage{}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		page.Next = publicationUnknownOutcomeCursor(last)
	}
	page.Publications = append([]eventing.PRDevelopmentPublication(nil), rows...)
	return page, nil
}

func publicationAfterOutcomeCursor(
	publication eventing.PRDevelopmentPublication,
	cursor eventing.PRDevelopmentPublicationUnknownOutcomeCursor,
) bool {
	if !publication.AvailableAt.Equal(cursor.AvailableAt) {
		return publication.AvailableAt.After(cursor.AvailableAt)
	}
	if !publication.CreatedAt.Equal(cursor.CreatedAt) {
		return publication.CreatedAt.After(cursor.CreatedAt)
	}
	return publication.ID > cursor.ID
}

func (store *publicationOutcomeStoreFake) GetPRDevelopmentPublication(
	_ context.Context,
	publicationID string,
) (eventing.PRDevelopmentPublication, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	publication, ok := store.publications[publicationID]
	if !ok {
		return eventing.PRDevelopmentPublication{}, eventing.ErrNotFound
	}
	return publication, nil
}

func (store *publicationOutcomeStoreFake) GetPRDevelopmentCase(
	_ context.Context,
	caseID string,
) (eventing.PRDevelopmentCase, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.ops = append(store.ops, "case")
	if caseID != store.caseValue.ID {
		return eventing.PRDevelopmentCase{}, eventing.ErrNotFound
	}
	return store.caseValue, nil
}

func (store *publicationOutcomeStoreFake) GetPRDevelopmentThreadForCase(
	_ context.Context,
	caseID string,
) (eventing.PRDevelopmentThread, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.ops = append(store.ops, "thread")
	if caseID != store.caseValue.ID {
		return eventing.PRDevelopmentThread{}, eventing.ErrNotFound
	}
	return store.thread, nil
}

func (store *publicationOutcomeStoreFake) ReconcilePRDevelopmentPublicationOutcome(
	_ context.Context,
	input eventing.PRDevelopmentPublicationOutcomeReconciliation,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.ops = append(store.ops, "reconcile")
	store.reconcileCalls++
	current, ok := store.publications[input.PublicationID]
	if !ok {
		return eventing.PRDevelopmentPublication{}, false, eventing.ErrNotFound
	}
	if current.Status == eventing.PRDevelopmentPublicationPublished {
		if current.ReconciliationObservation == input.Observation &&
			current.ReconciliationObservedAt != nil &&
			current.ReconciliationObservedAt.Equal(input.ObservedAt) &&
			current.PushResult == input.Result {
			return current, false, nil
		}
		return eventing.PRDevelopmentPublication{}, false,
			eventing.ErrPRDevelopmentPublicationConflict
	}
	if current.Status != eventing.PRDevelopmentPublicationOutcomeUnknown ||
		current.PushRequestHash != input.RequestHash ||
		input.Observation.HeadSHA != current.PushRequest.ExpectedTip {
		return eventing.PRDevelopmentPublication{}, false,
			eventing.ErrPRDevelopmentPublicationConflict
	}
	current.Status = eventing.PRDevelopmentPublicationPublished
	current.ReconciliationObservation = input.Observation
	current.ReconciliationObservationHash = strings.Repeat("d", 64)
	current.ReconciliationObservedAt = timePointer(input.ObservedAt)
	current.PushResult = input.Result
	current.PushResultHash = strings.Repeat("e", 64)
	current.PushDisposition = input.Result.Disposition
	current.WorkspaceClean = input.Result.WorkspaceClean
	current.LocalDrift = false
	current.LastErrorCode = ""
	current.LastErrorDetail = ""
	store.publications[current.ID] = current
	if store.commitThenError != nil {
		err := store.commitThenError
		store.commitThenError = nil
		return eventing.PRDevelopmentPublication{}, false, err
	}
	return current, true, nil
}

func (store *publicationOutcomeStoreFake) publication(
	publicationID string,
) eventing.PRDevelopmentPublication {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.publications[publicationID]
}

func (store *publicationOutcomeStoreFake) operations() []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]string(nil), store.ops...)
}

type publicationOutcomeObserverFake struct {
	mu           sync.Mutex
	observations []TimedPublicationRemoteObservation
	err          error
	callCount    int
	waitForCalls int
	release      chan struct{}
	block        bool
}

func (observer *publicationOutcomeObserverFake) ObservePublicationRemoteHead(
	ctx context.Context,
	_ eventing.PRDevelopmentCase,
	_ eventing.PRDevelopmentThreadIdentity,
) (TimedPublicationRemoteObservation, error) {
	observer.mu.Lock()
	observer.callCount++
	call := observer.callCount
	if observer.waitForCalls > 0 && observer.release == nil {
		observer.release = make(chan struct{})
	}
	release := observer.release
	if observer.waitForCalls > 0 && call == observer.waitForCalls {
		close(observer.release)
	}
	block := observer.block
	err := observer.err
	var observation TimedPublicationRemoteObservation
	if len(observer.observations) != 0 {
		index := call - 1
		if index >= len(observer.observations) {
			index = len(observer.observations) - 1
		}
		observation = observer.observations[index]
		observation.ObservedAt = observation.ObservedAt.Add(time.Duration(call) * time.Nanosecond)
	}
	observer.mu.Unlock()

	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return TimedPublicationRemoteObservation{}, ctx.Err()
		}
	}
	if block {
		<-ctx.Done()
		return TimedPublicationRemoteObservation{}, ctx.Err()
	}
	if err != nil {
		return TimedPublicationRemoteObservation{}, err
	}
	return observation, nil
}

func (observer *publicationOutcomeObserverFake) calls() int {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.callCount
}
