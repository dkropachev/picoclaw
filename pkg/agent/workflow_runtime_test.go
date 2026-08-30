package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowPromptCacheKey(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		agentID    string
		sessionKey string
		wantKey    string
		wantOff    bool
	}{
		{
			name:       "default uses session key",
			sessionKey: "workflow:chat:123",
			agentID:    "main",
			wantKey:    "workflow:chat:123",
		},
		{
			name:       "session uses session key",
			mode:       "session",
			sessionKey: "workflow:chat:123",
			agentID:    "main",
			wantKey:    "workflow:chat:123",
		},
		{
			name:       "agent uses agent id",
			mode:       "agent",
			sessionKey: "workflow:chat:123",
			agentID:    "main",
			wantKey:    "main",
		},
		{
			name:       "none disables prompt cache key",
			mode:       "none",
			sessionKey: "workflow:chat:123",
			agentID:    "main",
			wantOff:    true,
		},
		{
			name:       "custom key",
			mode:       "key:shared-summarizer",
			sessionKey: "workflow:chat:123",
			agentID:    "main",
			wantKey:    "shared-summarizer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKey, gotOff := workflowPromptCacheKey(tt.mode, tt.agentID, tt.sessionKey)
			if gotKey != tt.wantKey || gotOff != tt.wantOff {
				t.Fatalf(
					"workflowPromptCacheKey(%q) = (%q, %v), want (%q, %v)",
					tt.mode,
					gotKey,
					gotOff,
					tt.wantKey,
					tt.wantOff,
				)
			}
		})
	}
}

func TestRepositoryReviewProfileUsesDefaultFallbackChainAndRelevantDependencies(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.AccountRef = "review-account"
	cfg.Agents.Defaults.ModelName = "review-primary"
	cfg.Agents.Defaults.ModelFallbacks = []string{"review-fallback"}
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "review-primary", Model: "primary-v1"},
		{Name: "review-fallback", Model: "fallback-v1"},
		{Name: "unrelated", Model: "unrelated-v1"},
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "review-account", Provider: "openai", Model: "primary-v1",
		APIBase: "http://example.invalid/v1", APIKeys: config.SimpleSecureStrings("test-key"),
		Enabled: true,
	}}
	loop := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), &mockProvider{})
	runner := &workflowAgentRunner{loop: loop}
	profile, err := runner.ResolveRepositoryReviewProfile(t.Context(), "main", "", nil)
	if err != nil || !profile.IncludeDefaultReviewer ||
		!reflect.DeepEqual(profile.ReviewerModels, []string{"review-fallback"}) ||
		profile.Revision == "" || profile.MaxContentBytes < 8<<10 {
		t.Fatalf("default review profile=%#v err=%v", profile, err)
	}
	cfg.ModelAliases[2].Model = "unrelated-v2"
	unrelated, err := runner.ResolveRepositoryReviewProfile(t.Context(), "main", "", nil)
	if err != nil || unrelated.Revision != profile.Revision {
		t.Fatalf("unrelated model changed profile from %q to %q: %v", profile.Revision, unrelated.Revision, err)
	}
	cfg.ModelAliases[0].Model = "primary-v2"
	relevant, err := runner.ResolveRepositoryReviewProfile(t.Context(), "main", "", nil)
	if err != nil || relevant.Revision == profile.Revision {
		t.Fatalf("relevant alias did not change profile=%#v err=%v", relevant, err)
	}
	exact, err := runner.ResolveRepositoryReviewProfile(
		t.Context(), "main", "", []string{"review-primary", "review-fallback"},
	)
	if err != nil || exact.IncludeDefaultReviewer ||
		!reflect.DeepEqual(exact.ReviewerModels, []string{"review-primary", "review-fallback"}) {
		t.Fatalf("explicit review profile=%#v err=%v", exact, err)
	}
	cfg.ModelList[0].Provider = "codex-cli"
	cfg.ModelList[0].Model = "codex-cli/codex"
	if _, err := runner.ResolveRepositoryReviewProfile(
		t.Context(), "main", "", []string{"review-primary"},
	); err == nil || !strings.Contains(err.Error(), "agentic CLI provider") {
		t.Fatalf("agentic CLI reviewer was not rejected: %v", err)
	}
}

func TestRepositoryReviewProfileFreezesExplicitAccountAndItsAliasGraph(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.AccountRef = "review-default"
	cfg.Agents.Defaults.ModelName = "review-model"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name: "review-model", Model: "gpt-default",
		AccountOverrides: map[string]string{"review-alt": "gpt-alt"},
	}}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "review-default", Provider: "openai", Model: "gpt-default",
			APIBase: "http://example.invalid/v1", APIKeys: config.SimpleSecureStrings("default-key"),
			Enabled: true,
		},
		{
			ModelName: "review-alt", Provider: "openai", Model: "gpt-alt",
			APIBase: "http://example.invalid/v1", APIKeys: config.SimpleSecureStrings("alt-key"),
			Enabled: true,
		},
	}
	loop := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), &mockProvider{})
	t.Cleanup(loop.Close)
	runner := &workflowAgentRunner{loop: loop}

	inherited, err := runner.ResolveRepositoryReviewProfile(t.Context(), "main", "", nil)
	if err != nil || inherited.AccountRef != "review-default" {
		t.Fatalf("inherited profile=%#v err=%v", inherited, err)
	}
	explicit, err := runner.ResolveRepositoryReviewProfile(
		t.Context(), "main", "review-alt", nil,
	)
	if err != nil || explicit.AccountRef != "review-alt" || explicit.Revision == inherited.Revision {
		t.Fatalf("explicit profile=%#v inherited=%#v err=%v", explicit, inherited, err)
	}
	cfg.ModelList[0].APIBase = "http://changed-default.invalid/v1"
	unchangedExplicit, err := runner.ResolveRepositoryReviewProfile(
		t.Context(), "main", "review-alt", nil,
	)
	if err != nil || unchangedExplicit.Revision != explicit.Revision {
		t.Fatalf(
			"unselected account changed explicit profile from %q to %q: %v",
			explicit.Revision,
			unchangedExplicit.Revision,
			err,
		)
	}
	if _, err := runner.ResolveRepositoryReviewProfile(
		t.Context(), "main", "missing-account", nil,
	); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing explicit account error=%v", err)
	}
	if _, err := runner.ResolveRepositoryReviewProfile(
		t.Context(), "main", " review-alt ", nil,
	); err == nil || !strings.Contains(err.Error(), "reference is invalid") {
		t.Fatalf("non-exact explicit account error=%v", err)
	}
}

func TestRepositoryReviewProfileRejectsUnavailableRuntimeAgentsAndModels(t *testing.T) {
	var nilRunner *workflowAgentRunner
	if _, err := nilRunner.ResolveRepositoryReviewProfile(context.Background(), "main", "", nil); err == nil ||
		!strings.Contains(err.Error(), "agent loop not configured") {
		t.Fatalf("nil resolver error = %v", err)
	}
	if _, err := (&workflowAgentRunner{loop: &AgentLoop{}}).ResolveRepositoryReviewProfile(
		context.Background(), "main", "", nil,
	); err == nil || !strings.Contains(err.Error(), "runtime generation is not configured") {
		t.Fatalf("missing registry error = %v", err)
	}
	missing := &AgentLoop{
		cfg:      config.DefaultConfig(),
		registry: &AgentRegistry{agents: map[string]*AgentInstance{}},
	}
	if _, err := (&workflowAgentRunner{loop: missing}).ResolveRepositoryReviewProfile(
		context.Background(), "missing", "", nil,
	); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing agent error = %v", err)
	}
	withoutModels := &AgentLoop{
		cfg: config.DefaultConfig(),
		registry: &AgentRegistry{agents: map[string]*AgentInstance{
			"main": {ID: "main"},
		}},
	}
	if _, err := (&workflowAgentRunner{loop: withoutModels}).ResolveRepositoryReviewProfile(
		context.Background(), "main", "", nil,
	); err == nil || !strings.Contains(err.Error(), "no configured model aliases") {
		t.Fatalf("missing model aliases error = %v", err)
	}
	withoutConfig := &AgentLoop{registry: withoutModels.registry}
	if _, err := (&workflowAgentRunner{loop: withoutConfig}).ResolveRepositoryReviewProfile(
		context.Background(), "main", "", []string{"review-a"},
	); err == nil || !strings.Contains(err.Error(), "runtime generation is not configured") {
		t.Fatalf("missing config error = %v", err)
	}
	stopped := &AgentLoop{runtimeGateStopped: true}
	if _, err := (&workflowAgentRunner{loop: stopped}).ResolveRepositoryReviewProfile(
		context.Background(), "main", "", nil,
	); !errors.Is(err, errAgentRuntimeStopped) {
		t.Fatalf("stopped runtime error = %v", err)
	}
}

func TestWorkflowAgentAccountReferenceValidationCoversEveryRuntimeAccountKind(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name: "review-pool", Enabled: true, Entry: "primary",
		Blocks: []config.AccountRouterBlock{{
			ID: "primary", Type: config.AccountRouterBlockTypeAccount, Account: "direct",
		}},
	}}
	cfg.ModelList = []*config.ModelConfig{
		{ModelName: "direct", Provider: "openai", Enabled: true},
		{
			ModelName: "dynamic-model-router", Provider: config.ModelRouterProvider,
			ModelRouter: &config.ModelRouterConfig{Name: "dynamic-model-router"}, Enabled: true,
		},
	}

	for _, accountRef := range []string{
		"", "review-pool", "direct", "credential:openai:work",
	} {
		if err := validateWorkflowAgentAccountRef(cfg, accountRef); err != nil {
			t.Fatalf("valid account reference %q: %v", accountRef, err)
		}
	}

	invalidUTF8 := string([]byte{0xff})
	for _, test := range []struct {
		name       string
		cfg        *config.Config
		accountRef string
		contains   string
	}{
		{name: "missing config", accountRef: "direct", contains: "not configured"},
		{name: "surrounding whitespace", cfg: cfg, accountRef: " direct ", contains: "reference is invalid"},
		{name: "nul", cfg: cfg, accountRef: "direct\x00other", contains: "reference is invalid"},
		{name: "invalid utf8", cfg: cfg, accountRef: invalidUTF8, contains: "reference is invalid"},
		{name: "too long", cfg: cfg, accountRef: strings.Repeat("a", 257), contains: "reference is invalid"},
		{name: "model router", cfg: cfg, accountRef: "dynamic-model-router", contains: "references a model router"},
		{name: "unsupported credential provider", cfg: cfg, accountRef: "credential:custom:work", contains: "unsupported credential account"},
		{name: "unknown", cfg: cfg, accountRef: "missing", contains: "not configured"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateWorkflowAgentAccountRef(test.cfg, test.accountRef)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("validateWorkflowAgentAccountRef(%q) = %v, want %q", test.accountRef, err, test.contains)
			}
		})
	}
}

func TestRepositoryReviewProfileExpandsModelRouterDependenciesAndRejectsUnsafeDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "review-leaf", Model: "leaf-v1"}}
	cfg.ModelRouters = []config.ModelRouterConfig{{
		Name: "review-router", Enabled: true, Entry: "leaf",
		Blocks: []config.ModelRouterBlock{{
			ID: "leaf", Type: config.ModelRouterBlockTypeModel, Model: "review-leaf",
		}},
	}}
	agent := &AgentInstance{
		ID: "main", Model: "review-router", ContextWindow: 16 << 10, MaxTokens: 4 << 10,
	}
	loop := &AgentLoop{cfg: cfg, registry: &AgentRegistry{agents: map[string]*AgentInstance{"main": agent}}}
	profile, err := (&workflowAgentRunner{loop: loop}).ResolveRepositoryReviewProfile(
		context.Background(), "main", "", nil,
	)
	if err != nil || !profile.IncludeDefaultReviewer || profile.Revision == "" || profile.MaxContentBytes < 8<<10 {
		t.Fatalf("model-router review profile = %#v, %v", profile, err)
	}

	agent.Candidates = []providers.FallbackCandidate{{Provider: "codex-cli", Model: "codex"}}
	if _, err := (&workflowAgentRunner{loop: loop}).ResolveRepositoryReviewProfile(
		context.Background(), "main", "", nil,
	); err == nil || !strings.Contains(err.Error(), "agentic CLI provider") {
		t.Fatalf("unsafe default candidate error = %v", err)
	}
}

func TestRepositoryReviewModelDependencyAndReviewerNormalization(t *testing.T) {
	models := []string{"primary"}
	if got := appendRepositoryReviewModelDependency(models, " "); !reflect.DeepEqual(got, models) {
		t.Fatalf("blank dependency = %#v", got)
	}
	if got := appendRepositoryReviewModelDependency(models, "primary"); len(got) != 1 {
		t.Fatalf("duplicate dependency = %#v", got)
	}
	models = appendRepositoryReviewModelDependency(models, "secondary")
	if !reflect.DeepEqual(models, []string{"primary", "secondary"}) {
		t.Fatalf("appended dependencies = %#v", models)
	}
	if got := removeRepositoryReviewModelDependency(models, "primary"); !reflect.DeepEqual(got, []string{"secondary"}) {
		t.Fatalf("removed dependency = %#v", got)
	}
	if !repositoryReviewUnsafeProvider("codex-cli") || repositoryReviewUnsafeProvider("openai") {
		t.Fatal("repository review provider safety classification mismatch")
	}

	if got := workflowManagedReviewerModels(" a, b; a\n c "); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("string reviewers = %#v", got)
	}
	if got := workflowManagedReviewerModels([]string{" a ", "", "b"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("string-slice reviewers = %#v", got)
	}
	values := []any{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	if got := workflowManagedReviewerModels(values); len(got) != 8 || got[7] != "h" {
		t.Fatalf("bounded reviewers = %#v", got)
	}
	if got := workflowManagedReviewerModels(42); len(got) != 0 {
		t.Fatalf("unsupported reviewers = %#v", got)
	}
	options := workflowManagedOptions(map[string]any{
		"estimatedOutputTokens": 321, "includeDefaultReviewer": false,
	})
	if options.estimatedOutputTokens != 321 || options.includeDefaultReviewer {
		t.Fatalf("managed reviewer options = %#v", options)
	}
}

func TestWorkflowAgentRunnerValidatesRepositoryReviewSystemPromptAuthority(t *testing.T) {
	runner := &workflowAgentRunner{loop: &AgentLoop{
		cfg:      config.DefaultConfig(),
		registry: &AgentRegistry{agents: map[string]*AgentInstance{}},
	}}
	for _, test := range []struct {
		name string
		req  workflows.AgentRequest
		want string
	}{
		{name: "invalid review prompt", req: workflows.AgentRequest{ReviewSystemPrompt: " padded "}, want: "system prompt is invalid"},
		{name: "unbounded suppression", req: workflows.AgentRequest{SuppressDefaultContext: true}, want: "bounded frozen no-tool review"},
		{name: "prompt without suppression", req: workflows.AgentRequest{ReviewSystemPrompt: "review"}, want: "requires suppressed"},
		{name: "two prompt authorities", req: workflows.AgentRequest{
			ReviewSystemPrompt: "review", IsolatedSystemPrompt: "isolated", SuppressDefaultContext: true,
			EphemeralSession: true, History: "none", Cache: "none", Tools: workflows.AgentToolsNone,
			Scope: []any{}, ScopeContent: "immutable_git",
		}, want: "requires suppressed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := runner.RunAgent(context.Background(), test.req)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RunAgent error = %v, want %q", err, test.want)
			}
		})
	}
	_, err := runner.RunAgent(context.Background(), workflows.AgentRequest{
		ReviewSystemPrompt: "review", SuppressDefaultContext: true,
		EphemeralSession: true, History: "none", Cache: "none", Tools: workflows.AgentToolsNone,
		Scope: []any{}, ScopeContent: "immutable_git",
	})
	if err == nil || !strings.Contains(err.Error(), "no agent available") {
		t.Fatalf("valid review authority reached error = %v", err)
	}
}

