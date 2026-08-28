package agent

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func TestP015B2AApplicationDataPlaneZeroPreviewAndParity(t *testing.T) {
	const (
		contentCanary = "Error: P015B2A_INBOUND_1c9f0e87b4a64263"
		chatCanary    = "P015B2A_CHAT_1be3ea2d"
		senderCanary  = "P015B2A_SENDER_e9377c84"
		rawCanary     = "P015B2A_CONTEXT_RAW_91bb340a"
		handleCanary  = "P015B2A_REPLY_HANDLE_82a3f9c7"
		fileCanary    = "P015B2A_ATTACHMENT_0e8ea62d"
	)
	workspace := t.TempDir()
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: workspace, ModelName: "test-model", MaxTokens: 4096, MaxToolIterations: 2,
	}}}
	provider := &recordingProvider{}
	loop := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), provider)
	original := testInboundMessage(bus.InboundMessage{
		Channel: "cli", ChatID: chatCanary, SenderID: senderCanary,
		Content: contentCanary,
		Context: bus.InboundContext{
			Raw:          map[string]string{"private": rawCanary},
			ReplyHandles: map[string]string{"private": handleCanary},
			Attachments: []bus.InboundAttachment{{
				Filename: fileCanary, ContentType: "application/octet-stream", Kind: "file", SizeBytes: 7,
			}},
		},
	})
	inputSnapshot := bus.NormalizeInboundMessage(original)

	var response string
	var processErr error
	records, raw := captureP015HookRecords(t, func() {
		response, processErr = loop.processMessage(context.Background(), original)
	})
	if processErr != nil {
		t.Fatalf("processMessage() error = %v", processErr)
	}
	if response != "Mock response" {
		t.Fatalf("processMessage() response = %q, want Mock response", response)
	}
	if !reflect.DeepEqual(original, inputSnapshot) {
		t.Fatalf("processMessage mutated inbound input\nbefore: %#v\nafter:  %#v", inputSnapshot, original)
	}
	if len(provider.lastMessages) == 0 {
		t.Fatal("provider received no messages")
	}
	last := provider.lastMessages[len(provider.lastMessages)-1]
	if last.Role != "user" || last.Content != contentCanary {
		t.Fatalf("provider current message = %#v, want byte-exact canary", last)
	}
	assertP015CanariesAbsent(
		t,
		raw,
		contentCanary,
		chatCanary,
		senderCanary,
		rawCanary,
		handleCanary,
		fileCanary,
	)

	wantInboundObservation := logger.ObserveText(
		logger.ObservationDomainMessageGraph,
		contentCanary,
	)
	var inboundSafe, inboundSensitive map[string]any
	inboundSafeCount, inboundSensitiveCount := 0, 0
	for _, record := range records {
		if record["message"] != "Processing inbound message" {
			continue
		}
		if !p015B2ANonemptyRecordString(record, "message_graph_digest") ||
			!p015B2ANonemptyRecordString(record, "identity_channel_digest") ||
			!p015B2ANonemptyRecordString(record, "identity_chat_digest") ||
			!p015B2ANonemptyRecordString(record, "identity_sender_digest") {
			t.Fatalf("inbound safe record lacks observations: %#v", record)
		}
		if _, invalid := record["safe_fields_state"]; invalid {
			t.Fatalf("inbound safe fields rejected: %#v", record)
		}
		p015B2AAssertRuntimeObservation(
			t,
			record,
			logger.ObservationPrefixMessageGraph,
			wantInboundObservation,
		)
		switch record["level"] {
		case "info":
			inboundSafeCount++
			inboundSafe = record
		case "debug":
			inboundSensitiveCount++
			inboundSensitive = record
		}
	}
	if inboundSafeCount != 1 || inboundSensitiveCount != 1 {
		t.Fatalf(
			"inbound safe/sensitive pair = %d info/%d debug, want exact 1/1; records=%#v",
			inboundSafeCount,
			inboundSensitiveCount,
			records,
		)
	}
	if inboundSafe["message_graph_digest"] != inboundSensitive["message_graph_digest"] ||
		inboundSafe["message_graph_bytes"] != inboundSensitive["message_graph_bytes"] {
		t.Fatalf(
			"inbound safe/sensitive observations diverged: info=%#v debug=%#v",
			inboundSafe,
			inboundSensitive,
		)
	}
}

