package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowTriggersAPIYAML = `# keep generic root
name: Generic trigger API
on:
  manual: {}
  schedule:
    - cron: "0 8 * * *" # keep generic schedule
  channel_message:
    channels: [slack]
  command:
    name: deploy
    args:
      force:
        type: boolean
        default: false
  runtime_event:
    kinds: [workflow.started]
  event:
    sources: github
  workflow_call:
    inputs:
      count:
        type: number
        default: 7
jobs:
  main: # keep generic job
    runs-on: picoclaw
    steps:
      - uses: tool/message
`

func TestWorkflowTriggersInspectRouteReturnsAllLosslessProjections(t *testing.T) {
	mux := newWorkflowTriggersTestMux()
	rec := serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/inspect",
		map[string]any{"yaml": workflowTriggersAPIYAML},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertWorkflowEventTriggerResponseHeaders(t, rec)

	var response map[string]any
	decodeWorkflowEventTriggerTestResponse(t, rec, &response)
	if revision, _ := response["revision"].(string); !strings.HasPrefix(revision, "sha256:") {
		t.Fatalf("revision = %#v", response["revision"])
	}
	triggers, ok := response["triggers"].(map[string]any)
	if !ok || len(triggers) != 7 {
		t.Fatalf("triggers = %#v", response["triggers"])
	}
	for _, kind := range []string{
		"manual",
		"schedule",
		"channel_message",
		"command",
		"runtime_event",
		"event",
		"workflow_call",
	} {
		projection, exists := triggers[kind].(map[string]any)
		if !exists ||
			projection["present"] != true ||
			projection["editable"] != true ||
			projection["value"] == nil {
			t.Errorf("%s projection = %#v", kind, triggers[kind])
		}
	}
}

func TestWorkflowTriggersInspectRoutePreservesNormalizationProneValuesAsRawOnly(
	t *testing.T,
) {
	mux := newWorkflowTriggersTestMux()
	raw := `name: Presence
on:
  command:
    name: deploy
    channels: []
    args:
      force:
        type: boolean
        required: false
    conversation: {}
jobs: {}
`
	rec := serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/inspect",
		map[string]any{"yaml": raw},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	decodeWorkflowEventTriggerTestResponse(t, rec, &response)
	command := response["triggers"].(map[string]any)["command"].(map[string]any)
	if command["present"] != true ||
		command["editable"] != false ||
		!strings.Contains(command["reason"].(string), "field presence") {
		t.Fatalf("command projection = %#v", command)
	}
	value := command["value"].(map[string]any)
	if channels, exists := value["channels"].([]any); !exists || len(channels) != 0 {
		t.Fatalf("explicit empty channels = %#v, present=%t", value["channels"], exists)
	}
	if conversation, exists := value["conversation"].(map[string]any); !exists ||
		len(conversation) != 0 {
		t.Fatalf(
			"explicit empty conversation = %#v, present=%t",
			value["conversation"],
			exists,
		)
	}
	required, exists := value["args"].(map[string]any)["force"].(map[string]any)["required"]
	if !exists || required != false {
		t.Fatalf("explicit false required = %#v, present=%t", required, exists)
	}
}

