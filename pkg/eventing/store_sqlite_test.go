//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func newMutableClock(now time.Time) *mutableClock {
	return &mutableClock{now: now.UTC()}
}

func (clock *mutableClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *mutableClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

func (clock *mutableClock) Set(now time.Time) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = now.UTC()
}

func testEnvelope(dedupeKey string) Envelope {
	return Envelope{
		Source:    "github",
		Connector: "production",
		Type:      "issues.opened",
		DedupeKey: dedupeKey,
		Payload:   json.RawMessage(`{"action":"opened"}`),
		Attributes: map[string]string{
			"environment": "test",
		},
		Actor: &Actor{ID: "user-1", Type: "user"},
		Subject: &Subject{
			ID:   "repository-1",
			Type: "repository",
		},
	}
}

func openTestStore(t *testing.T, clock *mutableClock, options ...Option) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "eventing", "events.db")
	allOptions := make([]Option, 1, 1+len(options))
	allOptions[0] = WithClock(clock.Now)
	allOptions = append(allOptions, options...)
	store, err := Open(context.Background(), path, allOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store, path
}

func TestStoreInsertRedactsDeduplicatesAndPersists(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store, path := openTestStore(t, clock,
		WithRedaction([]string{"tenantCredential"}, []string{"known-secret"}),
	)

	input := testEnvelope("delivery-1")
	input.Payload = json.RawMessage(`{
		"authorization":"Bearer token",
		"message":"contains known-secret",
		"nested":{"tenant-credential":"value"}
	}`)
	input.Attributes["apiKey"] = "value"
	first, err := store.Insert(context.Background(), input)
	require.NoError(t, err)
	assert.True(t, first.Inserted)
	assert.Equal(t, RoutingPending, first.Event.Routing.Status)
	assert.Equal(t, now, first.Event.Routing.AvailableAt)
	assert.JSONEq(t, `{
		"authorization":"[REDACTED]",
		"message":"contains [REDACTED]",
		"nested":{"tenant-credential":"[REDACTED]"}
	}`, string(first.Event.Envelope.Payload))
	assert.Equal(t, RedactedValue, first.Event.Envelope.Attributes["apiKey"])

	input.Payload[2] = 'X'
	input.Attributes["environment"] = "mutated"
	got, err := store.Get(context.Background(), first.Event.Envelope.ID)
	require.NoError(t, err)
	assert.Equal(t, "test", got.Envelope.Attributes["environment"])
	assert.JSONEq(t, `{
		"authorization":"[REDACTED]",
		"message":"contains [REDACTED]",
		"nested":{"tenant-credential":"[REDACTED]"}
	}`, string(got.Envelope.Payload))

	duplicateInput := testEnvelope("delivery-1")
	duplicateInput.Payload = json.RawMessage(`{"different":"ignored"}`)
	duplicate, err := store.Insert(context.Background(), duplicateInput)
	require.NoError(t, err)
	assert.False(t, duplicate.Inserted)
	assert.Equal(t, first.Event.Envelope.ID, duplicate.Event.Envelope.ID)
	assert.JSONEq(t, string(first.Event.Envelope.Payload), string(duplicate.Event.Envelope.Payload))

	require.NoError(t, store.Close())
	reopened, err := Open(context.Background(), path, WithClock(clock.Now))
	require.NoError(t, err)
	defer reopened.Close()
	persisted, err := reopened.Get(context.Background(), first.Event.Envelope.ID)
	require.NoError(t, err)
	assert.Equal(t, first.Event.Envelope.ID, persisted.Envelope.ID)

	var version int
	require.NoError(t, reopened.db.QueryRow("PRAGMA user_version").Scan(&version))
	assert.Equal(t, schemaVersion, version)
	assertConnectionPragmas(t, reopened, 5*time.Second)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	parentInfo, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), parentInfo.Mode().Perm())
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar, statErr := os.Stat(path + suffix)
		if statErr == nil {
			assert.Equal(t, os.FileMode(0o600), sidecar.Mode().Perm(), suffix)
		} else {
			assert.True(t, os.IsNotExist(statErr), statErr)
		}
	}
}

func TestStoreConnectionReplacementPreservesPragmas(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "literal?database.db")
	store, err := Open(
		context.Background(),
		path,
		WithBusyTimeout(137*time.Millisecond),
	)
	require.NoError(t, err)
	defer store.Close()
	_, err = store.Insert(context.Background(), testEnvelope("replacement"))
	require.NoError(t, err)
	assertConnectionPragmas(t, store, 137*time.Millisecond)

	// Reducing the idle limit closes the sole idle connection. The next query
	// must open a replacement and receive the DSN-backed connection PRAGMAs.
	store.db.SetMaxIdleConns(0)
	store.db.SetMaxIdleConns(1)
	assertConnectionPragmas(t, store, 137*time.Millisecond)

	_, err = os.Stat(path)
	require.NoError(t, err, "query characters must remain part of the filesystem path")
}

func TestStoreRejectsSQLiteURIPath(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"file:/tmp/events.db",
		"file::memory:?cache=shared",
		"FILE:events.db",
	} {
		_, err := Open(context.Background(), path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "filesystem path")
	}
}

func TestSQLiteFileURLPortableEncoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		volume     string
		wantPrefix string
	}{
		{
			name:       "posix",
			path:       "/tmp/literal?name/events.db",
			wantPrefix: "file:///tmp/literal%3Fname/events.db",
		},
		{
			name:       "windows drive",
			path:       "C:/Users/Test User/events.db",
			volume:     "C:",
			wantPrefix: "file:///C:/Users/Test%20User/events.db",
		},
		{
			name:       "windows UNC",
			path:       "//server/share/event data/events.db",
			volume:     "//server/share",
			wantPrefix: "file:////server/share/event%20data/events.db",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			databaseURL, err := sqliteFileURL(test.path, test.volume)
			require.NoError(t, err)
			assert.Equal(t, test.wantPrefix, databaseURL.String())
		})
	}
}

