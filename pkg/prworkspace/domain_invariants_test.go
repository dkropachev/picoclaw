package prworkspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func completionFindingFixture(
	presence WorkPresence,
	distance ScopeDistance,
	size ChangeSize,
	compatible bool,
) CompletionFinding {
	result := CompletionFinding{
		AgentFinding: AgentFinding{
			Severity: "high", Title: "retry bug", File: "pkg/retry.go", Message: "The retry path remains incomplete.",
			Evidence: "retry remains incomplete", Impact: "requests fail",
			Validation: "Traced the incomplete retry path.", ScopeDistance: distance, ChangeSize: size,
			TypeCompatible: compatible, ScopeConfidence: 1, ScopeExplanation: "graded against retry charter",
			CharterClauses: []string{"fix retry"},
		},
		Presence: presence,
	}
	if presence == WorkCandidatePresent {
		result.Hunk, result.Module, result.SemanticLines = testCandidateHunk, "pkg", 10
	}
	return result
}

func TestCompletionMissingReopensMatchingUnresolvedFindingInsteadOfDroppingDuplicate(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	candidate := completionFindingFixture(WorkFollowUp, ScopeExact, ChangeSizeXS, true)
	existing := Finding{
		ID:          "pfn_11111111111111111111111111111111",
		Fingerprint: agentFindingFingerprint(candidate.AgentFinding),
		Origin:      FindingOriginReview,
		OriginRunID: "psr_22222222222222222222222222222222",
		Severity:    candidate.Severity,
		Title:       candidate.Title,
		File:        candidate.File,
		Message:     candidate.Message,
		Scope:       completionFindingScope(candidate),
		Disposition: FindingDeferred,
		Version:     4,
		CreatedAt:   now.Add(-time.Hour),
		UpdatedAt:   now.Add(-time.Hour),
	}
	aggregate := Aggregate{
		Workspace: Workspace{
			ID:              "devw_11111111111111111111111111111111",
			ActiveCharterID: "pcr_11111111111111111111111111111111",
		},
		ProviderSnapshot: ProviderSnapshot{HeadSHA: "head"},
		Charters: []Charter{{
			ID: "pcr_11111111111111111111111111111111", Confirmed: true, HeadSHA: "head",
		}},
		StageRuns: []StageRun{{
			ID: existing.OriginRunID, Stage: "review", State: ExecutionSucceeded,
			CharterID: "pcr_11111111111111111111111111111111", HeadSHA: "head",
		}},
		Findings: []Finding{existing},
	}
	rounds := []CompletionRound{{Initial: true, Result: CompletionPass{Missing: []CompletionFinding{candidate}}}}
	missing, deferred, drift, _ := materializeCompletionRounds(
		aggregate,
		"psr_11111111111111111111111111111111",
		rounds,
		ConfiguredNudgePolicy(0, 0),
		now,
	)
	if len(missing) != 1 || len(deferred) != 0 || len(drift) != 0 {
		t.Fatalf("materialized missing=%#v deferred=%#v drift=%#v", missing, deferred, drift)
	}
	if missing[0].ID != existing.ID || missing[0].Version != existing.Version+1 ||
		missing[0].Disposition != FindingInScope {
		t.Fatalf("matching blocker was not reopened in place: %#v", missing[0])
	}
}

type invariantImplementationAI struct {
	completion map[string]any
}

func (runner invariantImplementationAI) RunIsolated(
	_ context.Context,
	request IsolatedAIRequest,
) (map[string]any, error) {
	switch request.Operation {
	case "scope.audit":
		return exactScopeAuditFixture(), nil
	case "completion.initial":
		return runner.completion, nil
	case "nudge.plan":
		return nil, context.Canceled
	default:
		return nil, errors.New("unexpected isolated operation")
	}
}

type hardRepairScopeAI struct {
	distance   ScopeDistance
	compatible bool
}

func (runner hardRepairScopeAI) RunIsolated(_ context.Context, request IsolatedAIRequest) (map[string]any, error) {
	switch request.Operation {
	case "scope.audit":
		response := exactScopeAuditFixture()
		change := response["changes"].([]any)[0].(map[string]any)
		change["scope_distance"] = string(runner.distance)
		change["type_compatible"] = runner.compatible
		response["worst_scope_distance"] = string(runner.distance)
		response["type_compatible"] = runner.compatible
		return response, nil
	case "completion.initial":
		return map[string]any{
			"summary": "candidate behavior is complete", "complete": true,
			"missing_in_scope": []any{}, "out_of_scope": []any{}, "coverage": coverageJSON(),
		}, nil
	case "nudge.plan":
		return nil, context.Canceled
	default:
		return nil, errors.New("unexpected isolated operation")
	}
}

