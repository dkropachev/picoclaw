package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func agentFileMutationTestConfig(workspace string) *config.Config {
	return &config.Config{
		Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
			Workspace: workspace, ModelName: "gpt-5", Provider: "openai",
			RestrictToWorkspace: true,
		}},
		Tools: config.ToolsConfig{
			Adaptation: config.DefaultToolAdaptationConfig(),
			WriteFile:  config.ToolConfig{Enabled: true},
			EditFile:   config.ToolConfig{Enabled: true},
			AppendFile: config.ToolConfig{Enabled: true},
		},
	}
}

func executeAgentFileMutation(
	t *testing.T,
	tool tools.Tool,
	toolName string,
	workspace string,
	path string,
	exists bool,
) *tools.ToolResult {
	t.Helper()
	if toolName == "apply_patch" {
		patchPath, err := filepath.Rel(workspace, path)
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsLocal(patchPath) {
			patchPath = path
		}
		var patch string
		if exists {
			patch = "*** Begin Patch\n*** Update File: " + patchPath +
				"\n@@\n-before\n+changed\n*** End Patch"
		} else {
			patch = "*** Begin Patch\n*** Add File: " + patchPath +
				"\n+changed\n*** End Patch"
		}
		return tool.Execute(context.Background(), map[string]any{"patch": patch})
	}

	args := map[string]any{"path": path}
	switch toolName {
	case "write_file":
		args["content"] = "changed"
		args["overwrite"] = true
	case "edit_file":
		args["old_text"] = "before"
		args["new_text"] = "changed"
	case "append_file":
		args["content"] = "changed"
	}
	return tool.Execute(context.Background(), args)
}

func requireAgentFileMutationDenied(
	t *testing.T,
	registry *tools.ToolRegistry,
	toolName string,
	workspace string,
	path string,
	exists bool,
) {
	t.Helper()
	tool, ok := registry.Get(toolName)
	if !ok {
		t.Fatalf("%s is not registered", toolName)
	}
	result := executeAgentFileMutation(t, tool, toolName, workspace, path, exists)
	if result == nil || !result.IsError {
		t.Fatalf("%s protected mutation result = %#v", toolName, result)
	}
	if toolName == "apply_patch" {
		if !strings.Contains(result.ForLLM, "protected") {
			t.Fatalf("apply_patch denial = %q", result.ForLLM)
		}
	} else if !strings.Contains(result.ForLLM, "protected runtime state") {
		t.Fatalf("%s denial = %q", toolName, result.ForLLM)
	}
}

