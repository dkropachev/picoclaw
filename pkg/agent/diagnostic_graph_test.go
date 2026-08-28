package agent

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

var (
	_ func([]providers.Message) logger.Observation        = observeAgentMessageGraph
	_ func([]providers.ToolDefinition) logger.Observation = observeAgentToolDefinitions
)

func TestAgentDiagnosticProtocolFieldManifest(t *testing.T) {
	tests := []struct {
		value  any
		fields []string
	}{
		{
			value: protocoltypes.Message{},
			fields: []string{
				"Role", "Content", "ModelName", "CreatedAt", "Media",
				"Attachments", "Parts", "ReasoningContent", "SystemParts",
				"ToolCalls", "ToolCallID", "PromptLayer", "PromptSlot",
				"PromptSource",
			},
		},
		{
			value:  protocoltypes.Attachment{},
			fields: []string{"Type", "Ref", "URL", "Filename", "ContentType"},
		},
		{
			value:  protocoltypes.PromptPart{},
			fields: []string{"Type", "Text", "URI", "MIMEType", "Filename", "Detail"},
		},
		{
			value: protocoltypes.ContentBlock{},
			fields: []string{
				"Type", "Text", "CacheControl", "PromptLayer", "PromptSlot", "PromptSource",
			},
		},
		{value: protocoltypes.CacheControl{}, fields: []string{"Type"}},
		{
			value: protocoltypes.ToolCall{},
			fields: []string{
				"ID", "Type", "Function", "Name", "Arguments", "ThoughtSignature", "ExtraContent",
			},
		},
		{
			value:  protocoltypes.FunctionCall{},
			fields: []string{"Name", "Arguments", "ThoughtSignature"},
		},
		{
			value:  protocoltypes.ExtraContent{},
			fields: []string{"Google", "ToolFeedbackExplanation"},
		},
		{value: protocoltypes.GoogleExtra{}, fields: []string{"ThoughtSignature"}},
		{
			value: protocoltypes.ToolDefinition{},
			fields: []string{
				"Type", "Function", "PromptLayer", "PromptSlot", "PromptSource",
			},
		},
		{
			value:  protocoltypes.ToolFunctionDefinition{},
			fields: []string{"Name", "Description", "Parameters"},
		},
	}

	for _, test := range tests {
		typeOf := reflect.TypeOf(test.value)
		t.Run(typeOf.Name(), func(t *testing.T) {
			got := make([]string, typeOf.NumField())
			for index := range got {
				got[index] = typeOf.Field(index).Name
			}
			if !reflect.DeepEqual(got, test.fields) {
				t.Fatalf("%s field manifest changed\n got: %v\nwant: %v", typeOf, got, test.fields)
			}
		})
	}
}

func TestAgentDiagnosticGraphGoldenVectors(t *testing.T) {
	if agentMessageGraphDiagnosticFrame != "picoclaw.agent.message_graph.v1" ||
		agentToolDefinitionsDiagnosticFrame != "picoclaw.agent.tool_definitions.v1" {
		t.Fatalf(
			"diagnostic frame versions changed: message=%q tools=%q",
			agentMessageGraphDiagnosticFrame,
			agentToolDefinitionsDiagnosticFrame,
		)
	}

	tests := []struct {
		name string
		got  logger.Observation
		want string
	}{
		{
			name: "nil message graph",
			got:  observeAgentMessageGraph(nil),
			want: "sha256:9860cb0a341b3b51a24c507e95339ea83e6468e5f2732906a94ca22cfb66d137",
		},
		{
			name: "empty message graph",
			got:  observeAgentMessageGraph([]providers.Message{}),
			want: "sha256:58c0d2718a25006e1e8ee30c29742949a637d0e4ae8403f3362ed4225811d140",
		},
		{
			name: "full message graph",
			got:  observeAgentMessageGraph([]providers.Message{agentDiagnosticFixtureMessage()}),
			want: "sha256:c09980bec1f478233e7c843f657ee6046710d481de7f6724e594f74b4e185095",
		},
		{
			name: "nil tool definitions",
			got:  observeAgentToolDefinitions(nil),
			want: "sha256:c31e0d9fc9d32dfd5a74b4f772d80d68db9462adfe697d1126f1cf3a4d52d151",
		},
		{
			name: "empty tool definitions",
			got:  observeAgentToolDefinitions([]providers.ToolDefinition{}),
			want: "sha256:26b8ae6fde8ccb13097c0b16577aaf7e66dbac2c8436f0e2f9c7979ef16b34be",
		},
		{
			name: "full tool definitions",
			got: observeAgentToolDefinitions([]providers.ToolDefinition{
				agentDiagnosticFixtureDefinition(),
			}),
			want: "sha256:a22a88fca34389b5ab42d0c13ff961768868ec9d46b67ccd123ca53a9bce4319",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got.State != "complete" || test.got.Digest != test.want {
				t.Fatalf("observation = %#v; want complete digest %q", test.got, test.want)
			}
		})
	}
}

