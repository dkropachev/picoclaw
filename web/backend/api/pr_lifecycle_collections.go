package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/prworkspace/lifecycleflow"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	prLifecycleRepositoryAssignmentIDNamespace = "repository-assignment"
	prLifecycleReferenceBlockerLimit           = 32
)

type prLifecycleRepositoryAssignmentProjector func(
	config.PRLifecycleConfig,
) ([]prLifecycleRepositoryAssignmentItem, error)

type prLifecycleCollectionCandidateValidator func(
	context.Context,
	config.PRLifecycleConfig,
	*config.Config,
) error

type prLifecycleCollectionCandidateSaver func(
	*config.Config,
	config.PRLifecycleConfig,
	string,
) (string, error)

type prLifecycleCollectionEffects struct {
	GatewayEffect        string `json:"gateway_effect"`
	DeferredPolicyEffect string `json:"deferred_policy_effect"`
}

type prLifecycleRepositoryAssignmentSummary struct {
	ID            string `json:"id"`
	Repository    string `json:"repository"`
	Configuration string `json:"configuration"`
	DefaultBranch string `json:"default_branch"`
}

type prLifecycleRepositoryAssignmentResource struct {
	ID             string `json:"id"`
	Repository     string `json:"repository"`
	Configuration  string `json:"configuration"`
	DefaultBranch  string `json:"default_branch"`
	ProviderOrigin string `json:"provider_origin"`
	RepositoryID   string `json:"repository_id"`
}

type prLifecycleRepositoryAssignmentItem struct {
	Identity          string
	CanonicalIdentity string
	Summary           prLifecycleRepositoryAssignmentSummary
	Resource          prLifecycleRepositoryAssignmentResource
}

type prLifecycleRepositoryAssignmentInput struct {
	ProviderOrigin string `json:"provider_origin"`
	RepositoryID   string `json:"repository_id"`
	Repository     string `json:"repository"`
	Configuration  string `json:"configuration"`
	DefaultBranch  string `json:"default_branch"`
}

type prLifecycleRepositoryAssignmentMutationRequest struct {
	ExpectedConfigRevision string                                `json:"expected_config_revision"`
	RepositoryAssignment   *prLifecycleRepositoryAssignmentInput `json:"repository_assignment"`
}

type prLifecycleCollectionRevisionRequest struct {
	ExpectedConfigRevision string `json:"expected_config_revision"`
}

type prLifecycleWorkflowConfigurationSummaryItem struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	IsDefault      bool   `json:"is_default"`
	Bindings       int    `json:"bindings"`
	DeferredIssues string `json:"deferred_issues"`
}

type prLifecycleCollectionGateAction struct {
	Type        gatetypes.GateActionType `json:"type"`
	AgentID     string                   `json:"agent_id,omitempty"`
	Prompt      string                   `json:"prompt,omitempty"`
	Session     string                   `json:"session,omitempty"`
	History     string                   `json:"history,omitempty"`
	Cache       string                   `json:"cache,omitempty"`
	Tools       string                   `json:"tools,omitempty"`
	Fields      map[string]any           `json:"fields,omitempty"`
	WorkflowRef string                   `json:"workflow_ref,omitempty"`
}

type prLifecycleCollectionGateBinding struct {
	WorkflowRef string                           `json:"workflow_ref"`
	GateRef     string                           `json:"gate_ref"`
	Action      *prLifecycleCollectionGateAction `json:"action,omitempty"`
}

type prLifecycleCollectionScopeDisposition struct {
	Default config.PRLifecycleScopeDispositionRule            `json:"default"`
	ByType  map[string]config.PRLifecycleScopeDispositionRule `json:"by_type"`
}

type prLifecycleWorkflowConfigurationResource struct {
	ID               string                                 `json:"id"`
	Name             string                                 `json:"name"`
	IsDefault        bool                                   `json:"is_default"`
	Bindings         []prLifecycleCollectionGateBinding     `json:"bindings"`
	DeferredIssues   config.PRLifecycleDeferredIssueConfig  `json:"deferred_issues"`
	ScopeDisposition *prLifecycleCollectionScopeDisposition `json:"scope_disposition,omitempty"`
}

type prLifecycleWorkflowConfigurationInput struct {
	ID               string                                `json:"id"`
	Name             string                                `json:"name"`
	Bindings         []prLifecycleCollectionGateBinding    `json:"bindings"`
	DeferredIssues   config.PRLifecycleDeferredIssueConfig `json:"deferred_issues"`
	ScopeDisposition prLifecycleCollectionScopeDisposition `json:"scope_disposition"`
}

type prLifecycleWorkflowConfigurationMutationRequest struct {
	ExpectedConfigRevision string                                 `json:"expected_config_revision"`
	WorkflowConfiguration  *prLifecycleWorkflowConfigurationInput `json:"workflow_configuration"`
}

type prLifecycleWorkflowConfigurationItem struct {
	Configuration config.PRLifecycleWorkflowConfiguration
	Summary       prLifecycleWorkflowConfigurationSummaryItem
}

type prLifecycleWorkflowConfigurationChoice struct {
	Name           string                                `json:"name"`
	DeferredIssues config.PRLifecycleDeferredIssueConfig `json:"deferred_issues"`
}

type prLifecycleCollectionGateField struct {
	ID            string                      `json:"id"`
	Type          gatetypes.GateFieldType     `json:"type"`
	Label         string                      `json:"label"`
	Required      bool                        `json:"required"`
	MinSelections int                         `json:"min_selections,omitempty"`
	MaxSelections int                         `json:"max_selections,omitempty"`
	Options       []gatetypes.GateFieldOption `json:"options,omitempty"`
}

type prLifecycleCollectionGateCatalogEntry struct {
	WorkflowRef       string                           `json:"workflow_ref"`
	GateRef           string                           `json:"gate_ref"`
	WorkflowRevision  string                           `json:"workflow_revision,omitempty"`
	SourceAISupported bool                             `json:"source_ai_supported"`
	Prompt            string                           `json:"prompt"`
	Fields            []prLifecycleCollectionGateField `json:"fields,omitempty"`
	DefaultAction     *prLifecycleCollectionGateAction `json:"default_action,omitempty"`
	EffectiveAction   *prLifecycleCollectionGateAction `json:"effective_action,omitempty"`
	ActionSource      string                           `json:"action_source,omitempty"`
}

var prLifecycleRepositoryAssignmentCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "repository", Type: collectionquery.TypeString, Sortable: true},
		{Name: "configuration", Type: collectionquery.TypeString, Sortable: true},
		{Name: "default_branch", Type: collectionquery.TypeString, Sortable: true},
	},
	[]collectionquery.SortField{{
		Field: "repository", Direction: collectionquery.Ascending,
	}},
)

var prLifecycleWorkflowConfigurationCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "id", Type: collectionquery.TypeString, Sortable: true},
		{Name: "name", Type: collectionquery.TypeString, Sortable: true},
		{
			Name: "is_default", Type: collectionquery.TypeBoolean, Sortable: true,
			SuggestedValues: []string{"true", "false"},
		},
		{Name: "bindings", Type: collectionquery.TypeNumber, Sortable: true},
		{
			Name: "deferred_issues", Type: collectionquery.TypeEnum, Sortable: true,
			SuggestedValues: []string{
				string(config.PRLifecycleDeferredIssuesOff),
				string(config.PRLifecycleDeferredIssuesAsk),
				string(config.PRLifecycleDeferredIssuesAutomatic),
			},
		},
	},
	[]collectionquery.SortField{{Field: "name", Direction: collectionquery.Ascending}},
)