func TestWorkflowTriggersRenderRouteAddsAllKindsAndDeletesExplicitNull(t *testing.T) {
	mux := newWorkflowTriggersTestMux()
	base := `# generic add
name: Generic add
on: {}
jobs:
  main: # generic add job
    runs-on: picoclaw
    steps:
      - uses: tool/message
`
	replacements := map[string]any{
		"manual":          map[string]any{},
		"schedule":        []any{map[string]any{"cron": "30 10 * * *"}},
		"channel_message": map[string]any{"channels": []string{"discord"}},
		"command":         map[string]any{"name": "release"},
		"runtime_event":   map[string]any{"kinds": []string{"workflow.completed"}},
		"event":           map[string]any{"sources": []string{"gmail"}},
		"workflow_call": map[string]any{"inputs": map[string]any{
			"target": map[string]any{"type": "string", "required": true},
		}},
	}
	for kind, replacement := range replacements {
		t.Run(kind, func(t *testing.T) {
			inspection := workflows.InspectWorkflowTriggers(base)
			add := serveWorkflowEventTriggerJSON(
				t,
				mux,
				"/api/workflows/development/triggers/render",
				map[string]any{
					"yaml":         base,
					"revision":     inspection.Revision,
					"trigger_type": kind,
					"trigger":      replacement,
				},
			)
			if add.Code != http.StatusOK {
				t.Fatalf("add status = %d, body = %s", add.Code, add.Body.String())
			}
			assertWorkflowEventTriggerResponseHeaders(t, add)
			var added workflowTriggerRenderResponse
			decodeWorkflowEventTriggerTestResponse(t, add, &added)
			selected := added.Triggers[workflows.WorkflowTriggerKind(kind)]
			if !selected.Present || !selected.Editable || selected.Value == nil {
				t.Fatalf("added projection = %#v", selected)
			}
			if len(added.Triggers) != 7 ||
				!strings.Contains(added.YAML, "# generic add") ||
				!strings.Contains(added.YAML, "# generic add job") {
				t.Fatalf("add response = %#v\n%s", added, added.YAML)
			}

			remove := serveWorkflowEventTriggerJSON(
				t,
				mux,
				"/api/workflows/development/triggers/render",
				map[string]any{
					"yaml":         added.YAML,
					"revision":     added.Revision,
					"trigger_type": kind,
					"trigger":      nil,
				},
			)
			if remove.Code != http.StatusOK {
				t.Fatalf("delete status = %d, body = %s", remove.Code, remove.Body.String())
			}
			var removed workflowTriggerRenderResponse
			decodeWorkflowEventTriggerTestResponse(t, remove, &removed)
			if removed.Triggers[workflows.WorkflowTriggerKind(kind)].Present {
				t.Fatalf(
					"deleted projection = %#v",
					removed.Triggers[workflows.WorkflowTriggerKind(kind)],
				)
			}
		})
	}
}

func TestWorkflowTriggersRoutesIsolateMalformedSiblingFamilies(t *testing.T) {
	mux := newWorkflowTriggersTestMux()
	raw := `name: Isolated API trigger
on:
  schedule:
    cron: "0 8 * * *"
  event:
    sources: [github]
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`
	inspect := serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/inspect",
		map[string]any{"yaml": raw},
	)
	if inspect.Code != http.StatusOK {
		t.Fatalf("inspect status = %d, body = %s", inspect.Code, inspect.Body.String())
	}
	var inspection workflows.WorkflowTriggersInspection
	decodeWorkflowEventTriggerTestResponse(t, inspect, &inspection)
	if selected := inspection.Triggers[workflows.WorkflowTriggerEvent]; !selected.Editable {
		t.Fatalf("event projection = %#v", selected)
	}
	if broken := inspection.Triggers[workflows.WorkflowTriggerSchedule]; broken.Editable {
		t.Fatalf("schedule projection = %#v, want raw-only", broken)
	}

	render := serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/render",
		map[string]any{
			"yaml":         raw,
			"revision":     inspection.Revision,
			"trigger_type": "event",
			"trigger":      map[string]any{"sources": []string{"gmail"}},
		},
	)
	if render.Code != http.StatusOK {
		t.Fatalf("render status = %d, body = %s", render.Code, render.Body.String())
	}
	var rendered workflowTriggerRenderResponse
	decodeWorkflowEventTriggerTestResponse(t, render, &rendered)
	if !strings.Contains(rendered.YAML, `cron: "0 8 * * *"`) {
		t.Fatalf("render lost malformed schedule:\n%s", rendered.YAML)
	}
	if selected := rendered.Triggers[workflows.WorkflowTriggerEvent]; !selected.Editable {
		t.Fatalf("rendered event projection = %#v", selected)
	}
}

