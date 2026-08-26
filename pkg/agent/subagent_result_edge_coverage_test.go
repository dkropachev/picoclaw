package agent

import (
	"context"
	"testing"
	"time"
)

func TestProcessDirectWithChannelAndPublishOwnsReturnedResponse(t *testing.T) {
	loop, _, messageBus, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	response, err := loop.ProcessDirectWithChannelAndPublish(
		context.Background(),
		"publish this response",
		"direct-publish-session",
		"cli",
		"direct-publish-chat",
	)
	if err != nil {
		t.Fatalf("ProcessDirectWithChannelAndPublish() error = %v", err)
	}
	if response != "Mock response" {
		t.Fatalf("direct response = %q, want %q", response, "Mock response")
	}

	select {
	case outbound := <-messageBus.OutboundChan():
		if outbound.Content != response || outbound.SessionKey != "direct-publish-session" ||
			outbound.Context.Channel != "cli" || outbound.Context.ChatID != "direct-publish-chat" {
			t.Fatalf("direct published outbound = %#v", outbound)
		}
	case <-time.After(time.Second):
		t.Fatal("direct response was not published")
	}
	select {
	case duplicate := <-messageBus.OutboundChan():
		t.Fatalf("direct response published twice: %#v", duplicate)
	default:
	}
}

func TestSubTurnConfigUsesDefaultsWhenRuntimeConfigIsAbsent(t *testing.T) {
	runtimeConfig := (&AgentLoop{}).getSubTurnConfig()
	if runtimeConfig.maxDepth != defaultMaxSubTurnDepth ||
		runtimeConfig.maxConcurrent != defaultMaxConcurrentSubTurns ||
		runtimeConfig.concurrencyTimeout != defaultConcurrencyTimeout ||
		runtimeConfig.defaultTimeout != defaultSubTurnTimeout {
		t.Fatalf("nil runtime config defaults = %#v", runtimeConfig)
	}
}

func TestTrackedResultTurnStateDefensiveReleasePaths(t *testing.T) {
	var nilLoop *AgentLoop
	nilLoop.restoreTrackedSubagentOutputReservation(&turnState{})
	(&AgentLoop{}).restoreTrackedSubagentOutputReservation(nil)
	(*turnState)(nil).disableToolsForResultFollowUp()

	loop := &AgentLoop{}
	state := &turnState{
		turnID:     "unreserved-turn",
		agentID:    "agent-a",
		sessionKey: "session-a",
	}
	loop.activeTurnStates.Store(state.sessionKey, state)
	loop.restoreTrackedSubagentOutputReservation(state)
	if _, active := loop.activeTurnStates.Load(state.sessionKey); active {
		t.Fatal("turn without a matching output reservation remained active")
	}
}
