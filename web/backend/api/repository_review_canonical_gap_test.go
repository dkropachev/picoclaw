package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewCanonicalLifecycleMissingLedgerAndModel(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	empty := testRepositoryReviewAutomation()
	empty.ID = "rra_canonical_empty_ledger"
	empty.Repository = "owner/no-ledger"
	empty.RunIDs = []string{"missing-run"}
	empty, err = store.CreateAutomation(t.Context(), empty)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/repository-reviews/automations/" + empty.ID
	for _, request := range []struct {
		method string
		path   string
		body   map[string]any
	}{
		{
			http.MethodPatch,
			base + "/repository-findings/rrf_missing",
			map[string]any{"lifecycle": "dismissed", "expected_version": 1},
		},
		{
			http.MethodPost,
			base + "/repository-findings/rrf_missing/duplicates",
			map[string]any{
				"candidate_id":                 "rrf_other",
				"decision":                     "distinct",
				"expected_provisional_version": 1,
			},
		},
		{
			http.MethodPost,
			base + "/repository-findings/validations",
			map[string]any{"repository_finding_ids": []string{"rrf_missing"}},
		},
		{
			http.MethodPost,
			base + "/repository-findings/rrf_missing/sync",
			map[string]any{},
		},
	} {
		response := repositoryReviewAutomationMutation(t, mux, request.method, request.path, request.body)
		if response.Code != http.StatusNotFound {
			t.Fatalf("missing ledger %s=%d %s", request.path, response.Code, response.Body.String())
		}
	}

	state := seedRepositoryReviewAPIState(t, workspace)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	validationPath := "/api/repository-reviews/automations/" + automation.ID +
		"/repository-findings/validations"
	originalLoad := loadRepositoryReviewLifecycleConfig
	t.Cleanup(func() { loadRepositoryReviewLifecycleConfig = originalLoad })
	loadRepositoryReviewLifecycleConfig = func(string) (*config.Config, error) {
		return nil, errors.New("injected lifecycle config failure")
	}
	configFailure := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, validationPath,
		map[string]any{"repository_finding_ids": []string{state.RepositoryFindings[0].ID}},
	)
	if configFailure.Code != http.StatusInternalServerError {
		t.Fatalf("validation config failure=%d %s", configFailure.Code, configFailure.Body.String())
	}
	loadRepositoryReviewLifecycleConfig = originalLoad

	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.AccountRef = ""
	if err = config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	unavailable := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, validationPath,
		map[string]any{"repository_finding_ids": []string{state.RepositoryFindings[0].ID}},
	)
	if unavailable.Code != http.StatusInternalServerError {
		t.Fatalf("unavailable validation model=%d %s", unavailable.Code, unavailable.Body.String())
	}
	if err = config.SaveConfig(handler.configPath, cfg); err != nil {
		t.Fatal(err)
	}

	blankCampaign := testRepositoryReviewAutomation()
	blankCampaign.ID = "rra_canonical_blank_campaign"
	blankCampaign.Repository = state.Repository
	blankCampaign.RunIDs = []string{state.Runs[0].ID}
	if _, err = store.CreateAutomation(t.Context(), blankCampaign); err != nil {
		t.Fatal(err)
	}
	blankRetry := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+blankCampaign.ID+"/findings/status",
		map[string]any{"finding_ids": []string{state.Findings[0].ID}},
	)
	if blankRetry.Code != http.StatusNotFound {
		t.Fatalf("blank campaign retry=%d %s", blankRetry.Code, blankRetry.Body.String())
	}

	base = "/api/repository-reviews/automations/" + automation.ID
	invalidLifecycle := repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch,
		base+"/repository-findings/"+state.RepositoryFindings[0].ID,
		map[string]any{"lifecycle": "invalid", "expected_version": state.RepositoryFindings[0].Version},
	)
	if invalidLifecycle.Code != http.StatusBadRequest {
		t.Fatalf("invalid lifecycle=%d %s", invalidLifecycle.Code, invalidLifecycle.Body.String())
	}
	invalidDuplicate := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		base+"/repository-findings/"+state.RepositoryFindings[0].ID+"/duplicates",
		map[string]any{
			"candidate_id": "rrf_missing", "decision": "distinct",
			"expected_provisional_version": state.RepositoryFindings[0].Version,
		},
	)
	if invalidDuplicate.Code != http.StatusBadRequest && invalidDuplicate.Code != http.StatusNotFound {
		t.Fatalf("invalid duplicate=%d %s", invalidDuplicate.Code, invalidDuplicate.Body.String())
	}
	missingValidation := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, base+"/repository-findings/validations",
		map[string]any{"repository_finding_ids": []string{"rrf_missing"}},
	)
	if missingValidation.Code != http.StatusNotFound {
		t.Fatalf("missing validation=%d %s", missingValidation.Code, missingValidation.Body.String())
	}

	installEventProxyStubs(t, func(*http.Request, time.Duration) (*http.Response, error) {
		return eventUpstreamResponse(http.StatusOK, `{"repository_finding":{"id":"`+
			state.RepositoryFindings[0].ID+`"}}`), nil
	})
	syncResponse := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		base+"/repository-findings/"+state.RepositoryFindings[0].ID+"/sync",
		map[string]any{},
	)
	if syncResponse.Code != http.StatusOK {
		t.Fatalf("canonical sync=%d %s", syncResponse.Code, syncResponse.Body.String())
	}
}

