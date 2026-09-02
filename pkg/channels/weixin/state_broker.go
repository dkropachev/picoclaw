package weixin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	weixinBrokerDomain  = "channel-weixin"
	weixinBrokerVersion = 1

	weixinBrokerOperationPreflight     = "preflight"
	weixinBrokerOperationLoadCursor    = "load-cursor"
	weixinBrokerOperationSaveCursor    = "save-cursor"
	weixinBrokerOperationLoadTokens    = "load-tokens"
	weixinBrokerOperationReplaceTokens = "replace-tokens"
	weixinBrokerOperationSaveToken     = "save-token"
	weixinBrokerTokenPageSize          = 64
)

const WeixinStoreID database.StoreID = "channel.weixin"

var weixinBrokerClient = database.RuntimeClient

type weixinBrokerTarget struct {
	StoreID database.StoreID `json:"store_id"`
}

type weixinBrokerAccountRequest struct {
	StoreID    database.StoreID `json:"store_id"`
	AccountKey string           `json:"account_key"`
}

type weixinBrokerTokenPageRequest struct {
	StoreID     database.StoreID `json:"store_id"`
	AccountKey  string           `json:"account_key"`
	AfterUserID string           `json:"after_user_id,omitempty"`
	Limit       int              `json:"limit"`
}

type weixinBrokerCursorRequest struct {
	StoreID    database.StoreID `json:"store_id"`
	AccountKey string           `json:"account_key"`
	Cursor     string           `json:"cursor"`
}

type weixinBrokerTokensRequest struct {
	StoreID    database.StoreID  `json:"store_id"`
	AccountKey string            `json:"account_key"`
	Tokens     map[string]string `json:"tokens"`
}

type weixinBrokerTokenRequest struct {
	StoreID    database.StoreID `json:"store_id"`
	AccountKey string           `json:"account_key"`
	UserID     string           `json:"user_id"`
	Token      string           `json:"token"`
}

type weixinBrokerReadyResponse struct {
	Ready bool `json:"ready"`
}

type weixinBrokerCursorResponse struct {
	Cursor string `json:"cursor"`
}

type weixinBrokerTokensResponse struct {
	Tokens    map[string]string `json:"tokens"`
	NextAfter string            `json:"next_after,omitempty"`
}

type weixinBrokerMutationResponse struct {
	Updated bool `json:"updated"`
}

func newBrokerWeixinStateStore(
	locator, kind string,
	client *database.Client,
) (*weixinStateStore, error) {
	if client == nil {
		return nil, database.NewError(database.CodeUnavailable, "Weixin state broker is unavailable")
	}
	accountKey := "default"
	base := strings.TrimSuffix(filepath.Base(locator), filepath.Ext(locator))
	directory := filepath.Base(filepath.Dir(locator))
	if directory == "sync" && kind == weixinStateKindCursor ||
		directory == "context-tokens" && kind == weixinStateKindTokens {
		if !validWeixinAccountKey(base) {
			return nil, database.NewError(database.CodeInvalid, "Weixin account identity is invalid")
		}
		accountKey = base
	}
	return &weixinStateStore{
		accountKey: accountKey,
		legacyKind: kind,
		now:        time.Now,
		broker:     client,
		storeID:    WeixinStoreID,
	}, nil
}

func (s *weixinStateStore) brokerPreflight(ctx context.Context) error {
	if s == nil || s.broker == nil || s.StoreID() != WeixinStoreID {
		return database.NewError(database.CodeUnavailable, "Weixin state broker is unavailable")
	}
	var response weixinBrokerReadyResponse
	err := s.broker.CallWithOptions(
		ctx, weixinBrokerDomain, weixinBrokerVersion, weixinBrokerOperationPreflight,
		weixinBrokerTarget{StoreID: s.StoreID()}, &response,
		database.CallOptions{Mutation: true},
	)
	if err != nil {
		return err
	}
	if !response.Ready {
		return database.NewError(database.CodeIntegrity, "Weixin state readiness response is invalid")
	}
	return nil
}

func (s *weixinStateStore) brokerLoadCursor(ctx context.Context) (string, error) {
	var response weixinBrokerCursorResponse
	err := s.broker.Call(
		ctx, weixinBrokerDomain, weixinBrokerVersion, weixinBrokerOperationLoadCursor,
		weixinBrokerAccountRequest{StoreID: s.StoreID(), AccountKey: s.accountKey}, &response,
	)
	if err != nil {
		return "", err
	}
	if err := validateWeixinStateValue(response.Cursor, true); err != nil {
		return "", database.NewError(database.CodeIntegrity, "Weixin cursor response is invalid")
	}
	return response.Cursor, nil
}

func (s *weixinStateStore) brokerSaveCursor(ctx context.Context, cursor string) error {
	var response weixinBrokerMutationResponse
	err := s.broker.CallWithOptions(
		ctx, weixinBrokerDomain, weixinBrokerVersion, weixinBrokerOperationSaveCursor,
		weixinBrokerCursorRequest{
			StoreID: s.StoreID(), AccountKey: s.accountKey, Cursor: cursor,
		},
		&response, database.CallOptions{Mutation: true},
	)
	return validateWeixinMutationResponse(response, err)
}

