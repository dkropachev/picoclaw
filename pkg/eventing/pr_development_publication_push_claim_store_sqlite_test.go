//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestPRDevelopmentPublicationPushAuthenticationReadErrorClassification(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	integrityErr := errors.New("stored high-water invariant is invalid")
	testCases := []struct {
		name            string
		err             error
		missingRecovery bool
		wantRecovery    bool
	}{
		{name: "missing publication", err: ErrNotFound},
		{
			name: "missing high-water", err: ErrNotFound,
			missingRecovery: true, wantRecovery: true,
		},
		{name: "plain integrity", err: integrityErr, wantRecovery: true},
		{name: "domain conflict", err: ErrPRDevelopmentPublicationConflict},
		{name: "domain supersession", err: ErrPRDevelopmentPublicationSuperseded},
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "closed", err: ErrClosed},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			classified := fixture.Store.
				classifyPRDevelopmentPublicationPushAuthenticationReadError(
					testCase.err,
					testCase.missingRecovery,
				)
			assert.ErrorIs(t, classified, testCase.err)
			if testCase.wantRecovery {
				assert.ErrorIs(t, classified, ErrPRDevelopmentPublicationRecoveryRequired)
			} else {
				assert.NotErrorIs(t, classified, ErrPRDevelopmentPublicationRecoveryRequired)
			}
		})
	}
	assert.True(t, operationalPRDevelopmentPublicationSQLiteError(sqlite3.SQLITE_BUSY_TIMEOUT))
	assert.True(t, operationalPRDevelopmentPublicationSQLiteError(sqlite3.SQLITE_IOERR_READ))
	assert.False(t, operationalPRDevelopmentPublicationSQLiteError(sqlite3.SQLITE_CORRUPT))
	assert.False(t, operationalPRDevelopmentPublicationSQLiteError(sqlite3.SQLITE_NOTADB))
}

func TestPRDevelopmentPublicationPushAuthenticationPreservesModerncOperationalError(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "publication-auth-busy.db")
	primary, err := Open(ctx, path, WithBusyTimeout(time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, primary.Close()) })
	contender, err := Open(ctx, path, WithBusyTimeout(time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, contender.Close()) })

	lock, err := primary.db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Close()) })
	_, err = lock.ExecContext(ctx, "BEGIN IMMEDIATE")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = lock.ExecContext(context.Background(), "ROLLBACK") })

	busyErr := contender.withImmediate(ctx, func(*sql.Conn) error { return nil })
	require.Error(t, busyErr)
	var driverErr *sqlite.Error
	require.ErrorAs(t, busyErr, &driverErr)
	assert.Equal(t, sqlite3.SQLITE_BUSY, driverErr.Code()&0xff)
	classified := primary.classifyPRDevelopmentPublicationPushAuthenticationReadError(
		busyErr,
		true,
	)
	assert.ErrorIs(t, classified, busyErr)
	assert.NotErrorIs(t, classified, ErrPRDevelopmentPublicationRecoveryRequired)
}

func TestPRDevelopmentPublicationPushClaimAuthenticatorReturnsExactRedactedHighWater(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	_, claim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)

	authentication, err := fixture.Store.AuthenticateClaimedPRDevelopmentPublicationPush(
		ctx,
		claim.ID,
		claim.ClaimToken,
		claim.ClaimEpoch,
	)
	require.NoError(t, err)

	expectedPublication, err := fixture.Store.GetPRDevelopmentPublication(ctx, claim.ID)
	require.NoError(t, err)
	expectedThread, err := fixture.Store.GetPRDevelopmentThreadForCase(
		ctx,
		fixture.Orchestration.Operation.Case.ID,
	)
	require.NoError(t, err)

	assert.Equal(t, expectedPublication, authentication.Publication)
	assert.Empty(t, authentication.Publication.ClaimToken)
	assert.Equal(t, PRDevelopmentPublicationClaimed, authentication.Publication.Status)
	assert.Equal(t, PRDevelopmentPublicationPushReady, authentication.Publication.ClaimFrom)
	assert.Equal(t, fixture.Orchestration.Operation.Case, authentication.Case)
	assert.Equal(t, expectedThread.Identity, authentication.ThreadIdentity)
}

