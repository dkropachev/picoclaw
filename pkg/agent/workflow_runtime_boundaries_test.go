package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowRuntimeConstructorsFailClosedAndSelectExactAgent(t *testing.T) {
	if workflowDefinitionsDir(nil) != workflows.DefaultDefinitionsDir {
		t.Fatal("nil agent loop did not use the default workflow definitions directory")
	}
	if runner, ok := NewWorkflowAgentRunner(nil).(*workflowAgentRunner); !ok || runner.loop != nil {
		t.Fatalf("NewWorkflowAgentRunner(nil) = %#v", runner)
	}
	if _, err := NewWorkflowToolRunner(nil, "main"); err == nil {
		t.Fatal("nil agent loop was accepted by NewWorkflowToolRunner")
	}
	if _, err := NewWorkflowToolRunner(&AgentLoop{}, "main"); err == nil {
		t.Fatal("agent loop without a registry was accepted")
	}

	registry := &AgentRegistry{agents: map[string]*AgentInstance{}}
	loop := &AgentLoop{registry: registry}
	if _, err := NewWorkflowToolRunner(loop, "missing"); err == nil {
		t.Fatal("unknown explicit agent was accepted")
	}
	if _, err := NewWorkflowToolRunner(loop, ""); err == nil {
		t.Fatal("empty registry supplied a default agent")
	}

	registry.agents["main"] = &AgentInstance{ID: "main"}
	if _, err := NewWorkflowToolRunner(loop, "main"); err == nil {
		t.Fatal("agent without a tool registry was accepted")
	}
	registry.agents["main"].Tools = tools.NewToolRegistry()
	runner, err := NewWorkflowToolRunner(loop, "")
	if err != nil {
		t.Fatalf("default agent tool runner: %v", err)
	}
	selected := runner.(*workflowToolRunner)
	if selected.agentID != "main" || !selected.dynamic || selected.registry != registry.agents["main"].Tools {
		t.Fatalf("selected tool runner = %#v", selected)
	}

	cfg := config.DefaultConfig()
	cfg.Workflows.DefinitionsDir = "custom-workflows"
	loop.cfg = cfg
	if got := workflowDefinitionsDir(loop); got != "custom-workflows" {
		t.Fatalf("workflowDefinitionsDir() = %q", got)
	}
}

func TestWorkflowToolRunnerRejectsUnsafeDispatchAndPreservesDeliveryContext(t *testing.T) {
	if _, err := (*workflowToolRunner)(nil).RunTool(t.Context(), workflows.ToolRequest{}); err == nil {
		t.Fatal("nil workflow tool runner was accepted")
	}

	registry := tools.NewToolRegistry()
	messageTool := &workflowRuntimeResultTool{name: "message", result: tools.NewToolResult("sent")}
	registry.Register(messageTool)
	runner := &workflowToolRunner{agentID: "main", registry: registry}

	if _, err := runner.RunTool(t.Context(), workflows.ToolRequest{
		Name:      "missing-mcp-wrapper",
		MCP:       true,
		MCPServer: "github",
		MCPTool:   "issue_get",
	}); err == nil {
		t.Fatal("missing MCP wrapper was executed")
	}
	if _, err := runner.RunTool(t.Context(), workflows.ToolRequest{Name: tools.WorkflowToolName}); err == nil {
		t.Fatal("recursive workflow tool call was accepted")
	}

	request := workflows.ToolRequest{
		Name:    "message",
		Session: "workflow-session",
		Args:    map[string]any{"text": "hello"},
		Delivery: workflows.Delivery{
			Channel:          "telegram",
			ChatID:           "chat-1",
			TopicID:          "topic-1",
			MessageID:        "message-1",
			ReplyToMessageID: "reply-1",
		},
	}
	if _, err := runner.RunTool(t.Context(), request); err != nil {
		t.Fatalf("message tool dispatch: %v", err)
	}
	if messageTool.args["reply_to_message_id"] != "reply-1" ||
		tools.ToolAgentID(messageTool.ctx) != "main" ||
		tools.ToolSessionKey(messageTool.ctx) != "workflow-session" {
		t.Fatalf("message dispatch context = %#v, args = %#v", messageTool.ctx, messageTool.args)
	}

	errorTool := &workflowRuntimeResultTool{
		name:   "plain-error",
		result: &tools.ToolResult{ForLLM: "plain failure", IsError: true},
	}
	registry.Register(errorTool)
	outputs, err := runner.RunTool(t.Context(), workflows.ToolRequest{Name: errorTool.name})
	if err == nil || !strings.Contains(err.Error(), "plain failure") || outputs["is_error"] != true {
		t.Fatalf("plain tool failure = (%#v, %v)", outputs, err)
	}
}