func TestP015B2AApplicationHistorySanitizationParityAndRace(t *testing.T) {
	const unexpectedID = "P015B2A_TOOL_CALL_92efc4d0"
	history := []providers.Message{
		{Role: "user", Content: "question"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{
			ID:   "expected",
			Name: "tool",
			Function: &providers.FunctionCall{
				Name:             "tool",
				Arguments:        `{"nested":{"value":"exact"}}`,
				ThoughtSignature: "function-signature",
			},
			Arguments: map[string]any{
				"nested": map[string]any{"value": "exact"},
			},
			ExtraContent: &providers.ExtraContent{
				Google:                  &providers.GoogleExtra{ThoughtSignature: "google-signature"},
				ToolFeedbackExplanation: "exact explanation",
			},
		}}},
		{Role: "tool", ToolCallID: unexpectedID, Content: "drop"},
		{Role: "tool", ToolCallID: "expected", Content: "keep"},
	}
	snapshot := cloneProviderMessages(history)
	want := cloneProviderMessages([]providers.Message{history[0], history[1], history[3]})

	_, raw := captureP015HookRecords(t, func() {
		got := sanitizeHistoryForProvider(history)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sanitizeHistoryForProvider() = %#v, want %#v", got, want)
		}
	})
	if !reflect.DeepEqual(history, snapshot) {
		t.Fatalf("sanitizeHistoryForProvider mutated source\nbefore: %#v\nafter:  %#v", snapshot, history)
	}
	assertP015CanariesAbsent(t, raw, unexpectedID)

	oldLevel := logger.GetLevel()
	logger.SetLevel(logger.FATAL)
	defer logger.SetLevel(oldLevel)
	var wait sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 50; iteration++ {
				if got := sanitizeHistoryForProvider(history); !reflect.DeepEqual(got, want) {
					t.Errorf("concurrent sanitize result = %#v, want %#v", got, want)
					return
				}
			}
		}()
	}
	wait.Wait()
}

type p015B2ATextProjectionProvider struct {
	mu          sync.Mutex
	messages    []providers.Message
	definitions []providers.ToolDefinition
	response    providers.LLMResponse
}

func (provider *p015B2ATextProjectionProvider) Chat(
	_ context.Context,
	messages []providers.Message,
	definitions []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	provider.messages = cloneProviderMessages(messages)
	provider.definitions = cloneToolDefinitions(definitions)
	response := provider.response
	provider.mu.Unlock()
	return &response, nil
}

func (provider *p015B2ATextProjectionProvider) snapshotMessages() []providers.Message {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return cloneProviderMessages(provider.messages)
}

func (provider *p015B2ATextProjectionProvider) snapshotDefinitions() []providers.ToolDefinition {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return cloneToolDefinitions(provider.definitions)
}

