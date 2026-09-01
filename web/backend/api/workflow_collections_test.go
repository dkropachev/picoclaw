package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type workflowDefinitionsCollectionTestResponse struct {
	Workflows      []workflowDefinitionResponse `json:"workflows"`
	Total          int                          `json:"total"`
	NextCursor     string                       `json:"next_cursor"`
	CanonicalQuery string                       `json:"canonical_query"`
	QuerySchema    collectionquery.Schema       `json:"query_schema"`
}

type workflowRunsCollectionTestResponse struct {
	Runs           []workflowRunCollectionSummary `json:"runs"`
	Total          int                            `json:"total"`
	NextCursor     string                         `json:"next_cursor"`
	CanonicalQuery string                         `json:"canonical_query"`
	QuerySchema    collectionquery.Schema         `json:"query_schema"`
}

func TestWorkflowDefinitionIDMatchesSharedCollectionResourceContract(t *testing.T) {
	const ref = "workflows/team/review.yml"
	want, err := encodeCollectionResourceID("workflow-definition", ref)
	if err != nil {
		t.Fatal(err)
	}
	got, err := workflows.WorkflowDefinitionID(ref)
	if err != nil || got != want {
		t.Fatalf("workflow definition ID = %q, %v; want %q", got, err, want)
	}
}

func TestWorkflowDefinitionsCollectionPagesQueriesAndResolvesOpaqueIDs(t *testing.T) {
	workspace, _, mux := newWorkflowCollectionAPIHarness(t)
	writeWorkflowCollectionDefinition(t, workspace, "alpha.yml", `name: Alpha
on:
  manual: {}
  workflow_call:
    inputs:
      issue:
        type: string
        required: true
    secrets:
      token:
        required: true
jobs: {}
`)
	writeWorkflowCollectionDefinition(t, workspace, "beta.yml", `name: Beta
on:
  schedule:
    - cron: "0 9 * * *"
jobs: {}
`)
	writeWorkflowCollectionDefinition(t, workspace, "invalid.yml", "name: [unterminated\n")

	first := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodGet,
		"/api/workflows/definitions?limit=1",
		"",
		nil,
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first page status = %d, body=%s", first.Code, first.Body.String())
	}
	var page workflowDefinitionsCollectionTestResponse
	decodeWorkflowCollectionJSON(t, first, &page)
	if page.Total != 3 || len(page.Workflows) != 1 || page.NextCursor == "" ||
		page.CanonicalQuery != "ALL ORDER BY ref ASC" {
		t.Fatalf("first page = %#v", page)
	}
	if !workflows.ValidWorkflowDefinitionID(page.Workflows[0].ID) ||
		page.Workflows[0].WorkflowCall != nil || page.Workflows[0].EventTrigger != nil {
		t.Fatalf("definition list summary = %#v", page.Workflows[0])
	}
	assertWorkflowCollectionSchemaField(t, page.QuerySchema, "trigger", collectionquery.TypeEnum)

	second := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodGet,
		"/api/workflows/definitions?limit=1&cursor="+url.QueryEscape(page.NextCursor),
		"",
		nil,
	)
	var secondPage workflowDefinitionsCollectionTestResponse
	decodeWorkflowCollectionJSON(t, second, &secondPage)
	if len(secondPage.Workflows) != 1 || secondPage.Workflows[0].ID == page.Workflows[0].ID {
		t.Fatalf("second page = %#v", secondPage)
	}

	invalidOnly := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodGet,
		"/api/workflows/definitions?query="+url.QueryEscape(`status = "invalid" ORDER BY ref ASC`),
		"",
		nil,
	)
	var invalidPage workflowDefinitionsCollectionTestResponse
	decodeWorkflowCollectionJSON(t, invalidOnly, &invalidPage)
	if invalidPage.Total != 1 || len(invalidPage.Workflows) != 1 ||
		invalidPage.Workflows[0].Error != "Workflow definition is invalid" ||
		strings.Contains(invalidPage.Workflows[0].Error, workspace) {
		t.Fatalf("invalid projection = %#v", invalidPage)
	}

	all := serveWorkflowCollectionRequest(
		t, mux, http.MethodGet, "/api/workflows/definitions", "", nil,
	)
	var allPage workflowDefinitionsCollectionTestResponse
	decodeWorkflowCollectionJSON(t, all, &allPage)
	var alpha workflowDefinitionResponse
	for _, item := range allPage.Workflows {
		if item.Ref == "workflows/alpha.yml" {
			alpha = item
		}
	}
	if alpha.ID == "" || alpha.Trigger != "multiple" || alpha.Inputs != 1 || alpha.Secrets != 1 {
		t.Fatalf("alpha summary = %#v", alpha)
	}
	detail := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodGet,
		"/api/workflows/definitions/"+alpha.ID,
		"",
		nil,
	)
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body=%s", detail.Code, detail.Body.String())
	}
	var detailBody struct {
		Workflow workflowDefinitionResponse `json:"workflow"`
	}
	decodeWorkflowCollectionJSON(t, detail, &detailBody)
	if detailBody.Workflow.ID != alpha.ID || detailBody.Workflow.WorkflowCall == nil ||
		len(detailBody.Workflow.WorkflowCall.Inputs) != 1 {
		t.Fatalf("detail = %#v", detailBody.Workflow)
	}

	invalidID := serveWorkflowCollectionRequest(
		t, mux, http.MethodGet, "/api/workflows/definitions/not-an-id", "", nil,
	)
	requireWorkflowCollectionError(t, invalidID, http.StatusBadRequest, "invalid_workflow_definition_id")
	missingID, err := workflows.WorkflowDefinitionID("workflows/missing.yml")
	if err != nil {
		t.Fatal(err)
	}
	missing := serveWorkflowCollectionRequest(
		t, mux, http.MethodGet, "/api/workflows/definitions/"+missingID, "", nil,
	)
	requireWorkflowCollectionError(t, missing, http.StatusNotFound, "workflow_definition_not_found")

	wrongCursor := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodGet,
		"/api/workflows/definitions?query="+url.QueryEscape(`name ~ "a" ORDER BY ref ASC`)+
			"&cursor="+url.QueryEscape(page.NextCursor),
		"",
		nil,
	)
	requireWorkflowCollectionError(t, wrongCursor, http.StatusBadRequest, "invalid_cursor")
}

