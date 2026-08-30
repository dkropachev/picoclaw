package prworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type failAutomaticMutationStore struct {
	Store
	remaining int
}

func (store *failAutomaticMutationStore) Mutate(ctx context.Context, mutation Mutation) (MutationResult, error) {
	if store.remaining > 0 && len(mutation.Patch.AppendPublications) > 0 {
		store.remaining--
		current, _ := store.Store.Get(ctx, mutation.WorkspaceID)
		return MutationResult{Aggregate: current}, errors.New("automatic deferred mutation unavailable")
	}
	return store.Store.Mutate(ctx, mutation)
}

type failNthGetStore struct {
	Store
	failAt int
	gets   int
}

func (store *failNthGetStore) Get(ctx context.Context, workspaceID string) (Aggregate, error) {
	store.gets++
	if store.gets == store.failAt {
		return Aggregate{}, errors.New("automatic deferred dependency unavailable")
	}
	return store.Store.Get(ctx, workspaceID)
}

type countingReviewPublisher struct {
	calls  int
	result ReviewPublicationResult
	err    error
}

func (publisher *countingReviewPublisher) PublishReview(
	context.Context,
	ReviewPublicationRequest,
) (ReviewPublicationResult, error) {
	publisher.calls++
	return publisher.result, publisher.err
}

func (*countingReviewPublisher) ReconcileReview(
	context.Context,
	ReviewPublicationRequest,
) (ReviewPublicationResult, bool, error) {
	return ReviewPublicationResult{}, false, nil
}

type countingIssuePublisher struct {
	calls  int
	result IssuePublicationResult
	err    error
	found  bool
}

func (publisher *countingIssuePublisher) CreateIssue(
	context.Context,
	IssuePublicationRequest,
) (IssuePublicationResult, error) {
	publisher.calls++
	return publisher.result, publisher.err
}

func (publisher *countingIssuePublisher) FindIssueByMarker(
	context.Context,
	string,
	string,
	string,
	string,
) (IssuePublicationResult, bool, error) {
	return publisher.result, publisher.found, publisher.err
}

type reconcileWaitingGates struct{}

func (reconcileWaitingGates) Start(_ context.Context, request GateRequest) (GateRun, error) {
	gate := testSucceededGate(request)
	if request.DecisionPoint == "pr.publication.reconcile" {
		gate = testWaitingGateWithActions(request, "recheck-provider", "assume-failed")
	}
	return gate, nil
}

func (reconcileWaitingGates) Respond(_ context.Context, gate GateRun, fieldValues map[string]any) (GateRun, error) {
	return answerTestGate(gate, fieldValues), nil
}

