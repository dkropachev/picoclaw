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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewAutomationRoutesRegisterRunFindingStatus(t *testing.T) {
	handler := NewHandler(filepath.Join(t.TempDir(), "config.json"))
	mux := http.NewServeMux()
	handler.registerRepositoryReviewAutomationRoutes(mux)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/repository-reviews/automations/rra_test/findings/status",
		nil,
	)
	_, pattern := mux.Handler(request)
	if pattern != "POST /api/repository-reviews/automations/{automation_id}/findings/status" {
		t.Fatalf("run finding status route pattern=%q", pattern)
	}
}

func TestRepositoryReviewRunFindingStatusProjection(t *testing.T) {
	for _, test := range []struct {
		name    string
		state   repoaudit.RepositoryState
		finding repoaudit.Finding
		want    repositoryReviewRunFindingStatus
	}{
		{
			name: "pending",
			state: repoaudit.RepositoryState{MappingJobs: []repoaudit.RepositoryMappingJob{{
				ReviewFindingID: "rdf_pending", State: repoaudit.RepositoryMappingPending,
				Attempts: 2, Error: "private retry detail",
			}}},
			finding: repoaudit.Finding{ID: "rdf_pending"}, want: repositoryReviewRunFindingPending,
		},
		{
			name: "processing",
			state: repoaudit.RepositoryState{MappingJobs: []repoaudit.RepositoryMappingJob{{
				ReviewFindingID: "rdf_processing", State: repoaudit.RepositoryMappingRunning,
			}}},
			finding: repoaudit.Finding{ID: "rdf_processing"}, want: repositoryReviewRunFindingProcessing,
		},
		{
			name: "failed",
			state: repoaudit.RepositoryState{MappingJobs: []repoaudit.RepositoryMappingJob{{
				ReviewFindingID: "rdf_failed", State: repoaudit.RepositoryMappingPending,
				Attempts: repoaudit.RepositoryRunFindingStatusAttemptLimit,
				Error:    "private failure detail",
			}}},
			finding: repoaudit.Finding{ID: "rdf_failed"}, want: repositoryReviewRunFindingFailed,
		},
		{
			name: "associated new",
			state: repoaudit.RepositoryState{RepositoryFindings: []repoaudit.RepositoryFinding{{
				ID: "rrf_new", MatchState: repoaudit.RepositoryMatchKnown,
				ReviewFindingIDs: []string{"rdf_new", "rdf_later"},
			}}},
			finding: repoaudit.Finding{ID: "rdf_new", RepositoryFindingID: "rrf_new"},
			want:    repositoryReviewRunFindingAssociatedNew,
		},
		{
			name: "associated existing",
			state: repoaudit.RepositoryState{RepositoryFindings: []repoaudit.RepositoryFinding{{
				ID: "rrf_known", MatchState: repoaudit.RepositoryMatchKnown,
				ReviewFindingIDs: []string{"rdf_original", "rdf_known"},
			}}},
			finding: repoaudit.Finding{ID: "rdf_known", RepositoryFindingID: "rrf_known"},
			want:    repositoryReviewRunFindingAssociatedExisting,
		},
		{
			name: "needs review",
			state: repoaudit.RepositoryState{RepositoryFindings: []repoaudit.RepositoryFinding{{
				ID: "rrf_provisional", MatchState: repoaudit.RepositoryMatchProvisional,
			}}},
			finding: repoaudit.Finding{ID: "rdf_provisional", RepositoryFindingID: "rrf_provisional"},
			want:    repositoryReviewRunFindingNeedsReview,
		},
		{
			name:    "known association without aggregate projection",
			finding: repoaudit.Finding{ID: "rdf_known", RepositoryFindingID: "rrf_missing", RepositoryMatchState: repoaudit.RepositoryMatchKnown},
			want:    repositoryReviewRunFindingAssociatedExisting,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := repositoryReviewRunFindingStatusFor(test.state, test.finding); got != test.want {
				t.Fatalf("status=%q want=%q", got, test.want)
			}
		})
	}
}

func TestRepositoryReviewRunFindingStatusRetryRoute(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	state := seedRepositoryReviewAPIState(t, workspace)
	store := repoaudit.NewStore(workspace)
	for attempt := 0; attempt < repoaudit.RepositoryRunFindingStatusAttemptLimit; attempt++ {
		if _, err := store.ProcessPendingMappingJobs(
			t.Context(),
			state.Repository,
			repoaudit.RepositoryMappingProcessOptions{
				DefaultBranchVerified: func(context.Context, repoaudit.Finding) (bool, error) {
					return false, errors.New("private verifier failure")
				},
			},
		); err == nil {
			t.Fatalf("attempt %d succeeded", attempt+1)
		}
	}
	state, found, err := store.Get(state.Repository)
	if err != nil || !found {
		t.Fatalf("failed state found=%v err=%v", found, err)
	}
	findingID := state.Findings[0].ID
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	base := "/api/repository-reviews/automations/" + automation.ID
	page := httptest.NewRecorder()
	mux.ServeHTTP(page, httptest.NewRequest(
		http.MethodGet, base+"/findings?query=ALL", nil,
	))
	if page.Code != http.StatusOK ||
		!strings.Contains(page.Body.String(), `"run_finding_status":"failed"`) ||
		strings.Contains(page.Body.String(), "private verifier failure") ||
		strings.Contains(strings.ToLower(page.Body.String()), "mapping") {
		t.Fatalf("failed projection=%d %s", page.Code, page.Body.String())
	}

	retry := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		base+"/findings/status",
		map[string]any{"finding_ids": []string{findingID}},
	)
	if retry.Code != http.StatusAccepted ||
		!strings.Contains(retry.Body.String(), `"run_finding_status":"pending"`) ||
		!strings.Contains(retry.Body.String(), `"id":"`+findingID+`"`) {
		t.Fatalf("retry=%d %s", retry.Code, retry.Body.String())
	}
	reset, _, resetErr := store.Get(state.Repository)
	if resetErr != nil {
		t.Fatal(resetErr)
	}
	job := reset.MappingJobs[0]
	if job.Attempts != 0 || job.Error != "" || job.State != repoaudit.RepositoryMappingPending {
		t.Fatalf("reset job=%#v", job)
	}
	detail := httptest.NewRecorder()
	mux.ServeHTTP(detail, httptest.NewRequest(
		http.MethodGet, base+"/findings/"+findingID, nil,
	))
	if detail.Code != http.StatusOK ||
		!strings.Contains(detail.Body.String(), `"run_finding_status":"pending"`) {
		t.Fatalf("pending detail=%d %s", detail.Code, detail.Body.String())
	}

	if _, err := store.ProcessPendingMappingJobs(
		t.Context(),
		state.Repository,
		repoaudit.RepositoryMappingProcessOptions{
			DefaultBranchVerified: func(context.Context, repoaudit.Finding) (bool, error) {
				return true, nil
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	associated, _, associatedErr := store.Get(state.Repository)
	if associatedErr != nil || len(associated.RepositoryFindings) != 1 {
		t.Fatalf("associated=%#v err=%v", associated.RepositoryFindings, associatedErr)
	}
	aggregateDetail := httptest.NewRecorder()
	mux.ServeHTTP(aggregateDetail, httptest.NewRequest(
		http.MethodGet,
		base+"/repository-findings/"+associated.RepositoryFindings[0].ID,
		nil,
	))
	if aggregateDetail.Code != http.StatusOK ||
		!strings.Contains(aggregateDetail.Body.String(), `"run_finding_status":"associated_new"`) {
		t.Fatalf("aggregate detail=%d %s", aggregateDetail.Code, aggregateDetail.Body.String())
	}
	conflict := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		base+"/findings/status",
		map[string]any{"finding_ids": []string{findingID}},
	)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("associated retry=%d %s", conflict.Code, conflict.Body.String())
	}
}

