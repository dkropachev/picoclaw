//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openPRWorkspaceTestStore(t *testing.T, clock *mutableClock) *Store {
	t.Helper()
	store, _ := openTestStore(t, clock)
	require.NoError(t, store.withImmediate(context.Background(), func(conn *sql.Conn) error {
		return installSchemaV19PRWorkspace(context.Background(), conn)
	}))
	return store
}

func testPRProviderSnapshot(now time.Time) PRProviderSnapshot {
	return PRProviderSnapshot{
		Provider: "github", ProviderOrigin: "https://github.example.test",
		RepositoryID: "repo-42", Repository: "Octo/Project",
		PullRequestID: "pull-7", PullNumber: 7,
		Title: "Fix a race", Body: "Provider supplied body",
		AuthorID: "user-1", AuthorLogin: "octo", AuthenticatedUserID: "user-1",
		State: "open", BaseRef: "main", BaseSHA: "aaaaaaaa",
		HeadRepositoryID: "repo-42", HeadRepository: "Octo/Project",
		HeadRef: "fix/race", HeadSHA: "bbbbbbbb",
		Owned: true, HeadWritable: true, CanReview: true, CanCreateIssue: true,
		ProviderRevision: "etag-1", ObservedAt: now,
	}
}

func createPRWorkspaceForTest(t *testing.T, store *Store, now time.Time) PRWorkspaceAggregate {
	t.Helper()
	aggregate, created, err := store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID:   "req_create_workspace_1",
		WorkspaceID: "prw_00000000000000000000000000000001",
		Provider:    testPRProviderSnapshot(now), Phase: PRWorkspaceCharter,
		ExecutionState: PRExecutionWaitingUser,
	})
	require.NoError(t, err)
	require.True(t, created)
	return aggregate
}

func TestPRWorkspaceCreateGetListAndRequestReplay(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)

	first := createPRWorkspaceForTest(t, store, now)
	require.Equal(t, int64(1), first.Workspace.Version)
	require.Equal(t, PRWorkspaceCharter, first.Workspace.Phase)
	require.Len(t, first.ProviderSnapshots, 1)
	assert.Equal(t, int64(1), first.ProviderSnapshots[0].Ordinal)
	assert.Equal(t, first.Workspace.ID, first.ProviderSnapshots[0].WorkspaceID)

	replay, created, err := store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID:   "req_create_workspace_1",
		WorkspaceID: first.Workspace.ID,
		Provider:    testPRProviderSnapshot(now), Phase: PRWorkspaceCharter,
		ExecutionState: PRExecutionWaitingUser,
	})
	require.NoError(t, err)
	assert.True(t, created, "request replay returns original create result")
	assert.Equal(t, first.Workspace, replay.Workspace)

	duplicate, created, err := store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID: "req_create_workspace_2",
		Provider:  testPRProviderSnapshot(now), Phase: PRWorkspaceIntake,
		ExecutionState: PRExecutionQueued,
	})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.Workspace.ID, duplicate.Workspace.ID)
	assert.Equal(t, int64(1), duplicate.Workspace.Version)

	changed := testPRProviderSnapshot(now)
	changed.HeadSHA = "cccccccc"
	_, _, err = store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID: "req_create_workspace_1", WorkspaceID: first.Workspace.ID,
		Provider: changed, Phase: PRWorkspaceCharter, ExecutionState: PRExecutionWaitingUser,
	})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	page, err := store.ListPRWorkspaces(context.Background(), PRWorkspaceFilter{
		RepositoryID: "repo-42", Phase: PRWorkspaceCharter, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, page.Workspaces, 1)
	assert.Equal(t, first.Workspace.ID, page.Workspaces[0].ID)
	assert.Nil(t, page.Next)

	needsAction := true
	page, err = store.ListPRWorkspaces(context.Background(), PRWorkspaceFilter{NeedsAction: &needsAction})
	require.NoError(t, err)
	require.Len(t, page.Workspaces, 1)
	needsAction = false
	page, err = store.ListPRWorkspaces(context.Background(), PRWorkspaceFilter{NeedsAction: &needsAction})
	require.NoError(t, err)
	assert.Empty(t, page.Workspaces)
}

