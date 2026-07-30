package workflows

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
)

const allTriggerSimulationYAML = `
name: All trigger simulation
on:
  manual: {}
  schedule:
    - cron: "0 * * * *"
  channel_message:
    channels: telegram
    passthrough: true
  command:
    name: deploy
    channels: telegram
  runtime_event:
    kinds: agent.turn.end
    sources: agent/main
  event:
    sources: github
    types: issues.opened
  workflow_call:
    inputs:
      mode:
        type: string
        default: safe
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/main
`

func TestSimulateWorkflowTriggerAllFamilies(t *testing.T) {
	t.Parallel()
	scheduledAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	runtimeEvent := runtimeevents.Event{
		ID:   "runtime-1",
		Kind: runtimeevents.KindAgentTurnEnd,
		Time: scheduledAt,
		Source: runtimeevents.Source{
			Component: "agent",
			Name:      "main",
		},
	}
	externalEvent := eventing.Envelope{
		ID:         "ev_0123456789abcdef0123456789abcdef",
		Source:     "github",
		Connector:  "primary",
		Type:       "issues.opened",
		DedupeKey:  "delivery-1",
		ReceivedAt: scheduledAt,
		Payload:    json.RawMessage(`{"repository":"acme/widgets"}`),
	}
	scheduleIndex := 0
	tests := []struct {
		name     string
		selector WorkflowTriggerSelector
		scenario WorkflowTriggerSimulationScenario
	}{
		{
			name:     "manual",
			selector: WorkflowTriggerSelector{Kind: WorkflowTriggerManual},
			scenario: WorkflowTriggerSimulationScenario{
				Invocation: &WorkflowTriggerInvocation{},
			},
		},
		{
			name: "schedule",
			selector: WorkflowTriggerSelector{
				Kind:          WorkflowTriggerSchedule,
				ScheduleIndex: &scheduleIndex,
			},
			scenario: WorkflowTriggerSimulationScenario{ScheduledAt: &scheduledAt},
		},
		{
			name:     "channel_message",
			selector: WorkflowTriggerSelector{Kind: WorkflowTriggerChannelMessage},
			scenario: WorkflowTriggerSimulationScenario{
				Message: &ChannelMessageEvent{
					Channel: "telegram",
					ChatID:  "chat-1",
					Text:    "hello",
				},
			},
		},
		{
			name:     "command",
			selector: WorkflowTriggerSelector{Kind: WorkflowTriggerCommand},
			scenario: WorkflowTriggerSimulationScenario{
				Message: &ChannelMessageEvent{
					Channel: "telegram",
					ChatID:  "chat-1",
					Text:    "/deploy production",
				},
			},
		},
		{
			name:     "runtime_event",
			selector: WorkflowTriggerSelector{Kind: WorkflowTriggerRuntimeEvent},
			scenario: WorkflowTriggerSimulationScenario{RuntimeEvent: &runtimeEvent},
		},
		{
			name:     "event",
			selector: WorkflowTriggerSelector{Kind: WorkflowTriggerEvent},
			scenario: WorkflowTriggerSimulationScenario{Event: &externalEvent},
		},
		{
			name:     "workflow_call",
			selector: WorkflowTriggerSelector{Kind: WorkflowTriggerWorkflowCall},
			scenario: WorkflowTriggerSimulationScenario{
				Invocation: &WorkflowTriggerInvocation{},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := SimulateWorkflowTrigger(WorkflowTriggerSimulationInput{
				YAML:        allTriggerSimulationYAML,
				WorkflowRef: "workflows/all.yml",
				Trigger:     test.selector,
				Scenario:    test.scenario,
			})
			if err != nil {
				t.Fatalf("SimulateWorkflowTrigger() error = %v", err)
			}
			if !result.Simulation.Present ||
				!result.Simulation.Matched ||
				!result.Simulation.Executable ||
				result.Simulation.Reason != WorkflowTriggerSimulationMatched ||
				result.Simulation.EffectiveKind != test.selector.Kind {
				t.Fatalf("simulation = %#v", result.Simulation)
			}
			request, ok := result.RunRequest()
			if !ok || request.Workflow == nil {
				t.Fatalf("RunRequest() = %#v, %v", request, ok)
			}
			if request.Ref != "workflows/all.yml" ||
				request.WorkflowRef != "workflows/all.yml" ||
				request.Inputs["mode"] != "safe" {
				t.Fatalf("run request = %#v", request)
			}
		})
	}
}

