//nolint:govet // Generation assertions intentionally reuse short-lived error bindings.
package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type runtimeGateProvider struct {
	name   string
	closed chan struct{}
	called chan struct{}
}

func (p *runtimeGateProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	if p.called != nil {
		select {
		case p.called <- struct{}{}:
		default:
		}
	}
	return &providers.LLMResponse{Content: p.name}, nil
}

func (p *runtimeGateProvider) Close() {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
}

func TestAgentLoopStopBeforeRunPreventsLateStartup(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	msgBus := bus.NewMessageBus()
	provider := &runtimeGateProvider{name: "provider", closed: make(chan struct{})}
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, provider)

	// Model the gateway shutdown winning immediately after spawning Run but
	// before the goroutine is scheduled. Stop is terminal and must be
	// remembered across that registration gap.
	al.Stop()
	msgBus.Close()
	al.Close()
	if err := al.Run(context.Background()); err != nil {
		t.Fatalf("late AgentLoop.Run() error = %v", err)
	}
	if al.running.Load() {
		t.Fatal("late AgentLoop.Run() started after terminal Stop")
	}
	al.runLifecycleMu.Lock()
	done := al.runDone
	al.runLifecycleMu.Unlock()
	if done != nil {
		t.Fatal("late AgentLoop.Run() registered after terminal Stop")
	}
}

func TestAgentLoopStopRejectsNewRootRuntimeButAllowsRetainedChildren(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := newTestAgentLoopWithStrictModels(
		cfg,
		bus.NewMessageBus(),
		&runtimeGateProvider{name: "provider", closed: make(chan struct{})},
	)
	defer al.Close()

	rootCtx, releaseRoot, err := al.acquireRuntimeUse(context.Background())
	if err != nil {
		t.Fatalf("acquireRuntimeUse() error = %v", err)
	}
	al.Stop()

	_, releaseChild, err := al.retainRuntimeUse(rootCtx)
	if err != nil {
		releaseRoot()
		t.Fatalf("retainRuntimeUse() for admitted parent error = %v", err)
	}
	if _, _, err = al.acquireRuntimeUse(context.Background()); !errors.Is(err, errAgentRuntimeStopped) {
		releaseChild()
		releaseRoot()
		t.Fatalf("fresh acquireRuntimeUse() error = %v, want %v", err, errAgentRuntimeStopped)
	}
	releaseChild()
	releaseRoot()
}

func TestAgentLoopStopWakesPausedRootRuntimeAdmission(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	al := newTestAgentLoopWithStrictModels(
		cfg,
		bus.NewMessageBus(),
		&runtimeGateProvider{name: "provider", closed: make(chan struct{})},
	)
	defer al.Close()

	resume, err := al.PauseRuntimeForReload(context.Background())
	if err != nil {
		t.Fatalf("PauseRuntimeForReload() error = %v", err)
	}
	defer resume()

	admissionDone := make(chan error, 1)
	go func() {
		_, _, acquireErr := al.acquireRuntimeUse(context.Background())
		admissionDone <- acquireErr
	}()

	al.Stop()
	select {
	case acquireErr := <-admissionDone:
		if !errors.Is(acquireErr, errAgentRuntimeStopped) {
			t.Fatalf("paused acquireRuntimeUse() error = %v, want %v", acquireErr, errAgentRuntimeStopped)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not wake paused root runtime admission")
	}
}

func TestPauseRuntimeForReloadWithContextRevokesOwnershipOnResume(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&runtimeGateProvider{name: "provider", closed: make(chan struct{})},
	)
	defer func() {
		al.Close()
		messageBus.Close()
	}()

	ownedCtx, resume, err := al.PauseRuntimeForReloadWithContext(
		context.Background(),
		context.Background(),
	)
	if err != nil {
		t.Fatalf("PauseRuntimeForReloadWithContext() error = %v", err)
	}
	if runtimeLeaseOwner(ownedCtx) != al {
		resume()
		t.Fatal("paused runtime context did not own the agent loop")
	}
	if _, generationErr := al.runtimeGenerationFromLease(ownedCtx); generationErr == nil {
		resume()
		t.Fatal("generationless pause owner exposed a runtime tuple")
	}
	var childBoundary *runtimeLeaseBoundary
	err = al.withPausedRuntimeGeneration(ownedCtx, func(ctx context.Context) error {
		value, _ := runtimeLeaseContextFrom(ctx)
		childBoundary = value.boundary
		generation, generationErr := al.runtimeGenerationFromLease(ctx)
		if generationErr != nil {
			return generationErr
		}
		if generation.cfg != cfg || generation.registry != al.GetRegistry() {
			t.Fatalf("paused child generation = %#v, want exact current tuple", generation)
		}
		if got := logger.DiagnosticPolicyFromContext(ctx); got != (logger.DiagnosticPolicy{}) {
			t.Fatalf("paused child diagnostic policy = %#v, want zero", got)
		}
		if _, _, retainErr := al.retainRuntimeUse(ctx); retainErr == nil {
			t.Fatal("paused-current child was retainable")
		}
		return nil
	})
	if err != nil {
		resume()
		t.Fatalf("withPausedRuntimeGeneration() error = %v", err)
	}
	if childBoundary == nil || childBoundary.live() {
		resume()
		t.Fatal("paused-current child remained live after callback return")
	}
	al.runtimeGateMu.Lock()
	paused := al.runtimeGatePaused
	active := al.runtimeGateActive
	al.runtimeGateMu.Unlock()
	if !paused || active != 0 {
		resume()
		t.Fatalf("paused runtime gate = (paused=%t active=%d), want true/0", paused, active)
	}

	resume()
	if runtimeLeaseOwner(ownedCtx) != nil {
		t.Fatal("resumed reload context retained runtime ownership")
	}
	_, releaseRuntime, err := al.acquireRuntimeUse(context.Background())
	if err != nil {
		t.Fatalf("runtime admission after resume error = %v", err)
	}
	releaseRuntime()
}

