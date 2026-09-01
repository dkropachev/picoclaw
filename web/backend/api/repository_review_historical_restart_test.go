package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type repositoryReviewHistoricalRestartFixture struct {
	handler    *Handler
	mux        *http.ServeMux
	store      repoaudit.Store
	automation repoaudit.RepositoryReviewAutomation
	state      repoaudit.RepositoryState
	snapshot   repoaudit.HistoricalDeduplicationProfileSnapshot
}

func newRepositoryReviewHistoricalRestartFixture(
	t *testing.T,
	profileDrift bool,
	campaignDrift bool,
) repositoryReviewHistoricalRestartFixture {
	t.Helper()
	backfill := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	resolved := workflows.RepositoryReviewModelProfile{
		Revision:        "legacy-automation-profile",
		AccountRef:      backfill.automation.EffectiveAccountRef,
		ReviewerModels:  backfill.automation.ReviewerModels,
		MaxContentBytes: int(backfill.automation.MaxContentBytes),
	}
	automation, state, err := recoverRepositoryReviewHistoricalCampaign(
		t.Context(), backfill.store, backfill.workspace,
		backfill.automation, backfill.state, resolved,
	)
	if err != nil {
		t.Fatal(err)
	}
	// Restart is allowed only after downstream projections quiesce. This
	// fixture is concerned with replay identity, not repository mapping.
	state.MappingJobs = nil
	state.HistoricalDeduplication = repoaudit.HistoricalDeduplicationReplay{
		Required: true, Status: repoaudit.HistoricalDeduplicationPending,
		UpdatedAt: time.Now().UTC(),
	}
	state.Version++
	state.UpdatedAt = state.HistoricalDeduplication.UpdatedAt
	persistRepositoryReviewAdditionalCoverageState(t, backfill.workspace, state)

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = backfill.workspace
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}
	handler := NewHandler(configPath)
	t.Cleanup(handler.Shutdown)
	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	_, currentSnapshot, err := repositoryReviewHistoricalProfileSnapshot(
		t.Context(), handler.repositoryReviewControllerInstance(), backfill.store,
		loaded, automation,
	)
	if err != nil {
		t.Fatal(err)
	}
	frozenSnapshot := currentSnapshot
	if profileDrift {
		if frozenSnapshot.CandidateLimit == 0 {
			frozenSnapshot.CandidateLimit = 1
		} else {
			frozenSnapshot.CandidateLimit--
		}
	}
	state, _, err = backfill.store.FreezeHistoricalDeduplicationReplay(
		state.Repository, frozenSnapshot,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, admission, err := backfill.store.AdmitNextHistoricalDeduplicationBatch(
		state.Repository,
	)
	if err != nil || admission.Admitted != 1 {
		t.Fatalf("historical admission=%#v err=%v", admission, err)
	}
	state, _, err = backfill.store.FailHistoricalDeduplicationReplay(state.Repository, "")
	if err != nil {
		t.Fatal(err)
	}
	if campaignDrift {
		driftCampaign := "rrc_historical_api_drift"
		bucket, bucketErr := repoaudit.DeduplicationAdmissionBucket(
			driftCampaign, state.RawFindings[0].File, state.RawFindings[0].Symbol,
		)
		if bucketErr != nil {
			t.Fatal(bucketErr)
		}
		state.RawFindings[0].CampaignID = driftCampaign
		state.RawFindings[0].AdmissionBucket = bucket
		state.RawFindings[0].DiagnosisDigest = repoaudit.RawReviewFindingDiagnosisDigest(
			state.RawFindings[0],
		)
		state.DeduplicationJobs[0].AdmissionBucket = bucket
		state.Version++
		state.UpdatedAt = time.Now().UTC()
		persistRepositoryReviewAdditionalCoverageState(t, backfill.workspace, state)
	}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return repositoryReviewHistoricalRestartFixture{
		handler: handler, mux: mux, store: backfill.store,
		automation: automation, state: state, snapshot: currentSnapshot,
	}
}

