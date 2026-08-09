//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorePRDevelopmentLedgerAppendOrderReplayAndFindings(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newParkedPRDevelopmentLedgerFixtureForTest(t, ":memory:")
	caseID := fixture.DevelopmentCase.ID
	attemptID := fixture.Attempt.ID
	reviewLease := acquireParkedPRDevelopmentLedgerReviewForTest(
		t,
		fixture.Store,
		caseID,
		attemptID,
	)

	empty, err := fixture.Store.GetPRDevelopmentLedgerForCase(ctx, caseID)
	require.NoError(t, err)
	assert.Equal(t, fixture.Controller.Bound.ThreadID, empty.ThreadID)
	assert.Empty(t, empty.Entries)
	assert.Empty(t, empty.Checkpoints)
	assert.Nil(t, empty.LatestCheckpoint)
	assert.Equal(t, emptyPRDevelopmentLedgerEntriesDigest(), empty.EntriesDigest)
	assert.Equal(t, emptyPRDevelopmentLedgerCheckpointsDigest(), empty.CheckpointsDigest)
	snapshot, err := fixture.Store.GetPRDevelopmentContextSnapshot(ctx, caseID)
	require.NoError(t, err)
	assert.Equal(t, 0, snapshot.SelectedOrdinal)
	assert.Equal(t, empty, snapshot.Ledger)
	assert.Equal(t, empty.ThreadID, snapshot.Thread.ID)

	line := 27
	reviewInput := PRDevelopmentLedgerReviewAppend{
		CaseID:           " " + caseID + " ",
		AttemptID:        " " + attemptID + " ",
		ControllerID:     " " + reviewLease.ID + " ",
		ExpectedRevision: reviewLease.Revision,
		LeaseToken:       " " + reviewLease.LeaseToken + " ",
		LeaseEpoch:       reviewLease.LeaseEpoch,
		Summary:          "  The focused local review found one actionable issue.  ",
		Outcome:          PRDevelopmentLedgerReviewChangesRequired,
		Findings: []PRDevelopmentLedgerReviewFinding{
			{
				Severity:       ReviewSeverityHigh,
				Title:          "  Preserve the exact candidate  ",
				File:           "  pkg/example/example.go  ",
				Line:           &line,
				Message:        "  The candidate changed after local CI.  ",
				Evidence:       "  The verified tree differs from the committed tree.  ",
				Impact:         "  Review evidence would describe different code.  ",
				Recommendation: "  Re-run CI against the exact candidate.  ",
				Validation:     "  Compare the tree immediately before commit.  ",
			},
		},
	}
	_, changed, err := fixture.Store.AppendPRDevelopmentLedgerReview(ctx, reviewInput)
	assert.ErrorIs(t, err, ErrPRDevelopmentLedgerConflict)
	assert.False(t, changed, "review cannot be logged before its attempt account")

	attemptInput := PRDevelopmentLedgerAttemptAppend{
		CaseID:         " " + caseID + " ",
		AttemptID:      " " + attemptID + " ",
		Summary:        "  Applied the bounded repair and passed focused CI.  ",
		CIPlanDigest:   strings.Repeat("1", 64),
		CIResultDigest: strings.Repeat("2", 64),
	}
	attempt, changed, err := fixture.Store.AppendPRDevelopmentLedgerAttempt(ctx, attemptInput)
	require.NoError(t, err)
	require.True(t, changed)
	assert.True(t, validPrefixedHexID(attempt.ID, prDevelopmentLedgerEntryIDPrefix))
	assert.Equal(t, fixture.Controller.Bound.ThreadID, attempt.ThreadID)
	assert.Equal(t, 0, attempt.Ordinal)
	assert.Equal(t, PRDevelopmentLedgerAttempt, attempt.Kind)
	assert.Equal(t, attemptID, attempt.AttemptID)
	assert.Equal(t, 0, attempt.FenceOrdinal)
	assert.Equal(t, caseID, attempt.CaseID)
	assert.Equal(t, fixture.Controller.Fence.TipCommit, attempt.Commit)
	assert.Equal(t, fixture.Controller.Fence.Tree, attempt.Tree)
	assert.Equal(t, fixture.Controller.Fence.NoChanges, attempt.NoChanges)
	assert.Equal(t, "Applied the bounded repair and passed focused CI.", attempt.Summary)
	assert.Equal(t, strings.Repeat("1", 64), attempt.CIPlanDigest)
	assert.Equal(t, strings.Repeat("2", 64), attempt.CIResultDigest)
	assert.Empty(t, attempt.ReviewOutcome)
	assert.Empty(t, attempt.Findings)
	assert.Equal(
		t,
		mutationStagePRDevelopmentReviewFenceHash(fixture.Controller.Fence),
		attempt.FenceHash,
	)
	assert.Equal(t, emptyPRDevelopmentLedgerEntriesDigest(), attempt.PreviousHash)
	assert.Equal(t, hashPRDevelopmentLedgerEntry(attempt), attempt.EntryHash)

	*fixture.Clock = fixture.Clock.Add(-time.Hour)
	replayedAttempt, changed, err := fixture.Store.AppendPRDevelopmentLedgerAttempt(
		ctx,
		attemptInput,
	)
	require.NoError(t, err, "exact replay must not consult a regressed clock")
	assert.False(t, changed)
	assert.Equal(t, attempt, replayedAttempt)
	*fixture.Clock = fixture.Clock.Add(time.Hour)

	changedAttempt := attemptInput
	changedAttempt.Summary = "A different account for the same attempt."
	_, changed, err = fixture.Store.AppendPRDevelopmentLedgerAttempt(ctx, changedAttempt)
	assert.ErrorIs(t, err, ErrPRDevelopmentLedgerConflict)
	assert.False(t, changed)

	staleReviewProof := reviewInput
	staleReviewProof.LeaseEpoch++
	_, changed, err = fixture.Store.AppendPRDevelopmentLedgerReview(ctx, staleReviewProof)
	assert.ErrorIs(t, err, ErrPRDevelopmentControllerConflict)
	assert.False(t, changed, "only the exact live review lease can finish and append")

	review, changed, err := fixture.Store.AppendPRDevelopmentLedgerReview(ctx, reviewInput)
	require.NoError(t, err)
	require.True(t, changed)
	ready, err := fixture.Store.GetPRDevelopmentControllerForCase(ctx, caseID)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentControllerReady, ready.Phase)
	assert.Empty(t, ready.LeaseToken)
	assert.True(t, validPrefixedHexID(review.ID, prDevelopmentLedgerEntryIDPrefix))
	assert.Equal(t, attempt.ThreadID, review.ThreadID)
	assert.Equal(t, 1, review.Ordinal)
	assert.Equal(t, PRDevelopmentLedgerReview, review.Kind)
	assert.Equal(t, attemptID, review.AttemptID)
	assert.Equal(t, 0, review.FenceOrdinal)
	assert.Empty(t, review.Commit)
	assert.Empty(t, review.Tree)
	assert.False(t, review.NoChanges)
	assert.Equal(t, "The focused local review found one actionable issue.", review.Summary)
	assert.Equal(t, PRDevelopmentLedgerReviewChangesRequired, review.ReviewOutcome)
	require.Len(t, review.Findings, 1)
	assert.Equal(t, ReviewSeverityHigh, review.Findings[0].Severity)
	assert.Equal(t, "Preserve the exact candidate", review.Findings[0].Title)
	assert.Equal(t, "pkg/example/example.go", review.Findings[0].File)
	require.NotNil(t, review.Findings[0].Line)
	assert.Equal(t, 27, *review.Findings[0].Line)
	assert.Equal(t, ready.FencesDigest, review.FenceHash)
	assert.Equal(t, attempt.EntryHash, review.PreviousHash)
	assert.Equal(t, hashPRDevelopmentLedgerEntry(review), review.EntryHash)

	*fixture.Clock = fixture.Clock.Add(-time.Hour)
	replayedReview, changed, err := fixture.Store.AppendPRDevelopmentLedgerReview(
		ctx,
		reviewInput,
	)
	require.NoError(t, err, "exact replay must not consult a regressed clock")
	assert.False(t, changed)
	assert.Equal(t, review, replayedReview)
	*fixture.Clock = fixture.Clock.Add(time.Hour)
	changedReview := reviewInput
	changedReview.Summary = "A changed review account."
	_, changed, err = fixture.Store.AppendPRDevelopmentLedgerReview(ctx, changedReview)
	assert.ErrorIs(t, err, ErrPRDevelopmentLedgerConflict)
	assert.False(t, changed)

	*reviewInput.Findings[0].Line = 999
	reviewInput.Findings[0].Message = "mutated caller-owned input"
	loaded, err := fixture.Store.GetPRDevelopmentLedgerForCase(ctx, caseID)
	require.NoError(t, err)
	assert.Equal(t, review.EntryHash, loaded.EntriesDigest)
	require.Len(t, loaded.Entries, 2)
	assert.Equal(t, attempt, loaded.Entries[0])
	assert.Equal(t, review, loaded.Entries[1], "the store must clone caller-owned findings")
}

