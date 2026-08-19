package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/session"
)

func TestCompiledGatePrivateContextIsLocalAndFailsClosedOnCorruption(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	const subjectCanary = "private-gate-subject-canary-8f6499"

	compilation, err := CompileGateWorkflow("Private deterministic gate", []GateSpec{{
		ID:        "policy",
		Kind:      GateDeterministic,
		When:      "${{ inputs.gate_subject.ask == true }}",
		Title:     "Policy approval",
		Questions: []any{"Approve?"},
	}}, map[string]any{
		"ask":   false,
		"token": subjectCanary,
	})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	if compilation.PrivateRoot == nil {
		t.Fatal("CompileGateWorkflow() private root = nil")
	}
	if len(compilation.Inputs) != 0 {
		t.Fatalf("CompileGateWorkflow() public inputs = %#v, want empty", compilation.Inputs)
	}
	assertPrivateGateTestContains(t, "compiler private values", compilation.PrivateRoot.Values, subjectCanary)
	assertPrivateGateTestOmits(t, "compiler public JSON", compilation, subjectCanary)

	result, err := (&Executor{
		WorkspaceDir: workspace,
		Store:        store,
	}).Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-deterministic-gate",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() result = %#v, want succeeded", result)
	}
	assertPrivateGateTestOmits(t, "run result", result, subjectCanary)

	persisted, err := store.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if !IsPrivateWorkflowRun(persisted) || persisted.privateRoot == nil {
		t.Fatalf("persisted run private context = %#v", persisted)
	}
	assertPrivateGateTestOmits(t, "ordinary Run JSON", persisted, subjectCanary)
	assertPrivateGateTestOmits(
		t,
		"browser run projection",
		ProjectWorkflowRunForBrowser(persisted, false),
		subjectCanary,
	)
	events, err := store.Events(ctx, result.RunID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	assertPrivateGateTestEventsRedacted(t, events)
	assertPrivateGateTestOmits(t, "stored run events", events, subjectCanary)
	assertPrivateGateTestOmits(
		t,
		"browser event projection",
		ProjectWorkflowRunEventsForBrowser(events, false, true),
		subjectCanary,
	)

	runPath := filepath.Join(workspace, "workflow_runs", result.RunID, "run.json")
	raw, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("ReadFile(run.json) error = %v", err)
	}
	if !bytes.Contains(raw, []byte(`"private_context"`)) ||
		!bytes.Contains(raw, []byte(subjectCanary)) {
		t.Fatalf("run.json does not contain the local private context: %s", raw)
	}
	info, err := os.Stat(runPath)
	if err != nil {
		t.Fatalf("Stat(run.json) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("run.json permissions = %o, want 600", got)
	}

	reloaded, err := NewFileRunStore(workspace).GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("restarted GetRun() error = %v", err)
	}
	if reloaded.privateRoot == nil {
		t.Fatal("restarted GetRun() private root = nil")
	}
	assertPrivateGateTestContains(t, "reloaded private values", reloaded.privateRoot.Values, subjectCanary)
	assertPrivateGateTestOmits(t, "reloaded Run JSON", reloaded, subjectCanary)

	if count := bytes.Count(raw, []byte(subjectCanary)); count != 1 {
		t.Fatalf("run.json subject canary occurrences = %d, want exactly one private copy", count)
	}
	corrupted := bytes.Replace(
		raw,
		[]byte(subjectCanary),
		[]byte("tampered-private-gate-subject"),
		1,
	)
	if err := os.WriteFile(runPath, corrupted, 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt run.json) error = %v", err)
	}
	if _, err := NewFileRunStore(workspace).GetRun(ctx, result.RunID); !errors.Is(err, ErrPrivateWorkflowContext) {
		t.Fatalf("GetRun(corrupt private context) error = %v, want %v", err, ErrPrivateWorkflowContext)
	}
}

func TestPrivateGateCyclicMutationsFailBeforeDurableCreate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GateCompilation, *RunRequest)
	}{
		{
			name: "private values",
			mutate: func(compilation *GateCompilation, _ *RunRequest) {
				cycle := map[string]any{}
				cycle["self"] = cycle
				compilation.PrivateRoot.Values["cycle"] = cycle
			},
		},
		{
			name: "mixed inputs",
			mutate: func(_ *GateCompilation, request *RunRequest) {
				cycle := map[string]any{}
				cycle["self"] = cycle
				request.Inputs = cycle
			},
		},
		{
			name: "mixed event",
			mutate: func(_ *GateCompilation, request *RunRequest) {
				cycle := map[string]any{}
				cycle["self"] = cycle
				request.Event = cycle
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			compilation, err := CompileGateWorkflow("Private cyclic mutation", []GateSpec{{
				ID:        "policy",
				Kind:      GateDeterministic,
				When:      "false",
				Title:     "Policy",
				Questions: []any{"Approve?"},
			}}, map[string]any{"private": "cycle-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			request := RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-cyclic-mutation",
				PrivateRoot: compilation.PrivateRoot,
			}
			test.mutate(compilation, &request)
			result, runErr := (&Executor{
				WorkspaceDir: workspace,
				Store:        store,
			}).Run(context.Background(), request)
			if result != nil || !errors.Is(runErr, ErrPrivateWorkflowContext) {
				t.Fatalf(
					"Run() = (%#v, %v), want private-context rejection",
					result,
					runErr,
				)
			}
			runs, listErr := store.ListRuns(context.Background())
			if listErr != nil || len(runs) != 0 {
				t.Fatalf("ListRuns() = (%#v, %v), want no durable run", runs, listErr)
			}
		})
	}
}

func TestPrivateGateWorkflowCaptureRejectsCustomMarshalerWithoutInvokingIt(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private single capture", []GateSpec{{
		ID:        "approval",
		Kind:      GateDeterministic,
		When:      "true",
		Title:     "Approval",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "single-capture-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	stateful := &privateGateStatefulJSONMarshaler{}
	job := compilation.Workflow.Jobs[workflowGateJobID]
	job.Steps[0].With["questions"] = stateful
	compilation.Workflow.Jobs[workflowGateJobID] = job

	result, runErr := (&Executor{WorkspaceDir: workspace, Store: store}).Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-single-capture",
		PrivateRoot: compilation.PrivateRoot,
	})
	if result != nil || runErr != ErrPrivateWorkflowContext {
		t.Fatalf("Run() = (%#v, %v), want private-context rejection", result, runErr)
	}
	if stateful.calls != 0 {
		t.Fatalf("custom marshaler calls = %d, want zero", stateful.calls)
	}
	runs, listErr := store.ListRuns(ctx)
	if listErr != nil || len(runs) != 0 {
		t.Fatalf("ListRuns() = (%#v, %v), want no durable run", runs, listErr)
	}
}

func TestPrivateGateCapturedRetryCyclicMutationsFailBeforeClone(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Run)
	}{
		{
			name: "mixed inputs",
			mutate: func(source *Run) {
				cycle := map[string]any{}
				cycle["self"] = cycle
				source.Inputs = cycle
			},
		},
		{
			name: "private values",
			mutate: func(source *Run) {
				cycle := map[string]any{}
				cycle["self"] = cycle
				source.privateRoot.Values["cycle"] = cycle
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			executor := &Executor{WorkspaceDir: workspace, Store: store}
			compilation, err := CompileGateWorkflow("Private retry cycle", []GateSpec{{
				ID:        "policy",
				Kind:      GateDeterministic,
				When:      "false",
				Title:     "Policy",
				Questions: []any{"Approve?"},
			}}, map[string]any{"private": "retry-cycle-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			initial, err := executor.Run(ctx, RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-retry-cycle",
				PrivateRoot: compilation.PrivateRoot,
			})
			if err != nil || initial == nil || initial.Status != RunStatusSucceeded {
				t.Fatalf("initial Run() = (%#v, %v), want succeeded", initial, err)
			}
			source, err := store.GetRun(ctx, initial.RunID)
			if err != nil {
				t.Fatalf("GetRun(source) error = %v", err)
			}
			test.mutate(source)
			result, retryErr := executor.RetryCaptured(ctx, source, nil)
			if result != nil || retryErr != ErrPrivateWorkflowContext {
				t.Fatalf(
					"RetryCaptured() = (%#v, %v), want private-context rejection",
					result,
					retryErr,
				)
			}
			runs, listErr := store.ListRuns(ctx)
			if listErr != nil || len(runs) != 1 {
				t.Fatalf("ListRuns() = (%#v, %v), want source only", runs, listErr)
			}
		})
	}
}

