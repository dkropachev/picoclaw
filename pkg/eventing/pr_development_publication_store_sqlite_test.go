//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type prDevelopmentPublicationLifecycleFixture struct {
	Orchestration *prDevelopmentOrchestrationFixture
	Store         *Store
	Clock         *time.Time
	Publication   PRDevelopmentPublication
	Claim         PRDevelopmentPublication
	Observation   PRDevelopmentPublicationProviderObservation
}

func TestExactPRDevelopmentPublicationJSONPreservesOpaqueProducerOrder(t *testing.T) {
	t.Parallel()

	raw := []byte(
		`{"version":1,"source_revision":"catalog-v1",` +
			`"decision_revision":"sha256:1111111111111111111111111111111111111111111111111111111111111111",` +
			`"resolution":{"effective":[],"entries":[]}}`,
	)
	exact, err := exactPRDevelopmentPublicationJSON(raw, len(raw))
	require.NoError(t, err)
	assert.Equal(t, raw, exact)
	exact[0] = '['
	assert.Equal(t, byte('{'), raw[0], "validated bytes must be detached")

	_, err = exactPRDevelopmentPublicationJSON(
		[]byte(`{"version": 1,"source_revision":"catalog-v1"}`),
		MaxPRDevelopmentPublicationPolicyBytes,
	)
	assert.ErrorIs(t, err, ErrInvalidPRDevelopmentPublication)

	for name, invalid := range map[string][]byte{
		"duplicate nested key": []byte(
			`{"version":1,"resolution":{"entries":[],"entries":[]}}`,
		),
		"noncanonical string escape": []byte(`{"value":"\u0061"}`),
		"invalid UTF-8": append(
			append([]byte(nil), []byte(`{"value":"`)...),
			0xff, '"', '}',
		),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, invalidErr := exactPRDevelopmentPublicationJSON(
				invalid,
				MaxPRDevelopmentPublicationPolicyBytes,
			)
			assert.ErrorIs(t, invalidErr, ErrInvalidPRDevelopmentPublication)
		})
	}
}

func newPRDevelopmentPublicationLifecycleFixture(
	t *testing.T,
) prDevelopmentPublicationLifecycleFixture {
	t.Helper()
	fixture, _ := newCompletedPRDevelopmentAIReviewFixture(t, PRDevelopmentCIPassed)
	return completePRDevelopmentPublicationLifecycleFixture(t, fixture)
}

func newChangedPRDevelopmentPublicationLifecycleFixture(
	t *testing.T,
) prDevelopmentPublicationLifecycleFixture {
	t.Helper()
	fixture := newPRDevelopmentOrchestrationFixture(t)
	run := startAndCompletePRDevelopmentOrchestrationForTest(t, fixture)
	validation := validPRDevelopmentOrchestrationValidationForTest(fixture)
	validation.CandidateTree = strings.Repeat("c", len(fixture.Lease.Controller.Tree))
	validation.CandidateDigest = strings.Repeat("d", 64)
	validation.ChangedFiles = 2
	validation.NoChanges = false
	validation.CIStatus = PRDevelopmentCIPassed
	_, changed, err := fixture.Operation.Store.RecordPRDevelopmentRepairOrchestrationValidation(
		context.Background(),
		validation,
	)
	require.NoError(t, err)
	require.True(t, changed)
	_, _, committed := commitOperationForTest(t, fixture.Operation, fixture.Lease, 5901)
	lease := operationLeaseFromTransition(committed)
	_, _, parked := parkOperationForTest(
		t,
		fixture.Operation,
		lease,
		[]PRDevelopmentControllerOperation{committed.Operation},
		run.Summary,
		run.Iterations,
		5902,
	)
	require.NotNil(t, parked.Fence)
	require.False(t, parked.Fence.NoChanges)
	return completePRDevelopmentPublicationLifecycleFixture(t, fixture)
}

func newPRDevelopmentPublicationLifecycleFixtureAt(
	t *testing.T,
	databasePath string,
) prDevelopmentPublicationLifecycleFixture {
	t.Helper()
	store, clock, capture := newPRDevelopmentStoreFixture(t, databasePath)
	fixture := newPRDevelopmentAIReviewOrchestrationOnStore(
		t,
		store,
		clock,
		capture,
		"publication-concurrency-attempt",
		"publication-concurrency-workspace",
		4901,
	)
	completePRDevelopmentAIReviewFixture(t, fixture, PRDevelopmentCIPassed, 4902)
	return completePRDevelopmentPublicationLifecycleFixture(t, fixture)
}

