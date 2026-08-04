//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestEventAutomationWiresReviewAttentionOnlyWithTrustedPolicySource(
	t *testing.T,
) {
	tests := []struct {
		name             string
		workflowsEnabled bool
		policies         reviews.AttentionPolicySource
		wantLauncher     bool
	}{
		{
			name:             "enabled with trusted source",
			workflowsEnabled: true,
			policies:         gatewayAttentionPolicySource{},
			wantLauncher:     true,
		},
		{
			name:             "enabled without source",
			workflowsEnabled: true,
		},
		{
			name:             "source remains inert without workflows",
			workflowsEnabled: false,
			policies:         gatewayAttentionPolicySource{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			cfg := eventAutomationTestConfig(
				workspace,
				filepath.Join(workspace, "eventing", "events.db"),
				true,
				test.workflowsEnabled,
			)
			var executor *workflows.Executor
			if test.workflowsEnabled {
				executor = &workflows.Executor{WorkspaceDir: workspace}
			}
			service, err := newEventAutomationServiceWithReviews(
				context.Background(),
				cfg,
				executor,
				nil,
				nil,
				eventReviewRuntime{attentionPolicies: test.policies},
			)
			if err != nil {
				t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
			}
			if service == nil {
				t.Fatal("newEventAutomationServiceWithReviews() service is nil")
			}
			t.Cleanup(func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if closeErr := service.Close(closeCtx); closeErr != nil {
					t.Errorf("Close() error = %v", closeErr)
				}
			})
			if got := service.reviewAttention != nil; got != test.wantLauncher {
				t.Fatalf("review attention launcher configured = %t, want %t", got, test.wantLauncher)
			}
			if test.wantLauncher && executor.Store == nil {
				t.Fatal("review attention launcher was built before assigning the durable run store")
			}
		})
	}
}

type gatewayAttentionPolicySource struct{}

func (gatewayAttentionPolicySource) WithReviewAttentionPolicy(
	ctx context.Context,
	_ reviews.AttentionPolicySelector,
	consume reviews.AttentionPolicyUse,
) error {
	return consume(ctx, reviews.AttentionPolicySnapshot{})
}