func TestP015B2AApplicationPromptModelAndReasoningRuntimeProjection(t *testing.T) {
	const (
		promptCanary    = "P015B2A_PROMPT_RAW_6d3e1987"
		messageCanary   = "P015B2A_USER_RAW_4293d65c"
		responseCanary  = "P015B2A_MODEL_RAW_968c734e"
		reasoningCanary = "P015B2A_REASONING_RAW_b942f3a1"
		mediaCanary     = "P015B2A_MEDIA_RAW_0bbf813c"
		skillCanary     = "P015B2A_SKILL_RAW_c1452ab8"
		toolCanary      = "P015B2A_ALLOWED_TOOL_RAW_c9021ec4"
	)
	promptBuilder := NewContextBuilder(t.TempDir())
	promptRequest := PromptBuildRequest{
		CurrentMessage: messageCanary,
		Media:          []string{mediaCanary},
		ActiveSkills:   []string{skillCanary},
		AllowedSkills:  []string{skillCanary},
		AllowedTools:   []string{toolCanary},
		Overlays: []PromptPart{{
			ID:      "instruction.p015b2a_projection",
			Layer:   PromptLayerInstruction,
			Slot:    PromptSlotWorkspace,
			Source:  PromptSource{ID: PromptSourceSubTurnProfile, Name: "p015b2a.projection"},
			Title:   "P015b2a projection fixture",
			Content: promptCanary,
			Stable:  false,
			Cache:   PromptCacheNone,
		}},
		SuppressDefaultSystemPrompt: true,
		SuppressSkillContext:        true,
		SuppressToolUseRule:         true,
	}
	promptSnapshot := promptRequest
	promptSnapshot.Media = append([]string(nil), promptRequest.Media...)
	promptSnapshot.ActiveSkills = append([]string(nil), promptRequest.ActiveSkills...)
	promptSnapshot.AllowedSkills = append([]string(nil), promptRequest.AllowedSkills...)
	promptSnapshot.AllowedTools = append([]string(nil), promptRequest.AllowedTools...)
	promptSnapshot.Overlays = append([]PromptPart(nil), promptRequest.Overlays...)
	var promptMessages []providers.Message
	promptRecords, promptRaw := captureP015HookRecords(t, func() {
		promptMessages = promptBuilder.BuildMessagesFromPrompt(promptRequest)
	})
	if !reflect.DeepEqual(promptRequest, promptSnapshot) {
		t.Fatalf("prompt projection mutated request\nbefore: %#v\nafter:  %#v", promptSnapshot, promptRequest)
	}
	if len(promptMessages) != 2 || promptMessages[0].Role != "system" ||
		promptMessages[0].Content != promptCanary || len(promptMessages[0].SystemParts) != 1 ||
		promptMessages[0].SystemParts[0].Text != promptCanary ||
		promptMessages[1].Role != "user" || promptMessages[1].Content != messageCanary ||
		!reflect.DeepEqual(promptMessages[1].Media, []string{mediaCanary}) {
		t.Fatalf("prompt builder lost exact functional values: %#v", promptMessages)
	}
	promptRecord := p015B2ARequireRuntimeRecord(
		t, promptRecords, "System prompt diagnostics", nil,
	)
	promptSafeRecord := p015B2ARequireRuntimeRecord(
		t, promptRecords, "System prompt built", nil,
	)
	p015B2AAssertRuntimeObservation(
		t,
		promptSafeRecord,
		logger.ObservationPrefixPrompt,
		logger.ObserveText(logger.ObservationDomainPrompt, promptCanary),
	)
	p015B2AAssertRuntimeObservation(
		t,
		promptRecord,
		logger.ObservationPrefixPrompt,
		logger.ObserveText(logger.ObservationDomainPrompt, promptCanary),
	)
	assertP015CanariesAbsent(
		t,
		promptRaw,
		promptCanary,
		messageCanary,
		mediaCanary,
		skillCanary,
		toolCanary,
	)

	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		Workspace: t.TempDir(), ModelName: "test-model", MaxTokens: 4096, MaxToolIterations: 2,
	}}}
	provider := &p015B2ATextProjectionProvider{response: providers.LLMResponse{
		Content: responseCanary, ReasoningContent: reasoningCanary, FinishReason: "stop",
	}}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	channelManager, err := channels.NewManager(&config.Config{}, messageBus, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	channelManager.RegisterChannel("projection", &fakeChannel{id: "projection-reasoning"})
	loop.SetChannelManager(channelManager)
	t.Cleanup(loop.Close)
	agent := loop.registry.GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	agent.Tools.Register(&p015B2AProjectionTool{})

	var (
		response     string
		runErr       error
		reasoningOut bus.OutboundMessage
	)
	modelRecords, modelRaw := captureP015HookRecords(t, func() {
		response, runErr = loop.runAgentLoop(context.Background(), agent, processOptions{
			SessionKey:      "p015b2a:text-projection",
			Channel:         "projection",
			ChatID:          "projection-chat",
			UserMessage:     messageCanary,
			DefaultResponse: defaultResponse,
			NoHistory:       true,
			SendResponse:    false,
		})
		if runErr != nil {
			return
		}
		select {
		case reasoningOut = <-messageBus.OutboundChan():
		case <-time.After(3 * time.Second):
			runErr = context.DeadlineExceeded
		}
	})
	if runErr != nil {
		t.Fatalf("runAgentLoop() error/reasoning wait = %v", runErr)
	}
	if response != responseCanary {
		t.Fatalf("runAgentLoop() response = %q, want byte-exact %q", response, responseCanary)
	}
	if reasoningOut.Channel != "projection" || reasoningOut.ChatID != "projection-reasoning" ||
		reasoningOut.Content != reasoningCanary {
		t.Fatalf("reasoning outbound = %#v, want byte-exact reasoning canary", reasoningOut)
	}

	messages := provider.snapshotMessages()
	if len(messages) < 2 || messages[len(messages)-1].Role != "user" ||
		messages[len(messages)-1].Content != messageCanary {
		t.Fatalf("provider messages lost exact functional values: %#v", messages)
	}
	definitions := provider.snapshotDefinitions()
	if len(definitions) == 0 {
		t.Fatal("provider received no tool definitions for diagnostic framing")
	}
	fullRequestRecord := p015B2ARequireRuntimeRecord(
		t, modelRecords, "Full LLM request", nil,
	)
	p015B2AAssertRuntimeObservation(
		t,
		fullRequestRecord,
		logger.ObservationPrefixMessageGraph,
		observeAgentMessageGraph(messages),
	)
	p015B2AAssertRuntimeObservation(
		t,
		fullRequestRecord,
		logger.ObservationPrefixToolSchema,
		observeAgentToolDefinitions(definitions),
	)
	roleCounts := countAgentDiagnosticMessageRoles(messages)
	wantCounts := map[string]float64{
		"message_count":           float64(len(messages)),
		"tool_count":              float64(len(definitions)),
		"system_message_count":    float64(roleCounts.system),
		"user_message_count":      float64(roleCounts.user),
		"assistant_message_count": float64(roleCounts.assistant),
		"tool_message_count":      float64(roleCounts.tool),
		"unknown_count":           float64(roleCounts.unknown),
	}
	for key, want := range wantCounts {
		if got := fullRequestRecord[key]; got != want {
			t.Errorf("Full LLM request %s = %#v, want %v", key, got, want)
		}
	}

	modelRecord := p015B2ARequireRuntimeRecord(
		t,
		modelRecords,
		"Model response diagnostics",
		func(record map[string]any) bool {
			_, pipelineRecord := record["tool_call_count"]
			return pipelineRecord
		},
	)
	modelSafeRecord := p015B2ARequireRuntimeRecord(
		t, modelRecords, "LLM response", nil,
	)
	reasoningRecord := p015B2ARequireRuntimeRecord(
		t, modelRecords, "Model reasoning diagnostics", nil,
	)
	p015B2AAssertRuntimeObservation(
		t,
		modelSafeRecord,
		logger.ObservationPrefixModelResponse,
		logger.ObserveText(logger.ObservationDomainModelResponse, responseCanary),
	)
	p015B2AAssertRuntimeObservation(
		t,
		modelSafeRecord,
		logger.ObservationPrefixReasoning,
		logger.ObserveText(logger.ObservationDomainReasoning, reasoningCanary),
	)
	p015B2AAssertRuntimeObservation(
		t,
		modelRecord,
		logger.ObservationPrefixModelResponse,
		logger.ObserveText(logger.ObservationDomainModelResponse, responseCanary),
	)
	p015B2AAssertRuntimeObservation(
		t,
		reasoningRecord,
		logger.ObservationPrefixReasoning,
		logger.ObserveText(logger.ObservationDomainReasoning, reasoningCanary),
	)
	assertP015CanariesAbsent(
		t, modelRaw, messageCanary, responseCanary, reasoningCanary,
	)
}