func TestWorkflowManagedScopeSplitCombinesStructuredOutputs(t *testing.T) {
	contract := &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"summary", "findings"},
			"properties": map[string]any{
				"summary":  map[string]any{"type": "string"},
				"findings": map[string]any{"type": "array"},
			},
		},
	}
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_items_per_chunk": 2,
			"calibration": map[string]any{
				"enabled":     true,
				"sample_size": 3,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
			map[string]any{"id": "d"},
			map[string]any{"id": "e"},
		},
		Output: contract,
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		ids := workflowTestScopeIDs(t, message)
		findings := make([]string, 0, len(ids))
		for _, id := range ids {
			findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"title":"finding %s"}`, id, id))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	structured, ok := outputs["structured"].(map[string]any)
	if !ok {
		t.Fatalf("structured = %#v, want object", outputs["structured"])
	}
	findings, ok := structured["findings"].([]any)
	if !ok || len(findings) != 5 {
		t.Fatalf("findings = %#v, want five combined findings", structured["findings"])
	}
	managed, ok := outputs["managed"].(map[string]any)
	if !ok {
		t.Fatalf("managed = %#v, want metadata", outputs["managed"])
	}
	if managed["strategy"] != "scope_split" {
		t.Fatalf("strategy = %#v, want scope_split", managed["strategy"])
	}
	calibration := managed["calibration"].(map[string]any)
	if calibration["match"] != true {
		t.Fatalf("calibration = %#v, want match", calibration)
	}
	split := managed["split"].(map[string]any)
	if split["child_count"] != 3 {
		t.Fatalf("split child_count = %#v, want 3", split["child_count"])
	}
	tokenEfficiency, ok := split["token_efficiency"].(map[string]any)
	if !ok {
		t.Fatalf("token_efficiency = %#v, want metadata", split["token_efficiency"])
	}
	childTokens, ok := tokenEfficiency["child_prompt_tokens"].([]int)
	if !ok || len(childTokens) != 3 {
		t.Fatalf("child_prompt_tokens = %#v, want one estimate per child", tokenEfficiency["child_prompt_tokens"])
	}
}

func TestWorkflowAgentRunnerRejectsUnknownExplicitAgent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, &mockProvider{})
	defer al.Close()

	_, err := (&workflowAgentRunner{loop: al}).RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "reviewer",
		Prompt:  "Review this.",
	})
	if err == nil || !strings.Contains(err.Error(), `workflow agent "reviewer" not found`) {
		t.Fatalf("RunAgent() error = %v, want unknown agent error", err)
	}
}

func TestWorkflowAgentRunnerUsesExplicitAccountForEphemeralRun(t *testing.T) {
	defaultCalls, altCalls := 0, 0
	defaultModel, altModel := "", ""
	defaultServer := newChatCompletionTestServer(
		t, "workflow default account", "default answer", &defaultCalls, &defaultModel,
	)
	t.Cleanup(defaultServer.Close)
	altServer := newChatCompletionTestServer(
		t, "workflow alternate account", "alternate answer", &altCalls, &altModel,
	)
	t.Cleanup(altServer.Close)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.AccountRef = "review-default"
	cfg.Agents.Defaults.ModelName = "review-model"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name: "review-model", Model: "gpt-default",
		AccountOverrides: map[string]string{"review-alt": "gpt-alt"},
	}}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "review-default", Provider: "openai", Model: "gpt-default",
			APIBase: defaultServer.URL, APIKeys: config.SimpleSecureStrings("default-key"),
			Enabled: true,
		},
		{
			ModelName: "review-alt", Provider: "openai", Model: "gpt-alt",
			APIBase: altServer.URL, APIKeys: config.SimpleSecureStrings("alt-key"),
			Enabled: true,
		},
	}
	loop := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), &mockProvider{})
	t.Cleanup(loop.Close)

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		t.Context(),
		workflows.AgentRequest{
			AgentID: "main", AccountRef: "review-alt", Prompt: "Review this.",
			EphemeralSession: true, History: "none", Cache: "none", Tools: workflows.AgentToolsNone,
		},
	)
	if err != nil || outputs["text"] != "alternate answer" {
		t.Fatalf("explicit account run outputs=%#v err=%v", outputs, err)
	}
	if altCalls != 1 || altModel != "gpt-alt" || defaultCalls != 0 {
		t.Fatalf(
			"explicit account providers alt=%d/%q default=%d/%q",
			altCalls,
			altModel,
			defaultCalls,
			defaultModel,
		)
	}
	if _, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		t.Context(),
		workflows.AgentRequest{AgentID: "main", AccountRef: "missing", Prompt: "Review this."},
	); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing workflow account error=%v", err)
	}
}

func TestWorkflowManagedChildrenAndRepairsKeepExplicitAccount(t *testing.T) {
	contract := &workflows.AgentOutputContract{
		Format: "json", RepairAttempts: 1,
		Schema: map[string]any{
			"type": "object", "required": []any{"answer"},
			"properties": map[string]any{"answer": map[string]any{"type": "string"}},
		},
	}
	req := workflows.AgentRequest{
		AccountRef: "review-alt", Prompt: "Review this.", Scope: []any{map[string]any{"id": "one"}},
		Output: contract,
		Managed: map[string]any{
			"mode": "auto", "max_items_per_chunk": 1,
			"calibration": map[string]any{"enabled": false},
		},
		ManagedChildObserver: func(workflows.ManagedChildActivity) error { return nil },
	}
	var seenAccounts []string
	runOnce := func(_ string, _ bool, options workflowAgentRunOptions) (string, error) {
		seenAccounts = append(seenAccounts, options.AccountRef)
		if len(seenAccounts) == 1 {
			return `{}`, nil
		}
		return `{"answer":"ok"}`, nil
	}
	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "main", Model: "review-model", AccountRef: "review-default"},
		"main", workflows.AgentSessionEphemeral, "none", "none", "", "scope_split", runOnce,
	)
	if err != nil || outputs["structured_repairs"] != 1 {
		t.Fatalf("managed account repair outputs=%#v err=%v", outputs, err)
	}
	if !reflect.DeepEqual(seenAccounts, []string{"review-alt", "review-alt"}) {
		t.Fatalf("managed account refs=%#v", seenAccounts)
	}
}

func TestWorkflowAgentRunnerWithNoToolsDoesNotInitializeMCP(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "mcp-command-started")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers: map[string]config.MCPServerConfig{
			"private-server": {
				Enabled: true,
				Command: "sh",
				Args: []string{
					"-c",
					`printf started > "$1"`,
					"workflow-agent-test",
					marker,
				},
			},
		},
	}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, &mockProvider{})
	defer al.Close()

	outputs, err := (&workflowAgentRunner{loop: al}).RunAgent(
		context.Background(),
		workflows.AgentRequest{
			AgentID: "main",
			Prompt:  "Review this without tools.",
			History: "none",
			Tools:   workflows.AgentToolsNone,
		},
	)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["tools"] != workflows.AgentToolsNone {
		t.Fatalf("tools audit = %#v, want none", outputs["tools"])
	}
	if al.mcp.getManager() != nil || al.mcp.getInitErr() != nil {
		t.Fatal("no-tools agent run initialized or mutated MCP runtime state")
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("no-tools agent run executed MCP command: %v", statErr)
	}
}

func TestWorkflowAgentRunnerReadOnlyUsesExactImmutableSessionSnapshot(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{`{"needs_user":false,"reason":"clear"}`}}
	loop, agent, canonicalKey, alias := newWorkflowReadOnlyTestLoop(t, provider)
	reader := agent.Sessions.(session.SnapshotReader)
	before, found, err := reader.ReadSessionSnapshot(context.Background(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (%#v, %v, %v), want existing snapshot", before, found, err)
	}
	beforeSessions := append([]string(nil), agent.Sessions.ListSessions()...)

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflows.AgentRequest{
			AgentID: "main",
			Prompt:  "Decide whether this needs user attention.",
			Session: alias,
			History: "read_only",
			Cache:   "session",
			Tools:   workflows.AgentToolsNone,
			Output: &workflows.AgentOutputContract{
				Format: "json",
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"needs_user", "reason"},
					"properties": map[string]any{
						"needs_user": map[string]any{"type": "boolean"},
						"reason":     map[string]any{"type": "string"},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["session"] != canonicalKey {
		t.Fatalf("session output = %#v, want canonical %q", outputs["session"], canonicalKey)
	}
	if revision, _ := outputs["history_revision"].(string); !strings.HasPrefix(revision, "sha256:") {
		t.Fatalf("history_revision = %#v, want opaque sha256 revision", outputs["history_revision"])
	}
	if outputs["tools"] != workflows.AgentToolsNone {
		t.Fatalf("tools output = %#v, want none", outputs["tools"])
	}

	calls := provider.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	if calls[0].toolCount != 0 {
		t.Fatalf("provider tool definitions = %d, want 0", calls[0].toolCount)
	}
	if calls[0].promptCacheKey != canonicalKey {
		t.Fatalf("prompt cache key = %q, want canonical session", calls[0].promptCacheKey)
	}
	if !workflowMessagesContain(calls[0].messages, "existing problem context") {
		t.Fatalf("provider prompt omitted exact session history: %#v", calls[0].messages)
	}
	if !workflowMessagesContain(calls[0].messages, "existing decision summary") {
		t.Fatalf("provider prompt omitted exact session summary: %#v", calls[0].messages)
	}

	after, found, err := reader.ReadSessionSnapshot(context.Background(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot(after) = (%#v, %v, %v)", after, found, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("source session mutated\nbefore: %#v\nafter:  %#v", before, after)
	}
	if afterSessions := agent.Sessions.ListSessions(); !reflect.DeepEqual(afterSessions, beforeSessions) {
		t.Fatalf("session catalog changed: before=%v after=%v", beforeSessions, afterSessions)
	}
	if loop.mcp.getManager() != nil || loop.mcp.getInitErr() != nil {
		t.Fatal("read-only decision initialized MCP")
	}
}

func TestWorkflowAgentRunnerPublicReadOnlyRejectsReviewScopeWhilePrivateCaptureSucceeds(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"private review decision"}}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	runner := &workflowAgentRunner{loop: loop}
	reviewScope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "review",
		Account:    "default",
		Dimensions: []string{"review"},
		Values:     map[string]string{"review": "case-guard"},
	}
	reviewKey := session.BuildSessionKey(reviewScope)
	const reviewAlias = "review:agent:main:case:case-guard"
	replacer, ok := agent.Sessions.(session.SnapshotReplacer)
	if !ok {
		t.Fatalf("session store %T does not support exact replacement", agent.Sessions)
	}
	if err := replacer.ReplaceSessionSnapshot(context.Background(), session.SessionSnapshotReplacement{
		Key: reviewKey,
		History: []providers.Message{
			{Role: "user", Content: "private review finding"},
			{Role: "assistant", Content: "private review analysis"},
		},
		Summary: "private review summary",
		Scope:   &reviewScope,
		Aliases: []string{reviewAlias},
	}); err != nil {
		t.Fatalf("seed review snapshot: %v", err)
	}

	_, err := runner.RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "main",
		Session: reviewAlias,
		History: "read_only",
		Cache:   "session",
		Tools:   workflows.AgentToolsNone,
		Prompt:  "Expose the review history.",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot use a review-scoped session") {
		t.Fatalf("public review-scoped RunAgent() error = %v, want fail-closed rejection", err)
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("public rejection reached provider %d time(s), want zero", len(calls))
	}

	frozen, err := runner.CaptureReadOnlySession(context.Background(), workflows.ReadOnlySessionRef{
		AgentID: "main",
		Session: reviewAlias,
	})
	if err != nil {
		t.Fatalf("private CaptureReadOnlySession() error = %v", err)
	}
	if frozen.Snapshot.Scope == nil || frozen.Snapshot.Scope.Channel != "review" {
		t.Fatalf("captured review scope = %#v, want review", frozen.Snapshot.Scope)
	}
	outputs, err := runner.RunAgent(context.Background(), workflows.AgentRequest{
		AgentID:               "main",
		History:               "read_only",
		Cache:                 "session",
		Tools:                 workflows.AgentToolsNone,
		Prompt:                "Decide from the compiler-frozen review evidence.",
		FrozenReadOnlySession: frozen,
	})
	if err != nil {
		t.Fatalf("private frozen RunAgent() error = %v", err)
	}
	if outputs["text"] != "private review decision" ||
		outputs["session_mode"] != workflows.AgentSessionPrivate {
		t.Fatalf("private frozen outputs = %#v", outputs)
	}
	if calls := provider.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("private frozen provider calls = %d, want one", len(calls))
	}
}

func TestWorkflowAgentRunnerPrivateCaptureRevisionFenceExactAndCompatible(t *testing.T) {
	tests := []struct {
		name   string
		fenced bool
	}{
		{name: "exact revision", fenced: true},
		{name: "empty compatibility revision"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
			loop, agent, canonicalKey, alias := newWorkflowReadOnlyTestLoop(t, provider)
			snapshot, found, err := agent.Sessions.(session.SnapshotReader).ReadSessionSnapshot(
				context.Background(),
				canonicalKey,
			)
			if err != nil || !found || snapshot.Revision == "" {
				t.Fatalf("projected snapshot = (%#v, %v, %v), want revision", snapshot, found, err)
			}
			expected := ""
			if test.fenced {
				expected = snapshot.Revision
			}
			frozen, err := (&workflowAgentRunner{loop: loop}).CaptureReadOnlySession(
				context.Background(),
				workflows.ReadOnlySessionRef{
					AgentID:          "main",
					Session:          alias,
					ExpectedRevision: expected,
				},
			)
			if err != nil {
				t.Fatalf("CaptureReadOnlySession() error = %v", err)
			}
			if frozen.Snapshot.Key != canonicalKey || frozen.Snapshot.Revision != snapshot.Revision {
				t.Fatalf(
					"captured snapshot identity = %#v, want %q at %q",
					frozen.Snapshot,
					canonicalKey,
					snapshot.Revision,
				)
			}
			if calls := provider.snapshotCalls(); len(calls) != 0 {
				t.Fatalf("capture reached provider %d time(s), want zero", len(calls))
			}
		})
	}
}

func TestWorkflowAgentRunnerPrivateCaptureRejectsCanonicalKeyScopeMismatch(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, agent, canonicalKey, _ := newWorkflowReadOnlyTestLoop(t, provider)
	reader := agent.Sessions.(session.SnapshotReader)
	snapshot, found, err := reader.ReadSessionSnapshot(t.Context(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("source snapshot = (%#v, %v, %v)", snapshot, found, err)
	}
	snapshot.Key = session.BuildOpaqueSessionKey("different-canonical-owner")
	agent.Sessions = &workflowFixedSnapshotSessionStore{
		SessionStore: agent.Sessions,
		snapshot:     snapshot,
	}
	_, err = (&workflowAgentRunner{loop: loop}).CaptureReadOnlySession(
		t.Context(),
		workflows.ReadOnlySessionRef{AgentID: "main", Session: canonicalKey},
	)
	if err == nil || !strings.Contains(err.Error(), "does not match its declared scope") {
		t.Fatalf("CaptureReadOnlySession() error = %v, want key/scope rejection", err)
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("corrupt capture reached provider %d time(s), want zero", len(calls))
	}
}

func TestWorkflowAgentRunnerPrivateCaptureRejectsMutationBeforeLiveMediaRead(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, agent, canonicalKey, alias := newWorkflowReadOnlyTestLoop(t, provider)
	baseMediaStore := media.NewFileMediaStore()
	mediaPath := filepath.Join(t.TempDir(), "revision-fence.txt")
	if err := os.WriteFile(mediaPath, []byte("revision-fenced evidence"), 0o600); err != nil {
		t.Fatalf("write media fixture: %v", err)
	}
	ref, err := baseMediaStore.Store(mediaPath, media.MediaMeta{
		Filename:      "revision-fence.txt",
		ContentType:   "text/plain",
		CleanupPolicy: media.CleanupPolicyForgetOnly,
	}, "revision-fence-test")
	if err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	mediaSpy := &workflowSnapshotReadSpyMediaStore{MediaStore: baseMediaStore}
	loop.mediaStore = mediaSpy
	agent.Sessions.AddFullMessage(canonicalKey, providers.Message{
		Role:    "user",
		Content: "inspect projected media",
		Attachments: []providers.Attachment{{
			Type:        "file",
			Ref:         ref,
			Filename:    "revision-fence.txt",
			ContentType: "text/plain",
		}},
	})
	projected, found, err := agent.Sessions.(session.SnapshotReader).ReadSessionSnapshot(
		context.Background(),
		canonicalKey,
	)
	if err != nil || !found || projected.Revision == "" {
		t.Fatalf("projected snapshot = (%#v, %v, %v), want revision", projected, found, err)
	}

	// This is the bridge race: the UI projected one exact revision, then the
	// live discussion changed before workflow admission attempted capture.
	agent.Sessions.AddMessage(canonicalKey, "user", "mutation after projection")
	_, captureErr := (&workflowAgentRunner{loop: loop}).CaptureReadOnlySession(
		context.Background(),
		workflows.ReadOnlySessionRef{
			AgentID:          "main",
			Session:          alias,
			ExpectedRevision: projected.Revision,
		},
	)
	if captureErr == nil {
		t.Fatal("CaptureReadOnlySession() error = nil, want stale revision failure")
	}
	if got := mediaSpy.snapshotReads.Load(); got != 0 {
		t.Fatalf("stale capture read live media %d time(s), want zero", got)
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("stale capture reached provider %d time(s), want zero", len(calls))
	}
}

func TestWorkflowAgentRunnerPrivateCaptureRejectsMalformedRevisionBeforeSnapshotRead(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, agent, _, alias := newWorkflowReadOnlyTestLoop(t, provider)
	reader := agent.Sessions.(session.SnapshotReader)
	storeSpy := &workflowSnapshotReadCountingSessionStore{
		SessionStore: agent.Sessions,
		reader:       reader,
	}
	agent.Sessions = storeSpy
	invalid := []string{
		" surrounded ",
		string([]byte{0xff}),
		strings.Repeat("r", maxWorkflowReadOnlySessionRevisionBytes+1),
	}
	for _, expected := range invalid {
		_, err := (&workflowAgentRunner{loop: loop}).CaptureReadOnlySession(
			context.Background(),
			workflows.ReadOnlySessionRef{
				AgentID:          "main",
				Session:          alias,
				ExpectedRevision: expected,
			},
		)
		if err == nil {
			t.Fatal("CaptureReadOnlySession(malformed revision) error = nil")
		}
	}
	if got := storeSpy.snapshotReads.Load(); got != 0 {
		t.Fatalf("malformed revisions read live session %d time(s), want zero", got)
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("malformed revisions reached provider %d time(s), want zero", len(calls))
	}
}

func TestWorkflowAgentRunnerPrivateFrozenSnapshotNeverReadsOrExposesLiveSession(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"first", "second", "third"}}
	loop, agent, canonicalKey, alias := newWorkflowReadOnlyTestLoop(t, provider)
	runner := &workflowAgentRunner{loop: loop}
	frozen, err := runner.CaptureReadOnlySession(context.Background(), workflows.ReadOnlySessionRef{
		AgentID: "main",
		Session: alias,
	})
	if err != nil {
		t.Fatalf("CaptureReadOnlySession() error = %v", err)
	}
	if frozen.Snapshot.Key != canonicalKey || frozen.AgentID != "main" {
		t.Fatalf("captured identity = %#v, want main/%q", frozen, canonicalKey)
	}
	if revision := frozen.HistoryRevision; !strings.HasPrefix(revision, "sha256:") {
		t.Fatalf("captured history revision = %q, want opaque sha256 revision", revision)
	}
	if frozen.FrozenMedia.Version != media.FrozenSetVersion ||
		len(frozen.FrozenMedia.Assets) != 0 || len(frozen.FrozenMedia.Blobs) != 0 {
		t.Fatalf("no-media capture set = %#v, want empty versioned frozen set", frozen.FrozenMedia)
	}

	liveStore := agent.Sessions
	liveStore.AddMessage(canonicalKey, "user", "LIVE-SESSION-MUTATION-CANARY")
	readSpy := &workflowNoSnapshotReadStore{SessionStore: liveStore}
	agent.Sessions = readSpy
	t.Cleanup(func() { agent.Sessions = liveStore })

	req := workflows.AgentRequest{
		AgentID: "main",
		Prompt:  "Decide using only the captured evidence.",
		History: "read_only",
		Cache:   "session",
		Tools:   workflows.AgentToolsNone,
		Delivery: workflows.Delivery{
			Channel:   "RAW-CHANNEL-CANARY",
			ChatID:    "RAW-CHAT-CANARY",
			MessageID: "RAW-DELIVERY-MESSAGE-CANARY",
		},
		MessageID:             "RAW-REQUEST-MESSAGE-CANARY",
		FrozenReadOnlySession: frozen,
	}
	first, err := runner.RunAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("RunAgent(first) error = %v", err)
	}
	second, err := runner.RunAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("RunAgent(second) error = %v", err)
	}
	craftedCacheReq := req
	craftedCacheReq.Cache = "key:" + canonicalKey
	third, err := runner.RunAgent(context.Background(), craftedCacheReq)
	if err != nil {
		t.Fatalf("RunAgent(crafted cache key) error = %v", err)
	}
	if readSpy.snapshotReads.Load() != 0 {
		t.Fatalf("frozen runs read live snapshot %d time(s), want zero", readSpy.snapshotReads.Load())
	}
	for index, outputs := range []map[string]any{first, second, third} {
		if outputs["session"] != workflows.AgentSessionPrivate ||
			outputs["session_mode"] != workflows.AgentSessionPrivate {
			t.Fatalf("run %d session markers = %#v, want fixed private markers", index, outputs)
		}
		if outputs["history_revision"] != frozen.HistoryRevision {
			t.Fatalf(
				"run %d history revision = %#v, want %q",
				index,
				outputs["history_revision"],
				frozen.HistoryRevision,
			)
		}
		if outputs["cache_key"] != "" || outputs["message_id"] != "" {
			t.Fatalf("run %d exposed private cache/routing identity: %#v", index, outputs)
		}
		encoded, marshalErr := json.Marshal(outputs)
		if marshalErr != nil {
			t.Fatalf("json.Marshal(outputs) error = %v", marshalErr)
		}
		for _, canary := range []string{
			canonicalKey,
			alias,
			"RAW-CHANNEL-CANARY",
			"RAW-CHAT-CANARY",
			"RAW-DELIVERY-MESSAGE-CANARY",
			"RAW-REQUEST-MESSAGE-CANARY",
		} {
			if strings.Contains(string(encoded), canary) {
				t.Fatalf("run %d outputs exposed %q: %s", index, canary, encoded)
			}
		}
	}

	calls := provider.snapshotCalls()
	if len(calls) != 3 {
		t.Fatalf("provider calls = %d, want three", len(calls))
	}
	if calls[0].promptCacheKey == "" ||
		calls[0].promptCacheKey != calls[1].promptCacheKey ||
		calls[0].promptCacheKey != calls[2].promptCacheKey {
		t.Fatalf(
			"private prompt-cache identities = %q, %q, %q, want one stable pseudonym",
			calls[0].promptCacheKey,
			calls[1].promptCacheKey,
			calls[2].promptCacheKey,
		)
	}
	if !strings.HasPrefix(calls[0].promptCacheKey, "workflow:private:") {
		t.Fatalf("private prompt-cache identity = %q, want domain marker", calls[0].promptCacheKey)
	}
	for index, call := range calls {
		if !workflowMessagesContain(call.messages, "existing problem context") {
			t.Fatalf("provider call %d omitted captured history: %#v", index, call.messages)
		}
		if workflowMessagesContain(call.messages, "LIVE-SESSION-MUTATION-CANARY") {
			t.Fatalf("provider call %d observed live mutation: %#v", index, call.messages)
		}
		encoded, marshalErr := json.Marshal(struct {
			Messages       []providers.Message `json:"messages"`
			Options        map[string]any      `json:"options"`
			PromptCacheKey string              `json:"prompt_cache_key"`
		}{
			Messages:       call.messages,
			Options:        call.options,
			PromptCacheKey: call.promptCacheKey,
		})
		if marshalErr != nil {
			t.Fatalf("json.Marshal(provider call) error = %v", marshalErr)
		}
		for _, canary := range []string{
			canonicalKey,
			alias,
			"RAW-CHANNEL-CANARY",
			"RAW-CHAT-CANARY",
			"RAW-DELIVERY-MESSAGE-CANARY",
		} {
			if strings.Contains(string(encoded), canary) {
				t.Fatalf("provider call %d exposed %q: %s", index, canary, encoded)
			}
		}
	}
}

func TestWorkflowAgentRunnerPrivateFrozenMediaSurvivesCleanupAndRestart(t *testing.T) {
	const imageBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"private media decision"}}
	loop, agent, canonicalKey, alias := newWorkflowReadOnlyTestLoop(t, provider)
	store := media.NewFileMediaStore()
	imageBytes, decodeErr := base64.StdEncoding.Strict().DecodeString(imageBase64)
	if decodeErr != nil {
		t.Fatalf("decode test image: %v", decodeErr)
	}
	imagePath := filepath.Join(t.TempDir(), "private-screenshot.png")
	if writeErr := os.WriteFile(imagePath, imageBytes, 0o600); writeErr != nil {
		t.Fatalf("write test image: %v", writeErr)
	}
	const mediaScope = "workflow-private-capture"
	ref, storeErr := store.Store(imagePath, media.MediaMeta{
		Filename:      "private-screenshot.png",
		ContentType:   "image/png",
		Source:        "test:private-workflow",
		CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
	}, mediaScope)
	if storeErr != nil {
		t.Fatalf("Store() error = %v", storeErr)
	}
	loop.mediaStore = store
	agent.Sessions.SetHistory(canonicalKey, []providers.Message{{
		Role:    "user",
		Content: "Review the captured screenshot.",
		Attachments: []providers.Attachment{{
			Type:        "image",
			Ref:         ref,
			Filename:    "private-screenshot.png",
			ContentType: "image/png",
		}},
	}})

	runner := &workflowAgentRunner{loop: loop}
	frozen, captureErr := runner.CaptureReadOnlySession(context.Background(), workflows.ReadOnlySessionRef{
		AgentID: "main",
		Session: alias,
	})
	if captureErr != nil {
		t.Fatalf("CaptureReadOnlySession() error = %v", captureErr)
	}
	if validateErr := frozen.FrozenMedia.Validate(); validateErr != nil {
		t.Fatalf("captured FrozenMedia.Validate() error = %v", validateErr)
	}
	if got := frozen.Snapshot.History[0].Attachments[0].Ref; !strings.HasPrefix(got, "frozen-media://sha256/") {
		t.Fatalf("captured attachment ref = %q, want immutable frozen reference", got)
	}
	if strings.Contains(mustJSONForWorkflowTest(t, frozen), ref) {
		t.Fatal("captured private session retained the live media capability")
	}

	encoded, marshalErr := json.Marshal(frozen)
	if marshalErr != nil {
		t.Fatalf("json.Marshal(frozen) error = %v", marshalErr)
	}
	var restarted workflows.FrozenReadOnlySession
	if unmarshalErr := json.Unmarshal(encoded, &restarted); unmarshalErr != nil {
		t.Fatalf("json.Unmarshal(frozen) error = %v", unmarshalErr)
	}
	if releaseErr := store.ReleaseAll(mediaScope); releaseErr != nil {
		t.Fatalf("ReleaseAll() error = %v", releaseErr)
	}
	if _, statErr := os.Stat(imagePath); !os.IsNotExist(statErr) {
		t.Fatalf("source image still exists after cleanup: %v", statErr)
	}
	loop.mediaStore = nil
	agent.Sessions = &workflowNoSnapshotReadStore{SessionStore: agent.Sessions}

	outputs, runErr := runner.RunAgent(context.Background(), workflows.AgentRequest{
		AgentID:               "main",
		Prompt:                "Decide using only the durable screenshot.",
		History:               "read_only",
		Cache:                 "session",
		Tools:                 workflows.AgentToolsNone,
		FrozenReadOnlySession: &restarted,
	})
	if runErr != nil {
		t.Fatalf("RunAgent() after cleanup/restart error = %v", runErr)
	}
	if outputs["text"] != "private media decision" {
		t.Fatalf("text = %#v, want private media decision", outputs["text"])
	}
	calls := provider.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	var providerRef string
	for _, message := range calls[0].messages {
		for _, attachment := range message.Attachments {
			if attachment.Filename == "private-screenshot.png" {
				providerRef = attachment.Ref
				if attachment.ContentType != "image/png" {
					t.Fatalf("provider content type = %q, want image/png", attachment.ContentType)
				}
			}
		}
	}
	if providerRef != "data:image/png;base64,"+imageBase64 {
		t.Fatalf("provider attachment ref = %q, want canonical embedded data URI", providerRef)
	}
	providerCallJSON := mustJSONForWorkflowTest(t, calls[0])
	for _, forbidden := range []string{ref, imagePath, "frozen-media://"} {
		if strings.Contains(providerCallJSON, forbidden) {
			t.Fatalf("provider call exposed %q: %s", forbidden, providerCallJSON)
		}
	}

	cloneFrozen := func() *workflows.FrozenReadOnlySession {
		return &workflows.FrozenReadOnlySession{
			AgentID:         restarted.AgentID,
			Snapshot:        workflowCloneSessionSnapshot(restarted.Snapshot),
			HistoryRevision: restarted.HistoryRevision,
			FrozenMedia:     restarted.FrozenMedia.Clone(),
		}
	}
	baseRequest := func(value *workflows.FrozenReadOnlySession) workflows.AgentRequest {
		return workflows.AgentRequest{
			AgentID:               "main",
			Prompt:                "reject tampered media",
			History:               "read_only",
			Cache:                 "session",
			Tools:                 workflows.AgentToolsNone,
			FrozenReadOnlySession: value,
		}
	}
	tamperCases := []struct {
		name   string
		mutate func(*workflows.FrozenReadOnlySession)
	}{
		{
			name: "set version",
			mutate: func(value *workflows.FrozenReadOnlySession) {
				value.FrozenMedia.Version = 0
			},
		},
		{
			name: "blob bytes",
			mutate: func(value *workflows.FrozenReadOnlySession) {
				value.FrozenMedia.Blobs[0].Data[0] ^= 0xff
			},
		},
		{
			name: "authoritative metadata",
			mutate: func(value *workflows.FrozenReadOnlySession) {
				value.Snapshot.History[0].Attachments[0].Filename = "tampered.png"
				value.HistoryRevision, _ = workflowSessionSnapshotRevision(value.Snapshot)
			},
		},
		{
			name: "missing asset",
			mutate: func(value *workflows.FrozenReadOnlySession) {
				value.FrozenMedia.Assets = nil
				value.FrozenMedia.Blobs = nil
			},
		},
		{
			name: "unknown frozen reference",
			mutate: func(value *workflows.FrozenReadOnlySession) {
				value.Snapshot.History[0].Attachments[0].Ref = "frozen-media://sha256/" +
					strings.Repeat("0", 64)
				value.HistoryRevision, _ = workflowSessionSnapshotRevision(value.Snapshot)
			},
		},
		{
			name: "unused valid asset",
			mutate: func(value *workflows.FrozenReadOnlySession) {
				_, extra, freezeErr := media.FreezeInputs(
					context.Background(),
					[]media.FreezeInput{{Locator: "data:text/plain;base64,dW51c2Vk"}},
					nil,
				)
				if freezeErr != nil {
					t.Fatalf("FreezeInputs(extra) error = %v", freezeErr)
				}
				value.FrozenMedia.Assets = append(value.FrozenMedia.Assets, extra.Assets...)
				value.FrozenMedia.Blobs = append(value.FrozenMedia.Blobs, extra.Blobs...)
				sort.Slice(value.FrozenMedia.Assets, func(i, j int) bool {
					return value.FrozenMedia.Assets[i].ID < value.FrozenMedia.Assets[j].ID
				})
				sort.Slice(value.FrozenMedia.Blobs, func(i, j int) bool {
					return value.FrozenMedia.Blobs[i].SHA256 < value.FrozenMedia.Blobs[j].SHA256
				})
			},
		},
	}
	for _, test := range tamperCases {
		t.Run(test.name, func(t *testing.T) {
			provider.setResponses([]string{"unexpected"})
			value := cloneFrozen()
			test.mutate(value)
			if _, runErr := runner.RunAgent(context.Background(), baseRequest(value)); runErr == nil {
				t.Fatal("RunAgent() error = nil, want fail-closed frozen-media error")
			}
			if calls := provider.snapshotCalls(); len(calls) != 0 {
				t.Fatalf("tampered media reached provider %d time(s), want zero", len(calls))
			}
		})
	}
}

func TestWorkflowAgentRunnerPrivateCaptureRejectsLiveMediaWithoutSnapshotReader(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, agent, canonicalKey, alias := newWorkflowReadOnlyTestLoop(t, provider)
	loop.mediaStore = nil
	agent.Sessions.SetHistory(canonicalKey, []providers.Message{{
		Role:    "user",
		Content: "This live capability cannot be made durable.",
		Media:   []string{"media://550e8400-e29b-41d4-a716-446655440000"},
	}})

	if _, err := (&workflowAgentRunner{loop: loop}).CaptureReadOnlySession(
		context.Background(),
		workflows.ReadOnlySessionRef{AgentID: "main", Session: alias},
	); err == nil {
		t.Fatal("CaptureReadOnlySession() error = nil, want unavailable-media failure")
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("failed capture reached provider %d time(s), want zero", len(calls))
	}
}

func mustJSONForWorkflowTest(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%T) error = %v", value, err)
	}
	return string(encoded)
}

func TestWorkflowAgentRunnerPrivateFrozenSnapshotRejectsMismatchesBeforeProvider(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, _, _, alias := newWorkflowReadOnlyTestLoop(t, provider)
	runner := &workflowAgentRunner{loop: loop}
	frozen, err := runner.CaptureReadOnlySession(context.Background(), workflows.ReadOnlySessionRef{
		AgentID: "main",
		Session: alias,
	})
	if err != nil {
		t.Fatalf("CaptureReadOnlySession() error = %v", err)
	}
	cloneFrozen := func() *workflows.FrozenReadOnlySession {
		return &workflows.FrozenReadOnlySession{
			AgentID:         frozen.AgentID,
			Snapshot:        workflowCloneSessionSnapshot(frozen.Snapshot),
			HistoryRevision: frozen.HistoryRevision,
			FrozenMedia:     frozen.FrozenMedia.Clone(),
		}
	}
	baseRequest := func() workflows.AgentRequest {
		return workflows.AgentRequest{
			AgentID:               "main",
			Prompt:                "decide",
			History:               "read_only",
			Cache:                 "session",
			Tools:                 workflows.AgentToolsNone,
			FrozenReadOnlySession: cloneFrozen(),
		}
	}
	tests := []struct {
		name   string
		mutate func(*workflows.AgentRequest)
	}{
		{name: "request agent", mutate: func(req *workflows.AgentRequest) { req.AgentID = "support" }},
		{
			name: "frozen agent",
			mutate: func(req *workflows.AgentRequest) {
				req.FrozenReadOnlySession.AgentID = "support"
			},
		},
		{
			name: "owner",
			mutate: func(req *workflows.AgentRequest) {
				req.FrozenReadOnlySession.Snapshot.Scope.AgentID = "support"
			},
		},
		{
			name: "revision",
			mutate: func(req *workflows.AgentRequest) {
				req.FrozenReadOnlySession.HistoryRevision = "sha256:stale"
			},
		},
		{
			name: "mutated evidence",
			mutate: func(req *workflows.AgentRequest) {
				req.FrozenReadOnlySession.Snapshot.Summary = "changed"
			},
		},
		{name: "live session", mutate: func(req *workflows.AgentRequest) { req.Session = alias }},
		{name: "history", mutate: func(req *workflows.AgentRequest) { req.History = "none" }},
		{name: "tools", mutate: func(req *workflows.AgentRequest) { req.Tools = workflows.AgentToolsInherit }},
		{name: "ephemeral", mutate: func(req *workflows.AgentRequest) { req.EphemeralSession = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := baseRequest()
			test.mutate(&req)
			if _, runErr := runner.RunAgent(context.Background(), req); runErr == nil {
				t.Fatal("RunAgent() error = nil, want fail-closed private snapshot error")
			}
		})
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("mismatches reached provider %d time(s), want zero", len(calls))
	}
}

func TestWorkflowAgentRunnerReadOnlyRejectsMissingIdentityBeforeProvider(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	beforeSessions := append([]string(nil), agent.Sessions.ListSessions()...)

	requests := []workflows.AgentRequest{
		{AgentID: "main", History: "read_only", Tools: workflows.AgentToolsNone, Prompt: "decide"},
		{
			AgentID: "main", Session: "agent:main:missing", History: "read_only",
			Tools: workflows.AgentToolsNone, Prompt: "decide",
		},
		{
			AgentID: "Main", Session: "agent:main:review", History: "read_only",
			Tools: workflows.AgentToolsNone, Prompt: "decide",
		},
		{
			AgentID: "@@@", Session: "agent:main:review", History: "read_only",
			Tools: workflows.AgentToolsNone, Prompt: "decide",
		},
	}
	for _, req := range requests {
		if _, err := (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), req); err == nil {
			t.Fatalf("RunAgent(%#v) error = nil, want fail-closed identity error", req)
		}
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("provider calls = %d, want 0", len(calls))
	}
	if afterSessions := agent.Sessions.ListSessions(); !reflect.DeepEqual(afterSessions, beforeSessions) {
		t.Fatalf("failed lookups changed session catalog: before=%v after=%v", beforeSessions, afterSessions)
	}
}

func TestWorkflowAgentRunnerReadOnlyRejectsOwnerMismatchAndToolCalls(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, agent, canonicalKey, _ := newWorkflowReadOnlyTestLoop(t, provider)
	metadata := agent.Sessions.(session.MetadataAwareSessionStore)
	original, found, err := agent.Sessions.(session.SnapshotReader).ReadSessionSnapshot(
		t.Context(), canonicalKey,
	)
	if err != nil || !found || original.Scope == nil {
		t.Fatalf("original snapshot = (%#v, %v, %v)", original, found, err)
	}
	foreignScope := session.CloneScope(original.Scope)
	foreignScope.AgentID = "support"
	metadata.EnsureSessionMetadata(canonicalKey, foreignScope, nil)

	_, err = (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "main",
		Session: canonicalKey,
		History: "read_only",
		Tools:   workflows.AgentToolsNone,
		Prompt:  "decide",
	})
	if err == nil || !strings.Contains(err.Error(), "belongs to another agent") {
		t.Fatalf("owner mismatch error = %v", err)
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("owner mismatch provider calls = %d, want 0", len(calls))
	}

	metadata.EnsureSessionMetadata(canonicalKey, original.Scope, nil)
	provider.setResponses([]string{""})
	provider.toolCall = true
	_, err = (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "main",
		Session: canonicalKey,
		History: "read_only",
		Tools:   workflows.AgentToolsNone,
		Prompt:  "decide",
	})
	if err == nil || !strings.Contains(err.Error(), "returned tool calls") {
		t.Fatalf("tool-call response error = %v", err)
	}
}

func TestWorkflowAgentRunnerReadOnlyDoesNotClobberConcurrentAppend(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{"decision"},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	loop, agent, canonicalKey, _ := newWorkflowReadOnlyTestLoop(t, provider)
	loop.activeTurnStates.Store(canonicalKey, &turnState{sessionKey: canonicalKey})
	defer loop.activeTurnStates.Delete(canonicalKey)

	errCh := make(chan error, 1)
	go func() {
		_, err := (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), workflows.AgentRequest{
			AgentID: "main",
			Session: canonicalKey,
			History: "read_only",
			Tools:   workflows.AgentToolsNone,
			Prompt:  "decide",
		})
		errCh <- err
	}()
	<-provider.started
	agent.Sessions.AddMessage(canonicalKey, "user", "legitimate concurrent append")
	close(provider.release)
	if err := <-errCh; err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	history := agent.Sessions.GetHistory(canonicalKey)
	if !workflowMessagesContain(history, "legitimate concurrent append") {
		t.Fatalf("concurrent append was lost: %#v", history)
	}
	calls := provider.snapshotCalls()
	if len(calls) != 1 || workflowMessagesContain(calls[0].messages, "legitimate concurrent append") {
		t.Fatalf("decision did not use one frozen pre-append snapshot: %#v", calls)
	}
}

func TestWorkflowAgentRunnerReadOnlyRepairReusesFrozenSnapshot(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{
			"not json",
			`{"needs_user":true,"reason":"ambiguous"}`,
		},
		mutateNestedInput: true,
	}
	loop, agent, canonicalKey, _ := newWorkflowReadOnlyTestLoop(t, provider)
	agent.Sessions.AddMessage(canonicalKey, "user", "inspect nested immutable context")
	agent.Sessions.AddFullMessage(canonicalKey, providers.Message{
		Role:    "assistant",
		Content: "nested immutable context",
		SystemParts: []providers.ContentBlock{{
			Type:         "text",
			Text:         "frozen nested block",
			CacheControl: &providers.CacheControl{Type: "ephemeral"},
		}},
		ToolCalls: []providers.ToolCall{{
			ID:        "prior-call",
			Function:  &providers.FunctionCall{Name: "inspect", Arguments: `{}`},
			Arguments: map[string]any{"nested": map[string]any{"marker": "original"}},
		}},
	})
	agent.Sessions.AddFullMessage(canonicalKey, providers.Message{
		Role:       "tool",
		ToolCallID: "prior-call",
		Content:    "inspection complete",
	})
	provider.afterCall = func(index int) {
		if index == 0 {
			agent.Sessions.AddMessage(canonicalKey, "user", "arrived between decision and repair")
		}
	}

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflows.AgentRequest{
			AgentID: "main",
			Session: canonicalKey,
			History: "read_only",
			Tools:   workflows.AgentToolsNone,
			Prompt:  "decide",
			Output: &workflows.AgentOutputContract{
				Format:         "json",
				RepairAttempts: 1,
				Schema: map[string]any{
					"type":     "object",
					"required": []any{"needs_user", "reason"},
					"properties": map[string]any{
						"needs_user": map[string]any{"type": "boolean"},
						"reason":     map[string]any{"type": "string"},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["structured_repairs"] != 1 || outputs["structured_valid"] != true {
		t.Fatalf("structured outputs = %#v, want one valid repair", outputs)
	}
	calls := provider.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want initial plus repair", len(calls))
	}
	for index, call := range calls {
		if workflowMessagesContain(call.messages, "arrived between decision and repair") {
			t.Fatalf("provider call %d reread live session instead of frozen snapshot", index)
		}
		if call.toolCount != 0 {
			t.Fatalf("provider call %d tool definitions = %d, want 0", index, call.toolCount)
		}
		if cacheControl := workflowFrozenCacheControl(call.messages); cacheControl != "ephemeral" {
			t.Fatalf(
				"provider call %d frozen cache control = %q, want ephemeral: %#v",
				index,
				cacheControl,
				call.messages,
			)
		}
	}
	if !workflowMessagesContain(agent.Sessions.GetHistory(canonicalKey), "arrived between decision and repair") {
		t.Fatal("legitimate append between decision and repair was lost")
	}
}

func TestWorkflowAgentUsageIncludesNonManagedStructuredRepair(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{"not json", `{"ok":true}`},
		usages: []providers.UsageInfo{
			{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, CachedTokens: 3},
			{PromptTokens: 20, CompletionTokens: 4, TotalTokens: 24, CachedTokens: 5},
		},
	}
	loop, _, _, _ := newWorkflowReadOnlyTestLoop(t, provider) //nolint:dogsled // This test only needs the loop.
	var (
		observedMu sync.Mutex
		observed   []workflows.AgentUsage
	)
	request := workflowEphemeralTestRequest("Return valid JSON.")
	request.Output = &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"ok"},
			"properties": map[string]any{
				"ok": map[string]any{"type": "boolean"},
			},
		},
	}
	request.UsageObserver = func(usage workflows.AgentUsage) error {
		observedMu.Lock()
		observed = append(observed, usage)
		observedMu.Unlock()
		return nil
	}

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(t.Context(), request)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	usage, ok := outputs["usage"].([]workflows.AgentUsage)
	if !ok || !reflect.DeepEqual(usage, []workflows.AgentUsage{{
		Model:              "test-model",
		ProviderCalls:      2,
		UsageReportedCalls: 2,
		PromptTokens:       30,
		CompletionTokens:   6,
		TotalTokens:        36,
		CachedTokens:       8,
	}}) {
		t.Fatalf("usage = %#v, want exact initial+repair aggregate", outputs["usage"])
	}
	observedMu.Lock()
	defer observedMu.Unlock()
	if !reflect.DeepEqual(observed, []workflows.AgentUsage{
		{
			Model:              "test-model",
			ProviderCalls:      1,
			UsageReportedCalls: 1,
			PromptTokens:       10,
			CompletionTokens:   2,
			TotalTokens:        12,
			CachedTokens:       3,
		},
		{
			Model:              "test-model",
			ProviderCalls:      1,
			UsageReportedCalls: 1,
			PromptTokens:       20,
			CompletionTokens:   4,
			TotalTokens:        24,
			CachedTokens:       5,
		},
	}) {
		t.Fatalf("observed usage = %#v, want one event per provider response", observed)
	}
}

func TestWorkflowAgentUsageObserverErrorPropagates(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{"unused"},
		usages: []providers.UsageInfo{{
			PromptTokens: 7, CompletionTokens: 2, TotalTokens: 9,
		}},
	}
	loop, _, _, _ := newWorkflowReadOnlyTestLoop(t, provider) //nolint:dogsled // This test only needs the loop.
	wantErr := errors.New("shared workflow token budget exhausted")
	request := workflowEphemeralTestRequest("Observe this request.")
	request.UsageObserver = func(usage workflows.AgentUsage) error {
		if usage.Model != "test-model" || usage.TotalTokens != 9 {
			t.Fatalf("observer usage = %#v", usage)
		}
		return wantErr
	}

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(t.Context(), request)
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunAgent() outputs = %#v, error = %v, want observer error", outputs, err)
	}
	if !reflect.DeepEqual(outputs["usage"], []workflows.AgentUsage{{
		Model: "test-model", ProviderCalls: 1, UsageReportedCalls: 1,
		PromptTokens: 7, CompletionTokens: 2, TotalTokens: 9,
	}}) {
		t.Fatalf("observer-error usage = %#v", outputs["usage"])
	}
	if calls := provider.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("provider calls = %d, want no retry after observer error", len(calls))
	}
}

func TestWorkflowManagedCallAdmissionStopsQueuedChildrenWithScopePlaceholders(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{
		workflowManagedTestFindingsJSON([]string{"a"}),
		workflowManagedTestFindingsJSON([]string{"b"}),
	}}
	loop, _, _, _ := newWorkflowReadOnlyTestLoop(t, provider) //nolint:dogsled // This test only needs the loop.
	var admissions atomic.Int32
	request := workflowEphemeralTestRequest("Review bounded items.")
	request.Output = workflowManagedTestOutputContract()
	request.Managed = map[string]any{
		"mode": "auto", "max_items_per_chunk": 1, "max_parallel_children": 1,
		"continue_on_child_error": true,
		"calibration":             map[string]any{"enabled": false},
	}
	request.Scope = []any{
		map[string]any{"id": "a", "path": "a.go"},
		map[string]any{"id": "b", "path": "b.go"},
	}
	wantStop := errors.New("review budget stopped admission")
	request.CallAdmission = func() error {
		if admissions.Add(1) > 2 {
			return wantStop
		}
		return nil
	}

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(t.Context(), request)
	if err != nil && !errors.Is(err, wantStop) {
		t.Fatalf("RunAgent() error = %v, outputs=%#v", err, outputs)
	}
	if calls := provider.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("provider calls = %d, want only first child", len(calls))
	}
	children, ok := outputs["managed_children"].([]map[string]any)
	if !ok || len(children) != 2 {
		t.Fatalf("managed children = %#v, want two plan-preserving outputs", outputs["managed_children"])
	}
	if admitted, _ := children[0]["admitted"].(bool); !admitted {
		t.Fatalf("first child admitted = %#v", children[0])
	}
	if admitted, _ := children[1]["admitted"].(bool); admitted {
		t.Fatalf("second child admitted = %#v", children[1])
	}
	scope, _ := children[1]["scope"].([]any)
	if len(scope) != 1 || scope[0].(map[string]any)["path"] != "b.go" {
		t.Fatalf("unadmitted child scope = %#v", scope)
	}
}

func TestWorkflowAgentRunnerReadOnlyManagedChildrenReuseFrozenSnapshot(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{
		workflowManagedTestFindingsJSON([]string{"a"}),
		workflowManagedTestFindingsJSON([]string{"b"}),
	}}
	loop, agent, canonicalKey, _ := newWorkflowReadOnlyTestLoop(t, provider)
	provider.afterCall = func(index int) {
		if index == 0 {
			agent.Sessions.AddMessage(canonicalKey, "user", "arrived between managed children")
		}
	}

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflows.AgentRequest{
			AgentID: "main",
			Session: canonicalKey,
			History: "read_only",
			Tools:   workflows.AgentToolsNone,
			Prompt:  "review the assigned scope",
			Managed: map[string]any{
				"mode":                  "auto",
				"max_items_per_chunk":   1,
				"max_parallel_children": 1,
				"calibration": map[string]any{
					"enabled": false,
				},
			},
			Scope: []any{
				map[string]any{"id": "a"},
				map[string]any{"id": "b"},
			},
			Output: workflowManagedTestOutputContract(),
		},
	)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if revision, _ := outputs["history_revision"].(string); !strings.HasPrefix(revision, "sha256:") {
		t.Fatalf("history_revision = %#v, want frozen revision", outputs["history_revision"])
	}
	if outputs["session"] != canonicalKey {
		t.Fatalf("session = %#v, want canonical %q", outputs["session"], canonicalKey)
	}
	calls := provider.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want two managed children", len(calls))
	}
	for index, call := range calls {
		if call.toolCount != 0 {
			t.Fatalf("managed call %d tool definitions = %d, want 0", index, call.toolCount)
		}
		if !workflowMessagesContain(call.messages, "existing problem context") {
			t.Fatalf("managed call %d omitted frozen history", index)
		}
		if workflowMessagesContain(call.messages, "arrived between managed children") {
			t.Fatalf("managed call %d reread live history", index)
		}
	}
	if !workflowMessagesContain(agent.Sessions.GetHistory(canonicalKey), "arrived between managed children") {
		t.Fatal("append between managed children was lost")
	}
}

func TestWorkflowAgentRunnerReadOnlyPropagatesCancellationWithoutWrites(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{"unexpected"},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	loop, agent, canonicalKey, _ := newWorkflowReadOnlyTestLoop(t, provider)
	reader := agent.Sessions.(session.SnapshotReader)
	before, found, err := reader.ReadSessionSnapshot(context.Background(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (%#v, %v, %v)", before, found, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, runErr := (&workflowAgentRunner{loop: loop}).RunAgent(ctx, workflows.AgentRequest{
			AgentID: "main",
			Session: canonicalKey,
			History: "read_only",
			Tools:   workflows.AgentToolsNone,
			Prompt:  "decide",
		})
		errCh <- runErr
	}()
	<-provider.started
	cancel()
	if runErr := <-errCh; !errors.Is(runErr, context.Canceled) {
		t.Fatalf("RunAgent() error = %v, want context.Canceled", runErr)
	}
	after, found, err := reader.ReadSessionSnapshot(context.Background(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot(after) = (%#v, %v, %v)", after, found, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("canceled decision mutated source session\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestWorkflowAgentRunnerEphemeralIsIsolatedAndLeavesJSONLUntouched(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"isolated decision"}}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	originalContextManager := loop.contextManager
	contextTracker := &trackingContextManager{}
	loop.contextManager = contextTracker
	t.Cleanup(func() { loop.contextManager = originalContextManager })
	if _, ok := agent.Sessions.(*session.JSONLBackend); !ok {
		t.Fatalf("session store = %T, want real JSONL backend", agent.Sessions)
	}
	sessionsDir := filepath.Join(agent.Workspace, "sessions")
	beforeCatalog := append([]string(nil), agent.Sessions.ListSessions()...)
	beforeFiles := workflowDirectoryFileSnapshot(t, sessionsDir)

	req := workflowEphemeralTestRequest("Decide without durable context.")
	req.Delivery = workflows.Delivery{
		Channel:          "private-review-channel",
		ChatID:           "private-pr-chat-42",
		TopicID:          "private-topic-7",
		MessageID:        "private-message-9",
		ReplyToMessageID: "private-reply-8",
	}
	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["session"] != workflows.AgentSessionEphemeral ||
		outputs["session_mode"] != workflows.AgentSessionEphemeral {
		t.Fatalf("session audit = %#v, want opaque ephemeral mode", outputs)
	}
	if outputs["history"] != "none" || outputs["cache"] != "none" ||
		outputs["cache_key"] != "" || outputs["tools"] != workflows.AgentToolsNone {
		t.Fatalf("isolation audit = %#v, want history/cache/tools disabled", outputs)
	}

	calls := provider.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	if calls[0].toolCount != 0 {
		t.Fatalf("provider tool definitions = %d, want 0", calls[0].toolCount)
	}
	if calls[0].promptCachePresent || calls[0].promptCacheKey != "" {
		t.Fatalf("provider prompt cache = (%v, %q), want absent", calls[0].promptCachePresent, calls[0].promptCacheKey)
	}
	if workflowMessagesHavePromptCacheControl(calls[0].messages) {
		t.Fatalf("provider messages retained prompt cache controls: %#v", calls[0].messages)
	}
	if workflowMessagesContain(calls[0].messages, "existing problem context") ||
		workflowMessagesContain(calls[0].messages, "existing decision summary") {
		t.Fatalf("ephemeral request loaded durable history: %#v", calls[0].messages)
	}
	if !workflowMessagesContain(calls[0].messages, "Decide without durable context.") {
		t.Fatalf("provider request omitted current prompt: %#v", calls[0].messages)
	}
	for _, privateDeliveryValue := range []string{
		"private-review-channel",
		"private-pr-chat-42",
		"private-topic-7",
		"private-message-9",
		"private-reply-8",
	} {
		if workflowMessagesContain(calls[0].messages, privateDeliveryValue) {
			t.Fatalf("provider request leaked delivery value %q: %#v", privateDeliveryValue, calls[0].messages)
		}
	}

	workflowAssertSessionStoreUnchanged(t, agent, sessionsDir, beforeCatalog, beforeFiles)
	if contextTracker.assembleCalls.Load() != 0 || contextTracker.compactCalls.Load() != 0 ||
		contextTracker.ingestCalls.Load() != 0 || contextTracker.clearCalls.Load() != 0 {
		t.Fatalf(
			"ephemeral context manager calls = assemble:%d compact:%d ingest:%d clear:%d, want zero",
			contextTracker.assembleCalls.Load(),
			contextTracker.compactCalls.Load(),
			contextTracker.ingestCalls.Load(),
			contextTracker.clearCalls.Load(),
		)
	}
	workflowAssertNoEphemeralActiveTurn(t, loop)
	if loop.mcp.getManager() != nil || loop.mcp.getInitErr() != nil {
		t.Fatal("ephemeral decision initialized MCP")
	}
}

func TestWorkflowAgentRunnerIsolatedSystemPromptReplacesAllDefaultContext(t *testing.T) {
	const (
		isolatedSystemPrompt = "PR development assistant. Use only the supplied case transcript."
		isolatedUserContext  = `{"case_id":"prdev:owner/repo:42","messages":[{"role":"user","content":"Fix the race."}]}`
	)
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"isolated answer"}}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)

	canaries := map[string]string{
		"AGENTS.md":                          "WORKSPACE-BOOTSTRAP-CANARY",
		"IDENTITY.md":                        "WORKSPACE-IDENTITY-CANARY",
		filepath.Join("memory", "MEMORY.md"): "WORKSPACE-MEMORY-CANARY",
		filepath.Join("skills", "isolation-canary", "SKILL.md"): "---\n" +
			"name: isolation-canary\n" +
			"description: WORKSPACE-SKILL-CANARY\n" +
			"---\n\nWORKSPACE-SKILL-BODY-CANARY\n",
	}
	for relativePath, contents := range canaries {
		path := filepath.Join(agent.Workspace, relativePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create canary directory for %q: %v", relativePath, err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatalf("write canary %q: %v", relativePath, err)
		}
	}

	agent.ContextBuilder.systemPromptMutex.RLock()
	cacheBefore := agent.ContextBuilder.cachedSystemPrompt
	agent.ContextBuilder.systemPromptMutex.RUnlock()
	if cacheBefore != "" {
		t.Fatalf("local system prompt cache was populated before isolated run: %q", cacheBefore)
	}

	req := workflowEphemeralTestRequest("")
	req.Context = isolatedUserContext
	req.PrivateContext = true
	req.IsolatedSystemPrompt = isolatedSystemPrompt
	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["text"] != "isolated answer" {
		t.Fatalf("text = %#v, want isolated answer", outputs["text"])
	}

	calls := provider.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.toolCount != 0 {
		t.Fatalf("provider tool definitions = %d, want 0", call.toolCount)
	}
	if call.promptCachePresent || call.promptCacheKey != "" {
		t.Fatalf(
			"provider prompt cache = (%v, %q), want absent",
			call.promptCachePresent,
			call.promptCacheKey,
		)
	}
	if workflowMessagesHavePromptCacheControl(call.messages) {
		t.Fatalf("provider messages retained prompt cache controls: %#v", call.messages)
	}
	if len(call.messages) != 2 {
		t.Fatalf("provider messages = %#v, want exactly system and user", call.messages)
	}
	systemMessage := call.messages[0]
	if systemMessage.Role != "system" || systemMessage.Content != isolatedSystemPrompt {
		t.Fatalf("system message = %#v, want exact isolated prompt", systemMessage)
	}
	if len(systemMessage.SystemParts) != 1 ||
		systemMessage.SystemParts[0].Text != isolatedSystemPrompt {
		t.Fatalf("system message parts = %#v, want exact isolated prompt", systemMessage.SystemParts)
	}
	userMessage := call.messages[1]
	if userMessage.Role != "user" || userMessage.Content != isolatedUserContext {
		t.Fatalf("user message = %#v, want exact supplied context", userMessage)
	}

	for _, forbidden := range []string{
		agent.Workspace,
		"WORKSPACE-BOOTSTRAP-CANARY",
		"WORKSPACE-IDENTITY-CANARY",
		"WORKSPACE-MEMORY-CANARY",
		"WORKSPACE-SKILL-CANARY",
		"WORKSPACE-SKILL-BODY-CANARY",
		"existing problem context",
		"existing decision summary",
		"You are picoclaw",
		"## Current Time",
		"## Runtime",
	} {
		if workflowMessagesContain(call.messages, forbidden) {
			t.Fatalf("isolated provider request leaked %q: %#v", forbidden, call.messages)
		}
	}

	agent.ContextBuilder.systemPromptMutex.RLock()
	cacheAfter := agent.ContextBuilder.cachedSystemPrompt
	agent.ContextBuilder.systemPromptMutex.RUnlock()
	if cacheAfter != "" {
		t.Fatalf("isolated run populated local system prompt cache: %q", cacheAfter)
	}
}

func TestWorkflowAgentRunnerIsolatedSystemPromptRejectsBroaderAuthority(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, testAgent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	if testAgent == nil {
		t.Fatal("test agent is nil")
	}
	tests := []struct {
		name   string
		mutate func(*workflows.AgentRequest)
	}{
		{
			name: "public context",
			mutate: func(req *workflows.AgentRequest) {
				req.PrivateContext = false
			},
		},
		{
			name: "durable execution",
			mutate: func(req *workflows.AgentRequest) {
				req.EphemeralSession = false
			},
		},
		{
			name: "delivery context",
			mutate: func(req *workflows.AgentRequest) {
				req.Delivery.Channel = "web"
			},
		},
		{
			name: "delivery reply handles",
			mutate: func(req *workflows.AgentRequest) {
				req.Delivery.ReplyHandles = map[string]string{"review": "private"}
			},
		},
		{
			name: "message identity",
			mutate: func(req *workflows.AgentRequest) {
				req.MessageID = "message-42"
			},
		},
		{
			name: "managed scope",
			mutate: func(req *workflows.AgentRequest) {
				req.Scope = []any{"finding-1"}
			},
		},
		{
			name: "managed execution",
			mutate: func(req *workflows.AgentRequest) {
				req.Managed = map[string]any{"mode": "auto"}
			},
		},
	}

	runner := &workflowAgentRunner{loop: loop}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := workflowEphemeralTestRequest("private context")
			req.PrivateContext = true
			req.IsolatedSystemPrompt = "trusted isolated system prompt"
			tt.mutate(&req)
			if _, err := runner.RunAgent(context.Background(), req); err == nil ||
				!strings.Contains(err.Error(), "private ephemeral single-run request") {
				t.Fatalf("RunAgent() error = %v, want isolated authority rejection", err)
			}
		})
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("provider calls = %d, want rejected before provider I/O", len(calls))
	}
}

func TestWorkflowAgentRunnerIsolatedSystemPromptRejectsInvalidText(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, testAgent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	if testAgent == nil {
		t.Fatal("test agent is nil")
	}
	tests := []struct {
		name   string
		prompt string
	}{
		{name: "leading whitespace", prompt: " trusted prompt"},
		{name: "trailing whitespace", prompt: "trusted prompt\n"},
		{name: "nul", prompt: "trusted\x00prompt"},
		{name: "invalid utf8", prompt: string([]byte{'t', 0xff, 'x'})},
		{name: "too large", prompt: strings.Repeat("x", maxWorkflowIsolatedSystemPromptBytes+1)},
	}

	runner := &workflowAgentRunner{loop: loop}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := workflowEphemeralTestRequest("private context")
			req.PrivateContext = true
			req.IsolatedSystemPrompt = tt.prompt
			if _, err := runner.RunAgent(context.Background(), req); err == nil ||
				!strings.Contains(err.Error(), "system prompt is invalid") {
				t.Fatalf("RunAgent() error = %v, want isolated prompt validation rejection", err)
			}
		})
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("provider calls = %d, want rejected before provider I/O", len(calls))
	}
}

func TestWorkflowAgentRunnerEphemeralDoesNotInitializeConfiguredHooksOrMCP(t *testing.T) {
	const hookName = "test-workflow-ephemeral-isolation"
	spy := &workflowEphemeralHookSpy{}
	var factoryCalls atomic.Int64
	if err := RegisterBuiltinHook(hookName, func(
		context.Context,
		config.BuiltinHookConfig,
	) (any, error) {
		factoryCalls.Add(1)
		return spy, nil
	}); err != nil {
		t.Fatalf("RegisterBuiltinHook() error = %v", err)
	}
	t.Cleanup(func() { unregisterBuiltinHook(hookName) })

	marker := filepath.Join(t.TempDir(), "mcp-command-started")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.Hooks = config.HooksConfig{
		Enabled: true,
		Builtins: map[string]config.BuiltinHookConfig{
			hookName: {Enabled: true},
		},
	}
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers: map[string]config.MCPServerConfig{
			"private-server": {
				Enabled: true,
				Command: "sh",
				Args: []string{
					"-c",
					`printf started > "$1"`,
					"workflow-ephemeral-test",
					marker,
				},
			},
		},
	}
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"isolated"}}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	loop.providerFactory = func(modelConfig *config.ModelConfig) (providers.LLMProvider, string, error) {
		model := "workflow-ephemeral-test"
		if modelConfig != nil && strings.TrimSpace(modelConfig.Model) != "" {
			model = modelConfig.Model
		}
		return provider, model, nil
	}

	if _, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflowEphemeralTestRequest("Do not initialize extension runtimes."),
	); err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if factoryCalls.Load() != 0 || spy.beforeCalls.Load() != 0 || spy.afterCalls.Load() != 0 {
		t.Fatalf(
			"ephemeral hook calls = factory:%d before:%d after:%d, want zero",
			factoryCalls.Load(),
			spy.beforeCalls.Load(),
			spy.afterCalls.Load(),
		)
	}
	if loop.mcp.getManager() != nil || loop.mcp.getInitErr() != nil {
		t.Fatal("ephemeral decision initialized MCP runtime")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("ephemeral decision executed MCP command: %v", err)
	}
}

func TestWorkflowAgentRunnerEphemeralRepairsStructuredOutputWithoutPersistence(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{
		"not json",
		`{"needs_user":true,"reason":"ambiguous"}`,
	}}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	sessionsDir := filepath.Join(agent.Workspace, "sessions")
	beforeCatalog := append([]string(nil), agent.Sessions.ListSessions()...)
	beforeFiles := workflowDirectoryFileSnapshot(t, sessionsDir)
	req := workflowEphemeralTestRequest("Decide whether the user is needed.")
	req.Output = &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"needs_user", "reason"},
			"properties": map[string]any{
				"needs_user": map[string]any{"type": "boolean"},
				"reason":     map[string]any{"type": "string"},
			},
		},
	}

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["structured_valid"] != true || outputs["structured_repairs"] != 1 {
		t.Fatalf("structured audit = %#v, want one successful repair", outputs)
	}
	structured, ok := outputs["structured"].(map[string]any)
	if !ok || structured["needs_user"] != true || structured["reason"] != "ambiguous" {
		t.Fatalf("structured output = %#v, want repaired decision", outputs["structured"])
	}
	calls := provider.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want request plus repair", len(calls))
	}
	for index, call := range calls {
		if call.toolCount != 0 || call.promptCachePresent {
			t.Fatalf("provider call %d isolation = %#v, want no tools/cache", index, call)
		}
		if workflowMessagesContain(call.messages, "existing problem context") {
			t.Fatalf("provider call %d loaded durable history: %#v", index, call.messages)
		}
	}
	if !workflowMessagesContain(calls[1].messages, "previous response did not satisfy") {
		t.Fatalf("repair call omitted structured repair instruction: %#v", calls[1].messages)
	}
	workflowAssertSessionStoreUnchanged(t, agent, sessionsDir, beforeCatalog, beforeFiles)
}

func TestWorkflowAgentRunnerRejectsInvalidPatternBeforeProviderCall(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{`"unused"`}}
	loop, agent, configPath, workspace := newWorkflowReadOnlyTestLoop(t, provider)
	if agent == nil || configPath == "" || workspace == "" {
		t.Fatal("workflow test loop fixture is incomplete")
	}
	req := workflowEphemeralTestRequest("Return a value.")
	req.Output = &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"optional": map[string]any{"type": "string", "pattern": "["},
			},
		},
	}
	if _, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(), req,
	); err == nil || !strings.Contains(err.Error(), "invalid agent output schema") {
		t.Fatalf("invalid pattern contract error=%v", err)
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("invalid pattern contract reached provider: %#v", calls)
	}
}

func TestWorkflowAgentRunnerEphemeralCreatesAndClosesStatefulProviderPerCall(t *testing.T) {
	bootstrap := &workflowReadOnlyCaptureProvider{}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, bootstrap)
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	state := &workflowEphemeralStatefulProviderState{}
	loop.providerFactory = func(*config.ModelConfig) (providers.LLMProvider, string, error) {
		return state.newProvider(), "workflow-ephemeral-stateful", nil
	}
	req := workflowEphemeralTestRequest("Return structured output.")
	req.Output = &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"ok"},
			"properties": map[string]any{
				"ok": map[string]any{"type": "boolean"},
			},
		},
	}

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["structured_valid"] != true || outputs["structured_repairs"] != 1 {
		t.Fatalf("structured output = %#v, want one successful repair", outputs)
	}
	created, called, closed := state.snapshot()
	if !reflect.DeepEqual(created, []int{1, 2}) ||
		!reflect.DeepEqual(called, []int{1, 2}) ||
		!reflect.DeepEqual(closed, []int{1, 2}) {
		t.Fatalf(
			"stateful provider lifecycle = created:%v called:%v closed:%v, want distinct closed providers [1 2]",
			created,
			called,
			closed,
		)
	}
}

func TestWorkflowAgentRunnerEphemeralRejectsProviderToolCalls(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{"unexpected"},
		toolCall:  true,
	}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	sessionsDir := filepath.Join(agent.Workspace, "sessions")
	beforeCatalog := append([]string(nil), agent.Sessions.ListSessions()...)
	beforeFiles := workflowDirectoryFileSnapshot(t, sessionsDir)

	_, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflowEphemeralTestRequest("Return a decision, never a tool call."),
	)
	if err == nil || !strings.Contains(err.Error(), "returned tool calls") {
		t.Fatalf("RunAgent() error = %v, want fail-closed tool-call rejection", err)
	}
	calls := provider.snapshotCalls()
	if len(calls) != 1 || calls[0].toolCount != 0 {
		t.Fatalf("provider calls = %#v, want one call with no tool definitions", calls)
	}
	workflowAssertSessionStoreUnchanged(t, agent, sessionsDir, beforeCatalog, beforeFiles)
}

func TestWorkflowAgentRunnerEphemeralRejectsUnsafeRuntimeTupleBeforeProvider(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	sessionsDir := filepath.Join(agent.Workspace, "sessions")
	beforeCatalog := append([]string(nil), agent.Sessions.ListSessions()...)
	beforeFiles := workflowDirectoryFileSnapshot(t, sessionsDir)
	tests := []struct {
		name   string
		mutate func(*workflows.AgentRequest)
		want   string
	}{
		{
			name: "history default",
			mutate: func(req *workflows.AgentRequest) {
				req.History = ""
			},
			want: "requires history: none",
		},
		{
			name: "history read only",
			mutate: func(req *workflows.AgentRequest) {
				req.History = "read_only"
			},
			want: "requires history: none",
		},
		{
			name: "history mixed case",
			mutate: func(req *workflows.AgentRequest) {
				req.History = "NONE"
			},
			want: "requires history: none",
		},
		{
			name: "cache default",
			mutate: func(req *workflows.AgentRequest) {
				req.Cache = ""
			},
			want: "requires cache: none",
		},
		{
			name: "cache session",
			mutate: func(req *workflows.AgentRequest) {
				req.Cache = "session"
			},
			want: "requires cache: none",
		},
		{
			name: "cache mixed case",
			mutate: func(req *workflows.AgentRequest) {
				req.Cache = "NONE"
			},
			want: "requires cache: none",
		},
		{
			name: "tools default",
			mutate: func(req *workflows.AgentRequest) {
				req.Tools = ""
			},
			want: "requires tools: none",
		},
		{
			name: "tools inherit",
			mutate: func(req *workflows.AgentRequest) {
				req.Tools = workflows.AgentToolsInherit
			},
			want: "requires tools: none",
		},
		{
			name: "tools mixed case",
			mutate: func(req *workflows.AgentRequest) {
				req.Tools = "NONE"
			},
			want: "requires tools: none",
		},
		{
			name: "durable key",
			mutate: func(req *workflows.AgentRequest) {
				req.Session = "workflow:must-not-be-created"
			},
			want: "cannot use a durable session key",
		},
	}

	runner := &workflowAgentRunner{loop: loop}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := workflowEphemeralTestRequest("Reject an unsafe tuple.")
			tt.mutate(&req)
			if _, err := runner.RunAgent(context.Background(), req); err == nil ||
				!strings.Contains(err.Error(), tt.want) {
				t.Fatalf("RunAgent() error = %v, want %q", err, tt.want)
			}
		})
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("provider calls = %d, want unsafe tuples rejected before provider I/O", len(calls))
	}
	workflowAssertSessionStoreUnchanged(t, agent, sessionsDir, beforeCatalog, beforeFiles)
}

func TestWorkflowAgentRunnerEphemeralPropagatesCancellationWithoutResidue(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{"unexpected"},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	sessionsDir := filepath.Join(agent.Workspace, "sessions")
	beforeCatalog := append([]string(nil), agent.Sessions.ListSessions()...)
	beforeFiles := workflowDirectoryFileSnapshot(t, sessionsDir)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, runErr := (&workflowAgentRunner{loop: loop}).RunAgent(
			ctx,
			workflowEphemeralTestRequest("Wait for cancellation."),
		)
		errCh <- runErr
	}()
	<-provider.started
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunAgent() error = %v, want context.Canceled", err)
	}
	workflowAssertSessionStoreUnchanged(t, agent, sessionsDir, beforeCatalog, beforeFiles)
	workflowAssertNoEphemeralActiveTurn(t, loop)
}

func TestWorkflowAgentRunnerEphemeralConcurrentInvocationsDoNotCollideOrPersist(t *testing.T) {
	const workers = 12
	provider := &workflowReadOnlyCaptureProvider{
		release: make(chan struct{}),
		called:  make(chan struct{}, workers),
	}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	sessionsDir := filepath.Join(agent.Workspace, "sessions")
	beforeCatalog := append([]string(nil), agent.Sessions.ListSessions()...)
	beforeFiles := workflowDirectoryFileSnapshot(t, sessionsDir)
	type result struct {
		outputs map[string]any
		err     error
	}
	results := make(chan result, workers)
	var keyMu sync.Mutex
	generatedKeys := make([]string, 0, workers)
	runner := &workflowAgentRunner{
		loop: loop,
		newEphemeralSessionKey: func() string {
			key := newWorkflowEphemeralSessionKey()
			keyMu.Lock()
			generatedKeys = append(generatedKeys, key)
			keyMu.Unlock()
			return key
		},
	}
	for index := range workers {
		go func() {
			outputs, err := runner.RunAgent(
				context.Background(),
				workflowEphemeralTestRequest(fmt.Sprintf("Concurrent decision %d.", index)),
			)
			results <- result{outputs: outputs, err: err}
		}()
	}
	for range workers {
		<-provider.called
	}
	calls := provider.snapshotCalls()
	if len(calls) != workers {
		t.Fatalf("provider calls while concurrent = %d, want %d", len(calls), workers)
	}
	for index, call := range calls {
		if call.toolCount != 0 || call.promptCachePresent ||
			workflowMessagesContain(call.messages, "existing problem context") {
			t.Fatalf("concurrent provider call %d was not isolated: %#v", index, call)
		}
	}
	close(provider.release)
	for range workers {
		got := <-results
		if got.err != nil {
			t.Fatalf("concurrent RunAgent() error = %v", got.err)
		}
		if got.outputs["session"] != workflows.AgentSessionEphemeral ||
			got.outputs["session_mode"] != workflows.AgentSessionEphemeral {
			t.Fatalf("concurrent session output = %#v, want opaque ephemeral mode", got.outputs)
		}
	}

	keyMu.Lock()
	actualKeys := append([]string(nil), generatedKeys...)
	keyMu.Unlock()
	if len(actualKeys) != workers {
		t.Fatalf("ephemeral key generations = %d, want one per request (%d)", len(actualKeys), workers)
	}
	keys := make(map[string]struct{}, workers)
	for _, key := range actualKeys {
		if !strings.HasPrefix(key, "workflow:ephemeral:") {
			t.Fatalf("ephemeral internal key = %q, want namespaced key", key)
		}
		if _, exists := keys[key]; exists {
			t.Fatalf("duplicate ephemeral internal key %q", key)
		}
		keys[key] = struct{}{}
	}
	workflowAssertSessionStoreUnchanged(t, agent, sessionsDir, beforeCatalog, beforeFiles)
	workflowAssertNoEphemeralActiveTurn(t, loop)
}

func TestWorkflowAgentRunnerLiteralEphemeralSessionKeyRemainsDurable(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"durable answer"}}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflows.AgentRequest{
			AgentID: "main",
			Prompt:  "Persist this literal durable session.",
			Session: workflows.AgentSessionEphemeral,
			History: "inherit",
			Cache:   "session",
			Tools:   workflows.AgentToolsNone,
		},
	)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["session"] != workflows.AgentSessionEphemeral {
		t.Fatalf("session = %#v, want literal durable key", outputs["session"])
	}
	if _, exists := outputs["session_mode"]; exists {
		t.Fatalf("literal key was misclassified as ephemeral mode: %#v", outputs)
	}
	history := agent.Sessions.GetHistory(workflows.AgentSessionEphemeral)
	if !workflowMessagesContain(history, "Persist this literal durable session.") ||
		!workflowMessagesContain(history, "durable answer") {
		t.Fatalf("literal durable session history = %#v, want request and response", history)
	}
	if !workflowStringSliceContains(agent.Sessions.ListSessions(), workflows.AgentSessionEphemeral) {
		t.Fatalf("session catalog = %#v, want literal ephemeral key", agent.Sessions.ListSessions())
	}
	metadata := agent.Sessions.(session.MetadataAwareSessionStore)
	scope := metadata.GetSessionScope(workflows.AgentSessionEphemeral)
	if scope == nil || scope.Values["workflow_session"] != workflows.AgentSessionEphemeral {
		t.Fatalf("literal durable session scope = %#v, want persisted workflow metadata", scope)
	}
	files := workflowDirectoryFileSnapshot(t, filepath.Join(agent.Workspace, "sessions"))
	if _, ok := files["ephemeral.jsonl"]; !ok {
		t.Fatalf("session files = %#v, want ephemeral.jsonl", files)
	}
	if _, ok := files["ephemeral.meta.json"]; !ok {
		t.Fatalf("session files = %#v, want ephemeral.meta.json", files)
	}
}

func TestWorkflowAgentRunnerEphemeralDisablesAccountRouterSessionAffinity(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.AccountRef = "decision-router"
	cfg.Agents.Defaults.ModelName = "decision"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "decision", Model: "mock-model"}}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "account-a",
			Provider:  "openai",
			Model:     "mock-model",
			APIBase:   "http://example.invalid/v1",
			APIKeys:   config.SimpleSecureStrings("test-key-a"),
			Enabled:   true,
		},
		{
			ModelName: "account-b",
			Provider:  "openai",
			Model:     "mock-model",
			APIBase:   "http://example.invalid/v1",
			APIKeys:   config.SimpleSecureStrings("test-key-b"),
			Enabled:   true,
		},
	}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "decision-router",
		Enabled: true,
		Entry:   "pool",
		Blocks: []config.AccountRouterBlock{{
			ID:       "pool",
			Type:     config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-b"},
			Strategy: config.AccountRouterStrategyTokensSpent,
		}},
	}}
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"routed decision"}}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	loop.providerFactory = func(*config.ModelConfig) (providers.LLMProvider, string, error) {
		return provider, "mock-model", nil
	}
	agent := loop.GetRegistry().GetDefaultAgent()
	if agent == nil || agent.AccountRouter == nil {
		t.Fatalf("account-routed agent = %#v, want active router", agent)
	}
	statePath := filepath.Join(workspace, "account_router_state.json")
	beforeRouterSessions := workflowRawMessageKeySet(
		workflowAccountRouterSessionState(t, statePath, "decision-router"),
	)
	beforeCatalog := append([]string(nil), agent.Sessions.ListSessions()...)
	beforeFiles := workflowDirectoryFileSnapshot(t, filepath.Join(workspace, "sessions"))

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflowEphemeralTestRequest("Make an account-routed isolated decision."),
	)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["session_mode"] != workflows.AgentSessionEphemeral {
		t.Fatalf("session mode = %#v, want ephemeral", outputs["session_mode"])
	}
	afterRouterSessions := workflowRawMessageKeySet(
		workflowAccountRouterSessionState(t, statePath, "decision-router"),
	)
	if !reflect.DeepEqual(afterRouterSessions, beforeRouterSessions) {
		t.Fatalf(
			"ephemeral run changed router session affinity: before=%#v after=%#v",
			beforeRouterSessions,
			afterRouterSessions,
		)
	}
	stateData, readErr := os.ReadFile(statePath)
	if readErr != nil {
		t.Fatalf("read account router state: %v", readErr)
	}
	if strings.Contains(string(stateData), "workflow:ephemeral:") {
		t.Fatalf("account router state leaked internal ephemeral identity: %s", stateData)
	}
	workflowAssertSessionStoreUnchanged(
		t,
		agent,
		filepath.Join(workspace, "sessions"),
		beforeCatalog,
		beforeFiles,
	)
}

func TestWorkflowAgentRunnerPrivateEphemeralFallbackRedactsAccountRouterErrors(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.AccountRef = "decision-router"
	cfg.Agents.Defaults.ModelName = "decision"
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "decision", Model: "mock-model"}}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "account-a",
			Provider:  "openai",
			Model:     "mock-model",
			APIBase:   "http://example.invalid/v1",
			APIKeys:   config.SimpleSecureStrings("test-key-a"),
			Enabled:   true,
		},
		{
			ModelName: "account-b",
			Provider:  "openai",
			Model:     "mock-model",
			APIBase:   "http://example.invalid/v1",
			APIKeys:   config.SimpleSecureStrings("test-key-b"),
			Enabled:   true,
		},
	}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "decision-router",
		Enabled: true,
		Entry:   "primary",
		Blocks: []config.AccountRouterBlock{
			{
				ID:       "primary",
				Type:     config.AccountRouterBlockTypeAccount,
				Account:  "account-a",
				Fallback: "backup",
			},
			{
				ID:      "backup",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "account-b",
			},
		},
	}}
	const privateCanary = "PRIVATE-WORKFLOW-PROVIDER-ERROR-CANARY"
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&workflowPrivateAccountProvider{response: "unused"},
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	loop.providerFactory = func(modelConfig *config.ModelConfig) (providers.LLMProvider, string, error) {
		if modelConfig != nil && modelConfig.APIKey() == "test-key-a" {
			return &workflowPrivateAccountProvider{
				err: errors.New("rate limit echoed " + privateCanary),
			}, "mock-model", nil
		}
		return &workflowPrivateAccountProvider{response: "private fallback decision"}, "mock-model", nil
	}
	agent := loop.GetRegistry().GetDefaultAgent()
	if agent == nil || agent.AccountRouter == nil {
		t.Fatalf("account-routed agent = %#v, want active router", agent)
	}

	req := workflowEphemeralTestRequest("Evaluate private findings with fallback.")
	req.PrivateContext = true
	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["text"] != "private fallback decision" {
		t.Fatalf("text = %#v, want fallback decision", outputs["text"])
	}
	statePath := filepath.Join(workspace, "account_router_state.json")
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read account router state: %v", err)
	}
	if strings.Contains(string(stateData), privateCanary) {
		t.Fatalf("account router state leaked private provider error: %s", stateData)
	}
	if !strings.Contains(string(stateData), "provider request failed") {
		t.Fatalf("account router state omitted canonical private failure: %s", stateData)
	}
}

func TestWorkflowAgentRunnerPrivateVisionRetrySuppressesRuntimeErrorEvent(t *testing.T) {
	const privateCanary = "PRIVATE-VISION-ERROR-CANARY"
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{"", "private decision"},
		errors: []error{fmt.Errorf(
			"%s: no endpoints found that support image input",
			privateCanary,
		)},
	}
	loop, agent, canonicalKey, alias := newWorkflowReadOnlyTestLoop(t, provider)
	agent.Sessions.SetHistory(canonicalKey, []providers.Message{{
		Role:    "user",
		Content: "Review this private screenshot.",
		Media: []string{
			"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=",
		},
	}})
	runner := &workflowAgentRunner{loop: loop}
	frozen, err := runner.CaptureReadOnlySession(context.Background(), workflows.ReadOnlySessionRef{
		AgentID: "main",
		Session: alias,
	})
	if err != nil {
		t.Fatalf("CaptureReadOnlySession() error = %v", err)
	}
	eventStream, closeEvents := subscribeRuntimeEventsForTest(
		t,
		loop,
		8,
		runtimeevents.KindAgentLLMRetry,
	)
	defer closeEvents()

	outputs, err := runner.RunAgent(context.Background(), workflows.AgentRequest{
		AgentID:               "main",
		Prompt:                "Make a private decision.",
		History:               "read_only",
		Cache:                 "session",
		Tools:                 workflows.AgentToolsNone,
		FrozenReadOnlySession: frozen,
	})
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["text"] != "private decision" {
		t.Fatalf("text = %#v, want private decision", outputs["text"])
	}
	calls := provider.snapshotCalls()
	if len(calls) != 2 || !hasMediaRefs(calls[0].messages) || hasMediaRefs(calls[1].messages) {
		t.Fatalf("vision retry calls = %#v, want media then stripped media", calls)
	}
	if events := collectRuntimeEventStream(eventStream); len(events) != 0 {
		encoded, marshalErr := json.Marshal(events)
		if marshalErr != nil {
			t.Fatalf("json.Marshal(events) error = %v", marshalErr)
		}
		t.Fatalf("private vision retry emitted runtime events: %s", encoded)
	}
}

func TestWorkflowAgentRunnerEphemeralFallbackGetsDetachedMessagesAndOptions(t *testing.T) {
	workspace := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "primary-model"
	cfg.Agents.Defaults.ModelFallbacks = []string{"openai/fallback-model"}
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "primary-model",
		Provider:  "openai",
		Model:     "primary-model",
		Enabled:   true,
	}}
	state := &workflowEphemeralFallbackState{}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&workflowEphemeralFallbackProvider{model: "primary-model", state: state},
	)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	loop.providerFactory = func(modelConfig *config.ModelConfig) (providers.LLMProvider, string, error) {
		model := ""
		if modelConfig != nil {
			_, model = providers.ExtractProtocol(modelConfig)
		}
		return &workflowEphemeralFallbackProvider{model: model, state: state}, model, nil
	}

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflowEphemeralTestRequest("Use a clean fallback request."),
	)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["text"] != "fallback decision" {
		t.Fatalf("text = %#v, want fallback decision", outputs["text"])
	}
	calls := state.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("fallback provider calls = %d, want primary plus fallback", len(calls))
	}
	if calls[0].model != "primary-model" || calls[1].model != "fallback-model" {
		t.Fatalf("fallback models = %#v, want primary then fallback", calls)
	}
	if workflowMessagesContain(calls[1].messages, "primary-provider-mutated") {
		t.Fatalf("fallback observed primary message mutation: %#v", calls[1].messages)
	}
	if calls[1].promptCachePresent || calls[1].promptCacheKey != "" {
		t.Fatalf("fallback observed primary option mutation: %#v", calls[1])
	}
	if workflowMessagesHavePromptCacheControl(calls[1].messages) {
		t.Fatalf("fallback messages retained prompt cache controls: %#v", calls[1].messages)
	}

	agent := loop.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	loop.fallback = providers.NewFallbackChain(providers.NewCooldownTracker(), nil)
	response, err := loop.askSideQuestionWithOptions(
		context.Background(),
		agent,
		&processOptions{
			Dispatch: DispatchRequest{
				SessionKey:  "workflow:ephemeral:test-only",
				UserMessage: "Preserve the explicit fallback effort.",
			},
			ReasoningEffortOverride: "high",
			NoHistory:               true,
			DisableTools:            true,
			DisablePromptCache:      true,
		},
		"Preserve the explicit fallback effort.",
		sideQuestionExecutionOptions{
			disablePromptCache:     true,
			disableSessionAffinity: true,
			detachProviderMessages: true,
			skipHooks:              true,
			rejectToolCalls:        true,
		},
	)
	if err != nil {
		t.Fatalf("askSideQuestionWithOptions() error = %v", err)
	}
	if response != "fallback decision" {
		t.Fatalf("fallback response = %q, want fallback decision", response)
	}
	calls = state.snapshotCalls()
	if len(calls) != 4 {
		t.Fatalf("provider calls after explicit override = %d, want 4", len(calls))
	}
	for index, call := range calls[2:] {
		if call.reasoningEffort != "high" {
			t.Fatalf("explicit fallback call %d reasoning_effort = %q, want high", index, call.reasoningEffort)
		}
	}
}

func TestWorkflowAgentRunnerEphemeralManagedCalibrationChildrenAndRepairStayIsolated(t *testing.T) {
	var responseMu sync.Mutex
	var repairScope []string
	repairInjected := false
	provider := &workflowReadOnlyCaptureProvider{}
	provider.respond = func(_ int, messages []providers.Message) string {
		message := workflowLatestUserMessage(messages)
		responseMu.Lock()
		defer responseMu.Unlock()
		if strings.Contains(message, "previous response did not satisfy") {
			ids := append([]string(nil), repairScope...)
			repairScope = nil
			return workflowManagedTestFindingsJSON(ids)
		}
		ids := workflowScopeIDsInMessage(message, "a", "b", "c")
		if strings.Contains(message, "Agent execution optimization child task") &&
			strings.Contains(message, " of 3.") && !repairInjected {
			repairInjected = true
			repairScope = append([]string(nil), ids...)
			return "not json"
		}
		return workflowManagedTestFindingsJSON(ids)
	}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	sessionsDir := filepath.Join(agent.Workspace, "sessions")
	beforeCatalog := append([]string(nil), agent.Sessions.ListSessions()...)
	beforeFiles := workflowDirectoryFileSnapshot(t, sessionsDir)
	req := workflowEphemeralTestRequest("Review every assigned scope item.")
	req.Managed = map[string]any{
		"mode":                  "auto",
		"strategy":              "scope_split",
		"max_items_per_chunk":   1,
		"max_parallel_children": 1,
		"calibration": map[string]any{
			"enabled":       true,
			"sample_size":   2,
			"cache_enabled": true,
		},
	}
	req.Scope = []any{
		map[string]any{"id": "a"},
		map[string]any{"id": "b"},
		map[string]any{"id": "c"},
	}
	req.Output = workflowManagedTestOutputContract()

	keyCalls := 0
	runner := &workflowAgentRunner{
		loop: loop,
		newEphemeralSessionKey: func() string {
			keyCalls++
			return newWorkflowEphemeralSessionKey()
		},
	}
	outputs, err := runner.RunAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if keyCalls != 1 {
		t.Fatalf("ephemeral key generations = %d, want one across calibration, children, and repair", keyCalls)
	}
	workflowAssertEphemeralManagedOutputEnvelope(t, outputs)
	if outputs["structured_valid"] != true || outputs["structured_repairs"] != 1 {
		t.Fatalf("managed structured audit = %#v, want valid output with one real-child repair", outputs)
	}
	structured, ok := outputs["structured"].(map[string]any)
	if !ok {
		t.Fatalf("structured output = %#v, want object", outputs["structured"])
	}
	findings, ok := structured["findings"].([]any)
	if !ok || len(findings) != 3 {
		t.Fatalf("combined findings = %#v, want three findings", structured["findings"])
	}
	managed := outputs["managed"].(map[string]any)
	calibration := managed["calibration"].(map[string]any)
	if calibration["status"] != "passed" || calibration["match"] != true ||
		calibration["sample_scope"] != 2 || calibration["repairs"] != 0 {
		t.Fatalf("calibration = %#v, want clean two-item pass", calibration)
	}
	children, ok := outputs["managed_children"].([]map[string]any)
	if !ok || len(children) != 3 {
		t.Fatalf("managed_children = %#v, want three real children", outputs["managed_children"])
	}
	repairedChildren := 0
	for index, child := range children {
		if child["valid"] != true || child["tools"] != workflows.AgentToolsNone {
			t.Fatalf("managed child %d = %#v, want valid no-tools result", index, child)
		}
		if child["repairs"] == 1 {
			repairedChildren++
		} else if child["repairs"] != 0 {
			t.Fatalf("managed child %d repairs = %#v, want 0 or 1", index, child["repairs"])
		}
	}
	if repairedChildren != 1 {
		t.Fatalf("repaired real children = %d, want 1; children=%#v", repairedChildren, children)
	}

	calls := provider.snapshotCalls()
	workflowAssertEphemeralProviderCallsIsolated(t, calls)
	var baselineCalls, sampledChildCalls, realChildCalls, repairCalls int
	for _, call := range calls {
		switch {
		case workflowMessagesContain(call.messages, "Calibration label: grouped baseline."):
			if call.reasoningEffort != "" {
				t.Fatalf("calibration baseline reasoning_effort = %q, want no optimized override", call.reasoningEffort)
			}
			baselineCalls++
		case workflowMessagesContain(call.messages, "previous response did not satisfy"):
			if call.reasoningEffort != "low" {
				t.Fatalf("managed repair reasoning_effort = %q, want low", call.reasoningEffort)
			}
			repairCalls++
		case workflowMessagesContain(call.messages, "Agent execution optimization child task") &&
			workflowMessagesContain(call.messages, " of 2."):
			if call.reasoningEffort != "" {
				t.Fatalf("sampled child reasoning_effort = %q, want no optimized override", call.reasoningEffort)
			}
			sampledChildCalls++
		case workflowMessagesContain(call.messages, "Agent execution optimization child task") &&
			workflowMessagesContain(call.messages, " of 3."):
			if call.reasoningEffort != "low" {
				t.Fatalf("real managed child reasoning_effort = %q, want low", call.reasoningEffort)
			}
			realChildCalls++
		default:
			t.Fatalf("unclassified managed provider call: %#v", call.messages)
		}
	}
	if len(calls) != 7 || baselineCalls != 1 || sampledChildCalls != 2 ||
		realChildCalls != 3 || repairCalls != 1 {
		t.Fatalf(
			"managed provider calls = total:%d baseline:%d sampled:%d real:%d repair:%d, want 7/1/2/3/1",
			len(calls),
			baselineCalls,
			sampledChildCalls,
			realChildCalls,
			repairCalls,
		)
	}
	workflowAssertNoInternalEphemeralIdentity(t, outputs, agent.managedCalibrationCache)
	workflowAssertSessionStoreUnchanged(t, agent, sessionsDir, beforeCatalog, beforeFiles)
}

func TestWorkflowAgentRunnerEphemeralManagedCalibrationMismatchFallbackStaysIsolated(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{}
	provider.respond = func(_ int, messages []providers.Message) string {
		message := workflowLatestUserMessage(messages)
		ids := workflowScopeIDsInMessage(message, "a", "b")
		if strings.Contains(message, "Agent execution optimization child task") &&
			len(ids) == 1 && ids[0] == "b" {
			return workflowManagedTestFindingsJSON([]string{"mismatched-b"})
		}
		return workflowManagedTestFindingsJSON(ids)
	}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	sessionsDir := filepath.Join(agent.Workspace, "sessions")
	beforeCatalog := append([]string(nil), agent.Sessions.ListSessions()...)
	beforeFiles := workflowDirectoryFileSnapshot(t, sessionsDir)
	req := workflowEphemeralTestRequest("Review the complete scope after calibration.")
	req.Managed = map[string]any{
		"mode":                  "auto",
		"strategy":              "scope_split",
		"max_items_per_chunk":   1,
		"max_parallel_children": 1,
		"calibration": map[string]any{
			"enabled":       true,
			"sample_size":   2,
			"cache_enabled": false,
		},
	}
	req.Scope = []any{
		map[string]any{"id": "a"},
		map[string]any{"id": "b"},
	}
	req.Output = workflowManagedTestOutputContract()

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), req)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	workflowAssertEphemeralManagedOutputEnvelope(t, outputs)
	if outputs["structured_valid"] != true || outputs["structured_repairs"] != 0 {
		t.Fatalf("fallback structured audit = %#v, want valid unrepaired output", outputs)
	}
	if _, exists := outputs["managed_children"]; exists {
		t.Fatalf("real managed children ran after calibration mismatch: %#v", outputs["managed_children"])
	}
	structured := outputs["structured"].(map[string]any)
	findings, ok := structured["findings"].([]any)
	if !ok || len(findings) != 2 {
		t.Fatalf("fallback findings = %#v, want full two-item result", structured["findings"])
	}
	managed := outputs["managed"].(map[string]any)
	calibration := managed["calibration"].(map[string]any)
	if calibration["status"] != "failed" || calibration["match"] != false ||
		calibration["phase"] != "compare" {
		t.Fatalf("calibration = %#v, want comparison mismatch fallback", calibration)
	}

	calls := provider.snapshotCalls()
	workflowAssertEphemeralProviderCallsIsolated(t, calls)
	var baselineCalls, sampledChildCalls, fallbackCalls int
	for _, call := range calls {
		switch {
		case workflowMessagesContain(call.messages, "Calibration label: grouped baseline."):
			baselineCalls++
		case workflowMessagesContain(call.messages, "Agent execution optimization child task"):
			sampledChildCalls++
		default:
			fallbackCalls++
		}
	}
	if len(calls) != 4 || baselineCalls != 1 || sampledChildCalls != 2 || fallbackCalls != 1 {
		t.Fatalf(
			"mismatch provider calls = total:%d baseline:%d sampled:%d fallback:%d, want 4/1/2/1",
			len(calls),
			baselineCalls,
			sampledChildCalls,
			fallbackCalls,
		)
	}
	workflowAssertSessionStoreUnchanged(t, agent, sessionsDir, beforeCatalog, beforeFiles)
}

func TestWorkflowSessionSnapshotRevisionIncludesProviderVisibleInternalFields(t *testing.T) {
	base := session.SessionSnapshot{
		Key: "agent:main:direct:revision",
		History: []providers.Message{{
			Role:         "assistant",
			Content:      "prior call",
			PromptLayer:  "history",
			PromptSlot:   "conversation",
			PromptSource: "session",
			SystemParts: []providers.ContentBlock{{
				Type:         "text",
				Text:         "policy",
				PromptLayer:  "policy",
				PromptSlot:   "repo",
				PromptSource: "workspace",
			}},
			ToolCalls: []providers.ToolCall{{
				ID:               "call-1",
				Name:             "inspect-a",
				Arguments:        map[string]any{"path": "a.go"},
				ThoughtSignature: "signature-a",
				Function: &providers.FunctionCall{
					Name:      "same-json-function",
					Arguments: `{}`,
				},
			}},
		}},
	}
	first, err := workflowSessionSnapshotRevision(base)
	if err != nil {
		t.Fatalf("workflowSessionSnapshotRevision(base) error = %v", err)
	}

	changed := base
	changed.History = session.CloneMessages(base.History)
	changed.History[0].ToolCalls[0].Name = "inspect-b"
	changed.History[0].ToolCalls[0].Arguments["path"] = "b.go"
	changed.History[0].ToolCalls[0].ThoughtSignature = "signature-b"
	changed.History[0].PromptSource = "different-session-source"
	changed.History[0].SystemParts[0].PromptSource = "different-policy-source"
	second, err := workflowSessionSnapshotRevision(changed)
	if err != nil {
		t.Fatalf("workflowSessionSnapshotRevision(changed) error = %v", err)
	}
	if first == second {
		t.Fatalf("provider-visible internal field changes produced identical revision %q", first)
	}

	nilArguments := base
	nilArguments.History = session.CloneMessages(base.History)
	nilArguments.History[0].ToolCalls[0].Arguments = nil
	nilRevision, err := workflowSessionSnapshotRevision(nilArguments)
	if err != nil {
		t.Fatalf("workflowSessionSnapshotRevision(nil arguments) error = %v", err)
	}
	emptyArguments := base
	emptyArguments.History = session.CloneMessages(base.History)
	emptyArguments.History[0].ToolCalls[0].Arguments = map[string]any{}
	emptyRevision, err := workflowSessionSnapshotRevision(emptyArguments)
	if err != nil {
		t.Fatalf("workflowSessionSnapshotRevision(empty arguments) error = %v", err)
	}
	if nilRevision == emptyRevision {
		t.Fatalf("nil and empty tool arguments produced identical revision %q", nilRevision)
	}

	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	changed.History[0].ToolCalls[0].Arguments = cyclic
	if revision, err := workflowSessionSnapshotRevision(changed); err == nil || revision != "" {
		t.Fatalf("cyclic snapshot revision = (%q, %v), want serialization failure", revision, err)
	}
}

func TestWorkflowAgentRunnerEnforcesAndAuditsToolsMode(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	provider := &workflowToolsCaptureProvider{responses: []string{
		`{"category":"bug","model_prose":"post this verbatim"}`,
		`{"category":"bug"}`,
		"classified",
	}}
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, provider)
	defer al.Close()
	defaultAgent := al.GetRegistry().GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("default agent is nil")
	}
	defaultAgent.Tools.Register(&workflowHandledMediaTool{})

	runner := &workflowAgentRunner{loop: al}
	isolated, err := runner.RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "main",
		Prompt:  "Classify untrusted content.",
		History: "none",
		Tools:   workflows.AgentToolsNone,
		Output: &workflows.AgentOutputContract{
			Format:         "json",
			RepairAttempts: 1,
			Schema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"category"},
				"properties": map[string]any{
					"category": map[string]any{
						"type": "string",
						"enum": []any{"bug", "other"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("isolated RunAgent() error = %v", err)
	}
	inherited, err := runner.RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "main",
		Prompt:  "Use configured tools.",
		History: "none",
		Tools:   workflows.AgentToolsInherit,
	})
	if err != nil {
		t.Fatalf("inherited RunAgent() error = %v", err)
	}

	calls := provider.ToolCounts()
	if len(calls) != 3 {
		t.Fatalf("provider calls = %v, want isolated request, repair, and inherited request", calls)
	}
	if calls[0] != 0 || calls[1] != 0 {
		t.Fatalf("isolated provider tool counts = %v, want 0 for request and repair", calls[:2])
	}
	if calls[2] == 0 {
		t.Fatal("inherited provider tool count = 0, want configured tools")
	}
	if isolated["tools"] != workflows.AgentToolsNone {
		t.Fatalf("isolated tools audit = %#v, want none", isolated["tools"])
	}
	if isolated["structured_repairs"] != 1 ||
		isolated["structured_valid"] != true {
		t.Fatalf("isolated structured audit = %#v, want one successful repair", isolated)
	}
	if inherited["tools"] != workflows.AgentToolsInherit {
		t.Fatalf("inherited tools audit = %#v, want inherit", inherited["tools"])
	}
}

func TestWorkflowManagedChildrenDisableTools(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled": false,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		},
		Output: contract,
	}
	seen := make([]workflowAgentRunOptions, 0)
	runOnce := func(message string, _ bool, options workflowAgentRunOptions) (string, error) {
		seen = append(seen, options)
		ids := workflowTestScopeIDs(t, message)
		findings := make([]string, 0, len(ids))
		for _, id := range ids {
			findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"title":"finding %s"}`, id, id))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("child run count = %d, want 3", len(seen))
	}
	for i, options := range seen {
		if !options.NoTools {
			t.Fatalf("child %d run options = %#v, want NoTools", i, options)
		}
	}
	children := outputs["managed_children"].([]map[string]any)
	for i, child := range children {
		if child["tools"] != workflows.AgentToolsNone {
			t.Fatalf("child %d tools audit = %#v, want none", i, child["tools"])
		}
	}
}