func (h *Handler) registerPRLifecycleCollectionRoutes(mux *http.ServeMux) {
	mux.HandleFunc(
		"GET "+prLifecycleRepositoryAssignmentCollectionPath,
		h.handleListPRLifecycleRepositoryAssignments,
	)
	mux.HandleFunc(
		"POST "+prLifecycleRepositoryAssignmentCollectionPath,
		h.requireCollectionMutationOrigin(h.handleCreatePRLifecycleRepositoryAssignment),
	)
	mux.HandleFunc(
		"POST "+prLifecycleRepositoryAssignmentCollectionPath+"/bulk-delete",
		h.requireCollectionMutationOrigin(h.handleBulkDeletePRLifecycleRepositoryAssignments),
	)
	mux.HandleFunc(
		"GET "+prLifecycleRepositoryAssignmentCollectionPath+"/{id}",
		h.handleGetPRLifecycleRepositoryAssignment,
	)
	mux.HandleFunc(
		"PUT "+prLifecycleRepositoryAssignmentCollectionPath+"/{id}",
		h.requireCollectionMutationOrigin(h.handleUpdatePRLifecycleRepositoryAssignment),
	)
	mux.HandleFunc(
		"DELETE "+prLifecycleRepositoryAssignmentCollectionPath+"/{id}",
		h.requireCollectionMutationOrigin(h.handleDeletePRLifecycleRepositoryAssignment),
	)

	mux.HandleFunc(
		"GET "+prLifecycleWorkflowConfigurationItemsPath,
		h.handleListPRLifecycleWorkflowConfigurationItems,
	)
	mux.HandleFunc(
		"POST "+prLifecycleWorkflowConfigurationItemsPath,
		h.requireCollectionMutationOrigin(h.handleCreatePRLifecycleWorkflowConfigurationItem),
	)
	mux.HandleFunc(
		"POST "+prLifecycleWorkflowConfigurationItemsPath+"/{id}/default",
		h.requireCollectionMutationOrigin(h.handleDefaultPRLifecycleWorkflowConfigurationItem),
	)
	mux.HandleFunc(
		"GET "+prLifecycleWorkflowConfigurationItemsPath+"/{id}",
		h.handleGetPRLifecycleWorkflowConfigurationItem,
	)
	mux.HandleFunc(
		"PUT "+prLifecycleWorkflowConfigurationItemsPath+"/{id}",
		h.requireCollectionMutationOrigin(h.handleUpdatePRLifecycleWorkflowConfigurationItem),
	)
	mux.HandleFunc(
		"DELETE "+prLifecycleWorkflowConfigurationItemsPath+"/{id}",
		h.requireCollectionMutationOrigin(h.handleDeletePRLifecycleWorkflowConfigurationItem),
	)
}

func clonePRLifecycleCollectionConfig(source config.PRLifecycleConfig) config.PRLifecycleConfig {
	clone := source
	clone.Repositories = make(
		map[string]config.PRLifecycleRepositoryDescriptor,
		len(source.Repositories),
	)
	for identity, descriptor := range source.Repositories {
		clone.Repositories[identity] = descriptor
	}
	clone.RepositoryAssignments = make(map[string]string, len(source.RepositoryAssignments))
	for identity, configurationID := range source.RepositoryAssignments {
		clone.RepositoryAssignments[identity] = configurationID
	}
	clone.WorkflowConfigurations = make(
		map[string]config.PRLifecycleWorkflowConfiguration,
		len(source.WorkflowConfigurations),
	)
	for configurationID, workflowConfiguration := range source.WorkflowConfigurations {
		clone.WorkflowConfigurations[configurationID] = clonePRLifecycleWorkflowConfiguration(workflowConfiguration)
	}
	return clone
}

func clonePRLifecycleWorkflowConfiguration(
	source config.PRLifecycleWorkflowConfiguration,
) config.PRLifecycleWorkflowConfiguration {
	clone := source
	clone.Bindings = make([]config.PRLifecycleGateBinding, len(source.Bindings))
	for index, binding := range source.Bindings {
		clone.Bindings[index] = binding
		if binding.Action != nil {
			action := *binding.Action
			action.Fields = clonePRLifecycleJSONMap(binding.Action.Fields)
			clone.Bindings[index].Action = &action
		}
	}
	if source.ScopeDisposition.ByType != nil {
		clone.ScopeDisposition.ByType = make(
			map[string]config.PRLifecycleScopeDispositionRule,
			len(source.ScopeDisposition.ByType),
		)
		for kind, rule := range source.ScopeDisposition.ByType {
			clone.ScopeDisposition.ByType[kind] = rule
		}
	}
	return clone
}

func clonePRLifecycleJSONMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = clonePRLifecycleJSONValue(value)
	}
	return clone
}

func clonePRLifecycleJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return clonePRLifecycleJSONMap(typed)
	case []any:
		clone := make([]any, len(typed))
		for index := range typed {
			clone[index] = clonePRLifecycleJSONValue(typed[index])
		}
		return clone
	case []string:
		return append([]string(nil), typed...)
	case map[string]string:
		clone := make(map[string]string, len(typed))
		for key, entry := range typed {
			clone[key] = entry
		}
		return clone
	default:
		return typed
	}
}

func prLifecycleCollectionEffectsForHandler(h *Handler) prLifecycleCollectionEffects {
	gatewayEffect, deferredPolicyEffect := h.prLifecycleEffects()
	return prLifecycleCollectionEffects{
		GatewayEffect:        strings.ReplaceAll(gatewayEffect, "-", "_"),
		DeferredPolicyEffect: strings.ReplaceAll(deferredPolicyEffect, "-", "_"),
	}
}

func canonicalPRLifecycleRepositoryIdentityParts(
	identity string,
) (string, string, string, bool) {
	parts := strings.Split(identity, "|")
	if len(parts) != 2 {
		return "", "", "", false
	}
	canonical, err := config.CanonicalPRLifecycleRepositoryIdentity(parts[0], parts[1])
	if err != nil {
		return "", "", "", false
	}
	canonicalParts := strings.SplitN(canonical, "|", 2)
	return canonical, canonicalParts[0], canonicalParts[1], true
}

func prLifecycleRepositoryAssignmentID(identity string) (string, error) {
	canonical, _, _, valid := canonicalPRLifecycleRepositoryIdentityParts(identity)
	if !valid {
		return "", errInvalidCollectionResourceID
	}
	return encodeCollectionResourceID(prLifecycleRepositoryAssignmentIDNamespace, canonical)
}

