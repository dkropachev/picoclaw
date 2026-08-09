package gitworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"sort"
	"strconv"
	"time"
)

const maxPinnedReservationRotations = 8_192

// PinnedReservationRotationRequest atomically replaces one controller-owned
// reservation after its external lease has expired. Pin authenticates the old
// bearer and exact pinned source. Line fences are all required for a bound
// mutating line and must all be empty or zero for an unbound pinned workspace.
// IntentID and the replacement bearer must be stored durably by the caller
// before invocation so an ambiguous inventory write can be replayed exactly.
// The replacement inherits Pin.AgentID; recovery workers cannot replace the
// stable logical controller identity.
type PinnedReservationRotationRequest struct {
	Pin                       PinnedAcquireRequest `json:"-"`
	WorkspaceID               string               `json:"-"`
	IntentID                  string               `json:"-"`
	ReplacementReservationKey string               `json:"-"`
	LineID                    string               `json:"-"`
	ExpectedVersion           int64                `json:"-"`
	ExpectedMutationEpoch     int64                `json:"-"`
	ExpectedTip               string               `json:"-"`
	ExpectedTree              string               `json:"-"`
}

// PinnedReservationRotationResult exposes only the exact state fence installed
// under the caller's already-known replacement bearer. It deliberately omits
// reservation values, checkout paths, refs, and the private line identity.
type PinnedReservationRotationResult struct {
	WorkspaceID    string `json:"-"`
	Bound          bool   `json:"-"`
	Version        int64  `json:"-"`
	MutationEpoch  int64  `json:"-"`
	Tip            string `json:"-"`
	Tree           string `json:"-"`
	RotationHash   string `json:"-"`
	AlreadyRotated bool   `json:"-"`
}

// pinnedReservationRotationRecord is a private, append-only inventory chain.
// Reservation values are one-way fingerprints; the only retained bearer is
// the currently active WorkspaceRecord lock.
type pinnedReservationRotationRecord struct {
	IntentID                   string    `json:"intent_id"`
	WorkspaceID                string    `json:"workspace_id"`
	LineID                     string    `json:"line_id,omitempty"`
	RepoID                     string    `json:"repo_id"`
	SourceRef                  string    `json:"source_ref"`
	SourceCommit               string    `json:"source_commit"`
	Version                    int64     `json:"version"`
	MutationEpoch              int64     `json:"mutation_epoch"`
	Tip                        string    `json:"tip,omitempty"`
	Tree                       string    `json:"tree,omitempty"`
	PreviousReservationHash    string    `json:"previous_reservation_hash"`
	ReplacementReservationHash string    `json:"replacement_reservation_hash"`
	AgentID                    string    `json:"agent_id"`
	PreviousRecordHash         string    `json:"previous_record_hash"`
	RecordHash                 string    `json:"record_hash"`
	RotatedAt                  time.Time `json:"rotated_at"`
}

