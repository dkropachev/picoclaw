package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestPRLifecycleEndpointsFailClosedWithoutConfiguration(t *testing.T) {
	// A directory at the configured file path is an unreadable configuration,
	// rather than a legitimate first-run missing file that loads defaults.
	handler := NewHandler(t.TempDir())
	mux := http.NewServeMux()
	handler.registerPRLifecycleWorkflowConfigurationRoutes(mux)

	for _, path := range []string{
		prLifecycleWorkflowConfigurationsPath,
		prLifecycleRepositoryAssignmentsPath,
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://launcher.local"+path, nil))
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, PUT" {
			t.Fatalf("POST %s status = %d, Allow = %q", path, response.Code, response.Header().Get("Allow"))
		}

		response = httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://launcher.local"+path+"?unexpected=1", nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("queried GET %s status = %d body=%s", path, response.Code, response.Body.String())
		}

		response = httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://launcher.local"+path, nil))
		if response.Code != http.StatusInternalServerError ||
			!strings.Contains(response.Body.String(), "configuration_unavailable") {
			t.Fatalf("GET %s status = %d body=%s", path, response.Code, response.Body.String())
		}
	}

	workflowPut := putPRLifecycleWorkflowConfigurationsForTest(
		t,
		mux,
		"sha256:missing-configuration",
		config.DefaultPRLifecycleConfig(),
		nil,
	)
	if workflowPut.Code != http.StatusInternalServerError ||
		!strings.Contains(workflowPut.Body.String(), "configuration_unavailable") {
		t.Fatalf("workflow PUT status = %d body=%s", workflowPut.Code, workflowPut.Body.String())
	}
	assignmentPut := putPRLifecycleRepositoryAssignmentsForTest(
		t,
		mux,
		"sha256:missing-configuration",
		map[string]string{},
		nil,
	)
	if assignmentPut.Code != http.StatusInternalServerError ||
		!strings.Contains(assignmentPut.Body.String(), "configuration_unavailable") {
		t.Fatalf("assignment PUT status = %d body=%s", assignmentPut.Code, assignmentPut.Body.String())
	}
}

func TestPRLifecycleRequestValidationRejectsAmbiguousValues(t *testing.T) {
	if validPRLifecycleWorkflowConfigurationRequest(nil) ||
		validPRLifecycleScopedRequest(&http.Request{}, prLifecycleWorkflowConfigurationsPath) {
		t.Fatal("nil lifecycle request was accepted")
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"http://launcher.local"+prLifecycleWorkflowConfigurationsPath,
		nil,
	)
	if !validPRLifecycleWorkflowConfigurationRequest(request) {
		t.Fatal("canonical lifecycle request was rejected")
	}
	request.URL.ForceQuery = true
	if validPRLifecycleWorkflowConfigurationRequest(request) {
		t.Fatal("force-query lifecycle request was accepted")
	}

	for _, value := range []string{
		"short",
		" request-identifier-with-space",
		"request-identifier-with-slash/",
		strings.Repeat("a", 129),
	} {
		if validPRLifecycleRequestID(value) {
			t.Fatalf("invalid lifecycle request ID %q was accepted", value)
		}
	}
	if !validPRLifecycleRequestID("request.identifier:with-valid_chars-01") {
		t.Fatal("valid lifecycle request ID was rejected")
	}
	if err := validatePRLifecycleWorkflowConfigurations(
		context.Background(),
		config.DefaultPRLifecycleConfig(),
		nil,
	); err == nil {
		t.Fatal("nil configuration was accepted for lifecycle validation")
	}
	if err := validatePRLifecycleGateActionWorkflows(
		context.Background(),
		config.DefaultPRLifecycleConfig(),
		nil,
	); err == nil {
		t.Fatal("nil configuration was accepted for action-workflow validation")
	}
}

func TestPRLifecycleRepositoryAssignmentsRejectMalformedMutationEnvelopes(t *testing.T) {
	_, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	current, _ := getPRLifecycleRepositoryAssignmentsForTest(t, mux)

	query := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, current.ConfigRevision, map[string]string{},
		func(request *http.Request) { request.URL.RawQuery = "unexpected=1" },
	)
	if query.Code != http.StatusBadRequest {
		t.Fatalf("queried assignment PUT status = %d body=%s", query.Code, query.Body.String())
	}
	contentType := putPRLifecycleRepositoryAssignmentsForTest(
		t, mux, current.ConfigRevision, map[string]string{},
		func(request *http.Request) { request.Header.Set("Content-Type", "text/plain") },
	)
	if contentType.Code != http.StatusBadRequest {
		t.Fatalf("plain-text assignment PUT status = %d body=%s", contentType.Code, contentType.Body.String())
	}

	invalidEnvelope, err := json.Marshal(prLifecycleRepositoryAssignmentsPutRequest{
		ExpectedConfigRevision: current.ConfigRevision,
		RequestID:              "invalid request identifier",
		RepositoryAssignments:  map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPut,
		"http://launcher.local"+prLifecycleRepositoryAssignmentsPath,
		bytes.NewReader(invalidEnvelope),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	invalid := httptest.NewRecorder()
	mux.ServeHTTP(invalid, request)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid-envelope assignment PUT status = %d body=%s", invalid.Code, invalid.Body.String())
	}
}

