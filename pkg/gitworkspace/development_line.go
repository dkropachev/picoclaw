package gitworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	developmentLineParked    = "parked"
	developmentLineMutating  = "mutating"
	developmentLineSuspended = "suspended"

	maxDevelopmentLineIdentityBytes = 256
	maxDevelopmentLineReviewFiles   = 1_000
	maxDevelopmentLineReservations  = 8_192
	maxDevelopmentLinePathBytes     = 4_096
	maxDevelopmentLinePathsBytes    = 256 << 10
	maxDevelopmentLineDiffBytes     = 512 << 10
	developmentLinePostflight       = 30 * time.Second
	developmentLineReviewTimeout    = 30 * time.Second
)

var (
	// ErrPinnedLineInvalid reports malformed controller-owned line evidence.
	ErrPinnedLineInvalid = errors.New("invalid pinned development line request")
	// ErrPinnedLineConflict reports stale, changed, or corrupt line state.
	ErrPinnedLineConflict = errors.New("pinned development line conflict")
)

// developmentLineRecord is private manager inventory. MutationReservationHash
// fences a short-lived edit lease without retaining its bearer value in the
// line record; WorkspaceRecord.LockedBy remains the authoritative live owner.
type developmentLineRecord struct {
	ID                         string                            `json:"id"`
	WorkspaceID                string                            `json:"workspace_id"`
	RepoID                     string                            `json:"repo_id"`
	SourceRef                  string                            `json:"source_ref"`
	SourceCommit               string                            `json:"source_commit"`
	Branch                     string                            `json:"branch"`
	Tip                        string                            `json:"tip"`
	Tree                       string                            `json:"tree"`
	Version                    int64                             `json:"version"`
	MutationEpoch              int64                             `json:"mutation_epoch"`
	State                      string                            `json:"state"`
	MutationReservationHash    string                            `json:"mutation_reservation_hash,omitempty"`
	MutationAgentID            string                            `json:"mutation_agent_id,omitempty"`
	RetiredReservationHashes   []string                          `json:"retired_reservation_hashes,omitempty"`
	SuspensionCount            int                               `json:"suspension_count"`
	SuspensionTailHash         string                            `json:"suspension_tail_hash"`
	Suspensions                []developmentLineSuspensionRecord `json:"suspensions,omitempty"`
	PendingParkSet             bool                              `json:"pending_park_set,omitempty"`
	PendingParkIntentID        string                            `json:"pending_park_intent_id,omitempty"`
	PendingParkReservationHash string                            `json:"pending_park_reservation_hash,omitempty"`
	PendingParkAgentID         string                            `json:"pending_park_agent_id,omitempty"`
	PendingParkEpoch           int64                             `json:"pending_park_epoch,omitempty"`
	PendingParkExpectedVersion int64                             `json:"pending_park_expected_version,omitempty"`
	PendingParkPreviousTip     string                            `json:"pending_park_previous_tip,omitempty"`
	PendingParkTip             string                            `json:"pending_park_tip,omitempty"`
	PendingParkTree            string                            `json:"pending_park_tree,omitempty"`
	PendingParkNoChanges       bool                              `json:"pending_park_no_changes,omitempty"`
	LastParkIntentID           string                            `json:"last_park_intent_id,omitempty"`
	LastParkReservationHash    string                            `json:"last_park_reservation_hash,omitempty"`
	LastParkAgentID            string                            `json:"last_park_agent_id,omitempty"`
	LastParkEpoch              int64                             `json:"last_park_epoch,omitempty"`
	LastParkExpectedVersion    int64                             `json:"last_park_expected_version,omitempty"`
	LastParkPreviousTip        string                            `json:"last_park_previous_tip,omitempty"`
	LastParkTip                string                            `json:"last_park_tip,omitempty"`
	LastParkTree               string                            `json:"last_park_tree,omitempty"`
	CreatedAt                  time.Time                         `json:"created_at"`
	UpdatedAt                  time.Time                         `json:"updated_at"`
}

// PinnedLineAdoptRequest converts one freshly acquired, clean pinned checkout
// into a durable controller-owned development line without releasing its
// current mutation reservation.
type PinnedLineAdoptRequest struct {
	Pin          PinnedAcquireRequest
	WorkspaceID  string
	LineID       string
	ExpectedTree string
}

// PinnedLineResumeRequest acquires one parked line under a fresh mutation
// reservation. ExpectedEpoch is the most recently issued mutation epoch.
type PinnedLineResumeRequest struct {
	Pin             PinnedAcquireRequest
	WorkspaceID     string
	LineID          string
	ExpectedVersion int64
	ExpectedEpoch   int64
	ExpectedTip     string
	ExpectedTree    string
}

// PinnedLineParkRequest advances one line by exactly one clean local commit,
// or explicitly records a no-change attempt, before releasing its mutation
// reservation. IntentID is caller-durable and makes ambiguous retries exact.
type PinnedLineParkRequest struct {
	Pin             PinnedAcquireRequest
	WorkspaceID     string
	LineID          string
	IntentID        string
	ExpectedVersion int64
	MutationEpoch   int64
	PreviousTip     string
	Tip             string
	Tree            string
	NoChanges       bool
}

// PinnedLineLease is controller-only evidence that one mutation reservation
// owns the exact line version and tip. It exposes no path or internal branch.
type PinnedLineLease struct {
	WorkspaceID   string `json:"workspace_id"`
	Version       int64  `json:"version"`
	MutationEpoch int64  `json:"mutation_epoch"`
	Tip           string `json:"tip"`
	Tree          string `json:"tree"`
	AlreadyOwned  bool   `json:"already_owned"`
}

// PinnedLineParkResult proves the retained commit and released mutation lease.
// The stable internal branch and reservation identity are intentionally absent.
type PinnedLineParkResult struct {
	WorkspaceID    string
	Version        int64
	MutationEpoch  int64
	PreviousTip    string
	Tip            string
	Tree           string
	NoChanges      bool
	AlreadyParked  bool
	WorkspaceClean bool
}

// PinnedLineReviewRequest pins a bounded read-only review projection to one
// parked line version. The caller cannot select a filesystem path or Git ref.
type PinnedLineReviewRequest struct {
	LineID          string
	ExpectedVersion int64
	ExpectedBase    string
	ExpectedTip     string
	ExpectedTree    string
}

// PinnedLineReviewSnapshot contains only commit-addressed repository evidence.
// Paths and diff text are untrusted repository data and remain strictly bounded.
type PinnedLineReviewSnapshot struct {
	Version       int64
	MutationEpoch int64
	ParkIntentID  string
	BaseCommit    string
	Commit        string
	Tree          string
	ChangedPaths  []string
	UnifiedDiff   string
	ReviewDigest  string
}

