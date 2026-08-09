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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxPinnedCommitMessageBytes       = 512
	maxPinnedCommitGitOutputBytes     = 64 << 10
	maxPinnedCommitGitErrorBytes      = 16 << 10
	maxPinnedControlGitOutputBytes    = 32 << 20
	maxPinnedCandidateDiffBytes       = 16 << 20
	maxPinnedCandidateChangedFiles    = 10_000
	pinnedCommitPostflightTimeout     = 30 * time.Second
	pinnedCommitIntentPrefix          = "pdcmt_"
	pinnedCommitIntentHexBytes        = 16
	pinnedOperationLockFilenamePrefix = ".pinned-operation-"
)

var (
	// ErrPinnedCommitInvalid reports malformed controller commit evidence.
	ErrPinnedCommitInvalid = errors.New("invalid pinned workspace commit request")
	// ErrPinnedCommitConflict reports that the current checkout no longer
	// matches the exact parent, tree, or candidate evidence supplied by the
	// controller.
	ErrPinnedCommitConflict = errors.New("pinned workspace commit conflict")
	// ErrPinnedCommitWorkspaceDrift reports that the intended commit is proven
	// at HEAD but ordinary workspace content no longer matches its tree. The
	// commit remains applied and callers must enter explicit recovery.
	ErrPinnedCommitWorkspaceDrift = errors.New("pinned workspace changed after commit")
)

// PinnedCandidateRequest identifies one exact controller-owned checkout whose
// ordinary tracked and nonignored untracked content should be snapshotted.
// WorkspaceID is required independently of the reservation so a stale caller
// cannot silently adopt a replacement checkout.
type PinnedCandidateRequest struct {
	Pin         PinnedAcquireRequest
	WorkspaceID string
}

// PinnedCandidate is immutable evidence for all ordinary worktree content
// relative to ParentCommit. CandidateDigest is a domain-separated SHA-256 over
// the exact raw Git diff from ParentCommit to Tree.
type PinnedCandidate struct {
	WorkspaceID     string `json:"workspace_id"`
	ParentCommit    string `json:"parent_commit"`
	Tree            string `json:"tree"`
	CandidateDigest string `json:"candidate_digest"`
	ChangedFiles    int    `json:"changed_files"`
}

// PinnedCommitRequest consumes one exact candidate that a trusted controller
// has already validated. IntentID and AuthoredAt must be stored durably before
// invocation so retries recreate the same commit object.
type PinnedCommitRequest struct {
	Pin                     PinnedAcquireRequest
	WorkspaceID             string
	IntentID                string
	ExpectedParent          string
	ExpectedTree            string
	ExpectedCandidateDigest string
	Message                 string
	AuthoredAt              time.Time
}

// PinnedCommitResult contains only content-addressed Git evidence and opaque
// manager identity. It deliberately omits the checkout path and reservation.
type PinnedCommitResult struct {
	WorkspaceID     string `json:"workspace_id"`
	IntentID        string `json:"intent_id"`
	ParentCommit    string `json:"parent_commit"`
	Tree            string `json:"tree"`
	CandidateDigest string `json:"candidate_digest"`
	Commit          string `json:"commit"`
	ChangedFiles    int    `json:"changed_files"`
	AlreadyApplied  bool   `json:"already_applied"`
	WorkspaceClean  bool   `json:"workspace_clean"`
}

type pinnedOperationContextKey struct{}

type pinnedOperationToken struct {
	root        string
	reservation [sha256.Size]byte
	active      atomic.Bool
}