func TestPrivateGateStoreRejectsInjectedPublicRunContext(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Run)
	}{
		{
			name: "inputs",
			mutate: func(run *Run) {
				cycle := map[string]any{}
				cycle["self"] = cycle
				run.Inputs = cycle
			},
		},
		{name: "event", mutate: func(run *Run) { run.Event = map[string]any{"injected": true} }},
		{
			name: "origin",
			mutate: func(run *Run) {
				run.Origin = &RunOrigin{Kind: RunOriginExternalEvent, EventID: "evt_injected"}
			},
		},
		{name: "session", mutate: func(run *Run) { run.Session = "injected-session" }},
		{
			name:   "delivery",
			mutate: func(run *Run) { run.Delivery = Delivery{Channel: "injected-channel"} },
		},
		{name: "parent", mutate: func(run *Run) { run.ParentRunID = "wr_injected_parent" }},
		{name: "caller", mutate: func(run *Run) { run.CallerJobID = "injected-job" }},
		{name: "children", mutate: func(run *Run) { run.ChildRunIDs = []string{"wr_injected_child"} }},
		{name: "retry whitespace", mutate: func(run *Run) { run.RetryOfRunID = " " }},
		{name: "retry provenance", mutate: func(run *Run) { run.RetryOfRunID = "wr_injected_source" }},
		{name: "workflow ref", mutate: func(run *Run) { run.WorkflowRef = "inline/injected" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			executor := &Executor{WorkspaceDir: workspace, Store: store}
			compilation, err := CompileGateWorkflow("Private update injection", []GateSpec{{
				ID:        "approval",
				Kind:      GateDeterministic,
				When:      "true",
				Title:     "Approval",
				Questions: []any{"Proceed?"},
			}}, map[string]any{"private": "update-injection-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			waiting, err := executor.Run(ctx, RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-update-injection",
				PrivateRoot: compilation.PrivateRoot,
			})
			if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
				t.Fatalf("Run() = (%#v, %v), want waiting", waiting, err)
			}
			runPath := filepath.Join(workspace, "workflow_runs", waiting.RunID, "run.json")
			before, err := os.ReadFile(runPath)
			if err != nil {
				t.Fatalf("ReadFile(before) error = %v", err)
			}
			candidate, err := store.GetRun(ctx, waiting.RunID)
			if err != nil {
				t.Fatalf("GetRun(candidate) error = %v", err)
			}
			test.mutate(candidate)
			if updateErr := store.UpdateRun(ctx, candidate); updateErr != ErrPrivateWorkflowContext {
				t.Fatalf("UpdateRun() error = %v, want %v", updateErr, ErrPrivateWorkflowContext)
			}
			after, err := os.ReadFile(runPath)
			if err != nil {
				t.Fatalf("ReadFile(after) error = %v", err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("rejected private update changed durable run bytes")
			}
			reloaded, err := store.GetRun(ctx, waiting.RunID)
			if err != nil || reloaded.Status != RunStatusWaiting ||
				validatePrivateRunInvocationEnvelope(reloaded) != nil {
				t.Fatalf("GetRun(original) = (%#v, %v), want unchanged waiting run", reloaded, err)
			}
		})
	}
}

func TestPrivateGateStoreDoesNotRecreateMissingPrivateRun(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private missing record", []GateSpec{{
		ID:        "approval",
		Kind:      GateDeterministic,
		When:      "true",
		Title:     "Approval",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "missing-record-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	waiting, err := (&Executor{WorkspaceDir: workspace, Store: store}).Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-missing-record",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
		t.Fatalf("Run() = (%#v, %v), want waiting", waiting, err)
	}
	persisted, err := store.GetRun(ctx, waiting.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	forged := cloneRun(persisted)
	forged.ID = "wr_private_forged_copy"
	if updateErr := store.UpdateRun(ctx, forged); updateErr != ErrPrivateWorkflowContext {
		t.Fatalf("UpdateRun(forged private ID) error = %v, want %v", updateErr, ErrPrivateWorkflowContext)
	}
	forgedPath := filepath.Join(workspace, "workflow_runs", forged.ID, "run.json")
	if _, statErr := os.Stat(forgedPath); !os.IsNotExist(statErr) {
		t.Fatalf("forged private run record exists: %v", statErr)
	}
	runPath := filepath.Join(workspace, "workflow_runs", waiting.RunID, "run.json")
	if err := os.Remove(runPath); err != nil {
		t.Fatalf("Remove(run.json) error = %v", err)
	}
	if updateErr := store.UpdateRun(ctx, persisted); updateErr != ErrPrivateWorkflowContext {
		t.Fatalf("UpdateRun(missing private run) error = %v, want %v", updateErr, ErrPrivateWorkflowContext)
	}
	if _, statErr := os.Stat(runPath); !os.IsNotExist(statErr) {
		t.Fatalf("missing private run was recreated: %v", statErr)
	}
}

func TestPrivateGatePersistedRunBindingRejectsRawProvenanceTamper(t *testing.T) {
	for _, test := range []struct {
		name  string
		field string
		value string
	}{
		{name: "id", field: "id", value: "wr_forged_private_id"},
		{name: "workflow ref", field: "workflow_ref", value: "inline/forged-private-workflow"},
		{name: "retry provenance", field: "retry_of_run_id", value: "wr_forged_private_source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			compilation, err := CompileGateWorkflow("Private bound record", []GateSpec{{
				ID:        "policy",
				Kind:      GateDeterministic,
				When:      "false",
				Title:     "Policy",
				Questions: []any{"Proceed?"},
			}}, map[string]any{"private": "bound-record-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			result, err := (&Executor{WorkspaceDir: workspace, Store: store}).Run(ctx, RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-bound-record",
				PrivateRoot: compilation.PrivateRoot,
			})
			if err != nil || result == nil || result.Status != RunStatusSucceeded {
				t.Fatalf("Run() = (%#v, %v), want succeeded", result, err)
			}
			runPath := filepath.Join(workspace, "workflow_runs", result.RunID, "run.json")
			raw, err := os.ReadFile(runPath)
			if err != nil {
				t.Fatalf("ReadFile(run.json) error = %v", err)
			}
			var record map[string]any
			if unmarshalErr := json.Unmarshal(raw, &record); unmarshalErr != nil {
				t.Fatalf("json.Unmarshal(run.json) error = %v", unmarshalErr)
			}
			record[test.field] = test.value
			tampered, err := json.Marshal(record)
			if err != nil {
				t.Fatalf("json.Marshal(tampered run) error = %v", err)
			}
			if writeErr := os.WriteFile(runPath, tampered, 0o600); writeErr != nil {
				t.Fatalf("WriteFile(tampered run.json) error = %v", writeErr)
			}
			if _, getErr := store.GetRun(ctx, result.RunID); getErr != ErrPrivateWorkflowContext {
				t.Fatalf("GetRun(tampered %s) error = %v, want %v", test.field, getErr, ErrPrivateWorkflowContext)
			}
		})
	}
}

func TestPrivateGateIndependentMarkerRejectsJSONPrivacyDowngrade(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private downgrade marker", []GateSpec{{
		ID:        "policy",
		Kind:      GateDeterministic,
		When:      "false",
		Title:     "Policy",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "downgrade-marker-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	result, err := (&Executor{WorkspaceDir: workspace, Store: store}).Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-downgrade-marker",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() = (%#v, %v), want succeeded", result, err)
	}
	runDir := filepath.Join(workspace, "workflow_runs", result.RunID)
	markerPath := filepath.Join(runDir, privateRunMarkerFilename)
	marker, err := os.ReadFile(markerPath)
	if err != nil || string(marker) != privateRunMarkerContents {
		t.Fatalf("private marker = (%q, %v)", marker, err)
	}
	markerInfo, err := os.Stat(markerPath)
	if err != nil || markerInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private marker mode = (%v, %v), want 0600", markerInfo, err)
	}
	runPath := filepath.Join(runDir, "run.json")
	raw, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("ReadFile(run.json) error = %v", err)
	}
	var document map[string]json.RawMessage
	if unmarshalErr := json.Unmarshal(raw, &document); unmarshalErr != nil {
		t.Fatalf("json.Unmarshal(run.json) error = %v", unmarshalErr)
	}
	delete(document, "private_context")
	delete(document, "context_visibility")
	downgraded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("json.Marshal(downgraded run) error = %v", err)
	}
	if writeErr := os.WriteFile(runPath, downgraded, 0o600); writeErr != nil {
		t.Fatalf("WriteFile(downgraded run.json) error = %v", writeErr)
	}
	if _, getErr := store.GetRun(ctx, result.RunID); getErr != ErrPrivateWorkflowContext {
		t.Fatalf("GetRun(downgraded private run) error = %v, want %v", getErr, ErrPrivateWorkflowContext)
	}
	if _, eventsErr := store.Events(ctx, result.RunID); eventsErr != ErrPrivateWorkflowContext {
		t.Fatalf("Events(downgraded private run) error = %v, want %v", eventsErr, ErrPrivateWorkflowContext)
	}
	if runs, listErr := store.ListRuns(ctx); listErr != nil || len(runs) != 0 {
		t.Fatalf("ListRuns(downgraded private run) = (%#v, %v), want omitted", runs, listErr)
	}
}

