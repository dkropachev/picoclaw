//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testProtectedFinding(t *testing.T, workspaceID string) (PRFinding, PRFindingSourceExecution) {
	t.Helper()
	source := PRFindingSourceExecution{
		ExecutionID:     "aix_11111111111111111111111111111111",
		WorkspaceID:     workspaceID,
		Binding:         "sha256:protected-binding",
		AgentID:         "main",
		SessionRevision: "sha256:protected-source-revision",
		Tools:           "none",
	}
	source.Session = prFindingSourceSessionKey(source)
	finding := PRFinding{
		PRWorkspaceRecord: PRWorkspaceRecord{ID: "pfn_11111111111111111111111111111111"},
		Origin:            "review",
		Fingerprint:       "protected-source-fingerprint",
		Severity:          "high",
		Title:             "Protected source finding",
		Message:           "The source capability must remain private",
		Disposition:       PRFindingInScope,
		ScopeDistance:     PRScopeExact,
		ChangeSize:        PRChangeSizeS,
		TypeCompatible:    true,
		Version:           1,
	}
	require.NoError(t, finding.SetProtectedSourceExecution(&source))
	return finding, source
}

func requireNoProtectedFindingSourceJSON(t *testing.T, value any, source PRFindingSourceExecution) {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	projection := string(encoded)
	for _, secret := range []string{
		"source-execution",
		"protected-finding-sources",
		source.Session,
		source.SessionRevision,
		source.Binding,
		source.ExecutionID,
	} {
		require.NotContains(t, projection, secret)
	}
}

func TestPRFindingProtectedSourceIsAbsentFromEveryPublicJSONProjection(t *testing.T) {
	finding, source := testProtectedFinding(t, "devw_00000000000000000000000000000001")
	patch := PRWorkspacePatch{UpsertFindings: []PRFinding{finding}}

	requireNoProtectedFindingSourceJSON(t, finding, source)
	requireNoProtectedFindingSourceJSON(t, PRWorkspaceAggregate{Findings: []PRFinding{finding}}, source)
	requireNoProtectedFindingSourceJSON(t, patch, source)
	requireNoProtectedFindingSourceJSON(t, PRWorkspacePatchMutation{
		WorkspaceID:     source.WorkspaceID,
		ExpectedVersion: 1,
		RequestID:       "req_public_patch_projection",
		Patch:           patch,
	}, source)

	findingPayload, err := json.Marshal(finding)
	require.NoError(t, err)
	requireNoProtectedFindingSourceJSON(t, PRWorkspaceMutation{
		WorkspaceID:     source.WorkspaceID,
		ExpectedVersion: 1,
		RequestID:       "req_public_mutation_projection",
		Kind:            PRMutationFinding,
		Payload:         findingPayload,
	}, source)
}

func TestPRFindingProtectedSourceSurvivesSQLiteHistoryAndReplay(t *testing.T) {
	now := time.Date(2026, 8, 18, 16, 0, 0, 0, time.UTC)
	store := openPRWorkspaceTestStore(t, newMutableClock(now))
	created := createPRWorkspaceForTest(t, store, now)
	finding, source := testProtectedFinding(t, created.Workspace.ID)
	patch := PRWorkspacePatch{UpsertFindings: []PRFinding{finding}}
	mutation := PRWorkspacePatchMutation{
		WorkspaceID:     created.Workspace.ID,
		ExpectedVersion: created.Workspace.Version,
		RequestID:       "req_protected_source_round_trip",
		Patch:           patch,
	}

	first, err := store.ApplyPRWorkspacePatch(context.Background(), mutation)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	require.Len(t, first.Aggregate.Findings, 1)
	got, ok := first.Aggregate.Findings[0].ProtectedSourceExecution()
	require.True(t, ok)
	require.Equal(t, source, got)
	requireNoProtectedFindingSourceJSON(t, first.Aggregate, source)

	var persisted string
	require.NoError(t, store.db.QueryRowContext(context.Background(),
		`SELECT payload_json FROM pr_findings WHERE id = ?`, finding.ID,
	).Scan(&persisted))
	require.Contains(t, persisted, `"source-execution"`)
	require.Contains(t, persisted, source.Session)

	phase := PRWorkspaceImplementation
	_, err = store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: created.Workspace.ID, ExpectedVersion: 2,
		RequestID: "req_advance_after_protected_source", Patch: PRWorkspacePatch{Phase: &phase},
	})
	require.NoError(t, err)

	replay, err := store.ApplyPRWorkspacePatch(context.Background(), mutation)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, int64(2), replay.Aggregate.Workspace.Version)
	require.Len(t, replay.Aggregate.Findings, 1)
	replayedSource, ok := replay.Aggregate.Findings[0].ProtectedSourceExecution()
	require.True(t, ok)
	require.Equal(t, source, replayedSource)

	changedSource := source
	changedSource.SessionRevision = "sha256:different-source-revision"
	changedFinding := finding
	require.NoError(t, changedFinding.SetProtectedSourceExecution(&changedSource))
	mutation.Patch = PRWorkspacePatch{UpsertFindings: []PRFinding{changedFinding}}
	_, err = store.ApplyPRWorkspacePatch(context.Background(), mutation)
	require.ErrorIs(t, err, ErrPRWorkspaceConflict, "request identity must bind protected provenance")
}

