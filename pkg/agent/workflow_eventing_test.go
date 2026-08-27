package agent

import (
	"context"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestNewEventWorkflowExecutorInitializesMCPWithinStartupBarrier(t *testing.T) {
	server := workflowDependencyMCPTestServer(t, []string{"startup_tool"})
	cfg := mcpTestConfig(t, "startup", server.URL)
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&mockProvider{},
		WithRuntimeStartupBarrier(),
	)
	t.Cleanup(func() {
		loop.ReleaseRuntimeStartupBarrier()
		loop.Close()
		messageBus.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	setupCtx, releaseStartup, err := loop.AcquireRuntimeStartupUse(ctx, cfg)
	if err != nil {
		t.Fatalf("AcquireRuntimeStartupUse() error = %v", err)
	}
	t.Cleanup(releaseStartup)
	if _, _, secondErr := loop.AcquireRuntimeStartupUse(
		context.Background(),
		cfg,
	); secondErr == nil {
		t.Fatal("second startup runtime owner was admitted concurrently")
	}
	executor, err := NewEventWorkflowExecutor(setupCtx, loop)
	if err != nil {
		t.Fatalf("NewEventWorkflowExecutor() error = %v", err)
	}
	if executor == nil {
		t.Fatal("NewEventWorkflowExecutor() returned nil executor")
	}

	toolName := mcp.CanonicalToolName("startup", "startup_tool")
	if !loop.GetRegistry().GetDefaultAgent().Tools.HasRegistered(toolName) {
		t.Fatalf("startup MCP tool %q was not registered", toolName)
	}
	readiness := loop.ResolveWorkflowDependency(
		setupCtx,
		workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindMCP,
			Name: "startup/startup_tool",
		},
	)
	if readiness != workflows.WorkflowDependencyReadinessReady {
		t.Fatalf("startup MCP dependency readiness = %q, want ready", readiness)
	}

	loop.runtimeGateMu.Lock()
	paused := loop.runtimeGatePaused
	active := loop.runtimeGateActive
	loop.runtimeGateMu.Unlock()
	if !paused || active != 1 {
		t.Fatalf(
			"startup barrier changed during executor initialization: paused=%t active=%d",
			paused,
			active,
		)
	}

	unmarkedCtx, cancelUnmarked := context.WithTimeout(
		context.Background(),
		100*time.Millisecond,
	)
	_, unmarkedErr := NewEventWorkflowExecutor(unmarkedCtx, loop)
	cancelUnmarked()
	if unmarkedErr == nil {
		t.Fatal("unmarked event workflow executor crossed startup barrier")
	}

	releaseStartup()
	if runtimeLeaseOwner(setupCtx) != nil {
		t.Fatal("released startup context retained runtime ownership")
	}
	loop.runtimeGateMu.Lock()
	active = loop.runtimeGateActive
	loop.runtimeGateMu.Unlock()
	if active != 0 {
		t.Fatalf("active runtime leases after startup release = %d, want 0", active)
	}

	ordinaryAdmission := make(chan error, 1)
	go func() {
		_, release, acquireErr := loop.AcquireRuntimeGeneration(
			context.Background(),
			cfg,
		)
		if acquireErr == nil {
			release()
		}
		ordinaryAdmission <- acquireErr
	}()
	select {
	case acquireErr := <-ordinaryAdmission:
		t.Fatalf("ordinary runtime admission crossed startup barrier: %v", acquireErr)
	case <-time.After(100 * time.Millisecond):
	}

	loop.ReleaseRuntimeStartupBarrier()
	select {
	case acquireErr := <-ordinaryAdmission:
		if acquireErr != nil {
			t.Fatalf("ordinary runtime admission after barrier release: %v", acquireErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ordinary runtime admission stayed blocked after barrier release")
	}
	if _, _, reacquireErr := loop.AcquireRuntimeStartupUse(
		context.Background(),
		cfg,
	); reacquireErr == nil {
		t.Fatal("startup runtime ownership was admitted after barrier release")
	}
}