func TestPrivateGateStoreKeyRejectsRenamedRunDirectory(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private store key", []GateSpec{{
		ID:        "policy",
		Kind:      GateDeterministic,
		When:      "false",
		Title:     "Policy",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "store-key-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	result, err := executor.Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-store-key",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() = (%#v, %v), want succeeded", result, err)
	}
	const renamedID = "wr_private_renamed_directory"
	root := filepath.Join(workspace, "workflow_runs")
	if err := os.Rename(filepath.Join(root, result.RunID), filepath.Join(root, renamedID)); err != nil {
		t.Fatalf("Rename(private run directory) error = %v", err)
	}
	if _, getErr := store.GetRun(ctx, renamedID); getErr != ErrPrivateWorkflowContext {
		t.Fatalf("GetRun(renamed private directory) error = %v, want %v", getErr, ErrPrivateWorkflowContext)
	}
	if retried, retryErr := executor.Retry(ctx, renamedID, nil); retried != nil ||
		retryErr != ErrPrivateWorkflowContext {
		t.Fatalf("Retry(renamed private directory) = (%#v, %v), want rejection", retried, retryErr)
	}
}

func TestPrivateGateStoreMutationsRejectSafeIDAlias(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private exact store key", []GateSpec{{
		ID:        "policy",
		Kind:      GateDeterministic,
		When:      "false",
		Title:     "Policy",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "exact-store-key-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	result, err := executor.Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-exact-store-key",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() = (%#v, %v), want succeeded", result, err)
	}
	alias := strings.Replace(result.RunID, "_", "/", 1)
	if alias == result.RunID || safeID(alias) != result.RunID {
		t.Fatalf("test alias %q does not map to %q", alias, result.RunID)
	}
	if canceled, cancelErr := executor.CancelRun(ctx, alias, "alias cancel"); canceled != nil ||
		cancelErr != ErrPrivateWorkflowContext {
		t.Fatalf("CancelRun(alias) = (%#v, %v), want rejection", canceled, cancelErr)
	}
	if appendErr := store.AppendEvent(ctx, RunEvent{
		Kind: "workflow.alias", RunID: alias, Message: "alias-event-canary",
	}); appendErr != ErrPrivateWorkflowContext {
		t.Fatalf("AppendEvent(alias) error = %v, want %v", appendErr, ErrPrivateWorkflowContext)
	}
	if _, eventsErr := store.Events(ctx, alias); eventsErr != ErrPrivateWorkflowContext {
		t.Fatalf("Events(alias) error = %v, want %v", eventsErr, ErrPrivateWorkflowContext)
	}
	if deleteErr := store.DeleteRun(ctx, alias); deleteErr != ErrPrivateWorkflowContext {
		t.Fatalf("DeleteRun(alias) error = %v, want %v", deleteErr, ErrPrivateWorkflowContext)
	}
	persisted, err := store.GetRun(ctx, result.RunID)
	if err != nil || persisted.Status != RunStatusSucceeded {
		t.Fatalf("GetRun(victim) = (%#v, %v), want unchanged succeeded", persisted, err)
	}
}

func TestPrivateGateRunBindingRejectsCrossRunRootAndExecutionSplice(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	results := make([]*RunResult, 0, 2)
	for index := range 2 {
		compilation, err := CompileGateWorkflow(fmt.Sprintf("Private splice %d", index), []GateSpec{{
			ID:        "policy",
			Kind:      GateDeterministic,
			When:      "false",
			Title:     "Policy",
			Questions: []any{"Proceed?"},
		}}, map[string]any{"private": fmt.Sprintf("splice-subject-%d", index)})
		if err != nil {
			t.Fatalf("CompileGateWorkflow(%d) error = %v", index, err)
		}
		result, runErr := executor.Run(ctx, RunRequest{
			Workflow:    compilation.Workflow,
			WorkflowRef: fmt.Sprintf("inline/private-splice-%d", index),
			PrivateRoot: compilation.PrivateRoot,
		})
		if runErr != nil || result == nil || result.Status != RunStatusSucceeded {
			t.Fatalf("Run(%d) = (%#v, %v), want succeeded", index, result, runErr)
		}
		results = append(results, result)
	}
	documents := make([]map[string]json.RawMessage, 2)
	paths := make([]string, 2)
	for index, result := range results {
		paths[index] = filepath.Join(workspace, "workflow_runs", result.RunID, "run.json")
		raw, err := os.ReadFile(paths[index])
		if err != nil {
			t.Fatalf("ReadFile(run %d) error = %v", index, err)
		}
		if err := json.Unmarshal(raw, &documents[index]); err != nil {
			t.Fatalf("json.Unmarshal(run %d) error = %v", index, err)
		}
	}
	var sourceRoot, targetRoot map[string]json.RawMessage
	if err := json.Unmarshal(documents[0]["private_context"], &sourceRoot); err != nil {
		t.Fatalf("json.Unmarshal(source private root) error = %v", err)
	}
	if err := json.Unmarshal(documents[1]["private_context"], &targetRoot); err != nil {
		t.Fatalf("json.Unmarshal(target private root) error = %v", err)
	}
	for _, field := range []string{"values", "read_only_session", "revision"} {
		if value, exists := sourceRoot[field]; exists {
			targetRoot[field] = value
		} else {
			delete(targetRoot, field)
		}
	}
	splicedRoot, err := json.Marshal(targetRoot)
	if err != nil {
		t.Fatalf("json.Marshal(spliced private root) error = %v", err)
	}
	documents[1]["private_context"] = splicedRoot
	documents[1]["execution"] = documents[0]["execution"]
	splicedRun, err := json.Marshal(documents[1])
	if err != nil {
		t.Fatalf("json.Marshal(spliced run) error = %v", err)
	}
	if err := os.WriteFile(paths[1], splicedRun, 0o600); err != nil {
		t.Fatalf("WriteFile(spliced run) error = %v", err)
	}
	if _, getErr := store.GetRun(ctx, results[1].RunID); getErr != ErrPrivateWorkflowContext {
		t.Fatalf("GetRun(spliced private run) error = %v, want %v", getErr, ErrPrivateWorkflowContext)
	}
}

func TestPrivateGateFileEventStoreRedactsBeforeEncodingAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	const canary = "private-direct-event-canary-6f03a8"
	compilation, err := CompileGateWorkflow("Private event boundary", []GateSpec{{
		ID:        "policy",
		Kind:      GateDeterministic,
		When:      "false",
		Title:     "Policy",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": canary})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	result, err := (&Executor{WorkspaceDir: workspace, Store: store}).Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-event-boundary",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() = (%#v, %v), want succeeded", result, err)
	}
	cycle := map[string]any{"canary": canary}
	cycle["self"] = cycle
	if appendErr := store.AppendEvent(ctx, RunEvent{
		Kind: "workflow.direct", RunID: result.RunID, Message: canary, Payload: cycle,
	}); appendErr != nil {
		t.Fatalf("AppendEvent(private cyclic payload) error = %v", appendErr)
	}
	runDir := filepath.Join(workspace, "workflow_runs", result.RunID)
	eventsPath := filepath.Join(runDir, "events.jsonl")
	raw, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("ReadFile(events.jsonl) error = %v", err)
	}
	if bytes.Contains(raw, []byte(canary)) {
		t.Fatalf("direct private event persisted canary: %s", raw)
	}
	legacy := []byte(fmt.Sprintf(
		"{\"kind\":\"workflow.legacy\",\"run_id\":%q,\"message\":%q,\"payload\":{\"secret\":%q}}\n",
		result.RunID,
		canary,
		canary,
	))
	file, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(events.jsonl) error = %v", err)
	}
	if _, err = file.Write(legacy); err != nil {
		_ = file.Close()
		t.Fatalf("Write(legacy event) error = %v", err)
	}
	if err = file.Close(); err != nil {
		t.Fatalf("Close(events.jsonl) error = %v", err)
	}
	events, err := store.Events(ctx, result.RunID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	assertPrivateGateTestEventsRedacted(t, events)
	assertPrivateGateTestOmits(t, "direct event read projection", events, canary)

	runPath := filepath.Join(runDir, "run.json")
	if removeErr := os.Remove(runPath); removeErr != nil {
		t.Fatalf("Remove(run.json) error = %v", removeErr)
	}
	before, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("ReadFile(events before rejection) error = %v", err)
	}
	if _, eventsErr := store.Events(ctx, result.RunID); eventsErr != ErrPrivateWorkflowContext {
		t.Fatalf("Events(missing run) error = %v, want %v", eventsErr, ErrPrivateWorkflowContext)
	}
	if appendErr := store.AppendEvent(ctx, RunEvent{
		Kind: "workflow.after-delete", RunID: result.RunID, Message: canary,
	}); appendErr != ErrPrivateWorkflowContext {
		t.Fatalf("AppendEvent(missing run) error = %v, want %v", appendErr, ErrPrivateWorkflowContext)
	}
	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("ReadFile(events after rejection) error = %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("rejected append changed events for a missing private run")
	}
}