func TestAgentDiagnosticEveryMessageProtocolFieldPerturbsDigest(t *testing.T) {
	direct := map[string]func(*providers.Message){
		"Role": func(value *providers.Message) { value.Role += "-changed" },
		"Content": func(value *providers.Message) {
			value.Content += "-changed"
		},
		"ModelName": func(value *providers.Message) { value.ModelName += "-changed" },
		"CreatedAt": func(value *providers.Message) {
			changed := value.CreatedAt.Add(time.Second)
			value.CreatedAt = &changed
		},
		"Media": func(value *providers.Message) { value.Media[0] += "-changed" },
		"Attachments": func(value *providers.Message) {
			value.Attachments = append(value.Attachments, providers.Attachment{Type: "changed"})
		},
		"Parts": func(value *providers.Message) {
			value.Parts = append(value.Parts, providers.PromptPart{Type: "changed"})
		},
		"ReasoningContent": func(value *providers.Message) {
			value.ReasoningContent += "-changed"
		},
		"SystemParts": func(value *providers.Message) {
			value.SystemParts = append(value.SystemParts, providers.ContentBlock{Type: "changed"})
		},
		"ToolCalls": func(value *providers.Message) {
			value.ToolCalls = append(value.ToolCalls, providers.ToolCall{ID: "changed"})
		},
		"ToolCallID":   func(value *providers.Message) { value.ToolCallID += "-changed" },
		"PromptLayer":  func(value *providers.Message) { value.PromptLayer += "-changed" },
		"PromptSlot":   func(value *providers.Message) { value.PromptSlot += "-changed" },
		"PromptSource": func(value *providers.Message) { value.PromptSource += "-changed" },
	}
	assertAgentMessageFieldPerturbations(t, reflect.TypeOf(protocoltypes.Message{}), direct)

	attachments := map[string]func(*providers.Message){
		"Type":        func(value *providers.Message) { value.Attachments[0].Type += "-changed" },
		"Ref":         func(value *providers.Message) { value.Attachments[0].Ref += "-changed" },
		"URL":         func(value *providers.Message) { value.Attachments[0].URL += "-changed" },
		"Filename":    func(value *providers.Message) { value.Attachments[0].Filename += "-changed" },
		"ContentType": func(value *providers.Message) { value.Attachments[0].ContentType += "-changed" },
	}
	assertAgentMessageFieldPerturbations(t, reflect.TypeOf(protocoltypes.Attachment{}), attachments)

	parts := map[string]func(*providers.Message){
		"Type":     func(value *providers.Message) { value.Parts[0].Type += "-changed" },
		"Text":     func(value *providers.Message) { value.Parts[0].Text += "-changed" },
		"URI":      func(value *providers.Message) { value.Parts[0].URI += "-changed" },
		"MIMEType": func(value *providers.Message) { value.Parts[0].MIMEType += "-changed" },
		"Filename": func(value *providers.Message) { value.Parts[0].Filename += "-changed" },
		"Detail":   func(value *providers.Message) { value.Parts[0].Detail += "-changed" },
	}
	assertAgentMessageFieldPerturbations(t, reflect.TypeOf(protocoltypes.PromptPart{}), parts)

	blocks := map[string]func(*providers.Message){
		"Type": func(value *providers.Message) { value.SystemParts[0].Type += "-changed" },
		"Text": func(value *providers.Message) { value.SystemParts[0].Text += "-changed" },
		"CacheControl": func(value *providers.Message) {
			value.SystemParts[0].CacheControl = nil
		},
		"PromptLayer": func(value *providers.Message) {
			value.SystemParts[0].PromptLayer += "-changed"
		},
		"PromptSlot": func(value *providers.Message) {
			value.SystemParts[0].PromptSlot += "-changed"
		},
		"PromptSource": func(value *providers.Message) {
			value.SystemParts[0].PromptSource += "-changed"
		},
	}
	assertAgentMessageFieldPerturbations(t, reflect.TypeOf(protocoltypes.ContentBlock{}), blocks)
	assertAgentMessageFieldPerturbations(
		t,
		reflect.TypeOf(protocoltypes.CacheControl{}),
		map[string]func(*providers.Message){
			"Type": func(value *providers.Message) {
				value.SystemParts[0].CacheControl.Type += "-changed"
			},
		},
	)

	calls := map[string]func(*providers.Message){
		"ID":       func(value *providers.Message) { value.ToolCalls[0].ID += "-changed" },
		"Type":     func(value *providers.Message) { value.ToolCalls[0].Type += "-changed" },
		"Function": func(value *providers.Message) { value.ToolCalls[0].Function = nil },
		"Name":     func(value *providers.Message) { value.ToolCalls[0].Name += "-changed" },
		"Arguments": func(value *providers.Message) {
			value.ToolCalls[0].Arguments["changed"] = true
		},
		"ThoughtSignature": func(value *providers.Message) {
			value.ToolCalls[0].ThoughtSignature += "-changed"
		},
		"ExtraContent": func(value *providers.Message) { value.ToolCalls[0].ExtraContent = nil },
	}
	assertAgentMessageFieldPerturbations(t, reflect.TypeOf(protocoltypes.ToolCall{}), calls)

	functionCalls := map[string]func(*providers.Message){
		"Name": func(value *providers.Message) {
			value.ToolCalls[0].Function.Name += "-changed"
		},
		"Arguments": func(value *providers.Message) {
			value.ToolCalls[0].Function.Arguments += "-changed"
		},
		"ThoughtSignature": func(value *providers.Message) {
			value.ToolCalls[0].Function.ThoughtSignature += "-changed"
		},
	}
	assertAgentMessageFieldPerturbations(t, reflect.TypeOf(protocoltypes.FunctionCall{}), functionCalls)

	extraContent := map[string]func(*providers.Message){
		"Google": func(value *providers.Message) {
			value.ToolCalls[0].ExtraContent.Google = nil
		},
		"ToolFeedbackExplanation": func(value *providers.Message) {
			value.ToolCalls[0].ExtraContent.ToolFeedbackExplanation += "-changed"
		},
	}
	assertAgentMessageFieldPerturbations(t, reflect.TypeOf(protocoltypes.ExtraContent{}), extraContent)
	assertAgentMessageFieldPerturbations(
		t,
		reflect.TypeOf(protocoltypes.GoogleExtra{}),
		map[string]func(*providers.Message){
			"ThoughtSignature": func(value *providers.Message) {
				value.ToolCalls[0].ExtraContent.Google.ThoughtSignature += "-changed"
			},
		},
	)
}

func TestAgentDiagnosticEveryToolProtocolFieldPerturbsDigest(t *testing.T) {
	direct := map[string]func(*providers.ToolDefinition){
		"Type": func(value *providers.ToolDefinition) { value.Type += "-changed" },
		"Function": func(value *providers.ToolDefinition) {
			value.Function = providers.ToolFunctionDefinition{Name: "changed"}
		},
		"PromptLayer": func(value *providers.ToolDefinition) { value.PromptLayer += "-changed" },
		"PromptSlot":  func(value *providers.ToolDefinition) { value.PromptSlot += "-changed" },
		"PromptSource": func(value *providers.ToolDefinition) {
			value.PromptSource += "-changed"
		},
	}
	assertAgentToolFieldPerturbations(t, reflect.TypeOf(protocoltypes.ToolDefinition{}), direct)

	function := map[string]func(*providers.ToolDefinition){
		"Name": func(value *providers.ToolDefinition) { value.Function.Name += "-changed" },
		"Description": func(value *providers.ToolDefinition) {
			value.Function.Description += "-changed"
		},
		"Parameters": func(value *providers.ToolDefinition) {
			value.Function.Parameters["changed"] = true
		},
	}
	assertAgentToolFieldPerturbations(
		t,
		reflect.TypeOf(protocoltypes.ToolFunctionDefinition{}),
		function,
	)
}

func assertAgentMessageFieldPerturbations(
	t *testing.T,
	typeOf reflect.Type,
	mutators map[string]func(*providers.Message),
) {
	t.Helper()
	assertExactPerturbationManifest(t, typeOf, mutators)
	base := observeAgentMessageGraph([]providers.Message{agentDiagnosticFixtureMessage()})
	if base.State != "complete" {
		t.Fatalf("base message observation unavailable: %#v", base)
	}
	for field, mutate := range mutators {
		t.Run(typeOf.Name()+"/"+field, func(t *testing.T) {
			message := agentDiagnosticFixtureMessage()
			mutate(&message)
			changed := observeAgentMessageGraph([]providers.Message{message})
			if changed.State != "complete" || changed.Digest == base.Digest {
				t.Fatalf(
					"field %s.%s did not perturb complete digest: base=%#v changed=%#v",
					typeOf,
					field,
					base,
					changed,
				)
			}
		})
	}
}

func assertAgentToolFieldPerturbations(
	t *testing.T,
	typeOf reflect.Type,
	mutators map[string]func(*providers.ToolDefinition),
) {
	t.Helper()
	assertExactPerturbationManifest(t, typeOf, mutators)
	base := observeAgentToolDefinitions([]providers.ToolDefinition{agentDiagnosticFixtureDefinition()})
	if base.State != "complete" {
		t.Fatalf("base tool observation unavailable: %#v", base)
	}
	for field, mutate := range mutators {
		t.Run(typeOf.Name()+"/"+field, func(t *testing.T) {
			definition := agentDiagnosticFixtureDefinition()
			mutate(&definition)
			changed := observeAgentToolDefinitions([]providers.ToolDefinition{definition})
			if changed.State != "complete" || changed.Digest == base.Digest {
				t.Fatalf(
					"field %s.%s did not perturb complete digest: base=%#v changed=%#v",
					typeOf,
					field,
					base,
					changed,
				)
			}
		})
	}
}

func assertExactPerturbationManifest[T any](
	t *testing.T,
	typeOf reflect.Type,
	mutators map[string]T,
) {
	t.Helper()
	if len(mutators) != typeOf.NumField() {
		t.Fatalf("%s perturbation count = %d, want %d", typeOf, len(mutators), typeOf.NumField())
	}
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index).Name
		if _, ok := mutators[field]; !ok {
			t.Fatalf("%s field %q has no perturbation", typeOf, field)
		}
	}
}

