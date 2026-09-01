package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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
			name:       "top-level null",
			body:       `null`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_cancel_request",
		},
		{
			name:       "top-level array",
			body:       `[]`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_cancel_request",
		},
		{
			name:       "top-level string",
			body:       `"stop"`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_cancel_request",
		},
		{
			name:       "top-level boolean",
			body:       `true`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_cancel_request",
		},
		{
			name:       "top-level number",
			body:       `1`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_cancel_request",
		},
		{
			name:       "whitespace only",
			body:       " \n\t ",
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
			name:       "invalid UTF-8",
			body:       `{"reason":"` + string([]byte{0xff}) + `"}`,
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

func TestWorkflowRunHTTPProjectionDropsUntrustedOriginLineage(t *testing.T) {
	const (
		eventID    = "ev_0123456789abcdef0123456789abcdef"
		dispatchID = "dsp_0123456789abcdef0123456789abcdef"
	)
	origin := func(rootRunID string) *workflows.RunOrigin {
		return &workflows.RunOrigin{
			Kind:       workflows.RunOriginExternalEvent,
			EventID:    eventID,
			DispatchID: dispatchID,
			RootRunID:  rootRunID,
		}
	}
	event := func() map[string]any {
		return map[string]any{
			"id":        eventID,
			"source":    "github",
			"connector": "primary",
			"type":      "issues.opened",
		}
	}
	inputs := func() map[string]any {
		return map[string]any{
			"event_id":    eventID,
			"dispatch_id": dispatchID,
		}
	}
	tests := []struct {
		name       string
		runs       []*workflows.Run
		id         string
		wantOrigin *workflows.RunOrigin
	}{
		{
			name:       "missing retry source",
			id:         "wr_missing_retry",
			wantOrigin: origin("wr_missing_root"),
			runs: []*workflows.Run{
				{
					ID:           "wr_missing_retry",
					WorkflowRef:  "workflows/missing.yml",
					Status:       workflows.RunStatusRunning,
					RetryOfRunID: "wr_missing_root",
					Event:        event(),
					Inputs:       inputs(),
					Origin:       origin("wr_missing_root"),
				},
			},
		},
		{
			name: "cyclic parent lineage",
			id:   "wr_cycle_a",
			runs: []*workflows.Run{
				{
					ID:          "wr_cycle_a",
					WorkflowRef: "workflows/cycle.yml",
					Status:      workflows.RunStatusRunning,
					ParentRunID: "wr_cycle_b",
					Event:       event(),
					Origin:      origin("wr_cycle_root"),
				},
				{
					ID:          "wr_cycle_b",
					WorkflowRef: "workflows/cycle.yml",
					Status:      workflows.RunStatusRunning,
					ParentRunID: "wr_cycle_a",
					Event:       event(),
					Origin:      origin("wr_cycle_root"),
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
			store := workflows.NewFileRunStore(workspace)
			now := time.Now().UTC()
			for _, run := range test.runs {
				run.CreatedAt = now
				run.UpdatedAt = now
				if err := store.CreateRun(context.Background(), run); err != nil {
					t.Fatalf("CreateRun(%s) error = %v", run.ID, err)
				}
			}

			detail := httptest.NewRecorder()
			detailRequest := httptest.NewRequest(
				http.MethodGet,
				"/api/workflows/runs/"+test.id,
				nil,
			)
			detailRequest.SetPathValue("run_id", test.id)
			handler.handleGetWorkflowRun(detail, detailRequest)
			assertWorkflowRunResponseOrigin(t, detail, test.wantOrigin)

			canceled := cancelWorkflowRunRequest(t, handler, test.id, `{}`)
			assertWorkflowRunResponseOrigin(t, canceled, test.wantOrigin)
		})
	}
}

func TestWorkflowRunListHTTPProjectionDistinguishesPrunedAndUnreadableAncestor(
	t *testing.T,
) {
	const (
		eventID           = "ev_0123456789abcdef0123456789abcdef"
		dispatchID        = "dsp_0123456789abcdef0123456789abcdef"
		rootID            = "wr_list_origin_root"
		childID           = "wr_list_origin_child"
		privateDiagnostic = "retained private production diagnostic"
	)
	origin := &workflows.RunOrigin{
		Kind:       workflows.RunOriginExternalEvent,
		EventID:    eventID,
		DispatchID: dispatchID,
		RootRunID:  rootID,
	}
	event := map[string]any{
		"id":        eventID,
		"source":    "github",
		"connector": "primary",
		"type":      "issues.opened",
	}
	for _, test := range []struct {
		name       string
		corrupt    bool
		wantOrigin *workflows.RunOrigin
		wantError  string
	}{
		{
			name:       "missing ancestor is a retention boundary",
			wantOrigin: origin,
			wantError:  privateDiagnostic,
		},
		{
			name:      "unreadable retained ancestor is untrusted",
			corrupt:   true,
			wantError: workflows.EventBackedDraftRunErrorDiagnostic,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
			store := workflows.NewFileRunStore(workspace)
			now := time.Now().UTC()
			if test.corrupt {
				if err := store.CreateRun(ctx, &workflows.Run{
					ID:          rootID,
					WorkflowRef: "workflows/root.yml",
					Status:      workflows.RunStatusSucceeded,
					Event:       event,
					Inputs: map[string]any{
						"event_id":    eventID,
						"dispatch_id": dispatchID,
					},
					Origin:    origin,
					CreatedAt: now,
					UpdatedAt: now,
				}); err != nil {
					t.Fatalf("CreateRun(root) error = %v", err)
				}
			}
			if err := store.CreateRun(ctx, &workflows.Run{
				ID:          childID,
				WorkflowRef: "workflows/child.yml",
				Status:      workflows.RunStatusSucceeded,
				ParentRunID: rootID,
				Event:       event,
				Origin:      origin,
				Error:       privateDiagnostic,
				CreatedAt:   now.Add(time.Second),
				UpdatedAt:   now.Add(time.Second),
			}); err != nil {
				t.Fatalf("CreateRun(child) error = %v", err)
			}
			if test.corrupt {
				db, err := sql.Open("sqlite", filepath.Join(workspace, "state", "workflows.db"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE workflow_run_payloads SET event_json=? WHERE run_id=?`,
					[]byte(`{"id":`), rootID); err != nil {
					db.Close()
					t.Fatalf("corrupt ancestor run: %v", err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodGet,
				"/api/workflows/runs",
				nil,
			)
			handler.handleListWorkflowRuns(recorder, request)
			if test.corrupt {
				if recorder.Code != http.StatusInternalServerError {
					t.Fatalf("corrupt workflow store status = %d, body=%s", recorder.Code, recorder.Body.String())
				}
				return
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf(
					"workflow run list status = %d, body=%s",
					recorder.Code,
					recorder.Body.String(),
				)
			}
			var response struct {
				Runs []workflowRunCollectionSummary `json:"runs"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("workflow run list JSON error = %v", err)
			}
			var listedChild *workflowRunCollectionSummary
			for index := range response.Runs {
				if response.Runs[index].ID == childID {
					listedChild = &response.Runs[index]
					break
				}
			}
			if listedChild == nil {
				t.Fatalf("workflow run list = %#v, want child", response.Runs)
			}
			if !reflect.DeepEqual(listedChild.Origin, test.wantOrigin) {
				t.Fatalf(
					"listed child origin = %#v, want %#v",
					listedChild.Origin,
					test.wantOrigin,
				)
			}
			if strings.Contains(recorder.Body.String(), `"error"`) {
				t.Fatalf("workflow run list leaked diagnostic: %s", recorder.Body.String())
			}
			detail := httptest.NewRecorder()
			detailRequest := httptest.NewRequest(
				http.MethodGet,
				"/api/workflows/runs/"+childID,
				nil,
			)
			detailRequest.SetPathValue("run_id", childID)
			handler.handleGetWorkflowRun(detail, detailRequest)
			if detail.Code != http.StatusOK {
				t.Fatalf("workflow run detail = %d %s", detail.Code, detail.Body.String())
			}
			var detailed workflows.Run
			if err := json.Unmarshal(detail.Body.Bytes(), &detailed); err != nil {
				t.Fatal(err)
			}
			if detailed.Error != test.wantError {
				t.Fatalf(
					"detailed child error = %q, want %q",
					detailed.Error,
					test.wantError,
				)
			}
		})
	}
}

func assertWorkflowRunResponseOrigin(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	want *workflows.RunOrigin,
) {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("workflow run status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var run workflows.Run
	if err := json.Unmarshal(recorder.Body.Bytes(), &run); err != nil {
		t.Fatalf("workflow run response JSON error = %v", err)
	}
	if !reflect.DeepEqual(run.Origin, want) {
		t.Fatalf("projected origin = %#v, want %#v", run.Origin, want)
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