func completePRDevelopmentPublicationLifecycleFixture(
	t *testing.T,
	fixture *prDevelopmentOrchestrationFixture,
) prDevelopmentPublicationLifecycleFixture {
	t.Helper()
	lease := claimCompletedPRDevelopmentAIReviewFixture(t, fixture)
	completion, changed, err := fixture.Operation.Store.CompletePRDevelopmentReview(
		context.Background(),
		validPRDevelopmentAIReviewCompletionForTest(
			lease,
			PRDevelopmentLedgerReviewPassed,
		),
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.NotNil(t, completion.Publication)
	publication := *completion.Publication
	claimed, err := fixture.Operation.Store.ClaimPRDevelopmentPublications(
		context.Background(),
		PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "publication-worker",
			Limit:       1,
			Lease:       5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, publication.ID, claimed[0].ID)
	observation := PRDevelopmentPublicationProviderObservation{
		Repository:         fixture.Operation.Case.Repository,
		PullNumber:         fixture.Operation.Case.PullNumber,
		HeadRepository:     fixture.Operation.Case.HeadRepository,
		HeadRef:            publication.SourceRef,
		HeadSHA:            publication.SourceCommit,
		HeadCloneURL:       publication.SourceCloneURL,
		CurrentReviewState: fixture.Operation.Case.CurrentReviewState,
		ReviewDigest:       fixture.Run.ReviewDigest,
	}
	return prDevelopmentPublicationLifecycleFixture{
		Orchestration: fixture,
		Store:         fixture.Operation.Store, Clock: fixture.Operation.Clock,
		Publication: publication, Claim: claimed[0], Observation: observation,
	}
}

func pinPRDevelopmentPublicationForTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
) PRDevelopmentPublication {
	t.Helper()
	ctx := context.Background()
	policyRevision := "sha256:" + strings.Repeat("1", 64)
	subjectRevision := "sha256:" + strings.Repeat("2", 64)
	publication, changed, err := fixture.Store.PinPRDevelopmentPublicationPolicy(
		ctx,
		PRDevelopmentPublicationPolicyPin{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
			PolicyRevision: policyRevision,
			PinnedPolicy:   []byte(`{"gates":[{"type":"zero"}]}`),
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Empty(t, publication.ClaimToken)
	snapshot, err := fixture.Store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		fixture.Claim.ID,
		fixture.Claim.ClaimToken,
		fixture.Claim.ClaimEpoch,
	)
	require.NoError(t, err)
	_, changed, err = fixture.Store.PinPRDevelopmentPublicationSubject(
		ctx,
		PRDevelopmentPublicationSubjectPin{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
			PolicyRevision: policyRevision, SubjectRevision: subjectRevision,
			PinnedSubject:               []byte(`{"publication":"exact-passed-review"}`),
			ExpectedConversationVersion: snapshot.Conversation.Version,
			ExpectedTranscriptDigest:    snapshot.TranscriptDigest,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	*fixture.Clock = fixture.Clock.Add(time.Second)
	publication, changed, err = fixture.Store.PinPRDevelopmentPublicationProvider(
		ctx,
		PRDevelopmentPublicationProviderPin{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
			Observation: fixture.Observation, ObservedAt: *fixture.Clock,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	return publication
}

func startPRDevelopmentPublicationForTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
) (PRDevelopmentPublication, PRDevelopmentPublication) {
	t.Helper()
	pinned, pushClaim := claimPushReadyPRDevelopmentPublicationForTest(t, fixture)
	*fixture.Clock = fixture.Clock.Add(time.Second)
	request := publicationPushRequestFor(pushClaim, fixture.Observation.HeadSHA)
	started, newlyStarted, err := fixture.Store.StartPRDevelopmentPublicationPush(
		context.Background(),
		PRDevelopmentPublicationPushStart{
			PublicationID: pushClaim.ID,
			ClaimToken:    pushClaim.ClaimToken, ClaimEpoch: pushClaim.ClaimEpoch,
			Observation: fixture.Observation, ObservedAt: *fixture.Clock,
			Request: request,
		},
	)
	require.NoError(t, err)
	require.True(t, newlyStarted)
	require.Equal(t, PRDevelopmentPublicationPushStarted, started.Status)
	require.Empty(t, started.ClaimToken)
	require.Equal(t, pinned.ProviderObservationHash, started.ProviderObservationHash)
	return started, pushClaim
}

func claimPushReadyPRDevelopmentPublicationForTest(
	t *testing.T,
	fixture *prDevelopmentPublicationLifecycleFixture,
) (PRDevelopmentPublication, PRDevelopmentPublication) {
	t.Helper()
	pinned, err := getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		fixture.Claim.ID,
	)
	require.NoError(t, err)
	if pinned.PolicyRevision == "" {
		pinned = pinPRDevelopmentPublicationForTest(t, fixture)
	}
	ready, changed, err := fixture.Store.MarkPRDevelopmentPublicationPushReady(
		context.Background(),
		PRDevelopmentPublicationMarkPushReady{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken,
			ClaimEpoch:    fixture.Claim.ClaimEpoch,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, PRDevelopmentPublicationPushReady, ready.Status)
	claimed, err := fixture.Store.ClaimPRDevelopmentPublications(
		context.Background(),
		PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "publication-push-worker", Limit: 1, Lease: 5 * time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	return pinned, claimed[0]
}

func TestPRDevelopmentPublicationZeroGatePushLifecycleAndReaderRedaction(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	read, err := fixture.Store.GetPRDevelopmentPublication(
		context.Background(),
		fixture.Claim.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentPublicationClaimed, read.Status)
	assert.Empty(t, read.ClaimToken)
	assert.NotEmpty(t, fixture.Claim.ClaimToken)

	started, pushClaim := startPRDevelopmentPublicationForTest(t, &fixture)
	disposition := PRDevelopmentPublicationPushApplied
	if started.ExpectedRemoteTip == started.TipCommit {
		disposition = PRDevelopmentPublicationPushAlreadyCurrent
	}
	result := publicationPushResultFor(started, disposition, true)
	published, finalized, err := fixture.Store.FinalizePRDevelopmentPublicationPush(
		context.Background(),
		PRDevelopmentPublicationPushFinalize{
			PublicationID: started.ID,
			ClaimToken:    pushClaim.ClaimToken, ClaimEpoch: pushClaim.ClaimEpoch,
			RequestHash: started.PushRequestHash,
			Status:      PRDevelopmentPublicationPublished, Result: result,
		},
	)
	require.NoError(t, err)
	require.True(t, finalized)
	assert.Equal(t, PRDevelopmentPublicationPublished, published.Status)
	assert.Equal(t, disposition, published.PushDisposition)
	assert.True(t, published.WorkspaceClean)
	assert.False(t, published.LocalDrift)

	replayed, finalized, err := fixture.Store.FinalizePRDevelopmentPublicationPush(
		context.Background(),
		PRDevelopmentPublicationPushFinalize{
			PublicationID: started.ID,
			ClaimToken:    pushClaim.ClaimToken, ClaimEpoch: pushClaim.ClaimEpoch,
			RequestHash: started.PushRequestHash,
			Status:      PRDevelopmentPublicationPublished, Result: result,
		},
	)
	require.NoError(t, err)
	assert.False(t, finalized)
	assert.Equal(t, published, replayed)
}

func TestPRDevelopmentPublicationExpiredPushRequiresFreshHeadReconciliation(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	started, pushClaim := startPRDevelopmentPublicationForTest(t, &fixture)
	require.NotNil(t, pushClaim.ClaimUntil)
	*fixture.Clock = pushClaim.ClaimUntil.Add(time.Second)

	claimed, err := fixture.Store.ClaimPRDevelopmentPublications(
		context.Background(),
		PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "pre-effect-only-worker", Limit: 1, Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	assert.Empty(t, claimed)
	stillStarted, err := fixture.Store.GetPRDevelopmentPublication(
		context.Background(),
		started.ID,
	)
	require.NoError(t, err)
	assert.Equal(t, PRDevelopmentPublicationPushStarted, stillStarted.Status)

	expired, err := fixture.Store.ExpirePRDevelopmentPublicationPushes(
		context.Background(),
		1,
	)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	unknown := expired[0]
	require.Equal(t, PRDevelopmentPublicationOutcomeUnknown, unknown.Status)
	require.NotNil(t, unknown.CompletedAt)
	assert.Equal(t, unknown.ExpectedRemoteTip, unknown.TipCommit,
		"no-change/already-current intents must remain reconcilable")

	observation := PRDevelopmentPublicationRemoteObservation{
		Repository:     unknown.ProviderObservation.Repository,
		PullNumber:     unknown.ProviderObservation.PullNumber,
		HeadRepository: unknown.ProviderObservation.HeadRepository,
		HeadRef:        unknown.ProviderObservation.HeadRef,
		HeadSHA:        unknown.PushRequest.ExpectedTip,
	}
	result := publicationPushResultFor(
		unknown,
		PRDevelopmentPublicationPushReconciled,
		false,
	)
	assertStillUnknown := func(t *testing.T) {
		t.Helper()
		stored, loadErr := fixture.Store.GetPRDevelopmentPublication(
			context.Background(),
			unknown.ID,
		)
		require.NoError(t, loadErr)
		assert.Equal(t, PRDevelopmentPublicationOutcomeUnknown, stored.Status)
		assert.Equal(t, unknown.PushRequestHash, stored.PushRequestHash)
		assert.Empty(t, stored.ReconciliationObservationHash)
		assert.Empty(t, stored.PushResultHash)
	}
	_, _, err = fixture.Store.ReconcilePRDevelopmentPublicationOutcome(
		context.Background(),
		PRDevelopmentPublicationOutcomeReconciliation{
			PublicationID: unknown.ID, RequestHash: unknown.PushRequestHash,
			Observation: observation, ObservedAt: *unknown.CompletedAt, Result: result,
		},
	)
	assert.Error(t, err, "an observation at outcome-unknown completion is not fresh")
	assertStillUnknown(t)

	*fixture.Clock = fixture.Clock.Add(time.Second)
	freshAt := *fixture.Clock
	differentHex := func(value string) string {
		different := strings.Repeat("f", len(value))
		if different == value {
			different = strings.Repeat("e", len(value))
		}
		return different
	}
	negativeCases := []struct {
		name   string
		mutate func(*PRDevelopmentPublicationOutcomeReconciliation)
	}{
		{
			name: "wrong provider identity",
			mutate: func(input *PRDevelopmentPublicationOutcomeReconciliation) {
				input.Observation.PullNumber++
			},
		},
		{
			name: "wrong request hash",
			mutate: func(input *PRDevelopmentPublicationOutcomeReconciliation) {
				input.RequestHash = differentHex(input.RequestHash)
			},
		},
		{
			name: "wrong remote tip",
			mutate: func(input *PRDevelopmentPublicationOutcomeReconciliation) {
				input.Observation.HeadSHA = differentHex(input.Observation.HeadSHA)
			},
		},
	}
	for _, testCase := range negativeCases {
		input := PRDevelopmentPublicationOutcomeReconciliation{
			PublicationID: unknown.ID, RequestHash: unknown.PushRequestHash,
			Observation: observation, ObservedAt: freshAt, Result: result,
		}
		testCase.mutate(&input)
		_, reconciled, reconcileErr := fixture.Store.ReconcilePRDevelopmentPublicationOutcome(
			context.Background(),
			input,
		)
		assert.Error(t, reconcileErr, testCase.name)
		assert.False(t, reconciled, testCase.name)
		assertStillUnknown(t)
	}

	*fixture.Clock = freshAt.Add(maxPRDevelopmentPublicationObservationAge + time.Nanosecond)
	_, reconciled, err := fixture.Store.ReconcilePRDevelopmentPublicationOutcome(
		context.Background(),
		PRDevelopmentPublicationOutcomeReconciliation{
			PublicationID: unknown.ID, RequestHash: unknown.PushRequestHash,
			Observation: observation, ObservedAt: freshAt, Result: result,
		},
	)
	assert.Error(t, err, "an observation older than the freshness window must be rejected")
	assert.False(t, reconciled)
	assertStillUnknown(t)

	published, reconciled, err := fixture.Store.ReconcilePRDevelopmentPublicationOutcome(
		context.Background(),
		PRDevelopmentPublicationOutcomeReconciliation{
			PublicationID: unknown.ID, RequestHash: unknown.PushRequestHash,
			Observation: observation, ObservedAt: *fixture.Clock, Result: result,
		},
	)
	require.NoError(t, err)
	require.True(t, reconciled)
	assert.Equal(t, PRDevelopmentPublicationPublished, published.Status)
	assert.Equal(t, PRDevelopmentPublicationPushReconciled, published.PushDisposition)
	assert.Equal(t, unknown.CompletedAt, published.CompletedAt,
		"reconciliation must preserve when the outcome first became unknown")
	require.NotNil(t, published.ReconciliationObservedAt)
	assert.True(t, published.ReconciliationObservedAt.After(*published.CompletedAt))
	assert.False(t, published.WorkspaceClean)
	assert.False(t, published.LocalDrift)
	assert.Equal(t, unknown.ProviderObservationHash, published.ProviderObservationHash)
	assert.NotEmpty(t, published.ReconciliationObservationHash)

	_, err = fixture.Store.db.Exec(`PRAGMA ignore_check_constraints = ON`)
	require.NoError(t, err)
	_, err = fixture.Store.db.Exec(`
		UPDATE pr_development_publications
		SET reconciliation_observed_at = completed_at
		WHERE id = ?`, published.ID)
	require.NoError(t, err)
	_, err = fixture.Store.GetPRDevelopmentPublication(context.Background(), published.ID)
	assert.Error(t, err, "stored reconciliation must remain strictly after unknown completion")
}

func TestPRDevelopmentPublicationGateWaitCanReleaseAgainAfterDueClaim(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	pinned := pinPRDevelopmentPublicationForTest(t, &fixture)
	key := publicationPRDevelopmentDecisionKey(pinned)
	runID := "wr_" + strings.Repeat("3", 32)
	created := 0
	_, existed, err := fixture.Store.AdmitPRDevelopmentPublicationDecisionRun(
		context.Background(),
		PRDevelopmentPublicationDecisionRunAdmission{
			Key: key, RunID: runID,
			ClaimToken: fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
		},
		func(context.Context) error {
			created++
			return nil
		},
	)
	require.NoError(t, err)
	assert.False(t, existed)
	assert.Equal(t, 1, created)
	firstDue := fixture.Clock.Add(time.Second)
	waiting, changed, err := fixture.Store.ReleasePRDevelopmentPublicationGateWait(
		context.Background(),
		PRDevelopmentPublicationGateWait{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
			DecisionRunID: runID, AvailableAt: firstDue,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentPublicationGateWaiting, waiting.Status)

	*fixture.Clock = firstDue
	claimed, err := fixture.Store.ClaimPRDevelopmentPublications(
		context.Background(),
		PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "gate-poll-worker", Limit: 1, Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, PRDevelopmentPublicationGateWaiting, claimed[0].ClaimFrom)
	secondDue := fixture.Clock.Add(time.Minute)
	waiting, changed, err = fixture.Store.ReleasePRDevelopmentPublicationGateWait(
		context.Background(),
		PRDevelopmentPublicationGateWait{
			PublicationID: claimed[0].ID,
			ClaimToken:    claimed[0].ClaimToken, ClaimEpoch: claimed[0].ClaimEpoch,
			DecisionRunID: runID, AvailableAt: secondDue,
		},
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentPublicationGateWaiting, waiting.Status)
	assert.Equal(t, secondDue, waiting.AvailableAt)
}

func TestPRDevelopmentPublicationReaderRejectsTamperedTerminalCode(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	_, err := fixture.Store.db.Exec(`PRAGMA ignore_check_constraints = ON`)
	require.NoError(t, err)
	_, err = fixture.Store.db.Exec(`
		UPDATE pr_development_publications
		SET status = 'superseded', claim_from = '', claim_owner = '', claim_token = '',
			claim_until = NULL, completed_at = created_at,
			last_error_code = 'outcome_unknown'
		WHERE id = ?`, fixture.Claim.ID)
	require.NoError(t, err)
	_, err = fixture.Store.GetPRDevelopmentPublication(
		context.Background(),
		fixture.Claim.ID,
	)
	assert.Error(t, err)
}

func TestPRDevelopmentPublicationReaderRejectsUnknownStoredStatus(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	_, err := fixture.Store.db.Exec(`PRAGMA ignore_check_constraints = ON`)
	require.NoError(t, err)
	_, err = fixture.Store.db.Exec(`
		UPDATE pr_development_publications
		SET status = 'evil', claim_from = '', claim_owner = '', claim_token = '',
			claim_until = NULL, completed_at = created_at
		WHERE id = ?`, fixture.Claim.ID)
	require.NoError(t, err)
	_, err = fixture.Store.GetPRDevelopmentPublication(
		context.Background(),
		fixture.Claim.ID,
	)
	assert.Error(t, err)
}

func TestPRDevelopmentPublicationReaderRejectsFutureLifecycleTimestamp(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	_, err := fixture.Store.db.Exec(`PRAGMA ignore_check_constraints = ON`)
	require.NoError(t, err)
	_, err = fixture.Store.db.Exec(`
		UPDATE pr_development_publications
		SET claimed_at = claim_until
		WHERE id = ?`, fixture.Claim.ID)
	require.NoError(t, err)
	_, err = fixture.Store.GetPRDevelopmentPublication(
		context.Background(),
		fixture.Claim.ID,
	)
	assert.Error(t, err)
}

func TestPRDevelopmentPublicationObservationFreshnessBoundaries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	causal := now.Add(-maxPRDevelopmentPublicationObservationAge - time.Minute)
	assert.NoError(t, requireFreshPRDevelopmentPublicationObservation(
		now.Add(-maxPRDevelopmentPublicationObservationAge),
		now,
		causal,
	))
	assert.Error(t, requireFreshPRDevelopmentPublicationObservation(
		now.Add(-maxPRDevelopmentPublicationObservationAge-time.Nanosecond),
		now,
		causal,
	))
	assert.Error(t, requireFreshPRDevelopmentPublicationObservation(now.Add(time.Nanosecond), now, causal))
	assert.Error(t, requireFreshPRDevelopmentPublicationObservation(causal, now, causal))
	_, err := normalizePRDevelopmentPublicationTime(
		"far-future observation",
		time.Date(2500, time.January, 1, 0, 0, 0, 0, time.UTC),
	)
	assert.Error(t, err)
	_, err = normalizePRDevelopmentPublicationTime(
		"far-past observation",
		time.Date(1500, time.January, 1, 0, 0, 0, 0, time.UTC),
	)
	assert.Error(t, err)
}

func TestPRDevelopmentPublicationRenewOnlyExtendsAndRejectsClockRegression(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	require.NotNil(t, fixture.Claim.ClaimUntil)
	originalDeadline := *fixture.Claim.ClaimUntil
	*fixture.Clock = fixture.Clock.Add(time.Minute)
	err := fixture.Store.RenewPRDevelopmentPublication(
		context.Background(),
		PRDevelopmentPublicationRenew{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
			Lease: 10 * time.Minute,
		},
	)
	require.NoError(t, err)
	renewed, err := getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		fixture.Claim.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, renewed.ClaimUntil)
	assert.True(t, renewed.ClaimUntil.After(originalDeadline))

	err = fixture.Store.RenewPRDevelopmentPublication(
		context.Background(),
		PRDevelopmentPublicationRenew{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
			Lease: time.Minute,
		},
	)
	assert.Error(t, err)
	unchanged, loadErr := getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		fixture.Claim.ID,
	)
	require.NoError(t, loadErr)
	assert.Equal(t, renewed.ClaimUntil, unchanged.ClaimUntil)
	assert.Equal(t, renewed.UpdatedAt, unchanged.UpdatedAt)

	*fixture.Clock = fixture.Clock.Add(-30 * time.Second)
	err = fixture.Store.RenewPRDevelopmentPublication(
		context.Background(),
		PRDevelopmentPublicationRenew{
			PublicationID: fixture.Claim.ID,
			ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
			Lease: 20 * time.Minute,
		},
	)
	assert.Error(t, err)
	unchanged, loadErr = getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		fixture.Claim.ID,
	)
	require.NoError(t, loadErr)
	assert.Equal(t, renewed.ClaimUntil, unchanged.ClaimUntil)
	assert.Equal(t, renewed.UpdatedAt, unchanged.UpdatedAt)
}

func TestPRDevelopmentPublicationStartRejectsObservationOlderThanFiveMinutes(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	_, pushClaim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
	require.NotNil(t, pushClaim.ClaimedAt)
	staleObservedAt := pushClaim.ClaimedAt.Add(time.Second)
	*fixture.Clock = staleObservedAt.Add(
		maxPRDevelopmentPublicationObservationAge + time.Nanosecond,
	)
	request := publicationPushRequestFor(pushClaim, fixture.Observation.HeadSHA)
	_, started, err := fixture.Store.StartPRDevelopmentPublicationPush(
		context.Background(),
		PRDevelopmentPublicationPushStart{
			PublicationID: pushClaim.ID,
			ClaimToken:    pushClaim.ClaimToken, ClaimEpoch: pushClaim.ClaimEpoch,
			Observation: fixture.Observation, ObservedAt: staleObservedAt,
			Request: request,
		},
	)
	assert.Error(t, err)
	assert.False(t, started)
	stored, loadErr := getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		pushClaim.ID,
	)
	require.NoError(t, loadErr)
	assert.Equal(t, PRDevelopmentPublicationClaimed, stored.Status)
	assert.Empty(t, stored.PushRequestHash)
}

func TestPRDevelopmentPublicationDecisionAdmissionReplayRollbackPanicAndUncertainty(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	pinned := pinPRDevelopmentPublicationForTest(t, &fixture)
	key := publicationPRDevelopmentDecisionKey(pinned)
	failedRunID := "wr_" + strings.Repeat("4", 32)
	_, _, err := fixture.Store.AdmitPRDevelopmentPublicationDecisionRun(
		context.Background(),
		PRDevelopmentPublicationDecisionRunAdmission{
			Key: key, RunID: failedRunID,
			ClaimToken: fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
		},
		func(context.Context) error { return errors.New("injected workflow failure") },
	)
	assert.Error(t, err)
	stored, loadErr := getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		fixture.Claim.ID,
	)
	require.NoError(t, loadErr)
	assert.Empty(t, stored.DecisionRunID)

	panicRunID := "wr_" + strings.Repeat("5", 32)
	func() {
		defer func() { assert.Equal(t, "injected workflow panic", recover()) }()
		_, _, _ = fixture.Store.AdmitPRDevelopmentPublicationDecisionRun(
			context.Background(),
			PRDevelopmentPublicationDecisionRunAdmission{
				Key: key, RunID: panicRunID,
				ClaimToken: fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
			},
			func(context.Context) error { panic("injected workflow panic") },
		)
	}()
	stored, loadErr = getPRDevelopmentPublicationByID(
		context.Background(),
		fixture.Store.db,
		fixture.Claim.ID,
	)
	require.NoError(t, loadErr, "panic rollback must release the SQLite writer")
	assert.Empty(t, stored.DecisionRunID)

	runID := "wr_" + strings.Repeat("6", 32)
	created := 0
	link, existed, err := fixture.Store.AdmitPRDevelopmentPublicationDecisionRun(
		context.Background(),
		PRDevelopmentPublicationDecisionRunAdmission{
			Key: key, RunID: runID,
			ClaimToken: fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
		},
		func(context.Context) error { created++; return nil },
	)
	require.NoError(t, err)
	assert.False(t, existed)
	assert.Equal(t, runID, link.RunID)
	assert.Equal(t, 1, created)
	link, existed, err = fixture.Store.AdmitPRDevelopmentPublicationDecisionRun(
		context.Background(),
		PRDevelopmentPublicationDecisionRunAdmission{
			Key: key, RunID: runID,
			ClaimToken: "stale-but-ignored-on-historical-replay",
			ClaimEpoch: fixture.Claim.ClaimEpoch,
		},
		func(context.Context) error { created++; return nil },
	)
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, runID, link.RunID)
	assert.Equal(t, 1, created)
	_, _, err = fixture.Store.AdmitPRDevelopmentPublicationDecisionRun(
		context.Background(),
		PRDevelopmentPublicationDecisionRunAdmission{
			Key: key, RunID: "wr_" + strings.Repeat("7", 32),
			ClaimToken: fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
		},
		func(context.Context) error { created++; return nil },
	)
	assert.Error(t, err)
	assert.Equal(t, 1, created)

	uncertain := newPRDevelopmentPublicationLifecycleFixture(t)
	uncertainPinned := pinPRDevelopmentPublicationForTest(t, &uncertain)
	uncertainContext, cancel := context.WithCancel(context.Background())
	_, _, err = uncertain.Store.AdmitPRDevelopmentPublicationDecisionRun(
		uncertainContext,
		PRDevelopmentPublicationDecisionRunAdmission{
			Key:        publicationPRDevelopmentDecisionKey(uncertainPinned),
			RunID:      "wr_" + strings.Repeat("8", 32),
			ClaimToken: uncertain.Claim.ClaimToken, ClaimEpoch: uncertain.Claim.ClaimEpoch,
		},
		func(context.Context) error { cancel(); return nil },
	)
	assert.ErrorIs(t, err, ErrPRDevelopmentPublicationAdmissionUncertain)
	stored, loadErr = getPRDevelopmentPublicationByID(
		context.Background(),
		uncertain.Store.db,
		uncertain.Claim.ID,
	)
	require.NoError(t, loadErr)
	assert.Empty(t, stored.DecisionRunID)
}