func projectPRLifecycleRepositoryAssignmentItems(
	lifecycle config.PRLifecycleConfig,
) ([]prLifecycleRepositoryAssignmentItem, error) {
	items := make([]prLifecycleRepositoryAssignmentItem, 0, len(lifecycle.RepositoryAssignments))
	seenIDs := make(map[string]struct{}, len(lifecycle.RepositoryAssignments))
	for identity, configurationID := range lifecycle.RepositoryAssignments {
		canonicalIdentity, providerOrigin, repositoryID, valid := canonicalPRLifecycleRepositoryIdentityParts(identity)
		if !valid {
			return nil, errors.New("repository assignment identity is invalid")
		}
		// Namespace and canonical identity were validated immediately above.
		id, _ := encodeCollectionResourceID(
			prLifecycleRepositoryAssignmentIDNamespace,
			canonicalIdentity,
		)
		if _, duplicate := seenIDs[id]; duplicate {
			return nil, errors.New("repository assignment identities collide")
		}
		seenIDs[id] = struct{}{}
		_, descriptor, described := findPRLifecycleRepositoryDescriptor(
			lifecycle, canonicalIdentity,
		)
		repository := repositoryID
		defaultBranch := ""
		if described {
			repository = descriptor.Name
			defaultBranch = descriptor.DefaultBranch
		}
		summary := prLifecycleRepositoryAssignmentSummary{
			ID: id, Repository: repository, Configuration: configurationID,
			DefaultBranch: defaultBranch,
		}
		items = append(items, prLifecycleRepositoryAssignmentItem{
			Identity: identity, CanonicalIdentity: canonicalIdentity,
			Summary: summary,
			Resource: prLifecycleRepositoryAssignmentResource{
				ID: id, Repository: repository, Configuration: configurationID,
				DefaultBranch: defaultBranch, ProviderOrigin: providerOrigin,
				RepositoryID: repositoryID,
			},
		})
	}
	return items, nil
}

func findPRLifecycleRepositoryAssignment(
	items []prLifecycleRepositoryAssignmentItem,
	id string,
) (prLifecycleRepositoryAssignmentItem, bool) {
	if !validCollectionResourceID(id) {
		return prLifecycleRepositoryAssignmentItem{}, false
	}
	for _, item := range items {
		if item.Summary.ID == id {
			return item, true
		}
	}
	return prLifecycleRepositoryAssignmentItem{}, false
}

func findPRLifecycleRepositoryDescriptor(
	lifecycle config.PRLifecycleConfig,
	canonicalIdentity string,
) (string, config.PRLifecycleRepositoryDescriptor, bool) {
	if descriptor, exists := lifecycle.Repositories[canonicalIdentity]; exists {
		return canonicalIdentity, descriptor, true
	}
	identities := make([]string, 0, len(lifecycle.Repositories))
	for identity := range lifecycle.Repositories {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	for _, identity := range identities {
		descriptor := lifecycle.Repositories[identity]
		candidate, _, _, valid := canonicalPRLifecycleRepositoryIdentityParts(identity)
		if valid && candidate == canonicalIdentity {
			return identity, descriptor, true
		}
	}
	return "", config.PRLifecycleRepositoryDescriptor{}, false
}

func prLifecycleRepositoryAssignmentSchema(
	items []prLifecycleRepositoryAssignmentItem,
) collectionquery.Schema {
	repositories := make([]string, 0, len(items))
	configurations := make([]string, 0, len(items))
	branches := make([]string, 0, len(items))
	for _, item := range items {
		repositories = append(repositories, item.Summary.Repository)
		configurations = append(configurations, item.Summary.Configuration)
		branches = append(branches, item.Summary.DefaultBranch)
	}
	sort.Strings(repositories)
	sort.Strings(configurations)
	sort.Strings(branches)
	return collectionSchemaWithSuggestions(
		prLifecycleRepositoryAssignmentCollectionSchema,
		map[collectionquery.Field][]string{
			"repository": repositories, "configuration": configurations,
			"default_branch": branches,
		},
	)
}

func prLifecycleRepositoryAssignmentPageOptions() collectionquery.PageOptions[prLifecycleRepositoryAssignmentItem] {
	return collectionquery.PageOptions[prLifecycleRepositoryAssignmentItem]{
		ID: func(item prLifecycleRepositoryAssignmentItem) (string, error) {
			return item.Summary.ID, nil
		},
		ValidateID: validCollectionResourceID,
		Clone: func(item prLifecycleRepositoryAssignmentItem) prLifecycleRepositoryAssignmentItem {
			return item
		},
		Resolve: func(
			item prLifecycleRepositoryAssignmentItem,
			field collectionquery.Field,
			_ time.Time,
		) (collectionquery.FieldValue, bool) {
			switch field {
			case "repository":
				return collectionquery.StringValue(item.Summary.Repository), true
			case "configuration":
				return collectionquery.StringValue(item.Summary.Configuration), true
			case "default_branch":
				return collectionquery.StringValue(item.Summary.DefaultBranch), true
			default:
				return collectionquery.FieldValue{}, false
			}
		},
	}
}

func projectPRLifecycleWorkflowConfigurationItems(
	lifecycle config.PRLifecycleConfig,
) []prLifecycleWorkflowConfigurationItem {
	items := make([]prLifecycleWorkflowConfigurationItem, 0, len(lifecycle.WorkflowConfigurations))
	for id, workflowConfiguration := range lifecycle.WorkflowConfigurations {
		items = append(
			items,
			prLifecycleWorkflowConfigurationItemForConfig(
				lifecycle.DefaultWorkflowConfigurationID,
				id,
				workflowConfiguration,
			),
		)
	}
	return items
}

func prLifecycleWorkflowConfigurationItemForConfig(
	defaultID string,
	id string,
	workflowConfiguration config.PRLifecycleWorkflowConfiguration,
) prLifecycleWorkflowConfigurationItem {
	clone := clonePRLifecycleWorkflowConfiguration(workflowConfiguration)
	return prLifecycleWorkflowConfigurationItem{
		Configuration: clone,
		Summary: prLifecycleWorkflowConfigurationSummaryItem{
			ID: id, Name: clone.Name, IsDefault: defaultID == id,
			Bindings:       len(clone.Bindings),
			DeferredIssues: string(clone.DeferredIssues.Mode),
		},
	}
}

func findPRLifecycleWorkflowConfigurationItem(
	items []prLifecycleWorkflowConfigurationItem,
	id string,
) (prLifecycleWorkflowConfigurationItem, bool) {
	if !validPRLifecycleWorkflowConfigurationItemID(id) {
		return prLifecycleWorkflowConfigurationItem{}, false
	}
	for _, item := range items {
		if item.Summary.ID == id {
			return item, true
		}
	}
	return prLifecycleWorkflowConfigurationItem{}, false
}

func validPRLifecycleWorkflowConfigurationItemID(id string) bool {
	if id == "" || len(id) > 64 || id != strings.TrimSpace(id) {
		return false
	}
	if id[0] < 'a' || id[0] > 'z' {
		return false
	}
	previousHyphen := false
	for index := 1; index < len(id); index++ {
		character := id[index]
		if character == '-' {
			if previousHyphen || index == len(id)-1 {
				return false
			}
			previousHyphen = true
			continue
		}
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				return false
			}
		}
		previousHyphen = false
	}
	return true
}

func prLifecycleWorkflowConfigurationSchema(
	items []prLifecycleWorkflowConfigurationItem,
) collectionquery.Schema {
	ids := make([]string, 0, len(items))
	names := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.Summary.ID)
		names = append(names, item.Summary.Name)
	}
	sort.Strings(ids)
	sort.Strings(names)
	return collectionSchemaWithSuggestions(
		prLifecycleWorkflowConfigurationCollectionSchema,
		map[collectionquery.Field][]string{"id": ids, "name": names},
	)
}

