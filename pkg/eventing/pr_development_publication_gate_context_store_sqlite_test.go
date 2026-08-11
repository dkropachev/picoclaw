//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPRDevelopmentPublicationGateContextSnapshotProjectsExactPrivateEvidence(
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
			Content:         "Check the exact passed candidate before publishing it.",
		},
	)
	require.NoError(t, err)

	snapshot, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)

	expectedPublication, err := fixture.Store.GetPRDevelopmentPublication(
		ctx,
		fixture.Claim.ID,
	)
	require.NoError(t, err)
	expectedThread, err := fixture.Store.GetPRDevelopmentThreadForCase(
		ctx,
		fixture.Orchestration.Operation.Case.ID,
	)
	require.NoError(t, err)
	expectedController, err := fixture.Store.GetPRDevelopmentControllerForCase(
		ctx,
		fixture.Orchestration.Operation.Case.ID,
	)
	require.NoError(t, err)
	expectedOrchestration, err := fixture.Store.GetPRDevelopmentRepairOrchestration(
		ctx,
		fixture.Publication.AttemptID,
	)
	require.NoError(t, err)
	expectedLedger, err := fixture.Store.GetPRDevelopmentLedgerForCase(
		ctx,
		fixture.Orchestration.Operation.Case.ID,
	)
	require.NoError(t, err)
	expectedWorkbench, err := fixture.Store.GetPRDevelopmentWorkbench(
		ctx,
		fixture.Orchestration.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, expectedWorkbench.RepairSession)
	require.NotEmpty(t, expectedWorkbench.RepairSession.ReservationKey)
	expectedOwner := *expectedWorkbench.RepairSession
	expectedOwner.ReservationKey = ""
	expectedOwner.Attempts = append(
		[]PRDevelopmentRepairAttempt(nil),
		expectedOwner.Attempts...,
	)
	for index := range expectedOwner.Attempts {
		expectedOwner.Attempts[index].IdempotencyKey = ""
		expectedOwner.Attempts[index].LeaseOwner = ""
		expectedOwner.Attempts[index].LeaseToken = ""
		expectedOwner.Attempts[index].LeaseUntil = nil
	}

	assert.Equal(t, expectedPublication, snapshot.Publication)
	assert.Empty(t, snapshot.Publication.ClaimToken)
	assert.Equal(t, PRDevelopmentPublicationClaimed, snapshot.Publication.Status)
	assert.Equal(t, PRDevelopmentPublicationPending, snapshot.Publication.ClaimFrom)
	assert.Equal(t, fixture.Orchestration.Operation.Case, snapshot.Case)
	assert.Equal(t, expectedThread, snapshot.Thread)
	require.NotEmpty(t, snapshot.Thread.Cases)
	assert.Equal(t, snapshot.Thread.Cases[len(snapshot.Thread.Cases)-1].Ordinal, snapshot.SelectedOrdinal)
	assert.Equal(t, conversation, snapshot.Conversation)
	assert.Equal(
		t,
		prDevelopmentTranscriptDigestForTest(t, conversation.Messages),
		snapshot.TranscriptDigest,
	)
	assert.Equal(t, expectedOwner, snapshot.OwnerSession)
	assert.Empty(t, snapshot.OwnerSession.ReservationKey)
	for _, attempt := range snapshot.OwnerSession.Attempts {
		assert.Empty(t, attempt.IdempotencyKey)
		assert.Empty(t, attempt.LeaseOwner)
		assert.Empty(t, attempt.LeaseToken)
		assert.Nil(t, attempt.LeaseUntil)
	}
	assert.Equal(t, expectedController, snapshot.Controller)
	assert.Equal(t, expectedOrchestration, snapshot.Orchestration)
	assert.Equal(t, expectedLedger, snapshot.Ledger)
	assert.Equal(t, snapshot.Publication.FenceOrdinal, snapshot.Fence.Ordinal)
	assert.Equal(t, snapshot.Publication.FenceHash, snapshot.Fence.FenceHash)
	assert.Equal(t, snapshot.Publication.AttemptID, snapshot.Fence.AttemptID)
	assert.Equal(t, snapshot.Publication.AttemptLedgerEntryID, snapshot.AttemptEntry.ID)
	assert.Equal(t, snapshot.Publication.AttemptLedgerEntryHash, snapshot.AttemptEntry.EntryHash)
	assert.Equal(t, snapshot.SelectedOrdinal, snapshot.AttemptEntry.CaseOrdinal)
	assert.Equal(t, snapshot.Publication.ReviewLedgerEntryID, snapshot.ReviewEntry.ID)
	assert.Equal(t, snapshot.Publication.ReviewLedgerEntryHash, snapshot.ReviewEntry.EntryHash)
	assert.Equal(t, snapshot.SelectedOrdinal, snapshot.ReviewEntry.CaseOrdinal)
	assert.Equal(t, PRDevelopmentLedgerReviewPassed, snapshot.ReviewEntry.ReviewOutcome)

	for _, value := range []any{
		snapshot,
		PRDevelopmentPublicationSubjectPin{
			PublicationID:               "pdpub_secret",
			ClaimToken:                  "claim-secret",
			PolicyRevision:              "sha256:policy-secret",
			SubjectRevision:             "sha256:subject-secret",
			PinnedSubject:               json.RawMessage(`{"secret":true}`),
			ExpectedConversationVersion: conversation.Version,
			ExpectedTranscriptDigest:    snapshot.TranscriptDigest,
		},
		PRDevelopmentPublicationGateContextAnchor{
			SubjectRevision:     "sha256:subject-secret",
			ConversationVersion: conversation.Version,
			TranscriptDigest:    snapshot.TranscriptDigest,
		},
	} {
		encoded, encodeErr := json.Marshal(value)
		require.NoError(t, encodeErr)
		assert.JSONEq(t, `{}`, string(encoded))
	}
}