// WithPinnedOperation serializes one reservation across processes while run
// performs trusted filesystem work. The derived context lets atomic Manager
// methods safely reuse the same lock; it becomes invalid when run returns. The
// callback receives no checkout path or Git mutation capability from this API.
func (m *Manager) WithPinnedOperation(
	ctx context.Context,
	request PinnedAcquireRequest,
	run func(context.Context) error,
) error {
	if m == nil {
		return errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if run == nil {
		return errors.New("pinned workspace operation callback is required")
	}
	if _, err := validatePinnedAcquireRequest(ctx, request); err != nil {
		return err
	}
	token, inherited, release, err := m.acquirePinnedOperation(
		ctx,
		request.ReservationKey,
	)
	if err != nil {
		return err
	}
	defer release()
	if err := m.rejectSealedPinnedOperation(ctx, request.ReservationKey); err != nil {
		return err
	}
	if inherited {
		return run(ctx)
	}
	return run(context.WithValue(ctx, pinnedOperationContextKey{}, token))
}

func (m *Manager) rejectSealedPinnedOperation(
	ctx context.Context,
	reservationKey string,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.lockInventory(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := m.loadLocked()
	if err != nil {
		return err
	}
	reservationHash := developmentLineReservationHash(reservationKey)
	workspace, duplicate := findPinnedReservationWorkspaceLocked(state, reservationKey)
	if duplicate || pinnedReservationRotationRevoked(state, reservationHash) ||
		(pinnedReservationRotationHashUsed(state, reservationHash) && workspace == nil) {
		return fmt.Errorf(
			"%w: mutation reservation was revoked by a reservation rotation",
			ErrPinnedLineConflict,
		)
	}
	for _, line := range state.DevelopmentLines {
		if line == nil {
			continue
		}
		if line.PendingParkSet && line.MutationReservationHash == reservationHash {
			return fmt.Errorf(
				"%w: mutation reservation is sealed by a pending park",
				ErrPinnedLineConflict,
			)
		}
		if developmentLineReservationRetired(line, reservationHash) &&
			line.MutationReservationHash != reservationHash {
			return fmt.Errorf(
				"%w: mutation reservation was retired by a development line",
				ErrPinnedLineConflict,
			)
		}
	}
	return nil
}

func (m *Manager) acquirePinnedOperation(
	ctx context.Context,
	reservationKey string,
) (*pinnedOperationToken, bool, func(), error) {
	if m == nil {
		return nil, false, nil, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !validPinnedOperationIdentity(reservationKey, 256) {
		return nil, false, nil, errors.New(
			"pinned reservation key must be an exact bounded identity",
		)
	}
	if err := m.validateRoot(); err != nil {
		return nil, false, nil, err
	}
	digest := pinnedOperationLockDigest(reservationKey)
	if existing, ok := ctx.Value(pinnedOperationContextKey{}).(*pinnedOperationToken); ok &&
		existing != nil && existing.active.Load() {
		if existing.root != m.rootDir || existing.reservation != digest {
			return nil, false, nil, errors.New(
				"a different pinned workspace operation is already active",
			)
		}
		return existing, true, func() {}, nil
	}
	path := filepath.Join(
		m.rootDir,
		pinnedOperationLockFilenamePrefix+hex.EncodeToString(digest[:])+".lock",
	)
	unlock, err := lockInventoryFile(ctx, path)
	if err != nil {
		return nil, false, nil, fmt.Errorf("lock pinned workspace operation: %w", err)
	}
	if err := m.validateRoot(); err != nil {
		unlock()
		return nil, false, nil, err
	}
	token := &pinnedOperationToken{root: m.rootDir, reservation: digest}
	token.active.Store(true)
	var once sync.Once
	release := func() {
		once.Do(func() {
			token.active.Store(false)
			unlock()
		})
	}
	return token, false, release, nil
}

func pinnedOperationLockDigest(reservationKey string) [sha256.Size]byte {
	return sha256.Sum256(append(
		[]byte("picoclaw-pinned-operation-lock-v1\x00"),
		[]byte(reservationKey)...,
	))
}

// SnapshotPinnedCandidate builds a content-addressed candidate through a
// private temporary index. It does not alter HEAD, the real index, or ordinary
// worktree files. Ignored files are excluded by Git's normal repository rules.
func (m *Manager) SnapshotPinnedCandidate(
	ctx context.Context,
	request PinnedCandidateRequest,
) (PinnedCandidate, error) {
	if m == nil {
		return PinnedCandidate{}, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, err := validatePinnedCandidateRequest(ctx, request)
	if err != nil {
		return PinnedCandidate{}, err
	}
	_, _, releaseOperation, err := m.acquirePinnedOperation(
		ctx,
		request.Pin.ReservationKey,
	)
	if err != nil {
		return PinnedCandidate{}, err
	}
	defer releaseOperation()

	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, err := m.lockInventory(ctx)
	if err != nil {
		return PinnedCandidate{}, err
	}
	defer unlockInventory()
	state, err := m.loadLocked()
	if err != nil {
		return PinnedCandidate{}, err
	}
	if pendingErr := rejectPendingDevelopmentLineReservation(
		state,
		request.Pin.ReservationKey,
	); pendingErr != nil {
		return PinnedCandidate{}, pendingErr
	}
	environment, cleanup, err := m.newPinnedGitEnvironment()
	if err != nil {
		return PinnedCandidate{}, err
	}
	defer cleanup()
	workspace, err := m.pinnedWorkspaceForOperation(
		ctx,
		state,
		request.Pin,
		request.WorkspaceID,
		repository,
		environment,
	)
	if err != nil {
		return PinnedCandidate{}, err
	}
	if operationErr := verifyPinnedCommitOperationState(
		ctx,
		workspace.Path,
		environment,
	); operationErr != nil {
		return PinnedCandidate{}, operationErr
	}
	if lockErr := rejectPinnedGitLockFiles(workspace.Path); lockErr != nil {
		return PinnedCandidate{}, lockErr
	}
	parent, err := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	if err != nil {
		return PinnedCandidate{}, fmt.Errorf("resolve pinned candidate parent: %w", err)
	}
	if indexErr := requirePinnedIndexTree(
		ctx,
		workspace.Path,
		parent,
		environment,
	); indexErr != nil {
		return PinnedCandidate{}, indexErr
	}
	candidate, err := m.buildPinnedCandidate(
		ctx,
		workspace.Path,
		parent,
		parent,
		environment,
	)
	if err != nil {
		return PinnedCandidate{}, err
	}
	if candidate.Tree == candidate.parentTree {
		return PinnedCandidate{}, fmt.Errorf(
			"%w: pinned candidate contains no ordinary changes",
			ErrPinnedCommitConflict,
		)
	}
	return PinnedCandidate{
		WorkspaceID:     workspace.ID,
		ParentCommit:    parent,
		Tree:            candidate.Tree,
		CandidateDigest: candidate.Digest,
		ChangedFiles:    candidate.ChangedFiles,
	}, nil
}

// CommitPinned deterministically creates and compare-and-swaps one validated
// local commit on detached HEAD. It never pushes, updates a branch, runs a hook,
// invokes a shell, or releases the reservation.
func (m *Manager) CommitPinned(
	ctx context.Context,
	request PinnedCommitRequest,
) (PinnedCommitResult, error) {
	if m == nil {
		return PinnedCommitResult{}, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, authoredAt, err := validatePinnedCommitRequest(ctx, request)
	if err != nil {
		return PinnedCommitResult{}, err
	}
	_, _, releaseOperation, err := m.acquirePinnedOperation(
		ctx,
		request.Pin.ReservationKey,
	)
	if err != nil {
		return PinnedCommitResult{}, err
	}
	defer releaseOperation()

	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, err := m.lockInventory(ctx)
	if err != nil {
		return PinnedCommitResult{}, err
	}
	defer unlockInventory()
	state, err := m.loadLocked()
	if err != nil {
		return PinnedCommitResult{}, err
	}
	if pendingErr := rejectPendingDevelopmentLineReservation(
		state,
		request.Pin.ReservationKey,
	); pendingErr != nil {
		return PinnedCommitResult{}, pendingErr
	}
	environment, cleanup, err := m.newPinnedGitEnvironment()
	if err != nil {
		return PinnedCommitResult{}, err
	}
	defer cleanup()
	workspace, err := m.pinnedWorkspaceForOperation(
		ctx,
		state,
		request.Pin,
		request.WorkspaceID,
		repository,
		environment,
	)
	if err != nil {
		return PinnedCommitResult{}, err
	}
	if operationErr := verifyPinnedCommitOperationState(
		ctx,
		workspace.Path,
		environment,
	); operationErr != nil {
		return PinnedCommitResult{}, operationErr
	}
	parentTree, err := resolvePinnedTree(
		ctx,
		workspace.Path,
		request.ExpectedParent,
		environment,
	)
	if err != nil {
		return PinnedCommitResult{}, fmt.Errorf("resolve pinned parent tree: %w", err)
	}
	if parentTree == request.ExpectedTree {
		return PinnedCommitResult{}, fmt.Errorf(
			"%w: pinned commit candidate contains no ordinary changes",
			ErrPinnedCommitConflict,
		)
	}
	facts, err := pinnedCandidateFacts(
		ctx,
		workspace.Path,
		request.ExpectedParent,
		request.ExpectedTree,
		environment,
	)
	if err != nil {
		return PinnedCommitResult{}, err
	}
	if facts.Digest != request.ExpectedCandidateDigest {
		return PinnedCommitResult{}, fmt.Errorf(
			"%w: pinned candidate digest changed",
			ErrPinnedCommitConflict,
		)
	}
	commitEnvironment := pinnedCommitEnvironment(environment, authoredAt)
	intended, err := createPinnedCommitObject(
		ctx,
		workspace.Path,
		request.ExpectedTree,
		request.ExpectedParent,
		request.Message,
		request.IntentID,
		commitEnvironment,
	)
	if err != nil {
		return PinnedCommitResult{}, err
	}
	if verifyErr := verifyPinnedCommitObject(
		ctx,
		workspace.Path,
		intended,
		request.ExpectedTree,
		request.ExpectedParent,
		request.Message,
		request.IntentID,
		authoredAt,
		commitEnvironment,
	); verifyErr != nil {
		return PinnedCommitResult{}, verifyErr
	}
	result := PinnedCommitResult{
		WorkspaceID:     workspace.ID,
		IntentID:        request.IntentID,
		ParentCommit:    request.ExpectedParent,
		Tree:            request.ExpectedTree,
		CandidateDigest: request.ExpectedCandidateDigest,
		Commit:          intended,
		ChangedFiles:    facts.ChangedFiles,
	}

	head, err := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	if err != nil {
		return PinnedCommitResult{}, fmt.Errorf("resolve pinned commit HEAD: %w", err)
	}
	lockErr := rejectPinnedGitLockFiles(workspace.Path)
	switch head {
	case request.ExpectedParent:
		if lockErr != nil {
			return PinnedCommitResult{}, lockErr
		}
		if indexErr := requirePinnedIndexTree(
			ctx,
			workspace.Path,
			request.ExpectedParent,
			environment,
		); indexErr != nil {
			return PinnedCommitResult{}, indexErr
		}
		candidate, candidateErr := m.buildPinnedCandidate(
			ctx,
			workspace.Path,
			request.ExpectedParent,
			request.ExpectedParent,
			environment,
		)
		if candidateErr != nil {
			return PinnedCommitResult{}, candidateErr
		}
		if candidate.Tree != request.ExpectedTree ||
			candidate.Digest != request.ExpectedCandidateDigest {
			return PinnedCommitResult{}, fmt.Errorf(
				"%w: pinned worktree changed after validation",
				ErrPinnedCommitConflict,
			)
		}
		updateErr := updatePinnedDetachedHEAD(
			ctx,
			workspace.Path,
			intended,
			request.ExpectedParent,
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
			if postErr != nil || postHead != intended {
				if postErr != nil {
					return PinnedCommitResult{}, errors.Join(updateErr, postErr)
				}
				if postHead != request.ExpectedParent {
					return PinnedCommitResult{}, fmt.Errorf(
						"%w: pinned HEAD changed during commit",
						ErrPinnedCommitConflict,
					)
				}
				return PinnedCommitResult{}, updateErr
			}
		}
	case intended:
		result.AlreadyApplied = true
		if lockErr != nil {
			return result, errors.Join(ErrPinnedCommitWorkspaceDrift, lockErr)
		}
	default:
		return PinnedCommitResult{}, fmt.Errorf(
			"%w: pinned HEAD is neither the expected parent nor intended commit",
			ErrPinnedCommitConflict,
		)
	}
	if postLockErr := rejectPinnedGitLockFiles(workspace.Path); postLockErr != nil {
		return result, errors.Join(ErrPinnedCommitWorkspaceDrift, postLockErr)
	}

	// Recompute from the intended commit rather than trusting the old real
	// index. This is both the normal post-CAS check and crash reconciliation for
	// a process that stopped after updating HEAD but before repairing the index.
	postCtx, postCancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		pinnedCommitPostflightTimeout,
	)
	defer postCancel()
	indexTree, indexErr := resolvePinnedIndexTree(postCtx, workspace.Path, environment)
	if indexErr != nil || (indexTree != parentTree && indexTree != request.ExpectedTree) {
		if indexErr != nil {
			return result, errors.Join(ErrPinnedCommitWorkspaceDrift, indexErr)
		}
		return result, fmt.Errorf(
			"%w: real index is not an interrupted commit state",
			ErrPinnedCommitWorkspaceDrift,
		)
	}
	postCandidate, err := m.buildPinnedCandidate(
		postCtx,
		workspace.Path,
		intended,
		request.ExpectedParent,
		environment,
	)
	if err != nil || postCandidate.Tree != request.ExpectedTree ||
		postCandidate.Digest != request.ExpectedCandidateDigest {
		if err != nil {
			return result, errors.Join(ErrPinnedCommitWorkspaceDrift, err)
		}
		return result, ErrPinnedCommitWorkspaceDrift
	}
	if _, readErr := runPinnedGitPlumbing(
		postCtx,
		workspace.Path,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"read-tree",
		"--reset",
		intended,
	); readErr != nil {
		return result, errors.Join(ErrPinnedCommitWorkspaceDrift, readErr)
	}
	if err := provePinnedWorkspaceClean(
		postCtx,
		workspace.Path,
		intended,
		environment,
	); err != nil {
		return result, errors.Join(ErrPinnedCommitWorkspaceDrift, err)
	}
	if err := verifyPinnedWorkspaceContents(
		postCtx,
		workspace,
		repository,
		request.Pin.ExpectedCommit,
		false,
		environment,
	); err != nil {
		return result, errors.Join(ErrPinnedCommitWorkspaceDrift, err)
	}
	result.WorkspaceClean = true
	return result, nil
}

func validatePinnedAcquireRequest(
	ctx context.Context,
	request PinnedAcquireRequest,
) (string, error) {
	repositoryInput := strings.TrimSpace(request.Repository)
	if repositoryInput == "" || len(repositoryInput) > 4<<10 ||
		repositoryInput != request.Repository ||
		containsPinnedControlCharacter(repositoryInput) {
		return "", errors.New("pinned repository must be exact and non-empty")
	}
	if request.SourceRef == "" || len(request.SourceRef) > 4<<10 ||
		request.SourceRef != strings.TrimSpace(request.SourceRef) ||
		!validPinnedSourceRef(ctx, request.SourceRef) {
		return "", errors.New("pinned source ref is invalid")
	}
	if request.ExpectedCommit != strings.TrimSpace(request.ExpectedCommit) ||
		!validPinnedCommit(request.ExpectedCommit) {
		return "", errors.New("pinned expected commit is invalid")
	}
	if !validPinnedOperationIdentity(request.ReservationKey, 256) {
		return "", errors.New("pinned reservation key must be an exact bounded identity")
	}
	if !validPinnedOperationIdentity(request.AgentID, 256) {
		return "", errors.New("pinned agent ID must be exact and non-empty")
	}
	repository, err := normalizeRepository(repositoryInput)
	if err != nil {
		return "", err
	}
	return repository, nil
}

func validatePinnedCandidateRequest(
	ctx context.Context,
	request PinnedCandidateRequest,
) (string, error) {
	repository, err := validatePinnedAcquireRequest(ctx, request.Pin)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPinnedCommitInvalid, err)
	}
	if !validPinnedOperationIdentity(request.WorkspaceID, 256) {
		return "", fmt.Errorf("%w: workspace ID is invalid", ErrPinnedCommitInvalid)
	}
	return repository, nil
}

func validatePinnedCommitRequest(
	ctx context.Context,
	request PinnedCommitRequest,
) (string, time.Time, error) {
	repository, err := validatePinnedAcquireRequest(ctx, request.Pin)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: %v", ErrPinnedCommitInvalid, err)
	}
	if !validPinnedOperationIdentity(request.WorkspaceID, 256) ||
		!validPinnedCommitIntent(request.IntentID) ||
		!validPinnedCommit(request.ExpectedParent) ||
		!validPinnedCommit(request.ExpectedTree) ||
		len(request.ExpectedParent) != len(request.ExpectedTree) ||
		len(request.ExpectedParent) != len(request.Pin.ExpectedCommit) ||
		!validLowerHex(request.ExpectedCandidateDigest, sha256.Size*2) ||
		!validPinnedCommitMessage(request.Message) {
		return "", time.Time{}, fmt.Errorf(
			"%w: commit identity or evidence is invalid",
			ErrPinnedCommitInvalid,
		)
	}
	_, offset := request.AuthoredAt.Zone()
	if request.AuthoredAt.IsZero() || offset != 0 || request.AuthoredAt.Nanosecond() != 0 ||
		request.AuthoredAt.Year() < 1970 || request.AuthoredAt.Year() > 9999 {
		return "", time.Time{}, fmt.Errorf(
			"%w: authored time must be a UTC whole second",
			ErrPinnedCommitInvalid,
		)
	}
	return repository, request.AuthoredAt.UTC(), nil
}

func validPinnedOperationIdentity(value string, maximum int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maximum ||
		!utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func validPinnedCommitIntent(value string) bool {
	if !strings.HasPrefix(value, pinnedCommitIntentPrefix) {
		return false
	}
	return validLowerHex(
		strings.TrimPrefix(value, pinnedCommitIntentPrefix),
		pinnedCommitIntentHexBytes*2,
	)
}

func validLowerHex(value string, expectedLength int) bool {
	if len(value) != expectedLength {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validPinnedCommitMessage(value string) bool {
	return len(value) <= maxPinnedCommitMessageBytes &&
		validPinnedOperationIdentity(value, maxPinnedCommitMessageBytes)
}

func (m *Manager) pinnedWorkspaceForOperation(
	ctx context.Context,
	state *storeState,
	request PinnedAcquireRequest,
	workspaceID, repository string,
	environment []string,
) (*WorkspaceRecord, error) {
	workspace, duplicate := findPinnedReservationWorkspaceLocked(
		state,
		request.ReservationKey,
	)
	if duplicate {
		return nil, errors.New("pinned reservation owns multiple git workspaces")
	}
	if workspace == nil || workspace.ID != workspaceID || workspace.DroppedAt != nil ||
		workspace.LockedBy == nil || workspace.LockedBy.SessionKey != request.ReservationKey ||
		workspace.LockedBy.AgentID != request.AgentID || workspace.RepoID != repoID(repository) ||
		workspace.RemoteURL != repository || workspace.Ref != request.SourceRef ||
		workspace.PinnedSourceRef != request.SourceRef ||
		workspace.PinnedCommit != request.ExpectedCommit {
		return nil, fmt.Errorf(
			"%w: pinned workspace does not match controller identity",
			ErrPinnedCommitConflict,
		)
	}
	if err := m.verifyPinnedWorkspace(
		ctx,
		workspace,
		repository,
		request.ExpectedCommit,
		false,
		environment,
	); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPinnedCommitConflict, err)
	}
	return workspace, nil
}

func verifyPinnedCommitOperationState(
	ctx context.Context,
	directory string,
	environment []string,
) error {
	branch, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"rev-parse",
		"--abbrev-ref",
		"HEAD",
	)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(branch)) != "HEAD" {
		return fmt.Errorf("%w: pinned HEAD is attached", ErrPinnedCommitConflict)
	}
	gitDirectory := filepath.Join(directory, ".git")
	for _, relative := range []string{
		"AUTO_MERGE",
		"BISECT_START",
		"CHERRY_PICK_HEAD",
		"MERGE_HEAD",
		"REVERT_HEAD",
		"rebase-apply",
		"rebase-merge",
		"sequencer",
	} {
		_, statErr := os.Lstat(filepath.Join(gitDirectory, filepath.FromSlash(relative)))
		if statErr == nil {
			return fmt.Errorf(
				"%w: pinned workspace has an in-progress Git operation",
				ErrPinnedCommitConflict,
			)
		}
		if !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect pinned Git operation state: %w", statErr)
		}
	}
	unmerged, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"ls-files",
		"--unmerged",
		"-z",
	)
	if err != nil {
		return err
	}
	if len(unmerged) != 0 {
		return fmt.Errorf("%w: pinned index has conflicts", ErrPinnedCommitConflict)
	}
	return nil
}

