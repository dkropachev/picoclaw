package prdevelopment

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

type fakeControllerRecoveryStore struct {
	stage                func(context.Context, int) (int, error)
	next                 func(context.Context) (eventing.PRDevelopmentControllerRecoveryCandidate, bool, error)
	nextSuspension       func(context.Context) (eventing.PRDevelopmentControllerSuspensionWorkCandidate, bool, error)
	claimSuspension      func(context.Context, eventing.PRDevelopmentControllerSuspensionClaim) (eventing.PRDevelopmentControllerSuspensionLease, bool, error)
	renewSuspension      func(context.Context, eventing.PRDevelopmentControllerSuspensionRenew) error
	finishSuspension     func(context.Context, eventing.PRDevelopmentControllerSuspensionFinalize) (eventing.PRDevelopmentControllerSuspensionTransition, bool, error)
	nextResumeRecovery   func(context.Context) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate, bool, error)
	claimResumeRecovery  func(context.Context, eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease, bool, error)
	renewResumeRecovery  func(context.Context, eventing.PRDevelopmentControllerSuspendedResumeRecoveryRenew) error
	finishResumeRecovery func(context.Context, eventing.PRDevelopmentControllerSuspendedResumeRecoveryFinalize) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition, bool, error)
	claimReservation     func(context.Context, eventing.PRDevelopmentControllerRecoveryClaim) (eventing.PRDevelopmentControllerRecoveryLease, bool, error)
	renewReservation     func(context.Context, eventing.PRDevelopmentControllerRecoveryRenew) error
	finishReservation    func(context.Context, eventing.PRDevelopmentControllerRecoveryFinalize) (eventing.PRDevelopmentController, bool, error)
	claimOperation       func(context.Context, eventing.PRDevelopmentControllerOperationRecoveryClaim) (eventing.PRDevelopmentControllerOperationRecoveryLease, bool, error)
	renewOperation       func(context.Context, eventing.PRDevelopmentControllerOperationRecoveryRenew) error
	finishOperation      func(context.Context, eventing.PRDevelopmentControllerOperationRecoveryFinalize) (eventing.PRDevelopmentControllerOperationTransition, bool, error)
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

func (store *fakeControllerRecoveryStore) NextPRDevelopmentControllerSuspendedResumeRecovery(
	ctx context.Context,
) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate, bool, error) {
	if store.nextResumeRecovery == nil {
		return eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate{}, false, nil
	}
	return store.nextResumeRecovery(ctx)
}

func (store *fakeControllerRecoveryStore) ClaimPRDevelopmentControllerSuspendedResumeRecovery(
	ctx context.Context,
	input eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim,
) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease, bool, error) {
	if store.claimResumeRecovery == nil {
		return eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease{}, false,
			errors.New("unexpected suspended resume recovery claim")
	}
	return store.claimResumeRecovery(ctx, input)
}

func (store *fakeControllerRecoveryStore) RenewPRDevelopmentControllerSuspendedResumeRecovery(
	ctx context.Context,
	input eventing.PRDevelopmentControllerSuspendedResumeRecoveryRenew,
) error {
	if store.renewResumeRecovery == nil {
		return nil
	}
	return store.renewResumeRecovery(ctx, input)
}

func (store *fakeControllerRecoveryStore) FinalizePRDevelopmentControllerSuspendedResumeRecovery(
	ctx context.Context,
	input eventing.PRDevelopmentControllerSuspendedResumeRecoveryFinalize,
) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition, bool, error) {
	if store.finishResumeRecovery == nil {
		return eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition{}, false,
			errors.New("unexpected suspended resume recovery finalization")
	}
	return store.finishResumeRecovery(ctx, input)
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
	resumeSuspended func(context.Context, gitworkspace.PinnedLineSuspendedResumeRequest) (gitworkspace.PinnedLineSuspendedResumeResult, error)
	suspend         func(context.Context, gitworkspace.PinnedLineSuspendRequest) (gitworkspace.PinnedLineSuspendResult, error)
	suspendCommit   func(context.Context, gitworkspace.PinnedLineCommitSuspensionRequest) (gitworkspace.PinnedLineSuspendResult, error)
	rotate          func(context.Context, gitworkspace.PinnedReservationRotationRequest) (gitworkspace.PinnedReservationRotationResult, error)
	adopt           func(context.Context, gitworkspace.PinnedLineAdoptRecoveryRequest) (gitworkspace.PinnedLineReservationRecoveryResult, error)
	resume          func(context.Context, gitworkspace.PinnedLineResumeRecoveryRequest) (gitworkspace.PinnedLineReservationRecoveryResult, error)
	commit          func(context.Context, gitworkspace.PinnedCommitRequest) (gitworkspace.PinnedCommitResult, error)
	park            func(context.Context, gitworkspace.PinnedLineParkRequest) (gitworkspace.PinnedLineParkResult, error)
	snapshot        func(context.Context, gitworkspace.PinnedLineReviewRequest) (gitworkspace.PinnedLineReviewSnapshot, error)
}