func prLifecycleWorkflowConfigurationPageOptions() collectionquery.PageOptions[prLifecycleWorkflowConfigurationItem] {
	return collectionquery.PageOptions[prLifecycleWorkflowConfigurationItem]{
		ID: func(item prLifecycleWorkflowConfigurationItem) (string, error) {
			return item.Summary.ID, nil
		},
		ValidateID: validPRLifecycleWorkflowConfigurationItemID,
		Clone: func(item prLifecycleWorkflowConfigurationItem) prLifecycleWorkflowConfigurationItem {
			item.Configuration = clonePRLifecycleWorkflowConfiguration(item.Configuration)
			return item
		},
		Resolve: func(
			item prLifecycleWorkflowConfigurationItem,
			field collectionquery.Field,
			_ time.Time,
		) (collectionquery.FieldValue, bool) {
			switch field {
			case "id":
				return collectionquery.StringValue(item.Summary.ID), true
			case "name":
				return collectionquery.StringValue(item.Summary.Name), true
			case "is_default":
				return collectionquery.BooleanValue(item.Summary.IsDefault), true
			case "bindings":
				return collectionquery.NumberValue(float64(item.Summary.Bindings)), true
			case "deferred_issues":
				return collectionquery.EnumValue(item.Summary.DeferredIssues), true
			default:
				return collectionquery.FieldValue{}, false
			}
		},
	}
}

func prLifecycleWorkflowConfigurationResourceFromItem(
	item prLifecycleWorkflowConfigurationItem,
) prLifecycleWorkflowConfigurationResource {
	workflowConfiguration := clonePRLifecycleWorkflowConfiguration(item.Configuration)
	return prLifecycleWorkflowConfigurationResource{
		ID: item.Summary.ID, Name: workflowConfiguration.Name,
		IsDefault:      item.Summary.IsDefault,
		Bindings:       prLifecycleCollectionGateBindings(workflowConfiguration.Bindings),
		DeferredIssues: workflowConfiguration.DeferredIssues,
		ScopeDisposition: prLifecycleCollectionScopeDispositionFromConfig(
			workflowConfiguration.ScopeDisposition,
		),
	}
}

func prLifecycleCollectionScopeDispositionFromConfig(
	source config.PRLifecycleScopeDispositionConfig,
) *prLifecycleCollectionScopeDisposition {
	if source.Default == (config.PRLifecycleScopeDispositionRule{}) && source.ByType == nil {
		return nil
	}
	byType := clonePRLifecycleScopeDispositionRules(source.ByType)
	if byType == nil {
		byType = make(map[string]config.PRLifecycleScopeDispositionRule)
	}
	return &prLifecycleCollectionScopeDisposition{Default: source.Default, ByType: byType}
}

func prLifecycleWorkflowConfigurationChoices(
	lifecycle config.PRLifecycleConfig,
) map[string]prLifecycleWorkflowConfigurationChoice {
	choices := make(
		map[string]prLifecycleWorkflowConfigurationChoice,
		len(lifecycle.WorkflowConfigurations),
	)
	for id, workflowConfiguration := range lifecycle.WorkflowConfigurations {
		choices[id] = prLifecycleWorkflowConfigurationChoice{
			Name:           workflowConfiguration.Name,
			DeferredIssues: workflowConfiguration.DeferredIssues,
		}
	}
	return choices
}

func clonePRLifecycleScopeDispositionRules(
	source map[string]config.PRLifecycleScopeDispositionRule,
) map[string]config.PRLifecycleScopeDispositionRule {
	if source == nil {
		return nil
	}
	clone := make(map[string]config.PRLifecycleScopeDispositionRule, len(source))
	for kind, rule := range source {
		clone[kind] = rule
	}
	return clone
}

func prLifecycleCollectionGateActionFromConfig(
	action *gatetypes.GateAction,
) *prLifecycleCollectionGateAction {
	if action == nil {
		return nil
	}
	return &prLifecycleCollectionGateAction{
		Type: action.Type, AgentID: action.AgentID, Prompt: action.Prompt,
		Session: action.Session, History: action.History, Cache: action.Cache,
		Tools: action.Tools, Fields: clonePRLifecycleJSONMap(action.Fields),
		WorkflowRef: action.WorkflowRef,
	}
}

func prLifecycleConfigGateActionFromCollection(
	action *prLifecycleCollectionGateAction,
) *gatetypes.GateAction {
	if action == nil {
		return nil
	}
	return &gatetypes.GateAction{
		Type: action.Type, AgentID: action.AgentID, Prompt: action.Prompt,
		Session: action.Session, History: action.History, Cache: action.Cache,
		Tools: action.Tools, Fields: clonePRLifecycleJSONMap(action.Fields),
		WorkflowRef: action.WorkflowRef,
	}
}

func prLifecycleCollectionGateBindings(
	bindings []config.PRLifecycleGateBinding,
) []prLifecycleCollectionGateBinding {
	result := make([]prLifecycleCollectionGateBinding, len(bindings))
	for index, binding := range bindings {
		result[index] = prLifecycleCollectionGateBinding{
			WorkflowRef: binding.WorkflowRef, GateRef: binding.GateRef,
			Action: prLifecycleCollectionGateActionFromConfig(binding.Action),
		}
	}
	return result
}

func prLifecycleConfigGateBindings(
	bindings []prLifecycleCollectionGateBinding,
) []config.PRLifecycleGateBinding {
	if bindings == nil {
		return nil
	}
	result := make([]config.PRLifecycleGateBinding, len(bindings))
	for index, binding := range bindings {
		result[index] = config.PRLifecycleGateBinding{
			WorkflowRef: binding.WorkflowRef, GateRef: binding.GateRef,
			Action: prLifecycleConfigGateActionFromCollection(binding.Action),
		}
	}
	return result
}

func prLifecycleCollectionGateCatalog(
	lifecycle config.PRLifecycleConfig,
) map[string]prLifecycleCollectionGateCatalogEntry {
	source := prLifecycleGateCatalog(lifecycle)
	result := make(map[string]prLifecycleCollectionGateCatalogEntry, len(source))
	for key, entry := range source {
		fields := make([]prLifecycleCollectionGateField, len(entry.Fields))
		for index, field := range entry.Fields {
			fields[index] = prLifecycleCollectionGateField{
				ID: field.ID, Type: field.Type, Label: field.Label,
				Required: field.Required, MinSelections: field.MinSelections,
				MaxSelections: field.MaxSelections,
				Options:       append([]gatetypes.GateFieldOption(nil), field.Options...),
			}
		}
		result[key] = prLifecycleCollectionGateCatalogEntry{
			WorkflowRef: entry.WorkflowRef, GateRef: entry.GateRef,
			WorkflowRevision:  entry.WorkflowRevision,
			SourceAISupported: entry.SourceAISupported,
			Prompt:            entry.Prompt, Fields: fields,
			DefaultAction:   prLifecycleCollectionGateActionFromConfig(entry.DefaultAction),
			EffectiveAction: prLifecycleCollectionGateActionFromConfig(entry.EffectiveAction),
			ActionSource:    entry.ActionSource,
		}
	}
	return result
}

func prLifecycleRepositoryAssignmentDetailEnvelope(
	lifecycle config.PRLifecycleConfig,
	item prLifecycleRepositoryAssignmentItem,
	configRevision string,
	effects prLifecycleCollectionEffects,
) map[string]any {
	return map[string]any{
		"repository_assignment":   item.Resource,
		"workflow_configurations": prLifecycleWorkflowConfigurationChoices(lifecycle),
		"config_revision":         configRevision,
		"effects":                 effects,
	}
}

func prLifecycleWorkflowConfigurationDetailEnvelope(
	lifecycle config.PRLifecycleConfig,
	item prLifecycleWorkflowConfigurationItem,
	configRevision string,
	effects prLifecycleCollectionEffects,
) map[string]any {
	flow, flowRevision := lifecycleflow.Default()
	return map[string]any{
		"workflow_configuration": prLifecycleWorkflowConfigurationResourceFromItem(item),
		"gate_catalog":           prLifecycleCollectionGateCatalog(lifecycle),
		"flow":                   flow,
		"flow_revision":          flowRevision,
		"catalog_revision":       prLifecycleWorkflowConfigurationsCatalogRevision(lifecycle),
		"config_revision":        configRevision,
		"effects":                effects,
	}
}

