package workflows

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
)

func TestListLocalWorkflowsReturnsCanonicalRefs(t *testing.T) {
	workspace := t.TempDir()
	writeWorkflowFile(t, workspace, "one.yml", `
name: One
on:
  manual: {}
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	writeWorkflowFile(t, workspace, filepath.Join("nested", "two.yaml"), `
name: Two
on:
  manual: {}
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	if err := os.WriteFile(filepath.Join(workspace, "workflows", "ignore.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := ListLocal(context.Background(), workspace)
	if err != nil {
		t.Fatalf("ListLocal failed: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("defs len = %d, want 2: %#v", len(defs), defs)
	}
	if defs[0].Ref != "workflows/nested/two.yaml" || defs[1].Ref != "workflows/one.yml" {
		t.Fatalf("refs = %#v, want sorted canonical refs", defs)
	}
}

func TestListLoadAndRunUseConfiguredDefinitionsDir(t *testing.T) {
	workspace := t.TempDir()
	definitionsDir := "custom-definitions"
	writeWorkflowFileUnder(t, workspace, definitionsDir, "native.yml", `
name: Native
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with:
          action: set
          key: configured_dir
          value: ok
`)
	writeWorkflowFile(t, workspace, "stale.yml", `
name: Stale
on:
  manual: {}
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)

	opts := []LocalOption{WithDefinitionsDir(definitionsDir)}
	defs, err := ListLocal(context.Background(), workspace, opts...)
	if err != nil {
		t.Fatalf("ListLocal failed: %v", err)
	}
	if len(defs) != 1 || defs[0].Ref != "workflows/native.yml" {
		t.Fatalf("defs = %#v, want configured workflow only", defs)
	}
	if _, loadErr := LoadLocal(context.Background(), workspace, "workflows/native.yml", opts...); loadErr != nil {
		t.Fatalf("LoadLocal with configured definitions dir failed: %v", loadErr)
	}
	result, err := (&Executor{
		WorkspaceDir:   workspace,
		DefinitionsDir: definitionsDir,
	}).Run(context.Background(), RunRequest{Ref: "workflows/native.yml"})
	if err != nil {
		t.Fatalf("Executor.Run with configured definitions dir failed: %v", err)
	}
	if result.Status != RunStatusSucceeded {
		t.Fatalf("run status = %q, want succeeded", result.Status)
	}
}

func TestMatchChannelMessageBuildsSessionDeliveryAndEvent(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Chat
on:
  channel_message:
    channels: telegram
    chats: ["-1001"]
    senders: ["alice"]
    text_matches: "^/ask"
    conversation:
      session: discussion
      delivery: same_discussion
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	match, ok, err := MatchChannelMessage(workflow, "workflows/chat.yml", ChannelMessageEvent{
		Channel:          "telegram",
		ChatID:           "-1001",
		TopicID:          "42",
		SenderID:         "alice",
		MessageID:        "101",
		ReplyToMessageID: "",
		Text:             "/ask hello",
		Mentioned:        true,
		Raw:              map[string]string{"platform": "telegram"},
	})
	if err != nil {
		t.Fatalf("MatchChannelMessage failed: %v", err)
	}
	if !ok {
		t.Fatal("workflow did not match")
	}
	if got := match.Session; got != "workflow:workflows/chat.yml:discussion:telegram:-1001:topic:42" {
		t.Fatalf("session = %q, want discussion session", got)
	}
	if got := match.Delivery.ReplyToMessageID; got != "101" {
		t.Fatalf("reply target = %q, want current message id", got)
	}
	message, ok := match.Event["message"].(map[string]any)
	if !ok || message["text"] != "/ask hello" {
		t.Fatalf("message event = %#v", match.Event["message"])
	}
}

func TestMatchChannelMessageHonorsPassthrough(t *testing.T) {
	passthrough := true
	workflow := &Workflow{
		On: WorkflowTriggers{
			ChannelMessage: &ChannelMessageTrigger{
				Passthrough: &passthrough,
			},
		},
		Jobs: map[string]Job{"noop": {RunsOn: "picoclaw", Steps: []Step{{Uses: "tool/message"}}}},
	}
	match, ok, err := MatchChannelMessage(workflow, "workflows/chat.yml", ChannelMessageEvent{Text: "hello"})
	if err != nil {
		t.Fatalf("MatchChannelMessage failed: %v", err)
	}
	if !ok || !match.Passthrough {
		t.Fatalf("match = %#v ok=%v, want passthrough", match, ok)
	}
}

func TestMatchCommandMessageBuildsInputsAndDelivery(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Command
on:
  command:
    name: summarize
    channels: slack
    args:
      topic:
        type: string
        required: true
      tone:
        type: string
        default: short
    conversation:
      session: sender
      delivery: same_discussion
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	match, ok, err := MatchCommandMessage(workflow, "workflows/command.yml", ChannelMessageEvent{
		Channel:   "slack",
		ChatID:    "C123",
		TopicID:   "171234.1",
		SenderID:  "U123",
		MessageID: "m1",
		Text:      "/summarize --topic workflows",
	})
	if err != nil {
		t.Fatalf("MatchCommandMessage failed: %v", err)
	}
	if !ok {
		t.Fatal("command workflow did not match")
	}
	if match.Inputs["topic"] != "workflows" || match.Inputs["tone"] != "short" {
		t.Fatalf("inputs = %#v, want parsed topic and default tone", match.Inputs)
	}
	if match.Session != "workflow:workflows/command.yml:sender:slack:U123" {
		t.Fatalf("session = %q", match.Session)
	}
	if match.Delivery.ThreadTS != "171234.1" {
		t.Fatalf("thread_ts = %q, want 171234.1", match.Delivery.ThreadTS)
	}
}

func TestMatchCommandMessageBindsPositionalArgsDeterministically(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Command Positional
on:
  command:
    name: deploy
    args:
      second:
        type: string
      first:
        type: string
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	match, ok, err := MatchCommandMessage(workflow, "workflows/deploy.yml", ChannelMessageEvent{
		Text: "/deploy alpha beta",
	})
	if err != nil {
		t.Fatalf("MatchCommandMessage() error = %v", err)
	}
	if !ok {
		t.Fatal("command workflow did not match")
	}
	if match.Inputs["first"] != "alpha" || match.Inputs["second"] != "beta" {
		t.Fatalf("inputs = %#v, want sorted positional binding", match.Inputs)
	}
}

func TestMatchRuntimeEventBuildsInputsSessionAndDelivery(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Runtime
on:
  runtime_event:
    kinds: agent.turn.end
    sources: agent/main
    agents: main
    channels: telegram
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	evt := runtimeevents.Event{
		ID:   "evt-1",
		Kind: runtimeevents.KindAgentTurnEnd,
		Time: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
		Source: runtimeevents.Source{
			Component: "agent",
			Name:      "main",
		},
		Scope: runtimeevents.Scope{
			AgentID:    "main",
			SessionKey: "agent:main:telegram:chat1",
			Channel:    "telegram",
			ChatID:     "chat1",
			TopicID:    "42",
			MessageID:  "m1",
		},
		Correlation: runtimeevents.Correlation{ReplyToID: "root"},
		Payload:     map[string]any{"ok": true},
	}
	match, ok, err := MatchRuntimeEvent(workflow, "workflows/runtime.yml", evt)
	if err != nil {
		t.Fatalf("MatchRuntimeEvent failed: %v", err)
	}
	if !ok {
		t.Fatal("runtime event workflow did not match")
	}
	if match.Inputs["kind"] != runtimeevents.KindAgentTurnEnd.String() {
		t.Fatalf("inputs = %#v", match.Inputs)
	}
	if match.Session != "workflow:workflows/runtime.yml:runtime:agent:main:telegram:chat1" {
		t.Fatalf("session = %q", match.Session)
	}
	if match.Delivery.ReplyToMessageID != "root" || match.Delivery.TopicID != "42" {
		t.Fatalf("delivery = %#v", match.Delivery)
	}
}

func TestMatchRuntimeEventRejectsWorkflowTriggeredFeedbackEvent(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Runtime
on:
  runtime_event:
    kinds: workflow.triggered
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	match, ok, err := MatchRuntimeEvent(workflow, "workflows/runtime.yml", runtimeevents.Event{
		Kind: runtimeevents.KindWorkflowTriggered,
		Source: runtimeevents.Source{
			Component: "workflow",
			Name:      "workflows/runtime.yml",
		},
	})
	if err != nil {
		t.Fatalf("MatchRuntimeEvent() error = %v", err)
	}
	if ok || match != nil {
		t.Fatalf("MatchRuntimeEvent() = %#v, %v, want no match for workflow feedback event", match, ok)
	}
}

func TestMatchRuntimeEventRejectsOwnWorkflowLifecycleEvent(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Runtime
on:
  runtime_event:
    kinds: workflow.run.start
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	match, ok, err := MatchRuntimeEvent(workflow, "workflows/runtime.yml", runtimeevents.Event{
		Kind:   runtimeevents.KindWorkflowRunStart,
		Source: runtimeevents.Source{Component: "workflow", Name: "workflows/runtime.yml"},
	})
	if err != nil {
		t.Fatalf("MatchRuntimeEvent() error = %v", err)
	}
	if ok || match != nil {
		t.Fatalf("MatchRuntimeEvent() = %#v, %v, want no match for own workflow lifecycle event", match, ok)
	}

	match, ok, err = MatchRuntimeEvent(workflow, "workflows/runtime.yml", runtimeevents.Event{
		Kind:   runtimeevents.KindWorkflowRunStart,
		Source: runtimeevents.Source{Component: "workflow", Name: "workflows/producer.yml"},
	})
	if err != nil {
		t.Fatalf("MatchRuntimeEvent() error = %v", err)
	}
	if !ok || match == nil {
		t.Fatalf("MatchRuntimeEvent() = %#v, %v, want match for another workflow lifecycle event", match, ok)
	}
}

func writeWorkflowFileUnder(t *testing.T, workspace, definitionsDir, name, content string) {
	t.Helper()
	dir := filepath.Join(workspace, definitionsDir, filepath.Dir(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, definitionsDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
