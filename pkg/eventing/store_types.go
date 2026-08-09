package eventing

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const schemaVersion = 12

const (
	maxWorkflowRefLength      = 1024
	maxWorkflowRevisionLength = 256
	maxErrorDetailBytes       = 16 << 10
)

var (
	// ErrNotFound reports a missing event or dispatch.
	ErrNotFound = errors.New("eventing record not found")
	// ErrClosed reports use of a closed store.
	ErrClosed = errors.New("eventing store is closed")
	// ErrUnsupportedPlatform reports a target where the embedded SQLite driver
	// is unavailable.
	ErrUnsupportedPlatform = errors.New("durable eventing is unsupported on this platform")
	// ErrStaleLease rejects work by a worker that no longer owns a live lease.
	ErrStaleLease = errors.New("stale or unowned eventing lease")
	// ErrInvalidTransition rejects an invalid state-machine transition.
	ErrInvalidTransition = errors.New("invalid eventing state transition")
	// ErrSchemaTooNew reports a database created by newer code.
	ErrSchemaTooNew = errors.New("eventing database schema is newer than supported")
	// ErrSchemaInvalid reports a database whose declared schema version does not
	// match the required tables, columns, constraints, or indexes.
	ErrSchemaInvalid = errors.New("eventing database schema is invalid")
	// ErrRunIDMismatch prevents linking a dispatch to an unexpected workflow
	// run, preserving deterministic start idempotency.
	ErrRunIDMismatch = errors.New("workflow run ID does not match dispatch")
	// ErrPayloadTooLarge reports an event payload over the configured limit.
	ErrPayloadTooLarge = errors.New("event payload exceeds configured limit")
)

// RoutingStatus is the durable inbox routing state.
type RoutingStatus string

const (
	RoutingPending   RoutingStatus = "pending"
	RoutingClaimed   RoutingStatus = "claimed"
	RoutingSucceeded RoutingStatus = "succeeded"
	RoutingDead      RoutingStatus = "dead"
)

// DispatchStatus is the independent state of one event/workflow delivery.
type DispatchStatus string

const (
	DispatchPending   DispatchStatus = "pending"
	DispatchClaimed   DispatchStatus = "claimed"
	DispatchRunning   DispatchStatus = "running"
	DispatchSucceeded DispatchStatus = "succeeded"
	DispatchFailed    DispatchStatus = "failed"
	DispatchDead      DispatchStatus = "dead"
)

// RoutingState contains mutable inbox-routing bookkeeping.
type RoutingState struct {
	Status      RoutingStatus `json:"status"`
	LeaseToken  string        `json:"lease_token,omitempty"`
	LeaseUntil  *time.Time    `json:"lease_until,omitempty"`
	AvailableAt time.Time     `json:"available_at"`
	Attempts    int           `json:"attempts"`
	LastError   string        `json:"last_error,omitempty"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// StoredEvent combines an immutable envelope with mutable routing state.
type StoredEvent struct {
	Envelope Envelope     `json:"envelope"`
	Routing  RoutingState `json:"routing"`
}

// InsertResult reports whether an envelope was newly inserted or matched an
// existing connector deduplication identity.
type InsertResult struct {
	Event    StoredEvent `json:"event"`
	Inserted bool        `json:"inserted"`
}

// Dispatch tracks delivery of one event to one workflow definition.
type Dispatch struct {
	ID               string         `json:"id"`
	EventID          string         `json:"event_id"`
	WorkflowRef      string         `json:"workflow_ref"`
	WorkflowRevision string         `json:"workflow_revision,omitempty"`
	RunID            string         `json:"run_id"`
	Status           DispatchStatus `json:"status"`
	LeaseToken       string         `json:"lease_token,omitempty"`
	LeaseUntil       *time.Time     `json:"lease_until,omitempty"`
	AvailableAt      time.Time      `json:"available_at"`
	Attempts         int            `json:"attempts"`
	LastError        string         `json:"last_error,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	LinkedAt         *time.Time     `json:"linked_at,omitempty"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
}

// EventCursor is an opaque-in-spirit keyset position. Callers should pass back
// the value supplied by EventPage.Next.
type EventCursor struct {
	ReceivedAt time.Time `json:"received_at"`
	ID         string    `json:"id"`
}

// EventFilter selects events in newest-first stable keyset order.
type EventFilter struct {
	Source        string
	Connector     string
	Type          string
	RoutingStatus RoutingStatus
	After         *EventCursor
	Limit         int
}

// EventPage is one keyset-paginated event result.
type EventPage struct {
	Events []StoredEvent `json:"events"`
	Next   *EventCursor  `json:"next,omitempty"`
}

