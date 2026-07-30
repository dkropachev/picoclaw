package workflows

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const workflowJobsEditorFixture = `# root comment
name: Jobs editor
on:
  manual: {}
jobs:
  review: # job comment
    name: ""
    runs-on: picoclaw
    needs: []
    if: ""
    continue-on-error: false
    with: {}
    outputs: {}
    context: {}
    advanced-safe:
      retained: true
    steps:
      - id: inventory
        name: ""
        uses: function/git.inventory # target comment
        if: ""
        continue-on-error: false
        with:
          count: 7
          ratio: 0.5
          nested:
            - false
            - null
        context: {}
        advanced-step: keep
      - uses: agent/main
  publish:
    uses: workflows/publish.yml
    secrets: inherit
`

func TestInspectWorkflowJobsProjectsOrderedPresenceAndAdvancedFields(t *testing.T) {
	inspection := InspectWorkflowJobs(workflowJobsEditorFixture)
	if !inspection.Editable || !inspection.Complete || len(inspection.Limits) != 0 {
		t.Fatalf("inspection state = %#v", inspection)
	}
	if inspection.Validation == nil || !inspection.Validation.Valid {
		t.Fatalf("validation = %#v", inspection.Validation)
	}
	if len(inspection.Jobs) != 2 ||
		inspection.Jobs[0].ID != "review" ||
		inspection.Jobs[1].ID != "publish" ||
		inspection.Jobs[0].Index != 0 ||
		inspection.Jobs[1].Index != 1 {
		t.Fatalf("jobs = %#v", inspection.Jobs)
	}
	review := inspection.Jobs[0]
	if !review.Editable ||
		!review.AdvancedFieldsPresent ||
		!review.StepsPresent ||
		len(review.Steps) != 2 ||
		!review.Steps[0].AdvancedFieldsPresent {
		t.Fatalf("review = %#v", review)
	}
	assertWorkflowEditorField(t, review.Fields, "name", "")
	assertWorkflowEditorField(t, review.Fields, "needs", []string{})
	assertWorkflowEditorField(t, review.Fields, "continue_on_error", false)
	assertWorkflowEditorField(t, review.Fields, "with", map[string]any{})
	assertWorkflowEditorField(t, review.Fields, "outputs", map[string]string{})
	assertWorkflowEditorField(t, review.Fields, "context", map[string]string{})
	assertWorkflowEditorField(t, review.Steps[0].Fields, "name", "")
	assertWorkflowEditorField(
		t,
		review.Steps[0].Fields,
		"continue_on_error",
		false,
	)
	if review.Fields["uses"].Present {
		t.Fatalf("absent uses = %#v", review.Fields["uses"])
	}
	if !inspection.Jobs[1].Fields["secrets"].Present ||
		inspection.Jobs[1].Fields["secrets"].Value != "inherit" {
		t.Fatalf("publish secrets = %#v", inspection.Jobs[1].Fields["secrets"])
	}
	if next := InspectWorkflowJobs(workflowJobsEditorFixture + "\n"); next.Revision == inspection.Revision {
		t.Fatal("revision did not fence exact source bytes")
	}
}

