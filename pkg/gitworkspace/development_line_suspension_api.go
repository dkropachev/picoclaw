package gitworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PinnedLineSuspendRequest retires one exact live mutation reservation while
// retaining its current ordinary candidate on the private development line.
// Every field is controller-private and deliberately absent from JSON.
type PinnedLineSuspendRequest struct {
	Pin                   PinnedAcquireRequest `json:"-"`
	WorkspaceID           string               `json:"-"`
	LineID                string               `json:"-"`
	IntentID              string               `json:"-"`
	ExpectedVersion       int64                `json:"-"`
	ExpectedMutationEpoch int64                `json:"-"`
	ExpectedTip           string               `json:"-"`
	ExpectedTree          string               `json:"-"`
}

// PinnedLineCommitSuspensionRequest reconciles an exact prepared CommitPinned
// effect into reservation-free suspended state. Commit may be either not yet
// applied or already installed at detached HEAD.
type PinnedLineCommitSuspensionRequest struct {
	Suspend PinnedLineSuspendRequest `json:"-"`
	Commit  PinnedCommitRequest      `json:"-"`
}

// PinnedLineSuspendedResumeRequest installs one globally fresh reservation on
// the exact latest suspension. It carries only content-addressed evidence and
// never requires the retired bearer or original prepared-commit request.
type PinnedLineSuspendedResumeRequest struct {
	Pin                   PinnedAcquireRequest `json:"-"`
	WorkspaceID           string               `json:"-"`
	LineID                string               `json:"-"`
	IntentID              string               `json:"-"`
	ExpectedVersion       int64                `json:"-"`
	ExpectedMutationEpoch int64                `json:"-"`
	ExpectedTip           string               `json:"-"`
	ExpectedTree          string               `json:"-"`
	SuspensionHash        string               `json:"-"`
	CandidateTree         string               `json:"-"`
	CandidateDigest       string               `json:"-"`
	ChangedFileCount      int                  `json:"-"`
}

// PinnedLineSuspendResult is opaque controller evidence for one exact
// reservation-free retained candidate.
type PinnedLineSuspendResult struct {
	WorkspaceID           string `json:"-"`
	Version               int64  `json:"-"`
	MutationEpoch         int64  `json:"-"`
	Tip                   string `json:"-"`
	Tree                  string `json:"-"`
	CandidateTree         string `json:"-"`
	CandidateDigest       string `json:"-"`
	ChangedFileCount      int    `json:"-"`
	SuspensionHash        string `json:"-"`
	PreparedCommit        string `json:"-"`
	PreparedTree          string `json:"-"`
	PreparedCommitApplied bool   `json:"-"`
	AlreadySuspended      bool   `json:"-"`
}

// PinnedLineSuspendedResumeResult binds restored mutation ownership to both
// the immutable suspension and its fresh reservation-rotation proof.
type PinnedLineSuspendedResumeResult struct {
	WorkspaceID      string `json:"-"`
	Version          int64  `json:"-"`
	MutationEpoch    int64  `json:"-"`
	Tip              string `json:"-"`
	Tree             string `json:"-"`
	CandidateTree    string `json:"-"`
	CandidateDigest  string `json:"-"`
	ChangedFileCount int    `json:"-"`
	SuspensionHash   string `json:"-"`
	RotationHash     string `json:"-"`
	AlreadyResumed   bool   `json:"-"`
}

type pinnedLineSuspensionSnapshot struct {
	candidate      pinnedCandidateBuild
	preparedCommit string
	preparedTree   string
	applied        bool
}