func rejectPinnedGitLockFiles(directory string) error {
	gitDirectory := filepath.Join(directory, ".git")
	for _, name := range []string{"HEAD.lock", "index.lock"} {
		_, err := os.Lstat(filepath.Join(gitDirectory, name))
		if err == nil {
			return fmt.Errorf(
				"%w: pinned workspace has Git lock %s requiring explicit recovery",
				ErrPinnedCommitConflict,
				name,
			)
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("inspect pinned Git lock %s: %w", name, err)
		}
	}
	return nil
}

func requirePinnedIndexTree(
	ctx context.Context,
	directory, commit string,
	environment []string,
) error {
	expected, err := resolvePinnedTree(ctx, directory, commit, environment)
	if err != nil {
		return fmt.Errorf("resolve pinned index baseline: %w", err)
	}
	actual, err := resolvePinnedIndexTree(ctx, directory, environment)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf(
			"%w: pinned real index differs from HEAD",
			ErrPinnedCommitConflict,
		)
	}
	return nil
}

func resolvePinnedIndexTree(
	ctx context.Context,
	directory string,
	environment []string,
) (string, error) {
	output, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"write-tree",
	)
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(output))
	if !validPinnedCommit(tree) {
		return "", errors.New("Git wrote a noncanonical real index tree")
	}
	return tree, nil
}

