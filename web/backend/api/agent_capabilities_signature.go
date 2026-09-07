package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	picoagent "github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
)

type agentDefinitionSemanticSignature struct {
	AgentID     string   `json:"agent_id"`
	State       string   `json:"state,omitempty"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Model       string   `json:"model,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Skills      []string `json:"skills,omitempty"`
	MCPServers  []string `json:"mcp_servers,omitempty"`
	Tasks       []string `json:"tasks,omitempty"`
}

const (
	agentDefinitionSignatureAgentLimit      = 256
	agentDefinitionSignatureWorkspaceLimit  = 64
	agentDefinitionSignatureByteLimit       = 16 << 20
	malformedAgentFrontmatterSignatureState = "malformed_frontmatter"
)

func computeGatewayRuntimeSignature(cfg *config.Config) string {
	configSignature := computeConfigSignature(cfg)
	definitionSignature := computeAgentDefinitionsRuntimeSignature(cfg)
	if definitionSignature == gatewayUnknownBootConfigSignature {
		return gatewayUnknownBootConfigSignature
	}
	if definitionSignature == "" {
		return configSignature
	}
	if configSignature == "" {
		return "agent_definitions:" + definitionSignature
	}
	return configSignature + ";agent_definitions:" + definitionSignature
}

func computeAgentDefinitionsRuntimeSignature(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if len(cfg.Agents.List) > agentDefinitionSignatureAgentLimit {
		return gatewayUnknownBootConfigSignature
	}
	agents := make([]*config.AgentConfig, 0, len(cfg.Agents.List))
	if len(cfg.Agents.List) == 0 {
		agents = append(agents, nil)
	} else {
		for index := range cfg.Agents.List {
			agents = append(agents, &cfg.Agents.List[index])
		}
	}
	type cachedDefinitionSignature struct {
		entry    agentDefinitionSemanticSignature
		relevant bool
	}
	cache := make(
		map[string]cachedDefinitionSignature,
		min(len(agents), agentDefinitionSignatureWorkspaceLimit),
	)
	totalBytes := 0
	entries := make([]agentDefinitionSemanticSignature, 0, len(agents))
	for _, agentConfig := range agents {
		agentID := routing.DefaultAgentID
		if agentConfig != nil {
			agentID = agentConfig.ID
		}
		workspace := picoagent.ResolveAgentWorkspace(agentConfig, &cfg.Agents.Defaults)
		path := filepath.Clean(
			filepath.Join(workspace, agentDefinitionFileCurrent),
		)
		if cached, ok := cache[path]; ok {
			if cached.relevant {
				entry := cached.entry
				entry.AgentID = agentID
				entries = append(entries, entry)
			}
			continue
		}
		if len(cache) == agentDefinitionSignatureWorkspaceLimit {
			return gatewayUnknownBootConfigSignature
		}
		file, exists, err := readAgentDefinitionFile(path)
		if err != nil {
			entry := agentDefinitionSemanticSignature{
				State: agentDefinitionIssueCode(err),
			}
			cache[path] = cachedDefinitionSignature{entry: entry, relevant: true}
			entry.AgentID = agentID
			entries = append(entries, entry)
			continue
		}
		if !exists {
			legacyPath := filepath.Clean(
				filepath.Join(workspace, agentDefinitionFileLegacy),
			)
			legacy, legacyExists, legacyErr := readAgentDefinitionFile(
				legacyPath,
			)
			if legacyErr != nil {
				entry := agentDefinitionSemanticSignature{
					State: agentDefinitionFileLegacy + ":" +
						agentDefinitionIssueCode(legacyErr),
				}
				cache[path] = cachedDefinitionSignature{
					entry:    entry,
					relevant: true,
				}
				entry.AgentID = agentID
				entries = append(entries, entry)
				continue
			}
			if legacyExists {
				totalBytes += len(legacy.Data)
				if totalBytes > agentDefinitionSignatureByteLimit {
					return gatewayUnknownBootConfigSignature
				}
			}
			cache[path] = cachedDefinitionSignature{}
			continue
		}
		totalBytes += len(file.Data)
		if totalBytes > agentDefinitionSignatureByteLimit {
			return gatewayUnknownBootConfigSignature
		}
		entry, relevant := semanticAgentDefinitionSignature("", file.Data)
		cache[path] = cachedDefinitionSignature{entry: entry, relevant: relevant}
		if relevant {
			entry.AgentID = agentID
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return ""
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].AgentID < entries[right].AgentID
	})
	encoded, err := json.Marshal(entries)
	if err != nil {
		return "<invalid>"
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func semanticAgentDefinitionSignature(
	agentID string,
	data []byte,
) (agentDefinitionSemanticSignature, bool) {
	entry := agentDefinitionSemanticSignature{
		AgentID: agentID,
		Tasks:   picoagent.AgentDefinitionTasks(data),
	}
	frontmatter, _, _, ok := exactAgentFrontmatter(data)
	if !ok {
		if startsAgentFrontmatter(data) {
			entry.State = malformedAgentFrontmatterSignatureState
			return entry, true
		}
		return entry, len(entry.Tasks) > 0
	}
	runtimeFrontmatter, runtimeErr := parseRuntimeAgentFrontmatter(frontmatter)
	if runtimeErr != nil {
		entry.State = malformedAgentFrontmatterSignatureState
		return entry, true
	}
	var root yaml.Node
	if err := yaml.Unmarshal(frontmatter, &root); err != nil {
		entry.State = unsafeAgentFrontmatterState(frontmatter)
		return entry, true
	}
	if len(root.Content) == 0 ||
		len(root.Content) == 1 && isYAMLNull(root.Content[0]) {
		return entry, len(entry.Tasks) > 0
	}
	mapping, ok := yamlDocumentMapping(&root)
	if !ok || mappingHasDuplicateKeys(mapping) ||
		!yamlAgentFrontmatterRoundTripSafe(&root) {
		entry.State = unsafeAgentFrontmatterState(frontmatter)
		return entry, true
	}

	entry.Name = strings.TrimSpace(runtimeFrontmatter.Name)
	entry.Description = strings.TrimSpace(runtimeFrontmatter.Description)
	entry.Model = strings.TrimSpace(runtimeFrontmatter.Model)

	var malformed bool
	entry.Tools, malformed = semanticCapabilityField(mapping, "tools", true, true)
	if malformed {
		entry.State = unsafeAgentFrontmatterState(frontmatter)
		return entry, true
	}
	entry.Skills, malformed = semanticCapabilityField(mapping, "skills", false, false)
	if malformed {
		entry.State = unsafeAgentFrontmatterState(frontmatter)
		return entry, true
	}
	entry.MCPServers, malformed = semanticCapabilityField(
		mapping,
		"mcpServers",
		true,
		true,
	)
	if malformed {
		entry.State = unsafeAgentFrontmatterState(frontmatter)
		return entry, true
	}
	relevant := entry.Name != "" ||
		entry.Description != "" ||
		entry.Model != "" ||
		entry.Tools != nil ||
		entry.Skills != nil ||
		entry.MCPServers != nil ||
		len(entry.Tasks) > 0
	return entry, relevant
}

func unsafeAgentFrontmatterState(frontmatter []byte) string {
	digest := sha256.Sum256(frontmatter)
	return "unsafe:" + hex.EncodeToString(digest[:])
}

func semanticCapabilityField(
	mapping *yaml.Node,
	field string,
	nullMeansNone bool,
	unordered bool,
) ([]string, bool) {
	value, found := mappingValue(mapping, field)
	if !found || isYAMLNull(value) && !nullMeansNone {
		return nil, false
	}
	if isYAMLNull(value) {
		return []string{"<none>"}, false
	}
	if value.Kind != yaml.SequenceNode {
		return nil, true
	}
	seen := make(map[string]struct{}, len(value.Content))
	values := make([]string, 0, len(value.Content))
	for _, item := range value.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return nil, true
		}
		normalized := strings.ToLower(strings.TrimSpace(item.Value))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		values = append(values, normalized)
	}
	if len(values) == 0 {
		return []string{"<none>"}, false
	}
	if unordered {
		sort.Strings(values)
	}
	return values, false
}

func agentEffectsForRuntimeConfig(cfg *config.Config) agentEffects {
	currentSignature := computeGatewayRuntimeSignature(cfg)
	gateway.mu.Lock()
	bootSignature := gateway.bootConfigSignature
	runtimeStatus := gateway.runtimeStatus
	if runtimeStatus == "running" && !gatewayRuntimeAliveLocked() {
		runtimeStatus = "stopped"
	}
	gateway.mu.Unlock()
	gatewayEffect := "applied"
	if gatewayRestartRequiredBySignature(
		bootSignature,
		currentSignature,
		runtimeStatus,
	) {
		gatewayEffect = "restart_required"
	}
	return agentEffects{
		LauncherEffect: "applied",
		CatalogEffect:  "applied",
		GatewayEffect:  gatewayEffect,
	}
}