// RotatePinnedReservation waits for both the stale and replacement
// cross-process operation locks, in canonical digest order, then swaps only
// private inventory ownership. It does not inspect or alter Git, refs, the
// index, ordinary worktree content, or network state. Pending and parked lines
// are deliberately rejected: their ambiguous commit/park effects require a
// separately durable reconciliation intent before any reservation can move.
func (m *Manager) RotatePinnedReservation(
	ctx context.Context,
	request PinnedReservationRotationRequest,
) (PinnedReservationRotationResult, error) {
	if m == nil {
		return PinnedReservationRotationResult{}, errors.New(
			"git workspace manager is not configured",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, validationErr := validatePinnedReservationRotationRequest(ctx, request)
	if validationErr != nil {
		return PinnedReservationRotationResult{}, validationErr
	}
	releaseOperations, operationErr := m.acquirePinnedReservationRotationOperations(
		ctx,
		request.Pin.ReservationKey,
		request.ReplacementReservationKey,
	)
	if operationErr != nil {
		return PinnedReservationRotationResult{}, operationErr
	}
	defer releaseOperations()

	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, lockErr := m.lockInventory(ctx)
	if lockErr != nil {
		return PinnedReservationRotationResult{}, lockErr
	}
	defer unlockInventory()
	state, loadErr := m.loadLocked()
	if loadErr != nil {
		return PinnedReservationRotationResult{}, loadErr
	}

	rotations := state.PinnedReservationRotations[request.WorkspaceID]
	if len(rotations) > 0 && rotations[len(rotations)-1].IntentID == request.IntentID {
		return replayPinnedReservationRotation(state, rotations[len(rotations)-1], request, repository)
	}
	if pinnedReservationRotationIntentUsed(state, request.IntentID) {
		return PinnedReservationRotationResult{}, fmt.Errorf(
			"%w: reservation rotation intent was already used",
			ErrPinnedLineConflict,
		)
	}
	if len(rotations) >= maxPinnedReservationRotations {
		return PinnedReservationRotationResult{}, fmt.Errorf(
			"%w: reservation rotation history is full",
			ErrPinnedLineConflict,
		)
	}

	workspace, duplicate := findPinnedReservationWorkspaceLocked(
		state,
		request.Pin.ReservationKey,
	)
	if duplicate || workspace == nil || workspace.ID != request.WorkspaceID {
		return PinnedReservationRotationResult{}, fmt.Errorf(
			"%w: stale reservation does not own the exact workspace",
			ErrPinnedLineConflict,
		)
	}
	if workspace.RepoID != repoID(repository) || workspace.RemoteURL != repository ||
		workspace.Ref != request.Pin.SourceRef ||
		workspace.PinnedSourceRef != request.Pin.SourceRef ||
		workspace.PinnedCommit != request.Pin.ExpectedCommit ||
		workspace.LockedBy == nil ||
		workspace.LockedBy.AgentID != request.Pin.AgentID {
		return PinnedReservationRotationResult{}, fmt.Errorf(
			"%w: pinned workspace rotation fence changed",
			ErrPinnedLineConflict,
		)
	}
	previousReservationHash := developmentLineReservationHash(request.Pin.ReservationKey)
	if pinnedReservationRotationRevoked(state, previousReservationHash) {
		return PinnedReservationRotationResult{}, fmt.Errorf(
			"%w: stale reservation was already revoked",
			ErrPinnedLineConflict,
		)
	}
	replacementReservationHash := developmentLineReservationHash(
		request.ReplacementReservationKey,
	)
	if freshErr := requireFreshPinnedReservationRotation(
		state,
		replacementReservationHash,
	); freshErr != nil {
		return PinnedReservationRotationResult{}, freshErr
	}

	var line *developmentLineRecord
	if request.LineID == "" {
		if workspace.DevelopmentLineID != "" {
			return PinnedReservationRotationResult{}, fmt.Errorf(
				"%w: pinned workspace was bound to a development line",
				ErrPinnedLineConflict,
			)
		}
	} else {
		line = state.DevelopmentLines[request.LineID]
		if line == nil || workspace.DevelopmentLineID != request.LineID ||
			line.WorkspaceID != workspace.ID || line.RepoID != workspace.RepoID ||
			line.SourceRef != request.Pin.SourceRef ||
			line.SourceCommit != request.Pin.ExpectedCommit ||
			line.State != developmentLineMutating || line.PendingParkSet ||
			line.Version != request.ExpectedVersion ||
			line.MutationEpoch != request.ExpectedMutationEpoch ||
			line.Tip != request.ExpectedTip || line.Tree != request.ExpectedTree ||
			line.MutationReservationHash != previousReservationHash ||
			line.MutationAgentID != request.Pin.AgentID {
			return PinnedReservationRotationResult{}, fmt.Errorf(
				"%w: development line rotation fence changed",
				ErrPinnedLineConflict,
			)
		}
	}

	now := m.now().UTC()
	previousRecordHash := emptyPinnedReservationRotationDigest()
	if len(rotations) > 0 {
		previousRecordHash = rotations[len(rotations)-1].RecordHash
	}
	record := pinnedReservationRotationRecord{
		IntentID:                   request.IntentID,
		WorkspaceID:                workspace.ID,
		LineID:                     request.LineID,
		RepoID:                     workspace.RepoID,
		SourceRef:                  request.Pin.SourceRef,
		SourceCommit:               request.Pin.ExpectedCommit,
		Version:                    request.ExpectedVersion,
		MutationEpoch:              request.ExpectedMutationEpoch,
		Tip:                        request.ExpectedTip,
		Tree:                       request.ExpectedTree,
		PreviousReservationHash:    previousReservationHash,
		ReplacementReservationHash: replacementReservationHash,
		AgentID:                    request.Pin.AgentID,
		PreviousRecordHash:         previousRecordHash,
		RotatedAt:                  now,
	}
	record.RecordHash = pinnedReservationRotationRecordDigest(record)

	workspace.LockedBy = &LockInfo{
		SessionKey:  request.ReplacementReservationKey,
		AgentID:     request.Pin.AgentID,
		LockedAt:    now,
		HeartbeatAt: now,
	}
	workspace.UpdatedAt = now
	if line != nil {
		line.MutationReservationHash = replacementReservationHash
		line.MutationAgentID = request.Pin.AgentID
		line.UpdatedAt = now
	}
	rotations = append(rotations, record)
	state.PinnedReservationRotations[workspace.ID] = rotations
	workspace.PinnedReservationRotationCount = len(rotations)
	workspace.PinnedReservationRotationTailHash = record.RecordHash
	if saveErr := m.saveLocked(state); saveErr != nil {
		return PinnedReservationRotationResult{}, saveErr
	}
	return pinnedReservationRotationResult(record, false), nil
}

func validatePinnedReservationRotationRequest(
	ctx context.Context,
	request PinnedReservationRotationRequest,
) (string, error) {
	repository, err := validatePinnedAcquireRequest(ctx, request.Pin)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPinnedLineInvalid, err)
	}
	if !validPinnedOperationIdentity(request.WorkspaceID, 256) ||
		!validPinnedOperationIdentity(request.IntentID, maxDevelopmentLineIdentityBytes) ||
		!validPinnedOperationIdentity(request.ReplacementReservationKey, 256) ||
		request.ReplacementReservationKey == request.Pin.ReservationKey {
		return "", fmt.Errorf(
			"%w: reservation rotation identity is invalid",
			ErrPinnedLineInvalid,
		)
	}
	if request.LineID == "" {
		if request.ExpectedVersion != 0 || request.ExpectedMutationEpoch != 0 ||
			request.ExpectedTip != "" || request.ExpectedTree != "" {
			return "", fmt.Errorf(
				"%w: unbound reservation rotation has line evidence",
				ErrPinnedLineInvalid,
			)
		}
		return repository, nil
	}
	if !validPinnedOperationIdentity(request.LineID, maxDevelopmentLineIdentityBytes) ||
		request.ExpectedVersion < 0 ||
		request.ExpectedVersion >= maxDevelopmentLineReservations ||
		request.ExpectedMutationEpoch != request.ExpectedVersion+1 ||
		!validPinnedCommit(request.ExpectedTip) ||
		!validPinnedCommit(request.ExpectedTree) ||
		len(request.ExpectedTip) != len(request.Pin.ExpectedCommit) ||
		len(request.ExpectedTree) != len(request.Pin.ExpectedCommit) {
		return "", fmt.Errorf(
			"%w: bound reservation rotation evidence is invalid",
			ErrPinnedLineInvalid,
		)
	}
	return repository, nil
}

