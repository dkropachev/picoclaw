//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type prDevelopmentOrchestrationFixture struct {
	Operation *prDevelopmentOperationFixture
	Run       PRDevelopmentRepairOrchestration
	Lease     PRDevelopmentControllerLease
}

type claimedPRDevelopmentOrchestrationFixture struct {
	Store   *Store
	Clock   *time.Time
	Case    PRDevelopmentCase
	Attempt PRDevelopmentRepairAttempt
	Run     PRDevelopmentRepairOrchestration
}

func newClaimedPRDevelopmentOrchestrationFixture(
	t *testing.T,
) *claimedPRDevelopmentOrchestrationFixture {
	t.Helper()
	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx, validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	workbench := admitPRDevelopmentRepairForTest(
		t, store, developmentCase.ID, "orchestration-claimed-attempt", 0,
	)
	require.NotNil(t, workbench.RepairSession)
	attempt := workbench.RepairSession.Attempts[0]
	run, claimed, err := store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "orchestration-worker",
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	return &claimedPRDevelopmentOrchestrationFixture{
		Store: store, Clock: clock, Case: developmentCase, Attempt: attempt, Run: run,
	}
}

func validPRDevelopmentOrchestrationPinForTest(
	fixture *claimedPRDevelopmentOrchestrationFixture,
) PRDevelopmentRepairOrchestrationPin {
	pin := validPRDevelopmentRepairPinForTest(&fixture.Attempt)
	return PRDevelopmentRepairOrchestrationPin{
		AttemptID:      fixture.Run.AttemptID,
		ClaimToken:     fixture.Run.ClaimToken,
		HeadRepository: pin.HeadRepository,
		HeadRef:        pin.HeadRef,
		HeadSHA:        pin.HeadSHA,
		CloneURL:       pin.CloneURL,
		ReviewDigest:   pin.ReviewDigest,
		WorkspaceID:    "gw-orchestration-line",
		SourceTree:     strings.Repeat("b", 40),
	}
}

func newPRDevelopmentOrchestrationFixture(
	t *testing.T,
) *prDevelopmentOrchestrationFixture {
	t.Helper()
	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	workbench := admitPRDevelopmentRepairForTest(
		t, store, developmentCase.ID, "orchestration-attempt-1", 0,
	)
	require.NotNil(t, workbench.RepairSession)
	attempt := workbench.RepairSession.Attempts[0]
	run, claimed, err := store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "orchestration-worker",
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, attempt.ID, run.AttemptID)
	pin := validPRDevelopmentRepairPinForTest(&attempt)
	run, changed, err := store.PinPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationPin{
			AttemptID:      run.AttemptID,
			ClaimToken:     run.ClaimToken,
			HeadRepository: pin.HeadRepository,
			HeadRef:        pin.HeadRef,
			HeadSHA:        pin.HeadSHA,
			CloneURL:       pin.CloneURL,
			ReviewDigest:   pin.ReviewDigest,
			WorkspaceID:    "gw-orchestration-line",
			SourceTree:     strings.Repeat("b", 40),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	lease, acquired, err := store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx,
		PRDevelopmentRepairOrchestrationControllerAcquire{
			CaseID:           developmentCase.ID,
			AttemptID:        run.AttemptID,
			ClaimToken:       run.ClaimToken,
			ExpectedRevision: 0,
			WorkerLabel:      "orchestration-worker",
			Lease:            5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	workbench, err = store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	operationFixture := &prDevelopmentOperationFixture{
		Store:      store,
		Clock:      clock,
		Case:       developmentCase,
		Session:    *workbench.RepairSession,
		Attempt:    workbench.RepairSession.Attempts[0],
		Mutation:   lease,
		SourceTree: run.SourceTree,
		NextID:     1,
	}
	_, adopted := adoptOperationForTest(t, operationFixture, lease)
	lease = operationLeaseFromTransition(adopted)
	return &prDevelopmentOrchestrationFixture{
		Operation: operationFixture,
		Run:       run,
		Lease:     lease,
	}
}

func startAndCompletePRDevelopmentOrchestrationForTest(
	t *testing.T,
	fixture *prDevelopmentOrchestrationFixture,
) PRDevelopmentRepairOrchestration {
	t.Helper()
	ctx := context.Background()
	start := validPRDevelopmentOrchestrationModelStartForTest(fixture)
	_, changed, err := fixture.Operation.Store.StartPRDevelopmentRepairOrchestrationModel(
		ctx, start,
	)
	require.NoError(t, err)
	require.True(t, changed)
	complete := validPRDevelopmentOrchestrationModelCompleteForTest(fixture)
	run, changed, err := fixture.Operation.Store.CompletePRDevelopmentRepairOrchestrationModel(
		ctx, complete,
	)
	require.NoError(t, err)
	require.True(t, changed)
	fixture.Run = run
	return run
}

func validPRDevelopmentOrchestrationModelStartForTest(
	fixture *prDevelopmentOrchestrationFixture,
) PRDevelopmentRepairOrchestrationModelStart {
	controller := fixture.Lease.Controller
	return PRDevelopmentRepairOrchestrationModelStart{
		AttemptID:          fixture.Run.AttemptID,
		ClaimToken:         fixture.Run.ClaimToken,
		ControllerID:       controller.ID,
		ControllerRevision: controller.Revision,
		MutationLeaseToken: controller.LeaseToken,
		MutationLeaseEpoch: controller.LeaseEpoch,
		ContextDigest:      strings.Repeat("1", 64),
		PromptDigest:       strings.Repeat("2", 64),
	}
}

func validPRDevelopmentOrchestrationModelCompleteForTest(
	fixture *prDevelopmentOrchestrationFixture,
) PRDevelopmentRepairOrchestrationModelComplete {
	controller := fixture.Lease.Controller
	return PRDevelopmentRepairOrchestrationModelComplete{
		AttemptID:          fixture.Run.AttemptID,
		ClaimToken:         fixture.Run.ClaimToken,
		ControllerID:       controller.ID,
		ControllerRevision: controller.Revision,
		MutationLeaseToken: controller.LeaseToken,
		MutationLeaseEpoch: controller.LeaseEpoch,
		ModelResultDigest:  strings.Repeat("3", 64),
		Summary:            "Applied the focused repair and recorded its local CI result.",
		Iterations:         2,
	}
}

func validPRDevelopmentOrchestrationValidationForTest(
	fixture *prDevelopmentOrchestrationFixture,
) PRDevelopmentRepairOrchestrationValidation {
	controller := fixture.Lease.Controller
	return PRDevelopmentRepairOrchestrationValidation{
		AttemptID:             fixture.Run.AttemptID,
		ClaimToken:            fixture.Run.ClaimToken,
		ControllerID:          controller.ID,
		ControllerRevision:    controller.Revision,
		MutationLeaseToken:    controller.LeaseToken,
		MutationLeaseEpoch:    controller.LeaseEpoch,
		ParentCommit:          controller.TipCommit,
		ParentTree:            controller.Tree,
		CandidateTree:         controller.Tree,
		CandidateDigest:       strings.Repeat("4", 64),
		NoChanges:             true,
		CIStatus:              PRDevelopmentCIEnvironmentUnavailable,
		CIAttestationID:       "lcatt_orchestration_test",
		CIAttestationDigest:   strings.Repeat("5", 64),
		CIResultKey:           strings.Repeat("6", 64),
		CIEffectivePlanDigest: strings.Repeat("7", 64),
		CIExecutionDigest:     strings.Repeat("8", 64),
	}
}

func TestStorePRDevelopmentRepairLegacyScannerFirstSkipsProviderQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx, validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	workbench := admitPRDevelopmentRepairForTest(
		t, store, developmentCase.ID, "provider-before-legacy-scan", 0,
	)
	require.NotNil(t, workbench.Thread)
	require.NotNil(t, workbench.RepairSession)
	var cohortThreadID string
	require.NoError(t, store.db.QueryRow(`
		SELECT thread_id FROM pr_development_repair_orchestration_cohorts
		WHERE session_id = ?`, workbench.RepairSession.ID).Scan(&cohortThreadID))
	assert.Equal(t, workbench.Thread.ID, cohortThreadID)
	none, found, err := store.ClaimPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "legacy-first", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, PRDevelopmentRepairSession{}, none)
	loaded, err := store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.RepairSession)
	assert.Equal(t, workbench.RepairSession.Version, loaded.RepairSession.Version)
	assert.Equal(t, PRDevelopmentRepairQueued, loaded.RepairSession.Attempts[0].Status)
	run, claimed, err := store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{WorkerLabel: "v14-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, loaded.RepairSession.Attempts[0].ID, run.AttemptID)
}

