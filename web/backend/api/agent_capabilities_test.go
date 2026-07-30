package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	picoagent "github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
)

func newAgentCapabilitiesTestHarness(
	t *testing.T,
	configure func(*config.Config),
) *agentAPITestHarness {
	t.Helper()
	harness := newAgentAPITestHarness(t, configure)
	mux := http.NewServeMux()
	mux.HandleFunc(
		"GET /api/agents/{id}/capabilities",
		harness.handler.handleGetAgentCapabilities,
	)
	mux.HandleFunc(
		"PATCH /api/agents/{id}/capabilities",
		harness.handler.requireAgentMutationOrigin(
			harness.handler.handlePatchAgentCapabilities,
		),
	)
	harness.mux = mux
	return harness
}

func decodeAgentCapabilitiesResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) agentCapabilitiesResponse {
	t.Helper()
	if recorder.Code < 200 || recorder.Code >= 300 {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response agentCapabilitiesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return response
}

func capabilityPolicyRequest(
	mode string,
	values ...string,
) *agentCapabilityPolicyRequest {
	copyValues := make([]string, len(values))
	copy(copyValues, values)
	return &agentCapabilityPolicyRequest{Mode: mode, Values: &copyValues}
}

func TestAgentCapabilitiesGETContractAndSanitizedCatalogs(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentCapabilitiesTestHarness(t, func(cfg *config.Config) {
		cfg.Tools.ReadFile.Enabled = true
		cfg.Tools.MCP.Enabled = true
		cfg.Tools.MCP.Servers = map[string]config.MCPServerConfig{
			"private-server": {
				Enabled: true,
				Command: "/secret/bin/server",
				Args:    []string{"--token", "catalog-secret"},
				Env:     map[string]string{"SECRET": "catalog-secret"},
			},
		}
	})
	cfg, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	skillDir := filepath.Join(cfg.Agents.Defaults.Workspace, "skills", "safe-skill")
	if err = os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(skill) error = %v", err)
	}
	if err = os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: safe-skill\ndescription: catalog-secret\n---\nbody"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(skill) error = %v", err)
	}

	recorder := harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	)
	response := decodeAgentCapabilitiesResponse(t, recorder)
	if response.AgentID != "main" || response.Source != "missing" ||
		!response.Editable || response.IssueCode != "" ||
		response.LegacyUpgradeRequired {
		t.Fatalf("response identity/source = %#v", response)
	}
	if response.Capabilities.Tools.Mode != capabilityModeAll ||
		response.Capabilities.Skills.Mode != capabilityModeInherit ||
		response.Capabilities.MCPServers.Mode != capabilityModeAll {
		t.Fatalf("default capabilities = %#v", response.Capabilities)
	}
	if response.Capabilities.Tools.Values == nil ||
		response.Capabilities.Skills.Values == nil ||
		response.Capabilities.Skills.InheritedValues == nil ||
		response.Capabilities.MCPServers.Values == nil ||
		response.Catalogs.Tools == nil ||
		response.Catalogs.Skills == nil ||
		response.Catalogs.MCPServers == nil {
		t.Fatalf("response contains nil arrays: %#v", response)
	}
	if response.Revision == "" || response.ConfigRevision == "" {
		t.Fatalf("response revisions = %#v", response)
	}

	raw := recorder.Body.String()
	for _, secret := range []string{
		"catalog-secret",
		"/secret/bin/server",
		"--token",
		"config_key",
	} {
		if strings.Contains(raw, secret) {
			t.Fatalf("response leaked %q: %s", secret, raw)
		}
	}

	var generic map[string]any
	if err = json.Unmarshal(recorder.Body.Bytes(), &generic); err != nil {
		t.Fatalf("json.Unmarshal(generic) error = %v", err)
	}
	if generic["source"] != "missing" {
		t.Fatalf("serialized source = %#v", generic["source"])
	}
	capabilities := generic["capabilities"].(map[string]any)
	assertExactJSONKeys(
		t,
		capabilities["tools"].(map[string]any),
		"mode",
		"values",
	)
	assertExactJSONKeys(
		t,
		capabilities["skills"].(map[string]any),
		"mode",
		"values",
		"inherited_values",
	)
	assertExactJSONKeys(
		t,
		capabilities["mcp_servers"].(map[string]any),
		"mode",
		"values",
	)
	catalogs := generic["catalogs"].(map[string]any)
	tool := catalogs["tools"].([]any)[0].(map[string]any)
	assertExactJSONKeys(t, tool,
		"name", "description", "category", "status", "reason_code")
	skill := catalogs["skills"].([]any)[0].(map[string]any)
	assertExactJSONKeys(t, skill, "name", "source")
	mcp := catalogs["mcp_servers"].([]any)[0].(map[string]any)
	assertExactJSONKeys(t, mcp, "name", "enabled")
}

