package channelmessage

import (
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestChannelMessageProjectionAndBoundsResidualMatrix(t *testing.T) {
	var nilBackend *Backend
	if nilBackend.ConnectorNames() != nil || nilBackend.HasConnector("connector") || nilBackend.ConnectorCount() != 0 {
		t.Fatal("nil backend reported connectors")
	}
	if got := messageConnector(bus.InboundMessage{Channel: "fallback"}); got != "fallback" {
		t.Fatalf("fallback connector = %q", got)
	}
	if got := messageConnector(bus.InboundMessage{Channel: "fallback", Context: bus.InboundContext{
		Channel: "context",
	}}); got != "context" {
		t.Fatalf("context connector = %q", got)
	}
	if trust := emailTrust(bus.InboundMessage{}, AdapterConfig{Source: SourceChat}); trust != "" {
		t.Fatalf("chat email trust = %q", trust)
	}
	if trust := emailTrust(bus.InboundMessage{}, AdapterConfig{Source: SourceEmail}); trust != "unverified" {
		t.Fatalf("unverified email trust = %q", trust)
	}
	if trust := emailTrust(bus.InboundMessage{
		EventSenderVerified: true, EventTransportAuthenticated: true,
	}, AdapterConfig{Source: SourceEmail}); trust != "verified" {
		t.Fatalf("verified email trust = %q", trust)
	}

	attachments, truncated := safeAttachments([]bus.InboundAttachment{{
		Kind: strings.Repeat("k", maxSafeAttachmentStringBytes+1), SizeBytes: -1,
	}}, maxSafeAttachmentStringBytes+2)
	if len(attachments) != 1 || !truncated || attachments[0].SizeBytes != 0 {
		t.Fatalf("bounded attachments = %#v/%v", attachments, truncated)
	}
	attachments, truncated = safeAttachments(make([]bus.InboundAttachment, maxSafeAttachmentCount+1), 1)
	if len(attachments) != 1 || !truncated {
		t.Fatalf("work-bounded attachments = %d/%v", len(attachments), truncated)
	}
	if value, used, truncated := safeStringWithinBudget("value", 0); value != "" || used != 0 || !truncated {
		t.Fatalf("zero-budget string = %q/%d/%v", value, used, truncated)
	}
	if value, used, truncated := safeStringWithinBudget("", 0); value != "" || used != 0 || truncated {
		t.Fatalf("empty zero-budget string = %q/%d/%v", value, used, truncated)
	}

	adapter := AdapterConfig{Source: SourceChat, ChannelType: "matrix"}
	if actor := actorFromMessage(bus.InboundMessage{}, adapter); actor != nil {
		t.Fatalf("empty actor = %#v", actor)
	}
	actor := actorFromMessage(bus.InboundMessage{
		Context: bus.InboundContext{SenderID: "sender"},
	}, adapter)
	if actor == nil || actor.ID != "matrix:sender" || actor.Type != "user" {
		t.Fatalf("fallback actor = %#v", actor)
	}
	emailActor := actorFromMessage(bus.InboundMessage{
		Sender: bus.SenderInfo{CanonicalID: "mail:user@example.test"},
	}, AdapterConfig{Source: SourceEmail})
	if emailActor == nil || emailActor.Type != "email_address" {
		t.Fatalf("email actor = %#v", emailActor)
	}
	if subject := subjectFromMessage(bus.InboundMessage{}); subject != nil {
		t.Fatalf("empty subject = %#v", subject)
	}
	subject := subjectFromMessage(bus.InboundMessage{
		ConversationName: "Conversation",
		Context:          bus.InboundContext{ChatID: "chat", Mentioned: true, Account: "account"},
	})
	if subject == nil || subject.ID != "chat" || subject.Attributes["mentioned"] != "true" {
		t.Fatalf("subject = %#v", subject)
	}
	if compactAttributes(map[string]string{"empty": " ", "value": " x "})["value"] != "x" {
		t.Fatal("attribute compaction mismatch")
	}
	if compactAttributes(map[string]string{"empty": " "}) != nil {
		t.Fatal("empty attributes were retained")
	}
	if got := safeString("ééé", 5); got != "éé" {
		t.Fatalf("bounded UTF-8 string = %q", got)
	}
	if got := safeString(string([]byte{0xff}), 8); got == string([]byte{0xff}) {
		t.Fatal("invalid UTF-8 string was retained")
	}
	if validConnector("") || validConnector(" bad ") || validChannelType("") || validChannelType(" bad ") {
		t.Fatal("invalid connector/channel accepted")
	}
	now := time.Now()
	if cloned := cloneTime(&now); cloned == &now || !cloned.Equal(now) {
		t.Fatal("time clone mismatch")
	}
}
