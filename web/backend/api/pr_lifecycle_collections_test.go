package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

type prLifecycleRepositoryAssignmentCollectionTestResponse struct {
	RepositoryAssignments []prLifecycleRepositoryAssignmentSummary `json:"repository_assignments"`
	Total                 int                                      `json:"total"`
	NextCursor            string                                   `json:"next_cursor"`
	CanonicalQuery        string                                   `json:"canonical_query"`
	QuerySchema           collectionquery.Schema                   `json:"query_schema"`
	ConfigRevision        string                                   `json:"config_revision"`
	Effects               prLifecycleCollectionEffects             `json:"effects"`
}

type prLifecycleRepositoryAssignmentDetailTestResponse struct {
	RepositoryAssignment   prLifecycleRepositoryAssignmentResource           `json:"repository_assignment"`
	WorkflowConfigurations map[string]prLifecycleWorkflowConfigurationChoice `json:"workflow_configurations"`
	ConfigRevision         string                                            `json:"config_revision"`
	Effects                prLifecycleCollectionEffects                      `json:"effects"`
}

type prLifecycleWorkflowConfigurationCollectionTestResponse struct {
	WorkflowConfigurations []prLifecycleWorkflowConfigurationSummaryItem `json:"workflow_configurations"`
	Total                  int                                           `json:"total"`
	NextCursor             string                                        `json:"next_cursor"`
	CanonicalQuery         string                                        `json:"canonical_query"`
	QuerySchema            collectionquery.Schema                        `json:"query_schema"`
	ConfigRevision         string                                        `json:"config_revision"`
	Effects                prLifecycleCollectionEffects                  `json:"effects"`
}

type prLifecycleWorkflowConfigurationDetailTestResponse struct {
	WorkflowConfiguration prLifecycleWorkflowConfigurationResource         `json:"workflow_configuration"`
	GateCatalog           map[string]prLifecycleCollectionGateCatalogEntry `json:"gate_catalog"`
	Flow                  map[string]any                                   `json:"flow"`
	FlowRevision          string                                           `json:"flow_revision"`
	CatalogRevision       string                                           `json:"catalog_revision"`
	ConfigRevision        string                                           `json:"config_revision"`
	Effects               prLifecycleCollectionEffects                     `json:"effects"`
}

type prLifecycleCollectionDeleteTestResponse struct {
	DeletedIDs     []string                     `json:"deleted_ids"`
	Failures       []collectionBulkFailure      `json:"failures"`
	ConfigRevision string                       `json:"config_revision"`
	Effects        prLifecycleCollectionEffects `json:"effects"`
}

func prLifecycleCollectionRequestForTest(
	t *testing.T,
	mux http.Handler,
	method string,
	path string,
	body any,
	mutate func(*http.Request),
) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, "http://launcher.local"+path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if mutate != nil {
		mutate(request)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func decodePRLifecycleCollectionTestResponse[T any](
	t *testing.T,
	response *httptest.ResponseRecorder,
	status int,
) T {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, status, response.Body.String())
	}
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func savePRLifecycleCollectionTestConfig(
	t *testing.T,
	configPath string,
	mutate func(*config.PRLifecycleConfig),
) {
	t.Helper()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := clonePRLifecycleCollectionConfig(cfg.PRLifecycle.Effective())
	mutate(&lifecycle)
	cfg.PRLifecycle = lifecycle
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
}

func prLifecycleCollectionWorkflowInput(
	id string,
	name string,
	mode config.PRLifecycleDeferredIssueMode,
) prLifecycleWorkflowConfigurationInput {
	return prLifecycleWorkflowConfigurationInput{
		ID: id, Name: name,
		Bindings:       []prLifecycleCollectionGateBinding{},
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{Mode: mode},
		ScopeDisposition: prLifecycleCollectionScopeDisposition{
			Default: config.PRLifecycleScopeDispositionRule{Mode: config.PRLifecycleScopeStrict},
			ByType:  map[string]config.PRLifecycleScopeDispositionRule{},
		},
	}
}

func collectionSchemaFieldNames(schema collectionquery.Schema) []string {
	fields := make([]string, len(schema.Fields))
	for index := range schema.Fields {
		fields[index] = string(schema.Fields[index].Name)
	}
	return fields
}

func collectionSchemaSuggestions(
	schema collectionquery.Schema,
	field collectionquery.Field,
) []string {
	for _, entry := range schema.Fields {
		if entry.Name == field {
			return entry.SuggestedValues
		}
	}
	return nil
}

func TestPRLifecycleAdministrativeCollectionsPageQueryAndReadSafeResources(t *testing.T) {
	configPath, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	identityAlpha, err := config.CanonicalPRLifecycleRepositoryIdentity(
		"https://github.com", "repo-alpha",
	)
	if err != nil {
		t.Fatal(err)
	}
	identityZulu, err := config.CanonicalPRLifecycleRepositoryIdentity(
		"https://github.com", "repo-zulu",
	)
	if err != nil {
		t.Fatal(err)
	}
	savePRLifecycleCollectionTestConfig(t, configPath, func(lifecycle *config.PRLifecycleConfig) {
		lifecycle.WorkflowConfigurations["automatic"] = config.PRLifecycleWorkflowConfiguration{
			Name: "Automatic", Bindings: []config.PRLifecycleGateBinding{},
			DeferredIssues: config.PRLifecycleDeferredIssueConfig{
				Mode: config.PRLifecycleDeferredIssuesAutomatic,
			},
		}
		lifecycle.RepositoryAssignments[identityZulu] = "default"
		lifecycle.Repositories[identityZulu] = config.PRLifecycleRepositoryDescriptor{
			Name: "zeta/repository", DefaultBranch: "develop",
		}
		// A descriptor may legitimately be absent for an assignment loaded from
		// an older discovery path. The safe resource falls back to repository ID.
		lifecycle.RepositoryAssignments[identityAlpha] = "automatic"
	})

	firstResponse := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodGet,
		prLifecycleRepositoryAssignmentCollectionPath+"?limit=1", nil, nil,
	)
	first := decodePRLifecycleCollectionTestResponse[prLifecycleRepositoryAssignmentCollectionTestResponse](
		t,
		firstResponse,
		http.StatusOK,
	)
	if first.Total != 2 || len(first.RepositoryAssignments) != 1 ||
		first.RepositoryAssignments[0].Repository != "repo-alpha" || first.NextCursor == "" {
		t.Fatalf("first assignment page = %#v", first)
	}
	if first.CanonicalQuery != "ALL ORDER BY repository ASC" ||
		!reflect.DeepEqual(
			collectionSchemaFieldNames(first.QuerySchema),
			[]string{"repository", "configuration", "default_branch"},
		) {
		t.Fatalf("assignment query contract = %#v", first)
	}
	wantRepositorySuggestions := []string{"repo-alpha", "zeta/repository"}
	repositorySuggestions := collectionSchemaSuggestions(first.QuerySchema, "repository")
	if !reflect.DeepEqual(repositorySuggestions, wantRepositorySuggestions) {
		t.Fatalf("repository suggestions = %#v", repositorySuggestions)
	}

	secondPath := prLifecycleRepositoryAssignmentCollectionPath + "?limit=1&cursor=" +
		url.QueryEscape(first.NextCursor)
	second := decodePRLifecycleCollectionTestResponse[prLifecycleRepositoryAssignmentCollectionTestResponse](
		t,
		prLifecycleCollectionRequestForTest(t, mux, http.MethodGet, secondPath, nil, nil),
		http.StatusOK,
	)
	if second.Total != 2 || len(second.RepositoryAssignments) != 1 ||
		second.RepositoryAssignments[0].Repository != "zeta/repository" ||
		second.NextCursor != "" {
		t.Fatalf("second assignment page = %#v", second)
	}

	filteredQuery := `configuration = "automatic" ORDER BY default_branch DESC`
	filteredPath := prLifecycleRepositoryAssignmentCollectionPath + "?query=" +
		url.QueryEscape(filteredQuery)
	filtered := decodePRLifecycleCollectionTestResponse[prLifecycleRepositoryAssignmentCollectionTestResponse](
		t,
		prLifecycleCollectionRequestForTest(t, mux, http.MethodGet, filteredPath, nil, nil),
		http.StatusOK,
	)
	if filtered.Total != 1 || len(filtered.RepositoryAssignments) != 1 ||
		filtered.RepositoryAssignments[0].Configuration != "automatic" ||
		filtered.CanonicalQuery != filteredQuery {
		t.Fatalf("filtered assignment page = %#v", filtered)
	}
	multiOrderQuery := `ALL ORDER BY configuration ASC, repository DESC`
	multiOrder := decodePRLifecycleCollectionTestResponse[prLifecycleRepositoryAssignmentCollectionTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodGet,
			prLifecycleRepositoryAssignmentCollectionPath+"?query="+
				url.QueryEscape(multiOrderQuery),
			nil, nil,
		),
		http.StatusOK,
	)
	if len(multiOrder.RepositoryAssignments) != 2 ||
		multiOrder.RepositoryAssignments[0].Configuration != "automatic" ||
		multiOrder.RepositoryAssignments[1].Configuration != "default" ||
		multiOrder.CanonicalQuery != multiOrderQuery {
		t.Fatalf("multi-field order = %#v", multiOrder)
	}

	wrongCursorPath := prLifecycleRepositoryAssignmentCollectionPath + "?query=" +
		url.QueryEscape(`repository = "zeta/repository"`) + "&cursor=" +
		url.QueryEscape(first.NextCursor)
	wrongCursor := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodGet, wrongCursorPath, nil, nil,
	)
	if wrongCursor.Code != http.StatusBadRequest ||
		!strings.Contains(wrongCursor.Body.String(), `"code":"invalid_cursor"`) {
		t.Fatalf("query-bound cursor response = %d %s", wrongCursor.Code, wrongCursor.Body.String())
	}

	alphaID, err := prLifecycleRepositoryAssignmentID(identityAlpha)
	if err != nil {
		t.Fatal(err)
	}
	direct := decodePRLifecycleCollectionTestResponse[prLifecycleRepositoryAssignmentDetailTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodGet,
			prLifecycleRepositoryAssignmentCollectionPath+"/"+alphaID, nil, nil,
		),
		http.StatusOK,
	)
	if direct.RepositoryAssignment.ID != alphaID ||
		direct.RepositoryAssignment.ProviderOrigin != "https://github.com" ||
		direct.RepositoryAssignment.RepositoryID != "repo-alpha" ||
		direct.RepositoryAssignment.Repository != "repo-alpha" ||
		direct.RepositoryAssignment.DefaultBranch != "" ||
		direct.WorkflowConfigurations["automatic"].DeferredIssues.Mode !=
			config.PRLifecycleDeferredIssuesAutomatic {
		t.Fatalf("safe assignment detail = %#v", direct)
	}
	if strings.Contains(alphaID, "repo-alpha") || !validCollectionResourceID(alphaID) {
		t.Fatalf("assignment ID = %q", alphaID)
	}

	workflowPage := decodePRLifecycleCollectionTestResponse[prLifecycleWorkflowConfigurationCollectionTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodGet,
			prLifecycleWorkflowConfigurationItemsPath+"?limit=1", nil, nil,
		),
		http.StatusOK,
	)
	if workflowPage.Total != 2 || len(workflowPage.WorkflowConfigurations) != 1 ||
		workflowPage.WorkflowConfigurations[0].Name != "Automatic" ||
		workflowPage.CanonicalQuery != "ALL ORDER BY name ASC" || workflowPage.NextCursor == "" {
		t.Fatalf("workflow first page = %#v", workflowPage)
	}
	if !reflect.DeepEqual(
		collectionSchemaFieldNames(workflowPage.QuerySchema),
		[]string{"id", "name", "is_default", "bindings", "deferred_issues"},
	) {
		t.Fatalf("workflow schema = %#v", workflowPage.QuerySchema)
	}
	workflowFilter := `is_default = true AND bindings >= 0 AND deferred_issues = "ask"`
	workflowFiltered := decodePRLifecycleCollectionTestResponse[prLifecycleWorkflowConfigurationCollectionTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodGet,
			prLifecycleWorkflowConfigurationItemsPath+"?query="+url.QueryEscape(workflowFilter),
			nil, nil,
		),
		http.StatusOK,
	)
	if workflowFiltered.Total != 1 ||
		workflowFiltered.WorkflowConfigurations[0].ID != "default" {
		t.Fatalf("typed workflow filter = %#v", workflowFiltered)
	}

	workflowDirectResponse := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodGet,
		prLifecycleWorkflowConfigurationItemsPath+"/automatic", nil, nil,
	)
	workflowDirect := decodePRLifecycleCollectionTestResponse[prLifecycleWorkflowConfigurationDetailTestResponse](
		t,
		workflowDirectResponse,
		http.StatusOK,
	)
	if workflowDirect.WorkflowConfiguration.ID != "automatic" ||
		workflowDirect.WorkflowConfiguration.Name != "Automatic" ||
		len(workflowDirect.GateCatalog) < 14 || workflowDirect.FlowRevision == "" ||
		workflowDirect.CatalogRevision == "" {
		t.Fatalf("workflow detail = %#v", workflowDirect)
	}
	text := workflowDirectResponse.Body.String()
	for _, legacyKey := range []string{
		`"workflow-ref"`, `"gate-ref"`, `"source-ai-supported"`,
		`"scope-disposition"`, `"deferred-issues"`,
	} {
		if strings.Contains(text, legacyKey) {
			t.Fatalf("workflow collection detail contains legacy key %s: %s", legacyKey, text)
		}
	}
}

