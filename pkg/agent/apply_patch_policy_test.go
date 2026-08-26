package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestAgentApplyPatchProtectedRootsUsesAuthoritativeConstructionPaths(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	gitRoot := filepath.Join(t.TempDir(), "git-workspaces")
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}},
		GitWorkspaces: config.GitWorkspacesConfig{
			RootDir: gitRoot,
		},
	}
	want := []string{
		filepath.Join(workspace, "sessions"),
		filepath.Join(workspace, "account_router_state.json"),
		gitRoot,
	}
	got := agentApplyPatchProtectedRoots(workspace, cfg)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("protected roots = %#v, want %#v", got, want)
	}
	got[0] = "mutated"
	if again := agentApplyPatchProtectedRoots(workspace, cfg); !reflect.DeepEqual(again, want) {
		t.Fatalf("protected roots retained caller mutation: %#v", again)
	}
}

func TestAgentApplyPatchProtectedRootsWithoutConfig(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	want := []string{
		filepath.Join(workspace, "sessions"),
		filepath.Join(workspace, "account_router_state.json"),
	}
	if got := agentApplyPatchProtectedRoots(workspace, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("nil-config protected roots = %#v, want %#v", got, want)
	}
}

func TestAgentApplyPatchInvalidProtectedRootFailsConstruction(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: workspace, ModelName: "gpt-5", Provider: "openai",
			RestrictToWorkspace: true,
		}},
		GitWorkspaces: config.GitWorkspacesConfig{RootDir: "invalid\x00root"},
		Tools: config.ToolsConfig{
			Adaptation: config.DefaultToolAdaptationConfig(),
			WriteFile:  config.ToolConfig{Enabled: true},
		},
	}
	defer func() {
		message, ok := recover().(string)
		if !ok || !strings.Contains(message, "build apply_patch policy") {
			t.Fatalf("construction panic = %#v", message)
		}
	}()
	NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	t.Fatal("invalid apply-patch policy construction did not panic")
}

func TestAgentModelMayUseCodexCompatibleToolsForApplyPatchRegistration(t *testing.T) {
	if AgentModelMayUseCodexCompatibleTools("gpt-5", nil, nil) {
		t.Fatal("nil agent configuration enabled Codex-compatible tools")
	}
	defaults := &config.AgentDefaults{ModelName: "gpt-5", Workspace: t.TempDir()}
	cfg := &config.Config{Tools: config.ToolsConfig{
		Adaptation: config.DefaultToolAdaptationConfig(),
	}}
	if !AgentModelMayUseCodexCompatibleTools("gpt-5", defaults, cfg) {
		t.Fatal("direct GPT model did not enable Codex-compatible tools")
	}
	cfg.ModelRouters = []config.ModelRouterConfig{{
		Name: "coding-router", Enabled: true, Entry: "model",
		Blocks: []config.ModelRouterBlock{{
			ID: "model", Type: config.ModelRouterBlockTypeModel, Model: "gpt-5",
		}},
	}}
	if !AgentModelMayUseCodexCompatibleTools("coding-router", defaults, cfg) {
		t.Fatal("routed GPT model did not enable Codex-compatible tools")
	}
}

func TestAgentApplyPatchPolicyProtectsRootAndOwnerControlPaths(t *testing.T) {
	workspace := t.TempDir()
	fixtures := map[string]string{
		"sessions/private.jsonl":         "session\n",
		"account_router_state.json":      "account\n",
		".git-workspaces/inventory.json": "inventory\n",
		"src/root-compatible.txt":        "before\n",
		"src/owner-compatible.txt":       "before\n",
	}
	for relative, content := range fixtures {
		path := filepath.Join(workspace, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: workspace, ModelName: "gpt-5", Provider: "openai",
			RestrictToWorkspace: true,
		}},
		Tools: config.ToolsConfig{
			Adaptation: config.DefaultToolAdaptationConfig(),
			EditFile:   config.ToolConfig{Enabled: true},
			WriteFile:  config.ToolConfig{Enabled: true},
		},
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	rootTool, ok := agent.Tools.Get("apply_patch")
	if !ok {
		t.Fatalf("root apply_patch missing; tools=%v", agent.Tools.List())
	}
	owned, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "apply-policy-owner",
	}, []string{"apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	ownerTool, ok := owned.Get("apply_patch")
	if !ok {
		t.Fatal("owner apply_patch missing")
	}

	for label, tool := range map[string]tools.Tool{"root": rootTool, "owner": ownerTool} {
		for _, relative := range []string{
			"sessions/private.jsonl",
			"account_router_state.json",
			".git-workspaces/inventory.json",
		} {
			before, readErr := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(relative)))
			if readErr != nil {
				t.Fatal(readErr)
			}
			result := tool.Execute(context.Background(), map[string]any{
				"patch": "*** Begin Patch\n*** Update File: " + filepath.ToSlash(relative) +
					"\n@@\n-" + strings.TrimSuffix(string(before), "\n") + "\n+changed\n*** End Patch",
			})
			if result == nil || !result.IsError {
				t.Fatalf("%s protected patch %q = %#v", label, relative, result)
			}
			after, readErr := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(relative)))
			if readErr != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("%s protected file %q changed: %q, %v", label, relative, after, readErr)
			}
		}
	}

	for label, candidate := range map[string]struct {
		tool tools.Tool
		path string
	}{
		"root":  {tool: rootTool, path: "src/root-compatible.txt"},
		"owner": {tool: ownerTool, path: "src/owner-compatible.txt"},
	} {
		result := candidate.tool.Execute(context.Background(), map[string]any{
			"patch": "*** Begin Patch\n*** Update File: " + candidate.path +
				"\n@@\n-before\n+after\n*** End Patch",
		})
		if result == nil || result.IsError || result.ForLLM != "updated "+candidate.path {
			t.Fatalf("%s ordinary patch = %#v", label, result)
		}
	}
}

