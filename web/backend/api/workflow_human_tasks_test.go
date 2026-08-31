package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowHumanTaskHandlersResumeAndCancelDurably(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	handler := NewHandler(writeWorkflowAITestConfig(t, workspace))

	waiting := startWorkflowHumanTaskRun(t, workspace, "wr_human_resume")
	tasks := listWorkflowHumanTasks(t, handler, waiting.RunID)
	if len(tasks) != 1 || tasks[0].Status != workflows.HumanTaskStatusWaiting {
		t.Fatalf("tasks = %#v, want one waiting task", tasks)
	}
	task := tasks[0]
	secret := "resume-secret-must-not-be-persisted"
	responseBody := `{"expected_revision":1,"input_hash":` +
		quotedJSON(t, task.InputHash) +
		`,"response_id":"answer-1","response":{"approved":true},"secrets":{"TOKEN":` +
		quotedJSON(t, secret) +
		`}}`
	resume := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/"+waiting.RunID+"/tasks/"+task.ID+"/resume",
		waiting.RunID,
		task.ID,
		responseBody,
	)
	if resume.Code != http.StatusOK {
		t.Fatalf("resume status = %d, body = %s", resume.Code, resume.Body.String())
	}
	var resumed workflows.RunResult
	if err := json.Unmarshal(resume.Body.Bytes(), &resumed); err != nil {
		t.Fatalf("resume response JSON error = %v", err)
	}
	if resumed.Status != workflows.RunStatusSucceeded {
		t.Fatalf("resume result = %#v, want succeeded", resumed)
	}
	// The same response ID and value is an idempotent observation, not a second
	// continuation. The step output remains stable and no conflict is returned.
	replay := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/"+waiting.RunID+"/tasks/"+task.ID+"/resume",
		waiting.RunID,
		task.ID,
		responseBody,
	)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status = %d, body = %s", replay.Code, replay.Body.String())
	}

	persisted, err := workflows.NewFileRunStore(workspace).GetRun(ctx, waiting.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	answer, ok := persisted.Steps["main/approval"].Outputs["response"].(map[string]any)
	if !ok || answer["approved"] != true {
		t.Fatalf("answer output = %#v, want approved response", persisted.Steps["main/approval"].Outputs["response"])
	}
	db, err := sql.Open("sqlite", filepath.Join(workspace, "state", "workflows.db"))
	if err != nil {
		t.Fatal(err)
	}
	var persistedPayload string
	if queryErr := db.QueryRow(`SELECT COALESCE(CAST(event_json AS TEXT),'') ||
		COALESCE(CAST(inputs_json AS TEXT),'') || COALESCE(CAST(outputs_json AS TEXT),'') ||
		COALESCE(CAST(delivery_handles_json AS TEXT),'') || COALESCE(CAST(execution_json AS TEXT),'') ||
		COALESCE(CAST(private_context_json AS TEXT),'') FROM workflow_run_payloads WHERE run_id=?`,
		waiting.RunID).Scan(&persistedPayload); queryErr != nil {
		db.Close()
		t.Fatal(queryErr)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if strings.Contains(persistedPayload, secret) || strings.Contains(resume.Body.String(), secret) {
		t.Fatal("the direct resume secrets map was persisted or returned")
	}
	projected, err := json.Marshal(workflows.ProjectWorkflowRunForBrowser(persisted, false))
	if err != nil {
		t.Fatalf("project run JSON error = %v", err)
	}
	for _, forbidden := range []string{"execution", "workflow_execution", "human_tasks", "response_schema"} {
		if strings.Contains(string(projected), forbidden) {
			t.Fatalf("browser run leaked %q: %s", forbidden, projected)
		}
	}

	cancelWaiting := startWorkflowHumanTaskRun(t, workspace, "wr_human_cancel")
	cancelTasks := listWorkflowHumanTasks(t, handler, cancelWaiting.RunID)
	cancel := workflowHumanTaskRequest(
		t,
		handler.handleCancelWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/"+cancelWaiting.RunID+"/tasks/"+cancelTasks[0].ID+"/cancel",
		cancelWaiting.RunID,
		cancelTasks[0].ID,
		`{"reason":"operator canceled"}`,
	)
	if cancel.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancel.Code, cancel.Body.String())
	}
	canceledTasks := listWorkflowHumanTasks(t, handler, cancelWaiting.RunID)
	if len(canceledTasks) != 1 || canceledTasks[0].Status != workflows.HumanTaskStatusCanceled {
		t.Fatalf("canceled tasks = %#v, want canceled", canceledTasks)
	}
}