func TestRepositoryReviewIssueGenerationBoundsConcurrencyAndRetriesOnlyFailure(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	state := seedCanonicalRepositoryReviewGenerationFindings(t, workspace, 5)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	findingIDs := make([]string, 0, len(state.Findings))
	for _, finding := range state.Findings {
		findingIDs = append(findingIDs, finding.ID)
	}

	previous := runRepositoryReviewIssueWriter
	t.Cleanup(func() { runRepositoryReviewIssueWriter = previous })
	var active, maximum, calls atomic.Int64
	started := make(chan struct{}, 10)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	runRepositoryReviewIssueWriter = func(
		_ context.Context,
		_ *Handler,
		_ repoaudit.RepositoryReviewAutomation,
		finding repoaudit.Finding,
		_ []repoaudit.FindingContext,
		_ string,
		_ string,
	) (repositoryReviewIssueWriterResult, error) {
		calls.Add(1)
		inFlight := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if inFlight <= observed || maximum.CompareAndSwap(observed, inFlight) {
				break
			}
		}
		started <- struct{}{}
		<-release
		if finding.Title == "Finding 5" {
			return repositoryReviewIssueWriterResult{}, errors.New("provider detail must stay private")
		}
		return repositoryReviewIssueWriterResult{
			Title: finding.Title, Body: "Grounded evidence.", Labels: []string{"bug"},
		}, nil
	}

	body, err := json.Marshal(map[string]any{
		"generation_id": "rrig_batch", "finding_ids": findingIDs,
		"instructions_mode": "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/generations",
		strings.NewReader(string(body)),
	)
	setRepositoryReviewMutationHeaders(request)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(response, request)
		close(done)
	}()
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("four issue-writer calls did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("a fifth issue-writer call exceeded the concurrency limit")
	case <-time.After(25 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("issue generation did not complete")
	}
	if response.Code != http.StatusOK || maximum.Load() != 4 || calls.Load() != 5 ||
		!strings.Contains(response.Body.String(), `"state":"failed"`) ||
		strings.Contains(response.Body.String(), "provider detail must stay private") {
		t.Fatalf(
			"generation status=%d max=%d calls=%d body=%s",
			response.Code, maximum.Load(), calls.Load(), response.Body.String(),
		)
	}

	beforeReplay := calls.Load()
	replay := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+automation.ID+"/issues/generations",
		map[string]any{
			"generation_id": "rrig_batch", "finding_ids": findingIDs,
			"instructions_mode": "default",
		},
	)
	if replay.Code != http.StatusOK || calls.Load()-beforeReplay != 1 {
		t.Fatalf(
			"idempotent replay status=%d new_calls=%d body=%s",
			replay.Code, calls.Load()-beforeReplay, replay.Body.String(),
		)
	}
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	updated, found, err := store.Get(state.Repository)
	if err != nil || !found || len(updated.IssueDrafts) != 5 {
		t.Fatalf("replayed drafts=%d found=%v err=%v", len(updated.IssueDrafts), found, err)
	}
}

