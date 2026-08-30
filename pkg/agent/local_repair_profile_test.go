package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestLocalRepairPromptCacheKeyIsStableSeparatedAndOpaque(t *testing.T) {
	t.Parallel()

	first, err := localRepairPromptCacheKey("workspace-secret-a", "sha256:prompt-secret-a")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := localRepairPromptCacheKey("workspace-secret-a", "sha256:prompt-secret-a")
	if err != nil {
		t.Fatal(err)
	}
	otherWorkspace, err := localRepairPromptCacheKey("workspace-secret-b", "sha256:prompt-secret-a")
	if err != nil {
		t.Fatal(err)
	}
	otherPrompt, err := localRepairPromptCacheKey("workspace-secret-a", "sha256:prompt-secret-b")
	if err != nil {
		t.Fatal(err)
	}
	if first != repeated || first == otherWorkspace || first == otherPrompt ||
		!validLocalRepairOpaqueDigest(first) {
		t.Fatalf(
			"cache keys do not have stable separated identities: %q %q %q %q",
			first,
			repeated,
			otherWorkspace,
			otherPrompt,
		)
	}
	want := sha256.Sum256([]byte(
		"picoclaw-local-repair-cache-v1\x00workspace-secret-a\x00sha256:prompt-secret-a",
	))
	if first != hex.EncodeToString(want[:]) {
		t.Fatalf("cache key = %q, want exact contract %x", first, want)
	}
	for _, private := range []string{"workspace-secret", "prompt-secret", "sha256:"} {
		if strings.Contains(first, private) {
			t.Fatalf("cache key %q exposed private input %q", first, private)
		}
	}
}

func TestLocalRepairProviderProfileAllowsOnlyEffectiveOptions(t *testing.T) {
	profile, err := newLocalRepairProviderProfile(
		2048,
		0.25,
		" LOW ",
		"workspace-secret",
		"sha256:prompt-secret",
	)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ReasoningEffort != "low" {
		t.Fatalf("reasoning effort = %q, want low", profile.ReasoningEffort)
	}
	options := profile.options()
	if err := profile.validateOptions(options); err != nil {
		t.Fatalf("valid profile options rejected: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"unknown option": func(value map[string]any) { value["private"] = "value" },
		"wrong tokens":   func(value map[string]any) { value["max_tokens"] = 2049 },
		"wrong cache":    func(value map[string]any) { value["prompt_cache_key"] = strings.Repeat("a", 64) },
		"bad effort":     func(value map[string]any) { value["reasoning_effort"] = "HIGH" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := profile.options()
			mutate(candidate)
			if err := profile.validateOptions(candidate); err == nil {
				t.Fatalf("mutated options accepted: %#v", candidate)
			}
		})
	}
}

func TestLocalRepairProviderProfileDigestBindsEffectiveOptions(t *testing.T) {
	t.Parallel()

	base, err := newLocalRepairProviderProfile(
		2048,
		0.25,
		"low",
		"workspace-a",
		"sha256:prompt-a",
	)
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := base.digest()
	if err != nil {
		t.Fatal(err)
	}
	variants := []localRepairProviderProfile{
		{
			MaxTokens:       4096,
			Temperature:     base.Temperature,
			CacheKey:        base.CacheKey,
			ReasoningEffort: base.ReasoningEffort,
		},
		{MaxTokens: base.MaxTokens, Temperature: 0.5, CacheKey: base.CacheKey, ReasoningEffort: base.ReasoningEffort},
		{
			MaxTokens:       base.MaxTokens,
			Temperature:     base.Temperature,
			CacheKey:        strings.Repeat("b", 64),
			ReasoningEffort: base.ReasoningEffort,
		},
		{MaxTokens: base.MaxTokens, Temperature: base.Temperature, CacheKey: base.CacheKey, ReasoningEffort: "high"},
	}
	for _, variant := range variants {
		digest, digestErr := variant.digest()
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		if digest == baseDigest {
			t.Fatalf("profile digest did not bind variant %#v", variant)
		}
	}
	if !strings.HasPrefix(baseDigest, "sha256:") || len(baseDigest) != len("sha256:")+64 {
		t.Fatalf("profile digest = %q", baseDigest)
	}
	for _, private := range []string{"workspace-a", "prompt-a", base.CacheKey} {
		if strings.Contains(baseDigest, private) {
			t.Fatalf("profile digest exposed private profile input %q", private)
		}
	}
}

func TestLocalRepairProviderProfileRejectsUnsupportedEffort(t *testing.T) {
	t.Parallel()

	if _, err := newLocalRepairProviderProfile(
		2048,
		0.25,
		"turbo",
		"workspace-a",
		"sha256:prompt-a",
	); err == nil {
		t.Fatal("unsupported reasoning effort was accepted")
	}
}

