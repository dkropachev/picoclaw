package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ppid "github.com/sipeed/picoclaw/pkg/pid"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowAuthoringCapabilitiesProxyUsesPIDBearerAndValidatesResponse(t *testing.T) {
	valid := workflowAuthoringAPIValidResponse(t)
	var captured *http.Request
	withWorkflowAuthoringAPIGateway(t, func(
		request *http.Request,
		timeout time.Duration,
	) (*http.Response, error) {
		captured = request
		if timeout != workflowAuthoringGatewayTimeout {
			t.Fatalf("timeout = %s", timeout)
		}
		return workflowAuthoringAPIUpstream(http.StatusOK, "application/json; charset=utf-8", valid), nil
	})

	handler := NewHandler(t.TempDir() + "/missing-config.json")
	mux := http.NewServeMux()
	handler.registerWorkflowAuthoringRoutes(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, workflowAuthoringAPIPath, nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if captured == nil ||
		captured.Method != http.MethodGet ||
		captured.URL.Path != workflows.RuntimeAuthoringCapabilitiesPath ||
		captured.URL.RawQuery != "" ||
		captured.Header.Get("Authorization") != "Bearer workflow-authoring-pid-token" {
		t.Fatalf("upstream request = %#v", captured)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" ||
		recorder.Header().Get("X-Content-Type-Options") != "nosniff" ||
		recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response headers = %#v", recorder.Header())
	}
	if _, err := workflows.DecodeWorkflowAuthoringCapabilities(recorder.Body.Bytes()); err != nil {
		t.Fatalf("proxied body failed strict decode: %v\n%s", err, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "workflow-authoring-pid-token") {
		t.Fatal("PID token leaked in proxy response")
	}
}

func TestWorkflowAuthoringCapabilitiesProxyRejectsUnsafeGatewayResponses(t *testing.T) {
	valid := string(workflowAuthoringAPIValidResponse(t))
	deepShape := `{}`
	for index := 0; index < workflows.MaxWorkflowAuthoringShapeDepth; index++ {
		deepShape = `{"items":` + deepShape + `}`
	}
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		doErr       error
	}{
		{
			name:        "unknown leaky field",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"limits":[]`,
				`"private":"PRIVATE_SENTINEL","limits":[]`,
				1,
			),
		},
		{
			name:        "duplicate field",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"complete":true`,
				`"complete":true,"complete":true`,
				1,
			),
		},
		{
			name:        "wrong case root field",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"complete":true`,
				`"Complete":true`,
				1,
			),
		},
		{
			name:        "case colliding root fields",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"complete":true`,
				`"complete":true,"Complete":true`,
				1,
			),
		},
		{
			name:        "wrong case nested field",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"parameter_shape":{}`,
				`"Parameter_Shape":{}`,
				1,
			),
		},
		{
			name:        "target mismatch",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"target":"tool/alpha"`,
				`"target":"tool/PRIVATE_SENTINEL"`,
				1,
			),
		},
		{
			name:        "unsorted duplicate tools",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"tools":[{"name":"alpha","target":"tool/alpha","readiness":"ready","parameter_shape_projected":true,"parameter_shape":{}}]`,
				`"tools":[{"name":"zeta","target":"tool/zeta","readiness":"ready","parameter_shape_projected":true,"parameter_shape":{}},{"name":"alpha","target":"tool/alpha","readiness":"ready","parameter_shape_projected":true,"parameter_shape":{}}]`,
				1,
			),
		},
		{
			name:        "inconsistent complete",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"complete":true`,
				`"complete":false`,
				1,
			),
		},
		{
			name:        "invalid limit",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"limits":[]`,
				`"limits":["PRIVATE_SENTINEL"]`,
				1,
			),
		},
		{
			name:        "over depth",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"parameter_shape":{}`,
				`"parameter_shape":`+deepShape,
				1,
			),
		},
		{
			name:        "unsafe text",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"name":"alpha"`,
				`"name":"alpha\u202ePRIVATE_SENTINEL"`,
				1,
			),
		},
		{
			name:        "explicit null",
			status:      http.StatusOK,
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"parameter_shape":{}`,
				`"parameter_shape":null`,
				1,
			),
		},
		{
			name:        "upstream unauthorized",
			status:      http.StatusUnauthorized,
			contentType: "application/json",
			body:        `{"error":"PRIVATE_SENTINEL workflow-authoring-pid-token"}`,
		},
		{
			name:        "wrong content type",
			status:      http.StatusOK,
			contentType: "text/plain",
			body:        valid,
		},
		{
			name:  "transport failure",
			doErr: errors.New("PRIVATE_SENTINEL workflow-authoring-pid-token"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withWorkflowAuthoringAPIGateway(t, func(
				*http.Request,
				time.Duration,
			) (*http.Response, error) {
				if test.doErr != nil {
					return nil, test.doErr
				}
				return workflowAuthoringAPIUpstream(
					test.status,
					test.contentType,
					[]byte(test.body),
				), nil
			})
			handler := NewHandler(t.TempDir() + "/missing-config.json")
			recorder := httptest.NewRecorder()
			handler.handleGetWorkflowAuthoringCapabilities(
				recorder,
				httptest.NewRequest(http.MethodGet, workflowAuthoringAPIPath, nil),
			)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if recorder.Header().Get("Cache-Control") != "no-store" ||
				recorder.Header().Get("X-Content-Type-Options") != "nosniff" ||
				recorder.Header().Get("Retry-After") != "1" {
				t.Fatalf("headers = %#v", recorder.Header())
			}
			if body := recorder.Body.String(); body !=
				"{\"error\":\"workflow_authoring_capabilities_unavailable\"}\n" {
				t.Fatalf("error body = %q", body)
			}
			if strings.Contains(recorder.Body.String(), "PRIVATE_SENTINEL") ||
				strings.Contains(recorder.Body.String(), "workflow-authoring-pid-token") {
				t.Fatalf("private upstream data leaked: %s", recorder.Body.String())
			}
		})
	}
}