func TestWorkflowRunsCollectionProjectsPrivacyBeforePagingAndBindsCursors(t *testing.T) {
	workspace, _, mux := newWorkflowCollectionAPIHarness(t)
	store := workflows.NewFileRunStore(workspace)
	base := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	runs := []*workflows.Run{
		{
			ID: "wr_newest", WorkflowRef: "workflows/alpha.yml",
			Status: workflows.RunStatusRunning, Session: "session-a",
			Delivery: workflows.Delivery{Channel: "secret-channel"},
			Event:    map[string]any{"secret": "event"},
			Inputs:   map[string]any{"secret": "input"},
			Outputs:  map[string]any{"secret": "output"},
			Jobs:     map[string]workflows.JobExecution{"private": {Error: "job error"}},
			Steps:    map[string]workflows.StepExecution{"private": {Error: "step error"}},
			Error:    "run error", CancelReason: "cancel reason",
			CreatedAt: base.Add(3 * time.Minute), UpdatedAt: base.Add(3 * time.Minute),
		},
		{
			ID: "wr_draft", WorkflowRef: "draft:workflows/alpha.yml",
			Status: workflows.RunStatusFailed, Session: "session-b",
			CreatedAt: base.Add(2 * time.Minute), UpdatedAt: base.Add(2 * time.Minute),
		},
		{
			ID: "wr_related", WorkflowRef: "workflows/related.yml",
			Status: workflows.RunStatusSucceeded, ParentRunID: "wr_hidden",
			CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute),
		},
		{
			ID: "wr_hidden", WorkflowRef: "inline/pr-lifecycle/private",
			Status: workflows.RunStatusSucceeded, ChildRunIDs: []string{"wr_related"},
			CreatedAt: base, UpdatedAt: base,
		},
	}
	for _, run := range runs {
		if err := store.CreateRun(context.Background(), run); err != nil {
			t.Fatalf("CreateRun(%s) error = %v", run.ID, err)
		}
	}

	first := serveWorkflowCollectionRequest(
		t, mux, http.MethodGet, "/api/workflows/runs?limit=1", "", nil,
	)
	if first.Code != http.StatusOK {
		t.Fatalf("runs status = %d, body=%s", first.Code, first.Body.String())
	}
	var page workflowRunsCollectionTestResponse
	decodeWorkflowCollectionJSON(t, first, &page)
	if page.Total != 3 || len(page.Runs) != 1 || page.Runs[0].ID != "wr_newest" ||
		page.NextCursor == "" || page.CanonicalQuery != "ALL ORDER BY created DESC" {
		t.Fatalf("runs first page = %#v", page)
	}
	for _, omitted := range []string{
		`"delivery"`, `"event"`, `"inputs"`, `"outputs"`, `"jobs"`,
		`"steps"`, `"error"`, `"cancel_reason"`,
	} {
		if strings.Contains(first.Body.String(), omitted) {
			t.Fatalf("run list leaked %s: %s", omitted, first.Body.String())
		}
	}
	wantWorkflowID, err := workflows.WorkflowDefinitionID("workflows/alpha.yml")
	if err != nil || page.Runs[0].WorkflowID != wantWorkflowID {
		t.Fatalf("workflow ID = %q, %v; want %q", page.Runs[0].WorkflowID, err, wantWorkflowID)
	}
	assertWorkflowCollectionSchemaField(t, page.QuerySchema, "created", collectionquery.TypeTimestamp)
	assertWorkflowCollectionSuggestedValue(t, page.QuerySchema, "session", "session-a")

	failed := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodGet,
		"/api/workflows/runs?query="+url.QueryEscape(`status = "failed" ORDER BY created DESC`),
		"",
		nil,
	)
	var failedPage workflowRunsCollectionTestResponse
	decodeWorkflowCollectionJSON(t, failed, &failedPage)
	if failedPage.Total != 1 || len(failedPage.Runs) != 1 ||
		failedPage.Runs[0].ID != "wr_draft" || failedPage.Runs[0].WorkflowID != "" {
		t.Fatalf("failed runs = %#v", failedPage)
	}

	second := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodGet,
		"/api/workflows/runs?limit=1&cursor="+url.QueryEscape(page.NextCursor),
		"",
		nil,
	)
	var secondPage workflowRunsCollectionTestResponse
	decodeWorkflowCollectionJSON(t, second, &secondPage)
	if len(secondPage.Runs) != 1 || secondPage.Runs[0].ID != "wr_draft" {
		t.Fatalf("runs second page = %#v", secondPage)
	}

	relatedQuery := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodGet,
		"/api/workflows/runs?query="+url.QueryEscape(`id = "wr_related"`),
		"",
		nil,
	)
	var relatedPage workflowRunsCollectionTestResponse
	decodeWorkflowCollectionJSON(t, relatedQuery, &relatedPage)
	if relatedPage.Total != 1 || strings.Contains(relatedQuery.Body.String(), "parent_run_id") {
		t.Fatalf("private relationship leaked: %#v", relatedPage)
	}

	wrongCursor := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodGet,
		"/api/workflows/runs?query="+url.QueryEscape(`status != "waiting" ORDER BY created DESC`)+
			"&cursor="+url.QueryEscape(page.NextCursor),
		"",
		nil,
	)
	requireWorkflowCollectionError(t, wrongCursor, http.StatusBadRequest, "invalid_cursor")

	detail := serveWorkflowCollectionRequest(
		t, mux, http.MethodGet, "/api/workflows/runs/wr_newest", "", nil,
	)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), wantWorkflowID) {
		t.Fatalf("run detail status/body = %d %s", detail.Code, detail.Body.String())
	}
	invalid := serveWorkflowCollectionRequest(
		t, mux, http.MethodGet, "/api/workflows/runs/%2e%2e", "", nil,
	)
	if invalid.Code != http.StatusNotFound && invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid encoded run status = %d, body=%s", invalid.Code, invalid.Body.String())
	}
	invalid = serveWorkflowCollectionRequest(
		t, mux, http.MethodGet, "/api/workflows/runs/not-a-run", "", nil,
	)
	requireWorkflowCollectionError(t, invalid, http.StatusBadRequest, "invalid_workflow_run_id")
	hidden := serveWorkflowCollectionRequest(
		t, mux, http.MethodGet, "/api/workflows/runs/wr_hidden", "", nil,
	)
	requireWorkflowCollectionError(t, hidden, http.StatusNotFound, "workflow_run_not_found")
}

