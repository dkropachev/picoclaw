package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
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

func TestWorkflowManagedScopeSplitCombinesStructuredOutputs(t *testing.T) {
	contract := &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"summary", "findings"},
			"properties": map[string]any{
				"summary":  map[string]any{"type": "string"},
				"findings": map[string]any{"type": "array"},
			},
		},
	}
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_items_per_chunk": 2,
			"calibration": map[string]any{
				"enabled":     true,
				"sample_size": 3,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
			map[string]any{"id": "d"},
			map[string]any{"id": "e"},
		},
		Output: contract,
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		ids := workflowTestScopeIDs(t, message)
		findings := make([]string, 0, len(ids))
		for _, id := range ids {
			findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"title":"finding %s"}`, id, id))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	structured, ok := outputs["structured"].(map[string]any)
	if !ok {
		t.Fatalf("structured = %#v, want object", outputs["structured"])
	}
	findings, ok := structured["findings"].([]any)
	if !ok || len(findings) != 5 {
		t.Fatalf("findings = %#v, want five combined findings", structured["findings"])
	}
	managed, ok := outputs["managed"].(map[string]any)
	if !ok {
		t.Fatalf("managed = %#v, want metadata", outputs["managed"])
	}
	if managed["strategy"] != "scope_split" {
		t.Fatalf("strategy = %#v, want scope_split", managed["strategy"])
	}
	calibration := managed["calibration"].(map[string]any)
	if calibration["match"] != true {
		t.Fatalf("calibration = %#v, want match", calibration)
	}
	split := managed["split"].(map[string]any)
	if split["child_count"] != 3 {
		t.Fatalf("split child_count = %#v, want 3", split["child_count"])
	}
	tokenEfficiency, ok := split["token_efficiency"].(map[string]any)
	if !ok {
		t.Fatalf("token_efficiency = %#v, want metadata", split["token_efficiency"])
	}
	childTokens, ok := tokenEfficiency["child_prompt_tokens"].([]int)
	if !ok || len(childTokens) != 3 {
		t.Fatalf("child_prompt_tokens = %#v, want one estimate per child", tokenEfficiency["child_prompt_tokens"])
	}
}

func TestWorkflowAgentRunnerRejectsUnknownExplicitAgent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, &mockProvider{})
	defer al.Close()

	_, err := (&workflowAgentRunner{loop: al}).RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "reviewer",
		Prompt:  "Review this.",
	})
	if err == nil || !strings.Contains(err.Error(), `workflow agent "reviewer" not found`) {
		t.Fatalf("RunAgent() error = %v, want unknown agent error", err)
	}
}

func TestWorkflowAgentRunnerWithNoToolsDoesNotInitializeMCP(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "mcp-command-started")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	cfg.Tools.MCP = config.MCPConfig{
		ToolConfig: config.ToolConfig{Enabled: true},
		Servers: map[string]config.MCPServerConfig{
			"private-server": {
				Enabled: true,
				Command: "sh",
				Args: []string{
					"-c",
					`printf started > "$1"`,
					"workflow-agent-test",
					marker,
				},
			},
		},
	}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, &mockProvider{})
	defer al.Close()

	outputs, err := (&workflowAgentRunner{loop: al}).RunAgent(
		context.Background(),
		workflows.AgentRequest{
			AgentID: "main",
			Prompt:  "Review this without tools.",
			History: "none",
			Tools:   workflows.AgentToolsNone,
		},
	)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["tools"] != workflows.AgentToolsNone {
		t.Fatalf("tools audit = %#v, want none", outputs["tools"])
	}
	if al.mcp.getManager() != nil || al.mcp.getInitErr() != nil {
		t.Fatal("no-tools agent run initialized or mutated MCP runtime state")
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("no-tools agent run executed MCP command: %v", statErr)
	}
}

func TestWorkflowAgentRunnerReadOnlyUsesExactImmutableSessionSnapshot(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{`{"needs_user":false,"reason":"clear"}`}}
	loop, agent, canonicalKey, alias := newWorkflowReadOnlyTestLoop(t, provider)
	reader := agent.Sessions.(session.SnapshotReader)
	before, found, err := reader.ReadSessionSnapshot(context.Background(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (%#v, %v, %v), want existing snapshot", before, found, err)
	}
	beforeSessions := append([]string(nil), agent.Sessions.ListSessions()...)

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflows.AgentRequest{
			AgentID: "main",
			Prompt:  "Decide whether this needs user attention.",
			Session: alias,
			History: "read_only",
			Cache:   "session",
			Tools:   workflows.AgentToolsNone,
			Output: &workflows.AgentOutputContract{
				Format: "json",
				Schema: map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []any{"needs_user", "reason"},
					"properties": map[string]any{
						"needs_user": map[string]any{"type": "boolean"},
						"reason":     map[string]any{"type": "string"},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["session"] != canonicalKey {
		t.Fatalf("session output = %#v, want canonical %q", outputs["session"], canonicalKey)
	}
	if revision, _ := outputs["history_revision"].(string); !strings.HasPrefix(revision, "sha256:") {
		t.Fatalf("history_revision = %#v, want opaque sha256 revision", outputs["history_revision"])
	}
	if outputs["tools"] != workflows.AgentToolsNone {
		t.Fatalf("tools output = %#v, want none", outputs["tools"])
	}

	calls := provider.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	if calls[0].toolCount != 0 {
		t.Fatalf("provider tool definitions = %d, want 0", calls[0].toolCount)
	}
	if calls[0].promptCacheKey != canonicalKey {
		t.Fatalf("prompt cache key = %q, want canonical session", calls[0].promptCacheKey)
	}
	if !workflowMessagesContain(calls[0].messages, "existing problem context") {
		t.Fatalf("provider prompt omitted exact session history: %#v", calls[0].messages)
	}
	if !workflowMessagesContain(calls[0].messages, "existing decision summary") {
		t.Fatalf("provider prompt omitted exact session summary: %#v", calls[0].messages)
	}

	after, found, err := reader.ReadSessionSnapshot(context.Background(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot(after) = (%#v, %v, %v)", after, found, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("source session mutated\nbefore: %#v\nafter:  %#v", before, after)
	}
	if afterSessions := agent.Sessions.ListSessions(); !reflect.DeepEqual(afterSessions, beforeSessions) {
		t.Fatalf("session catalog changed: before=%v after=%v", beforeSessions, afterSessions)
	}
	if loop.mcp.getManager() != nil || loop.mcp.getInitErr() != nil {
		t.Fatal("read-only decision initialized MCP")
	}
}

func TestWorkflowAgentRunnerReadOnlyRejectsMissingIdentityBeforeProvider(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, agent, _, _ := newWorkflowReadOnlyTestLoop(t, provider)
	beforeSessions := append([]string(nil), agent.Sessions.ListSessions()...)

	requests := []workflows.AgentRequest{
		{AgentID: "main", History: "read_only", Tools: workflows.AgentToolsNone, Prompt: "decide"},
		{AgentID: "main", Session: "agent:main:missing", History: "read_only", Tools: workflows.AgentToolsNone, Prompt: "decide"},
		{AgentID: "Main", Session: "agent:main:review", History: "read_only", Tools: workflows.AgentToolsNone, Prompt: "decide"},
		{AgentID: "@@@", Session: "agent:main:review", History: "read_only", Tools: workflows.AgentToolsNone, Prompt: "decide"},
	}
	for _, req := range requests {
		if _, err := (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), req); err == nil {
			t.Fatalf("RunAgent(%#v) error = nil, want fail-closed identity error", req)
		}
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("provider calls = %d, want 0", len(calls))
	}
	if afterSessions := agent.Sessions.ListSessions(); !reflect.DeepEqual(afterSessions, beforeSessions) {
		t.Fatalf("failed lookups changed session catalog: before=%v after=%v", beforeSessions, afterSessions)
	}
}

func TestWorkflowAgentRunnerReadOnlyRejectsOwnerMismatchAndToolCalls(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{"unexpected"}}
	loop, agent, canonicalKey, _ := newWorkflowReadOnlyTestLoop(t, provider)
	metadata := agent.Sessions.(session.MetadataAwareSessionStore)
	metadata.EnsureSessionMetadata(canonicalKey, &session.SessionScope{
		Version: session.ScopeVersionV1,
		AgentID: "support",
		Channel: "web",
	}, nil)

	_, err := (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "main",
		Session: canonicalKey,
		History: "read_only",
		Tools:   workflows.AgentToolsNone,
		Prompt:  "decide",
	})
	if err == nil || !strings.Contains(err.Error(), "belongs to another agent") {
		t.Fatalf("owner mismatch error = %v", err)
	}
	if calls := provider.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("owner mismatch provider calls = %d, want 0", len(calls))
	}

	metadata.EnsureSessionMetadata(canonicalKey, &session.SessionScope{
		Version: session.ScopeVersionV1,
		AgentID: "main",
		Channel: "web",
	}, nil)
	provider.setResponses([]string{""})
	provider.toolCall = true
	_, err = (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "main",
		Session: canonicalKey,
		History: "read_only",
		Tools:   workflows.AgentToolsNone,
		Prompt:  "decide",
	})
	if err == nil || !strings.Contains(err.Error(), "returned tool calls") {
		t.Fatalf("tool-call response error = %v", err)
	}
}

func TestWorkflowAgentRunnerReadOnlyDoesNotClobberConcurrentAppend(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{"decision"},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	loop, agent, canonicalKey, _ := newWorkflowReadOnlyTestLoop(t, provider)
	loop.activeTurnStates.Store(canonicalKey, &turnState{sessionKey: canonicalKey})
	defer loop.activeTurnStates.Delete(canonicalKey)

	errCh := make(chan error, 1)
	go func() {
		_, err := (&workflowAgentRunner{loop: loop}).RunAgent(context.Background(), workflows.AgentRequest{
			AgentID: "main",
			Session: canonicalKey,
			History: "read_only",
			Tools:   workflows.AgentToolsNone,
			Prompt:  "decide",
		})
		errCh <- err
	}()
	<-provider.started
	agent.Sessions.AddMessage(canonicalKey, "user", "legitimate concurrent append")
	close(provider.release)
	if err := <-errCh; err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	history := agent.Sessions.GetHistory(canonicalKey)
	if !workflowMessagesContain(history, "legitimate concurrent append") {
		t.Fatalf("concurrent append was lost: %#v", history)
	}
	calls := provider.snapshotCalls()
	if len(calls) != 1 || workflowMessagesContain(calls[0].messages, "legitimate concurrent append") {
		t.Fatalf("decision did not use one frozen pre-append snapshot: %#v", calls)
	}
}

func TestWorkflowAgentRunnerReadOnlyRepairReusesFrozenSnapshot(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{
			"not json",
			`{"needs_user":true,"reason":"ambiguous"}`,
		},
		mutateNestedInput: true,
	}
	loop, agent, canonicalKey, _ := newWorkflowReadOnlyTestLoop(t, provider)
	agent.Sessions.AddMessage(canonicalKey, "user", "inspect nested immutable context")
	agent.Sessions.AddFullMessage(canonicalKey, providers.Message{
		Role:    "assistant",
		Content: "nested immutable context",
		SystemParts: []providers.ContentBlock{{
			Type:         "text",
			Text:         "frozen nested block",
			CacheControl: &providers.CacheControl{Type: "ephemeral"},
		}},
		ToolCalls: []providers.ToolCall{{
			ID:        "prior-call",
			Function:  &providers.FunctionCall{Name: "inspect", Arguments: `{}`},
			Arguments: map[string]any{"nested": map[string]any{"marker": "original"}},
		}},
	})
	agent.Sessions.AddFullMessage(canonicalKey, providers.Message{
		Role:       "tool",
		ToolCallID: "prior-call",
		Content:    "inspection complete",
	})
	provider.afterCall = func(index int) {
		if index == 0 {
			agent.Sessions.AddMessage(canonicalKey, "user", "arrived between decision and repair")
		}
	}

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflows.AgentRequest{
			AgentID: "main",
			Session: canonicalKey,
			History: "read_only",
			Tools:   workflows.AgentToolsNone,
			Prompt:  "decide",
			Output: &workflows.AgentOutputContract{
				Format:         "json",
				RepairAttempts: 1,
				Schema: map[string]any{
					"type":     "object",
					"required": []any{"needs_user", "reason"},
					"properties": map[string]any{
						"needs_user": map[string]any{"type": "boolean"},
						"reason":     map[string]any{"type": "string"},
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if outputs["structured_repairs"] != 1 || outputs["structured_valid"] != true {
		t.Fatalf("structured outputs = %#v, want one valid repair", outputs)
	}
	calls := provider.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want initial plus repair", len(calls))
	}
	for index, call := range calls {
		if workflowMessagesContain(call.messages, "arrived between decision and repair") {
			t.Fatalf("provider call %d reread live session instead of frozen snapshot", index)
		}
		if call.toolCount != 0 {
			t.Fatalf("provider call %d tool definitions = %d, want 0", index, call.toolCount)
		}
		if cacheControl := workflowFrozenCacheControl(call.messages); cacheControl != "ephemeral" {
			t.Fatalf("provider call %d frozen cache control = %q, want ephemeral: %#v", index, cacheControl, call.messages)
		}
	}
	if !workflowMessagesContain(agent.Sessions.GetHistory(canonicalKey), "arrived between decision and repair") {
		t.Fatal("legitimate append between decision and repair was lost")
	}
}

func TestWorkflowAgentRunnerReadOnlyManagedChildrenReuseFrozenSnapshot(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{responses: []string{
		workflowManagedTestFindingsJSON([]string{"a"}),
		workflowManagedTestFindingsJSON([]string{"b"}),
	}}
	loop, agent, canonicalKey, _ := newWorkflowReadOnlyTestLoop(t, provider)
	provider.afterCall = func(index int) {
		if index == 0 {
			agent.Sessions.AddMessage(canonicalKey, "user", "arrived between managed children")
		}
	}

	outputs, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflows.AgentRequest{
			AgentID: "main",
			Session: canonicalKey,
			History: "read_only",
			Tools:   workflows.AgentToolsNone,
			Prompt:  "review the assigned scope",
			Managed: map[string]any{
				"mode":                  "auto",
				"max_items_per_chunk":   1,
				"max_parallel_children": 1,
				"calibration": map[string]any{
					"enabled": false,
				},
			},
			Scope: []any{
				map[string]any{"id": "a"},
				map[string]any{"id": "b"},
			},
			Output: workflowManagedTestOutputContract(),
		},
	)
	if err != nil {
		t.Fatalf("RunAgent() error = %v", err)
	}
	if revision, _ := outputs["history_revision"].(string); !strings.HasPrefix(revision, "sha256:") {
		t.Fatalf("history_revision = %#v, want frozen revision", outputs["history_revision"])
	}
	if outputs["session"] != canonicalKey {
		t.Fatalf("session = %#v, want canonical %q", outputs["session"], canonicalKey)
	}
	calls := provider.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want two managed children", len(calls))
	}
	for index, call := range calls {
		if call.toolCount != 0 {
			t.Fatalf("managed call %d tool definitions = %d, want 0", index, call.toolCount)
		}
		if !workflowMessagesContain(call.messages, "existing problem context") {
			t.Fatalf("managed call %d omitted frozen history", index)
		}
		if workflowMessagesContain(call.messages, "arrived between managed children") {
			t.Fatalf("managed call %d reread live history", index)
		}
	}
	if !workflowMessagesContain(agent.Sessions.GetHistory(canonicalKey), "arrived between managed children") {
		t.Fatal("append between managed children was lost")
	}
}

func TestWorkflowAgentRunnerReadOnlyPropagatesCancellationWithoutWrites(t *testing.T) {
	provider := &workflowReadOnlyCaptureProvider{
		responses: []string{"unexpected"},
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	loop, agent, canonicalKey, _ := newWorkflowReadOnlyTestLoop(t, provider)
	reader := agent.Sessions.(session.SnapshotReader)
	before, found, err := reader.ReadSessionSnapshot(context.Background(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot() = (%#v, %v, %v)", before, found, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, runErr := (&workflowAgentRunner{loop: loop}).RunAgent(ctx, workflows.AgentRequest{
			AgentID: "main",
			Session: canonicalKey,
			History: "read_only",
			Tools:   workflows.AgentToolsNone,
			Prompt:  "decide",
		})
		errCh <- runErr
	}()
	<-provider.started
	cancel()
	if runErr := <-errCh; !errors.Is(runErr, context.Canceled) {
		t.Fatalf("RunAgent() error = %v, want context.Canceled", runErr)
	}
	after, found, err := reader.ReadSessionSnapshot(context.Background(), canonicalKey)
	if err != nil || !found {
		t.Fatalf("ReadSessionSnapshot(after) = (%#v, %v, %v)", after, found, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("canceled decision mutated source session\nbefore: %#v\nafter: %#v", before, after)
	}
}

func TestWorkflowSessionSnapshotRevisionIncludesProviderVisibleInternalFields(t *testing.T) {
	base := session.SessionSnapshot{
		Key: "agent:main:direct:revision",
		History: []providers.Message{{
			Role:         "assistant",
			Content:      "prior call",
			PromptLayer:  "history",
			PromptSlot:   "conversation",
			PromptSource: "session",
			SystemParts: []providers.ContentBlock{{
				Type:         "text",
				Text:         "policy",
				PromptLayer:  "policy",
				PromptSlot:   "repo",
				PromptSource: "workspace",
			}},
			ToolCalls: []providers.ToolCall{{
				ID:               "call-1",
				Name:             "inspect-a",
				Arguments:        map[string]any{"path": "a.go"},
				ThoughtSignature: "signature-a",
				Function: &providers.FunctionCall{
					Name:      "same-json-function",
					Arguments: `{}`,
				},
			}},
		}},
	}
	first, err := workflowSessionSnapshotRevision(base)
	if err != nil {
		t.Fatalf("workflowSessionSnapshotRevision(base) error = %v", err)
	}

	changed := base
	changed.History = session.CloneMessages(base.History)
	changed.History[0].ToolCalls[0].Name = "inspect-b"
	changed.History[0].ToolCalls[0].Arguments["path"] = "b.go"
	changed.History[0].ToolCalls[0].ThoughtSignature = "signature-b"
	changed.History[0].PromptSource = "different-session-source"
	changed.History[0].SystemParts[0].PromptSource = "different-policy-source"
	second, err := workflowSessionSnapshotRevision(changed)
	if err != nil {
		t.Fatalf("workflowSessionSnapshotRevision(changed) error = %v", err)
	}
	if first == second {
		t.Fatalf("provider-visible internal field changes produced identical revision %q", first)
	}

	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	changed.History[0].ToolCalls[0].Arguments = cyclic
	if revision, err := workflowSessionSnapshotRevision(changed); err == nil || revision != "" {
		t.Fatalf("cyclic snapshot revision = (%q, %v), want serialization failure", revision, err)
	}
}

func TestWorkflowAgentRunnerEnforcesAndAuditsToolsMode(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	provider := &workflowToolsCaptureProvider{responses: []string{
		`{"category":"bug","model_prose":"post this verbatim"}`,
		`{"category":"bug"}`,
		"classified",
	}}
	al := newTestAgentLoopWithStrictModels(cfg, msgBus, provider)
	defer al.Close()
	defaultAgent := al.GetRegistry().GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("default agent is nil")
	}
	defaultAgent.Tools.Register(&workflowHandledMediaTool{})

	runner := &workflowAgentRunner{loop: al}
	isolated, err := runner.RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "main",
		Prompt:  "Classify untrusted content.",
		History: "none",
		Tools:   workflows.AgentToolsNone,
		Output: &workflows.AgentOutputContract{
			Format:         "json",
			RepairAttempts: 1,
			Schema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"category"},
				"properties": map[string]any{
					"category": map[string]any{
						"type": "string",
						"enum": []any{"bug", "other"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("isolated RunAgent() error = %v", err)
	}
	inherited, err := runner.RunAgent(context.Background(), workflows.AgentRequest{
		AgentID: "main",
		Prompt:  "Use configured tools.",
		History: "none",
		Tools:   workflows.AgentToolsInherit,
	})
	if err != nil {
		t.Fatalf("inherited RunAgent() error = %v", err)
	}

	calls := provider.ToolCounts()
	if len(calls) != 3 {
		t.Fatalf("provider calls = %v, want isolated request, repair, and inherited request", calls)
	}
	if calls[0] != 0 || calls[1] != 0 {
		t.Fatalf("isolated provider tool counts = %v, want 0 for request and repair", calls[:2])
	}
	if calls[2] == 0 {
		t.Fatal("inherited provider tool count = 0, want configured tools")
	}
	if isolated["tools"] != workflows.AgentToolsNone {
		t.Fatalf("isolated tools audit = %#v, want none", isolated["tools"])
	}
	if isolated["structured_repairs"] != 1 ||
		isolated["structured_valid"] != true {
		t.Fatalf("isolated structured audit = %#v, want one successful repair", isolated)
	}
	if inherited["tools"] != workflows.AgentToolsInherit {
		t.Fatalf("inherited tools audit = %#v, want inherit", inherited["tools"])
	}
}

func TestWorkflowManagedChildrenDisableTools(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled": false,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		},
		Output: contract,
	}
	seen := make([]workflowAgentRunOptions, 0)
	runOnce := func(message string, _ bool, options workflowAgentRunOptions) (string, error) {
		seen = append(seen, options)
		ids := workflowTestScopeIDs(t, message)
		findings := make([]string, 0, len(ids))
		for _, id := range ids {
			findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"title":"finding %s"}`, id, id))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("child run count = %d, want 3", len(seen))
	}
	for i, options := range seen {
		if !options.NoTools {
			t.Fatalf("child %d run options = %#v, want NoTools", i, options)
		}
	}
	children := outputs["managed_children"].([]map[string]any)
	for i, child := range children {
		if child["tools"] != workflows.AgentToolsNone {
			t.Fatalf("child %d tools audit = %#v, want none", i, child["tools"])
		}
	}
}

