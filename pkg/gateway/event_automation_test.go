//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/eventing"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestSetupEventAutomationServiceDisabledDoesNotCreateStorage(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	databasePath := filepath.Join(workspace, "eventing", "events.db")
	cfg := eventAutomationTestConfig(workspace, databasePath, false, true)

	service, err := setupEventAutomationService(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("setupEventAutomationService() error = %v", err)
	}
	if service != nil {
		t.Fatalf("setupEventAutomationService() service = %#v, want nil", service)
	}
	if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(%q) error = %v, want os.ErrNotExist", workspace, err)
	}
}

func TestGitHubMCPConsumersUseExplicitDefaultAgentWorkspace(t *testing.T) {
	root := t.TempDir()
	configuredWorkspace := filepath.Join(root, "configured-default")
	customWorkspace := filepath.Join(root, "review-agent")
	cfg := eventAutomationTestConfig(
		configuredWorkspace,
		filepath.Join(configuredWorkspace, "eventing", "events.db"),
		true,
		false,
	)
	cfg.Agents.List = []config.AgentConfig{{
		ID:        "reviewer",
		Default:   true,
		Workspace: customWorkspace,
	}}
	cfg.Tools.MCP.Enabled = false

	msgBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(
		cfg,
		msgBus,
		&orderedShutdownProvider{closed: make(chan struct{})},
	)
	defer func() {
		msgBus.Close()
		agentLoop.Close()
	}()

	wantRoot := filepath.Join(customWorkspace, ".artifacts", "mcp")
	gotRoot := githubMCPArtifactRoot(cfg, agentLoop)
	if gotRoot != wantRoot {
		t.Fatalf("githubMCPArtifactRoot() = %q, want %q", gotRoot, wantRoot)
	}

	runtime := newEventReviewRuntime(cfg, agentLoop)
	if runtime.mcpArtifactRoot != wantRoot {
		t.Fatalf(
			"initial review runtime artifact root = %q, want %q",
			runtime.mcpArtifactRoot,
			wantRoot,
		)
	}
	runner := &gatewayNotificationPollRunner{}
	configureGitHubMCPReviewRuntime(&runtime, runner, gotRoot, true, true)
	if runtime.notificationMCP == nil || runtime.mcpArtifactRoot != wantRoot {
		t.Fatalf("notification runtime = %#v, want custom artifact root", runtime)
	}
	submitter, ok := runtime.submitter.(*reviews.GitHubSubmitter)
	if !ok {
		t.Fatalf("review submitter = %T, want *reviews.GitHubSubmitter", runtime.submitter)
	}
	if submitter.ArtifactRoot != wantRoot {
		t.Fatalf("review submitter artifact root = %q, want %q", submitter.ArtifactRoot, wantRoot)
	}
}

func TestEventAutomationIngressWithoutWorkflowsPersistsPendingEvent(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	databasePath := filepath.Join(workspace, "eventing", "events.db")
	cfg := eventAutomationTestConfig(workspace, databasePath, true, false)

	service, err := setupEventAutomationService(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("setupEventAutomationService() error = %v", err)
	}
	if service == nil || service.store == nil {
		t.Fatal("setupEventAutomationService() did not open the durable event store")
	}
	if service.prWorkspaces == nil || service.operatorBackend == nil {
		t.Fatal("setupEventAutomationService() did not compose the unified PR workspace operator")
	}
	if service.cancel == nil {
		t.Fatal("service.cancel is nil with ingress enabled; retention worker was not configured")
	}
	select {
	case <-service.done:
		t.Fatal("retention worker stopped before service shutdown")
	default:
	}

	inserted, err := service.store.Insert(context.Background(), eventAutomationTestEnvelope("disabled-workflows"))
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	if got := inserted.Event.Routing.Status; got != eventing.RoutingPending {
		t.Fatalf("inserted routing status = %q, want %q", got, eventing.RoutingPending)
	}

	persisted, err := service.store.Get(context.Background(), inserted.Event.Envelope.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got := persisted.Routing.Status; got != eventing.RoutingPending {
		t.Fatalf("persisted routing status = %q, want %q", got, eventing.RoutingPending)
	}

	if closeErr := service.Close(context.Background()); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	if closeErr := service.Close(context.Background()); closeErr != nil {
		t.Fatalf("second Close() error = %v", closeErr)
	}
	select {
	case <-service.done:
	default:
		t.Fatal("Close() returned before the retention worker drained")
	}
	if _, getErr := service.store.Get(
		context.Background(),
		inserted.Event.Envelope.ID,
	); !errors.Is(getErr, eventing.ErrClosed) {
		t.Fatalf("Get() after Close error = %v, want eventing.ErrClosed", getErr)
	}

	reopened, err := eventing.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("eventing.Open() after service close error = %v", err)
	}
	defer func() {
		if closeErr := reopened.Close(); closeErr != nil {
			t.Errorf("reopened.Close() error = %v", closeErr)
		}
	}()
	persisted, err = reopened.Get(context.Background(), inserted.Event.Envelope.ID)
	if err != nil {
		t.Fatalf("reopened.Get() error = %v", err)
	}
	if got := persisted.Routing.Status; got != eventing.RoutingPending {
		t.Fatalf("reopened routing status = %q, want %q", got, eventing.RoutingPending)
	}
}