func publicationTestService(
	t *testing.T,
	mode DeferredIssueMode,
	gates GateEvaluator,
	now time.Time,
) (*Service, Aggregate) {
	t.Helper()
	store := NewMemoryStore()
	input := testCreateInput()
	input.Provider.State = "open"
	input.Provider.CanReview, input.Provider.CanCreateIssue, input.Provider.HeadWritable = true, true, true
	created, err := store.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	charter := Charter{
		ID: "pcr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Type: PRTypeFix, Goal: "fix retry",
		BaseSHA: input.Provider.BaseSHA, HeadSHA: input.Provider.HeadSHA, Confirmed: true, CreatedAt: now,
	}
	stage := StageRun{
		ID:         "psr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Stage:      "review",
		State:      ExecutionSucceeded,
		CharterID:  charter.ID,
		HeadSHA:    charter.HeadSHA,
		Attempt:    1,
		Summary:    "retry review",
		StartedAt:  now,
		FinishedAt: &now,
	}
	inScope := Finding{
		ID:          "pfn_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Fingerprint: "sha256:in-scope",
		Origin:      FindingOriginReview,
		OriginRunID: stage.ID,
		Title:       "retry",
		Message:     "fix retry",
		Scope: ScopeAssessment{
			Distance:       ScopeExact,
			Size:           ChangeSizeXS,
			Presence:       WorkCandidatePresent,
			TypeCompatible: true,
			Confidence:     1,
		},
		Disposition: FindingInScope,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	deferred := Finding{
		ID:          "pfn_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Fingerprint: "sha256:deferred",
		Origin:      FindingOriginReview,
		OriginRunID: stage.ID,
		Title:       "follow-up",
		Message:     "later",
		Scope: ScopeAssessment{
			Distance:       ScopeRelatedFollowup,
			Size:           ChangeSizeS,
			Presence:       WorkFollowUp,
			TypeCompatible: true,
			Confidence:     1,
		},
		Disposition: FindingDeferred,
		Version:     1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	group := DeferredGroup{
		ID: "pdg_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "Follow-up", Body: "Track later",
		FindingIDs: []string{deferred.ID}, Scope: deferred.Scope, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	active := charter.ID
	seeded, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-publication-seed", Patch: AggregatePatch{
			ActiveCharterID: &active, AppendCharters: []Charter{charter}, AppendStageRuns: []StageRun{stage},
			UpsertFindings: []Finding{inScope, deferred}, UpsertDeferred: []DeferredGroup{group},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(
		ServiceConfig{Store: store, Gates: gates, DeferredIssueMode: mode, Now: func() time.Time { return now }},
	)
	if err != nil {
		t.Fatal(err)
	}
	return service, seeded.Aggregate
}

func TestReviewPublicationRequestReplayPrecedesVersionFence(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAsk, passingGates{}, now)
	request := QueueReviewPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-review-replay", ExpectedHeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		FindingIDs: []string{aggregate.Findings[0].ID},
	}
	queued, err := service.QueueReviewPublication(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.QueueReviewPublication(context.Background(), request)
	if err != nil || replayed.Workspace.Version != queued.Workspace.Version || len(replayed.Publications) != 1 {
		t.Fatalf("replay = %#v err=%v", replayed.Publications, err)
	}
}

func TestTamperedPublicationNeverInvokesPublisher(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAsk, passingGates{}, now)
	queued, err := service.QueueReviewPublication(context.Background(), QueueReviewPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-review-tamper", ExpectedHeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	publication := queued.Publications[0]
	publication.payload = append([]byte(nil), publication.payload...)
	publication.payload[len(publication.payload)-1] ^= 1
	tampered, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID:     queued.Workspace.ID,
		ExpectedVersion: queued.Workspace.Version,
		RequestID:       "request-review-tamper-store",
		Patch:           AggregatePatch{ReplacePublications: []Publication{publication}},
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher := &countingReviewPublisher{}
	_, err = service.DispatchReviewPublication(context.Background(), publisher, DispatchPhasePublicationRequest{
		WorkspaceID: tampered.Aggregate.Workspace.ID, PublicationID: publication.ID,
		ExpectedVersion: tampered.Aggregate.Workspace.Version, RequestID: "request-review-tamper-dispatch",
	})
	if err == nil || publisher.calls != 0 {
		t.Fatalf("tampered dispatch err=%v calls=%d", err, publisher.calls)
	}
}

func TestStaleHeadQueueIsTerminalWithoutProviderCall(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAsk, passingGates{}, now)
	queued, err := service.QueueReviewPublication(context.Background(), QueueReviewPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-review-stale", ExpectedHeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := queued.ProviderSnapshot
	provider.HeadSHA = "different-head"
	advanced, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: queued.Workspace.ID, ExpectedVersion: queued.Workspace.Version,
		RequestID: "request-review-stale-head", Patch: AggregatePatch{Provider: &provider},
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher := &countingReviewPublisher{}
	staled, err := service.DispatchReviewPublication(context.Background(), publisher, DispatchPhasePublicationRequest{
		WorkspaceID: advanced.Aggregate.Workspace.ID, PublicationID: queued.Publications[0].ID,
		ExpectedVersion: advanced.Aggregate.Workspace.Version, RequestID: "request-review-stale-dispatch",
	})
	if err != nil || publisher.calls != 0 || staled.Publications[0].State != ExecutionStale {
		t.Fatalf("stale dispatch state=%q calls=%d err=%v", staled.Publications[0].State, publisher.calls, err)
	}
}

func TestCrossRepositoryPublicationURLBecomesUnknown(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAsk, passingGates{}, now)
	queued, err := service.QueueReviewPublication(context.Background(), QueueReviewPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-review-url", ExpectedHeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher := &countingReviewPublisher{result: ReviewPublicationResult{
		ExternalID: "77", ExternalURL: "https://github.com/other/repo/pull/9#pullrequestreview-77",
	}}
	result, err := service.DispatchReviewPublication(context.Background(), publisher, DispatchPhasePublicationRequest{
		WorkspaceID: queued.Workspace.ID, PublicationID: queued.Publications[0].ID,
		ExpectedVersion: queued.Workspace.Version, RequestID: "request-review-url-dispatch",
	})
	if err != nil || publisher.calls != 1 || result.Publications[0].State != ExecutionUnknown {
		t.Fatalf("cross-repo result = %#v calls=%d err=%v", result.Publications, publisher.calls, err)
	}
}

