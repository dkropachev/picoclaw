package prworkspace

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestNotificationDraftsUseLatestCharterClarificationAndIgnoreAutonomousGates(t *testing.T) {
	aggregate := Aggregate{
		Workspace: Workspace{
			ID: "devw_00000000000000000000000000000001", Intent: IntentImplementFeature,
			SourceKind: SourceIssue, Repository: "octo/project", Phase: PhaseCharter,
		},
		Charters: []Charter{
			{
				ID:                    "pcr_00000000000000000000000000000001",
				Revision:              1,
				ClarificationNeeded:   true,
				ClarificationQuestion: "Old question",
			},
			{
				ID:                    "pcr_00000000000000000000000000000002",
				Revision:              2,
				ClarificationNeeded:   true,
				ClarificationQuestion: "Current question",
			},
		},
		Gates: []GateRun{{
			ID: "pgr_00000000000000000000000000000001", State: ExecutionWaitingGate,
			DecisionPoint: "pr.scope.exception",
		}},
	}
	drafts := notificationDrafts(aggregate)
	require.Len(t, drafts, 1)
	require.Equal(t, developmentnotifications.ReasonCharterAmbiguity, drafts[0].Reason)
	require.Equal(t, aggregate.Charters[1].ID, drafts[0].Target.EntityID)
	require.Equal(t, "charter", drafts[0].Target.Panel)
	require.Equal(t, "Current question", drafts[0].Summary)
	aggregate.Charters = append(aggregate.Charters, Charter{
		ID: "pcr_00000000000000000000000000000003", Revision: 3,
		Goal: "Clarified charter",
	})
	require.Empty(t, notificationDrafts(aggregate),
		"a superseded ambiguous draft must not keep attention open")
}

func TestNotificationServiceFailsClosedWithoutDurableBackend(t *testing.T) {
	service, err := NewService(ServiceConfig{Store: NewMemoryStore()})
	require.NoError(t, err)
	ctx := context.Background()

	_, err = service.ListNotifications(ctx, "", "", 10)
	require.ErrorContains(t, err, "unavailable")
	_, err = service.GetNotification(ctx, "dnt_00000000000000000000000000000001")
	require.ErrorContains(t, err, "unavailable")
	_, err = service.NotificationCounts(ctx)
	require.ErrorContains(t, err, "unavailable")
	_, err = service.NotificationNeighbors(ctx, "dnt_00000000000000000000000000000001", "")
	require.ErrorContains(t, err, "unavailable")
	_, err = service.MutateNotification(
		ctx, "dnt_00000000000000000000000000000001", 1, "mark_read", nil,
	)
	require.ErrorContains(t, err, "unavailable")
	_, err = service.MutateNotifications(
		ctx,
		"notification-service-request-0001",
		"mark_read",
		[]eventing.DevelopmentNotificationMutation{{
			ID: "dnt_00000000000000000000000000000001", ExpectedVersion: 1,
		}},
		nil,
	)
	require.ErrorContains(t, err, "unavailable")
	_, err = service.NotificationViews(ctx)
	require.ErrorContains(t, err, "unavailable")
	_, err = service.PutNotificationViews(ctx, []developmentnotifications.SavedView{}, 1)
	require.ErrorContains(t, err, "unavailable")
}
