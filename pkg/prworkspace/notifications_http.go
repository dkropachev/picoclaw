package prworkspace

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
	"github.com/sipeed/picoclaw/pkg/eventing"
)

func (handler *HTTPHandler) serveNotificationAPI(
	w http.ResponseWriter, r *http.Request, resource string, tail []string,
) {
	switch resource {
	case "notifications":
		handler.serveNotifications(w, r, tail)
	case "notification-views":
		handler.serveNotificationViews(w, r, tail)
	case "notification-settings":
		handler.serveNotificationSettings(w, r, tail)
	case "push-subscriptions":
		handler.servePushSubscriptions(w, r, tail)
	}
}

func (handler *HTTPHandler) serveNotifications(w http.ResponseWriter, r *http.Request, tail []string) {
	if len(tail) == 0 && r.Method == http.MethodGet {
		query := r.URL.Query()
		for key := range query {
			if key != "query" && key != "cursor" && key != "limit" {
				writeHTTPError(w, http.StatusBadRequest, "invalid_query", nil)
				return
			}
		}
		limit := 50
		if query.Get("limit") != "" {
			var err error
			limit, err = strconv.Atoi(query.Get("limit"))
			if err != nil {
				writeHTTPError(w, http.StatusBadRequest, "invalid_query", nil)
				return
			}
		}
		page, err := handler.service.ListNotifications(r.Context(), query.Get("query"), query.Get("cursor"), limit)
		if err != nil {
			var queryErr *developmentnotifications.QueryError
			if errors.As(err, &queryErr) {
				writeHTTPJSON(w, http.StatusBadRequest, map[string]any{
					"code": "invalid_query", "message": queryErr.Message, "position": queryErr.Position,
				})
				return
			}
			writeHTTPError(w, http.StatusBadRequest, "invalid_query", nil)
			return
		}
		counts, countErr := handler.service.NotificationCounts(r.Context())
		if countErr != nil {
			writeHTTPError(w, http.StatusServiceUnavailable, "notifications_unavailable", nil)
			return
		}
		writeHTTPJSON(w, http.StatusOK, map[string]any{
			"notifications": page.Notifications, "next_cursor": page.Next, "counts": counts,
		})
		return
	}
	if len(tail) == 1 && tail[0] == "bulk" && r.Method == http.MethodPost {
		var body struct {
			RequestID string `json:"request_id"`
			Action    string `json:"action"`
			Items     []struct {
				ID              string `json:"id"`
				ExpectedVersion uint64 `json:"expected_version"`
			} `json:"items"`
			SnoozedUntil *time.Time `json:"snoozed_until"`
		}
		if !decodeHTTPBody(w, r, &body) {
			return
		}
		if !validRequestID(body.RequestID) || len(body.Items) == 0 || len(body.Items) > 100 {
			writeHTTPError(w, http.StatusBadRequest, "invalid_request", nil)
			return
		}
		items := make([]eventing.DevelopmentNotificationMutation, 0, len(body.Items))
		for _, item := range body.Items {
			items = append(items, eventing.DevelopmentNotificationMutation{
				ID: item.ID, ExpectedVersion: item.ExpectedVersion,
			})
		}
		result, err := handler.service.MutateNotifications(
			r.Context(), body.RequestID, body.Action, items, body.SnoozedUntil,
		)
		if err != nil {
			writeHTTPError(w, http.StatusConflict, "notification_conflict", nil)
			return
		}
		writeHTTPJSON(w, http.StatusOK, map[string]any{
			"notifications": result.Notifications, "replayed": result.Replayed,
		})
		return
	}
	if len(tail) == 1 && r.Method == http.MethodGet {
		if r.URL.RawQuery != "" {
			writeHTTPError(w, http.StatusBadRequest, "invalid_query", nil)
			return
		}
		value, err := handler.service.GetNotification(r.Context(), tail[0])
		if err != nil {
			writeHTTPError(w, http.StatusNotFound, "not_found", nil)
			return
		}
		writeHTTPJSON(w, http.StatusOK, value)
		return
	}
	if len(tail) == 2 && tail[1] == "neighbors" && r.Method == http.MethodGet {
		for key := range r.URL.Query() {
			if key != "query" {
				writeHTTPError(w, http.StatusBadRequest, "invalid_query", nil)
				return
			}
		}
		neighbors, err := handler.service.NotificationNeighbors(r.Context(), tail[0], r.URL.Query().Get("query"))
		if err != nil {
			writeHTTPError(w, http.StatusBadRequest, "invalid_query", nil)
			return
		}
		writeHTTPJSON(w, http.StatusOK, neighbors)
		return
	}
	writeHTTPError(w, http.StatusNotFound, "not_found", nil)
}