func TestDeferredModeOffCancelsPreviouslyQueuedIssue(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	ask, aggregate := publicationTestService(t, DeferredIssuesAsk, passingGates{}, now)
	queued, err := ask.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, GroupID: aggregate.DeferredGroups[0].ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-issue-off",
	})
	if err != nil {
		t.Fatal(err)
	}
	off, err := NewService(
		ServiceConfig{Store: ask.store, DeferredIssueMode: DeferredIssuesOff, Now: func() time.Time { return now }},
	)
	if err != nil {
		t.Fatal(err)
	}
	publisher := &countingIssuePublisher{}
	canceled, err := off.DispatchIssuePublication(context.Background(), publisher, DispatchIssuePublicationRequest{
		WorkspaceID: queued.Workspace.ID, PublicationID: queued.Publications[0].ID,
		ExpectedVersion: queued.Workspace.Version, RequestID: "request-issue-off-dispatch",
	})
	if err != nil || publisher.calls != 0 || canceled.Publications[0].State != ExecutionCanceled ||
		canceled.DeferredGroups[0].PublicationID != "" {
		t.Fatalf(
			"off cancellation = %#v groups=%#v calls=%d err=%v",
			canceled.Publications,
			canceled.DeferredGroups,
			publisher.calls,
			err,
		)
	}
}

func TestAutomaticDeferredModeUsesExplicitImmediatePolicy(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAutomatic, reconcileWaitingGates{}, now)
	queued, err := service.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, GroupID: aggregate.DeferredGroups[0].ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-issue-auto",
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Publications[0].State != ExecutionQueued || len(queued.Gates) != 1 ||
		!gateCompletedWith(queued.Gates[0], "publish") || queued.Gates[0].Turns[0].ActionRevision == "" ||
		queued.Gates[0].Turns[0].GateForm == nil {
		t.Fatalf("automatic publication = pubs %#v gates %#v", queued.Publications, queued.Gates)
	}
	replayed, err := service.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, GroupID: aggregate.DeferredGroups[0].ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-issue-auto",
	})
	if err != nil || replayed.Workspace.Version != queued.Workspace.Version {
		t.Fatalf("issue replay version=%d want=%d err=%v", replayed.Workspace.Version, queued.Workspace.Version, err)
	}
}

func TestIssueDispatchFailureIsRetryableOnlyWhenOutcomeIsDefinite(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	t.Run("definite failure releases the group for retry", func(t *testing.T) {
		service, aggregate := publicationTestService(t, DeferredIssuesAutomatic, passingGates{}, now)
		queued, err := service.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
			WorkspaceID: aggregate.Workspace.ID, GroupID: aggregate.DeferredGroups[0].ID,
			ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-issue-definite",
		})
		if err != nil {
			t.Fatal(err)
		}
		failed, err := service.DispatchIssuePublication(
			context.Background(),
			&countingIssuePublisher{err: errors.New("request rejected before dispatch")},
			DispatchIssuePublicationRequest{
				WorkspaceID: queued.Workspace.ID, PublicationID: queued.Publications[0].ID,
				ExpectedVersion: queued.Workspace.Version, RequestID: "request-issue-definite-dispatch",
			},
		)
		if err == nil || failed.Publications[0].State != ExecutionFailed ||
			failed.Publications[0].PublicErrorCode != "provider_issue_create_failed" ||
			failed.DeferredGroups[0].PublicationID != "" {
			t.Fatalf("definite failure pubs=%#v groups=%#v err=%v", failed.Publications, failed.DeferredGroups, err)
		}
		retried, err := service.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
			WorkspaceID: failed.Workspace.ID, GroupID: failed.DeferredGroups[0].ID,
			ExpectedVersion: failed.Workspace.Version, RequestID: "request-issue-definite-retry",
		})
		if err != nil || len(retried.Publications) != 2 ||
			retried.DeferredGroups[0].PublicationID == "" ||
			retried.DeferredGroups[0].PublicationID == failed.Publications[0].ID {
			t.Fatalf("retry pubs=%#v groups=%#v err=%v", retried.Publications, retried.DeferredGroups, err)
		}
	})

	t.Run("ambiguous failure remains locked for reconciliation", func(t *testing.T) {
		service, aggregate := publicationTestService(t, DeferredIssuesAutomatic, passingGates{}, now)
		queued, err := service.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
			WorkspaceID: aggregate.Workspace.ID, GroupID: aggregate.DeferredGroups[0].ID,
			ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-issue-ambiguous",
		})
		if err != nil {
			t.Fatal(err)
		}
		unknown, err := service.DispatchIssuePublication(
			context.Background(),
			&countingIssuePublisher{
				result: IssuePublicationResult{Ambiguous: true},
				err:    errors.New("transport failed after dispatch may have started"),
			},
			DispatchIssuePublicationRequest{
				WorkspaceID: queued.Workspace.ID, PublicationID: queued.Publications[0].ID,
				ExpectedVersion: queued.Workspace.Version, RequestID: "request-issue-ambiguous-dispatch",
			},
		)
		if err == nil || unknown.Publications[0].State != ExecutionUnknown ||
			unknown.Publications[0].PublicErrorCode != "provider_outcome_unknown" ||
			unknown.DeferredGroups[0].PublicationID != unknown.Publications[0].ID {
			t.Fatalf("ambiguous failure pubs=%#v groups=%#v err=%v", unknown.Publications, unknown.DeferredGroups, err)
		}
		if _, retryErr := service.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
			WorkspaceID: unknown.Workspace.ID, GroupID: unknown.DeferredGroups[0].ID,
			ExpectedVersion: unknown.Workspace.Version, RequestID: "request-issue-ambiguous-retry",
		}); !errors.Is(retryErr, ErrConflict) {
			t.Fatalf("ambiguous retry error = %v, want ErrConflict", retryErr)
		}
	})
}

