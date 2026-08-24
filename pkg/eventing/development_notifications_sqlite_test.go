//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
)

func TestDevelopmentNotificationsPersistTransitionsViewsAndPushState(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)

	draft := developmentnotifications.Draft{
		ID: "dnt_11111111111111111111111111111111", SourceKey: "gate:scope:one",
		Generation: 1, WorkspaceID: aggregate.Workspace.ID, Repository: aggregate.Workspace.Repository,
		Intent:     developmentnotifications.IntentPickupPR,
		SourceKind: developmentnotifications.SourcePullRequest,
		Phase:      "triage", Reason: developmentnotifications.ReasonScopeException,
		Title: "Scope decision", Summary: "Choose disposition",
		Target: developmentnotifications.Target{Panel: "scope", EntityID: "pgr_11111111111111111111111111111111"},
	}
	created, err := store.UpsertDevelopmentNotification(context.Background(), draft)
	require.NoError(t, err)
	require.True(t, created.Created)
	require.Equal(t, developmentnotifications.PriorityMedium, created.Notification.Priority)

	listed, err := store.ListDevelopmentNotifications(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	read, err := store.MutateDevelopmentNotification(
		context.Background(), listed[0].ID, listed[0].Version, "mark_read", nil,
	)
	require.NoError(t, err)
	require.True(t, read.Read)

	view, err := developmentnotifications.NewSavedView(developmentnotifications.SavedViewDraft{
		ID: "scope", Name: "Scope", Query: "reason = scope_exception ORDER BY updated DESC",
		Pinned: true, Default: true,
	}, now)
	require.NoError(t, err)
	views, err := store.PutDevelopmentNotificationViews(
		context.Background(),
		[]developmentnotifications.SavedView{view},
		1,
	)
	require.NoError(t, err)
	require.Equal(t, uint64(2), views.Version)
	reloadedViews, err := store.GetDevelopmentNotificationViews(context.Background())
	require.NoError(t, err)
	require.Equal(t, views, reloadedViews)

	push, err := store.PutDevelopmentPushState(context.Background(), json.RawMessage(`{"private_key":"private"}`), 1)
	require.NoError(t, err)
	require.Equal(t, uint64(2), push.Version)
	reloadedPush, err := store.GetDevelopmentPushState(context.Background())
	require.NoError(t, err)
	require.JSONEq(t, string(push.State), string(reloadedPush.State))
}

func TestWorkspacePatchReconcilesAttentionAtomicallyAndIgnoresWaitingGate(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)
	gate := testDevelopmentNotificationGate(PRExecutionWaitingGate)

	first, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "req_notification_waiting_gate",
		Patch:     PRWorkspacePatch{AppendGateRuns: []PRGateRun{gate}},
	})
	require.NoError(t, err)
	listed, err := store.ListDevelopmentNotifications(context.Background())
	require.NoError(t, err)
	require.Empty(t, listed, "an autonomous gate must not ask for human attention")

	gate.State = PRExecutionWaitingUser
	second, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: first.Aggregate.Workspace.ID, ExpectedVersion: first.Aggregate.Workspace.Version,
		RequestID: "req_notification_waiting_user",
		Patch:     PRWorkspacePatch{ReplaceGateRuns: []PRGateRun{gate}},
	})
	require.NoError(t, err)
	listed, err = store.ListDevelopmentNotifications(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, developmentnotifications.StatusOpen, listed[0].Status)

	gate.State = PRExecutionSucceeded
	_, err = store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: second.Aggregate.Workspace.ID, ExpectedVersion: second.Aggregate.Workspace.Version,
		RequestID: "req_notification_gate_resolved",
		Patch:     PRWorkspacePatch{ReplaceGateRuns: []PRGateRun{gate}},
	})
	require.NoError(t, err)
	resolved, err := store.GetDevelopmentNotification(context.Background(), listed[0].ID)
	require.NoError(t, err)
	require.Equal(t, developmentnotifications.StatusResolved, resolved.Status)
}

