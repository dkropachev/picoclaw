package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestPromptRegistry_RejectsRegisteredSourceWrongPlacement(t *testing.T) {
	registry := NewPromptRegistry()
	if err := registry.RegisterSource(PromptSourceDescriptor{
		ID:      "test:source",
		Owner:   "test",
		Allowed: []PromptPlacement{{Layer: PromptLayerCapability, Slot: PromptSlotTooling}},
	}); err != nil {
		t.Fatalf("RegisterSource() error = %v", err)
	}

	err := registry.ValidatePart(PromptPart{
		ID:      "wrong.placement",
		Layer:   PromptLayerContext,
		Slot:    PromptSlotRuntime,
		Source:  PromptSource{ID: "test:source"},
		Content: "runtime text",
	})
	if err == nil {
		t.Fatal("ValidatePart() error = nil, want placement error")
	}
}

func TestPromptRegistry_AllowsUnregisteredSourceInCompatibilityMode(t *testing.T) {
	registry := NewPromptRegistry()

	err := registry.ValidatePart(PromptPart{
		ID:      "unregistered.part",
		Layer:   PromptLayerCapability,
		Slot:    PromptSlotMCP,
		Source:  PromptSource{ID: "mcp:dynamic-server"},
		Content: "dynamic MCP prompt",
	})
	if err != nil {
		t.Fatalf("ValidatePart() error = %v, want nil for unregistered source", err)
	}
}

func TestRenderPromptPartsLegacy_UsesLayerAndSlotOrder(t *testing.T) {
	parts := []PromptPart{
		{
			ID:      "context.runtime",
			Layer:   PromptLayerContext,
			Slot:    PromptSlotRuntime,
			Source:  PromptSource{ID: PromptSourceRuntime},
			Content: "runtime",
		},
		{
			ID:      "kernel.identity",
			Layer:   PromptLayerKernel,
			Slot:    PromptSlotIdentity,
			Source:  PromptSource{ID: PromptSourceKernel},
			Content: "kernel",
		},
		{
			ID:      "capability.skill",
			Layer:   PromptLayerCapability,
			Slot:    PromptSlotActiveSkill,
			Source:  PromptSource{ID: "skill:test"},
			Content: "skill",
		},
		{
			ID:      "instruction.workspace",
			Layer:   PromptLayerInstruction,
			Slot:    PromptSlotWorkspace,
			Source:  PromptSource{ID: PromptSourceWorkspace},
			Content: "workspace",
		},
	}

	got := renderPromptPartsLegacy(parts)
	want := strings.Join([]string{"kernel", "workspace", "skill", "runtime"}, "\n\n---\n\n")
	if got != want {
		t.Fatalf("renderPromptPartsLegacy() = %q, want %q", got, want)
	}
}

func TestBuildMessagesFromPrompt_IncludesSystemPromptOverlay(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cb := NewContextBuilder(t.TempDir())

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "do child task",
		Overlays: promptOverlaysForOptions(processOptions{
			SystemPromptOverride: "Use child-only system instructions.",
		}),
	})

	if len(messages) < 2 {
		t.Fatalf("messages len = %d, want at least 2", len(messages))
	}
	if messages[0].Role != "system" {
		t.Fatalf("messages[0].Role = %q, want system", messages[0].Role)
	}
	if !strings.Contains(messages[0].Content, "Use child-only system instructions.") {
		t.Fatalf("system prompt missing overlay: %q", messages[0].Content)
	}
	if messages[1].Role != "user" || messages[1].Content != "do child task" {
		t.Fatalf("messages[1] = %#v, want user task", messages[1])
	}
}