func TestWorkflowManagedSplitStrategyRequiresStructuredOutputAndSplittableDimensions(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	scope := []any{
		map[string]any{"id": "a"},
		map[string]any{"id": "b"},
		map[string]any{"id": "c"},
	}
	agentWithTasks := &AgentInstance{
		Definition: AgentContextDefinition{Agent: &AgentPromptDefinition{Tasks: []string{
			"Find correctness bugs.",
			"Find security risks.",
			"Find missing tests.",
		}}},
	}
	tests := []struct {
		name  string
		req   workflows.AgentRequest
		agent *AgentInstance
		want  string
	}{
		{
			name: "managed off",
			req: workflows.AgentRequest{
				Managed: "off",
				Scope:   scope,
				Output:  contract,
			},
			agent: agentWithTasks,
		},
		{
			name: "missing structured output",
			req: workflows.AgentRequest{
				Managed: map[string]any{"mode": "auto", "max_items_per_chunk": 2},
				Scope:   scope,
			},
			agent: agentWithTasks,
		},
		{
			name: "auto prefers hybrid",
			req: workflows.AgentRequest{
				Managed: map[string]any{
					"mode":                "auto",
					"max_items_per_chunk": 2,
					"max_tasks_per_chunk": 2,
				},
				Scope:  scope,
				Output: contract,
			},
			agent: agentWithTasks,
			want:  "hybrid_split",
		},
		{
			name: "auto scope only",
			req: workflows.AgentRequest{
				Managed: map[string]any{"mode": "auto", "max_items_per_chunk": 2},
				Scope:   scope,
				Output:  contract,
			},
			agent: &AgentInstance{},
			want:  "scope_split",
		},
		{
			name: "auto task only",
			req: workflows.AgentRequest{
				Managed: map[string]any{"mode": "auto", "max_tasks_per_chunk": 2},
				Output:  contract,
			},
			agent: agentWithTasks,
			want:  "task_split",
		},
		{
			name: "requested scope alias",
			req: workflows.AgentRequest{
				Managed: map[string]any{
					"strategy":            "by_scope",
					"max_items_per_chunk": 2,
				},
				Scope:  scope,
				Output: contract,
			},
			agent: agentWithTasks,
			want:  "scope_split",
		},
		{
			name: "requested task ignored when not splittable",
			req: workflows.AgentRequest{
				Managed: map[string]any{
					"strategy":            "task_split",
					"max_tasks_per_chunk": 10,
				},
				Output: contract,
			},
			agent: agentWithTasks,
		},
		{
			name: "requested none",
			req: workflows.AgentRequest{
				Managed: map[string]any{"split": "none"},
				Scope:   scope,
				Output:  contract,
			},
			agent: agentWithTasks,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workflowManagedSplitStrategy(tt.req, tt.agent)
			if got != tt.want {
				t.Fatalf("workflowManagedSplitStrategy() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWorkflowManagedScopeChunkingUsesInternalTokenTarget(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Scope: []any{
			map[string]any{"id": "a", "content": strings.Repeat("a", 2048)},
			map[string]any{"id": "b", "content": strings.Repeat("b", 2048)},
			map[string]any{"id": "c", "content": strings.Repeat("c", 2048)},
		},
		Output: contract,
	}
	singleItemTokens := workflowScopeChunkPromptTokens(req, workflowScopeItems(req.Scope)[:1])
	options := workflowManagedOptions(map[string]any{
		"mode":                "auto",
		"max_items_per_chunk": 8,
	})
	options.targetChildPromptTokens = singleItemTokens + 1

	chunks := workflowManagedScopeChunks(req, options)
	counts := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		counts = append(counts, len(chunk))
	}
	if fmt.Sprint(counts) != "[1 1 1]" {
		t.Fatalf("chunk counts = %#v, want [1 1 1]", counts)
	}
}

func TestWorkflowManagedAdaptiveScopeChunkingPacksLargerChunks(t *testing.T) {
	req := workflows.AgentRequest{
		Prompt: "Review assigned scope.",
		Scope: []any{
			map[string]any{"id": "a", "content": strings.Repeat("a", 128)},
			map[string]any{"id": "b", "content": strings.Repeat("b", 128)},
			map[string]any{"id": "c", "content": strings.Repeat("c", 128)},
			map[string]any{"id": "d", "content": strings.Repeat("d", 128)},
			map[string]any{"id": "e", "content": strings.Repeat("e", 128)},
		},
		Output: workflowManagedTestOutputContract(),
	}
	scope := workflowScopeItems(req.Scope)
	twoItemTokens := workflowScopeChunkPromptTokens(req, scope[:2])
	options := workflowManagedOptions(map[string]any{
		"mode":                "auto",
		"max_items_per_chunk": 8,
	})
	options.targetChildPromptTokens = twoItemTokens

	chunks := workflowManagedScopeChunks(req, options)
	counts := make([]int, 0, len(chunks))
	for _, chunk := range chunks {
		counts = append(counts, len(chunk))
	}
	if fmt.Sprint(counts) != "[2 2 1]" {
		t.Fatalf("chunk counts = %#v, want [2 2 1]", counts)
	}
}

func TestWorkflowManagedTargetChildPromptTokensIsInternal(t *testing.T) {
	options := workflowManagedOptions(map[string]any{
		"mode":                       "auto",
		"target_child_prompt_tokens": 999,
		"targetChildPromptTokens":    1000,
	})
	if options.targetChildPromptTokens != 0 {
		t.Fatalf("targetChildPromptTokens = %d, want internal unset value", options.targetChildPromptTokens)
	}

	agent := &AgentInstance{ContextWindow: 64000, MaxTokens: 8000}
	resolved := agent.workflowManagedResolveChildPromptTarget(
		workflows.AgentRequest{},
		options,
		"scope_split",
	)
	if resolved.targetChildPromptTokens != 28000 {
		t.Fatalf("resolved targetChildPromptTokens = %d, want 28000", resolved.targetChildPromptTokens)
	}
	if resolved.targetChildPromptSource != "context_window" {
		t.Fatalf("targetChildPromptSource = %q, want context_window", resolved.targetChildPromptSource)
	}
}

func TestWorkflowManagedSplitUsesAssignedTasksForStrategyAndMetadata(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Managed: map[string]any{
			"mode":                "auto",
			"max_tasks_per_chunk": 2,
		},
		Context: workflowManagedTaskMessage([]string{
			"Review correctness.",
			"Review security.",
			"Review test coverage.",
		}),
		Output: contract,
	}
	agent := &AgentInstance{}

	strategy := workflowManagedSplitStrategy(req, agent)
	if strategy != "task_split" {
		t.Fatalf("workflowManagedSplitStrategy() = %q, want task_split", strategy)
	}
	options := workflowManagedOptions(req.Managed)
	plans := workflowManagedChildPlans(req, agent, options, strategy)
	if len(plans) != 2 {
		t.Fatalf("plans len = %d, want 2", len(plans))
	}
	split := workflowManagedSplitMetadata(req, agent, options, strategy, plans)
	if split["task_count"] != 3 {
		t.Fatalf("split task_count = %#v, want 3", split["task_count"])
	}
	counts, ok := split["child_task_counts"].([]int)
	if !ok || len(counts) != 2 || counts[0] != 2 || counts[1] != 1 {
		t.Fatalf("child_task_counts = %#v, want [2 1]", split["child_task_counts"])
	}
}

