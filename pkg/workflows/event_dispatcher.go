package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
)

const (
	DefaultEventWorkerPollInterval = 250 * time.Millisecond
	DefaultEventRoutingLease       = 30 * time.Second
	DefaultEventDispatchLease      = 30 * time.Second
	DefaultEventMaxAttempts        = 8
	DefaultEventRetryBase          = time.Second
	DefaultEventRetryMax           = 5 * time.Minute
)

// EventRoutingInbox is the durable boundary used while matching external
// events to workflow definitions.
type EventRoutingInbox interface {
	eventing.RoutingQueue
	eventing.RevisionRoutingDispatchCreator
}

// EventDispatchInbox is the durable boundary used while delivering a matched
// external event to a workflow run.
type EventDispatchInbox interface {
	eventing.EventStore
	eventing.DispatchQueue
	eventing.DispatchLeaseRenewer
}

// EventWorkflowExecutor is the narrow execution contract used by the durable
// dispatcher. Implementations must durably create the requested run and invoke
// RunRequest.OnRunPersisted before workflow side effects. *Executor satisfies
// this contract.
type EventWorkflowExecutor interface {
	Run(ctx context.Context, req RunRequest) (*RunResult, error)
}

// SucceededEventRunSink captures an opt-in output from a successfully
// completed event workflow before its dispatch is acknowledged.
// Implementations must be idempotent for the immutable event, dispatch, and
// run identity because reconciliation can repeat after a crash or when a later
// sink in an ordered fanout fails.
type SucceededEventRunSink interface {
	CaptureSucceededEventRun(
		ctx context.Context,
		event eventing.Envelope,
		dispatch eventing.Dispatch,
		run *Run,
	) error
}

// EventReviewSink is retained as a source-compatible alias for the original
// review-only successful-run sink contract.
type EventReviewSink = SucceededEventRunSink

// SucceededEventRunSinkFanout invokes successful-run sinks in declared order.
// It stops at the first failure so a dispatch is not acknowledged until every
// preceding and current sink has durably accepted the run. Retried invocations
// rely on each sink's idempotency contract.
type SucceededEventRunSinkFanout []SucceededEventRunSink

func (sinks SucceededEventRunSinkFanout) CaptureSucceededEventRun(
	ctx context.Context,
	event eventing.Envelope,
	dispatch eventing.Dispatch,
	run *Run,
) error {
	for index, sink := range sinks {
		if sink == nil {
			continue
		}
		if err := sink.CaptureSucceededEventRun(
			ctx,
			event.Clone(),
			cloneEventDispatch(dispatch),
			cloneRun(run),
		); err != nil {
			return fmt.Errorf("successful event run sink %d: %w", index+1, err)
		}
	}
	return nil
}

func cloneEventDispatch(dispatch eventing.Dispatch) eventing.Dispatch {
	out := dispatch
	if dispatch.LeaseUntil != nil {
		value := *dispatch.LeaseUntil
		out.LeaseUntil = &value
	}
	if dispatch.LinkedAt != nil {
		value := *dispatch.LinkedAt
		out.LinkedAt = &value
	}
	if dispatch.FinishedAt != nil {
		value := *dispatch.FinishedAt
		out.FinishedAt = &value
	}
	return out
}

// EventWorkflowRouter durably fans one claimed external event out to every
// currently matching, runnable local workflow.
type EventWorkflowRouter struct {
	Inbox                EventRoutingInbox
	WorkspaceDir         string
	DefinitionsDir       string
	RuntimeCompatibility RuntimeCompatibility
	RuntimeEvents        RuntimeEventPublisher
	WorkerLabel          string
	LeaseDuration        time.Duration
	MaxAttempts          int
	RetryBase            time.Duration
	RetryMax             time.Duration
	Now                  func() time.Time
}

