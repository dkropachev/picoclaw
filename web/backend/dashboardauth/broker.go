package dashboardauth

import (
	"context"
	"errors"
	"sync"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	launcherAuthDomain  = "launcher-auth"
	launcherAuthVersion = 1

	launcherAuthOperationInitialized    = "is-initialized"
	launcherAuthOperationSetPassword    = "set-password"
	launcherAuthOperationVerifyPassword = "verify-password"
)

type launcherAuthEmptyRequest struct {
	StoreID database.StoreID `json:"store_id"`
}

type launcherAuthPasswordRequest struct {
	StoreID  database.StoreID `json:"store_id"`
	Password string           `json:"password"`
}

type launcherAuthInitializedResponse struct {
	Initialized bool `json:"initialized"`
}

type launcherAuthVerificationResponse struct {
	Verified bool `json:"verified"`
}

type launcherAuthMutationResponse struct {
	Updated bool `json:"updated"`
}

// BrokerHandler is the broker-side typed launcher authentication adapter. It
// opens its physical store lazily in the broker process and never exposes SQL
// or a path over IPC.
type BrokerHandler struct {
	home         string
	launcherPath string

	once  sync.Once
	store *Store
	err   error
}

func NewBrokerHandler(home, launcherPath string) *BrokerHandler {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return &BrokerHandler{err: database.NewError(
			database.CodeUnauthorized,
			"launcher-auth broker handler requires database broker authority",
		)}
	}
	return &BrokerHandler{home: home, launcherPath: launcherPath}
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != launcherAuthDomain || request.Version != launcherAuthVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	switch request.Operation {
	case launcherAuthOperationInitialized:
		var input launcherAuthEmptyRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != launcherAuthStoreID {
			return nil, database.NewError(database.CodeInvalid, "launcher auth request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, database.NewError(database.CodeUnavailable, "launcher auth store is unavailable")
		}
		initialized, err := store.IsInitialized(ctx)
		if err != nil {
			return nil, mapLauncherAuthError(err)
		}
		return launcherAuthInitializedResponse{Initialized: initialized}, nil
	case launcherAuthOperationSetPassword:
		var input launcherAuthPasswordRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != launcherAuthStoreID ||
			input.Password == "" {
			return nil, database.NewError(database.CodeInvalid, "launcher auth request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, database.NewError(database.CodeUnavailable, "launcher auth store is unavailable")
		}
		if err := store.SetPassword(ctx, input.Password); err != nil {
			return nil, mapLauncherAuthError(err)
		}
		return launcherAuthMutationResponse{Updated: true}, nil
	case launcherAuthOperationVerifyPassword:
		var input launcherAuthPasswordRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != launcherAuthStoreID {
			return nil, database.NewError(database.CodeInvalid, "launcher auth request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, database.NewError(database.CodeUnavailable, "launcher auth store is unavailable")
		}
		verified, err := store.VerifyPassword(ctx, input.Password)
		if err != nil {
			return nil, mapLauncherAuthError(err)
		}
		return launcherAuthVerificationResponse{Verified: verified}, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "launcher auth operation is unsupported")
	}
}

func (handler *BrokerHandler) open() (*Store, error) {
	if handler == nil || handler.err != nil {
		if handler != nil {
			return nil, handler.err
		}
		return nil, database.NewError(database.CodeUnavailable, "launcher auth handler is unavailable")
	}
	handler.once.Do(func() {
		handler.store, handler.err = newLocalWithLauncherConfig(handler.home, handler.launcherPath)
	})
	return handler.store, handler.err
}

func (handler *BrokerHandler) Close() error {
	if handler == nil || handler.store == nil {
		return nil
	}
	return handler.store.Close()
}

func mapLauncherAuthError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return database.NewError(database.CodeDeadline, "launcher auth request deadline was exceeded")
	}
	return database.NewError(database.CodeInternal, "launcher auth operation failed")
}

// RunOfflineDatabaseMigration upgrades launcher authentication only while the
// migration command owns exclusive storage fencing.
func RunOfflineDatabaseMigration(home, launcherPath string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"launcher auth migration requires the exclusive database fence",
		)
	}
	store, err := newLocalWithLauncherConfig(home, launcherPath)
	if err != nil {
		return err
	}
	return store.Close()
}

var _ database.Handler = (*BrokerHandler)(nil)
