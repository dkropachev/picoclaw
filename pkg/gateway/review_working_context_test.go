package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestDefaultReviewRuntimeAgentReturnsExactCanonicalAgent(t *testing.T) {
	cfg, agentLoop := newReviewWorkingContextTestLoop(t)

	got, err := defaultReviewRuntimeAgent(agentLoop)
	if err != nil {
		t.Fatalf("defaultReviewRuntimeAgent() error = %v", err)
	}
	want, ok := agentLoop.GetRegistry().GetAgent("reviewer")
	if !ok || want == nil {
		t.Fatal("test runtime does not contain the configured reviewer agent")
	}
	if got != want {
		t.Fatalf("defaultReviewRuntimeAgent() = %p, want exact runtime agent %p", got, want)
	}
	if got.ID != "reviewer" {
		t.Fatalf("default review agent ID = %q, want exact canonical ID reviewer", got.ID)
	}
	if got.Sessions == nil {
		t.Fatal("default review agent session store is nil")
	}
	if agentLoop.GetConfig() != cfg {
		t.Fatal("test runtime did not retain the exact config generation")
	}
}

func TestReviewWorkingContextRuntimeAcquireReturnsExactAgentStore(t *testing.T) {
	cfg, agentLoop := newReviewWorkingContextTestLoop(t)
	runtimeAgent, ok := agentLoop.GetRegistry().GetAgent("reviewer")
	if !ok || runtimeAgent == nil || runtimeAgent.Sessions == nil {
		t.Fatal("test runtime reviewer agent or its session store is unavailable")
	}

	acquire := newReviewWorkingContextRuntimeAcquire(cfg, agentLoop)
	leaseCtx, store, release, err := acquire(context.Background(), "reviewer")
	if err != nil {
		t.Fatalf("working-context acquire error = %v", err)
	}
	defer release()
	if leaseCtx == nil {
		t.Fatal("working-context acquire returned a nil context")
	}
	if store != runtimeAgent.Sessions {
		t.Fatalf("working-context store = %T %p, want exact agent store %T %p",
			store, store, runtimeAgent.Sessions, runtimeAgent.Sessions)
	}

	for _, agentID := range []string{"", "Reviewer", " reviewer", "reviewer "} {
		_, invalidStore, invalidRelease, invalidErr := acquire(context.Background(), agentID)
		invalidRelease()
		if invalidErr == nil {
			t.Fatalf("working-context acquire agent %q succeeded, want canonical-ID error", agentID)
		}
		if invalidStore != nil {
			t.Fatalf("working-context acquire agent %q store = %T, want nil", agentID, invalidStore)
		}
		if !strings.Contains(invalidErr.Error(), "exact and canonical") {
			t.Fatalf("working-context acquire agent %q error = %v, want canonical-ID error", agentID, invalidErr)
		}
	}
}

func TestReviewWorkingContextRuntimeAcquireRejectsGenerationAndAdmissionFailures(t *testing.T) {
	t.Run("generation mismatch", func(t *testing.T) {
		cfg, agentLoop := newReviewWorkingContextTestLoop(t)
		staleGeneration := *cfg
		acquire := newReviewWorkingContextRuntimeAcquire(&staleGeneration, agentLoop)

		_, store, release, err := acquire(context.Background(), "reviewer")
		release()
		if err == nil || !strings.Contains(err.Error(), "runtime config generation changed") {
			t.Fatalf("working-context acquire error = %v, want generation-changed error", err)
		}
		if store != nil {
			t.Fatalf("working-context store = %T, want nil after generation mismatch", store)
		}
		assertReviewRuntimeLeaseReleased(t, agentLoop)
	})

	t.Run("runtime admission failure", func(t *testing.T) {
		cfg, agentLoop := newReviewWorkingContextTestLoop(t)
		acquire := newReviewWorkingContextRuntimeAcquire(cfg, agentLoop)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, store, release, err := acquire(ctx, "reviewer")
		release()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("working-context acquire error = %v, want context.Canceled", err)
		}
		if store != nil {
			t.Fatalf("working-context store = %T, want nil after admission failure", store)
		}
		assertReviewRuntimeLeaseReleased(t, agentLoop)
	})
}

func TestReviewWorkingContextRuntimeAcquireMissingAgentReleasesLease(t *testing.T) {
	cfg, agentLoop := newReviewWorkingContextTestLoop(t)
	acquire := newReviewWorkingContextRuntimeAcquire(cfg, agentLoop)

	_, store, release, err := acquire(context.Background(), "missing")
	release()
	if err == nil || !strings.Contains(err.Error(), `review agent "missing" is unavailable`) {
		t.Fatalf("working-context acquire error = %v, want missing-agent error", err)
	}
	if store != nil {
		t.Fatalf("working-context store = %T, want nil for missing agent", store)
	}
	assertReviewRuntimeLeaseReleased(t, agentLoop)
}

func TestReviewWorkingContextRuntimeAcquireHoldsLeaseUntilRelease(t *testing.T) {
	cfg, agentLoop := newReviewWorkingContextTestLoop(t)
	acquire := newReviewWorkingContextRuntimeAcquire(cfg, agentLoop)

	_, store, release, err := acquire(context.Background(), "reviewer")
	if err != nil {
		t.Fatalf("working-context acquire error = %v", err)
	}
	if store == nil {
		release()
		t.Fatal("working-context acquire returned a nil session store")
	}

	blockedCtx, cancelBlocked := context.WithTimeout(context.Background(), 100*time.Millisecond)
	resume, pauseErr := agentLoop.PauseRuntimeForReload(blockedCtx)
	cancelBlocked()
	if resume != nil {
		resume()
	}
	if !errors.Is(pauseErr, context.DeadlineExceeded) {
		release()
		t.Fatalf("PauseRuntimeForReload() while leased error = %v, want deadline exceeded", pauseErr)
	}

	release()
	release() // The runtime release returned to consumers must be idempotent.
	assertReviewRuntimeLeaseReleased(t, agentLoop)
}

func newReviewWorkingContextTestLoop(t *testing.T) (*config.Config, *agent.AgentLoop) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP.Enabled = false
	cfg.Agents.List = []config.AgentConfig{{
		ID:      "reviewer",
		Default: true,
	}}
	messageBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(
		cfg,
		messageBus,
		&startupBlockedProvider{reason: "not used"},
	)
	t.Cleanup(func() {
		agentLoop.Stop()
		messageBus.Close()
		agentLoop.Close()
	})
	return cfg, agentLoop
}

func assertReviewRuntimeLeaseReleased(t *testing.T, agentLoop *agent.AgentLoop) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resume, err := agentLoop.PauseRuntimeForReload(ctx)
	if err != nil {
		t.Fatalf("PauseRuntimeForReload() after release error = %v", err)
	}
	if resume == nil {
		t.Fatal("PauseRuntimeForReload() after release returned a nil resume function")
	}
	resume()
}
