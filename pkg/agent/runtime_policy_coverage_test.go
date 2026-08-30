package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/modelrouter"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type runtimePolicyNonComparableProvider []string

func (runtimePolicyNonComparableProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "unused"}, nil
}

func TestRuntimeProviderGenerationCoverageEdges(t *testing.T) {
	if snapshotAgentRegistryProviderGeneration(nil) != nil {
		t.Fatal("nil registry produced a provider generation")
	}
	registry := &AgentRegistry{agents: map[string]*AgentInstance{"nil-agent": nil}}
	if generation := snapshotAgentRegistryProviderGeneration(registry); generation == nil {
		t.Fatal("sparse registry did not produce an empty generation")
	}

	var absent *agentRegistryProviderGeneration
	if absent.directForAgent("missing") != (agentDirectProviderBindings{}) ||
		absent.bindingsForAgent("missing") != nil ||
		absent.constructorProvider() != nil || absent.legacyRetainedProvider() != nil ||
		absent.providerSet() != nil {
		t.Fatal("nil provider generation exposed bindings")
	}
	absent.closeAll()
	absent.closeAllExcept(nil)

	primary := &p015B3ACountedProvider{name: "coverage-primary"}
	secondary := &p015B3ACountedProvider{name: "coverage-secondary"}
	withDefault := &agentRegistryProviderGeneration{defaultProvider: primary}
	if withDefault.constructorProvider() != primary ||
		withDefault.legacyRetainedProvider() != primary {
		t.Fatal("default provider fallback was not selected")
	}
	withOrdered := &agentRegistryProviderGeneration{
		orderedProviders: []providers.LLMProvider{primary, secondary},
	}
	if withOrdered.constructorProvider() != primary ||
		withOrdered.legacyRetainedProvider() != primary {
		t.Fatal("ordered provider fallback was not selected")
	}
	if (&agentRegistryProviderGeneration{}).constructorProvider() != nil {
		t.Fatal("empty provider generation selected a constructor provider")
	}
	if !sameLLMProvider(nil, nil) || sameLLMProvider(nil, primary) ||
		sameLLMProvider(
			runtimePolicyNonComparableProvider{"a"},
			runtimePolicyNonComparableProvider{"a"},
		) {
		t.Fatal("provider identity comparison accepted an invalid shape")
	}

	withOrdered.closeAllExcept(primary)
	if primary.closes.Load() != 0 || secondary.closes.Load() != 1 {
		t.Fatalf(
			"closeAllExcept counts = primary:%d secondary:%d",
			primary.closes.Load(),
			secondary.closes.Load(),
		)
	}
	withOrdered.closeAll()
	if primary.closes.Load() != 1 || secondary.closes.Load() != 2 {
		t.Fatal("closeAll did not visit the complete provider set")
	}
}

