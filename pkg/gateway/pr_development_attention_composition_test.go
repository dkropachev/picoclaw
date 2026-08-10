//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prdevelopment"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestSetupEventAutomationComposesProductionPRAttentionLauncher(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "eventing", "events.db"),
		true,
		true,
	)
	cfg.Tools.MCP.Enabled = false
	messageBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(
		cfg,
		messageBus,
		&startupBlockedProvider{reason: "not used"},
	)
	service, err := setupEventAutomationService(context.Background(), cfg, agentLoop)
	if err != nil {
		messageBus.Close()
		agentLoop.Close()
		t.Fatalf("setupEventAutomationService() error = %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := service.Close(closeCtx); closeErr != nil {
			t.Errorf("event automation Close() error = %v", closeErr)
		}
		messageBus.Close()
		agentLoop.Close()
	})

	if service == nil || service.prDevelopmentAttention == nil {
		t.Fatal("workflow-enabled production service omitted PR-development attention launcher")
	}
	if service.prDevelopmentBridge == nil {
		t.Fatal("production service omitted PR-development attention chat bridge")
	}
	if service.prLocalCI == nil || service.prLocalCI.evidence == nil {
		t.Fatal("PR-development attention launcher has no local CI evidence runtime")
	}
	if service.reviewAttention == nil {
		t.Fatal("shared configured policy source stopped composing legacy review attention")
	}
}

func TestEventAutomationPRAttentionLauncherIsInertAndWorkflowGated(t *testing.T) {
	tests := []struct {
		name             string
		workflowsEnabled bool
		wantLauncher     bool
	}{
		{name: "enabled full composition", workflowsEnabled: true, wantLauncher: true},
		{name: "workflows disabled", workflowsEnabled: false},
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
			policies, err := reviews.NewConfigAttentionPolicySource(nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			var workspaceCalls atomic.Int32
			var runtimeCalls atomic.Int32
			runtime := eventReviewRuntime{
				attentionPolicies:              policies,
				prDevelopmentAttentionPolicies: policies,
				prDevelopmentAttentionWorkspaces: func() (
					prdevelopment.AttentionReviewWorkspace,
					error,
				) {
					workspaceCalls.Add(1)
					return nil, errors.New("must remain inert")
				},
				acquirePRDevelopmentAttentionRuntime: func(
					ctx context.Context,
					_ string,
				) (context.Context, session.SessionStore, func(), error) {
					runtimeCalls.Add(1)
					return ctx, nil, func() {}, errors.New("must remain inert")
				},
			}
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
				runtime,
			)
			if err != nil {
				t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
			}
			if service == nil {
				t.Fatal("ingress-enabled service is nil")
			}
			if got := service.prDevelopmentAttention != nil; got != test.wantLauncher {
				t.Fatalf("PR attention launcher configured = %t, want %t", got, test.wantLauncher)
			}
			if service.prDevelopmentBridge == nil {
				t.Fatal("PR attention projection bridge must remain available read-only")
			}
			if !test.workflowsEnabled && service.prLocalCI != nil {
				t.Fatal("workflow-disabled attention composition opened local CI evidence")
			}
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err = service.Close(closeCtx); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if workspaceCalls.Load() != 0 || runtimeCalls.Load() != 0 {
				t.Fatalf(
					"inert launcher acquired dependencies: Git=%d runtime=%d",
					workspaceCalls.Load(),
					runtimeCalls.Load(),
				)
			}
		})
	}
}