type (
	p015B2ANamedString  string
	p015B2ANamedInt     int32
	p015B2ANamedStrings []p015B2ANamedString
	p015B2ANamedMap     map[p015B2ANamedString]any
)

type p015B2AProjectionTool struct {
	mu    sync.Mutex
	calls int
	args  map[string]any
	err   error
}

func (*p015B2AProjectionTool) Name() string { return "p015b2a_projection_tool" }

func (*p015B2AProjectionTool) Description() string {
	return "P015b2a diagnostic projection parity fixture"
}

func (*p015B2AProjectionTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *p015B2AProjectionTool) Execute(
	_ context.Context,
	arguments map[string]any,
) *tools.ToolResult {
	detached, err := tools.DetachToolArguments(arguments)
	tool.mu.Lock()
	tool.calls++
	tool.args = detached
	tool.err = err
	tool.mu.Unlock()
	return tools.SilentResult("ordinary-projection-result")
}

func (tool *p015B2AProjectionTool) snapshot() (int, map[string]any, error) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	detached, err := tools.DetachToolArguments(tool.args)
	if tool.err != nil {
		return tool.calls, detached, tool.err
	}
	return tool.calls, detached, err
}

type p015B2AProjectionHook struct {
	mu   sync.Mutex
	args map[string]any
	err  error
}

func (hook *p015B2AProjectionHook) BeforeTool(
	_ context.Context,
	request *ToolCallHookRequest,
) (*ToolCallHookRequest, HookDecision, error) {
	detached, err := tools.DetachToolArguments(request.Arguments)
	hook.mu.Lock()
	hook.args = detached
	hook.err = err
	hook.mu.Unlock()
	if err != nil {
		return request, HookDecision{Action: HookActionContinue}, err
	}
	next := request.Clone()
	next.HookResult = &tools.ToolResult{
		ForLLM:          "hook-projection-result",
		ForUser:         "hook-projection-user-output",
		ResponseHandled: true,
	}
	return next, HookDecision{Action: HookActionRespond}, nil
}

func (*p015B2AProjectionHook) AfterTool(
	_ context.Context,
	response *ToolResultHookResponse,
) (*ToolResultHookResponse, HookDecision, error) {
	return response.Clone(), HookDecision{Action: HookActionContinue}, nil
}

func (hook *p015B2AProjectionHook) snapshot() (map[string]any, error) {
	hook.mu.Lock()
	defer hook.mu.Unlock()
	detached, err := tools.DetachToolArguments(hook.args)
	if hook.err != nil {
		return detached, hook.err
	}
	return detached, err
}

type p015B2AToolProjectionProvider struct {
	mu        sync.Mutex
	calls     int
	arguments map[string]any
}

func (provider *p015B2AToolProjectionProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls++
	if provider.calls == 1 {
		arguments, err := tools.DetachToolArguments(provider.arguments)
		if err != nil {
			return nil, err
		}
		return &providers.LLMResponse{
			Content: "calling projection tool",
			ToolCalls: []providers.ToolCall{{
				ID: "p015b2a-projection-call", Name: "p015b2a_projection_tool",
				Arguments: arguments,
			}},
			FinishReason: "tool_calls",
		}, nil
	}
	return &providers.LLMResponse{Content: "ordinary-projection-finished", FinishReason: "stop"}, nil
}