// SuspendPinnedLine snapshots the exact current ordinary candidate and
// atomically removes its mutation reservation without moving HEAD, the index,
// ordinary files, or the retained ref.
func (m *Manager) SuspendPinnedLine(
	ctx context.Context,
	request PinnedLineSuspendRequest,
) (PinnedLineSuspendResult, error) {
	if m == nil {
		return PinnedLineSuspendResult{}, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, validationErr := validatePinnedLineSuspendRequest(ctx, request)
	if validationErr != nil {
		return PinnedLineSuspendResult{}, validationErr
	}
	requestHash := pinnedLineSuspensionRequestDigest(
		developmentLineSuspensionCandidate,
		request,
		repository,
		nil,
	)
	return m.suspendPinnedLine(
		ctx,
		request,
		repository,
		developmentLineSuspensionCandidate,
		requestHash,
		nil,
		time.Time{},
	)
}

// SuspendPinnedLineCommitRecovery records either side of one deterministic
// CommitPinned ambiguity while leaving Git state otherwise untouched.
func (m *Manager) SuspendPinnedLineCommitRecovery(
	ctx context.Context,
	request PinnedLineCommitSuspensionRequest,
) (PinnedLineSuspendResult, error) {
	if m == nil {
		return PinnedLineSuspendResult{}, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, validationErr := validatePinnedLineSuspendRequest(ctx, request.Suspend)
	if validationErr != nil {
		return PinnedLineSuspendResult{}, validationErr
	}
	commitRepository, authoredAt, commitErr := validatePinnedCommitRequest(ctx, request.Commit)
	if commitErr != nil {
		return PinnedLineSuspendResult{}, commitErr
	}
	if commitRepository != repository || request.Commit.Pin != request.Suspend.Pin ||
		request.Commit.WorkspaceID != request.Suspend.WorkspaceID ||
		request.Commit.ExpectedParent != request.Suspend.ExpectedTip {
		return PinnedLineSuspendResult{}, fmt.Errorf(
			"%w: prepared commit suspension identity changed",
			ErrPinnedLineInvalid,
		)
	}
	requestHash := pinnedLineSuspensionRequestDigest(
		developmentLineSuspensionCommitRecovery,
		request.Suspend,
		repository,
		&request.Commit,
	)
	commit := request.Commit
	return m.suspendPinnedLine(
		ctx,
		request.Suspend,
		repository,
		developmentLineSuspensionCommitRecovery,
		requestHash,
		&commit,
		authoredAt,
	)
}

func (m *Manager) suspendPinnedLine(
	ctx context.Context,
	request PinnedLineSuspendRequest,
	repository, mode, requestHash string,
	commit *PinnedCommitRequest,
	authoredAt time.Time,
) (PinnedLineSuspendResult, error) {
	if m == nil {
		return PinnedLineSuspendResult{}, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	releaseOperation, operationErr := m.acquireStandalonePinnedLineOperation(
		ctx,
		request.Pin.ReservationKey,
	)
	if operationErr != nil {
		return PinnedLineSuspendResult{}, operationErr
	}
	defer releaseOperation()

	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, lockErr := m.lockInventory(ctx)
	if lockErr != nil {
		return PinnedLineSuspendResult{}, lockErr
	}
	defer unlockInventory()
	state, loadErr := m.loadLocked()
	if loadErr != nil {
		return PinnedLineSuspendResult{}, loadErr
	}
	line := state.DevelopmentLines[request.LineID]
	if line == nil {
		return PinnedLineSuspendResult{}, fmt.Errorf(
			"%w: development line is missing",
			ErrPinnedLineConflict,
		)
	}
	reservationHash := developmentLineReservationHash(request.Pin.ReservationKey)
	if line.State == developmentLineSuspended {
		record, recordErr := matchPinnedLineSuspensionReplay(
			line,
			request,
			repository,
			mode,
			requestHash,
			reservationHash,
		)
		if recordErr != nil {
			return PinnedLineSuspendResult{}, recordErr
		}
		workspace := state.Workspaces[line.WorkspaceID]
		if verifyErr := m.verifyPinnedLineSuspensionRecordFilesystem(
			ctx,
			workspace,
			line,
			record,
			repository,
			commit,
			authoredAt,
			false,
		); verifyErr != nil {
			return PinnedLineSuspendResult{}, verifyErr
		}
		return pinnedLineSuspendResult(record, true), nil
	}
	if developmentLineSuspensionIntentUsed(state, request.IntentID) ||
		pinnedReservationRotationIntentUsed(state, request.IntentID) {
		return PinnedLineSuspendResult{}, fmt.Errorf(
			"%w: suspension intent was already used",
			ErrPinnedLineConflict,
		)
	}
	if developmentLineSuspensionRequestUsed(state, requestHash) {
		return PinnedLineSuspendResult{}, fmt.Errorf(
			"%w: suspension request was already used",
			ErrPinnedLineConflict,
		)
	}
	if line.State != developmentLineMutating || line.PendingParkSet ||
		line.Version != request.ExpectedVersion ||
		line.MutationEpoch != request.ExpectedMutationEpoch ||
		line.Tip != request.ExpectedTip || line.Tree != request.ExpectedTree ||
		line.MutationReservationHash != reservationHash ||
		line.MutationAgentID != request.Pin.AgentID {
		return PinnedLineSuspendResult{}, fmt.Errorf(
			"%w: development line suspension fence changed",
			ErrPinnedLineConflict,
		)
	}
	if sourceErr := matchPinnedLineSource(
		line,
		request.Pin,
		request.WorkspaceID,
		repository,
	); sourceErr != nil {
		return PinnedLineSuspendResult{}, sourceErr
	}
	workspace := state.Workspaces[line.WorkspaceID]
	if workspace == nil || workspace.PinnedReservationRotationCount >= maxPinnedReservationRotations ||
		line.SuspensionCount >= maxDevelopmentLineReservations {
		return PinnedLineSuspendResult{}, fmt.Errorf(
			"%w: development line suspension history has no resume capacity",
			ErrPinnedLineConflict,
		)
	}
	environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
	if environmentErr != nil {
		return PinnedLineSuspendResult{}, environmentErr
	}
	defer cleanup()
	if verifyErr := m.verifyDevelopmentLineOwnedWorkspace(
		ctx,
		state,
		line,
		request.Pin,
		request.WorkspaceID,
		repository,
		environment,
	); verifyErr != nil {
		return PinnedLineSuspendResult{}, verifyErr
	}
	snapshot, snapshotErr := m.snapshotPinnedLineForSuspension(
		ctx,
		workspace,
		line,
		mode,
		commit,
		authoredAt,
		environment,
	)
	if snapshotErr != nil {
		return PinnedLineSuspendResult{}, snapshotErr
	}

	now := m.now().UTC()
	record := developmentLineSuspensionRecord{
		Mode:                   mode,
		IntentID:               request.IntentID,
		RequestHash:            requestHash,
		WorkspaceID:            workspace.ID,
		LineID:                 line.ID,
		RepoID:                 line.RepoID,
		SourceRef:              line.SourceRef,
		SourceCommit:           line.SourceCommit,
		Version:                line.Version,
		MutationEpoch:          line.MutationEpoch,
		Tip:                    line.Tip,
		Tree:                   line.Tree,
		RetiredReservationHash: reservationHash,
		AgentID:                request.Pin.AgentID,
		CandidateTree:          snapshot.candidate.Tree,
		CandidateDigest:        snapshot.candidate.Digest,
		ChangedFileCount:       snapshot.candidate.ChangedFiles,
		PreparedCommit:         snapshot.preparedCommit,
		PreparedTree:           snapshot.preparedTree,
		PreparedCommitApplied:  snapshot.applied,
		PreviousRecordHash:     line.SuspensionTailHash,
		SuspendedAt:            now,
	}
	record.RecordHash = developmentLineSuspensionRecordDigest(record)
	line.Suspensions = append(line.Suspensions, record)
	line.SuspensionCount = len(line.Suspensions)
	line.SuspensionTailHash = record.RecordHash
	line.State = developmentLineSuspended
	line.MutationReservationHash = ""
	line.MutationAgentID = ""
	line.UpdatedAt = now
	workspace.LockedBy = nil
	workspace.UpdatedAt = now
	workspace.LastWorkAt = now
	if repositoryRecord := state.Repositories[workspace.RepoID]; repositoryRecord != nil {
		repositoryRecord.LastWorkAt = now
	}
	m.addHistoryLocked(
		state,
		now,
		"development_line_suspended",
		workspace.RepoID,
		workspace.ID,
		"",
		"",
		line.Tip,
	)
	if saveErr := m.saveLocked(state); saveErr != nil {
		return PinnedLineSuspendResult{}, saveErr
	}
	// Retirement is already durable at this point. Re-prove the exact
	// reservation-free filesystem form before reporting success; a failed
	// postflight remains safely replayable through the suspension record.
	if verifyErr := m.verifyPinnedLineSuspensionRecordFilesystem(
		ctx,
		workspace,
		line,
		record,
		repository,
		commit,
		authoredAt,
		false,
	); verifyErr != nil {
		return PinnedLineSuspendResult{}, verifyErr
	}
	return pinnedLineSuspendResult(record, false), nil
}

func validatePinnedLineSuspendRequest(
	ctx context.Context,
	request PinnedLineSuspendRequest,
) (string, error) {
	repository, err := validatePinnedAcquireRequest(ctx, request.Pin)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPinnedLineInvalid, err)
	}
	if !validPinnedOperationIdentity(request.WorkspaceID, 256) ||
		!validPinnedOperationIdentity(request.LineID, maxDevelopmentLineIdentityBytes) ||
		!validPinnedOperationIdentity(request.IntentID, maxDevelopmentLineIdentityBytes) ||
		request.ExpectedVersion < 0 ||
		request.ExpectedVersion >= maxDevelopmentLineReservations ||
		request.ExpectedMutationEpoch != request.ExpectedVersion+1 ||
		!validPinnedCommit(request.ExpectedTip) || !validPinnedCommit(request.ExpectedTree) ||
		len(request.ExpectedTip) != len(request.Pin.ExpectedCommit) ||
		len(request.ExpectedTree) != len(request.Pin.ExpectedCommit) {
		return "", fmt.Errorf(
			"%w: development line suspension identity is invalid",
			ErrPinnedLineInvalid,
		)
	}
	return repository, nil
}

func pinnedLineSuspensionRequestDigest(
	mode string,
	request PinnedLineSuspendRequest,
	repository string,
	commit *PinnedCommitRequest,
) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pinned-development-line-suspension-request-v1\x00"))
	values := []string{
		mode,
		repository,
		request.Pin.SourceRef,
		request.Pin.ExpectedCommit,
		developmentLineReservationHash(request.Pin.ReservationKey),
		request.Pin.AgentID,
		request.WorkspaceID,
		request.LineID,
		request.IntentID,
		strconv.FormatInt(request.ExpectedVersion, 10),
		strconv.FormatInt(request.ExpectedMutationEpoch, 10),
		request.ExpectedTip,
		request.ExpectedTree,
	}
	if commit != nil {
		values = append(values,
			commit.IntentID,
			commit.ExpectedParent,
			commit.ExpectedTree,
			commit.ExpectedCandidateDigest,
			commit.Message,
			commit.AuthoredAt.UTC().Format(time.RFC3339Nano),
		)
	}
	for _, value := range values {
		writeDevelopmentLineSuspensionHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func (m *Manager) acquireStandalonePinnedLineOperation(
	ctx context.Context,
	reservation string,
) (func(), error) {
	if existing, ok := ctx.Value(pinnedOperationContextKey{}).(*pinnedOperationToken); ok &&
		existing != nil && existing.active.Load() {
		return nil, errors.New(
			"pinned development line suspension must run outside another pinned operation",
		)
	}
	_, inherited, release, err := m.acquirePinnedOperation(ctx, reservation)
	if err != nil {
		return nil, err
	}
	if inherited {
		release()
		return nil, errors.New(
			"pinned development line suspension inherited an operation lock",
		)
	}
	return release, nil
}

func matchPinnedLineSuspensionReplay(
	line *developmentLineRecord,
	request PinnedLineSuspendRequest,
	repository, mode, requestHash, reservationHash string,
) (developmentLineSuspensionRecord, error) {
	if line == nil || line.State != developmentLineSuspended || len(line.Suspensions) == 0 {
		return developmentLineSuspensionRecord{}, fmt.Errorf(
			"%w: suspended development line is unavailable",
			ErrPinnedLineConflict,
		)
	}
	record := line.Suspensions[len(line.Suspensions)-1]
	if record.Mode != mode || record.IntentID != request.IntentID ||
		record.RequestHash != requestHash || record.WorkspaceID != request.WorkspaceID ||
		record.LineID != request.LineID || record.RepoID != repoID(repository) ||
		record.SourceRef != request.Pin.SourceRef ||
		record.SourceCommit != request.Pin.ExpectedCommit ||
		record.Version != request.ExpectedVersion ||
		record.MutationEpoch != request.ExpectedMutationEpoch ||
		record.Tip != request.ExpectedTip || record.Tree != request.ExpectedTree ||
		record.RetiredReservationHash != reservationHash ||
		record.AgentID != request.Pin.AgentID {
		return developmentLineSuspensionRecord{}, fmt.Errorf(
			"%w: suspension replay changed",
			ErrPinnedLineConflict,
		)
	}
	return record, nil
}

func developmentLineSuspensionIntentUsed(state *storeState, intentID string) bool {
	if state == nil || intentID == "" {
		return false
	}
	for _, line := range state.DevelopmentLines {
		if line == nil {
			continue
		}
		for _, record := range line.Suspensions {
			if record.IntentID == intentID {
				return true
			}
		}
	}
	return false
}

func developmentLineSuspensionRequestUsed(state *storeState, requestHash string) bool {
	if state == nil || requestHash == "" {
		return false
	}
	for _, line := range state.DevelopmentLines {
		if line == nil {
			continue
		}
		for _, record := range line.Suspensions {
			if record.RequestHash == requestHash {
				return true
			}
		}
	}
	return false
}

func pinnedLineSuspendResult(
	record developmentLineSuspensionRecord,
	replay bool,
) PinnedLineSuspendResult {
	return PinnedLineSuspendResult{
		WorkspaceID:           record.WorkspaceID,
		Version:               record.Version,
		MutationEpoch:         record.MutationEpoch,
		Tip:                   record.Tip,
		Tree:                  record.Tree,
		CandidateTree:         record.CandidateTree,
		CandidateDigest:       record.CandidateDigest,
		ChangedFileCount:      record.ChangedFileCount,
		SuspensionHash:        record.RecordHash,
		PreparedCommit:        record.PreparedCommit,
		PreparedTree:          record.PreparedTree,
		PreparedCommitApplied: record.PreparedCommitApplied,
		AlreadySuspended:      replay,
	}
}

func (m *Manager) snapshotPinnedLineForSuspension(
	ctx context.Context,
	workspace *WorkspaceRecord,
	line *developmentLineRecord,
	mode string,
	commit *PinnedCommitRequest,
	authoredAt time.Time,
	environment []string,
) (pinnedLineSuspensionSnapshot, error) {
	if workspace == nil || line == nil {
		return pinnedLineSuspensionSnapshot{}, fmt.Errorf(
			"%w: development line suspension owner is missing",
			ErrPinnedLineConflict,
		)
	}
	if operationErr := verifyPinnedCommitOperationState(
		ctx,
		workspace.Path,
		environment,
	); operationErr != nil {
		return pinnedLineSuspensionSnapshot{}, errors.Join(ErrPinnedLineConflict, operationErr)
	}
	if lockErr := rejectPinnedGitLockFiles(workspace.Path); lockErr != nil {
		return pinnedLineSuspensionSnapshot{}, errors.Join(ErrPinnedLineConflict, lockErr)
	}
	if refErr := verifyPinnedLineSuspensionRef(ctx, workspace, line, environment); refErr != nil {
		return pinnedLineSuspensionSnapshot{}, refErr
	}
	parentTree, treeErr := resolvePinnedTree(ctx, workspace.Path, line.Tip, environment)
	if treeErr != nil || parentTree != line.Tree {
		if treeErr == nil {
			treeErr = errors.New("retained development line tree changed")
		}
		return pinnedLineSuspensionSnapshot{}, errors.Join(ErrPinnedLineConflict, treeErr)
	}
	head, headErr := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	if headErr != nil {
		return pinnedLineSuspensionSnapshot{}, headErr
	}
	indexTree, indexErr := resolvePinnedIndexTree(ctx, workspace.Path, environment)
	if indexErr != nil {
		return pinnedLineSuspensionSnapshot{}, indexErr
	}
	preparedCommit := ""
	preparedTree := ""
	applied := false
	switch mode {
	case developmentLineSuspensionCandidate:
		if commit != nil || head != line.Tip || indexTree != line.Tree {
			return pinnedLineSuspensionSnapshot{}, fmt.Errorf(
				"%w: ordinary suspension Git fence changed",
				ErrPinnedLineConflict,
			)
		}
	case developmentLineSuspensionCommitRecovery:
		if commit == nil || commit.ExpectedParent != line.Tip ||
			commit.ExpectedTree == line.Tree {
			return pinnedLineSuspensionSnapshot{}, fmt.Errorf(
				"%w: prepared commit suspension fence is invalid",
				ErrPinnedLineConflict,
			)
		}
		facts, factsErr := pinnedCandidateFacts(
			ctx,
			workspace.Path,
			commit.ExpectedParent,
			commit.ExpectedTree,
			environment,
		)
		if factsErr != nil {
			return pinnedLineSuspensionSnapshot{}, factsErr
		}
		if facts.Digest != commit.ExpectedCandidateDigest {
			return pinnedLineSuspensionSnapshot{}, fmt.Errorf(
				"%w: prepared commit candidate digest changed",
				ErrPinnedLineConflict,
			)
		}
		commitEnvironment := pinnedCommitEnvironment(environment, authoredAt)
		preparedCommit, factsErr = createPinnedCommitObject(
			ctx,
			workspace.Path,
			commit.ExpectedTree,
			commit.ExpectedParent,
			commit.Message,
			commit.IntentID,
			commitEnvironment,
		)
		if factsErr != nil {
			return pinnedLineSuspensionSnapshot{}, factsErr
		}
		if verifyErr := verifyPinnedCommitObject(
			ctx,
			workspace.Path,
			preparedCommit,
			commit.ExpectedTree,
			commit.ExpectedParent,
			commit.Message,
			commit.IntentID,
			authoredAt,
			commitEnvironment,
		); verifyErr != nil {
			return pinnedLineSuspensionSnapshot{}, verifyErr
		}
		preparedTree = commit.ExpectedTree
		if head != line.Tip && head != preparedCommit {
			return pinnedLineSuspensionSnapshot{}, fmt.Errorf(
				"%w: prepared commit HEAD changed",
				ErrPinnedLineConflict,
			)
		}
		if head == line.Tip && indexTree != line.Tree {
			return pinnedLineSuspensionSnapshot{}, fmt.Errorf(
				"%w: unapplied prepared commit index changed",
				ErrPinnedLineConflict,
			)
		}
		if head == preparedCommit && indexTree != line.Tree &&
			indexTree != commit.ExpectedTree {
			return pinnedLineSuspensionSnapshot{}, fmt.Errorf(
				"%w: applied prepared commit index is not recoverable",
				ErrPinnedLineConflict,
			)
		}
		applied = head == preparedCommit
	default:
		return pinnedLineSuspensionSnapshot{}, fmt.Errorf(
			"%w: development line suspension mode is invalid",
			ErrPinnedLineInvalid,
		)
	}
	candidate, candidateErr := m.buildPinnedCandidate(
		ctx,
		workspace.Path,
		line.Tip,
		line.Tip,
		environment,
	)
	if candidateErr != nil {
		return pinnedLineSuspensionSnapshot{}, candidateErr
	}
	if commit != nil && !applied && (candidate.Tree != commit.ExpectedTree ||
		candidate.Digest != commit.ExpectedCandidateDigest) {
		return pinnedLineSuspensionSnapshot{}, fmt.Errorf(
			"%w: unapplied prepared commit candidate changed",
			ErrPinnedLineConflict,
		)
	}
	postHead, postHeadErr := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	postIndex, postIndexErr := resolvePinnedIndexTree(ctx, workspace.Path, environment)
	if postHeadErr != nil || postIndexErr != nil || postHead != head || postIndex != indexTree {
		return pinnedLineSuspensionSnapshot{}, fmt.Errorf(
			"%w: suspension Git state changed during snapshot",
			ErrPinnedLineConflict,
		)
	}
	if operationErr := verifyPinnedCommitOperationState(
		ctx,
		workspace.Path,
		environment,
	); operationErr != nil {
		return pinnedLineSuspensionSnapshot{}, errors.Join(ErrPinnedLineConflict, operationErr)
	}
	if lockErr := rejectPinnedGitLockFiles(workspace.Path); lockErr != nil {
		return pinnedLineSuspensionSnapshot{}, errors.Join(ErrPinnedLineConflict, lockErr)
	}
	if refErr := verifyPinnedLineSuspensionRef(ctx, workspace, line, environment); refErr != nil {
		return pinnedLineSuspensionSnapshot{}, refErr
	}
	return pinnedLineSuspensionSnapshot{
		candidate:      candidate,
		preparedCommit: preparedCommit,
		preparedTree:   preparedTree,
		applied:        applied,
	}, nil
}

func verifyPinnedLineSuspensionRef(
	ctx context.Context,
	workspace *WorkspaceRecord,
	line *developmentLineRecord,
	environment []string,
) error {
	if workspace == nil || line == nil || workspace.DevelopmentLineID != line.ID {
		return fmt.Errorf("%w: retained line owner changed", ErrPinnedLineConflict)
	}
	if layoutErr := validateDevelopmentLineRefLayout(
		workspace.Path,
		line.Branch,
		false,
	); layoutErr != nil {
		return errors.Join(ErrPinnedLineConflict, layoutErr)
	}
	current, found, inspectErr := inspectDevelopmentLineRef(
		ctx,
		workspace.Path,
		line.Branch,
		environment,
	)
	if inspectErr != nil || !found || current != line.Tip {
		if inspectErr == nil {
			inspectErr = errors.New("retained development line ref changed")
		}
		return errors.Join(ErrPinnedLineConflict, inspectErr)
	}
	return nil
}

func (m *Manager) verifyPinnedLineSuspensionRecordFilesystem(
	ctx context.Context,
	workspace *WorkspaceRecord,
	line *developmentLineRecord,
	record developmentLineSuspensionRecord,
	repository string,
	commit *PinnedCommitRequest,
	authoredAt time.Time,
	allowNormalizedParent bool,
) error {
	if workspace == nil || line == nil || workspace.DroppedAt != nil ||
		workspace.LockedBy != nil || workspace.ID != record.WorkspaceID ||
		workspace.DevelopmentLineID != line.ID || workspace.RepoID != record.RepoID ||
		workspace.RemoteURL != repository || workspace.Ref != record.SourceRef ||
		workspace.PinnedSourceRef != record.SourceRef ||
		workspace.PinnedCommit != record.SourceCommit {
		return fmt.Errorf("%w: suspended workspace identity changed", ErrPinnedLineConflict)
	}
	environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
	if environmentErr != nil {
		return environmentErr
	}
	defer cleanup()
	if verifyErr := m.verifyPinnedWorkspace(
		ctx,
		workspace,
		repository,
		record.SourceCommit,
		false,
		environment,
	); verifyErr != nil {
		return errors.Join(ErrPinnedLineConflict, verifyErr)
	}
	if operationErr := verifyPinnedCommitOperationState(
		ctx,
		workspace.Path,
		environment,
	); operationErr != nil {
		return errors.Join(ErrPinnedLineConflict, operationErr)
	}
	if lockErr := rejectPinnedGitLockFiles(workspace.Path); lockErr != nil {
		return errors.Join(ErrPinnedLineConflict, lockErr)
	}
	if refErr := verifyPinnedLineSuspensionRef(ctx, workspace, line, environment); refErr != nil {
		return refErr
	}
	parentTree, treeErr := resolvePinnedTree(ctx, workspace.Path, record.Tip, environment)
	if treeErr != nil || parentTree != record.Tree {
		if treeErr == nil {
			treeErr = errors.New("suspended retained tree changed")
		}
		return errors.Join(ErrPinnedLineConflict, treeErr)
	}
	head, headErr := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	if headErr != nil {
		return headErr
	}
	indexTree, indexErr := resolvePinnedIndexTree(ctx, workspace.Path, environment)
	if indexErr != nil {
		return indexErr
	}
	switch record.Mode {
	case developmentLineSuspensionCandidate:
		if commit != nil || record.PreparedCommit != "" ||
			head != record.Tip || indexTree != record.Tree {
			return fmt.Errorf("%w: suspended candidate Git state changed", ErrPinnedLineConflict)
		}
	case developmentLineSuspensionCommitRecovery:
		if preparedErr := verifyPinnedSuspensionPreparedCommit(
			ctx,
			workspace.Path,
			record,
			environment,
		); preparedErr != nil {
			return preparedErr
		}
		if commit != nil {
			commitEnvironment := pinnedCommitEnvironment(environment, authoredAt)
			intended, intendedErr := createPinnedCommitObject(
				ctx,
				workspace.Path,
				commit.ExpectedTree,
				commit.ExpectedParent,
				commit.Message,
				commit.IntentID,
				commitEnvironment,
			)
			if intendedErr != nil || intended != record.PreparedCommit {
				if intendedErr != nil {
					return intendedErr
				}
				return fmt.Errorf("%w: prepared commit replay changed", ErrPinnedLineConflict)
			}
			if verifyErr := verifyPinnedCommitObject(
				ctx,
				workspace.Path,
				intended,
				commit.ExpectedTree,
				commit.ExpectedParent,
				commit.Message,
				commit.IntentID,
				authoredAt,
				commitEnvironment,
			); verifyErr != nil {
				return verifyErr
			}
		}
		if !record.PreparedCommitApplied {
			if head != record.Tip || indexTree != record.Tree {
				return fmt.Errorf(
					"%w: unapplied prepared commit suspension changed",
					ErrPinnedLineConflict,
				)
			}
		} else if ((!allowNormalizedParent && head != record.PreparedCommit) ||
			(allowNormalizedParent && head != record.Tip && head != record.PreparedCommit)) ||
			(indexTree != record.Tree && indexTree != record.PreparedTree) {
			return fmt.Errorf(
				"%w: applied prepared commit suspension is not recoverable",
				ErrPinnedLineConflict,
			)
		}
	default:
		return fmt.Errorf("%w: suspension mode changed", ErrPinnedLineConflict)
	}
	candidate, candidateErr := m.buildPinnedCandidate(
		ctx,
		workspace.Path,
		record.Tip,
		record.Tip,
		environment,
	)
	if candidateErr != nil {
		return candidateErr
	}
	if candidate.Tree != record.CandidateTree || candidate.Digest != record.CandidateDigest ||
		candidate.ChangedFiles != record.ChangedFileCount {
		return fmt.Errorf("%w: suspended ordinary candidate changed", ErrPinnedLineConflict)
	}
	postHead, postHeadErr := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	postIndex, postIndexErr := resolvePinnedIndexTree(ctx, workspace.Path, environment)
	if postHeadErr != nil || postIndexErr != nil || postHead != head || postIndex != indexTree {
		return fmt.Errorf("%w: suspended Git state changed during verification", ErrPinnedLineConflict)
	}
	if operationErr := verifyPinnedCommitOperationState(
		ctx,
		workspace.Path,
		environment,
	); operationErr != nil {
		return errors.Join(ErrPinnedLineConflict, operationErr)
	}
	if lockErr := rejectPinnedGitLockFiles(workspace.Path); lockErr != nil {
		return errors.Join(ErrPinnedLineConflict, lockErr)
	}
	return verifyPinnedLineSuspensionRef(ctx, workspace, line, environment)
}

func verifyPinnedSuspensionPreparedCommit(
	ctx context.Context,
	directory string,
	record developmentLineSuspensionRecord,
	environment []string,
) error {
	if record.PreparedCommit == "" || record.PreparedCommit == record.Tip ||
		record.PreparedTree == "" || record.PreparedTree == record.Tree {
		return fmt.Errorf("%w: prepared suspension commit is invalid", ErrPinnedLineConflict)
	}
	parents, parentsErr := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"rev-list",
		"--parents",
		"-n",
		"1",
		record.PreparedCommit,
	)
	if parentsErr != nil {
		return parentsErr
	}
	fields := strings.Fields(string(parents))
	if len(fields) != 2 || fields[0] != record.PreparedCommit || fields[1] != record.Tip {
		return fmt.Errorf("%w: prepared suspension ancestry changed", ErrPinnedLineConflict)
	}
	tree, treeErr := resolvePinnedTree(ctx, directory, record.PreparedCommit, environment)
	if treeErr != nil || tree != record.PreparedTree {
		if treeErr != nil {
			return treeErr
		}
		return fmt.Errorf("%w: prepared suspension tree changed", ErrPinnedLineConflict)
	}
	return nil
}

// ResumeSuspendedPinnedLine restores the exact suspended candidate under one
// globally fresh reservation. A prepared Commit that reached HEAD is first
// normalized back to its retained parent without changing ordinary files.
func (m *Manager) ResumeSuspendedPinnedLine(
	ctx context.Context,
	request PinnedLineSuspendedResumeRequest,
) (PinnedLineSuspendedResumeResult, error) {
	if m == nil {
		return PinnedLineSuspendedResumeResult{}, errors.New(
			"git workspace manager is not configured",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, validationErr := validatePinnedLineSuspendedResumeRequest(ctx, request)
	if validationErr != nil {
		return PinnedLineSuspendedResumeResult{}, validationErr
	}
	releaseOperation, operationErr := m.acquireStandalonePinnedLineOperation(
		ctx,
		request.Pin.ReservationKey,
	)
	if operationErr != nil {
		return PinnedLineSuspendedResumeResult{}, operationErr
	}
	defer releaseOperation()

	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, lockErr := m.lockInventory(ctx)
	if lockErr != nil {
		return PinnedLineSuspendedResumeResult{}, lockErr
	}
	defer unlockInventory()
	state, loadErr := m.loadLocked()
	if loadErr != nil {
		return PinnedLineSuspendedResumeResult{}, loadErr
	}
	line := state.DevelopmentLines[request.LineID]
	if line == nil || len(line.Suspensions) == 0 {
		return PinnedLineSuspendedResumeResult{}, fmt.Errorf(
			"%w: suspended development line is missing",
			ErrPinnedLineConflict,
		)
	}
	suspension := line.Suspensions[len(line.Suspensions)-1]
	if matchErr := matchPinnedLineSuspendedResumeFence(
		line,
		suspension,
		request,
		repository,
	); matchErr != nil {
		return PinnedLineSuspendedResumeResult{}, matchErr
	}
	rotations := state.PinnedReservationRotations[request.WorkspaceID]
	if len(rotations) > 0 && rotations[len(rotations)-1].IntentID == request.IntentID {
		rotation := rotations[len(rotations)-1]
		if matchErr := matchPinnedLineSuspendedResumeRotation(
			rotation,
			suspension,
			request,
			repository,
		); matchErr != nil {
			return PinnedLineSuspendedResumeResult{}, matchErr
		}
		if verifyErr := m.verifyResumedPinnedLineCandidate(
			ctx,
			state,
			line,
			suspension,
			request,
			repository,
		); verifyErr != nil {
			return PinnedLineSuspendedResumeResult{}, verifyErr
		}
		return pinnedLineSuspendedResumeResult(suspension, rotation, true), nil
	}
	if pinnedReservationRotationIntentUsed(state, request.IntentID) ||
		developmentLineSuspensionIntentUsed(state, request.IntentID) {
		return PinnedLineSuspendedResumeResult{}, fmt.Errorf(
			"%w: suspended resume intent was already used",
			ErrPinnedLineConflict,
		)
	}
	if line.State != developmentLineSuspended || line.PendingParkSet ||
		line.MutationReservationHash != "" || line.MutationAgentID != "" ||
		line.Version != request.ExpectedVersion ||
		line.MutationEpoch != request.ExpectedMutationEpoch ||
		line.Tip != request.ExpectedTip || line.Tree != request.ExpectedTree ||
		suspension.AgentID != request.Pin.AgentID {
		return PinnedLineSuspendedResumeResult{}, fmt.Errorf(
			"%w: suspended resume fence changed",
			ErrPinnedLineConflict,
		)
	}
	workspace := state.Workspaces[line.WorkspaceID]
	if workspace == nil || workspace.DroppedAt != nil || workspace.LockedBy != nil ||
		workspace.DevelopmentLineID != line.ID ||
		workspace.PinnedReservationRotationCount >= maxPinnedReservationRotations {
		return PinnedLineSuspendedResumeResult{}, fmt.Errorf(
			"%w: suspended resume workspace is unavailable",
			ErrPinnedLineConflict,
		)
	}
	// A lost scheduling owner after this resume may leave Git ahead of its
	// eventing finalization. Preserve one rotation for that exact recovery's
	// later resume and one suspension record for immediately retiring the fresh
	// bearer again before installing it.
	if capacityErr := requirePinnedRecoverySuspensionCapacity(
		state,
		request.WorkspaceID,
		request.LineID,
		false,
		false,
	); capacityErr != nil {
		return PinnedLineSuspendedResumeResult{}, capacityErr
	}
	if freshErr := requireFreshPinnedLineReservation(
		state,
		line.ID,
		request.Pin.ReservationKey,
	); freshErr != nil {
		return PinnedLineSuspendedResumeResult{}, freshErr
	}
	if pinnedReservationRotationRevoked(state, suspension.RetiredReservationHash) {
		return PinnedLineSuspendedResumeResult{}, fmt.Errorf(
			"%w: suspension was already consumed",
			ErrPinnedLineConflict,
		)
	}
	if verifyErr := m.verifyPinnedLineSuspensionRecordFilesystem(
		ctx,
		workspace,
		line,
		suspension,
		repository,
		nil,
		time.Time{},
		true,
	); verifyErr != nil {
		return PinnedLineSuspendedResumeResult{}, verifyErr
	}
	if suspension.PreparedCommitApplied {
		environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
		if environmentErr != nil {
			return PinnedLineSuspendedResumeResult{}, environmentErr
		}
		defer cleanup()
		if reconcileErr := m.reconcilePinnedSuspensionPreparedCommit(
			ctx,
			workspace,
			line,
			suspension,
			environment,
		); reconcileErr != nil {
			return PinnedLineSuspendedResumeResult{}, reconcileErr
		}
	}
	// Re-prove the complete candidate after any crash-replayable HEAD/index
	// normalization and before inventory authority is installed.
	if verifyErr := m.verifyPinnedLineSuspensionRecordFilesystem(
		ctx,
		workspace,
		line,
		suspension,
		repository,
		nil,
		time.Time{},
		true,
	); verifyErr != nil {
		return PinnedLineSuspendedResumeResult{}, verifyErr
	}

	now := m.now().UTC()
	previousRecordHash := emptyPinnedReservationRotationDigest()
	if len(rotations) > 0 {
		previousRecordHash = rotations[len(rotations)-1].RecordHash
	}
	rotation := pinnedReservationRotationRecord{
		IntentID:                request.IntentID,
		WorkspaceID:             workspace.ID,
		LineID:                  line.ID,
		RepoID:                  line.RepoID,
		SourceRef:               line.SourceRef,
		SourceCommit:            line.SourceCommit,
		Version:                 line.Version,
		MutationEpoch:           line.MutationEpoch,
		Tip:                     line.Tip,
		Tree:                    line.Tree,
		SuspensionHash:          suspension.RecordHash,
		PreviousReservationHash: suspension.RetiredReservationHash,
		ReplacementReservationHash: developmentLineReservationHash(
			request.Pin.ReservationKey,
		),
		AgentID:            request.Pin.AgentID,
		PreviousRecordHash: previousRecordHash,
		RotatedAt:          now,
	}
	rotation.RecordHash = pinnedReservationRotationRecordDigest(rotation)
	rotations = append(rotations, rotation)
	state.PinnedReservationRotations[workspace.ID] = rotations
	workspace.PinnedReservationRotationCount = len(rotations)
	workspace.PinnedReservationRotationTailHash = rotation.RecordHash
	workspace.LockedBy = &LockInfo{
		SessionKey:  request.Pin.ReservationKey,
		AgentID:     request.Pin.AgentID,
		LockedAt:    now,
		HeartbeatAt: now,
	}
	workspace.UpdatedAt = now
	workspace.LastWorkAt = now
	line.State = developmentLineMutating
	line.MutationReservationHash = rotation.ReplacementReservationHash
	line.MutationAgentID = request.Pin.AgentID
	line.UpdatedAt = now
	if repositoryRecord := state.Repositories[workspace.RepoID]; repositoryRecord != nil {
		repositoryRecord.LastSeenAt = now
		repositoryRecord.LastWorkAt = now
	}
	m.addHistoryLocked(
		state,
		now,
		"development_line_suspension_resumed",
		workspace.RepoID,
		workspace.ID,
		"",
		"",
		line.Tip,
	)
	if saveErr := m.saveLocked(state); saveErr != nil {
		return PinnedLineSuspendedResumeResult{}, saveErr
	}
	// Fresh ownership is already durable. Re-prove its canonical parent/index
	// form and exact ordinary candidate before reporting success; an ambiguous
	// postflight is resolved only by exact latest-rotation replay.
	if verifyErr := m.verifyResumedPinnedLineCandidate(
		ctx,
		state,
		line,
		suspension,
		request,
		repository,
	); verifyErr != nil {
		return PinnedLineSuspendedResumeResult{}, verifyErr
	}
	return pinnedLineSuspendedResumeResult(suspension, rotation, false), nil
}

func validatePinnedLineSuspendedResumeRequest(
	ctx context.Context,
	request PinnedLineSuspendedResumeRequest,
) (string, error) {
	repository, err := validatePinnedAcquireRequest(ctx, request.Pin)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPinnedLineInvalid, err)
	}
	if !validPinnedOperationIdentity(request.WorkspaceID, 256) ||
		!validPinnedOperationIdentity(request.LineID, maxDevelopmentLineIdentityBytes) ||
		!validPinnedOperationIdentity(request.IntentID, maxDevelopmentLineIdentityBytes) ||
		request.ExpectedVersion < 0 ||
		request.ExpectedVersion >= maxDevelopmentLineReservations ||
		request.ExpectedMutationEpoch != request.ExpectedVersion+1 ||
		!validPinnedCommit(request.ExpectedTip) || !validPinnedCommit(request.ExpectedTree) ||
		len(request.ExpectedTip) != len(request.Pin.ExpectedCommit) ||
		len(request.ExpectedTree) != len(request.Pin.ExpectedCommit) ||
		!validLowerHex(request.SuspensionHash, sha256.Size*2) ||
		!validPinnedCommit(request.CandidateTree) ||
		len(request.CandidateTree) != len(request.Pin.ExpectedCommit) ||
		!validLowerHex(request.CandidateDigest, sha256.Size*2) ||
		request.ChangedFileCount < 0 ||
		request.ChangedFileCount > maxPinnedCandidateChangedFiles {
		return "", fmt.Errorf(
			"%w: suspended resume identity is invalid",
			ErrPinnedLineInvalid,
		)
	}
	return repository, nil
}

func matchPinnedLineSuspendedResumeFence(
	line *developmentLineRecord,
	suspension developmentLineSuspensionRecord,
	request PinnedLineSuspendedResumeRequest,
	repository string,
) error {
	if line == nil || suspension.RecordHash != request.SuspensionHash ||
		suspension.WorkspaceID != request.WorkspaceID ||
		suspension.LineID != request.LineID || suspension.RepoID != repoID(repository) ||
		suspension.SourceRef != request.Pin.SourceRef ||
		suspension.SourceCommit != request.Pin.ExpectedCommit ||
		suspension.Version != request.ExpectedVersion ||
		suspension.MutationEpoch != request.ExpectedMutationEpoch ||
		suspension.Tip != request.ExpectedTip || suspension.Tree != request.ExpectedTree ||
		suspension.CandidateTree != request.CandidateTree ||
		suspension.CandidateDigest != request.CandidateDigest ||
		suspension.ChangedFileCount != request.ChangedFileCount ||
		line.SuspensionTailHash != suspension.RecordHash {
		return fmt.Errorf("%w: suspended resume fence changed", ErrPinnedLineConflict)
	}
	return nil
}

func matchPinnedLineSuspendedResumeRotation(
	rotation pinnedReservationRotationRecord,
	suspension developmentLineSuspensionRecord,
	request PinnedLineSuspendedResumeRequest,
	repository string,
) error {
	if rotation.WorkspaceID != request.WorkspaceID || rotation.LineID != request.LineID ||
		rotation.RepoID != repoID(repository) || rotation.SourceRef != request.Pin.SourceRef ||
		rotation.SourceCommit != request.Pin.ExpectedCommit ||
		rotation.Version != request.ExpectedVersion ||
		rotation.MutationEpoch != request.ExpectedMutationEpoch ||
		rotation.Tip != request.ExpectedTip || rotation.Tree != request.ExpectedTree ||
		rotation.SuspensionHash != suspension.RecordHash ||
		rotation.PreviousReservationHash != suspension.RetiredReservationHash ||
		rotation.ReplacementReservationHash != developmentLineReservationHash(
			request.Pin.ReservationKey,
		) || rotation.AgentID != request.Pin.AgentID {
		return fmt.Errorf("%w: suspended resume replay changed", ErrPinnedLineConflict)
	}
	return nil
}

func (m *Manager) verifyResumedPinnedLineCandidate(
	ctx context.Context,
	state *storeState,
	line *developmentLineRecord,
	suspension developmentLineSuspensionRecord,
	request PinnedLineSuspendedResumeRequest,
	repository string,
) error {
	if line == nil || line.State != developmentLineMutating || line.PendingParkSet ||
		line.Version != suspension.Version || line.MutationEpoch != suspension.MutationEpoch ||
		line.Tip != suspension.Tip || line.Tree != suspension.Tree ||
		line.MutationReservationHash != developmentLineReservationHash(
			request.Pin.ReservationKey,
		) || line.MutationAgentID != request.Pin.AgentID {
		return fmt.Errorf("%w: resumed development line progressed", ErrPinnedLineConflict)
	}
	environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
	if environmentErr != nil {
		return environmentErr
	}
	defer cleanup()
	if verifyErr := m.verifyDevelopmentLineOwnedWorkspace(
		ctx,
		state,
		line,
		request.Pin,
		request.WorkspaceID,
		repository,
		environment,
	); verifyErr != nil {
		return verifyErr
	}
	workspace := state.Workspaces[line.WorkspaceID]
	if operationErr := verifyPinnedCommitOperationState(
		ctx,
		workspace.Path,
		environment,
	); operationErr != nil {
		return errors.Join(ErrPinnedLineConflict, operationErr)
	}
	if lockErr := rejectPinnedGitLockFiles(workspace.Path); lockErr != nil {
		return errors.Join(ErrPinnedLineConflict, lockErr)
	}
	head, headErr := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	indexTree, indexErr := resolvePinnedIndexTree(ctx, workspace.Path, environment)
	if headErr != nil || indexErr != nil || head != line.Tip || indexTree != line.Tree {
		return fmt.Errorf("%w: resumed Git state changed", ErrPinnedLineConflict)
	}
	if suspension.PreparedCommit != "" {
		if preparedErr := verifyPinnedSuspensionPreparedCommit(
			ctx,
			workspace.Path,
			suspension,
			environment,
		); preparedErr != nil {
			return preparedErr
		}
	}
	candidate, candidateErr := m.buildPinnedCandidate(
		ctx,
		workspace.Path,
		line.Tip,
		line.Tip,
		environment,
	)
	if candidateErr != nil {
		return candidateErr
	}
	if candidate.Tree != suspension.CandidateTree ||
		candidate.Digest != suspension.CandidateDigest ||
		candidate.ChangedFiles != suspension.ChangedFileCount {
		return fmt.Errorf("%w: resumed candidate changed", ErrPinnedLineConflict)
	}
	postHead, postHeadErr := resolvePinnedGitCommit(
		ctx,
		workspace.Path,
		"HEAD",
		environment,
	)
	postIndex, postIndexErr := resolvePinnedIndexTree(ctx, workspace.Path, environment)
	if postHeadErr != nil || postIndexErr != nil || postHead != line.Tip ||
		postIndex != line.Tree {
		return fmt.Errorf("%w: resumed Git state changed during verification", ErrPinnedLineConflict)
	}
	if operationErr := verifyPinnedCommitOperationState(
		ctx,
		workspace.Path,
		environment,
	); operationErr != nil {
		return errors.Join(ErrPinnedLineConflict, operationErr)
	}
	if lockErr := rejectPinnedGitLockFiles(workspace.Path); lockErr != nil {
		return errors.Join(ErrPinnedLineConflict, lockErr)
	}
	return verifyPinnedLineSuspensionRef(ctx, workspace, line, environment)
}

func (m *Manager) reconcilePinnedSuspensionPreparedCommit(
	ctx context.Context,
	workspace *WorkspaceRecord,
	line *developmentLineRecord,
	suspension developmentLineSuspensionRecord,
	environment []string,
) error {
	if workspace == nil || line == nil || !suspension.PreparedCommitApplied {
		return fmt.Errorf("%w: applied suspension is unavailable", ErrPinnedLineConflict)
	}
	head, headErr := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	if headErr != nil {
		return headErr
	}
	indexTree, indexErr := resolvePinnedIndexTree(ctx, workspace.Path, environment)
	if indexErr != nil || (indexTree != suspension.Tree &&
		indexTree != suspension.PreparedTree) {
		if indexErr != nil {
			return indexErr
		}
		return fmt.Errorf("%w: applied suspension index changed", ErrPinnedLineConflict)
	}
	switch head {
	case suspension.PreparedCommit:
		updateErr := updatePinnedDetachedHEAD(
			ctx,
			workspace.Path,
			suspension.Tip,
			suspension.PreparedCommit,
			environment,
		)
		if updateErr != nil {
			postCtx, cancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				pinnedCommitPostflightTimeout,
			)
			postHead, postErr := resolvePinnedGitCommit(
				postCtx,
				workspace.Path,
				"HEAD",
				environment,
			)
			cancel()
			if postErr != nil || postHead != suspension.Tip {
				if postErr != nil {
					return errors.Join(updateErr, postErr)
				}
				return updateErr
			}
		}
	case suspension.Tip:
		// Exact replay after the reverse HEAD CAS and before the index reset.
	default:
		return fmt.Errorf("%w: applied suspension HEAD changed", ErrPinnedLineConflict)
	}
	if _, readErr := runPinnedGitPlumbing(
		ctx,
		workspace.Path,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"read-tree",
		"--reset",
		suspension.Tip,
	); readErr != nil {
		return readErr
	}
	postHead, postHeadErr := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	postIndex, postIndexErr := resolvePinnedIndexTree(ctx, workspace.Path, environment)
	if postHeadErr != nil || postIndexErr != nil || postHead != suspension.Tip ||
		postIndex != suspension.Tree {
		return fmt.Errorf("%w: applied suspension normalization failed", ErrPinnedLineConflict)
	}
	candidate, candidateErr := m.buildPinnedCandidate(
		ctx,
		workspace.Path,
		suspension.Tip,
		suspension.Tip,
		environment,
	)
	if candidateErr != nil || candidate.Tree != suspension.CandidateTree ||
		candidate.Digest != suspension.CandidateDigest ||
		candidate.ChangedFiles != suspension.ChangedFileCount {
		if candidateErr != nil {
			return candidateErr
		}
		return fmt.Errorf("%w: normalization changed ordinary files", ErrPinnedLineConflict)
	}
	return verifyPinnedLineSuspensionRef(ctx, workspace, line, environment)
}

func pinnedLineSuspendedResumeResult(
	suspension developmentLineSuspensionRecord,
	rotation pinnedReservationRotationRecord,
	replay bool,
) PinnedLineSuspendedResumeResult {
	return PinnedLineSuspendedResumeResult{
		WorkspaceID:      suspension.WorkspaceID,
		Version:          suspension.Version,
		MutationEpoch:    suspension.MutationEpoch,
		Tip:              suspension.Tip,
		Tree:             suspension.Tree,
		CandidateTree:    suspension.CandidateTree,
		CandidateDigest:  suspension.CandidateDigest,
		ChangedFileCount: suspension.ChangedFileCount,
		SuspensionHash:   suspension.RecordHash,
		RotationHash:     rotation.RecordHash,
		AlreadyResumed:   replay,
	}
}
