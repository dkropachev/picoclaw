package gitworkspace

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PinnedLineAdoptRecoveryRequest reconciles an expired mutation lease across
// the exact before/after states of AdoptPinnedLine. Adopt authenticates the old
// reservation. IntentID and the replacement reservation must be durable before
// invocation.
type PinnedLineAdoptRecoveryRequest struct {
	Adopt                     PinnedLineAdoptRequest `json:"-"`
	IntentID                  string                 `json:"-"`
	ReplacementReservationKey string                 `json:"-"`
}

// PinnedLineResumeRecoveryRequest reconciles an expired mutation lease across
// the exact before/after states of ResumePinnedLine. Resume authenticates the
// issued old reservation, which may not have reached Git inventory yet.
type PinnedLineResumeRecoveryRequest struct {
	Resume                    PinnedLineResumeRequest `json:"-"`
	IntentID                  string                  `json:"-"`
	ReplacementReservationKey string                  `json:"-"`
}

// PinnedLineReservationRecoveryResult binds one recovered line to the stable
// old-to-fresh rotation proof. Reservation values, paths, and refs stay private.
type PinnedLineReservationRecoveryResult struct {
	WorkspaceID    string `json:"-"`
	Version        int64  `json:"-"`
	MutationEpoch  int64  `json:"-"`
	Tip            string `json:"-"`
	Tree           string `json:"-"`
	RotationHash   string `json:"-"`
	AlreadyRotated bool   `json:"-"`
}

