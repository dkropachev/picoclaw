package workflows

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const exactEventDraftInteger = "9007199254740993"

func TestExecutorPersistsOverflowEventNumbersAcrossRunAndLifecycleOutputs(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	functions := NewFunctionRegistry()
	if err := functions.Register(
		"echo_numbers",
		func(
			_ context.Context,
			args map[string]any,
			_ ExecutionContext,
		) (map[string]any, error) {
			return map[string]any{
				"huge":           args["huge"],
				"ordinary_count": args["ordinary_count"],
			}, nil
		},
	); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	workflow := parseWorkflow(t, `
name: Event overflow outputs
on:
  event:
    sources: github
    types: issues.opened
  workflow_call:
    outputs:
      huge:
        value: "${{ jobs.main.outputs.huge }}"
      ordinary_count:
        value: "${{ jobs.main.outputs.ordinary_count }}"
jobs:
  main:
    runs-on: picoclaw
    outputs:
      huge: "${{ steps.echo.outputs.huge }}"
      ordinary_count: "${{ steps.echo.outputs.ordinary_count }}"
    steps:
      - id: echo
        uses: function/echo_numbers
        with:
          huge: "${{ event.payload.huge }}"
          ordinary_count: "${{ event.payload.ordinary_count }}"
`)
	eventID := "ev_0123456789abcdef0123456789abcdef"
	event := map[string]any{
		"id":          eventID,
		"source":      "github",
		"connector":   "primary",
		"type":        "issues.opened",
		"received_at": "2026-07-29T12:34:56Z",
		"payload": map[string]any{
			"huge":           json.Number("1e400"),
			"ordinary_count": json.Number("7"),
		},
	}
	result, err := (&Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    functions,
	}).Run(ctx, RunRequest{
		Workflow:    workflow,
		WorkflowRef: "draft:workflows/overflow.yml",
		Event:       event,
		Inputs: map[string]any{
			"event_id":       eventID,
			"event":          cloneMap(event),
			"ordinary_count": json.Number("7"),
		},
		Session: EventWorkflowSession("workflows/overflow.yml", eventID),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() result = %#v", result)
	}

	run, err := store.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	assertOverflowOutputNumbers(t, run.Steps["main/echo"].Outputs)
	assertOverflowOutputNumbers(t, run.Jobs["main"].Outputs)
	assertOverflowOutputNumbers(t, run.Outputs)
	assertLegacyFloat64(t, "run inputs ordinary_count", run.Inputs["ordinary_count"], 7)

	runs, err := store.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns() = %#v", runs)
	}
	assertOverflowOutputNumbers(t, runs[0].Outputs)

	terminal, err := store.CancelRun(ctx, run.ID, "already complete")
	if err != nil {
		t.Fatalf("CancelRun() error = %v", err)
	}
	assertOverflowOutputNumbers(t, terminal.Outputs)

	events, err := store.Events(ctx, run.ID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	wantKinds := map[string]bool{
		"workflow.step.end": false,
		"workflow.job.end":  false,
		"workflow.run.end":  false,
	}
	for _, event := range events {
		if _, wanted := wantKinds[event.Kind]; !wanted {
			continue
		}
		outputs, ok := event.Payload["outputs"].(map[string]any)
		if !ok {
			t.Fatalf("%s payload = %#v", event.Kind, event.Payload)
		}
		assertOverflowOutputNumbers(t, outputs)
		wantKinds[event.Kind] = true
	}
	for kind, seen := range wantKinds {
		if !seen {
			t.Fatalf("Events() omitted %s overflow payload: %#v", kind, events)
		}
	}
}