func TestWorkflowCollectionSuggestionsAreBoundedAndDiscardIsFencedAndSameOrigin(t *testing.T) {
	definitions := make([]workflowDefinitionResponse, 150)
	runs := make([]workflowRunCollectionSummary, 150)
	for index := range definitions {
		definitions[index] = workflowDefinitionResponse{
			Ref:  "workflows/item-" + strings.Repeat("x", index%10) + string(rune('a'+index%26)) + ".yml",
			Name: "name-" + strings.Repeat("y", index%12) + string(rune('a'+index%26)),
		}
		runs[index] = workflowRunCollectionSummary{
			ID:          "wr_item_" + strings.Repeat("z", index%8) + string(rune('a'+index%26)),
			WorkflowRef: definitions[index].Ref,
			Session:     "session-" + strings.Repeat("s", index%7) + string(rune('a'+index%26)),
		}
	}
	for _, schema := range []collectionquery.Schema{
		workflowDefinitionSchemaWithSuggestions(definitions),
		workflowRunSchemaWithSuggestions(runs),
	} {
		for _, field := range schema.Fields {
			if len(field.SuggestedValues) > collectionquery.MaxSuggestedValues {
				t.Fatalf("field %s suggestions = %d", field.Name, len(field.SuggestedValues))
			}
		}
	}

	workspace, _, mux := newWorkflowCollectionAPIHarness(t)
	session, err := workflows.StartWorkflowDevelopment(
		context.Background(),
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "test"},
		workflows.WorkflowDevelopmentStartRequest{
			Reason:    workflows.WorkflowDevelopmentReasonNew,
			TargetRef: "workflows/discard.yml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"session_id":"` + session.ID + `","expected_session_revision":"` + session.SessionRevision + `"}`
	crossOrigin := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodPost,
		"/api/workflows/development/discard",
		body,
		map[string]string{
			"Content-Type":   "application/json",
			"Origin":         "https://attacker.invalid",
			"Sec-Fetch-Site": "cross-site",
		},
	)
	requireWorkflowCollectionError(t, crossOrigin, http.StatusForbidden, "cross_origin_mutation")
	if active, getErr := workflows.GetWorkflowDevelopmentSession(workspace); getErr != nil || active == nil {
		t.Fatalf("cross-origin discard changed session: %#v, %v", active, getErr)
	}

	stale := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodPost,
		"/api/workflows/development/discard",
		`{"session_id":"`+session.ID+`","expected_session_revision":"stale"}`,
		map[string]string{"Content-Type": "application/json"},
	)
	requireWorkflowCollectionError(t, stale, http.StatusConflict, "session_revision_mismatch")

	success := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodPost,
		"/api/workflows/development/discard",
		body,
		map[string]string{"Content-Type": "application/json"},
	)
	if success.Code != http.StatusOK {
		t.Fatalf("discard status = %d, body=%s", success.Code, success.Body.String())
	}
}