func TestP015B2AApplicationToolArgumentRuntimeProjection(t *testing.T) {
	const (
		secretCanary = "P015B2A_TOOL_RAW_66445aaf"
		itemCanary   = "P015B2A_ITEM_RAW_65de2f0e"
		keyCanary    = "P015B2A_KEY_RAW_24af2962"
	)
	arguments := map[string]any{
		"payload": p015B2ANamedMap{
			p015B2ANamedString(keyCanary): p015B2ANamedString(secretCanary),
			"count":                       p015B2ANamedInt(7),
			"items":                       p015B2ANamedStrings{p015B2ANamedString(itemCanary)},
		},
	}
	inputSnapshot, err := tools.DetachToolArguments(arguments)
	if err != nil {
		t.Fatalf("detach input snapshot: %v", err)
	}
	expectedProjected := map[string]any{
		"payload": map[string]any{
			keyCanary: secretCanary,
			"count":   int64(7),
			"items":   []any{itemCanary},
		},
	}
	expectedObservation := logger.ObserveJSONValue(
		logger.ObservationDomainToolArguments,
		expectedProjected,
	)
	if expectedObservation.State != "complete" {
		t.Fatalf("independent expected tool observation = %#v, want complete", expectedObservation)
	}

	for _, test := range []struct {
		name         string
		hook         bool
		message      string
		wantResponse string
	}{
		{
			name: "ordinary registry execution", message: "Tool arguments diagnostics",
			wantResponse: "ordinary-projection-finished",
		},
		{
			name: "trusted hook response", hook: true, message: "Hook tool arguments diagnostics",
			wantResponse: "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &p015B2AToolProjectionProvider{arguments: arguments}
			loop, agent, _ := newPipelineToolPolicyLoop(
				t, provider, tools.CompatibilityAllowToolPolicy{}, false,
			)
			projectionTool := &p015B2AProjectionTool{}
			agent.Tools.Register(projectionTool)
			var projectionHook *p015B2AProjectionHook
			if test.hook {
				projectionHook = &p015B2AProjectionHook{}
				if mountErr := loop.MountHook(NamedHook("p015b2a-projection", projectionHook)); mountErr != nil {
					t.Fatalf("MountHook() error = %v", mountErr)
				}
			}

			var response string
			var runErr error
			records, raw := captureP015HookRecords(t, func() {
				response, runErr = runPipelineToolPolicyTurn(
					context.Background(), loop, agent, "p015b2a-projection-"+test.name,
				)
			})
			if runErr != nil {
				t.Fatalf("runAgentLoop() error = %v", runErr)
			}
			if response != test.wantResponse {
				t.Fatalf("runAgentLoop() response = %q, want %q", response, test.wantResponse)
			}
			if !reflect.DeepEqual(arguments, inputSnapshot) {
				t.Fatalf("tool flow mutated provider arguments\nbefore: %#v\nafter:  %#v",
					inputSnapshot, arguments)
			}

			calls, executedArgs, executeErr := projectionTool.snapshot()
			if test.hook {
				if calls != 0 || executeErr != nil {
					t.Fatalf("hook response executed registry tool: calls=%d err=%v", calls, executeErr)
				}
				hookArgs, hookErr := projectionHook.snapshot()
				if hookErr != nil || !reflect.DeepEqual(hookArgs, inputSnapshot) {
					t.Fatalf("hook arguments = %#v / %v, want exact %#v", hookArgs, hookErr, inputSnapshot)
				}
			} else if calls != 1 || executeErr != nil || !reflect.DeepEqual(executedArgs, inputSnapshot) {
				t.Fatalf("registry arguments/calls = %#v / %d / %v, want exact %#v / 1",
					executedArgs, calls, executeErr, inputSnapshot)
			}

			safeRecord := p015B2ARequireRuntimeRecord(t, records, "Tool call", nil)
			sensitiveRecord := p015B2ARequireRuntimeRecord(
				t,
				records,
				test.message,
				func(record map[string]any) bool { return record["component"] == "agent" },
			)
			p015B2AAssertRuntimeObservation(
				t, safeRecord, logger.ObservationPrefixToolArguments, expectedObservation,
			)
			p015B2AAssertRuntimeObservation(
				t, sensitiveRecord, logger.ObservationPrefixToolArguments, expectedObservation,
			)
			assertP015CanariesAbsent(t, raw, secretCanary, itemCanary, keyCanary)
		})
	}
}

func p015B2ARequireRuntimeRecord(
	t *testing.T,
	records []map[string]any,
	message string,
	accept func(map[string]any) bool,
) map[string]any {
	t.Helper()
	var matches []map[string]any
	for _, record := range records {
		if record["message"] != message || accept != nil && !accept(record) {
			continue
		}
		matches = append(matches, record)
	}
	if len(matches) != 1 {
		t.Fatalf("runtime records for %q = %d, want exactly 1; records=%#v",
			message, len(matches), records)
	}
	return matches[0]
}