func TestStorePRDevelopmentRepairLegacyScannerDrainsExpiredProviderPreparing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx, validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	_ = admitPRDevelopmentRepairForTest(
		t, store, developmentCase.ID, "pre-v14-provider-preparing", 0,
	)
	preparing, found, err := store.claimPRDevelopmentRepairIncludingProviderForTest(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "v13-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	attempt := activePRDevelopmentRepairAttempt(&preparing)
	require.NotNil(t, attempt)
	require.NotNil(t, attempt.LeaseUntil)
	*clock = attempt.LeaseUntil.Add(time.Second)
	reclaimed, found, err := store.ClaimPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "v14-drain-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, found)
	reclaimedAttempt := activePRDevelopmentRepairAttempt(&reclaimed)
	require.NotNil(t, reclaimedAttempt)
	assert.Equal(t, attempt.ID, reclaimedAttempt.ID)
	assert.Equal(t, 2, reclaimedAttempt.Claims)
}

func TestStorePRDevelopmentRepairOrchestrationClaimReservesTerminalHeadroom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx, validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	workbench := admitPRDevelopmentRepairForTest(
		t, store, developmentCase.ID, "orchestration-headroom", 0,
	)
	require.NotNil(t, workbench.RepairSession)
	_, err = store.db.Exec(`
		UPDATE pr_development_repair_sessions SET version = ? WHERE id = ?`,
		MaxPRDevelopmentRepairVersion-1,
		workbench.RepairSession.ID,
	)
	require.NoError(t, err)
	none, claimed, err := store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{WorkerLabel: "no-headroom", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.Equal(t, PRDevelopmentRepairOrchestration{}, none)
	var suppressed int
	require.NoError(t, store.db.QueryRow(`
		SELECT claim_suppressed FROM pr_development_repair_sessions WHERE id = ?`,
		workbench.RepairSession.ID,
	).Scan(&suppressed))
	assert.Zero(t, suppressed)
}

func TestStorePRDevelopmentRepairOrchestrationPinAcquireReplayAndRedaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newClaimedPRDevelopmentOrchestrationFixture(t)
	readOnly, err := fixture.Store.GetPRDevelopmentRepairOrchestration(
		ctx, fixture.Run.AttemptID,
	)
	require.NoError(t, err)
	assert.Empty(t, readOnly.ClaimToken)
	assert.NotEmpty(t, fixture.Run.ClaimToken)
	pin := validPRDevelopmentOrchestrationPinForTest(fixture)
	pinned, changed, err := fixture.Store.PinPRDevelopmentRepairOrchestration(ctx, pin)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, pin.SourceTree, pinned.SourceTree)
	replayed, changed, err := fixture.Store.PinPRDevelopmentRepairOrchestration(ctx, pin)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, pinned.SourceTree, replayed.SourceTree)
	conflict := pin
	conflict.SourceTree = strings.Repeat("c", 40)
	_, changed, err = fixture.Store.PinPRDevelopmentRepairOrchestration(ctx, conflict)
	assert.ErrorIs(t, err, ErrPRDevelopmentOrchestrationConflict)
	assert.False(t, changed)
	readOnly, err = fixture.Store.GetPRDevelopmentRepairOrchestration(ctx, fixture.Run.AttemptID)
	require.NoError(t, err)
	assert.Empty(t, readOnly.ClaimToken)

	acquire := PRDevelopmentRepairOrchestrationControllerAcquire{
		CaseID:           fixture.Case.ID,
		AttemptID:        fixture.Run.AttemptID,
		ClaimToken:       fixture.Run.ClaimToken,
		ExpectedRevision: 0,
		WorkerLabel:      "orchestration-worker",
		Lease:            time.Minute,
	}
	lease, acquired, err := fixture.Store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx, acquire,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	replayedLease, acquired, err := fixture.Store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx, acquire,
	)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Equal(t, lease.Controller.LeaseToken, replayedLease.Controller.LeaseToken)
	foreign := acquire
	foreign.WorkerLabel = "different-worker"
	_, acquired, err = fixture.Store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx, foreign,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentOrchestrationConflict)
	assert.False(t, acquired)
	_, changed, err = fixture.Store.FailPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationFail{
			AttemptID: fixture.Run.AttemptID, ClaimToken: fixture.Run.ClaimToken,
			Summary:   "Workspace setup failed.",
			ErrorCode: PRDevelopmentRepairErrorWorkspaceUnavailable,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentOrchestrationConflict)
	assert.False(t, changed)
}