func (git *fakeControllerRecoveryGit) ResumeSuspendedPinnedLine(
	ctx context.Context,
	request gitworkspace.PinnedLineSuspendedResumeRequest,
) (gitworkspace.PinnedLineSuspendedResumeResult, error) {
	if git.resumeSuspended == nil {
		return gitworkspace.PinnedLineSuspendedResumeResult{},
			errors.New("unexpected suspended resume recovery")
	}
	return git.resumeSuspended(ctx, request)
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
		t.Parallel()

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
		t.Parallel()

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

func TestControllerRecoveryWorkerSuspendedResumeRecoveryPriority(t *testing.T) {
	t.Parallel()

	candidate, _, _ := suspendedResumeRecoveryFixture()
	t.Run("expired resume precedes recovery staging", func(t *testing.T) {
		t.Parallel()
		claimErr := errors.New("stop after expired resume priority claim")
		order := make([]string, 0, 3)
		store := &fakeControllerRecoveryStore{
			nextSuspension: func(context.Context) (
				eventing.PRDevelopmentControllerSuspensionWorkCandidate,
				bool,
				error,
			) {
				order = append(order, "suspension")
				return eventing.PRDevelopmentControllerSuspensionWorkCandidate{}, false, nil
			},
			nextResumeRecovery: func(context.Context) (
				eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate,
				bool,
				error,
			) {
				order = append(order, "resume recovery")
				return candidate, true, nil
			},
			claimResumeRecovery: func(
				context.Context,
				eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim,
			) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease, bool, error) {
				order = append(order, "claim resume recovery")
				return eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease{}, false,
					claimErr
			},
			stage: func(context.Context, int) (int, error) {
				order = append(order, "stage")
				return 0, nil
			},
		}
		worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
			return &fakeControllerRecoveryGit{}, nil
		})

		processed, err := worker.ProcessOne(context.Background())
		require.False(t, processed)
		require.ErrorIs(t, err, claimErr)
		assert.Equal(t, []string{
			"suspension",
			"resume recovery",
			"claim resume recovery",
		}, order)
	})

	t.Run("empty scans preserve the complete order", func(t *testing.T) {
		t.Parallel()
		order := make([]string, 0, 5)
		store := &fakeControllerRecoveryStore{
			nextSuspension: func(context.Context) (
				eventing.PRDevelopmentControllerSuspensionWorkCandidate,
				bool,
				error,
			) {
				order = append(order, "suspension")
				return eventing.PRDevelopmentControllerSuspensionWorkCandidate{}, false, nil
			},
			nextResumeRecovery: func(context.Context) (
				eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate,
				bool,
				error,
			) {
				order = append(order, "resume recovery")
				return eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate{}, false, nil
			},
			stage: func(context.Context, int) (int, error) {
				order = append(order, "stage")
				return 0, nil
			},
			next: func(context.Context) (
				eventing.PRDevelopmentControllerRecoveryCandidate,
				bool,
				error,
			) {
				order = append(order, "recovery")
				return eventing.PRDevelopmentControllerRecoveryCandidate{}, false, nil
			},
		}
		worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
			return &fakeControllerRecoveryGit{}, nil
		})

		processed, err := worker.ProcessOne(context.Background())
		require.NoError(t, err)
		require.False(t, processed)
		assert.Equal(t, []string{
			"suspension",
			"resume recovery",
			"stage",
			"suspension",
			"recovery",
		}, order)
	})
}

func TestControllerRecoveryWorkerResumesFinalizesAndSuspendsInOneCall(t *testing.T) {
	t.Parallel()

	candidate, controller, resume := suspendedResumeRecoveryFixture()
	order := make([]string, 0, 6)
	var (
		claimInput        eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim
		resumeRenewInput  eventing.PRDevelopmentControllerSuspendedResumeRecoveryRenew
		resumeFinishInput eventing.PRDevelopmentControllerSuspendedResumeRecoveryFinalize
		childRenewInput   eventing.PRDevelopmentControllerSuspensionRenew
		childFinishInput  eventing.PRDevelopmentControllerSuspensionFinalize
		child             eventing.PRDevelopmentControllerSuspension
	)
	store := &fakeControllerRecoveryStore{
		nextResumeRecovery: func(context.Context) (
			eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate,
			bool,
			error,
		) {
			return candidate, true, nil
		},
		claimResumeRecovery: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim,
		) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease, bool, error) {
			claimInput = input
			resume = claimedSuspendedResumeRecovery(resume, input)
			return eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease{
				Controller: controller,
				Suspension: resume,
				Reclaimed:  true,
			}, true, nil
		},
		renewResumeRecovery: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspendedResumeRecoveryRenew,
		) error {
			order = append(order, "renew resume")
			resumeRenewInput = input
			return nil
		},
		finishResumeRecovery: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspendedResumeRecoveryFinalize,
		) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition, bool, error) {
			order = append(order, "finalize resume")
			resumeFinishInput = input
			transition := suspendedResumeRecoveryTransition(controller, resume, input.Result)
			renewedChildDeadline := transition.NextSuspension.SuspendClaimUntil.Add(time.Minute)
			transition.NextSuspension.SuspendClaimUntil = &renewedChildDeadline
			child = transition.NextSuspension
			return transition, true, nil
		},
		renewSuspension: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspensionRenew,
		) error {
			order = append(order, "renew child")
			childRenewInput = input
			return nil
		},
		finishSuspension: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspensionFinalize,
		) (eventing.PRDevelopmentControllerSuspensionTransition, bool, error) {
			order = append(order, "finalize child")
			childFinishInput = input
			intermediate := suspendedResumeRecoveryTransition(
				controller,
				resume,
				resumeFinishInput.Result,
			).Controller
			return suspendedControllerTransition(intermediate, child, input.Result), true, nil
		},
	}
	resumeResult := suspendedResumeRecoveryGitResult(resume, false)
	git := &fakeControllerRecoveryGit{
		resumeSuspended: func(
			_ context.Context,
			_ gitworkspace.PinnedLineSuspendedResumeRequest,
		) (gitworkspace.PinnedLineSuspendedResumeResult, error) {
			order = append(order, "resume Git")
			return resumeResult, nil
		},
		suspend: func(
			_ context.Context,
			_ gitworkspace.PinnedLineSuspendRequest,
		) (gitworkspace.PinnedLineSuspendResult, error) {
			order = append(order, "suspend Git")
			return controllerSuspensionGitResult(child, false), nil
		},
	}
	worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
		return git, nil
	})

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, []string{
		"resume Git",
		"renew resume",
		"finalize resume",
		"renew child",
		"suspend Git",
		"renew child",
		"finalize child",
	}, order)
	assert.Equal(t, candidate.CaseID, claimInput.CaseID)
	assert.Equal(
		t,
		controllerSuspendedResumeRecoveryClaimID(worker.workerLabel, candidate),
		claimInput.ClaimID,
	)
	assert.Equal(t, resume.ResumeClaimToken, resumeRenewInput.ClaimToken)
	assert.Equal(t, resume.ResumeClaimToken, resumeFinishInput.ClaimToken)
	assert.Equal(t, child.SuspendClaimToken, childRenewInput.ClaimToken)
	assert.Equal(t, child.SuspendClaimToken, childFinishInput.ClaimToken)
}

