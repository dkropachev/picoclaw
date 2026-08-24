package prworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	developmentnotifications "github.com/sipeed/picoclaw/pkg/developmentnotifications"
	"github.com/sipeed/picoclaw/pkg/eventing"
)

type developmentPushStore interface {
	GetDevelopmentPushState(ctx context.Context) (eventing.DevelopmentPushStateDocument, error)
	PutDevelopmentPushState(
		ctx context.Context,
		state json.RawMessage,
		expectedVersion uint64,
	) (eventing.DevelopmentPushStateDocument, error)
}

const maxPushSuppressionEntries = 2048

type pushState struct {
	IncludeRepository bool                        `json:"include_repository"`
	PrivateKey        string                      `json:"private_key"`
	PublicKey         string                      `json:"public_key"`
	Subscriptions     map[string]pushSubscription `json:"subscriptions"`
}

type pushSubscription struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Endpoint        string            `json:"endpoint"`
	Auth            string            `json:"auth"`
	P256DH          string            `json:"p256dh"`
	Enabled         bool              `json:"enabled"`
	Version         uint64            `json:"version"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	LastDeliveredAt *time.Time        `json:"last_delivered_at,omitempty"`
	Delivered       map[string]uint64 `json:"delivered"`
}

type NotificationSettings struct {
	IncludeRepositoryInPush bool   `json:"include_repository_in_push"`
	VAPIDPublicKey          string `json:"vapid_public_key,omitempty"`
	Version                 uint64 `json:"version"`
}

type PushSubscriptionInput struct {
	Endpoint string
	Auth     string
	P256DH   string
	Name     string
}

type PushSubscriptionDevice struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Enabled         bool       `json:"enabled"`
	Version         uint64     `json:"version"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	LastDeliveredAt *time.Time `json:"last_delivered_at,omitempty"`
}

func (service *Service) NotificationSettings(ctx context.Context) (NotificationSettings, error) {
	document, state, err := service.loadPushState(ctx, true)
	if err != nil {
		return NotificationSettings{}, err
	}
	return NotificationSettings{
		IncludeRepositoryInPush: state.IncludeRepository,
		VAPIDPublicKey:          state.PublicKey, Version: document.Version,
	}, nil
}

func (service *Service) PutNotificationSettings(
	ctx context.Context, includeRepository bool, expectedVersion uint64,
) (NotificationSettings, error) {
	document, state, err := service.loadPushState(ctx, true)
	if err != nil || document.Version != expectedVersion {
		return NotificationSettings{}, ErrConflict
	}
	state.IncludeRepository = includeRepository
	document, err = service.savePushState(ctx, state, expectedVersion)
	if err != nil {
		return NotificationSettings{}, err
	}
	return NotificationSettings{
		IncludeRepositoryInPush: state.IncludeRepository,
		VAPIDPublicKey:          state.PublicKey, Version: document.Version,
	}, nil
}

