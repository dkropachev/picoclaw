package workflows

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const workflowEditorAllTriggersYAML = `# keep workflow root
name: All trigger editor
on:
  manual: {}
  schedule:
    - cron: "0 8 * * *" # keep schedule
  channel_message:
    channels: []
    mentioned: false
    command: ""
    conversation: {}
  command:
    name: deploy
    channels: [slack]
    args:
      force:
        type: boolean
        required: false
        default: false
    passthrough: false
    conversation: {}
  runtime_event:
    kinds: [workflow.started]
    sessions: []
  event:
    sources: github
    attributes:
      repository: acme/*
  workflow_call:
    inputs:
      count:
        type: number
        required: false
        default: 7
    secrets:
      token:
        required: false
    outputs: {}
jobs:
  main: # keep job
    runs-on: picoclaw
    steps:
      - uses: tool/message
`

const workflowEditorEditableAllTriggersYAML = `# keep workflow root
name: All editable triggers
on:
  manual: {}
  schedule:
    - cron: "0 8 * * *" # keep schedule
  channel_message:
    mentioned: false
  command:
    name: deploy
    channels: [slack]
    args:
      force:
        type: boolean
        default: false
    passthrough: false
  runtime_event:
    kinds: [workflow.started]
  event:
    sources: github
    attributes:
      repository: acme/*
  workflow_call:
    inputs:
      count:
        type: number
        default: 7
    secrets:
      token: {}
jobs:
  main: # keep job
    runs-on: picoclaw
    steps:
      - uses: tool/message
`

