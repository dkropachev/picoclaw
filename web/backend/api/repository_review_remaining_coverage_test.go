package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type repositoryModelEvaluationGetHookStore struct {
	base      repositoryModelEvaluationStateStore
	beforeGet func()
}

func (s *repositoryModelEvaluationGetHookStore) Create(
	ctx context.Context,
	request repoeval.CreateRequest,
) (repoeval.Evaluation, error) {
	return s.base.Create(ctx, request)
}

func (s *repositoryModelEvaluationGetHookStore) Get(
	ctx context.Context,
	id string,
) (repoeval.Evaluation, bool, error) {
	if s.beforeGet != nil {
		hook := s.beforeGet
		s.beforeGet = nil
		hook()
	}
	return s.base.Get(ctx, id)
}

func (s *repositoryModelEvaluationGetHookStore) Update(
	ctx context.Context,
	id string,
	version int64,
	mutate func(*repoeval.Evaluation) error,
) (repoeval.Evaluation, error) {
	return s.base.Update(ctx, id, version, mutate)
}

type repositoryModelEvaluationFinalizerErrorStore struct {
	base     repositoryModelEvaluationStateStore
	getCalls int
	getErrAt int
	getErr   error
}

func (s *repositoryModelEvaluationFinalizerErrorStore) Create(
	ctx context.Context,
	request repoeval.CreateRequest,
) (repoeval.Evaluation, error) {
	return s.base.Create(ctx, request)
}

func (s *repositoryModelEvaluationFinalizerErrorStore) Get(
	ctx context.Context,
	id string,
) (repoeval.Evaluation, bool, error) {
	s.getCalls++
	if s.getCalls == s.getErrAt {
		return repoeval.Evaluation{}, false, s.getErr
	}
	return s.base.Get(ctx, id)
}

func (*repositoryModelEvaluationFinalizerErrorStore) Update(
	context.Context,
	string,
	int64,
	func(*repoeval.Evaluation) error,
) (repoeval.Evaluation, error) {
	return repoeval.Evaluation{}, repoeval.ErrConflict
}

func TestRepositoryReviewRemainingPureCoverage(t *testing.T) {
	profile := repoaudit.RepositoryReviewProfile{ReviewerModel: "reviewer"}
	for _, field := range []collectionquery.Field{"deduplicator", "issue_writer"} {
		value, ok := repositoryReviewProfileCollectionField(profile, field)
		if !ok || value.Text != "reviewer" {
			t.Fatalf("profile fallback field=%q value=%#v ok=%v", field, value, ok)
		}
	}

	finding := repoaudit.RepositoryFinding{
		ReviewFindingIDs: []string{"rdf_one", "rdf_two"},
		FoundCommits:     []string{"one", "two", "three"},
		PathSymbolHistory: []repoaudit.RepositoryFindingPathSymbol{
			{Path: "old.go", Symbol: "Old"},
			{Path: "new.go", Symbol: "New"},
		},
		Issue: repoaudit.RepositoryFindingIssueAssociation{
			ConflictURLs: []string{"https://example.invalid/conflict"},
		},
	}
	summary := repositoryReviewRepositoryFindingSummary(finding)
	if summary.OccurrenceCount != 2 || summary.FoundCommitCount != 3 ||
		len(summary.PathSymbolHistory) != 1 || summary.PathSymbolHistory[0].Path != "new.go" ||
		len(summary.ReviewFindingIDs) != 0 || len(summary.Issue.ConflictURLs) != 0 {
		t.Fatalf("repository finding summary=%#v", summary)
	}

	errorResponse := httptest.NewRecorder()
	writeRepositoryReviewAutomationError(errorResponse, repoaudit.ErrHistoricalDeduplicationInProgress)
	if errorResponse.Code != http.StatusConflict {
		t.Fatalf("historical automation error status=%d body=%s", errorResponse.Code, errorResponse.Body.String())
	}

	controller := newRepositoryReviewController(NewHandler(t.TempDir()))
	if _, err := controller.repositoryReviewDeduplicationSnapshot(testRepositoryReviewAutomation()); err == nil {
		t.Fatal("deduplication snapshot accepted a directory config path")
	}

	line := 10
	occurrence := repoaudit.Finding{
		ID: "rdf_detail", RepositoryFindingID: "rrf_detail", File: repoaudit.FileRef{Path: "pkg/core.go"},
		Line: &line, Severity: "high", Title: "Lost update", Status: repoaudit.FindingOpen,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	ledger := repositoryReviewAutomationLedger{
		Found:      true,
		Automation: repoaudit.RepositoryReviewAutomation{ID: "rra_detail", Repository: "owner/repo"},
		State: repoaudit.RepositoryState{
			Repository: "owner/repo", Findings: []repoaudit.Finding{occurrence},
			RepositoryFindings: []repoaudit.RepositoryFinding{{ID: "rrf_detail"}},
		},
	}
	detail := repositoryReviewFindingDetail(ledger, occurrence)
	if _, ok := detail["repository_finding"]; !ok {
		t.Fatalf("finding detail omitted repository aggregate: %#v", detail)
	}
}

func TestRepositoryReviewRemainingCollectionBranches(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	base := "/api/repository-reviews/automations/" + automation.ID

	for _, target := range []string{
		base + "/run-findings?query=(",
		base + "/run-findings?query=ALL&cursor=invalid",
		"/api/repository-reviews/automations/invalid/run-findings?query=ALL",
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code == http.StatusOK {
			t.Fatalf("invalid collection request succeeded: %s body=%s", target, response.Body.String())
		}
	}

	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/invalid/run-findings/rdf_missing",
		nil,
	))
	if missing.Code == http.StatusOK {
		t.Fatalf("invalid finding lookup succeeded: %s", missing.Body.String())
	}
}