func TestControllerRecoveryWorkerRenewsTransferredChildBeforeGit(t *testing.T) {
	t.Parallel()

	candidate, controller, resume := suspendedResumeRecoveryFixture()
	var (
		child          eventing.PRDevelopmentControllerSuspension
		renewed        eventing.PRDevelopmentControllerSuspensionRenew
		childGitCalls  int
		childFinalized bool
	)
	store := &fakeControllerRecoveryStore{
		nextResumeRecovery: func(context.Context) (
			eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate,
			bool,
			error,
		) {
			return candidate, true, nil
		},
		claimResumeRecovery: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim,
		) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease, bool, error) {
			resume = claimedSuspendedResumeRecovery(resume, input)
			expired := time.Now().Add(-time.Minute).UTC()
			resume.ResumeClaimUntil = &expired
			return eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease{
				Controller: controller,
				Suspension: resume,
				Reclaimed:  true,
			}, false, nil
		},
		finishResumeRecovery: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspendedResumeRecoveryFinalize,
		) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition, bool, error) {
			transition := suspendedResumeRecoveryTransition(controller, resume, input.Result)
			child = transition.NextSuspension
			return transition, false, nil
		},
		renewSuspension: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspensionRenew,
		) error {
			renewed = input
			return eventing.ErrStaleLease
		},
		finishSuspension: func(
			context.Context,
			eventing.PRDevelopmentControllerSuspensionFinalize,
		) (eventing.PRDevelopmentControllerSuspensionTransition, bool, error) {
			childFinalized = true
			return eventing.PRDevelopmentControllerSuspensionTransition{}, false, nil
		},
	}
	git := &fakeControllerRecoveryGit{
		resumeSuspended: func(
			_ context.Context,
			_ gitworkspace.PinnedLineSuspendedResumeRequest,
		) (gitworkspace.PinnedLineSuspendedResumeResult, error) {
			return suspendedResumeRecoveryGitResult(resume, true), nil
		},
		suspend: func(
			context.Context,
			gitworkspace.PinnedLineSuspendRequest,
		) (gitworkspace.PinnedLineSuspendResult, error) {
			childGitCalls++
			return gitworkspace.PinnedLineSuspendResult{}, nil
		},
	}
	worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
		return git, nil
	})

	processed, err := worker.ProcessOne(context.Background())
	require.True(t, processed)
	require.ErrorIs(t, err, eventing.ErrStaleLease)
	assert.Zero(t, childGitCalls)
	assert.False(t, childFinalized)
	assert.Equal(t, child.ID, renewed.SuspensionID)
	assert.Equal(t, child.ControllerID, renewed.ControllerID)
	assert.Equal(t, child.AttemptID, renewed.AttemptID)
	assert.Equal(t, child.SuspendClaimID, renewed.ClaimID)
	assert.Equal(t, child.SuspendClaimToken, renewed.ClaimToken)
	assert.Equal(t, child.SuspendClaimEpoch, renewed.ClaimEpoch)
	assert.Equal(t, worker.lease, renewed.Lease)
}

func TestControllerRecoveryWorkerSuspendedResumeTerminalReplaySkipsChildGit(t *testing.T) {
	t.Parallel()

	candidate, controller, resume := suspendedResumeRecoveryFixture()
	resumeCalls := 0
	childCalls := 0
	store := &fakeControllerRecoveryStore{
		nextResumeRecovery: func(context.Context) (
			eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate,
			bool,
			error,
		) {
			return candidate, true, nil
		},
		claimResumeRecovery: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim,
		) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease, bool, error) {
			resume = claimedSuspendedResumeRecovery(resume, input)
			return eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease{
				Controller: controller,
				Suspension: resume,
				Reclaimed:  true,
			}, false, nil
		},
		finishResumeRecovery: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspendedResumeRecoveryFinalize,
		) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition, bool, error) {
			transition := suspendedResumeRecoveryTransition(controller, resume, input.Result)
			childResult := controllerSuspensionGitResult(transition.NextSuspension, true)
			mapped, err := mapControllerSuspensionResult(transition.NextSuspension, childResult)
			require.NoError(t, err)
			terminal := suspendedControllerTransition(
				transition.Controller,
				transition.NextSuspension,
				mapped,
			)
			transition.Controller = terminal.Controller
			transition.NextSuspension = terminal.Suspension
			return transition, false, nil
		},
	}
	git := &fakeControllerRecoveryGit{
		resumeSuspended: func(
			_ context.Context,
			_ gitworkspace.PinnedLineSuspendedResumeRequest,
		) (gitworkspace.PinnedLineSuspendedResumeResult, error) {
			resumeCalls++
			return suspendedResumeRecoveryGitResult(resume, true), nil
		},
		suspend: func(
			context.Context,
			gitworkspace.PinnedLineSuspendRequest,
		) (gitworkspace.PinnedLineSuspendResult, error) {
			childCalls++
			return gitworkspace.PinnedLineSuspendResult{}, errors.New("duplicate child Git")
		},
	}
	worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
		return git, nil
	})

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, 1, resumeCalls)
	assert.Zero(t, childCalls)
}