// StoredEventMetadata is the payload-free durable event projection used by
// operator inspection. Dedupe and lease-owner credentials are also omitted;
// PayloadBytes is obtained with SQLite length(payload_json), without reading
// the payload blob.
type StoredEventMetadata struct {
	Envelope     Envelope     `json:"envelope"`
	Routing      RoutingState `json:"routing"`
	PayloadBytes int          `json:"payload_bytes"`
}

// EventMetadataPage is a newest-first payload-free event page.
type EventMetadataPage struct {
	Events []StoredEventMetadata `json:"events"`
	Next   *EventCursor          `json:"next,omitempty"`
}

// EventOperatorReader exposes payload-free inspection separately from the
// explicit exact-payload read. It is deliberately additive to EventStore so
// existing inbox implementations retain their original contract.
type EventOperatorReader interface {
	GetEventMetadata(ctx context.Context, id string) (StoredEventMetadata, error)
	ListEventMetadata(ctx context.Context, filter EventFilter) (EventMetadataPage, error)
	GetEventPayload(ctx context.Context, id string) ([]byte, error)
}

// DispatchCursor is the stable keyset position for dispatch listings.
type DispatchCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

// DispatchFilter selects dispatches in newest-first stable keyset order.
type DispatchFilter struct {
	EventID     string
	WorkflowRef string
	Status      DispatchStatus
	After       *DispatchCursor
	Limit       int
}

// DispatchPage is one keyset-paginated dispatch result.
type DispatchPage struct {
	Dispatches []Dispatch      `json:"dispatches"`
	Next       *DispatchCursor `json:"next,omitempty"`
}

// DispatchMetadata is the operator-safe durable dispatch projection.
// LeaseUntil remains observable, while the worker owner/lease token is
// structurally omitted.
type DispatchMetadata struct {
	ID               string         `json:"id"`
	EventID          string         `json:"event_id"`
	WorkflowRef      string         `json:"workflow_ref"`
	WorkflowRevision string         `json:"workflow_revision,omitempty"`
	RunID            string         `json:"run_id"`
	Status           DispatchStatus `json:"status"`
	LeaseUntil       *time.Time     `json:"lease_until,omitempty"`
	AvailableAt      time.Time      `json:"available_at"`
	Attempts         int            `json:"attempts"`
	LastError        string         `json:"last_error,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	LinkedAt         *time.Time     `json:"linked_at,omitempty"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
}

// DispatchMetadataPage is a newest-first operator-safe dispatch page.
type DispatchMetadataPage struct {
	Dispatches []DispatchMetadata `json:"dispatches"`
	Next       *DispatchCursor    `json:"next,omitempty"`
}

// DispatchOperatorReader exposes dispatch inspection without worker
// ownership credentials. It is additive to DispatchQueue.
type DispatchOperatorReader interface {
	ListDispatchMetadata(
		ctx context.Context,
		filter DispatchFilter,
	) (DispatchMetadataPage, error)
}

// DispatchOperatorGetter exposes exact dispatch inspection without widening
// the established list-reader contract.
type DispatchOperatorGetter interface {
	GetDispatchMetadata(ctx context.Context, id string) (DispatchMetadata, error)
}

// EventStore owns durable envelope ingestion and retrieval.
type EventStore interface {
	Close() error
	Insert(ctx context.Context, event Envelope) (InsertResult, error)
	Get(ctx context.Context, id string) (StoredEvent, error)
	List(ctx context.Context, filter EventFilter) (EventPage, error)
}

// RoutingQueue owns the independently leased event-routing lifecycle.
type RoutingQueue interface {
	ClaimRouting(
		ctx context.Context,
		workerLabel string,
		limit int,
		lease time.Duration,
	) ([]StoredEvent, error)
	AckRouting(ctx context.Context, id, leaseToken string) error
	NackRouting(
		ctx context.Context,
		id, leaseToken string,
		availableAt time.Time,
		detail string,
	) error
	DeadRouting(ctx context.Context, id, leaseToken, detail string) error
}

// RoutingDispatchCreator fences workflow fan-out to the live routing claim
// that authorized it. A stale router must never create new dispatches after
// another worker has reclaimed or completed the event.
type RoutingDispatchCreator interface {
	CreateDispatchForRoutingClaim(
		ctx context.Context,
		eventID, leaseToken, workflowRef string,
	) (Dispatch, bool, error)
}

// RevisionRoutingDispatchCreator atomically binds a dispatch to the exact
// workflow content revision that matched while its routing claim is live.
type RevisionRoutingDispatchCreator interface {
	CreateRevisionedDispatchForRoutingClaim(
		ctx context.Context,
		eventID, leaseToken, workflowRef, workflowRevision string,
	) (Dispatch, bool, error)
}