func TestAgentCapabilitySkillCatalogBoundsDirectoryAndFileWork(t *testing.T) {
	workspace := t.TempDir()
	skillsRoot := filepath.Join(workspace, "skills")
	for index := 0; index <= agentCapabilityCatalogLimit; index++ {
		directory := filepath.Join(
			skillsRoot,
			fmt.Sprintf("skill-%03d", index),
		)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("MkdirAll(skill) error = %v", err)
		}
		if err := os.WriteFile(
			filepath.Join(directory, "SKILL.md"),
			[]byte(fmt.Sprintf(
				"---\nname: skill-%03d\ndescription: Bounded metadata.\n---\n",
				index,
			)),
			0o600,
		); err != nil {
			t.Fatalf("WriteFile(SKILL.md) error = %v", err)
		}
	}

	items, truncated := buildAgentCapabilitySkillCatalog(workspace)
	if !truncated || len(items) != agentCapabilityCatalogLimit {
		t.Fatalf("catalog len=%d truncated=%t", len(items), truncated)
	}
	for _, item := range items {
		if item.Source != "workspace" {
			t.Fatalf("catalog exposed unexpected source: %#v", item)
		}
	}

	oversized := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(
		oversized,
		bytes.Repeat([]byte("x"), agentCapabilitySkillFileMaxBytes+1),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(oversized SKILL.md) error = %v", err)
	}
	if safeAgentCapabilitySkillFile(oversized) {
		t.Fatal("oversized skill metadata file was accepted")
	}
}

func TestAgentCapabilityToolCatalogCoversRuntimeAllowlistIdentities(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Agents.Defaults.ModelName = "gpt-5"
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "worker"},
	}
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.EditFile.Enabled = true
	cfg.Tools.Exec.Enabled = true
	cfg.Tools.Exec.AllowRemote = true
	cfg.Tools.LoadImage.Enabled = true
	cfg.Tools.Spawn.Enabled = true
	cfg.Tools.Subagent.Enabled = true

	items := buildAgentCapabilityToolSupport(cfg, &cfg.Agents.List[0])
	byName := make(map[string]toolSupportItem, len(items))
	for _, item := range items {
		if _, duplicate := byName[item.Name]; duplicate {
			t.Fatalf("duplicate tool catalog identity %q", item.Name)
		}
		byName[item.Name] = item
	}
	for _, name := range []string{
		"read_file",
		"write_file",
		"list_dir",
		"edit_file",
		"append_file",
		"exec",
		"cron",
		"web_search",
		"web_fetch",
		"message",
		"reaction",
		"send_file",
		"send_tts",
		"load_image",
		"find_skills",
		"install_skill",
		"spawn",
		"subagent",
		"spawn_status",
		"delegate",
		"threads",
		"workflow",
		"git_workspace",
		"i2c",
		"spi",
		"serial",
		"tool_search_tool_regex",
		"tool_search_tool_bm25",
		"apply_patch",
		"exec_command",
		"write_stdin",
		"view_image",
		"update_plan",
	} {
		if _, exists := byName[name]; !exists {
			t.Fatalf("runtime allowlist identity %q is missing from catalog", name)
		}
	}
	for _, name := range []string{
		"reaction",
		"load_image",
		"subagent",
		"delegate",
		"apply_patch",
		"exec_command",
		"write_stdin",
		"view_image",
		"update_plan",
	} {
		if byName[name].Status != "enabled" {
			t.Fatalf("catalog status for %q = %#v, want enabled", name, byName[name])
		}
	}

	runtimeAgent := picoagent.NewAgentInstance(
		&cfg.Agents.List[0],
		&cfg.Agents.Defaults,
		cfg,
		nil,
	)
	for _, name := range []string{
		"apply_patch",
		"exec_command",
		"write_stdin",
		"update_plan",
	} {
		if !runtimeAgent.Tools.HasRegistered(name) {
			t.Fatalf(
				"catalog-enabled runtime identity %q was not registered; tools=%v",
				name,
				runtimeAgent.Tools.List(),
			)
		}
	}
}

func assertExactJSONKeys(t *testing.T, object map[string]any, expected ...string) {
	t.Helper()
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	if len(keys) != len(expected) {
		t.Fatalf("JSON keys = %v, want exactly %v", keys, expected)
	}
	for _, key := range expected {
		if _, ok := object[key]; !ok {
			t.Fatalf("JSON keys = %v, missing %q", keys, key)
		}
	}
}