// ProcessOne claims and routes at most one event. processed is false when no
// routing work was available.
func (r *EventWorkflowRouter) ProcessOne(ctx context.Context) (processed bool, err error) {
	if r == nil || r.Inbox == nil {
		return false, fmt.Errorf("event workflow router inbox is required")
	}
	claimed, err := r.Inbox.ClaimRouting(
		ctx,
		defaultString(r.WorkerLabel, "workflow-router"),
		1,
		defaultDuration(r.LeaseDuration, DefaultEventRoutingLease),
	)
	if err != nil {
		return false, fmt.Errorf("claim event routing: %w", err)
	}
	if len(claimed) == 0 {
		return false, nil
	}
	item := claimed[0]
	if err := r.renewRoutingLease(ctx, item); err != nil {
		renewalErr := fmt.Errorf("renew event routing %s before matching: %w", item.Envelope.ID, err)
		return true, errors.Join(renewalErr, r.retryRouting(ctx, item, renewalErr))
	}
	routeCtx, cancelRoute := context.WithCancel(ctx)
	defer cancelRoute()
	stopHeartbeat := r.startRoutingHeartbeat(routeCtx, cancelRoute, item)
	routeErr := r.routeClaim(routeCtx, item)
	heartbeatErr := stopHeartbeat()
	cancelRoute()
	if heartbeatErr != nil {
		renewalErr := fmt.Errorf(
			"renew lease for event routing %s: %w",
			item.Envelope.ID,
			heartbeatErr,
		)
		return true, errors.Join(
			routeErr,
			renewalErr,
			r.retryRouting(ctx, item, renewalErr),
		)
	}
	if routeErr != nil {
		return true, errors.Join(
			routeErr,
			r.retryRouting(ctx, item, routeErr),
		)
	}
	if err := r.Inbox.AckRouting(ctx, item.Envelope.ID, item.Routing.LeaseToken); err != nil {
		return true, fmt.Errorf("ack event routing %s: %w", item.Envelope.ID, err)
	}
	return true, nil
}

func (r *EventWorkflowRouter) routeClaim(ctx context.Context, item eventing.StoredEvent) error {
	localOpts := eventLocalOptions(r.DefinitionsDir)
	definitions, err := ListLocal(ctx, r.WorkspaceDir, localOpts...)
	if err != nil {
		return fmt.Errorf("list event workflow definitions: %w", err)
	}
	for _, definition := range definitions {
		if definition.Error != "" {
			continue
		}
		var snapshot *LocalWorkflowSnapshot
		if runtimeCompatibilityConfigured(r.RuntimeCompatibility) {
			snapshot, err = LoadRunnableLocalSnapshotWithRevision(
				ctx,
				r.WorkspaceDir,
				definition.Ref,
				r.RuntimeCompatibility,
				localOpts...,
			)
			if err != nil {
				continue
			}
		} else {
			snapshot, err = LoadValidatedLocalSnapshot(
				ctx,
				r.WorkspaceDir,
				definition.Ref,
				localOpts...,
			)
			if err != nil {
				continue
			}
		}
		if !WorkflowMatchesEvent(snapshot.Workflow, item.Envelope) {
			continue
		}
		dispatch, created, err := r.Inbox.CreateRevisionedDispatchForRoutingClaim(
			ctx,
			item.Envelope.ID,
			item.Routing.LeaseToken,
			definition.Ref,
			snapshot.Revision,
		)
		if err != nil {
			return fmt.Errorf("create event dispatch for %s: %w", definition.Ref, err)
		}
		if created {
			r.publishTriggered(item.Envelope, dispatch)
		}
	}
	return nil
}

func (r *EventWorkflowRouter) renewRoutingLease(
	ctx context.Context,
	item eventing.StoredEvent,
) error {
	renewer, ok := r.Inbox.(eventing.RoutingLeaseRenewer)
	if !ok {
		return nil
	}
	return renewer.RenewRoutingLease(
		ctx,
		item.Envelope.ID,
		item.Routing.LeaseToken,
		defaultDuration(r.LeaseDuration, DefaultEventRoutingLease),
	)
}

