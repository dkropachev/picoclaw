//go:build !mipsle && !netbsd && !(freebsd && arm)

package reviews

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestSubmissionWorkerNeverResubmitsExpiredClaimAfterCrash(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.July, 30, 14, 0, 0, 0, time.UTC)
	store, err := eventing.Open(ctx, ":memory:", eventing.WithClock(func() time.Time {
		return now
	}))
	if err != nil {
		t.Fatalf("Open(event store) error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(event store) error = %v", err)
		}
	})

	inserted, err := store.Insert(ctx, eventing.Envelope{
		Source:    "github",
		Connector: "github-primary",
		Type:      "pull_request.review_requested",
		DedupeKey: "delivery-expired-review-claim",
		Payload:   json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Insert(event) error = %v", err)
	}
	routing, err := store.ClaimRouting(ctx, "router", 1, time.Minute)
	if err != nil {
		t.Fatalf("ClaimRouting() error = %v", err)
	}
	if len(routing) != 1 {
		t.Fatalf("ClaimRouting() count = %d, want 1", len(routing))
	}
	dispatch, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		ctx,
		inserted.Event.Envelope.ID,
		routing[0].Routing.LeaseToken,
		"workflows/github-pr-review.yml",
		"revision-crash-safety",
	)
	if err != nil {
		t.Fatalf("CreateRevisionedDispatchForRoutingClaim() error = %v", err)
	}
	if !created {
		t.Fatal("CreateRevisionedDispatchForRoutingClaim() created = false")
	}
	reviewCase, created, err := store.CaptureReview(ctx, eventing.ReviewCaptureInput{
		EventID:          inserted.Event.Envelope.ID,
		DispatchID:       dispatch.ID,
		RunID:            dispatch.RunID,
		WorkflowRef:      dispatch.WorkflowRef,
		WorkflowRevision: dispatch.WorkflowRevision,
		Connector:        inserted.Event.Envelope.Connector,
		Repository:       "scylladb/gocql",
		PullNumber:       42,
		PullURL:          "https://github.com/scylladb/gocql/pull/42",
		BaseSHA:          strings.Repeat("a", 40),
		HeadSHA:          strings.Repeat("b", 40),
		Draft: eventing.ReviewDraft{
			SchemaVersion: eventing.ReviewDraftSchemaVersion,
			Summary:       "One actionable issue.",
			Findings: []eventing.ReviewFindingDraft{{
				Severity: eventing.ReviewSeverityHigh,
				Title:    "Queued item can be lost",
				File:     "pkg/queue/worker.go",
				Message:  "Restore the item before returning.",
			}},
		},
	})
	if err != nil {
		t.Fatalf("CaptureReview() error = %v", err)
	}
	if !created {
		t.Fatal("CaptureReview() created = false")
	}

	marker := reviewSubmissionMarker(reviewCase.ID, reviewCase.Version)
	request := SubmitRequest{
		Owner:      "scylladb",
		Repo:       "gocql",
		PullNumber: 42,
		HeadSHA:    strings.Repeat("b", 40),
		Summary:    reviewCase.Summary,
		Marker:     marker,
		Findings: []SubmitFinding{{
			Title:   "Queued item can be lost",
			File:    "pkg/queue/worker.go",
			Message: "Restore the item before returning.",
		}},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal(submission request) error = %v", err)
	}
	detail, err := store.CreateReviewSubmission(ctx, eventing.ReviewSubmissionDraft{
		CaseID:          reviewCase.ID,
		ExpectedVersion: reviewCase.Version,
		Marker:          marker,
		Request:         raw,
	})
	if err != nil {
		t.Fatalf("CreateReviewSubmission() error = %v", err)
	}
	if detail.Submission == nil {
		t.Fatal("CreateReviewSubmission() omitted submission")
	}
	submissionID := detail.Submission.ID

	claimed, err := store.ClaimReviewSubmissions(ctx, "worker-before-crash", 1, time.Minute)
	if err != nil {
		t.Fatalf("ClaimReviewSubmissions() error = %v", err)
	}
	if len(claimed) != 1 || claimed[0].ID != submissionID {
		t.Fatalf("initial claim = %#v, want submission %s", claimed, submissionID)
	}
	initialLease := claimed[0].LeaseToken

	// The process disappears after the GitHub call may have been sent but
	// before it can durably record an outcome.
	now = now.Add(2 * time.Minute)
	submitter := &workerSubmitterFake{}
	worker := &SubmissionWorker{
		Queue:         store,
		Submitter:     submitter,
		WorkerLabel:   "worker-after-restart",
		LeaseDuration: time.Minute,
	}
	processed, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("ProcessOne(after restart) error = %v", err)
	}
	if processed {
		t.Fatal("ProcessOne(after restart) processed expired ambiguous claim")
	}
	if len(submitter.requests) != 0 {
		t.Fatalf("expired ambiguous claim reached GitHub: %#v", submitter.requests)
	}

	stored, err := store.GetReviewSubmission(ctx, submissionID)
	if err != nil {
		t.Fatalf("GetReviewSubmission() error = %v", err)
	}
	if stored.Status != eventing.ReviewSubmissionUnknown ||
		stored.PublicErrorCode != "worker_outcome_unknown" ||
		stored.LeaseToken != "" ||
		stored.LeaseUntil != nil ||
		stored.Attempts != 1 {
		t.Fatalf("terminalized expired submission = %#v", stored)
	}
	if stored.InternalError == "" {
		t.Fatal("terminalized expired submission omitted internal crash diagnostic")
	}
	if initialLease == "" {
		t.Fatal("initial claim lease was empty")
	}
	latest, err := store.GetReviewCase(ctx, reviewCase.ID)
	if err != nil {
		t.Fatalf("GetReviewCase() error = %v", err)
	}
	if latest.Case.Status != eventing.ReviewCaseSubmissionUnknown ||
		latest.Case.PublicErrorCode != "worker_outcome_unknown" ||
		latest.Case.ResolvedAt == nil {
		t.Fatalf("terminalized expired case = %#v", latest.Case)
	}

	for attempt := 0; attempt < 2; attempt++ {
		processed, err = worker.ProcessOne(ctx)
		if err != nil {
			t.Fatalf("repeat ProcessOne() error = %v", err)
		}
		if processed {
			t.Fatalf("repeat ProcessOne() %d reclaimed unknown submission", attempt+1)
		}
	}
	if len(submitter.requests) != 0 {
		t.Fatalf("terminal unknown submission reached GitHub: %#v", submitter.requests)
	}
}