func TestAutomaticDeferredPolicyQueuesEligibleGroupsAndHonorsSuppression(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAutomatic, passingGates{}, now)
	handler, err := NewHTTPHandler(HTTPConfig{
		Service: service, IssuePublisher: &countingIssuePublisher{},
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := handler.applyDeferredIssuePolicy(
		httptest.NewRequest("POST", "/runtime/eventing/pr-workspaces", nil).Context(),
		aggregate, "request-auto-policy",
	)
	if err != nil || queued.DeferredGroups[0].PublicationID == "" || queued.Publications[0].State != ExecutionQueued {
		t.Fatalf("automatic policy = groups %#v pubs %#v err=%v", queued.DeferredGroups, queued.Publications, err)
	}

	service2, aggregate2 := publicationTestService(t, DeferredIssuesAutomatic, passingGates{}, now)
	group := aggregate2.DeferredGroups[0]
	group.PublicationSuppressed, group.SuppressionReason = true, "publication_gate_block"
	suppressed, err := service2.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate2.Workspace.ID, ExpectedVersion: aggregate2.Workspace.Version,
		RequestID: "request-auto-policy-suppress", Patch: AggregatePatch{UpsertDeferred: []DeferredGroup{group}},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler2, _ := NewHTTPHandler(HTTPConfig{
		Service: service2, IssuePublisher: &countingIssuePublisher{},
	})
	unchanged, err := handler2.applyDeferredIssuePolicy(
		httptest.NewRequest("POST", "/runtime/eventing/pr-workspaces", nil).Context(),
		suppressed.Aggregate, "request-auto-policy-suppressed",
	)
	if err != nil || len(unchanged.Publications) != 0 || !unchanged.DeferredGroups[0].PublicationSuppressed {
		t.Fatalf(
			"suppressed automatic policy = groups %#v pubs %#v err=%v",
			unchanged.DeferredGroups,
			unchanged.Publications,
			err,
		)
	}
}

func TestAutomaticDeferredPolicyFailureSurfacesAndPolicyRetryQueuesIt(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAutomatic, passingGates{}, now)
	service.store = &failAutomaticMutationStore{Store: service.store, remaining: 1}
	handler, handlerErr := NewHTTPHandler(HTTPConfig{
		Service: service, IssuePublisher: &countingIssuePublisher{},
	})
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	body, marshalErr := json.Marshal(map[string]any{
		"expected_version": aggregate.Workspace.Version,
		"request_id":       "request-auto-policy-http-retry",
		"disposition":      FindingDeferred,
		"reason":           "keep this as follow-up work",
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	path := RuntimeRoutePrefix + "/" + aggregate.Workspace.ID + "/findings/" + aggregate.DeferredGroups[0].FindingIDs[0] + "/disposition"
	request := func() *http.Request {
		value := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		value.Header.Set("Content-Type", "application/json")
		return value
	}
	failed := httptest.NewRecorder()
	handler.ServeHTTP(failed, request())
	if failed.Code != http.StatusInternalServerError {
		t.Fatalf("automatic policy failure status = %d body=%s", failed.Code, failed.Body.String())
	}
	var failure struct {
		Current *Aggregate `json:"current"`
	}
	if err := json.Unmarshal(failed.Body.Bytes(), &failure); err != nil || failure.Current == nil {
		t.Fatalf("automatic policy failure omitted retained aggregate: body=%s err=%v", failed.Body.String(), err)
	}
	current := *failure.Current
	if current.Workspace.Version != aggregate.Workspace.Version+1 || len(current.Publications) != 0 {
		t.Fatalf("primary mutation was not retained independently: %#v", current)
	}
	persisted, getErr := service.Get(context.Background(), aggregate.Workspace.ID)
	if getErr != nil || persisted.Workspace.Version != current.Workspace.Version {
		t.Fatalf("retained response does not match store: version=%d err=%v", persisted.Workspace.Version, getErr)
	}
	retryBody, retryMarshalErr := json.Marshal(map[string]any{
		"expected_version": current.Workspace.Version,
		"request_id":       "request-auto-policy-sync-retry",
	})
	if retryMarshalErr != nil {
		t.Fatal(retryMarshalErr)
	}
	retry := httptest.NewRequest(
		http.MethodPost,
		RuntimeRoutePrefix+"/"+aggregate.Workspace.ID+"/deferred-groups/automatic-sync",
		bytes.NewReader(retryBody),
	)
	retry.Header.Set("Content-Type", "application/json")
	retried := httptest.NewRecorder()
	handler.ServeHTTP(retried, retry)
	if retried.Code != http.StatusOK {
		t.Fatalf("automatic sync retry status = %d body=%s", retried.Code, retried.Body.String())
	}
	var result Aggregate
	if err := json.Unmarshal(retried.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Publications) != 1 || result.Publications[0].State != ExecutionQueued ||
		result.DeferredGroups[0].PublicationID != result.Publications[0].ID {
		t.Fatalf(
			"automatic retry did not queue publication: groups=%#v publications=%#v",
			result.DeferredGroups,
			result.Publications,
		)
	}
}

func TestAutomaticDeferredSyncPreservesCurrentOnPremutationRegroupFailure(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAutomatic, passingGates{}, now)
	ungrouped := aggregate.Findings[1]
	ungrouped.ID = "pfn_cccccccccccccccccccccccccccccccc"
	ungrouped.Fingerprint = "sha256:ungrouped-deferred"
	ungrouped.Title = "ungrouped follow-up"
	seeded, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-auto-regroup-failure-seed",
		Patch:     AggregatePatch{UpsertFindings: []Finding{ungrouped}},
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate = seeded.Aggregate
	handler, err := NewHTTPHandler(HTTPConfig{
		Service: service, IssuePublisher: &countingIssuePublisher{},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"expected_version": aggregate.Workspace.Version,
		"request_id":       "request-auto-regroup-failure",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		RuntimeRoutePrefix+"/"+aggregate.Workspace.ID+"/deferred-groups/automatic-sync",
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("automatic regroup failure status = %d body=%s", response.Code, response.Body.String())
	}
	var failure struct {
		Current *Aggregate `json:"current"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || failure.Current == nil {
		t.Fatalf("automatic regroup failure omitted current: body=%s err=%v", response.Body.String(), err)
	}
	if failure.Current.Workspace.Version != aggregate.Workspace.Version ||
		failure.Current.Workspace.ID != aggregate.Workspace.ID {
		t.Fatalf("automatic regroup failure current = %#v", failure.Current.Workspace)
	}
}

func TestAutomaticDeferredSyncReloadsPartialMultiGroupSuccess(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAutomatic, passingGates{}, now)
	secondFinding := aggregate.Findings[1]
	secondFinding.ID = "pfn_cccccccccccccccccccccccccccccccc"
	secondFinding.Fingerprint = "sha256:second-deferred"
	secondFinding.Title = "second follow-up"
	secondGroup := aggregate.DeferredGroups[0]
	secondGroup.ID = "pdg_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	secondGroup.Title = "Second follow-up"
	secondGroup.FindingIDs = []string{secondFinding.ID}
	seeded, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-auto-partial-seed",
		Patch: AggregatePatch{
			UpsertFindings: []Finding{secondFinding},
			UpsertDeferred: []DeferredGroup{secondGroup},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate = seeded.Aggregate
	// The route first resolves the workspace intent before entering the
	// mutation; fail the third operation-internal reload after that fence read.
	service.store = &failNthGetStore{Store: service.store, failAt: 4}
	handler, err := NewHTTPHandler(HTTPConfig{
		Service: service, IssuePublisher: &countingIssuePublisher{},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"expected_version": aggregate.Workspace.Version,
		"request_id":       "request-auto-partial-failure",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		RuntimeRoutePrefix+"/"+aggregate.Workspace.ID+"/deferred-groups/automatic-sync",
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("automatic partial failure status = %d body=%s", response.Code, response.Body.String())
	}
	var failure struct {
		Current *Aggregate `json:"current"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || failure.Current == nil {
		t.Fatalf("automatic partial failure omitted current: body=%s err=%v", response.Body.String(), err)
	}
	current := *failure.Current
	if current.Workspace.Version != aggregate.Workspace.Version+1 || len(current.Publications) != 1 {
		t.Fatalf(
			"automatic partial failure hid committed progress: version=%d publications=%#v",
			current.Workspace.Version,
			current.Publications,
		)
	}
	if current.DeferredGroups[0].PublicationID == "" || current.DeferredGroups[1].PublicationID != "" {
		t.Fatalf("automatic partial group state = %#v", current.DeferredGroups)
	}
}

func TestDeferredGateRejectionSuppressesAutomaticRequeue(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAsk, nil, now)
	waiting, err := service.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, GroupID: aggregate.DeferredGroups[0].ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-issue-reject",
	})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     waiting.Workspace.ID,
		GateRunID:       waiting.Gates[0].ID,
		ExpectedVersion: waiting.Workspace.Version,
		RequestID:       "request-issue-reject-gate",
		FieldValues:     map[string]any{"action": "stop"},
	})
	if err != nil || !rejected.DeferredGroups[0].PublicationSuppressed ||
		rejected.DeferredGroups[0].PublicationID != "" {
		t.Fatalf("rejected group = %#v err=%v", rejected.DeferredGroups[0], err)
	}
}