func TestPRWorkspaceStageEvidenceTransitionUsesCanonicalJSONSemantics(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 20, 30, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)
	charterID := "pcr_00000000000000000000000000000020"
	stageID := "psr_00000000000000000000000000000020"
	confirmed := now
	phase := PRWorkspaceImplementation
	state := PRExecutionWaitingGate
	created, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "req_stage_evidence_canonical_create",
		Patch: PRWorkspacePatch{
			Phase: &phase, ExecutionState: &state,
			AppendCharters: []PRCharterRevision{{
				PRWorkspaceRecord: PRWorkspaceRecord{ID: charterID}, Status: PRRecordConfirmed,
				Revision: 1, Type: PRTypeFix, Goal: "Preserve exact stage evidence",
				AcceptanceCriteria: []string{"The stage can finish with identical evidence"},
				BaseSHA:            "aaaaaaaa", HeadSHA: "bbbbbbbb", CreatedBy: "system", ConfirmedAt: &confirmed,
			}},
			AppendStageRuns: []PRStageRun{
				{
					PRWorkspaceRecord: PRWorkspaceRecord{ID: stageID},
					Phase:             PRWorkspaceImplementation,
					Kind:              "implementation",
					State:             PRExecutionWaitingGate,
					Attempt:           1,
					CharterID:         charterID,
					WorkspaceVersion:  aggregate.Workspace.Version,
					HeadSHA:           "bbbbbbbb",
					Evidence: json.RawMessage(
						`{"stage":"implementation_completion","validation":{"run":{"exact":9007199254740993,"checks":[{"name":"go test","status":"passed"}],"metrics":{"semantic_lines":10,"files":1}}}}`,
					),
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), created.Aggregate.Workspace.Version)

	stage := created.Aggregate.StageRuns[0]
	stage.State, stage.FinishedAt = PRExecutionSucceeded, &now
	stage.Evidence = json.RawMessage(
		`{"validation":{"run":{"metrics":{"files":1,"semantic_lines":10},"checks":[{"status":"passed","name":"go test"}],"exact":9007199254740993}},"stage":"implementation_completion"}`,
	)
	transitioned, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 2,
		RequestID: "req_stage_evidence_canonical_transition",
		Patch:     PRWorkspacePatch{ReplaceStageRuns: []PRStageRun{stage}},
	})
	require.NoError(t, err, "object key order must not change immutable evidence semantics")
	require.Equal(t, PRExecutionSucceeded, transitioned.Aggregate.StageRuns[0].State)

	changed := transitioned.Aggregate.StageRuns[0]
	changed.Evidence = json.RawMessage(
		`{"stage":"implementation_completion","validation":{"run":{"exact":9007199254740994,"checks":[{"name":"go test","status":"passed"}],"metrics":{"semantic_lines":10,"files":1}}}}`,
	)
	_, err = store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 3,
		RequestID: "req_stage_evidence_semantic_change",
		Patch:     PRWorkspacePatch{ReplaceStageRuns: []PRStageRun{changed}},
	})
	require.ErrorIs(t, err, ErrPRWorkspaceConflict, "exact JSON number changes must remain immutable")

	duplicate := transitioned.Aggregate.StageRuns[0]
	duplicate.Evidence = json.RawMessage(`{"validation":{"run":{"exact":1,"exact":1}}}`)
	_, err = store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 3,
		RequestID: "req_stage_evidence_duplicate_key",
		Patch:     PRWorkspacePatch{ReplaceStageRuns: []PRStageRun{duplicate}},
	})
	require.ErrorIs(t, err, ErrInvalidPRWorkspace, "duplicate keys must never enter semantic comparison")
}

