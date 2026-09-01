package workflows

import (
	"context"
	"errors"
	"fmt"
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
		workspace := t.TempDir()
		store := NewFileRunStore(workspace)
		result, err := (&Executor{Store: store, WorkspaceDir: workspace}).Run(context.Background(), RunRequest{
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
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			_, err := (&Executor{Store: store, WorkspaceDir: workspace}).Run(context.Background(), RunRequest{
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

	linkTests := []struct {
		name         string
		parentRunID  string
		retryOfRunID string
		rootRunID    string
	}{
		{
			name:        "whitespace padded parent link",
			parentRunID: " wr_origin_parent ",
			rootRunID:   "wr_origin_parent",
		},
		{
			name:         "whitespace padded retry link",
			retryOfRunID: " wr_origin_source ",
			rootRunID:    "wr_origin_source",
		},
		{
			name:         "retry missing inherited root",
			retryOfRunID: "wr_origin_source",
		},
	}
	for _, test := range linkTests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			_, err := (&Executor{Store: store, WorkspaceDir: workspace}).Run(context.Background(), RunRequest{
				RunID:       "wr_origin_descendant",
				Workflow:    workflow,
				WorkflowRef: "inline",
				Inputs:      cloneMap(inputs),
				Event:       cloneMap(event),
				Origin: &RunOrigin{
					Kind:       RunOriginExternalEvent,
					EventID:    testRunOriginEventID,
					DispatchID: testRunOriginDispatchID,
					RootRunID:  test.rootRunID,
				},
				ParentRunID:  test.parentRunID,
				RetryOfRunID: test.retryOfRunID,
			})
			if !errors.Is(err, ErrInvalidRunOrigin) {
				t.Fatalf("Run() error = %v, want ErrInvalidRunOrigin", err)
			}
			runs, listErr := store.ListRuns(context.Background())
			if listErr != nil {
				t.Fatalf("ListRuns() error = %v", listErr)
			}
			if len(runs) != 0 {
				t.Fatalf("invalid ancestry created runs: %#v", runs)
			}
		})
	}
}

func TestWorkflowRunBrowserProjectionDropsUntrustedPersistedOrigin(t *testing.T) {
	validOrigin := &RunOrigin{
		Kind:       RunOriginExternalEvent,
		EventID:    testRunOriginEventID,
		DispatchID: testRunOriginDispatchID,
		RootRunID:  "wr_projection",
	}
	tests := []struct {
		name   string
		mutate func(*Run)
	}{
		{
			name: "malformed dispatch id",
			mutate: func(run *Run) {
				run.Origin.DispatchID = "dsp_invalid"
			},
		},
		{
			name: "foreign initial root",
			mutate: func(run *Run) {
				run.Origin.RootRunID = "wr_foreign"
			},
		},
		{
			name: "mismatched input event id",
			mutate: func(run *Run) {
				run.Inputs["event_id"] = "ev_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			},
		},
		{
			name: "mismatched input dispatch id",
			mutate: func(run *Run) {
				run.Inputs["dispatch_id"] = "dsp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			},
		},
		{
			name: "draft test with persisted dispatch input",
			mutate: func(run *Run) {
				run.Origin.Kind = RunOriginExternalEventDraftTest
				run.Origin.DispatchID = ""
			},
		},
		{
			name: "whitespace padded parent link",
			mutate: func(run *Run) {
				run.ParentRunID = " wr_projection_parent "
			},
		},
		{
			name: "whitespace padded retry link",
			mutate: func(run *Run) {
				run.RetryOfRunID = " wr_projection_source "
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := &Run{
				ID:          "wr_projection",
				WorkflowRef: "workflows/test.yml",
				Event:       testRunOriginEvent(),
				Inputs: map[string]any{
					"event_id":    testRunOriginEventID,
					"dispatch_id": testRunOriginDispatchID,
				},
				Origin: cloneRunOrigin(validOrigin),
			}
			test.mutate(run)
			projected := ProjectWorkflowRunForBrowser(run, false)
			if projected.Origin != nil {
				t.Fatalf("projected untrusted origin = %#v, want nil", projected.Origin)
			}
		})
	}
}

func TestWorkflowRunBrowserProjectionVerifiesPersistedOriginLineage(t *testing.T) {
	ctx := context.Background()
	rootOrigin := &RunOrigin{
		Kind:       RunOriginExternalEvent,
		EventID:    testRunOriginEventID,
		DispatchID: testRunOriginDispatchID,
		RootRunID:  "wr_lineage_root",
	}
	root := &Run{
		ID:          rootOrigin.RootRunID,
		WorkflowRef: "workflows/test.yml",
		Event:       testRunOriginEvent(),
		Inputs: map[string]any{
			"event_id":    testRunOriginEventID,
			"dispatch_id": testRunOriginDispatchID,
		},
		Origin: cloneRunOrigin(rootOrigin),
	}
	child := &Run{
		ID:          "wr_lineage_child",
		WorkflowRef: "workflows/child.yml",
		ParentRunID: root.ID,
		Event:       testRunOriginEvent(),
		Origin:      cloneRunOrigin(rootOrigin),
	}
	retry := &Run{
		ID:           "wr_lineage_retry",
		WorkflowRef:  root.WorkflowRef,
		RetryOfRunID: root.ID,
		Event:        testRunOriginEvent(),
		Inputs:       cloneMap(root.Inputs),
		Origin:       cloneRunOrigin(rootOrigin),
	}
	childRetry := &Run{
		ID:           "wr_lineage_child_retry",
		WorkflowRef:  child.WorkflowRef,
		ParentRunID:  root.ID,
		RetryOfRunID: child.ID,
		Event:        testRunOriginEvent(),
		Origin:       cloneRunOrigin(rootOrigin),
	}

	t.Run("complete parent and retry lineage is trusted", func(t *testing.T) {
		store := NewFileRunStore(t.TempDir())
		for _, run := range []*Run{root, child, retry, childRetry} {
			if err := store.CreateRun(ctx, run); err != nil {
				t.Fatalf("CreateRun(%s) error = %v", run.ID, err)
			}
		}
		for _, run := range []*Run{child, retry, childRetry} {
			projected := ProjectWorkflowRunForBrowserWithStore(ctx, store, run, false)
			if !reflect.DeepEqual(projected.Origin, rootOrigin) {
				t.Fatalf(
					"projected %s origin = %#v, want %#v",
					run.ID,
					projected.Origin,
					rootOrigin,
				)
			}
		}
		listed := ProjectEventBackedDraftRunsForBrowser([]Run{*root, *child})
		if !reflect.DeepEqual(listed[1].Origin, rootOrigin) {
			t.Fatalf("listed child origin = %#v, want %#v", listed[1].Origin, rootOrigin)
		}
	})

	t.Run("foreign root is untrusted", func(t *testing.T) {
		store := NewFileRunStore(t.TempDir())
		if err := store.CreateRun(ctx, root); err != nil {
			t.Fatalf("CreateRun(root) error = %v", err)
		}
		forged := cloneRun(child)
		forged.Origin.RootRunID = "wr_foreign"
		projected := ProjectWorkflowRunForBrowserWithStore(ctx, store, forged, false)
		if projected.Origin != nil {
			t.Fatalf("projected foreign origin = %#v, want nil", projected.Origin)
		}
	})

	t.Run("pruned ancestor is a retention boundary", func(t *testing.T) {
		store := NewFileRunStore(t.TempDir())
		projected := ProjectWorkflowRunForBrowserWithStore(ctx, store, child, false)
		if !reflect.DeepEqual(projected.Origin, rootOrigin) {
			t.Fatalf(
				"projected origin = %#v, want %#v",
				projected.Origin,
				rootOrigin,
			)
		}
		listed := ProjectEventBackedDraftRunsForBrowserWithStore(
			ctx,
			store,
			[]Run{*child},
		)
		if !reflect.DeepEqual(listed[0].Origin, rootOrigin) {
			t.Fatalf(
				"listed origin = %#v, want %#v",
				listed[0].Origin,
				rootOrigin,
			)
		}
	})

	t.Run("corrupt retained ancestor fails the SQLite subsystem", func(t *testing.T) {
		workspace := t.TempDir()
		store := NewFileRunStore(workspace)
		if err := store.CreateRun(ctx, root); err != nil {
			t.Fatalf("CreateRun(root) error = %v", err)
		}
		if err := store.CreateRun(ctx, child); err != nil {
			t.Fatalf("CreateRun(child) error = %v", err)
		}
		db, err := openWorkflowDatabase(ctx, workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE workflow_run_payloads SET event_json=?
			WHERE run_id=?`, []byte(`{"id":`), root.ID); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if runs, err := store.ListRuns(ctx); err == nil || runs != nil {
			t.Fatalf("ListRuns() = %#v, %v; want fail closed", runs, err)
		}
	})

	t.Run("pruned parent does not hide an available invalid retry ancestor", func(t *testing.T) {
		store := NewFileRunStore(t.TempDir())
		invalidRetryAncestor := cloneRun(child)
		invalidRetryAncestor.Event["id"] = "ev_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if err := store.CreateRun(ctx, invalidRetryAncestor); err != nil {
			t.Fatalf("CreateRun(invalid retry ancestor) error = %v", err)
		}
		projected := ProjectWorkflowRunForBrowserWithStore(
			ctx,
			store,
			childRetry,
			false,
		)
		if projected.Origin != nil {
			t.Fatalf("projected origin = %#v, want nil", projected.Origin)
		}
	})

	t.Run("lineage read failure is untrusted", func(t *testing.T) {
		origin, trusted := trustedRunOriginWithLookup(
			ctx,
			child,
			func(context.Context, string) (*Run, error) {
				return nil, errors.New("store unavailable")
			},
		)
		if trusted || origin != nil {
			t.Fatalf("trusted origin = (%#v, %v), want (nil, false)", origin, trusted)
		}
	})

	t.Run("ancestry cycle is untrusted", func(t *testing.T) {
		store := NewFileRunStore(t.TempDir())
		cycleOrigin := cloneRunOrigin(rootOrigin)
		cycleOrigin.RootRunID = "wr_cycle_root"
		first := &Run{
			ID:          "wr_cycle_first",
			ParentRunID: "wr_cycle_second",
			Event:       testRunOriginEvent(),
			Origin:      cloneRunOrigin(cycleOrigin),
		}
		second := &Run{
			ID:          "wr_cycle_second",
			ParentRunID: first.ID,
			Event:       testRunOriginEvent(),
			Origin:      cloneRunOrigin(cycleOrigin),
		}
		for _, run := range []*Run{first, second} {
			if err := store.CreateRun(ctx, run); err != nil {
				t.Fatalf("CreateRun(%s) error = %v", run.ID, err)
			}
		}
		projected := ProjectWorkflowRunForBrowserWithStore(ctx, store, first, false)
		if projected.Origin != nil {
			t.Fatalf("projected cyclic origin = %#v, want nil", projected.Origin)
		}
	})

	t.Run("long retry lineage is retained and fully validated", func(t *testing.T) {
		store := NewFileRunStore(t.TempDir())
		if err := store.CreateRun(ctx, root); err != nil {
			t.Fatalf("CreateRun(root) error = %v", err)
		}
		previous := root
		const retryCount = 160
		for index := 0; index < retryCount; index++ {
			current := &Run{
				ID:           fmt.Sprintf("wr_long_retry_%03d", index),
				WorkflowRef:  root.WorkflowRef,
				RetryOfRunID: previous.ID,
				Event:        testRunOriginEvent(),
				Inputs:       cloneMap(root.Inputs),
				Origin:       cloneRunOrigin(rootOrigin),
			}
			if err := store.CreateRun(ctx, current); err != nil {
				t.Fatalf("CreateRun(%s) error = %v", current.ID, err)
			}
			previous = current
		}

		projected := ProjectWorkflowRunForBrowserWithStore(
			ctx,
			store,
			previous,
			false,
		)
		if !reflect.DeepEqual(projected.Origin, rootOrigin) {
			t.Fatalf(
				"projected long-lineage origin = %#v, want %#v",
				projected.Origin,
				rootOrigin,
			)
		}

		invalidRoot := cloneRun(root)
		invalidRoot.Origin.DispatchID = "dsp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if err := store.UpdateRun(ctx, invalidRoot); err != nil {
			t.Fatalf("UpdateRun(invalid root) error = %v", err)
		}
		projected = ProjectWorkflowRunForBrowserWithStore(
			ctx,
			store,
			previous,
			false,
		)
		if projected.Origin != nil {
			t.Fatalf(
				"projected origin past invalid deep ancestor = %#v, want nil",
				projected.Origin,
			)
		}
	})
}

func TestTrustedRunOriginsBatchSharesLineageAndHonorsCancellation(t *testing.T) {
	const runCount = 256
	origin := &RunOrigin{
		Kind:       RunOriginExternalEvent,
		EventID:    testRunOriginEventID,
		DispatchID: testRunOriginDispatchID,
		RootRunID:  "wr_batch_root",
	}
	rootInputs := map[string]any{
		"event_id":    testRunOriginEventID,
		"dispatch_id": testRunOriginDispatchID,
	}
	ascending := make([]*Run, 0, runCount)
	ascending = append(ascending, &Run{
		ID:          origin.RootRunID,
		WorkflowRef: "workflows/batch.yml",
		Event:       testRunOriginEvent(),
		Inputs:      cloneMap(rootInputs),
		Origin:      cloneRunOrigin(origin),
	})
	for index := 1; index < runCount; index++ {
		ascending = append(ascending, &Run{
			ID:           fmt.Sprintf("wr_batch_retry_%03d", index),
			WorkflowRef:  "workflows/batch.yml",
			RetryOfRunID: ascending[index-1].ID,
			Event:        testRunOriginEvent(),
			Inputs:       cloneMap(rootInputs),
			Origin:       cloneRunOrigin(origin),
		})
	}
	descending := make([]*Run, len(ascending))
	byID := make(map[string]*Run, len(ascending))
	for index, run := range ascending {
		descending[len(ascending)-1-index] = run
		byID[run.ID] = run
	}

	lookups := 0
	trusted := trustedRunOriginsWithLookup(
		context.Background(),
		descending,
		func(_ context.Context, runID string) (*Run, error) {
			lookups++
			return byID[runID], nil
		},
	)
	if len(trusted) != len(descending) {
		t.Fatalf(
			"trusted batch size = %d, want %d",
			len(trusted),
			len(descending),
		)
	}
	if lookups > len(descending)-1 {
		t.Fatalf(
			"batch lineage lookups = %d, want at most %d",
			lookups,
			len(descending)-1,
		)
	}

	fallbackOrigin, fallbackTrusted := trustedRunOriginWithStore(
		context.Background(),
		nil,
		ascending[0],
	)
	if !fallbackTrusted || !reflect.DeepEqual(fallbackOrigin, origin) {
		t.Fatalf(
			"nil-store trusted origin = (%#v, %v), want (%#v, true)",
			fallbackOrigin,
			fallbackTrusted,
			origin,
		)
	}

	failedLookups := 0
	failed := trustedRunOriginsWithLookup(
		context.Background(),
		[]*Run{ascending[1]},
		func(context.Context, string) (*Run, error) {
			failedLookups++
			return nil, errors.New("store unavailable")
		},
	)
	if len(failed) != 0 || failedLookups != 1 {
		t.Fatalf(
			"failed batch lookup = (%#v, %d lookups), want no trusted origins after one lookup",
			failed,
			failedLookups,
		)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelLookups := 0
	canceled := trustedRunOriginsWithLookup(
		ctx,
		descending,
		func(_ context.Context, runID string) (*Run, error) {
			cancelLookups++
			if cancelLookups == 8 {
				cancel()
			}
			return byID[runID], nil
		},
	)
	if len(canceled) != 0 {
		t.Fatalf("canceled batch trusted origins = %#v, want none", canceled)
	}
	if cancelLookups > 8 {
		t.Fatalf(
			"canceled batch performed %d lookups, want at most 8",
			cancelLookups,
		)
	}
}

func TestExecutorRetryIgnoresUntrustedPersistedOrigin(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "origin-retry.yml", `
name: Origin retry
on:
  manual: {}
jobs:
  work:
    runs-on: picoclaw
    steps:
      - uses: function/origin_retry_noop
`)
	functions := NewFunctionRegistry()
	if err := functions.Register(
		"origin_retry_noop",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	store := NewFileRunStore(workspace)
	source := &Run{
		ID:          "wr_untrusted_retry_source",
		WorkflowRef: "workflows/origin-retry.yml",
		Status:      RunStatusSucceeded,
		Event:       testRunOriginEvent(),
		Inputs: map[string]any{
			"event_id":    testRunOriginEventID,
			"dispatch_id": testRunOriginDispatchID,
		},
		Origin: &RunOrigin{
			Kind:       RunOriginExternalEvent,
			EventID:    testRunOriginEventID,
			DispatchID: "dsp_invalid",
			RootRunID:  "wr_untrusted_retry_source",
		},
	}
	if err := store.CreateRun(context.Background(), source); err != nil {
		t.Fatalf("CreateRun(source) error = %v", err)
	}
	result, err := (&Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    functions,
	}).Retry(context.Background(), source.ID, nil)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	retry, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun(retry) error = %v", err)
	}
	if retry.RetryOfRunID != source.ID || retry.Origin != nil {
		t.Fatalf("retry = %#v, want retry link with nil origin", retry)
	}
}

func TestExecutorRetryRetainsTrustedOriginAfterAncestorPruning(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "origin-pruned-retry.yml", `
name: Origin pruned retry
on:
  manual: {}
jobs:
  work:
    runs-on: picoclaw
    steps:
      - uses: function/origin_pruned_retry_noop
`)
	functions := NewFunctionRegistry()
	if err := functions.Register(
		"origin_pruned_retry_noop",
		func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	store := NewFileRunStore(workspace)
	origin := &RunOrigin{
		Kind:       RunOriginExternalEvent,
		EventID:    testRunOriginEventID,
		DispatchID: testRunOriginDispatchID,
		RootRunID:  "wr_pruned_retry_root",
	}
	root := &Run{
		ID:          origin.RootRunID,
		WorkflowRef: "workflows/origin-pruned-retry.yml",
		Status:      RunStatusSucceeded,
		Event:       testRunOriginEvent(),
		Inputs: map[string]any{
			"event_id":    testRunOriginEventID,
			"dispatch_id": testRunOriginDispatchID,
		},
		Origin: cloneRunOrigin(origin),
	}
	source := cloneRun(root)
	source.ID = "wr_pruned_retry_source"
	source.RetryOfRunID = root.ID
	if err := store.CreateRun(context.Background(), root); err != nil {
		t.Fatalf("CreateRun(root) error = %v", err)
	}
	if err := store.CreateRun(context.Background(), source); err != nil {
		t.Fatalf("CreateRun(source) error = %v", err)
	}
	if err := store.DeleteRun(context.Background(), root.ID); err != nil {
		t.Fatalf("DeleteRun(root) error = %v", err)
	}

	result, err := (&Executor{
		WorkspaceDir: workspace,
		Store:        store,
		Functions:    functions,
	}).Retry(context.Background(), source.ID, nil)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	retry, err := store.GetRun(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("GetRun(retry) error = %v", err)
	}
	if retry.RetryOfRunID != source.ID || !reflect.DeepEqual(retry.Origin, origin) {
		t.Fatalf(
			"retry = %#v, want retry link and retained origin %#v",
			retry,
			origin,
		)
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
