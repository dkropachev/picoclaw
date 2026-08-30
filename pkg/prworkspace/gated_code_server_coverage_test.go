package prworkspace

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type gatedCodeMutationErrorStore struct {
	Store
}

type gatedCodeFailGates struct{}

func (gatedCodeFailGates) Start(context.Context, GateRequest) (GateRun, error) {
	return GateRun{}, errors.New("injected gated-code gate failure")
}

func (gatedCodeFailGates) Respond(
	_ context.Context,
	gate GateRun,
	_ map[string]any,
) (GateRun, error) {
	return gate, nil
}

func (store gatedCodeMutationErrorStore) Mutate(
	ctx context.Context,
	mutation Mutation,
) (MutationResult, error) {
	current, _ := store.Store.Get(ctx, mutation.WorkspaceID)
	return MutationResult{Aggregate: current}, errors.New("injected gated-code mutation failure")
}

func TestGatedCodeCapabilitiesFailClosedForMalformedRuntimeLeases(t *testing.T) {
	tests := []struct {
		name        string
		lease       func(context.Context) (context.Context, func(), error)
		wantMissing string
		wantRelease bool
	}{
		{
			name: "transient lease error",
			lease: func(context.Context) (context.Context, func(), error) {
				return nil, nil, errors.New("runtime reloading")
			},
			wantMissing: "runtime_guard",
		},
		{
			name: "nil lease context releases partial lease",
			lease: func(context.Context) (context.Context, func(), error) {
				return nil, func() {}, nil
			},
			wantMissing: "runtime_guard",
			wantRelease: true,
		},
		{
			name: "nil release",
			lease: func(ctx context.Context) (context.Context, func(), error) {
				return ctx, nil, nil
			},
			wantMissing: "runtime_guard",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			released := false
			config := codeCLIFullHTTPConfig(t)
			config.FeatureRuntimeLease = func(ctx context.Context) (context.Context, func(), error) {
				leaseCtx, release, err := test.lease(ctx)
				if release != nil {
					return leaseCtx, func() {
						released = true
						release()
					}, err
				}
				return leaseCtx, nil, err
			}
			handler, err := NewHTTPHandler(config)
			if err != nil {
				t.Fatal(err)
			}
			missing := handler.implementFeatureMissing(t.Context())
			if !reflect.DeepEqual(missing, []string{test.wantMissing}) || released != test.wantRelease {
				t.Fatalf("missing = %#v released=%v", missing, released)
			}
		})
	}

	var nilHandler *HTTPHandler
	missing := nilHandler.implementFeatureMissing(t.Context())
	if len(missing) != 10 || missing[0] != "provider_resolver" || missing[len(missing)-1] != "runtime_guard" {
		t.Fatalf("nil handler missing = %#v", missing)
	}
}

func TestGatedCodeCapabilitiesRouteIsExact(t *testing.T) {
	handler, err := NewHTTPHandler(codeCLIFullHTTPConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, RuntimeRoutePrefix+"/capabilities", nil),
		httptest.NewRequest(http.MethodGet, RuntimeRoutePrefix+"/capabilities?private=1", nil),
		httptest.NewRequest(http.MethodGet, RuntimeRoutePrefix+"/capabilities/extra", nil),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
			t.Fatalf(
				"%s %s = %d allow=%q body=%s",
				request.Method,
				request.URL,
				response.Code,
				response.Header().Get("Allow"),
				response.Body.String(),
			)
		}
	}
}