func TestPRWorkspacePatchIsAtomicAndReplayReturnsOriginalAggregate(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 21, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)

	confirmed := now
	phase := PRWorkspaceImplementation
	state := PRExecutionRunning
	charterID := "pcr_00000000000000000000000000000001"
	stageID := "psr_00000000000000000000000000000001"
	findingID := "pfn_00000000000000000000000000000001"
	deferredFindingID := "pfn_00000000000000000000000000000002"
	conversationID := "pcv_00000000000000000000000000000001"
	correctionID := "pco_00000000000000000000000000000001"
	nudgeID := "pnr_00000000000000000000000000000001"
	groupID := "pdg_00000000000000000000000000000001"
	repairID := "pra_00000000000000000000000000000001"
	gateID := "pgr_00000000000000000000000000000001"

	patch := PRWorkspacePatch{
		Phase: &phase, ExecutionState: &state, ActiveCharterID: &charterID,
		AppendCharters: []PRCharterRevision{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: charterID}, Status: PRRecordConfirmed,
			Revision: 1, Type: PRTypeFix, Goal: "Fix race without broad cleanup",
			AcceptanceCriteria: []string{"Concurrent update remains deterministic"},
			IncludedAreas:      []string{"pkg/race"}, Exclusions: []string{"public API refactor"},
			NonGoals: []string{"unrelated cleanup"}, BaseSHA: "aaaaaaaa", HeadSHA: "bbbbbbbb",
			CreatedBy: "octo", ConfirmedAt: &confirmed,
		}},
		AppendStageRuns: []PRStageRun{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: stageID}, Phase: PRWorkspaceReview,
			Kind: "review_search", State: PRExecutionSucceeded, Attempt: 1, CharterID: charterID,
			WorkspaceVersion: 1, BaseSHA: "aaaaaaaa", HeadSHA: "bbbbbbbb",
			PromptDigest: "prompt-review", Summary: "Found one race",
		}},
		UpsertFindings: []PRFinding{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: findingID}, Origin: "review",
			StageRunID: stageID, Fingerprint: "finding-fingerprint", Severity: "high",
			Title: "Lost update", Message: "Concurrent writes can overwrite state",
			Disposition: PRFindingInScope, ScopeDistance: PRScopeExact,
			ChangeSize: PRChangeSizeS, TypeCompatible: true, ClassificationConf: .95,
			Version:          1,
			CharterClauses:   []string{"Concurrent update remains deterministic"},
			EstimatedMetrics: PRChangeMetrics{Files: 2, SemanticLines: 50, Modules: 1},
		}, {
			PRWorkspaceRecord: PRWorkspaceRecord{ID: deferredFindingID}, Origin: "review",
			StageRunID: stageID, Fingerprint: "cleanup-fingerprint", Severity: "low",
			Title: "Broader cleanup", Message: "Related cleanup is outside fix charter",
			Disposition: PRFindingDeferred, ScopeDistance: PRScopeRelatedFollowup,
			ChangeSize: PRChangeSizeM, TypeCompatible: false, ClassificationConf: .9,
			Version:          1,
			EstimatedMetrics: PRChangeMetrics{Files: 8, SemanticLines: 300, Modules: 3},
		}},
		AppendFindingEvents: []PRFindingEvent{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: "pfe_00000000000000000000000000000001"},
			FindingID:         findingID, Kind: "classified", Actor: "ai:scope",
		}},
		AppendConversations: []PRConversation{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: conversationID}, Channel: "workspace",
			Phase: PRWorkspaceReview, Status: PRRecordActive,
		}},
		AppendCorrections: []PRCorrection{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: correctionID}, Kind: "scope",
			Status: PRRecordActive, TargetKind: "finding", TargetID: findingID,
			OriginalClaim: "This requires four modules", Correction: "Only lock owner changes",
			AppliesToReview: true, AppliesToImplement: true, CharterID: charterID,
			HeadSHA: "bbbbbbbb",
		}},
		AppendLessons: []PRRepositoryLesson{
			{
				PRWorkspaceRecord:  PRWorkspaceRecord{ID: "prl_00000000000000000000000000000001"},
				RepositoryID:       "repo-42",
				Status:             PRRecordActive,
				Kind:               "scope",
				Content:            "Lock owner is local to pkg/race",
				SourceCorrectionID: correctionID,
				ApplicableTypes: []PRType{
					PRTypeFix,
				},
				ApplicablePhases: []PRWorkspacePhase{PRWorkspaceReview, PRWorkspaceImplementation},
				ConfirmedBy:      "octo",
			},
		},
		AppendMessages: []PRMessage{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: "pms_00000000000000000000000000000001"},
			ConversationID:    conversationID, StageRunID: stageID, FindingID: findingID,
			Phase: PRWorkspaceReview, Kind: "correction", Role: "user",
			Content: "Only lock owner changes", CorrectionID: correctionID,
		}},
		AppendNudgeRounds: []PRNudgeRound{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: nudgeID}, StageRunID: stageID,
			Phase: PRWorkspaceReview, State: PRExecutionSucceeded, Round: 1,
			MinimumRounds: 2, HardCap: 5, StrategyFamily: "adversarial",
			CoverageTarget: "concurrent writers", ChallengeDigest: "challenge-digest",
			PromptDigest: "nudge-prompt", CandidateCount: 1, NovelCount: 1,
		}},
		AppendNudgeRewards: []PRNudgeReward{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: "pnw_00000000000000000000000000000001"},
			NudgeRoundID:      nudgeID, FindingID: findingID, Reward: 1,
			Outcome: "fixed_and_validated", Provenance: "validation:pvr-1",
		}},
		UpsertDeferredGroups: []PRDeferredGroup{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: groupID}, Status: PRRecordDraft,
			Title: "Broader locking cleanup", Body: "Follow up outside fix charter",
			ScopeDistance: PRScopeRelatedFollowup, ChangeSize: PRChangeSizeM,
			DraftRevision: 1, Version: 1,
		}},
		UpsertDeferredItems: []PRDeferredGroupItem{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: "pdi_00000000000000000000000000000001"},
			GroupID:           groupID, FindingID: deferredFindingID,
		}},
		AppendRepairAttempts: []PRRepairAttempt{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: repairID}, StageRunID: stageID,
			State: PRExecutionSucceeded, Attempt: 1, GoalDigest: "repair-goal",
			BaseCommit: "bbbbbbbb", TipCommit: "cccccccc", ChangedFiles: []string{"pkg/race/store.go"},
			Metrics: PRChangeMetrics{Files: 1, SemanticLines: 18, Modules: 1},
		}},
		AppendValidationRuns: []PRValidationRun{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: "pvr_00000000000000000000000000000001"},
			StageRunID:        stageID, RepairAttemptID: repairID, State: PRExecutionSucceeded,
			Kind: "local_ci", Command: "go test ./pkg/race", Summary: "passed",
		}},
		AppendGateRuns: []PRGateRun{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: gateID}, DecisionPoint: "pr.implementation.complete",
			State: PRExecutionSucceeded, PolicyRevision: "sha256:policy-v3",
			WorkflowRef: "workflows/pr-lifecycle.yml", WorkflowRevision: "sha256:workflow-v3",
			GateRef: "gates.implementation-complete", WorkflowConfigurationID: "default",
			WorkflowConfigurationRevision: "config-v1", PinnedPolicy: json.RawMessage(`{"version":"4"}`),
			PinnedPolicyHash: "policy-hash", SubjectRevision: "subject-v1",
			PinnedSubject: json.RawMessage(`{"head_sha":"bbbbbbbb"}`), PinnedSubjectHash: "subject-hash",
			WorkflowRunID: "wr_gate-v3", RuntimePresent: true, CurrentStageID: "",
			Turns: []PRGateTurn{
				{
					StageID: "gate-exec",
					Kind:    "human",
					Title:   "Accept implementation",
					Status:  "answered",
					GateForm: json.RawMessage(
						`{"gate-ref":"gates.implementation-complete","prompt":"Accept implementation?","fields":[]}`,
					),
					FieldValues:    map[string]any{"action": "accept", "note": "validated"},
					ActorKind:      "human",
					ExecutionID:    "ge_implementation-complete",
					ActionRevision: "sha256:action-v3",
					InputHash:      "sha256:input-v3",
				},
			},
		}},
		AppendPublications: []PRPublication{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: "ppb_00000000000000000000000000000001"},
			Kind:              PRPublicationGitHubIssue, Status: PRPublicationPending, GateRunID: gateID,
			DeferredGroupID: groupID, Marker: "marker-1", Request: json.RawMessage(`{"title":"cleanup"}`),
			RequestDigest: testPRWorkspacePayloadDigest(json.RawMessage(`{"title":"cleanup"}`)),
			PayloadDigest: testPRWorkspacePayloadDigest(json.RawMessage(`{"title":"cleanup"}`)), AvailableAt: now,
		}},
		AppendOperationIntents: []PRWorkspaceOperationIntent{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: "poi_00000000000000000000000000000001"},
			Kind:              "review_search", State: PRExecutionQueued, StageRunID: stageID,
			InputWorkspaceVersion: 1, InputCharterID: charterID, InputHeadSHA: "bbbbbbbb",
			InputDigest: "operation-input", Input: json.RawMessage(`{"scope":"exact"}`),
			AvailableAt: now,
		}},
		UpsertIngressWatermarks: []PRIngressWatermark{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: "piw_00000000000000000000000000000001"},
			Source:            "github", Connector: "primary", InboxReceivedAt: now,
			InboxEventID: "evt_00000000000000000000000000000001",
		}},
		AppendActivity: []PRActivity{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: "pac_00000000000000000000000000000001"},
			Kind:              "finding.corrected", Actor: "octo", Summary: "Corrected scope estimate",
			EntityID: findingID, Metadata: map[string]any{"distance": "S0_exact"},
		}},
	}
	result, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 1,
		RequestID: "req_atomic_patch_1", Patch: patch,
	})
	require.NoError(t, err)
	assert.False(t, result.Replayed)
	assert.Equal(t, int64(2), result.Aggregate.Workspace.Version)
	assert.Equal(t, PRWorkspaceImplementation, result.Aggregate.Workspace.Phase)
	assert.Equal(t, charterID, result.Aggregate.Workspace.ActiveCharterID)
	assert.Len(t, result.Aggregate.Charters, 1)
	assert.Len(t, result.Aggregate.StageRuns, 1)
	assert.Len(t, result.Aggregate.Findings, 2)
	assert.Len(t, result.Aggregate.Corrections, 1)
	assert.Len(t, result.Aggregate.RepositoryLessons, 1)
	assert.Len(t, result.Aggregate.NudgeRounds, 1)
	assert.Len(t, result.Aggregate.DeferredGroups, 1)
	assert.Len(t, result.Aggregate.DeferredGroups[0].Items, 1)
	assert.Len(t, result.Aggregate.RepairAttempts, 1)
	assert.Len(t, result.Aggregate.ValidationRuns, 1)
	assert.Len(t, result.Aggregate.GateRuns, 1)
	assert.Equal(t, "sha256:policy-v3", result.Aggregate.GateRuns[0].PolicyRevision)
	assert.Equal(t, "workflows/pr-lifecycle.yml", result.Aggregate.GateRuns[0].WorkflowRef)
	assert.Equal(t, "sha256:workflow-v3", result.Aggregate.GateRuns[0].WorkflowRevision)
	assert.Equal(t, "gates.implementation-complete", result.Aggregate.GateRuns[0].GateRef)
	assert.Equal(t, "config-v1", result.Aggregate.GateRuns[0].WorkflowConfigurationRevision)
	assert.Equal(
		t,
		map[string]any{"action": "accept", "note": "validated"},
		result.Aggregate.GateRuns[0].Turns[0].FieldValues,
	)
	assert.Equal(t, "human", result.Aggregate.GateRuns[0].Turns[0].ActorKind)
	assert.JSONEq(
		t,
		`{"gate-ref":"gates.implementation-complete","prompt":"Accept implementation?","fields":[]}`,
		string(result.Aggregate.GateRuns[0].Turns[0].GateForm),
	)
	assert.Len(t, result.Aggregate.Publications, 1)
	assert.Len(t, result.Aggregate.OperationIntents, 1)
	assert.Len(t, result.Aggregate.Activity, 1)
	assert.Equal(t, "finding.corrected", result.Aggregate.Activity[0].Kind)

	statePayload, err := json.Marshal(
		PRWorkspaceStateChange{Phase: PRWorkspaceValidation, ExecutionState: PRExecutionQueued},
	)
	require.NoError(t, err)
	stateResult, err := store.ApplyPRWorkspaceMutation(context.Background(), PRWorkspaceMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 2,
		RequestID: "req_state_after_patch", Kind: PRMutationWorkspaceState, Payload: statePayload,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), stateResult.WorkspaceVersion)

	replay, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 1,
		RequestID: "req_atomic_patch_1", Patch: patch,
	})
	require.NoError(t, err)
	assert.True(t, replay.Replayed)
	assert.Equal(
		t,
		int64(2),
		replay.Aggregate.Workspace.Version,
		"replay returns original aggregate, not current state",
	)
	assert.Equal(t, PRWorkspaceImplementation, replay.Aggregate.Workspace.Phase)

	patch.Phase = ptrPRWorkspacePhase(PRWorkspacePublication)
	_, err = store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 1,
		RequestID: "req_atomic_patch_1", Patch: patch,
	})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)
}