func (r *EventWorkflowRouter) startRoutingHeartbeat(
	ctx context.Context,
	cancelRoute context.CancelFunc,
	item eventing.StoredEvent,
) func() error {
	renewer, ok := r.Inbox.(eventing.RoutingLeaseRenewer)
	if !ok {
		return func() error { return nil }
	}
	lease := defaultDuration(r.LeaseDuration, DefaultEventRoutingLease)
	interval := lease / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				done <- nil
				return
			case <-ticker.C:
				if err := renewer.RenewRoutingLease(
					heartbeatCtx,
					item.Envelope.ID,
					item.Routing.LeaseToken,
					lease,
				); err != nil {
					if heartbeatCtx.Err() != nil {
						done <- nil
						return
					}
					cancelRoute()
					done <- err
					return
				}
			}
		}
	}()
	return func() error {
		cancelHeartbeat()
		return <-done
	}
}

func (r *EventWorkflowRouter) retryRouting(
	ctx context.Context,
	item eventing.StoredEvent,
	cause error,
) error {
	attempts := item.Routing.Attempts
	if attempts >= defaultInt(r.MaxAttempts, DefaultEventMaxAttempts) {
		if err := r.Inbox.DeadRouting(
			ctx,
			item.Envelope.ID,
			item.Routing.LeaseToken,
			cause.Error(),
		); err != nil {
			return fmt.Errorf("mark event routing %s dead: %w", item.Envelope.ID, err)
		}
		return nil
	}
	availableAt := r.now().Add(eventRetryDelay(
		attempts,
		defaultDuration(r.RetryBase, DefaultEventRetryBase),
		defaultDuration(r.RetryMax, DefaultEventRetryMax),
	))
	if err := r.Inbox.NackRouting(
		ctx,
		item.Envelope.ID,
		item.Routing.LeaseToken,
		availableAt,
		cause.Error(),
	); err != nil {
		return fmt.Errorf("retry event routing %s: %w", item.Envelope.ID, err)
	}
	return nil
}

func (r *EventWorkflowRouter) publishTriggered(
	envelope eventing.Envelope,
	dispatch eventing.Dispatch,
) {
	if r.RuntimeEvents == nil {
		return
	}
	session := EventWorkflowSession(dispatch.WorkflowRef, envelope.ID)
	r.RuntimeEvents.PublishNonBlocking(runtimeevents.Event{
		Kind:   runtimeevents.KindWorkflowTriggered,
		Time:   r.now(),
		Source: runtimeevents.Source{Component: "workflow", Name: dispatch.WorkflowRef},
		Scope:  runtimeevents.Scope{SessionKey: session},
		Correlation: runtimeevents.Correlation{
			RequestID: envelope.ID,
		},
		Severity: runtimeevents.SeverityInfo,
		Attrs: map[string]any{
			"trigger":     "event",
			"event_id":    envelope.ID,
			"dispatch_id": dispatch.ID,
			"source":      envelope.Source,
			"connector":   envelope.Connector,
			"event_type":  envelope.Type,
		},
	})
}

func (r *EventWorkflowRouter) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// EventWorkflowDispatcher durably delivers one matched event to its
// deterministic workflow run.
type EventWorkflowDispatcher struct {
	Inbox                EventDispatchInbox
	Executor             EventWorkflowExecutor
	RunStore             RunStore
	WorkspaceDir         string
	DefinitionsDir       string
	RuntimeCompatibility RuntimeCompatibility
	SucceededRunSink     SucceededEventRunSink
	// ReviewSink is the deprecated review-only field. It is used only when
	// SucceededRunSink is nil so existing embedders retain their prior behavior.
	ReviewSink    EventReviewSink
	WorkerLabel   string
	LeaseDuration time.Duration
	MaxAttempts   int
	RetryBase     time.Duration
	RetryMax      time.Duration
	Now           func() time.Time
}

