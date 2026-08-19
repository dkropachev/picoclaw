package workflows

import (
	"errors"
	"math"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type unsupportedWorkflowJobsCoverageOperation struct{}

func (unsupportedWorkflowJobsCoverageOperation) workflowJobsOperation() {}

func TestWorkflowJobsInspectionRejectsAmbiguousASTShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "top-level sequence", raw: "- one\n- two\n"},
		{name: "anchored mapping", raw: "jobs: &shared\n  main: {runs-on: picoclaw}\n"},
		{name: "duplicate jobs", raw: "jobs: {}\njobs: {}\n"},
		{name: "non-mapping jobs", raw: "jobs: []\n"},
		{name: "scalar job", raw: "jobs:\n  main: invalid\n"},
		{name: "scalar step", raw: "jobs:\n  main:\n    runs-on: picoclaw\n    steps: [invalid]\n"},
		{name: "duplicate job ID", raw: "jobs:\n  main: {runs-on: picoclaw}\n  main: {runs-on: picoclaw}\n"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			inspection := InspectWorkflowJobs(test.raw)
			if inspection.Reason == "" {
				t.Fatalf("inspection = %#v, want raw-only reason", inspection)
			}
		})
	}
	withoutJobs := InspectWorkflowJobs("name: no-jobs\n")
	if !withoutJobs.Editable || len(withoutJobs.Jobs) != 0 {
		t.Fatalf("workflow without jobs = %#v", withoutJobs)
	}

	var oversized strings.Builder
	oversized.WriteString("jobs:\n")
	for index := 0; index <= MaxWorkflowJobsEditorJobs; index++ {
		oversized.WriteString("  job_")
		oversized.WriteString(workflowCoverageDecimal(index))
		oversized.WriteString(": {runs-on: picoclaw}\n")
	}
	limited := InspectWorkflowJobs(oversized.String())
	if !limited.hasLimit(WorkflowJobsEditorLimitJobs) || limited.Reason == "" {
		t.Fatalf("oversized jobs inspection = %#v", limited)
	}
}

func TestWorkflowJobsKnownFieldProjectionRejectsUnsafeShapes(t *testing.T) {
	nullNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	boolNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}
	stringNode := func(value string) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	}
	sequenceNode := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	tests := []struct {
		name  string
		field string
		node  *yaml.Node
		step  bool
	}{
		{name: "null", field: "name", node: nullNode},
		{name: "invalid ID", field: "id", node: boolNode},
		{name: "invalid name", field: "name", node: boolNode},
		{name: "invalid uses", field: "uses", node: boolNode},
		{name: "invalid boolean", field: "continue_on_error", node: stringNode("yes")},
		{name: "step needs", field: "needs", node: stringNode("main"), step: true},
		{name: "invalid needs", field: "needs", node: &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}},
		{name: "invalid with", field: "with", node: stringNode("no")},
		{name: "step secrets", field: "secrets", node: stringNode("inherit"), step: true},
		{name: "invalid secret scalar", field: "secrets", node: stringNode("all")},
		{name: "invalid secret collection", field: "secrets", node: sequenceNode},
		{name: "step outputs", field: "outputs", node: &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, step: true},
		{name: "invalid context", field: "context", node: sequenceNode},
		{name: "unsupported field", field: "unknown", node: stringNode("value")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, reason := projectWorkflowEditorKnownField(test.field, test.node, test.step); reason == "" {
				t.Fatal("unsafe field projection was accepted")
			}
		})
	}

	for _, test := range []struct {
		name           string
		node           *yaml.Node
		allowMultiline bool
	}{
		{name: "non-string", node: boolNode, allowMultiline: true},
		{name: "oversized", node: stringNode(strings.Repeat("x", MaxWorkflowJobsEditorStringBytes+1)), allowMultiline: true},
		{name: "multiline", node: stringNode("one\ntwo")},
		{name: "control", node: stringNode("bad\x00value"), allowMultiline: true},
	} {
		t.Run("string/"+test.name, func(t *testing.T) {
			if reason := workflowJobsStringNodeReason(test.node, test.allowMultiline); reason == "" {
				t.Fatal("unsafe string node was accepted")
			}
		})
	}
}