func assertConnectionPragmas(t *testing.T, store *Store, busyTimeout time.Duration) {
	t.Helper()
	var journalMode string
	require.NoError(t, store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode))
	assert.Equal(t, "wal", strings.ToLower(journalMode))
	var foreignKeys, gotBusyTimeout, synchronous int
	require.NoError(t, store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys))
	require.NoError(t, store.db.QueryRow("PRAGMA busy_timeout").Scan(&gotBusyTimeout))
	require.NoError(t, store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous))
	assert.Equal(t, 1, foreignKeys)
	assert.Equal(t, int(busyTimeout.Milliseconds()), gotBusyTimeout)
	assert.Equal(t, 1, synchronous)
}

func TestStoreCrossInstanceDeduplicationAndClaims(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	path := filepath.Join(t.TempDir(), "events.db")
	firstStore, err := Open(context.Background(), path, WithClock(clock.Now))
	require.NoError(t, err)
	defer firstStore.Close()
	secondStore, err := Open(context.Background(), path, WithClock(clock.Now))
	require.NoError(t, err)
	defer secondStore.Close()

	type insertOutcome struct {
		result InsertResult
		err    error
	}
	insertOutcomes := make(chan insertOutcome, 2)
	start := make(chan struct{})
	for _, store := range []*Store{firstStore, secondStore} {
		go func(store *Store) {
			<-start
			result, insertErr := store.Insert(
				context.Background(), testEnvelope("cross-instance"),
			)
			insertOutcomes <- insertOutcome{result: result, err: insertErr}
		}(store)
	}
	close(start)
	var eventID string
	insertedCount := 0
	for i := 0; i < 2; i++ {
		outcome := <-insertOutcomes
		require.NoError(t, outcome.err)
		if outcome.result.Inserted {
			insertedCount++
		}
		if eventID == "" {
			eventID = outcome.result.Event.Envelope.ID
		}
		assert.Equal(t, eventID, outcome.result.Event.Envelope.ID)
	}
	assert.Equal(t, 1, insertedCount)

	type routingOutcome struct {
		events []StoredEvent
		err    error
	}
	routingOutcomes := make(chan routingOutcome, 2)
	start = make(chan struct{})
	for i, store := range []*Store{firstStore, secondStore} {
		go func(index int, store *Store) {
			<-start
			events, claimErr := store.ClaimRouting(
				context.Background(), fmt.Sprintf("router-%d", index), 1, time.Minute,
			)
			routingOutcomes <- routingOutcome{events: events, err: claimErr}
		}(i, store)
	}
	close(start)
	var routingClaim StoredEvent
	routingClaimCount := 0
	for i := 0; i < 2; i++ {
		outcome := <-routingOutcomes
		require.NoError(t, outcome.err)
		routingClaimCount += len(outcome.events)
		if len(outcome.events) == 1 {
			routingClaim = outcome.events[0]
		}
	}
	assert.Equal(t, 1, routingClaimCount)
	assert.Equal(t, eventID, routingClaim.Envelope.ID)
	assert.NotEmpty(t, routingClaim.Routing.LeaseToken)

	dispatch, created, err := firstStore.CreateDispatch(
		context.Background(), eventID, "cross-instance-workflow",
	)
	require.NoError(t, err)
	assert.True(t, created)

	type dispatchOutcome struct {
		dispatches []Dispatch
		err        error
	}
	dispatchOutcomes := make(chan dispatchOutcome, 2)
	start = make(chan struct{})
	for i, store := range []*Store{firstStore, secondStore} {
		go func(index int, store *Store) {
			<-start
			dispatches, claimErr := store.ClaimDispatches(
				context.Background(), fmt.Sprintf("dispatcher-%d", index), 1, time.Minute,
			)
			dispatchOutcomes <- dispatchOutcome{dispatches: dispatches, err: claimErr}
		}(i, store)
	}
	close(start)
	dispatchClaimCount := 0
	var dispatchClaim Dispatch
	for i := 0; i < 2; i++ {
		outcome := <-dispatchOutcomes
		require.NoError(t, outcome.err)
		dispatchClaimCount += len(outcome.dispatches)
		if len(outcome.dispatches) == 1 {
			dispatchClaim = outcome.dispatches[0]
		}
	}
	assert.Equal(t, 1, dispatchClaimCount)
	assert.Equal(t, dispatch.ID, dispatchClaim.ID)
	assert.NotEmpty(t, dispatchClaim.LeaseToken)
}

func TestStoreConcurrentDeduplication(t *testing.T) {
	t.Parallel()

	clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
	store, _ := openTestStore(t, clock)
	const workers = 32
	var inserted atomic.Int32
	ids := make(chan string, workers)
	errorsSeen := make(chan error, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.Insert(context.Background(), testEnvelope("same-delivery"))
			if err != nil {
				errorsSeen <- err
				return
			}
			if result.Inserted {
				inserted.Add(1)
			}
			ids <- result.Event.Envelope.ID
		}()
	}
	wait.Wait()
	close(ids)
	close(errorsSeen)
	for err := range errorsSeen {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), inserted.Load())
	var firstID string
	for id := range ids {
		if firstID == "" {
			firstID = id
		}
		assert.Equal(t, firstID, id)
	}
}

