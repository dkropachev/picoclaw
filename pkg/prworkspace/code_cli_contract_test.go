package prworkspace

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"
)

type codeCLIOperationAI struct {
	operations map[string]int
}

func (runner *codeCLIOperationAI) RunIsolated(
	ctx context.Context,
	request IsolatedAIRequest,
) (map[string]any, error) {
	if runner.operations == nil {
		runner.operations = make(map[string]int)
	}
	runner.operations[request.Operation]++
	if request.Operation == "feature.plan" {
		return developmentPlanningResponse(false), nil
	}
	return serviceAI{}.RunIsolated(ctx, request)
}

type codeCLIProviderCounter struct {
	pullCalls       int
	repositoryCalls int
	listCalls       int
	verifyCalls     int
}

type codeCLILeaseAwareCatalog struct {
	developmentCatalogResolver
	observe func(context.Context)
}

func (provider *codeCLILeaseAwareCatalog) ListConfiguredRepositories(
	ctx context.Context,
) ([]ConfiguredRepository, error) {
	provider.observe(ctx)
	return provider.developmentCatalogResolver.ListConfiguredRepositories(ctx)
}

func (provider *codeCLIProviderCounter) ResolvePullRequest(
	context.Context,
	ResolveRequest,
) (ProviderSnapshot, error) {
	provider.pullCalls++
	return developmentProvider(SourcePullRequest, "pull-9", 9), nil
}

func (provider *codeCLIProviderCounter) ResolveRepository(
	context.Context,
	RepositoryResolveRequest,
) (ProviderSnapshot, error) {
	provider.repositoryCalls++
	return developmentProvider(SourceBrief, "brief-code-cli", 0), nil
}

func (provider *codeCLIProviderCounter) ListConfiguredRepositories(
	context.Context,
) ([]ConfiguredRepository, error) {
	provider.listCalls++
	return []ConfiguredRepository{{
		Identity: "https://github.com|42", Name: "octo/repo", CanImplement: true,
	}}, nil
}

func (provider *codeCLIProviderCounter) VerifyRepository(
	_ context.Context,
	identity string,
) (ConfiguredRepository, error) {
	provider.verifyCalls++
	return ConfiguredRepository{Identity: identity, Name: "octo/repo", CanImplement: true}, nil
}

type codeCLIValidationCounter struct {
	calls int
}

func (validation *codeCLIValidationCounter) Validate(
	_ context.Context,
	request ValidationRequest,
) (ValidationRun, error) {
	validation.calls++
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	return ValidationRun{
		State: ExecutionSucceeded, CandidateSHA: request.CandidateSHA,
		Checks:    []ValidationCheck{{ID: "test", Name: "tests", Status: "passed"}},
		StartedAt: now, FinishedAt: &now,
	}, nil
}

type codeCLIBranchPublisher struct {
	calls  int
	result BranchPublicationResult
}

func codeCLISafeFeatureRuntimeLease(
	ctx context.Context,
) (context.Context, func(), error) {
	return ctx, func() {}, nil
}

func (publisher *codeCLIBranchPublisher) PublishBranch(
	context.Context,
	BranchPublicationRequest,
) (BranchPublicationResult, error) {
	publisher.calls++
	return publisher.result, nil
}

func (publisher *codeCLIBranchPublisher) ReconcileBranch(
	context.Context,
	BranchPublicationRequest,
) (BranchPublicationResult, bool, error) {
	return BranchPublicationResult{}, false, nil
}