func TestRepositoryReviewCanonicalRouteFailureMatrix(t *testing.T) {
	badHandler := NewHandler(t.TempDir())
	badMux := http.NewServeMux()
	badHandler.RegisterRoutes(badMux)
	t.Cleanup(badHandler.Shutdown)

	for _, target := range []string{
		"/api/repository-reviews/automations",
		"/api/repository-reviews/automations/rra_missing",
		"/api/repository-reviews/automations/rra_missing/file-attributions?query=ALL",
		"/api/repository-reviews/automations/rra_missing/findings?query=ALL",
		"/api/repository-reviews/automations/rra_missing/findings/rdf_missing",
		"/api/repository-reviews/automations/rra_missing/findings/rdf_missing/sources",
		"/api/repository-reviews/automations/rra_missing/findings/rdf_missing/sources/rrw_missing",
		"/api/repository-reviews/automations/rra_missing/raw-findings?query=ALL",
		"/api/repository-reviews/automations/rra_missing/raw-findings/rrw_missing",
		"/api/repository-reviews/automations/rra_missing/findings-processing?query=ALL",
		"/api/repository-reviews/automations/rra_missing/findings-processing/sources/rrw_missing",
		"/api/repository-reviews/automations/rra_missing/repository-findings?query=ALL",
		"/api/repository-reviews/automations/rra_missing/repository-findings/rrf_missing",
		"/api/repository-reviews/automations/rra_missing/issues",
		"/api/repository-reviews/automations/rra_missing/issues/rid_missing",
	} {
		response := httptest.NewRecorder()
		badMux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code < http.StatusBadRequest {
			t.Fatalf("bad-config GET %s=%d %s", target, response.Code, response.Body.String())
		}
	}

	for _, probe := range []struct {
		method string
		target string
		body   map[string]any
	}{
		{http.MethodPost, "/api/repository-reviews/automations", map[string]any{
			"repository": "owner/repo", "profile_id": "rrpf_missing",
		}},
		{http.MethodPatch, "/api/repository-reviews/automations/rra_missing", map[string]any{
			"repository": "owner/repo", "profile_id": "rrpf_missing", "expected_version": 1,
		}},
		{
			http.MethodPost, "/api/repository-reviews/automations/rra_missing/findings/status",
			map[string]any{"finding_ids": []string{"rdf_missing"}},
		},
		{
			http.MethodPost, "/api/repository-reviews/automations/rra_missing/raw-findings/rrw_missing/retry",
			map[string]any{},
		},
		{
			http.MethodPost, "/api/repository-reviews/automations/rra_missing/findings-processing/retry",
			map[string]any{"source_ids": []string{"rrw_missing"}},
		},
		{
			http.MethodPost, "/api/repository-reviews/automations/rra_missing/findings-processing/sources/rrw_missing/retry",
			map[string]any{},
		},
		{
			http.MethodPatch, "/api/repository-reviews/automations/rra_missing/repository-findings/rrf_missing",
			map[string]any{"lifecycle": "dismissed", "expected_version": 1},
		},
		{
			http.MethodPost, "/api/repository-reviews/automations/rra_missing/repository-findings/rrf_missing/duplicates",
			map[string]any{"candidate_id": "rrf_other", "decision": "distinct", "expected_provisional_version": 1},
		},
		{
			http.MethodPost, "/api/repository-reviews/automations/rra_missing/repository-findings/validations",
			map[string]any{"repository_finding_ids": []string{"rrf_missing"}},
		},
		{
			http.MethodPost, "/api/repository-reviews/automations/rra_missing/repository-findings/rrf_missing/sync",
			map[string]any{},
		},
		{
			http.MethodPost, "/api/repository-reviews/automations/rra_missing/issues/generations",
			map[string]any{"generation_id": "rrig_missing", "finding_ids": []string{"rdf_missing"}},
		},
		{
			http.MethodPatch, "/api/repository-reviews/automations/rra_missing/issues/rid_missing",
			map[string]any{"title": "Title", "body": "Body", "expected_version": 1},
		},
		{
			http.MethodDelete, "/api/repository-reviews/automations/rra_missing/issues/rid_missing",
			map[string]any{"expected_version": 1, "confirmed": true},
		},
		{
			http.MethodPost, "/api/repository-reviews/automations/rra_missing/issues/rid_missing/regenerate",
			map[string]any{"expected_version": 1},
		},
	} {
		response := repositoryReviewAutomationMutation(t, badMux, probe.method, probe.target, probe.body)
		if response.Code < http.StatusBadRequest {
			t.Fatalf("bad-config %s %s=%d %s", probe.method, probe.target, response.Code, response.Body.String())
		}
	}

	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	for _, probe := range []struct {
		method string
		target string
	}{
		{http.MethodPost, "/api/repository-reviews/automations?unexpected=1"},
		{http.MethodPatch, "/api/repository-reviews/automations/rra_missing?unexpected=1"},
		{http.MethodDelete, "/api/repository-reviews/automations/rra_missing?unexpected=1"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/purge-history?unexpected=1"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/start?unexpected=1"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/pause?unexpected=1"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/resume?unexpected=1"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/restart?unexpected=1"},
	} {
		request := httptest.NewRequest(probe.method, probe.target, strings.NewReader("{}"))
		setRepositoryReviewMutationHeaders(request)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query fence %s %s=%d %s", probe.method, probe.target, response.Code, response.Body.String())
		}
	}
	for _, probe := range []struct {
		method string
		target string
	}{
		{http.MethodPost, "/api/repository-reviews/automations"},
		{http.MethodPatch, "/api/repository-reviews/automations/rra_missing"},
		{http.MethodDelete, "/api/repository-reviews/automations/rra_missing"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/purge-history"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/start"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/pause"},
	} {
		request := httptest.NewRequest(probe.method, probe.target, strings.NewReader("{"))
		setRepositoryReviewMutationHeaders(request)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("decode fence %s %s=%d %s", probe.method, probe.target, response.Code, response.Body.String())
		}
	}

	for _, probe := range []struct {
		method string
		target string
	}{
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/findings/status"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/raw-findings/rrw_missing/retry"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/findings-processing/retry"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/findings-processing/sources/rrw_missing/retry"},
		{http.MethodPatch, "/api/repository-reviews/automations/rra_missing/repository-findings/rrf_missing"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/repository-findings/rrf_missing/duplicates"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/repository-findings/validations"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/repository-findings/rrf_missing/sync"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/issues/generations"},
		{http.MethodPatch, "/api/repository-reviews/automations/rra_missing/issues/rid_missing"},
		{http.MethodDelete, "/api/repository-reviews/automations/rra_missing/issues/rid_missing"},
		{http.MethodPost, "/api/repository-reviews/automations/rra_missing/issues/rid_missing/regenerate"},
	} {
		queryRequest := httptest.NewRequest(
			probe.method, probe.target+"?unexpected=1", strings.NewReader("{}"),
		)
		setRepositoryReviewMutationHeaders(queryRequest)
		queryResponse := httptest.NewRecorder()
		mux.ServeHTTP(queryResponse, queryRequest)
		if queryResponse.Code != http.StatusBadRequest {
			t.Fatalf("canonical query fence %s %s=%d %s",
				probe.method, probe.target, queryResponse.Code, queryResponse.Body.String())
		}

		decodeRequest := httptest.NewRequest(probe.method, probe.target, strings.NewReader("{"))
		setRepositoryReviewMutationHeaders(decodeRequest)
		decodeResponse := httptest.NewRecorder()
		mux.ServeHTTP(decodeResponse, decodeRequest)
		if decodeResponse.Code != http.StatusBadRequest {
			t.Fatalf("canonical decode fence %s %s=%d %s",
				probe.method, probe.target, decodeResponse.Code, decodeResponse.Body.String())
		}
	}

	response := httptest.NewRecorder()
	writeRepositoryReviewAutomationError(response, repoaudit.ErrRepositoryReviewPurgeInProgress)
	if response.Code != http.StatusConflict {
		t.Fatalf("purge-in-progress status=%d %s", response.Code, response.Body.String())
	}
}