func TestWorkflowToolRunnerUsesCurrentRuntimeGeneration(t *testing.T) {
	registry := tools.NewToolRegistry()
	runner := &workflowToolRunner{
		agentID:  "main",
		registry: registry,
		loop:     &AgentLoop{runtimeGateStopped: true},
		dynamic:  true,
	}
	if _, err := runner.RunTool(
		t.Context(),
		workflows.ToolRequest{Name: "anything"},
	); !errors.Is(err, errAgentRuntimeStopped) {
		t.Fatalf("stopped runtime error = %v", err)
	}

	runner.loop = &AgentLoop{}
	if _, err := runner.RunTool(t.Context(), workflows.ToolRequest{Name: "anything"}); err == nil {
		t.Fatal("dynamic tool run accepted a missing current registry")
	}
	runner.loop.registry = &AgentRegistry{agents: map[string]*AgentInstance{}}
	if _, err := runner.RunTool(t.Context(), workflows.ToolRequest{Name: "anything"}); err == nil {
		t.Fatal("dynamic tool run accepted a missing current agent")
	}
}

func TestWorkflowHandledMediaDeliveryPropagatesFailuresAndBusFallback(t *testing.T) {
	result := tools.MediaResult("delivered", []string{"media://missing"}).WithResponseHandled()
	manager := &workflowRuntimeFailingMediaManager{sendErr: errors.New("send failed")}
	runner := &workflowToolRunner{
		agentID: "main",
		loop:    &AgentLoop{channelManager: manager},
	}
	request := workflows.ToolRequest{
		Name: "attachment",
		Delivery: workflows.Delivery{
			Channel: "telegram",
			ChatID:  "chat-1",
		},
	}
	if err := runner.deliverHandledMedia(t.Context(), request, result); err == nil {
		t.Fatal("channel media delivery failure was ignored")
	}

	messageBus := bus.NewMessageBus()
	runner.loop = &AgentLoop{bus: messageBus}
	result.ResponseHandled = true
	if err := runner.deliverHandledMedia(t.Context(), request, result); err != nil {
		t.Fatalf("bus media fallback: %v", err)
	}
	if result.ResponseHandled {
		t.Fatal("bus fallback did not return response handling to the agent")
	}
	messageBus.Close()

	result.ResponseHandled = true
	if err := runner.deliverHandledMedia(t.Context(), request, result); !errors.Is(err, bus.ErrBusClosed) {
		t.Fatalf("closed bus media error = %v", err)
	}
}

func TestWorkflowManagedOptionAliasesAndUtilityBoundaries(t *testing.T) {
	options := workflowManagedOptions(map[string]any{
		"mode":                  "scope_split",
		"maxItemsPerChunk":      int64(3),
		"calibrationSampleSize": float64(4),
		"maxTasksPerChunk":      json.Number("5"),
		"maxParallelChildren":   "6",
		"adaptiveChunking":      false,
		"estimatedOutputTokens": 700,
		"calibration": map[string]any{
			"enabled":             false,
			"sampleSize":          8,
			"taskSampleSize":      9,
			"requiredMatches":     2,
			"maxTrials":           3,
			"cacheEnabled":        false,
			"cacheMaxInterval":    11,
			"similarityThreshold": 0.91,
		},
		"optimize": map[string]any{
			"model":            false,
			"effort":           false,
			"model_candidates": []any{},
		},
		"strategy": "task_split",
	})
	if options.maxItemsPerChunk != 3 || options.calibrationSampleSize != 8 ||
		options.maxTasksPerChunk != 5 || options.maxParallelChildren != 6 ||
		options.adaptiveChunking || options.estimatedOutputTokens != 700 ||
		options.calibrationEnabled || options.calibrationTaskSampleSize != 9 ||
		options.calibrationRequiredMatches != 2 || options.calibrationMaxTrials != 3 ||
		options.calibrationCacheEnabled || options.calibrationCacheMaxInterval != 11 ||
		options.calibrationSimilarityThreshold != 0.91 || options.modelOptimization ||
		options.effortOptimization || options.requestedSplitStrategy != "task_split" {
		t.Fatalf("camel-case managed options = %#v", options)
	}

	nested := workflowManagedOptions(map[string]any{
		"split": "scope_split",
		"optimization": map[string]any{
			"model":  map[string]any{"enabled": false, "candidates": []any{}},
			"effort": map[string]any{"enabled": false},
		},
	})
	if nested.modelOptimization || nested.effortOptimization || nested.requestedSplitStrategy != "scope_split" {
		t.Fatalf("nested managed options = %#v", nested)
	}

	if got := workflowChunkScope(nil, 2); got != nil {
		t.Fatalf("empty chunks = %#v", got)
	}
	whole := workflowChunkScope([]any{"a", "b"}, 0)
	if len(whole) != 1 || !reflect.DeepEqual(whole[0], []any{"a", "b"}) {
		t.Fatalf("whole chunk = %#v", whole)
	}
	chunks := workflowChunkScope([]any{"a", "b", "c"}, 2)
	if len(chunks) != 2 || !reflect.DeepEqual(chunks[1], []any{"c"}) {
		t.Fatalf("bounded chunks = %#v", chunks)
	}

	integerCases := map[any]int{
		int64(7):         7,
		float64(8):       8,
		json.Number("9"): 9,
		" 10 ":           10,
		json.Number("x"): 0,
		"not-an-int":     0,
	}
	for value, want := range integerCases {
		if got := intFromAny(value); got != want {
			t.Fatalf("intFromAny(%#v) = %d, want %d", value, got, want)
		}
	}
}