func TestStorePRDevelopmentRepairOrchestrationAcquireReplayExtendsNearExpiryController(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx, validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	workbench := admitPRDevelopmentRepairForTest(
		t, store, developmentCase.ID, "near-expiry-controller-replay", 0,
	)
	require.NotNil(t, workbench.RepairSession)
	attempt := workbench.RepairSession.Attempts[0]
	run, claimed, err := store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "stable-orchestration-worker", Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	fixture := &claimedPRDevelopmentOrchestrationFixture{
		Store: store, Clock: clock, Case: developmentCase, Attempt: attempt, Run: run,
	}
	run, pinned, err := store.PinPRDevelopmentRepairOrchestration(
		ctx, validPRDevelopmentOrchestrationPinForTest(fixture),
	)
	require.NoError(t, err)
	require.True(t, pinned)
	first, acquired, err := store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx,
		PRDevelopmentRepairOrchestrationControllerAcquire{
			CaseID: developmentCase.ID, AttemptID: run.AttemptID,
			ClaimToken: run.ClaimToken, ExpectedRevision: 0,
			WorkerLabel: "stable-orchestration-worker", Lease: time.Minute + time.Second,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, run.ClaimUntil)
	require.NotNil(t, first.Controller.LeaseUntil)
	originalDeadline := *first.Controller.LeaseUntil
	*clock = run.ClaimUntil.Add(time.Millisecond)
	reclaimed, claimed, err := store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "stable-orchestration-worker", Lease: 5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	extended, acquired, err := store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx,
		PRDevelopmentRepairOrchestrationControllerAcquire{
			CaseID: developmentCase.ID, AttemptID: reclaimed.AttemptID,
			ClaimToken: reclaimed.ClaimToken, ExpectedRevision: 0,
			WorkerLabel: "stable-orchestration-worker", Lease: 5 * time.Minute,
		},
	)
	require.NoError(t, err)
	assert.False(t, acquired, "exact replay must not rotate controller authority")
	assert.Equal(t, first.Controller.LeaseToken, extended.Controller.LeaseToken)
	assert.Equal(t, first.Controller.LeaseEpoch, extended.Controller.LeaseEpoch)
	assert.Equal(t, first.Controller.Revision, extended.Controller.Revision)
	require.NotNil(t, extended.Controller.LeaseUntil)
	assert.True(t, extended.Controller.LeaseUntil.After(originalDeadline))
	assert.Equal(t, clock.Add(5*time.Minute), *extended.Controller.LeaseUntil)
}

func TestStorePRDevelopmentRepairOrchestrationBootstrapFailReplaysAfterProgress(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newClaimedPRDevelopmentOrchestrationFixture(t)
	before, err := fixture.Store.GetPRDevelopmentWorkbench(ctx, fixture.Case.ID)
	require.NoError(t, err)
	require.NotNil(t, before.RepairSession)
	failure := PRDevelopmentRepairOrchestrationFail{
		AttemptID:     fixture.Run.AttemptID,
		ClaimToken:    fixture.Run.ClaimToken,
		Summary:       "Local runtime was unavailable before workspace pinning.",
		ErrorCode:     PRDevelopmentRepairErrorRuntimeUnavailable,
		InternalError: "runtime startup failed",
	}
	failed, changed, err := fixture.Store.FailPRDevelopmentRepairOrchestration(ctx, failure)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentRepairOrchestrationFailed, failed.Phase)
	assert.Empty(t, failed.ClaimToken)
	after, err := fixture.Store.GetPRDevelopmentWorkbench(ctx, fixture.Case.ID)
	require.NoError(t, err)
	require.NotNil(t, after.RepairSession)
	assert.Equal(t, before.RepairSession.Version+1, after.RepairSession.Version)
	require.Len(t, after.RepairSession.Attempts, 1)
	assert.Equal(t, PRDevelopmentRepairFailed, after.RepairSession.Attempts[0].Status)
	var suppressed int
	require.NoError(t, fixture.Store.db.QueryRow(`
		SELECT claim_suppressed FROM pr_development_repair_sessions WHERE id = ?`,
		after.RepairSession.ID,
	).Scan(&suppressed))
	assert.Zero(t, suppressed)

	_, admitted, err := fixture.Store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID: fixture.Case.ID, ExpectedConversationVersion: 0,
			ExpectedRepairVersion: after.RepairSession.Version,
			IdempotencyKey:        "after-failure", AgentID: "main",
			Instruction: "Try again after runtime recovery.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	replayed, changed, err := fixture.Store.FailPRDevelopmentRepairOrchestration(ctx, failure)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, failed.FailedClaimTokenDigest, replayed.FailedClaimTokenDigest)
	changedFailure := failure
	changedFailure.Summary = "A changed failure replay."
	_, changed, err = fixture.Store.FailPRDevelopmentRepairOrchestration(ctx, changedFailure)
	assert.ErrorIs(t, err, ErrPRDevelopmentOrchestrationConflict)
	assert.False(t, changed)
}