func TestStorePRDevelopmentLedgerReviewAppendRollsBackLeaseCompletion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newParkedPRDevelopmentLedgerFixtureForTest(t, ":memory:")
	caseID := fixture.DevelopmentCase.ID
	attemptID := fixture.Attempt.ID
	_, changed, err := fixture.Store.AppendPRDevelopmentLedgerAttempt(
		ctx,
		validPRDevelopmentLedgerAttemptAppendForTest(caseID, attemptID),
	)
	require.NoError(t, err)
	require.True(t, changed)
	reviewLease := acquireParkedPRDevelopmentLedgerReviewForTest(
		t,
		fixture.Store,
		caseID,
		attemptID,
	)
	input := validPRDevelopmentLedgerReviewAppendForTest(
		caseID,
		attemptID,
		reviewLease,
	)

	_, err = fixture.Store.db.Exec(`
		CREATE TRIGGER abort_pr_development_ledger_review_insert
		BEFORE INSERT ON pr_development_ledger_entries
		WHEN NEW.kind = 'review'
		BEGIN
			SELECT RAISE(ABORT, 'forced review ledger insert failure');
		END`)
	require.NoError(t, err)
	_, changed, err = fixture.Store.AppendPRDevelopmentLedgerReview(ctx, input)
	require.Error(t, err)
	assert.False(t, changed)

	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		fixture.Store.db,
		reviewLease.ID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, PRDevelopmentControllerReview, controller.Phase)
	assert.Equal(t, reviewLease.Revision, controller.Revision)
	assert.Equal(t, reviewLease.LeaseToken, controller.LeaseToken)
	assert.Equal(t, reviewLease.LeaseEpoch, controller.LeaseEpoch)
	fence, found, err := loadPRDevelopmentReviewFenceByAttempt(
		ctx,
		fixture.Store.db,
		attemptID,
	)
	require.NoError(t, err)
	require.True(t, found)
	assert.Nil(t, fence.ReviewedAt)
	assert.Zero(t, fence.ReviewLeaseEpoch)
	assert.Empty(t, fence.ReviewLeaseTokenDigest)
	var reviewRows int
	require.NoError(t, fixture.Store.db.QueryRow(`
		SELECT COUNT(*) FROM pr_development_ledger_entries WHERE kind = 'review'`,
	).Scan(&reviewRows))
	assert.Zero(t, reviewRows)

	_, err = fixture.Store.db.Exec(
		`DROP TRIGGER abort_pr_development_ledger_review_insert`,
	)
	require.NoError(t, err)
	_, changed, err = fixture.Store.AppendPRDevelopmentLedgerReview(ctx, input)
	require.NoError(t, err)
	assert.True(t, changed, "the same live lease must remain retryable after rollback")
}