// RecoverPinnedLineAdoptReservation permanently revokes an expired adoption
// reservation and converges both an unadopted pin and an exactly adopted line
// to the same fresh, bound version-zero lease. In the unadopted case the
// rotation is saved before the retained ref or line owner is installed, while
// both reservation operation locks remain held across the complete sequence.
func (m *Manager) RecoverPinnedLineAdoptReservation(
	ctx context.Context,
	request PinnedLineAdoptRecoveryRequest,
) (PinnedLineReservationRecoveryResult, error) {
	if m == nil {
		return PinnedLineReservationRecoveryResult{}, errors.New(
			"git workspace manager is not configured",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, validationErr := validatePinnedLineAdoptRecoveryRequest(ctx, request)
	if validationErr != nil {
		return PinnedLineReservationRecoveryResult{}, validationErr
	}
	releaseOperations, operationErr := m.acquirePinnedReservationRotationOperations(
		ctx,
		request.Adopt.Pin.ReservationKey,
		request.ReplacementReservationKey,
	)
	if operationErr != nil {
		return PinnedLineReservationRecoveryResult{}, operationErr
	}
	defer releaseOperations()

	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, lockErr := m.lockInventory(ctx)
	if lockErr != nil {
		return PinnedLineReservationRecoveryResult{}, lockErr
	}
	defer unlockInventory()
	state, loadErr := m.loadLocked()
	if loadErr != nil {
		return PinnedLineReservationRecoveryResult{}, loadErr
	}

	unboundRotation := pinnedLineAdoptRecoveryRotationRequest(request, false)
	boundRotation := pinnedLineAdoptRecoveryRotationRequest(request, true)
	rotations := state.PinnedReservationRotations[request.Adopt.WorkspaceID]
	if len(rotations) > 0 && rotations[len(rotations)-1].IntentID == request.IntentID {
		record := rotations[len(rotations)-1]
		if record.LineID == "" {
			if matchErr := matchPinnedReservationRotationRecord(
				record,
				unboundRotation,
				repository,
			); matchErr != nil {
				return PinnedLineReservationRecoveryResult{}, matchErr
			}
			return m.completePinnedLineAdoptRecoveryLocked(
				ctx,
				state,
				request,
				repository,
				record,
				true,
			)
		}
		rotated, replayErr := replayPinnedReservationRotation(
			state,
			record,
			boundRotation,
			repository,
		)
		if replayErr != nil {
			return PinnedLineReservationRecoveryResult{}, replayErr
		}
		freshPin := request.Adopt.Pin
		freshPin.ReservationKey = request.ReplacementReservationKey
		if verifyErr := m.verifyPinnedLineReservationRecoveryState(
			ctx,
			state,
			state.DevelopmentLines[request.Adopt.LineID],
			freshPin,
			request.Adopt.WorkspaceID,
			repository,
		); verifyErr != nil {
			return PinnedLineReservationRecoveryResult{}, verifyErr
		}
		return pinnedLineRecoveryResultFromRotation(rotated), nil
	}
	if pinnedReservationRotationIntentUsed(state, request.IntentID) {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: reservation rotation intent was already used",
			ErrPinnedLineConflict,
		)
	}
	if len(rotations) >= maxPinnedReservationRotations {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: reservation rotation history is full",
			ErrPinnedLineConflict,
		)
	}

	workspace, duplicate := findPinnedReservationWorkspaceLocked(
		state,
		request.Adopt.Pin.ReservationKey,
	)
	if duplicate || workspace == nil || workspace.ID != request.Adopt.WorkspaceID {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: expired adoption reservation does not own the exact workspace",
			ErrPinnedLineConflict,
		)
	}
	if workspace.RepoID != repoID(repository) || workspace.RemoteURL != repository ||
		workspace.Ref != request.Adopt.Pin.SourceRef ||
		workspace.PinnedSourceRef != request.Adopt.Pin.SourceRef ||
		workspace.PinnedCommit != request.Adopt.Pin.ExpectedCommit ||
		workspace.LockedBy == nil ||
		workspace.LockedBy.AgentID != request.Adopt.Pin.AgentID {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: adoption workspace recovery fence changed",
			ErrPinnedLineConflict,
		)
	}
	previousHash := developmentLineReservationHash(request.Adopt.Pin.ReservationKey)
	if pinnedReservationRotationRevoked(state, previousHash) {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: expired adoption reservation was already revoked",
			ErrPinnedLineConflict,
		)
	}
	replacementHash := developmentLineReservationHash(request.ReplacementReservationKey)
	if freshErr := requireFreshPinnedReservationRotation(state, replacementHash); freshErr != nil {
		return PinnedLineReservationRecoveryResult{}, freshErr
	}

	if workspace.DevelopmentLineID == "" {
		now := m.now().UTC()
		record, recordErr := appendPinnedLineRecoveryRotation(
			state,
			workspace,
			unboundRotation,
			repository,
			now,
		)
		if recordErr != nil {
			return PinnedLineReservationRecoveryResult{}, recordErr
		}
		workspace.LockedBy = &LockInfo{
			SessionKey:  request.ReplacementReservationKey,
			AgentID:     request.Adopt.Pin.AgentID,
			LockedAt:    now,
			HeartbeatAt: now,
		}
		workspace.UpdatedAt = now
		if saveErr := m.saveLocked(state); saveErr != nil {
			return PinnedLineReservationRecoveryResult{}, saveErr
		}
		return m.completePinnedLineAdoptRecoveryLocked(
			ctx,
			state,
			request,
			repository,
			record,
			false,
		)
	}

	line := state.DevelopmentLines[request.Adopt.LineID]
	if line == nil || workspace.DevelopmentLineID != request.Adopt.LineID ||
		line.PendingParkSet {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: adoption recovery state changed",
			ErrPinnedLineConflict,
		)
	}
	if matchErr := matchAdoptedPinnedLine(
		state,
		line,
		request.Adopt,
		repository,
		previousHash,
	); matchErr != nil {
		return PinnedLineReservationRecoveryResult{}, matchErr
	}
	if verifyErr := m.verifyPinnedLineReservationRecoveryState(
		ctx,
		state,
		line,
		request.Adopt.Pin,
		request.Adopt.WorkspaceID,
		repository,
	); verifyErr != nil {
		return PinnedLineReservationRecoveryResult{}, verifyErr
	}
	now := m.now().UTC()
	record, recordErr := appendPinnedLineRecoveryRotation(
		state,
		workspace,
		boundRotation,
		repository,
		now,
	)
	if recordErr != nil {
		return PinnedLineReservationRecoveryResult{}, recordErr
	}
	workspace.LockedBy = &LockInfo{
		SessionKey:  request.ReplacementReservationKey,
		AgentID:     request.Adopt.Pin.AgentID,
		LockedAt:    now,
		HeartbeatAt: now,
	}
	workspace.UpdatedAt = now
	line.MutationReservationHash = replacementHash
	line.MutationAgentID = request.Adopt.Pin.AgentID
	line.UpdatedAt = now
	if saveErr := m.saveLocked(state); saveErr != nil {
		return PinnedLineReservationRecoveryResult{}, saveErr
	}
	return pinnedLineRecoveryResult(line, record.RecordHash, false), nil
}