func TestInspectWorkflowTriggersProjectsEveryFamilyAndExplicitEmptyShapes(t *testing.T) {
	inspection := InspectWorkflowTriggers(workflowEditorAllTriggersYAML)
	if !strings.HasPrefix(inspection.Revision, "sha256:") ||
		len(inspection.Revision) != len("sha256:")+64 {
		t.Fatalf("revision = %q", inspection.Revision)
	}
	if inspection.Validation == nil || !inspection.Validation.Valid {
		t.Fatalf("validation = %#v", inspection.Validation)
	}
	if len(inspection.Triggers) != len(workflowTriggerKinds) {
		t.Fatalf("trigger count = %d, want %d", len(inspection.Triggers), len(workflowTriggerKinds))
	}
	for _, kind := range workflowTriggerKinds {
		projection, exists := inspection.Triggers[kind]
		if !exists {
			t.Errorf("missing projection %q", kind)
			continue
		}
		if !projection.Present || projection.Value == nil {
			t.Errorf("%s projection = %#v", kind, projection)
		}
	}
	for _, kind := range []WorkflowTriggerKind{
		WorkflowTriggerChannelMessage,
		WorkflowTriggerCommand,
		WorkflowTriggerRuntimeEvent,
		WorkflowTriggerWorkflowCall,
	} {
		projection := inspection.Triggers[kind]
		if projection.Editable ||
			!strings.Contains(projection.Reason, "field presence") {
			t.Errorf("%s normalization projection = %#v", kind, projection)
		}
	}
	for _, kind := range []WorkflowTriggerKind{
		WorkflowTriggerManual,
		WorkflowTriggerSchedule,
		WorkflowTriggerEvent,
	} {
		if projection := inspection.Triggers[kind]; !projection.Editable {
			t.Errorf("%s unexpectedly raw-only: %#v", kind, projection)
		}
	}
	if next := InspectWorkflowTriggers(workflowEditorAllTriggersYAML); next.Revision != inspection.Revision {
		t.Fatalf("stable revision = %q, want %q", next.Revision, inspection.Revision)
	}
	if next := InspectWorkflowTriggers(workflowEditorAllTriggersYAML + "\n"); next.Revision == inspection.Revision {
		t.Fatal("revision did not change with exact source bytes")
	}

	encoded, err := json.Marshal(inspection)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var wire struct {
		Triggers map[string]struct {
			Value any `json:"value"`
		} `json:"triggers"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	channel := wire.Triggers["channel_message"].Value.(map[string]any)
	assertWorkflowTriggerWireField(t, channel, "channels", []any{})
	assertWorkflowTriggerWireField(t, channel, "mentioned", false)
	assertWorkflowTriggerWireField(t, channel, "command", "")
	assertWorkflowTriggerWireField(t, channel, "conversation", map[string]any{})
	command := wire.Triggers["command"].Value.(map[string]any)
	assertWorkflowTriggerWireField(t, command, "conversation", map[string]any{})
	force := command["args"].(map[string]any)["force"].(map[string]any)
	assertWorkflowTriggerWireField(t, force, "required", false)
	runtimeEvent := wire.Triggers["runtime_event"].Value.(map[string]any)
	assertWorkflowTriggerWireField(t, runtimeEvent, "sessions", []any{})
	call := wire.Triggers["workflow_call"].Value.(map[string]any)
	assertWorkflowTriggerWireField(t, call, "outputs", map[string]any{})
	secret := call["secrets"].(map[string]any)["token"].(map[string]any)
	assertWorkflowTriggerWireField(t, secret, "required", false)
}

func TestRenderWorkflowTriggersAddReplaceDeleteAndNoOpAllFamilies(t *testing.T) {
	replacements := map[WorkflowTriggerKind]any{
		WorkflowTriggerManual:         map[string]any{},
		WorkflowTriggerSchedule:       []ScheduleTrigger{{Cron: "15 9 * * 1"}},
		WorkflowTriggerChannelMessage: &ChannelMessageTrigger{Channels: StringList{"discord"}},
		WorkflowTriggerCommand:        &CommandTrigger{Name: "release"},
		WorkflowTriggerRuntimeEvent:   &RuntimeEventTrigger{Kinds: StringList{"workflow.completed"}},
		WorkflowTriggerEvent:          &EventTrigger{Sources: StringList{"gmail"}},
		WorkflowTriggerWorkflowCall: &WorkflowCall{Inputs: map[string]Input{
			"target": {Type: "string", Required: true},
		}},
	}

	base := `# add root
name: Add triggers
on: {}
jobs:
  main: # add job
    runs-on: picoclaw
    steps:
      - uses: tool/message
`
	for _, kind := range workflowTriggerKinds {
		t.Run(string(kind), func(t *testing.T) {
			baseInspection := InspectWorkflowTriggers(base)
			added, addedInspection, err := RenderWorkflowTrigger(
				base,
				baseInspection.Revision,
				kind,
				replacements[kind],
			)
			if err != nil {
				t.Fatalf("add error = %v", err)
			}
			if !addedInspection.Triggers[kind].Present {
				t.Fatalf("added projection = %#v", addedInspection.Triggers[kind])
			}
			for _, comment := range []string{"# add root", "# add job"} {
				if !strings.Contains(added, comment) {
					t.Errorf("add lost comment %q:\n%s", comment, added)
				}
			}

			removed, removedInspection, err := RenderWorkflowTrigger(
				added,
				addedInspection.Revision,
				kind,
				nil,
			)
			if err != nil {
				t.Fatalf("delete error = %v", err)
			}
			if removedInspection.Triggers[kind].Present {
				t.Fatalf("deleted projection = %#v", removedInspection.Triggers[kind])
			}
			unchanged, same, err := RenderWorkflowTrigger(
				removed,
				removedInspection.Revision,
				kind,
				nil,
			)
			if err != nil || unchanged != removed || same.Revision != removedInspection.Revision {
				t.Fatalf("delete no-op = (%q, %#v, %v)", unchanged, same, err)
			}

			current := InspectWorkflowTriggers(workflowEditorEditableAllTriggersYAML)
			exact, exactInspection, err := RenderWorkflowTrigger(
				workflowEditorEditableAllTriggersYAML,
				current.Revision,
				kind,
				current.Triggers[kind].Value,
			)
			if err != nil || exact != workflowEditorEditableAllTriggersYAML ||
				exactInspection.Revision != current.Revision {
				t.Fatalf("semantic no-op changed exact bytes: error=%v\n%s", err, exact)
			}

			replaced, next, err := RenderWorkflowTrigger(
				workflowEditorEditableAllTriggersYAML,
				current.Revision,
				kind,
				replacements[kind],
			)
			if err != nil {
				t.Fatalf("replace error = %v", err)
			}
			if kind != WorkflowTriggerManual &&
				replaced == workflowEditorEditableAllTriggersYAML {
				t.Fatal("replacement unexpectedly remained an exact no-op")
			}
			for _, comment := range []string{"# keep workflow root", "# keep schedule", "# keep job"} {
				if kind == WorkflowTriggerSchedule && comment == "# keep schedule" {
					continue
				}
				if !strings.Contains(replaced, comment) {
					t.Errorf("replace lost comment %q:\n%s", comment, replaced)
				}
			}
			for _, sibling := range workflowTriggerKinds {
				if sibling != kind && !next.Triggers[sibling].Present {
					t.Errorf("replace %s removed sibling %s", kind, sibling)
				}
			}
		})
	}
}

func TestRenderWorkflowTriggerSelectedOnlyValidationAndWorkflowCallJobs(t *testing.T) {
	inspection := InspectWorkflowTriggers(workflowEditorEditableAllTriggersYAML)
	tests := []struct {
		kind  WorkflowTriggerKind
		value any
	}{
		{WorkflowTriggerManual, map[string]any{"unsupported": true}},
		{WorkflowTriggerSchedule, []ScheduleTrigger{{Cron: ""}}},
		{WorkflowTriggerChannelMessage, &ChannelMessageTrigger{TextMatches: "["}},
		{WorkflowTriggerCommand, &CommandTrigger{}},
		{WorkflowTriggerRuntimeEvent, &RuntimeEventTrigger{}},
		{WorkflowTriggerEvent, &EventTrigger{}},
		{WorkflowTriggerWorkflowCall, &WorkflowCall{Outputs: map[string]Output{
			"result": {Value: "${{ jobs.missing.outputs.result }}"},
		}}},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			rendered, _, err := RenderWorkflowTrigger(
				workflowEditorEditableAllTriggersYAML,
				inspection.Revision,
				test.kind,
				test.value,
			)
			var validation ValidationErrors
			if !errors.As(err, &validation) || len(validation) == 0 {
				t.Fatalf("error = %T %v, want ValidationErrors", err, err)
			}
			if rendered != "" {
				t.Fatalf("invalid render returned YAML:\n%s", rendered)
			}
		})
	}
	validCall := &WorkflowCall{Outputs: map[string]Output{
		"result": {Value: "${{ jobs.main }}"},
	}}
	if _, _, err := RenderWorkflowTrigger(
		workflowEditorEditableAllTriggersYAML,
		inspection.Revision,
		WorkflowTriggerWorkflowCall,
		validCall,
	); err != nil {
		t.Fatalf("workflow_call did not validate against same raw jobs: %v", err)
	}
	if rendered, _, err := RenderWorkflowTrigger(
		workflowEditorEditableAllTriggersYAML+"\n",
		inspection.Revision,
		WorkflowTriggerEvent,
		&EventTrigger{Sources: StringList{"github"}},
	); !errors.Is(err, ErrWorkflowTriggerStaleRevision) || rendered != "" {
		t.Fatalf("stale render = (%q, %v)", rendered, err)
	}
}

func TestInspectAndRenderWorkflowTriggersIsolateMalformedSiblings(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		brokenKind    WorkflowTriggerKind
		selectedKind  WorkflowTriggerKind
		replacement   any
		preservedText string
	}{
		{
			name: "wrong schedule shape does not block event",
			raw: `name: Isolated event
on:
  schedule:
    cron: "0 8 * * *"
  event:
    sources: [github]
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`,
			brokenKind:    WorkflowTriggerSchedule,
			selectedKind:  WorkflowTriggerEvent,
			replacement:   &EventTrigger{Sources: StringList{"gmail"}},
			preservedText: `cron: "0 8 * * *"`,
		},
		{
			name: "null event does not block command",
			raw: `name: Isolated command
on:
  event: null
  command:
    name: deploy
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`,
			brokenKind:    WorkflowTriggerEvent,
			selectedKind:  WorkflowTriggerCommand,
			replacement:   &CommandTrigger{Name: "release"},
			preservedText: "event: null",
		},
		{
			name: "malformed jobs do not block command",
			raw: `name: Isolated jobs
on:
  command:
    name: deploy
jobs: []
`,
			selectedKind:  WorkflowTriggerCommand,
			replacement:   &CommandTrigger{Name: "release"},
			preservedText: "jobs: []",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspection := InspectWorkflowTriggers(test.raw)
			if inspection.Validation == nil || inspection.Validation.Valid {
				t.Fatalf("validation = %#v, want invalid sibling feedback", inspection.Validation)
			}
			if test.brokenKind.Valid() {
				broken := inspection.Triggers[test.brokenKind]
				if broken.Editable || broken.Reason == "" {
					t.Fatalf("broken projection = %#v", broken)
				}
			}
			selected := inspection.Triggers[test.selectedKind]
			if !selected.Present || !selected.Editable || selected.Value == nil {
				t.Fatalf("selected projection = %#v", selected)
			}
			rendered, next, err := RenderWorkflowTrigger(
				test.raw,
				inspection.Revision,
				test.selectedKind,
				test.replacement,
			)
			if err != nil {
				t.Fatalf("RenderWorkflowTrigger() error = %v", err)
			}
			if !strings.Contains(rendered, test.preservedText) {
				t.Fatalf("rendered YAML lost malformed sibling %q:\n%s", test.preservedText, rendered)
			}
			if selected := next.Triggers[test.selectedKind]; !selected.Editable {
				t.Fatalf("rendered selected projection = %#v", selected)
			}
		})
	}
}

func TestWorkflowTriggerEditorUsesOnlyExactStringOnKey(t *testing.T) {
	tests := []struct {
		name      string
		fakeKey   string
		isFakeKey func(*yaml.Node) bool
	}{
		{
			name:    "boolean true",
			fakeKey: "true",
			isFakeKey: func(node *yaml.Node) bool {
				return node.Value == "true" && node.ShortTag() == "!!bool"
			},
		},
		{
			name:    "spaced quoted on",
			fakeKey: `" on "`,
			isFakeKey: func(node *yaml.Node) bool {
				return node.Value == " on " && node.ShortTag() == "!!str"
			},
		},
		{
			name:    "custom tagged on",
			fakeKey: "!custom on",
			isFakeKey: func(node *yaml.Node) bool {
				return node.Value == "on" && node.ShortTag() == "!custom"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := "name: Exact on\n" + test.fakeKey + ":\n" +
				"  event:\n" +
				"    sources: [github]\n" +
				"jobs: {}\n"
			inspection := InspectWorkflowTriggers(raw)
			if projection := inspection.Triggers[WorkflowTriggerEvent]; projection.Present {
				t.Fatalf("fake on key projected an event trigger: %#v", projection)
			}

			rendered, next, err := RenderWorkflowTrigger(
				raw,
				inspection.Revision,
				WorkflowTriggerEvent,
				&EventTrigger{Sources: StringList{"gmail"}},
			)
			if err != nil {
				t.Fatalf("RenderWorkflowTrigger() error = %v", err)
			}
			if projection := next.Triggers[WorkflowTriggerEvent]; !projection.Present {
				t.Fatalf("real on.event was not added: %#v", projection)
			}

			document, err := decodeWorkflowEditorDocument(rendered)
			if err != nil {
				t.Fatalf("decode rendered YAML: %v", err)
			}
			root := document.Content[0]
			onIndexes := workflowRootOnPairIndexes(root)
			if len(onIndexes) != 1 {
				t.Fatalf("real on indexes = %v, rendered:\n%s", onIndexes, rendered)
			}
			fakeIndex := -1
			for index := 0; index+1 < len(root.Content); index += 2 {
				if index != onIndexes[0] && test.isFakeKey(root.Content[index]) {
					fakeIndex = index
					break
				}
			}
			if fakeIndex < 0 {
				t.Fatalf("fake key was not preserved, rendered:\n%s", rendered)
			}
			var fakeTriggers map[string]EventTrigger
			if err := root.Content[fakeIndex+1].Decode(&fakeTriggers); err != nil {
				t.Fatalf("decode preserved fake mapping: %v", err)
			}
			if got := fakeTriggers["event"].Sources; !reflect.DeepEqual(
				got,
				StringList{"github"},
			) {
				t.Fatalf("preserved fake event sources = %#v", got)
			}
		})
	}
}

func TestInspectWorkflowTriggersPreservesPresenceAcrossGlobalRawOnlyErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "unknown sibling before known family",
			raw: `name: Unknown sibling
on:
  webhook: {}
  event:
    sources: [github]
jobs: {}
`,
		},
		{
			name: "duplicate sibling before known family",
			raw: `name: Duplicate sibling
on:
  schedule:
    - cron: "0 8 * * *"
  schedule:
    - cron: "0 9 * * *"
  event:
    sources: [github]
jobs: {}
`,
		},
		{
			name: "alias outside trigger mapping",
			raw: `name: Alias sibling
on:
  event:
    sources: [github]
shared: &job
  runs-on: picoclaw
  steps:
    - uses: tool/message
jobs:
  main: *job
`,
		},
		{
			name: "duplicate on mappings",
			raw: `name: Duplicate on
on:
  manual: {}
on:
  event:
    sources: [github]
jobs: {}
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspection := InspectWorkflowTriggers(test.raw)
			projection := inspection.Triggers[WorkflowTriggerEvent]
			if !projection.Present || projection.Editable || projection.Reason == "" {
				t.Fatalf("event projection = %#v", projection)
			}
			event, ok := projection.Value.(*EventTrigger)
			if !ok || event == nil ||
				!reflect.DeepEqual(event.Sources, StringList{"github"}) {
				t.Fatalf("event value = %#v", projection.Value)
			}
			rendered, _, err := RenderWorkflowTrigger(
				test.raw,
				inspection.Revision,
				WorkflowTriggerEvent,
				&EventTrigger{Sources: StringList{"gmail"}},
			)
			if !errors.Is(err, ErrWorkflowTriggerNotEditable) || rendered != "" {
				t.Fatalf("raw-only render = (%q, %v)", rendered, err)
			}
		})
	}
}

