package code

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestResultSchemaV2AlwaysProjectsBoundedImplementationUsage(t *testing.T) {
	value := completedAggregate()
	usage := prworkspace.ImplementationUsage{
		Scope: prworkspace.ImplementationUsageScope, Complete: true,
		Repair: prworkspace.TokenUsage{
			ProviderCalls: 2, UsageReportedCalls: 2,
			PromptTokens: 10, CachedTokens: 4,
			CompletionTokens: 3, ReasoningTokens: 1, TotalTokens: 13,
			LatencyMillis: 20,
		},
		Audit: prworkspace.TokenUsage{
			ProviderCalls: 1, UsageReportedCalls: 1,
			PromptTokens: 5, CachedTokens: 2,
			CompletionTokens: 2, ReasoningTokens: 1, TotalTokens: 7,
			LatencyMillis: 8,
		},
		Total: prworkspace.TokenUsage{
			ProviderCalls: 3, UsageReportedCalls: 3,
			PromptTokens: 15, CachedTokens: 6,
			CompletionTokens: 5, ReasoningTokens: 2, TotalTokens: 20,
			LatencyMillis: 28,
		},
	}
	value.StageRuns = []prworkspace.StageRun{{
		ID: "psr_stage", Stage: "implementation", Usage: &usage,
	}}
	snapshot, err := classifyAggregate("request", value)
	require.NoError(t, err)
	require.Equal(t, ResultSchemaVersion, snapshot.result.Version)
	assert.Equal(t, ImplementationUsage{
		Scope: usage.Scope, Complete: true,
		Repair: projectTokenUsage(usage.Repair), Audit: projectTokenUsage(usage.Audit),
		Total: projectTokenUsage(usage.Total),
	}, snapshot.result.Usage)
	legacy := completedAggregate()
	legacy.StageRuns[0].Usage = nil
	snapshot, err = classifyAggregate("request", legacy)
	require.NoError(t, err)
	assert.Equal(
		t,
		ImplementationUsage{Scope: prworkspace.ImplementationUsageScope},
		snapshot.result.Usage,
	)

	encoded, err := json.Marshal(snapshot.result)
	require.NoError(t, err)
	var contract map[string]any
	require.NoError(t, json.Unmarshal(encoded, &contract))
	assert.ElementsMatch(t, []string{
		"version", "request_id", "workspace_id", "phase", "status",
		"candidate_revision", "validation_status", "pending_gate", "branch",
		"pull_request_url", "error_code", "usage",
	}, mapKeys(contract))
	usageJSON, ok := contract["usage"].(map[string]any)
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"scope", "complete", "repair", "audit", "total"}, mapKeys(usageJSON))
	for _, name := range []string{"repair", "audit", "total"} {
		tokens, tokenOK := usageJSON[name].(map[string]any)
		require.True(t, tokenOK)
		assert.ElementsMatch(t, []string{
			"provider_calls", "usage_reported_calls", "prompt_tokens", "cached_tokens",
			"completion_tokens", "reasoning_tokens", "total_tokens", "latency_millis",
		}, mapKeys(tokens))
	}

	value.StageRuns = nil
	value.RepairAttempts = nil
	value.ValidationRuns = nil
	value.Publications = nil
	value.Workspace.Phase = prworkspace.PhaseIntake
	value.Workspace.ExecutionState = prworkspace.ExecutionQueued
	snapshot, err = classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, ImplementationUsage{Scope: prworkspace.ImplementationUsageScope}, snapshot.result.Usage)

	usage.Total.TotalTokens--
	value.StageRuns = []prworkspace.StageRun{{Stage: "implementation", Usage: &usage}}
	_, err = classifyAggregate("request", value)
	require.Error(t, err)

	value.StageRuns = []prworkspace.StageRun{{Stage: "implementation"}}
	snapshot, err = classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(
		t,
		ImplementationUsage{Scope: prworkspace.ImplementationUsageScope},
		snapshot.result.Usage,
	)
}

func mapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}

