package workflows

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

const (
	testRunOriginEventID    = "ev_0123456789abcdef0123456789abcdef"
	testRunOriginDispatchID = "dsp_0123456789abcdef0123456789abcdef"
)

func TestExecutorPropagatesTrustedRunOriginThroughReusableChildrenAndRetry(
	t *testing.T,
) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "origin-child.yml", `
name: Origin child
on:
  workflow_call: {}
jobs:
  work:
    runs-on: picoclaw
    steps:
      - uses: function/origin_noop
`)
	writeWorkflowFile(t, workspace, "origin-root.yml", `
name: Origin root
on:
  manual: {}
jobs:
  child:
    uses: workflows/origin-child.yml
`)
	functions := NewFunctionRegistry()
	if err := functions.Register(
		"origin_noop",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	store := NewFileRunStore(workspace)
	executor := &Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    functions,
	}
	event := testRunOriginEvent()
	result, err := executor.Run(context.Background(), RunRequest{
		RunID: "wr_origin_root",
		Ref:   "workflows/origin-root.yml",
		Inputs: map[string]any{
			"event_id":    testRunOriginEventID,
			"dispatch_id": testRunOriginDispatchID,
			"event":       cloneMap(event),
		},
		Event: event,
		Origin: &RunOrigin{
			Kind:       RunOriginExternalEvent,
			EventID:    testRunOriginEventID,
			DispatchID: testRunOriginDispatchID,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	root, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun(root) error = %v", err)
	}
	wantOrigin := &RunOrigin{
		Kind:       RunOriginExternalEvent,
		EventID:    testRunOriginEventID,
		DispatchID: testRunOriginDispatchID,
		RootRunID:  root.ID,
	}
	if !reflect.DeepEqual(root.Origin, wantOrigin) {
		t.Fatalf("root origin = %#v, want %#v", root.Origin, wantOrigin)
	}
	if len(root.ChildRunIDs) != 1 {
		t.Fatalf("root child run ids = %#v, want one", root.ChildRunIDs)
	}
	child, err := store.GetRun(context.Background(), root.ChildRunIDs[0])
	if err != nil {
		t.Fatalf("GetRun(child) error = %v", err)
	}
	if !reflect.DeepEqual(child.Origin, wantOrigin) {
		t.Fatalf("child origin = %#v, want %#v", child.Origin, wantOrigin)
	}

	retryResult, err := executor.Retry(context.Background(), root.ID, nil)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	retry, err := store.GetRun(context.Background(), retryResult.RunID)
	if err != nil {
		t.Fatalf("GetRun(retry) error = %v", err)
	}
	if retry.RetryOfRunID != root.ID || !reflect.DeepEqual(retry.Origin, wantOrigin) {
		t.Fatalf("retry = %#v, want retry link and inherited origin", retry)
	}
}

func TestExecutorRunOriginIsStrictAndCannotBeInferredFromRunData(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Origin validation
on:
  manual: {}
jobs:
  work:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with:
          action: list
`)
	event := testRunOriginEvent()
	inputs := map[string]any{
		"event_id":    testRunOriginEventID,
		"dispatch_id": testRunOriginDispatchID,
	}

	t.Run("manual lookalike remains untrusted", func(t *testing.T) {
		store := NewFileRunStore(t.TempDir())
		result, err := (&Executor{Store: store}).Run(context.Background(), RunRequest{
			RunID:       "wr_manual_lookalike",
			Workflow:    workflow,
			WorkflowRef: "inline",
			Inputs:      cloneMap(inputs),
			Event:       cloneMap(event),
		})
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		run, err := store.GetRun(context.Background(), result.RunID)
		if err != nil {
			t.Fatalf("GetRun() error = %v", err)
		}
		if run.Origin != nil {
			t.Fatalf("manual lookalike origin = %#v, want nil", run.Origin)
		}
	})

	tests := []struct {
		name   string
		origin *RunOrigin
	}{
		{
			name: "unknown kind",
			origin: &RunOrigin{
				Kind:       "manual",
				EventID:    testRunOriginEventID,
				DispatchID: testRunOriginDispatchID,
			},
		},
		{
			name: "invalid event id",
			origin: &RunOrigin{
				Kind:       RunOriginExternalEvent,
				EventID:    "ev_invalid",
				DispatchID: testRunOriginDispatchID,
			},
		},
		{
			name: "production missing dispatch",
			origin: &RunOrigin{
				Kind:    RunOriginExternalEvent,
				EventID: testRunOriginEventID,
			},
		},
		{
			name: "draft includes dispatch",
			origin: &RunOrigin{
				Kind:       RunOriginExternalEventDraftTest,
				EventID:    testRunOriginEventID,
				DispatchID: testRunOriginDispatchID,
			},
		},
		{
			name: "foreign initial root",
			origin: &RunOrigin{
				Kind:       RunOriginExternalEvent,
				EventID:    testRunOriginEventID,
				DispatchID: testRunOriginDispatchID,
				RootRunID:  "wr_foreign",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewFileRunStore(t.TempDir())
			_, err := (&Executor{Store: store}).Run(context.Background(), RunRequest{
				RunID:       "wr_origin_invalid",
				Workflow:    workflow,
				WorkflowRef: "inline",
				Inputs:      cloneMap(inputs),
				Event:       cloneMap(event),
				Origin:      test.origin,
			})
			if !errors.Is(err, ErrInvalidRunOrigin) {
				t.Fatalf("Run() error = %v, want ErrInvalidRunOrigin", err)
			}
			runs, listErr := store.ListRuns(context.Background())
			if listErr != nil {
				t.Fatalf("ListRuns() error = %v", listErr)
			}
			if len(runs) != 0 {
				t.Fatalf("invalid origin created runs: %#v", runs)
			}
		})
	}
}

func TestWorkflowRunBrowserProjectionDropsMalformedPersistedOrigin(t *testing.T) {
	run := &Run{
		ID:          "wr_projection",
		WorkflowRef: "workflows/test.yml",
		Event:       testRunOriginEvent(),
		Origin: &RunOrigin{
			Kind:       RunOriginExternalEvent,
			EventID:    testRunOriginEventID,
			DispatchID: "dsp_invalid",
			RootRunID:  "wr_projection",
		},
	}
	projected := ProjectWorkflowRunForBrowser(run, false)
	if projected.Origin != nil {
		t.Fatalf("projected malformed origin = %#v, want nil", projected.Origin)
	}
}

func testRunOriginEvent() map[string]any {
	return map[string]any{
		"id":        testRunOriginEventID,
		"source":    "github",
		"connector": "primary",
		"type":      "issues.opened",
		"payload":   map[string]any{"action": "opened"},
	}
}