func TestPrivateGateUnknownRootFieldFailsClosedAtEveryStoreReadBoundary(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	const fieldCanary = "private-unknown-root-field-canary-7e62d1"
	compilation, err := CompileGateWorkflow("Private strict root", []GateSpec{{
		ID: "policy", Kind: GateDeterministic, When: "false", Title: "Policy",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "strict-root-subject"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	result, err := (&Executor{WorkspaceDir: workspace, Store: store}).Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-strict-root",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || result == nil || result.Status != RunStatusSucceeded {
		t.Fatalf("Run() = (%#v, %v), want succeeded", result, err)
	}
	runPath := filepath.Join(workspace, "workflow_runs", result.RunID, "run.json")
	raw, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("ReadFile(run.json) error = %v", err)
	}
	needle := []byte(`"private_context": {`)
	if !bytes.Contains(raw, needle) {
		t.Fatalf("run.json has no private root: %s", raw)
	}
	tampered := bytes.Replace(
		raw,
		needle,
		[]byte(`"private_context": {"`+fieldCanary+`":true,`),
		1,
	)
	if err := os.WriteFile(runPath, tampered, 0o600); err != nil {
		t.Fatalf("WriteFile(tampered run.json) error = %v", err)
	}
	if _, getErr := store.GetRun(ctx, result.RunID); getErr != ErrPrivateWorkflowContext {
		t.Fatalf("GetRun(unknown private field) error = %v, want %v", getErr, ErrPrivateWorkflowContext)
	} else {
		assertPrivateGateTestOmits(t, "unknown private field error", getErr, fieldCanary)
	}
	if _, eventsErr := store.Events(ctx, result.RunID); eventsErr != ErrPrivateWorkflowContext {
		t.Fatalf("Events(unknown private field) error = %v, want %v", eventsErr, ErrPrivateWorkflowContext)
	}
	if appendErr := store.AppendEvent(ctx, RunEvent{
		Kind: "workflow.tampered", RunID: result.RunID, Message: fieldCanary,
	}); appendErr != ErrPrivateWorkflowContext {
		t.Fatalf("AppendEvent(unknown private field) error = %v, want %v", appendErr, ErrPrivateWorkflowContext)
	}
}

func TestPrivateGateRawPublicContextTamperBlocksResumeBeforeClaim(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	compilation, err := CompileGateWorkflow("Private raw injection", []GateSpec{{
		ID:        "approval",
		Kind:      GateDeterministic,
		When:      "true",
		Title:     "Approval",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "raw-injection-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	waiting, err := executor.Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-raw-injection",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
		t.Fatalf("Run() = (%#v, %v), want waiting", waiting, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, waiting.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, err)
	}
	runPath := filepath.Join(workspace, "workflow_runs", waiting.RunID, "run.json")
	raw, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("ReadFile(run) error = %v", err)
	}
	var record map[string]any
	if unmarshalErr := json.Unmarshal(raw, &record); unmarshalErr != nil {
		t.Fatalf("json.Unmarshal(run) error = %v", unmarshalErr)
	}
	record["session"] = "raw-private-session-canary-83ac17"
	tampered, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal(tampered run) error = %v", err)
	}
	if writeErr := os.WriteFile(runPath, tampered, 0o600); writeErr != nil {
		t.Fatalf("WriteFile(tampered run) error = %v", writeErr)
	}
	if _, getErr := store.GetRun(ctx, waiting.RunID); getErr != ErrPrivateWorkflowContext {
		t.Fatalf("GetRun(tampered) error = %v, want private-context rejection", getErr)
	}
	executor.AdmittedHumanTaskClaim = func(
		_ context.Context,
		_, _ string,
		claim func() (*Run, WorkflowHumanTask, bool, error),
	) (*Run, WorkflowHumanTask, bool, error) {
		return claim()
	}
	result, resumeErr := executor.ResumeHumanTask(ctx, waiting.RunID, tasks[0].ID, HumanTaskResumeRequest{
		ExpectedRevision: tasks[0].Revision,
		InputHash:        tasks[0].InputHash,
		ResponseID:       "response-1",
		Response:         "approved",
	})
	if result != nil || resumeErr != ErrPrivateWorkflowContext {
		t.Fatalf("ResumeHumanTask() = (%#v, %v), want private-context rejection", result, resumeErr)
	}
	after, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("ReadFile(after rejected resume) error = %v", err)
	}
	if !bytes.Equal(after, tampered) {
		t.Fatal("rejected resume changed the tampered private run")
	}
}

func TestPrivateGateResumeRejectsClaimWrapperContextInjection(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	agents := &privateGateSecurityAgentRunner{outputs: map[string]any{
		"structured": map[string]any{
			"ask_user":  false,
			"reason":    "",
			"questions": []any{},
		},
	}}
	executor := &Executor{WorkspaceDir: workspace, Store: store, Agents: agents}
	compilation, err := CompileGateWorkflow("Private claim injection", []GateSpec{
		{
			ID:        "approval",
			Kind:      GateDeterministic,
			When:      "true",
			Title:     "Approval",
			Questions: []any{"Proceed?"},
		},
		{
			ID:       "decision",
			Kind:     GateAIIsolatedContext,
			AgentID:  "reviewer",
			Criteria: "Decide.",
			Title:    "Decision",
		},
	}, map[string]any{"private": "claim-injection-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	waiting, err := executor.Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-claim-injection",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
		t.Fatalf("Run() = (%#v, %v), want waiting", waiting, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, waiting.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, err)
	}
	executor.AdmittedHumanTaskClaim = func(
		_ context.Context,
		_, _ string,
		claim func() (*Run, WorkflowHumanTask, bool, error),
	) (*Run, WorkflowHumanTask, bool, error) {
		run, task, duplicate, claimErr := claim()
		if run != nil {
			cycle := map[string]any{}
			cycle["self"] = cycle
			run.Inputs = cycle
		}
		return run, task, duplicate, claimErr
	}
	result, resumeErr := executor.ResumeHumanTask(ctx, waiting.RunID, tasks[0].ID, HumanTaskResumeRequest{
		ExpectedRevision: tasks[0].Revision,
		InputHash:        tasks[0].InputHash,
		ResponseID:       "response-1",
		Response:         "approved",
	})
	if result != nil || resumeErr != ErrPrivateWorkflowContext {
		t.Fatalf("ResumeHumanTask() = (%#v, %v), want private-context rejection", result, resumeErr)
	}
	if len(agents.requests) != 0 {
		t.Fatalf("agent continuation ran after injected claim: %#v", agents.requests)
	}
}

func TestPrivateGateResumeRechecksSecretsAtAuthoritativeClaim(t *testing.T) {
	for _, fakeClaim := range []bool{false, true} {
		name := "file_store_claim"
		if fakeClaim {
			name = "custom_store_return"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			base := NewFileRunStore(workspace)
			compilation, err := CompileGateWorkflow("Private secret claim", []GateSpec{{
				ID:        "approval",
				Kind:      GateDeterministic,
				When:      "true",
				Title:     "Approval",
				Questions: []any{"Proceed?"},
			}}, map[string]any{"private": "claim-secret-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			creator := &Executor{WorkspaceDir: workspace, Store: base}
			waiting, err := creator.Run(ctx, RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-secret-claim",
				PrivateRoot: compilation.PrivateRoot,
			})
			if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
				t.Fatalf("Run() = (%#v, %v), want waiting", waiting, err)
			}
			tasks, err := base.ListHumanTasks(ctx, waiting.RunID)
			if err != nil || len(tasks) != 1 {
				t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, err)
			}
			store := &privateGateSecretClassificationStore{
				FileRunStore: base,
				fakeClaim:    fakeClaim,
			}
			result, resumeErr := (&Executor{WorkspaceDir: workspace, Store: store}).ResumeHumanTask(
				ctx,
				waiting.RunID,
				tasks[0].ID,
				HumanTaskResumeRequest{
					ExpectedRevision: tasks[0].Revision,
					InputHash:        tasks[0].InputHash,
					ResponseID:       "response-1",
					Response:         "approved",
					Secrets:          map[string]string{"new": "private-new-secret-canary"},
				},
			)
			if result != nil || resumeErr != ErrPrivateWorkflowContext {
				t.Fatalf("ResumeHumanTask() = (%#v, %v), want private-context rejection", result, resumeErr)
			}
			after, err := base.ListHumanTasks(ctx, waiting.RunID)
			if err != nil || len(after) != 1 || after[0].Status != HumanTaskStatusWaiting ||
				after[0].Revision != tasks[0].Revision {
				t.Fatalf("rejected private claim changed task: (%#v, %v)", after, err)
			}
		})
	}
}

