package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentloop "github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestHandleAIReviseWorkflowDevelopmentAppliesFencedYAML(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	configPath := writeWorkflowAITestConfig(t, workspace)
	h := NewHandler(configPath)

	if _, err := workflows.StartWorkflowDevelopment(
		ctx,
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"},
		workflows.WorkflowDevelopmentStartRequest{Prompt: "summarize support issues"},
	); err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}

	aiYAML := workflows.GenerateWorkflowDraftYAML("triage support issues and notify owner")
	oldRunner := runWorkflowAuthorAgent
	t.Cleanup(func() { runWorkflowAuthorAgent = oldRunner })
	runWorkflowAuthorAgent = func(
		_ context.Context,
		_ *Handler,
		session *workflows.WorkflowDevelopmentSession,
		validation *workflows.WorkflowDevelopmentValidation,
		_ []workflows.Definition,
	) (string, error) {
		if session.Prompt != "revise into a triage workflow" {
			t.Fatalf("session prompt = %q", session.Prompt)
		}
		if validation == nil || !validation.Valid {
			t.Fatalf("validation before AI = %#v, want valid", validation)
		}
		return "```yaml\n" + aiYAML + "```", nil
	}

	body := `{"prompt":"revise into a triage workflow","target_ref":"workflows/support-triage.yml"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workflows/development/ai-revise", strings.NewReader(body))
	h.handleAIReviseWorkflowDevelopment(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Session *workflows.WorkflowDevelopmentSession `json:"session"`
	}
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &got); decodeErr != nil {
		t.Fatalf("response JSON error = %v", decodeErr)
	}
	if got.Session == nil {
		t.Fatal("response session = nil")
	}
	if got.Session.TargetWorkflowRef != "workflows/support-triage.yml" {
		t.Fatalf("target ref = %q", got.Session.TargetWorkflowRef)
	}
	if got.Session.Validation == nil || !got.Session.Validation.Valid {
		t.Fatalf("validation = %#v, want valid", got.Session.Validation)
	}
	if got.Session.YAML != aiYAML {
		t.Fatalf("session YAML = %q, want AI YAML %q", got.Session.YAML, aiYAML)
	}
}

func TestHandleAIReviseWorkflowDevelopmentRequiresActiveSession(t *testing.T) {
	h := NewHandler(writeWorkflowAITestConfig(t, t.TempDir()))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workflows/development/ai-revise", strings.NewReader(`{}`))

	h.handleAIReviseWorkflowDevelopment(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAIReviseWorkflowDevelopmentRejectsConcurrentDevelopmentOperation(t *testing.T) {
	h := NewHandler(writeWorkflowAITestConfig(t, t.TempDir()))
	h.workflowDevelopmentMu.Lock()
	t.Cleanup(h.workflowDevelopmentMu.Unlock)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workflows/development/ai-revise", strings.NewReader(`{}`))
	h.handleAIReviseWorkflowDevelopment(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleTestWorkflowDevelopmentRunsDraftAgentStep(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	h := NewHandler(writeWorkflowAITestConfig(t, workspace))
	oldRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = oldRunners })
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		runner := fakeWorkflowRuntimeRunner{agentResponse: "draft agent response"}
		return workflowRuntimeRunners{Tools: runner, Agents: runner}
	}
	session, err := workflows.StartWorkflowDevelopment(
		ctx,
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"},
		workflows.WorkflowDevelopmentStartRequest{Prompt: "summarize support issues"},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workflows/development/test", strings.NewReader(`{}`))
	h.handleTestWorkflowDevelopment(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Session *workflows.WorkflowDevelopmentSession `json:"session"`
		Result  *workflows.RunResult                  `json:"result"`
		Error   string                                `json:"error"`
	}
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &got); decodeErr != nil {
		t.Fatalf("response JSON error = %v", decodeErr)
	}
	if got.Session == nil || got.Session.LastTest == nil {
		t.Fatalf("session last test = %#v, want persisted draft test", got.Session)
	}
	if got.Result == nil || got.Result.RunID == "" {
		t.Fatalf("result = %#v, want persisted run id", got.Result)
	}
	if got.Result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("result status = %q, want %q", got.Result.Status, workflows.RunStatusSucceeded)
	}
	if got.Error != "" {
		t.Fatalf("error = %q, want empty", got.Error)
	}
	if got.Session.LastTest.RunID != got.Result.RunID ||
		got.Session.LastTest.Status != workflows.RunStatusSucceeded ||
		got.Session.LastTest.DraftKey != workflows.WorkflowDevelopmentDraftKey(
			session.TargetWorkflowRef,
			session.YAML,
		) {
		t.Fatalf("last test = %#v, want matching succeeded run", got.Session.LastTest)
	}
	run, err := workflows.NewFileRunStore(workspace).GetRun(ctx, got.Result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.WorkflowRef != "draft:"+session.TargetWorkflowRef {
		t.Fatalf("workflow ref = %q, want draft ref for %q", run.WorkflowRef, session.TargetWorkflowRef)
	}
	step := run.Steps["develop/run_agent"]
	if step.Outputs["text"] != "draft agent response" {
		t.Fatalf("step text output = %#v, want draft agent response", step.Outputs["text"])
	}
}

func TestHandleTestWorkflowDevelopmentRunsDraftToolStep(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	h := NewHandler(writeWorkflowAITestConfig(t, workspace))
	oldRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = oldRunners })
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		runner := fakeWorkflowRuntimeRunner{toolResponse: "draft tool response"}
		return workflowRuntimeRunners{Tools: runner, Agents: runner}
	}
	session, err := workflows.StartWorkflowDevelopment(
		ctx,
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"},
		workflows.WorkflowDevelopmentStartRequest{Prompt: "send a workflow notification"},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}

	draftYAML := `name: Tool Draft