func TestInterruptedIssueAbsenceBecomesUnknownAndHumanCanAssumeFailed(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAsk, reconcileWaitingGates{}, now)
	queued, err := service.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, GroupID: aggregate.DeferredGroups[0].ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-issue-unknown",
	})
	if err != nil {
		t.Fatal(err)
	}
	publication := queued.Publications[0]
	publication.State, publication.Attempts, publication.UpdatedAt = ExecutionRunning, 1, now.Add(-3*time.Minute)
	running, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: queued.Workspace.ID, ExpectedVersion: queued.Workspace.Version,
		RequestID: "request-issue-running", Patch: AggregatePatch{ReplacePublications: []Publication{publication}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := service.ReconcileIssuePublication(
		context.Background(),
		&countingIssuePublisher{},
		ReconcileIssuePublicationRequest{
			WorkspaceID: running.Aggregate.Workspace.ID, PublicationID: publication.ID,
			ExpectedVersion: running.Aggregate.Workspace.Version, RequestID: "request-issue-recover",
		},
	)
	if err == nil || unknown.Publications[0].State != ExecutionUnknown {
		t.Fatalf("recovery = %#v err=%v", unknown.Publications, err)
	}
	waiting, err := service.ReconcileIssuePublication(
		context.Background(),
		&countingIssuePublisher{},
		ReconcileIssuePublicationRequest{
			WorkspaceID: unknown.Workspace.ID, PublicationID: publication.ID,
			ExpectedVersion: unknown.Workspace.Version, RequestID: "request-issue-reconcile-gate",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	gate := waiting.Gates[len(waiting.Gates)-1]
	resolved, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID: waiting.Workspace.ID, GateRunID: gate.ID, ExpectedVersion: waiting.Workspace.Version,
		RequestID: "request-issue-assume-failed", FieldValues: map[string]any{"action": "assume-failed"},
	})
	if err != nil || resolved.Publications[0].State != ExecutionFailed ||
		resolved.DeferredGroups[0].PublicationID != "" {
		t.Fatalf("human resolution pubs=%#v groups=%#v err=%v", resolved.Publications, resolved.DeferredGroups, err)
	}
}

