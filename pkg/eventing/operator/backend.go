// Package operator exposes a read-mostly, sanitized view of the live durable
// event inbox and an explicit additive replay operation.
package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	// DefaultLimit is the default operator page size.
	DefaultLimit = 50
	// MaximumLimit is the largest operator page size accepted.
	MaximumLimit = 100

	maxSourceBytes      = 128
	maxConnectorBytes   = 256
	maxEventTypeBytes   = 256
	maxWorkflowRefBytes = 1024
)

var (
	// ErrInvalidRequest reports invalid operator input. It is safe to map to an
	// HTTP 400 without exposing the wrapped diagnostic to remote clients.
	ErrInvalidRequest = errors.New("invalid event operator request")
	// ErrUnavailable reports an unavailable or internally inconsistent
	// operator backend.
	ErrUnavailable = errors.New("event operator backend is unavailable")
)

// Store is the complete, deliberately narrow durable boundary available to
// the operator surface. It excludes lease transitions and manual dispatch
// creation.
type Store interface {
	eventing.EventOperatorReader
	eventing.DispatchOperatorReader
	eventing.DispatchOperatorGetter
	Replay(ctx context.Context, id string) (eventing.InsertResult, error)
}

// BackendConfig contains generation-specific dependencies.
type BackendConfig struct {
	Store   Store
	Reviews http.Handler
}

// Backend is an immutable operator service suitable for sharing between an
// HTTP controller and other in-process consumers.
type Backend struct {
	store   Store
	reviews http.Handler
}

// NewBackend validates and freezes an operator backend.
func NewBackend(config BackendConfig) (*Backend, error) {
	if config.Store == nil {
		return nil, errors.New("event operator store is required")
	}
	return &Backend{
		store:   config.Store,
		reviews: config.Reviews,
	}, nil
}

// EventListRequest selects a newest-first event page.
type EventListRequest struct {
	Source        string
	Connector     string
	Type          string
	RoutingStatus eventing.RoutingStatus
	Limit         int
	Cursor        string
}

// DispatchListRequest selects a newest-first dispatch page.
type DispatchListRequest struct {
	EventID     string
	WorkflowRef string
	Status      eventing.DispatchStatus
	Limit       int
	Cursor      string
}