func TestStorePRDevelopmentRepairOrchestrationLaterReadyFailKeepsController(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	run := startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	_, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, validPRDevelopmentOrchestrationValidationForTest(fixture),
	)
	require.NoError(t, err)
	require.True(t, changed)
	_, _, parked := parkOperationForTest(
		t, fixture.Operation, fixture.Lease, nil, run.Summary, run.Iterations, 975,
	)
	require.NotNil(t, parked.Fence)
	reviewLease := acquireParkedPRDevelopmentLedgerReviewForTest(
		t, fixture.Operation.Store, fixture.Operation.Case.ID, fixture.Run.AttemptID,
	)
	_, changed, err = fixture.Operation.Store.AppendPRDevelopmentLedgerReview(
		ctx,
		validPRDevelopmentLedgerReviewAppendForTest(
			fixture.Operation.Case.ID, fixture.Run.AttemptID, reviewLease,
		),
	)
	require.NoError(t, err)
	require.True(t, changed)
	ready, err := fixture.Operation.Store.GetPRDevelopmentControllerForCase(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.Equal(t, PRDevelopmentControllerReady, ready.Phase)
	completedRun, err := fixture.Operation.Store.GetPRDevelopmentRepairOrchestration(
		ctx, fixture.Run.AttemptID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentRepairOrchestrationCompleted, completedRun.Phase)
	completedLedger, err := fixture.Operation.Store.GetPRDevelopmentLedgerForCase(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.Len(t, completedLedger.Entries, 2)
	workbench, err := fixture.Operation.Store.GetPRDevelopmentWorkbench(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	workbench, admitted, err := fixture.Operation.Store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID: fixture.Operation.Case.ID, ExpectedConversationVersion: 0,
			ExpectedRepairVersion: workbench.RepairSession.Version,
			IdempotencyKey:        "ready-bootstrap-fail", AgentID: "main",
			Instruction: "Attempt another local repair.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NotNil(t, workbench.RepairSession)
	secondAttempt := workbench.RepairSession.Attempts[len(workbench.RepairSession.Attempts)-1]
	secondRun, claimed, err := fixture.Operation.Store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{WorkerLabel: "ready-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, secondAttempt.ID, secondRun.AttemptID)
	_, changed, err = fixture.Operation.Store.FailPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationFail{
			AttemptID: secondRun.AttemptID, ClaimToken: secondRun.ClaimToken,
			Summary:   "Runtime failed before a new workspace pin.",
			ErrorCode: PRDevelopmentRepairErrorRuntimeUnavailable,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	stillReady, err := fixture.Operation.Store.GetPRDevelopmentControllerForCase(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerReady, stillReady.Phase)
	var suppressed int
	require.NoError(t, fixture.Operation.Store.db.QueryRow(`
		SELECT claim_suppressed FROM pr_development_repair_sessions WHERE id = ?`,
		workbench.RepairSession.ID,
	).Scan(&suppressed))
	assert.Equal(t, 1, suppressed)

	workbench, err = fixture.Operation.Store.GetPRDevelopmentWorkbench(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	workbench, admitted, err = fixture.Operation.Store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{
			CaseID: fixture.Operation.Case.ID, ExpectedConversationVersion: 0,
			ExpectedRepairVersion: workbench.RepairSession.Version,
			IdempotencyKey:        "ready-bootstrap-resume", AgentID: "main",
			Instruction: "Resume the retained line.",
		},
	)
	require.NoError(t, err)
	require.True(t, admitted)
	thirdAttempt := workbench.RepairSession.Attempts[len(workbench.RepairSession.Attempts)-1]
	thirdRun, claimed, err := fixture.Operation.Store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{WorkerLabel: "ready-worker", Lease: time.Minute},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	claimedFixture := &claimedPRDevelopmentOrchestrationFixture{
		Store: fixture.Operation.Store, Clock: fixture.Operation.Clock,
		Case: fixture.Operation.Case, Attempt: thirdAttempt, Run: thirdRun,
	}
	pin := validPRDevelopmentOrchestrationPinForTest(claimedFixture)
	pin.WorkspaceID = stillReady.WorkspaceID
	pin.SourceTree = stillReady.SourceTree
	_, changed, err = fixture.Operation.Store.PinPRDevelopmentRepairOrchestration(ctx, pin)
	require.NoError(t, err)
	require.True(t, changed)
	resumed, acquired, err := fixture.Operation.Store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx,
		PRDevelopmentRepairOrchestrationControllerAcquire{
			CaseID: fixture.Operation.Case.ID, AttemptID: thirdRun.AttemptID,
			ClaimToken: thirdRun.ClaimToken, ExpectedRevision: stillReady.Revision,
			WorkerLabel: "ready-worker", Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	assert.Equal(t, thirdRun.AttemptID, resumed.Controller.CurrentAttemptID)
}

func TestStorePRDevelopmentRepairOrchestrationClaimExcludesLegacyQueue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, capture := newPRDevelopmentStoreFixture(t, ":memory:")
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx, validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	workbench := admitPRDevelopmentRepairForTest(
		t, store, developmentCase.ID, "orchestration-claim-race", 0,
	)
	run, claimed, err := store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "controller-worker",
			Lease:       time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	legacy, found, err := store.ClaimPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairClaimRequest{WorkerLabel: "legacy-racer", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, PRDevelopmentRepairSession{}, legacy)
	loaded, err := store.GetPRDevelopmentWorkbench(ctx, developmentCase.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.RepairSession)
	assert.Equal(t, workbench.RepairSession.Version, loaded.RepairSession.Version)
	assert.Equal(t, PRDevelopmentRepairQueued, loaded.RepairSession.Attempts[0].Status)
	assert.Empty(t, loaded.RepairSession.Attempts[0].LeaseToken)
	assert.NotEmpty(t, run.ClaimToken)
	var suppressed int
	require.NoError(t, store.db.QueryRow(`
		SELECT claim_suppressed FROM pr_development_repair_sessions WHERE id = ?`,
		run.SessionID,
	).Scan(&suppressed))
	assert.Equal(t, 1, suppressed)
}

func TestStorePRDevelopmentRepairOrchestrationSafeReclaimRotatesClaimAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newClaimedPRDevelopmentOrchestrationFixture(t)
	require.NotNil(t, fixture.Run.ClaimUntil)
	oldToken := fixture.Run.ClaimToken
	*fixture.Clock = fixture.Run.ClaimUntil.Add(time.Second)
	reclaimed, claimed, err := fixture.Store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "replacement-worker", Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, fixture.Run.AttemptID, reclaimed.AttemptID)
	assert.Equal(t, fixture.Run.ClaimEpoch+1, reclaimed.ClaimEpoch)
	assert.NotEqual(t, oldToken, reclaimed.ClaimToken)
	err = fixture.Store.RenewPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationRenew{
			AttemptID: fixture.Run.AttemptID, ClaimToken: oldToken, Lease: time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrStaleLease)
	stalePin := validPRDevelopmentOrchestrationPinForTest(fixture)
	stalePin.ClaimToken = oldToken
	_, changed, err := fixture.Store.PinPRDevelopmentRepairOrchestration(ctx, stalePin)
	assert.ErrorIs(t, err, ErrStaleLease)
	assert.False(t, changed)
	_, changed, err = fixture.Store.FailPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationFail{
			AttemptID: fixture.Run.AttemptID, ClaimToken: oldToken,
			Summary:   "Stale worker failure.",
			ErrorCode: PRDevelopmentRepairErrorRuntimeUnavailable,
		},
	)
	assert.ErrorIs(t, err, ErrStaleLease)
	assert.False(t, changed)
	readOnly, err := fixture.Store.GetPRDevelopmentRepairOrchestration(
		ctx, fixture.Run.AttemptID,
	)
	require.NoError(t, err)
	assert.Empty(t, readOnly.ClaimToken)
}

func TestStorePRDevelopmentRepairOrchestrationEditingExpiryNeverReclaims(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	controller := fixture.Lease.Controller
	run, changed, err := fixture.Operation.Store.StartPRDevelopmentRepairOrchestrationModel(
		ctx,
		PRDevelopmentRepairOrchestrationModelStart{
			AttemptID:          fixture.Run.AttemptID,
			ClaimToken:         fixture.Run.ClaimToken,
			ControllerID:       controller.ID,
			ControllerRevision: controller.Revision,
			MutationLeaseToken: controller.LeaseToken,
			MutationLeaseEpoch: controller.LeaseEpoch,
			ContextDigest:      strings.Repeat("1", 64),
			PromptDigest:       strings.Repeat("2", 64),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, run.ClaimUntil)
	*fixture.Operation.Clock = run.ClaimUntil.Add(time.Second)
	none, claimed, err := fixture.Operation.Store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "replacement-worker",
			Lease:       time.Minute,
		},
	)
	require.NoError(t, err)
	assert.False(t, claimed)
	assert.Equal(t, PRDevelopmentRepairOrchestration{}, none)
	loaded, err := fixture.Operation.Store.GetPRDevelopmentRepairOrchestration(
		ctx, run.AttemptID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentRepairOrchestrationRecoveryRequired, loaded.Phase)
	assert.Empty(t, loaded.ClaimToken)
	workbench, err := fixture.Operation.Store.GetPRDevelopmentWorkbench(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	assert.Equal(
		t,
		PRDevelopmentRepairRecoveryRequired,
		workbench.RepairSession.Attempts[0].Status,
	)
}

func TestStorePRDevelopmentRepairOrchestrationModelReplayAndPhaseFencing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	start := validPRDevelopmentOrchestrationModelStartForTest(fixture)
	started, changed, err := fixture.Operation.Store.StartPRDevelopmentRepairOrchestrationModel(
		ctx, start,
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentRepairOrchestrationEditing, started.Phase)
	assert.Equal(t, fixture.Lease.Controller.LineID, started.ModelLineID)
	assert.NotEmpty(t, started.ModelLeaseTokenDigest)
	_, changed, err = fixture.Operation.Store.StartPRDevelopmentRepairOrchestrationModel(
		ctx, start,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	changedStart := start
	changedStart.PromptDigest = strings.Repeat("9", 64)
	_, changed, err = fixture.Operation.Store.StartPRDevelopmentRepairOrchestrationModel(
		ctx, changedStart,
	)
	require.Error(t, err)
	assert.False(t, changed)

	complete := validPRDevelopmentOrchestrationModelCompleteForTest(fixture)
	edited, changed, err := fixture.Operation.Store.CompletePRDevelopmentRepairOrchestrationModel(
		ctx, complete,
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentRepairOrchestrationEdited, edited.Phase)
	_, changed, err = fixture.Operation.Store.CompletePRDevelopmentRepairOrchestrationModel(
		ctx, complete,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	changedComplete := complete
	changedComplete.Summary = "Different completion evidence."
	_, changed, err = fixture.Operation.Store.CompletePRDevelopmentRepairOrchestrationModel(
		ctx, changedComplete,
	)
	require.Error(t, err)
	assert.False(t, changed)
	_, changed, err = fixture.Operation.Store.StartPRDevelopmentRepairOrchestrationModel(
		ctx, start,
	)
	require.Error(t, err)
	assert.False(t, changed)
	readOnly, err := fixture.Operation.Store.GetPRDevelopmentRepairOrchestration(
		ctx, fixture.Run.AttemptID,
	)
	require.NoError(t, err)
	assert.Empty(t, readOnly.ClaimToken)
}

func TestStorePRDevelopmentRepairOrchestrationAcceptsEveryLocalCIStatus(t *testing.T) {
	t.Parallel()
	statuses := []PRDevelopmentCIStatus{
		PRDevelopmentCIPassed,
		PRDevelopmentCIFailed,
		PRDevelopmentCIIncomplete,
		PRDevelopmentCIPlanChanged,
		PRDevelopmentCITimedOut,
		PRDevelopmentCICanceled,
		PRDevelopmentCIOutputLimitExceeded,
		PRDevelopmentCIEnvironmentUnavailable,
		PRDevelopmentCIInfrastructureError,
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			fixture := newPRDevelopmentOrchestrationFixture(t)
			startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
			input := validPRDevelopmentOrchestrationValidationForTest(fixture)
			input.CIStatus = status
			validated, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
				context.Background(), input,
			)
			require.NoError(t, err)
			require.True(t, changed)
			require.NotNil(t, validated.Validation)
			assert.Equal(t, status, validated.Validation.CIStatus)
			assert.Equal(t, validated.ModelLineID, validated.Validation.ModelLineID)
			assert.Equal(t, validated.ContextDigest, validated.Validation.ContextDigest)
			assert.Equal(t, validated.Summary, validated.Validation.ModelSummary)
		})
	}
}

