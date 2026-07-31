package reviews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestSubmissionWorkerClaimsPendingAndFinishesSubmitted(t *testing.T) {
	request := workerSubmitRequest()
	queue := newWorkerQueue(t, request)
	submitter := &workerSubmitterFake{
		result: SubmitResult{
			SubmittedReview: map[string]any{
				"review": map[string]any{
					"id":       json.Number("987654321"),
					"html_url": "https://github.com/scylladb/gocql/pull/42#pullrequestreview-987654321",
				},
			},
		},
	}
	worker := &SubmissionWorker{
		Queue:         queue,
		Submitter:     submitter,
		WorkerLabel:   " review-worker-a ",
		LeaseDuration: 3 * time.Minute,
	}

	processed, err := worker.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessOne() processed = false, want true")
	}
	if got, want := submitter.requests, []SubmitRequest{request}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Submit() requests = %#v, want %#v", got, want)
	}
	if got, want := queue.claimLabels(), []string{"review-worker-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claim labels = %#v, want %#v", got, want)
	}
	if got, want := queue.claimLimits(), []int{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claim limits = %#v, want %#v", got, want)
	}
	if got, want := queue.claimLeases(), []time.Duration{3 * time.Minute}; !reflect.DeepEqual(got, want) {
		t.Fatalf("claim leases = %#v, want %#v", got, want)
	}
	outcomes := queue.finishedOutcomes()
	if len(outcomes) != 1 {
		t.Fatalf("finish outcomes = %d, want 1", len(outcomes))
	}
	outcome := outcomes[0]
	if outcome.SubmissionID != serviceTestSubmissionID ||
		outcome.LeaseToken != "lease-token-1" ||
		outcome.Status != eventing.ReviewSubmissionSubmitted ||
		outcome.PublicErrorCode != "" ||
		outcome.InternalError != "" ||
		outcome.ExternalReviewID != "987654321" ||
		outcome.ExternalURL !=
			"https://github.com/scylladb/gocql/pull/42#pullrequestreview-987654321" {
		t.Fatalf("submitted outcome = %#v", outcome)
	}

	processed, err = worker.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("second ProcessOne() error = %v", err)
	}
	if processed {
		t.Fatal("second ProcessOne() processed = true, submitted item was reclaimed")
	}
	if len(submitter.requests) != 1 {
		t.Fatalf("GitHub submission calls = %d, want exactly 1", len(submitter.requests))
	}
}

func TestSubmissionWorkerMarksAmbiguousOutcomeUnknownAndNeverReclaims(t *testing.T) {
	request := workerSubmitRequest()
	queue := newWorkerQueue(t, request)
	transportErr := errors.New("response lost after write")
	submitter := &workerSubmitterFake{
		err: &SubmitStageError{
			Stage:                       SubmitStageSubmitPending,
			FindingIndex:                -1,
			CompletedCalls:              2,
			ExternalStateMayHaveChanged: true,
			Err:                         transportErr,
		},
	}
	worker := &SubmissionWorker{
		Queue:     queue,
		Submitter: submitter,
	}

	processed, err := worker.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessOne() processed = false, want true")
	}
	outcomes := queue.finishedOutcomes()
	if len(outcomes) != 1 {
		t.Fatalf("finish outcomes = %d, want 1", len(outcomes))
	}
	outcome := outcomes[0]
	if outcome.Status != eventing.ReviewSubmissionUnknown ||
		outcome.PublicErrorCode != "github_outcome_unknown" ||
		!strings.Contains(outcome.InternalError, transportErr.Error()) ||
		outcome.ExternalReviewID != "" ||
		outcome.ExternalURL != "https://github.com/scylladb/gocql/pull/42" {
		t.Fatalf("unknown outcome = %#v", outcome)
	}

	for attempt := 0; attempt < 2; attempt++ {
		processed, err = worker.ProcessOne(context.Background())
		if err != nil {
			t.Fatalf("repeat ProcessOne() error = %v", err)
		}
		if processed {
			t.Fatalf("repeat ProcessOne() %d reclaimed unknown item", attempt+1)
		}
	}
	if len(submitter.requests) != 1 {
		t.Fatalf("ambiguous submission calls = %d, want exactly 1", len(submitter.requests))
	}
	if got := len(queue.finishedOutcomes()); got != 1 {
		t.Fatalf("unknown finish calls = %d, want exactly 1", got)
	}
}

