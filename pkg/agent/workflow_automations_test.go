package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type scheduleAdmissionContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

type workflowTriggerMutationBus struct {
	runtimeevents.Bus
	once   sync.Once
	mutate func()
}

type closedWorkflowRuntimeEventSubscription struct {
	runtimeevents.Subscription
}

func (*closedWorkflowRuntimeEventSubscription) Close() error { return nil }

type closedWorkflowRuntimeEventChannel struct {
	runtimeevents.EventChannel
	events <-chan runtimeevents.Event
}

func (channel closedWorkflowRuntimeEventChannel) SubscribeChan(
	context.Context,
	runtimeevents.SubscribeOptions,
) (runtimeevents.Subscription, <-chan runtimeevents.Event, error) {
	return &closedWorkflowRuntimeEventSubscription{}, channel.events, nil
}

type closedWorkflowRuntimeEventBus struct {
	runtimeevents.Bus
	channel runtimeevents.EventChannel
}

func (eventBus closedWorkflowRuntimeEventBus) Channel() runtimeevents.EventChannel {
	return eventBus.channel
}

func (b *workflowTriggerMutationBus) PublishNonBlocking(
	evt runtimeevents.Event,
) runtimeevents.PublishResult {
	if evt.Kind == runtimeevents.KindWorkflowTriggered && b.mutate != nil {
		b.once.Do(b.mutate)
	}
	return b.Bus.PublishNonBlocking(evt)
}

func (c *scheduleAdmissionContext) Value(key any) any {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Value(key)
}

func TestLoadScheduledWorkflowRunsSkipsUntilRevalidated(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	writeWorkflowAutomationFile(t, workspace, "scheduled.yml", `
name: Scheduled
on:
  schedule:
    - cron: "* * * * *"
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/default
`)
	al := newWorkflowAutomationTestLoop(workspace)
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)

	schedules, err := al.loadScheduledWorkflowRuns(ctx, workspace, now, nil)
	if err != nil {
		t.Fatalf("loadScheduledWorkflowRuns() error = %v", err)
	}
	if len(schedules) != 0 {
		t.Fatalf("schedules before revalidation = %#v, want none", schedules)
	}

	if _, revalidateErr := workflows.RevalidateLocal(
		ctx,
		workspace,
		workflowRuntimeCompatibility(),
	); revalidateErr != nil {
		t.Fatalf("RevalidateLocal() error = %v", revalidateErr)
	}
	schedules, err = al.loadScheduledWorkflowRuns(ctx, workspace, now, nil)
	if err != nil {
		t.Fatalf("loadScheduledWorkflowRuns() after revalidation error = %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("schedules after revalidation = %#v, want one schedule", schedules)
	}
}

func TestScheduledWorkflowRunsMatchedSnapshotAfterDefinitionDrift(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	const workflowA = `
name: Scheduled A
on:
  schedule:
    - cron: "* * * * *"
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: version_a
        uses: agent/default
`
	const workflowB = `
name: Scheduled B
on:
  schedule:
    - cron: "* * * * *"
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: version_b
        uses: agent/default
`
	writeWorkflowAutomationFile(t, workspace, "scheduled.yml", workflowA)
	al := newWorkflowAutomationTestLoop(workspace)
	defer al.Close()
	if _, err := workflows.RevalidateLocal(
		ctx,
		workspace,
		workflowRuntimeCompatibility(),
	); err != nil {
		t.Fatalf("RevalidateLocal(A) error = %v", err)
	}
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	schedules, err := al.loadScheduledWorkflowRuns(ctx, workspace, now, nil)
	if err != nil {
		t.Fatalf("loadScheduledWorkflowRuns() error = %v", err)
	}
	schedule, ok := schedules["workflows/scheduled.yml#0"]
	if !ok || schedule.workflow == nil {
		t.Fatalf("bound schedule = %#v, want exact workflow snapshot", schedule)
	}
	schedule.generation = al.GetConfig()

	writeWorkflowAutomationFile(t, workspace, "scheduled.yml", workflowB)
	if _, err := workflows.RevalidateLocal(
		ctx,
		workspace,
		workflowRuntimeCompatibility(),
	); err != nil {
		t.Fatalf("RevalidateLocal(B) error = %v", err)
	}
	al.runScheduledWorkflow(ctx, schedule, now)

	run := waitForWorkflowRunCompletion(t, workspace)
	if _, ok := run.Steps["main/version_a"]; !ok {
		t.Fatalf("run steps = %#v, want version-A snapshot", run.Steps)
	}
	if _, ok := run.Steps["main/version_b"]; ok {
		t.Fatalf("run steps = %#v, definition B leaked into selected run", run.Steps)
	}
}