func TestP015B3AOrdinaryNestedAndReleasePolicy(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&runtimeGateProvider{name: "provider", closed: make(chan struct{})},
	)
	defer func() {
		al.Close()
		messageBus.Close()
	}()
	positive := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	al.mu.Lock()
	al.diagnosticPolicy = positive
	al.mu.Unlock()

	rootCtx, releaseRoot, err := al.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := logger.DiagnosticPolicyFromContext(rootCtx); got != positive {
		releaseRoot()
		t.Fatalf("root policy = %#v, want positive", got)
	}
	rootGeneration, err := al.runtimeGenerationFromLease(rootCtx)
	if err != nil || rootGeneration.cfg != cfg || rootGeneration.id == 0 {
		releaseRoot()
		t.Fatalf("root generation = %#v, %v", rootGeneration, err)
	}
	origin, err := al.runtimeDiagnosticOriginFromLease(rootCtx)
	if err != nil || !origin.valid || origin.policy != positive {
		releaseRoot()
		t.Fatalf("root origin = %#v, %v", origin, err)
	}

	retainedCtx, releaseRetained, err := al.retainRuntimeUse(rootCtx)
	if err != nil {
		releaseRoot()
		t.Fatal(err)
	}
	releaseRoot()
	if runtimeLeaseOwner(rootCtx) != nil {
		releaseRetained()
		t.Fatal("released root retained runtime ownership")
	}
	if _, err := al.runtimeGenerationFromLease(rootCtx); err == nil {
		releaseRetained()
		t.Fatal("released root retained tuple access")
	}
	if runtimeLeaseOwner(retainedCtx) != al ||
		logger.DiagnosticPolicyFromContext(retainedCtx) != positive {
		releaseRetained()
		t.Fatal("independent retained child did not survive parent release")
	}
	releaseRetained()
	if runtimeLeaseOwner(retainedCtx) != nil {
		t.Fatal("released retained child kept runtime ownership")
	}

	// Hold the diagnostic revoker open to make the forbidden release window
	// deterministic. Runtime ownership must remain live until diagnostic
	// authority is revoked; otherwise a lock-free sink can observe positive
	// policy after the lease has disappeared.
	releaseBoundary := newRuntimeLeaseBoundary(nil)
	releaseCtx, revokeReleaseDiagnostic := logger.BindRootDiagnosticPolicy(
		context.Background(),
		positive,
	)
	releaseCtx = context.WithValue(
		releaseCtx,
		runtimeLeaseContextKey{},
		runtimeLeaseContextValue{
			owner: al, generation: &rootGeneration, boundary: releaseBoundary,
			kind: runtimeLeaseKindTrustedRoot,
		},
	)
	al.runtimeGateMu.Lock()
	al.runtimeGateActive++
	al.signalRuntimeGateChangedLocked()
	al.runtimeGateMu.Unlock()
	revokeEntered := make(chan struct{})
	continueRevoke := make(chan struct{})
	releaseDone := make(chan struct{})
	releaseOrdered := al.newRuntimeLeaseRelease(
		releaseBoundary,
		func() {
			close(revokeEntered)
			<-continueRevoke
			revokeReleaseDiagnostic()
		},
		true,
	)
	go func() {
		releaseOrdered()
		close(releaseDone)
	}()
	<-revokeEntered
	ownerDuringRevoke := runtimeLeaseOwner(releaseCtx)
	policyDuringRevoke := logger.DiagnosticPolicyFromContext(releaseCtx)
	close(continueRevoke)
	<-releaseDone
	if ownerDuringRevoke != al || policyDuringRevoke != positive {
		t.Fatalf(
			"release exposed revoked ownership before diagnostic revoke: owner=%p policy=%#v",
			ownerDuringRevoke,
			policyDuringRevoke,
		)
	}
	if runtimeLeaseOwner(releaseCtx) != nil ||
		logger.DiagnosticPolicyFromContext(releaseCtx) != (logger.DiagnosticPolicy{}) {
		t.Fatal("completed release retained runtime or diagnostic authority")
	}
	al.runtimeGateMu.Lock()
	releaseActive := al.runtimeGateActive
	al.runtimeGateMu.Unlock()
	if releaseActive != 0 {
		t.Fatalf("ordered release leaked %d active runtime users", releaseActive)
	}
	if _, _, err := al.retainRuntimeUse(rootCtx); err == nil {
		t.Fatal("released root could be retained again")
	}
}

func TestP015B3ARetainAndStopPolicy(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&runtimeGateProvider{name: "provider", closed: make(chan struct{})},
	)
	defer func() {
		al.Close()
		messageBus.Close()
	}()
	positive := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	al.mu.Lock()
	al.diagnosticPolicy = positive
	al.mu.Unlock()

	for range 200 {
		rootCtx, releaseRoot, err := al.acquireTrustedRuntimeRoot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		retainedResult := make(chan struct {
			ctx     context.Context
			release func()
			err     error
		}, 1)
		rootReleased := make(chan struct{})
		go func() {
			<-start
			retainedCtx, releaseRetained, retainErr := al.retainRuntimeUse(rootCtx)
			retainedResult <- struct {
				ctx     context.Context
				release func()
				err     error
			}{ctx: retainedCtx, release: releaseRetained, err: retainErr}
		}()
		go func() {
			<-start
			releaseRoot()
			close(rootReleased)
		}()
		close(start)
		result := <-retainedResult
		<-rootReleased
		if result.err == nil {
			if runtimeLeaseOwner(result.ctx) != al ||
				logger.DiagnosticPolicyFromContext(result.ctx) != positive {
				result.release()
				t.Fatal("retain won linearization without a complete live child")
			}
			result.release()
		}
		if runtimeLeaseOwner(rootCtx) != nil {
			t.Fatal("released root remained live after retain/release race")
		}
		al.runtimeGateMu.Lock()
		active := al.runtimeGateActive
		al.runtimeGateMu.Unlock()
		if active != 0 {
			t.Fatalf("retain/release race leaked %d active runtime users", active)
		}
	}

	falseCap := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	assertNoActive := func(label string) {
		t.Helper()
		al.runtimeGateMu.Lock()
		active := al.runtimeGateActive
		al.runtimeGateMu.Unlock()
		if active != 0 {
			t.Fatalf("%s leaked %d active runtime users", label, active)
		}
	}

	// Model the live-linked request cap installed by ToolRegistry execution.
	// Retain-before-return must snapshot its false cap independently; return-
	// before-retain must fail rather than treating the context as a new root.
	rootBefore, releaseRootBefore, err := al.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	linkedBefore, revokeLinkedBefore := logger.NarrowDiagnosticPolicy(rootBefore, falseCap)
	retainedBefore, releaseRetainedBefore, err := al.retainRuntimeUse(linkedBefore)
	if err != nil {
		revokeLinkedBefore()
		releaseRootBefore()
		t.Fatal(err)
	}
	revokeLinkedBefore()
	releaseRootBefore()
	if runtimeLeaseOwner(retainedBefore) != al ||
		logger.DiagnosticPolicyFromContext(retainedBefore) != falseCap.Meet(positive) {
		releaseRetainedBefore()
		t.Fatal("live-linked retain winner lost its independent false cap")
	}
	releaseRetainedBefore()
	assertNoActive("live-linked retain winner")

	rootAfter, releaseRootAfter, err := al.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	linkedAfter, revokeLinkedAfter := logger.NarrowDiagnosticPolicy(rootAfter, falseCap)
	revokeLinkedAfter()
	releaseRootAfter()
	if retainedAfter, releaseRetainedAfter, retainErr := al.retainRuntimeUse(linkedAfter); retainErr == nil {
		releaseRetainedAfter()
		t.Fatalf("released live-linked origin retained as %#v", retainedAfter)
	}
	if runtimeLeaseOwner(linkedAfter) != nil ||
		logger.DiagnosticPolicyFromContext(linkedAfter) != (logger.DiagnosticPolicy{}) {
		t.Fatal("released live-linked origin retained authority")
	}
	assertNoActive("live-linked release winner")

	for range 200 {
		rootCtx, releaseRoot, acquireErr := al.acquireTrustedRuntimeRoot(context.Background())
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		linkedCtx, revokeLinked := logger.NarrowDiagnosticPolicy(rootCtx, falseCap)
		start := make(chan struct{})
		retainedResult := make(chan struct {
			ctx     context.Context
			release func()
			err     error
		}, 1)
		released := make(chan struct{})
		go func() {
			<-start
			ctx, release, retainErr := al.retainRuntimeUse(linkedCtx)
			retainedResult <- struct {
				ctx     context.Context
				release func()
				err     error
			}{ctx: ctx, release: release, err: retainErr}
		}()
		go func() {
			<-start
			revokeLinked()
			releaseRoot()
			close(released)
		}()
		close(start)
		result := <-retainedResult
		<-released
		if result.err == nil {
			got := logger.DiagnosticPolicyFromContext(result.ctx)
			if runtimeLeaseOwner(result.ctx) != al ||
				(got != falseCap.Meet(positive) && got != (logger.DiagnosticPolicy{})) {
				result.release()
				t.Fatalf("live-linked race retained widened/incomplete child %#v", got)
			}
			result.release()
		}
		if runtimeLeaseOwner(linkedCtx) != nil ||
			logger.DiagnosticPolicyFromContext(linkedCtx) != (logger.DiagnosticPolicy{}) {
			t.Fatal("live-linked race left origin authority live")
		}
		assertNoActive("live-linked retain/release race")
	}

	rootCtx, releaseRoot, err := al.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	retainedCtx, releaseRetained, err := al.retainRuntimeUse(rootCtx)
	if err != nil {
		releaseRoot()
		t.Fatal(err)
	}
	releaseRoot()
	al.Stop()
	if _, _, err := al.acquireTrustedRuntimeRoot(rootCtx); !errors.Is(
		err,
		errAgentRuntimeStopped,
	) {
		releaseRetained()
		t.Fatalf("released context bypassed Stop: %v", err)
	}
	if runtimeLeaseOwner(retainedCtx) != al ||
		logger.DiagnosticPolicyFromContext(retainedCtx) != positive {
		releaseRetained()
		t.Fatal("Stop revoked already-retained in-flight work")
	}
	releaseRetained()
}