func TestWorkspacePatchRollsBackWhenAttentionProjectionFails(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 30, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)
	firstGate := testDevelopmentNotificationGate(PRExecutionWaitingUser)
	secondGate := testDevelopmentNotificationGate(PRExecutionWaitingUser)
	secondGate.ID = "pgr_00000000000000000000000000000092"
	corruptSourceKey := strings.Join([]string{
		aggregate.Workspace.ID, string(developmentnotifications.ReasonScopeException), secondGate.ID,
	}, ":")
	require.NoError(t, store.withImmediate(context.Background(), func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), `INSERT INTO development_notifications (
			id,source_key,workspace_id,generation,status,priority,version,payload_json,created_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?)`, developmentNotificationID(corruptSourceKey), corruptSourceKey,
			aggregate.Workspace.ID, 1, "open", "medium", 1, []byte(`{}`), toDBTime(now), toDBTime(now))
		return err
	}))

	_, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "req_invalid_notification_projection",
		Patch:     PRWorkspacePatch{AppendGateRuns: []PRGateRun{firstGate, secondGate}},
	})
	require.Error(t, err)
	after, getErr := store.GetPRWorkspace(context.Background(), aggregate.Workspace.ID)
	require.NoError(t, getErr)
	require.Equal(t, int64(1), after.Workspace.Version)
	require.Empty(t, after.GateRuns)
	var projected int
	require.NoError(t, store.db.QueryRowContext(context.Background(), `SELECT count(*)
		FROM development_notifications WHERE source_key = ?`, strings.Join([]string{
		aggregate.Workspace.ID, string(developmentnotifications.ReasonScopeException), firstGate.ID,
	}, ":")).Scan(&projected))
	require.Zero(t, projected, "the valid notification written before the failure must roll back")
}

func TestWorkspacePatchProjectsLatestCharterClarification(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 45, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)
	charter := PRCharterRevision{
		PRWorkspaceRecord: PRWorkspaceRecord{ID: "pcr_00000000000000000000000000000092"},
		Status:            PRRecordDraft, Revision: 1, Type: PRTypeFix,
		Goal: "Implement the requested behavior", AcceptanceCriteria: []string{"Behavior is covered"},
		ClarificationNeeded: true, ClarificationQuestion: "Which behavior should be the default?",
		BaseSHA: "aaaaaaaa", HeadSHA: "bbbbbbbb", CreatedBy: "ai",
	}
	first, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "req_charter_clarification_open",
		Patch:     PRWorkspacePatch{AppendCharters: []PRCharterRevision{charter}},
	})
	require.NoError(t, err)
	listed, err := store.ListDevelopmentNotifications(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, developmentnotifications.ReasonCharterAmbiguity, listed[0].Reason)
	require.Equal(t, charter.ID, listed[0].Target.EntityID)
	require.Equal(t, "charter", listed[0].Target.Panel)
	require.Equal(t, charter.ClarificationQuestion, listed[0].Summary)

	clarified := charter
	clarified.ID = "pcr_00000000000000000000000000000093"
	clarified.Revision = 2
	clarified.ClarificationNeeded = false
	clarified.ClarificationQuestion = ""
	_, err = store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: first.Aggregate.Workspace.ID, ExpectedVersion: first.Aggregate.Workspace.Version,
		RequestID: "req_charter_clarification_resolved",
		Patch:     PRWorkspacePatch{AppendCharters: []PRCharterRevision{clarified}},
	})
	require.NoError(t, err)
	resolved, err := store.GetDevelopmentNotification(context.Background(), listed[0].ID)
	require.NoError(t, err)
	require.Equal(t, developmentnotifications.StatusResolved, resolved.Status)
}