func TestRepositoryReviewRunFindingStatusRetryRouteErrors(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(
		t,
		handler,
		state.Repository,
		state.Runs[0].ID,
	)
	base := "/api/repository-reviews/automations/" + automation.ID + "/findings/status"

	missingHeaders := httptest.NewRecorder()
	mux.ServeHTTP(missingHeaders, httptest.NewRequest(
		http.MethodPost,
		base,
		strings.NewReader(`{"finding_ids":[]}`),
	))
	if missingHeaders.Code < http.StatusBadRequest {
		t.Fatalf("missing-header retry status=%d", missingHeaders.Code)
	}

	queryResponse := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		base+"?unexpected=true",
		map[string]any{"finding_ids": []string{state.Findings[0].ID}},
	)
	if queryResponse.Code < http.StatusBadRequest {
		t.Fatalf("query retry status=%d", queryResponse.Code)
	}

	malformedRequest := httptest.NewRequest(
		http.MethodPost,
		base,
		strings.NewReader(`{`),
	)
	setRepositoryReviewMutationHeaders(malformedRequest)
	malformedResponse := httptest.NewRecorder()
	mux.ServeHTTP(malformedResponse, malformedRequest)
	if malformedResponse.Code < http.StatusBadRequest {
		t.Fatalf("malformed retry status=%d", malformedResponse.Code)
	}

	missingAutomation := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/automations/rra_missing/findings/status",
		map[string]any{"finding_ids": []string{state.Findings[0].ID}},
	)
	if missingAutomation.Code != http.StatusNotFound {
		t.Fatalf(
			"missing-automation retry status=%d body=%s",
			missingAutomation.Code,
			missingAutomation.Body.String(),
		)
	}

	automationStore, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	emptyAutomation := testRepositoryReviewAutomation()
	emptyAutomation.ID = automation.ID + "_empty"
	emptyAutomation.Repository = "owner/repository-without-ledger"
	emptyAutomation.RunIDs = []string{"run_without_ledger"}
	emptyAutomation, err = automationStore.CreateAutomation(t.Context(), emptyAutomation)
	if err != nil {
		t.Fatal(err)
	}
	missingLedger := repositoryReviewAutomationMutation(
		t,
		mux,
		http.MethodPost,
		"/api/repository-reviews/automations/"+emptyAutomation.ID+"/findings/status",
		map[string]any{"finding_ids": []string{state.Findings[0].ID}},
	)
	if missingLedger.Code != http.StatusNotFound {
		t.Fatalf(
			"missing-ledger retry status=%d body=%s",
			missingLedger.Code,
			missingLedger.Body.String(),
		)
	}
	before, _, err := automationStore.Get(state.Repository)
	if err != nil {
		t.Fatal(err)
	}
	_, err = automationStore.UpdateAutomation(
		t.Context(), automation.ID, automation.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	oldCampaign := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, base,
		map[string]any{"finding_ids": []string{state.Findings[0].ID}},
	)
	if oldCampaign.Code != http.StatusNotFound {
		t.Fatalf("old-campaign retry=%d %s", oldCampaign.Code, oldCampaign.Body.String())
	}
	after, _, err := automationStore.Get(state.Repository)
	if err != nil || after.Version != before.Version ||
		after.MappingJobs[0].Attempts != before.MappingJobs[0].Attempts {
		t.Fatalf("old-campaign retry mutated state: before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestRepositoryReviewAutomationLedgerCapabilitiesUseOneSnapshot(t *testing.T) {
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := handler.repositoryReviewAutomationLedger(t.Context(), automation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAutomation(
		t.Context(), automation.ID, automation.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.Name += " changed"
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RepositoryReviewPurgeEligibilityForAutomation(ledger.Automation); !errors.Is(
		err,
		repoaudit.ErrConflict,
	) {
		t.Fatalf("stale eligibility error = %v", err)
	}
	capabilities := repositoryReviewGlobalCapabilities(ledger)
	if capabilities.PurgeSummary == nil ||
		capabilities.PurgeSummary.LedgerFence != ledger.PurgeEligibility.Summary.LedgerFence {
		t.Fatalf("snapshot capabilities=%#v eligibility=%#v", capabilities, ledger.PurgeEligibility)
	}
	for _, blocker := range capabilities.PurgeBlockers {
		if blocker.Code == repoaudit.RepositoryReviewPurgeBlockerRetentionUnavailable {
			t.Fatalf("snapshot was replaced with unavailable blocker: %#v", capabilities)
		}
	}
}

func TestRepositoryReviewUnavailablePurgeInventoryOmitsCounts(t *testing.T) {
	capabilities := repositoryReviewGlobalCapabilities(repositoryReviewAutomationLedger{
		PurgeInventoryError: errors.New("injected inventory failure"),
	})
	if capabilities.PurgeSummary != nil || capabilities.CanPurgeHistory ||
		capabilities.CanRemoveRepository || len(capabilities.PurgeBlockers) != 1 ||
		capabilities.PurgeBlockers[0].Code != repoaudit.RepositoryReviewPurgeBlockerRetentionUnavailable {
		t.Fatalf("unavailable purge capabilities=%#v", capabilities)
	}
}

func TestRepositoryReviewRepositoryFindingLifecycleAndValidationRoutes(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	state := seedRepositoryReviewAPIState(t, workspace)
	store := repoaudit.NewStore(workspace)
	if _, err := store.ProcessPendingMappingJobs(
		t.Context(), state.Repository, repoaudit.RepositoryMappingProcessOptions{
			DefaultBranchVerified: func(context.Context, repoaudit.Finding) (bool, error) {
				return true, nil
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.Get(state.Repository)
	if err != nil || !found || len(state.RepositoryFindings) != 1 {
		t.Fatalf("repository findings=%#v found=%v err=%v", state.RepositoryFindings, found, err)
	}
	aggregate := state.RepositoryFindings[0]
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	page := httptest.NewRecorder()
	mux.ServeHTTP(page, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+"/repository-findings?query=ALL",
		nil,
	))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), aggregate.ID) {
		t.Fatalf("repository findings page=%d %s", page.Code, page.Body.String())
	}
	path := "/api/repository-reviews/automations/" + automation.ID +
		"/repository-findings/" + aggregate.ID
	dismissed := repositoryReviewAutomationMutation(t, mux, http.MethodPatch, path, map[string]any{
		"lifecycle": "dismissed", "expected_version": aggregate.Version,
	})
	if dismissed.Code != http.StatusOK || !strings.Contains(dismissed.Body.String(), `"lifecycle":"dismissed"`) {
		t.Fatalf("dismiss=%d %s", dismissed.Code, dismissed.Body.String())
	}
	var dismissedPayload struct {
		Finding repoaudit.RepositoryFinding `json:"repository_finding"`
	}
	if err := json.Unmarshal(dismissed.Body.Bytes(), &dismissedPayload); err != nil {
		t.Fatal(err)
	}
	reopened := repositoryReviewAutomationMutation(t, mux, http.MethodPatch, path, map[string]any{
		"lifecycle": "open", "expected_version": dismissedPayload.Finding.Version,
	})
	if reopened.Code != http.StatusOK {
		t.Fatalf("reopen=%d %s", reopened.Code, reopened.Body.String())
	}
	validation := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/repository-findings/validations",
		map[string]any{"repository_finding_ids": []string{aggregate.ID}},
	)
	if validation.Code != http.StatusAccepted ||
		!strings.Contains(validation.Body.String(), `"state":"pending"`) {
		t.Fatalf("validation=%d %s", validation.Code, validation.Body.String())
	}
}

func TestRepositoryReviewRepositoryFindingDetailProjectsSafeFixCheckFailure(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	store := repoaudit.NewStore(workspace)
	if _, err := store.ProcessPendingMappingJobs(
		t.Context(), state.Repository, repoaudit.RepositoryMappingProcessOptions{
			DefaultBranchVerified: func(context.Context, repoaudit.Finding) (bool, error) {
				return true, nil
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.Get(state.Repository)
	if err != nil || !found || len(state.RepositoryFindings) != 1 {
		t.Fatalf("repository findings=%#v found=%v err=%v", state.RepositoryFindings, found, err)
	}
	aggregate := state.RepositoryFindings[0]
	if _, _, reserveErr := store.ReserveValidationJobs(
		state.Repository,
		[]string{aggregate.ID},
		repoaudit.RepositoryMappingModelSnapshot{},
	); reserveErr != nil {
		t.Fatal(reserveErr)
	}
	secret := "provider token=secret private/repository/path.go"
	result, err := store.ProcessPendingValidationJobs(
		t.Context(),
		state.Repository,
		repoaudit.RepositoryValidationProcessOptions{
			Evidence: func(
				context.Context,
				repoaudit.RepositoryFinding,
				[]string,
			) ([]repoaudit.RepositoryValidationEvidence, error) {
				return []repoaudit.RepositoryValidationEvidence{{CurrentSource: "bounded source"}}, nil
			},
			Adjudicate: func(
				context.Context,
				repoaudit.RepositoryMappingModelSnapshot,
				repoaudit.RepositoryFinding,
				[]repoaudit.RepositoryValidationEvidence,
			) (repoaudit.RepositoryValidationDecision, error) {
				return repoaudit.RepositoryValidationDecision{}, repoaudit.WrapRepositoryValidationFailure(
					repoaudit.RepositoryValidationFailureCodeModelOutputInvalid,
					errors.New(secret),
				)
			},
			VerifyAncestry: func(context.Context, string) (bool, error) { return true, nil },
		},
	)
	if err != nil || result.Failed != 1 {
		t.Fatalf("validation result=%#v err=%v", result, err)
	}
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+
			"/repository-findings/"+aggregate.ID,
		nil,
	))
	var payload struct {
		Finding repoaudit.RepositoryFinding `json:"repository_finding"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	history := payload.Finding.ResolutionHistory
	if response.Code != http.StatusOK || payload.Finding.ValidationState != repoaudit.RepositoryValidationFailed ||
		len(history) != 1 || history[0].Failure == nil ||
		history[0].Failure.Code != repoaudit.RepositoryValidationFailureCodeModelOutputInvalid ||
		strings.Contains(response.Body.String(), secret) ||
		strings.Contains(response.Body.String(), "private/repository/path.go") {
		t.Fatalf("detail status=%d payload=%#v body=%s", response.Code, payload, response.Body.String())
	}
}

func TestRepositoryReviewAutomationIssueGenerationUsesSnapshottedWriter(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)

	previous := runRepositoryReviewIssueWriter
	t.Cleanup(func() { runRepositoryReviewIssueWriter = previous })
	type capturedCall struct {
		model        string
		account      string
		instructions string
	}
	captured := make(chan capturedCall, 1)
	runRepositoryReviewIssueWriter = func(
		_ context.Context,
		_ *Handler,
		automation repoaudit.RepositoryReviewAutomation,
		_ repoaudit.Finding,
		_ []repoaudit.FindingContext,
		instructions string,
		account string,
	) (repositoryReviewIssueWriterResult, error) {
		captured <- capturedCall{
			model: automation.IssueWriterModel, account: account, instructions: instructions,
		}
		return repositoryReviewIssueWriterResult{
			Title: "AI issue title", Body: "Grounded evidence and provenance.", Labels: []string{"bug"},
		}, nil
	}

	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/generations",
		map[string]any{
			"generation_id": "rrig_test", "finding_ids": []string{state.Findings[0].ID},
			"instructions_mode": "default",
		},
	)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "AI issue title") ||
		!strings.Contains(response.Body.String(), `"state":"editing"`) {
		t.Fatalf("generation status=%d body=%s", response.Code, response.Body.String())
	}
	call := <-captured
	if call.model != "cheap" || call.account != "api" ||
		!strings.Contains(call.instructions, "commit/blob provenance") {
		t.Fatalf("writer call=%#v", call)
	}
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	updated, found, err := store.Get(state.Repository)
	if err != nil || !found || len(updated.IssueDrafts) != 1 ||
		updated.IssueDrafts[0].GeneratorModel != "cheap" ||
		updated.IssueDrafts[0].GeneratorAccount != "api" ||
		updated.Findings[0].IssueDraftID != updated.IssueDrafts[0].ID {
		t.Fatalf("durable generation=%#v found=%v err=%v", updated, found, err)
	}
	regenerated := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/"+
			updated.IssueDrafts[0].ID+"/regenerate",
		map[string]any{"expected_version": updated.IssueDrafts[0].Version},
	)
	if regenerated.Code != http.StatusOK ||
		!strings.Contains(regenerated.Body.String(), `"state":"editing"`) {
		t.Fatalf("regeneration status=%d body=%s", regenerated.Code, regenerated.Body.String())
	}
	regenerationCall := <-captured
	if regenerationCall.model != "cheap" || regenerationCall.account != "api" {
		t.Fatalf("regeneration writer call=%#v", regenerationCall)
	}
}

func TestRepositoryReviewIssueDraftUsesCurrentAssignedProfileAndFreezesProvenance(t *testing.T) {
	_, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	store := repoaudit.NewStore(workspace)
	profile, err := store.CreateProfile(t.Context(), repoaudit.RepositoryReviewProfile{
		Name: "Current issue policy", ReviewFocus: "Find bugs.", ReviewerModel: "cheap",
		IssueWriterModel: "cheap", IssuePrompt: "Initial issue presentation.", AccountRef: "api",
		AutoContinue: true, MaxFilesPerRun: 4, MaxContentBytes: 65536,
		MaxParallelChildren: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.ID = "rra_current_profile"
	automation.Repository = state.Repository
	automation.CampaignID = state.CurrentCampaign.ID
	automation.ResolvedCommitSHA = state.CurrentCampaign.CommitSHA
	automation.RunIDs = []string{state.Runs[0].ID}
	automation.StartedAt = time.Now().UTC().Add(-time.Hour)
	automation, err = repoaudit.MaterializeRepositoryReviewAutomation(profile, automation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateAutomation(t.Context(), automation); err != nil {
		t.Fatal(err)
	}
	profile, err = store.UpdateProfile(
		t.Context(),
		profile.ID,
		profile.Version,
		func(candidate *repoaudit.RepositoryReviewProfile) error {
			candidate.IssuePrompt = "Current assigned issue presentation."
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	previous := runRepositoryReviewIssueWriter
	t.Cleanup(func() { runRepositoryReviewIssueWriter = previous })
	captured := make(chan string, 1)
	runRepositoryReviewIssueWriter = func(
		_ context.Context,
		_ *Handler,
		_ repoaudit.RepositoryReviewAutomation,
		_ repoaudit.Finding,
		_ []repoaudit.FindingContext,
		instructions string,
		_ string,
	) (repositoryReviewIssueWriterResult, error) {
		captured <- instructions
		return repositoryReviewIssueWriterResult{
			Title: "Current-profile issue", Body: "Grounded diagnosis.", Labels: []string{"bug"},
		}, nil
	}
	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/generations",
		map[string]any{
			"generation_id":     "rrig_current_profile",
			"finding_ids":       []string{state.Findings[0].ID},
			"instructions_mode": "default",
		},
	)
	if response.Code != http.StatusOK || <-captured != profile.IssuePrompt {
		t.Fatalf("generation status=%d body=%s", response.Code, response.Body.String())
	}
	updated, found, err := store.Get(state.Repository)
	if err != nil || !found || len(updated.IssueDrafts) != 1 {
		t.Fatalf("updated=%#v found=%v err=%v", updated, found, err)
	}
	draft := updated.IssueDrafts[0]
	if draft.GeneratorProfileID != profile.ID || draft.GeneratorProfileVersion != profile.Version ||
		draft.ResolvedInstructions != profile.IssuePrompt || draft.GeneratorModel != "cheap" ||
		draft.GeneratorAccount != "api" {
		t.Fatalf("draft provenance=%#v", draft)
	}
}

func TestRepositoryReviewDirectPostGeneratesThenPublishesWithoutConfirmationPayload(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(
		t, handler, state.Repository, state.Runs[0].ID,
	)
	previous := runRepositoryReviewIssueWriter
	t.Cleanup(func() { runRepositoryReviewIssueWriter = previous })
	runRepositoryReviewIssueWriter = func(
		context.Context,
		*Handler,
		repoaudit.RepositoryReviewAutomation,
		repoaudit.Finding,
		[]repoaudit.FindingContext,
		string,
		string,
	) (repositoryReviewIssueWriterResult, error) {
		return repositoryReviewIssueWriterResult{
			Title: "Direct issue", Body: "Grounded diagnosis and provenance.", Labels: []string{"bug"},
		}, nil
	}
	installEventProxyStubs(t, func(request *http.Request, _ time.Duration) (*http.Response, error) {
		if !strings.Contains(request.URL.Path, "/issue-drafts/") ||
			!strings.HasSuffix(request.URL.Path, "/publish") {
			t.Fatalf("unexpected direct-post upstream path %q", request.URL.Path)
		}
		return eventUpstreamResponse(http.StatusOK, `{"outcome":"posted"}`), nil
	})
	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/findings/"+
			state.Findings[0].ID+"/post",
		map[string]any{"expected_version": state.Findings[0].Version},
	)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"outcome":"posted"`) ||
		!strings.Contains(response.Body.String(), "Direct issue") {
		t.Fatalf("direct post status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRepositoryReviewInterruptedGenerationResumesWithSameGenerationID(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName: "old-account", Provider: "openai", Model: "openai/old", Enabled: true,
	})
	if saveErr := config.SaveConfig(handler.configPath, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}
	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	generationRequest := repoaudit.IssueGenerationRequest{
		Repository: state.Repository, FindingID: state.Findings[0].ID,
		GenerationID:         "rrig_interrupted",
		ResolvedInstructions: "Persisted presentation instructions.",
		InstructionsMode:     repoaudit.IssueDraftInstructionsDefault,
		GeneratorModel:       "quality", GeneratorAccount: "old-account",
	}
	_, draft, reserved, err := store.ReserveIssueGeneration(generationRequest)
	if err != nil || !reserved || draft.State != repoaudit.IssueDraftGenerating {
		t.Fatalf("interrupted reservation=%#v reserved=%v err=%v", draft, reserved, err)
	}
	_, activeDraft, claimed, err := claimRepositoryReviewIssueGeneration(store, generationRequest)
	if err != nil || !claimed {
		t.Fatalf("active generation claim=%#v claimed=%v err=%v", activeDraft, claimed, err)
	}
	defer releaseRepositoryReviewIssueGeneration(activeDraft)
	previous := runRepositoryReviewIssueWriter
	t.Cleanup(func() { runRepositoryReviewIssueWriter = previous })
	var calls atomic.Int64
	var capturedModel, capturedAccount, capturedInstructions string
	runRepositoryReviewIssueWriter = func(
		_ context.Context,
		_ *Handler,
		writerAutomation repoaudit.RepositoryReviewAutomation,
		_ repoaudit.Finding,
		_ []repoaudit.FindingContext,
		instructions string,
		account string,
	) (repositoryReviewIssueWriterResult, error) {
		calls.Add(1)
		capturedModel = writerAutomation.IssueWriterModel
		capturedAccount = account
		capturedInstructions = instructions
		return repositoryReviewIssueWriterResult{
			Title: "Recovered preview", Body: "Grounded evidence.", Labels: []string{"bug"},
		}, nil
	}
	inProgress := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/"+draft.ID+"/regenerate",
		map[string]any{"expected_version": draft.Version},
	)
	if inProgress.Code != http.StatusOK || calls.Load() != 0 ||
		!strings.Contains(inProgress.Body.String(), `"state":"generating"`) {
		t.Fatalf(
			"coalesced retry status=%d calls=%d body=%s",
			inProgress.Code, calls.Load(), inProgress.Body.String(),
		)
	}
	// Releasing the in-memory owner simulates process loss: the durable
	// reservation remains generating, and the same generation ID can be claimed
	// by the next process/request without creating a second draft.
	releaseRepositoryReviewIssueGeneration(activeDraft)
	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/"+draft.ID+"/regenerate",
		map[string]any{"expected_version": draft.Version},
	)
	if response.Code != http.StatusOK || calls.Load() != 1 ||
		!strings.Contains(response.Body.String(), `"generation_id":"rrig_interrupted"`) ||
		!strings.Contains(response.Body.String(), `"state":"editing"`) ||
		capturedModel != "quality" || capturedAccount != "old-account" ||
		capturedInstructions != "Persisted presentation instructions." {
		t.Fatalf(
			"interrupted retry status=%d calls=%d model=%q account=%q instructions=%q body=%s",
			response.Code, calls.Load(), capturedModel, capturedAccount,
			capturedInstructions, response.Body.String(),
		)
	}
}

func TestRepositoryReviewCanonicalIssueRoutesLifecycleAndPaging(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedCanonicalRepositoryReviewGenerationFindings(t, workspace, 2)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	store := repoaudit.NewSQLiteStore(workspace)
	drafts := make([]repoaudit.IssueDraft, 0, len(state.Findings))
	for index, finding := range state.Findings {
		_, draft, reserved, err := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
			Repository: state.Repository, FindingID: finding.ID,
			GenerationID:         fmt.Sprintf("rrig_canonical_lifecycle_%d", index),
			ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
			InstructionsMode:     repoaudit.IssueDraftInstructionsDefault,
			GeneratorModel:       "cheap", GeneratorAccount: "api",
		})
		if err != nil || !reserved {
			t.Fatalf("reserve %d: reserved=%v err=%v", index, reserved, err)
		}
		_, draft, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, draft.GenerationID,
			fmt.Sprintf("Issue %d", index+1), "Grounded evidence.", []string{"bug"}, "",
		)
		if err != nil {
			t.Fatal(err)
		}
		drafts = append(drafts, draft)
	}
	base := "/api/repository-reviews/automations/" + automation.ID + "/issues"

	for _, target := range []string{
		base + "?offset=0&limit=1",
		base + "?generation_id=" + drafts[1].GenerationID,
		base + "?query=ALL&limit=1",
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("issue collection %q=%d %s", target, response.Code, response.Body.String())
		}
	}
	for _, target := range []string{
		base + "?unknown=1", base + "?offset=nope", base + "?limit=201",
		base + "?generation_id=" + strings.Repeat("x", 257),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid issue page %q=%d %s", target, response.Code, response.Body.String())
		}
	}

	detailPath := base + "/" + drafts[0].ID
	detail := httptest.NewRecorder()
	mux.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, detailPath, nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"can_edit":true`) {
		t.Fatalf("issue detail=%d %s", detail.Code, detail.Body.String())
	}
	stale := repositoryReviewAutomationMutation(t, mux, http.MethodPatch, detailPath, map[string]any{
		"title": "Edited", "body": "Edited body", "labels": []string{"bug"},
		"expected_version": drafts[0].Version + 1,
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale edit=%d %s", stale.Code, stale.Body.String())
	}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPatch, detailPath, strings.NewReader(`{`)),
		httptest.NewRequest(http.MethodPatch, detailPath, strings.NewReader(`{}`)),
	} {
		setRepositoryReviewMutationHeaders(request)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest && response.Code != http.StatusConflict {
			t.Fatalf("invalid edit=%d %s", response.Code, response.Body.String())
		}
	}
	missingEdit := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, base+"/rid_missing",
		map[string]any{"title": "Edited", "body": "Edited body", "expected_version": 1},
	)
	if missingEdit.Code != http.StatusNotFound {
		t.Fatalf("missing edit=%d %s", missingEdit.Code, missingEdit.Body.String())
	}
	edited := repositoryReviewAutomationMutation(t, mux, http.MethodPatch, detailPath, map[string]any{
		"title": "Edited", "body": "Edited body", "labels": []string{"bug", "triage"},
		"expected_version": drafts[0].Version,
	})
	if edited.Code != http.StatusOK {
		t.Fatalf("edit=%d %s", edited.Code, edited.Body.String())
	}
	var editedPayload struct {
		Issue repoaudit.IssueDraft `json:"issue"`
	}
	if err := json.Unmarshal(edited.Body.Bytes(), &editedPayload); err != nil {
		t.Fatal(err)
	}
	for _, request := range []struct {
		body map[string]any
		want int
	}{
		{map[string]any{"expected_version": editedPayload.Issue.Version, "confirmed": false}, http.StatusBadRequest},
		{map[string]any{"expected_version": editedPayload.Issue.Version + 1, "confirmed": true}, http.StatusConflict},
	} {
		response := repositoryReviewAutomationMutation(t, mux, http.MethodDelete, detailPath, request.body)
		if response.Code != request.want {
			t.Fatalf("delete boundary=%d want=%d %s", response.Code, request.want, response.Body.String())
		}
	}
	malformedDelete := httptest.NewRequest(http.MethodDelete, detailPath, strings.NewReader(`{`))
	setRepositoryReviewMutationHeaders(malformedDelete)
	malformedDeleteResponse := httptest.NewRecorder()
	mux.ServeHTTP(malformedDeleteResponse, malformedDelete)
	if malformedDeleteResponse.Code != http.StatusBadRequest {
		t.Fatalf("malformed delete=%d %s", malformedDeleteResponse.Code, malformedDeleteResponse.Body.String())
	}
	deleted := repositoryReviewAutomationMutation(t, mux, http.MethodDelete, detailPath, map[string]any{
		"expected_version": editedPayload.Issue.Version, "confirmed": true,
	})
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"outcome":"deleted"`) {
		t.Fatalf("delete=%d %s", deleted.Code, deleted.Body.String())
	}
}

