package repoaudit

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

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	dbcatalog "github.com/sipeed/picoclaw/pkg/database/catalog"
)

const reviewOperationResolveStore = "resolve-store"

type reviewResolveStoreRequest struct {
	WorkspaceSelector string `json:"workspace_selector"`
}

type reviewResolveStoreResponse struct {
	StoreID database.StoreID `json:"store_id"`
}

// BrokerHandler routes only trusted configured workspaces. Each workspace owns
// an independent retained pool and lease namespace for the broker epoch.
type BrokerHandler struct {
	mu         sync.RWMutex
	workspaces map[database.StoreID]*reviewStoreHandler
	selectors  map[string]database.StoreID
	closed     bool
	closeOnce  sync.Once
	closeErr   error
}

func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedReviewProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"repository review broker handler requires online database fencing",
		)
	}
	configured, err := configuredReviewWorkspaces(home, cfg)
	if err != nil {
		return nil, err
	}
	handler := &BrokerHandler{
		workspaces: make(map[database.StoreID]*reviewStoreHandler, len(configured)),
		selectors:  make(map[string]database.StoreID, len(configured)),
	}
	for _, item := range configured {
		handler.workspaces[item.storeID] = newReviewStoreHandler(item.workspace, item.storeID)
		handler.selectors[item.selector] = item.storeID
	}
	return handler, nil
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != reviewBrokerDomain || request.Version != reviewBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, database.NewError(database.CodeDeadline, "repository review request deadline was exceeded")
	}
	if request.Operation == reviewOperationResolveStore {
		var input reviewResolveStoreRequest
		if request.DecodePayload(&input) != nil || input.WorkspaceSelector == "" {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		handler.mu.RLock()
		storeID, ok := handler.selectors[input.WorkspaceSelector]
		closed := handler.closed
		handler.mu.RUnlock()
		if closed {
			return nil, database.NewError(database.CodeUnavailable, "repository review broker is closed")
		}
		if !ok {
			return nil, database.NewError(database.CodeUnauthorized, "repository review workspace is not cataloged")
		}
		return reviewResolveStoreResponse{StoreID: storeID}, nil
	}
	storeID, err := reviewRequestStoreID(request)
	if err != nil {
		return nil, err
	}
	handler.mu.RLock()
	workspace := handler.workspaces[storeID]
	closed := handler.closed
	handler.mu.RUnlock()
	if closed {
		return nil, database.NewError(database.CodeUnavailable, "repository review broker is closed")
	}
	if workspace == nil {
		return nil, database.NewError(database.CodeUnauthorized, "repository review store is not cataloged")
	}
	return workspace.Handle(ctx, request)
}

func (handler *BrokerHandler) Close() error {
	if handler == nil {
		return nil
	}
	handler.closeOnce.Do(func() {
		handler.mu.Lock()
		handler.closed = true
		workspaces := make([]*reviewStoreHandler, 0, len(handler.workspaces))
		for _, workspace := range handler.workspaces {
			workspaces = append(workspaces, workspace)
		}
		handler.mu.Unlock()
		for _, workspace := range workspaces {
			handler.closeErr = errors.Join(handler.closeErr, workspace.Close())
		}
	})
	return handler.closeErr
}

func resolveReviewBrokerStoreID(
	ctx context.Context,
	client *database.Client,
	workspace string,
) (database.StoreID, error) {
	if client == nil {
		return "", database.NewError(database.CodeUnavailable, "repository review broker is unavailable")
	}
	selector, err := reviewWorkspaceSelector(workspace)
	if err != nil {
		return "", err
	}
	var response reviewResolveStoreResponse
	if err := client.Call(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationResolveStore,
		reviewResolveStoreRequest{WorkspaceSelector: selector}, &response,
	); err != nil {
		return "", err
	}
	if !response.StoreID.Valid() {
		return "", database.NewError(database.CodeIntegrity, "repository review StoreID is invalid")
	}
	return response.StoreID, nil
}

type configuredReviewWorkspace struct {
	workspace string
	selector  string
	storeID   database.StoreID
}

func configuredReviewWorkspaces(home string, cfg *config.Config) ([]configuredReviewWorkspace, error) {
	canonicalHome, err := database.CanonicalHome(home)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	trusted, err := dbcatalog.New(canonicalHome, cfg)
	if err != nil {
		return nil, err
	}
	primary, err := resolveReviewWorkspace(canonicalHome, cfg.Agents.Defaults.Workspace)
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
		workspace, resolveErr := resolveReviewWorkspace(canonicalHome, agent.Workspace)
		if resolveErr != nil {
			return nil, resolveErr
		}
		inputs = append(inputs, input{workspace: workspace})
	}
	seen := make(map[string]struct{}, len(inputs))
	selectors := make(map[string]string, len(inputs))
	result := make([]configuredReviewWorkspace, 0, len(inputs))
	for _, item := range inputs {
		if _, duplicate := seen[item.workspace]; duplicate {
			continue
		}
		seen[item.workspace] = struct{}{}
		selector, selectorErr := reviewWorkspaceSelector(item.workspace)
		if selectorErr != nil {
			return nil, selectorErr
		}
		if previous, collision := selectors[selector]; collision && previous != item.workspace {
			return nil, database.NewError(database.CodeIntegrity, "repository review selector collides")
		}
		selectors[selector] = item.workspace
		storeID := ReviewStoreID
		if !item.primary {
			storeID, selectorErr = database.ParseStoreID(
				"workspace." + selector + ".repository-reviews",
			)
			if selectorErr != nil {
				return nil, selectorErr
			}
		}
		if !trusted.Contains(storeID) {
			return nil, database.NewError(database.CodeIntegrity, "repository review store is absent from catalog")
		}
		result = append(result, configuredReviewWorkspace{
			workspace: item.workspace, selector: selector, storeID: storeID,
		})
	}
	return result, nil
}

func resolveReviewWorkspace(home, configured string) (string, error) {
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
		return "", fmt.Errorf("resolve repository review workspace: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	return abs, nil
}

func reviewWorkspaceSelector(workspace string) (string, error) {
	value := strings.TrimSpace(workspace)
	if value == "" || strings.ContainsRune(value, 0) {
		return "", database.NewError(database.CodeInvalid, "repository review workspace is invalid")
	}
	abs, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "repository review workspace is invalid")
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	digest := sha256.Sum256([]byte(filepath.Clean(abs)))
	return hex.EncodeToString(digest[:8]), nil
}

func reviewRequestStoreID(request database.Request) (database.StoreID, error) {
	var header struct {
		StoreID database.StoreID `json:"store_id"`
	}
	if json.Unmarshal(request.Payload, &header) != nil || !header.StoreID.Valid() {
		return "", database.NewError(database.CodeInvalid, "repository review request is invalid")
	}
	return header.StoreID, nil
}

var _ database.Handler = (*BrokerHandler)(nil)
