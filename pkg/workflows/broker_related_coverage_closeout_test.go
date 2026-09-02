//nolint:govet // Independent validation assertions intentionally reuse err.
package workflows

import (
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestWorkflowInspectionValidationCodeAndScopeMatrix(t *testing.T) {
	codeCases := []struct {
		path, message string
		want          WorkflowDefinitionValidationCode
	}{
		{"jobs", "at least one job is required", WorkflowDefinitionValidationJobsRequired},
		{"on.schedule[0]", "cron is required", WorkflowDefinitionValidationScheduleCronRequired},
		{"on.schedule[0]", "invalid cron expression", WorkflowDefinitionValidationScheduleCronInvalid},
		{"on.workflow_call.inputs.name", "input name is required", WorkflowDefinitionValidationInputNameRequired},
		{"on.command.args.name", "unsupported input type", WorkflowDefinitionValidationInputTypeUnsupported},
		{"on.workflow_call.inputs.name.default", "bad", WorkflowDefinitionValidationInputDefaultInvalid},
		{"on.workflow_call.outputs.value", "output value is required", WorkflowDefinitionValidationOutputRequired},
		{"on.workflow_call.outputs.value", "bad", WorkflowDefinitionValidationOutputExpressionInvalid},
		{"on.channel_message[0].text_matches", "bad", WorkflowDefinitionValidationChannelPatternInvalid},
		{"on.command[0].name", "bad", WorkflowDefinitionValidationCommandNameRequired},
		{"on.runtime_event[0]", "at least one filter is required", WorkflowDefinitionValidationRuntimeFilterRequired},
		{"on.event[0]", "at least one filter is required", WorkflowDefinitionValidationEventFilterRequired},
		{
			"on.event[0]",
			"at least one entity filter is required",
			WorkflowDefinitionValidationEventEntityFilterRequired,
		},
		{"on.event[0]", "pattern is required", WorkflowDefinitionValidationEventPatternRequired},
		{"on.event[0]", "attribute name is required", WorkflowDefinitionValidationEventAttributeRequired},
		{"jobs[0]", "job id is required", WorkflowDefinitionValidationJobIDRequired},
		{"jobs.main", "unknown dependency other", WorkflowDefinitionValidationJobDependencyUnknown},
		{"jobs.main", "dependency cycle detected", WorkflowDefinitionValidationJobDependencyCycle},
		{"jobs.main.uses", "bad", WorkflowDefinitionValidationReusableTargetInvalid},
		{
			"jobs.main",
			"reusable workflow jobs cannot define steps",
			WorkflowDefinitionValidationReusableStepsUnsupported,
		},
		{"jobs.main", "runs-on is required for step jobs", WorkflowDefinitionValidationJobRunnerRequired},
		{"jobs.main", "at least one step is required", WorkflowDefinitionValidationJobStepsRequired},
		{"jobs.main.steps[1]", "duplicate step id", WorkflowDefinitionValidationStepIDDuplicate},
		{"jobs.main.steps[1]", "uses is required", WorkflowDefinitionValidationStepTargetRequired},
		{
			"jobs.main.steps[1]",
			"reusable workflows are only supported at job level",
			WorkflowDefinitionValidationReusableStepUnsupported,
		},
		{"jobs.main.steps[1]", "unsupported uses target", WorkflowDefinitionValidationStepTargetUnsupported},
		{"jobs.main.conversation.session", "bad", WorkflowDefinitionValidationConversationSession},
		{"jobs.main.conversation.delivery", "bad", WorkflowDefinitionValidationConversationDelivery},
		{"jobs.main.context.session", "bad", WorkflowDefinitionValidationRunSessionUnsupported},
		{"jobs.main.context.delivery", "bad", WorkflowDefinitionValidationRunDeliveryUnsupported},
		{"jobs.main.with.history", "bad", WorkflowDefinitionValidationAgentHistoryUnsupported},
		{"jobs.main.with.cache", "bad", WorkflowDefinitionValidationAgentCacheUnsupported},
		{"jobs.main.with.tools", "bad", WorkflowDefinitionValidationAgentToolsUnsupported},
		{"other", "bad", WorkflowDefinitionValidationDefinitionInvalid},
	}
	for _, test := range codeCases {
		got := workflowInspectionValidationCode(ValidationError{Path: test.path, Message: test.message})
		if got != test.want {
			t.Errorf("code(%q, %q) = %q, want %q", test.path, test.message, got, test.want)
		}
	}
	scopeCases := map[string]WorkflowDefinitionValidationScope{
		"on.manual":          WorkflowDefinitionValidationScopeManual,
		"on.schedule":        WorkflowDefinitionValidationScopeSchedule,
		"on.channel_message": WorkflowDefinitionValidationScopeChannelMessage,
		"on.command":         WorkflowDefinitionValidationScopeCommand,
		"on.runtime_event":   WorkflowDefinitionValidationScopeRuntimeEvent,
		"on.event":           WorkflowDefinitionValidationScopeEvent,
		"on.workflow_call":   WorkflowDefinitionValidationScopeWorkflowCall,
		"jobs.main":          WorkflowDefinitionValidationScopeJobs,
		"other":              WorkflowDefinitionValidationScopeWorkflow,
	}
	for path, want := range scopeCases {
		if got := workflowInspectionValidationScope(path); got != want {
			t.Errorf("scope(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestWorkflowHumanResponseSchemaValidationMatrix(t *testing.T) {
	valid := []map[string]any{
		{},
		{"type": "string", "enum": []string{"a"}},
		{"type": "object", "required": []any{"name"}, "properties": map[string]any{
			"name": map[string]any{"type": "string"},
		}, "additionalProperties": false},
		{"type": "array", "items": map[string]any{"type": "integer"}},
	}
	for _, schema := range valid {
		if err := validateHumanTaskResponseSchema(schema, 0); err != nil {
			t.Errorf("valid schema %#v error = %v", schema, err)
		}
	}
	invalid := []map[string]any{
		{"unknown": true},
		{"type": 1},
		{"type": "unsupported"},
		{"enum": []any{}},
		{"required": []string{"name"}},
		{"type": "object", "required": "name"},
		{"type": "object", "required": []any{1}},
		{"type": "object", "required": []string{"", "name"}},
		{"type": "object", "required": []string{"name", "name"}},
		{"properties": map[string]any{}},
		{"type": "object", "properties": "bad"},
		{"type": "object", "properties": map[string]any{"": map[string]any{}}},
		{"type": "object", "properties": map[string]any{"name": "bad"}},
		{"additionalProperties": false},
		{"type": "object", "additionalProperties": "bad"},
		{"items": map[string]any{}},
		{"type": "array", "items": "bad"},
	}
	for _, schema := range invalid {
		if err := validateHumanTaskResponseSchema(schema, 0); err == nil {
			t.Errorf("invalid schema accepted: %#v", schema)
		}
	}
	if err := validateHumanTaskResponseSchema(map[string]any{}, 33); err == nil {
		t.Fatal("over-depth schema accepted")
	}
}

func TestWorkflowJSONNodeAndExpressionBoundaries(t *testing.T) {
	parse := func(raw string) *yaml.Node {
		t.Helper()
		var document yaml.Node
		if err := yaml.Unmarshal([]byte(raw), &document); err != nil {
			t.Fatal(err)
		}
		return document.Content[0]
	}
	cases := []struct {
		node   *yaml.Node
		nested bool
		valid  bool
	}{
		{nil, false, false},
		{parse("null"), false, false},
		{parse("null"), true, true},
		{parse(`"text"`), false, true},
		{parse(`"line\\nbreak"`), false, true},
		{parse("true"), false, true},
		{parse("TRUE"), false, false},
		{parse("9007199254740992"), false, false},
		{parse("[one, null, 2]"), false, true},
		{parse("{name: value}"), false, true},
		{&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "one"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"},
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "two"},
		}}, false, false},
		{&yaml.Node{Kind: yaml.AliasNode}, false, false},
	}
	for index, test := range cases {
		reason := workflowJSONValueNodeReason(test.node, test.nested)
		if (reason == "") != test.valid {
			t.Errorf("JSON node %d reason = %q, valid=%v", index, reason, test.valid)
		}
	}
	expression := `visible == "hidden.value" && path == 'escaped value' && secrets.token`
	stripped := stripExpressionStringLiterals(expression)
	if strings.Contains(stripped, "hidden.value") || strings.Contains(stripped, "escaped") ||
		!strings.Contains(stripped, "secrets.token") || len([]rune(stripped)) != len([]rune(expression)) {
		t.Fatalf("stripped expression = %q", stripped)
	}
}

func TestWorkflowAuthoringShapeAndJobsOperationBoundaries(t *testing.T) {
	validNull := WorkflowAuthoringScalar{kind: workflowAuthoringScalarNull}
	validString := WorkflowAuthoringScalar{kind: workflowAuthoringScalarString, text: "value"}
	allowed := true
	valid := &WorkflowAuthoringParameterShape{
		Type: "object",
		Properties: []WorkflowAuthoringParameterProperty{{
			Name: "name", Required: true,
			Shape: WorkflowAuthoringParameterShape{Type: "string", Enum: []WorkflowAuthoringScalar{validString}},
		}},
		AdditionalProperties: &WorkflowAuthoringAdditionalProperties{Allowed: &allowed},
	}
	units := 0
	if err := validateWorkflowAuthoringShape(valid, 1, &units); err != nil || units == 0 {
		t.Fatalf("valid shape = units %d, %v", units, err)
	}
	invalid := []*WorkflowAuthoringParameterShape{
		nil,
		{Type: "bad"},
		{Properties: []WorkflowAuthoringParameterProperty{{Name: ""}}},
		{Properties: []WorkflowAuthoringParameterProperty{{Name: "b"}, {Name: "a"}}},
		{Enum: []WorkflowAuthoringScalar{{kind: 255}}},
		{Enum: []WorkflowAuthoringScalar{validNull, validNull}},
		{AdditionalProperties: &WorkflowAuthoringAdditionalProperties{}},
		{AdditionalProperties: &WorkflowAuthoringAdditionalProperties{
			Allowed: &allowed, Shape: &WorkflowAuthoringParameterShape{},
		}},
	}
	for _, shape := range invalid {
		units := 0
		if err := validateWorkflowAuthoringShape(shape, 1, &units); err == nil {
			t.Errorf("invalid shape accepted: %#v", shape)
		}
	}
	units = 0
	overDepthErr := validateWorkflowAuthoringShape(
		&WorkflowAuthoringParameterShape{}, MaxWorkflowAuthoringShapeDepth+1, &units,
	)
	if overDepthErr == nil {
		t.Fatal("over-depth authoring shape accepted")
	}
	if err := validateWorkflowAuthoringProjectedShape(false, &WorkflowAuthoringParameterShape{}, &units); err == nil {
		t.Fatal("inconsistent projected shape accepted")
	}

	inspection := WorkflowJobsInspection{Jobs: []WorkflowJobProjection{{
		ID: "main", Editable: true, identityEditable: true, stepsContainerEditable: true,
		Steps: []WorkflowStepProjection{{Editable: true}, {Editable: false, Reason: "raw"}},
	}}}
	operations := []struct {
		operation WorkflowJobsOperation
		wantErr   bool
	}{
		{WorkflowJobInsertOperation{JobID: "new"}, false},
		{(*WorkflowJobInsertOperation)(nil), true},
		{WorkflowJobDeleteOperation{JobID: "main"}, true},
		{(*WorkflowJobDeleteOperation)(nil), true},
		{WorkflowJobPatchOperation{JobID: "main"}, false},
		{(*WorkflowJobPatchOperation)(nil), true},
		{WorkflowStepInsertOperation{JobID: "main"}, false},
		{(*WorkflowStepInsertOperation)(nil), true},
		{WorkflowStepDeleteOperation{JobID: "main", StepIndex: 0}, false},
		{(*WorkflowStepDeleteOperation)(nil), true},
		{WorkflowStepMoveOperation{JobID: "main", StepIndex: 0, ToIndex: 1}, true},
		{(*WorkflowStepMoveOperation)(nil), true},
		{WorkflowStepPatchOperation{JobID: "main", StepIndex: 0}, false},
		{(*WorkflowStepPatchOperation)(nil), true},
	}
	for _, test := range operations {
		err := requireWorkflowJobsOperationEditable(inspection, test.operation)
		if (err != nil) != test.wantErr {
			t.Errorf("operation %T error = %v, wantErr=%v", test.operation, err, test.wantErr)
		}
	}
	pointerOperations := []WorkflowJobsOperation{
		(*WorkflowJobInsertOperation)(nil), (*WorkflowJobDeleteOperation)(nil),
		(*WorkflowJobPatchOperation)(nil), (*WorkflowStepInsertOperation)(nil),
		(*WorkflowStepDeleteOperation)(nil), (*WorkflowStepMoveOperation)(nil),
		(*WorkflowStepPatchOperation)(nil),
	}
	for _, operation := range pointerOperations {
		if _, err := applyWorkflowJobsOperation(nil, operation); !errors.Is(err, ErrWorkflowJobsOperation) {
			t.Errorf("nil pointer operation %T error = %v", operation, err)
		}
	}
}

func TestWorkflowRunWireExactFieldDecodeBoundaries(t *testing.T) {
	valid := []string{
		`{"id":"wr_decode","workflow_ref":"workflows/x.yml","status":"running","created_at":"2026-09-02T12:00:00Z","event":{"large":9007199254740993},"inputs":{"event":{"nested":true}},"execution":{},"human_tasks":{}}`,
		`{"id":"wr_decode_null","workflow_ref":"workflows/x.yml","status":"running","created_at":"2026-09-02T12:00:00Z","execution":null,"human_tasks":null,"private_context":null}`,
	}
	for _, raw := range valid {
		if run, fields, err := decodeRunWithExactEventFields([]byte(raw)); err != nil || run == nil ||
			strings.Contains(raw, `"event"`) && !fields.hasEvent {
			t.Errorf("decode exact run = %#v/%#v/%v", run, fields, err)
		}
	}
	invalid := []string{
		`[]`,
		`{"inputs":[]}`,
		`{"execution":"bad"}`,
		`{"human_tasks":"bad"}`,
		`{"private_context":{"unknown":true}}`,
	}
	for _, raw := range invalid {
		if _, _, err := decodeRunWithExactEventFields([]byte(raw)); err == nil {
			t.Errorf("invalid exact run accepted: %s", raw)
		}
	}
}