func TestRenderWorkflowJobsOperationsPreserveUntouchedNodes(t *testing.T) {
	raw := workflowJobsEditorFixture
	inspection := InspectWorkflowJobs(raw)
	render := func(operation WorkflowJobsOperation) {
		t.Helper()
		var err error
		raw, inspection, err = RenderWorkflowJobs(raw, inspection.Revision, operation)
		if err != nil {
			t.Fatalf("RenderWorkflowJobs(%T) error = %v", operation, err)
		}
		if !inspection.Editable {
			t.Fatalf("next inspection = %#v", inspection)
		}
		for _, retained := range []string{
			"# root comment",
			"# job comment",
			"# target comment",
			"advanced-safe:",
			"advanced-step: keep",
		} {
			if !strings.Contains(raw, retained) {
				t.Fatalf("render lost %q:\n%s", retained, raw)
			}
		}
	}

	render(WorkflowJobInsertOperation{
		JobID: "prepare",
		Index: 1,
		Fields: WorkflowEditorFieldMutations{
			"runs_on": {
				Mode:  WorkflowEditorMutationSet,
				Value: "picoclaw",
			},
		},
	})
	if got := []string{
		inspection.Jobs[0].ID,
		inspection.Jobs[1].ID,
		inspection.Jobs[2].ID,
	}; fmt.Sprint(got) != "[review prepare publish]" {
		t.Fatalf("job order = %v", got)
	}
	render(WorkflowStepInsertOperation{
		JobID: "prepare",
		Index: 0,
		Fields: WorkflowEditorFieldMutations{
			"id": {
				Mode:  WorkflowEditorMutationSet,
				Value: "",
			},
			"uses": {
				Mode:  WorkflowEditorMutationSet,
				Value: "tool/message",
			},
			"continue_on_error": {
				Mode:  WorkflowEditorMutationSet,
				Value: false,
			},
			"with": {
				Mode: WorkflowEditorMutationSet,
				Value: map[string]any{
					"text": "",
				},
			},
		},
	})
	render(WorkflowStepInsertOperation{
		JobID: "prepare",
		Index: 1,
		Fields: WorkflowEditorFieldMutations{
			"uses": {
				Mode:  WorkflowEditorMutationSet,
				Value: "function/workflow.state",
			},
		},
	})
	render(WorkflowStepMoveOperation{JobID: "prepare", StepIndex: 1, ToIndex: 0})
	if got := inspection.Jobs[1].Steps[0].Fields["uses"].Value; got != "function/workflow.state" {
		t.Fatalf("moved first step uses = %#v", got)
	}
	render(WorkflowStepPatchOperation{
		JobID:     "prepare",
		StepIndex: 1,
		Fields: WorkflowEditorFieldMutations{
			"id": {Mode: WorkflowEditorMutationRemove},
			"name": {
				Mode:  WorkflowEditorMutationSet,
				Value: "",
			},
		},
	})
	render(WorkflowStepDeleteOperation{JobID: "prepare", StepIndex: 0})
	renamed := "build"
	render(WorkflowJobPatchOperation{
		JobID:    "prepare",
		NewJobID: &renamed,
		Fields: WorkflowEditorFieldMutations{
			"name": {
				Mode:  WorkflowEditorMutationSet,
				Value: "Build",
			},
		},
	})
	if inspection.Jobs[1].ID != "build" ||
		!strings.Contains(raw, "build:\n") {
		t.Fatalf("renamed output:\n%s", raw)
	}
	render(WorkflowJobDeleteOperation{JobID: "build"})
	if len(inspection.Jobs) != 2 ||
		inspection.Jobs[1].ID != "publish" {
		t.Fatalf("jobs after delete = %#v", inspection.Jobs)
	}
}

func TestRenderWorkflowJobsExactNoOpsRetainSourceBytes(t *testing.T) {
	inspection := InspectWorkflowJobs(workflowJobsEditorFixture)
	operations := []WorkflowJobsOperation{
		WorkflowJobPatchOperation{JobID: "review", Fields: WorkflowEditorFieldMutations{}},
		WorkflowJobPatchOperation{
			JobID: "review",
			Fields: WorkflowEditorFieldMutations{
				"name": {
					Mode:  WorkflowEditorMutationSet,
					Value: "",
				},
				"continue_on_error": {
					Mode:  WorkflowEditorMutationSet,
					Value: false,
				},
			},
		},
		WorkflowStepPatchOperation{
			JobID:     "review",
			StepIndex: 0,
			Fields: WorkflowEditorFieldMutations{
				"with": {
					Mode: WorkflowEditorMutationSet,
					Value: map[string]any{
						"count": int64(7),
						"ratio": float64(0.5),
						"nested": []any{
							false,
							nil,
						},
					},
				},
			},
		},
		WorkflowStepMoveOperation{JobID: "review", StepIndex: 0, ToIndex: 0},
	}
	for _, operation := range operations {
		rendered, next, err := RenderWorkflowJobs(
			workflowJobsEditorFixture,
			inspection.Revision,
			operation,
		)
		if err != nil {
			t.Fatalf("%T error = %v", operation, err)
		}
		if rendered != workflowJobsEditorFixture ||
			next.Revision != inspection.Revision {
			t.Fatalf("%T changed exact no-op bytes", operation)
		}
	}
}

