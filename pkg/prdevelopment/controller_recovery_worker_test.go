package prdevelopment

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeControllerRecoveryStore struct {
	stage             func(context.Context, int) (int, error)
	next              func(context.Context) (eventing.PRDevelopmentControllerRecoveryCandidate, bool, error)
	nextSuspension    func(context.Context) (eventing.PRDevelopmentControllerSuspensionWorkCandidate, bool, error)
	claimSuspension   func(context.Context, eventing.PRDevelopmentControllerSuspensionClaim) (eventing.PRDevelopmentControllerSuspensionLease, bool, error)
	renewSuspension   func(context.Context, eventing.PRDevelopmentControllerSuspensionRenew) error
	finishSuspension  func(context.Context, eventing.PRDevelopmentControllerSuspensionFinalize) (eventing.PRDevelopmentControllerSuspensionTransition, bool, error)
	claimReservation  func(context.Context, eventing.PRDevelopmentControllerRecoveryClaim) (eventing.PRDevelopmentControllerRecoveryLease, bool, error)
	renewReservation  func(context.Context, eventing.PRDevelopmentControllerRecoveryRenew) error
	finishReservation func(context.Context, eventing.PRDevelopmentControllerRecoveryFinalize) (eventing.PRDevelopmentController, bool, error)
	claimOperation    func(context.Context, eventing.PRDevelopmentControllerOperationRecoveryClaim) (eventing.PRDevelopmentControllerOperationRecoveryLease, bool, error)
	renewOperation    func(context.Context, eventing.PRDevelopmentControllerOperationRecoveryRenew) error
	finishOperation   func(context.Context, eventing.PRDevelopmentControllerOperationRecoveryFinalize) (eventing.PRDevelopmentControllerOperationTransition, bool, error)
}

func (store *fakeControllerRecoveryStore) StageExpiredPRDevelopmentControllerRecoveries(
	ctx context.Context,
	limit int,
) (int, error) {
	if store.stage == nil {
		return 0, errors.New("unexpected recovery staging")
	}
	return store.stage(ctx, limit)
}

func (store *fakeControllerRecoveryStore) NextPRDevelopmentControllerRecovery(
	ctx context.Context,
) (eventing.PRDevelopmentControllerRecoveryCandidate, bool, error) {
	if store.next == nil {
		return eventing.PRDevelopmentControllerRecoveryCandidate{}, false,
			errors.New("unexpected recovery scan")
	}
	return store.next(ctx)
}

func (store *fakeControllerRecoveryStore) NextPRDevelopmentControllerSuspension(
	ctx context.Context,
) (eventing.PRDevelopmentControllerSuspensionWorkCandidate, bool, error) {
	if store.nextSuspension == nil {
		return eventing.PRDevelopmentControllerSuspensionWorkCandidate{}, false, nil
	}
	return store.nextSuspension(ctx)
}

func (store *fakeControllerRecoveryStore) ClaimPRDevelopmentControllerSuspension(
	ctx context.Context,
	input eventing.PRDevelopmentControllerSuspensionClaim,
) (eventing.PRDevelopmentControllerSuspensionLease, bool, error) {
	if store.claimSuspension == nil {
		return eventing.PRDevelopmentControllerSuspensionLease{}, false,
			errors.New("unexpected controller suspension claim")
	}
	return store.claimSuspension(ctx, input)
}

func (store *fakeControllerRecoveryStore) RenewPRDevelopmentControllerSuspension(
	ctx context.Context,
	input eventing.PRDevelopmentControllerSuspensionRenew,
) error {
	if store.renewSuspension == nil {
		return nil
	}
	return store.renewSuspension(ctx, input)
}

func (store *fakeControllerRecoveryStore) FinalizePRDevelopmentControllerSuspension(
	ctx context.Context,
	input eventing.PRDevelopmentControllerSuspensionFinalize,
) (eventing.PRDevelopmentControllerSuspensionTransition, bool, error) {
	if store.finishSuspension == nil {
		return eventing.PRDevelopmentControllerSuspensionTransition{}, false,
			errors.New("unexpected controller suspension finalization")
	}
	return store.finishSuspension(ctx, input)
}

func (store *fakeControllerRecoveryStore) ClaimPRDevelopmentControllerRecovery(
	ctx context.Context,
	input eventing.PRDevelopmentControllerRecoveryClaim,
) (eventing.PRDevelopmentControllerRecoveryLease, bool, error) {
	if store.claimReservation == nil {
		return eventing.PRDevelopmentControllerRecoveryLease{}, false,
			errors.New("unexpected reservation recovery claim")
	}
	return store.claimReservation(ctx, input)
}

func (store *fakeControllerRecoveryStore) RenewPRDevelopmentControllerRecovery(
	ctx context.Context,
	input eventing.PRDevelopmentControllerRecoveryRenew,
) error {
	if store.renewReservation == nil {
		return nil
	}
	return store.renewReservation(ctx, input)
}

func (store *fakeControllerRecoveryStore) FinalizePRDevelopmentControllerRecovery(
	ctx context.Context,
	input eventing.PRDevelopmentControllerRecoveryFinalize,
) (eventing.PRDevelopmentController, bool, error) {
	if store.finishReservation == nil {
		return eventing.PRDevelopmentController{}, false,
			errors.New("unexpected reservation recovery finalization")
	}
	return store.finishReservation(ctx, input)
}

func (store *fakeControllerRecoveryStore) ClaimPRDevelopmentControllerOperationRecovery(
	ctx context.Context,
	input eventing.PRDevelopmentControllerOperationRecoveryClaim,
) (eventing.PRDevelopmentControllerOperationRecoveryLease, bool, error) {
	if store.claimOperation == nil {
		return eventing.PRDevelopmentControllerOperationRecoveryLease{}, false,
			errors.New("unexpected operation recovery claim")
	}
	return store.claimOperation(ctx, input)
}

func (store *fakeControllerRecoveryStore) RenewPRDevelopmentControllerOperationRecovery(
	ctx context.Context,
	input eventing.PRDevelopmentControllerOperationRecoveryRenew,
) error {
	if store.renewOperation == nil {
		return nil
	}
	return store.renewOperation(ctx, input)
}

func (store *fakeControllerRecoveryStore) FinalizePRDevelopmentControllerOperationRecovery(
	ctx context.Context,
	input eventing.PRDevelopmentControllerOperationRecoveryFinalize,
) (eventing.PRDevelopmentControllerOperationTransition, bool, error) {
	if store.finishOperation == nil {
		return eventing.PRDevelopmentControllerOperationTransition{}, false,
			errors.New("unexpected operation recovery finalization")
	}
	return store.finishOperation(ctx, input)
}

type fakeControllerRecoveryGit struct {
	suspend       func(context.Context, gitworkspace.PinnedLineSuspendRequest) (gitworkspace.PinnedLineSuspendResult, error)
	suspendCommit func(context.Context, gitworkspace.PinnedLineCommitSuspensionRequest) (gitworkspace.PinnedLineSuspendResult, error)
	rotate        func(context.Context, gitworkspace.PinnedReservationRotationRequest) (gitworkspace.PinnedReservationRotationResult, error)
	adopt         func(context.Context, gitworkspace.PinnedLineAdoptRecoveryRequest) (gitworkspace.PinnedLineReservationRecoveryResult, error)
	resume        func(context.Context, gitworkspace.PinnedLineResumeRecoveryRequest) (gitworkspace.PinnedLineReservationRecoveryResult, error)
	commit        func(context.Context, gitworkspace.PinnedCommitRequest) (gitworkspace.PinnedCommitResult, error)
	park          func(context.Context, gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineParkResult, error)
	snapshot      func(context.Context, gitworkspace.PinnedLineReviewRequest) (gitworkspace.PinnedLineReviewSnapshot, error)
}

func (git *fakeControllerRecoveryGit) SuspendPinnedLine(
	ctx context.Context,
	request gitworkspace.PinnedLineSuspendRequest,
) (gitworkspace.PinnedLineSuspendResult, error) {
	if git.suspend == nil {
		return gitworkspace.PinnedLineSuspendResult{}, errors.New("unexpected suspension")
	}
	return git.suspend(ctx, request)
}

func (git *fakeControllerRecoveryGit) SuspendPinnedLineCommitRecovery(
	ctx context.Context,
	request gitworkspace.PinnedLineCommitSuspensionRequest,
) (gitworkspace.PinnedLineSuspendResult, error) {
	if git.suspendCommit == nil {
		return gitworkspace.PinnedLineSuspendResult{}, errors.New("unexpected Commit suspension")
	}
	return git.suspendCommit(ctx, request)
}

func (git *fakeControllerRecoveryGit) RotatePinnedReservation(
	ctx context.Context,
	request gitworkspace.PinnedReservationRotationRequest,
) (gitworkspace.PinnedReservationRotationResult, error) {
	if git.rotate == nil {
		return gitworkspace.PinnedReservationRotationResult{}, errors.New("unexpected rotation")
	}
	return git.rotate(ctx, request)
}