func TestBuildMessagesFromPrompt_AttachesInternalPromptMetadata(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cb := NewContextBuilder(t.TempDir())

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "hello",
		Summary:        "prior context",
	})
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(messages))
	}

	system := messages[0]
	if len(system.SystemParts) < 3 {
		t.Fatalf("system parts len = %d, want at least 3", len(system.SystemParts))
	}
	if system.SystemParts[0].PromptLayer != string(PromptLayerKernel) ||
		system.SystemParts[0].PromptSlot != string(PromptSlotIdentity) ||
		system.SystemParts[0].PromptSource != string(PromptSourceKernel) {
		t.Fatalf("static system metadata = %#v, want kernel identity", system.SystemParts[0])
	}

	var hasRuntime, hasSummary bool
	for _, part := range system.SystemParts {
		switch part.PromptSource {
		case string(PromptSourceRuntime):
			hasRuntime = true
			if part.CacheControl != nil {
				t.Fatalf("runtime cache control = %#v, want nil", part.CacheControl)
			}
		case string(PromptSourceSummary):
			hasSummary = true
			if part.CacheControl != nil {
				t.Fatalf("summary cache control = %#v, want nil", part.CacheControl)
			}
		}
	}
	if !hasRuntime {
		t.Fatal("system parts missing runtime prompt metadata")
	}
	if !hasSummary {
		t.Fatal("system parts missing summary prompt metadata")
	}

	user := messages[1]
	if user.PromptLayer != string(PromptLayerTurn) ||
		user.PromptSlot != string(PromptSlotMessage) ||
		user.PromptSource != string(PromptSourceUserMessage) {
		t.Fatalf("user message metadata = %#v, want turn message", user)
	}

	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(data), "PromptSource") ||
		strings.Contains(string(data), "PromptLayer") ||
		strings.Contains(string(data), "PromptSlot") {
		t.Fatalf("internal prompt metadata leaked into JSON: %s", data)
	}
}

func TestContextBuilder_CollectsToolDiscoveryContributor(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cb := NewContextBuilder(t.TempDir()).WithToolDiscovery(true, false)

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	system := messages[0]
	if !strings.Contains(system.Content, "tool_search_tool_bm25") {
		t.Fatalf("system prompt missing tool discovery rule: %q", system.Content)
	}

	var found bool
	for _, part := range system.SystemParts {
		if part.PromptSource == string(PromptSourceToolDiscovery) {
			found = true
			if part.PromptLayer != string(PromptLayerCapability) || part.PromptSlot != string(PromptSlotTooling) {
				t.Fatalf("tool discovery metadata = %#v, want capability/tooling", part)
			}
			if part.CacheControl == nil || part.CacheControl.Type != "ephemeral" {
				t.Fatalf("tool discovery cache control = %#v, want ephemeral", part.CacheControl)
			}
		}
	}
	if !found {
		t.Fatal("system parts missing tool discovery prompt metadata")
	}
}

func TestContextBuilder_SuppressesToolDiscoveryContributorWhenToolsUnavailable(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cb := NewContextBuilder(t.TempDir()).WithToolDiscovery(true, false)

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage:      "hello",
		SuppressToolUseRule: true,
	})
	system := messages[0]
	if strings.Contains(system.Content, "tool_search_tool_bm25") {
		t.Fatalf("system prompt includes tool discovery despite tools being unavailable: %q", system.Content)
	}
	for _, part := range system.SystemParts {
		if part.PromptSource == string(PromptSourceToolDiscovery) {
			t.Fatalf("system parts include tool discovery despite tools being unavailable: %#v", part)
		}
	}
}

func TestContextBuilder_SuppressesToolReferencesWhenToolsUnavailable(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	writeTurnProfileSkill(
		t,
		workspace,
		"research",
		"---\ndescription: research skill\n---\n# research\n\nResearch carefully.",
	)
	cb := NewContextBuilder(workspace)

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage:      "hello",
		SuppressToolUseRule: true,
	})
	system := messages[0]
	if strings.Contains(system.Content, "When using tools") ||
		strings.Contains(system.Content, "read_file tool") ||
		strings.Contains(system.Content, "update "+workspace+"/memory/MEMORY.md") {
		t.Fatalf("system prompt includes tool references despite tools being unavailable: %q", system.Content)
	}
	if !strings.Contains(system.Content, "<name>research</name>") {
		t.Fatalf("system prompt should keep non-tool skill catalog context, got: %q", system.Content)
	}
}

