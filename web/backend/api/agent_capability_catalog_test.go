package api

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	picoagent "github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	picoskills "github.com/sipeed/picoclaw/pkg/skills"
)

func TestAgentCapabilitySkillCatalogMatchesRuntimeMetadataAndPrecedence(t *testing.T) {
	workspace, globalSkills, builtinSkills := isolatedAgentCapabilitySkillRoots(t)

	writeAgentCapabilitySkill(
		t,
		filepath.Join(workspace, "skills"),
		"workspace-renamed",
		"shared-name",
		"workspace version",
	)
	writeAgentCapabilitySkill(
		t,
		globalSkills,
		"global-renamed",
		"shared-name",
		"global version",
	)
	writeAgentCapabilitySkill(
		t,
		globalSkills,
		"global-unique-dir",
		"global-unique",
		"global unique version",
	)
	writeAgentCapabilitySkill(
		t,
		builtinSkills,
		"builtin-unique-dir",
		"builtin-unique",
		"builtin unique version",
	)
	writeAgentCapabilitySkill(
		t,
		filepath.Join(workspace, "skills"),
		"invalid-dir",
		"invalid_name",
		"invalid version",
	)

	runtimeSkills := picoskills.NewSkillsLoader(
		workspace,
		globalSkills,
		builtinSkills,
	).ListSkills()
	expected := make([]agentCapabilitySkillCatalogItem, 0, len(runtimeSkills))
	for _, skill := range runtimeSkills {
		expected = append(expected, agentCapabilitySkillCatalogItem{
			Name:   skill.Name,
			Source: skill.Source,
		})
	}
	sort.Slice(expected, func(left, right int) bool {
		if expected[left].Name == expected[right].Name {
			return expected[left].Source < expected[right].Source
		}
		return expected[left].Name < expected[right].Name
	})

	actual, truncated := buildAgentCapabilitySkillCatalog(workspace)
	sort.Slice(actual, func(left, right int) bool {
		if actual[left].Name == actual[right].Name {
			return actual[left].Source < actual[right].Source
		}
		return actual[left].Name < actual[right].Name
	})
	if fmt.Sprint(actual) != fmt.Sprint(expected) {
		t.Fatalf("catalog = %#v, runtime ListSkills projection = %#v", actual, expected)
	}
	if !truncated {
		t.Fatal("catalog_truncated.skills = false, want true for invalid and shadowed entries")
	}
	for _, item := range actual {
		if item.Name == "workspace-renamed" || item.Name == "global-renamed" {
			t.Fatalf("catalog exposed directory identity instead of metadata identity: %#v", item)
		}
	}
	assertAgentCapabilitySkillCatalogItem(
		t,
		actual,
		"shared-name",
		"workspace",
	)
}