func TestWorkflowHumanTaskResumeRejectsDisabledBeforeClaim(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	configPath := writeWorkflowAITestConfig(t, workspace)
	waiting := startWorkflowHumanTaskRun(t, workspace, "wr_human_disabled")
	task := listStoredWorkflowHumanTasks(t, workspace, waiting.RunID)[0]

	cfg, revision, err := config.LoadConfigForUpdateSnapshot(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdateSnapshot() error = %v", err)
	}
	cfg.Workflows.Enabled = false
	if _, err := config.SaveConfigIfRevision(configPath, cfg, revision); err != nil {
		t.Fatalf("SaveConfigIfRevision() error = %v", err)
	}

	previousRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = previousRunners })
	runtimeCalls := 0
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		runtimeCalls++
		return workflowRuntimeRunners{}
	}
	handler := NewHandler(configPath)
	response := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/"+waiting.RunID+"/tasks/"+task.ID+"/resume",
		waiting.RunID,
		task.ID,
		humanTaskResumeBody(t, task, "disabled-answer"),
	)
	assertWorkflowHumanTaskErrorResponse(
		t,
		response,
		http.StatusConflict,
		"workflow_tasks_disabled",
	)
	if runtimeCalls != 0 {
		t.Fatalf("workflow runtime created %d times while workflows were disabled", runtimeCalls)
	}
	assertWorkflowHumanTaskNotClaimed(t, ctx, workspace, waiting.RunID, task)
}

func TestWorkflowHumanTaskResumeFencesConfigRevisionBeforeClaim(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	configPath := writeWorkflowAITestConfig(t, workspace)
	waiting := startWorkflowHumanTaskRun(t, workspace, "wr_human_config_fence")
	task := listStoredWorkflowHumanTasks(t, workspace, waiting.RunID)[0]

	previousRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = previousRunners })
	mutationCalls := 0
	var mutationErr error
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		mutationCalls++
		cfg, revision, err := config.LoadConfigForUpdateSnapshot(configPath)
		if err == nil {
			cfg.Workflows.RetentionDays++
			_, err = config.SaveConfigIfRevision(configPath, cfg, revision)
		}
		mutationErr = err
		return workflowRuntimeRunners{}
	}

	response := workflowHumanTaskRequest(
		t,
		NewHandler(configPath).handleResumeWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/"+waiting.RunID+"/tasks/"+task.ID+"/resume",
		waiting.RunID,
		task.ID,
		humanTaskResumeBody(t, task, "fenced-answer"),
	)
	assertWorkflowHumanTaskErrorResponse(
		t,
		response,
		http.StatusConflict,
		"workflow_task_config_changed",
	)
	if mutationCalls != 1 || mutationErr != nil {
		t.Fatalf("config fence mutation calls=%d error=%v", mutationCalls, mutationErr)
	}
	assertWorkflowHumanTaskNotClaimed(t, ctx, workspace, waiting.RunID, task)
}

func TestWorkflowHumanTaskResumeReportsConcurrencyWithoutClaim(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	configPath := writeWorkflowAITestConfig(t, workspace)
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdateSnapshot() error = %v", err)
	}
	cfg.Workflows.MaxConcurrentRuns = 1
	if _, err := config.SaveConfigIfRevision(configPath, cfg, revision); err != nil {
		t.Fatalf("SaveConfigIfRevision() error = %v", err)
	}

	waiting := startWorkflowHumanTaskRun(t, workspace, "wr_human_concurrency")
	task := listStoredWorkflowHumanTasks(t, workspace, waiting.RunID)[0]
	store := workflows.NewFileRunStore(workspace)
	now := time.Now().UTC()
	if err := store.CreateRun(ctx, &workflows.Run{
		ID:          "wr_other_running",
		WorkflowRef: "workflows/other.yml",
		Status:      workflows.RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("CreateRun(other) error = %v", err)
	}

	handler := NewHandler(configPath)
	body := humanTaskResumeBody(t, task, "concurrency-answer")
	response := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/"+waiting.RunID+"/tasks/"+task.ID+"/resume",
		waiting.RunID,
		task.ID,
		body,
	)
	assertWorkflowHumanTaskErrorResponse(
		t,
		response,
		http.StatusConflict,
		"workflow_task_concurrency_limit",
	)
	assertWorkflowHumanTaskNotClaimed(t, ctx, workspace, waiting.RunID, task)

	if err := store.DeleteRun(ctx, "wr_other_running"); err != nil {
		t.Fatalf("DeleteRun(other) error = %v", err)
	}
	retry := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/"+waiting.RunID+"/tasks/"+task.ID+"/resume",
		waiting.RunID,
		task.ID,
		body,
	)
	if retry.Code != http.StatusOK {
		t.Fatalf("resume after releasing admission status = %d, body = %s", retry.Code, retry.Body.String())
	}
}