// ProcessOne claims and dispatches at most one workflow delivery. processed is
// false when no dispatch work was available.
func (d *EventWorkflowDispatcher) ProcessOne(ctx context.Context) (processed bool, err error) {
	if d == nil || d.Inbox == nil {
		return false, fmt.Errorf("event workflow dispatcher inbox is required")
	}
	if d.Executor == nil {
		return false, fmt.Errorf("event workflow executor is required")
	}
	if d.RunStore == nil {
		return false, fmt.Errorf("event workflow run store is required")
	}
	claimed, err := d.Inbox.ClaimDispatches(
		ctx,
		defaultString(d.WorkerLabel, "workflow-dispatcher"),
		1,
		defaultDuration(d.LeaseDuration, DefaultEventDispatchLease),
	)
	if err != nil {
		return false, fmt.Errorf("claim event dispatch: %w", err)
	}
	if len(claimed) == 0 {
		return false, nil
	}
	dispatch := claimed[0]
	if err := d.renewDispatchLease(ctx, dispatch); err != nil {
		renewalErr := fmt.Errorf(
			"renew event dispatch %s before reconciliation: %w",
			dispatch.ID,
			err,
		)
		return true, errors.Join(renewalErr, d.retryDispatch(ctx, dispatch, renewalErr))
	}

	dispatchCtx, cancelDispatch := context.WithCancel(ctx)
	stopHeartbeat := d.startHeartbeat(dispatchCtx, cancelDispatch, dispatch)
	dispatchErr := d.dispatchClaim(dispatchCtx, dispatch)
	heartbeatErr := stopHeartbeat()
	cancelDispatch()
	if heartbeatErr != nil {
		latest, latestErr := d.Inbox.GetDispatch(ctx, dispatch.ID)
		if latestErr == nil && dispatchLeaseReleased(latest.Status) {
			return true, dispatchErr
		}
		return true, errors.Join(
			dispatchErr,
			fmt.Errorf(
				"renew lease for event dispatch %s: %w",
				dispatch.ID,
				heartbeatErr,
			),
		)
	}
	return true, dispatchErr
}

func dispatchLeaseReleased(status eventing.DispatchStatus) bool {
	switch status {
	case eventing.DispatchPending,
		eventing.DispatchSucceeded,
		eventing.DispatchFailed,
		eventing.DispatchDead:
		return true
	default:
		return false
	}
}