func TestPrivateGateDuplicateResumeRedactsBeforeCloningOutputs(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private duplicate projection", []GateSpec{{
		ID:        "approval",
		Kind:      GateDeterministic,
		When:      "true",
		Title:     "Approval",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "duplicate-output-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	waiting, err := executor.Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-duplicate-projection",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
		t.Fatalf("Run() = (%#v, %v), want waiting", waiting, err)
	}
	tasks, err := store.ListHumanTasks(ctx, waiting.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, err)
	}
	executor.AdmittedHumanTaskClaim = func(
		_ context.Context,
		_, _ string,
		_ func() (*Run, WorkflowHumanTask, bool, error),
	) (*Run, WorkflowHumanTask, bool, error) {
		run, getErr := store.GetRun(ctx, waiting.RunID)
		if getErr != nil {
			return nil, WorkflowHumanTask{}, false, getErr
		}
		cycle := map[string]any{"private": "duplicate-output-canary"}
		cycle["self"] = cycle
		run.Outputs = cycle
		return run, tasks[0], true, nil
	}
	result, resumeErr := executor.ResumeHumanTask(ctx, waiting.RunID, tasks[0].ID, HumanTaskResumeRequest{
		ExpectedRevision: tasks[0].Revision,
		InputHash:        tasks[0].InputHash,
		ResponseID:       "response-1",
		Response:         "approved",
	})
	if resumeErr != nil || result == nil || result.RunID != waiting.RunID ||
		result.Outputs != nil || result.Error != "" {
		t.Fatalf("ResumeHumanTask(duplicate) = (%#v, %v), want redacted result", result, resumeErr)
	}
}

func TestPrivateGateBrowserProjectionsRedactBeforeCloning(t *testing.T) {
	const privateCanary = "private-projection-cycle-canary-9d651b"
	cycle := map[string]any{"canary": privateCanary}
	cycle["self"] = cycle
	run := &Run{
		ID:                "wr_private_projection_cycle",
		WorkflowRef:       "inline/private-projection-cycle",
		Status:            RunStatusFailed,
		ContextVisibility: WorkflowContextVisibilityPrivate,
		Origin:            &RunOrigin{Kind: RunOriginExternalEvent, EventID: privateCanary},
		ParentRunID:       privateCanary,
		ChildRunIDs:       []string{privateCanary},
		CallerJobID:       privateCanary,
		Session:           privateCanary,
		Delivery:          Delivery{Channel: privateCanary},
		Event:             cycle,
		Inputs:            cycle,
		Outputs:           cycle,
		Jobs: map[string]JobExecution{
			"main": {ID: "main", Status: RunStatusFailed, Outputs: cycle, Error: privateCanary},
		},
		Steps: map[string]StepExecution{
			"step": {ID: "step", Status: RunStatusFailed, Outputs: cycle, Error: privateCanary},
		},
		Error:        privateCanary,
		CancelReason: privateCanary,
		execution:    &workflowExecutionState{},
		humanTasks: map[string]WorkflowHumanTask{
			"task": {ID: "task", Title: privateCanary},
		},
		privateRoot: &frozenWorkflowRootContext{Values: cycle, Revision: privateCanary},
	}
	projections := map[string]*Run{
		"direct": ProjectWorkflowRunForBrowser(run, false),
		"store": ProjectWorkflowRunForBrowserWithStore(
			context.Background(),
			nil,
			run,
			false,
		),
	}
	directJSON, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("json.Marshal(private Run) error = %v", err)
	}
	if bytes.Contains(directJSON, []byte(privateCanary)) {
		t.Fatalf("default private Run JSON contains canary: %s", directJSON)
	}
	listed := ProjectEventBackedDraftRunsForBrowser([]Run{*run})
	if len(listed) != 1 {
		t.Fatalf("listed projections = %#v", listed)
	}
	projections["list"] = &listed[0]
	for label, projected := range projections {
		if projected == nil || projected.execution != nil || projected.humanTasks != nil ||
			projected.privateRoot != nil || projected.Inputs != nil || projected.Event != nil ||
			projected.Outputs != nil || projected.Origin != nil || projected.Session != "" ||
			projected.ParentRunID != "" || projected.CallerJobID != "" ||
			len(projected.ChildRunIDs) != 0 || projected.Jobs["main"].Outputs != nil ||
			projected.Steps["step"].Outputs != nil {
			t.Fatalf("%s private projection retained context: %#v", label, projected)
		}
		encoded, encodeErr := json.Marshal(projected)
		if encodeErr != nil {
			t.Fatalf("json.Marshal(%s projection) error = %v", label, encodeErr)
		}
		if bytes.Contains(encoded, []byte(privateCanary)) {
			t.Fatalf("%s projection contains private canary: %s", label, encoded)
		}
	}

	events := ProjectWorkflowRunEventsForBrowser([]RunEvent{{
		Kind:    "workflow.private",
		Message: privateCanary,
		Payload: cycle,
	}}, false, true)
	encodedEvents, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("json.Marshal(private events) error = %v", err)
	}
	if bytes.Contains(encodedEvents, []byte(privateCanary)) {
		t.Fatalf("private event projection contains canary: %s", encodedEvents)
	}
}