func TestContextBuilder_CustomToolAllowListSuppressesReadFileSkillInstruction(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	writeTurnProfileSkill(
		t,
		workspace,
		"research",
		"---\ndescription: research skill\n---\n# research\n\nResearch carefully.",
	)
	cb := NewContextBuilder(workspace)

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "hello",
		AllowedTools:   []string{"web_search"},
	})
	system := messages[0]
	if strings.Contains(system.Content, "read_file tool") {
		t.Fatalf("system prompt includes read_file skill instruction without read_file permission: %q", system.Content)
	}
	if !strings.Contains(system.Content, "<name>research</name>") {
		t.Fatalf("system prompt should keep skill catalog context, got: %q", system.Content)
	}
}

func TestContextBuilder_CollectsMCPServerContributor(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cb := NewContextBuilder(t.TempDir())
	contributor := mustMCPServerPromptContributor(
		t,
		"GitHub Server",
		[]string{
			"mcp_github_server_create_issue_1",
			"mcp_github_server_list_issues_2",
			"mcp_github_server_search_3",
		},
		[]string{"tool_search_tool_bm25"},
		true,
	)
	err := cb.RegisterPromptContributor(contributor)
	if err != nil {
		t.Fatalf("RegisterPromptContributor() error = %v", err)
	}

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	system := messages[0]
	if !strings.Contains(system.Content, "MCP server `GitHub Server` is connected") {
		t.Fatalf("system prompt missing MCP contributor content: %q", system.Content)
	}

	var found bool
	wantSource := "mcp:github_server:sha256:" +
		"1eacf87fe31fc10819b7fb287a79bb165c01ddcf6846b7dedf8298b0447c91f2"
	for _, part := range system.SystemParts {
		if part.PromptSource == wantSource {
			found = true
			if part.PromptLayer != string(PromptLayerCapability) || part.PromptSlot != string(PromptSlotMCP) {
				t.Fatalf("mcp metadata = %#v, want capability/mcp", part)
			}
			if part.CacheControl == nil || part.CacheControl.Type != "ephemeral" {
				t.Fatalf("mcp cache control = %#v, want ephemeral", part.CacheControl)
			}
		}
	}
	if !found {
		t.Fatal("system parts missing MCP prompt metadata")
	}
}

func TestContextBuilder_SuppressesMCPServerContributorWhenToolsUnavailable(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cb := NewContextBuilder(t.TempDir())
	contributor := mustMCPServerPromptContributor(
		t,
		"GitHub Server",
		[]string{"mcp_github_server_search_3"},
		nil,
		false,
	)
	err := cb.RegisterPromptContributor(contributor)
	if err != nil {
		t.Fatalf("RegisterPromptContributor() error = %v", err)
	}

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage:      "hello",
		SuppressToolUseRule: true,
	})
	system := messages[0]
	if strings.Contains(system.Content, "MCP server `GitHub Server` is connected") ||
		strings.Contains(system.Content, "available as native tools") {
		t.Fatalf("system prompt includes MCP tooling despite tools being unavailable: %q", system.Content)
	}
	for _, part := range system.SystemParts {
		if strings.HasPrefix(part.PromptSource, "mcp:github_server:") {
			t.Fatalf("system parts include MCP tooling despite tools being unavailable: %#v", part)
		}
	}
}

func TestContextBuilder_SuppressesAgentDiscoveryContributorWhenToolsUnavailable(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cb := NewContextBuilder(t.TempDir()).WithAgentDiscovery(
		"main",
		func(agentID string) []AgentDescriptor {
			return []AgentDescriptor{{
				ID:          "helper",
				Name:        "Helper",
				Description: "Helps with tasks",
			}}
		},
	)

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage:      "hello",
		SuppressToolUseRule: true,
	})
	system := messages[0]
	if strings.Contains(system.Content, "Agent Discovery") ||
		strings.Contains(system.Content, "calling spawn") {
		t.Fatalf("system prompt includes agent discovery despite tools being unavailable: %q", system.Content)
	}
	for _, part := range system.SystemParts {
		if part.PromptSource == string(PromptSourceAgentDiscovery) {
			t.Fatalf("system parts include agent discovery despite tools being unavailable: %#v", part)
		}
	}
}

