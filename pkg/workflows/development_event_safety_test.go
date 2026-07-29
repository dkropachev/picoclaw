package workflows

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const eventDraftPrivateDiagnostic = "provider echoed EVENT_PRIVATE_VALUE"

func TestSanitizeEventBackedDraftTestOutcomeMasksOnlyDiagnostics(t *testing.T) {
	original := &RunResult{
		RunID:   "wr_event",
		Status:  RunStatusFailed,
		Outputs: map[string]any{"visible": "workflow output"},
		Error:   eventDraftPrivateDiagnostic,
	}
	projected, projectedErr := SanitizeEventBackedDraftTestOutcome(
		original,
		errors.New(eventDraftPrivateDiagnostic),
	)
	if projectedErr == nil ||
		projectedErr.Error() != EventBackedDraftTestFailureDiagnostic {
		t.Fatalf("projected error = %v", projectedErr)
	}
	if projected == nil ||
		projected.RunID != original.RunID ||
		projected.Status != original.Status ||
		projected.Error != EventBackedDraftTestFailureDiagnostic ||
		!reflect.DeepEqual(projected.Outputs, original.Outputs) {
		t.Fatalf("projected result = %#v", projected)
	}
	if original.Error != eventDraftPrivateDiagnostic {
		t.Fatalf("sanitizer mutated source result = %#v", original)
	}
	projected.Outputs["visible"] = "changed"
	if original.Outputs["visible"] != "workflow output" {
		t.Fatal("projected outputs alias source outputs")
	}

	canceled, canceledErr := SanitizeEventBackedDraftTestOutcome(
		&RunResult{
			RunID:  "wr_canceled",
			Status: RunStatusCanceled,
			Error:  eventDraftPrivateDiagnostic,
		},
		errors.New(eventDraftPrivateDiagnostic),
	)
	if canceledErr == nil ||
		canceledErr.Error() != EventBackedDraftTestCanceledDiagnostic ||
		canceled.Error != EventBackedDraftTestCanceledDiagnostic {
		t.Fatalf("canceled outcome = %#v, error=%v", canceled, canceledErr)
	}
}

