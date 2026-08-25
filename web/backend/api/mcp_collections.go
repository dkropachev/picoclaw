package api

import (
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	picoagent "github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/routing"
)

var mcpServerCollectionSchema = mustCollectionQuerySchema(
	[]collectionquery.FieldSchema{
		{Name: "name", Type: collectionquery.TypeString, Sortable: true},
		{
			Name:            "enabled",
			Type:            collectionquery.TypeBoolean,
			Sortable:        true,
			SuggestedValues: []string{"true", "false"},
		},
		{
			Name:            "deferred",
			Type:            collectionquery.TypeBoolean,
			Sortable:        true,
			SuggestedValues: []string{"true", "false"},
		},
		{
			Name:            "type",
			Type:            collectionquery.TypeEnum,
			Sortable:        true,
			SuggestedValues: []string{"stdio", "http", "sse"},
		},
		{
			Name:            "auth",
			Type:            collectionquery.TypeEnum,
			Sortable:        true,
			SuggestedValues: []string{"none", "custom", "bearer", "oauth"},
		},
	},
	[]collectionquery.SortField{{Field: "name", Direction: collectionquery.Ascending}},
)

// mcpServerCollectionSummary deliberately excludes executable arguments,
// endpoint paths, environment-file paths, and key inventories. Those belong
// only to the direct item endpoint and editor.
type mcpServerCollectionSummary struct {
	Name                string         `json:"name"`
	Enabled             bool           `json:"enabled"`
	Deferred            *bool          `json:"deferred"`
	Type                string         `json:"type"`
	Address             string         `json:"address,omitempty"`
	EnvironmentKeyCount int            `json:"environment_key_count"`
	HeaderKeyCount      int            `json:"header_key_count"`
	Auth                mcpAuthSummary `json:"auth"`
}

