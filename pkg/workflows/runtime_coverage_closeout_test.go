package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowCoverageOffsetsExercisePublicRuntimeContracts(t *testing.T) {
	contract := &AgentOutputContract{Format: "json", Schema: map[string]any{
		"type": "object", "properties": map[string]any{"summary": map[string]any{"type": "string"}},
	}}
	instruction := contract.Instruction()
	if !strings.Contains(instruction, "Return only valid JSON") || !strings.Contains(instruction, `"summary"`) {
		t.Fatalf("structured instruction = %q", instruction)
	}
	if got := (*AgentOutputContract)(nil).Instruction(); got != "" {
		t.Fatalf("nil structured instruction = %q", got)
	}
	badSchema := &AgentOutputContract{Format: "json", Schema: map[string]any{"bad": make(chan int)}}
	if got := badSchema.Instruction(); !strings.Contains(got, "Structured output contract") {
		t.Fatalf("unserializable-schema instruction = %q", got)
	}
	for _, test := range []struct {
		value any
		want  int
	}{
		{value: nil, want: 0},
		{value: "", want: 0},
		{value: "12345", want: 2},
		{value: map[string]any{"a": "b"}, want: 3},
	} {
		if got := EstimateAgentPayloadTokens(test.value); got != test.want {
			t.Fatalf("EstimateAgentPayloadTokens(%#v) = %d, want %d", test.value, got, test.want)
		}
	}
	if got := EstimateAgentPayloadTokens(make(chan int)); got == 0 {
		t.Fatal("fallback payload token estimate was empty")
	}

	registry := NewFunctionRegistry()
	if err := registry.Register(" ", func(context.Context, map[string]any, ExecutionContext) (map[string]any, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("blank workflow function name was accepted")
	}
	if err := registry.Register("named", nil); err == nil {
		t.Fatal("nil workflow function was accepted")
	}
	if err := registry.Register("named", func(
		_ context.Context, args map[string]any, _ ExecutionContext,
	) (map[string]any, error) {
		return map[string]any{"value": args["value"]}, nil
	}); err != nil {
		t.Fatal(err)
	}
	output, err := registry.RunFunction(
		context.Background(), "named", map[string]any{"value": "ok"}, ExecutionContext{},
	)
	if err != nil || output["value"] != "ok" {
		t.Fatalf("registered workflow function = (%#v, %v)", output, err)
	}
	if _, err := registry.RunFunction(context.Background(), "missing", nil, ExecutionContext{}); err == nil {
		t.Fatal("missing workflow function was executed")
	}
	var nilRegistry *FunctionRegistry
	if _, err := nilRegistry.RunFunction(context.Background(), "named", nil, ExecutionContext{}); err == nil {
		t.Fatal("nil workflow function registry was executed")
	}

	if _, err := ValidateGateFieldValues([]GateField{{
		ID: "approved", Type: GateFieldBoolean, Required: true,
	}}, map[string]any{"approved": true}); err != nil {
		t.Fatalf("public gate field validation failed: %v", err)
	}
	if (workflowWaitingError{}).Error() != "workflow is waiting for human input" {
		t.Fatal("workflow waiting error text changed")
	}
}

func TestReloadLocalReportsLoadAndValidationFailures(t *testing.T) {
	workspace := t.TempDir()
	definitions := filepath.Join(workspace, DefaultDefinitionsDir)
	if err := os.MkdirAll(definitions, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(definitions, "valid.yml"), `
name: Valid
on:
  manual: {}
jobs:
  run:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with:
          action: list
`)
	writeTestFile(t, filepath.Join(definitions, "broken-yaml.yml"), "name: [\n")
	writeTestFile(t, filepath.Join(definitions, "invalid.yml"), `
name: Invalid
on:
  manual: {}
jobs: {}
`)
	result, reloadErr := ReloadLocal(context.Background(), workspace)
	if reloadErr != nil {
		t.Fatal(reloadErr)
	}
	if len(result.Workflows) != 3 || len(result.Errors) != 2 || result.ReloadedAt.IsZero() {
		t.Fatalf("reload result = %#v", result)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReloadLocal(canceled, workspace); err == nil {
		t.Fatal("canceled workflow reload succeeded")
	}

	if _, err := InstallWorkflowTemplate(
		context.Background(), workspace, "unknown-template", false,
	); err == nil || !strings.Contains(err.Error(), "unknown workflow template") {
		t.Fatalf("unknown template error = %v", err)
	}
	installed, installErr := InstallWorkflowTemplate(
		context.Background(), workspace, RepositoryBugFinderWorkflowName, false,
	)
	if installErr != nil || installed == nil || !installed.Installed {
		t.Fatalf("generic template install = (%#v, %v)", installed, installErr)
	}
}

func TestWorkflowAuthoringScalarRuntimeContracts(t *testing.T) {
	marshalCases := []struct {
		name  string
		value WorkflowAuthoringScalar
		want  string
	}{
		{name: "null", value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarNull}, want: "null"},
		{
			name:  "string",
			value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarString, text: "safe"},
			want:  `"safe"`,
		},
		{
			name:  "number",
			value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarNumber, number: "12.5"},
			want:  "12.5",
		},
		{
			name:  "unsafe number",
			value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarNumber, number: "not-a-number"},
			want:  "null",
		},
		{
			name:  "boolean",
			value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarBoolean, boolean: true},
			want:  "true",
		},
		{name: "unknown", value: WorkflowAuthoringScalar{kind: workflowAuthoringScalarKind(99)}, want: "null"},
	}
	for _, test := range marshalCases {
		t.Run(test.name, func(t *testing.T) {
			encoded, marshalErr := test.value.MarshalJSON()
			if marshalErr != nil || string(encoded) != test.want {
				t.Fatalf("MarshalJSON() = (%s, %v), want %s", encoded, marshalErr, test.want)
			}
		})
	}

	if unmarshalErr := (*WorkflowAuthoringScalar)(nil).UnmarshalJSON([]byte("null")); unmarshalErr == nil {
		t.Fatal("nil workflow authoring scalar destination was accepted")
	}
	unmarshalCases := []struct {
		name string
		raw  string
		kind workflowAuthoringScalarKind
	}{
		{name: "null", raw: "null", kind: workflowAuthoringScalarNull},
		{name: "true", raw: "true", kind: workflowAuthoringScalarBoolean},
		{name: "false", raw: "false", kind: workflowAuthoringScalarBoolean},
		{name: "string", raw: `"safe"`, kind: workflowAuthoringScalarString},
		{name: "number", raw: "42.5", kind: workflowAuthoringScalarNumber},
	}
	for _, test := range unmarshalCases {
		t.Run(test.name, func(t *testing.T) {
			var value WorkflowAuthoringScalar
			if unmarshalErr := value.UnmarshalJSON([]byte(test.raw)); unmarshalErr != nil || value.kind != test.kind {
				t.Fatalf("UnmarshalJSON(%s) = (%#v, %v)", test.raw, value, unmarshalErr)
			}
		})
	}
	for _, raw := range [][]byte{[]byte(`"unterminated`), []byte("not-a-number")} {
		var value WorkflowAuthoringScalar
		if unmarshalErr := value.UnmarshalJSON(raw); unmarshalErr == nil {
			t.Fatalf("invalid authoring scalar %q was accepted as %#v", raw, value)
		}
	}

	validScalars := []WorkflowAuthoringScalar{
		{kind: workflowAuthoringScalarNull},
		{kind: workflowAuthoringScalarBoolean, boolean: true},
		{kind: workflowAuthoringScalarString, text: "safe"},
		{kind: workflowAuthoringScalarNumber, number: "1"},
	}
	for _, value := range validScalars {
		if !validWorkflowAuthoringScalar(value) {
			t.Fatalf("valid authoring scalar was rejected: %#v", value)
		}
	}
	for _, value := range []WorkflowAuthoringScalar{
		{kind: workflowAuthoringScalarString, text: "bad\x00text"},
		{kind: workflowAuthoringScalarNumber, number: "not-a-number"},
		{kind: workflowAuthoringScalarKind(99)},
	} {
		if validWorkflowAuthoringScalar(value) {
			t.Fatalf("invalid authoring scalar was accepted: %#v", value)
		}
	}
	if !validWorkflowAuthoringReadiness(WorkflowDependencyReadinessReady) ||
		validWorkflowAuthoringReadiness(WorkflowDependencyReadinessCode("unknown")) {
		t.Fatal("workflow authoring readiness classification mismatch")
	}
	if err := validateWorkflowAuthoringLimits([]WorkflowAuthoringLimitCode{
		WorkflowAuthoringAgentsTruncated,
	}); err != nil {
		t.Fatalf("valid workflow authoring limits failed: %v", err)
	}
	if err := validateWorkflowAuthoringLimits([]WorkflowAuthoringLimitCode{"unknown"}); err == nil {
		t.Fatal("unknown workflow authoring limit was accepted")
	}
	if err := validateWorkflowAuthoringLimits([]WorkflowAuthoringLimitCode{
		WorkflowAuthoringAgentsTruncated, WorkflowAuthoringAgentsTruncated,
	}); err == nil {
		t.Fatal("unsorted workflow authoring limits were accepted")
	}
	if !workflowAuthoringLimitPresent(
		[]WorkflowAuthoringLimitCode{WorkflowAuthoringAgentsTruncated},
		WorkflowAuthoringAgentsTruncated,
	) || workflowAuthoringLimitPresent(nil, WorkflowAuthoringAgentsTruncated) {
		t.Fatal("workflow authoring limit lookup mismatch")
	}

	var decoded WorkflowAuthoringScalar
	if err := json.Unmarshal([]byte(`"round-trip"`), &decoded); err != nil {
		t.Fatalf("JSON scalar round-trip failed: %v", err)
	}
}