func resolvePinnedTree(
	ctx context.Context,
	directory, revision string,
	environment []string,
) (string, error) {
	output, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"rev-parse",
		"--verify",
		revision+"^{tree}",
	)
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(output))
	if !validPinnedCommit(tree) {
		return "", errors.New("Git resolved a noncanonical tree")
	}
	return tree, nil
}

type pinnedCandidateBuild struct {
	Tree         string
	Digest       string
	ChangedFiles int
	parentTree   string
}

func (m *Manager) buildPinnedCandidate(
	ctx context.Context,
	directory, indexBase, diffParent string,
	environment []string,
) (pinnedCandidateBuild, error) {
	if err := m.validateRoot(); err != nil {
		return pinnedCandidateBuild{}, err
	}
	temporaryRoot, err := os.MkdirTemp(m.rootDir, ".pinned-candidate-")
	if err != nil {
		return pinnedCandidateBuild{}, fmt.Errorf("create pinned candidate index: %w", err)
	}
	defer os.RemoveAll(temporaryRoot)
	indexPath := filepath.Join(temporaryRoot, "index")
	indexEnvironment := append(
		append([]string(nil), environment...),
		"GIT_INDEX_FILE="+indexPath,
	)
	if _, readErr := runPinnedGitPlumbing(
		ctx,
		directory,
		indexEnvironment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"read-tree",
		indexBase,
	); readErr != nil {
		return pinnedCandidateBuild{}, readErr
	}
	if _, addErr := runPinnedGitPlumbing(
		ctx,
		directory,
		indexEnvironment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"add",
		"--all",
		"--",
		".",
	); addErr != nil {
		return pinnedCandidateBuild{}, addErr
	}
	output, err := runPinnedGitPlumbing(
		ctx,
		directory,
		indexEnvironment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"write-tree",
	)
	if err != nil {
		return pinnedCandidateBuild{}, err
	}
	tree := strings.TrimSpace(string(output))
	if !validPinnedCommit(tree) {
		return pinnedCandidateBuild{}, errors.New("Git wrote a noncanonical candidate tree")
	}
	facts, err := pinnedCandidateFacts(ctx, directory, diffParent, tree, environment)
	if err != nil {
		return pinnedCandidateBuild{}, err
	}
	parentTree, err := resolvePinnedTree(ctx, directory, diffParent, environment)
	if err != nil {
		return pinnedCandidateBuild{}, err
	}
	return pinnedCandidateBuild{
		Tree:         tree,
		Digest:       facts.Digest,
		ChangedFiles: facts.ChangedFiles,
		parentTree:   parentTree,
	}, nil
}

