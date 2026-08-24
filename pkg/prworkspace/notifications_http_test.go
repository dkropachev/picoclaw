//go:build !mipsle && !netbsd && !(freebsd && arm)

package prworkspace

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
	"github.com/sipeed/picoclaw/pkg/eventing"
)

type notificationStackTestClock struct {
	now time.Time
}

func (clock *notificationStackTestClock) Now() time.Time {
	return clock.now
}

func (clock *notificationStackTestClock) Advance(duration time.Duration) {
	clock.now = clock.now.Add(duration)
}

type notificationStackFixture struct {
	store     *eventing.Store
	service   *Service
	handler   *HTTPHandler
	workspace Aggregate
	clock     *notificationStackTestClock
}

func newNotificationStackFixture(t *testing.T) *notificationStackFixture {
	t.Helper()
	clock := &notificationStackTestClock{
		now: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	}
	store, err := eventing.Open(
		t.Context(),
		filepath.Join(t.TempDir(), "notifications.sqlite"),
		eventing.WithClock(clock.Now),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	service, err := NewService(ServiceConfig{
		Store: NewEventingStore(store), Provider: developmentIntakeResolver{}, Now: clock.Now,
	})
	require.NoError(t, err)
	workspace, err := service.Create(t.Context(), CreateWorkspaceRequest{
		RequestID: "notification-stack-workspace-0001", Intent: IntentImplementFeature,
		SourceKind: SourceIssue, IssueURL: "https://github.com/octo/repo/issues/7",
	})
	require.NoError(t, err)
	handler, err := NewHTTPHandler(HTTPConfig{Service: service})
	require.NoError(t, err)
	return &notificationStackFixture{
		store: store, service: service, handler: handler, workspace: workspace, clock: clock,
	}
}

func (fixture *notificationStackFixture) seedNotification(
	t *testing.T,
	id string,
	reason developmentnotifications.Reason,
) developmentnotifications.Notification {
	t.Helper()
	result, err := fixture.store.UpsertDevelopmentNotification(t.Context(), developmentnotifications.Draft{
		ID: id, SourceKey: fixture.workspace.Workspace.ID + ":" + string(reason) + ":" + id,
		Generation: 1, WorkspaceID: fixture.workspace.Workspace.ID,
		Repository: fixture.workspace.Workspace.Repository,
		Intent:     developmentnotifications.IntentImplementFeature,
		SourceKind: developmentnotifications.SourceIssue,
		Phase:      "implementation", Reason: reason, Title: "Action needed", Summary: "Review this item",
		Target: developmentnotifications.Target{Panel: "overview", EntityID: fixture.workspace.Workspace.ID},
	})
	require.NoError(t, err)
	return result.Notification
}

func performNotificationStackRequest(
	t *testing.T,
	handler *HTTPHandler,
	method, path string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		require.NoError(t, err)
	}
	request := httptest.NewRequest(method, RuntimeRoutePrefix+path, bytes.NewReader(encoded))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeNotificationStackResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), target), response.Body.String())
}