func TestPRWorkspaceGateV3PersistenceHasNoDecisionCompatibilitySurface(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "gate-v3.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	var tableCount int
	require.NoError(t, store.db.QueryRowContext(
		ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'pr_gate_decisions'`,
	).Scan(&tableCount))
	assert.Zero(t, tableCount)

	for _, retired := range []string{
		`{"purpose":"authorization"}`,
		`{"outcome":"pass"}`,
		`{"profile_id":"default"}`,
		`{"profile_revision":"profile-v2"}`,
		`{"turns":[{"outcome":"pass"}]}`,
		`{"turns":[{"questions":["Approve?"]}]}`,
		`{"turns":[{"answers":{"approved":true}}]}`,
		`{"turns":[{"comment":"approved"}]}`,
	} {
		var gate PRGateRun
		assert.Error(t, decodeStrictPRWorkspaceJSON([]byte(retired), &gate), retired)
	}

	encoded, err := json.Marshal(PRWorkspaceAggregate{})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "gate_decisions")
}

func TestPRWorkspaceGateV3PersistedLoadRejectsRetiredFields(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	legacyGateID := "pgr_00000000000000000000000000000099"
	legacyPayload := func(workspaceID string) []byte {
		return []byte(fmt.Sprintf(`{
			"id":%q,"workspace_id":%q,"ordinal":1,
			"created_at":%q,"updated_at":%q,
			"decision_point":"pr.review.publish","purpose":"authorization",
			"state":"waiting_user","outcome":"pass",
			"profile_id":"default","profile_revision":"profile-v2",
			"pinned_policy":{},"pinned_policy_hash":"legacy-policy",
			"subject_revision":"legacy-subject","pinned_subject":{},
			"pinned_subject_hash":"legacy-subject","turns":[{
				"stage_id":"human","kind":"human","title":"Approve",
				"status":"waiting","questions":["Approve?"],"outcome":"pass"
			}]
		}`, workspaceID, workspaceID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)))
	}
	legacyTurnPayload := func(workspaceID string) []byte {
		return []byte(fmt.Sprintf(`{
			"id":%q,"workspace_id":%q,"ordinal":1,
			"created_at":%q,"updated_at":%q,
			"decision_point":"pr.review.publish","state":"waiting_user",
			"policy-revision":"policy-v3","workflow-ref":"workflows/pr-lifecycle.yml",
			"workflow-revision":"workflow-v3","gate-ref":"gates.review-publish",
			"workflow-configuration-id":"default","workflow-configuration-revision":"config-v3",
			"pinned_policy":{},"pinned_policy_hash":"policy-v3",
			"subject_revision":"subject-v3","pinned_subject":{},
			"pinned_subject_hash":"subject-v3","turns":[{
				"stage_id":"gate-exec","kind":"human","title":"Publish",
				"status":"waiting","questions":["Publish?"],"outcome":"pass"
			}]
		}`, legacyGateID, workspaceID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)))
	}

	t.Run("current aggregate", func(t *testing.T) {
		store := openPRWorkspaceTestStore(t, newMutableClock(now))
		aggregate := createPRWorkspaceForTest(t, store, now)
		_, err := store.db.ExecContext(context.Background(), `INSERT INTO pr_gate_runs (
			id, workspace_id, ordinal, status, payload_json, created_at, updated_at
		) VALUES (?, ?, 1, 'waiting_user', ?, ?, ?)`, legacyGateID, aggregate.Workspace.ID,
			legacyPayload(aggregate.Workspace.ID), toDBTime(now), toDBTime(now))
		require.NoError(t, err)

		conn, err := store.db.Conn(context.Background())
		require.NoError(t, err)
		defer conn.Close()
		_, err = loadPRWorkspaceAggregate(context.Background(), conn, aggregate.Workspace.ID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown field")
	})

	t.Run("historical aggregate", func(t *testing.T) {
		store := openPRWorkspaceTestStore(t, newMutableClock(now))
		aggregate := createPRWorkspaceForTest(t, store, now)
		var sequence int
		require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT coalesce(max(sequence), -1) + 1
			FROM pr_workspace_history WHERE workspace_id = ? AND version = 1`, aggregate.Workspace.ID).Scan(&sequence))
		_, err := store.db.ExecContext(context.Background(), `INSERT INTO pr_workspace_history (
			id, workspace_id, version, sequence, record_table, record_id, payload_json, created_at
		) VALUES (?, ?, 1, ?, 'pr_gate_runs', ?, ?, ?)`,
			"phs_00000000000000000000000000000099", aggregate.Workspace.ID, sequence,
			legacyGateID, legacyPayload(aggregate.Workspace.ID), toDBTime(now))
		require.NoError(t, err)

		_, err = store.getPRWorkspaceAtVersion(context.Background(), aggregate.Workspace.ID, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown field")
	})

	t.Run("retired turn fields", func(t *testing.T) {
		store := openPRWorkspaceTestStore(t, newMutableClock(now))
		aggregate := createPRWorkspaceForTest(t, store, now)
		_, err := store.db.ExecContext(context.Background(), `INSERT INTO pr_gate_runs (
			id, workspace_id, ordinal, status, payload_json, created_at, updated_at
		) VALUES (?, ?, 1, 'waiting_user', ?, ?, ?)`, legacyGateID, aggregate.Workspace.ID,
			legacyTurnPayload(aggregate.Workspace.ID), toDBTime(now), toDBTime(now))
		require.NoError(t, err)
		conn, err := store.db.Conn(context.Background())
		require.NoError(t, err)
		defer conn.Close()

		_, err = loadPRWorkspaceAggregate(context.Background(), conn, aggregate.Workspace.ID)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown field")
	})
}