func p015B2AAssertRuntimeObservation(
	t *testing.T,
	record map[string]any,
	prefix logger.ObservationFieldPrefix,
	want logger.Observation,
) {
	t.Helper()
	if want.State != "complete" {
		t.Fatalf("expected observation is not complete: %#v", want)
	}
	for key, expected := range logger.ObservationFields(prefix, want) {
		if integer, ok := expected.(int64); ok {
			expected = float64(integer)
		}
		if !reflect.DeepEqual(record[key], expected) {
			t.Fatalf("record observation field %q = %#v, want %#v; record=%#v",
				key, record[key], expected, record)
		}
	}
	if _, rejected := record["safe_fields_state"]; rejected {
		t.Fatalf("runtime observation record rejected its safe fields: %#v", record)
	}
	if _, previewed := record["sensitive_preview"]; previewed {
		t.Fatalf("zero diagnostic policy emitted a sensitive preview: %#v", record)
	}
}

func TestP015B2AApplicationLoggingASTManifest(t *testing.T) {
	expectedSafe := map[string]int{
		"agent_message.go": 5, "context.go": 22, "context_legacy.go": 3,
		"agent_utils.go": 2, "pipeline_llm.go": 18, "pipeline_execute.go": 15,
	}
	expectedSensitive := map[string]int{
		"agent_message.go": 1, "context.go": 1, "context_legacy.go": 0,
		"agent_utils.go": 0, "pipeline_llm.go": 2, "pipeline_execute.go": 2,
	}
	wantSensitiveSinks := map[string]p015B2ASensitiveSinkExpectation{
		"DiagnosticMessageInboundMessage": {
			file: "agent_message.go", sensitivity: "SensitivityInboundMessage",
			domain: "ObservationDomainMessageGraph", valuePath: []string{"msg", "Content"},
		},
		"DiagnosticMessageSystemPrompt": {
			file: "context.go", sensitivity: "SensitivityPrompt",
			domain: "ObservationDomainPrompt", valuePath: []string{"fullSystemPrompt"},
		},
		"DiagnosticMessageModelResponse": {
			file: "pipeline_llm.go", sensitivity: "SensitivityModelResponse",
			domain:    "ObservationDomainModelResponse",
			valuePath: []string{"exec", "response", "Content"},
		},
		"DiagnosticMessageModelReasoning": {
			file: "pipeline_llm.go", sensitivity: "SensitivityReasoning",
			domain: "ObservationDomainReasoning", valuePath: []string{"reasoningContent"},
		},
		"DiagnosticMessageToolArguments": {
			file: "pipeline_execute.go", sensitivity: "SensitivityToolArguments",
			domain: "ObservationDomainToolArguments", valuePath: []string{"diagnosticToolArgs"},
		},
		"DiagnosticMessageHookToolArguments": {
			file: "pipeline_execute.go", sensitivity: "SensitivityToolArguments",
			domain: "ObservationDomainToolArguments", valuePath: []string{"diagnosticToolArgs"},
		},
	}
	gotSensitiveMessages := make(map[string]int)
	totalSafe := 0
	for name, wantSafe := range expectedSafe {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, data, parser.AllErrors)
		if err != nil {
			t.Fatal(err)
		}
		safeCount, sensitiveCount, jsonMarshalCount, normalizeCount := 0, 0, 0, 0
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if identifier, isIdentifier := call.Fun.(*ast.Ident); isIdentifier &&
				identifier.Name == "normalizeAgentDiagnosticValue" {
				normalizeCount++
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if p015B2AIdent(selector.X, "json") && selector.Sel.Name == "Marshal" {
				jsonMarshalCount++
			}
			if !p015B2AIdent(selector.X, "logger") {
				return true
			}
			callee := selector.Sel.Name
			if p015B2ALegacyLoggerCall(callee) {
				t.Errorf("%s contains legacy logger.%s", name, callee)
			}
			if strings.HasSuffix(callee, "SafeCF") {
				safeCount++
				p015B2AValidateSafeSink(t, name, call)
			}
			if callee == "DebugSensitiveCF" {
				sensitiveCount++
				message := p015B2AValidateSensitiveSink(t, name, call, wantSensitiveSinks)
				gotSensitiveMessages[message]++
			}
			return true
		})
		if safeCount != wantSafe || sensitiveCount != expectedSensitive[name] {
			t.Errorf("%s sinks = %d safe/%d sensitive, want %d/%d", name,
				safeCount, sensitiveCount, wantSafe, expectedSensitive[name])
		}
		wantJSONMarshal := 0
		if name == "pipeline_llm.go" {
			wantJSONMarshal = 1
		}
		if jsonMarshalCount != wantJSONMarshal {
			t.Errorf("%s json.Marshal calls = %d, want functional-only count %d",
				name, jsonMarshalCount, wantJSONMarshal)
		}
		wantNormalize := 0
		if name == "pipeline_execute.go" {
			wantNormalize = 2
		}
		if normalizeCount != wantNormalize {
			t.Errorf("%s diagnostic normalizations = %d, want %d",
				name, normalizeCount, wantNormalize)
		}
		if name == "pipeline_execute.go" {
			p015B2AValidateToolProjectionPaths(t, parsed)
		}
		totalSafe += safeCount
		if bytes.Contains(data, []byte("formatMessagesForLog")) ||
			bytes.Contains(data, []byte("formatToolsForLog")) ||
			bytes.Contains(data, []byte("argsPreview")) {
			t.Errorf("%s retains a raw log formatter/preview path", name)
		}
	}
	if totalSafe != 65 {
		t.Fatalf("safe sink count = %d, want exact migrated census 65", totalSafe)
	}
	wantSensitiveMessages := make(map[string]int, len(wantSensitiveSinks))
	for message := range wantSensitiveSinks {
		wantSensitiveMessages[message] = 1
	}
	if !reflect.DeepEqual(gotSensitiveMessages, wantSensitiveMessages) {
		t.Fatalf("sensitive message manifest = %#v, want %#v", gotSensitiveMessages, wantSensitiveMessages)
	}
}

