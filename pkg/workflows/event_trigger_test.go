package workflows

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestParseEventTriggerAcceptsScalarListAndNestedFilters(t *testing.T) {
	workflow := parseWorkflow(t, `
name: External event
on:
  event:
    sources: GitHub
    connectors: [primary, backup]
    types:
      - pull_request.opened
      - pull_request.*
    actor:
      ids: dependabot[bot]
      types: [bot, service]
      attributes:
        role: [automation, integration]
    subject:
      types: repository
      attributes:
        repository: acme/*
    attributes:
      installation: [production, staging]
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)

	trigger := workflow.On.Event
	if trigger == nil {
		t.Fatal("event trigger is nil")
	}
	if !reflect.DeepEqual(trigger.Sources, StringList{"GitHub"}) {
		t.Fatalf("sources = %#v", trigger.Sources)
	}
	if !reflect.DeepEqual(trigger.Connectors, StringList{"primary", "backup"}) {
		t.Fatalf("connectors = %#v", trigger.Connectors)
	}
	if !reflect.DeepEqual(
		trigger.Types,
		StringList{"pull_request.opened", "pull_request.*"},
	) {
		t.Fatalf("types = %#v", trigger.Types)
	}
	if trigger.Actor == nil ||
		!reflect.DeepEqual(trigger.Actor.IDs, StringList{"dependabot[bot]"}) ||
		!reflect.DeepEqual(trigger.Actor.Attributes["role"], StringList{"automation", "integration"}) {
		t.Fatalf("actor = %#v", trigger.Actor)
	}
	if trigger.Subject == nil ||
		!reflect.DeepEqual(trigger.Subject.Types, StringList{"repository"}) ||
		!reflect.DeepEqual(trigger.Subject.Attributes["repository"], StringList{"acme/*"}) {
		t.Fatalf("subject = %#v", trigger.Subject)
	}
	if !reflect.DeepEqual(trigger.Attributes["installation"], StringList{"production", "staging"}) {
		t.Fatalf("attributes = %#v", trigger.Attributes)
	}
	if err := Validate(workflow); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestEventTriggerJSONKeyAndRoundTrip(t *testing.T) {
	input := []byte(`{
		"name": "JSON event",
		"on": {
			"event": {
				"sources": ["github"],
				"actor": {"types": ["bot"]}
			}
		},
		"jobs": {
			"main": {
				"runs-on": "picoclaw",
				"steps": [{"uses": "tool/message"}]
			}
		}
	}`)
	var workflow Workflow
	if err := json.Unmarshal(input, &workflow); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if workflow.On.Event == nil ||
		!reflect.DeepEqual(workflow.On.Event.Sources, StringList{"github"}) ||
		workflow.On.Event.Actor == nil ||
		!reflect.DeepEqual(workflow.On.Event.Actor.Types, StringList{"bot"}) {
		t.Fatalf("event trigger = %#v", workflow.On.Event)
	}
	if err := Validate(&workflow); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	encoded, err := json.Marshal(&workflow)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"event"`) {
		t.Fatalf("encoded workflow does not contain event key: %s", encoded)
	}
}