on:
  manual: {}
jobs:
  develop:
    runs-on: picoclaw
    steps:
      - id: notify
        uses: tool/message
        with:
          text: draft notification
`
	body, err := json.Marshal(map[string]any{"yaml": draftYAML})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workflows/development/test", strings.NewReader(string(body)))
	h.handleTestWorkflowDevelopment(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Result *workflows.RunResult `json:"result"`
		Error  string               `json:"error"`
	}
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &got); decodeErr != nil {
		t.Fatalf("response JSON error = %v", decodeErr)
	}
	if got.Result == nil || got.Result.RunID == "" {
		t.Fatalf("result = %#v, want persisted run id", got.Result)
	}
	if got.Result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("result status = %q, want %q", got.Result.Status, workflows.RunStatusSucceeded)
	}
	if got.Error != "" {
		t.Fatalf("error = %q, want empty", got.Error)
	}
	run, err := workflows.NewFileRunStore(workspace).GetRun(ctx, got.Result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.WorkflowRef != "draft:"+session.TargetWorkflowRef {
		t.Fatalf("workflow ref = %q, want draft ref for %q", run.WorkflowRef, session.TargetWorkflowRef)
	}
	step := run.Steps["develop/notify"]
	if step.Outputs["text"] != "draft tool response" {
		t.Fatalf("step text output = %#v, want draft tool response", step.Outputs["text"])
	}
	if step.Outputs["tool"] != "message" {
		t.Fatalf("step tool output = %#v, want message", step.Outputs["tool"])
	}
}

func TestHandleTestWorkflowDevelopmentRejectsInvalidDraftWithoutRun(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	h := NewHandler(writeWorkflowAITestConfig(t, workspace))
	if _, err := workflows.StartWorkflowDevelopment(
		ctx,
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"},
		workflows.WorkflowDevelopmentStartRequest{Prompt: "summarize support issues"},
	); err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}

	body := `{"yaml":"name: Broken\njobs: {}\n"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workflows/development/test", strings.NewReader(body))
	h.handleTestWorkflowDevelopment(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	runs, err := workflows.NewFileRunStore(workspace).ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %#v, want none for invalid draft", runs)
	}
	active, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if active == nil || active.LastTest == nil {
		t.Fatalf("active last test = %#v, want validation failure snapshot", active)
	}
	if active.LastTest.Status != "validation_failed" || active.LastTest.RunID != "" {
		t.Fatalf("active last test = %#v, want validation_failed without run", active.LastTest)
	}
}

