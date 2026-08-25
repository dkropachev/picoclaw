package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type baseCatalogReactionChannel struct {
	fakeChannel
	calls atomic.Int64
}

func (channel *baseCatalogReactionChannel) ReactToMessage(
	context.Context,
	string,
	string,
) (func(), error) {
	channel.calls.Add(1)
	return func() {}, nil
}

type baseCatalogChannelManager struct {
	recordingChannelManager
	reaction channels.Channel
}

func (manager *baseCatalogChannelManager) GetChannel(name string) (channels.Channel, bool) {
	if name != "catalog" || manager.reaction == nil {
		return nil, false
	}
	return manager.reaction, true
}

type baseCatalogGitManager struct {
	controllerGitWorkspaceManagerFake
	statsCalls atomic.Int64
}

func (manager *baseCatalogGitManager) Stats(context.Context) (gitworkspace.Stats, error) {
	manager.statsCalls.Add(1)
	return gitworkspace.Stats{}, nil
}

var expectedBaseFactoryCatalog = []string{
	"append_file",
	"apply_patch",
	"edit_file",
	"find_skills",
	"git_workspace",
	"i2c",
	"list_dir",
	"load_image",
	"message",
	"reaction",
	"read_file",
	"send_file",
	"send_tts",
	"serial",
	"spi",
	"update_plan",
	"view_image",
	"web_fetch",
	"web_search",
	"write_file",
}