func TestRuntimeReloadTransactionCoverageEdges(t *testing.T) {
	var nilLoop *AgentLoop
	if _, err := nilLoop.SnapshotRuntimeGenerationIdentity(); err == nil ||
		nilLoop.runtimeGenerationIdentityMatches(RuntimeGenerationIdentity{}) {
		t.Fatal("nil loop accepted a runtime identity")
	}
	if _, err := nilLoop.BeginRuntimeReloadTransaction(RuntimeGenerationIdentity{}); err == nil {
		t.Fatal("nil loop began a reload transaction")
	}

	var nilTransaction *RuntimeReloadTransaction
	if nilTransaction.InitialConfig() != nil {
		t.Fatal("nil transaction exposed an initial config")
	}
	if _, err := nilTransaction.PublishRetainingPrevious(
		context.Background(), nil, nil, isolation.ExecutionPolicy{}, logger.DiagnosticPolicy{},
	); err == nil {
		t.Fatal("nil transaction published a generation")
	}
	if _, err := nilTransaction.Rollback(context.Background(), nil); err == nil {
		t.Fatal("nil transaction rolled back")
	}
	if err := nilTransaction.CommitRetained(context.Background(), nil); err == nil {
		t.Fatal("nil transaction committed")
	}
	nilTransaction.Close()

	cfg := &config.Config{}
	inactive := &RuntimeReloadTransaction{current: RuntimeGenerationIdentity{cfg: cfg}}
	if inactive.InitialConfig() != nil {
		t.Fatal("inactive transaction exposed its config")
	}
	if _, err := inactive.PublishRetainingPrevious(
		context.Background(), nil, cfg, isolation.ExecutionPolicy{}, logger.DiagnosticPolicy{},
	); err == nil {
		t.Fatal("inactive transaction published")
	}
	if _, err := inactive.Rollback(context.Background(), nil); err == nil {
		t.Fatal("inactive transaction rolled back")
	}
	if err := inactive.CommitRetained(context.Background(), nil); err == nil {
		t.Fatal("inactive transaction committed")
	}
	inactive.Close()

	owner := &AgentLoop{}
	pending := &RetainedRuntimeGeneration{}
	transaction := &RuntimeReloadTransaction{
		owner: owner, active: true, current: RuntimeGenerationIdentity{owner: owner, id: 1, cfg: cfg},
		pending: pending,
	}
	if transaction.InitialConfig() != cfg {
		t.Fatal("active transaction lost its initial config")
	}
	if _, err := transaction.PublishRetainingPrevious(
		context.Background(), nil, cfg, isolation.ExecutionPolicy{}, logger.DiagnosticPolicy{},
	); err == nil {
		t.Fatal("transaction accepted a second pending generation")
	}
	other := &RetainedRuntimeGeneration{}
	if _, err := transaction.Rollback(context.Background(), other); err == nil {
		t.Fatal("transaction accepted a foreign retained generation")
	}
	if err := transaction.CommitRetained(context.Background(), other); err == nil {
		t.Fatal("transaction committed a foreign retained generation")
	}
	if err := pending.beginUse(transaction, transaction.current); err == nil {
		t.Fatal("incomplete retained generation began use")
	}
	var nilRetained *RetainedRuntimeGeneration
	if err := nilRetained.beginUse(transaction, transaction.current); err == nil {
		t.Fatal("nil retained generation began use")
	}
	nilRetained.finishUse(true)
	pending.finishUse(false)
	pending.state = retainedRuntimeGenerationInUse
	pending.finishUse(false)
	if pending.state != retainedRuntimeGenerationAvailable {
		t.Fatal("failed retained use did not become available")
	}
	pending.state = retainedRuntimeGenerationInUse
	pending.finishUse(true)
	if pending.state != retainedRuntimeGenerationConsumed {
		t.Fatal("successful retained use was not consumed")
	}

	owner.reloadMu.Lock()
	transaction.pending = pending
	transaction.Close()
	transaction.Close()

	owner2 := &AgentLoop{}
	owner2.reloadMu.Lock()
	closedWithInUse := &RuntimeReloadTransaction{
		owner: owner2, active: true,
		pending: &RetainedRuntimeGeneration{state: retainedRuntimeGenerationInUse},
	}
	closedWithInUse.Close()
}

