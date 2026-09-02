package repoaudit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	reviewBrokerDomain  = "repository-reviews"
	reviewBrokerVersion = 1

	reviewOperationPreflight  = "preflight"
	reviewOperationLock       = "lock"
	reviewOperationUnlock     = "unlock"
	reviewOperationRenewLease = "renew-lease"
	reviewOperationLoadState  = "load-state"
	reviewOperationSaveState  = "save-state"
	reviewOperationClock      = "clock"
)

const defaultReviewBrokerLeaseTTL = 30 * time.Second

const ReviewStoreID database.StoreID = "workspace.repository-reviews"

var reviewBrokerClient = database.RuntimeClient

type auditBrokerClientState struct {
	mu      sync.Mutex
	leaseID string
	lockKey string

	releaseErrMu sync.Mutex
	releaseErr   error
}

type reviewTarget struct {
	StoreID database.StoreID `json:"store_id"`
}

type reviewLockRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Key     string           `json:"key"`
}

type reviewLeaseRequest struct {
	StoreID database.StoreID `json:"store_id"`
	LeaseID string           `json:"lease_id"`
}

type reviewLoadRequest struct {
	StoreID    database.StoreID `json:"store_id"`
	LeaseID    string           `json:"lease_id"`
	Repository string           `json:"repository"`
}

type reviewSaveRequest struct {
	StoreID database.StoreID `json:"store_id"`
	LeaseID string           `json:"lease_id"`
	State   RepositoryState  `json:"state"`
}

type reviewReadyResponse struct {
	Ready bool `json:"ready"`
}
type reviewLeaseResponse struct {
	LeaseID        string `json:"lease_id"`
	TTLNanoSeconds int64  `json:"ttl_nanoseconds"`
}
type reviewStateResponse struct {
	State RepositoryState `json:"state"`
}
type reviewMutationResponse struct {
	Updated bool `json:"updated"`
}
type reviewClockResponse struct {
	Now time.Time `json:"now"`
}