// RecoverPinnedLineResumeReservation atomically converges an exact parked
// pre-Resume line or an exact old-owned post-Resume line to one fresh mutation
// reservation. No repository content or ref is changed.
func (m *Manager) RecoverPinnedLineResumeReservation(
	ctx context.Context,
	request PinnedLineResumeRecoveryRequest,
) (PinnedLineReservationRecoveryResult, error) {
	if m == nil {
		return PinnedLineReservationRecoveryResult{}, errors.New(
			"git workspace manager is not configured",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, validationErr := validatePinnedLineResumeRecoveryRequest(ctx, request)
	if validationErr != nil {
		return PinnedLineReservationRecoveryResult{}, validationErr
	}
	releaseOperations, operationErr := m.acquirePinnedReservationRotationOperations(
		ctx,
		request.Resume.Pin.ReservationKey,
		request.ReplacementReservationKey,
	)
	if operationErr != nil {
		return PinnedLineReservationRecoveryResult{}, operationErr
	}
	defer releaseOperations()

	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, lockErr := m.lockInventory(ctx)
	if lockErr != nil {
		return PinnedLineReservationRecoveryResult{}, lockErr
	}
	defer unlockInventory()
	state, loadErr := m.loadLocked()
	if loadErr != nil {
		return PinnedLineReservationRecoveryResult{}, loadErr
	}
	rotation := pinnedLineResumeRecoveryRotationRequest(request)
	rotations := state.PinnedReservationRotations[request.Resume.WorkspaceID]
	if len(rotations) > 0 && rotations[len(rotations)-1].IntentID == request.IntentID {
		rotated, replayErr := replayPinnedReservationRotation(
			state,
			rotations[len(rotations)-1],
			rotation,
			repository,
		)
		if replayErr != nil {
			return PinnedLineReservationRecoveryResult{}, replayErr
		}
		freshPin := request.Resume.Pin
		freshPin.ReservationKey = request.ReplacementReservationKey
		if verifyErr := m.verifyPinnedLineReservationRecoveryState(
			ctx,
			state,
			state.DevelopmentLines[request.Resume.LineID],
			freshPin,
			request.Resume.WorkspaceID,
			repository,
		); verifyErr != nil {
			return PinnedLineReservationRecoveryResult{}, verifyErr
		}
		return pinnedLineRecoveryResultFromRotation(rotated), nil
	}
	if pinnedReservationRotationIntentUsed(state, request.IntentID) {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: reservation rotation intent was already used",
			ErrPinnedLineConflict,
		)
	}
	if len(rotations) >= maxPinnedReservationRotations {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: reservation rotation history is full",
			ErrPinnedLineConflict,
		)
	}

	line := state.DevelopmentLines[request.Resume.LineID]
	if line == nil || line.PendingParkSet {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: resume recovery line is unavailable",
			ErrPinnedLineConflict,
		)
	}
	if matchErr := matchPinnedLineResumeIdentity(
		line,
		request.Resume,
		repository,
	); matchErr != nil {
		return PinnedLineReservationRecoveryResult{}, matchErr
	}
	workspace := state.Workspaces[line.WorkspaceID]
	if workspace == nil || workspace.ID != request.Resume.WorkspaceID ||
		workspace.DevelopmentLineID != line.ID || workspace.DroppedAt != nil {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: resume recovery workspace changed",
			ErrPinnedLineConflict,
		)
	}
	previousHash := developmentLineReservationHash(request.Resume.Pin.ReservationKey)
	replacementHash := developmentLineReservationHash(request.ReplacementReservationKey)
	if freshErr := requireFreshPinnedReservationRotation(state, replacementHash); freshErr != nil {
		return PinnedLineReservationRecoveryResult{}, freshErr
	}
	finalEpoch := request.Resume.ExpectedEpoch + 1
	preEffect := false
	switch {
	case line.State == developmentLineMutating &&
		line.Version == request.Resume.ExpectedVersion &&
		line.MutationEpoch == finalEpoch &&
		line.MutationReservationHash == previousHash &&
		line.MutationAgentID == request.Resume.Pin.AgentID:
		owned, duplicate := findPinnedReservationWorkspaceLocked(
			state,
			request.Resume.Pin.ReservationKey,
		)
		if duplicate || owned != workspace || workspace.LockedBy == nil ||
			workspace.LockedBy.AgentID != request.Resume.Pin.AgentID ||
			pinnedReservationRotationRevoked(state, previousHash) {
			return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
				"%w: post-resume reservation owner changed",
				ErrPinnedLineConflict,
			)
		}
	case line.State == developmentLineParked &&
		line.Version == request.Resume.ExpectedVersion &&
		line.MutationEpoch == request.Resume.ExpectedEpoch &&
		workspace.LockedBy == nil:
		if freshErr := requireFreshPinnedLineReservation(
			state,
			line.ID,
			request.Resume.Pin.ReservationKey,
		); freshErr != nil {
			return PinnedLineReservationRecoveryResult{}, freshErr
		}
		environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
		if environmentErr != nil {
			return PinnedLineReservationRecoveryResult{}, environmentErr
		}
		defer cleanup()
		if verifyErr := m.verifyDevelopmentLineParkedWorkspace(
			ctx,
			workspace,
			line,
			repository,
			request.Resume.ExpectedTip,
			request.Resume.ExpectedTree,
			environment,
		); verifyErr != nil {
			return PinnedLineReservationRecoveryResult{}, verifyErr
		}
		preEffect = true
	default:
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: resume recovery fence changed",
			ErrPinnedLineConflict,
		)
	}
	if !preEffect {
		if verifyErr := m.verifyPinnedLineReservationRecoveryState(
			ctx,
			state,
			line,
			request.Resume.Pin,
			request.Resume.WorkspaceID,
			repository,
		); verifyErr != nil {
			return PinnedLineReservationRecoveryResult{}, verifyErr
		}
	}

	now := m.now().UTC()
	record, recordErr := appendPinnedLineRecoveryRotation(
		state,
		workspace,
		rotation,
		repository,
		now,
	)
	if recordErr != nil {
		return PinnedLineReservationRecoveryResult{}, recordErr
	}
	workspace.LockedBy = &LockInfo{
		SessionKey:  request.ReplacementReservationKey,
		AgentID:     request.Resume.Pin.AgentID,
		LockedAt:    now,
		HeartbeatAt: now,
	}
	workspace.UpdatedAt = now
	line.State = developmentLineMutating
	line.MutationEpoch = finalEpoch
	line.MutationReservationHash = replacementHash
	line.MutationAgentID = request.Resume.Pin.AgentID
	line.UpdatedAt = now
	if preEffect {
		workspace.LastWorkAt = now
		if repositoryRecord := state.Repositories[workspace.RepoID]; repositoryRecord != nil {
			repositoryRecord.LastSeenAt = now
			repositoryRecord.LastWorkAt = now
		}
		m.addHistoryLocked(
			state,
			now,
			"development_line_resumed",
			workspace.RepoID,
			workspace.ID,
			"",
			"",
			line.Tip,
		)
	}
	if saveErr := m.saveLocked(state); saveErr != nil {
		return PinnedLineReservationRecoveryResult{}, saveErr
	}
	return pinnedLineRecoveryResult(line, record.RecordHash, false), nil
}