func (m *Manager) acquirePinnedReservationRotationOperations(
	ctx context.Context,
	previousReservation, replacementReservation string,
) (func(), error) {
	if existing, ok := ctx.Value(pinnedOperationContextKey{}).(*pinnedOperationToken); ok &&
		existing != nil && existing.active.Load() {
		return nil, errors.New(
			"pinned reservation rotation must run outside another pinned operation",
		)
	}
	type operation struct {
		reservation string
		digest      [sha256.Size]byte
	}
	operations := []operation{
		{reservation: previousReservation, digest: pinnedOperationLockDigest(previousReservation)},
		{reservation: replacementReservation, digest: pinnedOperationLockDigest(replacementReservation)},
	}
	if operations[0].digest == operations[1].digest {
		return nil, fmt.Errorf(
			"%w: reservation operation identities collide",
			ErrPinnedLineInvalid,
		)
	}
	sort.Slice(operations, func(left, right int) bool {
		return bytes.Compare(operations[left].digest[:], operations[right].digest[:]) < 0
	})
	_, _, releaseFirst, err := m.acquirePinnedOperation(ctx, operations[0].reservation)
	if err != nil {
		return nil, err
	}
	_, _, releaseSecond, err := m.acquirePinnedOperation(ctx, operations[1].reservation)
	if err != nil {
		releaseFirst()
		return nil, err
	}
	return func() {
		releaseSecond()
		releaseFirst()
	}, nil
}