func (s Store) brokerLock(key string) (func(), error) {
	if s.broker == nil || s.brokerState == nil {
		return nil, database.NewError(database.CodeUnavailable, "repository review broker is unavailable")
	}
	if s.brokerErr != nil {
		return nil, s.brokerErr
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, database.NewError(database.CodeInvalid, "repository review lock key is invalid")
	}
	s.brokerState.mu.Lock()
	if err := s.consumeBrokerLeaseError(); err != nil {
		s.brokerState.mu.Unlock()
		return nil, err
	}
	var response reviewLeaseResponse
	err := s.broker.CallWithOptions(
		context.Background(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationLock,
		reviewLockRequest{StoreID: s.StoreID(), Key: key}, &response,
		database.CallOptions{Mutation: true},
	)
	if err != nil || response.LeaseID == "" || response.TTLNanoSeconds <= 0 {
		s.brokerState.mu.Unlock()
		if err == nil {
			err = database.NewError(database.CodeIntegrity, "repository review lease response is invalid")
		}
		return nil, mapReviewClientError(err)
	}
	s.brokerState.leaseID = response.LeaseID
	s.brokerState.lockKey = key
	return s.newBrokerLeaseRelease(response.LeaseID, time.Duration(response.TTLNanoSeconds), func() {
		s.brokerState.leaseID, s.brokerState.lockKey = "", ""
		s.brokerState.mu.Unlock()
	}), nil
}

func (s Store) currentBrokerLease() (string, error) {
	if s.brokerErr != nil {
		return "", s.brokerErr
	}
	if s.brokerState == nil || s.brokerState.leaseID == "" {
		return "", database.NewError(database.CodeConflict, "repository review operation has no broker lease")
	}
	return s.brokerState.leaseID, nil
}

func (s Store) brokerLoadState(repository string) (RepositoryState, error) {
	leaseID, err := s.currentBrokerLease()
	if err != nil {
		return RepositoryState{}, err
	}
	var response reviewStateResponse
	err = s.broker.Call(
		context.Background(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationLoadState,
		reviewLoadRequest{
			StoreID: s.StoreID(), LeaseID: leaseID, Repository: strings.TrimSpace(repository),
		},
		&response,
	)
	return response.State, mapReviewClientError(err)
}

func (s Store) brokerSaveState(state *RepositoryState) error {
	if state == nil {
		return database.NewError(database.CodeInvalid, "repository review state is required")
	}
	leaseID, err := s.currentBrokerLease()
	if err != nil {
		return err
	}
	var response reviewMutationResponse
	err = s.broker.CallWithOptions(
		context.Background(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationSaveState,
		reviewSaveRequest{StoreID: s.StoreID(), LeaseID: leaseID, State: *state}, &response,
		database.CallOptions{Mutation: true},
	)
	if err == nil && !response.Updated {
		err = database.NewError(database.CodeIntegrity, "repository review mutation response is invalid")
	}
	return mapReviewClientError(err)
}

func (s Store) brokerClock() time.Time {
	if s.brokerErr != nil {
		return time.Time{}
	}
	var response reviewClockResponse
	err := s.broker.Call(
		context.Background(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationClock,
		reviewTarget{StoreID: s.StoreID()}, &response,
	)
	if err != nil || response.Now.IsZero() {
		return time.Time{}
	}
	return response.Now.UTC()
}

func (s Store) Preflight(ctx context.Context) error {
	if s.brokerErr != nil {
		return s.brokerErr
	}
	if s.broker == nil {
		_, release, err := s.acquireDatabase(ctx)
		if err != nil {
			return err
		}
		release()
		return nil
	}
	var response reviewReadyResponse
	err := s.broker.CallWithOptions(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationPreflight,
		reviewTarget{StoreID: s.StoreID()}, &response,
		database.CallOptions{Mutation: true},
	)
	if err == nil && !response.Ready {
		err = database.NewError(database.CodeIntegrity, "repository review readiness response is invalid")
	}
	return mapReviewClientError(err)
}

type reviewBrokerLease struct {
	key         string
	release     func()
	releaseOnce sync.Once
	timer       *time.Timer
	generation  uint64
}

func (lease *reviewBrokerLease) releaseNow() {
	if lease != nil {
		lease.releaseOnce.Do(func() {
			if lease.release != nil {
				lease.release()
			}
		})
	}
}

type reviewStoreHandler struct {
	storeID     database.StoreID
	workspace   string
	once        sync.Once
	store       Store
	err         error
	mu          sync.Mutex
	leases      map[string]*reviewBrokerLease
	closed      bool
	closeOnce   sync.Once
	closeErr    error
	leaseTTL    time.Duration
	requestGate chan struct{}
}

func newReviewStoreHandler(workspace string, storeID database.StoreID) *reviewStoreHandler {
	handler := &reviewStoreHandler{
		workspace: workspace, storeID: storeID,
		leases: make(map[string]*reviewBrokerLease), leaseTTL: defaultReviewBrokerLeaseTTL,
		requestGate: make(chan struct{}, 1),
	}
	handler.requestGate <- struct{}{}
	return handler
}

func (handler *reviewStoreHandler) open() (Store, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedReviewProviderForTests.Load() {
		return Store{}, database.NewError(
			database.CodeUnauthorized,
			"repository review broker opener requires online database fencing",
		)
	}
	handler.once.Do(func() {
		handler.store, handler.err = newRetainedReviewStore(handler.workspace)
		handler.store.storeID = handler.storeID
	})
	return handler.store, handler.err
}

func (handler *reviewStoreHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != reviewBrokerDomain || request.Version != reviewBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	gateRequest := request.Operation != reviewOperationLock &&
		request.Operation != reviewOperationAcquireNamedLease
	if gateRequest {
		select {
		case <-ctx.Done():
			return nil, database.NewError(database.CodeDeadline, "repository review request deadline was exceeded")
		case <-handler.requestGate:
		}
		defer func() { handler.requestGate <- struct{}{} }()
	}
	handler.mu.Lock()
	closed := handler.closed
	handler.mu.Unlock()
	if closed {
		return nil, database.NewError(database.CodeUnavailable, "repository review broker is closed")
	}
	switch request.Operation {
	case reviewOperationPreflight:
		var input reviewTarget
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		if _, err := handler.open(); err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewReadyResponse{Ready: true}, nil
	case reviewOperationLock:
		return handler.acquireLease(ctx, request)
	case reviewOperationUnlock:
		return handler.releaseLease(request)
	case reviewOperationRenewLease:
		return handler.renewLease(request)
	case reviewOperationLoadState:
		var input reviewLoadRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID ||
			strings.TrimSpace(input.Repository) == "" {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		lease, err := handler.authorizeLease(input.LeaseID, strings.TrimSpace(input.Repository))
		if err != nil {
			return nil, err
		}
		_ = lease
		store, err := handler.open()
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		state, err := store.load(input.Repository)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewStateResponse{State: state}, nil
	case reviewOperationSaveState:
		var input reviewSaveRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID ||
			strings.TrimSpace(input.State.Repository) == "" {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		if _, err := handler.authorizeLease(input.LeaseID, strings.TrimSpace(input.State.Repository)); err != nil {
			return nil, err
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		state := input.State
		if err := store.save(&state); err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewMutationResponse{Updated: true}, nil
	case reviewOperationAcquireNamedLease:
		return handler.acquireNamedLease(ctx, request)
	case reviewOperationClock:
		var input reviewTarget
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewClockResponse{Now: store.clock()}, nil
	default:
		return handler.handleExtended(ctx, request)
	}
}

func (handler *reviewStoreHandler) acquireLease(ctx context.Context, request database.Request) (any, error) {
	var input reviewLockRequest
	if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID || strings.TrimSpace(input.Key) == "" {
		return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
	}
	store, err := handler.open()
	if err != nil {
		return nil, mapReviewBrokerError(err)
	}
	var release func()
	for {
		release, err = store.lock(input.Key)
		if !errors.Is(err, ErrConflict) {
			break
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, mapReviewBrokerError(ctx.Err())
		case <-timer.C:
		}
	}
	if err != nil {
		return nil, mapReviewBrokerError(err)
	}
	id, err := newReviewLeaseID()
	if err != nil {
		release()
		return nil, database.NewError(database.CodeInternal, "repository review lease identity failed")
	}
	lease := &reviewBrokerLease{key: strings.TrimSpace(input.Key), release: release}
	if err := handler.registerLease(id, lease); err != nil {
		lease.releaseNow()
		return nil, err
	}
	return reviewLeaseResponse{LeaseID: id, TTLNanoSeconds: int64(handler.effectiveLeaseTTL())}, nil
}

func (handler *reviewStoreHandler) releaseLease(request database.Request) (any, error) {
	var input reviewLeaseRequest
	if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID || input.LeaseID == "" {
		return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
	}
	handler.mu.Lock()
	lease, ok := handler.leases[input.LeaseID]
	if ok {
		delete(handler.leases, input.LeaseID)
		lease.generation++
		if lease.timer != nil {
			lease.timer.Stop()
		}
	}
	handler.mu.Unlock()
	if !ok {
		return nil, database.NewError(database.CodeConflict, "repository review lease is unavailable")
	}
	lease.releaseNow()
	return reviewMutationResponse{Updated: true}, nil
}

func (handler *reviewStoreHandler) renewLease(request database.Request) (any, error) {
	var input reviewLeaseRequest
	if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID || input.LeaseID == "" {
		return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
	}
	handler.mu.Lock()
	lease := handler.leases[input.LeaseID]
	if lease == nil || handler.closed {
		handler.mu.Unlock()
		return nil, database.NewError(database.CodeConflict, "repository review lease is unavailable")
	}
	handler.armLeaseLocked(input.LeaseID, lease)
	ttl := handler.effectiveLeaseTTL()
	handler.mu.Unlock()
	return reviewLeaseResponse{LeaseID: input.LeaseID, TTLNanoSeconds: int64(ttl)}, nil
}

func (handler *reviewStoreHandler) authorizeLease(id, key string) (*reviewBrokerLease, error) {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	lease, ok := handler.leases[id]
	if !ok || lease.key != key {
		return nil, database.NewError(database.CodeConflict, "repository review lease is unavailable")
	}
	return lease, nil
}

func (handler *reviewStoreHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		handler.closed = true
		leases := handler.leases
		handler.leases = make(map[string]*reviewBrokerLease)
		for _, lease := range leases {
			lease.generation++
			if lease.timer != nil {
				lease.timer.Stop()
			}
		}
		handler.mu.Unlock()
		for _, lease := range leases {
			lease.releaseNow()
		}
		handler.closeErr = handler.store.Close()
	})
	return handler.closeErr
}

func (handler *reviewStoreHandler) effectiveLeaseTTL() time.Duration {
	if handler == nil || handler.leaseTTL <= 0 {
		return defaultReviewBrokerLeaseTTL
	}
	return handler.leaseTTL
}

func (handler *reviewStoreHandler) registerLease(id string, lease *reviewBrokerLease) error {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed {
		return database.NewError(database.CodeUnavailable, "repository review broker is closed")
	}
	if handler.leases[id] != nil {
		return database.NewError(database.CodeConflict, "repository review lease identity conflicts")
	}
	handler.leases[id] = lease
	handler.armLeaseLocked(id, lease)
	return nil
}

func (handler *reviewStoreHandler) armLeaseLocked(id string, lease *reviewBrokerLease) {
	lease.generation++
	generation := lease.generation
	if lease.timer != nil {
		lease.timer.Stop()
	}
	lease.timer = time.AfterFunc(handler.effectiveLeaseTTL(), func() {
		handler.expireLease(id, lease, generation)
	})
}

func (handler *reviewStoreHandler) expireLease(id string, lease *reviewBrokerLease, generation uint64) {
	handler.mu.Lock()
	if handler.leases[id] != lease || lease.generation != generation {
		handler.mu.Unlock()
		return
	}
	delete(handler.leases, id)
	lease.generation++
	handler.mu.Unlock()
	lease.releaseNow()
}

func newReviewLeaseID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func mapReviewBrokerError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return database.NewError(database.CodeDeadline, "repository review request deadline was exceeded")
	case errors.Is(err, ErrProfileAssigned):
		return database.NewError(database.CodeAlreadyExists, "repository review profile is assigned")
	case errors.Is(err, ErrProfileActive), errors.Is(err, ErrAutomationActive):
		return database.NewError(database.CodeUnsupported, "repository review record is active")
	case errors.Is(err, ErrConflict), errors.Is(err, ErrHistoricalDeduplicationInProgress),
		errors.Is(err, ErrAutomationControllerLocked):
		return database.NewError(database.CodeConflict, "repository review state changed concurrently")
	case errors.Is(err, os.ErrNotExist):
		return database.NewError(database.CodeNotFound, "repository review record was not found")
	case errors.Is(err, ErrInvalidPlan), errors.Is(err, ErrInvalidProfile), errors.Is(err, ErrInvalidAutomation):
		return database.NewError(database.CodeInvalid, "repository review request is invalid")
	case errors.Is(err, sqlitestore.ErrTooNew):
		return database.NewError(database.CodeUnsupported, "repository review schema is newer than supported")
	case errors.Is(err, sqlitestore.ErrInvalidSchema), errors.Is(err, sqlitestore.ErrIntegrity):
		return database.NewError(database.CodeIntegrity, "repository review integrity validation failed")
	default:
		return database.NewError(database.CodeInternal, "repository review operation failed")
	}
}

func mapReviewClientError(err error) error {
	switch database.CodeOf(err) {
	case database.CodeConflict:
		return ErrConflict
	case database.CodeNotFound:
		return os.ErrNotExist
	case database.CodeInvalid:
		return ErrInvalidPlan
	default:
		return err
	}
}
