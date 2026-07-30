package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowTriggerReviewedManualYAML = `name: Reviewed manual
on:
  manual: {}
jobs:
  inspect:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with:
          action: get
          key: reviewed-missing
`

const workflowTriggerAllFamiliesYAML = `name: Reviewed trigger families
on:
  manual: {}
  schedule:
    - cron: "0 * * * *"
  channel_message:
    channels: telegram
    passthrough: true
  command:
    name: deploy
    channels: telegram
  runtime_event:
    kinds: agent.turn.end
    sources: agent/main
  event:
    sources: github
    types: issues.opened
  workflow_call: {}
jobs:
  inspect:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with:
          action: get
          key: reviewed-missing
`

func TestWorkflowTriggerSimulationRoutesStrictlyDecodeExactRequests(t *testing.T) {
	handler := NewHandler(writeWorkflowAITestConfig(t, t.TempDir()))
	mux := http.NewServeMux()
	handler.registerWorkflowEditorRoutes(mux)

	valid := `{
		"session_id":"session-1",
		"expected_session_revision":"sha256:session",
		"expected_draft_revision":"sha256:draft",
		"prompt":"draft",
		"target_ref":"workflows/demo.yml",
		"yaml":"on:\n  manual: {}\njobs: {}\n",
		"trigger":{"type":"manual"},
		"scenario":{"inputs":{},"secrets":{},"session":"","delivery":{}}
	}`
	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
		wantStatus  int
		wantCode    string
	}{
		{
			name:        "valid simulation reaches handler",
			path:        "/api/workflows/development/triggers/simulate",
			contentType: "application/json",
			body:        valid,
			wantStatus:  http.StatusNotFound,
			wantCode:    "workflow_development_session_not_found",
		},
		{
			name:        "valid execute reaches review gate",
			path:        "/api/workflows/development/test/execute",
			contentType: "application/json; charset=utf-8",
			body: strings.TrimSuffix(valid, "\n\t}") +
				`,"review_token":"token"}`,
			wantStatus: http.StatusNotFound,
			wantCode:   "workflow_development_session_not_found",
		},
		{
			name:       "missing content type",
			path:       "/api/workflows/development/triggers/simulate",
			body:       valid,
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   "invalid_workflow_trigger_simulation_request",
		},
		{
			name:        "unknown top level field",
			path:        "/api/workflows/development/triggers/simulate",
			contentType: "application/json",
			body: strings.TrimSuffix(valid, "\n\t}") +
				`,"extra":true}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_workflow_trigger_simulation_request",
		},
		{
			name:        "missing exact field",
			path:        "/api/workflows/development/triggers/simulate",
			contentType: "application/json",
			body:        strings.Replace(valid, `"prompt":"draft",`, "", 1),
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_workflow_trigger_simulation_request",
		},
		{
			name:        "required prompt null",
			path:        "/api/workflows/development/triggers/simulate",
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"prompt":"draft"`,
				`"prompt":null`,
				1,
			),
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_workflow_trigger_simulation_request",
		},
		{
			name:        "scenario null",
			path:        "/api/workflows/development/triggers/simulate",
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"scenario":{"inputs":{},"secrets":{},"session":"","delivery":{}}`,
				`"scenario":null`,
				1,
			),
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_workflow_trigger_simulation_request",
		},
		{
			name:        "optional selector field null",
			path:        "/api/workflows/development/triggers/simulate",
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"trigger":{"type":"manual"}`,
				`"trigger":{"type":"manual","schedule_index":null}`,
				1,
			),
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_workflow_trigger_simulation_request",
		},
		{
			name:        "duplicate nested field",
			path:        "/api/workflows/development/triggers/simulate",
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"trigger":{"type":"manual"}`,
				`"trigger":{"type":"manual","type":"event"}`,
				1,
			),
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_workflow_trigger_simulation_request",
		},
		{
			name:        "trailing value",
			path:        "/api/workflows/development/triggers/simulate",
			contentType: "application/json",
			body:        valid + `{}`,
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_workflow_trigger_simulation_request",
		},
		{
			name:        "empty execute token",
			path:        "/api/workflows/development/test/execute",
			contentType: "application/json",
			body: strings.TrimSuffix(valid, "\n\t}") +
				`,"review_token":""}`,
			wantStatus: http.StatusForbidden,
			wantCode:   "workflow_trigger_review_required",
		},
		{
			name:        "execute token null",
			path:        "/api/workflows/development/test/execute",
			contentType: "application/json",
			body: strings.TrimSuffix(valid, "\n\t}") +
				`,"review_token":null}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_workflow_trigger_simulation_request",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				test.path,
				strings.NewReader(test.body),
			)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d; body=%s",
					response.Code,
					test.wantStatus,
					response.Body.String(),
				)
			}
			assertWorkflowTriggerSimulationErrorCode(
				t,
				response,
				test.wantCode,
			)
			if response.Header().Get("Cache-Control") != "no-store" ||
				response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("security headers = %#v", response.Header())
			}
		})
	}
}

func TestWorkflowTriggerSimulationRoutesRejectDecodedStringBoundsWithoutMutation(
	t *testing.T,
) {
	tests := []struct {
		name     string
		scenario json.RawMessage
		prompt   string
	}{
		{
			name: "overlong nested key",
			scenario: json.RawMessage(
				`{"inputs":{"` +
					strings.Repeat(`\u006b`, workflowTriggerJSONKeyMaxBytes+1) +
					`":"ok"}}`,
			),
			prompt: "reviewed trigger draft",
		},
		{
			name: "overlong nested value",
			scenario: json.RawMessage(
				`{"inputs":{"key":"` +
					strings.Repeat(
						`\u0076`,
						workflowTriggerJSONStringMaxBytes+1,
					) +
					`"}}`,
			),
			prompt: "reviewed trigger draft",
		},
		{
			name:     "overlong top level prompt",
			scenario: json.RawMessage(`{}`),
			prompt: strings.Repeat(
				"p",
				workflowTriggerJSONStringMaxBytes+1,
			),
		},
	}
	routes := []struct {
		name string
		path string
	}{
		{
			name: "simulate",
			path: "/api/workflows/development/triggers/simulate",
		},
		{
			name: "execute",
			path: "/api/workflows/development/test/execute",
		},
	}

	for _, test := range tests {
		for _, route := range routes {
			t.Run(test.name+"/"+route.name, func(t *testing.T) {
				workspace, _, mux, session := newWorkflowTriggerAPIHarness(t)
				before := readWorkflowTriggerActiveSessionBytes(t, workspace)
				simulationRequest := workflowTriggerTestRequest(
					session,
					workflowTriggerReviewedManualYAML,
					workflows.WorkflowTriggerManual,
					test.scenario,
				)
				simulationRequest.Prompt = test.prompt

				var request any = simulationRequest
				if route.name == "execute" {
					request = workflowTriggerExecutionFromSimulation(
						simulationRequest,
						"review-token",
					)
				}
				body, err := json.Marshal(request)
				if err != nil {
					t.Fatalf("marshal bounded request: %v", err)
				}
				if len(body) > workflowTriggerSimulationRequestMaxBytes {
					t.Fatalf(
						"test request = %d bytes, want at most %d",
						len(body),
						workflowTriggerSimulationRequestMaxBytes,
					)
				}

				response := postWorkflowTriggerJSON(
					t,
					mux,
					route.path,
					request,
				)
				if response.Code != http.StatusBadRequest {
					t.Fatalf(
						"status = %d, want 400; body=%s",
						response.Code,
						response.Body.String(),
					)
				}
				assertWorkflowTriggerSimulationErrorCode(
					t,
					response,
					"invalid_workflow_trigger_simulation_request",
				)
				assertWorkflowTriggerSecurityHeaders(t, response)
				after := readWorkflowTriggerActiveSessionBytes(t, workspace)
				if !bytes.Equal(before, after) {
					t.Fatalf(
						"rejected request mutated active session\nbefore=%s\nafter=%s",
						before,
						after,
					)
				}
				assertWorkflowTriggerNoRuns(t, workspace)
			})
		}
	}
}