// AdoptPinnedLine installs one stable reachability ref and durable line owner
// while retaining the current mutation reservation. Exact retries reconcile a
// ref created before an ambiguous inventory write.
func (m *Manager) AdoptPinnedLine(
	ctx context.Context,
	request PinnedLineAdoptRequest,
) (PinnedLineLease, error) {
	if m == nil {
		return PinnedLineLease{}, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, validationErr := validatePinnedLineAdoptRequest(ctx, request)
	if validationErr != nil {
		return PinnedLineLease{}, validationErr
	}
	_, _, releaseOperation, operationErr := m.acquirePinnedOperation(
		ctx,
		request.Pin.ReservationKey,
	)
	if operationErr != nil {
		return PinnedLineLease{}, operationErr
	}
	defer releaseOperation()

	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, lockErr := m.lockInventory(ctx)
	if lockErr != nil {
		return PinnedLineLease{}, lockErr
	}
	defer unlockInventory()
	state, loadErr := m.loadLocked()
	if loadErr != nil {
		return PinnedLineLease{}, loadErr
	}
	reservationHash := developmentLineReservationHash(request.Pin.ReservationKey)
	if existing := state.DevelopmentLines[request.LineID]; existing != nil {
		if existing.PendingParkSet {
			return PinnedLineLease{}, fmt.Errorf(
				"%w: development line has a pending park",
				ErrPinnedLineConflict,
			)
		}
		if matchErr := matchAdoptedPinnedLine(
			state,
			existing,
			request,
			repository,
			reservationHash,
		); matchErr != nil {
			return PinnedLineLease{}, matchErr
		}
		environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
		if environmentErr != nil {
			return PinnedLineLease{}, environmentErr
		}
		defer cleanup()
		if verifyErr := m.verifyDevelopmentLineOwnedWorkspace(
			ctx,
			state,
			existing,
			request.Pin,
			request.WorkspaceID,
			repository,
			environment,
		); verifyErr != nil {
			return PinnedLineLease{}, verifyErr
		}
		return pinnedLineLease(existing, true), nil
	}
	for _, line := range state.DevelopmentLines {
		if line != nil && line.WorkspaceID == request.WorkspaceID {
			return PinnedLineLease{}, fmt.Errorf(
				"%w: workspace already belongs to another development line",
				ErrPinnedLineConflict,
			)
		}
	}

	environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
	if environmentErr != nil {
		return PinnedLineLease{}, environmentErr
	}
	defer cleanup()
	workspace, workspaceErr := m.pinnedWorkspaceForOperation(
		ctx,
		state,
		request.Pin,
		request.WorkspaceID,
		repository,
		environment,
	)
	if workspaceErr != nil {
		return PinnedLineLease{}, fmt.Errorf("%w: %v", ErrPinnedLineConflict, workspaceErr)
	}
	if workspace.DevelopmentLineID != "" {
		return PinnedLineLease{}, fmt.Errorf(
			"%w: workspace already has a development line owner",
			ErrPinnedLineConflict,
		)
	}
	if !validControllerPinnedWorkspaceID(workspace.RepoID, workspace.ID) {
		return PinnedLineLease{}, fmt.Errorf(
			"%w: pinned workspace predates the private controller namespace",
			ErrPinnedLineConflict,
		)
	}
	if verifyErr := verifyPinnedLineCommitState(
		ctx,
		workspace,
		request.Pin.ExpectedCommit,
		request.ExpectedTree,
		environment,
	); verifyErr != nil {
		return PinnedLineLease{}, verifyErr
	}
	branch := developmentLineBranch(request.LineID)
	if _, advanceErr := m.advanceDevelopmentLineRef(
		ctx,
		workspace,
		branch,
		request.Pin.ExpectedCommit,
		request.Pin.ExpectedCommit,
		true,
		environment,
	); advanceErr != nil {
		return PinnedLineLease{}, advanceErr
	}
	now := m.now().UTC()
	line := &developmentLineRecord{
		ID:                      request.LineID,
		WorkspaceID:             workspace.ID,
		RepoID:                  workspace.RepoID,
		SourceRef:               request.Pin.SourceRef,
		SourceCommit:            request.Pin.ExpectedCommit,
		Branch:                  branch,
		Tip:                     request.Pin.ExpectedCommit,
		Tree:                    request.ExpectedTree,
		Version:                 0,
		MutationEpoch:           1,
		State:                   developmentLineMutating,
		MutationReservationHash: reservationHash,
		MutationAgentID:         request.Pin.AgentID,
		SuspensionTailHash:      emptyDevelopmentLineSuspensionDigest(),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	workspace.DevelopmentLineID = request.LineID
	workspace.UpdatedAt = now
	state.DevelopmentLines[request.LineID] = line
	m.addHistoryLocked(
		state,
		now,
		"development_line_adopted",
		workspace.RepoID,
		workspace.ID,
		"",
		"",
		request.Pin.ExpectedCommit,
	)
	if saveErr := m.saveLocked(state); saveErr != nil {
		return PinnedLineLease{}, saveErr
	}
	return pinnedLineLease(line, false), nil
}

// ResumePinnedLine installs one fresh mutation reservation on the exact parked
// line. It never fetches, reclones, resets, cleans, or recreates missing state.
func (m *Manager) ResumePinnedLine(
	ctx context.Context,
	request PinnedLineResumeRequest,
) (PinnedLineLease, error) {
	if m == nil {
		return PinnedLineLease{}, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, validationErr := validatePinnedLineResumeRequest(ctx, request)
	if validationErr != nil {
		return PinnedLineLease{}, validationErr
	}
	_, _, releaseOperation, operationErr := m.acquirePinnedOperation(
		ctx,
		request.Pin.ReservationKey,
	)
	if operationErr != nil {
		return PinnedLineLease{}, operationErr
	}
	defer releaseOperation()

	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, lockErr := m.lockInventory(ctx)
	if lockErr != nil {
		return PinnedLineLease{}, lockErr
	}
	defer unlockInventory()
	state, loadErr := m.loadLocked()
	if loadErr != nil {
		return PinnedLineLease{}, loadErr
	}
	line := state.DevelopmentLines[request.LineID]
	if line == nil {
		return PinnedLineLease{}, fmt.Errorf(
			"%w: development line is missing",
			ErrPinnedLineConflict,
		)
	}
	reservationHash := developmentLineReservationHash(request.Pin.ReservationKey)
	if line.PendingParkSet {
		return PinnedLineLease{}, fmt.Errorf(
			"%w: development line has a pending park",
			ErrPinnedLineConflict,
		)
	}
	if line.State == developmentLineMutating &&
		line.MutationEpoch == request.ExpectedEpoch+1 &&
		line.MutationReservationHash == reservationHash {
		if matchErr := matchPinnedLineResumeIdentity(line, request, repository); matchErr != nil {
			return PinnedLineLease{}, matchErr
		}
		environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
		if environmentErr != nil {
			return PinnedLineLease{}, environmentErr
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
			return PinnedLineLease{}, verifyErr
		}
		return pinnedLineLease(line, true), nil
	}
	if line.State != developmentLineParked ||
		line.Version != request.ExpectedVersion ||
		line.MutationEpoch != request.ExpectedEpoch ||
		line.Tip != request.ExpectedTip || line.Tree != request.ExpectedTree ||
		developmentLineReservationRetired(line, reservationHash) {
		return PinnedLineLease{}, fmt.Errorf(
			"%w: development line resume fence changed",
			ErrPinnedLineConflict,
		)
	}
	if matchErr := matchPinnedLineResumeIdentity(line, request, repository); matchErr != nil {
		return PinnedLineLease{}, matchErr
	}
	if reservationErr := requireFreshPinnedLineReservation(
		state,
		request.LineID,
		request.Pin.ReservationKey,
	); reservationErr != nil {
		return PinnedLineLease{}, reservationErr
	}
	workspace := state.Workspaces[line.WorkspaceID]
	if workspace == nil || workspace.DroppedAt != nil || workspace.LockedBy != nil {
		return PinnedLineLease{}, fmt.Errorf(
			"%w: parked development workspace is unavailable",
			ErrPinnedLineConflict,
		)
	}
	environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
	if environmentErr != nil {
		return PinnedLineLease{}, environmentErr
	}
	defer cleanup()
	if verifyErr := m.verifyDevelopmentLineParkedWorkspace(
		ctx,
		workspace,
		line,
		repository,
		request.ExpectedTip,
		request.ExpectedTree,
		environment,
	); verifyErr != nil {
		return PinnedLineLease{}, verifyErr
	}
	now := m.now().UTC()
	workspace.LockedBy = &LockInfo{
		SessionKey:  request.Pin.ReservationKey,
		AgentID:     request.Pin.AgentID,
		LockedAt:    now,
		HeartbeatAt: now,
	}
	workspace.UpdatedAt = now
	workspace.LastWorkAt = now
	line.State = developmentLineMutating
	line.MutationEpoch++
	line.MutationReservationHash = reservationHash
	line.MutationAgentID = request.Pin.AgentID
	line.UpdatedAt = now
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
	if saveErr := m.saveLocked(state); saveErr != nil {
		return PinnedLineLease{}, saveErr
	}
	return pinnedLineLease(line, false), nil
}

// ParkPinnedLine advances the stable line ref and only then atomically clears
// the current mutation reservation in inventory. A ref-ahead/inventory-behind
// crash is reconciled by repeating the exact caller-durable intent.
func (m *Manager) ParkPinnedLine(
	ctx context.Context,
	request PinnedLineParkRequest,
) (PinnedLineParkResult, error) {
	if m == nil {
		return PinnedLineParkResult{}, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, validationErr := validatePinnedLineParkRequest(ctx, request)
	if validationErr != nil {
		return PinnedLineParkResult{}, validationErr
	}
	_, inheritedOperation, releaseOperation, operationErr := m.acquirePinnedOperation(
		ctx,
		request.Pin.ReservationKey,
	)
	if operationErr != nil {
		return PinnedLineParkResult{}, operationErr
	}
	defer releaseOperation()
	if inheritedOperation {
		return PinnedLineParkResult{}, fmt.Errorf(
			"%w: park must run after the outer mutation operation returns",
			ErrPinnedLineConflict,
		)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, lockErr := m.lockInventory(ctx)
	if lockErr != nil {
		return PinnedLineParkResult{}, lockErr
	}
	defer unlockInventory()
	state, loadErr := m.loadLocked()
	if loadErr != nil {
		return PinnedLineParkResult{}, loadErr
	}
	line := state.DevelopmentLines[request.LineID]
	if line == nil {
		return PinnedLineParkResult{}, fmt.Errorf(
			"%w: development line is missing",
			ErrPinnedLineConflict,
		)
	}
	reservationHash := developmentLineReservationHash(request.Pin.ReservationKey)
	if line.State == developmentLineParked {
		if replayErr := matchParkedPinnedLineReplay(
			line,
			request,
			repository,
			reservationHash,
		); replayErr != nil {
			return PinnedLineParkResult{}, replayErr
		}
		workspace := state.Workspaces[line.WorkspaceID]
		environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
		if environmentErr != nil {
			return PinnedLineParkResult{}, environmentErr
		}
		defer cleanup()
		if workspace == nil || workspace.LockedBy != nil {
			return PinnedLineParkResult{}, fmt.Errorf(
				"%w: exactly parked workspace is unavailable",
				ErrPinnedLineConflict,
			)
		}
		if verifyErr := m.verifyDevelopmentLineParkedWorkspace(
			ctx,
			workspace,
			line,
			repository,
			request.Tip,
			request.Tree,
			environment,
		); verifyErr != nil {
			return PinnedLineParkResult{}, verifyErr
		}
		return pinnedLineParkResult(line, true, request.NoChanges), nil
	}
	if line.State != developmentLineMutating ||
		line.Version != request.ExpectedVersion ||
		line.MutationEpoch != request.MutationEpoch ||
		line.MutationReservationHash != reservationHash ||
		line.MutationAgentID != request.Pin.AgentID ||
		line.Tip != request.PreviousTip {
		return PinnedLineParkResult{}, fmt.Errorf(
			"%w: development line park fence changed",
			ErrPinnedLineConflict,
		)
	}
	if len(line.RetiredReservationHashes) >= maxDevelopmentLineReservations {
		return PinnedLineParkResult{}, fmt.Errorf(
			"%w: development line reservation history is full",
			ErrPinnedLineConflict,
		)
	}
	if line.LastParkIntentID == request.IntentID {
		return PinnedLineParkResult{}, fmt.Errorf(
			"%w: park intent was already consumed",
			ErrPinnedLineConflict,
		)
	}
	if matchErr := matchPinnedLineSource(
		line,
		request.Pin,
		request.WorkspaceID,
		repository,
	); matchErr != nil {
		return PinnedLineParkResult{}, matchErr
	}
	environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
	if environmentErr != nil {
		return PinnedLineParkResult{}, environmentErr
	}
	defer cleanup()
	workspace, workspaceErr := m.pinnedWorkspaceForOperation(
		ctx,
		state,
		request.Pin,
		request.WorkspaceID,
		repository,
		environment,
	)
	if workspaceErr != nil {
		return PinnedLineParkResult{}, fmt.Errorf(
			"%w: %v",
			ErrPinnedLineConflict,
			workspaceErr,
		)
	}
	if workspace.DevelopmentLineID != request.LineID {
		return PinnedLineParkResult{}, fmt.Errorf(
			"%w: workspace line owner changed",
			ErrPinnedLineConflict,
		)
	}
	if verifyErr := verifyPinnedLineCommitState(
		ctx,
		workspace,
		request.Tip,
		request.Tree,
		environment,
	); verifyErr != nil {
		return PinnedLineParkResult{}, verifyErr
	}
	if advanceValidationErr := verifyPinnedLineAdvance(
		ctx,
		workspace.Path,
		request.PreviousTip,
		request.Tip,
		request.NoChanges,
		environment,
	); advanceValidationErr != nil {
		return PinnedLineParkResult{}, advanceValidationErr
	}
	if line.PendingParkSet {
		if pendingErr := matchPendingPinnedLinePark(
			line,
			request,
			reservationHash,
		); pendingErr != nil {
			return PinnedLineParkResult{}, pendingErr
		}
	} else {
		installPendingPinnedLinePark(line, request, reservationHash, m.now().UTC())
		if saveErr := m.saveLocked(state); saveErr != nil {
			return PinnedLineParkResult{}, saveErr
		}
	}

	refAdvanced, advanceErr := m.advanceDevelopmentLineRef(
		ctx,
		workspace,
		line.Branch,
		request.PreviousTip,
		request.Tip,
		false,
		environment,
	)
	postCtx := ctx
	var postCancel context.CancelFunc
	if advanceErr != nil {
		postCtx, postCancel = context.WithTimeout(
			context.WithoutCancel(ctx),
			developmentLinePostflight,
		)
		defer postCancel()
		current, found, inspectErr := inspectDevelopmentLineRef(
			postCtx,
			workspace.Path,
			line.Branch,
			environment,
		)
		if inspectErr != nil || !found || current != request.Tip {
			return PinnedLineParkResult{}, advanceErr
		}
		if layoutErr := validateDevelopmentLineRefLayout(
			workspace.Path,
			line.Branch,
			false,
		); layoutErr != nil {
			return PinnedLineParkResult{}, errors.Join(ErrPinnedLineConflict, layoutErr)
		}
		if syncErr := syncDevelopmentLineRef(
			workspace.Path,
			line.Branch,
			request.Tip,
		); syncErr != nil {
			return PinnedLineParkResult{}, errors.Join(ErrPinnedLineConflict, syncErr)
		}
		refAdvanced = true
	}
	if !refAdvanced && ctx.Err() != nil {
		return PinnedLineParkResult{}, ctx.Err()
	}
	if refAdvanced && postCtx == ctx {
		postCtx, postCancel = context.WithTimeout(
			context.WithoutCancel(ctx),
			developmentLinePostflight,
		)
		defer postCancel()
	}
	if verifyErr := verifyPinnedLineCommitState(
		postCtx,
		workspace,
		request.Tip,
		request.Tree,
		environment,
	); verifyErr != nil {
		return PinnedLineParkResult{}, errors.Join(ErrPinnedLineConflict, verifyErr)
	}
	current, found, inspectErr := inspectDevelopmentLineRef(
		postCtx,
		workspace.Path,
		line.Branch,
		environment,
	)
	if inspectErr != nil || !found || current != request.Tip {
		if inspectErr == nil {
			inspectErr = errors.New("retained development line ref does not match parked commit")
		}
		return PinnedLineParkResult{}, errors.Join(ErrPinnedLineConflict, inspectErr)
	}
	if layoutErr := validateDevelopmentLineRefLayout(
		workspace.Path,
		line.Branch,
		false,
	); layoutErr != nil {
		return PinnedLineParkResult{}, errors.Join(ErrPinnedLineConflict, layoutErr)
	}
	if syncErr := syncDevelopmentLineRef(
		workspace.Path,
		line.Branch,
		request.Tip,
	); syncErr != nil {
		return PinnedLineParkResult{}, errors.Join(ErrPinnedLineConflict, syncErr)
	}

	now := m.now().UTC()
	line.Tip = request.Tip
	line.Tree = request.Tree
	line.Version++
	line.State = developmentLineParked
	line.LastParkIntentID = request.IntentID
	line.LastParkReservationHash = reservationHash
	line.LastParkAgentID = request.Pin.AgentID
	line.LastParkEpoch = request.MutationEpoch
	line.LastParkExpectedVersion = request.ExpectedVersion
	line.LastParkPreviousTip = request.PreviousTip
	line.LastParkTip = request.Tip
	line.LastParkTree = request.Tree
	line.MutationReservationHash = ""
	line.MutationAgentID = ""
	clearPendingPinnedLinePark(line)
	line.RetiredReservationHashes = append(
		line.RetiredReservationHashes,
		reservationHash,
	)
	line.UpdatedAt = now
	workspace.LockedBy = nil
	workspace.UpdatedAt = now
	workspace.LastWorkAt = now
	if repositoryRecord := state.Repositories[workspace.RepoID]; repositoryRecord != nil {
		repositoryRecord.LastSeenAt = now
		repositoryRecord.LastWorkAt = now
	}
	m.addHistoryLocked(
		state,
		now,
		"development_line_parked",
		workspace.RepoID,
		workspace.ID,
		"",
		"",
		request.Tip,
	)
	if saveErr := m.saveLocked(state); saveErr != nil {
		return PinnedLineParkResult{}, saveErr
	}
	return pinnedLineParkResult(line, false, request.NoChanges), nil
}

// PreviewPinnedLineReview validates one proposed park and returns the exact
// bounded review snapshot that it will expose after ParkPinnedLine succeeds.
// It holds (but never releases) the mutation reservation and does not update a
// ref, inventory record, worktree file, or Git index. It may run inside an
// existing WithPinnedOperation callback.
func (m *Manager) PreviewPinnedLineReview(
	ctx context.Context,
	request PinnedLineParkRequest,
) (PinnedLineReviewSnapshot, error) {
	if m == nil {
		return PinnedLineReviewSnapshot{}, errors.New(
			"git workspace manager is not configured",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, validationErr := validatePinnedLineParkRequest(ctx, request)
	if validationErr != nil {
		return PinnedLineReviewSnapshot{}, validationErr
	}
	reviewCtx, cancelReview := context.WithTimeout(ctx, developmentLineReviewTimeout)
	defer cancelReview()
	ctx = reviewCtx
	_, _, releaseOperation, operationErr := m.acquirePinnedOperation(
		ctx,
		request.Pin.ReservationKey,
	)
	if operationErr != nil {
		return PinnedLineReviewSnapshot{}, operationErr
	}
	defer releaseOperation()

	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, lockErr := m.lockInventory(ctx)
	if lockErr != nil {
		return PinnedLineReviewSnapshot{}, lockErr
	}
	defer unlockInventory()
	state, loadErr := m.loadLocked()
	if loadErr != nil {
		return PinnedLineReviewSnapshot{}, loadErr
	}
	line := state.DevelopmentLines[request.LineID]
	reservationHash := developmentLineReservationHash(request.Pin.ReservationKey)
	if line == nil || line.State != developmentLineMutating || line.PendingParkSet ||
		line.Version != request.ExpectedVersion ||
		line.MutationEpoch != request.MutationEpoch ||
		line.MutationReservationHash != reservationHash ||
		line.MutationAgentID != request.Pin.AgentID ||
		line.Tip != request.PreviousTip {
		return PinnedLineReviewSnapshot{}, fmt.Errorf(
			"%w: development line preview fence changed",
			ErrPinnedLineConflict,
		)
	}
	if len(line.RetiredReservationHashes) >= maxDevelopmentLineReservations ||
		line.LastParkIntentID == request.IntentID {
		return PinnedLineReviewSnapshot{}, fmt.Errorf(
			"%w: proposed park cannot be consumed",
			ErrPinnedLineConflict,
		)
	}
	if matchErr := matchPinnedLineSource(
		line,
		request.Pin,
		request.WorkspaceID,
		repository,
	); matchErr != nil {
		return PinnedLineReviewSnapshot{}, matchErr
	}
	environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
	if environmentErr != nil {
		return PinnedLineReviewSnapshot{}, environmentErr
	}
	defer cleanup()

	verifyProposedPark := func() (*WorkspaceRecord, error) {
		workspace, workspaceErr := m.pinnedWorkspaceForOperation(
			ctx,
			state,
			request.Pin,
			request.WorkspaceID,
			repository,
			environment,
		)
		if workspaceErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrPinnedLineConflict, workspaceErr)
		}
		if workspace.DevelopmentLineID != request.LineID {
			return nil, fmt.Errorf(
				"%w: workspace line owner changed",
				ErrPinnedLineConflict,
			)
		}
		if verifyErr := verifyPinnedLineCommitState(
			ctx,
			workspace,
			request.Tip,
			request.Tree,
			environment,
		); verifyErr != nil {
			return nil, verifyErr
		}
		if advanceErr := verifyPinnedLineAdvance(
			ctx,
			workspace.Path,
			request.PreviousTip,
			request.Tip,
			request.NoChanges,
			environment,
		); advanceErr != nil {
			return nil, advanceErr
		}
		if layoutErr := validateDevelopmentLineRefLayout(
			workspace.Path,
			line.Branch,
			false,
		); layoutErr != nil {
			return nil, errors.Join(ErrPinnedLineConflict, layoutErr)
		}
		current, found, inspectErr := inspectDevelopmentLineRef(
			ctx,
			workspace.Path,
			line.Branch,
			environment,
		)
		if inspectErr != nil || !found || current != request.PreviousTip {
			if inspectErr == nil {
				inspectErr = errors.New("development line ref changed before preview")
			}
			return nil, errors.Join(ErrPinnedLineConflict, inspectErr)
		}
		return workspace, nil
	}
	workspace, verifyErr := verifyProposedPark()
	if verifyErr != nil {
		return PinnedLineReviewSnapshot{}, verifyErr
	}
	paths, canonicalDiff, reviewErr := readPinnedLineReview(
		ctx,
		workspace.Path,
		request.PreviousTip,
		request.Tip,
		environment,
	)
	if reviewErr != nil {
		return PinnedLineReviewSnapshot{}, reviewErr
	}
	if _, verifyErr = verifyProposedPark(); verifyErr != nil {
		return PinnedLineReviewSnapshot{}, verifyErr
	}

	prospective := *line
	prospective.Version = line.Version + 1
	prospective.LastParkIntentID = request.IntentID
	prospective.LastParkEpoch = request.MutationEpoch
	prospective.LastParkPreviousTip = request.PreviousTip
	prospective.LastParkTip = request.Tip
	prospective.LastParkTree = request.Tree
	prospective.Tip = request.Tip
	prospective.Tree = request.Tree
	return newPinnedLineReviewSnapshot(
		&prospective,
		request.PreviousTip,
		request.Tip,
		request.Tree,
		paths,
		canonicalDiff,
	), nil
}

// SnapshotPinnedLineReview returns one bounded unified diff from the previous
// parked tip to the exact current parked commit. It reads Git objects only and
// grants no worktree, process, network, or lifecycle capability to the caller.
func (m *Manager) SnapshotPinnedLineReview(
	ctx context.Context,
	request PinnedLineReviewRequest,
) (PinnedLineReviewSnapshot, error) {
	if m == nil {
		return PinnedLineReviewSnapshot{}, errors.New(
			"git workspace manager is not configured",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if validationErr := validatePinnedLineReviewRequest(request); validationErr != nil {
		return PinnedLineReviewSnapshot{}, validationErr
	}
	reviewCtx, cancelReview := context.WithTimeout(ctx, developmentLineReviewTimeout)
	defer cancelReview()
	ctx = reviewCtx
	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, lockErr := m.lockInventory(ctx)
	if lockErr != nil {
		return PinnedLineReviewSnapshot{}, lockErr
	}
	defer unlockInventory()
	state, loadErr := m.loadLocked()
	if loadErr != nil {
		return PinnedLineReviewSnapshot{}, loadErr
	}
	line := state.DevelopmentLines[request.LineID]
	if line == nil || line.State != developmentLineParked ||
		line.Version != request.ExpectedVersion ||
		line.LastParkPreviousTip != request.ExpectedBase ||
		line.Tip != request.ExpectedTip || line.Tree != request.ExpectedTree {
		return PinnedLineReviewSnapshot{}, fmt.Errorf(
			"%w: review fence changed",
			ErrPinnedLineConflict,
		)
	}
	workspace := state.Workspaces[line.WorkspaceID]
	if workspace == nil || workspace.DroppedAt != nil || workspace.LockedBy != nil ||
		workspace.DevelopmentLineID != line.ID {
		return PinnedLineReviewSnapshot{}, fmt.Errorf(
			"%w: parked review workspace is unavailable",
			ErrPinnedLineConflict,
		)
	}
	environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
	if environmentErr != nil {
		return PinnedLineReviewSnapshot{}, environmentErr
	}
	defer cleanup()
	if verifyErr := m.verifyDevelopmentLineParkedWorkspace(
		ctx,
		workspace,
		line,
		workspace.RemoteURL,
		request.ExpectedTip,
		request.ExpectedTree,
		environment,
	); verifyErr != nil {
		return PinnedLineReviewSnapshot{}, verifyErr
	}
	paths, canonicalDiff, reviewErr := readPinnedLineReview(
		ctx,
		workspace.Path,
		request.ExpectedBase,
		request.ExpectedTip,
		environment,
	)
	if reviewErr != nil {
		return PinnedLineReviewSnapshot{}, reviewErr
	}
	if verifyErr := m.verifyDevelopmentLineParkedWorkspace(
		ctx,
		workspace,
		line,
		workspace.RemoteURL,
		request.ExpectedTip,
		request.ExpectedTree,
		environment,
	); verifyErr != nil {
		return PinnedLineReviewSnapshot{}, verifyErr
	}
	return newPinnedLineReviewSnapshot(
		line,
		request.ExpectedBase,
		request.ExpectedTip,
		request.ExpectedTree,
		paths,
		canonicalDiff,
	), nil
}

func validateDevelopmentLineInventory(state *storeState) error {
	if state == nil {
		return errors.New("git workspace development line inventory is unavailable")
	}
	if state.DevelopmentLines == nil {
		return errors.New("git workspace development line inventory is missing")
	}
	for repositoryID, repository := range state.Repositories {
		if repository == nil || repository.ID != repositoryID ||
			repository.RemoteURL == "" || repoID(repository.RemoteURL) != repositoryID {
			return errors.New("git workspace repository inventory identity is invalid")
		}
	}
	pinnedReservationOwners := make(map[string]string)
	for workspaceID, workspace := range state.Workspaces {
		if workspace == nil || workspace.ID != workspaceID ||
			workspace.RepoID == "" || workspace.RemoteURL == "" {
			return errors.New("git workspace inventory identity is invalid")
		}
		repository := state.Repositories[workspace.RepoID]
		if repository == nil || repository.RemoteURL != workspace.RemoteURL {
			return errors.New("git workspace repository owner is invalid")
		}
		hasPinnedSource := workspace.PinnedSourceRef != ""
		hasPinnedCommit := workspace.PinnedCommit != ""
		if hasPinnedSource != hasPinnedCommit {
			return errors.New("git workspace has incomplete pinned identity")
		}
		if hasPinnedSource && (len(workspace.PinnedSourceRef) > 4<<10 ||
			workspace.PinnedSourceRef != strings.TrimSpace(workspace.PinnedSourceRef) ||
			!utf8.ValidString(workspace.PinnedSourceRef) ||
			containsPinnedControlCharacter(workspace.PinnedSourceRef) ||
			!validControllerPinnedWorkspaceID(workspace.RepoID, workspace.ID) ||
			workspace.Ref != workspace.PinnedSourceRef ||
			!validPinnedCommit(workspace.PinnedCommit)) {
			return errors.New("git workspace pinned identity is invalid")
		}
		if hasPinnedSource && workspace.DroppedAt != nil {
			return errors.New("controller-pinned git workspace cannot be dropped")
		}
		if !hasPinnedSource || workspace.LockedBy == nil {
			continue
		}
		if !validPinnedOperationIdentity(workspace.LockedBy.SessionKey, 256) ||
			!validPinnedOperationIdentity(workspace.LockedBy.AgentID, 256) {
			return errors.New("git workspace pinned lock identity is invalid")
		}
		reservationHash := developmentLineReservationHash(workspace.LockedBy.SessionKey)
		if owner, duplicate := pinnedReservationOwners[reservationHash]; duplicate && owner != workspaceID {
			return errors.New("one reservation owns multiple pinned git workspaces")
		}
		pinnedReservationOwners[reservationHash] = workspaceID
	}
	workspaceOwners := make(map[string]string, len(state.DevelopmentLines))
	reservationOwners := make(map[string]string, len(state.DevelopmentLines))
	for key, line := range state.DevelopmentLines {
		if line == nil || line.ID != key ||
			!validPinnedOperationIdentity(line.ID, maxDevelopmentLineIdentityBytes) ||
			!validPinnedOperationIdentity(line.WorkspaceID, 256) ||
			line.RepoID == "" || line.SourceRef == "" ||
			line.SourceRef != strings.TrimSpace(line.SourceRef) ||
			containsPinnedControlCharacter(line.SourceRef) ||
			!validPinnedCommit(line.SourceCommit) ||
			!validPinnedCommit(line.Tip) || !validPinnedCommit(line.Tree) ||
			len(line.SourceCommit) != len(line.Tip) || len(line.Tip) != len(line.Tree) ||
			line.Branch != developmentLineBranch(line.ID) ||
			line.Version < 0 || line.Version > maxDevelopmentLineReservations ||
			line.MutationEpoch < 1 ||
			line.MutationEpoch > maxDevelopmentLineReservations+1 ||
			line.CreatedAt.IsZero() || line.UpdatedAt.Before(line.CreatedAt) {
			return errors.New("git workspace development line inventory is invalid")
		}
		workspace := state.Workspaces[line.WorkspaceID]
		if workspace == nil || workspace.DroppedAt != nil ||
			!validControllerPinnedWorkspaceID(line.RepoID, line.WorkspaceID) ||
			workspace.DevelopmentLineID != line.ID || workspace.RepoID != line.RepoID ||
			workspace.PinnedSourceRef != line.SourceRef ||
			workspace.PinnedCommit != line.SourceCommit {
			return errors.New("git workspace development line owner is invalid")
		}
		if owner, duplicate := workspaceOwners[line.WorkspaceID]; duplicate && owner != line.ID {
			return errors.New("git workspace belongs to multiple development lines")
		}
		workspaceOwners[line.WorkspaceID] = line.ID
		switch line.State {
		case developmentLineParked:
			if line.Version < 1 || line.MutationEpoch != line.Version ||
				workspace.LockedBy != nil || line.MutationReservationHash != "" ||
				line.MutationAgentID != "" || line.PendingParkSet {
				return errors.New("parked git workspace development line is still reserved")
			}
		case developmentLineMutating:
			if line.Version >= maxDevelopmentLineReservations ||
				line.MutationEpoch != line.Version+1 || workspace.LockedBy == nil ||
				!validPinnedOperationIdentity(line.MutationAgentID, 256) ||
				!validPinnedOperationIdentity(workspace.LockedBy.AgentID, 256) ||
				workspace.LockedBy.SessionKey == "" ||
				workspace.LockedBy.SessionKey != strings.TrimSpace(
					workspace.LockedBy.SessionKey,
				) ||
				!utf8.ValidString(workspace.LockedBy.SessionKey) ||
				containsPinnedControlCharacter(workspace.LockedBy.SessionKey) ||
				line.MutationAgentID != workspace.LockedBy.AgentID ||
				line.MutationReservationHash !=
					developmentLineReservationHash(workspace.LockedBy.SessionKey) {
				return errors.New("mutating git workspace development line owner is invalid")
			}
			if owner, duplicate := reservationOwners[line.MutationReservationHash]; duplicate && owner != line.ID {
				return errors.New("one mutation reservation owns multiple development lines")
			}
			owner, exists := pinnedReservationOwners[line.MutationReservationHash]
			if !exists || owner != line.WorkspaceID {
				return errors.New("development line reservation owner is not exclusive")
			}
			reservationOwners[line.MutationReservationHash] = line.ID
		case developmentLineSuspended:
			if line.Version >= maxDevelopmentLineReservations ||
				line.MutationEpoch != line.Version+1 || workspace.LockedBy != nil ||
				line.MutationReservationHash != "" || line.MutationAgentID != "" ||
				line.PendingParkSet {
				return errors.New("suspended git workspace development line is still reserved")
			}
		default:
			return errors.New("git workspace development line state is invalid")
		}
		if line.PendingParkSet {
			if line.State != developmentLineMutating ||
				!validPinnedOperationIdentity(
					line.PendingParkIntentID,
					maxDevelopmentLineIdentityBytes,
				) ||
				line.PendingParkReservationHash != line.MutationReservationHash ||
				line.PendingParkAgentID != line.MutationAgentID ||
				line.PendingParkEpoch != line.MutationEpoch ||
				line.PendingParkExpectedVersion != line.Version ||
				line.PendingParkPreviousTip != line.Tip ||
				(line.LastParkIntentID != "" &&
					line.PendingParkIntentID == line.LastParkIntentID) ||
				!validPinnedCommit(line.PendingParkTip) ||
				!validPinnedCommit(line.PendingParkTree) ||
				len(line.PendingParkTip) != len(line.Tip) ||
				len(line.PendingParkTree) != len(line.Tip) ||
				(line.PendingParkNoChanges && line.PendingParkTip != line.Tip) ||
				(!line.PendingParkNoChanges && line.PendingParkTip == line.Tip) {
				return errors.New("git workspace development line pending park is invalid")
			}
		} else if line.PendingParkIntentID != "" ||
			line.PendingParkReservationHash != "" || line.PendingParkAgentID != "" ||
			line.PendingParkEpoch != 0 || line.PendingParkExpectedVersion != 0 ||
			line.PendingParkPreviousTip != "" || line.PendingParkTip != "" ||
			line.PendingParkTree != "" || line.PendingParkNoChanges {
			return errors.New("git workspace development line has incomplete pending park")
		}
		lastParkPresent := line.LastParkIntentID != "" ||
			line.LastParkReservationHash != "" || line.LastParkAgentID != "" ||
			line.LastParkEpoch != 0 || line.LastParkExpectedVersion != 0 ||
			line.LastParkPreviousTip != "" ||
			line.LastParkTip != "" || line.LastParkTree != ""
		if line.Version == 0 {
			if line.Tip != line.SourceCommit || lastParkPresent ||
				len(line.RetiredReservationHashes) != 0 {
				return errors.New("unparked git workspace development line has park evidence")
			}
		} else if !lastParkPresent ||
			!validPinnedOperationIdentity(line.LastParkIntentID, maxDevelopmentLineIdentityBytes) ||
			!validLowerHex(line.LastParkReservationHash, sha256.Size*2) ||
			!validPinnedOperationIdentity(line.LastParkAgentID, 256) ||
			line.LastParkEpoch < 1 ||
			line.LastParkEpoch != line.Version ||
			line.LastParkExpectedVersion != line.Version-1 ||
			!validPinnedCommit(line.LastParkPreviousTip) ||
			len(line.LastParkPreviousTip) != len(line.Tip) ||
			line.LastParkTip != line.Tip || line.LastParkTree != line.Tree ||
			len(line.RetiredReservationHashes) != int(line.Version) ||
			len(line.RetiredReservationHashes) > maxDevelopmentLineReservations {
			return errors.New("git workspace development line park evidence is invalid")
		}
		retired := make(map[string]struct{}, len(line.RetiredReservationHashes))
		for _, reservationHash := range line.RetiredReservationHashes {
			if !validLowerHex(reservationHash, sha256.Size*2) {
				return errors.New("git workspace development line reservation history is invalid")
			}
			if _, duplicate := retired[reservationHash]; duplicate {
				return errors.New("git workspace development line reservation was reused")
			}
			if owner, duplicate := reservationOwners[reservationHash]; duplicate && owner != line.ID {
				return errors.New("one mutation reservation belongs to multiple development lines")
			}
			if _, active := pinnedReservationOwners[reservationHash]; active {
				return errors.New("retired development line reservation still owns a pinned workspace")
			}
			retired[reservationHash] = struct{}{}
			reservationOwners[reservationHash] = line.ID
		}
		if line.Version > 0 && line.RetiredReservationHashes[len(line.RetiredReservationHashes)-1] !=
			line.LastParkReservationHash {
			return errors.New("git workspace development line reservation tail is invalid")
		}
		if line.MutationReservationHash != "" &&
			developmentLineReservationRetired(line, line.MutationReservationHash) {
			return errors.New("git workspace development line reused a retired reservation")
		}
		if suspensionErr := validateDevelopmentLineSuspensions(
			line,
			workspace,
			pinnedReservationOwners,
			reservationOwners,
		); suspensionErr != nil {
			return suspensionErr
		}
	}
	for workspaceID, workspace := range state.Workspaces {
		if workspace == nil || workspace.DevelopmentLineID == "" {
			continue
		}
		if line := state.DevelopmentLines[workspace.DevelopmentLineID]; line == nil ||
			line.WorkspaceID != workspaceID {
			return errors.New("git workspace has an orphaned development line owner")
		}
	}
	return validatePinnedReservationRotationInventory(state)
}

func validatePinnedLineAdoptRequest(
	ctx context.Context,
	request PinnedLineAdoptRequest,
) (string, error) {
	repository, err := validatePinnedAcquireRequest(ctx, request.Pin)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPinnedLineInvalid, err)
	}
	if !validPinnedOperationIdentity(request.WorkspaceID, 256) ||
		!validPinnedOperationIdentity(request.LineID, maxDevelopmentLineIdentityBytes) ||
		!validPinnedCommit(request.ExpectedTree) ||
		len(request.ExpectedTree) != len(request.Pin.ExpectedCommit) {
		return "", fmt.Errorf("%w: adoption identity is invalid", ErrPinnedLineInvalid)
	}
	return repository, nil
}

func validatePinnedLineResumeRequest(
	ctx context.Context,
	request PinnedLineResumeRequest,
) (string, error) {
	repository, err := validatePinnedAcquireRequest(ctx, request.Pin)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPinnedLineInvalid, err)
	}
	if !validPinnedOperationIdentity(request.WorkspaceID, 256) ||
		!validPinnedOperationIdentity(request.LineID, maxDevelopmentLineIdentityBytes) ||
		request.ExpectedVersion < 1 ||
		request.ExpectedVersion >= maxDevelopmentLineReservations ||
		request.ExpectedEpoch < 1 ||
		!validPinnedCommit(request.ExpectedTip) ||
		!validPinnedCommit(request.ExpectedTree) ||
		len(request.ExpectedTip) != len(request.Pin.ExpectedCommit) ||
		len(request.ExpectedTree) != len(request.Pin.ExpectedCommit) {
		return "", fmt.Errorf("%w: resume evidence is invalid", ErrPinnedLineInvalid)
	}
	return repository, nil
}

func validatePinnedLineParkRequest(
	ctx context.Context,
	request PinnedLineParkRequest,
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
		request.MutationEpoch < 1 ||
		!validPinnedCommit(request.PreviousTip) || !validPinnedCommit(request.Tip) ||
		!validPinnedCommit(request.Tree) ||
		len(request.PreviousTip) != len(request.Pin.ExpectedCommit) ||
		len(request.Tip) != len(request.Pin.ExpectedCommit) ||
		len(request.Tree) != len(request.Pin.ExpectedCommit) ||
		(request.NoChanges && request.PreviousTip != request.Tip) ||
		(!request.NoChanges && request.PreviousTip == request.Tip) {
		return "", fmt.Errorf("%w: park evidence is invalid", ErrPinnedLineInvalid)
	}
	return repository, nil
}

func validatePinnedLineReviewRequest(request PinnedLineReviewRequest) error {
	if !validPinnedOperationIdentity(request.LineID, maxDevelopmentLineIdentityBytes) ||
		request.ExpectedVersion < 1 || !validPinnedCommit(request.ExpectedBase) ||
		!validPinnedCommit(request.ExpectedTip) || !validPinnedCommit(request.ExpectedTree) ||
		len(request.ExpectedBase) != len(request.ExpectedTip) ||
		len(request.ExpectedTip) != len(request.ExpectedTree) {
		return fmt.Errorf("%w: review evidence is invalid", ErrPinnedLineInvalid)
	}
	return nil
}

func matchAdoptedPinnedLine(
	state *storeState,
	line *developmentLineRecord,
	request PinnedLineAdoptRequest,
	repository, reservationHash string,
) error {
	if line.State != developmentLineMutating || line.Version != 0 ||
		line.MutationEpoch != 1 || line.WorkspaceID != request.WorkspaceID ||
		line.SourceRef != request.Pin.SourceRef ||
		line.SourceCommit != request.Pin.ExpectedCommit ||
		line.Tip != request.Pin.ExpectedCommit || line.Tree != request.ExpectedTree ||
		line.RepoID != repoID(repository) ||
		line.MutationReservationHash != reservationHash ||
		line.MutationAgentID != request.Pin.AgentID {
		return fmt.Errorf("%w: adoption intent changed", ErrPinnedLineConflict)
	}
	workspace := state.Workspaces[line.WorkspaceID]
	if workspace == nil || workspace.DevelopmentLineID != line.ID {
		return fmt.Errorf("%w: adopted workspace changed", ErrPinnedLineConflict)
	}
	return nil
}

func matchPinnedLineResumeIdentity(
	line *developmentLineRecord,
	request PinnedLineResumeRequest,
	repository string,
) error {
	if line.WorkspaceID != request.WorkspaceID || line.RepoID != repoID(repository) ||
		line.SourceRef != request.Pin.SourceRef ||
		line.SourceCommit != request.Pin.ExpectedCommit ||
		line.Version != request.ExpectedVersion || line.Tip != request.ExpectedTip ||
		line.Tree != request.ExpectedTree ||
		(line.State == developmentLineMutating &&
			line.MutationAgentID != request.Pin.AgentID) {
		return fmt.Errorf("%w: resume identity changed", ErrPinnedLineConflict)
	}
	return nil
}

func matchParkedPinnedLineReplay(
	line *developmentLineRecord,
	request PinnedLineParkRequest,
	repository, reservationHash string,
) error {
	if err := matchPinnedLineSource(line, request.Pin, request.WorkspaceID, repository); err != nil {
		return err
	}
	if line.LastParkIntentID != request.IntentID ||
		line.LastParkReservationHash != reservationHash ||
		line.LastParkAgentID != request.Pin.AgentID ||
		line.LastParkEpoch != request.MutationEpoch ||
		line.LastParkExpectedVersion != request.ExpectedVersion ||
		line.LastParkPreviousTip != request.PreviousTip ||
		line.LastParkTip != request.Tip || line.LastParkTree != request.Tree ||
		line.Version != request.ExpectedVersion+1 {
		return fmt.Errorf("%w: park replay changed", ErrPinnedLineConflict)
	}
	return nil
}

func matchPendingPinnedLinePark(
	line *developmentLineRecord,
	request PinnedLineParkRequest,
	reservationHash string,
) error {
	if line == nil || !line.PendingParkSet ||
		line.PendingParkIntentID != request.IntentID ||
		line.PendingParkReservationHash != reservationHash ||
		line.PendingParkAgentID != request.Pin.AgentID ||
		line.PendingParkEpoch != request.MutationEpoch ||
		line.PendingParkExpectedVersion != request.ExpectedVersion ||
		line.PendingParkPreviousTip != request.PreviousTip ||
		line.PendingParkTip != request.Tip || line.PendingParkTree != request.Tree ||
		line.PendingParkNoChanges != request.NoChanges {
		return fmt.Errorf("%w: pending park intent changed", ErrPinnedLineConflict)
	}
	return nil
}

func installPendingPinnedLinePark(
	line *developmentLineRecord,
	request PinnedLineParkRequest,
	reservationHash string,
	now time.Time,
) {
	line.PendingParkSet = true
	line.PendingParkIntentID = request.IntentID
	line.PendingParkReservationHash = reservationHash
	line.PendingParkAgentID = request.Pin.AgentID
	line.PendingParkEpoch = request.MutationEpoch
	line.PendingParkExpectedVersion = request.ExpectedVersion
	line.PendingParkPreviousTip = request.PreviousTip
	line.PendingParkTip = request.Tip
	line.PendingParkTree = request.Tree
	line.PendingParkNoChanges = request.NoChanges
	line.UpdatedAt = now
}

func clearPendingPinnedLinePark(line *developmentLineRecord) {
	line.PendingParkSet = false
	line.PendingParkIntentID = ""
	line.PendingParkReservationHash = ""
	line.PendingParkAgentID = ""
	line.PendingParkEpoch = 0
	line.PendingParkExpectedVersion = 0
	line.PendingParkPreviousTip = ""
	line.PendingParkTip = ""
	line.PendingParkTree = ""
	line.PendingParkNoChanges = false
}

func matchPinnedLineSource(
	line *developmentLineRecord,
	pin PinnedAcquireRequest,
	workspaceID, repository string,
) error {
	if line.WorkspaceID != workspaceID || line.RepoID != repoID(repository) ||
		line.SourceRef != pin.SourceRef || line.SourceCommit != pin.ExpectedCommit {
		return fmt.Errorf("%w: development line source changed", ErrPinnedLineConflict)
	}
	return nil
}

func requireFreshPinnedLineReservation(
	state *storeState,
	lineID, reservation string,
) error {
	workspace, duplicate := findPinnedReservationWorkspaceLocked(state, reservation)
	if duplicate || workspace != nil {
		return fmt.Errorf(
			"%w: mutation reservation already owns a pinned workspace",
			ErrPinnedLineConflict,
		)
	}
	reservationHash := developmentLineReservationHash(reservation)
	if pinnedReservationRotationHashUsed(state, reservationHash) {
		return fmt.Errorf(
			"%w: mutation reservation was already used by a reservation rotation",
			ErrPinnedLineConflict,
		)
	}
	for _, line := range state.DevelopmentLines {
		if line == nil {
			continue
		}
		if line.MutationReservationHash == reservationHash ||
			developmentLineReservationRetired(line, reservationHash) {
			label := "another"
			if line.ID == lineID {
				label = "this"
			}
			return fmt.Errorf(
				"%w: mutation reservation was already used by %s development line",
				ErrPinnedLineConflict,
				label,
			)
		}
	}
	return nil
}

func (m *Manager) verifyDevelopmentLineOwnedWorkspace(
	ctx context.Context,
	state *storeState,
	line *developmentLineRecord,
	pin PinnedAcquireRequest,
	workspaceID, repository string,
	environment []string,
) error {
	workspace, workspaceErr := m.pinnedWorkspaceForOperation(
		ctx,
		state,
		pin,
		workspaceID,
		repository,
		environment,
	)
	if workspaceErr != nil {
		return fmt.Errorf("%w: %v", ErrPinnedLineConflict, workspaceErr)
	}
	if workspace.DevelopmentLineID != line.ID {
		return fmt.Errorf("%w: workspace line owner changed", ErrPinnedLineConflict)
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
			inspectErr = errors.New("development line ref changed")
		}
		return errors.Join(ErrPinnedLineConflict, inspectErr)
	}
	return nil
}

func (m *Manager) verifyDevelopmentLineParkedWorkspace(
	ctx context.Context,
	workspace *WorkspaceRecord,
	line *developmentLineRecord,
	repository, tip, tree string,
	environment []string,
) error {
	if workspace == nil || workspace.DevelopmentLineID != line.ID ||
		workspace.ID != line.WorkspaceID || workspace.RepoID != line.RepoID ||
		workspace.RemoteURL != repository || workspace.PinnedSourceRef != line.SourceRef ||
		workspace.PinnedCommit != line.SourceCommit {
		return fmt.Errorf("%w: parked workspace identity changed", ErrPinnedLineConflict)
	}
	if err := m.verifyPinnedWorkspace(
		ctx,
		workspace,
		repository,
		line.SourceCommit,
		false,
		environment,
	); err != nil {
		return fmt.Errorf("%w: %v", ErrPinnedLineConflict, err)
	}
	if err := verifyPinnedLineCommitState(ctx, workspace, tip, tree, environment); err != nil {
		return err
	}
	if err := validateDevelopmentLineRefLayout(workspace.Path, line.Branch, false); err != nil {
		return errors.Join(ErrPinnedLineConflict, err)
	}
	current, found, err := inspectDevelopmentLineRef(
		ctx,
		workspace.Path,
		line.Branch,
		environment,
	)
	if err != nil || !found || current != tip {
		if err == nil {
			err = errors.New("development line ref changed")
		}
		return errors.Join(ErrPinnedLineConflict, err)
	}
	return nil
}

func verifyPinnedLineCommitState(
	ctx context.Context,
	workspace *WorkspaceRecord,
	commit, tree string,
	environment []string,
) error {
	if workspace == nil {
		return fmt.Errorf("%w: development line workspace is missing", ErrPinnedLineConflict)
	}
	if err := verifyPinnedCommitOperationState(ctx, workspace.Path, environment); err != nil {
		return errors.Join(ErrPinnedLineConflict, err)
	}
	if err := rejectPinnedGitLockFiles(workspace.Path); err != nil {
		return errors.Join(ErrPinnedLineConflict, err)
	}
	if err := provePinnedWorkspaceClean(ctx, workspace.Path, commit, environment); err != nil {
		return errors.Join(ErrPinnedLineConflict, err)
	}
	resolvedTree, err := resolvePinnedTree(ctx, workspace.Path, commit, environment)
	if err != nil || resolvedTree != tree {
		if err == nil {
			err = fmt.Errorf("development line tree is %s, want %s", resolvedTree, tree)
		}
		return errors.Join(ErrPinnedLineConflict, err)
	}
	return nil
}

func verifyPinnedLineAdvance(
	ctx context.Context,
	directory, previous, tip string,
	noChanges bool,
	environment []string,
) error {
	if noChanges {
		if previous != tip {
			return fmt.Errorf("%w: no-change line moved", ErrPinnedLineConflict)
		}
		return nil
	}
	parents, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"rev-list",
		"--parents",
		"-n",
		"1",
		tip,
	)
	if err != nil {
		return fmt.Errorf("verify development line parent: %w", err)
	}
	fields := strings.Fields(string(parents))
	if len(fields) != 2 || fields[0] != tip || fields[1] != previous {
		return fmt.Errorf(
			"%w: development line commit is not one direct child of its prior tip",
			ErrPinnedLineConflict,
		)
	}
	return nil
}

func (m *Manager) advanceDevelopmentLineRef(
	ctx context.Context,
	workspace *WorkspaceRecord,
	branch, previous, tip string,
	allowCreate bool,
	environment []string,
) (bool, error) {
	if workspace == nil || branch == "" || !validPinnedCommit(previous) ||
		!validPinnedCommit(tip) || len(previous) != len(tip) {
		return false, fmt.Errorf("%w: retained ref evidence is invalid", ErrPinnedLineInvalid)
	}
	if layoutErr := validateDevelopmentLineRefLayout(
		workspace.Path,
		branch,
		allowCreate,
	); layoutErr != nil {
		return false, errors.Join(ErrPinnedLineConflict, layoutErr)
	}
	current, found, inspectErr := inspectDevelopmentLineRef(
		ctx,
		workspace.Path,
		branch,
		environment,
	)
	if inspectErr != nil {
		return false, inspectErr
	}
	if found && current == tip {
		if layoutErr := validateDevelopmentLineRefLayout(
			workspace.Path,
			branch,
			false,
		); layoutErr != nil {
			return false, errors.Join(ErrPinnedLineConflict, layoutErr)
		}
		if syncErr := syncDevelopmentLineRef(workspace.Path, branch, tip); syncErr != nil {
			return false, errors.Join(ErrPinnedLineConflict, syncErr)
		}
		return true, nil
	}
	if found && current != previous {
		return false, fmt.Errorf("%w: retained ref moved", ErrPinnedLineConflict)
	}
	hooksPath, hooksErr := os.MkdirTemp(m.rootDir, ".development-line-hooks-")
	if hooksErr != nil {
		return false, fmt.Errorf("create development line hooks directory: %w", hooksErr)
	}
	defer os.RemoveAll(hooksPath)
	old := previous
	if !found {
		old = strings.Repeat("0", len(tip))
	}
	_, updateErr := runPinnedGitPlumbing(
		ctx,
		workspace.Path,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"-c",
		"core.hooksPath="+hooksPath,
		"-c",
		"core.logAllRefUpdates=false",
		"-c",
		"core.fsync=reference",
		"-c",
		"core.fsyncMethod=fsync",
		"update-ref",
		"--no-deref",
		"refs/heads/"+branch,
		tip,
		old,
	)
	if updateErr != nil {
		return false, fmt.Errorf("advance retained development line ref: %w", updateErr)
	}
	if layoutErr := validateDevelopmentLineRefLayout(
		workspace.Path,
		branch,
		false,
	); layoutErr != nil {
		return false, errors.Join(ErrPinnedLineConflict, layoutErr)
	}
	if syncErr := syncDevelopmentLineRef(workspace.Path, branch, tip); syncErr != nil {
		return false, errors.Join(ErrPinnedLineConflict, syncErr)
	}
	return true, nil
}

func syncDevelopmentLineRef(workspacePath, branch, expectedTip string) error {
	if !validPinnedCommit(expectedTip) {
		return errors.New("retained development line sync tip is invalid")
	}
	if layoutErr := validateDevelopmentLineRefLayout(
		workspacePath,
		branch,
		false,
	); layoutErr != nil {
		return layoutErr
	}
	leaf := filepath.Join(
		workspacePath,
		".git",
		"refs",
		"heads",
		filepath.FromSlash(branch),
	)
	before, statErr := os.Lstat(leaf)
	if statErr != nil {
		return fmt.Errorf("inspect retained development line ref before sync: %w", statErr)
	}
	file, openErr := os.Open(leaf)
	if openErr != nil {
		return fmt.Errorf("open retained development line ref for sync: %w", openErr)
	}
	opened, openedErr := file.Stat()
	if openedErr != nil || !os.SameFile(before, opened) ||
		opened.Mode()&os.ModeSymlink != 0 || !opened.Mode().IsRegular() ||
		!pinnedMetadataFileHasSingleLink(leaf, opened) || opened.Size() > 4<<10 {
		_ = file.Close()
		if openedErr != nil {
			return fmt.Errorf("inspect opened retained development line ref: %w", openedErr)
		}
		return errors.New("retained development line ref changed before sync")
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, (4<<10)+1))
	if readErr != nil {
		_ = file.Close()
		return fmt.Errorf("read retained development line ref for sync: %w", readErr)
	}
	if string(contents) != expectedTip+"\n" {
		_ = file.Close()
		return errors.New("retained development line ref changed before sync")
	}
	if syncErr := file.Sync(); syncErr != nil {
		_ = file.Close()
		return fmt.Errorf("sync retained development line ref: %w", syncErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("close retained development line ref after sync: %w", closeErr)
	}
	after, statErr := os.Lstat(leaf)
	if statErr != nil || !os.SameFile(opened, after) {
		if statErr != nil {
			return fmt.Errorf("inspect retained development line ref after sync: %w", statErr)
		}
		return errors.New("retained development line ref changed during sync")
	}
	if syncErr := fileutil.SyncDirectory(filepath.Dir(leaf)); syncErr != nil {
		return fmt.Errorf("sync retained development line ref directory: %w", syncErr)
	}
	if layoutErr := validateDevelopmentLineRefLayout(
		workspacePath,
		branch,
		false,
	); layoutErr != nil {
		return layoutErr
	}
	contents, readErr = os.ReadFile(leaf)
	if readErr != nil {
		return fmt.Errorf("read retained development line ref after sync: %w", readErr)
	}
	if string(contents) != expectedTip+"\n" {
		return errors.New("retained development line ref changed after sync")
	}
	return nil
}

func inspectDevelopmentLineRef(
	ctx context.Context,
	directory, branch string,
	environment []string,
) (string, bool, error) {
	output, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"for-each-ref",
		"--format=%(refname)%09%(objectname)",
		"refs/heads/"+branch,
	)
	if err != nil {
		return "", false, fmt.Errorf("inspect retained development line ref: %w", err)
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		return "", false, nil
	}
	expectedRef := "refs/heads/" + branch
	var exact string
	for _, line := range strings.Split(value, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 || fields[0] == "" || !validPinnedCommit(fields[1]) {
			return "", false, errors.New("retained development line ref is invalid")
		}
		if fields[0] != expectedRef {
			continue
		}
		if exact != "" {
			return "", false, errors.New("retained development line ref is duplicated")
		}
		exact = fields[1]
	}
	if exact == "" {
		return "", false, nil
	}
	return exact, true, nil
}