func TestPruneExpiredEventsUsesConfiguredRetentionCutoff(t *testing.T) {
	now := time.Date(2026, 7, 29, 17, 0, 0, 0, time.FixedZone("AST", -4*60*60))
	storeNow := now.AddDate(0, 0, -31)
	store, err := eventing.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "eventing", "events.db"),
		eventing.WithClock(func() time.Time { return storeNow }),
	)
	if err != nil {
		t.Fatalf("eventing.Open() error = %v", err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("store.Close() error = %v", closeErr)
		}
	}()

	insertTerminal := func(dedupeKey string) string {
		t.Helper()
		inserted, insertErr := store.Insert(
			context.Background(),
			eventAutomationTestEnvelope(dedupeKey),
		)
		if insertErr != nil {
			t.Fatalf("Insert(%q) error = %v", dedupeKey, insertErr)
		}
		claimed, claimErr := store.ClaimRouting(
			context.Background(),
			"retention-test-router",
			1,
			time.Minute,
		)
		if claimErr != nil {
			t.Fatalf("ClaimRouting(%q) error = %v", dedupeKey, claimErr)
		}
		if len(claimed) != 1 {
			t.Fatalf("ClaimRouting(%q) returned %d events, want 1", dedupeKey, len(claimed))
		}
		if ackErr := store.AckRouting(
			context.Background(),
			inserted.Event.Envelope.ID,
			claimed[0].Routing.LeaseToken,
		); ackErr != nil {
			t.Fatalf("AckRouting(%q) error = %v", dedupeKey, ackErr)
		}
		return inserted.Event.Envelope.ID
	}

	expiredID := insertTerminal("expired-retention-event")
	storeNow = now
	retainedID := insertTerminal("retained-retention-event")

	pruned, err := pruneExpiredEvents(
		context.Background(),
		store,
		30,
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("pruneExpiredEvents() error = %v", err)
	}
	if pruned != 1 {
		t.Fatalf("pruneExpiredEvents() count = %d, want 1", pruned)
	}
	if _, getErr := store.Get(context.Background(), expiredID); !errors.Is(
		getErr,
		eventing.ErrNotFound,
	) {
		t.Fatalf("Get(expired) error = %v, want eventing.ErrNotFound", getErr)
	}
	if _, getErr := store.Get(context.Background(), retainedID); getErr != nil {
		t.Fatalf("Get(retained) error = %v", getErr)
	}
}

func TestPruneExpiredEventsDoesNotWrapOversizedRetentionIntoFuture(
	t *testing.T,
) {
	const overflowRetentionDays64 int64 = 213_503_982_334_601
	overflowRetentionDays := int(overflowRetentionDays64)
	if int64(overflowRetentionDays) != overflowRetentionDays64 {
		t.Skip("exact overflow regression requires a 64-bit int")
	}
	now := time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)
	calls := 0
	pruner := eventRetentionPrunerFunc(
		func(_ context.Context, cutoff time.Time, _ int) (int64, error) {
			calls++
			if cutoff.After(now) {
				t.Fatalf("retention cutoff wrapped into the future: %s", cutoff)
			}
			return 1, nil
		},
	)

	pruned, err := pruneExpiredEvents(
		context.Background(),
		pruner,
		overflowRetentionDays,
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatalf("pruneExpiredEvents() error = %v", err)
	}
	if pruned != 0 {
		t.Fatalf("pruneExpiredEvents() count = %d, want safe no-op", pruned)
	}
	if calls != 0 {
		t.Fatalf("oversized retention invoked Prune %d times, want zero", calls)
	}
}

