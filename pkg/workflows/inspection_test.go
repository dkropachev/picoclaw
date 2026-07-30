package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInspectWorkflowDefinitionBytesProjectsSafeDefinition(t *testing.T) {
	t.Parallel()
	raw := []byte(`
name: CANARY_WORKFLOW_NAME
on:
  manual: {}
  schedule:
    - cron: "0 * * * *"
  channel_message:
    channels: [telegram]
    chats: [chat-1]
    senders: [sender-1]
    mentioned: false
    command: review
    text_matches: "^review"
    passthrough: true
    conversation:
      session: CANARY_CHANNEL_SESSION
      delivery: CANARY_CHANNEL_DELIVERY
  command:
    name: deploy
    channels: [discord]
    chats: [chat-2]
    senders: [sender-2]
    args:
      environment:
        type: string
        required: true
        default: CANARY_COMMAND_DEFAULT
    passthrough: false
    conversation:
      session: CANARY_COMMAND_SESSION
      delivery: CANARY_COMMAND_DELIVERY
  runtime_event:
    kinds: [agent.completed]
    sources: [agent/main]
    agents: [main]
    sessions: [CANARY_RUNTIME_SESSION, CANARY_RUNTIME_SESSION_TWO]
    channels: [telegram]
    chats: [chat-3]
  event:
    sources: [github]
    connectors: [github-primary]
    types: [issues.opened]
    actor:
      ids: [user-1]
    subject:
      types: [issue]
    attributes:
      repository: [owner/repository]
  workflow_call:
    inputs:
      request:
        type: string
        required: true
        default: CANARY_CALL_DEFAULT
    secrets:
      DEPLOY_TOKEN:
        required: true
    outputs:
      public_result:
        value: "${{ jobs.alpha.outputs.CANARY_OUTPUT_EXPRESSION }}"
jobs:
  reuse:
    uses: workflows/child.yml
    with:
      request: CANARY_REUSABLE_WITH
    secrets:
      DEPLOY_TOKEN: CANARY_REUSABLE_SECRET_VALUE
    context:
      session: key:CANARY_REUSABLE_SESSION
      delivery: none
  alpha:
    name: CANARY_JOB_NAME
    runs-on: CANARY_RUNNER_PATH
    if: "${{ inputs.CANARY_JOB_CONDITION }}"
    outputs:
      CANARY_OUTPUT_EXPRESSION: "${{ steps.classify.outputs.value }}"
    context:
      session: key:CANARY_JOB_SESSION
      delivery: none
    steps:
      - id: classify
        name: CANARY_STEP_NAME
        uses: agent/main
        if: "${{ inputs.CANARY_STEP_CONDITION }}"
        with:
          prompt: CANARY_PROMPT_VALUE
          token: CANARY_WITH_SECRET_VALUE
        context:
          session: key:CANARY_STEP_SESSION
          delivery: none
`)
	source := WorkflowDefinitionInspectionSource{
		Kind:         WorkflowDefinitionInspectionSourceTemplate,
		TemplateName: "unit-test",
	}
	inspection, err := InspectWorkflowDefinitionBytes(source, raw)
	if err != nil {
		t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
	}
	if inspection.Source.Kind != WorkflowDefinitionInspectionSourceTemplate ||
		inspection.Source.TemplateName != "unit-test" ||
		inspection.Source.Ref != "" {
		t.Fatalf("source = %#v", inspection.Source)
	}
	if inspection.Revision != workflowContentRevision(raw) {
		t.Fatalf("revision = %q, want exact content revision", inspection.Revision)
	}
	if !inspection.Complete || len(inspection.Limits) != 0 {
		t.Fatalf("completeness = %v, limits = %v", inspection.Complete, inspection.Limits)
	}
	if inspection.Validation.Valid {
		t.Fatal("validation unexpectedly valid with canary conversation modes")
	}
	if len(inspection.Triggers) != len(workflowTriggerKinds) {
		t.Fatalf("trigger count = %d", len(inspection.Triggers))
	}
	for _, kind := range workflowTriggerKinds {
		trigger := inspection.Triggers[kind]
		if !trigger.Present || !trigger.Projected || trigger.Value == nil {
			t.Fatalf("trigger %s = %#v", kind, trigger)
		}
	}

	channel, ok := inspection.Triggers[WorkflowTriggerChannelMessage].
		Value.(WorkflowDefinitionChannelTriggerInspection)
	if !ok {
		t.Fatalf("channel value type = %T", inspection.Triggers[WorkflowTriggerChannelMessage].Value)
	}
	if !channel.SessionConfigured || !channel.DeliveryConfigured ||
		channel.Mentioned == nil || *channel.Mentioned {
		t.Fatalf("channel projection = %#v", channel)
	}
	command, ok := inspection.Triggers[WorkflowTriggerCommand].
		Value.(WorkflowDefinitionCommandTriggerInspection)
	if !ok {
		t.Fatalf("command value type = %T", inspection.Triggers[WorkflowTriggerCommand].Value)
	}
	if !command.SessionConfigured || !command.DeliveryConfigured {
		t.Fatalf("command projection = %#v", command)
	}
	if arg := command.Args["environment"]; !arg.Required || !arg.HasDefault || arg.Type != "string" {
		t.Fatalf("command arg = %#v", arg)
	}
	runtimeTrigger, ok := inspection.Triggers[WorkflowTriggerRuntimeEvent].
		Value.(WorkflowDefinitionRuntimeEventTriggerInspection)
	if !ok {
		t.Fatalf("runtime value type = %T", inspection.Triggers[WorkflowTriggerRuntimeEvent].Value)
	}
	if !runtimeTrigger.SessionFilterPresent || runtimeTrigger.SessionFilterCount != 2 {
		t.Fatalf("runtime projection = %#v", runtimeTrigger)
	}
	call, ok := inspection.Triggers[WorkflowTriggerWorkflowCall].
		Value.(WorkflowDefinitionWorkflowCallInspection)
	if !ok {
		t.Fatalf("workflow-call value type = %T", inspection.Triggers[WorkflowTriggerWorkflowCall].Value)
	}
	if input := call.Inputs["request"]; !input.Required || !input.HasDefault || input.Type != "string" {
		t.Fatalf("workflow-call input = %#v", input)
	}
	if secret := call.Secrets["DEPLOY_TOKEN"]; !secret.Required {
		t.Fatalf("workflow-call secret = %#v", secret)
	}
	if len(call.Outputs) != 1 || call.Outputs[0] != "public_result" {
		t.Fatalf("workflow-call outputs = %#v", call.Outputs)
	}

	encoded, err := json.Marshal(inspection)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, canary := range []string{
		"CANARY_WORKFLOW_NAME",
		"CANARY_CHANNEL_SESSION",
		"CANARY_CHANNEL_DELIVERY",
		"CANARY_COMMAND_DEFAULT",
		"CANARY_COMMAND_SESSION",
		"CANARY_COMMAND_DELIVERY",
		"CANARY_RUNTIME_SESSION",
		"CANARY_CALL_DEFAULT",
		"CANARY_OUTPUT_EXPRESSION",
		"CANARY_REUSABLE_WITH",
		"CANARY_REUSABLE_SECRET_VALUE",
		"CANARY_REUSABLE_SESSION",
		"CANARY_JOB_NAME",
		"CANARY_RUNNER_PATH",
		"CANARY_JOB_CONDITION",
		"CANARY_JOB_SESSION",
		"CANARY_STEP_NAME",
		"CANARY_STEP_CONDITION",
		"CANARY_PROMPT_VALUE",
		"CANARY_WITH_SECRET_VALUE",
		"CANARY_STEP_SESSION",
	} {
		if bytes.Contains(encoded, []byte(canary)) {
			t.Fatalf("inspection leaked %q: %s", canary, encoded)
		}
	}
	if !bytes.Contains(encoded, []byte(`"DEPLOY_TOKEN":{"required":true}`)) {
		t.Fatalf("secret declaration absent: %s", encoded)
	}
}

