package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowJobsEditorAPIYAML = `# API root
name: API jobs
on:
  manual: {}
jobs:
  main: # API job
    runs-on: picoclaw
    steps:
      - id: first
        uses: tool/message
        with:
          count: 7
          ratio: 0.5
          nested: [false, null]
`

func TestWorkflowJobsInspectRouteReturnsBoundedOrderedProjection(t *testing.T) {
	recorder := serveWorkflowJobsJSON(
		t,
		"/api/workflows/development/jobs/inspect",
		map[string]any{"yaml": workflowJobsEditorAPIYAML},
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	assertWorkflowEventTriggerResponseHeaders(t, recorder)
	var inspection workflows.WorkflowJobsInspection
	decodeWorkflowEventTriggerTestResponse(t, recorder, &inspection)
	if !inspection.Editable ||
		!inspection.Complete ||
		len(inspection.Jobs) != 1 ||
		inspection.Jobs[0].ID != "main" ||
		len(inspection.Jobs[0].Steps) != 1 ||
		inspection.Validation == nil ||
		!inspection.Validation.Valid {
		t.Fatalf("inspection = %#v", inspection)
	}
}

func TestWorkflowJobsRenderRouteNormalizesNumbersForExactNoOp(t *testing.T) {
	inspection := workflows.InspectWorkflowJobs(workflowJobsEditorAPIYAML)
	recorder := serveWorkflowJobsJSON(
		t,
		"/api/workflows/development/jobs/render",
		map[string]any{
			"yaml":     workflowJobsEditorAPIYAML,
			"revision": inspection.Revision,
			"operation": map[string]any{
				"type":       "step.patch",
				"job_id":     "main",
				"step_index": 0,
				"fields": map[string]any{
					"with": map[string]any{
						"mode": "set",
						"value": map[string]any{
							"count": 7,
							"ratio": 0.5,
							"nested": []any{
								false,
								nil,
							},
						},
					},
				},
			},
		},
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response workflowJobsRenderResponse
	decodeWorkflowEventTriggerTestResponse(t, recorder, &response)
	if response.YAML != workflowJobsEditorAPIYAML ||
		response.Revision != inspection.Revision {
		t.Fatalf("numeric no-op changed bytes:\n%s", response.YAML)
	}
}

func TestWorkflowJobsRenderRouteSupportsRenameAndInvalidIntermediate(t *testing.T) {
	inspection := workflows.InspectWorkflowJobs(workflowJobsEditorAPIYAML)
	recorder := serveWorkflowJobsJSON(
		t,
		"/api/workflows/development/jobs/render",
		map[string]any{
			"yaml":     workflowJobsEditorAPIYAML,
			"revision": inspection.Revision,
			"operation": map[string]any{
				"type":   "job.patch",
				"job_id": "main",
				"new_job_id": map[string]any{
					"mode":  "set",
					"value": "renamed",
				},
				"fields": map[string]any{
					"runs_on": map[string]any{"mode": "remove"},
				},
			},
		},
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response workflowJobsRenderResponse
	decodeWorkflowEventTriggerTestResponse(t, recorder, &response)
	if !strings.Contains(response.YAML, "renamed:") ||
		response.Validation == nil ||
		response.Validation.Valid ||
		len(response.Validation.Errors) == 0 {
		t.Fatalf("response = %#v\n%s", response, response.YAML)
	}
}

func TestWorkflowJobsRoutesRejectMalformedExactProtocolShapes(t *testing.T) {
	inspection := workflows.InspectWorkflowJobs(workflowJobsEditorAPIYAML)
	validOperation := `{"type":"step.patch","job_id":"main","step_index":0,` +
		`"fields":{"name":{"mode":"set","value":"changed"}}}`
	validRender := func(operation string) string {
		return `{"yaml":` + mustWorkflowJobsJSONString(t, workflowJobsEditorAPIYAML) +
			`,"revision":` + mustWorkflowJobsJSONString(t, inspection.Revision) +
			`,"operation":` + operation + `}`
	}
	tests := []struct {
		name   string
		path   string
		body   string
		status int
		code   string
	}{
		{
			name:   "top case variant",
			path:   "/api/workflows/development/jobs/inspect",
			body:   `{"YAML":"jobs: {}"}`,
			status: http.StatusBadRequest,
			code:   "invalid_workflow_jobs_request",
		},
		{
			name:   "top duplicate",
			path:   "/api/workflows/development/jobs/inspect",
			body:   `{"yaml":"jobs: {}","yaml":"jobs: {}"}`,
			status: http.StatusBadRequest,
			code:   "invalid_workflow_jobs_request",
		},
		{
			name: "operation case variant",
			path: "/api/workflows/development/jobs/render",
			body: validRender(
				`{"Type":"step.patch","job_id":"main","step_index":0,"fields":{}}`,
			),
			status: http.StatusBadRequest,
			code:   "invalid_workflow_jobs_request",
		},
		{
			name: "operation irrelevant member",
			path: "/api/workflows/development/jobs/render",
			body: validRender(
				`{"type":"job.delete","job_id":"main","fields":{}}`,
			),
			status: http.StatusBadRequest,
			code:   "invalid_workflow_jobs_request",
		},
		{
			name: "field case variant",
			path: "/api/workflows/development/jobs/render",
			body: validRender(
				`{"type":"step.patch","job_id":"main","step_index":0,` +
					`"fields":{"Name":{"mode":"set","value":"changed"}}}`,
			),
			status: http.StatusUnprocessableEntity,
			code:   "invalid_workflow_jobs_operation",
		},
		{
			name: "envelope case variant",
			path: "/api/workflows/development/jobs/render",
			body: validRender(
				`{"type":"step.patch","job_id":"main","step_index":0,` +
					`"fields":{"name":{"Mode":"set","value":"changed"}}}`,
			),
			status: http.StatusUnprocessableEntity,
			code:   "invalid_workflow_jobs_operation",
		},
		{
			name: "remove includes null value",
			path: "/api/workflows/development/jobs/render",
			body: validRender(
				`{"type":"step.patch","job_id":"main","step_index":0,` +
					`"fields":{"name":{"mode":"remove","value":null}}}`,
			),
			status: http.StatusUnprocessableEntity,
			code:   "invalid_workflow_jobs_operation",
		},
		{
			name: "set missing value",
			path: "/api/workflows/development/jobs/render",
			body: validRender(
				`{"type":"step.patch","job_id":"main","step_index":0,` +
					`"fields":{"name":{"mode":"set"}}}`,
			),
			status: http.StatusUnprocessableEntity,
			code:   "invalid_workflow_jobs_operation",
		},
		{
			name: "nested duplicate",
			path: "/api/workflows/development/jobs/render",
			body: validRender(
				`{"type":"step.patch","job_id":"main","step_index":0,` +
					`"fields":{"with":{"mode":"set","value":{"x":1,"x":2}}}}`,
			),
			status: http.StatusBadRequest,
			code:   "invalid_workflow_jobs_request",
		},
		{
			name:   "trailing JSON",
			path:   "/api/workflows/development/jobs/render",
			body:   validRender(validOperation) + `{}`,
			status: http.StatusBadRequest,
			code:   "invalid_workflow_jobs_request",
		},
		{
			name: "null operation",
			path: "/api/workflows/development/jobs/render",
			body: validRender(
				`null`,
			),
			status: http.StatusBadRequest,
			code:   "invalid_workflow_jobs_request",
		},
		{
			name: "unsupported operation",
			path: "/api/workflows/development/jobs/render",
			body: validRender(
				`{"type":"job.move","job_id":"main"}`,
			),
			status: http.StatusBadRequest,
			code:   "unsupported_workflow_jobs_operation",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := serveWorkflowJobsRaw(t, test.path, []byte(test.body))
			assertWorkflowJobsError(t, recorder, test.status, test.code)
		})
	}
}

func TestWorkflowJobsRoutesAllowCaseDistinctDynamicKeys(t *testing.T) {
	inspection := workflows.InspectWorkflowJobs(workflowJobsEditorAPIYAML)
	recorder := serveWorkflowJobsJSON(
		t,
		"/api/workflows/development/jobs/render",
		map[string]any{
			"yaml":     workflowJobsEditorAPIYAML,
			"revision": inspection.Revision,
			"operation": map[string]any{
				"type":       "step.patch",
				"job_id":     "main",
				"step_index": 0,
				"fields": map[string]any{
					"with": map[string]any{
						"mode": "set",
						"value": map[string]any{
							"Foo": "one",
							"foo": "two",
						},
					},
				},
			},
		},
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestWorkflowJobsRoutesRejectInvalidUTF8SizeMediaAndDeepJSON(t *testing.T) {
	invalidUTF8 := []byte(`{"yaml":"jobs: `)
	invalidUTF8 = append(invalidUTF8, 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	assertWorkflowJobsError(
		t,
		serveWorkflowJobsRaw(
			t,
			"/api/workflows/development/jobs/inspect",
			invalidUTF8,
		),
		http.StatusBadRequest,
		"invalid_workflow_jobs_request",
	)

	oversized := `{"yaml":"` +
		strings.Repeat("x", workflowJobsEditorRequestMaxBytes) +
		`"}`
	assertWorkflowJobsError(
		t,
		serveWorkflowJobsRaw(
			t,
			"/api/workflows/development/jobs/inspect",
			[]byte(oversized),
		),
		http.StatusRequestEntityTooLarge,
		"workflow_jobs_request_too_large",
	)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/development/jobs/inspect",
		strings.NewReader(`{"yaml":"jobs: {}"}`),
	)
	request.Header.Set("Content-Type", "text/plain")
	newWorkflowTriggersTestMux().ServeHTTP(recorder, request)
	assertWorkflowJobsError(
		t,
		recorder,
		http.StatusUnsupportedMediaType,
		"invalid_workflow_jobs_request",
	)

	deep := strings.Repeat("[", workflowJobsEditorJSONMaxDepth+2) +
		"0" +
		strings.Repeat("]", workflowJobsEditorJSONMaxDepth+2)
	assertWorkflowJobsError(
		t,
		serveWorkflowJobsRaw(
			t,
			"/api/workflows/development/jobs/inspect",
			[]byte(`{"yaml":`+deep+`}`),
		),
		http.StatusBadRequest,
		"invalid_workflow_jobs_request",
	)
}

func TestWorkflowJobsRoutesRejectUnpairedJSONSurrogates(t *testing.T) {
	inspection := workflows.InspectWorkflowJobs(workflowJobsEditorAPIYAML)
	render := func(value string) string {
		return `{"yaml":` + mustWorkflowJobsJSONString(t, workflowJobsEditorAPIYAML) +
			`,"revision":` + mustWorkflowJobsJSONString(t, inspection.Revision) +
			`,"operation":{"type":"step.patch","job_id":"main",` +
			`"step_index":0,"fields":{"name":{"mode":"set","value":` +
			value + `}}}}`
	}
	for name, value := range map[string]string{
		"high value": `"before\uD800after"`,
		"low value":  `"before\uDC00after"`,
		"high key":   `{"\uD800":"value"}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := render(value)
			if name == "high key" {
				body = `{"yaml":` +
					mustWorkflowJobsJSONString(t, workflowJobsEditorAPIYAML) +
					`,"revision":` +
					mustWorkflowJobsJSONString(t, inspection.Revision) +
					`,"operation":{"type":"step.patch","job_id":"main",` +
					`"step_index":0,"fields":{"with":{"mode":"set","value":` +
					value + `}}}}`
			}
			assertWorkflowJobsError(
				t,
				serveWorkflowJobsRaw(
					t,
					"/api/workflows/development/jobs/render",
					[]byte(body),
				),
				http.StatusBadRequest,
				"invalid_workflow_jobs_request",
			)
		})
	}
	for name, value := range map[string]string{
		"valid pair":  `"emoji \uD83D\uDE00"`,
		"replacement": `"literal \uFFFD"`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := serveWorkflowJobsRaw(
				t,
				"/api/workflows/development/jobs/render",
				[]byte(render(value)),
			)
			if recorder.Code != http.StatusOK {
				t.Fatalf(
					"status = %d, body = %s",
					recorder.Code,
					recorder.Body.String(),
				)
			}
		})
	}
}

func TestWorkflowJobsRenderErrorsUseFixedCodesAndCurrentInspection(t *testing.T) {
	inspection := workflows.InspectWorkflowJobs(workflowJobsEditorAPIYAML)
	stale := serveWorkflowJobsJSON(
		t,
		"/api/workflows/development/jobs/render",
		map[string]any{
			"yaml":     workflowJobsEditorAPIYAML,
			"revision": "sha256:stale-secret-detail",
			"operation": map[string]any{
				"type":   "job.delete",
				"job_id": "main",
			},
		},
	)
	assertWorkflowJobsError(
		t,
		stale,
		http.StatusConflict,
		"workflow_jobs_revision_mismatch",
	)
	var staleResponse workflowJobsErrorResponse
	decodeWorkflowEventTriggerTestResponse(t, stale, &staleResponse)
	if staleResponse.Inspection == nil ||
		staleResponse.Inspection.Revision != inspection.Revision ||
		strings.Contains(stale.Body.String(), "secret-detail") {
		t.Fatalf("stale response = %s", stale.Body.String())
	}

	rawOnly := "jobs:\n  main:\n    context: null\n"
	rawInspection := workflows.InspectWorkflowJobs(rawOnly)
	blocked := serveWorkflowJobsJSON(
		t,
		"/api/workflows/development/jobs/render",
		map[string]any{
			"yaml":     rawOnly,
			"revision": rawInspection.Revision,
			"operation": map[string]any{
				"type":   "job.patch",
				"job_id": "main",
				"fields": map[string]any{
					"name": map[string]any{"mode": "set", "value": "blocked"},
				},
			},
		},
	)
	assertWorkflowJobsError(
		t,
		blocked,
		http.StatusUnprocessableEntity,
		"workflow_jobs_raw_only",
	)
}

func TestWorkflowJobsRenderChecksStaleRevisionBeforeOperationDecoding(t *testing.T) {
	current := workflows.InspectWorkflowJobs(workflowJobsEditorAPIYAML)
	envelope := func(operation string) string {
		return `{"yaml":` + mustWorkflowJobsJSONString(t, workflowJobsEditorAPIYAML) +
			`,"revision":"sha256:stale","operation":` + operation + `}`
	}
	for name, operation := range map[string]string{
		"malformed":   `{}`,
		"unsupported": `{"type":"job.move","job_id":"main"}`,
		"large dynamic value": `{"type":"step.patch","job_id":"main",` +
			`"step_index":0,"fields":{"with":{"mode":"set","value":{"data":` +
			mustWorkflowJobsJSONString(t, strings.Repeat("x", 512<<10)) +
			`}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := serveWorkflowJobsRaw(
				t,
				"/api/workflows/development/jobs/render",
				[]byte(envelope(operation)),
			)
			assertWorkflowJobsError(
				t,
				recorder,
				http.StatusConflict,
				"workflow_jobs_revision_mismatch",
			)
			var response workflowJobsErrorResponse
			decodeWorkflowEventTriggerTestResponse(t, recorder, &response)
			if response.Inspection == nil ||
				response.Inspection.Revision != current.Revision {
				t.Fatalf("stale inspection = %#v", response.Inspection)
			}
		})
	}
}

func TestWorkflowJobsRenderRejectsUnsafeJobIDSelectors(t *testing.T) {
	inspection := workflows.InspectWorkflowJobs(workflowJobsEditorAPIYAML)
	for name, jobID := range map[string]string{
		"too long":  strings.Repeat("j", workflows.MaxWorkflowJobsEditorIDBytes+1),
		"newline":   "main\nother",
		"control":   "main\u202eother",
		"untrimmed": " main ",
	} {
		t.Run(name, func(t *testing.T) {
			recorder := serveWorkflowJobsJSON(
				t,
				"/api/workflows/development/jobs/render",
				map[string]any{
					"yaml":     workflowJobsEditorAPIYAML,
					"revision": inspection.Revision,
					"operation": map[string]any{
						"type":   "job.delete",
						"job_id": jobID,
					},
				},
			)
			assertWorkflowJobsError(
				t,
				recorder,
				http.StatusBadRequest,
				"invalid_workflow_jobs_request",
			)
		})
	}
}

func TestWorkflowJobsRenderRouteRejectsMovesAcrossRawOnlySteps(t *testing.T) {
	raw := `jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
      - uses: [raw-only]
      - uses: tool/message
      - uses: tool/message
`
	inspection := workflows.InspectWorkflowJobs(raw)
	for _, indexes := range [][2]int{{0, 2}, {2, 0}} {
		recorder := serveWorkflowJobsJSON(
			t,
			"/api/workflows/development/jobs/render",
			map[string]any{
				"yaml":     raw,
				"revision": inspection.Revision,
				"operation": map[string]any{
					"type":       "step.move",
					"job_id":     "main",
					"step_index": indexes[0],
					"to_index":   indexes[1],
				},
			},
		)
		assertWorkflowJobsError(
			t,
			recorder,
			http.StatusUnprocessableEntity,
			"workflow_jobs_raw_only",
		)
	}
	recorder := serveWorkflowJobsJSON(
		t,
		"/api/workflows/development/jobs/render",
		map[string]any{
			"yaml":     raw,
			"revision": inspection.Revision,
			"operation": map[string]any{
				"type":       "step.move",
				"job_id":     "main",
				"step_index": 2,
				"to_index":   3,
			},
		},
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("safe move status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestWorkflowJobsWriterPreflightsResponseCeilingAndMarshalFailure(t *testing.T) {
	original := workflowJobsEditorResponseByteLimit
	t.Cleanup(func() { workflowJobsEditorResponseByteLimit = original })
	workflowJobsEditorResponseByteLimit = 16

	recorder := httptest.NewRecorder()
	writeWorkflowJobsJSON(recorder, http.StatusOK, map[string]string{
		"value": "larger than the injected ceiling",
	})
	if recorder.Code != http.StatusServiceUnavailable ||
		recorder.Body.String() !=
			"{\"error\":\"workflow_jobs_response_too_large\"}\n" {
		t.Fatalf("oversized writer = %d %q", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	writeWorkflowJobsJSON(recorder, http.StatusOK, make(chan int))
	if recorder.Code != http.StatusServiceUnavailable ||
		recorder.Body.String() !=
			"{\"error\":\"workflow_jobs_response_unavailable\"}\n" {
		t.Fatalf("marshal writer = %d %q", recorder.Code, recorder.Body.String())
	}
}

func serveWorkflowJobsJSON(
	t *testing.T,
	path string,
	value any,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return serveWorkflowJobsRaw(t, path, body)
}

func serveWorkflowJobsRaw(
	t *testing.T,
	path string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	newWorkflowTriggersTestMux().ServeHTTP(recorder, request)
	return recorder
}

func assertWorkflowJobsError(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	status int,
	code string,
) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf(
			"status = %d, want %d, body = %s",
			recorder.Code,
			status,
			recorder.Body.String(),
		)
	}
	assertWorkflowEventTriggerResponseHeaders(t, recorder)
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error = %v, body = %s", err, recorder.Body.String())
	}
	if response.Error != code {
		t.Fatalf("error = %q, want %q", response.Error, code)
	}
}

func mustWorkflowJobsJSONString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(encoded)
}