func TestWorkflowManagedSplitStrategyRequiresStructuredOutputAndSplittableDimensions(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	scope := []any{
		map[string]any{"id": "a"},
		map[string]any{"id": "b"},
		map[string]any{"id": "c"},
	}
	agentWithTasks := &AgentInstance{
		Definition: AgentContextDefinition{Agent: &AgentPromptDefinition{Tasks: []string{
			"Find correctness bugs.",
			"Find security risks.",
			"Find missing tests.",
		}}},
	}
	tests := []struct {
		name  string
		req   workflows.AgentRequest
		agent *AgentInstance
		want  string
	}{
		{
			name: "managed off",
			req: workflows.AgentRequest{
				Managed: "off",
				Scope:   scope,
				Output:  contract,
			},
			agent: agentWithTasks,
		},
		{
			name: "missing structured output",
			req: workflows.AgentRequest{
				Managed: map[string]any{"mode": "auto", "max_items_per_chunk": 2},
				Scope:   scope,
			},
			agent: agentWithTasks,
		},
		{
			name: "auto prefers hybrid",
			req: workflows.AgentRequest{
				Managed: map[string]any{
					"mode":                "auto",
					"max_items_per_chunk": 2,
					"max_tasks_per_chunk": 2,
				},
				Scope:  scope,
				Output: contract,
			},
			agent: agentWithTasks,
			want:  "hybrid_split",
		},
		{
			name: "auto scope only",
			req: workflows.AgentRequest{
				Managed: map[string]any{"mode": "auto", "max_items_per_chunk": 2},
				Scope:   scope,
				Output:  contract,
			},
			agent: &AgentInstance{},
			want:  "scope_split",
		},
		{
			name: "auto task only",
			req: workflows.AgentRequest{
				Managed: map[string]any{"mode": "auto", "max_tasks_per_chunk": 2},
				Output:  contract,
			},
			agent: agentWithTasks,
			want:  "task_split",
		},
		{
			name: "requested scope alias",
			req: workflows.AgentRequest{
				Managed: map[string]any{
					"strategy":            "by_scope",
					"max_items_per_chunk": 2,
				},
				Scope:  scope,
				Output: contract,
			},
			agent: agentWithTasks,
			want:  "scope_split",
		},
		{
			name: "requested task ignored when not splittable",
			req: workflows.AgentRequest{
				Managed: map[string]any{
					"strategy":            "task_split",
					"max_tasks_per_chunk": 10,
				},
				Output: contract,
			},
			agent: agentWithTasks,
		},
		{
			name: "requested none",
			req: workflows.AgentRequest{
				Managed: map[string]any{"split": "none"},
				Scope:   scope,
				Output:  contract,
			},
			agent: agentWithTasks,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workflowManagedSplitStrategy(tt.req, tt.agent)
			if got != tt.want {
				t.Fatalf("workflowManagedSplitStrategy() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkflowManagedScopeChunkingUsesInternalTokenTarget(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Scope: []any{
			map[string]any{"id": "a", "content": strings.Repeat("a", 2048)},
			map[string]any{"id": "b", "content": strings.Repeat("b", 2048)},
			map[string]any{"id": "c", "content": strings.Repeat("c", 2048)},
		},
		Output: contract,
	}
	singleItemTokens := workflowScopeChunkPromptTokens(req, workflowScopeItems(req.Scope)[:1])
	options := workflowManagedOptions(map[string]any{
		"mode":                "auto",
		"max_items_per_chunk": 8,
	})
	options.targetChildPromptTokens = singleItemTokens + 1

	chunks := workflowManagedScopeChunks(req, options)
	counts := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		counts = append(counts, len(chunk))
	}
	if fmt.Sprint(counts) != "[1 1 1]" {
		t.Fatalf("chunk counts = %#v, want [1 1 1]", counts)
	}
}

func TestWorkflowManagedAdaptiveScopeChunkingPacksLargerChunks(t *testing.T) {
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Scope: []any{
			map[string]any{"id": "a", "content": strings.Repeat("a", 128)},
			map[string]any{"id": "b", "content": strings.Repeat("b", 128)},
			map[string]any{"id": "c", "content": strings.Repeat("c", 128)},
			map[string]any{"id": "d", "content": strings.Repeat("d", 128)},
			map[string]any{"id": "e", "content": strings.Repeat("e", 128)},
		},
		Output: workflowManagedTestOutputContract(),
	}
	scope := workflowScopeItems(req.Scope)
	twoItemTokens := workflowScopeChunkPromptTokens(req, scope[:2])
	options := workflowManagedOptions(map[string]any{
		"mode":                "auto",
		"max_items_per_chunk": 8,
	})
	options.targetChildPromptTokens = twoItemTokens

	chunks := workflowManagedScopeChunks(req, options)
	counts := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		counts = append(counts, len(chunk))
	}
	if fmt.Sprint(counts) != "[2 2 1]" {
		t.Fatalf("chunk counts = %#v, want [2 2 1]", counts)
	}
}