func TestPRLifecycleRepositoryAssignmentCollectionCRUDAndBulkFences(t *testing.T) {
	configPath, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	initial := decodePRLifecycleCollectionTestResponse[prLifecycleWorkflowConfigurationCollectionTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodGet, prLifecycleWorkflowConfigurationItemsPath, nil, nil,
		),
		http.StatusOK,
	)
	automaticInput := prLifecycleCollectionWorkflowInput(
		"automatic", "Automatic", config.PRLifecycleDeferredIssuesAutomatic,
	)
	createdWorkflow := decodePRLifecycleCollectionTestResponse[prLifecycleWorkflowConfigurationDetailTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodPost, prLifecycleWorkflowConfigurationItemsPath,
			prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: initial.ConfigRevision,
				WorkflowConfiguration:  &automaticInput,
			}, nil,
		),
		http.StatusCreated,
	)
	if createdWorkflow.ConfigRevision == initial.ConfigRevision ||
		createdWorkflow.Effects.GatewayEffect != "restart_required" ||
		createdWorkflow.WorkflowConfiguration.ID != "automatic" ||
		len(createdWorkflow.GateCatalog) == 0 || createdWorkflow.FlowRevision == "" {
		t.Fatalf("created workflow = %#v", createdWorkflow)
	}

	input := prLifecycleRepositoryAssignmentInput{
		ProviderOrigin: "https://github.com", RepositoryID: "repo-42",
		Repository: "octo/repository", Configuration: "automatic",
		DefaultBranch: "main",
	}
	createResponse := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodPost, prLifecycleRepositoryAssignmentCollectionPath,
		prLifecycleRepositoryAssignmentMutationRequest{
			ExpectedConfigRevision: createdWorkflow.ConfigRevision,
			RepositoryAssignment:   &input,
		}, nil,
	)
	created := decodePRLifecycleCollectionTestResponse[prLifecycleRepositoryAssignmentDetailTestResponse](
		t,
		createResponse,
		http.StatusCreated,
	)
	if created.RepositoryAssignment.Repository != "octo/repository" ||
		created.RepositoryAssignment.Configuration != "automatic" ||
		created.ConfigRevision == createdWorkflow.ConfigRevision ||
		created.WorkflowConfigurations["default"].Name != "Default" ||
		created.Effects.GatewayEffect != "restart_required" {
		t.Fatalf("created assignment = %#v", created)
	}
	if createResponse.Header().Get("Location") !=
		prLifecycleRepositoryAssignmentCollectionPath+"/"+created.RepositoryAssignment.ID {
		t.Fatalf("create Location = %q", createResponse.Header().Get("Location"))
	}

	duplicate := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodPost, prLifecycleRepositoryAssignmentCollectionPath,
		prLifecycleRepositoryAssignmentMutationRequest{
			ExpectedConfigRevision: created.ConfigRevision,
			RepositoryAssignment:   &input,
		}, nil,
	)
	if duplicate.Code != http.StatusConflict ||
		!strings.Contains(duplicate.Body.String(), `"code":"repository_assignment_exists"`) {
		t.Fatalf("duplicate create = %d %s", duplicate.Code, duplicate.Body.String())
	}

	staleInput := input
	staleInput.RepositoryID = "repo-stale"
	staleInput.Repository = "octo/stale"
	stale := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodPost, prLifecycleRepositoryAssignmentCollectionPath,
		prLifecycleRepositoryAssignmentMutationRequest{
			ExpectedConfigRevision: createdWorkflow.ConfigRevision,
			RepositoryAssignment:   &staleInput,
		}, nil,
	)
	if stale.Code != http.StatusConflict ||
		!strings.Contains(stale.Body.String(), `"code":"config_revision_mismatch"`) {
		t.Fatalf("stale create = %d %s", stale.Code, stale.Body.String())
	}

	updateInput := input
	updateInput.Configuration = "default"
	updated := decodePRLifecycleCollectionTestResponse[prLifecycleRepositoryAssignmentDetailTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodPut,
			prLifecycleRepositoryAssignmentCollectionPath+"/"+created.RepositoryAssignment.ID,
			prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: created.ConfigRevision,
				RepositoryAssignment:   &updateInput,
			}, nil,
		),
		http.StatusOK,
	)
	if updated.RepositoryAssignment.Configuration != "default" ||
		updated.RepositoryAssignment.Repository != "octo/repository" ||
		updated.RepositoryAssignment.DefaultBranch != "main" {
		t.Fatalf("updated assignment = %#v", updated)
	}

	changedIdentity := updateInput
	changedIdentity.RepositoryID = "other"
	immutable := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodPut,
		prLifecycleRepositoryAssignmentCollectionPath+"/"+created.RepositoryAssignment.ID,
		prLifecycleRepositoryAssignmentMutationRequest{
			ExpectedConfigRevision: updated.ConfigRevision,
			RepositoryAssignment:   &changedIdentity,
		}, nil,
	)
	if immutable.Code != http.StatusConflict ||
		!strings.Contains(immutable.Body.String(), `"code":"repository_assignment_identity_immutable"`) {
		t.Fatalf("identity mutation = %d %s", immutable.Code, immutable.Body.String())
	}

	secondInput := prLifecycleRepositoryAssignmentInput{
		ProviderOrigin: "https://github.com", RepositoryID: "repo-84",
		Repository: "octo/second", Configuration: "automatic", DefaultBranch: "trunk",
	}
	second := decodePRLifecycleCollectionTestResponse[prLifecycleRepositoryAssignmentDetailTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodPost, prLifecycleRepositoryAssignmentCollectionPath,
			prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: updated.ConfigRevision,
				RepositoryAssignment:   &secondInput,
			}, nil,
		),
		http.StatusCreated,
	)
	missingID, err := prLifecycleRepositoryAssignmentID(
		"https://github.com|missing",
	)
	if err != nil {
		t.Fatal(err)
	}
	bulk := decodePRLifecycleCollectionTestResponse[prLifecycleCollectionDeleteTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodPost,
			prLifecycleRepositoryAssignmentCollectionPath+"/bulk-delete",
			collectionBulkDeleteRequest{
				IDs: []string{
					created.RepositoryAssignment.ID,
					created.RepositoryAssignment.ID,
					"bad/id",
					missingID,
					second.RepositoryAssignment.ID,
				},
				ExpectedConfigRevision: second.ConfigRevision,
			}, nil,
		),
		http.StatusOK,
	)
	if !reflect.DeepEqual(bulk.DeletedIDs, []string{second.RepositoryAssignment.ID}) {
		t.Fatalf("bulk deleted IDs = %#v", bulk.DeletedIDs)
	}
	codes := make([]string, len(bulk.Failures))
	for index, failure := range bulk.Failures {
		codes[index] = failure.Code
	}
	sort.Strings(codes)
	if !reflect.DeepEqual(codes, []string{"duplicate_id", "invalid_id", "not_found"}) {
		t.Fatalf("bulk failures = %#v", bulk.Failures)
	}

	deleted := decodePRLifecycleCollectionTestResponse[prLifecycleCollectionDeleteTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodDelete,
			prLifecycleRepositoryAssignmentCollectionPath+"/"+created.RepositoryAssignment.ID,
			prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: bulk.ConfigRevision,
			}, nil,
		),
		http.StatusOK,
	)
	if !reflect.DeepEqual(deleted.DeletedIDs, []string{created.RepositoryAssignment.ID}) {
		t.Fatalf("delete response = %#v", deleted)
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := config.CanonicalPRLifecycleRepositoryIdentity(
		input.ProviderOrigin, input.RepositoryID,
	)
	if _, assigned := reloaded.PRLifecycle.RepositoryAssignments[identity]; assigned {
		t.Fatal("deleted repository assignment was persisted")
	}
	descriptor := reloaded.PRLifecycle.Repositories[identity]
	if descriptor.Name != "octo/repository" || descriptor.DefaultBranch != "main" {
		t.Fatalf("delete did not preserve descriptor: %#v", descriptor)
	}
}