// RoutingLeaseRenewer is an optional capability for catalogs whose matching
// work may outlive the routing lease initially returned by ClaimRouting.
type RoutingLeaseRenewer interface {
	RenewRoutingLease(ctx context.Context, id, leaseToken string, lease time.Duration) error
}

// DispatchQueue owns durable per-workflow delivery state.
type DispatchQueue interface {
	CreateDispatch(ctx context.Context, eventID, workflowRef string) (Dispatch, bool, error)
	GetDispatch(ctx context.Context, id string) (Dispatch, error)
	ClaimDispatches(
		ctx context.Context,
		workerLabel string,
		limit int,
		lease time.Duration,
	) ([]Dispatch, error)
	LinkDispatchRun(ctx context.Context, id, leaseToken, runID string) error
	FinishDispatch(
		ctx context.Context,
		id, leaseToken string,
		status DispatchStatus,
		detail string,
	) error
	NackDispatch(
		ctx context.Context,
		id, leaseToken string,
		availableAt time.Time,
		detail string,
	) error
	ListDispatches(ctx context.Context, filter DispatchFilter) (DispatchPage, error)
}

// DispatchLeaseRenewer is an optional capability for workers whose execution
// may outlive the dispatch lease initially returned by ClaimDispatches. It is
// intentionally separate from DispatchQueue and Inbox so existing
// implementations remain source compatible.
type DispatchLeaseRenewer interface {
	RenewDispatchLease(
		ctx context.Context,
		id, leaseToken string,
		lease time.Duration,
	) error
}

// EventMaintenance owns additive replay and bounded retention.
type EventMaintenance interface {
	Replay(ctx context.Context, id string) (InsertResult, error)
	Prune(ctx context.Context, before time.Time, limit int) (int64, error)
}

// Inbox is the durable boundary consumed by ingress, routing, workflow, and
// operations layers. The concrete SQLite Store satisfies it on supported
// platforms; the portable stub returns ErrUnsupportedPlatform.
type Inbox interface {
	EventStore
	RoutingQueue
	DispatchQueue
	EventMaintenance
}

type storeOptions struct {
	now             func() time.Time
	busyTimeout     time.Duration
	additionalKeys  []string
	secretValues    []string
	maxPayloadBytes int
}

func defaultStoreOptions() storeOptions {
	return storeOptions{
		now:             time.Now,
		busyTimeout:     5 * time.Second,
		maxPayloadBytes: 1 << 20,
	}
}

// WithMaxPayloadBytes sets the maximum raw event payload size. The default is
// one MiB. Values must be positive to override the default.
func WithMaxPayloadBytes(maximum int) Option {
	return func(options *storeOptions) {
		if maximum > 0 {
			options.maxPayloadBytes = maximum
		}
	}
}

// Option customizes a durable Store.
type Option func(*storeOptions)

// WithClock installs a clock, primarily for deterministic tests.
func WithClock(now func() time.Time) Option {
	return func(options *storeOptions) {
		if now != nil {
			options.now = now
		}
	}
}

// WithBusyTimeout sets SQLite's lock wait timeout.
func WithBusyTimeout(timeout time.Duration) Option {
	return func(options *storeOptions) {
		if timeout > 0 {
			options.busyTimeout = timeout
		}
	}
}

// WithRedaction adds installation-specific sensitive keys and known secret
// values. Mandatory built-ins are always active.
func WithRedaction(additionalKeys, secretValues []string) Option {
	return func(options *storeOptions) {
		options.additionalKeys = append([]string(nil), additionalKeys...)
		options.secretValues = append([]string(nil), secretValues...)
	}
}

func optionsFrom(options []Option) storeOptions {
	resolved := defaultStoreOptions()
	for _, option := range options {
		if option != nil {
			option(&resolved)
		}
	}
	return resolved
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	return ctx.Err()
}

func validRoutingStatus(status RoutingStatus) bool {
	switch status {
	case RoutingPending, RoutingClaimed, RoutingSucceeded, RoutingDead:
		return true
	default:
		return false
	}
}

func validDispatchStatus(status DispatchStatus) bool {
	switch status {
	case DispatchPending, DispatchClaimed, DispatchRunning, DispatchSucceeded, DispatchFailed, DispatchDead:
		return true
	default:
		return false
	}
}

func terminalDispatchStatus(status DispatchStatus) bool {
	switch status {
	case DispatchSucceeded, DispatchFailed, DispatchDead:
		return true
	default:
		return false
	}
}