func TestWorkflowManagedTargetChildPromptTokensIsInternal(t *testing.T) {
	options := workflowManagedOptions(map[string]any{
		"mode":                       "auto",
		"target_child_prompt_tokens": 999,
		"targetChildPromptTokens":    1000,
	})
	if options.targetChildPromptTokens != 0 {
		t.Fatalf("targetChildPromptTokens = %d, want internal unset value", options.targetChildPromptTokens)
	}

	agent := &AgentInstance{ContextWindow: 64000, MaxTokens: 8000}
	resolved := agent.workflowManagedResolveChildPromptTarget(
		workflows.AgentRequest{},
		options,
		"scope_split",
	)
	if resolved.targetChildPromptTokens != 28000 {
		t.Fatalf("resolved targetChildPromptTokens = %d, want 28000", resolved.targetChildPromptTokens)
	}
	if resolved.targetChildPromptSource != "context_window" {
		t.Fatalf("targetChildPromptSource = %q, want context_window", resolved.targetChildPromptSource)
	}
}

func TestWorkflowManagedSplitUsesAssignedTasksForStrategyAndMetadata(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Managed: map[string]any{
			"mode":                "auto",
			"max_tasks_per_chunk": 2,
		},
		Context: workflowManagedTaskMessage([]string{
			"Review correctness.",
			"Review security.",
			"Review test coverage.",
		}),
		Output: contract,
	}
	agent := &AgentInstance{}

	strategy := workflowManagedSplitStrategy(req, agent)
	if strategy != "task_split" {
		t.Fatalf("workflowManagedSplitStrategy() = %q, want task_split", strategy)
	}
	options := workflowManagedOptions(req.Managed)
	plans := workflowManagedChildPlans(req, agent, options, strategy)
	if len(plans) != 2 {
		t.Fatalf("plans len = %d, want 2", len(plans))
	}
	split := workflowManagedSplitMetadata(req, agent, options, strategy, plans)
	if split["task_count"] != 3 {
		t.Fatalf("split task_count = %#v, want 3", split["task_count"])
	}
	counts, ok := split["child_task_counts"].([]int)
	if !ok || len(counts) != 2 || counts[0] != 2 || counts[1] != 1 {
		t.Fatalf("child_task_counts = %#v, want [2 1]", split["child_task_counts"])
	}
}