func TestWorkflowHumanTaskDevelopmentDraftLifecycle(t *testing.T) {
	t.Run("synchronous multiple waits resume to publishable", func(t *testing.T) {
		workspace := t.TempDir()
		handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
		yaml := workflowHumanTaskDraftYAML(true)
		started := startWorkflowHumanTaskDraftTest(t, handler, workspace, yaml, false)
		if started.Status != workflows.RunStatusWaiting {
			t.Fatalf("draft result status = %q, want waiting", started.Status)
		}
		waitForWorkflowDevelopmentTestStatus(t, workspace, started.RunID, workflows.RunStatusWaiting)
		if _, err := workflows.ReviseWorkflowDevelopment(
			workspace,
			workflows.WorkflowDevelopmentReviseRequest{YAML: &yaml},
		); !errors.Is(err, workflows.ErrDevelopmentBusy) {
			t.Fatalf("ReviseWorkflowDevelopment() error = %v, want busy while waiting", err)
		}

		first := waitingWorkflowHumanTask(t, handler, started.RunID)
		firstResume := resumeWorkflowHumanTask(t, handler, first, "first-answer")
		if firstResume.Status != workflows.RunStatusWaiting {
			t.Fatalf("first resume status = %q, want second wait", firstResume.Status)
		}
		waitForWorkflowDevelopmentTestStatus(t, workspace, started.RunID, workflows.RunStatusWaiting)
		second := waitingWorkflowHumanTask(t, handler, started.RunID)
		if second.ID == first.ID {
			t.Fatal("second suspension reused the first task identity")
		}
		secondResume := resumeWorkflowHumanTask(t, handler, second, "second-answer")
		if secondResume.Status != workflows.RunStatusSucceeded {
			t.Fatalf("second resume status = %q, want succeeded", secondResume.Status)
		}
		active := waitForWorkflowDevelopmentTestStatus(
			t,
			workspace,
			started.RunID,
			workflows.RunStatusSucceeded,
		)
		if active.Status != workflows.WorkflowDevelopmentStatusReadyToPublish {
			t.Fatalf("development status = %q, want ready_to_publish", active.Status)
		}
	})

	t.Run("asynchronous wait resumes", func(t *testing.T) {
		workspace := t.TempDir()
		handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
		started := startWorkflowHumanTaskDraftTest(
			t,
			handler,
			workspace,
			workflowHumanTaskDraftYAML(false),
			true,
		)
		waitForWorkflowRunStatus(t, workspace, started.RunID, workflows.RunStatusWaiting)
		waitForWorkflowDevelopmentTestStatus(t, workspace, started.RunID, workflows.RunStatusWaiting)
		task := waitingWorkflowHumanTask(t, handler, started.RunID)
		resumed := resumeWorkflowHumanTask(t, handler, task, "async-answer")
		if resumed.Status != workflows.RunStatusSucceeded {
			t.Fatalf("resume status = %q, want succeeded", resumed.Status)
		}
		waitForWorkflowDevelopmentTestStatus(t, workspace, started.RunID, workflows.RunStatusSucceeded)
	})

	t.Run("task cancellation reconciles draft", func(t *testing.T) {
		workspace := t.TempDir()
		handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
		started := startWorkflowHumanTaskDraftTest(
			t,
			handler,
			workspace,
			workflowHumanTaskDraftYAML(false),
			false,
		)
		task := waitingWorkflowHumanTask(t, handler, started.RunID)
		response := workflowHumanTaskRequest(
			t,
			handler.handleCancelWorkflowHumanTask,
			http.MethodPost,
			"/api/workflows/runs/"+started.RunID+"/tasks/"+task.ID+"/cancel",
			started.RunID,
			task.ID,
			`{"reason":"draft canceled"}`,
		)
		if response.Code != http.StatusOK {
			t.Fatalf("cancel status = %d, body = %s", response.Code, response.Body.String())
		}
		active := waitForWorkflowDevelopmentTestStatus(
			t,
			workspace,
			started.RunID,
			workflows.RunStatusCanceled,
		)
		if active.LastTest.Error != "draft canceled" {
			t.Fatalf("cancel error = %q, want operator reason", active.LastTest.Error)
		}
	})

	t.Run("refresh repairs completion after handler crash", func(t *testing.T) {
		ctx := context.Background()
		workspace := t.TempDir()
		handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
		started := startWorkflowHumanTaskDraftTest(
			t,
			handler,
			workspace,
			workflowHumanTaskDraftYAML(false),
			false,
		)
		task := waitingWorkflowHumanTask(t, handler, started.RunID)
		result, err := (&workflows.Executor{WorkspaceDir: workspace}).ResumeHumanTask(
			ctx,
			started.RunID,
			task.ID,
			workflows.HumanTaskResumeRequest{
				ExpectedRevision: task.Revision,
				InputHash:        task.InputHash,
				ResponseID:       "recovery-answer",
				Response:         true,
			},
		)
		if err != nil || result.Status != workflows.RunStatusSucceeded {
			t.Fatalf("direct resume result=%#v error=%v", result, err)
		}
		before, err := workflows.GetWorkflowDevelopmentSession(workspace)
		if err != nil || before.LastTest.Status != workflows.RunStatusWaiting {
			t.Fatalf("pre-refresh development session=%#v error=%v", before, err)
		}
		recorder := httptest.NewRecorder()
		handler.handleGetWorkflowDevelopment(
			recorder,
			httptest.NewRequest(http.MethodGet, "/api/workflows/development", nil),
		)
		if recorder.Code != http.StatusOK {
			t.Fatalf("refresh status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		active := waitForWorkflowDevelopmentTestStatus(
			t,
			workspace,
			started.RunID,
			workflows.RunStatusSucceeded,
		)
		if active.Status != workflows.WorkflowDevelopmentStatusReadyToPublish {
			t.Fatalf("recovered development status = %q", active.Status)
		}
	})
}

func workflowHumanTaskDraftYAML(multiple bool) string {
	second := ""
	if multiple {
		second = `
      - id: confirm
        uses: human/task
        with: {title: Confirm, questions: [{id: confirm}]}`
	}
	return `name: Human task draft
on: {manual: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: approve
        uses: human/task
        with: {title: Approve, questions: [{id: approve}]}` + second + "\n"
}

func startWorkflowHumanTaskDraftTest(
	t *testing.T,
	handler *Handler,
	workspace string,
	yaml string,
	async bool,
) *workflows.RunResult {
	t.Helper()
	if _, err := workflows.StartWorkflowDevelopment(
		context.Background(),
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "test"},
		workflows.WorkflowDevelopmentStartRequest{
			Prompt:    "human review",
			TargetRef: "workflows/human-draft.yml",
		},
	); err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	body, err := json.Marshal(workflowDevelopmentTestRequest{YAML: &yaml, Async: async})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.handleTestWorkflowDevelopment(
		recorder,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/development/test",
			strings.NewReader(string(body)),
		),
	)
	wantStatus := http.StatusOK
	if async {
		wantStatus = http.StatusAccepted
	}
	if recorder.Code != wantStatus {
		t.Fatalf("draft test status = %d, want %d; body = %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	var response struct {
		Result *workflows.RunResult `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("draft test response JSON error = %v", err)
	}
	if response.Result == nil || response.Result.RunID == "" {
		t.Fatalf("draft test result = %#v", response.Result)
	}
	return response.Result
}

func waitingWorkflowHumanTask(
	t *testing.T,
	handler *Handler,
	runID string,
) workflows.WorkflowHumanTask {
	t.Helper()
	for _, task := range listWorkflowHumanTasks(t, handler, runID) {
		if task.Status == workflows.HumanTaskStatusWaiting {
			return task
		}
	}
	t.Fatalf("run %q has no waiting human task", runID)
	return workflows.WorkflowHumanTask{}
}

func resumeWorkflowHumanTask(
	t *testing.T,
	handler *Handler,
	task workflows.WorkflowHumanTask,
	responseID string,
) *workflows.RunResult {
	t.Helper()
	requestBody := `{"expected_revision":` + fmt.Sprint(task.Revision) +
		`,"input_hash":` + quotedJSON(t, task.InputHash) +
		`,"response_id":` + quotedJSON(t, responseID) +
		`,"response":true}`
	recorder := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/"+task.RunID+"/tasks/"+task.ID+"/resume",
		task.RunID,
		task.ID,
		requestBody,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("resume status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var result workflows.RunResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("resume response JSON error = %v", err)
	}
	return &result
}

func waitForWorkflowDevelopmentTestStatus(
	t *testing.T,
	workspace string,
	runID string,
	status string,
) *workflows.WorkflowDevelopmentSession {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		session, err := workflows.GetWorkflowDevelopmentSession(workspace)
		if err != nil {
			t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
		}
		if session != nil && session.LastTest != nil &&
			session.LastTest.RunID == runID && session.LastTest.Status == status {
			return session
		}
		if time.Now().After(deadline) {
			t.Fatalf("development test did not reach %q: %#v", status, session)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startWorkflowHumanTaskRun(t *testing.T, workspace, runID string) *workflows.RunResult {
	t.Helper()
	workflow := &workflows.Workflow{
		Name: "Human task API test",
		On:   workflows.WorkflowTriggers{Manual: map[string]any{}},
		Jobs: map[string]workflows.Job{
			"main": {
				RunsOn: "picoclaw",
				Steps: []workflows.Step{{
					ID:   "approval",
					Uses: "human/task",
					With: map[string]any{
						"title":      "Approve local result",
						"input_hash": "snapshot-1",
						"questions": []any{map[string]any{
							"id": "approved", "prompt": "Continue?",
						}},
						"response_schema": map[string]any{
							"type":                 "object",
							"required":             []any{"approved"},
							"additionalProperties": false,
							"properties": map[string]any{
								"approved": map[string]any{"type": "boolean"},
							},
						},
					},
				}},
			},
		},
	}
	result, err := (&workflows.Executor{WorkspaceDir: workspace}).Run(
		context.Background(),
		workflows.RunRequest{
			RunID:       runID,
			Workflow:    workflow,
			WorkflowRef: "workflows/human-task-api.yml",
		},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != workflows.RunStatusWaiting {
		t.Fatalf("Run() = %#v, want waiting", result)
	}
	return result
}

func listStoredWorkflowHumanTasks(
	t *testing.T,
	workspace string,
	runID string,
) []workflows.WorkflowHumanTask {
	t.Helper()
	tasks, err := (&workflows.Executor{WorkspaceDir: workspace}).ListHumanTasks(
		context.Background(),
		runID,
	)
	if err != nil {
		t.Fatalf("ListHumanTasks() error = %v", err)
	}
	return tasks
}

func humanTaskResumeBody(
	t *testing.T,
	task workflows.WorkflowHumanTask,
	responseID string,
) string {
	t.Helper()
	return `{"expected_revision":` + fmt.Sprint(task.Revision) +
		`,"input_hash":` + quotedJSON(t, task.InputHash) +
		`,"response_id":` + quotedJSON(t, responseID) +
		`,"response":{"approved":true}}`
}

func assertWorkflowHumanTaskNotClaimed(
	t *testing.T,
	ctx context.Context,
	workspace string,
	runID string,
	want workflows.WorkflowHumanTask,
) {
	t.Helper()
	tasks := listStoredWorkflowHumanTasks(t, workspace, runID)
	if len(tasks) != 1 {
		t.Fatalf("stored tasks = %#v, want one task", tasks)
	}
	got := tasks[0]
	if got.ID != want.ID || got.Status != workflows.HumanTaskStatusWaiting ||
		got.Revision != want.Revision || got.ResponseID != "" || got.Response != nil ||
		got.AnsweredAt != nil {
		t.Fatalf("rejected resume mutated task: got=%#v want=%#v", got, want)
	}
	run, err := workflows.NewFileRunStore(workspace).GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.Status != workflows.RunStatusWaiting ||
		run.Steps["main/approval"].Status != workflows.RunStatusWaiting {
		t.Fatalf("rejected resume mutated run: %#v", run)
	}
}

func listWorkflowHumanTasks(
	t *testing.T,
	handler *Handler,
	runID string,
) []workflows.WorkflowHumanTask {
	t.Helper()
	recorder := workflowHumanTaskRequest(
		t,
		handler.handleListWorkflowHumanTasks,
		http.MethodGet,
		"/api/workflows/runs/"+runID+"/tasks",
		runID,
		"",
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Tasks []workflows.WorkflowHumanTask `json:"tasks"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("list response JSON error = %v", err)
	}
	return response.Tasks
}