func agentDiagnosticFixtureMessage() providers.Message {
	createdAt := time.Date(2026, time.August, 27, 13, 14, 15, 987654321,
		time.FixedZone("fixture-zone", -4*60*60))
	return providers.Message{
		Role:      "assistant-role",
		Content:   "message-content\nwith-control\tand-unicode-雪",
		ModelName: "model-name",
		CreatedAt: &createdAt,
		Media:     []string{"media-one", "media-two"},
		Attachments: []providers.Attachment{
			{
				Type:        "file",
				Ref:         "attachment-ref",
				URL:         "https://example.invalid/attachment",
				Filename:    "attachment.bin",
				ContentType: "application/octet-stream",
			},
		},
		Parts: []providers.PromptPart{
			{
				Type:     "image",
				Text:     "part-text",
				URI:      "media://part-ref",
				MIMEType: "image/png",
				Filename: "part.png",
				Detail:   "high",
			},
		},
		ReasoningContent: "reasoning-content",
		SystemParts: []providers.ContentBlock{
			{
				Type:         "text",
				Text:         "system-block-text",
				CacheControl: &providers.CacheControl{Type: "ephemeral"},
				PromptLayer:  "system-layer",
				PromptSlot:   "system-slot",
				PromptSource: "system-source",
			},
		},
		ToolCalls: []providers.ToolCall{
			{
				ID:   "tool-call-id",
				Type: "function",
				Function: &providers.FunctionCall{
					Name:             "function-name",
					Arguments:        `{"raw":"arguments"}`,
					ThoughtSignature: "function-thought-signature",
				},
				Name: "canonical-tool-name",
				Arguments: map[string]any{
					"alpha":  int64(-7),
					"nested": []any{true, nil, json.Number("1.25e+2")},
				},
				ThoughtSignature: "tool-thought-signature",
				ExtraContent: &providers.ExtraContent{
					Google: &providers.GoogleExtra{
						ThoughtSignature: "google-thought-signature",
					},
					ToolFeedbackExplanation: "feedback-explanation",
				},
			},
		},
		ToolCallID:   "result-tool-call-id",
		PromptLayer:  "message-layer",
		PromptSlot:   "message-slot",
		PromptSource: "message-source",
	}
}

func agentDiagnosticFixtureDefinition() providers.ToolDefinition {
	return providers.ToolDefinition{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name:        "definition-name",
			Description: "definition-description\nwith-unicode-雪",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"count": map[string]any{"type": "integer", "minimum": int64(-2)},
					"mode":  []any{"one", json.Number("2.0"), false},
				},
				"required": []string{"count"},
			},
		},
		PromptLayer:  "tool-layer",
		PromptSlot:   "tool-slot",
		PromptSource: "tool-source",
	}
}

func TestAgentDiagnosticNilEmptyAndPointerPresence(t *testing.T) {
	messageCases := []struct {
		name  string
		left  func() providers.Message
		right func() providers.Message
	}{
		{
			name:  "media",
			left:  func() providers.Message { return providers.Message{Media: nil} },
			right: func() providers.Message { return providers.Message{Media: []string{}} },
		},
		{
			name: "attachments",
			left: func() providers.Message {
				return providers.Message{Attachments: nil}
			},
			right: func() providers.Message {
				return providers.Message{Attachments: []providers.Attachment{}}
			},
		},
		{
			name:  "parts",
			left:  func() providers.Message { return providers.Message{Parts: nil} },
			right: func() providers.Message { return providers.Message{Parts: []providers.PromptPart{}} },
		},
		{
			name: "system parts",
			left: func() providers.Message {
				return providers.Message{SystemParts: nil}
			},
			right: func() providers.Message {
				return providers.Message{SystemParts: []providers.ContentBlock{}}
			},
		},
		{
			name: "tool calls",
			left: func() providers.Message {
				return providers.Message{ToolCalls: nil}
			},
			right: func() providers.Message {
				return providers.Message{ToolCalls: []providers.ToolCall{}}
			},
		},
		{
			name:  "created at pointer",
			left:  func() providers.Message { return providers.Message{CreatedAt: nil} },
			right: func() providers.Message { value := time.Time{}; return providers.Message{CreatedAt: &value} },
		},
		{
			name: "cache control pointer",
			left: func() providers.Message {
				return providers.Message{SystemParts: []providers.ContentBlock{{CacheControl: nil}}}
			},
			right: func() providers.Message {
				return providers.Message{SystemParts: []providers.ContentBlock{{
					CacheControl: &providers.CacheControl{},
				}}}
			},
		},
		{
			name: "function pointer",
			left: func() providers.Message {
				return providers.Message{ToolCalls: []providers.ToolCall{{Function: nil}}}
			},
			right: func() providers.Message {
				return providers.Message{ToolCalls: []providers.ToolCall{{
					Function: &providers.FunctionCall{},
				}}}
			},
		},
		{
			name: "arguments map",
			left: func() providers.Message {
				return providers.Message{ToolCalls: []providers.ToolCall{{Arguments: nil}}}
			},
			right: func() providers.Message {
				return providers.Message{ToolCalls: []providers.ToolCall{{
					Arguments: map[string]any{},
				}}}
			},
		},
		{
			name: "extra content pointer",
			left: func() providers.Message {
				return providers.Message{ToolCalls: []providers.ToolCall{{ExtraContent: nil}}}
			},
			right: func() providers.Message {
				return providers.Message{ToolCalls: []providers.ToolCall{{
					ExtraContent: &providers.ExtraContent{},
				}}}
			},
		},
		{
			name: "google pointer",
			left: func() providers.Message {
				return providers.Message{ToolCalls: []providers.ToolCall{{
					ExtraContent: &providers.ExtraContent{Google: nil},
				}}}
			},
			right: func() providers.Message {
				return providers.Message{ToolCalls: []providers.ToolCall{{
					ExtraContent: &providers.ExtraContent{Google: &providers.GoogleExtra{}},
				}}}
			},
		},
	}

	assertDifferentCompleteAgentObservations(
		t,
		"top-level message nil/empty",
		observeAgentMessageGraph(nil),
		observeAgentMessageGraph([]providers.Message{}),
	)
	for _, test := range messageCases {
		t.Run(test.name, func(t *testing.T) {
			assertDifferentCompleteAgentObservations(
				t,
				test.name,
				observeAgentMessageGraph([]providers.Message{test.left()}),
				observeAgentMessageGraph([]providers.Message{test.right()}),
			)
		})
	}

	assertDifferentCompleteAgentObservations(
		t,
		"top-level tool definitions nil/empty",
		observeAgentToolDefinitions(nil),
		observeAgentToolDefinitions([]providers.ToolDefinition{}),
	)
	leftDefinition := providers.ToolDefinition{
		Function: providers.ToolFunctionDefinition{Parameters: nil},
	}
	rightDefinition := providers.ToolDefinition{
		Function: providers.ToolFunctionDefinition{Parameters: map[string]any{}},
	}
	assertDifferentCompleteAgentObservations(
		t,
		"tool parameters map nil/empty",
		observeAgentToolDefinitions([]providers.ToolDefinition{leftDefinition}),
		observeAgentToolDefinitions([]providers.ToolDefinition{rightDefinition}),
	)
}

