package workflows

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const workflowEditorTestYAML = `# workflow comment
name: Event editor
on:
  schedule:
    - cron: "0 8 * * *" # schedule comment
  event:
    sources: github
    types: [issues.opened, pull_request.*]
jobs:
  main: # job comment
    runs-on: picoclaw
    steps:
      - uses: tool/message
`

func TestInspectWorkflowEventTriggerProjectsTriggerAndRevision(t *testing.T) {
	inspection := InspectWorkflowEventTrigger(workflowEditorTestYAML)

	if !inspection.Editable {
		t.Fatalf("Editable = false, reason = %q", inspection.Reason)
	}
	if inspection.Reason != "" {
		t.Fatalf("Reason = %q", inspection.Reason)
	}
	if !strings.HasPrefix(inspection.Revision, "sha256:") ||
		len(inspection.Revision) != len("sha256:")+64 {
		t.Fatalf("Revision = %q", inspection.Revision)
	}
	if inspection.Validation == nil || !inspection.Validation.Valid {
		t.Fatalf("Validation = %#v", inspection.Validation)
	}
	want := &EventTrigger{
		Sources: StringList{"github"},
		Types:   StringList{"issues.opened", "pull_request.*"},
	}
	if !reflect.DeepEqual(inspection.EventTrigger, want) {
		t.Fatalf("EventTrigger = %#v, want %#v", inspection.EventTrigger, want)
	}
	if next := InspectWorkflowEventTrigger(workflowEditorTestYAML); next.Revision != inspection.Revision {
		t.Fatalf("stable revision = %q, want %q", next.Revision, inspection.Revision)
	}
	if next := InspectWorkflowEventTrigger(workflowEditorTestYAML + "\n"); next.Revision == inspection.Revision {
		t.Fatal("revision did not change when the exact YAML bytes changed")
	}
}

func TestInspectWorkflowEventTriggerPreservesExplicitEmptyFilters(t *testing.T) {
	raw := `name: Invalid event
on:
  event:
    sources: []
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`
	inspection := InspectWorkflowEventTrigger(raw)

	if !inspection.Editable {
		t.Fatalf("Editable = false, reason = %q", inspection.Reason)
	}
	if inspection.EventTrigger == nil || inspection.EventTrigger.Sources == nil {
		t.Fatalf("EventTrigger.Sources = %#v, want explicit empty list", inspection.EventTrigger)
	}
	if len(inspection.EventTrigger.Sources) != 0 {
		t.Fatalf("EventTrigger.Sources = %#v, want empty", inspection.EventTrigger.Sources)
	}
	if inspection.Validation == nil || inspection.Validation.Valid {
		t.Fatalf("Validation = %#v, want invalid", inspection.Validation)
	}
	found := false
	for _, issue := range inspection.Validation.Errors {
		found = found || issue.Path == "on.event.sources"
	}
	if !found {
		got := inspection.Validation.Errors
		t.Fatalf("validation errors = %#v", got)
	}
}

func TestRenderWorkflowEventTriggerPreservesUnrelatedYAML(t *testing.T) {
	inspection := InspectWorkflowEventTrigger(workflowEditorTestYAML)
	requested := &EventTrigger{
		Connectors: StringList{"production"},
		Types:      StringList{"push"},
		Actor: &EventEntityTrigger{
			Types: StringList{"user"},
		},
		Attributes: map[string]StringList{
			"repository": {"acme/*"},
		},
	}
	rendered, next, err := RenderWorkflowEventTrigger(
		workflowEditorTestYAML,
		inspection.Revision,
		requested,
	)
	if err != nil {
		t.Fatalf("RenderWorkflowEventTrigger() error = %v", err)
	}

	for _, comment := range []string{
		"# workflow comment",
		"# schedule comment",
		"# job comment",
	} {
		if !strings.Contains(rendered, comment) {
			t.Errorf("rendered YAML lost unrelated comment %q:\n%s", comment, rendered)
		}
	}
	workflow, err := Parse([]byte(rendered))
	if err != nil {
		t.Fatalf("Parse(rendered) error = %v\n%s", err, rendered)
	}
	if !reflect.DeepEqual(workflow.On.Event, requested) {
		t.Fatalf("rendered event = %#v, want %#v", workflow.On.Event, requested)
	}
	if got := workflow.On.Schedule; len(got) != 1 || got[0].Cron != "0 8 * * *" {
		t.Fatalf("unrelated schedule = %#v", got)
	}
	if _, ok := workflow.Jobs["main"]; !ok {
		t.Fatalf("unrelated jobs = %#v", workflow.Jobs)
	}
	if !next.Editable || next.Revision == inspection.Revision {
		t.Fatalf("next inspection = %#v", next)
	}
	if !reflect.DeepEqual(next.EventTrigger, requested) {
		t.Fatalf("next EventTrigger = %#v, want %#v", next.EventTrigger, requested)
	}

	requested.Types[0] = "mutated-after-render"
	if next.EventTrigger.Types[0] != "push" {
		t.Fatal("render result aliases the caller's event trigger")
	}
}