func TestWorkflowManagedTaskSplitCombinesStructuredOutputs(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned task subset.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_tasks_per_chunk": 2,
			"calibration": map[string]any{
				"enabled":          true,
				"task_sample_size": 3,
			},
		},
		Output: contract,
	}
	agent := &AgentInstance{
		ID:    "reviewer",
		Model: "mock-model",
		Definition: AgentContextDefinition{Agent: &AgentPromptDefinition{Tasks: []string{
			"Find correctness bugs.",
			"Find security risks.",
			"Find missing tests.",
			"Find performance issues.",
			"Find API contract issues.",
		}}},
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		tasks := workflowTestAssignedTasks(t, message)
		findings := make([]string, 0, len(tasks))
		for _, task := range tasks {
			findings = append(findings, fmt.Sprintf(`{"task":%q,"title":"%s"}`, task, task))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(tasks, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		agent,
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"task_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 5 {
		t.Fatalf("findings = %#v, want five task findings", findings)
	}
	managed := outputs["managed"].(map[string]any)
	if managed["strategy"] != "task_split" {
		t.Fatalf("strategy = %#v, want task_split", managed["strategy"])
	}
	split := managed["split"].(map[string]any)
	if split["child_count"] != 3 {
		t.Fatalf("split child_count = %#v, want 3", split["child_count"])
	}
}

func TestWorkflowManagedHybridSplitCombinesStructuredOutputs(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope and task subset.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_items_per_chunk": 2,
			"max_tasks_per_chunk": 2,
			"calibration": map[string]any{
				"enabled":          true,
				"sample_size":      3,
				"task_sample_size": 3,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
			map[string]any{"id": "d"},
		},
		Output: contract,
	}
	agent := &AgentInstance{
		ID:    "reviewer",
		Model: "mock-model",
		Definition: AgentContextDefinition{Agent: &AgentPromptDefinition{Tasks: []string{
			"Find correctness bugs.",
			"Find security risks.",
			"Find missing tests.",
		}}},
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		ids := workflowTestScopeIDs(t, message)
		tasks := workflowTestAssignedTasks(t, message)
		findings := make([]string, 0, len(ids)*len(tasks))
		for _, id := range ids {
			for _, task := range tasks {
				findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"task":%q}`, id, task))
			}
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		agent,
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"hybrid_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 12 {
		t.Fatalf("findings = %#v, want twelve hybrid findings", findings)
	}
	managed := outputs["managed"].(map[string]any)
	if managed["strategy"] != "hybrid_split" {
		t.Fatalf("strategy = %#v, want hybrid_split", managed["strategy"])
	}
	split := managed["split"].(map[string]any)
	if split["child_count"] != 4 {
		t.Fatalf("split child_count = %#v, want 4", split["child_count"])
	}
	if fmt.Sprint(split["child_scope_counts"]) != "[2 2 2 2]" {
		t.Fatalf("child_scope_counts = %#v, want [2 2 2 2]", split["child_scope_counts"])
	}
	if fmt.Sprint(split["child_task_counts"]) != "[2 1 2 1]" {
		t.Fatalf("child_task_counts = %#v, want [2 1 2 1]", split["child_task_counts"])
	}
	calibration := managed["calibration"].(map[string]any)
	if calibration["status"] != "passed" || calibration["sample_scope"] != 3 || calibration["sample_tasks"] != 3 {
		t.Fatalf("calibration = %#v, want passed three-scope three-task sample", calibration)
	}
	children := outputs["managed_children"].([]map[string]any)
	if len(children) != 4 {
		t.Fatalf("managed_children len = %d, want 4", len(children))
	}
	for _, child := range children {
		label := fmt.Sprint(child["label"])
		if !strings.Contains(label, "scope chunk") || !strings.Contains(label, "task chunk") {
			t.Fatalf("child label = %q, want both split axes", label)
		}
		if child["scope_count"] != 2 {
			t.Fatalf("child scope_count = %#v, want 2", child["scope_count"])
		}
		if child["task_count"] != len(child["tasks"].([]string)) {
			t.Fatalf("child task metadata = %#v, want matching task count and labels", child)
		}
	}
}

func TestWorkflowManagedCalibrationCacheDecaysAfterEarlyRuns(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled":            true,
				"sample_size":        2,
				"cache_max_interval": 8,
			},
		},
		Scope: []any{
			map[string]any{"id": "a", "path": "pkg/a.go"},
			map[string]any{"id": "b", "path": "pkg/b.go"},
			map[string]any{"id": "c", "path": "pkg/c.go"},
		},
		Output: contract,
	}
	agent := &AgentInstance{ID: "reviewer", Model: "mock-model"}
	baselineCalls := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") &&
			strings.Contains(message, "grouped baseline") {
			baselineCalls++
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}

	statuses := make([]string, 0, 4)
	decisions := make([]string, 0, 4)
	for range 4 {
		outputs, err := (&workflowAgentRunner{}).runManagedSplit(
			req,
			agent,
			"reviewer",
			"workflow:test",
			"none",
			"none",
			"",
			"scope_split",
			runOnce,
		)
		if err != nil {
			t.Fatalf("runManagedSplit() error = %v", err)
		}
		calibration := outputs["managed"].(map[string]any)["calibration"].(map[string]any)
		statuses = append(statuses, fmt.Sprint(calibration["status"]))
		cache := calibration["cache"].(map[string]any)
		decisions = append(decisions, fmt.Sprint(cache["decision"]))
	}
	if baselineCalls != 3 {
		t.Fatalf("baseline calls = %d, want 3 with marginal split fit forcing run 3 calibration", baselineCalls)
	}
	if fmt.Sprint(statuses) != "[passed passed passed trusted_cache]" {
		t.Fatalf("calibration statuses = %#v, want passed passed passed trusted_cache", statuses)
	}
	if fmt.Sprint(decisions) != "[miss due due hit]" {
		t.Fatalf("cache decisions = %#v, want miss due due hit", decisions)
	}
}

func TestWorkflowManagedCalibrationCacheSoftReusesModelAndLanguageChanges(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	newReq := func(ext string) workflows.AgentRequest {
		return workflows.AgentRequest{
			Prompt: "Analyze assigned scope.",
			Managed: map[string]any{
				"mode":                  "auto",
				"max_items_per_chunk":   1,
				"max_parallel_children": 1,
				"calibration": map[string]any{
					"enabled":     true,
					"sample_size": 2,
				},
			},
			Scope: []any{
				map[string]any{"id": "a", "path": "pkg/a." + ext},
				map[string]any{"id": "b", "path": "pkg/b." + ext},
				map[string]any{"id": "c", "path": "pkg/c." + ext},
			},
			Output: contract,
		}
	}
	agent := &AgentInstance{ID: "reviewer", Model: "model-a"}
	baselineCalls := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") &&
			strings.Contains(message, "grouped baseline") {
			baselineCalls++
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}
	run := func(req workflows.AgentRequest) map[string]any {
		t.Helper()
		outputs, err := (&workflowAgentRunner{}).runManagedSplit(
			req,
			agent,
			"reviewer",
			"workflow:test",
			"none",
			"none",
			"",
			"scope_split",
			runOnce,
		)
		if err != nil {
			t.Fatalf("runManagedSplit() error = %v", err)
		}
		return outputs["managed"].(map[string]any)["calibration"].(map[string]any)
	}

	run(newReq("go"))
	run(newReq("go"))
	agent.Model = "model-b"
	modelChanged := run(newReq("go"))
	if modelChanged["status"] != "trusted_cache" {
		t.Fatalf("model-changed calibration = %#v, want borrowed trusted_cache", modelChanged)
	}
	modelCache := modelChanged["cache"].(map[string]any)
	if modelCache["decision"] != "similar_hit" || modelCache["provisional"] != true {
		t.Fatalf("model-changed cache = %#v, want provisional similar_hit", modelCache)
	}
	modelVerified := run(newReq("go"))
	if modelVerified["status"] != "passed" {
		t.Fatalf("model verification calibration = %#v, want passed", modelVerified)
	}
	modelVerifiedCache := modelVerified["cache"].(map[string]any)
	if modelVerifiedCache["decision"] != "borrowed_due" || modelVerifiedCache["provisional"] != false {
		t.Fatalf("model verification cache = %#v, want promoted borrowed_due", modelVerifiedCache)
	}
	agent.Model = "model-a"
	languageChanged := run(newReq("py"))
	if languageChanged["status"] != "trusted_cache" {
		t.Fatalf("language-changed calibration = %#v, want borrowed trusted_cache", languageChanged)
	}
	languageCache := languageChanged["cache"].(map[string]any)
	if languageCache["decision"] != "similar_hit" || languageCache["provisional"] != true {
		t.Fatalf("language-changed cache = %#v, want provisional similar_hit", languageCache)
	}
	if fmt.Sprint(languageCache["languages"]) != "[python]" {
		t.Fatalf("language cache metadata = %#v, want python", languageCache["languages"])
	}
	languageVerified := run(newReq("py"))
	if languageVerified["status"] != "passed" {
		t.Fatalf("language verification calibration = %#v, want passed", languageVerified)
	}
	languageVerifiedCache := languageVerified["cache"].(map[string]any)
	if languageVerifiedCache["decision"] != "borrowed_due" || languageVerifiedCache["provisional"] != false {
		t.Fatalf("language verification cache = %#v, want promoted borrowed_due", languageVerifiedCache)
	}
	if baselineCalls != 4 {
		t.Fatalf("baseline calls = %d, want 4", baselineCalls)
	}
}

func TestWorkflowManagedCalibrationCacheBorrowFailureResetsFresh(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	newReq := func(prompt string) workflows.AgentRequest {
		return workflows.AgentRequest{
			Prompt: prompt,
			Managed: map[string]any{
				"mode":                  "auto",
				"max_items_per_chunk":   1,
				"max_parallel_children": 1,
				"calibration": map[string]any{
					"enabled":              true,
					"sample_size":          2,
					"similarity_threshold": 0.70,
				},
			},
			Scope: []any{
				map[string]any{"id": "a", "path": "pkg/a.go"},
				map[string]any{"id": "b", "path": "pkg/b.go"},
				map[string]any{"id": "c", "path": "pkg/c.go"},
			},
			Output: contract,
		}
	}
	agent := &AgentInstance{ID: "reviewer", Model: "mock-model"}
	baselineCalls := 0
	forceMismatch := false
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") &&
			strings.Contains(message, "grouped baseline") {
			baselineCalls++
			if forceMismatch {
				return `{"summary":"baseline","findings":[{"scope_id":"baseline-only"}]}`, nil
			}
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}
	run := func(req workflows.AgentRequest) map[string]any {
		t.Helper()
		outputs, err := (&workflowAgentRunner{}).runManagedSplit(
			req,
			agent,
			"reviewer",
			"workflow:test",
			"none",
			"none",
			"",
			"scope_split",
			runOnce,
		)
		if err != nil {
			t.Fatalf("runManagedSplit() error = %v", err)
		}
		return outputs["managed"].(map[string]any)["calibration"].(map[string]any)
	}

	run(newReq("Analyze assigned scope."))
	run(newReq("Analyze assigned scope."))
	borrowed := run(newReq("Analyze assigned scope carefully."))
	borrowedCache := borrowed["cache"].(map[string]any)
	if borrowed["status"] != "trusted_cache" || borrowedCache["decision"] != "similar_hit" {
		t.Fatalf("borrowed calibration = %#v, want similar trusted cache", borrowed)
	}

	forceMismatch = true
	failed := run(newReq("Analyze assigned scope carefully."))
	failedCache := failed["cache"].(map[string]any)
	if failed["status"] != "failed" || failedCache["decision"] != "borrowed_due" {
		t.Fatalf("failed borrowed verification = %#v, want failed borrowed_due", failed)
	}
	if failedCache["trusted"] != false || failedCache["success_streak"] != 0 {
		t.Fatalf("failed borrowed cache = %#v, want reset confidence", failedCache)
	}

	forceMismatch = false
	fresh := run(newReq("Analyze assigned scope carefully."))
	freshCache := fresh["cache"].(map[string]any)
	if fresh["status"] != "passed" || freshCache["decision"] != "previous_not_trusted" {
		t.Fatalf("fresh calibration = %#v, want fresh previous_not_trusted pass", fresh)
	}
	if freshCache["success_streak"] != 1 {
		t.Fatalf("fresh cache = %#v, want success streak 1", freshCache)
	}
	if baselineCalls != 4 {
		t.Fatalf("baseline calls = %d, want 4", baselineCalls)
	}
}

func TestWorkflowManagedCalibrationCacheIntervalDependsOnSplitFit(t *testing.T) {
	if got := workflowManagedCalibrationCacheInterval(5, 16, 0.95); got != 16 {
		t.Fatalf("high-fit interval = %d, want 16", got)
	}
	if got := workflowManagedCalibrationCacheInterval(5, 16, 0.60); got != 8 {
		t.Fatalf("medium-fit interval = %d, want 8", got)
	}
	if got := workflowManagedCalibrationCacheInterval(5, 16, 0.30); got != 1 {
		t.Fatalf("low-fit interval = %d, want 1", got)
	}
}

func TestWorkflowManagedCalibrationCacheLearnsChildPromptTarget(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_items_per_chunk": 1,
		},
		Scope: []any{
			map[string]any{"id": "a", "path": "pkg/a.go", "content": strings.Repeat("a", 128)},
			map[string]any{"id": "b", "path": "pkg/b.go", "content": strings.Repeat("b", 128)},
		},
		Output: contract,
	}
	agent := &AgentInstance{ID: "reviewer", Model: "mock-model", ContextWindow: 64000, MaxTokens: 8000}
	options := agent.workflowManagedResolveChildPromptTarget(
		req,
		workflowManagedOptions(req.Managed),
		"scope_split",
	)
	plans := workflowManagedChildPlans(req, agent, options, "scope_split")
	key, identity := workflowManagedCalibrationCacheKey(req, agent, options, "scope_split", plans)

	meta := agent.recordWorkflowManagedCalibrationCache(
		key,
		identity,
		options,
		map[string]any{"status": "passed", "match": true},
	)
	learned := intFromAny(meta["learned_target_child_prompt_tokens"])
	if learned <= 0 {
		t.Fatalf("learned target = %#v, want positive", meta["learned_target_child_prompt_tokens"])
	}

	next := agent.workflowManagedResolveChildPromptTarget(
		req,
		workflowManagedOptions(req.Managed),
		"scope_split",
	)
	if next.targetChildPromptSource != "learned_cache" {
		t.Fatalf("target source = %q, want learned_cache", next.targetChildPromptSource)
	}
	if next.targetChildPromptTokens != learned {
		t.Fatalf("targetChildPromptTokens = %d, want learned %d", next.targetChildPromptTokens, learned)
	}
}

func TestWorkflowManagedOptimizationSelectsCheaperModelAndEffort(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                    "auto",
			"max_items_per_chunk":     1,
			"estimated_output_tokens": 500,
			"calibration": map[string]any{
				"enabled": false,
			},
			"optimization": map[string]any{
				"model": map[string]any{
					"enabled": true,
					"candidates": []any{
						map[string]any{
							"name":                "expensive-model",
							"input_price_per_1m":  5.0,
							"output_price_per_1m": 15.0,
						},
						map[string]any{
							"name":                "cheap-model",
							"input_price_per_1m":  0.1,
							"output_price_per_1m": 0.4,
						},
					},
				},
				"effort": map[string]any{"enabled": true},
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		},
		Output: contract,
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				AccountRef: "expensive-model",
				ModelName:  "expensive-model",
			},
		},
		ModelAliases: []config.ModelAliasConfig{
			{Name: "expensive-model", Model: "expensive-model"},
			{Name: "cheap-model", Model: "cheap-model"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName:          "expensive-model",
				Provider:           "openai",
				Model:              "openai/expensive-model",
				APIBase:            "http://example.invalid/v1",
				APIKeys:            config.SimpleSecureStrings("test-key"),
				Enabled:            true,
				InputPricePerMTok:  5.0,
				OutputPricePerMTok: 15.0,
			},
			{
				ModelName:          "cheap-model",
				Provider:           "openai",
				Model:              "openai/cheap-model",
				InputPricePerMTok:  0.1,
				OutputPricePerMTok: 0.4,
			},
		},
	}
	agent := &AgentInstance{
		ID:                 "reviewer",
		AccountRef:         "expensive-model",
		Model:              "expensive-model",
		Workspace:          t.TempDir(),
		CandidateProviders: map[string]providers.LLMProvider{},
	}
	cheapProtocol, cheapModel := providers.ExtractProtocol(cfg.ModelList[1])
	agent.CandidateProviders[providers.ModelKey(cheapProtocol, cheapModel)] = workflowManagedTestProvider{
		model: "openai/cheap-model",
	}
	var seenMu sync.Mutex
	var seen []workflowAgentRunOptions
	runOnce := func(message string, _ bool, options workflowAgentRunOptions) (string, error) {
		seenMu.Lock()
		seen = append(seen, options)
		seenMu.Unlock()
		ids := workflowTestScopeIDs(t, message)
		findings := make([]string, 0, len(ids))
		for _, id := range ids {
			findings = append(findings, fmt.Sprintf(`{"scope_id":%q}`, id))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{loop: &AgentLoop{cfg: cfg}}).runManagedSplit(
		req,
		agent,
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("run count = %d, want 3", len(seen))
	}
	for _, options := range seen {
		if options.ModelName != "cheap-model" {
			t.Fatalf("ModelName = %q, want cheap-model", options.ModelName)
		}
		if options.ReasoningEffort == "" {
			t.Fatalf("ReasoningEffort is empty, want optimized effort")
		}
	}
	managed := outputs["managed"].(map[string]any)
	optimization := managed["optimization"].(map[string]any)
	model := optimization["model"].(map[string]any)
	if model["changed"] != true {
		t.Fatalf("model optimization = %#v, want changed", model)
	}
	cost := optimization["cost"].(map[string]any)
	savings, _ := cost["estimated_savings_usd"].(float64)
	if savings <= 0 {
		t.Fatalf("estimated savings = %#v, want positive", cost["estimated_savings_usd"])
	}
}

func TestWorkflowManagedOptimizationSkipsCheaperModelWithoutProvider(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Provider: "openai"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName:          "default-model",
				Provider:           "openai",
				Model:              "openai/default-model",
				InputPricePerMTok:  5.0,
				OutputPricePerMTok: 15.0,
			},
			{
				ModelName:          "cheap-model",
				Provider:           "openai",
				Model:              "openai/cheap-model",
				InputPricePerMTok:  0.1,
				OutputPricePerMTok: 0.4,
			},
		},
	}
	agent := &AgentInstance{
		ID:                 "reviewer",
		Model:              "default-model",
		Workspace:          t.TempDir(),
		CandidateProviders: map[string]providers.LLMProvider{},
	}
	options := workflowManagedOptions(map[string]any{
		"optimization": map[string]any{
			"model": map[string]any{
				"enabled":    true,
				"candidates": []any{"cheap-model"},
			},
		},
	})

	choice := workflowManagedRunChoice(
		workflows.AgentRequest{Prompt: "Analyze."},
		agent,
		cfg,
		options,
		"scope_split",
		workflowManagedChildPlan{index: 1, scope: []any{map[string]any{"id": "a"}}, tasks: []string{"review"}},
	)
	if choice.modelName != "default-model" {
		t.Fatalf("modelName = %q, want default-model", choice.modelName)
	}
	if changed, _ := choice.modelMeta["changed"].(bool); changed {
		t.Fatalf("model metadata = %#v, want unchanged without candidate provider", choice.modelMeta)
	}
}

func TestWorkflowManagedCalibrationMismatchFallsBackToSingleRun(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled":     true,
				"sample_size": 2,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		},
		Output: contract,
		Tools:  workflows.AgentToolsNone,
	}
	calls := 0
	var runOptions []workflowAgentRunOptions
	runOnce := func(message string, _ bool, options workflowAgentRunOptions) (string, error) {
		calls++
		runOptions = append(runOptions, options)
		if strings.Contains(message, "Agent execution optimization split calibration.") {
			if strings.Contains(message, "grouped baseline") {
				return `{"summary":"baseline","findings":[{"scope_id":"baseline"}]}`, nil
			}
			ids := workflowTestScopeIDs(t, message)
			return workflowManagedTestFindingsJSON(ids), nil
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if calls != 4 {
		t.Fatalf("run count = %d, want 4", calls)
	}
	for i, options := range runOptions {
		if !options.NoTools {
			t.Fatalf("managed call %d options = %#v, want NoTools", i, options)
		}
	}
	if _, exists := outputs["managed_children"]; exists {
		t.Fatalf("managed_children present after calibration fallback: %#v", outputs["managed_children"])
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 3 {
		t.Fatalf("fallback findings = %#v, want full-scope result", findings)
	}
	managed := outputs["managed"].(map[string]any)
	calibration := managed["calibration"].(map[string]any)
	if calibration["status"] != "failed" || calibration["phase"] != "compare" || calibration["match"] != false {
		t.Fatalf("calibration = %#v, want failed compare mismatch", calibration)
	}
	if outputs["tools"] != workflows.AgentToolsNone {
		t.Fatalf("fallback tools audit = %#v, want none", outputs["tools"])
	}
}

func TestWorkflowManagedCalibrationSampleExpandsAndCatchesMismatch(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   3,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled":     true,
				"sample_size": 1,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
			map[string]any{"id": "d"},
		},
		Output: contract,
	}
	baselineCalls := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") {
			if strings.Contains(message, "grouped baseline") {
				baselineCalls++
				return `{"summary":"baseline","findings":[{"scope_id":"baseline-only"}]}`, nil
			}
			ids := workflowTestScopeIDs(t, message)
			return workflowManagedTestFindingsJSON(ids), nil
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if baselineCalls != 1 {
		t.Fatalf("baseline calls = %d, want 1", baselineCalls)
	}
	if _, exists := outputs["managed_children"]; exists {
		t.Fatalf("managed_children present after calibration fallback: %#v", outputs["managed_children"])
	}
	calibration := outputs["managed"].(map[string]any)["calibration"].(map[string]any)
	if calibration["status"] != "failed" || calibration["phase"] != "compare" {
		t.Fatalf("calibration = %#v, want failed compare mismatch", calibration)
	}
	if calibration["sample_scope"] != 4 {
		t.Fatalf("sample_scope = %#v, want expanded sample of 4", calibration["sample_scope"])
	}
}

func TestWorkflowManagedChildInvalidOutputReturnsDiagnostics(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration":           map[string]any{"enabled": false},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
		Output: contract,
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Your previous response did not satisfy") {
			return `{"summary":"still missing findings"}`, nil
		}
		ids := workflowTestScopeIDs(t, message)
		if len(ids) == 1 && ids[0] == "b" {
			return `{"summary":"missing findings"}`, nil
		}
		return workflowManagedTestFindingsJSON(ids), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err == nil {
		t.Fatal("runManagedSplit() error = nil, want child validation error")
	}
	children := outputs["managed_children"].([]map[string]any)
	if len(children) != 2 {
		t.Fatalf("managed_children len = %d, want 2", len(children))
	}
	invalid := 0
	for _, child := range children {
		if child["valid"] == false {
			invalid++
			if child["error"] == "" || child["run_error"] == "" {
				t.Fatalf("invalid child diagnostics = %#v, want error and run_error", child)
			}
		}
	}
	if invalid != 1 {
		t.Fatalf("invalid child count = %d, want 1; children=%#v", invalid, children)
	}
}

func TestWorkflowManagedChildRepairsInvalidJSONAndAggregatesRepairs(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration":           map[string]any{"enabled": false},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
		Output: contract,
	}
	repairs := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Your previous response did not satisfy") {
			repairs++
			id := fmt.Sprintf("repaired-%d", repairs)
			return workflowManagedTestFindingsJSON([]string{id}), nil
		}
		return "not json", nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if outputs["structured_repairs"] != 2 {
		t.Fatalf("structured_repairs = %#v, want 2", outputs["structured_repairs"])
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 2 {
		t.Fatalf("findings = %#v, want two repaired findings", findings)
	}
}

func TestWorkflowManagedCombinedOutputIsValidated(t *testing.T) {
	contract := &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"scope_id": map[string]any{"type": "string"},
				},
			},
		},
	}
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_items_per_chunk": 1,
			"calibration":         map[string]any{"enabled": false},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
		Output: contract,
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		ids := workflowTestScopeIDs(t, message)
		return fmt.Sprintf(`[{"scope_id":%q}]`, ids[0]), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err == nil {
		t.Fatal("runManagedSplit() error = nil, want combined schema validation error")
	}
	if outputs["structured_valid"] != false {
		t.Fatalf("structured_valid = %#v, want false", outputs["structured_valid"])
	}
	if outputs["structured_error"] == "" {
		t.Fatalf("structured_error is empty in outputs %#v", outputs)
	}
}

func TestWorkflowManagedProviderInitializationRegistersCandidate(t *testing.T) {
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{
			{Name: "default-model", Model: "default-model"},
			{Name: "cheap-model", Model: "cheap-model"},
			{Name: "subscription-model", Model: "subscription-model"},
			{Name: "metered-model", Model: "metered-model"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "account",
				Provider:  "openai",
				APIBase:   "http://example.invalid/v1",
				APIKeys:   config.SimpleSecureStrings("test-key"),
				Enabled:   true,
			},
			{
				ModelName: "default-model",
				Provider:  "openai",
				Model:     "openai/default-model",
			},
			{
				ModelName: "cheap-model",
				Provider:  "openai",
				Model:     "openai/cheap-model",
			},
		},
	}
	agent := &AgentInstance{
		ID:                 "reviewer",
		AccountRef:         "account",
		Model:              "default-model",
		Workspace:          t.TempDir(),
		CandidateProviders: map[string]providers.LLMProvider{},
	}
	runner := &workflowAgentRunner{loop: &AgentLoop{
		cfg: cfg,
	}}
	raw := map[string]any{
		"optimization": map[string]any{
			"model": map[string]any{
				"enabled":    true,
				"candidates": []any{"cheap-model"},
			},
		},
	}

	if err := runner.ensureWorkflowManagedProviders(agent, "", raw); err != nil {
		t.Fatalf("ensureWorkflowManagedProviders() error = %v", err)
	}
	protocol, modelID := "openai", "cheap-model"
	if agent.CandidateProviders[providers.ModelKey(protocol, modelID)] == nil {
		t.Fatalf("candidate provider for %s/%s not registered: %#v", protocol, modelID, agent.CandidateProviders)
	}
	if err := runner.ensureWorkflowManagedProviders(agent, "", raw); err != nil {
		t.Fatalf("second ensureWorkflowManagedProviders() error = %v", err)
	}
}

func TestWorkflowManagedProviderInitializationReportsCandidateFailures(t *testing.T) {
	runner := &workflowAgentRunner{loop: &AgentLoop{cfg: &config.Config{}}}
	err := runner.ensureWorkflowManagedProviders(&AgentInstance{Model: "default-model"}, "", map[string]any{
		"optimization": map[string]any{
			"model": map[string]any{
				"enabled":    true,
				"candidates": []any{"missing-model"},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "missing-model") {
		t.Fatalf("ensureWorkflowManagedProviders() error = %v, want missing-model failure", err)
	}
}

func TestWorkflowManagedMissingAliasFailsBeforeLLMCall(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	provider := &aliasRuntimeCountingProvider{}
	loop := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(loop.Close)

	_, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflows.AgentRequest{
			AgentID: "main",
			Prompt:  "Review the assigned scope.",
			History: "none",
			Managed: map[string]any{
				"mode":                "auto",
				"max_items_per_chunk": 1,
				"optimization": map[string]any{
					"model": map[string]any{
						"enabled":    true,
						"candidates": []any{"raw-or-missing-model"},
					},
				},
			},
			Scope: []any{
				map[string]any{"id": "a"},
				map[string]any{"id": "b"},
			},
			Output: &workflows.AgentOutputContract{
				Format: "json",
				Schema: map[string]any{"type": "object"},
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "raw-or-missing-model") {
		t.Fatalf("RunAgent() error = %v, want strict missing-alias failure", err)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("provider calls = %d, want failure before LLM I/O", calls)
	}
}

func TestWorkflowManagedModelCandidateMapUsesModelFallback(t *testing.T) {
	candidates := parseWorkflowManagedModelCandidates([]any{
		map[string]any{
			"model":              "cheap-model",
			"input_price_per_1m": "0.25",
			"subscription":       true,
		},
	})
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one candidate", candidates)
	}
	candidate := candidates[0]
	if candidate.name != "cheap-model" {
		t.Fatalf("candidate name = %q, want cheap-model", candidate.name)
	}
	if candidate.equivalentModelName != "" {
		t.Fatalf("equivalent model = %q, want empty", candidate.equivalentModelName)
	}
	if !candidate.priceKnown || candidate.inputPricePerMTok != 0.25 {
		t.Fatalf("candidate pricing = %#v, want known input price", candidate)
	}
	if !candidate.subscription {
		t.Fatal("candidate subscription = false, want true")
	}
}

func TestWorkflowManagedModelProfileUsesSubscriptionEquivalentPricing(t *testing.T) {
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{
			{Name: "subscription-model", Model: "subscription-model"},
			{Name: "metered-model", Model: "metered-model"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "account",
				Provider:  "openai",
				APIBase:   "http://example.invalid/v1",
				APIKeys:   config.SimpleSecureStrings("test-key"),
				Enabled:   true,
			},
			{
				ModelName:                   "subscription-model",
				Provider:                    "openai",
				Model:                       "subscription-model",
				Subscription:                true,
				SubscriptionEquivalentModel: "metered-model",
			},
			{
				ModelName:          "metered-model",
				Provider:           "openai",
				Model:              "metered-model",
				InputPricePerMTok:  2.5,
				OutputPricePerMTok: 10,
			},
		},
	}

	profile := workflowModelCandidateProfile(cfg, "account", "subscription-model")
	if !profile.subscription || !profile.priceKnown {
		t.Fatalf("profile = %#v, want subscription with known equivalent price", profile)
	}
	if profile.inputPricePerMTok != 2.5 || profile.outputPricePerMTok != 10 {
		t.Fatalf(
			"profile prices = (%v, %v), want equivalent model prices",
			profile.inputPricePerMTok,
			profile.outputPricePerMTok,
		)
	}
	if profile.source != "subscription_equivalent_model_config" {
		t.Fatalf("profile source = %q, want subscription_equivalent_model_config", profile.source)
	}
}

func TestWorkflowManagedModelProfileStopsSubscriptionEquivalentCycle(t *testing.T) {
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{
			{Name: "subscription-a", Model: "subscription-a"},
			{Name: "subscription-b", Model: "subscription-b"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "account",
				Provider:  "openai",
				Enabled:   true,
			},
			{
				ModelName:                   "subscription-a-metadata",
				Provider:                    "openai",
				Model:                       "subscription-a",
				Subscription:                true,
				SubscriptionEquivalentModel: "subscription-b",
			},
			{
				ModelName:                   "subscription-b-metadata",
				Provider:                    "openai",
				Model:                       "subscription-b",
				Subscription:                true,
				SubscriptionEquivalentModel: "subscription-a",
			},
		},
	}

	profile := workflowModelCandidateProfile(cfg, "account", "subscription-a")
	if !profile.subscription {
		t.Fatalf("profile = %#v, want subscription metadata", profile)
	}
	if profile.priceKnown {
		t.Fatalf("profile = %#v, want cycle to stop without inherited pricing", profile)
	}
}

func TestWorkflowManagedModelProfileBoundsSubscriptionEquivalentDepth(t *testing.T) {
	const terminalIndex = workflowModelCandidateProfileMaxEquivalentDepth
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{{
			ModelName: "account",
			Provider:  "openai",
			Enabled:   true,
		}},
	}
	for i := 0; i <= terminalIndex; i++ {
		name := fmt.Sprintf("subscription-%d", i)
		cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
			Name:  name,
			Model: name,
		})
		metadata := &config.ModelConfig{
			ModelName:    name + "-metadata",
			Provider:     "openai",
			Model:        name,
			Subscription: true,
		}
		if i < terminalIndex {
			metadata.SubscriptionEquivalentModel = fmt.Sprintf("subscription-%d", i+1)
		} else {
			metadata.InputPricePerMTok = 1
			metadata.OutputPricePerMTok = 2
		}
		cfg.ModelList = append(cfg.ModelList, metadata)
	}

	profile := workflowModelCandidateProfile(cfg, "account", "subscription-0")
	if profile.priceKnown {
		t.Fatalf(
			"profile inherited pricing beyond depth limit: %#v",
			profile,
		)
	}
}

func TestWorkflowManagedModeNormalization(t *testing.T) {
	tests := []struct {
		raw  any
		want string
	}{
		{raw: nil, want: "off"},
		{raw: false, want: "off"},
		{raw: true, want: "auto"},
		{raw: "none", want: "off"},
		{raw: "TRUE", want: "auto"},
		{raw: "custom", want: "custom"},
		{raw: map[string]any{"enabled": false}, want: "off"},
		{raw: map[string]any{"enabled": false, "mode": "task_split"}, want: "off"},
		{raw: map[string]any{"mode": "task_split"}, want: "task_split"},
		{raw: map[string]any{"max_items_per_chunk": 1}, want: "auto"},
	}
	for _, tt := range tests {
		if got := workflowManagedMode(tt.raw); got != tt.want {
			t.Fatalf("workflowManagedMode(%#v) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestWorkflowManagedScopeItemsAndPlanPreserveObjectWrapper(t *testing.T) {
	scope := map[string]any{
		"kind":  "files",
		"limit": 2,
		"items": []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
	}
	items := workflowScopeItems(scope)
	if len(items) != 2 {
		t.Fatalf("workflowScopeItems() = %#v, want two items", items)
	}

	planned := workflowScopeForPlan(scope, []any{map[string]any{"id": "a"}})
	mapped, ok := planned.(map[string]any)
	if !ok {
		t.Fatalf("planned scope = %#v, want map wrapper", planned)
	}
	if mapped["kind"] != "files" || mapped["limit"] != 2 {
		t.Fatalf("planned scope metadata = %#v, want original wrapper metadata", mapped)
	}
	plannedItems, ok := mapped["items"].([]any)
	if !ok || len(plannedItems) != 1 {
		t.Fatalf("planned items = %#v, want one scoped item", mapped["items"])
	}
}

func workflowManagedTestOutputContract() *workflows.AgentOutputContract {
	return &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"summary", "findings"},
			"properties": map[string]any{
				"summary":  map[string]any{"type": "string"},
				"findings": map[string]any{"type": "array"},
			},
		},
	}
}

type workflowManagedTestProvider struct {
	model string
}

type workflowEphemeralHookSpy struct {
	beforeCalls atomic.Int64
	afterCalls  atomic.Int64
}

func (s *workflowEphemeralHookSpy) BeforeLLM(
	_ context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	s.beforeCalls.Add(1)
	return req, HookDecision{Action: HookActionContinue}, nil
}

func (s *workflowEphemeralHookSpy) AfterLLM(
	_ context.Context,
	response *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	s.afterCalls.Add(1)
	return response, HookDecision{Action: HookActionContinue}, nil
}

type workflowToolsCaptureProvider struct {
	mu         sync.Mutex
	toolCounts []int
	responses  []string
}

type workflowReadOnlyProviderCall struct {
	messages           []providers.Message
	toolCount          int
	options            map[string]any
	promptCacheKey     string
	promptCachePresent bool
	reasoningEffort    string
}

type workflowEphemeralFallbackCall struct {
	model              string
	messages           []providers.Message
	promptCacheKey     string
	promptCachePresent bool
	reasoningEffort    string
}

type workflowEphemeralFallbackState struct {
	mu    sync.Mutex
	calls []workflowEphemeralFallbackCall
}

func (s *workflowEphemeralFallbackState) snapshotCalls() []workflowEphemeralFallbackCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]workflowEphemeralFallbackCall(nil), s.calls...)
}

type workflowEphemeralFallbackProvider struct {
	model string
	state *workflowEphemeralFallbackState
}

type workflowEphemeralStatefulProviderState struct {
	mu      sync.Mutex
	nextID  int
	created []int
	called  []int
	closed  []int
}

func (s *workflowEphemeralStatefulProviderState) newProvider() providers.LLMProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.created = append(s.created, s.nextID)
	return &workflowEphemeralStatefulProvider{id: s.nextID, state: s}
}

func (s *workflowEphemeralStatefulProviderState) snapshot() ([]int, []int, []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.created...), append([]int(nil), s.called...), append([]int(nil), s.closed...)
}

type workflowEphemeralStatefulProvider struct {
	id    int
	state *workflowEphemeralStatefulProviderState
}

func (p *workflowEphemeralStatefulProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.state.mu.Lock()
	p.state.called = append(p.state.called, p.id)
	p.state.mu.Unlock()
	if p.id == 1 {
		return &providers.LLMResponse{Content: "not json"}, nil
	}
	return &providers.LLMResponse{Content: `{"ok":true}`}, nil
}

func (p *workflowEphemeralStatefulProvider) Close() {
	p.state.mu.Lock()
	p.state.closed = append(p.state.closed, p.id)
	p.state.mu.Unlock()
}

func (p *workflowEphemeralFallbackProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	cacheKey, cachePresent := opts["prompt_cache_key"].(string)
	reasoningEffort, _ := opts["reasoning_effort"].(string)
	p.state.mu.Lock()
	p.state.calls = append(p.state.calls, workflowEphemeralFallbackCall{
		model:              p.model,
		messages:           session.CloneMessages(messages),
		promptCacheKey:     cacheKey,
		promptCachePresent: cachePresent,
		reasoningEffort:    reasoningEffort,
	})
	p.state.mu.Unlock()
	if p.model == "primary-model" {
		if len(messages) > 0 {
			messages[len(messages)-1].Content = "primary-provider-mutated"
		}
		opts["prompt_cache_key"] = "primary-provider-mutated"
		return nil, context.DeadlineExceeded
	}
	return &providers.LLMResponse{Content: "fallback decision"}, nil
}

type workflowReadOnlyCaptureProvider struct {
	mu                sync.Mutex
	responses         []string
	usages            []providers.UsageInfo
	errors            []error
	respond           func(int, []providers.Message) string
	calls             []workflowReadOnlyProviderCall
	toolCall          bool
	started           chan struct{}
	release           chan struct{}
	called            chan struct{}
	startOnce         sync.Once
	afterCall         func(int)
	mutateNestedInput bool
}

type workflowPrivateAccountProvider struct {
	response string
	err      error
}

func (p *workflowPrivateAccountProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &providers.LLMResponse{Content: p.response}, nil
}

type workflowNoSnapshotReadStore struct {
	session.SessionStore
	snapshotReads atomic.Int64
}

func (s *workflowNoSnapshotReadStore) ReadSessionSnapshot(
	context.Context,
	string,
) (session.SessionSnapshot, bool, error) {
	s.snapshotReads.Add(1)
	return session.SessionSnapshot{}, false, errors.New("live snapshot read is forbidden")
}

type workflowSnapshotReadCountingSessionStore struct {
	session.SessionStore
	reader        session.SnapshotReader
	snapshotReads atomic.Int64
}

func (s *workflowSnapshotReadCountingSessionStore) ReadSessionSnapshot(
	ctx context.Context,
	key string,
) (session.SessionSnapshot, bool, error) {
	s.snapshotReads.Add(1)
	return s.reader.ReadSessionSnapshot(ctx, key)
}

type workflowFixedSnapshotSessionStore struct {
	session.SessionStore
	snapshot session.SessionSnapshot
}

func (s *workflowFixedSnapshotSessionStore) ReadSessionSnapshot(
	context.Context,
	string,
) (session.SessionSnapshot, bool, error) {
	return workflowCloneSessionSnapshot(s.snapshot), true, nil
}

type workflowSnapshotReadSpyMediaStore struct {
	media.MediaStore
	snapshotReads atomic.Int64
}

func (s *workflowSnapshotReadSpyMediaStore) ReadSnapshot(
	context.Context,
	string,
	int64,
) (media.Snapshot, error) {
	s.snapshotReads.Add(1)
	return media.Snapshot{}, errors.New("live media snapshot read is forbidden")
}

func (p *workflowReadOnlyCaptureProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	_ string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	callIndex := len(p.calls)
	cacheKey, cachePresent := opts["prompt_cache_key"].(string)
	reasoningEffort, _ := opts["reasoning_effort"].(string)
	p.calls = append(p.calls, workflowReadOnlyProviderCall{
		messages:           session.CloneMessages(messages),
		toolCount:          len(tools),
		options:            cloneAnyMap(opts),
		promptCacheKey:     cacheKey,
		promptCachePresent: cachePresent,
		reasoningEffort:    reasoningEffort,
	})
	response := "decision"
	if callIndex < len(p.responses) {
		response = p.responses[callIndex]
	}
	var responseErr error
	if callIndex < len(p.errors) {
		responseErr = p.errors[callIndex]
	}
	var responseUsage *providers.UsageInfo
	if callIndex < len(p.usages) {
		usage := p.usages[callIndex]
		responseUsage = &usage
	}
	respond := p.respond
	toolCall := p.toolCall
	started := p.started
	release := p.release
	called := p.called
	afterCall := p.afterCall
	mutateNestedInput := p.mutateNestedInput
	p.mu.Unlock()
	if respond != nil {
		response = respond(callIndex, session.CloneMessages(messages))
	}
	if called != nil {
		select {
		case called <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if mutateNestedInput {
		for messageIndex := range messages {
			for blockIndex := range messages[messageIndex].SystemParts {
				block := &messages[messageIndex].SystemParts[blockIndex]
				if block.CacheControl != nil {
					block.CacheControl.Type = "provider-mutated"
				}
			}
			for callIndex := range messages[messageIndex].ToolCalls {
				nested, _ := messages[messageIndex].ToolCalls[callIndex].Arguments["nested"].(map[string]any)
				if nested != nil {
					nested["marker"] = "provider-mutated"
				}
			}
		}
	}
	if afterCall != nil {
		afterCall(callIndex)
	}

	if started != nil {
		p.startOnce.Do(func() { close(started) })
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if responseErr != nil {
		return nil, responseErr
	}
	result := &providers.LLMResponse{Content: response, Usage: responseUsage}
	if toolCall {
		result.ToolCalls = []providers.ToolCall{{
			ID:   "forbidden",
			Type: "function",
			Function: &providers.FunctionCall{
				Name:      "sentinel",
				Arguments: `{}`,
			},
		}}
	}
	return result, nil
}

func (p *workflowReadOnlyCaptureProvider) snapshotCalls() []workflowReadOnlyProviderCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]workflowReadOnlyProviderCall, len(p.calls))
	for i, call := range p.calls {
		result[i] = call
		result[i].messages = session.CloneMessages(call.messages)
		result[i].options = cloneAnyMap(call.options)
	}
	return result
}

func (p *workflowReadOnlyCaptureProvider) setResponses(responses []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses = append([]string(nil), responses...)
	p.calls = nil
}

func newWorkflowReadOnlyTestLoop(
	t *testing.T,
	provider *workflowReadOnlyCaptureProvider,
) (*AgentLoop, *AgentInstance, string, string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	loop.providerFactory = func(modelConfig *config.ModelConfig) (providers.LLMProvider, string, error) {
		model := "workflow-read-only-test"
		if modelConfig != nil && strings.TrimSpace(modelConfig.Model) != "" {
			model = modelConfig.Model
		}
		return provider, model, nil
	}
	agent := loop.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	scope := &session.SessionScope{
		Version: session.ScopeVersionV1,
		AgentID: "main",
		Channel: "web",
		Account: "default",
		Dimensions: []string{
			"chat",
		},
		Values: map[string]string{"chat": "review"},
	}
	canonicalKey := session.BuildSessionKey(*scope)
	alias := "agent:main:web:direct:review"
	metadata, ok := agent.Sessions.(session.MetadataAwareSessionStore)
	if !ok {
		t.Fatalf("session store %T is not metadata aware", agent.Sessions)
	}
	metadata.EnsureSessionMetadata(canonicalKey, scope, []string{alias})
	agent.Sessions.AddMessage(canonicalKey, "user", "existing problem context")
	agent.Sessions.AddMessage(canonicalKey, "assistant", "existing analysis")
	agent.Sessions.SetSummary(canonicalKey, "existing decision summary")
	return loop, agent, canonicalKey, alias
}

func TestWorkflowAgentSourceSessionCapturesRepairTranscriptAndReplaysExactPrompt(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{
		"not json",
		`{"findings":[]}`,
	}}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	capture := &workflows.AgentSourceCapture{
		ExecutionID: "aix_11111111111111111111111111111111",
		WorkspaceID: "prw_11111111111111111111111111111111",
		Binding:     "sha256:source-binding",
	}
	request := workflowEphemeralTestRequest("Review this change.")
	request.PrivateContext = true
	request.IsolatedSystemPrompt = "Exact isolated review system prompt."
	request.SourceCapture = capture
	request.Output = &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"findings"},
			"properties": map[string]any{
				"findings": map[string]any{"type": "array"},
			},
		},
	}
	runner := &workflowAgentRunner{loop: loop}
	first, firstErr := runner.RunAgent(t.Context(), request)
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	sessionKey, _ := first["source_session"].(string)
	revision, _ := first["source_revision"].(string)
	if sessionKey == "" || revision == "" || first["source_tools"] != workflows.AgentToolsNone {
		t.Fatalf("source outputs = %#v", first)
	}
	second, secondErr := runner.RunAgent(t.Context(), request)
	firstWithoutUsage := cloneAnyMap(first)
	secondWithoutUsage := cloneAnyMap(second)
	delete(firstWithoutUsage, "usage")
	delete(secondWithoutUsage, "usage")
	if secondErr != nil || !reflect.DeepEqual(firstWithoutUsage, secondWithoutUsage) {
		t.Fatalf("source replay = %#v, error = %v", second, secondErr)
	}
	if calls := provider.snapshotCalls(); len(calls) != 2 {
		t.Fatalf("provider calls after duplicate = %d, want initial plus repair only", len(calls))
	}
	changed := request
	changed.Prompt = "Review a different change."
	if _, err := runner.RunAgent(t.Context(), changed); err == nil ||
		!strings.Contains(err.Error(), "different request") {
		t.Fatalf("changed duplicate error = %v, want identity mismatch", err)
	}
	if calls := provider.snapshotCalls(); len(calls) != 2 {
		t.Fatalf("provider calls after changed duplicate = %d, want 2", len(calls))
	}
	frozen, captureErr := runner.CaptureReadOnlySession(t.Context(), workflows.ReadOnlySessionRef{
		AgentID: "main", Session: sessionKey, ExpectedRevision: revision,
	})
	if captureErr != nil || frozen == nil || len(frozen.Snapshot.History) != 4 ||
		frozen.Snapshot.History[0].Content != workflowAgentMessage(request) ||
		frozen.Snapshot.History[1].Content != "not json" ||
		!strings.Contains(frozen.Snapshot.History[2].Content, "previous response did not satisfy") ||
		frozen.Snapshot.History[3].Content != `{"findings":[]}` {
		t.Fatalf("captured source = %#v, error = %v", frozen, captureErr)
	}

	if err := os.WriteFile(
		filepath.Join(agent.Workspace, "AGENTS.md"),
		[]byte("CHANGED-DEFAULT-SYSTEM-CANARY"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	provider.setResponses([]string{"source follow-up"})
	sourceOutputs, sourceErr := runner.RunAgent(t.Context(), workflows.AgentRequest{
		AgentID:               "main",
		Prompt:                "Find any remaining issues.",
		History:               "read_only",
		Cache:                 "none",
		Tools:                 workflows.AgentToolsNone,
		PrivateContext:        true,
		FrozenReadOnlySession: frozen,
	})
	if sourceErr != nil || sourceOutputs["text"] != "source follow-up" {
		t.Fatalf("source follow-up = %#v, error = %v", sourceOutputs, sourceErr)
	}
	calls := provider.snapshotCalls()
	if len(calls) != 1 || calls[0].toolCount != 0 {
		t.Fatalf("source provider calls = %#v, want one no-tool call", calls)
	}
	if len(calls[0].messages) != 6 || calls[0].messages[0].Role != "system" ||
		calls[0].messages[0].Content != request.IsolatedSystemPrompt ||
		!workflowMessagesContain(calls[0].messages, "not json") ||
		!workflowMessagesContain(calls[0].messages, "previous response did not satisfy") ||
		workflowMessagesContain(calls[0].messages, "CHANGED-DEFAULT-SYSTEM-CANARY") ||
		workflowMessagesContain(calls[0].messages, workflowAgentSourceMetadataPrefix) {
		t.Fatalf("source provider context = %#v", calls[0].messages)
	}
}

func TestWorkflowAgentSourceSessionSerializesConcurrentDuplicateExecution(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{`{"findings":[]}`},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	loop, store, agentID, sessionKey := newWorkflowReadOnlyTestLoop(t, provider)
	_ = store
	_ = agentID
	_ = sessionKey
	runner := &workflowAgentRunner{loop: loop}
	request := workflowEphemeralTestRequest("Review concurrently.")
	request.PrivateContext = true
	request.IsolatedSystemPrompt = "Exact concurrent review system prompt."
	request.SourceCapture = &workflows.AgentSourceCapture{
		ExecutionID: "aix_22222222222222222222222222222222",
		WorkspaceID: "prw_22222222222222222222222222222222",
		Binding:     "sha256:concurrent-source-binding",
	}
	request.Output = &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"findings"},
			"properties": map[string]any{
				"findings": map[string]any{"type": "array"},
			},
		},
	}
	type result struct {
		outputs map[string]any
		err     error
	}
	results := make(chan result, 2)
	go func() {
		outputs, err := runner.RunAgent(t.Context(), request)
		results <- result{outputs: outputs, err: err}
	}()
	<-provider.started
	go func() {
		outputs, err := runner.RunAgent(t.Context(), request)
		results <- result{outputs: outputs, err: err}
	}()
	close(provider.release)
	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent errors = (%v, %v)", first.err, second.err)
	}
	firstWithoutUsage := cloneAnyMap(first.outputs)
	secondWithoutUsage := cloneAnyMap(second.outputs)
	delete(firstWithoutUsage, "usage")
	delete(secondWithoutUsage, "usage")
	if !reflect.DeepEqual(firstWithoutUsage, secondWithoutUsage) {
		t.Fatalf("concurrent outputs differ: %#v and %#v", first.outputs, second.outputs)
	}
	if calls := provider.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("concurrent provider calls = %d, want 1", len(calls))
	}
}

func workflowEphemeralTestRequest(prompt string) workflows.AgentRequest {
	return workflows.AgentRequest{
		AgentID:          "main",
		Prompt:           prompt,
		EphemeralSession: true,
		History:          "none",
		Cache:            "none",
		Tools:            workflows.AgentToolsNone,
	}
}

func TestWorkflowAgentExplicitModelAliasIsValidatedAndReported(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"evaluated"}}
	loop, _, _, _ := newWorkflowReadOnlyTestLoop(t, provider) //nolint:dogsled // Only the runtime loop is relevant.
	runner := &workflowAgentRunner{loop: loop}
	request := workflowEphemeralTestRequest("Evaluate this immutable corpus chunk.")
	request.Model = "test-model"

	outputs, err := runner.RunAgent(t.Context(), request)
	if err != nil || outputs["model"] != "test-model" || outputs["text"] != "evaluated" {
		t.Fatalf("explicit model outputs = %#v, error = %v", outputs, err)
	}
	if calls := provider.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}

	for _, model := range []string{" missing-model ", "missing-model"} {
		invalid := request
		invalid.Model = model
		if _, runErr := runner.RunAgent(t.Context(), invalid); runErr == nil ||
			!strings.Contains(runErr.Error(), "model alias") {
			t.Fatalf("RunAgent(model=%q) error = %v", model, runErr)
		}
	}
	if calls := provider.snapshotCalls(); len(calls) != 1 {
		t.Fatalf("provider calls after invalid aliases = %d, want 1", len(calls))
	}
}

func workflowDirectoryFileSnapshot(t *testing.T, directory string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read directory %q: %v", directory, err)
	}
	files := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			files[entry.Name()+"/"] = "<directory>"
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			t.Fatalf("read file %q: %v", entry.Name(), readErr)
		}
		files[entry.Name()] = string(data)
	}
	return files
}

func workflowAssertSessionStoreUnchanged(
	t *testing.T,
	agent *AgentInstance,
	sessionsDir string,
	wantCatalog []string,
	wantFiles map[string]string,
) {
	t.Helper()
	if got := agent.Sessions.ListSessions(); !reflect.DeepEqual(got, wantCatalog) {
		t.Fatalf("session catalog changed: before=%#v after=%#v", wantCatalog, got)
	}
	if got := workflowDirectoryFileSnapshot(t, sessionsDir); !reflect.DeepEqual(got, wantFiles) {
		t.Fatalf("session files changed:\nbefore=%#v\nafter=%#v", wantFiles, got)
	}
}

func workflowAssertNoEphemeralActiveTurn(t *testing.T, loop *AgentLoop) {
	t.Helper()
	var leaked []string
	loop.activeTurnStates.Range(func(key, _ any) bool {
		text, _ := key.(string)
		if strings.HasPrefix(text, "workflow:ephemeral:") {
			leaked = append(leaked, text)
		}
		return true
	})
	if len(leaked) != 0 {
		t.Fatalf("ephemeral active turn residue = %#v", leaked)
	}
}

func workflowStringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func workflowAccountRouterSessionState(
	t *testing.T,
	statePath string,
	routerName string,
) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read account router state: %v", err)
	}
	var state struct {
		Routers map[string]struct {
			Sessions map[string]json.RawMessage `json:"sessions"`
		} `json:"routers"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode account router state: %v", err)
	}
	router, ok := state.Routers[routerName]
	if !ok {
		t.Fatalf("account router state has no %q entry: %s", routerName, data)
	}
	return router.Sessions
}

func workflowRawMessageKeySet(values map[string]json.RawMessage) map[string]struct{} {
	keys := make(map[string]struct{}, len(values))
	for key := range values {
		keys[key] = struct{}{}
	}
	return keys
}

func workflowMessagesContain(messages []providers.Message, substring string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, substring) {
			return true
		}
		for _, block := range message.SystemParts {
			if strings.Contains(block.Text, substring) {
				return true
			}
		}
	}
	return false
}

