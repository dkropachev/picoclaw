package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

const workflowConfigGuardRootDefinition = `name: Config guard root
on:
  manual: {}
jobs:
  one:
    uses: workflows/config-guard-one.yml
`

const workflowConfigGuardOneDefinition = `name: Config guard one
on:
  workflow_call: {}
jobs:
  two:
    uses: workflows/config-guard-two.yml
`

const workflowConfigGuardTwoDefinition = `name: Config guard two
on:
  workflow_call: {}
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

func TestHandleRunAndRetryFenceAdmissionAtDurableCreate(t *testing.T) {
	tests := []struct {
		name       string
		retry      bool
		mutate     func(string, string) error
		revalidate bool
		wantStatus int
		wantCode   string
		wantRuns   int
		previousID string
		parentID   string
	}{
		{
			name: "run rejects config mutation",
			mutate: func(configPath, _ string) error {
				cfg, err := config.LoadConfig(configPath)
				if err != nil {
					return err
				}
				cfg.Workflows.MaxCallDepth = cfg.Workflows.EffectiveMaxCallDepth() + 1
				return config.SaveConfig(configPath, cfg)
			},
		},
		{
			name: "run rejects workflow mutation",
			mutate: func(_ string, definitionPath string) error {
				return os.WriteFile(
					definitionPath,
					[]byte(strings.Replace(
						workflowRunReadinessDefinition,
						"name: Readiness run",
						"name: Concurrently published run",
						1,
					)),
					0o644,
				)
			},
		},
		{
			name: "run reports published workflow drift as revision mismatch",
			mutate: func(_ string, definitionPath string) error {
				return os.WriteFile(
					definitionPath,
					[]byte(strings.Replace(
						workflowRunReadinessDefinition,
						"name: Readiness run",
						"name: Published replacement",
						1,
					)),
					0o644,
				)
			},
			revalidate: true,
		},
		{
			name: "run reports compatibility manifest failure as unavailable",
			mutate: func(configPath, _ string) error {
				cfg, err := config.LoadConfig(configPath)
				if err != nil {
					return err
				}
				return os.WriteFile(
					filepath.Join(
						cfg.WorkspacePath(),
						"workflow_validations",
						"manifest.json",
					),
					[]byte("{"),
					0o600,
				)
			},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   "dependency_check_unavailable",
		},
		{
			name: "run reports missing compatibility stamp as not ready",
			mutate: func(configPath, _ string) error {
				cfg, err := config.LoadConfig(configPath)
				if err != nil {
					return err
				}
				return os.Remove(filepath.Join(
					cfg.WorkspacePath(),
					"workflow_validations",
					"manifest.json",
				))
			},
			wantStatus: http.StatusConflict,
			wantCode:   "workflow_dependencies_not_ready",
		},
		{
			name:  "retry rejects config mutation",
			retry: true,
			mutate: func(configPath, _ string) error {
				cfg, err := config.LoadConfig(configPath)
				if err != nil {
					return err
				}
				cfg.Workflows.MaxCallDepth = cfg.Workflows.EffectiveMaxCallDepth() + 1
				return config.SaveConfig(configPath, cfg)
			},
			wantRuns:   1,
			previousID: "wr_retry_config_race",
		},
		{
			name:  "retry rejects workflow mutation",
			retry: true,
			mutate: func(_ string, definitionPath string) error {
				return os.WriteFile(
					definitionPath,
					[]byte(strings.Replace(
						workflowRunReadinessDefinition,
						"name: Readiness run",
						"name: Concurrently published retry",
						1,
					)),
					0o644,
				)
			},
			wantRuns:   1,
			previousID: "wr_retry_workflow_race",
			parentID:   "wr_original_parent",
		},
		{
			name:  "retry reports published workflow drift as revision mismatch",
			retry: true,
			mutate: func(_ string, definitionPath string) error {
				return os.WriteFile(
					definitionPath,
					[]byte(strings.Replace(
						workflowRunReadinessDefinition,
						"name: Readiness run",
						"name: Published retry replacement",
						1,
					)),
					0o644,
				)
			},
			revalidate: true,
			wantRuns:   1,
			previousID: "wr_retry_published_workflow_race",
			parentID:   "wr_original_parent",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
			definitionPath := writeWorkflowDependencyDefinition(
				t,
				workspace,
				"automation",
				"workflows/race.yml",
				workflowRunReadinessDefinition,
			)
			handler := NewHandler(configPath)
			restoreDependencies := stubWorkflowDependencyRuntime(t, nil)
			t.Cleanup(restoreDependencies)
			revalidateWorkflowRunReadinessDefinition(t, handler, configPath)
			shown := checkPublishedWorkflowDependencies(
				t,
				handler,
				"workflows/race.yml",
			)
			if !shown.Ready {
				t.Fatalf("shown dependency response = %#v, want ready", shown)
			}

			if test.retry {
				now := time.Now().UTC()
				if err := workflows.NewFileRunStore(workspace).CreateRun(
					ctx,
					&workflows.Run{
						ID:          test.previousID,
						WorkflowRef: "workflows/race.yml",
						Status:      workflows.RunStatusFailed,
						ParentRunID: test.parentID,
						CreatedAt:   now,
						UpdatedAt:   now,
						CompletedAt: &now,
					},
				); err != nil {
					t.Fatalf("CreateRun(previous) error = %v", err)
				}
			}

			previousRunners := newWorkflowRuntimeRunners
			t.Cleanup(func() { newWorkflowRuntimeRunners = previousRunners })
			var mutateErr error
			var mutateOnce sync.Once
			newWorkflowRuntimeRunners = func(path string) workflowRuntimeRunners {
				mutateOnce.Do(func() {
					mutateErr = test.mutate(configPath, definitionPath)
					if mutateErr != nil || !test.revalidate {
						return
					}
					cfg, err := config.LoadConfig(configPath)
					if err != nil {
						mutateErr = err
						return
					}
					_, mutateErr = workflows.RevalidateLocal(
						ctx,
						cfg.WorkspacePath(),
						handler.workflowCompatibilityRuntime(ctx),
						workflowLocalOptionsFromConfig(cfg)...,
					)
				})
				return previousRunners(path)
			}

			var response *httptest.ResponseRecorder
			requestBody := map[string]any{
				"expected_dependency_revision": shown.Revision,
			}
			if test.retry {
				response = postWorkflowRetry(
					t,
					handler,
					test.previousID,
					requestBody,
				)
			} else {
				requestBody["ref"] = "workflows/race.yml"
				response = postWorkflowRun(t, handler, requestBody)
			}
			if mutateErr != nil {
				t.Fatalf("concurrent mutation error = %v", mutateErr)
			}
			wantStatus := test.wantStatus
			if wantStatus == 0 {
				wantStatus = http.StatusConflict
			}
			wantCode := test.wantCode
			if wantCode == "" {
				wantCode = "dependency_revision_mismatch"
			}
			assertWorkflowAdmissionError(
				t,
				response,
				wantStatus,
				wantCode,
			)
			assertWorkflowRunCount(t, workspace, test.wantRuns)
		})
	}
}

func TestWorkflowAdmissionConfigGuardBlocksCrossProcessSaveThroughCreateAndUsesCapturedConfig(
	t *testing.T,
) {
	if os.Getenv("PICOCLAW_WORKFLOW_CONFIG_GUARD_CHILD") == "1" {
		runWorkflowAdmissionConfigGuardSaveChild(t)
		return
	}

	ctx := context.Background()
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	initialConfig, err := config.LoadConfigForUpdate(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdate(initial) error = %v", err)
	}
	initialConfig.Workflows.MaxCallDepth = 2
	if saveErr := config.SaveConfig(configPath, initialConfig); saveErr != nil {
		t.Fatalf("SaveConfig(initial depth) error = %v", saveErr)
	}
	for ref, definition := range map[string]string{
		"workflows/config-guard-root.yml": workflowConfigGuardRootDefinition,
		"workflows/config-guard-one.yml":  workflowConfigGuardOneDefinition,
		"workflows/config-guard-two.yml":  workflowConfigGuardTwoDefinition,
	} {
		writeWorkflowDependencyDefinition(
			t,
			workspace,
			"automation",
			ref,
			definition,
		)
	}
	handler := NewHandler(configPath)
	restoreDependencies := stubWorkflowDependencyRuntime(t, nil)
	t.Cleanup(restoreDependencies)
	revalidateWorkflowRunReadinessDefinition(t, handler, configPath)
	admission, err := handler.requirePublishedWorkflowDependenciesReady(
		ctx,
		"workflows/config-guard-root.yml",
		"",
	)
	if err != nil {
		t.Fatalf("requirePublishedWorkflowDependenciesReady() error = %v", err)
	}
	capturedConfig, _, executor, err := handler.workflowRuntimeFromConfig(
		ctx,
		admission.Config,
	)
	if err != nil {
		t.Fatalf("workflowRuntimeFromConfig() error = %v", err)
	}
	defer closeWorkflowRuntime(executor)
	if capturedConfig.Workflows.EffectiveMaxCallDepth() != 2 ||
		executor.MaxCallDepth != 2 {
		t.Fatalf(
			"captured max call depth = config %d, executor %d, want 2",
			capturedConfig.Workflows.EffectiveMaxCallDepth(),
			executor.MaxCallDepth,
		)
	}
	executor.WorkflowSnapshots = admission.Snapshots

	readyPath := configPath + ".save-started"
	completedPath := configPath + ".save-completed"
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestWorkflowAdmissionConfigGuardBlocksCrossProcessSaveThroughCreateAndUsesCapturedConfig$",
	)
	command.Env = append(
		os.Environ(),
		"PICOCLAW_WORKFLOW_CONFIG_GUARD_CHILD=1",
		"PICOCLAW_WORKFLOW_CONFIG_GUARD_PATH="+configPath,
		"PICOCLAW_WORKFLOW_CONFIG_GUARD_WORKSPACE="+workspace,
		"PICOCLAW_WORKFLOW_CONFIG_GUARD_READY="+readyPath,
		"PICOCLAW_WORKFLOW_CONFIG_GUARD_COMPLETED="+completedPath,
	)
	helperStarted := false
	helperWaited := false
	t.Cleanup(func() {
		if helperStarted && !helperWaited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	startHelper := func() error {
		if commandErr := command.Start(); commandErr != nil {
			return commandErr
		}
		helperStarted = true
		waitForWorkflowAdmissionTestFile(t, readyPath)
		select {
		case <-time.After(50 * time.Millisecond):
			if _, statErr := os.Stat(completedPath); statErr == nil {
				t.Fatal("cross-process SaveConfig completed before durable create")
			} else if !os.IsNotExist(statErr) {
				t.Fatalf("Stat(save completion) error = %v", statErr)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}

	var startOnce sync.Once
	var startErr error
	executor.AdmittedRunCreate = func(
		runCtx context.Context,
		candidate *workflows.Run,
		create func() error,
	) error {
		return workflows.WithGuardedFencedRunnableWorkflowSnapshots(
			runCtx,
			capturedConfig.WorkspacePath(),
			admission.orderedSnapshots(),
			executor.RuntimeCompatibility,
			func() error {
				if candidate == nil ||
					candidate.WorkflowRef != admission.Response.RootRef {
					return errWorkflowDependencyRevisionStale
				}
				return handler.fenceWorkflowDependencyAdmission(
					runCtx,
					admission,
					admission.Response.Revision,
				)
			},
			func(guarded func() error) error {
				return handler.guardWorkflowDependencyAdmissionConfig(
					admission,
					func() error {
						startOnce.Do(func() {
							startErr = startHelper()
						})
						if startErr != nil {
							return startErr
						}
						return guarded()
					},
				)
			},
			create,
		)
	}

	result, runErr := executor.Run(ctx, workflows.RunRequest{
		Ref: admission.Response.RootRef,
	})
	if runErr != nil {
		t.Fatalf("Run(captured config) error = %v, result=%#v", runErr, result)
	}
	if result == nil || result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("Run(captured config) result = %#v, want succeeded", result)
	}
	if !helperStarted {
		t.Fatal("cross-process SaveConfig helper did not start inside config guard")
	}
	if waitErr := command.Wait(); waitErr != nil {
		t.Fatalf("cross-process SaveConfig helper error = %v", waitErr)
	}
	helperWaited = true
	completed, err := os.ReadFile(completedPath)
	if err != nil {
		t.Fatalf("ReadFile(save completion) error = %v", err)
	}
	if string(completed) != "run-present" {
		t.Fatalf(
			"SaveConfig completion observation = %q, want durable run present",
			completed,
		)
	}
	currentConfig, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(after helper save) error = %v", err)
	}
	if currentConfig.Workflows.EffectiveMaxCallDepth() != 1 {
		t.Fatalf(
			"current max call depth = %d, want helper's 1",
			currentConfig.Workflows.EffectiveMaxCallDepth(),
		)
	}
	assertWorkflowRunCount(t, workspace, 3)
}

func TestWorkflowAdmissionConfigGuardClassifiesInfrastructureAndRevisionFailures(
	t *testing.T,
) {
	t.Run("revision drift is a conflict", func(t *testing.T) {
		configPath := filepath.Join(t.TempDir(), "config.json")
		initial := config.DefaultConfig()
		initial.Workflows.RetentionDays = 7
		if err := config.SaveConfig(configPath, initial); err != nil {
			t.Fatalf("SaveConfig(initial) error = %v", err)
		}
		revision, err := config.ConfigRevision(configPath)
		if err != nil {
			t.Fatalf("ConfigRevision(initial) error = %v", err)
		}
		current := config.DefaultConfig()
		current.Workflows.RetentionDays = 8
		if saveErr := config.SaveConfig(configPath, current); saveErr != nil {
			t.Fatalf("SaveConfig(current) error = %v", saveErr)
		}
		operationCalled := false
		err = NewHandler(configPath).guardWorkflowDependencyAdmissionConfig(
			&workflowDependencyAdmission{ConfigRevision: revision},
			func() error {
				operationCalled = true
				return nil
			},
		)
		if !errors.Is(err, errWorkflowDependencyRevisionStale) {
			t.Fatalf("config revision drift error = %v, want mismatch", err)
		}
		if operationCalled {
			t.Fatal("guarded create ran after config revision drift")
		}
	})

	t.Run("lock failure is unavailable", func(t *testing.T) {
		err := NewHandler("").guardWorkflowDependencyAdmissionConfig(
			&workflowDependencyAdmission{ConfigRevision: "missing"},
			func() error { return nil },
		)
		if !errors.Is(err, workflows.ErrWorkflowSnapshotAdmissionUnavailable) {
			t.Fatalf("config lock failure error = %v, want unavailable", err)
		}
	})

	t.Run("revision read failure is unavailable", func(t *testing.T) {
		configPath := t.TempDir()
		err := NewHandler(configPath).guardWorkflowDependencyAdmissionConfig(
			&workflowDependencyAdmission{ConfigRevision: "sha256:admitted"},
			func() error { return nil },
		)
		if !errors.Is(err, workflows.ErrWorkflowSnapshotAdmissionUnavailable) {
			t.Fatalf("config revision read failure error = %v, want unavailable", err)
		}
	})
}

func runWorkflowAdmissionConfigGuardSaveChild(t *testing.T) {
	t.Helper()
	configPath := os.Getenv("PICOCLAW_WORKFLOW_CONFIG_GUARD_PATH")
	workspace := os.Getenv("PICOCLAW_WORKFLOW_CONFIG_GUARD_WORKSPACE")
	readyPath := os.Getenv("PICOCLAW_WORKFLOW_CONFIG_GUARD_READY")
	completedPath := os.Getenv("PICOCLAW_WORKFLOW_CONFIG_GUARD_COMPLETED")
	if configPath == "" || workspace == "" || readyPath == "" || completedPath == "" {
		t.Fatal("workflow config guard helper environment is incomplete")
	}
	next := config.DefaultConfig()
	next.Agents.Defaults.Workspace = workspace
	next.Workflows.Enabled = true
	next.Workflows.DefinitionsDir = "automation"
	next.Workflows.MaxCallDepth = 1
	if err := os.WriteFile(readyPath, []byte("ready"), 0o600); err != nil {
		t.Fatalf("WriteFile(helper ready) error = %v", err)
	}
	if err := config.SaveConfig(configPath, next); err != nil {
		t.Fatalf("SaveConfig(helper) error = %v", err)
	}
	runs, err := workflows.NewFileRunStore(workspace).ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns(helper) error = %v", err)
	}
	observation := "run-missing"
	if len(runs) > 0 {
		observation = "run-present"
	}
	if err := os.WriteFile(completedPath, []byte(observation), 0o600); err != nil {
		t.Fatalf("WriteFile(helper completion) error = %v", err)
	}
}

func waitForWorkflowAdmissionTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("Stat(helper file) error = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for workflow admission helper process")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHandleRetryUsesCapturedSourceAfterAdmission(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/captured.yml",
		workflowRunReadinessDefinition,
	)
	handler := NewHandler(configPath)
	restoreDependencies := stubWorkflowDependencyRuntime(t, nil)
	t.Cleanup(restoreDependencies)
	revalidateWorkflowRunReadinessDefinition(t, handler, configPath)
	shown := checkPublishedWorkflowDependencies(
		t,
		handler,
		"workflows/captured.yml",
	)
	if !shown.Ready {
		t.Fatalf("shown dependency response = %#v, want ready", shown)
	}

	store := workflows.NewFileRunStore(workspace)
	now := time.Now().UTC()
	source := &workflows.Run{
		ID:          "wr_captured_retry_source",
		WorkflowRef: "workflows/captured.yml",
		Status:      workflows.RunStatusFailed,
		ParentRunID: "wr_captured_parent",
		CallerJobID: "child",
		Inputs:      map[string]any{"captured": true},
		CreatedAt:   now,
		UpdatedAt:   now,
		CompletedAt: &now,
	}
	if err := store.CreateRun(ctx, source); err != nil {
		t.Fatalf("CreateRun(source) error = %v", err)
	}

	previousRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = previousRunners })
	var mutationErr error
	var mutateOnce sync.Once
	newWorkflowRuntimeRunners = func(path string) workflowRuntimeRunners {
		mutateOnce.Do(func() {
			replacement, err := store.GetRun(ctx, source.ID)
			if err != nil {
				mutationErr = err
				return
			}
			replacement.WorkflowRef = "workflows/not-admitted.yml"
			mutationErr = store.UpdateRun(ctx, replacement)
		})
		return previousRunners(path)
	}

	response := postWorkflowRetry(
		t,
		handler,
		source.ID,
		map[string]any{"expected_dependency_revision": shown.Revision},
	)
	if mutationErr != nil {
		t.Fatalf("source mutation error = %v", mutationErr)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("retry response = (%d, %q), want success", response.Code, response.Body.String())
	}
	runs, err := store.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	var retry *workflows.Run
	for index := range runs {
		if runs[index].RetryOfRunID == source.ID {
			retry = &runs[index]
			break
		}
	}
	if retry == nil ||
		retry.WorkflowRef != source.WorkflowRef ||
		retry.ParentRunID != source.ParentRunID {
		t.Fatalf("captured retry = %#v, want original source context", retry)
	}
}

func TestStartWorkflowRunBackgroundTimeoutJoinsBeforeReturning(t *testing.T) {
	previousTimeout := workflowBackgroundStartTimeout
	workflowBackgroundStartTimeout = 20 * time.Millisecond
	t.Cleanup(func() { workflowBackgroundStartTimeout = previousTimeout })

	workflow, err := workflows.Parse([]byte(workflowRunReadinessDefinition))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	workspace := t.TempDir()
	store := workflows.NewFileRunStore(workspace)
	createResult := make(chan error, 1)
	executor := &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        store,
		AdmittedRunCreate: func(
			ctx context.Context,
			_ *workflows.Run,
			create func() error,
		) error {
			<-ctx.Done()
			err := create()
			createResult <- err
			return err
		},
	}
	started := startWorkflowRunBackground(
		executor,
		workflows.RunRequest{
			Workflow:    workflow,
			WorkflowRef: "workflows/timeout.yml",
		},
		nil,
	)
	if started.Run != nil ||
		started.Err == nil ||
		!strings.Contains(started.Err.Error(), "did not start within") {
		t.Fatalf("background start = %#v, want joined timeout", started)
	}
	select {
	case createErr := <-createResult:
		if !errors.Is(createErr, context.Canceled) {
			t.Fatalf("create after cancellation error = %v, want context canceled", createErr)
		}
	default:
		t.Fatal("background starter returned before create path stopped")
	}
	assertWorkflowRunCount(t, workspace, 0)

	persistedWorkspace := t.TempDir()
	persistedStore := workflows.NewFileRunStore(persistedWorkspace)
	persistedCallbackEntered := make(chan struct{})
	releasePersistedCallback := make(chan struct{})
	go func() {
		<-persistedCallbackEntered
		time.Sleep(3 * workflowBackgroundStartTimeout)
		close(releasePersistedCallback)
	}()
	persisted := startWorkflowRunBackground(
		&workflows.Executor{
			WorkspaceDir: persistedWorkspace,
			Store:        persistedStore,
		},
		workflows.RunRequest{
			Workflow:    workflow,
			WorkflowRef: "workflows/persisted-timeout.yml",
			OnRunPersisted: func(*workflows.Run) error {
				close(persistedCallbackEntered)
				<-releasePersistedCallback
				return errors.New("post-create persistence callback failed")
			},
		},
		nil,
	)
	if persisted.Run == nil ||
		persisted.Run.ID == "" ||
		persisted.Err != nil {
		t.Fatalf(
			"post-create timeout = %#v, want accepted durable run",
			persisted,
		)
	}
	persisted.Release()
	assertWorkflowRunCount(t, persistedWorkspace, 1)
}

func TestStartWorkflowRunBackgroundSkipsCallbackForPreCreateFailure(
	t *testing.T,
) {
	workflow, err := workflows.Parse([]byte(workflowRunReadinessDefinition))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	rejection := errors.New("admission rejected before create")
	handler := &Handler{}
	handler.workflowDevelopmentMu.Lock()
	locked := true
	defer func() {
		if locked {
			handler.workflowDevelopmentMu.Unlock()
		}
	}()
	callbackCalled := make(chan struct{}, 1)
	startedResult := make(chan backgroundWorkflowStart, 1)
	workspace := t.TempDir()
	go func() {
		startedResult <- startWorkflowRunBackground(
			&workflows.Executor{
				WorkspaceDir: workspace,
				AdmittedRunCreate: func(
					context.Context,
					*workflows.Run,
					func() error,
				) error {
					return rejection
				},
			},
			workflows.RunRequest{
				Workflow:    workflow,
				WorkflowRef: "workflows/pre-create-rejection.yml",
			},
			func(*workflows.RunResult, error) {
				handler.workflowDevelopmentMu.Lock()
				handler.workflowDevelopmentMu.Unlock()
				callbackCalled <- struct{}{}
			},
		)
	}()

	var started backgroundWorkflowStart
	select {
	case started = <-startedResult:
	case <-time.After(time.Second):
		handler.workflowDevelopmentMu.Unlock()
		locked = false
		<-startedResult
		t.Fatal("background starter deadlocked on the caller-owned development mutex")
	}
	if started.Run != nil || !errors.Is(started.Err, rejection) {
		t.Fatalf("background start = %#v, want pre-create rejection", started)
	}
	select {
	case <-callbackCalled:
		t.Fatal("completion callback ran for a non-durable rejection")
	default:
	}
	handler.workflowDevelopmentMu.Unlock()
	locked = false
	select {
	case <-callbackCalled:
		t.Fatal("completion callback was deferred for a non-durable rejection")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestStartWorkflowRunBackgroundAcceptsFastDurableFailureAndGatesCallback(
	t *testing.T,
) {
	workflow, err := workflows.Parse([]byte(workflowRunReadinessDefinition))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	workspace := t.TempDir()
	persistedErr := errors.New("post-create persistence callback failed")
	type completion struct {
		result *workflows.RunResult
		err    error
	}
	callback := make(chan completion, 1)
	started := startWorkflowRunBackground(
		&workflows.Executor{
			WorkspaceDir: workspace,
			Store:        workflows.NewFileRunStore(workspace),
		},
		workflows.RunRequest{
			Workflow:    workflow,
			WorkflowRef: "workflows/fast-durable-failure.yml",
			OnRunPersisted: func(*workflows.Run) error {
				return persistedErr
			},
		},
		func(result *workflows.RunResult, err error) {
			callback <- completion{result: result, err: err}
		},
	)
	if started.Run == nil || started.Run.ID == "" || started.Err != nil {
		t.Fatalf("background start = %#v, want accepted durable run", started)
	}
	select {
	case <-callback:
		t.Fatal("completion callback ran before the accepted start was released")
	default:
	}
	started.Release()
	select {
	case completed := <-callback:
		if completed.result == nil ||
			completed.result.RunID != started.Run.ID ||
			!errors.Is(completed.err, persistedErr) {
			t.Fatalf("completion = %#v, want durable persistence failure", completed)
		}
	case <-time.After(time.Second):
		t.Fatal("completion callback did not run after accepted start release")
	}
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
