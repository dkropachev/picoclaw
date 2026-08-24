//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
)

func TestSchemaV20DestructivelyReplacesV19WorkspaceTables(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "v19-to-v20.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1 + "\n" + schemaV2 + `
		CREATE TABLE pr_parent (id TEXT PRIMARY KEY);
		CREATE TABLE pr_child (
			id TEXT PRIMARY KEY,
			parent_id TEXT NOT NULL REFERENCES pr_parent(id)
		);`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO event_inbox (
		id, source, connector, event_type, dedupe_key, received_at, payload_json,
		attributes_json, routing_status, routing_available_at, routing_updated_at
	) VALUES ('ev_00000000000000000000000000000020', 'github', 'primary',
		'issues.opened', 'delivery-v20', 20, '{}', '{}', 'pending', 20, 20)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO pr_parent(id) VALUES ('parent')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO pr_child(id,parent_id) VALUES ('child','parent')`)
	require.NoError(t, err)
	_, err = db.Exec(`PRAGMA user_version = 19`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	var retained int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM event_inbox
		WHERE id = 'ev_00000000000000000000000000000020'`).Scan(&retained))
	assert.Equal(t, 1, retained, "generic event data must survive the destructive workspace cutover")
	assert.False(t, schemaObjectExists(t, store.db, "table", "pr_parent"))
	assert.False(t, schemaObjectExists(t, store.db, "table", "pr_child"))
	for _, table := range []string{
		"pr_workspaces",
		"development_notifications",
		"development_notification_views",
		"development_notification_requests",
		"development_push_state",
	} {
		assert.True(t, schemaObjectExists(t, store.db, "table", table), table)
	}
	var version int
	require.NoError(t, store.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, schemaVersion, version)
}

func TestSchemaV20RejectsCyclicV19WorkspaceTablesWithoutDataLoss(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "v19-cycle.db")
	db := openSchemaTestDB(t, path)
	_, err := db.Exec(schemaV1 + "\n" + schemaV2 + `
		CREATE TABLE pr_cycle_a (
			id TEXT PRIMARY KEY,
			b_id TEXT REFERENCES pr_cycle_b(id)
		);
		CREATE TABLE pr_cycle_b (
			id TEXT PRIMARY KEY,
			a_id TEXT REFERENCES pr_cycle_a(id)
		);`)
	require.NoError(t, err)
	_, err = db.Exec(`PRAGMA user_version = 19`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := Open(context.Background(), path)
	assert.Nil(t, store)
	require.ErrorIs(t, err, ErrSchemaInvalid)

	db = openSchemaTestDB(t, path)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	assert.True(t, schemaObjectExists(t, db, "table", "pr_cycle_a"))
	assert.True(t, schemaObjectExists(t, db, "table", "pr_cycle_b"))
	var version int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 19, version, "failed destructive cutover must roll back")
}

func TestSchemaV20ValidationRejectsMissingNotificationObjects(t *testing.T) {
	t.Parallel()

	for name, statement := range map[string]string{
		"notifications table": `DROP TABLE development_notifications`,
		"push index":          `DROP INDEX development_notifications_push`,
		"views table":         `DROP TABLE development_notification_views`,
		"requests table":      `DROP TABLE development_notification_requests`,
		"push state table":    `DROP TABLE development_push_state`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := openPRWorkspaceTestStore(t, newMutableClock(time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)))
			require.NoError(t, store.withImmediate(context.Background(), func(conn *sql.Conn) error {
				_, err := conn.ExecContext(context.Background(), statement)
				return err
			}))
			err := store.withImmediate(context.Background(), func(conn *sql.Conn) error {
				return validateSchemaV19PRWorkspace(context.Background(), conn)
			})
			assert.ErrorIs(t, err, ErrSchemaInvalid)
		})
	}
}

func TestEventingNotificationDraftsCoverEveryAttentionKind(t *testing.T) {
	workspaceID := "devw_00000000000000000000000000000020"
	activeCharterID := "pcr_00000000000000000000000000000020"
	aggregate := PRWorkspaceAggregate{
		Workspace: PRWorkspace{
			ID: workspaceID, Intent: DevelopmentImplementFeature, SourceKind: DevelopmentSourceBrief,
			SourceID: "brief-20", Repository: "Octo/Project", Phase: PRWorkspaceImplementation,
			ExecutionState: PRExecutionFailed, ActiveCharterID: activeCharterID,
		},
		Charters: []PRCharterRevision{
			{
				PRWorkspaceRecord: PRWorkspaceRecord{ID: "pcr_00000000000000000000000000000019", Ordinal: 1},
				Status:            PRRecordDraft, Revision: 1, ClarificationNeeded: true,
				ClarificationQuestion: "Old question",
			},
			{
				PRWorkspaceRecord: PRWorkspaceRecord{ID: activeCharterID, Ordinal: 2},
				Status:            PRRecordDraft, Revision: 2, ClarificationNeeded: true,
				ClarificationQuestion: "Choose the public API shape",
			},
		},
		GateRuns: []PRGateRun{
			notificationGateForDecision("pgr_00000000000000000000000000000020", "development.charter.approval"),
			notificationGateForDecision("pgr_00000000000000000000000000000021", "development.publish.approval"),
			notificationGateForDecision("pgr_00000000000000000000000000000022", "development.reconcile.provider"),
			notificationGateForDecision("pgr_00000000000000000000000000000023", "development.scope.exception"),
			{
				PRWorkspaceRecord: PRWorkspaceRecord{ID: "pgr_00000000000000000000000000000024"},
				DecisionPoint:     "development.scope.autonomous", State: PRExecutionWaitingGate,
			},
		},
		Publications: []PRPublication{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: "ppb_00000000000000000000000000000020"},
			ExecutionState:    PRExecutionUnknown,
		}},
		StageRuns: []PRStageRun{{Attempt: 1}, {Attempt: 3}},
		Messages: []PRMessage{
			{
				PRWorkspaceRecord: PRWorkspaceRecord{ID: "pms_00000000000000000000000000000020"},
				Kind:              "development_chat:steer:needs_clarification", CharterID: "pcr_stale",
			},
			{
				PRWorkspaceRecord: PRWorkspaceRecord{ID: "pms_00000000000000000000000000000021"},
				Kind:              "development_chat:steer:needs_clarification", CharterID: activeCharterID,
			},
			{
				PRWorkspaceRecord: PRWorkspaceRecord{ID: "pms_00000000000000000000000000000022"},
				Kind:              "development_chat:steer:accepted", CharterID: activeCharterID,
			},
			{
				PRWorkspaceRecord: PRWorkspaceRecord{ID: "pms_00000000000000000000000000000023"},
				Kind:              "development_chat:steer:needs_clarification", CharterID: activeCharterID,
			},
		},
	}

	drafts := eventingNotificationDrafts(aggregate)
	require.Len(t, drafts, 8)
	counts := make(map[developmentnotifications.Reason]int)
	byEntity := make(map[string]developmentnotifications.Draft)
	for _, draft := range drafts {
		counts[draft.Reason]++
		byEntity[draft.Target.EntityID] = draft
		assert.Equal(t, workspaceID, draft.WorkspaceID)
		assert.Equal(t, developmentnotifications.IntentImplementFeature, draft.Intent)
		assert.Equal(t, developmentnotifications.SourceBrief, draft.SourceKind)
		assert.NotEmpty(t, draft.ID)
	}
	assert.Equal(t, 2, counts[developmentnotifications.ReasonCharterAmbiguity])
	assert.Equal(t, 1, counts[developmentnotifications.ReasonPublicationApproval])
	assert.Equal(t, 2, counts[developmentnotifications.ReasonProviderOutcomeUnknown])
	assert.Equal(t, 1, counts[developmentnotifications.ReasonScopeException])
	assert.Equal(t, 1, counts[developmentnotifications.ReasonImplementationBlocked])
	assert.Equal(t, 1, counts[developmentnotifications.ReasonSteeringScopeChange])
	assert.Equal(t, uint64(3), byEntity[workspaceID].Generation)
	assert.Equal(t, uint64(1), byEntity["ppb_00000000000000000000000000000020"].Generation)
	assert.Equal(t, "publication", byEntity["pgr_00000000000000000000000000000021"].Target.Panel)
	assert.Equal(t, "charter", byEntity[activeCharterID].Target.Panel)
}

func notificationGateForDecision(id, decision string) PRGateRun {
	return PRGateRun{
		PRWorkspaceRecord: PRWorkspaceRecord{ID: id},
		DecisionPoint:     decision, State: PRExecutionWaitingUser,
		Turns: []PRGateTurn{{StageID: "stage-1", Kind: "form", Status: "waiting"}},
	}
}

func TestDevelopmentNotificationTransitionsAndBulkValidation(t *testing.T) {
	now := time.Date(2026, 8, 24, 17, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)
	draft := v20NotificationDraft(aggregate, "dnt_30000000000000000000000000000001", "v20:transition")

	created, err := store.UpsertDevelopmentNotification(context.Background(), draft)
	require.NoError(t, err)
	retry, err := store.UpsertDevelopmentNotification(context.Background(), draft)
	require.NoError(t, err)
	assert.False(t, retry.Changed)

	_, err = store.GetDevelopmentNotification(context.Background(), "dnt_ffffffffffffffffffffffffffffffff")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.MutateDevelopmentNotification(
		context.Background(), "dnt_ffffffffffffffffffffffffffffffff", 1, "mark_read", nil,
	)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.MutateDevelopmentNotification(
		context.Background(), created.Notification.ID, created.Notification.Version+1, "mark_read", nil,
	)
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)
	_, err = store.MutateDevelopmentNotification(
		context.Background(), created.Notification.ID, created.Notification.Version, "unsupported", nil,
	)
	assert.ErrorIs(t, err, developmentnotifications.ErrInvalidTransition)

	current, err := store.MutateDevelopmentNotification(
		context.Background(), created.Notification.ID, created.Notification.Version, "mark_read", nil,
	)
	require.NoError(t, err)
	unchanged, err := store.MutateDevelopmentNotification(
		context.Background(), current.ID, current.Version, "mark_read", nil,
	)
	require.NoError(t, err)
	assert.Equal(t, current.Version, unchanged.Version)
	current, err = store.MutateDevelopmentNotification(
		context.Background(), current.ID, current.Version, "mark_unread", nil,
	)
	require.NoError(t, err)
	_, err = store.MutateDevelopmentNotification(context.Background(), current.ID, current.Version, "snooze", nil)
	assert.ErrorIs(t, err, developmentnotifications.ErrInvalidTransition)
	past := now.Add(-time.Minute)
	_, err = store.MutateDevelopmentNotification(context.Background(), current.ID, current.Version, "snooze", &past)
	assert.ErrorIs(t, err, developmentnotifications.ErrInvalidTransition)
	future := now.Add(time.Hour)
	current, err = store.MutateDevelopmentNotification(
		context.Background(), current.ID, current.Version, "snooze", &future,
	)
	require.NoError(t, err)
	current, err = store.MutateDevelopmentNotification(
		context.Background(), current.ID, current.Version, "clear_snooze", nil,
	)
	require.NoError(t, err)
	unchanged, err = store.MutateDevelopmentNotification(
		context.Background(), current.ID, current.Version, "clear_snooze", nil,
	)
	require.NoError(t, err)
	assert.Equal(t, current.Version, unchanged.Version)
	current, err = store.MutateDevelopmentNotification(
		context.Background(), current.ID, current.Version, "resolve", nil,
	)
	require.NoError(t, err)
	current, err = store.MutateDevelopmentNotification(
		context.Background(), current.ID, current.Version, "archive", nil,
	)
	require.NoError(t, err)
	assert.Equal(t, developmentnotifications.StatusArchived, current.Status)
	_, err = store.MutateDevelopmentNotification(
		context.Background(), current.ID, current.Version, "snooze", &future,
	)
	assert.ErrorIs(t, err, developmentnotifications.ErrInvalidTransition)

	second, err := store.UpsertDevelopmentNotification(
		context.Background(), v20NotificationDraft(aggregate, "dnt_30000000000000000000000000000002", "v20:bulk"),
	)
	require.NoError(t, err)
	item := DevelopmentNotificationMutation{ID: second.Notification.ID, ExpectedVersion: second.Notification.Version}
	for name, input := range map[string]DevelopmentNotificationBulkMutation{
		"invalid request": {RequestID: "has whitespace", Action: "mark_read", Items: []DevelopmentNotificationMutation{item}},
		"empty items":     {RequestID: "v20-empty", Action: "mark_read"},
		"invalid action":  {RequestID: "v20-action", Action: "invalid", Items: []DevelopmentNotificationMutation{item}},
		"snooze nil":      {RequestID: "v20-snooze-nil", Action: "snooze", Items: []DevelopmentNotificationMutation{item}},
		"unexpected time": {RequestID: "v20-time", Action: "mark_read", Items: []DevelopmentNotificationMutation{item}, SnoozedUntil: &future},
		"invalid id":      {RequestID: "v20-id", Action: "mark_read", Items: []DevelopmentNotificationMutation{{ID: "bad", ExpectedVersion: 1}}},
		"zero version":    {RequestID: "v20-version", Action: "mark_read", Items: []DevelopmentNotificationMutation{{ID: item.ID}}},
		"duplicate":       {RequestID: "v20-duplicate", Action: "mark_read", Items: []DevelopmentNotificationMutation{item, item}},
	} {
		t.Run(name, func(t *testing.T) {
			_, mutationErr := store.MutateDevelopmentNotifications(context.Background(), input)
			require.Error(t, mutationErr)
		})
	}

	bulkInput := DevelopmentNotificationBulkMutation{
		RequestID: "v20-bulk-snooze", Action: "snooze",
		Items: []DevelopmentNotificationMutation{item}, SnoozedUntil: &future,
	}
	bulk, err := store.MutateDevelopmentNotifications(context.Background(), bulkInput)
	require.NoError(t, err)
	require.Len(t, bulk.Notifications, 1)
	require.NotNil(t, bulk.Notifications[0].SnoozedUntil)
	require.NoError(t, store.withImmediate(context.Background(), func(conn *sql.Conn) error {
		_, updateErr := conn.ExecContext(context.Background(), `UPDATE development_notification_requests
			SET result_json = 'xx' WHERE request_id = ?`, bulkInput.RequestID)
		return updateErr
	}))
	_, err = store.MutateDevelopmentNotifications(context.Background(), bulkInput)
	assert.ErrorContains(t, err, "decode development notification replay")

	_, err = store.MutateDevelopmentNotifications(context.Background(), DevelopmentNotificationBulkMutation{
		RequestID: "v20-bulk-missing", Action: "mark_read",
		Items: []DevelopmentNotificationMutation{{
			ID: "dnt_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", ExpectedVersion: 1,
		}},
	})
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestDevelopmentNotificationDocumentsRejectStaleAndCorruptState(t *testing.T) {
	now := time.Date(2026, 8, 24, 18, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)

	views, err := store.GetDevelopmentNotificationViews(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), views.Version)
	assert.Empty(t, views.Views)
	view, err := developmentnotifications.NewSavedView(developmentnotifications.SavedViewDraft{
		ID: "v20", Name: "V20", Query: "status = open ORDER BY updated DESC", Default: true,
	}, now)
	require.NoError(t, err)
	_, err = store.PutDevelopmentNotificationViews(context.Background(), []developmentnotifications.SavedView{view}, 0)
	assert.ErrorIs(t, err, developmentnotifications.ErrInvalidSavedView)
	_, err = store.PutDevelopmentNotificationViews(context.Background(), []developmentnotifications.SavedView{view}, 2)
	assert.ErrorIs(t, err, developmentnotifications.ErrStaleViewVersion)
	storedViews, err := store.PutDevelopmentNotificationViews(
		context.Background(), []developmentnotifications.SavedView{view}, 1,
	)
	require.NoError(t, err)
	_, err = store.PutDevelopmentNotificationViews(context.Background(), storedViews.Views, 1)
	assert.ErrorIs(t, err, developmentnotifications.ErrStaleViewVersion)
	storedViews, err = store.PutDevelopmentNotificationViews(context.Background(), storedViews.Views, 2)
	require.NoError(t, err)
	assert.Equal(t, uint64(3), storedViews.Version)
	_, err = store.db.Exec(`UPDATE development_notification_views SET payload_json = '{}' WHERE id = 'singleton'`)
	require.NoError(t, err)
	_, err = store.GetDevelopmentNotificationViews(context.Background())
	require.Error(t, err)

	push, err := store.GetDevelopmentPushState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), push.Version)
	assert.JSONEq(t, `{}`, string(push.State))
	_, err = store.PutDevelopmentPushState(context.Background(), json.RawMessage(`x`), 1)
	assert.ErrorIs(t, err, ErrInvalidPRWorkspace)
	_, err = store.PutDevelopmentPushState(context.Background(), json.RawMessage(`{}`), 0)
	assert.ErrorIs(t, err, ErrInvalidPRWorkspace)
	_, err = store.PutDevelopmentPushState(context.Background(), json.RawMessage(`{"endpoint":"one"}`), 2)
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)
	push, err = store.PutDevelopmentPushState(context.Background(), json.RawMessage(`{"endpoint":"one"}`), 1)
	require.NoError(t, err)
	_, err = store.PutDevelopmentPushState(context.Background(), json.RawMessage(`{"endpoint":"two"}`), 1)
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)
	push, err = store.PutDevelopmentPushState(context.Background(), json.RawMessage(`{"endpoint":"two"}`), push.Version)
	require.NoError(t, err)
	assert.Equal(t, uint64(3), push.Version)
	_, err = store.db.Exec(`UPDATE development_push_state SET payload_json = 'xx' WHERE id = 'singleton'`)
	require.NoError(t, err)
	_, err = store.GetDevelopmentPushState(context.Background())
	assert.ErrorContains(t, err, "development push state is invalid")

	_, err = store.PruneDevelopmentNotifications(context.Background(), time.Time{}, 1)
	assert.ErrorIs(t, err, ErrInvalidPRWorkspace)
	_, err = store.PruneDevelopmentNotifications(context.Background(), now, 0)
	assert.ErrorIs(t, err, ErrInvalidPRWorkspace)
	count, err := store.PruneDevelopmentNotifications(context.Background(), now, 501)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestDevelopmentNotificationReadsRejectCorruptDurablePayloads(t *testing.T) {
	now := time.Date(2026, 8, 24, 19, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	aggregate := createPRWorkspaceForTest(t, store, now)
	const notificationID = "dnt_30000000000000000000000000000003"
	require.NoError(t, store.withImmediate(context.Background(), func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), `INSERT INTO development_notifications (
			id,source_key,workspace_id,generation,status,priority,version,payload_json,created_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?)`, notificationID, "v20:corrupt", aggregate.Workspace.ID,
			1, "open", "critical", 1, []byte(`{}`), toDBTime(now), toDBTime(now))
		return err
	}))

	_, err := store.GetDevelopmentNotification(context.Background(), notificationID)
	assert.ErrorContains(t, err, "payload is invalid")
	_, err = store.ListDevelopmentNotifications(context.Background())
	assert.ErrorContains(t, err, "payload is invalid")
	_, err = store.ListRecentDevelopmentPushNotifications(context.Background(), 10)
	assert.ErrorContains(t, err, "payload is invalid")
	_, err = store.MutateDevelopmentNotification(context.Background(), notificationID, 1, "mark_read", nil)
	assert.ErrorContains(t, err, "payload is invalid")
	_, err = store.UpsertDevelopmentNotification(context.Background(), developmentnotifications.Draft{
		ID:          notificationID,
		SourceKey:   "v20:corrupt",
		Generation:  2,
		WorkspaceID: aggregate.Workspace.ID,
		Repository:  aggregate.Workspace.Repository,
		Intent:      developmentnotifications.IntentPickupPR,
		SourceKind:  developmentnotifications.SourcePullRequest,
		Phase:       "review",
		Reason:      developmentnotifications.ReasonProviderOutcomeUnknown,
		Title:       "Reconcile",
		Summary:     "Inspect provider state",
		Target:      developmentnotifications.Target{Panel: "publication"},
	})
	require.Error(t, err)
}

func TestDevelopmentWorkspaceFeatureIdentityAndProviderProjection(t *testing.T) {
	now := time.Date(2026, 8, 24, 20, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	store := openPRWorkspaceTestStore(t, clock)
	provider := testPRProviderSnapshot(now)
	provider.Intent = DevelopmentImplementFeature
	provider.SourceKind = DevelopmentSourceBrief
	provider.SourceID = " brief-20 "
	provider.SourceURL = " https://github.example.test/Octo/Project/issues/new "
	provider.SourceNumber = 0
	provider.PullRequestID = ""
	provider.PullNumber = 0

	aggregate, created, err := store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID: "v20-feature-create", WorkspaceID: "devw_30000000000000000000000000000001",
		Provider: provider, Phase: PRWorkspaceCharter, ExecutionState: PRExecutionWaitingUser,
	})
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, DevelopmentImplementFeature, aggregate.Workspace.Intent)
	assert.Equal(t, DevelopmentSourceBrief, aggregate.Workspace.SourceKind)
	assert.Equal(t, "brief-20", aggregate.Workspace.SourceID)

	observed := provider
	observed.SourceID = "brief-20"
	observed.SourceURL = "https://github.example.test/Octo/Project/issues/new"
	observed.PullRequestID = "pull-created-20"
	observed.PullNumber = 20
	observed.ProviderRevision = "etag-2"
	observed.ObservedAt = now.Add(time.Minute)
	first, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "v20-feature-provider-pull", Patch: PRWorkspacePatch{ProviderSnapshot: &observed},
	})
	require.NoError(t, err)
	assert.Equal(t, "pull-created-20", first.Aggregate.Workspace.PullRequestID)
	assert.Equal(t, int64(20), first.Aggregate.Workspace.PullNumber)

	changedIdentity := observed
	changedIdentity.PullRequestID = "pull-created-21"
	changedIdentity.PullNumber = 21
	_, err = store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: first.Aggregate.Workspace.Version,
		RequestID: "v20-feature-provider-conflict", Patch: PRWorkspacePatch{ProviderSnapshot: &changedIdentity},
	})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	confirmedAt := now.Add(2 * time.Minute)
	charter := PRCharterRevision{
		PRWorkspaceRecord:  PRWorkspaceRecord{ID: "pcr_30000000000000000000000000000001"},
		Status:             PRRecordConfirmed,
		Revision:           1,
		Type:               PRTypeFeature,
		Goal:               "Implement a standalone development workspace",
		AcceptanceCriteria: []string{"Planning starts after confirmation"},
		BaseSHA:            "aaaaaaaa",
		HeadSHA:            "bbbbbbbb",
		CreatedBy:          "user",
		ConfirmedAt:        &confirmedAt,
	}
	planned, err := store.ApplyPRWorkspacePatch(context.Background(), PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: first.Aggregate.Workspace.Version,
		RequestID: "v20-feature-charter", Patch: PRWorkspacePatch{AppendCharters: []PRCharterRevision{charter}},
	})
	require.NoError(t, err)
	assert.Equal(t, PRWorkspacePlanning, planned.Aggregate.Workspace.Phase)
	assert.Equal(t, charter.ID, planned.Aggregate.Workspace.ActiveCharterID)

	issueProvider := provider
	issueProvider.SourceKind = DevelopmentSourceIssue
	issueProvider.SourceID = "issue-42"
	issueProvider.SourceNumber = 42
	issueProvider.SourceURL = "https://github.example.test/Octo/Project/issues/42"
	issue, issueCreated, err := store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID: "v20-issue-create", WorkspaceID: "devw_30000000000000000000000000000002",
		Provider: issueProvider,
	})
	require.NoError(t, err)
	require.True(t, issueCreated)
	assert.Equal(t, DevelopmentSourceIssue, issue.Workspace.SourceKind)
	assert.Equal(t, int64(42), issue.Workspace.SourceNumber)
}

func TestDevelopmentWorkspaceProviderIdentityValidation(t *testing.T) {
	now := time.Date(2026, 8, 24, 21, 0, 0, 0, time.UTC)
	validFeature := testPRProviderSnapshot(now)
	validFeature.Intent = DevelopmentImplementFeature
	validFeature.SourceKind = DevelopmentSourceBrief
	validFeature.SourceID = "brief-validation"
	validFeature.PullRequestID = ""
	validFeature.PullNumber = 0

	invalid := map[string]func(*PRProviderSnapshot){
		"intent": func(value *PRProviderSnapshot) { value.Intent = "invalid" },
		"source": func(value *PRProviderSnapshot) { value.SourceKind = "invalid" },
		"pickup brief": func(value *PRProviderSnapshot) {
			value.Intent = DevelopmentPickupPR
		},
		"feature pull request": func(value *PRProviderSnapshot) {
			value.SourceKind = DevelopmentSourcePullRequest
		},
		"partial created pull request": func(value *PRProviderSnapshot) {
			value.PullRequestID = "pull-partial"
		},
		"issue without number": func(value *PRProviderSnapshot) {
			value.SourceKind = DevelopmentSourceIssue
		},
	}
	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			value := validFeature
			mutate(&value)
			require.ErrorIs(t, validatePRProviderSnapshot(value), ErrInvalidPRWorkspace)
		})
	}

	invalidPull := testPRProviderSnapshot(now)
	invalidPull.Intent = DevelopmentPickupPR
	invalidPull.SourceKind = DevelopmentSourcePullRequest
	invalidPull.SourceID = "pull-missing"
	invalidPull.PullRequestID = ""
	invalidPull.PullNumber = 0
	require.ErrorIs(t, validatePRProviderSnapshot(invalidPull), ErrInvalidPRWorkspace)
}

func TestPRWorkspaceMutationDecoderRecognizesEveryDurableKind(t *testing.T) {
	kinds := []PRWorkspaceMutationKind{
		PRMutationProviderSnapshot,
		PRMutationCharter,
		PRMutationStageRun,
		PRMutationFinding,
		PRMutationFindingEvent,
		PRMutationConversation,
		PRMutationMessage,
		PRMutationCorrection,
		PRMutationRepositoryLesson,
		PRMutationNudgeRound,
		PRMutationNudgeReward,
		PRMutationDeferredGroup,
		PRMutationDeferredGroupItem,
		PRMutationRepairAttempt,
		PRMutationValidationRun,
		PRMutationGateRun,
		PRMutationPublication,
		PRMutationOperationIntent,
		PRMutationIngressWatermark,
		PRMutationActivity,
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			_, _, err := decodePRWorkspaceMutation(kind, json.RawMessage(`{}`))
			require.ErrorIs(t, err, ErrInvalidPRWorkspace)
			assert.NotContains(t, err.Error(), "unsupported mutation kind",
				"declared durable mutation kinds must remain wired to their typed decoder")
		})
	}

	_, _, err := decodePRWorkspaceMutation("future_kind", json.RawMessage(`{}`))
	require.ErrorIs(t, err, ErrInvalidPRWorkspace)
	assert.ErrorContains(t, err, "unsupported mutation kind")
	_, _, err = decodePRWorkspaceMutation(PRMutationWorkspaceState, nil)
	require.ErrorIs(t, err, ErrInvalidPRWorkspace)
	_, _, err = decodePRWorkspaceMutation(
		PRMutationWorkspaceState,
		json.RawMessage(`{"phase":"planning","execution_state":"queued"}`),
	)
	require.NoError(t, err)
}

func TestPRWorkspaceMutableRecordTransitionContracts(t *testing.T) {
	happy := []struct {
		name  string
		value any
	}{
		{"charter", &PRCharterRevision{}},
		{"stage", &PRStageRun{}},
		{"finding", &PRFinding{}},
		{"conversation", &PRConversation{}},
		{"correction", &PRCorrection{}},
		{"repository lesson", &PRRepositoryLesson{}},
		{"nudge round", &PRNudgeRound{}},
		{"deferred item", &PRDeferredGroupItem{}},
		{"repair attempt", &PRRepairAttempt{}},
		{"validation run", &PRValidationRun{}},
		{"gate run", &PRGateRun{}},
		{"publication", &PRPublication{}},
		{"operation intent", &PRWorkspaceOperationIntent{}},
		{"ingress watermark", &PRIngressWatermark{}},
	}
	for _, testCase := range happy {
		t.Run(testCase.name, func(t *testing.T) {
			existing, err := marshalPRWorkspaceRecordPersistence(testCase.value)
			require.NoError(t, err)
			require.NoError(t, validatePRWorkspaceRecordTransition(existing, testCase.value))
		})
	}

	confirmed, err := json.Marshal(PRCharterRevision{Status: PRRecordConfirmed, Revision: 1})
	require.NoError(t, err)
	err = validatePRWorkspaceRecordTransition(confirmed, &PRCharterRevision{Status: PRRecordConfirmed, Revision: 1})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	stageBefore, err := json.Marshal(PRStageRun{Evidence: json.RawMessage(`{"run":1}`)})
	require.NoError(t, err)
	err = validatePRWorkspaceRecordTransition(stageBefore, &PRStageRun{Evidence: json.RawMessage(`{"run":2}`)})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	findingBefore, err := marshalPRFindingPersistence(PRFinding{Origin: "review", Fingerprint: "one"})
	require.NoError(t, err)
	err = validatePRWorkspaceRecordTransition(findingBefore, &PRFinding{Origin: "review", Fingerprint: "two"})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	deferredBefore, err := json.Marshal(PRDeferredGroupItem{FindingID: "pfn_one"})
	require.NoError(t, err)
	err = validatePRWorkspaceRecordTransition(deferredBefore, &PRDeferredGroupItem{FindingID: "pfn_two"})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	claimedPublication, err := json.Marshal(PRPublication{Status: PRPublicationClaimed})
	require.NoError(t, err)
	err = validatePRWorkspaceRecordTransition(claimedPublication, &PRPublication{Status: PRPublicationClaimed})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	leasedPublication, err := json.Marshal(PRPublication{LeaseOwner: "worker"})
	require.NoError(t, err)
	err = validatePRWorkspaceRecordTransition(leasedPublication, &PRPublication{LeaseOwner: "worker"})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	attemptedPublication, err := json.Marshal(PRPublication{Attempts: 2})
	require.NoError(t, err)
	err = validatePRWorkspaceRecordTransition(attemptedPublication, &PRPublication{Attempts: 1})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	runningOperation, err := json.Marshal(PRWorkspaceOperationIntent{State: PRExecutionRunning})
	require.NoError(t, err)
	err = validatePRWorkspaceRecordTransition(runningOperation, &PRWorkspaceOperationIntent{State: PRExecutionRunning})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)
}

func TestDevelopmentNotificationStoreAPIsRejectClosedStore(t *testing.T) {
	now := time.Date(2026, 8, 24, 22, 0, 0, 0, time.UTC)
	store := openPRWorkspaceTestStore(t, newMutableClock(now))
	require.NoError(t, store.Close())
	draft := developmentnotifications.Draft{
		ID:          "dnt_30000000000000000000000000000004",
		SourceKey:   "v20:closed",
		Generation:  1,
		WorkspaceID: "devw_30000000000000000000000000000004",
		Repository:  "Octo/Project",
		Intent:      developmentnotifications.IntentPickupPR,
		SourceKind:  developmentnotifications.SourcePullRequest,
		Phase:       "review",
		Reason:      developmentnotifications.ReasonScopeException,
		Title:       "Scope decision",
		Summary:     "Choose a disposition",
		Target:      developmentnotifications.Target{Panel: "scope"},
	}
	_, err := store.UpsertDevelopmentNotification(context.Background(), draft)
	require.Error(t, err)
	_, err = store.ListDevelopmentNotifications(context.Background())
	require.Error(t, err)
	_, err = store.ListRecentDevelopmentPushNotifications(context.Background(), 1)
	require.Error(t, err)
	_, err = store.GetDevelopmentNotification(context.Background(), draft.ID)
	require.Error(t, err)
	_, err = store.MutateDevelopmentNotification(context.Background(), draft.ID, 1, "mark_read", nil)
	require.Error(t, err)
	_, err = store.MutateDevelopmentNotifications(context.Background(), DevelopmentNotificationBulkMutation{
		RequestID: "v20-closed", Action: "mark_read",
		Items: []DevelopmentNotificationMutation{{ID: draft.ID, ExpectedVersion: 1}},
	})
	require.Error(t, err)
	_, err = store.GetDevelopmentNotificationViews(context.Background())
	require.Error(t, err)
	_, err = store.GetDevelopmentPushState(context.Background())
	require.Error(t, err)
	_, err = store.PruneDevelopmentNotifications(context.Background(), now, 1)
	require.Error(t, err)
}

func TestCreateDevelopmentWorkspaceDefaultsAndIdentityConflicts(t *testing.T) {
	now := time.Date(2026, 8, 24, 23, 0, 0, 0, time.UTC)
	store := openPRWorkspaceTestStore(t, newMutableClock(now))
	provider := testPRProviderSnapshot(now)

	created, wasCreated, err := store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID: "v20-default-create", Provider: provider,
	})
	require.NoError(t, err)
	require.True(t, wasCreated)
	assert.Equal(t, PRWorkspaceIntake, created.Workspace.Phase)
	assert.Equal(t, PRExecutionQueued, created.Workspace.ExecutionState)

	_, _, err = store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID: "v20-wrong-workspace", WorkspaceID: "devw_30000000000000000000000000000005",
		Provider: provider,
	})
	assert.ErrorIs(t, err, ErrPRWorkspaceConflict)

	invalidWorkspace := provider
	invalidWorkspace.SourceID = "other-source"
	invalidWorkspace.PullRequestID = "other-source"
	_, _, err = store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID: "v20-invalid-workspace-id", WorkspaceID: "bad", Provider: invalidWorkspace,
	})
	assert.ErrorIs(t, err, ErrInvalidPRWorkspace)
	_, _, err = store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID: "has whitespace", Provider: invalidWorkspace,
	})
	assert.ErrorIs(t, err, ErrInvalidPRWorkspace)
	_, _, err = store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID: "v20-invalid-phase", Provider: invalidWorkspace, Phase: "future",
	})
	assert.ErrorIs(t, err, ErrInvalidPRWorkspace)
	_, _, err = store.CreatePRWorkspace(context.Background(), PRWorkspaceCreate{
		RequestID: "v20-invalid-state", Provider: invalidWorkspace, ExecutionState: "future",
	})
	assert.ErrorIs(t, err, ErrInvalidPRWorkspace)
}

func TestDevelopmentWorkspaceLifecycleCRUDSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	clock := newMutableClock(now)
	path := filepath.Join(t.TempDir(), "development-lifecycle.db")
	store, err := Open(ctx, path, WithClock(clock.Now))
	require.NoError(t, err)

	provider := testPRProviderSnapshot(now)
	provider.Intent = DevelopmentImplementFeature
	provider.SourceKind = DevelopmentSourceBrief
	provider.SourceID = "brief-lifecycle"
	provider.SourceURL = "https://github.example.test/Octo/Project/issues/new"
	provider.PullRequestID = ""
	provider.PullNumber = 0
	aggregate, created, err := store.CreatePRWorkspace(ctx, PRWorkspaceCreate{
		RequestID: "v20-lifecycle-create", WorkspaceID: "devw_40000000000000000000000000000001",
		Provider: provider, Phase: PRWorkspaceCharter, ExecutionState: PRExecutionWaitingUser,
	})
	require.NoError(t, err)
	require.True(t, created)

	const (
		charterID      = "pcr_40000000000000000000000000000001"
		stageID        = "psr_40000000000000000000000000000001"
		findingID      = "pfn_40000000000000000000000000000001"
		conversationID = "pcv_40000000000000000000000000000001"
		correctionID   = "pco_40000000000000000000000000000001"
		lessonID       = "prl_40000000000000000000000000000001"
		nudgeID        = "pnr_40000000000000000000000000000001"
		groupID        = "pdg_40000000000000000000000000000001"
		itemID         = "pdi_40000000000000000000000000000001"
		repairID       = "pra_40000000000000000000000000000001"
		validationID   = "pvr_40000000000000000000000000000001"
		gateID         = "pgr_40000000000000000000000000000001"
		publicationID  = "ppb_40000000000000000000000000000001"
		operationID    = "poi_40000000000000000000000000000001"
		watermarkID    = "piw_40000000000000000000000000000001"
	)
	request := json.RawMessage(`{"title":"Track deferred lifecycle"}`)
	digest := testPRWorkspacePayloadDigest(request)
	initial := PRWorkspacePatch{
		AppendCharters: []PRCharterRevision{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: charterID}, Status: PRRecordDraft,
			Revision: 1, Type: PRTypeFeature, Goal: "Implement durable development lifecycle",
			AcceptanceCriteria:  []string{"Every mutable record survives restart"},
			ClarificationNeeded: true, ClarificationQuestion: "Which lifecycle should be public?",
			BaseSHA: "aaaaaaaa", HeadSHA: "bbbbbbbb", CreatedBy: "user",
		}},
		AppendStageRuns: []PRStageRun{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: stageID}, Phase: PRWorkspacePlanning,
			Kind: "planning", State: PRExecutionRunning, Attempt: 1,
			WorkspaceVersion: aggregate.Workspace.Version, BaseSHA: "aaaaaaaa", HeadSHA: "bbbbbbbb",
		}},
		UpsertFindings: []PRFinding{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: findingID}, Origin: "planning", StageRunID: stageID,
			Fingerprint: "lifecycle-finding", Severity: "medium", Title: "Deferred follow-up",
			Message: "Track follow-up through publication", Disposition: PRFindingDeferred,
			ScopeDistance: PRScopeRelatedFollowup, ChangeSize: PRChangeSizeS,
			TypeCompatible: true, ClassificationConf: .9, Version: 1,
			EstimatedMetrics: PRChangeMetrics{Files: 1, SemanticLines: 10, Modules: 1},
		}},
		AppendConversations: []PRConversation{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: conversationID}, Channel: "workspace",
			Phase: PRWorkspacePlanning, Status: PRRecordActive,
		}},
		AppendCorrections: []PRCorrection{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: correctionID}, Kind: "scope", Status: PRRecordActive,
			TargetKind: "workspace", TargetID: aggregate.Workspace.ID,
			OriginalClaim: "Two modules", Correction: "One module", AppliesToReview: true,
		}},
		AppendLessons: []PRRepositoryLesson{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: lessonID}, RepositoryID: provider.RepositoryID,
			Status: PRRecordActive, Kind: "scope", Content: "Lifecycle state is local",
			SourceCorrectionID: correctionID, ConfirmedBy: "user",
		}},
		AppendNudgeRounds: []PRNudgeRound{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: nudgeID}, StageRunID: stageID,
			Phase: PRWorkspaceReview, State: PRExecutionRunning, Round: 1, MinimumRounds: 0, HardCap: 2,
			StrategyFamily: "coverage", CoverageTarget: "restart", ChallengeDigest: "challenge",
			PromptDigest: "prompt",
		}},
		UpsertDeferredGroups: []PRDeferredGroup{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: groupID}, Status: PRRecordDraft,
			Title: "Deferred lifecycle", Body: "Publish after core work", ScopeDistance: PRScopeRelatedFollowup,
			ChangeSize: PRChangeSizeS, DraftRevision: 1, Version: 1,
		}},
		UpsertDeferredItems: []PRDeferredGroupItem{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: itemID}, GroupID: groupID,
			FindingID: findingID, OrdinalInGroup: 0,
		}},
		AppendRepairAttempts: []PRRepairAttempt{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: repairID}, StageRunID: stageID,
			State: PRExecutionRunning, Attempt: 1, GoalDigest: "goal", BaseCommit: "bbbbbbbb",
			FindingIDs: []string{findingID}, Metrics: PRChangeMetrics{Files: 1, SemanticLines: 4, Modules: 1},
		}},
		AppendValidationRuns: []PRValidationRun{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: validationID}, StageRunID: stageID,
			RepairAttemptID: repairID, State: PRExecutionRunning, Kind: "local_ci",
		}},
		AppendGateRuns: []PRGateRun{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: gateID}, DecisionPoint: "development.publish",
			State: PRExecutionWaitingUser, PolicyRevision: "policy-v20",
			WorkflowRef: "workflows/development.yml", WorkflowRevision: "workflow-v20",
			GateRef: "gates.publish", WorkflowConfigurationID: "default",
			WorkflowConfigurationRevision: "config-v20", PinnedPolicy: json.RawMessage(`{}`),
			PinnedPolicyHash: "policy-hash", SubjectRevision: "subject-v20",
			PinnedSubject: json.RawMessage(`{}`), PinnedSubjectHash: "subject-hash",
		}},
		AppendPublications: []PRPublication{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: publicationID}, Kind: PRPublicationGitHubIssue,
			Status: PRPublicationPending, GateRunID: gateID, DeferredGroupID: groupID,
			FindingIDs: []string{findingID}, Request: request, RequestDigest: digest,
			PayloadDigest: digest, AvailableAt: now,
		}},
		AppendOperationIntents: []PRWorkspaceOperationIntent{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: operationID}, Kind: "planning",
			State: PRExecutionQueued, StageRunID: stageID, InputWorkspaceVersion: aggregate.Workspace.Version,
			InputDigest: "input", Input: json.RawMessage(`{"kind":"planning"}`), AvailableAt: now,
		}},
		UpsertIngressWatermarks: []PRIngressWatermark{{
			PRWorkspaceRecord: PRWorkspaceRecord{ID: watermarkID}, Source: "github", Connector: "primary",
			InboxReceivedAt: now, InboxEventID: "event-v20-1",
		}},
	}
	first, err := store.ApplyPRWorkspacePatch(ctx, PRWorkspacePatchMutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "v20-lifecycle-initial", Patch: initial,
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	store, err = Open(ctx, path, WithClock(clock.Now))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	restarted, err := store.GetPRWorkspace(ctx, aggregate.Workspace.ID)
	require.NoError(t, err)
	assert.Equal(t, first.Aggregate, restarted)
	listedNotifications, err := store.ListDevelopmentNotifications(ctx)
	require.NoError(t, err)
	require.Len(t, listedNotifications, 2)
	var ambiguityNotification developmentnotifications.Notification
	for _, notification := range listedNotifications {
		if notification.Reason == developmentnotifications.ReasonCharterAmbiguity {
			ambiguityNotification = notification
		}
	}
	require.NotEmpty(t, ambiguityNotification.ID)

	clock.Advance(time.Minute)
	confirmedAt := clock.Now()
	charter := restarted.Charters[0]
	charter.Status = PRRecordConfirmed
	charter.ClarificationNeeded = false
	charter.ClarificationQuestion = ""
	charter.ConfirmedAt = &confirmedAt
	stage := restarted.StageRuns[0]
	stage.State = PRExecutionSucceeded
	stage.FinishedAt = &confirmedAt
	finding := restarted.Findings[0]
	finding.Disposition = PRFindingFixed
	finding.DeferredGroupID = ""
	finding.Version++
	conversation := restarted.Conversations[0]
	conversation.Status = PRRecordResolved
	correction := restarted.Corrections[0]
	correction.Status = PRRecordSuperseded
	lesson := restarted.RepositoryLessons[0]
	lesson.Status = PRRecordRevoked
	lesson.RevokedAt = &confirmedAt
	nudge := restarted.NudgeRounds[0]
	nudge.State = PRExecutionSucceeded
	group := restarted.DeferredGroups[0]
	group.Status = PRRecordActive
	group.Version++
	item := group.Items[0]
	item.Removed = true
	item.GroupID = ""
	item.OrdinalInGroup = -1
	repair := restarted.RepairAttempts[0]
	repair.State = PRExecutionSucceeded
	repair.FinishedAt = &confirmedAt
	validation := restarted.ValidationRuns[0]
	validation.State = PRExecutionSucceeded
	validation.FinishedAt = &confirmedAt
	gate := restarted.GateRuns[0]
	gate.State = PRExecutionSucceeded
	gate.FinishedAt = &confirmedAt
	publication := restarted.Publications[0]
	publication.Status = PRPublicationPublished
	publication.PublishedAt = &confirmedAt
	operation := restarted.OperationIntents[0]
	operation.State = PRExecutionSucceeded
	operation.Result = json.RawMessage(`{"plan":"done"}`)
	watermark := restarted.IngressWatermarks[0]
	watermark.InboxReceivedAt = confirmedAt
	watermark.InboxEventID = "event-v20-2"
	updated, err := store.ApplyPRWorkspacePatch(ctx, PRWorkspacePatchMutation{
		WorkspaceID: restarted.Workspace.ID, ExpectedVersion: restarted.Workspace.Version,
		RequestID: "v20-lifecycle-update",
		Patch: PRWorkspacePatch{
			ReplaceCharters: []PRCharterRevision{charter}, ReplaceStageRuns: []PRStageRun{stage},
			UpsertFindings: []PRFinding{finding}, ReplaceConversations: []PRConversation{conversation},
			ReplaceCorrections: []PRCorrection{correction}, ReplaceLessons: []PRRepositoryLesson{lesson},
			ReplaceNudgeRounds: []PRNudgeRound{nudge}, UpsertDeferredGroups: []PRDeferredGroup{group},
			UpsertDeferredItems: []PRDeferredGroupItem{item}, ReplaceRepairAttempts: []PRRepairAttempt{repair},
			ReplaceValidationRuns: []PRValidationRun{validation}, ReplaceGateRuns: []PRGateRun{gate},
			ReplacePublications:     []PRPublication{publication},
			ReplaceOperationIntents: []PRWorkspaceOperationIntent{operation},
			UpsertIngressWatermarks: []PRIngressWatermark{watermark},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, PRWorkspacePlanning, updated.Aggregate.Workspace.Phase)
	assert.Equal(t, PRExecutionSucceeded, updated.Aggregate.StageRuns[0].State)
	assert.Equal(t, PRFindingFixed, updated.Aggregate.Findings[0].Disposition)
	assert.Equal(t, PRRecordResolved, updated.Aggregate.Conversations[0].Status)
	assert.Equal(t, PRPublicationPublished, updated.Aggregate.Publications[0].Status)
	assert.Equal(t, PRExecutionSucceeded, updated.Aggregate.OperationIntents[0].State)
	require.Len(t, updated.Aggregate.DeferredGroups, 1)
	assert.Empty(t, updated.Aggregate.DeferredGroups[0].Items)

	resolved, err := store.GetDevelopmentNotification(ctx, ambiguityNotification.ID)
	require.NoError(t, err)
	assert.Equal(t, developmentnotifications.StatusResolved, resolved.Status)
	replay, err := store.ApplyPRWorkspacePatch(ctx, PRWorkspacePatchMutation{
		WorkspaceID: restarted.Workspace.ID, ExpectedVersion: restarted.Workspace.Version,
		RequestID: "v20-lifecycle-update",
		Patch: PRWorkspacePatch{
			ReplaceCharters: []PRCharterRevision{charter}, ReplaceStageRuns: []PRStageRun{stage},
			UpsertFindings: []PRFinding{finding}, ReplaceConversations: []PRConversation{conversation},
			ReplaceCorrections: []PRCorrection{correction}, ReplaceLessons: []PRRepositoryLesson{lesson},
			ReplaceNudgeRounds: []PRNudgeRound{nudge}, UpsertDeferredGroups: []PRDeferredGroup{group},
			UpsertDeferredItems: []PRDeferredGroupItem{item}, ReplaceRepairAttempts: []PRRepairAttempt{repair},
			ReplaceValidationRuns: []PRValidationRun{validation}, ReplaceGateRuns: []PRGateRun{gate},
			ReplacePublications:     []PRPublication{publication},
			ReplaceOperationIntents: []PRWorkspaceOperationIntent{operation},
			UpsertIngressWatermarks: []PRIngressWatermark{watermark},
		},
	})
	require.NoError(t, err)
	assert.True(t, replay.Replayed)
	assert.Equal(t, updated.Aggregate, replay.Aggregate)
}

func TestDevelopmentWorkspaceDurableRecordValidationRejectsCorruption(t *testing.T) {
	tooManyStrings := make([]string, maxPRWorkspaceListEntries+1)
	tooManyChecks := make([]PRValidationCheck, maxPRWorkspaceListEntries+1)
	tooManyTurns := make([]PRGateTurn, maxPRWorkspaceListEntries+1)
	tooManyChanges := make([]PRScopeChange, maxPRWorkspaceListEntries+1)
	negativeReward := -0.1
	negativeMetrics := PRChangeMetrics{Files: -1}

	tests := []struct {
		name  string
		value any
	}{
		{"unsupported record", new(string)},
		{"charter revision", mutateV20Charter(func(value *PRCharterRevision) { value.Revision = 0 })},
		{"charter status", mutateV20Charter(func(value *PRCharterRevision) { value.Status = "bad" })},
		{"charter type", mutateV20Charter(func(value *PRCharterRevision) { value.Type = "bad" })},
		{"charter goal", mutateV20Charter(func(value *PRCharterRevision) { value.Goal = "" })},
		{"charter criteria", mutateV20Charter(func(value *PRCharterRevision) { value.AcceptanceCriteria = nil })},
		{"charter areas", mutateV20Charter(func(value *PRCharterRevision) { value.IncludedAreas = []string{""} })},
		{"charter exclusions", mutateV20Charter(func(value *PRCharterRevision) { value.Exclusions = tooManyStrings })},
		{"charter clarification mismatch", mutateV20Charter(func(value *PRCharterRevision) {
			value.ClarificationNeeded = true
		})},
		{"charter clarification text", mutateV20Charter(func(value *PRCharterRevision) {
			value.ClarificationNeeded = true
			value.ClarificationQuestion = strings.Repeat("q", 8<<10+1)
		})},
		{"charter base", mutateV20Charter(func(value *PRCharterRevision) { value.BaseSHA = "" })},
		{"charter confirmed time", mutateV20Charter(func(value *PRCharterRevision) {
			value.Status = PRRecordConfirmed
		})},
		{"stage attempt", mutateV20Stage(func(value *PRStageRun) { value.Attempt = 0 })},
		{"stage phase", mutateV20Stage(func(value *PRStageRun) { value.Phase = "bad" })},
		{"stage state", mutateV20Stage(func(value *PRStageRun) { value.State = "bad" })},
		{"stage kind", mutateV20Stage(func(value *PRStageRun) { value.Kind = "" })},
		{"stage version", mutateV20Stage(func(value *PRStageRun) { value.WorkspaceVersion = 0 })},
		{"stage evidence", mutateV20Stage(func(value *PRStageRun) { value.Evidence = json.RawMessage(`{`) })},
		{"finding disposition", mutateV20Finding(func(value *PRFinding) { value.Disposition = "bad" })},
		{"finding distance", mutateV20Finding(func(value *PRFinding) { value.ScopeDistance = "bad" })},
		{"finding size", mutateV20Finding(func(value *PRFinding) { value.ChangeSize = "bad" })},
		{"finding scope", mutateV20Finding(func(value *PRFinding) { value.ScopePresence = "bad" })},
		{"finding origin", mutateV20Finding(func(value *PRFinding) { value.Origin = "" })},
		{"finding confidence", mutateV20Finding(func(value *PRFinding) { value.ClassificationConf = 1.1 })},
		{"finding version", mutateV20Finding(func(value *PRFinding) { value.Version = 0 })},
		{"finding policy mode", mutateV20Finding(func(value *PRFinding) { value.ScopePolicyMode = "bad" })},
		{"finding policy revision", mutateV20Finding(func(value *PRFinding) { value.ScopePolicyRevision = "bad\x00" })},
		{"finding reward", mutateV20Finding(func(value *PRFinding) { value.NudgeReward = &negativeReward })},
		{"finding reward source", mutateV20Finding(func(value *PRFinding) {
			reward := 0.5
			value.NudgeReward = &reward
		})},
		{"finding estimate", mutateV20Finding(func(value *PRFinding) { value.EstimatedMetrics = negativeMetrics })},
		{"finding actual", mutateV20Finding(func(value *PRFinding) { value.ActualMetrics = &negativeMetrics })},
		{"finding event finding", mutateV20FindingEvent(func(value *PRFindingEvent) { value.FindingID = "" })},
		{"finding event kind", mutateV20FindingEvent(func(value *PRFindingEvent) { value.Kind = "" })},
		{"finding event actor", mutateV20FindingEvent(func(value *PRFindingEvent) { value.Actor = "" })},
		{
			"finding event before",
			mutateV20FindingEvent(func(value *PRFindingEvent) { value.Before = json.RawMessage(`{`) }),
		},
		{
			"finding event after",
			mutateV20FindingEvent(func(value *PRFindingEvent) { value.After = json.RawMessage(`{`) }),
		},
		{"conversation channel", mutateV20Conversation(func(value *PRConversation) { value.Channel = "" })},
		{"conversation phase", mutateV20Conversation(func(value *PRConversation) { value.Phase = "bad" })},
		{"conversation status", mutateV20Conversation(func(value *PRConversation) { value.Status = "bad" })},
		{"message role", mutateV20Message(func(value *PRMessage) { value.Role = "bad" })},
		{"message phase", mutateV20Message(func(value *PRMessage) { value.Phase = "bad" })},
		{"message kind", mutateV20Message(func(value *PRMessage) { value.Kind = "" })},
		{"message content", mutateV20Message(func(value *PRMessage) { value.Content = "" })},
		{"message charter", mutateV20Message(func(value *PRMessage) { value.CharterID = "bad\x00" })},
		{"correction status", mutateV20Correction(func(value *PRCorrection) { value.Status = "bad" })},
		{"correction applies", mutateV20Correction(func(value *PRCorrection) { value.AppliesToReview = false })},
		{"correction kind", mutateV20Correction(func(value *PRCorrection) { value.Kind = "" })},
		{"lesson status", mutateV20Lesson(func(value *PRRepositoryLesson) { value.Status = "bad" })},
		{"lesson repository", mutateV20Lesson(func(value *PRRepositoryLesson) { value.RepositoryID = "" })},
		{"lesson type", mutateV20Lesson(func(value *PRRepositoryLesson) { value.ApplicableTypes = []PRType{"bad"} })},
		{
			"lesson phase",
			mutateV20Lesson(func(value *PRRepositoryLesson) { value.ApplicablePhases = []PRWorkspacePhase{"bad"} }),
		},
		{"nudge state", mutateV20Nudge(func(value *PRNudgeRound) { value.State = "bad" })},
		{"nudge phase", mutateV20Nudge(func(value *PRNudgeRound) { value.Phase = PRWorkspacePlanning })},
		{"nudge budget", mutateV20Nudge(func(value *PRNudgeRound) { value.Round = 0 })},
		{"nudge stage", mutateV20Nudge(func(value *PRNudgeRound) { value.StageRunID = "" })},
		{"nudge candidates", mutateV20Nudge(func(value *PRNudgeRound) { value.NovelCount = 1 })},
		{"nudge resolved", mutateV20Nudge(func(value *PRNudgeRound) { value.ResolvedFindings = 1 })},
		{"nudge reward range", mutateV20Nudge(func(value *PRNudgeRound) { value.Reward = &negativeReward })},
		{"reward round", mutateV20Reward(func(value *PRNudgeReward) { value.NudgeRoundID = "" })},
		{"reward value", mutateV20Reward(func(value *PRNudgeReward) { value.Reward = -1 })},
		{"reward outcome", mutateV20Reward(func(value *PRNudgeReward) { value.Outcome = "" })},
		{"reward provenance", mutateV20Reward(func(value *PRNudgeReward) { value.Provenance = "" })},
		{"group status", mutateV20Group(func(value *PRDeferredGroup) { value.Status = "bad" })},
		{"group grade", mutateV20Group(func(value *PRDeferredGroup) { value.ScopeDistance = "bad" })},
		{"group revision", mutateV20Group(func(value *PRDeferredGroup) { value.DraftRevision = 0 })},
		{"group version", mutateV20Group(func(value *PRDeferredGroup) { value.Version = 0 })},
		{"group assessment", mutateV20Group(func(value *PRDeferredGroup) { value.ScopeConfidence = 2 })},
		{"group projection", mutateV20Group(func(value *PRDeferredGroup) { value.ScopePresence = "bad" })},
		{"group title", mutateV20Group(func(value *PRDeferredGroup) { value.Title = "" })},
		{"group body", mutateV20Group(func(value *PRDeferredGroup) { value.Body = "" })},
		{"group labels", mutateV20Group(func(value *PRDeferredGroup) { value.Labels = []string{""} })},
		{
			"group suppression missing",
			mutateV20Group(func(value *PRDeferredGroup) { value.PublicationSuppressed = true }),
		},
		{
			"group suppression unexpected",
			mutateV20Group(func(value *PRDeferredGroup) { value.SuppressionReason = "reason" }),
		},
		{"item finding", mutateV20Item(func(value *PRDeferredGroupItem) { value.FindingID = "" })},
		{"item removed position", mutateV20Item(func(value *PRDeferredGroupItem) { value.Removed = true })},
		{"item group", mutateV20Item(func(value *PRDeferredGroupItem) { value.GroupID = "" })},
		{"item ordinal", mutateV20Item(func(value *PRDeferredGroupItem) { value.OrdinalInGroup = -1 })},
		{"repair state", mutateV20Repair(func(value *PRRepairAttempt) { value.State = "bad" })},
		{"repair attempt", mutateV20Repair(func(value *PRRepairAttempt) { value.Attempt = 0 })},
		{"repair stage", mutateV20Repair(func(value *PRRepairAttempt) { value.StageRunID = "" })},
		{"repair metrics", mutateV20Repair(func(value *PRRepairAttempt) { value.Metrics = negativeMetrics })},
		{"repair distance", mutateV20Repair(func(value *PRRepairAttempt) { value.ScopeDistance = "bad" })},
		{"repair size", mutateV20Repair(func(value *PRRepairAttempt) { value.ScopeChangeSize = "bad" })},
		{"repair confidence", mutateV20Repair(func(value *PRRepairAttempt) { value.ScopeConfidence = 2 })},
		{"repair projection", mutateV20Repair(func(value *PRRepairAttempt) { value.ScopePresence = "bad" })},
		{"repair files", mutateV20Repair(func(value *PRRepairAttempt) { value.ChangedFiles = []string{""} })},
		{"repair finding bound", mutateV20Repair(func(value *PRRepairAttempt) { value.FindingIDs = tooManyStrings })},
		{"repair fence versions", mutateV20Repair(func(value *PRRepairAttempt) {
			value.PublicationFence = &PRImplementationPublicationFence{}
		})},
		{"repair fence identity", mutateV20Repair(func(value *PRRepairAttempt) {
			value.PublicationFence = validV20PublicationFence()
			value.PublicationFence.Tree = ""
		})},
		{"validation state", mutateV20Validation(func(value *PRValidationRun) { value.State = "bad" })},
		{"validation stage", mutateV20Validation(func(value *PRValidationRun) { value.StageRunID = "" })},
		{"validation kind", mutateV20Validation(func(value *PRValidationRun) { value.Kind = "" })},
		{"validation check bound", mutateV20Validation(func(value *PRValidationRun) { value.Checks = tooManyChecks })},
		{"validation check id", mutateV20Validation(func(value *PRValidationRun) {
			value.Checks = []PRValidationCheck{{Name: "test", Status: "passed"}}
		})},
		{"validation check name", mutateV20Validation(func(value *PRValidationRun) {
			value.Checks = []PRValidationCheck{{ID: "test", Status: "passed"}}
		})},
		{"validation check status", mutateV20Validation(func(value *PRValidationRun) {
			value.Checks = []PRValidationCheck{{ID: "test", Name: "test"}}
		})},
		{"validation duration", mutateV20Validation(func(value *PRValidationRun) {
			value.Checks = []PRValidationCheck{{ID: "test", Name: "test", Status: "passed", DurationMS: -1}}
		})},
		{"gate state", mutateV20Gate(func(value *PRGateRun) { value.State = "bad" })},
		{"gate decision", mutateV20Gate(func(value *PRGateRun) { value.DecisionPoint = "" })},
		{"gate policy", mutateV20Gate(func(value *PRGateRun) { value.PinnedPolicy = json.RawMessage(`{`) })},
		{"gate subject", mutateV20Gate(func(value *PRGateRun) { value.PinnedSubject = json.RawMessage(`{`) })},
		{"gate evidence", mutateV20Gate(func(value *PRGateRun) { value.Evidence = json.RawMessage(`{`) })},
		{"gate turn bound", mutateV20Gate(func(value *PRGateRun) { value.Turns = tooManyTurns })},
		{"gate turn invalid", mutateV20Gate(func(value *PRGateRun) { value.Turns = []PRGateTurn{{}} })},
		{"publication kind", mutateV20Publication(func(value *PRPublication) { value.Kind = "bad" })},
		{"publication status", mutateV20Publication(func(value *PRPublication) { value.Status = "bad" })},
		{"publication execution", mutateV20Publication(func(value *PRPublication) { value.ExecutionState = "bad" })},
		{
			"publication finding bound",
			mutateV20Publication(func(value *PRPublication) { value.FindingIDs = tooManyStrings }),
		},
		{"publication digest required", mutateV20Publication(func(value *PRPublication) { value.RequestDigest = "" })},
		{
			"publication request",
			mutateV20Publication(func(value *PRPublication) { value.Request = json.RawMessage(`{`) }),
		},
		{
			"publication digest mismatch",
			mutateV20Publication(func(value *PRPublication) { value.PayloadDigest = "wrong" }),
		},
		{"publication attempts", mutateV20Publication(func(value *PRPublication) { value.Attempts = -1 })},
		{"publication available", mutateV20Publication(func(value *PRPublication) { value.AvailableAt = time.Time{} })},
		{"publication lease", mutateV20Publication(func(value *PRPublication) { value.LeaseOwner = "unexpected" })},
		{"operation state", mutateV20Operation(func(value *PRWorkspaceOperationIntent) { value.State = "bad" })},
		{"operation kind", mutateV20Operation(func(value *PRWorkspaceOperationIntent) { value.Kind = "" })},
		{
			"operation version",
			mutateV20Operation(func(value *PRWorkspaceOperationIntent) { value.InputWorkspaceVersion = 0 }),
		},
		{"operation digest", mutateV20Operation(func(value *PRWorkspaceOperationIntent) { value.InputDigest = "" })},
		{
			"operation input",
			mutateV20Operation(func(value *PRWorkspaceOperationIntent) { value.Input = json.RawMessage(`{`) }),
		},
		{
			"operation result",
			mutateV20Operation(func(value *PRWorkspaceOperationIntent) { value.Result = json.RawMessage(`{`) }),
		},
		{"operation attempts", mutateV20Operation(func(value *PRWorkspaceOperationIntent) { value.Attempts = -1 })},
		{
			"operation available",
			mutateV20Operation(func(value *PRWorkspaceOperationIntent) { value.AvailableAt = time.Time{} }),
		},
		{
			"operation lease",
			mutateV20Operation(func(value *PRWorkspaceOperationIntent) { value.LeaseOwner = "unexpected" }),
		},
		{"watermark source", mutateV20Watermark(func(value *PRIngressWatermark) { value.Source = "" })},
		{"watermark connector", mutateV20Watermark(func(value *PRIngressWatermark) { value.Connector = "" })},
		{"watermark event", mutateV20Watermark(func(value *PRIngressWatermark) { value.InboxEventID = "" })},
		{"watermark time", mutateV20Watermark(func(value *PRIngressWatermark) { value.InboxReceivedAt = time.Time{} })},
		{"activity kind", mutateV20Activity(func(value *PRActivity) { value.Kind = "" })},
		{"activity actor", mutateV20Activity(func(value *PRActivity) { value.Actor = "" })},
		{"activity summary", mutateV20Activity(func(value *PRActivity) { value.Summary = "" })},
		{"activity entity", mutateV20Activity(func(value *PRActivity) { value.EntityID = "bad\x00" })},
		{"activity metadata bound", mutateV20Activity(func(value *PRActivity) {
			value.Metadata = make(map[string]any, 129)
			for index := range 129 {
				value.Metadata[fmt.Sprintf("key-%d", index)] = index
			}
		})},
		{"activity metadata JSON", mutateV20Activity(func(value *PRActivity) {
			value.Metadata = map[string]any{"bad": func() {}}
		})},
		{"negative metadata ordinal", mutateV20Activity(func(value *PRActivity) { value.Ordinal = -1 })},
		{"long metadata id", mutateV20Activity(func(value *PRActivity) { value.ID = strings.Repeat("x", 129) })},
		{"bad metadata workspace", mutateV20Activity(func(value *PRActivity) { value.WorkspaceID = "bad" })},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			require.ErrorIs(t, validatePRWorkspaceRecord(testCase.value), ErrInvalidPRWorkspace)
		})
	}

	for name, validation := range map[string]func() error{
		"scope change bound": func() error { return validatePRScopeProjection("", tooManyChanges) },
		"scope path": func() error {
			return validatePRScopeProjection("", []PRScopeChange{{}})
		},
		"scope hunk": func() error {
			return validatePRScopeProjection("", []PRScopeChange{validV20ScopeChange(func(value *PRScopeChange) {
				value.Hunk = "bad\x00"
			})})
		},
		"scope module": func() error {
			return validatePRScopeProjection("", []PRScopeChange{validV20ScopeChange(func(value *PRScopeChange) {
				value.Module = "bad\x00"
			})})
		},
		"scope metrics": func() error {
			return validatePRScopeProjection("", []PRScopeChange{validV20ScopeChange(func(value *PRScopeChange) {
				value.SemanticLines = -1
			})})
		},
		"scope classification": func() error {
			return validatePRScopeProjection("", []PRScopeChange{validV20ScopeChange(func(value *PRScopeChange) {
				value.ChangeSize = "bad"
			})})
		},
		"scope clauses": func() error {
			return validatePRScopeProjection("", []PRScopeChange{validV20ScopeChange(func(value *PRScopeChange) {
				value.CharterClauses = []string{""}
			})})
		},
		"scope explanation": func() error {
			return validatePRScopeProjection("", []PRScopeChange{validV20ScopeChange(func(value *PRScopeChange) {
				value.Explanation = "bad\x00"
			})})
		},
		"gate actor": func() error {
			return validatePRGateTurn(PRGateTurn{StageID: "stage", Kind: "human", Status: "waiting", ActorKind: "bad"})
		},
		"gate form": func() error {
			return validatePRGateTurn(PRGateTurn{StageID: "stage", Kind: "human", Status: "waiting", GateForm: json.RawMessage(`{`)})
		},
		"gate values": func() error {
			return validatePRGateTurn(PRGateTurn{
				StageID: "stage", Kind: "human", Status: "waiting", FieldValues: map[string]any{"bad": func() {}},
			})
		},
		"claimed owner": func() error { return validatePRWorkspaceLease("", "", nil, true) },
		"claimed token": func() error { return validatePRWorkspaceLease("worker", "bad", nil, true) },
		"claimed deadline": func() error {
			return validatePRWorkspaceLease("worker", "plt_40000000000000000000000000000001", nil, true)
		},
		"unclaimed lease": func() error { return validatePRWorkspaceLease("worker", "", nil, false) },
		"string text":     func() error { return validatePRWorkspaceString("field", "bad\x00", 10, false) },
		"string required": func() error { return validatePRWorkspaceString("field", "", 10, true) },
		"string bound":    func() error { return validatePRWorkspaceString("field", "long", 2, false) },
		"list required":   func() error { return validatePRWorkspaceStringList("field", nil, true) },
		"list bound":      func() error { return validatePRWorkspaceStringList("field", tooManyStrings, false) },
		"raw JSON":        func() error { return validatePRWorkspaceRaw("field", json.RawMessage(`{`)) },
		"raw duplicate":   func() error { return validatePRWorkspaceRaw("field", json.RawMessage(`{"x":1,"X":2}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, validation(), ErrInvalidPRWorkspace)
		})
	}
}

func mutateV20Charter(mutate func(*PRCharterRevision)) *PRCharterRevision {
	value := &PRCharterRevision{
		Status: PRRecordDraft, Revision: 1, Type: PRTypeFeature, Goal: "Goal",
		AcceptanceCriteria: []string{"Criterion"}, BaseSHA: "base", HeadSHA: "head", CreatedBy: "user",
	}
	mutate(value)
	return value
}

func mutateV20Stage(mutate func(*PRStageRun)) *PRStageRun {
	value := &PRStageRun{
		Phase:            PRWorkspacePlanning,
		Kind:             "planning",
		State:            PRExecutionRunning,
		Attempt:          1,
		WorkspaceVersion: 1,
	}
	mutate(value)
	return value
}

func mutateV20Finding(mutate func(*PRFinding)) *PRFinding {
	value := &PRFinding{
		Origin: "review", Fingerprint: "fingerprint", Severity: "medium", Title: "Title", Message: "Message",
		Disposition: PRFindingOpen, ScopeDistance: PRScopeExact, ChangeSize: PRChangeSizeS,
		ClassificationConf: .5, Version: 1,
	}
	mutate(value)
	return value
}

func mutateV20FindingEvent(mutate func(*PRFindingEvent)) *PRFindingEvent {
	value := &PRFindingEvent{FindingID: "finding", Kind: "classified", Actor: "ai"}
	mutate(value)
	return value
}

func mutateV20Conversation(mutate func(*PRConversation)) *PRConversation {
	value := &PRConversation{Channel: "workspace", Phase: PRWorkspacePlanning, Status: PRRecordActive}
	mutate(value)
	return value
}

func mutateV20Message(mutate func(*PRMessage)) *PRMessage {
	value := &PRMessage{Role: "user", Phase: PRWorkspacePlanning, Kind: "steer", Content: "Continue"}
	mutate(value)
	return value
}

func mutateV20Correction(mutate func(*PRCorrection)) *PRCorrection {
	value := &PRCorrection{
		Status: PRRecordActive, Kind: "scope", TargetKind: "workspace",
		OriginalClaim: "Before", Correction: "After", AppliesToReview: true,
	}
	mutate(value)
	return value
}

func mutateV20Lesson(mutate func(*PRRepositoryLesson)) *PRRepositoryLesson {
	value := &PRRepositoryLesson{
		Status: PRRecordActive, RepositoryID: "repository", Kind: "scope", Content: "Lesson",
		SourceCorrectionID: "correction", ConfirmedBy: "user",
	}
	mutate(value)
	return value
}

func mutateV20Nudge(mutate func(*PRNudgeRound)) *PRNudgeRound {
	value := &PRNudgeRound{
		StageRunID: "stage", Phase: PRWorkspaceReview, State: PRExecutionRunning,
		Round: 1, MinimumRounds: 0, HardCap: 2, StrategyFamily: "coverage",
		CoverageTarget: "target", ChallengeDigest: "challenge", PromptDigest: "prompt",
	}
	mutate(value)
	return value
}

func mutateV20Reward(mutate func(*PRNudgeReward)) *PRNudgeReward {
	value := &PRNudgeReward{NudgeRoundID: "round", Reward: .5, Outcome: "fixed", Provenance: "validation"}
	mutate(value)
	return value
}

func mutateV20Group(mutate func(*PRDeferredGroup)) *PRDeferredGroup {
	value := &PRDeferredGroup{
		Status: PRRecordDraft, ScopeDistance: PRScopeRelatedFollowup, ChangeSize: PRChangeSizeS,
		DraftRevision: 1, Version: 1, Title: "Title", Body: "Body",
	}
	mutate(value)
	return value
}

func mutateV20Item(mutate func(*PRDeferredGroupItem)) *PRDeferredGroupItem {
	value := &PRDeferredGroupItem{FindingID: "finding", GroupID: "group", OrdinalInGroup: 0}
	mutate(value)
	return value
}

func mutateV20Repair(mutate func(*PRRepairAttempt)) *PRRepairAttempt {
	value := &PRRepairAttempt{
		StageRunID: "stage", State: PRExecutionRunning, Attempt: 1, GoalDigest: "goal", BaseCommit: "base",
	}
	mutate(value)
	return value
}

func mutateV20Validation(mutate func(*PRValidationRun)) *PRValidationRun {
	value := &PRValidationRun{StageRunID: "stage", State: PRExecutionRunning, Kind: "local_ci"}
	mutate(value)
	return value
}

func mutateV20Gate(mutate func(*PRGateRun)) *PRGateRun {
	value := &PRGateRun{
		DecisionPoint: "publish", State: PRExecutionWaitingUser, PolicyRevision: "policy",
		WorkflowRef: "workflow", WorkflowRevision: "revision", GateRef: "gate",
		WorkflowConfigurationID: "default", WorkflowConfigurationRevision: "config",
		PinnedPolicy: json.RawMessage(`{}`), PinnedPolicyHash: "policy-hash",
		SubjectRevision: "subject", PinnedSubject: json.RawMessage(`{}`), PinnedSubjectHash: "subject-hash",
	}
	mutate(value)
	return value
}

func mutateV20Publication(mutate func(*PRPublication)) *PRPublication {
	request := json.RawMessage(`{"title":"Issue"}`)
	digest := testPRWorkspacePayloadDigest(request)
	value := &PRPublication{
		Kind: PRPublicationGitHubIssue, Status: PRPublicationPending, Request: request,
		RequestDigest: digest, PayloadDigest: digest, AvailableAt: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}
	mutate(value)
	return value
}

func mutateV20Operation(mutate func(*PRWorkspaceOperationIntent)) *PRWorkspaceOperationIntent {
	value := &PRWorkspaceOperationIntent{
		Kind: "planning", State: PRExecutionQueued, InputWorkspaceVersion: 1,
		InputDigest: "digest", Input: json.RawMessage(`{}`),
		AvailableAt: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}
	mutate(value)
	return value
}

func mutateV20Watermark(mutate func(*PRIngressWatermark)) *PRIngressWatermark {
	value := &PRIngressWatermark{
		Source: "github", Connector: "primary", InboxEventID: "event",
		InboxReceivedAt: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}
	mutate(value)
	return value
}

func mutateV20Activity(mutate func(*PRActivity)) *PRActivity {
	value := &PRActivity{Kind: "updated", Actor: "user", Summary: "Updated lifecycle"}
	mutate(value)
	return value
}

func validV20PublicationFence() *PRImplementationPublicationFence {
	return &PRImplementationPublicationFence{
		GitWorkspaceID: "workspace", LineID: "line", LineVersion: 1, MutationEpoch: 1,
		ParkIntentID: "park", BaseCommit: "base", Tip: "tip", Tree: "tree",
	}
}

func validV20ScopeChange(mutate func(*PRScopeChange)) PRScopeChange {
	value := PRScopeChange{
		Path: "pkg/example.go", SemanticLines: 1, Presence: PRWorkCandidatePresent,
		ScopeDistance: PRScopeExact, ChangeSize: PRChangeSizeS, Confidence: .8,
	}
	mutate(&value)
	return value
}

func v20NotificationDraft(
	aggregate PRWorkspaceAggregate,
	id string,
	sourceKey string,
) developmentnotifications.Draft {
	return developmentnotifications.Draft{
		ID: id, SourceKey: sourceKey, Generation: 1,
		WorkspaceID: aggregate.Workspace.ID, Repository: aggregate.Workspace.Repository,
		Intent: developmentnotifications.IntentPickupPR, SourceKind: developmentnotifications.SourcePullRequest,
		Phase: "publication", Reason: developmentnotifications.ReasonProviderOutcomeUnknown,
		Title: "Provider outcome unknown", Summary: "Inspect provider state before retrying",
		Target: developmentnotifications.Target{Panel: "publication"},
	}
}