func TestAgentCapabilitySkillCatalogMarksUnsafeAndSkippedItemsTruncated(t *testing.T) {
	workspace, _, _ := isolatedAgentCapabilitySkillRoots(t)
	workspaceSkills := filepath.Join(workspace, "skills")
	writeAgentCapabilitySkill(
		t,
		workspaceSkills,
		"valid-dir",
		"valid-skill",
		"valid description",
	)

	missingFileDirectory := filepath.Join(workspaceSkills, "missing-file")
	if err := os.MkdirAll(missingFileDirectory, 0o755); err != nil {
		t.Fatalf("MkdirAll(missing file directory) error = %v", err)
	}
	oversizedDirectory := filepath.Join(workspaceSkills, "oversized")
	if err := os.MkdirAll(oversizedDirectory, 0o755); err != nil {
		t.Fatalf("MkdirAll(oversized directory) error = %v", err)
	}
	oversizedContent := append(
		[]byte("---\nname: oversized\ndescription: oversized skill\n---\n"),
		bytes.Repeat([]byte("x"), agentCapabilitySkillFileMaxBytes)...,
	)
	if err := os.WriteFile(
		filepath.Join(oversizedDirectory, "SKILL.md"),
		oversizedContent,
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(oversized SKILL.md) error = %v", err)
	}
	writeAgentCapabilitySkill(
		t,
		workspaceSkills,
		"missing-description",
		"missing-description",
		"",
	)
	whitespaceDirectory := filepath.Join(workspaceSkills, "whitespace-name")
	if err := os.MkdirAll(whitespaceDirectory, 0o755); err != nil {
		t.Fatalf("MkdirAll(whitespace directory) error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(whitespaceDirectory, "SKILL.md"),
		[]byte("---\nname: \" whitespace-name \"\ndescription: unsafe identity\n---\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(whitespace SKILL.md) error = %v", err)
	}

	items, truncated := buildAgentCapabilitySkillCatalog(workspace)
	if !truncated {
		t.Fatal("catalog_truncated.skills = false, want true")
	}
	if len(items) != 1 ||
		items[0].Name != "valid-skill" ||
		items[0].Source != "workspace" {
		t.Fatalf("catalog = %#v, want only valid-skill", items)
	}
}

func TestAgentCapabilitySkillCatalogDeduplicatesCaseFoldedIdentities(t *testing.T) {
	workspace, globalSkills, _ := isolatedAgentCapabilitySkillRoots(t)
	writeAgentCapabilitySkill(
		t,
		filepath.Join(workspace, "skills"),
		"workspace-case",
		"Case-Skill",
		"workspace version",
	)
	writeAgentCapabilitySkill(
		t,
		globalSkills,
		"global-case",
		"case-skill",
		"global version",
	)

	items, truncated := buildAgentCapabilitySkillCatalog(workspace)
	if !truncated {
		t.Fatal("catalog_truncated.skills = false, want collision signal")
	}
	if len(items) != 1 ||
		items[0].Name != "Case-Skill" ||
		items[0].Source != "workspace" {
		t.Fatalf("catalog = %#v, want workspace Case-Skill only", items)
	}
}

func TestAgentCapabilitySkillCatalogBoundsAggregateBytes(t *testing.T) {
	workspace, _, _ := isolatedAgentCapabilitySkillRoots(t)
	workspaceSkills := filepath.Join(workspace, "skills")
	const payloadBytes = 60 << 10
	skillCount := agentCapabilitySkillAggregateMaxBytes/payloadBytes + 2
	for index := 0; index < skillCount; index++ {
		directoryName := fmt.Sprintf("aggregate-%02d", index)
		directory := filepath.Join(workspaceSkills, directoryName)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", directoryName, err)
		}
		content := fmt.Sprintf(
			"---\nname: %s\ndescription: aggregate skill\n---\n%s",
			directoryName,
			strings.Repeat("x", payloadBytes),
		)
		if err := os.WriteFile(
			filepath.Join(directory, "SKILL.md"),
			[]byte(content),
			0o600,
		); err != nil {
			t.Fatalf("WriteFile(%s/SKILL.md) error = %v", directoryName, err)
		}
	}

	items, truncated := buildAgentCapabilitySkillCatalog(workspace)
	if !truncated {
		t.Fatal("catalog_truncated.skills = false, want aggregate bound signal")
	}
	if len(items) == 0 || len(items) >= skillCount {
		t.Fatalf("catalog len = %d, want between 1 and %d", len(items), skillCount-1)
	}
}

func TestAgentCapabilitySkillDirectoryScanIsBounded(t *testing.T) {
	root := t.TempDir()
	for index := 0; index <= agentCapabilitySkillScanLimit; index++ {
		if err := os.Mkdir(
			filepath.Join(root, fmt.Sprintf("entry-%04d", index)),
			0o755,
		); err != nil {
			t.Fatalf("Mkdir(entry %d) error = %v", index, err)
		}
	}

	scanned := 0
	directories, truncated, stop := boundedAgentCapabilitySkillDirectories(root, &scanned)
	if scanned != agentCapabilitySkillScanLimit {
		t.Fatalf("scanned entries = %d, want %d", scanned, agentCapabilitySkillScanLimit)
	}
	if len(directories) != agentCapabilitySkillScanLimit {
		t.Fatalf("directory count = %d, want %d", len(directories), agentCapabilitySkillScanLimit)
	}
	if !truncated || !stop {
		t.Fatalf("truncated=%t stop=%t, want true/true", truncated, stop)
	}
}

func TestAgentCapabilityMCPCatalogDeduplicatesNormalizedIdentities(t *testing.T) {
	workspace, _, _ := isolatedAgentCapabilitySkillRoots(t)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.Tools.MCP.Enabled = true
	cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
		"Server": {Enabled: true},
		"server": {Enabled: false},
	}

	catalogs, truncated := buildAgentCapabilityCatalogs(
		cfg,
		&cfg.Agents.List[0],
		workspace,
		"",
	)
	if !truncated.MCPServers {
		t.Fatal("catalog_truncated.mcp_servers = false, want collision signal")
	}
	if len(catalogs.MCPServers) != 1 ||
		catalogs.MCPServers[0].Name != "server" {
		t.Fatalf("MCP catalog = %#v, want one normalized server", catalogs.MCPServers)
	}
}

