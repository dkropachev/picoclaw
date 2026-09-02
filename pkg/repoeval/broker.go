package repoeval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	evaluationBrokerDomain   = "repository-evaluations"
	evaluationBrokerVersion  = 1
	evaluationBrokerPageSize = 2

	evaluationOperationCreate     = "create"
	evaluationOperationGet        = "get"
	evaluationOperationList       = "list"
	evaluationOperationUpdate     = "update"
	evaluationOperationDelete     = "delete"
	evaluationOperationBulkDelete = "bulk-delete"
	evaluationOperationLock       = "lock-controller"
	evaluationOperationUnlock     = "unlock-controller"
	evaluationOperationRenewLease = "renew-lease"
	evaluationOperationPreflight  = "preflight"
)

const defaultEvaluationBrokerLeaseTTL = 30 * time.Second

const EvaluationStoreID database.StoreID = "workspace.repository-evaluations"

var evaluationBrokerClient = database.RuntimeClient

type evaluationTarget struct {
	StoreID database.StoreID `json:"store_id"`
}

type evaluationCreateRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Input   CreateRequest    `json:"input"`
}

type evaluationIDRequest struct {
	StoreID database.StoreID `json:"store_id"`
	ID      string           `json:"id"`
}

type evaluationUpdateRequest struct {
	StoreID         database.StoreID `json:"store_id"`
	ID              string           `json:"id"`
	ExpectedVersion int64            `json:"expected_version"`
	Candidate       Evaluation       `json:"candidate"`
}

type evaluationDeleteRequest struct {
	StoreID         database.StoreID `json:"store_id"`
	ID              string           `json:"id"`
	ExpectedVersion int64            `json:"expected_version"`
}

type evaluationBulkDeleteRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Items   []BulkDeleteItem `json:"items"`
}

type evaluationListRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
}

type evaluationLeaseRequest struct {
	StoreID database.StoreID `json:"store_id"`
	LeaseID string           `json:"lease_id"`
}

type evaluationResponse struct {
	Evaluation Evaluation `json:"evaluation"`
}

type evaluationGetResponse struct {
	Found      bool       `json:"found"`
	Evaluation Evaluation `json:"evaluation"`
}

type evaluationListResponse struct {
	Items []Evaluation `json:"items"`
	Done  bool         `json:"done"`
}

type evaluationBulkDeleteResponse struct {
	Result BulkDeleteResult `json:"result"`
}

type evaluationMutationResponse struct {
	Updated bool `json:"updated"`
}

type evaluationLeaseResponse struct {
	LeaseID        string `json:"lease_id"`
	TTLNanoSeconds int64  `json:"ttl_nanoseconds"`
}

func (s Store) brokerCreate(ctx context.Context, input CreateRequest) (Evaluation, error) {
	if s.brokerErr != nil {
		return Evaluation{}, s.brokerErr
	}
	var response evaluationResponse
	err := s.broker.CallWithOptions(
		ctx, evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationCreate,
		evaluationCreateRequest{StoreID: s.StoreID(), Input: input}, &response,
		database.CallOptions{Mutation: true},
	)
	return response.Evaluation, mapEvaluationClientError(err)
}

func (s Store) brokerGet(ctx context.Context, id string) (Evaluation, bool, error) {
	if s.brokerErr != nil {
		return Evaluation{}, false, s.brokerErr
	}
	var response evaluationGetResponse
	err := s.broker.Call(
		ctx, evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationGet,
		evaluationIDRequest{StoreID: s.StoreID(), ID: id}, &response,
	)
	return response.Evaluation, response.Found, mapEvaluationClientError(err)
}

func (s Store) brokerList(ctx context.Context) ([]Evaluation, error) {
	if s.brokerErr != nil {
		return nil, s.brokerErr
	}
	items := make([]Evaluation, 0)
	for offset := 0; ; offset += evaluationBrokerPageSize {
		var response evaluationListResponse
		err := s.broker.Call(
			ctx, evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationList,
			evaluationListRequest{
				StoreID: s.StoreID(), Offset: offset, Limit: evaluationBrokerPageSize,
			},
			&response,
		)
		if err != nil {
			return nil, mapEvaluationClientError(err)
		}
		if len(response.Items) > evaluationBrokerPageSize || len(items)+len(response.Items) > maxEvaluations {
			return nil, database.NewError(database.CodeIntegrity, "evaluation list response is invalid")
		}
		items = append(items, response.Items...)
		if response.Done {
			return items, nil
		}
		if len(response.Items) != evaluationBrokerPageSize {
			return nil, database.NewError(database.CodeIntegrity, "evaluation list response is invalid")
		}
	}
}