func TestRepositoryReviewCanonicalAggregateIssueProjectionCoverage(t *testing.T) {
	finding := repoaudit.Finding{
		ID: "rdf_occurrence", CampaignID: "rrc_projection", RepositoryFindingID: "rrf_projection",
		Status: repoaudit.FindingOpen, IssueDraftID: "rid_projection", CreatedAt: time.Now().UTC(),
	}
	draft := repoaudit.IssueDraft{
		ID: "rid_projection", FindingIDs: []string{finding.ID},
		Origin: repoaudit.IssueDraftOriginLinked, State: repoaudit.IssueDraftPosted,
	}
	aggregate := repoaudit.RepositoryFinding{
		ID: "rrf_projection", MatchState: repoaudit.RepositoryMatchKnown,
		Lifecycle:          repoaudit.RepositoryFindingOpen,
		ReviewFindingIDs:   []string{"rdf_missing", finding.ID},
		Issue:              repoaudit.RepositoryFindingIssueAssociation{State: repoaudit.RepositoryFindingIssueOpen},
		PossibleDuplicates: []repoaudit.RepositoryFindingPossibleDuplicate{{CandidateID: "rrf_duplicate"}},
	}
	duplicate := repoaudit.RepositoryFinding{ID: "rrf_duplicate"}
	state := repoaudit.RepositoryState{
		Repository:         "owner/repo",
		Findings:           []repoaudit.Finding{finding},
		IssueDrafts:        []repoaudit.IssueDraft{draft},
		RepositoryFindings: []repoaudit.RepositoryFinding{aggregate, duplicate},
	}
	issue, found := repositoryReviewAggregateIssueByFinding(state, finding)
	if !found || issue.ID != draft.ID {
		t.Fatalf("aggregate issue=%#v found=%v", issue, found)
	}
	for _, candidate := range []repoaudit.Finding{
		{},
		{RepositoryFindingID: "rrf_missing"},
	} {
		if _, found := repositoryReviewAggregateIssueByFinding(state, candidate); found {
			t.Fatalf("unexpected aggregate issue for %#v", candidate)
		}
	}
	withoutIssue := state
	withoutIssue.RepositoryFindings = []repoaudit.RepositoryFinding{{
		ID: aggregate.ID,
		Issue: repoaudit.RepositoryFindingIssueAssociation{
			State: repoaudit.RepositoryFindingIssueNone,
		},
	}}
	if _, found := repositoryReviewAggregateIssueByFinding(withoutIssue, finding); found {
		t.Fatal("unassociated aggregate exposed an issue")
	}

	ledger := repositoryReviewAutomationLedger{
		Automation: repoaudit.RepositoryReviewAutomation{CampaignID: finding.CampaignID},
		State:      state, Found: true,
	}
	detail := repositoryReviewRepositoryFindingDetail(ledger, aggregate)
	if detail["issue"] == nil || detail["possible_duplicate_findings"] == nil {
		t.Fatalf("aggregate detail=%#v", detail)
	}
}