func TestAgentDiagnosticPreservesSequenceOrderAndFraming(t *testing.T) {
	first := agentDiagnosticFixtureMessage()
	second := agentDiagnosticFixtureMessage()
	second.Role = "second-role"
	assertDifferentCompleteAgentObservations(
		t,
		"message order",
		observeAgentMessageGraph([]providers.Message{first, second}),
		observeAgentMessageGraph([]providers.Message{second, first}),
	)

	messageOrderCases := []struct {
		name   string
		mutate func(*providers.Message)
	}{
		{name: "media", mutate: func(value *providers.Message) { swap(value.Media) }},
		{name: "attachments", mutate: func(value *providers.Message) { swap(value.Attachments) }},
		{name: "parts", mutate: func(value *providers.Message) { swap(value.Parts) }},
		{name: "system parts", mutate: func(value *providers.Message) { swap(value.SystemParts) }},
		{name: "tool calls", mutate: func(value *providers.Message) { swap(value.ToolCalls) }},
	}
	for _, test := range messageOrderCases {
		t.Run(test.name, func(t *testing.T) {
			left := agentDiagnosticFixtureMessageWithPairs()
			right := agentDiagnosticFixtureMessageWithPairs()
			test.mutate(&right)
			assertDifferentCompleteAgentObservations(
				t,
				test.name,
				observeAgentMessageGraph([]providers.Message{left}),
				observeAgentMessageGraph([]providers.Message{right}),
			)
		})
	}

	firstDefinition := agentDiagnosticFixtureDefinition()
	secondDefinition := agentDiagnosticFixtureDefinition()
	secondDefinition.Function.Name = "second-definition"
	assertDifferentCompleteAgentObservations(
		t,
		"tool definition order",
		observeAgentToolDefinitions([]providers.ToolDefinition{firstDefinition, secondDefinition}),
		observeAgentToolDefinitions([]providers.ToolDefinition{secondDefinition, firstDefinition}),
	)

	leftFraming := providers.Message{Media: []string{"ab", "c"}}
	rightFraming := providers.Message{Media: []string{"a", "bc"}}
	assertDifferentCompleteAgentObservations(
		t,
		"length framing",
		observeAgentMessageGraph([]providers.Message{leftFraming}),
		observeAgentMessageGraph([]providers.Message{rightFraming}),
	)
}

func TestAgentDiagnosticMessageRoleCountsAreExactAndClosed(t *testing.T) {
	messages := []providers.Message{
		{Role: "system"},
		{Role: "user"},
		{Role: "user"},
		{Role: "assistant"},
		{Role: "tool"},
		{Role: "developer"},
		{Role: "USER"},
		{Role: ""},
	}
	want := agentDiagnosticMessageRoleCounts{
		system: 1, user: 2, assistant: 1, tool: 1, unknown: 3,
	}
	if got := countAgentDiagnosticMessageRoles(messages); got != want {
		t.Fatalf("role counts = %#v, want %#v", got, want)
	}
	if got := countAgentDiagnosticMessageRoles(nil); got != (agentDiagnosticMessageRoleCounts{}) {
		t.Fatalf("nil role counts = %#v, want zero", got)
	}
}

func TestAgentDiagnosticCreatedAtExcludesMonotonicClock(t *testing.T) {
	withMonotonic := time.Now()
	withoutMonotonic := time.Unix(
		withMonotonic.Unix(),
		int64(withMonotonic.Nanosecond()),
	).In(withMonotonic.Location())
	left := observeAgentMessageGraph([]providers.Message{{CreatedAt: &withMonotonic}})
	right := observeAgentMessageGraph([]providers.Message{{CreatedAt: &withoutMonotonic}})
	if left.State != "complete" || right.State != "complete" || left.Digest != right.Digest {
		t.Fatalf("monotonic-only state changed digest: with=%#v without=%#v", left, right)
	}

	instant := time.Unix(1_777_777_777, 123456789)
	west := instant.In(time.FixedZone("west-name", -4*60*60))
	east := instant.In(time.FixedZone("east-name", 2*60*60))
	assertDifferentCompleteAgentObservations(
		t,
		"provider-visible zone offset",
		observeAgentMessageGraph([]providers.Message{{CreatedAt: &west}}),
		observeAgentMessageGraph([]providers.Message{{CreatedAt: &east}}),
	)

	westRenamed := instant.In(time.FixedZone("different-process-local-name", -4*60*60))
	renameObservation := observeAgentMessageGraph([]providers.Message{{CreatedAt: &westRenamed}})
	westObservation := observeAgentMessageGraph([]providers.Message{{CreatedAt: &west}})
	if renameObservation.State != "complete" || renameObservation.Digest != westObservation.Digest {
		t.Fatalf("process-local zone name changed digest: west=%#v renamed=%#v", westObservation, renameObservation)
	}
}

func TestAgentDiagnosticDeterminismAndMapOrder(t *testing.T) {
	message := agentDiagnosticFixtureMessage()
	definition := agentDiagnosticFixtureDefinition()
	wantMessage := observeAgentMessageGraph([]providers.Message{message})
	wantTools := observeAgentToolDefinitions([]providers.ToolDefinition{definition})
	for iteration := 0; iteration < 200; iteration++ {
		gotMessage := observeAgentMessageGraph([]providers.Message{message})
		if gotMessage.State != "complete" || gotMessage.Digest != wantMessage.Digest {
			t.Fatalf("message iteration %d = %#v, want %#v", iteration, gotMessage, wantMessage)
		}
		gotTools := observeAgentToolDefinitions([]providers.ToolDefinition{definition})
		if gotTools.State != "complete" || gotTools.Digest != wantTools.Digest {
			t.Fatalf("tool iteration %d = %#v, want %#v", iteration, gotTools, wantTools)
		}
	}

	invalidUTF8Key := string([]byte{0xff, 0x00, 'k'})
	argumentsFirst := make(map[string]any)
	argumentsFirst[invalidUTF8Key] = json.Number("1.0")
	argumentsFirst["\x00"] = []any{int64(2), false}
	argumentsSecond := make(map[string]any)
	argumentsSecond["\x00"] = []any{int64(2), false}
	argumentsSecond[invalidUTF8Key] = json.Number("1.0")
	messageFirst := providers.Message{ToolCalls: []providers.ToolCall{{Arguments: argumentsFirst}}}
	messageSecond := providers.Message{ToolCalls: []providers.ToolCall{{Arguments: argumentsSecond}}}
	assertSameCompleteAgentObservations(
		t,
		"argument map order",
		observeAgentMessageGraph([]providers.Message{messageFirst}),
		observeAgentMessageGraph([]providers.Message{messageSecond}),
	)

	schemaFirst := make(map[string]any)
	schemaFirst[invalidUTF8Key] = []any{json.Number("-2e3"), true}
	schemaFirst["\x00"] = "value"
	schemaSecond := make(map[string]any)
	schemaSecond["\x00"] = "value"
	schemaSecond[invalidUTF8Key] = []any{json.Number("-2e3"), true}
	assertSameCompleteAgentObservations(
		t,
		"schema map order",
		observeAgentToolDefinitions([]providers.ToolDefinition{{
			Function: providers.ToolFunctionDefinition{Parameters: schemaFirst},
		}}),
		observeAgentToolDefinitions([]providers.ToolDefinition{{
			Function: providers.ToolFunctionDefinition{Parameters: schemaSecond},
		}}),
	)
}

func TestAgentDiagnosticSourceMutationDoesNotAliasObservation(t *testing.T) {
	message := agentDiagnosticFixtureMessage()
	definition := agentDiagnosticFixtureDefinition()
	messages := []providers.Message{message}
	definitions := []providers.ToolDefinition{definition}
	messageSnapshot := cloneProviderMessages(messages)
	definitionSnapshot := cloneToolDefinitions(definitions)
	messageObservation := observeAgentMessageGraph(messages)
	toolObservation := observeAgentToolDefinitions(definitions)
	if messageObservation.State != "complete" || toolObservation.State != "complete" {
		t.Fatalf("fixture observation unavailable: message=%#v tool=%#v", messageObservation, toolObservation)
	}
	if !reflect.DeepEqual(messages, messageSnapshot) ||
		!reflect.DeepEqual(definitions, definitionSnapshot) {
		t.Fatalf(
			"diagnostic projection mutated input\nmessages: %#v\nwant: %#v\ndefinitions: %#v\nwant: %#v",
			messages,
			messageSnapshot,
			definitions,
			definitionSnapshot,
		)
	}

	messageDigest := messageObservation.Digest
	toolDigest := toolObservation.Digest
	done := make(chan struct{})
	go func() {
		message.Media[0] = "mutated-media"
		message.Attachments[0].Ref = "mutated-ref"
		message.ToolCalls[0].Arguments["nested"] = "mutated-arguments"
		message.ToolCalls[0].ExtraContent.Google.ThoughtSignature = "mutated-google"
		definition.Function.Parameters["properties"] = "mutated-schema"
		close(done)
	}()
	for range 1_000 {
		if messageObservation.Digest != messageDigest || toolObservation.Digest != toolDigest {
			t.Fatal("returned observation retained mutable source storage")
		}
	}
	<-done
	changedMessage := observeAgentMessageGraph(messages)
	if changedMessage.State != "complete" || changedMessage.Digest == messageDigest {
		t.Fatalf("message source mutation not reflected in a fresh call: %#v", changedMessage)
	}
	changedTools := observeAgentToolDefinitions(definitions)
	if changedTools.State != "complete" || changedTools.Digest == toolDigest {
		t.Fatalf("tool source mutation not reflected in a fresh call: %#v", changedTools)
	}
}