func (fixture repositoryReviewHistoricalRestartFixture) request(
	t *testing.T,
	action string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/repository-reviews/automations/"+fixture.automation.ID+
			"/historical-deduplication/"+action,
		strings.NewReader(body),
	)
	setRepositoryReviewMutationHeaders(request)
	response := httptest.NewRecorder()
	fixture.mux.ServeHTTP(response, request)
	return response
}

func TestRepositoryReviewHistoricalResumeProfileDriftIsNonMutating(t *testing.T) {
	fixture := newRepositoryReviewHistoricalRestartFixture(t, true, false)
	before, found, err := fixture.store.Get(fixture.state.Repository)
	if err != nil || !found {
		t.Fatalf("load before resume found=%v err=%v", found, err)
	}
	response := fixture.request(t, "retry", `{}`)
	if response.Code != http.StatusConflict || !strings.Contains(
		response.Body.String(), `"code":"historical_consolidation_restart_required"`,
	) {
		t.Fatalf("profile drift status=%d body=%s", response.Code, response.Body.String())
	}
	after, found, err := fixture.store.Get(fixture.state.Repository)
	if err != nil || !found || after.Version != before.Version ||
		after.HistoricalDeduplication.Status != repoaudit.HistoricalDeduplicationFailed ||
		after.RawFindings[0].Version != before.RawFindings[0].Version {
		t.Fatalf("profile drift mutated replay before=%#v after=%#v found=%v err=%v",
			before.HistoricalDeduplication, after.HistoricalDeduplication, found, err)
	}
	restart := fixture.request(t, "restart", `{"confirmed":true}`)
	restarted, found, err := fixture.store.Get(fixture.state.Repository)
	if restart.Code != http.StatusAccepted || err != nil || !found ||
		restarted.HistoricalDeduplication.Status != repoaudit.HistoricalDeduplicationReplaying ||
		restarted.HistoricalDeduplication.ProfileSnapshot != fixture.snapshot {
		t.Fatalf("profile restart status=%d replay=%#v found=%v err=%v body=%s",
			restart.Code, restarted.HistoricalDeduplication, found, err, restart.Body.String())
	}
}

