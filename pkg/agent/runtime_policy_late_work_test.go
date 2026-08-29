package agent

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type p015B3ALateSteeringProvider struct {
	mu    sync.Mutex
	calls int
}

func (provider *p015B3ALateSteeringProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls++
	return &providers.LLMResponse{
		Content: fmt.Sprintf("P015B3A_RESCUE_RESPONSE_%d_46c2", provider.calls),
	}, nil
}

func (provider *p015B3ALateSteeringProvider) callCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.calls
}

func TestP015B3ATrackedLateWorkPolicyMatrix(t *testing.T) {
	enabled := logger.NewDiagnosticPolicy(true, logger.DEBUG)
	disabled := logger.NewDiagnosticPolicy(false, logger.DEBUG)
	for _, test := range []struct {
		name          string
		origin        logger.DiagnosticPolicy
		current       logger.DiagnosticPolicy
		missingOrigin bool
	}{
		{name: "true_to_false", origin: enabled, current: disabled},
		{name: "false_to_true", origin: disabled, current: enabled},
		{name: "missing_to_true", current: enabled, missingOrigin: true},
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
			origin, err := loop.runtimeDiagnosticOriginFromLease(rootCtx)
			if err != nil {
				releaseRoot()
				t.Fatal(err)
			}
			source := &turnState{
				turnID:           "late-source",
				agentID:          "main",
				sessionKey:       "late-session",
				channel:          "test",
				chatID:           "late-chat",
				diagnosticPolicy: logger.DiagnosticPolicyFromContext(rootCtx),
			}
			route, err := snapshotTrackedSubagentResultRoute(source)
			if err != nil {
				releaseRoot()
				t.Fatal(err)
			}
			if route.diagnosticOrigin != origin ||
				cloneTrackedSubagentResultRoute(route).diagnosticOrigin != origin {
				releaseRoot()
				t.Fatal("tracked route did not preserve exact opaque origin")
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
			want := test.origin.Meet(test.current)
			trackedCtx, releaseTracked, err := loop.acquireRuntimeUseFromOrigin(
				context.Background(),
				route.diagnosticOrigin,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := logger.DiagnosticPolicyFromContext(trackedCtx); got != want {
				releaseTracked()
				t.Fatalf("tracked late policy = %#v, want %#v", got, want)
			}
			releaseTracked()

			steeringCtx, releaseSteering, err := loop.acquireSteeringRuntimeUse(
				context.Background(),
				&origin,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := logger.DiagnosticPolicyFromContext(steeringCtx); got != want {
				releaseSteering()
				t.Fatalf("steering late policy = %#v, want %#v", got, want)
			}
			releaseSteering()

			detachedCtx, releaseDetached, err := loop.acquireRuntimeUseFromOrigin(
				context.Background(),
				runtimeDiagnosticOrigin{},
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := logger.DiagnosticPolicyFromContext(detachedCtx); got !=
				(logger.DiagnosticPolicy{}) {
				releaseDetached()
				t.Fatalf("missing tracked origin gained policy %#v", got)
			}
			releaseDetached()

			providerA := newTrackedSubagentRuntimeProvider("late-generation-a")
			fixture := newTrackedSubagentRuntimeFixtureWithPolicy(
				t,
				providerA,
				test.origin,
			)
			providerB := newTrackedSubagentRuntimeProvider("late-generation-b")
			cfgBMailbox := fixture.newConfigB(fixture.rootWorkspace, true)
			ensureStrictTestModelSelection(cfgBMailbox, providerB)
			callbackCtx, releaseCallback, callbackErr := fixture.loop.acquireTrustedRuntimeRoot(
				context.Background(),
			)
			if callbackErr != nil {
				t.Fatal(callbackErr)
			}
			callbackOrigin := runtimeDiagnosticOrigin{}
			if !test.missingOrigin {
				callbackOrigin, callbackErr = fixture.loop.runtimeDiagnosticOriginFromLease(
					callbackCtx,
				)
				if callbackErr != nil {
					releaseCallback()
					t.Fatal(callbackErr)
				}
			}
			fixture.route.diagnosticOrigin = callbackOrigin
			reloadDone := make(chan error, 1)
			go func() {
				reloadDone <- fixture.loop.ReloadProviderAndConfigWithRuntimePolicies(
					context.Background(),
					providerB,
					cfgBMailbox,
					isolation.NewExecutionPolicy(cfgBMailbox.Isolation),
					test.current,
				)
			}()
			waitForRecursionReloadPause(t, fixture.loop, reloadDone)
			fixture.publishPendingResult(t)
			releaseCallback()
			waitForTrackedSubagentRuntimeReload(t, reloadDone)
			mailboxCall := waitForTrackedSubagentRuntimeCall(t, providerB)
			wantMailbox := logger.DiagnosticPolicy{}
			if !test.missingOrigin {
				wantMailbox = test.origin.Meet(test.current)
			}
			if mailboxCall.policy != wantMailbox {
				t.Fatalf(
					"mailbox late policy = %#v, want %#v",
					mailboxCall.policy,
					wantMailbox,
				)
			}
			waitForTrackedSubagentRuntimeRecord(
				t,
				fixture.loop,
				fixture.recordID,
				trackedSubagentResultClaimed,
				true,
			)
			if err := fixture.loop.steering.pushScope(
				fixture.route.RootSessionKey,
				providers.Message{Role: "user", Content: "late steering rescue"},
			); err != nil {
				t.Fatal(err)
			}
			fixture.loop.runTrackedSubagentSteeringRescue(
				trackedSubagentResultScope{
					AgentID:    fixture.route.RootAgentID,
					SessionKey: fixture.route.RootSessionKey,
				},
				fixture.route,
			)
			rescueCall := waitForTrackedSubagentRuntimeCall(t, providerB)
			if rescueCall.policy != wantMailbox {
				t.Fatalf(
					"tracked rescue policy = %#v, want %#v",
					rescueCall.policy,
					wantMailbox,
				)
			}
		})
	}
}

func TestP015B3ASteeringRescueOriginCapsEveryContinuation(t *testing.T) {
	const (
		sessionKey = "p015b3a:late-steering"
		firstUser  = "P015B3A_RESCUE_USER_1_19ad"
		secondUser = "P015B3A_RESCUE_USER_2_5e70"
		firstReply = "P015B3A_RESCUE_RESPONSE_1_46c2"
		lastReply  = "P015B3A_RESCUE_RESPONSE_2_46c2"
	)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &p015B3ALateSteeringProvider{}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	loop.mu.Lock()
	loop.diagnosticPolicy = logger.NewDiagnosticPolicy(true, logger.DEBUG)
	loop.mu.Unlock()
	defer func() {
		loop.Close()
		messageBus.Close()
	}()
	if _, _, err := loop.pushSteeringMessage(
		sessionKey,
		providers.Message{Role: "user", Content: firstUser},
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loop.pushSteeringMessage(
		sessionKey,
		providers.Message{Role: "user", Content: secondUser},
	); err != nil {
		t.Fatal(err)
	}
	falseOrigin := runtimeDiagnosticOrigin{
		policy: logger.NewDiagnosticPolicy(false, logger.DEBUG),
		valid:  true,
	}
	var (
		response string
		runErr   error
	)
	_, raw := captureP015HookRecords(t, func() {
		response, runErr = loop.drainQueuedSteeringContinuations(
			context.Background(),
			&continuationTarget{
				SessionKey: sessionKey,
				Channel:    "cli",
				ChatID:     "p015b3a-late-steering",
			},
			&falseOrigin,
		)
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if response != lastReply || provider.callCount() != 2 {
		t.Fatalf("drained rescue response/calls = %q/%d, want %q/2",
			response, provider.callCount(), lastReply)
	}
	for _, canary := range []string{firstUser, secondUser, firstReply, lastReply} {
		if bytes.Contains(raw, []byte(canary)) {
			t.Errorf("late false origin widened during rescue continuation: %q", canary)
		}
	}
}