func TestContextBuilder_CustomToolAllowListSuppressesUnallowedToolContributors(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cb := NewContextBuilder(t.TempDir()).
		WithToolDiscovery(true, true).
		WithAgentDiscovery(
			"main",
			func(agentID string) []AgentDescriptor {
				return []AgentDescriptor{{
					ID:          "helper",
					Name:        "Helper",
					Description: "Helps with tasks",
				}}
			},
		)
	contributor := mustMCPServerPromptContributor(
		t,
		"GitHub Server",
		[]string{"mcp_github_server_search_3"},
		nil,
		false,
	)
	err := cb.RegisterPromptContributor(contributor)
	if err != nil {
		t.Fatalf("RegisterPromptContributor() error = %v", err)
	}

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "hello",
		AllowedTools:   []string{"echo_text"},
	})
	system := messages[0]
	blockedSnippets := []string{
		"tool_search_tool_bm25",
		"tool_search_tool_regex",
		"MCP server `GitHub Server` is connected",
		"Agent Discovery",
		"calling spawn",
	}
	for _, snippet := range blockedSnippets {
		if strings.Contains(system.Content, snippet) {
			t.Fatalf("system prompt includes unallowed tool contributor %q: %q", snippet, system.Content)
		}
	}
	for _, part := range system.SystemParts {
		switch part.PromptSource {
		case string(PromptSourceToolDiscovery), string(PromptSourceAgentDiscovery):
			t.Fatalf("system parts include unallowed tool contributor: %#v", part)
		}
		if strings.HasPrefix(part.PromptSource, "mcp:github_server:") {
			t.Fatalf("system parts include unallowed MCP contributor: %#v", part)
		}
	}
}

func mustMCPServerPromptContributor(
	t *testing.T,
	serverName string,
	admittedCanonicalNames []string,
	discoveryToolNames []string,
	deferred bool,
) mcpServerPromptContributor {
	t.Helper()
	contributor, err := newMCPServerPromptContributor(
		serverName,
		admittedCanonicalNames,
		discoveryToolNames,
		deferred,
	)
	if err != nil {
		t.Fatal(err)
	}
	return contributor
}

func TestMCPPromptSourceIdentityIsCompatibleAndCollisionResistant(t *testing.T) {
	if got := mcpPromptSourceID("github"); got != "mcp:github" {
		t.Fatalf("safe source ID = %q, want historical mcp:github", got)
	}
	if got := mcpPromptPartID("github"); got != "capability.mcp.github" {
		t.Fatalf("safe part ID = %q, want historical capability.mcp.github", got)
	}

	const digest = "1eacf87fe31fc10819b7fb287a79bb165c01ddcf6846b7dedf8298b0447c91f2"
	if got, want := mcpPromptSourceID("GitHub Server"),
		PromptSourceID("mcp:github_server:sha256:"+digest); got != want {
		t.Fatalf("lossy source ID = %q, want %q", got, want)
	}
	if got, want := mcpPromptPartID("GitHub Server"),
		"capability.mcp.github_server.sha256."+digest; got != want {
		t.Fatalf("lossy part ID = %q, want %q", got, want)
	}

	identities := []PromptSourceID{
		mcpPromptSourceID("GitHub Server"),
		mcpPromptSourceID("GitHub@Server"),
		mcpPromptSourceID("github server"),
		mcpPromptSourceID(" github_server "),
		mcpPromptSourceID(strings.Repeat("server", 20) + "a"),
		mcpPromptSourceID(strings.Repeat("server", 20) + "b"),
	}
	seen := make(map[PromptSourceID]struct{}, len(identities))
	for _, identity := range identities {
		if _, duplicate := seen[identity]; duplicate {
			t.Fatalf("lossy prompt source ID collided: %q", identity)
		}
		seen[identity] = struct{}{}
	}
}