func TestPRDevelopmentPublicationExpiredPreEffectClaimIsReclaimedWithNewAuthority(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	first := fixture.Claim
	require.NotNil(t, first.ClaimUntil)
	*fixture.Clock = first.ClaimUntil.Add(time.Second)
	claimed, err := fixture.Store.ClaimPRDevelopmentPublications(
		context.Background(),
		PRDevelopmentPublicationClaimRequest{
			WorkerLabel: "publication-reclaimer", Limit: 1, Lease: time.Minute,
		},
	)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	reclaimed := claimed[0]
	assert.Equal(t, first.ID, reclaimed.ID)
	assert.Equal(t, first.ClaimEpoch+1, reclaimed.ClaimEpoch)
	assert.Equal(t, first.Claims+1, reclaimed.Claims)
	assert.NotEqual(t, first.ClaimToken, reclaimed.ClaimToken)
	require.NotNil(t, reclaimed.ClaimedAt)
	assert.Equal(t, *fixture.Clock, *reclaimed.ClaimedAt)
	err = fixture.Store.RenewPRDevelopmentPublication(
		context.Background(),
		PRDevelopmentPublicationRenew{
			PublicationID: first.ID, ClaimToken: first.ClaimToken,
			ClaimEpoch: first.ClaimEpoch, Lease: 2 * time.Minute,
		},
	)
	assert.ErrorIs(t, err, ErrStaleLease)
}