func TestAgentCapabilityToolCatalogUsesCapturedDefinitionModel(t *testing.T) {
	workspace, _, _ := isolatedAgentCapabilitySkillRoots(t)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.Provider = "ollama"
	cfg.Agents.Defaults.ModelName = "fallback-model"
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	agentConfig := &cfg.Agents.List[0]

	agentPath := filepath.Join(workspace, agentDefinitionFileCurrent)
	if err := os.WriteFile(
		agentPath,
		[]byte("---\nmodel: qwen3-coder\n---\nCaptured definition.\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(AGENT.md) error = %v", err)
	}
	state := loadAgentDefinitionState(cfg, agentConfig)
	if state.document.model != "qwen3-coder" {
		t.Fatalf("captured model = %q, want qwen3-coder", state.document.model)
	}

	largeDefinition := bytes.Repeat([]byte("x"), 2<<20)
	for _, name := range []string{"USER.md", "SOUL.md"} {
		if err := os.WriteFile(
			filepath.Join(workspace, name),
			largeDefinition,
			0o600,
		); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	if err := os.WriteFile(
		agentPath,
		[]byte("---\nmodel: gpt-5\n---\nChanged after capture.\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(changed AGENT.md) error = %v", err)
	}

	response := buildAgentCapabilitiesResponse(
		"main",
		cfg,
		agentConfig,
		"config-revision",
		state,
	)
	item := agentCapabilityToolCatalogItemByName(t, response.Catalogs.Tools, "exec_command")
	if item.Status != "blocked" || item.ReasonCode != "requires_codex_surface" {
		t.Fatalf("captured-model exec_command item = %#v, want blocked", item)
	}
	planItem := agentCapabilityToolCatalogItemByName(t, response.Catalogs.Tools, "update_plan")
	if planItem.Status != "blocked" || planItem.ReasonCode != "requires_durable_plan" {
		t.Fatalf("captured-model update_plan item = %#v, want durable-plan block", planItem)
	}

	freshState := loadAgentDefinitionState(cfg, agentConfig)
	freshResponse := buildAgentCapabilitiesResponse(
		"main",
		cfg,
		agentConfig,
		"config-revision",
		freshState,
	)
	freshItem := agentCapabilityToolCatalogItemByName(
		t,
		freshResponse.Catalogs.Tools,
		"exec_command",
	)
	if freshItem.Status != "enabled" {
		t.Fatalf("fresh-model exec_command item = %#v, want enabled", freshItem)
	}
	freshPlan := agentCapabilityToolCatalogItemByName(t, freshResponse.Catalogs.Tools, "update_plan")
	if freshPlan.Status != "blocked" || freshPlan.ReasonCode != "requires_durable_plan" {
		t.Fatalf("fresh-model update_plan item = %#v, want durable-plan block", freshPlan)
	}
}

func isolatedAgentCapabilitySkillRoots(t *testing.T) (string, string, string) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "workspace")
	globalHome := filepath.Join(t.TempDir(), "home")
	builtinSkills := filepath.Join(t.TempDir(), "builtin")
	for _, path := range []string{workspace, globalHome, builtinSkills} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", path, err)
		}
	}
	t.Setenv("PICOCLAW_HOME", globalHome)
	t.Setenv(config.EnvBuiltinSkills, builtinSkills)
	return workspace, filepath.Join(globalHome, "skills"), builtinSkills
}

func writeAgentCapabilitySkill(
	t *testing.T,
	root string,
	directoryName string,
	metadataName string,
	description string,
) {
	t.Helper()
	directory := filepath.Join(root, directoryName)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", directory, err)
	}
	content := fmt.Sprintf(
		"---\nname: %s\ndescription: %s\n---\n# Skill\n",
		metadataName,
		description,
	)
	if err := os.WriteFile(
		filepath.Join(directory, "SKILL.md"),
		[]byte(content),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(%s/SKILL.md) error = %v", directory, err)
	}
}

func assertAgentCapabilitySkillCatalogItem(
	t *testing.T,
	items []agentCapabilitySkillCatalogItem,
	name string,
	source string,
) {
	t.Helper()
	for _, item := range items {
		if item.Name == name && item.Source == source {
			return
		}
	}
	t.Fatalf("catalog %#v does not contain %s/%s", items, name, source)
}

func agentCapabilityToolCatalogItemByName(
	t *testing.T,
	items []agentCapabilityToolCatalogItem,
	name string,
) agentCapabilityToolCatalogItem {
	t.Helper()
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("tool catalog does not contain %q: %#v", name, items)
	return agentCapabilityToolCatalogItem{}
}

func TestResolveCapturedAgentCapabilityModelMatchesRuntimePrecedence(t *testing.T) {
	defaults := &config.AgentDefaults{ModelName: "default-model"}
	agentConfig := &config.AgentConfig{
		Model: &config.AgentModelConfig{Primary: "agent-model"},
	}
	if got := picoagent.ResolveAgentModelFromDefinition(
		agentConfig,
		defaults,
		" definition-model ",
	); got != "definition-model" {
		t.Fatalf("definition model = %q, want definition-model", got)
	}
	if got := picoagent.ResolveAgentModelFromDefinition(
		agentConfig,
		defaults,
		"",
	); got != "agent-model" {
		t.Fatalf("agent model = %q, want agent-model", got)
	}
	if got := picoagent.ResolveAgentModelFromDefinition(
		nil,
		defaults,
		"",
	); got != "default-model" {
		t.Fatalf("default model = %q, want default-model", got)
	}
}