func TestRuntimeGateCoverageEdges(t *testing.T) {
	if _, ok := runtimeLeaseContextFrom(nil); ok {
		t.Fatal("nil context exposed a runtime lease")
	}
	var nilBoundary *runtimeLeaseBoundary
	if !nilBoundary.live() {
		t.Fatal("nil boundary should terminate a live ancestry walk")
	}
	deep := newRuntimeLeaseBoundary(nil)
	for range 65 {
		deep = newRuntimeLeaseBoundary(deep)
	}
	if deep.live() {
		t.Fatal("over-depth boundary ancestry remained live")
	}
	var nilLoop *AgentLoop
	if _, err := nilLoop.snapshotRuntimeGeneration(); err == nil {
		t.Fatal("nil loop snapshotted a generation")
	}
	if _, _, err := nilLoop.acquireCountedRuntimeGeneration(
		context.Background(), runtimeLeaseKindDetached, nil, bindDetachedRuntimeDiagnostic,
	); err == nil {
		t.Fatal("nil loop admitted a counted generation")
	}
	if _, _, err := nilLoop.AcquireRuntimeStartupUse(context.Background(), &config.Config{}); err == nil {
		t.Fatal("nil loop admitted startup")
	}
	if _, _, err := nilLoop.retainRuntimeUse(context.Background()); err == nil {
		t.Fatal("nil loop retained runtime work")
	}
	if err := nilLoop.WithPausedRuntimeGeneration(context.Background(), func(context.Context) error {
		return nil
	}); err == nil {
		t.Fatal("nil loop ran a paused callback")
	}
	if _, _, err := nilLoop.pauseRuntimeUsesWithContext(
		context.Background(), context.Background(),
	); err == nil {
		t.Fatal("nil loop paused runtime work")
	}
	nilLoop.ReleaseRuntimeStartupBarrier()

	cfg := &config.Config{}
	registry := &AgentRegistry{cfg: cfg, agents: map[string]*AgentInstance{}}
	loop := &AgentLoop{cfg: cfg, registry: registry, runtimeGenerationID: 1}
	loop.releaseRuntimeAdmissionCount()
	paused := &AgentLoop{runtimeGatePaused: true}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := paused.incrementRuntimeAdmission(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("paused canceled admission error = %v", err)
	}
	paused.runtimeGateStopped = true
	if err := paused.incrementRuntimeAdmission(context.Background()); !errors.Is(
		err,
		errAgentRuntimeStopped,
	) {
		t.Fatalf("paused stopped admission error = %v", err)
	}
	stopped := &AgentLoop{runtimeGateStopped: true}
	if err := stopped.incrementRuntimeAdmission(context.Background()); !errors.Is(
		err,
		errAgentRuntimeStopped,
	) {
		t.Fatalf("stopped admission error = %v", err)
	}

	lease, releaseLease, leaseErr := loop.acquireTrustedRuntimeRoot(nil)
	if leaseErr != nil {
		t.Fatal(leaseErr)
	}
	if _, _, err := loop.acquireInheritedRuntimeUse(context.Background()); err == nil {
		releaseLease()
		t.Fatal("missing inherited lease was accepted")
	}
	missingOrigin, revokeMissing, originErr := loop.acquireRuntimeUseFromOrigin(
		lease,
		runtimeDiagnosticOrigin{},
	)
	if originErr != nil || logger.DiagnosticPolicyFromContext(missingOrigin) !=
		(logger.DiagnosticPolicy{}) {
		releaseLease()
		t.Fatalf("missing nested origin = %v", originErr)
	}
	revokeMissing()
	detached, revokeDetached, detachedErr := loop.acquireDetachedRuntimeUse(lease)
	if detachedErr != nil || logger.DiagnosticPolicyFromContext(detached) !=
		(logger.DiagnosticPolicy{}) {
		releaseLease()
		t.Fatalf("nested detached admission = %v", detachedErr)
	}
	revokeDetached()
	releaseLease()
	standaloneDetached, releaseStandaloneDetached, standaloneErr := loop.acquireDetachedRuntimeUse(nil)
	if standaloneErr != nil || runtimeLeaseOwner(standaloneDetached) != loop {
		t.Fatalf("standalone detached admission = %v", standaloneErr)
	}
	releaseStandaloneDetached()

	pauseBoundary := newRuntimeLeaseBoundary(nil)
	pauseCtx := context.WithValue(
		context.Background(),
		runtimeLeaseContextKey{},
		runtimeLeaseContextValue{
			owner: loop, boundary: pauseBoundary, kind: runtimeLeaseKindPauseOwner,
		},
	)
	if _, _, err := loop.acquireTrustedRuntimeRoot(pauseCtx); err == nil {
		t.Fatal("pause owner entered trusted-root admission")
	}
	if _, _, err := loop.acquireRuntimeUseFromOrigin(
		pauseCtx,
		runtimeDiagnosticOrigin{},
	); err == nil {
		t.Fatal("pause owner entered origin/current admission")
	}
	if _, _, err := loop.acquireDetachedRuntimeUse(pauseCtx); err == nil {
		t.Fatal("pause owner entered detached admission")
	}
	if _, _, err := loop.retainRuntimeUse(pauseCtx); err == nil {
		t.Fatal("pause owner was retained")
	}
	if err := loop.WithPausedRuntimeGeneration(pauseCtx, nil); err == nil {
		t.Fatal("nil paused callback ran")
	}
	if err := loop.WithPausedRuntimeGeneration(context.Background(), func(context.Context) error {
		return nil
	}); err == nil {
		t.Fatal("unowned paused callback ran")
	}

	if _, _, err := loop.AcquireRuntimeStartupUse(context.Background(), nil); err == nil {
		t.Fatal("startup accepted nil expected config")
	}
	if _, _, err := loop.AcquireRuntimeStartupUse(canceled, cfg); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("startup canceled error = %v", err)
	}
	if _, _, err := loop.AcquireRuntimeStartupUse(pauseCtx, cfg); err == nil {
		t.Fatal("startup accepted an owned context")
	}
	if _, _, err := loop.AcquireRuntimeStartupUse(context.Background(), cfg); err == nil {
		t.Fatal("startup ran without its exclusive barrier")
	}
	if _, _, err := loop.AcquireRuntimeGeneration(context.Background(), nil); err == nil {
		t.Fatal("exact generation accepted nil config")
	}
	if exactNil, releaseExactNil, err := loop.AcquireRuntimeGeneration(nil, cfg); err != nil {
		t.Fatal(err)
	} else {
		releaseExactNil()
		if runtimeLeaseOwner(exactNil) != nil {
			t.Fatal("released nil-context exact generation retained ownership")
		}
	}
	liveExact, releaseLiveExact, liveExactErr := loop.acquireTrustedRuntimeRoot(context.Background())
	if liveExactErr != nil {
		t.Fatal(liveExactErr)
	}
	if _, _, err := loop.AcquireRuntimeGeneration(liveExact, &config.Config{}); err == nil {
		releaseLiveExact()
		t.Fatal("live exact generation accepted mismatched config")
	}
	releaseLiveExact()
	if originNil, releaseOriginNil, err := loop.acquireRuntimeUseFromOrigin(
		nil,
		runtimeDiagnosticOrigin{},
	); err != nil {
		t.Fatal(err)
	} else {
		releaseOriginNil()
		if runtimeLeaseOwner(originNil) != nil {
			t.Fatal("released nil-context origin lease retained ownership")
		}
	}
	foreignOwner := &AgentLoop{}
	foreignBoundary := newRuntimeLeaseBoundary(nil)
	foreignContext := context.WithValue(
		context.Background(),
		runtimeLeaseContextKey{},
		runtimeLeaseContextValue{
			owner:      foreignOwner,
			generation: &runtimeGeneration{id: 1, cfg: cfg, registry: registry},
			boundary:   foreignBoundary,
			kind:       runtimeLeaseKindTrustedRoot,
		},
	)
	if _, _, err := loop.acquireCountedRuntimeGeneration(
		foreignContext,
		runtimeLeaseKindDetached,
		nil,
		bindDetachedRuntimeDiagnostic,
	); err == nil {
		t.Fatal("direct counted admission accepted foreign ownership")
	}

	canceledWait, cancelWait := context.WithCancel(context.Background())
	cancelWait()
	if _, err := loop.pauseRuntimeUses(canceledWait); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pause error = %v", err)
	}
	owned, releaseOwned, ownedErr := loop.acquireTrustedRuntimeRoot(context.Background())
	if ownedErr != nil {
		t.Fatal(ownedErr)
	}
	if _, _, err := loop.pauseRuntimeUsesWithContext(
		context.Background(),
		owned,
	); err == nil {
		releaseOwned()
		t.Fatal("pause accepted an owned runtime context")
	}
	releaseOwned()
	canceledRuntime, cancelRuntime := context.WithCancel(context.Background())
	cancelRuntime()
	if _, _, err := loop.pauseRuntimeUsesWithContext(
		context.Background(),
		canceledRuntime,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled runtime pause error = %v", err)
	}
	if _, _, err := loop.pauseRuntimeUsesWithContext(
		canceledWait,
		nil,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("nil runtime/canceled wait pause error = %v", err)
	}
	resume, pauseErr := loop.pauseRuntimeUses(nil)
	if pauseErr != nil {
		t.Fatal(pauseErr)
	}
	resume()

	startupLoop := &AgentLoop{
		cfg: cfg, registry: registry, runtimeGenerationID: 1,
		runtimeStartupBarrier: true, runtimeGatePaused: true, runtimeGatePauses: 1,
	}
	if _, _, err := startupLoop.AcquireRuntimeStartupUse(nil, &config.Config{}); err == nil {
		t.Fatal("startup accepted a mismatched generation config")
	}
	incompleteStartup := &AgentLoop{
		cfg: cfg, runtimeGenerationID: 1,
		runtimeStartupBarrier: true, runtimeGatePaused: true, runtimeGatePauses: 1,
	}
	if _, _, err := incompleteStartup.AcquireRuntimeStartupUse(nil, cfg); err == nil {
		t.Fatal("startup accepted an incomplete generation")
	}
	incompletePauseBoundary := newRuntimeLeaseBoundary(nil)
	incompletePause := context.WithValue(
		context.Background(),
		runtimeLeaseContextKey{},
		runtimeLeaseContextValue{
			owner:    incompleteStartup,
			boundary: incompletePauseBoundary,
			kind:     runtimeLeaseKindPauseOwner,
		},
	)
	if err := incompleteStartup.WithPausedRuntimeGeneration(
		incompletePause,
		func(context.Context) error { return nil },
	); err == nil {
		t.Fatal("paused callback accepted an incomplete generation")
	}
	loop.ReleaseRuntimeStartupBarrier()
}