func (git *fakeControllerRecoveryGit) RecoverPinnedLineAdoptReservation(
	ctx context.Context,
	request gitworkspace.PinnedLineAdoptRecoveryRequest,
) (gitworkspace.PinnedLineReservationRecoveryResult, error) {
	if git.adopt == nil {
		return gitworkspace.PinnedLineReservationRecoveryResult{}, errors.New("unexpected Adopt recovery")
	}
	return git.adopt(ctx, request)
}

func (git *fakeControllerRecoveryGit) RecoverPinnedLineResumeReservation(
	ctx context.Context,
	request gitworkspace.PinnedLineResumeRecoveryRequest,
) (gitworkspace.PinnedLineReservationRecoveryResult, error) {
	if git.resume == nil {
		return gitworkspace.PinnedLineReservationRecoveryResult{}, errors.New("unexpected Resume recovery")
	}
	return git.resume(ctx, request)
}

func (git *fakeControllerRecoveryGit) CommitPinned(
	ctx context.Context,
	request gitworkspace.PinnedCommitRequest,
) (gitworkspace.PinnedCommitResult, error) {
	if git.commit == nil {
		return gitworkspace.PinnedCommitResult{}, errors.New("unexpected Commit recovery")
	}
	return git.commit(ctx, request)
}

func (git *fakeControllerRecoveryGit) ParkPinnedLine(
	ctx context.Context,
	request gitworkspace.PinnedLineParkRequest,
) (gitworkspace.PinnedLineParkResult, error) {
	if git.park == nil {
		return gitworkspace.PinnedLineParkResult{}, errors.New("unexpected Park recovery")
	}
	return git.park(ctx, request)
}

func (git *fakeControllerRecoveryGit) SnapshotPinnedLineReview(
	ctx context.Context,
	request gitworkspace.PinnedLineReviewRequest,
) (gitworkspace.PinnedLineReviewSnapshot, error) {
	if git.snapshot == nil {
		return gitworkspace.PinnedLineReviewSnapshot{}, errors.New("unexpected Park snapshot")
	}
	return git.snapshot(ctx, request)
}

func TestControllerRecoveryWorkerNoWorkAndScannerFailures(t *testing.T) {
	t.Parallel()

	stageErr := errors.New("stage unavailable")
	scanErr := errors.New("scan unavailable")
	tests := []struct {
		name        string
		stageErr    error
		scanErr     error
		wantErr     error
		wantScanned bool
	}{
		{name: "empty", wantScanned: true},
		{name: "stage failure", stageErr: stageErr, wantErr: stageErr},
		{name: "scan failure", scanErr: scanErr, wantErr: scanErr, wantScanned: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scanned := false
			factoryCalled := false
			store := &fakeControllerRecoveryStore{
				stage: func(_ context.Context, limit int) (int, error) {
					require.Equal(t, controllerRecoveryStageBatch, limit)
					return 0, test.stageErr
				},
				next: func(context.Context) (eventing.PRDevelopmentControllerRecoveryCandidate, bool, error) {
					scanned = true
					return eventing.PRDevelopmentControllerRecoveryCandidate{}, false, test.scanErr
				},
			}
			worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
				factoryCalled = true
				return &fakeControllerRecoveryGit{}, nil
			})
			processed, err := worker.ProcessOne(nil)
			require.False(t, processed)
			if test.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, test.wantErr)
			}
			assert.Equal(t, test.wantScanned, scanned)
			assert.False(t, factoryCalled)
		})
	}
}

func TestControllerRecoveryWorkerExecutesDurableSuspensionModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mode     eventing.PRDevelopmentControllerSuspensionMode
		replayed bool
	}{
		{
			name: "candidate",
			mode: eventing.PRDevelopmentControllerSuspensionCandidate,
		},
		{
			name:     "Commit recovery replay",
			mode:     eventing.PRDevelopmentControllerSuspensionCommitRecovery,
			replayed: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			candidate, controller, suspension := controllerSuspensionFixture(test.mode)
			gitResult := controllerSuspensionGitResult(suspension, test.replayed)
			var (
				claimInput   eventing.PRDevelopmentControllerSuspensionClaim
				renewInput   eventing.PRDevelopmentControllerSuspensionRenew
				finalInput   eventing.PRDevelopmentControllerSuspensionFinalize
				suspendInput gitworkspace.PinnedLineSuspendRequest
				commitInput  gitworkspace.PinnedLineCommitSuspensionRequest
				stageCalled  bool
				recoveryScan bool
				order        []string
			)
			store := &fakeControllerRecoveryStore{
				nextSuspension: func(context.Context) (
					eventing.PRDevelopmentControllerSuspensionWorkCandidate,
					bool,
					error,
				) {
					return candidate, true, nil
				},
				stage: func(context.Context, int) (int, error) {
					stageCalled = true
					return 0, nil
				},
				next: func(context.Context) (
					eventing.PRDevelopmentControllerRecoveryCandidate,
					bool,
					error,
				) {
					recoveryScan = true
					return eventing.PRDevelopmentControllerRecoveryCandidate{}, false, nil
				},
				claimSuspension: func(
					_ context.Context,
					input eventing.PRDevelopmentControllerSuspensionClaim,
				) (eventing.PRDevelopmentControllerSuspensionLease, bool, error) {
					claimInput = input
					suspension = claimedControllerSuspension(suspension, input)
					return eventing.PRDevelopmentControllerSuspensionLease{
						Controller: controller,
						Suspension: suspension,
						Reclaimed:  test.replayed,
					}, !test.replayed, nil
				},
				renewSuspension: func(
					_ context.Context,
					input eventing.PRDevelopmentControllerSuspensionRenew,
				) error {
					order = append(order, "renew")
					renewInput = input
					return nil
				},
				finishSuspension: func(
					_ context.Context,
					input eventing.PRDevelopmentControllerSuspensionFinalize,
				) (eventing.PRDevelopmentControllerSuspensionTransition, bool, error) {
					order = append(order, "finalize")
					finalInput = input
					return suspendedControllerTransition(controller, suspension, input.Result),
						!test.replayed, nil
				},
			}
			git := &fakeControllerRecoveryGit{
				suspend: func(
					_ context.Context,
					input gitworkspace.PinnedLineSuspendRequest,
				) (gitworkspace.PinnedLineSuspendResult, error) {
					order = append(order, "git")
					suspendInput = input
					return gitResult, nil
				},
				suspendCommit: func(
					_ context.Context,
					input gitworkspace.PinnedLineCommitSuspensionRequest,
				) (gitworkspace.PinnedLineSuspendResult, error) {
					order = append(order, "git")
					commitInput = input
					return gitResult, nil
				},
			}
			worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
				return git, nil
			})

			processed, err := worker.ProcessOne(context.Background())
			require.NoError(t, err)
			require.True(t, processed)
			assert.False(t, stageCalled)
			assert.False(t, recoveryScan)
			assert.Equal(t, []string{"git", "renew", "finalize"}, order)
			assert.Equal(t, candidate.CaseID, claimInput.CaseID)
			assert.Equal(t, candidate.SuspensionID, claimInput.SuspensionID)
			assert.Equal(t, candidate.ExpectedRevision, claimInput.ExpectedRevision)
			assert.Equal(
				t,
				controllerSuspensionClaimID(worker.workerLabel, candidate),
				claimInput.ClaimID,
			)
			assert.Equal(t, suspension.SuspendClaimToken, finalInput.ClaimToken)
			assert.Equal(t, test.replayed, finalInput.Result.AlreadySuspended)
			assert.Equal(t, suspension.ID, renewInput.SuspensionID)
			assert.Equal(t, suspension.SuspendClaimToken, renewInput.ClaimToken)
			assert.Equal(t, worker.lease, renewInput.Lease)

			request := suspension.SuspendRequest
			pin := gitworkspace.PinnedAcquireRequest{
				Repository:     request.Repository,
				SourceRef:      request.SourceRef,
				ExpectedCommit: request.SourceCommit,
				ReservationKey: request.ReservationKey,
				AgentID:        request.AgentID,
			}
			expectedSuspend := gitworkspace.PinnedLineSuspendRequest{
				Pin:                   pin,
				WorkspaceID:           request.WorkspaceID,
				LineID:                request.LineID,
				IntentID:              request.IntentID,
				ExpectedVersion:       request.ExpectedVersion,
				ExpectedMutationEpoch: request.ExpectedMutationEpoch,
				ExpectedTip:           request.ExpectedTip,
				ExpectedTree:          request.ExpectedTree,
			}
			if test.mode == eventing.PRDevelopmentControllerSuspensionCandidate {
				assert.Equal(t, expectedSuspend, suspendInput)
				assert.Equal(t, gitworkspace.PinnedLineCommitSuspensionRequest{}, commitInput)
			} else {
				assert.Equal(t, gitworkspace.PinnedLineCommitSuspensionRequest{
					Suspend: expectedSuspend,
					Commit: gitworkspace.PinnedCommitRequest{
						Pin:                     pin,
						WorkspaceID:             request.WorkspaceID,
						IntentID:                request.CommitIntentID,
						ExpectedParent:          request.CommitExpectedParent,
						ExpectedTree:            request.CommitExpectedTree,
						ExpectedCandidateDigest: request.CommitCandidateDigest,
						Message:                 request.CommitMessage,
						AuthoredAt:              request.CommitAuthoredAt,
					},
				}, commitInput)
				assert.Equal(
					t,
					gitworkspace.PinnedLineSuspendRequest{},
					suspendInput,
				)
			}
		})
	}
}

