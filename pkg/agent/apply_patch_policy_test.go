package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
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

func TestAgentApplyPatchTransactionStateRootRejectsAbsFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit removing the process working directory")
	}
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	unavailableWorkingDirectory := t.TempDir()
	if err = os.Chdir(unavailableWorkingDirectory); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if restoreErr := os.Chdir(originalWorkingDirectory); restoreErr != nil {
			t.Errorf("restore working directory: %v", restoreErr)
		}
	}()
	if err = os.Remove(unavailableWorkingDirectory); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, "relative-home")
	if got, rootErr := agentApplyPatchTransactionStateRoot(); rootErr == nil || got != "" {
		t.Fatalf("state root after Abs failure = %q, %v", got, rootErr)
	}
}

func TestAgentApplyPatchTransactionRootOverlapFailsClosed(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if !agentApplyPatchTransactionRootOverlapsWorkspace(root, workspace) {
		t.Fatal("ancestor transaction root was not classified as overlapping")
	}
	if !agentApplyPatchTransactionRootOverlapsWorkspace("invalid\x00root", workspace) {
		t.Fatal("invalid transaction root did not fail closed")
	}
	if !agentApplyPatchTransactionRootOverlapsWorkspace(root, filepath.Join(root, "missing")) {
		t.Fatal("missing workspace did not fail closed")
	}
	resolved, err := agentApplyPatchResolveAgainstExistingAncestor(root)
	if err != nil || resolved != filepath.Clean(root) {
		t.Fatalf("resolved existing root = %q, %v", resolved, err)
	}
	resolved, err = agentApplyPatchResolveAgainstExistingAncestor(
		filepath.Join(root, "future", "child"),
	)
	if err != nil || resolved != filepath.Join(root, "future", "child") {
		t.Fatalf("resolved future root = %q, %v", resolved, err)
	}
}