func TestStoreRoutingLeaseFencingAndRetrySchedule(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store, _ := openTestStore(t, clock, WithRedaction(nil, []string{"routing-secret"}))
	inserted, err := store.Insert(context.Background(), testEnvelope("routing-1"))
	require.NoError(t, err)

	claimed, err := store.ClaimRouting(context.Background(), "router", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	firstToken := claimed[0].Routing.LeaseToken
	assert.NotEmpty(t, firstToken)
	assert.Equal(t, 1, claimed[0].Routing.Attempts)

	retryAt := now.Add(5 * time.Minute)
	require.NoError(t, store.NackRouting(
		context.Background(),
		inserted.Event.Envelope.ID,
		firstToken,
		retryAt,
		"failure routing-secret",
	))
	pending, err := store.Get(context.Background(), inserted.Event.Envelope.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingPending, pending.Routing.Status)
	assert.Equal(t, retryAt, pending.Routing.AvailableAt)
	assert.Equal(t, "failure [REDACTED]", pending.Routing.LastError)

	claimed, err = store.ClaimRouting(context.Background(), "router", 1, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, claimed)

	clock.Set(retryAt)
	claimed, err = store.ClaimRouting(context.Background(), "router", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	secondToken := claimed[0].Routing.LeaseToken
	assert.NotEqual(t, firstToken, secondToken)
	assert.Equal(t, 2, claimed[0].Routing.Attempts)
	assert.ErrorIs(t,
		store.AckRouting(context.Background(), inserted.Event.Envelope.ID, firstToken),
		ErrStaleLease,
	)
	require.NoError(t, store.AckRouting(
		context.Background(), inserted.Event.Envelope.ID, secondToken,
	))
	finished, err := store.Get(context.Background(), inserted.Event.Envelope.ID)
	require.NoError(t, err)
	assert.Equal(t, RoutingSucceeded, finished.Routing.Status)
	assert.Empty(t, finished.Routing.LeaseToken)
}

func TestStoreExpiredRoutingClaimRejectsStaleWorker(t *testing.T) {
	t.Parallel()

	clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
	store, _ := openTestStore(t, clock)
	inserted, err := store.Insert(context.Background(), testEnvelope("routing-expired"))
	require.NoError(t, err)
	first, err := store.ClaimRouting(context.Background(), "same-worker", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, first, 1)
	clock.Advance(time.Minute)
	second, err := store.ClaimRouting(context.Background(), "same-worker", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.NotEqual(t, first[0].Routing.LeaseToken, second[0].Routing.LeaseToken)
	assert.ErrorIs(t, store.DeadRouting(
		context.Background(),
		inserted.Event.Envelope.ID,
		first[0].Routing.LeaseToken,
		"late",
	), ErrStaleLease)
	require.NoError(t, store.DeadRouting(
		context.Background(),
		inserted.Event.Envelope.ID,
		second[0].Routing.LeaseToken,
		"permanent",
	))
}

func TestStoreRenewRoutingLeaseExtendsLiveClaim(t *testing.T) {
	t.Parallel()

	clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
	store, _ := openTestStore(t, clock)
	inserted, err := store.Insert(context.Background(), testEnvelope("routing-renew"))
	require.NoError(t, err)
	claimed, err := store.ClaimRouting(context.Background(), "router", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	clock.Advance(30 * time.Second)
	require.NoError(t, store.RenewRoutingLease(
		context.Background(),
		inserted.Event.Envelope.ID,
		claimed[0].Routing.LeaseToken,
		2*time.Minute,
	))
	renewed, err := store.Get(context.Background(), inserted.Event.Envelope.ID)
	require.NoError(t, err)
	require.NotNil(t, renewed.Routing.LeaseUntil)
	assert.Equal(t, clock.Now().Add(2*time.Minute), *renewed.Routing.LeaseUntil)

	clock.Advance(90 * time.Second)
	reclaimed, err := store.ClaimRouting(context.Background(), "competitor", 1, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, reclaimed)
	require.NoError(t, store.AckRouting(
		context.Background(),
		inserted.Event.Envelope.ID,
		claimed[0].Routing.LeaseToken,
	))
}

func TestStoreClaimedDispatchCreationFencesStaleRouter(t *testing.T) {
	t.Parallel()

	clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
	store, _ := openTestStore(t, clock)
	inserted, err := store.Insert(context.Background(), testEnvelope("routing-dispatch-fence"))
	require.NoError(t, err)
	first, err := store.ClaimRouting(context.Background(), "router-a", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, first, 1)

	firstDispatch, created, err := store.CreateDispatchForRoutingClaim(
		context.Background(),
		inserted.Event.Envelope.ID,
		first[0].Routing.LeaseToken,
		"workflows/first.yaml",
	)
	require.NoError(t, err)
	require.True(t, created)
	duplicate, created, err := store.CreateDispatchForRoutingClaim(
		context.Background(),
		inserted.Event.Envelope.ID,
		first[0].Routing.LeaseToken,
		"workflows/first.yaml",
	)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, firstDispatch.ID, duplicate.ID)

	clock.Advance(time.Minute)
	second, err := store.ClaimRouting(context.Background(), "router-b", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.ErrorIs(t, func() error {
		_, _, createErr := store.CreateDispatchForRoutingClaim(
			context.Background(),
			inserted.Event.Envelope.ID,
			first[0].Routing.LeaseToken,
			"workflows/stale.yaml",
		)
		return createErr
	}(), ErrStaleLease)

	page, err := store.ListDispatches(
		context.Background(),
		DispatchFilter{EventID: inserted.Event.Envelope.ID},
	)
	require.NoError(t, err)
	require.Len(t, page.Dispatches, 1)
	assert.Equal(t, "workflows/first.yaml", page.Dispatches[0].WorkflowRef)

	_, created, err = store.CreateDispatchForRoutingClaim(
		context.Background(),
		inserted.Event.Envelope.ID,
		second[0].Routing.LeaseToken,
		"workflows/current.yaml",
	)
	require.NoError(t, err)
	assert.True(t, created)
	require.NoError(t, store.AckRouting(
		context.Background(),
		inserted.Event.Envelope.ID,
		second[0].Routing.LeaseToken,
	))
	assert.ErrorIs(t, func() error {
		_, _, createErr := store.CreateDispatchForRoutingClaim(
			context.Background(),
			inserted.Event.Envelope.ID,
			second[0].Routing.LeaseToken,
			"workflows/after-ack.yaml",
		)
		return createErr
	}(), ErrStaleLease)
}

func TestStoreRevisionedDispatchCreationBindsFirstSelectionAtomically(t *testing.T) {
	t.Parallel()

	clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
	store, _ := openTestStore(t, clock)
	inserted, err := store.Insert(context.Background(), testEnvelope("revisioned-routing"))
	require.NoError(t, err)
	claimed, err := store.ClaimRouting(context.Background(), "router", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	const firstRevision = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dispatch, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		context.Background(),
		inserted.Event.Envelope.ID,
		claimed[0].Routing.LeaseToken,
		"workflows/revisioned.yaml",
		firstRevision,
	)
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, firstRevision, dispatch.WorkflowRevision)

	duplicate, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		context.Background(),
		inserted.Event.Envelope.ID,
		claimed[0].Routing.LeaseToken,
		"workflows/revisioned.yaml",
		"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, firstRevision, duplicate.WorkflowRevision)

	listed, err := store.ListDispatchMetadata(
		context.Background(),
		DispatchFilter{EventID: inserted.Event.Envelope.ID},
	)
	require.NoError(t, err)
	require.Len(t, listed.Dispatches, 1)
	assert.Equal(t, firstRevision, listed.Dispatches[0].WorkflowRevision)
}

func TestStoreDispatchLifecycleAndUniqueness(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store, _ := openTestStore(t, clock, WithRedaction(nil, []string{"dispatch-secret"}))
	event, err := store.Insert(context.Background(), testEnvelope("dispatch-1"))
	require.NoError(t, err)

	dispatch, created, err := store.CreateDispatch(
		context.Background(), event.Event.Envelope.ID, "workflows/github-triage.yaml",
	)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Regexp(t, `^dsp_[0-9a-f]{32}$`, dispatch.ID)
	assert.Regexp(t, `^wr_[0-9a-f]{32}$`, dispatch.RunID)
	assert.Equal(t, DispatchPending, dispatch.Status)
	duplicate, created, err := store.CreateDispatch(
		context.Background(), event.Event.Envelope.ID, "workflows/github-triage.yaml",
	)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, dispatch.ID, duplicate.ID)
	assert.Equal(t, dispatch.RunID, duplicate.RunID)

	claimed, err := store.ClaimDispatches(context.Background(), "dispatcher", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	firstToken := claimed[0].LeaseToken
	assert.ErrorIs(t, store.LinkDispatchRun(
		context.Background(), dispatch.ID, firstToken, "wr_wrong",
	), ErrRunIDMismatch)
	require.NoError(t, store.LinkDispatchRun(
		context.Background(), dispatch.ID, firstToken, dispatch.RunID,
	))

	clock.Advance(time.Minute)
	reclaimed, err := store.ClaimDispatches(context.Background(), "dispatcher", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	secondToken := reclaimed[0].LeaseToken
	assert.NotEqual(t, firstToken, secondToken)
	assert.ErrorIs(t, store.FinishDispatch(
		context.Background(), dispatch.ID, firstToken, DispatchSucceeded, "",
	), ErrStaleLease)

	retryAt := clock.Now().Add(10 * time.Minute)
	require.NoError(t, store.NackDispatch(
		context.Background(), dispatch.ID, secondToken, retryAt,
		"transient dispatch-secret",
	))
	pending, err := store.GetDispatch(context.Background(), dispatch.ID)
	require.NoError(t, err)
	assert.Equal(t, DispatchPending, pending.Status)
	assert.Equal(t, retryAt, pending.AvailableAt)
	assert.Equal(t, "transient [REDACTED]", pending.LastError)
	none, err := store.ClaimDispatches(context.Background(), "dispatcher", 1, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, none)

	clock.Set(retryAt)
	reclaimed, err = store.ClaimDispatches(context.Background(), "dispatcher", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	require.NoError(t, store.FinishDispatch(
		context.Background(), dispatch.ID, reclaimed[0].LeaseToken,
		DispatchFailed, strings.Repeat("é", maxErrorDetailBytes),
	))
	finished, err := store.GetDispatch(context.Background(), dispatch.ID)
	require.NoError(t, err)
	assert.Equal(t, DispatchFailed, finished.Status)
	assert.NotNil(t, finished.FinishedAt)
	assert.LessOrEqual(t, len(finished.LastError), maxErrorDetailBytes)
	assert.True(t, json.Valid([]byte(fmt.Sprintf("%q", finished.LastError))))
}

func TestStoreRenewDispatchLeaseExtendsClaimedAndRunningWork(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store, _ := openTestStore(t, clock)
	event, err := store.Insert(context.Background(), testEnvelope("renew-dispatch"))
	require.NoError(t, err)
	dispatch, created, err := store.CreateDispatch(
		context.Background(), event.Event.Envelope.ID, "workflows/renew.yaml",
	)
	require.NoError(t, err)
	require.True(t, created)
	claimed, err := store.ClaimDispatches(context.Background(), "dispatcher", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	token := claimed[0].LeaseToken

	clock.Advance(30 * time.Second)
	require.NoError(t, store.RenewDispatchLease(
		context.Background(), dispatch.ID, token, 2*time.Minute,
	))
	renewed, err := store.GetDispatch(context.Background(), dispatch.ID)
	require.NoError(t, err)
	require.NotNil(t, renewed.LeaseUntil)
	assert.Equal(t, clock.Now().Add(2*time.Minute), *renewed.LeaseUntil)
	assert.Equal(t, clock.Now(), renewed.UpdatedAt)
	assert.Equal(t, DispatchClaimed, renewed.Status)
	assert.Equal(t, token, renewed.LeaseToken)
	assert.Equal(t, 1, renewed.Attempts)

	// The original one-minute lease has expired, but the renewed lease still
	// owns the dispatch and can transition it to running.
	clock.Advance(90 * time.Second)
	require.NoError(t, store.LinkDispatchRun(
		context.Background(), dispatch.ID, token, dispatch.RunID,
	))
	clock.Advance(15 * time.Second)
	require.NoError(t, store.RenewDispatchLease(
		context.Background(), dispatch.ID, token, 3*time.Minute,
	))
	running, err := store.GetDispatch(context.Background(), dispatch.ID)
	require.NoError(t, err)
	require.NotNil(t, running.LeaseUntil)
	assert.Equal(t, clock.Now().Add(3*time.Minute), *running.LeaseUntil)
	assert.Equal(t, clock.Now(), running.UpdatedAt)
	assert.Equal(t, DispatchRunning, running.Status)
	assert.Equal(t, token, running.LeaseToken)
}

func TestStoreRenewDispatchLeaseRejectsUnownedExpiredAndInactiveWork(t *testing.T) {
	t.Parallel()

	t.Run("wrong lease token", func(t *testing.T) {
		t.Parallel()

		clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
		store, _ := openTestStore(t, clock)
		event, err := store.Insert(context.Background(), testEnvelope("renew-wrong-token"))
		require.NoError(t, err)
		dispatch, _, err := store.CreateDispatch(
			context.Background(), event.Event.Envelope.ID, "workflows/wrong-token.yaml",
		)
		require.NoError(t, err)
		claimed, err := store.ClaimDispatches(
			context.Background(), "dispatcher", 1, time.Minute,
		)
		require.NoError(t, err)
		require.Len(t, claimed, 1)

		assert.ErrorIs(t, store.RenewDispatchLease(
			context.Background(), dispatch.ID, claimed[0].LeaseToken+"-wrong", time.Minute,
		), ErrStaleLease)
		unchanged, err := store.GetDispatch(context.Background(), dispatch.ID)
		require.NoError(t, err)
		assert.Equal(t, claimed[0].LeaseUntil, unchanged.LeaseUntil)
		assert.Equal(t, claimed[0].UpdatedAt, unchanged.UpdatedAt)
	})

	t.Run("expired lease", func(t *testing.T) {
		t.Parallel()

		clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
		store, _ := openTestStore(t, clock)
		event, err := store.Insert(context.Background(), testEnvelope("renew-expired"))
		require.NoError(t, err)
		dispatch, _, err := store.CreateDispatch(
			context.Background(), event.Event.Envelope.ID, "workflows/expired.yaml",
		)
		require.NoError(t, err)
		claimed, err := store.ClaimDispatches(
			context.Background(), "dispatcher", 1, time.Minute,
		)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		clock.Advance(time.Minute)

		assert.ErrorIs(t, store.RenewDispatchLease(
			context.Background(), dispatch.ID, claimed[0].LeaseToken, time.Minute,
		), ErrStaleLease)
	})

	t.Run("pending dispatch", func(t *testing.T) {
		t.Parallel()

		clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
		store, _ := openTestStore(t, clock)
		event, err := store.Insert(context.Background(), testEnvelope("renew-pending"))
		require.NoError(t, err)
		dispatch, _, err := store.CreateDispatch(
			context.Background(), event.Event.Envelope.ID, "workflows/pending.yaml",
		)
		require.NoError(t, err)

		assert.ErrorIs(t, store.RenewDispatchLease(
			context.Background(), dispatch.ID, "lease_not_an_owner", time.Minute,
		), ErrStaleLease)
	})

	t.Run("terminal dispatch", func(t *testing.T) {
		t.Parallel()

		clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
		store, _ := openTestStore(t, clock)
		event, err := store.Insert(context.Background(), testEnvelope("renew-terminal"))
		require.NoError(t, err)
		dispatch, _, err := store.CreateDispatch(
			context.Background(), event.Event.Envelope.ID, "workflows/terminal.yaml",
		)
		require.NoError(t, err)
		claimed, err := store.ClaimDispatches(
			context.Background(), "dispatcher", 1, time.Minute,
		)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		token := claimed[0].LeaseToken
		require.NoError(t, store.FinishDispatch(
			context.Background(), dispatch.ID, token, DispatchSucceeded, "",
		))

		assert.ErrorIs(t, store.RenewDispatchLease(
			context.Background(), dispatch.ID, token, time.Minute,
		), ErrStaleLease)
	})

	t.Run("missing dispatch", func(t *testing.T) {
		t.Parallel()

		clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
		store, _ := openTestStore(t, clock)
		assert.ErrorIs(t, store.RenewDispatchLease(
			context.Background(),
			"dsp_00000000000000000000000000000000",
			"lease_dispatcher_00000000000000000000000000000000",
			time.Minute,
		), ErrNotFound)
	})
}

func TestStoreRenewDispatchLeaseValidatesArguments(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store, _ := openTestStore(t, clock)
	event, err := store.Insert(context.Background(), testEnvelope("renew-invalid"))
	require.NoError(t, err)
	dispatch, _, err := store.CreateDispatch(
		context.Background(), event.Event.Envelope.ID, "workflows/invalid.yaml",
	)
	require.NoError(t, err)
	claimed, err := store.ClaimDispatches(context.Background(), "dispatcher", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	for _, testCase := range []struct {
		name       string
		id         string
		leaseToken string
		lease      time.Duration
	}{
		{"empty ID", "", claimed[0].LeaseToken, time.Minute},
		{"blank ID", " \t", claimed[0].LeaseToken, time.Minute},
		{"empty token", dispatch.ID, "", time.Minute},
		{"blank token", dispatch.ID, " \t", time.Minute},
		{"zero duration", dispatch.ID, claimed[0].LeaseToken, 0},
		{"negative lease", dispatch.ID, claimed[0].LeaseToken, -time.Second},
	} {
		assert.EqualError(
			t,
			store.RenewDispatchLease(
				context.Background(), testCase.id, testCase.leaseToken, testCase.lease,
			),
			"dispatch ID, lease token, and positive lease are required",
			testCase.name,
		)
	}

	require.NoError(t, store.RenewDispatchLease(
		context.Background(), dispatch.ID, claimed[0].LeaseToken, time.Minute,
	))
}

func TestStoreRenewDispatchLeaseRejectsOutOfRangeDeadline(t *testing.T) {
	t.Parallel()

	now := time.Date(2260, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store, _ := openTestStore(t, clock)
	event, err := store.Insert(context.Background(), testEnvelope("renew-overflow"))
	require.NoError(t, err)
	dispatch, _, err := store.CreateDispatch(
		context.Background(), event.Event.Envelope.ID, "workflows/overflow.yaml",
	)
	require.NoError(t, err)
	claimed, err := store.ClaimDispatches(context.Background(), "dispatcher", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	assert.ErrorIs(t, store.RenewDispatchLease(
		context.Background(), dispatch.ID, claimed[0].LeaseToken,
		time.Duration(1<<63-1),
	), ErrTimestampOutOfRange)
}

func TestStoreListFiltersAndKeysetPagination(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	clock := newMutableClock(base)
	store, _ := openTestStore(t, clock)
	wantNewest := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		event := testEnvelope(fmt.Sprintf("list-%d", i))
		event.ReceivedAt = base.Add(time.Duration(i) * time.Minute)
		if i == 3 {
			event.Type = "pull_request.opened"
		}
		result, err := store.Insert(context.Background(), event)
		require.NoError(t, err)
		wantNewest = append([]string{result.Event.Envelope.ID}, wantNewest...)
	}

	var got []string
	var cursor *EventCursor
	for {
		page, err := store.List(context.Background(), EventFilter{Limit: 2, After: cursor})
		require.NoError(t, err)
		for _, event := range page.Events {
			got = append(got, event.Envelope.ID)
		}
		if page.Next == nil {
			break
		}
		cursor = page.Next
	}
	assert.Equal(t, wantNewest, got)

	filtered, err := store.ListEvents(context.Background(), EventFilter{
		Type:  "pull_request.opened",
		Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, filtered.Events, 1)
	assert.Equal(t, "pull_request.opened", filtered.Events[0].Envelope.Type)

	for i, eventID := range wantNewest[:3] {
		_, _, createErr := store.CreateDispatch(
			context.Background(), eventID, fmt.Sprintf("workflow-%d", i),
		)
		require.NoError(t, createErr)
		clock.Advance(time.Second)
	}
	firstPage, err := store.ListDispatches(context.Background(), DispatchFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, firstPage.Dispatches, 2)
	require.NotNil(t, firstPage.Next)
	secondPage, err := store.ListDispatches(context.Background(), DispatchFilter{
		Limit: 2,
		After: firstPage.Next,
	})
	require.NoError(t, err)
	require.Len(t, secondPage.Dispatches, 1)
}

func TestStoreDispatchMetadataExcludesOwnerAndPreservesPublicPage(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	clock := newMutableClock(base)
	store, _ := openTestStore(t, clock)
	event, err := store.Insert(
		context.Background(),
		testEnvelope("dispatch-metadata"),
	)
	require.NoError(t, err)

	created := make([]Dispatch, 0, 3)
	for index := 0; index < 3; index++ {
		dispatch, inserted, createErr := store.CreateDispatch(
			context.Background(),
			event.Event.Envelope.ID,
			fmt.Sprintf("workflows/metadata-%d.yaml", index),
		)
		require.NoError(t, createErr)
		require.True(t, inserted)
		created = append(created, dispatch)
		clock.Advance(time.Second)
	}
	claimed, err := store.ClaimDispatches(
		context.Background(),
		"operator-metadata-worker",
		len(created),
		time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, claimed, len(created))

	ownerSecrets := make([]string, len(created))
	for index, dispatch := range created {
		ownerSecrets[index] = fmt.Sprintf("owner-secret-%d", index)
		_, err = store.db.ExecContext(
			context.Background(),
			`UPDATE event_dispatches SET owner = ? WHERE id = ?`,
			ownerSecrets[index],
			dispatch.ID,
		)
		require.NoError(t, err)
	}
	full, err := store.GetDispatch(context.Background(), created[2].ID)
	require.NoError(t, err)
	assert.Equal(t, ownerSecrets[2], full.LeaseToken)

	first, err := store.ListDispatchMetadata(
		context.Background(),
		DispatchFilter{
			EventID: event.Event.Envelope.ID,
			Status:  DispatchClaimed,
			Limit:   2,
		},
	)
	require.NoError(t, err)
	require.Len(t, first.Dispatches, 2)
	require.NotNil(t, first.Next)
	assert.Equal(t, created[2].ID, first.Dispatches[0].ID)
	assert.Equal(t, created[1].ID, first.Dispatches[1].ID)
	assert.Equal(
		t,
		&DispatchCursor{
			CreatedAt: first.Dispatches[1].CreatedAt,
			ID:        first.Dispatches[1].ID,
		},
		first.Next,
	)

	second, err := store.ListDispatchMetadata(
		context.Background(),
		DispatchFilter{
			EventID: event.Event.Envelope.ID,
			Status:  DispatchClaimed,
			Limit:   2,
			After:   first.Next,
		},
	)
	require.NoError(t, err)
	require.Len(t, second.Dispatches, 1)
	assert.Nil(t, second.Next)
	assert.Equal(t, created[0].ID, second.Dispatches[0].ID)

	assert.Equal(t, DispatchMetadata{
		ID:          full.ID,
		EventID:     full.EventID,
		WorkflowRef: full.WorkflowRef,
		RunID:       full.RunID,
		Status:      full.Status,
		LeaseUntil:  full.LeaseUntil,
		AvailableAt: full.AvailableAt,
		Attempts:    full.Attempts,
		LastError:   full.LastError,
		CreatedAt:   full.CreatedAt,
		UpdatedAt:   full.UpdatedAt,
		LinkedAt:    full.LinkedAt,
		FinishedAt:  full.FinishedAt,
	}, first.Dispatches[0])

	filtered, err := store.ListDispatchMetadata(
		context.Background(),
		DispatchFilter{WorkflowRef: created[1].WorkflowRef},
	)
	require.NoError(t, err)
	require.Len(t, filtered.Dispatches, 1)
	assert.Equal(t, created[1].ID, filtered.Dispatches[0].ID)

	encoded, err := json.Marshal([]DispatchMetadataPage{first, second, filtered})
	require.NoError(t, err)
	forbiddenValues := append([]string(nil), ownerSecrets...)
	forbiddenValues = append(forbiddenValues, `"owner"`, `"lease_token"`)
	for _, forbidden := range forbiddenValues {
		assert.NotContains(t, string(encoded), forbidden)
	}

	_, err = store.ListDispatchMetadata(
		context.Background(),
		DispatchFilter{Status: "unknown"},
	)
	assert.EqualError(
		t,
		err,
		`invalid eventing state transition: unknown dispatch status "unknown"`,
	)
}

func TestStoreOperatorMetadataReadsDoNotMaterializeLargePayload(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	payload := json.RawMessage(
		`{"data":"` + strings.Repeat("x", 2<<20) + `"}`,
	)
	store, _ := openTestStore(
		t,
		clock,
		WithMaxPayloadBytes(len(payload)),
	)
	input := testEnvelope("metadata-only-large-payload")
	input.Payload = payload
	inserted, err := store.Insert(context.Background(), input)
	require.NoError(t, err)

	claimed, err := store.ClaimRouting(
		context.Background(),
		"metadata-test-worker",
		1,
		time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NotEmpty(t, claimed[0].Routing.LeaseToken)

	metadata, err := store.GetEventMetadata(
		context.Background(),
		inserted.Event.Envelope.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, len(inserted.Event.Envelope.Payload), metadata.PayloadBytes)
	assert.Nil(t, metadata.Envelope.Payload)
	assert.Empty(t, metadata.Envelope.DedupeKey)
	assert.Empty(t, metadata.Routing.LeaseToken)
	assert.Equal(t, RoutingClaimed, metadata.Routing.Status)

	page, err := store.ListEventMetadata(
		context.Background(),
		EventFilter{Source: "github", Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	assert.Equal(t, metadata.PayloadBytes, page.Events[0].PayloadBytes)
	assert.Nil(t, page.Events[0].Envelope.Payload)
	assert.Empty(t, page.Events[0].Envelope.DedupeKey)
	assert.Empty(t, page.Events[0].Routing.LeaseToken)

	exact, err := store.GetEventPayload(
		context.Background(),
		inserted.Event.Envelope.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, []byte(inserted.Event.Envelope.Payload), exact)
	exact[0] = '['
	again, err := store.GetEventPayload(
		context.Background(),
		inserted.Event.Envelope.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, []byte(inserted.Event.Envelope.Payload), again)
}

func TestStoreReplayAndPrunePreserveLineage(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	clock := newMutableClock(base)
	store, _ := openTestStore(t, clock)
	original, err := store.Insert(context.Background(), testEnvelope("original"))
	require.NoError(t, err)
	claim, err := store.ClaimRouting(context.Background(), "router", 1, time.Minute)
	require.NoError(t, err)
	require.NoError(t, store.AckRouting(
		context.Background(), original.Event.Envelope.ID, claim[0].Routing.LeaseToken,
	))

	clock.Advance(24 * time.Hour)
	replay, err := store.Replay(context.Background(), original.Event.Envelope.ID)
	require.NoError(t, err)
	assert.True(t, replay.Inserted)
	assert.Equal(t, original.Event.Envelope.ID, replay.Event.Envelope.ReplayOf)
	assert.Equal(t, "replay/"+replay.Event.Envelope.ID, replay.Event.Envelope.DedupeKey)
	assert.NotEqual(t, original.Event.Envelope.ID, replay.Event.Envelope.ID)

	cutoff := clock.Now().Add(time.Hour)
	count, err := store.Prune(context.Background(), cutoff, 100)
	require.NoError(t, err)
	assert.Zero(t, count, "pending replay and its source must both remain")

	claim, err = store.ClaimRouting(context.Background(), "router", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claim, 1)
	require.NoError(t, store.AckRouting(
		context.Background(), replay.Event.Envelope.ID, claim[0].Routing.LeaseToken,
	))
	count, err = store.Prune(context.Background(), cutoff, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "terminal replay is removed before its source")
	_, err = store.Get(context.Background(), original.Event.Envelope.ID)
	require.NoError(t, err)
	_, err = store.Get(context.Background(), replay.Event.Envelope.ID)
	assert.ErrorIs(t, err, ErrNotFound)

	count, err = store.Prune(context.Background(), cutoff, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	_, err = store.Get(context.Background(), original.Event.Envelope.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestStorePruneRetainsNonterminalDispatch(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	clock := newMutableClock(base)
	store, _ := openTestStore(t, clock)
	event, err := store.Insert(context.Background(), testEnvelope("retention-dispatch"))
	require.NoError(t, err)
	routing, err := store.ClaimRouting(context.Background(), "router", 1, time.Minute)
	require.NoError(t, err)
	require.NoError(t, store.AckRouting(
		context.Background(), event.Event.Envelope.ID, routing[0].Routing.LeaseToken,
	))
	dispatch, _, err := store.CreateDispatch(
		context.Background(), event.Event.Envelope.ID, "retention-workflow",
	)
	require.NoError(t, err)
	clock.Advance(24 * time.Hour)
	count, err := store.Prune(context.Background(), clock.Now(), 100)
	require.NoError(t, err)
	assert.Zero(t, count)

	claimed, err := store.ClaimDispatches(context.Background(), "dispatcher", 1, time.Minute)
	require.NoError(t, err)
	require.NoError(t, store.FinishDispatch(
		context.Background(), dispatch.ID, claimed[0].LeaseToken, DispatchSucceeded, "",
	))
	count, err = store.Prune(context.Background(), clock.Now(), 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
	_, err = store.GetDispatch(context.Background(), dispatch.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestStoreRejectsOversizedPayloadAndFutureSchema(t *testing.T) {
	t.Parallel()

	clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
	store, _ := openTestStore(t, clock, WithMaxPayloadBytes(8))
	event := testEnvelope("large")
	event.Payload = json.RawMessage(`{"value":"too large"}`)
	_, err := store.Insert(context.Background(), event)
	assert.ErrorIs(t, err, ErrPayloadTooLarge)

	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec("PRAGMA user_version = 99")
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = Open(context.Background(), path)
	assert.ErrorIs(t, err, ErrSchemaTooNew)
}

func TestStoreRejectsTimestampsThatWouldWrap(t *testing.T) {
	t.Parallel()

	clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
	store, _ := openTestStore(t, clock)
	inserted, err := store.Insert(context.Background(), testEnvelope("timestamp-range"))
	require.NoError(t, err)

	_, err = store.ClaimRouting(
		context.Background(),
		"router",
		1,
		time.Duration(1<<63-1),
	)
	assert.ErrorIs(t, err, ErrTimestampOutOfRange)

	claimed, err := store.ClaimRouting(context.Background(), "router", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	outOfRange := time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)
	err = store.NackRouting(
		context.Background(),
		inserted.Event.Envelope.ID,
		claimed[0].Routing.LeaseToken,
		outOfRange,
		"retry",
	)
	assert.ErrorIs(t, err, ErrTimestampOutOfRange)

	_, err = store.List(context.Background(), EventFilter{
		After: &EventCursor{
			ReceivedAt: outOfRange,
			ID:         inserted.Event.Envelope.ID,
		},
	})
	assert.ErrorIs(t, err, ErrTimestampOutOfRange)
	_, err = store.Prune(context.Background(), outOfRange, 1)
	assert.ErrorIs(t, err, ErrTimestampOutOfRange)

	clock.Set(outOfRange)
	_, err = store.Insert(context.Background(), testEnvelope("invalid-clock"))
	assert.ErrorIs(t, err, ErrTimestampOutOfRange)
}

func TestStoreZeroValueReturnsTypedErrors(t *testing.T) {
	t.Parallel()

	var store Store
	_, err := store.Get(context.Background(), "ev_00000000000000000000000000000000")
	assert.ErrorIs(t, err, ErrClosed)
	_, err = store.Insert(context.Background(), testEnvelope("zero-store"))
	assert.ErrorIs(t, err, ErrClosed)
	assert.ErrorIs(t, store.Close(), ErrClosed)

	var nilStore *Store
	assert.NoError(t, nilStore.Close())
}

func TestStoreCancellationCloseAndNotFound(t *testing.T) {
	t.Parallel()

	clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
	store, _ := openTestStore(t, clock)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.Insert(ctx, testEnvelope("canceled"))
	assert.ErrorIs(t, err, context.Canceled)
	_, err = store.Get(context.Background(), "ev_00000000000000000000000000000000")
	assert.ErrorIs(t, err, ErrNotFound)
	assert.ErrorIs(t, store.AckRouting(
		context.Background(),
		"ev_00000000000000000000000000000000",
		"token",
	), ErrNotFound)

	require.NoError(t, store.Close())
	require.NoError(t, store.Close())
	_, err = store.Get(context.Background(), "ev_00000000000000000000000000000000")
	assert.ErrorIs(t, err, ErrClosed)
	_, err = store.Insert(context.Background(), testEnvelope("closed"))
	assert.ErrorIs(t, err, ErrClosed)
	_, err = store.Prune(context.Background(), clock.Now(), 1)
	assert.ErrorIs(t, err, ErrClosed)
}

func TestStoreConcurrentCloseIsRaceSafe(t *testing.T) {
	clock := newMutableClock(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC))
	store, _ := openTestStore(t, clock)
	inserted, err := store.Insert(context.Background(), testEnvelope("close-race"))
	require.NoError(t, err)

	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for j := 0; j < 20; j++ {
				_, getErr := store.Get(context.Background(), inserted.Event.Envelope.ID)
				if getErr != nil && !errors.Is(getErr, ErrClosed) {
					t.Errorf("unexpected concurrent get error: %v", getErr)
					return
				}
			}
		}()
	}
	require.NoError(t, store.Close())
	wait.Wait()
}