func TestControllerRuntimeBoundaryCoverageEdges(t *testing.T) {
	var nilLoop *AgentLoop
	if _, err := nilLoop.NewControllerLocalRepairRunnerWithRuntimeLease(
		context.Background(), "main", "route",
	); err == nil || nilLoop.ControllerLocalRepairReadyWithRuntimeLease(
		context.Background(), "main",
	) {
		t.Fatal("nil loop exposed strict local repair")
	}
	if _, err := nilLoop.NewControllerLocalReviewRunner("main"); err == nil ||
		nilLoop.ControllerLocalReviewReady("main") ||
		nilLoop.ControllerLocalReviewReadyWithRuntimeLease(context.Background(), "main") {
		t.Fatal("nil loop exposed controller review")
	}
	incompleteReviewLoop := &AgentLoop{}
	if _, err := incompleteReviewLoop.NewControllerLocalReviewRunner("main"); err == nil ||
		incompleteReviewLoop.ControllerLocalReviewReady("main") {
		t.Fatal("incomplete loop exposed controller review")
	}
	var nilReview *ControllerLocalReviewRunner
	if _, err := nilReview.Run(context.Background(), ControllerLocalReviewRequest{}); !errors.Is(
		err,
		ErrControllerLocalReviewUnavailable,
	) {
		t.Fatalf("nil review runner error = %v", err)
	}
	if controllerLocalReviewAgentReady(nil, nil, "main") ||
		controllerLocalReviewAgentReady(&AgentLoop{}, &AgentInstance{ID: "main"}, "main") {
		t.Fatal("incomplete controller review agent was ready")
	}
	invalidAgent := &AgentInstance{
		ID:             "main",
		ContextBuilder: &ContextBuilder{},
		MaxTokens:      1,
		Temperature:    math.NaN(),
	}
	if controllerLocalReviewAgentReady(&AgentLoop{}, invalidAgent, "main") {
		t.Fatal("NaN controller review temperature was ready")
	}
	if validControllerLocalReviewOutcome("unknown") ||
		validControllerLocalReviewSeverity("unknown") {
		t.Fatal("unknown controller review enum was accepted")
	}
	reviewLoop, reviewAgent, _, _ := newWorkflowReadOnlyTestLoop(
		t,
		&workflowReadOnlyCaptureProvider{},
	)
	if _, err := reviewLoop.NewControllerLocalReviewRunner(" main"); err == nil {
		t.Fatal("inexact controller review agent ID was accepted")
	}
	if reviewLoop.ControllerLocalReviewReady(" main") {
		t.Fatal("inexact controller review agent ID was ready")
	}
	reviewLease, releaseReviewLease, reviewLeaseErr := reviewLoop.acquireTrustedRuntimeRoot(
		context.Background(),
	)
	if reviewLeaseErr != nil {
		t.Fatal(reviewLeaseErr)
	}
	if _, err := reviewLoop.NewControllerLocalReviewRunnerWithRuntimeLease(
		reviewLease,
		"missing",
	); err == nil {
		releaseReviewLease()
		t.Fatal("strict controller review accepted a missing agent")
	}
	if reviewLoop.ControllerLocalReviewReadyWithRuntimeLease(
		context.Background(),
		"main",
	) {
		releaseReviewLease()
		t.Fatal("controller review readiness accepted a missing lease")
	}
	generation, generationErr := reviewLoop.runtimeGenerationFromLease(reviewLease)
	if generationErr != nil {
		releaseReviewLease()
		t.Fatal(generationErr)
	}
	mismatchedReview := &ControllerLocalReviewRunner{
		loop: reviewLoop, agent: reviewAgent, agentID: "missing",
		generationID: generation.id, strict: true,
	}
	if _, err := mismatchedReview.Run(
		reviewLease,
		ControllerLocalReviewRequest{Context: "valid context"},
	); !errors.Is(err, ErrControllerLocalReviewUnavailable) {
		releaseReviewLease()
		t.Fatalf("mismatched strict review error = %v", err)
	}
	releaseReviewLease()
	compatReview, compatibilityErr := reviewLoop.NewControllerLocalReviewRunner("main")
	if compatibilityErr != nil {
		t.Fatal(compatibilityErr)
	}
	if _, err := compatReview.Run(nil, ControllerLocalReviewRequest{}); !errors.Is(
		err,
		ErrControllerLocalReviewInvalid,
	) {
		t.Fatalf("nil-context invalid review error = %v", err)
	}
	if (&ControllerLocalReviewRunner{loop: &AgentLoop{}, agent: reviewAgent, agentID: "main"}).
		isCurrentGenerationAgent() {
		t.Fatal("review runner accepted missing current generation")
	}
	withoutCandidates := *reviewAgent
	withoutCandidates.Candidates = nil
	withoutCandidates.LightCandidates = nil
	if controllerLocalReviewAgentReady(reviewLoop, &withoutCandidates, withoutCandidates.ID) {
		t.Fatal("review agent without candidates was ready")
	}
	lightOnly := *reviewAgent
	lightOnly.LightCandidates = append(
		[]providers.FallbackCandidate(nil),
		lightOnly.Candidates...,
	)
	lightOnly.Candidates = nil
	if !controllerLocalReviewAgentReady(reviewLoop, &lightOnly, lightOnly.ID) {
		t.Fatal("valid light-only review agent was not ready")
	}

	if firstRepairCandidate(nil) != nil ||
		controllerRepairCandidateReady(nil, nil, nil, nil) ||
		controllerRepairExactPrimary(nil, providers.FallbackCandidate{}) {
		t.Fatal("incomplete local repair candidate was ready")
	}
	if controllerLocalRepairReadyForGeneration(nil, nil, nil, " main") {
		t.Fatal("invalid local repair generation was ready")
	}
	if _, err := (&AgentLoop{}).newControllerLocalRepairRunner(
		nil, nil, nil, 0, true, " main", "route",
	); err == nil || !strings.Contains(err.Error(), "exact and canonical") {
		t.Fatalf("invalid local repair identity error = %v", err)
	}
	workspaces := &localRepairTestAcquirer{}
	provider := &controllerRepairFactoryProvider{name: "coverage"}
	validCandidate := controllerRepairFactoryCandidate(
		"account-a", "coding", "openai", "gpt-coverage",
	)
	agent := &AgentInstance{
		ID: "repairer", Model: "coding",
		Candidates: []providers.FallbackCandidate{validCandidate},
		Provider:   provider, MaxIterations: 1, MaxTokens: 1,
	}
	registry := &AgentRegistry{agents: map[string]*AgentInstance{"repairer": agent}}
	loop := &AgentLoop{}
	for name, run := range map[string]func() error{
		"config": func() error {
			_, err := loop.newControllerLocalRepairRunner(
				nil, registry, workspaces, 1, true, "repairer", "route",
			)
			return err
		},
		"registry": func() error {
			_, err := loop.newControllerLocalRepairRunner(
				&config.Config{}, nil, workspaces, 1, true, "repairer", "route",
			)
			return err
		},
		"workspaces": func() error {
			_, err := loop.newControllerLocalRepairRunner(
				&config.Config{}, registry, nil, 1, true, "repairer", "route",
			)
			return err
		},
		"missing agent": func() error {
			_, err := loop.newControllerLocalRepairRunner(
				&config.Config{}, &AgentRegistry{agents: map[string]*AgentInstance{}},
				workspaces, 1, true, "repairer", "route",
			)
			return err
		},
		"configuration error": func() error {
			agent.ConfigurationError = errors.New("invalid")
			defer func() { agent.ConfigurationError = nil }()
			_, err := loop.newControllerLocalRepairRunner(
				&config.Config{}, registry, workspaces, 1, true, "repairer", "route",
			)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("unavailable local repair dependency was accepted")
			}
		})
	}
	if (&AgentLoop{}).ControllerLocalRepairReadyWithRuntimeLease(
		context.Background(),
		"repairer",
	) {
		t.Fatal("missing runtime lease reported local repair ready")
	}
	invalidModel := validCandidate
	invalidModel.Model = " gpt-coverage"
	if controllerRepairCandidateReady(nil, workspaces, agent, &invalidModel) {
		t.Fatal("inexact local repair model was ready")
	}
	agent.Provider = nil
	if controllerRepairCandidateReady(nil, workspaces, agent, &validCandidate) {
		t.Fatal("missing local repair provider was ready")
	}
	agent.Provider = provider
	routerConfig := config.ModelRouterConfig{
		Name: "empty-router", Enabled: true,
		Blocks: []config.ModelRouterBlock{
			{ID: "not-model", Type: config.ModelRouterBlockTypeRules},
			{ID: "empty", Type: config.ModelRouterBlockTypeModel},
			{ID: "duplicate-a", Type: config.ModelRouterBlockTypeModel, Model: "coding"},
			{ID: "duplicate-b", Type: config.ModelRouterBlockTypeModel, Model: "coding"},
		},
	}
	agent.ModelRouter = modelrouter.New(routerConfig.Name, &routerConfig)
	if controllerLocalRepairReadyForGeneration(
		&config.Config{},
		registry,
		workspaces,
		"repairer",
	) {
		t.Fatal("unresolvable model router reported local repair ready")
	}
}

