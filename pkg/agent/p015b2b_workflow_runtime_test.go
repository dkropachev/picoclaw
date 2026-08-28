package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestP015B2BWorkflowScheduleDiagnosticsPreserveBehaviorAndRedact(t *testing.T) {
	const (
		workflowFile = "p015b2b-private-schedule.yml"
		workflowRef  = "workflows/" + workflowFile
		missingRef   = "P015B2B_WORKFLOW_REF_93ff6ae981"
	)
	workspace := t.TempDir()
	writeWorkflowAutomationFile(t, workspace, workflowFile, `
name: Private scheduled workflow
on:
  schedule:
    - cron: "* * * * *"
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: agent/default
`)
	loop := newWorkflowAutomationTestLoop(workspace)
	t.Cleanup(loop.Close)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

	var schedules map[string]scheduledWorkflowRun
	var loadErr error
	loadRecords, loadRaw := captureP015HookRecords(t, func() {
		schedules, loadErr = loop.loadScheduledWorkflowRuns(
			context.Background(),
			workspace,
			now,
			nil,
		)
	})
	if loadErr != nil {
		t.Fatalf("loadScheduledWorkflowRuns() error = %v", loadErr)
	}
	if len(schedules) != 0 {
		t.Fatalf("unvalidated schedules = %#v, want none", schedules)
	}
	loadRecord := p015B2ARequireRuntimeRecord(
		t,
		loadRecords,
		"Scheduled workflow skipped until revalidated",
		nil,
	)
	p015B2AAssertRuntimeObservation(
		t,
		loadRecord,
		logger.ObservationPrefixIdentityWorkflow,
		logger.ObserveIdentity(logger.ObservationDomainIdentityWorkflow, workflowRef),
	)
	if !p015B2ANonemptyRecordString(loadRecord, "error_digest") {
		t.Fatalf("unvalidated workflow record lacks safe error observation: %#v", loadRecord)
	}
	assertP015CanariesAbsent(t, loadRaw, workflowFile, workflowRef)

	missingRecords, missingRaw := captureP015HookRecords(t, func() {
		loop.runScheduledWorkflow(
			context.Background(),
			scheduledWorkflowRun{
				ref:        missingRef,
				generation: loop.GetConfig(),
			},
			now,
		)
	})
	missingRecord := p015B2ARequireRuntimeRecord(
		t,
		missingRecords,
		"Scheduled workflow has no bound definition snapshot",
		nil,
	)
	p015B2AAssertRuntimeObservation(
		t,
		missingRecord,
		logger.ObservationPrefixIdentityWorkflow,
		logger.ObserveIdentity(logger.ObservationDomainIdentityWorkflow, missingRef),
	)
	if missingRecord["available"] != false {
		t.Fatalf("missing-snapshot availability = %#v, want false", missingRecord["available"])
	}
	assertP015CanariesAbsent(t, missingRaw, missingRef)

	runs, err := workflows.NewFileRunStore(workspace).ListRuns(context.Background())
	if err != nil {
		t.Fatalf("ListRuns() error = %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("diagnostic failure paths created workflow runs: %#v", runs)
	}
	if strings.Contains(string(loadRaw)+string(missingRaw), "sensitive_preview") {
		t.Fatalf("workflow safe diagnostics emitted a sensitive preview")
	}
}