func TestInspectWorkflowDefinitionBytesTopologyDependenciesAndEffects(t *testing.T) {
	t.Parallel()
	raw := []byte(`
on:
  manual: {}
jobs:
  zeta:
    runs-on: picoclaw
    steps:
      - uses: agent/main
  reusable:
    uses: workflows/child.yml
  alpha:
    runs-on: picoclaw
    steps:
      - id: first
        uses: agent/main
      - uses: tool/read_file
      - uses: mcp/github/add_issue_comment
      - uses: function/workflow.state
      - uses: shell/private-action
      - id: missing
`)
	inspection, err := InspectWorkflowDefinitionBytes(workflowInspectionTestSource(), raw)
	if err != nil {
		t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
	}
	if got := workflowInspectionJobIDs(inspection.Jobs); strings.Join(got, ",") != "alpha,reusable,zeta" {
		t.Fatalf("job order = %v", got)
	}
	if inspection.Jobs[0].Kind != WorkflowDefinitionJobSteps ||
		len(inspection.Jobs[0].Steps) != 6 {
		t.Fatalf("alpha topology = %#v", inspection.Jobs[0])
	}
	if inspection.Jobs[0].Steps[0] != (WorkflowDefinitionStepInspection{
		Index: 0, ID: "first", Kind: WorkflowDefinitionStepAgent, Target: "agent/main",
	}) {
		t.Fatalf("first step = %#v", inspection.Jobs[0].Steps[0])
	}
	if inspection.Jobs[0].Steps[4].Kind != WorkflowDefinitionStepUnknown ||
		inspection.Jobs[0].Steps[4].Target != "shell/private-action" {
		t.Fatalf("unknown step = %#v", inspection.Jobs[0].Steps[4])
	}
	if inspection.Jobs[0].Steps[5].Kind != WorkflowDefinitionStepUnknown ||
		inspection.Jobs[0].Steps[5].Target != "" {
		t.Fatalf("missing target step = %#v", inspection.Jobs[0].Steps[5])
	}
	if inspection.Jobs[1].Kind != WorkflowDefinitionJobReusable ||
		inspection.Jobs[1].ReusableTarget != "workflows/child.yml" ||
		inspection.Jobs[1].Steps == nil {
		t.Fatalf("reusable topology = %#v", inspection.Jobs[1])
	}

	wantDependencies := []WorkflowDefinitionDependencyInspection{
		{Kind: WorkflowDependencyKindAgent, Target: "main", Occurrences: 2},
		{Kind: WorkflowDependencyKindFunction, Target: "workflow.state", Occurrences: 1},
		{Kind: WorkflowDependencyKindMCP, Target: "github/add_issue_comment", Occurrences: 1},
		{Kind: WorkflowDependencyKindReusable, Target: "workflows/child.yml", Occurrences: 1},
		{Kind: WorkflowDependencyKindTool, Target: "read_file", Occurrences: 1},
	}
	if fmt.Sprint(inspection.Dependencies) != fmt.Sprint(wantDependencies) {
		t.Fatalf("dependencies = %#v, want %#v", inspection.Dependencies, wantDependencies)
	}
	wantEffects := []WorkflowDefinitionEffectInspection{
		{
			Kind: WorkflowDefinitionEffectExternalStateChange, Target: "github/add_issue_comment",
			Occurrences: 1,
		},
		{
			Kind: WorkflowDefinitionEffectModelOrDelegatedAction, Target: "main",
			Occurrences: 2,
		},
		{
			Kind: WorkflowDefinitionEffectStateChange, Target: "read_file",
			Occurrences: 1,
		},
		{
			Kind: WorkflowDefinitionEffectStateChange, Target: "workflow.state",
			Occurrences: 1,
		},
		{
			Kind: WorkflowDefinitionEffectTransitiveUnknown, Target: "workflows/child.yml",
			Occurrences: 1,
		},
		{
			Kind: WorkflowDefinitionEffectUnclassifiedAction, Target: "shell/private-action",
			Occurrences: 1,
		},
	}
	if fmt.Sprint(inspection.Effects) != fmt.Sprint(wantEffects) {
		t.Fatalf("effects = %#v, want %#v", inspection.Effects, wantEffects)
	}
}

func TestInspectWorkflowDefinitionBytesPreservesLongActionTargets(t *testing.T) {
	t.Parallel()
	target := strings.Repeat(
		"x",
		MaxWorkflowInspectionStepTargetBytes-len("function/"),
	)
	raw := []byte(
		"jobs:\n  inspect:\n    runs-on: picoclaw\n    steps:\n" +
			"      - uses: mcp/" + target + "\n",
	)
	inspection, err := InspectWorkflowDefinitionBytes(
		workflowInspectionTestSource(),
		raw,
	)
	if err != nil {
		t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
	}
	if !inspection.Complete || len(inspection.Limits) != 0 {
		t.Fatalf("complete = %v, limits = %v", inspection.Complete, inspection.Limits)
	}
	if len(inspection.Jobs) != 1 ||
		len(inspection.Jobs[0].Steps) != 1 ||
		inspection.Jobs[0].Steps[0].Kind != WorkflowDefinitionStepMCP ||
		inspection.Jobs[0].Steps[0].Target != "mcp/"+target {
		t.Fatalf("jobs = %#v", inspection.Jobs)
	}
	if len(inspection.Dependencies) != 1 ||
		inspection.Dependencies[0] != (WorkflowDefinitionDependencyInspection{
			Kind:        WorkflowDependencyKindMCP,
			Target:      target,
			Occurrences: 1,
		}) {
		t.Fatalf("dependencies = %#v", inspection.Dependencies)
	}
	if len(inspection.Effects) != 1 ||
		inspection.Effects[0] != (WorkflowDefinitionEffectInspection{
			Kind:        WorkflowDefinitionEffectExternalStateChange,
			Target:      target,
			Occurrences: 1,
		}) {
		t.Fatalf("effects = %#v", inspection.Effects)
	}
}