func TestP015B3ANestedOriginCurrentTracksParentRevocation(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&runtimeGateProvider{name: "provider", closed: make(chan struct{})},
	)
	defer func() {
		al.Close()
		messageBus.Close()
	}()
	positive := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	al.mu.Lock()
	al.diagnosticPolicy = positive
	al.mu.Unlock()

	rootCtx, releaseRoot, err := al.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	origin, err := al.runtimeDiagnosticOriginFromLease(rootCtx)
	if err != nil {
		releaseRoot()
		t.Fatal(err)
	}
	childCtx, releaseChild, err := al.acquireRuntimeUseFromOrigin(rootCtx, origin)
	if err != nil {
		releaseRoot()
		t.Fatal(err)
	}
	if logger.DiagnosticPolicyFromContext(childCtx) != positive {
		releaseChild()
		releaseRoot()
		t.Fatal("live nested origin/current child lost positive policy")
	}
	releaseRoot()
	if logger.DiagnosticPolicyFromContext(childCtx) != (logger.DiagnosticPolicy{}) {
		releaseChild()
		t.Fatal("nested origin/current child outlived parent revocation")
	}
	releaseChild()
}

func TestP015B3ALateReacquirePolicyMatrix(t *testing.T) {
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	disabled := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	for _, test := range []struct {
		name          string
		origin        logger.DiagnosticPolicy
		current       logger.DiagnosticPolicy
		missingOrigin bool
		want          logger.DiagnosticPolicy
	}{
		{name: "true_true", origin: enabled, current: enabled, want: enabled},
		{name: "true_false", origin: enabled, current: disabled, want: enabled.Meet(disabled)},
		{name: "false_true", origin: disabled, current: enabled, want: disabled.Meet(enabled)},
		{name: "missing_true", current: enabled, missingOrigin: true, want: logger.DiagnosticPolicy{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfgA := config.DefaultConfig()
			cfgA.Agents.Defaults.Workspace = t.TempDir()
			cfgB := config.DefaultConfig()
			cfgB.Agents.Defaults.Workspace = t.TempDir()
			messageBus := bus.NewMessageBus()
			loop := NewAgentLoopWithRuntimePolicies(
				cfgA,
				messageBus,
				&runtimeGateProvider{name: "A", closed: make(chan struct{})},
				isolation.NewExecutionPolicy(cfgA.Isolation),
				test.origin,
			)
			defer func() {
				loop.Close()
				messageBus.Close()
			}()

			rootCtx, releaseRoot, err := loop.acquireTrustedRuntimeRoot(
				context.Background(),
			)
			if err != nil {
				t.Fatal(err)
			}
			origin := runtimeDiagnosticOrigin{}
			if !test.missingOrigin {
				origin, err = loop.runtimeDiagnosticOriginFromLease(rootCtx)
				if err != nil {
					releaseRoot()
					t.Fatal(err)
				}
			}
			releaseRoot()
			if reloadErr := loop.ReloadProviderAndConfigWithRuntimePolicies(
				context.Background(),
				&runtimeGateProvider{name: "B", closed: make(chan struct{})},
				cfgB,
				isolation.NewExecutionPolicy(cfgB.Isolation),
				test.current,
			); reloadErr != nil {
				t.Fatal(reloadErr)
			}
			lateCtx, releaseLate, err := loop.acquireRuntimeUseFromOrigin(
				context.Background(),
				origin,
			)
			if err != nil {
				t.Fatal(err)
			}
			defer releaseLate()
			if got := logger.DiagnosticPolicyFromContext(lateCtx); got != test.want {
				t.Fatalf("late policy = %#v, want %#v", got, test.want)
			}
		})
	}

	t.Run("broken_live_link_and_cumulative_false", func(t *testing.T) {
		cfgA := config.DefaultConfig()
		cfgA.Agents.Defaults.Workspace = t.TempDir()
		cfgB := config.DefaultConfig()
		cfgB.Agents.Defaults.Workspace = t.TempDir()
		cfgC := config.DefaultConfig()
		cfgC.Agents.Defaults.Workspace = t.TempDir()
		messageBus := bus.NewMessageBus()
		loop := NewAgentLoopWithRuntimePolicies(
			cfgA,
			messageBus,
			&runtimeGateProvider{name: "A", closed: make(chan struct{})},
			isolation.NewExecutionPolicy(cfgA.Isolation),
			enabled,
		)
		defer func() {
			loop.Close()
			messageBus.Close()
		}()
		rootCtx, releaseRoot, err := loop.acquireTrustedRuntimeRoot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		brokenCtx, revokeBroken := logger.NarrowDiagnosticPolicy(rootCtx, enabled)
		releaseRoot()
		defer revokeBroken()
		if reloadErr := loop.ReloadProviderAndConfigWithRuntimePolicies(
			context.Background(),
			&runtimeGateProvider{name: "B", closed: make(chan struct{})},
			cfgB,
			isolation.NewExecutionPolicy(cfgB.Isolation),
			disabled,
		); reloadErr != nil {
			t.Fatal(reloadErr)
		}
		bCtx, releaseB, err := loop.AcquireRuntimeGeneration(brokenCtx, cfgB)
		if err != nil {
			t.Fatal(err)
		}
		if got := logger.DiagnosticPolicyFromContext(bCtx); got != (logger.DiagnosticPolicy{}) {
			releaseB()
			t.Fatalf("broken live-linked origin revived as %#v", got)
		}
		releaseB()

		freshB, releaseFreshB, err := loop.acquireTrustedRuntimeRoot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		originB, err := loop.runtimeDiagnosticOriginFromLease(freshB)
		if err != nil {
			releaseFreshB()
			t.Fatal(err)
		}
		releaseFreshB()
		if reloadErr := loop.ReloadProviderAndConfigWithRuntimePolicies(
			context.Background(),
			&runtimeGateProvider{name: "C", closed: make(chan struct{})},
			cfgC,
			isolation.NewExecutionPolicy(cfgC.Isolation),
			enabled,
		); reloadErr != nil {
			t.Fatal(reloadErr)
		}
		cCtx, releaseC, err := loop.acquireRuntimeUseFromOrigin(
			context.Background(),
			originB,
		)
		if err != nil {
			t.Fatal(err)
		}
		defer releaseC()
		if got := logger.DiagnosticPolicyFromContext(cCtx); got != disabled.Meet(enabled) {
			t.Fatalf("A(true)->B(false)->C(true) widened to %#v", got)
		}
	})
}