func TestMCPPromptContributorsLossyServersRemainDistinctAndInvalidateCache(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cb := NewContextBuilder(t.TempDir())
	before := cb.BuildSystemPromptWithCache()
	if strings.Contains(before, "MCP server `") {
		t.Fatalf("baseline prompt unexpectedly includes MCP: %q", before)
	}

	contributors := []mcpServerPromptContributor{
		mustMCPServerPromptContributor(
			t, "GitHub Server", []string{"mcp_first_tool"}, nil, false,
		),
		mustMCPServerPromptContributor(
			t, "GitHub@Server", []string{"mcp_second_tool"}, nil, false,
		),
	}
	for _, contributor := range contributors {
		if err := cb.RegisterPromptContributor(contributor); err != nil {
			t.Fatal(err)
		}
	}

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	system := messages[0]
	for _, serverName := range []string{"GitHub Server", "GitHub@Server"} {
		if count := strings.Count(system.Content, "MCP server `"+serverName+"` is connected"); count != 1 {
			t.Fatalf("server %q prompt count = %d; prompt=%q", serverName, count, system.Content)
		}
	}
	wantSources := map[string]struct{}{
		string(mcpPromptSourceID("GitHub Server")): {},
		string(mcpPromptSourceID("GitHub@Server")): {},
	}
	for _, part := range system.SystemParts {
		delete(wantSources, part.PromptSource)
	}
	if len(wantSources) != 0 {
		t.Fatalf("lossy MCP contributors replaced each other: missing %#v", wantSources)
	}
}

func TestMCPServerPromptContributorDetachesSortsAndValidatesAdmissions(t *testing.T) {
	admitted := []string{"mcp_server_z", "mcp_server_a", "mcp_server_a"}
	discovery := []string{"tool_search_tool_regex", "tool_search_tool_bm25"}
	contributor := mustMCPServerPromptContributor(
		t,
		"server",
		admitted,
		discovery,
		true,
	)
	admitted[0] = "mutated"
	discovery[0] = "mutated"
	if !reflect.DeepEqual(
		contributor.admittedCanonicalNames,
		[]string{"mcp_server_a", "mcp_server_z"},
	) || !reflect.DeepEqual(
		contributor.discoveryToolNames,
		[]string{"tool_search_tool_bm25", "tool_search_tool_regex"},
	) {
		t.Fatalf("detached contributor = %#v", contributor)
	}

	for _, test := range []struct {
		name       string
		serverName string
		admitted   []string
		discovery  []string
	}{
		{name: "blank server", serverName: " ", admitted: []string{"mcp_server_a"}},
		{name: "no admissions", serverName: "server"},
		{name: "blank admission", serverName: "server", admitted: []string{""}},
		{name: "inexact admission", serverName: "server", admitted: []string{" mcp_server_a"}},
		{name: "blank discovery", serverName: "server", admitted: []string{"mcp_server_a"}, discovery: []string{""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := newMCPServerPromptContributor(
				test.serverName,
				test.admitted,
				test.discovery,
				false,
			); err == nil || got.serverName != "" {
				t.Fatalf("invalid contributor = %#v, %v", got, err)
			}
		})
	}
}

func TestMCPServerPromptContributorUsesExactAdmissionsAndSubsetCount(t *testing.T) {
	native := mustMCPServerPromptContributor(
		t,
		"GitHub Server",
		[]string{"mcp_github_server_create_11111111", "mcp_github_server_search_22222222"},
		nil,
		false,
	)
	assertPromptCount := func(
		t *testing.T,
		contributor mcpServerPromptContributor,
		allowed []string,
		want int,
	) {
		t.Helper()
		parts, err := contributor.ContributePrompt(context.Background(), PromptBuildRequest{
			AllowedTools: allowed,
		})
		if err != nil {
			t.Fatal(err)
		}
		if want == 0 {
			if len(parts) != 0 {
				t.Fatalf("allowed %v produced %#v, want no prompt", allowed, parts)
			}
			return
		}
		if len(parts) != 1 || !strings.Contains(
			parts[0].Content,
			fmt.Sprintf("It contributes %d tool(s)", want),
		) {
			t.Fatalf("allowed %v prompt = %#v, want count %d", allowed, parts, want)
		}
	}

	assertPromptCount(t, native, nil, 2)
	assertPromptCount(t, native, []string{"mcp_github_server_search_22222222"}, 1)
	assertPromptCount(t, native, []string{"MCP_GITHUB_SERVER_SEARCH_22222222"}, 1)
	assertPromptCount(t, native, []string{"mcp_github_server_unadmitted"}, 0)
	assertPromptCount(t, native, []string{"mcp_github_server"}, 0)

	deferred := mustMCPServerPromptContributor(
		t,
		"github",
		[]string{"mcp_github_hidden"},
		[]string{"tool_search_tool_bm25"},
		true,
	)
	assertPromptCount(t, deferred, nil, 1)
	assertPromptCount(t, deferred, []string{"mcp_github_hidden"}, 0)
	assertPromptCount(t, deferred, []string{"tool_search_tool_bm25"}, 0)
	assertPromptCount(
		t,
		deferred,
		[]string{"mcp_github_hidden", "tool_search_tool_bm25"},
		1,
	)
	withoutDiscovery := mustMCPServerPromptContributor(
		t,
		"github",
		[]string{"mcp_github_hidden"},
		nil,
		true,
	)
	assertPromptCount(t, withoutDiscovery, nil, 0)
}

