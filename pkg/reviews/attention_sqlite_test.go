//go:build !mipsle && !netbsd && !(freebsd && arm)

package reviews

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

func TestAttentionLauncherRealSQLiteAndFileRunStoreRecoverExactDecision(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "eventing.sqlite")
	workspace := filepath.Join(root, "workspace")
	now := time.Date(2026, time.August, 4, 15, 0, 0, 0, time.UTC)
	clock := eventing.WithClock(func() time.Time { return now })

	store, err := eventing.Open(ctx, databasePath, clock)
	if err != nil {
		t.Fatalf("Open(event store): %v", err)
	}
	reviewCase := captureAttentionSQLiteCase(t, ctx, store)
	agent := &attentionTestAgent{taskByAgent: map[string]bool{"reviewer": false}}
	policy := attentionTestPolicy("sqlite-policy-generation", []workflows.GateSpec{{
		ID: "isolated", Kind: workflows.GateAIIsolatedContext,
		AgentID: "reviewer", Criteria: "ask when evidence is incomplete",
		Title: "Complete the evidence",
	}})
	runStore := workflows.NewFileRunStore(workspace)
	service := newAttentionTestService(t, store, nil, nil)
	launcher := newAttentionTestLauncher(
		t,
		service,
		&workflows.Executor{WorkspaceDir: workspace, Store: runStore, Agents: agent},
		&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{policy}},
	)
	request := AttentionLaunchRequest{
		CaseID: reviewCase.ID, ExpectedCaseVersion: reviewCase.Version,
		DecisionPoint: "review.submitted",
	}

	started, err := launcher.Launch(ctx, request)
	if err != nil || started.RunID == "" || started.Existing ||
		started.Status != workflows.RunStatusSucceeded {
		t.Fatalf("first Launch() = (%#v, %v)", started, err)
	}
	key := eventing.ReviewDecisionKey{
		CaseID: reviewCase.ID, CaseVersion: reviewCase.Version,
		DecisionPoint: request.DecisionPoint, PolicyRevision: started.PolicyRevision,
	}
	link, err := store.GetReviewDecisionRun(ctx, key)
	if err != nil || link.RunID != started.RunID {
		t.Fatalf("GetReviewDecisionRun() = (%#v, %v)", link, err)
	}
	persisted, err := runStore.GetRun(ctx, started.RunID)
	if err != nil || persisted.Status != workflows.RunStatusSucceeded ||
		!workflows.IsPrivateWorkflowRun(persisted) {
		t.Fatalf("GetRun() = (%#v, %v)", persisted, err)
	}
	requests, captures := agent.observations()
	if len(requests) != 1 || len(captures) != 0 {
		t.Fatalf("first execution requests=%d captures=%d", len(requests), len(captures))
	}
	if closeErr := store.Close(); closeErr != nil {
		t.Fatalf("Close(first event store): %v", closeErr)
	}

	reopened, err := eventing.Open(ctx, databasePath, clock)
	if err != nil {
		t.Fatalf("reopen event store: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := reopened.Close(); closeErr != nil {
			t.Errorf("Close(reopened event store): %v", closeErr)
		}
	})
	restartedService := newAttentionTestService(t, reopened, nil, nil)
	restarted := newAttentionTestLauncher(
		t,
		restartedService,
		&workflows.Executor{
			WorkspaceDir: workspace,
			Store:        workflows.NewFileRunStore(workspace),
			Agents:       agent,
		},
		&attentionTestPolicySource{snapshots: []AttentionPolicySnapshot{policy}},
	)

	recovered, err := restarted.Launch(ctx, request)
	if err != nil || !recovered.Existing || recovered.RunID != started.RunID ||
		recovered.Status != workflows.RunStatusSucceeded ||
		recovered.PolicyRevision != started.PolicyRevision {
		t.Fatalf("restarted Launch() = (%#v, %v), first = %#v", recovered, err, started)
	}
	reopenedLink, err := reopened.GetReviewDecisionRun(ctx, key)
	if err != nil || reopenedLink != link {
		t.Fatalf("reopened decision link = (%#v, %v), want %#v", reopenedLink, err, link)
	}
	requests, captures = agent.observations()
	if len(requests) != 1 || len(captures) != 0 {
		t.Fatalf("restart re-executed gate: requests=%d captures=%d", len(requests), len(captures))
	}
}

func captureAttentionSQLiteCase(
	t *testing.T,
	ctx context.Context,
	store *eventing.Store,
) eventing.ReviewCase {
	t.Helper()
	inserted, err := store.Insert(ctx, eventing.Envelope{
		Source:    "github",
		Connector: "github-primary",
		Type:      "pull_request_review.submitted",
		DedupeKey: "delivery-review-attention",
		Payload:   json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Insert(event): %v", err)
	}
	claims, err := store.ClaimRouting(ctx, "review-router", 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimRouting() = (%d claims, %v), want one", len(claims), err)
	}
	dispatch, created, err := store.CreateRevisionedDispatchForRoutingClaim(
		ctx,
		inserted.Event.Envelope.ID,
		claims[0].Routing.LeaseToken,
		"workflows/github-pr-review.yml",
		strings.Repeat("c", 64),
	)
	if err != nil || !created {
		t.Fatalf("CreateRevisionedDispatchForRoutingClaim() = (created=%v, %v)", created, err)
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
			Summary:       "One actionable finding.",
			Findings: []eventing.ReviewFindingDraft{{
				Severity: eventing.ReviewSeverityHigh,
				Title:    "Queued item can be lost",
				File:     "pkg/queue/worker.go",
				Message:  "Restore the item before returning.",
			}},
		},
	})
	if err != nil || !created {
		t.Fatalf("CaptureReview() = (created=%v, %v)", created, err)
	}
	return reviewCase
}