func TestControllerRecoveryWorkerSuspendedResumeTerminalReplayAcceptsOrdinaryReclaim(
	t *testing.T,
) {
	t.Parallel()

	candidate, controller, resume := suspendedResumeRecoveryFixture()
	childCalls := 0
	store := &fakeControllerRecoveryStore{
		nextResumeRecovery: func(context.Context) (
			eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate,
			bool,
			error,
		) {
			return candidate, true, nil
		},
		claimResumeRecovery: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim,
		) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease, bool, error) {
			resume = claimedSuspendedResumeRecovery(resume, input)
			return eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease{
				Controller: controller,
				Suspension: resume,
				Reclaimed:  true,
			}, false, nil
		},
		finishResumeRecovery: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspendedResumeRecoveryFinalize,
		) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition, bool, error) {
			transition := suspendedResumeRecoveryTransition(controller, resume, input.Result)
			reclaimedAt := transition.NextSuspension.CreatedAt.Add(time.Minute)
			reclaimedUntil := reclaimedAt.Add(time.Hour)
			transition.NextSuspension.SuspendClaimID = "pdsc_ordinary_reclaimed_child"
			transition.NextSuspension.SuspendClaimOwner = "ordinary-suspension-worker"
			transition.NextSuspension.SuspendClaimToken = "ordinary-reclaimed-child-token"
			transition.NextSuspension.SuspendClaimUntil = &reclaimedUntil
			transition.NextSuspension.SuspendClaimEpoch++
			transition.NextSuspension.SuspendClaims++
			transition.NextSuspension.SuspendClaimedAt = &reclaimedAt
			childResult := controllerSuspensionGitResult(transition.NextSuspension, true)
			mapped, err := mapControllerSuspensionResult(transition.NextSuspension, childResult)
			require.NoError(t, err)
			terminal := suspendedControllerTransition(
				transition.Controller,
				transition.NextSuspension,
				mapped,
			)
			transition.Controller = terminal.Controller
			transition.NextSuspension = terminal.Suspension
			return transition, false, nil
		},
	}
	git := &fakeControllerRecoveryGit{
		resumeSuspended: func(
			_ context.Context,
			_ gitworkspace.PinnedLineSuspendedResumeRequest,
		) (gitworkspace.PinnedLineSuspendedResumeResult, error) {
			return suspendedResumeRecoveryGitResult(resume, true), nil
		},
		suspend: func(
			context.Context,
			gitworkspace.PinnedLineSuspendRequest,
		) (gitworkspace.PinnedLineSuspendResult, error) {
			childCalls++
			return gitworkspace.PinnedLineSuspendResult{}, errors.New("duplicate child Git")
		},
	}
	worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
		return git, nil
	})

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	assert.Zero(t, childCalls)
}

func TestControllerRecoveryWorkerSuspendedResumeTerminalBindsCandidateEvidence(t *testing.T) {
	t.Parallel()

	_, controller, resume := suspendedResumeRecoveryFixture()
	resume = claimedSuspendedResumeRecovery(
		resume,
		eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim{
			ClaimID:     "pdsrrc_test_claim",
			WorkerLabel: "controller-recovery-test",
		},
	)
	result := suspendedResumeRecoveryGitResult(resume, true)
	transition := suspendedResumeRecoveryTransition(
		controller,
		resume,
		eventing.PRDevelopmentControllerSuspendedResumeResult{
			WorkspaceID:      result.WorkspaceID,
			Version:          result.Version,
			MutationEpoch:    result.MutationEpoch,
			Tip:              result.Tip,
			Tree:             result.Tree,
			CandidateTree:    result.CandidateTree,
			CandidateDigest:  result.CandidateDigest,
			ChangedFileCount: result.ChangedFileCount,
			SuspensionHash:   result.SuspensionHash,
			RotationHash:     result.RotationHash,
		},
	)
	childResult := controllerSuspensionGitResult(transition.NextSuspension, true)
	mapped, err := mapControllerSuspensionResult(transition.NextSuspension, childResult)
	require.NoError(t, err)
	terminal := suspendedControllerTransition(
		transition.Controller,
		transition.NextSuspension,
		mapped,
	)
	transition.Controller = terminal.Controller
	transition.NextSuspension = terminal.Suspension
	transition.NextSuspension.SuspendResult.CandidateDigest = testDigest("0")

	err = validateControllerSuspendedResumeRecoveryTerminalChild(transition, resume)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retained candidate evidence")

	transition.NextSuspension.SuspendResult.CandidateDigest = transition.Resumed.ResumeResult.CandidateDigest
	transition.NextSuspension.SuspendResult.SuspensionHash = "malformed"
	err = validateControllerSuspendedResumeRecoveryTerminalChild(transition, resume)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retained candidate evidence")
}