func TestHandleWorkflowRuntimeEventSkipsUntilRevalidated(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	writeWorkflowAutomationFile(t, workspace, "runtime.yml", `
name: Runtime
on:
  runtime_event:
    kinds: agent.turn.end
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/default
`)
	al := newWorkflowAutomationTestLoop(workspace)
	evt := runtimeevents.Event{
		Kind: runtimeevents.KindAgentTurnEnd,
		Source: runtimeevents.Source{
			Component: "agent",
			Name:      "main",
		},
		Scope: runtimeevents.Scope{
			AgentID:    "main",
			SessionKey: "agent:main:test",
		},
	}

	al.handleWorkflowRuntimeEvent(ctx, evt)
	assertNoWorkflowRunsWithin(t, workspace, 200*time.Millisecond)

	if _, err := workflows.RevalidateLocal(ctx, workspace, workflowRuntimeCompatibility()); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
	al.handleWorkflowRuntimeEvent(ctx, evt)
	run := waitForWorkflowRunCompletion(t, workspace)
	if run.WorkflowRef != "workflows/runtime.yml" {
		t.Fatalf("workflow ref = %q, want workflows/runtime.yml", run.WorkflowRef)
	}
}

func TestChannelAndCommandWorkflowsRunMatchedSnapshotAfterDefinitionDrift(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
		content string
	}{
		{
			name: "channel message",
			trigger: `channel_message:
    channels: test
    passthrough: false`,
			content: "run snapshot",
		},
		{
			name: "command",
			trigger: `command:
    name: snapshot
    channels: test
    passthrough: false`,
			content: "/snapshot",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			workflow := func(version string) string {
				return `
name: Immediate ` + version + `
on:
  ` + test.trigger + `
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: version_` + version + `
        uses: agent/default
`
			}
			writeWorkflowAutomationFile(t, workspace, "immediate.yml", workflow("a"))
			al := newWorkflowAutomationTestLoop(workspace)
			defer al.Close()
			if _, err := workflows.RevalidateLocal(
				ctx,
				workspace,
				workflowRuntimeCompatibility(),
			); err != nil {
				t.Fatalf("RevalidateLocal(A) error = %v", err)
			}
			al.runtimeEvents = &workflowTriggerMutationBus{
				Bus: al.runtimeEvents,
				mutate: func() {
					writeWorkflowAutomationFile(t, workspace, "immediate.yml", workflow("b"))
					if _, err := workflows.RevalidateLocal(
						ctx,
						workspace,
						workflowRuntimeCompatibility(),
					); err != nil {
						t.Fatalf("RevalidateLocal(B) error = %v", err)
					}
				},
			}

			consumed := al.handleWorkflowTriggers(ctx, bus.InboundMessage{
				Context: bus.InboundContext{
					Channel:  "test",
					ChatID:   "snapshot-chat",
					ChatType: "direct",
					SenderID: "user",
				},
				Content: test.content,
			})
			if !consumed {
				t.Fatal("handleWorkflowTriggers() consumed = false, want true")
			}
			run := waitForWorkflowRunCompletion(t, workspace)
			if _, ok := run.Steps["main/version_a"]; !ok {
				t.Fatalf("run steps = %#v, want matched version-A snapshot", run.Steps)
			}
			if _, ok := run.Steps["main/version_b"]; ok {
				t.Fatalf("run steps = %#v, definition B leaked into selected run", run.Steps)
			}
		})
	}
}