var expectedBaseFactoryTraits = map[string]tools.ToolTraits{
	"read_file": {
		Risk: tools.ToolRiskReadOnly, Parallel: tools.ToolParallelSafe,
		Idempotency: tools.ToolIdempotencyIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"list_dir": {
		Risk: tools.ToolRiskReadOnly, Parallel: tools.ToolParallelSafe,
		Idempotency: tools.ToolIdempotencyIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"update_plan": {
		Risk: tools.ToolRiskMutation, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"edit_file": {
		Risk: tools.ToolRiskMutation, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"append_file": {
		Risk: tools.ToolRiskMutation, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"write_file": {
		Risk: tools.ToolRiskDestructive, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"apply_patch": {
		Risk: tools.ToolRiskDestructive, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"git_workspace": {
		Risk: tools.ToolRiskDestructive, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"web_search": {
		Risk: tools.ToolRiskNetwork, Parallel: tools.ToolParallelSafe,
		Idempotency: tools.ToolIdempotencyUnknown, Sharing: tools.ToolSharingPerOwner,
	},
	"web_fetch": {
		Risk: tools.ToolRiskNetwork, Parallel: tools.ToolParallelSafe,
		Idempotency: tools.ToolIdempotencyUnknown, Sharing: tools.ToolSharingPerOwner,
	},
	"find_skills": {
		Risk: tools.ToolRiskNetwork, Parallel: tools.ToolParallelSafe,
		Idempotency: tools.ToolIdempotencyUnknown, Sharing: tools.ToolSharingPerOwner,
	},
	"i2c": {
		Risk: tools.ToolRiskExternalWrite, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"spi": {
		Risk: tools.ToolRiskExternalWrite, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"serial": {
		Risk: tools.ToolRiskExternalWrite, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"message": {
		Risk: tools.ToolRiskExternalWrite, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"reaction": {
		Risk: tools.ToolRiskExternalWrite, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"send_file": {
		Risk: tools.ToolRiskExternalWrite, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"send_tts": {
		Risk: tools.ToolRiskExternalWrite, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"load_image": {
		Risk: tools.ToolRiskMutation, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
	"view_image": {
		Risk: tools.ToolRiskMutation, Parallel: tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent, Sharing: tools.ToolSharingPerOwner,
	},
}

func newBaseFactoryCatalogTestLoop(t *testing.T) *AgentLoop {
	t.Helper()
	loop, _, _ := newBaseFactoryCatalogTestRuntime(t)
	return loop
}

func newBaseFactoryCatalogTestRuntime(
	t *testing.T,
) (*AgentLoop, *config.Config, *bus.MessageBus) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "gpt-5"
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.I2C.Enabled = true
	cfg.Tools.SPI.Enabled = true
	cfg.Tools.Serial.Enabled = true
	cfg.Tools.SendTTS.Enabled = true
	cfg.Voice.TTSAccountRef = "mimo-account"
	cfg.Voice.TTSModelName = "mimo-tts"
	cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
		Name: "mimo-tts", Model: "mimo/mimo-v2-tts",
	})
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName: "mimo-account",
		Model:     "mimo/mimo-v2-tts",
		APIKeys:   config.SimpleSecureStrings("test-mimo-key"),
		Enabled:   true,
	})

	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, &mockProvider{})
	t.Cleanup(loop.Close)
	return loop, cfg, messageBus
}

func TestBaseToolFactoryCatalogExactPartitionAndTraits(t *testing.T) {
	loop := newBaseFactoryCatalogTestLoop(t)
	agent := loop.GetRegistry().GetDefaultAgent()
	if agent == nil || agent.Tools == nil {
		t.Fatal("default agent tool registry is unavailable")
	}

	expected := make(map[string]struct{}, len(expectedBaseFactoryCatalog))
	for _, name := range expectedBaseFactoryCatalog {
		expected[name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(expected))
	for _, capability := range agent.Tools.InstantiationCapabilities() {
		_, wanted := expected[capability.Name]
		if wanted {
			if !capability.FactoryBacked || capability.ImmutableShared {
				t.Errorf("base capability %q = %#v", capability.Name, capability)
			}
			seen[capability.Name] = struct{}{}
			wantTraits, hasExpectedTraits := expectedBaseFactoryTraits[capability.Name]
			if !hasExpectedTraits {
				t.Fatalf("base capability %q has no independent trait expectation", capability.Name)
			}
			gotTraits, ok := agent.Tools.Traits(capability.Name)
			if !ok || gotTraits != wantTraits {
				t.Errorf("traits %q = %#v, %t; want %#v", capability.Name, gotTraits, ok, wantTraits)
			}
			continue
		}
		if capability.FactoryBacked || capability.ImmutableShared {
			t.Errorf("deferred/root-only capability was classified: %#v", capability)
		}
	}
	if len(seen) != len(expected) {
		var missing []string
		for _, name := range expectedBaseFactoryCatalog {
			if _, ok := seen[name]; !ok {
				missing = append(missing, name)
			}
		}
		t.Fatalf("base factory catalog missing %v; registered=%v", missing, agent.Tools.List())
	}
	if len(expectedBaseFactoryTraits) != len(expectedBaseFactoryCatalog) {
		t.Fatalf(
			"trait matrix has %d entries, want %d",
			len(expectedBaseFactoryTraits),
			len(expectedBaseFactoryCatalog),
		)
	}

	for _, name := range []string{
		"exec", "exec_command", "write_stdin", "threads", "workflow", "install_skill",
		"spawn", "spawn_status", "subagent", "delegate",
	} {
		for _, capability := range agent.Tools.InstantiationCapabilities() {
			if capability.Name == name && (capability.FactoryBacked || capability.ImmutableShared) {
				t.Errorf("root-only/deferred tool %q became constructible", name)
			}
		}
	}
}

func TestBaseToolFactoriesPreserveDescriptorsResultsAndOwnerIdentity(t *testing.T) {
	loop, _, messageBus := newBaseFactoryCatalogTestRuntime(t)
	source := loop.GetRegistry().GetDefaultAgent().Tools
	first, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "factory-first",
	}, append([]string(nil), expectedBaseFactoryCatalog...))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "factory-second",
	}, append([]string(nil), expectedBaseFactoryCatalog...))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	type resultProjection struct {
		ForLLM, ForUser        string
		IsError, Silent, Async bool
		ResponseHandled        bool
		Err                    string
		Media                  []string
		Messages               []providers.Message
		ArtifactTags           []string
	}
	project := func(result *tools.ToolResult) resultProjection {
		if result == nil {
			return resultProjection{}
		}
		projection := resultProjection{
			ForLLM: result.ForLLM, ForUser: result.ForUser,
			IsError: result.IsError, Silent: result.Silent, Async: result.Async,
			ResponseHandled: result.ResponseHandled,
			Media:           append([]string(nil), result.Media...),
			Messages:        append([]providers.Message(nil), result.Messages...),
			ArtifactTags:    append([]string(nil), result.ArtifactTags...),
		}
		if result.Err != nil {
			projection.Err = result.Err.Error()
		}
		return projection
	}

	for _, name := range expectedBaseFactoryCatalog {
		liveSnapshot, liveOK := source.GetCoreToolSnapshot(name)
		firstSnapshot, firstOK := first.GetCoreToolSnapshot(name)
		secondTool, secondOK := second.GetRegistered(name)
		if !liveOK || !firstOK || !secondOK {
			t.Fatalf("tool %q snapshots = live:%t first:%t second:%t", name, liveOK, firstOK, secondOK)
		}
		if reflect.ValueOf(liveSnapshot.Tool).Pointer() == reflect.ValueOf(firstSnapshot.Tool).Pointer() ||
			reflect.ValueOf(firstSnapshot.Tool).Pointer() == reflect.ValueOf(secondTool).Pointer() {
			t.Errorf("tool %q reused a live or sibling pointer", name)
		}
		if !reflect.DeepEqual(liveSnapshot.Descriptor, firstSnapshot.Descriptor) {
			t.Errorf("tool %q descriptor changed across owner construction", name)
		}
		liveResult := project(liveSnapshot.Tool.Execute(context.Background(), map[string]any{}))
		childResult := project(firstSnapshot.Tool.Execute(context.Background(), map[string]any{}))
		if !reflect.DeepEqual(liveResult, childResult) {
			t.Errorf("tool %q invalid-input result changed: live=%#v child=%#v", name, liveResult, childResult)
		}
	}
	if closeErr := first.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	siblingMessage, ok := second.Get("message")
	if !ok {
		t.Fatal("owner-local message tool is unavailable")
	}
	messageResult := siblingMessage.Execute(
		tools.WithToolContext(context.Background(), "test", "borrowed-chat"),
		map[string]any{"content": "borrowed callback remains live"},
	)
	if messageResult == nil || messageResult.IsError {
		t.Fatalf("message after sibling close = %#v", messageResult)
	}
	select {
	case outbound := <-messageBus.OutboundChan():
		if outbound.Content != "borrowed callback remains live" ||
			outbound.ChatID != "borrowed-chat" {
			t.Fatalf("borrowed message callback output = %#v", outbound)
		}
	default:
		t.Fatal("borrowed message callback did not publish after sibling close")
	}
	third, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "factory-after-close",
	}, append([]string(nil), expectedBaseFactoryCatalog...))
	if err != nil {
		t.Fatalf("closing one owner invalidated borrowed services or source factories: %v", err)
	}
	if closeErr := third.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestViewImageOnlyAllowlistKeepsLoadImagePrivateAndMediaAware(t *testing.T) {
	workspace := setupWorkspace(t, map[string]string{
		"AGENT.md": `---
tools: [view_image]
---
# View only
`,
	})
	defer cleanupWorkspace(t, workspace)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.ModelName = "gpt-5"
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.LoadImage.Enabled = true

	loop := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), &mockProvider{})
	defer loop.Close()
	source := loop.GetRegistry().GetDefaultAgent().Tools
	if got := source.List(); !reflect.DeepEqual(got, []string{"view_image"}) {
		t.Fatalf("view-only tools = %v", got)
	}
	capabilities := source.InstantiationCapabilities()
	if len(capabilities) != 1 || capabilities[0].Name != "view_image" ||
		!capabilities[0].FactoryBacked {
		t.Fatalf("view-only capabilities = %#v", capabilities)
	}
	if source.HasRegistered("load_image") || len(source.ToProviderDefs()) != 1 {
		t.Fatal("private load_image escaped the view-only surface")
	}
	if direct, directErr := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "direct-load",
	}, []string{"load_image"}); directErr == nil || direct != nil {
		t.Fatalf("private load_image became selectable: %#v, %v", direct, directErr)
	}

	child, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "view-child",
	}, []string{"view_image"})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	if child.HasRegistered("load_image") || !reflect.DeepEqual(child.List(), []string{"view_image"}) {
		t.Fatalf("child exposed private loader: %v", child.List())
	}

	store := media.NewFileMediaStore()
	loop.SetMediaStore(store)
	child.SetMediaStore(store)
	imagePath := filepath.Join(workspace, "one.png")
	pngHeader := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
		0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE,
	}
	if err := os.WriteFile(imagePath, pngHeader, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := tools.WithToolContext(context.Background(), "test", "view-chat")
	for label, registry := range map[string]*tools.ToolRegistry{"root": source, "child": child} {
		view, ok := registry.Get("view_image")
		if !ok {
			t.Fatalf("%s view_image is unavailable", label)
		}
		result := view.Execute(ctx, map[string]any{"path": imagePath})
		if result == nil || result.IsError || len(result.Media) != 1 ||
			strings.Contains(result.ForLLM, "media store not configured") {
			t.Fatalf("%s view_image result = %#v", label, result)
		}
	}
}

