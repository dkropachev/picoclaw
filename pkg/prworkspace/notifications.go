package prworkspace

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
	"github.com/sipeed/picoclaw/pkg/eventing"
)

type developmentNotificationStore interface {
	UpsertDevelopmentNotification(
		ctx context.Context,
		draft developmentnotifications.Draft,
	) (developmentnotifications.UpsertResult, error)
	ListDevelopmentNotifications(ctx context.Context) ([]developmentnotifications.Notification, error)
	ListRecentDevelopmentPushNotifications(
		ctx context.Context,
		limit int,
	) ([]developmentnotifications.Notification, error)
	GetDevelopmentNotification(ctx context.Context, id string) (developmentnotifications.Notification, error)
	MutateDevelopmentNotification(
		ctx context.Context,
		id string,
		expectedVersion uint64,
		action string,
		snoozedUntil *time.Time,
	) (developmentnotifications.Notification, error)
	MutateDevelopmentNotifications(
		ctx context.Context,
		request eventing.DevelopmentNotificationBulkMutation,
	) (eventing.DevelopmentNotificationBulkMutationResult, error)
	GetDevelopmentNotificationViews(ctx context.Context) (eventing.DevelopmentNotificationViewsDocument, error)
	PutDevelopmentNotificationViews(
		ctx context.Context,
		views []developmentnotifications.SavedView,
		expectedVersion uint64,
	) (eventing.DevelopmentNotificationViewsDocument, error)
}

func (store *EventingStore) developmentNotifications() developmentNotificationStore {
	if store == nil {
		return nil
	}
	backend, _ := store.store.(developmentNotificationStore)
	return backend
}

func syncDevelopmentNotifications(ctx context.Context, store Store, aggregate Aggregate) {
	eventStore, ok := store.(*publicationFencedStore)
	if ok {
		store = eventStore.Store
	}
	adapter, ok := store.(*EventingStore)
	if !ok || adapter.developmentNotifications() == nil || aggregate.Workspace.ID == "" {
		return
	}
	backend := adapter.developmentNotifications()
	// The eventing transaction already reconciled all lifecycle state. This
	// post-commit hook is intentionally delivery-only: it cannot leave the
	// workspace and inbox out of sync. Reading by deterministic ID also keeps
	// its work proportional to this aggregate's active exceptions, not to the
	// size of a piled-up inbox.
	for _, draft := range notificationDrafts(aggregate) {
		item, err := backend.GetDevelopmentNotification(ctx, draft.ID)
		if err == nil {
			deliverDevelopmentPush(ctx, backend, item)
		}
	}
}