func replayPinnedReservationRotation(
	state *storeState,
	record pinnedReservationRotationRecord,
	request PinnedReservationRotationRequest,
	repository string,
) (PinnedReservationRotationResult, error) {
	previousHash := developmentLineReservationHash(request.Pin.ReservationKey)
	replacementHash := developmentLineReservationHash(request.ReplacementReservationKey)
	if record.WorkspaceID != request.WorkspaceID || record.LineID != request.LineID ||
		record.RepoID != repoID(repository) || record.SourceRef != request.Pin.SourceRef ||
		record.SourceCommit != request.Pin.ExpectedCommit ||
		record.Version != request.ExpectedVersion ||
		record.MutationEpoch != request.ExpectedMutationEpoch ||
		record.Tip != request.ExpectedTip || record.Tree != request.ExpectedTree ||
		record.PreviousReservationHash != previousHash ||
		record.ReplacementReservationHash != replacementHash ||
		record.AgentID != request.Pin.AgentID {
		return PinnedReservationRotationResult{}, fmt.Errorf(
			"%w: reservation rotation replay changed",
			ErrPinnedLineConflict,
		)
	}
	workspace := state.Workspaces[record.WorkspaceID]
	if workspace == nil || workspace.DroppedAt != nil ||
		workspace.RepoID != record.RepoID || workspace.RemoteURL != repository ||
		workspace.Ref != record.SourceRef ||
		workspace.PinnedSourceRef != record.SourceRef ||
		workspace.PinnedCommit != record.SourceCommit || workspace.LockedBy == nil ||
		workspace.LockedBy.SessionKey != request.ReplacementReservationKey ||
		workspace.LockedBy.AgentID != record.AgentID {
		return PinnedReservationRotationResult{}, fmt.Errorf(
			"%w: rotated workspace progressed",
			ErrPinnedLineConflict,
		)
	}
	if record.LineID == "" {
		if workspace.DevelopmentLineID != "" {
			return PinnedReservationRotationResult{}, fmt.Errorf(
				"%w: rotated workspace progressed",
				ErrPinnedLineConflict,
			)
		}
	} else {
		line := state.DevelopmentLines[record.LineID]
		if line == nil || workspace.DevelopmentLineID != record.LineID ||
			line.WorkspaceID != record.WorkspaceID ||
			line.State != developmentLineMutating || line.PendingParkSet ||
			line.Version != record.Version || line.MutationEpoch != record.MutationEpoch ||
			line.Tip != record.Tip || line.Tree != record.Tree ||
			line.MutationReservationHash != record.ReplacementReservationHash ||
			line.MutationAgentID != record.AgentID {
			return PinnedReservationRotationResult{}, fmt.Errorf(
				"%w: rotated development line progressed",
				ErrPinnedLineConflict,
			)
		}
	}
	return pinnedReservationRotationResult(record, true), nil
}

func pinnedReservationRotationResult(
	record pinnedReservationRotationRecord,
	replay bool,
) PinnedReservationRotationResult {
	return PinnedReservationRotationResult{
		WorkspaceID:    record.WorkspaceID,
		Bound:          record.LineID != "",
		Version:        record.Version,
		MutationEpoch:  record.MutationEpoch,
		Tip:            record.Tip,
		Tree:           record.Tree,
		RotationHash:   record.RecordHash,
		AlreadyRotated: replay,
	}
}

func requireFreshPinnedReservationRotation(
	state *storeState,
	replacementHash string,
) error {
	if state == nil {
		return fmt.Errorf(
			"%w: reservation rotation inventory is unavailable",
			ErrPinnedLineConflict,
		)
	}
	for _, workspace := range state.Workspaces {
		if workspace != nil && workspace.LockedBy != nil &&
			developmentLineReservationHash(workspace.LockedBy.SessionKey) == replacementHash {
			return fmt.Errorf(
				"%w: replacement reservation already owns a workspace",
				ErrPinnedLineConflict,
			)
		}
	}
	for _, line := range state.DevelopmentLines {
		if line != nil && (line.MutationReservationHash == replacementHash ||
			developmentLineReservationRetired(line, replacementHash)) {
			return fmt.Errorf(
				"%w: replacement reservation was already used by a development line",
				ErrPinnedLineConflict,
			)
		}
	}
	if pinnedReservationRotationHashUsed(state, replacementHash) {
		return fmt.Errorf(
			"%w: replacement reservation was already used by a rotation",
			ErrPinnedLineConflict,
		)
	}
	return nil
}

