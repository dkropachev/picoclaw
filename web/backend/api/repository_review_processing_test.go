package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewFindingsProcessingCollectionSchemaAndBinding(t *testing.T) {
	wantFields := []collectionquery.Field{
		"id", "campaign", "title", "path", "symbol", "severity", "model", "reviewer",
		"state", "disposition", "created", "updated",
	}
	gotFields := make([]collectionquery.Field, 0, len(repositoryReviewFindingsProcessingCollectionSchema.Fields))
	for _, field := range repositoryReviewFindingsProcessingCollectionSchema.Fields {
		gotFields = append(gotFields, field.Name)
	}
	if !slices.Equal(gotFields, wantFields) {
		t.Fatalf("processing fields=%#v want=%#v", gotFields, wantFields)
	}
	if got := repositoryReviewFindingsProcessingCollectionSchema.DefaultOrder; len(got) != 1 ||
		got[0].Field != "updated" || got[0].Direction != collectionquery.Descending {
		t.Fatalf("default processing order=%#v", got)
	}

	now := time.Date(2026, 8, 31, 17, 0, 0, 0, time.UTC)
	summary := repositoryReviewRawFindingSummary{
		ID: "rrw_binding", CampaignID: "rrc_binding", Title: "Lost update",
		Path: "pkg/cache.go", Symbol: "Cache.Save", Severity: "high",
		Model: "provider/review-model", Reviewer: "review-model",
		DeduplicationState: repoaudit.RawFindingDeduplicationFailed,
		Disposition:        repoaudit.RawFindingDispositionUndecided,
		CreatedAt:          now.Add(-time.Hour), UpdatedAt: now,
	}
	contextID := repositoryReviewCollectionCursorContext(
		"findings-processing", "rra_binding", "rrp_binding",
	)
	options := repositoryReviewFindingsProcessingPageOptions(contextID)
	id, err := options.ID(summary)
	if err != nil || !options.ValidateID(id) {
		t.Fatalf("processing cursor id=%q err=%v", id, err)
	}
	for _, field := range wantFields {
		if _, ok := options.Resolve(summary, field, now); !ok {
			t.Fatalf("processing field %q was not bound", field)
		}
	}
	if _, ok := options.Resolve(summary, "unknown", now); ok {
		t.Fatal("unknown processing field was bound")
	}

	for _, test := range []struct {
		target string
		legacy bool
	}{
		{target: "/", legacy: false},
		{target: "/?limit=10", legacy: false},
		{target: "/?offset=1", legacy: true},
		{target: "/?state=failed", legacy: true},
		{target: "/?query=", legacy: false},
		{target: "/?cursor=opaque", legacy: false},
		{target: "/?query=ALL&offset=1", legacy: true},
	} {
		request := httptest.NewRequest(http.MethodGet, test.target, nil)
		got, err := repositoryReviewUsesLegacyFindingsProcessingPage(request)
		if err != nil || got != test.legacy {
			t.Fatalf("legacy mode for %q=%v want=%v", test.target, got, test.legacy)
		}
	}
	malformed := httptest.NewRequest(http.MethodGet, "/?query=%zz", nil)
	if _, err := repositoryReviewUsesLegacyFindingsProcessingPage(malformed); err == nil {
		t.Fatal("malformed processing query mode was accepted")
	}
	for _, request := range []*http.Request{nil, new(http.Request)} {
		if legacy, err := repositoryReviewUsesLegacyFindingsProcessingPage(request); err == nil || legacy {
			t.Fatalf("invalid processing request selected legacy=%v err=%v", legacy, err)
		}
	}
}

