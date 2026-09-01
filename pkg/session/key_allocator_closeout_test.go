package session

import (
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
)

func TestSessionKeyIdentityLinkAndLegacyEdgeMatrix(t *testing.T) {
	if BuildOpaqueSessionKey(" ") != "" {
		t.Fatal("blank opaque key was not empty")
	}
	for _, key := range []string{"", "agent", "agent::rest", "agent:main:", "other:main:rest"} {
		if parsed := ParseLegacyAgentSessionKey(key); parsed != nil {
			t.Fatalf("ParseLegacyAgentSessionKey(%q) = %#v", key, parsed)
		}
	}
	if aliases := BuildLegacyDirectAliases("main", "", "", " "); aliases != nil {
		t.Fatalf("blank direct aliases = %v", aliases)
	}
	if got := BuildLegacyPeerAlias("main", "", "", ""); got != "agent:main:unknown:unknown:unknown" {
		t.Fatalf("default peer alias = %q", got)
	}
	if CanonicalSessionIdentityID("telegram", " ", map[string][]string{"person": {"telegram:1"}}) != "" {
		t.Fatal("blank canonical identity was not empty")
	}
	links := map[string][]string{
		"":         {"ignored"},
		" Person ": {"telegram:USER", "backup"},
		"Other":    {"matrix:other"},
	}
	for name, test := range map[string]struct {
		channel string
		raw     string
		want    string
	}{
		"channel-qualified": {" TELEGRAM ", "user", "person"},
		"raw-qualified":     {"", "telegram:user", "person"},
		"plain member":      {"ignored", "backup", "person"},
		"unlinked":          {"telegram", "nobody", "nobody"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := CanonicalSessionIdentityID(test.channel, test.raw, links); got != test.want {
				t.Fatalf("CanonicalSessionIdentityID() = %q, want %q", got, test.want)
			}
		})
	}
	if resolveLinkedPeerID(nil, "telegram", "user") != "" ||
		resolveLinkedPeerID(links, "telegram", " ") != "" {
		t.Fatal("empty linked-peer boundary was accepted")
	}
	if normalizeLegacyChannel(" ") != "unknown" || normalizeLegacyChannel(" TeLeGram ") != "telegram" {
		t.Fatal("legacy channel normalization failed")
	}
}

type sessionCloneCoverageStruct struct {
	Values []string
	Hidden string
}

func TestSessionGraphCloneKindAndProtocolFieldMatrix(t *testing.T) {
	seen := make(map[sessionCloneVisit]reflect.Value)
	if got := cloneSessionValue(reflect.Value{}, seen); got.IsValid() && !got.IsNil() {
		t.Fatalf("invalid clone = %#v", got)
	}
	var nilInterface any
	if got := cloneSessionValue(reflect.ValueOf(&nilInterface).Elem(), seen); !got.IsNil() {
		t.Fatalf("nil interface clone = %#v", got)
	}
	var nilMap map[string]any
	if got := cloneSessionValue(reflect.ValueOf(nilMap), seen); !got.IsNil() {
		t.Fatalf("nil map clone = %#v", got)
	}
	var nilSlice []any
	if got := cloneSessionValue(reflect.ValueOf(nilSlice), seen); !got.IsNil() {
		t.Fatalf("nil slice clone = %#v", got)
	}
	array := [2]string{"a", "b"}
	if got := cloneSessionValue(reflect.ValueOf(array), seen).Interface().([2]string); got != array {
		t.Fatalf("array clone = %#v", got)
	}
	value := &sessionCloneCoverageStruct{Values: []string{"original"}, Hidden: "keep"}
	clonedPointer := cloneSessionValue(reflect.ValueOf(value), seen).Interface().(*sessionCloneCoverageStruct)
	if clonedPointer == value || &clonedPointer.Values[0] == &value.Values[0] || clonedPointer.Hidden != "keep" {
		t.Fatalf("pointer/struct clone = %#v", clonedPointer)
	}

	google := &providers.GoogleExtra{ThoughtSignature: "signature"}
	extra := &providers.ExtraContent{Google: google}
	function := &providers.FunctionCall{Name: "tool", Arguments: `{}`}
	message := providers.Message{
		Role: "assistant", Media: []string{"media"},
		Attachments: []providers.Attachment{{Filename: "file"}},
		Parts:       []providers.PromptPart{{Type: "text", Text: "part"}},
		SystemParts: []providers.ContentBlock{{
			Type: "text", Text: "system", CacheControl: &providers.CacheControl{Type: "ephemeral"},
		}},
		ToolCalls: []providers.ToolCall{{
			ID: "call", Function: function, Arguments: map[string]any{"values": []any{"x"}},
			ExtraContent: extra,
		}},
	}
	clone := CloneMessages([]providers.Message{message})[0]
	clone.Media[0] = "changed"
	clone.Attachments[0].Filename = "changed"
	clone.Parts[0].Text = "changed"
	clone.SystemParts[0].CacheControl.Type = "changed"
	clone.ToolCalls[0].Function.Name = "changed"
	clone.ToolCalls[0].Arguments["values"].([]any)[0] = "changed"
	clone.ToolCalls[0].ExtraContent.Google.ThoughtSignature = "changed"
	if message.Media[0] != "media" || message.Attachments[0].Filename != "file" ||
		message.Parts[0].Text != "part" || message.SystemParts[0].CacheControl.Type != "ephemeral" ||
		message.ToolCalls[0].Function.Name != "tool" ||
		message.ToolCalls[0].Arguments["values"].([]any)[0] != "x" ||
		message.ToolCalls[0].ExtraContent.Google.ThoughtSignature != "signature" {
		t.Fatalf("message clone mutated source: %#v", message)
	}
}

