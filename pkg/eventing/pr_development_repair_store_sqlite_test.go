//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Store) claimPRDevelopmentRepairIncludingProviderForTest(
	ctx context.Context,
	input PRDevelopmentRepairClaimRequest,
) (PRDevelopmentRepairSession, bool, error) {
	return s.claimPRDevelopmentRepair(ctx, input, true)
}

func TestStorePRDevelopmentRepairAdmissionWorkbenchAndIdempotency(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	conversation, err := store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          developmentCase.ID,
			ExpectedVersion: 0,
			Role:            PRDevelopmentMessageUser,
			Content:         "Explain the review before editing.",
		},
	)
	require.NoError(t, err)

	admission := PRDevelopmentRepairAdmit{
		CaseID:                      developmentCase.ID,
		ExpectedConversationVersion: conversation.Version,
		ExpectedRepairVersion:       0,
		IdempotencyKey:              " repair-request-1 ",
		AgentID:                     " main ",
		Instruction:                 "  Apply the requested bounded retry fix.  ",
	}
	oversized := admission
	oversized.Instruction = strings.Repeat("a", MaxPRDevelopmentRepairInstructionBytes+1)
	_, admitted, err := store.AdmitPRDevelopmentRepair(ctx, oversized)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentRepair)
	assert.False(t, admitted)
	workbench, admitted, err := store.AdmitPRDevelopmentRepair(ctx, admission)
	require.NoError(t, err)
	require.True(t, admitted)
	assert.Equal(t, developmentCase, workbench.Case)
	assert.Equal(t, conversation, workbench.Conversation)
	require.NotNil(t, workbench.RepairSession)
	session := workbench.RepairSession
	assert.True(t, validPrefixedHexID(session.ID, prDevelopmentRepairSessionIDPrefix))
	assert.Equal(t, developmentCase.ID, session.CaseID)
	assert.Equal(t, int64(1), session.Version)
	assert.Equal(t, "main", session.AgentID)
	assert.True(t, validPrefixedHexID(
		session.ReservationKey,
		prDevelopmentRepairReservationPrefix,
	))
	assert.Empty(t, session.HeadRepository)
	assert.Empty(t, session.WorkspaceID)
	require.Len(t, session.Attempts, 1)
	attempt := session.Attempts[0]
	assert.Equal(t, 0, attempt.Ordinal)
	assert.Equal(t, int64(0), attempt.ExpectedRepairVersion)
	assert.Equal(t, conversation.Version, attempt.ConversationVersion)
	assert.Equal(t, "repair-request-1", attempt.IdempotencyKey)
	assert.Equal(t, "Apply the requested bounded retry fix.", attempt.Instruction)
	assert.Equal(t, PRDevelopmentRepairQueued, attempt.Status)
	assert.Zero(t, attempt.Claims)

	claimed, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "worker-a", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(2), claimed.Version)

	replayed, admitted, err := store.AdmitPRDevelopmentRepair(ctx, admission)
	require.NoError(t, err)
	assert.False(t, admitted)
	require.NotNil(t, replayed.RepairSession)
	assert.Equal(t, claimed, *replayed.RepairSession)

	changed := admission
	changed.Instruction = "different instruction"
	_, admitted, err = store.AdmitPRDevelopmentRepair(ctx, changed)
	assert.ErrorIs(t, err, ErrPRDevelopmentRepairConflict)
	assert.False(t, admitted)

	active := admission
	active.IdempotencyKey = "repair-request-2"
	active.ExpectedRepairVersion = claimed.Version
	_, admitted, err = store.AdmitPRDevelopmentRepair(ctx, active)
	assert.ErrorIs(t, err, ErrPRDevelopmentRepairActive)
	assert.False(t, admitted)

	staleConversation := active
	staleConversation.IdempotencyKey = "repair-request-3"
	staleConversation.ExpectedConversationVersion = 0
	_, admitted, err = store.AdmitPRDevelopmentRepair(ctx, staleConversation)
	assert.ErrorIs(t, err, ErrPRDevelopmentConversationConflict)
	assert.False(t, admitted)

	loaded, err := store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, replayed, loaded)
	assert.Equal(t, conversation, loaded.Conversation, "repair admission must not append chat")
}