func TestPRWorkspaceStalePatchRollsBackEveryChild(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 22, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)

	_, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 2,
		RequestID: "req_stale_patch", Patch: PRWorkspacePatch{
			AppendConversations: []PRConversation{{
				PRWorkspaceRecord: PRWorkspaceRecord{ID: "pcv_00000000000000000000000000000009"},
				Channel:           "workspace", Phase: PRWorkspaceReview, Status: PRRecordActive,
			}},
		},
	})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	stored, err := store.GetPRWorkspace(context.Background(), aggregate.Workspace.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), stored.Workspace.Version)
	assert.Empty(t, stored.Conversations)
}

func TestPRWorkspaceSchemaValidationRejectsChangedIndex(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 13, 23, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)

	require.NoError(t, store.withImmediate(context.Background(), func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(context.Background(), `DROP INDEX pr_findings_list`); err != nil {
			return err
		}
		_, err := conn.ExecContext(context.Background(), `CREATE INDEX pr_findings_list ON pr_findings(workspace_id)`)
		return err
	}))
	err := store.withImmediate(context.Background(), func(conn *sql.Conn) error {
		return validateSchemaV19PRWorkspace(context.Background(), conn)
	})
	assert.ErrorIs(t, err, ErrSchemaInvalid)
}

func ptrPRWorkspacePhase(value PRWorkspacePhase) *PRWorkspacePhase { return &value }

func TestPRWorkspaceMutationRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)

	_, err := store.ApplyPRWorkspaceMutation(context.Background(), PRWorkspaceMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 1,
		RequestID: "req_unknown_field", Kind: PRMutationWorkspaceState,
		Payload: json.RawMessage(`{"phase":"review","execution_state":"queued","extra":true}`),
	})
	assert.ErrorIs(t, err, ErrInvalidPRWorkspace)

	_, err = store.ApplyPRWorkspaceMutation(context.Background(), PRWorkspaceMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 1,
		RequestID: "req_duplicate_field", Kind: PRMutationWorkspaceState,
		Payload: json.RawMessage(`{"phase":"review","Phase":"triage","execution_state":"queued"}`),
	})
	assert.ErrorIs(t, err, ErrInvalidPRWorkspace)

	_, err = store.GetPRWorkspace(context.Background(), "prw_bad")
	assert.True(t, errors.Is(err, ErrInvalidPRWorkspace))
}

func TestPRWorkspaceIngressCutoverIsGlobalAndMonotonic(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)

	first := PRIngressCutoverWatermark{
		Source: " github ", Connector: " primary ", InboxReceivedAt: now.Add(-time.Minute),
		InboxEventID: "evt-10",
	}
	require.NoError(t, store.SetPRWorkspaceIngressCutover(context.Background(), first),
		"cutover must not require an existing workspace")
	stored, err := store.GetPRWorkspaceIngressCutover(context.Background(), "github", "primary")
	require.NoError(t, err)
	assert.Equal(t, "evt-10", stored.InboxEventID)
	assert.Equal(t, now.Add(-time.Minute), stored.InboxReceivedAt)
	assert.Equal(t, now, stored.CreatedAt)
	assert.Equal(t, now, stored.UpdatedAt)

	require.NoError(t, store.SetPRWorkspaceIngressCutover(context.Background(), PRIngressCutoverWatermark{
		Source: "github", Connector: "primary", InboxReceivedAt: now.Add(-time.Minute), InboxEventID: "evt-10",
	}), "same cursor is idempotent")
	err = store.SetPRWorkspaceIngressCutover(context.Background(), PRIngressCutoverWatermark{
		Source: "github", Connector: "primary", InboxReceivedAt: now.Add(-2 * time.Minute), InboxEventID: "evt-99",
	})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	clock.Advance(time.Second)
	require.NoError(t, store.SetPRWorkspaceIngressCutover(context.Background(), PRIngressCutoverWatermark{
		Source: "github", Connector: "primary", InboxReceivedAt: now, InboxEventID: "evt-11",
	}))
	advanced, err := store.GetPRWorkspaceIngressCutover(context.Background(), "github", "primary")
	require.NoError(t, err)
	assert.Equal(t, "evt-11", advanced.InboxEventID)
	assert.Equal(t, now.Add(time.Second), advanced.UpdatedAt)
}

