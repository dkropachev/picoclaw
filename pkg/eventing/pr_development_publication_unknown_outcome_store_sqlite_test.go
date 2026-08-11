//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPRDevelopmentPublicationUnknownOutcomeListDefaultsCapsAndValidatesCursor(
	t *testing.T,
) {
	t.Parallel()

	plan, err := buildPRDevelopmentPublicationUnknownOutcomeListPlan(
		PRDevelopmentPublicationUnknownOutcomeFilter{},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, plan.limit)
	require.NotEmpty(t, plan.args)
	assert.Equal(t, 2, plan.args[len(plan.args)-1])

	plan, err = buildPRDevelopmentPublicationUnknownOutcomeListPlan(
		PRDevelopmentPublicationUnknownOutcomeFilter{Limit: -1},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, plan.limit)

	plan, err = buildPRDevelopmentPublicationUnknownOutcomeListPlan(
		PRDevelopmentPublicationUnknownOutcomeFilter{
			Limit: maxPRDevelopmentPublicationClaimLimit + 100,
		},
	)
	require.NoError(t, err)
	assert.Equal(t, maxPRDevelopmentPublicationClaimLimit, plan.limit)
	assert.Equal(
		t,
		maxPRDevelopmentPublicationClaimLimit+1,
		plan.args[len(plan.args)-1],
	)

	createdAt := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	availableAt := createdAt.Add(time.Second)
	publicationID := "pdpub_" + strings.Repeat("a", 32)
	valid := PRDevelopmentPublicationUnknownOutcomeCursor{
		AvailableAt: availableAt,
		CreatedAt:   createdAt,
		ID:          publicationID,
	}
	plan, err = buildPRDevelopmentPublicationUnknownOutcomeListPlan(
		PRDevelopmentPublicationUnknownOutcomeFilter{After: &valid, Limit: 3},
	)
	require.NoError(t, err)
	assert.Equal(t, 3, plan.limit)
	assert.Contains(t, plan.query, "available_at > ?")

	nonUTC := time.Date(
		2026,
		time.August,
		10,
		12,
		0,
		0,
		0,
		time.FixedZone("noncanonical-utc", 0),
	)
	for _, test := range []struct {
		name   string
		cursor PRDevelopmentPublicationUnknownOutcomeCursor
	}{
		{name: "all absent", cursor: PRDevelopmentPublicationUnknownOutcomeCursor{}},
		{
			name: "available absent",
			cursor: PRDevelopmentPublicationUnknownOutcomeCursor{
				CreatedAt: createdAt,
				ID:        publicationID,
			},
		},
		{
			name: "created absent",
			cursor: PRDevelopmentPublicationUnknownOutcomeCursor{
				AvailableAt: availableAt,
				ID:          publicationID,
			},
		},
		{
			name: "available non UTC",
			cursor: PRDevelopmentPublicationUnknownOutcomeCursor{
				AvailableAt: nonUTC,
				CreatedAt:   createdAt,
				ID:          publicationID,
			},
		},
		{
			name: "created non UTC",
			cursor: PRDevelopmentPublicationUnknownOutcomeCursor{
				AvailableAt: availableAt,
				CreatedAt:   nonUTC,
				ID:          publicationID,
			},
		},
		{
			name: "availability before creation",
			cursor: PRDevelopmentPublicationUnknownOutcomeCursor{
				AvailableAt: createdAt.Add(-time.Second),
				CreatedAt:   createdAt,
				ID:          publicationID,
			},
		},
		{
			name: "invalid ID",
			cursor: PRDevelopmentPublicationUnknownOutcomeCursor{
				AvailableAt: availableAt,
				CreatedAt:   createdAt,
				ID:          " " + publicationID,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, planErr := buildPRDevelopmentPublicationUnknownOutcomeListPlan(
				PRDevelopmentPublicationUnknownOutcomeFilter{After: &test.cursor},
			)
			assert.ErrorIs(t, planErr, ErrInvalidPRDevelopmentPublication)
		})
	}
}