func TestRenderWorkflowCallDecodesSnapshotJobsIndependently(t *testing.T) {
	raw := `name: Isolated workflow call
on:
  schedule:
    cron: "0 8 * * *"
  workflow_call: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`
	inspection := InspectWorkflowTriggers(raw)
	selected := inspection.Triggers[WorkflowTriggerWorkflowCall]
	if !selected.Editable {
		t.Fatalf("workflow_call projection = %#v", selected)
	}
	replacement := &WorkflowCall{Outputs: map[string]Output{
		"result": {Value: "${{ jobs.main }}"},
	}}
	if _, _, err := RenderWorkflowTrigger(
		raw,
		inspection.Revision,
		WorkflowTriggerWorkflowCall,
		replacement,
	); err != nil {
		t.Fatalf("workflow_call did not use independently decoded jobs: %v", err)
	}

	malformedJobs := `name: Malformed jobs
on:
  workflow_call: {}
jobs: []
`
	malformedInspection := InspectWorkflowTriggers(malformedJobs)
	if selected := malformedInspection.Triggers[WorkflowTriggerWorkflowCall]; !selected.Editable {
		t.Fatalf("malformed jobs disabled workflow_call inspection: %#v", selected)
	}
	if _, _, err := RenderWorkflowTrigger(
		malformedJobs,
		malformedInspection.Revision,
		WorkflowTriggerWorkflowCall,
		replacement,
	); !errors.Is(err, ErrWorkflowTriggerValue) {
		t.Fatalf("malformed jobs render error = %v, want ErrWorkflowTriggerValue", err)
	}

	duplicateJobs := `name: Duplicate jobs
on:
  workflow_call: {}
jobs:
  first:
    runs-on: picoclaw
    steps:
      - uses: tool/message
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`
	duplicateInspection := InspectWorkflowTriggers(duplicateJobs)
	if selected := duplicateInspection.Triggers[WorkflowTriggerWorkflowCall]; !selected.Editable {
		t.Fatalf("duplicate jobs disabled workflow_call inspection: %#v", selected)
	}
	if _, _, err := RenderWorkflowTrigger(
		duplicateJobs,
		duplicateInspection.Revision,
		WorkflowTriggerWorkflowCall,
		replacement,
	); !errors.Is(err, ErrWorkflowTriggerValue) {
		t.Fatalf("duplicate jobs render error = %v, want ErrWorkflowTriggerValue", err)
	}
}