func (s *weixinStateStore) brokerLoadTokens(ctx context.Context) (map[string]string, error) {
	tokens := make(map[string]string)
	after := ""
	for {
		var response weixinBrokerTokensResponse
		err := s.broker.Call(
			ctx, weixinBrokerDomain, weixinBrokerVersion, weixinBrokerOperationLoadTokens,
			weixinBrokerTokenPageRequest{
				StoreID: s.StoreID(), AccountKey: s.accountKey,
				AfterUserID: after, Limit: weixinBrokerTokenPageSize,
			},
			&response,
		)
		if err != nil {
			return nil, err
		}
		if len(response.Tokens) > weixinBrokerTokenPageSize || validateWeixinTokens(response.Tokens) != nil {
			return nil, database.NewError(database.CodeIntegrity, "Weixin token response is invalid")
		}
		for userID, token := range response.Tokens {
			if _, duplicate := tokens[userID]; duplicate || after != "" && userID <= after {
				return nil, database.NewError(database.CodeIntegrity, "Weixin token response is invalid")
			}
			tokens[userID] = token
		}
		if len(tokens) > weixinStateMaxTokens {
			return nil, database.NewError(database.CodeIntegrity, "Weixin token response is invalid")
		}
		if response.NextAfter == "" {
			break
		}
		if validateWeixinStateValue(response.NextAfter, false) != nil || response.NextAfter <= after {
			return nil, database.NewError(database.CodeIntegrity, "Weixin token response is invalid")
		}
		if _, exists := response.Tokens[response.NextAfter]; !exists {
			return nil, database.NewError(database.CodeIntegrity, "Weixin token response is invalid")
		}
		after = response.NextAfter
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	return tokens, nil
}

func (s *weixinStateStore) brokerSaveTokens(ctx context.Context, tokens map[string]string) error {
	var response weixinBrokerMutationResponse
	err := s.broker.CallWithOptions(
		ctx, weixinBrokerDomain, weixinBrokerVersion, weixinBrokerOperationReplaceTokens,
		weixinBrokerTokensRequest{
			StoreID: s.StoreID(), AccountKey: s.accountKey, Tokens: tokens,
		},
		&response, database.CallOptions{Mutation: true},
	)
	return validateWeixinMutationResponse(response, err)
}

func (s *weixinStateStore) brokerSaveToken(ctx context.Context, userID, token string) error {
	var response weixinBrokerMutationResponse
	err := s.broker.CallWithOptions(
		ctx, weixinBrokerDomain, weixinBrokerVersion, weixinBrokerOperationSaveToken,
		weixinBrokerTokenRequest{
			StoreID: s.StoreID(), AccountKey: s.accountKey, UserID: userID, Token: token,
		},
		&response, database.CallOptions{Mutation: true},
	)
	return validateWeixinMutationResponse(response, err)
}

func validateWeixinMutationResponse(response weixinBrokerMutationResponse, err error) error {
	if err != nil {
		return err
	}
	if !response.Updated {
		return database.NewError(database.CodeIntegrity, "Weixin mutation response is invalid")
	}
	return nil
}

func validateWeixinTokens(tokens map[string]string) error {
	if len(tokens) > weixinStateMaxTokens {
		return errors.New("Weixin token count exceeds its limit")
	}
	for userID, token := range tokens {
		if validateWeixinStateValue(userID, false) != nil ||
			validateWeixinStateValue(token, true) != nil {
			return errors.New("Weixin token value is invalid")
		}
	}
	return nil
}

// RunOfflineDatabaseMigration upgrades the trusted Weixin store under the
// migration command's exclusive home fence.
func RunOfflineDatabaseMigration(home string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"Weixin migration requires the exclusive database fence",
		)
	}
	store, err := newRetainedWeixinStateStore(home)
	if err != nil {
		return err
	}
	return store.Close()
}

// BrokerHandler owns the supervisor-local Weixin state store and its stable
// connection pool through Close.
type BrokerHandler struct {
	home string

	once  sync.Once
	store *weixinStateStore
	err   error
}