func TestMatchInboundMessageTriggerCommandFirst(t *testing.T) {
	t.Parallel()
	workflow := parseWorkflow(t, `
name: Command first
on:
  command:
    name: deploy
    args:
      target:
        type: string
        required: true
  channel_message:
    text_matches: ".*"
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/main
`)
	match, ok, err := MatchInboundMessageTrigger(
		workflow,
		"workflows/command-first.yml",
		ChannelMessageEvent{Text: "/deploy production"},
	)
	if err != nil || !ok || match == nil || match.Kind != WorkflowTriggerCommand {
		t.Fatalf("command-first match = %#v, %v, %v", match, ok, err)
	}

	match, ok, err = MatchInboundMessageTrigger(
		workflow,
		"workflows/command-first.yml",
		ChannelMessageEvent{Text: "/deploy"},
	)
	if err == nil || ok || match != nil {
		t.Fatalf("required arg result = %#v, %v, %v; want command error without fallback", match, ok, err)
	}

	result, simulateErr := SimulateWorkflowTrigger(WorkflowTriggerSimulationInput{
		YAML:        allTriggerSimulationYAML,
		WorkflowRef: "workflows/all.yml",
		Trigger:     WorkflowTriggerSelector{Kind: WorkflowTriggerChannelMessage},
		Scenario: WorkflowTriggerSimulationScenario{
			Message: &ChannelMessageEvent{
				Channel: "telegram",
				Text:    "/deploy production",
			},
		},
	})
	if simulateErr != nil {
		t.Fatalf("SimulateWorkflowTrigger() error = %v", simulateErr)
	}
	if result.Simulation.Matched ||
		result.Simulation.EffectiveKind != WorkflowTriggerCommand ||
		result.Simulation.Reason != WorkflowTriggerSimulationShadowedByCommand {
		t.Fatalf("shadow simulation = %#v", result.Simulation)
	}
}

func TestSimulateWorkflowTriggerRuntimeFeedbackSuppressed(t *testing.T) {
	t.Parallel()
	const raw = `
name: Runtime feedback
on:
  runtime_event:
    kinds: workflow.run.start
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/main
`
	event := runtimeevents.Event{
		Kind: runtimeevents.KindWorkflowRunStart,
		Source: runtimeevents.Source{
			Component: "workflow",
			Name:      "workflows/runtime.yml",
		},
	}
	result, err := SimulateWorkflowTrigger(WorkflowTriggerSimulationInput{
		YAML:        raw,
		WorkflowRef: "workflows/runtime.yml",
		Trigger:     WorkflowTriggerSelector{Kind: WorkflowTriggerRuntimeEvent},
		Scenario:    WorkflowTriggerSimulationScenario{RuntimeEvent: &event},
	})
	if err != nil {
		t.Fatalf("SimulateWorkflowTrigger() error = %v", err)
	}
	if result.Simulation.Matched ||
		result.Simulation.Reason != WorkflowTriggerSimulationRuntimeFeedbackSuppressed {
		t.Fatalf("simulation = %#v", result.Simulation)
	}
}

func TestSimulateWorkflowTriggerScheduleMatchesSharedContext(t *testing.T) {
	t.Parallel()
	workflow := parseWorkflow(t, allTriggerSimulationYAML)
	scheduledAt := time.Date(
		2026, 7, 30, 9, 45, 0, 123,
		time.FixedZone("test", -4*60*60),
	)
	want, err := BuildWorkflowScheduleRunContext(
		workflow,
		"workflows/all.yml",
		0,
		scheduledAt,
	)
	if err != nil {
		t.Fatalf("BuildWorkflowScheduleRunContext() error = %v", err)
	}
	index := 0
	result, err := SimulateWorkflowTrigger(WorkflowTriggerSimulationInput{
		YAML:        allTriggerSimulationYAML,
		WorkflowRef: "workflows/all.yml",
		Trigger: WorkflowTriggerSelector{
			Kind:          WorkflowTriggerSchedule,
			ScheduleIndex: &index,
		},
		Scenario: WorkflowTriggerSimulationScenario{ScheduledAt: &scheduledAt},
	})
	if err != nil {
		t.Fatalf("SimulateWorkflowTrigger() error = %v", err)
	}
	request, ok := result.RunRequest()
	if !ok {
		t.Fatal("RunRequest() unavailable")
	}
	if !reflect.DeepEqual(request.Event, want.Event) ||
		request.Session != want.Session ||
		!reflect.DeepEqual(request.Delivery, want.Delivery) {
		t.Fatalf("request context = %#v, want %#v", request, want)
	}
	if request.Inputs["cron"] != want.Inputs["cron"] ||
		request.Inputs["scheduled_at"] != want.Inputs["scheduled_at"] {
		t.Fatalf("request inputs = %#v, want %#v", request.Inputs, want.Inputs)
	}
}