func validateDevelopmentLineRefLayout(
	workspacePath, branch string,
	allowCreate bool,
) error {
	const prefix = "picoclaw/development/"
	digest := strings.TrimPrefix(branch, prefix)
	if !strings.HasPrefix(branch, prefix) ||
		branch != developmentLineBranchFromDigest(digest) ||
		!validLowerHex(digest, sha256.Size*2) {
		return errors.New("retained development line branch is outside its namespace")
	}
	components := strings.Split(branch, "/")
	if len(components) != 3 {
		return errors.New("retained development line branch is invalid")
	}
	gitDirectory := filepath.Join(workspacePath, ".git")
	pathComponents := append(
		[]string{"refs", "heads"},
		components[:len(components)-1]...,
	)
	parent, err := pinnedDevelopmentLineMetadataParent(
		gitDirectory,
		allowCreate,
		pathComponents...,
	)
	if err != nil {
		return fmt.Errorf("validate retained development line ref: %w", err)
	}
	leaf := filepath.Join(parent, components[len(components)-1])
	if _, lockErr := os.Lstat(leaf + ".lock"); lockErr == nil {
		return errors.New("retained development line ref lock requires recovery")
	} else if !os.IsNotExist(lockErr) {
		return fmt.Errorf("inspect retained development line ref lock: %w", lockErr)
	}
	info, statErr := os.Lstat(leaf)
	if os.IsNotExist(statErr) {
		if !allowCreate {
			return errors.New("retained development line ref is missing")
		}
	} else if statErr != nil {
		return fmt.Errorf("inspect retained development line ref: %w", statErr)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		!pinnedMetadataFileHasSingleLink(leaf, info) || info.Size() > 4<<10 {
		return errors.New("retained development line ref is not an exclusive file")
	} else {
		contents, readErr := os.ReadFile(leaf)
		if readErr != nil {
			return fmt.Errorf("read retained development line ref: %w", readErr)
		}
		value := strings.TrimSuffix(string(contents), "\n")
		if value == string(contents) || !validPinnedCommit(value) {
			return errors.New("retained development line loose ref is not canonical")
		}
	}
	return validateDevelopmentLineReflogAbsent(gitDirectory, components)
}

func validateDevelopmentLineReflogAbsent(
	gitDirectory string,
	branchComponents []string,
) error {
	components := append(
		[]string{"logs", "refs", "heads"},
		branchComponents[:len(branchComponents)-1]...,
	)
	info, err := os.Lstat(gitDirectory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err != nil {
			return err
		}
		return errors.New("pinned Git metadata root is not a real directory")
	}
	current := gitDirectory
	for _, component := range components {
		current = filepath.Join(current, component)
		info, err = os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect retained development line reflog path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("retained development line reflog path is not a real directory")
		}
	}
	leaf := filepath.Join(current, branchComponents[len(branchComponents)-1])
	for _, candidate := range []string{leaf, leaf + ".lock"} {
		if _, err := os.Lstat(candidate); err == nil {
			return errors.New("retained development line must not have a reflog")
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect retained development line reflog: %w", err)
		}
	}
	return nil
}