func TestLocalRepairRunnerKeepsProfileStableAcrossLoopAndProviderMutation(t *testing.T) {
	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	provider := &localRepairTestProvider{}
	provider.handler = func(
		index int,
		_ []providers.Message,
		_ []providers.ToolDefinition,
		_ string,
		options map[string]any,
	) (*providers.LLMResponse, error) {
		if index == 0 {
			options["prompt_cache_key"] = "provider mutation must stay detached"
			options["reasoning_effort"] = "high"
			return &providers.LLMResponse{ToolCalls: []providers.ToolCall{
				localRepairTestToolCall(
					"read-1",
					"read_file",
					map[string]any{"path": "README.md"},
				),
			}}, nil
		}
		return &providers.LLMResponse{Content: "done"}, nil
	}
	runner, err := NewLocalRepairRunner(LocalRepairRunnerConfig{
		Workspaces: acquirer, Provider: provider, Model: "repair-model",
		MaxIterations: 4, MaxTokens: 1234, Temperature: 0.25, ReasoningEffort: "LOW",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), localRepairTestRunRequest(pin))
	if err != nil {
		t.Fatal(err)
	}
	calls := provider.Calls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(calls))
	}
	firstCache, firstOK := calls[0].options["prompt_cache_key"].(string)
	secondCache, secondOK := calls[1].options["prompt_cache_key"].(string)
	if !firstOK || !secondOK || firstCache != secondCache ||
		!validLocalRepairOpaqueDigest(firstCache) ||
		calls[0].options["reasoning_effort"] != "low" ||
		calls[1].options["reasoning_effort"] != "low" {
		t.Fatalf("provider mutation changed stable profile: %#v / %#v", calls[0].options, calls[1].options)
	}
	if !strings.HasPrefix(result.ProfileDigest, "sha256:") ||
		strings.Contains(result.ProfileDigest, workspace.ID) ||
		strings.Contains(result.ProfileDigest, result.PromptDigest) ||
		strings.Contains(result.ProfileDigest, firstCache) {
		t.Fatalf("profile digest is invalid or non-opaque: %#v", result)
	}
}

func TestLocalRepairRunnerChangesCacheKeyWithPromptEvidence(t *testing.T) {
	pin, workspace, _ := newLocalRepairTestWorkspace(t)
	acquirer := &localRepairTestAcquirer{workspace: workspace}
	provider := &localRepairTestProvider{}
	runner := newLocalRepairTestRunner(t, acquirer, provider, 2)

	firstRequest := localRepairTestRunRequest(pin)
	first, err := runner.Run(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := firstRequest
	secondRequest.Context += " Additional validation evidence."
	second, err := runner.Run(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	calls := provider.Calls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(calls))
	}
	if calls[0].options["prompt_cache_key"] == calls[1].options["prompt_cache_key"] ||
		first.PromptDigest == second.PromptDigest || first.ProfileDigest == second.ProfileDigest {
		t.Fatalf("prompt evidence did not separate repair profiles: %#v / %#v", first, second)
	}
}

func TestControllerLocalRepairResolvesSelectedModelReasoningEffort(t *testing.T) {
	cfg := strictAliasTestConfig(t)
	cfg.ModelList[0].ReasoningEffort = " LOW "
	candidate := controllerRepairFactoryCandidate(
		"account-a", "coding", "openai", "gpt-5.4",
	)
	provider := &controllerRepairFactoryProvider{name: "selected"}
	agent := &AgentInstance{
		ID: "repairer", AccountRef: "account-a", Model: "coding",
		Workspace:  cfg.Agents.Defaults.Workspace,
		Candidates: []providers.FallbackCandidate{candidate}, Provider: provider,
		MaxIterations: 3, MaxTokens: 1024, Temperature: 0.25,
	}
	loop := newControllerRepairFactoryLoop(t, cfg, agent)
	runner, err := loop.NewControllerLocalRepairRunner("repairer", "route")
	if err != nil {
		t.Fatal(err)
	}
	if runner.reasoningEffort != "low" {
		t.Fatalf("selected reasoning effort = %q, want low", runner.reasoningEffort)
	}

	cfg.ModelList[0].ReasoningEffort = "high"
	nextRunner, err := loop.NewControllerLocalRepairRunner("repairer", "route")
	if err != nil {
		t.Fatal(err)
	}
	if runner.reasoningEffort != "low" || nextRunner.reasoningEffort != "high" {
		t.Fatalf(
			"runner profiles crossed config generations: old=%q next=%q",
			runner.reasoningEffort,
			nextRunner.reasoningEffort,
		)
	}

	cfg.ModelList[0].ReasoningEffort = "turbo"
	if loop.ControllerLocalRepairReady("repairer") {
		t.Fatal("readiness accepted unsupported selected reasoning effort")
	}
	if _, err := loop.NewControllerLocalRepairRunner("repairer", "route"); err == nil {
		t.Fatal("factory accepted unsupported selected reasoning effort")
	}
}
