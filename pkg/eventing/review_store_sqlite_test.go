//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreReviewCaptureEditAndSubmissionLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, input := newReviewStoreFixture(t)

	reviewCase, created, err := store.CaptureReview(ctx, input)
	require.NoError(t, err)
	assert.True(t, created)
	assert.True(t, strings.HasPrefix(reviewCase.ID, reviewCaseIDPrefix))
	assert.Equal(t, ReviewCaseOpen, reviewCase.Status)
	assert.Equal(t, int64(1), reviewCase.Version)
	assert.Equal(t, 2, reviewCase.ActiveFindings)
	assert.Equal(t, []string{"go test ./..."}, reviewCase.Tests)
	assert.Equal(t, []string{"race coverage was not run"}, reviewCase.ResidualRisks)

	retry, created, err := store.CaptureReview(ctx, input)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, reviewCase.ID, retry.ID)

	conflict := input
	conflict.Draft.Summary = "different capture"
	_, _, err = store.CaptureReview(ctx, conflict)
	assert.ErrorIs(t, err, ErrReviewConflict)

	detail, err := store.GetReviewCase(ctx, reviewCase.ID)
	require.NoError(t, err)
	require.Len(t, detail.Findings, 2)
	assert.True(t, strings.HasPrefix(detail.Findings[0].ID, reviewFindingIDPrefix))
	detail.Case.Tests[0] = "mutated by caller"
	detail.Findings[0].Message = "mutated by caller"
	fresh, err := store.GetReviewCase(ctx, reviewCase.ID)
	require.NoError(t, err)
	assert.Equal(t, "go test ./...", fresh.Case.Tests[0])
	assert.Equal(t, input.Draft.Findings[0].Message, fresh.Findings[0].Message)

	page, err := store.ListReviewCases(ctx, ReviewCaseFilter{
		Repository: strings.ToUpper(input.Repository),
		PullNumber: input.PullNumber,
		Limit:      1,
	})
	require.NoError(t, err)
	require.Len(t, page.Cases, 1)
	assert.Equal(t, reviewCase.ID, page.Cases[0].ID)

	firstID := fresh.Findings[0].ID
	secondID := fresh.Findings[1].ID
	updatedDraft := input.Draft.Findings[0]
	updatedDraft.Message = "Use a bounded read before allocating the buffer."
	detail, err = store.UpdateReviewFinding(ctx, ReviewFindingUpdate{
		CaseID:          reviewCase.ID,
		FindingID:       firstID,
		ExpectedVersion: 1,
		Finding:         updatedDraft,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), detail.Case.Version)
	assert.Equal(t, int64(2), detail.Findings[0].Revision)
	assert.Equal(t, updatedDraft.Message, detail.Findings[0].Message)

	_, err = store.UpdateReviewFinding(ctx, ReviewFindingUpdate{
		CaseID:          reviewCase.ID,
		FindingID:       firstID,
		ExpectedVersion: 1,
		Finding:         updatedDraft,
	})
	assert.ErrorIs(t, err, ErrReviewConflict)

	retry, created, err = store.CaptureReview(ctx, input)
	require.NoError(t, err)
	assert.False(t, created, "capture retry remains idempotent after edits")
	assert.Equal(t, int64(2), retry.Version)

	detail, err = store.DropReviewFinding(ctx, ReviewFindingTransition{
		CaseID:          reviewCase.ID,
		FindingID:       firstID,
		ExpectedVersion: 2,
		Reason:          "not actionable",
	})
	require.NoError(t, err)
	assert.Equal(t, ReviewCaseOpen, detail.Case.Status)
	assert.Equal(t, 1, detail.Case.ActiveFindings)
	assert.Equal(t, ReviewFindingDropped, detail.Findings[0].State)

	detail, err = store.DropReviewFinding(ctx, ReviewFindingTransition{
		CaseID:          reviewCase.ID,
		FindingID:       secondID,
		ExpectedVersion: 3,
	})
	require.NoError(t, err)
	assert.Equal(t, ReviewCaseAllDropped, detail.Case.Status)
	assert.Zero(t, detail.Case.ActiveFindings)
	assert.NotNil(t, detail.Case.ResolvedAt)

	detail, err = store.RestoreReviewFinding(ctx, ReviewFindingTransition{
		CaseID:          reviewCase.ID,
		FindingID:       firstID,
		ExpectedVersion: 4,
	})
	require.NoError(t, err)
	assert.Equal(t, ReviewCaseOpen, detail.Case.Status)
	assert.Equal(t, 1, detail.Case.ActiveFindings)
	assert.Nil(t, detail.Case.ResolvedAt)

	detail, err = store.AppendReviewMessages(ctx, ReviewMessageAppend{
		CaseID:          reviewCase.ID,
		ExpectedVersion: 5,
		Messages: []ReviewMessageDraft{
			{
				FindingID: firstID,
				Kind:      ReviewMessageRephrase,
				Role:      ReviewMessageUser,
				Content:   "Make this more direct.",
			},
			{
				FindingID: firstID,
				Kind:      ReviewMessageRephrase,
				Role:      ReviewMessageAssistant,
				Content:   "Bound the read before allocating the buffer.",
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(6), detail.Case.Version)
	require.Len(t, detail.Messages, 2)
	assert.Equal(t, 0, detail.Messages[0].Ordinal)
	assert.Equal(t, 1, detail.Messages[1].Ordinal)
	assert.True(t, strings.HasPrefix(detail.Messages[0].ID, reviewMessageIDPrefix))

	submissionDraft := ReviewSubmissionDraft{
		CaseID:          reviewCase.ID,
		ExpectedVersion: 6,
		Marker:          "picoclaw-review/" + reviewCase.ID + "/6",
		Request: json.RawMessage(`{
			"event": "REQUEST_CHANGES",
			"body": "Automated review"
		}`),
	}
	detail, err = store.CreateReviewSubmission(ctx, submissionDraft)
	require.NoError(t, err)
	assert.Equal(t, ReviewCaseSubmitting, detail.Case.Status)
	assert.Equal(t, int64(7), detail.Case.Version)
	require.NotNil(t, detail.Submission)
	assert.Equal(t, ReviewSubmissionPending, detail.Submission.Status)
	assert.True(t, strings.HasPrefix(detail.Submission.ID, reviewSubmissionIDPrefix))
	submissionID := detail.Submission.ID

	retryDetail, err := store.CreateReviewSubmission(ctx, submissionDraft)
	require.NoError(t, err)
	assert.Equal(t, submissionID, retryDetail.Submission.ID)
	assert.Equal(t, int64(7), retryDetail.Case.Version)

	claimed, err := store.ClaimReviewSubmissions(ctx, "review-poster-a", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	assert.Equal(t, ReviewSubmissionClaimed, claimed[0].Status)
	assert.Equal(t, ReviewSubmissionPending, claimed[0].ClaimFrom)
	assert.Equal(t, 1, claimed[0].Attempts)
	firstLease := claimed[0].LeaseToken
	require.NoError(t, store.RenewReviewSubmissionLease(
		ctx,
		submissionID,
		firstLease,
		2*time.Minute,
	))
	assert.ErrorIs(t, store.RenewReviewSubmissionLease(
		ctx,
		submissionID,
		"wrong-lease",
		time.Minute,
	), ErrStaleLease)

	detail, err = store.FinishReviewSubmission(ctx, ReviewSubmissionOutcome{
		SubmissionID:     submissionID,
		LeaseToken:       firstLease,
		Status:           ReviewSubmissionSubmitted,
		ExternalReviewID: "123456",
		ExternalURL:      "https://github.com/acme/widgets/pull/42#pullrequestreview-123456",
	})
	require.NoError(t, err)
	assert.Equal(t, ReviewCaseSubmitted, detail.Case.Status)
	assert.Equal(t, int64(8), detail.Case.Version)
	assert.NotNil(t, detail.Case.ResolvedAt)
	assert.NotNil(t, detail.Case.SubmittedAt)
	assert.Equal(t, ReviewSubmissionSubmitted, detail.Submission.Status)
	assert.Equal(t, "123456", detail.Submission.ExternalReviewID)
	assert.Empty(t, detail.Submission.LeaseToken)
	assert.NotNil(t, detail.Submission.SubmittedAt)

	storedSubmission, err := store.GetReviewSubmission(ctx, submissionID)
	require.NoError(t, err)
	assert.Equal(t, ReviewSubmissionSubmitted, storedSubmission.Status)
	assert.JSONEq(t, string(submissionDraft.Request), string(storedSubmission.Request))
	publicJSON, err := json.Marshal(detail)
	require.NoError(t, err)
	for _, internalField := range []string{
		`"marker"`,
		`"claim_from"`,
		`"lease_token"`,
		`"lease_until"`,
		`"request"`,
		`"internal_error"`,
	} {
		assert.NotContains(t, string(publicJSON), internalField)
	}
}

func TestStoreReviewUnknownSubmissionIsTerminalAndNeverReclaimed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, input := newReviewStoreFixture(t)
	reviewCase, _, err := store.CaptureReview(ctx, input)
	require.NoError(t, err)
	detail, err := store.CreateReviewSubmission(ctx, ReviewSubmissionDraft{
		CaseID:          reviewCase.ID,
		ExpectedVersion: 1,
		Marker:          "picoclaw-review/" + reviewCase.ID + "/1",
		Request:         json.RawMessage(`{"event":"COMMENT","body":"Automated review"}`),
	})
	require.NoError(t, err)
	require.NotNil(t, detail.Submission)

	claimed, err := store.ClaimReviewSubmissions(ctx, "review-poster", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	detail, err = store.FinishReviewSubmission(ctx, ReviewSubmissionOutcome{
		SubmissionID:    claimed[0].ID,
		LeaseToken:      claimed[0].LeaseToken,
		Status:          ReviewSubmissionUnknown,
		PublicErrorCode: "github_timeout",
		InternalError:   "remote outcome could not be determined",
	})
	require.NoError(t, err)
	assert.Equal(t, ReviewCaseSubmissionUnknown, detail.Case.Status)
	assert.Equal(t, int64(3), detail.Case.Version)
	assert.NotNil(t, detail.Case.ResolvedAt)
	assert.Equal(t, ReviewSubmissionUnknown, detail.Submission.Status)
	assert.Empty(t, detail.Submission.LeaseToken)

	claimed, err = store.ClaimReviewSubmissions(ctx, "review-poster", 10, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, claimed, "unknown outcomes must never be automatically retried")

	_, err = store.DropReviewFinding(ctx, ReviewFindingTransition{
		CaseID:          reviewCase.ID,
		FindingID:       detail.Findings[0].ID,
		ExpectedVersion: detail.Case.Version,
	})
	assert.ErrorIs(t, err, ErrInvalidTransition)
	_, err = store.AppendReviewMessages(ctx, ReviewMessageAppend{
		CaseID:          reviewCase.ID,
		ExpectedVersion: detail.Case.Version,
		Messages: []ReviewMessageDraft{{
			Kind:    ReviewMessageChat,
			Role:    ReviewMessageUser,
			Content: "retry",
		}},
	})
	assert.ErrorIs(t, err, ErrInvalidTransition)
}

func TestStoreReviewUnknownSubmissionHumanReconciliation(t *testing.T) {
	makeUnknown := func(t *testing.T) (*Store, ReviewCaseDetail) {
		t.Helper()
		ctx := context.Background()
		store, _, input := newReviewStoreFixture(t)
		reviewCase, _, err := store.CaptureReview(ctx, input)
		require.NoError(t, err)
		detail, err := store.CreateReviewSubmission(ctx, ReviewSubmissionDraft{
			CaseID:          reviewCase.ID,
			ExpectedVersion: reviewCase.Version,
			Marker:          "picoclaw-review/" + reviewCase.ID + "/1",
			Request:         json.RawMessage(`{"event":"COMMENT"}`),
		})
		require.NoError(t, err)
		claimed, err := store.ClaimReviewSubmissions(
			ctx,
			"review-poster",
			1,
			time.Minute,
		)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		detail, err = store.FinishReviewSubmission(ctx, ReviewSubmissionOutcome{
			SubmissionID:    claimed[0].ID,
			LeaseToken:      claimed[0].LeaseToken,
			Status:          ReviewSubmissionUnknown,
			PublicErrorCode: "github_outcome_unknown",
			InternalError:   "response was lost",
			ExternalURL:     "https://github.com/acme/widgets/pull/42",
		})
		require.NoError(t, err)
		require.Equal(t, ReviewCaseSubmissionUnknown, detail.Case.Status)
		require.Equal(t, int64(3), detail.Case.Version)
		return store, detail
	}

	t.Run("submitted", func(t *testing.T) {
		ctx := context.Background()
		store, unknown := makeUnknown(t)
		externalURL := unknown.Submission.ExternalURL

		resolved, err := store.ReconcileReviewSubmission(
			ctx,
			ReviewSubmissionReconciliation{
				CaseID:          unknown.Case.ID,
				ExpectedVersion: unknown.Case.Version,
				Resolution:      ReviewReconciliationSubmitted,
			},
		)
		require.NoError(t, err)
		assert.Equal(t, ReviewCaseSubmitted, resolved.Case.Status)
		assert.Equal(t, int64(4), resolved.Case.Version)
		assert.Empty(t, resolved.Case.PublicErrorCode)
		assert.NotNil(t, resolved.Case.ResolvedAt)
		assert.NotNil(t, resolved.Case.SubmittedAt)
		require.NotNil(t, resolved.Submission)
		assert.Equal(t, ReviewSubmissionSubmitted, resolved.Submission.Status)
		assert.Empty(t, resolved.Submission.PublicErrorCode)
		assert.Equal(t, externalURL, resolved.Submission.ExternalURL)
		assert.NotNil(t, resolved.Submission.SubmittedAt)

		_, err = store.ReconcileReviewSubmission(
			ctx,
			ReviewSubmissionReconciliation{
				CaseID:          unknown.Case.ID,
				ExpectedVersion: unknown.Case.Version,
				Resolution:      ReviewReconciliationSubmitted,
			},
		)
		assert.ErrorIs(t, err, ErrReviewConflict)
	})

	t.Run("absent reopens editable new version", func(t *testing.T) {
		ctx := context.Background()
		store, unknown := makeUnknown(t)
		externalURL := unknown.Submission.ExternalURL

		reopened, err := store.ReconcileReviewSubmission(
			ctx,
			ReviewSubmissionReconciliation{
				CaseID:          unknown.Case.ID,
				ExpectedVersion: unknown.Case.Version,
				Resolution:      ReviewReconciliationAbsent,
			},
		)
		require.NoError(t, err)
		assert.Equal(t, ReviewCaseOpen, reopened.Case.Status)
		assert.Equal(t, int64(4), reopened.Case.Version)
		assert.Empty(t, reopened.Case.PublicErrorCode)
		assert.Nil(t, reopened.Case.ResolvedAt)
		assert.Nil(t, reopened.Case.SubmittedAt)
		require.NotNil(t, reopened.Submission)
		assert.Equal(t, ReviewSubmissionFailed, reopened.Submission.Status)
		assert.Equal(t, "reconciled_absent", reopened.Submission.PublicErrorCode)
		assert.Equal(t, externalURL, reopened.Submission.ExternalURL)
		assert.Nil(t, reopened.Submission.SubmittedAt)
		firstSubmissionID := reopened.Submission.ID

		edit := reopened.Findings[0]
		edited, err := store.UpdateReviewFinding(ctx, ReviewFindingUpdate{
			CaseID:          reopened.Case.ID,
			FindingID:       edit.ID,
			ExpectedVersion: reopened.Case.Version,
			Finding: ReviewFindingDraft{
				Severity:       edit.Severity,
				Title:          edit.Title,
				File:           edit.File,
				Line:           edit.Line,
				Message:        edit.Message + " Confirmed after reconciliation.",
				Evidence:       edit.Evidence,
				Impact:         edit.Impact,
				Recommendation: edit.Recommendation,
				Validation:     edit.Validation,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, int64(5), edited.Case.Version)
		second, err := store.CreateReviewSubmission(ctx, ReviewSubmissionDraft{
			CaseID:          edited.Case.ID,
			ExpectedVersion: edited.Case.Version,
			Marker:          "picoclaw-review/" + edited.Case.ID + "/5",
			Request:         json.RawMessage(`{"event":"COMMENT","retry":true}`),
		})
		require.NoError(t, err)
		require.NotNil(t, second.Submission)
		assert.NotEqual(t, firstSubmissionID, second.Submission.ID)
		assert.Equal(t, int64(5), second.Submission.DraftVersion)
	})

	t.Run("invalid resolution is atomic", func(t *testing.T) {
		ctx := context.Background()
		store, unknown := makeUnknown(t)
		_, err := store.ReconcileReviewSubmission(
			ctx,
			ReviewSubmissionReconciliation{
				CaseID:          unknown.Case.ID,
				ExpectedVersion: unknown.Case.Version,
				Resolution:      "retry",
			},
		)
		assert.ErrorIs(t, err, ErrInvalidReview)
		unchanged, getErr := store.GetReviewCase(ctx, unknown.Case.ID)
		require.NoError(t, getErr)
		assert.Equal(t, unknown.Case.Version, unchanged.Case.Version)
		assert.Equal(t, ReviewCaseSubmissionUnknown, unchanged.Case.Status)
		assert.Equal(t, ReviewSubmissionUnknown, unchanged.Submission.Status)
	})
}

func TestStoreReviewTranscriptLimitsAreExactAndAtomic(t *testing.T) {
	t.Run("message count", func(t *testing.T) {
		ctx := context.Background()
		store, _, input := newReviewStoreFixture(t)
		reviewCase, _, err := store.CaptureReview(ctx, input)
		require.NoError(t, err)

		version := reviewCase.Version
		remaining := MaxReviewMessagesPerCase
		for remaining > 0 {
			batchSize := remaining
			if batchSize > maxReviewMessagesPerAppend {
				batchSize = maxReviewMessagesPerAppend
			}
			messages := make([]ReviewMessageDraft, batchSize)
			for index := range messages {
				messages[index] = ReviewMessageDraft{
					Kind:    ReviewMessageChat,
					Role:    ReviewMessageUser,
					Content: "x",
				}
			}
			detail, appendErr := store.AppendReviewMessages(
				ctx,
				ReviewMessageAppend{
					CaseID:          reviewCase.ID,
					ExpectedVersion: version,
					Messages:        messages,
				},
			)
			require.NoError(t, appendErr)
			version = detail.Case.Version
			remaining -= batchSize
		}

		detail, err := store.GetReviewCase(ctx, reviewCase.ID)
		require.NoError(t, err)
		require.Len(t, detail.Messages, MaxReviewMessagesPerCase)
		_, err = store.AppendReviewMessages(ctx, ReviewMessageAppend{
			CaseID:          reviewCase.ID,
			ExpectedVersion: version,
			Messages: []ReviewMessageDraft{{
				Kind:    ReviewMessageChat,
				Role:    ReviewMessageAssistant,
				Content: "overflow",
			}},
		})
		assert.ErrorIs(t, err, ErrInvalidTransition)

		unchanged, err := store.GetReviewCase(ctx, reviewCase.ID)
		require.NoError(t, err)
		assert.Equal(t, version, unchanged.Case.Version)
		assert.Len(t, unchanged.Messages, MaxReviewMessagesPerCase)
	})

	t.Run("UTF-8 content bytes", func(t *testing.T) {
		ctx := context.Background()
		store, _, input := newReviewStoreFixture(t)
		reviewCase, _, err := store.CaptureReview(ctx, input)
		require.NoError(t, err)

		maximumMessage := strings.Repeat("é", MaxReviewMessageBytes/2)
		require.Equal(t, MaxReviewMessageBytes, len(maximumMessage))
		messageCount := MaxReviewTranscriptBytes / MaxReviewMessageBytes
		version := reviewCase.Version
		remaining := messageCount
		for remaining > 0 {
			batchSize := remaining
			if batchSize > maxReviewMessagesPerAppend {
				batchSize = maxReviewMessagesPerAppend
			}
			messages := make([]ReviewMessageDraft, batchSize)
			for index := range messages {
				messages[index] = ReviewMessageDraft{
					Kind:    ReviewMessageChat,
					Role:    ReviewMessageAssistant,
					Content: maximumMessage,
				}
			}
			detail, appendErr := store.AppendReviewMessages(
				ctx,
				ReviewMessageAppend{
					CaseID:          reviewCase.ID,
					ExpectedVersion: version,
					Messages:        messages,
				},
			)
			require.NoError(t, appendErr)
			version = detail.Case.Version
			remaining -= batchSize
		}

		detail, err := store.GetReviewCase(ctx, reviewCase.ID)
		require.NoError(t, err)
		require.Len(t, detail.Messages, messageCount)
		_, err = store.AppendReviewMessages(ctx, ReviewMessageAppend{
			CaseID:          reviewCase.ID,
			ExpectedVersion: version,
			Messages: []ReviewMessageDraft{{
				Kind:    ReviewMessageChat,
				Role:    ReviewMessageAssistant,
				Content: "x",
			}},
		})
		assert.ErrorIs(t, err, ErrInvalidTransition)

		unchanged, err := store.GetReviewCase(ctx, reviewCase.ID)
		require.NoError(t, err)
		assert.Equal(t, version, unchanged.Case.Version)
		assert.Len(t, unchanged.Messages, messageCount)
	})
}

func TestStoreReviewFailedSubmissionCanRetryAndBecomeStale(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, input := newReviewStoreFixture(t)
	reviewCase, _, err := store.CaptureReview(ctx, input)
	require.NoError(t, err)
	first, err := store.CreateReviewSubmission(ctx, ReviewSubmissionDraft{
		CaseID:          reviewCase.ID,
		ExpectedVersion: 1,
		Marker:          "picoclaw-review/" + reviewCase.ID + "/1",
		Request:         json.RawMessage(`{"event":"REQUEST_CHANGES"}`),
	})
	require.NoError(t, err)
	firstID := first.Submission.ID
	claimed, err := store.ClaimReviewSubmissions(ctx, "review-poster", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	failed, err := store.FinishReviewSubmission(ctx, ReviewSubmissionOutcome{
		SubmissionID:    claimed[0].ID,
		LeaseToken:      claimed[0].LeaseToken,
		Status:          ReviewSubmissionFailed,
		PublicErrorCode: "github_unavailable",
	})
	require.NoError(t, err)
	assert.Equal(t, ReviewCaseOpen, failed.Case.Status)
	assert.Equal(t, int64(3), failed.Case.Version)
	assert.Nil(t, failed.Case.ResolvedAt)
	assert.Equal(t, ReviewSubmissionFailed, failed.Submission.Status)

	second, err := store.CreateReviewSubmission(ctx, ReviewSubmissionDraft{
		CaseID:          reviewCase.ID,
		ExpectedVersion: 3,
		Marker:          "picoclaw-review/" + reviewCase.ID + "/3",
		Request:         json.RawMessage(`{"event":"REQUEST_CHANGES"}`),
	})
	require.NoError(t, err)
	require.NotNil(t, second.Submission)
	assert.NotEqual(t, firstID, second.Submission.ID)
	assert.Equal(t, int64(3), second.Submission.DraftVersion)

	claimed, err = store.ClaimReviewSubmissions(ctx, "review-poster", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	stale, err := store.FinishReviewSubmission(ctx, ReviewSubmissionOutcome{
		SubmissionID:    claimed[0].ID,
		LeaseToken:      claimed[0].LeaseToken,
		Status:          ReviewSubmissionFailed,
		Stale:           true,
		PublicErrorCode: "pull_head_changed",
	})
	require.NoError(t, err)
	assert.Equal(t, ReviewCaseStale, stale.Case.Status)
	assert.Equal(t, int64(5), stale.Case.Version)
	assert.NotNil(t, stale.Case.ResolvedAt)
	assert.Equal(t, "pull_head_changed", stale.Case.PublicErrorCode)
}

func TestStoreReviewCaptureVerifiesTrustedDispatchIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _, input := newReviewStoreFixture(t)
	input.WorkflowRevision = "wrong-revision"

	_, _, err := store.CaptureReview(ctx, input)
	assert.ErrorIs(t, err, ErrReviewConflict)

	page, listErr := store.ListReviewCases(ctx, ReviewCaseFilter{})
	require.NoError(t, listErr)
	assert.Empty(t, page.Cases)
}

func TestStorePruneRetainsEventsReferencedByReviewCases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, clock, input := newReviewStoreFixture(t)
	reviewCase, _, err := store.CaptureReview(ctx, input)
	require.NoError(t, err)

	unreferenced, err := store.Insert(ctx, Envelope{
		Source:    "github",
		Connector: input.Connector,
		Type:      "pull_request.opened",
		DedupeKey: "delivery-unreferenced",
		Payload:   json.RawMessage(`{}`),
	})
	require.NoError(t, err)

	_, err = store.db.Exec(`
		UPDATE event_inbox
		SET routing_status = ?, received_at = ?
		WHERE id IN (?, ?)`,
		RoutingSucceeded,
		toDBTime(clock.Add(-time.Hour)),
		input.EventID,
		unreferenced.Event.Envelope.ID,
	)
	require.NoError(t, err)
	_, err = store.db.Exec(`
		UPDATE event_dispatches
		SET status = ?, finished_at = ?
		WHERE id = ?`,
		DispatchSucceeded,
		toDBTime(*clock),
		input.DispatchID,
	)
	require.NoError(t, err)

	pruned, err := store.Prune(ctx, clock.Add(time.Minute), 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), pruned)
	_, err = store.Get(ctx, input.EventID)
	require.NoError(t, err)
	_, err = store.GetReviewCase(ctx, reviewCase.ID)
	require.NoError(t, err)
	_, err = store.Get(ctx, unreferenced.Event.Envelope.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func newReviewStoreFixture(
	t *testing.T,
) (*Store, *time.Time, ReviewCaptureInput) {
	t.Helper()

	ctx := context.Background()
	now := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	store, err := Open(ctx, ":memory:", WithClock(func() time.Time {
		return now
	}))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})

	inserted, err := store.Insert(ctx, Envelope{
		Source:    "github",
		Connector: "github-primary",
		Type:      "pull_request.opened",
		DedupeKey: "delivery-review",
		Payload:   json.RawMessage(`{}`),
	})
	require.NoError(t, err)
	claimed, err := store.ClaimRouting(ctx, "review-router", 1, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	dispatch, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		ctx,
		inserted.Event.Envelope.ID,
		claimed[0].Routing.LeaseToken,
		"workflows/pull-request-review.yml",
		"revision-2026-07-30",
	)
	require.NoError(t, err)
	require.True(t, created)

	firstLine := 27
	secondLine := 91
	return store, &now, ReviewCaptureInput{
		EventID:          inserted.Event.Envelope.ID,
		DispatchID:       dispatch.ID,
		RunID:            dispatch.RunID,
		WorkflowRef:      dispatch.WorkflowRef,
		WorkflowRevision: dispatch.WorkflowRevision,
		Connector:        inserted.Event.Envelope.Connector,
		Repository:       "acme/widgets",
		PullNumber:       42,
		PullURL:          "https://github.com/acme/widgets/pull/42",
		BaseSHA:          strings.Repeat("a", 40),
		HeadSHA:          strings.Repeat("b", 40),
		Draft: ReviewDraft{
			SchemaVersion: ReviewDraftSchemaVersion,
			Summary:       "The change needs two correctness fixes.",
			Tests:         []string{"go test ./..."},
			ResidualRisks: []string{"race coverage was not run"},
			Findings: []ReviewFindingDraft{
				{
					Severity:       ReviewSeverityHigh,
					Title:          "Unbounded allocation",
					File:           "pkg/parser/read.go",
					Line:           &firstLine,
					Message:        "The input controls the allocation size.",
					Evidence:       "size is read directly from the request",
					Impact:         "a request can exhaust memory",
					Recommendation: "bound size before allocating",
					Validation:     "add an oversized-input test",
				},
				{
					Severity:       ReviewSeverityMedium,
					Title:          "Lost cancellation",
					File:           "pkg/worker/run.go",
					Line:           &secondLine,
					Message:        "The child operation ignores the caller context.",
					Evidence:       "context.Background is passed to the child",
					Impact:         "cancelled jobs keep running",
					Recommendation: "pass the caller context",
					Validation:     "add a cancellation test",
				},
			},
		},
	}
}