func TestStorePRDevelopmentRepairOrchestrationRejectsInvalidCIAndCandidateShape(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	base := validPRDevelopmentOrchestrationValidationForTest(fixture)
	for _, status := range []PRDevelopmentCIStatus{"", "unknown"} {
		invalid := base
		invalid.CIStatus = status
		_, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
			ctx, invalid,
		)
		assert.ErrorIs(t, err, ErrInvalidPRDevelopmentOrchestration)
		assert.False(t, changed)
	}
	invalidShape := base
	invalidShape.ChangedFiles = 1
	_, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, invalidShape,
	)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentOrchestration)
	assert.False(t, changed)
}

func TestStorePRDevelopmentRepairOrchestrationValidationReplayAndConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	input := validPRDevelopmentOrchestrationValidationForTest(fixture)
	validated, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, input,
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, validated.Validation)
	assert.Equal(t, input.CIStatus, validated.Validation.CIStatus)
	assert.NotEmpty(t, validated.Validation.ReceiptHash)
	replayed, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, input,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, validated.Validation.ReceiptHash, replayed.Validation.ReceiptHash)
	conflict := input
	conflict.CIStatus = PRDevelopmentCIPassed
	_, changed, err = fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, conflict,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentOrchestrationConflict)
	assert.False(t, changed)
}

func TestStorePRDevelopmentRepairOrchestrationParkRequiresExactReceipt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	run := startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	request := operationBaseRequest(fixture.Operation, fixture.Lease.Controller)
	request.EffectIntentID = operationEffectID("pdlnpark_", 950)
	request.ExpectedVersion = fixture.Lease.Controller.LineVersion
	request.MutationEpoch = fixture.Lease.Controller.MutationEpoch
	request.PreviousTip = fixture.Lease.Controller.TipCommit
	request.Tip = fixture.Lease.Controller.TipCommit
	request.Tree = fixture.Lease.Controller.Tree
	request.NoChanges = true
	request.CompletionSummary = run.Summary
	request.CompletionIterations = run.Iterations
	prepare := func(operationID string, candidate PRDevelopmentControllerOperationRequest) error {
		_, changed, err := fixture.Operation.Store.PreparePRDevelopmentControllerOperation(
			ctx,
			PRDevelopmentControllerOperationPrepare{
				OperationID: operationID, ControllerID: fixture.Lease.Controller.ID,
				AttemptID:        fixture.Run.AttemptID,
				ExpectedRevision: fixture.Lease.Controller.Revision,
				LeaseToken:       fixture.Lease.Controller.LeaseToken,
				LeaseEpoch:       fixture.Lease.Controller.LeaseEpoch,
				Kind:             PRDevelopmentControllerOperationPark, Request: candidate,
			},
		)
		assert.False(t, changed)
		return err
	}
	err := prepare(fixture.Operation.operationID(), request)
	assert.ErrorIs(t, err, ErrPRDevelopmentOrchestrationConflict)
	_, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, validPRDevelopmentOrchestrationValidationForTest(fixture),
	)
	require.NoError(t, err)
	require.True(t, changed)
	changedRequest := request
	changedRequest.CompletionSummary = "Changed Park summary after validation."
	err = prepare(fixture.Operation.operationID(), changedRequest)
	assert.ErrorIs(t, err, ErrPRDevelopmentOrchestrationConflict)
}

