package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	workflowEditorAPIEventID = "ev_cccccccccccccccccccccccccccccccc"
	workflowEditorAPIYAML    = `# keep workflow comment
name: Event editor API
on:
  schedule:
    - cron: "0 8 * * *" # keep schedule comment
  event:
    sources: github
    connectors: primary
    types: issues.*
    actor:
      types: bot
    subject:
      ids: repository-1
    attributes:
      repository: acme/*
jobs:
  main: # keep job comment
    runs-on: picoclaw
    steps:
      - uses: tool/message
`
)

func TestWorkflowEventTriggerInspectRoute(t *testing.T) {
	h := NewHandler("")
	mux := http.NewServeMux()
	h.registerWorkflowEditorRoutes(mux)

	rec := serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/event-trigger/inspect",
		map[string]any{"yaml": workflowEditorAPIYAML},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertWorkflowEventTriggerResponseHeaders(t, rec)

	var response workflows.WorkflowEventTriggerInspection
	decodeWorkflowEventTriggerTestResponse(t, rec, &response)
	if !response.Editable || response.Reason != "" {
		t.Fatalf("inspection editability = %#v", response)
	}
	if !strings.HasPrefix(response.Revision, "sha256:") {
		t.Fatalf("revision = %q", response.Revision)
	}
	if response.Validation == nil || !response.Validation.Valid {
		t.Fatalf("validation = %#v", response.Validation)
	}
	if response.EventTrigger == nil ||
		len(response.EventTrigger.Sources) != 1 ||
		response.EventTrigger.Sources[0] != "github" {
		t.Fatalf("event_trigger = %#v", response.EventTrigger)
	}
}

func TestWorkflowEventTriggerRenderRoutePreservesYAMLAndRevision(t *testing.T) {
	h := NewHandler("")
	mux := http.NewServeMux()
	h.registerWorkflowEditorRoutes(mux)
	inspection := workflows.InspectWorkflowEventTrigger(workflowEditorAPIYAML)

	rec := serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/event-trigger/render",
		map[string]any{
			"yaml":     workflowEditorAPIYAML,
			"revision": inspection.Revision,
			"event_trigger": map[string]any{
				"sources":    []string{"gmail"},
				"connectors": []string{"support"},
				"types":      []string{"message.received"},
			},
		},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertWorkflowEventTriggerResponseHeaders(t, rec)

	var response workflowEventTriggerRenderResponse
	decodeWorkflowEventTriggerTestResponse(t, rec, &response)
	if response.Revision == inspection.Revision {
		t.Fatal("render did not return a new revision")
	}
	if response.EventTrigger == nil ||
		response.EventTrigger.Sources[0] != "gmail" ||
		response.EventTrigger.Connectors[0] != "support" {
		t.Fatalf("event_trigger = %#v", response.EventTrigger)
	}
	if response.Validation == nil || !response.Validation.Valid || !response.Editable {
		t.Fatalf("render response = %#v", response)
	}
	for _, comment := range []string{
		"# keep workflow comment",
		"# keep schedule comment",
		"# keep job comment",
	} {
		if !strings.Contains(response.YAML, comment) {
			t.Errorf("rendered YAML lost %q:\n%s", comment, response.YAML)
		}
	}
	parsed, err := workflows.Parse([]byte(response.YAML))
	if err != nil {
		t.Fatalf("Parse(rendered) error = %v", err)
	}
	if len(parsed.On.Schedule) != 1 || parsed.On.Schedule[0].Cron != "0 8 * * *" {
		t.Fatalf("unrelated schedule = %#v", parsed.On.Schedule)
	}
	if _, exists := parsed.Jobs["main"]; !exists {
		t.Fatalf("unrelated jobs = %#v", parsed.Jobs)
	}

	remove := serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/event-trigger/render",
		map[string]any{
			"yaml":          response.YAML,
			"revision":      response.Revision,
			"event_trigger": nil,
		},
	)
	if remove.Code != http.StatusOK {
		t.Fatalf("remove status = %d, body = %s", remove.Code, remove.Body.String())
	}
	var removed workflowEventTriggerRenderResponse
	decodeWorkflowEventTriggerTestResponse(t, remove, &removed)
	if removed.EventTrigger != nil {
		t.Fatalf("removed event_trigger = %#v", removed.EventTrigger)
	}
	parsed, err = workflows.Parse([]byte(removed.YAML))
	if err != nil || parsed.On.Event != nil || len(parsed.On.Schedule) != 1 {
		t.Fatalf("removed YAML parse = %#v, err = %v", parsed, err)
	}
}