func exactScopeAuditFixture() map[string]any {
	return map[string]any{
		"changes": []any{
			map[string]any{
				"path":            "pkg/retry.go",
				"hunk":            testCandidateHunk,
				"module":          "pkg/retry",
				"semantic_lines":  10,
				"presence":        "candidate_present",
				"scope_distance":  "S0_exact",
				"change_size":     "XS",
				"type_compatible": true,
				"confidence":      1.0,
				"charter_clauses": []any{"fix retry"},
				"explanation":     "exact charter work",
			},
		},
		"files": 1, "semantic_lines": 10, "modules": 1,
		"worst_scope_distance": "S0_exact", "worst_change_size": "XS",
		"type_compatible": true, "confidence": 1.0,
		"charter_clauses": []any{"fix retry"}, "explanation": "candidate matches the charter",
	}
}

func completionFindingJSON(presence, distance, size string, compatible bool) map[string]any {
	value := map[string]any{
		"severity":          "high",
		"title":             "retry bug",
		"file":              "pkg/retry.go",
		"message":           "The retry path remains incomplete.",
		"evidence":          "retry remains incomplete",
		"impact":            "requests fail",
		"validation":        "Traced the incomplete retry path.",
		"scope_distance":    distance,
		"change_size":       size,
		"type_compatible":   compatible,
		"scope_confidence":  1.0,
		"scope_explanation": "graded against retry charter",
		"charter_clauses":   []any{"fix retry"},
		"presence":          presence,
		"hunk":              "",
		"module":            "",
		"semantic_lines":    0,
	}
	if presence == "candidate_present" {
		value["hunk"], value["module"], value["semantic_lines"] = testCandidateHunk, "pkg", 10
	}
	return value
}

func coverageJSON() map[string]any {
	return map[string]any{
		"reviewed_areas": []any{}, "unreviewed_areas": []any{},
		"tests_considered": []any{}, "residual_risks": []any{},
	}
}

func TestImplementationDoesNotCompleteWhenCompletionAuditRepeatsExistingFinding(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	candidate := completionFindingFixture(WorkFollowUp, ScopeExact, ChangeSizeXS, true)
	existing := aggregate.Findings[0]
	existing.Fingerprint = agentFindingFingerprint(candidate.AgentFinding)
	existing.File = candidate.File
	seeded, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000018", Patch: AggregatePatch{UpsertFindings: []Finding{existing}},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.ai.Runner = invariantImplementationAI{completion: map[string]any{
		"summary": "retry is still missing", "complete": false,
		"missing_in_scope": []any{completionFindingJSON("follow_up", "S0_exact", "XS", true)},
		"out_of_scope":     []any{}, "coverage": coverageJSON(),
	}}
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: &implementationRepair{}, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: seeded.Aggregate.Workspace.ID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
		RequestID: "request-00000020", FindingIDs: []string{existing.ID},
		NudgePolicy: ConfiguredNudgePolicy(0, 0), MaxCycles: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	implementationStage := result.StageRuns[len(result.StageRuns)-1]
	if result.Workspace.Phase != PhaseImplementation || result.Workspace.ExecutionState != ExecutionBlocked ||
		implementationStage.PublicError != "completion_incomplete" {
		t.Fatalf("false completion was admitted: workspace=%#v stage=%#v", result.Workspace, implementationStage)
	}
	reopened, index := findFinding(result.Findings, existing.ID)
	if index < 0 || reopened.Disposition != FindingInScope || reopened.Version <= existing.Version {
		t.Fatalf("duplicate missing finding was not reopened: %#v", result.Findings)
	}
}

type scopeWaitingGates struct{}

func (scopeWaitingGates) Start(_ context.Context, request GateRequest) (GateRun, error) {
	gate := testSucceededGate(request)
	if request.DecisionPoint == "pr.implementation.scope" || request.DecisionPoint == "pr.implementation.hard-scope" ||
		request.DecisionPoint == "pr.finding.classify" {
		gate = testWaitingGate(request)
	}
	return gate, nil
}

func (scopeWaitingGates) Respond(_ context.Context, gate GateRun, fieldValues map[string]any) (GateRun, error) {
	return answerTestGate(gate, fieldValues), nil
}

