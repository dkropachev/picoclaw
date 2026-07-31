package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type activityTestProvider struct{}

func (*activityTestProvider) Chat(
	context.Context,
	[]providers.Message,
	[]providers.ToolDefinition,
	string,
	map[string]any,
) (*providers.LLMResponse, error) {
	return &providers.LLMResponse{Content: "unused"}, nil
}

func TestProjectAgentActivityDetailsUsesExactSafeKindTypeUnion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		kind      runtimeevents.Kind
		payload   any
		wantType  any
		wantLevel runtimeevents.Severity
	}{
		{
			name:      "turn start",
			kind:      runtimeevents.KindAgentTurnStart,
			payload:   TurnStartPayload{UserMessage: "secret", MediaCount: 2},
			wantType:  AgentActivityTurnStartDetails{},
			wantLevel: runtimeevents.SeverityInfo,
		},
		{
			name: "turn error",
			kind: runtimeevents.KindAgentTurnEnd,
			payload: TurnEndPayload{
				Status:       TurnEndStatusError,
				Workspace:    "/private/path",
				Iterations:   3,
				Duration:     1500 * time.Millisecond,
				FinalContent: "secret-result",
			},
			wantType:  AgentActivityTurnEndDetails{},
			wantLevel: runtimeevents.SeverityError,
		},
		{
			name: "llm request",
			kind: runtimeevents.KindAgentLLMRequest,
			payload: LLMRequestPayload{
				Model:         "private-provider/model",
				MessagesCount: 4,
				ToolsCount:    2,
			},
			wantType:  AgentActivityLLMRequestDetails{},
			wantLevel: runtimeevents.SeverityInfo,
		},
		{
			name: "llm response",
			kind: runtimeevents.KindAgentLLMResponse,
			payload: LLMResponsePayload{
				ContentLen:   999,
				ToolCalls:    1,
				HasReasoning: true,
			},
			wantType:  AgentActivityLLMResponseDetails{},
			wantLevel: runtimeevents.SeverityInfo,
		},
		{
			name: "llm retry",
			kind: runtimeevents.KindAgentLLMRetry,
			payload: LLMRetryPayload{
				Attempt:    1,
				MaxRetries: 3,
				Reason:     "private reason",
				Error:      "private error",
				Backoff:    time.Second,
			},
			wantType:  AgentActivityLLMRetryDetails{},
			wantLevel: runtimeevents.SeverityWarn,
		},
		{
			name: "context compress",
			kind: runtimeevents.KindAgentContextCompress,
			payload: ContextCompressPayload{
				Reason:            ContextCompressReasonRetry,
				DroppedMessages:   2,
				RemainingMessages: 3,
			},
			wantType:  AgentActivityContextCompressDetails{},
			wantLevel: runtimeevents.SeverityWarn,
		},
		{
			name: "session summarize",
			kind: runtimeevents.KindAgentSessionSummarize,
			payload: SessionSummarizePayload{
				SummarizedMessages: 2,
				KeptMessages:       1,
				SummaryLen:         500,
				OmittedOversized:   true,
			},
			wantType:  AgentActivitySessionSummarizeDetails{},
			wantLevel: runtimeevents.SeverityInfo,
		},
		{
			name: "tool start",
			kind: runtimeevents.KindAgentToolExecStart,
			payload: ToolExecStartPayload{
				Tool:      "exec_command",
				Arguments: map[string]any{"token": "private-argument"},
			},
			wantType:  AgentActivityToolStartDetails{},
			wantLevel: runtimeevents.SeverityInfo,
		},
		{
			name: "tool error",
			kind: runtimeevents.KindAgentToolExecEnd,
			payload: ToolExecEndPayload{
				Tool:     "exec_command",
				Duration: 2 * time.Second,
				IsError:  true,
				Async:    true,
			},
			wantType:  AgentActivityToolEndDetails{},
			wantLevel: runtimeevents.SeverityWarn,
		},
		{
			name: "tool skipped",
			kind: runtimeevents.KindAgentToolExecSkipped,
			payload: ToolExecSkippedPayload{
				Tool:   "exec_command",
				Reason: "private reason",
			},
			wantType:  AgentActivityToolSkippedDetails{},
			wantLevel: runtimeevents.SeverityWarn,
		},
		{
			name: "steering",
			kind: runtimeevents.KindAgentSteeringInjected,
			payload: SteeringInjectedPayload{
				Count:           2,
				TotalContentLen: 800,
			},
			wantType:  AgentActivitySteeringDetails{},
			wantLevel: runtimeevents.SeverityInfo,
		},
		{
			name: "follow up",
			kind: runtimeevents.KindAgentFollowUpQueued,
			payload: FollowUpQueuedPayload{
				SourceTool: "private_tool",
				ContentLen: 800,
			},
			wantType:  AgentActivityEmptyDetails{},
			wantLevel: runtimeevents.SeverityInfo,
		},
		{
			name: "interrupt",
			kind: runtimeevents.KindAgentInterruptReceived,
			payload: InterruptReceivedPayload{
				Kind:       InterruptKindHard,
				Role:       "private-role",
				ContentLen: 800,
				QueueDepth: 2,
			},
			wantType:  AgentActivityInterruptDetails{},
			wantLevel: runtimeevents.SeverityInfo,
		},
		{
			name: "subturn spawn",
			kind: runtimeevents.KindAgentSubTurnSpawn,
			payload: SubTurnSpawnPayload{
				AgentID:      "research",
				Label:        "private-label",
				ParentTurnID: "private-turn",
			},
			wantType:  AgentActivitySubTurnSpawnDetails{},
			wantLevel: runtimeevents.SeverityInfo,
		},
		{
			name: "subturn end error",
			kind: runtimeevents.KindAgentSubTurnEnd,
			payload: SubTurnEndPayload{
				AgentID: "research",
				Status:  "error",
			},
			wantType:  AgentActivitySubTurnEndDetails{},
			wantLevel: runtimeevents.SeverityError,
		},
		{
			name: "subturn result",
			kind: runtimeevents.KindAgentSubTurnResultDelivered,
			payload: SubTurnResultDeliveredPayload{
				TargetChannel: "private-channel",
				TargetChatID:  "private-chat",
				ContentLen:    800,
			},
			wantType:  AgentActivityEmptyDetails{},
			wantLevel: runtimeevents.SeverityInfo,
		},
		{
			name: "subturn orphan",
			kind: runtimeevents.KindAgentSubTurnOrphan,
			payload: SubTurnOrphanPayload{
				ParentTurnID: "private-parent",
				ChildTurnID:  "private-child",
				Reason:       "private-reason",
			},
			wantType:  AgentActivityEmptyDetails{},
			wantLevel: runtimeevents.SeverityError,
		},
		{
			name: "agent error",
			kind: runtimeevents.KindAgentError,
			payload: ErrorPayload{
				Stage:   "private-stage",
				Message: "private-error",
			},
			wantType:  AgentActivityEmptyDetails{},
			wantLevel: runtimeevents.SeverityError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			event, ok := projectAgentActivityEvent(activityEvent(
				"main",
				test.kind,
				test.payload,
			))
			if !ok {
				t.Fatal("projectAgentActivityEvent() rejected valid event")
			}
			if got, want := typeName(event.Details), typeName(test.wantType); got != want {
				t.Fatalf("details type = %s, want %s", got, want)
			}
			if event.Severity != test.wantLevel {
				t.Fatalf("severity = %q, want %q", event.Severity, test.wantLevel)
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			for _, forbidden := range []string{
				"secret",
				"private-",
				"/private/",
				`"attrs"`,
				`"session`,
				`"chat`,
				`"provider`,
				`"model`,
				`"arguments"`,
				`"result"`,
				`"reason":"private`,
				`"message"`,
			} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("safe projection exposed %q: %s", forbidden, encoded)
				}
			}
		})
	}
}