func TestPRDevelopmentPublicationPushClaimAuthenticatorDoesNotReadConversation(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	_, claim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
	conversation, err := fixture.Store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          fixture.Orchestration.Operation.Case.ID,
			ExpectedVersion: 0,
			Role:            PRDevelopmentMessageUser,
			Content:         "This context is intentionally outside push authentication.",
		},
	)
	require.NoError(t, err)
	require.NotEmpty(t, conversation.Messages)
	_, err = fixture.Store.db.Exec(`
		UPDATE pr_development_conversations
		SET transcript_digest = ?
		WHERE case_id = ?`,
		strings.Repeat("0", 64),
		fixture.Orchestration.Operation.Case.ID,
	)
	require.NoError(t, err)

	authentication, err := fixture.Store.AuthenticateClaimedPRDevelopmentPublicationPush(
		ctx,
		claim.ID,
		claim.ClaimToken,
		claim.ClaimEpoch,
	)
	require.NoError(t, err)
	assert.Equal(t, claim.ID, authentication.Publication.ID)
	assert.Empty(t, authentication.Publication.ClaimToken)
}

func TestPRDevelopmentPublicationPushClaimAuthenticatorRejectsInvalidOrStaleAuthority(
	t *testing.T,
) {
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	_, claim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
	assertRejected := func(
		name string,
		publicationID string,
		claimToken string,
		claimEpoch int64,
		want error,
	) {
		t.Helper()
		_, err := fixture.Store.AuthenticateClaimedPRDevelopmentPublicationPush(
			context.Background(),
			publicationID,
			claimToken,
			claimEpoch,
		)
		assert.ErrorIs(t, err, want, name)
	}

	assertRejected(
		"missing publication ID",
		"",
		claim.ClaimToken,
		claim.ClaimEpoch,
		ErrInvalidPRDevelopmentPublication,
	)
	assertRejected(
		"missing token",
		claim.ID,
		"",
		claim.ClaimEpoch,
		ErrInvalidPRDevelopmentPublication,
	)
	assertRejected(
		"invalid epoch",
		claim.ID,
		claim.ClaimToken,
		0,
		ErrInvalidPRDevelopmentPublication,
	)
	assertRejected(
		"wrong token",
		claim.ID,
		strings.Repeat("f", len(claim.ClaimToken)),
		claim.ClaimEpoch,
		ErrStaleLease,
	)
	assertRejected(
		"wrong epoch",
		claim.ID,
		claim.ClaimToken,
		claim.ClaimEpoch+1,
		ErrStaleLease,
	)

	*fixture.Clock = claim.ClaimUntil.Add(time.Nanosecond)
	assertRejected(
		"expired claim",
		claim.ID,
		claim.ClaimToken,
		claim.ClaimEpoch,
		ErrStaleLease,
	)
}

func TestPRDevelopmentPublicationPushClaimAuthenticatorRejectsWrongOriginOrPhase(
	t *testing.T,
) {
	t.Parallel()

	t.Run("pending claim", func(t *testing.T) {
		t.Parallel()

		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		_, err := fixture.Store.AuthenticateClaimedPRDevelopmentPublicationPush(
			context.Background(),
			fixture.Claim.ID,
			fixture.Claim.ClaimToken,
			fixture.Claim.ClaimEpoch,
		)
		assert.ErrorIs(t, err, ErrStaleLease)
	})

	t.Run("push already started", func(t *testing.T) {
		t.Parallel()

		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		started, claim := startPRDevelopmentPublicationForTest(t, &fixture)
		require.Equal(t, PRDevelopmentPublicationPushStarted, started.Status)

		_, err := fixture.Store.AuthenticateClaimedPRDevelopmentPublicationPush(
			context.Background(),
			claim.ID,
			claim.ClaimToken,
			claim.ClaimEpoch,
		)
		assert.ErrorIs(t, err, ErrStaleLease)
	})
}