func TestAgentCapabilitiesPATCHPreservesDocumentAndNoOpBytes(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentCapabilitiesTestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{{
			ID:      "main",
			Default: true,
			Skills:  []string{"inherited-skill"},
		}}
	})
	cfg, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	workspace := cfg.Agents.Defaults.Workspace
	if err = os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	path := filepath.Join(workspace, agentDefinitionFileCurrent)
	body := "# Body\r\nDo not touch this body.\r\n"
	initial := "---\r\n" +
		"name: Keeper\r\n" +
		"x-extra: stay # preserve-comment\r\n" +
		"tools:\r\n" +
		"  - Read_File # keep-read-comment\r\n" +
		"  - 'custom/tool' # keep-custom-comment\r\n" +
		"  - shell # keep-removed-comment\r\n" +
		"skills: null\r\n" +
		"mcpServers: []\r\n" +
		"model: old-model\r\n" +
		"---\r\n" + body
	if err = os.WriteFile(path, []byte(initial), 0o640); err != nil {
		t.Fatalf("WriteFile(AGENT.md) error = %v", err)
	}

	get := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))
	if get.Source != "agent" || !get.Editable ||
		get.Capabilities.Tools.Mode != capabilityModeSelected ||
		!reflect.DeepEqual(
			get.Capabilities.Tools.Values,
			[]string{"read_file", "custom/tool", "shell"},
		) ||
		get.Capabilities.Skills.Mode != capabilityModeInherit ||
		!reflect.DeepEqual(
			get.Capabilities.Skills.InheritedValues,
			[]string{"inherited-skill"},
		) ||
		get.Capabilities.MCPServers.Mode != capabilityModeNone {
		t.Fatalf("parsed capabilities = %#v", get)
	}

	patch := agentCapabilitiesPatchRequest{
		ExpectedRevision: get.Revision,
		Tools: capabilityPolicyRequest(
			capabilityModeSelected,
			"read_file",
			"custom/tool",
			"exec",
		),
		Skills:     capabilityPolicyRequest(capabilityModeNone),
		MCPServers: capabilityPolicyRequest(capabilityModeAll),
	}
	updateRecorder := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		patch,
	)
	if updateRecorder.Code != http.StatusOK {
		current, readErr := os.ReadFile(path)
		t.Fatalf(
			"PATCH status=%d body=%s current=%q readErr=%v",
			updateRecorder.Code,
			updateRecorder.Body.String(),
			current,
			readErr,
		)
	}
	updated := decodeAgentCapabilitiesResponse(t, updateRecorder)
	if updated.ConfigRevision != get.ConfigRevision ||
		updated.Revision == get.Revision ||
		updated.Source != "agent" ||
		updated.Capabilities.Skills.Mode != capabilityModeNone ||
		updated.Capabilities.MCPServers.Mode != capabilityModeAll {
		t.Fatalf("updated response = %#v", updated)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(updated) error = %v", err)
	}
	for _, preserved := range []string{
		"name: Keeper",
		"x-extra: stay # preserve-comment",
		"model: old-model",
		"Read_File # keep-read-comment",
		"'custom/tool' # keep-custom-comment",
		"# keep-removed-comment",
	} {
		if !bytes.Contains(after, []byte(preserved)) {
			t.Fatalf("updated AGENT.md lost %q:\n%s", preserved, after)
		}
	}
	if !bytes.HasSuffix(after, []byte(body)) {
		t.Fatalf("body changed:\n%s", after)
	}
	if bytes.Contains(after, []byte("mcpServers:")) {
		t.Fatalf("all policy should remove mcpServers:\n%s", after)
	}
	infoBeforeNoOp, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(before no-op) error = %v", err)
	}

	noOp := patch
	noOp.ExpectedRevision = updated.Revision
	noOpResponse := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		noOp,
	))
	afterNoOp, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(no-op) error = %v", err)
	}
	infoAfterNoOp, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(after no-op) error = %v", err)
	}
	if noOpResponse.Revision != updated.Revision ||
		!bytes.Equal(afterNoOp, after) ||
		!os.SameFile(infoBeforeNoOp, infoAfterNoOp) ||
		!infoBeforeNoOp.ModTime().Equal(infoAfterNoOp.ModTime()) {
		t.Fatalf("no-op rewrote AGENT.md")
	}
}