func (handler *HTTPHandler) serveNotificationViews(w http.ResponseWriter, r *http.Request, tail []string) {
	if len(tail) != 0 || r.URL.RawQuery != "" {
		writeHTTPError(w, http.StatusNotFound, "not_found", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		document, err := handler.service.NotificationViews(r.Context())
		if err != nil {
			writeHTTPError(w, http.StatusServiceUnavailable, "notifications_unavailable", nil)
			return
		}
		writeHTTPJSON(w, http.StatusOK, document)
	case http.MethodPut:
		var body struct {
			Views []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Query    string `json:"query"`
				Pinned   bool   `json:"pinned"`
				Default  bool   `json:"default"`
				Position int    `json:"position"`
			} `json:"views"`
			ExpectedVersion uint64 `json:"expected_version"`
			RequestID       string `json:"request_id"`
		}
		if !decodeHTTPBody(w, r, &body) {
			return
		}
		if !validRequestID(body.RequestID) {
			writeHTTPError(w, http.StatusBadRequest, "invalid_request", nil)
			return
		}
		current, err := handler.service.NotificationViews(r.Context())
		if err != nil || current.Version != body.ExpectedVersion {
			writeHTTPError(w, http.StatusConflict, "version_conflict", nil)
			return
		}
		byID := make(map[string]developmentnotifications.SavedView, len(current.Views))
		for _, view := range current.Views {
			byID[view.ID] = view
		}
		now := handler.service.now().UTC()
		views := make([]developmentnotifications.SavedView, 0, len(body.Views))
		for index, draft := range body.Views {
			id := strings.TrimSpace(draft.ID)
			if id == "" {
				id = stableID("dnv_", body.RequestID, strconv.Itoa(index))
			}
			input := developmentnotifications.SavedViewDraft{
				ID: id, Name: draft.Name, Query: draft.Query, Pinned: draft.Pinned,
				Default: draft.Default, Position: draft.Position,
			}
			if existing, ok := byID[id]; ok {
				view, _, updateErr := developmentnotifications.UpdateSavedView(existing, input, existing.Version, now)
				if updateErr != nil {
					writeHTTPError(w, http.StatusBadRequest, "invalid_views", nil)
					return
				}
				views = append(views, view)
			} else {
				view, createErr := developmentnotifications.NewSavedView(input, now)
				if createErr != nil {
					writeHTTPError(w, http.StatusBadRequest, "invalid_views", nil)
					return
				}
				views = append(views, view)
			}
		}
		document, err := handler.service.PutNotificationViews(r.Context(), views, body.ExpectedVersion)
		if err != nil {
			writeHTTPError(w, http.StatusConflict, "version_conflict", nil)
			return
		}
		writeHTTPJSON(w, http.StatusOK, document)
	default:
		writeHTTPMethod(w, http.MethodGet, http.MethodPut)
	}
}