func TestExecutorEventSnapshotSurvivesMutatingFunction(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	functions := NewFunctionRegistry()
	if err := functions.Register(
		"mutate_event",
		func(
			_ context.Context,
			args map[string]any,
			exec ExecutionContext,
		) (map[string]any, error) {
			argEvent := args["event"].(map[string]any)
			argPayload := argEvent["payload"].(map[string]any)
			if number, ok := argPayload["huge"].(json.Number); !ok || number.String() != "1e400" {
				t.Fatalf("function event payload huge = %#v, want json.Number(1e400)", argPayload["huge"])
			}
			argPayload["nested"].(map[string]any)["state"] = "mutated through map argument"
			args["items"].([]any)[0].(map[string]any)["state"] = "mutated through array argument"
			execPayload := exec.Event["payload"].(map[string]any)
			if state := execPayload["nested"].(map[string]any)["state"]; state != "original" {
				t.Fatalf("map expression result aliased execution event: state = %#v", state)
			}
			if state := execPayload["items"].([]any)[0].(map[string]any)["state"]; state != "first" {
				t.Fatalf("array expression result aliased execution event: state = %#v", state)
			}

			exec.Event["payload"].(map[string]any)["nested"].(map[string]any)["state"] = "mutated through execution event"
			exec.Inputs["event"].(map[string]any)["payload"].(map[string]any)["items"].([]any)[1].(map[string]any)["state"] = "mutated through execution inputs"
			return map[string]any{"ok": true}, nil
		},
	); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	workflow := parseWorkflow(t, `
name: Event snapshot mutation
on:
  event:
    sources: github
    types: issues.opened
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: mutate
        uses: function/mutate_event
        with:
          event: "${{ event }}"
          items: "${{ event.payload.items }}"
`)
	eventID := "ev_0123456789abcdef0123456789abcdef"
	runContext, err := EventWorkflowRunContextFromEnvelope(
		"workflows/mutation.yml",
		"",
		eventing.Envelope{
			ID:         eventID,
			Source:     "github",
			Connector:  "primary",
			Type:       "issues.opened",
			ReceivedAt: time.Date(2026, 7, 29, 12, 34, 56, 0, time.UTC),
			Payload: json.RawMessage(
				`{"nested":{"state":"original"},"items":[{"state":"first"},{"state":"second"}],"huge":1e400}`,
			),
		},
	)
	if err != nil {
		t.Fatalf("EventWorkflowRunContextFromEnvelope() error = %v", err)
	}
	result, err := (&Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    functions,
	}).Run(ctx, RunRequest{
		Workflow:    workflow,
		WorkflowRef: "draft:workflows/mutation.yml",
		Event:       runContext.Event,
		Inputs:      runContext.Inputs,
		Session:     runContext.Session,
		Delivery:    runContext.Delivery,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() result = %#v", result)
	}

	run, err := store.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	assertOriginalEventSnapshot(t, "run event", run.Event)
	inputEvent, ok := run.Inputs["event"].(map[string]any)
	if !ok {
		t.Fatalf("run inputs.event = %#v, want object", run.Inputs["event"])
	}
	assertOriginalEventSnapshot(t, "run inputs.event", inputEvent)
}

func assertOriginalEventSnapshot(t *testing.T, label string, event map[string]any) {
	t.Helper()
	payload, ok := event["payload"].(map[string]any)
	if !ok {
		t.Fatalf("%s payload = %#v, want object", label, event["payload"])
	}
	nested, ok := payload["nested"].(map[string]any)
	if !ok || nested["state"] != "original" {
		t.Fatalf("%s nested = %#v, want original", label, payload["nested"])
	}
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("%s items = %#v, want two items", label, payload["items"])
	}
	first, firstOK := items[0].(map[string]any)
	second, secondOK := items[1].(map[string]any)
	if !firstOK || first["state"] != "first" || !secondOK || second["state"] != "second" {
		t.Fatalf("%s items = %#v, want original states", label, items)
	}
	assertExactJSONNumberValue(t, payload["huge"], "1e400")
}