func TestInspectWorkflowTriggersRejectsUnsafeASTWithoutNormalizing(t *testing.T) {
	tests := []struct {
		name string
		kind WorkflowTriggerKind
		raw  string
	}{
		{"alias", WorkflowTriggerEvent, "shared: &x {sources: [github]}\non: {event: *x}\njobs: {}\n"},
		{"merge", WorkflowTriggerEvent, "shared: &x {sources: [github]}\non:\n  event:\n    <<: *x\njobs: {}\n"},
		{"multiple documents", WorkflowTriggerEvent, "on: {event: {sources: [github]}}\njobs: {}\n---\njobs: {}\n"},
		{"unknown trigger", WorkflowTriggerEvent, "on: {webhook: {}}\njobs: {}\n"},
		{
			"duplicate trigger",
			WorkflowTriggerEvent,
			"on:\n  event: {sources: [github]}\n  event: {sources: [gmail]}\njobs: {}\n",
		},
		{"unknown nested field", WorkflowTriggerEvent, "on: {event: {sources: [github], hidden: true}}\njobs: {}\n"},
		{
			"duplicate nested field",
			WorkflowTriggerEvent,
			"on:\n  event:\n    sources: [github]\n    sources: [gmail]\njobs: {}\n",
		},
		{"null trigger", WorkflowTriggerEvent, "on: {event: null}\njobs: {}\n"},
		{"wrong node kind", WorkflowTriggerEvent, "on: {event: [github]}\njobs: {}\n"},
		{"unsafe tag", WorkflowTriggerEvent, "on: {event: {sources: !unsafe github}}\njobs: {}\n"},
		{"non string key", WorkflowTriggerEvent, "on:\n  event:\n    ? [bad]\n    : github\njobs: {}\n"},
		{"multiline value", WorkflowTriggerEvent, "on: {event: {sources: [\"github\\ncorp\"]}}\njobs: {}\n"},
		{"normalized list", WorkflowTriggerEvent, "on: {event: {sources: [\" github \"]}}\njobs: {}\n"},
		{
			"unsafe integer default",
			WorkflowTriggerCommand,
			"on:\n  command:\n    name: run\n    args:\n      count: {type: number, default: 9007199254740992}\njobs: {}\n",
		},
		{
			"unsafe negative integer default",
			WorkflowTriggerCommand,
			"on:\n  command:\n    name: run\n    args:\n      count: {type: number, default: -9007199254740992}\njobs: {}\n",
		},
		{
			"unsafe decimal integer default",
			WorkflowTriggerCommand,
			"on:\n  command:\n    name: run\n    args:\n      count: {type: number, default: 9007199254740993.0}\njobs: {}\n",
		},
		{
			"unsafe exponent integer default",
			WorkflowTriggerCommand,
			"on:\n  command:\n    name: run\n    args:\n      count: {type: number, default: 9007199254740993e0}\njobs: {}\n",
		},
		{
			"top null default",
			WorkflowTriggerCommand,
			"on:\n  command:\n    name: run\n    args:\n      count: {type: number, default: null}\njobs: {}\n",
		},
		{
			"timestamp default",
			WorkflowTriggerCommand,
			"on:\n  command:\n    name: run\n    args:\n      date: {type: string, default: 2026-07-29}\njobs: {}\n",
		},
		{
			"hex default",
			WorkflowTriggerCommand,
			"on:\n  command:\n    name: run\n    args:\n      count: {type: number, default: 0x10}\njobs: {}\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspection := InspectWorkflowTriggers(test.raw)
			projection := inspection.Triggers[test.kind]
			if projection.Editable || projection.Reason == "" {
				t.Fatalf("projection = %#v", projection)
			}
			rendered, _, err := RenderWorkflowTrigger(
				test.raw,
				inspection.Revision,
				test.kind,
				nil,
			)
			if !errors.Is(err, ErrWorkflowTriggerNotEditable) || rendered != "" {
				t.Fatalf("render = (%q, %v), want raw-only", rendered, err)
			}
		})
	}
}