func (m *Manager) completePinnedLineAdoptRecoveryLocked(
	ctx context.Context,
	state *storeState,
	request PinnedLineAdoptRecoveryRequest,
	repository string,
	record pinnedReservationRotationRecord,
	alreadyRotated bool,
) (PinnedLineReservationRecoveryResult, error) {
	freshPin := request.Adopt.Pin
	freshPin.ReservationKey = request.ReplacementReservationKey
	freshRequest := request.Adopt
	freshRequest.Pin = freshPin
	replacementHash := developmentLineReservationHash(request.ReplacementReservationKey)
	if existing := state.DevelopmentLines[request.Adopt.LineID]; existing != nil {
		if existing.PendingParkSet {
			return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
				"%w: recovered adoption has a pending park",
				ErrPinnedLineConflict,
			)
		}
		if matchErr := matchAdoptedPinnedLine(
			state,
			existing,
			freshRequest,
			repository,
			replacementHash,
		); matchErr != nil {
			return PinnedLineReservationRecoveryResult{}, matchErr
		}
		if verifyErr := m.verifyPinnedLineReservationRecoveryState(
			ctx,
			state,
			existing,
			freshPin,
			request.Adopt.WorkspaceID,
			repository,
		); verifyErr != nil {
			return PinnedLineReservationRecoveryResult{}, verifyErr
		}
		return pinnedLineRecoveryResult(existing, record.RecordHash, alreadyRotated), nil
	}
	for _, line := range state.DevelopmentLines {
		if line != nil && line.WorkspaceID == request.Adopt.WorkspaceID {
			return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
				"%w: workspace already belongs to another development line",
				ErrPinnedLineConflict,
			)
		}
	}

	environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
	if environmentErr != nil {
		return PinnedLineReservationRecoveryResult{}, environmentErr
	}
	defer cleanup()
	workspace, workspaceErr := m.pinnedWorkspaceForOperation(
		ctx,
		state,
		freshPin,
		request.Adopt.WorkspaceID,
		repository,
		environment,
	)
	if workspaceErr != nil {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: %v",
			ErrPinnedLineConflict,
			workspaceErr,
		)
	}
	if workspace.DevelopmentLineID != "" {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: workspace already has a development line owner",
			ErrPinnedLineConflict,
		)
	}
	if !validControllerPinnedWorkspaceID(workspace.RepoID, workspace.ID) {
		return PinnedLineReservationRecoveryResult{}, fmt.Errorf(
			"%w: pinned workspace predates the private controller namespace",
			ErrPinnedLineConflict,
		)
	}
	if verifyErr := verifyPinnedLineCommitState(
		ctx,
		workspace,
		freshPin.ExpectedCommit,
		request.Adopt.ExpectedTree,
		environment,
	); verifyErr != nil {
		return PinnedLineReservationRecoveryResult{}, verifyErr
	}
	branch := developmentLineBranch(request.Adopt.LineID)
	if _, advanceErr := m.advanceDevelopmentLineRef(
		ctx,
		workspace,
		branch,
		freshPin.ExpectedCommit,
		freshPin.ExpectedCommit,
		true,
		environment,
	); advanceErr != nil {
		return PinnedLineReservationRecoveryResult{}, advanceErr
	}
	now := m.now().UTC()
	line := &developmentLineRecord{
		ID:                      request.Adopt.LineID,
		WorkspaceID:             workspace.ID,
		RepoID:                  workspace.RepoID,
		SourceRef:               freshPin.SourceRef,
		SourceCommit:            freshPin.ExpectedCommit,
		Branch:                  branch,
		Tip:                     freshPin.ExpectedCommit,
		Tree:                    request.Adopt.ExpectedTree,
		Version:                 0,
		MutationEpoch:           1,
		State:                   developmentLineMutating,
		MutationReservationHash: replacementHash,
		MutationAgentID:         freshPin.AgentID,
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	workspace.DevelopmentLineID = request.Adopt.LineID
	workspace.UpdatedAt = now
	state.DevelopmentLines[request.Adopt.LineID] = line
	m.addHistoryLocked(
		state,
		now,
		"development_line_adopted",
		workspace.RepoID,
		workspace.ID,
		"",
		"",
		freshPin.ExpectedCommit,
	)
	if saveErr := m.saveLocked(state); saveErr != nil {
		return PinnedLineReservationRecoveryResult{}, saveErr
	}
	return pinnedLineRecoveryResult(line, record.RecordHash, alreadyRotated), nil
}