func TestPRLifecycleWorkflowConfigurationCollectionCRUDDefaultAndBlockers(t *testing.T) {
	configPath, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	initial := decodePRLifecycleCollectionTestResponse[prLifecycleWorkflowConfigurationCollectionTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodGet, prLifecycleWorkflowConfigurationItemsPath, nil, nil,
		),
		http.StatusOK,
	)
	input := prLifecycleCollectionWorkflowInput(
		"automated", "Automated", config.PRLifecycleDeferredIssuesAutomatic,
	)
	input.Bindings = []prLifecycleCollectionGateBinding{{
		WorkflowRef: config.PRLifecycleWorkflowRef,
		GateRef:     "gates.charter-confirm",
		Action:      &prLifecycleCollectionGateAction{Type: "human"},
	}}
	createdResponse := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodPost, prLifecycleWorkflowConfigurationItemsPath,
		prLifecycleWorkflowConfigurationMutationRequest{
			ExpectedConfigRevision: initial.ConfigRevision,
			WorkflowConfiguration:  &input,
		}, nil,
	)
	created := decodePRLifecycleCollectionTestResponse[prLifecycleWorkflowConfigurationDetailTestResponse](
		t,
		createdResponse,
		http.StatusCreated,
	)
	if created.WorkflowConfiguration.ID != "automated" ||
		created.WorkflowConfiguration.IsDefault ||
		len(created.WorkflowConfiguration.Bindings) != 1 ||
		created.ConfigRevision == initial.ConfigRevision ||
		created.Effects.GatewayEffect != "restart_required" ||
		created.FlowRevision == "" || len(created.GateCatalog) == 0 {
		t.Fatalf("created workflow configuration = %#v", created)
	}
	for _, key := range []string{`"workflow_ref"`, `"gate_ref"`, `"scope_disposition"`} {
		if !strings.Contains(createdResponse.Body.String(), key) {
			t.Fatalf("created response omits %s: %s", key, createdResponse.Body.String())
		}
	}

	defaulted := decodePRLifecycleCollectionTestResponse[prLifecycleWorkflowConfigurationDetailTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodPost,
			prLifecycleWorkflowConfigurationItemsPath+"/automated/default",
			prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: created.ConfigRevision,
			}, nil,
		),
		http.StatusOK,
	)
	if !defaulted.WorkflowConfiguration.IsDefault ||
		defaulted.ConfigRevision == created.ConfigRevision {
		t.Fatalf("default response = %#v", defaulted)
	}

	blockedDefault := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodDelete,
		prLifecycleWorkflowConfigurationItemsPath+"/automated",
		prLifecycleCollectionRevisionRequest{
			ExpectedConfigRevision: defaulted.ConfigRevision,
		}, nil,
	)
	if blockedDefault.Code != http.StatusConflict ||
		!strings.Contains(blockedDefault.Body.String(), `"code":"default"`) {
		t.Fatalf("delete default = %d %s", blockedDefault.Code, blockedDefault.Body.String())
	}

	builtinDefault := decodePRLifecycleCollectionTestResponse[prLifecycleWorkflowConfigurationDetailTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodPost,
			prLifecycleWorkflowConfigurationItemsPath+"/default/default",
			prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: defaulted.ConfigRevision,
			}, nil,
		),
		http.StatusOK,
	)

	assignmentInput := prLifecycleRepositoryAssignmentInput{
		ProviderOrigin: "https://github.com", RepositoryID: "assigned-repository",
		Repository: "octo/assigned", Configuration: "automated", DefaultBranch: "main",
	}
	assignment := decodePRLifecycleCollectionTestResponse[prLifecycleRepositoryAssignmentDetailTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodPost, prLifecycleRepositoryAssignmentCollectionPath,
			prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: builtinDefault.ConfigRevision,
				RepositoryAssignment:   &assignmentInput,
			}, nil,
		),
		http.StatusCreated,
	)
	referenced := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodDelete,
		prLifecycleWorkflowConfigurationItemsPath+"/automated",
		prLifecycleCollectionRevisionRequest{
			ExpectedConfigRevision: assignment.ConfigRevision,
		}, nil,
	)
	if referenced.Code != http.StatusConflict ||
		!strings.Contains(referenced.Body.String(), `"code":"referenced"`) ||
		!strings.Contains(referenced.Body.String(), `"octo/assigned"`) {
		t.Fatalf("delete referenced = %d %s", referenced.Code, referenced.Body.String())
	}

	updatedInput := input
	updatedInput.Name = "Automated reviews"
	updatedInput.Bindings = []prLifecycleCollectionGateBinding{}
	updated := decodePRLifecycleCollectionTestResponse[prLifecycleWorkflowConfigurationDetailTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodPut,
			prLifecycleWorkflowConfigurationItemsPath+"/automated",
			prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: assignment.ConfigRevision,
				WorkflowConfiguration:  &updatedInput,
			}, nil,
		),
		http.StatusOK,
	)
	if updated.WorkflowConfiguration.Name != "Automated reviews" ||
		len(updated.WorkflowConfiguration.Bindings) != 0 {
		t.Fatalf("updated workflow configuration = %#v", updated)
	}

	renamed := updatedInput
	renamed.ID = "renamed"
	immutable := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodPut,
		prLifecycleWorkflowConfigurationItemsPath+"/automated",
		prLifecycleWorkflowConfigurationMutationRequest{
			ExpectedConfigRevision: updated.ConfigRevision,
			WorkflowConfiguration:  &renamed,
		}, nil,
	)
	if immutable.Code != http.StatusConflict ||
		!strings.Contains(immutable.Body.String(), `"code":"workflow_configuration_id_immutable"`) {
		t.Fatalf("rename = %d %s", immutable.Code, immutable.Body.String())
	}

	assignmentDelete := decodePRLifecycleCollectionTestResponse[prLifecycleCollectionDeleteTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodDelete,
			prLifecycleRepositoryAssignmentCollectionPath+"/"+assignment.RepositoryAssignment.ID,
			prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: updated.ConfigRevision,
			}, nil,
		),
		http.StatusOK,
	)
	deleted := decodePRLifecycleCollectionTestResponse[prLifecycleCollectionDeleteTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodDelete,
			prLifecycleWorkflowConfigurationItemsPath+"/automated",
			prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: assignmentDelete.ConfigRevision,
			}, nil,
		),
		http.StatusOK,
	)
	if !reflect.DeepEqual(deleted.DeletedIDs, []string{"automated"}) {
		t.Fatalf("deleted workflow = %#v", deleted)
	}
	reloaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := reloaded.PRLifecycle.WorkflowConfigurations["automated"]; exists {
		t.Fatal("deleted workflow configuration was persisted")
	}
	if reloaded.PRLifecycle.DefaultWorkflowConfigurationID != "default" {
		t.Fatalf("persisted default = %q", reloaded.PRLifecycle.DefaultWorkflowConfigurationID)
	}

	builtinDelete := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodDelete,
		prLifecycleWorkflowConfigurationItemsPath+"/default",
		prLifecycleCollectionRevisionRequest{
			ExpectedConfigRevision: deleted.ConfigRevision,
		}, nil,
	)
	if builtinDelete.Code != http.StatusConflict ||
		!strings.Contains(builtinDelete.Body.String(), `"code":"default"`) {
		t.Fatalf("delete built-in = %d %s", builtinDelete.Code, builtinDelete.Body.String())
	}
}