func TestWorkflowJSONNumberIsBrowserSafeUsesExactDecimalRoundTrip(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"0.1", true},
		{"9007199254740991", true},
		{"9007199254740991.0", true},
		{"9007199254740991e0", true},
		{"9007199254740992", false},
		{"9007199254740993.0", false},
		{"9007199254740993e0", false},
		{"0.10000000000000001", false},
		{"1e400", false},
		{"1e999999999", false},
		{"1e-999999999", false},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			if got := WorkflowJSONNumberIsBrowserSafe(test.value); got != test.want {
				t.Fatalf(
					"WorkflowJSONNumberIsBrowserSafe(%q) = %t, want %t",
					test.value,
					got,
					test.want,
				)
			}
		})
	}
}

func TestWorkflowTriggerSemanticEqualityPreservesNilEmptyAndNormalizesNumbers(t *testing.T) {
	if workflowTriggerValuesEqual([]string(nil), []string{}) {
		t.Fatal("nil and empty slices compared equal")
	}
	if workflowTriggerValuesEqual(map[string]any(nil), map[string]any{}) {
		t.Fatal("nil and empty maps compared equal")
	}
	if !workflowTriggerValuesEqual(
		map[string]any{"count": int64(7)},
		map[string]any{"count": float64(7)},
	) {
		t.Fatal("equal safe numeric representations compared different")
	}

	inspection := InspectWorkflowTriggers(workflowEditorEditableAllTriggersYAML)
	call := cloneWorkflowEditorValue(
		inspection.Triggers[WorkflowTriggerWorkflowCall].Value,
	).(*WorkflowCall)
	count := call.Inputs["count"]
	count.Default = float64(7)
	call.Inputs["count"] = count
	rendered, _, err := RenderWorkflowTrigger(
		workflowEditorEditableAllTriggersYAML,
		inspection.Revision,
		WorkflowTriggerWorkflowCall,
		call,
	)
	if err != nil || rendered != workflowEditorEditableAllTriggersYAML {
		t.Fatalf("numeric no-op = (%q, %v)", rendered, err)
	}
}

