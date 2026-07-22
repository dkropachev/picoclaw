package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/adhocore/gronx"

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
	ref   string
	index int
	cron  string
	next  time.Time
}

func (al *AgentLoop) startWorkflowAutomations(ctx context.Context) func() {
	if al == nil || al.cfg == nil || !al.cfg.Workflows.Enabled {
		return func() {}
	}
	automationCtx, cancel := context.WithCancel(ctx)
	go al.runScheduledWorkflowTriggers(automationCtx)
	go al.runRuntimeEventWorkflowTriggers(automationCtx)
	return cancel
}

func (al *AgentLoop) runScheduledWorkflowTriggers(ctx context.Context) {
	workspace := al.cfg.WorkspacePath()
	defaultAgent := al.GetRegistry().GetDefaultAgent()
	if defaultAgent == nil {
		return
	}
	schedules := make(map[string]scheduledWorkflowRun)
	refresh := func(now time.Time) {
		next, err := al.loadScheduledWorkflowRuns(ctx, workspace, now, schedules)
		if err != nil {
			logger.WarnCF("workflow", "Failed to refresh workflow schedules", map[string]any{"error": err.Error()})
			return
		}
		schedules = next
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
			for key, schedule := range schedules {
				if schedule.next.IsZero() || schedule.next.After(now) {
					continue
				}
				scheduledAt := schedule.next
				al.publishWorkflowAutomationTriggered(
					schedule.ref,
					"schedule",
					workflowScheduleSession(schedule.ref, schedule.index),
					workflows.Delivery{},
					map[string]any{
						"cron":         schedule.cron,
						"schedule_idx": schedule.index,
						"scheduled_at": scheduledAt,
					},
				)
				executor := al.newWorkflowExecutor(workspace, defaultAgent)
				go al.runScheduledWorkflow(ctx, executor, schedule, scheduledAt)
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
		if err := workflows.EnsureWorkflowRunnable(
			ctx,
			workspace,
			def.Ref,
			workflowRuntimeCompatibility(),
			localOpts...,
		); err != nil {
			logger.WarnCF(
				"workflow",
				"Scheduled workflow skipped until revalidated",
				map[string]any{"ref": def.Ref, "error": err.Error()},
			)
			continue
		}
		workflow, err := workflows.LoadLocal(ctx, workspace, def.Ref, localOpts...)
		if err != nil {
			logger.WarnCF(
				"workflow",
				"Failed to load scheduled workflow",
				map[string]any{"ref": def.Ref, "error": err.Error()},
			)
			continue
		}
		if err := workflows.Validate(workflow); err != nil {
			logger.WarnCF(
				"workflow",
				"Invalid scheduled workflow skipped",
				map[string]any{"ref": def.Ref, "error": err.Error()},
			)
			continue
		}
		for index, schedule := range workflow.On.Schedule {
			if schedule.Cron == "" {
				continue
			}
			key := workflowScheduleKey(def.Ref, index)
			item := scheduledWorkflowRun{ref: def.Ref, index: index, cron: schedule.Cron}
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
	executor *workflows.Executor,
	schedule scheduledWorkflowRun,
	scheduledAt time.Time,
) {
	event := map[string]any{
		"trigger":      "schedule",
		"workflow_ref": schedule.ref,
		"schedule": map[string]any{
			"cron":         schedule.cron,
			"index":        schedule.index,
			"scheduled_at": scheduledAt,
		},
	}
	if _, err := executor.Run(ctx, workflows.RunRequest{
		Ref: schedule.ref,
		Inputs: map[string]any{
			"cron":         schedule.cron,
			"scheduled_at": scheduledAt.Format(time.RFC3339),
		},
		Event:   event,
		Session: workflowScheduleSession(schedule.ref, schedule.index),
	}); err != nil {
		logger.WarnCF("workflow", "Scheduled workflow run failed", map[string]any{
			"ref":   schedule.ref,
			"cron":  schedule.cron,
			"error": err.Error(),
		})
	}
}

func (al *AgentLoop) runRuntimeEventWorkflowTriggers(ctx context.Context) {
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
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			al.handleWorkflowRuntimeEvent(ctx, evt)
		}
	}
}

func (al *AgentLoop) handleWorkflowRuntimeEvent(ctx context.Context, evt runtimeevents.Event) {
	workspace := al.cfg.WorkspacePath()
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
		if err := workflows.EnsureWorkflowRunnable(
			ctx,
			workspace,
			def.Ref,
			workflowRuntimeCompatibility(),
			localOpts...,
		); err != nil {
			logger.WarnCF(
				"workflow",
				"Runtime-event workflow skipped until revalidated",
				map[string]any{"ref": def.Ref, "error": err.Error()},
			)
			continue
		}
		workflow, err := workflows.LoadLocal(ctx, workspace, def.Ref, localOpts...)
		if err != nil {
			logger.WarnCF(
				"workflow",
				"Failed to load runtime-event workflow",
				map[string]any{"ref": def.Ref, "error": err.Error()},
			)
			continue
		}
		if validateErr := workflows.Validate(workflow); validateErr != nil {
			logger.WarnCF(
				"workflow",
				"Invalid runtime-event workflow skipped",
				map[string]any{"ref": def.Ref, "error": validateErr.Error()},
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
		go func(ref string, m *workflows.RuntimeEventMatch) {
			if _, err := executor.Run(ctx, workflows.RunRequest{
				Ref:      ref,
				Inputs:   m.Inputs,
				Event:    m.Event,
				Session:  m.Session,
				Delivery: m.Delivery,
			}); err != nil {
				logger.WarnCF(
					"workflow",
					"Runtime-event workflow run failed",
					map[string]any{"ref": ref, "error": err.Error()},
				)
			}
		}(def.Ref, match)
	}
}

func workflowScheduleKey(ref string, index int) string {
	return fmt.Sprintf("%s#%d", ref, index)
}

func workflowScheduleSession(ref string, index int) string {
	return fmt.Sprintf("workflow:%s:schedule:%d", ref, index)
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
