package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	RuntimeStateDomain  = "runtime-state"
	RuntimeStateVersion = 1

	runtimeStateOperationResolveStore   = "resolve-store"
	runtimeStateOperationPreflight      = "preflight"
	runtimeStateOperationSnapshot       = "snapshot"
	runtimeStateOperationSetLastChannel = "set-last-channel"
	runtimeStateOperationSetLastChatID  = "set-last-chat-id"
)

const RuntimeStateStoreID database.StoreID = "workspace.runtime-state"

var runtimeStateBrokerClient = database.RuntimeClient

type runtimeStateResolveRequest struct {
	WorkspaceSelector string `json:"workspace_selector"`
}

type runtimeStateResolveResponse struct {
	StoreID database.StoreID `json:"store_id"`
}

type runtimeStateTarget struct {
	StoreID database.StoreID `json:"store_id"`
}

type runtimeStateSetRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Value   string           `json:"value"`
}

type runtimeStateSnapshotResponse struct {
	State State `json:"state"`
}

type runtimeStateMutationResponse struct {
	Updated bool `json:"updated"`
}

func newBrokerManager(workspace string, client *database.Client) (*Manager, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, database.NewError(database.CodeInvalid, "workspace path is required")
	}
	if client == nil {
		return nil, database.NewError(database.CodeUnavailable, "runtime-state broker is unavailable")
	}
	storeID, err := resolveRuntimeStateStoreID(context.Background(), client, workspace)
	if err != nil {
		return nil, err
	}
	return &Manager{
		workspace: workspace, now: time.Now, broker: client, storeID: storeID,
	}, nil
}

func resolveRuntimeStateStoreID(
	ctx context.Context,
	client *database.Client,
	workspace string,
) (database.StoreID, error) {
	selector, err := runtimeWorkspaceSelector(workspace)
	if err != nil {
		return "", err
	}
	var response runtimeStateResolveResponse
	err = client.Call(
		ctx, RuntimeStateDomain, RuntimeStateVersion, runtimeStateOperationResolveStore,
		runtimeStateResolveRequest{WorkspaceSelector: selector}, &response,
	)
	if err != nil {
		return "", err
	}
	if !response.StoreID.Valid() {
		return "", database.NewError(database.CodeIntegrity, "runtime-state StoreID is invalid")
	}
	return response.StoreID, nil
}

func (sm *Manager) brokerSnapshot(ctx context.Context) (State, error) {
	if sm == nil || sm.broker == nil {
		return State{}, database.NewError(database.CodeUnavailable, "runtime-state broker is unavailable")
	}
	if sm.brokerErr != nil {
		return State{}, sm.brokerErr
	}
	if !sm.storeID.Valid() {
		return State{}, database.NewError(database.CodeUnavailable, "runtime-state StoreID is unavailable")
	}
	var response runtimeStateSnapshotResponse
	err := sm.broker.Call(
		ctx, RuntimeStateDomain, RuntimeStateVersion, runtimeStateOperationSnapshot,
		runtimeStateTarget{StoreID: sm.storeID}, &response,
	)
	return response.State, err
}

func (sm *Manager) brokerUpdate(ctx context.Context, field, value string) error {
	if sm == nil || sm.broker == nil {
		return database.NewError(database.CodeUnavailable, "runtime-state broker is unavailable")
	}
	if sm.brokerErr != nil {
		return sm.brokerErr
	}
	if !sm.storeID.Valid() {
		return database.NewError(database.CodeUnavailable, "runtime-state StoreID is unavailable")
	}
	operation := runtimeStateOperationSetLastChannel
	if field == "last_chat_id" {
		operation = runtimeStateOperationSetLastChatID
	} else if field != "last_channel" {
		return database.NewError(database.CodeInvalid, "runtime-state field is invalid")
	}
	var response runtimeStateMutationResponse
	err := sm.broker.CallWithOptions(
		ctx, RuntimeStateDomain, RuntimeStateVersion, operation,
		runtimeStateSetRequest{StoreID: sm.storeID, Value: value}, &response,
		database.CallOptions{Mutation: true},
	)
	if err != nil {
		return err
	}
	if !response.Updated {
		return database.NewError(database.CodeIntegrity, "runtime-state mutation response is invalid")
	}
	return nil
}

