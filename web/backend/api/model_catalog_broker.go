package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	modelCatalogOperationPreflight = "preflight"
	modelCatalogOperationLoadPage  = "load-page"
	modelCatalogOperationSaveAll   = "save-all"
	modelCatalogOperationSave      = "save"
	modelCatalogOperationDelete    = "delete"
)

const (
	modelCatalogPageItems  = 128
	modelCatalogPageBytes  = 8 << 20
	modelCatalogWriteBytes = 32 << 20
)

type modelCatalogPageRequest struct {
	StoreID       database.StoreID `json:"store_id"`
	CatalogCursor int              `json:"catalog_cursor"`
	ModelCursor   int              `json:"model_cursor"`
	Revision      string           `json:"revision,omitempty"`
}

type modelCatalogPageResponse struct {
	Entries           []*CatalogEntry `json:"entries"`
	NextCatalogCursor int             `json:"next_catalog_cursor"`
	NextModelCursor   int             `json:"next_model_cursor"`
	Done              bool            `json:"done"`
	Revision          string          `json:"revision"`
}

type modelCatalogSaveAllRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Store   *CatalogStore    `json:"store"`
}

type modelCatalogSaveRequest struct {
	StoreID  database.StoreID `json:"store_id"`
	Provider string           `json:"provider"`
	APIBase  string           `json:"api_base"`
	APIKey   string           `json:"api_key"`
	Models   []CatalogModel   `json:"models"`
}

type modelCatalogDeleteRequest struct {
	StoreID database.StoreID `json:"store_id"`
	ID      string           `json:"id"`
}

type modelCatalogMutationResponse struct {
	Updated bool `json:"updated"`
}

type modelCatalogDeleteResponse struct {
	Deleted bool `json:"deleted"`
}

// ModelCatalogBrokerHandler is the supervisor-local typed model-catalog
// adapter. It owns one pool for the broker epoch and never exposes its path or
// SQL surface over IPC.
type ModelCatalogBrokerHandler struct {
	path string
	once sync.Once
	db   *sql.DB
	err  error
}

func NewModelCatalogBrokerHandler(home string) *ModelCatalogBrokerHandler {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() {
		return &ModelCatalogBrokerHandler{err: database.NewError(
			database.CodeUnauthorized,
			"model-catalog broker handler requires database broker authority",
		)}
	}
	return &ModelCatalogBrokerHandler{path: filepath.Join(home, catalogDatabaseFilename)}
}