func pinnedDevelopmentLineMetadataParent(
	gitDirectory string,
	allowCreate bool,
	components ...string,
) (string, error) {
	if allowCreate {
		return ensurePinnedRealDirectoryComponents(gitDirectory, components...)
	}
	info, err := os.Lstat(gitDirectory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err != nil {
			return "", err
		}
		return "", errors.New("pinned Git metadata root is not a real directory")
	}
	current := gitDirectory
	for _, component := range components {
		if component == "" || component == "." || component == ".." ||
			strings.ContainsAny(component, `/\\`) {
			return "", errors.New("pinned Git metadata directory component is invalid")
		}
		current = filepath.Join(current, component)
		info, err = os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("pinned Git metadata directory %s is not real", component)
		}
	}
	return current, nil
}

func developmentLineBranch(lineID string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("picoclaw-pinned-development-line-ref-v1\x00"))
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(lineID)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write([]byte(lineID))
	return developmentLineBranchFromDigest(hex.EncodeToString(hash.Sum(nil)))
}

func developmentLineBranchFromDigest(digest string) string {
	return "picoclaw/development/" + digest
}

func developmentLineReservationHash(reservation string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("picoclaw-pinned-development-line-reservation-v1\x00"))
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(reservation)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write([]byte(reservation))
	return hex.EncodeToString(hash.Sum(nil))
}