func (service *Service) ListPushSubscriptions(ctx context.Context) ([]PushSubscriptionDevice, error) {
	_, state, err := service.loadPushState(ctx, true)
	if err != nil {
		return nil, err
	}
	result := make([]PushSubscriptionDevice, 0, len(state.Subscriptions))
	for _, subscription := range state.Subscriptions {
		result = append(result, publicPushSubscription(subscription))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (service *Service) CreatePushSubscription(
	ctx context.Context, input PushSubscriptionInput,
) (PushSubscriptionDevice, error) {
	if err := validatePushSubscriptionInput(input); err != nil {
		return PushSubscriptionDevice{}, err
	}
	document, state, err := service.loadPushState(ctx, true)
	if err != nil {
		return PushSubscriptionDevice{}, err
	}
	digest := sha256.Sum256([]byte(input.Endpoint))
	id := "dps_" + hex.EncodeToString(digest[:16])
	if existing, found := state.Subscriptions[id]; found {
		return publicPushSubscription(existing), nil
	}
	now := service.now().UTC()
	subscription := pushSubscription{
		ID: id, Name: strings.TrimSpace(input.Name), Endpoint: input.Endpoint,
		Auth: input.Auth, P256DH: input.P256DH, Enabled: true, Version: 1,
		CreatedAt: now, UpdatedAt: now, Delivered: make(map[string]uint64),
	}
	state.Subscriptions[id] = subscription
	if _, err = service.savePushState(ctx, state, document.Version); err != nil {
		return PushSubscriptionDevice{}, err
	}
	return publicPushSubscription(subscription), nil
}

func (service *Service) UpdatePushSubscription(
	ctx context.Context, id, name string, enabled bool, expectedVersion uint64,
) (PushSubscriptionDevice, error) {
	document, state, err := service.loadPushState(ctx, false)
	if err != nil {
		return PushSubscriptionDevice{}, err
	}
	subscription, found := state.Subscriptions[id]
	if !found {
		return PushSubscriptionDevice{}, ErrNotFound
	}
	if subscription.Version != expectedVersion || strings.TrimSpace(name) == "" || len(name) > 128 {
		return PushSubscriptionDevice{}, ErrConflict
	}
	subscription.Name, subscription.Enabled = strings.TrimSpace(name), enabled
	subscription.Version++
	subscription.UpdatedAt = service.now().UTC()
	state.Subscriptions[id] = subscription
	if _, err = service.savePushState(ctx, state, document.Version); err != nil {
		return PushSubscriptionDevice{}, err
	}
	return publicPushSubscription(subscription), nil
}

func (service *Service) DeletePushSubscription(
	ctx context.Context, id string, expectedVersion uint64,
) error {
	document, state, err := service.loadPushState(ctx, false)
	if err != nil {
		return err
	}
	subscription, found := state.Subscriptions[id]
	if !found {
		return ErrNotFound
	}
	if subscription.Version != expectedVersion {
		return ErrConflict
	}
	delete(state.Subscriptions, id)
	_, err = service.savePushState(ctx, state, document.Version)
	return err
}

func deliverDevelopmentPush(
	ctx context.Context, backend any, notification developmentnotifications.Notification,
) bool {
	if notification.Priority != developmentnotifications.PriorityCritical &&
		notification.Priority != developmentnotifications.PriorityHigh || notification.Status != developmentnotifications.StatusOpen {
		return false
	}
	store, ok := backend.(developmentPushStore)
	if !ok {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	document, err := store.GetDevelopmentPushState(ctx)
	if err != nil {
		return false
	}
	state, err := decodePushState(document.State)
	if err != nil || state.PrivateKey == "" || state.PublicKey == "" {
		return false
	}
	payloadValue := map[string]any{
		"notification_id": notification.ID, "reason": notification.Reason,
	}
	if state.IncludeRepository {
		payloadValue["repository"] = notification.Repository
	}
	payload, _ := json.Marshal(payloadValue)
	changed := false
	for id, subscription := range state.Subscriptions {
		if !subscription.Enabled || subscription.Delivered[notification.ID] >= notification.Generation {
			continue
		}
		response, sendErr := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
			Endpoint: subscription.Endpoint,
			Keys:     webpush.Keys{Auth: subscription.Auth, P256dh: subscription.P256DH},
		}, &webpush.Options{
			Subscriber: "https://picoclaw.local", VAPIDPublicKey: state.PublicKey,
			VAPIDPrivateKey: state.PrivateKey, TTL: 300, Topic: notification.ID,
		})
		if response != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
			_ = response.Body.Close()
		}
		if sendErr != nil || response == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
			if response != nil &&
				(response.StatusCode == http.StatusGone || response.StatusCode == http.StatusNotFound) {
				subscription.Enabled = false
				subscription.Version++
				subscription.UpdatedAt = time.Now().UTC()
				state.Subscriptions[id] = subscription
				changed = true
			}
			continue
		}
		now := time.Now().UTC()
		subscription.Delivered[notification.ID] = notification.Generation
		subscription.LastDeliveredAt = &now
		state.Subscriptions[id] = subscription
		changed = true
	}
	if changed {
		encoded, _ := json.Marshal(state)
		_, err = store.PutDevelopmentPushState(ctx, encoded, document.Version)
		return err == nil
	}
	return false
}