func TestPRLifecycleAdministrativeCollectionErrorsAreStrictBoundedAndUTF8Aware(t *testing.T) {
	_, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	current := decodePRLifecycleCollectionTestResponse[prLifecycleWorkflowConfigurationCollectionTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodGet, prLifecycleWorkflowConfigurationItemsPath, nil, nil,
		),
		http.StatusOK,
	)
	input := prLifecycleCollectionWorkflowInput(
		"strict", "Strict", config.PRLifecycleDeferredIssuesAsk,
	)
	validBody := prLifecycleWorkflowConfigurationMutationRequest{
		ExpectedConfigRevision: current.ConfigRevision,
		WorkflowConfiguration:  &input,
	}

	crossOrigin := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodPost, prLifecycleWorkflowConfigurationItemsPath,
		validBody,
		func(request *http.Request) {
			request.Header.Set("Sec-Fetch-Site", "cross-site")
		},
	)
	if crossOrigin.Code != http.StatusForbidden ||
		!strings.Contains(crossOrigin.Body.String(), `"code":"cross_origin_mutation"`) {
		t.Fatalf("cross-origin response = %d %s", crossOrigin.Code, crossOrigin.Body.String())
	}

	unknownBody := map[string]any{
		"expected_config_revision": current.ConfigRevision,
		"workflow_configuration":   input,
		"unexpected":               true,
	}
	unknown := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodPost, prLifecycleWorkflowConfigurationItemsPath,
		unknownBody, nil,
	)
	if unknown.Code != http.StatusBadRequest ||
		!strings.Contains(unknown.Body.String(), `"code":"invalid_collection_request"`) {
		t.Fatalf("unknown field response = %d %s", unknown.Code, unknown.Body.String())
	}

	duplicateContentType := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodPost, prLifecycleWorkflowConfigurationItemsPath,
		validBody,
		func(request *http.Request) {
			request.Header.Add("Content-Type", "application/json")
		},
	)
	if duplicateContentType.Code != http.StatusUnsupportedMediaType ||
		!strings.Contains(duplicateContentType.Body.String(), `"code":"json_content_type_required"`) {
		t.Fatalf(
			"duplicate content type response = %d %s",
			duplicateContentType.Code,
			duplicateContentType.Body.String(),
		)
	}

	encodedValidBody, err := json.Marshal(validBody)
	if err != nil {
		t.Fatal(err)
	}
	largeValidBody := append(
		encodedValidBody,
		bytes.Repeat([]byte{' '}, collectionMutationMaxBytes+128)...,
	)
	largeRequest := httptest.NewRequest(
		http.MethodPost,
		"http://launcher.local"+prLifecycleWorkflowConfigurationItemsPath,
		bytes.NewReader(largeValidBody),
	)
	largeRequest.Header.Set("Content-Type", "application/json")
	largeRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	largeResponse := httptest.NewRecorder()
	mux.ServeHTTP(largeResponse, largeRequest)
	if largeResponse.Code != http.StatusCreated {
		t.Fatalf(
			"valid workflow body above shared 1 MiB limit = %d %s",
			largeResponse.Code,
			largeResponse.Body.String(),
		)
	}

	query := `repository = "répo" AND`
	_, parseErr := collectionquery.Parse(
		query, prLifecycleRepositoryAssignmentCollectionSchema,
	)
	var queryError *collectionquery.QueryError
	if !errors.As(parseErr, &queryError) {
		t.Fatalf("expected query error, got %v", parseErr)
	}
	invalidQuery := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodGet,
		prLifecycleRepositoryAssignmentCollectionPath+"?query="+url.QueryEscape(query),
		nil, nil,
	)
	var invalidQueryBody struct {
		Code     string `json:"code"`
		Position int    `json:"position"`
		Message  string `json:"message"`
	}
	if unmarshalErr := json.Unmarshal(invalidQuery.Body.Bytes(), &invalidQueryBody); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if invalidQuery.Code != http.StatusBadRequest || invalidQueryBody.Code != "invalid_query" ||
		invalidQueryBody.Position != queryError.Position ||
		invalidQueryBody.Position <= len([]rune(query)) || len(invalidQueryBody.Message) > 512 {
		t.Fatalf("UTF-8 query response = %d %#v, parser=%#v", invalidQuery.Code, invalidQueryBody, queryError)
	}

	for _, path := range []string{
		prLifecycleRepositoryAssignmentCollectionPath + "?limit=201",
		prLifecycleWorkflowConfigurationItemsPath + "?unexpected=1",
	} {
		response := prLifecycleCollectionRequestForTest(t, mux, http.MethodGet, path, nil, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid list %q = %d %s", path, response.Code, response.Body.String())
		}
	}

	invalidID := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodGet,
		prLifecycleRepositoryAssignmentCollectionPath+"/not-an-opaque-id", nil, nil,
	)
	if invalidID.Code != http.StatusBadRequest ||
		!strings.Contains(invalidID.Body.String(), `"code":"invalid_repository_assignment_id"`) {
		t.Fatalf("invalid assignment ID = %d %s", invalidID.Code, invalidID.Body.String())
	}
	missingID, err := prLifecycleRepositoryAssignmentID("https://github.com|missing")
	if err != nil {
		t.Fatal(err)
	}
	missing := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodGet,
		prLifecycleRepositoryAssignmentCollectionPath+"/"+missingID, nil, nil,
	)
	if missing.Code != http.StatusNotFound ||
		!strings.Contains(missing.Body.String(), `"code":"repository_assignment_not_found"`) {
		t.Fatalf("missing assignment = %d %s", missing.Code, missing.Body.String())
	}

	oversizedRequest := httptest.NewRequest(
		http.MethodPost,
		"http://launcher.local"+prLifecycleWorkflowConfigurationItemsPath,
		strings.NewReader(strings.Repeat(" ", prLifecycleRequestMaxBytes+1)),
	)
	oversizedRequest.Header.Set("Content-Type", "application/json")
	oversizedRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	oversizedResponse := httptest.NewRecorder()
	mux.ServeHTTP(oversizedResponse, oversizedRequest)
	if oversizedResponse.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(oversizedResponse.Body.String(), `"code":"collection_request_too_large"`) {
		t.Fatalf(
			"oversized response = %d %s",
			oversizedResponse.Code,
			oversizedResponse.Body.String(),
		)
	}
}

func TestPRLifecycleCollectionCloneAndReferenceBounds(t *testing.T) {
	identity, err := config.CanonicalPRLifecycleRepositoryIdentity(
		"https://github.com", "repo",
	)
	if err != nil {
		t.Fatal(err)
	}
	source := config.DefaultPRLifecycleConfig()
	workflow := source.WorkflowConfigurations["default"]
	workflow.Bindings = []config.PRLifecycleGateBinding{{
		WorkflowRef: config.PRLifecycleWorkflowRef,
		GateRef:     "gates.review-start",
		Action: &gatetypes.GateAction{
			Type: gatetypes.GateActionDeterministic,
			Fields: map[string]any{
				"nested": map[string]any{"values": []any{"original"}},
			},
		},
	}}
	workflow.ScopeDisposition.ByType["fix"] = config.PRLifecycleScopeDispositionRule{
		Mode: config.PRLifecycleScopeRelaxed, Prompt: "original",
	}
	source.WorkflowConfigurations["default"] = workflow
	source.Repositories[identity] = config.PRLifecycleRepositoryDescriptor{
		Name: "octo/repo", DefaultBranch: "main",
	}
	source.RepositoryAssignments[identity] = "default"

	clone := clonePRLifecycleCollectionConfig(source)
	clone.Repositories[identity] = config.PRLifecycleRepositoryDescriptor{
		Name: "changed/repo", DefaultBranch: "changed",
	}
	clone.RepositoryAssignments[identity] = "changed"
	clonedWorkflow := clone.WorkflowConfigurations["default"]
	clonedWorkflow.Bindings[0].Action.Fields["nested"].(map[string]any)["values"].([]any)[0] = "changed"
	clonedWorkflow.ScopeDisposition.ByType["fix"] = config.PRLifecycleScopeDispositionRule{
		Mode: config.PRLifecycleScopeStrict, Prompt: "changed",
	}
	clone.WorkflowConfigurations["default"] = clonedWorkflow

	originalWorkflow := source.WorkflowConfigurations["default"]
	if source.Repositories[identity].Name != "octo/repo" ||
		source.RepositoryAssignments[identity] != "default" ||
		originalWorkflow.Bindings[0].Action.Fields["nested"].(map[string]any)["values"].([]any)[0] != "original" ||
		originalWorkflow.ScopeDisposition.ByType["fix"].Prompt != "original" {
		t.Fatalf("source changed through clone: %#v", source)
	}

	bounded := config.DefaultPRLifecycleConfig()
	bounded.WorkflowConfigurations["custom"] = config.PRLifecycleWorkflowConfiguration{
		Name: "Custom", Bindings: []config.PRLifecycleGateBinding{},
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{
			Mode: config.PRLifecycleDeferredIssuesAsk,
		},
	}
	for index := 0; index < prLifecycleReferenceBlockerLimit+12; index++ {
		repositoryID := "repo-" + strconv.Itoa(index)
		candidate, canonicalErr := config.CanonicalPRLifecycleRepositoryIdentity(
			"https://github.com", repositoryID,
		)
		if canonicalErr != nil {
			t.Fatal(canonicalErr)
		}
		bounded.RepositoryAssignments[candidate] = "custom"
		bounded.Repositories[candidate] = config.PRLifecycleRepositoryDescriptor{
			Name: "octo/" + repositoryID, DefaultBranch: "main",
		}
	}
	blockers := prLifecycleWorkflowConfigurationReferenceBlockers(bounded, "custom")
	if len(blockers) != prLifecycleReferenceBlockerLimit || !sort.StringsAreSorted(blockers) {
		t.Fatalf("bounded blockers = %#v", blockers)
	}

	noncanonical := config.DefaultPRLifecycleConfig()
	noncanonicalIdentity := "https://GitHub.com/|Repo-X"
	noncanonical.RepositoryAssignments[noncanonicalIdentity] = "default"
	noncanonical.Repositories[noncanonicalIdentity] = config.PRLifecycleRepositoryDescriptor{
		Name: "octo/repo-x", DefaultBranch: "main",
	}
	items, projectionErr := projectPRLifecycleRepositoryAssignmentItems(noncanonical)
	if projectionErr != nil || len(items) != 1 {
		t.Fatalf("noncanonical projection = %#v, %v", items, projectionErr)
	}
	wantCanonical, _ := config.CanonicalPRLifecycleRepositoryIdentity(
		"https://GitHub.com/", "Repo-X",
	)
	wantID, _ := prLifecycleRepositoryAssignmentID(wantCanonical)
	if items[0].CanonicalIdentity != wantCanonical || items[0].Summary.ID != wantID ||
		items[0].Resource.ProviderOrigin != "https://github.com" ||
		items[0].Resource.RepositoryID != "repo-x" {
		t.Fatalf("canonicalized projection = %#v", items[0])
	}
}