func TestPRDevelopmentPublicationPinsReplayExactlyAcrossPushRefresh(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	pinned := pinPRDevelopmentPublicationForTest(t, &fixture)
	pinnedAt := *pinned.ProviderPinnedAt
	policyInput := PRDevelopmentPublicationPolicyPin{
		PublicationID: fixture.Claim.ID,
		ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
		PolicyRevision: pinned.PolicyRevision, PinnedPolicy: pinned.PinnedPolicy,
	}
	_, changed, err := fixture.Store.PinPRDevelopmentPublicationPolicy(
		context.Background(),
		policyInput,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	policyInput.PinnedPolicy = []byte(`{"changed":true}`)
	_, _, err = fixture.Store.PinPRDevelopmentPublicationPolicy(context.Background(), policyInput)
	assert.Error(t, err)

	subjectInput := PRDevelopmentPublicationSubjectPin{
		PublicationID: fixture.Claim.ID,
		ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
		PolicyRevision: pinned.PolicyRevision, SubjectRevision: pinned.SubjectRevision,
		PinnedSubject:               pinned.PinnedSubject,
		ExpectedConversationVersion: 0,
		ExpectedTranscriptDigest:    emptyPRDevelopmentTranscriptDigest(),
	}
	_, changed, err = fixture.Store.PinPRDevelopmentPublicationSubject(
		context.Background(),
		subjectInput,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	subjectInput.PinnedSubject = []byte(`{"changed":true}`)
	_, _, err = fixture.Store.PinPRDevelopmentPublicationSubject(context.Background(), subjectInput)
	assert.Error(t, err)

	started, _ := startPRDevelopmentPublicationForTest(t, &fixture)
	require.NotNil(t, started.ProviderPinnedAt)
	require.NotNil(t, started.ProviderObservedAt)
	assert.Equal(t, pinnedAt, *started.ProviderPinnedAt)
	assert.True(t, started.ProviderObservedAt.After(*started.ProviderPinnedAt))
	providerInput := PRDevelopmentPublicationProviderPin{
		PublicationID: fixture.Claim.ID,
		ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
		Observation: fixture.Observation, ObservedAt: pinnedAt,
	}
	_, changed, err = fixture.Store.PinPRDevelopmentPublicationProvider(
		context.Background(),
		providerInput,
	)
	require.NoError(t, err)
	assert.False(t, changed, "Pin(t1) must replay after Start refreshes observed_at to t2")
	providerInput.Observation.HeadSHA = strings.Repeat("f", len(providerInput.Observation.HeadSHA))
	_, _, err = fixture.Store.PinPRDevelopmentPublicationProvider(context.Background(), providerInput)
	assert.Error(t, err)
}

func TestPRDevelopmentPublicationStartRejectsLocalOrProviderHighWaterDrift(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name  string
		drift func(*testing.T, *prDevelopmentPublicationLifecycleFixture,
			*PRDevelopmentPublicationProviderObservation)
	}{
		{
			name: "local controller revision",
			drift: func(
				t *testing.T,
				fixture *prDevelopmentPublicationLifecycleFixture,
				_ *PRDevelopmentPublicationProviderObservation,
			) {
				result, err := fixture.Store.db.Exec(`
					UPDATE pr_development_thread_controllers
					SET revision = revision + 1
					WHERE id = ?`, fixture.Publication.ControllerID)
				require.NoError(t, err)
				rows, err := result.RowsAffected()
				require.NoError(t, err)
				require.Equal(t, int64(1), rows)
			},
		},
		{
			name: "provider pull identity",
			drift: func(
				_ *testing.T,
				_ *prDevelopmentPublicationLifecycleFixture,
				observation *PRDevelopmentPublicationProviderObservation,
			) {
				observation.PullNumber++
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixture := newPRDevelopmentPublicationLifecycleFixture(t)
			_, pushClaim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
			observation := fixture.Observation
			testCase.drift(t, &fixture, &observation)
			*fixture.Clock = fixture.Clock.Add(time.Second)
			_, newlyStarted, err := fixture.Store.StartPRDevelopmentPublicationPush(
				context.Background(),
				PRDevelopmentPublicationPushStart{
					PublicationID: pushClaim.ID,
					ClaimToken:    pushClaim.ClaimToken, ClaimEpoch: pushClaim.ClaimEpoch,
					Observation: observation, ObservedAt: *fixture.Clock,
					Request: publicationPushRequestFor(pushClaim, fixture.Observation.HeadSHA),
				},
			)
			assert.Error(t, err)
			assert.False(t, newlyStarted)
			stored, err := fixture.Store.GetPRDevelopmentPublication(
				context.Background(),
				pushClaim.ID,
			)
			require.NoError(t, err)
			assert.Equal(t, PRDevelopmentPublicationClaimed, stored.Status)
			assert.Equal(t, PRDevelopmentPublicationPushReady, stored.ClaimFrom)
			assert.Zero(t, stored.Attempts)
			assert.Empty(t, stored.PushRequestHash)
			assert.Nil(t, stored.EffectStartedAt)
		})
	}
}

func TestPRDevelopmentPublicationPrestartCompletionSanitizesAndReplays(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	input := PRDevelopmentPublicationPrestartCompletion{
		PublicationID: fixture.Claim.ID,
		ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
		Status:        PRDevelopmentPublicationFailed,
		ErrorCode:     PRDevelopmentPublicationErrorInternal,
		InternalError: "unsafe\x00" + strings.Repeat("x", MaxPRDevelopmentPublicationErrorBytes+10),
	}
	failed, changed, err := fixture.Store.CompletePRDevelopmentPublicationPrestart(
		context.Background(),
		input,
	)
	require.NoError(t, err)
	require.True(t, changed)
	assert.Equal(t, PRDevelopmentPublicationFailed, failed.Status)
	assert.NotContains(t, failed.LastErrorDetail, "\x00")
	assert.LessOrEqual(t, len(failed.LastErrorDetail), MaxPRDevelopmentPublicationErrorBytes)
	replayed, changed, err := fixture.Store.CompletePRDevelopmentPublicationPrestart(
		context.Background(),
		input,
	)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, failed, replayed)
	input.ErrorCode = PRDevelopmentPublicationErrorPushFailed
	_, _, err = fixture.Store.CompletePRDevelopmentPublicationPrestart(
		context.Background(),
		input,
	)
	assert.Error(t, err)
}