func TestWorkflowTriggerSimulationRejectsLegacyConfigWithoutMigration(
	t *testing.T,
) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	securityPath := filepath.Join(dir, config.SecurityConfigFile)
	publicBefore := []byte(`{"version":2,"legacy":"configuration-canary"}`)
	securityBefore := []byte("legacy-security-canary")
	if err := os.WriteFile(configPath, publicBefore, 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	if err := os.WriteFile(securityPath, securityBefore, 0o600); err != nil {
		t.Fatalf("WriteFile(security) error = %v", err)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.registerWorkflowEditorRoutes(mux)
	request := workflowTriggerSimulationRequest{
		SessionID:               "session-1",
		ExpectedSessionRevision: "sha256:session",
		ExpectedDraftRevision:   "sha256:draft",
		Prompt:                  "draft",
		TargetRef:               "workflows/demo.yml",
		YAML:                    workflowTriggerReviewedManualYAML,
		Trigger: workflowTriggerRequestWire{
			Type: workflows.WorkflowTriggerManual,
		},
		Scenario: json.RawMessage(`{}`),
	}

	response := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/simulate",
		request,
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	assertWorkflowTriggerSimulationErrorCode(
		t,
		response,
		"workflow_trigger_simulation_unavailable",
	)
	publicAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	securityAfter, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatalf("ReadFile(security) error = %v", err)
	}
	if string(publicAfter) != string(publicBefore) ||
		string(securityAfter) != string(securityBefore) {
		t.Fatalf(
			"simulation migrated config: public=%q security=%q",
			publicAfter,
			securityAfter,
		)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "*.bak"))
	if err != nil {
		t.Fatalf("Glob(backups) error = %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("simulation created config backups: %v", backups)
	}
}

func TestWorkflowTriggerSimulationIsSideEffectFreeAndPrivate(t *testing.T) {
	workspace, _, mux, session := newWorkflowTriggerAPIHarness(t)
	before := readWorkflowTriggerActiveSessionBytes(t, workspace)
	request := workflowTriggerTestRequest(
		session,
		workflowTriggerReviewedManualYAML,
		workflows.WorkflowTriggerManual,
		json.RawMessage(`{
			"inputs":{"issue":7},
			"secrets":{"token":"PRIVATE_SIMULATION_SECRET"},
			"session":"PRIVATE_SIMULATION_SESSION",
			"delivery":{"channel":"telegram","chat_id":"PRIVATE_SIMULATION_CHAT"}
		}`),
	)

	response := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/simulate",
		request,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	for _, private := range []string{
		"PRIVATE_SIMULATION_SECRET",
		"PRIVATE_SIMULATION_SESSION",
		"PRIVATE_SIMULATION_CHAT",
	} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatalf("simulation response exposed %q: %s", private, response.Body.String())
		}
	}
	var result workflowTriggerSimulationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode simulation response: %v", err)
	}
	if !result.Simulation.Executable ||
		result.Simulation.Reason != workflows.WorkflowTriggerSimulationMatched ||
		result.Simulation.ContextSummary.InputCount != 1 ||
		result.Simulation.ContextSummary.SecretCount != 1 ||
		!result.Simulation.ContextSummary.HasSession ||
		!result.Simulation.ContextSummary.HasDelivery ||
		result.ReviewToken == "" {
		t.Fatalf("simulation response = %#v", result)
	}
	if result.Review.Targets == nil ||
		result.Review.Effects == nil ||
		result.Review.Limits == nil {
		t.Fatalf("review arrays must be non-null: %#v", result.Review)
	}
	after := readWorkflowTriggerActiveSessionBytes(t, workspace)
	if string(after) != string(before) {
		t.Fatalf("simulation mutated active session\nbefore=%s\nafter=%s", before, after)
	}
	assertWorkflowTriggerNoRuns(t, workspace)
	assertWorkflowTriggerSecurityHeaders(t, response)
}

func TestWorkflowTriggerSimulationRejectsCrossFamilyNullAndUnsafeScenarios(t *testing.T) {
	workspace, _, mux, session := newWorkflowTriggerAPIHarness(t)
	before := readWorkflowTriggerActiveSessionBytes(t, workspace)
	scheduleIndex := 0
	unsafeScheduleIndex := int(9007199254740992)
	tests := []struct {
		name     string
		kind     workflows.WorkflowTriggerKind
		index    *int
		scenario json.RawMessage
	}{
		{
			name:     "manual rejects schedule",
			kind:     workflows.WorkflowTriggerManual,
			scenario: json.RawMessage(`{"scheduled_at":"2026-07-30T12:00:00Z"}`),
		},
		{
			name:     "workflow call rejects message",
			kind:     workflows.WorkflowTriggerWorkflowCall,
			scenario: json.RawMessage(`{"message":{}}`),
		},
		{
			name:     "schedule rejects message",
			kind:     workflows.WorkflowTriggerSchedule,
			index:    &scheduleIndex,
			scenario: json.RawMessage(`{"message":{}}`),
		},
		{
			name:     "channel message rejects runtime event",
			kind:     workflows.WorkflowTriggerChannelMessage,
			scenario: json.RawMessage(`{"event":{}}`),
		},
		{
			name:     "command rejects invocation",
			kind:     workflows.WorkflowTriggerCommand,
			scenario: json.RawMessage(`{"inputs":{}}`),
		},
		{
			name:     "runtime event rejects durable event id",
			kind:     workflows.WorkflowTriggerRuntimeEvent,
			scenario: json.RawMessage(`{"event_id":"` + testEventID + `"}`),
		},
		{
			name:     "durable event rejects runtime object",
			kind:     workflows.WorkflowTriggerEvent,
			scenario: json.RawMessage(`{"event":{}}`),
		},
		{
			name:     "manual inputs typed null",
			kind:     workflows.WorkflowTriggerManual,
			scenario: json.RawMessage(`{"inputs":null}`),
		},
		{
			name:     "manual delivery nested null",
			kind:     workflows.WorkflowTriggerManual,
			scenario: json.RawMessage(`{"delivery":{"channel":null}}`),
		},
		{
			name:     "message object null",
			kind:     workflows.WorkflowTriggerChannelMessage,
			scenario: json.RawMessage(`{"message":null}`),
		},
		{
			name:     "message leaf null",
			kind:     workflows.WorkflowTriggerChannelMessage,
			scenario: json.RawMessage(`{"message":{"text":null}}`),
		},
		{
			name:     "schedule value null",
			kind:     workflows.WorkflowTriggerSchedule,
			index:    &scheduleIndex,
			scenario: json.RawMessage(`{"scheduled_at":null}`),
		},
		{
			name:     "schedule index outside browser safe range",
			kind:     workflows.WorkflowTriggerSchedule,
			index:    &unsafeScheduleIndex,
			scenario: json.RawMessage(`{"scheduled_at":"2026-07-30T12:00:00Z"}`),
		},
		{
			name:     "runtime event object null",
			kind:     workflows.WorkflowTriggerRuntimeEvent,
			scenario: json.RawMessage(`{"event":null}`),
		},
		{
			name:     "runtime typed nested null",
			kind:     workflows.WorkflowTriggerRuntimeEvent,
			scenario: json.RawMessage(`{"event":{"source":{"component":null}}}`),
		},
		{
			name:     "durable event id null",
			kind:     workflows.WorkflowTriggerEvent,
			scenario: json.RawMessage(`{"event_id":null}`),
		},
		{
			name:     "invocation unsafe integer",
			kind:     workflows.WorkflowTriggerManual,
			scenario: json.RawMessage(`{"inputs":{"count":9007199254740993}}`),
		},
		{
			name:     "invocation inexact float",
			kind:     workflows.WorkflowTriggerManual,
			scenario: json.RawMessage(`{"inputs":{"count":0.10000000000000001}}`),
		},
		{
			name: "runtime event unsafe payload",
			kind: workflows.WorkflowTriggerRuntimeEvent,
			scenario: json.RawMessage(`{"event":{
				"id":"runtime-1",
				"kind":"agent.turn.end",
				"time":"2026-07-30T12:00:00Z",
				"source":{"component":"agent","name":"main"},
				"payload":{"count":9007199254740993}
			}}`),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := workflowTriggerTestRequest(
				session,
				workflowTriggerAllFamiliesYAML,
				test.kind,
				test.scenario,
			)
			request.Trigger.ScheduleIndex = test.index
			response := postWorkflowTriggerJSON(
				t,
				mux,
				"/api/workflows/development/triggers/simulate",
				request,
			)
			if response.Code != http.StatusBadRequest {
				t.Fatalf(
					"status = %d, want 400; body=%s",
					response.Code,
					response.Body.String(),
				)
			}
			assertWorkflowTriggerSimulationErrorCode(
				t,
				response,
				"invalid_workflow_trigger_simulation_request",
			)
		})
	}
	after := readWorkflowTriggerActiveSessionBytes(t, workspace)
	if string(after) != string(before) {
		t.Fatalf("rejected scenarios mutated session\nbefore=%s\nafter=%s", before, after)
	}
	assertWorkflowTriggerNoRuns(t, workspace)
}

