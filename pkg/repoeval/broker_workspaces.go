package repoeval

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

const evaluationOperationResolveStore = "resolve-store"

type evaluationResolveStoreRequest struct {
	WorkspaceSelector string `json:"workspace_selector"`
}

type evaluationResolveStoreResponse struct {
	StoreID database.StoreID `json:"store_id"`
}

type BrokerHandler struct {
	mu         sync.RWMutex
	workspaces map[database.StoreID]*evaluationStoreHandler
	selectors  map[string]database.StoreID
	closed     bool
	closeOnce  sync.Once
	closeErr   error
}

func NewBrokerHandler(home string, cfg *config.Config) (*BrokerHandler, error) {
	if !database.BrokerAuthorityHeld() && !database.ProviderTestAuthorityHeld() &&
		!allowUnfencedEvaluationProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"repository evaluation broker handler requires online database fencing",
		)
	}
	configured, err := configuredEvaluationWorkspaces(home, cfg)
	if err != nil {
		return nil, err
	}
	handler := &BrokerHandler{
		workspaces: make(map[database.StoreID]*evaluationStoreHandler, len(configured)),
		selectors:  make(map[string]database.StoreID, len(configured)),
	}
	for _, item := range configured {
		handler.workspaces[item.storeID] = newEvaluationStoreHandler(item.workspace, item.storeID)
		handler.selectors[item.selector] = item.storeID
	}
	return handler, nil
}

func (handler *BrokerHandler) Handle(ctx context.Context, request database.Request) (any, error) {
	if handler == nil || request.Domain != evaluationBrokerDomain || request.Version != evaluationBrokerVersion {
		return nil, database.NewError(database.CodeUnsupported, "database domain is unsupported")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, database.NewError(database.CodeDeadline, "evaluation request deadline was exceeded")
	}
	if request.Operation == evaluationOperationResolveStore {
		var input evaluationResolveStoreRequest
		if request.DecodePayload(&input) != nil || input.WorkspaceSelector == "" {
			return nil, database.NewError(database.CodeInvalid, "evaluation request is invalid")
		}
		handler.mu.RLock()
		storeID, ok := handler.selectors[input.WorkspaceSelector]
		closed := handler.closed
		handler.mu.RUnlock()
		if closed {
			return nil, database.NewError(database.CodeUnavailable, "evaluation broker is closed")
		}
		if !ok {
			return nil, database.NewError(database.CodeUnauthorized, "evaluation workspace is not cataloged")
		}
		return evaluationResolveStoreResponse{StoreID: storeID}, nil
	}
	storeID, err := evaluationRequestStoreID(request)
	if err != nil {
		return nil, err
	}
	handler.mu.RLock()
	workspace := handler.workspaces[storeID]
	closed := handler.closed
	handler.mu.RUnlock()
	if closed {
		return nil, database.NewError(database.CodeUnavailable, "evaluation broker is closed")
	}
	if workspace == nil {
		return nil, database.NewError(database.CodeUnauthorized, "evaluation store is not cataloged")
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
		workspaces := make([]*evaluationStoreHandler, 0, len(handler.workspaces))
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

func resolveEvaluationBrokerStoreID(
	ctx context.Context,
	client *database.Client,
	workspace string,
) (database.StoreID, error) {
	if client == nil {
		return "", database.NewError(database.CodeUnavailable, "evaluation broker is unavailable")
	}
	selector, err := evaluationWorkspaceSelector(workspace)
	if err != nil {
		return "", err
	}
	var response evaluationResolveStoreResponse
	if err := client.Call(
		ctx, evaluationBrokerDomain, evaluationBrokerVersion, evaluationOperationResolveStore,
		evaluationResolveStoreRequest{WorkspaceSelector: selector}, &response,
	); err != nil {
		return "", err
	}
	if !response.StoreID.Valid() {
		return "", database.NewError(database.CodeIntegrity, "evaluation StoreID is invalid")
	}
	return response.StoreID, nil
}

type configuredEvaluationWorkspace struct {
	workspace string
	selector  string
	storeID   database.StoreID
}

func configuredEvaluationWorkspaces(home string, cfg *config.Config) ([]configuredEvaluationWorkspace, error) {
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
	primary, err := resolveEvaluationWorkspace(canonicalHome, cfg.Agents.Defaults.Workspace)
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
		workspace, resolveErr := resolveEvaluationWorkspace(canonicalHome, agent.Workspace)
		if resolveErr != nil {
			return nil, resolveErr
		}
		inputs = append(inputs, input{workspace: workspace})
	}
	seen := make(map[string]struct{}, len(inputs))
	selectors := make(map[string]string, len(inputs))
	result := make([]configuredEvaluationWorkspace, 0, len(inputs))
	for _, item := range inputs {
		if _, duplicate := seen[item.workspace]; duplicate {
			continue
		}
		seen[item.workspace] = struct{}{}
		selector, selectorErr := evaluationWorkspaceSelector(item.workspace)
		if selectorErr != nil {
			return nil, selectorErr
		}
		if previous, collision := selectors[selector]; collision && previous != item.workspace {
			return nil, database.NewError(database.CodeIntegrity, "evaluation selector collides")
		}
		selectors[selector] = item.workspace
		storeID := EvaluationStoreID
		if !item.primary {
			storeID, selectorErr = database.ParseStoreID(
				"workspace." + selector + ".repository-evaluations",
			)
			if selectorErr != nil {
				return nil, selectorErr
			}
		}
		if !trusted.Contains(storeID) {
			return nil, database.NewError(database.CodeIntegrity, "evaluation store is absent from catalog")
		}
		result = append(result, configuredEvaluationWorkspace{
			workspace: item.workspace, selector: selector, storeID: storeID,
		})
	}
	return result, nil
}

func resolveEvaluationWorkspace(home, configured string) (string, error) {
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
		return "", fmt.Errorf("resolve evaluation workspace: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	return abs, nil
}

func evaluationWorkspaceSelector(workspace string) (string, error) {
	value := strings.TrimSpace(workspace)
	if value == "" || strings.ContainsRune(value, 0) {
		return "", database.NewError(database.CodeInvalid, "evaluation workspace is invalid")
	}
	abs, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", database.NewError(database.CodeInvalid, "evaluation workspace is invalid")
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	digest := sha256.Sum256([]byte(filepath.Clean(abs)))
	return hex.EncodeToString(digest[:8]), nil
}

func evaluationRequestStoreID(request database.Request) (database.StoreID, error) {
	var header struct {
		StoreID database.StoreID `json:"store_id"`
	}
	if json.Unmarshal(request.Payload, &header) != nil || !header.StoreID.Valid() {
		return "", database.NewError(database.CodeInvalid, "evaluation request is invalid")
	}
	return header.StoreID, nil
}

var _ database.Handler = (*BrokerHandler)(nil)
