package wecom

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	wecomBrokerDomain  = "channel-wecom"
	wecomBrokerVersion = 1

	wecomBrokerOperationPreflight = "preflight"
	wecomBrokerOperationPut       = "put-route"
	wecomBrokerOperationGet       = "get-route"
	wecomBrokerOperationDelete    = "delete-route"
)

const WeComStoreID database.StoreID = "channel.wecom"

var wecomBrokerClient = database.RuntimeClient

type wecomBrokerTarget struct {
	StoreID database.StoreID `json:"store_id"`
}

type wecomBrokerPutRequest struct {
	StoreID        database.StoreID `json:"store_id"`
	ChatID         string           `json:"chat_id"`
	RequestID      string           `json:"request_id"`
	ChatType       uint32           `json:"chat_type"`
	TTLNanoSeconds int64            `json:"ttl_nanoseconds"`
}

type wecomBrokerChatRequest struct {
	StoreID database.StoreID `json:"store_id"`
	ChatID  string           `json:"chat_id"`
}

type wecomBrokerReadyResponse struct {
	Ready bool `json:"ready"`
}

type wecomBrokerMutationResponse struct {
	Updated bool `json:"updated"`
}

type wecomBrokerGetResponse struct {
	Found bool       `json:"found"`
	Route wecomRoute `json:"route"`
}

func (s *reqIDStore) brokerPreflight(ctx context.Context) error {
	if s == nil || s.broker == nil || s.StoreID() != WeComStoreID {
		return database.NewError(database.CodeUnavailable, "WeCom route broker is unavailable")
	}
	var response wecomBrokerReadyResponse
	err := s.broker.CallWithOptions(
		ctx, wecomBrokerDomain, wecomBrokerVersion, wecomBrokerOperationPreflight,
		wecomBrokerTarget{StoreID: s.StoreID()}, &response,
		database.CallOptions{Mutation: true},
	)
	if err != nil {
		return err
	}
	if !response.Ready {
		return database.NewError(database.CodeIntegrity, "WeCom route readiness response is invalid")
	}
	return nil
}

func (s *reqIDStore) brokerPut(
	ctx context.Context,
	chatID, requestID string,
	chatType uint32,
	ttl time.Duration,
) error {
	if s == nil || s.broker == nil || s.StoreID() != WeComStoreID {
		return database.NewError(database.CodeUnavailable, "WeCom route broker is unavailable")
	}
	var response wecomBrokerMutationResponse
	err := s.broker.CallWithOptions(
		ctx, wecomBrokerDomain, wecomBrokerVersion, wecomBrokerOperationPut,
		wecomBrokerPutRequest{
			StoreID: s.StoreID(), ChatID: chatID, RequestID: requestID,
			ChatType: chatType, TTLNanoSeconds: int64(ttl),
		},
		&response,
		database.CallOptions{Mutation: true},
	)
	if err != nil {
		return err
	}
	if !response.Updated {
		return database.NewError(database.CodeIntegrity, "WeCom route mutation response is invalid")
	}
	return nil
}

func (s *reqIDStore) brokerGet(
	ctx context.Context,
	chatID string,
) (wecomRoute, bool, error) {
	if s == nil || s.broker == nil || s.StoreID() != WeComStoreID {
		return wecomRoute{}, false, database.NewError(
			database.CodeUnavailable, "WeCom route broker is unavailable",
		)
	}
	var response wecomBrokerGetResponse
	err := s.broker.Call(
		ctx, wecomBrokerDomain, wecomBrokerVersion, wecomBrokerOperationGet,
		wecomBrokerChatRequest{StoreID: s.StoreID(), ChatID: chatID}, &response,
	)
	if err != nil {
		return wecomRoute{}, false, err
	}
	if !response.Found {
		return wecomRoute{}, false, nil
	}
	if err := validateWecomRoute(response.Route); err != nil || response.Route.ChatID != chatID {
		return wecomRoute{}, false, database.NewError(
			database.CodeIntegrity, "WeCom route response is invalid",
		)
	}
	return response.Route, true, nil
}

func (s *reqIDStore) brokerDelete(ctx context.Context, chatID string) error {
	if s == nil || s.broker == nil || s.StoreID() != WeComStoreID {
		return database.NewError(database.CodeUnavailable, "WeCom route broker is unavailable")
	}
	var response wecomBrokerMutationResponse
	err := s.broker.CallWithOptions(
		ctx, wecomBrokerDomain, wecomBrokerVersion, wecomBrokerOperationDelete,
		wecomBrokerChatRequest{StoreID: s.StoreID(), ChatID: chatID}, &response,
		database.CallOptions{Mutation: true},
	)
	if err != nil {
		return err
	}
	if !response.Updated {
		return database.NewError(database.CodeIntegrity, "WeCom route mutation response is invalid")
	}
	return nil
}