func TestPRDevelopmentPublicationPrestartTerminalTransitionMatrix(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		status PRDevelopmentPublicationStatus
		code   PRDevelopmentPublicationErrorCode
	}{
		{PRDevelopmentPublicationConflict, PRDevelopmentPublicationErrorProviderChanged},
		{PRDevelopmentPublicationSuperseded, PRDevelopmentPublicationErrorSuperseded},
		{PRDevelopmentPublicationFailed, PRDevelopmentPublicationErrorInternal},
		{PRDevelopmentPublicationRecoveryRequired, PRDevelopmentPublicationErrorRecoveryRequired},
	}
	for _, testCase := range testCases {
		t.Run(string(testCase.status), func(t *testing.T) {
			t.Parallel()

			fixture := newPRDevelopmentPublicationLifecycleFixture(t)
			input := PRDevelopmentPublicationPrestartCompletion{
				PublicationID: fixture.Claim.ID,
				ClaimToken:    fixture.Claim.ClaimToken, ClaimEpoch: fixture.Claim.ClaimEpoch,
				Status: testCase.status, ErrorCode: testCase.code,
				InternalError: "prestart transition matrix",
			}
			completed, changed, err := fixture.Store.CompletePRDevelopmentPublicationPrestart(
				context.Background(),
				input,
			)
			require.NoError(t, err)
			require.True(t, changed)
			assert.Equal(t, testCase.status, completed.Status)
			assert.Equal(t, testCase.code, completed.LastErrorCode)
			assert.NotNil(t, completed.CompletedAt)
			assert.Nil(t, completed.EffectStartedAt)
			assert.Empty(t, completed.PushRequestHash)
			replayed, changed, err := fixture.Store.CompletePRDevelopmentPublicationPrestart(
				context.Background(),
				input,
			)
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Equal(t, completed, replayed)
		})
	}
}