func TestPRLifecycleCatalogAndRevisionValidationRejectsInvalidBindings(t *testing.T) {
	cfg := config.DefaultConfig()
	unknown := config.DefaultPRLifecycleConfig()
	defaultConfiguration := unknown.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID]
	defaultConfiguration.Bindings = []config.PRLifecycleGateBinding{{
		WorkflowRef: "workflows/pr-lifecycle.yml",
		GateRef:     "gates.not-published",
	}}
	unknown.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID] = defaultConfiguration
	if err := validatePRLifecycleWorkflowConfigurations(context.Background(), unknown, cfg); err == nil {
		t.Fatal("unpublished workflow gate was accepted")
	}

	invalidFields := config.DefaultPRLifecycleConfig()
	defaultConfiguration = invalidFields.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID]
	defaultConfiguration.Bindings = []config.PRLifecycleGateBinding{{
		WorkflowRef: "workflows/pr-lifecycle.yml",
		GateRef:     "gates.review-start",
		Action: &gatetypes.GateAction{
			Type:   gatetypes.GateActionDeterministic,
			Fields: map[string]any{"action": "not-a-published-option"},
		},
	}}
	invalidFields.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID] = defaultConfiguration
	if err := validatePRLifecycleGateCatalogBindings(invalidFields); err == nil {
		t.Fatal("invalid deterministic gate fields were accepted")
	}
	if clonePRLifecycleGateCatalogAction(nil) != nil {
		t.Fatal("nil gate action clone was not nil")
	}

	canonical := config.DefaultPRLifecycleConfig()
	automatic := canonical.WorkflowConfigurations[config.DefaultPRLifecycleWorkflowConfigurationID]
	automatic.Name = "Automatic"
	automatic.DeferredIssues.Mode = config.PRLifecycleDeferredIssuesAutomatic
	automatic.Bindings = []config.PRLifecycleGateBinding{
		{WorkflowRef: "workflows/z.yml", GateRef: "gates.a"},
		{WorkflowRef: "workflows/a.yml", GateRef: "gates.z"},
		{WorkflowRef: "workflows/a.yml", GateRef: "gates.a"},
	}
	canonical.WorkflowConfigurations["automatic"] = automatic
	canonical.RepositoryAssignments = map[string]string{
		"malformed":                     "automatic",
		"not-a-provider|repo":           "automatic",
		"HTTPS://GITHUB.COM///|EXAMPLE": "automatic",
	}
	if revision := prLifecycleDeferredPolicyRevision(canonical); !strings.HasPrefix(revision, "sha256:") {
		t.Fatalf("deferred policy revision = %q", revision)
	}
	canonical = canonicalPRLifecycleRevisionInput(canonical)
	if canonical.RepositoryAssignments["https://github.com|example"] != "automatic" ||
		len(canonical.RepositoryAssignments) != 1 {
		t.Fatalf("canonical assignments = %#v", canonical.RepositoryAssignments)
	}
}

func TestPRWorkspaceOriginAndCanonicalPathValidation(t *testing.T) {
	if !prWorkspaceMutationCrossSite(nil) {
		t.Fatal("nil mutation request was accepted")
	}
	request := validPRWorkspaceMutationRequest(strings.NewReader(`{}`))
	request.Header["Origin"] = []string{"http://launcher.local", "http://launcher.local"}
	if !prWorkspaceMutationCrossSite(request) {
		t.Fatal("duplicate Origin headers were accepted")
	}
	request = validPRWorkspaceMutationRequest(strings.NewReader(`{}`))
	request.Header.Del("Sec-Fetch-Site")
	request.Header.Set("Origin", "http://attacker.invalid")
	if !prWorkspaceMutationCrossSite(request) {
		t.Fatal("foreign Origin was accepted")
	}
	request.Header.Set("Origin", "http://launcher.local")
	if prWorkspaceMutationCrossSite(request) {
		t.Fatal("same-origin Origin was rejected")
	}
	if location, ok := externalPRWorkspaceLocation(prWorkspaceRuntimePath); !ok || location != prWorkspaceAPIPath {
		t.Fatalf("collection location = %q, %t", location, ok)
	}

	called := false
	guarded := GuardPRWorkspaceCanonicalPaths(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	guarded.ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "http://launcher.local"+prWorkspaceAPIPath+"/"+testPRWorkspaceID, nil),
	)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("canonical path status = %d called = %t", response.Code, called)
	}
	if noncanonicalPRWorkspacePath(nil) || pathTraversesPRWorkspace("/api/unrelated") {
		t.Fatal("unrelated path was classified as a PR-workspace traversal")
	}
}

