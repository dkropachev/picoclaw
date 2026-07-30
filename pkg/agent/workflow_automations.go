package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/adhocore/gronx"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	workflowScheduleTickInterval    = time.Second
	workflowScheduleRefreshInterval = 30 * time.Second
	workflowRuntimeEventBuffer      = 128
)

type scheduledWorkflowRun struct {
	ref        string
	index      int
	cron       string
	next       time.Time
	generation *config.Config
	workflow   *workflows.Workflow
}

type workflowAutomationResetRequest struct {
	running bool
	done    chan struct{}
}

func (al *AgentLoop) startWorkflowAutomations(ctx context.Context) func() {
	if al == nil {
		return func() {}
	}
	automationCtx, cancel := context.WithCancel(ctx)
	reset := make(chan workflowAutomationResetRequest)
	done := make(chan struct{})
	al.workflowAutomationMu.Lock()
	al.workflowAutomationReset = reset
	al.workflowAutomationDone = done
	al.workflowAutomationMu.Unlock()
	scheduleDone := make(chan struct{})
	go func() {
		defer close(scheduleDone)
		al.runScheduledWorkflowTriggers(automationCtx)
	}()
	go al.runWorkflowAutomationController(automationCtx, reset, done)

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			cancel()
			<-done
			<-scheduleDone
			al.workflowAutomationMu.Lock()
			if al.workflowAutomationReset == reset {
				al.workflowAutomationReset = nil
				al.workflowAutomationDone = nil
			}
			al.workflowAutomationMu.Unlock()
		})
	}
}

func (al *AgentLoop) runWorkflowAutomationController(
	ctx context.Context,
	reset <-chan workflowAutomationResetRequest,
	done chan<- struct{},
) {
	defer close(done)
	stopWorkers := func() {}
	if generation := al.workflowAutomationGeneration(); generation != nil {
		stopWorkers = al.launchRuntimeEventWorkflowWorker(ctx, generation)
	}
	defer stopWorkers()
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-reset:
			stopWorkers()
			generation := al.workflowAutomationGeneration()
			if request.running && ctx.Err() == nil && generation != nil {
				stopWorkers = al.launchRuntimeEventWorkflowWorker(ctx, generation)
			} else {
				stopWorkers = func() {}
			}
			close(request.done)
		}
	}
}

func (al *AgentLoop) launchRuntimeEventWorkflowWorker(
	ctx context.Context,
	generation *config.Config,
) func() {
	workerCtx, cancel := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	ready := make(chan struct{})
	go func() {
		defer close(workerDone)
		al.runRuntimeEventWorkflowTriggers(workerCtx, generation, ready)
	}()
	<-ready
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			cancel()
			<-workerDone
		})
	}
}

func (al *AgentLoop) workflowAutomationGeneration() *config.Config {
	cfg := al.GetConfig()
	if cfg == nil || !cfg.Workflows.Enabled {
		return nil
	}
	return cfg
}

func (al *AgentLoop) setWorkflowAutomationsRunning(
	ctx context.Context,
	running bool,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	al.workflowAutomationMu.RLock()
	reset := al.workflowAutomationReset
	controllerDone := al.workflowAutomationDone
	al.workflowAutomationMu.RUnlock()
	if reset == nil || controllerDone == nil {
		return nil
	}
	request := workflowAutomationResetRequest{running: running, done: make(chan struct{})}
	select {
	case reset <- request:
	case <-controllerDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-request.done:
		return nil
	case <-controllerDone:
		return nil
	}
}