func TestWorkflowTriggerSimulationDoesNotEchoMessageOrRuntimePayload(t *testing.T) {
	workspace, _, mux, session := newWorkflowTriggerAPIHarness(t)
	before := readWorkflowTriggerActiveSessionBytes(t, workspace)
	runtimeEvent := runtimeevents.Event{
		ID:   "PRIVATE_RUNTIME_ID",
		Kind: runtimeevents.KindAgentTurnEnd,
		Time: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		Source: runtimeevents.Source{
			Component: "agent",
			Name:      "main",
		},
		Scope: runtimeevents.Scope{
			SessionKey: "PRIVATE_RUNTIME_SESSION",
			SenderID:   "PRIVATE_RUNTIME_SENDER",
		},
		Payload: map[string]any{"private": "PRIVATE_RUNTIME_PAYLOAD"},
	}
	runtimeScenario, err := json.Marshal(map[string]any{"event": runtimeEvent})
	if err != nil {
		t.Fatalf("marshal runtime scenario: %v", err)
	}
	tests := []struct {
		name     string
		kind     workflows.WorkflowTriggerKind
		scenario json.RawMessage
		private  []string
	}{
		{
			name: "message",
			kind: workflows.WorkflowTriggerChannelMessage,
			scenario: json.RawMessage(`{"message":{
				"channel":"telegram",
				"chat_id":"PRIVATE_MESSAGE_CHAT",
				"sender_id":"PRIVATE_MESSAGE_SENDER",
				"text":"PRIVATE_MESSAGE_TEXT"
			}}`),
			private: []string{
				"PRIVATE_MESSAGE_CHAT",
				"PRIVATE_MESSAGE_SENDER",
				"PRIVATE_MESSAGE_TEXT",
			},
		},
		{
			name:     "runtime",
			kind:     workflows.WorkflowTriggerRuntimeEvent,
			scenario: runtimeScenario,
			private: []string{
				"PRIVATE_RUNTIME_ID",
				"PRIVATE_RUNTIME_SESSION",
				"PRIVATE_RUNTIME_SENDER",
				"PRIVATE_RUNTIME_PAYLOAD",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := postWorkflowTriggerJSON(
				t,
				mux,
				"/api/workflows/development/triggers/simulate",
				workflowTriggerTestRequest(
					session,
					workflowTriggerAllFamiliesYAML,
					test.kind,
					test.scenario,
				),
			)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
			}
			for _, private := range test.private {
				if strings.Contains(response.Body.String(), private) {
					t.Fatalf("response exposed %q: %s", private, response.Body.String())
				}
			}
		})
	}
	after := readWorkflowTriggerActiveSessionBytes(t, workspace)
	if string(after) != string(before) {
		t.Fatalf("simulations mutated session\nbefore=%s\nafter=%s", before, after)
	}
	assertWorkflowTriggerNoRuns(t, workspace)
}

func TestWorkflowTriggerExecutionRejectsMissingTamperedExpiredAndStaleWithoutMutation(
	t *testing.T,
) {
	fixedNow := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		before     func(*Handler)
		mutate     func(*Handler, *workflowTriggerExecutionRequest)
		wantStatus int
		wantCode   string
	}{
		{
			name: "missing token",
			mutate: func(
				_ *Handler,
				request *workflowTriggerExecutionRequest,
			) {
				request.ReviewToken = ""
			},
			wantStatus: http.StatusForbidden,
			wantCode:   "workflow_trigger_review_required",
		},
		{
			name: "tampered token",
			mutate: func(
				_ *Handler,
				request *workflowTriggerExecutionRequest,
			) {
				request.ReviewToken = tamperWorkflowTriggerReviewToken(
					request.ReviewToken,
				)
			},
			wantStatus: http.StatusForbidden,
			wantCode:   "workflow_trigger_review_invalid",
		},
		{
			name: "request changed after review",
			mutate: func(
				_ *Handler,
				request *workflowTriggerExecutionRequest,
			) {
				request.Prompt += " changed"
			},
			wantStatus: http.StatusForbidden,
			wantCode:   "workflow_trigger_review_invalid",
		},
		{
			name: "expired token",
			before: func(handler *Handler) {
				handler.workflowTriggerReviewNow = func() time.Time {
					return fixedNow
				}
			},
			mutate: func(
				handler *Handler,
				_ *workflowTriggerExecutionRequest,
			) {
				handler.workflowTriggerReviewNow = func() time.Time {
					return fixedNow.Add(workflowTriggerReviewTTL)
				}
			},
			wantStatus: http.StatusForbidden,
			wantCode:   "workflow_trigger_review_invalid",
		},
		{
			name: "stale draft fence",
			mutate: func(
				_ *Handler,
				request *workflowTriggerExecutionRequest,
			) {
				request.ExpectedDraftRevision = "sha256:stale"
			},
			wantStatus: http.StatusConflict,
			wantCode:   "workflow_development_fence_mismatch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, handler, mux, session := newWorkflowTriggerAPIHarness(t)
			if test.before != nil {
				test.before(handler)
			}
			simulationRequest, token := simulateWorkflowTriggerReviewedManual(
				t,
				mux,
				session,
			)
			execution := workflowTriggerExecutionFromSimulation(
				simulationRequest,
				token,
			)
			if test.mutate != nil {
				test.mutate(handler, &execution)
			}
			before := readWorkflowTriggerActiveSessionBytes(t, workspace)
			response := postWorkflowTriggerJSON(
				t,
				mux,
				"/api/workflows/development/test/execute",
				execution,
			)
			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d; body=%s",
					response.Code,
					test.wantStatus,
					response.Body.String(),
				)
			}
			assertWorkflowTriggerSimulationErrorCode(
				t,
				response,
				test.wantCode,
			)
			after := readWorkflowTriggerActiveSessionBytes(t, workspace)
			if string(after) != string(before) {
				t.Fatalf(
					"rejected execution mutated session\nbefore=%s\nafter=%s",
					before,
					after,
				)
			}
			assertWorkflowTriggerNoRuns(t, workspace)
			assertWorkflowTriggerSecurityHeaders(t, response)
		})
	}
}