func (d *EventWorkflowDispatcher) dispatchClaim(
	ctx context.Context,
	dispatch eventing.Dispatch,
) error {
	stored, err := d.Inbox.Get(ctx, dispatch.EventID)
	if err != nil {
		return errors.Join(
			fmt.Errorf("load event %s for dispatch %s: %w", dispatch.EventID, dispatch.ID, err),
			d.retryDispatch(ctx, dispatch, err),
		)
	}

	existing, err := d.RunStore.GetRun(ctx, dispatch.RunID)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	switch {
	case err == nil && existing != nil:
		return d.reconcileRun(ctx, stored.Envelope, dispatch, existing)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return errors.Join(
			fmt.Errorf("load workflow run %s: %w", dispatch.RunID, err),
			d.retryDispatch(ctx, dispatch, err),
		)
	}
	if dispatch.LinkedAt != nil {
		missingRunErr := fmt.Errorf(
			"workflow run %s was previously created but its durable record is missing; refusing replay",
			dispatch.RunID,
		)
		if finishErr := d.Inbox.FinishDispatch(
			ctx,
			dispatch.ID,
			dispatch.LeaseToken,
			eventing.DispatchFailed,
			missingRunErr.Error(),
		); finishErr != nil {
			return errors.Join(missingRunErr, finishErr)
		}
		return missingRunErr
	}

	snapshot, err := d.loadRunnableWorkflow(ctx, dispatch.WorkflowRef)
	if err != nil {
		return errors.Join(err, d.retryDispatch(ctx, dispatch, err))
	}
	if dispatch.WorkflowRevision != "" &&
		dispatch.WorkflowRevision != snapshot.Revision {
		revisionErr := fmt.Errorf(
			"event workflow %s revision changed after dispatch selection",
			dispatch.WorkflowRef,
		)
		return errors.Join(revisionErr, d.retryDispatch(ctx, dispatch, revisionErr))
	}
	if !WorkflowMatchesEvent(snapshot.Workflow, stored.Envelope) {
		triggerErr := fmt.Errorf(
			"event workflow %s no longer matches dispatch event %s",
			dispatch.WorkflowRef,
			dispatch.EventID,
		)
		return errors.Join(triggerErr, d.retryDispatch(ctx, dispatch, triggerErr))
	}
	runContext, err := EventWorkflowRunContextFromEnvelope(
		dispatch.WorkflowRef,
		dispatch.ID,
		stored.Envelope,
	)
	if err != nil {
		return errors.Join(err, d.retryDispatch(ctx, dispatch, err))
	}
	if err := d.renewDispatchLease(ctx, dispatch); err != nil {
		renewalErr := fmt.Errorf(
			"renew event dispatch %s before run creation: %w",
			dispatch.ID,
			err,
		)
		return errors.Join(renewalErr, d.retryDispatch(ctx, dispatch, renewalErr))
	}

	result, runErr := d.Executor.Run(ctx, RunRequest{
		RunID:       dispatch.RunID,
		Ref:         dispatch.WorkflowRef,
		Workflow:    snapshot.Workflow,
		WorkflowRef: dispatch.WorkflowRef,
		Inputs:      runContext.Inputs,
		Event:       runContext.Event,
		Origin:      runContext.Origin,
		Session:     runContext.Session,
		Delivery:    runContext.Delivery,
		OnRunPersisted: func(run *Run) error {
			if run == nil || run.ID != dispatch.RunID {
				return fmt.Errorf(
					"persisted workflow run identity mismatch: got %#v, want %q",
					run,
					dispatch.RunID,
				)
			}
			if err := d.Inbox.LinkDispatchRun(
				ctx,
				dispatch.ID,
				dispatch.LeaseToken,
				run.ID,
			); err != nil {
				return fmt.Errorf("link event dispatch to durable run: %w", err)
			}
			if err := d.renewDispatchLease(ctx, dispatch); err != nil {
				return fmt.Errorf("renew linked event dispatch before workflow execution: %w", err)
			}
			return nil
		},
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return errors.Join(runErr, ctxErr)
	}

	latest, latestErr := d.RunStore.GetRun(ctx, dispatch.RunID)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return errors.Join(runErr, ctxErr)
	}
	if latestErr == nil && latest != nil {
		reconcileErr := d.reconcileRun(ctx, stored.Envelope, dispatch, latest)
		var resultErr error
		if result != nil &&
			(result.RunID != latest.ID || (result.Status != "" && result.Status != latest.Status)) {
			resultErr = fmt.Errorf(
				"workflow executor result mismatch: got run %q status %q, durable run is %q status %q",
				result.RunID,
				result.Status,
				latest.ID,
				latest.Status,
			)
		}
		if errors.Is(runErr, ErrRunAlreadyExists) && reconcileErr == nil && resultErr == nil {
			return nil
		}
		if runErr != nil {
			return errors.Join(
				fmt.Errorf("event workflow execution failed: %w", runErr),
				reconcileErr,
				resultErr,
			)
		}
		return errors.Join(reconcileErr, resultErr)
	}
	if latestErr != nil && !errors.Is(latestErr, fs.ErrNotExist) {
		return errors.Join(
			fmt.Errorf("inspect workflow run %s after execution: %w", dispatch.RunID, latestErr),
			runErr,
		)
	}
	if result != nil {
		detail := fmt.Sprintf(
			"workflow executor returned run %q status %q without durable run %q",
			result.RunID,
			result.Status,
			dispatch.RunID,
		)
		if result.Error != "" {
			detail += ": " + result.Error
		}
		if err := d.Inbox.FinishDispatch(
			ctx,
			dispatch.ID,
			dispatch.LeaseToken,
			eventing.DispatchFailed,
			detail,
		); err != nil {
			return errors.Join(
				fmt.Errorf("reject non-durable workflow result for dispatch %s: %s", dispatch.ID, detail),
				err,
				runErr,
			)
		}
		return errors.Join(fmt.Errorf("%s", detail), runErr)
	}
	if runErr == nil {
		runErr = fmt.Errorf("workflow executor returned neither a result nor a durable run")
	}
	return errors.Join(
		fmt.Errorf("start workflow run %s: %w", dispatch.RunID, runErr),
		d.retryDispatch(ctx, dispatch, runErr),
	)
}