func TestWorkflowRuntimeProjectionHelpersCoverAllInputShapes(t *testing.T) {
	managedModes := []struct {
		input any
		want  string
	}{
		{input: false, want: "off"},
		{input: "custom", want: "custom"},
		{input: map[string]any{"enabled": false}, want: "off"},
		{input: map[string]any{"mode": "TASK_SPLIT"}, want: "task_split"},
		{input: map[string]any{"enabled": true}, want: "auto"},
		{input: struct{ Enabled bool }{Enabled: true}, want: "auto"},
	}
	for _, test := range managedModes {
		if got := workflowManagedMode(test.input); got != test.want {
			t.Fatalf("workflowManagedMode(%#v) = %q, want %q", test.input, got, test.want)
		}
	}

	mapped := []map[string]any{{"id": "a"}, {"id": "b"}}
	if got := workflowScopeItems(mapped); len(got) != 2 {
		t.Fatalf("mapped scope items = %#v", got)
	}
	if got := workflowScopeItems(map[string]any{"items": mapped}); len(got) != 2 {
		t.Fatalf("nested scope items = %#v", got)
	}
	if got := workflowScopeItems(map[string]any{"id": "one"}); len(got) != 1 {
		t.Fatalf("single scope object = %#v", got)
	}
	if got := workflowScopeItems("one"); len(got) != 1 || got[0] != "one" {
		t.Fatalf("scalar scope = %#v", got)
	}

	if key, disabled := workflowPromptCacheKey("invalid", "main", "session-1"); key != "session-1" || disabled {
		t.Fatalf("invalid cache fallback = (%q, %v)", key, disabled)
	}
	if mode := workflowCacheMode("key:   "); mode != "session" {
		t.Fatalf("blank custom cache mode = %q", mode)
	}
	if outputs := workflowToolResultOutputs(nil); len(outputs) != 0 {
		t.Fatalf("nil tool outputs = %#v", outputs)
	}
	if parsed, ok := workflowToolJSONOutput("[1,2]"); !ok || len(parsed.([]any)) != 2 {
		t.Fatalf("array tool JSON = (%#v, %v)", parsed, ok)
	}
	for _, invalid := range []string{"", "plain text", "{broken"} {
		if _, ok := workflowToolJSONOutput(invalid); ok {
			t.Fatalf("invalid tool JSON %q was accepted", invalid)
		}
	}

	scopeMessage := workflowScopeMessage(map[string]any{"invalid": math.NaN()})
	if !strings.HasPrefix(scopeMessage, "Assigned scope:\nmap[") {
		t.Fatalf("non-JSON scope message = %q", scopeMessage)
	}
	message := workflowAgentMessage(workflows.AgentRequest{Message: " direct message "})
	if message != "direct message" {
		t.Fatalf("direct workflow message = %q", message)
	}
	metadata := workflowManagedMetadata(workflows.AgentRequest{Managed: true}, nil)
	model := metadata["optimization"].(map[string]any)["model"].(map[string]any)
	if model["reason"] != "agent unavailable" {
		t.Fatalf("missing-agent metadata = %#v", metadata)
	}
}