func TestStorePRDevelopmentRepairConcurrentAdmissionFencesBothStores(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "repair-concurrent.db")
	first, clock, capture := newPRDevelopmentStoreFixture(t, path)
	developmentCase, created, err := first.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	second, err := Open(ctx, path, WithClock(func() time.Time { return *clock }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close()) })

	type result struct {
		workbench PRDevelopmentWorkbench
		admitted  bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for index, store := range []*Store{first, second} {
		wait.Add(1)
		go func(index int, store *Store) {
			defer wait.Done()
			<-start
			workbench, admitted, callErr := store.AdmitPRDevelopmentRepair(
				ctx,
				PRDevelopmentRepairAdmit{
					CaseID:                      developmentCase.ID,
					ExpectedConversationVersion: 0,
					ExpectedRepairVersion:       0,
					IdempotencyKey:              fmt.Sprintf("concurrent-%d", index),
					AgentID:                     "main",
					Instruction:                 "fix the reviewed change",
				},
			)
			results <- result{workbench: workbench, admitted: admitted, err: callErr}
		}(index, store)
	}
	close(start)
	wait.Wait()
	close(results)

	successes, conflicts := 0, 0
	for item := range results {
		switch {
		case item.err == nil:
			successes++
			assert.True(t, item.admitted)
			require.NotNil(t, item.workbench.RepairSession)
		case errors.Is(item.err, ErrPRDevelopmentRepairConflict):
			conflicts++
			assert.False(t, item.admitted)
		default:
			t.Fatalf("AdmitPRDevelopmentRepair() error = %v", item.err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)
	loaded, err := second.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.RepairSession)
	require.Len(t, loaded.RepairSession.Attempts, 1)
}

func TestStorePRDevelopmentRepairProviderThreadHasOneSessionOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	first, created, err := store.CapturePRDevelopmentCase(
		ctx, validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	second := captureAdditionalPRDevelopmentThreadCase(
		t, store, clock, capture, "delivery-session-owner-second", "801",
	)
	input := PRDevelopmentRepairAdmit{
		CaseID: first.ID, ExpectedConversationVersion: 0, ExpectedRepairVersion: 0,
		IdempotencyKey: "thread-owner", AgentID: "main",
		Instruction: "Own the retained provider-thread line.",
	}
	owned, admitted, err := store.AdmitPRDevelopmentRepair(ctx, input)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, owned.RepairSession)
	replayed, admitted, err := store.AdmitPRDevelopmentRepair(ctx, input)
	require.NoError(t, err)
	assert.False(t, admitted)
	assert.Equal(t, owned.RepairSession.ID, replayed.RepairSession.ID)
	foreign := input
	foreign.CaseID = second.ID
	foreign.IdempotencyKey = "foreign-thread-owner"
	_, admitted, err = store.AdmitPRDevelopmentRepair(ctx, foreign)
	assert.ErrorIs(t, err, ErrPRDevelopmentRepairConflict)
	assert.False(t, admitted)
	run, claimed, err := store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{WorkerLabel: "owner-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, owned.RepairSession.Attempts[0].ID, run.AttemptID)
}

func TestStorePRDevelopmentRepairConcurrentSiblingAdmissionHasOneWinner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	first, created, err := store.CapturePRDevelopmentCase(
		ctx, validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	second := captureAdditionalPRDevelopmentThreadCase(
		t, store, clock, capture, "delivery-concurrent-owner-second", "802",
	)
	type result struct {
		admitted bool
		err      error
	}
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	for index, caseID := range []string{first.ID, second.ID} {
		index, caseID := index, caseID
		go func() {
			ready.Done()
			<-start
			_, admitted, admitErr := store.AdmitPRDevelopmentRepair(
				ctx,
				PRDevelopmentRepairAdmit{
					CaseID: caseID, ExpectedConversationVersion: 0,
					ExpectedRepairVersion: 0,
					IdempotencyKey:        fmt.Sprintf("concurrent-owner-%d", index),
					AgentID:               "main", Instruction: "Race for the one thread owner.",
				},
			)
			results <- result{admitted: admitted, err: admitErr}
		}()
	}
	ready.Wait()
	close(start)
	firstResult, secondResult := <-results, <-results
	close(results)
	wins := 0
	conflicts := 0
	for _, candidate := range []result{firstResult, secondResult} {
		if candidate.admitted {
			require.NoError(t, candidate.err)
			wins++
		} else {
			assert.ErrorIs(t, candidate.err, ErrPRDevelopmentRepairConflict)
			conflicts++
		}
	}
	assert.Equal(t, 1, wins)
	assert.Equal(t, 1, conflicts)
	var sessions int
	require.NoError(t, store.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_repair_sessions`).Scan(&sessions))
	assert.Equal(t, 1, sessions)
}

func TestStorePRDevelopmentRepairClaimSamplesLeaseAfterWriterContention(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "repair-claim-contention.db")
	store, clock, capture := newPRDevelopmentStoreFixture(t, path)
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	admitted := admitPRDevelopmentRepairForTest(t, store, developmentCase.ID, "contention", 0)
	require.NotNil(t, admitted.RepairSession)
	blocker, err := Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, blocker.Close()) })

	locked := make(chan struct{})
	release := make(chan struct{})
	blockerDone := make(chan error, 1)
	go func() {
		blockerDone <- blocker.withImmediate(ctx, func(*sql.Conn) error {
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("writer transaction did not acquire its lock")
	}

	var clockMu sync.Mutex
	contendedNow := *clock
	clockCalled := make(chan struct{}, 1)
	store.now = func() time.Time {
		select {
		case clockCalled <- struct{}{}:
		default:
		}
		clockMu.Lock()
		defer clockMu.Unlock()
		return contendedNow
	}
	type claimResult struct {
		session PRDevelopmentRepairSession
		found   bool
		err     error
	}
	claimDone := make(chan claimResult, 1)
	go func() {
		session, found, claimErr := store.claimPRDevelopmentRepairIncludingProviderForTest(
			ctx,
			PRDevelopmentRepairClaimRequest{
				WorkerLabel: "contended-worker",
				Lease:       time.Minute,
			},
		)
		claimDone <- claimResult{session: session, found: found, err: claimErr}
	}()

	// The store clock must not be sampled while BEGIN IMMEDIATE is still waiting
	// for the competing writer. Advance it before releasing that writer.
	sampledBeforeRelease := false
	select {
	case <-clockCalled:
		sampledBeforeRelease = true
	case <-time.After(250 * time.Millisecond):
	}
	clockMu.Lock()
	contendedNow = contendedNow.Add(2 * time.Minute)
	expectedLeaseUntil := contendedNow.Add(time.Minute)
	clockMu.Unlock()
	close(release)
	require.NoError(t, <-blockerDone)
	result := <-claimDone
	require.NoError(t, result.err)
	require.True(t, result.found)
	assert.False(t, sampledBeforeRelease)
	attempt := activePRDevelopmentRepairAttempt(&result.session)
	require.NotNil(t, attempt)
	require.NotNil(t, attempt.LeaseUntil)
	assert.Equal(t, expectedLeaseUntil, *attempt.LeaseUntil)
}

func TestStorePRDevelopmentRepairClaimRefreshesLeaseAtOwnershipWrite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	admitted := admitPRDevelopmentRepairForTest(t, store, developmentCase.ID, "fresh-claim", 0)
	require.NotNil(t, admitted.RepairSession)

	scanNow := *clock
	claimNow := scanNow.Add(2 * time.Minute)
	clockCalls := 0
	store.now = func() time.Time {
		clockCalls++
		if clockCalls == 1 {
			return scanNow
		}
		return claimNow
	}
	claimed, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{
			WorkerLabel: "fresh-claim-worker",
			Lease:       time.Second,
		},
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 2, clockCalls)
	assert.Equal(t, claimNow, claimed.UpdatedAt)
	attempt := activePRDevelopmentRepairAttempt(&claimed)
	require.NotNil(t, attempt)
	require.NotNil(t, attempt.LeaseUntil)
	assert.Equal(t, claimNow, attempt.UpdatedAt)
	assert.Equal(t, claimNow.Add(time.Second), *attempt.LeaseUntil)
}

func TestStorePRDevelopmentRepairClaimSkipsInvalidOldestAggregate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	oldestCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	oldest := admitPRDevelopmentRepairForTest(t, store, oldestCase.ID, "invalid-oldest", 0)
	require.NotNil(t, oldest.RepairSession)

	*clock = (*clock).Add(time.Second)
	laterCase := capturePRDevelopmentListCase(
		t,
		store,
		capture,
		"delivery-after-invalid-repair",
		"acme/later-project",
		43,
	)
	later := admitPRDevelopmentRepairForTest(t, store, laterCase.ID, "valid-later", 0)
	require.NotNil(t, later.RepairSession)

	// Uppercase is accepted by the schema's byte-length constraint but rejected
	// by the aggregate's canonical agent validation.
	_, err = store.db.Exec(`
		UPDATE pr_development_repair_sessions SET agent_id = ? WHERE id = ?`,
		"INVALID",
		oldest.RepairSession.ID,
	)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentWorkbench(ctx, oldestCase.ID)
	assert.ErrorIs(t, err, errInvalidStoredPRDevelopmentRepair)

	claimed, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "valid-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, later.RepairSession.ID, claimed.ID)

	var (
		oldestVersion         int64
		oldestUpdatedAt       int64
		oldestStatus          PRDevelopmentRepairStatus
		oldestClaims          int
		oldestClaimSuppressed int
	)
	err = store.db.QueryRow(`
		SELECT session.version, session.updated_at, session.claim_suppressed,
		       attempt.status, attempt.claims
		FROM pr_development_repair_sessions AS session
		JOIN pr_development_repair_attempts AS attempt
		  ON attempt.session_id = session.id
		WHERE session.id = ?`,
		oldest.RepairSession.ID,
	).Scan(
		&oldestVersion,
		&oldestUpdatedAt,
		&oldestClaimSuppressed,
		&oldestStatus,
		&oldestClaims,
	)
	require.NoError(t, err)
	assert.Equal(t, int64(1), oldestVersion)
	assert.Equal(t, toDBTime(oldest.RepairSession.UpdatedAt), oldestUpdatedAt)
	assert.Equal(t, 1, oldestClaimSuppressed)
	assert.Equal(t, PRDevelopmentRepairQueued, oldestStatus)
	assert.Zero(t, oldestClaims)
}

func TestStorePRDevelopmentRepairClaimDoesNotSuppressOperationalLoadErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	workbench := admitPRDevelopmentRepairForTest(t, store, developmentCase.ID, "scan-error", 0)
	require.NotNil(t, workbench.RepairSession)

	// SQLite's non-STRICT table accepts this type corruption while its ordering
	// CHECK remains true. Scanning it as an integer is an operational load error,
	// not a deterministic aggregate-validation failure eligible for suppression.
	_, err = store.db.Exec(`
		UPDATE pr_development_repair_sessions
		SET created_at = 'bad', updated_at = 'bad'
		WHERE id = ?`,
		workbench.RepairSession.ID,
	)
	require.NoError(t, err)
	_, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "worker", Lease: time.Minute},
	)
	assert.Error(t, err)
	assert.False(t, found)
	var suppressed int
	err = store.db.QueryRow(`
		SELECT claim_suppressed FROM pr_development_repair_sessions WHERE id = ?`,
		workbench.RepairSession.ID,
	).Scan(&suppressed)
	require.NoError(t, err)
	assert.Zero(t, suppressed)
}

func TestStorePRDevelopmentRepairClaimSuppressionMakesBoundedProgress(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	for index := 0; index < maxPRDevelopmentRepairClaimCandidates; index++ {
		var developmentCase PRDevelopmentCase
		if index == 0 {
			var created bool
			var captureErr error
			developmentCase, created, captureErr = store.CapturePRDevelopmentCase(
				ctx,
				validPRDevelopmentRequestForTest(capture),
			)
			require.NoError(t, captureErr)
			require.True(t, created)
		} else {
			developmentCase = capturePRDevelopmentListCase(
				t,
				store,
				capture,
				fmt.Sprintf("delivery-invalid-repair-%d", index),
				fmt.Sprintf("acme/invalid-project-%d", index),
				int64(100+index),
			)
		}
		workbench := admitPRDevelopmentRepairForTest(
			t,
			store,
			developmentCase.ID,
			fmt.Sprintf("invalid-repair-%d", index),
			0,
		)
		require.NotNil(t, workbench.RepairSession)
		_, suppressErr := store.db.Exec(`
			UPDATE pr_development_repair_sessions SET agent_id = ? WHERE id = ?`,
			"INVALID",
			workbench.RepairSession.ID,
		)
		require.NoError(t, suppressErr)
		*clock = (*clock).Add(time.Second)
	}
	validCase := capturePRDevelopmentListCase(
		t,
		store,
		capture,
		"delivery-after-suppression-batch",
		"acme/valid-project",
		1000,
	)
	valid := admitPRDevelopmentRepairForTest(t, store, validCase.ID, "valid-after-batch", 0)
	require.NotNil(t, valid.RepairSession)

	none, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "suppressor", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, none.ID)
	var suppressed int
	err = store.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_repair_sessions
		WHERE claim_suppressed = 1`,
	).Scan(&suppressed)
	require.NoError(t, err)
	assert.Equal(t, maxPRDevelopmentRepairClaimCandidates, suppressed)

	claimed, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "next-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, valid.RepairSession.ID, claimed.ID)
}

func TestStorePRDevelopmentRepairPreparingReclaimAndRunningExpiry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	workbench := admitPRDevelopmentRepairForTest(t, store, developmentCase.ID, "expiry", 0)

	first, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "worker-a", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(2), first.Version)
	firstAttempt := activePRDevelopmentRepairAttempt(&first)
	require.NotNil(t, firstAttempt)
	firstToken := firstAttempt.LeaseToken
	firstUpdatedAt := firstAttempt.UpdatedAt
	firstSessionUpdatedAt := first.UpdatedAt

	*clock = (*clock).Add(30 * time.Second)
	require.NoError(t, store.RenewPRDevelopmentRepairLease(
		ctx,
		firstAttempt.ID,
		firstToken,
		time.Minute,
	))
	afterRenew, err := store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, afterRenew.RepairSession)
	assert.Equal(t, first.Version, afterRenew.RepairSession.Version)
	assert.Equal(t, firstUpdatedAt, afterRenew.RepairSession.Attempts[0].UpdatedAt)

	*clock = (*clock).Add(2 * time.Minute)
	reclaimed, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "worker-b", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, first.Version, reclaimed.Version)
	assert.Equal(t, firstSessionUpdatedAt, reclaimed.UpdatedAt)
	reclaimedAttempt := activePRDevelopmentRepairAttempt(&reclaimed)
	require.NotNil(t, reclaimedAttempt)
	assert.Equal(t, firstAttempt.ID, reclaimedAttempt.ID)
	assert.Equal(t, PRDevelopmentRepairPreparing, reclaimedAttempt.Status)
	assert.Equal(t, 2, reclaimedAttempt.Claims)
	assert.NotEqual(t, firstToken, reclaimedAttempt.LeaseToken)
	assert.Equal(t, firstUpdatedAt, reclaimedAttempt.UpdatedAt)

	pin := validPRDevelopmentRepairPinForTest(reclaimedAttempt)
	pinned, err := store.PinPRDevelopmentRepairSession(ctx, pin)
	require.NoError(t, err)
	assert.Equal(t, int64(3), pinned.Version)
	exact, err := store.PinPRDevelopmentRepairSession(ctx, pin)
	require.NoError(t, err)
	assert.Equal(t, pinned, exact, "exact repin must not advance the version")
	changedPin := pin
	changedPin.HeadSHA = strings.Repeat("b", 40)
	_, err = store.PinPRDevelopmentRepairSession(ctx, changedPin)
	assert.ErrorIs(t, err, ErrPRDevelopmentRepairConflict)

	running, err := store.BeginPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairBegin{
			AttemptID:  reclaimedAttempt.ID,
			LeaseToken: reclaimedAttempt.LeaseToken,
			Lease:      time.Minute,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(4), running.Version)
	assert.Equal(t, PRDevelopmentRepairRunning, running.Attempts[0].Status)

	*clock = (*clock).Add(2 * time.Minute)
	none, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "worker-c", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, none.ID)
	recovered, err := store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, recovered.RepairSession)
	assert.Equal(t, int64(5), recovered.RepairSession.Version)
	require.Len(t, recovered.RepairSession.Attempts, 1)
	recovery := recovered.RepairSession.Attempts[0]
	assert.Equal(t, PRDevelopmentRepairRecoveryRequired, recovery.Status)
	assert.Equal(t, PRDevelopmentRepairErrorRecoveryRequired, recovery.ErrorCode)
	assert.NotEmpty(t, recovery.Summary)
	assert.Nil(t, recovery.LeaseUntil)
	assert.Empty(t, recovery.LeaseToken)

	_, found, err = store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "worker-d", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, found, "expired running work must never be reclaimed")
	unchanged, err := store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	assert.Equal(t, recovered, unchanged)
	assert.Equal(t, workbench.Conversation, unchanged.Conversation)
}