func TestRepositoryReviewIssueGenerationClaimBoundaryCoverage(t *testing.T) {
	workspace := t.TempDir()
	state := seedCanonicalRepositoryReviewGenerationFindings(t, workspace, 3)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	store := repoaudit.NewStore(workspace)
	requestFor := func(index int, generationID string) repoaudit.IssueGenerationRequest {
		return repoaudit.IssueGenerationRequest{
			Repository: state.Repository, FindingID: state.Findings[index].ID,
			GenerationID: generationID, ResolvedInstructions: repositoryReviewDefaultIssueInstructions,
			InstructionsMode: repoaudit.IssueDraftInstructionsDefault,
			GeneratorModel:   "cheap", GeneratorAccount: "api",
		}
	}

	firstRequest := requestFor(0, "rrig_claim_active")
	_, firstDraft, claimed, err := claimRepositoryReviewIssueGeneration(store, firstRequest)
	if err != nil || !claimed {
		t.Fatalf("first claim=%v draft=%#v err=%v", claimed, firstDraft, err)
	}
	_, replayDraft, replayClaimed, err := claimRepositoryReviewIssueGeneration(store, firstRequest)
	if err != nil || replayClaimed || replayDraft.ID != firstDraft.ID {
		t.Fatalf("active replay=%v draft=%#v err=%v", replayClaimed, replayDraft, err)
	}
	releaseRepositoryReviewIssueGeneration(firstDraft)
	releaseRepositoryReviewIssueGeneration(firstDraft) // idempotent cleanup covers the nil-release path.

	secondRequest := requestFor(1, "rrig_claim_locked")
	_, secondDraft, reserved, err := store.ReserveIssueGeneration(secondRequest)
	if err != nil || !reserved {
		t.Fatalf("second reserve=%v draft=%#v err=%v", reserved, secondDraft, err)
	}
	externalRelease, acquired, err := store.TryLockIssueGenerationAttempt(
		state.Repository, secondDraft.ID, secondDraft.AttemptGenerationID,
	)
	if err != nil || !acquired {
		t.Fatalf("external claim=%v err=%v", acquired, err)
	}
	_, lockedDraft, lockedClaimed, err := claimRepositoryReviewIssueGeneration(store, secondRequest)
	if err != nil || lockedClaimed || lockedDraft.ID != secondDraft.ID {
		t.Fatalf("locked replay=%v draft=%#v err=%v", lockedClaimed, lockedDraft, err)
	}
	externalRelease()

	thirdRequest := requestFor(2, "rrig_claim_failed")
	_, failedDraft, reserved, err := store.ReserveIssueGeneration(thirdRequest)
	if err != nil || !reserved {
		t.Fatalf("failed reserve=%v err=%v", reserved, err)
	}
	_, failedDraft, err = store.CompleteIssueGeneration(
		state.Repository, failedDraft.ID, failedDraft.AttemptGenerationID,
		"", "", nil, "safe failure",
	)
	if err != nil || failedDraft.State != repoaudit.IssueDraftFailed {
		t.Fatalf("failed completion=%#v err=%v", failedDraft, err)
	}
	_, retriedDraft, retried, err := claimRepositoryReviewIssueGeneration(store, thirdRequest)
	if err != nil || !retried || retriedDraft.State != repoaudit.IssueDraftGenerating {
		t.Fatalf("failed retry=%v draft=%#v err=%v", retried, retriedDraft, err)
	}
	releaseRepositoryReviewIssueGeneration(retriedDraft)

	// Settle the first generation, then exercise the dedicated regeneration
	// claim, active coalescing, and stale version error.
	_, firstDraft, reacquired, err := claimRepositoryReviewIssueGeneration(store, firstRequest)
	if err != nil || !reacquired {
		t.Fatalf("reacquire=%v err=%v", reacquired, err)
	}
	_, firstDraft, err = store.CompleteIssueGeneration(
		state.Repository, firstDraft.ID, firstDraft.AttemptGenerationID,
		"Preview", "Evidence", []string{"bug"}, "",
	)
	releaseRepositoryReviewIssueGeneration(firstDraft)
	if err != nil {
		t.Fatal(err)
	}
	regenRequest := firstRequest
	regenRequest.GenerationID = "rrig_regen_claim"
	regenRequest.ExpectedDraftVersion = firstDraft.Version
	_, regenerating, regenClaimed, err := claimRepositoryReviewIssueRegeneration(
		store, state.Repository, firstDraft.ID, regenRequest,
	)
	if err != nil || !regenClaimed {
		t.Fatalf("regen claim=%v draft=%#v err=%v", regenClaimed, regenerating, err)
	}
	_, coalesced, coalescedClaim, err := claimRepositoryReviewIssueRegeneration(
		store, state.Repository, firstDraft.ID, regenRequest,
	)
	if err != nil || coalescedClaim || coalesced.ID != firstDraft.ID {
		t.Fatalf("regen coalesced=%v draft=%#v err=%v", coalescedClaim, coalesced, err)
	}
	releaseRepositoryReviewIssueGeneration(regenerating)
	staleRequest := regenRequest
	staleRequest.GenerationID = "rrig_regen_stale"
	staleRequest.ExpectedDraftVersion = 1
	if _, _, _, claimErr := claimRepositoryReviewIssueRegeneration(
		store, state.Repository, firstDraft.ID, staleRequest,
	); !errors.Is(claimErr, repoaudit.ErrConflict) {
		t.Fatalf("stale regeneration error=%v", claimErr)
	}

	previousBegin := beginRepositoryReviewIssueRegeneration
	previousTryLock := tryLockRepositoryReviewIssueGenerationAttempt
	t.Cleanup(func() {
		beginRepositoryReviewIssueRegeneration = previousBegin
		tryLockRepositoryReviewIssueGenerationAttempt = previousTryLock
	})
	tryLockRepositoryReviewIssueGenerationAttempt = func(
		repoaudit.Store, string, string, string,
	) (func(), bool, error) {
		return nil, false, errors.New("injected attempt-lock failure")
	}
	if _, _, _, claimErr := claimRepositoryReviewIssueGeneration(store, secondRequest); claimErr == nil {
		t.Fatal("generation attempt-lock failure was ignored")
	}
	if _, _, _, claimErr := claimRepositoryReviewIssueRegeneration(
		store, state.Repository, firstDraft.ID, regenRequest,
	); claimErr == nil {
		t.Fatal("regeneration attempt-lock failure was ignored")
	}
	tryLockRepositoryReviewIssueGenerationAttempt = previousTryLock
	current, found, err := store.Get(state.Repository)
	if err != nil || !found {
		t.Fatalf("load failed retry state found=%v err=%v", found, err)
	}
	currentThird, _ := repositoryReviewIssueByID(current, retriedDraft.ID)
	_, _, err = store.CompleteIssueGeneration(
		state.Repository, currentThird.ID, currentThird.AttemptGenerationID,
		"", "", nil, "failed again",
	)
	if err != nil {
		t.Fatal(err)
	}
	beginRepositoryReviewIssueRegeneration = func(
		repoaudit.Store, string, string, repoaudit.IssueGenerationRequest,
	) (repoaudit.RepositoryState, repoaudit.IssueDraft, bool, error) {
		return repoaudit.RepositoryState{}, repoaudit.IssueDraft{}, false,
			errors.New("injected regeneration failure")
	}
	if _, _, _, err := claimRepositoryReviewIssueGeneration(store, thirdRequest); err == nil {
		t.Fatal("failed-draft regeneration failure was ignored")
	}
	beginRepositoryReviewIssueRegeneration = previousBegin

	unsafeStore := repoaudit.NewStore(filepath.Join(t.TempDir(), "missing", "workspace"))
	if _, _, _, err := claimRepositoryReviewIssueGeneration(unsafeStore, firstRequest); err == nil {
		t.Fatal("unsafe generation claim succeeded")
	}
}