func developmentLineReservationRetired(
	line *developmentLineRecord,
	reservationHash string,
) bool {
	if line == nil || reservationHash == "" {
		return false
	}
	return developmentLineParkReservationRetired(line, reservationHash) ||
		developmentLineSuspensionReservationRetired(line, reservationHash)
}

func developmentLineParkReservationRetired(
	line *developmentLineRecord,
	reservationHash string,
) bool {
	if line == nil || reservationHash == "" {
		return false
	}
	for _, retired := range line.RetiredReservationHashes {
		if retired == reservationHash {
			return true
		}
	}
	return false
}

func developmentLineSuspensionReservationRetired(
	line *developmentLineRecord,
	reservationHash string,
) bool {
	if line == nil || reservationHash == "" {
		return false
	}
	for _, suspension := range line.Suspensions {
		if suspension.RetiredReservationHash == reservationHash {
			return true
		}
	}
	return false
}

func rejectPendingDevelopmentLineReservation(
	state *storeState,
	reservation string,
) error {
	if state == nil {
		return errors.New("git workspace development line inventory is unavailable")
	}
	reservationHash := developmentLineReservationHash(reservation)
	for _, line := range state.DevelopmentLines {
		if line != nil && line.PendingParkSet &&
			line.MutationReservationHash == reservationHash {
			return fmt.Errorf(
				"%w: mutation reservation is sealed by a pending park",
				ErrPinnedLineConflict,
			)
		}
	}
	return nil
}

