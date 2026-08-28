package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	eventoperator "github.com/sipeed/picoclaw/pkg/eventing/operator"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const eventDraftPrivateAPIDiagnostic = "EVENT_DRAFT_PRIVATE_DIAGNOSTIC"

func TestRepositoryReviewWorkflowEventEndpointScrubsCampaignAuthority(t *testing.T) {
	workspace := t.TempDir()
	handler := NewHandler(writeWorkflowEventTestConfig(t, workspace))
	store := workflows.NewFileRunStore(workspace)
	now := time.Now().UTC()
	run := &workflows.Run{
		ID: workflows.NewRunID(), WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Status: workflows.RunStatusSucceeded, Inputs: map[string]any{
			"repository": "owner/repo", "campaign_id": "rrc_event_canary",
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(t.Context(), workflows.RunEvent{
		RunID: run.ID, Kind: "workflow.step.end", Payload: map[string]any{
			"outputs": map[string]any{"run": map[string]any{
				"campaign_id": "rrc_event_canary", "remaining_files": 0,
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/workflows/runs/"+run.ID+"/events", nil)
	request.SetPathValue("run_id", run.ID)
	handler.handleGetWorkflowRunEvents(recorder, request)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "rrc_event_canary") ||
		strings.Contains(recorder.Body.String(), "campaign_id") ||
		!strings.Contains(recorder.Body.String(), "remaining_files") {
		t.Fatalf("event projection status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	raw, err := store.Events(t.Context(), run.ID)
	if err != nil || len(raw) != 1 ||
		raw[0].Payload["outputs"].(map[string]any)["run"].(map[string]any)["campaign_id"] != "rrc_event_canary" {
		t.Fatalf("stored event=%#v err=%v", raw, err)
	}
}

func TestRepositoryReviewWorkflowRunProjectionDeeplyScrubsCampaignAuthority(t *testing.T) {
	const canary = "rrc_api_projection_canary"
	snapshot := &workflowRunPrivacySnapshot{}
	ordinary := &workflows.Run{
		WorkflowRef: "workflows/ordinary.yml",
		Inputs:      map[string]any{"campaign_id": canary},
	}
	if projected := snapshot.projectRunForBrowser(
		t.Context(), nil, ordinary,
	); projected.Inputs["campaign_id"] != canary {
		t.Fatalf("ordinary workflow projection=%#v", projected)
	}

	run := &workflows.Run{
		WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Inputs: map[string]any{
			"campaign_id": canary,
			"nested": map[string]any{
				"campaignId": canary,
				"safe":       "input",
			},
		},
		Outputs: map[string]any{
			"items": []any{
				map[string]any{"campaign_id": canary, "safe": "slice"},
				"scalar",
			},
			"maps": []map[string]any{{"campaignId": canary, "safe": "typed-slice"}},
		},
		Jobs: map[string]workflows.JobExecution{
			"review": {Outputs: map[string]any{"campaign_id": canary, "safe": "job"}},
		},
		Steps: map[string]workflows.StepExecution{
			"record": {Outputs: map[string]any{"campaignId": canary, "safe": "step"}},
		},
	}
	projected := snapshot.projectRunForBrowser(t.Context(), nil, run)
	if projected == run || projected.Inputs["campaign_id"] != nil ||
		projected.Inputs["nested"].(map[string]any)["campaignId"] != nil ||
		projected.Inputs["nested"].(map[string]any)["safe"] != "input" ||
		projected.Outputs["items"].([]any)[0].(map[string]any)["campaign_id"] != nil ||
		projected.Outputs["items"].([]any)[1] != "scalar" ||
		projected.Outputs["maps"].([]map[string]any)[0]["campaignId"] != nil ||
		projected.Jobs["review"].Outputs["campaign_id"] != nil ||
		projected.Steps["record"].Outputs["campaignId"] != nil {
		t.Fatalf("campaign workflow projection=%#v", projected)
	}
	if run.Inputs["campaign_id"] != canary ||
		run.Inputs["nested"].(map[string]any)["campaignId"] != canary ||
		run.Outputs["items"].([]any)[0].(map[string]any)["campaign_id"] != canary ||
		run.Outputs["maps"].([]map[string]any)[0]["campaignId"] != canary ||
		run.Jobs["review"].Outputs["campaign_id"] != canary ||
		run.Steps["record"].Outputs["campaignId"] != canary {
		t.Fatal("campaign projection mutated the stored workflow run")
	}
}

func TestLoadWorkflowEventEnvelopeUsesMetadataAndProtectedContextRoutes(t *testing.T) {
	configPath := writeWorkflowEventTestConfig(t, t.TempDir())
	var paths []string
	installEventProxyStubs(t, func(req *http.Request, _ time.Duration) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		if got := req.Header.Get("Authorization"); got != "Bearer gateway-pid-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if strings.HasSuffix(req.URL.Path, "/workflow-context") {
			return workflowEventUpstreamResponse("issues.opened"), nil
		}
		return eventUpstreamResponse(http.StatusOK, workflowEventMetadataJSON()), nil
	})

	h := NewHandler(configPath)
	metadata, err := h.loadWorkflowEventEnvelope(
		context.Background(),
		testEventID,
		false,
	)
	if err != nil {
		t.Fatalf("metadata load error = %v", err)
	}
	if metadata.ID != testEventID ||
		metadata.Source != "github" ||
		metadata.Connector != "primary" ||
		metadata.Type != "issues.opened" {
		t.Fatalf("metadata envelope = %#v", metadata)
	}
	if metadata.Payload != nil {
		t.Fatalf("metadata payload = %s, want nil", metadata.Payload)
	}
	if metadata.DedupeKey != "" {
		t.Fatalf("metadata dedupe key = %q, want omitted", metadata.DedupeKey)
	}

	envelope, err := h.loadWorkflowEventEnvelope(
		context.Background(),
		testEventID,
		true,
	)
	if err != nil {
		t.Fatalf("context load error = %v", err)
	}
	if envelope.DedupeKey != "" {
		t.Fatalf("context dedupe key = %q, want omitted", envelope.DedupeKey)
	}
	if !strings.Contains(string(envelope.Payload), "9007199254740993") {
		t.Fatalf("payload changed exact numeric token: %s", envelope.Payload)
	}
	eventContext, err := workflows.EventContextFromEnvelope(envelope)
	if err != nil {
		t.Fatalf("EventContextFromEnvelope() error = %v", err)
	}
	payload, ok := eventContext["payload"].(map[string]any)
	if !ok {
		t.Fatalf("event payload context = %#v", eventContext["payload"])
	}
	if got, ok := payload["large"].(json.Number); !ok || got.String() != "9007199254740993" {
		t.Fatalf("large payload value = %#v (%T)", payload["large"], payload["large"])
	}

	wantPaths := []string{
		"/runtime/eventing/events/" + testEventID,
		"/runtime/eventing/events/" + testEventID + "/workflow-context",
	}
	if fmt.Sprint(paths) != fmt.Sprint(wantPaths) {
		t.Fatalf("upstream paths = %#v, want %#v", paths, wantPaths)
	}
}

func TestWorkflowEventContextResponseLimitRequiresCanonicalExactHeader(t *testing.T) {
	header := http.Header{
		eventoperator.WorkflowEventPayloadBytesHeader: {"41943040"},
	}
	limit, payloadBytes, err := workflowEventContextResponseLimit(header)
	if err != nil {
		t.Fatalf("workflowEventContextResponseLimit() error = %v", err)
	}
	if want := int64(40<<20) + int64(workflowEventContextMetadataAllowanceBytes); limit != want {
		t.Fatalf("limit = %d, want %d", limit, want)
	}
	if payloadBytes != 40<<20 {
		t.Fatalf("payload bytes = %d", payloadBytes)
	}

	maximumInt64 := ^uint64(0) >> 1
	boundary := (maximumInt64 - 1) - workflowEventContextMetadataAllowanceBytes
	header.Set(eventoperator.WorkflowEventPayloadBytesHeader, strconv.FormatUint(boundary, 10))
	limit, payloadBytes, err = workflowEventContextResponseLimit(header)
	if err != nil ||
		uint64(limit) != maximumInt64-1 ||
		uint64(payloadBytes) != boundary {
		t.Fatalf("boundary = (%d, %d), error=%v", limit, payloadBytes, err)
	}
	header.Set(eventoperator.WorkflowEventPayloadBytesHeader, strconv.FormatUint(boundary+1, 10))
	if _, _, err = workflowEventContextResponseLimit(header); err == nil {
		t.Fatal("overflowing maximum error = nil")
	}

	for _, values := range [][]string{
		nil,
		{""},
		{"01"},
		{" 1"},
		{"+1"},
		{"1", "2"},
	} {
		testHeader := http.Header{}
		for _, value := range values {
			testHeader.Add(eventoperator.WorkflowEventPayloadBytesHeader, value)
		}
		if _, _, err := workflowEventContextResponseLimit(testHeader); err == nil {
			t.Fatalf("header values %#v error = nil", values)
		}
	}
}

func TestLoadWorkflowEventEnvelopeAcceptsStoredPayloadAboveLoweredConfig(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Events.Ingress.MaxPayloadBytes = 1
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		response := workflowEventUpstreamResponse("issues.opened")
		response.Header.Set(eventoperator.WorkflowEventPayloadBytesHeader, strconv.Itoa(40<<20))
		response.ContentLength = int64(eventProxyPayloadMaxBytes + (2 << 20))
		return response, nil
	})

	envelope, err := NewHandler(configPath).loadWorkflowEventEnvelope(
		context.Background(),
		testEventID,
		true,
	)
	if err != nil {
		t.Fatalf("configured large context load error = %v", err)
	}
	if envelope.ID != testEventID {
		t.Fatalf("envelope = %#v", envelope)
	}
}

func TestLoadWorkflowEventEnvelopeRejectsInvalidIDsWithoutUpstream(t *testing.T) {
	configPath := writeWorkflowEventTestConfig(t, t.TempDir())
	upstreamCalls := 0
	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		upstreamCalls++
		return eventUpstreamResponse(http.StatusOK, workflowEventMetadataJSON()), nil
	})
	h := NewHandler(configPath)

	for _, eventID := range []string{
		"",
		" " + testEventID,
		testEventID + " ",
		"ev_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"not-an-event",
	} {
		_, err := h.loadWorkflowEventEnvelope(context.Background(), eventID, false)
		if !errors.Is(err, errWorkflowEventInvalid) {
			t.Fatalf("event ID %q error = %v, want invalid sentinel", eventID, err)
		}
	}
	if upstreamCalls != 0 {
		t.Fatalf("invalid IDs reached upstream %d times", upstreamCalls)
	}
}

func TestLoadWorkflowEventEnvelopeMapsFailuresToOpaqueSentinels(t *testing.T) {
	tests := []struct {
		name string
		do   func(*http.Request, time.Duration) (*http.Response, error)
		want error
	}{
		{
			name: "not found",
			do: func(*http.Request, time.Duration) (*http.Response, error) {
				return eventUpstreamResponse(http.StatusNotFound, `{"error":"private-store-detail"}`), nil
			},
			want: errWorkflowEventNotFound,
		},
		{
			name: "operator authentication",
			do: func(*http.Request, time.Duration) (*http.Response, error) {
				return eventUpstreamResponse(http.StatusUnauthorized, `{"error":"private-token-detail"}`), nil
			},
			want: errWorkflowEventUnavailable,
		},
		{
			name: "transport",
			do: func(*http.Request, time.Duration) (*http.Response, error) {
				return nil, errors.New("private-network-detail")
			},
			want: errWorkflowEventUnavailable,
		},
		{
			name: "missing payload length header",
			do: func(*http.Request, time.Duration) (*http.Response, error) {
				response := workflowEventUpstreamResponse("issues.opened")
				response.Header.Del(eventoperator.WorkflowEventPayloadBytesHeader)
				return response, nil
			},
			want: errWorkflowEventUnavailable,
		},
		{
			name: "duplicate payload length header",
			do: func(*http.Request, time.Duration) (*http.Response, error) {
				response := workflowEventUpstreamResponse("issues.opened")
				response.Header.Add(eventoperator.WorkflowEventPayloadBytesHeader, "1")
				return response, nil
			},
			want: errWorkflowEventUnavailable,
		},
		{
			name: "non-canonical payload length header",
			do: func(*http.Request, time.Duration) (*http.Response, error) {
				response := workflowEventUpstreamResponse("issues.opened")
				response.Header.Set(eventoperator.WorkflowEventPayloadBytesHeader, "001")
				return response, nil
			},
			want: errWorkflowEventUnavailable,
		},
		{
			name: "unknown response field",
			do: func(*http.Request, time.Duration) (*http.Response, error) {
				return workflowEventBodyResponse(
					strings.TrimSuffix(workflowEventContextJSON("issues.opened"), "}") +
						`,"dedupe_key":"private-dedupe-detail"}`,
				), nil
			},
			want: errWorkflowEventUnavailable,
		},
		{
			name: "missing required timestamp",
			do: func(*http.Request, time.Duration) (*http.Response, error) {
				return workflowEventBodyResponse(
					`{"id":"` + testEventID + `","source":"github","connector":"primary",` +
						`"type":"issues.opened","payload":{}}`,
				), nil
			},
			want: errWorkflowEventUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := writeWorkflowEventTestConfig(t, t.TempDir())
			installEventProxyStubs(t, test.do)
			h := NewHandler(configPath)
			_, err := h.loadWorkflowEventEnvelope(
				context.Background(),
				testEventID,
				true,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			for _, private := range []string{
				"private-store-detail",
				"private-token-detail",
				"private-network-detail",
				"private-dedupe-detail",
			} {
				if strings.Contains(fmt.Sprint(err), private) {
					t.Fatalf("error exposed %q: %v", private, err)
				}
			}
		})
	}
}

func TestWriteWorkflowEventContextErrorUsesStrictOpaqueContract(t *testing.T) {
	tests := []struct {
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			err:        fmt.Errorf("%w: private invalid detail", errWorkflowEventInvalid),
			wantStatus: http.StatusBadRequest,
			wantBody:   "workflow event ID is invalid\n",
		},
		{
			err:        fmt.Errorf("%w: private store detail", errWorkflowEventNotFound),
			wantStatus: http.StatusNotFound,
			wantBody:   "workflow event was not found\n",
		},
		{
			err:        errors.New("private upstream detail"),
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "workflow event service is unavailable\n",
		},
	}
	for _, test := range tests {
		rec := httptest.NewRecorder()
		writeWorkflowEventContextError(rec, test.err)
		if rec.Code != test.wantStatus || rec.Body.String() != test.wantBody {
			t.Fatalf(
				"response = (%d, %q), want (%d, %q)",
				rec.Code,
				rec.Body.String(),
				test.wantStatus,
				test.wantBody,
			)
		}
		if strings.Contains(rec.Body.String(), "private") {
			t.Fatalf("response exposed internal error: %q", rec.Body.String())
		}
		if test.wantStatus == http.StatusServiceUnavailable &&
			rec.Header().Get("Retry-After") != "1" {
			t.Fatalf("Retry-After = %q, want 1", rec.Header().Get("Retry-After"))
		}
	}
}

func TestHandleListWorkflowsProjectsEventTrigger(t *testing.T) {
	workspace := t.TempDir()
	workflowDir := filepath.Join(workspace, "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(workflowDir, "event-preview.yml"),
		[]byte(workflowEventDraftYAML()),
		0o644,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	h := NewHandler(writeWorkflowEventTestConfig(t, workspace))
	rec := httptest.NewRecorder()
	h.handleListWorkflows(
		rec,
		httptest.NewRequest(http.MethodGet, "/api/workflows", nil),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Workflows []struct {
			Ref          string                  `json:"ref"`
			EventTrigger *workflows.EventTrigger `json:"event_trigger"`
		} `json:"workflows"`
	}
	if unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &response); unmarshalErr != nil {
		t.Fatalf("response JSON error = %v", unmarshalErr)
	}
	if len(response.Workflows) != 1 ||
		response.Workflows[0].Ref != "workflows/event-preview.yml" ||
		response.Workflows[0].EventTrigger == nil {
		t.Fatalf("workflows = %#v", response.Workflows)
	}
	trigger := response.Workflows[0].EventTrigger
	if len(trigger.Sources) != 1 ||
		trigger.Sources[0] != "github" ||
		len(trigger.Types) != 1 ||
		trigger.Types[0] != "issues.opened" ||
		len(trigger.Attributes["body_authenticated"]) != 1 ||
		trigger.Attributes["body_authenticated"][0] != "true" {
		t.Fatalf("event trigger = %#v", trigger)
	}
}

func TestHandleTestWorkflowDevelopmentRejectsMalformedStrictOrOversizedBeforeMutation(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	h := NewHandler(writeWorkflowEventTestConfig(t, workspace))
	original, err := workflows.StartWorkflowDevelopment(
		ctx,
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"},
		workflows.WorkflowDevelopmentStartRequest{
			Prompt:    "original prompt",
			TargetRef: "workflows/original.yml",
		},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	upstreamCalls := 0
	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		upstreamCalls++
		return workflowEventUpstreamResponse("issues.opened"), nil
	})
	runtimeCalls := 0
	oldRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = oldRunners })
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		runtimeCalls++
		runner := &workflowEventCaptureRunner{}
		return workflowRuntimeRunners{Tools: runner, Agents: runner}
	}

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name: "manual unknown top-level field",
			body: `{"yaml":"name: changed","unknown":"` +
				eventDraftPrivateAPIDiagnostic + `"}`,
			wantStatus: http.StatusBadRequest,
			wantBody:   "invalid workflow draft test request\n",
		},
		{
			name: "event unknown nested delivery field",
			body: `{"event_id":"` + testEventID +
				`","yaml":"name: changed","delivery":{"channel":"x","unknown":"` +
				eventDraftPrivateAPIDiagnostic + `"}}`,
			wantStatus: http.StatusBadRequest,
			wantBody:   "invalid workflow draft test request\n",
		},
		{
			name: "event trailing JSON value",
			body: `{"event_id":"` + testEventID +
				`","yaml":"name: changed"}{"private":"` +
				eventDraftPrivateAPIDiagnostic + `"}`,
			wantStatus: http.StatusBadRequest,
			wantBody:   "invalid workflow draft test request\n",
		},
		{
			name: "event malformed JSON",
			body: `{"event_id":"` + testEventID +
				`","yaml":"name: changed","private":`,
			wantStatus: http.StatusBadRequest,
			wantBody:   "invalid workflow draft test request\n",
		},
		{
			name: "event oversized JSON",
			body: `{"event_id":"` + testEventID + `","yaml":"` +
				strings.Repeat("x", workflowDevelopmentTestRequestMaxBytes) +
				`"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   "workflow draft test request exceeds 1 MiB\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.handleTestWorkflowDevelopment(
				rec,
				httptest.NewRequest(
					http.MethodPost,
					"/api/workflows/development/test",
					strings.NewReader(test.body),
				),
			)
			if rec.Code != test.wantStatus || rec.Body.String() != test.wantBody {
				t.Fatalf(
					"response = (%d, %q), want (%d, %q)",
					rec.Code,
					rec.Body.String(),
					test.wantStatus,
					test.wantBody,
				)
			}
			if strings.Contains(rec.Body.String(), eventDraftPrivateAPIDiagnostic) {
				t.Fatalf("response leaked submitted diagnostic: %s", rec.Body.String())
			}
			active, err := workflows.GetWorkflowDevelopmentSession(workspace)
			if err != nil {
				t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
			}
			if active == nil ||
				active.Prompt != original.Prompt ||
				active.TargetWorkflowRef != original.TargetWorkflowRef ||
				active.YAML != original.YAML ||
				active.LastTest != nil {
				t.Fatalf("rejected request mutated session: %#v", active)
			}
			runs, err := workflows.NewFileRunStore(workspace).ListRuns(ctx)
			if err != nil {
				t.Fatalf("ListRuns() error = %v", err)
			}
			if len(runs) != 0 {
				t.Fatalf("rejected request created runs: %#v", runs)
			}
		})
	}
	if upstreamCalls != 0 || runtimeCalls != 0 {
		t.Fatalf(
			"rejected requests reached upstream/runtime: upstream=%d runtime=%d",
			upstreamCalls,
			runtimeCalls,
		)
	}
}

func TestHandleTestWorkflowDevelopmentUsesProductionEventParityContext(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	configPath := writeWorkflowEventTestConfig(t, workspace)
	h := NewHandler(configPath)
	session, err := workflows.StartWorkflowDevelopment(
		ctx,
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"},
		workflows.WorkflowDevelopmentStartRequest{
			Prompt:    "triage matching events",
			TargetRef: "workflows/event-preview.yml",
		},
	)
	if err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}

	upstreamCalls := 0
	installEventProxyStubs(t, func(req *http.Request, _ time.Duration) (*http.Response, error) {
		upstreamCalls++
		if req.URL.Path != "/runtime/eventing/events/"+testEventID+"/workflow-context" {
			t.Fatalf("upstream path = %q", req.URL.Path)
		}
		return workflowEventUpstreamResponse("issues.opened"), nil
	})
	runner := &workflowEventCaptureRunner{}
	oldRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = oldRunners })
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		return workflowRuntimeRunners{
			Tools:         runner,
			Agents:        runner,
			RuntimeEvents: runner,
		}
	}

	draftYAML := workflowEventDraftYAML()
	body, err := json.Marshal(workflowDevelopmentTestRequest{
		YAML:    &draftYAML,
		EventID: testEventID,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/workflows/development/test",
		strings.NewReader(string(body)),
	)
	h.handleTestWorkflowDevelopment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var response struct {
		Session *workflows.WorkflowDevelopmentSession `json:"session"`
		Result  *workflows.RunResult                  `json:"result"`
		Error   string                                `json:"error"`
	}
	if unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &response); unmarshalErr != nil {
		t.Fatalf("response JSON error = %v", unmarshalErr)
	}
	if response.Error != "" ||
		response.Session == nil ||
		response.Session.LastTest == nil ||
		response.Result == nil {
		t.Fatalf("response = %#v", response)
	}
	if response.Session.LastTest.EventID != testEventID {
		t.Fatalf("last test event ID = %q", response.Session.LastTest.EventID)
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want one atomic context load", upstreamCalls)
	}

	run, err := workflows.NewFileRunStore(workspace).GetRun(ctx, response.Result.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.WorkflowRef != "draft:"+session.TargetWorkflowRef {
		t.Fatalf("workflow ref = %q", run.WorkflowRef)
	}
	wantSession := workflows.EventWorkflowSession(session.TargetWorkflowRef, testEventID)
	if run.Session != wantSession {
		t.Fatalf("run session = %q, want %q", run.Session, wantSession)
	}
	if run.Inputs["event_id"] != testEventID ||
		run.Inputs["source"] != "github" ||
		run.Inputs["connector"] != "primary" ||
		run.Inputs["type"] != "issues.opened" {
		t.Fatalf("run inputs = %#v", run.Inputs)
	}
	if _, exists := run.Inputs["dispatch_id"]; exists {
		t.Fatalf("draft run invented dispatch_id: %#v", run.Inputs)
	}
	if run.Event["id"] != testEventID {
		t.Fatalf("run event = %#v", run.Event)
	}
	if !workflowDeliveryIsEmpty(run.Delivery) {
		t.Fatalf("run delivery = %#v, want empty", run.Delivery)
	}
	if len(runner.toolRequests) != 1 {
		t.Fatalf("tool requests = %d, want one", len(runner.toolRequests))
	}
	toolRequest := runner.toolRequests[0]
	if toolRequest.Session != wantSession ||
		!workflowDeliveryIsEmpty(toolRequest.Delivery) {
		t.Fatalf("tool request context = %#v", toolRequest)
	}
	if toolRequest.Args["event_id"] != testEventID {
		t.Fatalf("tool event_id = %#v", toolRequest.Args["event_id"])
	}
	if got := fmt.Sprint(toolRequest.Args["large"]); got != "9007199254740993" {
		t.Fatalf("tool large value = %#v, want exact integer", toolRequest.Args["large"])
	}

	rawRun, err := os.ReadFile(filepath.Join(
		workspace,
		"workflow_runs",
		response.Result.RunID,
		"run.json",
	))
	if err != nil {
		t.Fatalf("read raw run error = %v", err)
	}
	if !strings.Contains(string(rawRun), "9007199254740993") {
		t.Fatalf("raw run changed exact numeric token: %s", rawRun)
	}
	if strings.Contains(string(rawRun), `"dispatch_id"`) {
		t.Fatalf("raw draft run contains dispatch_id: %s", rawRun)
	}
}

func TestHandleTestWorkflowDevelopmentRejectsEventContextOverridesBeforeLoading(t *testing.T) {
	configPath := writeWorkflowEventTestConfig(t, t.TempDir())
	h := NewHandler(configPath)
	upstreamCalls := 0
	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		upstreamCalls++
		return workflowEventUpstreamResponse("issues.opened"), nil
	})

	tests := []struct {
		name   string
		mutate func(*workflowDevelopmentTestRequest)
	}{
		{
			name: "inputs",
			mutate: func(req *workflowDevelopmentTestRequest) {
				req.Inputs = map[string]any{"manual": true}
			},
		},
		{
			name: "secrets",
			mutate: func(req *workflowDevelopmentTestRequest) {
				req.Secrets = map[string]string{"token": "private-secret"}
			},
		},
		{
			name: "session",
			mutate: func(req *workflowDevelopmentTestRequest) {
				req.Session = "manual-session"
			},
		},
		{
			name: "delivery channel",
			mutate: func(req *workflowDevelopmentTestRequest) {
				req.Delivery.Channel = "slack"
			},
		},
		{
			name: "delivery chat",
			mutate: func(req *workflowDevelopmentTestRequest) {
				req.Delivery.ChatID = "chat"
			},
		},
		{
			name: "delivery topic",
			mutate: func(req *workflowDevelopmentTestRequest) {
				req.Delivery.TopicID = "topic"
			},
		},
		{
			name: "delivery thread",
			mutate: func(req *workflowDevelopmentTestRequest) {
				req.Delivery.ThreadTS = "thread"
			},
		},
		{
			name: "delivery message",
			mutate: func(req *workflowDevelopmentTestRequest) {
				req.Delivery.MessageID = "message"
			},
		},
		{
			name: "delivery reply target",
			mutate: func(req *workflowDevelopmentTestRequest) {
				req.Delivery.ReplyToMessageID = "reply"
			},
		},
		{
			name: "delivery reply handles",
			mutate: func(req *workflowDevelopmentTestRequest) {
				req.Delivery.ReplyHandles = map[string]string{"thread": "private-handle"}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := workflowDevelopmentTestRequest{EventID: testEventID}
			test.mutate(&request)
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			rec := httptest.NewRecorder()
			h.handleTestWorkflowDevelopment(
				rec,
				httptest.NewRequest(
					http.MethodPost,
					"/api/workflows/development/test",
					strings.NewReader(string(body)),
				),
			)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "private-") {
				t.Fatalf("response exposed override value: %s", rec.Body.String())
			}
		})
	}
	if upstreamCalls != 0 {
		t.Fatalf("rejected overrides reached upstream %d times", upstreamCalls)
	}
}

func TestHandleTestWorkflowDevelopmentRecordsEventTriggerMismatchWithoutRunning(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	h := NewHandler(writeWorkflowEventTestConfig(t, workspace))
	if _, err := workflows.StartWorkflowDevelopment(
		ctx,
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"},
		workflows.WorkflowDevelopmentStartRequest{
			Prompt:    "triage matching events",
			TargetRef: "workflows/event-preview.yml",
		},
	); err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		return workflowEventUpstreamResponse("pull_request.opened"), nil
	})
	runtimeCalls := 0
	oldRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = oldRunners })
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		runtimeCalls++
		runner := &workflowEventCaptureRunner{}
		return workflowRuntimeRunners{Tools: runner, Agents: runner}
	}

	draftYAML := workflowEventDraftYAML()
	body, err := json.Marshal(workflowDevelopmentTestRequest{
		YAML:    &draftYAML,
		EventID: testEventID,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	rec := httptest.NewRecorder()
	h.handleTestWorkflowDevelopment(
		rec,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/development/test",
			strings.NewReader(string(body)),
		),
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if runtimeCalls != 0 {
		t.Fatalf("mismatched event created runtime %d times", runtimeCalls)
	}
	var response struct {
		Session *workflows.WorkflowDevelopmentSession `json:"session"`
		Error   string                                `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON error = %v", err)
	}
	if response.Error != "selected event does not match workflow event trigger" ||
		response.Session == nil ||
		response.Session.LastTest == nil ||
		response.Session.LastTest.EventID != testEventID ||
		response.Session.LastTest.Status != "validation_failed" {
		t.Fatalf("mismatch response = %#v", response)
	}
}