func TestCandidatePresentCompletionScopeDriftWaitsForExplicitScopeGate(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	service.gates = scopeWaitingGates{}
	service.ai.Runner = invariantImplementationAI{completion: map[string]any{
		"summary": "requirements complete but adjacent cleanup is in the candidate", "complete": true,
		"missing_in_scope": []any{},
		"out_of_scope":     []any{completionFindingJSON("candidate_present", "S2_related_followup", "XS", true)},
		"coverage":         coverageJSON(),
	}}
	result, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: &implementationRepair{}, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000020", FindingIDs: []string{aggregate.Findings[0].ID},
		NudgePolicy: ConfiguredNudgePolicy(0, 0), MaxCycles: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Workspace.Phase != PhaseImplementation || result.Workspace.ExecutionState != ExecutionWaitingGate {
		t.Fatalf("candidate drift was not fenced: %#v", result.Workspace)
	}
	var drift *Finding
	var scopeGate *GateRun
	for index := range result.Findings {
		if result.Findings[index].Scope.Presence == WorkCandidatePresent &&
			result.Findings[index].Scope.Distance == ScopeRelatedFollowup {
			drift = &result.Findings[index]
		}
	}
	for index := range result.Gates {
		if result.Gates[index].DecisionPoint == "pr.implementation.hard-scope" {
			scopeGate = &result.Gates[index]
		}
	}
	if drift == nil || drift.Disposition != FindingOpen || scopeGate == nil || scopeGate.State != ExecutionWaitingUser {
		t.Fatalf("candidate drift/gate missing: findings=%#v gates=%#v", result.Findings, result.Gates)
	}
	if !scopeGate.Evidence.HardScope || len(scopeGate.Evidence.HardScopeFindingIDs) != 1 ||
		scopeGate.Evidence.HardScopeFindingIDs[0] != drift.ID {
		t.Fatalf("hard scope was not pinned to only the drift finding: %#v", scopeGate.Evidence)
	}
	correctedScope := drift.Scope
	correctedScope.Distance, correctedScope.TypeCompatible = ScopeExact, true
	if _, editErr := service.UpdateFinding(context.Background(), UpdateFindingRequest{
		WorkspaceID: result.Workspace.ID, FindingID: drift.ID, ExpectedVersion: result.Workspace.Version,
		RequestID: "request-00000020-edit", Severity: drift.Severity, Title: drift.Title,
		Message: drift.Message, Evidence: drift.Evidence, Scope: correctedScope,
	}); editErr == nil {
		t.Fatal("mutable finding edit rewrote frozen hard-scope evidence")
	}
	if _, passErr := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     result.Workspace.ID,
		GateRunID:       scopeGate.ID,
		ExpectedVersion: result.Workspace.Version,
		RequestID:       "request-00000021",
		FieldValues:     map[string]any{"action": "approve"},
	}); passErr == nil {
		t.Fatal("hard candidate scope accepted a generic gate pass")
	}
	resolved, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     result.Workspace.ID,
		GateRunID:       scopeGate.ID,
		ExpectedVersion: result.Workspace.Version,
		RequestID:       "request-00000022",
		FieldValues:     map[string]any{"action": "defer-follow-up"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Workspace.Phase != PhaseImplementation || resolved.Workspace.ExecutionState != ExecutionQueued {
		t.Fatalf("defer did not schedule candidate removal: %#v", resolved.Workspace)
	}
	removal, removalIndex := findFinding(resolved.Findings, drift.ID)
	if removalIndex < 0 || removal.Disposition != FindingInScope || !findingRemovalAuthorized(resolved.Gates, removal) {
		t.Fatalf("candidate removal is not operationally selectable: %#v", removal)
	}
	followUps := 0
	for _, finding := range resolved.Findings {
		if finding.Scope.Presence == WorkFollowUp && finding.Disposition == FindingDeferred &&
			finding.Scope.Distance == ScopeRelatedFollowup {
			followUps++
		}
	}
	if followUps != 1 {
		t.Fatalf("deferred follow-up copy count = %d, findings=%#v", followUps, resolved.Findings)
	}
	service.ai.Runner = serviceAI{}
	service.gates = passingGates{}
	repair := &implementationRepair{}
	cleaned, err := service.RunImplementation(context.Background(), ImplementationConfig{
		Repair: repair, Validation: implementationValidation{},
	}, RunImplementationRequest{
		WorkspaceID: resolved.Workspace.ID, ExpectedVersion: resolved.Workspace.Version,
		RequestID: "request-00000023", FindingIDs: []string{removal.ID},
		NudgePolicy: ConfiguredNudgePolicy(0, 0), MaxCycles: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(repair.last.Instruction, "Remove candidate-present scope drift") {
		t.Fatalf("removal intent did not reach the repair prompt: %q", repair.last.Instruction)
	}
	removed, removedIndex := findFinding(cleaned.Findings, removal.ID)
	if cleaned.Workspace.Phase != PhasePublication || removedIndex < 0 || removed.Disposition != FindingFixed {
		t.Fatalf("clean removal did not unlock publication: workspace=%#v finding=%#v", cleaned.Workspace, removed)
	}
}

func TestHardRepairScopeCannotReachPublicationThroughGateApproval(t *testing.T) {
	tests := []struct {
		name       string
		distance   ScopeDistance
		compatible bool
	}{
		{name: "related", distance: ScopeRelatedFollowup, compatible: true},
		{name: "unrelated", distance: ScopeUnrelated, compatible: true},
		{name: "type mismatch", distance: ScopeExact, compatible: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, aggregate := readyImplementationService(t)
			service.ai.Runner = hardRepairScopeAI{distance: test.distance, compatible: test.compatible}
			result, err := service.RunImplementation(context.Background(), ImplementationConfig{
				Repair: &implementationRepair{}, Validation: implementationValidation{},
			}, RunImplementationRequest{
				WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
				RequestID: "request-00000030", FindingIDs: []string{aggregate.Findings[0].ID},
				NudgePolicy: ConfiguredNudgePolicy(0, 0), MaxCycles: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Workspace.Phase == PhasePublication || result.Workspace.ExecutionState != ExecutionWaitingGate {
				t.Fatalf("hard scope reached publication: %#v", result.Workspace)
			}
			if len(result.ValidationRuns) != 0 || len(result.RepairAttempts) != 1 ||
				result.RepairAttempts[0].PublicationFence != nil {
				t.Fatalf(
					"hard scope advanced into validation or finalization: repairs=%#v validations=%#v",
					result.RepairAttempts,
					result.ValidationRuns,
				)
			}
			var scopeGate *GateRun
			for index := range result.Gates {
				if result.Gates[index].DecisionPoint == "pr.implementation.hard-scope" {
					scopeGate = &result.Gates[index]
				}
			}
			if scopeGate == nil || !scopeGate.Evidence.HardScope {
				t.Fatalf("hard resolution gate missing: %#v", result.Gates)
			}
			if _, err = service.RespondGate(context.Background(), RespondGateRequest{
				WorkspaceID: result.Workspace.ID, GateRunID: scopeGate.ID,
				ExpectedVersion: result.Workspace.Version, RequestID: "request-00000031",
				FieldValues: map[string]any{"action": "approve"},
			}); err == nil {
				t.Fatal("hard repair scope accepted pass")
			}
		})
	}
}

func TestOrdinaryImplementationScopeActionsDeferOrReviseAsAdvertised(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	finding := aggregate.Findings[0]
	gate := GateRun{
		ID: "pgr_99999999999999999999999999999999", DecisionPoint: "pr.implementation.scope",
		TargetID: "pra_99999999999999999999999999999999", State: ExecutionSucceeded,
		Evidence: GateEvidence{FindingIDs: []string{finding.ID}, ScopeResolutionIDs: []string{finding.ID}},
		Turns:    []GateTurn{{Status: "answered", FieldValues: map[string]any{"action": "defer-follow-up"}}},
	}
	deferred, err := service.gateActionPatch(aggregate, gate)
	if err != nil || deferred.Phase == nil || *deferred.Phase != PhaseImplementation ||
		deferred.ExecutionState == nil || *deferred.ExecutionState != ExecutionQueued ||
		len(deferred.UpsertFindings) != 2 || deferred.UpsertFindings[1].Disposition != FindingDeferred ||
		deferred.UpsertFindings[1].Scope.Presence != WorkFollowUp {
		t.Fatalf("ordinary defer patch = %#v, error = %v", deferred, err)
	}
	gate.Turns[0].FieldValues = map[string]any{"action": "revise-charter"}
	revised, err := service.gateActionPatch(aggregate, gate)
	if err != nil || revised.Phase == nil || *revised.Phase != PhaseCharter ||
		revised.ExecutionState == nil || *revised.ExecutionState != ExecutionWaitingUser {
		t.Fatalf("ordinary revise patch = %#v, error = %v", revised, err)
	}
}

func TestBranchPublicationRejectsSuccessfulHardScopeRepair(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	now := aggregate.Workspace.UpdatedAt
	stage := StageRun{
		ID: "psr_99999999999999999999999999999999", Stage: "implementation", State: ExecutionSucceeded,
		CharterID: aggregate.Workspace.ActiveCharterID, HeadSHA: aggregate.ProviderSnapshot.HeadSHA,
		Attempt: 1, StartedAt: now, FinishedAt: &now,
	}
	repair := RepairAttempt{
		ID: "pra_99999999999999999999999999999999", StageRunID: stage.ID, Number: 1,
		State: ExecutionSucceeded, CandidateSHA: "hard-scope-candidate",
		Scope: ScopeAssessment{
			Presence: WorkCandidatePresent, Distance: ScopeRelatedFollowup, Size: ChangeSizeXS,
			TypeCompatible: true, Confidence: 1,
		},
		PublicationFence: &ImplementationPublicationFence{BaseCommit: aggregate.ProviderSnapshot.HeadSHA},
		StartedAt:        now, FinishedAt: &now,
	}
	phase := PhasePublication
	seeded, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000032", Patch: AggregatePatch{
			Phase: &phase, AppendStageRuns: []StageRun{stage}, AppendRepairs: []RepairAttempt{repair},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.QueueBranchPublication(context.Background(), QueueBranchPublicationRequest{
		WorkspaceID: seeded.Aggregate.Workspace.ID, ExpectedVersion: seeded.Aggregate.Workspace.Version,
		RequestID: "request-00000033", ExpectedHeadSHA: seeded.Aggregate.ProviderSnapshot.HeadSHA,
	}); err == nil {
		t.Fatal("branch publication accepted a hard-scope repair")
	}
}

type fixedResponseAI struct{ response map[string]any }

func (runner fixedResponseAI) RunIsolated(context.Context, IsolatedAIRequest) (map[string]any, error) {
	return runner.response, nil
}

func TestScopeAuditDefersUntrustedMetricsToDeterministicCandidateBinding(t *testing.T) {
	response := exactScopeAuditFixture()
	change := response["changes"].([]any)[0].(map[string]any)
	change["module"] = "model-invented-module"
	change["semantic_lines"] = 9
	response["files"] = 42
	response["semantic_lines"] = 999
	response["modules"] = 17
	response["worst_scope_distance"] = string(ScopeUnrelated)
	response["worst_change_size"] = string(ChangeSizeL)
	response["type_compatible"] = false

	audit, _, err := (AIController{Runner: fixedResponseAI{response: response}}).RunScopeAudit(
		context.Background(),
		testPromptBundle(),
	)
	if err != nil {
		t.Fatalf("scope audit rejected valid classifications with advisory metrics: %v", err)
	}
	canonical, mismatch := bindScopeAuditCandidateEvidence(audit, RepairResult{
		ChangedFiles: []string{"pkg/retry.go"}, SemanticLines: 10, Modules: 1,
		CandidateDiff: testCandidateDiff,
	})
	if mismatch != "" {
		t.Fatalf("deterministic candidate binding mismatch = %q", mismatch)
	}
	if canonical.Files != 1 || canonical.SemanticLines != 10 || canonical.Modules != 1 ||
		canonical.WorstDistance != ScopeExact || canonical.WorstSize != ChangeSizeXS ||
		!canonical.TypeCompatible || canonical.Changes[0].Module != "pkg" ||
		canonical.Changes[0].SemanticLines != 10 {
		t.Fatalf("canonical scope audit = %#v", canonical)
	}
}

func TestScopeAuditEvidenceMustMatchEveryExactCandidateHunk(t *testing.T) {
	audit := ScopeAuditPass{
		Changes: []ScopeChange{{
			Path: "pkg/retry.go", Hunk: testCandidateHunk, Module: "pkg/retry", SemanticLines: 10,
			Presence: WorkCandidatePresent, Distance: ScopeExact, Size: ChangeSizeXS,
			TypeCompatible: true, Confidence: 1, Explanation: "exact charter work",
		}},
		Files: 1, SemanticLines: 10, Modules: 1, WorstDistance: ScopeExact,
		WorstSize: ChangeSizeXS, TypeCompatible: true, Confidence: 1, Explanation: "exact candidate",
	}
	repair := RepairResult{
		ChangedFiles: []string{"pkg/retry.go"}, SemanticLines: 10, Modules: 1,
		CandidateDiff: testCandidateDiff,
	}
	if !scopeAuditMatchesCandidate(audit, repair) {
		t.Fatal("exact path/hunk evidence did not match the trusted candidate diff")
	}

	wrongHeader := audit
	wrongHeader.Changes = append([]ScopeChange(nil), audit.Changes...)
	wrongHeader.Changes[0].Hunk = "@@ -10 +10 @@"
	if scopeAuditMatchesCandidate(wrongHeader, repair) {
		t.Fatal("fabricated hunk header matched the candidate")
	}

	wrongLines := audit
	wrongLines.Changes = append([]ScopeChange(nil), audit.Changes...)
	wrongLines.Changes[0].SemanticLines = 9
	canonical, mismatch := bindScopeAuditCandidateEvidence(wrongLines, repair)
	if mismatch != "" || canonical.Changes[0].SemanticLines != 10 {
		t.Fatalf("untrusted per-hunk arithmetic was not replaced: mismatch=%q audit=%#v", mismatch, canonical)
	}

	omittedHunk := repair
	omittedHunk.CandidateDiff += "@@ -20 +20 @@\n-old extra\n+new extra\n"
	omittedHunk.SemanticLines = 12
	omittedAudit := audit
	omittedAudit.SemanticLines = 12
	if scopeAuditMatchesCandidate(omittedAudit, omittedHunk) {
		t.Fatal("an omitted real candidate hunk matched the scope audit")
	}
}

func TestScopeAuditCanonicalizesRedistributedMultiHunkCounts(t *testing.T) {
	diff := "diff --git a/greeting/time_aware_test.go b/greeting/time_aware_test.go\n" +
		"--- a/greeting/time_aware_test.go\n+++ b/greeting/time_aware_test.go\n" +
		"@@ -1,2 +1,3 @@ first context\n-old one\n-old two\n+new one\n+new two\n+new three\n" +
		"@@ -10 +11,2 @@ second context\n-old extra\n+new extra\n+another extra\n"
	audit := ScopeAuditPass{
		Changes: []ScopeChange{
			{
				Path:           "greeting/time_aware_test.go",
				Hunk:           "@@ -1,2 +1,3 @@ copied label",
				Module:         "tests",
				SemanticLines:  4,
				Presence:       WorkCandidatePresent,
				Distance:       ScopeExact,
				Size:           ChangeSizeXS,
				TypeCompatible: true,
				Confidence:     .9,
				Explanation:    "exact charter test",
			},
			{
				Path:           "greeting/time_aware_test.go",
				Hunk:           "@@ -10 +11,2 @@",
				Module:         "different-model-label",
				SemanticLines:  4,
				Presence:       WorkCandidatePresent,
				Distance:       ScopeRelatedFollowup,
				Size:           ChangeSizeS,
				TypeCompatible: false,
				Confidence:     .6,
				Explanation:    "related scope drift",
			},
		},
		Files: 1, SemanticLines: 8, Modules: 2, WorstDistance: ScopeExact,
		WorstSize: ChangeSizeXS, TypeCompatible: true, Confidence: 1, Explanation: "model rollup",
	}
	canonical, mismatch := bindScopeAuditCandidateEvidence(audit, RepairResult{
		ChangedFiles: []string{"greeting/time_aware_test.go"}, SemanticLines: 8, Modules: 1, CandidateDiff: diff,
	})
	if mismatch != "" {
		t.Fatalf("redistributed multi-hunk counts did not bind: %q", mismatch)
	}
	if canonical.Changes[0].SemanticLines != 5 || canonical.Changes[1].SemanticLines != 3 ||
		canonical.Changes[0].Module != "greeting" || canonical.Changes[1].Module != "greeting" ||
		canonical.SemanticLines != 8 || canonical.Modules != 1 ||
		canonical.WorstDistance != ScopeRelatedFollowup || canonical.WorstSize != ChangeSizeS ||
		canonical.TypeCompatible || canonical.Confidence != .6 {
		t.Fatalf("canonical multi-hunk audit = %#v", canonical)
	}
}

func TestScopeAuditHunkIdentityIgnoresOnlyTrailingContext(t *testing.T) {
	audit := ScopeAuditPass{
		Changes: []ScopeChange{{
			Path: "greeting/time_aware.go", Hunk: "@@ -9,16 +9,21 @@", Module: "greeting", SemanticLines: 6,
			Presence: WorkCandidatePresent, Distance: ScopeExact, Size: ChangeSizeXS,
			TypeCompatible: true, Confidence: 1, Explanation: "exact charter work",
		}},
		Files: 1, SemanticLines: 6, Modules: 1, WorstDistance: ScopeExact,
		WorstSize: ChangeSizeXS, TypeCompatible: true, Confidence: 1, Explanation: "exact candidate",
	}
	repair := RepairResult{
		ChangedFiles: []string{"greeting/time_aware.go"}, SemanticLines: 6, Modules: 1,
		CandidateDiff: "diff --git a/greeting/time_aware.go b/greeting/time_aware.go\n" +
			"--- a/greeting/time_aware.go\n+++ b/greeting/time_aware.go\n" +
			"@@ -9,16 +9,21 @@ var ErrInvalidHour = errors.New(\"hour must be between 0 and 23\")\n" +
			"-old\n+new one\n+new two\n+new three\n+new four\n+new five\n",
	}
	if !scopeAuditMatchesCandidate(audit, repair) {
		t.Fatal("coordinate-exact hunk without optional trailing context did not match")
	}

	withDifferentContext := audit
	withDifferentContext.Changes = append([]ScopeChange(nil), audit.Changes...)
	withDifferentContext.Changes[0].Hunk += " different descriptive label"
	if !scopeAuditMatchesCandidate(withDifferentContext, repair) {
		t.Fatal("trailing descriptive hunk context changed hunk identity")
	}

	wrongCoordinate := audit
	wrongCoordinate.Changes = append([]ScopeChange(nil), audit.Changes...)
	wrongCoordinate.Changes[0].Hunk = "@@ -10,16 +9,21 @@"
	if scopeAuditMatchesCandidate(wrongCoordinate, repair) {
		t.Fatal("different hunk coordinates matched the candidate")
	}

	malformed := audit
	malformed.Changes = append([]ScopeChange(nil), audit.Changes...)
	malformed.Changes[0].Hunk = "@@ -9,16 +9,21 @@\n@@ -100 +100 @@"
	if scopeAuditMatchesCandidate(malformed, repair) {
		t.Fatal("multiline hunk evidence matched the candidate")
	}
}

func TestExactCandidateDiffCountsSourceLinesBeginningWithDiffHeaderPrefixes(t *testing.T) {
	diff := "diff --git a/pkg/counter.go b/pkg/counter.go\n" +
		"--- a/pkg/counter.go\n+++ b/pkg/counter.go\n" +
		"@@ -1 +1 @@\n---flag\n+++counter\n"
	hunk := "@@ -1 +1 @@"
	audit := ScopeAuditPass{
		Changes: []ScopeChange{{
			Path: "pkg/counter.go", Hunk: hunk, Module: "pkg", SemanticLines: 2,
			Presence: WorkCandidatePresent, Distance: ScopeExact, Size: ChangeSizeXS,
			TypeCompatible: true, Confidence: 1, Explanation: "exact counter change",
		}},
		Files: 1, SemanticLines: 2, Modules: 1, WorstDistance: ScopeExact,
		WorstSize: ChangeSizeXS, TypeCompatible: true, Confidence: 1, Explanation: "exact candidate",
	}
	repair := RepairResult{
		ChangedFiles: []string{"pkg/counter.go"}, SemanticLines: 2, Modules: 1, CandidateDiff: diff,
	}
	if !scopeAuditMatchesCandidate(audit, repair) {
		t.Fatal("legitimate +++/--- source content was mistaken for a diff file header")
	}
}

func TestCompletionCandidateEvidenceMustMatchPersistedScopeAudit(t *testing.T) {
	candidate := completionFindingFixture(WorkCandidatePresent, ScopeRelatedFollowup, ChangeSizeXS, true)
	scope := ScopeAssessment{ChangeEvidence: []ScopeChange{{
		Path: "pkg/retry.go", Hunk: testCandidateHunk, Module: "pkg", SemanticLines: 10,
		Presence: WorkCandidatePresent, Distance: ScopeExact, Size: ChangeSizeXS,
		TypeCompatible: true, Confidence: 1,
	}}}
	rounds := []CompletionRound{
		{State: ExecutionSucceeded, Result: CompletionPass{OutOfScope: []CompletionFinding{candidate}}},
	}
	if !completionRoundsMatchCandidateScope(rounds, scope) {
		t.Fatal("exact completion finding did not match persisted scope evidence")
	}

	wrong := candidate
	wrong.Hunk = "@@ -99 +99 @@"
	rounds[0].Result.OutOfScope[0] = wrong
	if completionRoundsMatchCandidateScope(rounds, scope) {
		t.Fatal("fabricated completion hunk matched persisted scope evidence")
	}

	wrong = candidate
	wrong.SemanticLines--
	rounds[0].Result.OutOfScope[0] = wrong
	if completionRoundsMatchCandidateScope(rounds, scope) {
		t.Fatal("incorrect completion semantic-line count matched persisted scope evidence")
	}
}

func TestReviewFindingClassificationUsesScopeGrades(t *testing.T) {
	tests := []struct {
		scope ScopeAssessment
		want  FindingDisposition
	}{
		{ScopeAssessment{Distance: ScopeExact, Size: ChangeSizeXS, TypeCompatible: true}, FindingInScope},
		{ScopeAssessment{Distance: ScopeRelatedFollowup, Size: ChangeSizeXS, TypeCompatible: true}, FindingDeferred},
		{ScopeAssessment{Distance: ScopeExact, Size: ChangeSizeXS, TypeCompatible: false}, FindingDeferred},
		{ScopeAssessment{Distance: ScopeNecessaryAdjacent, Size: ChangeSizeXS, TypeCompatible: true}, FindingOpen},
		{ScopeAssessment{Distance: ScopeExact, Size: ChangeSizeM, TypeCompatible: true}, FindingOpen},
	}
	for _, test := range tests {
		if got := reviewFindingDisposition(test.scope); got != test.want {
			t.Fatalf("classification of %#v = %q, want %q", test.scope, got, test.want)
		}
	}
}

type ambiguousReviewAI struct{}

func (ambiguousReviewAI) RunIsolated(_ context.Context, request IsolatedAIRequest) (map[string]any, error) {
	if request.Operation != "review.initial" {
		return nil, errors.New("unexpected isolated operation")
	}
	return map[string]any{
		"summary": "one adjacent finding",
		"findings": []any{
			map[string]any{
				"severity":          "medium",
				"title":             "adjacent retry cleanup",
				"file":              "pkg/retry.go",
				"message":           "cleanup is adjacent",
				"evidence":          "adjacent path",
				"impact":            "maintenance",
				"validation":        "Compared the path with the confirmed charter.",
				"scope_distance":    "S1_necessary_adjacent",
				"change_size":       "XS",
				"type_compatible":   true,
				"scope_confidence":  1.0,
				"scope_explanation": "necessary-adjacent rather than exact",
				"charter_clauses":   []any{"fix retry"},
			},
		},
		"coverage": coverageJSON(),
	}, nil
}

func TestStrictPolicyDefersAdjacentReviewFindingWithoutAttentionGate(t *testing.T) {
	store := NewMemoryStore()
	created, err := store.Create(context.Background(), testCreateInput())
	if err != nil {
		t.Fatal(err)
	}
	now := created.Aggregate.Workspace.CreatedAt
	charter := Charter{
		ID:                 "pcr_44444444444444444444444444444444",
		Revision:           1,
		Type:               PRTypeFix,
		Goal:               "fix retry",
		AcceptanceCriteria: []string{"retry succeeds"},
		BaseSHA:            created.Aggregate.ProviderSnapshot.BaseSHA,
		HeadSHA:            created.Aggregate.ProviderSnapshot.HeadSHA,
		Confirmed:          true,
		CreatedAt:          now,
	}
	phase, state, active := PhaseReview, ExecutionQueued, charter.ID
	ready, err := store.Mutate(context.Background(), Mutation{
		WorkspaceID: created.Aggregate.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-00000040", Patch: AggregatePatch{
			Phase: &phase, ExecutionState: &state, ActiveCharterID: &active, AppendCharters: []Charter{charter},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{
		Store: store, AI: ambiguousReviewAI{}, ReviewEvidence: serviceReviewEvidence{},
		Gates: scopeWaitingGates{},
		Now:   func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunReview(context.Background(), RunReviewRequest{
		WorkspaceID: ready.Aggregate.Workspace.ID, ExpectedVersion: ready.Aggregate.Workspace.Version,
		RequestID: "request-00000041", NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Workspace.Phase != PhaseTriage || result.Workspace.ExecutionState != ExecutionQueued ||
		len(result.Findings) != 1 || result.Findings[0].Disposition != FindingDeferred {
		t.Fatalf("strict finding disposition: workspace=%#v findings=%#v", result.Workspace, result.Findings)
	}
	for _, gate := range result.Gates {
		if gate.DecisionPoint == "pr.finding.classify" && gate.TargetID == result.Findings[0].ID {
			t.Fatalf("strict adjacent work requested user attention: %#v", gate)
		}
	}
}

func TestExactLargeFindingBecomesSelectableOnlyAfterClassificationApproval(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	service.gates = scopeWaitingGates{}
	finding := aggregate.Findings[0]
	finding.Disposition = FindingOpen
	finding.Scope = ScopeAssessment{
		Distance: ScopeExact, Size: ChangeSizeM, Presence: WorkCandidatePresent,
		Files: 6, SemanticLines: 300, Modules: 2, TypeCompatible: true, Confidence: 1,
	}
	phase := PhaseTriage
	seeded, err := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000050", Patch: AggregatePatch{Phase: &phase, UpsertFindings: []Finding{finding}},
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := service.DecideFinding(context.Background(), FindingDecisionRequest{
		WorkspaceID: seeded.Aggregate.Workspace.ID, FindingID: finding.ID,
		ExpectedVersion: seeded.Aggregate.Workspace.Version, RequestID: "request-00000051",
		Disposition: FindingInScope, Scope: finding.Scope, Reason: "required but large",
	})
	if err != nil || waiting.Workspace.ExecutionState != ExecutionWaitingGate || len(waiting.Gates) != 1 ||
		waiting.Findings[0].Disposition != FindingOpen {
		t.Fatalf(
			"classification request = workspace %#v gates %#v findings %#v err %v",
			waiting.Workspace,
			waiting.Gates,
			waiting.Findings,
			err,
		)
	}
	accepted, err := service.RespondGate(context.Background(), RespondGateRequest{
		WorkspaceID:     waiting.Workspace.ID,
		GateRunID:       waiting.Gates[0].ID,
		ExpectedVersion: waiting.Workspace.Version,
		RequestID:       "request-00000052",
		FieldValues:     map[string]any{"action": "keep-in-pr"},
	})
	if err != nil || accepted.Findings[0].Disposition != FindingInScope {
		t.Fatalf("classification pass = findings %#v err %v", accepted.Findings, err)
	}
	selected, err := selectImplementationFindings(accepted.Findings, accepted.Gates, []string{finding.ID})
	if err != nil || len(selected) != 1 {
		t.Fatalf("gated exact-large finding not selectable: selected=%#v err=%v", selected, err)
	}
}

func TestLifecycleOperationsRejectOutOfPhaseRuns(t *testing.T) {
	service, aggregate := readyImplementationService(t)
	implementationPhase := PhaseImplementation
	implementationState := ExecutionRunning
	seeded, seedErr := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID:     aggregate.Workspace.ID,
		ExpectedVersion: aggregate.Workspace.Version,
		RequestID:       "request-00000019",
		Patch:           AggregatePatch{Phase: &implementationPhase, ExecutionState: &implementationState},
	})
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	aggregate = seeded.Aggregate
	_, err := service.RunReview(context.Background(), RunReviewRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000020", NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("implementation-phase review error = %v", err)
	}
	_, err = service.RunNudge(context.Background(), RunNudgeRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000021", Stage: NudgeReviewSearch,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("out-of-phase review nudge error = %v", err)
	}
	phase := PhaseTriage
	mutated, mutateErr := service.store.Mutate(context.Background(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-00000022", Patch: AggregatePatch{Phase: &phase},
	})
	if mutateErr != nil {
		t.Fatal(mutateErr)
	}
	_, err = service.RunCompletionAudit(context.Background(), RunCompletionAuditRequest{
		WorkspaceID: mutated.Aggregate.Workspace.ID, ExpectedVersion: mutated.Aggregate.Workspace.Version,
		RequestID: "request-00000023", NudgePolicy: ConfiguredNudgePolicy(0, 0),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("triage-phase completion audit error = %v", err)
	}
}