func TestRuntimeEventWorkflowRunsMatchedSnapshotAfterDefinitionDrift(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	workflow := func(version string) string {
		return `
name: Runtime ` + version + `
on:
  runtime_event:
    kinds: gateway.ready
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: version_` + version + `
        uses: agent/default
`
	}
	writeWorkflowAutomationFile(t, workspace, "runtime.yml", workflow("a"))
	al := newWorkflowAutomationTestLoop(workspace)
	defer al.Close()
	if _, err := workflows.RevalidateLocal(
		ctx,
		workspace,
		workflowRuntimeCompatibility(),
	); err != nil {
		t.Fatalf("RevalidateLocal(A) error = %v", err)
	}
	al.runtimeEvents = &workflowTriggerMutationBus{
		Bus: al.runtimeEvents,
		mutate: func() {
			writeWorkflowAutomationFile(t, workspace, "runtime.yml", workflow("b"))
			if _, err := workflows.RevalidateLocal(
				ctx,
				workspace,
				workflowRuntimeCompatibility(),
			); err != nil {
				t.Fatalf("RevalidateLocal(B) error = %v", err)
			}
		},
	}

	al.handleWorkflowRuntimeEvent(ctx, runtimeevents.Event{
		Kind:   runtimeevents.KindGatewayReady,
		Source: runtimeevents.Source{Component: "gateway", Name: "main"},
	})
	run := waitForWorkflowRunCompletion(t, workspace)
	if _, ok := run.Steps["main/version_a"]; !ok {
		t.Fatalf("run steps = %#v, want matched version-A snapshot", run.Steps)
	}
	if _, ok := run.Steps["main/version_b"]; ok {
		t.Fatalf("run steps = %#v, definition B leaked into selected run", run.Steps)
	}
}

func TestScheduledWorkflowRejectsStaleGenerationAfterCrossWorkspaceReload(t *testing.T) {
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	workflowYAML := `
name: Scheduled
on:
  schedule:
    - cron: "* * * * *"
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/default
`
	writeWorkflowAutomationFile(t, workspaceA, "scheduled.yml", workflowYAML)
	writeWorkflowAutomationFile(t, workspaceB, "scheduled.yml", workflowYAML)
	if _, err := workflows.RevalidateLocal(
		context.Background(),
		workspaceB,
		workflowRuntimeCompatibility(),
	); err != nil {
		t.Fatalf("RevalidateLocal(workspace B) error = %v", err)
	}

	al := newWorkflowAutomationTestLoop(workspaceA)
	defer al.Close()
	cfgA := al.GetConfig()
	cfgBValue := *cfgA
	cfgBValue.Agents.Defaults.Workspace = workspaceB
	cfgB := &cfgBValue
	eventCh, closeSubscription := subscribeRuntimeEventsForTest(
		t,
		al,
		4,
		runtimeevents.KindWorkflowTriggered,
	)
	defer closeSubscription()

	resumeRuntime, err := al.PauseRuntimeForReload(context.Background())
	if err != nil {
		t.Fatalf("PauseRuntimeForReload() error = %v", err)
	}
	previous, err := al.ReloadProviderAndConfigRetainingPrevious(
		context.Background(),
		&mockProvider{},
		cfgB,
	)
	if err != nil {
		resumeRuntime()
		t.Fatalf("ReloadProviderAndConfigRetainingPrevious() error = %v", err)
	}
	if previous == nil {
		resumeRuntime()
		t.Fatal("reload did not retain the workspace A provider")
	}

	admissionEntered := make(chan struct{})
	runDone := make(chan struct{})
	runCtx := &scheduleAdmissionContext{
		Context: context.Background(),
		entered: admissionEntered,
	}
	go func() {
		al.runScheduledWorkflow(runCtx, scheduledWorkflowRun{
			ref:        "workflows/scheduled.yml",
			index:      0,
			cron:       "* * * * *",
			next:       time.Now().UTC(),
			generation: cfgA,
		}, time.Now().UTC())
		close(runDone)
	}()
	select {
	case <-admissionEntered:
	case <-time.After(2 * time.Second):
		resumeRuntime()
		t.Fatal("stale schedule did not reach runtime admission")
	}
	resumeRuntime()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stale scheduled workflow did not leave runtime admission")
	}

	runs, err := workflows.NewFileRunStore(workspaceB).ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns(workspace B) error = %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("stale workspace A schedule created workspace B runs: %#v", runs)
	}
	select {
	case event := <-eventCh:
		t.Fatalf("stale schedule published workflow-triggered event: %#v", event)
	default:
	}
}