func (handler *ModelCatalogBrokerHandler) Handle(
	ctx context.Context,
	request database.Request,
) (any, error) {
	if handler == nil || request.Domain != modelCatalogBrokerDomain ||
		request.Version != modelCatalogBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	switch request.Operation {
	case modelCatalogOperationPreflight:
		var input struct {
			StoreID database.StoreID `json:"store_id"`
		}
		if err := request.DecodePayload(&input); err != nil || input.StoreID != ModelCatalogStoreID {
			return nil, database.NewError(database.CodeInvalid, "model catalog request is invalid")
		}
		if _, err := handler.open(ctx); err != nil {
			return nil, mapModelCatalogBrokerError(err)
		}
		return modelCatalogMutationResponse{Updated: true}, nil
	case modelCatalogOperationLoadPage:
		var input modelCatalogPageRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != ModelCatalogStoreID ||
			input.CatalogCursor < 0 || input.ModelCursor < 0 || !validModelCatalogRevision(input.Revision) {
			return nil, database.NewError(database.CodeInvalid, "model catalog request is invalid")
		}
		db, err := handler.open(ctx)
		if err != nil {
			return nil, mapModelCatalogBrokerError(err)
		}
		store, err := loadCatalogsFromDatabase(ctx, db)
		if err != nil {
			return nil, mapModelCatalogBrokerError(err)
		}
		revision, err := modelCatalogRevision(store)
		if err != nil {
			return nil, database.NewError(database.CodeIntegrity, "model catalog revision is invalid")
		}
		if input.Revision != "" && input.Revision != revision {
			return nil, database.NewError(database.CodeConflict, "model catalog changed during pagination")
		}
		response, err := pageModelCatalogStore(store, input)
		response.Revision = revision
		return response, err
	case modelCatalogOperationSaveAll:
		var input modelCatalogSaveAllRequest
		if err := request.DecodePayload(&input); err != nil ||
			input.StoreID != ModelCatalogStoreID || input.Store == nil {
			return nil, database.NewError(database.CodeInvalid, "model catalog request is invalid")
		}
		db, err := handler.open(ctx)
		if err != nil {
			return nil, mapModelCatalogBrokerError(err)
		}
		if err := saveCatalogsToDatabase(ctx, db, input.Store); err != nil {
			return nil, mapModelCatalogBrokerError(err)
		}
		return modelCatalogMutationResponse{Updated: true}, nil
	case modelCatalogOperationSave:
		var input modelCatalogSaveRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != ModelCatalogStoreID {
			return nil, database.NewError(database.CodeInvalid, "model catalog request is invalid")
		}
		db, err := handler.open(ctx)
		if err != nil {
			return nil, mapModelCatalogBrokerError(err)
		}
		if err := saveCatalogToDatabase(
			ctx, db, input.Provider, input.APIBase, input.APIKey, input.Models,
		); err != nil {
			return nil, mapModelCatalogBrokerError(err)
		}
		return modelCatalogMutationResponse{Updated: true}, nil
	case modelCatalogOperationDelete:
		var input modelCatalogDeleteRequest
		if err := request.DecodePayload(&input); err != nil || input.StoreID != ModelCatalogStoreID ||
			!validCatalogText(input.ID, 1, maximumCatalogIDBytes, true) {
			return nil, database.NewError(database.CodeInvalid, "model catalog request is invalid")
		}
		db, err := handler.open(ctx)
		if err != nil {
			return nil, mapModelCatalogBrokerError(err)
		}
		deleted, err := deleteCatalogFromDatabase(ctx, db, input.ID)
		if err != nil {
			return nil, mapModelCatalogBrokerError(err)
		}
		return modelCatalogDeleteResponse{Deleted: deleted}, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "model catalog operation is unsupported")
	}
}