// ActorView is the explicit public actor projection.
type ActorView struct {
	ID          string            `json:"id,omitempty"`
	Type        string            `json:"type,omitempty"`
	DisplayName string            `json:"display_name,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

// SubjectView is the explicit public subject projection.
type SubjectView struct {
	ID         string            `json:"id,omitempty"`
	Type       string            `json:"type,omitempty"`
	Name       string            `json:"name,omitempty"`
	URL        string            `json:"url,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// RoutingView excludes the live routing lease token.
type RoutingView struct {
	Status      eventing.RoutingStatus `json:"status"`
	LeaseUntil  *time.Time             `json:"lease_until,omitempty"`
	AvailableAt time.Time              `json:"available_at"`
	Attempts    int                    `json:"attempts"`
	LastError   string                 `json:"last_error,omitempty"`
	UpdatedAt   time.Time              `json:"updated_at"`
}

// EventView is the payload-free projection returned by event listings.
// Dedupe keys and routing lease tokens are intentionally unrepresentable.
type EventView struct {
	ID           string            `json:"id"`
	Source       string            `json:"source"`
	Connector    string            `json:"connector"`
	Type         string            `json:"type"`
	Actor        *ActorView        `json:"actor,omitempty"`
	Subject      *SubjectView      `json:"subject,omitempty"`
	OccurredAt   *time.Time        `json:"occurred_at,omitempty"`
	ReceivedAt   time.Time         `json:"received_at"`
	Attributes   map[string]string `json:"attributes,omitempty"`
	ReplayOf     string            `json:"replay_of,omitempty"`
	PayloadBytes int               `json:"payload_bytes"`
	Routing      RoutingView       `json:"routing"`
}

// WorkflowEventView is the exact already-redacted event projection used by
// trusted workflow-development callers. It deliberately omits deduplication
// identity and every routing owner/lease field while keeping the payload and
// its JSON number tokens available to the workflow expression decoder.
type WorkflowEventView struct {
	ID         string            `json:"id"`
	Source     string            `json:"source"`
	Connector  string            `json:"connector"`
	Type       string            `json:"type"`
	Actor      *ActorView        `json:"actor,omitempty"`
	Subject    *SubjectView      `json:"subject,omitempty"`
	OccurredAt *time.Time        `json:"occurred_at,omitempty"`
	ReceivedAt time.Time         `json:"received_at"`
	Payload    json.RawMessage   `json:"payload"`
	Attributes map[string]string `json:"attributes,omitempty"`
	ReplayOf   string            `json:"replay_of,omitempty"`
}

// EventPage is a sanitized event page.
type EventPage struct {
	Events     []EventView `json:"events"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// DispatchView excludes the live dispatch lease token.
type DispatchView struct {
	ID               string                  `json:"id"`
	EventID          string                  `json:"event_id"`
	WorkflowRef      string                  `json:"workflow_ref"`
	WorkflowRevision string                  `json:"workflow_revision,omitempty"`
	RunID            string                  `json:"run_id"`
	Status           eventing.DispatchStatus `json:"status"`
	LeaseUntil       *time.Time              `json:"lease_until,omitempty"`
	AvailableAt      time.Time               `json:"available_at"`
	Attempts         int                     `json:"attempts"`
	LastError        string                  `json:"last_error,omitempty"`
	CreatedAt        time.Time               `json:"created_at"`
	UpdatedAt        time.Time               `json:"updated_at"`
	LinkedAt         *time.Time              `json:"linked_at,omitempty"`
	FinishedAt       *time.Time              `json:"finished_at,omitempty"`
}

// DispatchPage is a sanitized dispatch page.
type DispatchPage struct {
	Dispatches []DispatchView `json:"dispatches"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// ReplayResult is the sanitized representation of one newly created replay.
type ReplayResult struct {
	Event EventView `json:"event"`
}

// ListEvents returns a bounded sanitized event page.
func (backend *Backend) ListEvents(
	ctx context.Context,
	request EventListRequest,
) (EventPage, error) {
	if backend == nil || backend.store == nil {
		return EventPage{}, ErrUnavailable
	}
	limit, err := validateLimit(request.Limit)
	if err != nil {
		return EventPage{}, err
	}
	if err = validateOptionalText("source", request.Source, maxSourceBytes); err != nil {
		return EventPage{}, err
	}
	if err = validateOptionalText("connector", request.Connector, maxConnectorBytes); err != nil {
		return EventPage{}, err
	}
	if err = validateOptionalText("type", request.Type, maxEventTypeBytes); err != nil {
		return EventPage{}, err
	}
	if !validRoutingStatus(request.RoutingStatus) {
		return EventPage{}, fmt.Errorf("%w: routing status is invalid", ErrInvalidRequest)
	}

	filterBinding := eventFilterBinding{
		Source:        request.Source,
		Connector:     request.Connector,
		Type:          request.Type,
		RoutingStatus: request.RoutingStatus,
	}
	after, err := decodeEventCursor(request.Cursor, filterBinding)
	if err != nil {
		return EventPage{}, err
	}
	stored, err := backend.store.ListEventMetadata(ctx, eventing.EventFilter{
		Source:        request.Source,
		Connector:     request.Connector,
		Type:          request.Type,
		RoutingStatus: request.RoutingStatus,
		After:         after,
		Limit:         limit,
	})
	if err != nil {
		return EventPage{}, err
	}

	page := EventPage{Events: make([]EventView, len(stored.Events))}
	for index := range stored.Events {
		page.Events[index] = projectEventMetadata(stored.Events[index])
	}
	if stored.Next != nil {
		page.NextCursor, err = encodeEventCursor(*stored.Next, filterBinding)
		if err != nil {
			return EventPage{}, fmt.Errorf("%w: encode event cursor", ErrUnavailable)
		}
	}
	return page, nil
}

// GetEvent returns a sanitized event detail.
func (backend *Backend) GetEvent(
	ctx context.Context,
	id string,
) (EventView, error) {
	if backend == nil || backend.store == nil {
		return EventView{}, ErrUnavailable
	}
	if !validEventID(id) {
		return EventView{}, fmt.Errorf("%w: event ID is invalid", ErrInvalidRequest)
	}
	stored, err := backend.store.GetEventMetadata(ctx, id)
	if err != nil {
		return EventView{}, err
	}
	return projectEventMetadata(stored), nil
}

// GetEventPayload returns an independent copy of the exact already-redacted
// stored JSON bytes. Callers must not parse and re-encode these bytes before
// presenting them because doing so can alter large numeric tokens.
func (backend *Backend) GetEventPayload(
	ctx context.Context,
	id string,
) ([]byte, error) {
	if backend == nil || backend.store == nil {
		return nil, ErrUnavailable
	}
	if !validEventID(id) {
		return nil, fmt.Errorf("%w: event ID is invalid", ErrInvalidRequest)
	}
	payload, err := backend.store.GetEventPayload(ctx, id)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), payload...), nil
}