func TestEventAutomationPRAttentionWorkerUsesGenerationAndDrains(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "eventing", "events.db"),
		true,
		true,
	)
	policies, err := reviews.NewConfigAttentionPolicySource(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	type runtimeMarker struct{}
	entered := make(chan struct{})
	exited := make(chan struct{})
	runtime := eventReviewRuntime{
		attentionPolicies:              policies,
		prDevelopmentAttentionPolicies: policies,
		prDevelopmentAttentionWorkspaces: func() (
			prdevelopment.AttentionReviewWorkspace,
			error,
		) {
			return nil, errors.New("attention process seam must not read Git")
		},
		acquirePRDevelopmentAttentionRuntime: func(
			ctx context.Context,
			_ string,
		) (context.Context, session.SessionStore, func(), error) {
			return ctx, nil, func() {}, errors.New(
				"attention process seam must not acquire its inner runtime",
			)
		},
		prDevelopmentAttentionProcess: func(ctx context.Context) (bool, error) {
			if marker, _ := ctx.Value(runtimeMarker{}).(bool); !marker {
				return false, errors.New("attention worker has no runtime generation")
			}
			close(entered)
			<-ctx.Done()
			close(exited)
			return false, ctx.Err()
		},
	}
	acquire := func(ctx context.Context) (context.Context, func(), error) {
		return context.WithValue(ctx, runtimeMarker{}, true), func() {}, nil
	}
	service, err := newEventAutomationServiceWithReviews(
		context.Background(),
		cfg,
		&workflows.Executor{WorkspaceDir: workspace},
		nil,
		acquire,
		runtime,
	)
	if err != nil {
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("PR-development attention worker did not enter its runtime generation")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = service.Close(closeCtx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("Close() returned before the PR-development attention worker joined")
	}
}

func TestPRAttentionGitReaderUsesCallersSingleOwnerRuntimeLease(t *testing.T) {
	var acquisitions atomic.Int32
	var managerAccesses atomic.Int32
	var reads atomic.Int32
	var leaseActive atomic.Bool
	acquire := func(
		ctx context.Context,
		_ string,
	) (context.Context, session.SessionStore, func(), error) {
		if acquisitions.Add(1) != 1 || !leaseActive.CompareAndSwap(false, true) {
			return ctx, nil, func() {}, errors.New("nested runtime acquisition")
		}
		return ctx, nil, func() { leaseActive.Store(false) }, nil
	}
	factory := newPRDevelopmentAttentionWorkspaceFactoryWithResolver(
		func() (prdevelopment.AttentionReviewWorkspace, error) {
			if !leaseActive.Load() {
				return nil, errors.New("manager accessed without owner runtime lease")
			}
			managerAccesses.Add(1)
			return &gatewayLeaseObservedAttentionReader{
				leaseActive: &leaseActive,
				reads:       &reads,
			}, nil
		},
	)
	leaseCtx, _, release, err := acquire(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := factory()
	if err != nil {
		release()
		t.Fatalf("attention Git factory error = %v", err)
	}
	if _, leaked := reader.(*gatewayLeaseObservedAttentionReader); leaked {
		release()
		t.Fatal("attention Git factory exposed the underlying reader capability")
	}
	if _, err = reader.SnapshotPinnedLineReview(
		leaseCtx,
		gitworkspace.PinnedLineReviewRequest{},
	); err != nil {
		release()
		t.Fatalf("SnapshotPinnedLineReview() error = %v", err)
	}
	if acquisitions.Load() != 1 || managerAccesses.Load() != 1 || reads.Load() != 1 ||
		!leaseActive.Load() {
		release()
		t.Fatalf(
			"leased read acquisitions=%d manager=%d reads=%d active=%t",
			acquisitions.Load(), managerAccesses.Load(), reads.Load(), leaseActive.Load(),
		)
	}
	release()
	if leaseActive.Load() {
		t.Fatal("owner runtime lease remained active after release")
	}
}

func TestEventAutomationPRAttentionIncompleteIsOptionalButInvalidFullAttemptFails(
	testingT *testing.T,
) {
	workspace := testingT.TempDir()
	policies, err := reviews.NewConfigAttentionPolicySource(nil, nil)
	if err != nil {
		testingT.Fatal(err)
	}
	newConfig := func(name string) *config.Config {
		root := filepath.Join(workspace, name)
		return eventAutomationTestConfig(
			root,
			filepath.Join(root, "eventing", "events.db"),
			true,
			true,
		)
	}

	incomplete, err := newEventAutomationServiceWithReviews(
		context.Background(),
		newConfig("incomplete"),
		&workflows.Executor{WorkspaceDir: filepath.Join(workspace, "incomplete")},
		nil,
		nil,
		eventReviewRuntime{
			attentionPolicies:              policies,
			prDevelopmentAttentionPolicies: policies,
		},
	)
	if err != nil {
		testingT.Fatalf("incomplete optional composition error = %v", err)
	}
	if incomplete == nil || incomplete.prDevelopmentAttention != nil {
		testingT.Fatalf("incomplete optional composition = %#v", incomplete)
	}
	if closeErr := incomplete.Close(context.Background()); closeErr != nil {
		testingT.Fatalf("incomplete Close() error = %v", closeErr)
	}

	var invalid *gatewayInvalidPRAttentionPolicySource
	invalidService, err := newEventAutomationServiceWithReviews(
		context.Background(),
		newConfig("invalid"),
		&workflows.Executor{WorkspaceDir: filepath.Join(workspace, "invalid")},
		nil,
		nil,
		eventReviewRuntime{
			attentionPolicies:              policies,
			prDevelopmentAttentionPolicies: invalid,
			prDevelopmentAttentionWorkspaces: func() (
				prdevelopment.AttentionReviewWorkspace,
				error,
			) {
				return nil, errors.New("not called")
			},
			acquirePRDevelopmentAttentionRuntime: func(
				ctx context.Context,
				_ string,
			) (context.Context, session.SessionStore, func(), error) {
				return ctx, nil, func() {}, errors.New("not called")
			},
		},
	)
	if invalidService != nil {
		testingT.Fatalf("invalid full composition service = %#v, want nil", invalidService)
	}
	if err == nil || !strings.Contains(err.Error(), "attention policy source") {
		testingT.Fatalf("invalid full composition error = %v", err)
	}
}

type gatewayInvalidPRAttentionPolicySource struct{}

func (*gatewayInvalidPRAttentionPolicySource) WithAttentionPolicy(
	context.Context,
	sharedattention.PolicySelector,
	sharedattention.PolicyUse,
) error {
	return errors.New("not called")
}

type gatewayLeaseObservedAttentionReader struct {
	leaseActive *atomic.Bool
	reads       *atomic.Int32
}

func (reader *gatewayLeaseObservedAttentionReader) SnapshotPinnedLineReview(
	context.Context,
	gitworkspace.PinnedLineReviewRequest,
) (gitworkspace.PinnedLineReviewSnapshot, error) {
	if reader == nil || reader.leaseActive == nil || !reader.leaseActive.Load() {
		return gitworkspace.PinnedLineReviewSnapshot{}, errors.New(
			"attention Git read escaped owner runtime lease",
		)
	}
	reader.reads.Add(1)
	return gitworkspace.PinnedLineReviewSnapshot{}, nil
}