func TestPRLifecycleCollectionHelperBoundaries(t *testing.T) {
	if cloned := clonePRLifecycleJSONMap(nil); cloned != nil {
		t.Fatalf("nil JSON map clone = %#v", cloned)
	}
	jsonSource := map[string]any{
		"strings": []string{"one"},
		"map":     map[string]string{"key": "value"},
		"slice":   []any{map[string]any{"nested": true}},
		"number":  42,
	}
	jsonClone := clonePRLifecycleJSONMap(jsonSource)
	jsonClone["strings"].([]string)[0] = "changed"
	jsonClone["map"].(map[string]string)["key"] = "changed"
	jsonClone["slice"].([]any)[0].(map[string]any)["nested"] = false
	if jsonSource["strings"].([]string)[0] != "one" ||
		jsonSource["map"].(map[string]string)["key"] != "value" ||
		jsonSource["slice"].([]any)[0].(map[string]any)["nested"] != true ||
		jsonClone["number"] != 42 {
		t.Fatalf("JSON clone aliases source: source=%#v clone=%#v", jsonSource, jsonClone)
	}

	for _, test := range []struct {
		identity string
		valid    bool
	}{
		{identity: "https://github.com|repo", valid: true},
		{identity: "missing-separator", valid: false},
		{identity: "http://github.com|repo", valid: false},
		{identity: "https://github.com|repo|extra", valid: false},
	} {
		canonical, _, _, valid := canonicalPRLifecycleRepositoryIdentityParts(test.identity)
		if valid != test.valid || valid && canonical == "" {
			t.Fatalf("identity %q = %q,%v", test.identity, canonical, valid)
		}
		_, idErr := prLifecycleRepositoryAssignmentID(test.identity)
		if (idErr == nil) != test.valid {
			t.Fatalf("identity ID %q error = %v", test.identity, idErr)
		}
	}

	for _, test := range []struct {
		id    string
		valid bool
	}{
		{id: "default", valid: true},
		{id: "a-1", valid: true},
		{id: "", valid: false},
		{id: "A", valid: false},
		{id: "a-", valid: false},
		{id: "a--b", valid: false},
		{id: "a_b", valid: false},
		{id: strings.Repeat("a", 65), valid: false},
	} {
		if valid := validPRLifecycleWorkflowConfigurationItemID(test.id); valid != test.valid {
			t.Fatalf("workflow ID %q valid=%v, want %v", test.id, valid, test.valid)
		}
	}

	canonical := "https://github.com|repo"
	assignmentID, err := prLifecycleRepositoryAssignmentID(canonical)
	if err != nil {
		t.Fatal(err)
	}
	assignmentItem := prLifecycleRepositoryAssignmentItem{
		Identity: canonical, CanonicalIdentity: canonical,
		Summary: prLifecycleRepositoryAssignmentSummary{
			ID: assignmentID, Repository: "octo/repo",
			Configuration: "default", DefaultBranch: "main",
		},
	}
	assignmentOptions := prLifecycleRepositoryAssignmentPageOptions()
	for _, field := range []collectionquery.Field{
		"repository", "configuration", "default_branch",
	} {
		if _, ok := assignmentOptions.Resolve(assignmentItem, field, time.Time{}); !ok {
			t.Fatalf("assignment resolver rejected %q", field)
		}
	}
	if _, ok := assignmentOptions.Resolve(assignmentItem, "unknown", time.Time{}); ok {
		t.Fatal("assignment resolver accepted unknown field")
	}
	if _, ok := findPRLifecycleRepositoryAssignment(
		[]prLifecycleRepositoryAssignmentItem{assignmentItem}, "invalid",
	); ok {
		t.Fatal("invalid assignment ID resolved")
	}

	workflowItem := prLifecycleWorkflowConfigurationItem{
		Summary: prLifecycleWorkflowConfigurationSummaryItem{
			ID: "default", Name: "Default", IsDefault: true,
			Bindings: 0, DeferredIssues: "ask",
		},
	}
	workflowOptions := prLifecycleWorkflowConfigurationPageOptions()
	for _, field := range []collectionquery.Field{
		"id", "name", "is_default", "bindings", "deferred_issues",
	} {
		if _, ok := workflowOptions.Resolve(workflowItem, field, time.Time{}); !ok {
			t.Fatalf("workflow resolver rejected %q", field)
		}
	}
	if _, ok := workflowOptions.Resolve(workflowItem, "unknown", time.Time{}); ok {
		t.Fatal("workflow resolver accepted unknown field")
	}
	if _, ok := findPRLifecycleWorkflowConfigurationItem(
		[]prLifecycleWorkflowConfigurationItem{workflowItem}, "invalid_id",
	); ok {
		t.Fatal("invalid workflow ID resolved")
	}

	if prLifecycleCollectionScopeDispositionFromConfig(
		config.PRLifecycleScopeDispositionConfig{},
	) != nil {
		t.Fatal("zero scope disposition should be omitted")
	}
	scope := prLifecycleCollectionScopeDispositionFromConfig(
		config.PRLifecycleScopeDispositionConfig{
			Default: config.PRLifecycleScopeDispositionRule{Mode: config.PRLifecycleScopeStrict},
		},
	)
	if scope == nil || scope.ByType == nil || len(scope.ByType) != 0 {
		t.Fatalf("scope projection = %#v", scope)
	}
	if clonePRLifecycleScopeDispositionRules(nil) != nil ||
		prLifecycleCollectionGateActionFromConfig(nil) != nil ||
		prLifecycleConfigGateActionFromCollection(nil) != nil ||
		prLifecycleConfigGateBindings(nil) != nil {
		t.Fatal("nil collection clone boundary changed")
	}
	clonedRules := clonePRLifecycleScopeDispositionRules(
		map[string]config.PRLifecycleScopeDispositionRule{
			"fix": {Mode: config.PRLifecycleScopeRelaxed, Prompt: "copy"},
		},
	)
	if clonedRules["fix"].Prompt != "copy" {
		t.Fatalf("cloned scope rules = %#v", clonedRules)
	}

	collision := config.DefaultPRLifecycleConfig()
	collision.RepositoryAssignments["https://github.com|repo"] = "default"
	collision.RepositoryAssignments["https://GitHub.com/|REPO"] = "default"
	if _, err = projectPRLifecycleRepositoryAssignmentItems(collision); err == nil {
		t.Fatal("canonical assignment collision was accepted")
	}
	invalid := config.DefaultPRLifecycleConfig()
	invalid.RepositoryAssignments["invalid"] = "default"
	if _, err = projectPRLifecycleRepositoryAssignmentItems(invalid); err == nil {
		t.Fatal("invalid assignment identity was accepted")
	}
	if code, blockers := prLifecycleWorkflowConfigurationDeleteBlockers(
		config.DefaultPRLifecycleConfig(), "custom",
	); code != "" || blockers != nil {
		t.Fatalf("unreferenced delete blockers = %q %#v", code, blockers)
	}
	fallbacks := config.DefaultPRLifecycleConfig()
	fallbacks.WorkflowConfigurations["custom"] = config.PRLifecycleWorkflowConfiguration{
		Name: "Custom", Bindings: []config.PRLifecycleGateBinding{},
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{
			Mode: config.PRLifecycleDeferredIssuesAsk,
		},
	}
	fallbacks.RepositoryAssignments["https://github.com|repo-fallback"] = "custom"
	fallbacks.RepositoryAssignments["invalid"] = "custom"
	fallbacks.RepositoryAssignments["https://github.com|other"] = "default"
	fallbackBlockers := prLifecycleWorkflowConfigurationReferenceBlockers(
		fallbacks, "custom",
	)
	if !reflect.DeepEqual(fallbackBlockers, []string{"repo-fallback", "repository"}) {
		t.Fatalf("fallback blockers = %#v", fallbackBlockers)
	}
}

