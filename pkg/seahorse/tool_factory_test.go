package seahorse

import (
	"context"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/tools"
)

func retrievalForToolFactoryTest(t *testing.T) *RetrievalEngine {
	t.Helper()
	return &RetrievalEngine{store: openTestStore(t)}
}

func TestRetrievalToolFactoriesRejectIncompleteRetrieval(t *testing.T) {
	tests := []struct {
		name      string
		retrieval *RetrievalEngine
	}{
		{name: "nil retrieval"},
		{name: "nil store", retrieval: &RetrievalEngine{}},
		{name: "nil database", retrieval: &RetrievalEngine{store: &Store{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			grep, grepFactory, grepErr := NewGrepToolWithFactory(test.retrieval)
			if grepErr == nil || grep != nil || grepFactory != nil {
				t.Fatalf("grep factory = %#v, %#v, %v", grep, grepFactory, grepErr)
			}
			expand, expandFactory, expandErr := NewExpandToolWithFactory(test.retrieval)
			if expandErr == nil || expand != nil || expandFactory != nil {
				t.Fatalf("expand factory = %#v, %#v, %v", expand, expandFactory, expandErr)
			}
		})
	}
}

func TestRetrievalToolFactoriesFreezeDescriptorsAndTraits(t *testing.T) {
	retrieval := retrievalForToolFactoryTest(t)
	grep, grepFactory, err := NewGrepToolWithFactory(retrieval)
	if err != nil {
		t.Fatal(err)
	}
	expand, expandFactory, err := NewExpandToolWithFactory(retrieval)
	if err != nil {
		t.Fatal(err)
	}

	wantTraits := tools.ToolTraits{
		Risk:        tools.ToolRiskReadOnly,
		Parallel:    tools.ToolParallelSafe,
		Idempotency: tools.ToolIdempotencyIdempotent,
		Sharing:     tools.ToolSharingPerOwner,
	}
	for _, test := range []struct {
		name    string
		live    tools.Tool
		factory tools.ToolFactory
	}{
		{name: ShortGrepToolName, live: grep, factory: grepFactory},
		{name: ShortExpandToolName, live: expand, factory: expandFactory},
	} {
		t.Run(test.name, func(t *testing.T) {
			descriptor := test.factory.Descriptor()
			if descriptor.Name != test.name || descriptor.Name != test.live.Name() ||
				descriptor.Description != test.live.Description() ||
				!reflect.DeepEqual(descriptor.Parameters, test.live.Parameters()) {
				t.Fatalf("descriptor = %#v, live=%T", descriptor, test.live)
			}
			if descriptor.PromptMetadata != (tools.PromptMetadata{
				Layer:  tools.ToolPromptLayerCapability,
				Slot:   tools.ToolPromptSlotTooling,
				Source: tools.ToolPromptSourceRegistry,
			}) {
				t.Fatalf("prompt metadata = %#v", descriptor.PromptMetadata)
			}
			if got := test.factory.Traits(); got != wantTraits {
				t.Fatalf("traits = %#v, want %#v", got, wantTraits)
			}

			descriptor.Parameters["type"] = "mutated"
			if required, ok := descriptor.Parameters["required"].([]string); ok && len(required) > 0 {
				required[0] = "mutated"
			}
			fresh := test.factory.Descriptor()
			if fresh.Parameters["type"] != "object" ||
				reflect.DeepEqual(fresh.Parameters, descriptor.Parameters) {
				t.Fatalf("factory retained returned descriptor aliases: %#v", fresh.Parameters)
			}
			liveParameters := test.live.Parameters()
			liveParameters["type"] = "mutated live projection"
			if test.factory.Descriptor().Parameters["type"] != "object" {
				t.Fatal("factory descriptor retained a live parameter projection")
			}
		})
	}
}

func TestRetrievalToolFactoriesReturnFreshBorrowedProducts(t *testing.T) {
	retrieval := retrievalForToolFactoryTest(t)
	grep, grepFactory, err := NewGrepToolWithFactory(retrieval)
	if err != nil {
		t.Fatal(err)
	}
	expand, expandFactory, err := NewExpandToolWithFactory(retrieval)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		live    tools.Tool
		factory tools.ToolFactory
		engine  func(tools.Tool) *RetrievalEngine
	}{
		{
			name: ShortGrepToolName, live: grep, factory: grepFactory,
			engine: func(tool tools.Tool) *RetrievalEngine { return tool.(*GrepTool).engine },
		},
		{
			name: ShortExpandToolName, live: expand, factory: expandFactory,
			engine: func(tool tools.Tool) *RetrievalEngine { return tool.(*ExpandTool).engine },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			first, err := test.factory.New(tools.ToolBuildContext{})
			if err != nil {
				t.Fatal(err)
			}
			second, err := test.factory.New(tools.ToolBuildContext{})
			if err != nil {
				t.Fatal(err)
			}
			if first == test.live || second == test.live || first == second ||
				test.engine(test.live) != retrieval || test.engine(first) != retrieval ||
				test.engine(second) != retrieval {
				t.Fatalf("products reused a wrapper or changed retrieval: live=%p first=%p second=%p",
					test.live, first, second)
			}
			for label, candidate := range map[string]tools.Tool{
				"live": test.live, "first": first, "second": second,
			} {
				if _, owns := candidate.(io.Closer); owns {
					t.Fatalf("%s wrapper unexpectedly owns a closer", label)
				}
			}
		})
	}
}

