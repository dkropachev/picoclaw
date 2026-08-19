package gitworkspace

import (
	"context"
	"errors"
	"fmt"
)

// PinnedCandidateReview is a bounded, read-only view of one exact candidate.
// It grants no commit, push, release, process, or worktree capability.
type PinnedCandidateReview struct {
	ChangedPaths []string `json:"changed_paths"`
	UnifiedDiff  string   `json:"unified_diff"`
}

// SnapshotPinnedCandidateReview revalidates an exact content-addressed
// candidate before and after reading its canonical bounded diff. The operation
// holds the reservation's cross-process fence for the complete read.
func (m *Manager) SnapshotPinnedCandidateReview(
	ctx context.Context,
	request PinnedCandidateValidationRequest,
) (PinnedCandidateReview, error) {
	if m == nil {
		return PinnedCandidateReview{}, errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repository, err := validatePinnedCandidateValidationRequest(ctx, request)
	if err != nil {
		return PinnedCandidateReview{}, err
	}
	var review PinnedCandidateReview
	err = m.WithPinnedOperation(ctx, request.Pin, func(operationCtx context.Context) error {
		environment, cleanup, environmentErr := m.newPinnedGitEnvironment()
		if environmentErr != nil {
			return environmentErr
		}
		defer cleanup()
		before, snapshotErr := m.snapshotPinnedCandidateValidationState(
			operationCtx, request, repository, environment,
		)
		if snapshotErr != nil {
			return snapshotErr
		}
		paths, canonicalDiff, reviewErr := readPinnedLineReview(
			operationCtx,
			before.workspacePath,
			request.ExpectedParent,
			request.ExpectedTree,
			environment,
		)
		if reviewErr != nil {
			return reviewErr
		}
		after, snapshotErr := m.snapshotPinnedCandidateValidationState(
			operationCtx, request, repository, environment,
		)
		if snapshotErr != nil {
			return fmt.Errorf("pinned candidate review postflight: %w", snapshotErr)
		}
		if before.workspacePath != after.workspacePath || before.parentTree != after.parentTree {
			return fmt.Errorf("%w: pinned candidate review fence changed", ErrPinnedCommitConflict)
		}
		review = PinnedCandidateReview{
			ChangedPaths: append([]string(nil), paths...),
			UnifiedDiff:  string(canonicalDiff),
		}
		return nil
	})
	return review, err
}