var agentDiagnosticHostileMethodCalls atomic.Int64

type agentDiagnosticHostileString string

func (agentDiagnosticHostileString) String() string {
	agentDiagnosticHostileMethodCalls.Add(1)
	panic("agent diagnostic hostile String method invoked")
}

func (agentDiagnosticHostileString) MarshalJSON() ([]byte, error) {
	agentDiagnosticHostileMethodCalls.Add(1)
	panic("agent diagnostic hostile MarshalJSON method invoked")
}

type agentDiagnosticHostileMap map[agentDiagnosticHostileString]agentDiagnosticHostileString

func (agentDiagnosticHostileMap) MarshalJSON() ([]byte, error) {
	agentDiagnosticHostileMethodCalls.Add(1)
	panic("agent diagnostic hostile map MarshalJSON method invoked")
}

type agentDiagnosticHostileSlice []agentDiagnosticHostileString

func (agentDiagnosticHostileSlice) MarshalJSON() ([]byte, error) {
	agentDiagnosticHostileMethodCalls.Add(1)
	panic("agent diagnostic hostile slice MarshalJSON method invoked")
}

type agentDiagnosticPointerSemanticString string

func (*agentDiagnosticPointerSemanticString) MarshalJSON() ([]byte, error) {
	agentDiagnosticHostileMethodCalls.Add(1)
	panic("agent diagnostic pointer MarshalJSON method invoked")
}

type agentDiagnosticHostileError struct {
	calls *atomic.Int64
}

func (value agentDiagnosticHostileError) Error() string {
	value.calls.Add(1)
	panic("agent diagnostic hostile Error method invoked")
}

func TestAgentDiagnosticDetachedNormalizationDoesNotInvokeMethods(t *testing.T) {
	agentDiagnosticHostileMethodCalls.Store(0)
	hostileGraph := map[string]any{
		"map": agentDiagnosticHostileMap{
			agentDiagnosticHostileString("hostile-key-canary"): agentDiagnosticHostileString("hostile-value-canary"),
		},
		"slice": agentDiagnosticHostileSlice{
			agentDiagnosticHostileString("hostile-slice-canary"),
		},
		"scalar": agentDiagnosticHostileString("hostile-scalar-canary"),
	}
	message := providers.Message{ToolCalls: []providers.ToolCall{{Arguments: hostileGraph}}}
	definition := providers.ToolDefinition{
		Function: providers.ToolFunctionDefinition{Parameters: hostileGraph},
	}
	messageObservation := observeAgentMessageGraph([]providers.Message{message})
	toolObservation := observeAgentToolDefinitions([]providers.ToolDefinition{definition})
	assertUnavailableAgentObservation(t, messageObservation)
	assertUnavailableAgentObservation(t, toolObservation)
	if normalized := normalizeAgentDiagnosticValue(hostileGraph); reflect.TypeOf(normalized) !=
		reflect.TypeOf(agentDiagnosticUnsupported{}) {
		t.Fatalf("method-bearing normalized graph type = %T, want sealed unsupported", normalized)
	}
	records, raw := captureP015HookRecords(t, func() {
		logger.DebugSensitiveCF(
			logger.NewDiagnosticPolicy(true, logger.DEBUG),
			logger.ComponentAgent,
			logger.DiagnosticMessageToolArguments,
			logger.NewSafeFields(),
			logger.SensitivityToolArguments,
			logger.ObservationDomainToolArguments,
			normalizeAgentDiagnosticValue(hostileGraph),
		)
	})
	if len(records) != 1 || records[0]["tool_arguments_state"] != "unavailable" {
		t.Fatalf("method-bearing sensitive projection = %#v, want unavailable", records)
	}
	for _, canary := range []string{
		"hostile-key-canary", "hostile-value-canary", "hostile-slice-canary", "hostile-scalar-canary",
	} {
		if strings.Contains(fmt.Sprintf("%#v %#v", messageObservation, toolObservation), canary) {
			t.Fatalf("raw canary %q escaped safe observations", canary)
		}
	}
	assertP015CanariesAbsent(
		t,
		raw,
		"hostile-key-canary",
		"hostile-value-canary",
		"hostile-slice-canary",
		"hostile-scalar-canary",
	)
	if calls := agentDiagnosticHostileMethodCalls.Load(); calls != 0 {
		t.Fatalf("named diagnostic methods invoked %d times", calls)
	}

	var calls atomic.Int64
	hostileError := agentDiagnosticHostileError{calls: &calls}
	unsupportedMessage := providers.Message{ToolCalls: []providers.ToolCall{{
		Arguments: map[string]any{"error": hostileError},
	}}}
	unsupportedTool := providers.ToolDefinition{
		Function: providers.ToolFunctionDefinition{
			Parameters: map[string]any{"error": hostileError},
		},
	}
	assertUnavailableAgentObservation(t, observeAgentMessageGraph([]providers.Message{unsupportedMessage}))
	assertUnavailableAgentObservation(t, observeAgentToolDefinitions([]providers.ToolDefinition{unsupportedTool}))
	if got := calls.Load(); got != 0 {
		t.Fatalf("hostile Error calls = %d, want 0", got)
	}
}

func TestAgentDiagnosticRejectsPointerOnlySemanticMethods(t *testing.T) {
	if !agentDiagnosticTypeHasSemanticMethods(reflect.TypeOf(agentDiagnosticHostileString(""))) {
		t.Fatal("value method set was not rejected")
	}
	if !agentDiagnosticTypeHasSemanticMethods(reflect.TypeOf(agentDiagnosticHostileError{})) {
		t.Fatal("semantic Error method set was not rejected")
	}
	if !agentDiagnosticTypeHasSemanticMethods(
		reflect.TypeOf(agentDiagnosticPointerSemanticString("")),
	) {
		t.Fatal("pointer-only semantic method set was not rejected")
	}
	methodKeyMap := map[agentDiagnosticHostileString]string{"redacted-key": "value"}
	if normalized := normalizeAgentDiagnosticValue(methodKeyMap); reflect.TypeOf(normalized) !=
		reflect.TypeOf(agentDiagnosticUnsupported{}) {
		t.Fatalf("method-bearing map key normalized type = %T, want sealed unsupported", normalized)
	}
	type methodFreeNamedString string
	if agentDiagnosticTypeHasSemanticMethods(reflect.TypeOf(methodFreeNamedString(""))) {
		t.Fatal("method-free named types were rejected")
	}
}