func TestWorkflowSourceCaptureRejectsMalformedIdentityAndTranscript(t *testing.T) {
	fixture := newWorkflowRuntimeSourceFixture(t)
	invalidCaptures := []workflows.AgentSourceCapture{
		{},
		{ExecutionID: "UPPER", WorkspaceID: fixture.capture.WorkspaceID, Binding: fixture.capture.Binding},
		{ExecutionID: fixture.capture.ExecutionID, WorkspaceID: " workspace", Binding: fixture.capture.Binding},
		{
			ExecutionID: fixture.capture.ExecutionID,
			WorkspaceID: fixture.capture.WorkspaceID,
			Binding:     strings.Repeat("x", maxWorkflowAgentSourceIdentityBytes+1),
		},
	}
	for _, capture := range invalidCaptures {
		if err := validateWorkflowAgentSourceCapture(capture); err == nil {
			t.Fatalf("invalid source capture was accepted: %#v", capture)
		}
	}
	if err := validateWorkflowAgentSourceCapture(fixture.capture); err != nil {
		t.Fatalf("valid source capture: %v", err)
	}
	if _, _, err := workflowAgentSourceScope(fixture.capture, "Bad Agent"); err == nil {
		t.Fatal("noncanonical source agent was accepted")
	}
	if scope, key, err := workflowAgentSourceScope(
		fixture.capture,
		"main",
	); err != nil || key != fixture.key ||
		!reflect.DeepEqual(scope, fixture.scope) {
		t.Fatalf("source scope = (%#v, %q, %v)", scope, key, err)
	}

	invalidRequests := []struct {
		message string
		prompt  string
		output  *workflows.AgentOutputContract
	}{
		{message: "", prompt: fixture.metadata.SystemPrompt},
		{message: "message\x00", prompt: fixture.metadata.SystemPrompt},
		{message: "message", prompt: " bad prompt"},
		{
			message: "message",
			prompt:  fixture.metadata.SystemPrompt,
			output:  &workflows.AgentOutputContract{RepairAttempts: maxWorkflowAgentSourceTranscriptMessages},
		},
	}
	for _, request := range invalidRequests {
		if _, err := workflowAgentSourceRequestRevision(
			fixture.capture,
			"main",
			request.message,
			request.prompt,
			request.output,
		); err == nil {
			t.Fatalf("invalid source request was accepted: %#v", request)
		}
	}
	if _, err := workflowAgentSourceRequestRevision(
		fixture.capture,
		"main",
		"message",
		fixture.metadata.SystemPrompt,
		nil,
	); err != nil {
		t.Fatalf("valid source request: %v", err)
	}

	if _, err := workflowAgentSourceTranscriptRevision(nil); err == nil {
		t.Fatal("empty source transcript was accepted")
	}
	if _, err := workflowAgentSourceTranscriptRevision([]providers.Message{
		{Role: "assistant", Content: "wrong first role"},
		{Role: "assistant", Content: "answer"},
	}); err == nil {
		t.Fatal("malformed source transcript was accepted")
	}
	if _, err := workflowAgentSourceTranscriptRevision(fixture.history); err != nil {
		t.Fatalf("valid source transcript: %v", err)
	}
	if workflowAgentSourceRevisionValid("sha256:short") ||
		workflowAgentSourceRevisionValid("sha256:"+strings.Repeat("g", 64)) ||
		!workflowAgentSourceRevisionValid(fixture.metadata.TranscriptRevision) {
		t.Fatal("source revision validation accepted or rejected the wrong value")
	}
	oversized := fixture.metadata
	oversized.SystemPrompt = strings.Repeat("x", maxWorkflowAgentSourceMetadataBytes+1)
	if _, err := encodeWorkflowAgentSourceMetadata(oversized); err == nil {
		t.Fatal("oversized source metadata was accepted")
	}
}

func TestWorkflowSourceEnvelopeFailsClosedForEveryBinding(t *testing.T) {
	fixture := newWorkflowRuntimeSourceFixture(t)
	metadataJSON, err := json.Marshal(fixture.metadata)
	if err != nil {
		t.Fatal(err)
	}
	withMetadata := func(metadata workflowAgentSourceMetadataV1) session.SessionSnapshot {
		snapshot := workflowCloneSessionSnapshot(fixture.snapshot)
		snapshot.Summary, err = encodeWorkflowAgentSourceMetadata(metadata)
		if err != nil {
			t.Fatalf("encode test source metadata: %v", err)
		}
		return snapshot
	}

	wrongIdentity := fixture.metadata
	wrongIdentity.Version++
	wrongRequest := fixture.metadata
	wrongRequest.RequestRevision = "sha256:" + strings.Repeat("1", 64)
	wrongPrompt := fixture.metadata
	wrongPrompt.SystemPrompt = ""
	wrongTranscript := fixture.metadata
	wrongTranscript.TranscriptRevision = "sha256:" + strings.Repeat("2", 64)
	badScope := workflowCloneSessionSnapshot(fixture.snapshot)
	badScope.Scope = nil
	missingEnvelope := workflowCloneSessionSnapshot(fixture.snapshot)
	missingEnvelope.Summary = "plain summary"
	emptyEnvelope := workflowCloneSessionSnapshot(fixture.snapshot)
	emptyEnvelope.Summary = workflowAgentSourceMetadataPrefix
	invalidEncoding := workflowCloneSessionSnapshot(fixture.snapshot)
	invalidEncoding.Summary = workflowAgentSourceMetadataPrefix + "{"
	invalidUTF8 := workflowCloneSessionSnapshot(fixture.snapshot)
	invalidUTF8.Summary = workflowAgentSourceMetadataPrefix + string([]byte{0xff})
	trailing := workflowCloneSessionSnapshot(fixture.snapshot)
	trailing.Summary = workflowAgentSourceMetadataPrefix + string(metadataJSON) + " {}"
	noncanonical := workflowCloneSessionSnapshot(fixture.snapshot)
	noncanonical.Summary = workflowAgentSourceMetadataPrefix + "\n" + string(metadataJSON)

	tests := []struct {
		name     string
		snapshot session.SessionSnapshot
		want     string
	}{
		{name: "scope", snapshot: badScope, want: "scope binding"},
		{name: "envelope", snapshot: missingEnvelope, want: "missing versioned envelope"},
		{name: "empty", snapshot: emptyEnvelope, want: "envelope bound"},
		{name: "utf8", snapshot: invalidUTF8, want: "envelope bound"},
		{name: "encoding", snapshot: invalidEncoding, want: "envelope encoding"},
		{name: "trailing", snapshot: trailing, want: "trailing envelope data"},
		{name: "canonical", snapshot: noncanonical, want: "non-canonical envelope"},
		{name: "identity", snapshot: withMetadata(wrongIdentity), want: "identity binding"},
		{name: "request", snapshot: withMetadata(wrongRequest), want: "different request"},
		{name: "prompt", snapshot: withMetadata(wrongPrompt), want: "system prompt"},
		{name: "transcript", snapshot: withMetadata(wrongTranscript), want: "transcript binding"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, decodeErr := decodeWorkflowAgentSourceSnapshot(
				test.snapshot,
				"main",
				fixture.metadata.RequestRevision,
			)
			if decodeErr == nil || !strings.Contains(decodeErr.Error(), test.want) {
				t.Fatalf("decode error = %v, want %q", decodeErr, test.want)
			}
		})
	}
	decoded, err := decodeWorkflowAgentSourceSnapshot(
		fixture.snapshot,
		"main",
		fixture.metadata.RequestRevision,
	)
	if err != nil || decoded != fixture.metadata {
		t.Fatalf("valid source envelope = (%#v, %v)", decoded, err)
	}
}