type pinnedCandidateFactSet struct {
	Digest       string
	ChangedFiles int
}

func pinnedCandidateFacts(
	ctx context.Context,
	directory, parent, tree string,
	environment []string,
) (pinnedCandidateFactSet, error) {
	writer := newPinnedRawDiffWriter(parent, tree)
	if _, err := runPinnedGitPlumbingTo(
		ctx,
		directory,
		environment,
		nil,
		writer,
		maxPinnedCandidateDiffBytes,
		"diff-tree",
		"--no-commit-id",
		"-r",
		"--raw",
		"--no-renames",
		"--no-ext-diff",
		"--no-textconv",
		"--no-abbrev",
		"-z",
		parent,
		tree,
	); err != nil {
		return pinnedCandidateFactSet{}, err
	}
	return writer.finish()
}

type pinnedRawDiffWriter struct {
	hash          io.Writer
	digest        hash.Hash
	header        bytes.Buffer
	readingHeader bool
	changedFiles  int
	gitlink       bool
	invalid       bool
}

func newPinnedRawDiffWriter(parent, tree string) *pinnedRawDiffWriter {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pinned-candidate-diff-v1\x00"))
	writePinnedDigestField(digest, parent)
	writePinnedDigestField(digest, tree)
	return &pinnedRawDiffWriter{
		hash:          digest,
		digest:        digest,
		readingHeader: true,
	}
}