func TestWorkflowTriggerExecutionDisabledLeavesDraftUnchanged(t *testing.T) {
	workspace, handler, mux, session := newWorkflowTriggerAPIHarness(t)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Workflows.Enabled = false
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	simulationRequest, token := simulateWorkflowTriggerReviewedManual(
		t,
		mux,
		session,
	)
	before := readWorkflowTriggerActiveSessionBytes(t, workspace)
	response := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		workflowTriggerExecutionFromSimulation(simulationRequest, token),
	)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	assertWorkflowTriggerSimulationErrorCode(
		t,
		response,
		"workflow_trigger_execution_disabled",
	)
	after := readWorkflowTriggerActiveSessionBytes(t, workspace)
	if string(after) != string(before) {
		t.Fatalf("disabled execution mutated session\nbefore=%s\nafter=%s", before, after)
	}
	assertWorkflowTriggerNoRuns(t, workspace)
}

func TestWorkflowTriggerExecutionRejectsNonExecutableReviewWithoutMutation(
	t *testing.T,
) {
	workspace, _, mux, session := newWorkflowTriggerAPIHarness(t)
	request := workflowTriggerTestRequest(
		session,
		`name: No manual trigger
on:
  workflow_call: {}
jobs:
  inspect:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with:
          action: get
          key: missing
`,
		workflows.WorkflowTriggerManual,
		json.RawMessage(`{}`),
	)
	before := readWorkflowTriggerActiveSessionBytes(t, workspace)
	response := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		workflowTriggerExecutionFromSimulation(request, "untrusted-token"),
	)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	assertWorkflowTriggerSimulationErrorCode(
		t,
		response,
		"workflow_trigger_not_executable",
	)
	after := readWorkflowTriggerActiveSessionBytes(t, workspace)
	if string(after) != string(before) {
		t.Fatalf("non-executable request mutated session\nbefore=%s\nafter=%s", before, after)
	}
	assertWorkflowTriggerNoRuns(t, workspace)
}

func TestWorkflowTriggerExecutionStartFailureLeavesCandidateUnapplied(
	t *testing.T,
) {
	workspace, handler, mux, session := newWorkflowTriggerAPIHarness(t)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Workflows.MaxConcurrentRuns = 1
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	now := time.Now().UTC()
	store := workflows.NewFileRunStore(workspace)
	if err := store.CreateRun(context.Background(), &workflows.Run{
		ID:          "wr_existing_capacity",
		WorkflowRef: "workflows/existing.yml",
		Status:      workflows.RunStatusRunning,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("CreateRun(existing) error = %v", err)
	}
	simulationRequest, token := simulateWorkflowTriggerReviewedManual(
		t,
		mux,
		session,
	)
	before := readWorkflowTriggerActiveSessionBytes(t, workspace)
	response := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		workflowTriggerExecutionFromSimulation(simulationRequest, token),
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	assertWorkflowTriggerSimulationErrorCode(
		t,
		response,
		"workflow_trigger_execution_unavailable",
	)
	after := readWorkflowTriggerActiveSessionBytes(t, workspace)
	if string(after) != string(before) {
		t.Fatalf("start failure mutated candidate\nbefore=%s\nafter=%s", before, after)
	}
	runs, err := store.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "wr_existing_capacity" {
		t.Fatalf("start failure created a run: %#v", runs)
	}
}

func TestWorkflowTriggerDegradedClaimConsumesReviewAndPreventsReplay(
	t *testing.T,
) {
	workspace, handler, mux, session := newWorkflowTriggerAPIHarness(t)
	completed := make(chan struct{})
	handler.workflowDevelopmentTestDone = func() {
		close(completed)
	}
	simulationRequest, token := simulateWorkflowTriggerReviewedManual(
		t,
		mux,
		session,
	)
	previousAdmission := admitWorkflowTriggerDevelopmentTestRun
	t.Cleanup(func() {
		admitWorkflowTriggerDevelopmentTestRun = previousAdmission
	})
	admissionCalls := 0
	admitWorkflowTriggerDevelopmentTestRun = func(
		workspace string,
		_ workflows.WorkflowDevelopmentTestRunAdmission,
		start func() (backgroundWorkflowStart, error),
		_ ...workflows.LocalOption,
	) (
		*workflows.WorkflowDevelopmentSession,
		bool,
		backgroundWorkflowStart,
		error,
	) {
		admissionCalls++
		started, err := start()
		if err != nil {
			return nil, false, started, err
		}
		current, getErr := workflows.GetWorkflowDevelopmentSession(workspace)
		if getErr != nil {
			return nil, false, started, getErr
		}
		return current, false, started, errors.New(
			"injected active-session persistence failure",
		)
	}

	execution := workflowTriggerExecutionFromSimulation(
		simulationRequest,
		token,
	)
	first := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		execution,
	)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d; body=%s", first.Code, first.Body.String())
	}
	var accepted struct {
		Result         *workflows.RunResult                   `json:"result"`
		Reconciliation *workflowDevelopmentTestReconciliation `json:"reconciliation"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if accepted.Result == nil ||
		accepted.Reconciliation == nil ||
		accepted.Reconciliation.Reason !=
			"draft_test_snapshot_not_recorded" {
		t.Fatalf("first response = %#v", accepted)
	}
	waitForWorkflowTriggerTestCompletion(t, completed)

	for name, alias := range equivalentWorkflowTriggerReviewTokenAliases(
		t,
		token,
	) {
		aliasedExecution := execution
		aliasedExecution.ReviewToken = alias
		replayed := postWorkflowTriggerJSON(
			t,
			mux,
			"/api/workflows/development/test/execute",
			aliasedExecution,
		)
		if replayed.Code != http.StatusForbidden {
			t.Fatalf(
				"%s alias status = %d; body=%s",
				name,
				replayed.Code,
				replayed.Body.String(),
			)
		}
		assertWorkflowTriggerSimulationErrorCode(
			t,
			replayed,
			"workflow_trigger_review_invalid",
		)
	}

	second := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		execution,
	)
	if second.Code != http.StatusConflict {
		t.Fatalf("replay status = %d; body=%s", second.Code, second.Body.String())
	}
	assertWorkflowTriggerSimulationErrorCode(
		t,
		second,
		"workflow_trigger_review_replayed",
	)
	if admissionCalls != 1 {
		t.Fatalf("admission calls = %d, want 1", admissionCalls)
	}
	runs, err := workflows.NewFileRunStore(workspace).ListRuns(
		context.Background(),
	)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].ID != accepted.Result.RunID {
		t.Fatalf("replay created duplicate runs: %#v", runs)
	}
}

func TestWorkflowTriggerDurableAcceptanceWithoutSessionStillReturns202(
	t *testing.T,
) {
	workspace, handler, mux, session := newWorkflowTriggerAPIHarness(t)
	completed := make(chan struct{})
	handler.workflowDevelopmentTestDone = func() {
		close(completed)
	}
	simulationRequest, token := simulateWorkflowTriggerReviewedManual(
		t,
		mux,
		session,
	)
	previousAdmission := admitWorkflowTriggerDevelopmentTestRun
	t.Cleanup(func() {
		admitWorkflowTriggerDevelopmentTestRun = previousAdmission
	})
	admitWorkflowTriggerDevelopmentTestRun = func(
		_ string,
		_ workflows.WorkflowDevelopmentTestRunAdmission,
		start func() (backgroundWorkflowStart, error),
		_ ...workflows.LocalOption,
	) (
		*workflows.WorkflowDevelopmentSession,
		bool,
		backgroundWorkflowStart,
		error,
	) {
		started, err := start()
		if err != nil {
			return nil, false, started, err
		}
		return nil, false, started, errors.New(
			"injected accepted-session read failure",
		)
	}

	response := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		workflowTriggerExecutionFromSimulation(simulationRequest, token),
	)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	var accepted struct {
		Session        json.RawMessage                        `json:"session"`
		Result         *workflows.RunResult                   `json:"result"`
		Reconciliation *workflowDevelopmentTestReconciliation `json:"reconciliation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(accepted.Session) != 0 ||
		accepted.Result == nil ||
		accepted.Result.RunID == "" ||
		accepted.Result.Status != workflows.RunStatusRunning ||
		accepted.Reconciliation == nil ||
		accepted.Reconciliation.Reason != "draft_test_response_truncated" ||
		accepted.Reconciliation.RunID != accepted.Result.RunID {
		t.Fatalf("response = %#v", accepted)
	}
	runs, err := workflows.NewFileRunStore(workspace).ListRuns(
		context.Background(),
	)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].ID != accepted.Result.RunID {
		t.Fatalf("durable runs = %#v", runs)
	}
	waitForWorkflowTriggerTestCompletion(t, completed)
}