func (al *AgentLoop) runScheduledWorkflowTriggers(ctx context.Context) {
	schedules := make(map[string]scheduledWorkflowRun)
	var scheduleGeneration *config.Config
	refresh := func(now time.Time) {
		leaseCtx, releaseRuntime, err := al.acquireRuntimeUse(ctx)
		if err != nil {
			logger.WarnCF(
				"workflow",
				"Failed to acquire scheduled workflow runtime",
				map[string]any{"error": err.Error()},
			)
			return
		}
		defer releaseRuntime()
		cfg := al.GetConfig()
		if cfg == nil {
			return
		}
		if !cfg.Workflows.Enabled {
			schedules = make(map[string]scheduledWorkflowRun)
			scheduleGeneration = cfg
			return
		}
		existing := schedules
		if scheduleGeneration != cfg {
			existing = nil
		}
		next, err := al.loadScheduledWorkflowRuns(
			leaseCtx,
			cfg.WorkspacePath(),
			now,
			existing,
		)
		if err != nil {
			logger.WarnCF("workflow", "Failed to refresh workflow schedules", map[string]any{"error": err.Error()})
			return
		}
		for key, schedule := range next {
			schedule.generation = cfg
			next[key] = schedule
		}
		schedules = next
		scheduleGeneration = cfg
	}
	refresh(time.Now().UTC())

	tick := time.NewTicker(workflowScheduleTickInterval)
	defer tick.Stop()
	refreshTick := time.NewTicker(workflowScheduleRefreshInterval)
	defer refreshTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-refreshTick.C:
			refresh(now.UTC())
		case now := <-tick.C:
			now = now.UTC()
			if al.GetConfig() != scheduleGeneration {
				refresh(now)
				if al.GetConfig() != scheduleGeneration {
					continue
				}
			}
			for key, schedule := range schedules {
				if schedule.next.IsZero() || schedule.next.After(now) {
					continue
				}
				scheduledAt := schedule.next
				runCtx, releaseRuntime, err := al.AcquireRuntimeGeneration(
					ctx,
					schedule.generation,
				)
				if err != nil {
					logger.WarnCF(
						"workflow",
						"Scheduled workflow generation changed before admission",
						map[string]any{"ref": schedule.ref, "error": err.Error()},
					)
					continue
				}
				go func() {
					defer releaseRuntime()
					al.runScheduledWorkflow(runCtx, schedule, scheduledAt)
				}()
				next, err := gronx.NextTickAfter(schedule.cron, now, false)
				if err != nil {
					logger.WarnCF("workflow", "Failed to compute next workflow schedule", map[string]any{
						"ref":   schedule.ref,
						"cron":  schedule.cron,
						"error": err.Error(),
					})
					delete(schedules, key)
					continue
				}
				schedule.next = next.UTC()
				schedules[key] = schedule
			}
		}
	}
}

func (al *AgentLoop) loadScheduledWorkflowRuns(
	ctx context.Context,
	workspace string,
	now time.Time,
	existing map[string]scheduledWorkflowRun,
) (map[string]scheduledWorkflowRun, error) {
	localOpts := []workflows.LocalOption{workflows.WithDefinitionsDir(workflowDefinitionsDir(al))}
	defs, err := workflows.ListLocal(ctx, workspace, localOpts...)
	if err != nil {
		return nil, err
	}
	next := make(map[string]scheduledWorkflowRun)
	for _, def := range defs {
		if def.Error != "" {
			continue
		}
		workflow, err := workflows.LoadRunnableLocalSnapshot(
			ctx,
			workspace,
			def.Ref,
			workflowRuntimeCompatibility(),
			localOpts...,
		)
		if err != nil {
			logger.WarnCF(
				"workflow",
				"Scheduled workflow skipped until revalidated",
				map[string]any{"ref": def.Ref, "error": err.Error()},
			)
			continue
		}
		for index, schedule := range workflow.On.Schedule {
			if schedule.Cron == "" {
				continue
			}
			key := workflowScheduleKey(def.Ref, index)
			item := scheduledWorkflowRun{
				ref:      def.Ref,
				index:    index,
				cron:     schedule.Cron,
				workflow: workflow,
			}
			if prev, ok := existing[key]; ok && prev.cron == schedule.Cron && prev.next.After(now) {
				item.next = prev.next
			} else {
				nextTick, err := gronx.NextTickAfter(schedule.Cron, now, false)
				if err != nil {
					logger.WarnCF("workflow", "Invalid workflow schedule skipped", map[string]any{
						"ref":   def.Ref,
						"cron":  schedule.Cron,
						"error": err.Error(),
					})
					continue
				}
				item.next = nextTick.UTC()
			}
			next[key] = item
		}
	}
	return next, nil
}