func TestRepositoryReviewHistoricalResumeCampaignDriftRequiresConfirmedRestart(t *testing.T) {
	fixture := newRepositoryReviewHistoricalRestartFixture(t, false, true)
	before, _, err := fixture.store.Get(fixture.state.Repository)
	if err != nil {
		t.Fatal(err)
	}
	response := fixture.request(t, "retry", `{}`)
	if response.Code != http.StatusConflict || !strings.Contains(
		response.Body.String(), `"code":"historical_consolidation_restart_required"`,
	) {
		t.Fatalf("campaign drift status=%d body=%s", response.Code, response.Body.String())
	}
	after, _, err := fixture.store.Get(fixture.state.Repository)
	if err != nil || after.Version != before.Version ||
		after.RawFindings[0].CampaignID != before.RawFindings[0].CampaignID {
		t.Fatalf("campaign drift mutated before restart before=%#v after=%#v err=%v",
			before.RawFindings[0], after.RawFindings[0], err)
	}

	unconfirmed := fixture.request(t, "restart", `{"confirmed":false}`)
	if unconfirmed.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed restart status=%d body=%s", unconfirmed.Code, unconfirmed.Body.String())
	}
	confirmed := fixture.request(t, "restart", `{"confirmed":true}`)
	if confirmed.Code != http.StatusAccepted || !strings.Contains(
		confirmed.Body.String(), `"status":"replaying"`,
	) {
		t.Fatalf("confirmed restart status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
	restarted, _, err := fixture.store.Get(fixture.state.Repository)
	if err != nil || restarted.RawFindings[0].CampaignID != fixture.automation.CampaignID ||
		restarted.HistoricalDeduplication.ProfileSnapshot != fixture.snapshot {
		t.Fatalf("confirmed restart raw=%#v replay=%#v err=%v",
			restarted.RawFindings[0], restarted.HistoricalDeduplication, err)
	}
	version := restarted.Version
	repeated := fixture.request(t, "restart", `{"confirmed":true}`)
	replayed, _, replayErr := fixture.store.Get(fixture.state.Repository)
	if repeated.Code != http.StatusAccepted || replayErr != nil || replayed.Version != version {
		t.Fatalf("repeated restart status=%d versions=%d/%d err=%v body=%s",
			repeated.Code, version, replayed.Version, replayErr, repeated.Body.String())
	}
}

func TestRepositoryReviewHistoricalRestartDoesNotRecoverCampaignWhileModelWorkRuns(
	t *testing.T,
) {
	backfill := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0}, occurrences: 1,
	})
	state := backfill.state
	state.RawFindings = nil
	state.DeduplicationJobs = nil
	state.DeduplicatedFindings = nil
	state.MappingJobs = nil
	state.NextDeduplicationOrdinal = 0
	state.FindingsProcessing = repoaudit.FindingsProcessingCounters{}
	state.HistoricalDeduplication = repoaudit.HistoricalDeduplicationReplay{
		Required: true, Status: repoaudit.HistoricalDeduplicationPending,
		UpdatedAt: time.Now().UTC(),
	}
	state.Version++
	state.UpdatedAt = state.HistoricalDeduplication.UpdatedAt
	persistRepositoryReviewAdditionalCoverageState(t, backfill.workspace, state)
	frozen := repoaudit.HistoricalDeduplicationProfileSnapshot{
		ReviewerModel: "review-a", DeduplicationModel: "review-a",
		AccountRef:          backfill.automation.EffectiveAccountRef,
		SimilarityThreshold: 90, CandidateLimit: 4,
	}
	state, _, err := backfill.store.FreezeHistoricalDeduplicationReplay(
		state.Repository, frozen,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, admission, err := backfill.store.AdmitNextHistoricalDeduplicationBatch(
		state.Repository,
	)
	if err != nil || admission.Admitted != 1 {
		t.Fatalf("historical admission=%#v err=%v", admission, err)
	}
	state, _, claimed, err := backfill.store.ClaimDeduplicationJob(
		state.Repository, state.DeduplicationJobs[0].ID, time.Minute,
	)
	if err != nil || !claimed {
		t.Fatalf("claim historical job claimed=%v err=%v", claimed, err)
	}
	state, _, err = backfill.store.FailHistoricalDeduplicationReplay(state.Repository, "")
	if err != nil || repoaudit.HistoricalDeduplicationModelWorkQuiescent(state) {
		t.Fatalf("running historical failure replay=%#v err=%v",
			state.HistoricalDeduplication, err)
	}

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = backfill.workspace
	cfg.Agents.Defaults.ModelName = "review-a"
	cfg.Agents.Defaults.AccountRef = backfill.automation.EffectiveAccountRef
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: backfill.automation.EffectiveAccountRef,
		Provider:  "openai", Model: "openai/test", Enabled: true,
		APIKeys: config.SecureStrings{config.NewSecureString("test-api-key")},
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "review-a", Model: "gpt-review"}}
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatal(saveErr)
	}
	handler := NewHandler(configPath)
	t.Cleanup(handler.Shutdown)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	automationBefore, found, err := backfill.store.GetAutomation(
		t.Context(), backfill.automation.ID,
	)
	if err != nil || !found {
		t.Fatalf("automation before restart found=%v err=%v", found, err)
	}
	repositoryBefore, found, err := backfill.store.Get(state.Repository)
	if err != nil || !found {
		t.Fatalf("repository before restart found=%v err=%v", found, err)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/repository-reviews/automations/"+backfill.automation.ID+
			"/historical-deduplication/restart",
		strings.NewReader(`{"confirmed":true}`),
	)
	setRepositoryReviewMutationHeaders(request)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(
		response.Body.String(), `"code":"historical_deduplication_in_progress"`,
	) {
		t.Fatalf("running restart status=%d body=%s", response.Code, response.Body.String())
	}
	automationAfter, found, err := backfill.store.GetAutomation(
		t.Context(), backfill.automation.ID,
	)
	if err != nil || !found {
		t.Fatalf("automation after restart found=%v err=%v", found, err)
	}
	repositoryAfter, found, err := backfill.store.Get(state.Repository)
	if err != nil || !found {
		t.Fatalf("repository after restart found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(automationBefore, automationAfter) ||
		!reflect.DeepEqual(repositoryBefore, repositoryAfter) {
		t.Fatal("running historical restart mutated campaign authority before quiescence")
	}
}