func (writer *pinnedRawDiffWriter) Write(value []byte) (int, error) {
	_, _ = writer.hash.Write(value)
	for _, character := range value {
		if writer.readingHeader {
			if character == 0 {
				writer.inspectHeader()
				writer.header.Reset()
				writer.readingHeader = false
				continue
			}
			if writer.header.Len() >= 512 {
				writer.invalid = true
				continue
			}
			_ = writer.header.WriteByte(character)
			continue
		}
		if character == 0 {
			writer.changedFiles++
			writer.readingHeader = true
		}
	}
	return len(value), nil
}

func (writer *pinnedRawDiffWriter) inspectHeader() {
	fields := strings.Fields(writer.header.String())
	if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") {
		writer.invalid = true
		return
	}
	oldMode := strings.TrimPrefix(fields[0], ":")
	newMode := fields[1]
	if oldMode == "160000" || newMode == "160000" {
		writer.gitlink = true
	}
}

func (writer *pinnedRawDiffWriter) finish() (pinnedCandidateFactSet, error) {
	if !writer.readingHeader || writer.header.Len() != 0 || writer.invalid {
		return pinnedCandidateFactSet{}, errors.New("Git returned an invalid raw candidate diff")
	}
	if writer.gitlink {
		return pinnedCandidateFactSet{}, fmt.Errorf(
			"%w: changed Git links are unsupported",
			ErrPinnedCommitConflict,
		)
	}
	if writer.changedFiles > maxPinnedCandidateChangedFiles {
		return pinnedCandidateFactSet{}, fmt.Errorf(
			"%w: pinned candidate changes too many files",
			ErrPinnedCommitConflict,
		)
	}
	return pinnedCandidateFactSet{
		Digest:       hex.EncodeToString(writer.digest.Sum(nil)),
		ChangedFiles: writer.changedFiles,
	}, nil
}