func TestEventBackedDraftFailureIsSafeAcrossPostRunListEventsAndSSE(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{name: "tool", yaml: workflowEventFailureDraftYAML("tool/fail")},
		{name: "agent", yaml: workflowEventFailureDraftYAML("agent/main")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			h := NewHandler(writeWorkflowEventTestConfig(t, workspace))
			if _, err := workflows.StartWorkflowDevelopment(
				ctx,
				workspace,
				workflows.RuntimeCompatibility{
					PicoclawVersion: "v1.0.0",
					GitCommit:       "abc123",
				},
				workflows.WorkflowDevelopmentStartRequest{
					Prompt:    "fail safely",
					TargetRef: "workflows/event-failure.yml",
				},
			); err != nil {
				t.Fatalf("StartWorkflowDevelopment() error = %v", err)
			}
			installEventProxyStubs(
				t,
				func(*http.Request, time.Duration) (*http.Response, error) {
					return workflowEventPrivateUpstreamResponse("issues.opened"), nil
				},
			)
			runner := &workflowEventFailingRunner{}
			oldRunners := newWorkflowRuntimeRunners
			t.Cleanup(func() { newWorkflowRuntimeRunners = oldRunners })
			newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
				return workflowRuntimeRunners{
					Tools:         runner,
					Agents:        runner,
					RuntimeEvents: runner,
				}
			}

			body, err := json.Marshal(workflowDevelopmentTestRequest{
				YAML:    &test.yaml,
				EventID: testEventID,
			})
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			rec := httptest.NewRecorder()
			h.handleTestWorkflowDevelopment(
				rec,
				httptest.NewRequest(
					http.MethodPost,
					"/api/workflows/development/test",
					strings.NewReader(string(body)),
				),
			)
			if rec.Code != http.StatusOK {
				t.Fatalf("POST status = %d, body=%s", rec.Code, rec.Body.String())
			}
			assertSafeEventDraftTestResponse(t, rec)

			var postResponse struct {
				Result *workflows.RunResult `json:"result"`
			}
			if unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &postResponse); unmarshalErr != nil {
				t.Fatalf("POST response JSON error = %v", unmarshalErr)
			}
			runID := postResponse.Result.RunID
			store := workflows.NewFileRunStore(workspace)
			rawRun, err := store.GetRun(ctx, runID)
			if err != nil {
				t.Fatalf("GetRun(raw) error = %v", err)
			}
			if !strings.Contains(rawRun.Error, eventDraftPrivateAPIDiagnostic) {
				t.Fatalf("raw audit run did not retain diagnostic: %#v", rawRun)
			}

			detail := httptest.NewRecorder()
			detailReq := httptest.NewRequest(
				http.MethodGet,
				"/api/workflows/runs/"+runID,
				nil,
			)
			detailReq.SetPathValue("run_id", runID)
			h.handleGetWorkflowRun(detail, detailReq)
			if detail.Code != http.StatusOK {
				t.Fatalf("detail status = %d, body=%s", detail.Code, detail.Body.String())
			}
			var projected workflows.Run
			if err := json.Unmarshal(detail.Body.Bytes(), &projected); err != nil {
				t.Fatalf("detail JSON error = %v", err)
			}
			assertSafeEventDraftRunProjection(t, &projected)
			payload := projected.Event["payload"].(map[string]any)
			if payload["private"] != eventDraftPrivateAPIDiagnostic {
				t.Fatalf("structured redacted event was not preserved: %#v", projected.Event)
			}

			list := httptest.NewRecorder()
			h.handleListWorkflowRuns(
				list,
				httptest.NewRequest(http.MethodGet, "/api/workflows/runs", nil),
			)
			var listed struct {
				Runs []workflowRunCollectionSummary `json:"runs"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil ||
				len(listed.Runs) != 1 {
				t.Fatalf("list response = %#v, error=%v", listed, err)
			}
			if listed.Runs[0].ID != runID || listed.Runs[0].Status != workflows.RunStatusFailed ||
				listed.Runs[0].Origin == nil ||
				listed.Runs[0].Origin.Kind != workflows.RunOriginExternalEventDraftTest ||
				strings.Contains(list.Body.String(), `"error"`) ||
				strings.Contains(list.Body.String(), `"event"`) ||
				strings.Contains(list.Body.String(), `"jobs"`) ||
				strings.Contains(list.Body.String(), `"steps"`) {
				t.Fatalf("unsafe run-list summary = %s", list.Body.String())
			}

			events := httptest.NewRecorder()
			eventsReq := httptest.NewRequest(
				http.MethodGet,
				"/api/workflows/runs/"+runID+"/events",
				nil,
			)
			eventsReq.SetPathValue("run_id", runID)
			h.handleGetWorkflowRunEvents(events, eventsReq)
			assertSafeEventDraftLifecycleResponse(t, events.Body.String())

			stream := httptest.NewRecorder()
			streamReq := httptest.NewRequest(
				http.MethodGet,
				"/api/workflows/runs/"+runID+"/events/stream?once=true",
				nil,
			)
			streamReq.SetPathValue("run_id", runID)
			h.handleStreamWorkflowRunEvents(stream, streamReq)
			assertSafeEventDraftLifecycleResponse(t, stream.Body.String())

			retry := httptest.NewRecorder()
			retryReq := httptest.NewRequest(
				http.MethodPost,
				"/api/workflows/runs/"+runID+"/retry",
				strings.NewReader(`{}`),
			)
			retryReq.SetPathValue("run_id", runID)
			h.handleRetryWorkflowRun(retry, retryReq)
			if retry.Code != http.StatusConflict ||
				retry.Body.String() !=
					"event-backed draft run retries are unavailable; run the draft test again\n" {
				t.Fatalf("retry response = (%d, %q)", retry.Code, retry.Body.String())
			}
		})
	}
}

func TestUnsafeFallbackAncestryStaysMaskedAcrossWorkflowRunHTTP(t *testing.T) {
	for _, test := range []struct {
		name         string
		runID        string
		parentRunID  string
		retryOfRunID string
	}{
		{
			name:        "whitespace parent",
			runID:       "wr_http_whitespace_parent",
			parentRunID: " wr_missing_parent ",
		},
		{
			name:         "invalid retry without parent",
			runID:        "wr_http_invalid_retry",
			retryOfRunID: "invalid-retry-id",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			h := NewHandler(writeWorkflowEventTestConfig(t, workspace))
			now := time.Now().UTC()
			run := &workflows.Run{
				ID:           test.runID,
				WorkflowRef:  "workflows/reusable.yml",
				Status:       workflows.RunStatusRunning,
				ParentRunID:  test.parentRunID,
				RetryOfRunID: test.retryOfRunID,
				Event: map[string]any{
					"id": "event-context-present",
					"payload": map[string]any{
						"redacted": "visible",
					},
				},
				Error:     eventDraftPrivateAPIDiagnostic,
				CreatedAt: now,
				UpdatedAt: now,
			}
			store := workflows.NewFileRunStore(workspace)
			if err := store.CreateRun(ctx, run); err != nil {
				t.Fatalf("CreateRun() error = %v", err)
			}
			if err := store.AppendEvent(ctx, workflows.RunEvent{
				Kind:    "workflow.step.failed",
				RunID:   run.ID,
				Message: eventDraftPrivateAPIDiagnostic,
				Payload: map[string]any{
					"private": eventDraftPrivateAPIDiagnostic,
				},
			}); err != nil {
				t.Fatalf("AppendEvent() error = %v", err)
			}

			detail := httptest.NewRecorder()
			detailRequest := httptest.NewRequest(
				http.MethodGet,
				"/api/workflows/runs/"+run.ID,
				nil,
			)
			detailRequest.SetPathValue("run_id", run.ID)
			h.handleGetWorkflowRun(detail, detailRequest)
			if detail.Code != http.StatusOK {
				t.Fatalf(
					"detail status = %d, body=%s",
					detail.Code,
					detail.Body.String(),
				)
			}
			var detailed workflows.Run
			if err := json.Unmarshal(detail.Body.Bytes(), &detailed); err != nil {
				t.Fatalf("detail JSON error = %v", err)
			}
			assertUnsafeFallbackRunMasked(t, &detailed)

			list := httptest.NewRecorder()
			h.handleListWorkflowRuns(
				list,
				httptest.NewRequest(http.MethodGet, "/api/workflows/runs", nil),
			)
			if list.Code != http.StatusOK {
				t.Fatalf(
					"list status = %d, body=%s",
					list.Code,
					list.Body.String(),
				)
			}
			var listed struct {
				Runs []workflowRunCollectionSummary `json:"runs"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil ||
				len(listed.Runs) != 1 {
				t.Fatalf("list response = %#v, error=%v", listed, err)
			}
			if listed.Runs[0].ID != run.ID || listed.Runs[0].Origin != nil ||
				strings.Contains(list.Body.String(), `"error"`) ||
				strings.Contains(list.Body.String(), `"event"`) ||
				strings.Contains(list.Body.String(), `"jobs"`) ||
				strings.Contains(list.Body.String(), `"steps"`) {
				t.Fatalf("unsafe fallback run-list summary = %s", list.Body.String())
			}

			events := httptest.NewRecorder()
			eventsRequest := httptest.NewRequest(
				http.MethodGet,
				"/api/workflows/runs/"+run.ID+"/events",
				nil,
			)
			eventsRequest.SetPathValue("run_id", run.ID)
			h.handleGetWorkflowRunEvents(events, eventsRequest)
			if events.Code != http.StatusOK {
				t.Fatalf(
					"events status = %d, body=%s",
					events.Code,
					events.Body.String(),
				)
			}
			assertSafeEventDraftLifecycleResponse(t, events.Body.String())

			stream := httptest.NewRecorder()
			streamRequest := httptest.NewRequest(
				http.MethodGet,
				"/api/workflows/runs/"+run.ID+"/events/stream?once=true",
				nil,
			)
			streamRequest.SetPathValue("run_id", run.ID)
			h.handleStreamWorkflowRunEvents(stream, streamRequest)
			if stream.Code != http.StatusOK {
				t.Fatalf(
					"stream status = %d, body=%s",
					stream.Code,
					stream.Body.String(),
				)
			}
			assertSafeEventDraftLifecycleResponse(t, stream.Body.String())

			retry := httptest.NewRecorder()
			retryRequest := httptest.NewRequest(
				http.MethodPost,
				"/api/workflows/runs/"+run.ID+"/retry",
				strings.NewReader(`{}`),
			)
			retryRequest.SetPathValue("run_id", run.ID)
			h.handleRetryWorkflowRun(retry, retryRequest)
			if retry.Code != http.StatusConflict ||
				!strings.Contains(
					retry.Body.String(),
					"event-backed draft run retries are unavailable",
				) {
				t.Fatalf(
					"retry response = (%d, %q)",
					retry.Code,
					retry.Body.String(),
				)
			}

			cancel := httptest.NewRecorder()
			cancelRequest := httptest.NewRequest(
				http.MethodPost,
				"/api/workflows/runs/"+run.ID+"/cancel",
				strings.NewReader(
					`{"reason":"`+eventDraftPrivateAPIDiagnostic+`"}`,
				),
			)
			cancelRequest.SetPathValue("run_id", run.ID)
			h.handleCancelWorkflowRun(cancel, cancelRequest)
			if cancel.Code != http.StatusOK {
				t.Fatalf(
					"cancel status = %d, body=%s",
					cancel.Code,
					cancel.Body.String(),
				)
			}
			var canceled workflows.Run
			if err := json.Unmarshal(cancel.Body.Bytes(), &canceled); err != nil {
				t.Fatalf("cancel JSON error = %v", err)
			}
			assertUnsafeFallbackRunMasked(t, &canceled)
			if canceled.Status != workflows.RunStatusCanceled ||
				canceled.CancelReason !=
					workflows.EventBackedDraftCancelReasonDiagnostic ||
				strings.Contains(cancel.Body.String(), eventDraftPrivateAPIDiagnostic) {
				t.Fatalf("unsafe cancel response = %s", cancel.Body.String())
			}
		})
	}
}