func workflowHumanTaskRequest(
	t *testing.T,
	handler func(http.ResponseWriter, *http.Request),
	method string,
	path string,
	runID string,
	taskID string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.SetPathValue("run_id", runID)
	if taskID != "" {
		request.SetPathValue("task_id", taskID)
	}
	handler(recorder, request)
	assertWorkflowHumanTaskResponseHeaders(t, recorder)
	return recorder
}

func assertWorkflowHumanTaskResponseHeaders(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func quotedJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(encoded)
}

func TestWorkflowHumanTaskRoutesAndHandlerFailures(t *testing.T) {
	workspace := t.TempDir()
	handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	waiting := startWorkflowHumanTaskRun(t, workspace, "wr_human_routes")
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/workflows/runs/"+waiting.RunID+"/tasks",
		nil,
	)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	assertWorkflowHumanTaskResponseHeaders(t, recorder)
	if recorder.Code != http.StatusOK {
		t.Fatalf("registered list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	wrongMethod := httptest.NewRecorder()
	mux.ServeHTTP(
		wrongMethod,
		httptest.NewRequest(
			http.MethodDelete,
			"/api/workflows/runs/"+waiting.RunID+"/tasks",
			nil,
		),
	)
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong-method status = %d, want %d", wrongMethod.Code, http.StatusMethodNotAllowed)
	}

	missing := workflowHumanTaskRequest(
		t,
		handler.handleListWorkflowHumanTasks,
		http.MethodGet,
		"/api/workflows/runs/wr_missing/tasks",
		"wr_missing",
		"",
		"",
	)
	assertWorkflowHumanTaskErrorResponse(
		t,
		missing,
		http.StatusNotFound,
		"workflow_task_not_found",
	)
	missingResume := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/wr_missing/tasks/ht_missing/resume",
		"wr_missing",
		"ht_missing",
		`{"expected_revision":1,"input_hash":"hash","response_id":"answer","response":true}`,
	)
	assertWorkflowHumanTaskErrorResponse(
		t,
		missingResume,
		http.StatusNotFound,
		"workflow_task_not_found",
	)
	missingCancel := workflowHumanTaskRequest(
		t,
		handler.handleCancelWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/wr_missing/tasks/ht_missing/cancel",
		"wr_missing",
		"ht_missing",
		`{"reason":"operator canceled"}`,
	)
	assertWorkflowHumanTaskErrorResponse(
		t,
		missingCancel,
		http.StatusNotFound,
		"workflow_task_not_found",
	)

	task := listWorkflowHumanTasks(t, handler, waiting.RunID)[0]
	resumePath := "/api/workflows/runs/" + waiting.RunID + "/tasks/" + task.ID + "/resume"
	staleRequestBody := `{"expected_revision":2,"input_hash":` +
		quotedJSON(t, task.InputHash) +
		`,"response_id":"stale","response":{"approved":true}}`
	stale := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		resumePath,
		waiting.RunID,
		task.ID,
		staleRequestBody,
	)
	assertWorkflowHumanTaskErrorResponse(t, stale, http.StatusConflict, "workflow_task_stale")

	invalidResponseBody := `{"expected_revision":1,"input_hash":` +
		quotedJSON(t, task.InputHash) +
		`,"response_id":"invalid","response":{"approved":"yes"}}`
	invalidResponse := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		resumePath,
		waiting.RunID,
		task.ID,
		invalidResponseBody,
	)
	assertWorkflowHumanTaskErrorResponse(
		t,
		invalidResponse,
		http.StatusBadRequest,
		"workflow_task_response_invalid",
	)

	malformedResume := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		resumePath,
		waiting.RunID,
		task.ID,
		`{"unknown":true}`,
	)
	assertWorkflowHumanTaskErrorResponse(
		t,
		malformedResume,
		http.StatusBadRequest,
		"invalid_task_resume_request",
	)

	largeResume := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		resumePath,
		waiting.RunID,
		task.ID,
		`{"response":"`+strings.Repeat("x", workflowHumanTaskRequestMaxBytes)+`"}`,
	)
	assertWorkflowHumanTaskErrorResponse(
		t,
		largeResume,
		http.StatusRequestEntityTooLarge,
		"workflow_task_request_too_large",
	)

	cancelPath := "/api/workflows/runs/" + waiting.RunID + "/tasks/" + task.ID + "/cancel"
	for name, body := range map[string]string{
		"invalid reason": `{"reason":"   "}`,
		"unknown field":  `{"reason":"no","unknown":true}`,
	} {
		t.Run("cancel "+name, func(t *testing.T) {
			response := workflowHumanTaskRequest(
				t,
				handler.handleCancelWorkflowHumanTask,
				http.MethodPost,
				cancelPath,
				waiting.RunID,
				task.ID,
				body,
			)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
	largeCancel := workflowHumanTaskRequest(
		t,
		handler.handleCancelWorkflowHumanTask,
		http.MethodPost,
		cancelPath,
		waiting.RunID,
		task.ID,
		`{"reason":"`+strings.Repeat("x", workflowHumanTaskRequestMaxBytes)+`"}`,
	)
	assertWorkflowHumanTaskErrorResponse(
		t,
		largeCancel,
		http.StatusRequestEntityTooLarge,
		"workflow_task_request_too_large",
	)
}

func TestWorkflowHumanTaskFailedReplayHidesRuntimeDiagnostic(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowAITestConfig(t, workspace)
	handler := NewHandler(configPath)
	private := "private/provider/path/and-secret"
	runner := &workflowEventFailingRunner{}
	previousRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = previousRunners })
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		return workflowRuntimeRunners{Tools: runner, Agents: runner}
	}
	workflow := &workflows.Workflow{
		Name: "Failed human continuation",
		On:   workflows.WorkflowTriggers{Manual: map[string]any{}},
		Jobs: map[string]workflows.Job{
			"main": {
				RunsOn: "picoclaw",
				Steps: []workflows.Step{
					{
						ID: "approval", Uses: "human/task",
						With: map[string]any{"title": "Approve", "questions": []any{"continue"}},
					},
					{
						ID: "fail", Uses: "tool/fail",
						With: map[string]any{"private": private},
					},
				},
			},
		},
	}
	executor := &workflows.Executor{WorkspaceDir: workspace, Tools: runner}
	started, err := executor.Run(context.Background(), workflows.RunRequest{
		Workflow: workflow, WorkflowRef: "workflows/failing-human.yml",
	})
	if err != nil || started.Status != workflows.RunStatusWaiting {
		t.Fatalf("Run() result=%#v error=%v", started, err)
	}
	task := waitingWorkflowHumanTask(t, handler, started.RunID)
	body := `{"expected_revision":1,"input_hash":` +
		quotedJSON(t, task.InputHash) +
		`,"response_id":"failed-answer","response":true}`
	first := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/"+started.RunID+"/tasks/"+task.ID+"/resume",
		started.RunID,
		task.ID,
		body,
	)
	assertWorkflowHumanTaskErrorResponse(
		t,
		first,
		http.StatusInternalServerError,
		"workflow_task_operation_failed",
	)
	if strings.Contains(first.Body.String(), private) {
		t.Fatalf("initial failure leaked runtime diagnostic: %s", first.Body.String())
	}
	replay := workflowHumanTaskRequest(
		t,
		handler.handleResumeWorkflowHumanTask,
		http.MethodPost,
		"/api/workflows/runs/"+started.RunID+"/tasks/"+task.ID+"/resume",
		started.RunID,
		task.ID,
		body,
	)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status = %d, body = %s", replay.Code, replay.Body.String())
	}
	if strings.Contains(replay.Body.String(), private) || strings.Contains(replay.Body.String(), "provider echoed") {
		t.Fatalf("replayed failure leaked runtime diagnostic: %s", replay.Body.String())
	}
	var replayed workflows.RunResult
	if err := json.Unmarshal(replay.Body.Bytes(), &replayed); err != nil {
		t.Fatalf("replay response JSON error = %v", err)
	}
	if replayed.Status != workflows.RunStatusFailed || replayed.Error != "" {
		t.Fatalf("replay result = %#v, want sanitized failed observation", replayed)
	}
}