func (m *Manager) verifyPinnedLineReservationRecoveryState(
	ctx context.Context,
	state *storeState,
	line *developmentLineRecord,
	pin PinnedAcquireRequest,
	workspaceID, repository string,
) error {
	if line == nil {
		return fmt.Errorf(
			"%w: recovered development line is missing",
			ErrPinnedLineConflict,
		)
	}
	environment, cleanup, err := m.newPinnedGitEnvironment()
	if err != nil {
		return err
	}
	defer cleanup()
	if err := m.verifyDevelopmentLineOwnedWorkspace(
		ctx,
		state,
		line,
		pin,
		workspaceID,
		repository,
		environment,
	); err != nil {
		return err
	}
	workspace := state.Workspaces[line.WorkspaceID]
	return verifyPinnedLineCommitState(
		ctx,
		workspace,
		line.Tip,
		line.Tree,
		environment,
	)
}

func validatePinnedLineAdoptRecoveryRequest(
	ctx context.Context,
	request PinnedLineAdoptRecoveryRequest,
) (string, error) {
	repository, err := validatePinnedLineAdoptRequest(ctx, request.Adopt)
	if err != nil {
		return "", err
	}
	if recoveryErr := validatePinnedLineRecoveryIdentity(
		request.IntentID,
		request.Adopt.Pin.ReservationKey,
		request.ReplacementReservationKey,
	); recoveryErr != nil {
		return "", recoveryErr
	}
	return repository, nil
}