func TestRetrievalToolFactoryOwnerConstructionAndResultParity(t *testing.T) {
	retrieval := retrievalForToolFactoryTest(t)
	ctx := context.Background()
	conversation, err := retrieval.store.GetOrCreateConversation(ctx, "factory-parity")
	if err != nil {
		t.Fatal(err)
	}
	message, err := retrieval.store.AddMessage(
		ctx,
		conversation.ConversationID,
		"user",
		"seahorse factory parity needle",
		11,
	)
	if err != nil {
		t.Fatal(err)
	}
	grep, grepFactory, err := NewGrepToolWithFactory(retrieval)
	if err != nil {
		t.Fatal(err)
	}
	expand, expandFactory, err := NewExpandToolWithFactory(retrieval)
	if err != nil {
		t.Fatal(err)
	}
	source := tools.NewToolRegistry()
	if registerErr := source.RegisterFactoryBacked(grep, grepFactory); registerErr != nil {
		t.Fatal(registerErr)
	}
	if registerErr := source.RegisterFactoryBacked(expand, expandFactory); registerErr != nil {
		t.Fatal(registerErr)
	}
	defer source.Close()
	roots := []string{ShortGrepToolName, ShortExpandToolName}
	child, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "seahorse-factory-child",
	}, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	sibling, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "seahorse-factory-sibling",
	}, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer sibling.Close()

	if !reflect.DeepEqual(source.ToProviderDefs(), child.ToProviderDefs()) ||
		!reflect.DeepEqual(source.ToProviderDefs(), sibling.ToProviderDefs()) {
		t.Fatal("owner construction changed provider definitions")
	}
	for _, registry := range []*tools.ToolRegistry{child, sibling} {
		childGrep, _ := registry.Get(ShortGrepToolName)
		childExpand, _ := registry.Get(ShortExpandToolName)
		if childGrep == grep || childExpand == expand ||
			childGrep.(*GrepTool).engine != retrieval ||
			childExpand.(*ExpandTool).engine != retrieval {
			t.Fatal("owner construction reused a wrapper or changed retrieval")
		}
	}

	grepArgs := map[string]any{"pattern": "needle", "all_conversations": true}
	expandArgs := map[string]any{"message_ids": []any{float64(message.ID)}}
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{name: ShortGrepToolName, args: grepArgs},
		{name: ShortExpandToolName, args: expandArgs},
	} {
		rootTool, _ := source.Get(test.name)
		want := rootTool.Execute(ctx, test.args)
		if want.IsError || !strings.Contains(want.ForLLM, "seahorse factory parity needle") {
			t.Fatalf("root %s result = %#v", test.name, want)
		}
		for label, registry := range map[string]*tools.ToolRegistry{
			"child": child, "sibling": sibling,
		} {
			candidate, _ := registry.Get(test.name)
			got := candidate.Execute(ctx, test.args)
			if got.ForLLM != want.ForLLM || got.ForUser != want.ForUser ||
				got.Silent != want.Silent || got.IsError != want.IsError ||
				got.Async != want.Async || got.ResponseHandled != want.ResponseHandled ||
				got.Err != nil || want.Err != nil || len(got.Media) != 0 ||
				len(got.Messages) != 0 || len(got.ArtifactTags) != 0 {
				t.Fatalf("%s %s result = %#v, want %#v", label, test.name, got, want)
			}
		}
	}

	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sibling.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if result, err := retrieval.Grep(ctx, GrepInput{Pattern: "needle"}); err != nil ||
		len(result.Messages) != 1 {
		t.Fatalf("registry close changed borrowed retrieval: %#v, %v", result, err)
	}
}

func TestRetrievalToolFactoriesKeepAgentDatabasesIsolated(t *testing.T) {
	ctx := context.Background()
	retrievalA := retrievalForToolFactoryTest(t)
	retrievalB := retrievalForToolFactoryTest(t)
	for label, retrieval := range map[string]*RetrievalEngine{"alpha": retrievalA, "beta": retrievalB} {
		conversation, err := retrieval.store.GetOrCreateConversation(ctx, label)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := retrieval.store.AddMessage(
			ctx,
			conversation.ConversationID,
			"user",
			label+"-only-memory",
			3,
		); err != nil {
			t.Fatal(err)
		}
	}
	grepA, _, err := NewGrepToolWithFactory(retrievalA)
	if err != nil {
		t.Fatal(err)
	}
	grepB, _, err := NewGrepToolWithFactory(retrievalB)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		label string
		tool  *GrepTool
		want  string
		deny  string
	}{
		{label: "alpha", tool: grepA, want: "alpha-only-memory", deny: "beta-only-memory"},
		{label: "beta", tool: grepB, want: "beta-only-memory", deny: "alpha-only-memory"},
	} {
		result := test.tool.Execute(ctx, map[string]any{
			"pattern": test.label + "-only-memory", "all_conversations": true,
		})
		if result.IsError || !strings.Contains(result.ForLLM, test.want) ||
			strings.Contains(result.ForLLM, test.deny) {
			t.Fatalf("%s result = %#v", test.label, result)
		}
	}
}