func (s Store) brokerUpdate(
	ctx context.Context,
	id string,
	expectedVersion int64,
	mutate func(*Evaluation) error,
) (Evaluation, error) {
	if s.brokerErr != nil {
		return Evaluation{}, s.brokerErr
	}
	if mutate == nil {
		return Evaluation{}, ErrInvalidEvaluation
	}
	current, found, err := s.brokerGet(ctx, id)
	if err != nil {
		return Evaluation{}, err
	}
	if !found {
		return Evaluation{}, os.ErrNotExist
	}
	if expectedVersion < 1 || current.Version != expectedVersion {
		return Evaluation{}, ErrConflict
	}
	candidate := Clone(current)
	if mutateErr := mutate(&candidate); mutateErr != nil {
		return Evaluation{}, mutateErr
	}
	var response evaluationResponse
	err = s.broker.CallWithOptions(
		ctx, evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationUpdate,
		evaluationUpdateRequest{
			StoreID: s.StoreID(), ID: id, ExpectedVersion: expectedVersion, Candidate: candidate,
		},
		&response, database.CallOptions{Mutation: true},
	)
	return response.Evaluation, mapEvaluationMutationClientError(err)
}

func (s Store) brokerDelete(ctx context.Context, id string, expectedVersion int64) error {
	if s.brokerErr != nil {
		return s.brokerErr
	}
	var response evaluationMutationResponse
	err := s.broker.CallWithOptions(
		ctx, evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationDelete,
		evaluationDeleteRequest{
			StoreID: s.StoreID(), ID: id, ExpectedVersion: expectedVersion,
		},
		&response, database.CallOptions{Mutation: true},
	)
	if err == nil && !response.Updated {
		return database.NewError(database.CodeIntegrity, "evaluation mutation response is invalid")
	}
	return mapEvaluationMutationClientError(err)
}

func (s Store) brokerBulkDelete(ctx context.Context, items []BulkDeleteItem) (BulkDeleteResult, error) {
	if s.brokerErr != nil {
		return BulkDeleteResult{}, s.brokerErr
	}
	var response evaluationBulkDeleteResponse
	err := s.broker.CallWithOptions(
		ctx, evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationBulkDelete,
		evaluationBulkDeleteRequest{StoreID: s.StoreID(), Items: items}, &response,
		database.CallOptions{Mutation: true},
	)
	return response.Result, mapEvaluationClientError(err)
}

func (s Store) brokerLockController() (func(), error) {
	if s.brokerErr != nil {
		return nil, s.brokerErr
	}
	if err := s.consumeBrokerLeaseError(); err != nil {
		return nil, err
	}
	var response evaluationLeaseResponse
	err := s.broker.CallWithOptions(
		context.Background(), evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationLock,
		evaluationTarget{StoreID: s.StoreID()}, &response,
		database.CallOptions{Mutation: true},
	)
	if err != nil {
		if database.CodeOf(err) == database.CodeConflict {
			return nil, ErrControllerLocked
		}
		return nil, mapEvaluationClientError(err)
	}
	if response.LeaseID == "" || response.TTLNanoSeconds <= 0 {
		return nil, database.NewError(database.CodeIntegrity, "evaluation lease response is invalid")
	}
	return s.newBrokerLeaseRelease(response.LeaseID, time.Duration(response.TTLNanoSeconds)), nil
}

// Preflight verifies the typed broker/local store is available before review or
// evaluation automation persists a running transition.
func (s Store) Preflight(ctx context.Context) error {
	if s.brokerErr != nil {
		return s.brokerErr
	}
	if s.broker == nil {
		_, release, err := s.acquire(ctx)
		if err != nil {
			return err
		}
		release()
		return nil
	}
	var response evaluationMutationResponse
	err := s.broker.CallWithOptions(
		ctx, evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationPreflight,
		evaluationTarget{StoreID: s.StoreID()}, &response,
		database.CallOptions{Mutation: true},
	)
	return mapEvaluationClientError(err)
}

type evaluationLease struct {
	release     func()
	releaseOnce sync.Once
	timer       *time.Timer
	generation  uint64
}

func (lease *evaluationLease) releaseNow() {
	if lease != nil {
		lease.releaseOnce.Do(func() {
			if lease.release != nil {
				lease.release()
			}
		})
	}
}

