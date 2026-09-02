package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	authBrokerDomain  = "auth"
	authBrokerVersion = 1
)

const GlobalAuthStoreID database.StoreID = "global.auth"

const (
	authBrokerPageItems  = 64
	authBrokerPageBytes  = 8 << 20
	authBrokerWriteBytes = 32 << 20
)

type authBrokerHandler struct {
	home string

	mu     sync.Mutex
	once   sync.Once
	db     *sql.DB
	unlock func()
	err    error
	closed bool
}

var (
	authBrokerHandlerDepth     atomic.Int32
	authBrokerRetainedDatabase atomic.Pointer[sql.DB]
)

// NewBrokerHandler returns the broker-side typed credential adapter.
func NewBrokerHandler(home string) *authBrokerHandler { return &authBrokerHandler{home: home} }

type authEmptyRequest struct {
	StoreID database.StoreID `json:"store_id"`
}

type authStoreRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Store   *AuthStore       `json:"store"`
}

type authCredentialRequest struct {
	StoreID      database.StoreID `json:"store_id"`
	CredentialID string           `json:"credential_id"`
	Credential   *AuthCredential  `json:"credential,omitempty"`
}

type authCASRequest struct {
	StoreID      database.StoreID `json:"store_id"`
	CredentialID string           `json:"credential_id"`
	Source       *AuthCredential  `json:"source"`
	Replacement  *AuthCredential  `json:"replacement"`
}

type authLoadPageRequest struct {
	StoreID  database.StoreID `json:"store_id"`
	Cursor   string           `json:"cursor,omitempty"`
	Revision string           `json:"revision,omitempty"`
}

type authCredentialItem struct {
	CredentialID string          `json:"credential_id"`
	Credential   *AuthCredential `json:"credential"`
}

type authLoadPageResponse struct {
	Items    []authCredentialItem `json:"items"`
	Next     string               `json:"next,omitempty"`
	Revision string               `json:"revision"`
	Done     bool                 `json:"done"`
}

type authCredentialResponse struct {
	Credential *AuthCredential `json:"credential,omitempty"`
	Committed  bool            `json:"committed,omitempty"`
}

type authMutationResponse struct {
	Updated bool `json:"updated"`
}

func (handler *authBrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != authBrokerDomain || request.Version != authBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed {
		return nil, database.NewError(database.CodeUnavailable, "auth store is unavailable")
	}
	authBrokerHandlerDepth.Add(1)
	defer authBrokerHandlerDepth.Add(-1)
	switch request.Operation {
	case "load-page":
		var input authLoadPageRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != GlobalAuthStoreID ||
			!validAuthRevision(input.Revision) {
			return nil, database.NewError(database.CodeInvalid, "auth request is invalid")
		}
		if err := handler.open(ctx); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		store, err := LoadStore()
		if err != nil {
			return nil, mapAuthBrokerError(err)
		}
		return pageAuthStore(store, input)
	case "save":
		var input authStoreRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != GlobalAuthStoreID ||
			input.Store == nil {
			return nil, database.NewError(database.CodeInvalid, "auth request is invalid")
		}
		if err := handler.open(ctx); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		if err := SaveStore(input.Store); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		return authMutationResponse{Updated: true}, nil
	case "get":
		var input authCredentialRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != GlobalAuthStoreID ||
			input.CredentialID == "" {
			return nil, database.NewError(database.CodeInvalid, "auth request is invalid")
		}
		if err := handler.open(ctx); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		credential, err := GetCredential(input.CredentialID)
		if err != nil {
			return nil, mapAuthBrokerError(err)
		}
		return authCredentialResponse{Credential: credential}, nil
	case "set":
		var input authCredentialRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != GlobalAuthStoreID ||
			input.CredentialID == "" || input.Credential == nil {
			return nil, database.NewError(database.CodeInvalid, "auth request is invalid")
		}
		if err := handler.open(ctx); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		if err := SetCredential(input.CredentialID, input.Credential); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		return authMutationResponse{Updated: true}, nil
	case "compare-and-set":
		var input authCASRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != GlobalAuthStoreID ||
			input.CredentialID == "" ||
			input.Source == nil || input.Replacement == nil {
			return nil, database.NewError(database.CodeInvalid, "auth request is invalid")
		}
		if err := handler.open(ctx); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		authoritative, committed, err := persistCredentialIfCurrentDetailed(
			input.CredentialID, input.Source, input.Replacement,
		)
		if err != nil {
			return nil, mapAuthBrokerError(err)
		}
		return authCredentialResponse{Credential: authoritative, Committed: committed}, nil
	case "update":
		var input authCASRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != GlobalAuthStoreID ||
			input.CredentialID == "" ||
			input.Replacement == nil {
			return nil, database.NewError(database.CodeInvalid, "auth request is invalid")
		}
		if err := handler.open(ctx); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		authoritative, committed, err := brokerPersistCredentialSnapshot(
			ctx, input.CredentialID, input.Source, input.Replacement,
		)
		if err != nil {
			return nil, mapAuthBrokerError(err)
		}
		return authCredentialResponse{Credential: authoritative, Committed: committed}, nil
	case "delete":
		var input authCredentialRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != GlobalAuthStoreID ||
			input.CredentialID == "" {
			return nil, database.NewError(database.CodeInvalid, "auth request is invalid")
		}
		if err := handler.open(ctx); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		if err := DeleteCredential(input.CredentialID); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		return authMutationResponse{Updated: true}, nil
	case "delete-all":
		var input authEmptyRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != GlobalAuthStoreID {
			return nil, database.NewError(database.CodeInvalid, "auth request is invalid")
		}
		if err := handler.open(ctx); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		if err := DeleteAllCredentials(); err != nil {
			return nil, mapAuthBrokerError(err)
		}
		return authMutationResponse{Updated: true}, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "auth operation is unsupported")
	}
}