func TestInspectWorkflowDefinitionBytesMarksOverBoundActionTextOmitted(t *testing.T) {
	t.Parallel()
	target := strings.Repeat(
		"x",
		MaxWorkflowInspectionStepTargetBytes-len("function/")+1,
	)
	raw := []byte(
		"jobs:\n  inspect:\n    runs-on: picoclaw\n    steps:\n" +
			"      - uses: mcp/" + target + "\n",
	)
	inspection, err := InspectWorkflowDefinitionBytes(
		workflowInspectionTestSource(),
		raw,
	)
	if err != nil {
		t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
	}
	if inspection.Complete ||
		!workflowInspectionHasLimit(
			inspection,
			WorkflowDefinitionInspectionLimitUnsafeFields,
		) {
		t.Fatalf("complete = %v, limits = %v", inspection.Complete, inspection.Limits)
	}
	if len(inspection.Jobs) != 1 ||
		len(inspection.Jobs[0].Steps) != 1 ||
		inspection.Jobs[0].Steps[0].Kind != WorkflowDefinitionStepMCP ||
		inspection.Jobs[0].Steps[0].Target != "" {
		t.Fatalf("jobs = %#v", inspection.Jobs)
	}
	if len(inspection.Dependencies) != 0 {
		t.Fatalf("dependencies = %#v", inspection.Dependencies)
	}
	if len(inspection.Effects) != 1 ||
		inspection.Effects[0] != (WorkflowDefinitionEffectInspection{
			Kind:        WorkflowDefinitionEffectExternalStateChange,
			Occurrences: 1,
		}) {
		t.Fatalf("effects = %#v", inspection.Effects)
	}
}

func TestInspectWorkflowDefinitionBytesBoundsAliasAmplifiedTopology(t *testing.T) {
	t.Parallel()
	target := "mcp/" + strings.Repeat("x", 800<<10)
	var raw strings.Builder
	raw.Grow(len(target) + 128 + MaxWorkflowInspectionEntries*20)
	raw.WriteString("shared: &huge \"")
	raw.WriteString(target)
	raw.WriteString("\"\njobs:\n  inspect:\n    runs-on: picoclaw\n    steps:\n")
	for range MaxWorkflowInspectionEntries {
		raw.WriteString("      - uses: *huge\n")
	}
	if int64(raw.Len()) > MaxWorkflowInspectionSourceBytes {
		t.Fatalf("test source = %d bytes", raw.Len())
	}
	inspection, err := InspectWorkflowDefinitionBytes(
		workflowInspectionTestSource(),
		[]byte(raw.String()),
	)
	if err != nil {
		t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
	}
	if inspection.Complete ||
		!workflowInspectionHasLimit(
			inspection,
			WorkflowDefinitionInspectionLimitUnsafeFields,
		) ||
		len(inspection.Jobs) != 1 ||
		len(inspection.Jobs[0].Steps) != MaxWorkflowInspectionEntries {
		t.Fatalf(
			"complete = %v, limits = %v, jobs/steps = %d/%d",
			inspection.Complete,
			inspection.Limits,
			len(inspection.Jobs),
			len(inspection.Jobs[0].Steps),
		)
	}
	for _, step := range inspection.Jobs[0].Steps {
		if step.Kind != WorkflowDefinitionStepMCP || step.Target != "" {
			t.Fatalf("unsafe aliased step projected: %#v", step)
		}
	}
	if len(inspection.Dependencies) != 0 ||
		len(inspection.Effects) != 1 ||
		inspection.Effects[0] != (WorkflowDefinitionEffectInspection{
			Kind:        WorkflowDefinitionEffectExternalStateChange,
			Occurrences: MaxWorkflowInspectionEntries,
		}) {
		t.Fatalf(
			"dependencies/effects = %#v / %#v",
			inspection.Dependencies,
			inspection.Effects,
		)
	}
	encoded, marshalErr := json.Marshal(inspection)
	if marshalErr != nil {
		t.Fatalf("json.Marshal() error = %v", marshalErr)
	}
	if len(encoded) > 1<<20 || bytes.Contains(encoded, []byte(target)) {
		t.Fatalf("encoded alias projection is not bounded: %d bytes", len(encoded))
	}
}

func TestInspectWorkflowDefinitionBytesMarksUnsafeActionTextOmitted(t *testing.T) {
	t.Parallel()
	raw := []byte(`
jobs:
  inspect:
    runs-on: picoclaw
    steps:
      - uses: "mcp/github/unsafe\0tool"
      - uses: "mcp/github/unsafe\u202etool"
`)
	inspection, err := InspectWorkflowDefinitionBytes(
		workflowInspectionTestSource(),
		raw,
	)
	if err != nil {
		t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
	}
	if inspection.Complete ||
		!workflowInspectionHasLimit(
			inspection,
			WorkflowDefinitionInspectionLimitUnsafeFields,
		) {
		t.Fatalf("complete = %v, limits = %v", inspection.Complete, inspection.Limits)
	}
	if len(inspection.Jobs) != 1 ||
		len(inspection.Jobs[0].Steps) != 2 ||
		inspection.Jobs[0].Steps[0].Kind != WorkflowDefinitionStepMCP ||
		inspection.Jobs[0].Steps[0].Target != "" ||
		inspection.Jobs[0].Steps[1].Kind != WorkflowDefinitionStepMCP ||
		inspection.Jobs[0].Steps[1].Target != "" {
		t.Fatalf("jobs = %#v", inspection.Jobs)
	}
	if len(inspection.Dependencies) != 0 {
		t.Fatalf("dependencies = %#v", inspection.Dependencies)
	}
	if len(inspection.Effects) != 1 ||
		inspection.Effects[0] != (WorkflowDefinitionEffectInspection{
			Kind:        WorkflowDefinitionEffectExternalStateChange,
			Occurrences: 2,
		}) {
		t.Fatalf("effects = %#v", inspection.Effects)
	}
}