func TestWorkflowTriggersRenderRouteUsesFixedConflictAndValidationErrors(t *testing.T) {
	mux := newWorkflowTriggersTestMux()
	inspection := workflows.InspectWorkflowTriggers(workflowTriggersAPIYAML)
	tests := []struct {
		name       string
		body       any
		wantStatus int
		wantCode   string
		wantPath   string
	}{
		{
			name: "stale",
			body: map[string]any{
				"yaml":         workflowTriggersAPIYAML + "\n",
				"revision":     inspection.Revision,
				"trigger_type": "event",
				"trigger":      nil,
			},
			wantStatus: http.StatusConflict,
			wantCode:   "workflow_trigger_revision_mismatch",
		},
		{
			name: "semantic validation",
			body: map[string]any{
				"yaml":         workflowTriggersAPIYAML,
				"revision":     inspection.Revision,
				"trigger_type": "command",
				"trigger":      map[string]any{},
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_workflow_trigger",
			wantPath:   "on.command.name",
		},
		{
			name: "unsupported",
			body: map[string]any{
				"yaml":         workflowTriggersAPIYAML,
				"revision":     inspection.Revision,
				"trigger_type": "webhook",
				"trigger":      map[string]any{},
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   "unsupported_trigger_type",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := serveWorkflowEventTriggerJSON(
				t,
				mux,
				"/api/workflows/development/triggers/render",
				test.body,
			)
			var response workflowTriggerErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			assertWorkflowTriggerErrorResponse(t, rec, test.wantStatus, test.wantCode)
			if test.name != "unsupported" {
				if response.Inspection == nil || len(response.Inspection.Triggers) != 7 {
					t.Fatalf("current inspection = %#v", response.Inspection)
				}
			}
			if test.wantPath != "" {
				if response.CandidateValidation == nil ||
					response.CandidateValidation.Valid ||
					len(response.CandidateValidation.Errors) == 0 ||
					response.CandidateValidation.Errors[0].Path != test.wantPath {
					t.Fatalf(
						"candidate validation = %#v, want first path %q",
						response.CandidateValidation,
						test.wantPath,
					)
				}
			} else if response.CandidateValidation != nil {
				t.Fatalf(
					"unexpected candidate validation = %#v",
					response.CandidateValidation,
				)
			}
		})
	}

	rawOnly := strings.Replace(
		workflowTriggersAPIYAML,
		"    sources: github",
		"    sources: !private github",
		1,
	)
	rawInspection := workflows.InspectWorkflowTriggers(rawOnly)
	rec := serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/render",
		map[string]any{
			"yaml":         rawOnly,
			"revision":     rawInspection.Revision,
			"trigger_type": "event",
			"trigger":      nil,
		},
	)
	assertWorkflowTriggerErrorResponse(
		t,
		rec,
		http.StatusUnprocessableEntity,
		"workflow_trigger_raw_only",
	)
}

func TestWorkflowTriggerCandidateValidationIsBounded(t *testing.T) {
	mux := newWorkflowTriggersTestMux()
	inspection := workflows.InspectWorkflowTriggers(workflowTriggersAPIYAML)
	schedules := make([]map[string]any, 140)
	for index := range schedules {
		schedules[index] = map[string]any{"cron": ""}
	}
	rec := serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/render",
		map[string]any{
			"yaml":         workflowTriggersAPIYAML,
			"revision":     inspection.Revision,
			"trigger_type": "schedule",
			"trigger":      schedules,
		},
	)
	var response workflowTriggerErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	assertWorkflowTriggerErrorResponse(
		t,
		rec,
		http.StatusUnprocessableEntity,
		"invalid_workflow_trigger",
	)
	if response.CandidateValidation == nil ||
		len(response.CandidateValidation.Errors) != 128 {
		t.Fatalf(
			"candidate errors = %#v, want 128 bounded issues",
			response.CandidateValidation,
		)
	}

	longName := strings.Repeat("argument", 300)
	rec = serveWorkflowEventTriggerJSON(
		t,
		mux,
		"/api/workflows/development/triggers/render",
		map[string]any{
			"yaml":         workflowTriggersAPIYAML,
			"revision":     inspection.Revision,
			"trigger_type": "command",
			"trigger": map[string]any{
				"name": "run",
				"args": map[string]any{
					longName: map[string]any{"type": "unsupported"},
				},
			},
		},
	)
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode long-path response: %v", err)
	}
	if response.CandidateValidation == nil ||
		len(response.CandidateValidation.Errors) != 1 ||
		len(response.CandidateValidation.Errors[0].Path) >
			workflowTriggerCandidateMaximumPathBytes {
		t.Fatalf("long-path candidate validation = %#v", response.CandidateValidation)
	}

	longIssues := make(workflows.ValidationErrors, 128)
	for index := range longIssues {
		longIssues[index] = workflows.ValidationError{
			Path:    strings.Repeat("p", workflowTriggerCandidateMaximumPathBytes*2),
			Message: strings.Repeat("\x01", workflowTriggerCandidateMaximumMessageBytes*2),
		}
	}
	bounded := boundedWorkflowTriggerCandidateValidation(longIssues)
	if len(bounded.Errors) == 0 ||
		len(bounded.Errors) > workflowTriggerCandidateMaximumIssues {
		t.Fatalf("bounded candidate errors = %d", len(bounded.Errors))
	}
	for _, issue := range bounded.Errors {
		if len(issue.Path) > workflowTriggerCandidateMaximumPathBytes ||
			len(issue.Message) > workflowTriggerCandidateMaximumMessageBytes {
			t.Fatalf("unbounded candidate issue = %#v", issue)
		}
	}
	encoded, err := json.Marshal(bounded)
	if err != nil {
		t.Fatalf("marshal bounded candidate validation: %v", err)
	}
	if len(encoded) > workflowTriggerCandidateMaximumEncodedBytes {
		t.Fatalf(
			"candidate validation encoded bytes = %d, want <= %d",
			len(encoded),
			workflowTriggerCandidateMaximumEncodedBytes,
		)
	}
}