func workflowMessagesHavePromptCacheControl(messages []providers.Message) bool {
	for _, message := range messages {
		for _, block := range message.SystemParts {
			if block.CacheControl != nil {
				return true
			}
		}
	}
	return false
}

func workflowLatestUserMessage(messages []providers.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == "user" {
			return messages[index].Content
		}
	}
	if len(messages) == 0 {
		return ""
	}
	return messages[len(messages)-1].Content
}

func workflowScopeIDsInMessage(message string, candidates ...string) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		spaced := fmt.Sprintf(`"id": %q`, candidate)
		compact := fmt.Sprintf(`"id":%q`, candidate)
		if strings.Contains(message, spaced) || strings.Contains(message, compact) {
			ids = append(ids, candidate)
		}
	}
	return ids
}

func workflowAssertEphemeralProviderCallsIsolated(
	t *testing.T,
	calls []workflowReadOnlyProviderCall,
) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatal("ephemeral managed execution made no provider calls")
	}
	for index, call := range calls {
		if call.toolCount != 0 {
			t.Fatalf("provider call %d tool definitions = %d, want 0", index, call.toolCount)
		}
		if call.promptCachePresent || call.promptCacheKey != "" {
			t.Fatalf(
				"provider call %d prompt cache = (%v, %q), want absent",
				index,
				call.promptCachePresent,
				call.promptCacheKey,
			)
		}
		if workflowMessagesHavePromptCacheControl(call.messages) {
			t.Fatalf("provider call %d retained CacheControl: %#v", index, call.messages)
		}
		if workflowMessagesContain(call.messages, "existing problem context") ||
			workflowMessagesContain(call.messages, "existing decision summary") {
			t.Fatalf("provider call %d loaded durable history: %#v", index, call.messages)
		}
	}
}

