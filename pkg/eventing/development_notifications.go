package eventing

import (
	"encoding/json"
	"time"

	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
)

type DevelopmentNotificationViewsDocument struct {
	Views   []developmentnotifications.SavedView `json:"views"`
	Version uint64                               `json:"version"`
}

type DevelopmentNotificationMutation struct {
	ID              string `json:"id"`
	ExpectedVersion uint64 `json:"expected_version"`
}

type DevelopmentNotificationBulkMutation struct {
	RequestID    string                            `json:"request_id"`
	Action       string                            `json:"action"`
	Items        []DevelopmentNotificationMutation `json:"items"`
	SnoozedUntil *time.Time                        `json:"snoozed_until,omitempty"`
}

type DevelopmentNotificationBulkMutationResult struct {
	Notifications []developmentnotifications.Notification `json:"notifications"`
	Replayed      bool                                    `json:"replayed,omitempty"`
}

type DevelopmentPushStateDocument struct {
	Version uint64          `json:"version"`
	State   json.RawMessage `json:"state"`
}
