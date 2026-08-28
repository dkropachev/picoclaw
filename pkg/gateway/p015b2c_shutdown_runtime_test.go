package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

type p015B2CShutdownFailingChannel struct {
	stopCalls atomic.Int32
	err       error
}

func (*p015B2CShutdownFailingChannel) Name() string { return "p015b2c-shutdown" }

func (*p015B2CShutdownFailingChannel) Start(context.Context) error { return nil }

func (channel *p015B2CShutdownFailingChannel) Stop(context.Context) error {
	if channel.stopCalls.Add(1) == 1 {
		return channel.err
	}
	return nil
}

func (*p015B2CShutdownFailingChannel) Send(
	context.Context,
	bus.OutboundMessage,
) ([]string, error) {
	return []string{"p015b2c-shutdown"}, nil
}

func (*p015B2CShutdownFailingChannel) IsRunning() bool { return true }

func (*p015B2CShutdownFailingChannel) IsAllowed(string) bool { return true }

func (*p015B2CShutdownFailingChannel) IsAllowedSender(bus.SenderInfo) bool { return true }

func (*p015B2CShutdownFailingChannel) ReasoningChannelID() string { return "" }

func TestP015B2CShutdownDependencyFailureIsSealedAndPreservesFailSafeState(t *testing.T) {
	const errorCanary = "P015_B2C_SHUTDOWN_DEPENDENCY_ERROR_CANARY"
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	provider := &startupBlockedProvider{reason: "not used"}
	loop := agent.NewAgentLoop(cfg, messageBus, provider)
	manager, err := channels.NewManager(cfg, messageBus, nil)
	if err != nil {
		t.Fatalf("channels.NewManager() error = %v", err)
	}
	failing := &p015B2CShutdownFailingChannel{err: errors.New(errorCanary)}
	manager.RegisterChannel(failing.Name(), failing)
	if err := manager.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}

	records, _ := captureGatewaySafeRecords(t, func() {
		shutdownGateway(
			&services{ChannelManager: manager},
			loop,
			provider,
			messageBus,
			true,
		)
	})
	dependency := p015B2CFindRuntimeRecord(
		t, records, "Failed to stop runtime dependencies cleanly",
	)
	if dependency["component"] != "gateway" ||
		dependency["error_class"] != "internal" ||
		dependency["error_digest"] == nil {
		t.Fatalf("dependency diagnostic = %#v", dependency)
	}
	stopped := p015B2CFindRuntimeRecord(t, records, "✓ Gateway stopped")
	if stopped["component"] != "gateway" {
		t.Fatalf("stopped diagnostic = %#v", stopped)
	}
	dependencyJSON, marshalErr := json.Marshal(dependency)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if string(dependencyJSON) == "" ||
		containsGatewayRuntimeCanary(dependencyJSON, errorCanary) {
		t.Fatalf("shutdown diagnostic leaked raw dependency error: %s", dependencyJSON)
	}
	if failing.stopCalls.Load() != 1 {
		t.Fatalf("channel stop calls = %d, want 1", failing.stopCalls.Load())
	}
	if err := messageBus.PublishVoiceControl(
		context.Background(),
		bus.VoiceControl{},
	); err != nil {
		t.Fatalf("failed shutdown closed message bus despite dependency failure: %v", err)
	}

	if err := manager.StopAll(context.Background()); err != nil {
		t.Fatalf("retry StopAll() error = %v", err)
	}
	messageBus.Close()
	loop.Close()
}

func containsGatewayRuntimeCanary(record []byte, canary string) bool {
	return len(canary) > 0 && strings.Contains(string(record), canary)
}

func p015B2CFindRuntimeRecord(
	t *testing.T,
	records []map[string]any,
	message string,
) map[string]any {
	t.Helper()
	for _, record := range records {
		if record["message"] == message {
			return record
		}
	}
	t.Fatalf("missing runtime record %q: %#v", message, records)
	return nil
}