func TestEventRetentionWorkerRunsImmediatelyAndJoinsOnCancellation(t *testing.T) {
	now := time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)
	type pruneCall struct {
		cutoff time.Time
		limit  int
	}
	calls := make(chan pruneCall, 2)
	pruner := eventRetentionPrunerFunc(
		func(_ context.Context, cutoff time.Time, limit int) (int64, error) {
			calls <- pruneCall{cutoff: cutoff, limit: limit}
			return 0, nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workers.Add(1)
	go runEventRetentionWorker(
		ctx,
		&workers,
		pruner,
		nil,
		45,
		time.Hour,
		func() time.Time { return now },
	)

	var call pruneCall
	select {
	case call = <-calls:
	case <-time.After(time.Second):
		t.Fatal("retention worker did not run startup maintenance")
	}
	wantCutoff := now.AddDate(0, 0, -45)
	if !call.cutoff.Equal(wantCutoff) {
		t.Fatalf("retention cutoff = %s, want %s", call.cutoff, wantCutoff)
	}
	if call.limit != eventRetentionPruneBatchSize {
		t.Fatalf(
			"retention prune limit = %d, want %d",
			call.limit,
			eventRetentionPruneBatchSize,
		)
	}

	cancel()
	joined := make(chan struct{})
	go func() {
		workers.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("retention worker did not join after cancellation")
	}
	select {
	case extra := <-calls:
		t.Fatalf("retention worker invoked prune after join: %#v", extra)
	default:
	}
}

func TestEventRetentionWorkerAcquiresRuntimeBeforePruning(t *testing.T) {
	acquireEntered := make(chan struct{})
	allowAcquire := make(chan struct{})
	pruneCalled := make(chan struct{})
	runtimeReleased := make(chan struct{})
	var acquireOnce sync.Once
	var pruneOnce sync.Once
	var releaseOnce sync.Once

	acquire := func(ctx context.Context) (context.Context, func(), error) {
		acquireOnce.Do(func() { close(acquireEntered) })
		select {
		case <-ctx.Done():
			return ctx, func() {}, ctx.Err()
		case <-allowAcquire:
			return ctx, func() {
				releaseOnce.Do(func() { close(runtimeReleased) })
			}, nil
		}
	}
	pruner := eventRetentionPrunerFunc(
		func(context.Context, time.Time, int) (int64, error) {
			pruneOnce.Do(func() { close(pruneCalled) })
			return 0, nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workers.Add(1)
	go runEventRetentionWorker(
		ctx,
		&workers,
		pruner,
		acquire,
		30,
		time.Hour,
		time.Now,
	)
	select {
	case <-acquireEntered:
	case <-time.After(time.Second):
		t.Fatal("retention worker did not reach runtime acquisition")
	}
	select {
	case <-pruneCalled:
		t.Fatal("retention worker pruned before runtime acquisition completed")
	default:
	}

	close(allowAcquire)
	select {
	case <-pruneCalled:
	case <-time.After(time.Second):
		t.Fatal("retention worker did not prune after runtime acquisition")
	}
	select {
	case <-runtimeReleased:
	case <-time.After(time.Second):
		t.Fatal("retention worker did not release the runtime generation")
	}
	cancel()
	workers.Wait()
}

func TestEventRetentionWorkerFailureDoesNotStopMaintenance(t *testing.T) {
	calls := make(chan int, 3)
	callNumber := 0
	acquireCalls := 0
	runtimeReleases := 0
	acquire := func(ctx context.Context) (context.Context, func(), error) {
		acquireCalls++
		return ctx, func() { runtimeReleases++ }, nil
	}
	pruner := eventRetentionPrunerFunc(
		func(context.Context, time.Time, int) (int64, error) {
			callNumber++
			calls <- callNumber
			if callNumber == 1 {
				return 0, errors.New("temporary retention failure")
			}
			return 0, nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workers.Add(1)
	go runEventRetentionWorker(
		ctx,
		&workers,
		pruner,
		acquire,
		30,
		5*time.Millisecond,
		time.Now,
	)
	for want := 1; want <= 2; want++ {
		select {
		case got := <-calls:
			if got != want {
				t.Fatalf("retention call = %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("retention worker stopped before call %d", want)
		}
	}
	cancel()
	workers.Wait()
	if acquireCalls < 2 || acquireCalls != callNumber {
		t.Fatalf(
			"runtime acquisitions = %d for %d maintenance cycles",
			acquireCalls,
			callNumber,
		)
	}
	if runtimeReleases != acquireCalls {
		t.Fatalf(
			"runtime releases = %d, want %d",
			runtimeReleases,
			acquireCalls,
		)
	}
}

func TestEventRetentionWorkerReloadStopsOldGenerationBeforeStartingNew(t *testing.T) {
	type generationCall struct {
		generation string
		cutoff     time.Time
	}
	calls := make(chan generationCall, 3)
	now := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)

	start := func(generation string, retentionDays int) (context.CancelFunc, *sync.WaitGroup) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		workers := &sync.WaitGroup{}
		workers.Add(1)
		go runEventRetentionWorker(
			ctx,
			workers,
			eventRetentionPrunerFunc(
				func(_ context.Context, cutoff time.Time, _ int) (int64, error) {
					calls <- generationCall{generation: generation, cutoff: cutoff}
					return 0, nil
				},
			),
			nil,
			retentionDays,
			time.Hour,
			func() time.Time { return now },
		)
		return cancel, workers
	}

	oldCancel, oldWorkers := start("old", 30)
	select {
	case call := <-calls:
		if call.generation != "old" ||
			!call.cutoff.Equal(now.AddDate(0, 0, -30)) {
			t.Fatalf("old generation startup call = %#v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("old retention generation did not start")
	}
	oldCancel()
	oldWorkers.Wait()

	newCancel, newWorkers := start("new", 7)
	select {
	case call := <-calls:
		if call.generation != "new" ||
			!call.cutoff.Equal(now.AddDate(0, 0, -7)) {
			t.Fatalf("new generation startup call = %#v", call)
		}
	case <-time.After(time.Second):
		t.Fatal("new retention generation did not start")
	}
	newCancel()
	newWorkers.Wait()

	select {
	case unexpected := <-calls:
		t.Fatalf("stopped retention generation ran again: %#v", unexpected)
	default:
	}
}

func TestPruneExpiredEventsBoundsEachMaintenanceCycle(t *testing.T) {
	calls := 0
	pruner := eventRetentionPrunerFunc(
		func(context.Context, time.Time, int) (int64, error) {
			calls++
			return eventRetentionPruneBatchSize, nil
		},
	)
	pruned, err := pruneExpiredEvents(
		context.Background(),
		pruner,
		30,
		time.Now,
	)
	if err != nil {
		t.Fatalf("pruneExpiredEvents() error = %v", err)
	}
	if calls != eventRetentionMaxBatchesPerCycle {
		t.Fatalf(
			"Prune() calls = %d, want bounded maximum %d",
			calls,
			eventRetentionMaxBatchesPerCycle,
		)
	}
	wantPruned := int64(
		eventRetentionPruneBatchSize * eventRetentionMaxBatchesPerCycle,
	)
	if pruned != wantPruned {
		t.Fatalf("pruneExpiredEvents() count = %d, want %d", pruned, wantPruned)
	}
}

func TestHandleConfigReloadFailedCandidateCannotPruneWithShorterRetention(t *testing.T) {
	workspace := t.TempDir()
	databasePath := filepath.Join(workspace, "eventing", "events.db")
	expiredOnlyForCandidate := time.Now().UTC().AddDate(0, 0, -10)
	seedStore, err := eventing.Open(
		context.Background(),
		databasePath,
		eventing.WithClock(func() time.Time { return expiredOnlyForCandidate }),
	)
	if err != nil {
		t.Fatalf("eventing.Open(seed) error = %v", err)
	}
	inserted, err := seedStore.Insert(
		context.Background(),
		eventAutomationTestEnvelope("candidate-retention-rollback"),
	)
	if err != nil {
		t.Fatalf("seedStore.Insert() error = %v", err)
	}
	claimed, err := seedStore.ClaimRouting(
		context.Background(),
		"retention-seed-router",
		1,
		time.Minute,
	)
	if err != nil {
		t.Fatalf("seedStore.ClaimRouting() error = %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("seedStore.ClaimRouting() returned %d events, want 1", len(claimed))
	}
	if err = seedStore.AckRouting(
		context.Background(),
		inserted.Event.Envelope.ID,
		claimed[0].Routing.LeaseToken,
	); err != nil {
		t.Fatalf("seedStore.AckRouting() error = %v", err)
	}
	if err = seedStore.Close(); err != nil {
		t.Fatalf("seedStore.Close() error = %v", err)
	}

	oldCfg := eventAutomationTestConfig(workspace, databasePath, true, false)
	oldCfg.Events.Ingress.RetentionDays = 30
	newCfg := eventAutomationTestConfig(workspace, databasePath, true, false)
	newCfg.Events.Ingress.RetentionDays = 1
	newCfg.Agents.Defaults.ModelName = ""

	msgBus := bus.NewMessageBus()
	oldProvider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(oldCfg, msgBus, oldProvider)
	oldService, err := setupEventAutomationService(
		context.Background(),
		oldCfg,
		agentLoop,
	)
	if err != nil {
		t.Fatalf("setupEventAutomationService(old) error = %v", err)
	}
	runningServices := &services{EventAutomation: oldService}
	installTestEventOperatorGeneration(t, runningServices)
	defer func() {
		_ = deactivateEventOperator(context.Background(), runningServices)
		_ = closeEventAutomationService(
			context.Background(),
			&runningServices.EventAutomation,
		)
		msgBus.Close()
		agentLoop.Close()
		oldProvider.Close()
	}()

	if _, getErr := oldService.store.Get(
		context.Background(),
		inserted.Event.Envelope.ID,
	); getErr != nil {
		t.Fatalf("old-retention event missing before reload: %v", getErr)
	}

	candidateAtFence := make(chan struct{})
	var candidateFenceOnce sync.Once
	forcedRestartErr := errors.New("forced failure after candidate retention start")
	serviceOps := configReloadServiceOps{
		stop: stopAndCleanupServices,
		restart: func(
			currentLoop *agent.AgentLoop,
			currentServices *services,
			_ *bus.MessageBus,
		) error {
			currentCfg := currentLoop.GetConfig()
			if currentCfg != newCfg {
				recovered, setupErr := setupEventAutomationService(
					context.Background(),
					currentCfg,
					currentLoop,
				)
				if setupErr != nil {
					return setupErr
				}
				currentServices.EventAutomation = recovered
				return nil
			}

			acquireCandidateRuntime := func(
				ctx context.Context,
			) (context.Context, func(), error) {
				candidateFenceOnce.Do(func() { close(candidateAtFence) })
				return currentLoop.AcquireRuntimeGeneration(ctx, currentCfg)
			}
			candidate, setupErr := newEventAutomationService(
				context.Background(),
				currentCfg,
				nil,
				nil,
				acquireCandidateRuntime,
			)
			if setupErr != nil {
				return setupErr
			}
			currentServices.EventAutomation = candidate
			select {
			case <-candidateAtFence:
				return forcedRestartErr
			case <-time.After(2 * time.Second):
				return fmt.Errorf("candidate retention did not reach runtime fence")
			}
		},
	}
	providerRef := providers.LLMProvider(oldProvider)

	err = handleConfigReloadWithServiceOps(
		context.Background(),
		agentLoop,
		newCfg,
		&providerRef,
		runningServices,
		msgBus,
		true,
		false,
		serviceOps,
	)
	if !errors.Is(err, forcedRestartErr) {
		t.Fatalf(
			"handleConfigReloadWithServiceOps() error = %v, want forced restart failure",
			err,
		)
	}
	if agentLoop.GetConfig() != oldCfg || providerRef != oldProvider {
		t.Fatal("failed candidate retention reload did not restore old runtime")
	}
	if runningServices.EventAutomation == nil {
		t.Fatal("failed candidate retention reload did not restart old event service")
	}
	if _, getErr := runningServices.EventAutomation.store.Get(
		context.Background(),
		inserted.Event.Envelope.ID,
	); getErr != nil {
		t.Fatalf("candidate retention mutated durable inbox before rollback: %v", getErr)
	}
}

func TestSetupEventAutomationServiceWithWorkflowsStartsDrainableWorkers(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "eventing", "events.db"),
		true,
		true,
	)
	msgBus := bus.NewMessageBus()
	provider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider)
	defer func() {
		msgBus.Close()
		agentLoop.Close()
	}()

	service, err := setupEventAutomationService(context.Background(), cfg, agentLoop)
	if err != nil {
		t.Fatalf("setupEventAutomationService() error = %v", err)
	}
	if service == nil || service.store == nil || service.cancel == nil {
		t.Fatalf("workflow-enabled event automation service = %#v, want store and workers", service)
	}
	select {
	case <-service.done:
		t.Fatal("workflow workers stopped before service shutdown")
	default:
	}
	if closeErr := service.Close(context.Background()); closeErr != nil {
		t.Fatalf("Close() error = %v", closeErr)
	}
	select {
	case <-service.done:
	default:
		t.Fatal("Close() returned before workflow workers drained")
	}
}

func TestEventAutomationServiceRejectsCorruptDatabaseAtStartup(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	databasePath := filepath.Join(workspace, "eventing", "events.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(databasePath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg := eventAutomationTestConfig(workspace, databasePath, true, false)

	service, err := setupEventAutomationService(context.Background(), cfg, nil)
	if err == nil {
		if service != nil {
			_ = service.Close(context.Background())
		}
		t.Fatal("setupEventAutomationService() error = nil, want corrupt database failure")
	}
	if service != nil {
		t.Fatalf("setupEventAutomationService() service = %#v after failure, want nil", service)
	}
	if !strings.Contains(err.Error(), "open durable event inbox") {
		t.Fatalf("setupEventAutomationService() error = %q, want durable inbox context", err)
	}
}

func TestHandleConfigReloadRejectsCorruptEventStoreBeforeCommit(t *testing.T) {
	oldWorkspace := t.TempDir()
	oldCfg := eventAutomationTestConfig(
		oldWorkspace,
		filepath.Join(oldWorkspace, "eventing", "events.db"),
		true,
		false,
	)
	msgBus := bus.NewMessageBus()
	provider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(oldCfg, msgBus, provider)
	oldService, err := setupEventAutomationService(context.Background(), oldCfg, agentLoop)
	if err != nil {
		t.Fatalf("setupEventAutomationService(old) error = %v", err)
	}
	defer func() {
		_ = oldService.Close(context.Background())
		msgBus.Close()
		agentLoop.Close()
	}()

	newWorkspace := t.TempDir()
	corruptPath := filepath.Join(newWorkspace, "eventing", "events.db")
	if mkdirErr := os.MkdirAll(filepath.Dir(corruptPath), 0o700); mkdirErr != nil {
		t.Fatalf("MkdirAll() error = %v", mkdirErr)
	}
	if writeErr := os.WriteFile(
		corruptPath,
		[]byte("not a sqlite database"),
		0o600,
	); writeErr != nil {
		t.Fatalf("WriteFile() error = %v", writeErr)
	}
	newCfg := eventAutomationTestConfig(newWorkspace, corruptPath, true, false)
	providerRef := providers.LLMProvider(provider)
	runningServices := &services{EventAutomation: oldService}
	installTestEventOperatorGeneration(t, runningServices)
	defer func() {
		_ = deactivateEventOperator(context.Background(), runningServices)
	}()

	err = handleConfigReload(
		context.Background(),
		agentLoop,
		newCfg,
		&providerRef,
		runningServices,
		msgBus,
		true,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "validate event automation before reload") {
		t.Fatalf("handleConfigReload() error = %v, want event storage preflight failure", err)
	}
	if agentLoop.GetConfig() != oldCfg {
		t.Fatal("failed reload committed the new agent configuration")
	}
	if providerRef != provider {
		t.Fatal("failed reload replaced the active provider")
	}
	if runningServices.EventAutomation != oldService {
		t.Fatal("failed reload replaced or stopped the active event automation service")
	}
	if _, insertErr := oldService.store.Insert(
		context.Background(),
		eventAutomationTestEnvelope("reload-preflight-old-service"),
	); insertErr != nil {
		t.Fatalf("old event automation service unusable after failed reload: %v", insertErr)
	}
}

func TestHandleConfigReloadRollsBackPostSwapWorkflowRuntimeFailure(t *testing.T) {
	oldWorkspace := t.TempDir()
	oldCfg := eventAutomationTestConfig(
		oldWorkspace,
		filepath.Join(oldWorkspace, "eventing", "events.db"),
		true,
		false,
	)
	msgBus := bus.NewMessageBus()
	oldProvider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(oldCfg, msgBus, oldProvider)
	oldService, err := setupEventAutomationService(context.Background(), oldCfg, agentLoop)
	if err != nil {
		t.Fatalf("setupEventAutomationService(old) error = %v", err)
	}
	healthServer := health.NewServer("127.0.0.1", 1, "")
	healthServer.SetReady(true)
	runningServices := &services{
		EventAutomation: oldService,
		HealthServer:    healthServer,
	}
	installTestEventOperatorGeneration(t, runningServices)
	defer func() {
		_ = deactivateEventOperator(context.Background(), runningServices)
		_ = closeEventAutomationService(context.Background(), &runningServices.EventAutomation)
		msgBus.Close()
		agentLoop.Close()
		oldProvider.Close()
	}()

	newWorkspace := t.TempDir()
	newCfg := eventAutomationTestConfig(
		newWorkspace,
		filepath.Join(newWorkspace, "eventing", "events.db"),
		true,
		true,
	)
	newCfg.Hooks.Enabled = true
	newCfg.Hooks.Builtins = map[string]config.BuiltinHookConfig{
		"definitely-unregistered": {Enabled: true},
	}
	providerRef := providers.LLMProvider(oldProvider)
	restartCount := 0
	candidateRestarted := false
	serviceOps := configReloadServiceOps{
		stop: stopAndCleanupServices,
		restart: func(
			currentLoop *agent.AgentLoop,
			currentServices *services,
			_ *bus.MessageBus,
		) error {
			restartCount++
			if currentLoop.GetConfig() == newCfg {
				candidateRestarted = true
			}
			service, setupErr := setupEventAutomationService(
				context.Background(),
				currentLoop.GetConfig(),
				currentLoop,
			)
			if setupErr != nil {
				return setupErr
			}
			currentServices.EventAutomation = service
			return nil
		},
	}

	err = handleConfigReloadWithServiceOps(
		context.Background(),
		agentLoop,
		newCfg,
		&providerRef,
		runningServices,
		msgBus,
		true,
		false,
		serviceOps,
	)
	if err == nil || !strings.Contains(err.Error(), `builtin hook "definitely-unregistered" is not registered`) {
		t.Fatalf("handleConfigReloadWithServiceOps() error = %v, want runtime initialization failure", err)
	}
	if restartCount != 1 {
		t.Fatalf("restart count = %d, want recovered previous services only", restartCount)
	}
	if candidateRestarted {
		t.Fatal("candidate services started before event workflow runtime preflight succeeded")
	}
	if agentLoop.GetConfig() != oldCfg {
		t.Fatal("post-swap failure did not restore the previous agent configuration")
	}
	if providerRef != oldProvider {
		t.Fatal("post-swap failure did not preserve the previous provider")
	}
	select {
	case <-oldProvider.closed:
		t.Fatal("post-swap failure closed the restored previous provider")
	default:
	}
	if !healthServer.IsReady() {
		t.Fatal("readiness was not restored after successful rollback")
	}
	if runningServices.EventAutomation == nil || runningServices.EventAutomation == oldService {
		t.Fatalf(
			"event automation after rollback = %#v, want a restarted previous service",
			runningServices.EventAutomation,
		)
	}
	if _, insertErr := runningServices.EventAutomation.store.Insert(
		context.Background(),
		eventAutomationTestEnvelope("post-swap-rollback"),
	); insertErr != nil {
		t.Fatalf("restored event automation service unusable: %v", insertErr)
	}
}

type runtimeAdmissionObservedContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

func (c *runtimeAdmissionObservedContext) Value(key any) any {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Value(key)
}

func TestHandleConfigReloadDoesNotAdmitTurnIntoProvisionalGeneration(t *testing.T) {
	oldCfg := eventAutomationTestConfig(t.TempDir(), "", false, false)
	newCfg := eventAutomationTestConfig(t.TempDir(), "", false, false)
	newCfg.Agents.Defaults.ModelName = ""
	msgBus := bus.NewMessageBus()
	oldProvider := &orderedShutdownProvider{
		closed:   make(chan struct{}),
		response: "old-generation",
	}
	agentLoop := agent.NewAgentLoop(oldCfg, msgBus, oldProvider)
	healthServer := health.NewServer("127.0.0.1", 1, "")
	healthServer.SetReady(true)
	runningServices := &services{HealthServer: healthServer}
	defer func() {
		msgBus.Close()
		agentLoop.Close()
		oldProvider.Close()
	}()

	type turnResult struct {
		response string
		err      error
	}
	turnDone := make(chan turnResult, 1)
	admissionEntered := make(chan struct{})
	forcedRestartErr := errors.New("forced candidate restart failure")
	restartCalls := 0
	serviceOps := configReloadServiceOps{
		stop: func(*services, time.Duration, bool) error { return nil },
		restart: func(
			currentLoop *agent.AgentLoop,
			_ *services,
			_ *bus.MessageBus,
		) error {
			restartCalls++
			if currentLoop.GetConfig() != newCfg {
				return nil
			}
			observedCtx := &runtimeAdmissionObservedContext{
				Context: context.Background(),
				entered: admissionEntered,
			}
			go func() {
				response, turnErr := currentLoop.ProcessDirectWithChannel(
					observedCtx,
					"which generation?",
					"reload-generation-gate",
					"cli",
					"direct",
				)
				turnDone <- turnResult{response: response, err: turnErr}
			}()
			select {
			case <-admissionEntered:
				return forcedRestartErr
			case <-time.After(2 * time.Second):
				return fmt.Errorf("turn did not reach runtime admission")
			}
		},
	}
	providerRef := providers.LLMProvider(oldProvider)

	err := handleConfigReloadWithServiceOps(
		context.Background(),
		agentLoop,
		newCfg,
		&providerRef,
		runningServices,
		msgBus,
		true,
		false,
		serviceOps,
	)
	if !errors.Is(err, forcedRestartErr) {
		t.Fatalf("handleConfigReloadWithServiceOps() error = %v, want forced restart failure", err)
	}
	if restartCalls != 2 {
		t.Fatalf("restart calls = %d, want candidate and recovered previous services", restartCalls)
	}
	select {
	case result := <-turnDone:
		if result.err != nil {
			t.Fatalf("turn after rollback error = %v", result.err)
		}
		if result.response != "old-generation" {
			t.Fatalf("turn response = %q, want restored old generation", result.response)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("turn did not resume after rollback")
	}
	if agentLoop.GetConfig() != oldCfg || providerRef != oldProvider {
		t.Fatal("failed candidate reload did not restore old runtime")
	}
	if !healthServer.IsReady() {
		t.Fatal("readiness was not restored after rollback")
	}
}

type observedCronExecutor struct {
	*agent.AgentLoop
	once    sync.Once
	entered chan struct{}
}

func (e *observedCronExecutor) AcquireRuntimeGeneration(
	ctx context.Context,
	expected *config.Config,
) (context.Context, func(), error) {
	e.once.Do(func() { close(e.entered) })
	return e.AgentLoop.AcquireRuntimeGeneration(ctx, expected)
}

func TestHandleConfigReloadFencesDueCandidateCommandOnRollback(t *testing.T) {
	oldCfg := eventAutomationTestConfig(t.TempDir(), "", false, false)
	newCfg := eventAutomationTestConfig(t.TempDir(), "", false, false)
	newCfg.Agents.Defaults.ModelName = ""
	msgBus := bus.NewMessageBus()
	oldProvider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(oldCfg, msgBus, oldProvider)
	runningServices := &services{}
	defer func() {
		if runningServices.CronService != nil {
			runningServices.CronService.Stop()
		}
		msgBus.Close()
		agentLoop.Close()
		oldProvider.Close()
	}()

	marker := filepath.Join(t.TempDir(), "candidate-command-ran")
	commandEntered := make(chan struct{})
	commandDone := make(chan string, 1)
	var candidateCronService *cron.CronService
	var candidateJobID string
	forcedRestartErr := errors.New("forced failure after candidate cron start")
	serviceOps := configReloadServiceOps{
		stop: stopAndCleanupServices,
		restart: func(
			currentLoop *agent.AgentLoop,
			currentServices *services,
			currentBus *bus.MessageBus,
		) error {
			if currentLoop.GetConfig() != newCfg {
				currentServices.CronService = nil
				return nil
			}
			executor := &observedCronExecutor{
				AgentLoop: currentLoop,
				entered:   commandEntered,
			}
			cronService := cron.NewCronService(
				filepath.Join(newCfg.WorkspacePath(), "cron", "jobs.json"),
				nil,
			)
			cronTool, setupErr := tools.NewCronTool(
				cronService,
				executor,
				currentBus,
				newCfg.WorkspacePath(),
				true,
				time.Second,
				newCfg,
			)
			if setupErr != nil {
				return setupErr
			}
			cronService.SetOnJob(func(job *cron.CronJob) (string, error) {
				result := cronTool.ExecuteJob(context.Background(), job)
				commandDone <- result
				return result, nil
			})
			everyMS := int64(1)
			job, addErr := cronService.AddJob(
				"candidate command",
				cron.CronSchedule{Kind: "every", EveryMS: &everyMS},
				"candidate command",
				"cli",
				"direct",
			)
			if addErr != nil {
				return addErr
			}
			job.Payload.Command = fmt.Sprintf("touch %q", marker)
			if updateErr := cronService.UpdateJob(job); updateErr != nil {
				return updateErr
			}
			candidateCronService = cronService
			candidateJobID = job.ID
			currentServices.CronService = cronService
			if startErr := cronService.Start(); startErr != nil {
				return startErr
			}
			select {
			case <-commandEntered:
				return forcedRestartErr
			case <-time.After(2 * time.Second):
				return fmt.Errorf("due candidate command did not reach runtime fence")
			}
		},
	}
	providerRef := providers.LLMProvider(oldProvider)

	err := handleConfigReloadWithServiceOps(
		context.Background(),
		agentLoop,
		newCfg,
		&providerRef,
		runningServices,
		msgBus,
		true,
		false,
		serviceOps,
	)
	if !errors.Is(err, forcedRestartErr) {
		t.Fatalf("handleConfigReloadWithServiceOps() error = %v, want forced restart failure", err)
	}
	select {
	case result := <-commandDone:
		if !strings.Contains(result, "runtime config generation changed") {
			t.Fatalf("candidate command result = %q, want stale-generation rejection", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("candidate command did not leave the runtime fence after rollback")
	}
	bookkeepingDeadline := time.Now().Add(2 * time.Second)
	for {
		job, ok := candidateCronService.GetJob(candidateJobID)
		if ok && job.State.LastRunAtMS != nil {
			break
		}
		if time.Now().After(bookkeepingDeadline) {
			t.Fatal("candidate cron service did not finish post-callback bookkeeping")
		}
		time.Sleep(time.Millisecond)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("candidate command marker stat error = %v, want os.ErrNotExist", statErr)
	}
	if agentLoop.GetConfig() != oldCfg || providerRef != oldProvider {
		t.Fatal("candidate command failure did not restore old runtime")
	}
}

func TestHandleConfigReloadRecoversServicesAfterInitialDrainFailure(t *testing.T) {
	workspace := t.TempDir()
	oldCfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "eventing", "events.db"),
		true,
		false,
	)
	msgBus := bus.NewMessageBus()
	oldProvider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(oldCfg, msgBus, oldProvider)
	oldService, err := setupEventAutomationService(context.Background(), oldCfg, agentLoop)
	if err != nil {
		t.Fatalf("setupEventAutomationService(old) error = %v", err)
	}
	healthServer := health.NewServer("127.0.0.1", 1, "")
	healthServer.SetReady(true)
	runningServices := &services{
		EventAutomation: oldService,
		HealthServer:    healthServer,
	}
	installTestEventOperatorGeneration(t, runningServices)
	defer func() {
		_ = deactivateEventOperator(context.Background(), runningServices)
		_ = closeEventAutomationService(context.Background(), &runningServices.EventAutomation)
		msgBus.Close()
		agentLoop.Close()
		oldProvider.Close()
	}()

	stopCalls := 0
	restartCalls := 0
	drainErr := errors.New("injected initial worker drain timeout")
	serviceOps := configReloadServiceOps{
		stop: func(currentServices *services, timeout time.Duration, isReload bool) error {
			stopCalls++
			stopErr := stopAndCleanupServices(currentServices, timeout, isReload)
			if stopCalls == 1 {
				return errors.Join(drainErr, stopErr)
			}
			return stopErr
		},
		restart: func(
			currentLoop *agent.AgentLoop,
			currentServices *services,
			_ *bus.MessageBus,
		) error {
			restartCalls++
			service, setupErr := setupEventAutomationService(
				context.Background(),
				currentLoop.GetConfig(),
				currentLoop,
			)
			if setupErr != nil {
				return setupErr
			}
			currentServices.EventAutomation = service
			return nil
		},
	}
	newCfg := eventAutomationTestConfig(
		t.TempDir(),
		filepath.Join(t.TempDir(), "eventing", "events.db"),
		true,
		false,
	)
	providerRef := providers.LLMProvider(oldProvider)

	err = handleConfigReloadWithServiceOps(
		context.Background(),
		agentLoop,
		newCfg,
		&providerRef,
		runningServices,
		msgBus,
		true,
		false,
		serviceOps,
	)
	if !errors.Is(err, drainErr) {
		t.Fatalf("handleConfigReloadWithServiceOps() error = %v, want drain failure", err)
	}
	if stopCalls != 2 || restartCalls != 1 {
		t.Fatalf("recovery calls: stop=%d restart=%d, want 2 and 1", stopCalls, restartCalls)
	}
	if agentLoop.GetConfig() != oldCfg || providerRef != oldProvider {
		t.Fatal("pre-swap drain failure changed the active config or provider")
	}
	if !healthServer.IsReady() {
		t.Fatal("readiness was not restored after drain-failure recovery")
	}
	if runningServices.EventAutomation == nil || runningServices.EventAutomation == oldService {
		t.Fatalf(
			"event automation after drain recovery = %#v, want restarted old service",
			runningServices.EventAutomation,
		)
	}
}

func TestEventAutomationServiceCloseWaitsForWorkersBeforeClosingStore(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "eventing", "events.db")
	store, err := eventing.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatalf("eventing.Open() error = %v", err)
	}

	done := make(chan struct{})
	cancelCalled := make(chan struct{})
	service := &eventAutomationService{
		store: store,
		cancel: func() {
			close(cancelCalled)
		},
		done: done,
	}

	timedOut, cancel := context.WithCancel(context.Background())
	cancel()
	if closeErr := service.Close(timedOut); !errors.Is(closeErr, context.Canceled) {
		t.Fatalf("Close(canceled context) error = %v, want context.Canceled", closeErr)
	}
	select {
	case <-cancelCalled:
	default:
		t.Fatal("Close() did not signal worker cancellation")
	}

	inserted, err := store.Insert(context.Background(), eventAutomationTestEnvelope("before-drain"))
	if err != nil {
		t.Fatalf("Insert() while workers have not drained error = %v; store closed too early", err)
	}

	close(done)
	if closeErr := service.Close(context.Background()); closeErr != nil {
		t.Fatalf("Close() after worker drain error = %v", closeErr)
	}
	if closeErr := service.Close(context.Background()); closeErr != nil {
		t.Fatalf("idempotent Close() error = %v", closeErr)
	}
	if _, getErr := store.Get(
		context.Background(),
		inserted.Event.Envelope.ID,
	); !errors.Is(getErr, eventing.ErrClosed) {
		t.Fatalf("Get() after drained Close error = %v, want eventing.ErrClosed", getErr)
	}

	servicePointer := service
	if closeErr := closeEventAutomationService(context.Background(), &servicePointer); closeErr != nil {
		t.Fatalf("closeEventAutomationService() error = %v", closeErr)
	}
	if servicePointer != nil {
		t.Fatal("closeEventAutomationService() did not clear the stopped service")
	}
	if closeErr := closeEventAutomationService(context.Background(), &servicePointer); closeErr != nil {
		t.Fatalf("idempotent closeEventAutomationService() error = %v", closeErr)
	}
}

func TestShutdownGatewayDrainsEventWorkersBeforeClosingProvider(t *testing.T) {
	store, err := eventing.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "eventing", "events.db"),
	)
	if err != nil {
		t.Fatalf("eventing.Open() error = %v", err)
	}
	workerDone := make(chan struct{})
	cancelCalled := make(chan struct{})
	service := &eventAutomationService{
		store: store,
		cancel: func() {
			close(cancelCalled)
		},
		done: workerDone,
	}
	provider := &orderedShutdownProvider{closed: make(chan struct{})}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	msgBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider)
	msgBus.SetEventPublisher(agentLoop.RuntimeEventBus())

	shutdownDone := make(chan struct{})
	go func() {
		shutdownGateway(
			&services{EventAutomation: service},
			agentLoop,
			provider,
			msgBus,
			true,
		)
		close(shutdownDone)
	}()
	select {
	case <-cancelCalled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel event workers")
	}
	select {
	case <-provider.closed:
		t.Fatal("provider closed before event workers drained")
	default:
	}

	close(workerDone)
	select {
	case <-provider.closed:
	case <-time.After(time.Second):
		t.Fatal("provider was not closed after event workers drained")
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not complete")
	}
}

func TestShutdownGatewayDrainsRuntimeGenerationBeforeClosingProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	msgBus := bus.NewMessageBus()
	provider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider)
	msgBus.SetEventPublisher(agentLoop.RuntimeEventBus())
	channelManager, err := channels.NewManager(cfg, msgBus, nil)
	if err != nil {
		t.Fatalf("channels.NewManager() error = %v", err)
	}
	shutdownChannel := &blockingShutdownChannel{
		stopEntered: make(chan struct{}),
		allowStop:   make(chan struct{}),
	}
	channelManager.RegisterChannel("shutdown-order", shutdownChannel)
	if startErr := channelManager.StartAll(context.Background()); startErr != nil {
		t.Fatalf("ChannelManager.StartAll() error = %v", startErr)
	}
	sub, shutdownEvents, err := agentLoop.RuntimeEvents().
		OfKind(runtimeevents.KindGatewayShutdown).
		SubscribeChan(
			context.Background(),
			runtimeevents.SubscribeOptions{Name: "shutdown-runtime-drain", Buffer: 1},
		)
	if err != nil {
		t.Fatalf("SubscribeChan() error = %v", err)
	}
	defer func() { _ = sub.Close() }()

	_, releaseRuntime, err := agentLoop.AcquireRuntimeGeneration(context.Background(), cfg)
	if err != nil {
		t.Fatalf("AcquireRuntimeGeneration() error = %v", err)
	}
	shutdownDone := make(chan struct{})
	go func() {
		shutdownGateway(
			&services{ChannelManager: channelManager},
			agentLoop,
			provider,
			msgBus,
			true,
		)
		close(shutdownDone)
	}()
	select {
	case <-shutdownEvents:
	case <-time.After(2 * time.Second):
		releaseRuntime()
		t.Fatal("shutdown did not begin")
	}
	select {
	case <-provider.closed:
		releaseRuntime()
		t.Fatal("provider closed while a runtime generation lease was active")
	default:
	}
	select {
	case <-shutdownChannel.stopEntered:
		releaseRuntime()
		t.Fatal("channel dependency stopped while a runtime generation lease was active")
	default:
	}
	select {
	case <-shutdownDone:
		releaseRuntime()
		t.Fatal("shutdown returned while a runtime generation lease was active")
	default:
	}

	releaseRuntime()
	select {
	case <-shutdownChannel.stopEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("channel dependency did not begin stopping after runtime generation drained")
	}
	select {
	case <-provider.closed:
		close(shutdownChannel.allowStop)
		t.Fatal("provider closed before channel dependency finished stopping")
	default:
	}
	close(shutdownChannel.allowStop)
	select {
	case <-provider.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not close after runtime generation drained")
	}
	select {
	case <-shutdownDone:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish after runtime generation drained")
	}
}