func TestGatedCodeProviderRoutesFailClosedForUnavailableOrMalformedLease(t *testing.T) {
	tests := []struct {
		name        string
		lease       func(context.Context) (context.Context, func(), error)
		wantRelease bool
	}{
		{name: "missing lease"},
		{
			name: "transient lease failure",
			lease: func(context.Context) (context.Context, func(), error) {
				return nil, nil, errors.New("runtime reloading")
			},
		},
		{
			name: "nil context",
			lease: func(context.Context) (context.Context, func(), error) {
				return nil, func() {}, nil
			},
			wantRelease: true,
		},
		{
			name: "nil release",
			lease: func(ctx context.Context) (context.Context, func(), error) {
				return ctx, nil, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &codeCLIProviderCounter{}
			service, err := NewService(ServiceConfig{Store: NewMemoryStore(), Provider: provider})
			if err != nil {
				t.Fatal(err)
			}
			released := false
			lease := test.lease
			if lease != nil {
				lease = func(original func(context.Context) (context.Context, func(), error)) func(context.Context) (context.Context, func(), error) {
					return func(ctx context.Context) (context.Context, func(), error) {
						leaseCtx, release, leaseErr := original(ctx)
						if release == nil {
							return leaseCtx, nil, leaseErr
						}
						return leaseCtx, func() {
							released = true
							release()
						}, leaseErr
					}
				}(
					lease,
				)
			}
			handler, err := NewHTTPHandler(HTTPConfig{Service: service, FeatureRuntimeLease: lease})
			if err != nil {
				t.Fatal(err)
			}
			response := developmentHTTPRequest(t, handler, http.MethodGet, RuntimeRoutePrefix+"/repositories", "")
			if response.Code != http.StatusServiceUnavailable || provider.listCalls != 0 ||
				released != test.wantRelease {
				t.Fatalf(
					"response=%d calls=%d released=%v body=%s",
					response.Code,
					provider.listCalls,
					released,
					response.Body.String(),
				)
			}
		})
	}
}

func TestGatedCodeAutonomousAdmissionGuardOutcomes(t *testing.T) {
	service, aggregate := seededDevelopmentAIService(t, PhasePlanning, ExecutionQueued, &codeCLIOperationAI{})
	tests := []struct {
		name       string
		handler    *HTTPHandler
		aggregate  Aggregate
		wantAdmit  bool
		wantUnsafe bool
		wantErr    bool
	}{
		{
			name:    "pickup bypasses feature guard",
			handler: &HTTPHandler{},
			aggregate: func() Aggregate {
				value := aggregate
				value.Workspace.Intent = IntentPickupPR
				return value
			}(),
			wantAdmit: true,
		},
		{
			name:      "missing guard fails closed",
			handler:   &HTTPHandler{service: service},
			aggregate: aggregate,
			wantErr:   true,
		},
		{
			name: "transient guard error is returned",
			handler: &HTTPHandler{service: service, leasedFeatureGuard: func(context.Context) error {
				return errors.New("runtime reloading")
			}},
			aggregate: aggregate,
			wantErr:   true,
		},
		{
			name: "unsafe guard persists terminal outcome",
			handler: &HTTPHandler{service: service, leasedFeatureGuard: func(context.Context) error {
				return ErrUnsafeProvider
			}},
			aggregate:  aggregate,
			wantUnsafe: true,
		},
		{
			name:      "safe guard admits",
			handler:   &HTTPHandler{service: service, leasedFeatureGuard: func(context.Context) error { return nil }},
			aggregate: aggregate,
			wantAdmit: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, admitted, err := test.handler.AdmitAutonomousDevelopmentWorkspace(
				t.Context(), test.aggregate, stableID("req_", "admission-coverage", test.name),
			)
			if (err != nil) != test.wantErr || admitted != test.wantAdmit {
				t.Fatalf("admitted=%v err=%v result=%#v", admitted, err, result.Workspace)
			}
			if test.wantUnsafe &&
				(err != nil || admitted || result.Workspace.ExecutionState != ExecutionFailed || !unsafeProviderFailureRecorded(result)) {
				t.Fatalf("unsafe result = admitted=%v err=%v aggregate=%#v", admitted, err, result)
			}
		})
	}
}

func TestGatedCodeTerminalFailuresAreIdempotentAndValidateIntent(t *testing.T) {
	service, aggregate := seededDevelopmentAIService(t, PhaseTriage, ExecutionQueued, &codeCLIOperationAI{})
	failed, err := service.FailImplementationUnavailable(
		t.Context(),
		aggregate,
		"request-implementation-unavailable-coverage",
	)
	if err != nil || failed.Workspace.ExecutionState != ExecutionFailed {
		t.Fatalf("first failure = %#v err=%v", failed.Workspace, err)
	}
	replayed, err := service.FailImplementationUnavailable(
		t.Context(),
		failed,
		"request-implementation-unavailable-replay",
	)
	if err != nil || !reflect.DeepEqual(replayed, failed) {
		t.Fatalf("replayed failure changed: equal=%v err=%v", reflect.DeepEqual(replayed, failed), err)
	}
	pickup := aggregate
	pickup.Workspace.Intent = IntentPickupPR
	if _, err = service.FailImplementationUnavailable(
		t.Context(),
		pickup,
		"request-invalid-pickup-failure",
	); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("pickup failure error = %v", err)
	}
	if _, err = (*Service)(
		nil,
	).FailUnsafeProvider(t.Context(), aggregate, "request-invalid-nil-service"); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("nil service error = %v", err)
	}
}