func TestControllerRecoveryWorkerSuspensionPriority(t *testing.T) {
	t.Parallel()

	candidate, _, _ := controllerSuspensionFixture(
		eventing.PRDevelopmentControllerSuspensionCandidate,
	)
	claimErr := errors.New("stop after priority claim")
	t.Run("existing handoff precedes staging", func(t *testing.T) {
		stageCalled := false
		recoveryScanned := false
		store := &fakeControllerRecoveryStore{
			nextSuspension: func(context.Context) (
				eventing.PRDevelopmentControllerSuspensionWorkCandidate,
				bool,
				error,
			) {
				return candidate, true, nil
			},
			claimSuspension: func(
				context.Context,
				eventing.PRDevelopmentControllerSuspensionClaim,
			) (eventing.PRDevelopmentControllerSuspensionLease, bool, error) {
				return eventing.PRDevelopmentControllerSuspensionLease{}, false, claimErr
			},
			stage: func(context.Context, int) (int, error) {
				stageCalled = true
				return 0, nil
			},
			next: func(context.Context) (
				eventing.PRDevelopmentControllerRecoveryCandidate,
				bool,
				error,
			) {
				recoveryScanned = true
				return eventing.PRDevelopmentControllerRecoveryCandidate{}, false, nil
			},
		}
		worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
			return &fakeControllerRecoveryGit{}, nil
		})

		processed, err := worker.ProcessOne(context.Background())
		require.False(t, processed)
		require.ErrorIs(t, err, claimErr)
		assert.False(t, stageCalled)
		assert.False(t, recoveryScanned)
	})

	t.Run("staged handoff precedes recovery scan", func(t *testing.T) {
		staged := false
		suspensionScans := 0
		recoveryScanned := false
		store := &fakeControllerRecoveryStore{
			nextSuspension: func(context.Context) (
				eventing.PRDevelopmentControllerSuspensionWorkCandidate,
				bool,
				error,
			) {
				suspensionScans++
				return candidate, staged, nil
			},
			stage: func(_ context.Context, limit int) (int, error) {
				require.Equal(t, controllerRecoveryStageBatch, limit)
				staged = true
				return 1, nil
			},
			claimSuspension: func(
				context.Context,
				eventing.PRDevelopmentControllerSuspensionClaim,
			) (eventing.PRDevelopmentControllerSuspensionLease, bool, error) {
				return eventing.PRDevelopmentControllerSuspensionLease{}, false, claimErr
			},
			next: func(context.Context) (
				eventing.PRDevelopmentControllerRecoveryCandidate,
				bool,
				error,
			) {
				recoveryScanned = true
				return eventing.PRDevelopmentControllerRecoveryCandidate{}, false, nil
			},
		}
		worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
			return &fakeControllerRecoveryGit{}, nil
		})

		processed, err := worker.ProcessOne(context.Background())
		require.False(t, processed)
		require.ErrorIs(t, err, claimErr)
		assert.Equal(t, 2, suspensionScans)
		assert.False(t, recoveryScanned)
	})
}

func TestControllerRecoveryWorkerSuspensionClaimLossCancelsGit(t *testing.T) {
	t.Parallel()

	candidate, controller, suspension := controllerSuspensionFixture(
		eventing.PRDevelopmentControllerSuspensionCandidate,
	)
	renewErr := errors.New("suspension claim lost")
	finalized := false
	var renewed eventing.PRDevelopmentControllerSuspensionRenew
	store := &fakeControllerRecoveryStore{
		nextSuspension: func(context.Context) (
			eventing.PRDevelopmentControllerSuspensionWorkCandidate,
			bool,
			error,
		) {
			return candidate, true, nil
		},
		claimSuspension: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspensionClaim,
		) (eventing.PRDevelopmentControllerSuspensionLease, bool, error) {
			suspension = claimedControllerSuspension(suspension, input)
			return eventing.PRDevelopmentControllerSuspensionLease{
				Controller: controller,
				Suspension: suspension,
			}, true, nil
		},
		renewSuspension: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspensionRenew,
		) error {
			renewed = input
			return renewErr
		},
		finishSuspension: func(
			context.Context,
			eventing.PRDevelopmentControllerSuspensionFinalize,
		) (eventing.PRDevelopmentControllerSuspensionTransition, bool, error) {
			finalized = true
			return eventing.PRDevelopmentControllerSuspensionTransition{}, false, nil
		},
	}
	git := &fakeControllerRecoveryGit{
		suspend: func(
			ctx context.Context,
			_ gitworkspace.PinnedLineSuspendRequest,
		) (gitworkspace.PinnedLineSuspendResult, error) {
			<-ctx.Done()
			return gitworkspace.PinnedLineSuspendResult{}, ctx.Err()
		},
	}
	worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
		return git, nil
	})
	worker.heartbeatInterval = func(time.Duration) time.Duration { return 5 * time.Millisecond }

	processed, err := worker.ProcessOne(context.Background())
	require.True(t, processed)
	require.ErrorIs(t, err, renewErr)
	assert.False(t, finalized)
	assert.Equal(t, suspension.ID, renewed.SuspensionID)
	assert.Equal(t, suspension.ControllerID, renewed.ControllerID)
	assert.Equal(t, suspension.SuspendClaimID, renewed.ClaimID)
	assert.Equal(t, suspension.SuspendClaimToken, renewed.ClaimToken)
	assert.Equal(t, suspension.SuspendClaimEpoch, renewed.ClaimEpoch)
	assert.Equal(t, worker.lease, renewed.Lease)
}