func TestPRDevelopmentPublicationUnknownOutcomeListFiltersOrdersAndPages(t *testing.T) {
	t.Parallel()

	first := newPRDevelopmentPublicationLifecycleFixture(t)
	firstUnknown := finalizePRDevelopmentPublicationUnknownOutcomeForListTest(t, &first)

	*first.Clock = first.Clock.Add(time.Minute)
	secondInput := validPRDevelopmentInputForTest()
	secondInput.PRDevelopmentCaptureIdentity = addPRDevelopmentDispatch(
		t,
		first.Store,
		"delivery-publication-unknown-second",
		"workflows/own-pr-feedback.yml",
		"revision-2026-08-10",
	)
	secondInput.Repository = "acme/second-project"
	secondInput.BaseRepository = "Acme/Second-Project"
	secondInput.PullNumber = 43
	secondInput.PullURL = "https://github.com/acme/second-project/pull/43"
	secondInput.ReviewID = "502"
	secondInput.TriggerReviewNodeID = "PRR_kwDOReview502"
	secondInput.ReviewURL = "https://github.com/acme/second-project/pull/43#pullrequestreview-502"
	secondOrchestration := newPRDevelopmentAIReviewOrchestrationOnStore(
		t,
		first.Store,
		first.Clock,
		secondInput,
		"publication-unknown-second-attempt",
		"publication-unknown-second-workspace",
		6901,
	)
	completePRDevelopmentAIReviewFixture(
		t,
		secondOrchestration,
		PRDevelopmentCIPassed,
		6902,
	)
	second := completePRDevelopmentPublicationLifecycleFixture(t, secondOrchestration)
	secondStarted, secondPushClaim := startPRDevelopmentPublicationForTest(t, &second)

	page, err := first.Store.ListPRDevelopmentPublicationUnknownOutcomes(
		context.Background(),
		PRDevelopmentPublicationUnknownOutcomeFilter{Limit: 64},
	)
	require.NoError(t, err)
	require.Len(t, page.Publications, 1)
	assert.Equal(t, firstUnknown.ID, page.Publications[0].ID)
	assert.Nil(t, page.Next)
	assert.Empty(t, page.Publications[0].ClaimToken)

	secondUnknown := finalizeStartedPRDevelopmentPublicationUnknownOutcomeForListTest(
		t,
		&second,
		secondStarted,
		secondPushClaim,
	)
	page, err = first.Store.ListPRDevelopmentPublicationUnknownOutcomes(
		context.Background(),
		PRDevelopmentPublicationUnknownOutcomeFilter{},
	)
	require.NoError(t, err)
	require.Len(t, page.Publications, 1)
	assert.Equal(t, firstUnknown.ID, page.Publications[0].ID)
	require.NotNil(t, page.Next)
	assert.Equal(t, firstUnknown.AvailableAt, page.Next.AvailableAt)
	assert.Equal(t, firstUnknown.CreatedAt, page.Next.CreatedAt)
	assert.Equal(t, firstUnknown.ID, page.Next.ID)

	secondPage, err := first.Store.ListPRDevelopmentPublicationUnknownOutcomes(
		context.Background(),
		PRDevelopmentPublicationUnknownOutcomeFilter{After: page.Next, Limit: 1},
	)
	require.NoError(t, err)
	require.Len(t, secondPage.Publications, 1)
	assert.Equal(t, secondUnknown.ID, secondPage.Publications[0].ID)
	assert.Nil(t, secondPage.Next)
	assert.Empty(t, secondPage.Publications[0].ClaimToken)

	afterLast := PRDevelopmentPublicationUnknownOutcomeCursor{
		AvailableAt: secondUnknown.AvailableAt,
		CreatedAt:   secondUnknown.CreatedAt,
		ID:          secondUnknown.ID,
	}
	empty, err := first.Store.ListPRDevelopmentPublicationUnknownOutcomes(
		context.Background(),
		PRDevelopmentPublicationUnknownOutcomeFilter{After: &afterLast, Limit: 64},
	)
	require.NoError(t, err)
	assert.Empty(t, empty.Publications)
	assert.Nil(t, empty.Next)

	all, err := first.Store.ListPRDevelopmentPublicationUnknownOutcomes(
		context.Background(),
		PRDevelopmentPublicationUnknownOutcomeFilter{
			Limit: maxPRDevelopmentPublicationClaimLimit + 1,
		},
	)
	require.NoError(t, err)
	require.Len(t, all.Publications, 2)
	assert.Equal(t, []string{firstUnknown.ID, secondUnknown.ID}, []string{
		all.Publications[0].ID,
		all.Publications[1].ID,
	})
	assert.Nil(t, all.Next)
}

