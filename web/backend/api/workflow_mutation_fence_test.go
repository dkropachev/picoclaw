package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowDevelopmentHTTPMutationsRejectReplacedSessionWithoutWrite(t *testing.T) {
	for _, test := range []struct {
		name    string
		path    string
		draft   bool
		handler func(*Handler, http.ResponseWriter, *http.Request)
	}{
		{
			name: "save", path: "/api/workflows/development/revise", draft: true,
			handler: func(handler *Handler, w http.ResponseWriter, r *http.Request) {
				handler.handleReviseWorkflowDevelopment(w, r)
			},
		},
		{
			name: "validate", path: "/api/workflows/development/validate",
			handler: func(handler *Handler, w http.ResponseWriter, r *http.Request) {
				handler.handleValidateWorkflowDevelopment(w, r)
			},
		},
		{
			name: "AI revise", path: "/api/workflows/development/ai-revise", draft: true,
			handler: func(handler *Handler, w http.ResponseWriter, r *http.Request) {
				handler.handleAIReviseWorkflowDevelopment(w, r)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
			first := startWorkflowMutationFenceTestSession(t, workspace, "first")
			if _, err := workflows.DiscardWorkflowDevelopment(workspace); err != nil {
				t.Fatal(err)
			}
			second := startWorkflowMutationFenceTestSession(t, workspace, "second")
			before := marshalWorkflowMutationFenceSession(t, second)
			payload := map[string]any{
				"session_id":                first.ID,
				"expected_session_revision": first.SessionRevision,
				"expected_draft_revision":   first.DraftRevision,
			}
			if test.draft {
				payload["prompt"] = "stale browser write"
				payload["yaml"] = workflows.GenerateWorkflowDraftYAML("stale browser write")
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			test.handler(handler, response, request)
			requireWorkflowDevelopmentFenceError(t, response)
			after, err := workflows.GetWorkflowDevelopmentSession(workspace)
			if err != nil || after == nil || after.ID != second.ID {
				t.Fatalf("replacement session = %#v, %v", after, err)
			}
			if got := marshalWorkflowMutationFenceSession(t, after); got != before {
				t.Fatalf("replacement session changed:\n before=%s\n after=%s", before, got)
			}
		})
	}
}

func TestAIReviseWorkflowDevelopmentDoesNotTouchReplacementCreatedDuringProviderCall(t *testing.T) {
	workspace := t.TempDir()
	handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
	first := startWorkflowMutationFenceTestSession(t, workspace, "first")
	var replacement *workflows.WorkflowDevelopmentSession
	oldRunner := runWorkflowAuthorAgent
	t.Cleanup(func() { runWorkflowAuthorAgent = oldRunner })
	runWorkflowAuthorAgent = func(
		_ context.Context,
		_ *Handler,
		_ *workflows.WorkflowDevelopmentSession,
		_ *workflows.WorkflowDevelopmentValidation,
		_ []workflows.Definition,
	) (string, error) {
		if _, err := workflows.DiscardWorkflowDevelopment(workspace); err != nil {
			t.Fatalf("DiscardWorkflowDevelopment() during provider call: %v", err)
		}
		replacement = startWorkflowMutationFenceTestSession(t, workspace, "replacement")
		return workflows.GenerateWorkflowDraftYAML("AI response for stale session"), nil
	}
	body, err := json.Marshal(map[string]any{
		"session_id":                first.ID,
		"expected_session_revision": first.SessionRevision,
		"expected_draft_revision":   first.DraftRevision,
		"prompt":                    "ask provider",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/development/ai-revise",
		strings.NewReader(string(body)),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.handleAIReviseWorkflowDevelopment(response, request)
	requireWorkflowDevelopmentFenceError(t, response)
	if replacement == nil {
		t.Fatal("provider did not create replacement session")
	}
	want := marshalWorkflowMutationFenceSession(t, replacement)
	active, err := workflows.GetWorkflowDevelopmentSession(workspace)
	if err != nil || active == nil || active.ID != replacement.ID {
		t.Fatalf("active replacement = %#v, %v", active, err)
	}
	if got := marshalWorkflowMutationFenceSession(t, active); got != want {
		t.Fatalf("provider-call replacement changed:\n want=%s\n got=%s", want, got)
	}
}

func startWorkflowMutationFenceTestSession(
	t *testing.T,
	workspace string,
	prompt string,
) *workflows.WorkflowDevelopmentSession {
	t.Helper()
	session, err := workflows.StartWorkflowDevelopment(
		context.Background(),
		workspace,
		workflows.RuntimeCompatibility{},
		workflows.WorkflowDevelopmentStartRequest{
			Prompt:    prompt,
			TargetRef: "workflows/" + prompt + ".yml",
		},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment(%q) error = %v", prompt, err)
	}
	return session
}

func marshalWorkflowMutationFenceSession(
	t *testing.T,
	session *workflows.WorkflowDevelopmentSession,
) string {
	t.Helper()
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func requireWorkflowDevelopmentFenceError(
	t *testing.T,
	response *httptest.ResponseRecorder,
) {
	t.Helper()
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode fence error: %v; body=%s", err, response.Body.String())
	}
	if response.Code != http.StatusConflict ||
		body.Code != "workflow_development_fence_mismatch" ||
		body.Message == "" || len(response.Body.Bytes()) > 1024 {
		t.Fatalf("fence error = %d %s", response.Code, response.Body.String())
	}
}