func pinnedLineLease(line *developmentLineRecord, replay bool) PinnedLineLease {
	return PinnedLineLease{
		WorkspaceID:   line.WorkspaceID,
		Version:       line.Version,
		MutationEpoch: line.MutationEpoch,
		Tip:           line.Tip,
		Tree:          line.Tree,
		AlreadyOwned:  replay,
	}
}

func pinnedLineParkResult(
	line *developmentLineRecord,
	replay, noChanges bool,
) PinnedLineParkResult {
	return PinnedLineParkResult{
		WorkspaceID:    line.WorkspaceID,
		Version:        line.Version,
		MutationEpoch:  line.MutationEpoch,
		PreviousTip:    line.LastParkPreviousTip,
		Tip:            line.Tip,
		Tree:           line.Tree,
		NoChanges:      noChanges,
		AlreadyParked:  replay,
		WorkspaceClean: true,
	}
}

func readPinnedLineReview(
	ctx context.Context,
	directory, base, tip string,
	environment []string,
) ([]string, []byte, error) {
	reviewEnvironment := append(
		append([]string(nil), environment...),
		"GIT_ATTR_SOURCE="+tip,
	)
	paths, pathsErr := developmentLineChangedPaths(
		ctx,
		directory,
		base,
		tip,
		reviewEnvironment,
	)
	if pathsErr != nil {
		return nil, nil, pathsErr
	}
	diff, diffErr := runPinnedGitPlumbing(
		ctx,
		directory,
		reviewEnvironment,
		nil,
		maxDevelopmentLineDiffBytes,
		"--attr-source="+tip,
		"diff",
		"--text",
		"--no-ext-diff",
		"--no-textconv",
		"--no-renames",
		"--full-index",
		"--no-color",
		"--src-prefix=a/",
		"--dst-prefix=b/",
		base,
		tip,
		"--",
	)
	if diffErr != nil {
		return nil, nil, fmt.Errorf(
			"read pinned development line diff: %w",
			diffErr,
		)
	}
	canonicalDiff, valid := canonicalDevelopmentLineReviewText(diff)
	if !valid {
		return nil, nil, fmt.Errorf(
			"%w: development line diff is not valid UTF-8 text",
			ErrPinnedLineConflict,
		)
	}
	return paths, canonicalDiff, nil
}