func workflowAssertEphemeralManagedOutputEnvelope(t *testing.T, outputs map[string]any) {
	t.Helper()
	if outputs["session"] != workflows.AgentSessionEphemeral ||
		outputs["session_mode"] != workflows.AgentSessionEphemeral {
		t.Fatalf("managed session audit = %#v, want opaque ephemeral mode", outputs)
	}
	if outputs["history"] != "none" || outputs["cache"] != "none" ||
		outputs["cache_key"] != "" || outputs["tools"] != workflows.AgentToolsNone {
		t.Fatalf("managed isolation audit = %#v, want history/cache/tools disabled", outputs)
	}
	if _, exists := outputs["history_revision"]; exists {
		t.Fatalf("ephemeral managed output exposed history_revision: %#v", outputs)
	}
}

func workflowAssertNoInternalEphemeralIdentity(t *testing.T, values ...any) {
	t.Helper()
	for index, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("serialize ephemeral audit value %d: %v", index, err)
		}
		if strings.Contains(string(encoded), "workflow:ephemeral:") {
			t.Fatalf("ephemeral audit value %d leaked internal identity: %s", index, encoded)
		}
	}
}

func workflowFrozenCacheControl(messages []providers.Message) string {
	for _, message := range messages {
		for _, block := range message.SystemParts {
			if block.Text == "frozen nested block" && block.CacheControl != nil {
				return block.CacheControl.Type
			}
		}
	}
	return ""
}