type evaluationStoreHandler struct {
	storeID     database.StoreID
	workspace   string
	once        sync.Once
	store       Store
	err         error
	mu          sync.Mutex
	leases      map[string]*evaluationLease
	closed      bool
	closeOnce   sync.Once
	closeErr    error
	leaseTTL    time.Duration
	requestGate chan struct{}
}

func newEvaluationStoreHandler(workspace string, storeID database.StoreID) *evaluationStoreHandler {
	handler := &evaluationStoreHandler{
		workspace: workspace, storeID: storeID,
		leases: make(map[string]*evaluationLease), leaseTTL: defaultEvaluationBrokerLeaseTTL,
		requestGate: make(chan struct{}, 1),
	}
	handler.requestGate <- struct{}{}
	return handler
}

func (handler *evaluationStoreHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != evaluationBrokerDomain || request.Version != evaluationBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, database.NewError(database.CodeDeadline, "evaluation request deadline was exceeded")
	case <-handler.requestGate:
	}
	defer func() { handler.requestGate <- struct{}{} }()
	handler.mu.Lock()
	closed := handler.closed
	handler.mu.Unlock()
	if closed {
		return nil, database.NewError(database.CodeUnavailable, "evaluation broker is closed")
	}
	switch request.Operation {
	case evaluationOperationPreflight:
		var input evaluationTarget
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "evaluation request is invalid")
		}
		if _, err := handler.open(); err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		return evaluationMutationResponse{Updated: true}, nil
	case evaluationOperationCreate:
		var input evaluationCreateRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "evaluation request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		value, err := store.Create(ctx, input.Input)
		if err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		return evaluationResponse{Evaluation: value}, nil
	case evaluationOperationGet:
		var input evaluationIDRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "evaluation request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		value, found, err := store.Get(ctx, input.ID)
		if err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		return evaluationGetResponse{Found: found, Evaluation: value}, nil
	case evaluationOperationList:
		var input evaluationListRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID ||
			input.Offset < 0 || input.Limit < 1 || input.Limit > evaluationBrokerPageSize {
			return nil, database.NewError(database.CodeInvalid, "evaluation request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		items, err := store.list(ctx, maxEvaluations)
		if err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		if input.Offset >= len(items) {
			return evaluationListResponse{Items: []Evaluation{}, Done: true}, nil
		}
		end := input.Offset + input.Limit
		if end > len(items) {
			end = len(items)
		}
		return evaluationListResponse{Items: items[input.Offset:end], Done: end == len(items)}, nil
	case evaluationOperationUpdate:
		var input evaluationUpdateRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "evaluation request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		value, err := store.Update(ctx, input.ID, input.ExpectedVersion, func(value *Evaluation) error {
			*value = Clone(input.Candidate)
			return nil
		})
		if err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		return evaluationResponse{Evaluation: value}, nil
	case evaluationOperationDelete:
		var input evaluationDeleteRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "evaluation request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		if err := store.Delete(ctx, input.ID, input.ExpectedVersion); err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		return evaluationMutationResponse{Updated: true}, nil
	case evaluationOperationBulkDelete:
		var input evaluationBulkDeleteRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "evaluation request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		result, err := store.BulkDelete(ctx, input.Items)
		if err != nil {
			return nil, mapEvaluationBrokerError(err)
		}
		return evaluationBulkDeleteResponse{Result: result}, nil
	case evaluationOperationLock:
		return handler.lockController(request)
	case evaluationOperationUnlock:
		return handler.unlockController(request)
	case evaluationOperationRenewLease:
		return handler.renewLease(request)
	default:
		return nil, database.NewError(database.CodeUnsupported, "evaluation operation is unsupported")
	}
}

func (handler *evaluationStoreHandler) open() (Store, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedEvaluationProviderForTests.Load() {
		return Store{}, database.NewError(
			database.CodeUnauthorized,
			"repository evaluation broker opener requires online database fencing",
		)
	}
	handler.once.Do(func() {
		handler.store, handler.err = newRetainedEvaluationStore(handler.workspace)
		handler.store.storeID = handler.storeID
	})
	return handler.store, handler.err
}

func (handler *evaluationStoreHandler) lockController(request database.Request) (any, error) {
	var input evaluationTarget
	if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
		return nil, database.NewError(database.CodeInvalid, "evaluation request is invalid")
	}
	store, err := handler.open()
	if err != nil {
		return nil, mapEvaluationBrokerError(err)
	}
	release, err := store.LockController()
	if err != nil {
		return nil, mapEvaluationBrokerError(err)
	}
	id, err := newEvaluationLeaseID()
	if err != nil {
		release()
		return nil, database.NewError(database.CodeInternal, "evaluation lease identity failed")
	}
	lease := &evaluationLease{release: release}
	if err := handler.registerLease(id, lease); err != nil {
		lease.releaseNow()
		return nil, err
	}
	return evaluationLeaseResponse{
		LeaseID: id, TTLNanoSeconds: int64(handler.effectiveLeaseTTL()),
	}, nil
}