func TestBaseFactoryHardwarePointersAndCompatibilityCloneRemainDistinct(t *testing.T) {
	if tools.NewI2CTool() == tools.NewI2CTool() ||
		tools.NewSPITool() == tools.NewSPITool() ||
		tools.NewSerialTool() == tools.NewSerialTool() {
		t.Fatal("hardware constructors reused a zero-sized pointer identity")
	}

	loop := newBaseFactoryCatalogTestLoop(t)
	source := loop.GetRegistry().GetDefaultAgent().Tools
	clone := source.Clone()
	rootPlan, rootOK := source.GetRegistered("update_plan")
	clonePlan, cloneOK := clone.GetRegistered("update_plan")
	if !rootOK || !cloneOK || rootPlan != clonePlan {
		t.Fatal("compatibility Clone stopped sharing the existing mutable root pointer before P005c")
	}

	names := append([]string(nil), expectedBaseFactoryCatalog...)
	sort.Strings(names)
	if !reflect.DeepEqual(names, expectedBaseFactoryCatalog) {
		t.Fatal("expected base factory catalog must remain sorted and deterministic")
	}
}

func TestBaseToolFactoriesDoNotReadMutableConfigAfterRegistration(t *testing.T) {
	loop, cfg, _ := newBaseFactoryCatalogTestRuntime(t)
	source := loop.GetRegistry().GetDefaultAgent().Tools
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for iteration := 0; ; iteration++ {
			select {
			case <-stop:
				return
			default:
			}
			cfg.Tools.ReadFile.Mode = config.ReadFileModeBytes
			cfg.Tools.ReadFile.MaxReadFileSize = iteration + 1
			cfg.Tools.AllowReadPaths = []string{"/mutated/read"}
			cfg.Tools.AllowWritePaths = []string{"/mutated/write"}
			cfg.Tools.Web.PrivateHostWhitelist = []string{"mutated.invalid"}
			cfg.Tools.Web.Brave.APIKeys = config.SimpleSecureStrings("mutated-key")
			cfg.Tools.Message.MediaEnabled = iteration%2 == 0
			cfg.Tools.Skills.SearchCache.MaxSize = iteration + 1
			cfg.Tools.Skills.SearchCache.TTLSeconds = iteration + 2
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()

	for iteration := range 25 {
		owner, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
			Scope:   tools.ToolOwnerScopeAgent,
			AgentID: fmt.Sprintf("frozen-config-%d", iteration),
		}, append([]string(nil), expectedBaseFactoryCatalog...))
		if err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		if err := owner.Close(); err != nil {
			t.Fatalf("iteration %d close: %v", iteration, err)
		}
	}
}