func TestSessionScopeAllocationMissingAndDefaultDimensionMatrix(t *testing.T) {
	base := AllocationInput{
		AgentID: " Worker ",
		Context: bus.InboundContext{
			Channel: " ", Account: " Account ", SpaceID: "SPACE", ChatID: "CHAT",
			TopicID: "TOPIC", SenderID: "Sender",
		},
		SessionPolicy: routing.SessionPolicy{
			Dimensions:    []string{"space", "chat", "topic", "sender", "unknown"},
			IdentityLinks: map[string][]string{"person": {"sender"}},
		},
	}
	scope := buildSessionScope(base)
	if scope.Channel != "unknown" || scope.AgentID != "worker" || scope.Account != "account" ||
		scope.Values["space"] != "space:space" || scope.Values["chat"] != "direct:chat" ||
		scope.Values["topic"] != "topic:topic" || scope.Values["sender"] != "person" {
		t.Fatalf("defaulted scope = %#v", scope)
	}

	empty := base
	empty.Context.SpaceID = ""
	empty.Context.ChatID = ""
	empty.Context.TopicID = ""
	empty.Context.SenderID = ""
	empty.SessionPolicy.IdentityLinks = nil
	scope = buildSessionScope(empty)
	if scope.Dimensions != nil || scope.Values != nil {
		t.Fatalf("empty dimensions persisted = %#v", scope)
	}
	aliases := buildLegacySessionAliases(empty)
	if len(aliases) != 1 || aliases[0] != "agent:worker:main" {
		t.Fatalf("empty direct aliases = %v", aliases)
	}

	group := base
	group.Context.Channel = "discord"
	group.Context.ChatType = "group"
	group.Context.ChatID = ""
	if aliases := buildLegacySessionAliases(group); len(aliases) != 1 {
		t.Fatalf("missing group aliases = %v", aliases)
	}
	group.Context.ChatID = "group"
	group.Context.TopicID = "topic"
	if aliases := buildLegacySessionAliases(group); len(aliases) != 2 ||
		aliases[1] != "agent:worker:discord:group:group/topic" {
		t.Fatalf("group aliases = %v", aliases)
	}

	telegram := base
	telegram.Context.Channel = "telegram"
	telegram.Context.ChatType = "group"
	telegram.SessionPolicy.Dimensions = []string{"chat"}
	if !shouldPreserveTelegramForumIsolation(telegram) {
		t.Fatal("telegram forum isolation was not preserved")
	}
	scope = buildSessionScope(telegram)
	if scope.Values["chat"] != "group:chat/topic" {
		t.Fatalf("telegram chat dimension = %#v", scope)
	}
	telegram.SessionPolicy.Dimensions = []string{"chat", " TOPIC "}
	if shouldPreserveTelegramForumIsolation(telegram) {
		t.Fatal("explicit topic dimension duplicated forum isolation")
	}
	telegram.Context.TopicID = ""
	if shouldPreserveTelegramForumIsolation(telegram) {
		t.Fatal("missing Telegram topic preserved isolation")
	}
	telegram.Context.Channel = "discord"
	telegram.Context.TopicID = "topic"
	if shouldPreserveTelegramForumIsolation(telegram) {
		t.Fatal("non-Telegram channel preserved forum isolation")
	}

	if got := uniqueAliases(nil); got != nil {
		t.Fatalf("uniqueAliases(nil) = %v", got)
	}
	if got := uniqueAliases([]string{" ", ""}); got != nil {
		t.Fatalf("uniqueAliases(blanks) = %v", got)
	}
}