func TestRenderWorkflowTriggerPostconditionNeverReturnsMutatedYAML(t *testing.T) {
	inspection := InspectWorkflowTriggers(workflowEditorEditableAllTriggersYAML)
	replacement := &CommandTrigger{
		Name: "changed",
		Args: map[string]Input{
			"value": {
				Type:    "string",
				Default: json.Number("7"),
			},
		},
	}
	rendered, returned, err := RenderWorkflowTrigger(
		workflowEditorEditableAllTriggersYAML,
		inspection.Revision,
		WorkflowTriggerCommand,
		replacement,
	)
	if !errors.Is(err, ErrWorkflowTriggerValue) {
		t.Fatalf("error = %v, want postcondition failure", err)
	}
	if rendered != "" {
		t.Fatalf("failed postcondition exposed mutated YAML:\n%s", rendered)
	}
	if returned.Revision != inspection.Revision ||
		!reflect.DeepEqual(
			returned.Triggers[WorkflowTriggerCommand].Value,
			inspection.Triggers[WorkflowTriggerCommand].Value,
		) {
		t.Fatalf("failure inspection was not the original snapshot: %#v", returned)
	}
}

func assertWorkflowTriggerWireField(
	t *testing.T,
	object map[string]any,
	key string,
	want any,
) {
	t.Helper()
	got, exists := object[key]
	if !exists || !reflect.DeepEqual(got, want) {
		t.Fatalf("%q = %#v (present %t), want %#v", key, got, exists, want)
	}
}
