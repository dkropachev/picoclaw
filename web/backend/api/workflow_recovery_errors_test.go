package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowRecoveryConflictResponsesRequireOperatorAttention(t *testing.T) {
	recoveryConflict := errors.Join(
		workflows.ErrWorkflowDevelopmentPublishRecoveryFailed,
		workflows.ErrWorkflowRecoveryConflict,
	)
	tests := []struct {
		name  string
		write func(http.ResponseWriter, error)
	}{
		{name: "template", write: writeWorkflowTemplateError},
		{name: "publish", write: writeWorkflowPublishError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.write(recorder, recoveryConflict)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
			}
			var response map[string]string
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response["error"] != "workflow_transaction_recovery_conflict" {
				t.Fatalf("error = %q", response["error"])
			}
		})
	}
}

func TestWorkflowTemplateRecoveryFailureResponseIsFixed(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeWorkflowTemplateError(
		recorder,
		errors.Join(
			workflows.ErrWorkflowTemplateCatalogUnavailable,
			workflows.ErrWorkflowTemplateRecoveryFailed,
		),
	)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"status = %d, want %d",
			recorder.Code,
			http.StatusServiceUnavailable,
		)
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["error"] != "template_recovery_failed" {
		t.Fatalf("error = %q", response["error"])
	}
}

func TestWorkflowPublishTemplateRecoveryFailureResponseIsFixed(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeWorkflowPublishError(
		recorder,
		errors.Join(
			workflows.ErrWorkflowTemplateCatalogUnavailable,
			workflows.ErrWorkflowTemplateRecoveryFailed,
		),
	)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"status = %d, want %d",
			recorder.Code,
			http.StatusServiceUnavailable,
		)
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["error"] != "workflow_publish_recovery_failed" {
		t.Fatalf("error = %q", response["error"])
	}
}
