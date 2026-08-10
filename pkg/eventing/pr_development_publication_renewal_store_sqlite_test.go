//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPRDevelopmentPublicationQueueRenewalRejectsPushStarted(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	started, pushClaim := startPRDevelopmentPublicationForTest(t, &fixture)
	require.NotNil(t, started.ClaimUntil)
	before, err := getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		started.ID,
	)
	require.NoError(t, err)
	*fixture.Clock = fixture.Clock.Add(time.Minute)
	input := PRDevelopmentPublicationRenew{
		PublicationID: started.ID,
		ClaimToken:    pushClaim.ClaimToken,
		ClaimEpoch:    pushClaim.ClaimEpoch,
		Lease:         10 * time.Minute,
	}

	err = fixture.Store.RenewPRDevelopmentPublication(context.Background(), input)
	assert.ErrorIs(t, err, ErrStaleLease)
	unchanged, loadErr := getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		started.ID,
	)
	require.NoError(t, loadErr)
	assert.Equal(t, before.ClaimUntil, unchanged.ClaimUntil)
	assert.Equal(t, before.UpdatedAt, unchanged.UpdatedAt)

	err = fixture.Store.RenewPRDevelopmentPublicationPush(context.Background(), input)
	require.NoError(t, err)
	renewed, loadErr := getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		started.ID,
	)
	require.NoError(t, loadErr)
	require.NotNil(t, renewed.ClaimUntil)
	assert.True(t, renewed.ClaimUntil.After(*before.ClaimUntil))
	assert.Equal(t, *fixture.Clock, renewed.UpdatedAt)
}

func TestPRDevelopmentPublicationPushRenewalRejectsPreEffectClaim(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	require.Equal(t, PRDevelopmentPublicationClaimed, fixture.Claim.Status)
	require.NotNil(t, fixture.Claim.ClaimUntil)
	before, err := getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		fixture.Claim.ID,
	)
	require.NoError(t, err)
	*fixture.Clock = fixture.Clock.Add(time.Minute)
	input := PRDevelopmentPublicationRenew{
		PublicationID: fixture.Claim.ID,
		ClaimToken:    fixture.Claim.ClaimToken,
		ClaimEpoch:    fixture.Claim.ClaimEpoch,
		Lease:         10 * time.Minute,
	}

	err = fixture.Store.RenewPRDevelopmentPublicationPush(context.Background(), input)
	assert.ErrorIs(t, err, ErrStaleLease)
	unchanged, loadErr := getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		fixture.Claim.ID,
	)
	require.NoError(t, loadErr)
	assert.Equal(t, before.ClaimUntil, unchanged.ClaimUntil)
	assert.Equal(t, before.UpdatedAt, unchanged.UpdatedAt)

	err = fixture.Store.RenewPRDevelopmentPublication(context.Background(), input)
	require.NoError(t, err)
	renewed, loadErr := getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		fixture.Claim.ID,
	)
	require.NoError(t, loadErr)
	require.NotNil(t, renewed.ClaimUntil)
	assert.True(t, renewed.ClaimUntil.After(*before.ClaimUntil))
	assert.Equal(t, *fixture.Clock, renewed.UpdatedAt)
}