func TestRepositoryReviewRemainingProfileValidationCoverage(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	if err := handler.validateRepositoryReviewProfileSelectionWithModels(
		"api", "cheap", "", "missing-deduplicator", repoaudit.RepositoryReviewBudgetPolicy{},
	); err == nil {
		t.Fatal("missing deduplication alias was accepted")
	}
	if err := handler.validateRepositoryReviewProfileSelectionWithModels(
		"api", "cheap", "missing-writer", "", repoaudit.RepositoryReviewBudgetPolicy{},
	); err == nil {
		t.Fatal("missing issue writer alias was accepted")
	}
	if !errors.Is(
		handler.validateRepositoryReviewProfileSelectionWithModels(
			"missing-account", "cheap", "", "", repoaudit.RepositoryReviewBudgetPolicy{},
		),
		repoaudit.ErrInvalidProfile,
	) {
		t.Fatal("missing profile account did not return an invalid-profile error")
	}
}

func TestRepositoryModelEvaluationRemainingCancellationCoverage(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	base := controller.store.(repoeval.Store)

	draft, err := base.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/idempotent-active"))
	if err != nil {
		t.Fatal(err)
	}
	token, activeCtx, activeCancel, err := controller.reserveActive(draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := base.Update(t.Context(), draft.ID, draft.Version, repositoryModelEvaluationApplyCancellation)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := controller.Cancel(t.Context(), draft.ID, draft.Version)
	if err != nil || replayed.Version != canceled.Version {
		t.Fatalf("active idempotent cancel=%#v err=%v", replayed, err)
	}
	select {
	case <-activeCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("idempotent cancellation did not cancel its active context")
	}
	activeCancel()
	controller.releaseActive(draft.ID, token)

	running := seedRunningRepositoryModelEvaluation(t, controller, base, "owner/token-recheck")
	oldToken, oldCtx, oldCancel, err := controller.reserveActive(running.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacementCtx, replacementCancel := context.WithCancel(controller.ctx)
	replacementToken := "replacement-token-before-get"
	controller.store = &repositoryModelEvaluationGetHookStore{
		base: base,
		beforeGet: func() {
			controller.mu.Lock()
			controller.active[running.ID] = repositoryModelEvaluationActiveRun{
				token: replacementToken, cancel: replacementCancel,
			}
			controller.mu.Unlock()
		},
	}
	if _, cancelErr := controller.Cancel(
		t.Context(), running.ID, running.Version,
	); !errors.Is(cancelErr, repoeval.ErrConflict) {
		t.Fatalf("replacement-before-get cancellation err=%v", cancelErr)
	}
	select {
	case <-oldCtx.Done():
		t.Fatal("stale cancellation canceled the old context")
	case <-replacementCtx.Done():
		t.Fatal("stale cancellation canceled the replacement context")
	default:
	}
	oldCancel()
	replacementCancel()
	controller.releaseActive(running.ID, replacementToken)
	_ = oldToken
	controller.store = base

	if applyErr := repositoryModelEvaluationApplyCancellation(
		&repoeval.Evaluation{Status: repoeval.StatusCompleted},
	); !errors.Is(applyErr, repoeval.ErrInvalidTransition) {
		t.Fatalf("completed cancellation mutation err=%v", applyErr)
	}

	cancelingDraft, err := base.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/finalizer-errors"))
	if err != nil {
		t.Fatal(err)
	}
	preflighting, err := base.Update(t.Context(), cancelingDraft.ID, cancelingDraft.Version, func(
		value *repoeval.Evaluation,
	) error {
		value.Status = repoeval.StatusPreflighting
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	canceling, err := base.Update(
		t.Context(), preflighting.ID, preflighting.Version, repositoryModelEvaluationApplyCancellation,
	)
	if err != nil {
		t.Fatal(err)
	}
	controller.store = &repositoryModelEvaluationFaultStore{base: base, conflicts: 32}
	if _, finishErr := controller.finishCanceled(t.Context(), canceling.ID); !errors.Is(
		finishErr, repoeval.ErrConflict,
	) {
		t.Fatalf("unsettled finalizer conflict err=%v", finishErr)
	}
	wantGetErr := errors.New("injected finalizer reload failure")
	controller.store = &repositoryModelEvaluationFinalizerErrorStore{
		base: base, getErrAt: 33, getErr: wantGetErr,
	}
	if _, finishErr := controller.finishCanceled(t.Context(), canceling.ID); !errors.Is(finishErr, wantGetErr) {
		t.Fatalf("finalizer reload err=%v", finishErr)
	}
	controller.store = base
}

func TestRepositoryModelEvaluationRemainingRequestCoverage(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/model-evaluations", strings.NewReader(`{} {}`))
	var target repositoryModelEvaluationActionRequest
	if err := decodeRepositoryModelEvaluationRequest(request, &target); err == nil {
		t.Fatal("model evaluation request accepted a trailing JSON value")
	}
}

func TestRepositoryReviewRemainingHistoricalLockCoverage(t *testing.T) {
	blockedRoot := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blockedRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store := repoaudit.NewStore(blockedRoot)
	snapshot := repoaudit.RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "reviewer", DeduplicationModel: "reviewer", AccountRef: "account",
		SimilarityThreshold: 90, CandidateLimit: 4,
	}
	groups := []repoaudit.HistoricalDeduplicationMergeGroup{{
		Members: []repoaudit.HistoricalDeduplicationFindingVersion{
			{ID: "rrf_lock_one", Version: 1},
			{ID: "rrf_lock_two", Version: 1},
		},
	}}
	assertError := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s unexpectedly acquired a blocked historical store", name)
		}
	}
	_, _, err := store.AdmitNextHistoricalDeduplicationBatch("owner/blocked")
	assertError("admit", err)
	_, _, err = store.FreezeHistoricalDeduplicationReplay("owner/blocked", snapshot)
	assertError("freeze", err)
	_, _, _, err = store.AcquireHistoricalDeduplicationMerge(
		"owner/blocked", "rhl_lock_coverage", groups,
	)
	assertError("acquire", err)
	_, _, err = store.CompleteHistoricalDeduplicationMerge("owner/blocked", "rhl_lock_coverage")
	assertError("complete", err)
	_, _, err = failHistoricalDeduplicationReplay(store, "owner/blocked", "rhl_lock_coverage")
	assertError("fail", err)
	_, _, err = store.RetryHistoricalDeduplicationReplay("owner/blocked")
	assertError("retry", err)
}

func TestRepositoryReviewRemainingBackfillAndResolverCoverage(t *testing.T) {
	fixture := newRepositoryReviewBackfillFixture(t, 1, repositoryReviewBackfillRunSpec{
		inspected: []int{0},
	})
	models := make([]string, 100)
	for index := range models {
		models[index] = fmt.Sprintf("reviewer-%03d", index)
	}
	profile := workflows.RepositoryReviewModelProfile{
		Revision: "oversized-reviewer-cohort", AccountRef: fixture.automation.EffectiveAccountRef,
		ReviewerModels: models, MaxContentBytes: int(fixture.automation.MaxContentBytes),
	}
	if _, err := prepareRepositoryReviewLegacyCampaignBackfill(
		t.Context(), fixture.automation, fixture.state,
		repoaudit.NewRepositoryReviewCampaignID(), fixture.runStore, profile,
	); err == nil {
		t.Fatal("legacy campaign accepted an oversized reviewer cohort")
	}

	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := newRepositoryReviewController(handler)
	if _, err := controller.resolveRepositoryReviewCampaignProfile(
		t.Context(), &config.Config{}, testRepositoryReviewAutomation(),
	); err == nil {
		t.Fatal("campaign profile resolver accepted an unusable runtime configuration")
	}
}

func TestRepositoryReviewRemainingHistoricalAdvanceCoverage(t *testing.T) {
	t.Run("failed raw", func(t *testing.T) {
		controller, _, state, workspace := repositoryReviewRemainingHistoricalAdvanceFixture(t, 4)
		now := time.Now().UTC()
		failure := &repoaudit.DeduplicationFailure{
			Code: "provider_failed", Message: "Deduplication failed.", Retryable: true, At: now,
		}
		state.RawFindings[0].State = repoaudit.RawFindingDeduplicationFailed
		state.RawFindings[0].Failure = failure
		state.RawFindings[0].Version++
		state.RawFindings[0].UpdatedAt = now
		state.RawFindings[0].History = append(state.RawFindings[0].History, repoaudit.RawFindingHistoryEntry{
			State:       repoaudit.RawFindingDeduplicationFailed,
			Disposition: repoaudit.RawFindingDispositionUndecided,
			Attempt:     repoaudit.DeduplicationAttemptLimit, Failure: failure, At: now,
		})
		state.DeduplicationJobs[0].State = repoaudit.DeduplicationJobFailed
		state.DeduplicationJobs[0].Attempts = repoaudit.DeduplicationAttemptLimit
		state.DeduplicationJobs[0].Failure = failure
		state.DeduplicationJobs[0].UpdatedAt = now
		state.DeduplicationJobs[0].History = append(
			state.DeduplicationJobs[0].History,
			repoaudit.DeduplicationJobHistoryEntry{
				State:   repoaudit.DeduplicationJobFailed,
				Attempt: repoaudit.DeduplicationAttemptLimit, Failure: failure, At: now,
			},
		)
		state.FindingsProcessing = repoaudit.FindingsProcessingCounters{
			RawTotal: 1, Failed: 1, UpdatedAt: now,
		}
		state.Version++
		state.UpdatedAt = now
		persistRepositoryReviewAdditionalCoverageState(t, workspace, state)
		if advanceErr := controller.advanceHistoricalFindingDeduplication(
			t.Context(), state, nil,
		); advanceErr == nil || !strings.Contains(advanceErr.Error(), "attempt limit") {
			t.Fatalf("failed historical advance raw=%#v jobs=%#v err=%v", state.RawFindings, state.DeduplicationJobs, advanceErr)
		}
	})

	t.Run("mapping not quiescent", func(t *testing.T) {
		controller, store, state, workspace := repositoryReviewRemainingHistoricalAdvanceFixture(t, 0)
		options := repoaudit.DeduplicationProcessOptions{
			ModelInputCeiling: repoaudit.DeduplicationMaximumInputBytes,
			LeaseDuration:     time.Hour,
		}
		if _, processErr := store.ProcessPendingDeduplicationJobs(
			t.Context(), state.Repository, options,
		); processErr != nil {
			t.Fatalf("historical mapping process raw=%#v jobs=%#v err=%v", state.RawFindings, state.DeduplicationJobs, processErr)
		}
		state, found, err := store.Get(state.Repository)
		if err != nil || !found || len(state.MappingJobs) == 0 {
			t.Fatalf("historical mapping state=%#v found=%v err=%v", state.MappingJobs, found, err)
		}
		now := time.Now().UTC()
		state.MappingJobs[0].State = repoaudit.RepositoryMappingRunning
		state.MappingJobs[0].Attempts = 1
		state.MappingJobs[0].ReservedAt = now
		state.MappingJobs[0].UpdatedAt = now
		state.Version++
		state.UpdatedAt = now
		persistRepositoryReviewAdditionalCoverageState(t, workspace, state)
		if advanceErr := controller.advanceHistoricalFindingDeduplication(
			t.Context(), state, nil,
		); !errors.Is(advanceErr, repoaudit.ErrHistoricalDeduplicationNotQuiescent) {
			t.Fatalf("non-quiescent historical advance err=%v", advanceErr)
		}
	})
}

func repositoryReviewRemainingHistoricalAdvanceFixture(
	t *testing.T,
	candidateLimit int,
) (*repositoryReviewController, repoaudit.Store, repoaudit.RepositoryState, string) {
	t.Helper()
	handler, _, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewAPIState(t, workspace)
	campaignID := repoaudit.NewRepositoryReviewCampaignID()
	commit := strings.Repeat("d", 40)
	state.Findings[0].CampaignID = campaignID
	state.Findings[0].CommitSHA = commit
	state.Findings[0].DeduplicationPending = false
	for index := range state.Contexts {
		state.Contexts[index].CampaignID = campaignID
		state.Contexts[index].CommitSHA = commit
	}
	state.Runs = nil
	state.RawFindings = nil
	state.DeduplicationJobs = nil
	state.DeduplicatedFindings = nil
	state.MappingJobs = nil
	state.FindingsProcessing = repoaudit.FindingsProcessingCounters{}
	state.NextDeduplicationOrdinal = 0
	state.FindingCount = len(state.Findings)
	state.OpenFindingCount = len(state.Findings)
	state.CampaignHistory = map[string]string{campaignID: commit}
	state.HistoricalDeduplication = repoaudit.HistoricalDeduplicationReplay{
		Required: true, Status: repoaudit.HistoricalDeduplicationPending,
		UpdatedAt: time.Now().UTC(),
	}
	persistRepositoryReviewAdditionalCoverageState(t, workspace, state)
	store := repoaudit.NewStore(workspace)
	snapshot := repoaudit.RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "cheap", DeduplicationModel: "cheap", AccountRef: "api",
		SimilarityThreshold: 90, CandidateLimit: candidateLimit,
	}
	state, _, err := store.FreezeHistoricalDeduplicationReplay(state.Repository, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state, admission, err := store.AdmitNextHistoricalDeduplicationBatch(state.Repository)
	if err != nil || admission.Admitted != 1 {
		t.Fatalf("historical fixture admission=%#v err=%v", admission, err)
	}
	cfg, err := config.LoadConfig(handler.configPath)
	if err != nil {
		t.Fatal(err)
	}
	controller := newRepositoryReviewController(handler)
	controller.startOnce.Do(func() {})
	controller.leasedStore = store
	controller.leasedConfig = cfg
	t.Cleanup(controller.Stop)
	return controller, store, state, workspace
}

func TestRepositoryReviewRemainingStartupReconciliationCoverage(t *testing.T) {
	original := reconcileRepositoryReviewDeduplicationJobs
	t.Cleanup(func() { reconcileRepositoryReviewDeduplicationJobs = original })
	wantErr := errors.New("injected deduplication reconciliation failure")
	reconcileRepositoryReviewDeduplicationJobs = func(
		repoaudit.Store, context.Context,
	) (int, error) {
		return 0, wantErr
	}
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	controller := newRepositoryReviewController(handler)
	if err := controller.Start(); !errors.Is(err, wantErr) {
		t.Fatalf("controller reconciliation error=%v", err)
	}
	if controller.ctx.Err() == nil || controller.releaseLease != nil {
		t.Fatalf("failed controller retained lifecycle state: %#v", controller)
	}
	controller.Stop()
}

func TestRepositoryReviewRemainingHistoricalMergeErrorCoverage(t *testing.T) {
	originalAdmit := admitNextHistoricalDeduplicationBatch
	originalGroups := historicalDeduplicationRepositoryMergeGroups
	originalAcquire := acquireHistoricalDeduplicationMerge
	originalComplete := completeHistoricalDeduplicationMerge
	originalFail := failHistoricalDeduplicationReplay
	t.Cleanup(func() {
		admitNextHistoricalDeduplicationBatch = originalAdmit
		historicalDeduplicationRepositoryMergeGroups = originalGroups
		acquireHistoricalDeduplicationMerge = originalAcquire
		completeHistoricalDeduplicationMerge = originalComplete
		failHistoricalDeduplicationReplay = originalFail
	})
	state := repoaudit.RepositoryState{
		Repository: "owner/historical-hooks",
		HistoricalDeduplication: repoaudit.HistoricalDeduplicationReplay{
			Required: true, Status: repoaudit.HistoricalDeduplicationReplaying,
		},
	}
	admitNextHistoricalDeduplicationBatch = func(
		repoaudit.Store, string,
	) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationAdmission, error) {
		return state, repoaudit.HistoricalDeduplicationAdmission{AllComplete: true}, nil
	}
	controller := newRepositoryReviewController(nil)

	historicalDeduplicationRepositoryMergeGroups = func(
		repoaudit.RepositoryState,
	) ([]repoaudit.HistoricalDeduplicationMergeGroup, error) {
		return nil, repoaudit.ErrHistoricalDeduplicationNotQuiescent
	}
	if err := controller.advanceHistoricalFindingDeduplication(t.Context(), state, nil); err != nil {
		t.Fatalf("not-quiescent merge error=%v", err)
	}

	wantGroupErr := errors.New("injected merge-group failure")
	historicalDeduplicationRepositoryMergeGroups = func(
		repoaudit.RepositoryState,
	) ([]repoaudit.HistoricalDeduplicationMergeGroup, error) {
		return nil, wantGroupErr
	}
	if err := controller.advanceHistoricalFindingDeduplication(
		t.Context(), state, nil,
	); !errors.Is(err, wantGroupErr) {
		t.Fatalf("merge-group error=%v", err)
	}

	historicalDeduplicationRepositoryMergeGroups = func(
		repoaudit.RepositoryState,
	) ([]repoaudit.HistoricalDeduplicationMergeGroup, error) {
		return nil, nil
	}
	wantAcquireErr := errors.New("injected merge acquisition failure")
	acquireHistoricalDeduplicationMerge = func(
		repoaudit.Store, string, string, []repoaudit.HistoricalDeduplicationMergeGroup,
	) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationReplay, bool, error) {
		return repoaudit.RepositoryState{}, repoaudit.HistoricalDeduplicationReplay{}, false, wantAcquireErr
	}
	if err := controller.advanceHistoricalFindingDeduplication(
		t.Context(), state, nil,
	); !errors.Is(err, wantAcquireErr) {
		t.Fatalf("merge acquisition error=%v", err)
	}

	acquireHistoricalDeduplicationMerge = func(
		repoaudit.Store, string, string, []repoaudit.HistoricalDeduplicationMergeGroup,
	) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationReplay, bool, error) {
		return state, repoaudit.HistoricalDeduplicationReplay{
			Required: true, Status: repoaudit.HistoricalDeduplicationMerging,
			MergeLease: repoaudit.HistoricalDeduplicationMergeLease{ID: "rhl_hook_lease"},
		}, true, nil
	}
	wantCompleteErr := errors.New("injected merge completion failure")
	wantFailErr := errors.New("injected merge failure-recording failure")
	completeHistoricalDeduplicationMerge = func(
		repoaudit.Store, string, string,
	) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationReplay, error) {
		return repoaudit.RepositoryState{}, repoaudit.HistoricalDeduplicationReplay{}, wantCompleteErr
	}
	failHistoricalDeduplicationReplay = func(
		repoaudit.Store, string, string,
	) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationReplay, error) {
		return repoaudit.RepositoryState{}, repoaudit.HistoricalDeduplicationReplay{}, wantFailErr
	}
	err := controller.advanceHistoricalFindingDeduplication(t.Context(), state, nil)
	if !errors.Is(err, wantCompleteErr) || !errors.Is(err, wantFailErr) {
		t.Fatalf("merge completion error=%v", err)
	}
}

