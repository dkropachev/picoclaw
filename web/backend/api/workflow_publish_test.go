package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowDevelopmentPublishUsesExactDependencyAndRevisionFences(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/child.yml",
		workflowDependencyTestChild,
	)
	restore := stubWorkflowDependencyRuntime(t, nil)
	defer restore()
	handler := NewHandler(configPath)
	session := readyWorkflowDevelopmentSession(
		t,
		workspace,
		"automation",
		"workflows/root.yml",
		workflowDependencyTestRoot,
	)
	readiness := checkWorkflowDependencyDraft(
		t,
		handler,
		session.TargetWorkflowRef,
		session.YAML,
	)
	if !readiness.Ready {
		t.Fatalf("readiness = %#v", readiness)
	}

	recorder := publishWorkflowDevelopmentRequest(
		t,
		handler,
		session,
		readiness.Revision,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
	}
	if strings.Contains(recorder.Body.String(), workspace) {
		t.Fatalf("publish response leaked workspace path: %s", recorder.Body.String())
	}
	resolved, err := (workflows.Resolver{
		WorkspaceDir:   workspace,
		DefinitionsDir: "automation",
	}).ResolveLocal("workflows/root.yml")
	if err != nil {
		t.Fatalf("ResolveLocal() error = %v", err)
	}
	data, err := os.ReadFile(resolved.Path)
	if err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	}
	if string(data) != session.YAML {
		t.Fatalf("target bytes = %q, want exact draft %q", data, session.YAML)
	}
	active, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if active != nil {
		t.Fatalf("active session remained after publish: %#v", active)
	}
}

func TestWorkflowDevelopmentPublishRejectsChangedDependencyRevision(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	childPath := writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/child.yml",
		workflowDependencyTestChild,
	)
	restore := stubWorkflowDependencyRuntime(t, nil)
	defer restore()
	handler := NewHandler(configPath)
	session := readyWorkflowDevelopmentSession(
		t,
		workspace,
		"automation",
		"workflows/root.yml",
		workflowDependencyTestRoot,
	)
	readiness := checkWorkflowDependencyDraft(
		t,
		handler,
		session.TargetWorkflowRef,
		session.YAML,
	)
	if err := os.WriteFile(
		childPath,
		[]byte(strings.Replace(
			workflowDependencyTestChild,
			"name: Shared child",
			"name: Changed child",
			1,
		)),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile(child) error = %v", err)
	}

	recorder := publishWorkflowDevelopmentRequest(
		t,
		handler,
		session,
		readiness.Revision,
	)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), "dependency_revision_mismatch") {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	active, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil || active == nil {
		t.Fatalf("active session after rejected publish = %#v, error=%v", active, err)
	}
}