func TestWorkflowJobsCollectionProjectionRejectsUnsafeShapes(t *testing.T) {
	str := func(value string) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	}
	if _, reason := workflowJobsStringListProjection(&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}); reason == "" {
		t.Fatal("mapping needs value was accepted")
	}
	largeList := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for index := 0; index <= MaxWorkflowJobsEditorJSONEntries; index++ {
		largeList.Content = append(largeList.Content, str("job"))
	}
	if _, reason := workflowJobsStringListProjection(largeList); reason == "" {
		t.Fatal("oversized needs list was accepted")
	}
	badList := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{str("bad\njob")}}
	if _, reason := workflowJobsStringListProjection(badList); reason == "" {
		t.Fatal("multiline needs item was accepted")
	}

	mapping := func(content ...*yaml.Node) *yaml.Node {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: content}
	}
	outputCases := []*yaml.Node{
		{Kind: yaml.SequenceNode, Tag: "!!seq"},
		mapping(str("key")),
		mapping(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}, str("value")),
		mapping(str("same"), str("one"), str("same"), str("two")),
		mapping(str("key"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}),
	}
	for index, node := range outputCases {
		if _, reason := workflowJobsStringMapProjection(node); reason == "" {
			t.Fatalf("unsafe output mapping %d was accepted", index)
		}
	}
	largeOutput := mapping()
	for index := 0; index <= MaxWorkflowJobsEditorJSONEntries; index++ {
		largeOutput.Content = append(largeOutput.Content, str("key"+workflowCoverageDecimal(index)), str("value"))
	}
	if _, reason := workflowJobsStringMapProjection(largeOutput); reason == "" {
		t.Fatal("oversized outputs mapping was accepted")
	}

	contextCases := []*yaml.Node{
		{Kind: yaml.SequenceNode, Tag: "!!seq"},
		mapping(str("key")),
		mapping(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}, str("value")),
		mapping(str("unknown"), str("value")),
		mapping(str("session"), str("one"), str("session"), str("two")),
		mapping(str("session"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}),
	}
	for index, node := range contextCases {
		if _, reason := workflowJobsContextProjection(node); reason == "" {
			t.Fatalf("unsafe context mapping %d was accepted", index)
		}
	}
}

func TestWorkflowJobsJSONProjectionRejectsEveryUnsafeValueClass(t *testing.T) {
	str := func(value string) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	}
	state := workflowJobsJSONState{}
	if _, reason := state.project(nil, 0); reason == "" {
		t.Fatal("nil JSON node was accepted")
	}
	if _, reason := state.project(str("value"), MaxWorkflowJobsEditorJSONDepth+1); reason == "" {
		t.Fatal("over-depth JSON node was accepted")
	}
	state.entries = MaxWorkflowJobsEditorJSONEntries
	if _, reason := state.project(str("value"), 0); reason == "" {
		t.Fatal("over-entry JSON node was accepted")
	}

	cases := []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: strings.Repeat("x", MaxWorkflowJobsEditorStringBytes+1)},
		{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "TRUE"},
		{Kind: yaml.ScalarNode, Tag: "!!int", Value: "9007199254740992"},
		{Kind: yaml.ScalarNode, Tag: "!!float", Value: ".nan"},
		{Kind: yaml.ScalarNode, Tag: "!!timestamp", Value: "2026-08-18"},
		{Kind: yaml.SequenceNode, Tag: "!custom"},
		{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{str("key")}},
		{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			str("same"), str("one"), str("same"), str("two"),
		}},
		{Kind: yaml.DocumentNode, Content: []*yaml.Node{str("value")}},
	}
	for index, node := range cases {
		state := workflowJobsJSONState{}
		if _, reason := state.project(node, 0); reason == "" {
			t.Fatalf("unsafe JSON node %d was accepted: %#v", index, node)
		}
	}
}

