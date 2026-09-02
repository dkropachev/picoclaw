package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/modelrouter"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type subTurnCountingProvider struct {
	calls    atomic.Int64
	mu       sync.Mutex
	tools    []providers.ToolDefinition
	messages []providers.Message
}

func (provider *subTurnCountingProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	definitions []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.calls.Add(1)
	provider.mu.Lock()
	provider.tools = append([]providers.ToolDefinition(nil), definitions...)
	provider.messages = append([]providers.Message(nil), messages...)
	provider.mu.Unlock()
	return &providers.LLMResponse{Content: "done"}, nil
}

func (provider *subTurnCountingProvider) prompt() string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.messages) == 0 {
		return ""
	}
	return provider.messages[0].Content
}

func (provider *subTurnCountingProvider) toolNames() []string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	names := make([]string, 0, len(provider.tools))
	for _, definition := range provider.tools {
		names = append(names, definition.Function.Name)
	}
	return names
}

type subTurnBlockingProvider struct {
	started chan struct{}
}

type subTurnImmediateErrorProvider struct{}

func (*subTurnImmediateErrorProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return nil, errors.New("injected permanent child provider failure")
}

type subTurnNativeFallbackProvider struct {
	supported bool
	fail      bool
	calls     atomic.Int64
	mu        sync.Mutex
	tools     []providers.ToolDefinition
	options   map[string]any
}

type subTurnCloseOrderSession struct {
	*ephemeralSessionStore
	order *[]string
}

type subTurnDisableNativeHook struct {
	mode string
}

type subTurnNativePolicyHook struct {
	model  string
	action string
}

func (hook subTurnNativePolicyHook) BeforeLLM(
	_ context.Context,
	request *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	next := request.Clone()
	if hook.model != "" {
		next.Model = hook.model
	}
	switch hook.action {
	case "enable":
		if next.Options == nil {
			next.Options = make(map[string]any)
		}
		next.Options["native_search"] = true
	case "narrow":
		delete(next.Options, "native_search")
	}
	return next, HookDecision{Action: HookActionModify}, nil
}

func (subTurnNativePolicyHook) AfterLLM(
	_ context.Context,
	response *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	return response, HookDecision{Action: HookActionContinue}, nil
}

func (hook subTurnDisableNativeHook) BeforeLLM(
	_ context.Context,
	request *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	next := request.Clone()
	switch hook.mode {
	case "false":
		next.Options["native_search"] = false
	case "nil":
		next.Options = nil
	default:
		delete(next.Options, "native_search")
	}
	return next, HookDecision{Action: HookActionModify}, nil
}

func (subTurnDisableNativeHook) AfterLLM(
	_ context.Context,
	response *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	return response, HookDecision{Action: HookActionContinue}, nil
}

func (store *subTurnCloseOrderSession) Close() error {
	*store.order = append(*store.order, "session")
	return nil
}

func (provider *subTurnNativeFallbackProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	definitions []providers.ToolDefinition,
	_ string,
	options map[string]any,
) (*providers.LLMResponse, error) {
	provider.calls.Add(1)
	provider.mu.Lock()
	provider.tools = append([]providers.ToolDefinition(nil), definitions...)
	provider.options = shallowCloneLLMOptions(options)
	provider.mu.Unlock()
	if provider.fail {
		return nil, errors.New("status: 429 - rate limit exceeded")
	}
	return &providers.LLMResponse{Content: "fallback native result"}, nil
}

func (provider *subTurnNativeFallbackProvider) SupportsNativeSearch() bool {
	return provider.supported
}

func (provider *subTurnNativeFallbackProvider) snapshot() ([]string, map[string]any) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	names := make([]string, 0, len(provider.tools))
	for _, definition := range provider.tools {
		names = append(names, definition.Function.Name)
	}
	return names, shallowCloneLLMOptions(provider.options)
}