func TestWorkflowCollectionSchemaResolversCoverEveryFieldAndTypedOperator(t *testing.T) {
	definitionAID, _ := workflows.WorkflowDefinitionID("workflows/a.yml")
	definitionBID, _ := workflows.WorkflowDefinitionID("workflows/b.yml")
	definitions := []workflowDefinitionResponse{
		{
			ID: definitionAID, Ref: "workflows/a.yml", Name: "Alpha",
			Status: workflows.WorkflowValidationStatusValid, Trigger: "manual",
			Inputs: 2, Secrets: 1,
		},
		{
			ID: definitionBID, Ref: "workflows/b.yml", Name: "Beta",
			Status: workflows.WorkflowValidationStatusNeedsReview, Trigger: "multiple",
			Inputs: 0, Secrets: 0,
		},
	}
	definitionQueries := []string{
		`ref ~ "a.yml"`,
		`name = "Alpha"`,
		`status IN ("valid", "invalid")`,
		`trigger != "multiple"`,
		`inputs >= 2`,
		`secrets = 1`,
	}
	for _, raw := range definitionQueries {
		query, err := collectionquery.Parse(raw, workflowDefinitionCollectionSchema)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", raw, err)
		}
		page, err := pageWorkflowDefinitions(definitions, collectionListRequest{
			Query: query, Limit: 200, Now: time.Now().UTC(),
		})
		if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != definitionAID {
			t.Fatalf("definition query %q page = %#v, %v", raw, page, err)
		}
	}
	definitionOrder, err := collectionquery.Parse(
		"ORDER BY status ASC, name DESC, ref ASC",
		workflowDefinitionCollectionSchema,
	)
	if err != nil {
		t.Fatal(err)
	}
	orderedDefinitions, err := pageWorkflowDefinitions(definitions, collectionListRequest{
		Query: definitionOrder, Limit: 200, Now: time.Now().UTC(),
	})
	if err != nil || len(orderedDefinitions.Items) != 2 ||
		orderedDefinitions.Items[0].ID != definitionBID {
		t.Fatalf("multi-field definition order = %#v, %v", orderedDefinitions, err)
	}
	if _, ok := resolveWorkflowDefinitionCollectionField(
		definitions[0], "unknown", time.Now(),
	); ok {
		t.Fatal("definition resolver accepted unknown field")
	}

	created := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	completed := created.Add(time.Hour)
	runs := []workflowRunCollectionSummary{
		{
			ID: "wr_alpha", WorkflowRef: "workflows/a.yml", Status: workflows.RunStatusSucceeded,
			Session: "session-a", CreatedAt: created, UpdatedAt: created.Add(time.Minute),
			CompletedAt: &completed,
		},
		{
			ID: "wr_beta", WorkflowRef: "workflows/b.yml", Status: workflows.RunStatusRunning,
			Session: "session-b", Origin: &workflows.RunOrigin{Kind: workflows.RunOriginExternalEvent},
			CreatedAt: created.Add(2 * time.Hour), UpdatedAt: created.Add(2 * time.Hour),
		},
	}
	runQueries := []struct {
		raw    string
		wantID string
	}{
		{`id = "wr_alpha"`, "wr_alpha"},
		{`workflow ~ "b.yml"`, "wr_beta"},
		{`status = "succeeded"`, "wr_alpha"},
		{`session != "session-a"`, "wr_beta"},
		{`origin = "external_event"`, "wr_beta"},
		{`created < "2026-08-27T13:00:00Z"`, "wr_alpha"},
		{`updated >= "2026-08-27T14:00:00Z"`, "wr_beta"},
		{`completed = "2026-08-27T13:00:00Z"`, "wr_alpha"},
	}
	for _, test := range runQueries {
		query, parseErr := collectionquery.Parse(test.raw, workflowRunCollectionSchema)
		if parseErr != nil {
			t.Fatalf("Parse(%q) error = %v", test.raw, parseErr)
		}
		page, pageErr := pageWorkflowRuns(runs, collectionListRequest{
			Query: query, Limit: 200, Now: time.Now().UTC(),
		})
		if pageErr != nil || page.Total != 1 || len(page.Items) != 1 ||
			page.Items[0].ID != test.wantID {
			t.Fatalf("run query %q page = %#v, %v", test.raw, page, pageErr)
		}
	}
	runOrder, err := collectionquery.Parse(
		"ORDER BY status ASC, created DESC, id ASC",
		workflowRunCollectionSchema,
	)
	if err != nil {
		t.Fatal(err)
	}
	orderedRuns, err := pageWorkflowRuns(runs, collectionListRequest{
		Query: runOrder, Limit: 200, Now: time.Now().UTC(),
	})
	if err != nil || len(orderedRuns.Items) != 2 || orderedRuns.Items[0].ID != "wr_beta" {
		t.Fatalf("multi-field run order = %#v, %v", orderedRuns, err)
	}
	if _, ok := resolveWorkflowRunCollectionField(runs[0], "unknown", time.Now()); ok {
		t.Fatal("run resolver accepted unknown field")
	}
}