func TestInspectWorkflowDefinitionBytesProjectsRuntimeResolvedJobMerge(t *testing.T) {
	t.Parallel()
	raw := []byte(`
job_defaults: &job_defaults
  runs-on: picoclaw
  steps:
    - uses: mcp/github/add_issue_comment
jobs:
  merged:
    <<: *job_defaults
`)
	inspection, err := InspectWorkflowDefinitionBytes(workflowInspectionTestSource(), raw)
	if err != nil {
		t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
	}
	if !inspection.Validation.Valid {
		t.Fatalf("validation = %#v", inspection.Validation)
	}
	if len(inspection.Jobs) != 1 ||
		len(inspection.Jobs[0].Steps) != 1 ||
		inspection.Jobs[0].Steps[0].Kind != WorkflowDefinitionStepMCP ||
		inspection.Jobs[0].Steps[0].Target != "mcp/github/add_issue_comment" {
		t.Fatalf("merged topology = %#v", inspection.Jobs)
	}
	if len(inspection.Effects) != 1 ||
		inspection.Effects[0] != (WorkflowDefinitionEffectInspection{
			Kind:        WorkflowDefinitionEffectExternalStateChange,
			Target:      "github/add_issue_comment",
			Occurrences: 1,
		}) {
		t.Fatalf("merged effects = %#v", inspection.Effects)
	}
}

func TestInspectWorkflowDefinitionBytesBoundsReusableTargets(t *testing.T) {
	t.Parallel()
	for _, length := range []int{
		MaxWorkflowInspectionDependencyTargetBytes,
		MaxWorkflowInspectionDependencyTargetBytes + 1,
	} {
		target := "workflows/" +
			strings.Repeat("x", length-len("workflows/")-len(".yml")) +
			".yml"
		raw := []byte("jobs:\n  reuse:\n    uses: " + target + "\n")
		inspection, err := InspectWorkflowDefinitionBytes(
			workflowInspectionTestSource(),
			raw,
		)
		if err != nil {
			t.Fatalf("length %d error = %v", length, err)
		}
		if len(inspection.Jobs) != 1 ||
			inspection.Jobs[0].Kind != WorkflowDefinitionJobReusable {
			t.Fatalf("length %d jobs = %#v", length, inspection.Jobs)
		}
		if length == MaxWorkflowInspectionDependencyTargetBytes {
			if !inspection.Complete ||
				inspection.Jobs[0].ReusableTarget != target ||
				len(inspection.Dependencies) != 1 ||
				inspection.Dependencies[0].Target != target ||
				len(inspection.Effects) != 1 ||
				inspection.Effects[0].Target != target {
				t.Fatalf(
					"exact target projection = %#v / %#v / %#v, limits = %v",
					inspection.Jobs,
					inspection.Dependencies,
					inspection.Effects,
					inspection.Limits,
				)
			}
		} else if inspection.Complete ||
			inspection.Jobs[0].ReusableTarget != "" ||
			len(inspection.Dependencies) != 0 ||
			len(inspection.Effects) != 1 ||
			inspection.Effects[0] != (WorkflowDefinitionEffectInspection{
				Kind:        WorkflowDefinitionEffectTransitiveUnknown,
				Occurrences: 1,
			}) ||
			!workflowInspectionHasLimit(
				inspection,
				WorkflowDefinitionInspectionLimitUnsafeFields,
			) {
			t.Fatalf(
				"over-bound target projection = %#v / %#v / %#v, limits = %v",
				inspection.Jobs,
				inspection.Dependencies,
				inspection.Effects,
				inspection.Limits,
			)
		}
	}
}

func TestInspectWorkflowDefinitionBytesInvalidYAMLUsesFixedIssue(t *testing.T) {
	t.Parallel()
	raw := []byte("jobs:\n  broken: [CANARY_RAW_ERROR_PATH\n")
	inspection, err := InspectWorkflowDefinitionBytes(workflowInspectionTestSource(), raw)
	if err != nil {
		t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
	}
	if inspection.Validation.Valid ||
		inspection.Validation.IssueCount != 1 ||
		len(inspection.Validation.Issues) != 1 ||
		inspection.Validation.Issues[0] != (WorkflowDefinitionValidationIssue{
			Code:  WorkflowDefinitionValidationInvalidYAML,
			Scope: WorkflowDefinitionValidationScopeWorkflow,
		}) {
		t.Fatalf("validation = %#v", inspection.Validation)
	}
	if inspection.Revision != workflowContentRevision(raw) {
		t.Fatalf("revision = %q", inspection.Revision)
	}
	encoded, err := json.Marshal(inspection)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if bytes.Contains(encoded, []byte("CANARY_RAW_ERROR_PATH")) {
		t.Fatalf("raw parse input leaked: %s", encoded)
	}
	for _, kind := range workflowTriggerKinds {
		if _, ok := inspection.Triggers[kind]; !ok {
			t.Fatalf("missing trigger key %s", kind)
		}
	}
}

func TestInspectWorkflowDefinitionBytesValidationIsBounded(t *testing.T) {
	t.Parallel()
	var raw strings.Builder
	raw.WriteString("on:\n  schedule:\n")
	for range MaxWorkflowInspectionValidationIssues + 1 {
		raw.WriteString("    - cron: \"\"\n")
	}
	raw.WriteString(`
jobs:
  test:
    runs-on: picoclaw
    steps:
      - uses: agent/main
`)
	inspection, err := InspectWorkflowDefinitionBytes(
		workflowInspectionTestSource(),
		[]byte(raw.String()),
	)
	if err != nil {
		t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
	}
	validation := inspection.Validation
	if validation.Valid ||
		validation.IssueCount != MaxWorkflowInspectionValidationIssues+1 ||
		len(validation.Issues) != MaxWorkflowInspectionValidationIssues ||
		!validation.Truncated {
		t.Fatalf("validation = %#v", validation)
	}
	for _, issue := range validation.Issues {
		if issue.Code != WorkflowDefinitionValidationScheduleCronRequired ||
			issue.Scope != WorkflowDefinitionValidationScopeSchedule {
			t.Fatalf("issue = %#v", issue)
		}
	}
	if inspection.Complete ||
		!workflowInspectionHasLimit(
			inspection,
			WorkflowDefinitionInspectionLimitValidationIssues,
		) {
		t.Fatalf("complete = %v, limits = %v", inspection.Complete, inspection.Limits)
	}
}