func TestControllerRecoveryWorkerSuspensionSecurityBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("changed bearer fails before Git", func(t *testing.T) {
		candidate, controller, suspension := controllerSuspensionFixture(
			eventing.PRDevelopmentControllerSuspensionCandidate,
		)
		factoryCalled := false
		store := &fakeControllerRecoveryStore{
			nextSuspension: func(context.Context) (
				eventing.PRDevelopmentControllerSuspensionWorkCandidate,
				bool,
				error,
			) {
				return candidate, true, nil
			},
			claimSuspension: func(
				_ context.Context,
				input eventing.PRDevelopmentControllerSuspensionClaim,
			) (eventing.PRDevelopmentControllerSuspensionLease, bool, error) {
				suspension = claimedControllerSuspension(suspension, input)
				suspension.SuspendRequest.ReservationKey = "changed-bearer"
				return eventing.PRDevelopmentControllerSuspensionLease{
					Controller: controller,
					Suspension: suspension,
				}, true, nil
			},
		}
		worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
			factoryCalled = true
			return &fakeControllerRecoveryGit{}, nil
		})

		processed, err := worker.ProcessOne(context.Background())
		require.True(t, processed)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "changed authority")
		assert.False(t, factoryCalled)
	})

	transitionTests := []struct {
		name    string
		wantErr string
		mutate  func(*eventing.PRDevelopmentControllerSuspensionTransition, string)
	}{
		{
			name:    "final transition cannot retain raw bearer",
			wantErr: "retained bearer authority",
			mutate: func(
				transition *eventing.PRDevelopmentControllerSuspensionTransition,
				bearer string,
			) {
				transition.Suspension.SuspensionReservationKey = bearer
			},
		},
		{
			name:    "final transition cannot change owner identity",
			wantErr: "changed durable identity",
			mutate: func(
				transition *eventing.PRDevelopmentControllerSuspensionTransition,
				_ string,
			) {
				transition.Suspension.ThreadID = "changed-thread"
			},
		},
		{
			name:    "final transition cannot contain premature resume evidence",
			wantErr: "retained bearer authority",
			mutate: func(
				transition *eventing.PRDevelopmentControllerSuspensionTransition,
				_ string,
			) {
				transition.Suspension.ResumeIntentID = "premature-resume"
			},
		},
	}
	for _, test := range transitionTests {
		t.Run(test.name, func(t *testing.T) {
			candidate, controller, suspension := controllerSuspensionFixture(
				eventing.PRDevelopmentControllerSuspensionCandidate,
			)
			gitResult := controllerSuspensionGitResult(suspension, false)
			store := &fakeControllerRecoveryStore{
				nextSuspension: func(context.Context) (
					eventing.PRDevelopmentControllerSuspensionWorkCandidate,
					bool,
					error,
				) {
					return candidate, true, nil
				},
				claimSuspension: func(
					_ context.Context,
					input eventing.PRDevelopmentControllerSuspensionClaim,
				) (eventing.PRDevelopmentControllerSuspensionLease, bool, error) {
					suspension = claimedControllerSuspension(suspension, input)
					return eventing.PRDevelopmentControllerSuspensionLease{
						Controller: controller,
						Suspension: suspension,
					}, true, nil
				},
				finishSuspension: func(
					_ context.Context,
					input eventing.PRDevelopmentControllerSuspensionFinalize,
				) (eventing.PRDevelopmentControllerSuspensionTransition, bool, error) {
					transition := suspendedControllerTransition(
						controller,
						suspension,
						input.Result,
					)
					test.mutate(&transition, suspension.SuspensionReservationKey)
					return transition, true, nil
				},
			}
			git := &fakeControllerRecoveryGit{
				suspend: func(
					context.Context,
					gitworkspace.PinnedLineSuspendRequest,
				) (gitworkspace.PinnedLineSuspendResult, error) {
					return gitResult, nil
				},
			}
			worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
				return git, nil
			})

			processed, err := worker.ProcessOne(context.Background())
			require.True(t, processed)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestControllerRecoveryWorkerRotatesBoundReservationIntoSuspensionPending(t *testing.T) {
	t.Parallel()

	candidate, controller, intent := reservationRecoveryFixture()
	var (
		claimInput    eventing.PRDevelopmentControllerRecoveryClaim
		rotateRequest gitworkspace.PinnedReservationRotationRequest
		finalInput    eventing.PRDevelopmentControllerRecoveryFinalize
	)
	store := &fakeControllerRecoveryStore{
		stage: func(context.Context, int) (int, error) { return 1, nil },
		next: func(context.Context) (eventing.PRDevelopmentControllerRecoveryCandidate, bool, error) {
			return candidate, true, nil
		},
		claimReservation: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerRecoveryClaim,
		) (eventing.PRDevelopmentControllerRecoveryLease, bool, error) {
			claimInput = input
			intent = claimedReservationRecovery(intent, input)
			return eventing.PRDevelopmentControllerRecoveryLease{
				Controller: controller,
				Intent:     intent,
				Reclaimed:  true,
			}, false, nil
		},
		finishReservation: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerRecoveryFinalize,
		) (eventing.PRDevelopmentController, bool, error) {
			finalInput = input
			return suspensionPendingController(controller), true, nil
		},
	}
	git := &fakeControllerRecoveryGit{
		rotate: func(
			_ context.Context,
			request gitworkspace.PinnedReservationRotationRequest,
		) (gitworkspace.PinnedReservationRotationResult, error) {
			rotateRequest = request
			return gitworkspace.PinnedReservationRotationResult{
				WorkspaceID:    intent.WorkspaceID,
				Bound:          true,
				Version:        intent.LineVersion,
				MutationEpoch:  intent.MutationEpoch,
				Tip:            intent.TipCommit,
				Tree:           intent.Tree,
				RotationHash:   testDigest("7"),
				AlreadyRotated: true,
			}, nil
		},
	}
	worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
		return git, nil
	})

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	require.True(t, processed)

	assert.Equal(t, candidate.CaseID, claimInput.CaseID)
	assert.Equal(t, candidate.AttemptID, claimInput.AttemptID)
	assert.Equal(t, candidate.ExpectedRevision, claimInput.ExpectedRevision)
	assert.Equal(t, controllerRecoveryClaimID(worker.workerLabel, candidate), claimInput.ClaimID)
	assert.Equal(t, worker.workerLabel, claimInput.WorkerLabel)
	assert.Equal(t, worker.lease, claimInput.Lease)
	assert.True(t, rotateRequest.RequireSuspensionCapacity)
	assert.Equal(t, intent.PreviousReservationKey, rotateRequest.Pin.ReservationKey)
	assert.Equal(t, intent.AgentID, rotateRequest.Pin.AgentID)
	assert.Equal(t, intent.ReplacementReservationKey, rotateRequest.ReplacementReservationKey)
	assert.Equal(t, intent.ID, rotateRequest.IntentID)
	assert.Equal(t, intent.LineID, rotateRequest.LineID)
	assert.Equal(t, intent.ClaimToken, finalInput.ClaimToken)
	assert.Equal(t, testDigest("7"), finalInput.Rotation.RotationHash)
	assert.True(t, finalInput.Rotation.AlreadyRotated)
	assert.Equal(t, worker.lease, finalInput.Lease)
}

func TestControllerRecoveryWorkerRecoversAdoptAndResume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind eventing.PRDevelopmentControllerOperationKind
	}{
		{name: "Adopt", kind: eventing.PRDevelopmentControllerOperationAdopt},
		{name: "Resume", kind: eventing.PRDevelopmentControllerOperationResume},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate, controller, operation := operationRecoveryFixture(test.kind)
			var finalized eventing.PRDevelopmentControllerOperationRecoveryFinalize
			store := operationRecoveryStore(
				candidate,
				controller,
				operation,
				func(input eventing.PRDevelopmentControllerOperationRecoveryFinalize) (
					eventing.PRDevelopmentControllerOperationTransition,
					error,
				) {
					finalized = input
					return suspendedOperationTransition(controller, operation), nil
				},
			)
			git := &fakeControllerRecoveryGit{}
			switch test.kind {
			case eventing.PRDevelopmentControllerOperationAdopt:
				git.adopt = func(
					_ context.Context,
					request gitworkspace.PinnedLineAdoptRecoveryRequest,
				) (gitworkspace.PinnedLineReservationRecoveryResult, error) {
					assert.True(t, request.RequireSuspensionCapacity)
					assert.Equal(t, operation.RecoveryID, request.IntentID)
					assert.Equal(t, controller.MutationReservationKey, request.Adopt.Pin.ReservationKey)
					assert.Equal(t, operation.ReplacementReservationKey, request.ReplacementReservationKey)
					assert.Equal(t, operation.Request.ExpectedTree, request.Adopt.ExpectedTree)
					return gitworkspace.PinnedLineReservationRecoveryResult{
						WorkspaceID:    operation.WorkspaceID,
						Version:        0,
						MutationEpoch:  1,
						Tip:            operation.SourceCommit,
						Tree:           operation.SourceTree,
						RotationHash:   testDigest("8"),
						AlreadyRotated: true,
					}, nil
				}
			case eventing.PRDevelopmentControllerOperationResume:
				git.resume = func(
					_ context.Context,
					request gitworkspace.PinnedLineResumeRecoveryRequest,
				) (gitworkspace.PinnedLineReservationRecoveryResult, error) {
					assert.True(t, request.RequireSuspensionCapacity)
					assert.Equal(t, operation.RecoveryID, request.IntentID)
					assert.Equal(t, controller.MutationReservationKey, request.Resume.Pin.ReservationKey)
					assert.Equal(t, operation.Request.ExpectedVersion, request.Resume.ExpectedVersion)
					assert.Equal(t, operation.Request.ExpectedEpoch, request.Resume.ExpectedEpoch)
					return gitworkspace.PinnedLineReservationRecoveryResult{
						WorkspaceID:   operation.WorkspaceID,
						Version:       operation.LineVersion,
						MutationEpoch: operation.MutationEpoch + 1,
						Tip:           operation.TipCommit,
						Tree:          operation.Tree,
						RotationHash:  testDigest("9"),
					}, nil
				}
			}
			worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
				return git, nil
			})

			processed, err := worker.ProcessOne(context.Background())
			require.NoError(t, err)
			require.True(t, processed)
			assert.Equal(t, operation.RecoveryID, finalized.RecoveryID)
			assert.True(t, finalized.Rotation.Bound)
			assert.Equal(t, operation.WorkspaceID, finalized.Result.WorkspaceID)
			assert.Equal(t, worker.lease, finalized.Lease)
		})
	}
}