func TestAgentFileMutationPolicyProtectsLauncherRuntimeAndFactories(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	customConfigDir := filepath.Join(workspace, "custom-config")
	customConfig := filepath.Join(customConfigDir, "config.json")
	launcherConfig := filepath.Join(customConfigDir, "launcher-config.json")
	database := filepath.Join(home, "launcher-auth.db")
	wal := database + "-wal"
	shm := database + "-shm"
	archive := filepath.Join(
		customConfigDir,
		"legacy-json",
		"launcher-auth-v1",
		"launcher-config.json",
	)
	for _, path := range []string{customConfig, launcherConfig, database, shm, archive} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archiveHardlink := filepath.Join(workspace, "archived-credentials-hardlink.json")
	archiveHardlinkAvailable := os.Link(archive, archiveHardlink) == nil
	t.Setenv(config.EnvHome, home)
	// Prove WithConfigPath, rather than the environment fallback, owns the
	// archive location used by this runtime.
	t.Setenv(config.EnvConfig, filepath.Join(workspace, "environment-config", "config.json"))

	cfg := agentFileMutationTestConfig(workspace)
	cfg.Tools.AllowWritePaths = []string{
		"^" + regexp.QuoteMeta(database) + "$",
		"^" + regexp.QuoteMeta(wal) + "$",
		"^" + regexp.QuoteMeta(shm) + "$",
	}
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoop(
		cfg,
		messageBus,
		&mockProvider{},
		WithConfigPath(customConfig),
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is missing")
	}

	registries := map[string]*tools.ToolRegistry{"root": agent.Tools}
	// Owner factories must retain the construction-time paths even if the
	// process environment changes before a child is instantiated.
	newHome := filepath.Join(workspace, "later-home")
	t.Setenv(config.EnvHome, newHome)
	t.Setenv(config.EnvConfig, filepath.Join(workspace, "later-config", "config.json"))
	owned, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "runtime-protection-owner",
	}, []string{"write_file", "edit_file", "append_file", "apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	registries["owner"] = owned

	for registryName, registry := range registries {
		for _, target := range []struct {
			path   string
			exists bool
		}{
			{path: database, exists: true},
			{path: wal, exists: false},
			{path: shm, exists: true},
			{path: archive, exists: true},
		} {
			for _, toolName := range []string{
				"write_file", "edit_file", "append_file", "apply_patch",
			} {
				t.Run(registryName+"_"+toolName+"_"+filepath.Base(target.path), func(t *testing.T) {
					requireAgentFileMutationDenied(
						t,
						registry,
						toolName,
						workspace,
						target.path,
						target.exists,
					)
				})
			}
		}
		if archiveHardlinkAvailable {
			for _, toolName := range []string{"write_file", "edit_file", "append_file"} {
				requireAgentFileMutationDenied(
					t, registry, toolName, workspace, archiveHardlink, true,
				)
			}
			patchTool, _ := registry.Get("apply_patch")
			result := executeAgentFileMutation(
				t,
				patchTool,
				"apply_patch",
				workspace,
				archiveHardlink,
				true,
			)
			if result == nil || !result.IsError {
				t.Fatalf("%s apply_patch accepted archive hardlink: %#v", registryName, result)
			}
		}
	}

	for _, path := range []string{database, shm, archive} {
		content, readErr := os.ReadFile(path)
		if readErr != nil || string(content) != "before" {
			t.Fatalf("protected content %q = %q, %v", path, content, readErr)
		}
	}
	if _, statErr := os.Stat(wal); !os.IsNotExist(statErr) {
		t.Fatalf("absent WAL was created: %v", statErr)
	}

	// Neither the application config nor its settings-only launcher sibling is
	// runtime state. Both remain legitimate source/config mutation targets.
	for _, path := range []string{customConfig, launcherConfig} {
		editTool, _ := agent.Tools.Get("edit_file")
		result := executeAgentFileMutation(
			t, editTool, "edit_file", workspace, path, true,
		)
		if result == nil || result.IsError {
			t.Fatalf("active config edit %q = %#v", path, result)
		}
	}

	ordinary := filepath.Join(workspace, "ordinary.go")
	if err := os.WriteFile(ordinary, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patchTool, _ := agent.Tools.Get("apply_patch")
	result := executeAgentFileMutation(
		t, patchTool, "apply_patch", workspace, ordinary, true,
	)
	if result == nil || result.IsError {
		t.Fatalf("ordinary apply_patch = %#v", result)
	}

	// SQLite may delete and recreate WAL files. Volatility must not stale the
	// apply-patch policy, while the recreated sidecar remains protected.
	if err := os.WriteFile(wal, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireAgentFileMutationDenied(
		t, agent.Tools, "apply_patch", workspace, wal, true,
	)
	ordinaryTwo := filepath.Join(workspace, "ordinary-two.go")
	if err := os.WriteFile(ordinaryTwo, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = executeAgentFileMutation(
		t, patchTool, "apply_patch", workspace, ordinaryTwo, true,
	)
	if result == nil || result.IsError {
		t.Fatalf("ordinary patch after WAL creation = %#v", result)
	}

	// A successful registry reload must reuse the loop-frozen roots rather than
	// adopting the config-path environment value changed above. Restore home so
	// apply_patch's separately owned authenticated transaction root remains in
	// its admitted construction namespace for this generation.
	t.Setenv(config.EnvHome, home)
	reloadedConfig := agentFileMutationTestConfig(workspace)
	reloadedConfig.Tools.AllowWritePaths = append(
		[]string(nil), cfg.Tools.AllowWritePaths...,
	)
	if err := loop.ReloadProviderAndConfig(
		context.Background(), &mockProvider{}, reloadedConfig,
	); err != nil {
		t.Fatal(err)
	}
	reloadedAgent := loop.registry.GetDefaultAgent()
	if reloadedAgent == nil {
		t.Fatal("reloaded default agent is missing")
	}
	for _, toolName := range []string{
		"write_file", "edit_file", "append_file", "apply_patch",
	} {
		requireAgentFileMutationDenied(
			t,
			reloadedAgent.Tools,
			toolName,
			workspace,
			database,
			true,
		)
	}

	newEnvironmentDatabase := filepath.Join(newHome, "launcher-auth.db")
	if err := os.MkdirAll(filepath.Dir(newEnvironmentDatabase), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTool, _ := reloadedAgent.Tools.Get("write_file")
	result = executeAgentFileMutation(
		t,
		writeTool,
		"write_file",
		workspace,
		newEnvironmentDatabase,
		false,
	)
	if result == nil || result.IsError {
		t.Fatalf("reload rebound frozen roots to changed environment: %#v", result)
	}
}

func TestAgentFileMutationPolicyProtectsChannelAndWorkspaceSQLiteAliases(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))

	wecomDatabase := filepath.Join(home, "channels", "wecom", "reqid-store.db")
	wecomLock := filepath.Join(wecomDatabase+".locks", "store.lock")
	wecomSource := filepath.Join(home, "wecom", "reqid-store.json")
	wecomArchive := filepath.Join(
		home,
		"legacy-json",
		"wecom-reqid-v1",
		"wecom",
		"reqid-store.json",
	)
	weixinRoot := filepath.Join(home, "channels", "weixin")
	weixinDatabase := filepath.Join(weixinRoot, "state.db")
	weixinLock := filepath.Join(weixinDatabase+".locks", "store.lock")
	weixinSource := filepath.Join(weixinRoot, "sync", "0123456789abcdef.json")
	weixinArchive := filepath.Join(
		weixinRoot,
		"legacy-json",
		"weixin-state-v1",
		"sync",
		"0123456789abcdef.json",
	)
	runtimeDatabase := filepath.Join(workspace, "state", "runtime.db")
	runtimeLock := filepath.Join(runtimeDatabase+".locks", "store.lock")
	runtimeSource := filepath.Join(workspace, "state.json")
	runtimeArchive := filepath.Join(
		workspace,
		"state",
		"legacy-json",
		"runtime-state-v1",
		"state.json",
	)

	directTargets := []string{
		wecomDatabase,
		wecomDatabase + "-wal",
		wecomDatabase + "-shm",
		wecomLock,
		wecomSource,
		wecomArchive,
		weixinDatabase,
		weixinDatabase + "-wal",
		weixinDatabase + "-shm",
		weixinLock,
		weixinSource,
		weixinArchive,
		runtimeDatabase,
		runtimeDatabase + "-wal",
		runtimeDatabase + "-shm",
		runtimeLock,
		runtimeSource,
		runtimeArchive,
	}
	for _, path := range directTargets {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg := agentFileMutationTestConfig(workspace)
	// Exact outside-workspace authority makes each denial prove runtime-state
	// protection without admitting apply_patch's authenticated journal root.
	for _, target := range directTargets {
		if !strings.HasPrefix(target, workspace+string(filepath.Separator)) {
			cfg.Tools.AllowWritePaths = append(
				cfg.Tools.AllowWritePaths,
				"^"+regexp.QuoteMeta(target)+"$",
			)
		}
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()

	for _, target := range directTargets {
		for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
			t.Run(toolName+"_"+strings.ReplaceAll(target, string(filepath.Separator), "_"), func(t *testing.T) {
				requireAgentFileMutationDenied(
					t,
					agent.Tools,
					toolName,
					workspace,
					target,
					true,
				)
			})
		}
	}

	aliasSources := map[string]string{
		"wecom-database":   wecomDatabase,
		"wecom-lock":       wecomLock,
		"wecom-archive":    wecomArchive,
		"weixin-database":  weixinDatabase,
		"weixin-lock":      weixinLock,
		"weixin-archive":   weixinArchive,
		"runtime-database": runtimeDatabase,
		"runtime-lock":     runtimeLock,
		"runtime-source":   runtimeSource,
		"runtime-archive":  runtimeArchive,
	}
	for label, source := range aliasSources {
		for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
			alias := filepath.Join(workspace, label+"-"+toolName+".alias")
			if err := os.Link(source, alias); err != nil {
				t.Logf("hardlink aliases unavailable for %s: %v", label, err)
				break
			}
			if toolName == "apply_patch" {
				tool, _ := agent.Tools.Get(toolName)
				result := executeAgentFileMutation(
					t,
					tool,
					toolName,
					workspace,
					alias,
					true,
				)
				if result == nil || !result.IsError {
					t.Fatalf("%s accepted hardlink alias %q: %#v", toolName, alias, result)
				}
				continue
			}
			requireAgentFileMutationDenied(
				t,
				agent.Tools,
				toolName,
				workspace,
				alias,
				true,
			)
		}
	}

	for _, path := range directTargets {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != "before" {
			t.Fatalf("protected state %q = %q, %v", path, content, err)
		}
	}
}

func TestAgentFileMutationPolicyProtectsIdentitySQLiteStores(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, configPath)

	targets := make([]string, 0, 16)
	targets = append(targets,
		filepath.Join(home, "auth.json"),
		filepath.Join(home, "model_catalogs.json"),
		filepath.Join(home, "tool_adaptation_state.json"),
	)
	for _, databaseName := range []string{
		"auth.db", "model-catalogs.db", "tool-adaptation.db",
	} {
		database := filepath.Join(home, databaseName)
		targets = append(targets, database, database+"-wal", database+"-shm")
	}
	targets = append(targets,
		filepath.Join(home, "auth.db.locks", "store.lock"),
		filepath.Join(home, "legacy-json", "auth-v1", "auth.json"),
		filepath.Join(home, "legacy-json", "model-catalogs-v1", "model_catalogs.json"),
		filepath.Join(
			home,
			"legacy-json",
			"tool-adaptation-v1",
			"tool_adaptation_state.json",
		),
	)
	cfg := agentFileMutationTestConfig(workspace)
	for _, target := range targets {
		cfg.Tools.AllowWritePaths = append(
			cfg.Tools.AllowWritePaths,
			"^"+regexp.QuoteMeta(target)+"$",
		)
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()
	// Construct before installing fixture bytes because agent initialization
	// performs a best-effort read of the real tool-adaptation database.
	for _, target := range targets {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, target := range targets {
		for _, toolName := range []string{
			"write_file", "edit_file", "append_file", "apply_patch",
		} {
			requireAgentFileMutationDenied(
				t,
				agent.Tools,
				toolName,
				workspace,
				target,
				true,
			)
		}
		content, err := os.ReadFile(target)
		if err != nil || string(content) != "before" {
			t.Fatalf("protected identity state %q = %q, %v", target, content, err)
		}
	}

	archive := filepath.Join(home, "legacy-json", "auth-v1", "auth.json")
	hardlink := filepath.Join(workspace, "auth-archive-hardlink.json")
	if err := os.Link(archive, hardlink); err == nil {
		for _, toolName := range []string{
			"write_file", "edit_file", "append_file",
		} {
			requireAgentFileMutationDenied(
				t,
				agent.Tools,
				toolName,
				workspace,
				hardlink,
				true,
			)
		}
		patchTool, _ := agent.Tools.Get("apply_patch")
		result := executeAgentFileMutation(
			t,
			patchTool,
			"apply_patch",
			workspace,
			hardlink,
			true,
		)
		if result == nil || !result.IsError {
			t.Fatalf("apply_patch accepted auth archive hardlink: %#v", result)
		}
	}
}

func TestAgentFileMutationPolicyProtectsSessionDatabaseAndArchive(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	archive := filepath.Join(workspace, "legacy-json", "sessions-v1", "retained.json")
	if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := agentFileMutationTestConfig(workspace)
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	t.Cleanup(func() { agent.Close() })
	owned, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "session-protection-owner",
	}, []string{"write_file", "edit_file", "append_file", "apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()

	database := filepath.Join(workspace, "sessions", "sessions.db")
	targets := []struct {
		path   string
		exists bool
	}{
		{path: database, exists: true},
		{path: database + "-wal", exists: pathExists(database + "-wal")},
		{path: database + "-shm", exists: pathExists(database + "-shm")},
		{path: archive, exists: true},
	}
	for registryName, registry := range map[string]*tools.ToolRegistry{
		"root": agent.Tools, "owner": owned,
	} {
		for _, target := range targets {
			for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
				t.Run(registryName+"_"+toolName+"_"+filepath.Base(target.path), func(t *testing.T) {
					requireAgentFileMutationDenied(
						t, registry, toolName, workspace, target.path, target.exists,
					)
				})
			}
		}
	}

	ordinary := filepath.Join(workspace, "ordinary-session-policy.txt")
	if err := os.WriteFile(ordinary, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patchTool, _ := agent.Tools.Get("apply_patch")
	if result := executeAgentFileMutation(
		t, patchTool, "apply_patch", workspace, ordinary, true,
	); result == nil || result.IsError {
		t.Fatalf("ordinary patch after session protection = %#v", result)
	}
}

func TestAgentFileMutationPolicyProtectsCronDatabaseAndLegacyState(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))

	cfg := agentFileMutationTestConfig(workspace)
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	t.Cleanup(func() { agent.Close() })
	owned, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "cron-protection-owner",
	}, []string{"write_file", "edit_file", "append_file", "apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()

	cronRoot := filepath.Join(workspace, "cron")
	if pathExists(cronRoot) {
		t.Fatal("cron namespace unexpectedly exists before the protection check")
	}
	for registryName, registry := range map[string]*tools.ToolRegistry{
		"root": agent.Tools, "owner": owned,
	} {
		for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
			t.Run(registryName+"_"+toolName+"_absent_cron_root", func(t *testing.T) {
				requireAgentFileMutationDenied(
					t, registry, toolName, workspace, cronRoot, false,
				)
			})
		}
	}

	database := filepath.Join(cronRoot, "jobs.db")
	archive := filepath.Join(cronRoot, "legacy-json", "cron-jobs-v1", "jobs.json")
	targets := []string{
		database,
		database + "-wal",
		database + "-shm",
		filepath.Join(cronRoot, "jobs.json"),
		archive,
	}
	for _, target := range targets {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for registryName, registry := range map[string]*tools.ToolRegistry{
		"root": agent.Tools, "owner": owned,
	} {
		for _, target := range targets {
			for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
				t.Run(registryName+"_"+toolName+"_"+filepath.Base(target), func(t *testing.T) {
					requireAgentFileMutationDenied(
						t, registry, toolName, workspace, target, true,
					)
				})
			}
			content, err := os.ReadFile(target)
			if err != nil || string(content) != "before" {
				t.Fatalf("protected cron state %q = %q, %v", target, content, err)
			}
		}
	}

	hardlink := filepath.Join(workspace, "cron-archive-hardlink.json")
	if err := os.Link(archive, hardlink); err == nil {
		for registryName, registry := range map[string]*tools.ToolRegistry{
			"root": agent.Tools, "owner": owned,
		} {
			for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
				t.Run(registryName+"_"+toolName+"_archive_hardlink", func(t *testing.T) {
					if toolName == "apply_patch" {
						tool, _ := registry.Get(toolName)
						result := executeAgentFileMutation(
							t, tool, toolName, workspace, hardlink, true,
						)
						if result == nil || !result.IsError {
							t.Fatalf("apply_patch accepted cron archive hardlink: %#v", result)
						}
						return
					}
					requireAgentFileMutationDenied(
						t, registry, toolName, workspace, hardlink, true,
					)
				})
			}
		}
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestAppendAgentWorkspaceSQLiteProtectedRootsNilConfigPassesThrough(t *testing.T) {
	existing := []string{"already-protected"}
	protected, err := appendAgentWorkspaceSQLiteProtectedRoots(existing, nil)
	if err != nil || len(protected) != 1 || protected[0] != existing[0] ||
		&protected[0] != &existing[0] {
		t.Fatalf("nil-config protected roots=%#v err=%v", protected, err)
	}
}

func TestAgentFileMutationPolicyProtectsLocalCIEvidenceSQLiteStore(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))

	cfg := agentFileMutationTestConfig(workspace)
	cfg.Events.Ingress.Enabled = true
	cfg.Events.Ingress.DatabasePath = filepath.Join("eventing", "events.db")
	evidenceRoot := filepath.Join(
		workspace,
		"eventing",
		"pr-workspace-local-ci",
		"evidence",
	)
	database := filepath.Join(evidenceRoot, "cache.db")
	targets := []string{
		database,
		database + "-wal",
		database + "-shm",
		filepath.Join(evidenceRoot, "cache", "aa", strings.Repeat("a", 64)+".json"),
		filepath.Join(
			evidenceRoot,
			"legacy-json",
			"local-ci-cache-v1",
			"cache",
			"aa",
			strings.Repeat("a", 64)+".json",
		),
	}
	for _, target := range targets {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.Tools.AllowWritePaths = append(
			cfg.Tools.AllowWritePaths,
			"^"+regexp.QuoteMeta(target)+"$",
		)
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()

	for _, target := range targets {
		for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
			requireAgentFileMutationDenied(
				t,
				agent.Tools,
				toolName,
				workspace,
				target,
				true,
			)
		}
		content, err := os.ReadFile(target)
		if err != nil || string(content) != "before" {
			t.Fatalf("protected local CI state %q = %q, %v", target, content, err)
		}
	}
	hardlink := filepath.Join(workspace, "local-ci-cache-hardlink.db")
	if err := os.Link(database, hardlink); err == nil {
		for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
			if toolName == "apply_patch" {
				tool, _ := agent.Tools.Get(toolName)
				result := executeAgentFileMutation(
					t,
					tool,
					toolName,
					workspace,
					hardlink,
					true,
				)
				if result == nil || !result.IsError {
					t.Fatalf("apply_patch accepted local CI database hardlink: %#v", result)
				}
				continue
			}
			requireAgentFileMutationDenied(
				t,
				agent.Tools,
				toolName,
				workspace,
				hardlink,
				true,
			)
		}
	}
}

