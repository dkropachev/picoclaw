package prworkspace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type developmentCatalogResolver struct {
	developmentIntakeResolver
	listErr   error
	verifyErr error
}

func (resolver developmentCatalogResolver) ListConfiguredRepositories(
	context.Context,
) ([]ConfiguredRepository, error) {
	if resolver.listErr != nil {
		return nil, resolver.listErr
	}
	return []ConfiguredRepository{{
		Identity: "https://github.com|42", Name: "octo/repo", DefaultBranch: "main", CanImplement: true,
	}}, nil
}

func (resolver developmentCatalogResolver) VerifyRepository(
	_ context.Context,
	identity string,
) (ConfiguredRepository, error) {
	if resolver.verifyErr != nil {
		return ConfiguredRepository{}, resolver.verifyErr
	}
	return ConfiguredRepository{
		Identity: identity, Name: "octo/repo", DefaultBranch: "main", CanImplement: true,
	}, nil
}

func developmentHTTPHandler(t *testing.T, provider ProviderResolver) (*HTTPHandler, *Service) {
	t.Helper()
	service, err := NewService(ServiceConfig{Store: NewMemoryStore(), Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(HTTPConfig{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	return handler, service
}

func developmentHTTPRequest(
	t *testing.T,
	handler http.Handler,
	method, path, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	var requestBody *strings.Reader
	if body == "" {
		requestBody = strings.NewReader("")
	} else {
		requestBody = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, requestBody)
	if method != http.MethodGet {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestDevelopmentHTTPCollectionCatalogAndConversation(t *testing.T) {
	handler, service := developmentHTTPHandler(t, developmentCatalogResolver{})

	for index, requestID := range []string{"request-http-brief-0001", "request-http-brief-0002"} {
		response := developmentHTTPRequest(
			t,
			handler,
			http.MethodPost,
			RuntimeRoutePrefix,
			`{"intent":"implement_feature","source":{"kind":"brief","repository_identity":"https://github.com|42","content":"Add HTTP coverage `+string(
				rune('A'+index),
			)+`"},"request_id":"`+requestID+`"}`,
		)
		if response.Code != http.StatusCreated {
			t.Fatalf("create %d response = %d %s", index, response.Code, response.Body.String())
		}
	}

	query := url.Values{
		"repository": {"octo/repo"},
		"phase":      {string(PhaseCharter)},
		"ownership":  {"owned"},
		"limit":      {"1"},
	}
	if parsed, err := listFilterFromQuery(url.Values{
		"state": {string(ExecutionQueued)}, "needs_action": {"false"},
	}); err != nil || parsed.State != ExecutionQueued || parsed.NeedsAction == nil || *parsed.NeedsAction {
		t.Fatalf("valid state/action filter = %#v, %v", parsed, err)
	}
	response := developmentHTTPRequest(
		t,
		handler,
		http.MethodGet,
		RuntimeRoutePrefix+"?"+query.Encode(),
		"",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("list response = %d %s", response.Code, response.Body.String())
	}
	var page struct {
		Workspaces []Workspace `json:"workspaces"`
		NextCursor string      `json:"next_cursor"`
	}
	if err := json.Unmarshal(
		response.Body.Bytes(),
		&page,
	); err != nil || len(page.Workspaces) != 1 ||
		page.NextCursor == "" {
		t.Fatalf("list page = %#v, %v", page, err)
	}
	response = developmentHTTPRequest(
		t,
		handler,
		http.MethodGet,
		RuntimeRoutePrefix+"?cursor="+url.QueryEscape(page.NextCursor),
		"",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("cursor response = %d %s", response.Code, response.Body.String())
	}

	response = developmentHTTPRequest(t, handler, http.MethodGet, RuntimeRoutePrefix+"/repositories", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "octo/repo") {
		t.Fatalf("repository list = %d %s", response.Code, response.Body.String())
	}
	response = developmentHTTPRequest(
		t,
		handler,
		http.MethodPost,
		RuntimeRoutePrefix+"/repositories/resolve",
		`{"repository_url":"https://github.com/octo/repo"}`,
	)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "https://github.com/octo/repo") {
		t.Fatalf("repository verify = %d %s", response.Code, response.Body.String())
	}

	conversationRunner := &developmentAIStub{response: developmentAskResponse()}
	conversationService, aggregate := seededDevelopmentAIService(
		t,
		PhaseTriage,
		ExecutionQueued,
		conversationRunner,
	)
	conversationHandler, err := NewHTTPHandler(HTTPConfig{Service: conversationService})
	if err != nil {
		t.Fatal(err)
	}
	conversationPath := RuntimeRoutePrefix + "/" + aggregate.Workspace.ID + "/conversation/messages"
	response = developmentHTTPRequest(t, conversationHandler, http.MethodGet, conversationPath, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"revision":0`) {
		t.Fatalf("conversation GET = %d %s", response.Code, response.Body.String())
	}
	response = developmentHTTPRequest(
		t,
		conversationHandler,
		http.MethodPost,
		conversationPath,
		`{"mode":"ask","content":"What is the status?","expected_revision":0,"request_id":"request-http-chat-0001"}`,
	)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"revision":2`) {
		t.Fatalf("conversation POST = %d %s", response.Code, response.Body.String())
	}
	response = developmentHTTPRequest(
		t,
		conversationHandler,
		http.MethodGet,
		RuntimeRoutePrefix+"/"+aggregate.Workspace.ID,
		"",
	)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), aggregate.Workspace.ID) {
		t.Fatalf("workspace GET = %d %s", response.Code, response.Body.String())
	}

	if _, err := service.ListConfiguredRepositories(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifyConfiguredRepository(t.Context(), "https://github.com/octo/repo"); err != nil {
		t.Fatal(err)
	}
}

func TestDevelopmentHTTPBoundaryAndFilterValidation(t *testing.T) {
	handler, _ := developmentHTTPHandler(t, developmentCatalogResolver{})
	for _, test := range []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodDelete, RuntimeRoutePrefix, `{}`, http.StatusMethodNotAllowed},
		{http.MethodGet, RuntimeRoutePrefix + "/not-a-workspace", "", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "/repositories/extra", "", http.StatusMethodNotAllowed},
		{http.MethodGet, RuntimeRoutePrefix + "/repositories/resolve", "", http.StatusMethodNotAllowed},
		{http.MethodGet, RuntimeRoutePrefix + "?unknown=1", "", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?phase=wrong", "", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?state=wrong", "", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?ownership=wrong", "", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?needs_action=wrong", "", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?limit=0", "", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?limit=101", "", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?limit=words", "", http.StatusBadRequest},
		{http.MethodGet, RuntimeRoutePrefix + "?cursor=not-base64", "", http.StatusBadRequest},
		{http.MethodPost, RuntimeRoutePrefix, `{`, http.StatusBadRequest},
		{http.MethodPost, RuntimeRoutePrefix, `{}` + `{}`, http.StatusBadRequest},
		{http.MethodPost, RuntimeRoutePrefix + "?query=1", `{}`, http.StatusBadRequest},
	} {
		response := developmentHTTPRequest(t, handler, test.method, test.path, test.body)
		if response.Code != test.status {
			t.Fatalf("%s %s response = %d %s", test.method, test.path, response.Code, response.Body.String())
		}
	}

	response := httptest.NewRecorder()
	(*HTTPHandler)(nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, RuntimeRoutePrefix, nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("nil handler status = %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("nil request status = %d", response.Code)
	}
	response = developmentHTTPRequest(t, handler, http.MethodGet, RuntimeRoutePrefix+"/", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("trailing slash status = %d", response.Code)
	}

	for _, candidate := range []string{
		strings.Repeat("x", 1025),
		"e30",
		url.QueryEscape("not-json"),
	} {
		if _, err := decodeWorkspaceCursor(candidate); err == nil {
			t.Fatalf("invalid cursor %q accepted", candidate)
		}
	}
	for _, phase := range []Phase{
		PhaseIntake,
		PhaseCharter,
		PhasePlanning,
		PhaseReview,
		PhaseTriage,
		PhaseImplementation,
		PhaseValidation,
		PhaseCompletionAudit,
		PhasePublication,
		PhaseComplete,
	} {
		if !validPhase(phase) {
			t.Fatalf("valid phase %q rejected", phase)
		}
	}
	for _, state := range []ExecutionState{
		ExecutionQueued,
		ExecutionRunning,
		ExecutionWaitingGate,
		ExecutionWaitingUser,
		ExecutionSucceeded,
		ExecutionFailed,
		ExecutionBlocked,
		ExecutionCanceled,
		ExecutionStale,
		ExecutionUnknown,
	} {
		if !validExecutionState(state) {
			t.Fatalf("valid state %q rejected", state)
		}
	}
	if validPhase("invalid") || validExecutionState("invalid") {
		t.Fatal("invalid lifecycle value accepted")
	}
}

func TestDevelopmentHTTPConfigurationAndAutonomousReadiness(t *testing.T) {
	if _, err := NewHTTPHandler(HTTPConfig{}); err == nil {
		t.Fatal("nil service accepted")
	}
	service, err := NewService(ServiceConfig{Store: NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range []HTTPConfig{
		{Service: service, ReviewNudgePolicy: NudgePolicy{MinimumAdditionalRounds: -1}},
		{Service: service, CompletionNudgePolicy: NudgePolicy{MinimumAdditionalRounds: -1}},
		{Service: service, SizePolicy: SizePolicy{}},
	} {
		if config.SizePolicy == (SizePolicy{}) {
			config.SizePolicy = SizePolicy{XS: SizeThreshold{Files: -1}}
		}
		if _, configErr := NewHTTPHandler(config); configErr == nil {
			t.Fatalf("invalid HTTP config %#v accepted", config)
		}
	}

	runner := &developmentAIStub{response: developmentPlanningResponse(false)}
	readyService, aggregate := seededDevelopmentAIService(t, PhasePlanning, ExecutionQueued, runner)
	readyHandler, err := NewHTTPHandler(HTTPConfig{Service: readyService})
	if err != nil {
		t.Fatal(err)
	}
	if (*HTTPHandler)(nil).AutonomousDevelopmentWorkspaceReady(aggregate) ||
		(*HTTPHandler)(nil).AutonomousDevelopmentWorkspaceClaimRequired(aggregate) {
		t.Fatal("nil handler reported autonomous readiness")
	}
	for _, phase := range []Phase{
		PhaseCharter,
		PhasePlanning,
		PhaseReview,
		PhaseTriage,
		PhaseImplementation,
		PhasePublication,
		PhaseComplete,
	} {
		candidate := aggregate
		candidate.Workspace.Phase = phase
		_ = readyHandler.AutonomousDevelopmentWorkspaceReady(candidate)
		_ = readyHandler.AutonomousDevelopmentWorkspaceClaimRequired(candidate)
	}
	chartered := aggregate
	chartered.Workspace.Phase = PhaseCharter
	chartered.Charters = []Charter{{ID: "pcr_11111111111111111111111111111111"}}
	if !readyHandler.AutonomousDevelopmentWorkspaceReady(chartered) ||
		readyHandler.AutonomousDevelopmentWorkspaceClaimRequired(chartered) {
		t.Fatalf("chartered readiness = %v/%v",
			readyHandler.AutonomousDevelopmentWorkspaceReady(chartered),
			readyHandler.AutonomousDevelopmentWorkspaceClaimRequired(chartered))
	}

	withoutProvider, err := NewService(ServiceConfig{Store: NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutProvider.ListConfiguredRepositories(t.Context()); err == nil {
		t.Fatal("missing repository lister accepted")
	}
	if _, err := withoutProvider.VerifyConfiguredRepository(t.Context(), "https://github.com/octo/repo"); err == nil {
		t.Fatal("missing repository verifier accepted")
	}
	if _, err := (*Service)(nil).List(t.Context(), ListFilter{}); err == nil {
		t.Fatal("nil service list accepted")
	}
	if _, err := (*Service)(nil).ListConfiguredRepositories(t.Context()); err == nil {
		t.Fatal("nil service repository list accepted")
	}
	if _, err := (*Service)(nil).VerifyConfiguredRepository(t.Context(), "repository"); err == nil {
		t.Fatal("nil service repository verify accepted")
	}
}