func TestAgentActivityRecorderCountsProjectionOmissions(t *testing.T) {
	t.Parallel()

	recorder := newAgentActivityRecorder(4)
	invalid := make([]runtimeevents.Event, 0, 5)
	invalid = append(invalid,
		activityEvent("main", runtimeevents.KindAgentTurnStart, map[string]any{
			"user_message": "secret",
		}),
		activityEvent("main", runtimeevents.KindAgentLLMDelta, LLMDeltaPayload{
			ContentDeltaLen: 5,
		}),
		activityEvent("Main", runtimeevents.KindAgentTurnStart, TurnStartPayload{}),
		activityEvent("main", runtimeevents.KindAgentToolExecStart, ToolExecStartPayload{
			Tool: "tool/with/path",
		}),
	)
	invalidSource := activityEvent(
		"main",
		runtimeevents.KindAgentTurnStart,
		TurnStartPayload{},
	)
	invalidSource.Source.Name = "other"
	invalid = append(invalid, invalidSource)

	for _, event := range invalid {
		if err := recorder.handle(context.Background(), event); err != nil {
			t.Fatalf("handle() error = %v", err)
		}
	}
	page, err := recorder.snapshot("main", "", 10, 7)
	if err != nil {
		t.Fatalf("snapshot() error = %v", err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("events = %#v, want empty", page.Events)
	}
	if page.Dropped != (AgentActivityDroppedCounters{
		Subscription: "7",
		Retention:    "0",
		Projection:   strconv.Itoa(len(invalid)),
	}) {
		t.Fatalf("dropped = %#v", page.Dropped)
	}
}

func TestAgentActivityRecorderPagingResetAndTruncation(t *testing.T) {
	t.Parallel()

	recorder := newAgentActivityRecorder(3)
	initial, err := recorder.snapshot("main", "", 2, 0)
	if err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}
	for index := 0; index < 5; index++ {
		_ = recorder.handle(context.Background(), activityEvent(
			"main",
			runtimeevents.KindAgentTurnStart,
			TurnStartPayload{MediaCount: index},
		))
	}

	newest, err := recorder.snapshot("main", "", 2, 4)
	if err != nil {
		t.Fatalf("newest snapshot: %v", err)
	}
	assertActivitySequences(t, newest.Events, "4", "5")
	if !newest.Truncated || newest.Reset {
		t.Fatalf("newest flags = reset:%t truncated:%t", newest.Reset, newest.Truncated)
	}
	if newest.Dropped != (AgentActivityDroppedCounters{
		Subscription: "4",
		Retention:    "2",
		Projection:   "0",
	}) {
		t.Fatalf("newest dropped = %#v", newest.Dropped)
	}

	fromOldCursor, err := recorder.snapshot("main", initial.NextCursor, 2, 0)
	if err != nil {
		t.Fatalf("old cursor snapshot: %v", err)
	}
	assertActivitySequences(t, fromOldCursor.Events, "3", "4")
	if !fromOldCursor.Truncated {
		t.Fatal("old cursor did not report truncation")
	}
	lastPage, err := recorder.snapshot("main", fromOldCursor.NextCursor, 2, 0)
	if err != nil {
		t.Fatalf("last page: %v", err)
	}
	assertActivitySequences(t, lastPage.Events, "5")

	otherGeneration := newAgentActivityRecorder(4)
	for index := 0; index < 3; index++ {
		_ = otherGeneration.handle(context.Background(), activityEvent(
			"main",
			runtimeevents.KindAgentTurnStart,
			TurnStartPayload{},
		))
	}
	reset, err := otherGeneration.snapshot("main", newest.NextCursor, 2, 0)
	if err != nil {
		t.Fatalf("reset snapshot: %v", err)
	}
	assertActivitySequences(t, reset.Events, "2", "3")
	if !reset.Reset || !reset.Truncated {
		t.Fatalf("reset flags = reset:%t truncated:%t", reset.Reset, reset.Truncated)
	}
}