func TestPrivateWorkingGateCapturesBeforeCreateAndRetryReusesSnapshot(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	const (
		subjectCanary       = "private-working-subject-canary-338aa1"
		liveReferenceCanary = "private-live-session-alias-canary-f4392a"
		canonicalCanary     = "agent:main:web:private-canonical-session-canary-e89038"
		newLiveCanary       = "agent:main:web:changed-live-session-canary-72f9b1"
		summaryCanary       = "private-frozen-summary-canary-1c543d"
		outputCanary        = "private-agent-output-canary-d15ab9"
	)

	order := []string{}
	agents := &privateGateSecurityAgentRunner{
		order:           &order,
		captureKey:      canonicalCanary,
		captureSummary:  summaryCanary,
		historyRevision: "history-revision-frozen-1",
		outputs: map[string]any{
			"structured": map[string]any{
				"ask_user":  false,
				"reason":    "",
				"questions": []any{},
			},
			"private_echo": outputCanary,
			"cache_key":    liveReferenceCanary,
		},
	}
	baseStore := NewFileRunStore(workspace)
	store := &privateGateSecurityOrderingStore{
		RunStore: baseStore,
		order:    &order,
		agents:   agents,
	}

	compilation, err := CompileGateWorkflow("Private working gate", []GateSpec{{
		ID:       "discussion",
		Kind:     GateAIWorkingContext,
		AgentID:  "main",
		Criteria: "Ask only for an unresolved product choice.",
		Title:    "PR discussion",
	}}, map[string]any{"finding": subjectCanary})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	compilation.PrivateRoot.ReadOnlySession = &ReadOnlySessionRef{
		AgentID: "main",
		Session: liveReferenceCanary,
	}

	executor := &Executor{WorkspaceDir: workspace, Store: store, Agents: agents}
	first, err := executor.Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-working-gate",
		PrivateRoot: compilation.PrivateRoot,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if first == nil || first.Status != RunStatusSucceeded {
		t.Fatalf("Run() result = %#v, want succeeded", first)
	}
	if got, want := order, []string{"capture", "create", "agent"}; !equalPrivateGateTestStrings(got, want) {
		t.Fatalf("initial operation order = %#v, want %#v", got, want)
	}
	if agents.captureCount != 1 {
		t.Fatalf("initial capture count = %d, want 1", agents.captureCount)
	}
	if len(agents.captureRefs) != 1 || agents.captureRefs[0].Session != liveReferenceCanary {
		t.Fatalf("capture refs = %#v", agents.captureRefs)
	}
	assertPrivateGateTestOmits(
		t,
		"initial run result",
		first,
		subjectCanary,
		liveReferenceCanary,
		canonicalCanary,
		summaryCanary,
		outputCanary,
	)
	assertPrivateGateTestAgentRequests(t, agents.requests, canonicalCanary, summaryCanary)

	firstRaw, err := os.ReadFile(filepath.Join(
		workspace,
		"workflow_runs",
		first.RunID,
		"run.json",
	))
	if err != nil {
		t.Fatalf("ReadFile(first run.json) error = %v", err)
	}
	if bytes.Contains(firstRaw, []byte(liveReferenceCanary)) {
		t.Fatalf("unresolved live session reference persisted: %s", firstRaw)
	}
	if !bytes.Contains(firstRaw, []byte(canonicalCanary)) ||
		!bytes.Contains(firstRaw, []byte(summaryCanary)) {
		t.Fatal("frozen canonical snapshot is missing from local private context")
	}

	agents.captureKey = newLiveCanary
	agents.captureSummary = "changed-live-summary-must-not-be-read"
	retry, err := executor.Retry(ctx, first.RunID, nil)
	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if retry == nil || retry.Status != RunStatusSucceeded {
		t.Fatalf("Retry() result = %#v, want succeeded", retry)
	}
	if agents.captureCount != 1 {
		t.Fatalf("capture count after retry = %d, want 1", agents.captureCount)
	}
	if got, want := order, []string{
		"capture", "create", "agent", "create", "agent",
	}; !equalPrivateGateTestStrings(got, want) {
		t.Fatalf("operation order after retry = %#v, want %#v", got, want)
	}
	assertPrivateGateTestAgentRequests(t, agents.requests, canonicalCanary, summaryCanary)
	assertPrivateGateTestOmits(
		t,
		"retry result",
		retry,
		subjectCanary,
		liveReferenceCanary,
		canonicalCanary,
		newLiveCanary,
		summaryCanary,
		outputCanary,
	)

	for _, runID := range []string{first.RunID, retry.RunID} {
		persisted, getErr := store.GetRun(ctx, runID)
		if getErr != nil {
			t.Fatalf("GetRun(%q) error = %v", runID, getErr)
		}
		if persisted.privateRoot == nil || persisted.privateRoot.ReadOnlySession == nil {
			t.Fatalf("GetRun(%q) frozen session = nil", runID)
		}
		frozen := persisted.privateRoot.ReadOnlySession
		if frozen.Snapshot.Key != canonicalCanary || frozen.Snapshot.Summary != summaryCanary {
			t.Fatalf("GetRun(%q) frozen snapshot = %#v", runID, frozen.Snapshot)
		}
		decision := persisted.Steps["gates/gate_discussion_decision"]
		if _, exists := decision.Outputs["cache_key"]; exists {
			t.Fatalf("GetRun(%q) retained agent cache key: %#v", runID, decision.Outputs)
		}
		if decision.Outputs["session"] != AgentSessionPrivate ||
			decision.Outputs["session_mode"] != AgentSessionPrivate {
			t.Fatalf("GetRun(%q) public session markers = %#v", runID, decision.Outputs)
		}
		assertPrivateGateTestOmits(
			t,
			"browser projection for "+runID,
			ProjectWorkflowRunForBrowser(persisted, false),
			subjectCanary,
			liveReferenceCanary,
			canonicalCanary,
			newLiveCanary,
			summaryCanary,
			outputCanary,
		)
		events, eventsErr := store.Events(ctx, runID)
		if eventsErr != nil {
			t.Fatalf("Events(%q) error = %v", runID, eventsErr)
		}
		assertPrivateGateTestEventsRedacted(t, events)
		assertPrivateGateTestOmits(
			t,
			"events for "+runID,
			events,
			subjectCanary,
			liveReferenceCanary,
			canonicalCanary,
			newLiveCanary,
			summaryCanary,
			outputCanary,
		)
	}

	const malformedFieldCanary = "private-malformed-field-canary-9c38e1"
	firstRunPath := filepath.Join(workspace, "workflow_runs", first.RunID, "run.json")
	malformedRaw, readErr := os.ReadFile(firstRunPath)
	if readErr != nil {
		t.Fatalf("ReadFile(malformed source) error = %v", readErr)
	}
	needle := []byte(`"read_only_session": {`)
	if !bytes.Contains(malformedRaw, needle) {
		t.Fatalf("run.json has no frozen session object: %s", malformedRaw)
	}
	malformedRaw = bytes.Replace(
		malformedRaw,
		needle,
		[]byte(`"read_only_session": {"`+malformedFieldCanary+`": true,`),
		1,
	)
	if writeErr := os.WriteFile(firstRunPath, malformedRaw, 0o600); writeErr != nil {
		t.Fatalf("WriteFile(malformed run.json) error = %v", writeErr)
	}
	if _, getErr := store.GetRun(ctx, first.RunID); getErr != ErrPrivateWorkflowContext {
		t.Fatalf(
			"GetRun(malformed private record) error = %v, want canonical %v",
			getErr,
			ErrPrivateWorkflowContext,
		)
	} else {
		assertPrivateGateTestOmits(t, "malformed private error", getErr, malformedFieldCanary)
	}
}

func TestPrivateGateFailureReturnsFixedPublicOutcome(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	const (
		subjectCanary = "private-error-subject-canary-a18b68"
		outputCanary  = "private-error-output-canary-621bc5"
		errorCanary   = "private-runner-error-canary-5bd381"
	)
	agents := &privateGateSecurityAgentRunner{
		outputs: map[string]any{"private_output": outputCanary},
		err:     fmt.Errorf("model provider exposed %s", errorCanary),
	}
	runtimeEvents := &fakeRuntimeEventPublisher{}
	store := NewFileRunStore(workspace)
	compilation, err := CompileGateWorkflow("Private failing gate", []GateSpec{{
		ID:       "finding",
		Kind:     GateAIIsolatedContext,
		AgentID:  "reviewer",
		Criteria: "Escalate incomplete evidence.",
		Title:    "Finding review",
	}}, map[string]any{"finding": subjectCanary})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}

	result, runErr := (&Executor{
		WorkspaceDir:  workspace,
		Store:         store,
		Agents:        agents,
		RuntimeEvents: runtimeEvents,
	}).Run(ctx, RunRequest{
		Workflow:    compilation.Workflow,
		WorkflowRef: "inline/private-failing-gate",
		PrivateRoot: compilation.PrivateRoot,
	})
	if !errors.Is(runErr, ErrPrivateWorkflowFailed) ||
		runErr == nil || runErr.Error() != ErrPrivateWorkflowFailed.Error() {
		t.Fatalf("Run() error = %v, want fixed %v", runErr, ErrPrivateWorkflowFailed)
	}
	if result == nil || result.Status != RunStatusFailed ||
		result.Error != "" || result.Outputs != nil {
		t.Fatalf("Run() public failure result = %#v", result)
	}
	assertPrivateGateTestOmits(t, "public failure error", runErr.Error(), errorCanary)
	assertPrivateGateTestOmits(
		t,
		"public failure result",
		result,
		subjectCanary,
		outputCanary,
		errorCanary,
	)

	persisted, err := store.GetRun(ctx, result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	assertPrivateGateTestOmits(
		t,
		"failed browser run",
		ProjectWorkflowRunForBrowser(persisted, false),
		subjectCanary,
		outputCanary,
		errorCanary,
	)
	events, err := store.Events(ctx, result.RunID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	assertPrivateGateTestEventsRedacted(t, events)
	assertPrivateGateTestOmits(
		t,
		"failed run events",
		events,
		subjectCanary,
		outputCanary,
		errorCanary,
	)
	if len(runtimeEvents.events) == 0 {
		t.Fatal("private run published no redacted runtime lifecycle events")
	}
	assertPrivateGateTestOmits(
		t,
		"runtime lifecycle events",
		runtimeEvents.events,
		subjectCanary,
		outputCanary,
		errorCanary,
	)
}

func TestPrivateGatePreservesCanonicalAdmissionErrors(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
	}{
		{name: "conflict", sentinel: ErrRunAdmissionConflict},
		{name: "unavailable", sentinel: ErrRunAdmissionUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const errorCanary = "private-admission-error-canary-4a7b90"
			compilation, err := CompileGateWorkflow("Private admission error", []GateSpec{{
				ID:        "policy",
				Kind:      GateDeterministic,
				When:      "false",
				Title:     "Policy",
				Questions: []any{"Approve?"},
			}}, map[string]any{"private": "admission-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			executor := &Executor{
				WorkspaceDir: t.TempDir(),
				AdmittedRunCreate: func(context.Context, *Run, func() error) error {
					return fmt.Errorf("%s: %w", errorCanary, test.sentinel)
				},
			}
			result, runErr := executor.Run(context.Background(), RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-admission-error",
				PrivateRoot: compilation.PrivateRoot,
			})
			if result != nil || runErr != test.sentinel {
				t.Fatalf(
					"Run() = (%#v, %v), want canonical %v",
					result,
					runErr,
					test.sentinel,
				)
			}
			assertPrivateGateTestOmits(t, "admission error", runErr, errorCanary)
		})
	}
}