func TestClassifyAggregateRequiresCompleteExactDraftPullEvidence(t *testing.T) {
	valid := completedAggregate()
	snapshot, err := classifyAggregate("devq_11111111111111111111111111111111", valid)
	require.NoError(t, err)
	assert.Equal(t, actionComplete, snapshot.action)
	assert.Equal(t, "commit", snapshot.result.CandidateRevision)
	assert.Equal(t, "feature-branch", snapshot.result.Branch)
	assert.Equal(t, "https://github.com/octo/repo/pull/17", snapshot.result.PullRequestURL)

	tests := map[string]func(*prworkspace.Aggregate){
		"phase only": func(value *prworkspace.Aggregate) {
			value.Publications = nil
		},
		"missing validation": func(value *prworkspace.Aggregate) {
			value.ValidationRuns = nil
		},
		"wrong validation stage": func(value *prworkspace.Aggregate) {
			value.ValidationRuns[0].StageRunID = "psr_other"
		},
		"wrong repair attempt": func(value *prworkspace.Aggregate) {
			value.ValidationRuns[0].RepairAttemptID = "pra_other"
		},
		"empty validation candidate": func(value *prworkspace.Aggregate) {
			value.ValidationRuns[0].CandidateSHA = ""
		},
		"empty checks": func(value *prworkspace.Aggregate) {
			value.ValidationRuns[0].Checks = nil
		},
		"failed check": func(value *prworkspace.Aggregate) {
			value.ValidationRuns[0].Checks[0].Status = "failed"
		},
		"wrong publication target": func(value *prworkspace.Aggregate) {
			value.Publications[0].TargetID = "pra_other"
		},
		"empty URL": func(value *prworkspace.Aggregate) {
			value.Publications[0].ExternalURL = ""
		},
		"foreign URL": func(value *prworkspace.Aggregate) {
			value.Publications[0].ExternalURL = "https://example.com/octo/repo/pull/17"
		},
		"credential URL": func(value *prworkspace.Aggregate) {
			value.Publications[0].ExternalURL = "https://token@github.com/octo/repo/pull/17"
		},
		"missing pull identity": func(value *prworkspace.Aggregate) {
			value.ProviderSnapshot.PullRequestID = ""
		},
		"wrong external identity": func(value *prworkspace.Aggregate) {
			value.Publications[0].ExternalID = "18"
		},
		"wrong head": func(value *prworkspace.Aggregate) {
			value.ProviderSnapshot.HeadSHA = "different"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := completedAggregate()
			mutate(&candidate)
			classified, classifyErr := classifyAggregate(
				"devq_11111111111111111111111111111111",
				candidate,
			)
			require.NoError(t, classifyErr)
			assert.Equal(t, actionFail, classified.action)
			assert.Equal(t, "incomplete_publication_evidence", classified.result.ErrorCode)
		})
	}
}