func TestContextBuilder_IncludesThreadPolicyContributor(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Tools.Threads.Policy = config.ThreadPolicyConfig{
		Enabled: true,
		Mode:    config.ThreadPolicyModeTool,
		Rules: []config.ThreadPolicyRule{
			{
				Type:           "coding",
				Description:    "Move code work into a coding thread.",
				MinMessages:    12,
				MinTextChars:   6000,
				ThresholdLogic: config.ThreadPolicyThresholdAll,
			},
		},
	}
	cb := NewContextBuilder(t.TempDir()).WithThreadPolicy(cfg)

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "please code this",
	})
	system := messages[0]
	thresholdSnippet := "12 visible user/assistant messages and 6000 visible user/assistant text characters"
	if !strings.Contains(system.Content, "## Thread Routing Policy") ||
		!strings.Contains(system.Content, "Start the main chat as a normal chat") ||
		!strings.Contains(system.Content, "Move code work into a coding thread.") ||
		!strings.Contains(system.Content, "register_current") ||
		!strings.Contains(system.Content, thresholdSnippet) ||
		!strings.Contains(system.Content, "thread navigation, not new work") ||
		!strings.Contains(system.Content, "without `create_if_missing`") {
		t.Fatalf("system prompt missing thread policy: %q", system.Content)
	}
}

func TestContextBuilder_SuppressesThreadPolicyWithoutThreadsTool(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cfg := config.DefaultConfig()
	cb := NewContextBuilder(t.TempDir()).WithThreadPolicy(cfg)

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{
		CurrentMessage: "please code this",
		AllowedTools:   []string{"echo_text"},
	})
	system := messages[0]
	if strings.Contains(system.Content, "## Thread Routing Policy") {
		t.Fatalf("system prompt includes thread policy despite tool allowlist: %q", system.Content)
	}
}

type testPromptContributor struct {
	desc PromptSourceDescriptor
	part PromptPart
}

type panickingPromptContributor struct{}

func (*panickingPromptContributor) PromptSource() PromptSourceDescriptor {
	panic("prompt source panic")
}

func (*panickingPromptContributor) ContributePrompt(
	context.Context,
	PromptBuildRequest,
) ([]PromptPart, error) {
	return nil, nil
}

func (c testPromptContributor) PromptSource() PromptSourceDescriptor {
	return c.desc
}

func (c testPromptContributor) ContributePrompt(_ context.Context, _ PromptBuildRequest) ([]PromptPart, error) {
	return []PromptPart{c.part}, nil
}

func TestContextBuilder_CollectsRegisteredPromptContributors(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cb := NewContextBuilder(t.TempDir())

	sourceID := PromptSourceID("test:contributor")
	err := cb.RegisterPromptContributor(testPromptContributor{
		desc: PromptSourceDescriptor{
			ID:      sourceID,
			Owner:   "test",
			Allowed: []PromptPlacement{{Layer: PromptLayerCapability, Slot: PromptSlotMCP}},
		},
		part: PromptPart{
			ID:      "capability.mcp.test",
			Layer:   PromptLayerCapability,
			Slot:    PromptSlotMCP,
			Source:  PromptSource{ID: sourceID, Name: "test"},
			Content: "registered contributor prompt",
		},
	})
	if err != nil {
		t.Fatalf("RegisterPromptContributor() error = %v", err)
	}

	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	if !strings.Contains(messages[0].Content, "registered contributor prompt") {
		t.Fatalf("system prompt missing contributor content: %q", messages[0].Content)
	}
}