func TestP015B3ADetachedGenerationWorkIsSafeOnly(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	loop := NewAgentLoopWithRuntimePolicies(
		cfg,
		messageBus,
		&runtimeGateProvider{name: "detached", closed: make(chan struct{})},
		isolation.NewExecutionPolicy(cfg.Isolation),
		enabled,
	)
	defer func() {
		loop.Close()
		messageBus.Close()
	}()

	ctx, release, err := loop.AcquireRuntimeGeneration(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if got := logger.DiagnosticPolicyFromContext(ctx); got != (logger.DiagnosticPolicy{}) {
		t.Fatalf("detached exact-generation policy = %#v, want zero", got)
	}
	ordinary, releaseOrdinary, err := loop.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	releaseOrdinary()
	staleSameLoop, releaseStaleSameLoop, err := loop.AcquireRuntimeGeneration(
		ordinary,
		cfg,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := logger.DiagnosticPolicyFromContext(staleSameLoop); got !=
		(logger.DiagnosticPolicy{}) {
		releaseStaleSameLoop()
		t.Fatalf("released same-loop origin gained policy %#v", got)
	}
	releaseStaleSameLoop()

	forged, revokeForged := logger.BindRootDiagnosticPolicy(
		context.Background(),
		enabled,
	)
	defer revokeForged()
	forgedLease, releaseForged, err := loop.AcquireRuntimeGeneration(forged, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := logger.DiagnosticPolicyFromContext(forgedLease); got !=
		(logger.DiagnosticPolicy{}) {
		releaseForged()
		t.Fatalf("forged public diagnostic binding gained policy %#v", got)
	}
	releaseForged()

	foreignCfg := config.DefaultConfig()
	foreignCfg.Agents.Defaults.Workspace = t.TempDir()
	foreignBus := bus.NewMessageBus()
	foreignLoop := NewAgentLoopWithRuntimePolicies(
		foreignCfg,
		foreignBus,
		&runtimeGateProvider{name: "foreign", closed: make(chan struct{})},
		isolation.NewExecutionPolicy(foreignCfg.Isolation),
		enabled,
	)
	defer func() {
		foreignLoop.Close()
		foreignBus.Close()
	}()
	foreignCtx, releaseForeign, err := foreignLoop.acquireTrustedRuntimeRoot(
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}
	releaseForeign()
	// Rebind intentionally preserves an ordinary origin's effective value after
	// revoke. The private stale foreign marker must still prevent that public
	// value from becoming authority in this loop.
	staleForeign, revokeStaleForeign := logger.RebindDiagnosticPolicy(
		foreignCtx,
		foreignCtx,
		enabled,
	)
	defer revokeStaleForeign()
	foreignLease, releaseForeignLease, err := loop.AcquireRuntimeGeneration(
		staleForeign,
		cfg,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := logger.DiagnosticPolicyFromContext(foreignLease); got !=
		(logger.DiagnosticPolicy{}) {
		releaseForeignLease()
		t.Fatalf("revoked foreign origin gained policy %#v", got)
	}
	releaseForeignLease()
}

func TestP015B3ARuntimeAdmissionRejectsLiveForeignLoop(t *testing.T) {
	t.Parallel()

	newLoop := func(workspace string) (*AgentLoop, *bus.MessageBus) {
		cfg := config.DefaultConfig()
		cfg.Agents.Defaults.Workspace = workspace
		messageBus := bus.NewMessageBus()
		loop := newTestAgentLoopWithStrictModels(
			cfg,
			messageBus,
			&runtimeGateProvider{name: workspace, closed: make(chan struct{})},
		)
		return loop, messageBus
	}
	loopA, busA := newLoop(t.TempDir())
	loopB, busB := newLoop(t.TempDir())
	defer func() {
		loopA.Close()
		loopB.Close()
		busA.Close()
		busB.Close()
	}()

	ctxA, releaseA, err := loopA.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	originA, err := loopA.runtimeDiagnosticOriginFromLease(ctxA)
	if err != nil {
		t.Fatal(err)
	}

	assertRejected := func(
		name string,
		parent context.Context,
		acquire func() (context.Context, func(), error),
	) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			before, beforeErr := loopB.snapshotRuntimeGeneration()
			if beforeErr != nil {
				t.Fatal(beforeErr)
			}
			loopA.runtimeGateMu.Lock()
			beforeA := loopA.runtimeGateActive
			loopA.runtimeGateMu.Unlock()
			loopB.runtimeGateMu.Lock()
			beforeB := loopB.runtimeGateActive
			loopB.runtimeGateMu.Unlock()
			returned, release, acquireErr := acquire()
			if release != nil {
				release()
			}
			if acquireErr == nil {
				t.Fatal("live foreign runtime lease was accepted")
			}
			if runtimeLeaseOwner(returned) == loopB {
				t.Fatal("failed foreign admission published a loop-B lease")
			}
			if logger.DiagnosticPolicyFromContext(returned) !=
				logger.DiagnosticPolicyFromContext(parent) {
				t.Fatal("failed foreign admission changed the caller diagnostic binding")
			}
			after, afterErr := loopB.snapshotRuntimeGeneration()
			if afterErr != nil || after.id != before.id || after.cfg != before.cfg ||
				after.registry != before.registry ||
				after.diagnosticPolicy != before.diagnosticPolicy {
				t.Fatalf(
					"failed foreign admission changed current tuple: before=%#v after=%#v err=%v",
					before,
					after,
					afterErr,
				)
			}
			loopA.runtimeGateMu.Lock()
			afterA := loopA.runtimeGateActive
			loopA.runtimeGateMu.Unlock()
			loopB.runtimeGateMu.Lock()
			afterB := loopB.runtimeGateActive
			loopB.runtimeGateMu.Unlock()
			if afterA != beforeA || afterB != beforeB {
				t.Fatalf("failed foreign admission changed counts A:%d->%d B:%d->%d", beforeA, afterA, beforeB, afterB)
			}
		})
	}
	assertRejected("trusted", ctxA, func() (context.Context, func(), error) {
		return loopB.acquireTrustedRuntimeRoot(ctxA)
	})
	assertRejected("inherited", ctxA, func() (context.Context, func(), error) {
		return loopB.acquireInheritedRuntimeUse(ctxA)
	})
	assertRejected("origin_current", ctxA, func() (context.Context, func(), error) {
		return loopB.acquireRuntimeUseFromOrigin(ctxA, originA)
	})
	assertRejected("detached", ctxA, func() (context.Context, func(), error) {
		return loopB.acquireDetachedRuntimeUse(ctxA)
	})
	assertRejected("exact_generation", ctxA, func() (context.Context, func(), error) {
		return loopB.AcquireRuntimeGeneration(ctxA, loopB.GetConfig())
	})
	assertRejected("retain", ctxA, func() (context.Context, func(), error) {
		return loopB.retainRuntimeUse(ctxA)
	})

	assertEffectFreeFailure := func(
		name string,
		parent context.Context,
		acquire func() (context.Context, func(), error),
	) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			before, beforeErr := loopB.snapshotRuntimeGeneration()
			if beforeErr != nil {
				t.Fatal(beforeErr)
			}
			returned, release, acquireErr := acquire()
			if release != nil {
				release()
			}
			if acquireErr == nil {
				t.Fatal("invalid runtime admission succeeded")
			}
			if runtimeLeaseOwner(returned) != nil ||
				logger.DiagnosticPolicyFromContext(returned) !=
					logger.DiagnosticPolicyFromContext(parent) {
				t.Fatal("failed runtime admission published authority")
			}
			after, afterErr := loopB.snapshotRuntimeGeneration()
			loopB.runtimeGateMu.Lock()
			active := loopB.runtimeGateActive
			loopB.runtimeGateMu.Unlock()
			if afterErr != nil || after.id != before.id || after.cfg != before.cfg ||
				after.registry != before.registry || active != 0 {
				t.Fatalf(
					"failed runtime admission changed tuple/count: before=%#v after=%#v active=%d err=%v",
					before,
					after,
					active,
					afterErr,
				)
			}
		})
	}
	mismatchCtx := context.Background()
	assertEffectFreeFailure("config mismatch", mismatchCtx, func() (context.Context, func(), error) {
		return loopB.AcquireRuntimeGeneration(mismatchCtx, &config.Config{})
	})
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	assertEffectFreeFailure("canceled", canceledCtx, func() (context.Context, func(), error) {
		return loopB.acquireTrustedRuntimeRoot(canceledCtx)
	})

	loopA.runtimeGateMu.Lock()
	activeA := loopA.runtimeGateActive
	loopA.runtimeGateMu.Unlock()
	loopB.runtimeGateMu.Lock()
	activeB := loopB.runtimeGateActive
	loopB.runtimeGateMu.Unlock()
	if activeA != 1 || activeB != 0 {
		t.Fatalf("foreign rejection active counts = A:%d B:%d, want 1/0", activeA, activeB)
	}
}