func TestPRDevelopmentPublicationPushClaimAuthenticatorRejectsMissingOrCorruptState(
	t *testing.T,
) {
	t.Parallel()

	t.Run("missing publication", func(t *testing.T) {
		t.Parallel()

		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		_, claim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
		missingID, err := newPrefixedID(prDevelopmentPublicationIDPrefix)
		require.NoError(t, err)

		_, err = fixture.Store.AuthenticateClaimedPRDevelopmentPublicationPush(
			context.Background(),
			missingID,
			claim.ClaimToken,
			claim.ClaimEpoch,
		)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("corrupt claimed publication", func(t *testing.T) {
		t.Parallel()

		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		_, claim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
		_, err := fixture.Store.db.Exec(`
			UPDATE pr_development_publications
			SET provider_observation_hash = ?
			WHERE id = ?`,
			strings.Repeat("f", 64),
			claim.ID,
		)
		require.NoError(t, err)

		_, err = fixture.Store.AuthenticateClaimedPRDevelopmentPublicationPush(
			context.Background(),
			claim.ID,
			claim.ClaimToken,
			claim.ClaimEpoch,
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentPublicationRecoveryRequired)
		assert.ErrorContains(t, err, "stored publication provider pin is invalid")
	})
}

func TestPRDevelopmentPublicationPushClaimAuthenticatorRejectsInvalidHighWater(
	t *testing.T,
) {
	t.Parallel()

	t.Run("missing case", func(t *testing.T) {
		t.Parallel()

		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		_, claim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
		_, err := fixture.Store.db.Exec(`PRAGMA foreign_keys = OFF`)
		require.NoError(t, err)
		_, err = fixture.Store.db.Exec(
			`DELETE FROM pr_development_cases WHERE id = ?`,
			fixture.Orchestration.Operation.Case.ID,
		)
		require.NoError(t, err)

		_, err = fixture.Store.AuthenticateClaimedPRDevelopmentPublicationPush(
			context.Background(),
			claim.ID,
			claim.ClaimToken,
			claim.ClaimEpoch,
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentPublicationRecoveryRequired)
		assert.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("corrupt provider thread identity", func(t *testing.T) {
		t.Parallel()

		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		_, claim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
		_, err := fixture.Store.db.Exec(`
			UPDATE pr_development_threads
			SET identity_hash = ?
			WHERE id = ?`,
			strings.Repeat("f", 64),
			claim.ThreadID,
		)
		require.NoError(t, err)

		_, err = fixture.Store.AuthenticateClaimedPRDevelopmentPublicationPush(
			context.Background(),
			claim.ID,
			claim.ClaimToken,
			claim.ClaimEpoch,
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentPublicationRecoveryRequired)
		assert.ErrorContains(
			t,
			err,
			"stored provider pull request development thread identity is invalid",
		)
	})

	t.Run("changed controller revision", func(t *testing.T) {
		t.Parallel()

		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		_, claim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
		_, err := fixture.Store.db.Exec(`
			UPDATE pr_development_publications
			SET controller_revision = controller_revision + 1
			WHERE id = ?`,
			claim.ID,
		)
		require.NoError(t, err)

		_, err = fixture.Store.AuthenticateClaimedPRDevelopmentPublicationPush(
			context.Background(),
			claim.ID,
			claim.ClaimToken,
			claim.ClaimEpoch,
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentPublicationConflict)
	})

	t.Run("newer review occurrence", func(t *testing.T) {
		t.Parallel()

		fixture := newPRDevelopmentPublicationLifecycleFixture(t)
		_, claim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
		_ = captureAdditionalPRDevelopmentThreadCase(
			t,
			fixture.Store,
			fixture.Clock,
			validPRDevelopmentInputForTest(),
			"delivery-publication-push-auth-newer-review",
			"898",
		)

		_, err := fixture.Store.AuthenticateClaimedPRDevelopmentPublicationPush(
			context.Background(),
			claim.ID,
			claim.ClaimToken,
			claim.ClaimEpoch,
		)
		assert.ErrorIs(t, err, ErrPRDevelopmentPublicationSuperseded)
	})
}