func TestRepositoryReviewIssueWriterRequestIsPrivateEphemeralAndStructured(t *testing.T) {
	canary := repoaudit.NewRepositoryReviewCampaignID()
	request := repositoryReviewIssueWriterAgentRequest(
		repoaudit.RepositoryReviewAutomation{IssueWriterModel: "writer"},
		repoaudit.Finding{ID: "finding", CampaignID: canary},
		[]repoaudit.FindingContext{{ID: "context", CampaignID: canary}},
		"instructions", "account",
	)
	if request.Model != "writer" || request.AccountRef != "account" ||
		!request.EphemeralSession || request.History != "none" || request.Cache != "none" ||
		request.Tools != workflows.AgentToolsNone || !request.PrivateContext ||
		request.IsolatedSystemPrompt != repositoryReviewIssueWriterSystemPrompt ||
		request.Output == nil || request.Output.Format != "json" ||
		request.Output.Schema["additionalProperties"] != false {
		t.Fatalf("issue writer request=%#v", request)
	}
	if strings.Contains(request.Prompt, canary) || strings.Contains(request.Prompt, "campaign_id") {
		t.Fatalf("issue writer prompt exposed campaign authority: %s", request.Prompt)
	}
	invalid := workflows.ValidateAgentStructuredOutput(
		`{"title":"Bug","body":"Evidence","labels":["bug"],"fix":"change the code"}`,
		request.Output,
	)
	if invalid.Valid || !strings.Contains(invalid.Error, "fix") {
		t.Fatalf("extra issue-writer field was accepted: %#v", invalid)
	}
}