func TestClassifyAggregateSurfacesGateCharterReconcileAndSafeFailure(t *testing.T) {
	t.Parallel()

	base := completedAggregate()
	base.Workspace.Phase = prworkspace.PhaseImplementation
	base.Workspace.ExecutionState = prworkspace.ExecutionWaitingUser
	base.Gates = []prworkspace.GateRun{{
		ID:              "pgr_11111111111111111111111111111111",
		DecisionPoint:   "pr.implementation.complete",
		SubjectRevision: "sha256:subject",
		State:           prworkspace.ExecutionWaitingUser,
		Turns: []prworkspace.GateTurn{{
			Status: "waiting",
			GateForm: &prworkspace.GateForm{
				Prompt: "Accept?\x1b[31m\nsecret",
				Fields: []gatetypes.GateField{{
					ID: "action", Type: gatetypes.GateFieldSelect, Label: "Action", Required: true,
					Options: []gatetypes.GateFieldOption{{ID: "accept", Label: "Accept"}},
				}},
			},
		}},
	}}
	snapshot, err := classifyAggregate("request", base)
	require.NoError(t, err)
	require.Equal(t, actionGate, snapshot.action)
	require.NotNil(t, snapshot.result.PendingGate)
	assert.NotContains(t, snapshot.result.PendingGate.Prompt, "\x1b")
	assert.Len(t, snapshot.result.PendingGate.Fields, 1)

	base.Gates = []prworkspace.GateRun{{DecisionPoint: "pr.charter.confirm", State: prworkspace.ExecutionSucceeded}}
	base.Charters = []prworkspace.Charter{{ID: "pcr_1", Confirmed: false}}
	base.Workspace.Phase = prworkspace.PhaseCharter
	base.Workspace.ActiveCharterID = ""
	snapshot, err = classifyAggregate("request", base)
	require.NoError(t, err)
	assert.Equal(t, actionCharter, snapshot.action)

	base.Workspace.Phase = prworkspace.PhasePublication
	base.Charters = nil
	base.Gates = nil
	base.Publications[0].State = prworkspace.ExecutionUnknown
	snapshot, err = classifyAggregate("request", base)
	require.NoError(t, err)
	assert.Equal(t, actionReconcile, snapshot.action)

	base.Publications = nil
	base.Workspace.ExecutionState = prworkspace.ExecutionFailed
	base.Activity = []prworkspace.Activity{{
		Kind: "development.failed", Metadata: map[string]any{"code": "unsafe_provider"},
	}}
	snapshot, err = classifyAggregate("request", base)
	require.NoError(t, err)
	assert.Equal(t, actionFail, snapshot.action)
	assert.Equal(t, "unsafe_provider", snapshot.result.ErrorCode)
}

func TestClassifyAggregateRejectsMalformedIdentityAndValidationInfrastructure(t *testing.T) {
	t.Parallel()

	value := completedAggregate()
	value.Workspace.ID = "devw_bad"
	_, err := classifyAggregate("request", value)
	require.Error(t, err)

	value = completedAggregate()
	value.Workspace.Phase = prworkspace.PhaseValidation
	value.Workspace.ExecutionState = prworkspace.ExecutionFailed
	value.ValidationRuns[0].State = prworkspace.ExecutionFailed
	value.ValidationRuns[0].Checks[0].Status = "infrastructure_error"
	snapshot, err := classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, "validation_unavailable", snapshot.result.ErrorCode)

	value = completedAggregate()
	value.Workspace.Phase = prworkspace.PhaseTriage
	value.Workspace.ExecutionState = prworkspace.ExecutionWaitingUser
	value.Findings = []prworkspace.Finding{{Disposition: prworkspace.FindingOpen}}
	snapshot, err = classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, actionFail, snapshot.action)
	assert.Equal(t, "implementation_unavailable", snapshot.result.ErrorCode)

	value.Findings = nil
	value.Workspace.ExecutionState = prworkspace.ExecutionBlocked
	snapshot, err = classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, "implementation_unavailable", snapshot.result.ErrorCode)
}

func TestClassifyAggregatePhaseStateMatrix(t *testing.T) {
	t.Parallel()

	phases := []prworkspace.Phase{
		prworkspace.PhaseIntake,
		prworkspace.PhaseCharter,
		prworkspace.PhasePlanning,
		prworkspace.PhaseReview,
		prworkspace.PhaseTriage,
		prworkspace.PhaseImplementation,
		prworkspace.PhaseValidation,
		prworkspace.PhaseCompletionAudit,
		prworkspace.PhasePublication,
		prworkspace.PhaseComplete,
	}
	states := []prworkspace.ExecutionState{
		prworkspace.ExecutionQueued,
		prworkspace.ExecutionRunning,
		prworkspace.ExecutionWaitingGate,
		prworkspace.ExecutionWaitingUser,
		prworkspace.ExecutionSucceeded,
		prworkspace.ExecutionFailed,
		prworkspace.ExecutionBlocked,
		prworkspace.ExecutionCanceled,
		prworkspace.ExecutionStale,
		prworkspace.ExecutionUnknown,
	}
	for _, phase := range phases {
		for _, state := range states {
			value := completedAggregate()
			value.Workspace.Phase = phase
			value.Workspace.ExecutionState = state
			value.Gates = nil
			value.Charters = nil
			snapshot, err := classifyAggregate("request", value)
			require.NoError(t, err, "phase=%s state=%s", phase, state)

			want := actionPoll
			switch {
			case phase == prworkspace.PhaseComplete && state == prworkspace.ExecutionSucceeded:
				want = actionComplete
			case phase == prworkspace.PhaseComplete,
				terminalExecutionState(state),
				phase == prworkspace.PhaseTriage && state == prworkspace.ExecutionWaitingUser:
				want = actionFail
			}
			assert.Equal(t, want, snapshot.action, "phase=%s state=%s", phase, state)
		}
	}
}

