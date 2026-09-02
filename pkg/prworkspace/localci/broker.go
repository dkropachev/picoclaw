package localci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	CacheBrokerDomain  = "local-ci-cache"
	CacheBrokerVersion = 1

	CacheStoreID database.StoreID = "workspace.local-ci"

	cacheOperationResolve   = "resolve-store"
	cacheOperationPreflight = "preflight"
	cacheOperationLookup    = "lookup-passing"
	cacheOperationPromote   = "promote-passing"
)

type cacheResolveRequest struct {
	Selector string `json:"selector"`
}

type cacheResolveResponse struct {
	StoreID database.StoreID `json:"store_id"`
}

type cacheStoreRequest struct {
	StoreID database.StoreID `json:"store_id"`
}

type cacheLookupRequest struct {
	StoreID   database.StoreID `json:"store_id"`
	ResultKey string           `json:"result_key"`
}

type cachePromoteRequest struct {
	StoreID         database.StoreID `json:"store_id"`
	ResultKey       string           `json:"result_key"`
	ExecutionDigest string           `json:"execution_digest"`
}

type cacheResponse struct {
	Updated   bool      `json:"updated,omitempty"`
	Found     bool      `json:"found,omitempty"`
	Execution Execution `json:"execution"`
}

type cacheBrokerStore struct {
	selector string
	root     string
	store    *FileEvidenceStore
	opMu     sync.Mutex
}

type BrokerHandler struct {
	stores    map[database.StoreID]*cacheBrokerStore
	selectors map[string]database.StoreID

	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedLocalCIProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"local CI broker handler requires authenticated broker authority",
		)
	}
	configured, err := configuredCacheStores(home, cfg)
	if err != nil {
		return nil, err
	}
	handler := &BrokerHandler{
		stores:    make(map[database.StoreID]*cacheBrokerStore, len(configured)),
		selectors: make(map[string]database.StoreID, len(configured)),
	}
	for _, item := range configured {
		handler.stores[item.storeID] = &cacheBrokerStore{
			selector: item.selector,
			root:     item.root,
		}
		handler.selectors[item.selector] = item.storeID
	}
	return handler, nil
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != CacheBrokerDomain || request.Version != CacheBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, database.NewError(database.CodeDeadline, "local CI cache deadline was exceeded")
	}
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.closed {
		return nil, database.NewError(database.CodeUnavailable, "local CI cache broker is closed")
	}
	if request.Operation == cacheOperationResolve {
		var input cacheResolveRequest
		if request.DecodePayload(&input) != nil || input.Selector == "" {
			return nil, database.NewError(database.CodeInvalid, "local CI cache request is invalid")
		}
		storeID, ok := handler.selectors[input.Selector]
		if !ok {
			return nil, database.NewError(database.CodeUnauthorized, "local CI evidence root is not cataloged")
		}
		return cacheResolveResponse{StoreID: storeID}, nil
	}
	storeID, err := cacheRequestStoreID(request)
	if err != nil {
		return nil, err
	}
	item, ok := handler.stores[storeID]
	if !ok || item == nil {
		return nil, database.NewError(database.CodeUnauthorized, "local CI cache StoreID is not cataloged")
	}
	if request.Operation == cacheOperationPreflight {
		var input cacheStoreRequest
		if request.DecodePayload(&input) != nil || input.StoreID != storeID {
			return nil, database.NewError(database.CodeInvalid, "local CI cache request is invalid")
		}
	}
	item.opMu.Lock()
	defer item.opMu.Unlock()
	if item.store == nil {
		store, openErr := openFileEvidenceStoreLocal(item.root)
		if openErr != nil {
			return nil, mapCacheBrokerError(openErr)
		}
		item.store = store
	}
	switch request.Operation {
	case cacheOperationPreflight:
		return cacheResponse{}, nil
	case cacheOperationLookup:
		var input cacheLookupRequest
		if request.DecodePayload(&input) != nil || input.StoreID != storeID || !validDigest(input.ResultKey) {
			return nil, database.NewError(database.CodeInvalid, "local CI cache request is invalid")
		}
		execution, found, err := item.store.lookupPassingCache(ctx, input.ResultKey)
		if err != nil {
			return nil, mapCacheBrokerError(err)
		}
		return cacheResponse{Found: found, Execution: execution}, nil
	case cacheOperationPromote:
		var input cachePromoteRequest
		if request.DecodePayload(&input) != nil || input.StoreID != storeID ||
			!validDigest(input.ResultKey) || !validDigest(input.ExecutionDigest) {
			return nil, database.NewError(database.CodeInvalid, "local CI cache request is invalid")
		}
		if err := item.store.promotePassingCache(ctx, input.ResultKey, input.ExecutionDigest); err != nil {
			return nil, mapCacheBrokerError(err)
		}
		return cacheResponse{Updated: true}, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "local CI cache operation is unsupported")
	}
}