// GetWorkflowEvent returns metadata and payload through one acquired operator
// generation. Events are immutable, so reading metadata before payload is a
// consistent snapshot; retention between the reads fails the whole operation
// instead of returning a partial projection.
func (backend *Backend) GetWorkflowEvent(
	ctx context.Context,
	id string,
) (WorkflowEventView, error) {
	if backend == nil || backend.store == nil {
		return WorkflowEventView{}, ErrUnavailable
	}
	if !validEventID(id) {
		return WorkflowEventView{}, fmt.Errorf("%w: event ID is invalid", ErrInvalidRequest)
	}
	stored, err := backend.store.GetEventMetadata(ctx, id)
	if err != nil {
		return WorkflowEventView{}, err
	}
	if stored.Envelope.ID != id {
		return WorkflowEventView{}, fmt.Errorf("%w: event metadata identity changed", ErrUnavailable)
	}
	payload, err := backend.store.GetEventPayload(ctx, id)
	if err != nil {
		return WorkflowEventView{}, err
	}
	if len(payload) != stored.PayloadBytes {
		return WorkflowEventView{}, fmt.Errorf("%w: event payload metadata changed", ErrUnavailable)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil || object == nil {
		return WorkflowEventView{}, fmt.Errorf("%w: event payload is invalid", ErrUnavailable)
	}
	envelope := stored.Envelope
	return WorkflowEventView{
		ID:         envelope.ID,
		Source:     envelope.Source,
		Connector:  envelope.Connector,
		Type:       envelope.Type,
		Actor:      projectActor(envelope.Actor),
		Subject:    projectSubject(envelope.Subject),
		OccurredAt: cloneTime(envelope.OccurredAt),
		ReceivedAt: envelope.ReceivedAt,
		Payload:    append(json.RawMessage(nil), payload...),
		Attributes: cloneStringMap(envelope.Attributes),
		ReplayOf:   envelope.ReplayOf,
	}, nil
}

// ListDispatches returns a bounded sanitized dispatch page.
func (backend *Backend) ListDispatches(
	ctx context.Context,
	request DispatchListRequest,
) (DispatchPage, error) {
	if backend == nil || backend.store == nil {
		return DispatchPage{}, ErrUnavailable
	}
	limit, err := validateLimit(request.Limit)
	if err != nil {
		return DispatchPage{}, err
	}
	if request.EventID != "" && !validEventID(request.EventID) {
		return DispatchPage{}, fmt.Errorf("%w: event ID is invalid", ErrInvalidRequest)
	}
	if err = validateOptionalText(
		"workflow reference",
		request.WorkflowRef,
		maxWorkflowRefBytes,
	); err != nil {
		return DispatchPage{}, err
	}
	if !validDispatchStatus(request.Status) {
		return DispatchPage{}, fmt.Errorf("%w: dispatch status is invalid", ErrInvalidRequest)
	}

	filterBinding := dispatchFilterBinding{
		EventID:     request.EventID,
		WorkflowRef: request.WorkflowRef,
		Status:      request.Status,
	}
	after, err := decodeDispatchCursor(request.Cursor, filterBinding)
	if err != nil {
		return DispatchPage{}, err
	}
	stored, err := backend.store.ListDispatchMetadata(ctx, eventing.DispatchFilter{
		EventID:     request.EventID,
		WorkflowRef: request.WorkflowRef,
		Status:      request.Status,
		After:       after,
		Limit:       limit,
	})
	if err != nil {
		return DispatchPage{}, err
	}

	page := DispatchPage{Dispatches: make([]DispatchView, len(stored.Dispatches))}
	for index := range stored.Dispatches {
		page.Dispatches[index] = projectDispatch(stored.Dispatches[index])
	}
	if stored.Next != nil {
		page.NextCursor, err = encodeDispatchCursor(*stored.Next, filterBinding)
		if err != nil {
			return DispatchPage{}, fmt.Errorf("%w: encode dispatch cursor", ErrUnavailable)
		}
	}
	return page, nil
}

// GetDispatch returns one sanitized dispatch without worker ownership
// credentials.
func (backend *Backend) GetDispatch(
	ctx context.Context,
	id string,
) (DispatchView, error) {
	if backend == nil || backend.store == nil {
		return DispatchView{}, ErrUnavailable
	}
	if !validDispatchID(id) {
		return DispatchView{}, fmt.Errorf("%w: dispatch ID is invalid", ErrInvalidRequest)
	}
	stored, err := backend.store.GetDispatchMetadata(ctx, id)
	if err != nil {
		return DispatchView{}, err
	}
	if stored.ID != id {
		return DispatchView{}, fmt.Errorf(
			"%w: dispatch metadata identity changed",
			ErrUnavailable,
		)
	}
	return projectDispatch(stored), nil
}

// Replay creates exactly one additive replay through the live store.
func (backend *Backend) Replay(ctx context.Context, id string) (ReplayResult, error) {
	if backend == nil || backend.store == nil {
		return ReplayResult{}, ErrUnavailable
	}
	if !validEventID(id) {
		return ReplayResult{}, fmt.Errorf("%w: event ID is invalid", ErrInvalidRequest)
	}
	result, err := backend.store.Replay(ctx, id)
	if err != nil {
		return ReplayResult{}, err
	}
	if !result.Inserted {
		return ReplayResult{}, fmt.Errorf("%w: replay was not inserted", ErrUnavailable)
	}
	if !validEventID(result.Event.Envelope.ID) ||
		result.Event.Envelope.ReplayOf != id {
		return ReplayResult{}, fmt.Errorf("%w: replay result is invalid", ErrUnavailable)
	}
	return ReplayResult{Event: projectStoredEvent(result.Event)}, nil
}

func projectEventMetadata(stored eventing.StoredEventMetadata) EventView {
	return projectEvent(stored.Envelope, stored.Routing, stored.PayloadBytes)
}

func projectStoredEvent(stored eventing.StoredEvent) EventView {
	return projectEvent(stored.Envelope, stored.Routing, len(stored.Envelope.Payload))
}

func projectEvent(
	envelope eventing.Envelope,
	routing eventing.RoutingState,
	payloadBytes int,
) EventView {
	return EventView{
		ID:           envelope.ID,
		Source:       envelope.Source,
		Connector:    envelope.Connector,
		Type:         envelope.Type,
		Actor:        projectActor(envelope.Actor),
		Subject:      projectSubject(envelope.Subject),
		OccurredAt:   cloneTime(envelope.OccurredAt),
		ReceivedAt:   envelope.ReceivedAt,
		Attributes:   cloneStringMap(envelope.Attributes),
		ReplayOf:     envelope.ReplayOf,
		PayloadBytes: payloadBytes,
		Routing: RoutingView{
			Status:      routing.Status,
			LeaseUntil:  cloneTime(routing.LeaseUntil),
			AvailableAt: routing.AvailableAt,
			Attempts:    routing.Attempts,
			LastError:   routing.LastError,
			UpdatedAt:   routing.UpdatedAt,
		},
	}
}

func projectActor(actor *eventing.Actor) *ActorView {
	if actor == nil {
		return nil
	}
	return &ActorView{
		ID:          actor.ID,
		Type:        actor.Type,
		DisplayName: actor.DisplayName,
		Attributes:  cloneStringMap(actor.Attributes),
	}
}

func projectSubject(subject *eventing.Subject) *SubjectView {
	if subject == nil {
		return nil
	}
	return &SubjectView{
		ID:         subject.ID,
		Type:       subject.Type,
		Name:       subject.Name,
		URL:        subject.URL,
		Attributes: cloneStringMap(subject.Attributes),
	}
}

func projectDispatch(dispatch eventing.DispatchMetadata) DispatchView {
	return DispatchView{
		ID:               dispatch.ID,
		EventID:          dispatch.EventID,
		WorkflowRef:      dispatch.WorkflowRef,
		WorkflowRevision: dispatch.WorkflowRevision,
		RunID:            dispatch.RunID,
		Status:           dispatch.Status,
		LeaseUntil:       cloneTime(dispatch.LeaseUntil),
		AvailableAt:      dispatch.AvailableAt,
		Attempts:         dispatch.Attempts,
		LastError:        dispatch.LastError,
		CreatedAt:        dispatch.CreatedAt,
		UpdatedAt:        dispatch.UpdatedAt,
		LinkedAt:         cloneTime(dispatch.LinkedAt),
		FinishedAt:       cloneTime(dispatch.FinishedAt),
	}
}

func validateLimit(limit int) (int, error) {
	switch {
	case limit == 0:
		return DefaultLimit, nil
	case limit < 0:
		return 0, fmt.Errorf("%w: limit must be positive", ErrInvalidRequest)
	case limit > MaximumLimit:
		return 0, fmt.Errorf(
			"%w: limit must not exceed %d",
			ErrInvalidRequest,
			MaximumLimit,
		)
	default:
		return limit, nil
	}
}

func validateOptionalText(field, value string, maximum int) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) ||
		value != strings.TrimSpace(value) ||
		len(value) > maximum {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidRequest, field)
	}
	return nil
}

func validRoutingStatus(status eventing.RoutingStatus) bool {
	switch status {
	case "", eventing.RoutingPending, eventing.RoutingClaimed,
		eventing.RoutingSucceeded, eventing.RoutingDead:
		return true
	default:
		return false
	}
}

func validDispatchStatus(status eventing.DispatchStatus) bool {
	switch status {
	case "", eventing.DispatchPending, eventing.DispatchClaimed,
		eventing.DispatchRunning, eventing.DispatchSucceeded,
		eventing.DispatchFailed, eventing.DispatchDead:
		return true
	default:
		return false
	}
}

func validEventID(id string) bool {
	return validPrefixedHexID(id, "ev_")
}

func validDispatchID(id string) bool {
	return validPrefixedHexID(id, "dsp_")
}

func validPrefixedHexID(id, prefix string) bool {
	if len(id) != len(prefix)+32 || !strings.HasPrefix(id, prefix) {
		return false
	}
	for _, character := range id[len(prefix):] {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneStringMap(value map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	cloned := make(map[string]string, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}