func TestControllerRecoveryWorkerCommitPreservesWorkspaceDriftProof(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		workspaceClean bool
		commitErr      error
		wantError      bool
		wantFinalize   bool
	}{
		{name: "clean", workspaceClean: true, wantFinalize: true},
		{
			name:         "proven drift",
			commitErr:    gitworkspace.ErrPinnedCommitWorkspaceDrift,
			wantFinalize: true,
		},
		{name: "other failure", commitErr: errors.New("commit failed"), wantError: true},
		{
			name:           "inconsistent drift proof",
			workspaceClean: true,
			commitErr:      gitworkspace.ErrPinnedCommitWorkspaceDrift,
			wantError:      true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate, controller, operation := operationRecoveryFixture(
				eventing.PRDevelopmentControllerOperationCommit,
			)
			var (
				mu        sync.Mutex
				order     []string
				finalized *eventing.PRDevelopmentControllerOperationRecoveryFinalize
			)
			store := operationRecoveryStore(
				candidate,
				controller,
				operation,
				func(input eventing.PRDevelopmentControllerOperationRecoveryFinalize) (
					eventing.PRDevelopmentControllerOperationTransition,
					error,
				) {
					copyInput := input
					finalized = &copyInput
					return suspendedOperationTransition(controller, operation), nil
				},
			)
			git := &fakeControllerRecoveryGit{
				rotate: func(
					_ context.Context,
					request gitworkspace.PinnedReservationRotationRequest,
				) (gitworkspace.PinnedReservationRotationResult, error) {
					mu.Lock()
					order = append(order, "rotate")
					mu.Unlock()
					assert.True(t, request.RequireSuspensionCapacity)
					assert.Equal(t, controller.MutationReservationKey, request.Pin.ReservationKey)
					return gitworkspace.PinnedReservationRotationResult{
						WorkspaceID:   operation.WorkspaceID,
						Bound:         true,
						Version:       operation.LineVersion,
						MutationEpoch: operation.MutationEpoch,
						Tip:           operation.TipCommit,
						Tree:          operation.Tree,
						RotationHash:  testDigest("a"),
					}, nil
				},
				commit: func(
					_ context.Context,
					request gitworkspace.PinnedCommitRequest,
				) (gitworkspace.PinnedCommitResult, error) {
					mu.Lock()
					order = append(order, "commit")
					mu.Unlock()
					assert.Equal(t, operation.ReplacementReservationKey, request.Pin.ReservationKey)
					assert.Equal(t, operation.Request.EffectIntentID, request.IntentID)
					assert.Equal(t, operation.Request.CandidateDigest, request.ExpectedCandidateDigest)
					return gitworkspace.PinnedCommitResult{
						WorkspaceID:     operation.WorkspaceID,
						IntentID:        operation.Request.EffectIntentID,
						ParentCommit:    operation.Request.ExpectedParent,
						Tree:            operation.Request.ExpectedTree,
						CandidateDigest: operation.Request.CandidateDigest,
						Commit:          testObjectID("6"),
						ChangedFiles:    2,
						AlreadyApplied:  true,
						WorkspaceClean:  test.workspaceClean,
					}, test.commitErr
				},
			}
			worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
				return git, nil
			})

			processed, err := worker.ProcessOne(context.Background())
			require.True(t, processed)
			if test.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, []string{"rotate", "commit"}, order)
			if test.wantFinalize {
				require.NotNil(t, finalized)
				assert.Equal(t, test.workspaceClean, finalized.Result.WorkspaceClean)
				assert.Equal(t, testObjectID("6"), finalized.Result.Commit)
				assert.True(t, finalized.Result.AlreadyApplied)
			} else {
				assert.Nil(t, finalized)
			}
		})
	}
}

func TestControllerRecoveryWorkerParkSnapshotsExactReviewAndFinalizesDirectly(t *testing.T) {
	t.Parallel()

	candidate, controller, operation := operationRecoveryFixture(
		eventing.PRDevelopmentControllerOperationPark,
	)
	var finalized eventing.PRDevelopmentControllerOperationRecoveryFinalize
	store := operationRecoveryStore(
		candidate,
		controller,
		operation,
		func(input eventing.PRDevelopmentControllerOperationRecoveryFinalize) (
			eventing.PRDevelopmentControllerOperationTransition,
			error,
		) {
			finalized = input
			return parkedOperationTransition(controller, operation, input.Result), nil
		},
	)
	parked := gitworkspace.PinnedLineParkResult{
		WorkspaceID:    operation.WorkspaceID,
		Version:        operation.Request.ExpectedVersion + 1,
		MutationEpoch:  operation.Request.MutationEpoch,
		PreviousTip:    operation.Request.PreviousTip,
		Tip:            operation.Request.Tip,
		Tree:           operation.Request.Tree,
		NoChanges:      operation.Request.NoChanges,
		AlreadyParked:  true,
		WorkspaceClean: true,
	}
	git := &fakeControllerRecoveryGit{
		park: func(
			_ context.Context,
			request gitworkspace.PinnedLineParkRequest,
		) (gitworkspace.PinnedLineParkResult, error) {
			assert.Equal(t, controller.MutationReservationKey, request.Pin.ReservationKey)
			assert.Equal(t, operation.Request.EffectIntentID, request.IntentID)
			assert.Equal(t, operation.Request.PreviousTip, request.PreviousTip)
			return parked, nil
		},
		snapshot: func(
			_ context.Context,
			request gitworkspace.PinnedLineReviewRequest,
		) (gitworkspace.PinnedLineReviewSnapshot, error) {
			assert.Equal(t, parked.Version, request.ExpectedVersion)
			assert.Equal(t, parked.PreviousTip, request.ExpectedBase)
			assert.Equal(t, parked.Tip, request.ExpectedTip)
			return gitworkspace.PinnedLineReviewSnapshot{
				Version:       parked.Version,
				MutationEpoch: parked.MutationEpoch,
				ParkIntentID:  operation.Request.EffectIntentID,
				BaseCommit:    parked.PreviousTip,
				Commit:        parked.Tip,
				Tree:          parked.Tree,
				ReviewDigest:  testDigest("b"),
			}, nil
		},
	}
	worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
		return git, nil
	})

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	assert.Empty(t, finalized.RecoveryID)
	assert.Zero(t, finalized.Lease)
	assert.Equal(t, eventing.PRDevelopmentControllerRecoveryRotationResult{}, finalized.Rotation)
	assert.Equal(t, testDigest("b"), finalized.Result.ReviewDigest)
	assert.True(t, finalized.Result.AlreadyParked)
}

func TestControllerRecoveryWorkerHeartbeatRenewsAndCancelsOnLoss(t *testing.T) {
	t.Parallel()

	t.Run("renews exact reservation claim", func(t *testing.T) {
		candidate, controller, intent := reservationRecoveryFixture()
		renewed := make(chan eventing.PRDevelopmentControllerRecoveryRenew, 1)
		releaseGit := make(chan struct{})
		store := reservationRecoveryStore(candidate, controller, intent)
		store.renewReservation = func(
			_ context.Context,
			input eventing.PRDevelopmentControllerRecoveryRenew,
		) error {
			select {
			case renewed <- input:
			default:
			}
			return nil
		}
		git := &fakeControllerRecoveryGit{
			rotate: func(
				ctx context.Context,
				_ gitworkspace.PinnedReservationRotationRequest,
			) (gitworkspace.PinnedReservationRotationResult, error) {
				select {
				case <-releaseGit:
				case <-ctx.Done():
					return gitworkspace.PinnedReservationRotationResult{}, ctx.Err()
				}
				return reservationRotationResult(intent), nil
			},
		}
		worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
			return git, nil
		})
		worker.heartbeatInterval = func(time.Duration) time.Duration { return 5 * time.Millisecond }
		done := make(chan error, 1)
		go func() {
			_, err := worker.ProcessOne(context.Background())
			done <- err
		}()

		select {
		case input := <-renewed:
			assert.Equal(t, intent.ID, input.RecoveryID)
			assert.Equal(t, "reservation-claim-token", input.ClaimToken)
			assert.Equal(t, worker.lease, input.Lease)
		case <-time.After(time.Second):
			t.Fatal("recovery heartbeat did not renew")
		}
		close(releaseGit)
		require.NoError(t, <-done)
	})

	t.Run("renewal loss cancels Git and leaves claim reclaimable", func(t *testing.T) {
		candidate, controller, intent := reservationRecoveryFixture()
		renewErr := errors.New("claim lost")
		store := reservationRecoveryStore(candidate, controller, intent)
		store.renewReservation = func(context.Context, eventing.PRDevelopmentControllerRecoveryRenew) error {
			return renewErr
		}
		finalized := false
		store.finishReservation = func(
			context.Context,
			eventing.PRDevelopmentControllerRecoveryFinalize,
		) (eventing.PRDevelopmentController, bool, error) {
			finalized = true
			return eventing.PRDevelopmentController{}, false, nil
		}
		git := &fakeControllerRecoveryGit{
			rotate: func(
				ctx context.Context,
				_ gitworkspace.PinnedReservationRotationRequest,
			) (gitworkspace.PinnedReservationRotationResult, error) {
				<-ctx.Done()
				return gitworkspace.PinnedReservationRotationResult{}, ctx.Err()
			},
		}
		worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
			return git, nil
		})
		worker.heartbeatInterval = func(time.Duration) time.Duration { return 5 * time.Millisecond }

		processed, err := worker.ProcessOne(context.Background())
		require.True(t, processed)
		require.ErrorIs(t, err, renewErr)
		assert.False(t, finalized)
	})

	t.Run("terminal barrier drains in-flight renewal loss before finalization", func(t *testing.T) {
		candidate, controller, intent := reservationRecoveryFixture()
		renewErr := errors.New("in-flight claim loss")
		renewStarted := make(chan struct{})
		releaseRenew := make(chan struct{})
		gitReturned := make(chan struct{})
		finalizeCalled := make(chan struct{}, 1)
		var startOnce sync.Once
		store := reservationRecoveryStore(candidate, controller, intent)
		store.renewReservation = func(context.Context, eventing.PRDevelopmentControllerRecoveryRenew) error {
			startOnce.Do(func() { close(renewStarted) })
			<-releaseRenew
			return renewErr
		}
		store.finishReservation = func(
			context.Context,
			eventing.PRDevelopmentControllerRecoveryFinalize,
		) (eventing.PRDevelopmentController, bool, error) {
			finalizeCalled <- struct{}{}
			return suspensionPendingController(controller), true, nil
		}
		git := &fakeControllerRecoveryGit{
			rotate: func(
				context.Context,
				gitworkspace.PinnedReservationRotationRequest,
			) (gitworkspace.PinnedReservationRotationResult, error) {
				<-renewStarted
				close(gitReturned)
				return reservationRotationResult(intent), nil
			},
		}
		worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
			return git, nil
		})
		worker.heartbeatInterval = func(time.Duration) time.Duration { return 5 * time.Millisecond }
		done := make(chan error, 1)
		go func() {
			_, err := worker.ProcessOne(context.Background())
			done <- err
		}()

		select {
		case <-gitReturned:
		case <-time.After(time.Second):
			t.Fatal("Git recovery did not return")
		}
		select {
		case <-finalizeCalled:
			t.Fatal("finalization crossed an in-flight renewal")
		default:
		}
		close(releaseRenew)
		require.ErrorIs(t, <-done, renewErr)
		select {
		case <-finalizeCalled:
			t.Fatal("finalization ran after known renewal loss")
		default:
		}
	})
}