func prLifecycleCollectionConfigLoadError(w http.ResponseWriter) {
	writeCollectionError(
		w, http.StatusInternalServerError, "config_load_failed",
		"Failed to load configuration", -1, nil,
	)
}

func prLifecycleRepositoryAssignmentIDFromPath(
	w http.ResponseWriter,
	r *http.Request,
) (string, bool) {
	if r == nil || !validCollectionResourceID(r.PathValue("id")) {
		writeCollectionError(
			w, http.StatusBadRequest, "invalid_repository_assignment_id",
			"Repository assignment ID is invalid", -1, nil,
		)
		return "", false
	}
	return r.PathValue("id"), true
}

func prLifecycleWorkflowConfigurationIDFromPath(
	w http.ResponseWriter,
	r *http.Request,
) (string, bool) {
	if r == nil || !validPRLifecycleWorkflowConfigurationItemID(r.PathValue("id")) {
		writeCollectionError(
			w, http.StatusBadRequest, "invalid_workflow_configuration_id",
			"Workflow configuration ID is invalid", -1, nil,
		)
		return "", false
	}
	return r.PathValue("id"), true
}

func (h *Handler) handleListPRLifecycleRepositoryAssignments(
	w http.ResponseWriter,
	r *http.Request,
) {
	request, ok := parseCollectionListRequest(
		w, r, prLifecycleRepositoryAssignmentCollectionSchema,
	)
	if !ok {
		return
	}
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	cfg, revision, err := config.LoadCurrentConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		prLifecycleCollectionConfigLoadError(w)
		return
	}
	lifecycle := clonePRLifecycleCollectionConfig(cfg.PRLifecycle.Effective())
	items, err := h.projectPRLifecycleRepositoryAssignmentItems(lifecycle)
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "repository_assignment_projection_failed",
			"Failed to project repository assignments", -1, nil,
		)
		return
	}
	page, err := collectionquery.Paginate(
		items, request.Query, request.Cursor, request.Limit, request.Now,
		prLifecycleRepositoryAssignmentPageOptions(),
	)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	summaries := make([]prLifecycleRepositoryAssignmentSummary, len(page.Items))
	for index := range page.Items {
		summaries[index] = page.Items[index].Summary
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"repository_assignments": summaries,
		"total":                  page.Total,
		"next_cursor":            page.NextCursor,
		"canonical_query":        request.Query.Canonical(),
		"query_schema":           prLifecycleRepositoryAssignmentSchema(items),
		"config_revision":        revision,
		"effects":                prLifecycleCollectionEffectsForHandler(h),
	})
}

func (h *Handler) handleGetPRLifecycleRepositoryAssignment(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	id, ok := prLifecycleRepositoryAssignmentIDFromPath(w, r)
	if !ok {
		return
	}
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	cfg, revision, err := config.LoadCurrentConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		prLifecycleCollectionConfigLoadError(w)
		return
	}
	lifecycle := clonePRLifecycleCollectionConfig(cfg.PRLifecycle.Effective())
	items, err := h.projectPRLifecycleRepositoryAssignmentItems(lifecycle)
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "repository_assignment_projection_failed",
			"Failed to project repository assignment", -1, nil,
		)
		return
	}
	item, found := findPRLifecycleRepositoryAssignment(items, id)
	if !found {
		writeCollectionError(
			w, http.StatusNotFound, "repository_assignment_not_found",
			"Repository assignment not found", -1, nil,
		)
		return
	}
	writeCollectionJSON(
		w,
		http.StatusOK,
		prLifecycleRepositoryAssignmentDetailEnvelope(
			lifecycle, item, revision, prLifecycleCollectionEffectsForHandler(h),
		),
	)
}

func (h *Handler) handleListPRLifecycleWorkflowConfigurationItems(
	w http.ResponseWriter,
	r *http.Request,
) {
	request, ok := parseCollectionListRequest(
		w, r, prLifecycleWorkflowConfigurationCollectionSchema,
	)
	if !ok {
		return
	}
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	cfg, revision, err := config.LoadCurrentConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		prLifecycleCollectionConfigLoadError(w)
		return
	}
	lifecycle := clonePRLifecycleCollectionConfig(cfg.PRLifecycle.Effective())
	items := projectPRLifecycleWorkflowConfigurationItems(lifecycle)
	page, err := collectionquery.Paginate(
		items, request.Query, request.Cursor, request.Limit, request.Now,
		prLifecycleWorkflowConfigurationPageOptions(),
	)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	summaries := make([]prLifecycleWorkflowConfigurationSummaryItem, len(page.Items))
	for index := range page.Items {
		summaries[index] = page.Items[index].Summary
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"workflow_configurations": summaries,
		"total":                   page.Total,
		"next_cursor":             page.NextCursor,
		"canonical_query":         request.Query.Canonical(),
		"query_schema":            prLifecycleWorkflowConfigurationSchema(items),
		"config_revision":         revision,
		"effects":                 prLifecycleCollectionEffectsForHandler(h),
	})
}

func (h *Handler) handleGetPRLifecycleWorkflowConfigurationItem(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	id, ok := prLifecycleWorkflowConfigurationIDFromPath(w, r)
	if !ok {
		return
	}
	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	cfg, revision, err := config.LoadCurrentConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		prLifecycleCollectionConfigLoadError(w)
		return
	}
	lifecycle := clonePRLifecycleCollectionConfig(cfg.PRLifecycle.Effective())
	item, found := findPRLifecycleWorkflowConfigurationItem(
		projectPRLifecycleWorkflowConfigurationItems(lifecycle), id,
	)
	if !found {
		writeCollectionError(
			w, http.StatusNotFound, "workflow_configuration_not_found",
			"Workflow configuration not found", -1, nil,
		)
		return
	}
	writeCollectionJSON(
		w,
		http.StatusOK,
		prLifecycleWorkflowConfigurationDetailEnvelope(
			lifecycle, item, revision, prLifecycleCollectionEffectsForHandler(h),
		),
	)
}

func decodePRLifecycleCollectionMutation(
	w http.ResponseWriter,
	r *http.Request,
	target any,
) bool {
	return decodeCollectionJSONWithLimit(
		w, r, target, int64(prLifecycleRequestMaxBytes),
	)
}

func prLifecycleRepositoryAssignmentInputIdentity(
	input prLifecycleRepositoryAssignmentInput,
) (string, bool) {
	identity, err := config.CanonicalPRLifecycleRepositoryIdentity(
		input.ProviderOrigin, input.RepositoryID,
	)
	return identity, err == nil
}

func validPRLifecycleRepositoryDescriptorInput(
	identity string,
	input prLifecycleRepositoryAssignmentInput,
) bool {
	test := config.DefaultPRLifecycleConfig()
	test.Repositories[identity] = config.PRLifecycleRepositoryDescriptor{
		Name: input.Repository, DefaultBranch: input.DefaultBranch,
	}
	return test.Validate() == nil
}