type runtimeBrokerWorkspace struct {
	selector  string
	workspace string
	manager   *Manager
	opMu      sync.Mutex
}

// BrokerHandler owns one stable pool for the primary and every distinct
// configured-agent workspace loaded from trusted configuration.
type BrokerHandler struct {
	workspaces map[database.StoreID]*runtimeBrokerWorkspace
	selectors  map[string]database.StoreID

	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedRuntimeStateProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"runtime-state broker handler requires online database fencing",
		)
	}
	configured, err := configuredRuntimeWorkspaces(home, cfg)
	if err != nil {
		return nil, err
	}
	handler := &BrokerHandler{
		workspaces: make(map[database.StoreID]*runtimeBrokerWorkspace, len(configured)),
		selectors:  make(map[string]database.StoreID, len(configured)),
	}
	for _, item := range configured {
		handler.workspaces[item.storeID] = &runtimeBrokerWorkspace{
			selector: item.selector, workspace: item.workspace,
		}
		handler.selectors[item.selector] = item.storeID
	}
	return handler, nil
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != RuntimeStateDomain || request.Version != RuntimeStateVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, database.NewError(database.CodeDeadline, "runtime-state request deadline was exceeded")
	}
	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.closed {
		return nil, database.NewError(database.CodeUnavailable, "runtime-state broker is closed")
	}
	if request.Operation == runtimeStateOperationResolveStore {
		var input runtimeStateResolveRequest
		if request.DecodePayload(&input) != nil || input.WorkspaceSelector == "" {
			return nil, database.NewError(database.CodeInvalid, "runtime-state request is invalid")
		}
		storeID, ok := handler.selectors[input.WorkspaceSelector]
		if !ok {
			return nil, database.NewError(database.CodeUnauthorized, "runtime-state workspace is not cataloged")
		}
		return runtimeStateResolveResponse{StoreID: storeID}, nil
	}
	storeID, err := runtimeRequestStoreID(request)
	if err != nil {
		return nil, err
	}
	workspace, ok := handler.workspaces[storeID]
	if !ok || workspace == nil {
		return nil, database.NewError(database.CodeUnauthorized, "runtime-state StoreID is not cataloged")
	}
	if request.Operation == runtimeStateOperationPreflight {
		var input runtimeStateTarget
		if request.DecodePayload(&input) != nil || input.StoreID != storeID {
			return nil, database.NewError(database.CodeInvalid, "runtime-state request is invalid")
		}
	}
	workspace.opMu.Lock()
	defer workspace.opMu.Unlock()
	if workspace.manager == nil {
		manager, openErr := newRetainedSQLiteManager(workspace.workspace)
		if openErr != nil {
			return nil, mapRuntimeStateBrokerError(openErr)
		}
		manager.storeID = storeID
		workspace.manager = manager
	}
	manager := workspace.manager
	switch request.Operation {
	case runtimeStateOperationPreflight:
		return runtimeStateMutationResponse{}, nil
	case runtimeStateOperationSnapshot:
		var input runtimeStateTarget
		if request.DecodePayload(&input) != nil || input.StoreID != storeID {
			return nil, database.NewError(database.CodeInvalid, "runtime-state request is invalid")
		}
		value, err := manager.snapshotContext(ctx)
		if err != nil {
			return nil, mapRuntimeStateBrokerError(err)
		}
		return runtimeStateSnapshotResponse{State: value}, nil
	case runtimeStateOperationSetLastChannel, runtimeStateOperationSetLastChatID:
		var input runtimeStateSetRequest
		if request.DecodePayload(&input) != nil || input.StoreID != storeID ||
			validateRuntimeStateValue(input.Value) != nil {
			return nil, database.NewError(database.CodeInvalid, "runtime-state request is invalid")
		}
		field := "last_channel"
		if request.Operation == runtimeStateOperationSetLastChatID {
			field = "last_chat_id"
		}
		if err := manager.updateValue(ctx, field, input.Value); err != nil {
			return nil, mapRuntimeStateBrokerError(err)
		}
		return runtimeStateMutationResponse{Updated: true}, nil
	default:
		return nil, database.NewError(database.CodeUnsupported, "runtime-state operation is unsupported")
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
		for _, workspace := range handler.workspaces {
			if workspace.manager != nil {
				handler.closeErr = errors.Join(handler.closeErr, workspace.manager.Close())
			}
		}
	})
	return handler.closeErr
}