func TestRepositoryReviewCanonicalIssueWriterOutputBoundaries(t *testing.T) {
	for name, test := range map[string]struct {
		outputs map[string]any
		valid   bool
	}{
		"invalid flag":  {outputs: map[string]any{}},
		"unmarshalable": {outputs: map[string]any{"structured_valid": true, "structured": make(chan int)}},
		"unknown field": {outputs: map[string]any{
			"structured_valid": true,
			"structured": map[string]any{
				"title": "Title", "body": "Body", "labels": []string{}, "extra": true,
			},
		}},
		"blank": {outputs: map[string]any{
			"structured_valid": true,
			"structured":       map[string]any{"title": "", "body": "Body", "labels": []string{}},
		}},
		"valid": {outputs: map[string]any{
			"structured_valid": true,
			"structured":       map[string]any{"title": " Title ", "body": " Body ", "labels": []string{"bug"}},
		}, valid: true},
	} {
		result, err := repositoryReviewIssueWriterResultFromOutputs(test.outputs)
		if test.valid {
			if err != nil || result.Title != "Title" || result.Body != "Body" {
				t.Fatalf("%s result=%#v err=%v", name, result, err)
			}
		} else if err == nil {
			t.Fatalf("%s invalid output succeeded: %#v", name, result)
		}
	}
}