func TestHandleTestWorkflowDevelopmentStartsAsyncRun(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	h := NewHandler(writeWorkflowAITestConfig(t, workspace))
	runner := newBlockingWorkflowRuntimeRunner("async draft response")
	oldRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() {
		runner.release()
		newWorkflowRuntimeRunners = oldRunners
	})
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		return workflowRuntimeRunners{Tools: runner, Agents: runner}
	}
	session, err := workflows.StartWorkflowDevelopment(
		ctx,
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"},
		workflows.WorkflowDevelopmentStartRequest{Prompt: "summarize support issues"},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workflows/development/test", strings.NewReader(`{"async":true}`))
	h.handleTestWorkflowDevelopment(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Session *workflows.WorkflowDevelopmentSession `json:"session"`
		Result  *workflows.RunResult                  `json:"result"`
	}
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &got); decodeErr != nil {
		t.Fatalf("response JSON error = %v", decodeErr)
	}
	if got.Result == nil || got.Result.Status != workflows.RunStatusRunning {
		t.Fatalf("result = %#v, want running run", got.Result)
	}
	if got.Session == nil || got.Session.LastTest == nil ||
		got.Session.LastTest.RunID != got.Result.RunID ||
		got.Session.LastTest.Status != workflows.RunStatusRunning ||
		got.Session.LastTest.DraftKey != workflows.WorkflowDevelopmentDraftKey(
			session.TargetWorkflowRef,
			session.YAML,
		) {
		t.Fatalf("session last test = %#v, want running async test", got.Session)
	}
	run, err := workflows.NewFileRunStore(workspace).GetRun(ctx, got.Result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.Status != workflows.RunStatusRunning {
		t.Fatalf("run status = %q, want running", run.Status)
	}

	runner.release()
	waitForWorkflowRunStatus(t, workspace, got.Result.RunID, workflows.RunStatusSucceeded)
	active, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if active == nil || active.LastTest == nil ||
		active.LastTest.RunID != got.Result.RunID ||
		active.LastTest.Status != workflows.RunStatusSucceeded {
		t.Fatalf("active last test = %#v, want async completion recorded", active)
	}
}