func TestPRDevelopmentPublicationPinnedGateContextSnapshotReturnsExactPrefix(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	_, err := fixture.Store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          fixture.Orchestration.Operation.Case.ID,
			ExpectedVersion: 0,
			Role:            PRDevelopmentMessageUser,
			Content:         "Use this exact conversation prefix for the gate.",
		},
	)
	require.NoError(t, err)
	captured, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)
	policyRevision := pinPRDevelopmentPublicationGatePolicyForContextTest(t, &fixture)
	subjectPin := publicationGateSubjectPinForContextTest(
		&fixture,
		policyRevision,
		captured,
	)
	_, changed, err := fixture.Store.PinPRDevelopmentPublicationSubject(ctx, subjectPin)
	require.NoError(t, err)
	require.True(t, changed)

	later, err := fixture.Store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          captured.Case.ID,
			ExpectedVersion: captured.Conversation.Version,
			Role:            PRDevelopmentMessageAssistant,
			Content:         "This later message must not change the pinned gate subject.",
		},
	)
	require.NoError(t, err)
	require.Greater(t, later.Version, captured.Conversation.Version)

	current, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)
	anchored, err := fixture.Store.GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
		publicationGateContextAnchorForContextTest(subjectPin),
	)
	require.NoError(t, err)

	expected := current
	expected.Conversation = captured.Conversation
	expected.TranscriptDigest = captured.TranscriptDigest
	assert.Equal(t, expected, anchored)
	assert.Equal(t, later, current.Conversation)
	assert.NotEqual(t, current.Conversation, anchored.Conversation)
	assert.Equal(t, subjectPin.SubjectRevision, anchored.Publication.SubjectRevision)
	assert.Empty(t, anchored.Publication.ClaimToken)
}