func TestSimulateWorkflowTriggerScheduleSelectionReasons(t *testing.T) {
	t.Parallel()
	scheduledAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	index := 7
	outOfRange, err := SimulateWorkflowTrigger(WorkflowTriggerSimulationInput{
		YAML:        allTriggerSimulationYAML,
		WorkflowRef: "workflows/all.yml",
		Trigger: WorkflowTriggerSelector{
			Kind:          WorkflowTriggerSchedule,
			ScheduleIndex: &index,
		},
		Scenario: WorkflowTriggerSimulationScenario{ScheduledAt: &scheduledAt},
	})
	if err != nil {
		t.Fatalf("out-of-range simulation error = %v", err)
	}
	if outOfRange.Simulation.Present ||
		outOfRange.Simulation.Reason != WorkflowTriggerSimulationScheduleIndexOutOfRange {
		t.Fatalf("out-of-range simulation = %#v", outOfRange.Simulation)
	}

	index = 0
	absent, err := SimulateWorkflowTrigger(WorkflowTriggerSimulationInput{
		YAML: `
name: Manual only
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/main
`,
		WorkflowRef: "workflows/manual.yml",
		Trigger: WorkflowTriggerSelector{
			Kind:          WorkflowTriggerSchedule,
			ScheduleIndex: &index,
		},
		Scenario: WorkflowTriggerSimulationScenario{ScheduledAt: &scheduledAt},
	})
	if err != nil {
		t.Fatalf("absent simulation error = %v", err)
	}
	if absent.Simulation.Present ||
		absent.Simulation.Reason != WorkflowTriggerSimulationTriggerAbsent {
		t.Fatalf("absent simulation = %#v", absent.Simulation)
	}
}