func TestWorkflowHumanTaskHandlersHideRuntimeErrors(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile(config.json) error = %v", err)
	}
	handler := NewHandler(configPath)
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		method  string
		path    string
		taskID  string
		body    string
	}{
		{
			name: "list", handler: handler.handleListWorkflowHumanTasks,
			method: http.MethodGet, path: "/api/workflows/runs/wr_test/tasks",
		},
		{
			name: "resume", handler: handler.handleResumeWorkflowHumanTask,
			method: http.MethodPost, path: "/api/workflows/runs/wr_test/tasks/ht_test/resume",
			taskID: "ht_test",
			body:   `{"expected_revision":1,"input_hash":"hash","response_id":"answer","response":{}}`,
		},
		{
			name: "cancel", handler: handler.handleCancelWorkflowHumanTask,
			method: http.MethodPost, path: "/api/workflows/runs/wr_test/tasks/ht_test/cancel",
			taskID: "ht_test", body: `{"reason":"operator canceled"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := workflowHumanTaskRequest(
				t,
				test.handler,
				test.method,
				test.path,
				"wr_test",
				test.taskID,
				test.body,
			)
			assertWorkflowHumanTaskErrorResponse(
				t,
				response,
				http.StatusInternalServerError,
				"workflow_tasks_unavailable",
			)
			if strings.Contains(response.Body.String(), configPath) {
				t.Fatalf("response leaked config path: %s", response.Body.String())
			}
		})
	}
}

func assertWorkflowHumanTaskErrorResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, wantStatus, response.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response JSON error = %v; body = %s", err, response.Body.String())
	}
	if payload["error"] != wantCode {
		t.Fatalf("error = %q, want %q", payload["error"], wantCode)
	}
}

func TestDecodeWorkflowHumanTaskRequestIsStrictAndBounded(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name: "valid",
			body: `{"expected_revision":1,"input_hash":"hash",` +
				`"response_id":"response-1","response":{"count":9007199254740993}}`,
		},
		{name: "empty", body: "", wantStatus: http.StatusBadRequest},
		{name: "null", body: "null", wantStatus: http.StatusBadRequest},
		{name: "array", body: "[]", wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"expected_revision":1,"unknown":true}`, wantStatus: http.StatusBadRequest},
		{name: "trailing value", body: `{"expected_revision":1}{}`, wantStatus: http.StatusBadRequest},
		{
			name:       "too large",
			body:       `{"response":"` + strings.Repeat("x", workflowHumanTaskRequestMaxBytes) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/workflows/runs/wr_test/tasks/ht_test/resume",
				strings.NewReader(test.body),
			)
			var destination workflows.HumanTaskResumeRequest
			err := decodeWorkflowHumanTaskRequest(recorder, request, &destination)
			if test.wantStatus == 0 {
				if err != nil {
					t.Fatalf("decodeWorkflowHumanTaskRequest() error = %v", err)
				}
				response, ok := destination.Response.(map[string]any)
				if !ok || response["count"] != json.Number("9007199254740993") {
					t.Fatalf("response = %#v, want exact json.Number", destination.Response)
				}
				return
			}
			if err == nil {
				t.Fatal("decodeWorkflowHumanTaskRequest() succeeded, want error")
			}
			response := httptest.NewRecorder()
			writeWorkflowHumanTaskDecodeError(response, err, "invalid_task_resume_request")
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestDecodeWorkflowHumanTaskRequestRejectsInvalidUTF8BeforeJSONReplacement(t *testing.T) {
	invalid := string([]byte{0xff})
	tests := []struct {
		name        string
		body        string
		destination any
	}{
		{
			name:        "resume response id",
			body:        `{"response_id":"` + invalid + `","response":true}`,
			destination: &workflows.HumanTaskResumeRequest{},
		},
		{
			name:        "cancel reason",
			body:        `{"reason":"` + invalid + `"}`,
			destination: &workflowHumanTaskCancelRequest{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/workflows/runs/wr_test/tasks/ht_test",
				strings.NewReader(test.body),
			)
			if err := decodeWorkflowHumanTaskRequest(
				recorder,
				request,
				test.destination,
			); err == nil {
				t.Fatal("decodeWorkflowHumanTaskRequest() succeeded, want invalid UTF-8 error")
			}
		})
	}
}