func TestPRLifecycleCollectionMutationFailureMatrix(t *testing.T) {
	configPath, handler, mux := prLifecycleWorkflowConfigurationTestServer(t)
	descriptorIdentity, _ := config.CanonicalPRLifecycleRepositoryIdentity(
		"https://github.com", "descriptor-only",
	)
	assignedIdentity, _ := config.CanonicalPRLifecycleRepositoryIdentity(
		"https://github.com", "assigned",
	)
	savePRLifecycleCollectionTestConfig(t, configPath, func(lifecycle *config.PRLifecycleConfig) {
		lifecycle.WorkflowConfigurations["custom"] = config.PRLifecycleWorkflowConfiguration{
			Name: "Custom", Bindings: []config.PRLifecycleGateBinding{},
			DeferredIssues: config.PRLifecycleDeferredIssueConfig{
				Mode: config.PRLifecycleDeferredIssuesAsk,
			},
		}
		lifecycle.Repositories[descriptorIdentity] = config.PRLifecycleRepositoryDescriptor{
			Name: "octo/descriptor", DefaultBranch: "main",
		}
		lifecycle.Repositories[assignedIdentity] = config.PRLifecycleRepositoryDescriptor{
			Name: "octo/assigned", DefaultBranch: "main",
		}
		lifecycle.RepositoryAssignments[assignedIdentity] = "default"
	})
	current := decodePRLifecycleCollectionTestResponse[prLifecycleRepositoryAssignmentCollectionTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodGet, prLifecycleRepositoryAssignmentCollectionPath, nil, nil,
		),
		http.StatusOK,
	)
	assignedID, _ := prLifecycleRepositoryAssignmentID(assignedIdentity)
	missingID, _ := prLifecycleRepositoryAssignmentID("https://github.com|missing")

	validRepository := prLifecycleRepositoryAssignmentInput{
		ProviderOrigin: "https://github.com", RepositoryID: "new",
		Repository: "octo/new", Configuration: "default", DefaultBranch: "main",
	}
	invalidIdentity := validRepository
	invalidIdentity.ProviderOrigin = "http://github.com"
	invalidDescriptor := validRepository
	invalidDescriptor.RepositoryID = "invalid-descriptor"
	invalidDescriptor.DefaultBranch = ""
	missingConfiguration := validRepository
	missingConfiguration.RepositoryID = "missing-configuration"
	missingConfiguration.Repository = "octo/missing-configuration"
	missingConfiguration.Configuration = "missing"
	descriptorMismatch := validRepository
	descriptorMismatch.RepositoryID = "descriptor-only"
	descriptorMismatch.Repository = "other/descriptor"
	updateMissingConfiguration := prLifecycleRepositoryAssignmentInput{
		ProviderOrigin: "https://github.com", RepositoryID: "assigned",
		Repository: "octo/assigned", Configuration: "missing", DefaultBranch: "main",
	}

	tests := []struct {
		name   string
		method string
		path   string
		body   any
		status int
		code   string
	}{
		{
			name: "missing repository object", method: http.MethodPost,
			path: prLifecycleRepositoryAssignmentCollectionPath,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
			},
			status: http.StatusBadRequest, code: "invalid_repository_assignment",
		},
		{
			name: "invalid repository identity", method: http.MethodPost,
			path: prLifecycleRepositoryAssignmentCollectionPath,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &invalidIdentity,
			},
			status: http.StatusUnprocessableEntity, code: "invalid_repository_assignment",
		},
		{
			name: "invalid descriptor", method: http.MethodPost,
			path: prLifecycleRepositoryAssignmentCollectionPath,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &invalidDescriptor,
			},
			status: http.StatusUnprocessableEntity, code: "invalid_repository_descriptor",
		},
		{
			name: "missing selected configuration", method: http.MethodPost,
			path: prLifecycleRepositoryAssignmentCollectionPath,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &missingConfiguration,
			},
			status: http.StatusUnprocessableEntity, code: "workflow_configuration_not_found",
		},
		{
			name: "descriptor mismatch", method: http.MethodPost,
			path: prLifecycleRepositoryAssignmentCollectionPath,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &descriptorMismatch,
			},
			status: http.StatusConflict, code: "repository_descriptor_mismatch",
		},
		{
			name: "update missing assignment", method: http.MethodPut,
			path: prLifecycleRepositoryAssignmentCollectionPath + "/" + missingID,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &validRepository,
			},
			status: http.StatusNotFound, code: "repository_assignment_not_found",
		},
		{
			name: "update missing configuration", method: http.MethodPut,
			path: prLifecycleRepositoryAssignmentCollectionPath + "/" + assignedID,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &updateMissingConfiguration,
			},
			status: http.StatusUnprocessableEntity, code: "workflow_configuration_not_found",
		},
		{
			name: "delete missing assignment", method: http.MethodDelete,
			path: prLifecycleRepositoryAssignmentCollectionPath + "/" + missingID,
			body: prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: current.ConfigRevision,
			},
			status: http.StatusNotFound, code: "repository_assignment_not_found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := prLifecycleCollectionRequestForTest(
				t, mux, test.method, test.path, test.body, nil,
			)
			if response.Code != test.status ||
				!strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}

	for _, ids := range [][]string{nil, make([]string, collectionquery.MaxPageSize+1)} {
		response := prLifecycleCollectionRequestForTest(
			t, mux, http.MethodPost,
			prLifecycleRepositoryAssignmentCollectionPath+"/bulk-delete",
			collectionBulkDeleteRequest{
				IDs: ids, ExpectedConfigRevision: current.ConfigRevision,
			}, nil,
		)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), `"code":"invalid_bulk_delete"`) {
			t.Fatalf("invalid bulk size = %d %s", response.Code, response.Body.String())
		}
	}
	conflictingBulkRevision := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodPost,
		prLifecycleRepositoryAssignmentCollectionPath+"/bulk-delete",
		collectionBulkDeleteRequest{
			IDs: []string{assignedID}, ConfigRevision: "first",
			ExpectedConfigRevision: "second",
		}, nil,
	)
	if conflictingBulkRevision.Code != http.StatusBadRequest ||
		!strings.Contains(
			conflictingBulkRevision.Body.String(),
			`"code":"conflicting_config_revision"`,
		) {
		t.Fatalf(
			"conflicting bulk revision = %d %s",
			conflictingBulkRevision.Code,
			conflictingBulkRevision.Body.String(),
		)
	}
	missingBulk := decodePRLifecycleCollectionTestResponse[prLifecycleCollectionDeleteTestResponse](
		t,
		prLifecycleCollectionRequestForTest(
			t, mux, http.MethodPost,
			prLifecycleRepositoryAssignmentCollectionPath+"/bulk-delete",
			collectionBulkDeleteRequest{
				IDs: []string{missingID}, ExpectedConfigRevision: current.ConfigRevision,
			}, nil,
		),
		http.StatusOK,
	)
	if len(missingBulk.DeletedIDs) != 0 || len(missingBulk.Failures) != 1 ||
		missingBulk.Failures[0].Code != "not_found" ||
		missingBulk.ConfigRevision != current.ConfigRevision {
		t.Fatalf("missing-only bulk = %#v", missingBulk)
	}

	existingWorkflow := prLifecycleCollectionWorkflowInput(
		"custom", "Custom", config.PRLifecycleDeferredIssuesAsk,
	)
	invalidWorkflow := prLifecycleCollectionWorkflowInput(
		"invalid-config", "", config.PRLifecycleDeferredIssuesAsk,
	)
	missingWorkflow := prLifecycleCollectionWorkflowInput(
		"missing", "Missing", config.PRLifecycleDeferredIssuesAsk,
	)
	invalidCustomWorkflow := prLifecycleCollectionWorkflowInput(
		"custom", "", config.PRLifecycleDeferredIssuesAsk,
	)
	workflowTests := []struct {
		name   string
		method string
		path   string
		body   any
		status int
		code   string
	}{
		{
			name: "missing workflow object", method: http.MethodPost,
			path: prLifecycleWorkflowConfigurationItemsPath,
			body: prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
			},
			status: http.StatusBadRequest, code: "invalid_workflow_configuration",
		},
		{
			name: "invalid workflow ID", method: http.MethodPost,
			path: prLifecycleWorkflowConfigurationItemsPath,
			body: prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				WorkflowConfiguration: &prLifecycleWorkflowConfigurationInput{
					ID: "Invalid",
				},
			},
			status: http.StatusUnprocessableEntity, code: "invalid_workflow_configuration",
		},
		{
			name: "existing workflow", method: http.MethodPost,
			path: prLifecycleWorkflowConfigurationItemsPath,
			body: prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				WorkflowConfiguration:  &existingWorkflow,
			},
			status: http.StatusConflict, code: "workflow_configuration_exists",
		},
		{
			name: "invalid workflow body", method: http.MethodPost,
			path: prLifecycleWorkflowConfigurationItemsPath,
			body: prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				WorkflowConfiguration:  &invalidWorkflow,
			},
			status: http.StatusUnprocessableEntity, code: "invalid_workflow_configuration",
		},
		{
			name: "update missing workflow", method: http.MethodPut,
			path: prLifecycleWorkflowConfigurationItemsPath + "/missing",
			body: prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				WorkflowConfiguration:  &missingWorkflow,
			},
			status: http.StatusNotFound, code: "workflow_configuration_not_found",
		},
		{
			name: "update invalid workflow", method: http.MethodPut,
			path: prLifecycleWorkflowConfigurationItemsPath + "/custom",
			body: prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				WorkflowConfiguration:  &invalidCustomWorkflow,
			},
			status: http.StatusUnprocessableEntity, code: "invalid_workflow_configuration",
		},
		{
			name: "delete missing workflow", method: http.MethodDelete,
			path: prLifecycleWorkflowConfigurationItemsPath + "/missing",
			body: prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: current.ConfigRevision,
			},
			status: http.StatusNotFound, code: "workflow_configuration_not_found",
		},
		{
			name: "default missing workflow", method: http.MethodPost,
			path: prLifecycleWorkflowConfigurationItemsPath + "/missing/default",
			body: prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: current.ConfigRevision,
			},
			status: http.StatusNotFound, code: "workflow_configuration_not_found",
		},
	}
	for _, test := range workflowTests {
		t.Run(test.name, func(t *testing.T) {
			response := prLifecycleCollectionRequestForTest(
				t, mux, test.method, test.path, test.body, nil,
			)
			if response.Code != test.status ||
				!strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}

	invalidWorkflowPath := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodGet,
		prLifecycleWorkflowConfigurationItemsPath+"/Invalid", nil, nil,
	)
	if invalidWorkflowPath.Code != http.StatusBadRequest ||
		!strings.Contains(
			invalidWorkflowPath.Body.String(),
			`"code":"invalid_workflow_configuration_id"`,
		) {
		t.Fatalf(
			"invalid workflow path = %d %s",
			invalidWorkflowPath.Code,
			invalidWorkflowPath.Body.String(),
		)
	}
	missingWorkflowRead := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodGet,
		prLifecycleWorkflowConfigurationItemsPath+"/missing", nil, nil,
	)
	if missingWorkflowRead.Code != http.StatusNotFound ||
		!strings.Contains(
			missingWorkflowRead.Body.String(),
			`"code":"workflow_configuration_not_found"`,
		) {
		t.Fatalf(
			"missing workflow read = %d %s",
			missingWorkflowRead.Code,
			missingWorkflowRead.Body.String(),
		)
	}

	cfg, revision, err := config.LoadCurrentConfigForUpdateSnapshot(configPath)
	if err != nil {
		t.Fatal(err)
	}
	candidate := clonePRLifecycleCollectionConfig(cfg.PRLifecycle.Effective())
	candidate.WorkflowConfigurations["save-race"] = config.PRLifecycleWorkflowConfiguration{
		Name: "Save race", Bindings: []config.PRLifecycleGateBinding{},
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{
			Mode: config.PRLifecycleDeferredIssuesAsk,
		},
	}
	saveError := httptest.NewRecorder()
	if nextRevision, ok := savePRLifecycleCollectionCandidate(
		handler, saveError, cfg, candidate, revision+"-stale",
	); ok || nextRevision != "" || saveError.Code != http.StatusConflict ||
		!strings.Contains(saveError.Body.String(), `"code":"config_revision_mismatch"`) {
		t.Fatalf("save race = %q,%v %d %s", nextRevision, ok, saveError.Code, saveError.Body.String())
	}

	validCustomWorkflow := prLifecycleCollectionWorkflowInput(
		"custom", "Custom", config.PRLifecycleDeferredIssuesAsk,
	)
	newWorkflow := prLifecycleCollectionWorkflowInput(
		"forced", "Forced", config.PRLifecycleDeferredIssuesAsk,
	)
	newRepository := prLifecycleRepositoryAssignmentInput{
		ProviderOrigin: "https://github.com", RepositoryID: "forced",
		Repository: "octo/forced", Configuration: "default", DefaultBranch: "main",
	}
	assignedRepository := prLifecycleRepositoryAssignmentInput{
		ProviderOrigin: "https://github.com", RepositoryID: "assigned",
		Repository: "octo/assigned", Configuration: "default", DefaultBranch: "main",
	}

	originalProjector := handler.projectPRLifecycleRepositoryAssignmentItems
	handler.projectPRLifecycleRepositoryAssignmentItems = func(
		config.PRLifecycleConfig,
	) ([]prLifecycleRepositoryAssignmentItem, error) {
		return nil, errors.New("forced projection error")
	}
	projectionRequests := []struct {
		method string
		path   string
		body   any
	}{
		{method: http.MethodGet, path: prLifecycleRepositoryAssignmentCollectionPath},
		{
			method: http.MethodGet,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/" + assignedID,
		},
		{
			method: http.MethodPost, path: prLifecycleRepositoryAssignmentCollectionPath,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &newRepository,
			},
		},
		{
			method: http.MethodPut,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/" + assignedID,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &assignedRepository,
			},
		},
		{
			method: http.MethodDelete,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/" + assignedID,
			body: prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: current.ConfigRevision,
			},
		},
		{
			method: http.MethodPost,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/bulk-delete",
			body: collectionBulkDeleteRequest{
				IDs: []string{assignedID}, ExpectedConfigRevision: current.ConfigRevision,
			},
		},
	}
	for _, request := range projectionRequests {
		response := prLifecycleCollectionRequestForTest(
			t, mux, request.method, request.path, request.body, nil,
		)
		if response.Code != http.StatusInternalServerError ||
			!strings.Contains(response.Body.String(), `"code":"repository_assignment_projection_failed"`) {
			t.Fatalf("forced projection = %d %s", response.Code, response.Body.String())
		}
	}
	handler.projectPRLifecycleRepositoryAssignmentItems = originalProjector
	newRepositoryIdentity, _ := config.CanonicalPRLifecycleRepositoryIdentity(
		newRepository.ProviderOrigin, newRepository.RepositoryID,
	)
	newRepositoryID, _ := prLifecycleRepositoryAssignmentID(newRepositoryIdentity)
	lateProjectionRequests := []struct {
		method    string
		path      string
		body      any
		missingID string
	}{
		{
			method: http.MethodPost, path: prLifecycleRepositoryAssignmentCollectionPath,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &newRepository,
			},
			missingID: newRepositoryID,
		},
		{
			method: http.MethodPut,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/" + assignedID,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &assignedRepository,
			},
			missingID: assignedID,
		},
	}
	for _, request := range lateProjectionRequests {
		for _, omitItem := range []bool{false, true} {
			calls := 0
			handler.projectPRLifecycleRepositoryAssignmentItems = func(
				lifecycle config.PRLifecycleConfig,
			) ([]prLifecycleRepositoryAssignmentItem, error) {
				calls++
				items, projectionErr := originalProjector(lifecycle)
				if projectionErr != nil || calls != 2 {
					return items, projectionErr
				}
				if !omitItem {
					return nil, errors.New("forced late projection error")
				}
				filtered := make([]prLifecycleRepositoryAssignmentItem, 0, len(items))
				for _, item := range items {
					if item.Summary.ID != request.missingID {
						filtered = append(filtered, item)
					}
				}
				return filtered, nil
			}
			response := prLifecycleCollectionRequestForTest(
				t, mux, request.method, request.path, request.body, nil,
			)
			if response.Code != http.StatusInternalServerError ||
				!strings.Contains(
					response.Body.String(),
					`"code":"repository_assignment_projection_failed"`,
				) {
				t.Fatalf("forced late projection = %d %s", response.Code, response.Body.String())
			}
		}
	}
	handler.projectPRLifecycleRepositoryAssignmentItems = originalProjector

	originalValidator := handler.validatePRLifecycleCollectionCandidate
	handler.validatePRLifecycleCollectionCandidate = func(
		context.Context,
		config.PRLifecycleConfig,
		*config.Config,
	) error {
		return errors.New("forced candidate error")
	}
	failureMutations := []struct {
		method string
		path   string
		body   any
		code   string
	}{
		{
			method: http.MethodPost, path: prLifecycleRepositoryAssignmentCollectionPath,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &newRepository,
			},
			code: "invalid_repository_assignment",
		},
		{
			method: http.MethodPut,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/" + assignedID,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				RepositoryAssignment:   &assignedRepository,
			},
			code: "invalid_repository_assignment",
		},
		{
			method: http.MethodDelete,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/" + assignedID,
			body: prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: current.ConfigRevision,
			},
			code: "invalid_repository_assignment",
		},
		{
			method: http.MethodPost,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/bulk-delete",
			body: collectionBulkDeleteRequest{
				IDs: []string{assignedID}, ExpectedConfigRevision: current.ConfigRevision,
			},
			code: "invalid_repository_assignment",
		},
		{
			method: http.MethodPost, path: prLifecycleWorkflowConfigurationItemsPath,
			body: prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				WorkflowConfiguration:  &newWorkflow,
			},
			code: "invalid_workflow_configuration",
		},
		{
			method: http.MethodPut,
			path:   prLifecycleWorkflowConfigurationItemsPath + "/custom",
			body: prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: current.ConfigRevision,
				WorkflowConfiguration:  &validCustomWorkflow,
			},
			code: "invalid_workflow_configuration",
		},
		{
			method: http.MethodDelete,
			path:   prLifecycleWorkflowConfigurationItemsPath + "/custom",
			body: prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: current.ConfigRevision,
			},
			code: "invalid_workflow_configuration",
		},
		{
			method: http.MethodPost,
			path:   prLifecycleWorkflowConfigurationItemsPath + "/custom/default",
			body: prLifecycleCollectionRevisionRequest{
				ExpectedConfigRevision: current.ConfigRevision,
			},
			code: "invalid_workflow_configuration",
		},
	}
	for _, mutation := range failureMutations {
		response := prLifecycleCollectionRequestForTest(
			t, mux, mutation.method, mutation.path, mutation.body, nil,
		)
		if response.Code != http.StatusUnprocessableEntity ||
			!strings.Contains(response.Body.String(), `"code":"`+mutation.code+`"`) {
			t.Fatalf("forced validation = %d %s", response.Code, response.Body.String())
		}
	}
	handler.validatePRLifecycleCollectionCandidate = originalValidator

	handler.savePRLifecycleCollectionCandidate = func(
		*config.Config,
		config.PRLifecycleConfig,
		string,
	) (string, error) {
		return "", config.ErrConfigRevisionMismatch
	}
	for _, mutation := range failureMutations {
		response := prLifecycleCollectionRequestForTest(
			t, mux, mutation.method, mutation.path, mutation.body, nil,
		)
		if response.Code != http.StatusConflict ||
			!strings.Contains(response.Body.String(), `"code":"config_revision_mismatch"`) {
			t.Fatalf("forced save race = %d %s", response.Code, response.Body.String())
		}
	}
	handler.savePRLifecycleCollectionCandidate = nil
}