func TestGatedCodeImplementationPublicationIdentityValidation(t *testing.T) {
	_, aggregate, publication, candidate := codeCLIQueuedFeaturePublication(t)
	repair, found := findRepairAttempt(aggregate.RepairAttempts, publication.TargetID)
	if !found {
		t.Fatal("queued publication has no repair")
	}
	provider := aggregate.ProviderSnapshot
	provider.Intent = IntentImplementFeature
	provider.PullRequestID = "pull-17"
	provider.PullNumber = 17
	provider.HeadRef = "picoclaw/code-cli"
	provider.HeadSHA = candidate
	valid := phasePublicationResult{
		externalID: "pull-17", externalURL: "https://github.com/octo/repo/pull/17", provider: &provider,
	}
	if !validImplementationBranchPublicationResult(aggregate, publication, valid) {
		t.Fatal("complete implementation publication was rejected")
	}

	tests := []struct {
		name   string
		mutate func(*Aggregate, *Publication, *phasePublicationResult)
	}{
		{
			name:   "repair absent",
			mutate: func(a *Aggregate, _ *Publication, _ *phasePublicationResult) { a.RepairAttempts = nil },
		},
		{
			name:   "provider absent",
			mutate: func(_ *Aggregate, _ *Publication, r *phasePublicationResult) { r.provider = nil },
		},
		{
			name:   "wrong intent",
			mutate: func(_ *Aggregate, _ *Publication, r *phasePublicationResult) { r.provider.Intent = IntentPickupPR },
		},
		{name: "wrong origin", mutate: func(_ *Aggregate, _ *Publication, r *phasePublicationResult) {
			r.provider.ProviderOrigin = "https://gitlab.com"
		}},
		{
			name:   "wrong repository id",
			mutate: func(_ *Aggregate, _ *Publication, r *phasePublicationResult) { r.provider.RepositoryID = "99" },
		},
		{
			name:   "wrong repository",
			mutate: func(_ *Aggregate, _ *Publication, r *phasePublicationResult) { r.provider.Repository = "octo/other" },
		},
		{
			name:   "missing pull id",
			mutate: func(_ *Aggregate, _ *Publication, r *phasePublicationResult) { r.provider.PullRequestID = "" },
		},
		{
			name:   "invalid pull number",
			mutate: func(_ *Aggregate, _ *Publication, r *phasePublicationResult) { r.provider.PullNumber = 0 },
		},
		{
			name:   "missing head ref",
			mutate: func(_ *Aggregate, _ *Publication, r *phasePublicationResult) { r.provider.HeadRef = "" },
		},
		{name: "wrong head", mutate: func(_ *Aggregate, _ *Publication, r *phasePublicationResult) {
			r.provider.HeadSHA = "3333333333333333333333333333333333333333"
		}},
		{
			name:   "wrong external id",
			mutate: func(_ *Aggregate, _ *Publication, r *phasePublicationResult) { r.externalID = "pull-18" },
		},
		{name: "wrong URL", mutate: func(_ *Aggregate, _ *Publication, r *phasePublicationResult) {
			r.externalURL = "https://github.com/octo/repo/pull/18"
		}},
		{
			name:   "wrong publication kind",
			mutate: func(_ *Aggregate, p *Publication, _ *phasePublicationResult) { p.Kind = PublicationGitHubReview },
		},
		{name: "failed repair", mutate: func(a *Aggregate, _ *Publication, _ *phasePublicationResult) {
			a.RepairAttempts[len(a.RepairAttempts)-1].State = ExecutionFailed
		}},
		{name: "empty repair candidate", mutate: func(a *Aggregate, _ *Publication, r *phasePublicationResult) {
			a.RepairAttempts[len(a.RepairAttempts)-1].CandidateSHA = ""
			r.provider.HeadSHA = ""
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := aggregate
			a.RepairAttempts = append([]RepairAttempt(nil), aggregate.RepairAttempts...)
			p := publication
			r := valid
			providerCopy := provider
			r.provider = &providerCopy
			test.mutate(&a, &p, &r)
			if validImplementationBranchPublicationResult(a, p, r) {
				t.Fatal("incomplete implementation publication was accepted")
			}
		})
	}
	_ = repair
}