func TestAgentActivityRecorderPollingDoesNotLoseOtherAgentEvents(t *testing.T) {
	t.Parallel()

	recorder := newAgentActivityRecorder(10)
	first, _ := recorder.snapshot("main", "", 2, 0)
	_ = recorder.handle(context.Background(), activityEvent(
		"other",
		runtimeevents.KindAgentTurnStart,
		TurnStartPayload{},
	))
	_ = recorder.handle(context.Background(), activityEvent(
		"main",
		runtimeevents.KindAgentTurnStart,
		TurnStartPayload{},
	))
	_ = recorder.handle(context.Background(), activityEvent(
		"main",
		runtimeevents.KindAgentTurnEnd,
		TurnEndPayload{Status: TurnEndStatusCompleted},
	))
	_ = recorder.handle(context.Background(), activityEvent(
		"main",
		runtimeevents.KindAgentError,
		ErrorPayload{},
	))

	page, err := recorder.snapshot("main", first.NextCursor, 2, 0)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	assertActivitySequences(t, page.Events, "2", "3")
	if !page.Truncated {
		t.Fatal("incremental page with additional events did not report truncation")
	}
	next, err := recorder.snapshot("main", page.NextCursor, 2, 0)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	assertActivitySequences(t, next.Events, "4")
	empty, err := recorder.snapshot("main", next.NextCursor, 2, 0)
	if err != nil || len(empty.Events) != 0 {
		t.Fatalf("empty poll = %#v, %v", empty, err)
	}
}