func TestPRDevelopmentPublicationPoststartTerminalTransitionMatrix(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		status PRDevelopmentPublicationStatus
		code   PRDevelopmentPublicationErrorCode
	}{
		{PRDevelopmentPublicationConflict, PRDevelopmentPublicationErrorPushConflict},
		{PRDevelopmentPublicationFailed, PRDevelopmentPublicationErrorPushFailed},
		{PRDevelopmentPublicationRecoveryRequired, PRDevelopmentPublicationErrorRecoveryRequired},
		{PRDevelopmentPublicationOutcomeUnknown, PRDevelopmentPublicationErrorOutcomeUnknown},
	}
	for _, testCase := range testCases {
		t.Run(string(testCase.status), func(t *testing.T) {
			t.Parallel()

			fixture := newPRDevelopmentPublicationLifecycleFixture(t)
			started, pushClaim := startPRDevelopmentPublicationForTest(t, &fixture)
			input := PRDevelopmentPublicationPushFinalize{
				PublicationID: started.ID,
				ClaimToken:    pushClaim.ClaimToken, ClaimEpoch: pushClaim.ClaimEpoch,
				RequestHash: started.PushRequestHash,
				Status:      testCase.status, ErrorCode: testCase.code,
				InternalError: "poststart transition matrix",
			}
			completed, finalized, err := fixture.Store.FinalizePRDevelopmentPublicationPush(
				context.Background(),
				input,
			)
			require.NoError(t, err)
			require.True(t, finalized)
			assert.Equal(t, testCase.status, completed.Status)
			assert.Equal(t, testCase.code, completed.LastErrorCode)
			assert.NotNil(t, completed.CompletedAt)
			assert.NotNil(t, completed.EffectStartedAt)
			assert.NotEmpty(t, completed.PushRequestHash)
			assert.Empty(t, completed.PushResultHash)
			replayed, finalized, err := fixture.Store.FinalizePRDevelopmentPublicationPush(
				context.Background(),
				input,
			)
			require.NoError(t, err)
			assert.False(t, finalized)
			assert.Equal(t, completed, replayed)
		})
	}
}