func TestWorkflowTriggerAcceptedNearLimitUsesBounded202Fallback(t *testing.T) {
	workspace, handler, mux, session := newWorkflowTriggerAPIHarness(t)
	completed := make(chan struct{})
	handler.workflowDevelopmentTestDone = func() {
		close(completed)
	}
	_, sampleToken := simulateWorkflowTriggerReviewedManual(t, mux, session)
	largeRequest := workflowTriggerTestRequest(
		session,
		workflowTriggerReviewedManualYAML+"#",
		workflows.WorkflowTriggerManual,
		json.RawMessage(`{}`),
	)
	sampleExecution := workflowTriggerExecutionFromSimulation(
		largeRequest,
		sampleToken,
	)
	sampleBody, err := json.Marshal(sampleExecution)
	if err != nil {
		t.Fatalf("marshal sample execution: %v", err)
	}
	targetBytes := workflowTriggerSimulationRequestMaxBytes - 64
	fillerBytes := targetBytes - len(sampleBody)
	if fillerBytes <= 0 {
		t.Fatalf("sample execution unexpectedly large: %d", len(sampleBody))
	}
	largeRequest.YAML += strings.Repeat("x", fillerBytes)
	simulated := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/simulate",
		largeRequest,
	)
	if simulated.Code != http.StatusOK {
		t.Fatalf("simulate status = %d; body=%s", simulated.Code, simulated.Body.String())
	}
	var reviewed workflowTriggerSimulationResponse
	if err := json.Unmarshal(simulated.Body.Bytes(), &reviewed); err != nil {
		t.Fatalf("decode simulation response: %v", err)
	}
	execution := workflowTriggerExecutionFromSimulation(
		largeRequest,
		reviewed.ReviewToken,
	)
	executionBody, err := json.Marshal(execution)
	if err != nil {
		t.Fatalf("marshal execution: %v", err)
	}
	if len(executionBody) > workflowTriggerSimulationRequestMaxBytes {
		t.Fatalf("execution body = %d bytes", len(executionBody))
	}
	executed := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		execution,
	)
	if executed.Code != http.StatusAccepted {
		t.Fatalf(
			"execute status = %d, want 202; body=%s",
			executed.Code,
			executed.Body.String(),
		)
	}
	if executed.Body.Len() > workflowTriggerSimulationResponseMaxBytes {
		t.Fatalf("fallback response = %d bytes", executed.Body.Len())
	}
	var accepted struct {
		Session        json.RawMessage                        `json:"session"`
		Result         *workflows.RunResult                   `json:"result"`
		Reconciliation *workflowDevelopmentTestReconciliation `json:"reconciliation"`
	}
	if err := json.Unmarshal(executed.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode fallback: %v", err)
	}
	if len(accepted.Session) != 0 ||
		accepted.Result == nil ||
		accepted.Result.Status != workflows.RunStatusRunning ||
		accepted.Reconciliation == nil ||
		accepted.Reconciliation.Reason != "draft_test_response_truncated" {
		t.Fatalf("fallback response = %#v", accepted)
	}
	if _, err := workflows.NewFileRunStore(workspace).GetRun(
		context.Background(),
		accepted.Result.RunID,
	); err != nil {
		t.Fatalf("accepted durable run missing: %v", err)
	}
	waitForWorkflowTriggerTestCompletion(t, completed)
}