func TestPRWorkspaceTransportBoundaryRejectsMalformedRequests(t *testing.T) {
	called := false
	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		called = true
		return eventUpstreamResponse(http.StatusOK, `{}`), nil
	})
	handler := NewHandler(savedPRWorkspaceTestConfig(t))

	tests := []struct {
		name   string
		build  func() *http.Request
		status int
	}{
		{name: "nil request", build: func() *http.Request { return nil }, status: http.StatusBadRequest},
		{
			name:   "nil URL",
			build:  func() *http.Request { return &http.Request{Method: http.MethodGet} },
			status: http.StatusBadRequest,
		},
		{
			name: "outside route",
			build: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "http://launcher.local/api/not-pr-workspaces", nil)
			},
			status: http.StatusBadRequest,
		},
		{
			name: "unsupported method",
			build: func() *http.Request {
				return httptest.NewRequest(http.MethodDelete, "http://launcher.local"+prWorkspaceAPIPath, nil)
			},
			status: http.StatusMethodNotAllowed,
		},
		{
			name: "GET body",
			build: func() *http.Request {
				return httptest.NewRequest(
					http.MethodGet,
					"http://launcher.local"+prWorkspaceAPIPath,
					strings.NewReader(`{}`),
				)
			},
			status: http.StatusBadRequest,
		},
		{
			name: "mutation query",
			build: func() *http.Request {
				request := validPRWorkspaceMutationRequest(strings.NewReader(`{}`))
				request.URL.RawQuery = "unexpected=1"
				return request
			},
			status: http.StatusBadRequest,
		},
		{
			name: "missing origin proof",
			build: func() *http.Request {
				request := validPRWorkspaceMutationRequest(strings.NewReader(`{}`))
				request.Header.Del("Sec-Fetch-Site")
				return request
			},
			status: http.StatusForbidden,
		},
		{
			name: "invalid content type",
			build: func() *http.Request {
				request := validPRWorkspaceMutationRequest(strings.NewReader(`{}`))
				request.Header.Set("Content-Type", "text/plain")
				return request
			},
			status: http.StatusBadRequest,
		},
		{
			name: "nil body",
			build: func() *http.Request {
				request := validPRWorkspaceMutationRequest(strings.NewReader(`{}`))
				request.Body = nil
				return request
			},
			status: http.StatusBadRequest,
		},
		{
			name: "declared oversized body",
			build: func() *http.Request {
				request := validPRWorkspaceMutationRequest(strings.NewReader(`{}`))
				request.ContentLength = prWorkspaceMaxBodyBytes + 1
				return request
			},
			status: http.StatusBadRequest,
		},
		{
			name: "empty body",
			build: func() *http.Request {
				return validPRWorkspaceMutationRequest(strings.NewReader(""))
			},
			status: http.StatusBadRequest,
		},
		{
			name: "body read error",
			build: func() *http.Request {
				request := validPRWorkspaceMutationRequest(strings.NewReader(`{}`))
				request.Body = failingPRWorkspaceReadCloser{}
				request.ContentLength = -1
				return request
			},
			status: http.StatusBadRequest,
		},
		{
			name: "actual oversized body",
			build: func() *http.Request {
				request := validPRWorkspaceMutationRequest(
					bytes.NewReader(bytes.Repeat([]byte("x"), prWorkspaceMaxBodyBytes+1)),
				)
				request.ContentLength = -1
				return request
			},
			status: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called = false
			testResponse := httptest.NewRecorder()
			handler.handlePRWorkspaceProxy(testResponse, test.build())
			if testResponse.Code != test.status || called {
				t.Fatalf("status = %d, called = %t, body=%s", testResponse.Code, called, testResponse.Body.String())
			}
		})
	}
}