func TestPrunedTrustedOriginKindControlsWorkflowRunHTTPFamily(t *testing.T) {
	const (
		eventID           = "ev_0123456789abcdef0123456789abcdef"
		dispatchID        = "dsp_0123456789abcdef0123456789abcdef"
		workflowRef       = "workflows/pruned-origin.yml"
		productionRootID  = "wr_pruned_production_root"
		productionChildID = "wr_pruned_production_child"
		draftRootID       = "wr_pruned_draft_root"
		draftChildID      = "wr_pruned_draft_child"
	)
	ctx := context.Background()
	workspace := t.TempDir()
	configPath := writeWorkflowDependencyTestConfig(t, workspace, true)
	writeWorkflowDependencyDefinition(
		t,
		workspace,
		"automation",
		workflowRef,
		workflowRunReadinessDefinition,
	)
	h := NewHandler(configPath)
	restoreDependencies := stubWorkflowDependencyRuntime(t, nil)
	defer restoreDependencies()
	revalidateWorkflowRunReadinessDefinition(t, h, configPath)
	readiness := checkPublishedWorkflowDependencies(t, h, workflowRef)
	if !readiness.Ready {
		t.Fatalf("dependency response = %#v, want ready", readiness)
	}

	event := func() map[string]any {
		return map[string]any{
			"id":        eventID,
			"source":    "github",
			"connector": "primary",
			"type":      "issues.opened",
			"payload": map[string]any{
				"private": eventDraftPrivateAPIDiagnostic,
			},
		}
	}
	productionOrigin := &workflows.RunOrigin{
		Kind:       workflows.RunOriginExternalEvent,
		EventID:    eventID,
		DispatchID: dispatchID,
		RootRunID:  productionRootID,
	}
	draftOrigin := &workflows.RunOrigin{
		Kind:      workflows.RunOriginExternalEventDraftTest,
		EventID:   eventID,
		RootRunID: draftRootID,
	}
	now := time.Now().UTC()
	completed := now.Add(time.Second)
	store := workflows.NewFileRunStore(workspace)
	for _, run := range []*workflows.Run{
		{
			ID:          productionRootID,
			WorkflowRef: workflowRef,
			Status:      workflows.RunStatusFailed,
			Event:       event(),
			Inputs: map[string]any{
				"event_id":    eventID,
				"dispatch_id": dispatchID,
			},
			Origin:      productionOrigin,
			CreatedAt:   now,
			UpdatedAt:   now,
			CompletedAt: &completed,
		},
		{
			ID:          productionChildID,
			WorkflowRef: workflowRef,
			Status:      workflows.RunStatusFailed,
			ParentRunID: productionRootID,
			Event:       event(),
			Inputs: map[string]any{
				"event_id":    eventID,
				"dispatch_id": dispatchID,
			},
			Origin:      productionOrigin,
			Error:       eventDraftPrivateAPIDiagnostic,
			CreatedAt:   now,
			UpdatedAt:   now,
			CompletedAt: &completed,
		},
		{
			ID:          draftRootID,
			WorkflowRef: "draft:" + workflowRef,
			Status:      workflows.RunStatusFailed,
			Event:       event(),
			Inputs:      map[string]any{"event_id": eventID},
			Origin:      draftOrigin,
			CreatedAt:   now,
			UpdatedAt:   now,
			CompletedAt: &completed,
		},
		{
			ID:          draftChildID,
			WorkflowRef: workflowRef,
			Status:      workflows.RunStatusFailed,
			ParentRunID: draftRootID,
			Event:       event(),
			Inputs:      map[string]any{"event_id": eventID},
			Origin:      draftOrigin,
			Error:       eventDraftPrivateAPIDiagnostic,
			CreatedAt:   now,
			UpdatedAt:   now,
			CompletedAt: &completed,
		},
	} {
		if err := store.CreateRun(ctx, run); err != nil {
			t.Fatalf("CreateRun(%s) error = %v", run.ID, err)
		}
	}
	for _, runID := range []string{productionChildID, draftChildID} {
		if err := store.AppendEvent(ctx, workflows.RunEvent{
			Kind:    "workflow.step.failed",
			RunID:   runID,
			Message: eventDraftPrivateAPIDiagnostic,
			Payload: map[string]any{
				"private": eventDraftPrivateAPIDiagnostic,
			},
		}); err != nil {
			t.Fatalf("AppendEvent(%s) error = %v", runID, err)
		}
	}
	for _, rootID := range []string{productionRootID, draftRootID} {
		if err := store.DeleteRun(ctx, rootID); err != nil {
			t.Fatalf("DeleteRun(%s) error = %v", rootID, err)
		}
	}

	getDetail := func(runID string) workflows.Run {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/workflows/runs/"+runID,
			nil,
		)
		request.SetPathValue("run_id", runID)
		h.handleGetWorkflowRun(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf(
				"detail %s status = %d, body=%s",
				runID,
				recorder.Code,
				recorder.Body.String(),
			)
		}
		var run workflows.Run
		if err := json.Unmarshal(recorder.Body.Bytes(), &run); err != nil {
			t.Fatalf("detail %s JSON error = %v", runID, err)
		}
		return run
	}
	productionDetail := getDetail(productionChildID)
	if productionDetail.Error != eventDraftPrivateAPIDiagnostic ||
		productionDetail.Origin == nil ||
		productionDetail.Origin.Kind != workflows.RunOriginExternalEvent {
		t.Fatalf("pruned production detail = %#v", productionDetail)
	}
	draftDetail := getDetail(draftChildID)
	if draftDetail.Error != workflows.EventBackedDraftRunErrorDiagnostic ||
		draftDetail.Origin == nil ||
		draftDetail.Origin.Kind != workflows.RunOriginExternalEventDraftTest {
		t.Fatalf("pruned draft detail = %#v", draftDetail)
	}

	listRecorder := httptest.NewRecorder()
	h.handleListWorkflowRuns(
		listRecorder,
		httptest.NewRequest(http.MethodGet, "/api/workflows/runs", nil),
	)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf(
			"list status = %d, body=%s",
			listRecorder.Code,
			listRecorder.Body.String(),
		)
	}
	var listResponse struct {
		Runs []workflowRunCollectionSummary `json:"runs"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("list JSON error = %v", err)
	}
	listed := make(map[string]workflowRunCollectionSummary, len(listResponse.Runs))
	for _, run := range listResponse.Runs {
		listed[run.ID] = run
	}
	if listed[productionChildID].Origin == nil ||
		listed[productionChildID].Origin.Kind != workflows.RunOriginExternalEvent {
		t.Fatalf("pruned production list = %#v", listed[productionChildID])
	}
	if listed[draftChildID].Origin == nil ||
		listed[draftChildID].Origin.Kind != workflows.RunOriginExternalEventDraftTest {
		t.Fatalf("pruned draft list = %#v", listed[draftChildID])
	}
	if strings.Contains(listRecorder.Body.String(), `"error"`) {
		t.Fatalf("pruned run list leaked diagnostics: %s", listRecorder.Body.String())
	}

	getEvents := func(runID string) []workflows.RunEvent {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodGet,
			"/api/workflows/runs/"+runID+"/events",
			nil,
		)
		request.SetPathValue("run_id", runID)
		h.handleGetWorkflowRunEvents(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf(
				"events %s status = %d, body=%s",
				runID,
				recorder.Code,
				recorder.Body.String(),
			)
		}
		var response struct {
			Events []workflows.RunEvent `json:"events"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("events %s JSON error = %v", runID, err)
		}
		return response.Events
	}
	productionEvents := getEvents(productionChildID)
	if len(productionEvents) != 1 ||
		productionEvents[0].Message != eventDraftPrivateAPIDiagnostic ||
		productionEvents[0].Payload["private"] != eventDraftPrivateAPIDiagnostic {
		t.Fatalf("pruned production events = %#v", productionEvents)
	}
	draftEvents := getEvents(draftChildID)
	if len(draftEvents) != 1 ||
		draftEvents[0].Message != workflows.EventBackedDraftEventMessageDiagnostic ||
		draftEvents[0].Payload["diagnostic"] !=
			workflows.EventBackedDraftEventPayloadDiagnostic {
		t.Fatalf("pruned draft events = %#v", draftEvents)
	}

	draftRetry := postWorkflowRetry(t, h, draftChildID, map[string]any{})
	if draftRetry.Code != http.StatusConflict ||
		!strings.Contains(
			draftRetry.Body.String(),
			"event-backed draft run retries are unavailable",
		) {
		t.Fatalf(
			"pruned draft retry = (%d, %q)",
			draftRetry.Code,
			draftRetry.Body.String(),
		)
	}
	productionRetry := postWorkflowRetry(
		t,
		h,
		productionChildID,
		map[string]any{
			"expected_dependency_revision": readiness.Revision,
		},
	)
	if productionRetry.Code != http.StatusOK {
		t.Fatalf(
			"pruned production retry = (%d, %q)",
			productionRetry.Code,
			productionRetry.Body.String(),
		)
	}
	runs, err := store.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	var retried *workflows.Run
	for index := range runs {
		if runs[index].RetryOfRunID == productionChildID {
			retried = &runs[index]
			break
		}
	}
	if retried == nil ||
		retried.Origin == nil ||
		retried.Origin.Kind != workflows.RunOriginExternalEvent ||
		retried.Origin.RootRunID != productionRootID {
		t.Fatalf("pruned production retry run = %#v", retried)
	}
}

