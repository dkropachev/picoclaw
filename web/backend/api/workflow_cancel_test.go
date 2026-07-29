package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestHandleCancelWorkflowRunValidatesReasonBeforeMutation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "explicit empty",
			body:       `{"reason":""}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_cancel_reason",
		},
		{
			name:       "explicit whitespace",
			body:       `{"reason":"  \n\t "}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_cancel_reason",
		},
		{
			name:       "explicit null",
			body:       `{"reason":null}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_cancel_request",
		},
		{
			name:       "over UTF-8 byte limit",
			body:       `{"reason":"` + strings.Repeat("é", workflows.MaxWorkflowCancelReasonBytes/2+1) + `"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_cancel_reason",
		},
		{
			name:       "unknown field",
			body:       `{"reason":"stop","origin":"forged"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_cancel_request",
		},
		{
			name:       "trailing JSON",
			body:       `{"reason":"stop"}{}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_cancel_request",
		},
		{
			name:       "request too large",
			body:       `{"reason":"` + strings.Repeat("x", workflowCancelRequestMaxBytes) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   "cancel_request_too_large",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
			run := createCancelableWorkflowRun(t, workspace, "wr_cancel_invalid")
			recorder := cancelWorkflowRunRequest(t, handler, run.ID, test.body)
			if recorder.Code != test.wantStatus ||
				!strings.Contains(recorder.Body.String(), `"`+test.wantCode+`"`) {
				t.Fatalf(
					"response = (%d, %q), want %d %q",
					recorder.Code,
					recorder.Body.String(),
					test.wantStatus,
					test.wantCode,
				)
			}
			persisted, err := workflows.NewFileRunStore(workspace).GetRun(
				context.Background(),
				run.ID,
			)
			if err != nil {
				t.Fatalf("GetRun() error = %v", err)
			}
			if persisted.Status != workflows.RunStatusRunning ||
				persisted.CancelReason != "" ||
				persisted.CancelRequestedAt != nil ||
				persisted.CompletedAt != nil {
				t.Fatalf("invalid request mutated run = %#v", persisted)
			}
		})
	}
}

func TestHandleCancelWorkflowRunProjectsCompleteLifecycle(t *testing.T) {
	workspace := t.TempDir()
	handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
	run := createCancelableWorkflowRun(t, workspace, "wr_cancel_lifecycle")

	recorder := cancelWorkflowRunRequest(
		t,
		handler,
		run.ID,
		`{"reason":"  operator requested stop  "}`,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var canceled workflows.Run
	if err := json.Unmarshal(recorder.Body.Bytes(), &canceled); err != nil {
		t.Fatalf("cancel response JSON error = %v", err)
	}
	if canceled.Status != workflows.RunStatusCanceled ||
		canceled.CancelReason != "operator requested stop" ||
		canceled.CancelRequestedAt == nil ||
		canceled.CompletedAt == nil {
		t.Fatalf("cancel response = %#v", canceled)
	}

	detail := httptest.NewRecorder()
	detailRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/workflows/runs/"+run.ID,
		nil,
	)
	detailRequest.SetPathValue("run_id", run.ID)
	handler.handleGetWorkflowRun(detail, detailRequest)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", detail.Code, detail.Body.String())
	}
	var projected workflows.Run
	if err := json.Unmarshal(detail.Body.Bytes(), &projected); err != nil {
		t.Fatalf("detail JSON error = %v", err)
	}
	if projected.CancelReason != canceled.CancelReason ||
		projected.CancelRequestedAt == nil ||
		!projected.CancelRequestedAt.Equal(*canceled.CancelRequestedAt) ||
		projected.CompletedAt == nil ||
		!projected.CompletedAt.Equal(*canceled.CompletedAt) {
		t.Fatalf("projected lifecycle = %#v, canceled = %#v", projected, canceled)
	}
}

func TestHandleCancelWorkflowRunPreservesEmptyReasonWhenMissing(
	t *testing.T,
) {
	for _, body := range []string{"", `{}`} {
		t.Run(body, func(t *testing.T) {
			workspace := t.TempDir()
			handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
			run := createCancelableWorkflowRun(t, workspace, "wr_cancel_default")
			recorder := cancelWorkflowRunRequest(t, handler, run.ID, body)
			if recorder.Code != http.StatusOK {
				t.Fatalf("cancel status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
			var canceled workflows.Run
			if err := json.Unmarshal(recorder.Body.Bytes(), &canceled); err != nil {
				t.Fatalf("cancel response JSON error = %v", err)
			}
			if canceled.CancelReason != "" {
				t.Fatalf(
					"omitted cancel reason = %q, want empty",
					canceled.CancelReason,
				)
			}
		})
	}
}

func createCancelableWorkflowRun(
	t *testing.T,
	workspace string,
	runID string,
) *workflows.Run {
	t.Helper()
	now := time.Now().UTC()
	run := &workflows.Run{
		ID:          runID,
		WorkflowRef: "workflows/cancel.yml",
		Status:      workflows.RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := workflows.NewFileRunStore(workspace).CreateRun(
		context.Background(),
		run,
	); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	return run
}

func cancelWorkflowRunRequest(
	t *testing.T,
	handler *Handler,
	runID string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/runs/"+runID+"/cancel",
		strings.NewReader(body),
	)
	request.SetPathValue("run_id", runID)
	handler.handleCancelWorkflowRun(recorder, request)
	return recorder
}
