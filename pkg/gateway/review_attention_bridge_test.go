//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	eventoperator "github.com/sipeed/picoclaw/pkg/eventing/operator"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestEventAutomationProjectsQueuedReviewAttentionWithoutWorkflows(
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
	if service == nil || service.reviewBridge == nil {
		t.Fatal("workflow-disabled service omitted the read-only attention bridge")
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if closeErr := service.Close(closeCtx); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	})

	trigger, err := service.store.GetReviewAttentionTrigger(
		context.Background(),
		submissionID,
	)
	if err != nil {
		t.Fatalf("GetReviewAttentionTrigger() error = %v", err)
	}
	controller := eventoperator.NewController()
	generation, err := controller.Activate(service.operatorBackend)
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	t.Cleanup(func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = controller.Deactivate(drainCtx, generation)
	})

	request := httptest.NewRequest(
		http.MethodGet,
		eventoperator.RoutePrefix+"reviews/"+trigger.CaseID+"/attention",
		nil,
	)
	response := httptest.NewRecorder()
	controller.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET attention status = %d, body=%s", response.Code, response.Body.String())
	}
	var projected struct {
		CaseVersion int64  `json:"case_version"`
		Status      string `json:"status"`
		CanRespond  bool   `json:"can_respond"`
		Turns       []any  `json:"turns"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &projected); err != nil {
		t.Fatalf("decode attention projection: %v", err)
	}
	if projected.CaseVersion != trigger.CaseVersion ||
		projected.Status != "queued" ||
		projected.CanRespond ||
		len(projected.Turns) != 0 {
		t.Fatalf("attention projection = %#v, trigger=%#v", projected, trigger)
	}
	for _, private := range []string{
		submissionID,
		trigger.PolicyRevision,
		trigger.RunID,
		string(trigger.PinnedPolicy),
		eventing.ReviewAttentionDecisionSubmitted,
	} {
		if private != "" && strings.Contains(response.Body.String(), private) {
			t.Fatalf("private attention value leaked: %q", private)
		}
	}
}

func TestEventAutomationWorkflowDisabledBridgeIgnoresInjectedExecutor(
	t *testing.T,
) {
	ctx := context.Background()
	workspace := t.TempDir()
	databasePath := filepath.Join(workspace, "eventing", "events.db")
	submissionID := seedSubmittedReviewAttentionTrigger(t, databasePath)
	seedStore, err := eventing.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("eventing.Open(seed attention) error = %v", err)
	}
	reviewService, err := reviews.NewService(reviews.ServiceConfig{Store: seedStore})
	if err != nil {
		_ = seedStore.Close()
		t.Fatalf("reviews.NewService() error = %v", err)
	}
	policies, err := reviews.NewConfigAttentionPolicySource(
		map[string][]workflows.GateSpec{
			eventing.ReviewAttentionDecisionSubmitted: {
				{
					ID: "operator", Kind: workflows.GateDeterministic, When: "true",
					Title: "Confirm operator choice", Questions: []any{"Which choice?"},
				},
			},
		},
		nil,
	)
	if err != nil {
		_ = seedStore.Close()
		t.Fatalf("NewConfigAttentionPolicySource() error = %v", err)
	}
	runStore := workflows.NewFileRunStore(workspace)
	injectedExecutor := &workflows.Executor{WorkspaceDir: workspace, Store: runStore}
	launcher, err := reviews.NewAttentionLauncher(reviews.AttentionLauncherConfig{
		Service: reviewService, Executor: injectedExecutor, Policies: policies,
	})
	if err != nil {
		_ = seedStore.Close()
		t.Fatalf("NewAttentionLauncher() error = %v", err)
	}
	processed, err := (&reviews.AttentionTriggerWorker{
		Queue: seedStore, Launcher: launcher, WorkerLabel: "seed-disabled-bridge",
		LeaseDuration: time.Minute,
	}).ProcessOne(ctx)
	if err != nil || !processed {
		_ = seedStore.Close()
		t.Fatalf("ProcessOne() = (%t, %v), want delivered waiting run", processed, err)
	}
	trigger, err := seedStore.GetReviewAttentionTrigger(ctx, submissionID)
	if err != nil || trigger.Status != eventing.ReviewAttentionDelivered || trigger.RunID == "" {
		_ = seedStore.Close()
		t.Fatalf("delivered trigger = (%#v, %v)", trigger, err)
	}
	if closeErr := seedStore.Close(); closeErr != nil {
		t.Fatalf("seed store Close() error = %v", closeErr)
	}

	cfg := eventAutomationTestConfig(workspace, databasePath, true, false)
	service, err := newEventAutomationServiceWithReviews(
		ctx,
		cfg,
		injectedExecutor,
		nil,
		nil,
		eventReviewRuntime{},
	)
	if err != nil {
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
	}
	if service == nil || service.reviewBridge == nil {
		t.Fatal("workflow-disabled service omitted attention bridge")
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if closeErr := service.Close(closeCtx); closeErr != nil {
			t.Errorf("Close() error = %v", closeErr)
		}
	})
	view, err := service.reviewBridge.Project(ctx, trigger.CaseID)
	if err != nil || view.Status != reviews.AttentionStatusWaiting ||
		view.CanRespond || len(view.Turns) != 1 || view.Turns[0].ResponseToken != "" {
		t.Fatalf("workflow-disabled waiting projection = (%#v, %v)", view, err)
	}
}