func TestPRWorkspaceWorkersClaimFinishAndRecoverExpiredLeases(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)

	const (
		operationID   = "poi_00000000000000000000000000000010"
		publicationID = "ppb_00000000000000000000000000000010"
	)
	created, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 1, RequestID: "req_worker_records",
		Patch: PRWorkspacePatch{
			AppendOperationIntents: []PRWorkspaceOperationIntent{{
				PRWorkspaceRecord: PRWorkspaceRecord{ID: operationID}, Kind: "review_search",
				State: PRExecutionQueued, InputWorkspaceVersion: 1, InputDigest: "input-1",
				Input: json.RawMessage(`{"stage":"review"}`), AvailableAt: now,
			}},
			AppendPublications: []PRPublication{{
				PRWorkspaceRecord: PRWorkspaceRecord{ID: publicationID}, Kind: PRPublicationGitHubReview,
				Status: PRPublicationPending, Request: json.RawMessage(`{"body":"review"}`),
				RequestDigest: testPRWorkspacePayloadDigest(json.RawMessage(`{"body":"review"}`)),
				PayloadDigest: testPRWorkspacePayloadDigest(json.RawMessage(`{"body":"review"}`)), AvailableAt: now,
			}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), created.Aggregate.Workspace.Version)

	operations, err := store.ClaimPRWorkspaceOperations(context.Background(), PRWorkspaceClaimRequest{
		WorkerID: "operation-worker", Limit: 10, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, operations, 1)
	assert.Equal(t, int64(3), operations[0].WorkspaceVersion)
	assert.Equal(t, PRExecutionRunning, operations[0].Intent.State)
	assert.Equal(t, 1, operations[0].Intent.Attempts)
	firstOperationToken := operations[0].Intent.LeaseToken
	assert.True(t, validPrefixedID(firstOperationToken, prLeaseTokenIDPrefix))

	none, err := store.ClaimPRWorkspaceOperations(context.Background(), PRWorkspaceClaimRequest{
		WorkerID: "other-worker", Limit: 10, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	assert.Empty(t, none)
	finishedOperation, err := store.FinishPRWorkspaceOperation(context.Background(), PRWorkspaceOperationFinish{
		IntentID: operationID, LeaseToken: firstOperationToken, State: PRExecutionSucceeded,
		Result: json.RawMessage(`{"findings":2}`), ResultDigest: "result-1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(4), finishedOperation.WorkspaceVersion)
	assert.Empty(t, finishedOperation.Intent.LeaseToken)

	publications, err := store.ClaimPRWorkspacePublications(context.Background(), PRWorkspaceClaimRequest{
		WorkerID: "publication-worker", Limit: 10, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, publications, 1)
	assert.Equal(t, int64(5), publications[0].WorkspaceVersion)
	publicationToken := publications[0].Publication.LeaseToken
	finishedPublication, err := store.FinishPRWorkspacePublication(context.Background(), PRWorkspacePublicationFinish{
		PublicationID: publicationID, LeaseToken: publicationToken, Status: PRPublicationPublished,
		ExternalID: "review-99", ExternalURL: "https://github.example.test/review/99",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(6), finishedPublication.WorkspaceVersion)
	require.NotNil(t, finishedPublication.Publication.PublishedAt)

	const recoveryID = "poi_00000000000000000000000000000011"
	queued, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: 6, RequestID: "req_worker_recovery",
		Patch: PRWorkspacePatch{AppendOperationIntents: []PRWorkspaceOperationIntent{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: recoveryID}, Kind: "completion_audit",
			State: PRExecutionQueued, InputWorkspaceVersion: 6, InputDigest: "input-2",
			Input: json.RawMessage(`{"stage":"completion"}`), AvailableAt: now,
		}}},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(7), queued.Aggregate.Workspace.Version)
	firstLease, err := store.ClaimPRWorkspaceOperations(context.Background(), PRWorkspaceClaimRequest{
		WorkerID: "worker-a", Limit: 1, LeaseDuration: time.Second,
	})
	require.NoError(t, err)
	require.Len(t, firstLease, 1)
	clock.Advance(2 * time.Second)
	recovered, err := store.ClaimPRWorkspaceOperations(context.Background(), PRWorkspaceClaimRequest{
		WorkerID: "worker-b", Limit: 1, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.Len(t, recovered, 1)
	assert.Equal(t, 2, recovered[0].Intent.Attempts)
	assert.NotEqual(t, firstLease[0].Intent.LeaseToken, recovered[0].Intent.LeaseToken)
	_, err = store.FinishPRWorkspaceOperation(context.Background(), PRWorkspaceOperationFinish{
		IntentID: recoveryID, LeaseToken: firstLease[0].Intent.LeaseToken, State: PRExecutionSucceeded,
	})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)
	_, err = store.FinishPRWorkspaceOperation(context.Background(), PRWorkspaceOperationFinish{
		IntentID: recoveryID, LeaseToken: recovered[0].Intent.LeaseToken, State: PRExecutionSucceeded,
	})
	require.NoError(t, err)

	stored, err := store.GetPRWorkspace(context.Background(), aggregate.Workspace.ID)
	require.NoError(t, err)
	assert.Equal(t, PRExecutionSucceeded, stored.OperationIntents[0].State)
	assert.Equal(t, PRPublicationPublished, stored.Publications[0].Status)
}

func testPRWorkspacePayloadDigest(raw json.RawMessage) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}