func NewBrokerHandler(home string) *BrokerHandler {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return &BrokerHandler{err: database.NewError(
			database.CodeUnauthorized,
			"Weixin broker handler requires database broker authority",
		)}
	}
	return &BrokerHandler{home: home}
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != weixinBrokerDomain || request.Version != weixinBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	switch request.Operation {
	case weixinBrokerOperationPreflight:
		var input weixinBrokerTarget
		if err := request.DecodePayload(&input); err != nil || input.StoreID != WeixinStoreID {
			return nil, database.NewError(database.CodeInvalid, "Weixin state request is invalid")
		}
		if _, err := handler.open(); err != nil {
			return nil, mapWeixinBrokerError(err)
		}
		return weixinBrokerReadyResponse{Ready: true}, nil
	case weixinBrokerOperationLoadCursor:
		var input weixinBrokerAccountRequest
		if err := request.DecodePayload(&input); err != nil || !validWeixinBrokerAccount(input) {
			return nil, database.NewError(database.CodeInvalid, "Weixin state request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapWeixinBrokerError(err)
		}
		cursor, err := store.forAccount(input.AccountKey).loadCursorLocal(ctx)
		if err != nil {
			return nil, mapWeixinBrokerError(err)
		}
		return weixinBrokerCursorResponse{Cursor: cursor}, nil
	case weixinBrokerOperationSaveCursor:
		var input weixinBrokerCursorRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != WeixinStoreID ||
			!validWeixinAccountKey(input.AccountKey) || validateWeixinStateValue(input.Cursor, true) != nil {
			return nil, database.NewError(database.CodeInvalid, "Weixin state request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapWeixinBrokerError(err)
		}
		if err := store.forAccount(input.AccountKey).saveCursorLocal(ctx, input.Cursor); err != nil {
			return nil, mapWeixinBrokerError(err)
		}
		return weixinBrokerMutationResponse{Updated: true}, nil
	case weixinBrokerOperationLoadTokens:
		var input weixinBrokerTokenPageRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != WeixinStoreID ||
			!validWeixinAccountKey(input.AccountKey) || input.Limit < 1 ||
			input.Limit > weixinBrokerTokenPageSize ||
			input.AfterUserID != "" && validateWeixinStateValue(input.AfterUserID, false) != nil {
			return nil, database.NewError(database.CodeInvalid, "Weixin state request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapWeixinBrokerError(err)
		}
		tokens, nextAfter, err := store.forAccount(input.AccountKey).loadTokensPageLocal(
			ctx, input.AfterUserID, input.Limit,
		)
		if err != nil {
			return nil, mapWeixinBrokerError(err)
		}
		return weixinBrokerTokensResponse{Tokens: tokens, NextAfter: nextAfter}, nil
	case weixinBrokerOperationReplaceTokens:
		var input weixinBrokerTokensRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != WeixinStoreID ||
			!validWeixinAccountKey(input.AccountKey) || validateWeixinTokens(input.Tokens) != nil {
			return nil, database.NewError(database.CodeInvalid, "Weixin state request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapWeixinBrokerError(err)
		}
		userIDs := make([]string, 0, len(input.Tokens))
		for userID := range input.Tokens {
			userIDs = append(userIDs, userID)
		}
		sort.Strings(userIDs)
		if err := store.forAccount(input.AccountKey).saveTokensLocal(ctx, input.Tokens, userIDs); err != nil {
			return nil, mapWeixinBrokerError(err)
		}
		return weixinBrokerMutationResponse{Updated: true}, nil
	case weixinBrokerOperationSaveToken:
		var input weixinBrokerTokenRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != WeixinStoreID ||
			!validWeixinAccountKey(input.AccountKey) ||
			validateWeixinStateValue(input.UserID, false) != nil ||
			validateWeixinStateValue(input.Token, true) != nil {
			return nil, database.NewError(database.CodeInvalid, "Weixin state request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapWeixinBrokerError(err)
		}
		if err := store.forAccount(input.AccountKey).saveTokenLocal(ctx, input.UserID, input.Token); err != nil {
			return nil, mapWeixinBrokerError(err)
		}
		return weixinBrokerMutationResponse{Updated: true}, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "Weixin state operation is unsupported")
	}
}

func validWeixinBrokerAccount(input weixinBrokerAccountRequest) bool {
	return input.StoreID == WeixinStoreID && validWeixinAccountKey(input.AccountKey)
}

func (handler *BrokerHandler) open() (*weixinStateStore, error) {
	if handler == nil {
		return nil, errors.New("Weixin state handler is unavailable")
	}
	if handler.err != nil {
		return nil, handler.err
	}
	handler.once.Do(func() {
		handler.store, handler.err = newRetainedWeixinStateStore(handler.home)
	})
	return handler.store, handler.err
}

func (handler *BrokerHandler) Close() error {
	if handler == nil || handler.store == nil {
		return nil
	}
	return handler.store.Close()
}

func (s *weixinStateStore) forAccount(accountKey string) *weixinStateStore {
	if s == nil {
		return nil
	}
	clone := *s
	clone.accountKey = accountKey
	clone.broker = nil
	return &clone
}

func mapWeixinBrokerError(err error) error {
	if err == nil {
		return nil
	}
	var structured *database.Error
	if errors.As(err, &structured) {
		return database.NewError(structured.Code, structured.Message)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return database.NewError(database.CodeDeadline, "Weixin state request deadline was exceeded")
	case errors.Is(err, sqlitestore.ErrTooNew):
		return database.NewError(database.CodeUnsupported, "Weixin state schema is newer than supported")
	case errors.Is(err, sqlitestore.ErrInvalidSchema), errors.Is(err, sqlitestore.ErrIntegrity):
		return database.NewError(database.CodeIntegrity, "Weixin state integrity validation failed")
	case errors.Is(err, os.ErrPermission):
		return database.NewError(database.CodeUnavailable, "Weixin state store is unavailable")
	default:
		return database.NewError(database.CodeInternal, "Weixin state operation failed")
	}
}

var _ database.Handler = (*BrokerHandler)(nil)