func TestPRDevelopmentPublicationDirectReconciledFinalizeReplays(t *testing.T) {
	t.Parallel()

	fixture := newChangedPRDevelopmentPublicationLifecycleFixture(t)
	started, pushClaim := startPRDevelopmentPublicationForTest(t, &fixture)
	require.NotEqual(t, started.ExpectedRemoteTip, started.TipCommit)
	result := publicationPushResultFor(
		started,
		PRDevelopmentPublicationPushReconciled,
		true,
	)
	input := PRDevelopmentPublicationPushFinalize{
		PublicationID: started.ID,
		ClaimToken:    pushClaim.ClaimToken, ClaimEpoch: pushClaim.ClaimEpoch,
		RequestHash: started.PushRequestHash,
		Status:      PRDevelopmentPublicationPublished, Result: result,
	}
	published, finalized, err := fixture.Store.FinalizePRDevelopmentPublicationPush(
		context.Background(),
		input,
	)
	require.NoError(t, err)
	require.True(t, finalized)
	assert.Equal(t, PRDevelopmentPublicationPushReconciled, published.PushDisposition)
	assert.True(t, published.WorkspaceClean)
	assert.False(t, published.LocalDrift)
	assert.Empty(t, published.ReconciliationObservationHash,
		"a direct proven result is not head-only outcome reconciliation")
	replayed, finalized, err := fixture.Store.FinalizePRDevelopmentPublicationPush(
		context.Background(),
		input,
	)
	require.NoError(t, err)
	assert.False(t, finalized)
	assert.Equal(t, published, replayed)
}