func TestControllerRecoveryWorkerRejectsChangedClaimBeforeGit(t *testing.T) {
	t.Parallel()

	candidate, controller, intent := reservationRecoveryFixture()
	factoryCalled := false
	store := &fakeControllerRecoveryStore{
		stage: func(context.Context, int) (int, error) { return 0, nil },
		next: func(context.Context) (eventing.PRDevelopmentControllerRecoveryCandidate, bool, error) {
			return candidate, true, nil
		},
		claimReservation: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerRecoveryClaim,
		) (eventing.PRDevelopmentControllerRecoveryLease, bool, error) {
			intent = claimedReservationRecovery(intent, input)
			controller.ID = "different-controller"
			return eventing.PRDevelopmentControllerRecoveryLease{
				Controller: controller,
				Intent:     intent,
			}, true, nil
		},
	}
	worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
		factoryCalled = true
		return &fakeControllerRecoveryGit{}, nil
	})

	processed, err := worker.ProcessOne(context.Background())
	require.True(t, processed)
	require.Error(t, err)
	assert.False(t, factoryCalled)
}

func TestControllerRecoveryWorkerRejectsLegacyUnboundReservationClaim(t *testing.T) {
	t.Parallel()

	candidate, controller, intent := reservationRecoveryFixture()
	intent.Mode = eventing.PRDevelopmentControllerRecoveryUnbound
	factoryCalled := false
	store := reservationRecoveryStore(candidate, controller, intent)
	worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
		factoryCalled = true
		return &fakeControllerRecoveryGit{}, nil
	})

	processed, err := worker.ProcessOne(context.Background())
	require.True(t, processed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "changed authority")
	assert.False(t, factoryCalled)
}

func TestControllerRecoveryWorkerUnavailableManagerLeavesClaimReclaimable(t *testing.T) {
	t.Parallel()

	candidate, controller, intent := reservationRecoveryFixture()
	factoryErr := errors.New("generation manager unavailable")
	finalized := false
	store := reservationRecoveryStore(candidate, controller, intent)
	store.finishReservation = func(
		context.Context,
		eventing.PRDevelopmentControllerRecoveryFinalize,
	) (eventing.PRDevelopmentController, bool, error) {
		finalized = true
		return eventing.PRDevelopmentController{}, false, nil
	}
	worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
		return nil, factoryErr
	})

	processed, err := worker.ProcessOne(context.Background())
	require.True(t, processed)
	require.ErrorIs(t, err, factoryErr)
	assert.False(t, finalized)
}

func TestControllerRecoveryWorkerConstructionAndDeterministicClaimIdentity(t *testing.T) {
	t.Parallel()

	_, err := NewControllerRecoveryWorker(ControllerRecoveryWorkerConfig{})
	require.ErrorIs(t, err, ErrUnavailable)
	_, err = newControllerRecoveryWorkerWithDependencies(controllerRecoveryWorkerDependencies{})
	require.ErrorIs(t, err, ErrUnavailable)

	candidate, _, _ := reservationRecoveryFixture()
	first := controllerRecoveryClaimID("worker-one", candidate)
	assert.Equal(t, first, controllerRecoveryClaimID("worker-one", candidate))
	assert.NotEqual(t, first, controllerRecoveryClaimID("worker-two", candidate))
	candidate.ExpectedRevision++
	assert.NotEqual(t, first, controllerRecoveryClaimID("worker-one", candidate))

	suspensionCandidate, _, _ := controllerSuspensionFixture(
		eventing.PRDevelopmentControllerSuspensionCandidate,
	)
	suspensionClaim := controllerSuspensionClaimID("worker-one", suspensionCandidate)
	assert.Equal(
		t,
		suspensionClaim,
		controllerSuspensionClaimID("worker-one", suspensionCandidate),
	)
	assert.NotEqual(t, first, suspensionClaim)
	assert.True(t, strings.HasPrefix(suspensionClaim, controllerSuspensionClaimIDPrefix))
	suspensionCandidate.Mode = eventing.PRDevelopmentControllerSuspensionCommitRecovery
	assert.NotEqual(
		t,
		suspensionClaim,
		controllerSuspensionClaimID("worker-one", suspensionCandidate),
	)
}

func mustControllerRecoveryWorker(
	t *testing.T,
	store controllerRecoveryStore,
	workspaces func() (controllerRecoveryGit, error),
) *ControllerRecoveryWorker {
	t.Helper()
	worker, err := newControllerRecoveryWorkerWithDependencies(
		controllerRecoveryWorkerDependencies{
			store:       store,
			workspaces:  workspaces,
			workerLabel: "controller-recovery-test",
			lease:       MinimumRepairControllerLease,
			heartbeatInterval: func(time.Duration) time.Duration {
				return time.Hour
			},
		},
	)
	require.NoError(t, err)
	return worker
}

func reservationRecoveryFixture() (
	eventing.PRDevelopmentControllerRecoveryCandidate,
	eventing.PRDevelopmentController,
	eventing.PRDevelopmentControllerRecoveryIntent,
) {
	controller := eventing.PRDevelopmentController{
		ID:                     "controller-one",
		CurrentAttemptID:       "attempt-one",
		Revision:               7,
		Phase:                  eventing.PRDevelopmentControllerRecoveryRequired,
		AgentID:                "agent-one",
		LineID:                 "line-one",
		WorkspaceID:            "workspace-one",
		MutationReservationKey: "old-reservation",
		LeaseEpoch:             3,
	}
	intent := eventing.PRDevelopmentControllerRecoveryIntent{
		ID:                        "recovery-one",
		ControllerID:              controller.ID,
		AttemptID:                 controller.CurrentAttemptID,
		RecoveryRevision:          controller.Revision,
		Mode:                      eventing.PRDevelopmentControllerRecoveryBound,
		Status:                    eventing.PRDevelopmentControllerRecoveryPending,
		AgentID:                   controller.AgentID,
		WorkspaceID:               controller.WorkspaceID,
		LineID:                    controller.LineID,
		SourceCloneURL:            "https://example.test/owner/repo.git",
		SourceRef:                 "refs/pull/1/head",
		SourceCommit:              testObjectID("1"),
		SourceTree:                testObjectID("2"),
		LineVersion:               2,
		MutationEpoch:             3,
		TipCommit:                 testObjectID("3"),
		Tree:                      testObjectID("4"),
		PreviousReservationKey:    controller.MutationReservationKey,
		ReplacementReservationKey: "fresh-reservation",
	}
	candidate := eventing.PRDevelopmentControllerRecoveryCandidate{
		Kind:             eventing.PRDevelopmentControllerRecoveryWorkReservation,
		CaseID:           "case-one",
		ControllerID:     controller.ID,
		AttemptID:        controller.CurrentAttemptID,
		RecoveryID:       intent.ID,
		ExpectedRevision: controller.Revision,
		AvailableAt:      time.Now().UTC(),
	}
	return candidate, controller, intent
}

func claimedReservationRecovery(
	intent eventing.PRDevelopmentControllerRecoveryIntent,
	input eventing.PRDevelopmentControllerRecoveryClaim,
) eventing.PRDevelopmentControllerRecoveryIntent {
	deadline := time.Now().Add(time.Hour)
	intent.Status = eventing.PRDevelopmentControllerRecoveryClaimed
	intent.ClaimID = input.ClaimID
	intent.ClaimOwner = input.WorkerLabel
	intent.ClaimToken = "reservation-claim-token"
	intent.ClaimEpoch = 1
	intent.ClaimUntil = &deadline
	return intent
}