func TestWorkflowCollectionHelperAndProjectionBoundaries(t *testing.T) {
	if projectWorkflowDevelopmentSession(nil) != nil {
		t.Fatal("nil development session projected non-nil")
	}
	withoutID := workflowRunCollectionItem{Run: workflows.Run{
		ID: "wr_no_id", WorkflowRef: "draft:workflows/a.yml",
	}}
	encoded, err := json.Marshal(withoutID)
	if err != nil || strings.Contains(string(encoded), "workflow_id") {
		t.Fatalf("run without definition ID = %s, %v", encoded, err)
	}
	_, err = json.Marshal(workflowRunCollectionItem{Run: workflows.Run{
		ID: "wr_bad_json", Inputs: map[string]any{"unsupported": make(chan int)},
	}})
	if err == nil {
		t.Fatal("run projection accepted unsupported JSON")
	}

	triggerCases := []struct {
		workflow *workflows.Workflow
		want     string
	}{
		{nil, "none"},
		{&workflows.Workflow{}, "none"},
		{
			&workflows.Workflow{On: workflows.WorkflowTriggers{Manual: map[string]any{}}},
			"manual",
		},
		{
			&workflows.Workflow{On: workflows.WorkflowTriggers{
				Schedule: []workflows.ScheduleTrigger{{Cron: "* * * * *"}},
			}},
			"schedule",
		},
		{
			&workflows.Workflow{On: workflows.WorkflowTriggers{
				ChannelMessage: &workflows.ChannelMessageTrigger{},
			}},
			"channel_message",
		},
		{
			&workflows.Workflow{On: workflows.WorkflowTriggers{
				Command: &workflows.CommandTrigger{},
			}},
			"command",
		},
		{
			&workflows.Workflow{On: workflows.WorkflowTriggers{
				RuntimeEvent: &workflows.RuntimeEventTrigger{},
			}},
			"runtime_event",
		},
		{
			&workflows.Workflow{On: workflows.WorkflowTriggers{
				Event: &workflows.EventTrigger{},
			}},
			"event",
		},
		{
			&workflows.Workflow{On: workflows.WorkflowTriggers{
				WorkflowCall: &workflows.WorkflowCall{},
			}},
			"workflow_call",
		},
		{
			&workflows.Workflow{On: workflows.WorkflowTriggers{
				Manual:       map[string]any{},
				WorkflowCall: &workflows.WorkflowCall{},
			}},
			"multiple",
		},
	}
	for _, test := range triggerCases {
		if got := workflowDefinitionTriggerLabel(test.workflow); got != test.want {
			t.Fatalf("trigger label = %q, want %q", got, test.want)
		}
	}

	fallbacks := []workflowDefinitionResponse{
		{Ref: "workflows/error.yml", Error: "private", Status: ""},
		{Ref: "workflows/pending.yml", Status: ""},
		{Ref: "workflows/kept.yml", Status: workflows.WorkflowValidationStatusValid},
	}
	applyWorkflowDefinitionCompatibility(fallbacks, nil)
	if fallbacks[0].Status != workflows.WorkflowValidationStatusInvalid ||
		fallbacks[1].Status != workflows.WorkflowValidationStatusPendingRevalidation ||
		fallbacks[2].Status != workflows.WorkflowValidationStatusValid {
		t.Fatalf("fallback statuses = %#v", fallbacks)
	}
	applyWorkflowDefinitionCompatibility(fallbacks, &workflows.WorkflowCompatibilitySummary{
		Workflows: []workflows.WorkflowValidationStamp{{
			WorkflowRef: "workflows/kept.yml",
			Status:      workflows.WorkflowValidationStatusNeedsReview,
		}},
	})
	if fallbacks[2].Status != workflows.WorkflowValidationStatusNeedsReview {
		t.Fatalf("compatibility status = %#v", fallbacks[2])
	}

	for _, id := range []string{
		"", "wr_", "not_wr_value", "wr_bad/value", "wr_" + string([]byte{0xff}),
		"wr_" + strings.Repeat("x", 1022),
	} {
		if validWorkflowRunResourceID(id) {
			t.Fatalf("validWorkflowRunResourceID(%q) = true", id)
		}
	}
	if !validWorkflowRunResourceID("wr_A-z_09") {
		t.Fatal("valid workflow run ID was rejected")
	}
	if got := workflowRunOrigin(workflowRunCollectionSummary{
		Origin: &workflows.RunOrigin{Kind: "untrusted"},
	}); got != "manual" {
		t.Fatalf("untrusted origin label = %q", got)
	}
}