func TestBaseToolFactoryCatalogInvariantFailuresPanic(t *testing.T) {
	assertPanic := func(name string, run func()) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected invariant failure to panic")
				}
			}()
			run()
		})
	}
	assertPanic("factory prototype", func() {
		mustToolFactoryFromPrototype(nil, tools.ToolTraits{}, func(tools.ToolBuildContext) (tools.Tool, error) {
			return nil, nil
		})
	})
	factory := tools.NewUpdatePlanToolFactory()
	assertPanic("factory-backed registration", func() {
		mustRegisterFactoryBackedTool(nil, tools.NewUpdatePlanTool(), factory)
	})
	assertPanic("factory dependency registration", func() {
		mustRegisterFactoryDependency(nil, factory)
	})
	if _, ok := baseToolFactoryTraits("not_in_catalog"); ok {
		t.Fatal("unknown tool received base catalog traits")
	}
	assertPanic("unknown traits", func() { mustBaseToolFactoryTraits("not_in_catalog") })
}

func TestBaseToolFactoryOwnerStateMediaAndBorrowedServices(t *testing.T) {
	var skillSearchCalls atomic.Int64
	skillServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		skillSearchCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"results":[{"score":0.95,"slug":"catalog-skill","displayName":"Catalog Skill","summary":"test","version":"1.0.0"}]}`,
		))
	}))
	defer skillServer.Close()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "gpt-5"
	cfg.Agents.Defaults.Provider = "openai"
	cfg.Tools.Adaptation = config.DefaultToolAdaptationConfig()
	cfg.Tools.Message.MediaEnabled = true
	cfg.Tools.Skills.Registries = config.SkillsRegistriesConfig{
		&config.SkillRegistryConfig{
			Name: "clawhub", Enabled: true, BaseURL: skillServer.URL,
		},
	}
	reactionChannel := &baseCatalogReactionChannel{fakeChannel: fakeChannel{id: "catalog"}}
	channelManager := &baseCatalogChannelManager{reaction: reactionChannel}
	gitManager := &baseCatalogGitManager{}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(
		cfg,
		messageBus,
		&mockProvider{},
		func(loop *AgentLoop) {
			loop.gitWorkspaces = gitManager
		},
	)
	defer loop.Close()
	store := media.NewFileMediaStore()
	loop.SetMediaStore(store)
	source := loop.GetRegistry().GetDefaultAgent().Tools
	roots := []string{
		"find_skills", "git_workspace", "load_image", "message", "reaction",
		"send_file", "view_image",
	}
	first, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "services-first",
	}, roots)
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "services-second",
	}, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	first.SetMediaStore(store)
	second.SetMediaStore(store)

	firstMessageRaw, _ := first.Get("message")
	secondMessageRaw, _ := second.Get("message")
	firstMessage := firstMessageRaw.(*tools.MessageTool)
	secondMessage := secondMessageRaw.(*tools.MessageTool)
	messageContext := tools.WithToolContext(context.Background(), "catalog", "chat")
	messageContext = tools.WithToolSessionContext(messageContext, "main", "state-session", nil)
	if result := firstMessage.Execute(
		messageContext,
		map[string]any{"content": "first"},
	); result == nil || result.IsError {
		t.Fatalf("first message result = %#v", result)
	}
	if !firstMessage.HasSentInRound("state-session") || secondMessage.HasSentInRound("state-session") {
		t.Fatal("message sent-target state crossed owner boundaries")
	}
	select {
	case <-messageBus.OutboundChan():
	default:
		t.Fatal("first owner did not use the borrowed message bus")
	}

	firstSkills, _ := first.Get("find_skills")
	for range 2 {
		result := firstSkills.Execute(context.Background(), map[string]any{
			"query": "catalog", "limit": float64(5),
		})
		if result == nil || result.IsError || !strings.Contains(result.ForLLM, "catalog-skill") {
			t.Fatalf("first skills result = %#v", result)
		}
	}
	if skillSearchCalls.Load() != 1 {
		t.Fatalf("first owner search calls = %d, want cached 1", skillSearchCalls.Load())
	}
	if closeErr := first.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	secondSkills, _ := second.Get("find_skills")
	if result := secondSkills.Execute(context.Background(), map[string]any{
		"query": "catalog", "limit": float64(5),
	}); result == nil || result.IsError || !strings.Contains(result.ForLLM, "catalog-skill") {
		t.Fatalf("second skills result = %#v", result)
	}
	if skillSearchCalls.Load() != 2 {
		t.Fatalf("second owner reused sibling cache; search calls = %d", skillSearchCalls.Load())
	}

	imagePath := filepath.Join(cfg.Agents.Defaults.Workspace, "catalog.png")
	pngHeader := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
		0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE,
	}
	if err := os.WriteFile(imagePath, pngHeader, 0o600); err != nil {
		t.Fatal(err)
	}
	mediaContext := tools.WithToolContext(context.Background(), "catalog", "media-chat")
	mediaContext = tools.WithToolSessionContext(mediaContext, "main", "media-session", nil)
	messageResult := secondMessage.Execute(mediaContext, map[string]any{
		"content": "media",
		"media": []any{map[string]any{
			"path": imagePath, "type": "image", "filename": "catalog.png",
		}},
	})
	if messageResult == nil || messageResult.IsError || !messageResult.Silent ||
		!secondMessage.HasSentInRound("media-session") {
		t.Fatalf("owner-local media message result = %#v", messageResult)
	}
	select {
	case outbound := <-messageBus.OutboundMediaChan():
		if len(outbound.Parts) != 1 || outbound.ChatID != "media-chat" {
			t.Fatalf("media callback output = %#v", outbound)
		}
	default:
		t.Fatal("media-aware message did not use its injected store/callback")
	}

	for _, name := range []string{"load_image", "view_image"} {
		tool, _ := second.Get(name)
		result := tool.Execute(mediaContext, map[string]any{"path": imagePath})
		if result == nil || result.IsError || len(result.Media) != 1 || result.ResponseHandled {
			t.Fatalf("%s media result = %#v", name, result)
		}
	}
	sendFile, _ := second.Get("send_file")
	fileResult := sendFile.Execute(mediaContext, map[string]any{
		"path": imagePath, "filename": "sent.png",
	})
	if fileResult == nil || fileResult.IsError || len(fileResult.Media) != 1 ||
		!fileResult.ResponseHandled {
		t.Fatalf("send_file result = %#v", fileResult)
	}

	loop.channelManager = channelManager
	reaction, _ := second.Get("reaction")
	reactionContext := tools.WithToolInboundContext(
		context.Background(), "catalog", "reaction-chat", "message-1", "",
	)
	reactionResult := reaction.Execute(reactionContext, map[string]any{})
	if reactionResult == nil || reactionResult.IsError || !reactionResult.Silent ||
		reactionChannel.calls.Load() != 1 {
		t.Fatalf("reaction borrowed-service result = %#v calls=%d", reactionResult, reactionChannel.calls.Load())
	}

	gitTool, _ := second.Get("git_workspace")
	gitResult := gitTool.Execute(context.Background(), map[string]any{"action": "list"})
	if gitResult == nil || gitResult.IsError || gitManager.statsCalls.Load() != 1 {
		t.Fatalf("git borrowed-service result = %#v calls=%d", gitResult, gitManager.statsCalls.Load())
	}
}

func TestBaseToolFactorySendTTSBorrowedProviderSurvivesSiblingClose(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"audio":{"data":"YXVkaW8="}}}]}`))
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "test-model"
	cfg.Voice.TTSAccountRef = "mimo-account"
	cfg.Voice.TTSModelName = "mimo-tts"
	cfg.Tools.SendTTS.Enabled = true
	cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
		Name: "mimo-tts", Model: "mimo/mimo-v2-tts",
	})
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName: "mimo-account",
		Model:     "mimo/mimo-v2-tts",
		APIBase:   server.URL,
		APIKeys:   config.SimpleSecureStrings("test-mimo-key"),
		Enabled:   true,
	})
	loop := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), &mockProvider{})
	defer loop.Close()
	store := media.NewFileMediaStore()
	loop.SetMediaStore(store)
	source := loop.GetRegistry().GetDefaultAgent().Tools
	first, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "tts-first",
	}, []string{"send_tts"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "tts-second",
	}, []string{"send_tts"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.SetMediaStore(store)
	if closeErr := first.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	tool, _ := second.Get("send_tts")
	ctx := tools.WithToolContext(context.Background(), "catalog", "tts-chat")
	result := tool.Execute(ctx, map[string]any{"text": "hello"})
	if result == nil || result.IsError || !result.ResponseHandled ||
		result.ForUser != "hello" || len(result.Media) != 1 || requests.Load() != 1 {
		t.Fatalf("send_tts borrowed-provider result = %#v requests=%d", result, requests.Load())
	}
	path, err := store.Resolve(result.Media[0])
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path)
}