func TestPRDevelopmentPublicationPinnedGateContextSnapshotRejectsAlternateValidPrefix(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	_, err := fixture.Store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          fixture.Orchestration.Operation.Case.ID,
			ExpectedVersion: 0,
			Role:            PRDevelopmentMessageUser,
			Content:         "Bind the durable subject to this conversation prefix.",
		},
	)
	require.NoError(t, err)
	captured, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)
	require.Positive(t, captured.Conversation.Version)
	policyRevision := pinPRDevelopmentPublicationGatePolicyForContextTest(t, &fixture)
	subjectPin := publicationGateSubjectPinForContextTest(
		&fixture,
		policyRevision,
		captured,
	)
	_, changed, err := fixture.Store.PinPRDevelopmentPublicationSubject(ctx, subjectPin)
	require.NoError(t, err)
	require.True(t, changed)

	_, err = fixture.Store.GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
		PRDevelopmentPublicationGateContextAnchor{
			SubjectRevision:     subjectPin.SubjectRevision,
			ConversationVersion: 0,
			TranscriptDigest:    emptyPRDevelopmentTranscriptDigest(),
		},
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentPublicationConflict)
}

func TestPRDevelopmentPublicationPinnedGateContextSnapshotFailsClosedOnAnchor(
	t *testing.T,
) {
	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	snapshot, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)
	policyRevision := pinPRDevelopmentPublicationGatePolicyForContextTest(t, &fixture)
	subjectPin := publicationGateSubjectPinForContextTest(
		&fixture,
		policyRevision,
		snapshot,
	)
	anchor := publicationGateContextAnchorForContextTest(subjectPin)

	_, err = fixture.Store.GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
		anchor,
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentPublicationConflict)

	_, changed, err := fixture.Store.PinPRDevelopmentPublicationSubject(ctx, subjectPin)
	require.NoError(t, err)
	require.True(t, changed)
	wrongDigest := strings.Repeat("0", 64)
	if wrongDigest == anchor.TranscriptDigest {
		wrongDigest = strings.Repeat("1", 64)
	}
	for _, test := range []struct {
		name       string
		claimToken string
		claimEpoch int64
		anchor     PRDevelopmentPublicationGateContextAnchor
		want       error
	}{
		{
			name:       "wrong claim token",
			claimToken: strings.Repeat("f", len(fixture.Claim.ClaimToken)),
			claimEpoch: fixture.Claim.ClaimEpoch,
			anchor:     anchor,
			want:       ErrStaleLease,
		},
		{
			name:       "malformed subject revision",
			claimToken: fixture.Claim.ClaimToken,
			claimEpoch: fixture.Claim.ClaimEpoch,
			anchor: PRDevelopmentPublicationGateContextAnchor{
				SubjectRevision:     "subject",
				ConversationVersion: anchor.ConversationVersion,
				TranscriptDigest:    anchor.TranscriptDigest,
			},
			want: ErrInvalidPRDevelopmentPublication,
		},
		{
			name:       "different pinned subject",
			claimToken: fixture.Claim.ClaimToken,
			claimEpoch: fixture.Claim.ClaimEpoch,
			anchor: PRDevelopmentPublicationGateContextAnchor{
				SubjectRevision:     "sha256:" + strings.Repeat("c", 64),
				ConversationVersion: anchor.ConversationVersion,
				TranscriptDigest:    anchor.TranscriptDigest,
			},
			want: ErrPRDevelopmentPublicationConflict,
		},
		{
			name:       "negative conversation version",
			claimToken: fixture.Claim.ClaimToken,
			claimEpoch: fixture.Claim.ClaimEpoch,
			anchor: PRDevelopmentPublicationGateContextAnchor{
				SubjectRevision:     anchor.SubjectRevision,
				ConversationVersion: -1,
				TranscriptDigest:    anchor.TranscriptDigest,
			},
			want: ErrInvalidPRDevelopmentPublication,
		},
		{
			name:       "future conversation version",
			claimToken: fixture.Claim.ClaimToken,
			claimEpoch: fixture.Claim.ClaimEpoch,
			anchor: PRDevelopmentPublicationGateContextAnchor{
				SubjectRevision:     anchor.SubjectRevision,
				ConversationVersion: snapshot.Conversation.Version + 1,
				TranscriptDigest:    anchor.TranscriptDigest,
			},
			want: ErrPRDevelopmentPublicationConflict,
		},
		{
			name:       "malformed transcript digest",
			claimToken: fixture.Claim.ClaimToken,
			claimEpoch: fixture.Claim.ClaimEpoch,
			anchor: PRDevelopmentPublicationGateContextAnchor{
				SubjectRevision:     anchor.SubjectRevision,
				ConversationVersion: anchor.ConversationVersion,
				TranscriptDigest:    strings.Repeat("A", 64),
			},
			want: ErrInvalidPRDevelopmentPublication,
		},
		{
			name:       "changed transcript digest",
			claimToken: fixture.Claim.ClaimToken,
			claimEpoch: fixture.Claim.ClaimEpoch,
			anchor: PRDevelopmentPublicationGateContextAnchor{
				SubjectRevision:     anchor.SubjectRevision,
				ConversationVersion: anchor.ConversationVersion,
				TranscriptDigest:    wrongDigest,
			},
			want: ErrPRDevelopmentPublicationConflict,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, readErr := fixture.Store.GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot(
				ctx,
				fixture.Claim.ID,
				test.claimToken,
				test.claimEpoch,
				test.anchor,
			)
			assert.ErrorIs(t, readErr, test.want)
		})
	}

	exact, err := fixture.Store.GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
		anchor,
	)
	require.NoError(t, err)
	assert.Equal(t, snapshot.Conversation, exact.Conversation)
	assert.Equal(t, snapshot.TranscriptDigest, exact.TranscriptDigest)
}