func TestWriteWorkflowHumanTaskErrorUsesFixedCodes(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name: "not found", err: workflows.ErrHumanTaskNotFound,
			wantStatus: http.StatusNotFound, wantCode: "workflow_task_not_found",
		},
		{
			name: "stale", err: workflows.ErrHumanTaskStale,
			wantStatus: http.StatusConflict, wantCode: "workflow_task_stale",
		},
		{
			name: "conflict", err: workflows.ErrHumanTaskConflict,
			wantStatus: http.StatusConflict, wantCode: "workflow_task_conflict",
		},
		{
			name: "unsupported", err: workflows.ErrHumanTaskUnsupported,
			wantStatus: http.StatusConflict, wantCode: "workflow_task_unsupported",
		},
		{
			name: "invalid response", err: workflows.ErrHumanTaskResponseInvalid,
			wantStatus: http.StatusBadRequest, wantCode: "workflow_task_response_invalid",
		},
		{
			name: "concurrency limit", err: workflows.ErrRunConcurrencyLimit,
			wantStatus: http.StatusConflict, wantCode: "workflow_task_concurrency_limit",
		},
		{
			name: "internal", err: errors.New("private path and secret"),
			wantStatus: http.StatusInternalServerError, wantCode: "workflow_task_operation_failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeWorkflowHumanTaskError(recorder, test.err)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, test.wantStatus)
			}
			var response map[string]string
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("response JSON error = %v", err)
			}
			if response["error"] != test.wantCode {
				t.Fatalf("error = %q, want %q", response["error"], test.wantCode)
			}
			if strings.Contains(recorder.Body.String(), "private") {
				t.Fatalf("response leaked internal error: %s", recorder.Body.String())
			}
		})
	}
}
