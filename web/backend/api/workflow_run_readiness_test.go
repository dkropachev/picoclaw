package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowRunReadinessDefinition = `name: Readiness run
on:
  manual: {}
jobs:
  inspect:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with:
          action: list
`

func TestHandleRunWorkflowFreshDependencyAdmission(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	definitionPath := writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/run.yml",
		workflowRunReadinessDefinition,
	)
	handler := NewHandler(configPath)
	restore := stubWorkflowDependencyRuntime(t, nil)
	defer restore()
	revalidateWorkflowRunReadinessDefinition(t, handler, configPath)

	shown := checkPublishedWorkflowDependencies(t, handler, "workflows/run.yml")
	if !shown.Ready {
		t.Fatalf("shown dependency response = %#v, want ready", shown)
	}

	if err := os.WriteFile(
		definitionPath,
		[]byte(strings.Replace(
			workflowRunReadinessDefinition,
			"name: Readiness run",
			"name: Changed readiness run",
			1,
		)),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(changed definition) error = %v", err)
	}
	stale := postWorkflowRun(
		t,
		handler,
		map[string]any{
			"ref":                          "workflows/run.yml",
			"expected_dependency_revision": shown.Revision,
		},
	)
	assertWorkflowAdmissionError(
		t,
		stale,
		http.StatusConflict,
		"dependency_revision_mismatch",
	)
	assertWorkflowRunCount(t, workspace, 0)

	revalidateWorkflowRunReadinessDefinition(t, handler, configPath)
	current := checkPublishedWorkflowDependencies(t, handler, "workflows/run.yml")
	admitted := postWorkflowRun(
		t,
		handler,
		map[string]any{
			"ref":                          "workflows/run.yml",
			"expected_dependency_revision": current.Revision,
		},
	)
	if admitted.Code != http.StatusOK {
		t.Fatalf("admitted status = %d, body=%s", admitted.Code, admitted.Body.String())
	}
	var result workflows.RunResult
	if err := json.Unmarshal(admitted.Body.Bytes(), &result); err != nil {
		t.Fatalf("admitted response JSON error = %v", err)
	}
	if result.RunID == "" || result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("admitted result = %#v", result)
	}

	withoutStamp := postWorkflowRun(
		t,
		handler,
		map[string]any{"ref": "workflows/run.yml"},
	)
	if withoutStamp.Code != http.StatusOK {
		t.Fatalf(
			"optional-stamp status = %d, body=%s",
			withoutStamp.Code,
			withoutStamp.Body.String(),
		)
	}
	assertWorkflowRunCount(t, workspace, 2)
}

func TestHandleRunWorkflowRejectsCurrentUnreadyAndUnavailableDependencies(
	t *testing.T,
) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/blocked.yml",
		`name: Blocked
on:
  manual: {}
jobs:
  blocked:
    runs-on: picoclaw
    steps:
      - uses: tool/not_configured
`,
	)
	handler := NewHandler(configPath)

	restore := stubWorkflowDependencyRuntime(t, func(
		workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		return workflows.WorkflowDependencyReadinessNotConfigured
	})
	blocked := checkPublishedWorkflowDependencies(t, handler, "workflows/blocked.yml")
	if blocked.Ready {
		t.Fatalf("blocked dependency response = %#v", blocked)
	}
	staleAndBlocked := postWorkflowRun(
		t,
		handler,
		map[string]any{
			"ref":                          "workflows/blocked.yml",
			"expected_dependency_revision": "sha256:stale",
		},
	)
	assertWorkflowAdmissionError(
		t,
		staleAndBlocked,
		http.StatusConflict,
		"dependency_revision_mismatch",
	)
	notReady := postWorkflowRun(
		t,
		handler,
		map[string]any{
			"ref":                          "workflows/blocked.yml",
			"expected_dependency_revision": blocked.Revision,
		},
	)
	assertWorkflowAdmissionError(
		t,
		notReady,
		http.StatusConflict,
		"workflow_dependencies_not_ready",
	)
	restore()
	assertWorkflowRunCount(t, workspace, 0)

	previous := newWorkflowDependencyRuntime
	defer func() { newWorkflowDependencyRuntime = previous }()
	newWorkflowDependencyRuntime = func(string, *config.Config) workflowDependencyRuntime {
		return nil
	}
	unavailable := postWorkflowRun(
		t,
		handler,
		map[string]any{"ref": "workflows/blocked.yml"},
	)
	assertWorkflowAdmissionError(
		t,
		unavailable,
		http.StatusServiceUnavailable,
		"dependency_check_unavailable",
	)
	assertWorkflowRunCount(t, workspace, 0)
}