func pageAuthStore(store *AuthStore, input authLoadPageRequest) (authLoadPageResponse, error) {
	if store == nil {
		return authLoadPageResponse{}, database.NewError(database.CodeIntegrity, "auth store is invalid")
	}
	keys := make([]string, 0, len(store.Credentials))
	for key := range store.Credentials {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	revision, err := authStoreRevision(store, keys)
	if err != nil {
		return authLoadPageResponse{}, database.NewError(database.CodeIntegrity, "auth revision is invalid")
	}
	if input.Revision != "" && input.Revision != revision {
		return authLoadPageResponse{}, database.NewError(database.CodeConflict, "auth store changed during pagination")
	}
	start := 0
	if input.Cursor != "" {
		start = sort.SearchStrings(keys, input.Cursor)
		if start >= len(keys) || keys[start] != input.Cursor {
			return authLoadPageResponse{}, database.NewError(database.CodeConflict, "auth cursor is stale")
		}
		start++
	}
	response := authLoadPageResponse{
		Items:    make([]authCredentialItem, 0, min(authBrokerPageItems, len(keys)-start)),
		Revision: revision,
	}
	pageBytes := 0
	for index := start; index < len(keys) && len(response.Items) < authBrokerPageItems; index++ {
		item := authCredentialItem{
			CredentialID: keys[index], Credential: cloneCredential(store.Credentials[keys[index]]),
		}
		raw, err := database.MarshalCanonical(item)
		if err != nil || len(raw) > authBrokerPageBytes {
			return authLoadPageResponse{}, database.NewError(database.CodeIntegrity, "auth item exceeds page limit")
		}
		if len(response.Items) > 0 && pageBytes+len(raw) > authBrokerPageBytes {
			break
		}
		response.Items = append(response.Items, item)
		response.Next = item.CredentialID
		pageBytes += len(raw)
	}
	response.Done = start+len(response.Items) == len(keys)
	if response.Done {
		response.Next = ""
	}
	return response, nil
}

func authStoreRevision(store *AuthStore, keys []string) (string, error) {
	digest := sha256.New()
	for _, key := range keys {
		raw, err := database.MarshalCanonical(authCredentialItem{
			CredentialID: key, Credential: store.Credentials[key],
		})
		if err != nil {
			return "", err
		}
		_, _ = digest.Write(raw)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validAuthRevision(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func (handler *authBrokerHandler) open(ctx context.Context) error {
	handler.once.Do(func() {
		if !authProviderAccessAuthorized() {
			handler.err = database.NewError(
				database.CodeUnauthorized,
				"auth broker provider access requires database owner authority",
			)
			return
		}
		home, err := filepath.Abs(filepath.Clean(handler.home))
		if err != nil || handler.home == "" {
			handler.err = database.NewError(database.CodeInvalid, "auth broker home is invalid")
			return
		}
		path := filepath.Join(home, authDatabaseFilename)
		authDatabaseAccessMu.Lock()
		unlockFile, err := lockAuthPath(path + ".locks/store")
		if err != nil {
			authDatabaseAccessMu.Unlock()
			handler.err = err
			return
		}
		db, err := sqlitestore.Open(ctx, path, authStoreOptions(home))
		if err != nil {
			unlockFile()
			authDatabaseAccessMu.Unlock()
			handler.err = err
			return
		}
		handler.db = db
		handler.unlock = func() {
			unlockFile()
			authDatabaseAccessMu.Unlock()
		}
		authBrokerRetainedDatabase.Store(db)
	})
	return handler.err
}

func (handler *authBrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.closed {
		return nil
	}
	handler.closed = true
	if handler.db != nil {
		authBrokerRetainedDatabase.CompareAndSwap(handler.db, nil)
	}
	var closeErr error
	if handler.db != nil {
		closeErr = handler.db.Close()
		handler.db = nil
	}
	if handler.unlock != nil {
		handler.unlock()
		handler.unlock = nil
	}
	return closeErr
}

func retainedAuthBrokerDatabase() (*sql.DB, error) {
	if db := authBrokerRetainedDatabase.Load(); db != nil {
		return db, nil
	}
	if authBrokerHandlerDepth.Load() > 0 {
		return nil, fmt.Errorf("auth broker database is unavailable")
	}
	return nil, nil
}

// RunOfflineDatabaseMigration initializes or upgrades the trusted global auth
// store while the database migration command owns the exclusive home fence.
func RunOfflineDatabaseMigration(ctx context.Context, home string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(database.CodeConflict, "auth migration requires the exclusive database fence")
	}
	home, err := filepath.Abs(filepath.Clean(home))
	if err != nil || home == "" {
		return database.NewError(database.CodeInvalid, "auth migration home is invalid")
	}
	db, err := sqlitestore.Open(
		ctx,
		filepath.Join(home, authDatabaseFilename),
		authStoreOptions(home),
	)
	if err != nil {
		return err
	}
	return db.Close()
}

func brokerPersistCredentialSnapshot(
	ctx context.Context,
	credentialID string,
	source,
	replacement *AuthCredential,
) (*AuthCredential, bool, error) {
	db, unlock, err := openAuthDatabaseForWrite(ctx)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	defer closeAuthDatabase(db)
	canonical := canonicalCredentialID(credentialID)
	var authoritative *AuthCredential
	committed := false
	err = sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		current, version, found, loadErr := loadCredentialFromConn(ctx, conn, canonical)
		if loadErr != nil {
			return loadErr
		}
		if !reflect.DeepEqual(current, source) {
			authoritative = cloneCredential(current)
			return nil
		}
		normalized, normalizeErr := normalizeCredentialForStorage(canonical, replacement)
		if normalizeErr != nil {
			return normalizeErr
		}
		if reflect.DeepEqual(current, normalized) {
			authoritative, committed = cloneCredential(normalized), true
			return nil
		}
		if !found {
			if _, insertErr := insertCredential(ctx, conn, canonical, normalized); insertErr != nil {
				return insertErr
			}
		} else if updateErr := updateCredentialVersioned(
			ctx, conn, canonical, normalized, version,
		); updateErr != nil {
			return updateErr
		}
		authoritative, committed = cloneCredential(normalized), true
		return nil
	})
	return authoritative, committed, err
}

func useAuthBroker() bool {
	return database.RuntimeClient() != nil && authBrokerHandlerDepth.Load() == 0
}

func mapAuthBrokerError(err error) error {
	if err == nil {
		return nil
	}
	if database.CodeOf(err) != database.CodeInternal {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return database.NewError(database.CodeDeadline, "auth operation deadline was exceeded")
	}
	return database.NewError(database.CodeInternal, "auth operation failed")
}

func callAuthBroker(operation string, input, output any, mutation bool) error {
	client := database.RuntimeClient()
	if client == nil {
		return database.NewError(database.CodeUnavailable, "auth broker client is unavailable")
	}
	if mutation {
		return client.CallWithOptions(
			context.Background(), authBrokerDomain, authBrokerVersion, operation,
			input, output, database.CallOptions{Mutation: true},
		)
	}
	return client.Call(context.Background(), authBrokerDomain, authBrokerVersion, operation, input, output)
}