func projectMCPServerCollectionSummary(server mcpServerSummary) mcpServerCollectionSummary {
	address := strings.TrimSpace(server.Command)
	if server.Type == "stdio" {
		if separator := strings.LastIndexAny(address, `/\`); separator >= 0 {
			address = address[separator+1:]
		}
	} else if endpoint, err := url.Parse(server.URL); err == nil && endpoint.Scheme != "" && endpoint.Host != "" {
		address = endpoint.Scheme + "://" + endpoint.Host
	} else {
		address = ""
	}
	address = strings.TrimSpace(strings.ToValidUTF8(address, ""))
	if len(address) > 256 {
		address = address[:256]
		for !utf8.ValidString(address) {
			address = address[:len(address)-1]
		}
	}
	return mcpServerCollectionSummary{
		Name: server.Name, Enabled: server.Enabled, Deferred: cloneBoolPointer(server.Deferred),
		Type: server.Type, Address: address,
		EnvironmentKeyCount: len(server.EnvKeys), HeaderKeyCount: len(server.HeaderKeys),
		Auth: server.Auth,
	}
}

func (h *Handler) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	listRequest, ok := parseCollectionListRequest(w, r, mcpServerCollectionSchema)
	if !ok {
		return
	}
	cfg, revision, err := config.LoadConfigSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	response, err := buildMCPConfigResponse(cfg)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"mcp_credentials_failed",
			"Failed to load MCP credential status",
			-1,
			nil,
		)
		return
	}
	summaries := make([]mcpServerCollectionSummary, len(response.Servers))
	names := make([]string, 0, len(response.Servers))
	for index, server := range response.Servers {
		summaries[index] = projectMCPServerCollectionSummary(server)
		names = append(names, server.Name)
	}
	sort.Strings(names)
	page, err := collectionquery.Paginate(
		summaries, listRequest.Query, listRequest.Cursor, listRequest.Limit, listRequest.Now,
		collectionquery.PageOptions[mcpServerCollectionSummary]{
			ID:         func(server mcpServerCollectionSummary) (string, error) { return server.Name, nil },
			ValidateID: func(name string) bool { return validateMCPServerName(name) == nil },
			Resolve: func(server mcpServerCollectionSummary, field collectionquery.Field, _ time.Time) (collectionquery.FieldValue, bool) {
				switch field {
				case "name":
					return collectionquery.StringValue(server.Name), true
				case "enabled":
					return collectionquery.BooleanValue(server.Enabled), true
				case "deferred":
					return collectionquery.BooleanValue(server.Deferred != nil && *server.Deferred), true
				case "type":
					return collectionquery.EnumValue(server.Type), true
				case "auth":
					return collectionquery.EnumValue(server.Auth.Type), true
				default:
					return collectionquery.FieldValue{}, false
				}
			},
		},
	)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"servers": page.Items, "total": page.Total, "next_cursor": page.NextCursor,
		"canonical_query": listRequest.Query.Canonical(),
		"query_schema": collectionSchemaWithSuggestions(
			mcpServerCollectionSchema,
			map[collectionquery.Field][]string{"name": names},
		),
		"config_revision": revision,
	})
}

func (h *Handler) handleGetMCPServer(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	cfg, revision, err := config.LoadConfigSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	actualName := findMCPServerName(cfg.Tools.MCP.Servers, r.PathValue("name"))
	if actualName == "" {
		writeCollectionError(w, http.StatusNotFound, "mcp_server_not_found", "MCP server not found", -1, nil)
		return
	}
	response, err := buildMCPConfigResponse(cfg)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"mcp_credentials_failed",
			"Failed to load MCP credential status",
			-1,
			nil,
		)
		return
	}
	for _, server := range response.Servers {
		if server.Name == actualName {
			writeCollectionJSON(w, http.StatusOK, map[string]any{"server": server, "config_revision": revision})
			return
		}
	}
	writeCollectionError(w, http.StatusNotFound, "mcp_server_not_found", "MCP server not found", -1, nil)
}

func (h *Handler) handleBulkDeleteMCPServers(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request collectionBulkDeleteRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	if len(request.IDs) == 0 || len(request.IDs) > 200 {
		writeCollectionError(
			w,
			http.StatusBadRequest,
			"invalid_bulk_delete",
			"Bulk deletion requires between 1 and 200 IDs",
			-1,
			nil,
		)
		return
	}

	unlock := sync.OnceFunc(h.lockMCPConfigMutation())
	defer unlock()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeCollectionError(
			w,
			http.StatusInternalServerError,
			"config_load_failed",
			"Failed to load configuration",
			-1,
			nil,
		)
		return
	}
	bodyRevision, ok := bulkCollectionRevision(w, request)
	if !ok {
		return
	}
	expectedRevision, ok := resolveCollectionRevision(w, r, bodyRevision)
	if !ok || !requireCollectionRevision(w, expectedRevision, revision) {
		return
	}

	requested, failures := normalizeMCPBulkIDs(cfg.Tools.MCP.Servers, request.IDs)
	deleteNames := make(map[string]bool, len(requested))
	credentialOwners := make(map[string][]string)
	for _, requestedName := range requested {
		name := findMCPServerName(cfg.Tools.MCP.Servers, requestedName)
		if name == "" {
			failures = append(failures, collectionBulkFailure{ID: requestedName, Code: "not_found"})
			continue
		}
		if blockers := mcpServerReferences(cfg, name); len(blockers) > 0 {
			failures = append(failures, collectionBulkFailure{ID: name, Code: "referenced", Blockers: blockers})
			continue
		}
		server := cfg.Tools.MCP.Servers[name]
		if server.Auth != nil {
			if credentialID, credentialErr := picomcp.CredentialID(
				name,
				server.Auth,
			); credentialErr == nil &&
				credentialID != "" {
				credentialOwners[credentialID] = append(credentialOwners[credentialID], name)
			}
		}
		deleteNames[name] = true
	}
	deleted := make([]string, 0, len(deleteNames))
	for name := range deleteNames {
		delete(cfg.Tools.MCP.Servers, name)
		deleted = append(deleted, name)
	}
	if len(cfg.Tools.MCP.Servers) == 0 {
		cfg.Tools.MCP.Enabled = false
	}
	nextRevision := revision
	if len(deleted) > 0 {
		nextRevision, err = mcpSaveConfigIfRevision(h.configPath, cfg, revision)
		if err != nil {
			writeMCPConfigSaveError(w, err)
			return
		}
		for credentialID := range credentialOwners {
			if !mcpCredentialReferenced(cfg.Tools.MCP.Servers, credentialID) {
				continue
			}
			delete(credentialOwners, credentialID)
		}
	}
	cleanupFailures := cleanupMCPUnreferencedCredentials(credentialOwners)
	sort.Strings(deleted)
	sortCollectionFailures(failures)
	unlock()
	writeCollectionJSON(
		w,
		http.StatusOK,
		collectionBulkDeleteResponse{
			DeletedIDs: deleted, Failures: failures, CleanupFailures: cleanupFailures,
			ConfigRevision: nextRevision,
			Effects:        agentEffectsForConfig(cfg),
		},
	)
}

func normalizeMCPBulkIDs(
	servers map[string]config.MCPServerConfig,
	ids []string,
) ([]string, []collectionBulkFailure) {
	groups := make(map[string][]string, len(ids))
	for _, raw := range ids {
		trimmed := strings.TrimSpace(raw)
		groups[strings.ToLower(trimmed)] = append(groups[strings.ToLower(trimmed)], trimmed)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	requested := make([]string, 0, len(keys))
	failures := make([]collectionBulkFailure, 0)
	for _, key := range keys {
		values := groups[key]
		sort.Strings(values)
		id := values[0]
		if actual := findMCPServerName(servers, id); actual != "" {
			id = actual
		}
		if key == "" {
			failures = append(failures, collectionBulkFailure{ID: "", Code: "invalid_id"})
			continue
		}
		if len(values) > 1 {
			failures = append(failures, collectionBulkFailure{ID: id, Code: "duplicate_id"})
			continue
		}
		requested = append(requested, values[0])
	}
	return requested, failures
}

func mcpServerReferences(cfg *config.Config, serverName string) []string {
	if cfg == nil || strings.TrimSpace(serverName) == "" {
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(serverName))
	agents := make([]*config.AgentConfig, 0, len(cfg.Agents.List)+1)
	if len(cfg.Agents.List) == 0 {
		agents = append(agents, nil)
	} else {
		for index := range cfg.Agents.List {
			agents = append(agents, &cfg.Agents.List[index])
		}
	}
	seenPaths := make(map[string]agentDefinitionSemanticSignature)
	blockers := make([]string, 0)
	for _, agentConfig := range agents {
		agentID := routing.DefaultAgentID
		if agentConfig != nil {
			agentID = agentConfig.ID
		}
		workspace := picoagent.ResolveAgentWorkspace(agentConfig, &cfg.Agents.Defaults)
		path := filepath.Clean(filepath.Join(workspace, agentDefinitionFileCurrent))
		signature, cached := seenPaths[path]
		if !cached {
			file, exists, err := readAgentDefinitionFile(path)
			if err != nil {
				signature = agentDefinitionSemanticSignature{State: "unavailable"}
				seenPaths[path] = signature
			} else if !exists {
				seenPaths[path] = agentDefinitionSemanticSignature{}
				continue
			} else {
				signature, _ = semanticAgentDefinitionSignature("", file.Data)
				seenPaths[path] = signature
			}
		}
		if signature.State != "" {
			blockers = append(blockers, fmt.Sprintf("agent:%s:definition_unavailable", agentID))
			continue
		}
		for _, selected := range signature.MCPServers {
			if strings.ToLower(strings.TrimSpace(selected)) == want {
				blockers = append(blockers, fmt.Sprintf("agent:%s", agentID))
				break
			}
		}
	}
	sort.Strings(blockers)
	return blockers
}