func TestRepositoryReviewIssueWriterFailsClosedAfterAliasBecomesAgentic(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelList[0].Provider = "codex-cli"
	cfg.ModelList[0].Model = "codex-cli/gpt-5"
	cfg.ModelAliases[0].Model = "codex-cli/gpt-5"
	cfg.ModelAliases[0].AccountOverrides = nil
	if saveErr := config.SaveConfig(handler.configPath, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}
	_, err = defaultRunRepositoryReviewIssueWriter(
		t.Context(), handler,
		repoaudit.RepositoryReviewAutomation{
			IssueWriterModel: "cheap", EffectiveAccountRef: "api",
		},
		repoaudit.Finding{}, nil, repositoryReviewDefaultIssueInstructions, "api",
	)
	if err == nil || !strings.Contains(err.Error(), "agentic CLI") {
		t.Fatalf("agentic issue writer alias was not rejected: %v", err)
	}
}

func TestRepositoryReviewAutomationPublishUsesProtectedLedgerRoute(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	withDraft, draft, reserved, err := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
		Repository: state.Repository, FindingID: state.Findings[0].ID, GenerationID: "rrig_publish",
		ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
		InstructionsMode:     repoaudit.IssueDraftInstructionsDefault,
		GeneratorModel:       "cheap", GeneratorAccount: "api",
	})
	if err != nil || !reserved {
		t.Fatal(err)
	}
	_, draft, err = store.CompleteIssueGeneration(
		state.Repository, draft.ID, draft.GenerationID, "Issue", "Evidence", []string{"bug"}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	var capturedPath, capturedBody string
	installEventProxyStubs(t, func(request *http.Request, _ time.Duration) (*http.Response, error) {
		capturedPath = request.URL.Path
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		encoded, _ := json.Marshal(body)
		capturedBody = string(encoded)
		return eventUpstreamResponse(http.StatusOK, `{"draft":{"id":"`+draft.ID+`","state":"posted"}}`), nil
	})
	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/"+draft.ID+"/publish",
		map[string]any{"expected_version": draft.Version, "confirmed": true},
	)
	if response.Code != http.StatusOK ||
		capturedPath != "/runtime/repository-reviews/"+withDraft.ID+"/issue-drafts/"+draft.ID+"/publish" ||
		capturedBody != `{"expected_version":2}` {
		t.Fatalf(
			"publish status=%d path=%q request=%s response=%s",
			response.Code, capturedPath, capturedBody, response.Body.String(),
		)
	}
}

func TestRepositoryReviewIssueLinkActionsUseProtectedAutomationRoutes(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	var captured []string
	installEventProxyStubs(t, func(request *http.Request, _ time.Duration) (*http.Response, error) {
		captured = append(captured, request.Method+" "+request.URL.Path)
		return eventUpstreamResponse(http.StatusOK, `{"candidates":[]}`), nil
	})
	base := "/api/repository-reviews/automations/" + automation.ID +
		"/findings/" + state.Findings[0].ID + "/issue-link"
	candidates := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, base+"/candidates",
		map[string]any{"expected_version": state.Findings[0].Version},
	)
	link := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, base,
		map[string]any{
			"issue_url":        "https://github.com/owner/repo/issues/12",
			"expected_version": state.Findings[0].Version, "confirmed": true,
		},
	)
	if candidates.Code != http.StatusOK || link.Code != http.StatusOK || len(captured) != 2 ||
		captured[0] != "POST /runtime/repository-reviews/automations/"+automation.ID+
			"/findings/"+state.Findings[0].ID+"/issue-link/candidates" ||
		captured[1] != "POST /runtime/repository-reviews/automations/"+automation.ID+
			"/findings/"+state.Findings[0].ID+"/issue-link" {
		t.Fatalf(
			"candidate=%d link=%d captured=%#v",
			candidates.Code, link.Code, captured,
		)
	}
}

