package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestP015B2BCatalogAliasAndRegistryIdentityCounts(t *testing.T) {
	const (
		routerCanary = "p015b2b-router-secret"
		modelCanary  = "p015b2b-model-secret"
	)
	aliasCfg := &config.Config{AccountRouters: []config.AccountRouterConfig{{
		Name: routerCanary, Enabled: true,
	}}}
	var routerCreated bool
	records, _ := captureP015HookRecords(t, func() {
		routerCreated = buildAccountRouterWithAliases(
			aliasCfg,
			routerCanary,
			modelCanary,
			nil,
			t.TempDir(),
			map[string]providers.LLMProvider{},
		) != nil
	})
	if routerCreated {
		t.Fatal("invalid model alias unexpectedly created an account router")
	}
	record := p015B2BCatalogRecord(t, records, "Account router model aliases are invalid")
	if record["identity_route_digest"] == nil || record["identity_model_digest"] == nil ||
		record["error_class"] != "validation" {
		t.Fatalf("alias diagnostic = %#v", record)
	}
	p015B2BCatalogAssertRecordCanariesAbsent(t, record, routerCanary, modelCanary)

	registryCfg := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{
			Workspace: t.TempDir(), ModelName: "registry-default", MaxTokens: 1024,
		},
		List: []config.AgentConfig{
			{ID: "p015b2b-agent-one", Default: true, Workspace: t.TempDir()},
			{ID: "p015b2b-agent-two", Workspace: t.TempDir()},
		},
	}}
	var registry *AgentRegistry
	records, _ = captureP015HookRecords(t, func() {
		registry = NewAgentRegistry(registryCfg, nil)
	})
	if registry == nil || len(registry.ListAgentIDs()) != 2 {
		t.Fatalf("registry = %#v", registry)
	}
	t.Cleanup(registry.Close)
	registered := 0
	for _, candidate := range records {
		if candidate["message"] != "Registered agent" {
			continue
		}
		registered++
		if candidate["identity_agent_digest"] == nil ||
			candidate["identity_workspace_digest"] == nil ||
			candidate["identity_model_digest"] == nil {
			t.Fatalf("registered-agent diagnostic = %#v", candidate)
		}
		p015B2BCatalogAssertRecordCanariesAbsent(
			t, candidate, "p015b2b-agent-one", "p015b2b-agent-two",
		)
	}
	if registered != 2 {
		t.Fatalf("registered-agent diagnostics = %d, want 2; records=%#v", registered, records)
	}
}