func TestGatedCodePullRequestURLValidationIsExact(t *testing.T) {
	provider := ProviderSnapshot{ProviderOrigin: "https://github.com", Repository: "octo/repo", PullNumber: 17}
	if !validImplementationPullRequestURL("https://github.com/octo/repo/pull/17", provider) {
		t.Fatal("exact HTTPS pull request URL rejected")
	}
	for _, raw := range []string{
		"://bad", "http://github.com/octo/repo/pull/17", "https://user@github.com/octo/repo/pull/17",
		"https://github.com/octo/repo/pull/17?x=1", "https://github.com/octo/repo/pull/17#fragment",
		"https://gitlab.com/octo/repo/pull/17", "https://github.com/octo/repo/pull/18",
	} {
		if validImplementationPullRequestURL(raw, provider) {
			t.Fatalf("invalid pull request URL accepted: %q", raw)
		}
	}
	badOrigin := provider
	badOrigin.ProviderOrigin = "://bad"
	if validImplementationPullRequestURL("https://github.com/octo/repo/pull/17", badOrigin) {
		t.Fatal("invalid provider origin accepted")
	}
}

func TestGatedCodeRemainingFailClosedBranches(t *testing.T) {
	t.Run("repository and lease errors", func(t *testing.T) {
		service, err := NewService(ServiceConfig{
			Store: NewMemoryStore(),
			Provider: developmentCatalogResolver{
				listErr: errors.New("repository catalog unavailable"),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewHTTPHandler(HTTPConfig{
			Service: service, FeatureRuntimeLease: codeCLISafeFeatureRuntimeLease,
		})
		if err != nil {
			t.Fatal(err)
		}
		response := developmentHTTPRequest(
			t,
			handler,
			http.MethodGet,
			RuntimeRoutePrefix+"/repositories",
			"",
		)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("repository catalog status = %d: %s", response.Code, response.Body.String())
		}

		unsafeHandler, handlerErr := NewHTTPHandler(HTTPConfig{
			Service: service,
			FeatureRuntimeLease: func(context.Context) (context.Context, func(), error) {
				return nil, nil, ErrUnsafeProvider
			},
		})
		if handlerErr != nil {
			t.Fatal(handlerErr)
		}
		response = developmentHTTPRequest(
			t,
			unsafeHandler,
			http.MethodPost,
			RuntimeRoutePrefix+"/repositories/resolve",
			`{"repository_url":"https://github.com/octo/repo"}`,
		)
		if response.Code != http.StatusConflict {
			t.Fatalf("unsafe resolve status = %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("autonomous guard failures", func(t *testing.T) {
		service, aggregate := seededDevelopmentAIService(
			t,
			PhasePlanning,
			ExecutionQueued,
			&codeCLIOperationAI{},
		)
		missing, err := NewHTTPHandler(HTTPConfig{Service: service})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = missing.AdvanceDevelopmentWorkspace(
			t.Context(),
			aggregate,
			"request-gated-code-missing-runtime-guard",
		); err == nil {
			t.Fatal("missing runtime guard advanced feature work")
		}
		transient, err := NewHTTPHandler(HTTPConfig{
			Service: service,
			LeasedFeatureGuard: func(context.Context) error {
				return errors.New("runtime reloading")
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = transient.AdvanceDevelopmentWorkspace(
			t.Context(),
			aggregate,
			"request-gated-code-transient-runtime-guard",
		); err == nil {
			t.Fatal("transient runtime guard failure advanced feature work")
		}
	})

	t.Run("workspace mutation routing errors", func(t *testing.T) {
		if workspaceMutationRoute(http.MethodPost, "unknown") {
			t.Fatal("unknown workspace mutation route was admitted")
		}
		service, err := NewService(ServiceConfig{Store: NewMemoryStore()})
		if err != nil {
			t.Fatal(err)
		}
		handler := &HTTPHandler{service: service}
		request := httptest.NewRequest(http.MethodPost, RuntimeRoutePrefix+"/missing/messages", nil)
		response := httptest.NewRecorder()
		_, release, admitted := handler.leaseWorkspaceMutation(
			response,
			request,
			"devw_99999999999999999999999999999999",
		)
		release()
		if admitted || response.Code != http.StatusNotFound {
			t.Fatalf("missing workspace mutation = admitted=%v status=%d", admitted, response.Code)
		}
	})

	t.Run("unsafe queued and running publications", func(t *testing.T) {
		service, aggregate := seededDevelopmentAIService(
			t,
			PhasePublication,
			ExecutionQueued,
			&codeCLIOperationAI{},
		)
		now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
		publication := Publication{
			ID: "ppb_99999999999999999999999999999999", Kind: PublicationBranchPush,
			State: ExecutionQueued, ExpectedHeadSHA: aggregate.ProviderSnapshot.HeadSHA,
			PayloadDigest: "sha256:gated-code-queued", CreatedAt: now, UpdatedAt: now,
		}
		queued, err := service.store.Mutate(t.Context(), Mutation{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID: "request-gated-code-queue-unsafe",
			Patch:     AggregatePatch{AppendPublications: []Publication{publication}},
		})
		if err != nil {
			t.Fatal(err)
		}
		failed, err := service.FailUnsafeProvider(
			t.Context(),
			queued.Aggregate,
			"request-gated-code-fail-queued-unsafe",
		)
		if err != nil || failed.Publications[0].State != ExecutionFailed ||
			failed.Publications[0].PublicErrorCode != "unsafe_provider" {
			t.Fatalf("queued unsafe failure = %#v, err=%v", failed.Publications, err)
		}

		runningService, running, runningPublication, _ := codeCLIQueuedFeaturePublication(t)
		runningPublication.State = ExecutionRunning
		runningPublication.Attempts = 1
		claimed, err := runningService.store.Mutate(t.Context(), Mutation{
			WorkspaceID: running.Workspace.ID, ExpectedVersion: running.Workspace.Version,
			RequestID:                "request-gated-code-claim-running-unsafe",
			Patch:                    AggregatePatch{ReplacePublications: []Publication{runningPublication}},
			branchPublicationLeaseID: runningPublication.ID,
		})
		if err != nil {
			t.Fatal(err)
		}
		runningService.store = gatedCodeMutationErrorStore{Store: runningService.store}
		if _, err = runningService.FailUnsafeProvider(
			t.Context(),
			claimed.Aggregate,
			"request-gated-code-fail-running-unsafe",
		); err == nil {
			t.Fatal("running unsafe publication ignored store failure")
		}
	})

	t.Run("invalid implementation failure and charter confirmation", func(t *testing.T) {
		service, aggregate := seededDevelopmentAIService(
			t,
			PhaseTriage,
			ExecutionQueued,
			&codeCLIOperationAI{},
		)
		if _, err := service.FailImplementationUnavailable(
			t.Context(),
			aggregate,
			"bad",
		); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid implementation failure error = %v", err)
		}
		charterService, _, waiting, _, _ := codeCLIWaitingCharter(t)
		if _, err := charterService.ConfirmCharterAutomatically(
			t.Context(),
			ConfirmCharterRequest{
				WorkspaceID: waiting.Workspace.ID, CharterID: waiting.Charters[0].ID,
				ExpectedVersion: waiting.Workspace.Version,
				RequestID:       "request-gated-code-repeat-charter-confirm",
			},
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("repeat charter confirmation error = %v", err)
		}
	})

	t.Run("missing phase publishers fail before queueing", func(t *testing.T) {
		service, aggregate := seededDevelopmentAIService(
			t,
			PhasePublication,
			ExecutionWaitingUser,
			&codeCLIOperationAI{},
		)
		handler, err := NewHTTPHandler(HTTPConfig{
			Service: service, FeatureRuntimeLease: codeCLISafeFeatureRuntimeLease,
		})
		if err != nil {
			t.Fatal(err)
		}
		expectedHead := aggregate.ProviderSnapshot.ProviderRevision
		if expectedHead == "" {
			expectedHead = aggregate.ProviderSnapshot.HeadSHA
		}
		for _, kind := range []string{"review", "implementation"} {
			response := developmentLifecycleRequest(
				t,
				handler,
				http.MethodPost,
				"/"+aggregate.Workspace.ID+"/publications/"+kind,
				map[string]any{
					"expected_version":       aggregate.Workspace.Version,
					"expected_head_revision": expectedHead,
					"request_id":             "request-gated-code-missing-" + kind + "-publisher",
				},
			)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("missing %s publisher status = %d: %s", kind, response.Code, response.Body.String())
			}
		}
	})

	t.Run("automatic charter confirmation surfaces gate failure", func(t *testing.T) {
		service, err := NewService(ServiceConfig{
			Store: NewMemoryStore(), Provider: developmentCatalogResolver{}, AI: serviceAI{},
			Gates: gatedCodeFailGates{},
		})
		if err != nil {
			t.Fatal(err)
		}
		aggregate, err := service.Create(t.Context(), CreateWorkspaceRequest{
			RequestID: "request-gated-code-charter-create", Intent: IntentImplementFeature,
			SourceKind: SourceBrief, RepositoryIdentity: "https://github.com|42",
			Brief: "Exercise automatic charter confirmation failure",
		})
		if err != nil {
			t.Fatal(err)
		}
		drafted, err := service.DraftCharter(t.Context(), DraftCharterRequest{
			WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
			RequestID: "request-gated-code-charter-draft",
		})
		if err != nil {
			t.Fatal(err)
		}
		handler, err := NewHTTPHandler(HTTPConfig{
			Service: service, LeasedFeatureGuard: func(context.Context) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = handler.AdvanceDevelopmentWorkspace(
			t.Context(),
			drafted,
			"request-gated-code-charter-advance",
		); err == nil {
			t.Fatal("automatic charter confirmation hid gate failure")
		}
	})

	t.Run("implementation helper surfaces repair failure", func(t *testing.T) {
		service, aggregate := seededDevelopmentAIService(
			t,
			PhaseImplementation,
			ExecutionQueued,
			&codeCLIOperationAI{},
		)
		handler, err := NewHTTPHandler(HTTPConfig{
			Service: service,
			Implementation: ImplementationConfig{
				Repair: failedImplementationRepair{}, Validation: implementationValidation{},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = handler.maybeRunImplementation(
			t.Context(),
			aggregate,
			"request-gated-code-failed-repair",
		); err == nil {
			t.Fatal("implementation helper hid repair failure")
		}
	})

	t.Run("autonomous review surfaces missing evidence", func(t *testing.T) {
		service, aggregate := seededDevelopmentAIService(
			t,
			PhaseReview,
			ExecutionQueued,
			&codeCLIOperationAI{},
		)
		handler, err := NewHTTPHandler(HTTPConfig{
			Service: service, LeasedFeatureGuard: func(context.Context) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = handler.AdvanceDevelopmentWorkspace(
			t.Context(),
			aggregate,
			"request-gated-code-review-without-evidence",
		); err == nil {
			t.Fatal("autonomous review hid missing evidence")
		}
	})

	t.Run("automatic deferred failure is returned", func(t *testing.T) {
		now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
		service, aggregate := publicationTestService(
			t,
			DeferredIssuesAutomatic,
			passingGates{},
			now,
		)
		service.store = &failAutomaticMutationStore{Store: service.store, remaining: 1}
		handler, err := NewHTTPHandler(HTTPConfig{
			Service: service, IssuePublisher: &countingIssuePublisher{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = handler.AdvanceDevelopmentWorkspace(
			t.Context(),
			aggregate,
			"request-gated-code-deferred-failure",
		); err == nil {
			t.Fatal("automatic deferred policy hid store failure")
		}
	})
}
