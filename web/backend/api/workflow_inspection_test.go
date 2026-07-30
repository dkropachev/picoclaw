package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

const workflowInspectionAPITestYAML = `name: Inspect safely
on:
  manual: {}
  event:
    sources: github
jobs:
  inspect:
    runs-on: picoclaw
    if: ${{ secrets.PRIVATE_CONDITION }}
    steps:
      - id: notify
        uses: tool/message
        with:
          message: TOP_SECRET_WORKFLOW_VALUE
`

func TestWorkflowDefinitionInspectionRoutesReturnSanitizedProjections(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowInspectionAPIConfig(t, workspace)
	writeWorkflowInspectionAPIDefinition(
		t,
		workspace,
		"inspect.yml",
		workflowInspectionAPITestYAML,
	)
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/definitions/inspect",
		strings.NewReader(`{"ref":"workflows/inspect.yml"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	assertWorkflowInspectionHeaders(t, recorder)
	assertWorkflowInspectionResponseSanitized(t, recorder.Body.String(), workspace)

	var response struct {
		Source struct {
			Kind string `json:"kind"`
			Ref  string `json:"ref"`
		} `json:"source"`
		Revision string `json:"revision"`
		Complete bool   `json:"complete"`
		Triggers map[string]struct {
			Present   bool `json:"present"`
			Projected bool `json:"projected"`
		} `json:"triggers"`
		Jobs []struct {
			ID    string `json:"id"`
			Steps []struct {
				Kind   string `json:"kind"`
				Target string `json:"target"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON error = %v", err)
	}
	if response.Source.Kind != "published" ||
		response.Source.Ref != "workflows/inspect.yml" ||
		!strings.HasPrefix(response.Revision, "sha256:") ||
		!response.Complete {
		t.Fatalf("unexpected response metadata: %#v", response)
	}
	for _, family := range []string{
		"manual",
		"schedule",
		"channel_message",
		"command",
		"runtime_event",
		"event",
		"workflow_call",
	} {
		if _, ok := response.Triggers[family]; !ok {
			t.Errorf("trigger family %q absent", family)
		}
	}
	if len(response.Jobs) != 1 ||
		response.Jobs[0].ID != "inspect" ||
		len(response.Jobs[0].Steps) != 1 ||
		response.Jobs[0].Steps[0].Kind != "tool" ||
		response.Jobs[0].Steps[0].Target != "tool/message" {
		t.Fatalf("jobs = %#v", response.Jobs)
	}

	templateRecorder := httptest.NewRecorder()
	mux.ServeHTTP(
		templateRecorder,
		httptest.NewRequest(
			http.MethodGet,
			"/api/workflows/templates/code-review/inspect",
			nil,
		),
	)
	if templateRecorder.Code != http.StatusOK {
		t.Fatalf(
			"template status = %d, body=%s",
			templateRecorder.Code,
			templateRecorder.Body.String(),
		)
	}
	assertWorkflowInspectionHeaders(t, templateRecorder)
	assertWorkflowInspectionResponseSanitized(t, templateRecorder.Body.String(), workspace)
	var templateResponse struct {
		Source struct {
			Kind         string `json:"kind"`
			TemplateName string `json:"template_name"`
		} `json:"source"`
	}
	if err := json.Unmarshal(templateRecorder.Body.Bytes(), &templateResponse); err != nil {
		t.Fatalf("template response JSON error = %v", err)
	}
	if templateResponse.Source.Kind != "template" ||
		templateResponse.Source.TemplateName != "code-review" {
		t.Fatalf("template source = %#v", templateResponse.Source)
	}
}

func TestWorkflowDefinitionInspectionReturnsMalformedDefinitionAsSafeInspection(
	t *testing.T,
) {
	workspace := t.TempDir()
	configPath := writeWorkflowInspectionAPIConfig(t, workspace)
	writeWorkflowInspectionAPIDefinition(
		t,
		workspace,
		"malformed.yml",
		"name: [TOP_SECRET_MALFORMED_VALUE\n",
	)
	h := NewHandler(configPath)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/definitions/inspect",
		strings.NewReader(`{"ref":"workflows/malformed.yml"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	h.handleInspectWorkflowDefinition(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	assertWorkflowInspectionHeaders(t, recorder)
	assertWorkflowInspectionResponseSanitized(t, recorder.Body.String(), workspace)
	var response struct {
		Complete   bool `json:"complete"`
		Validation struct {
			Valid      bool `json:"valid"`
			IssueCount int  `json:"issue_count"`
		} `json:"validation"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON error = %v", err)
	}
	if !response.Complete || response.Validation.Valid ||
		response.Validation.IssueCount == 0 {
		t.Fatalf("malformed response = %#v", response)
	}
}