func TestRetrievalToolFactoryConcurrentProductsAndExecution(t *testing.T) {
	retrieval := retrievalForToolFactoryTest(t)
	ctx := context.Background()
	conversation, err := retrieval.store.GetOrCreateConversation(ctx, "factory-race")
	if err != nil {
		t.Fatal(err)
	}
	message, err := retrieval.store.AddMessage(
		ctx,
		conversation.ConversationID,
		"user",
		"concurrent seahorse retrieval",
		4,
	)
	if err != nil {
		t.Fatal(err)
	}
	beforeMessages, err := retrieval.store.GetMessageCount(ctx, conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	beforeSummaries, err := retrieval.store.getSummaryCount(ctx, conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	_, grepFactory, err := NewGrepToolWithFactory(retrieval)
	if err != nil {
		t.Fatal(err)
	}
	_, expandFactory, err := NewExpandToolWithFactory(retrieval)
	if err != nil {
		t.Fatal(err)
	}

	const workers = 32
	var wg sync.WaitGroup
	errorsCh := make(chan string, workers)
	products := make(chan tools.Tool, workers*2)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			grepRaw, buildErr := grepFactory.New(tools.ToolBuildContext{})
			if buildErr != nil {
				errorsCh <- buildErr.Error()
				return
			}
			expandRaw, buildErr := expandFactory.New(tools.ToolBuildContext{})
			if buildErr != nil {
				errorsCh <- buildErr.Error()
				return
			}
			products <- grepRaw
			products <- expandRaw
			grepResult := grepRaw.Execute(ctx, map[string]any{
				"pattern": "concurrent", "all_conversations": true,
			})
			expandResult := expandRaw.Execute(ctx, map[string]any{
				"message_ids": []any{float64(message.ID)},
			})
			if grepResult.IsError || expandResult.IsError {
				errorsCh <- "concurrent retrieval tool execution failed"
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	close(products)
	for message := range errorsCh {
		t.Error(message)
	}
	seen := make(map[uintptr]struct{}, workers*2)
	for product := range products {
		pointer := reflect.ValueOf(product).Pointer()
		if _, duplicate := seen[pointer]; duplicate {
			t.Fatalf("factory reused product pointer %x", pointer)
		}
		seen[pointer] = struct{}{}
	}
	if len(seen) != workers*2 {
		t.Fatalf("unique product pointers = %d, want %d", len(seen), workers*2)
	}
	afterMessages, err := retrieval.store.GetMessageCount(ctx, conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	afterSummaries, err := retrieval.store.getSummaryCount(ctx, conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if afterMessages != beforeMessages || afterSummaries != beforeSummaries {
		t.Fatalf(
			"read-only factory products mutated storage: messages %d/%d summaries %d/%d",
			beforeMessages,
			afterMessages,
			beforeSummaries,
			afterSummaries,
		)
	}
}

func TestRetrievalToolFactoriesDoNotOwnEngine(t *testing.T) {
	engine, err := NewEngine(Config{
		DBPath: filepath.Join(t.TempDir(), "seahorse.db"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	retrieval := engine.GetRetrieval()
	grep, grepFactory, err := NewGrepToolWithFactory(retrieval)
	if err != nil {
		t.Fatal(err)
	}
	source := tools.NewToolRegistry()
	if registerErr := source.RegisterFactoryBacked(grep, grepFactory); registerErr != nil {
		t.Fatal(registerErr)
	}
	child, err := source.InstantiateForOwnerSelection(tools.ToolOwner{
		Scope: tools.ToolOwnerScopeAgent, AgentID: "seahorse-borrowed-engine",
	}, []string{ShortGrepToolName})
	if err != nil {
		t.Fatal(err)
	}
	childTool, _ := child.Get(ShortGrepToolName)
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if result := childTool.Execute(context.Background(), map[string]any{
		"pattern": "nothing",
	}); result.IsError {
		t.Fatalf("registry close closed borrowed engine: %#v", result)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if result := childTool.Execute(context.Background(), map[string]any{
		"pattern": "nothing",
	}); !result.IsError || !strings.Contains(strings.ToLower(result.ForLLM), "closed") {
		t.Fatalf("wrapper remained usable after owning engine close: %#v", result)
	}
}