func TestClassifyAggregateUsesLatestRepairAndTerminalStateBeforeHistoricalAttention(t *testing.T) {
	t.Parallel()

	value := completedAggregate()
	value.RepairAttempts = append(value.RepairAttempts, prworkspace.RepairAttempt{
		ID: "pra_22222222222222222222222222222222", StageRunID: "psr_new",
		State: prworkspace.ExecutionFailed,
	})
	snapshot, err := classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, actionFail, snapshot.action)
	assert.Equal(t, "incomplete_publication_evidence", snapshot.result.ErrorCode)
	assert.Empty(t, snapshot.result.CandidateRevision)

	value = waitingGateAggregate()
	value.Workspace.ExecutionState = prworkspace.ExecutionCanceled
	snapshot, err = classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, actionFail, snapshot.action)
	assert.Equal(t, "development_canceled", snapshot.result.ErrorCode)

	value = waitingGateAggregate()
	value.Workspace.Phase = prworkspace.PhasePublication
	value.Workspace.ExecutionState = prworkspace.ExecutionUnknown
	value.Publications[0].State = prworkspace.ExecutionUnknown
	value.Gates[0].DecisionPoint = "pr.publication.reconcile"
	value.Gates[0].TargetID = "ppb_other"
	snapshot, err = classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, actionFail, snapshot.action)
	assert.Equal(t, "development_outcome_unknown", snapshot.result.ErrorCode)

	value.Gates[0].TargetID = value.Publications[0].ID
	snapshot, err = classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, actionGate, snapshot.action)

	value.Gates = nil
	snapshot, err = classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, actionReconcile, snapshot.action)
}

func TestClassifyAggregateSurfacesPublicationFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		publicCode string
		want       string
	}{
		{name: "ordinary", want: "publication_failed"},
		{name: "unsafe provider", publicCode: "unsafe_provider", want: "unsafe_provider"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			value := completedAggregate()
			value.Workspace.Phase = prworkspace.PhasePublication
			value.Workspace.ExecutionState = prworkspace.ExecutionRunning
			value.Publications[0].State = prworkspace.ExecutionFailed
			value.Publications[0].PublicErrorCode = test.publicCode
			snapshot, err := classifyAggregate("request", value)
			require.NoError(t, err)
			assert.Equal(t, actionFail, snapshot.action)
			assert.Equal(t, test.want, snapshot.result.ErrorCode)
		})
	}
}

func TestClassifyAggregateDoesNotReconfirmRevisedOrStoppedCharter(t *testing.T) {
	t.Parallel()

	charter := prworkspace.Charter{ID: "pcr_11111111111111111111111111111111", Revision: 1}
	value := completedAggregate()
	value.Workspace.Phase = prworkspace.PhaseCharter
	value.Workspace.ExecutionState = prworkspace.ExecutionWaitingUser
	value.Workspace.ActiveCharterID = ""
	value.Charters = []prworkspace.Charter{charter}
	value.Gates = []prworkspace.GateRun{{
		DecisionPoint: "pr.charter.confirm", TargetID: charter.ID,
		State: prworkspace.ExecutionSucceeded,
		Turns: []prworkspace.GateTurn{{
			Status: "completed", FieldValues: map[string]any{"action": "revise"},
		}},
	}}
	snapshot, err := classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, actionFail, snapshot.action)
	assert.Equal(t, "charter_revision_required", snapshot.result.ErrorCode)

	value.Workspace.ExecutionState = prworkspace.ExecutionBlocked
	value.Gates[0].Turns[0].FieldValues["action"] = "stop"
	snapshot, err = classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, actionFail, snapshot.action)
	assert.Equal(t, "development_blocked", snapshot.result.ErrorCode)
}