func TestWorkflowCollectionRequestBoundsUTF8PositionsAndDirectParameters(t *testing.T) {
	workspace, _, mux := newWorkflowCollectionAPIHarness(t)
	writeWorkflowCollectionDefinition(t, workspace, "one.yml", `name: One
on:
  manual: {}
jobs: {}
`)
	for _, endpoint := range []string{"/api/workflows/definitions", "/api/workflows/runs"} {
		for _, test := range []struct {
			suffix string
			code   string
		}{
			{"?limit=0", "invalid_page_limit"},
			{"?limit=201", "invalid_page_limit"},
			{"?other=1", "invalid_collection_request"},
			{"?query=a&query=b", "invalid_collection_request"},
			{"?cursor=not-a-cursor", "invalid_cursor"},
		} {
			response := serveWorkflowCollectionRequest(
				t, mux, http.MethodGet, endpoint+test.suffix, "", nil,
			)
			requireWorkflowCollectionError(t, response, http.StatusBadRequest, test.code)
		}
		for _, limit := range []string{"1", "200"} {
			response := serveWorkflowCollectionRequest(
				t, mux, http.MethodGet, endpoint+"?limit="+limit, "", nil,
			)
			if response.Code != http.StatusOK {
				t.Fatalf("%s limit %s status/body = %d %s", endpoint, limit, response.Code, response.Body.String())
			}
		}
	}

	rawQuery := `name = "é" AND`
	utf8Error := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodGet,
		"/api/workflows/definitions?query="+url.QueryEscape(rawQuery),
		"",
		nil,
	)
	var errorBody struct {
		Code     string `json:"code"`
		Position int    `json:"position"`
	}
	decodeWorkflowCollectionJSON(t, utf8Error, &errorBody)
	if utf8Error.Code != http.StatusBadRequest || errorBody.Code != "invalid_query" ||
		errorBody.Position <= len([]rune(rawQuery))-1 || errorBody.Position > len([]byte(rawQuery)) {
		t.Fatalf("UTF-8 query error = %d %#v", utf8Error.Code, errorBody)
	}

	all := serveWorkflowCollectionRequest(t, mux, http.MethodGet, "/api/workflows/definitions", "", nil)
	var page workflowDefinitionsCollectionTestResponse
	decodeWorkflowCollectionJSON(t, all, &page)
	direct := serveWorkflowCollectionRequest(
		t,
		mux,
		http.MethodGet,
		"/api/workflows/definitions/"+page.Workflows[0].ID+"?query=x",
		"",
		nil,
	)
	requireWorkflowCollectionError(t, direct, http.StatusBadRequest, "invalid_collection_request")
}