func TestDevelopmentNotificationBulkMutationIsAtomicAndReplayable(t *testing.T) {
	now := time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)
	created := make([]developmentnotifications.Notification, 0, 2)
	for index := 1; index <= 2; index++ {
		result, err := store.UpsertDevelopmentNotification(context.Background(), developmentnotifications.Draft{
			ID: fmt.Sprintf("dnt_%032x", index), SourceKey: fmt.Sprintf("bulk:%d", index),
			Generation: 1, WorkspaceID: aggregate.Workspace.ID, Repository: aggregate.Workspace.Repository,
			Intent: developmentnotifications.IntentPickupPR, SourceKind: developmentnotifications.SourcePullRequest,
			Phase: "triage", Reason: developmentnotifications.ReasonScopeException,
			Title: "Scope decision", Summary: "Choose disposition",
			Target: developmentnotifications.Target{Panel: "scope"},
		})
		require.NoError(t, err)
		created = append(created, result.Notification)
	}
	input := DevelopmentNotificationBulkMutation{
		RequestID: "notification-bulk-request-1", Action: "mark_read",
		Items: []DevelopmentNotificationMutation{
			{ID: created[0].ID, ExpectedVersion: created[0].Version},
			{ID: created[1].ID, ExpectedVersion: created[1].Version},
		},
	}
	first, err := store.MutateDevelopmentNotifications(context.Background(), input)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	require.Len(t, first.Notifications, 2)
	require.True(t, first.Notifications[0].Read)

	replay, err := store.MutateDevelopmentNotifications(context.Background(), input)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, first.Notifications, replay.Notifications)

	conflictingRequest := input
	conflictingRequest.Action = "archive"
	_, err = store.MutateDevelopmentNotifications(context.Background(), conflictingRequest)
	require.ErrorIs(t, err, ErrPRWorkspaceConflict)

	_, err = store.MutateDevelopmentNotifications(context.Background(), DevelopmentNotificationBulkMutation{
		RequestID: "notification-bulk-request-rollback", Action: "mark_unread",
		Items: []DevelopmentNotificationMutation{
			{ID: created[0].ID, ExpectedVersion: first.Notifications[0].Version},
			{ID: created[1].ID, ExpectedVersion: first.Notifications[1].Version + 10},
		},
	})
	require.ErrorIs(t, err, ErrPRWorkspaceConflict)
	unchanged, err := store.GetDevelopmentNotification(context.Background(), created[0].ID)
	require.NoError(t, err)
	require.True(t, unchanged.Read, "the first write must roll back when a later item conflicts")
	require.Equal(t, first.Notifications[0].Version, unchanged.Version)
}

func TestListDevelopmentNotificationsDoesNotFailAboveTenThousand(t *testing.T) {
	now := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)
	require.NoError(t, store.withImmediate(context.Background(), func(conn *sql.Conn) error {
		for index := 1; index <= 10001; index++ {
			notification := developmentnotifications.Notification{
				ID: fmt.Sprintf("dnt_%032x", index), SourceKey: fmt.Sprintf("pile:%d", index),
				Generation: 1, WorkspaceID: aggregate.Workspace.ID, Repository: aggregate.Workspace.Repository,
				Intent: developmentnotifications.IntentPickupPR, SourceKind: developmentnotifications.SourcePullRequest,
				Phase: "triage", Reason: developmentnotifications.ReasonScopeException,
				Priority: developmentnotifications.PriorityMedium, Status: developmentnotifications.StatusOpen,
				Title: "Scope decision", Summary: "Choose disposition",
				Target: developmentnotifications.Target{Panel: "scope"}, Version: 1,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := writeDevelopmentNotificationTx(context.Background(), conn, notification); err != nil {
				return err
			}
		}
		return nil
	}))
	listed, err := store.ListDevelopmentNotifications(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 10001)
}