func TestAgentFileMutationPolicyProtectsRepositorySQLiteStores(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))

	targets := []string{
		filepath.Join(workspace, "repository_reviews", "repository-reviews.db"),
		filepath.Join(workspace, "repository_reviews", "repository-reviews.db-wal"),
		filepath.Join(workspace, "repository_reviews", "legacy-json", "repository-reviews-v1", "repo_legacy.json"),
		filepath.Join(workspace, "repository_evaluations", "evaluations.db"),
		filepath.Join(workspace, "repository_evaluations", "evaluations.db-shm"),
		filepath.Join(
			workspace,
			"repository_evaluations",
			"legacy-json",
			"repository-evaluations-v1",
			"evaluation_legacy.json",
		),
	}
	cfg := agentFileMutationTestConfig(workspace)
	for _, target := range targets {
		cfg.Tools.AllowWritePaths = append(cfg.Tools.AllowWritePaths, "^"+regexp.QuoteMeta(target)+"$")
	}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()
	for _, target := range targets {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
			requireAgentFileMutationDenied(t, agent.Tools, toolName, workspace, target, true)
		}
		content, err := os.ReadFile(target)
		if err != nil || string(content) != "before" {
			t.Fatalf("protected repository state %q=%q err=%v", target, content, err)
		}
	}
}

func TestAgentFileMutationPolicyHomeInsideWorkspaceOmitsUnsafeApplyPatch(t *testing.T) {
	workspace := t.TempDir()
	home := filepath.Join(workspace, ".picoclaw")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(home, "launcher-auth.db")
	if err := os.WriteFile(database, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(workspace, "config.json"))
	cfg := agentFileMutationTestConfig(workspace)
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()

	if _, ok := agent.Tools.Get("apply_patch"); ok {
		t.Fatal("apply_patch retained an authenticated state root inside workspace authority")
	}
	for _, toolName := range []string{"write_file", "edit_file", "append_file"} {
		requireAgentFileMutationDenied(
			t, agent.Tools, toolName, workspace, database, true,
		)
	}
	content, err := os.ReadFile(database)
	if err != nil || string(content) != "before" {
		t.Fatalf("home-inside-workspace database = %q, %v", content, err)
	}
}

func TestAgentFileMutationPolicyProtectsAccountRouterStateAndHardlinks(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	database := filepath.Join(workspace, "state", "account-router.db")
	lockFile := filepath.Join(database+".locks", "store.lock")
	archive := filepath.Join(
		workspace, "state", "legacy-json", "account-router-v1", "account_router_state.json",
	)
	legacy := filepath.Join(workspace, "account_router_state.json")
	sidecar := legacy + ".auth-invalidation.0123456789abcdef0123456789abcdef"
	targets := []string{database, database + "-wal", database + "-shm", lockFile, archive, legacy, sidecar}
	for _, path := range targets {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := agentFileMutationTestConfig(workspace)
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()
	for _, target := range targets {
		for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
			requireAgentFileMutationDenied(t, agent.Tools, toolName, workspace, target, true)
		}
	}
	for label, source := range map[string]string{
		"database": database,
		"lock":     lockFile,
		"archive":  archive,
		"sidecar":  sidecar,
	} {
		for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
			alias := filepath.Join(workspace, label+"-"+toolName+".alias")
			if err := os.Link(source, alias); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			if toolName == "apply_patch" {
				tool, _ := agent.Tools.Get(toolName)
				result := executeAgentFileMutation(t, tool, toolName, workspace, alias, true)
				if result == nil || !result.IsError {
					t.Fatalf("apply_patch accepted %s hardlink: %#v", label, result)
				}
				continue
			}
			requireAgentFileMutationDenied(t, agent.Tools, toolName, workspace, alias, true)
		}
	}
}

func TestAgentRuntimeFileMutationProtectedRootsUseEnvironmentFallback(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	configPath := filepath.Join(root, "custom", "config.json")
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, configPath)
	roots, err := agentRuntimeFileMutationProtectedRoots("")
	if err != nil {
		t.Fatal(err)
	}
	wecomDatabase := filepath.Join(home, "channels", "wecom", "reqid-store.db")
	weixinDatabase := filepath.Join(home, "channels", "weixin", "state.db")
	wecomArchiveRoot := filepath.Join(home, "legacy-json", "wecom-reqid-v1")
	weixinArchiveRoot := filepath.Join(home, "channels", "weixin", "legacy-json", "weixin-state-v1")
	want := make([]string, 0, 40)
	want = append(want,
		filepath.Join(home, "auth.json"),
		filepath.Join(home, "model_catalogs.json"),
		filepath.Join(home, "tool_adaptation_state.json"),
	)
	for _, databaseName := range []string{
		"launcher-auth.db", "auth.db", "model-catalogs.db", "tool-adaptation.db",
	} {
		identityDatabase := filepath.Join(home, databaseName)
		want = append(
			want,
			identityDatabase,
			identityDatabase+"-wal",
			identityDatabase+"-shm",
		)
	}
	authLockDirectory := filepath.Join(home, "auth.db.locks")
	want = append(want,
		authLockDirectory,
		filepath.Join(authLockDirectory, "store.lock"),
		filepath.Join(filepath.Dir(configPath), "legacy-json"),
		filepath.Join(
			filepath.Dir(configPath),
			"legacy-json",
			"launcher-auth-v1",
			"launcher-config.json",
		),
		filepath.Join(home, "legacy-json"),
		filepath.Join(home, "legacy-json", "auth-v1", "auth.json"),
		filepath.Join(home, "legacy-json", "model-catalogs-v1", "model_catalogs.json"),
		filepath.Join(
			home,
			"legacy-json",
			"tool-adaptation-v1",
			"tool_adaptation_state.json",
		),
		wecomDatabase,
		wecomDatabase+"-wal",
		wecomDatabase+"-shm",
		wecomDatabase+".locks",
		filepath.Join(wecomDatabase+".locks", "store.lock"),
		filepath.Join(home, "wecom", "reqid-store.json"),
		wecomArchiveRoot,
		filepath.Join(wecomArchiveRoot, "wecom", "reqid-store.json"),
		weixinDatabase,
		weixinDatabase+"-wal",
		weixinDatabase+"-shm",
		weixinDatabase+".locks",
		filepath.Join(weixinDatabase+".locks", "store.lock"),
		filepath.Join(home, "channels", "weixin", "sync"),
		filepath.Join(home, "channels", "weixin", "context-tokens"),
		weixinArchiveRoot,
	)
	var defaultConfig *config.Config
	gitRoot := defaultConfig.GitWorkspaceRootPath()
	gitInventory := filepath.Join(gitRoot, "inventory.db")
	checkpointRoot := filepath.Join(gitRoot, ".pr-workspace-implementation", "active")
	checkpointDatabase := filepath.Join(checkpointRoot, "checkpoints.db")
	want = append(want,
		gitInventory, gitInventory+"-wal", gitInventory+"-shm",
		checkpointDatabase, checkpointDatabase+"-wal", checkpointDatabase+"-shm",
		filepath.Join(gitRoot, "inventory.json"),
		filepath.Join(gitRoot, "inventory.lock"),
		filepath.Join(gitRoot, ".locks"),
		filepath.Join(gitRoot, "legacy-json"),
		filepath.Join(gitRoot, "legacy-json", "git-workspaces-v1", "inventory.json"),
		checkpointRoot,
		filepath.Join(checkpointRoot, "legacy-json"),
	)
	if len(roots) != len(want) {
		t.Fatalf("protected roots = %#v, want %#v", roots, want)
	}
	for index := range want {
		if roots[index] != want[index] {
			t.Fatalf("protected root %d = %q, want %q", index, roots[index], want[index])
		}
	}
	roots[0] = "mutated"
	again, err := agentRuntimeFileMutationProtectedRoots("")
	if err != nil || again[0] != filepath.Join(home, "auth.json") {
		t.Fatalf("protected roots retained caller mutation: %#v, %v", again, err)
	}
}