func TestStorePRDevelopmentLedgerRejectsSeparatelyFinishedReview(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newParkedPRDevelopmentLedgerFixtureForTest(t, ":memory:")
	caseID := fixture.DevelopmentCase.ID
	attemptID := fixture.Attempt.ID
	reviewLease := acquireParkedPRDevelopmentLedgerReviewForTest(
		t,
		fixture.Store,
		caseID,
		attemptID,
	)
	input := validPRDevelopmentLedgerReviewAppendForTest(
		caseID,
		attemptID,
		reviewLease,
	)
	_, changed, err := fixture.Store.FinishPRDevelopmentControllerReview(
		ctx,
		PRDevelopmentControllerReviewTransition{
			ControllerID:     reviewLease.ID,
			AttemptID:        attemptID,
			ExpectedRevision: reviewLease.Revision,
			LeaseToken:       reviewLease.LeaseToken,
			LeaseEpoch:       reviewLease.LeaseEpoch,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)

	_, changed, err = fixture.Store.AppendPRDevelopmentLedgerReview(ctx, input)
	assert.ErrorIs(t, err, ErrPRDevelopmentLedgerConflict)
	assert.False(t, changed, "a finished fence cannot accept a later payload")
	_, changed, err = fixture.Store.AppendPRDevelopmentLedgerAttempt(
		ctx,
		validPRDevelopmentLedgerAttemptAppendForTest(caseID, attemptID),
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentLedgerConflict)
	assert.False(t, changed, "a finished legacy fence cannot start a ledger")
}

func TestStorePRDevelopmentLedgerFailsClosedOnReviewedAttemptTail(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newParkedPRDevelopmentLedgerFixtureForTest(t, ":memory:")
	caseID := fixture.DevelopmentCase.ID
	attemptID := fixture.Attempt.ID
	_, changed, err := fixture.Store.AppendPRDevelopmentLedgerAttempt(
		ctx,
		validPRDevelopmentLedgerAttemptAppendForTest(caseID, attemptID),
	)
	require.NoError(t, err)
	require.True(t, changed)
	reviewLease := acquireParkedPRDevelopmentLedgerReviewForTest(
		t,
		fixture.Store,
		caseID,
		attemptID,
	)
	_, changed, err = fixture.Store.FinishPRDevelopmentControllerReview(
		ctx,
		PRDevelopmentControllerReviewTransition{
			ControllerID:     reviewLease.ID,
			AttemptID:        attemptID,
			ExpectedRevision: reviewLease.Revision,
			LeaseToken:       reviewLease.LeaseToken,
			LeaseEpoch:       reviewLease.LeaseEpoch,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)

	_, err = fixture.Store.GetPRDevelopmentLedgerForCase(ctx, caseID)
	assert.ErrorIs(t, err, errInvalidStoredPRDevelopmentLedger)
}

func TestStorePRDevelopmentLedgerCheckpointReplayMonotonicityAndRawRetention(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture, attempt, review := completePRDevelopmentLedgerForTest(t, ":memory:")
	input := PRDevelopmentLedgerCheckpointAppend{
		CaseID:         " " + fixture.DevelopmentCase.ID + " ",
		ThroughOrdinal: review.Ordinal,
		SourceDigest:   review.EntryHash,
		Summary:        "  The first mutation and its local review are compacted here.  ",
		CompactorID:    "  ledger-compactor-v1  ",
		PromptDigest:   strings.Repeat("3", 64),
	}

	wrongSource := input
	wrongSource.SourceDigest = strings.Repeat("f", 64)
	_, changed, err := fixture.Store.AppendPRDevelopmentLedgerCheckpoint(ctx, wrongSource)
	assert.ErrorIs(t, err, ErrPRDevelopmentLedgerConflict)
	assert.False(t, changed)

	checkpoint, changed, err := fixture.Store.AppendPRDevelopmentLedgerCheckpoint(ctx, input)
	require.NoError(t, err)
	require.True(t, changed)
	assert.True(t, validPrefixedHexID(
		checkpoint.ID,
		prDevelopmentLedgerCheckpointIDPrefix,
	))
	assert.Equal(t, attempt.ThreadID, checkpoint.ThreadID)
	assert.Equal(t, 0, checkpoint.Generation)
	assert.Equal(t, review.Ordinal, checkpoint.ThroughOrdinal)
	assert.Equal(t, review.EntryHash, checkpoint.SourceDigest)
	assert.Equal(
		t,
		"The first mutation and its local review are compacted here.",
		checkpoint.Summary,
	)
	assert.Equal(t, "ledger-compactor-v1", checkpoint.CompactorID)
	assert.Equal(t, strings.Repeat("3", 64), checkpoint.PromptDigest)
	assert.Equal(t, emptyPRDevelopmentLedgerCheckpointsDigest(), checkpoint.PreviousHash)
	assert.Equal(t, hashPRDevelopmentLedgerCheckpoint(checkpoint), checkpoint.CheckpointHash)

	*fixture.Clock = fixture.Clock.Add(-time.Hour)
	replayed, changed, err := fixture.Store.AppendPRDevelopmentLedgerCheckpoint(ctx, input)
	require.NoError(t, err, "exact checkpoint replay must not consult a regressed clock")
	assert.False(t, changed)
	assert.Equal(t, checkpoint, replayed)
	*fixture.Clock = fixture.Clock.Add(time.Hour)

	changedReplay := input
	changedReplay.Summary = "A different summary for the same prefix."
	_, changed, err = fixture.Store.AppendPRDevelopmentLedgerCheckpoint(ctx, changedReplay)
	assert.ErrorIs(t, err, ErrPRDevelopmentLedgerConflict)
	assert.False(t, changed)

	unavailableAdvance := input
	unavailableAdvance.ThroughOrdinal = 3
	_, changed, err = fixture.Store.AppendPRDevelopmentLedgerCheckpoint(
		ctx,
		unavailableAdvance,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentLedgerConflict)
	assert.False(t, changed, "compaction cannot skip beyond the reviewed prefix")

	loaded, err := fixture.Store.GetPRDevelopmentLedgerForCase(
		ctx,
		fixture.DevelopmentCase.ID,
	)
	require.NoError(t, err)
	require.Len(t, loaded.Entries, 2, "logical compaction must retain raw entries")
	assert.Equal(t, attempt, loaded.Entries[0])
	assert.Equal(t, review, loaded.Entries[1])
	require.Len(t, loaded.Checkpoints, 1)
	assert.Equal(t, checkpoint, loaded.Checkpoints[0])
	require.NotNil(t, loaded.LatestCheckpoint)
	assert.Equal(t, checkpoint, *loaded.LatestCheckpoint)
	assert.Equal(t, checkpoint.CheckpointHash, loaded.CheckpointsDigest)

	var entryCount, findingCount, checkpointCount int
	require.NoError(t, fixture.Store.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_ledger_entries`,
	).Scan(&entryCount))
	require.NoError(t, fixture.Store.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_ledger_review_findings`,
	).Scan(&findingCount))
	require.NoError(t, fixture.Store.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_ledger_checkpoints`,
	).Scan(&checkpointCount))
	assert.Equal(t, 2, entryCount)
	assert.Equal(t, 1, findingCount)
	assert.Equal(t, 1, checkpointCount)
}

func TestStorePRDevelopmentLedgerCorruptionFailsClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tamper func(t *testing.T, store *Store)
	}{
		{
			name: "attempt payload",
			tamper: func(t *testing.T, store *Store) {
				t.Helper()
				_, err := store.db.Exec(`
					UPDATE pr_development_ledger_entries
					SET summary = 'tampered attempt account'
					WHERE kind = 'attempt'`)
				require.NoError(t, err)
			},
		},
		{
			name: "review finding payload",
			tamper: func(t *testing.T, store *Store) {
				t.Helper()
				_, err := store.db.Exec(`
					UPDATE pr_development_ledger_review_findings
					SET message = 'tampered finding'
					WHERE ordinal = 0`)
				require.NoError(t, err)
			},
		},
		{
			name: "checkpoint payload",
			tamper: func(t *testing.T, store *Store) {
				t.Helper()
				_, err := store.db.Exec(`
					UPDATE pr_development_ledger_checkpoints
					SET summary = 'tampered checkpoint'`)
				require.NoError(t, err)
			},
		},
		{
			name: "missing finding",
			tamper: func(t *testing.T, store *Store) {
				t.Helper()
				_, err := store.db.Exec(`DELETE FROM pr_development_ledger_review_findings`)
				require.NoError(t, err)
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture, _, review := completePRDevelopmentLedgerForTest(t, ":memory:")
			_, changed, err := fixture.Store.AppendPRDevelopmentLedgerCheckpoint(
				context.Background(),
				PRDevelopmentLedgerCheckpointAppend{
					CaseID:         fixture.DevelopmentCase.ID,
					ThroughOrdinal: review.Ordinal,
					SourceDigest:   review.EntryHash,
					Summary:        "Compact the reviewed prefix.",
					CompactorID:    "ledger-corruption-test",
					PromptDigest:   strings.Repeat("4", 64),
				},
			)
			require.NoError(t, err)
			require.True(t, changed)
			test.tamper(t, fixture.Store)

			_, err = fixture.Store.GetPRDevelopmentLedgerForCase(
				context.Background(),
				fixture.DevelopmentCase.ID,
			)
			assert.ErrorIs(t, err, errInvalidStoredPRDevelopmentLedger)
		})
	}
}

func TestStoreMigratedLedgerAnchorsToPreexistingParkedFence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger-v10-anchor.db")
	fixture := newParkedPRDevelopmentLedgerFixtureForTest(t, path)
	caseID := fixture.DevelopmentCase.ID
	attemptID := fixture.Attempt.ID
	clock := fixture.Clock
	require.NoError(t, fixture.Store.Close())

	db := openSchemaTestDB(t, path)
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`DROP TABLE pr_development_ledger_review_findings`,
		`DROP TABLE pr_development_ledger_checkpoints`,
		`DROP TABLE pr_development_ledger_entries`,
		`PRAGMA user_version = 10`,
	} {
		_, err := db.Exec(statement)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())

	migrated, err := Open(ctx, path, WithClock(func() time.Time { return *clock }))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, migrated.Close()) })
	var version, fenceCount int
	require.NoError(t, migrated.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	require.NoError(t, migrated.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_attempt_review_fences`,
	).Scan(&fenceCount))
	assert.Equal(t, schemaVersion, version)
	assert.Equal(t, 1, fenceCount, "migration must retain the preexisting v10 fence")

	empty, err := migrated.GetPRDevelopmentLedgerForCase(ctx, caseID)
	require.NoError(t, err)
	assert.Empty(t, empty.Entries, "migration must not manufacture ledger accounts")
	attempt, changed, err := migrated.AppendPRDevelopmentLedgerAttempt(
		ctx,
		validPRDevelopmentLedgerAttemptAppendForTest(caseID, attemptID),
	)
	require.NoError(t, err)
	require.True(t, changed)
	reviewLease := acquireParkedPRDevelopmentLedgerReviewForTest(
		t,
		migrated,
		caseID,
		attemptID,
	)
	review, changed, err := migrated.AppendPRDevelopmentLedgerReview(
		ctx,
		validPRDevelopmentLedgerReviewAppendForTest(caseID, attemptID, reviewLease),
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, 0, attempt.Ordinal)
	assert.Equal(t, 1, review.Ordinal)
}