func (d *EventWorkflowDispatcher) loadRunnableWorkflow(
	ctx context.Context,
	ref string,
) (*LocalWorkflowSnapshot, error) {
	localOpts := eventLocalOptions(d.DefinitionsDir)
	if runtimeCompatibilityConfigured(d.RuntimeCompatibility) {
		snapshot, err := LoadRunnableLocalSnapshotWithRevision(
			ctx,
			d.WorkspaceDir,
			ref,
			d.RuntimeCompatibility,
			localOpts...,
		)
		if err != nil {
			return nil, fmt.Errorf("event workflow %s is not runnable: %w", ref, err)
		}
		return snapshot, nil
	}
	snapshot, err := LoadValidatedLocalSnapshot(ctx, d.WorkspaceDir, ref, localOpts...)
	if err != nil {
		return nil, fmt.Errorf("load event workflow %s: %w", ref, err)
	}
	return snapshot, nil
}

func (d *EventWorkflowDispatcher) reconcileRun(
	ctx context.Context,
	envelope eventing.Envelope,
	dispatch eventing.Dispatch,
	run *Run,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.renewDispatchLease(ctx, dispatch); err != nil {
		renewalErr := fmt.Errorf(
			"renew event dispatch %s before run reconciliation: %w",
			dispatch.ID,
			err,
		)
		return errors.Join(renewalErr, d.retryDispatch(ctx, dispatch, renewalErr))
	}
	if run == nil {
		return fmt.Errorf("workflow run %s is nil", dispatch.RunID)
	}
	if run.ID != dispatch.RunID || run.WorkflowRef != dispatch.WorkflowRef {
		identityErr := fmt.Errorf(
			"workflow run identity mismatch for dispatch %s: got id %q ref %q, want id %q ref %q",
			dispatch.ID,
			run.ID,
			run.WorkflowRef,
			dispatch.RunID,
			dispatch.WorkflowRef,
		)
		return errors.Join(identityErr, d.retryDispatch(ctx, dispatch, identityErr))
	}
	switch run.Status {
	case RunStatusSucceeded:
		if err := d.captureSucceededRun(ctx, envelope, dispatch, run); err != nil {
			captureErr := fmt.Errorf(
				"capture successful event run for dispatch %s: %w",
				dispatch.ID,
				err,
			)
			return errors.Join(captureErr, d.retryDispatch(ctx, dispatch, captureErr))
		}
		if err := d.Inbox.FinishDispatch(
			ctx,
			dispatch.ID,
			dispatch.LeaseToken,
			eventing.DispatchSucceeded,
			"",
		); err != nil {
			return fmt.Errorf("reconcile successful event dispatch %s: %w", dispatch.ID, err)
		}
		return nil
	case RunStatusFailed, RunStatusCanceled, RunStatusSkipped:
		detail := firstNonEmpty(run.Error, run.CancelReason, "workflow run did not succeed")
		if err := d.Inbox.FinishDispatch(
			ctx,
			dispatch.ID,
			dispatch.LeaseToken,
			eventing.DispatchFailed,
			detail,
		); err != nil {
			return fmt.Errorf("reconcile failed event dispatch %s: %w", dispatch.ID, err)
		}
		return nil
	default:
		reason := "event dispatch recovered after an interrupted workflow execution"
		canceled, err := d.RunStore.CancelRun(ctx, run.ID, reason)
		if err != nil {
			return errors.Join(
				fmt.Errorf("cancel interrupted workflow run %s: %w", run.ID, err),
				d.retryDispatch(ctx, dispatch, err),
			)
		}
		if canceled == nil {
			cancelErr := fmt.Errorf("cancel interrupted workflow run %s returned no run", run.ID)
			return errors.Join(cancelErr, d.retryDispatch(ctx, dispatch, cancelErr))
		}
		if canceled.ID != dispatch.RunID || canceled.WorkflowRef != dispatch.WorkflowRef {
			identityErr := fmt.Errorf(
				"canceled workflow run identity mismatch for dispatch %s",
				dispatch.ID,
			)
			return errors.Join(identityErr, d.retryDispatch(ctx, dispatch, identityErr))
		}
		switch canceled.Status {
		case RunStatusSucceeded:
			if err := d.captureSucceededRun(ctx, envelope, dispatch, canceled); err != nil {
				captureErr := fmt.Errorf(
					"capture concurrently successful event run for dispatch %s: %w",
					dispatch.ID,
					err,
				)
				return errors.Join(captureErr, d.retryDispatch(ctx, dispatch, captureErr))
			}
			if err := d.Inbox.FinishDispatch(
				ctx,
				dispatch.ID,
				dispatch.LeaseToken,
				eventing.DispatchSucceeded,
				"",
			); err != nil {
				return fmt.Errorf("finish concurrently successful event dispatch %s: %w", dispatch.ID, err)
			}
			return nil
		case RunStatusFailed, RunStatusCanceled, RunStatusSkipped:
			detail := firstNonEmpty(canceled.CancelReason, canceled.Error, reason)
			if err := d.Inbox.FinishDispatch(
				ctx,
				dispatch.ID,
				dispatch.LeaseToken,
				eventing.DispatchFailed,
				detail,
			); err != nil {
				return fmt.Errorf("finish interrupted event dispatch %s: %w", dispatch.ID, err)
			}
			return nil
		default:
			cancelErr := fmt.Errorf(
				"workflow run %s remained nonterminal after interruption cancellation",
				run.ID,
			)
			return errors.Join(cancelErr, d.retryDispatch(ctx, dispatch, cancelErr))
		}
	}
}