type p015B2ASensitiveSinkExpectation struct {
	file        string
	sensitivity string
	domain      string
	valuePath   []string
}

func p015B2AValidateSafeSink(t *testing.T, file string, call *ast.CallExpr) {
	t.Helper()
	if len(call.Args) != 3 || !p015B2ASelector(call.Args[0], "logger", "ComponentAgent") ||
		!p015B2ASelectorPrefix(call.Args[1], "logger", "DiagnosticMessage") ||
		!p015B2ACall(call.Args[2], "logger", "NewSafeFields") {
		t.Errorf("%s has non-direct/non-closed SafeCF call at byte %d", file, call.Pos())
	}
	p015B2ARejectHostileFormatting(t, file, call)
}

func p015B2AValidateSensitiveSink(
	t *testing.T,
	file string,
	call *ast.CallExpr,
	want map[string]p015B2ASensitiveSinkExpectation,
) string {
	t.Helper()
	if len(call.Args) != 7 || !p015B2AEmptyPolicy(call.Args[0]) ||
		!p015B2ASelector(call.Args[1], "logger", "ComponentAgent") ||
		!p015B2ACall(call.Args[3], "logger", "NewSafeFields") {
		t.Errorf("%s has non-zero/non-direct sensitive call at byte %d", file, call.Pos())
		return ""
	}
	p015B2ARejectHostileFormatting(t, file, call)
	selector, ok := call.Args[2].(*ast.SelectorExpr)
	if !ok || !p015B2AIdent(selector.X, "logger") {
		t.Errorf("%s sensitive sink has non-closed message at byte %d", file, call.Pos())
		return ""
	}
	message := selector.Sel.Name
	expected, ok := want[message]
	if !ok {
		t.Errorf("%s sensitive sink uses unplanned message logger.%s at byte %d",
			file, message, call.Pos())
		return message
	}
	valuePath, valueOK := p015B2AExpressionPath(call.Args[6])
	if file != expected.file ||
		!p015B2ASelector(call.Args[4], "logger", expected.sensitivity) ||
		!p015B2ASelector(call.Args[5], "logger", expected.domain) ||
		!valueOK || !reflect.DeepEqual(valuePath, expected.valuePath) {
		t.Errorf(
			"%s logger.%s tuple at byte %d = class/domain/value %s/%s/%v, want %s/%s/%v in %s",
			file,
			message,
			call.Pos(),
			p015B2ASelectorName(call.Args[4]),
			p015B2ASelectorName(call.Args[5]),
			valuePath,
			expected.sensitivity,
			expected.domain,
			expected.valuePath,
			expected.file,
		)
	}
	return message
}

func p015B2AValidateToolProjectionPaths(t *testing.T, parsed *ast.File) {
	t.Helper()
	want := map[string]int{
		"DiagnosticMessageToolArguments":     1,
		"DiagnosticMessageHookToolArguments": 1,
	}
	got := make(map[string]int, len(want))
	ast.Inspect(parsed, func(node ast.Node) bool {
		block, ok := node.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for index, statement := range block.List {
			sensitive := p015B2ADirectLoggerCall(statement, "DebugSensitiveCF")
			message := p015B2ACallMessageName(sensitive)
			if _, planned := want[message]; !planned {
				continue
			}
			got[message]++
			if index < 2 {
				t.Errorf("pipeline_execute.go logger.%s lacks a preceding shared projection", message)
				continue
			}

			projectionCount := 0
			for _, preceding := range block.List[:index] {
				if p015B2AProjectionSnapshot(preceding) {
					projectionCount++
				}
			}
			if projectionCount != 1 || !p015B2AProjectionSnapshot(block.List[index-2]) {
				t.Errorf(
					"pipeline_execute.go logger.%s projection snapshots = %d or not immediately preceding safe+sensitive pair",
					message,
					projectionCount,
				)
			}

			safe := p015B2ADirectLoggerCall(block.List[index-1], "InfoSafeCF")
			if safe == nil || len(safe.Args) != 3 ||
				!p015B2ASelector(safe.Args[1], "logger", "DiagnosticMessageToolCall") ||
				!p015B2ACall(safe.Args[2], "logger", "NewSafeFields") {
				t.Errorf("pipeline_execute.go logger.%s is not paired with the direct safe tool record", message)
				continue
			}
			observationCount := 0
			ast.Inspect(safe.Args[2], func(node ast.Node) bool {
				call, callOK := node.(*ast.CallExpr)
				if !callOK || !p015B2ASelector(call.Fun, "logger", "ObserveJSONValue") {
					return true
				}
				observationCount++
				if len(call.Args) != 2 ||
					!p015B2ASelector(call.Args[0], "logger", "ObservationDomainToolArguments") ||
					!p015B2AIdent(call.Args[1], "diagnosticToolArgs") {
					t.Errorf(
						"pipeline_execute.go logger.%s safe observation does not consume the shared projected snapshot",
						message,
					)
				}
				return true
			})
			if observationCount != 1 {
				t.Errorf("pipeline_execute.go logger.%s safe observation count = %d, want 1",
					message, observationCount)
			}
		}
		return true
	})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pipeline_execute.go projected path manifest = %#v, want %#v", got, want)
	}
}