func TestControllerRecoveryWorkerSuspendedResumeClaimLossCannotFinalize(t *testing.T) {
	t.Parallel()

	candidate, controller, baseResume := suspendedResumeRecoveryFixture()
	claimErr := errors.New("resume recovery claim lost")
	tests := []struct {
		name          string
		claimFails    bool
		renewFails    bool
		finalizeFails bool
		wantGit       bool
	}{
		{name: "claim error", claimFails: true},
		{name: "post-Git claim loss", renewFails: true, wantGit: true},
		{name: "finalization error", finalizeFails: true, wantGit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resume := baseResume
			gitCalls := 0
			finalizeCalls := 0
			childCalls := 0
			store := &fakeControllerRecoveryStore{
				nextResumeRecovery: func(context.Context) (
					eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate,
					bool,
					error,
				) {
					return candidate, true, nil
				},
				claimResumeRecovery: func(
					_ context.Context,
					input eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim,
				) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease, bool, error) {
					if test.claimFails {
						return eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease{},
							false,
							claimErr
					}
					resume = claimedSuspendedResumeRecovery(resume, input)
					return eventing.PRDevelopmentControllerSuspendedResumeRecoveryLease{
						Controller: controller,
						Suspension: resume,
						Reclaimed:  true,
					}, true, nil
				},
				renewResumeRecovery: func(
					context.Context,
					eventing.PRDevelopmentControllerSuspendedResumeRecoveryRenew,
				) error {
					if test.renewFails {
						return claimErr
					}
					return nil
				},
				finishResumeRecovery: func(
					_ context.Context,
					input eventing.PRDevelopmentControllerSuspendedResumeRecoveryFinalize,
				) (eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition, bool, error) {
					finalizeCalls++
					if test.finalizeFails {
						return eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition{},
							false,
							claimErr
					}
					return suspendedResumeRecoveryTransition(controller, resume, input.Result),
						true,
						nil
				},
			}
			git := &fakeControllerRecoveryGit{
				resumeSuspended: func(
					_ context.Context,
					_ gitworkspace.PinnedLineSuspendedResumeRequest,
				) (gitworkspace.PinnedLineSuspendedResumeResult, error) {
					gitCalls++
					return suspendedResumeRecoveryGitResult(resume, false), nil
				},
				suspend: func(
					context.Context,
					gitworkspace.PinnedLineSuspendRequest,
				) (gitworkspace.PinnedLineSuspendResult, error) {
					childCalls++
					return gitworkspace.PinnedLineSuspendResult{}, nil
				},
			}
			worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
				return git, nil
			})

			processed, err := worker.ProcessOne(context.Background())
			require.ErrorIs(t, err, claimErr)
			assert.Equal(t, test.wantGit, gitCalls == 1)
			if test.claimFails {
				assert.False(t, processed)
			} else {
				assert.True(t, processed)
			}
			if test.renewFails {
				assert.Zero(t, finalizeCalls)
			}
			assert.Zero(t, childCalls)
		})
	}
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
		t.Parallel()

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
			t.Parallel()

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
	pendingController := suspensionPendingController(controller)
	childCandidate, child := controllerRecoverySuspensionFixture(
		pendingController,
		intent.ReplacementReservationKey,
		intent.ID,
	)
	var (
		claimInput    eventing.PRDevelopmentControllerRecoveryClaim
		rotateRequest gitworkspace.PinnedReservationRotationRequest
		finalInput    eventing.PRDevelopmentControllerRecoveryFinalize
		handoffReady  bool
		order         []string
	)
	store := &fakeControllerRecoveryStore{
		stage: func(context.Context, int) (int, error) { return 1, nil },
		nextSuspension: func(context.Context) (
			eventing.PRDevelopmentControllerSuspensionWorkCandidate,
			bool,
			error,
		) {
			if !handoffReady {
				return eventing.PRDevelopmentControllerSuspensionWorkCandidate{}, false, nil
			}
			return childCandidate, true, nil
		},
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
			order = append(order, "finalize recovery")
			finalInput = input
			handoffReady = true
			return pendingController, true, nil
		},
		claimSuspension: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspensionClaim,
		) (eventing.PRDevelopmentControllerSuspensionLease, bool, error) {
			child = claimedControllerSuspension(child, input)
			return eventing.PRDevelopmentControllerSuspensionLease{
				Controller: pendingController,
				Suspension: child,
			}, true, nil
		},
		finishSuspension: func(
			_ context.Context,
			input eventing.PRDevelopmentControllerSuspensionFinalize,
		) (eventing.PRDevelopmentControllerSuspensionTransition, bool, error) {
			order = append(order, "finalize suspension")
			return suspendedControllerTransition(pendingController, child, input.Result), true, nil
		},
	}
	git := &fakeControllerRecoveryGit{
		rotate: func(
			_ context.Context,
			request gitworkspace.PinnedReservationRotationRequest,
		) (gitworkspace.PinnedReservationRotationResult, error) {
			order = append(order, "rotate Git")
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
		suspend: func(
			_ context.Context,
			_ gitworkspace.PinnedLineSuspendRequest,
		) (gitworkspace.PinnedLineSuspendResult, error) {
			order = append(order, "suspend Git")
			return controllerSuspensionGitResult(child, false), nil
		},
	}
	worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
		return git, nil
	})

	processed, err := worker.ProcessOne(context.Background())
	require.NoError(t, err)
	require.True(t, processed)
	assert.Equal(t, []string{
		"rotate Git",
		"finalize recovery",
		"suspend Git",
		"finalize suspension",
	}, order)

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

func TestControllerRecoveryWorkerRequiresExactPostRecoverySuspensionHandoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		candidate   bool
		wrongSource bool
		wantError   string
	}{
		{
			name:      "missing handoff",
			wantError: "no executable suspension handoff",
		},
		{
			name:        "different source handoff",
			candidate:   true,
			wrongSource: true,
			wantError:   "different handoff",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate, controller, intent := reservationRecoveryFixture()
			pending := suspensionPendingController(controller)
			handoffCandidate, _ := controllerRecoverySuspensionFixture(
				pending,
				intent.ReplacementReservationKey,
				intent.ID,
			)
			if test.wrongSource {
				handoffCandidate.SourceKind = eventing.PRDevelopmentControllerSuspensionSourceOperationRecovery
			}
			scans := 0
			claimedChild := false
			store := reservationRecoveryStore(candidate, controller, intent)
			store.finishReservation = func(
				context.Context,
				eventing.PRDevelopmentControllerRecoveryFinalize,
			) (eventing.PRDevelopmentController, bool, error) {
				return pending, true, nil
			}
			store.nextSuspension = func(context.Context) (
				eventing.PRDevelopmentControllerSuspensionWorkCandidate,
				bool,
				error,
			) {
				scans++
				if scans < 3 || !test.candidate {
					return eventing.PRDevelopmentControllerSuspensionWorkCandidate{}, false, nil
				}
				return handoffCandidate, true, nil
			}
			store.claimSuspension = func(
				context.Context,
				eventing.PRDevelopmentControllerSuspensionClaim,
			) (eventing.PRDevelopmentControllerSuspensionLease, bool, error) {
				claimedChild = true
				return eventing.PRDevelopmentControllerSuspensionLease{}, false, nil
			}
			git := &fakeControllerRecoveryGit{
				rotate: func(
					context.Context,
					gitworkspace.PinnedReservationRotationRequest,
				) (gitworkspace.PinnedReservationRotationResult, error) {
					return reservationRotationResult(intent), nil
				},
			}
			worker := mustControllerRecoveryWorker(t, store, func() (controllerRecoveryGit, error) {
				return git, nil
			})

			processed, err := worker.ProcessOne(context.Background())
			require.True(t, processed)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantError)
			assert.False(t, claimedChild)
		})
	}
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
		t.Parallel()

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
		t.Parallel()

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
		t.Parallel()

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
	controller.SourceCloneURL = intent.SourceCloneURL
	controller.SourceRef = intent.SourceRef
	controller.SourceCommit = intent.SourceCommit
	controller.SourceTree = intent.SourceTree
	controller.LineVersion = intent.LineVersion
	controller.MutationEpoch = intent.MutationEpoch
	controller.TipCommit = intent.TipCommit
	controller.Tree = intent.Tree
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
		return suspendedRecoveryController(controller), false, nil
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
		Controller: suspendedRecoveryController(controller),
		Operation:  operation,
	}
}