func TestWorkflowSourceAdmissionRejectsUnavailableIncompleteAndTamperedState(t *testing.T) {
	fixture := newWorkflowRuntimeSourceFixture(t)
	if _, err := beginWorkflowAgentSourceExecution(
		t.Context(),
		nil,
		"main",
		fixture.capture,
		"message",
		fixture.metadata.SystemPrompt,
		nil,
	); err == nil {
		t.Fatal("nil source agent was accepted")
	}
	if _, err := beginWorkflowAgentSourceExecution(
		t.Context(),
		&AgentInstance{Sessions: &workflowRuntimeOpaqueSessionStore{}},
		"main",
		fixture.capture,
		"message",
		fixture.metadata.SystemPrompt,
		nil,
	); err == nil {
		t.Fatal("source store without exact snapshot support was accepted")
	}

	tests := []struct {
		name    string
		capture workflows.AgentSourceCapture
		store   *workflowRuntimeSourceStore
		want    string
	}{
		{
			name:    "invalid capture",
			capture: workflows.AgentSourceCapture{},
			store:   workflowRuntimeSourceStoreFor(fixture.snapshot),
			want:    "identity is invalid",
		},
		{
			name:    "admission",
			capture: fixture.capture,
			store: &workflowRuntimeSourceStore{
				found:    true,
				snapshot: fixture.snapshot,
				admitErr: errors.New("admission failed"),
			},
			want: "admit source session",
		},
		{
			name:    "read",
			capture: fixture.capture,
			store: &workflowRuntimeSourceStore{
				found:   true,
				readErr: errors.New("read failed"),
			},
			want: "read source session",
		},
		{
			name:    "incomplete",
			capture: fixture.capture,
			store: workflowRuntimeSourceStoreFor(session.SessionSnapshot{
				Key:      fixture.key,
				Revision: "revision",
				Scope:    session.CloneScope(&fixture.scope),
				History:  fixture.history[:1],
			}),
			want: "incomplete protected snapshot",
		},
		{
			name:    "tampered",
			capture: fixture.capture,
			store: workflowRuntimeSourceStoreFor(session.SessionSnapshot{
				Key:      fixture.key,
				Revision: "revision",
				Scope:    session.CloneScope(&fixture.scope),
				History:  session.CloneMessages(fixture.history),
				Summary:  "tampered",
			}),
			want: "missing versioned envelope",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := beginWorkflowAgentSourceExecution(
				t.Context(),
				&AgentInstance{Sessions: test.store},
				"main",
				test.capture,
				"message",
				fixture.metadata.SystemPrompt,
				nil,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("begin source execution error = %v, want %q", err, test.want)
			}
		})
	}

	freshStore := workflowRuntimeSourceStoreFor(session.SessionSnapshot{
		Key:      fixture.key,
		Revision: "reserved",
		Scope:    session.CloneScope(&fixture.scope),
	})
	fresh, err := beginWorkflowAgentSourceExecution(
		t.Context(),
		&AgentInstance{Sessions: freshStore},
		"main",
		fixture.capture,
		"message",
		fixture.metadata.SystemPrompt,
		nil,
	)
	if err != nil || fresh.replay != nil {
		t.Fatalf("fresh source execution = (%#v, %v)", fresh, err)
	}
	fresh.unlock()

	replayStore := workflowRuntimeSourceStoreFor(fixture.snapshot)
	replay, err := beginWorkflowAgentSourceExecution(
		t.Context(),
		&AgentInstance{Sessions: replayStore},
		"main",
		fixture.capture,
		"message",
		fixture.metadata.SystemPrompt,
		nil,
	)
	if err != nil || replay.replay == nil {
		t.Fatalf("replayed source execution = (%#v, %v)", replay, err)
	}
	replay.unlock()
}