func TestListRecentDevelopmentPushNotificationsIsSelectiveAndBounded(t *testing.T) {
	now := time.Date(2026, 8, 24, 16, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)
	for index, reason := range []developmentnotifications.Reason{
		developmentnotifications.ReasonScopeException,
		developmentnotifications.ReasonImplementationBlocked,
		developmentnotifications.ReasonProviderOutcomeUnknown,
	} {
		_, err := store.UpsertDevelopmentNotification(context.Background(), developmentnotifications.Draft{
			ID: fmt.Sprintf("dnt_%032x", index+100), SourceKey: fmt.Sprintf("push:%d", index),
			Generation: 1, WorkspaceID: aggregate.Workspace.ID, Repository: aggregate.Workspace.Repository,
			Intent: developmentnotifications.IntentPickupPR, SourceKind: developmentnotifications.SourcePullRequest,
			Phase: "implementation", Reason: reason, Title: "Attention", Summary: "Review it",
			Target: developmentnotifications.Target{Panel: "overview"},
		})
		require.NoError(t, err)
		clock.Advance(time.Second)
	}
	listed, err := store.ListRecentDevelopmentPushNotifications(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, developmentnotifications.PriorityCritical, listed[0].Priority)
	_, err = store.ListRecentDevelopmentPushNotifications(context.Background(), 2049)
	require.ErrorIs(t, err, ErrInvalidPRWorkspace)
}

func testDevelopmentNotificationGate(state PRExecutionState) PRGateRun {
	return PRGateRun{
		PRWorkspaceRecord: PRWorkspaceRecord{ID: "pgr_00000000000000000000000000000091"},
		DecisionPoint:     "pr.scope.exception", State: state, PolicyRevision: "policy-v1",
		WorkflowRef: "workflows/development.yml", WorkflowRevision: "workflow-v1",
		GateRef: "gates.scope-exception", WorkflowConfigurationID: "default",
		WorkflowConfigurationRevision: "config-v1", PinnedPolicy: json.RawMessage(`{"version":1}`),
		PinnedPolicyHash: "policy-hash", SubjectRevision: "subject-v1",
		PinnedSubject: json.RawMessage(`{"finding":"one"}`), PinnedSubjectHash: "subject-hash",
	}
}

func TestDevelopmentNotificationGenerationAndRetention(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)
	draft := developmentnotifications.Draft{
		ID: "dnt_22222222222222222222222222222222", SourceKey: "publication:unknown",
		Generation: 1, WorkspaceID: aggregate.Workspace.ID, Repository: aggregate.Workspace.Repository,
		Intent:     developmentnotifications.IntentPickupPR,
		SourceKind: developmentnotifications.SourcePullRequest,
		Phase:      "publication", Reason: developmentnotifications.ReasonProviderOutcomeUnknown,
		Title: "Unknown", Summary: "Reconcile", Target: developmentnotifications.Target{Panel: "publication"},
	}
	created, err := store.UpsertDevelopmentNotification(context.Background(), draft)
	require.NoError(t, err)
	clock.Advance(time.Hour)
	_, err = store.MutateDevelopmentNotification(
		context.Background(), created.Notification.ID, created.Notification.Version, "resolve", nil,
	)
	require.NoError(t, err)
	draft.Generation = 2
	reopened, err := store.UpsertDevelopmentNotification(context.Background(), draft)
	require.NoError(t, err)
	require.True(t, reopened.NewGeneration)
	require.Equal(t, developmentnotifications.StatusOpen, reopened.Notification.Status)
	clock.Advance(time.Hour)
	resolved, err := store.MutateDevelopmentNotification(
		context.Background(), reopened.Notification.ID, reopened.Notification.Version, "resolve", nil,
	)
	require.NoError(t, err)
	clock.Advance(91 * 24 * time.Hour)
	count, err := store.PruneDevelopmentNotifications(context.Background(), clock.Now().Add(-90*24*time.Hour), 10)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	_, err = store.GetDevelopmentNotification(context.Background(), resolved.ID)
	require.ErrorIs(t, err, ErrNotFound)
}