func TestPRDevelopmentPublicationStartReplayAndFinalizeLocalDriftOrFailure(t *testing.T) {
	t.Parallel()

	fixture := newPRDevelopmentPublicationLifecycleFixture(t)
	started, pushClaim := startPRDevelopmentPublicationForTest(t, &fixture)
	replayed, newlyStarted, err := fixture.Store.StartPRDevelopmentPublicationPush(
		context.Background(),
		PRDevelopmentPublicationPushStart{
			PublicationID: started.ID,
			ClaimToken:    pushClaim.ClaimToken, ClaimEpoch: pushClaim.ClaimEpoch,
			Observation: fixture.Observation, ObservedAt: *started.ProviderObservedAt,
			Request: started.PushRequest,
		},
	)
	require.NoError(t, err)
	assert.False(t, newlyStarted)
	assert.Equal(t, started, replayed)
	changedRequest := started.PushRequest
	changedRequest.ExpectedTree = strings.Repeat("f", len(changedRequest.ExpectedTree))
	_, _, err = fixture.Store.StartPRDevelopmentPublicationPush(
		context.Background(),
		PRDevelopmentPublicationPushStart{
			PublicationID: started.ID,
			ClaimToken:    pushClaim.ClaimToken, ClaimEpoch: pushClaim.ClaimEpoch,
			Observation: fixture.Observation, ObservedAt: *started.ProviderObservedAt,
			Request: changedRequest,
		},
	)
	assert.Error(t, err)
	disposition := PRDevelopmentPublicationPushAlreadyCurrent
	if started.ExpectedRemoteTip != started.TipCommit {
		disposition = PRDevelopmentPublicationPushApplied
	}
	result := publicationPushResultFor(started, disposition, false)
	published, finalized, err := fixture.Store.FinalizePRDevelopmentPublicationPush(
		context.Background(),
		PRDevelopmentPublicationPushFinalize{
			PublicationID: started.ID,
			ClaimToken:    pushClaim.ClaimToken, ClaimEpoch: pushClaim.ClaimEpoch,
			RequestHash: started.PushRequestHash, Status: PRDevelopmentPublicationPublished,
			Result: result, LocalDrift: true,
		},
	)
	require.NoError(t, err)
	require.True(t, finalized)
	assert.False(t, published.WorkspaceClean)
	assert.True(t, published.LocalDrift)

	failedFixture := newPRDevelopmentPublicationLifecycleFixture(t)
	failedStart, failedClaim := startPRDevelopmentPublicationForTest(t, &failedFixture)
	failed, finalized, err := failedFixture.Store.FinalizePRDevelopmentPublicationPush(
		context.Background(),
		PRDevelopmentPublicationPushFinalize{
			PublicationID: failedStart.ID,
			ClaimToken:    failedClaim.ClaimToken, ClaimEpoch: failedClaim.ClaimEpoch,
			RequestHash:   failedStart.PushRequestHash,
			Status:        PRDevelopmentPublicationFailed,
			ErrorCode:     PRDevelopmentPublicationErrorPushFailed,
			InternalError: "bounded push failure",
		},
	)
	require.NoError(t, err)
	require.True(t, finalized)
	assert.Equal(t, PRDevelopmentPublicationFailed, failed.Status)
	assert.NotNil(t, failed.EffectStartedAt)
	assert.Empty(t, failed.PushResultHash)
}

func TestPRDevelopmentPublicationStartSerializesWithNextMutationAcrossStores(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "publication-race", "eventing.db")
	fixture := newPRDevelopmentPublicationLifecycleFixtureAt(t, path)
	_, pushClaim := claimPushReadyPRDevelopmentPublicationForTest(t, &fixture)
	conversation, err := fixture.Store.AppendPRDevelopmentMessage(
		context.Background(),
		PRDevelopmentMessageAppend{
			CaseID:          fixture.Orchestration.Operation.Case.ID,
			ExpectedVersion: 0, Role: PRDevelopmentMessageUser,
			Content: "Start another local repair only if publication has not begun.",
		},
	)
	require.NoError(t, err)
	workbench, err := fixture.Store.GetPRDevelopmentWorkbench(
		context.Background(),
		fixture.Orchestration.Operation.Case.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, workbench.RepairSession)
	peer, err := Open(
		context.Background(),
		path,
		WithClock(func() time.Time { return *fixture.Clock }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, peer.Close()) })
	*fixture.Clock = fixture.Clock.Add(time.Second)
	startInput := PRDevelopmentPublicationPushStart{
		PublicationID: pushClaim.ID,
		ClaimToken:    pushClaim.ClaimToken, ClaimEpoch: pushClaim.ClaimEpoch,
		Observation: fixture.Observation, ObservedAt: *fixture.Clock,
		Request: publicationPushRequestFor(pushClaim, fixture.Observation.HeadSHA),
	}
	admitInput := PRDevelopmentRepairAdmit{
		CaseID:                      fixture.Orchestration.Operation.Case.ID,
		ExpectedConversationVersion: conversation.Version,
		ExpectedRepairVersion:       workbench.RepairSession.Version,
		IdempotencyKey:              "publication-start-mutation-race",
		AgentID:                     workbench.RepairSession.AgentID,
		Instruction:                 "Race the next mutation against publication start.",
	}
	type startResult struct {
		publication PRDevelopmentPublication
		started     bool
		err         error
	}
	type admitResult struct {
		workbench PRDevelopmentWorkbench
		admitted  bool
		err       error
	}
	startResults := make(chan startResult, 1)
	admitResults := make(chan admitResult, 1)
	begin := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	go func() {
		ready.Done()
		<-begin
		publication, started, startErr := fixture.Store.StartPRDevelopmentPublicationPush(
			context.Background(),
			startInput,
		)
		startResults <- startResult{publication: publication, started: started, err: startErr}
	}()
	go func() {
		ready.Done()
		<-begin
		updated, admitted, admitErr := peer.AdmitPRDevelopmentRepair(
			context.Background(),
			admitInput,
		)
		admitResults <- admitResult{workbench: updated, admitted: admitted, err: admitErr}
	}()
	ready.Wait()
	close(begin)
	startOutcome := <-startResults
	admitOutcome := <-admitResults
	require.NoError(t, admitOutcome.err)
	require.True(t, admitOutcome.admitted)
	require.NotNil(t, admitOutcome.workbench.RepairSession)
	nextAttempt := admitOutcome.workbench.RepairSession.Attempts[len(
		admitOutcome.workbench.RepairSession.Attempts,
	)-1]
	lease, acquired, acquireErr := peer.AcquirePRDevelopmentControllerLease(
		context.Background(),
		PRDevelopmentControllerAcquire{
			CaseID:           fixture.Orchestration.Operation.Case.ID,
			AttemptID:        nextAttempt.ID,
			ExpectedRevision: fixture.Publication.ControllerRevision,
			Kind:             PRDevelopmentControllerMutationLease,
			WorkerLabel:      "publication-race-mutation", Lease: time.Minute,
		},
	)
	if startOutcome.started {
		require.NoError(t, startOutcome.err)
		assert.False(t, acquired)
		assert.Error(t, acquireErr)
		assert.Equal(t, PRDevelopmentPublicationPushStarted, startOutcome.publication.Status)
	} else {
		assert.Error(t, startOutcome.err)
		require.NoError(t, acquireErr)
		require.True(t, acquired)
		assert.Equal(t, PRDevelopmentControllerMutation, lease.Controller.Phase)
	}
}