func (handler *evaluationStoreHandler) unlockController(request database.Request) (any, error) {
	var input evaluationLeaseRequest
	if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID || input.LeaseID == "" {
		return nil, database.NewError(database.CodeInvalid, "evaluation request is invalid")
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
		return nil, database.NewError(database.CodeConflict, "evaluation lease is unavailable")
	}
	lease.releaseNow()
	return evaluationMutationResponse{Updated: true}, nil
}

func (handler *evaluationStoreHandler) renewLease(request database.Request) (any, error) {
	var input evaluationLeaseRequest
	if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID || input.LeaseID == "" {
		return nil, database.NewError(database.CodeInvalid, "evaluation request is invalid")
	}
	handler.mu.Lock()
	lease := handler.leases[input.LeaseID]
	if lease == nil || handler.closed {
		handler.mu.Unlock()
		return nil, database.NewError(database.CodeConflict, "evaluation lease is unavailable")
	}
	handler.armLeaseLocked(input.LeaseID, lease)
	ttl := handler.effectiveLeaseTTL()
	handler.mu.Unlock()
	return evaluationLeaseResponse{LeaseID: input.LeaseID, TTLNanoSeconds: int64(ttl)}, nil
}

func (handler *evaluationStoreHandler) effectiveLeaseTTL() time.Duration {
	if handler == nil || handler.leaseTTL <= 0 {
		return defaultEvaluationBrokerLeaseTTL
	}
	return handler.leaseTTL
}

func (handler *evaluationStoreHandler) registerLease(id string, lease *evaluationLease) error {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed {
		return database.NewError(database.CodeUnavailable, "evaluation broker is closed")
	}
	if handler.leases[id] != nil {
		return database.NewError(database.CodeConflict, "evaluation lease identity conflicts")
	}
	handler.leases[id] = lease
	handler.armLeaseLocked(id, lease)
	return nil
}

func (handler *evaluationStoreHandler) armLeaseLocked(id string, lease *evaluationLease) {
	lease.generation++
	generation := lease.generation
	if lease.timer != nil {
		lease.timer.Stop()
	}
	lease.timer = time.AfterFunc(handler.effectiveLeaseTTL(), func() {
		handler.expireLease(id, lease, generation)
	})
}

func (handler *evaluationStoreHandler) expireLease(id string, lease *evaluationLease, generation uint64) {
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

func (handler *evaluationStoreHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		handler.closed = true
		leases := handler.leases
		handler.leases = make(map[string]*evaluationLease)
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

func newEvaluationLeaseID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func mapEvaluationBrokerError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return database.NewError(database.CodeDeadline, "evaluation request deadline was exceeded")
	case errors.Is(err, ErrConflict), errors.Is(err, ErrControllerLocked):
		return database.NewError(database.CodeConflict, "evaluation changed concurrently")
	case errors.Is(err, ErrInvalidTransition):
		return database.NewError(database.CodeUnsupported, "evaluation transition is unsupported")
	case errors.Is(err, ErrInvalidEvaluation):
		return database.NewError(database.CodeInvalid, "evaluation request is invalid")
	case errors.Is(err, os.ErrNotExist):
		return database.NewError(database.CodeNotFound, "evaluation was not found")
	case errors.Is(err, sqlitestore.ErrTooNew):
		return database.NewError(database.CodeUnsupported, "evaluation schema is newer than supported")
	case errors.Is(err, sqlitestore.ErrInvalidSchema), errors.Is(err, sqlitestore.ErrIntegrity):
		return database.NewError(database.CodeIntegrity, "evaluation integrity validation failed")
	default:
		return database.NewError(database.CodeInternal, "evaluation operation failed")
	}
}

func mapEvaluationClientError(err error) error {
	switch database.CodeOf(err) {
	case database.CodeConflict:
		return ErrConflict
	case database.CodeInvalid:
		return ErrInvalidEvaluation
	case database.CodeNotFound:
		return os.ErrNotExist
	default:
		return err
	}
}

func mapEvaluationMutationClientError(err error) error {
	if database.CodeOf(err) == database.CodeUnsupported {
		return ErrInvalidTransition
	}
	return mapEvaluationClientError(err)
}
