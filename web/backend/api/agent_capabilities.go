package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	picoagent "github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/audio/tts"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/routing"
)

const (
	agentCapabilityCatalogLimit            = 256
	agentCapabilitiesRequestMaxBytes int64 = 4 << 20
)

var (
	agentCapabilitiesMutationMu             sync.Mutex
	agentCapabilitiesBeforeFinalFence       = func() {}
	agentCapabilitiesAfterConditionalCreate = func() {}
	writeAgentCapabilitiesFile              = writeAgentCapabilitiesFileIfUnchanged
)

type agentCapabilityPolicyRequest struct {
	Mode   string    `json:"mode"`
	Values *[]string `json:"values"`
}

type agentCapabilitiesPatchRequest struct {
	ExpectedRevision string                        `json:"expected_revision"`
	UpgradeLegacy    bool                          `json:"upgrade_legacy,omitempty"`
	Tools            *agentCapabilityPolicyRequest `json:"tools,omitempty"`
	Skills           *agentCapabilityPolicyRequest `json:"skills,omitempty"`
	MCPServers       *agentCapabilityPolicyRequest `json:"mcp_servers,omitempty"`
}

type agentCapabilitySkillCatalogItem struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

type agentCapabilityMCPServerCatalogItem struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type agentCapabilityToolCatalogItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Status      string `json:"status"`
	ReasonCode  string `json:"reason_code"`
}

type agentCapabilityCatalogs struct {
	Tools      []agentCapabilityToolCatalogItem      `json:"tools"`
	Skills     []agentCapabilitySkillCatalogItem     `json:"skills"`
	MCPServers []agentCapabilityMCPServerCatalogItem `json:"mcp_servers"`
}

type agentCapabilityCatalogTruncated struct {
	Tools      bool `json:"tools"`
	Skills     bool `json:"skills"`
	MCPServers bool `json:"mcp_servers"`
}

type agentCapabilityPolicyResponse struct {
	Mode   string   `json:"mode"`
	Values []string `json:"values"`
}

type agentCapabilitySkillsPolicyResponse struct {
	Mode            string   `json:"mode"`
	Values          []string `json:"values"`
	InheritedValues []string `json:"inherited_values"`
}

type agentCapabilitiesResponseValue struct {
	Tools      agentCapabilityPolicyResponse       `json:"tools"`
	Skills     agentCapabilitySkillsPolicyResponse `json:"skills"`
	MCPServers agentCapabilityPolicyResponse       `json:"mcp_servers"`
}

type agentCapabilitiesResponse struct {
	AgentID               string                          `json:"agent_id"`
	Source                string                          `json:"source"`
	Editable              bool                            `json:"editable"`
	IssueCode             string                          `json:"issue_code"`
	LegacyUpgradeRequired bool                            `json:"legacy_upgrade_required"`
	Capabilities          agentCapabilitiesResponseValue  `json:"capabilities"`
	Catalogs              agentCapabilityCatalogs         `json:"catalogs"`
	CatalogTruncated      agentCapabilityCatalogTruncated `json:"catalog_truncated"`
	Revision              string                          `json:"revision"`
	ConfigRevision        string                          `json:"config_revision"`
	Effects               agentEffects                    `json:"effects"`
}

type agentDefinitionState struct {
	document  agentCapabilitiesDocument
	source    string
	issueCode string
}