func TestSubmissionWorkerTreatsUntypedSubmitterErrorAsUnknown(t *testing.T) {
	for _, submitterErr := range []error{
		errors.New("submitter returned an unclassified failure"),
		fmt.Errorf("untyped validation sentinel: %w", ErrInvalidSubmitRequest),
	} {
		t.Run(submitterErr.Error(), func(t *testing.T) {
			request := workerSubmitRequest()
			queue := newWorkerQueue(t, request)
			worker := &SubmissionWorker{
				Queue:     queue,
				Submitter: &workerSubmitterFake{err: submitterErr},
			}

			processed, err := worker.ProcessOne(context.Background())
			if err != nil || !processed {
				t.Fatalf("ProcessOne() = (%v, %v), want (true, nil)", processed, err)
			}
			outcomes := queue.finishedOutcomes()
			if len(outcomes) != 1 {
				t.Fatalf("finish outcomes = %d, want 1", len(outcomes))
			}
			outcome := outcomes[0]
			if outcome.Status != eventing.ReviewSubmissionUnknown ||
				outcome.PublicErrorCode != "github_outcome_unknown" ||
				outcome.Stale ||
				outcome.ExternalURL != "https://github.com/scylladb/gocql/pull/42" {
				t.Fatalf("unknown outcome = %#v", outcome)
			}
		})
	}
}

func TestSubmissionWorkerMarksHeadChangeTerminalStale(t *testing.T) {
	request := workerSubmitRequest()
	queue := newWorkerQueue(t, request)
	headErr := &PullRequestHeadChangedError{
		Expected: request.HeadSHA,
		Actual:   strings.Repeat("c", 40),
	}
	worker := &SubmissionWorker{
		Queue: queue,
		Submitter: &workerSubmitterFake{err: &SubmitStageError{
			Stage:        SubmitStageVerifyHead,
			FindingIndex: -1,
			Err:          headErr,
		}},
	}

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v), want (true, nil)", processed, err)
	}
	outcomes := queue.finishedOutcomes()
	if len(outcomes) != 1 {
		t.Fatalf("finish outcomes = %d, want 1", len(outcomes))
	}
	outcome := outcomes[0]
	if outcome.Status != eventing.ReviewSubmissionFailed ||
		!outcome.Stale ||
		outcome.PublicErrorCode != "pull_request_head_changed" ||
		!strings.Contains(outcome.InternalError, request.HeadSHA) ||
		outcome.ExternalURL != "https://github.com/scylladb/gocql/pull/42" {
		t.Fatalf("stale outcome = %#v", outcome)
	}
}

func TestSubmissionWorkerTreatsTypedHeadReadFailureAsDefiniteFailure(t *testing.T) {
	request := workerSubmitRequest()
	queue := newWorkerQueue(t, request)
	readErr := errors.New("read unavailable")
	worker := &SubmissionWorker{
		Queue: queue,
		Submitter: &workerSubmitterFake{err: &SubmitStageError{
			Stage:        SubmitStageVerifyHead,
			FindingIndex: -1,
			Err:          readErr,
		}},
	}

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = (%v, %v), want (true, nil)", processed, err)
	}
	outcomes := queue.finishedOutcomes()
	if len(outcomes) != 1 {
		t.Fatalf("finish outcomes = %d, want 1", len(outcomes))
	}
	outcome := outcomes[0]
	if outcome.Status != eventing.ReviewSubmissionFailed ||
		outcome.Stale ||
		outcome.PublicErrorCode != "github_submission_failed" ||
		outcome.ExternalURL != "" {
		t.Fatalf("definite failure outcome = %#v", outcome)
	}
}