func (provider *subTurnBlockingProvider) Chat(
	ctx context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	select {
	case <-provider.started:
	default:
		close(provider.started)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type subTurnSelectorTestTool struct {
	name           string
	marker         byte
	panicOnName    bool
	closeCount     *atomic.Int64
	closeErr       error
	executions     *atomic.Int64
	executeEntered chan struct{}
	releaseExecute chan struct{}
}

type subTurnValueSelector string

func (selector subTurnValueSelector) Name() string      { return string(selector) }
func (subTurnValueSelector) Description() string        { return "value selector" }
func (subTurnValueSelector) Parameters() map[string]any { return nil }
func (subTurnValueSelector) Execute(context.Context, map[string]any) *tools.ToolResult {
	return tools.SilentResult("unused")
}

func (tool *subTurnSelectorTestTool) Name() string {
	_ = tool.marker
	if tool.panicOnName {
		panic("selector name panic")
	}
	return tool.name
}

func (*subTurnSelectorTestTool) Description() string { return "subturn selector fixture" }

func (*subTurnSelectorTestTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *subTurnSelectorTestTool) Execute(
	context.Context,
	map[string]any,
) *tools.ToolResult {
	if tool.executions != nil {
		tool.executions.Add(1)
	}
	if tool.executeEntered != nil {
		select {
		case <-tool.executeEntered:
		default:
			close(tool.executeEntered)
		}
	}
	if tool.releaseExecute != nil {
		<-tool.releaseExecute
	}
	return tools.SilentResult("ok")
}

func (tool *subTurnSelectorTestTool) Close() error {
	if tool.closeCount != nil {
		tool.closeCount.Add(1)
	}
	return tool.closeErr
}

func registerSubTurnSelectorFactory(
	t *testing.T,
	registry *tools.ToolRegistry,
	name string,
	build func() *subTurnSelectorTestTool,
) {
	t.Helper()
	if build == nil {
		build = func() *subTurnSelectorTestTool {
			return &subTurnSelectorTestTool{name: name}
		}
	}
	live := &subTurnSelectorTestTool{name: name}
	factory, err := tools.NewToolFactoryFromPrototype(
		live,
		tools.ToolTraits{Sharing: tools.ToolSharingPerOwner},
		func(tools.ToolBuildContext) (tools.Tool, error) { return build(), nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterFactoryBacked(live, factory); err != nil {
		t.Fatal(err)
	}
}

func newSubTurnSelectionAgent(
	t *testing.T,
	id string,
	names ...string,
) *AgentInstance {
	t.Helper()
	registry := tools.NewToolRegistry()
	for _, name := range names {
		registerSubTurnSelectorFactory(t, registry, name, nil)
	}
	t.Cleanup(func() { _ = registry.Close() })
	return &AgentInstance{
		ID: id, Model: "model-" + id, Workspace: t.TempDir(),
		Provider: &simpleMockProvider{response: "done"}, Tools: registry,
	}
}

func TestSelectEffectiveSubTurnToolsNilEmptyExactProfileAndDepth(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.PreferNative = false
	agent := newSubTurnSelectionAgent(
		t,
		"main",
		"read_file",
		"write_file",
		"spawn",
		"subagent",
		"spawn_status",
		"delegate",
	)
	agent.Tools.Register(&subTurnSelectorTestTool{name: "threads"})
	parent := &turnState{agent: agent}

	inherited, err := selectEffectiveSubTurnTools(cfg, parent, agent, nil, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	wantInherited := []string{
		"delegate", "read_file", "spawn", "spawn_status", "subagent", "write_file",
	}
	if !slices.Equal(inherited.roots, wantInherited) {
		t.Fatalf("inherited roots = %v, want %v", inherited.roots, wantInherited)
	}
	if inherited.profile.ToolsMode != config.TurnProfileModeCustom ||
		!slices.Equal(inherited.profile.AllowedTools, wantInherited) {
		t.Fatalf("inherited profile = %#v", inherited.profile)
	}

	empty, err := selectEffectiveSubTurnTools(
		cfg,
		parent,
		agent,
		[]tools.Tool{},
		1,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.roots) != 0 || empty.nativeSearch ||
		empty.profile.ToolsMode != config.TurnProfileModeOff {
		t.Fatalf("explicit empty selection = %#v", empty)
	}

	exact, err := selectEffectiveSubTurnTools(
		cfg,
		parent,
		agent,
		[]tools.Tool{&subTurnSelectorTestTool{name: " READ_FILE "}},
		1,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(exact.roots, []string{"read_file"}) {
		t.Fatalf("foreign exact selector roots = %v", exact.roots)
	}

	parent.profile = config.EffectiveTurnProfile{
		Enabled:      true,
		ToolsMode:    config.TurnProfileModeCustom,
		AllowedTools: []string{"write_file", "spawn_status"},
	}
	profiled, err := selectEffectiveSubTurnTools(cfg, parent, agent, nil, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(profiled.roots, []string{"write_file"}) {
		t.Fatalf("profiled roots = %v", profiled.roots)
	}

	parent.profile = config.EffectiveTurnProfile{}
	depthBounded, err := selectEffectiveSubTurnTools(cfg, parent, agent, nil, 3, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(depthBounded.roots, []string{"read_file", "write_file"}) {
		t.Fatalf("depth-bounded roots = %v", depthBounded.roots)
	}
	_, err = selectEffectiveSubTurnTools(
		cfg,
		parent,
		agent,
		[]tools.Tool{&subTurnSelectorTestTool{name: "spawn_status"}},
		1,
		3,
	)
	if err == nil || !errors.Is(err, ErrInvalidSubTurnConfig) ||
		!strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("status-only selector error = %v", err)
	}
}

func TestSelectEffectiveSubTurnToolsExplicitSelectorFailures(t *testing.T) {
	cfg := config.DefaultConfig()
	agent := newSubTurnSelectionAgent(t, "main", "read_file")
	agent.Tools.Register(&subTurnSelectorTestTool{name: "legacy"})
	parent := &turnState{agent: agent}
	var typedNil *subTurnSelectorTestTool
	tests := []struct {
		name     string
		selector tools.Tool
		contains string
	}{
		{name: "nil", selector: nil, contains: "is nil"},
		{name: "typed nil", selector: typedNil, contains: "is nil"},
		{name: "blank", selector: &subTurnSelectorTestTool{name: "  "}, contains: "blank"},
		{name: "panic", selector: &subTurnSelectorTestTool{name: "read_file", panicOnName: true}, contains: "panicked"},
		{name: "unknown", selector: &subTurnSelectorTestTool{name: "missing"}, contains: "unknown"},
		{name: "legacy", selector: &subTurnSelectorTestTool{name: "legacy"}, contains: "unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := selectEffectiveSubTurnTools(
				cfg,
				parent,
				agent,
				[]tools.Tool{test.selector},
				1,
				3,
			)
			if err == nil || !errors.Is(err, ErrInvalidSubTurnConfig) ||
				!strings.Contains(err.Error(), test.contains) {
				t.Fatalf("selector error = %v", err)
			}
		})
	}

	ambiguous := newSubTurnSelectionAgent(t, "ambiguous", "case_tool", "CASE_TOOL")
	_, err := selectEffectiveSubTurnTools(
		cfg,
		&turnState{agent: ambiguous},
		ambiguous,
		[]tools.Tool{&subTurnSelectorTestTool{name: "Case_Tool"}},
		1,
		3,
	)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous selector error = %v", err)
	}
}

func TestSelectEffectiveSubTurnToolsRejectsEveryFactoryBackedStaticDenylistRoot(t *testing.T) {
	cfg := config.DefaultConfig()
	registry := tools.NewToolRegistry()
	var builds atomic.Int64
	denied := []string{"threads", "workflow", "cron", "exec", "exec_command", "write_stdin"}
	for _, name := range denied {
		live := &subTurnSelectorTestTool{name: name}
		factory, err := tools.NewToolFactoryFromPrototype(
			live,
			tools.ToolTraits{Sharing: tools.ToolSharingPerOwner},
			func(tools.ToolBuildContext) (tools.Tool, error) {
				builds.Add(1)
				return &subTurnSelectorTestTool{name: name}, nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.RegisterFactoryBacked(live, factory); err != nil {
			t.Fatal(err)
		}
	}
	defer registry.Close()
	agent := &AgentInstance{
		ID: "denylist", Model: "denylist-model", Workspace: t.TempDir(),
		Provider: &simpleMockProvider{response: "unused"}, Tools: registry,
	}
	parent := &turnState{agent: agent}
	selection, err := selectEffectiveSubTurnTools(cfg, parent, agent, nil, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.roots) != 0 || builds.Load() != 0 {
		t.Fatalf("implicit denied selection = roots:%v builds:%d", selection.roots, builds.Load())
	}
	for _, name := range denied {
		_, err := selectEffectiveSubTurnTools(
			cfg,
			parent,
			agent,
			[]tools.Tool{&subTurnSelectorTestTool{name: name}},
			1,
			3,
		)
		if err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("explicit denied tool %q error = %v", name, err)
		}
	}
	if builds.Load() != 0 {
		t.Fatalf("denied factories ran before rejection: %d", builds.Load())
	}
}

func TestSelectEffectiveSubTurnToolsParentTargetAndNestedIntersection(t *testing.T) {
	cfg := config.DefaultConfig()
	parentAgent := newSubTurnSelectionAgent(t, "parent", "alpha", "beta")
	targetAgent := newSubTurnSelectionAgent(t, "target", "alpha", "beta", "gamma")
	parent := &turnState{
		agent: parentAgent,
		profile: config.EffectiveTurnProfile{
			Enabled:      true,
			ToolsMode:    config.TurnProfileModeCustom,
			AllowedTools: []string{"beta", "gamma"},
		},
	}
	selection, err := selectEffectiveSubTurnTools(
		cfg,
		parent,
		targetAgent,
		[]tools.Tool{
			&subTurnSelectorTestTool{name: "BETA"},
			&subTurnSelectorTestTool{name: "beta"},
		},
		1,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(selection.roots, []string{"beta"}) {
		t.Fatalf("four-way intersection roots = %v", selection.roots)
	}

	parentOwned, err := parentAgent.Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "parent-owner"},
		[]string{"alpha"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer parentOwned.Close()
	nestedAgent := *parentAgent
	nestedAgent.Tools = parentOwned
	nestedParent := &turnState{
		agent:              &nestedAgent,
		toolAuthorityBound: true,
	}
	nested, err := selectEffectiveSubTurnTools(
		cfg,
		nestedParent,
		targetAgent,
		nil,
		2,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(nested.roots, []string{"alpha"}) {
		t.Fatalf("nested target roots = %v", nested.roots)
	}
}

func TestSelectEffectiveSubTurnToolsNativePseudoCapabilitySurvivesNestedOwner(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.PreferNative = true
	agent := newSubTurnSelectionAgent(t, "native")
	agent.Provider = &nativeSearchProvider{supported: true}
	parent := &turnState{agent: agent}
	first, err := selectEffectiveSubTurnTools(cfg, parent, agent, nil, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.roots) != 0 || !first.nativeSearch ||
		!slices.Equal(first.profile.AllowedTools, []string{"web_search"}) {
		t.Fatalf("first native selection = %#v", first)
	}

	emptyOwned, err := agent.Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "native-parent"},
		[]string{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer emptyOwned.Close()
	nestedAgent := *agent
	nestedAgent.Tools = emptyOwned
	nestedParent := &turnState{
		agent:               &nestedAgent,
		profile:             first.profile,
		toolAuthorityBound:  true,
		nativeSearchAllowed: true,
	}
	nested, err := selectEffectiveSubTurnTools(cfg, nestedParent, &nestedAgent, nil, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !nested.nativeSearch || !slices.Equal(nested.profile.AllowedTools, []string{"web_search"}) {
		t.Fatalf("nested native selection = %#v", nested)
	}
	blocked, err := selectEffectiveSubTurnTools(
		cfg,
		nestedParent,
		&nestedAgent,
		[]tools.Tool{},
		2,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.nativeSearch || blocked.profile.ToolsMode != config.TurnProfileModeOff {
		t.Fatalf("explicit-empty native selection = %#v", blocked)
	}

	mixed := newSubTurnSelectionAgent(t, "mixed-native")
	mixed.Provider = &nativeSearchProvider{supported: true}
	mixed.CandidateProviders = make(map[string]providers.LLMProvider)
	mixed.Candidates = []providers.FallbackCandidate{
		{Provider: "mock", Model: "native", IdentityKey: "native"},
		{Provider: "mock", Model: "client", IdentityKey: "client"},
	}
	bindBootstrapProvider(
		mixed.CandidateProviders,
		mixed.Candidates[0],
		&nativeSearchProvider{supported: true},
	)
	bindBootstrapProvider(
		mixed.CandidateProviders,
		mixed.Candidates[1],
		&nativeSearchProvider{supported: false},
	)
	mixedSelection, err := selectEffectiveSubTurnTools(
		cfg,
		&turnState{agent: mixed},
		mixed,
		nil,
		1,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if mixedSelection.nativeSearch {
		t.Fatalf("pseudo-only mixed-provider selection enabled native search: %#v", mixedSelection)
	}
	lightMixed := newSubTurnSelectionAgent(t, "light-mixed")
	lightMixed.Provider = &nativeSearchProvider{supported: true}
	lightMixed.LightProvider = &nativeSearchProvider{supported: false}
	lightSelection, err := selectEffectiveSubTurnTools(
		cfg,
		&turnState{agent: lightMixed},
		lightMixed,
		nil,
		1,
		3,
	)
	if err != nil || lightSelection.nativeSearch {
		t.Fatalf("pseudo-only light-provider selection = %#v, %v", lightSelection, err)
	}
	imageMixed := newSubTurnSelectionAgent(t, "image-mixed")
	imageMixed.Provider = &nativeSearchProvider{supported: true}
	imageMixed.CandidateProviders = make(map[string]providers.LLMProvider)
	imageMixed.ImageCandidates = []providers.FallbackCandidate{{
		Provider: "mock", Model: "image-client", IdentityKey: "image-client",
	}}
	bindBootstrapProvider(
		imageMixed.CandidateProviders,
		imageMixed.ImageCandidates[0],
		&nativeSearchProvider{supported: false},
	)
	imageSelection, err := selectEffectiveSubTurnTools(
		cfg,
		&turnState{agent: imageMixed},
		imageMixed,
		nil,
		1,
		3,
	)
	if err != nil || imageSelection.nativeSearch {
		t.Fatalf("pseudo-only image-provider selection = %#v, %v", imageSelection, err)
	}

	physicalTarget := newSubTurnSelectionAgent(t, "physical-target", "Web_Search")
	physicalTarget.Provider = &nativeSearchProvider{supported: false}
	targetSelection, err := selectEffectiveSubTurnTools(
		cfg,
		parent,
		physicalTarget,
		[]tools.Tool{&subTurnSelectorTestTool{name: "web_search"}},
		1,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(targetSelection.roots, []string{"Web_Search"}) {
		t.Fatalf("pseudo-parent physical-target roots = %v", targetSelection.roots)
	}

	pseudoTarget := newSubTurnSelectionAgent(t, "pseudo-target")
	pseudoTarget.Provider = &nativeSearchProvider{supported: true}
	noWebParent := newSubTurnSelectionAgent(t, "no-web-parent")
	noWebParent.Provider = &nativeSearchProvider{supported: false}
	_, err = selectEffectiveSubTurnTools(
		cfg,
		&turnState{agent: noWebParent},
		pseudoTarget,
		[]tools.Tool{&subTurnSelectorTestTool{name: "web_search"}},
		1,
		3,
	)
	if err == nil || !strings.Contains(err.Error(), "unavailable") ||
		strings.Contains(err.Error(), "unknown") {
		t.Fatalf("target-known pseudo selector error = %v", err)
	}
}

func TestSubTurnNativeAuthorityLeaseSnapshotAndProviderSetProofAreFailClosed(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.PreferNative = true
	agent := newSubTurnSelectionAgent(t, "native-proof")
	agent.Model = "primary"
	agent.Fallbacks = []string{"fallback"}
	agent.Provider = &nativeSearchProvider{supported: true}
	parent := &turnState{
		agent: agent, nativeSearchObserved: true, nativeSearchAllowed: false,
	}
	lease, err := parent.retainSubTurnConstruction()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.release()
	parent.recordNativeSearchObservation(true)
	frozen, err := selectEffectiveSubTurnTools(
		cfg,
		parent,
		agent,
		nil,
		1,
		3,
		subTurnToolSelectionOptions{
			implementationProviderSetProven: true,
			parentAuthorityFrozen:           true,
			parentAuthority:                 lease.nativeAuthority,
		},
	)
	if err != nil || frozen.nativeSearch {
		t.Fatalf("frozen non-native request authority = %#v, %v", frozen, err)
	}
	live, err := selectEffectiveSubTurnTools(cfg, parent, agent, nil, 1, 3)
	if err != nil || !live.nativeSearch {
		t.Fatalf("later native request authority = %#v, %v", live, err)
	}

	proofCases := []struct {
		name string
		cfg  SubTurnConfig
		want bool
	}{
		{name: "configured", cfg: SubTurnConfig{Model: "primary"}, want: true},
		{
			name: "exact-fallbacks",
			cfg:  SubTurnConfig{Model: "primary", ModelFallbacks: []string{"fallback"}},
			want: true,
		},
		{name: "custom-model", cfg: SubTurnConfig{Model: "other"}},
		{name: "custom-fallbacks", cfg: SubTurnConfig{Model: "primary", ModelFallbacks: []string{"other"}}},
		{name: "target-generation", cfg: SubTurnConfig{TargetAgentID: "target"}, want: true},
	}
	for _, testCase := range proofCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := subTurnUsesProvenImplementationProviderSet(agent, testCase.cfg); got != testCase.want {
				t.Fatalf("provider-set proof = %t, want %t", got, testCase.want)
			}
		})
	}
	agent.ModelRouter = modelrouter.New("dynamic", &config.ModelRouterConfig{Enabled: true})
	if subTurnAgentProvidersSupportNativeSearch(agent) {
		t.Fatal("model-router provider set was treated as statically proven")
	}
	unproven, err := selectEffectiveSubTurnTools(
		cfg,
		parent,
		agent,
		[]tools.Tool{&subTurnSelectorTestTool{name: "web_search"}},
		1,
		3,
		subTurnToolSelectionOptions{
			implementationProviderSetProven: false,
			parentAuthorityFrozen:           true,
			parentAuthority: subTurnNativeAuthoritySnapshot{
				observed: true,
				allowed:  true,
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "unavailable") || unproven.nativeSearch {
		t.Fatalf("unproven explicit pseudo search = %#v, %v", unproven, err)
	}
}

func TestSpawnSubTurnCentralTargetAuthorizationFailsBeforeConstructionAndProvider(t *testing.T) {
	provider := &subTurnCountingProvider{}
	loop, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	parentAgent, _ := loop.registry.GetAgent("alpha")
	parentAgent.Subagents = &config.SubagentsConfig{AllowAgents: []string{}}
	parent := &turnState{
		turnID: "unauthorized-parent", agent: parentAgent,
		pendingResults: make(chan *tools.ToolResult, 1),
		concurrencySem: make(chan struct{}, 1),
	}
	result, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, parent, SubTurnConfig{
		TargetAgentID: "beta", SystemPrompt: "unauthorized",
	})
	if result != nil || err == nil || !errors.Is(err, ErrInvalidSubTurnConfig) ||
		!strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("unauthorized target result = %#v, %v", result, err)
	}
	if provider.calls.Load() != 0 || len(parent.childTurnIDs) != 0 {
		t.Fatalf("unauthorized target effects = calls:%d children:%v", provider.calls.Load(), parent.childTurnIDs)
	}
}

func TestSpawnSubTurnRuntimeEnforcesParentTargetExplicitProfileIntersection(t *testing.T) {
	provider := &subTurnCountingProvider{}
	loop, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	parentAgent, _ := loop.registry.GetAgent("alpha")
	targetAgent, _ := loop.registry.GetAgent("beta")
	for _, registration := range []struct {
		registry *tools.ToolRegistry
		name     string
	}{
		{registry: parentAgent.Tools, name: "intersection_shared"},
		{registry: parentAgent.Tools, name: "intersection_parent_only"},
		{registry: targetAgent.Tools, name: "intersection_shared"},
		{registry: targetAgent.Tools, name: "intersection_target_only"},
	} {
		registerSubTurnSelectorFactory(t, registration.registry, registration.name, nil)
	}
	parent := &turnState{
		turnID: "intersection-parent", agent: parentAgent,
		pendingResults: make(chan *tools.ToolResult, 1),
		concurrencySem: make(chan struct{}, 1),
		profile: config.EffectiveTurnProfile{
			Enabled:      true,
			ToolsMode:    config.TurnProfileModeCustom,
			AllowedTools: []string{"intersection_shared", "intersection_target_only"},
		},
	}
	result, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, parent, SubTurnConfig{
		TargetAgentID: "beta", SystemPrompt: "intersect",
		Tools: []tools.Tool{&subTurnSelectorTestTool{name: "INTERSECTION_SHARED"}},
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("intersection result = %#v, %v", result, err)
	}
	if names := provider.toolNames(); !slices.Equal(names, []string{"intersection_shared"}) {
		t.Fatalf("intersection provider tools = %v", names)
	}

	callsBefore := provider.calls.Load()
	result, err = spawnSubTurnFromTrustedRuntime(context.Background(), loop, &turnState{
		turnID: "intersection-unavailable-parent", agent: parentAgent,
		pendingResults: make(chan *tools.ToolResult, 1),
		concurrencySem: make(chan struct{}, 1),
	}, SubTurnConfig{
		TargetAgentID: "beta", SystemPrompt: "must fail",
		Tools: []tools.Tool{&subTurnSelectorTestTool{name: "intersection_target_only"}},
	})
	if result != nil || err == nil || !strings.Contains(err.Error(), "unavailable") ||
		provider.calls.Load() != callsBefore {
		t.Fatalf(
			"target-only selector = result:%#v error:%v calls:%d/%d",
			result,
			err,
			provider.calls.Load(),
			callsBefore,
		)
	}
}

func TestSpawnSubTurnRuntimeAppliesNilEmptyAndForeignExactSelectors(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &turnProfileCaptureProvider{}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	defer func() {
		loop.Close()
		messageBus.Close()
	}()
	agent := loop.GetRegistry().GetDefaultAgent()
	newParent := func(turnID string) *turnState {
		return &turnState{
			turnID: turnID, agent: agent,
			pendingResults: make(chan *tools.ToolResult, 1),
			concurrencySem: make(chan struct{}, 1),
		}
	}
	if _, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, newParent("nil-parent"), SubTurnConfig{
		Model: "test-model", SystemPrompt: "inherit", Tools: nil,
	}); err != nil {
		t.Fatal(err)
	}
	if len(provider.tools) == 0 {
		t.Fatal("nil selector did not inherit constructible tools")
	}
	if _, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, newParent("empty-parent"), SubTurnConfig{
		Model: "test-model", SystemPrompt: "empty", Tools: []tools.Tool{},
	}); err != nil {
		t.Fatal(err)
	}
	if len(provider.tools) != 0 {
		t.Fatalf("explicit empty provider tools = %#v", provider.tools)
	}
	if _, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, newParent("exact-parent"), SubTurnConfig{
		Model: "test-model", SystemPrompt: "exact",
		Tools: []tools.Tool{&subTurnSelectorTestTool{name: "READ_FILE"}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(provider.tools) != 1 || provider.tools[0].Function.Name != "read_file" {
		t.Fatalf("foreign exact provider tools = %#v", provider.tools)
	}
}

func TestSpawnSubTurnConstructsToolsForExactScopedTurnOwner(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&simpleMockProvider{response: "owner done"},
	)
	defer func() {
		loop.Close()
		messageBus.Close()
	}()
	agent := loop.GetRegistry().GetDefaultAgent()
	owners := make(chan tools.ToolOwner, 1)
	live := &subTurnSelectorTestTool{name: "owner_probe"}
	factory, err := tools.NewToolFactoryFromPrototype(
		live,
		tools.ToolTraits{Sharing: tools.ToolSharingPerOwner},
		func(ctx tools.ToolBuildContext) (tools.Tool, error) {
			owners <- ctx.Owner()
			return &subTurnSelectorTestTool{name: "owner_probe"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if registerErr := agent.Tools.RegisterFactoryBacked(live, factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	parent := &turnState{
		turnID: "owner-parent", agent: agent,
		pendingResults: make(chan *tools.ToolResult, 1),
		concurrencySem: make(chan struct{}, 1),
	}
	result, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, parent, SubTurnConfig{
		Model: "test-model", SystemPrompt: "check owner",
		Tools: []tools.Tool{&subTurnSelectorTestTool{name: "owner_probe"}},
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("owner child result = %#v, %v", result, err)
	}
	owner := <-owners
	parent.mu.RLock()
	var child *turnState
	for _, candidate := range parent.childTurns {
		child = candidate
		break
	}
	parent.mu.RUnlock()
	if child == nil {
		t.Fatal("exact child was not retained")
	}
	if owner.Scope != tools.ToolOwnerScopeTurn || owner.AgentID != child.agentID ||
		owner.SessionKey != child.sessionKey || owner.TurnID != child.turnID {
		t.Fatalf("tool owner = %#v, child = agent:%q session:%q turn:%q",
			owner, child.agentID, child.sessionKey, child.turnID)
	}
	if owner.TurnID == owner.SessionKey {
		t.Fatalf("scoped turn identity collapsed to child label: %#v", owner)
	}
}

func TestSpawnSubTurnNestedRuntimeCannotRegainRemovedRoot(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &subTurnCountingProvider{}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	defer func() {
		loop.Close()
		messageBus.Close()
	}()
	rootAgent := loop.GetRegistry().GetDefaultAgent()
	registerSubTurnSelectorFactory(t, rootAgent.Tools, "nested_kept", nil)
	registerSubTurnSelectorFactory(t, rootAgent.Tools, "nested_removed", nil)
	parentTools, err := rootAgent.Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "nested-runtime-parent"},
		[]string{"nested_kept"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer parentTools.Close()
	parentAgent := *rootAgent
	parentAgent.Tools = parentTools
	parent := &turnState{
		turnID: "nested-runtime-parent", agent: &parentAgent,
		pendingResults:     make(chan *tools.ToolResult, 1),
		concurrencySem:     make(chan struct{}, 1),
		toolAuthorityBound: true,
	}
	result, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, parent, SubTurnConfig{
		Model: "test-model", SystemPrompt: "nested",
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("nested runtime result = %#v, %v", result, err)
	}
	if names := provider.toolNames(); !slices.Equal(names, []string{"nested_kept"}) {
		t.Fatalf("nested runtime provider tools = %v", names)
	}
}

func TestSpawnSubTurnPromptContributorsUseExactEffectiveChildTools(t *testing.T) {
	provider := &subTurnCountingProvider{}
	loop, cleanup := newMultiAgentLoop(t, provider)
	defer cleanup()
	parentAgent, _ := loop.registry.GetAgent("alpha")
	if !parentAgent.Tools.HasRegistered("spawn") {
		registerSubTurnSelectorFactory(t, parentAgent.Tools, "spawn", nil)
	}
	if !parentAgent.Tools.HasRegistered("read_file") {
		registerSubTurnSelectorFactory(t, parentAgent.Tools, "read_file", nil)
	}
	newParent := func(turnID string) *turnState {
		return &turnState{
			turnID: turnID, agent: parentAgent,
			pendingResults: make(chan *tools.ToolResult, 1),
			concurrencySem: make(chan struct{}, 1),
		}
	}
	if _, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, newParent("prompt-inherit"), SubTurnConfig{
		Model: "model-alpha", SystemPrompt: "inherit prompt", Tools: nil,
	}); err != nil {
		t.Fatal(err)
	}
	if prompt := provider.prompt(); !strings.Contains(prompt, "# Agent Discovery") {
		t.Fatalf("inherited child prompt omitted spawn contributor:\n%s", prompt)
	}
	if _, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, newParent("prompt-empty"), SubTurnConfig{
		Model: "model-alpha", SystemPrompt: "empty prompt", Tools: []tools.Tool{},
	}); err != nil {
		t.Fatal(err)
	}
	if prompt := provider.prompt(); strings.Contains(prompt, "# Agent Discovery") ||
		strings.Contains(prompt, toolUseSystemPromptRule()) {
		t.Fatalf("empty child prompt retained tool contributors:\n%s", prompt)
	}
	if _, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, newParent("prompt-read"), SubTurnConfig{
		Model: "model-alpha", SystemPrompt: "read prompt",
		Tools: []tools.Tool{&subTurnSelectorTestTool{name: "read_file"}},
	}); err != nil {
		t.Fatal(err)
	}
	if prompt := provider.prompt(); strings.Contains(prompt, "# Agent Discovery") ||
		!strings.Contains(prompt, toolUseSystemPromptRule()) {
		t.Fatalf("read-only child prompt contributor mismatch:\n%s", prompt)
	}
}

func TestSpawnSubTurnStrictResourcesCloseAndCloseFailureRelabelsResult(t *testing.T) {
	for _, test := range []struct {
		name      string
		closeErr  error
		wantError bool
	}{
		{name: "successful close"},
		{name: "close failure", closeErr: errors.New("injected turn tool close failure"), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			provider := &subTurnCountingProvider{}
			messageBus := bus.NewMessageBus()
			loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
			defer func() {
				loop.Close()
				messageBus.Close()
			}()
			agent := loop.GetRegistry().GetDefaultAgent()
			events, closeEvents := subscribeRuntimeEventsForTest(
				t,
				loop,
				8,
				runtimeevents.KindAgentTurnEnd,
			)
			defer closeEvents()
			var closeCount atomic.Int64
			registerSubTurnSelectorFactory(
				t,
				agent.Tools,
				"turn_close_probe",
				func() *subTurnSelectorTestTool {
					return &subTurnSelectorTestTool{
						name: "turn_close_probe", closeCount: &closeCount, closeErr: test.closeErr,
					}
				},
			)
			parent := &turnState{
				turnID: "close-parent", agent: agent,
				pendingResults: make(chan *tools.ToolResult, 1),
				concurrencySem: make(chan struct{}, 1),
			}
			result, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, parent, SubTurnConfig{
				Model: "test-model", SystemPrompt: "finish",
			})
			if test.wantError {
				if err == nil || result == nil || !result.IsError ||
					!strings.Contains(err.Error(), "injected turn tool close failure") {
					t.Fatalf("close failure result = %#v, %v", result, err)
				}
			} else if err != nil || result == nil || result.IsError {
				t.Fatalf("successful close result = %#v, %v", result, err)
			}
			if closeCount.Load() != 1 {
				t.Fatalf("turn product close count = %d", closeCount.Load())
			}
			wantStatus := TurnEndStatusCompleted
			if test.wantError {
				wantStatus = TurnEndStatusError
			}
			if status := waitForTurnEndStatus(t, events, "main-turn-1"); status != wantStatus {
				t.Fatalf("turn.end status = %q, want %q", status, wantStatus)
			}
		})
	}
}

func TestSpawnSubTurnStrictResourcesCloseOnceOnPanicAndHardAbort(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider providers.LLMProvider
		closeErr error
	}{
		{name: "error", provider: &subTurnImmediateErrorProvider{}},
		{
			name: "panic", provider: &panicMockProvider{},
			closeErr: errors.New("secondary close failure"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			messageBus := bus.NewMessageBus()
			loop := newTestAgentLoopWithStrictModels(cfg, messageBus, test.provider)
			defer func() {
				loop.Close()
				messageBus.Close()
			}()
			agent := loop.GetRegistry().GetDefaultAgent()
			var closeCount atomic.Int64
			toolName := test.name + "_close_probe"
			registerSubTurnSelectorFactory(t, agent.Tools, toolName, func() *subTurnSelectorTestTool {
				return &subTurnSelectorTestTool{
					name: toolName, closeCount: &closeCount, closeErr: test.closeErr,
				}
			})
			parent := &turnState{
				turnID: test.name + "-resource-parent", agent: agent,
				pendingResults: make(chan *tools.ToolResult, 1),
				concurrencySem: make(chan struct{}, 1),
			}
			result, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, parent, SubTurnConfig{
				Model: "test-model", SystemPrompt: test.name,
			})
			if err == nil || result == nil || !result.IsError || closeCount.Load() != 1 {
				t.Fatalf(
					"%s resources = result:%#v error:%v closes:%d",
					test.name,
					result,
					err,
					closeCount.Load(),
				)
			}
			if test.name == "panic" &&
				(!strings.Contains(err.Error(), "intentional panic") ||
					strings.Contains(err.Error(), "secondary close failure")) {
				t.Fatalf("original panic did not retain precedence: %v", err)
			}
		})
	}

	for _, test := range []struct {
		name string
		hard bool
	}{
		{name: "cancellation"},
		{name: "hard abort", hard: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			provider := &subTurnBlockingProvider{started: make(chan struct{})}
			messageBus := bus.NewMessageBus()
			loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
			defer func() {
				loop.Close()
				messageBus.Close()
			}()
			agent := loop.GetRegistry().GetDefaultAgent()
			events, closeEvents := subscribeRuntimeEventsForTest(
				t,
				loop,
				8,
				runtimeevents.KindAgentTurnEnd,
			)
			defer closeEvents()
			var closeCount atomic.Int64
			toolName := strings.ReplaceAll(test.name, " ", "_") + "_close_probe"
			registerSubTurnSelectorFactory(t, agent.Tools, toolName, func() *subTurnSelectorTestTool {
				product := &subTurnSelectorTestTool{name: toolName, closeCount: &closeCount}
				if test.hard {
					product.closeErr = errors.New("hard-abort close failure")
				}
				return product
			})
			parent := &turnState{
				turnID: test.name + "-resource-parent", agent: agent,
				pendingResults: make(chan *tools.ToolResult, 1),
				concurrencySem: make(chan struct{}, 1),
			}
			type spawnOutcome struct {
				result *tools.ToolResult
				err    error
			}
			done := make(chan spawnOutcome, 1)
			go func() {
				result, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, parent, SubTurnConfig{
					Model: "test-model", SystemPrompt: "block",
				})
				done <- spawnOutcome{result: result, err: err}
			}()
			select {
			case <-provider.started:
			case <-time.After(2 * time.Second):
				t.Fatal("child provider did not start")
			}
			parent.mu.RLock()
			childIDs := append([]string(nil), parent.childTurnIDs...)
			parent.mu.RUnlock()
			if len(childIDs) != 1 {
				t.Fatalf("terminating children = %v", childIDs)
			}
			if test.hard {
				if err := loop.HardAbort(childIDs[0]); err != nil {
					t.Fatal(err)
				}
			} else {
				child := loop.getActiveTurnState(childIDs[0])
				if child == nil {
					t.Fatal("active child is unavailable")
				}
				requestTurnTreeCancellation(loop, child, false)
			}
			select {
			case outcome := <-done:
				if outcome.err == nil || outcome.result == nil || !outcome.result.IsError {
					t.Fatalf("termination outcome = %#v, %v", outcome.result, outcome.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("terminated child did not finish")
			}
			if closeCount.Load() != 1 {
				t.Fatalf("termination close count = %d", closeCount.Load())
			}
			wantStatus := TurnEndStatusError
			if status := waitForTurnEndStatus(t, events, "main-turn-1"); status != wantStatus {
				t.Fatalf("termination turn.end status = %q, want %q", status, wantStatus)
			}
		})
	}
}

func TestSpawnSubTurnFactoryFailureClosesEarlierProductsBeforePublication(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &subTurnCountingProvider{}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	defer func() {
		loop.Close()
		messageBus.Close()
	}()
	agent := loop.GetRegistry().GetDefaultAgent()
	var closeCount atomic.Int64
	registerSubTurnSelectorFactory(
		t,
		agent.Tools,
		"aaa_constructed",
		func() *subTurnSelectorTestTool {
			return &subTurnSelectorTestTool{name: "aaa_constructed", closeCount: &closeCount}
		},
	)
	live := &subTurnSelectorTestTool{name: "zzz_factory_failure"}
	factory, err := tools.NewToolFactoryFromPrototype(
		live,
		tools.ToolTraits{},
		func(tools.ToolBuildContext) (tools.Tool, error) {
			return nil, errors.New("injected child factory failure")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if registerErr := agent.Tools.RegisterFactoryBacked(live, factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	parent := &turnState{
		turnID: "factory-parent", agent: agent,
		pendingResults: make(chan *tools.ToolResult, 1),
		concurrencySem: make(chan struct{}, 1),
	}
	result, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, parent, SubTurnConfig{
		Model: "test-model", SystemPrompt: "must not start",
	})
	if result != nil || err == nil || !strings.Contains(err.Error(), "injected child factory failure") {
		t.Fatalf("factory failure result = %#v, %v", result, err)
	}
	if provider.calls.Load() != 0 || closeCount.Load() != 1 || len(parent.childTurnIDs) != 0 {
		t.Fatalf(
			"factory failure effects = calls:%d closes:%d children:%v",
			provider.calls.Load(),
			closeCount.Load(),
			parent.childTurnIDs,
		)
	}
}

func TestSpawnSubTurnAttachRejectionClosesConstructedProducts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &subTurnCountingProvider{}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	defer func() {
		loop.Close()
		messageBus.Close()
	}()
	agent := loop.GetRegistry().GetDefaultAgent()
	constructionEntered := make(chan struct{})
	releaseConstruction := make(chan struct{})
	var closeCount atomic.Int64
	live := &subTurnSelectorTestTool{name: "attach_rejection_probe"}
	factory, err := tools.NewToolFactoryFromPrototype(
		live,
		tools.ToolTraits{},
		func(tools.ToolBuildContext) (tools.Tool, error) {
			close(constructionEntered)
			<-releaseConstruction
			return &subTurnSelectorTestTool{
				name: "attach_rejection_probe", closeCount: &closeCount,
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if registerErr := agent.Tools.RegisterFactoryBacked(live, factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	parent := &turnState{
		turnID: "attach-rejection-parent", agent: agent,
		pendingResults: make(chan *tools.ToolResult, 1),
		concurrencySem: make(chan struct{}, 1),
	}
	type spawnOutcome struct {
		result *tools.ToolResult
		err    error
	}
	done := make(chan spawnOutcome, 1)
	go func() {
		result, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, parent, SubTurnConfig{
			Model: "test-model", SystemPrompt: "reject attach",
			Tools: []tools.Tool{&subTurnSelectorTestTool{name: "attach_rejection_probe"}},
		})
		done <- spawnOutcome{result: result, err: err}
	}()
	select {
	case <-constructionEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("attach-rejection construction did not start")
	}
	parent.mu.Lock()
	parent.cancelRequested = true
	parent.mu.Unlock()
	close(releaseConstruction)
	select {
	case outcome := <-done:
		if outcome.result != nil || outcome.err == nil ||
			!strings.Contains(outcome.err.Error(), "no longer accepts children") {
			t.Fatalf("attach-rejection outcome = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("attach-rejected child did not finish")
	}
	if closeCount.Load() != 1 || provider.calls.Load() != 0 || len(parent.childTurnIDs) != 0 {
		t.Fatalf(
			"attach-rejection effects = closes:%d calls:%d children:%v",
			closeCount.Load(),
			provider.calls.Load(),
			parent.childTurnIDs,
		)
	}
}

func TestSpawnSubTurnStrictRegistryClosesOnlyAfterBlockingToolUseQuiesces(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	provider := &toolCallRespProvider{
		toolName: "blocking_owned_tool", toolArgs: map[string]any{}, response: "unused",
	}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	defer func() {
		loop.Close()
		messageBus.Close()
	}()
	agent := loop.GetRegistry().GetDefaultAgent()
	executeEntered := make(chan struct{})
	releaseExecute := make(chan struct{})
	var closeCount atomic.Int64
	registerSubTurnSelectorFactory(t, agent.Tools, "blocking_owned_tool", func() *subTurnSelectorTestTool {
		return &subTurnSelectorTestTool{
			name: "blocking_owned_tool", closeCount: &closeCount,
			executeEntered: executeEntered, releaseExecute: releaseExecute,
		}
	})
	parent := &turnState{
		turnID: "blocking-use-parent", agent: agent,
		pendingResults: make(chan *tools.ToolResult, 1),
		concurrencySem: make(chan struct{}, 1),
	}
	type spawnOutcome struct {
		result *tools.ToolResult
		err    error
	}
	done := make(chan spawnOutcome, 1)
	go func() {
		result, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, parent, SubTurnConfig{
			Model: "test-model", SystemPrompt: "execute blocking tool",
			Tools: []tools.Tool{&subTurnSelectorTestTool{name: "blocking_owned_tool"}},
		})
		done <- spawnOutcome{result: result, err: err}
	}()
	select {
	case <-executeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("owned tool execution did not start")
	}
	parent.mu.RLock()
	childIDs := append([]string(nil), parent.childTurnIDs...)
	parent.mu.RUnlock()
	if len(childIDs) != 1 {
		t.Fatalf("blocking-use children = %v", childIDs)
	}
	if err := loop.HardAbort(childIDs[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-done:
		t.Fatalf("turn finished before tool use quiesced: %#v, %v", outcome.result, outcome.err)
	case <-time.After(50 * time.Millisecond):
	}
	if closeCount.Load() != 0 {
		t.Fatalf("strict registry closed beneath tool use: %d", closeCount.Load())
	}
	close(releaseExecute)
	select {
	case outcome := <-done:
		if outcome.err == nil || outcome.result == nil || !outcome.result.IsError {
			t.Fatalf("blocking-use outcome = %#v, %v", outcome.result, outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocking tool turn did not finish")
	}
	if closeCount.Load() != 1 {
		t.Fatalf("blocking-use close count = %d", closeCount.Load())
	}
}

func TestTurnStateOwnedResourcesCloseRegistryBeforeSessionExactlyOnce(t *testing.T) {
	order := []string{}
	registry := tools.NewToolRegistry()
	live := &subTurnSelectorTestTool{name: "close_order"}
	factory, err := tools.NewToolFactoryFromPrototype(
		live,
		tools.ToolTraits{},
		func(tools.ToolBuildContext) (tools.Tool, error) {
			return &subTurnCloseOrderTool{
				subTurnSelectorTestTool: subTurnSelectorTestTool{name: "close_order"},
				order:                   &order,
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if registerErr := registry.RegisterFactoryBacked(live, factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	defer registry.Close()
	owned, err := registry.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "close-order-owner"},
		[]string{"close_order"},
	)
	if err != nil {
		t.Fatal(err)
	}
	state := &turnState{
		turnTools: owned,
		turnSession: &subTurnCloseOrderSession{
			ephemeralSessionStore: &ephemeralSessionStore{}, order: &order,
		},
	}
	if closeErr, first := state.closeOwnedTurnResources(); closeErr != nil || !first {
		t.Fatalf("first resource close = first:%t error:%v", first, closeErr)
	}
	if !slices.Equal(order, []string{"tool", "session"}) {
		t.Fatalf("resource close order = %v", order)
	}
	if closeErr, first := state.closeOwnedTurnResources(); closeErr != nil || first {
		t.Fatalf("second resource close = first:%t error:%v", first, closeErr)
	}
	if !slices.Equal(order, []string{"tool", "session"}) {
		t.Fatalf("resource close repeated = %v", order)
	}
}

func TestTurnStateConstructionDrainTimeoutDefersCloseUntilLastLeaseRelease(t *testing.T) {
	order := []string{}
	registry := tools.NewToolRegistry()
	live := &subTurnSelectorTestTool{name: "deferred_close"}
	factory, err := tools.NewToolFactoryFromPrototype(
		live,
		tools.ToolTraits{Sharing: tools.ToolSharingPerOwner},
		func(tools.ToolBuildContext) (tools.Tool, error) {
			return &subTurnCloseOrderTool{
				subTurnSelectorTestTool: subTurnSelectorTestTool{name: "deferred_close"},
				order:                   &order,
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if registerErr := registry.RegisterFactoryBacked(live, factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	defer registry.Close()
	owned, err := registry.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "deferred-close-owner"},
		[]string{"deferred_close"},
	)
	if err != nil {
		t.Fatal(err)
	}
	state := &turnState{
		turnTools: owned,
		turnSession: &subTurnCloseOrderSession{
			ephemeralSessionStore: &ephemeralSessionStore{}, order: &order,
		},
	}
	first, err := state.retainSubTurnConstruction()
	if err != nil || !first.consumeFor(state) {
		t.Fatalf("first construction lease = %#v, %v", first, err)
	}
	second, err := state.retainSubTurnConstruction()
	if err != nil || !second.consumeFor(state) {
		t.Fatalf("second construction lease = %#v, %v", second, err)
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := state.waitForSubTurnConstructions(drainCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded construction drain error = %v", err)
	}
	if closeErr, initiated := state.closeOwnedTurnResources(); closeErr != nil || initiated {
		t.Fatalf("pending close = initiated:%t error:%v", initiated, closeErr)
	}
	if closeErr, initiated := state.closeOwnedTurnResources(); closeErr != nil || initiated {
		t.Fatalf("outer pending close = initiated:%t error:%v", initiated, closeErr)
	}
	if len(order) != 0 {
		t.Fatalf("resources closed beneath construction: %v", order)
	}
	first.release()
	if len(order) != 0 {
		t.Fatalf("first of two releases closed resources: %v", order)
	}
	second.release()
	if !slices.Equal(order, []string{"tool", "session"}) {
		t.Fatalf("last-release close order = %v", order)
	}
	if closeErr, initiated := state.closeOwnedTurnResources(); closeErr != nil || initiated {
		t.Fatalf("post-deferred close = initiated:%t error:%v", initiated, closeErr)
	}
}

type subTurnCloseOrderTool struct {
	subTurnSelectorTestTool
	order *[]string
}

func (tool *subTurnCloseOrderTool) Close() error {
	*tool.order = append(*tool.order, "tool")
	return nil
}

func TestSubTurnConstructionLeaseAllowsPreAdmittedAttachmentAndQuiesces(t *testing.T) {
	loop := &AgentLoop{}
	parent := &turnState{turnID: "lease-parent", runAdmitted: true}
	lease, err := parent.retainSubTurnConstruction()
	if err != nil {
		t.Fatal(err)
	}
	if !lease.consumeFor(parent) {
		t.Fatal("construction lease was not consumed")
	}
	if lease.consumeFor(parent) {
		t.Fatal("one prepared construction lease was consumed twice")
	}
	secondLease, err := parent.retainSubTurnConstruction()
	if err != nil || !secondLease.consumeFor(parent) {
		t.Fatalf("second counted construction lease = %#v, %v", secondLease, err)
	}
	if !parent.claimRunTerminalOwnership() {
		t.Fatal("terminal ownership was not claimed")
	}
	if _, err := parent.retainSubTurnConstruction(); err == nil {
		t.Fatal("terminal parent admitted a new construction")
	}
	child := &turnState{
		turnID: "lease-child", sessionKey: "lease-child",
		parentTurnID: parent.turnID, parentTurnState: parent,
	}
	if !lease.reserveAttachmentFor(parent, child) {
		t.Fatal("pre-admitted child attachment was not reserved")
	}
	if !loop.attachChildTurnWithLease(parent, child, lease) {
		t.Fatal("pre-admitted child was rejected during terminal claim")
	}
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- parent.waitForSubTurnConstructions(context.Background())
	}()
	select {
	case err := <-waitDone:
		t.Fatalf("construction drain returned before release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	lease.release()
	select {
	case err := <-waitDone:
		t.Fatalf("construction drain ignored second lease: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	secondLease.release()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("construction drain did not finish")
	}
}

func TestSubTurnConcurrencyLeaseIsOneShotAndPrecedesConstructionAdmission(t *testing.T) {
	parent := &turnState{
		turnID:         "concurrency-lease-parent",
		concurrencySem: make(chan struct{}, 1),
	}
	runtimeCfg := subTurnRuntimeConfig{
		maxConcurrent: 1, concurrencyTimeout: time.Second,
	}
	lease, err := acquireSubTurnConcurrencyLease(context.Background(), parent, runtimeCfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(parent.concurrencySem) != 1 || !lease.consumeFor(parent) || lease.consumeFor(parent) {
		t.Fatalf("prepared concurrency lease = slots:%d state:%d",
			len(parent.concurrencySem), lease.state.Load())
	}
	lease.release()
	lease.release()
	if len(parent.concurrencySem) != 0 {
		t.Fatalf("idempotent concurrency release left %d slots", len(parent.concurrencySem))
	}
	canceledCtx, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	if canceled, canceledErr := acquireSubTurnConcurrencyLease(
		canceledCtx,
		parent,
		runtimeCfg,
	); canceled != nil || !errors.Is(canceledErr, context.Canceled) ||
		len(parent.concurrencySem) != 0 {
		t.Fatalf("canceled concurrency admission = %#v, %v, slots:%d",
			canceled, canceledErr, len(parent.concurrencySem))
	}

	parent.concurrencySem <- struct{}{}
	shortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	blocked, err := acquireSubTurnConcurrencyLease(shortCtx, parent, runtimeCfg)
	if blocked != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("saturated concurrency admission = %#v, %v", blocked, err)
	}
	parent.mu.RLock()
	constructionUses := parent.subTurnConstructionUses
	parent.mu.RUnlock()
	if constructionUses != 0 {
		t.Fatalf("saturated slot admitted %d construction leases", constructionUses)
	}
	<-parent.concurrencySem
}

func TestPrepareAsyncSubTurnBoundsQueuedSourceAdmissionsWithoutBlockingAck(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.SubTurn.MaxConcurrent = 1
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&simpleMockProvider{response: "unused"},
	)
	defer func() {
		loop.Close()
		messageBus.Close()
	}()
	parent := &turnState{
		turnID: "bounded-async-parent", agent: loop.GetRegistry().GetDefaultAgent(),
		concurrencySem: make(chan struct{}, 1),
	}
	parent.concurrencySem <- struct{}{}
	spawner := NewSubTurnSpawner(loop)
	detachedCtx := withTurnState(WithAgentLoop(context.Background(), loop), parent)
	if _, detachedRelease, detachedErr := spawner.PrepareAsyncSubTurn(detachedCtx); detachedErr == nil {
		detachedRelease()
		t.Fatal("detached async preparation retained runtime without a live origin")
	} else if detachedRelease == nil {
		t.Fatal("detached async preparation returned a nil release function")
	}

	rootCtx, releaseRoot, err := loop.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatalf("acquire trusted runtime root: %v", err)
	}
	defer releaseRoot()
	ctx := withTurnState(WithAgentLoop(rootCtx, loop), parent)
	preparedCtx, releasePrepared, err := spawner.PrepareAsyncSubTurn(ctx)
	if err != nil || preparedCtx == nil {
		t.Fatalf("first queued async preparation = %#v, %v", preparedCtx, err)
	}
	parent.mu.RLock()
	uses := parent.subTurnConstructionUses
	parent.mu.RUnlock()
	if uses != 1 {
		t.Fatalf("first queued source uses = %d", uses)
	}
	secondCtx, secondRelease, secondErr := spawner.PrepareAsyncSubTurn(ctx)
	if secondCtx != ctx || secondRelease == nil ||
		!errors.Is(secondErr, ErrConcurrencyTimeout) {
		if secondRelease != nil {
			secondRelease()
		}
		t.Fatalf("second queued async preparation = %#v, %v", secondCtx, secondErr)
	}
	parent.mu.RLock()
	uses = parent.subTurnConstructionUses
	parent.mu.RUnlock()
	if uses != 1 {
		t.Fatalf("rejected queue widened source uses = %d", uses)
	}
	queuedCtx, cancelQueued := context.WithTimeout(preparedCtx, 10*time.Millisecond)
	defer cancelQueued()
	queuedResult, queuedErr := spawner.SpawnSubTurn(queuedCtx, tools.SubTurnConfig{
		Model: "test-model", SystemPrompt: "must time out before construction",
		Async: true, Critical: true,
	})
	if queuedResult != nil || !errors.Is(queuedErr, context.DeadlineExceeded) {
		t.Fatalf("saturated queued spawn = %#v, %v", queuedResult, queuedErr)
	}
	parent.mu.RLock()
	uses = parent.subTurnConstructionUses
	parent.mu.RUnlock()
	if uses != 0 {
		t.Fatalf("slot failure retained queued source through callback: %d", uses)
	}
	releasePrepared()
	parent.mu.RLock()
	uses = parent.subTurnConstructionUses
	parent.mu.RUnlock()
	if uses != 0 {
		t.Fatalf("queued source release uses = %d", uses)
	}
	<-parent.concurrencySem
}

func TestSpawnToolAsyncConstructionLeaseProtectsStrictParentSource(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&simpleMockProvider{response: "child done"},
	)
	defer func() {
		loop.Close()
		messageBus.Close()
	}()
	rootAgent := loop.GetRegistry().GetDefaultAgent()
	constructionEntered := make(chan struct{})
	releaseConstruction := make(chan struct{})
	var constructions atomic.Int64
	live := &subTurnSelectorTestTool{name: "aaa_blocking_child_factory"}
	factory, err := tools.NewToolFactoryFromPrototype(
		live,
		tools.ToolTraits{},
		func(tools.ToolBuildContext) (tools.Tool, error) {
			if constructions.Add(1) == 2 {
				close(constructionEntered)
				<-releaseConstruction
			}
			return &subTurnSelectorTestTool{name: "aaa_blocking_child_factory"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if registerErr := rootAgent.Tools.RegisterFactoryBacked(live, factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	parentTools, err := rootAgent.Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "strict-async-parent"},
		[]string{"aaa_blocking_child_factory", "spawn"},
	)
	if err != nil {
		t.Fatal(err)
	}
	parentAgent := *rootAgent
	parentAgent.Tools = parentTools
	parentSession := newEphemeralSession(nil)
	parentAgent.Sessions = parentSession
	parent := &turnState{
		agent: &parentAgent, turnID: "strict-async-parent",
		sessionKey: "strict-async-parent", session: parentSession,
		pendingResults: make(chan *tools.ToolResult, 2),
		concurrencySem: make(chan struct{}, 1),
		runAdmitted:    true, toolAuthorityBound: true,
		turnTools: parentTools, turnSession: parentSession,
	}
	loop.prepareTurnState(parent)
	spawnRaw, ok := parentTools.GetRegistered("spawn")
	if !ok {
		t.Fatal("strict parent spawn is unavailable")
	}
	spawn, ok := spawnRaw.(tools.AsyncExecutor)
	if !ok {
		t.Fatalf("strict spawn type = %T", spawnRaw)
	}
	callbackDone := make(chan *tools.ToolResult, 1)
	rootCtx, releaseRoot, err := loop.acquireTrustedRuntimeRoot(context.Background())
	if err != nil {
		t.Fatalf("acquire trusted runtime root: %v", err)
	}
	defer releaseRoot()
	parentCtx := withTurnState(WithAgentLoop(rootCtx, loop), parent)
	ack := spawn.ExecuteAsync(
		parentCtx,
		map[string]any{"task": "block during child construction"},
		func(_ context.Context, result *tools.ToolResult) { callbackDone <- result },
	)
	if ack == nil || ack.IsError || !ack.Async {
		t.Fatalf("spawn acknowledgement = %#v", ack)
	}
	select {
	case <-constructionEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("child factory did not block")
	}
	if !parent.claimRunTerminalOwnership() {
		t.Fatal("strict parent terminal claim failed")
	}
	drainDone := make(chan error, 1)
	go func() {
		drainDone <- parent.waitForSubTurnConstructions(context.Background())
	}()
	select {
	case err := <-drainDone:
		t.Fatalf("terminal drain returned under active child construction: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseConstruction)
	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal drain did not observe child construction release")
	}
	if len(parent.childTurnIDs) != 1 {
		t.Fatalf("pre-admitted async children = %v", parent.childTurnIDs)
	}
	if closeErr, first := parent.closeOwnedTurnResources(); closeErr != nil || !first {
		t.Fatalf("strict parent close = first:%t error:%v", first, closeErr)
	}
	if status, committed := parent.commitClaimedRunTerminal(
		TurnEndStatusCompleted,
		false,
	); !committed || status != TurnEndStatusCompleted {
		t.Fatalf("strict parent terminal = %q, %t", status, committed)
	}
	select {
	case result := <-callbackDone:
		if result == nil || result.IsError {
			t.Fatalf("async child result = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("async child did not finish")
	}
}

func TestSubTurnNativeSearchProjectionIsProviderSpecificAndPseudoIsNotPhysical(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.PreferNative = true
	ts := &turnState{
		toolAuthorityBound:  true,
		nativeSearchAllowed: true,
		profile: config.EffectiveTurnProfile{
			Enabled:      true,
			ToolsMode:    config.TurnProfileModeCustom,
			AllowedTools: []string{"read_file", "web_search"},
		},
	}
	definitions := []providers.ToolDefinition{
		{Type: "function", Function: providers.ToolFunctionDefinition{Name: "read_file"}},
		{Type: "function", Function: providers.ToolFunctionDefinition{Name: "web_search"}},
	}
	nativeOptions := map[string]any{}
	nativeDefs, native := projectNativeSearchForProvider(
		cfg,
		ts,
		&nativeSearchProvider{supported: true},
		true,
		definitions,
		nativeOptions,
	)
	if !native || nativeOptions["native_search"] != true ||
		len(nativeDefs) != 1 || nativeDefs[0].Function.Name != "read_file" {
		t.Fatalf("native projection = defs:%#v opts:%#v native:%t", nativeDefs, nativeOptions, native)
	}

	clientOptions := map[string]any{"native_search": true}
	clientDefs, native := projectNativeSearchForProvider(
		cfg,
		ts,
		&nativeSearchProvider{supported: false},
		true,
		definitions,
		clientOptions,
	)
	if native || clientOptions["native_search"] != nil ||
		len(clientDefs) != 2 || clientDefs[1].Function.Name != "web_search" {
		t.Fatalf("client projection = defs:%#v opts:%#v native:%t", clientDefs, clientOptions, native)
	}

	emptyRegistry, err := tools.NewOwnedToolRegistry(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeTurn, TurnID: "pseudo-filter-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer emptyRegistry.Close()
	ts.agent = &AgentInstance{Tools: emptyRegistry}
	if filtered := filterSubTurnHookDefinitionsToPhysicalTools(
		ts,
		[]providers.ToolDefinition{{
			Type: "function", Function: providers.ToolFunctionDefinition{Name: "web_search"},
		}},
	); len(filtered) != 0 {
		t.Fatalf("pseudo capability admitted fabricated function definition: %#v", filtered)
	}
}

func TestSubTurnNativeSearchFallbackRetainsClientToolForUnsupportedProvider(t *testing.T) {
	primary := &subTurnNativeFallbackProvider{supported: true, fail: true}
	fallback := &subTurnNativeFallbackProvider{supported: false}
	loop, agent, cleanup := newTurnCoordTestLoop(t, primary)
	defer cleanup()
	loop.cfg.Tools.Web.Enabled = true
	loop.cfg.Tools.Web.PreferNative = true
	accountRef := agent.AccountRef
	primaryCandidate := providers.FallbackCandidate{
		Provider: "openai", Model: "native-primary", DisplayName: "native-primary",
		IdentityKey: accountAliasIdentityKey(accountRef, "native-primary"),
	}
	fallbackCandidate := providers.FallbackCandidate{
		Provider: "openai", Model: "client-fallback", DisplayName: "client-fallback",
		IdentityKey: accountAliasIdentityKey(accountRef, "client-fallback"),
	}
	loop.cfg.ModelAliases = append(
		loop.cfg.ModelAliases,
		config.ModelAliasConfig{Name: "native-primary", Model: "native-primary"},
		config.ModelAliasConfig{Name: "client-fallback", Model: "client-fallback"},
	)
	agent.Model = "native-primary"
	agent.Candidates = []providers.FallbackCandidate{primaryCandidate, fallbackCandidate}
	bindBootstrapProvider(agent.CandidateProviders, primaryCandidate, primary)
	bindBootstrapProvider(agent.CandidateProviders, fallbackCandidate, fallback)
	agent.Provider = primary
	if err := agent.Tools.Close(); err != nil {
		t.Fatal(err)
	}
	agent.Tools = tools.NewToolRegistry()
	agent.Tools.Register(adaptationNamedTool("web_search"))
	loop.fallback = providers.NewFallbackChain(providers.NewCooldownTracker(), nil)
	ts := newTurnState(agent, makeTestProcessOpts("native-provider-fallback"), turnEventScope{
		turnID:  "native-provider-fallback",
		context: newTurnContext(nil, nil, nil),
	})
	ts.toolAuthorityBound = true
	ts.nativeSearchAllowed = true
	ts.profile = config.EffectiveTurnProfile{
		Enabled:      true,
		ToolsMode:    config.TurnProfileModeCustom,
		AllowedTools: []string{"web_search"},
	}
	pipeline := NewPipeline(loop)
	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pipeline.CallLLM(context.Background(), context.Background(), ts, exec, 1); err != nil {
		t.Fatal(err)
	}
	primaryNames, primaryOptions := primary.snapshot()
	if len(primaryNames) != 0 || primaryOptions["native_search"] != true {
		t.Fatalf("primary native call = tools:%v options:%#v", primaryNames, primaryOptions)
	}
	fallbackNames, fallbackOptions := fallback.snapshot()
	if !slices.Equal(fallbackNames, []string{"web_search"}) ||
		fallbackOptions["native_search"] != nil {
		t.Fatalf("fallback client call = tools:%v options:%#v", fallbackNames, fallbackOptions)
	}
}

func TestSubTurnBeforeLLMHookCanNarrowNativeSearch(t *testing.T) {
	for _, mode := range []string{"delete", "false", "nil"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			cfg.Tools.Web.Enabled = true
			cfg.Tools.Web.PreferNative = true
			provider := &nativeSearchCaptureProvider{}
			messageBus := bus.NewMessageBus()
			loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
			defer func() {
				loop.Close()
				messageBus.Close()
			}()
			agent := loop.GetRegistry().GetDefaultAgent()
			if !agent.Tools.HasRegistered("web_search") {
				registerSubTurnSelectorFactory(t, agent.Tools, "web_search", nil)
			}
			if err := loop.MountHook(NamedHook(
				"disable-native-"+mode,
				subTurnDisableNativeHook{mode: mode},
			)); err != nil {
				t.Fatal(err)
			}
			parent := &turnState{
				turnID: "native-hook-parent", agent: agent,
				pendingResults: make(chan *tools.ToolResult, 1),
				concurrencySem: make(chan struct{}, 1),
			}
			result, err := spawnSubTurnFromTrustedRuntime(context.Background(), loop, parent, SubTurnConfig{
				Model: "test-model", SystemPrompt: "hook narrows native",
				Tools: []tools.Tool{&subTurnSelectorTestTool{name: "web_search"}},
			})
			if err != nil || result == nil || result.IsError {
				t.Fatalf("hook-narrowed child = %#v, %v", result, err)
			}
			if provider.lastOpts["native_search"] != nil ||
				len(provider.tools) != 1 || provider.tools[0].Function.Name != "web_search" {
				t.Fatalf(
					"hook-narrowed call = tools:%#v options:%#v",
					provider.tools,
					provider.lastOpts,
				)
			}
		})
	}
}

func TestSubTurnPseudoNativeHookModelRewriteRequiresSupportUnlessNarrowed(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		action    string
		wantError bool
		wantCalls int64
	}{
		{name: "pure-rewrite-rejected", wantError: true},
		{name: "narrowed-rewrite-allowed", action: "narrow", wantCalls: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			primary := &subTurnNativeFallbackProvider{supported: true}
			unsupported := &subTurnNativeFallbackProvider{supported: false}
			loop, agent, cleanup := newHookTestLoop(t, primary)
			defer cleanup()
			loop.cfg.Tools.Web.Enabled = true
			loop.cfg.Tools.Web.PreferNative = true
			modelCfg, err := concreteAccountModelConfig(
				loop.cfg,
				agent.AccountRef,
				"hook-model",
				agent.Workspace,
			)
			if err != nil {
				t.Fatal(err)
			}
			candidate, ok := candidateFromModelConfig("", modelCfg)
			if !ok {
				t.Fatal("hook-model candidate is unavailable")
			}
			candidate.DisplayName = "hook-model"
			candidate.IdentityKey = accountAliasIdentityKey(agent.AccountRef, "hook-model")
			bindBootstrapProvider(agent.CandidateProviders, candidate, unsupported)
			if closeErr := agent.Tools.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			empty, err := tools.NewOwnedToolRegistry(tools.ToolOwner{
				Scope: tools.ToolOwnerScopeTurn, TurnID: "pseudo-hook-owner",
			})
			if err != nil {
				t.Fatal(err)
			}
			agent.Tools = empty
			if mountErr := loop.MountHook(NamedHook(
				"pseudo-hook-rewrite",
				subTurnNativePolicyHook{model: "hook-model", action: testCase.action},
			)); mountErr != nil {
				t.Fatal(mountErr)
			}
			ts := newTurnState(agent, makeTestProcessOpts("pseudo-hook-rewrite"), turnEventScope{
				turnID: "pseudo-hook-rewrite", context: newTurnContext(nil, nil, nil),
			})
			ts.toolAuthorityBound = true
			ts.nativeSearchAllowed = true
			ts.profile = config.EffectiveTurnProfile{
				Enabled: true, ToolsMode: config.TurnProfileModeCustom,
				AllowedTools: []string{"web_search"},
			}
			pipeline := NewPipeline(loop)
			exec, err := pipeline.SetupTurn(context.Background(), ts)
			if err != nil {
				t.Fatal(err)
			}
			_, callErr := pipeline.CallLLM(
				context.Background(), context.Background(), ts, exec, 1,
			)
			if testCase.wantError {
				if callErr == nil || !strings.Contains(callErr.Error(), "pseudo-only native web search") {
					t.Fatalf("pure rewrite error = %v", callErr)
				}
			} else if callErr != nil {
				t.Fatal(callErr)
			}
			if unsupported.calls.Load() != testCase.wantCalls {
				t.Fatalf("unsupported provider calls = %d, want %d",
					unsupported.calls.Load(), testCase.wantCalls)
			}
			if !testCase.wantError {
				names, options := unsupported.snapshot()
				if len(names) != 0 || options["native_search"] != nil {
					t.Fatalf("narrowed rewrite = tools:%v options:%#v", names, options)
				}
			}
		})
	}
}

func TestSubTurnGracefulTerminalHookCannotReenableNativeSearch(t *testing.T) {
	provider := &subTurnNativeFallbackProvider{supported: true}
	loop, agent, cleanup := newHookTestLoop(t, provider)
	defer cleanup()
	loop.cfg.Tools.Web.Enabled = true
	loop.cfg.Tools.Web.PreferNative = true
	if closeErr := agent.Tools.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	empty, err := tools.NewOwnedToolRegistry(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeTurn, TurnID: "graceful-pseudo-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.Tools = empty
	if mountErr := loop.MountHook(NamedHook(
		"graceful-enable-native",
		subTurnNativePolicyHook{action: "enable"},
	)); mountErr != nil {
		t.Fatal(mountErr)
	}
	ts := newTurnState(agent, makeTestProcessOpts("graceful-pseudo"), turnEventScope{
		turnID: "graceful-pseudo", context: newTurnContext(nil, nil, nil),
	})
	ts.toolAuthorityBound = true
	ts.nativeSearchAllowed = true
	ts.profile = config.EffectiveTurnProfile{
		Enabled: true, ToolsMode: config.TurnProfileModeCustom,
		AllowedTools: []string{"web_search"},
	}
	pipeline := NewPipeline(loop)
	exec, err := pipeline.SetupTurn(context.Background(), ts)
	if err != nil {
		t.Fatal(err)
	}
	if !ts.requestGracefulInterrupt("finish without tools") {
		t.Fatal("graceful terminal request was rejected")
	}
	if _, err := pipeline.CallLLM(
		context.Background(), context.Background(), ts, exec, 1,
	); err != nil {
		t.Fatal(err)
	}
	names, options := provider.snapshot()
	if len(names) != 0 || options["native_search"] != nil {
		t.Fatalf("graceful hook re-enabled search = tools:%v options:%#v", names, options)
	}
}

func TestSelectEffectiveSubTurnToolsRetainsHiddenRootsWithOwnerLocalPromotion(t *testing.T) {
	cfg := config.DefaultConfig()
	registry := tools.NewToolRegistry()
	live := &subTurnSelectorTestTool{name: "hidden_mcp_tool"}
	factory, err := tools.NewToolFactoryFromPrototype(
		live,
		tools.ToolTraits{},
		func(tools.ToolBuildContext) (tools.Tool, error) {
			return &subTurnSelectorTestTool{name: "hidden_mcp_tool"}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if registerErr := registry.RegisterHiddenFactoryBacked(live, factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	defer registry.Close()
	agent := &AgentInstance{
		ID: "main", Model: "model-main", Workspace: t.TempDir(),
		Provider: &simpleMockProvider{response: "done"}, Tools: registry,
	}
	selection, err := selectEffectiveSubTurnTools(
		cfg,
		&turnState{agent: agent},
		agent,
		nil,
		1,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(selection.roots, []string{"hidden_mcp_tool"}) {
		t.Fatalf("hidden selection roots = %v", selection.roots)
	}
	first, err := registry.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "hidden-first"},
		selection.roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := registry.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "hidden-second"},
		selection.roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if len(first.ToProviderDefs()) != 0 || len(second.ToProviderDefs()) != 0 {
		t.Fatal("hidden root was provider-visible before child promotion")
	}
	first.PromoteTools([]string{"hidden_mcp_tool"}, 2)
	if len(first.ToProviderDefs()) != 1 || len(second.ToProviderDefs()) != 0 ||
		len(registry.ToProviderDefs()) != 0 {
		t.Fatalf(
			"owner-local hidden promotion = first:%d second:%d source:%d",
			len(first.ToProviderDefs()),
			len(second.ToProviderDefs()),
			len(registry.ToProviderDefs()),
		)
	}
}

func TestSelectEffectiveSubTurnToolsSiblingProductsKeepIndependentMutableState(t *testing.T) {
	cfg := config.DefaultConfig()
	registry := tools.NewToolRegistry()
	var countersMu sync.Mutex
	counters := make([]*atomic.Int64, 0, 2)
	live := &subTurnSelectorTestTool{name: "sibling_state"}
	factory, err := tools.NewToolFactoryFromPrototype(
		live,
		tools.ToolTraits{},
		func(tools.ToolBuildContext) (tools.Tool, error) {
			counter := &atomic.Int64{}
			countersMu.Lock()
			counters = append(counters, counter)
			countersMu.Unlock()
			return &subTurnSelectorTestTool{name: "sibling_state", executions: counter}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if registerErr := registry.RegisterFactoryBacked(live, factory); registerErr != nil {
		t.Fatal(registerErr)
	}
	defer registry.Close()
	agent := &AgentInstance{
		ID: "main", Model: "model-main", Workspace: t.TempDir(),
		Provider: &simpleMockProvider{response: "done"}, Tools: registry,
	}
	selection, err := selectEffectiveSubTurnTools(
		cfg,
		&turnState{agent: agent},
		agent,
		nil,
		1,
		3,
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "sibling-first"},
		selection.roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := registry.InstantiateForOwnerSelection(
		tools.ToolOwner{Scope: tools.ToolOwnerScopeTurn, TurnID: "sibling-second"},
		selection.roots,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	firstTool, _ := first.GetRegistered("sibling_state")
	secondTool, _ := second.GetRegistered("sibling_state")
	if firstTool == nil || secondTool == nil || firstTool == secondTool {
		t.Fatalf("sibling product identities = %T/%T", firstTool, secondTool)
	}
	if result := first.Execute(context.Background(), "sibling_state", nil); result.IsError {
		t.Fatalf("first sibling execution = %#v", result)
	}
	if len(counters) != 2 || counters[0].Load()+counters[1].Load() != 1 ||
		counters[0].Load() == counters[1].Load() {
		t.Fatalf(
			"sibling execution counters = %d/%d",
			counters[0].Load(),
			counters[1].Load(),
		)
	}
}

func TestSubTurnCoverageExercisesFailClosedSelectorEdges(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Web.Enabled = true
	cfg.Tools.Web.PreferNative = true
	base := newSubTurnSelectionAgent(t, "coverage-base", "parent_only")
	base.Provider = &nativeSearchProvider{supported: true}

	for name, call := range map[string]func() error{
		"missing-parent": func() error {
			_, err := selectEffectiveSubTurnTools(cfg, nil, base, nil, 1, 3)
			return err
		},
		"missing-implementation": func() error {
			_, err := selectEffectiveSubTurnTools(
				cfg,
				&turnState{agent: base},
				nil,
				nil,
				1,
				3,
			)
			return err
		},
		"invalid-profile": func() error {
			_, err := selectEffectiveSubTurnTools(
				cfg,
				&turnState{agent: base, profile: config.EffectiveTurnProfile{
					Enabled: true, HistoryMode: config.TurnProfileModeCustom,
				}},
				base,
				nil,
				1,
				3,
			)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrInvalidSubTurnConfig) {
				t.Fatalf("selector error = %v", err)
			}
		})
	}

	target := newSubTurnSelectionAgent(t, "coverage-target")
	if _, err := selectEffectiveSubTurnTools(
		cfg,
		&turnState{agent: base},
		target,
		[]tools.Tool{subTurnValueSelector("parent_only")},
		1,
		3,
	); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("parent-known target-unavailable selector = %v", err)
	}

	pseudo := newSubTurnSelectionAgent(t, "coverage-pseudo")
	pseudo.Provider = &nativeSearchProvider{supported: true}
	pseudoSelection, err := selectEffectiveSubTurnTools(
		cfg,
		&turnState{agent: pseudo},
		pseudo,
		[]tools.Tool{subTurnValueSelector("web_search")},
		1,
		3,
	)
	if err != nil || !pseudoSelection.nativeSearch || len(pseudoSelection.roots) != 0 {
		t.Fatalf("explicit pseudo-only selector = %#v, %v", pseudoSelection, err)
	}

	if subTurnUsesProvenImplementationProviderSet(nil, SubTurnConfig{}) ||
		subTurnUsesProvenImplementationProviderSet(base, SubTurnConfig{
			Model: base.Model, ModelFallbacks: []string{"too", "many"},
		}) {
		t.Fatal("unproven implementation provider set was accepted")
	}
	candidate := providers.FallbackCandidate{
		Provider: "mock", Model: "native", IdentityKey: "coverage-native",
	}
	pseudo.CandidateProviders = make(map[string]providers.LLMProvider)
	bindBootstrapProvider(
		pseudo.CandidateProviders,
		candidate,
		&nativeSearchProvider{supported: true},
	)
	pseudo.AccountRouter = &accountrouter.Router{Accounts: map[string]accountrouter.Account{
		"native": {Candidates: []providers.FallbackCandidate{candidate}},
	}}
	if !subTurnAgentProvidersSupportNativeSearch(pseudo) ||
		!subTurnCandidatesSupportNativeSearch(pseudo, []providers.FallbackCandidate{candidate}) ||
		subTurnCandidatesSupportNativeSearch(nil, nil) ||
		subTurnRequiresPseudoOnlyNativeSearch(nil) {
		t.Fatal("native provider proof edge classification failed")
	}
	strictDenied := &turnState{
		agent: pseudo, toolAuthorityBound: true, nativeSearchAllowed: false,
		profile: config.EffectiveTurnProfile{
			Enabled: true, ToolsMode: config.TurnProfileModeCustom,
			AllowedTools: []string{"web_search"},
		},
	}
	if subTurnNativeSearchForProvider(
		cfg,
		strictDenied,
		&nativeSearchProvider{supported: true},
	) {
		t.Fatal("strict native-search denial was widened")
	}
}

func TestSubTurnCoverageExercisesLeaseResourceAndSessionEdges(t *testing.T) {
	if lease, err := (*turnState)(nil).retainSubTurnConstruction(); lease != nil || err == nil {
		t.Fatalf("nil construction owner = %#v, %v", lease, err)
	}
	var nilConstruction *subTurnConstructionLease
	nilConstruction.release()
	(&subTurnConstructionLease{owner: &turnState{}}).release()
	var nilConcurrency *subTurnConcurrencyLease
	nilConcurrency.release()
	(&subTurnConcurrencyLease{owner: &turnState{}}).release()
	if err := (&turnState{}).waitForSubTurnConstructions(nil); err != nil ||
		func() bool { var state *turnState; return state.waitForSubTurnConstructions(nil) != nil }() {
		t.Fatalf("empty construction drain error = %v", err)
	}

	owner := &turnState{turnID: "coverage-lease-owner"}
	lease, err := owner.retainSubTurnConstruction()
	if err != nil || !lease.consumeFor(owner) {
		t.Fatalf("coverage construction lease = %#v, %v", lease, err)
	}
	invalidChild := &turnState{}
	if lease.reserveAttachmentFor(owner, invalidChild) {
		t.Fatal("invalid attachment reservation succeeded")
	}
	child := &turnState{turnID: "coverage-child-turn", sessionKey: "coverage-child"}
	if !lease.reserveAttachmentFor(owner, child) ||
		!lease.reserveAttachmentFor(owner, child) ||
		lease.reserveAttachmentFor(owner, &turnState{
			turnID: "other-turn", sessionKey: "other-child",
		}) {
		t.Fatal("exact attachment reservation was not stable")
	}
	if !lease.attachmentAdmittedFor(owner, child) ||
		lease.attachmentAdmittedFor(owner, child) ||
		lease.attachmentAdmittedFor(nil, nil) {
		t.Fatal("attachment reservation was not one-shot")
	}
	lease.release()

	deferredCloseErr := errors.New("deferred close coverage")
	pending := &turnState{
		agentID: "coverage", turnID: "coverage-pending",
		turnResourcesState:      turnResourcesPending,
		subTurnConstructionUses: 1,
		turnSession: &agentInstanceCloseSessionStore{
			closeErr: deferredCloseErr,
		},
	}
	pendingLease := &subTurnConstructionLease{owner: pending}
	pendingLease.state.Store(subTurnConstructionLeaseConsumed)
	pendingLease.release()
	if pending.turnResourcesState != turnResourcesClosed ||
		!errors.Is(pending.turnResourcesErr, deferredCloseErr) {
		t.Fatalf("deferred close state = %d, %v",
			pending.turnResourcesState, pending.turnResourcesErr)
	}

	if closeErr, initiated := (*turnState)(nil).closeOwnedTurnResources(); closeErr != nil || initiated {
		t.Fatalf("nil resource close = %t, %v", initiated, closeErr)
	}
	closing := &turnState{turnResourcesState: turnResourcesClosing}
	if closeErr, initiated := closing.closeOwnedTurnResources(); closeErr != nil || initiated {
		t.Fatalf("concurrent resource close = %t, %v", initiated, closeErr)
	}
	invalid := &turnState{turnResourcesState: turnResourceCloseState(255)}
	if closeErr, initiated := invalid.closeOwnedTurnResources(); closeErr == nil || initiated {
		t.Fatalf("invalid resource close = %t, %v", initiated, closeErr)
	}

	var nilState *turnState
	if nilState.acceptsPreAdmittedSubTurnConstruction(nil) ||
		nilState.nativeSearchAuthoritySnapshot() != (subTurnNativeAuthoritySnapshot{}) {
		t.Fatal("nil turn authority was accepted")
	}
	nilState.recordNativeSearchObservation(true)
	strict := &turnState{toolAuthorityBound: true}
	strict.recordNativeSearchObservation(true)
	if strict.nativeSearchObserved || strict.nativeSearchAllowed {
		t.Fatal("strict child native authority was mutated")
	}
	if got := turnReservationFromContext(nil); got != nil {
		t.Fatalf("nil reservation context = %#v", got)
	}
	if got := withTurnReservation(nil, nil); got == nil {
		t.Fatal("nil reservation context was not normalized")
	}
	if got := subTurnConstructionLeaseFromContext(nil); got != nil {
		t.Fatalf("nil construction context = %#v", got)
	}
	if got := withSubTurnConstructionLease(nil, nil); got == nil {
		t.Fatal("nil construction context was not normalized")
	}
	(&AgentLoop{}).releaseSessionTurnState("", nil)
	normalized := normalizeMessageForComparison(providers.Message{
		SystemParts: []providers.ContentBlock{{
			PromptLayer: "layer", PromptSlot: "slot", PromptSource: "source",
		}},
	})
	if len(normalized.SystemParts) != 1 || normalized.SystemParts[0].PromptLayer != "" ||
		normalized.SystemParts[0].PromptSlot != "" ||
		normalized.SystemParts[0].PromptSource != "" {
		t.Fatalf("normalized system prompt metadata = %#v", normalized.SystemParts)
	}
	terminal := &turnState{terminalClaimed: true}
	prepared := &subTurnConstructionLease{owner: terminal}
	prepared.state.Store(subTurnConstructionLeasePrepared)
	if !terminal.acceptsPreAdmittedSubTurnConstruction(prepared) {
		t.Fatal("exact pre-admitted construction was rejected")
	}
	terminal.cancelDispatched = true
	if terminal.acceptsPreAdmittedSubTurnConstruction(prepared) {
		t.Fatal("failure-fenced construction was accepted")
	}

	sessionStore := newEphemeralSession([]providers.Message{{Role: "user", Content: "one"}})
	sessionStore.AddMessage("", "assistant", "two")
	sessionStore.TruncateHistory("", 99)
	sessionStore.TruncateHistory("", 1)
	if history := sessionStore.GetHistory(""); len(history) != 1 || history[0].Content != "two" {
		t.Fatalf("partial ephemeral truncation = %#v", history)
	}
	sessionStore.TruncateHistory("", 0)
	if len(sessionStore.GetHistory("")) != 0 || sessionStore.ListSessions() != nil {
		t.Fatal("ephemeral session clear/list edge failed")
	}
}

func TestSubTurnCoverageExercisesConcurrencyPromptAndRouterEdges(t *testing.T) {
	if lease, err := acquireSubTurnConcurrencyLease(
		context.Background(), nil, subTurnRuntimeConfig{},
	); lease != nil || err == nil {
		t.Fatalf("nil-parent concurrency lease = %#v, %v", lease, err)
	}
	owner := &turnState{turnID: "coverage-concurrency"}
	lease, err := acquireSubTurnConcurrencyLease(nil, owner, subTurnRuntimeConfig{})
	if err != nil || lease == nil || lease.slot != nil {
		t.Fatalf("slotless concurrency lease = %#v, %v", lease, err)
	}
	lease.release()
	finished := &turnState{
		turnID: "coverage-finished", concurrencySem: make(chan struct{}, 1),
	}
	finished.concurrencySem <- struct{}{}
	close(finished.Finished())
	if lease, err := acquireSubTurnConcurrencyLease(
		context.Background(),
		finished,
		subTurnRuntimeConfig{maxConcurrent: 1, concurrencyTimeout: time.Second},
	); lease != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("finished-parent concurrency lease = %#v, %v", lease, err)
	}
	timed := &turnState{turnID: "coverage-timeout", concurrencySem: make(chan struct{}, 1)}
	timed.concurrencySem <- struct{}{}
	if lease, err := acquireSubTurnConcurrencyLease(
		context.Background(),
		timed,
		subTurnRuntimeConfig{maxConcurrent: 1, concurrencyTimeout: time.Millisecond},
	); lease != nil || !errors.Is(err, ErrConcurrencyTimeout) {
		t.Fatalf("internal concurrency timeout = %#v, %v", lease, err)
	}

	if _, _, err := (&AgentLoopSpawner{}).PrepareAsyncSubTurn(context.Background()); err == nil {
		t.Fatal("nil loop async preparation succeeded")
	}
	cfg := config.DefaultConfig()
	pipeline := &Pipeline{Cfg: cfg}
	candidate := providers.FallbackCandidate{
		Provider: "mock", Model: "compressed", IdentityKey: "compressed-candidate",
	}
	provider := &nativeSearchProvider{supported: true}
	agent := &AgentInstance{
		ID: "coverage-router", Model: "compressed", Workspace: t.TempDir(),
		Provider: provider, CandidateProviders: map[string]providers.LLMProvider{},
	}
	bindBootstrapProvider(agent.CandidateProviders, candidate, provider)
	routerConfig := config.AccountRouterConfig{
		Name: "coverage-router", Enabled: true, Entry: "account",
		Blocks: []config.AccountRouterBlock{{
			ID: "account", Type: config.AccountRouterBlockTypeAccount, Account: "one",
		}},
	}
	router := accountrouter.NewForWorkspace(
		routerConfig.Name,
		&routerConfig,
		map[string]accountrouter.Account{
			"one": {Candidates: []providers.FallbackCandidate{candidate}},
		},
		t.TempDir(),
	)
	ts := &turnState{agent: agent, sessionKey: "coverage-compression"}
	exec := &turnExecution{accountRouter: router}
	pipeline.reselectAccountRouterAfterCompression(ts, exec)
	if len(exec.activeCandidates) != 1 || exec.activeModel != "compressed" ||
		exec.activeProvider != provider {
		t.Fatalf("compressed router selection = %#v, %q, %T",
			exec.activeCandidates, exec.activeModel, exec.activeProvider)
	}

	registry := tools.NewToolRegistry()
	registerSubTurnSelectorFactory(t, registry, "registered", nil)
	defer registry.Close()
	promptTS := &turnState{
		depth: 1,
		agent: &AgentInstance{
			Tools: registry, Provider: &nativeSearchProvider{supported: false},
		},
	}
	promptReq := PromptBuildRequest{AllowedTools: []string{"missing"}}
	restrictChildPromptToRegisteredTools(promptTS, &promptReq, cfg)
	if !promptReq.SuppressToolUseRule || len(promptReq.AllowedTools) != 0 {
		t.Fatalf("empty prompt intersection = %#v", promptReq)
	}
	if message := subTurnResultPromptMessage("coverage result"); message.Role != "user" || message.Content == "" {
		t.Fatalf("subturn result prompt = %#v", message)
	}
}

var _ providers.LLMProvider = (*nativeSearchProvider)(nil)