func TestControllerLocalReviewParserCoverageEdges(t *testing.T) {
	decode := func(raw string) *json.Decoder {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		return decoder
	}
	if err := consumeControllerLocalReviewJSONValue(decode("0"), 0); err != nil {
		t.Fatalf("scalar JSON value error = %v", err)
	}
	for name, run := range map[string]func() error{
		"depth": func() error {
			return consumeControllerLocalReviewJSONValue(
				decode("{}"),
				maxControllerLocalReviewJSONDepth+1,
			)
		},
		"empty token": func() error {
			return consumeControllerLocalReviewJSONValue(decode(""), 0)
		},
		"duplicate key": func() error {
			return consumeControllerLocalReviewJSONValue(decode(`{"a":1,"a":2}`), 0)
		},
		"truncated object value": func() error {
			return consumeControllerLocalReviewJSONValue(decode(`{"a":`), 0)
		},
		"truncated array": func() error {
			return consumeControllerLocalReviewJSONValue(decode(`[1`), 0)
		},
		"wrong delimiter": func() error {
			return requireControllerLocalReviewJSONDelimiter(decode(`[]`), '}')
		},
		"missing delimiter": func() error {
			return requireControllerLocalReviewJSONDelimiter(decode(``), '}')
		},
		"trailing value": func() error {
			decoder := decode(`{} {}`)
			if err := consumeControllerLocalReviewJSONValue(decoder, 0); err != nil {
				return err
			}
			return requireControllerLocalReviewJSONEOF(decoder)
		},
		"malformed trailing": func() error {
			decoder := decode(`{} x`)
			if err := consumeControllerLocalReviewJSONValue(decoder, 0); err != nil {
				return err
			}
			return requireControllerLocalReviewJSONEOF(decoder)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("invalid JSON edge was accepted")
			}
		})
	}

	for name, object := range map[string]map[string]any{
		"line type":     {"line": "1"},
		"line syntax":   {"line": json.Number("not-a-number")},
		"line zero":     {"line": json.Number("0")},
		"line overflow": {"line": json.Number("2147483648")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseControllerLocalReviewFinding(object); err == nil {
				t.Fatal("invalid finding line was accepted")
			}
		})
	}
	validLine := json.Number("7")
	finding, err := parseControllerLocalReviewFinding(map[string]any{
		"severity": "low", "title": "title", "message": "message", "line": validLine,
	})
	if err != nil || finding.Line == nil || *finding.Line != 7 {
		t.Fatalf("valid finding line = %#v, %v", finding, err)
	}

	lineZero := 0
	lineTooLarge := math.MaxInt32 + 1
	for name, result := range map[string]ControllerLocalReviewResult{
		"invalid outcome": {Outcome: "unknown", Summary: "summary"},
		"passed with finding": {
			Outcome: ControllerLocalReviewPassed, Summary: "summary",
			Findings: []ControllerLocalReviewFinding{{Severity: "low"}},
		},
		"changes without finding": {
			Outcome: ControllerLocalReviewChangesRequired, Summary: "summary",
		},
		"invalid severity": {
			Outcome: ControllerLocalReviewChangesRequired, Summary: "summary",
			Findings: []ControllerLocalReviewFinding{{
				Severity: "unknown", Title: "title", Message: "message",
			}},
		},
		"zero line": {
			Outcome: ControllerLocalReviewChangesRequired, Summary: "summary",
			Findings: []ControllerLocalReviewFinding{{
				Severity: ControllerLocalReviewSeverityLow,
				Title:    "title", Message: "message", Line: &lineZero,
			}},
		},
		"large line": {
			Outcome: ControllerLocalReviewChangesRequired, Summary: "summary",
			Findings: []ControllerLocalReviewFinding{{
				Severity: ControllerLocalReviewSeverityLow,
				Title:    "title", Message: "message", Line: &lineTooLarge,
			}},
		},
		"oversized total": {
			Outcome: ControllerLocalReviewChangesRequired, Summary: "summary",
			Findings: []ControllerLocalReviewFinding{{
				Severity: ControllerLocalReviewSeverityLow,
				Title:    strings.Repeat("t", MaxControllerLocalReviewFindingsBytes),
				Message:  "message",
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateControllerLocalReviewResult(result); err == nil {
				t.Fatal("invalid controller review result was accepted")
			}
		})
	}
	for _, outcome := range []ControllerLocalReviewOutcome{
		ControllerLocalReviewPassed,
		ControllerLocalReviewChangesRequired,
		ControllerLocalReviewAttentionRequired,
	} {
		if !validControllerLocalReviewOutcome(outcome) {
			t.Fatalf("valid outcome %q was rejected", outcome)
		}
	}
	for _, severity := range []ControllerLocalReviewSeverity{
		ControllerLocalReviewSeverityCritical,
		ControllerLocalReviewSeverityHigh,
		ControllerLocalReviewSeverityMedium,
		ControllerLocalReviewSeverityLow,
	} {
		if !validControllerLocalReviewSeverity(severity) {
			t.Fatalf("valid severity %q was rejected", severity)
		}
	}
}

