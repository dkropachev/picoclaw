package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestReviseWorkflowDevelopmentAPIPreservesExactTrailingYAMLBytes(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	handler := NewHandler(configPath)
	session := readyWorkflowDevelopmentSession(
		t,
		workspace,
		"automation",
		"workflows/exact.yml",
		workflowDependencyTestRoot,
	)
	previousDraftRevision := session.DraftRevision
	exactYAML := session.YAML + " \t\n\n"
	body, err := json.Marshal(map[string]any{
		"session_id":                session.ID,
		"expected_session_revision": session.SessionRevision,
		"expected_draft_revision":   session.DraftRevision,
		"target_ref":                session.TargetWorkflowRef,
		"yaml":                      exactYAML,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/development/revise",
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.handleReviseWorkflowDevelopment(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Session *workflows.WorkflowDevelopmentSession `json:"session"`
	}
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &response); decodeErr != nil {
		t.Fatalf("decode response: %v", decodeErr)
	}
	if response.Session == nil {
		t.Fatal("session = nil")
	}
	if response.Session.YAML != exactYAML {
		t.Fatalf(
			"response YAML = %q, want exact bytes %q",
			response.Session.YAML,
			exactYAML,
		)
	}
	if response.Session.DraftRevision == previousDraftRevision ||
		response.Session.DraftRevision != workflows.WorkflowDevelopmentDraftRevision(
			response.Session.TargetWorkflowRef,
			exactYAML,
		) {
		t.Fatalf("response draft revision = %q", response.Session.DraftRevision)
	}
	if response.Session.LastTest != nil {
		t.Fatalf("last test = %#v, want stale test cleared", response.Session.LastTest)
	}
	persisted, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil {
		t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
	}
	if persisted == nil || persisted.YAML != exactYAML {
		t.Fatalf("persisted session = %#v, want exact YAML", persisted)
	}
}