func TestInspectWorkflowDefinitionBytesTriggerProjectionBounds(t *testing.T) {
	t.Parallel()
	t.Run("raw-only family is explicitly incomplete", func(t *testing.T) {
		t.Parallel()
		inspection, err := InspectWorkflowDefinitionBytes(
			workflowInspectionTestSource(),
			[]byte(`
on:
  channel_message:
    channels: []
    conversation: {}
jobs: {}
`),
		)
		if err != nil {
			t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
		}
		trigger := inspection.Triggers[WorkflowTriggerChannelMessage]
		if !trigger.Present ||
			trigger.Projected ||
			trigger.Value != nil ||
			inspection.Complete ||
			!workflowInspectionHasLimit(
				inspection,
				WorkflowDefinitionInspectionLimitTriggers,
			) {
			t.Fatalf(
				"trigger = %#v, complete = %v, limits = %v",
				trigger,
				inspection.Complete,
				inspection.Limits,
			)
		}
	})
	t.Run("schedule count and empty cron", func(t *testing.T) {
		t.Parallel()
		for _, count := range []int{
			MaxWorkflowInspectionTriggerSchedules,
			MaxWorkflowInspectionTriggerSchedules + 1,
		} {
			var raw strings.Builder
			raw.WriteString("on:\n  schedule:\n")
			for range count {
				raw.WriteString("    - cron: \"0 * * * *\"\n")
			}
			raw.WriteString("jobs: {}\n")
			inspection, err := InspectWorkflowDefinitionBytes(
				workflowInspectionTestSource(),
				[]byte(raw.String()),
			)
			if err != nil {
				t.Fatalf("count %d error = %v", count, err)
			}
			trigger := inspection.Triggers[WorkflowTriggerSchedule]
			if count == MaxWorkflowInspectionTriggerSchedules {
				if !trigger.Projected ||
					workflowInspectionHasLimit(
						inspection,
						WorkflowDefinitionInspectionLimitTriggers,
					) {
					t.Fatalf("count %d trigger = %#v, limits = %v", count, trigger, inspection.Limits)
				}
			} else if trigger.Projected ||
				inspection.Complete ||
				!workflowInspectionHasLimit(
					inspection,
					WorkflowDefinitionInspectionLimitTriggers,
				) {
				t.Fatalf(
					"count %d trigger = %#v, complete = %v, limits = %v",
					count,
					trigger,
					inspection.Complete,
					inspection.Limits,
				)
			}
		}
		emptyInspection, err := InspectWorkflowDefinitionBytes(
			workflowInspectionTestSource(),
			[]byte("on:\n  schedule:\n    - {}\njobs: {}\n"),
		)
		if err != nil {
			t.Fatalf("empty schedule error = %v", err)
		}
		emptyTrigger := emptyInspection.Triggers[WorkflowTriggerSchedule]
		if !emptyTrigger.Projected {
			t.Fatalf("empty schedule trigger = %#v", emptyTrigger)
		}
		encoded, marshalErr := json.Marshal(emptyTrigger)
		if marshalErr != nil {
			t.Fatalf("empty schedule marshal error = %v", marshalErr)
		}
		if !bytes.Contains(encoded, []byte(`"cron":""`)) {
			t.Fatalf("empty cron omitted: %s", encoded)
		}
	})

	t.Run("trigger text", func(t *testing.T) {
		t.Parallel()
		for _, length := range []int{
			MaxWorkflowInspectionTriggerTextBytes,
			MaxWorkflowInspectionTriggerTextBytes + 1,
		} {
			raw := []byte(
				"on:\n  schedule:\n    - cron: \"" +
					strings.Repeat("x", length) +
					"\"\njobs: {}\n",
			)
			inspection, err := InspectWorkflowDefinitionBytes(
				workflowInspectionTestSource(),
				raw,
			)
			if err != nil {
				t.Fatalf("length %d error = %v", length, err)
			}
			projected := inspection.Triggers[WorkflowTriggerSchedule].Projected
			if projected != (length == MaxWorkflowInspectionTriggerTextBytes) {
				t.Fatalf("length %d projected = %v, limits = %v", length, projected, inspection.Limits)
			}
			if (length > MaxWorkflowInspectionTriggerTextBytes) !=
				workflowInspectionHasLimit(
					inspection,
					WorkflowDefinitionInspectionLimitTriggers,
				) {
				t.Fatalf("length %d limits = %v", length, inspection.Limits)
			}
		}
		formatInspection, err := InspectWorkflowDefinitionBytes(
			workflowInspectionTestSource(),
			[]byte("on:\n  schedule:\n    - cron: \"\\u202e\"\njobs: {}\n"),
		)
		if err != nil {
			t.Fatalf("format-control inspection error = %v", err)
		}
		if formatInspection.Triggers[WorkflowTriggerSchedule].Projected ||
			formatInspection.Complete ||
			!workflowInspectionHasLimit(
				formatInspection,
				WorkflowDefinitionInspectionLimitTriggers,
			) {
			t.Fatalf(
				"format-control trigger = %#v, complete = %v, limits = %v",
				formatInspection.Triggers[WorkflowTriggerSchedule],
				formatInspection.Complete,
				formatInspection.Limits,
			)
		}
	})

	t.Run("declaration name and type", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			nameLength int
			typeLength int
			projected  bool
		}{
			{
				nameLength: MaxWorkflowInspectionTriggerNameBytes,
				typeLength: MaxWorkflowInspectionTriggerTypeBytes,
				projected:  true,
			},
			{
				nameLength: MaxWorkflowInspectionTriggerNameBytes + 1,
				typeLength: 1,
			},
			{
				nameLength: 1,
				typeLength: MaxWorkflowInspectionTriggerTypeBytes + 1,
			},
		}
		for _, test := range tests {
			raw := []byte(
				"on:\n  command:\n    name: run\n    args:\n      " +
					strings.Repeat("n", test.nameLength) +
					":\n        type: \"" +
					strings.Repeat("t", test.typeLength) +
					"\"\njobs: {}\n",
			)
			inspection, err := InspectWorkflowDefinitionBytes(
				workflowInspectionTestSource(),
				raw,
			)
			if err != nil {
				t.Fatalf(
					"name/type %d/%d error = %v",
					test.nameLength,
					test.typeLength,
					err,
				)
			}
			trigger := inspection.Triggers[WorkflowTriggerCommand]
			if trigger.Projected != test.projected {
				t.Fatalf(
					"name/type %d/%d trigger = %#v, limits = %v",
					test.nameLength,
					test.typeLength,
					trigger,
					inspection.Limits,
				)
			}
			if !test.projected &&
				!workflowInspectionHasLimit(
					inspection,
					WorkflowDefinitionInspectionLimitTriggers,
				) {
				t.Fatalf("name/type %d/%d limits = %v", test.nameLength, test.typeLength, inspection.Limits)
			}
		}
	})

	t.Run("aggregate entries omit a whole later family", func(t *testing.T) {
		t.Parallel()
		var raw strings.Builder
		raw.WriteString("on:\n  schedule:\n")
		for range MaxWorkflowInspectionTriggerSchedules {
			raw.WriteString("    - cron: \"0 * * * *\"\n")
		}
		raw.WriteString("  channel_message:\n    channels: [")
		for index := 0; index <
			MaxWorkflowInspectionTriggerEntries-MaxWorkflowInspectionTriggerSchedules; index++ {
			if index > 0 {
				raw.WriteByte(',')
			}
			raw.WriteByte('x')
		}
		raw.WriteString("]\n  command:\n    name: run\n    args:\n      value:\n        type: string\njobs: {}\n")
		inspection, err := InspectWorkflowDefinitionBytes(
			workflowInspectionTestSource(),
			[]byte(raw.String()),
		)
		if err != nil {
			t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
		}
		if !inspection.Triggers[WorkflowTriggerSchedule].Projected ||
			!inspection.Triggers[WorkflowTriggerChannelMessage].Projected ||
			inspection.Triggers[WorkflowTriggerCommand].Projected ||
			inspection.Triggers[WorkflowTriggerCommand].Value != nil ||
			inspection.Complete ||
			!workflowInspectionHasLimit(
				inspection,
				WorkflowDefinitionInspectionLimitTriggers,
			) {
			t.Fatalf(
				"triggers = %#v, complete = %v, limits = %v",
				inspection.Triggers,
				inspection.Complete,
				inspection.Limits,
			)
		}
	})
}