func TestFileRunStoreRestoresExactEventDraftNumbersOnGetListAndCancel(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	eventID := "ev_0123456789abcdef0123456789abcdef"
	run := eventDraftExactNumberRun("wr_event", eventID)
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	got, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	assertExactEventDraftNumbers(t, got)
	runs, err := store.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %#v", runs)
	}
	assertExactEventDraftNumbers(t, &runs[0])

	canceled, err := store.CancelRun(ctx, run.ID, "operator canceled")
	if err != nil {
		t.Fatalf("CancelRun() error = %v", err)
	}
	assertExactEventDraftNumbers(t, canceled)
}

func TestFileRunStoreRestoresExactNumbersForTrustedReusableDescendant(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	eventID := "ev_0123456789abcdef0123456789abcdef"
	parent := eventDraftExactNumberRun("wr_parent", eventID)
	if err := store.CreateRun(ctx, parent); err != nil {
		t.Fatalf("CreateRun(parent) error = %v", err)
	}
	child := &Run{
		ID:          "wr_child",
		WorkflowRef: "workflows/reusable.yml",
		Status:      RunStatusRunning,
		ParentRunID: parent.ID,
		CallerJobID: "reuse",
		Session:     parent.Session,
		Event:       cloneMap(parent.Event),
		Inputs: map[string]any{
			"event":          cloneMap(parent.Event),
			"ordinary_count": json.Number(exactEventDraftInteger),
		},
		Outputs:      map[string]any{},
		Jobs:         map[string]JobExecution{},
		Steps:        map[string]StepExecution{},
		ChildRunIDs:  []string{},
		RetryOfRunID: "",
	}
	if err := store.CreateRun(ctx, child); err != nil {
		t.Fatalf("CreateRun(child) error = %v", err)
	}
	got, err := store.GetRun(ctx, child.ID)
	if err != nil {
		t.Fatalf("GetRun(child) error = %v", err)
	}
	assertExactJSONNumber(t, got.Event["payload"].(map[string]any)["large"])
	assertExactJSONNumberValue(
		t,
		got.Event["payload"].(map[string]any)["huge"],
		"1e400",
	)
	inputEvent := got.Inputs["event"].(map[string]any)
	assertExactJSONNumber(t, inputEvent["payload"].(map[string]any)["large"])
	assertExactJSONNumberValue(
		t,
		inputEvent["payload"].(map[string]any)["huge"],
		"1e400",
	)
	if _, promoted := got.Inputs["ordinary_count"].(json.Number); promoted {
		t.Fatalf("unrelated child input was promoted: %#v", got.Inputs)
	}
}

func TestFileRunStoreDoesNotPromoteUntrustedDescendantNumbers(t *testing.T) {
	eventIDA := "ev_0123456789abcdef0123456789abcdef"
	eventIDB := "ev_fedcba9876543210fedcba9876543210"

	t.Run("mismatched intermediate event", func(t *testing.T) {
		ctx := context.Background()
		store := NewFileRunStore(t.TempDir())
		root := eventDraftExactNumberRun("wr_root", eventIDA)
		if err := store.CreateRun(ctx, root); err != nil {
			t.Fatalf("CreateRun(root) error = %v", err)
		}
		middle := reusableNumberRun("wr_middle", root.ID, eventIDB)
		child := reusableNumberRun("wr_child", middle.ID, eventIDB)
		if err := store.CreateRun(ctx, middle); err != nil {
			t.Fatalf("CreateRun(middle) error = %v", err)
		}
		if err := store.CreateRun(ctx, child); err != nil {
			t.Fatalf("CreateRun(child) error = %v", err)
		}
		assertOrdinaryReusableNumbers(t, getRunForNumberTest(t, store, child.ID))
	})

	t.Run("missing parent", func(t *testing.T) {
		ctx := context.Background()
		store := NewFileRunStore(t.TempDir())
		child := reusableNumberRun("wr_child", "wr_missing", eventIDA)
		if err := store.CreateRun(ctx, child); err != nil {
			t.Fatalf("CreateRun(child) error = %v", err)
		}
		assertOrdinaryReusableNumbers(t, getRunForNumberTest(t, store, child.ID))
	})

	t.Run("cyclic ancestry", func(t *testing.T) {
		ctx := context.Background()
		store := NewFileRunStore(t.TempDir())
		first := reusableNumberRun("wr_first", "wr_second", eventIDA)
		second := reusableNumberRun("wr_second", first.ID, eventIDA)
		if err := store.CreateRun(ctx, first); err != nil {
			t.Fatalf("CreateRun(first) error = %v", err)
		}
		if err := store.CreateRun(ctx, second); err != nil {
			t.Fatalf("CreateRun(second) error = %v", err)
		}
		assertOrdinaryReusableNumbers(t, getRunForNumberTest(t, store, first.ID))
	})
}