func TestParseAgentActivityQueryIsStrictAndBounded(t *testing.T) {
	t.Parallel()

	recorder := newAgentActivityRecorder(1)
	page, _ := recorder.snapshot("main", "", 1, 0)
	valid := []string{
		"",
		"limit=1",
		"cursor=" + page.NextCursor,
		"cursor=" + page.NextCursor + "&limit=100",
	}
	for _, raw := range valid {
		if _, _, err := ParseAgentActivityQuery(raw); err != nil {
			t.Errorf("ParseAgentActivityQuery(%q) error = %v", raw, err)
		}
	}
	invalid := []string{
		"unknown=1",
		"limit=",
		"limit=0",
		"limit=101",
		"limit=-1",
		"limit=1.0",
		"limit=01",
		"limit=1&limit=2",
		"cursor=",
		"cursor=not-a-cursor",
		"cursor=" + page.NextCursor + "&cursor=" + page.NextCursor,
		"broken=%zz",
		strings.Repeat("x", maxAgentActivityQueryBytes+1),
	}
	for _, raw := range invalid {
		if _, _, err := ParseAgentActivityQuery(raw); !errors.Is(
			err,
			ErrInvalidAgentActivityQuery,
		) {
			t.Errorf("ParseAgentActivityQuery(%q) error = %v", raw, err)
		}
	}
}

func TestDecodeAgentActivityPageRejectsShadowingAndUnsafeShapes(t *testing.T) {
	t.Parallel()

	recorder := newAgentActivityRecorder(2)
	_ = recorder.handle(context.Background(), activityEvent(
		"main",
		runtimeevents.KindAgentToolExecStart,
		ToolExecStartPayload{
			Tool:      "exec_command",
			Arguments: map[string]any{"token": "private"},
		},
	))
	page, _ := recorder.snapshot("main", "", 1, 0)
	valid, err := MarshalAgentActivityPage(page)
	if err != nil {
		t.Fatalf("MarshalAgentActivityPage() error = %v", err)
	}
	decoded, err := DecodeAgentActivityPage(valid)
	if err != nil || decoded.AgentID != "main" || len(decoded.Events) != 1 {
		t.Fatalf("DecodeAgentActivityPage() = %#v, %v", decoded, err)
	}

	const (
		validCursorJSON = `"next_cursor":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",` +
			`"reset":false,"truncated":false,` +
			`"dropped":{"subscription":"0","retention":"0","projection":"0"}`
		eventPrefixJSON = `{"sequence":"1","agent_id":"main",` +
			`"timestamp":"2026-01-01T00:00:00Z",`
		eventCursorJSON = `"next_cursor":"AAAAAAAAAAAAAAAAAAAAAAEAAAAAAAAA",` +
			`"reset":false,"truncated":false,` +
			`"dropped":{"subscription":"0","retention":"0","projection":"0"}`
	)
	invalid := [][]byte{
		[]byte(
			`{"agent_id":"main","Agent_ID":"other","events":[],` +
				validCursorJSON + `}`,
		),
		[]byte(
			`{"agent_id":"main","events":[` + eventPrefixJSON +
				`"kind":"agent.tool.exec_start","severity":"info",` +
				`"details":{"tool_name":"exec","Tool_Name":"shadow"}}],` +
				eventCursorJSON + `}`,
		),
		[]byte(
			`{"agent_id":"main","events":[` + eventPrefixJSON +
				`"kind":"agent.tool.exec_start","\u212Aind":"agent.error",` +
				`"severity":"info","details":{"tool_name":"exec"}}],` +
				eventCursorJSON + `}`,
		),
		[]byte(
			`{"agent_id":"main","events":[],"unknown":true,` +
				validCursorJSON + `}`,
		),
		[]byte(
			`{"agent_id":"main","events":null,` + validCursorJSON + `}`,
		),
		[]byte(
			`{"agent_id":"main","events":[],"next_cursor":"bad",` +
				`"reset":false,"truncated":false,` +
				`"dropped":{"subscription":"0","retention":"0","projection":"0"}}`,
		),
		[]byte(
			`{"agent_id":"main","events":[],` +
				`"next_cursor":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",` +
				`"reset":false,"truncated":false,` +
				`"dropped":{"subscription":"00","retention":"0","projection":"0"}}`,
		),
		[]byte(
			`{"agent_id":"main","events":[` + eventPrefixJSON +
				`"kind":"agent.tool.exec_start","severity":"info",` +
				`"details":{"tool_name":"../../secret"}}],` +
				eventCursorJSON + `}`,
		),
		append([]byte(`{"agent_id":"ma`), 0xff),
	}
	for index, raw := range invalid {
		if _, decodeErr := DecodeAgentActivityPage(raw); decodeErr == nil {
			t.Errorf("invalid case %d decoded successfully: %s", index, raw)
		}
	}
}