func TestRepositoryReviewCanonicalDetailProjectionEdges(t *testing.T) {
	now := time.Now().UTC()
	issue := repoaudit.IssueDraft{
		ID: "rid_linked", Origin: repoaudit.IssueDraftOriginLinked,
		State: repoaudit.IssueDraftPosted,
	}
	first := repoaudit.Finding{
		ID: "rdf_a", CampaignID: "rrc_detail", Status: repoaudit.FindingOpen,
		RepositoryFindingID: "rrf_detail", IssueDraftID: issue.ID, CreatedAt: now,
	}
	second := repoaudit.Finding{
		ID: "rdf_b", CampaignID: "rrc_detail", Status: repoaudit.FindingOpen,
		RepositoryFindingID: "rrf_detail", CreatedAt: now,
	}
	state := repoaudit.RepositoryState{
		Repository: "owner/repo", Findings: []repoaudit.Finding{second, first},
		IssueDrafts: []repoaudit.IssueDraft{issue},
		RepositoryFindings: []repoaudit.RepositoryFinding{
			{
				ID: "rrf_detail", MatchState: repoaudit.RepositoryMatchKnown,
				Lifecycle:        repoaudit.RepositoryFindingOpen,
				ReviewFindingIDs: []string{first.ID, second.ID},
				PossibleDuplicates: []repoaudit.RepositoryFindingPossibleDuplicate{
					{CandidateID: "rrf_candidate"}, {CandidateID: "rrf_missing"},
				},
				Issue: repoaudit.RepositoryFindingIssueAssociation{
					State: repoaudit.RepositoryFindingIssueOpen,
				},
			},
			{ID: "rrf_candidate", MatchState: repoaudit.RepositoryMatchNew},
		},
	}
	ledger := repositoryReviewAutomationLedger{
		Found: true, State: state,
		Automation: repoaudit.RepositoryReviewAutomation{CampaignID: "rrc_detail"},
	}
	detail := repositoryReviewRepositoryFindingDetail(ledger, state.RepositoryFindings[0])
	duplicates, ok := detail["possible_duplicate_findings"].([]repoaudit.RepositoryFinding)
	if !ok || len(duplicates) != 1 || duplicates[0].ID != "rrf_candidate" {
		t.Fatalf("detail duplicates=%#v", detail)
	}

	orphan := second
	orphan.RepositoryFindingID = "rrf_missing"
	if capabilities := repositoryReviewFindingCapabilities(state, orphan); capabilities.CanGenerate {
		t.Fatalf("orphan capabilities=%#v", capabilities)
	}
	if _, found := repositoryReviewAggregateIssueByFinding(state, orphan); found {
		t.Fatal("orphan aggregate issue was projected")
	}
	state.RepositoryFindings[0].ReviewFindingIDs = []string{"rdf_missing"}
	if _, found := repositoryReviewAggregateIssueByFinding(state, second); found {
		t.Fatal("aggregate without a present issue occurrence was projected")
	}

	index := newRepositoryReviewRunFindingStatusIndex(repoaudit.RepositoryState{})
	for matchState, want := range map[repoaudit.RepositoryMatchState]repositoryReviewRunFindingStatus{
		repoaudit.RepositoryMatchNew:   repositoryReviewRunFindingAssociatedNew,
		repoaudit.RepositoryMatchKnown: repositoryReviewRunFindingAssociatedExisting,
	} {
		finding := repoaudit.Finding{
			ID: "rdf_status", RepositoryFindingID: "rrf_status", RepositoryMatchState: matchState,
		}
		if got := index.status(finding); got != want {
			t.Fatalf("status %s=%s want %s", matchState, got, want)
		}
	}
}