func TestPRFindingProtectedSourceIsImmutable(t *testing.T) {
	now := time.Date(2026, 8, 18, 17, 0, 0, 0, time.UTC)
	store := openPRWorkspaceTestStore(t, newMutableClock(now))
	created := createPRWorkspaceForTest(t, store, now)
	finding, source := testProtectedFinding(t, created.Workspace.ID)
	first, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: created.Workspace.ID, ExpectedVersion: 1,
		RequestID: "req_protected_source_create", Patch: PRWorkspacePatch{UpsertFindings: []PRFinding{finding}},
	})
	require.NoError(t, err)

	changed := first.Aggregate.Findings[0]
	changed.Version++
	changedSource := source
	changedSource.Binding = "sha256:changed-binding"
	changedSource.Session = prFindingSourceSessionKey(changedSource)
	require.NoError(t, changed.SetProtectedSourceExecution(&changedSource))
	_, err = store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: created.Workspace.ID, ExpectedVersion: 2,
		RequestID: "req_protected_source_change", Patch: PRWorkspacePatch{UpsertFindings: []PRFinding{changed}},
	})
	require.ErrorIs(t, err, ErrPRWorkspaceConflict)

	removed := first.Aggregate.Findings[0]
	removed.Version++
	require.NoError(t, removed.SetProtectedSourceExecution(nil))
	_, err = store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: created.Workspace.ID, ExpectedVersion: 2,
		RequestID: "req_protected_source_remove", Patch: PRWorkspacePatch{UpsertFindings: []PRFinding{removed}},
	})
	require.ErrorIs(t, err, ErrPRWorkspaceConflict)
}

func TestPRFindingProtectedPersistenceSchemaIsStrictAndBounded(t *testing.T) {
	finding, source := testProtectedFinding(t, "devw_00000000000000000000000000000001")
	valid, err := marshalPRFindingPersistence(finding)
	require.NoError(t, err)
	restored, err := decodePRFindingPersistence(valid)
	require.NoError(t, err)
	got, ok := restored.ProtectedSourceExecution()
	require.True(t, ok)
	require.Equal(t, source, got)

	var persisted map[string]any
	require.NoError(t, json.Unmarshal(valid, &persisted))
	originalSource := persisted["source-execution"].(map[string]any)

	t.Run("unknown source field", func(t *testing.T) {
		candidate := cloneJSONMap(originalSource)
		candidate["unexpected"] = "not allowed"
		persisted["source-execution"] = candidate
		raw, err := json.Marshal(persisted)
		require.NoError(t, err)
		_, err = decodePRFindingPersistence(raw)
		require.ErrorIs(t, err, ErrInvalidPRWorkspace)
	})

	t.Run("missing required source field", func(t *testing.T) {
		candidate := cloneJSONMap(originalSource)
		delete(candidate, "session-revision")
		persisted["source-execution"] = candidate
		raw, err := json.Marshal(persisted)
		require.NoError(t, err)
		_, err = decodePRFindingPersistence(raw)
		require.ErrorIs(t, err, ErrInvalidPRWorkspace)
	})

	t.Run("null source", func(t *testing.T) {
		persisted["source-execution"] = nil
		raw, err := json.Marshal(persisted)
		require.NoError(t, err)
		_, err = decodePRFindingPersistence(raw)
		require.ErrorIs(t, err, ErrInvalidPRWorkspace)
	})

	t.Run("oversized source value", func(t *testing.T) {
		candidate := cloneJSONMap(originalSource)
		candidate["session"] = strings.Repeat("s", maxPRFindingSourceValueBytes+1)
		persisted["source-execution"] = candidate
		raw, err := json.Marshal(persisted)
		require.NoError(t, err)
		_, err = decodePRFindingPersistence(raw)
		require.ErrorIs(t, err, ErrInvalidPRWorkspace)
	})

	t.Run("cross-bound session", func(t *testing.T) {
		candidate := cloneJSONMap(originalSource)
		candidate["session"] = "session:v1:redirected"
		persisted["source-execution"] = candidate
		raw, err := json.Marshal(persisted)
		require.NoError(t, err)
		_, err = decodePRFindingPersistence(raw)
		require.ErrorIs(t, err, ErrInvalidPRWorkspace)
	})
}

func cloneJSONMap(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}