// DeliverPendingDevelopmentPush recovers notifications committed before a
// process stopped between the durable workspace transaction and Web Push. It
// examines only a bounded recent set and compacts each subscription's delivery
// ledger to that same set, preventing the singleton push document from growing
// with notification history forever.
func (service *Service) DeliverPendingDevelopmentPush(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 20 {
		return 0, ErrInvalid
	}
	backend := service.notificationBackend()
	store := service.pushBackend()
	if backend == nil || store == nil {
		return 0, errors.New("push notification store is unavailable")
	}
	recent, err := backend.ListRecentDevelopmentPushNotifications(ctx, maxPushSuppressionEntries)
	if err != nil {
		return 0, err
	}
	document, err := store.GetDevelopmentPushState(ctx)
	if err != nil {
		return 0, err
	}
	state, err := decodePushState(document.State)
	if err != nil {
		return 0, err
	}
	active := make(map[string]struct{}, len(recent))
	for _, notification := range recent {
		active[notification.ID] = struct{}{}
	}
	processed := 0
	if prunePushDeliverySuppression(&state, active) {
		if _, err := service.savePushState(ctx, state, document.Version); err != nil {
			return 0, err
		}
		processed = 1
	}
	selected := make([]developmentnotifications.Notification, 0, limit)
	for _, notification := range recent {
		pending := false
		for _, subscription := range state.Subscriptions {
			if subscription.Enabled && subscription.Delivered[notification.ID] < notification.Generation {
				pending = true
				break
			}
		}
		if pending {
			selected = append(selected, notification)
			if len(selected) == limit {
				break
			}
		}
	}
	for _, notification := range selected {
		if deliverDevelopmentPush(ctx, backend, notification) {
			processed++
		}
	}
	return processed, nil
}

func prunePushDeliverySuppression(state *pushState, active map[string]struct{}) bool {
	if state == nil {
		return false
	}
	changed := false
	for id, subscription := range state.Subscriptions {
		if subscription.Delivered == nil {
			subscription.Delivered = make(map[string]uint64)
			state.Subscriptions[id] = subscription
			changed = true
			continue
		}
		for notificationID := range subscription.Delivered {
			if _, keep := active[notificationID]; keep {
				continue
			}
			delete(subscription.Delivered, notificationID)
			changed = true
		}
		state.Subscriptions[id] = subscription
	}
	return changed
}

func (service *Service) loadPushState(
	ctx context.Context, generate bool,
) (eventing.DevelopmentPushStateDocument, pushState, error) {
	store := service.pushBackend()
	if store == nil {
		return eventing.DevelopmentPushStateDocument{}, pushState{}, errors.New(
			"push notification store is unavailable",
		)
	}
	document, err := store.GetDevelopmentPushState(ctx)
	if err != nil {
		return document, pushState{}, err
	}
	state, err := decodePushState(document.State)
	if err != nil {
		return document, pushState{}, err
	}
	if generate && state.PrivateKey == "" {
		privateKey, publicKey, keyErr := webpush.GenerateVAPIDKeys()
		if keyErr != nil {
			return document, state, keyErr
		}
		state.PrivateKey, state.PublicKey = privateKey, publicKey
		document, err = service.savePushState(ctx, state, document.Version)
	}
	return document, state, err
}

func (service *Service) savePushState(
	ctx context.Context, state pushState, expectedVersion uint64,
) (eventing.DevelopmentPushStateDocument, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return eventing.DevelopmentPushStateDocument{}, err
	}
	return service.pushBackend().PutDevelopmentPushState(ctx, encoded, expectedVersion)
}

func (service *Service) pushBackend() developmentPushStore {
	backend := service.notificationBackend()
	store, _ := backend.(developmentPushStore)
	return store
}

func decodePushState(raw json.RawMessage) (pushState, error) {
	state := pushState{Subscriptions: make(map[string]pushSubscription)}
	if len(raw) == 0 || string(raw) == "{}" {
		return state, nil
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return pushState{}, err
	}
	if state.Subscriptions == nil {
		state.Subscriptions = make(map[string]pushSubscription)
	}
	for id, subscription := range state.Subscriptions {
		if subscription.Delivered == nil {
			subscription.Delivered = make(map[string]uint64)
			state.Subscriptions[id] = subscription
		}
	}
	return state, nil
}

func validatePushSubscriptionInput(input PushSubscriptionInput) error {
	parsed, err := url.ParseRequestURI(input.Endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || len(input.Endpoint) > 4096 ||
		strings.TrimSpace(input.Name) == "" || len(input.Name) > 128 {
		return ErrInvalid
	}
	for _, key := range []string{input.Auth, input.P256DH} {
		if len(key) < 8 || len(key) > 512 {
			return ErrInvalid
		}
		if _, err := base64.RawURLEncoding.DecodeString(key); err != nil {
			return ErrInvalid
		}
	}
	return nil
}

func publicPushSubscription(value pushSubscription) PushSubscriptionDevice {
	return PushSubscriptionDevice{
		ID: value.ID, Name: value.Name, Enabled: value.Enabled, Version: value.Version,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		LastDeliveredAt: value.LastDeliveredAt,
	}
}