// BrokerHandler owns the supervisor-local WeCom request-route store and holds
// its stable pool until Close.
type BrokerHandler struct {
	home string

	once  sync.Once
	store *reqIDStore
	err   error
}

func NewBrokerHandler(home string) *BrokerHandler {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return &BrokerHandler{err: database.NewError(
			database.CodeUnauthorized,
			"WeCom broker handler requires database broker authority",
		)}
	}
	return &BrokerHandler{home: home}
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != wecomBrokerDomain || request.Version != wecomBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	switch request.Operation {
	case wecomBrokerOperationPreflight:
		var input wecomBrokerTarget
		if err := request.DecodePayload(&input); err != nil || input.StoreID != WeComStoreID {
			return nil, database.NewError(database.CodeInvalid, "WeCom route request is invalid")
		}
		if _, err := handler.open(); err != nil {
			return nil, mapWecomBrokerError(err)
		}
		return wecomBrokerReadyResponse{Ready: true}, nil
	case wecomBrokerOperationPut:
		var input wecomBrokerPutRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != WeComStoreID ||
			validateWecomRouteValue(input.ChatID) != nil ||
			validateWecomRouteValue(input.RequestID) != nil || input.RequestID == "" {
			return nil, database.NewError(database.CodeInvalid, "WeCom route request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapWecomBrokerError(err)
		}
		expires := store.now().UTC().Add(time.Duration(input.TTLNanoSeconds))
		if _, _, err := wecomTimestampValues(expires); err != nil {
			return nil, database.NewError(database.CodeInvalid, "WeCom route TTL is invalid")
		}
		if err := store.putLocal(
			ctx, input.ChatID, input.RequestID, input.ChatType, time.Duration(input.TTLNanoSeconds),
		); err != nil {
			return nil, mapWecomBrokerError(err)
		}
		return wecomBrokerMutationResponse{Updated: true}, nil
	case wecomBrokerOperationGet:
		var input wecomBrokerChatRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != WeComStoreID ||
			validateWecomRouteValue(input.ChatID) != nil {
			return nil, database.NewError(database.CodeInvalid, "WeCom route request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapWecomBrokerError(err)
		}
		route, found, err := store.getLocal(ctx, input.ChatID, false)
		if err != nil {
			return nil, mapWecomBrokerError(err)
		}
		return wecomBrokerGetResponse{Found: found, Route: route}, nil
	case wecomBrokerOperationDelete:
		var input wecomBrokerChatRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != WeComStoreID ||
			validateWecomRouteValue(input.ChatID) != nil {
			return nil, database.NewError(database.CodeInvalid, "WeCom route request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapWecomBrokerError(err)
		}
		if err := store.deleteLocal(ctx, input.ChatID); err != nil {
			return nil, mapWecomBrokerError(err)
		}
		return wecomBrokerMutationResponse{Updated: true}, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "WeCom route operation is unsupported")
	}
}

func (handler *BrokerHandler) open() (*reqIDStore, error) {
	if handler == nil {
		return nil, errors.New("WeCom route handler is unavailable")
	}
	if handler.err != nil {
		return nil, handler.err
	}
	handler.once.Do(func() {
		handler.store, handler.err = newRetainedReqIDStore(handler.home)
	})
	return handler.store, handler.err
}

func (handler *BrokerHandler) Close() error {
	if handler == nil || handler.store == nil {
		return nil
	}
	return handler.store.Close()
}

func mapWecomBrokerError(err error) error {
	if err == nil {
		return nil
	}
	var structured *database.Error
	if errors.As(err, &structured) {
		return database.NewError(structured.Code, structured.Message)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return database.NewError(database.CodeDeadline, "WeCom route request deadline was exceeded")
	case errors.Is(err, sqlitestore.ErrTooNew):
		return database.NewError(database.CodeUnsupported, "WeCom route schema is newer than supported")
	case errors.Is(err, sqlitestore.ErrInvalidSchema), errors.Is(err, sqlitestore.ErrIntegrity):
		return database.NewError(database.CodeIntegrity, "WeCom route integrity validation failed")
	case errors.Is(err, sql.ErrNoRows):
		return database.NewError(database.CodeNotFound, "WeCom route was not found")
	case errors.Is(err, os.ErrPermission):
		return database.NewError(database.CodeUnavailable, "WeCom route store is unavailable")
	default:
		return database.NewError(database.CodeInternal, "WeCom route operation failed")
	}
}

// RunOfflineDatabaseMigration upgrades the trusted WeCom store under the
// migration command's exclusive home fence.
func RunOfflineDatabaseMigration(home string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"WeCom migration requires the exclusive database fence",
		)
	}
	store, err := newRetainedReqIDStore(home)
	if err != nil {
		return err
	}
	return store.Close()
}

var _ database.Handler = (*BrokerHandler)(nil)