func promptBatchTestContributor(sourceID PromptSourceID, content string) testPromptContributor {
	return promptBatchTestContributorAt(
		sourceID,
		content,
		PromptPlacement{Layer: PromptLayerCapability, Slot: PromptSlotMCP},
	)
}

func promptBatchTestContributorAt(
	sourceID PromptSourceID,
	content string,
	placement PromptPlacement,
) testPromptContributor {
	return testPromptContributor{
		desc: PromptSourceDescriptor{
			ID: sourceID, Owner: content,
			Allowed: []PromptPlacement{placement},
		},
		part: PromptPart{
			ID:    "capability.batch." + string(sourceID),
			Layer: placement.Layer, Slot: placement.Slot,
			Source: PromptSource{ID: sourceID}, Content: content,
		},
	}
}

func collectPromptBatchContents(
	t *testing.T,
	registry *PromptRegistry,
) []string {
	t.Helper()
	parts, err := registry.Collect(context.Background(), PromptBuildRequest{})
	if err != nil {
		t.Fatal(err)
	}
	contents := make([]string, 0, len(parts))
	for _, part := range parts {
		contents = append(contents, part.Content)
	}
	sort.Strings(contents)
	return contents
}

func TestPromptRegistryRegisterContributorsRejectsWholeInvalidBatch(t *testing.T) {
	registry := NewPromptRegistry()
	baseline := []PromptContributor{
		promptBatchTestContributor("batch:a", "old-a"),
		promptBatchTestContributor("batch:b", "old-b"),
	}
	if err := registry.RegisterContributors(baseline); err != nil {
		t.Fatal(err)
	}
	want := []string{"old-a", "old-b"}

	assertUnchanged := func(t *testing.T) {
		t.Helper()
		if got := collectPromptBatchContents(t, registry); !reflect.DeepEqual(got, want) {
			t.Fatalf("registry contents = %v, want unchanged %v", got, want)
		}
		registry.mu.RLock()
		descriptor := registry.sources["batch:a"]
		registry.mu.RUnlock()
		if descriptor.Owner != "old-a" {
			t.Fatalf("source descriptor partially replaced: %#v", descriptor)
		}
	}

	invalid := promptBatchTestContributor("batch:c", "invalid")
	invalid.desc.Allowed = nil
	if err := registry.RegisterContributors([]PromptContributor{
		promptBatchTestContributor("batch:a", "new-a"),
		invalid,
	}); err == nil {
		t.Fatal("invalid descriptor batch was accepted")
	}
	assertUnchanged(t)

	if err := registry.RegisterContributors([]PromptContributor{
		promptBatchTestContributor("batch:a", "new-a"),
		promptBatchTestContributor(" batch:a ", "duplicate-a"),
	}); err == nil {
		t.Fatal("duplicate source batch was accepted")
	}
	assertUnchanged(t)

	if err := registry.RegisterContributors([]PromptContributor{
		promptBatchTestContributor("batch:a", "new-a"),
		nil,
	}); err == nil {
		t.Fatal("nil contributor batch was accepted")
	}
	assertUnchanged(t)

	var typedNil *testPromptContributor
	if err := registry.RegisterContributors([]PromptContributor{
		promptBatchTestContributor("batch:a", "new-a"),
		typedNil,
	}); err == nil {
		t.Fatal("typed-nil contributor batch was accepted")
	}
	assertUnchanged(t)

	if err := registry.RegisterContributors([]PromptContributor{
		promptBatchTestContributor("batch:a", "new-a"),
		&panickingPromptContributor{},
	}); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("panicking descriptor error = %v", err)
	}
	assertUnchanged(t)
}