func suspendedRecoveryController(
	controller eventing.PRDevelopmentController,
) eventing.PRDevelopmentController {
	controller = suspensionPendingController(controller)
	controller.Phase = eventing.PRDevelopmentControllerSuspended
	controller.Revision++
	return controller
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

func suspendedResumeRecoveryFixture() (
	eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate,
	eventing.PRDevelopmentController,
	eventing.PRDevelopmentControllerSuspension,
) {
	now := time.Now().UTC()
	createdAt := now.Add(-2 * time.Hour)
	suspendedAt := now.Add(-time.Hour)
	preparedAt := now.Add(-30 * time.Minute)
	claimUntil := now.Add(time.Hour)
	controller := eventing.PRDevelopmentController{
		ID:               "controller-resume-recovery",
		ThreadID:         "thread-resume-recovery",
		OwnerSessionID:   "session-resume-recovery",
		AgentID:          "agent-resume-recovery",
		Revision:         12,
		Phase:            eventing.PRDevelopmentControllerSuspended,
		LineID:           "line-resume-recovery",
		WorkspaceID:      "workspace-resume-recovery",
		SourceCloneURL:   "https://example.test/owner/resume-recovery.git",
		SourceRef:        "refs/pull/5/head",
		SourceCommit:     testObjectID("1"),
		SourceTree:       testObjectID("2"),
		LineVersion:      3,
		MutationEpoch:    4,
		TipCommit:        testObjectID("3"),
		Tree:             testObjectID("4"),
		LeaseEpoch:       5,
		CurrentAttemptID: "attempt-resume-recovery",
		UpdatedAt:        preparedAt,
	}
	suspension := eventing.PRDevelopmentControllerSuspension{
		ID:                          "pdsi_resume_recovery",
		ControllerID:                controller.ID,
		ThreadID:                    controller.ThreadID,
		OwnerSessionID:              controller.OwnerSessionID,
		AttemptID:                   "attempt-before-resume-recovery",
		Ordinal:                     2,
		SourceKind:                  eventing.PRDevelopmentControllerSuspensionSourceControllerRecovery,
		SourceRecoveryID:            "pdri_resume_recovery_source",
		SourceFinalRevision:         9,
		SourceFinalHash:             testDigest("1"),
		Mode:                        eventing.PRDevelopmentControllerSuspensionCandidate,
		Status:                      eventing.PRDevelopmentControllerSuspensionStatusResumeClaimed,
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
		SuspensionReservationDigest: testDigest("2"),
		MutationLeaseEpoch:          4,
		MutationLeaseTokenDigest:    testDigest("3"),
		SuspendIntentID:             "pdsi_resume_recovery",
		SuspendRequestJSON:          []byte(`{"version":1,"mode":"suspend"}`),
		SuspendRequestHash:          testDigest("4"),
		PreviousHash:                testDigest("5"),
		IntentHash:                  testDigest("6"),
		SuspendClaimID:              "pdsc_previous_suspension_claim",
		SuspendClaimOwner:           "previous-suspension-worker",
		SuspendClaimEpoch:           1,
		SuspendClaims:               1,
		SuspendClaimedAt:            &createdAt,
		SuspendClaimTokenDigest:     testDigest("7"),
		SuspendResult: eventing.PRDevelopmentControllerSuspensionResult{
			WorkspaceID:      controller.WorkspaceID,
			Version:          controller.LineVersion,
			MutationEpoch:    controller.MutationEpoch,
			Tip:              controller.TipCommit,
			Tree:             controller.Tree,
			CandidateTree:    testObjectID("5"),
			CandidateDigest:  testDigest("8"),
			ChangedFileCount: 2,
			SuspensionHash:   testDigest("9"),
		},
		SuspendResultJSON:       []byte(`{"version":1,"result":"suspended"}`),
		SuspendResultHash:       testDigest("a"),
		FinalSuspensionRevision: controller.Revision - 1,
		SuspensionFinalHash:     testDigest("b"),
		SuspendedAt:             &suspendedAt,
		ResumeAttemptID:         controller.CurrentAttemptID,
		ResumeIntentID:          "pdsri_resume_recovery",
		ResumeReservationKey:    "resume-recovery-reservation-secret",
		ResumeReservationDigest: controllerRecoveryReservationDigest(
			"resume-recovery-reservation-secret",
		),
		ResumeRequestJSON: []byte(`{"version":1,"mode":"resume"}`),
		ResumeRequestHash: testDigest("d"),
		ResumeIntentHash:  testDigest("e"),
		ResumePreparedAt:  &preparedAt,
		ResumeClaimID:     "pdsrc_expired_resume_claim",
		ResumeClaimOwner:  "expired-resume-worker",
		ResumeClaimToken:  "expired-resume-claim-token",
		ResumeClaimUntil:  &claimUntil,
		ResumeClaimEpoch:  1,
		ResumeClaims:      1,
		ResumeClaimedAt:   &preparedAt,
		CreatedAt:         createdAt,
		UpdatedAt:         preparedAt,
	}
	suspension.SuspendRequest = eventing.PRDevelopmentControllerSuspensionRequest{
		Repository:            suspension.SourceCloneURL,
		SourceRef:             suspension.SourceRef,
		SourceCommit:          suspension.SourceCommit,
		AgentID:               suspension.AgentID,
		WorkspaceID:           suspension.WorkspaceID,
		LineID:                suspension.LineID,
		IntentID:              suspension.SuspendIntentID,
		ExpectedVersion:       suspension.LineVersion,
		ExpectedMutationEpoch: suspension.MutationEpoch,
		ExpectedTip:           suspension.TipCommit,
		ExpectedTree:          suspension.Tree,
	}
	suspension.ResumeRequest = eventing.PRDevelopmentControllerSuspendedResumeRequest{
		Repository:            suspension.SourceCloneURL,
		SourceRef:             suspension.SourceRef,
		SourceCommit:          suspension.SourceCommit,
		ReservationKey:        suspension.ResumeReservationKey,
		AgentID:               suspension.AgentID,
		WorkspaceID:           suspension.WorkspaceID,
		LineID:                suspension.LineID,
		IntentID:              suspension.ResumeIntentID,
		ExpectedVersion:       suspension.LineVersion,
		ExpectedMutationEpoch: suspension.MutationEpoch,
		ExpectedTip:           suspension.TipCommit,
		ExpectedTree:          suspension.Tree,
		SuspensionHash:        suspension.SuspendResult.SuspensionHash,
		CandidateTree:         suspension.SuspendResult.CandidateTree,
		CandidateDigest:       suspension.SuspendResult.CandidateDigest,
		ChangedFileCount:      suspension.SuspendResult.ChangedFileCount,
	}
	return eventing.PRDevelopmentControllerSuspendedResumeRecoveryCandidate{
		CaseID:           "case-resume-recovery",
		SuspensionID:     suspension.ID,
		ControllerID:     controller.ID,
		AttemptID:        controller.CurrentAttemptID,
		ExpectedRevision: controller.Revision,
		AvailableAt:      now.Add(-time.Minute),
	}, controller, suspension
}

func claimedSuspendedResumeRecovery(
	suspension eventing.PRDevelopmentControllerSuspension,
	input eventing.PRDevelopmentControllerSuspendedResumeRecoveryClaim,
) eventing.PRDevelopmentControllerSuspension {
	now := time.Now().UTC()
	deadline := now.Add(time.Hour)
	suspension.ResumeClaimID = input.ClaimID
	suspension.ResumeClaimOwner = input.WorkerLabel
	suspension.ResumeClaimToken = "resume-recovery-claim-token-secret"
	suspension.ResumeClaimUntil = &deadline
	suspension.ResumeClaimEpoch++
	suspension.ResumeClaims++
	suspension.ResumeClaimedAt = &now
	suspension.UpdatedAt = now
	return suspension
}

func suspendedResumeRecoveryGitResult(
	suspension eventing.PRDevelopmentControllerSuspension,
	replayed bool,
) gitworkspace.PinnedLineSuspendedResumeResult {
	request := suspension.ResumeRequest
	return gitworkspace.PinnedLineSuspendedResumeResult{
		WorkspaceID:      request.WorkspaceID,
		Version:          request.ExpectedVersion,
		MutationEpoch:    request.ExpectedMutationEpoch,
		Tip:              request.ExpectedTip,
		Tree:             request.ExpectedTree,
		CandidateTree:    request.CandidateTree,
		CandidateDigest:  request.CandidateDigest,
		ChangedFileCount: request.ChangedFileCount,
		SuspensionHash:   request.SuspensionHash,
		RotationHash:     testDigest("f"),
		AlreadyResumed:   replayed,
	}
}

func suspendedResumeRecoveryTransition(
	controller eventing.PRDevelopmentController,
	claimed eventing.PRDevelopmentControllerSuspension,
	result eventing.PRDevelopmentControllerSuspendedResumeResult,
) eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition {
	now := time.Now().UTC()
	result.AlreadyResumed = false
	resumed := claimed
	resumed.Status = eventing.PRDevelopmentControllerSuspensionStatusResumed
	resumed.ResumeReservationKey = ""
	resumed.ResumeRequest.ReservationKey = ""
	resumed.ResumeClaimToken = ""
	resumed.ResumeClaimUntil = nil
	resumed.ResumeClaimTokenDigest = controllerRecoverySuspensionTokenDigest(
		"picoclaw-pr-development-controller-suspension-resume-claim-token-v1\x00",
		claimed.ResumeClaimToken,
	)
	resumed.ResumeResult = result
	resumed.ResumeResultJSON = []byte(`{"version":1,"result":"resumed"}`)
	resumed.ResumeResultHash = testDigest("2")
	resumed.NewMutationLeaseEpoch = controller.LeaseEpoch
	resumed.NewMutationLeaseTokenDigest = controllerRecoverySuspensionTokenDigest(
		"picoclaw-pr-development-controller-suspended-resume-recovery-handoff-v1\x00",
		claimed.ResumeClaimToken,
	)
	resumed.NewMutationLeaseUntil = claimed.ResumeClaimUntil
	resumed.FinalResumeRevision = controller.Revision
	resumed.ResumeFinalHash = testDigest("4")
	resumed.ResumedAt = &now
	resumed.UpdatedAt = now

	controller.Revision++
	controller.Phase = eventing.PRDevelopmentControllerSuspensionPending
	controller.WorkspaceID = result.WorkspaceID
	controller.LineVersion = result.Version
	controller.MutationEpoch = result.MutationEpoch
	controller.TipCommit = result.Tip
	controller.Tree = result.Tree
	controller.LeaseKind = ""
	controller.LeaseOwner = ""
	controller.LeaseToken = ""
	controller.LeaseUntil = nil
	controller.MutationReservationKey = ""
	controller.UpdatedAt = now

	child := eventing.PRDevelopmentControllerSuspension{
		ID:                          "pdsi_resume_recovery_child",
		ControllerID:                controller.ID,
		ThreadID:                    controller.ThreadID,
		OwnerSessionID:              controller.OwnerSessionID,
		AttemptID:                   controller.CurrentAttemptID,
		Ordinal:                     resumed.Ordinal + 1,
		SourceKind:                  eventing.PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery,
		SourceRecoveryID:            resumed.ID,
		SourceFinalRevision:         resumed.FinalResumeRevision,
		SourceFinalHash:             resumed.ResumeFinalHash,
		Mode:                        eventing.PRDevelopmentControllerSuspensionCandidate,
		Status:                      eventing.PRDevelopmentControllerSuspensionStatusSuspendClaimed,
		AgentID:                     controller.AgentID,
		WorkspaceID:                 result.WorkspaceID,
		LineID:                      controller.LineID,
		SourceCloneURL:              controller.SourceCloneURL,
		SourceRef:                   controller.SourceRef,
		SourceCommit:                controller.SourceCommit,
		SourceTree:                  controller.SourceTree,
		LineVersion:                 result.Version,
		MutationEpoch:               result.MutationEpoch,
		TipCommit:                   result.Tip,
		Tree:                        result.Tree,
		SuspensionReservationKey:    claimed.ResumeReservationKey,
		SuspensionReservationDigest: resumed.ResumeReservationDigest,
		MutationLeaseEpoch:          resumed.NewMutationLeaseEpoch,
		MutationLeaseTokenDigest:    resumed.NewMutationLeaseTokenDigest,
		SuspendIntentID:             "pdsi_resume_recovery_child",
		SuspendRequestJSON:          []byte(`{"version":1,"mode":"suspend"}`),
		SuspendRequestHash:          testDigest("5"),
		PreviousHash:                resumed.ResumeFinalHash,
		IntentHash:                  testDigest("6"),
		SuspendClaimID: controllerSuspendedResumeRecoveryChildClaimID(
			resumed.ID,
			resumed.ResumeClaimID,
		),
		SuspendClaimOwner: resumed.ResumeClaimOwner,
		SuspendClaimToken: claimed.ResumeClaimToken,
		SuspendClaimUntil: resumed.NewMutationLeaseUntil,
		SuspendClaimEpoch: resumed.ResumeClaimEpoch,
		SuspendClaims:     int(resumed.ResumeClaimEpoch),
		SuspendClaimedAt:  &now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	child.SuspendRequest = eventing.PRDevelopmentControllerSuspensionRequest{
		Repository:            child.SourceCloneURL,
		SourceRef:             child.SourceRef,
		SourceCommit:          child.SourceCommit,
		ReservationKey:        child.SuspensionReservationKey,
		AgentID:               child.AgentID,
		WorkspaceID:           child.WorkspaceID,
		LineID:                child.LineID,
		IntentID:              child.ID,
		ExpectedVersion:       child.LineVersion,
		ExpectedMutationEpoch: child.MutationEpoch,
		ExpectedTip:           child.TipCommit,
		ExpectedTree:          child.Tree,
	}
	return eventing.PRDevelopmentControllerSuspendedResumeRecoveryTransition{
		Controller:     controller,
		Resumed:        resumed,
		NextSuspension: child,
	}
}

func controllerRecoverySuspensionFixture(
	controller eventing.PRDevelopmentController,
	reservation, sourceRecoveryID string,
) (
	eventing.PRDevelopmentControllerSuspensionWorkCandidate,
	eventing.PRDevelopmentControllerSuspension,
) {
	now := time.Now().UTC()
	suspension := eventing.PRDevelopmentControllerSuspension{
		ID:                          "pdsi_controller_recovery_handoff",
		ControllerID:                controller.ID,
		ThreadID:                    controller.ThreadID,
		OwnerSessionID:              controller.OwnerSessionID,
		AttemptID:                   controller.CurrentAttemptID,
		Ordinal:                     1,
		SourceKind:                  eventing.PRDevelopmentControllerSuspensionSourceControllerRecovery,
		SourceRecoveryID:            sourceRecoveryID,
		SourceFinalRevision:         controller.Revision - 1,
		SourceFinalHash:             testDigest("1"),
		Mode:                        eventing.PRDevelopmentControllerSuspensionCandidate,
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
		SuspensionReservationKey:    reservation,
		SuspensionReservationDigest: controllerRecoveryReservationDigest(reservation),
		MutationLeaseEpoch:          controller.LeaseEpoch,
		MutationLeaseTokenDigest:    testDigest("2"),
		SuspendIntentID:             "pdsi_controller_recovery_handoff",
		SuspendRequestJSON:          []byte(`{"version":1,"mode":"suspend"}`),
		SuspendRequestHash:          testDigest("3"),
		PreviousHash:                testDigest("4"),
		IntentHash:                  testDigest("5"),
		CreatedAt:                   now,
		UpdatedAt:                   now,
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
	return eventing.PRDevelopmentControllerSuspensionWorkCandidate{
		CaseID:           "case-one",
		SuspensionID:     suspension.ID,
		ControllerID:     suspension.ControllerID,
		AttemptID:        suspension.AttemptID,
		SourceKind:       suspension.SourceKind,
		Mode:             suspension.Mode,
		ExpectedRevision: controller.Revision,
		AvailableAt:      now,
	}, suspension
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
	claimToken := claimed.SuspendClaimToken
	claimed.Status = eventing.PRDevelopmentControllerSuspensionStatusSuspended
	claimed.SuspensionReservationKey = ""
	claimed.SuspendRequest.ReservationKey = ""
	claimed.SuspendClaimToken = ""
	claimed.SuspendClaimUntil = nil
	claimed.SuspendClaimTokenDigest = controllerRecoverySuspensionTokenDigest(
		"picoclaw-pr-development-controller-suspension-claim-token-v1\x00",
		claimToken,
	)
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