func TestWorkflowDevelopmentEventTestPersistsSafeSyncAndAsyncDiagnostics(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	session, err := StartWorkflowDevelopment(
		ctx,
		workspace,
		RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"},
		WorkflowDevelopmentStartRequest{Prompt: "triage events"},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	const eventID = "ev_0123456789abcdef0123456789abcdef"
	active, err := RecordWorkflowDevelopmentEventTest(
		workspace,
		eventID,
		&RunResult{
			RunID:  "wr_sync",
			Status: RunStatusFailed,
			Error:  eventDraftPrivateDiagnostic,
		},
		errors.New(eventDraftPrivateDiagnostic),
	)
	if err != nil {
		t.Fatalf("RecordWorkflowDevelopmentEventTest(sync) error = %v", err)
	}
	assertSafeEventDraftLastTest(t, active, eventID, "wr_sync", RunStatusFailed)

	active, err = RecordWorkflowDevelopmentEventTest(
		workspace,
		eventID,
		&RunResult{RunID: "wr_async", Status: RunStatusRunning},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordWorkflowDevelopmentEventTest(running) error = %v", err)
	}
	if active.LastTest == nil ||
		active.LastTest.Status != RunStatusRunning ||
		active.LastTest.Error != "" {
		t.Fatalf("running snapshot = %#v", active.LastTest)
	}
	draftKey := WorkflowDevelopmentDraftKey(session.TargetWorkflowRef, session.YAML)
	active, recorded, err := RecordWorkflowDevelopmentEventTestIfCurrent(
		workspace,
		session.ID,
		draftKey,
		eventID,
		&RunResult{
			RunID:  "wr_async",
			Status: RunStatusFailed,
			Error:  eventDraftPrivateDiagnostic,
		},
		errors.New(eventDraftPrivateDiagnostic),
	)
	if err != nil || !recorded {
		t.Fatalf("async completion recorded=%v error=%v", recorded, err)
	}
	assertSafeEventDraftLastTest(t, active, eventID, "wr_async", RunStatusFailed)

	data, err := GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if strings.Contains(data.LastTest.Error, eventDraftPrivateDiagnostic) {
		t.Fatalf("persisted last test leaked diagnostic: %#v", data.LastTest)
	}
}

func TestEventBackedDraftBrowserProjectionMasksRunAndLifecycleDiagnostics(t *testing.T) {
	run := &Run{
		ID:          "wr_event",
		WorkflowRef: "draft:workflows/triage.yml",
		Status:      RunStatusFailed,
		Session:     "workflow:workflows/triage.yml:event:ev_0123456789abcdef0123456789abcdef",
		Event: map[string]any{
			"id":      "ev_0123456789abcdef0123456789abcdef",
			"payload": map[string]any{"visible": "redacted event context"},
		},
		Inputs:       map[string]any{"event_id": "ev_0123456789abcdef0123456789abcdef"},
		Outputs:      map[string]any{"visible": "workflow output"},
		Error:        eventDraftPrivateDiagnostic,
		CancelReason: eventDraftPrivateDiagnostic,
		Jobs: map[string]JobExecution{
			"main": {
				ID:      "main",
				Status:  RunStatusFailed,
				Error:   eventDraftPrivateDiagnostic,
				Outputs: map[string]any{"visible": "job output"},
			},
		},
		Steps: map[string]StepExecution{
			"main/fail": {
				ID:      "fail",
				Status:  RunStatusFailed,
				Error:   eventDraftPrivateDiagnostic,
				Outputs: map[string]any{"visible": "step output"},
			},
		},
	}
	projected := ProjectEventBackedDraftRunForBrowser(run)
	if projected.Error != EventBackedDraftRunErrorDiagnostic ||
		projected.CancelReason != EventBackedDraftCancelReasonDiagnostic ||
		projected.Jobs["main"].Error != EventBackedDraftJobErrorDiagnostic ||
		projected.Steps["main/fail"].Error != EventBackedDraftStepErrorDiagnostic {
		t.Fatalf("projected run diagnostics = %#v", projected)
	}
	if projected.ID != run.ID ||
		projected.Status != run.Status ||
		!reflect.DeepEqual(projected.Event, run.Event) ||
		!reflect.DeepEqual(projected.Outputs, run.Outputs) ||
		!reflect.DeepEqual(projected.Jobs["main"].Outputs, run.Jobs["main"].Outputs) ||
		!reflect.DeepEqual(projected.Steps["main/fail"].Outputs, run.Steps["main/fail"].Outputs) {
		t.Fatalf("projection changed structured run data: %#v", projected)
	}
	if run.Error != eventDraftPrivateDiagnostic ||
		run.Jobs["main"].Error != eventDraftPrivateDiagnostic ||
		run.Steps["main/fail"].Error != eventDraftPrivateDiagnostic {
		t.Fatal("projection mutated raw audit run")
	}

	events := []RunEvent{{
		Kind:    "workflow.step.failed",
		RunID:   run.ID,
		JobID:   "main",
		StepID:  "fail",
		Message: eventDraftPrivateDiagnostic,
		Payload: map[string]any{
			"error":   eventDraftPrivateDiagnostic,
			"outputs": map[string]any{"visible": "raw event output"},
		},
	}}
	projectedEvents := ProjectEventBackedDraftEventsForBrowser(run, events)
	if len(projectedEvents) != 1 ||
		projectedEvents[0].Message != EventBackedDraftEventMessageDiagnostic ||
		!reflect.DeepEqual(projectedEvents[0].Payload, map[string]any{
			"diagnostic": EventBackedDraftEventPayloadDiagnostic,
		}) ||
		projectedEvents[0].RunID != run.ID ||
		projectedEvents[0].JobID != "main" ||
		projectedEvents[0].StepID != "fail" {
		t.Fatalf("projected events = %#v", projectedEvents)
	}
	if events[0].Message != eventDraftPrivateDiagnostic ||
		events[0].Payload["error"] != eventDraftPrivateDiagnostic {
		t.Fatal("event projection mutated raw audit event")
	}

	manual := *run
	manual.WorkflowRef = "workflows/manual.yml"
	manual.Event = nil
	manualProjected := ProjectEventBackedDraftRunForBrowser(&manual)
	manualEvents := ProjectEventBackedDraftEventsForBrowser(&manual, events)
	if manualProjected.Error != eventDraftPrivateDiagnostic ||
		manualProjected.Jobs["main"].Error != eventDraftPrivateDiagnostic ||
		manualProjected.Steps["main/fail"].Error != eventDraftPrivateDiagnostic ||
		manualEvents[0].Message != eventDraftPrivateDiagnostic ||
		manualEvents[0].Payload["error"] != eventDraftPrivateDiagnostic {
		t.Fatalf(
			"manual diagnostics changed: run=%#v events=%#v",
			manualProjected,
			manualEvents,
		)
	}
}

func TestEventBackedDraftBrowserProjectionResolvesChildrenAndProductionRoots(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	event := map[string]any{
		"id":        "ev_0123456789abcdef0123456789abcdef",
		"source":    "github",
		"connector": "primary",
		"type":      "issues.opened",
	}
	draftRoot := &Run{
		ID:          "wr_draft_root",
		WorkflowRef: "draft:workflows/triage.yml",
		Status:      RunStatusFailed,
		Event:       cloneMap(event),
		Error:       eventDraftPrivateDiagnostic,
	}
	draftChild := &Run{
		ID:          "wr_draft_child",
		WorkflowRef: "workflows/reusable.yml",
		ParentRunID: draftRoot.ID,
		Status:      RunStatusFailed,
		Event:       cloneMap(event),
		Error:       eventDraftPrivateDiagnostic,
	}
	productionRoot := &Run{
		ID:          "wr_production_root",
		WorkflowRef: "workflows/triage.yml",
		Status:      RunStatusFailed,
		Event:       cloneMap(event),
		Error:       eventDraftPrivateDiagnostic,
	}
	productionChild := &Run{
		ID:          "wr_production_child",
		WorkflowRef: "workflows/reusable.yml",
		ParentRunID: productionRoot.ID,
		Status:      RunStatusFailed,
		Event:       cloneMap(event),
		Error:       eventDraftPrivateDiagnostic,
	}
	for _, run := range []*Run{
		draftRoot,
		draftChild,
		productionRoot,
		productionChild,
	} {
		if err := store.CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun(%s) error = %v", run.ID, err)
		}
	}
	if !IsEventBackedDraftRunFamily(ctx, store, draftChild) {
		t.Fatal("draft reusable child was not classified")
	}
	if IsEventBackedDraftRunFamily(ctx, store, productionChild) {
		t.Fatal("production reusable child was classified as draft")
	}

	projected := ProjectEventBackedDraftRunsForBrowser([]Run{
		*draftChild,
		*productionChild,
		*draftRoot,
		*productionRoot,
	})
	byID := make(map[string]Run, len(projected))
	for _, run := range projected {
		byID[run.ID] = run
	}
	if byID[draftRoot.ID].Error != EventBackedDraftRunErrorDiagnostic ||
		byID[draftChild.ID].Error != EventBackedDraftRunErrorDiagnostic {
		t.Fatalf("draft family projection = %#v", byID)
	}
	if byID[productionRoot.ID].Error != eventDraftPrivateDiagnostic ||
		byID[productionChild.ID].Error != eventDraftPrivateDiagnostic {
		t.Fatalf("production family diagnostics changed = %#v", byID)
	}
}