func TestAgentRuntimeFileMutationProtectedRootsAvoidPreCatalogWeixinEnumeration(t *testing.T) {
	home := t.TempDir()
	weixinRoot := filepath.Join(home, "channels", "weixin")
	if err := os.MkdirAll(weixinRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(weixinRoot, "sync")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	t.Setenv(config.EnvHome, home)
	roots, err := agentRuntimeFileMutationProtectedRoots(filepath.Join(home, "config.json"))
	if err != nil || len(roots) == 0 {
		t.Fatalf("lexical Weixin roots = %#v, %v", roots, err)
	}
	workspace := t.TempDir()
	if catalog, catalogErr := agentFileMutationIdentityCatalog(
		workspace,
		&config.Config{},
		roots,
	); catalogErr == nil || catalog != nil || !strings.Contains(catalogErr.Error(), "unsafe") {
		t.Fatalf("symlinked Weixin catalog = %#v, %v", catalog, catalogErr)
	}
}

func TestAgentEvolutionFileMutationProtectedRootsCoverDatabaseAndLegacySources(t *testing.T) {
	workspace := t.TempDir()
	custom := filepath.Join(t.TempDir(), "custom-evolution")
	for _, test := range []struct {
		name, stateDir, root string
	}{
		{name: "default", root: filepath.Join(workspace, "state", "evolution")},
		{name: "custom", stateDir: custom, root: custom},
	} {
		t.Run(test.name, func(t *testing.T) {
			roots, err := agentEvolutionFileMutationProtectedRoots(workspace, test.stateDir)
			if err != nil {
				t.Fatal(err)
			}
			database := filepath.Join(test.root, "evolution.db")
			want := []string{
				database, database + "-wal", database + "-shm",
				filepath.Join(test.root, "legacy-json"),
				filepath.Join(test.root, "learning-records.jsonl"),
				filepath.Join(test.root, "task-records.jsonl"),
				filepath.Join(test.root, "pattern-records.jsonl"),
				filepath.Join(test.root, "skill-drafts.json"),
				filepath.Join(test.root, "profiles"),
				filepath.Join(test.root, "backups"),
			}
			if len(roots) != len(want) {
				t.Fatalf("roots = %#v, want %#v", roots, want)
			}
			for index := range want {
				if roots[index] != want[index] {
					t.Fatalf("root %d = %q, want %q", index, roots[index], want[index])
				}
			}
		})
	}
}

func TestAgentFileMutationPolicyProtectsConfiguredEvolutionDatabase(t *testing.T) {
	workspace := t.TempDir()
	stateDir := filepath.Join(workspace, "custom-evolution")
	database := filepath.Join(stateDir, "evolution.db")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := agentFileMutationTestConfig(workspace)
	cfg.Evolution.StateDir = stateDir
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()
	for _, toolName := range []string{"write_file", "edit_file", "append_file"} {
		requireAgentFileMutationDenied(t, agent.Tools, toolName, workspace, database, true)
	}
	content, err := os.ReadFile(database)
	if err != nil || string(content) != "before" {
		t.Fatalf("protected evolution database = %q, %v", content, err)
	}
}

func TestAgentFileMutationPolicyProtectsGitInventoryAndCandidateCheckpoints(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	gitRoot := filepath.Join(workspace, "custom-git-state")
	t.Setenv(config.EnvHome, home)
	cfg := agentFileMutationTestConfig(workspace)
	cfg.GitWorkspaces.RootDir = gitRoot
	inventory := filepath.Join(gitRoot, "inventory.db")
	activeInventory := filepath.Join(gitRoot, "inventory.json")
	checkpointRoot := filepath.Join(gitRoot, ".pr-workspace-implementation", "active")
	checkpoint := filepath.Join(checkpointRoot, "checkpoints.db")
	inventoryArchive := filepath.Join(gitRoot, "legacy-json", "git-workspaces-v1", "inventory.json")
	activeCheckpoint := filepath.Join(checkpointRoot, "active-checkpoint.json")
	checkpointArchive := filepath.Join(
		checkpointRoot,
		"legacy-json",
		"pr-workspace-checkpoints-v1",
		"archived-checkpoint.json",
	)
	migratedCheckpoint := filepath.Join(
		checkpointRoot,
		"legacy-json",
		"pr-workspace-checkpoints-v1",
		filepath.Base(activeCheckpoint),
	)
	for _, target := range []string{inventoryArchive, activeCheckpoint, checkpointArchive} {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	messageBus := bus.NewMessageBus()
	loop := NewAgentLoop(cfg, messageBus, &mockProvider{})
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is missing")
	}

	registries := map[string]*tools.ToolRegistry{"root": agent.Tools}
	owned, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "git-state-protection-owner",
	}, []string{"write_file", "edit_file", "append_file", "apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	registries["owner"] = owned
	requireHardlinkDenied := func(
		t *testing.T,
		registry *tools.ToolRegistry,
		toolName string,
		path string,
	) {
		t.Helper()
		if toolName != "apply_patch" {
			requireAgentFileMutationDenied(t, registry, toolName, workspace, path, true)
			return
		}
		tool, ok := registry.Get(toolName)
		if !ok {
			t.Fatalf("%s is not registered", toolName)
		}
		result := executeAgentFileMutation(t, tool, toolName, workspace, path, true)
		if result == nil || !result.IsError {
			t.Fatalf("apply_patch accepted hardlink: %#v", result)
		}
	}

	for _, target := range []struct {
		path   string
		exists bool
	}{
		{inventory, true},
		{inventory + "-wal", false},
		{inventory + "-shm", false},
		{activeInventory, false},
		{checkpoint, false},
		{checkpoint + "-wal", false},
		{checkpoint + "-shm", false},
		{filepath.Join(checkpointRoot, "future-checkpoint.json"), false},
		{migratedCheckpoint, false},
		{filepath.Join(gitRoot, "inventory.lock"), false},
		{filepath.Join(gitRoot, ".locks", "pinned-operation-example.lock"), false},
		{inventoryArchive, true},
		{activeCheckpoint, true},
		{checkpointArchive, true},
	} {
		for registryName, registry := range registries {
			for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
				t.Run(registryName+"_"+toolName+"_"+filepath.Base(target.path), func(t *testing.T) {
					requireAgentFileMutationDenied(
						t, registry, toolName, workspace, target.path, target.exists,
					)
				})
			}
		}
	}
	for _, target := range []string{inventoryArchive, activeCheckpoint, checkpointArchive} {
		if content, err := os.ReadFile(target); err != nil || string(content) != "before" {
			t.Fatalf("protected Git state %q = %q, %v", target, content, err)
		}
	}

	for label, target := range map[string]string{
		"inventory_archive":   inventoryArchive,
		"active_checkpoint":   activeCheckpoint,
		"archived_checkpoint": checkpointArchive,
	} {
		hardlink := filepath.Join(workspace, label+"-hardlink.json")
		if err := os.Link(target, hardlink); err != nil {
			continue
		}
		for registryName, registry := range registries {
			for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
				t.Run(registryName+"_"+toolName+"_"+label+"_hardlink", func(t *testing.T) {
					requireHardlinkDenied(t, registry, toolName, hardlink)
				})
			}
		}
	}

	if err := os.Rename(activeCheckpoint, migratedCheckpoint); err != nil {
		t.Fatal(err)
	}
	migratedHardlink := filepath.Join(workspace, "migrated-checkpoint-hardlink.json")
	if err := os.Link(migratedCheckpoint, migratedHardlink); err == nil {
		for registryName, registry := range registries {
			for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
				t.Run(registryName+"_"+toolName+"_migrated_checkpoint_hardlink", func(t *testing.T) {
					requireHardlinkDenied(t, registry, toolName, migratedHardlink)
				})
			}
		}
	}
}

