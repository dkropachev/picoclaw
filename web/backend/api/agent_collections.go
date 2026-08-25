package api

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
)

type agentBulkDeleteRequest struct {
	IDs                    []string `json:"ids"`
	ConfigRevision         string   `json:"config_revision,omitempty"`
	ExpectedConfigRevision string   `json:"expected_config_revision,omitempty"`
}

type agentBulkDeleteFailure struct {
	ID       string               `json:"id"`
	Code     string               `json:"code"`
	Blockers []agentDeleteBlocker `json:"blockers,omitempty"`
}

type agentBulkDeleteResponse struct {
	DeletedIDs     []string                 `json:"deleted_ids"`
	Failures       []agentBulkDeleteFailure `json:"failures"`
	ConfigRevision string                   `json:"config_revision"`
	Effects        agentEffects             `json:"effects"`
}

var agentCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "id", Type: collectionquery.TypeString, Sortable: true},
		{Name: "name", Type: collectionquery.TypeString, Sortable: true},
		{Name: "workspace", Type: collectionquery.TypeString, Sortable: true},
		{Name: "account", Type: collectionquery.TypeString, Sortable: true},
		{Name: "model", Type: collectionquery.TypeString, Sortable: true},
		{
			Name:            "default",
			Type:            collectionquery.TypeBoolean,
			Sortable:        true,
			SuggestedValues: []string{"true", "false"},
		},
		{
			Name:            "implicit",
			Type:            collectionquery.TypeBoolean,
			Sortable:        true,
			SuggestedValues: []string{"true", "false"},
		},
		{Name: "position", Type: collectionquery.TypeNumber, Sortable: true},
	},
	[]collectionquery.SortField{{Field: "position", Direction: collectionquery.Ascending}},
)

func pageAgentResources(
	resources []agentResource,
	request collectionListRequest,
) (collectionquery.PageResult[agentResource], error) {
	positions := make(map[string]int, len(resources))
	for index := range resources {
		positions[resources[index].ID] = index
	}
	return collectionquery.Paginate(
		resources, request.Query, request.Cursor, request.Limit, request.Now,
		collectionquery.PageOptions[agentResource]{
			ID:         func(agent agentResource) (string, error) { return agent.ID, nil },
			ValidateID: routing.IsCanonicalAgentID,
			Clone: func(agent agentResource) agentResource {
				if agent.Model != nil {
					model := *agent.Model
					if agent.Model.Fallbacks != nil {
						fallbacks := append([]string{}, (*agent.Model.Fallbacks)...)
						model.Fallbacks = &fallbacks
					}
					agent.Model = &model
				}
				agent.Skills = append([]string(nil), agent.Skills...)
				if agent.Subagents != nil {
					subagents := *agent.Subagents
					subagents.AllowAgents = append([]string(nil), agent.Subagents.AllowAgents...)
					agent.Subagents = &subagents
				}
				return agent
			},
			Resolve: func(agent agentResource, field collectionquery.Field, _ time.Time) (collectionquery.FieldValue, bool) {
				switch field {
				case "id":
					return collectionquery.StringValue(agent.ID), true
				case "name":
					return collectionquery.StringValue(agent.Name), true
				case "workspace":
					return collectionquery.StringValue(agent.Workspace), true
				case "account":
					return collectionquery.StringValue(agent.AccountRef), true
				case "model":
					model := ""
					if agent.Model != nil {
						model = agent.Model.Primary
					}
					return collectionquery.StringValue(model), true
				case "default":
					return collectionquery.BooleanValue(agent.IsDefault), true
				case "implicit":
					return collectionquery.BooleanValue(agent.Implicit), true
				case "position":
					return collectionquery.NumberValue(float64(positions[agent.ID])), true
				default:
					return collectionquery.FieldValue{}, false
				}
			},
		},
	)
}