func (handler *HTTPHandler) serveNotificationSettings(w http.ResponseWriter, r *http.Request, tail []string) {
	if len(tail) != 0 || r.URL.RawQuery != "" {
		writeHTTPError(w, http.StatusNotFound, "not_found", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := handler.service.NotificationSettings(r.Context())
		if err != nil {
			writeHTTPError(w, http.StatusServiceUnavailable, "push_unavailable", nil)
			return
		}
		writeHTTPJSON(w, http.StatusOK, settings)
	case http.MethodPut:
		var body struct {
			IncludeRepository bool   `json:"include_repository_in_push"`
			ExpectedVersion   uint64 `json:"expected_version"`
			RequestID         string `json:"request_id"`
		}
		if !decodeHTTPBody(w, r, &body) {
			return
		}
		if !validRequestID(body.RequestID) {
			writeHTTPError(w, http.StatusBadRequest, "invalid_request", nil)
			return
		}
		settings, err := handler.service.PutNotificationSettings(
			r.Context(),
			body.IncludeRepository,
			body.ExpectedVersion,
		)
		if err != nil {
			writeHTTPError(w, http.StatusConflict, "version_conflict", nil)
			return
		}
		writeHTTPJSON(w, http.StatusOK, settings)
	default:
		writeHTTPMethod(w, http.MethodGet, http.MethodPut)
	}
}

func (handler *HTTPHandler) servePushSubscriptions(w http.ResponseWriter, r *http.Request, tail []string) {
	if r.URL.RawQuery != "" {
		writeHTTPError(w, http.StatusBadRequest, "invalid_query", nil)
		return
	}
	if len(tail) == 0 {
		switch r.Method {
		case http.MethodGet:
			items, err := handler.service.ListPushSubscriptions(r.Context())
			if err != nil {
				writeHTTPError(w, http.StatusServiceUnavailable, "push_unavailable", nil)
				return
			}
			writeHTTPJSON(w, http.StatusOK, map[string]any{"subscriptions": items})
		case http.MethodPost:
			var body struct {
				Endpoint string `json:"endpoint"`
				Keys     struct {
					Auth   string `json:"auth"`
					P256DH string `json:"p256dh"`
				} `json:"keys"`
				Name       string `json:"name"`
				RequestID  string `json:"request_id"`
				Expiration *int64 `json:"expiration_time"`
			}
			if !decodeHTTPBody(w, r, &body) {
				return
			}
			if !validRequestID(body.RequestID) {
				writeHTTPError(w, http.StatusBadRequest, "invalid_request", nil)
				return
			}
			item, err := handler.service.CreatePushSubscription(r.Context(), PushSubscriptionInput{
				Endpoint: body.Endpoint, Auth: body.Keys.Auth, P256DH: body.Keys.P256DH, Name: body.Name,
			})
			if err != nil {
				writeHTTPError(w, http.StatusBadRequest, "invalid_subscription", nil)
				return
			}
			writeHTTPJSON(w, http.StatusCreated, item)
		default:
			writeHTTPMethod(w, http.MethodGet, http.MethodPost)
		}
		return
	}
	if len(tail) != 1 {
		writeHTTPError(w, http.StatusNotFound, "not_found", nil)
		return
	}
	var body struct {
		Name            string `json:"name"`
		Enabled         bool   `json:"enabled"`
		ExpectedVersion uint64 `json:"expected_version"`
		RequestID       string `json:"request_id"`
	}
	if !decodeHTTPBody(w, r, &body) {
		return
	}
	if !validRequestID(body.RequestID) {
		writeHTTPError(w, http.StatusBadRequest, "invalid_request", nil)
		return
	}
	switch r.Method {
	case http.MethodPut:
		item, err := handler.service.UpdatePushSubscription(
			r.Context(),
			tail[0],
			body.Name,
			body.Enabled,
			body.ExpectedVersion,
		)
		if err != nil {
			writeHTTPError(w, http.StatusConflict, "subscription_conflict", nil)
			return
		}
		writeHTTPJSON(w, http.StatusOK, item)
	case http.MethodDelete:
		if err := handler.service.DeletePushSubscription(r.Context(), tail[0], body.ExpectedVersion); err != nil {
			writeHTTPError(w, http.StatusConflict, "subscription_conflict", nil)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeHTTPMethod(w, http.MethodPut, http.MethodDelete)
	}
}