func TestClassifyAggregateSurfacesPersistedImplementationUnavailable(t *testing.T) {
	t.Parallel()

	value := completedAggregate()
	value.Workspace.Phase = prworkspace.PhaseTriage
	value.Workspace.ExecutionState = prworkspace.ExecutionFailed
	value.RepairAttempts = nil
	value.ValidationRuns = nil
	value.Publications = nil
	value.Activity = []prworkspace.Activity{{
		Kind:     "development.failed",
		Metadata: map[string]any{"code": "implementation_unavailable"},
	}}
	snapshot, err := classifyAggregate("request", value)
	require.NoError(t, err)
	assert.Equal(t, actionFail, snapshot.action)
	assert.Equal(t, "implementation_unavailable", snapshot.result.ErrorCode)
}

func TestStateValidationHelperEdgeCases(t *testing.T) {
	t.Parallel()

	assert.False(t, validDevelopmentPhase(prworkspace.Phase("future")))
	assert.False(t, validExecutionState(prworkspace.ExecutionState("future")))
	assert.False(t, validBranchName(""))
	assert.False(t, validBranchName(" branch"))
	assert.False(t, validBranchName("branch\nname"))
	assert.False(t, validBranchName("branch\u200ename"))
	assert.False(t, validBranchName(string([]byte{0xff})))

	charter := prworkspace.Charter{ID: "pcr_11111111111111111111111111111111", Confirmed: true}
	aggregate := completedAggregate()
	aggregate.Workspace.Phase = prworkspace.PhaseCharter
	aggregate.Workspace.ActiveCharterID = ""
	aggregate.Charters = []prworkspace.Charter{charter}
	_, pending := pendingCharterDecision(aggregate)
	assert.False(t, pending)
	_, revisionRequired := charterRevisionRequired(aggregate)
	assert.False(t, revisionRequired)

	gates := []prworkspace.GateRun{
		{
			DecisionPoint: "pr.charter.confirm", TargetID: "different",
			State: prworkspace.ExecutionSucceeded,
		},
		{
			DecisionPoint: "pr.charter.confirm", TargetID: charter.ID,
			State: prworkspace.ExecutionRunning,
		},
	}
	_, found := latestCharterGateAction(gates, charter.ID)
	assert.False(t, found)

	aggregate = completedAggregate()
	aggregate.Workspace.Phase = prworkspace.PhaseValidation
	aggregate.Workspace.ExecutionState = prworkspace.ExecutionFailed
	aggregate.ValidationRuns[0].State = prworkspace.ExecutionFailed
	aggregate.ValidationRuns[0].Checks[0].Status = "failed"
	snapshot, err := classifyAggregate("request", aggregate)
	require.NoError(t, err)
	assert.Equal(t, "validation_failed", snapshot.result.ErrorCode)
}