func TestPRWorkspaceProxyRejectsUntrustedGatewayResponses(t *testing.T) {
	var upstream *http.Response
	var upstreamErr error
	called := false
	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		called = true
		return upstream, upstreamErr
	})
	handler := NewHandler(savedPRWorkspaceTestConfig(t))
	request := httptest.NewRequest(http.MethodGet, "http://launcher.local"+prWorkspaceAPIPath, nil)

	call := func(method, upstreamPath string) *httptest.ResponseRecorder {
		t.Helper()
		called = false
		response := httptest.NewRecorder()
		handler.proxyPRWorkspaceGateway(
			response,
			request,
			method,
			upstreamPath,
			"",
			nil,
			prWorkspaceReadTimeout,
		)
		return response
	}

	originalPIDData := prWorkspaceGatewayPIDData
	prWorkspaceGatewayPIDData = func() *ppid.PidFileData { return nil }
	response := call(http.MethodGet, prWorkspaceRuntimePath)
	prWorkspaceGatewayPIDData = originalPIDData
	if response.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("missing PID status = %d, called = %t", response.Code, called)
	}

	response = call(http.MethodGet, "/runtime/not-eventing")
	if response.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("invalid target status = %d, called = %t", response.Code, called)
	}
	response = call("invalid method", prWorkspaceRuntimePath)
	if response.Code != http.StatusBadGateway || called {
		t.Fatalf("invalid method status = %d, called = %t", response.Code, called)
	}

	tests := []struct {
		name        string
		response    http.Response
		hasResponse bool
		err         error
		status      int
	}{
		{name: "transport error", err: errors.New("gateway unavailable"), status: http.StatusServiceUnavailable},
		{name: "nil response", status: http.StatusBadGateway},
		{
			name:        "nil response body",
			response:    http.Response{StatusCode: http.StatusOK},
			hasResponse: true,
			status:      http.StatusBadGateway,
		},
		{
			name:        "unauthorized",
			response:    prWorkspaceGatewayResponse(http.StatusUnauthorized, `{}`),
			hasResponse: true,
			status:      http.StatusServiceUnavailable,
		},
		{
			name:        "redirect",
			response:    prWorkspaceGatewayResponse(http.StatusFound, `{}`),
			hasResponse: true,
			status:      http.StatusBadGateway,
		},
		{
			name:        "invalid status",
			response:    prWorkspaceGatewayResponse(700, `{}`),
			hasResponse: true,
			status:      http.StatusBadGateway,
		},
		{
			name: "declared oversized response",
			response: func() http.Response {
				result := prWorkspaceGatewayResponse(http.StatusOK, `{}`)
				result.ContentLength = prWorkspaceProxyResponseMaxBytes + 1
				return result
			}(),
			hasResponse: true,
			status:      http.StatusBadGateway,
		},
		{
			name: "response read error",
			response: http.Response{
				StatusCode:    http.StatusOK,
				Header:        http.Header{"Content-Type": {"application/json"}},
				Body:          failingPRWorkspaceReadCloser{},
				ContentLength: -1,
			},
			hasResponse: true,
			status:      http.StatusBadGateway,
		},
		{
			name:        "invalid JSON",
			response:    prWorkspaceGatewayResponse(http.StatusOK, `not-json`),
			hasResponse: true,
			status:      http.StatusBadGateway,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream = nil
			if test.hasResponse {
				responseCopy := test.response
				upstream = &responseCopy
			}
			upstreamErr = test.err
			testResponse := call(http.MethodGet, prWorkspaceRuntimePath)
			if upstream != nil && upstream.Body != nil {
				_ = upstream.Body.Close()
			}
			if testResponse.Code != test.status || !called {
				t.Fatalf("status = %d, called = %t, body=%s", testResponse.Code, called, testResponse.Body.String())
			}
		})
	}

	upstreamErr = nil
	upstreamResponse := prWorkspaceGatewayResponse(http.StatusServiceUnavailable, `{"code":"busy"}`)
	upstream = &upstreamResponse
	upstream.Header["Location"] = []string{prWorkspaceRuntimePath, prWorkspaceRuntimePath + "/" + testPRWorkspaceID}
	response = call(http.MethodGet, prWorkspaceRuntimePath)
	_ = upstream.Body.Close()
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" ||
		response.Header().Get("Location") != "" {
		t.Fatalf(
			"503 response status = %d headers=%v body=%s",
			response.Code,
			response.Header(),
			response.Body.String(),
		)
	}
}

func validPRWorkspaceMutationRequest(body io.Reader) *http.Request {
	request := httptest.NewRequest(
		http.MethodPost,
		"http://launcher.local"+prWorkspaceAPIPath+"/intake",
		body,
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	return request
}

func savedPRWorkspaceTestConfig(t *testing.T) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, config.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func prWorkspaceGatewayResponse(status int, body string) http.Response {
	return http.Response{
		StatusCode:    status,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

type failingPRWorkspaceReadCloser struct{}

func (failingPRWorkspaceReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (failingPRWorkspaceReadCloser) Close() error {
	return nil
}