func TestRepositoryReviewRemainingInjectedControllerBranches(t *testing.T) {
	originalRuntime := repositoryReviewCampaignWorkflowRuntime
	originalPause := applyRepositoryReviewPause
	t.Cleanup(func() {
		repositoryReviewCampaignWorkflowRuntime = originalRuntime
		applyRepositoryReviewPause = originalPause
	})
	wantRuntimeErr := errors.New("injected campaign runtime failure")
	repositoryReviewCampaignWorkflowRuntime = func(
		*repositoryReviewController,
		context.Context,
		*config.Config,
	) (*config.Config, *workflows.FileRunStore, *workflows.Executor, error) {
		return nil, nil, nil, wantRuntimeErr
	}
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	controller := newRepositoryReviewController(handler)
	if _, err := controller.resolveRepositoryReviewCampaignProfile(
		t.Context(), config.DefaultConfig(), testRepositoryReviewAutomation(),
	); !errors.Is(err, wantRuntimeErr) {
		t.Fatalf("campaign runtime error=%v", err)
	}

	store, err := handler.repositoryReviewStore()
	if err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), testRepositoryReviewAutomation())
	if err != nil {
		t.Fatal(err)
	}
	applyRepositoryReviewPause = func(
		*repoaudit.RepositoryReviewAutomation, int64, string,
	) error {
		return errRepositoryReviewPauseSettled
	}
	controller.leasedStore = store
	settled, err := controller.pauseAutomationForRun(
		t.Context(), automation.ID, automation.Version, "",
	)
	if err != nil || settled.ID != automation.ID {
		t.Fatalf("settled pause=%#v err=%v", settled, err)
	}
}

func TestRepositoryReviewRemainingLegacyRepositoryPageCoverage(t *testing.T) {
	handler, mux, workspace := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	state := seedRepositoryReviewGenerationFindings(t, workspace, 2)
	state = completeRepositoryReviewAPIMappingJobs(t, workspace, state)
	if len(state.RepositoryFindings) != 2 {
		t.Fatalf("repository findings=%d", len(state.RepositoryFindings))
	}
	automation := seedRepositoryReviewDetailAutomation(t, handler, state.Repository, state.Runs[0].ID)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/repository-reviews/automations/"+automation.ID+"/report?scope=all&offset=0&limit=1",
		nil,
	))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"next_repository_finding_offset":1`) {
		t.Fatalf("legacy repository page status=%d body=%s", response.Code, response.Body.String())
	}
}
