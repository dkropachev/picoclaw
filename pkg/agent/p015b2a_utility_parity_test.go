package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/utils"
)

func TestP015B2AUtilityParityAtMigrationBoundaries(t *testing.T) {
	inbound := &bus.InboundContext{
		Raw:          map[string]string{"private": "value"},
		ReplyHandles: map[string]string{"sender": "handle"},
	}
	outbound := outboundContextFromInbound(inbound, "channel", "chat", "reply")
	if outbound.Channel != "channel" || outbound.ChatID != "chat" ||
		outbound.ReplyToMessageID != "reply" {
		t.Fatalf("outbound context defaults = %#v", outbound)
	}
	outbound.Raw["private"] = "mutated"
	outbound.ReplyHandles["sender"] = "mutated"
	if inbound.Raw["private"] != "value" || inbound.ReplyHandles["sender"] != "handle" {
		t.Fatalf("outbound context aliases inbound maps: %#v", inbound)
	}

	markFinalOutbound(nil)
	var final bus.OutboundMessage
	markFinalOutbound(&final)
	if final.Context.Raw[metadataKeyOutboundKind] != outboundKindFinal {
		t.Fatalf("final outbound metadata = %#v", final.Context.Raw)
	}

	if got := toolFeedbackExplanationFromResponse(nil, nil); got != "" {
		t.Fatalf("nil response explanation = %q", got)
	}
	toolCalls := []providers.ToolCall{
		{},
		{ExtraContent: &providers.ExtraContent{ToolFeedbackExplanation: " exact explanation "}},
	}
	if got := toolFeedbackExplanationFromToolCalls(toolCalls); got != "exact explanation" {
		t.Fatalf("tool-call explanation = %q", got)
	}
	messages := []providers.Message{
		{Role: "assistant", Content: "ignore"},
		{Role: "user", Content: "  "},
		{Role: "user", Content: " exact question "},
	}
	wantContinuation := utils.ToolFeedbackContinuationHint + ": exact question"
	if got := toolFeedbackExplanationForToolCall(nil, providers.ToolCall{}, messages); got != wantContinuation {
		t.Fatalf("nil-response feedback explanation = %q, want %q", got, wantContinuation)
	}
	if got := toolFeedbackExplanationForToolCall(
		&providers.LLMResponse{},
		providers.ToolCall{},
		messages,
	); got != wantContinuation {
		t.Fatalf("empty-response feedback explanation = %q, want %q", got, wantContinuation)
	}

	if got := hookDeniedToolContent("denied", "\n\t"); got != "denied" {
		t.Fatalf("empty sanitized denial = %q", got)
	}
	if got := hookDeniedToolContent("denied", " because\nprivate "); got != "denied: because private" {
		t.Fatalf("sanitized denial = %q", got)
	}
	fields := map[string]any{"existing": true}
	appendEventContextFields(fields, nil)
	if !reflect.DeepEqual(fields, map[string]any{"existing": true}) {
		t.Fatalf("nil turn context mutated fields: %#v", fields)
	}

	if aliases := buildSessionAliases("canonical"); aliases != nil {
		t.Fatalf("zero aliases = %#v", aliases)
	}
	aliases := buildSessionAliases(
		"canonical",
		"",
		" canonical ",
		"second",
		"second",
		"third",
	)
	if !reflect.DeepEqual(aliases, []string{"second", "third"}) {
		t.Fatalf("session aliases = %#v", aliases)
	}

	for _, test := range []struct {
		filename    string
		contentType string
		want        string
	}{
		{filename: "vector.svg", contentType: "image/svg+xml", want: "file"},
		{filename: "photo.bin", contentType: "image/png", want: "image"},
		{filename: "sound.bin", contentType: "application/ogg", want: "audio"},
		{filename: "movie.bin", contentType: "video/mp4", want: "video"},
		{filename: "photo.webp", want: "image"},
		{filename: "sound.opus", want: "audio"},
		{filename: "movie.mkv", want: "video"},
		{filename: "document.txt", want: "file"},
	} {
		if got := inferMediaType(test.filename, test.contentType); got != test.want {
			t.Errorf("inferMediaType(%q, %q) = %q, want %q",
				test.filename, test.contentType, got, test.want)
		}
	}

	if got := sideQuestionResponseContent(nil); got != "" {
		t.Fatalf("nil side-question response = %q", got)
	}
	if got := sideQuestionResponseContent(&providers.LLMResponse{Content: " exact "}); got != " exact " {
		t.Fatalf("content side-question response = %q", got)
	}
	if got := sideQuestionResponseContent(&providers.LLMResponse{Reasoning: "reason"}); got != "reason" {
		t.Fatalf("reasoning side-question response = %q", got)
	}
	if got := responseReasoningContent(nil); got != "" {
		t.Fatalf("nil reasoning response = %q", got)
	}
	if got := responseReasoningContent(&providers.LLMResponse{ReasoningContent: "details"}); got != "details" {
		t.Fatalf("reasoning-content response = %q", got)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepWithContext(canceled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled sleep error = %v", err)
	}
	if err := sleepWithContext(context.Background(), 0); err != nil {
		t.Fatalf("zero-duration sleep error = %v", err)
	}

	commandErr := errors.New("command failed")
	if got := mapCommandError(
		commands.ExecuteResult{Err: commandErr},
	); !strings.Contains(
		got,
		"Failed to execute command",
	) {
		t.Fatalf("anonymous command error = %q", got)
	}
	if got := mapCommandError(
		commands.ExecuteResult{Command: "status", Err: commandErr},
	); !strings.Contains(
		got,
		"Failed to execute /status",
	) {
		t.Fatalf("named command error = %q", got)
	}

	definitions := []providers.ToolDefinition{
		{Function: providers.ToolFunctionDefinition{Name: "web_search"}},
		{Function: providers.ToolFunctionDefinition{Name: "read_file"}},
	}
	filtered := filterClientWebSearch(definitions)
	if len(filtered) != 1 || filtered[0].Function.Name != "read_file" {
		t.Fatalf("filtered client web-search definitions = %#v", filtered)
	}
}