func validatePinnedLineResumeRecoveryRequest(
	ctx context.Context,
	request PinnedLineResumeRecoveryRequest,
) (string, error) {
	repository, err := validatePinnedLineResumeRequest(ctx, request.Resume)
	if err != nil {
		return "", err
	}
	if request.Resume.ExpectedEpoch != request.Resume.ExpectedVersion {
		return "", fmt.Errorf(
			"%w: resume recovery requires the exact parked epoch",
			ErrPinnedLineInvalid,
		)
	}
	if recoveryErr := validatePinnedLineRecoveryIdentity(
		request.IntentID,
		request.Resume.Pin.ReservationKey,
		request.ReplacementReservationKey,
	); recoveryErr != nil {
		return "", recoveryErr
	}
	return repository, nil
}

func validatePinnedLineRecoveryIdentity(intentID, previous, replacement string) error {
	if !validPinnedOperationIdentity(intentID, maxDevelopmentLineIdentityBytes) ||
		!validPinnedOperationIdentity(replacement, 256) || replacement == previous {
		return fmt.Errorf(
			"%w: line reservation recovery identity is invalid",
			ErrPinnedLineInvalid,
		)
	}
	return nil
}

func pinnedLineAdoptRecoveryRotationRequest(
	request PinnedLineAdoptRecoveryRequest,
	bound bool,
) PinnedReservationRotationRequest {
	rotation := PinnedReservationRotationRequest{
		Pin:                       request.Adopt.Pin,
		WorkspaceID:               request.Adopt.WorkspaceID,
		IntentID:                  request.IntentID,
		ReplacementReservationKey: request.ReplacementReservationKey,
	}
	if bound {
		rotation.LineID = request.Adopt.LineID
		rotation.ExpectedMutationEpoch = 1
		rotation.ExpectedTip = request.Adopt.Pin.ExpectedCommit
		rotation.ExpectedTree = request.Adopt.ExpectedTree
	}
	return rotation
}