func TestRepositoryReviewFindingsProcessingStandardAndLegacyRoutes(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	const currentCampaign = "rrc_processing_current"
	state = seedRepositoryReviewDeduplicationAPIState(t, workspace, state, currentCampaign)
	state = appendRepositoryReviewProcessingTestSource(
		t, workspace, state, "rrw_processing_other", "rrc_processing_other", "run-other", false,
	)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	base := "/api/repository-reviews/automations/" + automation.ID + "/findings-processing"
	other, found := repositoryReviewRawFindingByID(state, "rrw_processing_other")
	if !found {
		t.Fatal("cross-campaign source was not seeded")
	}

	const allFieldsFormat = `id = %q AND campaign = %q AND title = %q AND path = %q` +
		` AND symbol = %q AND severity = %s AND model = %q AND reviewer = %q` +
		` AND state = failed AND disposition = undecided AND created <= %q` +
		` AND updated >= %q ORDER BY updated DESC`
	allFieldsQuery := fmt.Sprintf(
		allFieldsFormat,
		other.ID,
		other.CampaignID,
		other.Title,
		other.File.Path,
		other.Symbol,
		other.Severity,
		other.Model,
		other.Reviewer,
		other.CreatedAt.Add(time.Minute).Format(time.RFC3339Nano),
		other.UpdatedAt.Add(-time.Minute).Format(time.RFC3339Nano),
	)
	filtered := httptest.NewRecorder()
	mux.ServeHTTP(filtered, httptest.NewRequest(
		http.MethodGet, base+"?query="+url.QueryEscape(allFieldsQuery), nil,
	))
	var filteredPage struct {
		RawFindings    []repositoryReviewRawFindingSummary `json:"raw_findings"`
		Total          int                                 `json:"total"`
		CanonicalQuery string                              `json:"canonical_query"`
		QuerySchema    collectionquery.Schema              `json:"query_schema"`
	}
	if err := json.Unmarshal(filtered.Body.Bytes(), &filteredPage); err != nil {
		t.Fatal(err)
	}
	if filtered.Code != http.StatusOK || filteredPage.Total != 1 ||
		len(filteredPage.RawFindings) != 1 || filteredPage.RawFindings[0].ID != other.ID ||
		len(filteredPage.QuerySchema.Fields) != len(repositoryReviewFindingsProcessingCollectionSchema.Fields) ||
		filteredPage.CanonicalQuery == "" {
		t.Fatalf("standard processing status=%d page=%#v body=%s", filtered.Code, filteredPage, filtered.Body.String())
	}

	query := url.QueryEscape("ALL ORDER BY updated DESC")
	first := httptest.NewRecorder()
	mux.ServeHTTP(first, httptest.NewRequest(
		http.MethodGet, base+"?query="+query+"&limit=1", nil,
	))
	var firstPage struct {
		RawFindings []repositoryReviewRawFindingSummary `json:"raw_findings"`
		Total       int                                 `json:"total"`
		NextCursor  string                              `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if first.Code != http.StatusOK || firstPage.Total != len(state.RawFindings) ||
		len(firstPage.RawFindings) != 1 || firstPage.RawFindings[0].ID != other.ID ||
		firstPage.NextCursor == "" {
		t.Fatalf("first standard page status=%d page=%#v body=%s", first.Code, firstPage, first.Body.String())
	}
	second := httptest.NewRecorder()
	mux.ServeHTTP(second, httptest.NewRequest(
		http.MethodGet,
		base+"?query="+query+"&limit=1&cursor="+url.QueryEscape(firstPage.NextCursor),
		nil,
	))
	var secondPage struct {
		RawFindings []repositoryReviewRawFindingSummary `json:"raw_findings"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	if second.Code != http.StatusOK || len(secondPage.RawFindings) != 1 ||
		secondPage.RawFindings[0].ID == other.ID {
		t.Fatalf("second standard page status=%d page=%#v body=%s", second.Code, secondPage, second.Body.String())
	}
	for _, target := range []string{base, base + "?limit=1"} {
		standard := httptest.NewRecorder()
		mux.ServeHTTP(standard, httptest.NewRequest(http.MethodGet, target, nil))
		var page struct {
			RawFindings    []repositoryReviewRawFindingSummary `json:"raw_findings"`
			Total          int                                 `json:"total"`
			CanonicalQuery string                              `json:"canonical_query"`
		}
		if err := json.Unmarshal(standard.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if standard.Code != http.StatusOK || page.Total != len(state.RawFindings) ||
			len(page.RawFindings) == 0 || page.RawFindings[0].ID != other.ID ||
			page.CanonicalQuery != "ALL ORDER BY updated DESC" {
			t.Fatalf(
				"default standard processing %q status=%d page=%#v body=%s",
				target,
				standard.Code,
				page,
				standard.Body.String(),
			)
		}
	}
	malformed := httptest.NewRecorder()
	mux.ServeHTTP(malformed, httptest.NewRequest(http.MethodGet, base+"?query=%zz", nil))
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed processing query status=%d body=%s", malformed.Code, malformed.Body.String())
	}
	unsupported := httptest.NewRecorder()
	mux.ServeHTTP(unsupported, httptest.NewRequest(http.MethodGet, base+"?unsupported=true", nil))
	if unsupported.Code != http.StatusBadRequest {
		t.Fatalf("unsupported processing parameter status=%d body=%s", unsupported.Code, unsupported.Body.String())
	}

	for _, target := range []string{
		base + "?query=ALL&offset=1",
		base + "?cursor=" + url.QueryEscape(firstPage.NextCursor) + "&state=failed",
		base + "?query=" + url.QueryEscape("state = pending ORDER BY updated DESC") +
			"&cursor=" + url.QueryEscape(firstPage.NextCursor),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf(
				"mixed/mismatched processing query %q status=%d body=%s",
				target,
				response.Code,
				response.Body.String(),
			)
		}
	}

	for _, target := range []string{
		base + "?offset=0&limit=1",
		base + "?state=failed&offset=0&limit=10",
	} {
		legacy := httptest.NewRecorder()
		mux.ServeHTTP(legacy, httptest.NewRequest(http.MethodGet, target, nil))
		var legacyPage struct {
			RawFindings []repositoryReviewRawFindingSummary `json:"raw_findings"`
			Offset      int                                 `json:"offset"`
		}
		if err := json.Unmarshal(legacy.Body.Bytes(), &legacyPage); err != nil {
			t.Fatal(err)
		}
		if legacy.Code != http.StatusOK {
			t.Fatalf("legacy processing %q status=%d body=%s", target, legacy.Code, legacy.Body.String())
		}
		for _, raw := range legacyPage.RawFindings {
			if raw.ID == other.ID {
				t.Fatalf("legacy current-campaign page leaked canonical source: %s", legacy.Body.String())
			}
		}
	}
	campaignPage := httptest.NewRecorder()
	mux.ServeHTTP(campaignPage, httptest.NewRequest(
		http.MethodGet,
		strings.TrimSuffix(base, "/findings-processing")+"/campaigns/"+currentCampaign+
			"/findings-processing?state=failed&offset=0&limit=10",
		nil,
	))
	if campaignPage.Code != http.StatusOK ||
		!strings.Contains(campaignPage.Body.String(), `"raw_findings"`) ||
		strings.Contains(campaignPage.Body.String(), other.ID) {
		t.Fatalf("campaign compatibility status=%d body=%s", campaignPage.Code, campaignPage.Body.String())
	}

	detail := httptest.NewRecorder()
	mux.ServeHTTP(detail, httptest.NewRequest(
		http.MethodGet, base+"/sources/"+other.ID, nil,
	))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), other.Evidence) ||
		!strings.Contains(detail.Body.String(), `"historical_consolidation"`) {
		t.Fatalf("canonical processing detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	wrongCampaignDetail := httptest.NewRecorder()
	mux.ServeHTTP(wrongCampaignDetail, httptest.NewRequest(
		http.MethodGet,
		strings.TrimSuffix(base, "/findings-processing")+"/campaigns/"+currentCampaign+
			"/findings-processing/sources/"+other.ID,
		nil,
	))
	if wrongCampaignDetail.Code != http.StatusNotFound {
		t.Fatalf(
			"campaign detail leaked canonical source status=%d body=%s",
			wrongCampaignDetail.Code,
			wrongCampaignDetail.Body.String(),
		)
	}
}

func TestRepositoryReviewFindingsProcessingIndividualRetryUsesCanonicalLedger(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = seedRepositoryReviewDeduplicationAPIState(t, workspace, state, "rrc_retry_current")
	state = appendRepositoryReviewProcessingTestSource(
		t, workspace, state, "rrw_retry_other", "rrc_retry_other", "run-other", false,
	)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	handler.repositoryReviewControllerInstance().cancel()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+
			"/findings-processing/sources/rrw_retry_other/retry",
		strings.NewReader(`{}`),
	)
	setRepositoryReviewMutationHeaders(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var payload struct {
		Source repoaudit.RawReviewFinding    `json:"source"`
		Health repositoryReviewFindingHealth `json:"health"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusAccepted ||
		payload.Source.State != repoaudit.RawFindingDeduplicationPending ||
		payload.Health.FindingsProcessing.Total != len(state.RawFindings) {
		t.Fatalf("canonical retry status=%d payload=%#v body=%s", response.Code, payload, response.Body.String())
	}
}

func TestRepositoryReviewFindingsProcessingCompletedDetailLinksCanonicalFindings(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	state = seedRepositoryReviewDeduplicationAPIState(t, workspace, state, "rrc_detail_links")
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	completed := state.RawFindings[0]
	if completed.State != repoaudit.RawFindingDeduplicationCompleted ||
		completed.DeduplicatedFindingID == "" {
		t.Fatalf("completed source fixture=%#v", completed)
	}

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+
			"/findings-processing/sources/"+completed.ID,
		nil,
	))
	var payload struct {
		Source            repoaudit.RawReviewFinding  `json:"source"`
		Finding           json.RawMessage             `json:"finding"`
		RepositoryFinding repoaudit.RepositoryFinding `json:"repository_finding"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || payload.Source.ID != completed.ID ||
		len(payload.Finding) == 0 || payload.RepositoryFinding.ID == "" ||
		payload.RepositoryFinding.ID != state.DeduplicatedFindings[0].RepositoryFindingID {
		t.Fatalf("completed detail status=%d payload=%#v body=%s", response.Code, payload, response.Body.String())
	}
}

func TestRepositoryReviewProcessingSourceDetailSupportsCompatibilityFindingShapes(t *testing.T) {
	canonical := repoaudit.DeduplicatedReviewFinding{
		ID: "rdf_canonical_only", RepositoryFindingID: "rpf_canonical",
	}
	canonicalDetail := repositoryReviewProcessingSourceDetail(
		repositoryReviewAutomationLedger{
			State: repoaudit.RepositoryState{
				DeduplicatedFindings: []repoaudit.DeduplicatedReviewFinding{canonical},
			},
		},
		repoaudit.RawReviewFinding{DeduplicatedFindingID: canonical.ID},
	)
	canonicalFinding, ok := canonicalDetail["finding"].(repoaudit.DeduplicatedReviewFinding)
	if !ok || canonicalFinding.ID != canonical.ID {
		t.Fatalf("canonical-only detail finding=%#v", canonicalDetail["finding"])
	}

	compatibility := repoaudit.Finding{
		ID: "rdf_compatibility_only", RepositoryFindingID: "rpf_compatibility",
	}
	compatibilityDetail := repositoryReviewProcessingSourceDetail(
		repositoryReviewAutomationLedger{
			State: repoaudit.RepositoryState{Findings: []repoaudit.Finding{compatibility}},
		},
		repoaudit.RawReviewFinding{DeduplicatedFindingID: compatibility.ID},
	)
	compatibilityFinding, ok := compatibilityDetail["finding"].(repositoryReviewRunFindingProjection)
	if !ok || compatibilityFinding.ID != compatibility.ID {
		t.Fatalf("compatibility detail finding=%#v", compatibilityDetail["finding"])
	}
}

func TestRepositoryReviewFindingsProcessingRetryRouteErrors(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = seedRepositoryReviewDeduplicationAPIState(t, workspace, state, "rrc_retry_errors")
	state = appendRepositoryReviewProcessingTestSource(
		t, workspace, state, "rrw_retry_historical", "rrc_retry_historical", "run-historical", true,
	)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	handler.repositoryReviewControllerInstance().cancel()
	base := "/api/repository-reviews/automations/" + automation.ID +
		"/findings-processing/sources/"
	tests := []struct {
		name    string
		target  string
		body    string
		headers bool
		want    int
	}{
		{
			name: "query rejected", target: base + "rrw_api_failed/retry?query=1",
			body: `{}`, headers: true, want: http.StatusBadRequest,
		},
		{
			name: "cross site rejected", target: base + "rrw_api_failed/retry",
			body: `{}`, want: http.StatusBadRequest,
		},
		{
			name: "malformed body", target: base + "rrw_api_failed/retry",
			body: `{`, headers: true, want: http.StatusBadRequest,
		},
		{
			name:   "missing automation",
			target: "/api/repository-reviews/automations/rra_missing/findings-processing/sources/rrw_api_failed/retry",
			body:   `{}`, headers: true, want: http.StatusNotFound,
		},
		{
			name: "missing source", target: base + "rrw_missing/retry",
			body: `{}`, headers: true, want: http.StatusNotFound,
		},
		{
			name: "not failed", target: base + "rrw_api_pending/retry",
			body: `{}`, headers: true, want: http.StatusConflict,
		},
		{
			name: "historical source", target: base + "rrw_retry_historical/retry",
			body: `{}`, headers: true, want: http.StatusConflict,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(test.body))
			if test.headers {
				setRepositoryReviewMutationHeaders(request)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestRepositoryReviewFindingsProcessingBulkRetryValidationAndPartialResults(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = seedRepositoryReviewDeduplicationAPIState(t, workspace, state, "rrc_bulk_current")
	state = appendRepositoryReviewProcessingTestSource(
		t, workspace, state, "rrw_bulk_second", "rrc_bulk_other", "run-other", false,
	)
	state = appendRepositoryReviewProcessingTestSource(
		t, workspace, state, "rrw_bulk_historical", "rrc_bulk_historical", "run-historical", true,
	)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	handler.repositoryReviewControllerInstance().cancel()
	base := "/api/repository-reviews/automations/" + automation.ID + "/findings-processing/retry"
	store := repoaudit.NewStore(workspace)
	before, found, err := store.Get(state.Repository)
	if err != nil || !found {
		t.Fatalf("initial state found=%v err=%v", found, err)
	}
	for _, test := range []struct {
		target  string
		headers bool
		want    int
	}{
		{target: base + "?query=1", headers: true, want: http.StatusBadRequest},
		{target: base, want: http.StatusBadRequest},
		{
			target:  "/api/repository-reviews/automations/rra_missing/findings-processing/retry",
			headers: true,
			want:    http.StatusNotFound,
		},
	} {
		request := httptest.NewRequest(
			http.MethodPost, test.target, strings.NewReader(`{"source_ids":["rrw_api_failed"]}`),
		)
		if test.headers {
			setRepositoryReviewMutationHeaders(request)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf(
				"bulk preflight %q status=%d want=%d body=%s",
				test.target,
				response.Code,
				test.want,
				response.Body.String(),
			)
		}
	}

	invalidBodies := []string{
		`{"source_ids":[]}`,
		`{"source_ids":["rrw_api_failed"," rrw_api_failed "]}`,
		`{"source_ids":[""]}`,
		`{"source_ids":[`,
		`{"source_ids":["rrw_api_failed"],"extra":true}`,
	}
	for _, body := range invalidBodies {
		request := httptest.NewRequest(http.MethodPost, base, strings.NewReader(body))
		setRepositoryReviewMutationHeaders(request)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid bulk body %q status=%d body=%s", body, response.Code, response.Body.String())
		}
	}
	invalidBeforeLookup := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/automations/rra_missing/findings-processing/retry",
		map[string]any{"source_ids": []string{"rrw_duplicate", " rrw_duplicate "}},
	)
	if invalidBeforeLookup.Code != http.StatusBadRequest {
		t.Fatalf(
			"invalid selection reached automation lookup status=%d body=%s",
			invalidBeforeLookup.Code,
			invalidBeforeLookup.Body.String(),
		)
	}
	tooMany := make([]string, 201)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("rrw_too_many_%03d", index)
	}
	tooManyResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, base, map[string]any{"source_ids": tooMany},
	)
	if tooManyResponse.Code != http.StatusBadRequest {
		t.Fatalf("too many bulk retry status=%d body=%s", tooManyResponse.Code, tooManyResponse.Body.String())
	}
	afterInvalid, found, err := store.Get(state.Repository)
	if err != nil || !found || afterInvalid.Version != before.Version {
		t.Fatalf(
			"invalid selections mutated state before=%d after=%d found=%v err=%v",
			before.Version,
			afterInvalid.Version,
			found,
			err,
		)
	}
	mixed := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		base,
		map[string]any{"source_ids": []string{
			"rrw_api_failed", "rrw_api_pending", "rrw_missing", "rrw_bulk_historical", "rrw_bulk_second",
		}},
	)
	var mixedPayload struct {
		RetriedIDs         []string                                 `json:"retried_ids"`
		Failures           []repoaudit.DeduplicationRetryFailure    `json:"failures"`
		FindingsProcessing repositoryReviewFindingsProcessingHealth `json:"findings_processing"`
		Health             repositoryReviewFindingHealth            `json:"health"`
	}
	if err := json.Unmarshal(mixed.Body.Bytes(), &mixedPayload); err != nil {
		t.Fatal(err)
	}
	if mixed.Code != http.StatusAccepted ||
		!slices.Equal(mixedPayload.RetriedIDs, []string{"rrw_api_failed", "rrw_bulk_second"}) ||
		len(mixedPayload.Failures) != 3 ||
		mixedPayload.Failures[0].SourceID != "rrw_api_pending" ||
		mixedPayload.Failures[0].Code != "not_retryable" ||
		mixedPayload.Failures[1].SourceID != "rrw_missing" ||
		mixedPayload.Failures[1].Code != "not_found" ||
		mixedPayload.Failures[2].SourceID != "rrw_bulk_historical" ||
		mixedPayload.Failures[2].Code != "historical_replay_required" ||
		mixedPayload.Health.FindingsProcessing.Total != len(state.RawFindings) ||
		mixedPayload.FindingsProcessing.Total != len(state.RawFindings) {
		t.Fatalf("mixed bulk payload=%#v status=%d body=%s", mixedPayload, mixed.Code, mixed.Body.String())
	}
	for _, failure := range mixedPayload.Failures {
		if strings.Contains(strings.ToLower(failure.Message), "provider") || failure.Message == "" {
			t.Fatalf("unsafe bulk failure=%#v", failure)
		}
	}

	allInvalid := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		base,
		map[string]any{"source_ids": []string{
			"rrw_api_completed", "rrw_missing_again", "rrw_bulk_historical",
		}},
	)
	var allInvalidPayload struct {
		RetriedIDs []string                              `json:"retried_ids"`
		Failures   []repoaudit.DeduplicationRetryFailure `json:"failures"`
	}
	if err := json.Unmarshal(allInvalid.Body.Bytes(), &allInvalidPayload); err != nil {
		t.Fatal(err)
	}
	if allInvalid.Code != http.StatusAccepted || allInvalidPayload.RetriedIDs == nil ||
		len(allInvalidPayload.RetriedIDs) != 0 || len(allInvalidPayload.Failures) != 3 {
		t.Fatalf(
			"all-invalid bulk payload=%#v status=%d body=%s",
			allInvalidPayload,
			allInvalid.Code,
			allInvalid.Body.String(),
		)
	}
}