func TestStorePRDevelopmentRepairOrchestrationCommitRequiresValidatedCandidate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	request := operationBaseRequest(fixture.Operation, fixture.Lease.Controller)
	request.ExpectedTree = strings.Repeat("c", 40)
	request.EffectIntentID = operationEffectID("pdcmt_", 980)
	request.ExpectedParent = fixture.Lease.Controller.TipCommit
	request.CandidateDigest = strings.Repeat("d", 64)
	request.CommitMessage = "Apply the validated candidate"
	request.AuthoredAt = fixture.Operation.Clock.UTC().Truncate(time.Second)
	_, changed, err := fixture.Operation.Store.PreparePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationPrepare{
			OperationID:  fixture.Operation.operationID(),
			ControllerID: fixture.Lease.Controller.ID, AttemptID: fixture.Run.AttemptID,
			ExpectedRevision: fixture.Lease.Controller.Revision,
			LeaseToken:       fixture.Lease.Controller.LeaseToken,
			LeaseEpoch:       fixture.Lease.Controller.LeaseEpoch,
			Kind:             PRDevelopmentControllerOperationCommit, Request: request,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentOrchestrationConflict)
	assert.False(t, changed)
	run := startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	validation := validPRDevelopmentOrchestrationValidationForTest(fixture)
	validation.CandidateTree = strings.Repeat("c", 40)
	validation.CandidateDigest = strings.Repeat("d", 64)
	validation.ChangedFiles = 2
	validation.NoChanges = false
	validated, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, validation,
	)
	require.NoError(t, err)
	require.True(t, changed)
	_, _, committed := commitOperationForTest(t, fixture.Operation, fixture.Lease, 981)
	lease := operationLeaseFromTransition(committed)
	_, _, parked := parkOperationForTest(
		t, fixture.Operation, lease, []PRDevelopmentControllerOperation{committed.Operation},
		run.Summary, run.Iterations, 982,
	)
	require.NotNil(t, parked.Fence)
	assert.False(t, parked.Fence.NoChanges)
	assert.Equal(t, validation.CandidateTree, parked.Fence.Tree)
	ledger, err := fixture.Operation.Store.GetPRDevelopmentLedgerForCase(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.Len(t, ledger.Entries, 1)
	assert.Equal(t, validated.Validation.CIStatus, ledger.Entries[0].CIStatus)
}

func TestStorePRDevelopmentRepairOrchestrationCommitReplayAfterLeaseExtension(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	run := startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	validation := validPRDevelopmentOrchestrationValidationForTest(fixture)
	validation.CandidateTree = strings.Repeat("c", len(fixture.Lease.Controller.Tree))
	validation.CandidateDigest = strings.Repeat("d", 64)
	validation.ChangedFiles = 2
	validation.NoChanges = false
	_, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, validation,
	)
	require.NoError(t, err)
	require.True(t, changed)
	operation, result, committed := commitOperationForTest(
		t, fixture.Operation, fixture.Lease, 983,
	)
	require.NotNil(t, committed.Operation.FinalizedAt)

	require.NoError(t, fixture.Operation.Store.RenewPRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerRenew{
			ControllerID: committed.Controller.ID,
			AttemptID:    operation.AttemptID,
			LeaseToken:   committed.Controller.LeaseToken,
			LeaseEpoch:   committed.Controller.LeaseEpoch,
			Lease:        10 * time.Minute,
		},
	))
	require.NotNil(t, run.ClaimUntil)
	*fixture.Operation.Clock = run.ClaimUntil.Add(time.Second)
	reclaimed, claimed, err := fixture.Operation.Store.ClaimPRDevelopmentRepairOrchestration(
		ctx,
		PRDevelopmentRepairOrchestrationClaim{
			WorkerLabel: "orchestration-worker",
			Lease:       10 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, claimed)
	extended, acquired, err := fixture.Operation.Store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx,
		PRDevelopmentRepairOrchestrationControllerAcquire{
			CaseID:           fixture.Operation.Case.ID,
			AttemptID:        run.AttemptID,
			ClaimToken:       reclaimed.ClaimToken,
			ExpectedRevision: committed.Controller.Revision,
			WorkerLabel:      "orchestration-worker",
			Lease:            10 * time.Minute,
		},
	)
	require.NoError(t, err)
	assert.False(t, acquired, "exact replay must not rotate controller authority")
	assert.Equal(t, committed.Controller.LeaseToken, extended.Controller.LeaseToken)
	assert.Equal(t, committed.Controller.LeaseEpoch, extended.Controller.LeaseEpoch)
	assert.Equal(t, committed.Controller.Revision, extended.Controller.Revision)
	assert.True(t, extended.Controller.UpdatedAt.After(*committed.Operation.FinalizedAt))
	reprepared, changed, err := fixture.Operation.Store.PreparePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationPrepare{
			OperationID:      operation.ID,
			ControllerID:     operation.ControllerID,
			AttemptID:        operation.AttemptID,
			ExpectedRevision: operation.PreparedControllerRevision,
			LeaseToken:       extended.Controller.LeaseToken,
			LeaseEpoch:       operation.MutationLeaseEpoch,
			Kind:             operation.Kind,
			Request:          operation.Request,
		},
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, PRDevelopmentControllerOperationFinalized, reprepared.Status)
	assert.Equal(t, committed.Operation.FinalHash, reprepared.FinalHash)
	assert.Equal(t, committed.Operation.Result, reprepared.Result)

	replayed, changed, err := fixture.Operation.Store.FinalizePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationFinalize{
			ControllerID:     operation.ControllerID,
			AttemptID:        operation.AttemptID,
			OperationID:      operation.ID,
			ExpectedRevision: operation.PreparedControllerRevision,
			LeaseToken:       extended.Controller.LeaseToken,
			LeaseEpoch:       operation.MutationLeaseEpoch,
			Result:           result,
		},
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, committed.Operation.FinalHash, replayed.Operation.FinalHash)

	_, _, parked := parkOperationForTest(
		t,
		fixture.Operation,
		operationLeaseFromTransition(replayed),
		[]PRDevelopmentControllerOperation{replayed.Operation},
		run.Summary,
		run.Iterations,
		984,
	)
	require.NotNil(t, parked.Fence)
	completed, err := fixture.Operation.Store.GetPRDevelopmentRepairOrchestration(
		ctx, run.AttemptID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentRepairOrchestrationCompleted, completed.Phase)
	assert.Equal(t, parked.Fence.FenceHash, completed.FenceHash)
}

func TestStorePRDevelopmentRepairOrchestrationParkAtomicallyCompletesLedger(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	run := startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	validated, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, validPRDevelopmentOrchestrationValidationForTest(fixture),
	)
	require.NoError(t, err)
	require.True(t, changed)
	park, parkResult, transition := parkOperationForTest(
		t,
		fixture.Operation,
		fixture.Lease,
		nil,
		run.Summary,
		run.Iterations,
		901,
	)
	require.NotNil(t, transition.Fence)
	assert.Equal(t, PRDevelopmentControllerReviewPending, transition.Controller.Phase)
	assert.Empty(t, transition.Controller.LeaseKind)
	assert.Empty(t, transition.Controller.LeaseToken)
	assert.Nil(t, transition.Controller.LeaseUntil)
	assert.Empty(t, transition.Controller.MutationReservationKey)
	completed, err := fixture.Operation.Store.GetPRDevelopmentRepairOrchestration(
		ctx, run.AttemptID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentRepairOrchestrationCompleted, completed.Phase)
	assert.Equal(t, park.ID, completed.ParkOperationID)
	assert.Equal(t, transition.Fence.FenceHash, completed.FenceHash)
	assert.NotEmpty(t, completed.LedgerEntryID)
	assert.Equal(t, validated.Validation.ReceiptHash, completed.Validation.ReceiptHash)
	workbench, err := fixture.Operation.Store.GetPRDevelopmentWorkbench(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	assert.Equal(t, PRDevelopmentRepairCompleted, workbench.RepairSession.Attempts[0].Status)
	ledger, err := fixture.Operation.Store.GetPRDevelopmentLedgerForCase(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.Len(t, ledger.Entries, 1)
	assert.Equal(t, completed.LedgerEntryID, ledger.Entries[0].ID)
	assert.Equal(t, validated.Validation.CIStatus, ledger.Entries[0].CIStatus)
	assert.Equal(t, validated.Validation.CIEffectivePlanDigest, ledger.Entries[0].CIPlanDigest)
	assert.Equal(t, validated.Validation.CIExecutionDigest, ledger.Entries[0].CIResultDigest)
	assert.Empty(t, completed.ClaimToken)
	replayedTransition, changed, err := fixture.Operation.Store.FinalizePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationFinalize{
			ControllerID:     park.ControllerID,
			AttemptID:        park.AttemptID,
			OperationID:      park.ID,
			ExpectedRevision: park.PreparedControllerRevision,
			LeaseToken:       fixture.Lease.Controller.LeaseToken,
			LeaseEpoch:       park.MutationLeaseEpoch,
			Result:           parkResult,
		},
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, transition.Fence.FenceHash, replayedTransition.Fence.FenceHash)
	_, err = fixture.Operation.Store.db.Exec(`
		UPDATE pr_development_repair_orchestrations
		SET ci_status = 'passed'
		WHERE attempt_id = ?`, completed.AttemptID)
	require.NoError(t, err)
	_, err = fixture.Operation.Store.GetPRDevelopmentLedgerForCase(
		ctx, fixture.Operation.Case.ID,
	)
	require.Error(t, err, "a valid-enum failed-to-passed receipt flip must fail closed")
}