func TestAgentCapabilitiesPATCHPreservesEmptyFrontmatterComments(t *testing.T) {
	resetGatewayTestState(t)
	for name, initial := range map[string]string{
		"explicit null": "---\n# keep-null-head\nnull # keep-null-line\n---\nbody\n",
		"comment only":  "---\n# keep-comment-only\n---\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			harness := newAgentCapabilitiesTestHarness(t, nil)
			cfg, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
			if err != nil {
				t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
			}
			workspace := cfg.Agents.Defaults.Workspace
			if err = os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatalf("MkdirAll(workspace) error = %v", err)
			}
			path := filepath.Join(workspace, agentDefinitionFileCurrent)
			if err = os.WriteFile(path, []byte(initial), 0o640); err != nil {
				t.Fatalf("WriteFile(AGENT.md) error = %v", err)
			}

			before := decodeAgentCapabilitiesResponse(t, harness.request(
				t,
				http.MethodGet,
				"/api/agents/main/capabilities",
				nil,
			))
			after := decodeAgentCapabilitiesResponse(t, harness.request(
				t,
				http.MethodPatch,
				"/api/agents/main/capabilities",
				agentCapabilitiesPatchRequest{
					ExpectedRevision: before.Revision,
					Tools:            capabilityPolicyRequest(capabilityModeNone),
				},
			))
			if after.Capabilities.Tools.Mode != capabilityModeNone {
				t.Fatalf("updated response = %#v", after)
			}
			current, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("ReadFile(AGENT.md) error = %v", readErr)
			}
			for _, comment := range []string{
				"keep-null-head",
				"keep-null-line",
				"keep-comment-only",
			} {
				if strings.Contains(initial, comment) &&
					!bytes.Contains(current, []byte(comment)) {
					t.Fatalf("updated AGENT.md lost %q:\n%s", comment, current)
				}
			}
			if !bytes.HasSuffix(current, []byte("body\n")) {
				t.Fatalf("body changed:\n%s", current)
			}
		})
	}
}

func TestAgentCapabilitiesLegacyUpgradeLeavesLegacyIntact(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentCapabilitiesTestHarness(t, nil)
	cfg, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	workspace := cfg.Agents.Defaults.Workspace
	if err = os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	legacyPath := filepath.Join(workspace, agentDefinitionFileLegacy)
	legacy := []byte("# Legacy agent\n\nKeep this prompt exactly.\n")
	if err = os.WriteFile(legacyPath, legacy, 0o640); err != nil {
		t.Fatalf("WriteFile(AGENTS.md) error = %v", err)
	}

	get := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))
	if get.Source != "legacy" || get.Editable ||
		!get.LegacyUpgradeRequired || get.IssueCode != "" {
		t.Fatalf("legacy response = %#v", get)
	}
	blocked := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: get.Revision,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if blocked.Code != http.StatusConflict ||
		!strings.Contains(blocked.Body.String(), "legacy_upgrade_required") {
		t.Fatalf("blocked status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	upgraded := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: get.Revision,
			UpgradeLegacy:    true,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	))
	if upgraded.Source != "agent" || !upgraded.Editable ||
		upgraded.LegacyUpgradeRequired ||
		upgraded.Capabilities.Tools.Mode != capabilityModeNone {
		t.Fatalf("upgrade response = %#v", upgraded)
	}
	current, err := os.ReadFile(filepath.Join(workspace, agentDefinitionFileCurrent))
	if err != nil {
		t.Fatalf("ReadFile(AGENT.md) error = %v", err)
	}
	if !bytes.HasSuffix(current, legacy) {
		t.Fatalf("upgraded body does not retain legacy bytes:\n%s", current)
	}
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil || !bytes.Equal(legacyAfter, legacy) {
		t.Fatalf("legacy changed: data=%q err=%v", legacyAfter, err)
	}
}

func TestAgentCapabilitiesLegacyUpgradeOnlyKeepsFrontmatterShapedLegacyAsBody(
	t *testing.T,
) {
	resetGatewayTestState(t)
	for name, legacy := range map[string][]byte{
		"valid-looking": []byte(
			"---\nmodel: must-not-activate\ntools: [exec]\n---\nlegacy body\n",
		),
		"malformed-looking": []byte(
			"---\nmodel: [\n---\nlegacy malformed body\n",
		),
	} {
		t.Run(name, func(t *testing.T) {
			harness := newAgentCapabilitiesTestHarness(t, nil)
			cfg, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
			if err != nil {
				t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
			}
			workspace := cfg.Agents.Defaults.Workspace
			if err = os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatalf("MkdirAll(workspace) error = %v", err)
			}
			legacyPath := filepath.Join(workspace, agentDefinitionFileLegacy)
			if err = os.WriteFile(legacyPath, legacy, 0o640); err != nil {
				t.Fatalf("WriteFile(AGENTS.md) error = %v", err)
			}

			before := decodeAgentCapabilitiesResponse(t, harness.request(
				t,
				http.MethodGet,
				"/api/agents/main/capabilities",
				nil,
			))
			after := decodeAgentCapabilitiesResponse(t, harness.request(
				t,
				http.MethodPatch,
				"/api/agents/main/capabilities",
				agentCapabilitiesPatchRequest{
					ExpectedRevision: before.Revision,
					UpgradeLegacy:    true,
				},
			))
			if after.Source != "agent" || !after.Editable ||
				after.IssueCode != "" ||
				after.Capabilities.Tools.Mode != capabilityModeAll {
				t.Fatalf("upgraded response = %#v", after)
			}
			current, readErr := os.ReadFile(
				filepath.Join(workspace, agentDefinitionFileCurrent),
			)
			if readErr != nil {
				t.Fatalf("ReadFile(AGENT.md) error = %v", readErr)
			}
			frontmatter, _, end, ok := exactAgentFrontmatter(current)
			if !ok || bytes.Contains(frontmatter, []byte("must-not-activate")) {
				t.Fatalf("unsafe generated frontmatter %q in:\n%s", frontmatter, current)
			}
			_, bodyStart, lineOK := exactLine(current, end)
			if !lineOK || !bytes.Equal(current[bodyStart:], legacy) {
				t.Fatalf("legacy bytes were not preserved as body:\n%s", current)
			}
			legacyAfter, readErr := os.ReadFile(legacyPath)
			if readErr != nil || !bytes.Equal(legacyAfter, legacy) {
				t.Fatalf(
					"legacy changed: data=%q err=%v",
					legacyAfter,
					readErr,
				)
			}
		})
	}
}