func TestHandleCancelWorkflowRunRecordsRunningDraftTestCanceled(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	h := NewHandler(writeWorkflowAITestConfig(t, workspace))
	session, err := workflows.StartWorkflowDevelopment(
		ctx,
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"},
		workflows.WorkflowDevelopmentStartRequest{Prompt: "summarize support issues"},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	runID := "wr_draft_cancel"
	now := time.Now().UTC()
	if createErr := workflows.NewFileRunStore(workspace).CreateRun(ctx, &workflows.Run{
		ID:          runID,
		WorkflowRef: "draft:" + session.TargetWorkflowRef,
		Status:      workflows.RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); createErr != nil {
		t.Fatalf("CreateRun() error = %v", createErr)
	}
	if _, recordErr := workflows.RecordWorkflowDevelopmentTest(
		workspace,
		&workflows.RunResult{RunID: runID, Status: workflows.RunStatusRunning},
		nil,
	); recordErr != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest(running) error = %v", recordErr)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/runs/"+runID+"/cancel",
		strings.NewReader(`{"reason":"stop draft"}`),
	)
	req.SetPathValue("run_id", runID)
	h.handleCancelWorkflowRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	active, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if active == nil || active.LastTest == nil ||
		active.LastTest.RunID != runID ||
		active.LastTest.Status != workflows.RunStatusCanceled ||
		active.LastTest.Error != "stop draft" {
		t.Fatalf("active last test = %#v, want canceled draft test", active)
	}
}

func TestHandleCancelWorkflowRunClosesWorkflowRuntime(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	h := NewHandler(writeWorkflowAITestConfig(t, workspace))
	runID := "wr_cancel_close"
	now := time.Now().UTC()
	if err := workflows.NewFileRunStore(workspace).CreateRun(ctx, &workflows.Run{
		ID:          runID,
		WorkflowRef: "workflows/cancel.yml",
		Status:      workflows.RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	runner := &closeCountingWorkflowRuntimeRunner{}
	orig := newWorkflowRuntimeRunners
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		return workflowRuntimeRunners{Tools: runner, Agents: runner, RuntimeEvents: runner}
	}
	defer func() { newWorkflowRuntimeRunners = orig }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/workflows/runs/"+runID+"/cancel", strings.NewReader(`{}`))
	req.SetPathValue("run_id", runID)
	h.handleCancelWorkflowRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if runner.publishCount == 0 {
		t.Fatal("cancel did not publish runtime event")
	}
	if runner.closeCount == 0 {
		t.Fatal("workflow runtime was not closed after cancel")
	}
}

func TestHandleRunWorkflowStartsAsyncRun(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	workflowDir := filepath.Join(workspace, "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "async.yml"), []byte(`name: Async
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - id: ask
        uses: agent/default
        with:
          prompt: async
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	h := NewHandler(writeWorkflowAITestConfig(t, workspace))
	if _, err := workflows.RevalidateLocal(ctx, workspace, h.workflowCompatibilityRuntime(ctx)); err != nil {
		t.Fatalf("RevalidateLocal() error = %v", err)
	}

	runner := newBlockingWorkflowRuntimeRunner("async manual response")
	oldRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() {
		runner.release()
		newWorkflowRuntimeRunners = oldRunners
	})
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		return workflowRuntimeRunners{Tools: runner, Agents: runner}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/run",
		strings.NewReader(`{"ref":"workflows/async.yml","async":true}`),
	)
	h.handleRunWorkflow(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got workflows.RunResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response JSON error = %v", err)
	}
	if got.RunID == "" || got.Status != workflows.RunStatusRunning {
		t.Fatalf("result = %#v, want running run id", got)
	}
	run, err := workflows.NewFileRunStore(workspace).GetRun(ctx, got.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.WorkflowRef != "workflows/async.yml" || run.Status != workflows.RunStatusRunning {
		t.Fatalf("run = %#v, want running workflows/async.yml", run)
	}

	runner.release()
	waitForWorkflowRunStatus(t, workspace, got.RunID, workflows.RunStatusSucceeded)
}

func TestExtractWorkflowAuthorYAMLTrimsProseAndFence(t *testing.T) {
	got, err := extractWorkflowAuthorYAML(
		"Here is the draft:\n```yaml\nname: Test\non:\n  manual: {}\njobs: {}\n```\nDone.",
	)
	if err != nil {
		t.Fatalf("extractWorkflowAuthorYAML() error = %v", err)
	}
	want := "name: Test\non:\n  manual: {}\njobs: {}\n"
	if got != want {
		t.Fatalf("YAML = %q, want %q", got, want)
	}
}

func TestBuildWorkflowAuthorPromptIncludesNativeFunctionTargets(t *testing.T) {
	prompt := buildWorkflowAuthorPrompt(&workflows.WorkflowDevelopmentSession{
		ID:                "dev_123",
		Reason:            workflows.WorkflowDevelopmentReasonNew,
		TargetWorkflowRef: "workflows/test.yml",
		YAML:              "name: Test\non:\n  manual: {}\njobs: {}\n",
	}, nil, nil, workflowAuthorCapabilities{
		Agents: []agentloop.AgentDescriptor{{
			ID:          "main",
			Description: "Default workflow authoring agent",
		}},
		Tools: []string{"- `message` - Send a message"},
	})

	if !strings.Contains(
		prompt,
		"Dashboard-testable step targets may use agent/, tool/, mcp/, or supported native function/ targets.",
	) {
		t.Fatalf("prompt does not prefer dashboard-testable targets:\n%s", prompt)
	}
	if !strings.Contains(prompt, "function/git.inventory") {
		t.Fatalf("prompt does not include native function targets:\n%s", prompt)
	}
	if strings.Contains(prompt, "function/git.file_plan") || strings.Contains(prompt, "function/git.file_record") {
		t.Fatalf("prompt includes review-specific native function targets:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Prefer native function/ targets over shell scripts") {
		t.Fatalf("prompt does not prefer native functions over scripts:\n%s", prompt)
	}
	if !strings.Contains(prompt, "agent/main - Default workflow authoring agent") {
		t.Fatalf("prompt does not include agent target inventory:\n%s", prompt)
	}
	if !strings.Contains(prompt, "`message` - Send a message") {
		t.Fatalf("prompt does not include tool target inventory:\n%s", prompt)
	}
}

func writeWorkflowAITestConfig(t *testing.T, workspace string) string {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	return configPath
}

type fakeWorkflowRuntimeRunner struct {
	agentResponse string
	toolResponse  string
}

func (r fakeWorkflowRuntimeRunner) RunAgent(_ context.Context, req workflows.AgentRequest) (map[string]any, error) {
	return map[string]any{
		"text":     r.agentResponse,
		"agent_id": req.AgentID,
		"session":  req.Session,
	}, nil
}

func (r fakeWorkflowRuntimeRunner) RunTool(_ context.Context, req workflows.ToolRequest) (map[string]any, error) {
	return map[string]any{
		"text":    r.toolResponse,
		"tool":    req.Name,
		"session": req.Session,
		"args":    req.Args,
	}, nil
}

type closeCountingWorkflowRuntimeRunner struct {
	closeCount   int
	publishCount int
}

func (r *closeCountingWorkflowRuntimeRunner) RunAgent(
	context.Context,
	workflows.AgentRequest,
) (map[string]any, error) {
	return map[string]any{"text": "ok"}, nil
}

func (r *closeCountingWorkflowRuntimeRunner) RunTool(
	context.Context,
	workflows.ToolRequest,
) (map[string]any, error) {
	return map[string]any{"text": "ok"}, nil
}

func (r *closeCountingWorkflowRuntimeRunner) PublishNonBlocking(
	runtimeevents.Event,
) runtimeevents.PublishResult {
	r.publishCount++
	return runtimeevents.PublishResult{}
}

func (r *closeCountingWorkflowRuntimeRunner) Close() error {
	r.closeCount++
	return nil
}

type blockingWorkflowRuntimeRunner struct {
	response  string
	released  chan struct{}
	closed    chan struct{}
	once      sync.Once
	closeOnce sync.Once
}

func newBlockingWorkflowRuntimeRunner(response string) *blockingWorkflowRuntimeRunner {
	return &blockingWorkflowRuntimeRunner{
		response: response,
		released: make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

func (r *blockingWorkflowRuntimeRunner) RunAgent(
	ctx context.Context,
	req workflows.AgentRequest,
) (map[string]any, error) {
	if err := r.wait(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"text":     r.response,
		"agent_id": req.AgentID,
		"session":  req.Session,
	}, nil
}

func (r *blockingWorkflowRuntimeRunner) RunTool(
	ctx context.Context,
	req workflows.ToolRequest,
) (map[string]any, error) {
	if err := r.wait(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"text":    r.response,
		"tool":    req.Name,
		"session": req.Session,
		"args":    req.Args,
	}, nil
}

func (r *blockingWorkflowRuntimeRunner) wait(ctx context.Context) error {
	select {
	case <-r.closed:
		return errors.New("workflow runtime closed before runner release")
	default:
	}
	select {
	case <-r.released:
		select {
		case <-r.closed:
			return errors.New("workflow runtime closed before runner release")
		default:
			return nil
		}
	case <-r.closed:
		return errors.New("workflow runtime closed before runner release")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *blockingWorkflowRuntimeRunner) release() {
	r.once.Do(func() {
		close(r.released)
	})
}

func (r *blockingWorkflowRuntimeRunner) Close() error {
	r.closeOnce.Do(func() {
		close(r.closed)
	})
	return nil
}

func waitForWorkflowRunStatus(t *testing.T, workspace, runID, status string) {
	t.Helper()
	store := workflows.NewFileRunStore(workspace)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := store.GetRun(context.Background(), runID)
		if err == nil && run.Status == status {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	run, err := store.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun() after wait error = %v", err)
	}
	t.Fatalf("run status = %q, want %q", run.Status, status)
}
