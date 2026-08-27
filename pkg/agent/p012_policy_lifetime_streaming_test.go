package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type p012LifetimePolicy struct {
	reason string
}

func (policy *p012LifetimePolicy) EvaluateTool(
	context.Context,
	tools.ToolPolicyRequest,
) (tools.ToolPolicyDecision, error) {
	return tools.ToolPolicyDecision{
		Kind:       tools.ToolPolicyDecisionAllow,
		ReasonCode: policy.reason,
	}, nil
}

func TestToolPolicyStrictChildInheritsParentAdmissionSnapshot(t *testing.T) {
	parentPolicy := &p012LifetimePolicy{reason: "parent_snapshot"}
	lateLoopPolicy := &p012LifetimePolicy{reason: "late_loop_policy"}
	loop := &AgentLoop{
		cfg:        &config.Config{},
		toolPolicy: parentPolicy,
	}

	parent := &turnState{}
	loop.prepareTurnState(parent)
	loop.toolPolicy = lateLoopPolicy

	child := &turnState{parentTurnState: parent}
	loop.prepareTurnState(child)

	if !parent.toolPolicyBound || parent.toolPolicy != parentPolicy {
		t.Fatalf(
			"parent policy binding = %T/%v, want admitted parent snapshot",
			parent.toolPolicy,
			parent.toolPolicyBound,
		)
	}
	if !child.toolPolicyBound || child.toolPolicy != parentPolicy {
		t.Fatalf("child policy binding = %T/%v, want exact parent snapshot", child.toolPolicy, child.toolPolicyBound)
	}
	if child.toolPolicy == lateLoopPolicy {
		t.Fatal("strict child widened to loop policy installed after parent admission")
	}

	unpreparedParent := &turnState{}
	failClosedChild := &turnState{parentTurnState: unpreparedParent}
	loop.prepareTurnState(failClosedChild)
	if !failClosedChild.toolPolicyBound || failClosedChild.toolPolicy != nil {
		t.Fatalf(
			"child of unprepared parent policy = %T/%v, want bound nil fail-closed policy",
			failClosedChild.toolPolicy,
			failClosedChild.toolPolicyBound,
		)
	}
}

type p012MutatingStreamingFallbackProvider struct {
	streamCalls int
	chatCalls   int

	streamDefinitions []providers.ToolDefinition
	chatDefinitions   []providers.ToolDefinition
}

func (provider *p012MutatingStreamingFallbackProvider) ChatStream(
	_ context.Context,
	_ []providers.Message,
	definitions []providers.ToolDefinition,
	_ string,
	_ map[string]any,
	_ func(string),
) (*providers.LLMResponse, error) {
	provider.streamCalls++
	provider.streamDefinitions = cloneToolDefinitions(definitions)
	if len(definitions) > 0 {
		definitions[0].Function.Name = "provider_mutated_tool"
		definitions[0].Function.Parameters["type"] = "array"
	}
	return nil, errors.New("stream failed before publishing a chunk")
}

func (provider *p012MutatingStreamingFallbackProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	definitions []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.chatCalls++
	provider.chatDefinitions = cloneToolDefinitions(definitions)
	return &providers.LLMResponse{
		Content:      "chat fallback retained definitions",
		FinishReason: "stop",
	}, nil
}

func TestConfiguredStreamingFallbackDetachesDefinitionsPerProviderAttempt(t *testing.T) {
	cfg := newConfiguredStreamingTestConfig(t, true, true, nil)
	messageBus := bus.NewMessageBus()
	messageBus.SetStreamDelegate(configuredStreamingDelegate{streamer: &recordingStreamer{}})
	provider := &p012MutatingStreamingFallbackProvider{}
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(loop.Close)

	if got := runConfiguredStreamingTurn(t, loop, "pico"); got != "chat fallback retained definitions" {
		t.Fatalf("streaming fallback response = %q", got)
	}
	if provider.streamCalls != 1 || provider.chatCalls != 1 {
		t.Fatalf("provider calls = stream %d/chat %d, want 1/1", provider.streamCalls, provider.chatCalls)
	}
	if len(provider.streamDefinitions) == 0 {
		t.Fatal("streaming provider received no tool definitions")
	}
	if !reflect.DeepEqual(provider.chatDefinitions, provider.streamDefinitions) {
		t.Fatalf(
			"fallback Chat definitions retained streaming-provider mutation:\nstream before mutation: %#v\nchat: %#v",
			provider.streamDefinitions,
			provider.chatDefinitions,
		)
	}
}