func TestAgentActivityStrictCodecRequiresEveryCanonicalMember(t *testing.T) {
	t.Parallel()

	missingReset := []byte(
		`{"agent_id":"main","events":[],` +
			`"next_cursor":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",` +
			`"truncated":false,` +
			`"dropped":{"subscription":"0","retention":"0","projection":"0"}}`,
	)
	if _, err := DecodeAgentActivityPage(missingReset); err == nil {
		t.Fatal("page without reset decoded successfully")
	}
	nullDropped := []byte(
		`{"agent_id":"main","events":[],` +
			`"next_cursor":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",` +
			`"reset":false,"truncated":false,"dropped":null}`,
	)
	if _, err := DecodeAgentActivityPage(nullDropped); err == nil {
		t.Fatal("page with null dropped counters decoded successfully")
	}
	for name, raw := range map[string]string{
		"null details": "null",
		"missing boolean": `{"tool_name":"exec_command",` +
			`"duration_ms":"0","is_error":false}`,
		"case changed field": `{"Tool_Name":"exec_command",` +
			`"duration_ms":"0","is_error":false,"async":false}`,
	} {
		if _, err := decodeAgentActivityDetails(
			runtimeevents.KindAgentToolExecEnd,
			json.RawMessage(raw),
		); err == nil {
			t.Errorf("%s decoded successfully", name)
		}
	}
	event := AgentActivityEvent{
		Sequence:  "1",
		AgentID:   "main",
		Timestamp: "2026-01-01T00:00:00+00:00",
		Kind:      runtimeevents.KindAgentTurnStart,
		Severity:  runtimeevents.SeverityInfo,
		Details:   AgentActivityTurnStartDetails{MediaCount: 0},
	}
	if err := validateAgentActivityEvent(event); err == nil {
		t.Fatal("noncanonical UTC timestamp validated successfully")
	}
}

func TestAgentActivityCodecRejectsInvalidTimestampAndKindDetailsUnion(t *testing.T) {
	t.Parallel()

	recorder := newAgentActivityRecorder(1)
	if err := recorder.handle(context.Background(), activityEvent(
		"main",
		runtimeevents.KindAgentTurnStart,
		TurnStartPayload{},
	)); err != nil {
		t.Fatalf("handle() error = %v", err)
	}
	page, err := recorder.snapshot("main", "", 1, 0)
	if err != nil {
		t.Fatalf("snapshot() error = %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events = %#v, want one event", page.Events)
	}

	for _, timestamp := range []string{
		"0001-01-01T00:00:00Z",
		"9999-12-31T23:59:59.999999999Z",
	} {
		boundary := page
		boundary.Events = append([]AgentActivityEvent(nil), page.Events...)
		boundary.Events[0].Timestamp = timestamp
		raw, marshalErr := MarshalAgentActivityPage(boundary)
		if marshalErr != nil {
			t.Errorf("MarshalAgentActivityPage(timestamp %q) error = %v", timestamp, marshalErr)
			continue
		}
		if _, decodeErr := DecodeAgentActivityPage(raw); decodeErr != nil {
			t.Errorf("DecodeAgentActivityPage(timestamp %q) error = %v", timestamp, decodeErr)
		}
	}

	yearZero := page
	yearZero.Events = append([]AgentActivityEvent(nil), page.Events...)
	yearZero.Events[0].Timestamp = "0000-01-01T00:00:00Z"
	if _, err = MarshalAgentActivityPage(yearZero); err == nil {
		t.Fatal("page with year-zero timestamp marshaled successfully")
	}

	mismatchedDetails := page
	mismatchedDetails.Events = append([]AgentActivityEvent(nil), page.Events...)
	mismatchedDetails.Events[0].Details = AgentActivityToolStartDetails{
		ToolName: "exec_command",
	}
	if _, err = MarshalAgentActivityPage(mismatchedDetails); err == nil {
		t.Fatal("page with mismatched kind and details marshaled successfully")
	}

	unknownKind := page
	unknownKind.Events = append([]AgentActivityEvent(nil), page.Events...)
	unknownKind.Events[0].Kind = runtimeevents.Kind("agent.unknown")
	unknownKind.Events[0].Details = AgentActivityEmptyDetails{}
	if _, err = MarshalAgentActivityPage(unknownKind); err == nil {
		t.Fatal("page with unknown kind marshaled successfully")
	}

	raw, err := json.Marshal(yearZero)
	if err != nil {
		t.Fatalf("json.Marshal(yearZero) error = %v", err)
	}
	if _, err = DecodeAgentActivityPage(raw); err == nil {
		t.Fatal("page with year-zero timestamp decoded successfully")
	}
}

