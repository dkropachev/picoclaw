//go:build mipsle || netbsd || (freebsd && arm)

package eventing

import (
	"context"
	"time"
)

// Store is unavailable on targets unsupported by modernc SQLite.
type Store struct{}

var (
	_ Inbox                          = (*Store)(nil)
	_ EventOperatorReader            = (*Store)(nil)
	_ DispatchOperatorReader         = (*Store)(nil)
	_ DispatchOperatorGetter         = (*Store)(nil)
	_ RoutingDispatchCreator         = (*Store)(nil)
	_ RevisionRoutingDispatchCreator = (*Store)(nil)
	_ RoutingLeaseRenewer            = (*Store)(nil)
	_ DispatchLeaseRenewer           = (*Store)(nil)
)

func Open(context.Context, string, ...Option) (*Store, error) {
	return nil, ErrUnsupportedPlatform
}

func OpenStore(ctx context.Context, path string, options ...Option) (*Store, error) {
	return Open(ctx, path, options...)
}

func (*Store) Close() error { return nil }

func (*Store) Insert(context.Context, Envelope) (InsertResult, error) {
	return InsertResult{}, ErrUnsupportedPlatform
}

func (*Store) Get(context.Context, string) (StoredEvent, error) {
	return StoredEvent{}, ErrUnsupportedPlatform
}

func (*Store) List(context.Context, EventFilter) (EventPage, error) {
	return EventPage{}, ErrUnsupportedPlatform
}

func (s *Store) ListEvents(ctx context.Context, filter EventFilter) (EventPage, error) {
	return s.List(ctx, filter)
}

func (*Store) GetEventMetadata(context.Context, string) (StoredEventMetadata, error) {
	return StoredEventMetadata{}, ErrUnsupportedPlatform
}

func (*Store) ListEventMetadata(context.Context, EventFilter) (EventMetadataPage, error) {
	return EventMetadataPage{}, ErrUnsupportedPlatform
}

func (*Store) GetEventPayload(context.Context, string) ([]byte, error) {
	return nil, ErrUnsupportedPlatform
}

func (*Store) ClaimRouting(context.Context, string, int, time.Duration) ([]StoredEvent, error) {
	return nil, ErrUnsupportedPlatform
}

func (*Store) AckRouting(context.Context, string, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) NackRouting(context.Context, string, string, time.Time, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) DeadRouting(context.Context, string, string, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) CreateDispatchForRoutingClaim(
	context.Context,
	string,
	string,
	string,
) (Dispatch, bool, error) {
	return Dispatch{}, false, ErrUnsupportedPlatform
}

func (*Store) CreateRevisionedDispatchForRoutingClaim(
	context.Context,
	string,
	string,
	string,
	string,
) (Dispatch, bool, error) {
	return Dispatch{}, false, ErrUnsupportedPlatform
}

func (*Store) RenewRoutingLease(context.Context, string, string, time.Duration) error {
	return ErrUnsupportedPlatform
}

func (*Store) CreateDispatch(context.Context, string, string) (Dispatch, bool, error) {
	return Dispatch{}, false, ErrUnsupportedPlatform
}

func (*Store) GetDispatch(context.Context, string) (Dispatch, error) {
	return Dispatch{}, ErrUnsupportedPlatform
}

func (*Store) GetDispatchMetadata(context.Context, string) (DispatchMetadata, error) {
	return DispatchMetadata{}, ErrUnsupportedPlatform
}

func (*Store) ClaimDispatches(context.Context, string, int, time.Duration) ([]Dispatch, error) {
	return nil, ErrUnsupportedPlatform
}

func (*Store) LinkDispatchRun(context.Context, string, string, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) RenewDispatchLease(context.Context, string, string, time.Duration) error {
	return ErrUnsupportedPlatform
}

func (*Store) FinishDispatch(context.Context, string, string, DispatchStatus, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) NackDispatch(context.Context, string, string, time.Time, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) ListDispatches(context.Context, DispatchFilter) (DispatchPage, error) {
	return DispatchPage{}, ErrUnsupportedPlatform
}

func (*Store) ListDispatchMetadata(
	context.Context,
	DispatchFilter,
) (DispatchMetadataPage, error) {
	return DispatchMetadataPage{}, ErrUnsupportedPlatform
}

func (*Store) Replay(context.Context, string) (InsertResult, error) {
	return InsertResult{}, ErrUnsupportedPlatform
}

func (*Store) Prune(context.Context, time.Time, int) (int64, error) {
	return 0, ErrUnsupportedPlatform
}