func validatePRLifecycleRepositoryAssignmentInput(
	w http.ResponseWriter,
	input *prLifecycleRepositoryAssignmentInput,
) (string, bool) {
	if input == nil {
		writeCollectionError(
			w, http.StatusBadRequest, "invalid_repository_assignment",
			"A repository_assignment object is required", -1, nil,
		)
		return "", false
	}
	identity, valid := prLifecycleRepositoryAssignmentInputIdentity(*input)
	if !valid || !validPRLifecycleWorkflowConfigurationItemID(input.Configuration) {
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "invalid_repository_assignment",
			"Repository assignment identity or configuration is invalid", -1, nil,
		)
		return "", false
	}
	return identity, true
}

func validatePRLifecycleCollectionCandidate(
	h *Handler,
	r *http.Request,
	w http.ResponseWriter,
	candidate config.PRLifecycleConfig,
	cfg *config.Config,
	errorCode string,
) bool {
	if err := h.validatePRLifecycleCollectionCandidate(r.Context(), candidate, cfg); err != nil {
		writeCollectionError(
			w, http.StatusUnprocessableEntity, errorCode, err.Error(), -1, nil,
		)
		return false
	}
	return true
}

func savePRLifecycleCollectionCandidate(
	h *Handler,
	w http.ResponseWriter,
	cfg *config.Config,
	candidate config.PRLifecycleConfig,
	revision string,
) (string, bool) {
	save := h.savePRLifecycleCollectionCandidate
	if save == nil {
		save = h.savePRLifecycleConfig
	}
	nextRevision, err := save(cfg, candidate, revision)
	if err != nil {
		writeCollectionConfigSaveError(w, err)
		return "", false
	}
	return nextRevision, true
}

func loadPRLifecycleCollectionMutation(
	h *Handler,
	w http.ResponseWriter,
	r *http.Request,
	bodyRevision string,
) (*config.Config, config.PRLifecycleConfig, string, bool) {
	cfg, revision, err := config.LoadCurrentConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		prLifecycleCollectionConfigLoadError(w)
		return nil, config.PRLifecycleConfig{}, "", false
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, bodyRevision)
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return nil, config.PRLifecycleConfig{}, "", false
	}
	return cfg, clonePRLifecycleCollectionConfig(cfg.PRLifecycle.Effective()), revision, true
}

func (h *Handler) handleCreatePRLifecycleRepositoryAssignment(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request prLifecycleRepositoryAssignmentMutationRequest
	if !decodePRLifecycleCollectionMutation(w, r, &request) {
		return
	}
	identity, ok := validatePRLifecycleRepositoryAssignmentInput(
		w, request.RepositoryAssignment,
	)
	if !ok {
		return
	}

	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, candidate, revision, ok := loadPRLifecycleCollectionMutation(
		h, w, r, request.ExpectedConfigRevision,
	)
	if !ok {
		return
	}
	id, idErr := prLifecycleRepositoryAssignmentID(identity)
	existingItems, projectionErr := h.projectPRLifecycleRepositoryAssignmentItems(candidate)
	if idErr != nil || projectionErr != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "repository_assignment_projection_failed",
			"Failed to project repository assignments", -1, nil,
		)
		return
	}
	if _, exists := findPRLifecycleRepositoryAssignment(existingItems, id); exists {
		writeCollectionError(
			w, http.StatusConflict, "repository_assignment_exists",
			"A repository assignment already exists for this identity", -1, nil,
		)
		return
	}
	input := *request.RepositoryAssignment
	storageIdentity := identity
	if descriptorIdentity, descriptor, exists := findPRLifecycleRepositoryDescriptor(
		candidate, identity,
	); exists {
		storageIdentity = descriptorIdentity
		if input.Repository != "" && input.Repository != descriptor.Name ||
			input.DefaultBranch != "" && input.DefaultBranch != descriptor.DefaultBranch {
			writeCollectionError(
				w, http.StatusConflict, "repository_descriptor_mismatch",
				"Repository descriptor does not match the configured identity", -1, nil,
			)
			return
		}
	} else {
		if !validPRLifecycleRepositoryDescriptorInput(identity, input) {
			writeCollectionError(
				w, http.StatusUnprocessableEntity, "invalid_repository_descriptor",
				"Repository name and default branch are invalid", -1, nil,
			)
			return
		}
		candidate.Repositories[identity] = config.PRLifecycleRepositoryDescriptor{
			Name: input.Repository, DefaultBranch: input.DefaultBranch,
		}
	}
	if _, exists := candidate.WorkflowConfigurations[input.Configuration]; !exists {
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "workflow_configuration_not_found",
			"Selected workflow configuration does not exist", -1, nil,
		)
		return
	}
	candidate.RepositoryAssignments[storageIdentity] = input.Configuration
	if !validatePRLifecycleCollectionCandidate(
		h, r, w, candidate, cfg, "invalid_repository_assignment",
	) {
		return
	}
	items, projectionErr := h.projectPRLifecycleRepositoryAssignmentItems(candidate)
	if projectionErr != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "repository_assignment_projection_failed",
			"Failed to project repository assignment", -1, nil,
		)
		return
	}
	item, found := findPRLifecycleRepositoryAssignment(items, id)
	if !found {
		writeCollectionError(
			w, http.StatusInternalServerError, "repository_assignment_projection_failed",
			"Failed to project repository assignment", -1, nil,
		)
		return
	}
	nextRevision, ok := savePRLifecycleCollectionCandidate(
		h, w, cfg, candidate, revision,
	)
	if !ok {
		return
	}
	w.Header().Set(
		"Location", prLifecycleRepositoryAssignmentCollectionPath+"/"+url.PathEscape(id),
	)
	effects := prLifecycleCollectionEffectsForHandler(h)
	releaseConfigMutation()
	writeCollectionJSON(
		w,
		http.StatusCreated,
		prLifecycleRepositoryAssignmentDetailEnvelope(
			candidate, item, nextRevision, effects,
		),
	)
}

func (h *Handler) handleUpdatePRLifecycleRepositoryAssignment(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	id, ok := prLifecycleRepositoryAssignmentIDFromPath(w, r)
	if !ok {
		return
	}
	var request prLifecycleRepositoryAssignmentMutationRequest
	if !decodePRLifecycleCollectionMutation(w, r, &request) {
		return
	}
	inputIdentity, ok := validatePRLifecycleRepositoryAssignmentInput(
		w, request.RepositoryAssignment,
	)
	if !ok {
		return
	}

	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, candidate, revision, ok := loadPRLifecycleCollectionMutation(
		h, w, r, request.ExpectedConfigRevision,
	)
	if !ok {
		return
	}
	items, projectionErr := h.projectPRLifecycleRepositoryAssignmentItems(candidate)
	if projectionErr != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "repository_assignment_projection_failed",
			"Failed to project repository assignment", -1, nil,
		)
		return
	}
	existing, found := findPRLifecycleRepositoryAssignment(items, id)
	if !found {
		writeCollectionError(
			w, http.StatusNotFound, "repository_assignment_not_found",
			"Repository assignment not found", -1, nil,
		)
		return
	}
	input := *request.RepositoryAssignment
	if inputIdentity != existing.CanonicalIdentity ||
		input.Repository != "" && input.Repository != existing.Resource.Repository ||
		input.DefaultBranch != "" && input.DefaultBranch != existing.Resource.DefaultBranch {
		writeCollectionError(
			w, http.StatusConflict, "repository_assignment_identity_immutable",
			"Repository assignment identity and descriptor are immutable", -1, nil,
		)
		return
	}
	if _, exists := candidate.WorkflowConfigurations[input.Configuration]; !exists {
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "workflow_configuration_not_found",
			"Selected workflow configuration does not exist", -1, nil,
		)
		return
	}
	candidate.RepositoryAssignments[existing.Identity] = input.Configuration
	if !validatePRLifecycleCollectionCandidate(
		h, r, w, candidate, cfg, "invalid_repository_assignment",
	) {
		return
	}
	items, projectionErr = h.projectPRLifecycleRepositoryAssignmentItems(candidate)
	if projectionErr != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "repository_assignment_projection_failed",
			"Failed to project repository assignment", -1, nil,
		)
		return
	}
	updated, found := findPRLifecycleRepositoryAssignment(items, id)
	if !found {
		writeCollectionError(
			w, http.StatusInternalServerError, "repository_assignment_projection_failed",
			"Failed to project repository assignment", -1, nil,
		)
		return
	}
	nextRevision, ok := savePRLifecycleCollectionCandidate(
		h, w, cfg, candidate, revision,
	)
	if !ok {
		return
	}
	effects := prLifecycleCollectionEffectsForHandler(h)
	releaseConfigMutation()
	writeCollectionJSON(
		w,
		http.StatusOK,
		prLifecycleRepositoryAssignmentDetailEnvelope(
			candidate, updated, nextRevision, effects,
		),
	)
}