func TestSubmissionWorkerMarksLeaseRenewalFailureUnknown(t *testing.T) {
	request := workerSubmitRequest()
	queue := newWorkerQueue(t, request)
	queue.renewErr = errors.New("database temporarily unavailable")
	submitter := &workerSubmitterFake{waitForCancel: true}
	worker := &SubmissionWorker{
		Queue:         queue,
		Submitter:     submitter,
		LeaseDuration: 3 * time.Second,
	}

	processed, err := worker.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessOne() processed = false, want true")
	}
	outcomes := queue.finishedOutcomes()
	if len(outcomes) != 1 {
		t.Fatalf("finish outcomes = %d, want 1", len(outcomes))
	}
	outcome := outcomes[0]
	if outcome.Status != eventing.ReviewSubmissionUnknown ||
		outcome.PublicErrorCode != "worker_outcome_unknown" ||
		!strings.Contains(outcome.InternalError, queue.renewErr.Error()) ||
		outcome.ExternalReviewID != "" ||
		outcome.ExternalURL != "https://github.com/scylladb/gocql/pull/42" {
		t.Fatalf("lease renewal outcome = %#v", outcome)
	}
}

func TestSubmissionWorkerFailsInvalidStoredRequestWithoutGitHubCall(t *testing.T) {
	tests := []struct {
		name   string
		raw    json.RawMessage
		marker string
	}{
		{
			name: "unknown JSON field",
			raw: append(
				mustMarshalWorkerRequest(t, workerSubmitRequest())[:len(
					mustMarshalWorkerRequest(t, workerSubmitRequest()),
				)-1],
				[]byte(`,"unexpected":true}`)...,
			),
			marker: workerSubmitRequest().Marker,
		},
		{
			name:   "marker mismatch",
			raw:    mustMarshalWorkerRequest(t, workerSubmitRequest()),
			marker: "<!-- different durable marker -->",
		},
		{
			name:   "trailing JSON",
			raw:    append(mustMarshalWorkerRequest(t, workerSubmitRequest()), []byte(` {}`)...),
			marker: workerSubmitRequest().Marker,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queue := newWorkerQueue(t, workerSubmitRequest())
			queue.submission.Request = append(json.RawMessage(nil), test.raw...)
			queue.submission.Marker = test.marker
			submitter := &workerSubmitterFake{}
			worker := &SubmissionWorker{Queue: queue, Submitter: submitter}

			processed, err := worker.ProcessOne(context.Background())
			if err != nil {
				t.Fatalf("ProcessOne() error = %v", err)
			}
			if !processed {
				t.Fatal("ProcessOne() processed = false, want true")
			}
			if len(submitter.requests) != 0 {
				t.Fatalf("invalid stored request reached GitHub: %#v", submitter.requests)
			}
			outcomes := queue.finishedOutcomes()
			if len(outcomes) != 1 {
				t.Fatalf("finish outcomes = %d, want 1", len(outcomes))
			}
			outcome := outcomes[0]
			if outcome.Status != eventing.ReviewSubmissionFailed ||
				outcome.PublicErrorCode != "invalid_submission" ||
				outcome.InternalError == "" ||
				outcome.ExternalReviewID != "" ||
				outcome.ExternalURL != "" {
				t.Fatalf("invalid stored request outcome = %#v", outcome)
			}
		})
	}
}

func workerSubmitRequest() SubmitRequest {
	return SubmitRequest{
		Owner:      "scylladb",
		Repo:       "gocql",
		PullNumber: 42,
		HeadSHA:    strings.Repeat("b", 40),
		Summary:    "One actionable finding.",
		Marker:     "<!-- picoclaw-review:" + serviceTestCaseID + ":v14 -->",
		Findings: []SubmitFinding{{
			ID:      serviceTestFindingID,
			Title:   "Queued item can be lost",
			File:    "pkg/queue/worker.go",
			Message: "Restore the item before returning.",
		}},
	}
}

func mustMarshalWorkerRequest(t *testing.T, request SubmitRequest) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal(submit request) error = %v", err)
	}
	return raw
}