func (al *AgentLoop) runScheduledWorkflow(
	ctx context.Context,
	schedule scheduledWorkflowRun,
	scheduledAt time.Time,
) {
	leaseCtx, releaseRuntime, err := al.AcquireRuntimeGeneration(ctx, schedule.generation)
	if err != nil {
		logger.WarnCF(
			"workflow",
			"Failed to acquire scheduled workflow runtime",
			map[string]any{"ref": schedule.ref, "error": err.Error()},
		)
		return
	}
	defer releaseRuntime()
	cfg := al.GetConfig()
	registry := al.GetRegistry()
	if cfg == nil || cfg != schedule.generation || registry == nil {
		return
	}
	defaultAgent := registry.GetDefaultAgent()
	if defaultAgent == nil {
		return
	}
	if schedule.workflow == nil {
		logger.WarnCF(
			"workflow",
			"Scheduled workflow has no bound definition snapshot",
			map[string]any{"ref": schedule.ref},
		)
		return
	}
	runContext, contextErr := workflows.BuildWorkflowScheduleRunContext(
		schedule.workflow,
		schedule.ref,
		schedule.index,
		scheduledAt,
	)
	if contextErr != nil {
		logger.WarnCF(
			"workflow",
			"Scheduled workflow context is invalid",
			map[string]any{"ref": schedule.ref, "error": contextErr.Error()},
		)
		return
	}
	executor := al.newWorkflowExecutor(cfg.WorkspacePath(), defaultAgent)
	al.publishWorkflowAutomationTriggered(
		schedule.ref,
		"schedule",
		runContext.Session,
		runContext.Delivery,
		map[string]any{
			"cron":         schedule.cron,
			"schedule_idx": schedule.index,
			"scheduled_at": scheduledAt,
		},
	)
	if _, err := executor.Run(leaseCtx, workflows.RunRequest{
		Ref:         schedule.ref,
		Workflow:    schedule.workflow,
		WorkflowRef: schedule.ref,
		Inputs:      runContext.Inputs,
		Event:       runContext.Event,
		Session:     runContext.Session,
		Delivery:    runContext.Delivery,
	}); err != nil {
		logger.WarnCF("workflow", "Scheduled workflow run failed", map[string]any{
			"ref":   schedule.ref,
			"cron":  schedule.cron,
			"error": err.Error(),
		})
	}
}

func (al *AgentLoop) runRuntimeEventWorkflowTriggers(
	ctx context.Context,
	generation *config.Config,
	ready chan<- struct{},
) {
	var readyOnce sync.Once
	signalReady := func() {
		readyOnce.Do(func() { close(ready) })
	}
	defer signalReady()
	if al.runtimeEvents == nil {
		return
	}
	sub, ch, err := al.runtimeEvents.Channel().SubscribeChan(ctx, runtimeevents.SubscribeOptions{
		Name:         "workflow-runtime-events",
		Buffer:       workflowRuntimeEventBuffer,
		Backpressure: runtimeevents.DropOldest,
	})
	if err != nil {
		logger.WarnCF("workflow", "Failed to subscribe workflow runtime events", map[string]any{"error": err.Error()})
		return
	}
	defer sub.Close()
	signalReady()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			al.handleWorkflowRuntimeEventForGeneration(ctx, generation, evt)
		}
	}
}

func (al *AgentLoop) handleWorkflowRuntimeEvent(ctx context.Context, evt runtimeevents.Event) {
	al.handleWorkflowRuntimeEventForGeneration(ctx, al.GetConfig(), evt)
}