func (h *Handler) handleDeletePRLifecycleRepositoryAssignment(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	id, ok := prLifecycleRepositoryAssignmentIDFromPath(w, r)
	if !ok {
		return
	}
	var request prLifecycleCollectionRevisionRequest
	if !decodePRLifecycleCollectionMutation(w, r, &request) {
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, candidate, revision, ok := loadPRLifecycleCollectionMutation(
		h, w, r, request.ExpectedConfigRevision,
	)
	if !ok {
		return
	}
	items, projectionErr := h.projectPRLifecycleRepositoryAssignmentItems(candidate)
	if projectionErr != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "repository_assignment_projection_failed",
			"Failed to project repository assignment", -1, nil,
		)
		return
	}
	item, found := findPRLifecycleRepositoryAssignment(items, id)
	if !found {
		writeCollectionError(
			w, http.StatusNotFound, "repository_assignment_not_found",
			"Repository assignment not found", -1, nil,
		)
		return
	}
	delete(candidate.RepositoryAssignments, item.Identity)
	if !validatePRLifecycleCollectionCandidate(
		h, r, w, candidate, cfg, "invalid_repository_assignment",
	) {
		return
	}
	nextRevision, ok := savePRLifecycleCollectionCandidate(
		h, w, cfg, candidate, revision,
	)
	if !ok {
		return
	}
	effects := prLifecycleCollectionEffectsForHandler(h)
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"deleted_ids":     []string{id},
		"failures":        []collectionBulkFailure{},
		"config_revision": nextRevision,
		"effects":         effects,
	})
}

func (h *Handler) handleBulkDeletePRLifecycleRepositoryAssignments(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request collectionBulkDeleteRequest
	if !decodePRLifecycleCollectionMutation(w, r, &request) {
		return
	}
	if len(request.IDs) == 0 || len(request.IDs) > collectionquery.MaxPageSize {
		writeCollectionError(
			w, http.StatusBadRequest, "invalid_bulk_delete",
			"Bulk deletion requires between 1 and 200 IDs", -1, nil,
		)
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	bodyRevision, ok := bulkCollectionRevision(w, request)
	if !ok {
		return
	}
	cfg, candidate, revision, ok := loadPRLifecycleCollectionMutation(
		h, w, r, bodyRevision,
	)
	if !ok {
		return
	}
	items, projectionErr := h.projectPRLifecycleRepositoryAssignmentItems(candidate)
	if projectionErr != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "repository_assignment_projection_failed",
			"Failed to project repository assignments", -1, nil,
		)
		return
	}
	requested, failures := normalizeBulkIDs(request.IDs)
	deleting := make(map[string]string, len(requested))
	for _, id := range requested {
		if !validCollectionResourceID(id) {
			failures = append(failures, collectionBulkFailure{ID: id, Code: "invalid_id"})
			continue
		}
		item, found := findPRLifecycleRepositoryAssignment(items, id)
		if !found {
			failures = append(failures, collectionBulkFailure{ID: id, Code: "not_found"})
			continue
		}
		deleting[item.Identity] = id
	}
	deletedIDs := make([]string, 0, len(deleting))
	for identity, id := range deleting {
		delete(candidate.RepositoryAssignments, identity)
		deletedIDs = append(deletedIDs, id)
	}
	nextRevision := revision
	if len(deletedIDs) > 0 {
		if !validatePRLifecycleCollectionCandidate(
			h, r, w, candidate, cfg, "invalid_repository_assignment",
		) {
			return
		}
		nextRevision, ok = savePRLifecycleCollectionCandidate(
			h, w, cfg, candidate, revision,
		)
		if !ok {
			return
		}
	}
	sort.Strings(deletedIDs)
	sortCollectionFailures(failures)
	effects := prLifecycleCollectionEffectsForHandler(h)
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"deleted_ids":     deletedIDs,
		"failures":        failures,
		"config_revision": nextRevision,
		"effects":         effects,
	})
}

func validatePRLifecycleWorkflowConfigurationInput(
	w http.ResponseWriter,
	input *prLifecycleWorkflowConfigurationInput,
) (string, config.PRLifecycleWorkflowConfiguration, bool) {
	if input == nil {
		writeCollectionError(
			w, http.StatusBadRequest, "invalid_workflow_configuration",
			"A workflow_configuration object is required", -1, nil,
		)
		return "", config.PRLifecycleWorkflowConfiguration{}, false
	}
	if !validPRLifecycleWorkflowConfigurationItemID(input.ID) {
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "invalid_workflow_configuration",
			"Workflow configuration ID is invalid", -1, nil,
		)
		return "", config.PRLifecycleWorkflowConfiguration{}, false
	}
	workflowConfiguration := clonePRLifecycleWorkflowConfiguration(
		config.PRLifecycleWorkflowConfiguration{
			Name: input.Name, Bindings: prLifecycleConfigGateBindings(input.Bindings),
			DeferredIssues: input.DeferredIssues,
			ScopeDisposition: config.PRLifecycleScopeDispositionConfig{
				Default: input.ScopeDisposition.Default,
				ByType:  clonePRLifecycleScopeDispositionRules(input.ScopeDisposition.ByType),
			},
		},
	)
	return input.ID, workflowConfiguration, true
}