func TestStorePRDevelopmentLedgerRejectsMalformedInputsWithoutRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newParkedPRDevelopmentLedgerFixtureForTest(t, ":memory:")
	caseID := fixture.DevelopmentCase.ID
	attemptID := fixture.Attempt.ID
	reviewLease := acquireParkedPRDevelopmentLedgerReviewForTest(
		t,
		fixture.Store,
		caseID,
		attemptID,
	)
	invalidAttempt := validPRDevelopmentLedgerAttemptAppendForTest(caseID, attemptID)
	invalidAttempt.CIPlanDigest = "not-a-digest"
	_, changed, err := fixture.Store.AppendPRDevelopmentLedgerAttempt(ctx, invalidAttempt)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentLedger)
	assert.False(t, changed)

	line := 0
	invalidReview := validPRDevelopmentLedgerReviewAppendForTest(
		caseID,
		attemptID,
		reviewLease,
	)
	invalidReview.Findings[0].Line = &line
	_, changed, err = fixture.Store.AppendPRDevelopmentLedgerReview(ctx, invalidReview)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentLedger)
	assert.False(t, changed)

	_, changed, err = fixture.Store.AppendPRDevelopmentLedgerCheckpoint(
		ctx,
		PRDevelopmentLedgerCheckpointAppend{
			CaseID:         caseID,
			ThroughOrdinal: 0,
			SourceDigest:   strings.Repeat("a", 64),
			Summary:        "An invalid attempt-only prefix.",
			CompactorID:    "compactor",
			PromptDigest:   strings.Repeat("b", 64),
		},
	)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentLedger)
	assert.False(t, changed)

	var entries, checkpoints int
	require.NoError(t, fixture.Store.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_ledger_entries`,
	).Scan(&entries))
	require.NoError(t, fixture.Store.db.QueryRow(
		`SELECT COUNT(*) FROM pr_development_ledger_checkpoints`,
	).Scan(&checkpoints))
	assert.Zero(t, entries)
	assert.Zero(t, checkpoints)
}

func TestPRDevelopmentLedgerTypesAreJSONPrivate(t *testing.T) {
	t.Parallel()

	sentinel := "private-ledger-sentinel"
	line := 7
	values := []any{
		PRDevelopmentLedgerReviewFinding{Title: sentinel, Line: &line},
		PRDevelopmentLedgerEntry{Summary: sentinel},
		PRDevelopmentLedgerCheckpoint{Summary: sentinel},
		PRDevelopmentLedger{
			ThreadID: sentinel,
			Entries:  []PRDevelopmentLedgerEntry{{Summary: sentinel}},
		},
		PRDevelopmentContextSnapshot{
			SelectedOrdinal: 7,
			Ledger:          PRDevelopmentLedger{ThreadID: sentinel},
		},
		PRDevelopmentLedgerAttemptAppend{Summary: sentinel},
		PRDevelopmentLedgerReviewAppend{
			Summary:  sentinel,
			Findings: []PRDevelopmentLedgerReviewFinding{{Title: sentinel}},
		},
		PRDevelopmentLedgerCheckpointAppend{Summary: sentinel},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), sentinel)
		assert.Equal(t, `{}`, string(encoded))
	}
}

type prDevelopmentLedgerTestFixture struct {
	Store           *Store
	Clock           *time.Time
	DevelopmentCase PRDevelopmentCase
	Completed       PRDevelopmentRepairSession
	Attempt         PRDevelopmentRepairAttempt
	Controller      prDevelopmentControllerTestFixture
}

func newParkedPRDevelopmentLedgerFixtureForTest(
	t *testing.T,
	databasePath string,
) prDevelopmentLedgerTestFixture {
	t.Helper()
	ctx := context.Background()
	store, clock, capture := newPRDevelopmentStoreFixture(t, databasePath)
	developmentCase, created, err := store.CapturePRDevelopmentCase(
		ctx,
		validPRDevelopmentRequestForTest(capture),
	)
	require.NoError(t, err)
	require.True(t, created)
	completed := completePRDevelopmentRepairForControllerTest(
		t,
		store,
		developmentCase.ID,
	)
	attempt := completed.Attempts[len(completed.Attempts)-1]
	controller := parkPRDevelopmentControllerForTest(
		t,
		store,
		developmentCase.ID,
		completed,
	)
	return prDevelopmentLedgerTestFixture{
		Store:           store,
		Clock:           clock,
		DevelopmentCase: developmentCase,
		Completed:       completed,
		Attempt:         attempt,
		Controller:      controller,
	}
}

func acquireParkedPRDevelopmentLedgerReviewForTest(
	t *testing.T,
	store *Store,
	caseID, attemptID string,
) PRDevelopmentController {
	t.Helper()
	ctx := context.Background()
	parked, err := store.GetPRDevelopmentControllerForCase(
		ctx,
		caseID,
	)
	require.NoError(t, err)
	reviewLease, acquired, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{
			CaseID:           caseID,
			AttemptID:        attemptID,
			ExpectedRevision: parked.Revision,
			Kind:             PRDevelopmentControllerReviewLease,
			WorkerLabel:      "ledger-review-fixture",
			Lease:            time.Minute,
		},
	)
	require.NoError(t, err)
	require.True(t, acquired)
	return reviewLease.Controller
}

func completePRDevelopmentLedgerForTest(
	t *testing.T,
	databasePath string,
) (prDevelopmentLedgerTestFixture, PRDevelopmentLedgerEntry, PRDevelopmentLedgerEntry) {
	t.Helper()
	fixture := newParkedPRDevelopmentLedgerFixtureForTest(t, databasePath)
	ctx := context.Background()
	attempt, changed, err := fixture.Store.AppendPRDevelopmentLedgerAttempt(
		ctx,
		validPRDevelopmentLedgerAttemptAppendForTest(
			fixture.DevelopmentCase.ID,
			fixture.Attempt.ID,
		),
	)
	require.NoError(t, err)
	require.True(t, changed)
	reviewLease := acquireParkedPRDevelopmentLedgerReviewForTest(
		t,
		fixture.Store,
		fixture.DevelopmentCase.ID,
		fixture.Attempt.ID,
	)
	review, changed, err := fixture.Store.AppendPRDevelopmentLedgerReview(
		ctx,
		validPRDevelopmentLedgerReviewAppendForTest(
			fixture.DevelopmentCase.ID,
			fixture.Attempt.ID,
			reviewLease,
		),
	)
	require.NoError(t, err)
	require.True(t, changed)
	return fixture, attempt, review
}

func validPRDevelopmentLedgerAttemptAppendForTest(
	caseID, attemptID string,
) PRDevelopmentLedgerAttemptAppend {
	return PRDevelopmentLedgerAttemptAppend{
		CaseID:         caseID,
		AttemptID:      attemptID,
		Summary:        "Applied the local repair and passed focused CI.",
		CIPlanDigest:   strings.Repeat("1", 64),
		CIResultDigest: strings.Repeat("2", 64),
	}
}

func validPRDevelopmentLedgerReviewAppendForTest(
	caseID, attemptID string,
	reviewLease PRDevelopmentController,
) PRDevelopmentLedgerReviewAppend {
	line := 19
	return PRDevelopmentLedgerReviewAppend{
		CaseID:           caseID,
		AttemptID:        attemptID,
		ControllerID:     reviewLease.ID,
		ExpectedRevision: reviewLease.Revision,
		LeaseToken:       reviewLease.LeaseToken,
		LeaseEpoch:       reviewLease.LeaseEpoch,
		Summary:          "The local review found one actionable issue.",
		Outcome:          PRDevelopmentLedgerReviewChangesRequired,
		Findings: []PRDevelopmentLedgerReviewFinding{
			{
				Severity:       ReviewSeverityMedium,
				Title:          "Retain the exact reviewed candidate",
				File:           "pkg/example/example.go",
				Line:           &line,
				Message:        "The candidate proof needs another bounded repair.",
				Evidence:       "The review digest records the mismatch.",
				Impact:         "Publication is not ready.",
				Recommendation: "Repair and validate the retained line again.",
				Validation:     "Run the targeted test against the exact tree.",
			},
		},
	}
}