func TestAgentCapabilitiesCompositeCASFencesConfigAndFile(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentCapabilitiesTestHarness(t, nil)
	cfg, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	path := filepath.Join(cfg.Agents.Defaults.Workspace, agentDefinitionFileCurrent)
	initial := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))

	originalHook := agentCapabilitiesBeforeFinalFence
	t.Cleanup(func() {
		agentCapabilitiesBeforeFinalFence = originalHook
	})
	agentCapabilitiesBeforeFinalFence = func() {
		agentCapabilitiesBeforeFinalFence = func() {}
		concurrent, revision, loadErr := config.LoadConfigForUpdateSnapshot(
			harness.configPath,
		)
		if loadErr != nil {
			t.Fatalf("LoadConfigForUpdateSnapshot() error = %v", loadErr)
		}
		concurrent.Agents.Defaults.MaxTokens++
		if _, saveErr := config.SaveConfigIfRevision(
			harness.configPath,
			concurrent,
			revision,
		); saveErr != nil {
			t.Fatalf("SaveConfigIfRevision() error = %v", saveErr)
		}
	}
	configRace := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: initial.Revision,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if configRace.Code != http.StatusConflict ||
		!strings.Contains(configRace.Body.String(), "capabilities_revision_mismatch") {
		t.Fatalf("config race status=%d body=%s", configRace.Code, configRace.Body.String())
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config race created AGENT.md: %v", err)
	}

	current := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))
	external := []byte("---\ntools: [external_tool]\n---\nexternal body\n")
	agentCapabilitiesBeforeFinalFence = func() {
		agentCapabilitiesBeforeFinalFence = func() {}
		if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
			t.Fatalf("MkdirAll(external) error = %v", mkdirErr)
		}
		if writeErr := os.WriteFile(path, external, 0o600); writeErr != nil {
			t.Fatalf("WriteFile(external) error = %v", writeErr)
		}
	}
	fileRace := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: current.Revision,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if fileRace.Code != http.StatusConflict ||
		!strings.Contains(fileRace.Body.String(), "capabilities_revision_mismatch") {
		t.Fatalf("file race status=%d body=%s", fileRace.Code, fileRace.Body.String())
	}
	preserved, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(preserved, external) {
		t.Fatalf("file race overwrote external data: data=%q err=%v", preserved, err)
	}
}

func TestAgentCapabilitiesWriterFenceRejectsLastMomentPromptEdit(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentCapabilitiesTestHarness(t, nil)
	cfg, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	workspace := cfg.Agents.Defaults.Workspace
	if err = os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	path := filepath.Join(workspace, agentDefinitionFileCurrent)
	initial := []byte("---\ntools: [exec]\n---\ninitial prompt\n")
	if err = os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatalf("WriteFile(initial) error = %v", err)
	}
	before := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))

	conditionalWrite := writeAgentCapabilitiesFile
	t.Cleanup(func() {
		writeAgentCapabilitiesFile = conditionalWrite
	})
	external := []byte("---\ntools: [read_file]\n---\nexternal prompt edit\n")
	writeAgentCapabilitiesFile = func(
		target string,
		data []byte,
		permission fs.FileMode,
		expected agentDefinitionFile,
		expectedExists bool,
	) (agentCapabilitiesWriteResult, error) {
		writeAgentCapabilitiesFile = conditionalWrite
		if writeErr := os.WriteFile(target, external, 0o600); writeErr != nil {
			t.Fatalf("WriteFile(external) error = %v", writeErr)
		}
		return conditionalWrite(
			target,
			data,
			permission,
			expected,
			expectedExists,
		)
	}

	recorder := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: before.Revision,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(
			recorder.Body.String(),
			"capabilities_revision_mismatch",
		) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	current, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(current, external) {
		t.Fatalf("external edit was overwritten: data=%q err=%v", current, readErr)
	}
}