func (h *Handler) handleBulkDeleteAgents(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	var request agentBulkDeleteRequest
	if !decodeAgentRequest(w, r, &request) {
		return
	}
	if len(request.IDs) == 0 || len(request.IDs) > 200 {
		writeAgentError(w, http.StatusBadRequest, "invalid_bulk_delete", nil)
		return
	}
	configRevision := strings.TrimSpace(request.ConfigRevision)
	expectedRevision := strings.TrimSpace(request.ExpectedConfigRevision)
	if configRevision != "" && expectedRevision != "" && configRevision != expectedRevision {
		writeAgentError(w, http.StatusBadRequest, "conflicting_config_revision", nil)
		return
	}
	if configRevision != "" {
		expectedRevision = configRevision
	}
	if expectedRevision == "" {
		writeAgentError(w, http.StatusBadRequest, "expected_config_revision_required", nil)
		return
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	cfg, currentRevision, ok := h.loadAgentConfigForUpdate(w)
	if !ok {
		return
	}
	if expectedRevision != currentRevision {
		writeAgentError(w, http.StatusConflict, "config_revision_mismatch", nil)
		return
	}

	requested, commonFailures := normalizeBulkIDs(request.IDs)
	failures := make([]agentBulkDeleteFailure, 0, len(commonFailures))
	for _, failure := range commonFailures {
		failures = append(failures, agentBulkDeleteFailure{ID: failure.ID, Code: failure.Code})
	}
	deletable := make(map[string]bool, len(requested))
	for _, id := range requested {
		if !routing.IsCanonicalAgentID(id) {
			failures = append(failures, agentBulkDeleteFailure{ID: id, Code: "invalid_agent_id"})
			continue
		}
		if _, exists := findConfiguredAgent(cfg, id); !exists {
			code := "agent_not_found"
			if len(cfg.Agents.List) == 0 && id == routing.DefaultAgentID {
				code = "implicit_agent_required"
			}
			failures = append(failures, agentBulkDeleteFailure{ID: id, Code: code})
			continue
		}
		deletable[id] = true
	}

	// Resolve blockers to a fixed point. A selected referrer does not block its
	// target only while the referrer itself remains eligible for this same save.
	for changed := true; changed; {
		changed = false
		ids := sortedAgentIDSet(deletable)
		for _, id := range ids {
			blockers := agentBulkDeleteBlockers(cfg, id, deletable)
			if len(blockers) == 0 {
				continue
			}
			delete(deletable, id)
			failures = append(failures, agentBulkDeleteFailure{ID: id, Code: "agent_referenced", Blockers: blockers})
			changed = true
		}
	}

	deleted := sortedAgentIDSet(deletable)
	if len(deleted) == 0 {
		sortAgentBulkFailures(failures)
		writeAgentJSON(w, http.StatusOK, agentBulkDeleteResponse{
			DeletedIDs: []string{}, Failures: failures, ConfigRevision: currentRevision,
			Effects: agentEffectsForConfig(cfg),
		})
		return
	}
	kept := make([]config.AgentConfig, 0, len(cfg.Agents.List)-len(deleted))
	for _, agent := range cfg.Agents.List {
		if !deletable[agent.ID] {
			kept = append(kept, agent)
		}
	}
	cfg.Agents.List = kept
	if len(cfg.Agents.List) > 0 {
		defaultFound := false
		for index := range cfg.Agents.List {
			if cfg.Agents.List[index].Default && !defaultFound {
				defaultFound = true
				continue
			}
			cfg.Agents.List[index].Default = false
		}
		if !defaultFound {
			cfg.Agents.List[0].Default = true
		}
	}
	if err := validateAgentConfiguration(cfg); err != nil {
		writeAgentValidationError(w, http.StatusUnprocessableEntity, "invalid_agent", err)
		return
	}
	revision, ok := h.saveAgentConfig(w, cfg, currentRevision)
	if !ok {
		return
	}
	sortAgentBulkFailures(failures)
	writeAgentJSON(w, http.StatusOK, agentBulkDeleteResponse{
		DeletedIDs: deleted, Failures: failures, ConfigRevision: revision,
		Effects: agentEffectsForConfig(cfg),
	})
}

func agentBulkDeleteBlockers(
	cfg *config.Config,
	targetID string,
	deletable map[string]bool,
) []agentDeleteBlocker {
	blockers := make([]agentDeleteBlocker, 0)
	if cfg.Agents.Dispatch != nil {
		for _, rule := range cfg.Agents.Dispatch.Rules {
			if rule.Agent == targetID {
				blockers = append(blockers, agentDeleteBlocker{Kind: "dispatch_rule", Name: rule.Name})
			}
		}
	}
	for index := range cfg.Agents.List {
		agent := &cfg.Agents.List[index]
		if agent.ID == targetID || deletable[agent.ID] || agent.Subagents == nil {
			continue
		}
		for _, allowed := range agent.Subagents.AllowAgents {
			if allowed == targetID {
				blockers = append(blockers, agentDeleteBlocker{Kind: "subagent_allowlist", AgentID: agent.ID})
				break
			}
		}
	}
	sort.SliceStable(blockers, func(i, j int) bool {
		if blockers[i].Kind == blockers[j].Kind {
			if blockers[i].AgentID == blockers[j].AgentID {
				return blockers[i].Name < blockers[j].Name
			}
			return blockers[i].AgentID < blockers[j].AgentID
		}
		return blockers[i].Kind < blockers[j].Kind
	})
	return blockers
}

func sortedAgentIDSet(values map[string]bool) []string {
	ids := make([]string, 0, len(values))
	for id, included := range values {
		if included {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func sortAgentBulkFailures(failures []agentBulkDeleteFailure) {
	sort.SliceStable(failures, func(i, j int) bool {
		if failures[i].ID == failures[j].ID {
			return failures[i].Code < failures[j].Code
		}
		return failures[i].ID < failures[j].ID
	})
}