func TestAgentApplyPatchAdmissionRejectsEverySiblingAuthority(t *testing.T) {
	transactionRoot := filepath.Join(t.TempDir(), "apply_patch_transactions")
	safeRoot := t.TempDir()
	safePatterns := compilePatterns([]string{agentApplyPatchTestPathPattern(safeRoot)})
	statePatterns := compilePatterns([]string{agentApplyPatchTestPathPattern(transactionRoot)})

	for _, toolName := range []string{
		"read_file", "list_dir", "message_media", "send_file", "load_image",
		"write_file", "edit_file", "append_file",
	} {
		t.Run("unrestricted_"+toolName, func(t *testing.T) {
			cfg := &config.Config{}
			enableAgentApplyPatchTestFilesystemTool(&cfg.Tools, toolName)
			defaults := &config.AgentDefaults{RestrictToWorkspace: false}
			if agentApplyPatchAdmissionSafe(
				defaults, cfg, transactionRoot, safePatterns, safePatterns,
			) {
				t.Fatalf("%s global authority admitted apply_patch", toolName)
			}
		})

		t.Run("state_pattern_"+toolName, func(t *testing.T) {
			cfg := &config.Config{}
			enableAgentApplyPatchTestFilesystemTool(&cfg.Tools, toolName)
			defaults := &config.AgentDefaults{RestrictToWorkspace: true}
			readPatterns, writePatterns := safePatterns, safePatterns
			switch toolName {
			case "read_file", "list_dir", "message_media", "send_file", "load_image":
				readPatterns = statePatterns
			default:
				writePatterns = statePatterns
			}
			if agentApplyPatchAdmissionSafe(
				defaults, cfg, transactionRoot, readPatterns, writePatterns,
			) {
				t.Fatalf("%s state-root regex authority admitted apply_patch", toolName)
			}
		})
	}

	for _, toolName := range []string{
		"read_file", "list_dir", "message_media", "send_file", "load_image",
	} {
		t.Run("outside_read_"+toolName, func(t *testing.T) {
			cfg := &config.Config{}
			enableAgentApplyPatchTestFilesystemTool(&cfg.Tools, toolName)
			defaults := &config.AgentDefaults{
				RestrictToWorkspace:       true,
				AllowReadOutsideWorkspace: true,
			}
			if agentApplyPatchAdmissionSafe(
				defaults, cfg, transactionRoot, safePatterns, safePatterns,
			) {
				t.Fatalf("%s outside-read authority admitted apply_patch", toolName)
			}
		})
	}

	safeConfig := &config.Config{}
	for _, toolName := range []string{
		"read_file", "list_dir", "message_media", "send_file", "load_image",
		"write_file", "edit_file", "append_file",
	} {
		enableAgentApplyPatchTestFilesystemTool(&safeConfig.Tools, toolName)
	}
	if !agentApplyPatchAdmissionSafe(
		&config.AgentDefaults{RestrictToWorkspace: true},
		safeConfig,
		transactionRoot,
		safePatterns,
		safePatterns,
	) {
		t.Fatal("restricted siblings with disjoint path authority did not admit apply_patch")
	}
	adjacentPattern := compilePatterns([]string{
		agentApplyPatchTestPathPattern(transactionRoot + "-adjacent"),
	})
	if agentApplyPatchPatternsMayReachStateRoot(transactionRoot, adjacentPattern) {
		t.Fatal("adjacent but path-disjoint allow root was rejected")
	}
	unicodeRoot := filepath.Join(filepath.Dir(transactionRoot), "caf\u00e9", "state")
	unicodeAlias := filepath.Join(filepath.Dir(transactionRoot), "cafe\u0301", "state")
	if !agentApplyPatchPatternsMayReachStateRoot(
		unicodeRoot,
		compilePatterns([]string{agentApplyPatchTestPathPattern(unicodeAlias)}),
	) {
		t.Fatal("Unicode-normalized state-root alias was treated as disjoint")
	}
	if !agentApplyPatchPatternsMayReachStateRoot(
		transactionRoot,
		compilePatterns([]string{agentApplyPatchTestPathPattern(transactionRoot + ".")}),
	) {
		t.Fatal("trailing-dot state-root alias was treated as disjoint")
	}
	if got, want := agentApplyPatchAuthorityPathKey(
		filepath.Join(filepath.Dir(transactionRoot), "...", filepath.Base(transactionRoot)),
	), agentApplyPatchAuthorityPathKey(transactionRoot); got != want {
		t.Fatalf("empty normalized alias component = %q, want %q", got, want)
	}
	if !agentApplyPatchLiteralPrefixesMayOverlap(
		string(os.PathSeparator),
		string(os.PathSeparator)+"control",
	) {
		t.Fatal("filesystem root prefix was treated as disjoint from its descendant")
	}
	if !agentApplyPatchPatternsMayReachStateRoot(
		transactionRoot,
		compilePatterns([]string{"auth\\.key$"}),
	) {
		t.Fatal("unanchored control-file pattern was not rejected fail closed")
	}
	for name, candidate := range map[string]struct {
		root     string
		patterns []*regexp.Regexp
	}{
		"relative root": {
			root: "relative/state", patterns: safePatterns,
		},
		"nil pattern": {
			root: transactionRoot, patterns: []*regexp.Regexp{nil},
		},
		"no literal prefix": {
			root: transactionRoot, patterns: compilePatterns([]string{"^.*auth\\.key$"}),
		},
		"relative literal prefix": {
			root: transactionRoot, patterns: compilePatterns([]string{"^relative/path$"}),
		},
	} {
		if !agentApplyPatchPatternsMayReachStateRoot(candidate.root, candidate.patterns) {
			t.Fatalf("%s was not rejected fail closed", name)
		}
	}
	if agentApplyPatchAdmissionSafe(nil, safeConfig, transactionRoot, nil, nil) ||
		agentApplyPatchAdmissionSafe(
			&config.AgentDefaults{RestrictToWorkspace: true},
			nil,
			transactionRoot,
			nil,
			nil,
		) {
		t.Fatal("incomplete construction policy admitted apply_patch")
	}
}

