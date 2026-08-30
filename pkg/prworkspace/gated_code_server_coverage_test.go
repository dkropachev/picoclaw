package prworkspace

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

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