func (handler *BrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		defer handler.mu.Unlock()
		handler.closed = true
		for _, item := range handler.stores {
			if item.store != nil {
				handler.closeErr = errors.Join(handler.closeErr, item.store.Close())
			}
		}
	})
	return handler.closeErr
}

func (store *FileEvidenceStore) lookupPassingBroker(
	ctx context.Context,
	resultKey string,
) (Execution, bool, error) {
	if store.cacheBrokerErr != nil {
		return Execution{}, false, store.cacheBrokerErr
	}
	var response cacheResponse
	err := store.cacheBroker.Call(
		ctx, CacheBrokerDomain, CacheBrokerVersion, cacheOperationLookup,
		cacheLookupRequest{StoreID: store.cacheStoreID, ResultKey: resultKey}, &response,
	)
	return response.Execution, response.Found, err
}

func (store *FileEvidenceStore) promotePassingBroker(
	ctx context.Context,
	resultKey,
	executionDigest string,
) error {
	if store.cacheBrokerErr != nil {
		return store.cacheBrokerErr
	}
	var response cacheResponse
	err := store.cacheBroker.CallWithOptions(
		ctx, CacheBrokerDomain, CacheBrokerVersion, cacheOperationPromote,
		cachePromoteRequest{
			StoreID: store.cacheStoreID, ResultKey: resultKey, ExecutionDigest: executionDigest,
		},
		&response, database.CallOptions{Mutation: true},
	)
	if err != nil {
		return err
	}
	if !response.Updated {
		return database.NewError(database.CodeIntegrity, "local CI cache response is invalid")
	}
	return nil
}

func resolveCacheStoreID(
	ctx context.Context,
	client *database.Client,
	root string,
) (database.StoreID, error) {
	selector, err := cacheRootSelector(root)
	if err != nil {
		return "", err
	}
	var response cacheResolveResponse
	err = client.Call(
		ctx, CacheBrokerDomain, CacheBrokerVersion, cacheOperationResolve,
		cacheResolveRequest{Selector: selector}, &response,
	)
	if err != nil {
		return "", err
	}
	if !response.StoreID.Valid() {
		return "", database.NewError(database.CodeIntegrity, "local CI cache StoreID is invalid")
	}
	return response.StoreID, nil
}

type configuredCacheStore struct {
	root     string
	selector string
	storeID  database.StoreID
}

