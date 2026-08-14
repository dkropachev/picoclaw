package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type reviewReconciliationSubmitter struct {
	submitResult       reviews.SubmitResult
	submitErr          error
	pendingResult      reviews.SubmitResult
	pendingErr         error
	submitCalls        int
	pendingCalls       int
	lastPendingRequest reviews.SubmitRequest
}

func (submitter *reviewReconciliationSubmitter) Submit(
	context.Context,
	reviews.SubmitRequest,
) (reviews.SubmitResult, error) {
	submitter.submitCalls++
	return submitter.submitResult, submitter.submitErr
}

func (submitter *reviewReconciliationSubmitter) SubmitPending(
	_ context.Context,
	request reviews.SubmitRequest,
) (reviews.SubmitResult, error) {
	submitter.pendingCalls++
	submitter.lastPendingRequest = request
	return submitter.pendingResult, submitter.pendingErr
}

type reviewHistoryRunner struct {
	responses []string
	requests  []workflows.ToolRequest
}

func (runner *reviewHistoryRunner) RunTool(
	_ context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	runner.requests = append(runner.requests, request)
	if len(runner.responses) == 0 {
		return nil, errors.New("unexpected review-history read")
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	return map[string]any{"text": response}, nil
}

func TestPRWorkspaceReviewPublishDoesNotTreatPendingResultAsSuccess(t *testing.T) {
	submitter := &reviewReconciliationSubmitter{submitResult: reviews.SubmitResult{
		PendingReview: map[string]any{
			"id":       float64(101),
			"html_url": "https://github.com/octo/repo/pull/42#pullrequestreview-101",
		},
	}}
	runtime := &prWorkspaceReviewPublicationRuntime{submitter: submitter}
	result, err := runtime.PublishReview(context.Background(), reviewReconciliationRequest())
	if err == nil || !result.Ambiguous || result.ExternalID != "" || submitter.submitCalls != 1 {
		t.Fatalf("pending create was recorded as publication success: result=%#v calls=%d err=%v", result, submitter.submitCalls, err)
	}
}

func TestPRWorkspaceReviewPublishRejectsForeignSubmittedIdentity(t *testing.T) {
	request := reviewReconciliationRequest()
	for _, test := range []struct {
		name        string
		externalID  any
		externalURL string
	}{
		{name: "foreign origin", externalID: float64(101), externalURL: "https://attacker.example/octo/repo/pull/42#pullrequestreview-101"},
		{name: "different pull", externalID: float64(101), externalURL: "https://github.com/octo/repo/pull/99#pullrequestreview-101"},
		{name: "different review", externalID: float64(101), externalURL: "https://github.com/octo/repo/pull/42#pullrequestreview-999"},
		{name: "nonnumeric review ID", externalID: "review-101", externalURL: "https://github.com/octo/repo/pull/42#pullrequestreview-review-101"},
	} {
		t.Run(test.name, func(t *testing.T) {
			submitter := &reviewReconciliationSubmitter{submitResult: reviews.SubmitResult{
				SubmittedReview: map[string]any{"id": test.externalID, "html_url": test.externalURL},
			}}
			runtime := &prWorkspaceReviewPublicationRuntime{submitter: submitter}
			result, err := runtime.PublishReview(context.Background(), request)
			if err == nil || !result.Ambiguous || result.ExternalID == "" {
				t.Fatalf("foreign submitted identity accepted: result=%#v err=%v", result, err)
			}
		})
	}
}

func TestPRWorkspaceReviewReconciliationAcceptsOnlySubmittedComment(t *testing.T) {
	request := reviewReconciliationRequest()
	if !samePRWorkspaceReviewURL(
		"https://github.com/octo/repo/pull/42#pullrequestreview-101",
		request.Provider,
		"101",
	) {
		t.Fatal("expected canonical review URL to satisfy the publication fence")
	}
	runner := &reviewHistoryRunner{responses: []string{
		reviewHistoryJSON(t, reviewHistoryRecord(request, "COMMENTED", 101, request.Marker)),
	}}
	provider, err := reviews.NewGitHubProvider(runner, "")
	if err != nil {
		t.Fatal(err)
	}
	submitter := &reviewReconciliationSubmitter{}
	runtime := &prWorkspaceReviewPublicationRuntime{submitter: submitter, provider: provider}

	result, found, err := runtime.ReconcileReview(context.Background(), request)
	if err != nil || !found || result.Ambiguous || result.ExternalID != "101" || submitter.pendingCalls != 0 {
		t.Fatalf("submitted COMMENT reconciliation = result %#v found=%v pending_calls=%d err=%v", result, found, submitter.pendingCalls, err)
	}
}

func TestPRWorkspaceReviewReconciliationCompletesPendingThenReobservesComment(t *testing.T) {
	request := reviewReconciliationRequest()
	pending := reviewHistoryJSON(t, reviewHistoryRecord(request, "PENDING", 101, request.Marker))
	submitted := reviewHistoryJSON(t, reviewHistoryRecord(request, "COMMENTED", 101, request.Marker))
	runner := &reviewHistoryRunner{responses: []string{pending, submitted}}
	provider, err := reviews.NewGitHubProvider(runner, "")
	if err != nil {
		t.Fatal(err)
	}
	submitter := &reviewReconciliationSubmitter{}
	runtime := &prWorkspaceReviewPublicationRuntime{submitter: submitter, provider: provider}

	result, found, err := runtime.ReconcileReview(context.Background(), request)
	if err != nil || !found || result.ExternalID != "101" || submitter.pendingCalls != 1 {
		t.Fatalf("pending recovery = result %#v found=%v pending_calls=%d err=%v", result, found, submitter.pendingCalls, err)
	}
	if submitter.lastPendingRequest.Marker != request.Marker ||
		submitter.lastPendingRequest.Summary != request.Summary ||
		len(submitter.lastPendingRequest.Findings) != len(request.Findings) {
		t.Fatalf("pending recovery did not use frozen review payload: %#v", submitter.lastPendingRequest)
	}
}

func TestPRWorkspaceReviewReconciliationNeverAcceptsPersistentPending(t *testing.T) {
	request := reviewReconciliationRequest()
	pending := reviewHistoryJSON(t, reviewHistoryRecord(request, "PENDING", 101, request.Marker))
	runner := &reviewHistoryRunner{responses: []string{pending, pending}}
	provider, err := reviews.NewGitHubProvider(runner, "")
	if err != nil {
		t.Fatal(err)
	}
	submitter := &reviewReconciliationSubmitter{}
	runtime := &prWorkspaceReviewPublicationRuntime{submitter: submitter, provider: provider}

	result, found, err := runtime.ReconcileReview(context.Background(), request)
	if err == nil || found || !result.Ambiguous || result.ExternalID != "101" || submitter.pendingCalls != 1 {
		t.Fatalf("persistent pending review was accepted: result=%#v found=%v pending_calls=%d err=%v", result, found, submitter.pendingCalls, err)
	}
}

func TestPRWorkspaceReviewReconciliationRejectsMalformedAndDuplicateMarkers(t *testing.T) {
	request := reviewReconciliationRequest()
	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed JSON", raw: `{"broken":`},
		{
			name: "duplicate reviews",
			raw: reviewHistoryJSON(t,
				reviewHistoryRecord(request, "COMMENTED", 101, request.Marker),
				reviewHistoryRecord(request, "COMMENTED", 102, request.Marker),
			),
		},
		{
			name: "duplicate marker in body",
			raw:  reviewHistoryJSON(t, reviewHistoryRecord(request, "COMMENTED", 101, request.Marker+"\n"+request.Marker)),
		},
		{
			name: "missing state",
			raw:  reviewHistoryJSON(t, reviewHistoryRecord(request, "", 101, request.Marker)),
		},
		{
			name: "wrong commit",
			raw: reviewHistoryJSON(t, map[string]any{
				"id": 101, "body": request.Marker, "state": "COMMENTED",
				"commit_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"html_url":  "https://github.com/octo/repo/pull/42#pullrequestreview-101",
			}),
		},
		{
			name: "foreign review URL",
			raw: reviewHistoryJSON(t, map[string]any{
				"id": 101, "body": request.Marker, "state": "COMMENTED",
				"commit_id": request.Provider.HeadSHA,
				"html_url":  "https://attacker.example/octo/repo/pull/42#pullrequestreview-101",
			}),
		},
		{
			name: "different pull URL",
			raw: reviewHistoryJSON(t, map[string]any{
				"id": 101, "body": request.Marker, "state": "COMMENTED",
				"commit_id": request.Provider.HeadSHA,
				"html_url":  "https://github.com/octo/repo/pull/99#pullrequestreview-101",
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &reviewHistoryRunner{responses: []string{test.raw}}
			provider, err := reviews.NewGitHubProvider(runner, "")
			if err != nil {
				t.Fatal(err)
			}
			submitter := &reviewReconciliationSubmitter{}
			runtime := &prWorkspaceReviewPublicationRuntime{submitter: submitter, provider: provider}
			result, found, err := runtime.ReconcileReview(context.Background(), request)
			if err == nil || found || result.ExternalID != "" || submitter.pendingCalls != 0 {
				t.Fatalf("malformed marker response accepted: result=%#v found=%v pending_calls=%d err=%v", result, found, submitter.pendingCalls, err)
			}
		})
	}
}

func reviewReconciliationRequest() prworkspace.ReviewPublicationRequest {
	return prworkspace.ReviewPublicationRequest{
		Provider: prworkspace.ProviderSnapshot{
			ProviderOrigin: "https://github.com", Repository: "octo/repo", PullNumber: 42,
			HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		Summary: "Review summary",
		Findings: []prworkspace.Finding{{
			ID: "pfn_11111111111111111111111111111111", Title: "Retry can be lost",
			File: "pkg/retry.go", Message: "Preserve the retry token.",
		}},
		Marker: "picoclaw-pr-publication:ppb_11111111111111111111111111111111:sha256:marker",
	}
}

func reviewHistoryRecord(
	request prworkspace.ReviewPublicationRequest,
	state string,
	id int64,
	body string,
) map[string]any {
	return map[string]any{
		"id": id, "body": body, "state": state, "commit_id": request.Provider.HeadSHA,
		"html_url": "https://github.com/octo/repo/pull/42#pullrequestreview-" + fmt.Sprint(id),
	}
}

func reviewHistoryJSON(t *testing.T, values ...map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