func (h *Handler) handleGetAgentCapabilities(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !routing.IsCanonicalAgentID(id) {
		writeAgentError(w, http.StatusBadRequest, "invalid_agent_id", nil)
		return
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	agentCapabilitiesMutationMu.Lock()
	defer agentCapabilitiesMutationMu.Unlock()

	cfg, configRevision, ok := h.loadCurrentAgentConfig(w)
	if !ok {
		return
	}
	agentConfig, found := capabilityAgentConfig(cfg, id)
	if !found {
		writeAgentError(w, http.StatusNotFound, "agent_not_found", nil)
		return
	}
	state := loadAgentDefinitionState(cfg, agentConfig)
	writeAgentJSON(
		w,
		http.StatusOK,
		buildAgentCapabilitiesResponse(id, cfg, agentConfig, configRevision, state),
	)
}

func (h *Handler) handlePatchAgentCapabilities(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !routing.IsCanonicalAgentID(id) {
		writeAgentError(w, http.StatusBadRequest, "invalid_agent_id", nil)
		return
	}

	var request agentCapabilitiesPatchRequest
	if !decodeAgentRequestWithMaxBytes(
		w,
		r,
		&request,
		agentCapabilitiesRequestMaxBytes,
	) {
		return
	}
	if request.ExpectedRevision == "" {
		writeAgentError(w, http.StatusBadRequest, "expected_revision_required", nil)
		return
	}
	if request.Tools == nil && request.Skills == nil &&
		request.MCPServers == nil && !request.UpgradeLegacy {
		writeAgentError(w, http.StatusBadRequest, "capability_patch_required", nil)
		return
	}

	tools, skills, mcpServers, ok := validateCapabilitiesPatch(w, request)
	if !ok {
		return
	}

	h.configMutationMu.Lock()
	defer h.configMutationMu.Unlock()
	agentCapabilitiesMutationMu.Lock()
	defer agentCapabilitiesMutationMu.Unlock()

	cfg, configRevision, loaded := h.loadCurrentAgentConfig(w)
	if !loaded {
		return
	}
	agentConfig, found := capabilityAgentConfig(cfg, id)
	if !found {
		writeAgentError(w, http.StatusNotFound, "agent_not_found", nil)
		return
	}
	state := loadAgentDefinitionState(cfg, agentConfig)
	if state.issueCode != "" ||
		!agentCapabilitiesConditionalCreateSupported() {
		writeAgentError(w, http.StatusConflict, "capabilities_not_editable", nil)
		return
	}
	currentRevision := agentCapabilitiesRevision(id, configRevision, state)
	if request.ExpectedRevision != currentRevision {
		writeAgentError(w, http.StatusConflict, "capabilities_revision_mismatch", nil)
		return
	}
	if state.source == agentDefinitionSourceLegacy && !request.UpgradeLegacy {
		writeAgentError(w, http.StatusConflict, "legacy_upgrade_required", nil)
		return
	}
	if request.UpgradeLegacy && state.source != agentDefinitionSourceLegacy {
		writeAgentError(w, http.StatusUnprocessableEntity, "legacy_upgrade_not_available", nil)
		return
	}

	document := state.document
	workspace := picoagent.ResolveAgentWorkspace(agentConfig, &cfg.Agents.Defaults)
	targetPath := filepath.Join(workspace, agentDefinitionFileCurrent)
	requiresWrite := false
	if state.source == agentDefinitionSourceLegacy {
		document.source = agentDefinitionSourceCurrent
		document.path = targetPath
		document.hasFrontmatter = false
		document.frontmatterStart = 0
		document.frontmatterEnd = 0
		document.root = newAgentCapabilitiesYAMLRoot()
		requiresWrite = true
	} else if state.source == agentDefinitionSourceNone {
		document.source = agentDefinitionSourceCurrent
		document.path = targetPath
		document.mode = 0o644
	}

	if tools != nil && !capabilityPoliciesEqual(document.capabilities.Tools, *tools) {
		if err := applyCapabilityPolicy(&document, "tools", *tools, capabilityModeAll); err != nil {
			writeAgentError(w, http.StatusConflict, "capabilities_not_editable", nil)
			return
		}
		document.capabilities.Tools = *tools
		requiresWrite = true
	}
	if skills != nil && !capabilityPoliciesEqual(document.capabilities.Skills, *skills) {
		if err := applyCapabilityPolicy(&document, "skills", *skills, capabilityModeInherit); err != nil {
			writeAgentError(w, http.StatusConflict, "capabilities_not_editable", nil)
			return
		}
		document.capabilities.Skills = *skills
		requiresWrite = true
	}
	if mcpServers != nil &&
		!capabilityPoliciesEqual(document.capabilities.MCPServers, *mcpServers) {
		if err := applyCapabilityPolicy(
			&document,
			"mcpServers",
			*mcpServers,
			capabilityModeAll,
		); err != nil {
			writeAgentError(w, http.StatusConflict, "capabilities_not_editable", nil)
			return
		}
		document.capabilities.MCPServers = *mcpServers
		requiresWrite = true
	}

	candidate := append([]byte(nil), document.raw...)
	if requiresWrite {
		var renderErr error
		candidate, renderErr = renderAgentCapabilitiesDocument(document)
		if renderErr != nil {
			writeAgentError(w, http.StatusInternalServerError, "capabilities_save_failed", nil)
			return
		}
		if len(candidate) > agentDefinitionMaxBytes {
			writeAgentError(w, http.StatusUnprocessableEntity, "invalid_capabilities", nil)
			return
		}
	}

	agentCapabilitiesBeforeFinalFence()
	var fencedState agentDefinitionState
	fenceErr := config.WithConfigMutationLock(h.configPath, func() error {
		fencedConfigRevision, err := config.ConfigRevision(h.configPath)
		if err != nil || fencedConfigRevision != configRevision {
			return config.ErrConfigRevisionMismatch
		}
		fencedState = loadAgentDefinitionState(cfg, agentConfig)
		if agentCapabilitiesRevision(id, configRevision, fencedState) != currentRevision {
			return errAgentCapabilitiesRevisionMismatch
		}
		if !requiresWrite {
			return nil
		}
		if err = ensureAgentDefinitionTargetSafe(targetPath); err != nil {
			return err
		}
		expectedTargetExists := fencedState.source == agentDefinitionSourceCurrent
		if !expectedTargetExists &&
			!agentCapabilitiesConditionalCreateSupported() {
			return errAgentCapabilitiesAtomicReplaceUnavailable
		}
		permission := agentCapabilitiesFilePermission(
			fs.FileMode(fencedState.document.mode),
			expectedTargetExists,
		)
		expectedTarget := agentDefinitionFile{}
		if expectedTargetExists {
			expectedTarget = agentDefinitionFile{
				Data: append([]byte(nil), fencedState.document.raw...),
				Mode: fs.FileMode(fencedState.document.mode).Perm(),
			}
		}
		legacyPath := filepath.Join(workspace, agentDefinitionFileLegacy)
		expectedLegacy := agentDefinitionFile{}
		expectedLegacyExists := fencedState.source == agentDefinitionSourceLegacy
		if expectedLegacyExists {
			expectedLegacy = agentDefinitionFile{
				Data: append([]byte(nil), fencedState.document.raw...),
				Mode: fs.FileMode(fencedState.document.mode).Perm(),
			}
		}
		if !expectedTargetExists &&
			!agentCapabilitiesSourceMatches(
				legacyPath,
				expectedLegacy,
				expectedLegacyExists,
			) {
			return errAgentCapabilitiesRevisionMismatch
		}
		writeResult, err := writeAgentCapabilitiesFile(
			targetPath,
			candidate,
			permission,
			expectedTarget,
			expectedTargetExists,
		)
		if err != nil {
			var visibleCommit *agentCapabilitiesVisibleCommitError
			if !expectedTargetExists &&
				errors.As(err, &visibleCommit) &&
				agentCapabilitiesTargetMatches(
					targetPath,
					agentDefinitionFile{
						Data: candidate,
						Mode: permission,
					},
					true,
				) &&
				agentCapabilitiesTargetIdentityMatches(
					targetPath,
					writeResult.candidateIdentity,
				) {
				rollbackErr := rollbackCreatedAgentCapabilitiesFile(
					targetPath,
					agentDefinitionFile{
						Data: candidate,
						Mode: permission,
					},
					writeResult.candidateIdentity,
				)
				if errors.Is(
					rollbackErr,
					errAgentCapabilitiesRevisionMismatch,
				) {
					return errAgentCapabilitiesRevisionMismatch
				}
				if rollbackErr != nil {
					return fmt.Errorf(
						"roll back uncommitted agent capability creation after %v: %w",
						err,
						rollbackErr,
					)
				}
			}
			return err
		}
		if !expectedTargetExists {
			agentCapabilitiesAfterConditionalCreate()
			if !agentCapabilitiesSourceMatches(
				legacyPath,
				expectedLegacy,
				expectedLegacyExists,
			) {
				rollbackErr := rollbackCreatedAgentCapabilitiesFile(
					targetPath,
					agentDefinitionFile{
						Data: candidate,
						Mode: permission,
					},
					writeResult.candidateIdentity,
				)
				if rollbackErr != nil &&
					!errors.Is(
						rollbackErr,
						errAgentCapabilitiesRevisionMismatch,
					) {
					return fmt.Errorf(
						"roll back stale agent capability migration: %w",
						rollbackErr,
					)
				}
				return errAgentCapabilitiesRevisionMismatch
			}
		}
		fencedState = loadAgentDefinitionState(cfg, agentConfig)
		if fencedState.issueCode != "" ||
			fencedState.source != agentDefinitionSourceCurrent ||
			!bytes.Equal(fencedState.document.raw, candidate) {
			return errAgentCapabilitiesWriteVerification
		}
		return nil
	})
	switch {
	case errors.Is(fenceErr, config.ErrConfigRevisionMismatch),
		errors.Is(fenceErr, errAgentCapabilitiesRevisionMismatch):
		writeAgentError(w, http.StatusConflict, "capabilities_revision_mismatch", nil)
		return
	case fenceErr != nil:
		logger.ErrorCF(
			"agent-capabilities",
			"failed to save agent capabilities",
			map[string]any{
				"agent_id": id,
				"error":    fenceErr.Error(),
			},
		)
		writeAgentError(w, http.StatusInternalServerError, "capabilities_save_failed", nil)
		return
	}

	writeAgentJSON(
		w,
		http.StatusOK,
		buildAgentCapabilitiesResponse(
			id,
			cfg,
			agentConfig,
			configRevision,
			fencedState,
		),
	)
}

func agentCapabilitiesFilePermission(
	mode fs.FileMode,
	targetExists bool,
) fs.FileMode {
	permission := mode.Perm()
	if !targetExists && permission == 0 {
		return 0o644
	}
	return permission
}

var (
	errAgentCapabilitiesRevisionMismatch  = errors.New("agent capabilities revision mismatch")
	errAgentCapabilitiesWriteVerification = errors.New("agent capabilities write verification failed")
)

func validateCapabilitiesPatch(
	w http.ResponseWriter,
	request agentCapabilitiesPatchRequest,
) (
	*agentCapabilityPolicy,
	*agentCapabilityPolicy,
	*agentCapabilityPolicy,
	bool,
) {
	allModes := map[string]struct{}{
		capabilityModeAll:      {},
		capabilityModeNone:     {},
		capabilityModeSelected: {},
	}
	skillModes := map[string]struct{}{
		capabilityModeInherit:  {},
		capabilityModeNone:     {},
		capabilityModeSelected: {},
	}
	convert := func(
		value *agentCapabilityPolicyRequest,
		modes map[string]struct{},
		lower bool,
	) (*agentCapabilityPolicy, error) {
		if value == nil {
			return nil, nil
		}
		if value.Values == nil {
			return nil, errors.New("capability values are required")
		}
		returnPolicy, err := validateCapabilityPolicy(agentCapabilityPolicy{
			Mode:   value.Mode,
			Values: *value.Values,
		}, modes, lower)
		return &returnPolicy, err
	}
	tools, err := convert(request.Tools, allModes, true)
	if err != nil {
		writeAgentError(w, http.StatusUnprocessableEntity, "invalid_capabilities", nil)
		return nil, nil, nil, false
	}
	skills, err := convert(request.Skills, skillModes, false)
	if err != nil {
		writeAgentError(w, http.StatusUnprocessableEntity, "invalid_capabilities", nil)
		return nil, nil, nil, false
	}
	mcpServers, err := convert(request.MCPServers, allModes, true)
	if err != nil {
		writeAgentError(w, http.StatusUnprocessableEntity, "invalid_capabilities", nil)
		return nil, nil, nil, false
	}
	return tools, skills, mcpServers, true
}

func capabilityAgentConfig(cfg *config.Config, id string) (*config.AgentConfig, bool) {
	if cfg == nil {
		return nil, false
	}
	if len(cfg.Agents.List) == 0 {
		return nil, id == routing.DefaultAgentID
	}
	for index := range cfg.Agents.List {
		if cfg.Agents.List[index].ID == id {
			return &cfg.Agents.List[index], true
		}
	}
	return nil, false
}

func loadAgentDefinitionState(
	cfg *config.Config,
	agentConfig *config.AgentConfig,
) agentDefinitionState {
	workspace := picoagent.ResolveAgentWorkspace(agentConfig, &cfg.Agents.Defaults)
	currentPath := filepath.Join(workspace, agentDefinitionFileCurrent)
	current, exists, err := readAgentDefinitionFile(currentPath)
	if err != nil {
		return agentDefinitionState{
			source:    agentDefinitionSourceCurrent,
			issueCode: agentDefinitionIssueCode(err),
			document: agentCapabilitiesDocument{
				source:       agentDefinitionSourceCurrent,
				path:         currentPath,
				capabilities: defaultAgentCapabilities(capabilityInheritedSkills(agentConfig)),
			},
		}
	}
	if exists {
		document, parseErr := parseAgentCapabilitiesDocument(
			agentDefinitionSourceCurrent,
			currentPath,
			current,
			capabilityInheritedSkills(agentConfig),
		)
		state := agentDefinitionState{
			source:   agentDefinitionSourceCurrent,
			document: document,
		}
		if parseErr != nil {
			state.issueCode = "agent_definition_invalid"
		}
		return state
	}

	legacyPath := filepath.Join(workspace, agentDefinitionFileLegacy)
	legacy, exists, err := readAgentDefinitionFile(legacyPath)
	if err != nil {
		return agentDefinitionState{
			source:    agentDefinitionSourceLegacy,
			issueCode: agentDefinitionIssueCode(err),
			document: agentCapabilitiesDocument{
				source:       agentDefinitionSourceLegacy,
				path:         legacyPath,
				capabilities: defaultAgentCapabilities(capabilityInheritedSkills(agentConfig)),
			},
		}
	}
	if exists {
		document, _ := parseAgentCapabilitiesDocument(
			agentDefinitionSourceLegacy,
			legacyPath,
			legacy,
			capabilityInheritedSkills(agentConfig),
		)
		return agentDefinitionState{
			source:   agentDefinitionSourceLegacy,
			document: document,
		}
	}
	return agentDefinitionState{
		source: agentDefinitionSourceNone,
		document: agentCapabilitiesDocument{
			source:       agentDefinitionSourceNone,
			path:         currentPath,
			mode:         0o644,
			newline:      "\n",
			capabilities: defaultAgentCapabilities(capabilityInheritedSkills(agentConfig)),
		},
	}
}

func capabilityInheritedSkills(agentConfig *config.AgentConfig) []string {
	if agentConfig == nil {
		return []string{}
	}
	return normalizedCapabilityValues(agentConfig.Skills, false)
}

func agentCapabilitiesRevision(
	agentID string,
	configRevision string,
	state agentDefinitionState,
) string {
	digest := sha256.New()
	var length [8]byte
	for _, part := range []string{
		agentID,
		configRevision,
		state.source,
		state.issueCode,
		string(state.document.raw),
	} {
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		digest.Write(length[:])
		digest.Write([]byte(part))
	}
	binary.BigEndian.PutUint64(
		length[:],
		uint64(fs.FileMode(state.document.mode).Perm()),
	)
	digest.Write(length[:])
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func buildAgentCapabilitiesResponse(
	id string,
	cfg *config.Config,
	agentConfig *config.AgentConfig,
	configRevision string,
	state agentDefinitionState,
) agentCapabilitiesResponse {
	capabilities := state.document.capabilities
	ensureCapabilityResponseArrays(&capabilities)
	issueCode := state.issueCode
	if issueCode == "" && !agentCapabilitiesConditionalCreateSupported() {
		issueCode = "atomic_replace_unavailable"
	}
	catalogs, truncated := buildAgentCapabilityCatalogs(
		cfg,
		agentConfig,
		picoagent.ResolveAgentWorkspace(agentConfig, &cfg.Agents.Defaults),
		state.document.model,
	)
	return agentCapabilitiesResponse{
		AgentID:               id,
		Source:                state.source,
		Editable:              issueCode == "" && state.source != agentDefinitionSourceLegacy,
		IssueCode:             issueCode,
		LegacyUpgradeRequired: issueCode == "" && state.source == agentDefinitionSourceLegacy,
		Capabilities: agentCapabilitiesResponseValue{
			Tools: agentCapabilityPolicyResponse{
				Mode:   capabilities.Tools.Mode,
				Values: capabilities.Tools.Values,
			},
			Skills: agentCapabilitySkillsPolicyResponse{
				Mode:            capabilities.Skills.Mode,
				Values:          capabilities.Skills.Values,
				InheritedValues: capabilities.Skills.InheritedValues,
			},
			MCPServers: agentCapabilityPolicyResponse{
				Mode:   capabilities.MCPServers.Mode,
				Values: capabilities.MCPServers.Values,
			},
		},
		Catalogs:         catalogs,
		CatalogTruncated: truncated,
		Revision:         agentCapabilitiesRevision(id, configRevision, state),
		ConfigRevision:   configRevision,
		Effects:          agentEffectsForRuntimeConfig(cfg),
	}
}

func ensureCapabilityResponseArrays(capabilities *agentCapabilities) {
	if capabilities.Tools.Values == nil {
		capabilities.Tools.Values = []string{}
	}
	if capabilities.Skills.Values == nil {
		capabilities.Skills.Values = []string{}
	}
	if capabilities.Skills.InheritedValues == nil {
		capabilities.Skills.InheritedValues = []string{}
	}
	if capabilities.MCPServers.Values == nil {
		capabilities.MCPServers.Values = []string{}
	}
}

func buildAgentCapabilityCatalogs(
	cfg *config.Config,
	agentConfig *config.AgentConfig,
	workspace string,
	definitionModel string,
) (agentCapabilityCatalogs, agentCapabilityCatalogTruncated) {
	toolSupport := buildAgentCapabilityToolSupport(cfg, agentConfig, definitionModel)
	toolItems := make([]agentCapabilityToolCatalogItem, 0, len(toolSupport))
	for _, item := range toolSupport {
		toolItems = append(toolItems, agentCapabilityToolCatalogItem{
			Name:        item.Name,
			Description: item.Description,
			Category:    item.Category,
			Status:      item.Status,
			ReasonCode:  item.ReasonCode,
		})
	}
	sort.SliceStable(toolItems, func(left, right int) bool {
		return toolItems[left].Name < toolItems[right].Name
	})

	skillItems, skillsTruncated := buildAgentCapabilitySkillCatalog(workspace)
	sort.SliceStable(skillItems, func(left, right int) bool {
		return strings.ToLower(skillItems[left].Name) <
			strings.ToLower(skillItems[right].Name)
	})

	mcpNames := make([]string, 0, len(cfg.Tools.MCP.Servers))
	for name := range cfg.Tools.MCP.Servers {
		mcpNames = append(mcpNames, name)
	}
	sort.Strings(mcpNames)
	mcpItems := make([]agentCapabilityMCPServerCatalogItem, 0, len(mcpNames))
	seenMCPNames := make(map[string]struct{}, len(mcpNames))
	mcpProjectionTruncated := false
	for _, name := range mcpNames {
		if !safeCapabilityIdentifier(name) {
			mcpProjectionTruncated = true
			continue
		}
		normalizedName := strings.ToLower(name)
		if _, exists := seenMCPNames[normalizedName]; exists {
			mcpProjectionTruncated = true
			continue
		}
		seenMCPNames[normalizedName] = struct{}{}
		server := cfg.Tools.MCP.Servers[name]
		mcpItems = append(mcpItems, agentCapabilityMCPServerCatalogItem{
			Name:    normalizedName,
			Enabled: cfg.Tools.MCP.Enabled && server.Enabled,
		})
	}
	sort.SliceStable(mcpItems, func(left, right int) bool {
		return strings.ToLower(mcpItems[left].Name) <
			strings.ToLower(mcpItems[right].Name)
	})

	toolsTruncated := len(toolItems) > agentCapabilityCatalogLimit
	if toolsTruncated {
		toolItems = toolItems[:agentCapabilityCatalogLimit]
	}
	mcpTruncated := mcpProjectionTruncated ||
		len(mcpItems) > agentCapabilityCatalogLimit
	if len(mcpItems) > agentCapabilityCatalogLimit {
		mcpItems = mcpItems[:agentCapabilityCatalogLimit]
	}
	if toolItems == nil {
		toolItems = []agentCapabilityToolCatalogItem{}
	}
	if skillItems == nil {
		skillItems = []agentCapabilitySkillCatalogItem{}
	}
	if mcpItems == nil {
		mcpItems = []agentCapabilityMCPServerCatalogItem{}
	}
	return agentCapabilityCatalogs{
			Tools:      toolItems,
			Skills:     skillItems,
			MCPServers: mcpItems,
		}, agentCapabilityCatalogTruncated{
			Tools:      toolsTruncated,
			Skills:     skillsTruncated,
			MCPServers: mcpTruncated,
		}
}

func buildAgentCapabilityToolSupport(
	cfg *config.Config,
	agentConfig *config.AgentConfig,
	definitionModels ...string,
) []toolSupportItem {
	definitionModel := ""
	if len(definitionModels) > 0 {
		definitionModel = definitionModels[0]
	}
	items := buildToolSupport(cfg)
	seen := make(map[string]struct{}, len(items)+10)
	for _, item := range items {
		seen[item.Name] = struct{}{}
	}
	add := func(
		name string,
		description string,
		category string,
		status string,
		reasonCode string,
	) {
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		items = append(items, toolSupportItem{
			Name:        name,
			Description: description,
			Category:    category,
			Status:      status,
			ReasonCode:  reasonCode,
		})
	}
	configuredStatus := func(enabled bool) string {
		if enabled {
			return "enabled"
		}
		return "disabled"
	}

	add(
		"reaction",
		"Add a reaction to the active chat message.",
		"communication",
		configuredStatus(cfg.Tools.IsToolEnabled("reaction")),
		"",
	)
	add(
		"load_image",
		"Load a local image for visual inspection.",
		"filesystem",
		configuredStatus(cfg.Tools.IsToolEnabled("load_image")),
		"",
	)
	sendTTSStatus := configuredStatus(cfg.Tools.IsToolEnabled("send_tts"))
	sendTTSReason := ""
	if sendTTSStatus == "enabled" && tts.DetectTTS(cfg) == nil {
		sendTTSStatus = "blocked"
		sendTTSReason = "requires_tts_provider"
	}
	add(
		"send_tts",
		"Synthesize speech and send it to the active chat.",
		"communication",
		sendTTSStatus,
		sendTTSReason,
	)

	subagentStatus := "disabled"
	subagentReason := ""
	if cfg.Tools.IsToolEnabled("spawn") {
		if cfg.Tools.IsToolEnabled("subagent") {
			subagentStatus = "enabled"
		} else {
			subagentStatus = "blocked"
			subagentReason = "requires_subagent"
		}
	}
	add(
		"subagent",
		"Run a synchronous task in an independent subagent.",
		"agents",
		subagentStatus,
		subagentReason,
	)

	delegateStatus := "disabled"
	if len(cfg.Agents.List) > 1 {
		delegateStatus = "enabled"
	}
	add(
		"delegate",
		"Delegate a task to another configured agent and wait for its result.",
		"agents",
		delegateStatus,
		"",
	)

	resolvedModel := picoagent.ResolveAgentModelFromDefinition(
		agentConfig,
		&cfg.Agents.Defaults,
		definitionModel,
	)
	codexCompatible := picoagent.AgentModelMayUseCodexCompatibleTools(
		resolvedModel,
		&cfg.Agents.Defaults,
		cfg,
	)
	addCodex := func(
		name string,
		description string,
		category string,
		dependencyEnabled bool,
	) {
		status := "disabled"
		reasonCode := ""
		if dependencyEnabled {
			if codexCompatible {
				status = "enabled"
			} else {
				status = "blocked"
				reasonCode = "requires_codex_surface"
			}
		}
		add(name, description, category, status, reasonCode)
	}
	addCodex(
		"apply_patch",
		"Apply Codex-style file patches using the configured write permissions.",
		"filesystem",
		cfg.Tools.IsToolEnabled("edit_file") ||
			cfg.Tools.IsToolEnabled("write_file"),
	)
	addCodex(
		"exec_command",
		"Run shell commands with the Codex-compatible command interface.",
		"filesystem",
		cfg.Tools.IsToolEnabled("exec"),
	)
	addCodex(
		"write_stdin",
		"Write input to a running Codex-compatible command session.",
		"filesystem",
		cfg.Tools.IsToolEnabled("exec"),
	)
	addCodex(
		"view_image",
		"Load a local image through the Codex-compatible image interface.",
		"filesystem",
		cfg.Tools.IsToolEnabled("load_image"),
	)
	addCodex(
		"update_plan",
		"Maintain a structured task plan through the Codex-compatible interface.",
		"agents",
		true,
	)
	return items
}