func TestWorkflowDevelopmentReadAndStartConflictProjectSourceWorkflowID(t *testing.T) {
	workspace, _, mux := newWorkflowCollectionAPIHarness(t)
	writeWorkflowCollectionDefinition(t, workspace, "editable.yml", `name: Editable
on:
  manual: {}
jobs: {}
`)
	session, err := workflows.StartWorkflowDevelopment(
		context.Background(),
		workspace,
		workflows.RuntimeCompatibility{PicoclawVersion: "test"},
		workflows.WorkflowDevelopmentStartRequest{
			Reason: workflows.WorkflowDevelopmentReasonEdit,
			Ref:    "workflows/editable.yml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	wantID, err := workflows.WorkflowDefinitionID(session.SourceWorkflowRef)
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []*httptest.ResponseRecorder{
		serveWorkflowCollectionRequest(
			t, mux, http.MethodGet, "/api/workflows/development", "", nil,
		),
		serveWorkflowCollectionRequest(
			t,
			mux,
			http.MethodPost,
			"/api/workflows/development/start",
			`{"reason":"new","target_ref":"workflows/new.yml"}`,
			map[string]string{"Content-Type": "application/json"},
		),
	} {
		if response.Code != http.StatusOK && response.Code != http.StatusConflict {
			t.Fatalf("development response = %d %s", response.Code, response.Body.String())
		}
		var body struct {
			Session struct {
				ID               string `json:"id"`
				SourceWorkflowID string `json:"source_workflow_id"`
			} `json:"session"`
		}
		decodeWorkflowCollectionJSON(t, response, &body)
		if body.Session.ID != session.ID || body.Session.SourceWorkflowID != wantID {
			t.Fatalf("development identity = %#v, want %q", body.Session, wantID)
		}
	}
}

func TestWorkflowCollectionHandlersReturnBoundedUnavailableErrors(t *testing.T) {
	badConfigPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(badConfigPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	badHandler := NewHandler(badConfigPath)
	badMux := http.NewServeMux()
	badHandler.RegisterRoutes(badMux)
	definitionID, _ := workflows.WorkflowDefinitionID("workflows/a.yml")
	for _, test := range []struct {
		target string
		code   string
	}{
		{"/api/workflows/definitions", "workflow_definitions_unavailable"},
		{"/api/workflows/definitions/" + definitionID, "workflow_definitions_unavailable"},
		{"/api/workflows/runs", "workflow_runs_unavailable"},
		{"/api/workflows/runs/wr_missing", "workflow_runs_unavailable"},
	} {
		response := serveWorkflowCollectionRequest(
			t, badMux, http.MethodGet, test.target, "", nil,
		)
		requireWorkflowCollectionError(t, response, http.StatusInternalServerError, test.code)
	}

	workspace, handler, mux := newWorkflowCollectionAPIHarness(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := handler.loadWorkflowDefinitionResponses(canceled); err == nil {
		t.Fatal("canceled workflow definition load succeeded")
	}

	writeWorkflowCollectionDefinition(t, workspace, "one.yml", `name: One
on:
  manual: {}
jobs: {}
`)
	if _, err := workflows.LoadCompatibilitySummary(
		context.Background(), workspace, workflows.RuntimeCompatibility{},
	); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open(
		"sqlite",
		"file:"+filepath.ToSlash(filepath.Join(workspace, "state", "workflows.db")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DROP TABLE workflow_compatibility_runtime`); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	response := serveWorkflowCollectionRequest(
		t, mux, http.MethodGet, "/api/workflows/definitions", "", nil,
	)
	requireWorkflowCollectionError(t, response, http.StatusInternalServerError, "workflow_definitions_unavailable")

	originalProjection := loadWorkflowDefinitionResponseProjection
	t.Cleanup(func() { loadWorkflowDefinitionResponseProjection = originalProjection })
	loadWorkflowDefinitionResponseProjection = func(
		context.Context,
		string,
		[]workflows.Definition,
		...workflows.LocalOption,
	) ([]workflowDefinitionResponse, error) {
		return nil, context.Canceled
	}
	response = serveWorkflowCollectionRequest(
		t, mux, http.MethodGet, "/api/workflows/definitions", "", nil,
	)
	requireWorkflowCollectionError(t, response, http.StatusInternalServerError, "workflow_definitions_unavailable")
}

func newWorkflowCollectionAPIHarness(t *testing.T) (string, *Handler, *http.ServeMux) {
	t.Helper()
	workspace := t.TempDir()
	configPath := writeWorkflowAITestConfig(t, workspace)
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return workspace, handler, mux
}

func writeWorkflowCollectionDefinition(t *testing.T, workspace, name, contents string) {
	t.Helper()
	dir := filepath.Join(workspace, "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func serveWorkflowCollectionRequest(
	t *testing.T,
	mux *http.ServeMux,
	method string,
	target string,
	body string,
	headers map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func decodeWorkflowCollectionJSON(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, response.Body.String())
	}
}

func requireWorkflowCollectionError(
	t *testing.T,
	response *httptest.ResponseRecorder,
	status int,
	code string,
) {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	decodeWorkflowCollectionJSON(t, response, &body)
	if response.Code != status || body.Code != code || len(response.Body.Bytes()) > 1024 {
		t.Fatalf("error status/body = %d %s; want %d/%s", response.Code, response.Body.String(), status, code)
	}
}

func assertWorkflowCollectionSchemaField(
	t *testing.T,
	schema collectionquery.Schema,
	name collectionquery.Field,
	fieldType collectionquery.FieldType,
) {
	t.Helper()
	for _, field := range schema.Fields {
		if field.Name == name {
			if field.Type != fieldType {
				t.Fatalf("schema field %s type = %s, want %s", name, field.Type, fieldType)
			}
			return
		}
	}
	t.Fatalf("schema omitted field %s", name)
}

func assertWorkflowCollectionSuggestedValue(
	t *testing.T,
	schema collectionquery.Schema,
	name collectionquery.Field,
	value string,
) {
	t.Helper()
	for _, field := range schema.Fields {
		if field.Name != name {
			continue
		}
		for _, suggestion := range field.SuggestedValues {
			if suggestion == value {
				return
			}
		}
	}
	t.Fatalf("schema field %s omitted suggestion %q", name, value)
}