func TestAgentDiagnosticValueNormalizationProducesDetachedBuiltins(t *testing.T) {
	type namedString string
	type namedInt int32
	type namedSlice []namedString
	type namedMap map[namedString]any

	sourceSlice := namedSlice{"first", "second"}
	source := namedMap{
		"number": namedInt(7),
		"values": sourceSlice,
	}
	normalized := normalizeAgentDiagnosticValue(source)
	got, ok := normalized.(map[string]any)
	if !ok {
		t.Fatalf("normalized type = %T, want map[string]any", normalized)
	}
	if normalizedNumber, numberOK := got["number"].(int64); !numberOK || normalizedNumber != 7 {
		t.Fatalf("normalized number = %#v (%T), want int64(7)", got["number"], got["number"])
	}
	values, ok := got["values"].([]any)
	if !ok || len(values) != 2 || values[0] != "first" || values[1] != "second" {
		t.Fatalf("normalized values = %#v (%T), want detached []any", got["values"], got["values"])
	}

	sourceSlice[0] = "mutated"
	source["number"] = namedInt(9)
	if got["number"] != int64(7) || values[0] != "first" {
		t.Fatalf("normalized graph aliases source after mutation: %#v", got)
	}
	observation := logger.ObserveJSONValue(logger.ObservationDomainToolArguments, normalized)
	if observation.State != "complete" {
		t.Fatalf("normalized graph observation = %#v, want complete", observation)
	}

	var calls atomic.Int64
	hostile := agentDiagnosticHostileError{calls: &calls}
	rejected := normalizeAgentDiagnosticValue(map[string]any{"hostile": hostile})
	if _, ok := rejected.(agentDiagnosticUnsupported); !ok {
		t.Fatalf("rejected graph type = %T, want sealed unsupported sentinel", rejected)
	}
	if calls.Load() != 0 {
		t.Fatalf("normalization invoked %d hostile methods", calls.Load())
	}
	if unavailable := logger.ObserveJSONValue(
		logger.ObservationDomainToolArguments,
		rejected,
	); unavailable.State != "unavailable" {
		t.Fatalf("rejected graph observation = %#v, want unavailable", unavailable)
	}
}

func TestAgentDiagnosticBoundsCyclesAndUnsupportedValuesFailClosed(t *testing.T) {
	tooManyMessages := make([]providers.Message, maxAgentDiagnosticMembers+1)
	tooManyDefinitions := make([]providers.ToolDefinition, maxAgentDiagnosticMembers+1)
	assertUnavailableAgentObservation(t, observeAgentMessageGraph(tooManyMessages))
	assertUnavailableAgentObservation(t, observeAgentToolDefinitions(tooManyDefinitions))

	tooManyMedia := make([]string, maxAgentDiagnosticMembers+1)
	assertUnavailableAgentObservation(t, observeAgentMessageGraph([]providers.Message{{Media: tooManyMedia}}))

	tooManyMapMembers := make(map[string]any, maxAgentDiagnosticMembers+1)
	for index := 0; index <= maxAgentDiagnosticMembers; index++ {
		tooManyMapMembers[fmt.Sprintf("member-%03d", index)] = nil
	}
	assertUnavailableAgentObservation(t, observeAgentToolDefinitions([]providers.ToolDefinition{{
		Function: providers.ToolFunctionDefinition{Parameters: tooManyMapMembers},
	}}))

	cycleMap := make(map[string]any)
	cycleMap["self"] = cycleMap
	assertUnavailableAgentObservation(t, observeAgentMessageGraph([]providers.Message{{
		ToolCalls: []providers.ToolCall{{Arguments: cycleMap}},
	}}))
	cycleSlice := make([]any, 1)
	cycleSlice[0] = cycleSlice
	assertUnavailableAgentObservation(t, observeAgentToolDefinitions([]providers.ToolDefinition{{
		Function: providers.ToolFunctionDefinition{
			Parameters: map[string]any{"cycle": cycleSlice},
		},
	}}))

	shared := []any{"shared"}
	sharedObservation := observeAgentMessageGraph([]providers.Message{{
		ToolCalls: []providers.ToolCall{{
			Arguments: map[string]any{"left": shared, "right": shared},
		}},
	}})
	if sharedObservation.State != "complete" {
		t.Fatalf("shared acyclic alias rejected as a cycle: %#v", sharedObservation)
	}

	atDepthLimit := agentDiagnosticNestedValue(maxAgentDiagnosticValueDepth - 2)
	atDepth := observeAgentToolDefinitions([]providers.ToolDefinition{{
		Function: providers.ToolFunctionDefinition{
			Parameters: map[string]any{"value": atDepthLimit},
		},
	}})
	if atDepth.State != "complete" {
		t.Fatalf("exact detached depth limit rejected: %#v", atDepth)
	}
	overDepthLimit := agentDiagnosticNestedValue(maxAgentDiagnosticValueDepth - 1)
	assertUnavailableAgentObservation(t, observeAgentToolDefinitions([]providers.ToolDefinition{{
		Function: providers.ToolFunctionDefinition{
			Parameters: map[string]any{"value": overDepthLimit},
		},
	}}))

	nodeExhaustion := make(map[string]any, 8)
	for index := range 8 {
		nodeExhaustion[fmt.Sprintf("branch-%d", index)] = make([]any, maxAgentDiagnosticMembers)
	}
	assertUnavailableAgentObservation(t, observeAgentToolDefinitions([]providers.ToolDefinition{{
		Function: providers.ToolFunctionDefinition{Parameters: nodeExhaustion},
	}}))

	oversized := strings.Repeat("x", maxAgentDiagnosticBytes)
	assertUnavailableAgentObservation(t, observeAgentMessageGraph([]providers.Message{{Content: oversized}}))

	nonnilPointer := 7
	unsupportedValues := []any{
		json.Number("01"),
		math.NaN(),
		math.Inf(1),
		complex(1, 2),
		make(chan struct{}),
		func() {},
		&nonnilPointer,
		map[int]any{1: "value"},
	}
	for index, unsupported := range unsupportedValues {
		t.Run(fmt.Sprintf("unsupported-%d-%T", index, unsupported), func(t *testing.T) {
			assertUnavailableAgentObservation(t, observeAgentMessageGraph([]providers.Message{{
				ToolCalls: []providers.ToolCall{{
					Arguments: map[string]any{"value": unsupported},
				}},
			}}))
			assertUnavailableAgentObservation(t, observeAgentToolDefinitions([]providers.ToolDefinition{{
				Function: providers.ToolFunctionDefinition{
					Parameters: map[string]any{"value": unsupported},
				},
			}}))
		})
	}
}

func TestAgentDiagnosticInternalExhaustionBranchesFailClosed(t *testing.T) {
	for name, value := range map[string]any{
		"bool":   true,
		"string": "value",
		"int":    int64(-7),
		"uint":   uint64(7),
	} {
		t.Run(name, func(t *testing.T) {
			framer := newAgentDiagnosticFramer()
			framer.nodes = maxAgentDiagnosticNodes
			if projected, ok := framer.projectDetached(value, 1); ok || projected != nil {
				t.Fatalf("exhausted %s projection = %#v, %v; want nil, false", name, projected, ok)
			}
		})
	}

	framer := newAgentDiagnosticFramer()
	if projected, ok := framer.projectValue(reflect.Value{}, 1); !ok || projected != nil {
		t.Fatalf("invalid reflect value projection = %#v, %v; want nil, true", projected, ok)
	}

	framer = newAgentDiagnosticFramer()
	framer.nodes = maxAgentDiagnosticNodes - 1
	if projected, ok := framer.projectMap(
		reflect.ValueOf(map[string]any{"key": true}),
		1,
	); ok || projected != nil {
		t.Fatalf("key-exhausted map projection = %#v, %v; want nil, false", projected, ok)
	}

	instant := time.Unix(1_777_777_777, 123456789)
	framer = newAgentDiagnosticFramer()
	framer.nodes = maxAgentDiagnosticNodes - 4
	if frame, ok := framer.message(providers.Message{CreatedAt: &instant}); ok || frame != nil {
		t.Fatalf("timestamp-envelope exhaustion = %#v, %v; want nil, false", frame, ok)
	}

	framer = newAgentDiagnosticFramer()
	framer.nodes = maxAgentDiagnosticNodes - 3
	if frame, ok := framer.createdAt(&instant); ok || frame != nil {
		t.Fatalf("timestamp-offset exhaustion = %#v, %v; want nil, false", frame, ok)
	}

	type methodFreePointer *int
	methodFreePointerType := reflect.TypeOf((*methodFreePointer)(nil)).Elem()
	if agentDiagnosticTypeHasSemanticMethods(methodFreePointerType) {
		t.Fatal("method-free named pointer was classified as semantic")
	}
}