func TestWorkflowEventTriggerRenderRouteRejectsStaleAndInvalidRequests(t *testing.T) {
	h := NewHandler("")
	mux := http.NewServeMux()
	h.registerWorkflowEditorRoutes(mux)
	inspection := workflows.InspectWorkflowEventTrigger(workflowEditorAPIYAML)

	tests := []struct {
		name       string
		body       string
		content    string
		wantStatus int
		wantError  string
	}{
		{
			name: "stale revision",
			body: mustWorkflowEventTriggerJSON(t, map[string]any{
				"yaml":          workflowEditorAPIYAML + "\n",
				"revision":      inspection.Revision,
				"event_trigger": nil,
			}),
			content:    "application/json",
			wantStatus: http.StatusConflict,
			wantError:  "changed",
		},
		{
			name: "invalid trigger",
			body: mustWorkflowEventTriggerJSON(t, map[string]any{
				"yaml":     workflowEditorAPIYAML,
				"revision": inspection.Revision,
				"event_trigger": map[string]any{
					"sources": []string{},
				},
			}),
			content:    "application/json",
			wantStatus: http.StatusUnprocessableEntity,
			wantError:  "cannot be rendered",
		},
		{
			name: "missing explicit nullable field",
			body: mustWorkflowEventTriggerJSON(t, map[string]any{
				"yaml":     workflowEditorAPIYAML,
				"revision": inspection.Revision,
			}),
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantError:  "event_trigger",
		},
		{
			name: "unknown nested trigger field",
			body: mustWorkflowEventTriggerJSON(t, map[string]any{
				"yaml":     workflowEditorAPIYAML,
				"revision": inspection.Revision,
				"event_trigger": map[string]any{
					"sources": []string{"github"},
					"secret":  "must not be accepted",
				},
			}),
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantError:  "valid event trigger",
		},
		{
			name: "multiple JSON values",
			body: mustWorkflowEventTriggerJSON(t, map[string]any{
				"yaml":          workflowEditorAPIYAML,
				"revision":      inspection.Revision,
				"event_trigger": nil,
			}) + `{}`,
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid JSON",
		},
		{
			name:       "wrong content type",
			body:       `{}`,
			content:    "text/plain",
			wantStatus: http.StatusUnsupportedMediaType,
			wantError:  "Content-Type",
		},
		{
			name: "oversized",
			body: `{"yaml":"` +
				strings.Repeat("x", workflowEventTriggerRequestMaxBytes) +
				`","revision":"x","event_trigger":null}`,
			content:    "application/json",
			wantStatus: http.StatusRequestEntityTooLarge,
			wantError:  "exceeds",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/workflows/development/event-trigger/render",
				strings.NewReader(test.body),
			)
			req.Header.Set("Content-Type", test.content)
			mux.ServeHTTP(rec, req)
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			assertWorkflowEventTriggerResponseHeaders(t, rec)
			var response workflowEventTriggerErrorResponse
			decodeWorkflowEventTriggerTestResponse(t, rec, &response)
			if !strings.Contains(response.Error, test.wantError) {
				t.Fatalf("error = %q, want substring %q", response.Error, test.wantError)
			}
			if test.name == "invalid trigger" {
				if response.Validation == nil ||
					response.Validation.Valid ||
					len(response.Validation.Errors) == 0 {
					t.Fatalf("structured validation = %#v", response.Validation)
				}
			}
		})
	}
}

func TestWorkflowEventTriggerMatchRouteUsesAuthoritativeEvaluator(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	upstreamCalls := 0
	installEventProxyStubs(t, func(req *http.Request, _ time.Duration) (*http.Response, error) {
		upstreamCalls++
		if req.Method != http.MethodGet ||
			req.URL.Path != "/runtime/eventing/events/"+workflowEditorAPIEventID {
			t.Fatalf("upstream request = %s %s", req.Method, req.URL.Path)
		}
		if req.Header.Get("Authorization") != "Bearer gateway-pid-token" {
			t.Fatalf("upstream authorization = %q", req.Header.Get("Authorization"))
		}
		return eventUpstreamResponse(http.StatusOK, `{
			"id":"`+workflowEditorAPIEventID+`",
			"source":"GitHub",
			"connector":"PRIMARY",
			"type":"issues.opened",
			"actor":{"id":"actor-1","type":"BOT"},
			"subject":{"id":"repository-1","type":"repository"},
			"received_at":"2026-07-29T12:00:00Z",
			"attributes":{"repository":"acme/picoclaw"},
			"payload_bytes":0,
			"routing":{
				"status":"succeeded",
				"available_at":"2026-07-29T12:00:00Z",
				"attempts":1,
				"updated_at":"2026-07-29T12:00:00Z"
			}
		}`), nil
	})

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.registerWorkflowEditorRoutes(mux)
	rec := serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/event-trigger/match",
		map[string]any{
			"yaml":     workflowEditorAPIYAML,
			"event_id": workflowEditorAPIEventID,
		},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertWorkflowEventTriggerResponseHeaders(t, rec)

	var response workflowEventTriggerMatchResponse
	decodeWorkflowEventTriggerTestResponse(t, rec, &response)
	if response.EventID != workflowEditorAPIEventID || !response.Matched {
		t.Fatalf("match response = %#v", response)
	}
	if response.Validation == nil || !response.Validation.Valid {
		t.Fatalf("validation = %#v", response.Validation)
	}
	wantPaths := []string{
		"on.event.actor.types",
		"on.event.attributes.repository",
		"on.event.connectors",
		"on.event.sources",
		"on.event.subject.ids",
		"on.event.types",
	}
	if len(response.Checks) != len(wantPaths) {
		t.Fatalf("checks = %#v", response.Checks)
	}
	for index, want := range wantPaths {
		if response.Checks[index].Path != want ||
			!response.Checks[index].Present ||
			!response.Checks[index].Matched {
			t.Errorf("checks[%d] = %#v, want matched %q", index, response.Checks[index], want)
		}
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
	}
}