func TestRepositoryReviewCanonicalDetailResidualBranches(t *testing.T) {
	now := time.Now().UTC()
	issue := repoaudit.IssueDraft{
		ID: "rid_residual", Origin: repoaudit.IssueDraftOriginLinked,
		State: repoaudit.IssueDraftPosted,
	}
	older := repoaudit.Finding{
		ID: "rdf_residual_old", CampaignID: "rrc_residual", Status: repoaudit.FindingOpen,
		RepositoryFindingID: "rrf_residual", CreatedAt: now.Add(-time.Minute),
	}
	newer := repoaudit.Finding{
		ID: "rdf_residual_new", CampaignID: "rrc_residual", Status: repoaudit.FindingPosted,
		RepositoryFindingID: "rrf_residual", IssueDraftID: issue.ID, CreatedAt: now,
	}
	state := repoaudit.RepositoryState{
		Repository: "owner/repo", Findings: []repoaudit.Finding{newer, older},
		IssueDrafts: []repoaudit.IssueDraft{issue},
		RepositoryFindings: []repoaudit.RepositoryFinding{{
			ID: "rrf_residual", MatchState: repoaudit.RepositoryMatchKnown,
			Lifecycle:        repoaudit.RepositoryFindingOpen,
			ReviewFindingIDs: []string{older.ID, newer.ID},
			Issue: repoaudit.RepositoryFindingIssueAssociation{
				State: repoaudit.RepositoryFindingIssueOpen,
			},
		}},
	}
	ledger := repositoryReviewAutomationLedger{
		Found: true, State: state,
		Automation: repoaudit.RepositoryReviewAutomation{CampaignID: "rrc_residual"},
	}
	detail := repositoryReviewRepositoryFindingDetail(ledger, state.RepositoryFindings[0])
	if detail["finding"] == nil {
		t.Fatalf("residual aggregate detail=%#v", detail)
	}
	capabilities := repositoryReviewFindingCapabilities(state, older)
	if capabilities.CanGenerate || capabilities.CanUnlinkIssue || capabilities.CanReplaceIssue {
		t.Fatalf("aggregate issue capabilities=%#v", capabilities)
	}
	if got := newRepositoryReviewRunFindingStatusIndex(repoaudit.RepositoryState{}).status(
		repoaudit.Finding{ID: "rdf_unmapped"},
	); got != repositoryReviewRunFindingPending {
		t.Fatalf("unmapped canonical status=%q", got)
	}

	if _, err := defaultRunRepositoryReviewIssueWriter(
		t.Context(), nil, repoaudit.RepositoryReviewAutomation{}, repoaudit.Finding{}, nil, "", "",
	); err == nil {
		t.Fatal("nil issue writer handler succeeded")
	}
	badHandler := NewHandler(t.TempDir())
	t.Cleanup(badHandler.Shutdown)
	if _, err := defaultRunRepositoryReviewIssueWriter(
		t.Context(), badHandler, repoaudit.RepositoryReviewAutomation{}, repoaudit.Finding{}, nil, "", "",
	); err == nil {
		t.Fatal("directory-backed issue writer config succeeded")
	}
	if _, err := badHandler.repositoryReviewIssueWriterAccount(repoaudit.RepositoryReviewAutomation{}); err == nil {
		t.Fatal("directory-backed issue writer account succeeded")
	}

	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	seeded := seedRepositoryReviewAPIState(t, workspace)
	wrongCampaign := testRepositoryReviewAutomation()
	wrongCampaign.ID = "rra_detail_wrong_campaign"
	wrongCampaign.Repository = seeded.Repository
	wrongCampaign.CampaignID = repoaudit.NewRepositoryReviewCampaignID()
	wrongCampaign.RunIDs = []string{seeded.Runs[0].ID}
	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	wrongCampaign, err = store.CreateAutomation(t.Context(), wrongCampaign)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = handler.repositoryReviewAutomationFinding(
		t.Context(), wrongCampaign.ID, seeded.Findings[0].ID,
	); err == nil {
		t.Fatal("cross-campaign finding resolved")
	}
	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost,
		"/api/repository-reviews/automations/"+wrongCampaign.ID+"/issues/generations",
		map[string]any{
			"generation_id": "rrig_wrong_campaign",
			"finding_ids":   []string{seeded.Findings[0].ID},
		},
	)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-campaign issue generation=%d %s", response.Code, response.Body.String())
	}
}