func TestWorkflowSourceCompletionHandlesPersistenceConflictsExactly(t *testing.T) {
	fixture := newWorkflowRuntimeSourceFixture(t)
	if _, err := (*workflowAgentSourceExecution)(nil).complete(t.Context(), fixture.history); err == nil {
		t.Fatal("nil source execution completed")
	}
	if _, err := fixture.execution(workflowRuntimeSourceStoreFor(fixture.snapshot)).
		complete(t.Context(), nil); err == nil {
		t.Fatal("source execution accepted an empty transcript")
	}

	replayExecution := fixture.execution(workflowRuntimeSourceStoreFor(fixture.snapshot))
	replayExecution.replay = &fixture.snapshot
	outputs, replayErr := replayExecution.complete(t.Context(), fixture.history)
	if replayErr != nil || outputs["source_revision"] != fixture.snapshot.Revision {
		t.Fatalf("source replay outputs = (%#v, %v)", outputs, replayErr)
	}

	persistFailure := workflowRuntimeSourceStoreFor(fixture.snapshot)
	persistFailure.replaceErr = errors.New("disk failed")
	if _, err := fixture.execution(persistFailure).
		complete(t.Context(), fixture.history); err == nil ||
		!strings.Contains(err.Error(), "persist source session") {
		t.Fatalf("source persistence error = %v", err)
	}

	conflict := workflowRuntimeSourceStoreFor(fixture.snapshot)
	conflict.replaceErr = session.ErrSnapshotConflict
	conflict.snapshot.Summary = "different"
	if _, err := fixture.execution(conflict).
		complete(t.Context(), fixture.history); err == nil ||
		!strings.Contains(err.Error(), "conflicting completed execution") {
		t.Fatalf("source conflict error = %v", err)
	}

	concurrent := workflowRuntimeSourceStoreFor(fixture.snapshot)
	concurrent.replaceErr = session.ErrSnapshotConflict
	concurrent.replaceHook = func(replacement session.SessionSnapshotReplacement) {
		concurrent.accept(replacement)
	}
	outputs, replayErr = fixture.execution(concurrent).complete(t.Context(), fixture.history)
	if replayErr != nil || outputs["source_revision"] != "revision-after-replace" {
		t.Fatalf("identical concurrent completion = (%#v, %v)", outputs, replayErr)
	}

	invalidVerification := workflowRuntimeSourceStoreFor(fixture.snapshot)
	invalidVerification.replaceHook = func(session.SessionSnapshotReplacement) {
		invalidVerification.snapshot.Summary = "invalid"
	}
	if _, err := fixture.execution(invalidVerification).
		complete(t.Context(), fixture.history); err == nil ||
		!strings.Contains(err.Error(), "verify source session") {
		t.Fatalf("invalid source verification error = %v", err)
	}

	success := workflowRuntimeSourceStoreFor(fixture.snapshot)
	outputs, replayErr = fixture.execution(success).complete(t.Context(), fixture.history)
	if replayErr != nil || outputs["source_revision"] != "revision-after-replace" {
		t.Fatalf("source completion = (%#v, %v)", outputs, replayErr)
	}
}

func TestWorkflowReadOnlySnapshotBoundariesFailClosed(t *testing.T) {
	if _, _, err := workflowReadOnlySessionSnapshot(t.Context(), nil, "main", "key"); err == nil {
		t.Fatal("nil read-only agent was accepted")
	}
	if _, _, err := workflowReadOnlySessionSnapshot(
		t.Context(),
		&AgentInstance{Sessions: &workflowRuntimeOpaqueSessionStore{}},
		"main",
		"key",
	); err == nil {
		t.Fatal("session store without snapshot support was accepted")
	}
	reader := &workflowRuntimeSourceStore{readErr: errors.New("read failed")}
	if _, _, err := workflowReadOnlySessionSnapshot(
		t.Context(),
		&AgentInstance{Sessions: reader},
		"main",
		"key",
	); err == nil {
		t.Fatal("snapshot read failure was ignored")
	}
	reader.readErr = nil
	if _, _, err := workflowReadOnlySessionSnapshot(
		t.Context(),
		&AgentInstance{Sessions: reader},
		"main",
		"key",
	); err == nil {
		t.Fatal("missing snapshot was accepted")
	}
	if _, _, err := workflowReadOnlySessionSnapshot(
		t.Context(),
		&AgentInstance{Sessions: reader},
		"main",
		string([]byte{0xff}),
	); err == nil {
		t.Fatal("invalid UTF-8 snapshot key was accepted")
	}

	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "web",
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": "one"},
	}
	key := session.BuildSessionKey(scope)
	snapshot := session.SessionSnapshot{
		Key:      key,
		Scope:    session.CloneScope(&scope),
		Revision: "stored-revision",
		History:  []providers.Message{{Role: "user", Content: "question"}},
	}
	reader = workflowRuntimeSourceStoreFor(snapshot)
	loaded, revision, err := workflowReadOnlySessionSnapshot(
		t.Context(),
		&AgentInstance{Sessions: reader},
		"main",
		key,
	)
	if err != nil || loaded.Key != key || revision == "" {
		t.Fatalf("valid read-only snapshot = (%#v, %q, %v)", loaded, revision, err)
	}

	badKey := workflowCloneSessionSnapshot(snapshot)
	badKey.Key = "different"
	if _, _, err := workflowReadOnlySessionSnapshot(
		t.Context(),
		&AgentInstance{Sessions: workflowRuntimeSourceStoreFor(badKey)},
		"main",
		key,
	); err == nil {
		t.Fatal("snapshot with mismatched scope key was accepted")
	}

	invalidOwner := workflowCloneSessionSnapshot(snapshot)
	invalidOwner.Scope.AgentID = "Bad Agent"
	if err := workflowReadOnlySessionOwner(invalidOwner, "main"); err == nil {
		t.Fatal("invalid scope owner was accepted")
	}
	if err := workflowReadOnlySessionOwner(session.SessionSnapshot{Key: "opaque"}, "main"); err == nil {
		t.Fatal("snapshot without owner metadata was accepted")
	}
	if err := workflowReadOnlySessionOwner(snapshot, "other"); err == nil {
		t.Fatal("snapshot owned by another agent was accepted")
	}

	nanSnapshot := workflowCloneSessionSnapshot(snapshot)
	nanSnapshot.History[0].ToolCalls = []providers.ToolCall{{Arguments: map[string]any{"bad": math.NaN()}}}
	if _, err := workflowSessionSnapshotRevision(nanSnapshot); err == nil {
		t.Fatal("nonserializable snapshot generated a revision")
	}
}