func pinnedReservationRotationIntentUsed(state *storeState, intentID string) bool {
	if state == nil {
		return false
	}
	for _, rotations := range state.PinnedReservationRotations {
		for _, record := range rotations {
			if record.IntentID == intentID {
				return true
			}
		}
	}
	return false
}

func pinnedReservationRotationHashUsed(state *storeState, reservationHash string) bool {
	if state == nil || reservationHash == "" {
		return false
	}
	for _, rotations := range state.PinnedReservationRotations {
		for _, record := range rotations {
			if record.PreviousReservationHash == reservationHash ||
				record.ReplacementReservationHash == reservationHash {
				return true
			}
		}
	}
	return false
}

func pinnedReservationRotationRevoked(state *storeState, reservationHash string) bool {
	if state == nil || reservationHash == "" {
		return false
	}
	for _, rotations := range state.PinnedReservationRotations {
		for _, record := range rotations {
			if record.PreviousReservationHash == reservationHash {
				return true
			}
		}
	}
	return false
}

func emptyPinnedReservationRotationDigest() string {
	digest := sha256.Sum256([]byte("picoclaw-pinned-reservation-rotations-empty-v1\x00"))
	return hex.EncodeToString(digest[:])
}

func pinnedReservationRotationRecordDigest(record pinnedReservationRotationRecord) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pinned-reservation-rotation-record-v1\x00"))
	for _, value := range []string{
		record.IntentID,
		record.WorkspaceID,
		record.LineID,
		record.RepoID,
		record.SourceRef,
		record.SourceCommit,
		strconv.FormatInt(record.Version, 10),
		strconv.FormatInt(record.MutationEpoch, 10),
		record.Tip,
		record.Tree,
		record.PreviousReservationHash,
		record.ReplacementReservationHash,
		record.AgentID,
		record.PreviousRecordHash,
		record.RotatedAt.UTC().Format(time.RFC3339Nano),
	} {
		writePinnedReservationRotationHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writePinnedReservationRotationHashField(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}

type pinnedReservationRotationOccurrence struct {
	workspaceID string
	index       int
}

func validatePinnedReservationRotationInventory(state *storeState) error {
	if state == nil || state.PinnedReservationRotations == nil {
		return errors.New("git workspace reservation rotation inventory is missing")
	}
	for workspaceID, workspace := range state.Workspaces {
		rotations, present := state.PinnedReservationRotations[workspaceID]
		if workspace == nil {
			return errors.New("git workspace reservation rotation anchor owner is missing")
		}
		if !controllerPrivateWorkspace(workspace) {
			if workspace.PinnedReservationRotationCount != 0 ||
				workspace.PinnedReservationRotationTailHash != "" || present {
				return errors.New("generic git workspace has a reservation rotation anchor")
			}
			continue
		}
		if workspace.PinnedReservationRotationCount == 0 {
			if workspace.PinnedReservationRotationTailHash !=
				emptyPinnedReservationRotationDigest() || present {
				return errors.New("git workspace empty reservation rotation anchor is invalid")
			}
			continue
		}
		if workspace.PinnedReservationRotationCount < 0 ||
			workspace.PinnedReservationRotationCount > maxPinnedReservationRotations ||
			!present || len(rotations) != workspace.PinnedReservationRotationCount ||
			workspace.PinnedReservationRotationTailHash != rotations[len(rotations)-1].RecordHash {
			return errors.New("git workspace reservation rotation anchor is invalid")
		}
	}
	activeReservations := make(map[string][]string)
	for workspaceID, workspace := range state.Workspaces {
		if workspace == nil || workspace.LockedBy == nil ||
			workspace.PinnedSourceRef == "" || workspace.PinnedCommit == "" {
			continue
		}
		reservationHash := developmentLineReservationHash(workspace.LockedBy.SessionKey)
		activeReservations[reservationHash] = append(
			activeReservations[reservationHash],
			workspaceID,
		)
	}
	activeLineReservations := make(map[string][]string)
	retiredReservations := make(map[string][]string)
	for lineID, line := range state.DevelopmentLines {
		if line == nil {
			continue
		}
		if line.MutationReservationHash != "" {
			activeLineReservations[line.MutationReservationHash] = append(
				activeLineReservations[line.MutationReservationHash],
				lineID,
			)
		}
		for _, reservationHash := range line.RetiredReservationHashes {
			retiredReservations[reservationHash] = append(
				retiredReservations[reservationHash],
				lineID,
			)
		}
	}

	intents := make(map[string]pinnedReservationRotationOccurrence)
	previousReservations := make(map[string]pinnedReservationRotationOccurrence)
	replacementReservations := make(map[string]pinnedReservationRotationOccurrence)
	workspaceIDs := make([]string, 0, len(state.PinnedReservationRotations))
	for workspaceID := range state.PinnedReservationRotations {
		workspaceIDs = append(workspaceIDs, workspaceID)
	}
	sort.Strings(workspaceIDs)
	for _, workspaceID := range workspaceIDs {
		rotations := state.PinnedReservationRotations[workspaceID]
		workspace := state.Workspaces[workspaceID]
		if workspace == nil || workspace.DroppedAt != nil ||
			workspace.PinnedSourceRef == "" || workspace.PinnedCommit == "" ||
			len(rotations) == 0 || len(rotations) > maxPinnedReservationRotations {
			return errors.New("git workspace reservation rotation owner is invalid")
		}
		previousRecordHash := emptyPinnedReservationRotationDigest()
		var previousRecord *pinnedReservationRotationRecord
		for index := range rotations {
			record := &rotations[index]
			occurrence := pinnedReservationRotationOccurrence{
				workspaceID: workspaceID,
				index:       index,
			}
			if record.WorkspaceID != workspaceID ||
				!validPinnedOperationIdentity(record.IntentID, maxDevelopmentLineIdentityBytes) ||
				record.RepoID != workspace.RepoID ||
				record.SourceRef != workspace.PinnedSourceRef ||
				record.SourceCommit != workspace.PinnedCommit ||
				!validLowerHex(record.PreviousReservationHash, sha256.Size*2) ||
				!validLowerHex(record.ReplacementReservationHash, sha256.Size*2) ||
				record.PreviousReservationHash == record.ReplacementReservationHash ||
				!validPinnedOperationIdentity(record.AgentID, 256) ||
				record.PreviousRecordHash != previousRecordHash ||
				!validLowerHex(record.RecordHash, sha256.Size*2) ||
				record.RecordHash != pinnedReservationRotationRecordDigest(*record) ||
				record.RotatedAt.IsZero() || record.RotatedAt.Before(workspace.CreatedAt) ||
				(previousRecord != nil && record.RotatedAt.Before(previousRecord.RotatedAt)) {
				return errors.New("git workspace reservation rotation record is invalid")
			}
			if _, exists := intents[record.IntentID]; exists {
				return errors.New("git workspace reservation rotation intent was reused")
			}
			intents[record.IntentID] = occurrence
			if _, exists := previousReservations[record.PreviousReservationHash]; exists {
				return errors.New("git workspace stale reservation was rotated more than once")
			}
			previousReservations[record.PreviousReservationHash] = occurrence
			if _, exists := replacementReservations[record.ReplacementReservationHash]; exists {
				return errors.New("git workspace replacement reservation was reused")
			}
			replacementReservations[record.ReplacementReservationHash] = occurrence

			if record.LineID == "" {
				if record.Version != 0 || record.MutationEpoch != 0 ||
					record.Tip != "" || record.Tree != "" {
					return errors.New("unbound git workspace reservation rotation has line evidence")
				}
			} else {
				line := state.DevelopmentLines[record.LineID]
				if !validPinnedOperationIdentity(
					record.LineID,
					maxDevelopmentLineIdentityBytes,
				) || line == nil || line.WorkspaceID != workspaceID ||
					line.RepoID != record.RepoID || line.SourceRef != record.SourceRef ||
					line.SourceCommit != record.SourceCommit ||
					record.Version < 0 || record.Version >= maxDevelopmentLineReservations ||
					record.MutationEpoch != record.Version+1 ||
					record.Version > line.Version || !validPinnedCommit(record.Tip) ||
					!validPinnedCommit(record.Tree) ||
					len(record.Tip) != len(record.SourceCommit) ||
					len(record.Tree) != len(record.SourceCommit) ||
					(line.State == developmentLineParked && record.Version >= line.Version) ||
					(line.State == developmentLineMutating && record.Version == line.Version &&
						(record.Tip != line.Tip || record.Tree != line.Tree)) {
					return errors.New("bound git workspace reservation rotation fence is invalid")
				}
			}

			if previousRecord != nil {
				if pinnedReservationRotationContinues(*previousRecord, *record) {
					if record.PreviousReservationHash !=
						previousRecord.ReplacementReservationHash {
						return errors.New("git workspace reservation rotation is not causal")
					}
				} else {
					validLaterEpisode := record.LineID != "" &&
						record.MutationEpoch > 1 && record.Version > 0
					if previousRecord.LineID != "" {
						validLaterEpisode = validLaterEpisode &&
							record.LineID == previousRecord.LineID &&
							record.MutationEpoch > previousRecord.MutationEpoch &&
							record.Version > previousRecord.Version
					}
					if !validLaterEpisode {
						return errors.New("git workspace reservation rotation episode is invalid")
					}
					if tailErr := validatePinnedReservationRotationTail(
						state,
						workspace,
						*previousRecord,
					); tailErr != nil {
						return tailErr
					}
				}
			}
			previousRecordHash = record.RecordHash
			previousRecord = record
		}
		if previousRecord == nil {
			return errors.New("git workspace reservation rotation history is empty")
		}
		if tailErr := validatePinnedReservationRotationTail(
			state,
			workspace,
			*previousRecord,
		); tailErr != nil {
			return tailErr
		}
	}

	for reservationHash, previous := range previousReservations {
		if len(activeReservations[reservationHash]) != 0 ||
			len(activeLineReservations[reservationHash]) != 0 {
			return errors.New("revoked git workspace reservation is still active")
		}
		if len(retiredReservations[reservationHash]) != 0 {
			return errors.New("revoked git workspace reservation was later retired")
		}
		if replacement, exists := replacementReservations[reservationHash]; exists {
			if replacement.workspaceID != previous.workspaceID ||
				replacement.index+1 != previous.index {
				return errors.New("git workspace reservation was reused across rotation chains")
			}
			rotations := state.PinnedReservationRotations[previous.workspaceID]
			if !pinnedReservationRotationContinues(
				rotations[replacement.index],
				rotations[previous.index],
			) {
				return errors.New("git workspace reservation causal reuse is invalid")
			}
		}
	}
	return validatePinnedReservationRotationReplacementOwners(
		state,
		activeReservations,
		activeLineReservations,
		retiredReservations,
		previousReservations,
		replacementReservations,
	)
}

func hasPinnedReservationRotationAnchors(state *storeState) bool {
	if state == nil {
		return false
	}
	for _, workspace := range state.Workspaces {
		if workspace != nil && (workspace.PinnedReservationRotationCount != 0 ||
			workspace.PinnedReservationRotationTailHash != "") {
			return true
		}
	}
	return false
}

func initializePinnedReservationRotationAnchors(state *storeState) {
	if state == nil {
		return
	}
	for _, workspace := range state.Workspaces {
		if controllerPrivateWorkspace(workspace) {
			workspace.PinnedReservationRotationCount = 0
			workspace.PinnedReservationRotationTailHash = emptyPinnedReservationRotationDigest()
		}
	}
}

func validatePinnedReservationRotationReplacementOwners(
	state *storeState,
	activeWorkspaces, activeLines, retiredLines map[string][]string,
	previous, replacements map[string]pinnedReservationRotationOccurrence,
) error {
	for reservationHash, occurrence := range replacements {
		if _, consumed := previous[reservationHash]; consumed {
			// The exact adjacent causal record revoked this intermediate
			// replacement. The caller already proved it is neither active nor
			// retired anywhere.
			continue
		}
		rotations := state.PinnedReservationRotations[occurrence.workspaceID]
		if occurrence.index < 0 || occurrence.index >= len(rotations) {
			return errors.New("git workspace reservation rotation occurrence is invalid")
		}
		record := rotations[occurrence.index]
		workspace := state.Workspaces[record.WorkspaceID]
		if workspace == nil {
			return errors.New("git workspace reservation rotation replacement owner is missing")
		}

		expectedActiveWorkspace := ""
		expectedActiveLine := ""
		expectedRetiredLine := ""
		lineID := record.LineID
		epoch := record.MutationEpoch
		if lineID == "" && workspace.DevelopmentLineID == "" {
			if workspace.LockedBy != nil {
				expectedActiveWorkspace = workspace.ID
			}
		} else {
			if lineID == "" {
				lineID = workspace.DevelopmentLineID
				epoch = 1
			}
			line := state.DevelopmentLines[lineID]
			if line == nil || line.WorkspaceID != workspace.ID {
				return errors.New("git workspace reservation rotation replacement line is invalid")
			}
			if line.State == developmentLineMutating && line.MutationEpoch == epoch {
				expectedActiveWorkspace = workspace.ID
				expectedActiveLine = line.ID
			} else {
				expectedRetiredLine = line.ID
			}
		}
		if !exactPinnedReservationRotationOwners(
			activeWorkspaces[reservationHash],
			expectedActiveWorkspace,
		) || !exactPinnedReservationRotationOwners(
			activeLines[reservationHash],
			expectedActiveLine,
		) || !exactPinnedReservationRotationOwners(
			retiredLines[reservationHash],
			expectedRetiredLine,
		) {
			return errors.New(
				"git workspace reservation rotation replacement has an unrelated owner",
			)
		}
	}
	return nil
}

func exactPinnedReservationRotationOwners(owners []string, expected string) bool {
	if expected == "" {
		return len(owners) == 0
	}
	return len(owners) == 1 && owners[0] == expected
}

func pinnedReservationRotationContinues(
	previous, next pinnedReservationRotationRecord,
) bool {
	if previous.WorkspaceID != next.WorkspaceID {
		return false
	}
	if previous.LineID == "" {
		return next.LineID == "" ||
			(next.LineID != "" && next.Version == 0 && next.MutationEpoch == 1)
	}
	return next.LineID == previous.LineID && next.Version == previous.Version &&
		next.MutationEpoch == previous.MutationEpoch
}

func validatePinnedReservationRotationTail(
	state *storeState,
	workspace *WorkspaceRecord,
	record pinnedReservationRotationRecord,
) error {
	if state == nil || workspace == nil {
		return errors.New("git workspace reservation rotation tail owner is missing")
	}
	lineID := record.LineID
	epoch := record.MutationEpoch
	if lineID == "" && workspace.DevelopmentLineID == "" {
		if workspace.LockedBy == nil {
			// An unbound pinned workspace may have been explicitly released. Its
			// replacement remains permanently non-reusable through the chain.
			return nil
		}
		if developmentLineReservationHash(workspace.LockedBy.SessionKey) !=
			record.ReplacementReservationHash ||
			workspace.LockedBy.AgentID != record.AgentID {
			return errors.New("git workspace reservation rotation tail is not active")
		}
		return nil
	}
	if lineID == "" {
		lineID = workspace.DevelopmentLineID
		epoch = 1
	}
	line := state.DevelopmentLines[lineID]
	if line == nil || line.WorkspaceID != workspace.ID || epoch < 1 {
		return errors.New("git workspace reservation rotation tail line is invalid")
	}
	if line.State == developmentLineMutating && line.MutationEpoch == epoch {
		if workspace.LockedBy == nil ||
			developmentLineReservationHash(workspace.LockedBy.SessionKey) !=
				record.ReplacementReservationHash ||
			line.MutationReservationHash != record.ReplacementReservationHash ||
			workspace.LockedBy.AgentID != record.AgentID ||
			line.MutationAgentID != record.AgentID {
			return errors.New("git workspace reservation rotation tail owner changed")
		}
		return nil
	}
	retiredIndex := epoch - 1
	if retiredIndex < 0 || retiredIndex >= int64(len(line.RetiredReservationHashes)) ||
		line.RetiredReservationHashes[retiredIndex] != record.ReplacementReservationHash {
		return errors.New("git workspace reservation rotation tail was not retired causally")
	}
	return nil
}
