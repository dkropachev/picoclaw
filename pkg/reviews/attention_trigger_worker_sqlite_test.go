//go:build !mipsle && !netbsd && !(freebsd && arm)

package reviews

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestAttentionTriggerWorkerRenewsSubsecondSQLiteLeaseDuringBlockedPolicyCapture(
	t *testing.T,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, submitted := newSQLiteAttentionTriggerFixture(t, ctx)
	service, err := NewService(ServiceConfig{Store: store})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	const (
		lease = 600 * time.Millisecond
		block = 1250 * time.Millisecond
	)
	policies := &blockingAttentionPolicySource{delay: block}
	workspace := t.TempDir()
	launcher, err := NewAttentionLauncher(AttentionLauncherConfig{
		Service: service,
		Executor: &workflows.Executor{
			WorkspaceDir: workspace,
			Store:        workflows.NewFileRunStore(workspace),
		},
		Policies: policies,
	})
	if err != nil {
		t.Fatalf("NewAttentionLauncher() error = %v", err)
	}
	worker := &AttentionTriggerWorker{
		Queue:         store,
		Launcher:      launcher,
		WorkerLabel:   "sqlite-subsecond-renewal",
		LeaseDuration: lease,
	}

	started := time.Now()
	processed, err := worker.ProcessOne(ctx)
	elapsed := time.Since(started)
	if !processed || err != nil {
		t.Fatalf("ProcessOne() = (%v, %v)", processed, err)
	}
	if elapsed < block {
		t.Fatalf("policy capture elapsed %v, want at least %v", elapsed, block)
	}
	trigger, err := store.GetReviewAttentionTrigger(ctx, submitted.Submission.ID)
	if err != nil {
		t.Fatalf("GetReviewAttentionTrigger() error = %v", err)
	}
	if trigger.Status != eventing.ReviewAttentionNoop || trigger.Attempts != 1 ||
		trigger.PolicyRevision == "" || len(trigger.PinnedPolicy) == 0 ||
		trigger.RunID != "" || trigger.LeaseToken != "" || trigger.LeaseUntil != nil ||
		policies.calls.Load() != 1 {
		t.Fatalf(
			"renewed terminal trigger=%#v policy_calls=%d",
			trigger,
			policies.calls.Load(),
		)
	}
}

func newSQLiteAttentionTriggerFixture(
	t *testing.T,
	ctx context.Context,
) (*eventing.Store, eventing.ReviewCaseDetail) {
	t.Helper()
	store, err := eventing.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open(event store) error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("Close(event store) error = %v", closeErr)
		}
	})
	inserted, err := store.Insert(ctx, eventing.Envelope{
		Source:    "github",
		Connector: "github-primary",
		Type:      "pull_request.review_requested",
		DedupeKey: "attention-worker-subsecond-renewal",
		Payload:   json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Insert(event) error = %v", err)
	}
	routing, err := store.ClaimRouting(ctx, "router", 1, time.Minute)
	if err != nil || len(routing) != 1 {
		t.Fatalf("ClaimRouting() = (%#v, %v)", routing, err)
	}
	dispatch, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		ctx,
		inserted.Event.Envelope.ID,
		routing[0].Routing.LeaseToken,
		"workflows/github-pr-review.yml",
		"revision-attention-worker-renewal",
	)
	if err != nil || !created {
		t.Fatalf("CreateRevisionedDispatchForRoutingClaim() = (%#v, %v, %v)", dispatch, created, err)
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
	if err != nil || !created {
		t.Fatalf("CaptureReview() = (%#v, %v, %v)", reviewCase, created, err)
	}
	marker := reviewSubmissionMarker(reviewCase.ID, reviewCase.Version)
	raw, err := json.Marshal(SubmitRequest{
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
	})
	if err != nil {
		t.Fatalf("Marshal(submission) error = %v", err)
	}
	submitting, err := store.CreateReviewSubmission(ctx, eventing.ReviewSubmissionDraft{
		CaseID:          reviewCase.ID,
		ExpectedVersion: reviewCase.Version,
		Marker:          marker,
		Request:         raw,
	})
	if err != nil || submitting.Submission == nil {
		t.Fatalf("CreateReviewSubmission() = (%#v, %v)", submitting, err)
	}
	claimed, err := store.ClaimReviewSubmissions(ctx, "review-submitter", 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimReviewSubmissions() = (%#v, %v)", claimed, err)
	}
	submitted, err := store.FinishReviewSubmission(ctx, eventing.ReviewSubmissionOutcome{
		SubmissionID:     claimed[0].ID,
		LeaseToken:       claimed[0].LeaseToken,
		Status:           eventing.ReviewSubmissionSubmitted,
		ExternalReviewID: "review-subsecond-renewal",
		ExternalURL:      "https://github.com/scylladb/gocql/pull/42#pullrequestreview-1",
	})
	if err != nil || submitted.Submission == nil {
		t.Fatalf("FinishReviewSubmission() = (%#v, %v)", submitted, err)
	}
	return store, submitted
}

type blockingAttentionPolicySource struct {
	delay time.Duration
	calls atomic.Int32
}

func (source *blockingAttentionPolicySource) WithReviewAttentionPolicy(
	ctx context.Context,
	_ AttentionPolicySelector,
	use AttentionPolicyUse,
) error {
	source.calls.Add(1)
	timer := time.NewTimer(source.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	return use(ctx, AttentionPolicySnapshot{Revision: "subsecond-renewal-policy"})
}