func TestRenderWorkflowJobsPreservesChangedNodeStyles(t *testing.T) {
	raw := `name: styles
on:
  manual: {}
jobs:
  main:
    name: 'old job'
    runs-on: picoclaw
    with: {old: value}
    steps:
      - name: |-
          old
          lines
        uses: tool/message
`
	inspection := InspectWorkflowJobs(raw)
	rendered, next, err := RenderWorkflowJobs(
		raw,
		inspection.Revision,
		WorkflowJobPatchOperation{
			JobID: "main",
			Fields: WorkflowEditorFieldMutations{
				"name": {
					Mode:  WorkflowEditorMutationSet,
					Value: "new job",
				},
				"with": {
					Mode: WorkflowEditorMutationSet,
					Value: map[string]any{
						"new": "value",
					},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("job style patch error = %v", err)
	}
	rendered, _, err = RenderWorkflowJobs(
		rendered,
		next.Revision,
		WorkflowStepPatchOperation{
			JobID:     "main",
			StepIndex: 0,
			Fields: WorkflowEditorFieldMutations{
				"name": {
					Mode:  WorkflowEditorMutationSet,
					Value: "new\nlines",
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("step style patch error = %v", err)
	}
	for _, styled := range []string{
		"name: 'new job'",
		"with: {new: value}",
		"name: |-",
	} {
		if !strings.Contains(rendered, styled) {
			t.Fatalf("missing retained style %q:\n%s", styled, rendered)
		}
	}
}

func TestRenderWorkflowJobsAllowsInvalidIntermediateCandidate(t *testing.T) {
	raw := "name: draft\non:\n  manual: {}\njobs: {}\n"
	inspection := InspectWorkflowJobs(raw)
	rendered, next, err := RenderWorkflowJobs(
		raw,
		inspection.Revision,
		WorkflowJobInsertOperation{
			JobID:  "draft",
			Index:  0,
			Fields: WorkflowEditorFieldMutations{},
		},
	)
	if err != nil || rendered == "" {
		t.Fatalf("render = %q, inspection = %#v, err = %v", rendered, next, err)
	}
	if next.Validation == nil ||
		next.Validation.Valid ||
		len(next.Validation.Errors) == 0 {
		t.Fatalf("candidate validation = %#v", next.Validation)
	}
}

func TestWorkflowJobsEditorGranularRawOnlyTargets(t *testing.T) {
	raw := `name: granular
on:
  manual: {}
jobs:
  raw_job:
    runs-on: picoclaw
    context:
      advanced: keep
    steps:
      - uses: tool/message
  mixed:
    runs-on: picoclaw
    steps:
      - uses: tool/message
      - uses: [unsafe-shape]
      - uses: tool/message
      - uses: tool/message
  safe:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`
	inspection := InspectWorkflowJobs(raw)
	if !inspection.Editable ||
		inspection.Complete ||
		!inspection.hasLimit(WorkflowJobsEditorLimitUnsafeFields) {
		t.Fatalf("inspection = %#v", inspection)
	}
	if inspection.Jobs[0].Editable ||
		!inspection.Jobs[1].Editable ||
		!inspection.Jobs[1].Steps[0].Editable ||
		inspection.Jobs[1].Steps[1].Editable ||
		!inspection.Jobs[1].Steps[2].Editable ||
		!inspection.Jobs[2].Editable {
		t.Fatalf("granular projections = %#v", inspection.Jobs)
	}

	rendered, _, err := RenderWorkflowJobs(
		raw,
		inspection.Revision,
		WorkflowJobPatchOperation{
			JobID: "safe",
			Fields: WorkflowEditorFieldMutations{
				"name": {Mode: WorkflowEditorMutationSet, Value: "Safe"},
			},
		},
	)
	if err != nil || !strings.Contains(rendered, "advanced: keep") {
		t.Fatalf("safe sibling render error = %v\n%s", err, rendered)
	}
	if _, _, err := RenderWorkflowJobs(
		raw,
		inspection.Revision,
		WorkflowJobPatchOperation{
			JobID: "raw_job",
			Fields: WorkflowEditorFieldMutations{
				"name": {Mode: WorkflowEditorMutationSet, Value: "Blocked"},
			},
		},
	); !errors.Is(err, ErrWorkflowJobsNotEditable) {
		t.Fatalf("raw job patch error = %v", err)
	}
	if _, _, err := RenderWorkflowJobs(
		raw,
		inspection.Revision,
		WorkflowStepPatchOperation{
			JobID:     "mixed",
			StepIndex: 1,
			Fields: WorkflowEditorFieldMutations{
				"name": {Mode: WorkflowEditorMutationSet, Value: "Blocked"},
			},
		},
	); !errors.Is(err, ErrWorkflowJobsNotEditable) {
		t.Fatalf("raw step patch error = %v", err)
	}
	for _, move := range []WorkflowStepMoveOperation{
		{JobID: "mixed", StepIndex: 0, ToIndex: 2},
		{JobID: "mixed", StepIndex: 2, ToIndex: 0},
	} {
		if _, _, err := RenderWorkflowJobs(
			raw,
			inspection.Revision,
			move,
		); !errors.Is(err, ErrWorkflowJobsNotEditable) {
			t.Fatalf("raw crossing move %#v error = %v", move, err)
		}
	}
	if _, _, err := RenderWorkflowJobs(
		raw,
		inspection.Revision,
		WorkflowStepMoveOperation{JobID: "mixed", StepIndex: 2, ToIndex: 3},
	); err != nil {
		t.Fatalf("safe-range move error = %v", err)
	}
	if _, _, err := RenderWorkflowJobs(
		raw,
		inspection.Revision,
		WorkflowStepInsertOperation{
			JobID: "raw_job",
			Index: 1,
			Fields: WorkflowEditorFieldMutations{
				"uses": {Mode: WorkflowEditorMutationSet, Value: "tool/message"},
			},
		},
	); err != nil {
		t.Fatalf("step insert beside raw-only job field error = %v", err)
	}

	malformedSteps := `jobs:
  main:
    context: null
    steps: {}
`
	malformedInspection := InspectWorkflowJobs(malformedSteps)
	if _, _, err := RenderWorkflowJobs(
		malformedSteps,
		malformedInspection.Revision,
		WorkflowStepInsertOperation{
			JobID: "main",
			Index: 0,
			Fields: WorkflowEditorFieldMutations{
				"uses": {Mode: WorkflowEditorMutationSet, Value: "tool/message"},
			},
		},
	); !errors.Is(err, ErrWorkflowJobsNotEditable) {
		t.Fatalf("malformed later steps insert error = %v", err)
	}
}

func TestInspectWorkflowJobsBoundsAggregateStepsWithoutPanic(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("name: bounded\non:\n  manual: {}\njobs:\n")
	counts := []int{3000, 3000, 1}
	for jobIndex, count := range counts {
		fmt.Fprintf(&raw, "  job_%d:\n    runs-on: picoclaw\n    steps:\n", jobIndex)
		for stepIndex := 0; stepIndex < count; stepIndex++ {
			raw.WriteString("      - uses: tool/message\n")
		}
	}
	inspection := InspectWorkflowJobs(raw.String())
	if inspection.Editable ||
		inspection.Complete ||
		!inspection.hasLimit(WorkflowJobsEditorLimitSteps) ||
		len(inspection.Jobs) != 3 ||
		len(inspection.Jobs[0].Steps) != 3000 ||
		len(inspection.Jobs[1].Steps) != 1096 ||
		len(inspection.Jobs[2].Steps) != 0 {
		t.Fatalf(
			"bounded projection: editable=%t complete=%t limits=%v counts=%d/%d/%d",
			inspection.Editable,
			inspection.Complete,
			inspection.Limits,
			len(inspection.Jobs[0].Steps),
			len(inspection.Jobs[1].Steps),
			len(inspection.Jobs[2].Steps),
		)
	}
}

func TestInspectWorkflowJobsPreflightRejectsUnsafeAndComplexBeforeSemanticParse(
	t *testing.T,
) {
	original := workflowJobsValidateSemantic
	t.Cleanup(func() { workflowJobsValidateSemantic = original })
	calls := 0
	workflowJobsValidateSemantic = func(string) (*WorkflowDevelopmentValidation, bool) {
		calls++
		return &WorkflowDevelopmentValidation{Valid: true}, false
	}
	cases := map[string]string{
		"alias": `base: &base
  runs-on: picoclaw
jobs:
  copied: *base
`,
		"merge": `base: &base
  runs-on: picoclaw
jobs:
  copied:
    <<: *base
`,
		"scalar anchor":  "jobs:\n  main:\n    name: &label hello\n",
		"mapping anchor": "jobs: &catalog {}\n",
		"sequence anchor": `jobs:
  main:
    steps: &steps []
`,
		"YAML directive": "%YAML 1.1\n---\njobs: {}\n",
		"TAG directive":  "%TAG !yaml! tag:yaml.org,2002:\n---\njobs: {}\n",
		"custom tag":     "jobs:\n  main: !private {}\n",
		"duplicate":      "jobs:\n  main: {}\n  main: {}\n",
		"bidi key":       "jobs:\n  main:\n    advanced\u202e: value\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			inspection := InspectWorkflowJobs(raw)
			if inspection.Editable ||
				inspection.Complete ||
				inspection.Validation == nil ||
				len(inspection.Validation.Errors) != 1 {
				t.Fatalf("inspection = %#v", inspection)
			}
			if strings.Contains(
				inspection.Validation.Errors[0].Message,
				"private",
			) {
				t.Fatalf("validation leaked source detail: %#v", inspection.Validation)
			}
			if _, _, err := RenderWorkflowJobs(
				raw,
				inspection.Revision,
				WorkflowJobInsertOperation{
					JobID:  "new",
					Fields: WorkflowEditorFieldMutations{},
				},
			); !errors.Is(err, ErrWorkflowJobsNotEditable) {
				t.Fatalf("unsafe render error = %v", err)
			}
		})
	}

	var deep strings.Builder
	deep.WriteString("root:")
	for index := 0; index < MaxWorkflowJobsEditorYAMLDepth+2; index++ {
		deep.WriteString("\n")
		deep.WriteString(strings.Repeat("  ", index+1))
		deep.WriteString("next:")
	}
	deep.WriteString(" value\n")
	if inspection := InspectWorkflowJobs(deep.String()); inspection.Editable {
		t.Fatalf("deep inspection = %#v", inspection)
	}

	var wide strings.Builder
	wide.WriteString("root:\n")
	for index := 0; index < MaxWorkflowJobsEditorYAMLNodes; index++ {
		fmt.Fprintf(&wide, "  - %d\n", index)
	}
	started := time.Now()
	if inspection := InspectWorkflowJobs(wide.String()); inspection.Editable {
		t.Fatalf("wide inspection = %#v", inspection)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("bounded preflight took %s", elapsed)
	}
	if calls != 0 {
		t.Fatalf("semantic validator called %d times for unsafe inputs", calls)
	}
}

func TestInspectWorkflowJobsDoesNotTreatBlockScalarTextAsDirective(t *testing.T) {
	raw := `name: |-
  %YAML 1.1
  %TAG !yaml! tag:yaml.org,2002:
jobs: {}
`
	if inspection := InspectWorkflowJobs(raw); !inspection.Editable {
		t.Fatalf("block-scalar directive text = %#v", inspection)
	}
}

func TestInspectWorkflowJobsRawOnlyRootsAlwaysReportOmission(t *testing.T) {
	cases := map[string]string{
		"syntax":             "jobs: [private-detail",
		"multiple documents": "jobs: {}\n---\njobs: {}\n",
		"empty":              "",
		"scalar":             "private-detail\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			inspection := InspectWorkflowJobs(raw)
			if inspection.Editable ||
				inspection.Complete ||
				!inspection.hasLimit(WorkflowJobsEditorLimitUnsafeFields) ||
				inspection.Validation == nil ||
				len(inspection.Validation.Errors) == 0 {
				t.Fatalf("inspection = %#v", inspection)
			}
			if strings.Contains(
				inspection.Validation.Errors[0].Message,
				"private-detail",
			) {
				t.Fatalf("validation leaked source: %#v", inspection.Validation)
			}
		})
	}
}

func TestWorkflowJobsValidationBoundsSignalEveryOmission(t *testing.T) {
	exact := strings.Repeat("x", MaxWorkflowJobsEditorStringBytes)
	issues, truncated := boundedWorkflowJobsValidationIssues(
		[]WorkflowValidationIssue{{Path: exact, Message: exact}},
	)
	if truncated || len(issues) != 1 {
		t.Fatalf("exact issue = %#v, truncated=%t", issues, truncated)
	}
	issues, truncated = boundedWorkflowJobsValidationIssues(
		[]WorkflowValidationIssue{{
			Path:    exact + "x",
			Message: "message",
		}},
	)
	if !truncated || len(issues) != 1 ||
		len(issues[0].Path) != MaxWorkflowJobsEditorStringBytes {
		t.Fatalf("oversized issue = %#v, truncated=%t", issues, truncated)
	}
	many := make([]WorkflowValidationIssue, 100)
	for index := range many {
		many[index] = WorkflowValidationIssue{
			Path:    exact,
			Message: exact,
		}
	}
	issues, truncated = boundedWorkflowJobsValidationIssues(many)
	if !truncated ||
		len(issues) == 0 ||
		len(issues) >= len(many) {
		t.Fatalf("aggregate issues = %d, truncated=%t", len(issues), truncated)
	}
	tooMany := make([]WorkflowValidationIssue, 1025)
	for index := range tooMany {
		tooMany[index] = WorkflowValidationIssue{Message: "bounded"}
	}
	issues, truncated = boundedWorkflowJobsValidationIssues(tooMany)
	if !truncated || len(issues) != 1024 {
		t.Fatalf("issue-count bound = %d, truncated=%t", len(issues), truncated)
	}

	original := workflowJobsValidateSemantic
	t.Cleanup(func() { workflowJobsValidateSemantic = original })
	workflowJobsValidateSemantic = func(
		string,
	) (*WorkflowDevelopmentValidation, bool) {
		return &WorkflowDevelopmentValidation{
			Errors: []WorkflowValidationIssue{{Message: "bounded"}},
		}, true
	}
	inspection := InspectWorkflowJobs("jobs: {}\n")
	if !inspection.Editable ||
		inspection.Complete ||
		!inspection.hasLimit(WorkflowJobsEditorLimitValidation) {
		t.Fatalf("validation-limited inspection = %#v", inspection)
	}
}

func TestWorkflowJobsEditorRejectsControlsBoundsAndUnsafeNumbers(t *testing.T) {
	controls := []string{
		"a\tb",
		"a\u007fb",
		"a\u202eb",
		"a\u200db",
	}
	for _, id := range controls {
		raw := fmt.Sprintf("jobs:\n  %q: {}\n", id)
		if inspection := InspectWorkflowJobs(raw); inspection.Editable {
			t.Fatalf("control ID %q was editable", id)
		}
	}
	for name, raw := range map[string]string{
		"dynamic tab key": "jobs:\n  main:\n    with:\n      \"a\\tb\": value\n",
		"output bidi key": "jobs:\n  main:\n    outputs:\n      \"a\\u202eb\": value\n",
		"context ZWJ key": "jobs:\n  main:\n    context:\n      \"a\\u200db\": value\n",
	} {
		t.Run(name, func(t *testing.T) {
			if inspection := InspectWorkflowJobs(raw); inspection.Editable {
				t.Fatalf("control key inspection = %#v", inspection)
			}
		})
	}
	allowed := `name: controls
on:
  manual: {}
jobs:
  main:
    name: |-
      line one
      ` + "\t" + `line two
    runs-on: picoclaw
    steps:
      - uses: tool/message
        with:
          prompt: "line one\n\tline two\r"
`
	if inspection := InspectWorkflowJobs(allowed); !inspection.Editable {
		t.Fatalf("allowed formatting controls = %#v", inspection)
	}
	unsafeNumber := `jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
        with:
          value: 1e-10000
`
	started := time.Now()
	if inspection := InspectWorkflowJobs(unsafeNumber); inspection.Jobs[0].Steps[0].Editable {
		t.Fatalf("unsafe numeric projection = %#v", inspection)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("unsafe numeric inspection took %s", elapsed)
	}
	for name, operation := range map[string]WorkflowJobsOperation{
		"NUL string": WorkflowJobPatchOperation{
			JobID: "main",
			Fields: WorkflowEditorFieldMutations{
				"name": {Mode: WorkflowEditorMutationSet, Value: "a\x00b"},
			},
		},
		"DEL output key": WorkflowJobPatchOperation{
			JobID: "main",
			Fields: WorkflowEditorFieldMutations{
				"outputs": {
					Mode: WorkflowEditorMutationSet,
					Value: map[string]string{
						"a\x7fb": "value",
					},
				},
			},
		},
		"bidi dynamic key": WorkflowJobPatchOperation{
			JobID: "main",
			Fields: WorkflowEditorFieldMutations{
				"with": {
					Mode: WorkflowEditorMutationSet,
					Value: map[string]any{
						"a\u202eb": "value",
					},
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw := "jobs:\n  main: {}\n"
			inspection := InspectWorkflowJobs(raw)
			if _, _, err := RenderWorkflowJobs(
				raw,
				inspection.Revision,
				operation,
			); !errors.Is(err, ErrWorkflowJobsOperation) {
				t.Fatalf("control mutation error = %v", err)
			}
		})
	}

	var needs strings.Builder
	needs.WriteString("jobs:\n  main:\n    needs:\n")
	for index := 0; index <= MaxWorkflowJobsEditorJSONEntries; index++ {
		fmt.Fprintf(&needs, "      - job_%d\n", index)
	}
	if inspection := InspectWorkflowJobs(needs.String()); inspection.Jobs[0].Editable {
		t.Fatalf("oversized needs = %#v", inspection.Jobs[0])
	}
}

func TestWorkflowJobsIdentityFieldsUseExactShapeBounds(t *testing.T) {
	maxID := strings.Repeat("i", MaxWorkflowJobsEditorIDBytes)
	tooLongID := maxID + "i"
	for name, test := range map[string]struct {
		raw  string
		step bool
	}{
		"step id too long": {
			raw: fmt.Sprintf(
				"jobs:\n  main:\n    steps:\n      - id: %q\n",
				tooLongID,
			),
			step: true,
		},
		"needs item too long": {
			raw: fmt.Sprintf(
				"jobs:\n  main:\n    needs: [%q]\n",
				tooLongID,
			),
		},
		"step id newline": {
			raw:  "jobs:\n  main:\n    steps:\n      - id: \"one\\ntwo\"\n",
			step: true,
		},
		"needs control": {
			raw: "jobs:\n  main:\n    needs: [\"one\\u202etwo\"]\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			inspection := InspectWorkflowJobs(test.raw)
			if len(inspection.Jobs) != 1 {
				t.Fatalf("identity inspection = %#v", inspection)
			}
			if test.step {
				if len(inspection.Jobs[0].Steps) != 1 ||
					inspection.Jobs[0].Steps[0].Editable {
					t.Fatalf("step identity inspection = %#v", inspection)
				}
			} else if inspection.Jobs[0].Editable {
				t.Fatalf("job identity inspection = %#v", inspection)
			}
		})
	}

	raw := fmt.Sprintf(
		"jobs:\n  main:\n    needs: [%q, \"\"]\n    steps:\n      - id: %q\n",
		maxID,
		maxID,
	)
	inspection := InspectWorkflowJobs(raw)
	if !inspection.Editable ||
		!inspection.Jobs[0].Editable ||
		!inspection.Jobs[0].Steps[0].Editable {
		t.Fatalf("bounded/empty identity inspection = %#v", inspection)
	}
	assertWorkflowEditorField(
		t,
		inspection.Jobs[0].Steps[0].Fields,
		"id",
		maxID,
	)
	assertWorkflowEditorField(
		t,
		inspection.Jobs[0].Fields,
		"needs",
		[]string{maxID, ""},
	)

	base := "jobs:\n  main:\n    steps:\n      - uses: tool/message\n"
	baseInspection := InspectWorkflowJobs(base)
	if _, _, err := RenderWorkflowJobs(
		base,
		baseInspection.Revision,
		WorkflowStepPatchOperation{
			JobID:     "main",
			StepIndex: 0,
			Fields: WorkflowEditorFieldMutations{
				"id": {Mode: WorkflowEditorMutationSet, Value: maxID},
			},
		},
	); err != nil {
		t.Fatalf("max step ID mutation error = %v", err)
	}
	for name, operation := range map[string]WorkflowJobsOperation{
		"step id too long": WorkflowStepPatchOperation{
			JobID:     "main",
			StepIndex: 0,
			Fields: WorkflowEditorFieldMutations{
				"id": {Mode: WorkflowEditorMutationSet, Value: tooLongID},
			},
		},
		"needs too long": WorkflowJobPatchOperation{
			JobID: "main",
			Fields: WorkflowEditorFieldMutations{
				"needs": {
					Mode:  WorkflowEditorMutationSet,
					Value: []string{tooLongID},
				},
			},
		},
		"needs empty": WorkflowJobPatchOperation{
			JobID: "main",
			Fields: WorkflowEditorFieldMutations{
				"needs": {
					Mode:  WorkflowEditorMutationSet,
					Value: []string{""},
				},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := RenderWorkflowJobs(
				base,
				baseInspection.Revision,
				operation,
			); !errors.Is(err, ErrWorkflowJobsOperation) {
				t.Fatalf("identity mutation error = %v", err)
			}
		})
	}
}

func TestInspectWorkflowJobsKeepsDistinctRawOnlyJobIDsProjectable(t *testing.T) {
	raw := `jobs:
  " first ":
    runs-on: picoclaw
    steps:
      - uses: tool/message
  " second ":
    runs-on: picoclaw
    steps:
      - uses: tool/message
`
	inspection := InspectWorkflowJobs(raw)
	if !inspection.Editable ||
		len(inspection.Jobs) != 2 ||
		inspection.Jobs[0].ID != " first " ||
		inspection.Jobs[1].ID != " second " ||
		inspection.Jobs[0].Editable ||
		inspection.Jobs[1].Editable {
		t.Fatalf("raw-only IDs = %#v", inspection)
	}
	if _, _, err := RenderWorkflowJobs(
		raw,
		inspection.Revision,
		WorkflowStepInsertOperation{
			JobID:  " first ",
			Index:  1,
			Fields: WorkflowEditorFieldMutations{},
		},
	); !errors.Is(err, ErrWorkflowJobsOperation) {
		t.Fatalf("raw-only ID step insert error = %v", err)
	}

	malformed := `jobs:
  " invalid ":
    context: null
    steps: {}
`
	malformedInspection := InspectWorkflowJobs(malformed)
	if len(malformedInspection.Jobs) != 1 ||
		malformedInspection.Jobs[0].sourceStepCount != 0 ||
		malformedInspection.Jobs[0].stepsContainerEditable {
		t.Fatalf("invalid ID topology = %#v", malformedInspection.Jobs)
	}
	if _, _, err := RenderWorkflowJobs(
		malformed,
		malformedInspection.Revision,
		WorkflowStepInsertOperation{
			JobID:  " invalid ",
			Index:  0,
			Fields: WorkflowEditorFieldMutations{},
		},
	); !errors.Is(err, ErrWorkflowJobsOperation) {
		t.Fatalf("invalid ID malformed steps insert error = %v", err)
	}
}

func TestRenderWorkflowJobsRejectsRevisionTargetsAndInvalidOperations(t *testing.T) {
	inspection := InspectWorkflowJobs(workflowJobsEditorFixture)
	if _, _, err := RenderWorkflowJobs(
		workflowJobsEditorFixture,
		"sha256:stale",
		WorkflowJobDeleteOperation{JobID: "review"},
	); !errors.Is(err, ErrWorkflowJobsStaleRevision) {
		t.Fatalf("stale error = %v", err)
	}
	if _, _, err := RenderWorkflowJobs(
		workflowJobsEditorFixture,
		inspection.Revision,
		WorkflowJobDeleteOperation{JobID: "missing"},
	); !errors.Is(err, ErrWorkflowJobsTarget) {
		t.Fatalf("target error = %v", err)
	}
	if _, _, err := RenderWorkflowJobs(
		workflowJobsEditorFixture,
		inspection.Revision,
		WorkflowStepPatchOperation{
			JobID:     "review",
			StepIndex: 0,
			Fields: WorkflowEditorFieldMutations{
				"uses": {
					Mode:  WorkflowEditorMutationSet,
					Value: "not-a-target",
				},
			},
		},
	); !errors.Is(err, ErrWorkflowJobsOperation) {
		t.Fatalf("invalid target error = %v", err)
	}
	collision := "publish"
	if _, _, err := RenderWorkflowJobs(
		workflowJobsEditorFixture,
		inspection.Revision,
		WorkflowJobPatchOperation{
			JobID:    "review",
			NewJobID: &collision,
			Fields:   WorkflowEditorFieldMutations{},
		},
	); !errors.Is(err, ErrWorkflowJobsOperation) {
		t.Fatalf("rename collision error = %v", err)
	}
}

func assertWorkflowEditorField(
	t *testing.T,
	fields map[string]WorkflowEditorFieldProjection,
	name string,
	want any,
) {
	t.Helper()
	field, exists := fields[name]
	if !exists || !field.Present || !workflowTriggerValuesEqual(field.Value, want) {
		t.Fatalf("%s = %#v, want %#v", name, field, want)
	}
}