func TestStorePRDevelopmentRepairRunningExpiryMakesBoundedProgress(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	total := maxPRDevelopmentRepairClaimCandidates + 1
	for index := 0; index < total; index++ {
		var developmentCase PRDevelopmentCase
		if index == 0 {
			var created bool
			var captureErr error
			developmentCase, created, captureErr = store.CapturePRDevelopmentCase(
				ctx,
				validPRDevelopmentRequestForTest(capture),
			)
			require.NoError(t, captureErr)
			require.True(t, created)
		} else {
			developmentCase = capturePRDevelopmentListCase(
				t,
				store,
				capture,
				fmt.Sprintf("delivery-expired-running-%d", index),
				fmt.Sprintf("acme/running-project-%d", index),
				int64(200+index),
			)
		}
		workbench := admitPRDevelopmentRepairForTest(
			t,
			store,
			developmentCase.ID,
			fmt.Sprintf("expired-running-%d", index),
			0,
		)
		require.NotNil(t, workbench.RepairSession)
		claimed, found, claimErr := store.claimPRDevelopmentRepairIncludingProviderForTest(
			ctx,
			PRDevelopmentRepairClaimRequest{WorkerLabel: "worker", Lease: time.Minute},
		)
		require.NoError(t, claimErr)
		require.True(t, found)
		attempt := activePRDevelopmentRepairAttempt(&claimed)
		require.NotNil(t, attempt)
		_, pinErr := store.PinPRDevelopmentRepairSession(
			ctx,
			validPRDevelopmentRepairPinForTest(attempt),
		)
		require.NoError(t, pinErr)
		_, beginErr := store.BeginPRDevelopmentRepair(
			ctx,
			PRDevelopmentRepairBegin{
				AttemptID: attempt.ID, LeaseToken: attempt.LeaseToken, Lease: time.Minute,
			},
		)
		require.NoError(t, beginErr)
	}

	*clock = (*clock).Add(2 * time.Minute)
	_, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "reaper", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, found)
	var recovered, running int
	err = store.db.QueryRow(`
		SELECT
			SUM(CASE WHEN status = ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN status = ? THEN 1 ELSE 0 END)
		FROM pr_development_repair_attempts`,
		PRDevelopmentRepairRecoveryRequired,
		PRDevelopmentRepairRunning,
	).Scan(&recovered, &running)
	require.NoError(t, err)
	assert.Equal(t, maxPRDevelopmentRepairClaimCandidates, recovered)
	assert.Equal(t, 1, running)

	_, found, err = store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "reaper", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, found)
	err = store.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_repair_attempts WHERE status = ?`,
		PRDevelopmentRepairRecoveryRequired,
	).Scan(&recovered)
	require.NoError(t, err)
	assert.Equal(t, total, recovered)
}

func TestStorePRDevelopmentRepairBeginAndFinishFences(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	admitted := admitPRDevelopmentRepairForTest(t, store, developmentCase.ID, "finish", 0)
	require.NotNil(t, admitted.RepairSession)
	claimed, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	attempt := activePRDevelopmentRepairAttempt(&claimed)
	require.NotNil(t, attempt)
	pinned, err := store.PinPRDevelopmentRepairSession(
		ctx,
		validPRDevelopmentRepairPinForTest(attempt),
	)
	require.NoError(t, err)

	_, err = store.FinishPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairOutcome{
			AttemptID:   attempt.ID,
			LeaseToken:  attempt.LeaseToken,
			Status:      PRDevelopmentRepairCompleted,
			Summary:     "fixed",
			Iterations:  1,
			WorkspaceID: "gw-invalid-transition",
		},
	)
	assert.ErrorIs(t, err, ErrInvalidTransition)
	_, err = store.FinishPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairOutcome{
			AttemptID:  attempt.ID,
			LeaseToken: attempt.LeaseToken,
			Status:     PRDevelopmentRepairRecoveryRequired,
			Summary:    "must start before recovery is ambiguous",
			ErrorCode:  PRDevelopmentRepairErrorRecoveryRequired,
		},
	)
	assert.ErrorIs(t, err, ErrInvalidTransition)
	stillPreparing, err := store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, stillPreparing.RepairSession)
	assert.Equal(t, pinned, *stillPreparing.RepairSession)

	running, err := store.BeginPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairBegin{
			AttemptID: attempt.ID, LeaseToken: attempt.LeaseToken, Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	_, err = store.FinishPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairOutcome{
			AttemptID:   attempt.ID,
			LeaseToken:  attempt.LeaseToken,
			Status:      PRDevelopmentRepairCompleted,
			Summary:     strings.Repeat("a", MaxPRDevelopmentRepairSummaryBytes+1),
			Iterations:  1,
			WorkspaceID: "gw-oversized-summary",
		},
	)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentRepair)
	_, err = store.FinishPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairOutcome{
			AttemptID:  attempt.ID,
			LeaseToken: attempt.LeaseToken,
			Status:     PRDevelopmentRepairCompleted,
			Summary:    "workspace fence is required",
			Iterations: 1,
		},
	)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentRepair)
	completed, err := store.FinishPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairOutcome{
			AttemptID:   attempt.ID,
			LeaseToken:  attempt.LeaseToken,
			Status:      PRDevelopmentRepairCompleted,
			Summary:     "  Updated retry handling and its focused tests.  ",
			Iterations:  3,
			WorkspaceID: "gw-0123456789ab",
		},
	)
	require.NoError(t, err)
	assert.Equal(t, running.Version+1, completed.Version)
	assert.Equal(t, "gw-0123456789ab", completed.WorkspaceID)
	require.Len(t, completed.Attempts, 1)
	assert.Equal(t, PRDevelopmentRepairCompleted, completed.Attempts[0].Status)
	assert.Equal(t, "Updated retry handling and its focused tests.", completed.Attempts[0].Summary)
	assert.Equal(t, 3, completed.Attempts[0].Iterations)
	assert.Empty(t, completed.Attempts[0].ErrorCode)
	assert.Empty(t, completed.Attempts[0].LeaseToken)

	_, err = store.BeginPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairBegin{
			AttemptID: attempt.ID, LeaseToken: attempt.LeaseToken, Lease: time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrStaleLease)
	assert.Error(t, store.RenewPRDevelopmentRepairLease(
		ctx,
		attempt.ID,
		attempt.LeaseToken,
		time.Minute,
	))

	secondWorkbench := admitPRDevelopmentRepairForTest(
		t,
		store,
		developmentCase.ID,
		"finish-second",
		completed.Version,
	)
	require.NotNil(t, secondWorkbench.RepairSession)
	secondClaim, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	second := activePRDevelopmentRepairAttempt(&secondClaim)
	require.NotNil(t, second)
	repinned, err := store.PinPRDevelopmentRepairSession(
		ctx,
		validPRDevelopmentRepairPinForTest(second),
	)
	require.NoError(t, err)
	assert.Equal(t, secondClaim.Version, repinned.Version, "stable session pin is reused")
	_, err = store.BeginPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairBegin{
			AttemptID: second.ID, LeaseToken: second.LeaseToken, Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	_, err = store.FinishPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairOutcome{
			AttemptID:     second.ID,
			LeaseToken:    second.LeaseToken,
			Status:        PRDevelopmentRepairFailed,
			Summary:       "The model stopped before a safe final answer.",
			ErrorCode:     PRDevelopmentRepairErrorRepairFailed,
			InternalError: "provider secret-token failed",
			Iterations:    0,
			WorkspaceID:   "gw-0123456789ab",
		},
	)
	assert.ErrorIs(t, err, ErrInvalidTransition)
	recovery, err := store.FinishPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairOutcome{
			AttemptID:     second.ID,
			LeaseToken:    second.LeaseToken,
			Status:        PRDevelopmentRepairRecoveryRequired,
			Summary:       "The model stopped after local execution began.",
			ErrorCode:     PRDevelopmentRepairErrorRecoveryRequired,
			InternalError: "provider secret-token failed",
			Iterations:    0,
			WorkspaceID:   "gw-0123456789ab",
		},
	)
	require.NoError(t, err)
	require.Len(t, recovery.Attempts, 2)
	assert.Equal(t, PRDevelopmentRepairRecoveryRequired, recovery.Attempts[1].Status)
	assert.Equal(
		t,
		PRDevelopmentRepairErrorRecoveryRequired,
		recovery.Attempts[1].ErrorCode,
	)
	assert.NotEmpty(t, recovery.Attempts[1].InternalError)
	assert.Equal(t, recovery.Attempts[0].CreatedAt, recovery.Attempts[1].CreatedAt)

	_, err = store.db.Exec(`
		UPDATE pr_development_repair_sessions SET workspace_id = '' WHERE id = ?`,
		recovery.ID,
	)
	require.NoError(t, err)
	_, err = store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	assert.Error(t, err, "stored completed history must retain its workspace fence")
}

func TestStorePRDevelopmentRepairBeginRefreshesExecutionLease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	admitted := admitPRDevelopmentRepairForTest(t, store, developmentCase.ID, "refresh", 0)
	require.NotNil(t, admitted.RepairSession)
	claimed, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	attempt := activePRDevelopmentRepairAttempt(&claimed)
	require.NotNil(t, attempt)
	require.NotNil(t, attempt.LeaseUntil)
	originalLeaseUntil := *attempt.LeaseUntil
	_, err = store.PinPRDevelopmentRepairSession(ctx, validPRDevelopmentRepairPinForTest(attempt))
	require.NoError(t, err)

	*clock = (*clock).Add(50 * time.Second)
	_, err = store.BeginPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairBegin{AttemptID: attempt.ID, LeaseToken: attempt.LeaseToken},
	)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentRepair)
	_, err = store.BeginPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairBegin{
			AttemptID:  attempt.ID,
			LeaseToken: attempt.LeaseToken,
			Lease:      time.Duration(1<<63 - 1),
		},
	)
	assert.ErrorIs(t, err, ErrTimestampOutOfRange)
	executionLease := 2 * time.Minute
	running, err := store.BeginPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairBegin{
			AttemptID: attempt.ID, LeaseToken: attempt.LeaseToken, Lease: executionLease,
		},
	)
	require.NoError(t, err)
	runningAttempt := activePRDevelopmentRepairAttempt(&running)
	require.NotNil(t, runningAttempt)
	require.NotNil(t, runningAttempt.LeaseUntil)
	assert.Equal(t, (*clock).Add(executionLease), *runningAttempt.LeaseUntil)
	assert.True(t, runningAttempt.LeaseUntil.After(originalLeaseUntil))

	// Passing the original claim deadline must not let another claimant expire
	// execution now protected by the refreshed running lease.
	*clock = originalLeaseUntil.Add(time.Nanosecond)
	none, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "other-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, none.ID)
	unchanged, err := store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, unchanged.RepairSession)
	assert.Equal(t, running, *unchanged.RepairSession)
}

func TestStorePRDevelopmentRepairRenewCannotShortenConcurrentBeginLease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "repair-renew-begin.db")
	store, clock, capture := newPRDevelopmentStoreFixture(t, path)
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	admitted := admitPRDevelopmentRepairForTest(t, store, developmentCase.ID, "renew-begin", 0)
	require.NotNil(t, admitted.RepairSession)
	claimed, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "worker", Lease: 10 * time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	attempt := activePRDevelopmentRepairAttempt(&claimed)
	require.NotNil(t, attempt)
	_, err = store.PinPRDevelopmentRepairSession(ctx, validPRDevelopmentRepairPinForTest(attempt))
	require.NoError(t, err)

	var clockMu sync.Mutex
	contendedNow := *clock
	readClock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return contendedNow
	}
	store.now = readClock
	beginStore, err := Open(ctx, path, WithClock(readClock))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, beginStore.Close()) })

	// Occupy this store's only connection so renewal is in flight but cannot
	// reach its write transaction while the other store commits Begin.
	store.db.SetMaxOpenConns(1)
	held, err := store.db.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = held.Close() }()
	clockCalled := make(chan struct{}, 1)
	store.now = func() time.Time {
		select {
		case clockCalled <- struct{}{}:
		default:
		}
		return readClock()
	}
	renewDone := make(chan error, 1)
	go func() {
		renewDone <- store.RenewPRDevelopmentRepairLease(
			ctx,
			attempt.ID,
			attempt.LeaseToken,
			time.Minute,
		)
	}()

	sampledBeforeBegin := false
	select {
	case <-clockCalled:
		sampledBeforeBegin = true
	case <-time.After(100 * time.Millisecond):
	}
	clockMu.Lock()
	contendedNow = contendedNow.Add(2 * time.Minute)
	beginNow := contendedNow
	clockMu.Unlock()
	executionLease := 5 * time.Minute
	running, err := beginStore.BeginPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairBegin{
			AttemptID: attempt.ID, LeaseToken: attempt.LeaseToken, Lease: executionLease,
		},
	)
	require.NoError(t, err)
	runningAttempt := activePRDevelopmentRepairAttempt(&running)
	require.NotNil(t, runningAttempt)
	require.NotNil(t, runningAttempt.LeaseUntil)
	expectedLeaseUntil := beginNow.Add(executionLease)
	assert.Equal(t, expectedLeaseUntil, *runningAttempt.LeaseUntil)

	require.NoError(t, held.Close())
	require.NoError(t, <-renewDone)
	assert.False(t, sampledBeforeBegin)
	afterRenew, err := beginStore.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, afterRenew.RepairSession)
	afterRenewAttempt := activePRDevelopmentRepairAttempt(afterRenew.RepairSession)
	require.NotNil(t, afterRenewAttempt)
	require.NotNil(t, afterRenewAttempt.LeaseUntil)
	assert.Equal(t, expectedLeaseUntil, *afterRenewAttempt.LeaseUntil)
}

func TestStorePRDevelopmentRepairAttemptAndVersionBoundsAreAtomic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)

	var version int64
	for ordinal := 0; ordinal < MaxPRDevelopmentRepairAttempts; ordinal++ {
		workbench := admitPRDevelopmentRepairForTest(
			t,
			store,
			developmentCase.ID,
			fmt.Sprintf("capacity-%d", ordinal),
			version,
		)
		require.NotNil(t, workbench.RepairSession)
		claimed, found, claimErr := store.claimPRDevelopmentRepairIncludingProviderForTest(
			ctx,
			PRDevelopmentRepairClaimRequest{WorkerLabel: "capacity", Lease: time.Minute},
		)
		require.NoError(t, claimErr)
		require.True(t, found)
		attempt := activePRDevelopmentRepairAttempt(&claimed)
		require.NotNil(t, attempt)
		finished, finishErr := store.FinishPRDevelopmentRepair(
			ctx,
			PRDevelopmentRepairOutcome{
				AttemptID:  attempt.ID,
				LeaseToken: attempt.LeaseToken,
				Status:     PRDevelopmentRepairFailed,
				Summary:    "Provider was unavailable before execution.",
				ErrorCode:  PRDevelopmentRepairErrorRuntimeUnavailable,
			},
		)
		require.NoError(t, finishErr)
		version = finished.Version
	}
	_, admitted, err := store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID:                      developmentCase.ID,
			ExpectedConversationVersion: 0,
			ExpectedRepairVersion:       version,
			IdempotencyKey:              "over-capacity",
			AgentID:                     "main",
			Instruction:                 "must not commit",
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentRepairCapacity)
	assert.False(t, admitted)
	unchanged, err := store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, unchanged.RepairSession)
	assert.Len(t, unchanged.RepairSession.Attempts, MaxPRDevelopmentRepairAttempts)
	assert.Equal(t, version, unchanged.RepairSession.Version)

	otherStore, _, otherCapture := newPRDevelopmentStoreFixture(t, ":memory:")
	otherCase, created, err := otherStore.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(otherCapture),
	)
	require.NoError(t, err)
	require.True(t, created)
	limited := admitPRDevelopmentRepairForTest(t, otherStore, otherCase.ID, "version", 0)
	require.NotNil(t, limited.RepairSession)
	insufficientQueuedVersion := int64(MaxPRDevelopmentRepairVersion) -
		prDevelopmentRepairUnpinnedQueuedTransitions + 1
	_, err = otherStore.db.Exec(`
		UPDATE pr_development_repair_sessions SET version = ? WHERE id = ?`,
		insufficientQueuedVersion,
		limited.RepairSession.ID,
	)
	require.NoError(t, err)
	nextCase := capturePRDevelopmentListCase(
		t,
		otherStore,
		otherCapture,
		"delivery-repair-after-capped-session",
		"acme/next-project",
		43,
	)
	next := admitPRDevelopmentRepairForTest(t, otherStore, nextCase.ID, "next-version", 0)
	require.NotNil(t, next.RepairSession)
	claimed, found, err := otherStore.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "bounded", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	claimedAttempt := activePRDevelopmentRepairAttempt(&claimed)
	require.NotNil(t, claimedAttempt)
	assert.Equal(t, next.RepairSession.ID, claimedAttempt.SessionID)
	bounded, err := otherStore.GetPRDevelopmentWorkbench(ctx, otherCase.ID)
	require.NoError(t, err)
	require.NotNil(t, bounded.RepairSession)
	assert.Equal(t, insufficientQueuedVersion, bounded.RepairSession.Version)
	assert.Equal(t, PRDevelopmentRepairQueued, bounded.RepairSession.Attempts[0].Status)
	assert.Zero(t, bounded.RepairSession.Attempts[0].Claims)
}

func TestStorePRDevelopmentRepairRevisionHeadroomAndCappedExpiryIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	firstCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	first := admitPRDevelopmentRepairForTest(t, store, firstCase.ID, "headroom-first", 0)
	require.NotNil(t, first.RepairSession)

	// A terminal unpinned session must reserve append, claim, pin, begin, and
	// finish before accepting another attempt.
	claimed, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "headroom", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	attempt := activePRDevelopmentRepairAttempt(&claimed)
	require.NotNil(t, attempt)
	terminal, err := store.FinishPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairOutcome{
			AttemptID:  attempt.ID,
			LeaseToken: attempt.LeaseToken,
			Status:     PRDevelopmentRepairFailed,
			Summary:    "Runtime unavailable before execution.",
			ErrorCode:  PRDevelopmentRepairErrorRuntimeUnavailable,
		},
	)
	require.NoError(t, err)
	_, err = store.db.Exec(`
		UPDATE pr_development_repair_sessions SET version = ? WHERE id = ?`,
		maxPRDevelopmentRepairVersionBeforeAdmission(false)+1,
		terminal.ID,
	)
	require.NoError(t, err)
	_, admitted, err := store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID:                      firstCase.ID,
			ExpectedConversationVersion: 0,
			ExpectedRepairVersion:       maxPRDevelopmentRepairVersionBeforeAdmission(false) + 1,
			IdempotencyKey:              "headroom-rejected",
			AgentID:                     "main",
			Instruction:                 "must retain terminal headroom",
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentRepairCapacity)
	assert.False(t, admitted)

	// A legacy/corrupt capped running session is intentionally left untouched,
	// but it must not prevent unrelated queued work from being claimed.
	_, err = store.db.Exec(`
		UPDATE pr_development_repair_sessions
		SET version = ?, head_repository = ?, head_ref = ?, head_sha = ?,
		    clone_url = ?, review_digest = ?
		WHERE id = ?`,
		MaxPRDevelopmentRepairVersion,
		"review-user/project-fork",
		"repair/retries",
		strings.Repeat("a", 40),
		"https://github.com/review-user/project-fork.git",
		"sha256:"+strings.Repeat("b", 64),
		terminal.ID,
	)
	require.NoError(t, err)
	_, err = store.db.Exec(`
		UPDATE pr_development_repair_attempts
		SET status = ?, lease_owner = ?, lease_token = ?, lease_until = ?, claims = 1,
		    summary = '', error_code = '', internal_error = '', iterations = 0
		WHERE id = ?`,
		PRDevelopmentRepairRunning,
		"expired-worker",
		"expired-lease-token",
		toDBTime(clock.Add(-time.Minute)),
		attempt.ID,
	)
	require.NoError(t, err)

	secondCase := capturePRDevelopmentListCase(
		t,
		store,
		capture,
		"delivery-repair-after-capped-running",
		"acme/second-project",
		44,
	)
	second := admitPRDevelopmentRepairForTest(t, store, secondCase.ID, "headroom-second", 0)
	require.NotNil(t, second.RepairSession)
	claimed, found, err = store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "next-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, second.RepairSession.ID, claimed.ID)

	capped, err := store.GetPRDevelopmentWorkbench(ctx, firstCase.ID)
	require.NoError(t, err)
	require.NotNil(t, capped.RepairSession)
	assert.Equal(t, int64(MaxPRDevelopmentRepairVersion), capped.RepairSession.Version)
	assert.Equal(t, PRDevelopmentRepairRunning, capped.RepairSession.Attempts[0].Status)
}

func TestStoreMigratesV7ToPRDevelopmentRepairSchemaWithoutBackfill(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "repair-v7.db")
	db := openSchemaTestDB(t, path)
	installEventingSchemaThroughV7ForRepairTest(t, db)
	setSchemaTestVersion(t, db, 7)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	defer store.Close()
	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
	assert.True(t, schemaObjectExists(
		t,
		store.db,
		"table",
		"pr_development_repair_sessions",
	))
	assert.True(t, schemaObjectExists(
		t,
		store.db,
		"table",
		"pr_development_repair_attempts",
	))
	var suppressionColumns int
	require.NoError(t, store.db.QueryRow(`
		SELECT COUNT(*)
		FROM pragma_table_info('pr_development_repair_sessions')
		WHERE name = 'claim_suppressed' AND "notnull" = 1 AND dflt_value = '0'`,
	).Scan(&suppressionColumns))
	assert.Equal(t, 1, suppressionColumns)
	var sessions int
	require.NoError(t, store.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_repair_sessions`,
	).Scan(&sessions))
	assert.Zero(t, sessions)
}

