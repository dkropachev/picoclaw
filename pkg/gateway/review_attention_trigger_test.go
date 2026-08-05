//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestEventAutomationReviewAttentionTriggerWorkerUsesRuntimeGenerationAndDrains(
	t *testing.T,
) {
	workspace := t.TempDir()
	databasePath := filepath.Join(workspace, "eventing", "events.db")
	submissionID := seedSubmittedReviewAttentionTrigger(t, databasePath)
	cfg := eventAutomationTestConfig(workspace, databasePath, true, true)

	allowRuntime := make(chan struct{})
	acquireEntered := make(chan struct{}, 1)
	acquire := func(ctx context.Context) (context.Context, func(), error) {
		select {
		case acquireEntered <- struct{}{}:
		default:
		}
		select {
		case <-allowRuntime:
			return ctx, func() {}, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	service, err := newEventAutomationServiceWithReviews(
		context.Background(),
		cfg,
		&workflows.Executor{WorkspaceDir: workspace},
		nil,
		acquire,
		eventReviewRuntime{},
	)
	if err != nil {
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
	}
	if service == nil || service.reviewAttention == nil {
		t.Fatal("workflow-enabled service omitted review attention runtime")
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if closeErr := service.Close(closeCtx); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	}()

	select {
	case <-acquireEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("event workers did not request the runtime generation")
	}
	blocked, err := service.store.GetReviewAttentionTrigger(
		context.Background(),
		submissionID,
	)
	if err != nil {
		t.Fatalf("GetReviewAttentionTrigger(blocked) error = %v", err)
	}
	if blocked.Status != eventing.ReviewAttentionPending {
		t.Fatalf(
			"trigger before runtime acquisition = %q, want %q",
			blocked.Status,
			eventing.ReviewAttentionPending,
		)
	}

	close(allowRuntime)
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		trigger, getErr := service.store.GetReviewAttentionTrigger(
			context.Background(),
			submissionID,
		)
		if getErr != nil {
			t.Fatalf("GetReviewAttentionTrigger() error = %v", getErr)
		}
		if trigger.Status == eventing.ReviewAttentionNoop {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf(
				"generation-fenced trigger status = %q, want %q",
				trigger.Status,
				eventing.ReviewAttentionNoop,
			)
		case <-ticker.C:
		}
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = service.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-service.done:
	default:
		t.Fatal("Close() returned before the attention worker joined")
	}
}

func TestEventAutomationWithoutWorkflowsLeavesReviewAttentionTriggerPending(
	t *testing.T,
) {
	workspace := t.TempDir()
	databasePath := filepath.Join(workspace, "eventing", "events.db")
	submissionID := seedSubmittedReviewAttentionTrigger(t, databasePath)
	cfg := eventAutomationTestConfig(workspace, databasePath, true, false)

	service, err := newEventAutomationServiceWithReviews(
		context.Background(),
		cfg,
		nil,
		nil,
		nil,
		eventReviewRuntime{},
	)
	if err != nil {
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
	}
	if service == nil {
		t.Fatal("ingress-enabled service is nil")
	}
	if service.reviewAttention != nil {
		t.Fatal("workflow-disabled service configured review attention runtime")
	}
	if err = service.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	probe, err := eventing.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("eventing.Open(probe) error = %v", err)
	}
	defer func() { _ = probe.Close() }()
	trigger, err := probe.GetReviewAttentionTrigger(context.Background(), submissionID)
	if err != nil {
		t.Fatalf("GetReviewAttentionTrigger() error = %v", err)
	}
	if trigger.Status != eventing.ReviewAttentionPending {
		t.Fatalf(
			"workflow-disabled trigger status = %q, want %q",
			trigger.Status,
			eventing.ReviewAttentionPending,
		)
	}
}

func seedSubmittedReviewAttentionTrigger(t *testing.T, databasePath string) string {
	t.Helper()
	ctx := context.Background()
	store, err := eventing.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("eventing.Open(seed) error = %v", err)
	}
	defer func() { _ = store.Close() }()

	inserted, err := store.Insert(ctx, eventing.Envelope{
		Source:    "github",
		Connector: "github-primary",
		Type:      "pull_request.review_requested",
		DedupeKey: "gateway-review-attention",
		Payload:   json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Insert(seed event) error = %v", err)
	}
	routing, err := store.ClaimRouting(ctx, "seed-router", 1, time.Minute)
	if err != nil || len(routing) != 1 {
		t.Fatalf("ClaimRouting(seed) = (%d, %v), want one claim", len(routing), err)
	}
	dispatch, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		ctx,
		inserted.Event.Envelope.ID,
		routing[0].Routing.LeaseToken,
		"workflows/github-pr-review.yml",
		"gateway-attention-test-revision",
	)
	if err != nil || !created {
		t.Fatalf("CreateRevisionedDispatchForRoutingClaim(seed) = (%t, %v)", created, err)
	}
	reviewCase, created, err := store.CaptureReview(ctx, eventing.ReviewCaptureInput{
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
		Draft: eventing.ReviewDraft{
			SchemaVersion: eventing.ReviewDraftSchemaVersion,
			Summary:       "One finding.",
			Findings: []eventing.ReviewFindingDraft{{
				Severity: eventing.ReviewSeverityHigh,
				Title:    "Finding",
				File:     "pkg/review.go",
				Message:  "Fix the finding.",
			}},
		},
	})
	if err != nil || !created {
		t.Fatalf("CaptureReview(seed) = (%t, %v)", created, err)
	}
	detail, err := store.CreateReviewSubmission(ctx, eventing.ReviewSubmissionDraft{
		CaseID:          reviewCase.ID,
		ExpectedVersion: reviewCase.Version,
		Marker:          "gateway-attention-" + reviewCase.ID,
		Request:         json.RawMessage(`{}`),
	})
	if err != nil || detail.Submission == nil {
		t.Fatalf("CreateReviewSubmission(seed) = (%#v, %v)", detail.Submission, err)
	}
	claimed, err := store.ClaimReviewSubmissions(ctx, "seed-submitter", 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != detail.Submission.ID {
		t.Fatalf("ClaimReviewSubmissions(seed) = (%#v, %v)", claimed, err)
	}
	if _, err = store.FinishReviewSubmission(ctx, eventing.ReviewSubmissionOutcome{
		SubmissionID: claimed[0].ID,
		LeaseToken:   claimed[0].LeaseToken,
		Status:       eventing.ReviewSubmissionSubmitted,
	}); err != nil {
		t.Fatalf("FinishReviewSubmission(seed) error = %v", err)
	}
	trigger, err := store.GetReviewAttentionTrigger(ctx, claimed[0].ID)
	if err != nil || trigger.Status != eventing.ReviewAttentionPending {
		t.Fatalf("GetReviewAttentionTrigger(seed) = (%#v, %v)", trigger, err)
	}
	return claimed[0].ID
}