func TestP015B2BCatalogInitializationFallbackAndRegexAreSealed(t *testing.T) {
	const regexCanary = "P015B2B_REGEX_SECRET_9d23f1["
	directoryCanary := filepath.Join(t.TempDir(), "P015B2B_SESSION_SECRET_87c19a")
	if err := os.WriteFile(directoryCanary, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	var store session.SessionStore
	var recovered any
	var patternsCount int
	records, raw := captureP015HookRecords(t, func() {
		func() {
			defer func() { recovered = recover() }()
			store = initSessionStore(directoryCanary)
		}()
		patternsCount = len(compilePatterns([]string{"^safe$", regexCanary}))
	})
	if store != nil || recovered != "open SQLite session store" {
		t.Fatalf("failed session open = (store=%T, panic=%v)", store, recovered)
	}
	if patternsCount != 1 {
		t.Fatalf("compiled patterns = %d, want one valid pattern", patternsCount)
	}
	assertP015CanariesAbsent(t, raw, directoryCanary, regexCanary)

	regex := p015B2BCatalogRecord(t, records, "invalid path pattern in compilePatterns")
	if regex["regex_digest"] == nil || regex["error_class"] != "validation" {
		t.Fatalf("regex diagnostic = %#v", regex)
	}
}

func TestP015B2BCatalogMCPCountsAndPostCommitFailureRemainSafe(t *testing.T) {
	server := workflowDependencyMCPTestServer(t, []string{"search"})
	cfg := mcpCatalogRuntimeConfig(t, map[string]string{"catalog-canary": server.URL})
	loop := mcpCatalogRuntimeLoop(t, cfg)
	var initErr error
	records, _ := captureP015HookRecords(t, func() {
		initErr = loop.ensureMCPInitializedForGeneration(context.Background(), cfg, loop.registry)
	})
	if initErr != nil {
		t.Fatal(initErr)
	}
	record := p015B2BCatalogRecord(t, records, "MCP factory catalog installed successfully")
	p015B2BCatalogAssertRecordCanariesAbsent(t, record, "catalog-canary")
	for key, want := range map[string]float64{
		"server_count": 1, "tool_count": 1, "count": 2, "agent_count": 2,
	} {
		if record[key] != want {
			t.Fatalf("MCP count %s = %#v, want %v; record=%#v", key, record[key], want, record)
		}
	}

	failingServer := workflowDependencyMCPTestServer(t, []string{"failure-canary-tool"})
	failingCfg := mcpCatalogRuntimeConfig(t, map[string]string{"failure-canary-server": failingServer.URL})
	failingLoop := mcpCatalogRuntimeLoop(t, failingCfg)
	failingLoop.mcp.installer = func(
		batches []tools.FactoryBackedBatch,
	) ([]tools.FactoryBackedAdmission, error) {
		admissions, err := tools.InstallFactoryBackedTransaction(batches)
		if err != nil || len(admissions) == 0 {
			return admissions, err
		}
		return admissions[:len(admissions)-1], nil
	}
	records, _ = captureP015HookRecords(t, func() {
		initErr = failingLoop.ensureMCPInitializedForGeneration(
			context.Background(), failingCfg, failingLoop.registry,
		)
	})
	if initErr != nil || failingLoop.mcp.getManager() == nil {
		t.Fatalf("post-commit result = err:%v manager:%p", initErr, failingLoop.mcp.getManager())
	}
	record = p015B2BCatalogRecord(t, records,
		"MCP admission projection failed after catalog commit")
	p015B2BCatalogAssertRecordCanariesAbsent(
		t, record, "failure-canary-server", "failure-canary-tool",
	)
	if record["error_class"] != "internal" || record["error_digest"] == nil {
		t.Fatalf("post-commit diagnostic = %#v", record)
	}
}

type p015B2BCatalogHostilePanic struct{ calls *atomic.Int64 }

func (value p015B2BCatalogHostilePanic) String() string {
	value.calls.Add(1)
	panic("hostile String method invoked")
}

func (value p015B2BCatalogHostilePanic) Format(fmt.State, rune) {
	value.calls.Add(1)
	panic("hostile Format method invoked")
}

func TestP015B2BCatalogPostCommitPanicSinkDoesNotFormatRecoveredValue(t *testing.T) {
	var calls atomic.Int64
	server := workflowDependencyMCPTestServer(t, []string{"panic-canary-tool"})
	cfg := mcpCatalogRuntimeConfig(t, map[string]string{"panic-canary-server": server.URL})
	loop := mcpCatalogRuntimeLoop(t, cfg)
	loop.mcp.beforePublish = func(*mcp.Manager) {
		panic(p015B2BCatalogHostilePanic{calls: &calls})
	}
	var initErr error
	records, raw := captureP015HookRecords(t, func() {
		initErr = loop.ensureMCPInitializedForGeneration(
			context.Background(), cfg, loop.registry,
		)
	})
	if initErr != nil {
		t.Fatalf("post-commit panic escaped as initialization error: %v", initErr)
	}
	if loop.mcp.getManager() == nil {
		t.Fatal("post-commit panic did not retain the committed manager")
	}
	if calls.Load() != 0 {
		t.Fatalf("hostile recovered-value methods invoked %d time(s)", calls.Load())
	}
	assertP015CanariesAbsent(
		t,
		raw,
		"hostile String method invoked",
		"hostile Format method invoked",
	)
	record := p015B2BCatalogRecord(t, records,
		"MCP post-commit publication panicked; retained committed manager")
	p015B2BCatalogAssertRecordCanariesAbsent(
		t, record, "panic-canary-server", "panic-canary-tool",
	)
	if record["panic_class"] != "panic" || record["panic_digest"] == nil {
		t.Fatalf("panic diagnostic = %#v", record)
	}
}

func p015B2BCatalogRecord(
	t *testing.T,
	records []map[string]any,
	message string,
) map[string]any {
	t.Helper()
	var match map[string]any
	for _, record := range records {
		if record["message"] != message {
			continue
		}
		if match != nil {
			t.Fatalf("duplicate diagnostic %q: %#v", message, records)
		}
		match = record
	}
	if match == nil {
		t.Fatalf("missing diagnostic %q: %#v", message, records)
	}
	if _, rejected := match["safe_fields_state"]; rejected {
		t.Fatalf("diagnostic %q rejected safe fields: %#v", message, match)
	}
	return match
}

func p015B2BCatalogAssertRecordCanariesAbsent(
	t *testing.T,
	record map[string]any,
	canaries ...string,
) {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	assertP015CanariesAbsent(t, raw, canaries...)
}