func newWorkerQueue(t *testing.T, request SubmitRequest) *workerQueueFake {
	t.Helper()
	return &workerQueueFake{
		claimable: true,
		submission: eventing.ReviewSubmission{
			ID:           serviceTestSubmissionID,
			CaseID:       serviceTestCaseID,
			DraftVersion: 14,
			Marker:       request.Marker,
			Status:       eventing.ReviewSubmissionPending,
			Request:      mustMarshalWorkerRequest(t, request),
			CreatedAt:    serviceTestTime,
			UpdatedAt:    serviceTestTime,
		},
	}
}

type workerQueueFake struct {
	mu sync.Mutex

	claimable  bool
	submission eventing.ReviewSubmission
	labels     []string
	limits     []int
	leases     []time.Duration
	renewals   int
	renewErr   error
	outcomes   []eventing.ReviewSubmissionOutcome
}

func (queue *workerQueueFake) GetReviewSubmission(
	_ context.Context,
	id string,
) (eventing.ReviewSubmission, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if id != queue.submission.ID {
		return eventing.ReviewSubmission{}, eventing.ErrNotFound
	}
	return queue.submission, nil
}

func (queue *workerQueueFake) ClaimReviewSubmissions(
	_ context.Context,
	label string,
	limit int,
	lease time.Duration,
) ([]eventing.ReviewSubmission, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.labels = append(queue.labels, label)
	queue.limits = append(queue.limits, limit)
	queue.leases = append(queue.leases, lease)
	if !queue.claimable {
		return nil, nil
	}
	queue.claimable = false
	queue.submission.ClaimFrom = eventing.ReviewSubmissionPending
	queue.submission.Status = eventing.ReviewSubmissionClaimed
	queue.submission.LeaseToken = "lease-token-1"
	return []eventing.ReviewSubmission{queue.submission}, nil
}

func (queue *workerQueueFake) RenewReviewSubmissionLease(
	_ context.Context,
	id, leaseToken string,
	_ time.Duration,
) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if id != queue.submission.ID || leaseToken != queue.submission.LeaseToken {
		return eventing.ErrReviewConflict
	}
	queue.renewals++
	return queue.renewErr
}

func (queue *workerQueueFake) FinishReviewSubmission(
	_ context.Context,
	outcome eventing.ReviewSubmissionOutcome,
) (eventing.ReviewCaseDetail, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if outcome.SubmissionID != queue.submission.ID ||
		outcome.LeaseToken != queue.submission.LeaseToken {
		return eventing.ReviewCaseDetail{}, eventing.ErrReviewConflict
	}
	queue.outcomes = append(queue.outcomes, outcome)
	queue.submission.Status = outcome.Status
	queue.submission.PublicErrorCode = outcome.PublicErrorCode
	queue.submission.InternalError = outcome.InternalError
	queue.submission.ExternalReviewID = outcome.ExternalReviewID
	queue.submission.ExternalURL = outcome.ExternalURL
	return eventing.ReviewCaseDetail{
		Case: eventing.ReviewCase{ID: serviceTestCaseID},
		Submission: &eventing.ReviewSubmission{
			ID:     serviceTestSubmissionID,
			Status: outcome.Status,
		},
	}, nil
}

func (queue *workerQueueFake) claimLabels() []string {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return append([]string(nil), queue.labels...)
}

func (queue *workerQueueFake) claimLimits() []int {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return append([]int(nil), queue.limits...)
}

func (queue *workerQueueFake) claimLeases() []time.Duration {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return append([]time.Duration(nil), queue.leases...)
}

func (queue *workerQueueFake) finishedOutcomes() []eventing.ReviewSubmissionOutcome {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return append([]eventing.ReviewSubmissionOutcome(nil), queue.outcomes...)
}

type workerSubmitterFake struct {
	mu sync.Mutex

	requests      []SubmitRequest
	result        SubmitResult
	err           error
	waitForCancel bool
}

func (submitter *workerSubmitterFake) Submit(
	ctx context.Context,
	request SubmitRequest,
) (SubmitResult, error) {
	submitter.mu.Lock()
	submitter.requests = append(submitter.requests, request)
	waitForCancel := submitter.waitForCancel
	result := submitter.result
	err := submitter.err
	submitter.mu.Unlock()
	if waitForCancel {
		<-ctx.Done()
		return SubmitResult{}, ctx.Err()
	}
	return result, err
}