func (al *AgentLoop) handleWorkflowRuntimeEventForGeneration(
	ctx context.Context,
	generation *config.Config,
	evt runtimeevents.Event,
) {
	if generation == nil {
		return
	}
	leaseCtx, releaseRuntime, err := al.AcquireRuntimeGeneration(ctx, generation)
	if err != nil {
		logger.WarnCF(
			"workflow",
			"Failed to acquire runtime-event workflow runtime",
			map[string]any{"error": err.Error()},
		)
		return
	}
	defer releaseRuntime()
	ctx = leaseCtx

	cfg := al.GetConfig()
	if cfg == nil || !cfg.Workflows.Enabled {
		return
	}
	workspace := cfg.WorkspacePath()
	defaultAgent := al.GetRegistry().GetDefaultAgent()
	if defaultAgent == nil {
		return
	}
	localOpts := []workflows.LocalOption{workflows.WithDefinitionsDir(workflowDefinitionsDir(al))}
	defs, err := workflows.ListLocal(ctx, workspace, localOpts...)
	if err != nil {
		logger.WarnCF("workflow", "Failed to list runtime-event workflows", map[string]any{"error": err.Error()})
		return
	}
	for _, def := range defs {
		if def.Error != "" {
			continue
		}
		workflow, err := workflows.LoadRunnableLocalSnapshot(
			ctx,
			workspace,
			def.Ref,
			workflowRuntimeCompatibility(),
			localOpts...,
		)
		if err != nil {
			logger.WarnCF(
				"workflow",
				"Runtime-event workflow skipped until revalidated",
				map[string]any{"ref": def.Ref, "error": err.Error()},
			)
			continue
		}
		match, ok, err := workflows.MatchRuntimeEvent(workflow, def.Ref, evt)
		if err != nil {
			logger.WarnCF(
				"workflow",
				"Workflow runtime-event trigger evaluation failed",
				map[string]any{"ref": def.Ref, "error": err.Error()},
			)
			continue
		}
		if !ok {
			continue
		}
		al.publishWorkflowAutomationTriggered(def.Ref, "runtime_event", match.Session, match.Delivery, map[string]any{
			"event_kind": evt.Kind.String(),
			"event_id":   evt.ID,
		})
		executor := al.newWorkflowExecutor(workspace, defaultAgent)
		workflowCtx, releaseWorkflow, err := al.retainRuntimeUse(ctx)
		if err != nil {
			logger.WarnCF(
				"workflow",
				"Failed to retain runtime-event workflow runtime",
				map[string]any{"ref": def.Ref, "error": err.Error()},
			)
			continue
		}
		go func(
			runCtx context.Context,
			release func(),
			ref string,
			workflow *workflows.Workflow,
			m *workflows.RuntimeEventMatch,
		) {
			defer release()
			if _, err := executor.Run(runCtx, workflows.RunRequest{
				Ref:         ref,
				Workflow:    workflow,
				WorkflowRef: ref,
				Inputs:      m.Inputs,
				Event:       m.Event,
				Session:     m.Session,
				Delivery:    m.Delivery,
			}); err != nil {
				logger.WarnCF(
					"workflow",
					"Runtime-event workflow run failed",
					map[string]any{"ref": ref, "error": err.Error()},
				)
			}
		}(workflowCtx, releaseWorkflow, def.Ref, workflow, match)
	}
}

func workflowScheduleKey(ref string, index int) string {
	return fmt.Sprintf("%s#%d", ref, index)
}

func (al *AgentLoop) publishWorkflowAutomationTriggered(
	ref, trigger, session string,
	delivery workflows.Delivery,
	payload map[string]any,
) {
	if payload == nil {
		payload = make(map[string]any)
	}
	payload["workflow_ref"] = ref
	payload["trigger"] = trigger
	al.publishRuntimeEvent(runtimeevents.Event{
		Kind:   runtimeevents.KindWorkflowTriggered,
		Source: runtimeevents.Source{Component: "workflow", Name: ref},
		Scope: runtimeevents.Scope{
			SessionKey: session,
			Channel:    delivery.Channel,
			ChatID:     delivery.ChatID,
			TopicID:    delivery.TopicID,
			MessageID:  delivery.MessageID,
		},
		Severity: runtimeevents.SeverityInfo,
		Payload:  payload,
	})
}