func TestPRDevelopmentPublicationGateContextSnapshotRejectsInvalidOrStaleAuthority(
	t *testing.T,
) {
	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	for _, test := range []struct {
		name          string
		publicationID string
		claimToken    string
		claimEpoch    int64
		want          error
	}{
		{
			name:          "missing publication",
			publicationID: "",
			claimToken:    fixture.Claim.ClaimToken,
			claimEpoch:    fixture.Claim.ClaimEpoch,
			want:          ErrInvalidPRDevelopmentPublication,
		},
		{
			name:          "missing token",
			publicationID: fixture.Claim.ID,
			claimToken:    "",
			claimEpoch:    fixture.Claim.ClaimEpoch,
			want:          ErrInvalidPRDevelopmentPublication,
		},
		{
			name:          "invalid epoch",
			publicationID: fixture.Claim.ID,
			claimToken:    fixture.Claim.ClaimToken,
			claimEpoch:    0,
			want:          ErrInvalidPRDevelopmentPublication,
		},
		{
			name:          "wrong token",
			publicationID: fixture.Claim.ID,
			claimToken:    strings.Repeat("f", len(fixture.Claim.ClaimToken)),
			claimEpoch:    fixture.Claim.ClaimEpoch,
			want:          ErrStaleLease,
		},
		{
			name:          "wrong epoch",
			publicationID: fixture.Claim.ID,
			claimToken:    fixture.Claim.ClaimToken,
			claimEpoch:    fixture.Claim.ClaimEpoch + 1,
			want:          ErrStaleLease,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
				ctx,
				test.publicationID,
				test.claimToken,
				test.claimEpoch,
			)
			assert.ErrorIs(t, err, test.want)
		})
	}

	*fixture.Clock = fixture.Claim.ClaimUntil.Add(time.Nanosecond)
	_, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	assert.ErrorIs(t, err, ErrStaleLease)
}

