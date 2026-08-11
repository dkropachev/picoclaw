//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPRDevelopmentPublicationGateClaimAuthenticatorReturnsRedactedPublication(
	t *testing.T,
) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	authentication, err := fixture.Store.AuthenticateClaimedPRDevelopmentPublicationGate(
		context.Background(),
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)

	expected, err := fixture.Store.GetPRDevelopmentPublication(
		context.Background(),
		fixture.Claim.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, expected, authentication.Publication)
	assert.Empty(t, authentication.Publication.ClaimToken)
	assert.Equal(t, PRDevelopmentPublicationClaimed, authentication.Publication.Status)
	assert.Equal(t, PRDevelopmentPublicationPending, authentication.Publication.ClaimFrom)
	assert.Equal(t, fixture.Orchestration.Operation.Case.Repository, authentication.Repository)
}

func TestPRDevelopmentPublicationGateClaimAuthenticatorRejectsInvalidOrStaleAuthority(
	t *testing.T,
) {
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	assertRejected := func(
		name string,
		publicationID string,
		claimToken string,
		claimEpoch int64,
		want error,
	) {
		t.Helper()
		_, err := fixture.Store.AuthenticateClaimedPRDevelopmentPublicationGate(
			context.Background(),
			publicationID,
			claimToken,
			claimEpoch,
		)
		assert.ErrorIs(t, err, want, name)
	}

	assertRejected(
		"missing publication",
		"",
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
		ErrInvalidPRDevelopmentPublication,
	)
	assertRejected(
		"missing token",
		fixture.Claim.ID,
		"",
		fixture.Claim.ClaimEpoch,
		ErrInvalidPRDevelopmentPublication,
	)
	assertRejected(
		"invalid epoch",
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		0,
		ErrInvalidPRDevelopmentPublication,
	)
	assertRejected(
		"wrong token",
		fixture.Claim.ID,
		strings.Repeat("f", len(fixture.Claim.ClaimToken)),
		fixture.Claim.ClaimEpoch,
		ErrStaleLease,
	)
	assertRejected(
		"wrong epoch",
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch+1,
		ErrStaleLease,
	)

	*fixture.Clock = fixture.Claim.ClaimUntil.Add(time.Nanosecond)
	_, err := fixture.Store.AuthenticateClaimedPRDevelopmentPublicationGate(
		context.Background(),
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	assert.ErrorIs(t, err, ErrStaleLease)
}

func TestPRDevelopmentPublicationGateClaimAuthenticatorRejectsNonInitialClaim(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	pinned := pinPRDevelopmentPublicationForTest(t, &fixture)
	ready, changed, err := fixture.Store.MarkPRDevelopmentPublicationPushReady(
		ctx,
		PRDevelopmentPublicationMarkPushReady{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken,
			ClaimEpoch:    fixture.Claim.ClaimEpoch,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, pinned.ID, ready.ID)

	claimed, err := fixture.Store.ClaimPRDevelopmentPublications(
		ctx,
		PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "publication-gate-auth-push-worker",
			Limit:       1,
			Lease:       time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, PRDevelopmentPublicationPushReady, claimed[0].ClaimFrom)

	_, err = fixture.Store.AuthenticateClaimedPRDevelopmentPublicationGate(
		ctx,
		claimed[0].ID,
		claimed[0].ClaimToken,
		claimed[0].ClaimEpoch,
	)
	assert.ErrorIs(t, err, ErrStaleLease)
}

func TestPRDevelopmentPublicationGateClaimAuthenticatorRejectsSupersededHighWater(
	t *testing.T,
) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	_ = captureAdditionalPRDevelopmentThreadCase(
		t,
		fixture.Store,
		fixture.Clock,
		validPRDevelopmentInputForTest(),
		"delivery-publication-gate-auth-newer-review",
		"799",
	)

	_, err := fixture.Store.AuthenticateClaimedPRDevelopmentPublicationGate(
		context.Background(),
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentPublicationSuperseded)
}

func TestPRDevelopmentPublicationGateClaimAuthenticatorDoesNotReadConversation(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	conversation, err := fixture.Store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          fixture.Orchestration.Operation.Case.ID,
			ExpectedVersion: 0,
			Role:            PRDevelopmentMessageUser,
			Content:         "This context is intentionally outside narrow authentication.",
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

	authentication, err := fixture.Store.AuthenticateClaimedPRDevelopmentPublicationGate(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)
	assert.Equal(t, fixture.Claim.ID, authentication.Publication.ID)
	assert.Empty(t, authentication.Publication.ClaimToken)
	assert.Equal(t, fixture.Orchestration.Operation.Case.Repository, authentication.Repository)
}

func TestPRDevelopmentPublicationGateClaimAuthenticatorRejectsProviderWithoutSubject(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	pinned := pinPRDevelopmentPublicationForTest(t, &fixture)
	require.NotEmpty(t, pinned.ProviderObservationJSON)
	require.NotEmpty(t, pinned.SubjectRevision)
	_, err := fixture.Store.db.Exec(`
		UPDATE pr_development_publications
		SET subject_revision = '', pinned_subject_json = X'', pinned_subject_hash = ''
		WHERE id = ?`,
		fixture.Claim.ID,
	)
	require.NoError(t, err)

	_, err = fixture.Store.AuthenticateClaimedPRDevelopmentPublicationGate(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	assert.ErrorContains(t, err, "stored publication provider pin is invalid")
}