func validModelCatalogRevision(value string) bool {
	if value == "" {
		return true
	}
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func modelCatalogRevision(store *CatalogStore) (string, error) {
	if store == nil {
		return "", errors.New("model catalog store is unavailable")
	}
	keys := make([]string, 0, len(store.Entries))
	for key := range store.Entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	digest := sha256.New()
	for _, key := range keys {
		entry := store.Entries[key]
		if entry == nil || entry.ID != key {
			return "", errors.New("model catalog entry is invalid")
		}
		header := *entry
		header.Models = nil
		headerRaw, err := database.MarshalCanonical(header)
		if err != nil {
			return "", err
		}
		_, _ = digest.Write(headerRaw)
		if entry.Models == nil {
			_, _ = digest.Write([]byte{0})
		} else {
			_, _ = digest.Write([]byte{1})
		}
		for _, model := range entry.Models {
			raw, err := database.MarshalCanonical(model)
			if err != nil {
				return "", err
			}
			_, _ = digest.Write(raw)
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func pageModelCatalogStore(
	store *CatalogStore,
	request modelCatalogPageRequest,
) (modelCatalogPageResponse, error) {
	if store == nil {
		return modelCatalogPageResponse{}, database.NewError(
			database.CodeInvalid,
			"model catalog cursor is invalid",
		)
	}
	keys := make([]string, 0, len(store.Entries))
	for key := range store.Entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if request.CatalogCursor > len(keys) {
		return modelCatalogPageResponse{}, database.NewError(
			database.CodeInvalid,
			"model catalog cursor is invalid",
		)
	}
	if request.CatalogCursor == len(keys) {
		if request.ModelCursor != 0 {
			return modelCatalogPageResponse{}, database.NewError(
				database.CodeInvalid,
				"model catalog cursor is invalid",
			)
		}
		return modelCatalogPageResponse{
			Entries: make([]*CatalogEntry, 0), NextCatalogCursor: len(keys), Done: true,
		}, nil
	}
	entry := store.Entries[keys[request.CatalogCursor]]
	if entry == nil || request.ModelCursor > len(entry.Models) {
		return modelCatalogPageResponse{}, database.NewError(
			database.CodeIntegrity,
			"model catalog page source is invalid",
		)
	}
	fragment := *entry
	fragment.Models = nil
	if entry.Models != nil {
		fragment.Models = make([]CatalogModel, 0, min(modelCatalogPageItems, len(entry.Models)-request.ModelCursor))
	}
	nextCatalog, nextModel := request.CatalogCursor, request.ModelCursor
	if len(entry.Models) == 0 {
		nextCatalog++
		nextModel = 0
	} else {
		for nextModel < len(entry.Models) && len(fragment.Models) < modelCatalogPageItems {
			fragment.Models = append(fragment.Models, entry.Models[nextModel])
			raw, err := database.MarshalCanonical(fragment)
			if err != nil || len(raw) > modelCatalogPageBytes {
				fragment.Models = fragment.Models[:len(fragment.Models)-1]
				break
			}
			nextModel++
		}
		if len(fragment.Models) == 0 {
			return modelCatalogPageResponse{}, database.NewError(
				database.CodeIntegrity,
				"model catalog item exceeds page limit",
			)
		}
		if nextModel == len(entry.Models) {
			nextCatalog++
			nextModel = 0
		}
	}
	response := modelCatalogPageResponse{
		Entries: []*CatalogEntry{&fragment}, NextCatalogCursor: nextCatalog,
		NextModelCursor: nextModel, Done: nextCatalog == len(keys) && nextModel == 0,
	}
	return response, nil
}

func (handler *ModelCatalogBrokerHandler) open(ctx context.Context) (*sql.DB, error) {
	if handler == nil || handler.err != nil {
		if handler != nil {
			return nil, handler.err
		}
		return nil, database.NewError(database.CodeUnavailable, "model catalog handler is unavailable")
	}
	handler.once.Do(func() {
		handler.db, handler.err = openCatalogDatabaseAt(ctx, handler.path)
	})
	return handler.db, handler.err
}

func (handler *ModelCatalogBrokerHandler) Close() error {
	if handler == nil || handler.db == nil {
		return nil
	}
	return handler.db.Close()
}

func mapModelCatalogBrokerError(err error) error {
	if err == nil {
		return nil
	}
	var structured *database.Error
	if errors.As(err, &structured) {
		return database.NewError(structured.Code, structured.Message)
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return database.NewError(database.CodeDeadline, "model catalog request deadline was exceeded")
	case errors.Is(err, sqlitestore.ErrTooNew):
		return database.NewError(database.CodeUnsupported, "model catalog schema is newer than supported")
	case errors.Is(err, sqlitestore.ErrInvalidSchema), errors.Is(err, sqlitestore.ErrIntegrity):
		return database.NewError(database.CodeIntegrity, "model catalog integrity validation failed")
	case errors.Is(err, os.ErrPermission):
		return database.NewError(database.CodeUnavailable, "model catalog store is unavailable")
	case modelCatalogInputError(err):
		return database.NewError(database.CodeInvalid, "model catalog input is invalid")
	default:
		return database.NewError(database.CodeInternal, "model catalog operation failed")
	}
}

// RunOfflineModelCatalogMigration initializes or upgrades the trusted global
// catalog under the database migration command's exclusive fence.
func RunOfflineModelCatalogMigration(ctx context.Context, home string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"model catalog migration requires the exclusive database fence",
		)
	}
	path := filepath.Join(home, catalogDatabaseFilename)
	db, err := openCatalogDatabaseAt(ctx, path)
	if err != nil {
		return err
	}
	return db.Close()
}

func modelCatalogInputError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "invalid") || strings.Contains(message, "exceeds") ||
		strings.Contains(message, "limit") || strings.Contains(message, "timestamp")
}

var _ database.Handler = (*ModelCatalogBrokerHandler)(nil)