func TestAgentDiagnosticEveryNestedCollectionAndScalarBoundFailsClosed(t *testing.T) {
	tooManyAttachments := make([]providers.Attachment, maxAgentDiagnosticMembers+1)
	tooManyParts := make([]providers.PromptPart, maxAgentDiagnosticMembers+1)
	tooManyBlocks := make([]providers.ContentBlock, maxAgentDiagnosticMembers+1)
	tooManyCalls := make([]providers.ToolCall, maxAgentDiagnosticMembers+1)
	for name, message := range map[string]providers.Message{
		"attachments":  {Attachments: tooManyAttachments},
		"prompt parts": {Parts: tooManyParts},
		"system parts": {SystemParts: tooManyBlocks},
		"tool calls":   {ToolCalls: tooManyCalls},
	} {
		t.Run(name, func(t *testing.T) {
			assertUnavailableAgentObservation(
				t,
				observeAgentMessageGraph([]providers.Message{message}),
			)
		})
	}

	oversized := strings.Repeat("x", maxAgentDiagnosticBytes)
	messageMutators := map[string]func(*providers.Message){
		"role":          func(value *providers.Message) { value.Role = oversized },
		"model name":    func(value *providers.Message) { value.ModelName = oversized },
		"media":         func(value *providers.Message) { value.Media[0] = oversized },
		"attachment":    func(value *providers.Message) { value.Attachments[0].Type = oversized },
		"prompt part":   func(value *providers.Message) { value.Parts[0].Type = oversized },
		"reasoning":     func(value *providers.Message) { value.ReasoningContent = oversized },
		"content block": func(value *providers.Message) { value.SystemParts[0].Type = oversized },
		"cache control": func(value *providers.Message) {
			value.SystemParts[0].CacheControl.Type = oversized
		},
		"tool call id": func(value *providers.Message) { value.ToolCalls[0].ID = oversized },
		"function call": func(value *providers.Message) {
			value.ToolCalls[0].Function.Name = oversized
		},
		"tool name": func(value *providers.Message) { value.ToolCalls[0].Name = oversized },
		"tool thought": func(value *providers.Message) {
			value.ToolCalls[0].ThoughtSignature = oversized
		},
		"google extra": func(value *providers.Message) {
			value.ToolCalls[0].ExtraContent.Google.ThoughtSignature = oversized
		},
		"feedback": func(value *providers.Message) {
			value.ToolCalls[0].ExtraContent.ToolFeedbackExplanation = oversized
		},
		"result id":     func(value *providers.Message) { value.ToolCallID = oversized },
		"prompt source": func(value *providers.Message) { value.PromptSource = oversized },
	}
	for name, mutate := range messageMutators {
		t.Run("message/"+name, func(t *testing.T) {
			message := agentDiagnosticFixtureMessage()
			mutate(&message)
			assertUnavailableAgentObservation(
				t,
				observeAgentMessageGraph([]providers.Message{message}),
			)
		})
	}

	definitionMutators := map[string]func(*providers.ToolDefinition){
		"type":        func(value *providers.ToolDefinition) { value.Type = oversized },
		"name":        func(value *providers.ToolDefinition) { value.Function.Name = oversized },
		"description": func(value *providers.ToolDefinition) { value.Function.Description = oversized },
		"layer":       func(value *providers.ToolDefinition) { value.PromptLayer = oversized },
		"slot":        func(value *providers.ToolDefinition) { value.PromptSlot = oversized },
		"source":      func(value *providers.ToolDefinition) { value.PromptSource = oversized },
	}
	for name, mutate := range definitionMutators {
		t.Run("tool/"+name, func(t *testing.T) {
			definition := agentDiagnosticFixtureDefinition()
			mutate(&definition)
			assertUnavailableAgentObservation(
				t,
				observeAgentToolDefinitions([]providers.ToolDefinition{definition}),
			)
		})
	}
}

func TestAgentDiagnosticBudgetExhaustionPropagatesAtEveryFrameBoundary(t *testing.T) {
	createdAt := time.Time{}
	tests := []struct {
		name string
		run  func(*agentDiagnosticFramer) bool
	}{
		{name: "message graph", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.messageGraph(nil)
			return ok
		}},
		{name: "tool definitions", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.toolDefinitions(nil)
			return ok
		}},
		{name: "messages", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.messages([]providers.Message{{}})
			return ok
		}},
		{name: "message", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.message(providers.Message{})
			return ok
		}},
		{name: "created at", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.createdAt(&createdAt)
			return ok
		}},
		{name: "strings", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.strings([]string{"value"})
			return ok
		}},
		{name: "attachments", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.attachments([]providers.Attachment{{}})
			return ok
		}},
		{name: "prompt parts", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.promptParts([]providers.PromptPart{{}})
			return ok
		}},
		{name: "content blocks", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.contentBlocks([]providers.ContentBlock{{}})
			return ok
		}},
		{name: "content block", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.contentBlock(providers.ContentBlock{})
			return ok
		}},
		{name: "tool calls", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.toolCalls([]providers.ToolCall{{}})
			return ok
		}},
		{name: "tool call", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.toolCall(providers.ToolCall{})
			return ok
		}},
		{name: "extra content", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.extraContent(&providers.ExtraContent{})
			return ok
		}},
		{name: "definition list", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.definitionList([]providers.ToolDefinition{{}})
			return ok
		}},
		{name: "tool definition", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.toolDefinition(providers.ToolDefinition{})
			return ok
		}},
		{name: "string tuple", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.stringTuple("value")
			return ok
		}},
		{name: "put string", run: func(value *agentDiagnosticFramer) bool {
			return value.putString(nil, 0, "value")
		}},
		{name: "put integer", run: func(value *agentDiagnosticFramer) bool {
			return value.putInt64(nil, 0, 1)
		}},
		{name: "slice", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.slice(0, false)
			return ok
		}},
		{name: "null", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.projectNull(1)
			return ok
		}},
		{name: "map", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.projectMap(reflect.ValueOf(map[string]any{}), 1)
			return ok
		}},
		{name: "slice projection", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.projectSlice(reflect.ValueOf([]any{}), 1)
			return ok
		}},
		{name: "array projection", run: func(value *agentDiagnosticFramer) bool {
			_, ok := value.projectArray(reflect.ValueOf([1]any{}), 1)
			return ok
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			framer := newAgentDiagnosticFramer()
			framer.nodes = maxAgentDiagnosticNodes
			if test.run(framer) {
				t.Fatal("frame boundary widened an exhausted global node budget")
			}
		})
	}

	for _, test := range []struct {
		name    string
		framer  agentDiagnosticFramer
		payload int
	}{
		{name: "negative payload", framer: *newAgentDiagnosticFramer(), payload: -1},
		{
			name:    "oversized payload",
			framer:  *newAgentDiagnosticFramer(),
			payload: maxAgentDiagnosticBytes,
		},
		{
			name: "aggregate bytes",
			framer: agentDiagnosticFramer{
				bytes:  maxAgentDiagnosticBytes - agentDiagnosticNodeBytes + 1,
				active: make(map[agentDiagnosticVisit]struct{}),
			},
			payload: 0,
		},
	} {
		t.Run("take node/"+test.name, func(t *testing.T) {
			framer := test.framer
			if framer.takeNode(test.payload) {
				t.Fatal("invalid node charge was accepted")
			}
		})
	}

	exact := newAgentDiagnosticFramer()
	exact.bytes = maxAgentDiagnosticBytes - agentDiagnosticNodeBytes
	if !exact.takeNode(0) || exact.bytes != maxAgentDiagnosticBytes ||
		exact.takeNode(0) {
		t.Fatalf("exact byte boundary changed: %#v", exact)
	}
}