func newPinnedLineReviewSnapshot(
	line *developmentLineRecord,
	base, tip, tree string,
	paths []string,
	diff []byte,
) PinnedLineReviewSnapshot {
	return PinnedLineReviewSnapshot{
		Version:       line.Version,
		MutationEpoch: line.LastParkEpoch,
		ParkIntentID:  line.LastParkIntentID,
		BaseCommit:    base,
		Commit:        tip,
		Tree:          tree,
		ChangedPaths:  paths,
		UnifiedDiff:   string(diff),
		ReviewDigest:  developmentLineReviewDigest(line, paths, diff),
	}
}

func developmentLineChangedPaths(
	ctx context.Context,
	directory, base, tip string,
	environment []string,
) ([]string, error) {
	output, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxDevelopmentLinePathsBytes,
		"diff",
		"--name-only",
		"--no-renames",
		"-z",
		base,
		tip,
		"--",
	)
	if err != nil {
		return nil, fmt.Errorf("list pinned development line paths: %w", err)
	}
	if len(output) == 0 {
		return []string{}, nil
	}
	if output[len(output)-1] != 0 {
		return nil, fmt.Errorf(
			"%w: changed path list is incomplete",
			ErrPinnedLineConflict,
		)
	}
	parts := bytes.Split(output[:len(output)-1], []byte{0})
	if len(parts) > maxDevelopmentLineReviewFiles {
		return nil, fmt.Errorf(
			"%w: changed path count exceeds %d",
			ErrPinnedLineConflict,
			maxDevelopmentLineReviewFiles,
		)
	}
	paths := make([]string, 0, len(parts))
	for _, raw := range parts {
		path := string(raw)
		if !validDevelopmentLineReviewPath(path) {
			return nil, fmt.Errorf(
				"%w: changed path is invalid",
				ErrPinnedLineConflict,
			)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func validDevelopmentLineReviewPath(value string) bool {
	if value == "" || len(value) > maxDevelopmentLinePathBytes || !utf8.ValidString(value) ||
		strings.IndexByte(value, 0) >= 0 || pathpkg.IsAbs(value) ||
		pathpkg.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func canonicalDevelopmentLineReviewText(value []byte) ([]byte, bool) {
	if !utf8.Valid(value) || bytes.IndexByte(value, 0) >= 0 {
		return nil, false
	}
	canonical := value
	if bytes.IndexByte(value, '\r') >= 0 {
		canonical = make([]byte, 0, len(value))
		for index := 0; index < len(value); index++ {
			if value[index] != '\r' {
				canonical = append(canonical, value[index])
				continue
			}
			if index+1 >= len(value) || value[index+1] != '\n' {
				return nil, false
			}
		}
	}
	for _, character := range string(canonical) {
		if character == '\n' || character == '\t' {
			continue
		}
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return nil, false
		}
	}
	return canonical, true
}

func developmentLineReviewDigest(
	line *developmentLineRecord,
	paths []string,
	diff []byte,
) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pinned-development-line-review-v1\x00"))
	if line == nil {
		return hex.EncodeToString(digest.Sum(nil))
	}
	writePinnedDigestField(digest, line.ID)
	var number [8]byte
	binary.BigEndian.PutUint64(number[:], uint64(line.Version))
	_, _ = digest.Write(number[:])
	binary.BigEndian.PutUint64(number[:], uint64(line.LastParkEpoch))
	_, _ = digest.Write(number[:])
	writePinnedDigestField(digest, line.LastParkIntentID)
	writePinnedDigestField(digest, line.LastParkPreviousTip)
	writePinnedDigestField(digest, line.Tip)
	writePinnedDigestField(digest, line.Tree)
	binary.BigEndian.PutUint64(number[:], uint64(len(paths)))
	_, _ = digest.Write(number[:])
	for _, path := range paths {
		writePinnedDigestField(digest, path)
	}
	binary.BigEndian.PutUint64(number[:], uint64(len(diff)))
	_, _ = digest.Write(number[:])
	_, _ = digest.Write(diff)
	return hex.EncodeToString(digest.Sum(nil))
}