func TestPRLifecycleCollectionHandlersFailClosedWhenConfigIsUnavailable(t *testing.T) {
	configPath := t.TempDir() + "/config.json"
	if err := os.WriteFile(configPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.registerPRLifecycleWorkflowConfigurationRoutes(mux)
	assignmentID, err := prLifecycleRepositoryAssignmentID("https://github.com|repo")
	if err != nil {
		t.Fatal(err)
	}
	workflowInput := prLifecycleCollectionWorkflowInput(
		"custom", "Custom", config.PRLifecycleDeferredIssuesAsk,
	)
	repositoryInput := prLifecycleRepositoryAssignmentInput{
		ProviderOrigin: "https://github.com", RepositoryID: "repo",
		Repository: "octo/repo", Configuration: "default", DefaultBranch: "main",
	}
	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			name: "list assignments", method: http.MethodGet,
			path: prLifecycleRepositoryAssignmentCollectionPath,
		},
		{
			name: "get assignment", method: http.MethodGet,
			path: prLifecycleRepositoryAssignmentCollectionPath + "/" + assignmentID,
		},
		{
			name: "list workflows", method: http.MethodGet,
			path: prLifecycleWorkflowConfigurationItemsPath,
		},
		{
			name: "get workflow", method: http.MethodGet,
			path: prLifecycleWorkflowConfigurationItemsPath + "/default",
		},
		{
			name: "create assignment", method: http.MethodPost,
			path: prLifecycleRepositoryAssignmentCollectionPath,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: "missing", RepositoryAssignment: &repositoryInput,
			},
		},
		{
			name: "update assignment", method: http.MethodPut,
			path: prLifecycleRepositoryAssignmentCollectionPath + "/" + assignmentID,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: "missing", RepositoryAssignment: &repositoryInput,
			},
		},
		{
			name: "delete assignment", method: http.MethodDelete,
			path: prLifecycleRepositoryAssignmentCollectionPath + "/" + assignmentID,
			body: prLifecycleCollectionRevisionRequest{ExpectedConfigRevision: "missing"},
		},
		{
			name: "bulk delete assignments", method: http.MethodPost,
			path: prLifecycleRepositoryAssignmentCollectionPath + "/bulk-delete",
			body: collectionBulkDeleteRequest{
				IDs: []string{assignmentID}, ExpectedConfigRevision: "missing",
			},
		},
		{
			name: "create workflow", method: http.MethodPost,
			path: prLifecycleWorkflowConfigurationItemsPath,
			body: prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: "missing", WorkflowConfiguration: &workflowInput,
			},
		},
		{
			name: "update workflow", method: http.MethodPut,
			path: prLifecycleWorkflowConfigurationItemsPath + "/custom",
			body: prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: "missing", WorkflowConfiguration: &workflowInput,
			},
		},
		{
			name: "delete workflow", method: http.MethodDelete,
			path: prLifecycleWorkflowConfigurationItemsPath + "/custom",
			body: prLifecycleCollectionRevisionRequest{ExpectedConfigRevision: "missing"},
		},
		{
			name: "default workflow", method: http.MethodPost,
			path: prLifecycleWorkflowConfigurationItemsPath + "/custom/default",
			body: prLifecycleCollectionRevisionRequest{ExpectedConfigRevision: "missing"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := prLifecycleCollectionRequestForTest(
				t, mux, test.method, test.path, test.body, nil,
			)
			if response.Code != http.StatusInternalServerError ||
				!strings.Contains(response.Body.String(), `"code":"config_load_failed"`) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestPRLifecycleCollectionItemRoutesRejectQueriesAndMalformedBodies(t *testing.T) {
	_, _, mux := prLifecycleWorkflowConfigurationTestServer(t)
	assignmentID, err := prLifecycleRepositoryAssignmentID("https://github.com|repo")
	if err != nil {
		t.Fatal(err)
	}
	readPaths := []string{
		prLifecycleRepositoryAssignmentCollectionPath + "/" + assignmentID,
		prLifecycleWorkflowConfigurationItemsPath + "/default",
	}
	for _, path := range readPaths {
		response := prLifecycleCollectionRequestForTest(
			t, mux, http.MethodGet, path+"?unsupported=1", nil, nil,
		)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), `"code":"invalid_collection_request"`) {
			t.Fatalf("read query %q = %d %s", path, response.Code, response.Body.String())
		}
	}

	mutationRoutes := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: prLifecycleRepositoryAssignmentCollectionPath},
		{
			method: http.MethodPut,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/" + assignmentID,
		},
		{
			method: http.MethodDelete,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/" + assignmentID,
		},
		{
			method: http.MethodPost,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/bulk-delete",
		},
		{method: http.MethodPost, path: prLifecycleWorkflowConfigurationItemsPath},
		{
			method: http.MethodPut,
			path:   prLifecycleWorkflowConfigurationItemsPath + "/default",
		},
		{
			method: http.MethodDelete,
			path:   prLifecycleWorkflowConfigurationItemsPath + "/default",
		},
		{
			method: http.MethodPost,
			path:   prLifecycleWorkflowConfigurationItemsPath + "/default/default",
		},
	}
	for _, route := range mutationRoutes {
		queryResponse := prLifecycleCollectionRequestForTest(
			t, mux, route.method, route.path+"?unsupported=1", map[string]any{}, nil,
		)
		if queryResponse.Code != http.StatusBadRequest ||
			!strings.Contains(queryResponse.Body.String(), `"code":"invalid_collection_request"`) {
			t.Fatalf(
				"mutation query %s %s = %d %s",
				route.method,
				route.path,
				queryResponse.Code,
				queryResponse.Body.String(),
			)
		}
		bodyResponse := prLifecycleCollectionRequestForTest(
			t, mux, route.method, route.path, nil,
			func(request *http.Request) { request.Body = nil },
		)
		if bodyResponse.Code != http.StatusBadRequest ||
			!strings.Contains(bodyResponse.Body.String(), `"code":"invalid_collection_request"`) {
			t.Fatalf(
				"mutation body %s %s = %d %s",
				route.method,
				route.path,
				bodyResponse.Code,
				bodyResponse.Body.String(),
			)
		}
	}

	workflowCursor := prLifecycleCollectionRequestForTest(
		t, mux, http.MethodGet,
		prLifecycleWorkflowConfigurationItemsPath+"?cursor=invalid", nil, nil,
	)
	if workflowCursor.Code != http.StatusBadRequest ||
		!strings.Contains(workflowCursor.Body.String(), `"code":"invalid_cursor"`) {
		t.Fatalf("workflow cursor = %d %s", workflowCursor.Code, workflowCursor.Body.String())
	}

	invalidItemMutations := []struct {
		method string
		path   string
		code   string
	}{
		{
			method: http.MethodPut,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/invalid",
			code:   "invalid_repository_assignment_id",
		},
		{
			method: http.MethodDelete,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/invalid",
			code:   "invalid_repository_assignment_id",
		},
		{
			method: http.MethodPut,
			path:   prLifecycleWorkflowConfigurationItemsPath + "/Invalid",
			code:   "invalid_workflow_configuration_id",
		},
		{
			method: http.MethodDelete,
			path:   prLifecycleWorkflowConfigurationItemsPath + "/Invalid",
			code:   "invalid_workflow_configuration_id",
		},
		{
			method: http.MethodPost,
			path:   prLifecycleWorkflowConfigurationItemsPath + "/Invalid/default",
			code:   "invalid_workflow_configuration_id",
		},
	}
	for _, mutation := range invalidItemMutations {
		response := prLifecycleCollectionRequestForTest(
			t, mux, mutation.method, mutation.path, map[string]any{}, nil,
		)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), `"code":"`+mutation.code+`"`) {
			t.Fatalf("invalid item mutation = %d %s", response.Code, response.Body.String())
		}
	}

	missingResourceBodies := []struct {
		method string
		path   string
		body   any
		code   string
	}{
		{
			method: http.MethodPut,
			path:   prLifecycleRepositoryAssignmentCollectionPath + "/" + assignmentID,
			body: prLifecycleRepositoryAssignmentMutationRequest{
				ExpectedConfigRevision: "unused",
			},
			code: "invalid_repository_assignment",
		},
		{
			method: http.MethodPut,
			path:   prLifecycleWorkflowConfigurationItemsPath + "/default",
			body: prLifecycleWorkflowConfigurationMutationRequest{
				ExpectedConfigRevision: "unused",
			},
			code: "invalid_workflow_configuration",
		},
	}
	for _, mutation := range missingResourceBodies {
		response := prLifecycleCollectionRequestForTest(
			t, mux, mutation.method, mutation.path, mutation.body, nil,
		)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), `"code":"`+mutation.code+`"`) {
			t.Fatalf("missing resource body = %d %s", response.Code, response.Body.String())
		}
	}
}