func p015B2AProjectionSnapshot(statement ast.Stmt) bool {
	assignment, ok := statement.(*ast.AssignStmt)
	if !ok || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 1 ||
		len(assignment.Rhs) != 1 || !p015B2AIdent(assignment.Lhs[0], "diagnosticToolArgs") {
		return false
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	return ok && p015B2AIdent(call.Fun, "normalizeAgentDiagnosticValue") &&
		len(call.Args) == 1 && p015B2AIdent(call.Args[0], "toolArgs")
}

func p015B2ADirectLoggerCall(statement ast.Stmt, name string) *ast.CallExpr {
	expression, ok := statement.(*ast.ExprStmt)
	if !ok {
		return nil
	}
	call, ok := expression.X.(*ast.CallExpr)
	if !ok || !p015B2ASelector(call.Fun, "logger", name) {
		return nil
	}
	return call
}

func p015B2ACallMessageName(call *ast.CallExpr) string {
	if call == nil || len(call.Args) < 3 {
		return ""
	}
	selector, ok := call.Args[2].(*ast.SelectorExpr)
	if !ok || !p015B2AIdent(selector.X, "logger") {
		return ""
	}
	return selector.Sel.Name
}

func p015B2AExpressionPath(expression ast.Expr) ([]string, bool) {
	switch value := expression.(type) {
	case *ast.Ident:
		return []string{value.Name}, true
	case *ast.SelectorExpr:
		path, ok := p015B2AExpressionPath(value.X)
		if !ok {
			return nil, false
		}
		return append(path, value.Sel.Name), true
	default:
		return nil, false
	}
}

func p015B2ASelectorName(expression ast.Expr) string {
	path, ok := p015B2AExpressionPath(expression)
	if !ok {
		return "<non-selector>"
	}
	return strings.Join(path, ".")
}

func p015B2ARejectHostileFormatting(t *testing.T, file string, root ast.Node) {
	t.Helper()
	ast.Inspect(root, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch selector.Sel.Name {
		case "Error", "String", "Format", "Sprintf", "Marshal", "MarshalJSON":
			t.Errorf("%s sink argument invokes hostile formatter %s", file, selector.Sel.Name)
		}
		return true
	})
}

func p015B2AEmptyPolicy(expression ast.Expr) bool {
	literal, ok := expression.(*ast.CompositeLit)
	return ok && len(literal.Elts) == 0 && p015B2ASelector(literal.Type, "logger", "DiagnosticPolicy")
}

func p015B2ACall(expression ast.Expr, pkg, name string) bool {
	call, ok := expression.(*ast.CallExpr)
	return ok && p015B2ASelector(call.Fun, pkg, name)
}

func p015B2ASelectorPrefix(expression ast.Expr, pkg, prefix string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && p015B2AIdent(selector.X, pkg) && strings.HasPrefix(selector.Sel.Name, prefix)
}

func p015B2ASelector(expression ast.Expr, pkg, name string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && p015B2AIdent(selector.X, pkg) && selector.Sel.Name == name
}

func p015B2AIdent(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == name
}

func p015B2ANonemptyRecordString(record map[string]any, key string) bool {
	value, ok := record[key].(string)
	return ok && value != ""
}

func p015B2ALegacyLoggerCall(name string) bool {
	switch name {
	case "Debug", "Debugf", "DebugCF", "Info", "Infof", "InfoCF",
		"Warn", "Warnf", "WarnCF", "Error", "Errorf", "ErrorCF",
		"Fatal", "Fatalf", "FatalCF", "RecoverPanic", "RecoverPanicNoExit":
		return true
	default:
		return false
	}
}