func TestInspectWorkflowDefinitionBytesJobAndStepLimits(t *testing.T) {
	t.Parallel()
	t.Run("jobs", func(t *testing.T) {
		t.Parallel()
		var raw strings.Builder
		raw.WriteString("jobs:\n")
		for index := 0; index < MaxWorkflowInspectionJobs+1; index++ {
			fmt.Fprintf(
				&raw,
				"  job_%03d:\n    runs-on: picoclaw\n    steps:\n      - uses: agent/main\n",
				index,
			)
		}
		inspection, err := InspectWorkflowDefinitionBytes(
			workflowInspectionTestSource(),
			[]byte(raw.String()),
		)
		if err != nil {
			t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
		}
		if len(inspection.Jobs) != MaxWorkflowInspectionJobs ||
			inspection.Complete ||
			!workflowInspectionHasLimit(inspection, WorkflowDefinitionInspectionLimitJobs) {
			t.Fatalf(
				"jobs = %d, complete = %v, limits = %v",
				len(inspection.Jobs),
				inspection.Complete,
				inspection.Limits,
			)
		}
	})
	t.Run("omitted_job_effects_are_still_aggregated", func(t *testing.T) {
		t.Parallel()
		var raw strings.Builder
		raw.WriteString("jobs:\n")
		for index := 0; index < MaxWorkflowInspectionJobs; index++ {
			fmt.Fprintf(
				&raw,
				"  job_%03d:\n    runs-on: picoclaw\n    steps:\n      - id: no_action\n",
				index,
			)
		}
		raw.WriteString(
			"  zzz_effect:\n    runs-on: picoclaw\n    steps:\n" +
				"      - uses: mcp/github/add_issue_comment\n",
		)
		inspection, err := InspectWorkflowDefinitionBytes(
			workflowInspectionTestSource(),
			[]byte(raw.String()),
		)
		if err != nil {
			t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
		}
		if len(inspection.Jobs) != MaxWorkflowInspectionJobs ||
			!workflowInspectionHasLimit(
				inspection,
				WorkflowDefinitionInspectionLimitJobs,
			) {
			t.Fatalf("jobs = %d, limits = %v", len(inspection.Jobs), inspection.Limits)
		}
		if len(inspection.Dependencies) != 1 ||
			inspection.Dependencies[0] != (WorkflowDefinitionDependencyInspection{
				Kind:        WorkflowDependencyKindMCP,
				Target:      "github/add_issue_comment",
				Occurrences: 1,
			}) {
			t.Fatalf("dependencies = %#v", inspection.Dependencies)
		}
		if len(inspection.Effects) != 1 ||
			inspection.Effects[0] != (WorkflowDefinitionEffectInspection{
				Kind:        WorkflowDefinitionEffectExternalStateChange,
				Target:      "github/add_issue_comment",
				Occurrences: 1,
			}) {
			t.Fatalf("effects = %#v", inspection.Effects)
		}
	})
	t.Run("steps_and_derived_sections", func(t *testing.T) {
		t.Parallel()
		var raw strings.Builder
		raw.WriteString("jobs:\n  test:\n    runs-on: picoclaw\n    steps:\n")
		for range MaxWorkflowInspectionEntries + 1 {
			raw.WriteString("      - uses: agent/main\n")
		}
		inspection, err := InspectWorkflowDefinitionBytes(
			workflowInspectionTestSource(),
			[]byte(raw.String()),
		)
		if err != nil {
			t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
		}
		if len(inspection.Jobs) != 1 ||
			len(inspection.Jobs[0].Steps) != MaxWorkflowInspectionEntries {
			t.Fatalf("job topology lengths = %d/%d", len(inspection.Jobs), len(inspection.Jobs[0].Steps))
		}
		for _, code := range []WorkflowDefinitionInspectionLimitCode{
			WorkflowDefinitionInspectionLimitSteps,
			WorkflowDefinitionInspectionLimitDependencies,
			WorkflowDefinitionInspectionLimitEffects,
		} {
			if !workflowInspectionHasLimit(inspection, code) {
				t.Fatalf("limits = %v, missing %s", inspection.Limits, code)
			}
		}
		if inspection.Dependencies[0].Occurrences != MaxWorkflowInspectionEntries ||
			inspection.Effects[0].Occurrences != MaxWorkflowInspectionEntries {
			t.Fatalf(
				"bounded derived occurrences = %#v / %#v",
				inspection.Dependencies,
				inspection.Effects,
			)
		}
	})
	t.Run("dependency_and_effect_occurrences", func(t *testing.T) {
		var raw strings.Builder
		raw.WriteString("jobs:\n")
		stepsPerJob := MaxWorkflowInspectionEntries / MaxWorkflowInspectionJobs
		for jobIndex := 0; jobIndex < MaxWorkflowInspectionJobs; jobIndex++ {
			fmt.Fprintf(&raw, "  job_%03d:\n    uses: workflows/child.yml\n    steps:\n", jobIndex)
			for range stepsPerJob {
				raw.WriteString("      - uses: agent/main\n")
			}
		}
		inspection, err := InspectWorkflowDefinitionBytes(
			workflowInspectionTestSource(),
			[]byte(raw.String()),
		)
		if err != nil {
			t.Fatalf("InspectWorkflowDefinitionBytes() error = %v", err)
		}
		if workflowInspectionHasLimit(inspection, WorkflowDefinitionInspectionLimitSteps) {
			t.Fatalf("unexpected step limit: %v", inspection.Limits)
		}
		for _, code := range []WorkflowDefinitionInspectionLimitCode{
			WorkflowDefinitionInspectionLimitDependencies,
			WorkflowDefinitionInspectionLimitEffects,
		} {
			if !workflowInspectionHasLimit(inspection, code) {
				t.Fatalf("limits = %v, missing %s", inspection.Limits, code)
			}
		}
		if got := workflowInspectionDependencyOccurrences(inspection.Dependencies); got !=
			MaxWorkflowInspectionEntries {
			t.Fatalf("dependency occurrences = %d", got)
		}
		if got := workflowInspectionEffectOccurrences(inspection.Effects); got !=
			MaxWorkflowInspectionEntries {
			t.Fatalf("effect occurrences = %d", got)
		}
	})
}