func codeCLIFullHTTPConfig(t *testing.T) HTTPConfig {
	t.Helper()
	service, err := NewService(ServiceConfig{
		Store: NewMemoryStore(), Provider: developmentCatalogResolver{}, AI: serviceAI{},
		PlanningEvidence: planningEvidenceStub{
			evidence: json.RawMessage(`{"files":["pkg/example.go"]}`),
		},
		Gates: passingGates{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return HTTPConfig{
		Service: service,
		Implementation: ImplementationConfig{
			Repair: &implementationRepair{}, Validation: &codeCLIValidationCounter{},
		},
		BranchPublisher:         &codeCLIBranchPublisher{},
		FeatureRuntimeLease:     codeCLISafeFeatureRuntimeLease,
		LeasedFeatureGuard:      func(context.Context) error { return nil },
		RepositoryResolverReady: true,
		GateRuntimeReady:        true,
		DraftPullRequestReady:   true,
	}
}

func TestCodeCLICapabilitiesReportCompleteAndExactMissingDependencies(t *testing.T) {
	allMissingService, err := NewService(ServiceConfig{Store: NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		config  func(*testing.T) HTTPConfig
		ready   bool
		missing []string
	}{
		{
			name: "complete",
			config: func(t *testing.T) HTTPConfig {
				return codeCLIFullHTTPConfig(t)
			},
			ready:   true,
			missing: []string{},
		},
		{
			name: "all dependencies missing",
			config: func(*testing.T) HTTPConfig {
				return HTTPConfig{Service: allMissingService}
			},
			missing: []string{
				"provider_resolver", "repository_resolver", "isolated_ai", "planning_evidence",
				"gate_runtime", "repair_runner", "local_ci", "branch_publisher",
				"draft_pull_request_publisher", "runtime_guard",
			},
		},
		{
			name: "gate runtime flag is explicit",
			config: func(t *testing.T) HTTPConfig {
				config := codeCLIFullHTTPConfig(t)
				config.GateRuntimeReady = false
				return config
			},
			missing: []string{"gate_runtime"},
		},
		{
			name: "repository resolver readiness is explicit",
			config: func(t *testing.T) HTTPConfig {
				config := codeCLIFullHTTPConfig(t)
				config.RepositoryResolverReady = false
				return config
			},
			missing: []string{"repository_resolver"},
		},
		{
			name: "draft PR flag is explicit",
			config: func(t *testing.T) HTTPConfig {
				config := codeCLIFullHTTPConfig(t)
				config.DraftPullRequestReady = false
				return config
			},
			missing: []string{"draft_pull_request_publisher"},
		},
		{
			name: "unsafe provider is distinct from unavailable guard",
			config: func(t *testing.T) HTTPConfig {
				config := codeCLIFullHTTPConfig(t)
				config.FeatureRuntimeLease = func(context.Context) (context.Context, func(), error) {
					return nil, nil, ErrUnsafeProvider
				}
				return config
			},
			missing: []string{"unsafe_provider"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, handlerErr := NewHTTPHandler(test.config(t))
			if handlerErr != nil {
				t.Fatal(handlerErr)
			}
			response := developmentHTTPRequest(
				t,
				handler,
				http.MethodGet,
				RuntimeRoutePrefix+"/capabilities",
				"",
			)
			if response.Code != http.StatusOK {
				t.Fatalf("capabilities status = %d: %s", response.Code, response.Body.String())
			}
			var capability developmentCapabilities
			if decodeErr := json.Unmarshal(response.Body.Bytes(), &capability); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if capability.Version != 1 || capability.ImplementFeatureReady != test.ready ||
				!reflect.DeepEqual(capability.Missing, test.missing) {
				t.Fatalf(
					"capabilities = %#v, want ready=%v missing=%#v",
					capability,
					test.ready,
					test.missing,
				)
			}
		})
	}
}

func TestCodeCLIUnsafeGuardPrecedesCreateAndProviderEffects(t *testing.T) {
	store := NewMemoryStore()
	provider := &codeCLIProviderCounter{}
	service, err := NewService(ServiceConfig{Store: store, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	guardCalls := 0
	handler, err := NewHTTPHandler(HTTPConfig{
		Service: service,
		FeatureRuntimeLease: func(context.Context) (context.Context, func(), error) {
			guardCalls++
			return nil, nil, ErrUnsafeProvider
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	repositories := developmentHTTPRequest(
		t,
		handler,
		http.MethodGet,
		RuntimeRoutePrefix+"/repositories",
		"",
	)
	if repositories.Code != http.StatusConflict || provider.listCalls != 0 {
		t.Fatalf(
			"guarded repository list = %d calls=%d body=%s",
			repositories.Code,
			provider.listCalls,
			repositories.Body.String(),
		)
	}
	created := developmentHTTPRequest(
		t,
		handler,
		http.MethodPost,
		RuntimeRoutePrefix,
		`{"intent":"implement_feature","source":{"kind":"brief","repository_identity":"https://github.com|42","content":"Add guarded code CLI"},"request_id":"request-code-cli-guard-create"}`,
	)
	if created.Code != http.StatusConflict || provider.repositoryCalls != 0 ||
		provider.pullCalls != 0 || provider.verifyCalls != 0 {
		t.Fatalf(
			"guarded create = %d provider calls=(pull=%d repository=%d list=%d verify=%d) body=%s",
			created.Code,
			provider.pullCalls,
			provider.repositoryCalls,
			provider.listCalls,
			provider.verifyCalls,
			created.Body.String(),
		)
	}
	page, err := store.List(t.Context(), ListFilter{})
	if err != nil || len(page.Workspaces) != 0 || guardCalls != 2 {
		t.Fatalf(
			"guarded state = workspaces=%d guard calls=%d err=%v",
			len(page.Workspaces),
			guardCalls,
			err,
		)
	}
}

func TestCodeCLIRuntimeLeaseSpansRepositoryProviderEffect(t *testing.T) {
	type leaseKey struct{}
	key := leaseKey{}
	released := false
	observed := false
	provider := &codeCLILeaseAwareCatalog{
		observe: func(ctx context.Context) {
			observed = ctx.Value(key) == "leased" && !released
		},
	}
	service, err := NewService(ServiceConfig{Store: NewMemoryStore(), Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(HTTPConfig{
		Service: service,
		FeatureRuntimeLease: func(ctx context.Context) (context.Context, func(), error) {
			return context.WithValue(ctx, key, "leased"), func() { released = true }, nil
		},
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
	if response.Code != http.StatusOK || !observed || !released {
		t.Fatalf(
			"leased provider effect = status=%d observed=%v released=%v body=%s",
			response.Code,
			observed,
			released,
			response.Body.String(),
		)
	}
}

func TestCodeCLIUnsafeReloadFencesDirectHumanGateMutation(t *testing.T) {
	service, _, waiting, gate, runner := codeCLIWaitingCharter(t)
	handler, err := NewHTTPHandler(HTTPConfig{
		Service: service,
		FeatureRuntimeLease: func(context.Context) (context.Context, func(), error) {
			return nil, nil, ErrUnsafeProvider
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := developmentLifecycleRequest(
		t,
		handler,
		http.MethodPost,
		"/"+waiting.Workspace.ID+"/gates/"+gate.ID+"/respond",
		map[string]any{
			"expected_version": waiting.Workspace.Version,
			"request_id":       "request-code-cli-gate-unsafe-reload",
			"field-values":     map[string]any{"action": "approve"},
		},
	)
	current, getErr := service.Get(t.Context(), waiting.Workspace.ID)
	if response.Code != http.StatusConflict || getErr != nil ||
		current.Workspace.Version != waiting.Workspace.Version ||
		current.Gates[0].State != ExecutionWaitingUser || runner.operations["feature.plan"] != 0 {
		t.Fatalf(
			"unsafe gate = status=%d version=%d gate=%q planning=%d err=%v body=%s",
			response.Code,
			current.Workspace.Version,
			current.Gates[0].State,
			runner.operations["feature.plan"],
			getErr,
			response.Body.String(),
		)
	}
}

func TestCodeCLIUnsafeAdvanceIsTerminalBeforeRepairValidationOrPublication(t *testing.T) {
	service, aggregate := seededDevelopmentAIService(
		t,
		PhaseTriage,
		ExecutionQueued,
		&codeCLIOperationAI{},
	)
	repair := &implementationRepair{}
	validation := &codeCLIValidationCounter{}
	publisher := &codeCLIBranchPublisher{}
	handler, err := NewHTTPHandler(HTTPConfig{
		Service: service,
		Implementation: ImplementationConfig{
			Repair: repair, Validation: validation,
		},
		BranchPublisher: publisher,
		LeasedFeatureGuard: func(context.Context) error {
			return ErrUnsafeProvider
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	failed, err := handler.AdvanceDevelopmentWorkspace(
		t.Context(),
		aggregate,
		"request-code-cli-unsafe-advance",
	)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Workspace.Phase != aggregate.Workspace.Phase ||
		failed.Workspace.ExecutionState != ExecutionFailed ||
		repair.calls != 0 || validation.calls != 0 || publisher.calls != 0 ||
		len(
			failed.RepairAttempts,
		) != 0 || len(failed.ValidationRuns) != 0 || len(failed.Publications) != 0 {
		t.Fatalf(
			"unsafe result = phase=%q state=%q repairs=%d/%d validations=%d/%d publications=%d/%d",
			failed.Workspace.Phase,
			failed.Workspace.ExecutionState,
			repair.calls,
			len(failed.RepairAttempts),
			validation.calls,
			len(failed.ValidationRuns),
			publisher.calls,
			len(failed.Publications),
		)
	}
	unsafeActivities := 0
	for _, activity := range failed.Activity {
		if activity.Kind == "development.failed" && activity.Metadata["code"] == "unsafe_provider" {
			unsafeActivities++
		}
	}
	if unsafeActivities != 1 {
		t.Fatalf("unsafe terminal activity = %#v", failed.Activity)
	}
	replayed, err := handler.AdvanceDevelopmentWorkspace(
		t.Context(),
		failed,
		"request-code-cli-unsafe-advance-retry",
	)
	if err != nil || !reflect.DeepEqual(replayed, failed) || repair.calls != 0 ||
		validation.calls != 0 || publisher.calls != 0 {
		t.Fatalf(
			"unsafe retry = equal=%v repair=%d validation=%d publication=%d err=%v",
			reflect.DeepEqual(replayed, failed),
			repair.calls,
			validation.calls,
			publisher.calls,
			err,
		)
	}
}

func TestCodeCLIUnavailableTriagePersistsTerminalSignalWithoutClaim(t *testing.T) {
	service, aggregate := seededDevelopmentAIService(
		t,
		PhaseTriage,
		ExecutionQueued,
		&codeCLIOperationAI{},
	)
	handler, err := NewHTTPHandler(HTTPConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	if !handler.AutonomousDevelopmentWorkspaceReady(aggregate) ||
		handler.AutonomousDevelopmentWorkspaceClaimRequired(aggregate) {
		t.Fatal("unavailable triage must be selected for a pure terminal mutation")
	}
	admittedAggregate, admitted, err := handler.AdmitAutonomousDevelopmentWorkspace(
		t.Context(),
		aggregate,
		"request-code-cli-implementation-unavailable-admit",
	)
	if err != nil || !admitted || !reflect.DeepEqual(admittedAggregate, aggregate) {
		t.Fatalf("unavailable triage admission = admitted=%v err=%v", admitted, err)
	}

	failed, err := handler.AdvanceDevelopmentWorkspace(
		t.Context(),
		aggregate,
		"request-code-cli-implementation-unavailable",
	)
	if err != nil || failed.Workspace.ExecutionState != ExecutionFailed ||
		failed.Workspace.Version != aggregate.Workspace.Version+1 ||
		handler.AutonomousDevelopmentWorkspaceReady(failed) {
		t.Fatalf("unavailable triage = workspace=%#v err=%v", failed.Workspace, err)
	}
	if !developmentFailureRecorded(failed, "implementation_unavailable") ||
		len(failed.RepairAttempts) != 0 || len(failed.ValidationRuns) != 0 ||
		len(failed.Publications) != 0 {
		t.Fatalf(
			"unavailable triage effects = activity=%#v repairs=%d validations=%d publications=%d",
			failed.Activity,
			len(failed.RepairAttempts),
			len(failed.ValidationRuns),
			len(failed.Publications),
		)
	}
}

func codeCLIWaitingCharter(
	t *testing.T,
) (*Service, *HTTPHandler, Aggregate, GateRun, *codeCLIOperationAI) {
	t.Helper()
	runner := &codeCLIOperationAI{}
	service, err := NewService(ServiceConfig{
		Store: NewMemoryStore(), Provider: developmentIntakeResolver{}, AI: runner,
		PlanningEvidence: planningEvidenceStub{
			evidence: json.RawMessage(`{"files":["pkg/example.go"]}`),
		},
		Gates: testAllWaitingGates{},
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := service.Create(t.Context(), CreateWorkspaceRequest{
		RequestID: "request-code-cli-charter-create", Intent: IntentImplementFeature,
		SourceKind: SourceBrief, RepositoryIdentity: "https://github.com|42", Brief: "Add the code command",
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err = service.DraftCharter(t.Context(), DraftCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-code-cli-charter-draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err = service.ConfirmCharter(t.Context(), ConfirmCharterRequest{
		WorkspaceID: aggregate.Workspace.ID, CharterID: aggregate.Charters[0].ID,
		ExpectedVersion: aggregate.Workspace.Version, RequestID: "request-code-cli-charter-confirm",
	})
	if err != nil || len(aggregate.Gates) != 1 || aggregate.Gates[0].State != ExecutionWaitingUser {
		t.Fatalf("waiting charter gate = %#v, err=%v", aggregate.Gates, err)
	}
	handler, err := NewHTTPHandler(HTTPConfig{
		Service: service, FeatureRuntimeLease: codeCLISafeFeatureRuntimeLease,
		LeasedFeatureGuard: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, handler, aggregate, aggregate.Gates[0], runner
}

func TestCodeCLIGateResponseCommitsBeforeLaterAdvanceRunsPlanningOnce(t *testing.T) {
	service, handler, waiting, gate, runner := codeCLIWaitingCharter(t)
	response := developmentLifecycleRequest(
		t,
		handler,
		http.MethodPost,
		"/"+waiting.Workspace.ID+"/gates/"+gate.ID+"/respond",
		map[string]any{
			"expected_version": waiting.Workspace.Version,
			"request_id":       "request-code-cli-gate-approve",
			"field-values":     map[string]any{"action": "approve"},
		},
	)
	if response.Code != http.StatusOK {
		t.Fatalf("gate response = %d: %s", response.Code, response.Body.String())
	}
	committed, err := service.Get(t.Context(), waiting.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Workspace.Phase != PhasePlanning || runner.operations["feature.plan"] != 0 {
		t.Fatalf(
			"gate response ran work inline: phase=%q operations=%#v",
			committed.Workspace.Phase,
			runner.operations,
		)
	}

	planned, err := handler.AdvanceDevelopmentWorkspace(
		t.Context(),
		committed,
		"request-code-cli-plan-advance",
	)
	if err != nil || runner.operations["feature.plan"] != 1 {
		t.Fatalf("planning advance = operations=%#v err=%v", runner.operations, err)
	}
	terminal, err := handler.AdvanceDevelopmentWorkspace(
		t.Context(),
		planned,
		"request-code-cli-plan-advance-again",
	)
	if err != nil || runner.operations["feature.plan"] != 1 ||
		terminal.Workspace.ExecutionState != ExecutionFailed ||
		!developmentFailureRecorded(terminal, "implementation_unavailable") {
		t.Fatalf(
			"planning repeated = operations=%#v terminal=%#v err=%v",
			runner.operations,
			terminal.Workspace,
			err,
		)
	}
}

func TestCodeCLIRevisedConfirmationGateIsExactAutoAdvanceFence(t *testing.T) {
	service, handler, waiting, gate, runner := codeCLIWaitingCharter(t)
	response := developmentLifecycleRequest(
		t,
		handler,
		http.MethodPost,
		"/"+waiting.Workspace.ID+"/gates/"+gate.ID+"/respond",
		map[string]any{
			"expected_version": waiting.Workspace.Version,
			"request_id":       "request-code-cli-gate-revise",
			"field-values":     map[string]any{"action": "revise"},
		},
	)
	if response.Code != http.StatusOK {
		t.Fatalf("gate response = %d: %s", response.Code, response.Body.String())
	}
	revised, err := service.Get(t.Context(), waiting.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revised.Workspace.Phase != PhaseCharter ||
		handler.AutonomousDevelopmentWorkspaceReady(revised) {
		t.Fatalf("revised charter remained autonomous: workspace=%#v", revised.Workspace)
	}
	unchanged, err := handler.AdvanceDevelopmentWorkspace(
		t.Context(),
		revised,
		"request-code-cli-revised-auto-advance",
	)
	if err != nil || !reflect.DeepEqual(unchanged, revised) ||
		runner.operations["feature.plan"] != 0 {
		t.Fatalf(
			"revised gate auto-advanced: equal=%v operations=%#v err=%v",
			reflect.DeepEqual(unchanged, revised),
			runner.operations,
			err,
		)
	}
}

type codeCLIBranchResultPublisher struct {
	result BranchPublicationResult
}

func (publisher codeCLIBranchResultPublisher) PublishBranch(
	context.Context,
	BranchPublicationRequest,
) (BranchPublicationResult, error) {
	return publisher.result, nil
}

func (codeCLIBranchResultPublisher) ReconcileBranch(
	context.Context,
	BranchPublicationRequest,
) (BranchPublicationResult, bool, error) {
	return BranchPublicationResult{}, false, nil
}

func codeCLIQueuedFeaturePublication(
	t *testing.T,
) (*Service, Aggregate, Publication, string) {
	t.Helper()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	service, err := NewService(ServiceConfig{
		Store: NewMemoryStore(), Provider: developmentIntakeResolver{}, Gates: passingGates{},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := service.Create(t.Context(), CreateWorkspaceRequest{
		RequestID: "request-code-cli-publication-create", Intent: IntentImplementFeature,
		SourceKind: SourceBrief, RepositoryIdentity: "https://github.com|42", Brief: "Publish a draft PR",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := "2222222222222222222222222222222222222222"
	charter := Charter{
		ID: stableID("pcr_", aggregate.Workspace.ID, "code-cli-charter"), Revision: 1,
		Type: PRTypeFeature, Goal: "Publish a draft PR", AcceptanceCriteria: []string{"Draft PR exists"},
		BaseSHA: aggregate.ProviderSnapshot.BaseSHA, HeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		Confirmed: true, ConfirmedAt: &now, CreatedAt: now,
	}
	planning := StageRun{
		ID: stableID("psr_", aggregate.Workspace.ID, "code-cli-planning"), Stage: "planning",
		State: ExecutionSucceeded, CharterID: charter.ID, HeadSHA: charter.HeadSHA,
		StartedAt: now, FinishedAt: &now,
	}
	implementation := StageRun{
		ID: stableID(
			"psr_",
			aggregate.Workspace.ID,
			"code-cli-implementation",
		), Stage: "implementation",
		State: ExecutionSucceeded, CharterID: charter.ID, HeadSHA: charter.HeadSHA,
		StartedAt: now, FinishedAt: &now,
	}
	repair := RepairAttempt{
		ID: stableID(
			"pra_",
			aggregate.Workspace.ID,
			"code-cli-repair",
		), StageRunID: implementation.ID,
		Number: 1, State: ExecutionSucceeded, ResultSummary: "implemented", CandidateSHA: candidate,
		Scope: ScopeAssessment{
			Distance: ScopeExact, Size: ChangeSizeXS, Presence: WorkCandidatePresent,
			TypeCompatible: true, Confidence: 1,
		},
		StartedAt: now, FinishedAt: &now,
		PublicationFence: &ImplementationPublicationFence{
			BaseCommit: aggregate.ProviderSnapshot.HeadSHA, Tip: candidate, Tree: candidate,
		},
	}
	validation := ValidationRun{
		ID:              stableID("pvr_", aggregate.Workspace.ID, "code-cli-validation"),
		StageRunID:      implementation.ID,
		RepairAttemptID: repair.ID,
		State:           ExecutionSucceeded,
		CandidateSHA:    candidate,
		Checks:          []ValidationCheck{{ID: "test", Name: "tests", Status: "passed"}},
		StartedAt:       now, FinishedAt: &now,
	}
	phase, state, active := PhasePublication, ExecutionWaitingUser, charter.ID
	seeded, err := service.store.Mutate(t.Context(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-code-cli-publication-seed",
		Patch: AggregatePatch{
			Phase: &phase, ExecutionState: &state, ActiveCharterID: &active,
			AppendCharters: []Charter{
				charter,
			}, AppendStageRuns: []StageRun{planning, implementation},
			AppendRepairs: []RepairAttempt{repair}, AppendValidations: []ValidationRun{validation},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	contextRevision, err := implementationCompletionContextRevision(seeded.Aggregate)
	if err != nil {
		t.Fatal(err)
	}
	subject := map[string]any{"implementation_context_revision": contextRevision}
	subjectRevision, err := fingerprintValue(subject)
	if err != nil {
		t.Fatal(err)
	}
	completionGate, err := pinGateSubject(GateRun{
		ID:            stableID("pgr_", aggregate.Workspace.ID, "code-cli-completion"),
		DecisionPoint: "pr.implementation.complete", TargetID: repair.ID,
		State: ExecutionSucceeded, PolicyRevision: "sha256:code-cli-test", SubjectRevision: subjectRevision,
		Turns: []GateTurn{
			{Status: "answered", FieldValues: map[string]any{"action": "accept"}},
		},
		CreatedAt: now, FinishedAt: &now,
	}, subject)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := service.store.Mutate(t.Context(), Mutation{
		WorkspaceID: seeded.Aggregate.Workspace.ID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
		RequestID: "request-code-cli-publication-authorize",
		Patch:     AggregatePatch{AppendGates: []GateRun{completionGate}},
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := service.QueueBranchPublication(t.Context(), QueueBranchPublicationRequest{
		WorkspaceID: authorized.Aggregate.Workspace.ID, ExpectedVersion: authorized.Aggregate.Workspace.Version,
		RequestID:       "request-code-cli-publication-queue",
		ExpectedHeadSHA: authorized.Aggregate.ProviderSnapshot.HeadSHA,
	})
	if err != nil {
		t.Fatal(err)
	}
	publication := queued.Publications[len(queued.Publications)-1]
	return service, queued, publication, candidate
}

func TestCodeCLIIncompleteDraftPRIdentityNeverCompletesWorkspace(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BranchPublicationResult)
	}{
		{
			name: "URL is not the created pull request",
			mutate: func(result *BranchPublicationResult) {
				result.ExternalURL = "https://github.com/octo/repo"
			},
		},
		{
			name: "pull identity is missing",
			mutate: func(result *BranchPublicationResult) {
				result.PullRequestID = ""
				result.PullNumber = 0
			},
		},
		{
			name: "published head is not the repaired candidate",
			mutate: func(result *BranchPublicationResult) {
				result.HeadSHA = "3333333333333333333333333333333333333333"
			},
		},
		{
			name: "published head ref is missing",
			mutate: func(result *BranchPublicationResult) {
				result.HeadRef = ""
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, queued, publication, candidate := codeCLIQueuedFeaturePublication(t)
			result := BranchPublicationResult{
				ExternalID: "pull-17", ExternalURL: "https://github.com/octo/repo/pull/17",
				PullRequestID: "pull-17", PullNumber: 17, HeadRef: "picoclaw/code-cli", HeadSHA: candidate,
			}
			test.mutate(&result)
			finalized, err := service.DispatchBranchPublication(
				t.Context(),
				codeCLIBranchResultPublisher{result: result},
				DispatchPhasePublicationRequest{
					WorkspaceID: queued.Workspace.ID, PublicationID: publication.ID,
					ExpectedVersion: queued.Workspace.Version,
					RequestID:       "request-code-cli-publication-dispatch",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			persisted, getErr := service.Get(t.Context(), queued.Workspace.ID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			if finalized.Workspace.Phase == PhaseComplete ||
				persisted.Workspace.Phase == PhaseComplete {
				t.Fatalf(
					"incomplete provider identity completed workspace: result=%#v workspace=%#v",
					result,
					persisted.Workspace,
				)
			}
			storedPublication, found := findPublication(persisted.Publications, publication.ID)
			if !found || storedPublication.State == ExecutionSucceeded {
				t.Fatalf(
					"incomplete provider identity succeeded publication: result=%#v publication=%#v",
					result,
					storedPublication,
				)
			}
		})
	}
}