func TestRuntimeRetainingCompatibilityWrappers(t *testing.T) {
	cfgA := executionPolicyAgentConfig(t, "coverage-wrapper-a")
	cfgB := executionPolicyAgentConfig(t, "coverage-wrapper-b")
	cfgC := executionPolicyAgentConfig(t, "coverage-wrapper-c")
	providerA := &runtimeGateProvider{name: "a", closed: make(chan struct{})}
	providerB := &runtimeGateProvider{name: "b", closed: make(chan struct{})}
	providerC := &runtimeGateProvider{name: "c", closed: make(chan struct{})}
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoopWithRuntimePolicies(
		cfgA,
		messageBus,
		providerA,
		executionPolicyAgentSnapshot(t, cfgA, "A"),
		logger.DiagnosticPolicy{},
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	retainedA, err := loop.ReloadProviderAndConfigRetainingPreviousWithExecutionPolicy(
		context.Background(),
		providerB,
		cfgB,
		executionPolicyAgentSnapshot(t, cfgB, "B"),
	)
	if err != nil || retainedA == nil {
		t.Fatalf("execution-policy retaining reload = %T, %v", retainedA, err)
	}
	loop.CloseRetainedProvider(context.Background(), retainedA)
	retainedB, err := loop.ReloadProviderAndConfigRetainingPreviousWithRuntimePolicies(
		context.Background(),
		providerC,
		cfgC,
		executionPolicyAgentSnapshot(t, cfgC, "C"),
		logger.NewDiagnosticPolicy(false, logger.DEBUG),
	)
	if err != nil || retainedB == nil {
		t.Fatalf("runtime-policy retaining reload = %T, %v", retainedB, err)
	}
	loop.CloseRetainedProvider(context.Background(), retainedB)
}