func notificationDrafts(aggregate Aggregate) []developmentnotifications.Draft {
	workspace := aggregate.Workspace
	intent := developmentnotifications.Intent(workspace.Intent)
	source := developmentnotifications.SourceKind(workspace.SourceKind)
	base := func(reason developmentnotifications.Reason, entity, title, summary, panel string, generation uint64) developmentnotifications.Draft {
		sourceKey := strings.Join([]string{workspace.ID, string(reason), entity}, ":")
		if generation == 0 {
			generation = 1
		}
		return developmentnotifications.Draft{
			ID: stableID("dnt_", sourceKey), SourceKey: sourceKey, Generation: generation,
			WorkspaceID: workspace.ID, Repository: workspace.Repository,
			Intent: intent, SourceKind: source, Phase: string(workspace.Phase), Reason: reason,
			Title: title, Summary: summary,
			Target: developmentnotifications.Target{Panel: panel, EntityID: entity},
		}
	}
	var drafts []developmentnotifications.Draft
	var latestCharter *Charter
	for index := range aggregate.Charters {
		charter := &aggregate.Charters[index]
		if latestCharter == nil || charter.Revision > latestCharter.Revision {
			latestCharter = charter
		}
	}
	if latestCharter != nil && !latestCharter.Confirmed && latestCharter.ClarificationNeeded {
		drafts = append(drafts, base(
			developmentnotifications.ReasonCharterAmbiguity, latestCharter.ID,
			"Feature clarification needed", latestCharter.ClarificationQuestion, "charter",
			uint64(latestCharter.Revision),
		))
	}
	for _, gate := range aggregate.Gates {
		if gate.State != ExecutionWaitingUser {
			continue
		}
		reason := developmentnotifications.ReasonScopeException
		title, panel := "Scope decision needed", "scope"
		switch {
		case strings.Contains(gate.DecisionPoint, "charter"):
			reason, title, panel = developmentnotifications.ReasonCharterAmbiguity, "Charter decision needed", "charter"
		case strings.Contains(gate.DecisionPoint, "publish"):
			reason, title, panel = developmentnotifications.ReasonPublicationApproval, "Publication approval needed", "publication"
		case strings.Contains(gate.DecisionPoint, "reconcile"):
			reason, title, panel = developmentnotifications.ReasonProviderOutcomeUnknown, "Provider outcome needs reconciliation", "publication"
		}
		drafts = append(drafts, base(reason, gate.ID, title, gate.DecisionPoint, panel, uint64(len(gate.Turns)+1)))
	}
	for _, publication := range aggregate.Publications {
		if publication.State == ExecutionUnknown {
			drafts = append(drafts, base(
				developmentnotifications.ReasonProviderOutcomeUnknown,
				publication.ID,
				"Provider outcome is unknown",
				"Check provider state before retrying.",
				"publication",
				uint64(publication.Attempts),
			))
		}
	}
	if workspace.ExecutionState == ExecutionFailed || workspace.ExecutionState == ExecutionBlocked {
		generation := uint64(1)
		if len(aggregate.StageRuns) > 0 {
			generation = uint64(aggregate.StageRuns[len(aggregate.StageRuns)-1].Attempt)
		}
		drafts = append(drafts, base(
			developmentnotifications.ReasonImplementationBlocked,
			workspace.ID,
			"Implementation is blocked",
			"Review the latest failed stage and validation evidence.",
			"overview",
			generation,
		))
	}
	for messageIndex, message := range aggregate.Messages {
		if message.Mode == "steer" && message.Status == "needs_clarification" &&
			(message.CharterID == "" || message.CharterID == aggregate.Workspace.ActiveCharterID) {
			superseded := false
			for _, later := range aggregate.Messages[messageIndex+1:] {
				if later.Mode == "steer" {
					superseded = true
					break
				}
			}
			if superseded {
				continue
			}
			drafts = append(drafts, base(
				developmentnotifications.ReasonSteeringScopeChange,
				message.ID,
				"Steering would change scope",
				"Send narrower steering or start a new feature with the expanded scope.",
				"chat",
				1,
			))
		}
	}
	sort.Slice(drafts, func(i, j int) bool { return drafts[i].SourceKey < drafts[j].SourceKey })
	return drafts
}

func (service *Service) ListNotifications(
	ctx context.Context, rawQuery, cursor string, limit int,
) (developmentnotifications.Page, error) {
	backend := service.notificationBackend()
	if backend == nil {
		return developmentnotifications.Page{}, fmt.Errorf("notifications are unavailable")
	}
	query, err := developmentnotifications.ParseQuery(rawQuery)
	if err != nil {
		return developmentnotifications.Page{}, err
	}
	all, err := backend.ListDevelopmentNotifications(ctx)
	if err != nil {
		return developmentnotifications.Page{}, err
	}
	if limit == 0 {
		limit = 50
	}
	return developmentnotifications.PageNotifications(all, query, cursor, limit, service.now().UTC())
}

func (service *Service) GetNotification(ctx context.Context, id string) (developmentnotifications.Notification, error) {
	backend := service.notificationBackend()
	if backend == nil {
		return developmentnotifications.Notification{}, fmt.Errorf("notifications are unavailable")
	}
	return backend.GetDevelopmentNotification(ctx, id)
}

type NotificationNeighbors struct {
	PreviousID string `json:"previous_id,omitempty"`
	NextID     string `json:"next_id,omitempty"`
}

type NotificationCounts struct {
	Open    int `json:"open"`
	Unread  int `json:"unread"`
	Snoozed int `json:"snoozed"`
}