func TestIssueProviderErrorDuringRecoveryBecomesUnknown(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAsk, passingGates{}, now)
	queued, err := service.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, GroupID: aggregate.DeferredGroups[0].ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-issue-error",
	})
	if err != nil {
		t.Fatal(err)
	}
	publication := queued.Publications[0]
	publication.State, publication.UpdatedAt = ExecutionRunning, now.Add(-3*time.Minute)
	running, _ := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID:     queued.Workspace.ID,
		ExpectedVersion: queued.Workspace.Version,
		RequestID:       "request-issue-error-running",
		Patch:           AggregatePatch{ReplacePublications: []Publication{publication}},
	})
	result, err := service.ReconcileIssuePublication(
		context.Background(),
		&countingIssuePublisher{err: errors.New("provider unavailable")},
		ReconcileIssuePublicationRequest{
			WorkspaceID: running.Aggregate.Workspace.ID, PublicationID: publication.ID,
			ExpectedVersion: running.Aggregate.Workspace.Version, RequestID: "request-issue-error-reconcile",
		},
	)
	if err == nil || result.Publications[0].State != ExecutionUnknown {
		t.Fatalf("provider error recovery = %#v err=%v", result.Publications, err)
	}
}

func TestIssueReconciliationRejectsReviseAndDeferWithoutStrandingPublication(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAsk, reconcileWaitingGates{}, now)
	queued, err := service.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, GroupID: aggregate.DeferredGroups[0].ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-issue-reconcile-actions-queue",
	})
	if err != nil {
		t.Fatal(err)
	}
	unknown := seedUnknownPublication(
		t,
		service,
		queued,
		queued.Publications[0],
		"request-issue-reconcile-actions-unknown",
	)
	waiting, err := service.ReconcileIssuePublication(
		context.Background(),
		&countingIssuePublisher{},
		ReconcileIssuePublicationRequest{
			WorkspaceID: unknown.Workspace.ID, PublicationID: unknown.Publications[0].ID,
			ExpectedVersion: unknown.Workspace.Version, RequestID: "request-issue-reconcile-actions-gate",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved := rejectUnsupportedReconciliationOutcomesThenBlock(
		t,
		service,
		waiting,
		unknown.Publications[0].ID,
		"issue",
	)
	if resolved.DeferredGroups[0].PublicationID != "" {
		t.Fatalf("assume-failed issue did not unlock deferred group: %#v", resolved.DeferredGroups[0])
	}
	requeued, err := service.QueueDeferredPublication(context.Background(), QueueDeferredPublicationRequest{
		WorkspaceID: resolved.Workspace.ID, GroupID: resolved.DeferredGroups[0].ID,
		ExpectedVersion: resolved.Workspace.Version, RequestID: "request-issue-reconcile-actions-requeue",
	})
	if err != nil || requeued.Publications[len(requeued.Publications)-1].State == ExecutionFailed {
		t.Fatalf("issue publication remained stranded: pubs=%#v err=%v", requeued.Publications, err)
	}
}

func TestReviewReconciliationRejectsReviseAndDeferWithoutStrandingPublication(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	service, aggregate := publicationTestService(t, DeferredIssuesAsk, reconcileWaitingGates{}, now)
	queued, err := service.QueueReviewPublication(context.Background(), QueueReviewPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-review-reconcile-actions-queue", ExpectedHeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	unknown := seedUnknownPublication(
		t,
		service,
		queued,
		queued.Publications[0],
		"request-review-reconcile-actions-unknown",
	)
	waiting, err := service.ReconcilePhasePublication(
		context.Background(),
		&countingReviewPublisher{},
		nil,
		ReconcilePhasePublicationRequest{
			WorkspaceID: unknown.Workspace.ID, PublicationID: unknown.Publications[0].ID,
			ExpectedVersion: unknown.Workspace.Version, RequestID: "request-review-reconcile-actions-gate",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved := rejectUnsupportedReconciliationOutcomesThenBlock(
		t,
		service,
		waiting,
		unknown.Publications[0].ID,
		"review",
	)
	requeued, err := service.QueueReviewPublication(context.Background(), QueueReviewPublicationRequest{
		WorkspaceID: resolved.Workspace.ID, ExpectedVersion: resolved.Workspace.Version,
		RequestID: "request-review-reconcile-actions-requeue", ExpectedHeadSHA: resolved.ProviderSnapshot.HeadSHA,
		FindingIDs: []string{resolved.Findings[0].ID},
	})
	if err != nil || requeued.Publications[len(requeued.Publications)-1].State == ExecutionFailed {
		t.Fatalf("review publication remained stranded: pubs=%#v err=%v", requeued.Publications, err)
	}
}

func TestBranchReconciliationRejectsReviseAndDeferWithoutStrandingPublication(t *testing.T) {
	service, waitingCompletion, completionGate := implementationWaitingOnCompletion(t)
	completed, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     waitingCompletion.Workspace.ID,
		GateRunID:       completionGate.ID,
		ExpectedVersion: waitingCompletion.Workspace.Version,
		RequestID:       "request-branch-reconcile-actions-complete",
		FieldValues:     map[string]any{"action": "accept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := service.QueueBranchPublication(context.Background(), QueueBranchPublicationRequest{
		WorkspaceID: completed.Workspace.ID, ExpectedVersion: completed.Workspace.Version,
		RequestID: "request-branch-reconcile-actions-queue", ExpectedHeadSHA: completed.ProviderSnapshot.HeadSHA,
	})
	if err != nil {
		t.Fatal(err)
	}
	publication := queued.Publications[len(queued.Publications)-1]
	unknown := seedUnknownPublication(t, service, queued, publication, "request-branch-reconcile-actions-unknown")
	service.gates = reconcileWaitingGates{}
	waiting, err := service.ReconcilePhasePublication(
		context.Background(),
		nil,
		&countingBranchPublisher{},
		ReconcilePhasePublicationRequest{
			WorkspaceID: unknown.Workspace.ID, PublicationID: publication.ID,
			ExpectedVersion: unknown.Workspace.Version, RequestID: "request-branch-reconcile-actions-gate",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved := rejectUnsupportedReconciliationOutcomesThenBlock(t, service, waiting, publication.ID, "branch")
	requeued, err := service.QueueBranchPublication(context.Background(), QueueBranchPublicationRequest{
		WorkspaceID: resolved.Workspace.ID, ExpectedVersion: resolved.Workspace.Version,
		RequestID: "request-branch-reconcile-actions-requeue", ExpectedHeadSHA: resolved.ProviderSnapshot.HeadSHA,
	})
	if err != nil || requeued.Publications[len(requeued.Publications)-1].State == ExecutionFailed {
		t.Fatalf("branch publication remained stranded: pubs=%#v err=%v", requeued.Publications, err)
	}
}

func seedUnknownPublication(
	t *testing.T,
	service *Service,
	aggregate Aggregate,
	publication Publication,
	requestID string,
) Aggregate {
	t.Helper()
	publication.State = ExecutionUnknown
	publication.PublicErrorCode = "provider_outcome_unknown"
	publication.UpdatedAt = service.now().UTC()
	seeded, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: requestID, Patch: AggregatePatch{ReplacePublications: []Publication{publication}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return seeded.Aggregate
}

func rejectUnsupportedReconciliationOutcomesThenBlock(
	t *testing.T,
	service *Service,
	waiting Aggregate,
	publicationID string,
	requestPrefix string,
) Aggregate {
	t.Helper()
	gate := waiting.Gates[len(waiting.Gates)-1]
	if gate.DecisionPoint != "pr.publication.reconcile" || gate.State != ExecutionWaitingUser {
		t.Fatalf("reconciliation gate is not actionable: %#v", gate)
	}
	unchanged, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID: waiting.Workspace.ID, GateRunID: gate.ID,
		ExpectedVersion: waiting.Workspace.Version,
		RequestID:       "request-" + requestPrefix + "-unsupported-revise",
		FieldValues:     map[string]any{"action": "revise"},
	})
	publication, _ := findPublication(unchanged.Publications, publicationID)
	unchangedGate, _ := findGate(unchanged.Gates, gate.ID)
	if !errors.Is(err, ErrInvalid) || unchanged.Workspace.Version != waiting.Workspace.Version ||
		publication.State != ExecutionUnknown || unchangedGate.State != ExecutionWaitingUser {
		t.Fatalf(
			"unsupported action stranded publication: workspace=%#v publication=%#v gate=%#v err=%v",
			unchanged.Workspace,
			publication,
			unchangedGate,
			err,
		)
	}
	resolved, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     waiting.Workspace.ID,
		GateRunID:       gate.ID,
		ExpectedVersion: waiting.Workspace.Version,
		RequestID:       "request-" + requestPrefix + "-assume-failed",
		FieldValues:     map[string]any{"action": "assume-failed"},
	})
	publication, _ = findPublication(resolved.Publications, publicationID)
	if err != nil || publication.State != ExecutionFailed ||
		publication.PublicErrorCode != "publication_assumed_failed_by_user" {
		t.Fatalf("assume-failed did not resolve publication: publication=%#v err=%v", publication, err)
	}
	return resolved
}