func TestEventBackedDraftBrowserProjectionFailsClosedForMixedEventCycle(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	first := &Run{
		ID:          "wr_cycle_first",
		WorkflowRef: "workflows/first.yml",
		ParentRunID: "wr_cycle_second",
		Status:      RunStatusFailed,
		Error:       eventDraftPrivateDiagnostic,
	}
	second := &Run{
		ID:          "wr_cycle_second",
		WorkflowRef: "workflows/second.yml",
		ParentRunID: first.ID,
		Status:      RunStatusFailed,
		Event:       map[string]any{"id": "event-context-present"},
		Error:       eventDraftPrivateDiagnostic,
	}
	if err := store.CreateRun(ctx, first); err != nil {
		t.Fatalf("CreateRun(first) error = %v", err)
	}
	if err := store.CreateRun(ctx, second); err != nil {
		t.Fatalf("CreateRun(second) error = %v", err)
	}
	if !IsEventBackedDraftRunFamily(ctx, store, first) ||
		!IsEventBackedDraftRunFamily(ctx, store, second) {
		t.Fatal("mixed-event cycle did not fail closed")
	}
	projected := ProjectEventBackedDraftRunsForBrowser([]Run{*first, *second})
	for _, run := range projected {
		if run.Error != EventBackedDraftRunErrorDiagnostic {
			t.Fatalf("cycle run %s error = %q", run.ID, run.Error)
		}
	}
}

func TestEventBackedDraftBrowserProjectionDoesNotReuseUnresolvedFalseMemo(t *testing.T) {
	missingParent := Run{
		ID:          "wr_missing_parent_path",
		WorkflowRef: "workflows/reusable.yml",
		ParentRunID: "wr_absent",
		Status:      RunStatusFailed,
		Error:       eventDraftPrivateDiagnostic,
	}
	eventChild := Run{
		ID:          "wr_event_child",
		WorkflowRef: "workflows/reusable.yml",
		ParentRunID: missingParent.ID,
		Status:      RunStatusFailed,
		Event:       map[string]any{"id": "event-context-present"},
		Error:       eventDraftPrivateDiagnostic,
	}
	projected := ProjectEventBackedDraftRunsForBrowser([]Run{
		missingParent,
		eventChild,
	})
	if projected[0].Error != eventDraftPrivateDiagnostic {
		t.Fatalf("event-free unresolved path was masked: %#v", projected[0])
	}
	if projected[1].Error != EventBackedDraftRunErrorDiagnostic {
		t.Fatalf("event child inherited unresolved false memo: %#v", projected[1])
	}
}

func assertSafeEventDraftLastTest(
	t *testing.T,
	session *WorkflowDevelopmentSession,
	eventID string,
	runID string,
	status string,
) {
	t.Helper()
	if session == nil ||
		session.LastTest == nil ||
		session.LastTest.EventID != eventID ||
		session.LastTest.RunID != runID ||
		session.LastTest.Status != status ||
		session.LastTest.Error != EventBackedDraftTestFailureDiagnostic {
		t.Fatalf("last test = %#v", session)
	}
	if strings.Contains(session.LastTest.Error, eventDraftPrivateDiagnostic) {
		t.Fatalf("last test leaked diagnostic: %#v", session.LastTest)
	}
}