func TestP015B3AIndependentLoopPolicyRace(t *testing.T) {
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	disabled := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	newLoop := func(policy logger.DiagnosticPolicy) (*AgentLoop, *bus.MessageBus) {
		cfg := config.DefaultConfig()
		cfg.Agents.Defaults.Workspace = t.TempDir()
		messageBus := bus.NewMessageBus()
		return NewAgentLoopWithRuntimePolicies(
			cfg,
			messageBus,
			&runtimeGateProvider{name: "independent", closed: make(chan struct{})},
			isolation.NewExecutionPolicy(cfg.Isolation),
			policy,
		), messageBus
	}
	loopA, busA := newLoop(enabled)
	loopB, busB := newLoop(disabled)
	defer func() {
		loopA.Close()
		loopB.Close()
		busA.Close()
		busB.Close()
	}()

	start := make(chan struct{})
	results := make(chan error, 2)
	run := func(loop *AgentLoop, want logger.DiagnosticPolicy) {
		<-start
		for range 500 {
			ctx, release, err := loop.acquireTrustedRuntimeRoot(context.Background())
			if err != nil {
				results <- err
				return
			}
			if got := logger.DiagnosticPolicyFromContext(ctx); got != want {
				release()
				results <- fmt.Errorf("independent policy = %#v, want %#v", got, want)
				return
			}
			release()
		}
		results <- nil
	}
	go run(loopA, enabled)
	go run(loopB, disabled)
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestP015B3AStartupAndReloadPauseSafeOnly(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&runtimeGateProvider{name: "provider", closed: make(chan struct{})},
	)
	defer func() {
		al.Close()
		messageBus.Close()
	}()
	positive := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	al.mu.Lock()
	al.diagnosticPolicy = positive
	al.mu.Unlock()

	al.runtimeGateMu.Lock()
	al.runtimeStartupBarrier = true
	al.runtimeGatePaused = true
	al.runtimeGatePauses = 1
	al.runtimeGateMu.Unlock()
	startupCtx, releaseStartup, err := al.AcquireRuntimeStartupUse(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := logger.DiagnosticPolicyFromContext(startupCtx); got != (logger.DiagnosticPolicy{}) {
		releaseStartup()
		t.Fatalf("startup policy = %#v, want zero", got)
	}
	if _, _, retainErr := al.retainRuntimeUse(startupCtx); retainErr == nil {
		releaseStartup()
		t.Fatal("startup lease was retainable")
	}
	releaseStartup()
	if runtimeLeaseOwner(startupCtx) != nil {
		t.Fatal("released startup lease retained ownership")
	}
	al.ReleaseRuntimeStartupBarrier()

	pauseCtx, resume, err := al.PauseRuntimeForReloadWithContext(
		context.Background(),
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}
	var escapedBoundary *runtimeLeaseBoundary
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("paused callback panic was not propagated")
			}
		}()
		_ = al.withPausedRuntimeGeneration(pauseCtx, func(ctx context.Context) error {
			value, _ := runtimeLeaseContextFrom(ctx)
			escapedBoundary = value.boundary
			if logger.DiagnosticPolicyFromContext(ctx) != (logger.DiagnosticPolicy{}) {
				t.Fatal("paused-current callback gained positive policy")
			}
			panic("paused callback")
		})
	}()
	if escapedBoundary == nil || escapedBoundary.live() {
		resume()
		t.Fatal("panic-escaped paused child remained live")
	}
	al.runtimeGateMu.Lock()
	active := al.runtimeGateActive
	al.runtimeGateMu.Unlock()
	if active != 0 {
		resume()
		t.Fatalf("paused child changed active count to %d", active)
	}
	resume()
}