func TestRuntimeEventWorkflowPumpFollowsWorkflowEnableReloads(t *testing.T) {
	workspace := t.TempDir()
	cfgDisabled := workflowAutomationTestConfig(workspace, false)
	al := newTestAgentLoopWithStrictModels(cfgDisabled, bus.NewMessageBus(), &mockProvider{})
	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- al.Run(runCtx) }()
	defer func() {
		cancelRun()
		al.Stop()
		if err := al.WaitStopped(context.Background()); err != nil {
			t.Errorf("WaitStopped() error = %v", err)
		}
		al.Close()
	}()
	waitForWorkflowAutomationController(t, al)
	if got := workflowRuntimeEventSubscriberCount(al); got != 0 {
		t.Fatalf("runtime-event workflow subscribers while disabled = %d, want 0", got)
	}

	cfgEnabledValue := *cfgDisabled
	cfgEnabledValue.Workflows.Enabled = true
	cfgEnabled := &cfgEnabledValue
	if err := al.ReloadProviderAndConfig(context.Background(), &mockProvider{}, cfgEnabled); err != nil {
		t.Fatalf("false -> true reload error = %v", err)
	}
	if got := workflowRuntimeEventSubscriberCount(al); got != 1 {
		t.Fatalf("runtime-event workflow subscribers after enable = %d, want 1", got)
	}

	cfgDisabledAgainValue := *cfgEnabled
	cfgDisabledAgainValue.Workflows.Enabled = false
	cfgDisabledAgain := &cfgDisabledAgainValue
	if err := al.ReloadProviderAndConfig(
		context.Background(),
		&mockProvider{},
		cfgDisabledAgain,
	); err != nil {
		t.Fatalf("true -> false reload error = %v", err)
	}
	if got := workflowRuntimeEventSubscriberCount(al); got != 0 {
		t.Fatalf("runtime-event workflow subscribers after disable = %d, want 0", got)
	}

	cfgEnabledAgainValue := *cfgDisabledAgain
	cfgEnabledAgainValue.Workflows.Enabled = true
	if err := al.ReloadProviderAndConfig(
		context.Background(),
		&mockProvider{},
		&cfgEnabledAgainValue,
	); err != nil {
		t.Fatalf("second enable reload error = %v", err)
	}
	if got := workflowRuntimeEventSubscriberCount(al); got != 1 {
		t.Fatalf("runtime-event workflow subscribers after second enable = %d, want 1", got)
	}

	cancelRun()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop")
	}
}

func TestRuntimeEventWorkflowPumpReturnsForClosedSubscription(t *testing.T) {
	events := make(chan runtimeevents.Event)
	close(events)
	loop := &AgentLoop{runtimeEvents: closedWorkflowRuntimeEventBus{
		Bus: runtimeevents.NewBus(),
		channel: closedWorkflowRuntimeEventChannel{
			events: events,
		},
	}}
	ready := make(chan struct{})
	loop.runRuntimeEventWorkflowTriggers(t.Context(), &config.Config{}, ready)
	select {
	case <-ready:
	default:
		t.Fatal("closed runtime-event subscription did not signal readiness")
	}
}

func TestRuntimeEventWorkflowPumpDropsProvisionalEventsOnRollback(t *testing.T) {
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	writeWorkflowAutomationFile(t, workspaceA, "runtime.yml", `
name: Runtime
on:
  runtime_event:
    kinds: gateway.ready
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/default
`)
	if _, err := workflows.RevalidateLocal(
		context.Background(),
		workspaceA,
		workflowRuntimeCompatibility(),
	); err != nil {
		t.Fatalf("RevalidateLocal(workspace A) error = %v", err)
	}
	cfgA := workflowAutomationTestConfig(workspaceA, true)
	providerA := &mockProvider{}
	al := newTestAgentLoopWithStrictModels(cfgA, bus.NewMessageBus(), providerA)
	runCtx, cancelRun := context.WithCancel(context.Background())
	go func() { _ = al.Run(runCtx) }()
	defer func() {
		cancelRun()
		al.Stop()
		_ = al.WaitStopped(context.Background())
		al.Close()
		waitForWorkflowAutomationSQLiteIdle(t, workspaceA)
	}()
	waitForWorkflowRuntimeEventSubscribers(t, al, 1)

	resumeRuntime, err := al.PauseRuntimeForReload(context.Background())
	if err != nil {
		t.Fatalf("PauseRuntimeForReload() error = %v", err)
	}
	if got := workflowRuntimeEventSubscriberCount(al); got != 0 {
		resumeRuntime()
		t.Fatalf("runtime-event subscribers during outer pause = %d, want 0", got)
	}
	cfgBValue := *cfgA
	cfgBValue.Agents.Defaults.Workspace = workspaceB
	cfgB := &cfgBValue
	previous, err := al.ReloadProviderAndConfigRetainingPrevious(
		context.Background(),
		&mockProvider{},
		cfgB,
	)
	if err != nil {
		resumeRuntime()
		t.Fatalf("candidate reload error = %v", err)
	}
	if got := workflowRuntimeEventSubscriberCount(al); got != 0 {
		resumeRuntime()
		t.Fatalf("candidate runtime-event subscribers while transaction paused = %d, want 0", got)
	}
	al.RuntimeEventBus().Publish(context.Background(), runtimeevents.Event{
		Kind:   runtimeevents.KindGatewayReady,
		Source: runtimeevents.Source{Component: "gateway", Name: "candidate"},
	})

	if _, err := al.ReloadProviderAndConfigRetainingPrevious(
		context.Background(),
		previous,
		cfgA,
	); err != nil {
		resumeRuntime()
		t.Fatalf("rollback reload error = %v", err)
	}
	resumeRuntime()
	waitForWorkflowRuntimeEventSubscribers(t, al, 1)
	assertNoWorkflowRunsWithin(t, workspaceA, 100*time.Millisecond)

	al.RuntimeEventBus().Publish(context.Background(), runtimeevents.Event{
		Kind:   runtimeevents.KindGatewayReady,
		Source: runtimeevents.Source{Component: "gateway", Name: "committed"},
	})
	run := waitForWorkflowRunCompletion(t, workspaceA)
	if run.WorkflowRef != "workflows/runtime.yml" {
		t.Fatalf("workflow ref = %q, want workflows/runtime.yml", run.WorkflowRef)
	}
}