func TestWorkflowAuthoringCapabilitiesProxyEnforcesExactRequestAndBodyBound(t *testing.T) {
	called := false
	withWorkflowAuthoringAPIGateway(t, func(
		*http.Request,
		time.Duration,
	) (*http.Response, error) {
		called = true
		body := strings.Repeat("x", int(workflows.MaxWorkflowAuthoringResponseBytes)+1)
		return workflowAuthoringAPIUpstream(
			http.StatusOK,
			"application/json",
			[]byte(body),
		), nil
	})
	handler := NewHandler(t.TempDir() + "/missing-config.json")

	queryRecorder := httptest.NewRecorder()
	handler.handleGetWorkflowAuthoringCapabilities(
		queryRecorder,
		httptest.NewRequest(http.MethodGet, workflowAuthoringAPIPath+"?private=1", nil),
	)
	if queryRecorder.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("query response = %d, upstream called = %t", queryRecorder.Code, called)
	}

	bodyRecorder := httptest.NewRecorder()
	handler.handleGetWorkflowAuthoringCapabilities(
		bodyRecorder,
		httptest.NewRequest(http.MethodGet, workflowAuthoringAPIPath, nil),
	)
	if bodyRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("oversized response status = %d", bodyRecorder.Code)
	}
}

func TestWorkflowAuthoringGatewayURLRequiresExplicitValidPIDMetadata(t *testing.T) {
	handler := NewHandler(t.TempDir() + "/config.json")
	tests := []ppid.PidFileData{
		{PID: 1, Token: "token", Host: "", Port: 18790},
		{PID: 1, Token: "token", Host: "127.0.0.1", Port: 0},
		{PID: 1, Token: "token", Host: "bad host", Port: 18790},
		{PID: 1, Token: "token", Host: " 127.0.0.1", Port: 18790},
	}
	for _, pidData := range tests {
		if target, ok := handler.workflowAuthoringGatewayURL(&pidData); ok || target != nil {
			t.Fatalf("workflowAuthoringGatewayURL(%#v) = %v, %t", pidData, target, ok)
		}
	}
	valid, ok := handler.workflowAuthoringGatewayURL(&ppid.PidFileData{
		PID:   1,
		Token: "token",
		Host:  "*",
		Port:  18790,
	})
	if !ok || valid == nil || valid.Port() != "18790" {
		t.Fatalf("valid wildcard PID metadata = %v, %t", valid, ok)
	}
}