func TestBaseWebToolFactoriesFreezeSlicesAndKeepKeyPoolsOwnerLocal(t *testing.T) {
	keys := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			defer r.Body.Close()
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode Tavily request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			key, _ := payload["api_key"].(string)
			keys <- key
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":[{"title":"result","url":"https://example.test","content":"ok"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>frozen fetch options</body></html>"))
	}))
	defer server.Close()

	whitelist := []string{"127.0.0.1"}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.Defaults.ModelName = "test-model"
	cfg.Tools.Web.Provider = "tavily"
	cfg.Tools.Web.Tavily.Enabled = true
	cfg.Tools.Web.Tavily.BaseURL = server.URL
	cfg.Tools.Web.Tavily.SetAPIKeys([]string{"original-1", "original-2"})
	cfg.Tools.Web.PrivateHostWhitelist = whitelist
	loop := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), &mockProvider{})
	defer loop.Close()

	cfg.Tools.Web.Tavily.APIKeys[0].Set("mutated")
	cfg.Tools.Web.Tavily.APIKeys[1].Set("also-mutated")
	whitelist[0] = "example.invalid"
	source := loop.GetRegistry().GetDefaultAgent().Tools
	first, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "web-first",
	}, []string{"web_fetch", "web_search"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "web-second",
	}, []string{"web_fetch", "web_search"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	for _, owner := range []*tools.ToolRegistry{first, second, first, second} {
		search, _ := owner.Get("web_search")
		result := search.Execute(context.Background(), map[string]any{
			"query": "factory", "count": float64(1),
		})
		if result == nil || result.IsError || !strings.Contains(result.ForLLM, "result") {
			t.Fatalf("web search result = %#v", result)
		}
	}
	for index, want := range []string{"original-1", "original-1", "original-2", "original-2"} {
		select {
		case got := <-keys:
			if got != want {
				t.Fatalf("Tavily key %d = %q, want %q", index, got, want)
			}
		default:
			t.Fatalf("missing Tavily request %d", index)
		}
	}
	for label, owner := range map[string]*tools.ToolRegistry{"first": first, "second": second} {
		fetch, _ := owner.Get("web_fetch")
		result := fetch.Execute(context.Background(), map[string]any{
			"url": server.URL + "/fetch", "maxChars": float64(1000),
		})
		if result == nil || result.IsError || !strings.Contains(result.ForLLM, "frozen fetch options") {
			t.Fatalf("%s web fetch result = %#v", label, result)
		}
	}
}