func TestPrivateGateCancellationEventsRedactReasons(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}
	compilation, err := CompileGateWorkflow("Private waiting gate", []GateSpec{{
		ID:        "approval",
		Kind:      GateDeterministic,
		When:      "true",
		Title:     "Approval",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "cancellation-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow() error = %v", err)
	}
	runWaiting := func(t *testing.T) (*RunResult, WorkflowHumanTask) {
		t.Helper()
		result, runErr := executor.Run(ctx, RunRequest{
			Workflow:    compilation.Workflow,
			WorkflowRef: "inline/private-waiting-gate",
			PrivateRoot: compilation.PrivateRoot,
		})
		if runErr != nil || result == nil || result.Status != RunStatusWaiting {
			t.Fatalf("Run() = (%#v, %v), want waiting", result, runErr)
		}
		tasks, taskErr := executor.ListHumanTasks(ctx, result.RunID)
		if taskErr != nil || len(tasks) != 1 {
			t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, taskErr)
		}
		return result, tasks[0]
	}
	assertCanceledEvents := func(t *testing.T, runID string, canary string) {
		t.Helper()
		events, eventsErr := store.Events(ctx, runID)
		if eventsErr != nil {
			t.Fatalf("Events() error = %v", eventsErr)
		}
		assertPrivateGateTestEventsRedacted(t, events)
		assertPrivateGateTestOmits(t, "private cancellation events", events, canary)
	}
	assertCanceledResultRedacted := func(t *testing.T, run *Run, canary string) {
		t.Helper()
		if run == nil || run.CancelReason != "" || run.Error != "" ||
			run.Outputs != nil || run.Inputs != nil || run.Event != nil ||
			run.privateRoot != nil || run.execution != nil || run.humanTasks != nil {
			t.Fatalf("private cancellation returned raw run fields: %#v", run)
		}
		for key, job := range run.Jobs {
			if job.Outputs != nil || job.Error != "" {
				t.Fatalf("private cancellation job %q retained diagnostics: %#v", key, job)
			}
		}
		for key, step := range run.Steps {
			if step.Outputs != nil || step.Error != "" {
				t.Fatalf("private cancellation step %q retained diagnostics: %#v", key, step)
			}
		}
		assertPrivateGateTestOmits(t, "private cancellation direct result", run, canary)
	}

	t.Run("run cancellation", func(t *testing.T) {
		const reasonCanary = "private-run-cancel-reason-canary-638d62"
		result, _ := runWaiting(t)
		canceled, cancelErr := executor.CancelRun(ctx, result.RunID, reasonCanary)
		if cancelErr != nil || canceled == nil || canceled.Status != RunStatusCanceled {
			t.Fatalf("CancelRun() = (%#v, %v), want canceled", canceled, cancelErr)
		}
		assertCanceledResultRedacted(t, canceled, reasonCanary)
		assertCanceledEvents(t, result.RunID, reasonCanary)
	})

	t.Run("human task cancellation", func(t *testing.T) {
		const reasonCanary = "private-task-cancel-reason-canary-b37cf1"
		result, task := runWaiting(t)
		canceled, cancelErr := executor.CancelHumanTask(
			ctx,
			result.RunID,
			task.ID,
			reasonCanary,
		)
		if cancelErr != nil || canceled == nil || canceled.Status != RunStatusCanceled {
			t.Fatalf(
				"CancelHumanTask() = (%#v, %v), want canceled",
				canceled,
				cancelErr,
			)
		}
		assertCanceledResultRedacted(t, canceled, reasonCanary)
		assertCanceledEvents(t, result.RunID, reasonCanary)
	})
}

func TestPrivateGateCanonicalizesWrappedCancellationErrors(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
	}{
		{name: "canceled", sentinel: context.Canceled},
		{name: "deadline", sentinel: context.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const errorCanary = "private-wrapped-error-canary-e039a7"
			workspace := t.TempDir()
			compilation, err := CompileGateWorkflow("Private error gate", []GateSpec{{
				ID:       "decision",
				Kind:     GateAIIsolatedContext,
				AgentID:  "reviewer",
				Criteria: "Decide.",
				Title:    "Decision",
			}}, map[string]any{"private": "error-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			agents := &privateGateSecurityAgentRunner{
				err: fmt.Errorf("%s: %w", errorCanary, test.sentinel),
			}
			result, runErr := (&Executor{
				WorkspaceDir: workspace,
				Agents:       agents,
			}).Run(context.Background(), RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-error-gate",
				PrivateRoot: compilation.PrivateRoot,
			})
			if runErr != test.sentinel {
				t.Fatalf("Run() error = %v, want canonical %v", runErr, test.sentinel)
			}
			assertPrivateGateTestOmits(t, "canonical run error", runErr, errorCanary)
			if result == nil || result.Status != RunStatusFailed ||
				result.Error != "" || result.Outputs != nil {
				t.Fatalf("Run() result = %#v, want redacted failure", result)
			}
		})
	}
}

func TestPrivateGateResumeCanonicalizesWrappedCancellationErrors(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
	}{
		{name: "canceled", sentinel: context.Canceled},
		{name: "deadline", sentinel: context.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const errorCanary = "private-resume-wrapped-error-canary-e67d05"
			workspace := t.TempDir()
			store := NewFileRunStore(workspace)
			agents := &privateGateSecurityAgentRunner{
				err: fmt.Errorf("%s: %w", errorCanary, test.sentinel),
			}
			executor := &Executor{WorkspaceDir: workspace, Store: store, Agents: agents}
			compilation, err := CompileGateWorkflow("Private resume error gate", []GateSpec{
				{
					ID:        "approval",
					Kind:      GateDeterministic,
					When:      "true",
					Title:     "Approval",
					Questions: []any{"Proceed?"},
				},
				{
					ID:       "decision",
					Kind:     GateAIIsolatedContext,
					AgentID:  "reviewer",
					Criteria: "Decide.",
					Title:    "Decision",
				},
			}, map[string]any{"private": "resume-error-subject-canary"})
			if err != nil {
				t.Fatalf("CompileGateWorkflow() error = %v", err)
			}
			waiting, runErr := executor.Run(context.Background(), RunRequest{
				Workflow:    compilation.Workflow,
				WorkflowRef: "inline/private-resume-error-gate",
				PrivateRoot: compilation.PrivateRoot,
			})
			if runErr != nil || waiting == nil || waiting.Status != RunStatusWaiting {
				t.Fatalf("Run() = (%#v, %v), want waiting", waiting, runErr)
			}
			tasks, taskErr := executor.ListHumanTasks(context.Background(), waiting.RunID)
			if taskErr != nil || len(tasks) != 1 {
				t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, taskErr)
			}
			result, resumeErr := executor.ResumeHumanTask(
				context.Background(),
				waiting.RunID,
				tasks[0].ID,
				HumanTaskResumeRequest{
					ExpectedRevision: tasks[0].Revision,
					InputHash:        tasks[0].InputHash,
					ResponseID:       "response-1",
					Response:         "approved",
				},
			)
			if resumeErr != test.sentinel {
				t.Fatalf(
					"ResumeHumanTask() error = %v, want canonical %v",
					resumeErr,
					test.sentinel,
				)
			}
			assertPrivateGateTestOmits(t, "canonical resume error", resumeErr, errorCanary)
			if result == nil || result.Status != RunStatusFailed ||
				result.Error != "" || result.Outputs != nil {
				t.Fatalf("ResumeHumanTask() result = %#v, want redacted failure", result)
			}
		})
	}
}