func writePinnedDigestField(writer io.Writer, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}

func pinnedCommitEnvironment(environment []string, authoredAt time.Time) []string {
	date := strconv.FormatInt(authoredAt.Unix(), 10) + " +0000"
	return append(
		append([]string(nil), environment...),
		"GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_DATE="+date,
	)
}

func createPinnedCommitObject(
	ctx context.Context,
	directory, tree, parent, message, intentID string,
	environment []string,
) (string, error) {
	commitMessage := pinnedCommitObjectMessage(message, intentID)
	output, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		strings.NewReader(commitMessage),
		maxPinnedCommitGitOutputBytes,
		"-c",
		"commit.gpgSign=false",
		"-c",
		"i18n.commitEncoding=UTF-8",
		"commit-tree",
		tree,
		"-p",
		parent,
		"-F",
		"-",
	)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(output))
	if !validPinnedCommit(commit) || len(commit) != len(parent) {
		return "", errors.New("Git created a noncanonical commit")
	}
	return commit, nil
}

func verifyPinnedCommitObject(
	ctx context.Context,
	directory, commit, tree, parent, message, intentID string,
	authoredAt time.Time,
	environment []string,
) error {
	output, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"cat-file",
		"commit",
		commit,
	)
	if err != nil {
		return err
	}
	headers, body, found := bytes.Cut(output, []byte("\n\n"))
	if !found || string(body) != pinnedCommitObjectMessage(message, intentID) {
		return errors.New("created pinned commit message does not match intent")
	}
	lines := strings.Split(string(headers), "\n")
	if len(lines) != 4 || lines[0] != "tree "+tree || lines[1] != "parent "+parent {
		return errors.New("created pinned commit ancestry does not match intent")
	}
	identity := "PicoClaw <picoclaw@localhost> " +
		strconv.FormatInt(authoredAt.Unix(), 10) + " +0000"
	if lines[2] != "author "+identity || lines[3] != "committer "+identity {
		return errors.New("created pinned commit identity does not match intent")
	}
	return nil
}