func TestPRLifecycleCollectionPathsAreCoveredByCanonicalGuard(t *testing.T) {
	guarded := GuardPRWorkspaceCanonicalPaths(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	))
	for _, path := range []string{
		"/api/development//repository-assignments",
		"/api/development/repository-assignments/../repository-assignments",
		"/api/development/workflow-configurations//items",
	} {
		request := httptest.NewRequest(http.MethodGet, "http://launcher.local"+path, nil)
		response := httptest.NewRecorder()
		guarded.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("noncanonical %q = %d %s", path, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"http://launcher.local"+prLifecycleRepositoryAssignmentCollectionPath,
		nil,
	)
	response := httptest.NewRecorder()
	guarded.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("canonical path = %d %s", response.Code, response.Body.String())
	}
}

func TestPRLifecycleCollectionValidationCoversConfiguredAgentsAndCatalogErrors(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	if err := validatePRLifecycleGateActionWorkflows(
		context.Background(), cfg.PRLifecycle.Effective(), cfg,
	); err != nil {
		t.Fatalf("configured-agent validation = %v", err)
	}

	candidate := clonePRLifecycleCollectionConfig(cfg.PRLifecycle.Effective())
	candidate.WorkflowConfigurations["custom"] = config.PRLifecycleWorkflowConfiguration{
		Name: "Custom",
		Bindings: []config.PRLifecycleGateBinding{{
			WorkflowRef: config.PRLifecycleWorkflowRef,
			GateRef:     "gates.charter-confirm",
			Action: &gatetypes.GateAction{
				Type:   gatetypes.GateActionDeterministic,
				Fields: map[string]any{"unknown": "value"},
			},
		}},
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{
			Mode: config.PRLifecycleDeferredIssuesAsk,
		},
	}
	if err := validatePRLifecycleWorkflowConfigurations(
		context.Background(), candidate, cfg,
	); err == nil || !strings.Contains(err.Error(), "deterministic fields") {
		t.Fatalf("catalog validation error = %v", err)
	}
}