func pinnedLineResumeRecoveryRotationRequest(
	request PinnedLineResumeRecoveryRequest,
) PinnedReservationRotationRequest {
	return PinnedReservationRotationRequest{
		Pin:                       request.Resume.Pin,
		WorkspaceID:               request.Resume.WorkspaceID,
		IntentID:                  request.IntentID,
		ReplacementReservationKey: request.ReplacementReservationKey,
		LineID:                    request.Resume.LineID,
		ExpectedVersion:           request.Resume.ExpectedVersion,
		ExpectedMutationEpoch:     request.Resume.ExpectedEpoch + 1,
		ExpectedTip:               request.Resume.ExpectedTip,
		ExpectedTree:              request.Resume.ExpectedTree,
	}
}

func appendPinnedLineRecoveryRotation(
	state *storeState,
	workspace *WorkspaceRecord,
	request PinnedReservationRotationRequest,
	repository string,
	now time.Time,
) (pinnedReservationRotationRecord, error) {
	if state == nil || workspace == nil {
		return pinnedReservationRotationRecord{}, fmt.Errorf(
			"%w: reservation rotation owner is unavailable",
			ErrPinnedLineConflict,
		)
	}
	rotations := state.PinnedReservationRotations[workspace.ID]
	if len(rotations) >= maxPinnedReservationRotations {
		return pinnedReservationRotationRecord{}, fmt.Errorf(
			"%w: reservation rotation history is full",
			ErrPinnedLineConflict,
		)
	}
	previousRecordHash := emptyPinnedReservationRotationDigest()
	if len(rotations) > 0 {
		previousRecordHash = rotations[len(rotations)-1].RecordHash
	}
	record := pinnedReservationRotationRecord{
		IntentID:                request.IntentID,
		WorkspaceID:             workspace.ID,
		LineID:                  request.LineID,
		RepoID:                  repoID(repository),
		SourceRef:               request.Pin.SourceRef,
		SourceCommit:            request.Pin.ExpectedCommit,
		Version:                 request.ExpectedVersion,
		MutationEpoch:           request.ExpectedMutationEpoch,
		Tip:                     request.ExpectedTip,
		Tree:                    request.ExpectedTree,
		PreviousReservationHash: developmentLineReservationHash(request.Pin.ReservationKey),
		ReplacementReservationHash: developmentLineReservationHash(
			request.ReplacementReservationKey,
		),
		AgentID:            request.Pin.AgentID,
		PreviousRecordHash: previousRecordHash,
		RotatedAt:          now,
	}
	record.RecordHash = pinnedReservationRotationRecordDigest(record)
	rotations = append(rotations, record)
	state.PinnedReservationRotations[workspace.ID] = rotations
	workspace.PinnedReservationRotationCount = len(rotations)
	workspace.PinnedReservationRotationTailHash = record.RecordHash
	return record, nil
}

func pinnedLineRecoveryResult(
	line *developmentLineRecord,
	rotationHash string,
	alreadyRotated bool,
) PinnedLineReservationRecoveryResult {
	if line == nil {
		return PinnedLineReservationRecoveryResult{}
	}
	return PinnedLineReservationRecoveryResult{
		WorkspaceID:    line.WorkspaceID,
		Version:        line.Version,
		MutationEpoch:  line.MutationEpoch,
		Tip:            line.Tip,
		Tree:           line.Tree,
		RotationHash:   rotationHash,
		AlreadyRotated: alreadyRotated,
	}
}

func pinnedLineRecoveryResultFromRotation(
	rotation PinnedReservationRotationResult,
) PinnedLineReservationRecoveryResult {
	return PinnedLineReservationRecoveryResult{
		WorkspaceID:    rotation.WorkspaceID,
		Version:        rotation.Version,
		MutationEpoch:  rotation.MutationEpoch,
		Tip:            rotation.Tip,
		Tree:           rotation.Tree,
		RotationHash:   rotation.RotationHash,
		AlreadyRotated: rotation.AlreadyRotated,
	}
}