func TestAgentDiagnosticTypedNilAndScalarWidthNormalization(t *testing.T) {
	type namedMap map[string]int
	type namedSlice []int
	type namedString string
	type namedBool bool
	type namedInt int32
	type namedUint uint16
	type namedFloat float32

	var nilMap namedMap
	var nilSlice namedSlice
	var nilPointer *int
	graph := map[string]any{
		"nil-map":     nilMap,
		"nil-slice":   nilSlice,
		"nil-pointer": nilPointer,
		"string":      namedString("value"),
		"bool":        namedBool(true),
		"int":         namedInt(-7),
		"uint":        namedUint(9),
		"float":       namedFloat(1.25),
		"array":       [2]namedString{"a", "b"},
	}
	builtins := map[string]any{
		"nil-map":     map[string]any(nil),
		"nil-slice":   []any(nil),
		"nil-pointer": nil,
		"string":      "value",
		"bool":        true,
		"int":         int64(-7),
		"uint":        uint64(9),
		"float":       float64(float32(1.25)),
		"array":       []any{"a", "b"},
	}
	assertSameCompleteAgentObservations(
		t,
		"named argument grammar",
		observeAgentMessageGraph([]providers.Message{{
			ToolCalls: []providers.ToolCall{{Arguments: graph}},
		}}),
		observeAgentMessageGraph([]providers.Message{{
			ToolCalls: []providers.ToolCall{{Arguments: builtins}},
		}}),
	)
	assertSameCompleteAgentObservations(
		t,
		"named schema grammar",
		observeAgentToolDefinitions([]providers.ToolDefinition{{
			Function: providers.ToolFunctionDefinition{Parameters: graph},
		}}),
		observeAgentToolDefinitions([]providers.ToolDefinition{{
			Function: providers.ToolFunctionDefinition{Parameters: builtins},
		}}),
	)

	nilGraph := map[string]any{"map": namedMap(nil), "slice": namedSlice(nil)}
	emptyGraph := map[string]any{"map": namedMap{}, "slice": namedSlice{}}
	assertDifferentCompleteAgentObservations(
		t,
		"typed nil versus empty detached collections",
		observeAgentToolDefinitions([]providers.ToolDefinition{{
			Function: providers.ToolFunctionDefinition{Parameters: nilGraph},
		}}),
		observeAgentToolDefinitions([]providers.ToolDefinition{{
			Function: providers.ToolFunctionDefinition{Parameters: emptyGraph},
		}}),
	)
}

func TestAgentDiagnosticConcurrentDeterminism(t *testing.T) {
	message := agentDiagnosticFixtureMessage()
	definition := agentDiagnosticFixtureDefinition()
	wantMessage := observeAgentMessageGraph([]providers.Message{message})
	wantTool := observeAgentToolDefinitions([]providers.ToolDefinition{definition})
	if wantMessage.State != "complete" || wantTool.State != "complete" {
		t.Fatalf("fixture observation unavailable: message=%#v tool=%#v", wantMessage, wantTool)
	}

	const workers = 16
	const iterations = 100
	errors := make(chan string, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range iterations {
				messageObservation := observeAgentMessageGraph([]providers.Message{message})
				toolObservation := observeAgentToolDefinitions([]providers.ToolDefinition{definition})
				if messageObservation.Digest != wantMessage.Digest ||
					messageObservation.State != "complete" ||
					toolObservation.Digest != wantTool.Digest ||
					toolObservation.State != "complete" {
					errors <- fmt.Sprintf("message=%#v tool=%#v", messageObservation, toolObservation)
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errors)
	for problem := range errors {
		t.Fatalf("concurrent projection changed: %s", problem)
	}
}

func TestAgentDiagnosticHelpersHaveClosedSafeSurface(t *testing.T) {
	parsed, err := parser.ParseFile(
		token.NewFileSet(),
		"diagnostic_graph.go",
		nil,
		parser.SkipObjectResolution,
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, declaration := range parsed.Decls {
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			if ast.IsExported(typed.Name.Name) {
				t.Errorf("diagnostic graph exports function %q", typed.Name.Name)
			}
		case *ast.GenDecl:
			for _, specification := range typed.Specs {
				switch spec := specification.(type) {
				case *ast.TypeSpec:
					if ast.IsExported(spec.Name.Name) {
						t.Errorf("diagnostic graph exports type %q", spec.Name.Name)
					}
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						if ast.IsExported(name.Name) {
							t.Errorf("diagnostic graph exports value %q", name.Name)
						}
					}
				}
			}
		}
	}

	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if ok && identifier.Name == "logger" && strings.HasSuffix(selector.Sel.Name, "CF") {
			t.Errorf("diagnostic graph emits through logger.%s", selector.Sel.Name)
		}
		if ok && identifier.Name == "json" && selector.Sel.Name != "Number" {
			t.Errorf("diagnostic graph calls json.%s; want method-free scalar handling", selector.Sel.Name)
		}
		return true
	})

	messageCanary := "raw-message-surface-canary"
	toolCanary := "raw-tool-surface-canary"
	messageObservation := observeAgentMessageGraph([]providers.Message{{Content: messageCanary}})
	toolObservation := observeAgentToolDefinitions([]providers.ToolDefinition{{
		Function: providers.ToolFunctionDefinition{Description: toolCanary},
	}})
	formatted := fmt.Sprintf("%#v %#v", messageObservation, toolObservation)
	if strings.Contains(formatted, messageCanary) || strings.Contains(formatted, toolCanary) {
		t.Fatalf("raw source escaped Observation-only surface: %s", formatted)
	}
}

func agentDiagnosticFixtureMessageWithPairs() providers.Message {
	message := agentDiagnosticFixtureMessage()
	message.Attachments = append(message.Attachments, providers.Attachment{
		Type: "second-attachment",
	})
	message.Parts = append(message.Parts, providers.PromptPart{Type: "second-part"})
	message.SystemParts = append(message.SystemParts, providers.ContentBlock{Type: "second-block"})
	message.ToolCalls = append(message.ToolCalls, providers.ToolCall{ID: "second-call"})
	return message
}

func agentDiagnosticNestedValue(wrappers int) any {
	value := any("leaf")
	for range wrappers {
		value = []any{value}
	}
	return value
}

func swap[T any](values []T) {
	values[0], values[1] = values[1], values[0]
}

func assertSameCompleteAgentObservations(
	t *testing.T,
	name string,
	left logger.Observation,
	right logger.Observation,
) {
	t.Helper()
	if left.State != "complete" || right.State != "complete" || left.Digest != right.Digest {
		t.Fatalf("%s observations differ: left=%#v right=%#v", name, left, right)
	}
}

func assertDifferentCompleteAgentObservations(
	t *testing.T,
	name string,
	left logger.Observation,
	right logger.Observation,
) {
	t.Helper()
	if left.State != "complete" || right.State != "complete" || left.Digest == right.Digest {
		t.Fatalf("%s observations not distinct and complete: left=%#v right=%#v", name, left, right)
	}
}

func assertUnavailableAgentObservation(t *testing.T, observation logger.Observation) {
	t.Helper()
	if observation.State != "unavailable" || observation.Digest != "" ||
		observation.Count != 0 || observation.Bytes != 0 {
		t.Fatalf("observation did not fail closed: %#v", observation)
	}
}
