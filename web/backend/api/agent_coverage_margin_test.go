package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestAgentManagementBoundaryFailuresAreStableAndNonMutating(t *testing.T) {
	harness := newAgentAPITestHarness(t, nil)
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	before, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(before) error = %v", err)
	}

	revision := initial.ConfigRevision
	tests := []struct {
		name       string
		method     string
		path       string
		payload    any
		wantStatus int
		wantCode   string
	}{
		{
			name:       "get rejects noncanonical id",
			method:     http.MethodGet,
			path:       "/api/agents/Bad-ID",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_agent_id",
		},
		{
			name:       "create requires an agent",
			method:     http.MethodPost,
			path:       "/api/agents",
			payload:    agentMutationRequest{ExpectedConfigRevision: &revision},
			wantStatus: http.StatusBadRequest,
			wantCode:   "agent_required",
		},
		{
			name:   "create preserves implicit main",
			method: http.MethodPost,
			path:   "/api/agents",
			payload: agentMutationRequest{
				ExpectedConfigRevision: &revision,
				Agent:                  &agentResource{ID: "main"},
			},
			wantStatus: http.StatusConflict,
			wantCode:   "agent_exists",
		},
		{
			name:       "update rejects noncanonical path id",
			method:     http.MethodPut,
			path:       "/api/agents/Bad-ID",
			payload:    agentMutationRequest{},
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_agent_id",
		},
		{
			name:       "update requires a revision",
			method:     http.MethodPut,
			path:       "/api/agents/worker",
			payload:    agentMutationRequest{Agent: &agentResource{ID: "worker"}},
			wantStatus: http.StatusBadRequest,
			wantCode:   "expected_config_revision_required",
		},
		{
			name:       "update requires an agent",
			method:     http.MethodPut,
			path:       "/api/agents/worker",
			payload:    agentMutationRequest{ExpectedConfigRevision: &revision},
			wantStatus: http.StatusBadRequest,
			wantCode:   "agent_required",
		},
		{
			name:   "update keeps identity immutable",
			method: http.MethodPut,
			path:   "/api/agents/worker",
			payload: agentMutationRequest{
				ExpectedConfigRevision: &revision,
				Agent:                  &agentResource{ID: "other"},
			},
			wantStatus: http.StatusConflict,
			wantCode:   "agent_id_immutable",
		},
		{
			name:   "update rejects missing agent",
			method: http.MethodPut,
			path:   "/api/agents/worker",
			payload: agentMutationRequest{
				ExpectedConfigRevision: &revision,
				Agent:                  &agentResource{ID: "worker"},
			},
			wantStatus: http.StatusNotFound,
			wantCode:   "agent_not_found",
		},
		{
			name:       "delete rejects noncanonical id",
			method:     http.MethodDelete,
			path:       "/api/agents/Bad-ID",
			payload:    agentRevisionRequest{},
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_agent_id",
		},
		{
			name:       "delete requires a revision",
			method:     http.MethodDelete,
			path:       "/api/agents/main",
			payload:    agentRevisionRequest{},
			wantStatus: http.StatusBadRequest,
			wantCode:   "expected_config_revision_required",
		},
		{
			name:   "delete fences stale revisions",
			method: http.MethodDelete,
			path:   "/api/agents/main",
			payload: agentRevisionRequest{
				ExpectedConfigRevision: agentTestStringPointer("stale"),
			},
			wantStatus: http.StatusConflict,
			wantCode:   "config_revision_mismatch",
		},
		{
			name:   "delete preserves implicit main",
			method: http.MethodDelete,
			path:   "/api/agents/main",
			payload: agentRevisionRequest{
				ExpectedConfigRevision: &revision,
			},
			wantStatus: http.StatusConflict,
			wantCode:   "implicit_agent_required",
		},
		{
			name:   "delete rejects missing agent",
			method: http.MethodDelete,
			path:   "/api/agents/worker",
			payload: agentRevisionRequest{
				ExpectedConfigRevision: &revision,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   "agent_not_found",
		},
		{
			name:       "default rejects noncanonical id",
			method:     http.MethodPost,
			path:       "/api/agents/Bad-ID/default",
			payload:    agentRevisionRequest{},
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_agent_id",
		},
		{
			name:       "default requires a revision",
			method:     http.MethodPost,
			path:       "/api/agents/main/default",
			payload:    agentRevisionRequest{},
			wantStatus: http.StatusBadRequest,
			wantCode:   "expected_config_revision_required",
		},
		{
			name:   "default fences stale revisions",
			method: http.MethodPost,
			path:   "/api/agents/main/default",
			payload: agentRevisionRequest{
				ExpectedConfigRevision: agentTestStringPointer("stale"),
			},
			wantStatus: http.StatusConflict,
			wantCode:   "config_revision_mismatch",
		},
		{
			name:   "default rejects missing agent",
			method: http.MethodPost,
			path:   "/api/agents/worker/default",
			payload: agentRevisionRequest{
				ExpectedConfigRevision: &revision,
			},
			wantStatus: http.StatusNotFound,
			wantCode:   "agent_not_found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := harness.request(t, test.method, test.path, test.payload)
			if recorder.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, body=%s, want %d",
					recorder.Code,
					recorder.Body.String(),
					test.wantStatus,
				)
			}
			var response agentErrorResponse
			if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &response); decodeErr != nil {
				t.Fatalf("json.Unmarshal() error = %v", decodeErr)
			}
			if response.Error != test.wantCode {
				t.Fatalf("error = %q, want %q", response.Error, test.wantCode)
			}
		})
	}

	t.Run("valid origin reaches handler", func(t *testing.T) {
		request := httptest.NewRequest(
			http.MethodPost,
			"http://example.test/api/agents",
			strings.NewReader(`{"expected_config_revision":""}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://example.test")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest ||
			!strings.Contains(recorder.Body.String(), "expected_config_revision_required") {
			t.Fatalf("response = %d/%q", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("malformed origin fails before handler", func(t *testing.T) {
		request := httptest.NewRequest(
			http.MethodPost,
			"http://example.test/api/agents",
			strings.NewReader(`{}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "not-an-origin")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		recorder := httptest.NewRecorder()
		harness.mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden ||
			!strings.Contains(recorder.Body.String(), "cross_origin_agent_mutation") {
			t.Fatalf("response = %d/%q", recorder.Code, recorder.Body.String())
		}
	})

	implicitDefault := decodeAgentCollection(t, harness.request(
		t,
		http.MethodPost,
		"/api/agents/main/default",
		agentRevisionRequest{ExpectedConfigRevision: &revision},
	))
	if implicitDefault.ConfigRevision != revision ||
		len(implicitDefault.Agents) != 1 || !implicitDefault.Agents[0].Implicit {
		t.Fatalf("implicit default response = %#v", implicitDefault)
	}

	after, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("rejected and idempotent requests changed the config")
	}
}

func TestAgentManagementConfiguredDefaultIsIdempotent(t *testing.T) {
	harness := newAgentAPITestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	})
	initial := decodeAgentCollection(
		t,
		harness.request(t, http.MethodGet, "/api/agents", nil),
	)
	response := decodeAgentCollection(t, harness.request(
		t,
		http.MethodPost,
		"/api/agents/main/default",
		agentRevisionRequest{ExpectedConfigRevision: &initial.ConfigRevision},
	))
	if response.ConfigRevision != initial.ConfigRevision ||
		len(response.Agents) != 1 || !response.Agents[0].DefaultConfigured {
		t.Fatalf("idempotent default response = %#v", response)
	}
}

func agentTestStringPointer(value string) *string {
	return &value
}
