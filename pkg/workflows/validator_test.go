package workflows

import (
	"strings"
	"testing"
)

func TestValidateReusableWorkflowContract(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Summarize Text
on:
  workflow_call:
    inputs:
      text:
        type: string
        required: true
      style:
        type: string
        default: concise
    secrets:
      slack_token:
        required: false
    outputs:
      summary:
        value: ${{ jobs.summarize.outputs.summary }}
jobs:
  summarize:
    runs-on: picoclaw
    outputs:
      summary: ${{ steps.agent.outputs.text }}
    steps:
      - id: agent
        uses: agent/main
        with:
          session: inherit
          history: read_write
          cache: session
          message: ${{ inputs.text }}
`)
	if err := Validate(workflow); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestValidateCallerJobUsesReusableWorkflowRef(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Daily Brief
on:
  manual: {}
jobs:
  search:
    runs-on: picoclaw
    outputs:
      results: ${{ steps.search.outputs.text }}
    steps:
      - id: search
        uses: tool/web_search
        with:
          query: AI workflow news
  summarize:
    needs: search
    uses: workflows/summarize-text.yml
    with:
      text: ${{ needs.search.outputs.results }}
      style: bullet
    secrets: inherit
`)
	if err := Validate(workflow); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestValidateChannelConversationAndAgentPipelineModes(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Chat Pipeline
on:
  channel_message:
    channels: [telegram, slack]
    text_matches: "^/ask\\b"
    passthrough: false
    conversation:
      session: discussion
      delivery: same_discussion
jobs:
  chat:
    runs-on: picoclaw
    steps:
      - id: classify
        uses: agent/router
        with:
          session: inherit
          history: read_only
          cache: session
          message: ${{ event.message.text }}
      - id: answer
        uses: agent/main
        with:
          session: inherit
          history: read_write
          cache: key:agent-main-session
          message: ${{ event.message.text }}
`)
	if err := Validate(workflow); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestValidateCommandTriggerContract(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Command
on:
  command:
    name: /deploy
    channels: [telegram]
    args:
      env:
        type: string
        required: true
    passthrough: false
    conversation:
      session: sender
      delivery: same_discussion
jobs:
  deploy:
    runs-on: picoclaw
    context:
      session: key:deploy-session
      delivery: inherit
    steps:
      - id: build
        uses: function/build_release
      - id: notify
        uses: tool/message
`)
	if err := Validate(workflow); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestValidateRejectsInvalidTriggerAndContextModes(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Invalid Trigger
on:
  channel_message:
    text_matches: "["
    conversation:
      session: invalid
      delivery: elsewhere
  command:
    args:
      count:
        type: duration
jobs:
  bad:
    runs-on: picoclaw
    context:
      session: bad
      delivery: remote
    steps:
      - id: bad-agent
        uses: agent/main
        context:
          session: bad
          delivery: remote
        with:
          session: bad
          history: append
          cache: shared
      - uses: unknown/thing
`)
	err := Validate(workflow)
	if err == nil {
		t.Fatal("Validate succeeded, want multiple validation errors")
	}
	for _, want := range []string{
		"invalid regex",
		"unsupported session mode",
		"unsupported delivery mode",
		"command name is required",
		"unsupported input type",
		"unsupported history mode",
		"unsupported cache mode",
		"unsupported uses target",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate error %q missing %q", err.Error(), want)
		}
	}
}

func TestValidateRejectsMissingJobShapeAndDuplicateStepID(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Missing Shape
on:
  manual: {}
jobs:
  first:
    needs: missing
    steps:
      - id: dup
        uses: tool/message
      - id: dup
        uses: tool/message
  caller:
    uses: workflows/helper.yml
    steps:
      - uses: tool/message
`)
	err := Validate(workflow)
	if err == nil {
		t.Fatal("Validate succeeded, want job shape errors")
	}
	for _, want := range []string{
		"unknown dependency",
		"runs-on is required",
		"duplicate step id",
		"reusable workflow jobs cannot define steps",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate error %q missing %q", err.Error(), want)
		}
	}
}

func TestValidateRejectsStepLevelReusableWorkflowUse(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Bad
on:
  manual: {}
jobs:
  bad:
    runs-on: picoclaw
    steps:
      - uses: workflows/helper.yml
`)
	err := Validate(workflow)
	if err == nil || !strings.Contains(err.Error(), "job level") {
		t.Fatalf("Validate error = %v, want job-level reusable workflow error", err)
	}
}

func TestValidateDetectsJobDependencyCycle(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Cycle
on:
  manual: {}
jobs:
  a:
    needs: b
    runs-on: picoclaw
    steps:
      - uses: tool/message
  b:
    needs: a
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	err := Validate(workflow)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Validate error = %v, want cycle error", err)
	}
}

func TestValidateRejectsInvalidWorkflowCallInputType(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Bad Input
on:
  workflow_call:
    inputs:
      value:
        type: duration
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	err := Validate(workflow)
	if err == nil || !strings.Contains(err.Error(), "unsupported input type") {
		t.Fatalf("Validate error = %v, want input type error", err)
	}
}

func TestParseRejectsInvalidWorkflowShapes(t *testing.T) {
	if _, err := Parse([]byte("- not: mapping")); err == nil {
		t.Fatal("Parse sequence succeeded, want workflow mapping error")
	}
	if _, err := Parse([]byte("on: []\njobs: {}")); err == nil {
		t.Fatal("Parse list trigger succeeded, want on mapping error")
	}
}

func TestStringListAcceptsScalarAndSequence(t *testing.T) {
	scalar := parseWorkflow(t, `
name: Scalar
on:
  manual: {}
jobs:
  one:
    needs: two
    uses: workflows/two.yml
  two:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	if got := scalar.Jobs["one"].Needs; len(got) != 1 || got[0] != "two" {
		t.Fatalf("scalar needs = %#v, want [two]", got)
	}

	sequence := parseWorkflow(t, `
name: Sequence
on:
  manual: {}
jobs:
  one:
    needs: [two, three]
    uses: workflows/two.yml
  two:
    runs-on: picoclaw
    steps:
      - uses: tool/message
  three:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	if got := sequence.Jobs["one"].Needs; len(got) != 2 || got[0] != "two" || got[1] != "three" {
		t.Fatalf("sequence needs = %#v, want [two three]", got)
	}
}

func TestValidateNilWorkflowAndErrorFormatting(t *testing.T) {
	err := Validate(nil)
	if err == nil || !strings.Contains(err.Error(), "workflow is required") {
		t.Fatalf("Validate(nil) error = %v, want workflow required", err)
	}
	formatted := (ValidationErrors{{Message: "global"}, {Path: "jobs.a", Message: "bad"}}).Error()
	if !strings.Contains(formatted, "global") || !strings.Contains(formatted, "jobs.a: bad") {
		t.Fatalf("ValidationErrors.Error() = %q", formatted)
	}
}

func TestValidateRejectsInvalidScheduleCron(t *testing.T) {
	workflow := parseWorkflow(t, `
name: Bad Schedule
on:
  schedule:
    - cron: "not cron"
jobs:
  noop:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`)
	err := Validate(workflow)
	if err == nil || !strings.Contains(err.Error(), "invalid cron expression") {
		t.Fatalf("Validate error = %v, want invalid cron expression", err)
	}
}

func parseWorkflow(t *testing.T, text string) *Workflow {
	t.Helper()
	workflow, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	return workflow
}