func TestPendingGateProjectionBoundsAndSanitizesAllFields(t *testing.T) {
	t.Parallel()

	gate := prworkspace.GateRun{
		ID: "pgr_11111111111111111111111111111111", DecisionPoint: "review",
		SubjectRevision: strings.Repeat("s", 300),
		Evidence: prworkspace.GateEvidence{
			CandidateSHA:    strings.Repeat("c", 300),
			ChangedFiles:    []string{"safe.go", "bad\npath.go"},
			ValidationState: prworkspace.ExecutionSucceeded,
			FindingCount:    2, PublicationKind: prworkspace.PublicationBranchPush,
			Repository: "octo/repo",
		},
	}
	form := &prworkspace.GateForm{
		Prompt: "Prompt\x1b[31m",
		Fields: []gatetypes.GateField{
			{ID: "ignored", Type: gatetypes.GateFieldType("unsupported")},
			{
				ID: "action", Type: gatetypes.GateFieldSelect, Label: "Action", Required: true,
				Options: []gatetypes.GateFieldOption{{ID: "accept", Label: "Accept"}},
			},
		},
	}
	projected := projectPendingGate(gate, form)
	require.NotNil(t, projected)
	assert.Len(t, projected.Fields, 1)
	assert.Equal(t, []string{"safe.go", "bad path.go"}, projected.Evidence.ChangedFiles)
	assert.Len(t, projected.SubjectRevision, 256)
	assert.Len(t, projected.Evidence.CandidateRevision, 256)
	assert.NotContains(t, projected.Prompt, "\x1b")
	assert.False(t, gateFieldTypeSupported(gatetypes.GateFieldType("unsupported")))
	assert.Equal(t, "é", boundedTerminalText("éé", len("é")))
}

func completedAggregate() prworkspace.Aggregate {
	usage := &prworkspace.ImplementationUsage{
		Scope: prworkspace.ImplementationUsageScope, Complete: true,
		Repair: prworkspace.TokenUsage{
			ProviderCalls: 1, UsageReportedCalls: 1,
			PromptTokens: 10, CachedTokens: 4, CompletionTokens: 3, TotalTokens: 13,
		},
		Audit: prworkspace.TokenUsage{
			ProviderCalls: 1, UsageReportedCalls: 1,
			PromptTokens: 5, CachedTokens: 2, CompletionTokens: 2, TotalTokens: 7,
		},
		Total: prworkspace.TokenUsage{
			ProviderCalls: 2, UsageReportedCalls: 2,
			PromptTokens: 15, CachedTokens: 6, CompletionTokens: 5, TotalTokens: 20,
		},
	}
	return prworkspace.Aggregate{
		Workspace: prworkspace.Workspace{
			ID: "devw_11111111111111111111111111111111", Phase: prworkspace.PhaseComplete,
			Intent: prworkspace.IntentImplementFeature, SourceKind: prworkspace.SourceBrief,
			ProviderOrigin: "https://github.com", RepositoryID: "42", Repository: "octo/repo",
			ExecutionState: prworkspace.ExecutionSucceeded, Version: 9,
		},
		ProviderSnapshot: prworkspace.ProviderSnapshot{
			Intent: prworkspace.IntentImplementFeature, SourceKind: prworkspace.SourceBrief,
			ProviderOrigin: "https://github.com", Repository: "octo/repo",
			RepositoryID: "42", PullRequestID: "17", PullNumber: 17,
			HeadRef: "feature-branch", HeadSHA: "commit",
		},
		RepairAttempts: []prworkspace.RepairAttempt{{
			ID: "pra_11111111111111111111111111111111", StageRunID: "psr_stage",
			State: prworkspace.ExecutionSucceeded, CandidateSHA: "commit",
		}},
		StageRuns: []prworkspace.StageRun{{
			ID: "psr_stage", Stage: "implementation", State: prworkspace.ExecutionSucceeded,
			Usage: usage,
		}},
		ValidationRuns: []prworkspace.ValidationRun{{
			ID: "pvr_11111111111111111111111111111111", StageRunID: "psr_stage",
			RepairAttemptID: "pra_11111111111111111111111111111111",
			State:           prworkspace.ExecutionSucceeded, CandidateSHA: "tree",
			Checks: []prworkspace.ValidationCheck{{ID: "test", Name: "tests", Status: "passed"}},
		}},
		Publications: []prworkspace.Publication{{
			ID:   "ppb_11111111111111111111111111111111",
			Kind: prworkspace.PublicationBranchPush, State: prworkspace.ExecutionSucceeded,
			TargetID: "pra_11111111111111111111111111111111", ExternalID: "17",
			ExternalURL: "https://github.com/octo/repo/pull/17",
		}},
	}
}