func (h *Handler) handleCreatePRLifecycleWorkflowConfigurationItem(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request prLifecycleWorkflowConfigurationMutationRequest
	if !decodePRLifecycleCollectionMutation(w, r, &request) {
		return
	}
	id, workflowConfiguration, ok := validatePRLifecycleWorkflowConfigurationInput(
		w, request.WorkflowConfiguration,
	)
	if !ok {
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, candidate, revision, ok := loadPRLifecycleCollectionMutation(
		h, w, r, request.ExpectedConfigRevision,
	)
	if !ok {
		return
	}
	if _, exists := candidate.WorkflowConfigurations[id]; exists {
		writeCollectionError(
			w, http.StatusConflict, "workflow_configuration_exists",
			"A workflow configuration with this ID already exists", -1, nil,
		)
		return
	}
	candidate.WorkflowConfigurations[id] = workflowConfiguration
	if !validatePRLifecycleCollectionCandidate(
		h, r, w, candidate, cfg, "invalid_workflow_configuration",
	) {
		return
	}
	nextRevision, ok := savePRLifecycleCollectionCandidate(
		h, w, cfg, candidate, revision,
	)
	if !ok {
		return
	}
	item := prLifecycleWorkflowConfigurationItemForConfig(
		candidate.DefaultWorkflowConfigurationID,
		id,
		candidate.WorkflowConfigurations[id],
	)
	w.Header().Set(
		"Location", prLifecycleWorkflowConfigurationItemsPath+"/"+url.PathEscape(id),
	)
	effects := prLifecycleCollectionEffectsForHandler(h)
	releaseConfigMutation()
	writeCollectionJSON(
		w,
		http.StatusCreated,
		prLifecycleWorkflowConfigurationDetailEnvelope(
			candidate, item, nextRevision, effects,
		),
	)
}

func (h *Handler) handleUpdatePRLifecycleWorkflowConfigurationItem(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	pathID, ok := prLifecycleWorkflowConfigurationIDFromPath(w, r)
	if !ok {
		return
	}
	var request prLifecycleWorkflowConfigurationMutationRequest
	if !decodePRLifecycleCollectionMutation(w, r, &request) {
		return
	}
	id, workflowConfiguration, ok := validatePRLifecycleWorkflowConfigurationInput(
		w, request.WorkflowConfiguration,
	)
	if !ok {
		return
	}
	if id != pathID {
		writeCollectionError(
			w, http.StatusConflict, "workflow_configuration_id_immutable",
			"Workflow configuration ID cannot be changed", -1, nil,
		)
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, candidate, revision, ok := loadPRLifecycleCollectionMutation(
		h, w, r, request.ExpectedConfigRevision,
	)
	if !ok {
		return
	}
	if _, exists := candidate.WorkflowConfigurations[id]; !exists {
		writeCollectionError(
			w, http.StatusNotFound, "workflow_configuration_not_found",
			"Workflow configuration not found", -1, nil,
		)
		return
	}
	candidate.WorkflowConfigurations[id] = workflowConfiguration
	if !validatePRLifecycleCollectionCandidate(
		h, r, w, candidate, cfg, "invalid_workflow_configuration",
	) {
		return
	}
	nextRevision, ok := savePRLifecycleCollectionCandidate(
		h, w, cfg, candidate, revision,
	)
	if !ok {
		return
	}
	item := prLifecycleWorkflowConfigurationItemForConfig(
		candidate.DefaultWorkflowConfigurationID,
		id,
		candidate.WorkflowConfigurations[id],
	)
	effects := prLifecycleCollectionEffectsForHandler(h)
	releaseConfigMutation()
	writeCollectionJSON(
		w,
		http.StatusOK,
		prLifecycleWorkflowConfigurationDetailEnvelope(
			candidate, item, nextRevision, effects,
		),
	)
}

func prLifecycleWorkflowConfigurationReferenceBlockers(
	lifecycle config.PRLifecycleConfig,
	configurationID string,
) []string {
	blockers := make([]string, 0)
	for identity, assignedID := range lifecycle.RepositoryAssignments {
		if assignedID != configurationID {
			continue
		}
		label := ""
		canonicalIdentity, _, _, validIdentity := canonicalPRLifecycleRepositoryIdentityParts(identity)
		if _, descriptor, exists := findPRLifecycleRepositoryDescriptor(
			lifecycle, canonicalIdentity,
		); exists && validIdentity {
			label = descriptor.Name
		}
		if label == "" {
			_, _, repositoryID, valid := canonicalPRLifecycleRepositoryIdentityParts(identity)
			if valid {
				label = repositoryID
			}
		}
		if label == "" {
			label = "repository"
		}
		blockers = append(blockers, boundedCollectionMessage(label, 256))
	}
	sort.Strings(blockers)
	if len(blockers) > prLifecycleReferenceBlockerLimit {
		blockers = blockers[:prLifecycleReferenceBlockerLimit]
	}
	return blockers
}

func prLifecycleWorkflowConfigurationDeleteBlockers(
	lifecycle config.PRLifecycleConfig,
	id string,
) (string, []string) {
	if id == config.DefaultPRLifecycleWorkflowConfigurationID ||
		id == lifecycle.DefaultWorkflowConfigurationID {
		return "default", nil
	}
	blockers := prLifecycleWorkflowConfigurationReferenceBlockers(lifecycle, id)
	if len(blockers) > 0 {
		return "referenced", blockers
	}
	return "", nil
}

func (h *Handler) handleDeletePRLifecycleWorkflowConfigurationItem(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	id, ok := prLifecycleWorkflowConfigurationIDFromPath(w, r)
	if !ok {
		return
	}
	var request prLifecycleCollectionRevisionRequest
	if !decodePRLifecycleCollectionMutation(w, r, &request) {
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, candidate, revision, ok := loadPRLifecycleCollectionMutation(
		h, w, r, request.ExpectedConfigRevision,
	)
	if !ok {
		return
	}
	if _, exists := candidate.WorkflowConfigurations[id]; !exists {
		writeCollectionError(
			w, http.StatusNotFound, "workflow_configuration_not_found",
			"Workflow configuration not found", -1, nil,
		)
		return
	}
	if code, blockers := prLifecycleWorkflowConfigurationDeleteBlockers(candidate, id); code != "" {
		writeCollectionError(
			w, http.StatusConflict, code,
			"Workflow configuration cannot be deleted while it is "+code,
			-1, blockers,
		)
		return
	}
	delete(candidate.WorkflowConfigurations, id)
	if !validatePRLifecycleCollectionCandidate(
		h, r, w, candidate, cfg, "invalid_workflow_configuration",
	) {
		return
	}
	nextRevision, ok := savePRLifecycleCollectionCandidate(
		h, w, cfg, candidate, revision,
	)
	if !ok {
		return
	}
	effects := prLifecycleCollectionEffectsForHandler(h)
	releaseConfigMutation()
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"deleted_ids":     []string{id},
		"failures":        []collectionBulkFailure{},
		"config_revision": nextRevision,
		"effects":         effects,
	})
}

func (h *Handler) handleDefaultPRLifecycleWorkflowConfigurationItem(
	w http.ResponseWriter,
	r *http.Request,
) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	id, ok := prLifecycleWorkflowConfigurationIDFromPath(w, r)
	if !ok {
		return
	}
	var request prLifecycleCollectionRevisionRequest
	if !decodePRLifecycleCollectionMutation(w, r, &request) {
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, candidate, revision, ok := loadPRLifecycleCollectionMutation(
		h, w, r, request.ExpectedConfigRevision,
	)
	if !ok {
		return
	}
	if _, exists := candidate.WorkflowConfigurations[id]; !exists {
		writeCollectionError(
			w, http.StatusNotFound, "workflow_configuration_not_found",
			"Workflow configuration not found", -1, nil,
		)
		return
	}
	candidate.DefaultWorkflowConfigurationID = id
	if !validatePRLifecycleCollectionCandidate(
		h, r, w, candidate, cfg, "invalid_workflow_configuration",
	) {
		return
	}
	nextRevision, ok := savePRLifecycleCollectionCandidate(
		h, w, cfg, candidate, revision,
	)
	if !ok {
		return
	}
	item := prLifecycleWorkflowConfigurationItemForConfig(
		candidate.DefaultWorkflowConfigurationID,
		id,
		candidate.WorkflowConfigurations[id],
	)
	effects := prLifecycleCollectionEffectsForHandler(h)
	releaseConfigMutation()
	writeCollectionJSON(
		w,
		http.StatusOK,
		prLifecycleWorkflowConfigurationDetailEnvelope(
			candidate, item, nextRevision, effects,
		),
	)
}