func configuredCacheStores(home string, cfg *config.Config) ([]configuredCacheStore, error) {
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	primary, err := resolveCacheWorkspace(canonicalHome, cfg.Agents.Defaults.Workspace)
	if err != nil {
		return nil, err
	}
	type input struct {
		workspace string
		primary   bool
	}
	inputs := []input{{workspace: primary, primary: true}}
	for _, agent := range cfg.Agents.List {
		if strings.TrimSpace(agent.Workspace) == "" {
			continue
		}
		workspace, resolveErr := resolveCacheWorkspace(canonicalHome, agent.Workspace)
		if resolveErr != nil {
			return nil, resolveErr
		}
		inputs = append(inputs, input{workspace: workspace})
	}
	seen := make(map[string]struct{}, len(inputs))
	seenRoots := make(map[string]database.StoreID, len(inputs))
	seenSelectors := make(map[string]string, len(inputs))
	result := make([]configuredCacheStore, 0, len(inputs))
	for _, item := range inputs {
		if _, duplicate := seen[item.workspace]; duplicate {
			continue
		}
		seen[item.workspace] = struct{}{}
		effective := config.EffectiveEventIngressConfig(nil, item.workspace)
		if item.primary {
			effective = config.EffectiveEventIngressConfig(cfg, item.workspace)
		}
		root := filepath.Join(filepath.Dir(effective.DatabasePath), "pr-workspace-local-ci", "evidence")
		root, err = filepath.Abs(filepath.Clean(root))
		if err != nil {
			return nil, err
		}
		selector, selectorErr := cacheRootSelector(root)
		if selectorErr != nil {
			return nil, selectorErr
		}
		storeID := CacheStoreID
		if !item.primary {
			workspaceSelector, workspaceErr := cacheWorkspaceSelector(item.workspace)
			if workspaceErr != nil {
				return nil, workspaceErr
			}
			storeID, workspaceErr = database.ParseStoreID("workspace." + workspaceSelector + ".local-ci")
			if workspaceErr != nil {
				return nil, workspaceErr
			}
		}
		if previous, collision := seenRoots[root]; collision && previous != storeID {
			return nil, database.NewError(database.CodeIntegrity, "local CI cache roots collide")
		}
		if previous, collision := seenSelectors[selector]; collision && previous != root {
			return nil, database.NewError(database.CodeIntegrity, "local CI cache selectors collide")
		}
		seenRoots[root] = storeID
		seenSelectors[selector] = root
		result = append(result, configuredCacheStore{root: root, selector: selector, storeID: storeID})
	}
	return result, nil
}

func resolveCacheWorkspace(home, configured string) (string, error) {
	value := strings.TrimSpace(configured)
	if value == "" {
		value = filepath.Join(home, "workspace")
	} else if value == "~" || strings.HasPrefix(value, "~/") {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if value == "~" {
			value = userHome
		} else {
			value = filepath.Join(userHome, value[2:])
		}
	} else if !filepath.IsAbs(value) {
		value = filepath.Join(home, value)
	}
	return filepath.Abs(filepath.Clean(value))
}

func cacheRootSelector(root string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(strings.TrimSpace(root)))
	if err != nil || strings.TrimSpace(root) == "" || strings.ContainsRune(root, 0) {
		return "", database.NewError(database.CodeInvalid, "local CI evidence root is invalid")
	}
	digest := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(digest[:8]), nil
}

func cacheWorkspaceSelector(workspace string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(digest[:8]), nil
}

func cacheRequestStoreID(request database.Request) (database.StoreID, error) {
	var header struct {
		StoreID database.StoreID `json:"store_id"`
	}
	if json.Unmarshal(request.Payload, &header) != nil || !header.StoreID.Valid() {
		return "", database.NewError(database.CodeInvalid, "local CI cache request is invalid")
	}
	return header.StoreID, nil
}

func mapCacheBrokerError(err error) error {
	if err == nil {
		return nil
	}
	if code := database.CodeOf(err); code != database.CodeInternal {
		return database.NewError(code, "local CI cache operation failed")
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return database.NewError(database.CodeDeadline, "local CI cache deadline was exceeded")
	case errors.Is(err, ErrEvidenceConflict):
		return database.NewError(database.CodeConflict, "local CI cache changed concurrently")
	case errors.Is(err, ErrEvidenceCorrupt), errors.Is(err, sqlitestore.ErrIntegrity),
		errors.Is(err, sqlitestore.ErrInvalidSchema):
		return database.NewError(database.CodeIntegrity, "local CI cache integrity validation failed")
	case errors.Is(err, ErrInvalid):
		return database.NewError(database.CodeInvalid, "local CI cache request is invalid")
	default:
		return database.NewError(database.CodeInternal, "local CI cache operation failed")
	}
}

var _ database.Handler = (*BrokerHandler)(nil)