func (d *EventWorkflowDispatcher) captureSucceededRun(
	ctx context.Context,
	envelope eventing.Envelope,
	dispatch eventing.Dispatch,
	run *Run,
) error {
	sink := d.SucceededRunSink
	if sink == nil {
		sink = d.ReviewSink
	}
	if sink == nil {
		return nil
	}
	return sink.CaptureSucceededEventRun(ctx, envelope, dispatch, run)
}

func (d *EventWorkflowDispatcher) retryDispatch(
	ctx context.Context,
	dispatch eventing.Dispatch,
	cause error,
) error {
	attempts := dispatch.Attempts
	if attempts >= defaultInt(d.MaxAttempts, DefaultEventMaxAttempts) {
		if err := d.Inbox.FinishDispatch(
			ctx,
			dispatch.ID,
			dispatch.LeaseToken,
			eventing.DispatchDead,
			cause.Error(),
		); err != nil {
			return fmt.Errorf("mark event dispatch %s dead: %w", dispatch.ID, err)
		}
		return nil
	}
	availableAt := d.now().Add(eventRetryDelay(
		attempts,
		defaultDuration(d.RetryBase, DefaultEventRetryBase),
		defaultDuration(d.RetryMax, DefaultEventRetryMax),
	))
	if err := d.Inbox.NackDispatch(
		ctx,
		dispatch.ID,
		dispatch.LeaseToken,
		availableAt,
		cause.Error(),
	); err != nil {
		return fmt.Errorf("retry event dispatch %s: %w", dispatch.ID, err)
	}
	return nil
}

func (d *EventWorkflowDispatcher) startHeartbeat(
	ctx context.Context,
	cancelRun context.CancelFunc,
	dispatch eventing.Dispatch,
) func() error {
	lease := defaultDuration(d.LeaseDuration, DefaultEventDispatchLease)
	interval := lease / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				done <- nil
				return
			case <-ticker.C:
				if err := d.Inbox.RenewDispatchLease(
					heartbeatCtx,
					dispatch.ID,
					dispatch.LeaseToken,
					lease,
				); err != nil {
					if heartbeatCtx.Err() != nil {
						done <- nil
						return
					}
					cancelRun()
					done <- err
					return
				}
			}
		}
	}()
	return func() error {
		cancelHeartbeat()
		return <-done
	}
}

func (d *EventWorkflowDispatcher) renewDispatchLease(
	ctx context.Context,
	dispatch eventing.Dispatch,
) error {
	return d.Inbox.RenewDispatchLease(
		ctx,
		dispatch.ID,
		dispatch.LeaseToken,
		defaultDuration(d.LeaseDuration, DefaultEventDispatchLease),
	)
}