func TestNotificationMutationRoutesRejectMissingRequestFence(t *testing.T) {
	store, err := eventing.Open(t.Context(), filepath.Join(t.TempDir(), "notifications.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	service, err := NewService(ServiceConfig{Store: NewEventingStore(store)})
	require.NoError(t, err)
	handler, err := NewHTTPHandler(HTTPConfig{Service: service})
	require.NoError(t, err)

	tests := []struct {
		method string
		path   string
		body   string
	}{
		{
			http.MethodPost,
			"/notifications/bulk",
			`{"request_id":"","action":"mark_read","items":[{"id":"dnt_00000000000000000000000000000001","expected_version":1}]}`,
		},
		{http.MethodPut, "/notification-views", `{"request_id":"","expected_version":1,"views":[]}`},
		{
			http.MethodPut,
			"/notification-settings",
			`{"request_id":"","expected_version":1,"include_repository_in_push":false}`,
		},
		{
			http.MethodPost,
			"/push-subscriptions",
			`{"request_id":"","name":"Phone","endpoint":"https://push.example.test/x","keys":{"auth":"YWJjZGVmZ2g","p256dh":"YWJjZGVmZ2g"}}`,
		},
		{
			http.MethodDelete,
			"/push-subscriptions/dps_00000000000000000000000000000001",
			`{"request_id":"","expected_version":1}`,
		},
	}
	for _, test := range tests {
		t.Run(test.method+test.path, func(t *testing.T) {
			request := httptest.NewRequest(
				test.method, RuntimeRoutePrefix+test.path, bytes.NewBufferString(test.body),
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), `"code":"invalid_request"`)
		})
	}

	request := httptest.NewRequest(
		http.MethodGet,
		RuntimeRoutePrefix+"/notifications/dnt_00000000000000000000000000000001/neighbors?extra=1",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, response.Body.String(), `"code":"invalid_query"`)
}

func TestNotificationHTTPSuccessLifecycle(t *testing.T) {
	fixture := newNotificationStackFixture(t)
	first := fixture.seedNotification(
		t,
		"dnt_00000000000000000000000000000011",
		developmentnotifications.ReasonProviderOutcomeUnknown,
	)
	fixture.clock.Advance(time.Minute)
	second := fixture.seedNotification(
		t,
		"dnt_00000000000000000000000000000012",
		developmentnotifications.ReasonImplementationBlocked,
	)

	query := "status = open ORDER BY updated ASC"
	listPath := "/notifications?" + url.Values{
		"query": {query}, "limit": {"1"},
	}.Encode()
	response := performNotificationStackRequest(t, fixture.handler, http.MethodGet, listPath, nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var firstPage struct {
		Notifications []developmentnotifications.Notification `json:"notifications"`
		NextCursor    string                                  `json:"next_cursor"`
		Counts        NotificationCounts                      `json:"counts"`
	}
	decodeNotificationStackResponse(t, response, &firstPage)
	require.Len(t, firstPage.Notifications, 1)
	require.Equal(t, first.ID, firstPage.Notifications[0].ID)
	require.NotEmpty(t, firstPage.NextCursor)
	require.Equal(t, NotificationCounts{Open: 2, Unread: 2}, firstPage.Counts)

	response = performNotificationStackRequest(
		t,
		fixture.handler,
		http.MethodGet,
		"/notifications?"+url.Values{
			"query": {query}, "limit": {"1"}, "cursor": {firstPage.NextCursor},
		}.Encode(),
		nil,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var secondPage struct {
		Notifications []developmentnotifications.Notification `json:"notifications"`
	}
	decodeNotificationStackResponse(t, response, &secondPage)
	require.Len(t, secondPage.Notifications, 1)
	require.Equal(t, second.ID, secondPage.Notifications[0].ID)

	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodGet, "/notifications/"+first.ID, nil,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var detail developmentnotifications.Notification
	decodeNotificationStackResponse(t, response, &detail)
	require.Equal(t, first, detail)

	response = performNotificationStackRequest(
		t,
		fixture.handler,
		http.MethodGet,
		"/notifications/"+first.ID+"/neighbors?"+url.Values{"query": {query}}.Encode(),
		nil,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var neighbors NotificationNeighbors
	decodeNotificationStackResponse(t, response, &neighbors)
	require.Empty(t, neighbors.PreviousID)
	require.Equal(t, second.ID, neighbors.NextID)

	first, err := fixture.service.MutateNotification(
		t.Context(), first.ID, first.Version, "mark_read", nil,
	)
	require.NoError(t, err)
	require.True(t, first.Read)
	bulkBody := map[string]any{
		"request_id": "notification-http-bulk-0001",
		"action":     "mark_read",
		"items": []map[string]any{
			{"id": first.ID, "expected_version": first.Version},
			{"id": second.ID, "expected_version": second.Version},
		},
	}
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPost, "/notifications/bulk", bulkBody,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var bulkResult struct {
		Notifications []developmentnotifications.Notification `json:"notifications"`
		Replayed      bool                                    `json:"replayed"`
	}
	decodeNotificationStackResponse(t, response, &bulkResult)
	require.False(t, bulkResult.Replayed)
	require.Len(t, bulkResult.Notifications, 2)
	require.True(t, bulkResult.Notifications[0].Read)
	require.True(t, bulkResult.Notifications[1].Read)

	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPost, "/notifications/bulk", bulkBody,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	decodeNotificationStackResponse(t, response, &bulkResult)
	require.True(t, bulkResult.Replayed)

	conflictBody := map[string]any{
		"request_id": "notification-http-bulk-0001", "action": "mark_unread",
		"items": []map[string]any{{"id": first.ID, "expected_version": first.Version}},
	}
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPost, "/notifications/bulk", conflictBody,
	)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())

	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodGet, "/notification-views", nil,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var views eventing.DevelopmentNotificationViewsDocument
	decodeNotificationStackResponse(t, response, &views)
	require.Equal(t, uint64(1), views.Version)
	require.Empty(t, views.Views)
	viewBody := map[string]any{
		"request_id": "notification-http-view-0001", "expected_version": views.Version,
		"views": []map[string]any{{
			"name": "Open work", "query": query, "pinned": true, "default": true, "position": 0,
		}},
	}
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPut, "/notification-views", viewBody,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	decodeNotificationStackResponse(t, response, &views)
	require.Equal(t, uint64(2), views.Version)
	require.Len(t, views.Views, 1)
	require.Regexp(t, `^dnv_[0-9a-f]{32}$`, views.Views[0].ID)
	viewBody = map[string]any{
		"request_id": "notification-http-view-0002", "expected_version": views.Version,
		"views": []map[string]any{{
			"id": views.Views[0].ID, "name": "Open development work", "query": query,
			"pinned": true, "default": true, "position": 0,
		}},
	}
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPut, "/notification-views", viewBody,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	decodeNotificationStackResponse(t, response, &views)
	require.Equal(t, uint64(3), views.Version)
	require.Equal(t, "Open development work", views.Views[0].Name)

	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodGet, "/notification-settings", nil,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var settings NotificationSettings
	decodeNotificationStackResponse(t, response, &settings)
	require.NotEmpty(t, settings.VAPIDPublicKey)
	require.Equal(t, uint64(2), settings.Version)
	response = performNotificationStackRequest(
		t,
		fixture.handler,
		http.MethodPut,
		"/notification-settings",
		map[string]any{
			"request_id": "notification-http-settings-0001", "expected_version": settings.Version,
			"include_repository_in_push": true,
		},
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	decodeNotificationStackResponse(t, response, &settings)
	require.True(t, settings.IncludeRepositoryInPush)
	require.Equal(t, uint64(3), settings.Version)

	receiver := newWebPushReceiver(t)
	createSubscription := map[string]any{
		"request_id": "notification-http-subscription-0001",
		"endpoint":   "https://push.example.test/device",
		"name":       "Phone",
		"keys":       map[string]any{"auth": receiver.auth, "p256dh": receiver.publicKey},
	}
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPost, "/push-subscriptions", createSubscription,
	)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var device PushSubscriptionDevice
	decodeNotificationStackResponse(t, response, &device)
	require.Regexp(t, `^dps_[0-9a-f]{32}$`, device.ID)
	require.Equal(t, "Phone", device.Name)
	require.True(t, device.Enabled)
	require.Equal(t, uint64(1), device.Version)
	require.NotContains(t, response.Body.String(), "push.example.test")
	require.NotContains(t, response.Body.String(), receiver.auth)

	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodGet, "/push-subscriptions", nil,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var devices struct {
		Subscriptions []PushSubscriptionDevice `json:"subscriptions"`
	}
	decodeNotificationStackResponse(t, response, &devices)
	require.Equal(t, []PushSubscriptionDevice{device}, devices.Subscriptions)

	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPut, "/push-subscriptions/"+device.ID, map[string]any{
			"request_id": "notification-http-subscription-0002", "expected_version": device.Version,
			"name": "Tablet", "enabled": false,
		},
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	decodeNotificationStackResponse(t, response, &device)
	require.Equal(t, "Tablet", device.Name)
	require.False(t, device.Enabled)
	require.Equal(t, uint64(2), device.Version)

	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodDelete, "/push-subscriptions/"+device.ID, map[string]any{
			"request_id": "notification-http-subscription-0003", "expected_version": device.Version,
		},
	)
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodGet, "/push-subscriptions", nil,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	decodeNotificationStackResponse(t, response, &devices)
	require.Empty(t, devices.Subscriptions)
}

func TestDeliverPendingDevelopmentPushPrunesAndSuppressesDurably(t *testing.T) {
	fixture := newNotificationStackFixture(t)
	notification := fixture.seedNotification(
		t,
		"dnt_00000000000000000000000000000021",
		developmentnotifications.ReasonProviderOutcomeUnknown,
	)
	receiver := newWebPushReceiver(t)
	requests := make(chan capturedWebPushRequest, 2)
	server := newWebPushTestServer(t, http.StatusCreated, requests)
	privateKey, publicKey := newVAPIDKeyPair(t)
	state := pushState{
		PrivateKey: privateKey, PublicKey: publicKey,
		Subscriptions: map[string]pushSubscription{
			"device": {
				ID: "device", Name: "Phone", Endpoint: server.URL,
				Auth: receiver.auth, P256DH: receiver.publicKey, Enabled: true, Version: 1,
				Delivered: map[string]uint64{"dnt_00000000000000000000000000000999": 1},
			},
		},
	}
	encoded, err := json.Marshal(state)
	require.NoError(t, err)
	_, err = fixture.store.PutDevelopmentPushState(t.Context(), encoded, 1)
	require.NoError(t, err)

	processed, err := fixture.service.DeliverPendingDevelopmentPush(t.Context(), 1)
	require.NoError(t, err)
	require.Equal(t, 2, processed, "one suppression compaction plus one delivery")
	request := <-requests
	payload := decryptWebPushPayload(t, receiver, request.body)
	require.Equal(t, notification.ID, payload["notification_id"])

	document, err := fixture.store.GetDevelopmentPushState(t.Context())
	require.NoError(t, err)
	persisted, err := decodePushState(document.State)
	require.NoError(t, err)
	require.Equal(t, notification.Generation, persisted.Subscriptions["device"].Delivered[notification.ID])
	require.NotContains(
		t,
		persisted.Subscriptions["device"].Delivered,
		"dnt_00000000000000000000000000000999",
	)

	processed, err = fixture.service.DeliverPendingDevelopmentPush(t.Context(), 1)
	require.NoError(t, err)
	require.Zero(t, processed)
	select {
	case duplicate := <-requests:
		t.Fatalf("duplicate Web Push request = %#v", duplicate)
	default:
	}
	_, err = fixture.service.DeliverPendingDevelopmentPush(t.Context(), 0)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = fixture.service.DeliverPendingDevelopmentPush(t.Context(), 21)
	require.ErrorIs(t, err, ErrInvalid)

	memoryService, err := NewService(ServiceConfig{Store: NewMemoryStore()})
	require.NoError(t, err)
	_, err = memoryService.DeliverPendingDevelopmentPush(t.Context(), 1)
	require.ErrorContains(t, err, "unavailable")

	corrupt, err := fixture.store.PutDevelopmentPushState(
		t.Context(), json.RawMessage(`{"subscriptions":[]}`), document.Version,
	)
	require.NoError(t, err)
	require.NotZero(t, corrupt.Version)
	_, err = fixture.service.DeliverPendingDevelopmentPush(t.Context(), 1)
	require.Error(t, err)
}

func TestNotificationHTTPRejectsInvalidQueriesMethodsAndStaleWrites(t *testing.T) {
	fixture := newNotificationStackFixture(t)
	notification := fixture.seedNotification(
		t,
		"dnt_00000000000000000000000000000041",
		developmentnotifications.ReasonScopeException,
	)

	tests := []struct {
		name   string
		method string
		path   string
		body   any
		status int
	}{
		{"unknown list query", http.MethodGet, "/notifications?extra=1", nil, http.StatusBadRequest},
		{"invalid list limit", http.MethodGet, "/notifications?limit=lots", nil, http.StatusBadRequest},
		{"invalid list expression", http.MethodGet, "/notifications?query=%28", nil, http.StatusBadRequest},
		{
			"missing detail",
			http.MethodGet,
			"/notifications/dnt_00000000000000000000000000000999",
			nil,
			http.StatusNotFound,
		},
		{
			"missing neighbor", http.MethodGet,
			"/notifications/dnt_00000000000000000000000000000999/neighbors", nil, http.StatusBadRequest,
		},
		{"unknown notification route", http.MethodGet, "/notifications/one/two/three", nil, http.StatusNotFound},
		{"view tail", http.MethodGet, "/notification-views/extra", nil, http.StatusNotFound},
		{"view method", http.MethodPost, "/notification-views", map[string]any{}, http.StatusMethodNotAllowed},
		{"settings query", http.MethodGet, "/notification-settings?extra=1", nil, http.StatusNotFound},
		{"settings method", http.MethodPost, "/notification-settings", map[string]any{}, http.StatusMethodNotAllowed},
		{"subscription query", http.MethodGet, "/push-subscriptions?extra=1", nil, http.StatusBadRequest},
		{"subscription tail", http.MethodGet, "/push-subscriptions/one/two", nil, http.StatusNotFound},
		{"subscription method", http.MethodPatch, "/push-subscriptions", map[string]any{}, http.StatusMethodNotAllowed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performNotificationStackRequest(
				t, fixture.handler, test.method, test.path, test.body,
			)
			require.Equal(t, test.status, response.Code, response.Body.String())
		})
	}

	response := performNotificationStackRequest(
		t, fixture.handler, http.MethodPost, "/notifications/bulk", map[string]any{
			"request_id": "notification-http-stale-bulk-0001", "action": "mark_read",
			"items": []map[string]any{{"id": notification.ID, "expected_version": notification.Version + 1}},
		},
	)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())

	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPut, "/notification-views", map[string]any{
			"request_id": "notification-http-stale-view-0001", "expected_version": 99,
			"views": []map[string]any{},
		},
	)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPut, "/notification-views", map[string]any{
			"request_id": "notification-http-invalid-view-0001", "expected_version": 1,
			"views": []map[string]any{{"name": "", "query": "status = open", "position": 0}},
		},
	)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())

	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodGet, "/notification-settings", nil,
	)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var settings NotificationSettings
	decodeNotificationStackResponse(t, response, &settings)
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPut, "/notification-settings", map[string]any{
			"request_id": "notification-http-stale-settings-0001", "expected_version": settings.Version + 1,
			"include_repository_in_push": true,
		},
	)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())

	receiver := newWebPushReceiver(t)
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPost, "/push-subscriptions", map[string]any{
			"request_id": "notification-http-invalid-subscription-0001", "endpoint": "http://unsafe.test",
			"name": "Phone", "keys": map[string]any{"auth": receiver.auth, "p256dh": receiver.publicKey},
		},
	)
	require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPost, "/push-subscriptions", map[string]any{
			"request_id": "notification-http-stale-subscription-0001",
			"endpoint":   "https://push.example.test/stale", "name": "Phone",
			"keys": map[string]any{"auth": receiver.auth, "p256dh": receiver.publicKey},
		},
	)
	require.Equal(t, http.StatusCreated, response.Code, response.Body.String())
	var device PushSubscriptionDevice
	decodeNotificationStackResponse(t, response, &device)
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodPut, "/push-subscriptions/"+device.ID, map[string]any{
			"request_id":       "notification-http-stale-subscription-0002",
			"expected_version": device.Version + 1, "name": "Tablet", "enabled": false,
		},
	)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	response = performNotificationStackRequest(
		t, fixture.handler, http.MethodDelete, "/push-subscriptions/"+device.ID, map[string]any{
			"request_id":       "notification-http-stale-subscription-0003",
			"expected_version": device.Version + 1,
		},
	)
	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
}
