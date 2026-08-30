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

func developmentLifecycleRequest(
	t *testing.T,
	handler *HTTPHandler,
	method, suffix string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, RuntimeRoutePrefix+suffix, bytes.NewReader(encoded))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func developmentLifecycleAggregate(
	t *testing.T,
	response *httptest.ResponseRecorder,
) Aggregate {
	t.Helper()
	var aggregate Aggregate
	if err := json.Unmarshal(response.Body.Bytes(), &aggregate); err != nil {
		t.Fatalf("decode aggregate: %v: %s", err, response.Body.String())
	}
	return aggregate
}

func developmentLifecycleService(
	t *testing.T,
	gates GateEvaluator,
) (*Service, *HTTPHandler, Aggregate) {
	t.Helper()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	service, err := NewService(ServiceConfig{
		Store: NewMemoryStore(), Provider: developmentIntakeResolver{}, AI: serviceAI{}, Gates: gates,
		PlanningEvidence: planningEvidenceStub{evidence: json.RawMessage(`{"files":["pkg/example.go"]}`)},
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := service.Create(t.Context(), CreateWorkspaceRequest{
		RequestID: "request-lifecycle-workspace-0001", Intent: IntentImplementFeature,
		SourceKind: SourceIssue, IssueURL: "https://github.com/octo/repo/issues/7",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(HTTPConfig{
		Service: service, FeatureRuntimeLease: codeCLISafeFeatureRuntimeLease,
		LeasedFeatureGuard: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, handler, aggregate
}

func developmentCharterBody(
	aggregate Aggregate,
	requestID, goal string,
) map[string]any {
	return map[string]any{
		"expected_version":       aggregate.Workspace.Version,
		"expected_head_revision": aggregate.ProviderSnapshot.HeadSHA,
		"request_id":             requestID,
		"pr_type":                PRTypeFeature,
		"goal":                   goal,
		"acceptance_criteria":    []string{"The behavior is covered."},
		"included_areas":         []string{"pkg"},
		"exclusions":             []string{"deployment"},
		"non_goals":              []string{"broad cleanup"},
	}
}

func TestDevelopmentCharterRoutesAndAutonomousPlanningAdvance(t *testing.T) {
	service, handler, aggregate := developmentLifecycleService(t, passingGates{})
	response := developmentLifecycleRequest(
		t,
		handler,
		http.MethodPost,
		"/"+aggregate.Workspace.ID+"/charter/draft",
		developmentCharterBody(aggregate, "request-lifecycle-charter-draft", "Draft the feature"),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("draft status = %d: %s", response.Code, response.Body.String())
	}
	aggregate = developmentLifecycleAggregate(t, response)
	if len(aggregate.Charters) != 1 || aggregate.Charters[0].Revision != 1 {
		t.Fatalf("drafted charters = %#v", aggregate.Charters)
	}

	response = developmentLifecycleRequest(
		t,
		handler,
		http.MethodPut,
		"/"+aggregate.Workspace.ID+"/charter",
		developmentCharterBody(aggregate, "request-lifecycle-charter-save", "Save the bounded feature"),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("save status = %d: %s", response.Code, response.Body.String())
	}
	aggregate = developmentLifecycleAggregate(t, response)
	if len(aggregate.Charters) != 2 || aggregate.Charters[1].Goal != "Save the bounded feature" {
		t.Fatalf("saved charters = %#v", aggregate.Charters)
	}

	confirm := developmentCharterBody(aggregate, "request-lifecycle-charter-confirm", "unused")
	confirm["expected_charter_revision"] = aggregate.Charters[1].Revision
	response = developmentLifecycleRequest(
		t,
		handler,
		http.MethodPost,
		"/"+aggregate.Workspace.ID+"/charter/confirm",
		confirm,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("confirm status = %d: %s", response.Code, response.Body.String())
	}
	aggregate = developmentLifecycleAggregate(t, response)
	if aggregate.Workspace.Phase != PhasePlanning || aggregate.Workspace.ActiveCharterID == "" {
		t.Fatalf("confirmed workspace = %#v", aggregate.Workspace)
	}

	revise := developmentCharterBody(aggregate, "request-lifecycle-charter-revise", "Revise the feature")
	revise["expected_charter_revision"] = aggregate.Charters[1].Revision
	response = developmentLifecycleRequest(
		t,
		handler,
		http.MethodPost,
		"/"+aggregate.Workspace.ID+"/charter/revise",
		revise,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("revise status = %d: %s", response.Code, response.Body.String())
	}
	aggregate = developmentLifecycleAggregate(t, response)
	if aggregate.Workspace.Phase != PhaseCharter || aggregate.Workspace.ActiveCharterID != "" ||
		len(aggregate.Charters) != 3 {
		t.Fatalf("revised workspace = %#v charters=%#v", aggregate.Workspace, aggregate.Charters)
	}

	stale := developmentCharterBody(aggregate, "request-lifecycle-charter-stale", "Stale edit")
	stale["expected_head_revision"] = "different-provider-head"
	response = developmentLifecycleRequest(
		t,
		handler,
		http.MethodPut,
		"/"+aggregate.Workspace.ID+"/charter",
		stale,
	)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale head status = %d: %s", response.Code, response.Body.String())
	}

	advanced, err := handler.AdvanceDevelopmentWorkspace(
		t.Context(), aggregate, "request-lifecycle-autonomous-advance",
	)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Workspace.Version != aggregate.Workspace.Version ||
		advanced.Workspace.Phase != PhaseCharter || advanced.Workspace.ActiveCharterID != "" ||
		advanced.Charters[len(advanced.Charters)-1].Confirmed {
		t.Fatalf("human-revised charter advanced autonomously = %#v", advanced)
	}
	if handler.AutonomousDevelopmentWorkspaceReady(advanced) {
		t.Fatal("human-gated charter remained autonomously runnable")
	}

	confirm = developmentCharterBody(
		advanced,
		"request-lifecycle-charter-reconfirm",
		"unused",
	)
	confirm["expected_charter_revision"] = advanced.Charters[len(advanced.Charters)-1].Revision
	response = developmentLifecycleRequest(
		t,
		handler,
		http.MethodPost,
		"/"+advanced.Workspace.ID+"/charter/confirm",
		confirm,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("reconfirm status = %d: %s", response.Code, response.Body.String())
	}
	advanced = developmentLifecycleAggregate(t, response)
	advanced, err = handler.AdvanceDevelopmentWorkspace(
		t.Context(), advanced, "request-lifecycle-human-reconfirmed-advance",
	)
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Workspace.Phase != PhaseTriage || advanced.Workspace.ExecutionState != ExecutionBlocked ||
		advanced.Workspace.ActiveCharterID == "" || len(advanced.StageRuns) != 1 {
		t.Fatalf("human-reconfirmed advance = %#v", advanced)
	}
	persisted, err := service.Get(t.Context(), advanced.Workspace.ID)
	if err != nil || persisted.Workspace.Version != advanced.Workspace.Version {
		t.Fatalf("persisted autonomous advance = %#v, %v", persisted, err)
	}
}

func TestDevelopmentGateHTTPListsAndRespondsToCharterApproval(t *testing.T) {
	service, handler, aggregate := developmentLifecycleService(t, testAllWaitingGates{})
	draft := CharterDraftOutput{
		Type: PRTypeFeature, Goal: "Approve the feature charter",
		AcceptanceCriteria: []string{"The charter is confirmed."}, IncludedAreas: []string{"pkg"},
	}
	var err error
	aggregate, err = service.SaveCharter(t.Context(), SaveCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-lifecycle-gate-charter", Draft: draft,
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err = service.ConfirmCharter(t.Context(), ConfirmCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, CharterID: aggregate.Charters[0].ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-lifecycle-gate-start",
	})
	if err != nil || len(aggregate.Gates) != 1 || aggregate.Gates[0].State != ExecutionWaitingUser {
		t.Fatalf("waiting charter gate = %#v, %v", aggregate.Gates, err)
	}

	path := "/" + aggregate.Workspace.ID + "/gates"
	response := developmentLifecycleRequest(t, handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(aggregate.Gates[0].ID)) {
		t.Fatalf("gate list status = %d: %s", response.Code, response.Body.String())
	}
	response = developmentLifecycleRequest(
		t,
		handler,
		http.MethodPost,
		path+"/"+aggregate.Gates[0].ID+"/respond",
		map[string]any{
			"expected_version": aggregate.Workspace.Version,
			"request_id":       "request-lifecycle-gate-respond",
			"field-values":     map[string]any{"action": "approve"},
		},
	)
	if response.Code != http.StatusOK {
		t.Fatalf("gate response status = %d: %s", response.Code, response.Body.String())
	}
	aggregate = developmentLifecycleAggregate(t, response)
	if aggregate.Gates[0].State != ExecutionSucceeded || aggregate.Workspace.ActiveCharterID == "" ||
		!aggregate.Charters[0].Confirmed {
		t.Fatalf("answered charter gate = %#v", aggregate)
	}
}

func TestDevelopmentRunStageMessageAndCorrectionRoutes(t *testing.T) {
	t.Run("planning and unavailable implementation runs", func(t *testing.T) {
		runner := &developmentAIStub{response: developmentPlanningResponse(true)}
		service, aggregate := seededDevelopmentAIService(t, PhasePlanning, ExecutionQueued, runner)
		handler, err := NewHTTPHandler(HTTPConfig{
			Service: service, FeatureRuntimeLease: codeCLISafeFeatureRuntimeLease,
		})
		if err != nil {
			t.Fatal(err)
		}
		body := map[string]any{
			"expected_version":       aggregate.Workspace.Version,
			"expected_head_revision": aggregate.ProviderSnapshot.HeadSHA,
			"request_id":             "request-lifecycle-planning-run",
		}
		response := developmentLifecycleRequest(
			t, handler, http.MethodPost, "/"+aggregate.Workspace.ID+"/planning-runs", body,
		)
		if response.Code != http.StatusOK {
			t.Fatalf("planning run status = %d: %s", response.Code, response.Body.String())
		}
		planned := developmentLifecycleAggregate(t, response)
		if planned.Workspace.Phase != PhaseTriage || len(planned.Findings) != 1 {
			t.Fatalf("planned workspace = %#v", planned)
		}

		body["expected_version"] = planned.Workspace.Version
		body["request_id"] = "request-lifecycle-implementation-unavailable"
		response = developmentLifecycleRequest(
			t, handler, http.MethodPost, "/"+aggregate.Workspace.ID+"/implementation-runs", body,
		)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("implementation unavailable status = %d: %s", response.Code, response.Body.String())
		}
		response = developmentLifecycleRequest(
			t, handler, http.MethodGet, "/"+aggregate.Workspace.ID+"/planning-runs", nil,
		)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("planning method status = %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("stage cancellation", func(t *testing.T) {
		store := NewMemoryStore()
		input := testCreateInput()
		created, err := store.Create(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		now := input.Workspace.CreatedAt
		phase, state := PhaseReview, ExecutionRunning
		stage := StageRun{
			ID: "psr_99999999999999999999999999999991", Stage: "review", State: ExecutionRunning,
			HeadSHA: input.Provider.HeadSHA, Attempt: 1, StartedAt: now,
		}
		seeded, err := store.Mutate(t.Context(), Mutation{
			WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
			RequestID: "request-lifecycle-stage-seed", Patch: AggregatePatch{
				Phase: &phase, ExecutionState: &state, AppendStageRuns: []StageRun{stage},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		service, err := NewService(ServiceConfig{Store: store, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewHTTPHandler(HTTPConfig{
			Service: service, FeatureRuntimeLease: codeCLISafeFeatureRuntimeLease,
		})
		if err != nil {
			t.Fatal(err)
		}
		response := developmentLifecycleRequest(
			t,
			handler,
			http.MethodPost,
			"/"+input.Workspace.ID+"/stage-runs/"+stage.ID+"/cancel",
			map[string]any{
				"expected_version": seeded.Aggregate.Workspace.Version,
				"request_id":       "request-lifecycle-stage-cancel",
			},
		)
		if response.Code != http.StatusOK {
			t.Fatalf("stage cancel status = %d: %s", response.Code, response.Body.String())
		}
		canceled := developmentLifecycleAggregate(t, response)
		if canceled.StageRuns[0].State != ExecutionCanceled ||
			canceled.Workspace.ExecutionState != ExecutionCanceled {
			t.Fatalf("canceled stage = %#v", canceled)
		}
	})

	t.Run("message correction and promotion", func(t *testing.T) {
		now := time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC)
		service, aggregate := publicationTestService(t, DeferredIssuesAsk, passingGates{}, now)
		handler, err := NewHTTPHandler(HTTPConfig{
			Service: service, FeatureRuntimeLease: codeCLISafeFeatureRuntimeLease,
		})
		if err != nil {
			t.Fatal(err)
		}
		response := developmentLifecycleRequest(
			t, handler, http.MethodPost, "/"+aggregate.Workspace.ID+"/messages", map[string]any{
				"expected_version":   aggregate.Workspace.Version,
				"request_id":         "request-lifecycle-message",
				"stage":              "review",
				"content":            "The retry limit is three.",
				"mark_as_correction": true,
				"applicability":      CorrectionReviewAndImpl,
			},
		)
		if response.Code != http.StatusOK {
			t.Fatalf("message status = %d: %s", response.Code, response.Body.String())
		}
		aggregate = developmentLifecycleAggregate(t, response)
		if len(aggregate.Messages) != 1 || len(aggregate.Corrections) != 1 {
			t.Fatalf("message correction = %#v", aggregate)
		}

		response = developmentLifecycleRequest(
			t, handler, http.MethodPost, "/"+aggregate.Workspace.ID+"/corrections", map[string]any{
				"expected_version": aggregate.Workspace.Version,
				"request_id":       "request-lifecycle-correction",
				"kind":             CorrectionFactual,
				"applicability":    CorrectionReviewAndImpl,
				"original_claim":   "Retries are unlimited.",
				"correction":       "Retries stop after three attempts.",
				"reason":           "Repository policy fixes the limit.",
			},
		)
		if response.Code != http.StatusOK {
			t.Fatalf("correction status = %d: %s", response.Code, response.Body.String())
		}
		aggregate = developmentLifecycleAggregate(t, response)
		correction := aggregate.Corrections[len(aggregate.Corrections)-1]
		response = developmentLifecycleRequest(
			t,
			handler,
			http.MethodPost,
			"/"+aggregate.Workspace.ID+"/corrections/"+correction.ID+"/promote",
			map[string]any{
				"expected_version": aggregate.Workspace.Version,
				"request_id":       "request-lifecycle-correction-promote",
			},
		)
		if response.Code != http.StatusOK {
			t.Fatalf("correction promotion status = %d: %s", response.Code, response.Body.String())
		}
		aggregate = developmentLifecycleAggregate(t, response)
		if !aggregate.Corrections[len(aggregate.Corrections)-1].Promoted || len(aggregate.RepositoryLessons) != 1 {
			t.Fatalf("promoted correction = %#v", aggregate)
		}
	})
}

type lifecycleReviewPublisher struct {
	result ReviewPublicationResult
	found  bool
	err    error
}

func (publisher *lifecycleReviewPublisher) PublishReview(
	context.Context,
	ReviewPublicationRequest,
) (ReviewPublicationResult, error) {
	return publisher.result, publisher.err
}

func (publisher *lifecycleReviewPublisher) ReconcileReview(
	context.Context,
	ReviewPublicationRequest,
) (ReviewPublicationResult, bool, error) {
	return publisher.result, publisher.found, publisher.err
}

func seedRunningReviewPublication(
	t *testing.T,
	service *Service,
	aggregate Aggregate,
	requestID string,
) Aggregate {
	t.Helper()
	queued, err := service.QueueReviewPublication(t.Context(), QueueReviewPublicationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: requestID + "-queue", ExpectedHeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		FindingIDs: []string{aggregate.Findings[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	publication := queued.Publications[len(queued.Publications)-1]
	publication.State = ExecutionRunning
	publication.Attempts++
	publication.UpdatedAt = service.now().UTC()
	running, err := service.store.Mutate(t.Context(), Mutation{
		WorkspaceID: queued.Workspace.ID, ExpectedVersion: queued.Workspace.Version,
		RequestID: requestID + "-running", Patch: AggregatePatch{ReplacePublications: []Publication{publication}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return running.Aggregate
}

func TestDevelopmentReviewPublicationHTTPReconcilesSuccessAndUnknown(t *testing.T) {
	now := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	t.Run("provider confirms success", func(t *testing.T) {
		service, aggregate := publicationTestService(t, DeferredIssuesAsk, passingGates{}, now)
		publisher := &lifecycleReviewPublisher{found: true, result: ReviewPublicationResult{
			ExternalID: "review-17", ExternalURL: "https://github.com/octo/repo/pull/3",
		}}
		handler, err := NewHTTPHandler(HTTPConfig{
			Service: service, ReviewPublisher: publisher,
			FeatureRuntimeLease: codeCLISafeFeatureRuntimeLease,
		})
		if err != nil {
			t.Fatal(err)
		}
		queueBody := map[string]any{
			"expected_version":       aggregate.Workspace.Version,
			"expected_head_revision": aggregate.ProviderSnapshot.HeadSHA,
			"request_id":             "request-lifecycle-review-publication",
			"finding_ids":            []string{aggregate.Findings[0].ID},
		}
		response := developmentLifecycleRequest(
			t, handler, http.MethodPost, "/"+aggregate.Workspace.ID+"/publications/review", queueBody,
		)
		if response.Code != http.StatusOK {
			t.Fatalf("review queue status = %d: %s", response.Code, response.Body.String())
		}
		aggregate, err = service.Get(t.Context(), aggregate.Workspace.ID)
		if err != nil {
			t.Fatal(err)
		}
		publication := aggregate.Publications[len(aggregate.Publications)-1]
		publication.State = ExecutionRunning
		publication.Attempts++
		publication.UpdatedAt = now
		running, err := service.store.Mutate(t.Context(), Mutation{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID: "request-lifecycle-review-running",
			Patch:     AggregatePatch{ReplacePublications: []Publication{publication}},
		})
		if err != nil {
			t.Fatal(err)
		}
		response = developmentLifecycleRequest(
			t,
			handler,
			http.MethodPost,
			"/"+aggregate.Workspace.ID+"/publications/"+publication.ID+"/reconcile",
			map[string]any{
				"expected_version":       running.Aggregate.Workspace.Version,
				"expected_head_revision": aggregate.ProviderSnapshot.HeadSHA,
				"request_id":             "request-lifecycle-review-reconcile",
			},
		)
		if response.Code != http.StatusOK {
			t.Fatalf("review reconcile status = %d: %s", response.Code, response.Body.String())
		}
		reconciled := developmentLifecycleAggregate(t, response)
		publication = reconciled.Publications[len(reconciled.Publications)-1]
		if publication.State != ExecutionSucceeded || publication.ExternalID != "review-17" {
			t.Fatalf("reconciled publication = %#v", publication)
		}
	})

	t.Run("provider read failure remains unknown", func(t *testing.T) {
		service, aggregate := publicationTestService(t, DeferredIssuesAsk, passingGates{}, now)
		aggregate = seedRunningReviewPublication(t, service, aggregate, "request-lifecycle-review-error")
		publisherErr := errors.New("provider observation unavailable")
		publisher := &lifecycleReviewPublisher{err: publisherErr}
		handler, err := NewHTTPHandler(HTTPConfig{
			Service: service, ReviewPublisher: publisher,
			FeatureRuntimeLease: codeCLISafeFeatureRuntimeLease,
		})
		if err != nil {
			t.Fatal(err)
		}
		publication := aggregate.Publications[len(aggregate.Publications)-1]
		response := developmentLifecycleRequest(
			t,
			handler,
			http.MethodPost,
			"/"+aggregate.Workspace.ID+"/publications/"+publication.ID+"/reconcile",
			map[string]any{
				"expected_version":       aggregate.Workspace.Version,
				"expected_head_revision": aggregate.ProviderSnapshot.HeadSHA,
				"request_id":             "request-lifecycle-review-error-reconcile",
			},
		)
		if response.Code == http.StatusOK {
			t.Fatalf("provider error status = %d: %s", response.Code, response.Body.String())
		}
		persisted, getErr := service.Get(t.Context(), aggregate.Workspace.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		publication = persisted.Publications[len(persisted.Publications)-1]
		if publication.State != ExecutionUnknown || publication.PublicErrorCode != "provider_outcome_unknown" {
			t.Fatalf("unknown publication = %#v", publication)
		}
	})
}

func TestDevelopmentServiceRepositoryPoliciesFailClosed(t *testing.T) {
	if _, err := NewService(ServiceConfig{
		Store: NewMemoryStore(), DeferredIssueMode: "invalid",
	}); err == nil {
		t.Fatal("invalid deferred issue mode accepted")
	}
	policy := ScopeDispositionPolicy{
		Default: ScopeDispositionRule{Mode: ScopeDispositionRelaxed, Prompt: "Only directly relevant work."},
	}
	service, err := NewService(ServiceConfig{
		Store: NewMemoryStore(), DeferredIssueMode: DeferredIssuesAsk,
		DeferredIssueModeForRepository: func(string, string) DeferredIssueMode {
			return DeferredIssuesAutomatic
		},
		ScopeDispositionForRepository: func(string, string) ScopeDispositionPolicy { return policy },
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate := Aggregate{
		Workspace: Workspace{ProviderOrigin: "https://github.com", RepositoryID: "42"},
		ProviderSnapshot: ProviderSnapshot{
			ProviderOrigin: "https://github.com", RepositoryID: "42",
		},
	}
	if service.deferredMode(aggregate) != DeferredIssuesAutomatic ||
		service.scopeDisposition(aggregate).Default.Mode != ScopeDispositionRelaxed {
		t.Fatalf("repository policies = %q %#v", service.deferredMode(aggregate), service.scopeDisposition(aggregate))
	}
	service.deferredIssueModeForRepository = func(string, string) DeferredIssueMode { return "invalid" }
	if service.deferredMode(aggregate) != DeferredIssuesOff ||
		(*Service)(nil).deferredMode(aggregate) != DeferredIssuesOff {
		t.Fatal("invalid repository deferred mode did not fail closed")
	}
}