func TestAgentGitWorkspaceProtectedRootsRejectUnsafeCheckpointState(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "checkpoint root file",
			setup: func(t *testing.T, checkpointRoot string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(checkpointRoot), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(checkpointRoot, []byte("unsafe"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "selected source symlink",
			setup: func(t *testing.T, checkpointRoot string) {
				t.Helper()
				if err := os.MkdirAll(checkpointRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(checkpointRoot, "unsafe.json")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
		{
			name: "selected source directory",
			setup: func(t *testing.T, checkpointRoot string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(checkpointRoot, "unsafe.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "archive root file",
			setup: func(t *testing.T, checkpointRoot string) {
				t.Helper()
				if err := os.MkdirAll(checkpointRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(
					filepath.Join(checkpointRoot, "legacy-json"),
					[]byte("unsafe"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "archive symlink",
			setup: func(t *testing.T, checkpointRoot string) {
				t.Helper()
				if err := os.MkdirAll(checkpointRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(checkpointRoot, "legacy-json")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			gitRoot := filepath.Join(t.TempDir(), "git-state")
			checkpointRoot := filepath.Join(gitRoot, ".pr-workspace-implementation", "active")
			test.setup(t, checkpointRoot)
			cfg := agentFileMutationTestConfig(t.TempDir())
			cfg.GitWorkspaces.RootDir = gitRoot
			roots, err := agentGitWorkspaceFileMutationProtectedRoots(cfg)
			if err == nil || roots != nil || !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("unsafe checkpoint roots = %#v, %v", roots, err)
			}
		})
	}
}

func TestAgentRegistryRefreshesGitRuntimeRootsForReloadGeneration(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	oldGitRoot := filepath.Join(root, "old-git-workspaces")
	newGitRoot := filepath.Join(root, "new-git-workspaces")
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	oldConfig := agentFileMutationTestConfig(workspace)
	oldConfig.GitWorkspaces.RootDir = oldGitRoot
	newConfig := agentFileMutationTestConfig(workspace)
	newConfig.GitWorkspaces.RootDir = newGitRoot
	newConfig.Tools.AllowWritePaths = []string{
		"^" + regexp.QuoteMeta(oldGitRoot) + "(?:" + regexp.QuoteMeta(string(os.PathSeparator)) + "|$)",
		"^" + regexp.QuoteMeta(newGitRoot) + "(?:" + regexp.QuoteMeta(string(os.PathSeparator)) + "|$)",
	}
	oldRoots, err := agentRuntimeFileMutationProtectedRoots("", oldConfig)
	if err != nil {
		t.Fatal(err)
	}
	targets := []string{
		filepath.Join(oldGitRoot, agentGitInventoryDatabase),
		filepath.Join(newGitRoot, agentGitInventoryDatabase),
		filepath.Join(
			newGitRoot,
			".pr-workspace-implementation",
			"active",
			"future-checkpoint.json",
		),
	}
	for _, target := range targets {
		newConfig.Tools.AllowWritePaths = append(
			newConfig.Tools.AllowWritePaths,
			"^"+regexp.QuoteMeta(target)+"$",
		)
	}
	for _, target := range targets[:2] {
		if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(target, []byte("before"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	registry := newAgentRegistryWithRuntimePolicies(
		newConfig,
		&mockProvider{},
		isolation.NewExecutionPolicy(config.IsolationConfig{}),
		logger.DiagnosticPolicy{},
		nil,
		oldRoots,
	)
	t.Cleanup(registry.Close)
	agent := registry.GetDefaultAgent()
	if agent == nil || agent.preparedFileMutationPolicy == nil {
		t.Fatal("reload generation has no prepared mutation policy")
	}
	owner, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "git-root-reload-owner",
	}, []string{"write_file", "edit_file", "append_file", "apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	for registryName, toolRegistry := range map[string]*tools.ToolRegistry{
		"root": agent.Tools, "owner": owner,
	} {
		for _, target := range targets {
			exists := target != targets[2]
			for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
				t.Run(registryName+"_"+toolName+"_"+filepath.Base(target), func(t *testing.T) {
					requireAgentFileMutationDenied(
						t, toolRegistry, toolName, workspace, target, exists,
					)
				})
			}
		}
	}
	hardlink := filepath.Join(workspace, "new-inventory-hardlink.db")
	if err := os.Link(targets[1], hardlink); err == nil {
		for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
			requireAgentFileMutationDenied(t, agent.Tools, toolName, workspace, hardlink, true)
		}
	}
	for _, target := range targets {
		protected, protectErr := agent.preparedFileMutationPolicy.ProtectsPath(target)
		if protectErr != nil || !protected {
			t.Fatalf(
				"reload prepared policy target %q protected=%t err=%v",
				filepath.Base(target),
				protected,
				protectErr,
			)
		}
	}
}

func TestAgentCheckpointRetainedStateEnumerationBoundsAndModes(t *testing.T) {
	if files, err := agentCheckpointRetainedStateFilesBounded(
		"unused", "unused", 0, 1, 1, 1,
	); err == nil || files != nil || !strings.Contains(err.Error(), "bounds") {
		t.Fatalf("invalid checkpoint enumeration bounds = %#v, %v", files, err)
	}
	newRoots := func(t *testing.T) (string, string) {
		t.Helper()
		checkpointRoot := filepath.Join(t.TempDir(), "active")
		if err := os.MkdirAll(checkpointRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		return checkpointRoot, filepath.Join(checkpointRoot, "legacy-json")
	}

	t.Run("exact bounded state", func(t *testing.T) {
		checkpointRoot, archiveRoot := newRoots(t)
		archive := filepath.Join(archiveRoot, "pr-workspace-checkpoints-v1", "retained.json")
		if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archive, []byte("retained\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		files, err := agentCheckpointRetainedStateFilesBounded(
			checkpointRoot, archiveRoot, 1, 1, 2, 1,
		)
		if err != nil || !slices.Contains(files, archive) {
			t.Fatalf("bounded checkpoint state = %#v, %v", files, err)
		}
	})

	for _, test := range []struct {
		name      string
		setup     func(*testing.T, string, string)
		rootLimit int
		sources   int
		entries   int
		depth     int
		want      string
	}{
		{
			name: "active entry limit", rootLimit: 1, sources: 1, entries: 4, depth: 2,
			setup: func(t *testing.T, checkpointRoot, _ string) {
				for _, name := range []string{"one.txt", "two.txt"} {
					if err := os.WriteFile(filepath.Join(checkpointRoot, name), []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "entry limit",
		},
		{
			name: "active source limit", rootLimit: 3, sources: 1, entries: 4, depth: 2,
			setup: func(t *testing.T, checkpointRoot, _ string) {
				for _, name := range []string{"one.json", "two.json"} {
					if err := os.WriteFile(filepath.Join(checkpointRoot, name), []byte("{}"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "source limit",
		},
		{
			name: "archive entry limit", rootLimit: 2, sources: 1, entries: 1, depth: 2,
			setup: func(t *testing.T, _, archiveRoot string) {
				for _, name := range []string{"one", "two"} {
					if err := os.MkdirAll(filepath.Join(archiveRoot, name), 0o700); err != nil {
						t.Fatal(err)
					}
				}
			},
			want: "entry limit",
		},
		{
			name: "archive depth limit", rootLimit: 2, sources: 1, entries: 4, depth: 1,
			setup: func(t *testing.T, _, archiveRoot string) {
				if err := os.MkdirAll(filepath.Join(archiveRoot, "one", "two"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "depth limit",
		},
		{
			name: "public archive directory", rootLimit: 2, sources: 1, entries: 4, depth: 2,
			setup: func(t *testing.T, _, archiveRoot string) {
				if runtime.GOOS == "windows" {
					t.Skip("Windows FileMode permission bits do not represent ACL privacy")
				}
				path := filepath.Join(archiveRoot, "public")
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "unsafe directory",
		},
		{
			name: "nested archive symlink", rootLimit: 2, sources: 1, entries: 4, depth: 2,
			setup: func(t *testing.T, _, archiveRoot string) {
				if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(archiveRoot, "linked")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			want: "unsafe symlink",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkpointRoot, archiveRoot := newRoots(t)
			test.setup(t, checkpointRoot, archiveRoot)
			files, err := agentCheckpointRetainedStateFilesBounded(
				checkpointRoot,
				archiveRoot,
				test.rootLimit,
				test.sources,
				test.entries,
				test.depth,
			)
			if err == nil || files != nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("bounded unsafe checkpoint state = %#v, %v", files, err)
			}
		})
	}
}

func TestAgentFileMutationPolicyFailureBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit removing the process working directory")
	}
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	absoluteStateRoot := t.TempDir()
	absoluteHome := filepath.Join(absoluteStateRoot, "home")
	absoluteWorkspace := filepath.Join(absoluteStateRoot, "workspace")
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
	t.Setenv(config.EnvConfig, "")
	if roots, rootErr := agentRuntimeFileMutationProtectedRoots(""); rootErr == nil || roots != nil {
		t.Fatalf("relative home roots = %#v, %v", roots, rootErr)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("mustAgentRuntimeFileMutationProtectedRoots did not panic")
			}
		}()
		_ = mustAgentRuntimeFileMutationProtectedRoots("")
	}()

	t.Setenv(config.EnvHome, absoluteHome)
	if roots, rootErr := agentRuntimeFileMutationProtectedRoots(
		"relative-config.json",
	); rootErr == nil ||
		roots != nil {
		t.Fatalf("relative config roots = %#v, %v", roots, rootErr)
	}
	if resolved, resolveErr := agentApplyPatchResolveAgainstExistingAncestor(
		"relative-root",
	); resolveErr == nil ||
		resolved != "" {
		t.Fatalf("relative apply-patch root = %q, %v", resolved, resolveErr)
	}
	if !agentApplyPatchTransactionRootOverlapsWorkspace(
		"relative-root",
		originalWorkingDirectory,
	) {
		t.Fatal("invalid transaction root did not fail closed")
	}
	if roots, rootErr := prepareLocalRepairProtectedRoots([]string{"relative-root"}); rootErr == nil || roots != nil {
		t.Fatalf("relative local-repair roots = %#v, %v", roots, rootErr)
	}

	// Workspace-scoped SQLite protection must fail closed at every public
	// construction boundary when a relative workspace cannot be resolved.
	t.Setenv(config.EnvConfig, filepath.Join(originalWorkingDirectory, "config.json"))
	cfg := agentFileMutationTestConfig("relative-workspace")
	if roots, rootErr := appendAgentWorkspaceSQLiteProtectedRoots(nil, cfg); rootErr == nil || roots != nil {
		t.Fatalf("relative workspace SQLite roots = %#v, %v", roots, rootErr)
	}
	executionPolicy := isolation.NewExecutionPolicy(config.IsolationConfig{})
	diagnosticPolicy := logger.DiagnosticPolicy{}
	absoluteDefaults := cfg.Agents.Defaults
	absoluteDefaults.Workspace = absoluteWorkspace
	constructors := map[string]func(){
		"loop": func() {
			_ = newAgentLoop(cfg, nil, &mockProvider{}, executionPolicy, diagnosticPolicy)
		},
		"registry": func() {
			_ = newAgentRegistryWithRuntimePolicies(
				cfg, &mockProvider{}, executionPolicy, diagnosticPolicy, nil, nil,
			)
		},
		"instance": func() {
			instance := newAgentInstanceWithRuntimePolicies(
				nil, &absoluteDefaults, cfg, &mockProvider{}, executionPolicy,
				diagnosticPolicy, nil, nil,
			)
			if instance != nil {
				instance.Close()
			}
		},
	}
	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s accepted unresolved workspace SQLite protection", name)
				}
			}()
			construct()
		})
	}
}

func TestAgentInstanceRejectsInvalidMutationPolicyForEveryFileTool(t *testing.T) {
	for _, toolName := range []string{"edit_file", "append_file", "write_file"} {
		t.Run(toolName, func(t *testing.T) {
			workspace := t.TempDir()
			cfg := agentFileMutationTestConfig(workspace)
			cfg.Tools.EditFile.Enabled = toolName == "edit_file"
			cfg.Tools.AppendFile.Enabled = toolName == "append_file"
			cfg.Tools.WriteFile.Enabled = toolName == "write_file"
			defer func() {
				if recover() == nil {
					t.Fatalf("%s accepted an invalid mutation policy", toolName)
				}
			}()
			instance := newAgentInstanceWithRuntimePolicies(
				nil,
				&cfg.Agents.Defaults,
				cfg,
				&mockProvider{},
				isolation.NewExecutionPolicy(config.IsolationConfig{}),
				logger.DiagnosticPolicy{},
				nil,
				[]string{"invalid\x00root"},
			)
			if instance != nil {
				instance.Close()
			}
		})
	}
}

func TestAgentInstanceRejectsUnavailableApplyPatchTransactionRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot remove the process working directory")
	}
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
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

	cfg := agentFileMutationTestConfig(workspace)
	defer func() {
		if recover() == nil {
			t.Fatal("agent instance accepted an unavailable apply-patch transaction root")
		}
	}()
	instance := newAgentInstanceWithRuntimePolicies(
		nil,
		&cfg.Agents.Defaults,
		cfg,
		&mockProvider{},
		isolation.NewExecutionPolicy(config.IsolationConfig{}),
		logger.DiagnosticPolicy{},
		nil,
		[]string{filepath.Join(originalWorkingDirectory, "launcher-auth.db")},
	)
	if instance != nil {
		instance.Close()
	}
}

func TestWorkflowRuntimeMutationRootsProtectDatabaseAndRecoveryState(t *testing.T) {
	workspace := t.TempDir()
	roots, err := agentWorkflowRuntimeFileMutationProtectedRoots(workspace)
	if err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(workspace, "state", "workflows.db")
	want := []string{
		filepath.Join(workspace, "state"),
		database,
		database + "-wal",
		database + "-shm",
		filepath.Join(workspace, "legacy-json"),
		filepath.Join(workspace, "workflow_runs"),
		filepath.Join(workspace, "workflow_validations"),
		filepath.Join(workspace, "workflow_dev"),
		filepath.Join(workspace, "workflow_state"),
		filepath.Join(workspace, "workflow_state", "mutation.lock"),
		filepath.Join(workspace, "workflow_state", "publish-transaction.json"),
		filepath.Join(workspace, "workflow_state", "template-transaction.json"),
	}
	if len(roots) != len(want) {
		t.Fatalf("roots = %#v", roots)
	}
	for index := range want {
		if roots[index] != want[index] {
			t.Fatalf("root %d = %q, want %q", index, roots[index], want[index])
		}
	}
}

func TestAgentDynamicRuntimeIdentityCatalogIsSharedByRootAndOwnerAfterArchiveRename(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	source := filepath.Join(workspace, "workflow_runs", "wr_fixture", "run.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := agentFileMutationTestConfig(workspace)
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()
	if agent.fileMutationIdentityCatalog == nil || agent.fileMutationIdentityCatalog.Len() == 0 {
		t.Fatal("agent generation did not retain the dynamic identity catalog")
	}
	owned, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "dynamic-identity-owner",
	}, []string{"write_file", "edit_file", "append_file", "apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	alias := filepath.Join(workspace, "runtime-alias.json")
	if err := os.Link(source, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	archive := filepath.Join(
		workspace,
		"legacy-json",
		"workflows-v1",
		"workflow_runs",
		"wr_fixture",
		"run.json",
	)
	if err := os.MkdirAll(filepath.Dir(archive), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, archive); err != nil {
		t.Fatal(err)
	}
	for registryName, registry := range map[string]*tools.ToolRegistry{
		"root":  agent.Tools,
		"owner": owned,
	} {
		for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
			t.Run(registryName+"_"+toolName, func(t *testing.T) {
				requireAgentFileMutationDenied(t, registry, toolName, workspace, alias, true)
			})
		}
	}
	content, readErr := os.ReadFile(archive)
	if readErr != nil || string(content) != "before" {
		t.Fatalf("archived workflow identity = %q, %v", content, readErr)
	}
}

func TestAgentFileMutationIdentityGenerationReusesOneImmutableCatalog(t *testing.T) {
	workspace := t.TempDir()
	source := filepath.Join(workspace, "workflow_runs", "wr_fixture", "run.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	generation := &agentFileMutationIdentityGeneration{}
	roots, err := agentWorkflowRuntimeFileMutationProtectedRoots(workspace)
	if err != nil {
		t.Fatal(err)
	}
	first, err := generation.catalog(workspace, &config.Config{}, roots)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generation.catalog(workspace, &config.Config{}, append([]string(nil), roots...))
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || second != first || first.Len() == 0 {
		t.Fatalf("generation catalogs = %#v / %#v", first, second)
	}
}

func TestAgentRegistrySharesCrossWorkspaceIdentityUnionWithRootOwnerAndLocalRepair(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	configuredWorkspace := filepath.Join(root, "configured-default")
	mainWorkspace := filepath.Join(root, "main")
	namedWorkspace := filepath.Join(root, "named")
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	type fixture struct {
		source  string
		archive string
	}
	fixtures := []fixture{
		{
			source: filepath.Join(configuredWorkspace, agentRepositoryReviewStateDir, "default.json"),
			archive: filepath.Join(
				configuredWorkspace,
				agentRepositoryReviewStateDir,
				"legacy-json",
				"repository-reviews-v1",
				"default.json",
			),
		},
		{
			source: filepath.Join(mainWorkspace, agentRepositoryEvalStateDir, "main.json"),
			archive: filepath.Join(
				mainWorkspace,
				agentRepositoryEvalStateDir,
				"legacy-json",
				"repository-evaluations-v1",
				"main.json",
			),
		},
		{
			source: filepath.Join(namedWorkspace, agentRepositoryReviewStateDir, "named.json"),
			archive: filepath.Join(
				namedWorkspace,
				agentRepositoryReviewStateDir,
				"legacy-json",
				"repository-reviews-v1",
				"named.json",
			),
		},
	}
	for _, fixture := range fixtures {
		if err := os.MkdirAll(filepath.Dir(fixture.source), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture.source, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := agentFileMutationTestConfig(configuredWorkspace)
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true, Workspace: mainWorkspace},
		{ID: "reviewer", Workspace: namedWorkspace},
	}
	registry := NewAgentRegistry(cfg, &mockProvider{})
	t.Cleanup(registry.Close)
	main, mainOK := registry.GetAgent("main")
	named, namedOK := registry.GetAgent("reviewer")
	if !mainOK || !namedOK || main.fileMutationIdentityCatalog == nil ||
		main.fileMutationIdentityCatalog != named.fileMutationIdentityCatalog ||
		main.preparedFileMutationPolicy == nil ||
		main.preparedFileMutationPolicy != named.preparedFileMutationPolicy {
		t.Fatalf("registry catalogs main=%#v named=%#v", main, named)
	}

	agents := []*AgentInstance{main, named}
	aliases := make(map[*AgentInstance][]string, len(agents))
	for _, agent := range agents {
		aliasRoot := filepath.Join(agent.Workspace, "ordinary-aliases")
		if err := os.MkdirAll(aliasRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		for index, fixture := range fixtures {
			alias := filepath.Join(aliasRoot, fmt.Sprintf("legacy-%d.alias", index))
			if err := os.Link(fixture.source, alias); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			aliases[agent] = append(aliases[agent], alias)
		}
	}
	for _, fixture := range fixtures {
		if err := os.MkdirAll(filepath.Dir(fixture.archive), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(fixture.source, fixture.archive); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range agents {
		owner, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
			Scope: tools.ToolOwnerScopeAgent, AgentID: agent.ID + "-owner",
		}, []string{"write_file", "edit_file", "append_file", "apply_patch"})
		if err != nil {
			t.Fatal(err)
		}
		for registryName, toolRegistry := range map[string]*tools.ToolRegistry{
			"root": agent.Tools, "owner": owner,
		} {
			for _, alias := range aliases[agent] {
				for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
					t.Run(agent.ID+"_"+registryName+"_"+toolName+"_"+filepath.Base(alias), func(t *testing.T) {
						requireAgentFileMutationDenied(
							t,
							toolRegistry,
							toolName,
							agent.Workspace,
							alias,
							true,
						)
					})
				}
			}
		}
		owner.Close()
	}

	candidate := controllerRepairFactoryCandidate("account-a", "coding", "openai", "coding")
	main.Model = "coding"
	main.Candidates = []providers.FallbackCandidate{candidate}
	main.Provider = &controllerRepairFactoryProvider{}
	main.MaxIterations = 2
	main.MaxTokens = 512
	main.ConfigurationError = nil
	loop := newControllerRepairFactoryLoop(t, cfg, main)
	runner, err := loop.NewControllerLocalRepairRunner("main", "cross-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if runner.protectedIdentities != main.fileMutationIdentityCatalog ||
		len(runner.protectedRoots) != 0 {
		t.Fatal("prepared local repair lost its shared identity catalog")
	}
	if runner.preparedMutationPolicy != main.preparedFileMutationPolicy {
		t.Fatal("local repair did not retain registry prepared mutation policy")
	}
	if runner.preparedApplyPatchRoots != main.preparedApplyPatchRoots ||
		runner.preparedApplyPatchRoots == nil {
		t.Fatal("local repair did not retain prepared apply_patch volatile roots")
	}
	pin, repairWorkspace, checkout := newLocalRepairTestWorkspace(t)
	repairAlias := filepath.Join(checkout, "cross-workspace-runtime.alias")
	if linkErr := os.Link(fixtures[0].archive, repairAlias); linkErr != nil {
		t.Skipf("hardlinks unavailable for local repair: %v", linkErr)
	}
	guard, err := newLocalRepairPathGuardWithPolicy(
		repairWorkspace,
		pin,
		nil,
		nil,
		runner.preparedMutationPolicy,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.validateMutation(filepath.Base(repairAlias)); err == nil {
		t.Fatal("local repair accepted cross-workspace archived-state hardlink")
	}
	if mixedGuard, mixedErr := newLocalRepairPathGuardWithPolicy(
		repairWorkspace,
		pin,
		[]string{fixtures[0].archive},
		nil,
		runner.preparedMutationPolicy,
	); mixedErr == nil || mixedGuard != nil {
		t.Fatalf("mixed prepared/source guard = %#v, %v", mixedGuard, mixedErr)
	}
	if mixedRunner, mixedErr := NewLocalRepairRunner(LocalRepairRunnerConfig{
		Workspaces:             &localRepairTestAcquirer{},
		Provider:               &localRepairTestProvider{},
		Model:                  "repair-model",
		ProtectedRoots:         []string{fixtures[0].archive},
		PreparedMutationPolicy: runner.preparedMutationPolicy,
	}); mixedErr == nil || mixedRunner != nil {
		t.Fatalf("mixed prepared/source runner = %#v, %v", mixedRunner, mixedErr)
	}
}

func TestAgentRegistryProtectsSiblingWorkspaceSQLiteFromOutsideWriters(t *testing.T) {
	for _, test := range []struct {
		name       string
		restricted bool
	}{
		{name: "unrestricted"},
		{name: "outside-allowlist", restricted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			mainWorkspace := filepath.Join(root, "main")
			siblingWorkspace := filepath.Join(root, "sibling")
			t.Setenv(config.EnvHome, home)
			t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
			cfg := agentFileMutationTestConfig(mainWorkspace)
			cfg.Agents.Defaults.RestrictToWorkspace = test.restricted
			cfg.Agents.List = []config.AgentConfig{
				{ID: "main", Default: true, Workspace: mainWorkspace},
				{ID: "sibling", Workspace: siblingWorkspace},
			}
			siblingDatabase := filepath.Join(siblingWorkspace, "sessions", "sessions.db")
			if test.restricted {
				cfg.Tools.AllowWritePaths = []string{"^" + regexp.QuoteMeta(siblingDatabase) + "$"}
			}
			registry := NewAgentRegistry(cfg, &mockProvider{})
			t.Cleanup(registry.Close)
			main, ok := registry.GetAgent("main")
			if !ok {
				t.Fatal("main agent is unavailable")
			}
			if info, err := os.Stat(siblingDatabase); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("sibling sessions database = %#v, %v", info, err)
			}
			siblingAlias := filepath.Join(mainWorkspace, "sibling-sessions.alias")
			if err := os.Link(siblingDatabase, siblingAlias); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			owner, err := main.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
				Scope: tools.ToolOwnerScopeAgent, AgentID: "sibling-database-writer",
			}, []string{"write_file", "edit_file", "append_file"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = owner.Close() })
			for registryName, toolRegistry := range map[string]*tools.ToolRegistry{
				"root": main.Tools, "owner": owner,
			} {
				for _, target := range []string{siblingDatabase, siblingAlias} {
					for _, toolName := range []string{"write_file", "edit_file", "append_file"} {
						t.Run(registryName+"_"+toolName+"_"+filepath.Base(target), func(t *testing.T) {
							requireAgentFileMutationDenied(
								t,
								toolRegistry,
								toolName,
								mainWorkspace,
								target,
								true,
							)
						})
					}
				}
			}
			pin, repairWorkspace, checkout := newLocalRepairTestWorkspace(t)
			repairAlias := filepath.Join(checkout, "sibling-sessions.alias")
			if linkErr := os.Link(siblingDatabase, repairAlias); linkErr != nil {
				t.Skipf("hardlinks unavailable for local repair: %v", linkErr)
			}
			guard, err := newLocalRepairPathGuardWithPolicy(
				repairWorkspace,
				pin,
				nil,
				nil,
				main.preparedFileMutationPolicy,
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := guard.validateMutation(filepath.Base(repairAlias)); err == nil {
				t.Fatal("local repair accepted sibling SQLite hardlink alias")
			}
		})
	}
}

func TestDirectAgentConstructorProtectsCustomRepositoryDatabasesAndAliases(t *testing.T) {
	home := t.TempDir()
	defaultWorkspace := t.TempDir()
	customWorkspace := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	cfg := agentFileMutationTestConfig(defaultWorkspace)
	cfg.Agents.Defaults.RestrictToWorkspace = false
	agentConfig := &config.AgentConfig{ID: "custom", Workspace: customWorkspace}
	databaseTargets := []string{
		filepath.Join(customWorkspace, agentRepositoryReviewStateDir, "repository-reviews.db"),
		filepath.Join(customWorkspace, agentRepositoryEvalStateDir, "evaluations.db"),
	}
	for _, target := range databaseTargets {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	agent := NewAgentInstance(agentConfig, &cfg.Agents.Defaults, cfg, &mockProvider{})
	t.Cleanup(func() { _ = agent.Close() })
	owner, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "custom-owner",
	}, []string{"write_file", "edit_file", "append_file"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	for _, target := range databaseTargets {
		alias := filepath.Join(customWorkspace, filepath.Base(filepath.Dir(target))+".alias")
		if err := os.Link(target, alias); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		for registryName, toolRegistry := range map[string]*tools.ToolRegistry{
			"root": agent.Tools, "owner": owner,
		} {
			for _, candidate := range []string{target, alias} {
				for _, toolName := range []string{"write_file", "edit_file", "append_file"} {
					t.Run(registryName+"_"+toolName+"_"+filepath.Base(candidate), func(t *testing.T) {
						requireAgentFileMutationDenied(
							t,
							toolRegistry,
							toolName,
							customWorkspace,
							candidate,
							true,
						)
					})
				}
			}
		}
	}
}

func TestAgentDynamicIdentityCatalogCoversEveryMutableLegacyTreeAcrossRename(t *testing.T) {
	workspace := t.TempDir()
	evolutionRoot := filepath.Join(workspace, "custom-evolution")
	cfg := agentFileMutationTestConfig(workspace)
	cfg.Evolution.StateDir = evolutionRoot
	cfg.Events.Ingress.Enabled = true
	cfg.Events.Ingress.DatabasePath = filepath.Join(workspace, "eventing", "events.db")
	evidenceRoot := filepath.Join(workspace, "eventing", "pr-workspace-local-ci", "evidence")
	fixtures := []struct {
		name, source, archive string
	}{
		{
			name:   "sessions",
			source: filepath.Join(workspace, "sessions", "legacy.json"),
			archive: filepath.Join(
				workspace, "legacy-json", "sessions-v1", "sessions", "legacy.json",
			),
		},
		{
			name:   "workflows",
			source: filepath.Join(workspace, "workflow_runs", "wr_fixture", "run.json"),
			archive: filepath.Join(
				workspace, "legacy-json", "workflows-v1", "workflow_runs", "wr_fixture", "run.json",
			),
		},
		{
			name:   "evolution",
			source: filepath.Join(evolutionRoot, "profiles", "profile.json"),
			archive: filepath.Join(
				evolutionRoot, "legacy-json", "evolution-v1", "profiles", "profile.json",
			),
		},
		{
			name:   "evolution-backup",
			source: filepath.Join(evolutionRoot, "backups", "scope", "skill", "revision", "SKILL.md"),
			archive: filepath.Join(
				evolutionRoot, "legacy-json", "evolution-v1", "backups", "SKILL.md",
			),
		},
		{
			name:   "local-ci",
			source: filepath.Join(evidenceRoot, "cache", "aa", "legacy.json"),
			archive: filepath.Join(
				evidenceRoot, "legacy-json", "local-ci-cache-v1", "cache", "aa", "legacy.json",
			),
		},
		{
			name:   "review",
			source: filepath.Join(workspace, "repository_reviews", "repo_legacy.json"),
			archive: filepath.Join(
				workspace,
				"repository_reviews",
				"legacy-json",
				"repository-reviews-v1",
				"repo_legacy.json",
			),
		},
		{
			name:   "evaluation",
			source: filepath.Join(workspace, "repository_evaluations", "evaluation_legacy.json"),
			archive: filepath.Join(
				workspace,
				"repository_evaluations",
				"legacy-json",
				"repository-evaluations-v1",
				"evaluation_legacy.json",
			),
		},
	}
	for _, fixture := range fixtures {
		if err := os.MkdirAll(filepath.Dir(fixture.source), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixture.source, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exact, err := agentEvolutionFileMutationProtectedRoots(workspace, evolutionRoot)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := agentFileMutationIdentityCatalog(workspace, cfg, exact)
	if err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(workspace, "ordinary-aliases")
	if err := os.MkdirAll(aliasRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			alias := filepath.Join(aliasRoot, fixture.name+".alias")
			if err := os.Link(fixture.source, alias); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(fixture.archive), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(fixture.source, fixture.archive); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(alias)
			if err != nil {
				t.Fatal(err)
			}
			protected, err := catalog.ProtectsPath(alias, info)
			if err != nil || !protected {
				t.Fatalf("renamed %s identity protected=%v err=%v", fixture.name, protected, err)
			}
		})
	}
}

func TestAgentAccountRouterLegacySidecarSnapshotIsPinnedAndBounded(t *testing.T) {
	workspace := t.TempDir()
	for index := 0; index < 2; index++ {
		sidecar := filepath.Join(
			workspace,
			fmt.Sprintf(
				"account_router_state.json.auth-invalidation.%032x",
				index+1,
			),
		)
		if err := os.WriteFile(sidecar, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ordinary-a", "ordinary-b"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("ordinary"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := agentAccountRouterLegacySidecarSnapshot(
		[]string{workspace},
		4,
		2,
	)
	if err != nil || len(snapshot) != 2 {
		t.Fatalf("bounded sidecar snapshot = %#v, %v", snapshot, err)
	}
	if snapshot, err = agentAccountRouterLegacySidecarSnapshot(
		[]string{workspace},
		3,
		2,
	); err == nil || snapshot != nil || !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("wide sidecar snapshot = %#v, %v", snapshot, err)
	}
	if snapshot, err = agentAccountRouterLegacySidecarSnapshot(
		[]string{workspace},
		4,
		1,
	); err == nil || snapshot != nil || !strings.Contains(err.Error(), "sidecar limit") {
		t.Fatalf("sidecar capacity snapshot = %#v, %v", snapshot, err)
	}
	if snapshot, err = agentAccountRouterLegacySidecarSnapshot(
		[]string{workspace},
		0,
		1,
	); err == nil || snapshot != nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid sidecar limits = %#v, %v", snapshot, err)
	}

	symlinkWorkspace := t.TempDir()
	symlink := filepath.Join(
		symlinkWorkspace,
		"account_router_state.json.auth-invalidation.0123456789abcdef0123456789abcdef",
	)
	if symlinkErr := os.Symlink(filepath.Join(workspace, "ordinary-a"), symlink); symlinkErr != nil {
		t.Skipf("symlinks unavailable: %v", symlinkErr)
	}
	if snapshot, err = agentAccountRouterLegacySidecarSnapshot(
		[]string{symlinkWorkspace},
		1,
		1,
	); err == nil || snapshot != nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("symlink sidecar snapshot = %#v, %v", snapshot, err)
	}

	secondWorkspace := t.TempDir()
	secondSidecar := filepath.Join(
		secondWorkspace,
		"account_router_state.json.auth-invalidation.abcdef0123456789abcdef0123456789",
	)
	if writeErr := os.WriteFile(secondSidecar, []byte("before"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if snapshot, err = agentAccountRouterLegacySidecarSnapshot(
		[]string{workspace, secondWorkspace},
		8,
		2,
	); err == nil || snapshot != nil || !strings.Contains(err.Error(), "sidecar limit") {
		t.Fatalf("aggregate multi-workspace sidecar bound = %#v, %v", snapshot, err)
	}
	if snapshot, err = agentAccountRouterLegacySidecarSnapshot(
		[]string{workspace, secondWorkspace},
		4,
		4,
	); err == nil || snapshot != nil || !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("aggregate multi-workspace entry bound = %#v, %v", snapshot, err)
	}
}

func TestAgentFileMutationIdentityCatalogRejectsSidecarNamespaceRace(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	sidecar := filepath.Join(
		workspace,
		"account_router_state.json.auth-invalidation.0123456789abcdef0123456789abcdef",
	)
	agentFileMutationIdentityBetweenSidecarSnapshots = func() {
		if err := os.WriteFile(sidecar, []byte("raced"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { agentFileMutationIdentityBetweenSidecarSnapshots = nil })
	catalog, err := agentFileMutationIdentityCatalog(workspace, &config.Config{}, nil)
	if err == nil || catalog != nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("raced sidecar catalog = %#v, %v", catalog, err)
	}
}

func TestAgentFileMutationIdentityCatalogRejectsOverdeepMutableArchive(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	deep := filepath.Join(workspace, agentRepositoryReviewStateDir)
	for index := 0; index < 65; index++ {
		deep = filepath.Join(deep, "nested")
	}
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "legacy.json"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := agentFileMutationIdentityCatalog(workspace, &config.Config{}, nil)
	if err == nil || catalog != nil || !strings.Contains(err.Error(), "enumerated") {
		t.Fatalf("overdeep archive catalog = %#v, %v", catalog, err)
	}
}

func TestAgentFileMutationIdentityCatalogRejectsCheckpointSnapshotRaces(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T, string, string)
	}{
		{
			name: "source-created-after-catalog",
			run: func(t *testing.T, checkpointRoot, _ string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(checkpointRoot, "late.json"), []byte("late"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "source-migrated-after-catalog",
			run: func(t *testing.T, checkpointRoot, archiveRoot string) {
				t.Helper()
				source := filepath.Join(checkpointRoot, "active.json")
				destination := filepath.Join(
					archiveRoot,
					"pr-workspace-checkpoints-v1",
					filepath.Base(source),
				)
				if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(source, destination); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			workspace := filepath.Join(root, "workspace")
			gitRoot := filepath.Join(root, "git-workspaces")
			checkpointRoot := filepath.Join(
				gitRoot,
				".pr-workspace-implementation",
				"active",
			)
			archiveRoot := filepath.Join(checkpointRoot, "legacy-json")
			if err := os.MkdirAll(checkpointRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(checkpointRoot, "active.json"), []byte("active"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(config.EnvHome, home)
			t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
			cfg := agentFileMutationTestConfig(workspace)
			cfg.GitWorkspaces.RootDir = gitRoot
			agentFileMutationIdentityBetweenCheckpointSnapshots = func() {
				agentFileMutationIdentityBetweenCheckpointSnapshots = nil
				test.run(t, checkpointRoot, archiveRoot)
			}
			t.Cleanup(func() { agentFileMutationIdentityBetweenCheckpointSnapshots = nil })
			catalog, err := agentFileMutationIdentityCatalog(workspace, cfg, nil)
			if err == nil || catalog != nil || !strings.Contains(err.Error(), "checkpoint state changed") {
				t.Fatalf("checkpoint-raced catalog = %#v, %v", catalog, err)
			}
			if strings.Contains(err.Error(), checkpointRoot) || strings.Contains(err.Error(), "active.json") {
				t.Fatalf("checkpoint race error disclosed private path: %v", err)
			}
		})
	}
}

func TestWorkflowProtectionLeavesDefinitionsArtifactsAndConfigEditable(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	cfg := agentFileMutationTestConfig(workspace)
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()
	owned, err := agent.Tools.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "workflow-editable-owner",
	}, []string{"write_file", "edit_file", "append_file", "apply_patch"})
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	for registryName, registry := range map[string]*tools.ToolRegistry{"root": agent.Tools, "owner": owned} {
		for _, relativeRoot := range []string{"workflows", "workflow_artifacts"} {
			for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
				path := filepath.Join(workspace, relativeRoot, registryName+"-"+toolName+".txt")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				tool, ok := registry.Get(toolName)
				if !ok {
					t.Fatalf("%s is not registered", toolName)
				}
				result := executeAgentFileMutation(t, tool, toolName, workspace, path, true)
				if result == nil || result.IsError {
					t.Fatalf("%s %s denied editable workflow exception: %#v", registryName, toolName, result)
				}
			}
		}
		for _, toolName := range []string{"write_file", "edit_file", "append_file", "apply_patch"} {
			path := filepath.Join(workspace, registryName+"-"+toolName+"-config.json")
			if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			tool, ok := registry.Get(toolName)
			if !ok {
				t.Fatalf("%s is not registered", toolName)
			}
			result := executeAgentFileMutation(t, tool, toolName, workspace, path, true)
			if result == nil || result.IsError {
				t.Fatalf("%s %s denied editable config: %#v", registryName, toolName, result)
			}
		}
	}
}

func TestAgentFileToolsDenyWorkflowSQLiteRuntimeState(t *testing.T) {
	workspace := t.TempDir()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvConfig, filepath.Join(home, "config.json"))
	protectedFiles := []string{
		filepath.Join(workspace, "state", "workflows.db"),
		filepath.Join(workspace, "workflow_state", "publish-transaction.json"),
		filepath.Join(workspace, "workflow_state", "template-transaction.json"),
	}
	for _, path := range protectedFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := agentFileMutationTestConfig(workspace)
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	defer agent.Close()
	for _, path := range protectedFiles {
		for _, toolName := range []string{"write_file", "edit_file", "append_file"} {
			requireAgentFileMutationDenied(t, agent.Tools, toolName, workspace, path, true)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "before" {
			t.Fatalf("protected workflow file %q changed = %q, %v", path, data, err)
		}
	}
}