func TestInspectWorkflowDefinitionBytesSourceValidationAndByteLimit(t *testing.T) {
	t.Parallel()
	valid := workflowInspectionTestSource()
	tests := []WorkflowDefinitionInspectionSource{
		{},
		{Kind: WorkflowDefinitionInspectionSourcePublished},
		{
			Kind:         WorkflowDefinitionInspectionSourcePublished,
			Ref:          "workflows/test.yml",
			TemplateName: "template",
		},
		{Kind: WorkflowDefinitionInspectionSourceTemplate},
		{
			Kind:         WorkflowDefinitionInspectionSourceTemplate,
			Ref:          "workflows/test.yml",
			TemplateName: "template",
		},
		{Kind: "unknown", TemplateName: "template"},
		{Kind: WorkflowDefinitionInspectionSourceTemplate, TemplateName: "../template"},
	}
	for _, source := range tests {
		t.Run(fmt.Sprintf("%s-%s-%s", source.Kind, source.Ref, source.TemplateName), func(t *testing.T) {
			t.Parallel()
			_, err := InspectWorkflowDefinitionBytes(source, []byte("jobs: {}"))
			if !errors.Is(err, ErrWorkflowInspectionSourceInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	for _, ref := range []string{
		"workflows/unsafe\u0000.yml",
		"workflows/unsafe\u202e.yml",
		"./workflows/test.yml",
		" workflows/test.yml",
		"workflows/" + strings.Repeat("x", MaxWorkflowInspectionSourceRefBytes),
	} {
		_, err := InspectWorkflowDefinitionBytes(
			WorkflowDefinitionInspectionSource{
				Kind: WorkflowDefinitionInspectionSourcePublished,
				Ref:  ref,
			},
			[]byte("jobs: {}"),
		)
		if !errors.Is(err, ErrWorkflowInspectionSourceInvalid) {
			t.Fatalf("unsafe published ref error = %v", err)
		}
	}
	exactRef := "workflows/" +
		strings.Repeat(
			"x",
			MaxWorkflowInspectionSourceRefBytes-len("workflows/")-len(".yml"),
		) +
		".yml"
	if _, err := InspectWorkflowDefinitionBytes(
		WorkflowDefinitionInspectionSource{
			Kind: WorkflowDefinitionInspectionSourcePublished,
			Ref:  exactRef,
		},
		[]byte("jobs: {}"),
	); err != nil {
		t.Fatalf("exact published ref error = %v", err)
	}

	exact := bytes.Repeat([]byte{' '}, int(MaxWorkflowInspectionSourceBytes))
	inspection, err := InspectWorkflowDefinitionBytes(valid, exact)
	if err != nil {
		t.Fatalf("exact limit error = %v", err)
	}
	if inspection.Revision != workflowContentRevision(exact) {
		t.Fatalf("exact-limit revision = %q", inspection.Revision)
	}
	_, err = InspectWorkflowDefinitionBytes(
		valid,
		bytes.Repeat([]byte{' '}, int(MaxWorkflowInspectionSourceBytes)+1),
	)
	if !errors.Is(err, ErrWorkflowInspectionSourceTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestInspectLocalWorkflowDefinitionUsesConfiguredResolverAndSafeErrors(t *testing.T) {
	workspace := t.TempDir()
	definitionsDir := "configured-definitions"
	root := filepath.Join(workspace, definitionsDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	raw := []byte(`
on:
  manual: {}
jobs:
  test:
    runs-on: picoclaw
    steps:
      - uses: agent/main
`)
	path := filepath.Join(root, "test.yml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	inspection, err := InspectLocalWorkflowDefinition(
		context.Background(),
		workspace,
		"workflows/test.yml",
		WithDefinitionsDir(definitionsDir),
	)
	if err != nil {
		t.Fatalf("InspectLocalWorkflowDefinition() error = %v", err)
	}
	if inspection.Source != (WorkflowDefinitionInspectionSource{
		Kind: WorkflowDefinitionInspectionSourcePublished,
		Ref:  "workflows/test.yml",
	}) {
		t.Fatalf("source = %#v", inspection.Source)
	}
	if inspection.Revision != workflowContentRevision(raw) {
		t.Fatalf("revision = %q", inspection.Revision)
	}
	encoded, err := json.Marshal(inspection)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if bytes.Contains(encoded, []byte(workspace)) ||
		bytes.Contains(encoded, []byte(definitionsDir)) {
		t.Fatalf("filesystem path leaked: %s", encoded)
	}

	_, err = InspectLocalWorkflowDefinition(
		context.Background(),
		workspace,
		"workflows/missing.yml",
		WithDefinitionsDir(definitionsDir),
	)
	if !errors.Is(err, ErrWorkflowInspectionNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	_, err = InspectLocalWorkflowDefinition(
		context.Background(),
		workspace,
		"../escape.yml",
		WithDefinitionsDir(definitionsDir),
	)
	if !errors.Is(err, ErrWorkflowInspectionSourceInvalid) {
		t.Fatalf("invalid ref error = %v", err)
	}
	_, err = InspectLocalWorkflowDefinition(
		context.Background(),
		workspace,
		"workflows/test.yml",
		WithDefinitionsDir("../outside"),
	)
	if !errors.Is(err, ErrWorkflowInspectionUnavailable) {
		t.Fatalf("unsafe definitions directory error = %v", err)
	}
	directoryTarget := filepath.Join(root, "directory.yml")
	if mkdirErr := os.Mkdir(directoryTarget, 0o755); mkdirErr != nil {
		t.Fatalf("Mkdir() error = %v", mkdirErr)
	}
	_, err = InspectLocalWorkflowDefinition(
		context.Background(),
		workspace,
		"workflows/directory.yml",
		WithDefinitionsDir(definitionsDir),
	)
	if !errors.Is(err, ErrWorkflowInspectionUnavailable) {
		t.Fatalf("directory error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.yml")
	if writeErr := os.WriteFile(outside, raw, 0o600); writeErr != nil {
		t.Fatalf("WriteFile(outside) error = %v", writeErr)
	}
	link := filepath.Join(root, "escape.yml")
	if symlinkErr := os.Symlink(outside, link); symlinkErr != nil {
		t.Logf("symlink escape check skipped: %v", symlinkErr)
	} else {
		_, inspectErr := InspectLocalWorkflowDefinition(
			context.Background(),
			workspace,
			"workflows/escape.yml",
			WithDefinitionsDir(definitionsDir),
		)
		if !errors.Is(inspectErr, ErrWorkflowInspectionUnavailable) {
			t.Fatalf("symlink escape error = %v", inspectErr)
		}
	}
	oversizedPath := filepath.Join(root, "oversized.yml")
	if writeErr := os.WriteFile(
		oversizedPath,
		bytes.Repeat([]byte{' '}, int(MaxWorkflowInspectionSourceBytes)+1),
		0o600,
	); writeErr != nil {
		t.Fatalf("WriteFile(oversized) error = %v", writeErr)
	}
	_, err = InspectLocalWorkflowDefinition(
		context.Background(),
		workspace,
		"workflows/oversized.yml",
		WithDefinitionsDir(definitionsDir),
	)
	if !errors.Is(err, ErrWorkflowInspectionSourceTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = InspectLocalWorkflowDefinition(
		canceled,
		workspace,
		"workflows/test.yml",
		WithDefinitionsDir(definitionsDir),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func TestInspectLocalWorkflowDefinitionUsesMutationLock(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Join(workspace, DefaultDefinitionsDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "test.yml"),
		[]byte("jobs: {}\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		t.Fatalf("lockWorkflowMutation() error = %v", err)
	}
	invalidResult := make(chan error, 1)
	go func() {
		_, inspectErr := InspectLocalWorkflowDefinition(
			context.Background(),
			workspace,
			"../invalid.yml",
		)
		invalidResult <- inspectErr
	}()
	select {
	case inspectErr := <-invalidResult:
		if !errors.Is(inspectErr, ErrWorkflowInspectionSourceInvalid) {
			unlock()
			t.Fatalf("invalid ref error = %v", inspectErr)
		}
	case <-time.After(5 * time.Second):
		unlock()
		t.Fatal("invalid ref waited for the mutation lock")
	}
	result := make(chan error, 1)
	go func() {
		_, inspectErr := InspectLocalWorkflowDefinition(
			context.Background(),
			workspace,
			"workflows/test.yml",
		)
		result <- inspectErr
	}()
	select {
	case err := <-result:
		unlock()
		t.Fatalf("inspection returned before mutation lock release: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("inspection error after release = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inspection remained blocked after mutation lock release")
	}
}

func TestInspectBuiltInWorkflowTemplateUsesRegistryBytes(t *testing.T) {
	t.Parallel()
	for _, template := range builtInWorkflowTemplateRegistry {
		t.Run(template.name, func(t *testing.T) {
			t.Parallel()
			inspection, err := InspectBuiltInWorkflowTemplate(template.name)
			if err != nil {
				t.Fatalf("InspectBuiltInWorkflowTemplate() error = %v", err)
			}
			if inspection.Source != (WorkflowDefinitionInspectionSource{
				Kind:         WorkflowDefinitionInspectionSourceTemplate,
				TemplateName: template.name,
			}) {
				t.Fatalf("source = %#v", inspection.Source)
			}
			if inspection.Revision != workflowContentRevision([]byte(template.raw)) {
				t.Fatalf("revision = %q", inspection.Revision)
			}
			if _, err := InspectBuiltInWorkflowTemplate(
				" " + strings.ToUpper(template.name) + " ",
			); !errors.Is(err, ErrWorkflowInspectionSourceInvalid) {
				t.Fatalf("noncanonical template error = %v", err)
			}
		})
	}
	if _, err := InspectBuiltInWorkflowTemplate(" "); !errors.Is(
		err,
		ErrWorkflowInspectionSourceInvalid,
	) {
		t.Fatalf("blank template error = %v", err)
	}
	if _, err := InspectBuiltInWorkflowTemplate("missing"); !errors.Is(
		err,
		ErrWorkflowTemplateUnknown,
	) {
		t.Fatalf("unknown template error = %v", err)
	}
}

func TestInspectWorkflowDefinitionBytesExactRevisionChangesWithWhitespace(t *testing.T) {
	t.Parallel()
	first, err := InspectWorkflowDefinitionBytes(
		workflowInspectionTestSource(),
		[]byte("jobs: {}\n"),
	)
	if err != nil {
		t.Fatalf("first inspection error = %v", err)
	}
	second, err := InspectWorkflowDefinitionBytes(
		workflowInspectionTestSource(),
		[]byte("jobs: {}\n\n"),
	)
	if err != nil {
		t.Fatalf("second inspection error = %v", err)
	}
	if first.Revision == second.Revision {
		t.Fatal("exact revisions ignored trailing whitespace")
	}
}

func workflowInspectionTestSource() WorkflowDefinitionInspectionSource {
	return WorkflowDefinitionInspectionSource{
		Kind:         WorkflowDefinitionInspectionSourceTemplate,
		TemplateName: "test",
	}
}

func workflowInspectionJobIDs(jobs []WorkflowDefinitionJobInspection) []string {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	return ids
}

func workflowInspectionHasLimit(
	inspection *WorkflowDefinitionInspection,
	code WorkflowDefinitionInspectionLimitCode,
) bool {
	for _, candidate := range inspection.Limits {
		if candidate == code {
			return true
		}
	}
	return false
}

func workflowInspectionDependencyOccurrences(
	dependencies []WorkflowDefinitionDependencyInspection,
) int {
	total := 0
	for _, dependency := range dependencies {
		total += dependency.Occurrences
	}
	return total
}

func workflowInspectionEffectOccurrences(
	effects []WorkflowDefinitionEffectInspection,
) int {
	total := 0
	for _, effect := range effects {
		total += effect.Occurrences
	}
	return total
}