func TestAgentCapabilitiesRevisionFencesPermissionChanges(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentCapabilitiesTestHarness(t, nil)
	cfg, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	workspace := cfg.Agents.Defaults.Workspace
	if err = os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	path := filepath.Join(workspace, agentDefinitionFileCurrent)
	initial := []byte("---\ntools: [exec]\n---\nprompt\n")
	if err = os.WriteFile(path, initial, 0o644); err != nil {
		t.Fatalf("WriteFile(initial) error = %v", err)
	}
	before := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	recorder := harness.request(
		t,
		http.MethodPatch,
		"/api/agents/main/capabilities",
		agentCapabilitiesPatchRequest{
			ExpectedRevision: before.Revision,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
	)
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(
			recorder.Body.String(),
			"capabilities_revision_mismatch",
		) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("Stat() error = %v", statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v, want 0600", info.Mode().Perm())
	}
	current, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(current, initial) {
		t.Fatalf("chmod race changed data: data=%q err=%v", current, readErr)
	}
}

func TestAgentCapabilitiesStrictModesOpaqueRevisionAndAgentBoundRevision(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentCapabilitiesTestHarness(t, func(cfg *config.Config) {
		root := filepath.Dir(cfg.Agents.Defaults.Workspace)
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true, Workspace: filepath.Join(root, "one")},
			{ID: "worker", Workspace: filepath.Join(root, "two")},
		}
	})
	main := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	))
	worker := decodeAgentCapabilitiesResponse(t, harness.request(
		t,
		http.MethodGet,
		"/api/agents/worker/capabilities",
		nil,
	))
	if main.Revision == worker.Revision {
		t.Fatalf("agent-bound revisions collided: %q", main.Revision)
	}

	for name, request := range map[string]agentCapabilitiesPatchRequest{
		"whitespace revision": {
			ExpectedRevision: " " + main.Revision,
			Tools:            capabilityPolicyRequest(capabilityModeNone),
		},
		"trimmed mode": {
			ExpectedRevision: main.Revision,
			Tools:            capabilityPolicyRequest(" none"),
		},
		"runtime-normalized request value": {
			ExpectedRevision: main.Revision,
			Tools: capabilityPolicyRequest(
				capabilityModeSelected,
				"Read_File",
			),
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := harness.request(
				t,
				http.MethodPatch,
				"/api/agents/main/capabilities",
				request,
			)
			if recorder.Code != http.StatusConflict &&
				recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestRequestedCapabilityValuesMatchSafeReadBounds(t *testing.T) {
	values := make([]string, 0, agentCapabilityValuesLimit)
	for index := 0; index < agentCapabilityValuesLimit; index++ {
		values = append(values, fmt.Sprintf("tool-%04d", index))
	}
	values[0] = strings.Repeat("a", agentCapabilityValueMaxBytes)
	if _, err := validateRequestedCapabilityValues(values, true); err != nil {
		t.Fatalf("safe retained values were rejected: %v", err)
	}
	if _, err := validateRequestedCapabilityValues(
		append(values, "one-too-many"),
		true,
	); err == nil {
		t.Fatal("oversized value collection was accepted")
	}
	if _, err := validateRequestedCapabilityValues(
		[]string{strings.Repeat("a", agentCapabilityValueMaxBytes+1)},
		true,
	); err == nil {
		t.Fatal("oversized capability value was accepted")
	}
}

func TestAgentCapabilitiesUnsafeCurrentFileDoesNotFallBackToLegacy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows")
	}
	resetGatewayTestState(t)
	harness := newAgentCapabilitiesTestHarness(t, nil)
	cfg, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	workspace := cfg.Agents.Defaults.Workspace
	if err = os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("MkdirAll(workspace) error = %v", err)
	}
	secretPath := filepath.Join(filepath.Dir(workspace), "secret.md")
	if err = os.WriteFile(secretPath, []byte("must-not-leak"), 0o600); err != nil {
		t.Fatalf("WriteFile(secret) error = %v", err)
	}
	if err = os.Symlink(secretPath, filepath.Join(workspace, agentDefinitionFileCurrent)); err != nil {
		t.Skipf("Symlink() unavailable: %v", err)
	}
	if err = os.WriteFile(
		filepath.Join(workspace, agentDefinitionFileLegacy),
		[]byte("legacy fallback"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(legacy) error = %v", err)
	}

	recorder := harness.request(
		t,
		http.MethodGet,
		"/api/agents/main/capabilities",
		nil,
	)
	response := decodeAgentCapabilitiesResponse(t, recorder)
	if response.Source != "agent" || response.Editable ||
		response.IssueCode != "agent_definition_not_regular" ||
		response.LegacyUpgradeRequired {
		t.Fatalf("unsafe response = %#v", response)
	}
	if strings.Contains(recorder.Body.String(), "must-not-leak") ||
		strings.Contains(recorder.Body.String(), "legacy fallback") {
		t.Fatalf("unsafe response leaked file content: %s", recorder.Body.String())
	}
}