func TestWorkflowDefinitionInspectionMapsFixedSafeErrors(t *testing.T) {
	workspace := t.TempDir()
	configPath := writeWorkflowInspectionAPIConfig(t, workspace)
	h := NewHandler(configPath)

	tests := []struct {
		name       string
		ref        string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "invalid ref",
			ref:        "../outside.yml",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_definition_inspection_request",
		},
		{
			name:       "noncanonical ref",
			ref:        "./workflows/missing.yml",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_definition_inspection_request",
		},
		{
			name:       "control character ref",
			ref:        "workflows/unsafe\u0000.yml",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_definition_inspection_request",
		},
		{
			name:       "missing workflow",
			ref:        "workflows/missing.yml",
			wantStatus: http.StatusNotFound,
			wantCode:   "workflow_not_found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/workflows/definitions/inspect",
				strings.NewReader(`{"ref":`+mustWorkflowInspectionJSON(t, test.ref)+`}`),
			)
			request.Header.Set("Content-Type", "application/json")
			h.handleInspectWorkflowDefinition(recorder, request)
			assertWorkflowInspectionError(
				t,
				recorder,
				test.wantStatus,
				test.wantCode,
			)
			assertWorkflowInspectionResponseSanitized(t, recorder.Body.String(), workspace)
		})
	}

	oversized := strings.Repeat("x", (1<<20)+1)
	writeWorkflowInspectionAPIDefinition(t, workspace, "oversized.yml", oversized)
	oversizedRecorder := httptest.NewRecorder()
	oversizedRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/definitions/inspect",
		strings.NewReader(`{"ref":"workflows/oversized.yml"}`),
	)
	oversizedRequest.Header.Set("Content-Type", "application/json")
	h.handleInspectWorkflowDefinition(oversizedRecorder, oversizedRequest)
	assertWorkflowInspectionError(
		t,
		oversizedRecorder,
		http.StatusRequestEntityTooLarge,
		"workflow_definition_too_large",
	)

	unavailableConfigPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(unavailableConfigPath, []byte("{"), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	unavailable := NewHandler(unavailableConfigPath)
	unavailableRecorder := httptest.NewRecorder()
	unavailableRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/definitions/inspect",
		strings.NewReader(`{"ref":"workflows/missing.yml"}`),
	)
	unavailableRequest.Header.Set("Content-Type", "application/json")
	unavailable.handleInspectWorkflowDefinition(unavailableRecorder, unavailableRequest)
	assertWorkflowInspectionError(
		t,
		unavailableRecorder,
		http.StatusServiceUnavailable,
		"workflow_inspection_unavailable",
	)
	assertWorkflowInspectionResponseSanitized(
		t,
		unavailableRecorder.Body.String(),
		unavailable.configPath,
	)

	t.Run("definitions root escape is unavailable", func(t *testing.T) {
		escapeWorkspace := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(
			outside,
			filepath.Join(escapeWorkspace, "automation"),
		); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		escapeHandler := NewHandler(
			writeWorkflowInspectionAPIConfig(t, escapeWorkspace),
		)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/definitions/inspect",
			strings.NewReader(`{"ref":"workflows/missing.yml"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		escapeHandler.handleInspectWorkflowDefinition(recorder, request)
		assertWorkflowInspectionError(
			t,
			recorder,
			http.StatusServiceUnavailable,
			"workflow_inspection_unavailable",
		)
		assertWorkflowInspectionResponseSanitized(
			t,
			recorder.Body.String(),
			outside,
		)
	})

	templateRecorder := httptest.NewRecorder()
	templateRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/workflows/templates/not-real/inspect",
		nil,
	)
	templateRequest.SetPathValue("name", "not-real")
	h.handleInspectWorkflowTemplate(templateRecorder, templateRequest)
	assertWorkflowInspectionError(
		t,
		templateRecorder,
		http.StatusNotFound,
		"template_not_found",
	)

	noncanonicalTemplateRecorder := httptest.NewRecorder()
	noncanonicalTemplateRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/workflows/templates/CODE-REVIEW/inspect",
		nil,
	)
	noncanonicalTemplateRequest.SetPathValue("name", "CODE-REVIEW")
	h.handleInspectWorkflowTemplate(
		noncanonicalTemplateRecorder,
		noncanonicalTemplateRequest,
	)
	assertWorkflowInspectionError(
		t,
		noncanonicalTemplateRecorder,
		http.StatusBadRequest,
		"invalid_definition_inspection_request",
	)
}

func TestWriteWorkflowInspectionJSONFailsClosedAboveResponseLimit(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeWorkflowInspectionJSON(
		recorder,
		http.StatusOK,
		map[string]string{
			"value": strings.Repeat(
				"CANARY_OVERSIZED_INSPECTION",
				workflowDefinitionInspectionResponseMaxBytes/
					len("CANARY_OVERSIZED_INSPECTION")+1,
			),
		},
	)

	assertWorkflowInspectionError(
		t,
		recorder,
		http.StatusServiceUnavailable,
		"workflow_inspection_unavailable",
	)
	if strings.Contains(recorder.Body.String(), "CANARY_OVERSIZED_INSPECTION") {
		t.Fatalf("oversized response content leaked: %s", recorder.Body.String())
	}
}

func TestWorkflowDefinitionInspectionRejectsUnsafeRequests(t *testing.T) {
	workspace := t.TempDir()
	h := NewHandler(writeWorkflowInspectionAPIConfig(t, workspace))
	validRef := "workflows/inspect.yml"

	tests := []struct {
		name        string
		body        string
		contentType string
		mutate      func(*http.Request)
		wantStatus  int
		wantCode    string
	}{
		{
			name:       "missing content type",
			body:       `{"ref":"` + validRef + `"}`,
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   "invalid_definition_inspection_content_type",
		},
		{
			name:        "wrong content type",
			body:        `{"ref":"` + validRef + `"}`,
			contentType: "text/plain",
			wantStatus:  http.StatusUnsupportedMediaType,
			wantCode:    "invalid_definition_inspection_content_type",
		},
		{
			name:        "duplicate content type",
			body:        `{"ref":"` + validRef + `"}`,
			contentType: "application/json",
			mutate: func(request *http.Request) {
				request.Header["Content-Type"] = []string{
					"application/json",
					"application/json",
				}
			},
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   "invalid_definition_inspection_content_type",
		},
		{
			name:        "duplicate case insensitive content type",
			body:        `{"ref":"` + validRef + `"}`,
			contentType: "application/json",
			mutate: func(request *http.Request) {
				request.Header["content-type"] = []string{"application/json"}
			},
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   "invalid_definition_inspection_content_type",
		},
		{
			name:        "unsupported content type parameter",
			body:        `{"ref":"` + validRef + `"}`,
			contentType: "application/json; boundary=unsafe",
			wantStatus:  http.StatusUnsupportedMediaType,
			wantCode:    "invalid_definition_inspection_content_type",
		},
		{
			name:        "empty body",
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name:        "null body",
			body:        `null`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name:        "missing ref",
			body:        `{}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name:        "null ref",
			body:        `{"ref":null}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name:        "empty ref",
			body:        `{"ref":" "}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name:        "unknown field",
			body:        `{"ref":"` + validRef + `","path":"private"}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name:        "wrong case field",
			body:        `{"Ref":"` + validRef + `"}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name:        "duplicate ref",
			body:        `{"ref":"` + validRef + `","\u0072ef":"workflows/other.yml"}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name:        "recursive duplicate",
			body:        `{"ref":"` + validRef + `","extra":{"x":1,"x":2}}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name:        "recursive null",
			body:        `{"ref":"` + validRef + `","extra":{"x":null}}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name:        "trailing JSON",
			body:        `{"ref":"` + validRef + `"}{}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name:        "invalid UTF-8",
			body:        "{\"ref\":\"workflows/\xff.yml\"}",
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    "invalid_definition_inspection_request",
		},
		{
			name: "oversized body",
			body: `{"ref":"` +
				strings.Repeat("x", workflowDefinitionInspectionRequestMaxBytes) +
				`"}`,
			contentType: "application/json",
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    "definition_inspection_request_too_large",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/workflows/definitions/inspect",
				strings.NewReader(test.body),
			)
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.mutate != nil {
				test.mutate(request)
			}
			h.handleInspectWorkflowDefinition(recorder, request)
			assertWorkflowInspectionError(
				t,
				recorder,
				test.wantStatus,
				test.wantCode,
			)
		})
	}

	parameterRecorder := httptest.NewRecorder()
	parameterRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/definitions/inspect",
		strings.NewReader(`{"ref":"workflows/missing.yml"}`),
	)
	parameterRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
	h.handleInspectWorkflowDefinition(parameterRecorder, parameterRequest)
	assertWorkflowInspectionError(
		t,
		parameterRecorder,
		http.StatusNotFound,
		"workflow_not_found",
	)
}