func TestStorePRDevelopmentRepairOrchestrationRecoveredCommitTerminalizesOldFence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	run := startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	candidateTree := strings.Repeat("c", len(fixture.Lease.Controller.Tree))
	validation := validPRDevelopmentOrchestrationValidationForTest(fixture)
	validation.CandidateTree = candidateTree
	validation.CandidateDigest = strings.Repeat("d", 64)
	validation.ChangedFiles = 2
	validation.NoChanges = false
	validated, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, validation,
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, validated.Validation)
	request := operationBaseRequest(fixture.Operation, fixture.Lease.Controller)
	request.ExpectedTree = candidateTree
	request.EffectIntentID = operationEffectID("pdcmt_", 991)
	request.ExpectedParent = fixture.Lease.Controller.TipCommit
	request.CandidateDigest = validation.CandidateDigest
	request.CommitMessage = "Reconcile the validated candidate"
	request.AuthoredAt = fixture.Operation.Clock.UTC().Truncate(time.Second)
	operation := prepareOperationForTest(
		t, fixture.Operation, fixture.Lease, PRDevelopmentControllerOperationCommit,
		fixture.Operation.operationID(), request,
	)
	result := PRDevelopmentControllerOperationResult{
		WorkspaceID: fixture.Operation.Session.WorkspaceID, Tree: candidateTree,
		WorkspaceClean: true, IntentID: request.EffectIntentID,
		ParentCommit: request.ExpectedParent, CandidateDigest: request.CandidateDigest,
		Commit: strings.Repeat("e", len(fixture.Lease.Controller.TipCommit)), ChangedFiles: 2,
	}
	prepared := preparedPRDevelopmentOperationRecovery{
		Fixture: fixture.Operation, Lease: fixture.Lease, Operation: operation, Result: result,
	}
	quarantined := stagePRDevelopmentOperationExpiryForTest(t, prepared)
	claimed, changed, err := fixture.Operation.Store.ClaimPRDevelopmentControllerOperationRecovery(
		ctx,
		PRDevelopmentControllerOperationRecoveryClaim{
			CaseID: fixture.Operation.Case.ID, AttemptID: operation.AttemptID,
			OperationID: operation.ID, ExpectedRevision: quarantined.Revision,
			ClaimID: "v14-commit-recovery", WorkerLabel: "v14-recovery-worker",
			Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	finalize := PRDevelopmentControllerOperationRecoveryFinalize{
		ControllerID: claimed.Controller.ID, AttemptID: operation.AttemptID,
		OperationID: operation.ID, RecoveryID: claimed.Operation.RecoveryID,
		ExpectedRevision: claimed.Controller.Revision,
		ClaimID:          claimed.Operation.ClaimID, ClaimToken: claimed.Operation.ClaimToken,
		ClaimEpoch: claimed.Operation.ClaimEpoch, Lease: time.Minute,
		Rotation: recoveryRotationForOperationTest(prepared, true), Result: result,
	}
	transition, changed, err := fixture.Operation.Store.FinalizePRDevelopmentControllerOperationRecovery(
		ctx, finalize,
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentControllerSuspensionPending, transition.Controller.Phase)
	assert.Empty(t, transition.Controller.LeaseToken)
	assert.Empty(t, transition.Controller.MutationReservationKey)
	assert.Equal(t, PRDevelopmentControllerOperationFinalized, transition.Operation.Status)
	recovery, err := fixture.Operation.Store.GetPRDevelopmentRepairOrchestration(
		ctx, run.AttemptID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentRepairOrchestrationRecoveryRequired, recovery.Phase)
	assert.Empty(t, recovery.ClaimToken)
	require.NotNil(t, recovery.Validation)
	assert.Equal(t, validated.Validation.ReceiptHash, recovery.Validation.ReceiptHash)
	workbench, err := fixture.Operation.Store.GetPRDevelopmentWorkbench(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	attempt := workbench.RepairSession.Attempts[len(workbench.RepairSession.Attempts)-1]
	assert.Equal(t, PRDevelopmentRepairRecoveryRequired, attempt.Status)
	assert.Equal(t, run.Summary, attempt.Summary)
	assert.Equal(t, run.Iterations, attempt.Iterations)
	assert.Equal(t, PRDevelopmentRepairErrorRecoveryRequired, attempt.ErrorCode)
	_, claimedAgain, err := fixture.Operation.Store.ClaimPRDevelopmentRepairOrchestration(
		ctx, PRDevelopmentRepairOrchestrationClaim{WorkerLabel: "stale-fence", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, claimedAgain)
	replayed, changed, err := fixture.Operation.Store.FinalizePRDevelopmentControllerOperationRecovery(
		ctx, finalize,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, transition.Operation.FinalHash, replayed.Operation.FinalHash)
}

func TestStorePRDevelopmentRepairOrchestrationRecoveredLeaseTerminalizesEditedFence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	run := startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	require.Equal(t, PRDevelopmentRepairOrchestrationEdited, run.Phase)
	require.NotNil(t, fixture.Lease.Controller.LeaseUntil)
	*fixture.Operation.Clock = fixture.Lease.Controller.LeaseUntil.Add(time.Second)
	_, acquired, err := fixture.Operation.Store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID: fixture.Operation.Case.ID, AttemptID: run.AttemptID,
			ExpectedRevision: fixture.Lease.Controller.Revision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "replacement-after-model", Lease: time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerRecoveryRequired)
	assert.False(t, acquired)
	quarantined, err := fixture.Operation.Store.GetPRDevelopmentControllerForCase(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.Equal(t, PRDevelopmentControllerRecoveryRequired, quarantined.Phase)
	claim, changed, err := fixture.Operation.Store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{
			CaseID: fixture.Operation.Case.ID, AttemptID: run.AttemptID,
			ExpectedRevision: quarantined.Revision, ClaimID: "v14-edited-recovery",
			WorkerLabel: "v14-edited-recovery-worker", Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	finalize := PRDevelopmentControllerRecoveryFinalize{
		ControllerID: claim.Controller.ID, AttemptID: run.AttemptID,
		RecoveryID: claim.Intent.ID, ExpectedRevision: quarantined.Revision,
		ClaimID: claim.Intent.ClaimID, ClaimToken: claim.Intent.ClaimToken,
		ClaimEpoch: claim.Intent.ClaimEpoch,
		Rotation: PRDevelopmentControllerRecoveryRotationResult{
			WorkspaceID: claim.Intent.WorkspaceID, Bound: true,
			Version: claim.Intent.LineVersion, MutationEpoch: claim.Intent.MutationEpoch,
			Tip: claim.Intent.TipCommit, Tree: claim.Intent.Tree,
			RotationHash: strings.Repeat("9", 64),
		},
		Lease: time.Minute,
	}
	recoveredController, changed, err := fixture.Operation.Store.FinalizePRDevelopmentControllerRecovery(
		ctx, finalize,
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentControllerSuspensionPending, recoveredController.Phase)
	assert.Empty(t, recoveredController.LeaseToken)
	assert.Empty(t, recoveredController.MutationReservationKey)
	recovery, err := fixture.Operation.Store.GetPRDevelopmentRepairOrchestration(
		ctx, run.AttemptID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentRepairOrchestrationRecoveryRequired, recovery.Phase)
	assert.Nil(t, recovery.Validation)
	assert.Equal(t, run.ModelResultDigest, recovery.ModelResultDigest)
	assert.Equal(t, run.Summary, recovery.Summary)
	workbench, err := fixture.Operation.Store.GetPRDevelopmentWorkbench(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	attempt := workbench.RepairSession.Attempts[len(workbench.RepairSession.Attempts)-1]
	assert.Equal(t, PRDevelopmentRepairRecoveryRequired, attempt.Status)
	assert.Equal(t, run.Summary, attempt.Summary)
	_, claimed, err := fixture.Operation.Store.ClaimPRDevelopmentRepairOrchestration(
		ctx, PRDevelopmentRepairOrchestrationClaim{WorkerLabel: "old-fence", Lease: time.Minute},
	)
	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestStorePRDevelopmentRepairOrchestrationRecoveredParkIsAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	run := startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	validated, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, validPRDevelopmentOrchestrationValidationForTest(fixture),
	)
	require.NoError(t, err)
	require.True(t, changed)
	request := operationBaseRequest(fixture.Operation, fixture.Lease.Controller)
	request.EffectIntentID = operationEffectID("pdlnpark_", 990)
	request.ExpectedVersion = fixture.Lease.Controller.LineVersion
	request.MutationEpoch = fixture.Lease.Controller.MutationEpoch
	request.PreviousTip = fixture.Lease.Controller.TipCommit
	request.Tip = fixture.Lease.Controller.TipCommit
	request.Tree = fixture.Lease.Controller.Tree
	request.NoChanges = true
	request.CompletionSummary = run.Summary
	request.CompletionIterations = run.Iterations
	operation := prepareOperationForTest(
		t, fixture.Operation, fixture.Lease, PRDevelopmentControllerOperationPark,
		fixture.Operation.operationID(), request,
	)
	result := PRDevelopmentControllerOperationResult{
		WorkspaceID: fixture.Operation.Session.WorkspaceID,
		Version:     request.ExpectedVersion + 1, MutationEpoch: request.MutationEpoch,
		PreviousTip: request.PreviousTip, Tip: request.Tip, Tree: request.Tree,
		NoChanges: true, WorkspaceClean: true,
		ReviewVersion:       request.ExpectedVersion + 1,
		ReviewMutationEpoch: request.MutationEpoch,
		ReviewParkIntentID:  request.EffectIntentID,
		ReviewBaseCommit:    request.PreviousTip, ReviewCommit: request.Tip,
		ReviewTree: request.Tree, ReviewDigest: strings.Repeat("f", 64),
	}
	prepared := preparedPRDevelopmentOperationRecovery{
		Fixture: fixture.Operation, Lease: fixture.Lease, Operation: operation, Result: result,
	}
	quarantined := stagePRDevelopmentOperationExpiryForTest(t, prepared)
	before, err := fixture.Operation.Store.GetPRDevelopmentWorkbench(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, before.RepairSession)
	beforeVersion := before.RepairSession.Version
	assert.Equal(t, PRDevelopmentRepairQueued, before.RepairSession.Attempts[0].Status)
	claimed, changed, err := fixture.Operation.Store.ClaimPRDevelopmentControllerOperationRecovery(
		ctx,
		PRDevelopmentControllerOperationRecoveryClaim{
			CaseID: fixture.Operation.Case.ID, AttemptID: operation.AttemptID,
			OperationID: operation.ID, ExpectedRevision: quarantined.Revision,
			ClaimID: "v14-park-recovery", WorkerLabel: "v14-recovery-worker",
			Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	transition, changed, err := fixture.Operation.Store.FinalizePRDevelopmentControllerOperationRecovery(
		ctx,
		PRDevelopmentControllerOperationRecoveryFinalize{
			ControllerID: claimed.Controller.ID, AttemptID: operation.AttemptID,
			OperationID: operation.ID, RecoveryID: claimed.Operation.RecoveryID,
			ExpectedRevision: claimed.Controller.Revision,
			ClaimID:          claimed.Operation.ClaimID, ClaimToken: claimed.Operation.ClaimToken,
			ClaimEpoch: claimed.Operation.ClaimEpoch, Result: result,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, transition.Fence)
	assert.Equal(t, PRDevelopmentControllerReviewPending, transition.Controller.Phase)
	assert.Empty(t, transition.Controller.MutationReservationKey)
	completed, err := fixture.Operation.Store.GetPRDevelopmentRepairOrchestration(
		ctx, fixture.Run.AttemptID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentRepairOrchestrationCompleted, completed.Phase)
	assert.Equal(t, operation.ID, completed.ParkOperationID)
	assert.Equal(t, validated.Validation.ReceiptHash, completed.Validation.ReceiptHash)
	after, err := fixture.Operation.Store.GetPRDevelopmentWorkbench(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, after.RepairSession)
	assert.Equal(t, beforeVersion+1, after.RepairSession.Version)
	assert.Equal(t, PRDevelopmentRepairCompleted, after.RepairSession.Attempts[0].Status)
	ledger, err := fixture.Operation.Store.GetPRDevelopmentLedgerForCase(
		ctx, fixture.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.Len(t, ledger.Entries, 1)
	assert.Equal(t, validated.Validation.CIStatus, ledger.Entries[0].CIStatus)
	storedOperation, found, err := loadPRDevelopmentControllerOperationByID(
		ctx, fixture.Operation.Store.db, operation.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, PRDevelopmentControllerOperationFinalized, storedOperation.Status)
}