func (d *EventWorkflowDispatcher) now() time.Time {
	if d != nil && d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

// EventWorkflowRunContext is the source-neutral workflow execution context
// derived from one already-redacted durable envelope.
type EventWorkflowRunContext struct {
	Inputs   map[string]any
	Event    map[string]any
	Origin   *RunOrigin
	Session  string
	Delivery Delivery
}

// EventWorkflowRunContextFromEnvelope builds the context shared by durable
// dispatch and explicit draft previews. A non-empty dispatch ID is included
// only for a real durable dispatch; previews must not invent one.
func EventWorkflowRunContextFromEnvelope(
	workflowRef string,
	dispatchID string,
	envelope eventing.Envelope,
) (EventWorkflowRunContext, error) {
	eventContext, err := EventContextFromEnvelope(envelope)
	if err != nil {
		return EventWorkflowRunContext{}, err
	}
	inputs := map[string]any{
		"event_id":  envelope.ID,
		"source":    envelope.Source,
		"connector": envelope.Connector,
		"type":      envelope.Type,
		"event":     cloneMap(eventContext),
	}
	if dispatchID = strings.TrimSpace(dispatchID); dispatchID != "" {
		inputs["dispatch_id"] = dispatchID
	}
	origin := &RunOrigin{
		Kind:    RunOriginExternalEventDraftTest,
		EventID: envelope.ID,
	}
	if dispatchID != "" {
		origin.Kind = RunOriginExternalEvent
		origin.DispatchID = dispatchID
	}
	return EventWorkflowRunContext{
		Inputs:   inputs,
		Event:    eventContext,
		Origin:   origin,
		Session:  EventWorkflowSession(workflowRef, envelope.ID),
		Delivery: Delivery{},
	}, nil
}

// EventContextFromEnvelope returns a detached workflow expression context for
// an already-redacted durable envelope.
func EventContextFromEnvelope(envelope eventing.Envelope) (map[string]any, error) {
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode durable event payload: %w", err)
	}
	if payload == nil {
		return nil, fmt.Errorf("decode durable event payload: object is required")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("decode durable event payload: trailing JSON value")
		}
		return nil, fmt.Errorf("decode durable event payload: trailing data: %w", err)
	}
	out := map[string]any{
		"id":          envelope.ID,
		"source":      envelope.Source,
		"connector":   envelope.Connector,
		"type":        envelope.Type,
		"occurred_at": nil,
		"received_at": envelope.ReceivedAt.UTC().Format(time.RFC3339Nano),
		"payload":     payload,
		"attributes":  eventStringMapValue(envelope.Attributes),
		"replay_of":   envelope.ReplayOf,
		"actor":       nil,
		"subject":     nil,
	}
	if envelope.OccurredAt != nil {
		out["occurred_at"] = envelope.OccurredAt.UTC().Format(time.RFC3339Nano)
	}
	if envelope.Actor != nil {
		out["actor"] = map[string]any{
			"id":           envelope.Actor.ID,
			"type":         envelope.Actor.Type,
			"display_name": envelope.Actor.DisplayName,
			"attributes":   eventStringMapValue(envelope.Actor.Attributes),
		}
	}
	if envelope.Subject != nil {
		out["subject"] = map[string]any{
			"id":         envelope.Subject.ID,
			"type":       envelope.Subject.Type,
			"name":       envelope.Subject.Name,
			"url":        envelope.Subject.URL,
			"attributes": eventStringMapValue(envelope.Subject.Attributes),
		}
	}
	return out, nil
}

func eventStringMapValue(values map[string]string) any {
	if len(values) == 0 {
		return nil
	}
	return stringMapAny(values)
}

// EventWorkflowSession is the deterministic isolated session used for one
// event/workflow pair.
func EventWorkflowSession(workflowRef, eventID string) string {
	return "workflow:" + strings.TrimSpace(workflowRef) + ":event:" + strings.TrimSpace(eventID)
}

func eventLocalOptions(definitionsDir string) []LocalOption {
	if strings.TrimSpace(definitionsDir) == "" {
		return nil
	}
	return []LocalOption{WithDefinitionsDir(definitionsDir)}
}

func eventRetryDelay(attempt int, base, maximum time.Duration) time.Duration {
	if base <= 0 {
		base = DefaultEventRetryBase
	}
	if maximum <= 0 {
		maximum = DefaultEventRetryMax
	}
	if maximum < base {
		maximum = base
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for i := 1; i < attempt && delay < maximum; i++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func defaultDuration(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func defaultInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
