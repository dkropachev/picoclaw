package prdevelopment

import (
	"context"
	"errors"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

type suspendedResumeGitBackend interface {
	ResumeSuspendedPinnedLine(
		ctx context.Context,
		request gitworkspace.PinnedLineSuspendedResumeRequest,
	) (gitworkspace.PinnedLineSuspendedResumeResult, error)
}

// resumeSuspendedController executes only the exact request decoded from the
// private eventing WAL. It creates no request field from model/provider input
// and returns only content-addressed evidence for atomic store finalization.
func resumeSuspendedController(
	ctx context.Context,
	backend suspendedResumeGitBackend,
	lease eventing.PRDevelopmentControllerSuspendedResumeLease,
) (eventing.PRDevelopmentControllerSuspendedResumeResult, error) {
	if backend == nil {
		return eventing.PRDevelopmentControllerSuspendedResumeResult{},
			errors.New("suspended-resume Git backend is unavailable")
	}
	controller := lease.Controller
	suspension := lease.Suspension
	request := suspension.ResumeRequest
	if controller.ID == "" || controller.Phase != eventing.PRDevelopmentControllerSuspended ||
		controller.ID != suspension.ControllerID ||
		controller.CurrentAttemptID != suspension.ResumeAttemptID ||
		controller.WorkspaceID != suspension.WorkspaceID ||
		controller.LineID != suspension.LineID ||
		controller.SourceCloneURL != suspension.SourceCloneURL ||
		controller.SourceRef != suspension.SourceRef ||
		controller.SourceCommit != suspension.SourceCommit ||
		controller.LineVersion != suspension.LineVersion ||
		controller.MutationEpoch != suspension.MutationEpoch ||
		controller.TipCommit != suspension.TipCommit || controller.Tree != suspension.Tree ||
		suspension.Status != eventing.PRDevelopmentControllerSuspensionStatusResumeClaimed ||
		suspension.ResumeReservationKey == "" || suspension.ResumeClaimToken == "" ||
		request.Repository != suspension.SourceCloneURL ||
		request.SourceRef != suspension.SourceRef || request.SourceCommit != suspension.SourceCommit ||
		request.ReservationKey != suspension.ResumeReservationKey ||
		request.AgentID != suspension.AgentID || request.WorkspaceID != suspension.WorkspaceID ||
		request.LineID != suspension.LineID || request.IntentID != suspension.ResumeIntentID ||
		request.ExpectedVersion != suspension.LineVersion ||
		request.ExpectedMutationEpoch != suspension.MutationEpoch ||
		request.ExpectedTip != suspension.TipCommit || request.ExpectedTree != suspension.Tree ||
		request.SuspensionHash != suspension.SuspendResult.SuspensionHash ||
		request.CandidateTree != suspension.SuspendResult.CandidateTree ||
		request.CandidateDigest != suspension.SuspendResult.CandidateDigest ||
		request.ChangedFileCount != suspension.SuspendResult.ChangedFileCount {
		return eventing.PRDevelopmentControllerSuspendedResumeResult{}, fmt.Errorf(
			"%w: suspended resume lease differs from its durable line fence",
			errControllerEffectConflict,
		)
	}
	resumed, err := backend.ResumeSuspendedPinnedLine(
		ctxOrBackground(ctx),
		gitworkspace.PinnedLineSuspendedResumeRequest{
			Pin: gitworkspace.PinnedAcquireRequest{
				Repository:     request.Repository,
				SourceRef:      request.SourceRef,
				ExpectedCommit: request.SourceCommit,
				ReservationKey: request.ReservationKey,
				AgentID:        request.AgentID,
			},
			WorkspaceID:           request.WorkspaceID,
			LineID:                request.LineID,
			IntentID:              request.IntentID,
			ExpectedVersion:       request.ExpectedVersion,
			ExpectedMutationEpoch: request.ExpectedMutationEpoch,
			ExpectedTip:           request.ExpectedTip,
			ExpectedTree:          request.ExpectedTree,
			SuspensionHash:        request.SuspensionHash,
			CandidateTree:         request.CandidateTree,
			CandidateDigest:       request.CandidateDigest,
			ChangedFileCount:      request.ChangedFileCount,
		},
	)
	if err != nil {
		return eventing.PRDevelopmentControllerSuspendedResumeResult{}, fmt.Errorf(
			"resume suspended retained development line: %w",
			err,
		)
	}
	if resumed.WorkspaceID != request.WorkspaceID ||
		resumed.Version != request.ExpectedVersion ||
		resumed.MutationEpoch != request.ExpectedMutationEpoch ||
		resumed.Tip != request.ExpectedTip || resumed.Tree != request.ExpectedTree ||
		resumed.CandidateTree != request.CandidateTree ||
		resumed.CandidateDigest != request.CandidateDigest ||
		resumed.ChangedFileCount != request.ChangedFileCount ||
		resumed.SuspensionHash != request.SuspensionHash ||
		!validControllerSHA256(resumed.RotationHash) {
		return eventing.PRDevelopmentControllerSuspendedResumeResult{}, fmt.Errorf(
			"%w: suspended resume result differs from its durable request",
			errControllerEffectConflict,
		)
	}
	return eventing.PRDevelopmentControllerSuspendedResumeResult{
		WorkspaceID:      resumed.WorkspaceID,
		Version:          resumed.Version,
		MutationEpoch:    resumed.MutationEpoch,
		Tip:              resumed.Tip,
		Tree:             resumed.Tree,
		CandidateTree:    resumed.CandidateTree,
		CandidateDigest:  resumed.CandidateDigest,
		ChangedFileCount: resumed.ChangedFileCount,
		SuspensionHash:   resumed.SuspensionHash,
		RotationHash:     resumed.RotationHash,
		AlreadyResumed:   resumed.AlreadyResumed,
	}, nil
}