func TestAgentCapabilitiesRejectsMalformedAndOversizeDefinitions(t *testing.T) {
	resetGatewayTestState(t)
	for name, testCase := range map[string]struct {
		data  []byte
		issue string
	}{
		"malformed": {
			data:  []byte("---\ntools: [\n---\nbody"),
			issue: "agent_definition_invalid",
		},
		"unterminated": {
			data: []byte(
				"---\ntools: []\nmcpServers: []\n# no closing delimiter",
			),
			issue: "agent_definition_invalid",
		},
		"opening delimiter only": {
			data:  []byte("---"),
			issue: "agent_definition_invalid",
		},
		"merge alias": {
			data: []byte(
				"---\nbase: &base\n  tools: [exec]\n<<: *base\n---\nbody",
			),
			issue: "agent_definition_invalid",
		},
		"oversize": {
			data:  bytes.Repeat([]byte("x"), agentDefinitionMaxBytes+1),
			issue: "agent_definition_too_large",
		},
	} {
		t.Run(name, func(t *testing.T) {
			harness := newAgentCapabilitiesTestHarness(t, nil)
			cfg, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
			if err != nil {
				t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
			}
			if err = os.MkdirAll(cfg.Agents.Defaults.Workspace, 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			if err = os.WriteFile(
				filepath.Join(
					cfg.Agents.Defaults.Workspace,
					agentDefinitionFileCurrent,
				),
				testCase.data,
				0o600,
			); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			response := decodeAgentCapabilitiesResponse(t, harness.request(
				t,
				http.MethodGet,
				"/api/agents/main/capabilities",
				nil,
			))
			if response.Source != "agent" || response.Editable ||
				response.IssueCode != testCase.issue {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestGatewayRuntimeSignatureTracksOnlySemanticAgentFrontmatter(t *testing.T) {
	cfg := config.DefaultConfig()
	workspace := t.TempDir()
	cfg.Agents.Defaults.Workspace = workspace
	path := filepath.Join(workspace, agentDefinitionFileCurrent)
	configSignature := computeConfigSignature(cfg)

	write := func(content string) string {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(AGENT.md) error = %v", err)
		}
		if got := computeConfigSignature(cfg); got != configSignature {
			t.Fatalf("filesystem changed pure config signature: got %q want %q", got, configSignature)
		}
		return computeGatewayRuntimeSignature(cfg)
	}

	first := write(`---
name: First name
description: ignored
x-extra: one
model: model-one
tools: [exec, read_file]
skills: [beta, alpha]
mcpServers: [github, linear]
---
# First body
`)
	presentationOnly := write(`---
# a different comment
name: First name
description: ignored
x-extra: two
model: model-one
tools: [read_file, exec]
skills: [beta, alpha]
mcpServers: [linear, github]
---
# Completely different body
`)
	if presentationOnly != first {
		t.Fatalf("presentation-only change affected runtime signature:\n%s\n%s", first, presentationOnly)
	}
	identityChanged := write(`---
name: Completely different
description: changed identity
x-extra: two
model: model-one
tools: [read_file, exec]
skills: [beta, alpha]
mcpServers: [linear, github]
---
# Same body
`)
	if identityChanged == presentationOnly {
		t.Fatal("agent identity change did not affect runtime signature")
	}
	modelChanged := write(`---
model: model-two
tools: [exec, read_file]
skills: [beta, alpha]
mcpServers: [github, linear]
---
body
`)
	if modelChanged == identityChanged {
		t.Fatal("model change did not affect runtime signature")
	}
	skillOrderChanged := write(`---
model: model-two
tools: [exec, read_file]
skills: [alpha, beta]
mcpServers: [github, linear]
---
body
`)
	if skillOrderChanged == modelChanged {
		t.Fatal("skill order change did not affect runtime signature")
	}
	malformed := write("---\ntools: [\n---\nbody")
	if malformed == skillOrderChanged {
		t.Fatal("malformed state did not affect runtime signature")
	}
	otherMalformed := write("---\nmodel: {\n---\ndifferent body")
	if otherMalformed != malformed {
		t.Fatal("equivalent runtime-rejected frontmatter changed runtime signature")
	}
	decodeFailure := write("---\ntools: exec\n---\ndifferent body")
	if decodeFailure != malformed {
		t.Fatal("runtime decode failure did not use the stable malformed state")
	}
	unterminated := write("---\ntools: [write_file]\n# Tasks\n- ignored")
	if unterminated != malformed {
		t.Fatal("unterminated frontmatter did not use the stable malformed state")
	}
	if openingOnly := write("---"); openingOnly != malformed {
		t.Fatal("opening-only frontmatter did not use the stable malformed state")
	}
	unsafeMerge := write(`---
base: &base
  tools: [exec]
<<: *base
---
body
`)
	changedUnsafeMerge := write(`---
base: &base
  tools: [write_file]
<<: *base
---
body
`)
	if changedUnsafeMerge == unsafeMerge {
		t.Fatal("normalization-unsafe merged capability change was ignored")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove(AGENT.md) error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(workspace, agentDefinitionFileLegacy),
		[]byte("legacy body is not structured runtime state"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(AGENTS.md) error = %v", err)
	}
	if got := computeGatewayRuntimeSignature(cfg); got != configSignature {
		t.Fatalf("legacy prose affected runtime signature: got %q want %q", got, configSignature)
	}
}

func TestGatewayRuntimeSignatureBoundsAndDeduplicatesAgentDefinitions(t *testing.T) {
	t.Run("shared workspace", func(t *testing.T) {
		cfg := config.DefaultConfig()
		workspace := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(workspace, agentDefinitionFileCurrent),
			[]byte("---\nmodel: shared-model\n---\nbody\n"),
			0o600,
		); err != nil {
			t.Fatalf("WriteFile(shared AGENT.md) error = %v", err)
		}
		cfg.Agents.List = make(
			[]config.AgentConfig,
			agentDefinitionSignatureWorkspaceLimit+1,
		)
		for index := range cfg.Agents.List {
			cfg.Agents.List[index] = config.AgentConfig{
				ID:        fmt.Sprintf("agent-%03d", index),
				Workspace: workspace,
			}
		}
		signature := computeAgentDefinitionsRuntimeSignature(cfg)
		if signature == gatewayUnknownBootConfigSignature || signature == "" {
			t.Fatalf("shared definitions were not deduplicated: %q", signature)
		}
	})

	t.Run("unique workspace limit", func(t *testing.T) {
		cfg := config.DefaultConfig()
		root := t.TempDir()
		cfg.Agents.List = make(
			[]config.AgentConfig,
			agentDefinitionSignatureWorkspaceLimit+1,
		)
		for index := range cfg.Agents.List {
			cfg.Agents.List[index] = config.AgentConfig{
				ID:        fmt.Sprintf("agent-%03d", index),
				Workspace: filepath.Join(root, fmt.Sprintf("workspace-%03d", index)),
			}
		}
		if signature := computeAgentDefinitionsRuntimeSignature(cfg); signature != gatewayUnknownBootConfigSignature {
			t.Fatalf("unique-workspace signature = %q, want unknown", signature)
		}
	})

	t.Run("agent limit", func(t *testing.T) {
		cfg := config.DefaultConfig()
		cfg.Agents.List = make(
			[]config.AgentConfig,
			agentDefinitionSignatureAgentLimit+1,
		)
		if signature := computeAgentDefinitionsRuntimeSignature(cfg); signature != gatewayUnknownBootConfigSignature {
			t.Fatalf("oversized agent signature = %q, want unknown", signature)
		}
	})

	t.Run("aggregate byte limit", func(t *testing.T) {
		cfg := config.DefaultConfig()
		root := t.TempDir()
		count := agentDefinitionSignatureByteLimit/agentDefinitionMaxBytes + 1
		cfg.Agents.List = make([]config.AgentConfig, count)
		for index := range cfg.Agents.List {
			workspace := filepath.Join(root, fmt.Sprintf("workspace-%03d", index))
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatalf("MkdirAll(workspace) error = %v", err)
			}
			content := append(
				[]byte("---\nmodel: bounded\n---\n"),
				bytes.Repeat(
					[]byte("x"),
					agentDefinitionMaxBytes-len("---\nmodel: bounded\n---\n"),
				)...,
			)
			if err := os.WriteFile(
				filepath.Join(workspace, agentDefinitionFileCurrent),
				content,
				0o600,
			); err != nil {
				t.Fatalf("WriteFile(AGENT.md) error = %v", err)
			}
			cfg.Agents.List[index] = config.AgentConfig{
				ID:        fmt.Sprintf("agent-%03d", index),
				Workspace: workspace,
			}
		}
		if signature := computeAgentDefinitionsRuntimeSignature(cfg); signature != gatewayUnknownBootConfigSignature {
			t.Fatalf("aggregate-byte signature = %q, want unknown", signature)
		}
	})
}
