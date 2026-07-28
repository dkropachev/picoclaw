package eventing

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const schemaVersion = 1

const (
	maxWorkflowRefLength = 1024
	maxErrorDetailBytes  = 16 << 10
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
	ID          string         `json:"id"`
	EventID     string         `json:"event_id"`
	WorkflowRef string         `json:"workflow_ref"`
	RunID       string         `json:"run_id"`
	Status      DispatchStatus `json:"status"`
	LeaseToken  string         `json:"lease_token,omitempty"`
	LeaseUntil  *time.Time     `json:"lease_until,omitempty"`
	AvailableAt time.Time      `json:"available_at"`
	Attempts    int            `json:"attempts"`
	LastError   string         `json:"last_error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	LinkedAt    *time.Time     `json:"linked_at,omitempty"`
	FinishedAt  *time.Time     `json:"finished_at,omitempty"`
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

// Inbox is the durable boundary consumed by ingress, routing, workflow, and
// operations layers. The concrete SQLite Store satisfies it on supported
// platforms; the portable stub returns ErrUnsupportedPlatform.
type Inbox interface {
	Close() error
	Insert(context.Context, Envelope) (InsertResult, error)
	Get(context.Context, string) (StoredEvent, error)
	List(context.Context, EventFilter) (EventPage, error)
	ClaimRouting(context.Context, string, int, time.Duration) ([]StoredEvent, error)
	AckRouting(context.Context, string, string) error
	NackRouting(context.Context, string, string, time.Time, string) error
	DeadRouting(context.Context, string, string, string) error
	CreateDispatch(context.Context, string, string) (Dispatch, bool, error)
	GetDispatch(context.Context, string) (Dispatch, error)
	ClaimDispatches(context.Context, string, int, time.Duration) ([]Dispatch, error)
	LinkDispatchRun(context.Context, string, string, string) error
	FinishDispatch(context.Context, string, string, DispatchStatus, string) error
	NackDispatch(context.Context, string, string, time.Time, string) error
	ListDispatches(context.Context, DispatchFilter) (DispatchPage, error)
	Replay(context.Context, string) (InsertResult, error)
	Prune(context.Context, time.Time, int) (int64, error)
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