func TestWorkflowEventTriggerMatchRouteValidatesBeforeLoadingEvent(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	upstreamCalls := 0
	installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
		upstreamCalls++
		return nil, errors.New("must not be called")
	})
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.registerWorkflowEditorRoutes(mux)

	invalidYAML := strings.Replace(
		workflowEditorAPIYAML,
		"    sources: github",
		"    sources: []",
		1,
	)
	rec := serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/event-trigger/match",
		map[string]any{
			"yaml":     invalidYAML,
			"event_id": workflowEditorAPIEventID,
		},
	)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response workflowEventTriggerMatchResponse
	decodeWorkflowEventTriggerTestResponse(t, rec, &response)
	if response.Validation == nil ||
		response.Validation.Valid ||
		len(response.Validation.Errors) == 0 {
		t.Fatalf("validation = %#v", response.Validation)
	}
	if response.Checks == nil || len(response.Checks) != 0 {
		t.Fatalf("checks = %#v, want explicit empty array", response.Checks)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
	}
}

func TestWorkflowEventTriggerMatchRouteMapsEventErrorsOpaquely(t *testing.T) {
	tests := []struct {
		name       string
		eventID    string
		upstream   func(*http.Request, time.Duration) (*http.Response, error)
		wantStatus int
		wantError  string
		wantRetry  string
	}{
		{
			name:       "invalid ID",
			eventID:    "../private",
			wantStatus: http.StatusBadRequest,
			wantError:  "invalid event request",
		},
		{
			name:    "not found",
			eventID: workflowEditorAPIEventID,
			upstream: func(*http.Request, time.Duration) (*http.Response, error) {
				return eventUpstreamResponse(
					http.StatusNotFound,
					`{"error":"upstream private diagnostic"}`,
				), nil
			},
			wantStatus: http.StatusNotFound,
			wantError:  "event not found",
		},
		{
			name:    "unavailable",
			eventID: workflowEditorAPIEventID,
			upstream: func(*http.Request, time.Duration) (*http.Response, error) {
				return nil, errors.New("gateway private diagnostic")
			},
			wantStatus: http.StatusServiceUnavailable,
			wantError:  "event service unavailable",
			wantRetry:  "1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()
			upstreamCalls := 0
			installEventProxyStubs(t, func(req *http.Request, timeout time.Duration) (*http.Response, error) {
				upstreamCalls++
				return test.upstream(req, timeout)
			})

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.registerWorkflowEditorRoutes(mux)
			rec := serveWorkflowEventTriggerJSON(
				t,
				mux,
				"/api/workflows/development/event-trigger/match",
				map[string]any{
					"yaml":     workflowEditorAPIYAML,
					"event_id": test.eventID,
				},
			)
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			assertWorkflowEventTriggerResponseHeaders(t, rec)
			if got := rec.Header().Get("Retry-After"); got != test.wantRetry {
				t.Fatalf("Retry-After = %q, want %q", got, test.wantRetry)
			}
			var response struct {
				Error string `json:"error"`
			}
			decodeWorkflowEventTriggerTestResponse(t, rec, &response)
			if response.Error != test.wantError {
				t.Fatalf("error = %q, want %q", response.Error, test.wantError)
			}
			if strings.Contains(rec.Body.String(), "private diagnostic") {
				t.Fatalf("response leaked upstream error: %s", rec.Body.String())
			}
			if test.name == "invalid ID" && upstreamCalls != 0 {
				t.Fatalf("invalid ID made %d upstream calls", upstreamCalls)
			}
		})
	}
}

func serveWorkflowEventTriggerJSON(
	t *testing.T,
	mux http.Handler,
	path string,
	value any,
) *httptest.ResponseRecorder {
	t.Helper()
	body := mustWorkflowEventTriggerJSON(t, value)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	return rec
}

func mustWorkflowEventTriggerJSON(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(body)
}

func decodeWorkflowEventTriggerTestResponse(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	destination any,
) {
	t.Helper()
	decoder := json.NewDecoder(rec.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		t.Fatalf("decode response error = %v, body = %s", err, rec.Body.String())
	}
}

func assertWorkflowEventTriggerResponseHeaders(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}