type configuredRuntimeWorkspace struct {
	workspace string
	selector  string
	storeID   database.StoreID
}

func configuredRuntimeWorkspaces(home string, cfg *config.Config) ([]configuredRuntimeWorkspace, error) {
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	primary, err := resolveRuntimeWorkspace(canonicalHome, cfg.Agents.Defaults.Workspace)
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
		workspace, resolveErr := resolveRuntimeWorkspace(canonicalHome, agent.Workspace)
		if resolveErr != nil {
			return nil, resolveErr
		}
		inputs = append(inputs, input{workspace: workspace})
	}
	seen := make(map[string]struct{}, len(inputs))
	selectors := make(map[string]string, len(inputs))
	result := make([]configuredRuntimeWorkspace, 0, len(inputs))
	for _, item := range inputs {
		if _, duplicate := seen[item.workspace]; duplicate {
			continue
		}
		seen[item.workspace] = struct{}{}
		selector, selectorErr := runtimeWorkspaceSelector(item.workspace)
		if selectorErr != nil {
			return nil, selectorErr
		}
		if previous, collision := selectors[selector]; collision && previous != item.workspace {
			return nil, database.NewError(database.CodeIntegrity, "runtime-state selector collides")
		}
		selectors[selector] = item.workspace
		storeID := RuntimeStateStoreID
		if !item.primary {
			storeID, selectorErr = database.ParseStoreID("workspace." + selector + ".runtime-state")
			if selectorErr != nil {
				return nil, selectorErr
			}
		}
		result = append(result, configuredRuntimeWorkspace{
			workspace: item.workspace, selector: selector, storeID: storeID,
		})
	}
	return result, nil
}

func resolveRuntimeWorkspace(home, configured string) (string, error) {
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
	abs, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("resolve runtime-state workspace: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	return abs, nil
}

func runtimeWorkspaceSelector(workspace string) (string, error) {
	value := strings.TrimSpace(workspace)
	if value == "" || strings.ContainsRune(value, 0) {
		return "", database.NewError(database.CodeInvalid, "runtime-state workspace is invalid")
	}
	abs, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "runtime-state workspace is invalid")
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	digest := sha256.Sum256([]byte(filepath.Clean(abs)))
	return hex.EncodeToString(digest[:8]), nil
}

func runtimeRequestStoreID(request database.Request) (database.StoreID, error) {
	var header struct {
		StoreID database.StoreID `json:"store_id"`
	}
	if json.Unmarshal(request.Payload, &header) != nil || !header.StoreID.Valid() {
		return "", database.NewError(database.CodeInvalid, "runtime-state request is invalid")
	}
	return header.StoreID, nil
}

func mapRuntimeStateBrokerError(err error) error {
	if err == nil {
		return nil
	}
	var structured *database.Error
	if errors.As(err, &structured) {
		return database.NewError(structured.Code, structured.Message)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return database.NewError(database.CodeDeadline, "runtime-state request deadline was exceeded")
	case errors.Is(err, errRuntimeStateVersionChanged):
		return database.NewError(database.CodeConflict, "runtime-state changed concurrently")
	case errors.Is(err, sqlitestore.ErrTooNew):
		return database.NewError(database.CodeUnsupported, "runtime-state schema is newer than supported")
	case errors.Is(err, sqlitestore.ErrInvalidSchema), errors.Is(err, sqlitestore.ErrIntegrity):
		return database.NewError(database.CodeIntegrity, "runtime-state integrity validation failed")
	case errors.Is(err, os.ErrPermission):
		return database.NewError(database.CodeUnavailable, "runtime-state store is unavailable")
	default:
		return database.NewError(database.CodeInternal, "runtime-state operation failed")
	}
}

var _ database.Handler = (*BrokerHandler)(nil)