func TestWorkflowTriggersRoutesRejectStrictJSONAndUnsafeValues(t *testing.T) {
	mux := newWorkflowTriggersTestMux()
	inspection := workflows.InspectWorkflowTriggers(workflowTriggersAPIYAML)
	quotedYAML, err := json.Marshal(workflowTriggersAPIYAML)
	if err != nil {
		t.Fatal(err)
	}
	validPrefix := `"yaml":` + string(quotedYAML) +
		`,"revision":"` + inspection.Revision + `","trigger_type":"command",`

	tests := []struct {
		name       string
		path       string
		body       string
		content    string
		mutate     func(*http.Request)
		wantStatus int
		wantCode   string
	}{
		{
			name:       "missing yaml",
			path:       "/api/workflows/development/triggers/inspect",
			body:       `{}`,
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_trigger_request",
		},
		{
			name:       "unknown outer field",
			path:       "/api/workflows/development/triggers/inspect",
			body:       `{"yaml":"","hidden":true}`,
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_trigger_request",
		},
		{
			name:       "duplicate outer key",
			path:       "/api/workflows/development/triggers/render",
			body:       `{"yaml":` + string(quotedYAML) + `,"yaml":` + string(quotedYAML) + `}`,
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_trigger_request",
		},
		{
			name:       "duplicate trigger field",
			path:       "/api/workflows/development/triggers/render",
			body:       `{` + validPrefix + `"trigger":{"name":"one","name":"two"}}`,
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_trigger_request",
		},
		{
			name: "duplicate default object key",
			path: "/api/workflows/development/triggers/render",
			body: `{` + validPrefix +
				`"trigger":{"name":"run","args":{"x":{"type":"object","default":{"a":1,"a":2}}}}}`,
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_trigger_request",
		},
		{
			name:       "unknown nested field",
			path:       "/api/workflows/development/triggers/render",
			body:       `{` + validPrefix + `"trigger":{"name":"run","hidden":true}}`,
			content:    "application/json",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_workflow_trigger",
		},
		{
			name:       "wrong trigger shape",
			path:       "/api/workflows/development/triggers/render",
			body:       `{` + validPrefix + `"trigger":[]}`,
			content:    "application/json",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_workflow_trigger",
		},
		{
			name:       "nested null field",
			path:       "/api/workflows/development/triggers/render",
			body:       `{` + validPrefix + `"trigger":{"name":"run","channels":null}}`,
			content:    "application/json",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_workflow_trigger",
		},
		{
			name: "top level null default",
			path: "/api/workflows/development/triggers/render",
			body: `{` + validPrefix +
				`"trigger":{"name":"run","args":{"x":{"type":"string","default":null}}}}`,
			content:    "application/json",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_workflow_trigger",
		},
		{
			name: "integer over browser safe range",
			path: "/api/workflows/development/triggers/render",
			body: `{` + validPrefix +
				`"trigger":{"name":"run","args":{"x":{"type":"number","default":9007199254740992}}}}`,
			content:    "application/json",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_workflow_trigger",
		},
		{
			name: "negative integer over browser safe range",
			path: "/api/workflows/development/triggers/render",
			body: `{` + validPrefix +
				`"trigger":{"name":"run","args":{"x":{"type":"number","default":-9007199254740992}}}}`,
			content:    "application/json",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_workflow_trigger",
		},
		{
			name: "decimal integer over browser safe range",
			path: "/api/workflows/development/triggers/render",
			body: `{` + validPrefix +
				`"trigger":{"name":"run","args":{"x":{"type":"number","default":9007199254740993.0}}}}`,
			content:    "application/json",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_workflow_trigger",
		},
		{
			name: "exponent integer over browser safe range",
			path: "/api/workflows/development/triggers/render",
			body: `{` + validPrefix +
				`"trigger":{"name":"run","args":{"x":{"type":"number","default":9007199254740993e0}}}}`,
			content:    "application/json",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_workflow_trigger",
		},
		{
			name: "non finite number",
			path: "/api/workflows/development/triggers/render",
			body: `{` + validPrefix +
				`"trigger":{"name":"run","args":{"x":{"type":"number","default":1e400}}}}`,
			content:    "application/json",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_workflow_trigger",
		},
		{
			name:       "missing explicit trigger",
			path:       "/api/workflows/development/triggers/render",
			body:       `{` + strings.TrimSuffix(validPrefix, ",") + `}`,
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_trigger_request",
		},
		{
			name: "missing trigger type",
			path: "/api/workflows/development/triggers/render",
			body: `{"yaml":` + string(quotedYAML) +
				`,"revision":"` + inspection.Revision + `","trigger":null}`,
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_trigger_request",
		},
		{
			name: "input named default does not weaken null rejection",
			path: "/api/workflows/development/triggers/render",
			body: `{` + validPrefix +
				`"trigger":{"name":"run","args":{"default":{"type":"string","required":null}}}}`,
			content:    "application/json",
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_workflow_trigger",
		},
		{
			name:       "multiple JSON values",
			path:       "/api/workflows/development/triggers/render",
			body:       `{` + validPrefix + `"trigger":null}{}`,
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_trigger_request",
		},
		{
			name:       "malformed JSON",
			path:       "/api/workflows/development/triggers/render",
			body:       `{`,
			content:    "application/json",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_trigger_request",
		},
		{
			name:       "wrong content type",
			path:       "/api/workflows/development/triggers/inspect",
			body:       `{"yaml":""}`,
			content:    "text/plain",
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   "invalid_trigger_request",
		},
		{
			name:       "missing content type",
			path:       "/api/workflows/development/triggers/inspect",
			body:       `{"yaml":""}`,
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   "invalid_trigger_request",
		},
		{
			name:    "duplicate content type",
			path:    "/api/workflows/development/triggers/inspect",
			body:    `{"yaml":""}`,
			content: "application/json",
			mutate: func(request *http.Request) {
				request.Header["Content-Type"] = []string{
					"application/json",
					"application/json",
				}
			},
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   "invalid_trigger_request",
		},
		{
			name: "oversized",
			path: "/api/workflows/development/triggers/inspect",
			body: `{"yaml":"` +
				strings.Repeat("x", workflowEventTriggerRequestMaxBytes) +
				`"}`,
			content:    "application/json",
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   "trigger_request_too_large",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				test.path,
				strings.NewReader(test.body),
			)
			if test.content != "" {
				request.Header.Set("Content-Type", test.content)
			}
			if test.mutate != nil {
				test.mutate(request)
			}
			mux.ServeHTTP(rec, request)
			assertWorkflowTriggerErrorResponse(
				t,
				rec,
				test.wantStatus,
				test.wantCode,
			)
		})
	}
}