func TestValidateEventTriggerRequiresEffectiveFilter(t *testing.T) {
	tests := []struct {
		name    string
		trigger *EventTrigger
		want    []string
	}{
		{
			name:    "no filters",
			trigger: &EventTrigger{},
			want:    []string{"on.event: at least one filter is required"},
		},
		{
			name:    "explicit empty list",
			trigger: &EventTrigger{Sources: StringList{}},
			want: []string{
				"on.event.sources: at least one pattern is required",
				"on.event: at least one filter is required",
			},
		},
		{
			name:    "blank pattern",
			trigger: &EventTrigger{Sources: StringList{"github", "  "}},
			want:    []string{"on.event.sources[1]: pattern is required"},
		},
		{
			name:    "empty attributes",
			trigger: &EventTrigger{Attributes: map[string]StringList{}},
			want: []string{
				"on.event.attributes: at least one attribute filter is required",
				"on.event: at least one filter is required",
			},
		},
		{
			name: "empty attribute value",
			trigger: &EventTrigger{Attributes: map[string]StringList{
				"repository": {},
			}},
			want: []string{
				"on.event.attributes.repository: at least one pattern is required",
				"on.event: at least one filter is required",
			},
		},
		{
			name: "empty attribute name",
			trigger: &EventTrigger{Attributes: map[string]StringList{
				"": {"value"},
			}},
			want: []string{
				"on.event.attributes: attribute name is required",
				"on.event: at least one filter is required",
			},
		},
		{
			name: "empty actor",
			trigger: &EventTrigger{
				Sources: StringList{"github"},
				Actor:   &EventEntityTrigger{},
			},
			want: []string{"on.event.actor: at least one entity filter is required"},
		},
		{
			name: "empty entity list",
			trigger: &EventTrigger{
				Subject: &EventEntityTrigger{IDs: StringList{}},
			},
			want: []string{
				"on.event.subject.ids: at least one pattern is required",
				"on.event.subject: at least one entity filter is required",
				"on.event: at least one filter is required",
			},
		},
		{
			name:    "explicit catch all",
			trigger: &EventTrigger{Types: StringList{"*"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Validate(eventTriggerTestWorkflow(test.trigger))
			if len(test.want) == 0 {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() succeeded, want error")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Validate() error = %q, want substring %q", err, want)
				}
			}
		})
	}
}

func TestParseEventTriggerPreservesInvalidEmptyFiltersForValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		path string
	}{
		{
			name: "empty top level list",
			body: "sources: []",
			path: "on.event.sources: at least one pattern is required",
		},
		{
			name: "blank top level scalar",
			body: `sources: ""`,
			path: "on.event.sources[0]: pattern is required",
		},
		{
			name: "blank list item",
			body: "sources: [github, \"\"]",
			path: "on.event.sources[1]: pattern is required",
		},
		{
			name: "empty nested list",
			body: "actor:\n      types: []",
			path: "on.event.actor.types: at least one pattern is required",
		},
		{
			name: "empty attribute value",
			body: "attributes:\n      repository: []",
			path: "on.event.attributes.repository: at least one pattern is required",
		},
		{
			name: "empty attribute map",
			body: "attributes: {}",
			path: "on.event.attributes: at least one attribute filter is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workflow := parseWorkflow(t, `
name: Invalid event
on:
  event:
    `+test.body+`
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
			err := Validate(workflow)
			if err == nil || !strings.Contains(err.Error(), test.path) {
				t.Fatalf("Validate() error = %v, want %q", err, test.path)
			}
		})
	}
}

func TestParseEventTriggerPreservesEmptyFiltersFromYAMLMerge(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Merged event filters
event_defaults: &event_defaults
  sources: []
on:
  event:
    <<: *event_defaults
    types: "*"
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	if workflow.On.Event == nil || workflow.On.Event.Sources == nil {
		t.Fatalf("merged event sources were not preserved: %#v", workflow.On.Event)
	}
	err := Validate(workflow)
	if err == nil || !strings.Contains(
		err.Error(),
		"on.event.sources: at least one pattern is required",
	) {
		t.Fatalf("Validate() error = %v, want merged empty source filter error", err)
	}
}

func TestParseEventTriggerRejectsInvalidShapes(t *testing.T) {
	tests := []string{
		"event: []",
		"event:\n    sources: {not: a-list}",
		"event:\n    actor: nope",
		"event:\n    attributes: []",
		"event:\n    subject:\n      attributes:\n        repository: {bad: value}",
	}
	for _, trigger := range tests {
		_, err := Parse([]byte("on:\n  " + trigger + "\njobs: {}\n"))
		if err == nil {
			t.Errorf("Parse() succeeded for:\n%s", trigger)
		}
	}
}