func TestRepositoryReviewFindingsProcessingMissingLedgerAndLegacyErrors(t *testing.T) {
	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.ID = "rra_processing_without_ledger"
	automation.Repository = "owner/processing-without-ledger"
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}

	bulk := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/findings-processing/retry",
		map[string]any{"source_ids": []string{"rrw_missing"}},
	)
	if bulk.Code != http.StatusBadRequest {
		t.Fatalf("missing-ledger bulk retry status=%d body=%s", bulk.Code, bulk.Body.String())
	}

	for _, target := range []string{
		"/api/repository-reviews/automations/rra_missing/findings-processing?offset=0",
		"/api/repository-reviews/automations/rra_missing/campaigns/rrc_missing/" +
			"findings-processing/sources/rrw_missing",
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("legacy GET %s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
}

func appendRepositoryReviewProcessingTestSource(
	t *testing.T,
	workspace string,
	state repoaudit.RepositoryState,
	id string,
	campaignID string,
	runID string,
	historical bool,
) repoaudit.RepositoryState {
	t.Helper()
	var templateRaw repoaudit.RawReviewFinding
	var templateJob repoaudit.DeduplicationJob
	for _, raw := range state.RawFindings {
		if raw.State == repoaudit.RawFindingDeduplicationFailed {
			templateRaw = raw
			break
		}
	}
	for _, job := range state.DeduplicationJobs {
		if job.RawFindingID == templateRaw.ID {
			templateJob = job
			break
		}
	}
	if templateRaw.ID == "" || templateJob.ID == "" {
		t.Fatal("failed processing source template is missing")
	}
	ordinal := state.NextDeduplicationOrdinal
	if ordinal == 0 {
		ordinal = 1
	}
	at := time.Now().UTC().Add(-100 * time.Millisecond)
	failure := *templateRaw.Failure
	failure.At = at
	raw := templateRaw
	raw.ID = id
	raw.Version = 1
	raw.CampaignID = campaignID
	raw.AdmissionBucket = "rdb_" + id
	raw.InsertionOrdinal = ordinal
	raw.LegacyFindingID = ""
	raw.RunID = runID
	raw.AssignmentID = "assignment-" + id
	raw.DeduplicatedFindingID = ""
	raw.Symbol = "Processing.Save"
	raw.Failure = &failure
	raw.History = []repoaudit.RawFindingHistoryEntry{{
		State: raw.State, Disposition: raw.Disposition, Attempt: templateJob.Attempts,
		Failure: &failure, At: at,
	}}
	raw.CreatedAt = at
	raw.UpdatedAt = at
	if historical {
		raw.LegacyFindingID = state.Findings[0].ID
		raw.AssignmentID = "historical-replay"
		state.HistoricalDeduplication = repoaudit.HistoricalDeduplicationReplay{
			Required: true, Status: repoaudit.HistoricalDeduplicationFailed,
			Attempts: 1, Error: "Historical consolidation failed.", UpdatedAt: at,
		}
	}
	raw.DiagnosisDigest = repoaudit.RawReviewFindingDiagnosisDigest(raw)
	job := templateJob
	job.ID = "rdj_" + id
	job.RawFindingID = id
	job.AdmissionBucket = raw.AdmissionBucket
	job.InsertionOrdinal = ordinal
	job.LeaseID = ""
	job.LeaseExpiresAt = time.Time{}
	job.CandidateUniverseDigest = ""
	job.CandidateVersions = nil
	job.ShortlistedScores = nil
	job.Decision = repoaudit.DeduplicationJudgment{}
	job.Failure = &failure
	job.History = []repoaudit.DeduplicationJobHistoryEntry{{
		State: repoaudit.DeduplicationJobFailed, Attempt: job.Attempts,
		Failure: &failure, At: at,
	}}
	job.CreatedAt = at
	job.UpdatedAt = at
	state.RawFindings = append(state.RawFindings, raw)
	state.DeduplicationJobs = append(state.DeduplicationJobs, job)
	state.NextDeduplicationOrdinal = ordinal + 1
	state.FindingsProcessing = repositoryReviewFindingsProcessingCounters(state.RawFindings)
	state.Version++
	state.UpdatedAt = at
	writeRepositoryReviewProcessingTestState(t, workspace, state)
	loaded, found, err := repoaudit.NewStore(workspace).Get(state.Repository)
	if err != nil || !found {
		t.Fatalf("processing state found=%v err=%v", found, err)
	}
	return loaded
}

func writeRepositoryReviewProcessingTestState(
	t *testing.T,
	workspace string,
	state repoaudit.RepositoryState,
) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(workspace, "repository_reviews", "repo_*.json"))
	if err != nil {
		t.Fatal(err)
	}
	statePath := ""
	for _, path := range paths {
		if !strings.HasSuffix(path, ".summary.json") {
			statePath = path
			break
		}
	}
	if statePath == "" {
		t.Fatal("repository review state path is missing")
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