func pinnedCommitObjectMessage(message, intentID string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pinned-commit-intent-v1\x00"))
	writePinnedDigestField(digest, intentID)
	return message + "\n\nPicoClaw-Intent: " + hex.EncodeToString(digest.Sum(nil)) + "\n"
}

func updatePinnedDetachedHEAD(
	ctx context.Context,
	directory, commit, parent string,
	environment []string,
) error {
	_, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"update-ref",
		"--no-deref",
		"HEAD",
		commit,
		parent,
	)
	return err
}

func provePinnedWorkspaceClean(
	ctx context.Context,
	directory, commit string,
	environment []string,
) error {
	head, err := resolvePinnedGitCommit(ctx, directory, "HEAD", environment)
	if err != nil || head != commit {
		if err != nil {
			return err
		}
		return errors.New("pinned HEAD changed during commit postflight")
	}
	if indexErr := requirePinnedIndexTree(ctx, directory, commit, environment); indexErr != nil {
		return indexErr
	}
	status, err := runPinnedGitPlumbing(
		ctx,
		directory,
		environment,
		nil,
		maxPinnedCommitGitOutputBytes,
		"status",
		"--porcelain=v1",
		"--untracked-files=all",
	)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(status)) != 0 {
		return errors.New("pinned workspace is not clean after commit")
	}
	return nil
}

type boundedPinnedWriter struct {
	writer   io.Writer
	limit    int
	written  int
	overflow bool
	cancel   context.CancelFunc
}

func (writer *boundedPinnedWriter) Write(value []byte) (int, error) {
	remaining := writer.limit - writer.written
	if remaining > 0 {
		portion := value
		if len(portion) > remaining {
			portion = portion[:remaining]
		}
		_, _ = writer.writer.Write(portion)
		writer.written += len(portion)
	}
	if len(value) > remaining {
		writer.overflow = true
		if writer.cancel != nil {
			writer.cancel()
		}
	}
	return len(value), nil
}

func runPinnedGitPlumbing(
	ctx context.Context,
	directory string,
	environment []string,
	input io.Reader,
	maximumOutput int,
	args ...string,
) ([]byte, error) {
	var output bytes.Buffer
	_, err := runPinnedGitPlumbingTo(
		ctx,
		directory,
		environment,
		input,
		&output,
		maximumOutput,
		args...,
	)
	return output.Bytes(), err
}

func runPinnedGitPlumbingTo(
	ctx context.Context,
	directory string,
	environment []string,
	input io.Reader,
	output io.Writer,
	maximumOutput int,
	args ...string,
) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if environment == nil {
		environment = pinnedGitEnvironment(os.DevNull, os.DevNull)
	}
	if maximumOutput < 1 {
		maximumOutput = maxPinnedCommitGitOutputBytes
	}
	commandCtx, cancelCommand := context.WithCancel(ctx)
	defer cancelCommand()
	command := exec.CommandContext(commandCtx, "git", args...)
	command.Dir = directory
	command.Env = pinnedPlumbingEnvironment(environment)
	command.Stdin = input
	var errorsOutput bytes.Buffer
	boundedOutput := &boundedPinnedWriter{
		writer: output,
		limit:  maximumOutput,
		cancel: cancelCommand,
	}
	boundedError := &boundedPinnedWriter{
		writer: &errorsOutput,
		limit:  maxPinnedCommitGitErrorBytes,
		cancel: cancelCommand,
	}
	command.Stdout = boundedOutput
	command.Stderr = boundedError
	err := command.Run()
	if boundedOutput.overflow || boundedError.overflow {
		return boundedOutput.written, errors.New("pinned Git plumbing output exceeded its limit")
	}
	if err != nil {
		message := strings.TrimSpace(errorsOutput.String())
		if message == "" {
			message = err.Error()
		}
		operation := "operation"
		if len(args) > 0 {
			operation = args[0]
		}
		return boundedOutput.written, fmt.Errorf(
			"pinned Git %s failed: %s",
			operation,
			message,
		)
	}
	return boundedOutput.written, nil
}

func pinnedPlumbingEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+8)
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "EDITOR", "VISUAL", "PAGER", "LESS", "LANG", "LANGUAGE", "LC_ALL":
			continue
		}
		if strings.HasPrefix(strings.ToUpper(name), "LC_") {
			continue
		}
		result = append(result, entry)
	}
	return append(
		result,
		"LC_ALL=C",
		"LANG=C",
		"PAGER=cat",
		"GIT_PAGER=cat",
		"EDITOR=false",
		"VISUAL=false",
		"GIT_EDITOR=false",
		"GIT_SEQUENCE_EDITOR=false",
	)
}