func TestAgentApplyPatchUnsafeAdmissionOmitsRootAndOwnerFactory(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		configure func(*config.Config, string)
	}{
		{
			name: "unrestricted writer",
			configure: func(cfg *config.Config, _ string) {
				cfg.Agents.Defaults.RestrictToWorkspace = false
			},
		},
		{
			name: "global reader",
			configure: func(cfg *config.Config, _ string) {
				cfg.Agents.Defaults.AllowReadOutsideWorkspace = true
				cfg.Tools.ReadFile.Enabled = true
			},
		},
		{
			name: "write pattern reaches root",
			configure: func(cfg *config.Config, root string) {
				cfg.Tools.AllowWritePaths = []string{agentApplyPatchTestPathPattern(root)}
			},
		},
		{
			name: "read pattern reaches control descendant",
			configure: func(cfg *config.Config, root string) {
				cfg.Tools.ReadFile.Enabled = true
				cfg.Tools.AllowReadPaths = []string{
					"^" + regexp.QuoteMeta(filepath.Join(root, "auth.key")) + "$",
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv(config.EnvHome, home)
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
			root := filepath.Join(home, agentApplyPatchTransactionStateDirectory)
			testCase.configure(cfg, root)
			agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
			defer agent.Close()
			if _, ok := agent.Tools.Get("apply_patch"); ok {
				t.Fatalf("unsafe root apply_patch registered; tools=%v", agent.Tools.List())
			}
			owner, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
				Scope: tools.ToolOwnerScopeAgent, AgentID: "unsafe-apply-policy",
			}, []string{"apply_patch"})
			if err == nil || owner != nil {
				if owner != nil {
					_ = owner.Close()
				}
				t.Fatalf("unsafe owner apply_patch selection = %#v, %v", owner, err)
			}

			cfg.Agents.Defaults.RestrictToWorkspace = true
			cfg.Agents.Defaults.AllowReadOutsideWorkspace = false
			cfg.Tools.AllowReadPaths = nil
			cfg.Tools.AllowWritePaths = nil
			owner, err = agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
				Scope: tools.ToolOwnerScopeAgent, AgentID: "mutated-safe-apply-policy",
			}, []string{"apply_patch"})
			if err == nil || owner != nil {
				if owner != nil {
					_ = owner.Close()
				}
				t.Fatalf("config mutation restored omitted apply_patch = %#v, %v", owner, err)
			}
		})
	}
}

func TestAgentApplyPatchRootAndAdmissionAreFrozenForOwnerFactory(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	workspace := t.TempDir()
	disjointReadRoot := t.TempDir()
	disjointWriteRoot := t.TempDir()
	cfg := &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: workspace, ModelName: "gpt-5", Provider: "openai",
			RestrictToWorkspace: true,
		}},
		Tools: config.ToolsConfig{
			AllowReadPaths:  []string{agentApplyPatchTestPathPattern(disjointReadRoot)},
			AllowWritePaths: []string{agentApplyPatchTestPathPattern(disjointWriteRoot)},
			Adaptation:      config.DefaultToolAdaptationConfig(),
			ReadFile:        config.ReadFileToolConfig{Enabled: true},
			ListDir:         config.ToolConfig{Enabled: true},
			EditFile:        config.ToolConfig{Enabled: true},
			AppendFile:      config.ToolConfig{Enabled: true},
			WriteFile:       config.ToolConfig{Enabled: true},
		},
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()
	if _, ok := agent.Tools.Get("apply_patch"); !ok {
		t.Fatalf("safe root apply_patch missing; tools=%v", agent.Tools.List())
	}

	// If the owner factory rereads either environment or Config, this mutated
	// home overlaps the workspace and the now-global sibling policy is unsafe.
	// The frozen construction snapshot must still produce the same safe tool.
	t.Setenv(config.EnvHome, filepath.Join(workspace, "mutated-home"))
	cfg.Agents.Defaults.RestrictToWorkspace = false
	cfg.Agents.Defaults.AllowReadOutsideWorkspace = true
	cfg.Tools.AllowReadPaths = []string{".*"}
	cfg.Tools.AllowWritePaths = []string{".*"}
	owner, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "frozen-apply-policy-state",
	}, []string{"apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, ok := owner.Get("apply_patch"); !ok {
		t.Fatal("owner factory lost frozen apply_patch admission")
	}
}

func agentApplyPatchTestPathPattern(root string) string {
	separator := regexp.QuoteMeta(string(os.PathSeparator))
	return "^" + regexp.QuoteMeta(filepath.Clean(root)) + "(?:" + separator + "|$)"
}

func enableAgentApplyPatchTestFilesystemTool(toolsConfig *config.ToolsConfig, name string) {
	switch name {
	case "read_file":
		toolsConfig.ReadFile.Enabled = true
	case "list_dir":
		toolsConfig.ListDir.Enabled = true
	case "message_media":
		toolsConfig.Message.Enabled = true
		toolsConfig.Message.MediaEnabled = true
	case "send_file":
		toolsConfig.SendFile.Enabled = true
	case "load_image":
		toolsConfig.LoadImage.Enabled = true
	case "write_file":
		toolsConfig.WriteFile.Enabled = true
	case "edit_file":
		toolsConfig.EditFile.Enabled = true
	case "append_file":
		toolsConfig.AppendFile.Enabled = true
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