func (service *Service) NotificationCounts(ctx context.Context) (NotificationCounts, error) {
	backend := service.notificationBackend()
	if backend == nil {
		return NotificationCounts{}, fmt.Errorf("notifications are unavailable")
	}
	all, err := backend.ListDevelopmentNotifications(ctx)
	if err != nil {
		return NotificationCounts{}, err
	}
	now := service.now().UTC()
	counts := NotificationCounts{}
	for _, item := range all {
		if item.Status == developmentnotifications.StatusOpen {
			counts.Open++
			if !item.Read {
				counts.Unread++
			}
			if item.IsSnoozed(now) {
				counts.Snoozed++
			}
		}
	}
	return counts, nil
}

func (service *Service) NotificationNeighbors(
	ctx context.Context, id, rawQuery string,
) (NotificationNeighbors, error) {
	backend := service.notificationBackend()
	if backend == nil {
		return NotificationNeighbors{}, fmt.Errorf("notifications are unavailable")
	}
	query, err := developmentnotifications.ParseQuery(rawQuery)
	if err != nil {
		return NotificationNeighbors{}, err
	}
	all, err := backend.ListDevelopmentNotifications(ctx)
	if err != nil {
		return NotificationNeighbors{}, err
	}
	filtered := make([]developmentnotifications.Notification, 0, len(all))
	for _, item := range all {
		if query.Match(item, service.now().UTC()) {
			filtered = append(filtered, item)
		}
	}
	filtered, err = developmentnotifications.SortNotifications(filtered, query, service.now().UTC())
	if err != nil {
		return NotificationNeighbors{}, err
	}
	for index := range filtered {
		if filtered[index].ID != id {
			continue
		}
		neighbors := NotificationNeighbors{}
		if index > 0 {
			neighbors.PreviousID = filtered[index-1].ID
		}
		if index+1 < len(filtered) {
			neighbors.NextID = filtered[index+1].ID
		}
		return neighbors, nil
	}
	return NotificationNeighbors{}, ErrNotFound
}

func (service *Service) MutateNotification(
	ctx context.Context, id string, version uint64, action string, until *time.Time,
) (developmentnotifications.Notification, error) {
	backend := service.notificationBackend()
	if backend == nil {
		return developmentnotifications.Notification{}, fmt.Errorf("notifications are unavailable")
	}
	return backend.MutateDevelopmentNotification(ctx, id, version, action, until)
}

func (service *Service) MutateNotifications(
	ctx context.Context,
	requestID, action string,
	items []eventing.DevelopmentNotificationMutation,
	until *time.Time,
) (eventing.DevelopmentNotificationBulkMutationResult, error) {
	backend := service.notificationBackend()
	if backend == nil {
		return eventing.DevelopmentNotificationBulkMutationResult{}, fmt.Errorf("notifications are unavailable")
	}
	return backend.MutateDevelopmentNotifications(ctx, eventing.DevelopmentNotificationBulkMutation{
		RequestID: requestID, Action: action, Items: items, SnoozedUntil: until,
	})
}

func (service *Service) NotificationViews(ctx context.Context) (eventing.DevelopmentNotificationViewsDocument, error) {
	backend := service.notificationBackend()
	if backend == nil {
		return eventing.DevelopmentNotificationViewsDocument{}, fmt.Errorf("notifications are unavailable")
	}
	return backend.GetDevelopmentNotificationViews(ctx)
}

func (service *Service) PutNotificationViews(
	ctx context.Context, views []developmentnotifications.SavedView, version uint64,
) (eventing.DevelopmentNotificationViewsDocument, error) {
	backend := service.notificationBackend()
	if backend == nil {
		return eventing.DevelopmentNotificationViewsDocument{}, fmt.Errorf("notifications are unavailable")
	}
	return backend.PutDevelopmentNotificationViews(ctx, views, version)
}

func (service *Service) notificationBackend() developmentNotificationStore {
	if service == nil {
		return nil
	}
	store := service.store
	if fenced, ok := store.(*publicationFencedStore); ok {
		store = fenced.Store
	}
	adapter, _ := store.(*EventingStore)
	if adapter == nil {
		return nil
	}
	return adapter.developmentNotifications()
}
