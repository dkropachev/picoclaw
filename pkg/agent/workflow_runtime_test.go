package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/media"
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

func (m *workflowMediaChannelManager) DismissToolFeedback(context.Context, string, string, *bus.InboundContext) {
}