func TestRenderWorkflowEventTriggerAddsAndRemovesOnlyEvent(t *testing.T) {
	withoutEvent := `name: Add event
on:
  manual: {}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`
	inspection := InspectWorkflowEventTrigger(withoutEvent)
	rendered, next, err := RenderWorkflowEventTrigger(
		withoutEvent,
		inspection.Revision,
		&EventTrigger{Sources: StringList{"github"}},
	)
	if err != nil {
		t.Fatalf("add event error = %v", err)
	}
	workflow, err := Parse([]byte(rendered))
	if err != nil {
		t.Fatalf("Parse(added) error = %v", err)
	}
	if workflow.On.Event == nil ||
		!reflect.DeepEqual(workflow.On.Event.Sources, StringList{"github"}) ||
		workflow.On.Manual == nil {
		t.Fatalf("added workflow triggers = %#v", workflow.On)
	}

	removed, removedInspection, err := RenderWorkflowEventTrigger(
		rendered,
		next.Revision,
		nil,
	)
	if err != nil {
		t.Fatalf("remove event error = %v", err)
	}
	workflow, err = Parse([]byte(removed))
	if err != nil {
		t.Fatalf("Parse(removed) error = %v", err)
	}
	if workflow.On.Event != nil || workflow.On.Manual == nil {
		t.Fatalf("removed workflow triggers = %#v", workflow.On)
	}
	if removedInspection.EventTrigger != nil || !removedInspection.Editable {
		t.Fatalf("removed inspection = %#v", removedInspection)
	}

	unchanged, sameInspection, err := RenderWorkflowEventTrigger(
		removed,
		removedInspection.Revision,
		nil,
	)
	if err != nil {
		t.Fatalf("no-op removal error = %v", err)
	}
	if unchanged != removed || sameInspection.Revision != removedInspection.Revision {
		t.Fatal("no-op removal changed exact YAML or revision")
	}
}

func TestRenderWorkflowEventTriggerRejectsStaleAndInvalidEdits(t *testing.T) {
	inspection := InspectWorkflowEventTrigger(workflowEditorTestYAML)

	_, _, err := RenderWorkflowEventTrigger(
		workflowEditorTestYAML+"\n",
		inspection.Revision,
		&EventTrigger{Sources: StringList{"github"}},
	)
	if !errors.Is(err, ErrWorkflowEventTriggerStaleRevision) {
		t.Fatalf("stale error = %v", err)
	}

	_, _, err = RenderWorkflowEventTrigger(
		workflowEditorTestYAML,
		inspection.Revision,
		&EventTrigger{Sources: StringList{}},
	)
	var validationErrs ValidationErrors
	if !errors.As(err, &validationErrs) {
		t.Fatalf("invalid edit error = %T %v, want ValidationErrors", err, err)
	}
	if len(validationErrs) == 0 || validationErrs[0].Path != "on.event.sources" {
		t.Fatalf("validation errors = %#v", validationErrs)
	}
}

func TestInspectWorkflowEventTriggerRefusesUnsafeOrUnsupportedYAML(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantReason string
	}{
		{
			name: "alias",
			raw: `name: Alias event
filters: &event_filters
  sources: [github]
on:
  event: *event_filters
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`,
			wantReason: "aliases",
		},
		{
			name: "merge",
			raw: `name: Merge event
defaults: &defaults
  runs-on: picoclaw
jobs:
  main:
    <<: *defaults
    steps:
      - uses: tool/message
on:
  event:
    sources: [github]
`,
			wantReason: "merge keys",
		},
		{
			name: "scalar on",
			raw: `name: Scalar on
on: manual
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`,
			wantReason: "on value",
		},
		{
			name:       "malformed",
			raw:        "name: [broken\n",
			wantReason: "syntax errors",
		},
		{
			name:       "non mapping",
			raw:        "- workflow\n",
			wantReason: "top-level mapping",
		},
		{
			name: "multiple documents",
			raw: workflowEditorTestYAML + `---
name: Hidden second workflow
jobs: {}
`,
			wantReason: "exactly one document",
		},
		{
			name: "parser-equivalent duplicate on",
			raw: `name: Duplicate logical on
" on ":
  manual: {}
on:
  event:
    sources: [github]
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`,
			wantReason: "duplicate on",
		},
		{
			name: "pattern containing a line break",
			raw: `name: Multiline pattern
on:
  event:
    types: ["issues.\nopened"]
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`,
			wantReason: "line breaks",
		},
		{
			name: "attribute name containing a line break",
			raw: `name: Multiline attribute
on:
  event:
    attributes:
      "repository\nname": acme/example
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`,
			wantReason: "line breaks",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			inspection := InspectWorkflowEventTrigger(test.raw)
			if inspection.Editable {
				t.Fatalf("inspection unexpectedly editable: %#v", inspection)
			}
			if !strings.Contains(strings.ToLower(inspection.Reason), test.wantReason) {
				t.Fatalf("Reason = %q, want substring %q", inspection.Reason, test.wantReason)
			}
			_, _, err := RenderWorkflowEventTrigger(
				test.raw,
				inspection.Revision,
				&EventTrigger{Sources: StringList{"github"}},
			)
			if !errors.Is(err, ErrWorkflowEventTriggerNotEditable) {
				t.Fatalf("render error = %v, want not-editable", err)
			}
		})
	}
}