func TestHandleRunAndRetryFreshGateDisabledWorkflows(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, false)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/disabled.yml",
		workflowRunReadinessDefinition,
	)
	handler := NewHandler(configPath)
	restore := stubWorkflowDependencyRuntime(t, nil)
	defer restore()
	current := checkPublishedWorkflowDependencies(
		t,
		handler,
		"workflows/disabled.yml",
	)
	if current.Ready || current.WorkflowEnabled {
		t.Fatalf("disabled dependency response = %#v", current)
	}

	now := time.Now().UTC()
	previous := &workflows.Run{
		ID:          "wr_disabled_previous",
		WorkflowRef: "workflows/disabled.yml",
		Status:      workflows.RunStatusFailed,
		CreatedAt:   now,
		UpdatedAt:   now,
		CompletedAt: &now,
	}
	if err := workflows.NewFileRunStore(workspace).CreateRun(ctx, previous); err != nil {
		t.Fatalf("CreateRun(previous) error = %v", err)
	}

	runNotReady := postWorkflowRun(
		t,
		handler,
		map[string]any{"ref": "workflows/disabled.yml"},
	)
	assertWorkflowAdmissionError(
		t,
		runNotReady,
		http.StatusConflict,
		"workflow_dependencies_not_ready",
	)
	runMismatch := postWorkflowRun(
		t,
		handler,
		map[string]any{
			"ref":                          "workflows/disabled.yml",
			"expected_dependency_revision": "sha256:stale",
		},
	)
	assertWorkflowAdmissionError(
		t,
		runMismatch,
		http.StatusConflict,
		"dependency_revision_mismatch",
	)

	retryNotReady := postWorkflowRetry(
		t,
		handler,
		previous.ID,
		map[string]any{},
	)
	assertWorkflowAdmissionError(
		t,
		retryNotReady,
		http.StatusConflict,
		"workflow_dependencies_not_ready",
	)
	retryMismatch := postWorkflowRetry(
		t,
		handler,
		previous.ID,
		map[string]any{"expected_dependency_revision": "sha256:stale"},
	)
	assertWorkflowAdmissionError(
		t,
		retryMismatch,
		http.StatusConflict,
		"dependency_revision_mismatch",
	)
	assertWorkflowRunCount(t, workspace, 1)
}