func TestWorkflowFrozenReadOnlySnapshotRejectsIdentityAndRevisionDrift(t *testing.T) {
	if _, _, err := workflowFrozenReadOnlySessionSnapshot(t.Context(), nil, "main"); err == nil {
		t.Fatal("nil frozen snapshot was accepted")
	}
	scope := session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "web",
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": "one"},
	}
	snapshot := session.SessionSnapshot{
		Key:     session.BuildSessionKey(scope),
		Scope:   session.CloneScope(&scope),
		History: []providers.Message{{Role: "user", Content: "question"}},
	}
	revision, err := workflowSessionSnapshotRevision(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	valid := workflows.FrozenReadOnlySession{
		AgentID:         "main",
		Snapshot:        snapshot,
		HistoryRevision: revision,
		FrozenMedia:     media.FrozenSet{Version: media.FrozenSetVersion},
	}
	tests := []struct {
		name   string
		mutate func(*workflows.FrozenReadOnlySession)
	}{
		{name: "agent format", mutate: func(value *workflows.FrozenReadOnlySession) { value.AgentID = " Main" }},
		{name: "agent mismatch", mutate: func(value *workflows.FrozenReadOnlySession) { value.AgentID = "other" }},
		{name: "key format", mutate: func(value *workflows.FrozenReadOnlySession) { value.Snapshot.Key = " bad" }},
		{
			name:   "scope key",
			mutate: func(value *workflows.FrozenReadOnlySession) { value.Snapshot.Scope.Values["chat"] = "two" },
		},
		{name: "revision", mutate: func(value *workflows.FrozenReadOnlySession) { value.HistoryRevision = "stale" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Snapshot = workflowCloneSessionSnapshot(valid.Snapshot)
			test.mutate(&candidate)
			if _, _, snapshotErr := workflowFrozenReadOnlySessionSnapshot(
				t.Context(),
				&candidate,
				"main",
			); snapshotErr == nil {
				t.Fatalf("invalid frozen snapshot was accepted: %#v", candidate)
			}
		})
	}
	materialized, gotRevision, err := workflowFrozenReadOnlySessionSnapshot(t.Context(), &valid, "main")
	if err != nil || materialized.Key != snapshot.Key || gotRevision != revision {
		t.Fatalf("valid frozen snapshot = (%#v, %q, %v)", materialized, gotRevision, err)
	}
}

func TestWorkflowStructuredAgentReportsInitialRepairAndValidationFailures(t *testing.T) {
	contract := &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"answer"},
		},
	}
	initialErr := errors.New("initial failed")
	if _, result, repairs, _, err := workflowRunStructuredAgentWithOptions(
		"message",
		contract,
		func(string, bool, workflowAgentRunOptions) (string, error) { return "", initialErr },
		workflowAgentRunOptions{},
	); !errors.Is(err, initialErr) || result.Valid || repairs != 0 {
		t.Fatalf("initial structured failure = (%#v, %d, %v)", result, repairs, err)
	}

	repairErr := errors.New("repair failed")
	calls := 0
	if _, result, repairs, _, err := workflowRunStructuredAgentWithOptions(
		"message",
		contract,
		func(string, bool, workflowAgentRunOptions) (string, error) {
			calls++
			if calls == 1 {
				return `{}`, nil
			}
			return "", repairErr
		},
		workflowAgentRunOptions{},
	); !errors.Is(err, repairErr) || result.Valid || repairs != 1 {
		t.Fatalf("structured repair failure = (%#v, %d, %v)", result, repairs, err)
	}

	contract.RepairAttempts = 0
	if _, result, repairs, _, err := workflowRunStructuredAgentWithOptions(
		"message",
		contract,
		func(string, bool, workflowAgentRunOptions) (string, error) { return `{}`, nil },
		workflowAgentRunOptions{},
	); err == nil || result.Valid || repairs != 0 {
		t.Fatalf("unrepaired structured failure = (%#v, %d, %v)", result, repairs, err)
	}
}

type workflowRuntimeSourceFixture struct {
	capture  workflows.AgentSourceCapture
	scope    session.SessionScope
	key      string
	history  []providers.Message
	metadata workflowAgentSourceMetadataV1
	snapshot session.SessionSnapshot
}