func TestSimulateWorkflowTriggerWorkflowCallContractAndDetachment(t *testing.T) {
	t.Parallel()
	const raw = `
name: Workflow call
on:
  workflow_call:
    inputs:
      mode:
        type: string
        default: safe
      config:
        type: object
    secrets:
      TOKEN:
        required: true
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/main
`
	inputs := map[string]any{
		"config": map[string]any{
			"nested": "original",
			"list":   []string{"original"},
		},
	}
	secrets := map[string]string{"TOKEN": "CANARY_SECRET"}
	replyHandles := map[string]string{"thread": "CANARY_REPLY"}
	result, err := SimulateWorkflowTrigger(WorkflowTriggerSimulationInput{
		YAML:        raw,
		WorkflowRef: "workflows/call.yml",
		Trigger:     WorkflowTriggerSelector{Kind: WorkflowTriggerWorkflowCall},
		Scenario: WorkflowTriggerSimulationScenario{
			Invocation: &WorkflowTriggerInvocation{
				Inputs:  inputs,
				Secrets: secrets,
				Session: "CANARY_SESSION",
				Delivery: Delivery{
					Channel:      "telegram",
					ReplyHandles: replyHandles,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("SimulateWorkflowTrigger() error = %v", err)
	}
	if !result.Simulation.Executable {
		t.Fatalf("simulation = %#v", result.Simulation)
	}
	inputs["config"].(map[string]any)["nested"] = "mutated"
	inputs["config"].(map[string]any)["list"].([]string)[0] = "mutated"
	secrets["TOKEN"] = "mutated"
	replyHandles["thread"] = "mutated"

	first, ok := result.RunRequest()
	if !ok ||
		first.Inputs["mode"] != "safe" ||
		first.Inputs["config"].(map[string]any)["nested"] != "original" ||
		first.Inputs["config"].(map[string]any)["list"].([]string)[0] != "original" ||
		first.Secrets["TOKEN"] != "CANARY_SECRET" ||
		first.Delivery.ReplyHandles["thread"] != "CANARY_REPLY" {
		t.Fatalf("first RunRequest() = %#v, %v", first, ok)
	}
	first.Inputs["config"].(map[string]any)["nested"] = "first"
	first.Inputs["config"].(map[string]any)["list"].([]string)[0] = "first"
	first.Secrets["TOKEN"] = "first"
	first.Delivery.ReplyHandles["thread"] = "first"
	first.Workflow.Name = "first"
	second, ok := result.RunRequest()
	if !ok ||
		second.Inputs["config"].(map[string]any)["nested"] != "original" ||
		second.Inputs["config"].(map[string]any)["list"].([]string)[0] != "original" ||
		second.Secrets["TOKEN"] != "CANARY_SECRET" ||
		second.Delivery.ReplyHandles["thread"] != "CANARY_REPLY" ||
		second.Workflow.Name != "Workflow call" {
		t.Fatalf("second RunRequest() = %#v, %v", second, ok)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, canary := range []string{
		"CANARY_SECRET",
		"CANARY_SESSION",
		"CANARY_REPLY",
		"original",
	} {
		if strings.Contains(string(encoded), canary) {
			t.Fatalf("serialized result leaked %q: %s", canary, encoded)
		}
	}

	missingSecret, err := SimulateWorkflowTrigger(WorkflowTriggerSimulationInput{
		YAML:        raw,
		WorkflowRef: "workflows/call.yml",
		Trigger:     WorkflowTriggerSelector{Kind: WorkflowTriggerWorkflowCall},
		Scenario: WorkflowTriggerSimulationScenario{
			Invocation: &WorkflowTriggerInvocation{},
		},
	})
	if err != nil {
		t.Fatalf("missing-secret simulation error = %v", err)
	}
	if missingSecret.Simulation.Matched ||
		missingSecret.Simulation.Executable ||
		missingSecret.Simulation.Reason != WorkflowTriggerSimulationInvalidScenario {
		t.Fatalf("missing-secret simulation = %#v", missingSecret.Simulation)
	}
}

func TestSimulateWorkflowTriggerIncompleteReviewCannotRun(t *testing.T) {
	t.Parallel()
	var raw strings.Builder
	raw.WriteString("name: Review limit\non:\n  manual: {}\njobs:\n")
	for index := 0; index <= MaxWorkflowInspectionJobs; index++ {
		fmt.Fprintf(
			&raw,
			"  job_%03d:\n    runs-on: picoclaw\n    steps:\n      - uses: agent/main\n",
			index,
		)
	}
	result, err := SimulateWorkflowTrigger(WorkflowTriggerSimulationInput{
		YAML:        raw.String(),
		WorkflowRef: "workflows/large.yml",
		Trigger:     WorkflowTriggerSelector{Kind: WorkflowTriggerManual},
		Scenario: WorkflowTriggerSimulationScenario{
			Invocation: &WorkflowTriggerInvocation{},
		},
	})
	if err != nil {
		t.Fatalf("SimulateWorkflowTrigger() error = %v", err)
	}
	if !result.Simulation.Matched ||
		result.Simulation.Executable ||
		result.Simulation.Reason != WorkflowTriggerSimulationReviewIncomplete ||
		result.Review.Complete {
		t.Fatalf("simulation = %#v, review = %#v", result.Simulation, result.Review)
	}
	if _, ok := result.RunRequest(); ok {
		t.Fatal("RunRequest() available for incomplete review")
	}
	if !containsWorkflowInspectionLimit(
		result.Review.Limits,
		WorkflowDefinitionInspectionLimitJobs,
	) {
		t.Fatalf("limits = %#v", result.Review.Limits)
	}
}

func TestSimulateWorkflowTriggerRejectsInvalidStrictUnion(t *testing.T) {
	t.Parallel()
	_, err := SimulateWorkflowTrigger(WorkflowTriggerSimulationInput{
		YAML:        allTriggerSimulationYAML,
		WorkflowRef: "workflows/all.yml",
		Trigger:     WorkflowTriggerSelector{Kind: WorkflowTriggerManual},
		Scenario: WorkflowTriggerSimulationScenario{
			Invocation:  &WorkflowTriggerInvocation{},
			ScheduledAt: func() *time.Time { value := time.Now(); return &value }(),
		},
	})
	if !errors.Is(err, ErrWorkflowTriggerSimulationScenario) {
		t.Fatalf("error = %v, want ErrWorkflowTriggerSimulationScenario", err)
	}
}

func containsWorkflowInspectionLimit(
	values []WorkflowDefinitionInspectionLimitCode,
	want WorkflowDefinitionInspectionLimitCode,
) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