func writeWorkflowInspectionAPIConfig(t *testing.T, workspace string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Workflows.Enabled = true
	cfg.Workflows.DefinitionsDir = "automation"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	return configPath
}

func writeWorkflowInspectionAPIDefinition(
	t *testing.T,
	workspace string,
	name string,
	contents string,
) {
	t.Helper()
	root := filepath.Join(workspace, "automation")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func mustWorkflowInspectionJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return string(encoded)
}

func assertWorkflowInspectionError(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf(
			"status = %d, want %d, body=%s",
			recorder.Code,
			wantStatus,
			recorder.Body.String(),
		)
	}
	assertWorkflowInspectionHeaders(t, recorder)
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON error = %v", err)
	}
	if response.Error != wantCode {
		t.Fatalf("error = %q, want %q", response.Error, wantCode)
	}
}

func assertWorkflowInspectionHeaders(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) {
	t.Helper()
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func assertWorkflowInspectionResponseSanitized(
	t *testing.T,
	body string,
	sensitiveValues ...string,
) {
	t.Helper()
	for _, forbidden := range append(
		sensitiveValues,
		`"path"`,
		`"yaml"`,
		"TOP_SECRET_WORKFLOW_VALUE",
		"TOP_SECRET_MALFORMED_VALUE",
		"PRIVATE_CONDITION",
	) {
		if forbidden != "" && strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}