func TestParseEventTriggerRejectsUnknownFieldsIncludingMerges(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "top-level typo",
			yaml: `
on:
  event:
    types: "*"
    source: github
jobs: {}
`,
			want: `event trigger has unknown field "source"`,
		},
		{
			name: "entity typo",
			yaml: `
on:
  event:
    actor:
      types: bot
      id: dependabot
jobs: {}
`,
			want: `event entity trigger has unknown field "id"`,
		},
		{
			name: "whitespace-surrounded field",
			yaml: `
on:
  event:
    types: "*"
    " sources ": github
jobs: {}
`,
			want: `event trigger has unknown field " sources "`,
		},
		{
			name: "unknown merged field",
			yaml: `
event_defaults: &event_defaults
  types: "*"
  source: github
on:
  event:
    <<: *event_defaults
jobs: {}
`,
			want: `event trigger has unknown field "source"`,
		},
		{
			name: "quoted merge key is an unknown top-level field",
			yaml: `
on:
  event:
    types: "*"
    "<<":
      types: ignored.*
jobs: {}
`,
			want: `event trigger has unknown field "<<"`,
		},
		{
			name: "quoted merge key is an unknown entity field",
			yaml: `
on:
  event:
    actor:
      types: bot
      "<<":
        ids: ignored
jobs: {}
`,
			want: `event entity trigger has unknown field "<<"`,
		},
		{
			name: "merge tag on another key is unknown",
			yaml: `
on:
  event:
    types: "*"
    !!merge ignored:
      types: ignored.*
jobs: {}
`,
			want: `event trigger has unknown field "ignored"`,
		},
		{
			name: "alias resolving to merge spelling is unknown",
			yaml: `
merge_spelling: &merge_spelling "<<"
on:
  event:
    types: "*"
    ? *merge_spelling
    : {types: ignored.*}
jobs: {}
`,
			want: `event trigger has unknown field "<<"`,
		},
		{
			name: "explicit null event trigger",
			yaml: `
on:
  event: null
jobs: {}
`,
			want: `event trigger cannot be null`,
		},
		{
			name: "bare null event trigger",
			yaml: `
on:
  event:
jobs: {}
`,
			want: `event trigger cannot be null`,
		},
		{
			name: "merged null event trigger",
			yaml: `
trigger_defaults: &trigger_defaults
  event: null
on:
  <<: *trigger_defaults
jobs: {}
`,
			want: `event trigger cannot be null`,
		},
		{
			name: "null actor filter",
			yaml: `
on:
  event:
    types: "*"
    actor: null
jobs: {}
`,
			want: `event trigger field "actor" cannot be null`,
		},
		{
			name: "null top-level attributes",
			yaml: `
on:
  event:
    types: "*"
    attributes: null
jobs: {}
`,
			want: `event trigger field "attributes" cannot be null`,
		},
		{
			name: "null entity attributes",
			yaml: `
on:
  event:
    actor:
      types: bot
      attributes: null
jobs: {}
`,
			want: `event entity trigger field "attributes" cannot be null`,
		},
		{
			name: "null list filter",
			yaml: `
on:
  event:
    types: null
jobs: {}
`,
			want: `event trigger field "types" cannot be null`,
		},
		{
			name: "null top-level attribute patterns",
			yaml: `
on:
  event:
    types: "*"
    attributes:
      repository: null
jobs: {}
`,
			want: `event trigger attribute "repository" cannot be null`,
		},
		{
			name: "null entity attribute patterns",
			yaml: `
on:
  event:
    actor:
      types: bot
      attributes:
        role: null
jobs: {}
`,
			want: `event entity trigger attribute "role" cannot be null`,
		},
		{
			name: "merged null attribute patterns",
			yaml: `
event_defaults: &event_defaults
  attributes:
    repository: null
on:
  event:
    <<: *event_defaults
    types: "*"
jobs: {}
`,
			want: `event trigger attribute "repository" cannot be null`,
		},
		{
			name: "attribute-map merge with null patterns",
			yaml: `
attribute_defaults: &attribute_defaults
  repository: null
on:
  event:
    types: "*"
    attributes:
      <<: *attribute_defaults
jobs: {}
`,
			want: `event trigger attribute "repository" cannot be null`,
		},
		{
			name: "entity attribute-map merge with null patterns",
			yaml: `
attribute_defaults: &attribute_defaults
  role: null
on:
  event:
    actor:
      types: bot
      attributes:
        <<: *attribute_defaults
jobs: {}
`,
			want: `event entity trigger attribute "role" cannot be null`,
		},
		{
			name: "cyclic event merge",
			yaml: `
event_defaults: &event_defaults
  <<: *event_defaults
  types: "*"
on:
  event:
    <<: *event_defaults
jobs: {}
`,
			want: `cyclic YAML merge`,
		},
		{
			name: "cyclic triggers merge",
			yaml: `
on: &triggers
  <<: *triggers
  event:
    types: "*"
jobs: {}
`,
			want: `cyclic YAML merge`,
		},
		{
			name: "null attribute name",
			yaml: `
on:
  event:
    types: "*"
    attributes:
      null: present
jobs: {}
`,
			want: `event trigger attribute name must be a string`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMatchEventTriggerCombinesFieldsAndAlternatives(t *testing.T) {
	trigger := &EventTrigger{
		Sources:    StringList{"gitlab", "git*"},
		Connectors: StringList{"PRIMARY"},
		Types:      StringList{"issues.*", "pull_request.?pened"},
		Actor: &EventEntityTrigger{
			IDs:   StringList{"dependabot[bot]"},
			Types: StringList{"BOT"},
			Attributes: map[string]StringList{
				"role": {"auto*"},
			},
		},
		Subject: &EventEntityTrigger{
			IDs:   StringList{"repo-*"},
			Types: StringList{"REPOSITORY"},
			Attributes: map[string]StringList{
				"repository": {"acme/?ico*"},
			},
		},
		Attributes: map[string]StringList{
			"installation": {"prod*"},
		},
	}
	event := eventing.Envelope{
		Source:    "GitHub",
		Connector: "primary",
		Type:      "Pull_Request.Opened",
		Actor: &eventing.Actor{
			ID:         "dependabot[bot]",
			Type:       "bot",
			Attributes: map[string]string{"role": "automation"},
		},
		Subject: &eventing.Subject{
			ID:         "repo-42",
			Type:       "repository",
			Attributes: map[string]string{"repository": "acme/picoclaw"},
		},
		Attributes: map[string]string{"installation": "production"},
	}
	if !MatchEventTrigger(trigger, event) {
		t.Fatal("MatchEventTrigger() = false, want true")
	}
	if !WorkflowMatchesEvent(&Workflow{On: WorkflowTriggers{Event: trigger}}, event) {
		t.Fatal("WorkflowMatchesEvent() = false, want true")
	}
}

func TestEvaluateEventTriggerReturnsDeterministicRuntimeChecks(t *testing.T) {
	trigger := &EventTrigger{
		Sources:    StringList{"git*"},
		Connectors: StringList{"PRIMARY"},
		Types:      StringList{"issues.*"},
		Actor: &EventEntityTrigger{
			IDs:   StringList{"actor-*"},
			Types: StringList{"BOT"},
			Attributes: map[string]StringList{
				"zeta":  {"last"},
				"alpha": {"first"},
			},
		},
		Subject: &EventEntityTrigger{
			Types: StringList{"repository"},
		},
		Attributes: map[string]StringList{
			"region":       {"us-*"},
			"installation": {"prod"},
		},
	}
	event := eventing.Envelope{
		Source:    "GitHub",
		Connector: "primary",
		Type:      "Issues.Opened",
		Actor: &eventing.Actor{
			ID:   "actor-1",
			Type: "bot",
			Attributes: map[string]string{
				"alpha": "first",
				"zeta":  "last",
			},
		},
		Subject: &eventing.Subject{Type: "Repository"},
		Attributes: map[string]string{
			"installation": "prod",
			"region":       "us-east",
		},
	}

	result, err := EvaluateEventTrigger(trigger, event)
	if err != nil {
		t.Fatalf("EvaluateEventTrigger() error = %v", err)
	}
	if !result.Matched || !MatchEventTrigger(trigger, event) {
		t.Fatalf("match result = %#v, want runtime match", result)
	}
	gotPaths := make([]string, 0, len(result.Checks))
	for _, check := range result.Checks {
		gotPaths = append(gotPaths, check.Path)
		if !check.Present || !check.Matched {
			t.Fatalf("check = %#v, want present match", check)
		}
	}
	wantPaths := []string{
		"on.event.actor.attributes.alpha",
		"on.event.actor.attributes.zeta",
		"on.event.actor.ids",
		"on.event.actor.types",
		"on.event.attributes.installation",
		"on.event.attributes.region",
		"on.event.connectors",
		"on.event.sources",
		"on.event.subject.types",
		"on.event.types",
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("check paths = %#v, want %#v", gotPaths, wantPaths)
	}
}

func TestEvaluateEventTriggerExplainsMissingAndNonMatchingFields(t *testing.T) {
	trigger := &EventTrigger{
		Types: StringList{"issues.*"},
		Actor: &EventEntityTrigger{
			IDs: StringList{"actor-*"},
			Attributes: map[string]StringList{
				"role": {"automation"},
			},
		},
		Attributes: map[string]StringList{
			"installation": {"prod"},
		},
	}
	result, err := EvaluateEventTrigger(trigger, eventing.Envelope{
		Type:       "pull_request.opened",
		Attributes: map[string]string{"installation": "prod"},
	})
	if err != nil {
		t.Fatalf("EvaluateEventTrigger() error = %v", err)
	}
	if result.Matched || MatchEventTrigger(trigger, eventing.Envelope{
		Type:       "pull_request.opened",
		Attributes: map[string]string{"installation": "prod"},
	}) {
		t.Fatalf("match result = %#v, want non-match", result)
	}
	checks := make(map[string]EventTriggerMatchCheck, len(result.Checks))
	for _, check := range result.Checks {
		checks[check.Path] = check
	}
	if check := checks["on.event.types"]; !check.Present || check.Matched {
		t.Fatalf("type check = %#v, want present non-match", check)
	}
	for _, path := range []string{
		"on.event.actor.ids",
		"on.event.actor.attributes.role",
	} {
		if check := checks[path]; check.Present || check.Matched {
			t.Fatalf("%s check = %#v, want missing non-match", path, check)
		}
	}
	if check := checks["on.event.attributes.installation"]; !check.Present || !check.Matched {
		t.Fatalf("installation check = %#v, want present match", check)
	}
}

func TestEvaluateEventTriggerRejectsAbsentAndInvalidTriggers(t *testing.T) {
	tests := []struct {
		name    string
		trigger *EventTrigger
		want    string
	}{
		{name: "absent", want: "on.event: event trigger is required"},
		{
			name:    "empty",
			trigger: &EventTrigger{},
			want:    "on.event: at least one filter is required",
		},
		{
			name:    "blank pattern",
			trigger: &EventTrigger{Types: StringList{""}},
			want:    "on.event.types[0]: pattern is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := EvaluateEventTrigger(test.trigger, eventing.Envelope{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("EvaluateEventTrigger() result=%#v error=%v, want %q", result, err, test.want)
			}
			if result.Matched || MatchEventTrigger(test.trigger, eventing.Envelope{}) {
				t.Fatalf("invalid trigger matched: %#v", test.trigger)
			}
		})
	}
}