func TestHandleRetryWorkflowRunGatesPreviousWorkflowRef(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	for _, name := range []string{"previous", "selected"} {
		writeWorkflowDependencyDefinition(
			t,
			workspace,
			"automation",
			"workflows/"+name+".yml",
			strings.Replace(
				workflowRunReadinessDefinition,
				"name: Readiness run",
				"name: "+name,
				1,
			),
		)
	}
	handler := NewHandler(configPath)
	restore := stubWorkflowDependencyRuntime(t, nil)
	defer restore()
	revalidateWorkflowRunReadinessDefinition(t, handler, configPath)
	selected := checkPublishedWorkflowDependencies(
		t,
		handler,
		"workflows/selected.yml",
	)

	now := time.Now().UTC()
	previous := &workflows.Run{
		ID:          "wr_previous",
		WorkflowRef: "workflows/previous.yml",
		Status:      workflows.RunStatusFailed,
		CreatedAt:   now,
		UpdatedAt:   now,
		CompletedAt: &now,
	}
	if err := workflows.NewFileRunStore(workspace).CreateRun(ctx, previous); err != nil {
		t.Fatalf("CreateRun(previous) error = %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"expected_dependency_revision": selected.Revision,
	})
	if err != nil {
		t.Fatalf("json.Marshal(retry) error = %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/runs/"+previous.ID+"/retry",
		strings.NewReader(string(body)),
	)
	request.SetPathValue("run_id", previous.ID)
	handler.handleRetryWorkflowRun(recorder, request)
	assertWorkflowAdmissionError(
		t,
		recorder,
		http.StatusConflict,
		"dependency_revision_mismatch",
	)
	assertWorkflowRunCount(t, workspace, 1)
}

func TestWorkflowRunAndRetryRequestsRejectBrowserSuppliedOrigin(t *testing.T) {
	workspace := t.TempDir()
	handler := NewHandler(writeWorkflowDependencyTestConfig(t, workspace, true))
	origin := `{"kind":"external_event","event_id":"ev_0123456789abcdef0123456789abcdef","dispatch_id":"dsp_0123456789abcdef0123456789abcdef","root_run_id":"wr_forged"}`

	run := httptest.NewRecorder()
	handler.handleRunWorkflow(
		run,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/run",
			strings.NewReader(`{"ref":"workflows/missing.yml","origin":`+origin+`}`),
		),
	)
	if run.Code != http.StatusBadRequest ||
		!strings.Contains(run.Body.String(), `unknown field "origin"`) {
		t.Fatalf("forged run origin response = (%d, %q)", run.Code, run.Body.String())
	}

	retry := httptest.NewRecorder()
	retryRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/runs/wr_missing/retry",
		strings.NewReader(`{"origin":`+origin+`}`),
	)
	retryRequest.SetPathValue("run_id", "wr_missing")
	handler.handleRetryWorkflowRun(retry, retryRequest)
	if retry.Code != http.StatusBadRequest ||
		!strings.Contains(retry.Body.String(), `unknown field "origin"`) {
		t.Fatalf("forged retry origin response = (%d, %q)", retry.Code, retry.Body.String())
	}
}

func revalidateWorkflowRunReadinessDefinition(
	t *testing.T,
	handler *Handler,
	configPath string,
) {
	t.Helper()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if _, err := workflows.RevalidateLocal(
		context.Background(),
		cfg.WorkspacePath(),
		handler.workflowCompatibilityRuntime(context.Background()),
		workflowLocalOptionsFromConfig(cfg)...,
	); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}
}

func checkPublishedWorkflowDependencies(
	t *testing.T,
	handler *Handler,
	ref string,
) workflowDependencyCheckResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.handleCheckWorkflowDependencies(
		recorder,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/dependencies/check",
			strings.NewReader(`{"ref":"`+ref+`"}`),
		),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("dependency check status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response workflowDependencyCheckResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("dependency check JSON error = %v", err)
	}
	return response
}

func postWorkflowRun(
	t *testing.T,
	handler *Handler,
	body map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal(run) error = %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.handleRunWorkflow(
		recorder,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/run",
			strings.NewReader(string(encoded)),
		),
	)
	return recorder
}

func postWorkflowRetry(
	t *testing.T,
	handler *Handler,
	runID string,
	body map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal(retry) error = %v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/runs/"+runID+"/retry",
		strings.NewReader(string(encoded)),
	)
	request.SetPathValue("run_id", runID)
	handler.handleRetryWorkflowRun(recorder, request)
	return recorder
}

func assertWorkflowAdmissionError(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	status int,
	code string,
) {
	t.Helper()
	if recorder.Code != status ||
		!strings.Contains(recorder.Body.String(), `"`+code+`"`) {
		t.Fatalf(
			"admission response = (%d, %q), want %d %q",
			recorder.Code,
			recorder.Body.String(),
			status,
			code,
		)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
}

func assertWorkflowRunCount(t *testing.T, workspace string, want int) {
	t.Helper()
	runs, err := workflows.NewFileRunStore(workspace).ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != want {
		t.Fatalf("run count = %d, want %d: %#v", len(runs), want, runs)
	}
}