func TestWorkflowMutationAndDevelopmentCloseoutContracts(t *testing.T) {
	workspace := t.TempDir()
	if err := WithWorkflowMutationLockAndDevelopmentSession(workspace, nil); err == nil {
		t.Fatal("nil development-session mutation was accepted")
	}
	called := false
	if err := WithWorkflowMutationLockAndDevelopmentSession(
		workspace,
		func(session *WorkflowDevelopmentSession) error {
			called = true
			if session != nil {
				t.Fatalf("unexpected active development session: %#v", session)
			}
			return nil
		},
	); err != nil || !called {
		t.Fatalf("development-session mutation = (called=%t, err=%v)", called, err)
	}

	session, startErr := StartWorkflowDevelopment(
		context.Background(),
		workspace,
		RuntimeCompatibility{},
		WorkflowDevelopmentStartRequest{Prompt: "review events", TargetRef: "workflows/review-events.yml"},
	)
	if startErr != nil {
		t.Fatal(startErr)
	}
	discarded, discardErr := DiscardWorkflowDevelopment(workspace)
	if discardErr != nil || discarded.ID != session.ID {
		t.Fatalf("discarded development = (%#v, %v)", discarded, discardErr)
	}
	active, activeErr := GetWorkflowDevelopmentSession(workspace)
	if activeErr != nil || active != nil {
		t.Fatalf("active development after discard = (%#v, %v)", active, activeErr)
	}
	if _, discardErr := DiscardWorkflowDevelopment(workspace); !errors.Is(discardErr, ErrNoActiveDevelopment) {
		t.Fatalf("second discard error = %v", discardErr)
	}

	loader := ReusableWorkflowLoaderFunc(func(_ context.Context, ref string) (*Workflow, error) {
		return &Workflow{Name: ref}, nil
	})
	loaded, loadErr := loader.LoadReusableWorkflow(context.Background(), "workflows/reusable.yml")
	if loadErr != nil || loaded.Name != "workflows/reusable.yml" {
		t.Fatalf("reusable workflow loader = (%#v, %v)", loaded, loadErr)
	}
	if err := ValidatePrivateGateActionWorkflow(nil); err == nil {
		t.Fatal("nil private gate action workflow was accepted")
	}
}