func newWorkflowRuntimeSourceFixture(t *testing.T) workflowRuntimeSourceFixture {
	t.Helper()
	fixture := workflowRuntimeSourceFixture{
		capture: workflows.AgentSourceCapture{
			ExecutionID: "aix_11111111111111111111111111111111",
			WorkspaceID: "prw_11111111111111111111111111111111",
			Binding:     "sha256:source-binding",
		},
		history: []providers.Message{
			{Role: "user", Content: "question"},
			{Role: "assistant", Content: "answer"},
		},
	}
	var err error
	fixture.scope, fixture.key, err = workflowAgentSourceScope(fixture.capture, "main")
	if err != nil {
		t.Fatal(err)
	}
	transcriptRevision, err := workflowAgentSourceTranscriptRevision(fixture.history)
	if err != nil {
		t.Fatal(err)
	}
	requestRevision, err := workflowAgentSourceRequestRevision(
		fixture.capture,
		"main",
		"message",
		"Exact source system prompt.",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.metadata = workflowAgentSourceMetadataV1{
		Version:            workflowAgentSourceMetadataVersion,
		ExecutionID:        fixture.capture.ExecutionID,
		WorkspaceID:        fixture.capture.WorkspaceID,
		Binding:            fixture.capture.Binding,
		AgentID:            "main",
		Tools:              workflows.AgentToolsNone,
		SystemPrompt:       "Exact source system prompt.",
		RequestRevision:    requestRevision,
		TranscriptRevision: transcriptRevision,
	}
	summary, err := encodeWorkflowAgentSourceMetadata(fixture.metadata)
	if err != nil {
		t.Fatal(err)
	}
	fixture.snapshot = session.SessionSnapshot{
		Key:      fixture.key,
		History:  session.CloneMessages(fixture.history),
		Summary:  summary,
		Scope:    session.CloneScope(&fixture.scope),
		Revision: "source-revision",
	}
	return fixture
}

func (fixture workflowRuntimeSourceFixture) execution(
	store *workflowRuntimeSourceStore,
) *workflowAgentSourceExecution {
	return &workflowAgentSourceExecution{
		capture:         fixture.capture,
		agentID:         "main",
		key:             fixture.key,
		scope:           fixture.scope,
		store:           store,
		requestRevision: fixture.metadata.RequestRevision,
		systemPrompt:    fixture.metadata.SystemPrompt,
		initialRevision: fixture.snapshot.Revision,
	}
}

type workflowRuntimeOpaqueSessionStore struct {
	session.SessionStore
}

type workflowRuntimeSourceStore struct {
	session.SessionStore
	snapshot    session.SessionSnapshot
	found       bool
	readErr     error
	admitErr    error
	replaceErr  error
	replaceHook func(session.SessionSnapshotReplacement)
}

func workflowRuntimeSourceStoreFor(snapshot session.SessionSnapshot) *workflowRuntimeSourceStore {
	return &workflowRuntimeSourceStore{snapshot: workflowCloneSessionSnapshot(snapshot), found: true}
}

func (store *workflowRuntimeSourceStore) AdmitSessionScope(
	context.Context,
	session.SessionScopeAdmission,
) (bool, error) {
	return store.admitErr == nil, store.admitErr
}

func (store *workflowRuntimeSourceStore) ReadSessionSnapshot(
	context.Context,
	string,
) (session.SessionSnapshot, bool, error) {
	return workflowCloneSessionSnapshot(store.snapshot), store.found, store.readErr
}

func (store *workflowRuntimeSourceStore) ReplaceSessionSnapshot(
	_ context.Context,
	replacement session.SessionSnapshotReplacement,
) error {
	if store.replaceHook != nil {
		store.replaceHook(replacement)
	} else if store.replaceErr == nil {
		store.accept(replacement)
	}
	return store.replaceErr
}

func (store *workflowRuntimeSourceStore) accept(replacement session.SessionSnapshotReplacement) {
	store.snapshot = session.SessionSnapshot{
		Key:      replacement.Key,
		History:  session.CloneMessages(replacement.History),
		Summary:  replacement.Summary,
		Scope:    session.CloneScope(replacement.Scope),
		Aliases:  append([]string(nil), replacement.Aliases...),
		Revision: "revision-after-replace",
	}
	store.found = true
}

type workflowRuntimeResultTool struct {
	name   string
	result *tools.ToolResult
	ctx    context.Context
	args   map[string]any
}

func (tool *workflowRuntimeResultTool) Name() string { return tool.name }

func (*workflowRuntimeResultTool) Description() string { return "workflow runtime boundary test tool" }

func (*workflowRuntimeResultTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}

func (tool *workflowRuntimeResultTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	tool.ctx = ctx
	tool.args = cloneAnyMap(args)
	return tool.result
}

type workflowRuntimeFailingMediaManager struct {
	workflowMediaChannelManager
	sendErr error
}

func (manager *workflowRuntimeFailingMediaManager) SendMedia(
	context.Context,
	bus.OutboundMediaMessage,
) error {
	return manager.sendErr
}