func TestP015B3APausedRuntimeCallbackBoundary(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&runtimeGateProvider{name: "provider", closed: make(chan struct{})},
	)
	defer func() {
		al.Close()
		messageBus.Close()
	}()

	pauseCtx, resume, err := al.PauseRuntimeForReloadWithContext(
		context.Background(),
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan context.Context, 1)
	releaseCallback := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- al.withPausedRuntimeGeneration(pauseCtx, func(ctx context.Context) error {
			entered <- ctx
			<-releaseCallback
			return nil
		})
	}()
	childCtx := <-entered
	if runtimeLeaseOwner(childCtx) != al {
		resume()
		close(releaseCallback)
		<-done
		t.Fatal("paused-current callback did not start with live ownership")
	}
	resume()
	if runtimeLeaseOwner(childCtx) != nil {
		close(releaseCallback)
		<-done
		t.Fatal("pause resume did not revoke escaped paused-current child")
	}
	if _, err := al.runtimeGenerationFromLease(childCtx); err == nil {
		close(releaseCallback)
		<-done
		t.Fatal("revoked paused-current child retained tuple access")
	}
	close(releaseCallback)
	if err := <-done; err != nil {
		t.Fatalf("paused callback returned error after concurrent resume: %v", err)
	}
	al.runtimeGateMu.Lock()
	active := al.runtimeGateActive
	al.runtimeGateMu.Unlock()
	if active != 0 {
		t.Fatalf("paused callback changed active count to %d", active)
	}
}