func TestFileRunStoreDoesNotPromoteManualDraftLookalikeNumbers(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	run := eventDraftExactNumberRun(
		"wr_manual_lookalike",
		"ev_0123456789abcdef0123456789abcdef",
	)
	run.Session = "manual-session"
	delete(run.Event["payload"].(map[string]any), "huge")
	delete(
		run.Inputs["event"].(map[string]any)["payload"].(map[string]any),
		"huge",
	)
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	got, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if _, ok := got.Event["payload"].(map[string]any)["large"].(json.Number); ok {
		t.Fatalf("manual lookalike event was promoted: %#v", got.Event)
	}
	if _, ok := got.Inputs["ordinary_count"].(json.Number); ok {
		t.Fatalf("manual lookalike inputs were promoted: %#v", got.Inputs)
	}
}

func reusableNumberRun(runID, parentRunID, eventID string) *Run {
	event := map[string]any{
		"id":          eventID,
		"source":      "github",
		"connector":   "primary",
		"type":        "issues.opened",
		"received_at": "2026-07-29T12:34:56Z",
		"payload": map[string]any{
			"large": json.Number(exactEventDraftInteger),
		},
	}
	return &Run{
		ID:          runID,
		WorkflowRef: "workflows/reusable.yml",
		Status:      RunStatusRunning,
		ParentRunID: parentRunID,
		Event:       event,
		Inputs: map[string]any{
			"event":          cloneMap(event),
			"ordinary_count": json.Number(exactEventDraftInteger),
		},
		Outputs: overflowOutputNumbers(),
		Jobs: map[string]JobExecution{
			"main": {
				ID:      "main",
				Status:  RunStatusSucceeded,
				Outputs: overflowOutputNumbers(),
			},
		},
		Steps: map[string]StepExecution{
			"main/echo": {
				ID:      "echo",
				Status:  RunStatusSucceeded,
				Outputs: overflowOutputNumbers(),
			},
		},
	}
}

func getRunForNumberTest(
	t *testing.T,
	store *FileRunStore,
	runID string,
) *Run {
	t.Helper()
	run, err := store.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun(%q) error = %v", runID, err)
	}
	return run
}

func assertOrdinaryReusableNumbers(t *testing.T, run *Run) {
	t.Helper()
	if _, promoted := run.Event["payload"].(map[string]any)["large"].(json.Number); promoted {
		t.Fatalf("untrusted run event was promoted: %#v", run.Event)
	}
	inputEvent, ok := run.Inputs["event"].(map[string]any)
	if !ok {
		t.Fatalf("inputs.event = %#v", run.Inputs["event"])
	}
	if _, promoted := inputEvent["payload"].(map[string]any)["large"].(json.Number); promoted {
		t.Fatalf("untrusted inputs.event was promoted: %#v", inputEvent)
	}
	if _, promoted := run.Inputs["ordinary_count"].(json.Number); promoted {
		t.Fatalf("unrelated input was promoted: %#v", run.Inputs)
	}
	assertOverflowOutputNumbers(t, run.Steps["main/echo"].Outputs)
	assertOverflowOutputNumbers(t, run.Jobs["main"].Outputs)
	assertOverflowOutputNumbers(t, run.Outputs)
}