func TestRepositoryReviewIssueGenerationRequestAndRegenerationBoundaries(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	generationPath := "/api/repository-reviews/automations/" + automation.ID + "/issues/generations"
	validFinding := state.Findings[0].ID
	cases := []map[string]any{
		{},
		{"generation_id": "", "finding_ids": []string{validFinding}},
		{"generation_id": strings.Repeat("x", 257), "finding_ids": []string{validFinding}},
		{"generation_id": "rrig", "finding_ids": []string{}},
		{"generation_id": "rrig", "finding_ids": []string{""}},
		{"generation_id": "rrig", "finding_ids": []string{validFinding, validFinding}},
		{"generation_id": "rrig", "finding_ids": []string{validFinding}, "instructions_mode": "bad"},
		{"generation_id": "rrig", "finding_ids": []string{validFinding}, "instructions_mode": "custom"},
		{
			"generation_id": "rrig", "finding_ids": []string{validFinding},
			"instructions_mode": "custom", "instructions": strings.Repeat("x", 16<<10),
		},
	}
	for index, body := range cases {
		response := repositoryReviewAutomationMutation(t, mux, http.MethodPost, generationPath, body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid generation %d=%d %s", index, response.Code, response.Body.String())
		}
	}
	missingAutomation := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/rra_missing/issues/generations",
		map[string]any{"generation_id": "rrig", "finding_ids": []string{validFinding}},
	)
	if missingAutomation.Code != http.StatusNotFound {
		t.Fatalf("missing automation generation=%d %s", missingAutomation.Code, missingAutomation.Body.String())
	}
	store := repoaudit.NewStore(workspace)
	empty := testRepositoryReviewAutomation()
	empty.ID = "rra_generation_empty"
	empty.Repository = "owner/no-ledger"
	empty, err := store.CreateAutomation(t.Context(), empty)
	if err != nil {
		t.Fatal(err)
	}
	missingLedger := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+empty.ID+"/issues/generations",
		map[string]any{"generation_id": "rrig", "finding_ids": []string{validFinding}},
	)
	if missingLedger.Code != http.StatusNotFound {
		t.Fatalf("missing ledger generation=%d %s", missingLedger.Code, missingLedger.Body.String())
	}

	_, draft, reserved, err := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
		Repository: state.Repository, FindingID: validFinding, GenerationID: "rrig_regen_bound",
		ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
		InstructionsMode:     repoaudit.IssueDraftInstructionsDefault,
		GeneratorModel:       "cheap", GeneratorAccount: "api",
	})
	if err != nil || !reserved {
		t.Fatalf("reserve: %v/%v", reserved, err)
	}
	_, draft, err = store.CompleteIssueGeneration(
		state.Repository, draft.ID, draft.GenerationID, "Preview", "Evidence", []string{"bug"}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	regenPath := "/api/repository-reviews/automations/" + automation.ID + "/issues/" + draft.ID + "/regenerate"
	badRegen := repositoryReviewAutomationMutation(t, mux, http.MethodPost, regenPath, map[string]any{})
	if badRegen.Code != http.StatusConflict {
		t.Fatalf("missing regen version=%d %s", badRegen.Code, badRegen.Body.String())
	}
	staleRegen := repositoryReviewAutomationMutation(t, mux, http.MethodPost, regenPath, map[string]any{
		"expected_version": draft.Version + 1,
	})
	if staleRegen.Code != http.StatusConflict {
		t.Fatalf("stale regen=%d %s", staleRegen.Code, staleRegen.Body.String())
	}
	previousRandom := readRepositoryReviewIssueGenerationRandom
	readRepositoryReviewIssueGenerationRandom = func([]byte) (int, error) {
		return 0, errors.New("entropy unavailable")
	}
	randomFailure := repositoryReviewAutomationMutation(t, mux, http.MethodPost, regenPath, map[string]any{
		"expected_version": draft.Version,
	})
	readRepositoryReviewIssueGenerationRandom = previousRandom
	if randomFailure.Code != http.StatusInternalServerError {
		t.Fatalf("regen entropy failure=%d %s", randomFailure.Code, randomFailure.Body.String())
	}
	missingRegen := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, baseAutomationIssuePath(automation.ID, "missing")+"/regenerate",
		map[string]any{"expected_version": 1},
	)
	if missingRegen.Code != http.StatusNotFound {
		t.Fatalf("missing regen=%d %s", missingRegen.Code, missingRegen.Body.String())
	}
}

func TestRepositoryReviewGatewayProxyAndPublicationBoundaryCoverage(t *testing.T) {
	t.Run("link proxy validation", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		state := seedRepositoryReviewAPIState(t, workspace)
		automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
		base := "/api/repository-reviews/automations/" + automation.ID +
			"/findings/" + state.Findings[0].ID + "/issue-link/candidates"
		for name, request := range map[string]*http.Request{
			"cross-site": func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "http://launcher.local"+base, strings.NewReader(`{}`))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Sec-Fetch-Site", "cross-site")
				return r
			}(),
			"empty": httptest.NewRequest(http.MethodPost, base, nil),
			"invalid-json": func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, base, strings.NewReader(`{`))
				setRepositoryReviewMutationHeaders(r)
				return r
			}(),
			"query": func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, base+"?x=1", strings.NewReader(`{}`))
				setRepositoryReviewMutationHeaders(r)
				return r
			}(),
		} {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("%s proxy=%d %s", name, response.Code, response.Body.String())
			}
		}
	})

	t.Run("single publication boundaries", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		state := seedRepositoryReviewAPIState(t, workspace)
		state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
		automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
		store := repoaudit.NewStore(workspace)
		_, draft, _, err := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
			Repository: state.Repository, FindingID: state.Findings[0].ID,
			GenerationID: "rrig_gateway_bounds", ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
			InstructionsMode: repoaudit.IssueDraftInstructionsDefault,
			GeneratorModel:   "cheap", GeneratorAccount: "api",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, draft, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, draft.AttemptGenerationID,
			"Preview", "Evidence", []string{"bug"}, "",
		)
		if err != nil {
			t.Fatal(err)
		}
		path := baseAutomationIssuePath(automation.ID, draft.ID) + "/publish"
		for name, body := range map[string]map[string]any{
			"unconfirmed": {"expected_version": draft.Version, "confirmed": false},
			"stale":       {"expected_version": draft.Version + 1, "confirmed": true},
		} {
			response := repositoryReviewAutomationMutation(t, mux, http.MethodPost, path, body)
			want := http.StatusBadRequest
			if name == "stale" {
				want = http.StatusConflict
			}
			if response.Code != want {
				t.Fatalf("%s publication=%d %s", name, response.Code, response.Body.String())
			}
		}
		missing := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, baseAutomationIssuePath(automation.ID, "missing")+"/publish",
			map[string]any{"expected_version": 1, "confirmed": true},
		)
		if missing.Code != http.StatusNotFound {
			t.Fatalf("missing publication=%d %s", missing.Code, missing.Body.String())
		}
		installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
			response := eventUpstreamResponse(
				http.StatusServiceUnavailable,
				`{"code":"publication_failed","message":"safe"}`,
			)
			response.Header.Set("Retry-After", "1")
			return response, nil
		})
		failure := repositoryReviewAutomationMutation(t, mux, http.MethodPost, path, map[string]any{
			"expected_version": draft.Version, "confirmed": true,
		})
		if failure.Code != http.StatusServiceUnavailable || failure.Header().Get("Retry-After") != "1" {
			t.Fatalf(
				"gateway publication failure=%d headers=%v body=%s",
				failure.Code,
				failure.Header(),
				failure.Body.String(),
			)
		}
	})

	t.Run("batch selection and outcomes", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		state := seedRepositoryReviewAPIState(t, workspace)
		automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
		path := "/api/repository-reviews/automations/" + automation.ID + "/issues/publish"
		for name, body := range map[string]map[string]any{
			"unconfirmed":  {"confirmed": false, "issues": []map[string]any{{"id": "draft", "expected_version": 1}}},
			"empty":        {"confirmed": true, "issues": []map[string]any{}},
			"invalid item": {"confirmed": true, "issues": []map[string]any{{"id": "", "expected_version": 0}}},
			"duplicate": {"confirmed": true, "issues": []map[string]any{
				{"id": "draft", "expected_version": 1}, {"id": "draft", "expected_version": 1},
			}},
		} {
			response := repositoryReviewAutomationMutation(t, mux, http.MethodPost, path, body)
			if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
				t.Fatalf("%s batch=%d %s", name, response.Code, response.Body.String())
			}
		}
		missingAutomation := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			"/api/repository-reviews/automations/rra_missing/issues/publish",
			map[string]any{"confirmed": true, "issues": []map[string]any{{"id": "draft", "expected_version": 1}}},
		)
		if missingAutomation.Code != http.StatusNotFound {
			t.Fatalf("missing automation batch=%d %s", missingAutomation.Code, missingAutomation.Body.String())
		}
		emptyAutomation := testRepositoryReviewAutomation()
		emptyAutomation.ID = "rra_publish_empty"
		emptyAutomation.Repository = "owner/no-ledger"
		store := repoaudit.NewStore(workspace)
		if _, err := store.CreateAutomation(t.Context(), emptyAutomation); err != nil {
			t.Fatal(err)
		}
		missingLedger := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			"/api/repository-reviews/automations/"+emptyAutomation.ID+"/issues/publish",
			map[string]any{"confirmed": true, "issues": []map[string]any{{"id": "draft", "expected_version": 1}}},
		)
		if missingLedger.Code != http.StatusNotFound {
			t.Fatalf("missing ledger batch=%d %s", missingLedger.Code, missingLedger.Body.String())
		}

		// A valid ledger with an unknown draft exercises the per-selection
		// not-found result without aborting the whole confirmed batch.
		unknown := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost, path,
			map[string]any{"confirmed": true, "issues": []map[string]any{{"id": "missing", "expected_version": 1}}},
		)
		if unknown.Code != http.StatusOK || !strings.Contains(unknown.Body.String(), `"code":"not_found"`) {
			t.Fatalf("unknown draft batch=%d %s", unknown.Code, unknown.Body.String())
		}
	})

	// Direct publication result classification covers successful reconciliation,
	// ordinary success, and a safe gateway failure without depending on store state.
	handler := NewHandler("")
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	ledger := repositoryReviewAutomationLedger{State: repoaudit.RepositoryState{ID: "rrp_test"}}
	var status int
	var responseBody string
	installEventProxyStubs(t, func(_ *http.Request, _ time.Duration) (*http.Response, error) {
		return eventUpstreamResponse(status, responseBody), nil
	})
	status, responseBody = http.StatusAccepted, `{"outcome":"unknown","draft":{"id":"draft","state":"unknown"}}`
	unknown := handler.publishRepositoryReviewAutomationDraft(request, ledger, "draft", 1)
	status, responseBody = http.StatusOK, `{"draft":{"id":"draft","state":"posted","external_url":"https://github.com/o/r/issues/1"}}`
	posted := handler.publishRepositoryReviewAutomationDraft(request, ledger, "draft", 1)
	status, responseBody = http.StatusServiceUnavailable, `{"code":"finding_status_unresolved","message":"safe","publish_blockers":[{"code":"finding_status_unresolved","count":2,"message":"safe"}]}`
	failed := handler.publishRepositoryReviewAutomationDraft(request, ledger, "draft", 1)
	failedBlockers, blockersOK := failed["publish_blockers"].([]repoaudit.IssuePublicationBlocker)
	if unknown["outcome"] != "unknown" || posted["outcome"] != "posted" ||
		posted["success"] != true || failed["outcome"] != "failed" ||
		failed["code"] != "finding_status_unresolved" || !blockersOK ||
		len(failedBlockers) != 1 || failedBlockers[0].Count != 2 {
		t.Fatalf("publication outcomes unknown=%#v posted=%#v failed=%#v", unknown, posted, failed)
	}
}