func newWorkflowAutomationTestLoop(workspace string) *AgentLoop {
	return newTestAgentLoopWithStrictModels(
		workflowAutomationTestConfig(workspace, true),
		bus.NewMessageBus(),
		&mockProvider{},
	)
}

func workflowAutomationTestConfig(workspace string, enabled bool) *config.Config {
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "mock-model",
				MaxTokens: 4096,
			},
		},
		Workflows: config.WorkflowsConfig{
			Enabled: enabled,
		},
	}
}

func waitForWorkflowAutomationController(t *testing.T, al *AgentLoop) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		al.workflowAutomationMu.RLock()
		started := al.workflowAutomationReset != nil
		al.workflowAutomationMu.RUnlock()
		if started {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("workflow automation controller did not start")
}

func workflowRuntimeEventSubscriberCount(al *AgentLoop) int {
	count := 0
	for _, subscriber := range al.RuntimeEventBus().Stats().SubscriberStats {
		if subscriber.Name == "workflow-runtime-events" {
			count++
		}
	}
	return count
}

func waitForWorkflowRuntimeEventSubscribers(t *testing.T, al *AgentLoop, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if workflowRuntimeEventSubscriberCount(al) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf(
		"runtime-event workflow subscriber count = %d, want %d",
		workflowRuntimeEventSubscriberCount(al),
		want,
	)
}

func writeWorkflowAutomationFile(t *testing.T, workspace, name, contents string) {
	t.Helper()
	dir := filepath.Join(workspace, "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func assertNoWorkflowRunsWithin(t *testing.T, workspace string, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	store := workflows.NewFileRunStore(workspace)
	for time.Now().Before(deadline) {
		runs, err := store.ListRuns(context.Background())
		if err != nil {
			t.Fatalf("ListRuns() error = %v", err)
		}
		if len(runs) > 0 {
			t.Fatalf("runs before revalidation = %#v, want none", runs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForWorkflowRunCompletion(t *testing.T, workspace string) *workflows.Run {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	store := workflows.NewFileRunStore(workspace)
	for time.Now().Before(deadline) {
		runs, err := store.ListRuns(context.Background())
		if err != nil {
			t.Fatalf("ListRuns() error = %v", err)
		}
		if len(runs) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		run := runs[0]
		var terminalEvent string
		switch run.Status {
		case workflows.RunStatusSucceeded:
			terminalEvent = "workflow.run.end"
		case workflows.RunStatusFailed:
			terminalEvent = "workflow.run.failed"
		case workflows.RunStatusCanceled:
			terminalEvent = "workflow.run.canceled"
		default:
			time.Sleep(10 * time.Millisecond)
			continue
		}
		events, err := store.Events(context.Background(), run.ID)
		if err != nil {
			t.Fatalf("Events() error = %v", err)
		}
		for _, event := range events {
			if event.Kind != terminalEvent {
				continue
			}
			return &run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for workflow run completion")
	return nil
}

func waitForWorkflowAutomationSQLiteIdle(t *testing.T, workspace string) {
	t.Helper()
	database := filepath.Join(workspace, "state", "workflows.db")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		idle := true
		for _, companion := range []string{database + "-wal", database + "-shm"} {
			if _, err := os.Stat(companion); err == nil || !os.IsNotExist(err) {
				idle = false
				break
			}
		}
		if idle {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("workflow SQLite pool did not become idle")
}