func TestAgentLoopActivitySubscriptionFiltersDomainAndCloses(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP.Enabled = false
	messageBus := bus.NewMessageBus()
	loop := newTestAgentLoopWithStrictModels(cfg, messageBus, &activityTestProvider{})
	loop.agentActivityMu.RLock()
	subscription := loop.agentActivitySub
	loop.agentActivityMu.RUnlock()
	if subscription == nil {
		t.Fatal("agent activity subscription is nil")
	}

	eventBus := loop.RuntimeEventBus()
	eventBus.PublishNonBlocking(runtimeevents.Event{
		Kind:   runtimeevents.KindWorkflowRunStart,
		Source: runtimeevents.Source{Component: "workflow", Name: "private"},
		Scope:  runtimeevents.Scope{AgentID: "main"},
		Payload: map[string]any{
			"secret": "workflow payload",
		},
	})
	eventBus.PublishNonBlocking(activityEvent(
		"main",
		runtimeevents.KindAgentLLMDelta,
		LLMDeltaPayload{ContentDeltaLen: 100},
	))
	for range agentActivitySubscriptionBuffer * 2 {
		eventBus.PublishNonBlocking(activityEvent(
			"main",
			runtimeevents.KindAgentLLMDelta,
			LLMDeltaPayload{ContentDeltaLen: 100},
		))
	}
	eventBus.PublishNonBlocking(activityEvent(
		"main",
		runtimeevents.KindAgentTurnStart,
		TurnStartPayload{UserMessage: "private"},
	))

	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := loop.AgentActivity("main", "", 10)
		if err == nil &&
			len(page.Events) == 1 &&
			page.Dropped.Projection == "0" &&
			page.Dropped.Subscription == "0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("activity did not settle: page=%#v err=%v", page, err)
		}
		time.Sleep(time.Millisecond)
	}

	loop.Close()
	messageBus.Close()
	select {
	case <-subscription.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("activity subscription did not close")
	}
	if _, err := loop.AgentActivity("main", "", 10); !errors.Is(
		err,
		ErrAgentActivityUnavailable,
	) {
		t.Fatalf("AgentActivity() after close error = %v", err)
	}
}

func activityEvent(
	agentID string,
	kind runtimeevents.Kind,
	payload any,
) runtimeevents.Event {
	return runtimeevents.Event{
		Kind:   kind,
		Time:   time.Date(2026, time.July, 30, 12, 0, 0, 0, time.FixedZone("test", -4*60*60)),
		Source: runtimeevents.Source{Component: "agent", Name: agentID},
		Scope: runtimeevents.Scope{
			AgentID:    agentID,
			SessionKey: "private-session",
			TurnID:     "private-turn",
			ChatID:     "private-chat",
		},
		Attrs: map[string]any{
			"private": "attribute",
		},
		Payload: payload,
	}
}

func assertActivitySequences(
	t *testing.T,
	events []AgentActivityEvent,
	want ...string,
) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("event count = %d, want %d: %#v", len(events), len(want), events)
	}
	for index := range want {
		if events[index].Sequence != want[index] {
			t.Fatalf(
				"event %d sequence = %q, want %q",
				index,
				events[index].Sequence,
				want[index],
			)
		}
	}
}

func typeName(value any) string {
	return fmt.Sprintf("%T", value)
}