func TestWorkflowJobsMutationValidationRejectsEveryUnsafeFieldClass(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value any
		step  bool
	}{
		{name: "ID type", field: "id", value: true},
		{name: "ID multiline", field: "id", value: "bad\nid"},
		{name: "name type", field: "name", value: true},
		{name: "uses multiline", field: "uses", value: "bad\ntarget"},
		{name: "step target", field: "uses", value: "not-a-target", step: true},
		{name: "workflow target", field: "uses", value: "workflows/../bad.yml"},
		{name: "boolean", field: "continue_on_error", value: "true"},
		{name: "step needs", field: "needs", value: []string{"main"}, step: true},
		{name: "needs type", field: "needs", value: []any{"main"}},
		{name: "needs item", field: "needs", value: []string{" bad "}},
		{name: "with type", field: "with", value: "value"},
		{name: "with non-JSON", field: "with", value: map[string]any{"bad": make(chan int)}},
		{name: "step secrets", field: "secrets", value: "inherit", step: true},
		{name: "secret scalar", field: "secrets", value: "all"},
		{name: "secret type", field: "secrets", value: []string{"secret"}},
		{name: "step outputs", field: "outputs", value: map[string]string{}, step: true},
		{name: "outputs type", field: "outputs", value: map[string]any{}},
		{name: "output key", field: "outputs", value: map[string]string{"": "value"}},
		{name: "context type", field: "context", value: map[string]any{}},
		{name: "context key", field: "context", value: map[string]string{"unknown": "value"}},
		{name: "unknown field", field: "unknown", value: "value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateWorkflowEditorMutationValue(test.field, test.value, test.step); err == nil {
				t.Fatal("unsafe mutation value was accepted")
			}
		})
	}
}

func TestWorkflowJobsJSONMutationValidationRejectsLimits(t *testing.T) {
	deep := any("leaf")
	for depth := 0; depth <= MaxWorkflowJobsEditorJSONDepth; depth++ {
		deep = []any{deep}
	}
	large := make([]any, MaxWorkflowJobsEditorJSONEntries+1)
	for index := range large {
		large[index] = true
	}
	for _, value := range []any{
		make(chan int),
		strings.Repeat("x", MaxWorkflowJobsEditorStringBytes+1),
		int64(9007199254740992),
		float64(math.Inf(1)),
		map[string]any{"": "value"},
		deep,
		large,
	} {
		if err := validateWorkflowJobsJSONValue(value); err == nil {
			t.Fatalf("unsafe JSON mutation %#v was accepted", value)
		}
	}
}

func TestWorkflowJobsOperationsRejectMissingAndUnsafeTargets(t *testing.T) {
	root := func(raw string) *yaml.Node {
		t.Helper()
		document, err := decodeWorkflowEditorDocument(raw)
		if err != nil {
			t.Fatal(err)
		}
		value, reason := editableWorkflowRoot(document)
		if reason != "" {
			t.Fatal(reason)
		}
		return value
	}
	base := "name: test\njobs:\n  main:\n    runs-on: picoclaw\n    steps:\n      - id: first\n        uses: function/first\n      - id: second\n        uses: function/second\n  other:\n    runs-on: picoclaw\n"

	operations := []WorkflowJobsOperation{
		unsupportedWorkflowJobsCoverageOperation{},
		WorkflowJobInsertOperation{JobID: "", Index: 0},
		WorkflowJobInsertOperation{JobID: "main", Index: 0},
		WorkflowJobInsertOperation{JobID: "other", Index: -1},
		WorkflowJobInsertOperation{JobID: "other", Index: 99},
		WorkflowJobDeleteOperation{JobID: "missing"},
		WorkflowJobPatchOperation{JobID: "missing"},
		WorkflowJobPatchOperation{JobID: "main", NewJobID: pointerToWorkflowCoverageString(" bad ")},
		WorkflowJobPatchOperation{JobID: "main", NewJobID: pointerToWorkflowCoverageString("other")},
		WorkflowStepInsertOperation{JobID: "missing", Index: 0},
		WorkflowStepInsertOperation{JobID: "main", Index: -1},
		WorkflowStepInsertOperation{JobID: "main", Index: 99},
		WorkflowStepDeleteOperation{JobID: "missing", StepIndex: 0},
		WorkflowStepDeleteOperation{JobID: "main", StepIndex: -1},
		WorkflowStepDeleteOperation{JobID: "main", StepIndex: 99},
		WorkflowStepMoveOperation{JobID: "missing", StepIndex: 0, ToIndex: 1},
		WorkflowStepMoveOperation{JobID: "main", StepIndex: -1, ToIndex: 1},
		WorkflowStepMoveOperation{JobID: "main", StepIndex: 0, ToIndex: 99},
		WorkflowStepPatchOperation{JobID: "missing", StepIndex: 0},
		WorkflowStepPatchOperation{JobID: "main", StepIndex: -1},
		WorkflowStepPatchOperation{JobID: "main", StepIndex: 99},
	}
	for index, operation := range operations {
		if _, err := applyWorkflowJobsOperation(root(base), operation); err == nil {
			t.Fatalf("invalid operation %d (%T) was accepted", index, operation)
		}
	}

	moveRoot := root(base)
	changed, err := applyWorkflowStepMove(moveRoot, WorkflowStepMoveOperation{
		JobID: "main", StepIndex: 1, ToIndex: 0,
	})
	if err != nil || !changed {
		t.Fatalf("backward step move = (%t, %v)", changed, err)
	}
	unchanged, err := applyWorkflowStepMove(moveRoot, WorkflowStepMoveOperation{
		JobID: "main", StepIndex: 0, ToIndex: 0,
	})
	if err != nil || unchanged {
		t.Fatalf("no-op step move = (%t, %v)", unchanged, err)
	}
}