func TestPRDevelopmentPublicationGateContextSnapshotHonorsCancellation(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestPRDevelopmentPublicationGateContextSnapshotRejectsWrongClaimPhase(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	snapshot, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)
	policyRevision := pinPRDevelopmentPublicationGatePolicyForContextTest(t, &fixture)
	anchor := publicationGateContextAnchorForContextTest(
		publicationGateSubjectPinForContextTest(&fixture, policyRevision, snapshot),
	)
	pinPRDevelopmentPublicationGateSubjectForContextTest(
		t,
		&fixture,
		policyRevision,
		snapshot,
	)
	*fixture.Clock = fixture.Clock.Add(time.Second)
	_, changed, err := fixture.Store.PinPRDevelopmentPublicationProvider(
		ctx,
		PRDevelopmentPublicationProviderPin{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken,
			ClaimEpoch:    fixture.Claim.ClaimEpoch,
			Observation:   fixture.Observation,
			ObservedAt:    *fixture.Clock,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	_, changed, err = fixture.Store.MarkPRDevelopmentPublicationPushReady(
		ctx,
		PRDevelopmentPublicationMarkPushReady{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken,
			ClaimEpoch:    fixture.Claim.ClaimEpoch,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	claimed, err := fixture.Store.ClaimPRDevelopmentPublications(
		ctx,
		PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "publication-push-phase-worker",
			Limit:       1,
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, PRDevelopmentPublicationPushReady, claimed[0].ClaimFrom)

	_, err = fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		claimed[0].ID,
		claimed[0].ClaimToken,
		claimed[0].ClaimEpoch,
	)
	assert.ErrorIs(t, err, ErrStaleLease)
	_, err = fixture.Store.GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot(
		ctx,
		claimed[0].ID,
		claimed[0].ClaimToken,
		claimed[0].ClaimEpoch,
		anchor,
	)
	assert.ErrorIs(t, err, ErrStaleLease)
}

func TestPRDevelopmentPublicationSubjectPinConflictsAfterGateContextConversationDrift(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	snapshot, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)
	policyRevision := pinPRDevelopmentPublicationGatePolicyForContextTest(t, &fixture)

	_, err = fixture.Store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          snapshot.Case.ID,
			ExpectedVersion: snapshot.Conversation.Version,
			Role:            PRDevelopmentMessageUser,
			Content:         "Consider this newer instruction before publishing.",
		},
	)
	require.NoError(t, err)

	_, changed, err := fixture.Store.PinPRDevelopmentPublicationSubject(
		ctx,
		publicationGateSubjectPinForContextTest(&fixture, policyRevision, snapshot),
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentPublicationConflict)
	assert.False(t, changed)
	stored, loadErr := fixture.Store.GetPRDevelopmentPublication(ctx, fixture.Claim.ID)
	require.NoError(t, loadErr)
	assert.Empty(t, stored.SubjectRevision)
	assert.Empty(t, stored.PinnedSubject)
}

func TestPRDevelopmentPublicationSubjectPinRequiresConversationFence(t *testing.T) {
	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	snapshot, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)
	policyRevision := pinPRDevelopmentPublicationGatePolicyForContextTest(t, &fixture)
	valid := publicationGateSubjectPinForContextTest(&fixture, policyRevision, snapshot)

	for _, test := range []struct {
		name   string
		mutate func(*PRDevelopmentPublicationSubjectPin)
	}{
		{
			name: "negative version",
			mutate: func(input *PRDevelopmentPublicationSubjectPin) {
				input.ExpectedConversationVersion = -1
			},
		},
		{
			name: "missing transcript digest",
			mutate: func(input *PRDevelopmentPublicationSubjectPin) {
				input.ExpectedTranscriptDigest = ""
			},
		},
		{
			name: "noncanonical transcript digest",
			mutate: func(input *PRDevelopmentPublicationSubjectPin) {
				input.ExpectedTranscriptDigest = strings.ToUpper(input.ExpectedTranscriptDigest)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			_, changed, pinErr := fixture.Store.PinPRDevelopmentPublicationSubject(
				ctx,
				input,
			)
			assert.ErrorIs(t, pinErr, ErrInvalidPRDevelopmentPublication)
			assert.False(t, changed)
		})
	}
}

func TestPRDevelopmentPublicationSubjectPinExactReplayIgnoresLaterConversation(
	t *testing.T,
) {
	t.Parallel()

	ctx := context.Background()
	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	snapshot, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)
	policyRevision := pinPRDevelopmentPublicationGatePolicyForContextTest(t, &fixture)
	input := publicationGateSubjectPinForContextTest(&fixture, policyRevision, snapshot)
	pinned, changed, err := fixture.Store.PinPRDevelopmentPublicationSubject(ctx, input)
	require.NoError(t, err)
	require.True(t, changed)

	_, err = fixture.Store.AppendPRDevelopmentMessage(
		ctx,
		PRDevelopmentMessageAppend{
			CaseID:          snapshot.Case.ID,
			ExpectedVersion: snapshot.Conversation.Version,
			Role:            PRDevelopmentMessageAssistant,
			Content:         "This arrived after the exact subject was durably pinned.",
		},
	)
	require.NoError(t, err)
	replayed, changed, err := fixture.Store.PinPRDevelopmentPublicationSubject(ctx, input)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, pinned, replayed)
}