func TestPrivateGateRetryAndResumeRejectNewSecrets(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	executor := &Executor{WorkspaceDir: workspace, Store: store}

	completedCompilation, err := CompileGateWorkflow("Private completed gate", []GateSpec{{
		ID:        "policy",
		Kind:      GateDeterministic,
		When:      "false",
		Title:     "Policy",
		Questions: []any{"Approve?"},
	}}, map[string]any{"private": "retry-secret-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow(completed) error = %v", err)
	}
	completed, err := executor.Run(ctx, RunRequest{
		Workflow:    completedCompilation.Workflow,
		WorkflowRef: "inline/private-completed-gate",
		PrivateRoot: completedCompilation.PrivateRoot,
	})
	if err != nil || completed == nil || completed.Status != RunStatusSucceeded {
		t.Fatalf("Run(completed) = (%#v, %v), want succeeded", completed, err)
	}
	if result, retryErr := executor.Retry(
		ctx,
		completed.RunID,
		map[string]string{"new": "retry-secret-canary"},
	); result != nil || retryErr != ErrPrivateWorkflowContext {
		t.Fatalf(
			"Retry(private secrets) = (%#v, %v), want canonical rejection",
			result,
			retryErr,
		)
	}

	waitingCompilation, err := CompileGateWorkflow("Private waiting gate", []GateSpec{{
		ID:        "approval",
		Kind:      GateDeterministic,
		When:      "true",
		Title:     "Approval",
		Questions: []any{"Proceed?"},
	}}, map[string]any{"private": "resume-secret-subject-canary"})
	if err != nil {
		t.Fatalf("CompileGateWorkflow(waiting) error = %v", err)
	}
	waiting, err := executor.Run(ctx, RunRequest{
		Workflow:    waitingCompilation.Workflow,
		WorkflowRef: "inline/private-waiting-secret-gate",
		PrivateRoot: waitingCompilation.PrivateRoot,
	})
	if err != nil || waiting == nil || waiting.Status != RunStatusWaiting {
		t.Fatalf("Run(waiting) = (%#v, %v), want waiting", waiting, err)
	}
	tasks, err := executor.ListHumanTasks(ctx, waiting.RunID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v), want one", tasks, err)
	}
	result, resumeErr := executor.ResumeHumanTask(
		ctx,
		waiting.RunID,
		tasks[0].ID,
		HumanTaskResumeRequest{
			ExpectedRevision: tasks[0].Revision,
			InputHash:        tasks[0].InputHash,
			ResponseID:       "response-with-secret",
			Response:         "approved",
			Secrets:          map[string]string{"new": "resume-secret-canary"},
		},
	)
	if result != nil || resumeErr != ErrPrivateWorkflowContext {
		t.Fatalf(
			"ResumeHumanTask(private secrets) = (%#v, %v), want canonical rejection",
			result,
			resumeErr,
		)
	}
	remaining, err := executor.ListHumanTasks(ctx, waiting.RunID)
	if err != nil || len(remaining) != 1 || remaining[0].Status != HumanTaskStatusWaiting {
		t.Fatalf("tasks after rejected resume = (%#v, %v), want unchanged waiting", remaining, err)
	}
}

type privateGateSecurityAgentRunner struct {
	order           *[]string
	captureKey      string
	captureSummary  string
	historyRevision string
	captureCount    int
	captureRefs     []ReadOnlySessionRef
	requests        []AgentRequest
	outputs         map[string]any
	err             error
}

type privateGateStatefulJSONMarshaler struct {
	calls int
}

type privateGateSecretClassificationStore struct {
	*FileRunStore
	fakeClaim bool
}

func (s *privateGateSecretClassificationStore) GetRun(
	ctx context.Context,
	runID string,
) (*Run, error) {
	run, err := s.FileRunStore.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	// Simulate a stale/non-authoritative pre-read that classifies the run as
	// ordinary. The claim result remains the authoritative boundary.
	run.privateRoot = nil
	run.ContextVisibility = ""
	return run, nil
}

func (s *privateGateSecretClassificationStore) ClaimHumanTask(
	ctx context.Context,
	runID string,
	taskID string,
	req HumanTaskResumeRequest,
) (*Run, WorkflowHumanTask, bool, error) {
	if !s.fakeClaim {
		return s.FileRunStore.ClaimHumanTask(ctx, runID, taskID, req)
	}
	run, err := s.FileRunStore.GetRun(ctx, runID)
	if err != nil {
		return nil, WorkflowHumanTask{}, false, err
	}
	task, ok := run.humanTasks[taskID]
	if !ok {
		return nil, WorkflowHumanTask{}, false, ErrHumanTaskNotFound
	}
	return run, task, false, nil
}

func (m *privateGateStatefulJSONMarshaler) MarshalJSON() ([]byte, error) {
	m.calls++
	return []byte(`["unexpected"]`), nil
}

func (r *privateGateSecurityAgentRunner) CaptureReadOnlySession(
	_ context.Context,
	ref ReadOnlySessionRef,
) (*FrozenReadOnlySession, error) {
	r.captureCount++
	r.captureRefs = append(r.captureRefs, ref)
	if r.order != nil {
		*r.order = append(*r.order, "capture")
	}
	return &FrozenReadOnlySession{
		AgentID: ref.AgentID,
		Snapshot: session.SessionSnapshot{
			Key:     r.captureKey,
			Summary: r.captureSummary,
		},
		HistoryRevision: r.historyRevision,
		FrozenMedia:     media.FrozenSet{Version: media.FrozenSetVersion},
	}, nil
}

func (r *privateGateSecurityAgentRunner) RunAgent(
	_ context.Context,
	req AgentRequest,
) (map[string]any, error) {
	if r.order != nil {
		*r.order = append(*r.order, "agent")
	}
	cloned := req
	cloned.Inputs = cloneMap(req.Inputs)
	cloned.FrozenReadOnlySession = cloneFrozenReadOnlySession(req.FrozenReadOnlySession)
	r.requests = append(r.requests, cloned)
	return cloneMap(r.outputs), r.err
}

type privateGateSecurityOrderingStore struct {
	RunStore
	order  *[]string
	agents *privateGateSecurityAgentRunner
}

func (s *privateGateSecurityOrderingStore) CreateRun(ctx context.Context, run *Run) error {
	if s.agents == nil || s.agents.captureCount != 1 {
		return fmt.Errorf("durable create observed %d captures, want 1", s.agents.captureCount)
	}
	if s.order != nil {
		*s.order = append(*s.order, "create")
	}
	return s.RunStore.CreateRun(ctx, run)
}

func assertPrivateGateTestAgentRequests(
	t *testing.T,
	requests []AgentRequest,
	wantKey string,
	wantSummary string,
) {
	t.Helper()
	if len(requests) == 0 {
		t.Fatal("agent requests = empty")
	}
	for index, req := range requests {
		if !req.PrivateContext {
			t.Fatalf("agent request %d private context = false", index)
		}
		if req.Session != "" {
			t.Fatalf("agent request %d live session = %q, want empty", index, req.Session)
		}
		if req.FrozenReadOnlySession == nil {
			t.Fatalf("agent request %d frozen session = nil", index)
		}
		if req.FrozenReadOnlySession.Snapshot.Key != wantKey ||
			req.FrozenReadOnlySession.Snapshot.Summary != wantSummary {
			t.Fatalf(
				"agent request %d frozen snapshot = %#v",
				index,
				req.FrozenReadOnlySession.Snapshot,
			)
		}
	}
}

func assertPrivateGateTestEventsRedacted(t *testing.T, events []RunEvent) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("private run events = empty")
	}
	for index, event := range events {
		if event.Message != "" || event.Payload != nil {
			t.Fatalf("private event %d is not redacted: %#v", index, event)
		}
	}
}

func assertPrivateGateTestContains(t *testing.T, label string, value any, canary string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%s) error = %v", label, err)
	}
	if !bytes.Contains(encoded, []byte(canary)) {
		t.Fatalf("%s does not contain %q: %s", label, canary, encoded)
	}
}

func assertPrivateGateTestOmits(t *testing.T, label string, value any, canaries ...string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%s) error = %v", label, err)
	}
	for _, canary := range canaries {
		if strings.Contains(string(encoded), canary) {
			t.Fatalf("%s contains private canary %q: %s", label, canary, encoded)
		}
	}
}

func equalPrivateGateTestStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