func TestWorkflowJobsFieldMutationRejectsInvalidModesAndMappings(t *testing.T) {
	scalar := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "not-map"}
	if _, err := applyWorkflowEditorFields(
		scalar, WorkflowEditorFieldMutations{}, workflowJobEditorYAMLFields, false, false,
	); !errors.Is(err, ErrWorkflowJobsNotEditable) {
		t.Fatalf("scalar mutation error = %v", err)
	}
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	tests := []WorkflowEditorFieldMutations{
		{"name": {Mode: WorkflowEditorMutationRemove}},
		{"name": {Mode: WorkflowEditorMutationRemove, Value: "unexpected"}},
		{"name": {Mode: "unsupported", Value: "value"}},
		{"unknown": {Mode: WorkflowEditorMutationSet, Value: "value"}},
	}
	for index, mutations := range tests {
		insert := index == 0
		if _, err := applyWorkflowEditorFields(
			mapping, mutations, workflowJobEditorYAMLFields, false, insert,
		); err == nil {
			t.Fatalf("invalid field mutation %d was accepted", index)
		}
	}

	duplicateJobs := workflowCoverageRoot(t, "jobs: {}\njobs: {}\n")
	if _, err := ensureWorkflowJobsMapping(duplicateJobs); !errors.Is(err, ErrWorkflowJobsNotEditable) {
		t.Fatalf("duplicate jobs ensure error = %v", err)
	}
	nonMappingJobs := workflowCoverageRoot(t, "jobs: []\n")
	if _, err := existingWorkflowJobsMapping(nonMappingJobs); !errors.Is(err, ErrWorkflowJobsNotEditable) {
		t.Fatalf("non-mapping jobs error = %v", err)
	}
	withoutJobs := workflowCoverageRoot(t, "name: test\n")
	if job, err := workflowJobsTargetJob(withoutJobs, "main"); job != nil || !errors.Is(err, ErrWorkflowJobsTarget) {
		t.Fatalf("missing target job = (%#v, %v)", job, err)
	}
	jobWithoutSteps := workflowCoverageRoot(t, "jobs:\n  main: {runs-on: picoclaw}\n")
	if _, err := workflowJobsTargetSteps(jobWithoutSteps, "main"); !errors.Is(err, ErrWorkflowJobsTarget) {
		t.Fatalf("missing target steps error = %v", err)
	}
	jobWithBadSteps := workflowCoverageRoot(t, "jobs:\n  main:\n    steps: {}\n")
	if _, err := workflowJobsTargetSteps(jobWithBadSteps, "main"); !errors.Is(err, ErrWorkflowJobsNotEditable) {
		t.Fatalf("invalid target steps error = %v", err)
	}
}

func workflowCoverageRoot(t *testing.T, raw string) *yaml.Node {
	t.Helper()
	document, err := decodeWorkflowEditorDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	root, reason := editableWorkflowRoot(document)
	if reason != "" {
		t.Fatal(reason)
	}
	return root
}

func pointerToWorkflowCoverageString(value string) *string {
	return &value
}

func workflowCoverageDecimal(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [24]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}