func TestAgentApplyPatchOwnerFactoryUsesFrozenProtectedRoots(t *testing.T) {
	workspace := t.TempDir()
	controlA := filepath.Join(workspace, "control-a")
	controlB := filepath.Join(workspace, "control-b")
	for _, root := range []string{controlA, controlB} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "state.txt"), []byte("before\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: workspace, ModelName: "gpt-5", Provider: "openai",
			RestrictToWorkspace: true,
		}},
		GitWorkspaces: config.GitWorkspacesConfig{RootDir: controlA},
		Tools: config.ToolsConfig{
			Adaptation: config.DefaultToolAdaptationConfig(),
			EditFile:   config.ToolConfig{Enabled: true},
			WriteFile:  config.ToolConfig{Enabled: true},
		},
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	rootTool, ok := agent.Tools.Get("apply_patch")
	if !ok {
		t.Fatal("root apply_patch missing")
	}
	cfg.GitWorkspaces.RootDir = controlB
	owned, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "frozen-apply-policy",
	}, []string{"apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	ownerTool, ok := owned.Get("apply_patch")
	if !ok {
		t.Fatal("owner apply_patch missing")
	}

	for label, tool := range map[string]tools.Tool{"root": rootTool, "owner": ownerTool} {
		result := tool.Execute(context.Background(), map[string]any{
			"patch": "*** Begin Patch\n*** Update File: control-a/state.txt\n" +
				"@@\n-before\n+changed\n*** End Patch",
		})
		if result == nil || !result.IsError {
			t.Fatalf("%s tool reread mutable config policy: %#v", label, result)
		}
		content, readErr := os.ReadFile(filepath.Join(controlA, "state.txt"))
		if readErr != nil || string(content) != "before\n" {
			t.Fatalf("%s frozen protected file = %q, %v", label, content, readErr)
		}
	}

	ownerResult := ownerTool.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Update File: control-b/state.txt\n" +
			"@@\n-before\n+after\n*** End Patch",
	})
	if ownerResult == nil || ownerResult.IsError {
		t.Fatalf("owner tool adopted later config root: %#v", ownerResult)
	}
}

func TestAgentApplyPatchSnapshotsProtectedRootsAtExecution(t *testing.T) {
	workspace := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: workspace, ModelName: "gpt-5", Provider: "openai",
			RestrictToWorkspace: true,
		}},
		Tools: config.ToolsConfig{
			Adaptation: config.DefaultToolAdaptationConfig(),
			WriteFile:  config.ToolConfig{Enabled: true},
		},
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	if info, err := os.Stat(filepath.Join(workspace, "sessions")); err != nil || !info.IsDir() {
		t.Fatalf("agent sessions root was not created after tool construction: %#v, %v", info, err)
	}
	patchTool, ok := agent.Tools.Get("apply_patch")
	if !ok {
		t.Fatal("apply_patch missing")
	}
	result := patchTool.Execute(context.Background(), map[string]any{
		"patch": "*** Begin Patch\n*** Add File: ordinary.txt\n+created\n*** End Patch",
	})
	if result == nil || result.IsError {
		t.Fatalf("post-construction protected-root creation caused false drift: %#v", result)
	}
	content, err := os.ReadFile(filepath.Join(workspace, "ordinary.txt"))
	if err != nil || string(content) != "created\n" {
		t.Fatalf("ordinary patch content = %q, %v", content, err)
	}
}