func eventDraftExactNumberRun(runID, eventID string) *Run {
	event := map[string]any{
		"id":          eventID,
		"source":      "github",
		"connector":   "primary",
		"type":        "issues.opened",
		"received_at": "2026-07-29T12:34:56Z",
		"payload": map[string]any{
			"large": json.Number(exactEventDraftInteger),
			"huge":  json.Number("1e400"),
		},
	}
	return &Run{
		ID:          runID,
		WorkflowRef: "draft:workflows/triage.yml",
		Status:      RunStatusRunning,
		Session: EventWorkflowSession(
			"workflows/triage.yml",
			eventID,
		),
		Event: event,
		Inputs: map[string]any{
			"event_id": eventID,
			"event":    cloneMap(event),
			"ordinary_count": json.Number(
				exactEventDraftInteger,
			),
		},
		Outputs: overflowOutputNumbers(),
		Jobs: map[string]JobExecution{
			"main": {
				ID:      "main",
				Status:  RunStatusSucceeded,
				Outputs: overflowOutputNumbers(),
			},
		},
		Steps: map[string]StepExecution{
			"main/echo": {
				ID:      "echo",
				Status:  RunStatusSucceeded,
				Outputs: overflowOutputNumbers(),
			},
		},
	}
}

func assertExactEventDraftNumbers(t *testing.T, run *Run) {
	t.Helper()
	payload, ok := run.Event["payload"].(map[string]any)
	if !ok {
		t.Fatalf("event payload = %#v", run.Event["payload"])
	}
	assertExactJSONNumber(t, payload["large"])
	assertExactJSONNumberValue(t, payload["huge"], "1e400")
	inputEvent, ok := run.Inputs["event"].(map[string]any)
	if !ok {
		t.Fatalf("inputs.event = %#v", run.Inputs["event"])
	}
	inputPayload, ok := inputEvent["payload"].(map[string]any)
	if !ok {
		t.Fatalf("inputs.event.payload = %#v", inputEvent["payload"])
	}
	assertExactJSONNumber(t, inputPayload["large"])
	assertExactJSONNumberValue(t, inputPayload["huge"], "1e400")
	if _, promoted := run.Inputs["ordinary_count"].(json.Number); promoted {
		t.Fatalf("unrelated input was promoted: %#v", run.Inputs)
	}
	assertOverflowOutputNumbers(t, run.Steps["main/echo"].Outputs)
	assertOverflowOutputNumbers(t, run.Jobs["main"].Outputs)
	assertOverflowOutputNumbers(t, run.Outputs)
}

func assertExactJSONNumber(t *testing.T, value any) {
	t.Helper()
	assertExactJSONNumberValue(t, value, exactEventDraftInteger)
}

func assertExactJSONNumberValue(t *testing.T, value any, want string) {
	t.Helper()
	number, ok := value.(json.Number)
	if !ok || number.String() != want {
		t.Fatalf("value = %#v (%T), want exact json.Number", value, value)
	}
}

func assertOverflowOutputNumbers(t *testing.T, outputs map[string]any) {
	t.Helper()
	assertExactJSONNumberValue(t, outputs["huge"], "1e400")
	assertLegacyFloat64(t, "ordinary_count", outputs["ordinary_count"], 7)
}

func overflowOutputNumbers() map[string]any {
	return map[string]any{
		"huge":           json.Number("1e400"),
		"ordinary_count": json.Number("7"),
	}
}

func assertLegacyFloat64(
	t *testing.T,
	label string,
	value any,
	want float64,
) {
	t.Helper()
	number, ok := value.(float64)
	if !ok || number != want {
		t.Fatalf("%s = %#v (%T), want float64(%v)", label, value, value, want)
	}
}