func reservationRecoveryStore(
	candidate eventing.PRDevelopmentControllerRecoveryCandidate,
	controller eventing.PRDevelopmentController,
	intent eventing.PRDevelopmentControllerRecoveryIntent,
) *fakeControllerRecoveryStore {
	store := &fakeControllerRecoveryStore{
		stage: func(context.Context, int) (int, error) { return 0, nil },
		next: func(context.Context) (eventing.PRDevelopmentControllerRecoveryCandidate, bool, error) {
			return candidate, true, nil
		},
	}
	store.claimReservation = func(
		_ context.Context,
		input eventing.PRDevelopmentControllerRecoveryClaim,
	) (eventing.PRDevelopmentControllerRecoveryLease, bool, error) {
		return eventing.PRDevelopmentControllerRecoveryLease{
			Controller: controller,
			Intent:     claimedReservationRecovery(intent, input),
		}, true, nil
	}
	store.finishReservation = func(
		context.Context,
		eventing.PRDevelopmentControllerRecoveryFinalize,
	) (eventing.PRDevelopmentController, bool, error) {
		return suspensionPendingController(controller), true, nil
	}
	return store
}

func reservationRotationResult(
	intent eventing.PRDevelopmentControllerRecoveryIntent,
) gitworkspace.PinnedReservationRotationResult {
	return gitworkspace.PinnedReservationRotationResult{
		WorkspaceID:   intent.WorkspaceID,
		Bound:         true,
		Version:       intent.LineVersion,
		MutationEpoch: intent.MutationEpoch,
		Tip:           intent.TipCommit,
		Tree:          intent.Tree,
		RotationHash:  testDigest("c"),
	}
}

func operationRecoveryFixture(
	kind eventing.PRDevelopmentControllerOperationKind,
) (
	eventing.PRDevelopmentControllerRecoveryCandidate,
	eventing.PRDevelopmentController,
	eventing.PRDevelopmentControllerOperation,
) {
	controller := eventing.PRDevelopmentController{
		ID:                     "controller-operation",
		CurrentAttemptID:       "attempt-operation",
		Revision:               9,
		Phase:                  eventing.PRDevelopmentControllerRecoveryRequired,
		AgentID:                "agent-operation",
		LineID:                 "line-operation",
		WorkspaceID:            "workspace-operation",
		MutationReservationKey: "operation-old-reservation",
		LeaseEpoch:             4,
	}
	operation := eventing.PRDevelopmentControllerOperation{
		ID:                         "operation-one",
		ControllerID:               controller.ID,
		AttemptID:                  controller.CurrentAttemptID,
		Kind:                       kind,
		Status:                     eventing.PRDevelopmentControllerOperationRecoveryPending,
		PreparedControllerRevision: controller.Revision - 1,
		AgentID:                    controller.AgentID,
		WorkspaceID:                controller.WorkspaceID,
		LineID:                     controller.LineID,
		SourceCloneURL:             "https://example.test/owner/repo.git",
		SourceRef:                  "refs/pull/2/head",
		SourceCommit:               testObjectID("1"),
		SourceTree:                 testObjectID("2"),
		LineVersion:                2,
		MutationEpoch:              3,
		TipCommit:                  testObjectID("3"),
		Tree:                       testObjectID("4"),
		RecoveryID:                 "operation-recovery-one",
		ReplacementReservationKey:  "operation-fresh-reservation",
		RecoveryRevision:           controller.Revision,
	}
	operation.Request = eventing.PRDevelopmentControllerOperationRequest{
		Repository:   "owner/repo",
		SourceRef:    operation.SourceRef,
		SourceCommit: operation.SourceCommit,
		AgentID:      operation.AgentID,
		WorkspaceID:  operation.WorkspaceID,
		LineID:       operation.LineID,
	}
	switch kind {
	case eventing.PRDevelopmentControllerOperationAdopt:
		operation.LineVersion = 0
		operation.MutationEpoch = 0
		operation.TipCommit = operation.SourceCommit
		operation.Tree = operation.SourceTree
		operation.Request.ExpectedTree = operation.SourceTree
	case eventing.PRDevelopmentControllerOperationResume:
		operation.MutationEpoch = operation.LineVersion
		operation.Request.ExpectedVersion = operation.LineVersion
		operation.Request.ExpectedEpoch = operation.MutationEpoch
		operation.Request.ExpectedTip = operation.TipCommit
		operation.Request.ExpectedTree = operation.Tree
	case eventing.PRDevelopmentControllerOperationCommit:
		operation.Request.EffectIntentID = "commit-intent-one"
		operation.Request.ExpectedParent = operation.TipCommit
		operation.Request.ExpectedTree = testObjectID("5")
		operation.Request.CandidateDigest = testDigest("d")
		operation.Request.CommitMessage = "Apply local repair"
		operation.Request.AuthoredAt = time.Unix(1_700_000_000, 0).UTC()
	case eventing.PRDevelopmentControllerOperationPark:
		operation.RecoveryID = ""
		operation.ReplacementReservationKey = ""
		operation.Request.EffectIntentID = "park-intent-one"
		operation.Request.ExpectedVersion = operation.LineVersion
		operation.Request.MutationEpoch = operation.MutationEpoch
		operation.Request.PreviousTip = operation.TipCommit
		operation.Request.Tip = testObjectID("6")
		operation.Request.Tree = testObjectID("7")
		operation.Request.CompletionSummary = "Done"
		operation.Request.CompletionIterations = 1
	}
	candidate := eventing.PRDevelopmentControllerRecoveryCandidate{
		Kind:             eventing.PRDevelopmentControllerRecoveryWorkOperation,
		CaseID:           "case-operation",
		ControllerID:     controller.ID,
		AttemptID:        controller.CurrentAttemptID,
		RecoveryID:       operation.RecoveryID,
		OperationID:      operation.ID,
		ExpectedRevision: controller.Revision,
		AvailableAt:      time.Now().UTC(),
	}
	return candidate, controller, operation
}

func operationRecoveryStore(
	candidate eventing.PRDevelopmentControllerRecoveryCandidate,
	controller eventing.PRDevelopmentController,
	operation eventing.PRDevelopmentControllerOperation,
	finish func(eventing.PRDevelopmentControllerOperationRecoveryFinalize) (
		eventing.PRDevelopmentControllerOperationTransition,
		error,
	),
) *fakeControllerRecoveryStore {
	store := &fakeControllerRecoveryStore{
		stage: func(context.Context, int) (int, error) { return 0, nil },
		next: func(context.Context) (eventing.PRDevelopmentControllerRecoveryCandidate, bool, error) {
			return candidate, true, nil
		},
	}
	store.claimOperation = func(
		_ context.Context,
		input eventing.PRDevelopmentControllerOperationRecoveryClaim,
	) (eventing.PRDevelopmentControllerOperationRecoveryLease, bool, error) {
		deadline := time.Now().Add(time.Hour)
		claimed := operation
		claimed.Status = eventing.PRDevelopmentControllerOperationRecoveryClaimed
		claimed.ClaimID = input.ClaimID
		claimed.ClaimOwner = input.WorkerLabel
		claimed.ClaimToken = "operation-claim-token"
		claimed.ClaimEpoch = 1
		claimed.ClaimUntil = &deadline
		return eventing.PRDevelopmentControllerOperationRecoveryLease{
			Controller: controller,
			Operation:  claimed,
		}, true, nil
	}
	store.finishOperation = func(
		_ context.Context,
		input eventing.PRDevelopmentControllerOperationRecoveryFinalize,
	) (eventing.PRDevelopmentControllerOperationTransition, bool, error) {
		transition, err := finish(input)
		return transition, err == nil, err
	}
	return store
}

func suspensionPendingController(
	controller eventing.PRDevelopmentController,
) eventing.PRDevelopmentController {
	controller.Phase = eventing.PRDevelopmentControllerSuspensionPending
	controller.LeaseKind = ""
	controller.LeaseOwner = ""
	controller.LeaseToken = ""
	controller.LeaseUntil = nil
	controller.MutationReservationKey = ""
	controller.Revision++
	return controller
}

func suspendedOperationTransition(
	controller eventing.PRDevelopmentController,
	operation eventing.PRDevelopmentControllerOperation,
) eventing.PRDevelopmentControllerOperationTransition {
	operation.Status = eventing.PRDevelopmentControllerOperationFinalized
	return eventing.PRDevelopmentControllerOperationTransition{
		Controller: suspensionPendingController(controller),
		Operation:  operation,
	}
}

func parkedOperationTransition(
	controller eventing.PRDevelopmentController,
	operation eventing.PRDevelopmentControllerOperation,
	result eventing.PRDevelopmentControllerOperationResult,
) eventing.PRDevelopmentControllerOperationTransition {
	operation.Status = eventing.PRDevelopmentControllerOperationFinalized
	controller.Phase = eventing.PRDevelopmentControllerReviewPending
	controller.LeaseKind = ""
	controller.LeaseOwner = ""
	controller.LeaseToken = ""
	controller.LeaseUntil = nil
	controller.MutationReservationKey = ""
	return eventing.PRDevelopmentControllerOperationTransition{
		Controller: controller,
		Operation:  operation,
		Fence: &eventing.PRDevelopmentAttemptReviewFence{
			AttemptID:        operation.AttemptID,
			ControllerID:     operation.ControllerID,
			LineID:           operation.LineID,
			LineVersion:      result.ReviewVersion,
			MutationEpoch:    result.ReviewMutationEpoch,
			ParkIntentID:     result.ReviewParkIntentID,
			BaseCommit:       result.ReviewBaseCommit,
			TipCommit:        result.ReviewCommit,
			Tree:             result.ReviewTree,
			LineReviewDigest: result.ReviewDigest,
		},
	}
}