func TestPRDevelopmentPublicationGateContextSnapshotFailsClosedOnConversationTamper(
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
			Content:         "Integrity-check this private gate context.",
		},
	)
	require.NoError(t, err)
	require.NotEmpty(t, conversation.Messages)
	snapshot, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)
	policyRevision := pinPRDevelopmentPublicationGatePolicyForContextTest(t, &fixture)
	subjectPin := publicationGateSubjectPinForContextTest(
		&fixture,
		policyRevision,
		snapshot,
	)
	_, changed, err := fixture.Store.PinPRDevelopmentPublicationSubject(ctx, subjectPin)
	require.NoError(t, err)
	require.True(t, changed)
	_, err = fixture.Store.db.Exec(`
		UPDATE pr_development_conversations
		SET transcript_digest = ?
		WHERE case_id = ?`,
		strings.Repeat("0", 64),
		fixture.Orchestration.Operation.Case.ID,
	)
	require.NoError(t, err)

	_, err = fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.Error(t, err)
	_, err = fixture.Store.GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
		publicationGateContextAnchorForContextTest(subjectPin),
	)
	require.Error(t, err)
}

func pinPRDevelopmentPublicationGatePolicyForContextTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
) string {
	t.Helper()
	policyRevision := "sha256:" + strings.Repeat("a", 64)
	_, changed, err := fixture.Store.PinPRDevelopmentPublicationPolicy(
		context.Background(),
		PRDevelopmentPublicationPolicyPin{
			PublicationID:  fixture.Claim.ID,
			ClaimToken:     fixture.Claim.ClaimToken,
			ClaimEpoch:     fixture.Claim.ClaimEpoch,
			PolicyRevision: policyRevision,
			PinnedPolicy:   json.RawMessage(`{"gates":[{"type":"zero"}]}`),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	return policyRevision
}

func publicationGateSubjectPinForContextTest(
	fixture *prDevelopmentPublicationLifecycleFixture,
	policyRevision string,
	snapshot PRDevelopmentPublicationGateContextSnapshot,
) PRDevelopmentPublicationSubjectPin {
	pinnedSubject := json.RawMessage(
		`{"conversation_version":` +
			strconv.FormatInt(snapshot.Conversation.Version, 10) +
			`,"publication":"exact-passed-review","transcript_digest":"` +
			snapshot.TranscriptDigest + `"}`,
	)
	return PRDevelopmentPublicationSubjectPin{
		PublicationID:               fixture.Claim.ID,
		ClaimToken:                  fixture.Claim.ClaimToken,
		ClaimEpoch:                  fixture.Claim.ClaimEpoch,
		PolicyRevision:              policyRevision,
		SubjectRevision:             "sha256:" + strings.Repeat("b", 64),
		PinnedSubject:               pinnedSubject,
		ExpectedConversationVersion: snapshot.Conversation.Version,
		ExpectedTranscriptDigest:    snapshot.TranscriptDigest,
	}
}

func publicationGateContextAnchorForContextTest(
	input PRDevelopmentPublicationSubjectPin,
) PRDevelopmentPublicationGateContextAnchor {
	return PRDevelopmentPublicationGateContextAnchor{
		SubjectRevision:     input.SubjectRevision,
		ConversationVersion: input.ExpectedConversationVersion,
		TranscriptDigest:    input.ExpectedTranscriptDigest,
	}
}

func pinPRDevelopmentPublicationGateSubjectForContextTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
	policyRevision string,
	snapshot PRDevelopmentPublicationGateContextSnapshot,
) {
	t.Helper()
	_, changed, err := fixture.Store.PinPRDevelopmentPublicationSubject(
		context.Background(),
		publicationGateSubjectPinForContextTest(fixture, policyRevision, snapshot),
	)
	require.NoError(t, err)
	require.True(t, changed)
}
