package prdevelopment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

type suspendedResumeGitFake struct {
	calls   int
	request gitworkspace.PinnedLineSuspendedResumeRequest
	result  gitworkspace.PinnedLineSuspendedResumeResult
	err     error
}

func (fake *suspendedResumeGitFake) ResumeSuspendedPinnedLine(
	_ context.Context,
	request gitworkspace.PinnedLineSuspendedResumeRequest,
) (gitworkspace.PinnedLineSuspendedResumeResult, error) {
	fake.calls++
	fake.request = request
	return fake.result, fake.err
}

func TestResumeSuspendedControllerExecutesOnlyDurableRequest(t *testing.T) {
	t.Parallel()

	lease := validSuspendedResumeLease()
	request := lease.Suspension.ResumeRequest
	backend := &suspendedResumeGitFake{result: gitworkspace.PinnedLineSuspendedResumeResult{
		WorkspaceID:      request.WorkspaceID,
		Version:          request.ExpectedVersion,
		MutationEpoch:    request.ExpectedMutationEpoch,
		Tip:              request.ExpectedTip,
		Tree:             request.ExpectedTree,
		CandidateTree:    request.CandidateTree,
		CandidateDigest:  request.CandidateDigest,
		ChangedFileCount: request.ChangedFileCount,
		SuspensionHash:   request.SuspensionHash,
		RotationHash:     strings.Repeat("6", 64),
		AlreadyResumed:   true,
	}}

	result, err := resumeSuspendedController(context.Background(), backend, lease)
	require.NoError(t, err)
	require.Equal(t, 1, backend.calls)
	require.Equal(t, request.Repository, backend.request.Pin.Repository)
	require.Equal(t, request.SourceRef, backend.request.Pin.SourceRef)
	require.Equal(t, request.SourceCommit, backend.request.Pin.ExpectedCommit)
	require.Equal(t, request.ReservationKey, backend.request.Pin.ReservationKey)
	require.Equal(t, request.AgentID, backend.request.Pin.AgentID)
	require.Equal(t, request.IntentID, backend.request.IntentID)
	require.Equal(t, request.CandidateDigest, backend.request.CandidateDigest)
	require.Equal(t, strings.Repeat("6", 64), result.RotationHash)
	require.True(t, result.AlreadyResumed)
}

func TestResumeSuspendedControllerRejectsChangedLeaseBeforeGit(t *testing.T) {
	t.Parallel()

	lease := validSuspendedResumeLease()
	lease.Suspension.ResumeRequest.CandidateDigest = strings.Repeat("7", 64)
	backend := &suspendedResumeGitFake{}

	_, err := resumeSuspendedController(context.Background(), backend, lease)
	require.Error(t, err)
	require.ErrorIs(t, err, errControllerEffectConflict)
	require.Zero(t, backend.calls)
}

func TestResumeSuspendedControllerRejectsChangedGitResult(t *testing.T) {
	t.Parallel()

	lease := validSuspendedResumeLease()
	request := lease.Suspension.ResumeRequest
	backend := &suspendedResumeGitFake{result: gitworkspace.PinnedLineSuspendedResumeResult{
		WorkspaceID:      request.WorkspaceID,
		Version:          request.ExpectedVersion,
		MutationEpoch:    request.ExpectedMutationEpoch,
		Tip:              request.ExpectedTip,
		Tree:             request.ExpectedTree,
		CandidateTree:    request.CandidateTree,
		CandidateDigest:  strings.Repeat("7", 64),
		ChangedFileCount: request.ChangedFileCount,
		SuspensionHash:   request.SuspensionHash,
		RotationHash:     strings.Repeat("6", 64),
	}}

	_, err := resumeSuspendedController(context.Background(), backend, lease)
	require.Error(t, err)
	require.ErrorIs(t, err, errControllerEffectConflict)
	require.Equal(t, 1, backend.calls)
}

func TestResumeSuspendedControllerPreservesGitFailure(t *testing.T) {
	t.Parallel()

	lease := validSuspendedResumeLease()
	sentinel := errors.New("resume failed")
	backend := &suspendedResumeGitFake{err: sentinel}

	_, err := resumeSuspendedController(context.Background(), backend, lease)
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 1, backend.calls)
}

func validSuspendedResumeLease() eventing.PRDevelopmentControllerSuspendedResumeLease {
	commit := strings.Repeat("1", 40)
	tree := strings.Repeat("2", 40)
	candidateTree := strings.Repeat("3", 40)
	candidateDigest := strings.Repeat("4", 64)
	suspensionHash := strings.Repeat("5", 64)
	request := eventing.PRDevelopmentControllerSuspendedResumeRequest{
		Repository:            "https://example.invalid/owner/repo.git",
		SourceRef:             "refs/heads/topic",
		SourceCommit:          commit,
		ReservationKey:        "pdck_resume",
		AgentID:               "agent",
		WorkspaceID:           "workspace",
		LineID:                "line",
		IntentID:              "resume-intent",
		ExpectedVersion:       2,
		ExpectedMutationEpoch: 3,
		ExpectedTip:           commit,
		ExpectedTree:          tree,
		SuspensionHash:        suspensionHash,
		CandidateTree:         candidateTree,
		CandidateDigest:       candidateDigest,
		ChangedFileCount:      2,
	}
	suspension := eventing.PRDevelopmentControllerSuspension{
		ID:             "pdsi_suspension",
		ControllerID:   "pdtc_controller",
		AttemptID:      "pdra_old",
		Status:         eventing.PRDevelopmentControllerSuspensionStatusResumeClaimed,
		AgentID:        request.AgentID,
		WorkspaceID:    request.WorkspaceID,
		LineID:         request.LineID,
		SourceCloneURL: request.Repository,
		SourceRef:      request.SourceRef,
		SourceCommit:   request.SourceCommit,
		LineVersion:    request.ExpectedVersion,
		MutationEpoch:  request.ExpectedMutationEpoch,
		TipCommit:      request.ExpectedTip,
		Tree:           request.ExpectedTree,
		SuspendResult: eventing.PRDevelopmentControllerSuspensionResult{
			SuspensionHash:   request.SuspensionHash,
			CandidateTree:    request.CandidateTree,
			CandidateDigest:  request.CandidateDigest,
			ChangedFileCount: request.ChangedFileCount,
		},
		ResumeAttemptID:      "pdra_new",
		ResumeIntentID:       request.IntentID,
		ResumeReservationKey: request.ReservationKey,
		ResumeRequest:        request,
		ResumeClaimToken:     "claim-token",
	}
	return eventing.PRDevelopmentControllerSuspendedResumeLease{
		Controller: eventing.PRDevelopmentController{
			ID:               suspension.ControllerID,
			Phase:            eventing.PRDevelopmentControllerSuspended,
			CurrentAttemptID: suspension.ResumeAttemptID,
			WorkspaceID:      suspension.WorkspaceID,
			LineID:           suspension.LineID,
			SourceCloneURL:   suspension.SourceCloneURL,
			SourceRef:        suspension.SourceRef,
			SourceCommit:     suspension.SourceCommit,
			LineVersion:      suspension.LineVersion,
			MutationEpoch:    suspension.MutationEpoch,
			TipCommit:        suspension.TipCommit,
			Tree:             suspension.Tree,
		},
		Suspension: suspension,
	}
}