func TestWorkflowTriggerJSONDefaultAllowsNestedNullAndSafeIntegerBoundary(t *testing.T) {
	mux := newWorkflowTriggersTestMux()
	inspection := workflows.InspectWorkflowTriggers(workflowTriggersAPIYAML)
	for _, number := range []string{
		"0.1",
		"9007199254740991",
		"-9007199254740991",
		"9007199254740991.0",
		"9007199254740991e0",
	} {
		t.Run(number, func(t *testing.T) {
			body := fmt.Sprintf(
				`{"yaml":%q,"revision":%q,"trigger_type":"command",`+
					`"trigger":{"name":"run","args":{"x":{"type":"object",`+
					`"default":{"number":%s,"nullable":null}}}}}`,
				workflowTriggersAPIYAML,
				inspection.Revision,
				number,
			)
			rec := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/workflows/development/triggers/render",
				strings.NewReader(body),
			)
			request.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, request)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func newWorkflowTriggersTestMux() *http.ServeMux {
	handler := NewHandler("")
	mux := http.NewServeMux()
	handler.registerWorkflowEditorRoutes(mux)
	return mux
}

func assertWorkflowTriggerErrorResponse(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, wantStatus, rec.Body.String())
	}
	assertWorkflowEventTriggerResponseHeaders(t, rec)
	var response workflowTriggerErrorResponse
	decodeWorkflowEventTriggerTestResponse(t, rec, &response)
	if response.Error != wantCode {
		t.Fatalf("error = %q, want %q", response.Error, wantCode)
	}
}
