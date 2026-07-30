package workflows

import (
	"errors"
	"fmt"
	"time"
)

// ErrWorkflowScheduleTriggerContext identifies an invalid request to build
// the pure execution context for one exact schedule entry.
var ErrWorkflowScheduleTriggerContext = errors.New("workflow schedule trigger context is invalid")

// WorkflowScheduleRunContext is the pure execution context shared by the
// scheduler and trigger simulation.
type WorkflowScheduleRunContext struct {
	Inputs   map[string]any
	Event    map[string]any
	Session  string
	Delivery Delivery
}

// BuildWorkflowScheduleRunContext builds the exact context used by production
// schedule execution without reading or mutating runtime state.
func BuildWorkflowScheduleRunContext(
	workflow *Workflow,
	ref string,
	index int,
	scheduledAt time.Time,
) (WorkflowScheduleRunContext, error) {
	if workflow == nil ||
		index < 0 ||
		index >= len(workflow.On.Schedule) ||
		scheduledAt.IsZero() {
		return WorkflowScheduleRunContext{}, ErrWorkflowScheduleTriggerContext
	}
	cron := workflow.On.Schedule[index].Cron
	session := WorkflowScheduleSession(ref, index)
	return WorkflowScheduleRunContext{
		Inputs: map[string]any{
			"cron":         cron,
			"scheduled_at": scheduledAt.Format(time.RFC3339),
		},
		Event: map[string]any{
			"trigger":      "schedule",
			"workflow_ref": ref,
			"schedule": map[string]any{
				"cron":         cron,
				"index":        index,
				"scheduled_at": scheduledAt,
			},
		},
		Session:  session,
		Delivery: Delivery{},
	}, nil
}

// WorkflowScheduleSession returns the stable session key used by schedule
// execution and schedule-trigger simulation.
func WorkflowScheduleSession(ref string, index int) string {
	return fmt.Sprintf("workflow:%s:schedule:%d", ref, index)
}