func TestReloadDrainsRuntimeGenerationBeforeReturningRetainedProvider(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	providerA := &runtimeGateProvider{name: "provider-a", closed: make(chan struct{})}
	providerB := &runtimeGateProvider{name: "provider-b", closed: make(chan struct{})}
	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), providerA)
	defer al.Close()
	factory := reloadToolRegistryLeaseFactory(t)
	liveA := &reloadToolRegistryLeaseProbe{marker: 10}
	agentA := al.GetRegistry().GetDefaultAgent()
	if agentA == nil || agentA.Tools == nil {
		t.Fatal("generation A tool registry is unavailable")
	}
	if registerErr := agentA.Tools.RegisterFactoryBacked(liveA, factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	competitorA := tools.NewToolRegistry()
	if registerErr := competitorA.RegisterFactoryBacked(liveA, factory); registerErr == nil {
		t.Fatal("generation A compatibility lease was not retained")
	}

	cfgB := *cfg
	previous, err := al.ReloadProviderAndConfigRetainingPrevious(
		context.Background(),
		providerB,
		&cfgB,
	)
	if err != nil {
		t.Fatalf("reload A -> B error = %v", err)
	}
	if previous != providerA {
		t.Fatalf("reload A -> B retained %T, want provider A", previous)
	}
	if registerErr := competitorA.RegisterFactoryBacked(liveA, factory); registerErr != nil {
		t.Fatalf("reload A -> B did not release generation A tool lease: %v", registerErr)
	}
	if closeErr := competitorA.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	select {
	case <-providerA.closed:
		t.Fatal("retained provider A was closed with its agent registry")
	default:
	}
	if response, chatErr := providerA.Chat(
		context.Background(),
		nil,
		nil,
		"",
		nil,
	); chatErr != nil || response == nil ||
		response.Content != "provider-a" {
		t.Fatalf("retained provider A is unusable: %#v, %v", response, chatErr)
	}

	_, releaseRuntime, err := al.acquireRuntimeUse(context.Background())
	if err != nil {
		t.Fatalf("acquireRuntimeUse() error = %v", err)
	}
	captured := al.GetRegistry().GetDefaultAgent()
	if captured == nil || captured.Provider != providerB {
		releaseRuntime()
		t.Fatalf("captured agent provider = %v, want provider B", captured)
	}
	liveB := &reloadToolRegistryLeaseProbe{marker: 11}
	if registerErr := captured.Tools.RegisterFactoryBacked(liveB, factory); registerErr != nil {
		releaseRuntime()
		t.Fatal(registerErr)
	}
	competitorB := tools.NewToolRegistry()
	if registerErr := competitorB.RegisterFactoryBacked(liveB, factory); registerErr == nil {
		releaseRuntime()
		t.Fatal("generation B compatibility lease was not retained")
	}

	rollbackDone := make(chan struct {
		provider providers.LLMProvider
		err      error
	}, 1)
	go func() {
		retained, reloadErr := al.ReloadProviderAndConfigRetainingPrevious(
			context.Background(),
			providerA,
			cfg,
		)
		rollbackDone <- struct {
			provider providers.LLMProvider
			err      error
		}{provider: retained, err: reloadErr}
	}()

	select {
	case result := <-rollbackDone:
		releaseRuntime()
		t.Fatalf("rollback returned before captured B runtime drained: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-providerB.closed:
		releaseRuntime()
		t.Fatal("provider B closed while its runtime generation was active")
	default:
	}

	releaseRuntime()
	var result struct {
		provider providers.LLMProvider
		err      error
	}
	select {
	case result = <-rollbackDone:
	case <-time.After(2 * time.Second):
		t.Fatal("rollback did not finish after runtime generation drained")
	}
	if result.err != nil {
		t.Fatalf("rollback B -> A error = %v", result.err)
	}
	if result.provider != providerB {
		t.Fatalf("rollback retained %T, want provider B", result.provider)
	}
	if registerErr := competitorB.RegisterFactoryBacked(liveB, factory); registerErr != nil {
		t.Fatalf("rollback did not release generation B tool lease: %v", registerErr)
	}
	if closeErr := competitorB.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	select {
	case <-providerB.closed:
		t.Fatal("retained provider B closed before explicit disposal")
	default:
	}

	al.CloseRetainedProvider(context.Background(), result.provider)
	select {
	case <-providerB.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("provider B was not closed after rollback and drain")
	}
}

type runtimeGateBlockingWorkflowTool struct {
	started chan struct{}
	release chan struct{}
}

func (t *runtimeGateBlockingWorkflowTool) Name() string {
	return "runtime_gate_block"
}

func (t *runtimeGateBlockingWorkflowTool) Description() string {
	return "blocks a workflow until released"
}

func (t *runtimeGateBlockingWorkflowTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (t *runtimeGateBlockingWorkflowTool) Execute(
	ctx context.Context,
	_ map[string]any,
) *tools.ToolResult {
	select {
	case <-t.started:
	default:
		close(t.started)
	}
	select {
	case <-t.release:
		return tools.NewToolResult("released")
	case <-ctx.Done():
		return tools.ErrorResult(ctx.Err().Error()).WithError(ctx.Err())
	}
}

func TestChannelWorkflowRetainsRuntimeAfterTriggerReturns(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	workspace := t.TempDir()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Workflows.Enabled = true
	providerA := &runtimeGateProvider{name: "provider-a", closed: make(chan struct{})}
	providerB := &runtimeGateProvider{name: "provider-b", closed: make(chan struct{})}
	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), providerA)
	defer al.Close()

	blockingTool := &runtimeGateBlockingWorkflowTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	al.GetRegistry().GetDefaultAgent().Tools.Register(blockingTool)
	workflowDir := filepath.Join(workspace, workflows.DefaultDefinitionsDir)
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "gate.yml"), []byte(`
name: Runtime gate
on:
  channel_message:
    channels: test
    passthrough: false
jobs:
  gate:
    runs-on: picoclaw
    steps:
      - uses: tool/runtime_gate_block
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workflows.RevalidateLocal(
		context.Background(),
		workspace,
		workflowRuntimeCompatibility(),
	); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}

	consumed := al.handleWorkflowTriggers(context.Background(), bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "test",
			ChatID:   "gate-chat",
			ChatType: "direct",
			SenderID: "user",
		},
		Content: "run gate workflow",
	})
	if !consumed {
		t.Fatal("handleWorkflowTriggers() consumed = false, want true")
	}
	select {
	case <-blockingTool.started:
	case <-time.After(2 * time.Second):
		t.Fatal("workflow tool did not start")
	}

	reloadDone := make(chan error, 1)
	cfgB := *cfg
	go func() {
		reloadDone <- al.ReloadProviderAndConfig(context.Background(), providerB, &cfgB)
	}()
	select {
	case err := <-reloadDone:
		t.Fatalf("reload returned while async workflow was active: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-providerA.closed:
		t.Fatal("provider A closed while async workflow retained its runtime")
	default:
	}

	close(blockingTool.release)
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadProviderAndConfig() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not finish after workflow release")
	}
	select {
	case <-providerA.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("provider A was not closed after workflow drained")
	}
}

func TestInboundMessageRetainsRoutingGenerationWhileWaitingForWorker(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	newConfig := func(agentID string) *config.Config {
		cfg := config.DefaultConfig()
		cfg.Agents.Defaults.Workspace = workspace
		cfg.Agents.Defaults.MaxParallelTurns = 1
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true},
			{ID: "alpha"},
			{ID: "beta"},
		}
		cfg.Agents.Dispatch = &config.DispatchConfig{
			Rules: []config.DispatchRule{{
				Name:  "generation-route",
				Agent: agentID,
				When:  config.DispatchSelector{Channel: "test"},
			}},
		}
		// Exercise the workflow-trigger decision immediately before normal
		// routing without adding a matching workflow that would itself retain
		// the generation.
		cfg.Workflows.Enabled = true
		return cfg
	}
	cfgA := newConfig("alpha")
	cfgB := newConfig("beta")
	providerA := &runtimeGateProvider{
		name:   "provider-a",
		closed: make(chan struct{}),
		called: make(chan struct{}, 1),
	}
	providerB := &runtimeGateProvider{
		name:   "provider-b",
		closed: make(chan struct{}),
		called: make(chan struct{}, 1),
	}
	msgBus := bus.NewMessageBus()
	al := newTestAgentLoopWithStrictModels(cfgA, msgBus, providerA)
	defer al.Close()

	// Saturate the only worker slot so the admitted message must wait after
	// its trigger decision, route resolution, and placeholder claim.
	al.workerSem <- struct{}{}
	workerSlotHeld := true
	defer func() {
		if workerSlotHeld {
			<-al.workerSem
		}
	}()

	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  "test",
			ChatID:   "generation-chat",
			ChatType: "direct",
			SenderID: "user",
		},
		Content: "keep one generation",
	}
	sessionKeyA, agentIDA, ok := al.resolveSteeringTarget(msg)
	if !ok || agentIDA != "alpha" {
		t.Fatalf("generation A route = %q, %v, want alpha", agentIDA, ok)
	}
	agentA, ok := al.GetRegistry().GetAgent("alpha")
	if !ok {
		t.Fatal("generation A alpha agent not found")
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() {
		runDone <- al.Run(runCtx)
	}()
	defer func() {
		cancelRun()
		al.Stop()
		select {
		case runErr := <-runDone:
			if runErr != nil {
				t.Errorf("AgentLoop.Run() error = %v", runErr)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("AgentLoop.Run() did not stop")
		}
	}()
	if err := msgBus.PublishInbound(context.Background(), msg); err != nil {
		t.Fatalf("PublishInbound() error = %v", err)
	}

	placeholderDeadline := time.After(2 * time.Second)
	for al.getActiveTurnState(sessionKeyA) == nil {
		select {
		case <-placeholderDeadline:
			t.Fatal("message did not claim its generation A session")
		case <-time.After(time.Millisecond):
		}
	}

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- al.ReloadProviderAndConfig(context.Background(), providerB, cfgB)
	}()
	select {
	case reloadErr := <-reloadDone:
		t.Fatalf("reload crossed queued generation A message: %v", reloadErr)
	case <-time.After(100 * time.Millisecond):
	}

	<-al.workerSem
	workerSlotHeld = false
	select {
	case <-providerA.called:
	case <-time.After(2 * time.Second):
		t.Fatal("queued message did not execute with generation A provider")
	}
	select {
	case reloadErr := <-reloadDone:
		if reloadErr != nil {
			t.Fatalf("ReloadProviderAndConfig() error = %v", reloadErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not finish after queued generation A message")
	}

	select {
	case <-providerB.called:
		t.Fatal("queued generation A message executed with generation B provider")
	default:
	}
	storeA, err := memory.NewSQLiteStore(filepath.Join(agentA.Workspace, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	if history, err := storeA.GetHistory(context.Background(), sessionKeyA); err != nil || len(history) == 0 {
		t.Fatal("generation A routed session has no message history")
	}
	sessionKeyB, agentIDB, ok := al.resolveSteeringTarget(msg)
	if !ok || agentIDB != "beta" {
		t.Fatalf("generation B route = %q, %v, want beta", agentIDB, ok)
	}
	agentB, ok := al.GetRegistry().GetAgent("beta")
	if !ok {
		t.Fatal("generation B beta agent not found")
	}
	storeB, err := memory.NewSQLiteStore(filepath.Join(agentB.Workspace, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()
	if history, err := storeB.GetHistory(context.Background(), sessionKeyB); err != nil || len(history) != 0 {
		t.Fatalf("generation B session history = %#v, want empty", history)
	}
	if state := al.getActiveTurnState(sessionKeyA); state != nil {
		t.Fatalf("generation A placeholder leaked after execution: %#v", state)
	}
}

func TestLegacySummarizerRejectsReloadedWorkspaceGeneration(t *testing.T) {
	t.Parallel()

	cfgA := config.DefaultConfig()
	cfgA.Agents.Defaults.Workspace = t.TempDir()
	cfgA.Agents.Defaults.SummarizeMessageThreshold = 1
	cfgB := config.DefaultConfig()
	cfgB.Agents.Defaults.Workspace = t.TempDir()
	cfgB.Agents.Defaults.SummarizeMessageThreshold = 1
	providerA := &runtimeGateProvider{
		name:   "provider-a",
		closed: make(chan struct{}),
		called: make(chan struct{}, 1),
	}
	providerB := &runtimeGateProvider{
		name:   "provider-b",
		closed: make(chan struct{}),
		called: make(chan struct{}, 1),
	}
	al := newTestAgentLoopWithStrictModels(cfgA, bus.NewMessageBus(), providerA)
	defer al.Close()
	manager := &legacyContextManager{al: al}
	const sessionKey = "summarizer-generation"
	history := []providers.Message{
		{Role: "user", Content: "old one"},
		{Role: "assistant", Content: "old two"},
		{Role: "user", Content: "old three"},
		{Role: "assistant", Content: "old four"},
		{Role: "user", Content: "old five"},
		{Role: "assistant", Content: "old six"},
	}
	agentA := al.GetRegistry().GetDefaultAgent()
	agentA.Sessions.SetHistory(sessionKey, history)
	if err := agentA.Sessions.Save(sessionKey); err != nil {
		t.Fatalf("save generation A history: %v", err)
	}

	resumeRuntime, err := al.PauseRuntimeForReload(context.Background())
	if err != nil {
		t.Fatalf("PauseRuntimeForReload() error = %v", err)
	}
	resumed := false
	defer func() {
		if !resumed {
			resumeRuntime()
		}
	}()
	manager.maybeSummarize(sessionKey)
	summarizeKey := agentA.ID + ":" + sessionKey
	if _, scheduled := manager.summarizing.Load(summarizeKey); !scheduled {
		t.Fatal("generation A summarization was not scheduled")
	}

	if err := al.ReloadProviderAndConfig(context.Background(), providerB, cfgB); err != nil {
		t.Fatalf("ReloadProviderAndConfig() error = %v", err)
	}
	agentB := al.GetRegistry().GetDefaultAgent()
	agentB.Sessions.SetHistory(sessionKey, history)
	if err := agentB.Sessions.Save(sessionKey); err != nil {
		t.Fatalf("save generation B history: %v", err)
	}

	resumeRuntime()
	resumed = true
	deadline := time.After(2 * time.Second)
	for {
		if _, scheduled := manager.summarizing.Load(summarizeKey); !scheduled {
			break
		}
		select {
		case <-deadline:
			t.Fatal("stale summarization did not leave the admission queue")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-providerB.called:
		t.Fatal("generation A summarization called generation B provider")
	default:
	}
	got := agentB.Sessions.GetHistory(sessionKey)
	if len(got) != len(history) {
		t.Fatalf("generation B history length = %d, want %d", len(got), len(history))
	}
	for i := range history {
		if got[i].Role != history[i].Role || got[i].Content != history[i].Content {
			t.Fatalf("generation B history[%d] = %#v, want %#v", i, got[i], history[i])
		}
	}
	if summary := agentB.Sessions.GetSummary(sessionKey); summary != "" {
		t.Fatalf("generation B summary = %q, want empty", summary)
	}
}

type delayedRuntimeGateSpawner struct {
	delegate *AgentLoopSpawner
	entered  chan struct{}
	admit    chan struct{}
}

func (s *delayedRuntimeGateSpawner) PrepareAsyncSubTurn(
	ctx context.Context,
) (context.Context, func(), error) {
	return s.delegate.PrepareAsyncSubTurn(ctx)
}

func (s *delayedRuntimeGateSpawner) SpawnSubTurn(
	ctx context.Context,
	cfg tools.SubTurnConfig,
) (*tools.ToolResult, error) {
	close(s.entered)
	<-s.admit
	return s.delegate.SpawnSubTurn(ctx, cfg)
}

func TestSpawnToolRetainsRuntimeBeforeLaunchingBackgroundSubturn(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	providerA := &runtimeGateProvider{name: "provider-a", closed: make(chan struct{})}
	providerB := &runtimeGateProvider{name: "provider-b", closed: make(chan struct{})}
	al := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), providerA)
	defer al.Close()

	rootCtx, releaseRoot, err := al.acquireRuntimeUse(context.Background())
	if err != nil {
		t.Fatalf("acquireRuntimeUse() error = %v", err)
	}
	parentAgent := al.GetRegistry().GetDefaultAgent()
	parent := &turnState{
		ctx:            rootCtx,
		turnID:         "runtime-gate-parent",
		agent:          parentAgent,
		session:        newEphemeralSession(nil),
		pendingResults: make(chan *tools.ToolResult, 4),
		concurrencySem: make(chan struct{}, 2),
		opts: processOptions{
			Dispatch: DispatchRequest{SessionKey: "runtime-gate-parent"},
		},
	}
	rootCtx = withTurnState(rootCtx, parent)
	rootCtx = WithAgentLoop(rootCtx, al)
	parent.ctx = rootCtx

	delayed := &delayedRuntimeGateSpawner{
		delegate: NewSubTurnSpawner(al),
		entered:  make(chan struct{}),
		admit:    make(chan struct{}),
	}
	spawnTool := tools.NewSpawnTool(tools.NewSubagentManager(
		providerA,
		parentAgent.Model,
		parentAgent.Workspace,
	))
	spawnTool.SetSpawner(delayed)
	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackDone := make(chan struct{})
	result := spawnTool.ExecuteAsync(
		rootCtx,
		map[string]any{"task": "finish", "agent_id": parentAgent.ID},
		func(context.Context, *tools.ToolResult) {
			close(callbackEntered)
			<-releaseCallback
			close(callbackDone)
		},
	)
	if result == nil || result.IsError {
		releaseRoot()
		t.Fatalf("SpawnTool result = %#v, want async acknowledgement", result)
	}
	select {
	case <-delayed.entered:
	case <-time.After(2 * time.Second):
		releaseRoot()
		t.Fatal("background spawn did not reach delayed admission")
	}
	releaseRoot()

	reloadDone := make(chan error, 1)
	cfgB := *cfg
	go func() {
		reloadDone <- al.ReloadProviderAndConfig(context.Background(), providerB, &cfgB)
	}()
	select {
	case err := <-reloadDone:
		t.Fatalf("reload returned before background subturn admission: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(delayed.admit)
	select {
	case <-callbackEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("background subturn callback did not start")
	}
	select {
	case err := <-reloadDone:
		t.Fatalf("reload returned before tracked callback completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseCallback)
	select {
	case <-callbackDone:
	case <-time.After(2 * time.Second):
		t.Fatal("background subturn did not complete while reload was paused")
	}
	select {
	case err := <-reloadDone:
		if err != nil {
			t.Fatalf("ReloadProviderAndConfig() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not finish after background subturn")
	}
	select {
	case <-providerA.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("provider A was not closed after background subturn drained")
	}
}