type blockingShutdownChannel struct {
	once        sync.Once
	stopEntered chan struct{}
	allowStop   chan struct{}
}

func (*blockingShutdownChannel) Name() string { return "shutdown-order" }

func (*blockingShutdownChannel) Start(context.Context) error { return nil }

func (c *blockingShutdownChannel) Stop(ctx context.Context) error {
	c.once.Do(func() {
		close(c.stopEntered)
	})
	select {
	case <-c.allowStop:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*blockingShutdownChannel) Send(
	context.Context,
	bus.OutboundMessage,
) ([]string, error) {
	return []string{"shutdown-order"}, nil
}

func (*blockingShutdownChannel) IsRunning() bool { return true }

func (*blockingShutdownChannel) IsAllowed(string) bool { return true }

func (*blockingShutdownChannel) IsAllowedSender(bus.SenderInfo) bool { return true }

func (*blockingShutdownChannel) ReasoningChannelID() string { return "" }

func TestCleanupFailedGatewayStartupClosesOwnedResources(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	msgBus := bus.NewMessageBus()
	provider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(cfg, msgBus, provider)
	msgBus.SetEventPublisher(agentLoop.RuntimeEventBus())

	_, releaseRuntime, err := agentLoop.AcquireRuntimeGeneration(context.Background(), cfg)
	if err != nil {
		t.Fatalf("AcquireRuntimeGeneration() error = %v", err)
	}
	sub, shutdownEvents, err := agentLoop.RuntimeEvents().
		OfKind(runtimeevents.KindGatewayShutdown).
		SubscribeChan(
			context.Background(),
			runtimeevents.SubscribeOptions{Name: "failed-startup-runtime-drain", Buffer: 1},
		)
	if err != nil {
		releaseRuntime()
		t.Fatalf("SubscribeChan() error = %v", err)
	}
	defer func() { _ = sub.Close() }()

	cleanupDone := make(chan struct{})
	go func() {
		cleanupFailedGatewayStartup(nil, agentLoop, provider, msgBus)
		close(cleanupDone)
	}()
	select {
	case <-shutdownEvents:
	case <-time.After(2 * time.Second):
		releaseRuntime()
		t.Fatal("failed-startup cleanup did not begin")
	}

	select {
	case <-provider.closed:
		releaseRuntime()
		t.Fatal("failed-startup cleanup closed provider before runtime drained")
	default:
	}
	select {
	case <-cleanupDone:
		releaseRuntime()
		t.Fatal("failed-startup cleanup returned before runtime drained")
	default:
	}
	releaseRuntime()

	select {
	case <-provider.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("failed-startup cleanup did not close the provider after runtime drained")
	}
	select {
	case <-cleanupDone:
	case <-time.After(2 * time.Second):
		t.Fatal("failed-startup cleanup did not finish")
	}
	if err := msgBus.PublishInbound(context.Background(), bus.InboundMessage{
		Channel: "test",
		ChatID:  "test",
		Content: "after close",
	}); !errors.Is(err, bus.ErrBusClosed) {
		t.Fatalf("PublishInbound() after cleanup error = %v, want bus.ErrBusClosed", err)
	}
	if stats := agentLoop.RuntimeEventStats(); !stats.Closed {
		t.Fatalf("runtime event stats after cleanup = %#v, want closed", stats)
	}
}

type orderedShutdownProvider struct {
	once     sync.Once
	closed   chan struct{}
	response string
}

func (p *orderedShutdownProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	response := p.response
	if response == "" {
		response = "unused"
	}
	return &providers.LLMResponse{Content: response}, nil
}

func (*orderedShutdownProvider) GetDefaultModel() string { return "unused" }

func (p *orderedShutdownProvider) Close() {
	p.once.Do(func() {
		close(p.closed)
	})
}

func eventAutomationTestConfig(
	workspace string,
	databasePath string,
	ingressEnabled bool,
	workflowsEnabled bool,
) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.AccountRef = "test-account"
	cfg.Agents.Defaults.ModelName = "test-model"
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "test-model",
		Model: "test-model",
	}}
	cfg.ModelList = config.SecureModelList{&config.ModelConfig{
		ModelName: "test-account",
		Provider:  "openai",
		APIBase:   "http://127.0.0.1",
		Enabled:   true,
	}}
	cfg.Events.Ingress = config.EventIngressConfig{
		Enabled:      ingressEnabled,
		DatabasePath: databasePath,
	}
	cfg.Workflows.Enabled = workflowsEnabled
	return cfg
}

func eventAutomationTestEnvelope(dedupeKey string) eventing.Envelope {
	return eventing.Envelope{
		Source:    "github",
		Connector: "test",
		Type:      "issues.opened",
		DedupeKey: dedupeKey,
		Payload:   json.RawMessage(`{"action":"opened"}`),
	}
}

type eventRetentionPrunerFunc func(context.Context, time.Time, int) (int64, error)

func (f eventRetentionPrunerFunc) Prune(
	ctx context.Context,
	before time.Time,
	limit int,
) (int64, error) {
	return f(ctx, before, limit)
}