func TestWorkflowManagedTaskSplitCombinesStructuredOutputs(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned task subset.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_tasks_per_chunk": 2,
			"calibration": map[string]any{
				"enabled":          true,
				"task_sample_size": 3,
			},
		},
		Output: contract,
	}
	agent := &AgentInstance{
		ID:    "reviewer",
		Model: "mock-model",
		Definition: AgentContextDefinition{Agent: &AgentPromptDefinition{Tasks: []string{
			"Find correctness bugs.",
			"Find security risks.",
			"Find missing tests.",
			"Find performance issues.",
			"Find API contract issues.",
		}}},
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		tasks := workflowTestAssignedTasks(t, message)
		findings := make([]string, 0, len(tasks))
		for _, task := range tasks {
			findings = append(findings, fmt.Sprintf(`{"task":%q,"title":"%s"}`, task, task))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(tasks, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		agent,
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"task_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 5 {
		t.Fatalf("findings = %#v, want five task findings", findings)
	}
	managed := outputs["managed"].(map[string]any)
	if managed["strategy"] != "task_split" {
		t.Fatalf("strategy = %#v, want task_split", managed["strategy"])
	}
	split := managed["split"].(map[string]any)
	if split["child_count"] != 3 {
		t.Fatalf("split child_count = %#v, want 3", split["child_count"])
	}
}

func TestWorkflowManagedHybridSplitCombinesStructuredOutputs(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope and task subset.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_items_per_chunk": 2,
			"max_tasks_per_chunk": 2,
			"calibration": map[string]any{
				"enabled":          true,
				"sample_size":      3,
				"task_sample_size": 3,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
			map[string]any{"id": "d"},
		},
		Output: contract,
	}
	agent := &AgentInstance{
		ID:    "reviewer",
		Model: "mock-model",
		Definition: AgentContextDefinition{Agent: &AgentPromptDefinition{Tasks: []string{
			"Find correctness bugs.",
			"Find security risks.",
			"Find missing tests.",
		}}},
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		ids := workflowTestScopeIDs(t, message)
		tasks := workflowTestAssignedTasks(t, message)
		findings := make([]string, 0, len(ids)*len(tasks))
		for _, id := range ids {
			for _, task := range tasks {
				findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"task":%q}`, id, task))
			}
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		agent,
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"hybrid_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 12 {
		t.Fatalf("findings = %#v, want twelve hybrid findings", findings)
	}
	managed := outputs["managed"].(map[string]any)
	if managed["strategy"] != "hybrid_split" {
		t.Fatalf("strategy = %#v, want hybrid_split", managed["strategy"])
	}
	split := managed["split"].(map[string]any)
	if split["child_count"] != 4 {
		t.Fatalf("split child_count = %#v, want 4", split["child_count"])
	}
	if fmt.Sprint(split["child_scope_counts"]) != "[2 2 2 2]" {
		t.Fatalf("child_scope_counts = %#v, want [2 2 2 2]", split["child_scope_counts"])
	}
	if fmt.Sprint(split["child_task_counts"]) != "[2 1 2 1]" {
		t.Fatalf("child_task_counts = %#v, want [2 1 2 1]", split["child_task_counts"])
	}
	calibration := managed["calibration"].(map[string]any)
	if calibration["status"] != "passed" || calibration["sample_scope"] != 3 || calibration["sample_tasks"] != 3 {
		t.Fatalf("calibration = %#v, want passed three-scope three-task sample", calibration)
	}
	children := outputs["managed_children"].([]map[string]any)
	if len(children) != 4 {
		t.Fatalf("managed_children len = %d, want 4", len(children))
	}
	for _, child := range children {
		label := fmt.Sprint(child["label"])
		if !strings.Contains(label, "scope chunk") || !strings.Contains(label, "task chunk") {
			t.Fatalf("child label = %q, want both split axes", label)
		}
		if child["scope_count"] != 2 {
			t.Fatalf("child scope_count = %#v, want 2", child["scope_count"])
		}
		if child["task_count"] != len(child["tasks"].([]string)) {
			t.Fatalf("child task metadata = %#v, want matching task count and labels", child)
		}
	}
}

func TestWorkflowManagedCalibrationCacheDecaysAfterEarlyRuns(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled":            true,
				"sample_size":        2,
				"cache_max_interval": 8,
			},
		},
		Scope: []any{
			map[string]any{"id": "a", "path": "pkg/a.go"},
			map[string]any{"id": "b", "path": "pkg/b.go"},
			map[string]any{"id": "c", "path": "pkg/c.go"},
		},
		Output: contract,
	}
	agent := &AgentInstance{ID: "reviewer", Model: "mock-model"}
	baselineCalls := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") &&
			strings.Contains(message, "grouped baseline") {
			baselineCalls++
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}

	statuses := make([]string, 0, 4)
	decisions := make([]string, 0, 4)
	for range 4 {
		outputs, err := (&workflowAgentRunner{}).runManagedSplit(
			req,
			agent,
			"reviewer",
			"workflow:test",
			"none",
			"none",
			"",
			"scope_split",
			runOnce,
		)
		if err != nil {
			t.Fatalf("runManagedSplit() error = %v", err)
		}
		calibration := outputs["managed"].(map[string]any)["calibration"].(map[string]any)
		statuses = append(statuses, fmt.Sprint(calibration["status"]))
		cache := calibration["cache"].(map[string]any)
		decisions = append(decisions, fmt.Sprint(cache["decision"]))
	}
	if baselineCalls != 3 {
		t.Fatalf("baseline calls = %d, want 3 with marginal split fit forcing run 3 calibration", baselineCalls)
	}
	if fmt.Sprint(statuses) != "[passed passed passed trusted_cache]" {
		t.Fatalf("calibration statuses = %#v, want passed passed passed trusted_cache", statuses)
	}
	if fmt.Sprint(decisions) != "[miss due due hit]" {
		t.Fatalf("cache decisions = %#v, want miss due due hit", decisions)
	}
}

func TestWorkflowManagedCalibrationCacheSoftReusesModelAndLanguageChanges(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	newReq := func(ext string) workflows.AgentRequest {
		return workflows.AgentRequest{
			Prompt: "Analyze assigned scope.",
			Managed: map[string]any{
				"mode":                  "auto",
				"max_items_per_chunk":   1,
				"max_parallel_children": 1,
				"calibration": map[string]any{
					"enabled":     true,
					"sample_size": 2,
				},
			},
			Scope: []any{
				map[string]any{"id": "a", "path": "pkg/a." + ext},
				map[string]any{"id": "b", "path": "pkg/b." + ext},
				map[string]any{"id": "c", "path": "pkg/c." + ext},
			},
			Output: contract,
		}
	}
	agent := &AgentInstance{ID: "reviewer", Model: "model-a"}
	baselineCalls := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") &&
			strings.Contains(message, "grouped baseline") {
			baselineCalls++
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}
	run := func(req workflows.AgentRequest) map[string]any {
		t.Helper()
		outputs, err := (&workflowAgentRunner{}).runManagedSplit(
			req,
			agent,
			"reviewer",
			"workflow:test",
			"none",
			"none",
			"",
			"scope_split",
			runOnce,
		)
		if err != nil {
			t.Fatalf("runManagedSplit() error = %v", err)
		}
		return outputs["managed"].(map[string]any)["calibration"].(map[string]any)
	}

	run(newReq("go"))
	run(newReq("go"))
	agent.Model = "model-b"
	modelChanged := run(newReq("go"))
	if modelChanged["status"] != "trusted_cache" {
		t.Fatalf("model-changed calibration = %#v, want borrowed trusted_cache", modelChanged)
	}
	modelCache := modelChanged["cache"].(map[string]any)
	if modelCache["decision"] != "similar_hit" || modelCache["provisional"] != true {
		t.Fatalf("model-changed cache = %#v, want provisional similar_hit", modelCache)
	}
	modelVerified := run(newReq("go"))
	if modelVerified["status"] != "passed" {
		t.Fatalf("model verification calibration = %#v, want passed", modelVerified)
	}
	modelVerifiedCache := modelVerified["cache"].(map[string]any)
	if modelVerifiedCache["decision"] != "borrowed_due" || modelVerifiedCache["provisional"] != false {
		t.Fatalf("model verification cache = %#v, want promoted borrowed_due", modelVerifiedCache)
	}
	agent.Model = "model-a"
	languageChanged := run(newReq("py"))
	if languageChanged["status"] != "trusted_cache" {
		t.Fatalf("language-changed calibration = %#v, want borrowed trusted_cache", languageChanged)
	}
	languageCache := languageChanged["cache"].(map[string]any)
	if languageCache["decision"] != "similar_hit" || languageCache["provisional"] != true {
		t.Fatalf("language-changed cache = %#v, want provisional similar_hit", languageCache)
	}
	if fmt.Sprint(languageCache["languages"]) != "[python]" {
		t.Fatalf("language cache metadata = %#v, want python", languageCache["languages"])
	}
	languageVerified := run(newReq("py"))
	if languageVerified["status"] != "passed" {
		t.Fatalf("language verification calibration = %#v, want passed", languageVerified)
	}
	languageVerifiedCache := languageVerified["cache"].(map[string]any)
	if languageVerifiedCache["decision"] != "borrowed_due" || languageVerifiedCache["provisional"] != false {
		t.Fatalf("language verification cache = %#v, want promoted borrowed_due", languageVerifiedCache)
	}
	if baselineCalls != 4 {
		t.Fatalf("baseline calls = %d, want 4", baselineCalls)
	}
}

func TestWorkflowManagedCalibrationCacheBorrowFailureResetsFresh(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	newReq := func(prompt string) workflows.AgentRequest {
		return workflows.AgentRequest{
			Prompt: prompt,
			Managed: map[string]any{
				"mode":                  "auto",
				"max_items_per_chunk":   1,
				"max_parallel_children": 1,
				"calibration": map[string]any{
					"enabled":              true,
					"sample_size":          2,
					"similarity_threshold": 0.70,
				},
			},
			Scope: []any{
				map[string]any{"id": "a", "path": "pkg/a.go"},
				map[string]any{"id": "b", "path": "pkg/b.go"},
				map[string]any{"id": "c", "path": "pkg/c.go"},
			},
			Output: contract,
		}
	}
	agent := &AgentInstance{ID: "reviewer", Model: "mock-model"}
	baselineCalls := 0
	forceMismatch := false
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") &&
			strings.Contains(message, "grouped baseline") {
			baselineCalls++
			if forceMismatch {
				return `{"summary":"baseline","findings":[{"scope_id":"baseline-only"}]}`, nil
			}
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}
	run := func(req workflows.AgentRequest) map[string]any {
		t.Helper()
		outputs, err := (&workflowAgentRunner{}).runManagedSplit(
			req,
			agent,
			"reviewer",
			"workflow:test",
			"none",
			"none",
			"",
			"scope_split",
			runOnce,
		)
		if err != nil {
			t.Fatalf("runManagedSplit() error = %v", err)
		}
		return outputs["managed"].(map[string]any)["calibration"].(map[string]any)
	}

	run(newReq("Analyze assigned scope."))
	run(newReq("Analyze assigned scope."))
	borrowed := run(newReq("Analyze assigned scope carefully."))
	borrowedCache := borrowed["cache"].(map[string]any)
	if borrowed["status"] != "trusted_cache" || borrowedCache["decision"] != "similar_hit" {
		t.Fatalf("borrowed calibration = %#v, want similar trusted cache", borrowed)
	}

	forceMismatch = true
	failed := run(newReq("Analyze assigned scope carefully."))
	failedCache := failed["cache"].(map[string]any)
	if failed["status"] != "failed" || failedCache["decision"] != "borrowed_due" {
		t.Fatalf("failed borrowed verification = %#v, want failed borrowed_due", failed)
	}
	if failedCache["trusted"] != false || failedCache["success_streak"] != 0 {
		t.Fatalf("failed borrowed cache = %#v, want reset confidence", failedCache)
	}

	forceMismatch = false
	fresh := run(newReq("Analyze assigned scope carefully."))
	freshCache := fresh["cache"].(map[string]any)
	if fresh["status"] != "passed" || freshCache["decision"] != "previous_not_trusted" {
		t.Fatalf("fresh calibration = %#v, want fresh previous_not_trusted pass", fresh)
	}
	if freshCache["success_streak"] != 1 {
		t.Fatalf("fresh cache = %#v, want success streak 1", freshCache)
	}
	if baselineCalls != 4 {
		t.Fatalf("baseline calls = %d, want 4", baselineCalls)
	}
}

func TestWorkflowManagedCalibrationCacheIntervalDependsOnSplitFit(t *testing.T) {
	if got := workflowManagedCalibrationCacheInterval(5, 16, 0.95); got != 16 {
		t.Fatalf("high-fit interval = %d, want 16", got)
	}
	if got := workflowManagedCalibrationCacheInterval(5, 16, 0.60); got != 8 {
		t.Fatalf("medium-fit interval = %d, want 8", got)
	}
	if got := workflowManagedCalibrationCacheInterval(5, 16, 0.30); got != 1 {
		t.Fatalf("low-fit interval = %d, want 1", got)
	}
}

func TestWorkflowManagedCalibrationCacheLearnsChildPromptTarget(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_items_per_chunk": 1,
		},
		Scope: []any{
			map[string]any{"id": "a", "path": "pkg/a.go", "content": strings.Repeat("a", 128)},
			map[string]any{"id": "b", "path": "pkg/b.go", "content": strings.Repeat("b", 128)},
		},
		Output: contract,
	}
	agent := &AgentInstance{ID: "reviewer", Model: "mock-model", ContextWindow: 64000, MaxTokens: 8000}
	options := agent.workflowManagedResolveChildPromptTarget(
		req,
		workflowManagedOptions(req.Managed),
		"scope_split",
	)
	plans := workflowManagedChildPlans(req, agent, options, "scope_split")
	key, identity := workflowManagedCalibrationCacheKey(req, agent, options, "scope_split", plans)

	meta := agent.recordWorkflowManagedCalibrationCache(
		key,
		identity,
		options,
		map[string]any{"status": "passed", "match": true},
	)
	learned := intFromAny(meta["learned_target_child_prompt_tokens"])
	if learned <= 0 {
		t.Fatalf("learned target = %#v, want positive", meta["learned_target_child_prompt_tokens"])
	}

	next := agent.workflowManagedResolveChildPromptTarget(
		req,
		workflowManagedOptions(req.Managed),
		"scope_split",
	)
	if next.targetChildPromptSource != "learned_cache" {
		t.Fatalf("target source = %q, want learned_cache", next.targetChildPromptSource)
	}
	if next.targetChildPromptTokens != learned {
		t.Fatalf("targetChildPromptTokens = %d, want learned %d", next.targetChildPromptTokens, learned)
	}
}

func TestWorkflowManagedOptimizationSelectsCheaperModelAndEffort(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                    "auto",
			"max_items_per_chunk":     1,
			"estimated_output_tokens": 500,
			"calibration": map[string]any{
				"enabled": false,
			},
			"optimization": map[string]any{
				"model": map[string]any{
					"enabled": true,
					"candidates": []any{
						map[string]any{
							"name":                "expensive-model",
							"input_price_per_1m":  5.0,
							"output_price_per_1m": 15.0,
						},
						map[string]any{
							"name":                "cheap-model",
							"input_price_per_1m":  0.1,
							"output_price_per_1m": 0.4,
						},
					},
				},
				"effort": map[string]any{"enabled": true},
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		},
		Output: contract,
	}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				AccountRef: "expensive-model",
				ModelName:  "expensive-model",
			},
		},
		ModelAliases: []config.ModelAliasConfig{
			{Name: "expensive-model", Model: "expensive-model"},
			{Name: "cheap-model", Model: "cheap-model"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName:          "expensive-model",
				Provider:           "openai",
				Model:              "openai/expensive-model",
				APIBase:            "http://example.invalid/v1",
				APIKeys:            config.SimpleSecureStrings("test-key"),
				Enabled:            true,
				InputPricePerMTok:  5.0,
				OutputPricePerMTok: 15.0,
			},
			{
				ModelName:          "cheap-model",
				Provider:           "openai",
				Model:              "openai/cheap-model",
				InputPricePerMTok:  0.1,
				OutputPricePerMTok: 0.4,
			},
		},
	}
	agent := &AgentInstance{
		ID:                 "reviewer",
		AccountRef:         "expensive-model",
		Model:              "expensive-model",
		Workspace:          t.TempDir(),
		CandidateProviders: map[string]providers.LLMProvider{},
	}
	cheapProtocol, cheapModel := providers.ExtractProtocol(cfg.ModelList[1])
	agent.CandidateProviders[providers.ModelKey(cheapProtocol, cheapModel)] = workflowManagedTestProvider{
		model: "openai/cheap-model",
	}
	var seenMu sync.Mutex
	var seen []workflowAgentRunOptions
	runOnce := func(message string, _ bool, options workflowAgentRunOptions) (string, error) {
		seenMu.Lock()
		seen = append(seen, options)
		seenMu.Unlock()
		ids := workflowTestScopeIDs(t, message)
		findings := make([]string, 0, len(ids))
		for _, id := range ids {
			findings = append(findings, fmt.Sprintf(`{"scope_id":%q}`, id))
		}
		return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ",")), nil
	}

	outputs, err := (&workflowAgentRunner{loop: &AgentLoop{cfg: cfg}}).runManagedSplit(
		req,
		agent,
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("run count = %d, want 3", len(seen))
	}
	for _, options := range seen {
		if options.ModelName != "cheap-model" {
			t.Fatalf("ModelName = %q, want cheap-model", options.ModelName)
		}
		if options.ReasoningEffort == "" {
			t.Fatalf("ReasoningEffort is empty, want optimized effort")
		}
	}
	managed := outputs["managed"].(map[string]any)
	optimization := managed["optimization"].(map[string]any)
	model := optimization["model"].(map[string]any)
	if model["changed"] != true {
		t.Fatalf("model optimization = %#v, want changed", model)
	}
	cost := optimization["cost"].(map[string]any)
	savings, _ := cost["estimated_savings_usd"].(float64)
	if savings <= 0 {
		t.Fatalf("estimated savings = %#v, want positive", cost["estimated_savings_usd"])
	}
}

func TestWorkflowManagedOptimizationSkipsCheaperModelWithoutProvider(t *testing.T) {
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Provider: "openai"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName:          "default-model",
				Provider:           "openai",
				Model:              "openai/default-model",
				InputPricePerMTok:  5.0,
				OutputPricePerMTok: 15.0,
			},
			{
				ModelName:          "cheap-model",
				Provider:           "openai",
				Model:              "openai/cheap-model",
				InputPricePerMTok:  0.1,
				OutputPricePerMTok: 0.4,
			},
		},
	}
	agent := &AgentInstance{
		ID:                 "reviewer",
		Model:              "default-model",
		Workspace:          t.TempDir(),
		CandidateProviders: map[string]providers.LLMProvider{},
	}
	options := workflowManagedOptions(map[string]any{
		"optimization": map[string]any{
			"model": map[string]any{
				"enabled":    true,
				"candidates": []any{"cheap-model"},
			},
		},
	})

	choice := workflowManagedRunChoice(
		workflows.AgentRequest{Prompt: "Analyze."},
		agent,
		cfg,
		options,
		"scope_split",
		workflowManagedChildPlan{index: 1, scope: []any{map[string]any{"id": "a"}}, tasks: []string{"review"}},
	)
	if choice.modelName != "default-model" {
		t.Fatalf("modelName = %q, want default-model", choice.modelName)
	}
	if changed, _ := choice.modelMeta["changed"].(bool); changed {
		t.Fatalf("model metadata = %#v, want unchanged without candidate provider", choice.modelMeta)
	}
}

func TestWorkflowManagedCalibrationMismatchFallsBackToSingleRun(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled":     true,
				"sample_size": 2,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
		},
		Output: contract,
		Tools:  workflows.AgentToolsNone,
	}
	calls := 0
	var runOptions []workflowAgentRunOptions
	runOnce := func(message string, _ bool, options workflowAgentRunOptions) (string, error) {
		calls++
		runOptions = append(runOptions, options)
		if strings.Contains(message, "Agent execution optimization split calibration.") {
			if strings.Contains(message, "grouped baseline") {
				return `{"summary":"baseline","findings":[{"scope_id":"baseline"}]}`, nil
			}
			ids := workflowTestScopeIDs(t, message)
			return workflowManagedTestFindingsJSON(ids), nil
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if calls != 4 {
		t.Fatalf("run count = %d, want 4", calls)
	}
	for i, options := range runOptions {
		if !options.NoTools {
			t.Fatalf("managed call %d options = %#v, want NoTools", i, options)
		}
	}
	if _, exists := outputs["managed_children"]; exists {
		t.Fatalf("managed_children present after calibration fallback: %#v", outputs["managed_children"])
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 3 {
		t.Fatalf("fallback findings = %#v, want full-scope result", findings)
	}
	managed := outputs["managed"].(map[string]any)
	calibration := managed["calibration"].(map[string]any)
	if calibration["status"] != "failed" || calibration["phase"] != "compare" || calibration["match"] != false {
		t.Fatalf("calibration = %#v, want failed compare mismatch", calibration)
	}
	if outputs["tools"] != workflows.AgentToolsNone {
		t.Fatalf("fallback tools audit = %#v, want none", outputs["tools"])
	}
}

func TestWorkflowManagedCalibrationSampleExpandsAndCatchesMismatch(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   3,
			"max_parallel_children": 1,
			"calibration": map[string]any{
				"enabled":     true,
				"sample_size": 1,
			},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
			map[string]any{"id": "c"},
			map[string]any{"id": "d"},
		},
		Output: contract,
	}
	baselineCalls := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Agent execution optimization split calibration.") {
			if strings.Contains(message, "grouped baseline") {
				baselineCalls++
				return `{"summary":"baseline","findings":[{"scope_id":"baseline-only"}]}`, nil
			}
			ids := workflowTestScopeIDs(t, message)
			return workflowManagedTestFindingsJSON(ids), nil
		}
		ids := workflowTestScopeIDs(t, message)
		return workflowManagedTestFindingsJSON(ids), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if baselineCalls != 1 {
		t.Fatalf("baseline calls = %d, want 1", baselineCalls)
	}
	if _, exists := outputs["managed_children"]; exists {
		t.Fatalf("managed_children present after calibration fallback: %#v", outputs["managed_children"])
	}
	calibration := outputs["managed"].(map[string]any)["calibration"].(map[string]any)
	if calibration["status"] != "failed" || calibration["phase"] != "compare" {
		t.Fatalf("calibration = %#v, want failed compare mismatch", calibration)
	}
	if calibration["sample_scope"] != 4 {
		t.Fatalf("sample_scope = %#v, want expanded sample of 4", calibration["sample_scope"])
	}
}

func TestWorkflowManagedChildInvalidOutputReturnsDiagnostics(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration":           map[string]any{"enabled": false},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
		Output: contract,
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Your previous response did not satisfy") {
			return `{"summary":"still missing findings"}`, nil
		}
		ids := workflowTestScopeIDs(t, message)
		if len(ids) == 1 && ids[0] == "b" {
			return `{"summary":"missing findings"}`, nil
		}
		return workflowManagedTestFindingsJSON(ids), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err == nil {
		t.Fatal("runManagedSplit() error = nil, want child validation error")
	}
	children := outputs["managed_children"].([]map[string]any)
	if len(children) != 2 {
		t.Fatalf("managed_children len = %d, want 2", len(children))
	}
	invalid := 0
	for _, child := range children {
		if child["valid"] == false {
			invalid++
			if child["error"] == "" || child["run_error"] == "" {
				t.Fatalf("invalid child diagnostics = %#v, want error and run_error", child)
			}
		}
	}
	if invalid != 1 {
		t.Fatalf("invalid child count = %d, want 1; children=%#v", invalid, children)
	}
}

func TestWorkflowManagedChildRepairsInvalidJSONAndAggregatesRepairs(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                  "auto",
			"max_items_per_chunk":   1,
			"max_parallel_children": 1,
			"calibration":           map[string]any{"enabled": false},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
		Output: contract,
	}
	repairs := 0
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if strings.Contains(message, "Your previous response did not satisfy") {
			repairs++
			id := fmt.Sprintf("repaired-%d", repairs)
			return workflowManagedTestFindingsJSON([]string{id}), nil
		}
		return "not json", nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if outputs["structured_repairs"] != 2 {
		t.Fatalf("structured_repairs = %#v, want 2", outputs["structured_repairs"])
	}
	structured := outputs["structured"].(map[string]any)
	findings := structured["findings"].([]any)
	if len(findings) != 2 {
		t.Fatalf("findings = %#v, want two repaired findings", findings)
	}
}

func TestWorkflowManagedCombinedOutputIsValidated(t *testing.T) {
	contract := &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"scope_id": map[string]any{"type": "string"},
				},
			},
		},
	}
	req := workflows.AgentRequest{
		Prompt: "Analyze assigned scope.",
		Managed: map[string]any{
			"mode":                "auto",
			"max_items_per_chunk": 1,
			"calibration":         map[string]any{"enabled": false},
		},
		Scope: []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
		Output: contract,
	}
	runOnce := func(message string, _ bool, _ workflowAgentRunOptions) (string, error) {
		ids := workflowTestScopeIDs(t, message)
		return fmt.Sprintf(`[{"scope_id":%q}]`, ids[0]), nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{ID: "reviewer", Model: "mock-model"},
		"reviewer",
		"workflow:test",
		"none",
		"none",
		"",
		"scope_split",
		runOnce,
	)
	if err == nil {
		t.Fatal("runManagedSplit() error = nil, want combined schema validation error")
	}
	if outputs["structured_valid"] != false {
		t.Fatalf("structured_valid = %#v, want false", outputs["structured_valid"])
	}
	if outputs["structured_error"] == "" {
		t.Fatalf("structured_error is empty in outputs %#v", outputs)
	}
}

func TestWorkflowManagedProviderInitializationRegistersCandidate(t *testing.T) {
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{
			{Name: "default-model", Model: "default-model"},
			{Name: "cheap-model", Model: "cheap-model"},
			{Name: "subscription-model", Model: "subscription-model"},
			{Name: "metered-model", Model: "metered-model"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "account",
				Provider:  "openai",
				APIBase:   "http://example.invalid/v1",
				APIKeys:   config.SimpleSecureStrings("test-key"),
				Enabled:   true,
			},
			{
				ModelName: "default-model",
				Provider:  "openai",
				Model:     "openai/default-model",
			},
			{
				ModelName: "cheap-model",
				Provider:  "openai",
				Model:     "openai/cheap-model",
			},
		},
	}
	agent := &AgentInstance{
		ID:                 "reviewer",
		AccountRef:         "account",
		Model:              "default-model",
		Workspace:          t.TempDir(),
		CandidateProviders: map[string]providers.LLMProvider{},
	}
	runner := &workflowAgentRunner{loop: &AgentLoop{
		cfg: cfg,
	}}
	raw := map[string]any{
		"optimization": map[string]any{
			"model": map[string]any{
				"enabled":    true,
				"candidates": []any{"cheap-model"},
			},
		},
	}

	if err := runner.ensureWorkflowManagedProviders(agent, raw); err != nil {
		t.Fatalf("ensureWorkflowManagedProviders() error = %v", err)
	}
	protocol, modelID := "openai", "cheap-model"
	if agent.CandidateProviders[providers.ModelKey(protocol, modelID)] == nil {
		t.Fatalf("candidate provider for %s/%s not registered: %#v", protocol, modelID, agent.CandidateProviders)
	}
	if err := runner.ensureWorkflowManagedProviders(agent, raw); err != nil {
		t.Fatalf("second ensureWorkflowManagedProviders() error = %v", err)
	}
}

func TestWorkflowManagedProviderInitializationReportsCandidateFailures(t *testing.T) {
	runner := &workflowAgentRunner{loop: &AgentLoop{cfg: &config.Config{}}}
	err := runner.ensureWorkflowManagedProviders(&AgentInstance{Model: "default-model"}, map[string]any{
		"optimization": map[string]any{
			"model": map[string]any{
				"enabled":    true,
				"candidates": []any{"missing-model"},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "missing-model") {
		t.Fatalf("ensureWorkflowManagedProviders() error = %v, want missing-model failure", err)
	}
}

func TestWorkflowManagedMissingAliasFailsBeforeLLMCall(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	provider := &aliasRuntimeCountingProvider{}
	loop := newTestAgentLoopWithStrictModels(cfg, bus.NewMessageBus(), provider)
	t.Cleanup(loop.Close)

	_, err := (&workflowAgentRunner{loop: loop}).RunAgent(
		context.Background(),
		workflows.AgentRequest{
			AgentID: "main",
			Prompt:  "Review the assigned scope.",
			History: "none",
			Managed: map[string]any{
				"mode":                "auto",
				"max_items_per_chunk": 1,
				"optimization": map[string]any{
					"model": map[string]any{
						"enabled":    true,
						"candidates": []any{"raw-or-missing-model"},
					},
				},
			},
			Scope: []any{
				map[string]any{"id": "a"},
				map[string]any{"id": "b"},
			},
			Output: &workflows.AgentOutputContract{
				Format: "json",
				Schema: map[string]any{"type": "object"},
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "raw-or-missing-model") {
		t.Fatalf("RunAgent() error = %v, want strict missing-alias failure", err)
	}
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("provider calls = %d, want failure before LLM I/O", calls)
	}
}

func TestWorkflowManagedModelCandidateMapUsesModelFallback(t *testing.T) {
	candidates := parseWorkflowManagedModelCandidates([]any{
		map[string]any{
			"model":              "cheap-model",
			"input_price_per_1m": "0.25",
			"subscription":       true,
		},
	})
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one candidate", candidates)
	}
	candidate := candidates[0]
	if candidate.name != "cheap-model" {
		t.Fatalf("candidate name = %q, want cheap-model", candidate.name)
	}
	if candidate.equivalentModelName != "" {
		t.Fatalf("equivalent model = %q, want empty", candidate.equivalentModelName)
	}
	if !candidate.priceKnown || candidate.inputPricePerMTok != 0.25 {
		t.Fatalf("candidate pricing = %#v, want known input price", candidate)
	}
	if !candidate.subscription {
		t.Fatal("candidate subscription = false, want true")
	}
}

func TestWorkflowManagedModelProfileUsesSubscriptionEquivalentPricing(t *testing.T) {
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{
			{Name: "subscription-model", Model: "subscription-model"},
			{Name: "metered-model", Model: "metered-model"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "account",
				Provider:  "openai",
				APIBase:   "http://example.invalid/v1",
				APIKeys:   config.SimpleSecureStrings("test-key"),
				Enabled:   true,
			},
			{
				ModelName:                   "subscription-model",
				Provider:                    "openai",
				Model:                       "subscription-model",
				Subscription:                true,
				SubscriptionEquivalentModel: "metered-model",
			},
			{
				ModelName:          "metered-model",
				Provider:           "openai",
				Model:              "metered-model",
				InputPricePerMTok:  2.5,
				OutputPricePerMTok: 10,
			},
		},
	}

	profile := workflowModelCandidateProfile(cfg, "account", "subscription-model")
	if !profile.subscription || !profile.priceKnown {
		t.Fatalf("profile = %#v, want subscription with known equivalent price", profile)
	}
	if profile.inputPricePerMTok != 2.5 || profile.outputPricePerMTok != 10 {
		t.Fatalf(
			"profile prices = (%v, %v), want equivalent model prices",
			profile.inputPricePerMTok,
			profile.outputPricePerMTok,
		)
	}
	if profile.source != "subscription_equivalent_model_config" {
		t.Fatalf("profile source = %q, want subscription_equivalent_model_config", profile.source)
	}
}

func TestWorkflowManagedModelProfileStopsSubscriptionEquivalentCycle(t *testing.T) {
	cfg := &config.Config{
		ModelAliases: []config.ModelAliasConfig{
			{Name: "subscription-a", Model: "subscription-a"},
			{Name: "subscription-b", Model: "subscription-b"},
		},
		ModelList: []*config.ModelConfig{
			{
				ModelName: "account",
				Provider:  "openai",
				Enabled:   true,
			},
			{
				ModelName:                   "subscription-a-metadata",
				Provider:                    "openai",
				Model:                       "subscription-a",
				Subscription:                true,
				SubscriptionEquivalentModel: "subscription-b",
			},
			{
				ModelName:                   "subscription-b-metadata",
				Provider:                    "openai",
				Model:                       "subscription-b",
				Subscription:                true,
				SubscriptionEquivalentModel: "subscription-a",
			},
		},
	}

	profile := workflowModelCandidateProfile(cfg, "account", "subscription-a")
	if !profile.subscription {
		t.Fatalf("profile = %#v, want subscription metadata", profile)
	}
	if profile.priceKnown {
		t.Fatalf("profile = %#v, want cycle to stop without inherited pricing", profile)
	}
}

func TestWorkflowManagedModelProfileBoundsSubscriptionEquivalentDepth(t *testing.T) {
	const terminalIndex = workflowModelCandidateProfileMaxEquivalentDepth
	cfg := &config.Config{
		ModelList: []*config.ModelConfig{{
			ModelName: "account",
			Provider:  "openai",
			Enabled:   true,
		}},
	}
	for i := 0; i <= terminalIndex; i++ {
		name := fmt.Sprintf("subscription-%d", i)
		cfg.ModelAliases = append(cfg.ModelAliases, config.ModelAliasConfig{
			Name:  name,
			Model: name,
		})
		metadata := &config.ModelConfig{
			ModelName:    name + "-metadata",
			Provider:     "openai",
			Model:        name,
			Subscription: true,
		}
		if i < terminalIndex {
			metadata.SubscriptionEquivalentModel = fmt.Sprintf("subscription-%d", i+1)
		} else {
			metadata.InputPricePerMTok = 1
			metadata.OutputPricePerMTok = 2
		}
		cfg.ModelList = append(cfg.ModelList, metadata)
	}

	profile := workflowModelCandidateProfile(cfg, "account", "subscription-0")
	if profile.priceKnown {
		t.Fatalf(
			"profile inherited pricing beyond depth limit: %#v",
			profile,
		)
	}
}

func TestWorkflowManagedModeNormalization(t *testing.T) {
	tests := []struct {
		raw  any
		want string
	}{
		{raw: nil, want: "off"},
		{raw: false, want: "off"},
		{raw: true, want: "auto"},
		{raw: "none", want: "off"},
		{raw: "TRUE", want: "auto"},
		{raw: "custom", want: "custom"},
		{raw: map[string]any{"enabled": false}, want: "off"},
		{raw: map[string]any{"enabled": false, "mode": "task_split"}, want: "off"},
		{raw: map[string]any{"mode": "task_split"}, want: "task_split"},
		{raw: map[string]any{"max_items_per_chunk": 1}, want: "auto"},
	}
	for _, tt := range tests {
		if got := workflowManagedMode(tt.raw); got != tt.want {
			t.Fatalf("workflowManagedMode(%#v) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestWorkflowManagedScopeItemsAndPlanPreserveObjectWrapper(t *testing.T) {
	scope := map[string]any{
		"kind":  "files",
		"limit": 2,
		"items": []any{
			map[string]any{"id": "a"},
			map[string]any{"id": "b"},
		},
	}
	items := workflowScopeItems(scope)
	if len(items) != 2 {
		t.Fatalf("workflowScopeItems() = %#v, want two items", items)
	}

	planned := workflowScopeForPlan(scope, []any{map[string]any{"id": "a"}})
	mapped, ok := planned.(map[string]any)
	if !ok {
		t.Fatalf("planned scope = %#v, want map wrapper", planned)
	}
	if mapped["kind"] != "files" || mapped["limit"] != 2 {
		t.Fatalf("planned scope metadata = %#v, want original wrapper metadata", mapped)
	}
	plannedItems, ok := mapped["items"].([]any)
	if !ok || len(plannedItems) != 1 {
		t.Fatalf("planned items = %#v, want one scoped item", mapped["items"])
	}
}

func workflowManagedTestOutputContract() *workflows.AgentOutputContract {
	return &workflows.AgentOutputContract{
		Format:         "json",
		RepairAttempts: 1,
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"summary", "findings"},
			"properties": map[string]any{
				"summary":  map[string]any{"type": "string"},
				"findings": map[string]any{"type": "array"},
			},
		},
	}
}

type workflowManagedTestProvider struct {
	model string
}

type workflowToolsCaptureProvider struct {
	mu         sync.Mutex
	toolCounts []int
	responses  []string
}

type workflowReadOnlyProviderCall struct {
	messages       []providers.Message
	toolCount      int
	promptCacheKey string
}

type workflowReadOnlyCaptureProvider struct {
	mu                sync.Mutex
	responses         []string
	calls             []workflowReadOnlyProviderCall
	toolCall          bool
	started           chan struct{}
	release           chan struct{}
	startOnce         sync.Once
	afterCall         func(int)
	mutateNestedInput bool
}

func (p *workflowReadOnlyCaptureProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	tools []providers.ToolDefinition,
	_ string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	callIndex := len(p.calls)
	cacheKey, _ := opts["prompt_cache_key"].(string)
	p.calls = append(p.calls, workflowReadOnlyProviderCall{
		messages:       session.CloneMessages(messages),
		toolCount:      len(tools),
		promptCacheKey: cacheKey,
	})
	response := "decision"
	if callIndex < len(p.responses) {
		response = p.responses[callIndex]
	}
	toolCall := p.toolCall
	started := p.started
	release := p.release
	afterCall := p.afterCall
	mutateNestedInput := p.mutateNestedInput
	p.mu.Unlock()
	if mutateNestedInput {
		for messageIndex := range messages {
			for blockIndex := range messages[messageIndex].SystemParts {
				block := &messages[messageIndex].SystemParts[blockIndex]
				if block.CacheControl != nil {
					block.CacheControl.Type = "provider-mutated"
				}
			}
			for callIndex := range messages[messageIndex].ToolCalls {
				nested, _ := messages[messageIndex].ToolCalls[callIndex].Arguments["nested"].(map[string]any)
				if nested != nil {
					nested["marker"] = "provider-mutated"
				}
			}
		}
	}
	if afterCall != nil {
		afterCall(callIndex)
	}

	if started != nil {
		p.startOnce.Do(func() { close(started) })
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	result := &providers.LLMResponse{Content: response}
	if toolCall {
		result.ToolCalls = []providers.ToolCall{{
			ID:   "forbidden",
			Type: "function",
			Function: &providers.FunctionCall{
				Name:      "sentinel",
				Arguments: `{}`,
			},
		}}
	}
	return result, nil
}

func (p *workflowReadOnlyCaptureProvider) snapshotCalls() []workflowReadOnlyProviderCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]workflowReadOnlyProviderCall, len(p.calls))
	for i, call := range p.calls {
		result[i] = call
		result[i].messages = session.CloneMessages(call.messages)
	}
	return result
}

func (p *workflowReadOnlyCaptureProvider) setResponses(responses []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses = append([]string(nil), responses...)
	p.calls = nil
}

func newWorkflowReadOnlyTestLoop(
	t *testing.T,
	provider *workflowReadOnlyCaptureProvider,
) (*AgentLoop, *AgentInstance, string, string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Agents.List = []config.AgentConfig{{ID: "main", Default: true}}
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, provider)
	t.Cleanup(func() {
		loop.Close()
		messageBus.Close()
	})
	loop.providerFactory = func(modelConfig *config.ModelConfig) (providers.LLMProvider, string, error) {
		model := "workflow-read-only-test"
		if modelConfig != nil && strings.TrimSpace(modelConfig.Model) != "" {
			model = modelConfig.Model
		}
		return provider, model, nil
	}
	agent := loop.GetRegistry().GetDefaultAgent()
	if agent == nil {
		t.Fatal("default agent is nil")
	}
	canonicalKey := session.BuildOpaqueSessionKey("agent:main:web:direct:review")
	alias := "agent:main:web:direct:review"
	metadata, ok := agent.Sessions.(session.MetadataAwareSessionStore)
	if !ok {
		t.Fatalf("session store %T is not metadata aware", agent.Sessions)
	}
	metadata.EnsureSessionMetadata(canonicalKey, &session.SessionScope{
		Version: session.ScopeVersionV1,
		AgentID: "main",
		Channel: "web",
		Dimensions: []string{
			"chat",
		},
		Values: map[string]string{"chat": "review"},
	}, []string{alias})
	agent.Sessions.AddMessage(canonicalKey, "user", "existing problem context")
	agent.Sessions.AddMessage(canonicalKey, "assistant", "existing analysis")
	agent.Sessions.SetSummary(canonicalKey, "existing decision summary")
	return loop, agent, canonicalKey, alias
}

func workflowMessagesContain(messages []providers.Message, substring string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, substring) {
			return true
		}
		for _, block := range message.SystemParts {
			if strings.Contains(block.Text, substring) {
				return true
			}
		}
	}
	return false
}

func workflowFrozenCacheControl(messages []providers.Message) string {
	for _, message := range messages {
		for _, block := range message.SystemParts {
			if block.Text == "frozen nested block" && block.CacheControl != nil {
				return block.CacheControl.Type
			}
		}
	}
	return ""
}

func (p *workflowToolsCaptureProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	tools []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	p.mu.Lock()
	call := len(p.toolCounts)
	p.toolCounts = append(p.toolCounts, len(tools))
	response := "classified"
	if call < len(p.responses) {
		response = p.responses[call]
	}
	p.mu.Unlock()
	return &providers.LLMResponse{Content: response}, nil
}

func (p *workflowToolsCaptureProvider) ToolCounts() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.toolCounts...)
}

func (p workflowManagedTestProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "{}"}, nil
}

func workflowTestScopeIDs(t *testing.T, message string) []string {
	t.Helper()
	_, parsed, err := workflows.ExtractJSONValue(message)
	if err != nil {
		t.Fatalf("extract assigned scope from message: %v\n%s", err, message)
	}
	items, ok := parsed.([]any)
	if !ok {
		t.Fatalf("assigned scope = %#v, want array", parsed)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		mapped, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("scope item = %#v, want object", item)
		}
		ids = append(ids, fmt.Sprint(mapped["id"]))
	}
	return ids
}

func workflowManagedTestFindingsJSON(ids []string) string {
	findings := make([]string, 0, len(ids))
	for _, id := range ids {
		findings = append(findings, fmt.Sprintf(`{"scope_id":%q,"title":"finding %s"}`, id, id))
	}
	return fmt.Sprintf(`{"summary":%q,"findings":[%s]}`, strings.Join(ids, ","), strings.Join(findings, ","))
}

func workflowTestAssignedTasks(t *testing.T, message string) []string {
	t.Helper()
	tasks := workflowAssignedTasks(workflows.AgentRequest{Context: message})
	if len(tasks) == 0 {
		t.Fatalf("assigned tasks not found in message:\n%s", message)
	}
	return tasks
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

type workflowMCPCall struct {
	server string
	tool   string
}

type workflowMCPRecordingManager struct {
	calls []workflowMCPCall
}

func (m *workflowMCPRecordingManager) CallTool(
	_ context.Context,
	serverName, toolName string,
	_ map[string]any,
) (*sdkmcp.CallToolResult, error) {
	m.calls = append(m.calls, workflowMCPCall{server: serverName, tool: toolName})
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}},
	}, nil
}

func TestWorkflowToolRunnerRequiresExactMCPIdentity(t *testing.T) {
	t.Run("boundary-canonical alternate is not executed", func(t *testing.T) {
		manager := &workflowMCPRecordingManager{}
		registry := tools.NewToolRegistry()
		registry.Register(tools.NewMCPTool(
			manager,
			"a",
			&sdkmcp.Tool{Name: "b_c"},
		))
		runner := &workflowToolRunner{agentID: "main", registry: registry}

		_, err := runner.RunTool(context.Background(), workflows.ToolRequest{
			Name:      picomcp.CanonicalToolName("a_b", "c"),
			MCP:       true,
			MCPServer: "a_b",
			MCPTool:   "c",
		})
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("RunTool() error = %v, want exact MCP identity mismatch", err)
		}
		if len(manager.calls) != 0 {
			t.Fatalf("MCP calls = %#v, want no execution", manager.calls)
		}
	})

	t.Run("exact identity executes", func(t *testing.T) {
		manager := &workflowMCPRecordingManager{}
		registry := tools.NewToolRegistry()
		registry.Register(tools.NewMCPTool(
			manager,
			"a_b",
			&sdkmcp.Tool{Name: "c"},
		))
		runner := &workflowToolRunner{agentID: "main", registry: registry}

		if _, err := runner.RunTool(context.Background(), workflows.ToolRequest{
			Name:      picomcp.CanonicalToolName("a_b", "c"),
			MCP:       true,
			MCPServer: "a_b",
			MCPTool:   "c",
		}); err != nil {
			t.Fatalf("RunTool() error = %v", err)
		}
		if len(manager.calls) != 1 ||
			manager.calls[0] != (workflowMCPCall{server: "a_b", tool: "c"}) {
			t.Fatalf("MCP calls = %#v, want exact a_b/c execution", manager.calls)
		}
	})
}

func TestWorkflowToolResultOutputsExposesJSONFields(t *testing.T) {
	outputs := workflowToolResultOutputs(tools.SilentResult(`{
  "workspace": {
    "id": "gw-test",
    "path": "/tmp/repo"
  },
  "next": "inspect path"
}`))

	workspace, ok := outputs["workspace"].(map[string]any)
	if !ok {
		t.Fatalf("workspace output = %#v, want parsed object", outputs["workspace"])
	}
	if workspace["path"] != "/tmp/repo" {
		t.Fatalf("workspace.path = %#v, want /tmp/repo", workspace["path"])
	}
	jsonOutput, ok := outputs["json"].(map[string]any)
	if !ok || jsonOutput["next"] != "inspect path" {
		t.Fatalf("json output = %#v, want parsed tool JSON", outputs["json"])
	}
	if outputs["text"] == "" {
		t.Fatalf("text output should preserve original content: %#v", outputs)
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
func (m *workflowMediaChannelManager) InvokeTypingStopForMessage(string, string, string) {
}

func (m *workflowMediaChannelManager) CleanupTurnUXForMessage(
	context.Context, string, string, string,
) {
}

func (m *workflowMediaChannelManager) RebindTurnUXForMessage(string, string, string, string) {
}

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

func (m *workflowMediaChannelManager) SendPlaceholderForMessage(
	context.Context,
	string,
	string,
	string,
) bool {
	return false
}

func (m *workflowMediaChannelManager) DismissToolFeedback(context.Context, string, string, *bus.InboundContext) {
}