func testObjectID(character string) string {
	return strings.Repeat(character, 40)
}

func testDigest(character string) string {
	return strings.Repeat(character, 64)
}

func controllerSuspensionFixture(
	mode eventing.PRDevelopmentControllerSuspensionMode,
) (
	eventing.PRDevelopmentControllerSuspensionWorkCandidate,
	eventing.PRDevelopmentController,
	eventing.PRDevelopmentControllerSuspension,
) {
	controller := eventing.PRDevelopmentController{
		ID:               "controller-suspension",
		ThreadID:         "thread-suspension",
		OwnerSessionID:   "session-suspension",
		AgentID:          "agent-suspension",
		Revision:         11,
		Phase:            eventing.PRDevelopmentControllerSuspensionPending,
		LineID:           "line-suspension",
		WorkspaceID:      "workspace-suspension",
		SourceCloneURL:   "https://example.test/owner/repo.git",
		SourceRef:        "refs/pull/3/head",
		SourceCommit:     testObjectID("1"),
		SourceTree:       testObjectID("2"),
		LineVersion:      2,
		MutationEpoch:    3,
		TipCommit:        testObjectID("3"),
		Tree:             testObjectID("4"),
		CurrentAttemptID: "attempt-suspension",
	}
	suspension := eventing.PRDevelopmentControllerSuspension{
		ID:                          "pdsi_suspension",
		ControllerID:                controller.ID,
		ThreadID:                    controller.ThreadID,
		OwnerSessionID:              controller.OwnerSessionID,
		AttemptID:                   controller.CurrentAttemptID,
		SourceKind:                  eventing.PRDevelopmentControllerSuspensionSourceControllerRecovery,
		SourceRecoveryID:            "pdri_recovery",
		SourceFinalRevision:         controller.Revision - 1,
		SourceFinalHash:             testDigest("1"),
		Mode:                        mode,
		Status:                      eventing.PRDevelopmentControllerSuspensionStatusSuspendPending,
		AgentID:                     controller.AgentID,
		WorkspaceID:                 controller.WorkspaceID,
		LineID:                      controller.LineID,
		SourceCloneURL:              controller.SourceCloneURL,
		SourceRef:                   controller.SourceRef,
		SourceCommit:                controller.SourceCommit,
		SourceTree:                  controller.SourceTree,
		LineVersion:                 controller.LineVersion,
		MutationEpoch:               controller.MutationEpoch,
		TipCommit:                   controller.TipCommit,
		Tree:                        controller.Tree,
		SuspensionReservationKey:    "suspension-fresh-reservation-secret",
		SuspensionReservationDigest: testDigest("2"),
		MutationLeaseEpoch:          4,
		MutationLeaseTokenDigest:    testDigest("3"),
		SuspendIntentID:             "pdsi_suspension",
		SuspendRequestJSON:          []byte(`{"version":1,"mode":"suspend"}`),
		SuspendRequestHash:          testDigest("4"),
		PreviousHash:                testDigest("5"),
		IntentHash:                  testDigest("6"),
	}
	suspension.SuspendRequest = eventing.PRDevelopmentControllerSuspensionRequest{
		Repository:            suspension.SourceCloneURL,
		SourceRef:             suspension.SourceRef,
		SourceCommit:          suspension.SourceCommit,
		ReservationKey:        suspension.SuspensionReservationKey,
		AgentID:               suspension.AgentID,
		WorkspaceID:           suspension.WorkspaceID,
		LineID:                suspension.LineID,
		IntentID:              suspension.ID,
		ExpectedVersion:       suspension.LineVersion,
		ExpectedMutationEpoch: suspension.MutationEpoch,
		ExpectedTip:           suspension.TipCommit,
		ExpectedTree:          suspension.Tree,
	}
	if mode == eventing.PRDevelopmentControllerSuspensionCommitRecovery {
		suspension.SourceKind = eventing.PRDevelopmentControllerSuspensionSourceOperationRecovery
		suspension.SourceOperationID = "pdoi_operation"
		suspension.SourceOperationKind = eventing.PRDevelopmentControllerOperationCommit
		suspension.SuspendRequest.CommitIntentID = "pdci_commit"
		suspension.SuspendRequest.CommitExpectedParent = suspension.TipCommit
		suspension.SuspendRequest.CommitExpectedTree = testObjectID("7")
		suspension.SuspendRequest.CommitCandidateDigest = testDigest("7")
		suspension.SuspendRequest.CommitMessage = "Apply recovered repair"
		suspension.SuspendRequest.CommitAuthoredAt = time.Unix(1_700_000_100, 0).UTC()
	}
	candidate := eventing.PRDevelopmentControllerSuspensionWorkCandidate{
		CaseID:           "case-suspension",
		SuspensionID:     suspension.ID,
		ControllerID:     suspension.ControllerID,
		AttemptID:        suspension.AttemptID,
		SourceKind:       suspension.SourceKind,
		Mode:             suspension.Mode,
		ExpectedRevision: controller.Revision,
		AvailableAt:      time.Now().UTC(),
	}
	return candidate, controller, suspension
}

func claimedControllerSuspension(
	suspension eventing.PRDevelopmentControllerSuspension,
	input eventing.PRDevelopmentControllerSuspensionClaim,
) eventing.PRDevelopmentControllerSuspension {
	deadline := time.Now().Add(time.Hour)
	suspension.Status = eventing.PRDevelopmentControllerSuspensionStatusSuspendClaimed
	suspension.SuspendClaimID = input.ClaimID
	suspension.SuspendClaimOwner = input.WorkerLabel
	suspension.SuspendClaimToken = "suspension-claim-token-secret"
	suspension.SuspendClaimUntil = &deadline
	suspension.SuspendClaimEpoch = 1
	suspension.SuspendClaims = 1
	claimedAt := time.Now().UTC()
	suspension.SuspendClaimedAt = &claimedAt
	return suspension
}

func controllerSuspensionGitResult(
	suspension eventing.PRDevelopmentControllerSuspension,
	replayed bool,
) gitworkspace.PinnedLineSuspendResult {
	result := gitworkspace.PinnedLineSuspendResult{
		WorkspaceID:      suspension.WorkspaceID,
		Version:          suspension.LineVersion,
		MutationEpoch:    suspension.MutationEpoch,
		Tip:              suspension.TipCommit,
		Tree:             suspension.Tree,
		CandidateTree:    testObjectID("5"),
		CandidateDigest:  testDigest("8"),
		ChangedFileCount: 2,
		SuspensionHash:   testDigest("9"),
		AlreadySuspended: replayed,
	}
	if suspension.Mode == eventing.PRDevelopmentControllerSuspensionCommitRecovery {
		result.CandidateTree = suspension.SuspendRequest.CommitExpectedTree
		result.CandidateDigest = suspension.SuspendRequest.CommitCandidateDigest
		result.PreparedCommit = testObjectID("8")
		result.PreparedTree = suspension.SuspendRequest.CommitExpectedTree
	}
	return result
}

func suspendedControllerTransition(
	controller eventing.PRDevelopmentController,
	claimed eventing.PRDevelopmentControllerSuspension,
	result eventing.PRDevelopmentControllerSuspensionResult,
) eventing.PRDevelopmentControllerSuspensionTransition {
	now := time.Now().UTC()
	controller.Phase = eventing.PRDevelopmentControllerSuspended
	controller.Revision = claimed.SourceFinalRevision + 2
	controller.LeaseKind = ""
	controller.LeaseOwner = ""
	controller.LeaseToken = ""
	controller.LeaseUntil = nil
	controller.MutationReservationKey = ""
	controller.UpdatedAt = now
	claimed.Status = eventing.PRDevelopmentControllerSuspensionStatusSuspended
	claimed.SuspensionReservationKey = ""
	claimed.SuspendRequest.ReservationKey = ""
	claimed.SuspendClaimToken = ""
	claimed.SuspendClaimUntil = nil
	claimed.SuspendClaimTokenDigest = testDigest("a")
	result.AlreadySuspended = false
	claimed.SuspendResult = result
	claimed.SuspendResultJSON = []byte(`{"version":1,"result":"suspended"}`)
	claimed.SuspendResultHash = testDigest("b")
	claimed.FinalSuspensionRevision = controller.Revision
	claimed.SuspensionFinalHash = testDigest("c")
	claimed.SuspendedAt = &now
	claimed.UpdatedAt = now
	return eventing.PRDevelopmentControllerSuspensionTransition{
		Controller: controller,
		Suspension: claimed,
	}
}