func TestPromptRegistryRegisterContributorsReplacesAtomicallyAndDetachesSources(t *testing.T) {
	registry := NewPromptRegistry()
	if err := registry.RegisterContributors([]PromptContributor{
		promptBatchTestContributor("batch:a", "old-a"),
		promptBatchTestContributor("batch:unrelated", "unrelated"),
	}); err != nil {
		t.Fatal(err)
	}

	placements := []PromptPlacement{{Layer: PromptLayerCapability, Slot: PromptSlotMCP}}
	replacement := promptBatchTestContributor("batch:a", "new-a")
	replacement.desc.Allowed = placements
	if err := registry.RegisterContributors([]PromptContributor{
		replacement,
		promptBatchTestContributor("batch:b", "new-b"),
	}); err != nil {
		t.Fatal(err)
	}
	placements[0] = PromptPlacement{Layer: PromptLayerContext, Slot: PromptSlotRuntime}

	if got, want := collectPromptBatchContents(t, registry),
		[]string{"new-a", "new-b", "unrelated"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement contents = %v, want %v", got, want)
	}
	if err := registry.ValidatePart(PromptPart{
		ID:    "replacement.detachment",
		Layer: PromptLayerCapability, Slot: PromptSlotMCP,
		Source: PromptSource{ID: "batch:a"}, Content: "valid",
	}); err != nil {
		t.Fatalf("registered descriptor retained caller placement alias: %v", err)
	}
}

func TestContextBuilderRegisterPromptContributorsInvalidatesCacheOnce(t *testing.T) {
	t.Setenv("PICOCLAW_BUILTIN_SKILLS", t.TempDir())
	cb := NewContextBuilder(t.TempDir())
	if prompt := cb.BuildSystemPromptWithCache(); prompt == "" || cb.cachedSystemPrompt == "" {
		t.Fatal("test setup did not populate the context-builder cache")
	}
	if err := cb.RegisterPromptContributors([]PromptContributor{
		promptBatchTestContributor("batch:cache-a", "cache-a"),
		promptBatchTestContributor("batch:cache-b", "cache-b"),
	}); err != nil {
		t.Fatal(err)
	}
	if cb.cachedSystemPrompt != "" {
		t.Fatal("successful prompt batch did not invalidate the cached system prompt")
	}
	messages := cb.BuildMessagesFromPrompt(PromptBuildRequest{CurrentMessage: "hello"})
	if !strings.Contains(messages[0].Content, "cache-a") ||
		!strings.Contains(messages[0].Content, "cache-b") {
		t.Fatalf("rebuilt prompt omitted batch contributors: %q", messages[0].Content)
	}
}

func TestPromptRegistryConcurrentCollectSeesOldOrCompleteNewBatch(t *testing.T) {
	registry := NewPromptRegistry()
	oldBatch := []PromptContributor{
		promptBatchTestContributor("batch:one", "old-one"),
		promptBatchTestContributor("batch:two", "old-two"),
	}
	newBatch := []PromptContributor{
		promptBatchTestContributorAt(
			"batch:one",
			"new-one",
			PromptPlacement{Layer: PromptLayerContext, Slot: PromptSlotRuntime},
		),
		promptBatchTestContributorAt(
			"batch:two",
			"new-two",
			PromptPlacement{Layer: PromptLayerContext, Slot: PromptSlotRuntime},
		),
	}
	if err := registry.RegisterContributors(oldBatch); err != nil {
		t.Fatal(err)
	}

	const collectors = 8
	const collectionsPerWorker = 500
	start := make(chan struct{})
	errorsCh := make(chan string, collectors)
	var observations atomic.Uint64
	var wg sync.WaitGroup
	for range collectors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range collectionsPerWorker {
				parts, err := registry.Collect(context.Background(), PromptBuildRequest{})
				if err != nil {
					select {
					case errorsCh <- err.Error():
					default:
					}
					return
				}
				contents := make([]string, 0, len(parts))
				for _, part := range parts {
					contents = append(contents, part.Content)
				}
				sort.Strings(contents)
				state := strings.Join(contents, "|")
				if state != "old-one|old-two" && state != "new-one|new-two" {
					select {
					case errorsCh <- "partial prompt batch: " + state:
					default:
					}
					return
				}
				observations.Add(1)
			}
		}()
	}
	close(start)
	for iteration := range collectionsPerWorker {
		batch := oldBatch
		if iteration%2 == 0 {
			batch = newBatch
		}
		if err := registry.RegisterContributors(batch); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	close(errorsCh)
	for message := range errorsCh {
		t.Error(message)
	}
	if observations.Load() == 0 {
		t.Fatal("concurrent collectors made no observations")
	}
}