func TestMatchEventTriggerCaseAndMissingValueSemantics(t *testing.T) {
	baseEvent := eventing.Envelope{
		Source:    "GitHub",
		Connector: "Primary",
		Type:      "Pull_Request.Opened",
		Actor: &eventing.Actor{
			ID:         "Dependabot",
			Type:       "Bot",
			Attributes: map[string]string{"role": "Automation"},
		},
		Subject: &eventing.Subject{
			ID:         "Repo-1",
			Type:       "Repository",
			Attributes: map[string]string{"repository": "Acme/Picoclaw", "empty": ""},
		},
		Attributes: map[string]string{"installation": "Production"},
	}
	tests := []struct {
		name    string
		trigger *EventTrigger
		mutate  func(*eventing.Envelope)
		want    bool
	}{
		{
			name: "top level fields ignore case",
			trigger: &EventTrigger{
				Sources:    StringList{"github"},
				Connectors: StringList{"primary"},
				Types:      StringList{"pull_request.*"},
			},
			want: true,
		},
		{
			name:    "actor type ignores case",
			trigger: &EventTrigger{Actor: &EventEntityTrigger{Types: StringList{"bot"}}},
			want:    true,
		},
		{
			name:    "actor id preserves case",
			trigger: &EventTrigger{Actor: &EventEntityTrigger{IDs: StringList{"dependabot"}}},
		},
		{
			name: "attribute value preserves case",
			trigger: &EventTrigger{Attributes: map[string]StringList{
				"installation": {"production"},
			}},
		},
		{
			name:    "subject id preserves case",
			trigger: &EventTrigger{Subject: &EventEntityTrigger{IDs: StringList{"repo-*"}}},
		},
		{
			name: "present empty attribute matches wildcard",
			trigger: &EventTrigger{Subject: &EventEntityTrigger{Attributes: map[string]StringList{
				"empty": {"*"},
			}}},
			want: true,
		},
		{
			name:    "missing actor never matches wildcard",
			trigger: &EventTrigger{Actor: &EventEntityTrigger{IDs: StringList{"*"}}},
			mutate:  func(event *eventing.Envelope) { event.Actor = nil },
		},
		{
			name:    "missing subject never matches wildcard",
			trigger: &EventTrigger{Subject: &EventEntityTrigger{Types: StringList{"*"}}},
			mutate:  func(event *eventing.Envelope) { event.Subject = nil },
		},
		{
			name: "missing top attribute never matches wildcard",
			trigger: &EventTrigger{Attributes: map[string]StringList{
				"missing": {"*"},
			}},
		},
		{
			name: "missing entity attribute never matches wildcard",
			trigger: &EventTrigger{Actor: &EventEntityTrigger{Attributes: map[string]StringList{
				"missing": {"*"},
			}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := baseEvent.Clone()
			if test.mutate != nil {
				test.mutate(&event)
			}
			if got := MatchEventTrigger(test.trigger, event); got != test.want {
				t.Fatalf("MatchEventTrigger() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMatchEventTriggerIsAnchoredAndTreatsOnlyStarQuestionAsWildcards(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{pattern: "pull_*", value: "pull_request.opened", want: true},
		{pattern: "pull", value: "pull_request.opened"},
		{pattern: "opened", value: "pull_request.opened"},
		{pattern: "repo-?", value: "repo-1", want: true},
		{pattern: "repo-?", value: "repo-12"},
		{pattern: "dependabot[bot]", value: "dependabot[bot]", want: true},
		{pattern: "dependabot[bot]", value: "dependabotb"},
		{pattern: `repo\*`, value: `repo\anything`, want: true},
		{pattern: "?", value: "🐙", want: true},
		{pattern: "??", value: "🐙"},
	}
	for _, test := range tests {
		t.Run(test.pattern+"/"+test.value, func(t *testing.T) {
			if got := eventGlobMatch(test.pattern, test.value, false); got != test.want {
				t.Fatalf("eventGlobMatch(%q, %q) = %v, want %v", test.pattern, test.value, got, test.want)
			}
		})
	}
}

func TestMatchEventTriggerUsesUnicodeCaseFoldingForTypedFields(t *testing.T) {
	if !MatchEventTrigger(
		&EventTrigger{Types: StringList{"Σ"}},
		eventing.Envelope{Type: "ς"},
	) {
		t.Fatal("case-insensitive event type did not use Unicode case folding")
	}
	if MatchEventTrigger(
		&EventTrigger{Actor: &EventEntityTrigger{IDs: StringList{"Σ"}}},
		eventing.Envelope{Actor: &eventing.Actor{ID: "ς"}},
	) {
		t.Fatal("case-sensitive actor ID unexpectedly used Unicode case folding")
	}
}

func TestMatchEventTriggerRejectsAbsentOrInvalidTriggers(t *testing.T) {
	event := eventing.Envelope{Source: "github"}
	if MatchEventTrigger(nil, event) {
		t.Fatal("nil trigger matched")
	}
	if WorkflowMatchesEvent(nil, event) {
		t.Fatal("nil workflow matched")
	}
	if WorkflowMatchesEvent(&Workflow{}, event) {
		t.Fatal("workflow without event trigger matched")
	}
	for _, trigger := range []*EventTrigger{
		{},
		{Types: StringList{}},
		{Types: StringList{""}},
		{Types: StringList{"*"}, Actor: &EventEntityTrigger{}},
		{Attributes: map[string]StringList{"missing": {}}},
	} {
		if MatchEventTrigger(trigger, event) {
			t.Fatalf("invalid trigger matched: %#v", trigger)
		}
	}
}

func TestMatchEventTriggerDoesNotMutateInputs(t *testing.T) {
	trigger := &EventTrigger{
		Sources: StringList{"GIT*"},
		Actor: &EventEntityTrigger{
			IDs: StringList{"actor-*"},
			Attributes: map[string]StringList{
				"role": {"bot"},
			},
		},
	}
	event := eventing.Envelope{
		Source: "GitHub",
		Actor: &eventing.Actor{
			ID:         "actor-1",
			Attributes: map[string]string{"role": "bot"},
		},
		Attributes: map[string]string{"installation": "prod"},
	}
	triggerBefore := cloneEventTriggerForTest(t, trigger)
	eventBefore := event.Clone()

	if !MatchEventTrigger(trigger, event) {
		t.Fatal("MatchEventTrigger() = false, want true")
	}
	if !reflect.DeepEqual(trigger, triggerBefore) {
		t.Fatalf("trigger mutated:\nbefore %#v\nafter  %#v", triggerBefore, trigger)
	}
	if !reflect.DeepEqual(event, eventBefore) {
		t.Fatalf("event mutated:\nbefore %#v\nafter  %#v", eventBefore, event)
	}
}

func TestEventTriggerPreservesExistingWorkflowTriggers(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Trigger compatibility
on:
  manual: {}
  schedule:
    - cron: "0 * * * *"
  channel_message:
    channels: telegram
  command:
    name: deploy
  runtime_event:
    kinds: agent.turn.end
  event:
    types: github.*
  workflow_call:
    inputs:
      target:
        type: string
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	if workflow.On.Manual == nil ||
		len(workflow.On.Schedule) != 1 ||
		workflow.On.ChannelMessage == nil ||
		workflow.On.Command == nil ||
		workflow.On.RuntimeEvent == nil ||
		workflow.On.Event == nil ||
		workflow.On.WorkflowCall == nil {
		t.Fatalf("triggers were not preserved: %#v", workflow.On)
	}
	if err := Validate(workflow); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestAgentToolsModeBumpsWorkflowCompatibility(t *testing.T) {
	if WorkflowEngineVersion != "7" {
		t.Fatalf("WorkflowEngineVersion = %q, want 7", WorkflowEngineVersion)
	}
	if WorkflowSchemaVersion != "2" {
		t.Fatalf("WorkflowSchemaVersion = %q, want 2", WorkflowSchemaVersion)
	}
	if ValidatorFingerprint != "picoclaw-workflow-validator-v3" {
		t.Fatalf("ValidatorFingerprint = %q, want v3", ValidatorFingerprint)
	}

	current := NormalizeRuntimeCompatibility(RuntimeCompatibility{PicoclawVersion: "v1.0.0"})
	oldStamp := WorkflowValidationStamp{
		WorkflowHash:         "same-hash",
		PicoclawVersion:      current.PicoclawVersion,
		WorkflowEngine:       "5",
		WorkflowSchema:       "2",
		ValidatorFingerprint: "picoclaw-workflow-validator-v2",
	}
	if stampMatchesRuntime(oldStamp, current, "same-hash") {
		t.Fatal("pre-agent-tools compatibility stamp matched current runtime")
	}
}

func eventTriggerTestWorkflow(trigger *EventTrigger) *Workflow {
	return &Workflow{
		On: WorkflowTriggers{Event: trigger},
		Jobs: map[string]Job{
			"main": {
				RunsOn: "picoclaw",
				Steps:  []Step{{Uses: "tool/message"}},
			},
		},
	}
}

func cloneEventTriggerForTest(t *testing.T, trigger *EventTrigger) *EventTrigger {
	t.Helper()
	data, err := json.Marshal(trigger)
	if err != nil {
		t.Fatal(err)
	}
	var clone EventTrigger
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return &clone
}
