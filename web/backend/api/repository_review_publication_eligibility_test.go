package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewIssueDetailAndCollectionProjectPublicationEligibility(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	store := repoaudit.NewStore(workspace)
	state, draft, reserved, err := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
		Repository: state.Repository, FindingID: state.Findings[0].ID,
		GenerationID:         "rrig_publication_projection",
		ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
		InstructionsMode:     repoaudit.IssueDraftInstructionsDefault,
		GeneratorModel:       "cheap", GeneratorAccount: "api",
	})
	if err != nil || !reserved || draft.State != repoaudit.IssueDraftGenerating {
		t.Fatalf("reserve state=%#v draft=%#v reserved=%v err=%v", state, draft, reserved, err)
	}
	want := repoaudit.EvaluateIssuePublication(state, draft)
	if want.CanPublish || len(want.PublishBlockers) != 1 ||
		want.PublishBlockers[0].Code != repoaudit.IssuePublicationStateNotPublishable {
		t.Fatalf("fixture eligibility = %#v", want)
	}

	detailResponse := httptest.NewRecorder()
	mux.ServeHTTP(detailResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/"+draft.ID,
		nil,
	))
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailResponse.Code, detailResponse.Body.String())
	}
	var detail struct {
		Issue        repoaudit.IssueDraft         `json:"issue"`
		Capabilities repositoryReviewCapabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(detailResponse.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Issue.ID != draft.ID || detail.Capabilities.CanPublish != want.CanPublish ||
		!reflect.DeepEqual(detail.Capabilities.PublishBlockers, want.PublishBlockers) {
		t.Fatalf("detail=%#v want=%#v", detail, want)
	}

	collectionResponse := httptest.NewRecorder()
	mux.ServeHTTP(collectionResponse, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+"/issues?query="+
			url.QueryEscape("ALL ORDER BY updated DESC"),
		nil,
	))
	if collectionResponse.Code != http.StatusOK {
		t.Fatalf("collection status=%d body=%s", collectionResponse.Code, collectionResponse.Body.String())
	}
	var collection struct {
		Issues []repositoryReviewIssueCollectionSummary `json:"issues"`
	}
	if err := json.Unmarshal(collectionResponse.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	if len(collection.Issues) != 1 || collection.Issues[0].ID != draft.ID ||
		collection.Issues[0].Publishable != want.CanPublish ||
		!reflect.DeepEqual(collection.Issues[0].PublishBlockers, want.PublishBlockers) {
		t.Fatalf("collection=%#v want=%#v", collection, want)
	}
	if !reflect.DeepEqual(
		detail.Capabilities.PublishBlockers,
		collection.Issues[0].PublishBlockers,
	) {
		t.Fatalf(
			"detail blockers=%#v collection blockers=%#v",
			detail.Capabilities.PublishBlockers,
			collection.Issues[0].PublishBlockers,
		)
	}

	proxyCalls := 0
	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		proxyCalls++
		return eventUpstreamResponse(http.StatusInternalServerError, `{}`), nil
	})
	publishResponse := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/"+draft.ID+"/publish",
		map[string]any{"expected_version": draft.Version, "confirmed": true},
	)
	if publishResponse.Code != http.StatusConflict || proxyCalls != 0 {
		t.Fatalf(
			"blocked publication status=%d proxy_calls=%d body=%s",
			publishResponse.Code,
			proxyCalls,
			publishResponse.Body.String(),
		)
	}
	var publicationError struct {
		Code            repoaudit.IssuePublicationBlockerCode `json:"code"`
		PublishBlockers []repoaudit.IssuePublicationBlocker   `json:"publish_blockers"`
	}
	if err := json.Unmarshal(publishResponse.Body.Bytes(), &publicationError); err != nil {
		t.Fatal(err)
	}
	if publicationError.Code != repoaudit.IssuePublicationStateNotPublishable ||
		!reflect.DeepEqual(publicationError.PublishBlockers, want.PublishBlockers) {
		t.Fatalf("publication error=%#v want=%#v", publicationError, want)
	}
}

func TestRepositoryReviewPublicationEligibilityResponseBoundaries(t *testing.T) {
	emptyResult := repositoryReviewPublicationEligibilityResult(
		"rid_empty",
		repoaudit.IssuePublicationEligibility{},
	)
	if emptyResult["code"] != "publication_not_allowed" ||
		emptyResult["draft_id"] != "rid_empty" {
		t.Fatalf("empty result=%#v", emptyResult)
	}

	nonGitHub := repoaudit.IssuePublicationEligibility{
		PublishBlockers: []repoaudit.IssuePublicationBlocker{{
			Code: repoaudit.IssuePublicationRepositoryNotGitHub, Count: 1,
			Message: "This repository is not a canonical GitHub repository.",
		}},
	}
	response := httptest.NewRecorder()
	writeRepositoryReviewPublicationEligibilityAPIError(response, nonGitHub)
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), `"code":"repository_not_github"`) {
		t.Fatalf("non-GitHub response=%d %s", response.Code, response.Body.String())
	}
}