func TestEventBackedDraftAsyncFailureKeepsRunningAndCompletionSnapshotsSafe(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	h := NewHandler(writeWorkflowEventTestConfig(t, workspace))
	if _, err := workflows.StartWorkflowDevelopment(
		ctx,
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "v1.0.0", GitCommit: "abc123"},
		workflows.WorkflowDevelopmentStartRequest{
			Prompt:    "fail asynchronously",
			TargetRef: "workflows/event-async-failure.yml",
		},
	); err != nil {
		t.Fatalf("StartWorkflowDevelopment() error = %v", err)
	}
	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		return workflowEventPrivateUpstreamResponse("issues.opened"), nil
	})
	started := make(chan struct{})
	release := make(chan struct{})
	runner := &workflowEventFailingRunner{started: started, release: release}
	oldRunners := newWorkflowRuntimeRunners
	t.Cleanup(func() { newWorkflowRuntimeRunners = oldRunners })
	newWorkflowRuntimeRunners = func(string) workflowRuntimeRunners {
		return workflowRuntimeRunners{
			Tools:         runner,
			Agents:        runner,
			RuntimeEvents: runner,
		}
	}
	draftYAML := workflowEventFailureDraftYAML("tool/fail")
	body, err := json.Marshal(workflowDevelopmentTestRequest{
		YAML:    &draftYAML,
		EventID: testEventID,
		Async:   true,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	rec := httptest.NewRecorder()
	h.handleTestWorkflowDevelopment(
		rec,
		httptest.NewRequest(
			http.MethodPost,
			"/api/workflows/development/test",
			strings.NewReader(string(body)),
		),
	)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("async status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		Session *workflows.WorkflowDevelopmentSession `json:"session"`
		Result  *workflows.RunResult                  `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("accepted JSON error = %v", err)
	}
	if accepted.Session == nil ||
		accepted.Session.LastTest == nil ||
		accepted.Session.LastTest.EventID != testEventID ||
		accepted.Session.LastTest.Status != workflows.RunStatusRunning ||
		accepted.Session.LastTest.Error != "" ||
		accepted.Result == nil ||
		accepted.Result.Error != "" {
		t.Fatalf("running snapshot = %#v", accepted)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("async tool did not start")
	}
	close(release)

	deadline := time.Now().Add(3 * time.Second)
	for {
		session, err := workflows.GetWorkflowDevelopmentSession(workspace)
		if err != nil {
			t.Fatalf("GetWorkflowDevelopmentSession() error = %v", err)
		}
		if session != nil &&
			session.LastTest != nil &&
			session.LastTest.Status != workflows.RunStatusRunning {
			if session.LastTest.RunID != accepted.Result.RunID ||
				session.LastTest.EventID != testEventID ||
				session.LastTest.Status != workflows.RunStatusFailed ||
				session.LastTest.Error != workflows.EventBackedDraftTestFailureDiagnostic {
				t.Fatalf("completion snapshot = %#v", session.LastTest)
			}
			if strings.Contains(session.LastTest.Error, eventDraftPrivateAPIDiagnostic) {
				t.Fatalf("completion leaked diagnostic: %#v", session.LastTest)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("async completion was not recorded: %#v", session)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeWorkflowEventTestConfig(t *testing.T, workspace string) string {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	return configPath
}

func workflowEventMetadataJSON() string {
	return `{
		"id":"` + testEventID + `",
		"source":"github",
		"connector":"primary",
		"type":"issues.opened",
		"actor":{"id":"octocat","type":"user","display_name":"Octo Cat"},
		"subject":{"id":"repo-1","type":"repository","name":"acme/example"},
		"received_at":"2026-07-29T12:34:56Z",
		"payload_bytes":70,
		"attributes":{"body_authenticated":"true"},
		"routing":{"status":"complete"}
	}`
}

func workflowEventContextJSON(eventType string) string {
	return `{
		"id":"` + testEventID + `",
		"source":"github",
		"connector":"primary",
		"type":"` + eventType + `",
		"actor":{"id":"octocat","type":"user","display_name":"Octo Cat"},
		"subject":{"id":"repo-1","type":"repository","name":"acme/example"},
		"received_at":"2026-07-29T12:34:56Z",
		"payload":{"title":"Investigate","large":9007199254740993,"token":"[REDACTED]"},
		"attributes":{"body_authenticated":"true"}
	}`
}

func workflowEventUpstreamResponse(eventType string) *http.Response {
	return workflowEventBodyResponse(workflowEventContextJSON(eventType))
}

func workflowEventBodyResponse(body string) *http.Response {
	response := eventUpstreamResponse(http.StatusOK, body)
	var view struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(body), &view); err == nil {
		response.Header.Set(
			eventoperator.WorkflowEventPayloadBytesHeader,
			strconv.Itoa(len(view.Payload)),
		)
	}
	return response
}

func workflowEventDraftYAML() string {
	return `name: Event preview
on:
  event:
    sources: github
    types: issues.opened
    attributes:
      body_authenticated: "true"
jobs:
  develop:
    runs-on: picoclaw
    steps:
      - id: inspect
        uses: tool/capture
        with:
          event_id: "${{ inputs.event_id }}"
          large: "${{ event.payload.large }}"
`
}

func workflowEventFailureDraftYAML(target string) string {
	step := `        uses: tool/fail
        with:
          private: "${{ event.payload.private }}"`
	if target == "agent/main" {
		step = `        uses: agent/main
        with:
          prompt: "${{ event.payload.private }}"
          history: none
          tools: none`
	}
	return `name: Event failure preview
on:
  event:
    sources: github
    types: issues.opened
jobs:
  develop:
    runs-on: picoclaw
    steps:
      - id: fail
` + step + `
`
}

func workflowEventPrivateUpstreamResponse(eventType string) *http.Response {
	body := strings.Replace(
		workflowEventContextJSON(eventType),
		`"payload":{`,
		`"payload":{"private":"`+eventDraftPrivateAPIDiagnostic+`",`,
		1,
	)
	return workflowEventBodyResponse(body)
}

type workflowEventCaptureRunner struct {
	toolRequests []workflows.ToolRequest
}

func (runner *workflowEventCaptureRunner) RunAgent(
	context.Context,
	workflows.AgentRequest,
) (map[string]any, error) {
	return map[string]any{"text": "ok"}, nil
}

func (runner *workflowEventCaptureRunner) RunTool(
	_ context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	runner.toolRequests = append(runner.toolRequests, request)
	return map[string]any{"args": request.Args}, nil
}

func (runner *workflowEventCaptureRunner) PublishNonBlocking(
	runtimeevents.Event,
) runtimeevents.PublishResult {
	return runtimeevents.PublishResult{}
}

type workflowEventFailingRunner struct {
	started chan struct{}
	release chan struct{}
}

func (runner *workflowEventFailingRunner) RunAgent(
	ctx context.Context,
	request workflows.AgentRequest,
) (map[string]any, error) {
	return runner.fail(ctx, request.Prompt)
}

func (runner *workflowEventFailingRunner) RunTool(
	ctx context.Context,
	request workflows.ToolRequest,
) (map[string]any, error) {
	return runner.fail(ctx, fmt.Sprint(request.Args["private"]))
}

func (runner *workflowEventFailingRunner) fail(
	ctx context.Context,
	private string,
) (map[string]any, error) {
	if runner.started != nil {
		close(runner.started)
	}
	if runner.release != nil {
		select {
		case <-runner.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return map[string]any{"visible": "partial output"},
		fmt.Errorf("provider echoed %s", private)
}

func (runner *workflowEventFailingRunner) PublishNonBlocking(
	runtimeevents.Event,
) runtimeevents.PublishResult {
	return runtimeevents.PublishResult{}
}

func assertSafeEventDraftTestResponse(
	t *testing.T,
	rec *httptest.ResponseRecorder,
) {
	t.Helper()
	var response struct {
		Session *workflows.WorkflowDevelopmentSession `json:"session"`
		Result  *workflows.RunResult                  `json:"result"`
		Error   string                                `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("test response JSON error = %v", err)
	}
	if response.Session == nil ||
		response.Session.LastTest == nil ||
		response.Result == nil {
		t.Fatalf("test response = %#v", response)
	}
	if response.Error != workflows.EventBackedDraftTestFailureDiagnostic ||
		response.Result.Error != workflows.EventBackedDraftTestFailureDiagnostic ||
		response.Session.LastTest.Error != workflows.EventBackedDraftTestFailureDiagnostic ||
		response.Result.Status != workflows.RunStatusFailed ||
		response.Session.LastTest.Status != workflows.RunStatusFailed ||
		response.Session.LastTest.EventID != testEventID {
		t.Fatalf("unsafe test response = %#v", response)
	}
	if strings.Contains(rec.Body.String(), "provider echoed") ||
		strings.Contains(rec.Body.String(), eventDraftPrivateAPIDiagnostic) {
		t.Fatalf("test response exposed private diagnostic: %s", rec.Body.String())
	}
}

func assertSafeEventDraftRunProjection(t *testing.T, run *workflows.Run) {
	t.Helper()
	if run == nil ||
		run.Error != workflows.EventBackedDraftRunErrorDiagnostic {
		t.Fatalf("run projection = %#v", run)
	}
	if run.Origin == nil ||
		run.Origin.Kind != workflows.RunOriginExternalEventDraftTest ||
		run.Origin.EventID != testEventID ||
		run.Origin.DispatchID != "" ||
		run.Origin.RootRunID != run.ID {
		t.Fatalf("run origin = %#v, run id = %q", run.Origin, run.ID)
	}
	if len(run.Jobs) == 0 || len(run.Steps) == 0 {
		t.Fatalf("run projection omitted executions: %#v", run)
	}
	for _, job := range run.Jobs {
		if job.Error != "" &&
			job.Error != workflows.EventBackedDraftJobErrorDiagnostic {
			t.Fatalf("job diagnostic was not masked: %#v", job)
		}
	}
	for _, step := range run.Steps {
		if step.Error != "" &&
			step.Error != workflows.EventBackedDraftStepErrorDiagnostic {
			t.Fatalf("step diagnostic was not masked: %#v", step)
		}
	}
	encoded, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("json.Marshal(run) error = %v", err)
	}
	if strings.Contains(string(encoded), "provider echoed") {
		t.Fatalf("run projection exposed private diagnostic: %s", encoded)
	}
}

func assertUnsafeFallbackRunMasked(t *testing.T, run *workflows.Run) {
	t.Helper()
	if run == nil ||
		run.Error != workflows.EventBackedDraftRunErrorDiagnostic ||
		run.Origin != nil {
		t.Fatalf("unsafe fallback projection = %#v", run)
	}
	encoded, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("json.Marshal(run) error = %v", err)
	}
	if strings.Contains(string(encoded), eventDraftPrivateAPIDiagnostic) {
		t.Fatalf("unsafe fallback projection exposed diagnostic: %s", encoded)
	}
}

func assertSafeEventDraftLifecycleResponse(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(
		body,
		workflows.EventBackedDraftEventMessageDiagnostic,
	) ||
		!strings.Contains(
			body,
			workflows.EventBackedDraftEventPayloadDiagnostic,
		) {
		t.Fatalf("lifecycle response did not contain fixed projections: %s", body)
	}
	if strings.Contains(body, "provider echoed") ||
		strings.Contains(body, eventDraftPrivateAPIDiagnostic) {
		t.Fatalf("lifecycle response exposed private diagnostic: %s", body)
	}
}

func workflowDeliveryIsEmpty(delivery workflows.Delivery) bool {
	return delivery.Channel == "" &&
		delivery.ChatID == "" &&
		delivery.TopicID == "" &&
		delivery.ThreadTS == "" &&
		delivery.MessageID == "" &&
		delivery.ReplyToMessageID == "" &&
		len(delivery.ReplyHandles) == 0
}