func TestWorkflowAuthoringCapabilitiesLookupNeverMutatesPIDOrConfig(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "malformed PID", raw: []byte("not json")},
		{
			name: "stale PID",
			raw: mustWorkflowAuthoringJSON(t, ppid.PidFileData{
				PID:   99999999,
				Token: "stale-token",
				Host:  "127.0.0.1",
				Port:  18790,
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("PICOCLAW_HOME", home)
			pidPath := filepath.Join(home, ".picoclaw.pid")
			if err := os.WriteFile(pidPath, test.raw, 0o600); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(home, "config.json")
			configRaw := []byte(`{"private":"must remain byte exact"}`)
			if err := os.WriteFile(configPath, configRaw, 0o600); err != nil {
				t.Fatal(err)
			}

			gateway.mu.Lock()
			previousCachedPID := gateway.pidData
			gateway.pidData = nil
			gateway.mu.Unlock()
			previousMatcher := gatewayProcessMatcher
			gatewayProcessMatcher = func(int) (bool, bool) { return false, true }
			previousDo := workflowAuthoringGatewayDo
			called := false
			workflowAuthoringGatewayDo = func(
				*http.Request,
				time.Duration,
			) (*http.Response, error) {
				called = true
				return nil, errors.New("must not proxy")
			}
			t.Cleanup(func() {
				gateway.mu.Lock()
				gateway.pidData = previousCachedPID
				gateway.mu.Unlock()
				gatewayProcessMatcher = previousMatcher
				workflowAuthoringGatewayDo = previousDo
			})

			recorder := httptest.NewRecorder()
			NewHandler(configPath).handleGetWorkflowAuthoringCapabilities(
				recorder,
				httptest.NewRequest(http.MethodGet, workflowAuthoringAPIPath, nil),
			)
			if recorder.Code != http.StatusServiceUnavailable || called {
				t.Fatalf("response = %d, upstream called = %t", recorder.Code, called)
			}
			assertWorkflowAuthoringFileBytes(t, pidPath, test.raw)
			assertWorkflowAuthoringFileBytes(t, configPath, configRaw)
			entries, err := os.ReadDir(home)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 {
				t.Fatalf("read-only lookup created files: %#v", entries)
			}
		})
	}
}

func mustWorkflowAuthoringJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertWorkflowAuthoringFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s changed: %q, want %q", path, got, want)
	}
}

func withWorkflowAuthoringAPIGateway(
	t *testing.T,
	do func(*http.Request, time.Duration) (*http.Response, error),
) {
	t.Helper()
	previousDo := workflowAuthoringGatewayDo
	previousPIDData := workflowAuthoringGatewayPIDData
	workflowAuthoringGatewayDo = do
	workflowAuthoringGatewayPIDData = func(
		*Handler,
	) *ppid.PidFileData {
		return &ppid.PidFileData{
			PID:   os.Getpid(),
			Token: "workflow-authoring-pid-token",
			Host:  "127.0.0.1",
			Port:  18789,
		}
	}
	t.Cleanup(func() {
		workflowAuthoringGatewayDo = previousDo
		workflowAuthoringGatewayPIDData = previousPIDData
	})
}

func workflowAuthoringAPIUpstream(
	status int,
	contentType string,
	body []byte,
) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", contentType)
	return &http.Response{
		StatusCode:    status,
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(string(body))),
		ContentLength: int64(len(body)),
	}
}

func workflowAuthoringAPIValidResponse(t *testing.T) []byte {
	t.Helper()
	functions := make([]workflows.WorkflowAuthoringFunctionCapability, 0, 4)
	for _, name := range workflows.NativeFunctionNames() {
		functions = append(functions, workflows.WorkflowAuthoringFunctionCapability{
			Name:      name,
			Target:    "function/" + name,
			Readiness: workflows.WorkflowDependencyReadinessReady,
		})
	}
	catalog := workflows.WorkflowAuthoringCapabilities{
		Complete:  true,
		MCPStatus: workflows.WorkflowAuthoringMCPDisabled,
		Agents: []workflows.WorkflowAuthoringAgentCapability{{
			ID:        "main",
			Target:    "agent/main",
			IsDefault: true,
			Readiness: workflows.WorkflowDependencyReadinessReady,
		}},
		Tools: []workflows.WorkflowAuthoringToolCapability{{
			Name:                    "alpha",
			Target:                  "tool/alpha",
			Readiness:               workflows.WorkflowDependencyReadinessReady,
			ParameterShapeProjected: true,
			ParameterShape:          &workflows.WorkflowAuthoringParameterShape{},
		}},
		MCPTools:  []workflows.WorkflowAuthoringMCPToolCapability{},
		Functions: functions,
		Limits:    []workflows.WorkflowAuthoringLimitCode{},
	}
	encoded, ok := workflows.MarshalWorkflowAuthoringCapabilities(catalog)
	if !ok {
		t.Fatal("valid workflow authoring API catalog did not marshal")
	}
	return encoded
}