func TestRepositoryReviewProfileCollectionFieldCoverage(t *testing.T) {
	profile := repoaudit.RepositoryReviewProfile{
		ID: "rrpf_fields", Name: "Fields", AccountRef: "account",
		ReviewerModel: "reviewer", IssueWriterModel: "writer", Force: true,
		AutoContinue: true, MaxFilesPerRun: 12, MaxParallelChildren: 4,
		Version: 3, UpdatedAt: time.Now().UTC(),
	}
	for _, field := range []string{
		"id", "name", "account", "reviewer", "issue_writer", "force",
		"auto_continue", "files", "parallel", "version", "updated",
	} {
		if _, ok := repositoryReviewProfileCollectionField(profile, collectionquery.Field(field)); !ok {
			t.Fatalf("profile collection field %q not resolved", field)
		}
	}
	profile.IssueWriterModel = ""
	if _, ok := repositoryReviewProfileCollectionField(profile, "issue_writer"); !ok {
		t.Fatal("inherited issue writer not resolved")
	}
	if _, ok := repositoryReviewProfileCollectionField(profile, "unknown"); ok {
		t.Fatal("unknown profile collection field resolved")
	}

	_, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	invalid := httptest.NewRecorder()
	mux.ServeHTTP(invalid, httptest.NewRequest(
		http.MethodGet, "/api/repository-reviews/profiles?unknown=1", nil,
	))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid profile collection request=%d %s", invalid.Code, invalid.Body.String())
	}
	badCursor := httptest.NewRecorder()
	mux.ServeHTTP(badCursor, httptest.NewRequest(
		http.MethodGet, "/api/repository-reviews/profiles?query=ALL&cursor=invalid&limit=1", nil,
	))
	if badCursor.Code != http.StatusBadRequest {
		t.Fatalf("bad profile cursor=%d %s", badCursor.Code, badCursor.Body.String())
	}
}

func TestRepositoryReviewDefaultIssueWriterProviderBoundaries(t *testing.T) {
	responseStatus := http.StatusOK
	responseContent := `{"title":"Generated issue","body":"Grounded evidence.","labels":["bug"]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(responseStatus)
		if responseStatus != http.StatusOK {
			_, _ = w.Write([]byte(`{"error":{"message":"provider unavailable"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-review", "object": "chat.completion",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": responseContent},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
			},
		})
	}))
	t.Cleanup(server.Close)
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelList[0].APIBase = server.URL + "/v1"
	cfg.ModelList[0].APIKeys = config.SimpleSecureStrings("test-key")
	if saveErr := config.SaveConfig(handler.configPath, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}
	automation := repoaudit.RepositoryReviewAutomation{
		IssueWriterModel: "cheap", EffectiveAccountRef: "api",
	}
	finding := repoaudit.Finding{
		ID: "finding", Title: "Lost update", Evidence: "A stale write overwrites data.",
		Impact: "Data is lost.", File: repoaudit.FileRef{Path: "service.go", BlobSHA: strings.Repeat("a", 40)},
		CommitSHA: strings.Repeat("b", 40), Validation: repoaudit.Validation{Summary: "Confirmed."},
	}
	generated, err := defaultRunRepositoryReviewIssueWriter(
		t.Context(), handler, automation, finding, nil,
		repositoryReviewDefaultIssueInstructions, "api",
	)
	if err != nil || generated.Title != "Generated issue" || generated.Body != "Grounded evidence." {
		t.Fatalf("generated=%#v err=%v", generated, err)
	}
	responseContent = `not structured JSON`
	if _, writerErr := defaultRunRepositoryReviewIssueWriter(
		t.Context(), handler, automation, finding, nil,
		repositoryReviewDefaultIssueInstructions, "api",
	); writerErr == nil || !strings.Contains(writerErr.Error(), "structured output") {
		t.Fatalf("invalid structured response error=%v", writerErr)
	}
	responseStatus = http.StatusServiceUnavailable
	if _, writerErr := defaultRunRepositoryReviewIssueWriter(
		t.Context(), handler, automation, finding, nil,
		repositoryReviewDefaultIssueInstructions, "api",
	); writerErr == nil {
		t.Fatal("provider failure was accepted")
	}
	responseStatus = http.StatusOK
	cfg, err = config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.List = []config.AgentConfig{{ID: "other", Default: true}}
	if err := config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := defaultRunRepositoryReviewIssueWriter(
		t.Context(), handler, automation, finding, nil,
		repositoryReviewDefaultIssueInstructions, "api",
	); err == nil || !strings.Contains(err.Error(), "main") {
		t.Fatalf("missing main agent resolution error=%v", err)
	}
}

func TestRepositoryReviewGenerationPersistenceAndAccountFailureBoundaries(t *testing.T) {
	t.Run("unavailable effective account", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		cfg, err := config.LoadConfig(handler.configPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Agents.Defaults.AccountRef = ""
		if saveErr := config.SaveConfig(handler.configPath, cfg); saveErr != nil {
			t.Fatal(saveErr)
		}
		state := seedRepositoryReviewAPIState(t, workspace)
		state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
		automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
		store := repoaudit.NewStore(workspace)
		_, draft, _, err := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
			Repository: state.Repository, FindingID: state.Findings[0].ID,
			GenerationID: "rrig_no_account_regen", ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
			InstructionsMode: repoaudit.IssueDraftInstructionsDefault,
			GeneratorModel:   "cheap", GeneratorAccount: "api",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, draft, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, draft.AttemptGenerationID,
			"Preview", "Evidence", []string{"bug"}, "",
		)
		if err != nil {
			t.Fatal(err)
		}
		regen := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			baseAutomationIssuePath(automation.ID, draft.ID)+"/regenerate",
			map[string]any{"expected_version": draft.Version},
		)
		if regen.Code != http.StatusInternalServerError {
			t.Fatalf("missing account regeneration=%d %s", regen.Code, regen.Body.String())
		}
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			"/api/repository-reviews/automations/"+automation.ID+"/issues/generations",
			map[string]any{
				"generation_id": "rrig_no_account", "finding_ids": []string{state.Findings[0].ID},
			},
		)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("missing account generation=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("late persistence failure is safe", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		state := seedRepositoryReviewAPIState(t, workspace)
		state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
		automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
		previous := runRepositoryReviewIssueWriter
		t.Cleanup(func() { runRepositoryReviewIssueWriter = previous })
		runRepositoryReviewIssueWriter = func(
			_ context.Context, _ *Handler, _ repoaudit.RepositoryReviewAutomation,
			_ repoaudit.Finding, _ []repoaudit.FindingContext, _, _ string,
		) (repositoryReviewIssueWriterResult, error) {
			root := filepath.Join(workspace, "repository_reviews")
			if err := os.RemoveAll(root); err != nil {
				return repositoryReviewIssueWriterResult{}, err
			}
			if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
				return repositoryReviewIssueWriterResult{}, err
			}
			return repositoryReviewIssueWriterResult{}, errors.New("private provider failure")
		}
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			"/api/repository-reviews/automations/"+automation.ID+"/issues/generations",
			map[string]any{
				"generation_id": "rrig_late_failure", "finding_ids": []string{state.Findings[0].ID},
			},
		)
		if response.Code != http.StatusInternalServerError ||
			strings.Contains(response.Body.String(), "not a directory") {
			t.Fatalf("late persistence generation=%d %s", response.Code, response.Body.String())
		}
	})

	state := repoaudit.RepositoryState{
		Repository: "owner/repo",
		Findings: []repoaudit.Finding{{
			ID: "posted", Status: repoaudit.FindingPosted, Version: 1,
		}},
	}
	ledger := repositoryReviewAutomationLedger{
		Store: repoaudit.NewStore(t.TempDir()), State: state, Found: true,
		Automation: repoaudit.RepositoryReviewAutomation{IssueWriterModel: "cheap"},
	}
	_, result := (&Handler{}).generateRepositoryReviewIssue(
		t.Context(), ledger, "posted", "rrig", repoaudit.IssueDraftInstructionsDefault,
		repositoryReviewDefaultIssueInstructions, "api",
	)
	if result["code"] != "generation_conflict" {
		t.Fatalf("generation conflict result=%#v", result)
	}
}