func TestWorkflowTriggerExecutionAcceptsAsyncRunAndRejectsReplay(t *testing.T) {
	workspace, _, mux, session := newWorkflowTriggerAPIHarness(t)
	expiredAt := time.Now().UTC().AddDate(-1, 0, 0)
	store := workflows.NewFileRunStore(workspace)
	if err := store.CreateRun(context.Background(), &workflows.Run{
		ID:          "wr_expired_history_to_prune",
		WorkflowRef: "workflows/history.yml",
		Status:      workflows.RunStatusSucceeded,
		CreatedAt:   expiredAt,
		UpdatedAt:   expiredAt,
		CompletedAt: &expiredAt,
	}); err != nil {
		t.Fatalf("CreateRun(expired history) error = %v", err)
	}
	simulationRequest, token := simulateWorkflowTriggerReviewedManual(
		t,
		mux,
		session,
	)
	execution := workflowTriggerExecutionFromSimulation(
		simulationRequest,
		token,
	)
	response := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		execution,
	)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", response.Code, response.Body.String())
	}
	assertWorkflowTriggerSecurityHeaders(t, response)
	var accepted struct {
		Session        *workflows.WorkflowDevelopmentSession  `json:"session"`
		Result         *workflows.RunResult                   `json:"result"`
		Reconciliation *workflowDevelopmentTestReconciliation `json:"reconciliation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode accepted response: %v", err)
	}
	if accepted.Session == nil ||
		accepted.Session.TargetWorkflowRef != simulationRequest.TargetRef ||
		accepted.Session.YAML != simulationRequest.YAML ||
		accepted.Session.Validation == nil ||
		!accepted.Session.Validation.Valid ||
		accepted.Session.LastTest == nil ||
		accepted.Session.LastTest.Status != workflows.RunStatusRunning ||
		accepted.Result == nil ||
		accepted.Result.Status != workflows.RunStatusRunning ||
		accepted.Result.RunID == "" ||
		accepted.Session.LastTest.RunID != accepted.Result.RunID ||
		accepted.Reconciliation != nil {
		t.Fatalf("accepted payload = %#v", accepted)
	}

	runs, err := store.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 ||
		runs[0].ID != accepted.Result.RunID ||
		runs[0].WorkflowRef != "draft:"+simulationRequest.TargetRef {
		t.Fatalf("durable runs = %#v", runs)
	}

	replay := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		execution,
	)
	if replay.Code != http.StatusConflict {
		t.Fatalf("replay status = %d; body=%s", replay.Code, replay.Body.String())
	}
	assertWorkflowTriggerSimulationErrorCode(
		t,
		replay,
		"workflow_development_fence_mismatch",
	)
	runs, err = store.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns(replay) error = %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("replay created runs: %#v", runs)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		active, sessionErr := workflows.GetWorkflowDevelopmentSession(workspace)
		if sessionErr != nil {
			t.Fatalf("GetWorkflowDevelopmentSession() error = %v", sessionErr)
		}
		if active != nil &&
			active.LastTest != nil &&
			active.LastTest.RunID == accepted.Result.RunID &&
			active.LastTest.Status == workflows.RunStatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("async completion was not reconciled: %#v", active)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWorkflowTriggerExecutionRejectsChangedConfigBindingWithoutMutation(
	t *testing.T,
) {
	workspace, handler, mux, session := newWorkflowTriggerAPIHarness(t)
	simulationRequest, token := simulateWorkflowTriggerReviewedManual(
		t,
		mux,
		session,
	)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Workflows.RetentionDays++
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	before := readWorkflowTriggerActiveSessionBytes(t, workspace)
	response := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		workflowTriggerExecutionFromSimulation(simulationRequest, token),
	)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	assertWorkflowTriggerSimulationErrorCode(
		t,
		response,
		"workflow_trigger_review_invalid",
	)
	after := readWorkflowTriggerActiveSessionBytes(t, workspace)
	if string(after) != string(before) {
		t.Fatalf("config-bound rejection mutated session\nbefore=%s\nafter=%s", before, after)
	}
	assertWorkflowTriggerNoRuns(t, workspace)
}

func TestWorkflowTriggerExecutionRejectsConfigRaceBeforeAdmission(t *testing.T) {
	workspace, _, mux, session := newWorkflowTriggerAPIHarness(t)
	expiredAt := time.Now().UTC().AddDate(-1, 0, 0)
	expiredRun := &workflows.Run{
		ID:          "wr_expired_history_must_survive",
		WorkflowRef: "workflows/history.yml",
		Status:      workflows.RunStatusSucceeded,
		CreatedAt:   expiredAt,
		UpdatedAt:   expiredAt,
		CompletedAt: &expiredAt,
	}
	store := workflows.NewFileRunStore(workspace)
	if err := store.CreateRun(context.Background(), expiredRun); err != nil {
		t.Fatalf("CreateRun(expired history) error = %v", err)
	}
	simulationRequest, token := simulateWorkflowTriggerReviewedManual(
		t,
		mux,
		session,
	)
	previousRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = previousRunners })
	newWorkflowRuntimeRunners = func(path string) workflowRuntimeRunners {
		cfg, err := config.LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig(runtime hook) error = %v", err)
		}
		cfg.Workflows.RetentionDays++
		if err := config.SaveConfig(path, cfg); err != nil {
			t.Fatalf("SaveConfig(runtime hook) error = %v", err)
		}
		return workflowRuntimeRunners{}
	}
	before := readWorkflowTriggerActiveSessionBytes(t, workspace)
	response := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		workflowTriggerExecutionFromSimulation(simulationRequest, token),
	)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	assertWorkflowTriggerSimulationErrorCode(
		t,
		response,
		"workflow_trigger_execution_config_changed",
	)
	after := readWorkflowTriggerActiveSessionBytes(t, workspace)
	if string(after) != string(before) {
		t.Fatalf("config race mutated session\nbefore=%s\nafter=%s", before, after)
	}
	retained, err := store.GetRun(context.Background(), expiredRun.ID)
	if err != nil {
		t.Fatalf("rejected execution pruned existing run: %v", err)
	}
	if retained.ID != expiredRun.ID ||
		retained.Status != workflows.RunStatusSucceeded {
		t.Fatalf("retained run = %#v", retained)
	}
	runs, err := store.ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].ID != expiredRun.ID {
		t.Fatalf("rejected execution changed run store: %#v", runs)
	}
}

func TestWorkflowTriggerExecutionRechecksReviewExpiryAtDurableAdmission(
	t *testing.T,
) {
	workspace, handler, mux, session := newWorkflowTriggerAPIHarness(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	handler.workflowTriggerReviewNow = func() time.Time { return now }
	simulationRequest, token := simulateWorkflowTriggerReviewedManual(
		t,
		mux,
		session,
	)
	previousRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = previousRunners })
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		handler.workflowTriggerReviewNow = func() time.Time {
			return now.Add(workflowTriggerReviewTTL)
		}
		return workflowRuntimeRunners{}
	}
	before := readWorkflowTriggerActiveSessionBytes(t, workspace)

	response := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		workflowTriggerExecutionFromSimulation(simulationRequest, token),
	)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d; body=%s", response.Code, response.Body.String())
	}
	assertWorkflowTriggerSimulationErrorCode(
		t,
		response,
		"workflow_trigger_review_invalid",
	)
	after := readWorkflowTriggerActiveSessionBytes(t, workspace)
	if string(after) != string(before) {
		t.Fatalf(
			"expired final review mutated session\nbefore=%s\nafter=%s",
			before,
			after,
		)
	}
	assertWorkflowTriggerNoRuns(t, workspace)
}

func TestWorkflowTriggerExecutionRejectsChangedProtectedEventWithoutMutation(
	t *testing.T,
) {
	workspace, _, mux, session := newWorkflowTriggerAPIHarness(t)
	generation := "PRIVATE_EVENT_GENERATION_ONE"
	calls := 0
	installEventProxyStubs(t, func(
		request *http.Request,
		_ time.Duration,
	) (*http.Response, error) {
		calls++
		if request.URL.Path !=
			"/runtime/eventing/events/"+testEventID+"/workflow-context" {
			t.Fatalf("upstream path = %q", request.URL.Path)
		}
		return workflowTriggerEventResponse(generation), nil
	})
	simulationRequest := workflowTriggerTestRequest(
		session,
		workflowTriggerAllFamiliesYAML,
		workflows.WorkflowTriggerEvent,
		json.RawMessage(`{"event_id":"`+testEventID+`"}`),
	)
	simulated := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/simulate",
		simulationRequest,
	)
	if simulated.Code != http.StatusOK {
		t.Fatalf("simulate status = %d; body=%s", simulated.Code, simulated.Body.String())
	}
	var result workflowTriggerSimulationResponse
	if err := json.Unmarshal(simulated.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode simulation response: %v", err)
	}
	if result.ReviewToken == "" ||
		strings.Contains(simulated.Body.String(), generation) {
		t.Fatalf("event simulation response = %s", simulated.Body.String())
	}
	generation = "PRIVATE_EVENT_GENERATION_TWO"
	before := readWorkflowTriggerActiveSessionBytes(t, workspace)
	executed := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		workflowTriggerExecutionFromSimulation(
			simulationRequest,
			result.ReviewToken,
		),
	)
	if executed.Code != http.StatusForbidden {
		t.Fatalf("execute status = %d; body=%s", executed.Code, executed.Body.String())
	}
	assertWorkflowTriggerSimulationErrorCode(
		t,
		executed,
		"workflow_trigger_review_invalid",
	)
	if calls != 2 {
		t.Fatalf("protected envelope loads = %d, want 2", calls)
	}
	if strings.Contains(executed.Body.String(), generation) {
		t.Fatalf("execute response exposed event payload: %s", executed.Body.String())
	}
	after := readWorkflowTriggerActiveSessionBytes(t, workspace)
	if string(after) != string(before) {
		t.Fatalf("changed event mutated session\nbefore=%s\nafter=%s", before, after)
	}
	assertWorkflowTriggerNoRuns(t, workspace)
}

func TestWorkflowTriggerEventExecutionUsesIDOnlyAndReloadsProtectedEnvelope(
	t *testing.T,
) {
	workspace, handler, mux, session := newWorkflowTriggerAPIHarness(t)
	completed := make(chan struct{})
	handler.workflowDevelopmentTestDone = func() {
		close(completed)
	}
	const privateGeneration = "PRIVATE_EVENT_CURRENT_GENERATION"
	calls := 0
	installEventProxyStubs(t, func(
		request *http.Request,
		_ time.Duration,
	) (*http.Response, error) {
		calls++
		if request.URL.Path !=
			"/runtime/eventing/events/"+testEventID+"/workflow-context" {
			t.Fatalf("upstream path = %q", request.URL.Path)
		}
		return workflowTriggerEventResponse(privateGeneration), nil
	})
	simulationRequest := workflowTriggerTestRequest(
		session,
		workflowTriggerAllFamiliesYAML,
		workflows.WorkflowTriggerEvent,
		json.RawMessage(`{"event_id":"`+testEventID+`"}`),
	)
	simulated := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/simulate",
		simulationRequest,
	)
	if simulated.Code != http.StatusOK {
		t.Fatalf("simulate status = %d; body=%s", simulated.Code, simulated.Body.String())
	}
	var reviewed workflowTriggerSimulationResponse
	if err := json.Unmarshal(simulated.Body.Bytes(), &reviewed); err != nil {
		t.Fatalf("decode simulation response: %v", err)
	}
	if reviewed.ReviewToken == "" {
		t.Fatalf("simulation response = %s", simulated.Body.String())
	}
	executed := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/test/execute",
		workflowTriggerExecutionFromSimulation(
			simulationRequest,
			reviewed.ReviewToken,
		),
	)
	if executed.Code != http.StatusAccepted {
		t.Fatalf("execute status = %d; body=%s", executed.Code, executed.Body.String())
	}
	if calls != 3 {
		t.Fatalf("protected envelope loads = %d, want 3", calls)
	}
	if strings.Contains(simulated.Body.String(), privateGeneration) ||
		strings.Contains(executed.Body.String(), privateGeneration) {
		t.Fatalf(
			"event payload leaked: simulate=%s execute=%s",
			simulated.Body.String(),
			executed.Body.String(),
		)
	}
	var accepted struct {
		Session *workflows.WorkflowDevelopmentSession `json:"session"`
		Result  *workflows.RunResult                  `json:"result"`
	}
	if err := json.Unmarshal(executed.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode accepted response: %v", err)
	}
	if accepted.Session == nil ||
		accepted.Session.LastTest == nil ||
		accepted.Session.LastTest.EventID != testEventID ||
		accepted.Result == nil {
		t.Fatalf("accepted event payload = %#v", accepted)
	}
	run, err := workflows.NewFileRunStore(workspace).GetRun(
		context.Background(),
		accepted.Result.RunID,
	)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	payload, ok := run.Event["payload"].(map[string]any)
	if !ok || payload["generation"] != privateGeneration {
		t.Fatalf("durable run did not use protected envelope: %#v", run.Event)
	}
	if run.Origin == nil ||
		run.Origin.Kind != workflows.RunOriginExternalEventDraftTest ||
		run.Origin.EventID != testEventID {
		t.Fatalf("run origin = %#v", run.Origin)
	}
	waitForWorkflowTriggerTestCompletion(t, completed)
}

func TestWorkflowTriggerReviewTokenBindsRequestAndStableReview(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	handler := NewHandler(t.TempDir() + "/config.json")
	handler.workflowTriggerReviewNow = func() time.Time { return now }
	handler.workflowTriggerReviewOnce.Do(func() {
		for index := range handler.workflowTriggerReviewKey {
			handler.workflowTriggerReviewKey[index] = byte(index + 1)
		}
	})
	request := workflowTriggerSimulationRequest{
		SessionID:               "session-1",
		ExpectedSessionRevision: "sha256:session",
		ExpectedDraftRevision:   "sha256:draft",
		Prompt:                  "draft",
		TargetRef:               "workflows/demo.yml",
		YAML:                    "on:\n  manual: {}\njobs: {}\n",
		Trigger: workflowTriggerRequestWire{
			Type: "manual",
		},
		Scenario: json.RawMessage(
			`{"inputs":{"issue":1},"secrets":{"token":"secret-value"},"session":"","delivery":{}}`,
		),
	}
	review := map[string]any{
		"simulation": map[string]any{
			"selected_kind": "manual",
			"matched":       true,
			"executable":    true,
			"reason":        "matched",
		},
		"review": map[string]any{
			"targets": []string{"tool/message"},
			"validation": map[string]any{
				"valid":        true,
				"validated_at": now,
			},
		},
	}
	binding := workflowTriggerReviewBinding{
		ConfigRevision: "sha256:config",
	}

	token, err := handler.issueWorkflowTriggerReviewToken(
		request,
		review,
		binding,
	)
	if err != nil {
		t.Fatalf("issueWorkflowTriggerReviewToken() error = %v", err)
	}
	if strings.Contains(token, "secret-value") {
		t.Fatal("review token exposes a secret value")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Fatalf("token parts = %d", len(parts))
	}
	payload, decodeErr := base64.RawURLEncoding.DecodeString(parts[0])
	if decodeErr != nil {
		t.Fatalf("decode token payload: %v", decodeErr)
	}
	if strings.Contains(string(payload), "secret") ||
		strings.Contains(string(payload), "workflow") {
		t.Fatalf("token payload exposes request-derived values: %s", payload)
	}
	if err := handler.verifyWorkflowTriggerReviewToken(
		token,
		request,
		review,
		binding,
	); err != nil {
		t.Fatalf("verifyWorkflowTriggerReviewToken() error = %v", err)
	}

	recomputedReview := map[string]any{
		"simulation": review["simulation"],
		"review": map[string]any{
			"targets": []string{"tool/message"},
			"validation": map[string]any{
				"valid":        true,
				"validated_at": now.Add(time.Minute),
			},
		},
	}
	if err := handler.verifyWorkflowTriggerReviewToken(
		token,
		request,
		recomputedReview,
		binding,
	); err != nil {
		t.Fatalf("volatile validation time invalidated token: %v", err)
	}

	changedRequest := request
	changedRequest.YAML += "# changed\n"
	if err := handler.verifyWorkflowTriggerReviewToken(
		token,
		changedRequest,
		review,
		binding,
	); !errors.Is(err, errWorkflowTriggerReviewToken) {
		t.Fatalf("changed request error = %v", err)
	}
	changedReview := map[string]any{
		"simulation": review["simulation"],
		"review": map[string]any{
			"targets": []string{"mcp/github/delete_issue"},
			"validation": map[string]any{
				"valid":        true,
				"validated_at": now,
			},
		},
	}
	if err := handler.verifyWorkflowTriggerReviewToken(
		token,
		request,
		changedReview,
		binding,
	); !errors.Is(err, errWorkflowTriggerReviewToken) {
		t.Fatalf("changed review error = %v", err)
	}
	changedBinding := binding
	changedBinding.ConfigRevision = "sha256:changed"
	if err := handler.verifyWorkflowTriggerReviewToken(
		token,
		request,
		review,
		changedBinding,
	); !errors.Is(err, errWorkflowTriggerReviewToken) {
		t.Fatalf("changed private binding error = %v", err)
	}
	changedBinding = binding
	changedBinding.EventDigest = "changed-event-digest"
	if err := handler.verifyWorkflowTriggerReviewToken(
		token,
		request,
		review,
		changedBinding,
	); !errors.Is(err, errWorkflowTriggerReviewToken) {
		t.Fatalf("changed event binding error = %v", err)
	}

	handler.workflowTriggerReviewNow = func() time.Time {
		return now.Add(workflowTriggerReviewTTL)
	}
	if err := handler.verifyWorkflowTriggerReviewToken(
		token,
		request,
		review,
		binding,
	); !errors.Is(err, errWorkflowTriggerReviewToken) {
		t.Fatalf("expired token error = %v", err)
	}
}

func TestWorkflowTriggerReviewKeyFailureIsFailClosed(t *testing.T) {
	previousRandomRead := workflowTriggerSimulationRandomRead
	workflowTriggerSimulationRandomRead = func([]byte) (int, error) {
		return 0, errors.New("entropy unavailable")
	}
	t.Cleanup(func() {
		workflowTriggerSimulationRandomRead = previousRandomRead
	})

	handler := NewHandler(t.TempDir() + "/config.json")
	handler.workflowTriggerReviewNow = func() time.Time { return time.Now().UTC() }
	_, err := handler.issueWorkflowTriggerReviewToken(
		workflowTriggerSimulationRequest{},
		map[string]any{"executable": true},
		workflowTriggerReviewBinding{ConfigRevision: "sha256:config"},
	)
	if !errors.Is(err, errWorkflowTriggerReviewToken) {
		t.Fatalf("issue error = %v, want review-token failure", err)
	}
}

func newWorkflowTriggerAPIHarness(
	t *testing.T,
) (
	string,
	*Handler,
	*http.ServeMux,
	*workflows.WorkflowDevelopmentSession,
) {
	t.Helper()
	workspace := t.TempDir()
	handler := NewHandler(writeWorkflowAITestConfig(t, workspace))
	session, err := workflows.StartWorkflowDevelopment(
		context.Background(),
		workspace,
		workflows.RuntimeCompatibility{
			PicoclawVersion: "v1.0.0",
			GitCommit:       "abc123",
		},
		workflows.WorkflowDevelopmentStartRequest{
			Prompt:    "original trigger draft",
			TargetRef: "workflows/original-trigger.yml",
		},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	mux := http.NewServeMux()
	handler.registerWorkflowEditorRoutes(mux)
	return workspace, handler, mux, session
}

func workflowTriggerTestRequest(
	session *workflows.WorkflowDevelopmentSession,
	yaml string,
	kind workflows.WorkflowTriggerKind,
	scenario json.RawMessage,
) workflowTriggerSimulationRequest {
	return workflowTriggerSimulationRequest{
		SessionID:               session.ID,
		ExpectedSessionRevision: session.SessionRevision,
		ExpectedDraftRevision:   session.DraftRevision,
		Prompt:                  "reviewed trigger draft",
		TargetRef:               "workflows/reviewed-trigger.yml",
		YAML:                    yaml,
		Trigger: workflowTriggerRequestWire{
			Type: kind,
		},
		Scenario: append(json.RawMessage(nil), scenario...),
	}
}

func workflowTriggerExecutionFromSimulation(
	request workflowTriggerSimulationRequest,
	token string,
) workflowTriggerExecutionRequest {
	return workflowTriggerExecutionRequest{
		SessionID:               request.SessionID,
		ExpectedSessionRevision: request.ExpectedSessionRevision,
		ExpectedDraftRevision:   request.ExpectedDraftRevision,
		Prompt:                  request.Prompt,
		TargetRef:               request.TargetRef,
		YAML:                    request.YAML,
		Trigger:                 request.Trigger,
		Scenario:                append(json.RawMessage(nil), request.Scenario...),
		ReviewToken:             token,
	}
}

func simulateWorkflowTriggerReviewedManual(
	t *testing.T,
	mux *http.ServeMux,
	session *workflows.WorkflowDevelopmentSession,
) (workflowTriggerSimulationRequest, string) {
	t.Helper()
	request := workflowTriggerTestRequest(
		session,
		workflowTriggerReviewedManualYAML,
		workflows.WorkflowTriggerManual,
		json.RawMessage(`{}`),
	)
	response := postWorkflowTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/simulate",
		request,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("simulate status = %d; body=%s", response.Code, response.Body.String())
	}
	var result workflowTriggerSimulationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode simulation response: %v", err)
	}
	if !result.Simulation.Executable || result.ReviewToken == "" {
		t.Fatalf("simulation result = %#v", result)
	}
	return request, result.ReviewToken
}

func tamperWorkflowTriggerReviewToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[1] == "" {
		return token + "tampered"
	}
	mac := []byte(parts[1])
	if mac[0] == 'A' {
		mac[0] = 'B'
	} else {
		mac[0] = 'A'
	}
	return parts[0] + "." + string(mac)
}

func equivalentWorkflowTriggerReviewTokenAliases(
	t *testing.T,
	token string,
) map[string]string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(parts[1]) < 2 {
		t.Fatalf("review token is not splittable: %q", token)
	}
	decodedMAC, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode review token MAC: %v", err)
	}
	newlineAlias := parts[0] + "." +
		parts[1][:len(parts[1])-1] + "\n" +
		parts[1][len(parts[1])-1:]

	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var trailingBitsAlias string
	for index := range alphabet {
		candidateMAC := parts[1][:len(parts[1])-1] +
			string(alphabet[index])
		if candidateMAC == parts[1] {
			continue
		}
		candidateBytes, decodeErr :=
			base64.RawURLEncoding.DecodeString(candidateMAC)
		if decodeErr == nil && bytes.Equal(candidateBytes, decodedMAC) {
			trailingBitsAlias = parts[0] + "." + candidateMAC
			break
		}
	}
	if trailingBitsAlias == "" {
		t.Fatal("could not construct equivalent trailing-bit token alias")
	}
	return map[string]string{
		"newline":      newlineAlias,
		"trailing-bit": trailingBitsAlias,
	}
}

func waitForWorkflowTriggerTestCompletion(
	t *testing.T,
	completed <-chan struct{},
) {
	t.Helper()
	select {
	case <-completed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for workflow trigger test reconciliation")
	}
}

func workflowTriggerEventResponse(generation string) *http.Response {
	body := strings.Replace(
		workflowEventContextJSON("issues.opened"),
		`"payload":{`,
		`"payload":{"generation":"`+generation+`",`,
		1,
	)
	return workflowEventBodyResponse(body)
}

func postWorkflowTriggerJSON(
	t *testing.T,
	mux *http.ServeMux,
	path string,
	value any,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		path,
		strings.NewReader(string(body)),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func readWorkflowTriggerActiveSessionBytes(
	t *testing.T,
	workspace string,
) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(
		workspace,
		"workflow_dev",
		"active.json",
	))
	if err != nil {
		t.Fatalf("read active session: %v", err)
	}
	return data
}

func assertWorkflowTriggerNoRuns(t *testing.T, workspace string) {
	t.Helper()
	runs, err := workflows.NewFileRunStore(workspace).ListRuns(
		context.Background(),
	)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("unexpected workflow runs: %#v", runs)
	}
}

func assertWorkflowTriggerSecurityHeaders(
	t *testing.T,
	response *httptest.ResponseRecorder,
) {
	t.Helper()
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("security headers = %#v", response.Header())
	}
}

func assertWorkflowTriggerSimulationErrorCode(
	t *testing.T,
	response *httptest.ResponseRecorder,
	want string,
) {
	t.Helper()
	var body workflowTriggerSimulationErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, response.Body.String())
	}
	if body.Error != want {
		t.Fatalf("error = %q, want %q", body.Error, want)
	}
}