func TestPRDevelopmentPublicationUnknownOutcomeListRejectsCorruptRow(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	unknown := finalizePRDevelopmentPublicationUnknownOutcomeForListTest(t, &fixture)
	changedHash := strings.Repeat("0", 64)
	if changedHash == unknown.PushRequestHash {
		changedHash = strings.Repeat("f", 64)
	}
	_, err := fixture.Store.db.Exec(`
		UPDATE pr_development_publications
		SET push_request_hash = ?
		WHERE id = ?`, changedHash, unknown.ID)
	require.NoError(t, err)

	page, err := fixture.Store.ListPRDevelopmentPublicationUnknownOutcomes(
		context.Background(),
		PRDevelopmentPublicationUnknownOutcomeFilter{Limit: 64},
	)
	assert.Error(t, err)
	assert.Empty(t, page.Publications)
	assert.Nil(t, page.Next)
}

func TestPRDevelopmentPublicationUnknownOutcomeListSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publication-unknown-reopen.db")
	fixture := newPRDevelopmentPublicationLifecycleFixtureAt(t, path)
	unknown := finalizePRDevelopmentPublicationUnknownOutcomeForListTest(t, &fixture)
	require.NoError(t, fixture.Store.Close())

	reopened, err := Open(
		context.Background(),
		path,
		WithClock(func() time.Time { return *fixture.Clock }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	page, err := reopened.ListPRDevelopmentPublicationUnknownOutcomes(
		context.Background(),
		PRDevelopmentPublicationUnknownOutcomeFilter{Limit: 64},
	)
	require.NoError(t, err)
	require.Len(t, page.Publications, 1)
	assert.Equal(t, unknown, page.Publications[0])
	assert.Nil(t, page.Next)
}

func finalizePRDevelopmentPublicationUnknownOutcomeForListTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
) PRDevelopmentPublication {
	t.Helper()
	started, pushClaim := startPRDevelopmentPublicationForTest(t, fixture)
	return finalizeStartedPRDevelopmentPublicationUnknownOutcomeForListTest(
		t,
		fixture,
		started,
		pushClaim,
	)
}

func finalizeStartedPRDevelopmentPublicationUnknownOutcomeForListTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
	started PRDevelopmentPublication,
	pushClaim PRDevelopmentPublication,
) PRDevelopmentPublication {
	t.Helper()
	*fixture.Clock = fixture.Clock.Add(time.Second)
	unknown, finalized, err := fixture.Store.FinalizePRDevelopmentPublicationPush(
		context.Background(),
		PRDevelopmentPublicationPushFinalize{
			PublicationID: started.ID,
			ClaimToken:    pushClaim.ClaimToken,
			ClaimEpoch:    pushClaim.ClaimEpoch,
			RequestHash:   started.PushRequestHash,
			Status:        PRDevelopmentPublicationOutcomeUnknown,
			ErrorCode:     PRDevelopmentPublicationErrorOutcomeUnknown,
			InternalError: "publication outcome is unknown in enumeration test",
		},
	)
	require.NoError(t, err)
	require.True(t, finalized)
	require.Equal(t, PRDevelopmentPublicationOutcomeUnknown, unknown.Status)
	return unknown
}