func TestRepositoryReviewRemainingGenerationAndLedgerBranches(t *testing.T) {
	t.Run("deterministic canceled slot", func(t *testing.T) {
		workspace := t.TempDir()
		store := repoaudit.NewStore(workspace)
		releases := make([]func(), 0, repositoryReviewIssueWriterConcurrency)
		for range repositoryReviewIssueWriterConcurrency {
			release, err := store.AcquireIssueGenerationSlot(t.Context(), repositoryReviewIssueWriterConcurrency)
			if err != nil {
				t.Fatal(err)
			}
			releases = append(releases, release)
		}
		defer func() {
			for _, release := range releases {
				release()
			}
		}()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := runRepositoryReviewIssueWriterWithSlot(
			ctx, store, &Handler{}, repoaudit.RepositoryReviewAutomation{},
			repoaudit.Finding{}, nil, "", "",
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("full canceled slot error=%v", err)
		}
	})

	t.Run("external regeneration lock", func(t *testing.T) {
		workspace := t.TempDir()
		state := seedRepositoryReviewAPIState(t, workspace)
		state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
		store := repoaudit.NewStore(workspace)
		_, draft, _, err := store.ReserveIssueGeneration(repoaudit.IssueGenerationRequest{
			Repository: state.Repository, FindingID: state.Findings[0].ID,
			GenerationID: "rrig_external_regen", ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
			InstructionsMode: repoaudit.IssueDraftInstructionsDefault,
			GeneratorModel:   "cheap", GeneratorAccount: "api",
		})
		if err != nil {
			t.Fatal(err)
		}
		_, draft, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, draft.AttemptGenerationID,
			"Preview", "Evidence", []string{"bug"}, "",
		)
		if err != nil {
			t.Fatal(err)
		}
		request := repoaudit.IssueGenerationRequest{
			Repository: state.Repository, FindingID: state.Findings[0].ID,
			GenerationID: "rrig_external_regen_2", ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
			InstructionsMode: repoaudit.IssueDraftInstructionsDefault,
			GeneratorModel:   "cheap", GeneratorAccount: "api", ExpectedDraftVersion: draft.Version,
		}
		_, generating, reserved, err := store.BeginIssueRegeneration(state.Repository, draft.ID, request)
		if err != nil || !reserved {
			t.Fatalf("begin external regen=%v err=%v", reserved, err)
		}
		release, acquired, err := store.TryLockIssueGenerationAttempt(
			state.Repository, draft.ID, generating.AttemptGenerationID,
		)
		if err != nil || !acquired {
			t.Fatalf("external regen lock=%v err=%v", acquired, err)
		}
		defer release()
		_, replay, claimed, err := claimRepositoryReviewIssueRegeneration(
			store, state.Repository, draft.ID, request,
		)
		if err != nil || claimed || replay.ID != draft.ID {
			t.Fatalf("external regen replay=%v draft=%#v err=%v", claimed, replay, err)
		}
	})

	t.Run("missing final ledger", func(t *testing.T) {
		handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
		state := seedRepositoryReviewAPIState(t, workspace)
		state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
		automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
		previous := runRepositoryReviewIssueWriter
		t.Cleanup(func() { runRepositoryReviewIssueWriter = previous })
		runRepositoryReviewIssueWriter = func(
			_ context.Context, _ *Handler, _ repoaudit.RepositoryReviewAutomation,
			_ repoaudit.Finding, _ []repoaudit.FindingContext, _, _ string,
		) (repositoryReviewIssueWriterResult, error) {
			databasePath := filepath.Join(
				workspace, "repository_reviews", "repository-reviews.db",
			)
			for _, suffix := range []string{"", "-wal", "-shm"} {
				_ = os.Remove(databasePath + suffix)
			}
			return repositoryReviewIssueWriterResult{Title: "Preview", Body: "Evidence"}, nil
		}
		response := repositoryReviewAutomationMutation(
			t, mux, http.MethodPost,
			"/api/repository-reviews/automations/"+automation.ID+"/issues/generations",
			map[string]any{
				"generation_id": "rrig_missing_final", "finding_ids": []string{state.Findings[0].ID},
			},
		)
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing final ledger=%d %s", response.Code, response.Body.String())
		}
	})

	t.Run("ledger list and empty owned lookups", func(t *testing.T) {
		handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
		store := repoaudit.NewStore(workspace)
		empty := testRepositoryReviewAutomation()
		empty.ID = "rra_empty_owned_lookup"
		empty.Repository = filepath.Join(t.TempDir(), "missing")
		empty, err := store.CreateAutomation(t.Context(), empty)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, lookupErr := handler.repositoryReviewAutomationFinding(t.Context(), empty.ID, "finding"); !errors.Is(
			lookupErr,
			os.ErrNotExist,
		) {
			t.Fatalf("empty owned finding error=%v", lookupErr)
		}
		if _, _, lookupErr := handler.repositoryReviewAutomationIssue(t.Context(), empty.ID, "draft"); !errors.Is(
			lookupErr,
			os.ErrNotExist,
		) {
			t.Fatalf("empty owned issue error=%v", lookupErr)
		}
		if _, _, lookupErr := handler.repositoryReviewAutomationFinding(
			t.Context(), "rra_missing", "finding",
		); !errors.Is(
			lookupErr,
			os.ErrNotExist,
		) {
			t.Fatalf("missing automation finding error=%v", lookupErr)
		}
		if _, _, lookupErr := handler.repositoryReviewAutomationIssue(t.Context(), "rra_missing", "draft"); !errors.Is(
			lookupErr,
			os.ErrNotExist,
		) {
			t.Fatalf("missing automation issue error=%v", lookupErr)
		}

		withRuns := testRepositoryReviewAutomation()
		withRuns.ID = "rra_bad_list"
		withRuns.Repository = filepath.Join(t.TempDir(), "detached")
		withRuns.RunIDs = []string{"run"}
		withRuns, err = store.CreateAutomation(t.Context(), withRuns)
		if err != nil {
			t.Fatal(err)
		}
		badState := filepath.Join(workspace, "repository_reviews", "repository-reviews.db")
		if err := os.WriteFile(badState, []byte("not-sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := handler.repositoryReviewAutomationLedger(t.Context(), withRuns.ID); err == nil {
			t.Fatal("corrupt list ledger lookup succeeded")
		}
	})
}

func baseAutomationIssuePath(automationID, draftID string) string {
	return "/api/repository-reviews/automations/" + automationID + "/issues/" + draftID
}

func seedRepositoryReviewDetailAutomation(
	t *testing.T,
	handler *Handler,
	repository string,
	runID string,
) repoaudit.RepositoryReviewAutomation {
	t.Helper()
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation := testRepositoryReviewAutomation()
	automation.ID = "rra_detail_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	automation.Repository = repository
	automation.RunIDs = []string{runID}
	if state, found, loadErr := store.Get(repoaudit.CanonicalRepositoryIdentity(repository)); loadErr == nil && found &&
		state.CurrentCampaign != nil {
		automation.CampaignID = state.CurrentCampaign.ID
		automation.ResolvedCommitSHA = state.CurrentCampaign.CommitSHA
	}
	automation.StartedAt = time.Now().UTC().Add(-time.Hour)
	automation.IssueWriterModel = "cheap"
	automation, err = store.CreateAutomation(t.Context(), automation)
	if err != nil {
		t.Fatal(err)
	}
	return automation
}