func TestWorkflowDevelopmentPublishRejectsCurrentButBlockedDependencies(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/child.yml",
		workflowDependencyTestChild,
	)
	restore := stubWorkflowDependencyRuntime(t, func(
		dependency workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		if dependency.Kind == workflows.WorkflowDependencyKindMCP {
			return workflows.WorkflowDependencyReadinessNotConnected
		}
		return workflows.WorkflowDependencyReadinessReady
	})
	defer restore()
	handler := NewHandler(configPath)
	session := readyWorkflowDevelopmentSession(
		t,
		workspace,
		"automation",
		"workflows/root.yml",
		workflowDependencyTestRoot,
	)
	readiness := checkWorkflowDependencyDraft(
		t,
		handler,
		session.TargetWorkflowRef,
		session.YAML,
	)
	if readiness.Ready || readiness.Revision == "" {
		t.Fatalf("readiness = %#v", readiness)
	}

	recorder := publishWorkflowDevelopmentRequest(
		t,
		handler,
		session,
		readiness.Revision,
	)
	if recorder.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(recorder.Body.String(), "workflow_dependencies_not_ready") {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWorkflowDevelopmentPublishRejectsStaleTargetAndUnsafeRequests(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	restore := stubWorkflowDependencyRuntime(t, nil)
	defer restore()
	handler := NewHandler(configPath)
	session := readyWorkflowDevelopmentSession(
		t,
		workspace,
		"automation",
		"workflows/root.yml",
		`name: Root
on:
  manual: {}
jobs:
  work:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
`,
	)
	readiness := checkWorkflowDependencyDraft(
		t,
		handler,
		session.TargetWorkflowRef,
		session.YAML,
	)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		"workflows/root.yml",
		"name: external change\n",
	)
	stale := publishWorkflowDevelopmentRequest(
		t,
		handler,
		session,
		readiness.Revision,
	)
	if stale.Code != http.StatusConflict ||
		!strings.Contains(stale.Body.String(), "target_revision_mismatch") {
		t.Fatalf("stale status = %d, body=%s", stale.Code, stale.Body.String())
	}

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "empty",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_publish_request",
		},
		{
			name:       "unknown field",
			body:       `{"session_id":"x","path":"/tmp/private"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_publish_request",
		},
		{
			name:       "trailing JSON",
			body:       `{}{}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_publish_request",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.handlePublishWorkflowDevelopment(
				recorder,
				httptest.NewRequest(
					http.MethodPost,
					"/api/workflows/development/publish",
					strings.NewReader(test.body),
				),
			)
			if recorder.Code != test.wantStatus ||
				!strings.Contains(recorder.Body.String(), test.wantCode) {
				t.Fatalf(
					"status = %d, body=%s, want status=%d code=%q",
					recorder.Code,
					recorder.Body.String(),
					test.wantStatus,
					test.wantCode,
				)
			}
		})
	}

	oversized := httptest.NewRecorder()
	handler.handlePublishWorkflowDevelopment(
		oversized,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/development/publish",
			strings.NewReader(
				`{"session_id":"`+
					strings.Repeat("x", workflowDevelopmentPublishRequestMaxBytes)+
					`"}`,
			),
		),
	)
	if oversized.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(oversized.Body.String(), "publish_request_too_large") {
		t.Fatalf("oversized status = %d, body=%s", oversized.Code, oversized.Body.String())
	}
}

func readyWorkflowDevelopmentSession(
	t *testing.T,
	workspace string,
	definitionsDir string,
	targetRef string,
	raw string,
) *workflows.WorkflowDevelopmentSession {
	t.Helper()
	session, err := workflows.StartWorkflowDevelopment(
		context.Background(),
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "test"},
		workflows.WorkflowDevelopmentStartRequest{
			Prompt:    "dependency fenced publish",
			TargetRef: targetRef,
		},
		workflows.WithDefinitionsDir(definitionsDir),
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	session, err = workflows.ReviseWorkflowDevelopment(
		workspace,
		workflows.WorkflowDevelopmentReviseRequest{YAML: &raw},
		workflows.WithDefinitionsDir(definitionsDir),
	)
	if err != nil {
		t.Fatalf("ReviseWorkflowDevelopment() error = %v", err)
	}
	session, err = workflows.ValidateWorkflowDevelopment(
		workspace,
		workflows.WithDefinitionsDir(definitionsDir),
	)
	if err != nil {
		t.Fatalf("ValidateWorkflowDevelopment() error = %v", err)
	}
	if session.Validation == nil || !session.Validation.Valid {
		t.Fatalf("session validation = %#v", session.Validation)
	}
	session, err = workflows.RecordWorkflowDevelopmentTest(
		workspace,
		&workflows.RunResult{
			RunID:  "test-run",
			Status: workflows.RunStatusSucceeded,
		},
		nil,
	)
	if err != nil {
		t.Fatalf("RecordWorkflowDevelopmentTest() error = %v", err)
	}
	return session
}

func publishWorkflowDevelopmentRequest(
	t *testing.T,
	handler *Handler,
	session *workflows.WorkflowDevelopmentSession,
	dependencyRevision string,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(workflows.WorkflowDevelopmentPublishRequest{
		SessionID:                  session.ID,
		ExpectedSessionRevision:    session.SessionRevision,
		ExpectedDraftRevision:      session.DraftRevision,
		ExpectedBaseTargetRevision: session.BaseTargetRevision,
		ExpectedDependencyRevision: dependencyRevision,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.handlePublishWorkflowDevelopment(
		recorder,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/development/publish",
			strings.NewReader(string(body)),
		),
	)
	return recorder
}