func TestStorePRDevelopmentRepairMigrationValidationRollsBack(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "repair-v8-invalid.db")
	db := openSchemaTestDB(t, path)
	installEventingSchemaThroughV7ForRepairTest(t, db)
	malformed := strings.Replace(
		schemaV8PRDevelopmentRepairSessionsTable,
		"version >= 1 AND version <= 1024",
		"version >= 1 AND version <= 2048",
		1,
	)
	_, err := db.Exec(malformed)
	require.NoError(t, err)
	setSchemaTestVersion(t, db, 7)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.ErrorIs(t, err, ErrSchemaInvalid)
	assert.Contains(t, err.Error(), "validate eventing schema v8")

	db = openSchemaTestDB(t, path)
	defer db.Close()
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 7, version)
	assert.False(t, schemaObjectExists(
		t,
		db,
		"table",
		"pr_development_repair_attempts",
	), "new v8 objects must roll back")
}

func admitPRDevelopmentRepairForTest(
	t *testing.T,
	store *Store,
	caseID, marker string,
	expectedRepairVersion int64,
) PRDevelopmentWorkbench {
	t.Helper()
	workbench, admitted, err := store.AdmitPRDevelopmentRepair(
		context.Background(),
		PRDevelopmentRepairAdmit{
			CaseID:                      caseID,
			ExpectedConversationVersion: 0,
			ExpectedRepairVersion:       expectedRepairVersion,
			IdempotencyKey:              marker,
			AgentID:                     "main",
			Instruction:                 "Address the submitted review locally.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	return workbench
}

func validPRDevelopmentRepairPinForTest(
	attempt *PRDevelopmentRepairAttempt,
) PRDevelopmentRepairPin {
	return PRDevelopmentRepairPin{
		AttemptID:      attempt.ID,
		LeaseToken:     attempt.LeaseToken,
		HeadRepository: "review-user/project-fork",
		HeadRef:        "repair/retries",
		HeadSHA:        strings.Repeat("a", 40),
		CloneURL:       "https://github.com/review-user/project-fork.git",
		ReviewDigest:   "sha256:" + strings.Repeat("b", 64),
	}
}

func installEventingSchemaThroughV7ForRepairTest(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, schema := range []string{
		schemaV1,
		schemaV2,
		schemaV3,
		schemaV4,
		schemaV5,
		schemaV6,
		schemaV7,
	} {
		_, err := db.Exec(schema)
		require.NoError(t, err)
	}
}