func (p *workflowToolsCaptureProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	tools []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	call := len(p.toolCounts)
	p.toolCounts = append(p.toolCounts, len(tools))
	response := "classified"
	if call < len(p.responses) {
		response = p.responses[call]
	}
	p.mu.Unlock()
	return &providers.LLMResponse{Content: response}, nil
}

func (p *workflowToolsCaptureProvider) ToolCounts() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.toolCounts...)
}

func (p workflowManagedTestProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "{}"}, nil
}

func workflowTestScopeIDs(t *testing.T, message string) []string {
	t.Helper()
	_, parsed, err := workflows.ExtractJSONValue(message)
	if err != nil {
		t.Fatalf("extract assigned scope from message: %v\n%s", err, message)
	}
	items, ok := parsed.([]any)
	if !ok {
		t.Fatalf("assigned scope = %#v, want array", parsed)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		mapped, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("scope item = %#v, want object", item)
		}
		ids = append(ids, fmt.Sprint(mapped["id"]))
	}
	return ids
}

func workflowManagedTestFindingsJSON(ids []string) string {
	findings := make([]string, 0, len(ids))
	for _, id := range ids {
		findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"title":"finding %s"}`, id, id))
	}
	return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ","))
}

func workflowTestAssignedTasks(t *testing.T, message string) []string {
	t.Helper()
	tasks := workflowAssignedTasks(workflows.AgentRequest{Context: message})
	if len(tasks) == 0 {
		t.Fatalf("assigned tasks not found in message:\n%s", message)
	}
	return tasks
}

func TestWorkflowToolRunnerDeliversHandledMedia(t *testing.T) {
	store := media.NewFileMediaStore()
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("workflow report"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(path, media.MediaMeta{
		Filename:    "report.txt",
		ContentType: "text/plain",
		Source:      "test:workflow",
	}, "test:workflow")
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewToolRegistry()
	registry.Register(&workflowHandledMediaTool{ref: ref})
	manager := &workflowMediaChannelManager{}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	loop := &AgentLoop{
		bus:            msgBus,
		channelManager: manager,
		mediaStore:     store,
	}

	outputs, err := (&workflowToolRunner{
		agentID:  "main",
		registry: registry,
		loop:     loop,
	}).RunTool(context.Background(), workflows.ToolRequest{
		Name:    "workflow_handled_media",
		Session: "workflow:session",
		Delivery: workflows.Delivery{
			Channel:          "telegram",
			ChatID:           "chat1",
			TopicID:          "42",
			MessageID:        "m1",
			ReplyToMessageID: "m1",
		},
	})
	if err != nil {
		t.Fatalf("RunTool failed: %v", err)
	}
	if outputs["response_handled"] != true {
		t.Fatalf("outputs = %#v, want response_handled=true", outputs)
	}
	if len(manager.sentMedia) != 1 {
		t.Fatalf("sent media = %d, want 1", len(manager.sentMedia))
	}
	got := manager.sentMedia[0]
	if got.Channel != "telegram" || got.ChatID != "chat1" {
		t.Fatalf("target = %#v", got)
	}
	if got.Context.TopicID != "42" || got.Context.ReplyToMessageID != "m1" {
		t.Fatalf("context = %#v", got.Context)
	}
	if len(got.Parts) != 1 || got.Parts[0].Ref != ref || got.Parts[0].Type != "file" {
		t.Fatalf("parts = %#v", got.Parts)
	}
}

type workflowMCPCall struct {
	server string
	tool   string
}

type workflowMCPRecordingManager struct {
	calls []workflowMCPCall
}

func (m *workflowMCPRecordingManager) CallTool(
	_ context.Context,
	serverName, toolName string,
	_ map[string]any,
) (*sdkmcp.CallToolResult, error) {
	m.calls = append(m.calls, workflowMCPCall{server: serverName, tool: toolName})
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}},
	}, nil
}

func TestWorkflowToolRunnerRequiresExactMCPIdentity(t *testing.T) {
	t.Run("boundary-canonical alternate is not executed", func(t *testing.T) {
		manager := &workflowMCPRecordingManager{}
		registry := tools.NewToolRegistry()
		registry.Register(tools.NewMCPTool(
			manager,
			"a",
			&sdkmcp.Tool{Name: "b_c"},
		))
		runner := &workflowToolRunner{agentID: "main", registry: registry}

		_, err := runner.RunTool(context.Background(), workflows.ToolRequest{
			Name:      picomcp.CanonicalToolName("a_b", "c"),
			MCP:       true,
			MCPServer: "a_b",
			MCPTool:   "c",
		})
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("RunTool() error = %v, want exact MCP identity mismatch", err)
		}
		if len(manager.calls) != 0 {
			t.Fatalf("MCP calls = %#v, want no execution", manager.calls)
		}
	})

	t.Run("exact identity executes", func(t *testing.T) {
		manager := &workflowMCPRecordingManager{}
		registry := tools.NewToolRegistry()
		registry.Register(tools.NewMCPTool(
			manager,
			"a_b",
			&sdkmcp.Tool{Name: "c"},
		))
		runner := &workflowToolRunner{agentID: "main", registry: registry}

		if _, err := runner.RunTool(context.Background(), workflows.ToolRequest{
			Name:      picomcp.CanonicalToolName("a_b", "c"),
			MCP:       true,
			MCPServer: "a_b",
			MCPTool:   "c",
		}); err != nil {
			t.Fatalf("RunTool() error = %v", err)
		}
		if len(manager.calls) != 1 ||
			manager.calls[0] != (workflowMCPCall{server: "a_b", tool: "c"}) {
			t.Fatalf("MCP calls = %#v, want exact a_b/c execution", manager.calls)
		}
	})
}

func TestWorkflowToolRunnerPreservesPreDispatchValidationError(t *testing.T) {
	manager := &workflowMCPRecordingManager{}
	registry := tools.NewToolRegistry()
	registry.Register(tools.NewMCPTool(
		manager,
		"github",
		&sdkmcp.Tool{
			Name: "issue_write",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"labels": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
			},
		},
	))
	runner := &workflowToolRunner{agentID: "main", registry: registry}

	_, err := runner.RunTool(context.Background(), workflows.ToolRequest{
		Name:      picomcp.CanonicalToolName("github", "issue_write"),
		Args:      map[string]any{"labels": []string{"picoclaw"}},
		MCP:       true,
		MCPServer: "github",
		MCPTool:   "issue_write",
	})
	if !errors.Is(err, workflows.ErrToolCallNotDispatched) {
		t.Fatalf("RunTool() error = %v, want ErrToolCallNotDispatched", err)
	}
	if len(manager.calls) != 0 {
		t.Fatalf("MCP calls = %#v, want no dispatch", manager.calls)
	}
}

func TestWorkflowToolResultOutputsExposesJSONFields(t *testing.T) {
	outputs := workflowToolResultOutputs(tools.SilentResult(`{
  "workspace": {
    "id": "gw-test",
    "path": "/tmp/repo"
  },
  "next": "inspect path"
}`))

	workspace, ok := outputs["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("workspace output = %#v, want parsed object", outputs["workspace"])
	}
	if workspace["path"] != "/tmp/repo" {
		t.Fatalf("workspace.path = %#v, want /tmp/repo", workspace["path"])
	}
	jsonOutput, ok := outputs["json"].(map[string]any)
	if !ok || jsonOutput["next"] != "inspect path" {
		t.Fatalf("json output = %#v, want parsed tool JSON", outputs["json"])
	}
	if outputs["text"] == "" {
		t.Fatalf("text output should preserve original content: %#v", outputs)
	}
}

type workflowHandledMediaTool struct {
	ref string
}

func (t *workflowHandledMediaTool) Name() string { return "workflow_handled_media" }

func (t *workflowHandledMediaTool) Description() string { return "returns handled media" }

func (t *workflowHandledMediaTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (t *workflowHandledMediaTool) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.MediaResult("Attachment delivered.", []string{t.ref}).WithResponseHandled()
}

type workflowMediaChannelManager struct {
	sentMedia []bus.OutboundMediaMessage
}

func (m *workflowMediaChannelManager) GetChannel(string) (channels.Channel, bool) { return nil, false }

func (m *workflowMediaChannelManager) GetEnabledChannels() []string { return nil }

func (m *workflowMediaChannelManager) InvokeTypingStop(string, string) {}
func (m *workflowMediaChannelManager) InvokeTypingStopForMessage(string, string, string) {
}

func (m *workflowMediaChannelManager) CleanupTurnUXForMessage(
	context.Context, string, string, string,
) {
}

func (m *workflowMediaChannelManager) RebindTurnUXForMessage(string, string, string, string) {
}

func (m *workflowMediaChannelManager) SendMessage(context.Context, bus.OutboundMessage) error {
	return nil
}

func (m *workflowMediaChannelManager) SendMedia(_ context.Context, msg bus.OutboundMediaMessage) error {
	m.sentMedia = append(m.sentMedia, msg)
	return nil
}

func (m *workflowMediaChannelManager) SendPlaceholder(context.Context, string, string) bool {
	return false
}

func (m *workflowMediaChannelManager) SendPlaceholderForMessage(
	context.Context,
	string,
	string,
	string,
) bool {
	return false
}

func (m *workflowMediaChannelManager) DismissToolFeedback(context.Context, string, string, *bus.InboundContext) {
}
